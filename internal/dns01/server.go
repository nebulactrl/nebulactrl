package dns01

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Staged change markers of a resolver or wildcard certificate.
const (
	ChangeNone   = "none"
	ChangeAdd    = "add"
	ChangeUpdate = "update"
	ChangeRemove = "remove"
)

// DNSResolverView is the API view of a server's DNS-01 resolver of one provider.
type DNSResolverView struct {
	Provider       string    `json:"provider" doc:"The lego provider code"`
	ProviderName   string    `json:"providerName"`
	CredentialID   uuid.UUID `json:"credentialId"`
	CredentialName string    `json:"credentialName"`
	DelaySeconds   int       `json:"delaySeconds" doc:"Wait this long before checking the TXT record has propagated"`
	Staging        bool      `json:"staging" doc:"Use the Let's Encrypt staging CA: certificates are not trusted by browsers"`
	Change         string    `json:"change" enum:"none,add,update,remove" doc:"Staged change awaiting Deploy proxy"`
	Rotated        bool      `json:"rotated" doc:"The credential's values changed since the proxy applied it"`
	Automatic      bool      `json:"automatic" doc:"NebulaCtrl added this resolver itself: the server serves names in a zone of the connected Cloudflare account, and the connection's token is the credential. It follows the connection and cannot be edited or removed here"`
}

// WildcardCertView is the API view of a wildcard certificate.
type WildcardCertView struct {
	ID             uuid.UUID  `json:"id"`
	Domain         string     `json:"domain" doc:"Base domain: the certificate covers *.<domain>"`
	Names          []string   `json:"names"`
	IncludeApex    bool       `json:"includeApex"`
	Provider       string     `json:"provider" doc:"The lego provider code"`
	CoverGenerated bool       `json:"coverGenerated" doc:"Generated domains under the base domain use this certificate"`
	Default        bool       `json:"default" doc:"The proxy's default certificate (defaultGeneratedCert)"`
	Status         string     `json:"status" enum:"pending,active,error"`
	StatusMessage  string     `json:"statusMessage"`
	Issuer         string     `json:"issuer"`
	NotAfter       *time.Time `json:"notAfter"`
	DaysToExpiry   *int       `json:"daysToExpiry"`
	CheckedAt      *time.Time `json:"checkedAt"`
	Change         string     `json:"change" enum:"none,add,update,remove" doc:"Staged change awaiting Deploy proxy"`
}

// ServerDNS01 is a server's DNS-01 configuration.
type ServerDNS01 struct {
	Resolvers []DNSResolverView  `json:"resolvers"`
	Wildcards []WildcardCertView `json:"wildcards"`
	// ACMEEmail is the server's ACME contact address; DNS-01 needs one.
	ACMEEmail  string `json:"acmeEmail"`
	BaseDomain string `json:"baseDomain" doc:"The domain generated hostnames live under (the server's base domain)"`
	Staged     bool   `json:"staged" doc:"Staged DNS-01 changes await Deploy proxy"`
}

func daysUntil(t, now time.Time) int { return int(t.Sub(now).Hours() / 24) }

// GetServerState returns a server's DNS-01 configuration: desired state, with
// staged additions, updates and removals marked.
func (s *Service) GetServerState(ctx context.Context, serverID uuid.UUID) (ServerDNS01, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeServersRead, serverID)
	if err != nil {
		return ServerDNS01{}, err
	}
	return s.state(ctx, s.q, srv, time.Now())
}

func (s *Service) state(ctx context.Context, q *db.Queries, srv db.Server, now time.Time) (ServerDNS01, error) {
	desired, err := loadDesired(ctx, q, srv)
	if err != nil {
		return ServerDNS01{}, err
	}
	applied := AppliedSnapshot(srv)
	st := ServerDNS01{Resolvers: []DNSResolverView{}, Wildcards: []WildcardCertView{}, ACMEEmail: derefStr(srv.AcmeEmail)}
	if base, ok := baseDomainOf(srv); ok {
		st.BaseDomain = base
	}

	appliedRes := map[string]agentprotocol.DNSResolver{}
	for _, r := range applied.Resolvers {
		appliedRes[r.Provider] = r
	}
	seen := map[string]bool{}
	for _, r := range desired.resolvers {
		spec, _ := agentprotocol.DNSProvider(r.Provider)
		v := DNSResolverView{
			Provider: r.Provider, ProviderName: spec.Name, CredentialID: r.CredentialID, CredentialName: r.CredentialName,
			DelaySeconds: int(r.DelaySeconds), Staging: r.Staging, Change: ChangeAdd, Automatic: r.Auto,
		}
		if a, ok := appliedRes[r.Provider]; ok {
			seen[r.Provider] = true
			v.Change = ChangeNone
			want := desired.resolverSpec(r)
			if a.ID != want.ID || a.DelaySeconds != want.DelaySeconds || a.CAServer != want.CAServer {
				v.Change = ChangeUpdate
			}
			if a.Rev != want.Rev && a.ID == want.ID {
				v.Change, v.Rotated = ChangeUpdate, true
			}
		}
		if v.Change != ChangeNone {
			st.Staged = true
		}
		st.Resolvers = append(st.Resolvers, v)
	}
	for _, a := range applied.Resolvers {
		if seen[a.Provider] || desired.hasResolver(a.Provider) {
			continue
		}
		spec, _ := agentprotocol.DNSProvider(a.Provider)
		st.Resolvers = append(st.Resolvers, DNSResolverView{Provider: a.Provider, ProviderName: spec.Name, DelaySeconds: a.DelaySeconds, Staging: a.CAServer != "", Change: ChangeRemove})
		st.Staged = true
	}

	appliedWild := map[uuid.UUID]agentprotocol.WildcardCert{}
	for _, w := range applied.Wildcards {
		if id, err := uuid.Parse(w.ID); err == nil {
			appliedWild[id] = w
		}
	}
	liveWild := map[uuid.UUID]bool{}
	for _, w := range desired.wildcards {
		liveWild[w.ID] = true
		v := wildcardView(w, now)
		v.Change = ChangeAdd
		if a, ok := appliedWild[w.ID]; ok {
			v.Change = ChangeNone
			if a != desired.wildcardSpec(w) {
				v.Change = ChangeUpdate
			}
		}
		if v.Change != ChangeNone {
			st.Staged = true
		}
		st.Wildcards = append(st.Wildcards, v)
	}
	for id, a := range appliedWild {
		if liveWild[id] {
			continue
		}
		spec := a
		st.Wildcards = append(st.Wildcards, WildcardCertView{
			ID: id, Domain: spec.Domain, Names: spec.Names(), IncludeApex: spec.IncludeApex, Provider: spec.Provider,
			CoverGenerated: spec.CoverGenerated, Default: spec.Default, Status: agentprotocol.AcmeStatusActive, Change: ChangeRemove,
		})
		st.Staged = true
	}
	return st, nil
}

func wildcardView(w db.WildcardCert, now time.Time) WildcardCertView {
	spec := agentprotocol.WildcardCert{Domain: w.Domain, IncludeApex: w.IncludeApex}
	v := WildcardCertView{
		ID: w.ID, Domain: w.Domain, Names: spec.Names(), IncludeApex: w.IncludeApex, Provider: w.Provider,
		CoverGenerated: w.CoverGenerated, Default: w.IsDefault, Status: w.Status, StatusMessage: w.StatusMessage,
		Issuer: w.Issuer, NotAfter: w.NotAfter, CheckedAt: w.CheckedAt, Change: ChangeNone,
	}
	if w.NotAfter != nil {
		d := daysUntil(*w.NotAfter, now)
		v.DaysToExpiry = &d
	}
	return v
}

// ResolverInput sets a server's resolver of one provider.
type ResolverInput struct {
	CredentialID uuid.UUID
	DelaySeconds int
	Staging      bool
}

// SetResolver stages the resolver of provider on a server (create or replace).
func (s *Service) SetResolver(ctx context.Context, serverID uuid.UUID, provider string, in ResolverInput) (ServerDNS01, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ServerDNS01{}, err
	}
	if err := RequireDNS01Provider(srv, provider); err != nil {
		return ServerDNS01{}, err
	}
	spec, ok := agentprotocol.DNSProvider(provider)
	if !ok {
		return ServerDNS01{}, providerError(provider)
	}
	if in.DelaySeconds < 0 || in.DelaySeconds > agentprotocol.MaxDNSDelaySeconds {
		return ServerDNS01{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("delaySeconds must be between 0 and %d", agentprotocol.MaxDNSDelaySeconds))
	}
	if derefStr(srv.AcmeEmail) == "" {
		return ServerDNS01{}, apierr.Unprocessable("acme_email_required", "set an ACME email on the server first: Let's Encrypt needs a contact address for DNS-01")
	}
	cred, err := s.q.GetDNSCredential(ctx, db.GetDNSCredentialParams{ID: in.CredentialID, WorkspaceID: srv.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerDNS01{}, apierr.Unprocessable("invalid_credential", "the credential does not exist")
	}
	if err != nil {
		return ServerDNS01{}, fmt.Errorf("get dns credential: %w", err)
	}
	if cred.Provider != provider {
		return ServerDNS01{}, apierr.Unprocessable("invalid_credential", fmt.Sprintf("the credential is for %s, not %s", cred.Provider, spec.Name))
	}
	if cred.ServerID != nil && *cred.ServerID != serverID {
		return ServerDNS01{}, apierr.Unprocessable("invalid_credential", "the credential is scoped to another server")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID}); err != nil {
			return err
		}
		// The proxy container has one environment: another provider's resolver
		// must not set a variable this credential sets.
		others, err := q.ListServerDNSResolvers(ctx, serverID)
		if err != nil {
			return err
		}
		for _, o := range others {
			if o.Provider == provider {
				continue
			}
			for _, n := range cred.EnvNames {
				if slices.Contains(o.EnvNames, n) {
					return apierr.Unprocessable("dns_variable_clash", fmt.Sprintf("the %s resolver already sets %s; the proxy container has one environment, so two providers cannot share a variable", o.Provider, n))
				}
			}
		}
		if err := q.UpsertServerDNSResolver(ctx, db.UpsertServerDNSResolverParams{
			ServerID: serverID, Provider: provider, CredentialID: in.CredentialID, DelaySeconds: int32(in.DelaySeconds), Staging: in.Staging,
		}); err != nil {
			return err
		}
		if err := q.BumpServerProxyGeneration(ctx, serverID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.dns01.resolver.set", ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{"provider": provider, "credential": cred.Name, "delaySeconds": in.DelaySeconds, "staging": in.Staging},
		})
	})
	if err != nil {
		return ServerDNS01{}, fmt.Errorf("set dns resolver: %w", err)
	}
	s.publish(serverID)
	return s.state(ctx, s.q, srv, time.Now())
}

// DeleteResolver stages the removal of a server's resolver. Refused while a
// wildcard certificate or a domain of a service on the server uses it.
func (s *Service) DeleteResolver(ctx context.Context, serverID uuid.UUID, provider string) (ServerDNS01, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ServerDNS01{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID}); err != nil {
			return err
		}
		if n, err := q.CountServerWildcardsForProvider(ctx, db.CountServerWildcardsForProviderParams{ServerID: serverID, Provider: provider}); err != nil {
			return err
		} else if n > 0 {
			return apierr.Conflict("resolver_in_use", fmt.Sprintf("%d wildcard certificate(s) use this resolver; remove them first", n))
		}
		if n, err := q.CountServerDomainsForProvider(ctx, db.CountServerDomainsForProviderParams{ServerID: serverID, DnsResolver: provider}); err != nil {
			return err
		} else if n > 0 {
			return apierr.Conflict("resolver_in_use", fmt.Sprintf("%d domain(s) of services on this server use this resolver; switch them to automatic first", n))
		}
		n, err := q.DeleteServerDNSResolver(ctx, db.DeleteServerDNSResolverParams{ServerID: serverID, Provider: provider})
		if err != nil {
			return err
		}
		if n == 0 {
			return apierr.NotFound("resolver")
		}
		if err := q.BumpServerProxyGeneration(ctx, serverID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.dns01.resolver.remove", ResourceType: "server", ResourceID: serverID.String(), Metadata: map[string]any{"provider": provider},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ServerDNS01{}, err
		}
		return ServerDNS01{}, fmt.Errorf("delete dns resolver: %w", err)
	}
	s.publish(serverID)
	return s.state(ctx, s.q, srv, time.Now())
}

// WildcardInput creates or updates a wildcard certificate.
type WildcardInput struct {
	Domain         string
	Provider       string
	IncludeApex    bool
	CoverGenerated bool
	Default        bool
}

func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "*.")
	return strings.TrimSuffix(d, ".")
}

// CreateWildcard stages a wildcard certificate for *.<domain> on a server. The
// provider's resolver must already be set.
func (s *Service) CreateWildcard(ctx context.Context, serverID uuid.UUID, in WildcardInput) (ServerDNS01, error) {
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, serverID)
	if err != nil {
		return ServerDNS01{}, err
	}
	if err := RequireDNS01Provider(srv, in.Provider); err != nil {
		return ServerDNS01{}, err
	}
	domain := normalizeDomain(in.Domain)
	if !agentprotocol.ValidWildcardBase(domain) {
		return ServerDNS01{}, apierr.Unprocessable("invalid_hostname", "domain must be a base domain such as example.com (the certificate covers *.example.com)")
	}
	if _, ok := agentprotocol.DNSProvider(in.Provider); !ok {
		return ServerDNS01{}, providerError(in.Provider)
	}
	var row db.WildcardCert
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: serverID, WorkspaceID: srv.WorkspaceID}); err != nil {
			return err
		}
		if err := requireResolver(ctx, q, serverID, in.Provider); err != nil {
			return err
		}
		if in.Default {
			// One default per server: the new one takes over.
			if err := q.ClearDefaultWildcardCert(ctx, db.ClearDefaultWildcardCertParams{ServerID: serverID, ID: uuid.Nil}); err != nil {
				return err
			}
		}
		var err error
		row, err = q.InsertWildcardCert(ctx, db.InsertWildcardCertParams{
			WorkspaceID: srv.WorkspaceID, ServerID: serverID, Domain: domain, IncludeApex: in.IncludeApex, Provider: in.Provider,
			CoverGenerated: in.CoverGenerated, IsDefault: in.Default,
		})
		if err != nil {
			return err
		}
		if err := q.BumpServerProxyGeneration(ctx, serverID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "wildcard_cert.create", ResourceType: "wildcard_cert", ResourceID: row.ID.String(),
			Metadata: map[string]any{"serverId": serverID.String(), "domain": domain, "provider": in.Provider, "coverGenerated": in.CoverGenerated, "default": in.Default},
		})
	})
	if isUnique(err) {
		return ServerDNS01{}, apierr.Conflict("wildcard_exists", fmt.Sprintf("*.%s is already configured on this server", domain))
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ServerDNS01{}, err
		}
		return ServerDNS01{}, fmt.Errorf("create wildcard certificate: %w", err)
	}
	s.publish(serverID)
	return s.state(ctx, s.q, srv, time.Now())
}

func requireResolver(ctx context.Context, q *db.Queries, serverID uuid.UUID, provider string) error {
	rs, err := q.ListServerDNSResolvers(ctx, serverID)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.Provider == provider {
			return nil
		}
	}
	spec, _ := agentprotocol.DNSProvider(provider)
	return apierr.Unprocessable("resolver_not_configured", "add a "+spec.Name+" DNS-01 resolver to this server first")
}

// WildcardPatch changes a wildcard certificate's options. The domain and the
// provider cannot change: remove it and add another.
type WildcardPatch struct {
	IncludeApex    *bool
	CoverGenerated *bool
	Default        *bool
}

// UpdateWildcard stages a change of a wildcard certificate's options.
func (s *Service) UpdateWildcard(ctx context.Context, id uuid.UUID, in WildcardPatch) (ServerDNS01, error) {
	if err := policy.Require(ctx, policy.ScopeProxyWrite, nil); err != nil {
		return ServerDNS01{}, err
	}
	ws := policy.WorkspaceID(ctx)
	cur, err := s.q.GetWildcardCert(ctx, db.GetWildcardCertParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerDNS01{}, apierr.NotFound("wildcard certificate")
	}
	if err != nil {
		return ServerDNS01{}, fmt.Errorf("get wildcard certificate: %w", err)
	}
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, cur.ServerID)
	if err != nil {
		return ServerDNS01{}, err
	}
	apex, cover, def := cur.IncludeApex, cur.CoverGenerated, cur.IsDefault
	if in.IncludeApex != nil {
		apex = *in.IncludeApex
	}
	if in.CoverGenerated != nil {
		cover = *in.CoverGenerated
	}
	if in.Default != nil {
		def = *in.Default
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: srv.ID, WorkspaceID: ws}); err != nil {
			return err
		}
		if def {
			if err := q.ClearDefaultWildcardCert(ctx, db.ClearDefaultWildcardCertParams{ServerID: srv.ID, ID: id}); err != nil {
				return err
			}
		}
		if _, err := q.UpdateWildcardCert(ctx, db.UpdateWildcardCertParams{
			ID: id, WorkspaceID: ws, IncludeApex: apex, Provider: cur.Provider, CoverGenerated: cover, IsDefault: def,
		}); err != nil {
			return err
		}
		if err := q.BumpServerProxyGeneration(ctx, srv.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "wildcard_cert.update", ResourceType: "wildcard_cert", ResourceID: id.String(),
			Metadata: map[string]any{"serverId": srv.ID.String(), "domain": cur.Domain, "includeApex": apex, "coverGenerated": cover, "default": def},
		})
	})
	if err != nil {
		return ServerDNS01{}, fmt.Errorf("update wildcard certificate: %w", err)
	}
	s.publish(srv.ID)
	return s.state(ctx, s.q, srv, time.Now())
}

// DeleteWildcard stages the removal of a wildcard certificate.
func (s *Service) DeleteWildcard(ctx context.Context, id uuid.UUID) (ServerDNS01, error) {
	if err := policy.Require(ctx, policy.ScopeProxyWrite, nil); err != nil {
		return ServerDNS01{}, err
	}
	ws := policy.WorkspaceID(ctx)
	cur, err := s.q.GetWildcardCert(ctx, db.GetWildcardCertParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerDNS01{}, apierr.NotFound("wildcard certificate")
	}
	if err != nil {
		return ServerDNS01{}, fmt.Errorf("get wildcard certificate: %w", err)
	}
	srv, err := s.loadServer(ctx, s.q, policy.ScopeProxyWrite, cur.ServerID)
	if err != nil {
		return ServerDNS01{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockServerForProxy(ctx, db.LockServerForProxyParams{ID: srv.ID, WorkspaceID: ws}); err != nil {
			return err
		}
		if _, err := q.DeleteWildcardCert(ctx, db.DeleteWildcardCertParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		if err := q.BumpServerProxyGeneration(ctx, srv.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "wildcard_cert.delete", ResourceType: "wildcard_cert", ResourceID: id.String(),
			Metadata: map[string]any{"serverId": srv.ID.String(), "domain": cur.Domain},
		})
	})
	if err != nil {
		return ServerDNS01{}, fmt.Errorf("delete wildcard certificate: %w", err)
	}
	s.publish(srv.ID)
	return s.state(ctx, s.q, srv, time.Now())
}

// ---------------------------------------------------------------------------
// Desired and applied configuration (consumed by the proxy server service).

type desiredRow struct {
	Provider       string
	CredentialID   uuid.UUID
	CredentialName string
	DelaySeconds   int32
	Staging        bool
	EnvNames       []string
	Rev            int32
	// Auto marks the automatic Cloudflare resolver (cloudflare_auto.go): its
	// credential is the team's Cloudflare connection, not a stored credential.
	Auto      bool
	Workspace uuid.UUID
}

type desired struct {
	resolvers []desiredRow
	wildcards []db.WildcardCert
}

// loadDesired reads the server's desired DNS-01 configuration: the resolvers
// and wildcard certificates staged on it plus the automatic Cloudflare resolver
// when the server serves a name in a connected zone.
func loadDesired(ctx context.Context, q *db.Queries, srv db.Server) (desired, error) {
	rs, err := q.ListServerDNSResolvers(ctx, srv.ID)
	if err != nil {
		return desired{}, fmt.Errorf("list dns resolvers: %w", err)
	}
	ws, err := q.ListServerWildcardCerts(ctx, srv.ID)
	if err != nil {
		return desired{}, fmt.Errorf("list wildcard certificates: %w", err)
	}
	d := desired{wildcards: ws}
	for _, r := range rs {
		d.resolvers = append(d.resolvers, desiredRow{
			Provider: r.Provider, CredentialID: r.CredentialID, CredentialName: r.CredentialName, DelaySeconds: r.DelaySeconds,
			Staging: r.Staging, EnvNames: r.EnvNames, Rev: r.Rev,
		})
	}
	auto, ok, err := autoRow(ctx, q, srv, d)
	if err != nil {
		return desired{}, err
	}
	if ok {
		d.resolvers = append(d.resolvers, auto)
		sortResolvers(d.resolvers)
	}
	return d, nil
}

func (d desired) hasResolver(provider string) bool {
	for _, r := range d.resolvers {
		if r.Provider == provider {
			return true
		}
	}
	return false
}

func (d desired) resolverSpec(r desiredRow) agentprotocol.DNSResolver {
	id := r.CredentialID.String()
	if r.Auto {
		id = AutoResolverID(r.Workspace)
	}
	out := agentprotocol.DNSResolver{
		ID: id, Provider: r.Provider, DelaySeconds: int(r.DelaySeconds), EnvNames: nonNil(r.EnvNames), Rev: int(r.Rev),
	}
	if r.Staging {
		out.CAServer = agentprotocol.LetsEncryptStagingCA
	}
	return out
}

func (d desired) wildcardSpec(w db.WildcardCert) agentprotocol.WildcardCert {
	return agentprotocol.WildcardCert{
		ID: w.ID.String(), Provider: w.Provider, Domain: w.Domain, IncludeApex: w.IncludeApex,
		CoverGenerated: w.CoverGenerated, Default: w.IsDefault,
	}
}

// Snapshot is the DNS-01 part of a proxy configuration. It carries no value:
// resolvers list variable names, the values travel as send-time secrets.
type Snapshot struct {
	Resolvers []agentprotocol.DNSResolver  `json:"resolvers"`
	Wildcards []agentprotocol.WildcardCert `json:"wildcards"`
}

// AppliedSnapshot decodes servers.applied_dns01: what the last successful
// proxy_configure applied.
func AppliedSnapshot(srv db.Server) Snapshot {
	var s Snapshot
	if len(srv.AppliedDns01) > 0 {
		_ = json.Unmarshal(srv.AppliedDns01, &s)
	}
	return s
}

// Desired builds the DESIRED DNS-01 configuration of a server (staged changes
// included).
func Desired(ctx context.Context, q *db.Queries, serverID uuid.UUID) (Snapshot, error) {
	srv, err := q.GetServerByID(ctx, serverID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load server: %w", err)
	}
	d, err := loadDesired(ctx, q, srv)
	if err != nil {
		return Snapshot{}, err
	}
	var out Snapshot
	for _, r := range d.resolvers {
		out.Resolvers = append(out.Resolvers, d.resolverSpec(r))
	}
	for _, w := range d.wildcards {
		out.Wildcards = append(out.Wildcards, d.wildcardSpec(w))
	}
	return out, nil
}

// Apply sets the DNS-01 fields of pc from snap.
func (snap Snapshot) Apply(pc *agentprotocol.ProxyConfig) {
	pc.DNSResolvers = snap.Resolvers
	pc.WildcardCerts = snap.Wildcards
}

// SnapshotOf extracts the DNS-01 part of a proxy configuration (the payload of
// a successful proxy_configure) for servers.applied_dns01.
func SnapshotOf(pc agentprotocol.ProxyConfig) json.RawMessage {
	raw, _ := json.Marshal(Snapshot{Resolvers: nonNil(pc.DNSResolvers), Wildcards: nonNil(pc.WildcardCerts)})
	return raw
}

// Secrets returns the send-time secrets of pc's DNS-01 resolvers: one entry per
// credential variable, keyed by agentprotocol.DNSSecretKey. Only credentials
// the server may use (its team's, team-wide or scoped to it) are read; the
// automatic Cloudflare resolver's is the team's Cloudflare token. Never persist
// the result.
func Secrets(ctx context.Context, q *db.Queries, box *secretbox.Box, serverID uuid.UUID, pc *agentprotocol.ProxyConfig) (map[string]string, error) {
	out := map[string]string{}
	if pc == nil {
		return out, nil
	}
	for _, r := range pc.DNSResolvers {
		if _, auto := autoWorkspace(r.ID); auto {
			if err := autoSecrets(ctx, q, box, serverID, r, out); err != nil {
				return nil, err
			}
			continue
		}
		id, err := uuid.Parse(r.ID)
		if err != nil {
			return nil, fmt.Errorf("dns credential id %q: %w", r.ID, err)
		}
		row, err := q.GetDNSCredentialForServer(ctx, db.GetDNSCredentialForServerParams{ID: id, ServerID: serverID})
		if err != nil {
			return nil, fmt.Errorf("load dns credential %s: %w", r.ID, err)
		}
		values, err := openValues(box, row.ID, row.SecretsEnc)
		if err != nil {
			return nil, err
		}
		for _, name := range r.EnvNames {
			v, ok := values[name]
			if !ok {
				return nil, fmt.Errorf("dns credential %s lacks %s", r.ID, name)
			}
			out[agentprotocol.DNSSecretKey(r.ID, name)] = v
		}
	}
	return out, nil
}

func baseDomainOf(srv db.Server) (string, bool) {
	if srv.BaseDomain == nil {
		return "", false
	}
	b := strings.Trim(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(*srv.BaseDomain)), "*."), ".")
	return b, b != ""
}
