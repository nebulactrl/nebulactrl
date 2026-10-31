package dns01

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/cfzone"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Automatic Cloudflare DNS-01.
//
// A hostname in a zone of the team's connected Cloudflare account gets its
// certificate through the DNS-01 challenge, with the Cloudflare token of the
// connection, and needs no setup: the rule's resolver is "cloudflare" and the
// server runs a resolver of that provider whose credential is the connection's
// token (ID "cf-<workspace id>", see AutoResolverID). Nothing of it is stored:
// it is derived from the zone list cached on the connection and from the name's
// own DNS sync (Evidence: a sync wrote its record with the token, which is all
// that shows the token may edit the zone), so it follows the connection (a new
// zone, a rotated token, a removed connection) and the sync.
//
// The pieces: CloudflareAccess says per hostname whether DNS-01 can be used and,
// if not, why (Choose); ApplyAuto sets the resolver on route rules; loadDesired
// adds the resolver to a server that serves such a name; Secrets delivers the
// token with the command; DomainCerts reports it.
//
// A resolver is static proxy configuration, so it reaches a server with the next
// Deploy proxy like every other resolver. Until then the agent keeps serving the
// name over HTTP-01 (a rule that names a provider the proxy does not run uses the
// HTTP-01 resolver), so a name never loses its certificate source on the way. A
// certificate Traefik already holds for a name stays in use: it is served from the
// default store whichever resolver the router names, and Traefik asks a
// resolver for a certificate only when the store has none for the name.

const (
	// AutoProvider is the DNS-01 provider of the automatic choice.
	AutoProvider = agentprotocol.DNSCloudflare
	// autoTokenEnv is the variable lego's Cloudflare provider reads the token from.
	autoTokenEnv = "CF_DNS_API_TOKEN"
	autoIDPrefix = "cf-"
	// AutoCredentialName names the automatic resolver's credential in plans and views.
	AutoCredentialName = "Cloudflare connection (automatic)"
)

// AutoResolverID is the resolver ID (the Secrets key part) of the automatic
// Cloudflare resolver of a team. It is not a UUID, so it can never be mistaken
// for a stored credential.
func AutoResolverID(ws uuid.UUID) string { return autoIDPrefix + ws.String() }

// IsAutoResolver reports whether r is the automatic Cloudflare resolver (not a
// resolver a person set up).
func IsAutoResolver(r agentprotocol.DNSResolver) bool {
	_, ok := autoWorkspace(r.ID)
	return ok
}

func autoWorkspace(resolverID string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(resolverID, autoIDPrefix)
	if !ok {
		return uuid.Nil, false
	}
	ws, err := uuid.Parse(rest)
	return ws, err == nil
}

// CloudflareAccess is what a team's Cloudflare connection allows: the zones its
// token sees and, per zone, whether it may edit DNS records.
type CloudflareAccess struct {
	WorkspaceID uuid.UUID
	// Connected is false when the team has no connection.
	Connected bool
	// Status is the connection's state: "ok", or "error" after Cloudflare
	// rejected the token.
	Status string
	Zones  []cfzone.Zone
	// TokenRev changes when the token is replaced (it feeds DNSResolver.Rev).
	TokenRev int32
}

// LoadCloudflare reads the team's connection. A team without one yields a
// CloudflareAccess with Connected false.
func LoadCloudflare(ctx context.Context, q *db.Queries, ws uuid.UUID) (CloudflareAccess, error) {
	row, err := q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return CloudflareAccess{WorkspaceID: ws}, nil
	}
	if err != nil {
		return CloudflareAccess{}, fmt.Errorf("get cloudflare connection: %w", err)
	}
	h := fnv.New32a()
	_, _ = h.Write(row.TokenEnc)
	return CloudflareAccess{
		WorkspaceID: ws, Connected: true, Status: row.Status, Zones: cfzone.Decode(row.Zones),
		TokenRev: int32(h.Sum32() & 0x7fffffff),
	}, nil
}

// Evidence is what NebulaCtrl knows of a name's DNS record in its zone, from
// the name's own DNS sync: the zone it matched, its status and its last error.
// A name whose record a sync wrote with the connection's token (status managed,
// no error since) has shown the token may edit that zone; nothing else does: a
// record somebody else created (present), a conflict, a missing record or a name
// not synced yet proves nothing.
type Evidence struct {
	ZoneID string
	Status string
	Error  string
}

// statusManaged is the sync status of a name whose record NebulaCtrl wrote.
const statusManaged = "managed"

// Choice is the certificate method of one hostname, as far as the Cloudflare
// connection decides it.
type Choice struct {
	// DNS01: the hostname is in a connected zone and a DNS sync wrote its record
	// with the connection's token, so its certificate is issued with DNS-01
	// through Cloudflare.
	DNS01 bool
	// Fallback says why a hostname in a connected zone still uses HTTP-01. Empty
	// for DNS01 and for hostnames outside every connected zone.
	Fallback string
}

// InZone reports whether the hostname lives in a zone of the connection (a
// wildcard hostname is matched by its base).
func (a CloudflareAccess) InZone(host string) bool {
	if !a.Connected {
		return false
	}
	_, ok := cfzone.Match(a.Zones, host)
	return ok
}

// permissionFailure reports whether a stored sync error is Cloudflare refusing
// the token (HTTP 401 or 403): see errorText in the Cloudflare integration, which
// stores APIError's text, "cloudflare API answered 403 ...".
func permissionFailure(syncError string) bool {
	return strings.Contains(syncError, "answered 403") || strings.Contains(syncError, "answered 401")
}

// Choose decides for one hostname from the connection's zones and the name's
// evidence.
func (a CloudflareAccess) Choose(host string, ev Evidence) Choice {
	if !a.Connected {
		return Choice{}
	}
	z, ok := cfzone.Match(a.Zones, host)
	if !ok {
		return Choice{}
	}
	switch {
	case a.Status != "ok":
		return Choice{Fallback: "Cloudflare rejected the connection's token, so this certificate uses HTTP-01. Reconnect Cloudflare under Settings, Integrations."}
	case z.Status != "" && z.Status != "active":
		return Choice{Fallback: fmt.Sprintf("The zone %s is %s on Cloudflare, not active, so this certificate uses HTTP-01.", z.Name, z.Status)}
	case ev.Status != statusManaged || ev.ZoneID != z.ID:
		return Choice{Fallback: fmt.Sprintf("Waiting for the first successful DNS sync in %s: DNS-01 needs the token to have written this name's record, so this certificate uses HTTP-01 until then.", z.Name)}
	case permissionFailure(ev.Error):
		return Choice{Fallback: fmt.Sprintf("Cloudflare refused the last DNS change (the token lacks Zone, DNS: Edit on %s?), so this certificate uses HTTP-01 until a sync succeeds.", z.Name)}
	case ev.Error != "":
		return Choice{Fallback: fmt.Sprintf("The last DNS sync with Cloudflare failed (%s), so this certificate uses HTTP-01 until a sync succeeds.", ev.Error)}
	}
	return Choice{DNS01: true}
}

// serverAuto is the automatic Cloudflare DNS-01 state of one server.
type serverAuto struct {
	srv    db.Server
	access CloudflareAccess
	// usable: the server's proxy can run the resolver. When false, why says what
	// is missing and no rule gets the automatic resolver (a resolver a person set
	// stays as it is).
	usable bool
	why    string
}

func loadServerAuto(ctx context.Context, q *db.Queries, srv db.Server) (serverAuto, error) {
	access, err := LoadCloudflare(ctx, q, srv.WorkspaceID)
	if err != nil {
		return serverAuto{}, err
	}
	return newServerAuto(srv, access), nil
}

func newServerAuto(srv db.Server, access CloudflareAccess) serverAuto {
	sa := serverAuto{srv: srv, access: access, usable: true}
	switch {
	case derefStr(srv.AcmeEmail) == "":
		sa.usable, sa.why = false, "The server has no ACME email, so no certificate is requested at all. Set one under the server's proxy settings."
	case !SupportsDNS01(srv):
		sa.usable, sa.why = false, "The server's agent is too old for DNS-01 certificates, so this certificate uses HTTP-01. Update the agent."
	}
	return sa
}

// serves lists the hostnames of the server that the automatic resolver would
// issue: names in a connected zone whose certificate is ours to request (tunnel
// names are not, and a wildcard certificate of the server covers its names).
func (sa serverAuto) serves(ctx context.Context, q *db.Queries, wildcards []db.WildcardCert) ([]string, error) {
	var out []string
	if !sa.access.Connected || !sa.usable {
		return nil, nil
	}
	rows, err := q.ListServerTLSDomains(ctx, sa.srv.ID)
	if err != nil {
		return nil, fmt.Errorf("list server domains: %w", err)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	tunnelled, err := tunnelledSet(ctx, q, sa.srv.ID, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if tunnelled[r.ID] || wildcardCovered(wildcards, r.Hostname, r.Generated) {
			continue
		}
		if sa.access.Choose(r.Hostname, Evidence{ZoneID: r.CfZoneID, Status: r.CfStatus, Error: r.CfError}).DNS01 {
			out = append(out, r.Hostname)
		}
	}
	if sa.srv.IsControlPlane {
		pages, err := q.ListWorkspacePublishedStatusPages(ctx, sa.srv.WorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("list status pages: %w", err)
		}
		for _, p := range pages {
			if sa.access.Choose(p.Hostname, Evidence{ZoneID: p.CfZoneID, Status: p.CfStatus, Error: p.CfError}).DNS01 {
				out = append(out, p.Hostname)
			}
		}
	}
	return out, nil
}

func wildcardCovered(ws []db.WildcardCert, host string, generated bool) bool {
	for _, w := range ws {
		spec := agentprotocol.WildcardCert{Domain: w.Domain, IncludeApex: w.IncludeApex}
		if agentprotocol.WildcardCovers(spec, host) && (!generated || w.CoverGenerated) {
			return true
		}
	}
	return false
}

// tunnelledSet returns the domains served through the server's Cloudflare
// Tunnel: their public TLS ends at Cloudflare, so no certificate is requested.
func tunnelledSet(ctx context.Context, q *db.Queries, serverID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	tun, err := q.ListTunnelledDomainIDs(ctx, db.ListTunnelledDomainIDsParams{DomainIds: ids, ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("list tunnelled domains: %w", err)
	}
	for _, id := range tun {
		out[id] = true
	}
	return out, nil
}

// autoRow returns the automatic resolver of the server's desired configuration,
// or false when the server serves no name that needs it. A resolver of the
// provider that a person staged takes its place: one resolver per provider.
func autoRow(ctx context.Context, q *db.Queries, srv db.Server, d desired) (desiredRow, bool, error) {
	if d.hasResolver(AutoProvider) {
		return desiredRow{}, false, nil
	}
	sa, err := loadServerAuto(ctx, q, srv)
	if err != nil {
		return desiredRow{}, false, err
	}
	names, err := sa.serves(ctx, q, d.wildcards)
	if err != nil || len(names) == 0 {
		return desiredRow{}, false, err
	}
	return desiredRow{
		Provider: AutoProvider, CredentialName: AutoCredentialName, EnvNames: []string{autoTokenEnv}, Rev: sa.access.TokenRev,
		Auto: true, Workspace: srv.WorkspaceID,
	}, true, nil
}

// ApplyAuto sets the automatic Cloudflare resolver on the domain rules of an
// http route table: rules of domains in a connected zone whose certificate is
// requested with DNS-01. Other rules are left as they are: a resolver a person
// set stays for names outside a connected zone, tunnel names keep their
// behaviour, and the dashboard's and status pages' rules are not domain rows
// (status pages: see AutoStatusPages). Routes are modified in place.
func ApplyAuto(ctx context.Context, q *db.Queries, serverID uuid.UUID, routes []agentprotocol.Route) error {
	var rules []*agentprotocol.DomainRule
	for i := range routes {
		if routes[i].IsL4() {
			continue
		}
		for j := range routes[i].Domains {
			rules = append(rules, &routes[i].Domains[j])
		}
	}
	return applyAuto(ctx, q, serverID, rules)
}

// ApplyAutoToRules is ApplyAuto for the rules of one service in a deploy
// payload.
func ApplyAutoToRules(ctx context.Context, q *db.Queries, serverID uuid.UUID, rules []agentprotocol.DomainRule) error {
	ptrs := make([]*agentprotocol.DomainRule, len(rules))
	for i := range rules {
		ptrs[i] = &rules[i]
	}
	return applyAuto(ctx, q, serverID, ptrs)
}

func applyAuto(ctx context.Context, q *db.Queries, serverID uuid.UUID, rules []*agentprotocol.DomainRule) error {
	var ids []uuid.UUID
	for _, r := range rules {
		if r.Tunnel || (r.TLSMode != "" && r.TLSMode != agentprotocol.TLSAuto) {
			continue
		}
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	sa, err := loadServerAuto(ctx, q, srv)
	if err != nil || !sa.usable || !sa.access.Connected {
		return err
	}
	// Only rules of domain rows: the dashboard's and the status pages' rules
	// carry ids that are not domains.
	rows, err := q.ListDomainDNSEvidence(ctx, ids)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	evidence := make(map[string]Evidence, len(rows))
	for _, r := range rows {
		evidence[r.ID.String()] = Evidence{ZoneID: r.CfZoneID, Status: r.CfStatus, Error: r.CfError}
	}
	tunnelled, err := tunnelledSet(ctx, q, serverID, ids)
	if err != nil {
		return err
	}
	for _, r := range rules {
		ev, isDomain := evidence[r.ID]
		if !isDomain || r.Tunnel || (r.TLSMode != "" && r.TLSMode != agentprotocol.TLSAuto) {
			continue
		}
		if id, err := uuid.Parse(r.ID); err != nil || tunnelled[id] {
			continue
		}
		if sa.access.Choose(r.Host, ev).DNS01 {
			r.DNSResolver = AutoProvider
		}
	}
	return nil
}

// AutoStatusPages returns the published status pages of the control-plane
// server's team whose hostname gets the automatic Cloudflare resolver. Pages of
// other teams are not included: the resolver's token is this team's.
func AutoStatusPages(ctx context.Context, q *db.Queries, serverID uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load server: %w", err)
	}
	if !srv.IsControlPlane {
		return out, nil
	}
	sa, err := loadServerAuto(ctx, q, srv)
	if err != nil || !sa.usable || !sa.access.Connected {
		return out, err
	}
	pages, err := q.ListWorkspacePublishedStatusPages(ctx, srv.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list status pages: %w", err)
	}
	for _, p := range pages {
		if sa.access.Choose(p.Hostname, Evidence{ZoneID: p.CfZoneID, Status: p.CfStatus, Error: p.CfError}).DNS01 {
			out[p.ID] = true
		}
	}
	return out, nil
}

// InConnectedZone reports whether a hostname served by srv is in a zone of the
// team's connection and srv can run the automatic resolver, for validating a
// domain before it exists: such a name needs no resolver set by hand (a
// wildcard name gets its certificate with DNS-01 once its record is synced).
func InConnectedZone(ctx context.Context, q *db.Queries, srv db.Server, host string) (bool, error) {
	sa, err := loadServerAuto(ctx, q, srv)
	if err != nil {
		return false, err
	}
	return sa.usable && sa.access.Status == "ok" && sa.access.InZone(host), nil
}

// autoSecrets adds the token of the automatic resolver r to out. The token is
// the team's Cloudflare connection's, read at send time. A connection that is
// gone adds nothing: the agent keeps the value it resolved earlier until the
// next Deploy proxy removes the resolver, so route pushes of a server whose
// connection was removed keep working.
func autoSecrets(ctx context.Context, q *db.Queries, box *secretbox.Box, serverID uuid.UUID, r agentprotocol.DNSResolver, out map[string]string) error {
	ws, ok := autoWorkspace(r.ID)
	if !ok {
		return fmt.Errorf("dns resolver id %q", r.ID)
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.WorkspaceID != ws {
		return fmt.Errorf("dns resolver %s belongs to another team", r.ID)
	}
	row, err := q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get cloudflare connection: %w", err)
	}
	token, err := box.OpenString(secretbox.CloudflareConnectionsToken, ws.String(), row.TokenEnc)
	if err != nil {
		return fmt.Errorf("decrypt cloudflare token: %w", err)
	}
	for _, name := range r.EnvNames {
		if name != autoTokenEnv {
			return fmt.Errorf("dns resolver %s lacks %s", r.ID, name)
		}
		out[agentprotocol.DNSSecretKey(r.ID, name)] = token
	}
	return nil
}

// sortResolvers keeps a desired resolver list ordered by provider.
func sortResolvers(rs []desiredRow) {
	slices.SortFunc(rs, func(a, b desiredRow) int { return strings.Compare(a.Provider, b.Provider) })
}
