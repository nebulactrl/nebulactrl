// Package dashboardaccess lets the instance admin control who may reach the
// dashboard and how its certificate is issued.
//
// The dashboard is served by the system route of the control-plane host's proxy
// (package systemroute). Its access settings are an allow list of CIDRs (an
// ipAllowList on the route, which takes effect with a route push and needs no
// proxy restart) and an optional DNS-01 provider for its certificate (a
// certificate resolver of the control-plane server, which needs the server's
// "Deploy proxy").
//
// The settings live in one database row (dashboard_access). The installer's
// environment (NEBULACTRL_DASHBOARD_ALLOW_CIDRS, NEBULACTRL_DASHBOARD_DNS_*)
// only seeds that row: while no admin has edited it the installer's values are
// written on every start, and once an admin has saved the settings in the
// dashboard (or the API or CLI) the row is the source of truth and the
// environment is ignored, so edits survive restarts and installer re-runs.
// The process-wide systemroute configuration follows the row: it is loaded at
// start (Load) and updated by every change.
package dashboardaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Audit actions.
const (
	// ActionUpdate is an admin's change of the settings.
	ActionUpdate = "dashboard.access.update"
	// ActionSeed is the installer's settings reaching the row.
	ActionSeed = "dashboard.access.seed"

	resourceType = "dashboard_access"
	resourceID   = "dashboard"
)

// Settings sources.
const (
	SourceDefault   = "default"   // never stored: no allow list, HTTP-01
	SourceInstaller = "installer" // seeded from the installer's environment
	SourceAdmin     = "admin"     // saved by an instance admin
)

// Certificate methods.
const (
	MethodHTTP01 = "http-01"
	MethodDNS01  = "dns-01"
)

// AdminChecker tells whether a user is the instance admin (the first account).
// *auth.Service implements it.
type AdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// RoutePusher re-sends a server's route table inside tx (proxyserver.Service).
type RoutePusher interface {
	PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error
}

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI
// generation only.
type Deps struct {
	Pool   *pgxpool.Pool
	Config *config.Config
	Admins AdminChecker
	Routes RoutePusher
	Logger *slog.Logger
}

// Service implements the dashboard access use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	cfg    config.Installer
	admins AdminChecker
	routes RoutePusher
	log    *slog.Logger

	// mu serializes changes of the row and of the process-wide configuration.
	mu sync.Mutex
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{pool: d.Pool, admins: d.Admins, routes: d.Routes, log: d.Logger.With("component", "dashboardaccess")}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	if d.Config != nil {
		s.cfg = d.Config.Installer
	}
	return s
}

// DashboardResolverOption is a DNS-01 provider with a resolver on the control-plane
// server.
type DashboardResolverOption struct {
	Provider     string `json:"provider" doc:"The lego provider code, for example cloudflare"`
	ProviderName string `json:"providerName"`
	Applied      bool   `json:"applied" doc:"The proxy runs this resolver. false: it is staged and needs Deploy proxy (Servers, Proxy)"`
}

// DashboardCertificate describes how the dashboard's certificate is issued.
type DashboardCertificate struct {
	Method  string `json:"method" enum:"http-01,dns-01"`
	Status  string `json:"status" enum:"pending,unknown" doc:"pending: the DNS-01 resolver is not applied yet, the proxy serves a Let's Encrypt HTTP-01 certificate until Deploy proxy. unknown: the proxy obtains it itself and does not report its state"`
	Message string `json:"message"`
}

// DashboardAccessView is the dashboard access settings as the API shows them.
type DashboardAccessView struct {
	Enabled bool   `json:"enabled" doc:"The dashboard has a system route (an https public URL with a DNS name, installer layout). false: nothing to restrict"`
	Host    string `json:"host" doc:"The dashboard's hostname"`
	// AllowCIDRs is the allow list; empty means everybody may reach the dashboard.
	AllowCIDRs        []string                  `json:"allowCidrs"`
	DNSResolver       string                    `json:"dnsResolver" doc:"DNS-01 provider of the certificate; empty is automatic HTTP-01"`
	ResolverAvailable []DashboardResolverOption `json:"resolverAvailable" doc:"Providers with a resolver on the control-plane server (Servers, Proxy)"`
	Certificate       DashboardCertificate      `json:"certificate"`
	// ClientIP is the caller's address as the allow list sees it.
	ClientIP          string `json:"clientIp" doc:"The caller's address as the dashboard route's allow list sees it. Empty when it cannot be determined"`
	ClientIPAllowed   bool   `json:"clientIpAllowed" doc:"The current allow list admits clientIp (always true while it is empty or the caller does not come through the route)"`
	ViaDashboardRoute bool   `json:"viaDashboardRoute" doc:"The caller reached the control plane through the dashboard route, so the allow list applies to them"`
	// ProxyDeployRequired is true when the chosen DNS-01 resolver is staged but
	// the proxy has not applied it: run Deploy proxy on ProxyServerID.
	ProxyDeployRequired bool       `json:"proxyDeployRequired"`
	ProxyServerID       *uuid.UUID `json:"proxyServerId" doc:"The control-plane server, where Deploy proxy lives"`
	Source              string     `json:"source" enum:"default,installer,admin" doc:"Where the stored settings come from. installer: seeded from the installer's environment and still overwritten by it on restart; admin: saved here, the installer's environment is ignored"`
	UpdatedAt           *time.Time `json:"updatedAt"`
}

// UpdateInput replaces the settings.
type UpdateInput struct {
	AllowCIDRs     []string
	DNSResolver    string
	ConfirmLockout bool
}

// authorize allows the instance admin (the first account; a token counts
// through its creator and needs the admin scope).
func (s *Service) authorize(ctx context.Context) (*auth.Principal, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return nil, apierr.Forbidden()
	}
	if s.admins == nil {
		return nil, apierr.Forbidden()
	}
	ok, err := s.admins.IsInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// normalize trims, drops blanks and duplicates and validates an allow list
// like a route's access list.
func normalize(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if err := agentprotocol.ValidateAccessLists(out, nil, false); err != nil {
		return nil, apierr.Unprocessable("invalid_cidr", "allowCidrs: "+err.Error())
	}
	return out, nil
}

// Get returns the settings. Instance admin only.
func (s *Service) Get(ctx context.Context, peer Peer) (DashboardAccessView, error) {
	if _, err := s.authorize(ctx); err != nil {
		return DashboardAccessView{}, err
	}
	return s.view(ctx, s.q, peer)
}

func (s *Service) view(ctx context.Context, q *db.Queries, peer Peer) (DashboardAccessView, error) {
	v := DashboardAccessView{AllowCIDRs: []string{}, ResolverAvailable: []DashboardResolverOption{}, Source: SourceDefault}
	cfg, enabled := systemroute.Current()
	v.Enabled, v.Host = enabled, cfg.Host
	v.AllowCIDRs, v.DNSResolver = nonNil(slices.Clone(cfg.AllowCIDRs)), cfg.DNSResolver

	row, err := q.GetDashboardAccess(ctx)
	switch {
	case err == nil:
		v.Source = SourceInstaller
		if row.EditedAt != nil {
			v.Source = SourceAdmin
		}
		t := row.UpdatedAt
		v.UpdatedAt = &t
	case !errors.Is(err, pgx.ErrNoRows):
		return DashboardAccessView{}, fmt.Errorf("load dashboard access: %w", err)
	}

	ip, via := RealClientIP(peer.RemoteAddr, peer.ForwardedFor)
	if ip.IsValid() {
		v.ClientIP = ip.String()
	}
	v.ViaDashboardRoute = via
	v.ClientIPAllowed = !via || Allows(v.AllowCIDRs, ip)

	srv, err := q.GetControlPlaneServer(ctx)
	switch {
	case err == nil:
		v.ProxyServerID = &srv.ID
		if v.ResolverAvailable, err = s.resolvers(ctx, q, srv); err != nil {
			return DashboardAccessView{}, err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return DashboardAccessView{}, fmt.Errorf("load control-plane server: %w", err)
	}
	v.Certificate = certificate(v.DNSResolver, v.ResolverAvailable)
	for _, r := range v.ResolverAvailable {
		if r.Provider == v.DNSResolver && !r.Applied {
			v.ProxyDeployRequired = true
		}
	}
	return v, nil
}

func certificate(resolver string, avail []DashboardResolverOption) DashboardCertificate {
	if resolver == "" {
		return DashboardCertificate{Method: MethodHTTP01, Status: "unknown", Message: "Let's Encrypt validates the hostname over HTTP-01 on port 80; the proxy obtains the certificate itself"}
	}
	for _, r := range avail {
		if r.Provider == resolver && r.Applied {
			return DashboardCertificate{Method: MethodDNS01, Status: "unknown", Message: "The proxy obtains the certificate through its " + resolver + " DNS-01 resolver and does not report its state"}
		}
	}
	return DashboardCertificate{Method: MethodDNS01, Status: "pending", Message: "The " + resolver + " resolver is not applied on the proxy yet: the dashboard keeps its HTTP-01 certificate until you run Deploy proxy"}
}

// resolvers lists the DNS-01 providers the control-plane server has a
// resolver for, and whether the proxy runs each of them.
func (s *Service) resolvers(ctx context.Context, q *db.Queries, srv db.Server) ([]DashboardResolverOption, error) {
	want, err := dns01.Desired(ctx, q, srv.ID)
	if err != nil {
		return nil, err
	}
	have := dns01.AppliedSnapshot(srv)
	out := make([]DashboardResolverOption, 0, len(want.Resolvers))
	for _, w := range want.Resolvers {
		if dns01.IsAutoResolver(w) {
			continue // names in a connected Cloudflare zone, not a resolver the dashboard can name
		}
		spec, _ := agentprotocol.DNSProvider(w.Provider)
		opt := DashboardResolverOption{Provider: w.Provider, ProviderName: spec.Name}
		for _, h := range have.Resolvers {
			if h.Provider == w.Provider && h.ID == w.ID && h.Rev == w.Rev && h.DelaySeconds == w.DelaySeconds && h.CAServer == w.CAServer {
				opt.Applied = true
			}
		}
		out = append(out, opt)
	}
	return out, nil
}

// Update saves the settings: validates them, refuses to lock the caller out
// (409 dashboard_lockout) unless ConfirmLockout is set, applies them to the
// process-wide route configuration and re-sends the control-plane server's
// route table. Instance admin only.
func (s *Service) Update(ctx context.Context, peer Peer, in UpdateInput) (DashboardAccessView, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return DashboardAccessView{}, err
	}
	cur, enabled := systemroute.Current()
	if !enabled {
		return DashboardAccessView{}, apierr.Conflict("dashboard_route_disabled", "the dashboard has no system route (it needs an https public URL with a DNS name, as the installer sets up), so there is nothing to restrict")
	}
	allow, err := normalize(in.AllowCIDRs)
	if err != nil {
		return DashboardAccessView{}, err
	}
	resolver := strings.ToLower(strings.TrimSpace(in.DNSResolver))
	srv, err := s.q.GetControlPlaneServer(ctx)
	hasServer := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DashboardAccessView{}, fmt.Errorf("load control-plane server: %w", err)
	}
	if resolver != "" {
		if err := s.checkResolver(ctx, srv, hasServer, resolver); err != nil {
			return DashboardAccessView{}, err
		}
	}
	if ip, via := RealClientIP(peer.RemoteAddr, peer.ForwardedFor); via && len(allow) > 0 && !Allows(allow, ip) && !in.ConfirmLockout {
		return DashboardAccessView{}, apierr.Conflict("dashboard_lockout",
			fmt.Sprintf("your current address %s is not in the allow list; saving would lock you out of %s. Add it, or confirm the lock-out", ip, cur.Host)).
			WithDetails(map[string]any{"clientIp": ip.String()})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	prevAllow, prevResolver := systemroute.SetAccess(allow, resolver)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.UpdateDashboardAccess(ctx, db.UpdateDashboardAccessParams{AllowCidrs: allow, DnsResolver: resolver, EditedBy: userID(p)}); err != nil {
			return err
		}
		wsID := p.WorkspaceID
		if hasServer {
			wsID = srv.WorkspaceID
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionUpdate, ResourceType: resourceType, ResourceID: resourceID, WorkspaceID: wsID,
			Metadata: map[string]any{
				"allowCidrs": allow, "allowCidrCount": len(allow), "dnsResolver": resolver,
				"previousAllowCidrCount": len(prevAllow), "previousDnsResolver": prevResolver, "confirmLockout": in.ConfirmLockout,
			},
		}); err != nil {
			return err
		}
		if hasServer && s.routes != nil {
			return s.routes.PushRoutes(ctx, tx, srv.ID)
		}
		return nil
	})
	if err != nil {
		systemroute.SetAccess(prevAllow, prevResolver)
		return DashboardAccessView{}, fmt.Errorf("save dashboard access: %w", err)
	}
	return s.view(ctx, s.q, peer)
}

func userID(p *auth.Principal) *uuid.UUID {
	if p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// checkResolver requires a known provider with a resolver on the control-plane
// server whose agent can run it.
func (s *Service) checkResolver(ctx context.Context, srv db.Server, hasServer bool, provider string) error {
	if _, ok := agentprotocol.DNSProvider(provider); !ok {
		return apierr.Unprocessable("invalid_dns_resolver", "dnsResolver must be empty or a lego DNS provider code, for example cloudflare")
	}
	notConfigured := apierr.Unprocessable("resolver_not_configured",
		"the control-plane server has no "+provider+" DNS-01 resolver; add one under Servers, Proxy, then deploy the proxy")
	if !hasServer {
		return notConfigured
	}
	has, err := dns01.HasResolver(ctx, s.q, srv.ID, provider)
	if err != nil {
		return err
	}
	if !has {
		return notConfigured
	}
	return dns01.RequireDNS01Provider(srv, provider)
}

// Load installs the stored settings into the process-wide route configuration
// (call it once at start, before the first route push). While no admin has
// edited the row, the installer's allow list is written to it again (a
// re-run of the installer with another list takes effect); an edited row is
// left alone. Without an installer value and without a row nothing is stored.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.q.GetDashboardAccess(ctx)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load dashboard access: %w", err)
	}
	if !(found && row.EditedAt != nil) && (found || len(s.cfg.DashboardAllowCIDRs) > 0 || s.cfg.DashboardDNSResolver != "") {
		// The installer's provider counts only once its resolver went live
		// (the seeder records that); a different provider than before resets it.
		resolver := ""
		if found && s.cfg.DashboardDNSResolver != "" && row.DnsResolver == s.cfg.DashboardDNSResolver {
			resolver = row.DnsResolver
		}
		allow := nonNil(slices.Clone(s.cfg.DashboardAllowCIDRs))
		var changed bool
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			q := db.New(tx)
			_, err := q.SeedDashboardAccess(ctx, db.SeedDashboardAccessParams{AllowCidrs: allow, DnsResolver: resolver})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return nil // nothing changed
			case err != nil:
				return err
			}
			changed = true
			// A fresh install has no team to attribute the entry to yet.
			if _, err := q.FirstWorkspace(ctx); err != nil {
				return nil
			}
			return audit.Record(ctx, tx, audit.Entry{
				Action: ActionSeed, ResourceType: resourceType, ResourceID: resourceID,
				Metadata: map[string]any{"allowCidrs": allow, "allowCidrCount": len(allow), "dnsResolver": resolver, "by": "installer"},
			})
		})
		if err != nil {
			return fmt.Errorf("seed dashboard access: %w", err)
		}
		if changed || !found {
			if row, err = s.q.GetDashboardAccess(ctx); err != nil {
				return fmt.Errorf("reload dashboard access: %w", err)
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	systemroute.SetAccess(row.AllowCidrs, row.DnsResolver)
	return nil
}

// resolverLive records that the installer's DNS-01 provider is applied on the
// proxy: the row (when no admin has edited it) and the route configuration then
// name it, and the route table is re-sent. It reports whether anything changed.
func (s *Service) resolverLive(ctx context.Context, srv db.Server, provider string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed bool
	var prevAllow []string
	var prevResolver string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.SetDashboardAccessResolverFromInstaller(ctx, provider)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // an admin owns the settings, or it is recorded already
		}
		if err != nil {
			return err
		}
		changed = true
		prevAllow, prevResolver = systemroute.SetAccess(row.AllowCidrs, row.DnsResolver)
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionSeed, ResourceType: resourceType, ResourceID: resourceID, WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"dnsResolver": provider, "by": "installer"},
		}); err != nil {
			return err
		}
		if s.routes != nil {
			return s.routes.PushRoutes(ctx, tx, srv.ID)
		}
		return nil
	})
	if err != nil {
		if changed {
			systemroute.SetAccess(prevAllow, prevResolver)
		}
		return false, err
	}
	return changed, nil
}
