// Package firewall implements the service firewall: ordered allow and block
// rules (agentprotocol.Firewall) the proxy evaluates against every request to
// a service's domains.
//
// The firewall is an operational setting like maintenance mode: it is stored on
// the service row, never bumps the generation, is not part of a release and is
// never rolled back. A change reaches the server running the service at once on
// its route table (sync_routes); an undeployed service gets it with its first
// deployment.
package firewall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ActionUpdate is the audit action of a firewall change.
const ActionUpdate = "service.firewall.update"

// Enforcement modes (FirewallView.Enforcement).
const (
	EnforcementNone   = "none"
	EnforcementRoutes = "routes"
	EnforcementGuard  = "guard"
)

// RoutePusher sends a server its route table inside a transaction
// (*proxyserver.Service).
type RoutePusher interface {
	PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error
}

// Service implements the firewall use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	routes RoutePusher
	log    *slog.Logger
}

// NewService returns a Service (pool and routes may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, routes RoutePusher, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{pool: pool, bus: bus, routes: routes, log: logger.With("component", "firewall")}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// FirewallView is a service's firewall and where it applies.
type FirewallView struct {
	Rules []agentprotocol.FirewallRule `json:"rules" doc:"The service's own rules in evaluation order; the first rule whose conditions all match decides"`
	// Standard is the standard protection, evaluated after Rules.
	Standard []domains.StandardRule `json:"standard" doc:"Standard protection NebulaCtrl applies to every HTTP service after its own rules; it cannot be switched off, but an allow rule above lets a request through. Empty for tcp/udp routes"`
	// Enforcement says how the proxy evaluates the active rules.
	Enforcement string `json:"enforcement" enum:"none,routes,guard" doc:"none: no firewall (tcp/udp routes). routes: compiled into the proxy's routers (no extra hop). guard: the edge guard next to the proxy evaluates them (country rules, or visitor addresses behind Cloudflare)"`
	// Hosts are the domains the firewall covers.
	Hosts []string `json:"hosts" doc:"Every domain of the service; the firewall covers all of them"`
	// Deployed is false while the service has never run: the rules then
	// apply with its first deployment.
	Deployed bool `json:"deployed"`
	// Server is the server the rules run on (empty without one).
	Server string `json:"server"`
	// GeoAvailable tells whether that server places visitors by country
	// (geolocation on): country rules need it.
	GeoAvailable bool `json:"geoAvailable"`
	// Supported is false when the server's agent is too old for firewalls.
	Supported bool `json:"supported"`
}

// target is the service and the server its rules run on.
type target struct {
	sc       services.Scope
	server   *db.Server
	deployed bool
}

func (s *Service) load(ctx context.Context, scope string, id uuid.UUID) (target, error) {
	sc, err := services.LoadScope(ctx, s.q, scope, id)
	if err != nil {
		return target{}, err
	}
	t := target{sc: sc}
	serverID := sc.Service.ServerID
	if dep := sc.Service.CurrentDeploymentID; dep != nil {
		deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*dep})
		if err != nil {
			return target{}, fmt.Errorf("current deployment: %w", err)
		}
		if len(deps) == 1 && deps[0].ServerID != nil {
			serverID, t.deployed = deps[0].ServerID, true
		}
	}
	if serverID != nil {
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID})
		if err != nil {
			return target{}, fmt.Errorf("load server: %w", err)
		}
		t.server = &srv
	}
	return t, nil
}

// Get returns the firewall of a service (scope projects:read).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (FirewallView, error) {
	t, err := s.load(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return FirewallView{}, err
	}
	return s.view(ctx, t)
}

// httpService reports whether the service serves HTTP: a tcp/udp route has
// no firewall, not even the standard protection.
func httpService(sc services.Scope) bool {
	rc := sc.Config().Route
	return sc.Service.Kind != services.KindRoute || rc == nil || rc.Protocol == "" || rc.Protocol == agentprotocol.ProtoHTTP
}

func (s *Service) view(ctx context.Context, t target) (FirewallView, error) {
	v := FirewallView{Rules: []agentprotocol.FirewallRule{}, Standard: []domains.StandardRule{}, Enforcement: EnforcementNone, Hosts: []string{}, Deployed: t.deployed, Supported: true}
	fw := domains.DecodeFirewall(t.sc.Service.Firewall)
	if fw != nil {
		v.Rules = fw.Rules
	}
	doms, err := s.q.ListServiceDomains(ctx, t.sc.Service.ID)
	if err != nil {
		return FirewallView{}, fmt.Errorf("list domains: %w", err)
	}
	tunnelOrLocked := false
	for _, d := range doms {
		if !slices.Contains(v.Hosts, d.Hostname) {
			v.Hosts = append(v.Hosts, d.Hostname)
		}
	}
	if len(doms) > 0 {
		// Whether a domain is tunnelled or locked to Cloudflare is decided per
		// server when the table is built; the same pass tells it here.
		if t.server != nil {
			rules, err := domains.PrepareDeployRules(ctx, s.q, t.server.ID, t.sc.Service.ID, doms)
			if err != nil {
				return FirewallView{}, err
			}
			for _, d := range rules.Rules {
				tunnelOrLocked = tunnelOrLocked || d.Tunnel || d.OriginLocked
			}
		}
	}
	if httpService(t.sc) {
		v.Standard = domains.StandardRules()
		v.Enforcement = EnforcementRoutes
		if domains.RouteFirewall(t.sc.Service.Firewall).NeedsGuard(tunnelOrLocked) {
			v.Enforcement = EnforcementGuard
		}
	}
	if t.server != nil {
		v.Server = t.server.Name
		v.GeoAvailable = t.server.GeoEnabled && t.server.TrafficAnalyticsEnabled
		v.Supported = supportsFirewall(*t.server)
	}
	return v, nil
}

// supportsFirewall reports whether the server's agent advertised
// agentprotocol.CapFirewall. A server that never connected is assumed to
// enroll with a current agent.
func supportsFirewall(srv db.Server) bool {
	if len(srv.Facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapFirewall)
}

// PutInput replaces the rules of a service's firewall.
type PutInput struct {
	Rules []agentprotocol.FirewallRule
}

// Put replaces a service's firewall (scopes projects:write and proxy:write).
// An empty rule list removes it. Rules without an id get one.
func (s *Service) Put(ctx context.Context, id uuid.UUID, in PutInput) (FirewallView, error) {
	t, err := s.load(ctx, policy.ScopeProjectsWrite, id)
	if err != nil {
		return FirewallView{}, err
	}
	sc := t.sc
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return FirewallView{}, err
	}
	if err := services.RequireNotStack(sc); err != nil {
		return FirewallView{}, err
	}
	if !httpService(sc) {
		return FirewallView{}, apierr.Unprocessable("firewall_unsupported", "a firewall only applies to HTTP services, not tcp/udp routes")
	}
	if len(in.Rules) > agentprotocol.MaxFirewallRules {
		return FirewallView{}, apierr.Unprocessable("invalid_firewall", fmt.Sprintf("at most %d firewall rules", agentprotocol.MaxFirewallRules))
	}
	for i, r := range in.Rules {
		if domains.IsStandardRuleID(strings.TrimSpace(r.ID)) {
			return FirewallView{}, apierr.Unprocessable("invalid_firewall",
				fmt.Sprintf("rule %d: ids starting with %q are reserved for the standard protection", i+1, domains.StandardRuleIDPrefix))
		}
	}
	fw := normalize(in.Rules)
	if err := fw.Validate(); err != nil {
		return FirewallView{}, apierr.Unprocessable("invalid_firewall", err.Error())
	}
	if t.server != nil && len(fw.Rules) > 0 {
		if !supportsFirewall(*t.server) {
			return FirewallView{}, apierr.Unprocessable("agent_too_old", "the agent on "+t.server.Name+" is too old for firewalls; update the agent first")
		}
		if fw.UsesCountry() && !(t.server.GeoEnabled && t.server.TrafficAnalyticsEnabled) {
			return FirewallView{}, apierr.Unprocessable("geo_required", "Country rules need geolocation on "+t.server.Name+
				": turn on traffic analytics and geolocation in the server's settings (the free DB-IP country database needs no account).")
		}
	}
	var raw json.RawMessage
	if len(fw.Rules) > 0 {
		if raw, err = json.Marshal(fw); err != nil {
			return FirewallView{}, err
		}
	}
	// The column is jsonb, which returns the document in its own layout (key
	// order, spacing): compare the stored rules as the document we would write.
	var current json.RawMessage
	if old := domains.DecodeFirewall(sc.Service.Firewall); old != nil && len(old.Rules) > 0 {
		if current, err = json.Marshal(old); err != nil {
			return FirewallView{}, err
		}
	}
	if string(raw) == string(current) {
		return s.view(ctx, t)
	}
	active := len(fw.Active())
	if err := sc.Guard(ctx, "set_firewall",
		fmt.Sprintf("Change the firewall of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{fmt.Sprintf("%d rule(s), %d active. The proxy applies them to every domain of the service at once.", len(fw.Rules), active),
			"A block rule answers matching visitors with HTTP 403."},
		string(raw)); err != nil {
		return FirewallView{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := db.New(tx).SetServiceFirewall(ctx, db.SetServiceFirewallParams{ID: id, Firewall: raw})
		if err != nil {
			return fmt.Errorf("store firewall: %w", err)
		}
		sc.Service = row
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: ActionUpdate, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: map[string]any{"slug": row.Slug, "rules": len(fw.Rules), "active": active, "countries": fw.UsesCountry()},
		}); err != nil {
			return err
		}
		if !t.deployed || s.routes == nil {
			return nil
		}
		return s.routes.PushRoutes(ctx, tx, t.server.ID)
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return FirewallView{}, err
		}
		return FirewallView{}, fmt.Errorf("set firewall: %w", err)
	}
	t.sc = sc
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return s.view(ctx, t)
}

// normalize trims names and values, upper-cases countries and methods and
// gives rules without an id a fresh one.
func normalize(rules []agentprotocol.FirewallRule) agentprotocol.Firewall {
	out := agentprotocol.Firewall{Rules: make([]agentprotocol.FirewallRule, len(rules))}
	for i, r := range rules {
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			r.ID = newRuleID()
		}
		r.Name = strings.TrimSpace(r.Name)
		conds := make([]agentprotocol.FirewallCondition, len(r.Conditions))
		for j, c := range r.Conditions {
			c.Name = strings.TrimSpace(c.Name)
			vals := make([]string, 0, len(c.Values))
			for _, v := range c.Values {
				v = strings.TrimSpace(v)
				if c.Field == agentprotocol.FirewallFieldCountry || c.Field == agentprotocol.FirewallFieldMethod {
					v = strings.ToUpper(v)
				}
				if v != "" && !slices.Contains(vals, v) {
					vals = append(vals, v)
				}
			}
			c.Values = vals
			conds[j] = c
		}
		r.Conditions = conds
		out.Rules[i] = r
	}
	return out
}

func newRuleID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
