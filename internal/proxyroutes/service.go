package proxyroutes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Service implements the route-service use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	jm     *jobs.Manager
	disp   agents.Dispatcher
	svcs   *services.Service
	logger *slog.Logger
}

var _ services.RouteHook = (*Service)(nil)

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, jm *jobs.Manager, disp agents.Dispatcher, svcs *services.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	var q *db.Queries
	if pool != nil {
		q = db.New(pool)
	}
	return &Service{pool: pool, q: q, bus: bus, jm: jm, disp: disp, svcs: svcs, logger: logger}
}

// UpstreamInput is one upstream of a route: an external "host:port" address or
// a managed service of the same environment (serviceId + port).
type UpstreamInput struct {
	Address   string     `json:"address,omitempty" maxLength:"300" doc:"External host:port, e.g. 192.168.1.20:8080"`
	ServiceID *uuid.UUID `json:"serviceId,omitempty" doc:"A managed service of the same environment on the route's server"`
	Port      int        `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Container port of the managed service"`
}

// DomainInput attaches a hostname to a new http route.
type DomainInput struct {
	Hostname string `json:"hostname" minLength:"3" maxLength:"253"`
}

// RouteFields are the route settings shared by create and update (nil = keep / default).
type RouteFields struct {
	Upstreams          *[]UpstreamInput `json:"upstreams,omitempty" doc:"1-20 upstreams"`
	Scheme             *string          `json:"scheme,omitempty" enum:"http,https" doc:"http routes: scheme used towards the upstream"`
	InsecureSkipVerify *bool            `json:"insecureSkipVerify,omitempty" doc:"https upstreams: do not verify the upstream certificate"`
	ServerName         *string          `json:"serverName,omitempty" maxLength:"253" doc:"https upstreams: TLS server name"`
	PassHost           *bool            `json:"passHost,omitempty" doc:"false rewrites the Host header to the upstream's (default true)"`
	HealthPath         *string          `json:"healthPath,omitempty" maxLength:"200" doc:"Enables a proxy health check on this path"`
	VerifySkip         *bool            `json:"verifySkip,omitempty" doc:"Skip the post-deploy HTTP verification (upstreams that legitimately answer 5xx)"`
	ListenPort         *int             `json:"listenPort,omitempty" minimum:"0" maximum:"65535" doc:"tcp/udp routes: host port on the server (1024-65535)"`
	AllowCIDRs         *[]string        `json:"allowCidrs,omitempty" doc:"tcp routes: client networks allowed; UDP cannot be restricted"`
	DenyCIDRs          *[]string        `json:"denyCidrs,omitempty" doc:"tcp routes: client networks refused first, even when allowCidrs admits them; UDP cannot be restricted"`
	OpenToInternet     *bool            `json:"openToInternet,omitempty" doc:"tcp/udp routes: required when allowCidrs is empty"`
	ProxyProtocol      *int             `json:"proxyProtocol,omitempty" enum:"0,1,2" doc:"tcp routes: PROXY protocol version sent to the upstream"`
}

// CreateInput creates a route service.
type CreateInput struct {
	Name string
	// Slug, when set, is the exact slug of the route service (nebula.toml keys services by slug); empty derives it from Name.
	Slug     string
	ServerID uuid.UUID
	Protocol string
	Domains  []DomainInput
	RouteFields
}

// UpdateInput patches a route service. The protocol and server are immutable.
type UpdateInput struct {
	Name *string
	RouteFields
}

// ServerOf loads the route's server within the caller's workspace (404 otherwise).
func (s *Service) serverOf(ctx context.Context, q *db.Queries, id uuid.UUID) (db.Server, error) {
	ws := policy.WorkspaceID(ctx)
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	return srv, nil
}

// apply overlays the fields on cfg.
func (f RouteFields) apply(cfg services.RouteConfig) services.RouteConfig {
	if f.Upstreams != nil {
		cfg.Upstreams = nil
		for _, u := range *f.Upstreams {
			ru := services.RouteUpstream{Address: strings.TrimSpace(u.Address), ServiceID: u.ServiceID, Port: u.Port}
			cfg.Upstreams = append(cfg.Upstreams, ru)
		}
	}
	if f.Scheme != nil {
		cfg.Scheme = *f.Scheme
	}
	if f.InsecureSkipVerify != nil {
		cfg.InsecureSkipVerify = *f.InsecureSkipVerify
	}
	if f.ServerName != nil {
		cfg.ServerName = *f.ServerName
	}
	if f.PassHost != nil {
		cfg.PassHost = f.PassHost
	}
	if f.HealthPath != nil {
		cfg.HealthPath = *f.HealthPath
	}
	if f.VerifySkip != nil {
		cfg.VerifySkip = *f.VerifySkip
	}
	if f.ListenPort != nil {
		cfg.ListenPort = *f.ListenPort
	}
	if f.AllowCIDRs != nil {
		cfg.AllowCIDRs = slices.Clone(*f.AllowCIDRs)
	}
	if f.DenyCIDRs != nil {
		cfg.DenyCIDRs = slices.Clone(*f.DenyCIDRs)
	}
	if f.OpenToInternet != nil {
		cfg.OpenToInternet = *f.OpenToInternet
	}
	if f.ProxyProtocol != nil {
		cfg.ProxyProtocol = *f.ProxyProtocol
	}
	return cfg
}

// checkUpstreams enforces the server's upstream policy on external addresses
// (422 upstream_denied, including the addresses a name resolves to) and the
// environment/server rules on managed ones.
func (s *Service) checkUpstreams(ctx context.Context, q *db.Queries, cfg services.RouteConfig, envID uuid.UUID, srv db.Server) error {
	pol := proxyserver.PolicyFromRow(srv.AppliedUpstreamPolicy)
	for _, u := range cfg.Upstreams {
		if u.ServiceID == nil {
			if err := agentprotocol.CheckUpstream(u.Address, pol); err != nil {
				return apierr.Unprocessable("upstream_denied", err.Error()).WithDetails(map[string]any{"address": u.Address})
			}
			if err := checkUpstreamName(ctx, u.Address, pol); err != nil {
				return err
			}
			continue
		}
		target, err := services.LoadScope(ctx, q, policy.ScopeProjectsRead, *u.ServiceID)
		if err != nil {
			return err
		}
		if target.EnvironmentID != envID {
			return apierr.NotFound("service")
		}
		if target.Service.Kind == services.KindRoute {
			return apierr.Unprocessable("invalid_upstream", "a route cannot point at another route service")
		}
		if target.Service.Kind == services.KindStack {
			return apierr.Unprocessable("invalid_upstream", "a route cannot point at a Compose stack: put a domain on the stack's component instead")
		}
		if target.Service.ServerID == nil || *target.Service.ServerID != srv.ID {
			return apierr.Unprocessable("invalid_upstream", target.Service.Name+" runs on another server: a route can only target services on its own server")
		}
	}
	if cfg.Protocol == agentprotocol.ProtoTCP || cfg.Protocol == agentprotocol.ProtoUDP {
		for _, c := range cfg.AllowCIDRs {
			if _, err := netip.ParsePrefix(c); err != nil {
				if _, aerr := netip.ParseAddr(c); aerr != nil {
					return apierr.Unprocessable("invalid_config", fmt.Sprintf("%q is not a CIDR or IP address", c))
				}
			}
		}
	}
	return nil
}

// Create stages a new route service on a server.
func (s *Service) Create(ctx context.Context, envID uuid.UUID, in CreateInput) (services.ServiceView, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return services.ServiceView{}, err
	}
	var row db.Service
	var after func(context.Context)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, after, err = s.CreateTx(ctx, tx, env, in)
		return err
	})
	if err != nil {
		return services.ServiceView{}, err
	}
	after(ctx)
	return s.svcs.ViewFor(ctx, services.ScopeOf(env, row))
}

// CreateTx is the transaction-bound core of Create: it stages a new route
// service on a server inside the caller's transaction. The caller loads env
// itself (services.LoadEnvironment with policy.ScopeProjectsWrite on db.New(tx))
// and owns the commit. Every query uses tx; the route's DNS-check jobs are
// enqueued in tx. The returned function (never nil) publishes the service
// event and MUST be run only after the transaction committed.
//
// A domain unique violation aborts the surrounding transaction: the caller must
// roll it back after any error. A held listen port is detected up front
// (*PortInUseError, 409 port_in_use) before any insert.
func (s *Service) CreateTx(ctx context.Context, tx pgx.Tx, env services.EnvScope, in CreateInput) (db.Service, func(context.Context), error) {
	noop := func(context.Context) {}
	q := db.New(tx)
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &env.Env.ProjectID); err != nil {
		return db.Service{}, noop, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return db.Service{}, noop, apierr.Unprocessable("validation_failed", "name is required")
	}
	srv, err := s.serverOf(ctx, q, in.ServerID)
	if err != nil {
		return db.Service{}, noop, err
	}
	if srv.Status == "revoked" {
		return db.Service{}, noop, apierr.Unprocessable("server_revoked", "server revoked: "+srv.Name+" was revoked; pick another server")
	}
	if err := domains.RequireProxyV2(&srv); err != nil {
		return db.Service{}, noop, err
	}
	cfg := in.RouteFields.apply(services.RouteConfig{Protocol: in.Protocol})
	if in.Protocol != agentprotocol.ProtoHTTP && len(in.Domains) > 0 {
		return db.Service{}, noop, apierr.Unprocessable("invalid_config", "domains apply to http routes only")
	}
	if in.Protocol == agentprotocol.ProtoTCP || in.Protocol == agentprotocol.ProtoUDP {
		if err := ValidateListenPort(cfg.ListenPort); err != nil {
			return db.Service{}, noop, err
		}
	}
	if err := validateRouteConfig(cfg); err != nil {
		return db.Service{}, noop, err
	}
	if err := s.checkUpstreams(ctx, q, cfg, env.Env.ID, srv); err != nil {
		return db.Service{}, noop, err
	}
	hosts := make([]string, 0, len(in.Domains))
	for _, d := range in.Domains {
		h, err := domains.ValidateHostname(d.Hostname)
		if err != nil {
			return db.Service{}, noop, err
		}
		if slices.Contains(hosts, h) {
			return db.Service{}, noop, apierr.Unprocessable("invalid_hostname", "duplicate hostname "+h)
		}
		hosts = append(hosts, h)
	}
	// Pre-check the listen port: a unique violation inside tx would abort it
	// and hide the holder's name.
	if cfg.Protocol == agentprotocol.ProtoTCP || cfg.Protocol == agentprotocol.ProtoUDP {
		holder, err := q.GetRoutePortHolder(ctx, db.GetRoutePortHolderParams{ServerID: srv.ID, Protocol: cfg.Protocol, ListenPort: int32(cfg.ListenPort)})
		switch {
		case err == nil:
			return db.Service{}, noop, &PortInUseError{Protocol: cfg.Protocol, Port: cfg.ListenPort, ServerID: srv.ID, HeldBy: holder.ServiceName}
		case !errors.Is(err, pgx.ErrNoRows):
			return db.Service{}, noop, fmt.Errorf("check route port: %w", err)
		}
	}
	row, err := s.insertRouteTx(ctx, tx, env, srv, name, in.Slug, cfg, hosts)
	if services.IsUniqueViolation(err) {
		return db.Service{}, noop, apierr.Conflict("domain_conflict", "a hostname is already in use")
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return db.Service{}, noop, err
		}
		return db.Service{}, noop, fmt.Errorf("create route: %w", err)
	}
	sc := services.ScopeOf(env, row)
	return row, func(context.Context) {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
	}, nil
}

// insertRouteTx inserts the route row, its hostnames (with DNS-check jobs) and
// the route.create audit entry inside tx.
func (s *Service) insertRouteTx(ctx context.Context, tx pgx.Tx, env services.EnvScope, srv db.Server, name, slug string, cfg services.RouteConfig, hosts []string) (db.Service, error) {
	row, err := insertRouteService(ctx, tx, env, name, slug, cfg, srv.ID)
	if err != nil {
		return db.Service{}, err
	}
	q := db.New(tx)
	for _, h := range hosts {
		dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
			ServiceID: row.ID, Hostname: h, TargetPort: 0, StatusMessage: domains.DNSGuidance(h, srv.PublicIp),
		})
		if err != nil {
			return db.Service{}, err
		}
		if err := s.jm.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
			return db.Service{}, fmt.Errorf("enqueue dns check: %w", err)
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "route.create", ResourceType: "service", ResourceID: row.ID.String(), ProjectID: &env.Env.ProjectID,
		Metadata: map[string]any{
			"service": row.Slug, "protocol": cfg.Protocol, "server": srv.Name, "listenPort": cfg.ListenPort,
			"upstreams": len(cfg.Upstreams), "hosts": hosts,
		},
	}); err != nil {
		return db.Service{}, err
	}
	return row, nil
}

// Get returns one route service.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (services.ServiceView, error) {
	sc, err := s.loadRoute(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	return s.svcs.ViewFor(ctx, sc)
}

// loadRoute loads a kind=route service (404 for every other kind).
func (s *Service) loadRoute(ctx context.Context, scope string, id uuid.UUID) (services.Scope, error) {
	sc, err := services.LoadScope(ctx, s.q, scope, id)
	if err != nil {
		return services.Scope{}, err
	}
	if sc.Service.Kind != services.KindRoute {
		return services.Scope{}, apierr.NotFound("route")
	}
	return sc, nil
}

// Update patches a route service (staged until deployed), guarded by If-Match.
func (s *Service) Update(ctx context.Context, id uuid.UUID, expected int64, in UpdateInput) (services.ServiceView, error) {
	sc, err := s.loadRoute(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	// Checked before the generation so a caller without proxy:write sees 403, not 412.
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return services.ServiceView{}, err
	}
	if sc.Service.Generation != expected {
		return services.ServiceView{}, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it").
			WithDetails(map[string]any{"currentGeneration": sc.Service.Generation})
	}
	var row db.Service
	var after func(context.Context)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, after, err = s.UpdateTx(ctx, tx, sc, in)
		return err
	})
	if err != nil {
		return services.ServiceView{}, err
	}
	after(ctx)
	sc.Service = row
	return s.svcs.ViewFor(ctx, sc)
}

// UpdateTx is the transaction-bound core of Update: it patches a route
// service inside the caller's transaction. sc is the service's scope, loaded by
// the caller on db.New(tx) with policy.ScopeProjectsWrite while holding the
// row lock; there is no If-Match here, the lock stands in for the generation
// compare (the write still goes through UpdateServiceCAS on sc's generation, so
// a lost lock surfaces as 412 generation_conflict). The protocol and server are
// immutable. When nothing changes, nothing is written: the returned row is
// sc.Service (same generation) and the closure is a no-op. Otherwise the
// generation advances exactly once and the closure publishes the service event;
// it MUST be run only after the transaction committed.
//
// A listen port held by another route is reported up front as *PortInUseError
// (409 port_in_use); callers must roll the transaction back after any error.
func (s *Service) UpdateTx(ctx context.Context, tx pgx.Tx, sc services.Scope, in UpdateInput) (db.Service, func(context.Context), error) {
	noop := func(context.Context) {}
	q := db.New(tx)
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return db.Service{}, noop, err
	}
	cur := sc.Service
	oldCfg := sc.Config()
	old, err := routeOf(cur)
	if err != nil {
		return db.Service{}, noop, err
	}
	if cur.ServerID == nil {
		return db.Service{}, noop, apierr.Unprocessable("no_server", "the route has no server")
	}
	srv, err := s.serverOf(ctx, q, *cur.ServerID)
	if err != nil {
		return db.Service{}, noop, err
	}
	if err := domains.RequireProxyV2(&srv); err != nil {
		return db.Service{}, noop, err
	}
	newCfg := in.RouteFields.apply(old)
	newName := cur.Name
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n == "" {
			return db.Service{}, noop, apierr.Unprocessable("validation_failed", "name must not be empty")
		} else {
			newName = n
		}
	}
	if old.Protocol == agentprotocol.ProtoHTTP && (newCfg.ListenPort != 0 || len(newCfg.AllowCIDRs) > 0 || len(newCfg.DenyCIDRs) > 0 || newCfg.ProxyProtocol != 0) {
		return db.Service{}, noop, apierr.Unprocessable("invalid_config", "listenPort, allowCidrs, denyCidrs and proxyProtocol apply to tcp/udp routes only")
	}
	if newCfg.Protocol != agentprotocol.ProtoHTTP {
		if err := ValidateListenPort(newCfg.ListenPort); err != nil {
			return db.Service{}, noop, err
		}
	}
	if err := validateRouteConfig(newCfg); err != nil {
		return db.Service{}, noop, err
	}
	if err := s.checkUpstreams(ctx, q, newCfg, sc.EnvironmentID, srv); err != nil {
		return db.Service{}, noop, err
	}
	var changed []string
	if newName != cur.Name {
		changed = append(changed, "name")
	}
	newFull := oldCfg
	newFull.Route = &newCfg
	for _, d := range services.DiffConfig("config", oldCfg, newFull.WithDefaults()) {
		changed = append(changed, d.Field)
	}
	if len(changed) == 0 {
		return cur, noop, nil
	}
	raw, err := routeServiceConfig(newCfg)
	if err != nil {
		return db.Service{}, noop, err
	}
	// Pre-check the listen port: a unique violation inside tx would abort it.
	if newCfg.Protocol == agentprotocol.ProtoTCP || newCfg.Protocol == agentprotocol.ProtoUDP {
		holder, err := q.GetRoutePortHolder(ctx, db.GetRoutePortHolderParams{ServerID: srv.ID, Protocol: newCfg.Protocol, ListenPort: int32(newCfg.ListenPort)})
		switch {
		case err == nil && holder.ServiceID != cur.ID:
			return db.Service{}, noop, &PortInUseError{Protocol: newCfg.Protocol, Port: newCfg.ListenPort, ServerID: srv.ID, HeldBy: holder.ServiceName}
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return db.Service{}, noop, fmt.Errorf("check route port: %w", err)
		}
	}
	row, err := q.UpdateServiceCAS(ctx, db.UpdateServiceCASParams{
		ID: cur.ID, ExpectedGeneration: cur.Generation, Name: newName, Config: raw, ServerID: cur.ServerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, noop, apierr.PreconditionFailed("generation_conflict", "the service changed since you loaded it")
	}
	if err != nil {
		return db.Service{}, noop, fmt.Errorf("update route: %w", err)
	}
	if err := registerPort(ctx, q, cur.ID, srv.ID, newCfg); err != nil {
		if _, ok := apierr.As(err); ok {
			return db.Service{}, noop, err
		}
		return db.Service{}, noop, fmt.Errorf("update route: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "route.update", ResourceType: "service", ResourceID: cur.ID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": cur.Slug, "fields": changed, "generation": row.Generation},
	}); err != nil {
		return db.Service{}, noop, fmt.Errorf("update route: %w", err)
	}
	sc.Service = row
	return row, func(context.Context) {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
	}, nil
}

// Delete removes a route service. A deployed route disappears from its
// server's table through the same flow as every service delete.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	sc, err := s.loadRoute(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return err
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return err
	}
	return s.svcs.Delete(ctx, id, services.DeleteInput{Confirm: sc.Service.Slug})
}

// RouteDeleted implements services.RouteHook: it sends the remaining route
// table to the server the deleted route was deployed on (atomically with the
// delete), so the proxy stops publishing it.
func (s *Service) RouteDeleted(ctx context.Context, tx pgx.Tx, sc services.Scope) error {
	if sc.Service.CurrentDeploymentID == nil {
		return nil
	}
	q := db.New(tx)
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sc.Service.CurrentDeploymentID})
	if err != nil {
		return err
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return nil
	}
	serverID := *deps[0].ServerID
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	routes, seq, err := domains.ServerTable(ctx, q, serverID, uuid.Nil)
	if err != nil {
		return err
	}
	pc, err := proxyserver.BuildConfig(ctx, q, serverID)
	if err != nil {
		return err
	}
	pl := agentprotocol.SyncRoutesPayload{Routes: routes, Proxy: pc, TableSeq: seq}
	if srv.AcmeEmail != nil {
		pl.ACMEEmail = *srv.AcmeEmail
	}
	traffic := srv.TrafficAnalyticsEnabled
	pl.TrafficAnalytics = &traffic
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindSyncRoutes, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue sync_routes: %w", err)
	}
	return nil
}

// PortView is one L4 listen port of a server.
type PortView struct {
	Protocol    string     `json:"protocol" enum:"tcp,udp"`
	Port        int        `json:"port"`
	ServiceID   uuid.UUID  `json:"serviceId"`
	ServiceName string     `json:"serviceName" doc:"Empty when the holder belongs to a project the caller cannot see"`
	Deployed    bool       `json:"deployed" doc:"false while the route is staged and not yet published by the proxy"`
	ProjectID   *uuid.UUID `json:"projectId"`
}

// ServerPorts lists the tcp/udp ports held by route services on a server.
func (s *Service) ServerPorts(ctx context.Context, serverID uuid.UUID) ([]PortView, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return nil, err
	}
	if _, err := s.serverOf(ctx, s.q, serverID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListServerRoutePorts(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list route ports: %w", err)
	}
	out := make([]PortView, 0, len(rows))
	for _, r := range rows {
		pv := PortView{Protocol: r.Protocol, Port: int(r.ListenPort), ServiceID: r.ServiceID, Deployed: r.CurrentDeploymentID != nil}
		if policy.Require(ctx, policy.ScopeProjectsRead, &r.ProjectID) == nil {
			pv.ServiceName, pv.ProjectID = r.ServiceName, &r.ProjectID
		}
		out = append(out, pv)
	}
	return out, nil
}

// CheckUpstreams applies the server's upstream policy and the managed-upstream rules to cfg (the same
// checks Create and Update run), for callers that validate a route before writing it.
func (s *Service) CheckUpstreams(ctx context.Context, q *db.Queries, cfg services.RouteConfig, envID uuid.UUID, srv db.Server) error {
	return s.checkUpstreams(ctx, q, cfg, envID, srv)
}
