// Package domains implements hostnames for services: validation, generated
// domains, DNS checks and the route table derived from them.
package domains

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/hostclaim"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	maxHostnameLen = 253
	maxLabelLen    = 63
)

// ValidateHostname normalizes (lowercase, no trailing dot) and validates a
// hostname per RFC 1123: at most 253 characters, 1-63 character labels of
// [a-z0-9-] that neither start nor end with a hyphen, at least two labels and
// a non-numeric last label (so IP addresses are rejected).
func ValidateHostname(h string) (string, error) {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	bad := func(msg string) error { return apierr.Unprocessable("invalid_hostname", msg) }
	if h == "" {
		return "", bad("hostname is required")
	}
	if len(h) > maxHostnameLen {
		return "", bad("hostname must be at most 253 characters")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", bad("hostname needs at least two labels, e.g. app.example.com")
	}
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLen {
			return "", bad("each hostname label must be 1-63 characters")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return "", bad("hostname labels must not start or end with a hyphen")
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", bad("hostnames may only contain letters, digits, hyphens and dots")
			}
		}
	}
	if _, err := strconv.Atoi(labels[len(labels)-1]); err == nil {
		return "", bad("hostname must not be an IP address")
	}
	return h, nil
}

// truncateLabel cuts a label to 63 characters without leaving a trailing hyphen.
func truncateLabel(l string) string {
	if len(l) > maxLabelLen {
		l = l[:maxLabelLen]
	}
	return strings.Trim(l, "-")
}

// GenerateHostname builds `<service>-<env>-<project>.<base>`. The first label
// is truncated to 63 characters; labels of base are kept as given.
func GenerateHostname(serviceSlug, envSlug, projectSlug, base string) string {
	return truncateLabel(serviceSlug+"-"+envSlug+"-"+projectSlug) + "." + base
}

// BaseDomain returns the domain generated hostnames live under: the server's
// base_domain, else `<public_ip>.sslip.io`. ok is false when neither exists.
func BaseDomain(srv db.Server) (string, bool) {
	if srv.BaseDomain != nil {
		b := strings.Trim(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(*srv.BaseDomain)), "*."), ".")
		if b != "" {
			return b, true
		}
	}
	if srv.PublicIp != nil && strings.TrimSpace(*srv.PublicIp) != "" {
		ip := strings.ReplaceAll(strings.TrimSpace(*srv.PublicIp), ":", "-")
		return ip + ".sslip.io", true
	}
	return "", false
}

// DNSGuidance is the instruction stored in a domain's statusMessage.
func DNSGuidance(host string, publicIP *string) string {
	if publicIP == nil || *publicIP == "" {
		return "The server has no public IP set, so DNS cannot be checked. Point an A record for " + host + " to the server's public IP."
	}
	return "Point an A record for " + host + " to " + *publicIP
}

// Service implements the domain use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	jm   *jobs.Manager
	box  *secretbox.Box
	dns  DNSHook
}

// DNSHook keeps DNS records in step with domains (the Cloudflare integration).
// Every method runs inside the domain's transaction. A nil hook means no DNS
// automation.
type DNSHook interface {
	// DomainCreated matches a new domain against the team's DNS zones and schedules its first sync.
	DomainCreated(ctx context.Context, tx pgx.Tx, domainID uuid.UUID) error
	// DomainRenamed re-matches a domain whose hostname changed.
	DomainRenamed(ctx context.Context, tx pgx.Tx, domainID uuid.UUID) error
	// DomainsRemoved is called after domains of the team were deleted.
	DomainsRemoved(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error
}

// SetDNS plugs in the DNS automation (wiring only; call before serving).
func (s *Service) SetDNS(h DNSHook) { s.dns = h }

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, jm *jobs.Manager, box *secretbox.Box) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus, jm: jm, box: box}
}

// List returns a service's domains.
func (s *Service) List(ctx context.Context, serviceID uuid.UUID) ([]services.DomainView, error) {
	if _, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceDomains(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	return services.DomainViews(ctx, s.q, rows)
}

// serverFor loads the service's server (nil when unassigned).
func (s *Service) serverFor(ctx context.Context, sc services.Scope) (*db.Server, error) {
	return s.serverForQ(ctx, s.q, sc)
}

// serverForQ is serverFor on q (a transaction's queries for the Tx variants).
func (s *Service) serverForQ(ctx context.Context, q *db.Queries, sc services.Scope) (*db.Server, error) {
	if sc.Service.ServerID == nil {
		return nil, nil
	}
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *sc.Service.ServerID})
	if err != nil {
		return nil, fmt.Errorf("load server: %w", err)
	}
	return &srv, nil
}

// noAfterCommit is the after-commit function of a Tx adapter that has nothing to run.
func noAfterCommit(context.Context) {}

// announceChange announces a domain change and its service's to the live-update feed.
func (s *Service) announceChange(sc services.Scope, domainID uuid.UUID) {
	services.Publish(s.bus, services.EventDomainUpdated, sc.ProjectID, sc.EnvironmentID, domainID)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
}

// inSavepoint runs fn in a savepoint of tx. When fn fails (a unique violation
// in particular) the savepoint is rolled back, so tx stays usable for the caller.
func inSavepoint(ctx context.Context, tx pgx.Tx, fn func(sp pgx.Tx) error) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin savepoint: %w", err)
	}
	if err := fn(sp); err != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("%w (rollback savepoint: %v)", err, rbErr)
		}
		return err
	}
	return sp.Commit(ctx)
}

func (s *Service) checkProxy(srv *db.Server) error {
	if srv != nil && srv.ProxyStatus != nil && *srv.ProxyStatus == agentprotocol.ProxyError {
		msg := "the proxy on " + srv.Name + " is not running"
		if srv.ProxyError != nil && *srv.ProxyError != "" {
			msg += ": " + *srv.ProxyError
		}
		return apierr.Unprocessable("proxy_unavailable", msg)
	}
	return nil
}

// Create attaches a hostname to a service (staged until deployed). Any field
// of in beyond the hostname needs proxy:write and a proxy-v2 agent. Compose
// stacks need CreateForComponent.
func (s *Service) Create(ctx context.Context, serviceID uuid.UUID, hostname string, targetPort int, in RuleInput) (services.DomainView, error) {
	return s.CreateForComponent(ctx, serviceID, hostname, targetPort, "", in)
}

// CreateForComponent is Create for a service that routes per component: a
// Compose stack, whose domain names the component (compose service) and the
// container port it routes to.
func (s *Service) CreateForComponent(ctx context.Context, serviceID uuid.UUID, hostname string, targetPort int, component string, in RuleInput) (services.DomainView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return services.DomainView{}, err
	}
	host, err := ValidateDomainHost(hostname)
	if err != nil {
		return services.DomainView{}, err
	}
	return s.create(ctx, sc, host, targetPort, component, false, in)
}

// generatedHost builds the platform hostname of a service (or one of its
// components) on its server's base domain, using q's reads.
func (s *Service) generatedHost(ctx context.Context, q *db.Queries, sc services.Scope, component string) (string, error) {
	srv, err := s.serverForQ(ctx, q, sc)
	if err != nil {
		return "", err
	}
	if srv == nil {
		return "", apierr.Unprocessable("no_server", "assign the service to a server first")
	}
	base, ok := BaseDomain(*srv)
	if !ok {
		return "", apierr.Unprocessable("no_base_domain", "the server has neither a base domain nor a public IP to generate a hostname from")
	}
	slug := sc.Service.Slug
	if component != "" {
		slug += "-" + component
	}
	host := GenerateHostname(slug, sc.EnvSlug, sc.ProjectSlug, base)
	if _, err := ValidateHostname(host); err != nil {
		return "", err
	}
	return host, nil
}

// generatedAttempts is how many plain candidate hostnames Generate tries
// (`name`, `name-2`, … `name-5`); one more, tied to the team, follows them.
const generatedAttempts = 5

// generatedTries is every candidate Generate tries.
const generatedTries = generatedAttempts + 1

// generatedCandidate returns the i-th (1-based) candidate: the plain ones, then
// the team's own (teamCandidate), which another team cannot have squatted.
func generatedCandidate(host string, i int, workspaceID uuid.UUID) string {
	if i > generatedAttempts {
		return teamCandidate(host, workspaceID)
	}
	return candidateHost(host, i)
}

// candidateHost returns the i-th (1-based) candidate of a generated hostname:
// host itself, then its first label with `-2`, `-3`, … appended.
func candidateHost(host string, i int) string {
	if i <= 1 {
		return host
	}
	first, rest, _ := strings.Cut(host, ".")
	suffix := "-" + strconv.Itoa(i)
	if len(first)+len(suffix) > maxLabelLen {
		first = strings.Trim(first[:maxLabelLen-len(suffix)], "-")
	}
	return first + suffix + "." + rest
}

func isDomainConflict(err error) bool {
	var ae *apierr.Error
	return errors.As(err, &ae) && ae.Code == "domain_conflict"
}

// Generate attaches a generated hostname `<service>-<env>-<project>.<base>`.
func (s *Service) Generate(ctx context.Context, serviceID uuid.UUID, component string) (services.DomainView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return services.DomainView{}, err
	}
	component, err = generateComponent(sc, component)
	if err != nil {
		return services.DomainView{}, err
	}
	host, err := s.generatedHost(ctx, s.q, sc, component)
	if err != nil {
		return services.DomainView{}, err
	}
	var lastErr error
	for i := 1; i <= generatedTries; i++ {
		v, err := s.create(ctx, sc, generatedCandidate(host, i, sc.WorkspaceID), 0, component, true, RuleInput{})
		if err == nil {
			return v, nil
		}
		if !isDomainConflict(err) {
			return services.DomainView{}, err
		}
		lastErr = err
	}
	return services.DomainView{}, lastErr
}

// GenerateTx is Generate on the caller's transaction, for applying a project
// configuration file. A unique violation would abort tx, so every candidate
// hostname is checked for use first (the insert itself still runs in a
// savepoint). sc is the service's scope, loaded by the caller inside tx
// (policy.ScopeProjectsWrite). The returned function publishes the change
// events and MUST be called only after tx committed.
func (s *Service) GenerateTx(ctx context.Context, tx pgx.Tx, sc services.Scope, component string) (services.DomainView, func(context.Context), error) {
	q := db.New(tx)
	component, err := generateComponent(sc, component)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	host, err := s.generatedHost(ctx, q, sc, component)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	var lastErr error
	for i := 1; i <= generatedTries; i++ {
		candidate := generatedCandidate(host, i, sc.WorkspaceID)
		n, err := q.CountDomainsByHostname(ctx, candidate)
		if err != nil {
			return services.DomainView{}, noAfterCommit, fmt.Errorf("check hostname: %w", err)
		}
		if n > 0 {
			lastErr = hostclaim.Unavailable("domain_conflict", candidate)
			continue
		}
		v, after, err := s.createTx(ctx, tx, sc, candidate, 0, component, true, RuleInput{})
		if err == nil {
			return v, after, nil
		}
		if !isDomainConflict(err) {
			return services.DomainView{}, noAfterCommit, err
		}
		lastErr = err
	}
	return services.DomainView{}, noAfterCommit, lastErr
}

// createPlan is a validated domain insert: everything create writes.
type createPlan struct {
	sc         services.Scope
	host       string
	targetPort int
	component  string
	generated  bool
	sp         spec
	wwwRule    *spec // the www redirect to create alongside, when asked for
	publicIP   *string
}

// planCreate resolves and validates a new domain using q's reads only: port and
// component rules of the service kind, proxy health, authorization, the rule
// itself and the www redirect.
func (s *Service) planCreate(ctx context.Context, q *db.Queries, sc services.Scope, host string, targetPort int, component string, generated bool, in RuleInput) (createPlan, error) {
	cfg := sc.Config()
	switch sc.Service.Kind {
	case services.KindRoute:
		if component != "" {
			return createPlan{}, apierr.Unprocessable("invalid_component", "component only applies to Compose stacks")
		}
		if cfg.Route == nil || cfg.Route.Protocol != agentprotocol.ProtoHTTP {
			return createPlan{}, apierr.Unprocessable("no_http_route", "only http route services take domains")
		}
		if targetPort != 0 {
			return createPlan{}, apierr.Unprocessable("invalid_target_port", "route services have no target port")
		}
	case services.KindStack:
		var err error
		if targetPort, err = stackTarget(cfg, component, targetPort); err != nil {
			return createPlan{}, err
		}
	default:
		p, stored, err := cfg.ResolveDomainProcess(component, targetPort)
		if err != nil {
			return createPlan{}, err
		}
		// The row stores the process (empty for the default one) in its component
		// column and always the process' own port.
		targetPort, component = p.Port, stored
	}
	srv, err := s.serverForQ(ctx, q, sc)
	if err != nil {
		return createPlan{}, err
	}
	if err := s.checkProxy(srv); err != nil {
		return createPlan{}, err
	}
	if err := s.authorize(ctx, sc, srv, in); err != nil {
		return createPlan{}, err
	}
	if agentprotocol.IsWildcardHost(host) {
		// A wildcard routes every subdomain: a proxy feature like the rule fields.
		if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
			return createPlan{}, err
		}
		if err := RequireProxyV2(srv); err != nil {
			return createPlan{}, err
		}
	}
	sp, err := defaultSpec(host).apply(in)
	if err != nil {
		return createPlan{}, err
	}
	if err := s.validate(ctx, q, sc, srv, sp, uuid.Nil); err != nil {
		return createPlan{}, err
	}
	p := createPlan{sc: sc, host: host, targetPort: targetPort, component: component, generated: generated, sp: sp}
	if in.WWWRedirect != nil && *in.WWWRedirect {
		cs, err := s.wwwSpec(ctx, q, sc, srv, sp, nil, uuid.Nil)
		if err != nil {
			return createPlan{}, err
		}
		p.wwwRule = &cs
	}
	if srv != nil {
		p.publicIP = srv.PublicIp
	}
	return p, nil
}

// writeCreate performs the writes of a planned create on tx (insert, generation
// bump, DNS check, DNS hook, www redirect, audit) and returns the new row. Errors
// are raw; see writeError.
func (s *Service) writeCreate(ctx context.Context, tx pgx.Tx, p createPlan) (db.Domain, error) {
	q := db.New(tx)
	sp, sc := p.sp, p.sc
	dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
		ServiceID: sc.Service.ID, Hostname: p.host, TargetPort: int32(p.targetPort), Generated: p.generated,
		StatusMessage: DNSGuidance(p.host, p.publicIP), Kind: sp.Kind, PathPrefix: sp.PathPrefix, StripPrefix: sp.StripPrefix,
		AddPrefix: sp.AddPrefix, RedirectTo: sp.redirectTo(), RedirectCode: int32(sp.RedirectCode), PreservePath: sp.PreservePath,
		TlsMode: sp.TLSMode, CertificateID: sp.CertificateID, Options: sp.optionsJSON(), DnsResolver: sp.DNSResolver, Component: p.component,
	})
	if err != nil {
		return db.Domain{}, err
	}
	if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
		return db.Domain{}, err
	}
	if err := s.jm.InsertTx(ctx, tx, DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
		return db.Domain{}, fmt.Errorf("enqueue dns check: %w", err)
	}
	if s.dns != nil {
		if err := s.dns.DomainCreated(ctx, tx, dom.ID); err != nil {
			return db.Domain{}, fmt.Errorf("dns hook: %w", err)
		}
		if dom, err = q.GetDomainByID(ctx, dom.ID); err != nil {
			return db.Domain{}, err
		}
	}
	if p.wwwRule != nil {
		if err := s.insertCompanion(ctx, tx, sc, dom, *p.wwwRule, p.publicIP); err != nil {
			return db.Domain{}, err
		}
	}
	meta := map[string]any{"service": sc.Service.Slug, "hostname": p.host, "generated": p.generated}
	if p.wwwRule != nil {
		meta["wwwRedirect"] = p.wwwRule.Hostname
	}
	if p.component != "" {
		meta["component"] = p.component
	}
	if sp.PathPrefix != "" {
		meta["pathPrefix"] = sp.PathPrefix
	}
	if sp.Kind == agentprotocol.DomainRedirect {
		meta["redirectTo"] = sp.RedirectTo
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "domain.add", ResourceType: "domain", ResourceID: dom.ID.String(), ProjectID: &sc.ProjectID, Metadata: meta,
	}); err != nil {
		return db.Domain{}, err
	}
	return dom, nil
}

// writeError maps an error of writeCreate to the API error of the use case.
func (p createPlan) writeError(err error) error {
	if services.IsUniqueViolation(err) {
		return hostclaim.Unavailable("domain_conflict", p.host+p.sp.PathPrefix)
	}
	if _, ok := apierr.As(err); ok {
		return err
	}
	return fmt.Errorf("create domain: %w", err)
}

func (s *Service) create(ctx context.Context, sc services.Scope, host string, targetPort int, component string, generated bool, in RuleInput) (services.DomainView, error) {
	p, err := s.planCreate(ctx, s.q, sc, host, targetPort, component, generated, in)
	if err != nil {
		return services.DomainView{}, err
	}
	var dom db.Domain
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		dom, err = s.writeCreate(ctx, tx, p)
		return err
	})
	if err != nil {
		return services.DomainView{}, p.writeError(err)
	}
	s.announceChange(sc, dom.ID)
	return s.view(ctx, s.q, dom)
}

// CreateTx is CreateForComponent on the caller's transaction, for applying a
// project configuration file. sc is the service's scope, loaded by the caller
// inside tx (policy.ScopeProjectsWrite). A nil field of in takes the create
// default. The insert runs in a savepoint, so a hostname and path already in use
// yields a domain_conflict error and leaves tx usable. Reads and writes go
// through tx; the returned function publishes the change events and MUST be
// called only after tx committed.
func (s *Service) CreateTx(ctx context.Context, tx pgx.Tx, sc services.Scope, hostname string, targetPort int, component string, in RuleInput) (services.DomainView, func(context.Context), error) {
	host, err := ValidateDomainHost(hostname)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	return s.createTx(ctx, tx, sc, host, targetPort, component, false, in)
}

// createTx is CreateTx for an already validated host.
func (s *Service) createTx(ctx context.Context, tx pgx.Tx, sc services.Scope, host string, targetPort int, component string, generated bool, in RuleInput) (services.DomainView, func(context.Context), error) {
	q := db.New(tx)
	p, err := s.planCreate(ctx, q, sc, host, targetPort, component, generated, in)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	var dom db.Domain
	err = inSavepoint(ctx, tx, func(sp pgx.Tx) error {
		var err error
		dom, err = s.writeCreate(ctx, sp, p)
		return err
	})
	if err != nil {
		return services.DomainView{}, noAfterCommit, p.writeError(err)
	}
	v, err := s.view(ctx, q, dom)
	if err != nil {
		return services.DomainView{}, noAfterCommit, err
	}
	return v, func(context.Context) { s.announceChange(sc, dom.ID) }, nil
}

// view renders one domain row with its basic-auth user names.
func (s *Service) view(ctx context.Context, q *db.Queries, d db.Domain) (services.DomainView, error) {
	vs, err := services.DomainViews(ctx, q, []db.Domain{d})
	if err != nil {
		return services.DomainView{}, err
	}
	return vs[0], nil
}

// Delete detaches a hostname (staged until deployed).
func (s *Service) Delete(ctx context.Context, domainID uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return err
	}
	dom, err := s.q.GetDomainByID(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("domain")
	}
	if err != nil {
		return fmt.Errorf("get domain: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, dom.ServiceID)
	if err != nil {
		return err
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		if err := prodguard.Check(ctx,
			sc.ProdScope(fmt.Sprintf("Remove the domain %s from %s in production (%s / %s)", dom.Hostname, sc.Service.Name, sc.ProjectName, sc.EnvName), false,
				"Stages the removal of "+dom.Hostname+"; the proxy stops serving it with the next deployment.",
				"Visitors using this address get an error until another service takes it."),
			"delete_domain", prodguard.Digest("delete_domain", domainID)); err != nil {
			return err
		}
	}
	siblings, err := s.q.ListServiceDomains(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	comp, haveComp := services.FindWWWCompanion(siblings, dom)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.writeDelete(ctx, tx, sc, dom, comp, haveComp)
	})
	if err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	s.announceChange(sc, domainID)
	return nil
}

// DeleteTx is Delete on the caller's transaction, for applying a project
// configuration file. sc is the domain's service scope, loaded by the caller
// inside tx; a domain of another service is not found. It removes the www
// redirect with the domain and runs the DNS hook, but skips the production
// guard: the caller performs its own. The returned function publishes the
// change events and MUST be called only after tx committed.
func (s *Service) DeleteTx(ctx context.Context, tx pgx.Tx, sc services.Scope, domainID uuid.UUID) (func(context.Context), error) {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return noAfterCommit, err
	}
	q := db.New(tx)
	dom, err := q.GetDomainByID(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && dom.ServiceID != sc.Service.ID) {
		return noAfterCommit, apierr.NotFound("domain")
	}
	if err != nil {
		return noAfterCommit, fmt.Errorf("get domain: %w", err)
	}
	siblings, err := q.ListServiceDomains(ctx, sc.Service.ID)
	if err != nil {
		return noAfterCommit, fmt.Errorf("list domains: %w", err)
	}
	comp, haveComp := services.FindWWWCompanion(siblings, dom)
	if err := s.writeDelete(ctx, tx, sc, dom, comp, haveComp); err != nil {
		return noAfterCommit, fmt.Errorf("delete domain: %w", err)
	}
	return func(context.Context) { s.announceChange(sc, domainID) }, nil
}

// writeDelete removes dom and its www redirect comp (when haveComp), bumps the
// service generation, runs the DNS hook and records the audit entry, all on tx.
func (s *Service) writeDelete(ctx context.Context, tx pgx.Tx, sc services.Scope, dom, comp db.Domain, haveComp bool) error {
	q := db.New(tx)
	if _, err := q.DeleteDomain(ctx, dom.ID); err != nil {
		return err
	}
	if haveComp {
		// The www/apex redirect has no purpose without its domain.
		if _, err := q.DeleteDomain(ctx, comp.ID); err != nil {
			return err
		}
	}
	if _, err := q.BumpServiceGeneration(ctx, sc.Service.ID); err != nil {
		return err
	}
	if s.dns != nil {
		if err := s.dns.DomainsRemoved(ctx, tx, sc.WorkspaceID); err != nil {
			return fmt.Errorf("dns hook: %w", err)
		}
	}
	meta := map[string]any{"service": sc.Service.Slug, "hostname": dom.Hostname}
	if haveComp {
		meta["wwwRedirectRemoved"] = comp.Hostname
	}
	return audit.Record(ctx, tx, audit.Entry{
		Action: "domain.remove", ResourceType: "domain", ResourceID: dom.ID.String(), ProjectID: &sc.ProjectID, Metadata: meta,
	})
}

// FindTx looks a service's domain up by its file identity: hostname and path
// prefix (both normalized like Create). ok is false when the service has no such
// domain.
func (s *Service) FindTx(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, hostname, pathPrefix string) (db.Domain, bool, error) {
	host, err := ValidateDomainHost(hostname)
	if err != nil {
		return db.Domain{}, false, err
	}
	d, err := db.New(tx).GetServiceDomainByKey(ctx, db.GetServiceDomainByKeyParams{ServiceID: serviceID, Hostname: host, PathPrefix: pathPrefix})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Domain{}, false, nil
	}
	if err != nil {
		return db.Domain{}, false, fmt.Errorf("find domain: %w", err)
	}
	return d, true, nil
}

// ValidateDomainHost is ValidateHostname for a service domain: it also accepts a
// wildcard hostname (`*.example.com`), which matches exactly one label.
func ValidateDomainHost(h string) (string, error) {
	h = strings.TrimSpace(strings.ToLower(h))
	rest, wild := strings.CutPrefix(h, "*.")
	if !wild {
		return ValidateHostname(h)
	}
	base, err := ValidateHostname(rest)
	if err != nil {
		return "", err
	}
	if !agentprotocol.IsWildcardHost("*." + base) {
		return "", apierr.Unprocessable("invalid_hostname", "the wildcard's base domain must end in a top-level domain of letters")
	}
	return "*." + base, nil
}
