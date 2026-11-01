package domains

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/hostclaim"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// bcryptCost is the cost of stored basic-auth hashes.
const bcryptCost = 10

// maxAuthUsers bounds the users of one domain (agentprotocol.ValidBasicAuthLines).
const maxAuthUsers = 50

// loadDomain loads a domain and authorizes scope on its service (404 across
// workspaces and projects).
func (s *Service) loadDomain(ctx context.Context, scope string, domainID uuid.UUID) (db.Domain, services.Scope, error) {
	return s.loadDomainWith(ctx, s.q, scope, domainID)
}

// loadDomainWith is loadDomain on q (a transaction's queries for the Tx variants).
func (s *Service) loadDomainWith(ctx context.Context, q *db.Queries, scope string, domainID uuid.UUID) (db.Domain, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Domain{}, services.Scope{}, err
	}
	dom, err := q.GetDomainByID(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Domain{}, services.Scope{}, apierr.NotFound("domain")
	}
	if err != nil {
		return db.Domain{}, services.Scope{}, fmt.Errorf("get domain: %w", err)
	}
	sc, err := services.LoadScope(ctx, q, scope, dom.ServiceID)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Status == 404 {
			return db.Domain{}, services.Scope{}, apierr.NotFound("domain")
		}
		return db.Domain{}, services.Scope{}, err
	}
	return dom, sc, nil
}

// UpdateInput holds the optional fields of a domain PATCH.
type UpdateInput struct {
	Hostname *string
	Rule     RuleInput
}

// updatePlan is a validated domain update: what to write and what changes.
type updatePlan struct {
	dom         db.Domain
	sc          services.Scope
	sp          spec
	changed     []string
	hostChanged bool
	www         wwwPlan
	publicIP    *string
	noop        bool // nothing to write: no field changes and no www redirect action
}

// planUpdate authorizes and validates in against dom using q's reads only.
func (s *Service) planUpdate(ctx context.Context, q *db.Queries, dom db.Domain, sc services.Scope, in UpdateInput) (updatePlan, error) {
	srv, err := s.serverForQ(ctx, q, sc)
	if err != nil {
		return updatePlan{}, err
	}
	if err := s.authorize(ctx, sc, srv, in.Rule); err != nil {
		return updatePlan{}, err
	}
	old := specOf(dom)
	sp, err := old.apply(in.Rule)
	if err != nil {
		return updatePlan{}, err
	}
	if in.Rule.Kind != nil && sp.Kind == agentprotocol.DomainServe && in.Rule.RedirectTo == nil {
		sp.RedirectTo = ""
	}
	hostChanged := false
	if in.Hostname != nil {
		host, err := ValidateDomainHost(*in.Hostname)
		if err != nil {
			return updatePlan{}, err
		}
		hostChanged = host != dom.Hostname
		sp.Hostname = host
	}
	changed := changedFields(old, sp)
	// Validate the rule itself first (a redirect loop, a bad target) so the
	// error names what the request changed, not the www companion it touches.
	if len(changed) > 0 {
		if err := s.validate(ctx, q, sc, srv, sp, dom.ID); err != nil {
			return updatePlan{}, err
		}
	}
	// The www/apex redirect follows the domain (rename, certificate) and is
	// created or removed by wwwRedirect.
	siblings, err := q.ListServiceDomains(ctx, sc.Service.ID)
	if err != nil {
		return updatePlan{}, fmt.Errorf("list domains: %w", err)
	}
	comp, haveComp := services.FindWWWCompanion(siblings, dom)
	plan, err := s.planWWW(ctx, q, sc, srv, sp, in.Rule.WWWRedirect, comp, haveComp)
	if err != nil {
		return updatePlan{}, err
	}
	p := updatePlan{dom: dom, sc: sc, sp: sp, changed: changed, hostChanged: hostChanged, www: plan}
	if len(changed) == 0 && plan.action == wwwNone {
		p.noop = true
		return p, nil
	}
	if err := s.checkProxy(srv); err != nil && (hostChanged || plan.action != wwwNone) {
		return updatePlan{}, err
	}
	if srv != nil {
		p.publicIP = srv.PublicIp
	}
	return p, nil
}

// writeUpdate performs the writes of a planned update on tx (generation bump,
// DNS check, DNS hook, www redirect, audit) and returns the updated row and the
// new service generation. Errors are raw; see writeError.
func (s *Service) writeUpdate(ctx context.Context, tx pgx.Tx, p updatePlan) (db.Domain, int64, error) {
	q := db.New(tx)
	domainID := p.dom.ID
	sp, sc := p.sp, p.sc
	row := p.dom
	var err error
	if len(p.changed) > 0 {
		row, err = q.UpdateDomain(ctx, db.UpdateDomainParams{
			ID: domainID, Hostname: sp.Hostname, PathPrefix: sp.PathPrefix, StripPrefix: sp.StripPrefix, AddPrefix: sp.AddPrefix,
			Kind: sp.Kind, RedirectTo: sp.redirectTo(), RedirectCode: int32(sp.RedirectCode), PreservePath: sp.PreservePath,
			TlsMode: sp.TLSMode, CertificateID: sp.CertificateID, Options: sp.optionsJSON(), DnsResolver: sp.DNSResolver,
			ResetDns: p.hostChanged, StatusMessage: DNSGuidance(sp.Hostname, p.publicIP),
		})
		if err != nil {
			return db.Domain{}, 0, err
		}
	}
	gen, err := q.BumpServiceGeneration(ctx, sc.Service.ID)
	if err != nil {
		return db.Domain{}, 0, err
	}
	if p.hostChanged {
		if err := s.jm.InsertTx(ctx, tx, DNSCheckArgs{DomainID: &domainID}, nil); err != nil {
			return db.Domain{}, 0, fmt.Errorf("enqueue dns check: %w", err)
		}
	}
	if p.hostChanged && s.dns != nil {
		if err := s.dns.DomainRenamed(ctx, tx, domainID); err != nil {
			return db.Domain{}, 0, fmt.Errorf("dns hook: %w", err)
		}
		if row, err = q.GetDomainByID(ctx, domainID); err != nil {
			return db.Domain{}, 0, err
		}
	}
	switch p.www.action {
	case wwwCreate:
		if err := s.insertCompanion(ctx, tx, sc, row, p.www.spec, p.publicIP); err != nil {
			return db.Domain{}, 0, err
		}
	case wwwSync:
		if err := s.syncCompanion(ctx, tx, p.www.companion, p.www.spec, p.publicIP); err != nil {
			return db.Domain{}, 0, err
		}
	case wwwDelete:
		if err := s.deleteCompanion(ctx, tx, sc, p.www.companion); err != nil {
			return db.Domain{}, 0, err
		}
	}
	meta := map[string]any{"service": sc.Service.Slug, "hostname": sp.Hostname, "fields": p.changed}
	if p.www.action != wwwNone {
		meta["wwwRedirect"] = p.www.action.String()
	}
	err = audit.Record(ctx, tx, audit.Entry{
		Action: "domain.update", ResourceType: "domain", ResourceID: domainID.String(), ProjectID: &sc.ProjectID, Metadata: meta,
	})
	if err != nil {
		return db.Domain{}, 0, err
	}
	return row, gen, nil
}

// writeError maps an error of writeUpdate to the API error of the use case.
func (p updatePlan) writeError(err error) error {
	if services.IsUniqueViolation(err) {
		return hostclaim.Unavailable("domain_conflict", p.sp.Hostname+p.sp.PathPrefix)
	}
	if _, ok := apierr.As(err); ok {
		return err
	}
	return fmt.Errorf("update domain: %w", err)
}

// Update applies a partial update guarded by the owning service's generation
// (If-Match). The change is staged: it takes effect on the next deployment of
// the service. It returns the new view and the service generation.
func (s *Service) Update(ctx context.Context, domainID uuid.UUID, expected int64, in UpdateInput) (services.DomainView, int64, error) {
	dom, sc, err := s.loadDomain(ctx, policy.ScopeProjectsWrite, domainID)
	if err != nil {
		return services.DomainView{}, 0, err
	}
	if sc.Service.Generation != expected {
		return services.DomainView{}, 0, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it").
			WithDetails(map[string]any{"currentGeneration": sc.Service.Generation})
	}
	p, err := s.planUpdate(ctx, s.q, dom, sc, in)
	if err != nil {
		return services.DomainView{}, 0, err
	}
	if p.noop {
		v, err := s.view(ctx, s.q, dom)
		return v, sc.Service.Generation, err
	}
	var row db.Domain
	var gen int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := db.New(tx).LockServiceRow(ctx, sc.Service.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("service")
		}
		if err != nil {
			return err
		}
		if cur.Generation != expected {
			return apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it")
		}
		row, gen, err = s.writeUpdate(ctx, tx, p)
		return err
	})
	if err != nil {
		return services.DomainView{}, 0, p.writeError(err)
	}
	s.announceChange(sc, domainID)
	v, err := s.view(ctx, s.q, row)
	return v, gen, err
}

// UpdateTx is Update on the caller's transaction, for applying a project
// configuration file: nil fields of in keep their current value, and an update
// that changes nothing returns the current view without bumping the service
// generation. It does not compare generations: the caller holds the service
// row lock (FOR UPDATE) for the whole transaction. Reads and writes go through
// tx; the returned function publishes the change events and MUST be called
// only after tx committed.
func (s *Service) UpdateTx(ctx context.Context, tx pgx.Tx, domainID uuid.UUID, in UpdateInput) (services.DomainView, func(context.Context), error) {
	q := db.New(tx)
	dom, sc, err := s.loadDomainWith(ctx, q, policy.ScopeProjectsWrite, domainID)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	p, err := s.planUpdate(ctx, q, dom, sc, in)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	if p.noop {
		v, err := s.view(ctx, q, dom)
		return v, noAfterCommit, err
	}
	var row db.Domain
	err = inSavepoint(ctx, tx, func(sp pgx.Tx) error {
		var err error
		row, _, err = s.writeUpdate(ctx, sp, p)
		return err
	})
	if err != nil {
		return services.DomainView{}, noAfterCommit, p.writeError(err)
	}
	v, err := s.view(ctx, q, row)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	return v, func(context.Context) { s.announceChange(sc, domainID) }, nil
}

// AuthUserInput is one basic-auth user of PUT /domains/{id}/auth-users. The
// password may be omitted for a user that already exists (keeps its hash).
type AuthUserInput struct {
	Username string
	Password string
}

// SetAuthUsers replaces the basic-auth users of a domain and returns their
// names. Passwords are hashed with bcrypt and stored sealed; hashes are never
// returned. The service generation advances; users apply live with the next
// route push.
func (s *Service) SetAuthUsers(ctx context.Context, domainID uuid.UUID, users []AuthUserInput) ([]string, error) {
	dom, sc, err := s.loadDomain(ctx, policy.ScopeProjectsWrite, domainID)
	if err != nil {
		return nil, err
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return nil, err
	}
	srv, err := s.serverFor(ctx, sc)
	if err != nil {
		return nil, err
	}
	if err := RequireProxyV2(srv); err != nil {
		return nil, err
	}
	if dom.Kind == agentprotocol.DomainRedirect {
		return nil, apierr.Unprocessable("invalid_redirect", "a redirect domain takes no basic auth")
	}
	if len(users) > maxAuthUsers {
		return nil, apierr.Unprocessable("invalid_auth_users", fmt.Sprintf("at most %d basic-auth users", maxAuthUsers))
	}
	existing, err := s.q.ListDomainAuthUsernames(ctx, domainID)
	if err != nil {
		return nil, fmt.Errorf("list auth users: %w", err)
	}
	type hashed struct {
		name string
		enc  []byte
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(users))
	var fresh []hashed
	for _, u := range users {
		if !agentprotocol.ValidBasicUsername(u.Username) {
			return nil, apierr.Unprocessable("invalid_auth_users", fmt.Sprintf("invalid user name %q: use 1-64 letters, digits, . _ -", u.Username))
		}
		if seen[u.Username] {
			return nil, apierr.Unprocessable("invalid_auth_users", "duplicate user "+u.Username)
		}
		seen[u.Username] = true
		names = append(names, u.Username)
		if u.Password == "" {
			if !slices.Contains(existing, u.Username) {
				return nil, apierr.Unprocessable("invalid_auth_users", "a password is required for the new user "+u.Username)
			}
			continue
		}
		if len(u.Password) < 8 || len(u.Password) > 72 {
			return nil, apierr.Unprocessable("invalid_auth_users", "passwords must be 8-72 bytes long")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcryptCost)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		enc, err := s.box.SealString(secretbox.DomainAuthUsersHash, secretbox.RowID(domainID.String(), u.Username), string(h))
		if err != nil {
			return nil, fmt.Errorf("seal hash: %w", err)
		}
		fresh = append(fresh, hashed{name: u.Username, enc: enc})
	}
	if len(names) == 0 && services.DomainOptions(dom.Options).BasicAuth {
		return nil, apierr.Unprocessable("basic_auth_needs_users", "basicAuth is enabled: disable it before removing every user")
	}
	slices.Sort(names)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		for _, h := range fresh {
			if err := q.UpsertDomainAuthUser(ctx, db.UpsertDomainAuthUserParams{DomainID: domainID, Username: h.name, HashEnc: h.enc}); err != nil {
				return err
			}
		}
		if err := q.DeleteDomainAuthUsersExcept(ctx, db.DeleteDomainAuthUsersExceptParams{DomainID: domainID, Keep: names}); err != nil {
			return err
		}
		if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "domain.auth.set", ResourceType: "domain", ResourceID: domainID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "hostname": dom.Hostname, "users": names},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("set auth users: %w", err)
	}
	services.Publish(s.bus, services.EventDomainUpdated, sc.ProjectID, sc.EnvironmentID, domainID)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return names, nil
}

// DeleteAuthUser removes one basic-auth user (404 when it does not exist).
func (s *Service) DeleteAuthUser(ctx context.Context, domainID uuid.UUID, username string) error {
	dom, sc, err := s.loadDomain(ctx, policy.ScopeProjectsWrite, domainID)
	if err != nil {
		return err
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return err
	}
	users, err := s.q.ListDomainAuthUsernames(ctx, domainID)
	if err != nil {
		return fmt.Errorf("list auth users: %w", err)
	}
	if !slices.Contains(users, username) {
		return apierr.NotFound("user")
	}
	if services.DomainOptions(dom.Options).BasicAuth && len(users) <= 1 {
		return apierr.Unprocessable("basic_auth_needs_users", "basicAuth is enabled: disable it before removing the last user")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.DeleteDomainAuthUser(ctx, db.DeleteDomainAuthUserParams{DomainID: domainID, Username: username})
		if err != nil {
			return err
		}
		if n == 0 {
			return apierr.NotFound("user")
		}
		if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "domain.auth.set", ResourceType: "domain", ResourceID: domainID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "hostname": dom.Hostname, "removed": username},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete auth user: %w", err)
	}
	services.Publish(s.bus, services.EventDomainUpdated, sc.ProjectID, sc.EnvironmentID, domainID)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return nil
}
