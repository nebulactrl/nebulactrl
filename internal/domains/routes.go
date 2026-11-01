package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RuleFromRow builds the agent-side rule of a domain row. The rule carries no
// secret: basic-auth users are referenced by AuthRef and attached at send time.
func RuleFromRow(d db.Domain) agentprotocol.DomainRule {
	r := agentprotocol.DomainRule{
		ID: d.ID.String(), Host: d.Hostname, PathPrefix: d.PathPrefix, StripPrefix: d.StripPrefix, AddPrefix: d.AddPrefix,
		Kind: d.Kind, TLSMode: d.TlsMode, Options: services.DomainOptions(d.Options), DNSResolver: d.DnsResolver, Generated: d.Generated,
	}
	if d.Kind == agentprotocol.DomainRedirect {
		r.PreservePath, r.RedirectCode = d.PreservePath, int(d.RedirectCode)
		if d.RedirectTo != nil {
			r.RedirectTo = *d.RedirectTo
		}
	}
	if d.CertificateID != nil {
		r.CertificateID = d.CertificateID.String()
	}
	if r.Options.BasicAuth {
		r.AuthRef = agentprotocol.SecretAuthPrefix + d.ID.String()
	}
	return r
}

// RulesAndHosts converts domain rows into the rules and the distinct hostnames
// (in first-seen order) of one route.
func RulesAndHosts(rows []db.Domain) (rules []agentprotocol.DomainRule, hosts []string) {
	for _, d := range rows {
		rules = append(rules, RuleFromRow(d))
		if !slices.Contains(hosts, d.Hostname) {
			hosts = append(hosts, d.Hostname)
		}
	}
	return rules, hosts
}

// Target is a managed service's containers as referenced by route services:
// the deployment whose container names an upstream resolves to.
type Target struct {
	Slug         string
	DeploymentID string
	Replicas     int
	// Networks are the networks the proxy joins to reach the containers.
	Networks []string
}

// Overrides replaces the current deployment of managed services when the
// table is built for a deployment that is not current yet: routes that target
// a service point at its NEW containers before the agent applies the table.
type Overrides map[uuid.UUID]Target

// ServerRoutes returns the route table of every service whose current
// deployment runs on serverID, excluding exclude (uuid.Nil excludes none).
// Regular services contribute one Route per service and target port (hosts
// deduplicated, one DomainRule per domain row, replica upstreams); route
// services contribute their http (external) or tcp/udp route built from the
// release snapshot. HealthPath carries the health check path of the release
// when its health check is of type http.
func ServerRoutes(ctx context.Context, q *db.Queries, serverID uuid.UUID, exclude uuid.UUID) ([]agentprotocol.Route, error) {
	return ServerRoutesWith(ctx, q, serverID, exclude, nil)
}

// ServerTable is ServerRoutes with the number of the table: the server's
// route-table counter is bumped first, then the routes are read, so of two
// tables of one server the one built from the newer state has the higher
// number (agentprotocol.SyncRoutesPayload.TableSeq). q must be bound to the
// transaction that carries the table to the agent's command queue.
//
// The counter's lock lasts until that transaction ends, so it must be the last
// lock the transaction takes: do every write and row lock first, call
// ServerTable (or db.Queries.NumberRouteTable) right before enqueueing the
// command, and do nothing that locks after it. A transaction that builds
// tables of several servers does so in ascending server id (see db.Queries.NumberRouteTable).
func ServerTable(ctx context.Context, q *db.Queries, serverID uuid.UUID, exclude uuid.UUID) ([]agentprotocol.Route, uint64, error) {
	seq, err := q.NumberRouteTable(ctx, serverID)
	if err != nil {
		return nil, 0, fmt.Errorf("number the route table: %w", err)
	}
	routes, err := ServerRoutes(ctx, q, serverID, exclude)
	if err != nil {
		return nil, 0, err
	}
	return routes, seq, nil
}

// ServerRoutesWith is ServerRoutes with managed-upstream overrides. Domains
// served through the server's active Cloudflare Tunnel are flagged Tunnel;
// proxied domains that are locked to Cloudflare carry the origin lock
// (DomainRule.OriginLocked, Route.OriginLock; see cloudflare.ApplyOriginLock).
// Domains in a zone of the team's connected Cloudflare account get the
// automatic DNS-01 resolver (DomainRule.DNSResolver cloudflare, dns01.ApplyAuto).
// The domains of platform services are restricted last, to internal networks
// and the workspace's servers (withPlatformAccess); an IP allow list on a
// tunnelled domain admits nobody (withTunnelAccess).
func ServerRoutesWith(ctx context.Context, q *db.Queries, serverID uuid.UUID, exclude uuid.UUID, over Overrides) ([]agentprotocol.Route, error) {
	routes, err := serverRoutesWith(ctx, q, serverID, exclude, over)
	if err != nil {
		return nil, err
	}
	if routes, err = withSystemRoute(ctx, q, serverID, routes); err != nil {
		return nil, err
	}
	if routes, err = withStatusRoutes(ctx, q, serverID, routes); err != nil {
		return nil, err
	}
	if routes, err = markTunnelled(ctx, q, serverID, routes); err != nil {
		return nil, err
	}
	if err := dns01.ApplyAuto(ctx, q, serverID, routes); err != nil {
		return nil, err
	}
	if err := cloudflare.ApplyOriginLock(ctx, q, serverID, routes); err != nil {
		return nil, err
	}
	routes, err = withPlatformAccess(ctx, q, serverID, routes)
	if err != nil {
		return nil, err
	}
	// Last: an IP allow list on a tunnelled domain cannot be enforced (the peer
	// is the tunnel connector) and admits nobody instead (withTunnelAccess).
	return withTunnelAccess(routes), nil
}

// markTunnelled sets DomainRule.Tunnel on the domains whose DNS record is the
// server's tunnel CNAME: the tunnel is active and the domain is managed in a
// connected Cloudflare zone.
func markTunnelled(ctx context.Context, q *db.Queries, serverID uuid.UUID, routes []agentprotocol.Route) ([]agentprotocol.Route, error) {
	var ids []uuid.UUID
	for _, rt := range routes {
		for _, d := range rt.Domains {
			if id, err := uuid.Parse(d.ID); err == nil {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return routes, nil
	}
	tunnelled, err := q.ListTunnelledDomainIDs(ctx, db.ListTunnelledDomainIDsParams{DomainIds: ids, ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("list tunnelled domains: %w", err)
	}
	if len(tunnelled) == 0 {
		return routes, nil
	}
	set := make(map[string]bool, len(tunnelled))
	for _, id := range tunnelled {
		set[id.String()] = true
	}
	for i := range routes {
		for j := range routes[i].Domains {
			routes[i].Domains[j].Tunnel = set[routes[i].Domains[j].ID]
		}
	}
	return routes, nil
}

func serverRoutesWith(ctx context.Context, q *db.Queries, serverID uuid.UUID, exclude uuid.UUID, over Overrides) ([]agentprotocol.Route, error) {
	rows, err := q.ListServerRouteDomains(ctx, &serverID)
	if err != nil {
		return nil, fmt.Errorf("list route domains: %w", err)
	}
	// A service whose applied tailnet configuration is "Tailnet only" keeps its
	// domains but is never routed publicly.
	hidden, err := q.ListHiddenPublicServices(ctx, &serverID)
	if err != nil {
		return nil, fmt.Errorf("list tailnet-only services: %w", err)
	}
	tailnetOnly := make(map[uuid.UUID]bool, len(hidden))
	for _, id := range hidden {
		tailnetOnly[id] = true
	}
	// One http Route per service and process: the agent names Traefik objects
	// after the route's service id, which is the service's own for its default
	// process (domains.component "") and a ProcessRouteID for a named one. A
	// process has a single HTTP port (domain target ports always equal it), so the
	// first domain's port decides the upstreams.
	type routeKey struct {
		service uuid.UUID
		process string
	}
	idx := map[routeKey]int{}
	routes := []agentprotocol.Route{}
	stacks := newStackRoutes()
	for _, r := range rows {
		if r.ServiceID == exclude || tailnetOnly[r.ServiceID] {
			continue
		}
		if r.ServiceKind == services.KindStack {
			// A stack routes per (component, port): one Route each, named by StackRouteID.
			stacks.add(r.ServiceID, r.EnvironmentID, services.ReleaseBlocksEgress(r.ReleaseConfig), r.Domain)
			continue
		}
		k := routeKey{r.ServiceID, r.Domain.Component}
		i, ok := idx[k]
		if !ok {
			// The default process runs what a deploy of the release runs: a
			// singleton or files-bearing service has one replica whatever its setting.
			n := int(r.Replicas)
			if len(r.ReleaseConfig) > 0 {
				snap := services.DecodeSnapshot(r.ReleaseConfig)
				n = services.EffectiveReplicas(r.ServiceKind, snap.Config, len(snap.Volumes), n)
			}
			rt := agentprotocol.Route{
				ServiceID:  r.ServiceID.String(),
				Upstreams:  Upstreams(r.ServiceSlug, r.DeploymentID.String(), n, int(r.Domain.TargetPort)),
				HealthPath: healthPath(r.ReleaseConfig),
				Networks:   services.RouteNetworks(r.EnvironmentID, services.ReleaseBlocksEgress(r.ReleaseConfig)),
				RouteExt:   agentprotocol.RouteExt{Maintenance: maintenanceFrom(r.MaintenanceEnabled, r.MaintenanceTitle, r.MaintenanceMessage, r.MaintenanceEta)},
			}
			if r.Domain.Component != "" {
				if rt, ok = processRoute(r); !ok {
					continue // the running release has no such process (yet): nothing to route to
				}
			}
			i = len(routes)
			idx[k] = i
			routes = append(routes, rt)
		}
		routes[i].Domains = append(routes[i].Domains, RuleFromRow(r.Domain))
		if !slices.Contains(routes[i].Hosts, r.Domain.Hostname) {
			routes[i].Hosts = append(routes[i].Hosts, r.Domain.Hostname)
		}
	}
	routes = append(routes, stacks.routes...)

	rs, err := q.ListServerRouteServices(ctx, &serverID)
	if err != nil {
		return nil, fmt.Errorf("list route services: %w", err)
	}
	live := rs[:0:0]
	for _, r := range rs {
		if r.ServiceID != exclude {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return routes, nil
	}
	ids := make([]uuid.UUID, len(live))
	for i, r := range live {
		ids[i] = r.ServiceID
	}
	doms, err := q.ListDomainsForServices(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list route service domains: %w", err)
	}
	domsBySvc := map[uuid.UUID][]db.Domain{}
	for _, d := range doms {
		domsBySvc[d.ServiceID] = append(domsBySvc[d.ServiceID], d)
	}
	res, err := newTargetResolver(ctx, q, serverID, over, live)
	if err != nil {
		return nil, err
	}
	for _, r := range live {
		cfgRaw := r.ReleaseConfig
		var rc *services.RouteConfig
		if len(cfgRaw) > 0 {
			rc = services.DecodeSnapshot(cfgRaw).Config.Route
		}
		if rc == nil {
			rc = services.DecodeConfig(r.ServiceConfig).Route
		}
		if rc == nil {
			continue
		}
		if rt, ok := buildRouteRoute(r.ServiceID, *rc, domsBySvc[r.ServiceID], res); ok {
			if !rt.IsL4() {
				rt.Maintenance = maintenanceFrom(r.MaintenanceEnabled, r.MaintenanceTitle, r.MaintenanceMessage, r.MaintenanceEta)
			}
			routes = append(routes, rt)
		}
	}
	return routes, nil
}

// RouteServiceRoute builds the table entry of one route service from cfg and
// its live domains. ok is false when the route has nothing to route (an http
// route without domains, or without a resolvable upstream). serverID is the
// server the route will run on; over supplies targets that are not current yet.
func RouteServiceRoute(ctx context.Context, q *db.Queries, serviceID, serverID uuid.UUID, cfg services.RouteConfig, over Overrides) (agentprotocol.Route, bool, error) {
	doms, err := q.ListServiceDomains(ctx, serviceID)
	if err != nil {
		return agentprotocol.Route{}, false, fmt.Errorf("list domains: %w", err)
	}
	res, err := newTargetResolver(ctx, q, serverID, over, []db.ListServerRouteServicesRow{{ServiceID: serviceID, ServiceConfig: mustJSON(services.Config{Route: &cfg})}})
	if err != nil {
		return agentprotocol.Route{}, false, err
	}
	rt, ok := buildRouteRoute(serviceID, cfg, doms, res)
	return rt, ok, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// targetResolver maps managed upstreams to container names on one server.
type targetResolver struct {
	byID map[uuid.UUID]Target
}

func newTargetResolver(ctx context.Context, q *db.Queries, serverID uuid.UUID, over Overrides, rs []db.ListServerRouteServicesRow) (*targetResolver, error) {
	t := &targetResolver{byID: map[uuid.UUID]Target{}}
	var want []uuid.UUID
	for _, r := range rs {
		cfg := services.DecodeConfig(r.ServiceConfig)
		if len(r.ReleaseConfig) > 0 {
			if snap := services.DecodeSnapshot(r.ReleaseConfig).Config; snap.Route != nil {
				cfg = snap
			}
		}
		if cfg.Route == nil {
			continue
		}
		for _, u := range cfg.Route.Upstreams {
			if u.ServiceID != nil {
				if _, ok := over[*u.ServiceID]; !ok {
					want = append(want, *u.ServiceID)
				}
			}
		}
	}
	if len(want) > 0 {
		rows, err := q.ListManagedTargets(ctx, want)
		if err != nil {
			return nil, fmt.Errorf("list managed targets: %w", err)
		}
		for _, r := range rows {
			if r.ServerID != nil && *r.ServerID == serverID {
				t.byID[r.ServiceID] = Target{
					Slug: r.ServiceSlug, DeploymentID: r.DeploymentID.String(), Replicas: int(r.Replicas),
					Networks: services.RouteNetworks(r.EnvironmentID, services.ReleaseBlocksEgress(r.ReleaseConfig)),
				}
			}
		}
	}
	for id, tg := range over {
		t.byID[id] = tg
	}
	return t, nil
}

// buildRouteRoute turns a route service config into a table entry.
func buildRouteRoute(serviceID uuid.UUID, cfg services.RouteConfig, doms []db.Domain, res *targetResolver) (agentprotocol.Route, bool) {
	var ups, nets []string
	external := false
	for _, u := range cfg.Upstreams {
		if u.ServiceID == nil {
			if u.Address != "" {
				ups = append(ups, u.Address)
				external = true
			}
			continue
		}
		tg, ok := res.byID[*u.ServiceID]
		if !ok {
			continue
		}
		ups = append(ups, Upstreams(tg.Slug, tg.DeploymentID, tg.Replicas, u.Port)...)
		for _, n := range tg.Networks {
			if !slices.Contains(nets, n) {
				nets = append(nets, n)
			}
		}
	}
	if len(ups) == 0 {
		return agentprotocol.Route{}, false
	}
	rt := agentprotocol.Route{ServiceID: serviceID.String(), Upstreams: ups, Networks: nets}
	if external {
		rt.Kind = agentprotocol.RouteKindExternal
	}
	switch cfg.Protocol {
	case agentprotocol.ProtoTCP, agentprotocol.ProtoUDP:
		rt.Protocol, rt.ListenPort = cfg.Protocol, cfg.ListenPort
		rt.AllowCIDRs = slices.Clone(cfg.AllowCIDRs)
		rt.DenyCIDRs = slices.Clone(cfg.DenyCIDRs)
		rt.ProxyProtocol = cfg.ProxyProtocol
		rt.Hosts = []string{}
		return rt, true
	}
	if len(doms) == 0 {
		return agentprotocol.Route{}, false
	}
	rt.Domains, rt.Hosts = RulesAndHosts(doms)
	rt.Scheme = cfg.Scheme
	if cfg.Scheme == "http" {
		rt.Scheme = ""
	}
	if cfg.InsecureSkipVerify || cfg.ServerName != "" {
		rt.Transport = &agentprotocol.UpstreamTransport{InsecureSkipVerify: cfg.InsecureSkipVerify, ServerName: cfg.ServerName}
	}
	rt.PassHost = cfg.PassHost
	rt.HealthPath = cfg.HealthPath
	rt.VerifySkip = cfg.VerifySkip
	return rt, true
}

// Upstreams returns "<containerName>:<port>" for each of n replicas of a
// deployment (at least one).
func Upstreams(slug, deploymentID string, n, port int) []string {
	names := agentprotocol.ContainerNames(slug, deploymentID, max(n, 1))
	out := make([]string, len(names))
	p := strconv.Itoa(port)
	for i, name := range names {
		out[i] = net.JoinHostPort(name, p)
	}
	return out
}

// healthPath is the proxy health check path of a release snapshot: the http
// health check's path, empty for every other type.
func healthPath(releaseConfig json.RawMessage) string {
	if len(releaseConfig) == 0 {
		return ""
	}
	hc := services.DecodeSnapshot(releaseConfig).Config.Healthcheck
	if hc.Type != services.HealthHTTP {
		return ""
	}
	if hc.Path == "" {
		return "/"
	}
	return hc.Path
}

// AuthSecrets returns the send-time basic-auth secrets of the given route
// tables: SecretAuthPrefix+<domain id> → "user:bcrypt" lines for every
// DomainRule with an AuthRef. Never persist the result.
func AuthSecrets(ctx context.Context, q *db.Queries, box *secretbox.Box, tables ...[]agentprotocol.Route) (map[string]string, error) {
	var ids []uuid.UUID
	refs := map[uuid.UUID]string{}
	for _, routes := range tables {
		for _, rt := range routes {
			for _, d := range rt.Domains {
				if d.AuthRef == "" {
					continue
				}
				id, err := uuid.Parse(d.ID)
				if err != nil {
					return nil, fmt.Errorf("domain id %q: %w", d.ID, err)
				}
				if _, seen := refs[id]; !seen {
					ids = append(ids, id)
					refs[id] = d.AuthRef
				}
			}
		}
	}
	return authSecretsFor(ctx, q, box, ids, refs)
}

// RuleSecrets is AuthSecrets for loose domain rules (a deploy payload's DomainRules).
func RuleSecrets(ctx context.Context, q *db.Queries, box *secretbox.Box, rules []agentprotocol.DomainRule) (map[string]string, error) {
	var ids []uuid.UUID
	refs := map[uuid.UUID]string{}
	for _, d := range rules {
		if d.AuthRef == "" {
			continue
		}
		id, err := uuid.Parse(d.ID)
		if err != nil {
			return nil, fmt.Errorf("domain id %q: %w", d.ID, err)
		}
		if _, seen := refs[id]; !seen {
			ids = append(ids, id)
			refs[id] = d.AuthRef
		}
	}
	return authSecretsFor(ctx, q, box, ids, refs)
}

func authSecretsFor(ctx context.Context, q *db.Queries, box *secretbox.Box, ids []uuid.UUID, refs map[uuid.UUID]string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListDomainAuthHashes(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list auth users: %w", err)
	}
	for _, r := range rows {
		hash, err := box.OpenString(secretbox.DomainAuthUsersHash, secretbox.RowID(r.DomainID.String(), r.Username), r.HashEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt auth hash: %w", err)
		}
		line := r.Username + ":" + hash
		ref := refs[r.DomainID]
		if out[ref] != "" {
			out[ref] += "\n"
		}
		out[ref] += line
	}
	return out, nil
}
