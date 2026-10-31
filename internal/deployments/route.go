package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// StrategyRouteOnly is the strategy of a route service deployment: nothing is
// built or started, only the route table is pushed (sync_routes).
const StrategyRouteOnly = "route_only"

// routePayload builds the sync_routes payload of a route-only deployment: the
// server's full table including this route (which is not "current" yet, so its
// entry is built from the new snapshot config), the applied proxy config and
// the deployment id the agent reports its route/verify steps against.
func routePayload(ctx context.Context, q *db.Queries, sc services.Scope, depID uuid.UUID, rc services.RouteConfig, srv db.Server) (agentprotocol.SyncRoutesPayload, error) {
	routes, err := domains.ServerRoutes(ctx, q, srv.ID, sc.Service.ID)
	if err != nil {
		return agentprotocol.SyncRoutesPayload{}, err
	}
	rt, ok, err := domains.RouteServiceRoute(ctx, q, sc.Service.ID, srv.ID, rc, nil)
	if err != nil {
		return agentprotocol.SyncRoutesPayload{}, err
	}
	if ok {
		if !rt.IsL4() {
			rt.Maintenance = domains.MaintenanceOf(sc.Service)
		}
		routes = append(routes, rt)
	}
	pc, err := proxyserver.BuildConfig(ctx, q, srv.ID)
	if err != nil {
		return agentprotocol.SyncRoutesPayload{}, err
	}
	pl := agentprotocol.SyncRoutesPayload{Routes: routes, Proxy: pc, DeploymentID: depID.String()}
	if srv.AcmeEmail != nil {
		pl.ACMEEmail = *srv.AcmeEmail
	}
	traffic := srv.TrafficAnalyticsEnabled
	pl.TrafficAnalytics = &traffic
	return pl, nil
}

// routeProblems lists what blocks deploying a route service right now.
func routeProblems(ctx context.Context, q *db.Queries, in *inputs) ([]PlanBlocking, error) {
	rc := in.cfg.Route
	if in.sc.Service.Kind != services.KindRoute || rc == nil || in.server == nil {
		return nil, nil
	}
	var out []PlanBlocking
	if !domains.SupportsProxyV2(*in.server) {
		out = append(out, PlanBlocking{Code: "agent_too_old", Message: "The agent on " + in.server.Name + " is too old for route services; update the agent first."})
	}
	if rc.Protocol == agentprotocol.ProtoHTTP && len(in.domains) == 0 {
		out = append(out, PlanBlocking{Code: "route_needs_domain", Message: "Add a domain to this route: an http route without a hostname publishes nothing."})
	}
	var want []uuid.UUID
	for _, u := range rc.Upstreams {
		if u.ServiceID != nil {
			want = append(want, *u.ServiceID)
		}
	}
	if len(want) > 0 {
		targets, err := q.ListManagedTargets(ctx, want)
		if err != nil {
			return nil, fmt.Errorf("list managed targets: %w", err)
		}
		running := map[uuid.UUID]bool{}
		for _, t := range targets {
			running[t.ServiceID] = t.ServerID != nil && *t.ServerID == in.server.ID
		}
		for _, id := range want {
			if !running[id] {
				name := id.String()
				if row, err := q.LoadServiceNameByID(ctx, id); err == nil {
					name = row
				}
				out = append(out, PlanBlocking{Code: "upstream_not_deployed", Message: "The target service " + name + " is not deployed on " + in.server.Name + " yet: deploy it first."})
			}
		}
	}
	return out, nil
}

// routeWarnings lists the non-blocking concerns of deploying a route service.
func (s *Service) routeWarnings(ctx context.Context, q *db.Queries, in *inputs) ([]string, error) {
	rc := in.cfg.Route
	var out []string
	if rc == nil || in.server == nil {
		return out, nil
	}
	switch rc.Protocol {
	case agentprotocol.ProtoHTTP:
		unguarded := false
		for _, d := range in.domains {
			o := services.DomainOptions(d.Options)
			if d.Kind == agentprotocol.DomainServe && !o.BasicAuth && (o.Access == nil || len(o.Access.AllowCIDRs) == 0) {
				unguarded = true
			}
		}
		if unguarded {
			out = append(out,
				"This route has no access control: anyone who can reach the hostname can reach the upstream. Add basic auth or an IP allowlist to the domain.",
				"Post-deploy verification checks routing, not access rules.")
		}
	case agentprotocol.ProtoUDP:
		out = append(out, "UDP routes cannot be restricted by IP (Traefik has no UDP middleware): use a firewall or VPN in front of the server.")
	case agentprotocol.ProtoTCP:
		if len(rc.AllowCIDRs) == 0 {
			out = append(out, fmt.Sprintf("TCP port %d is open to the internet. Restrict it with allowed CIDRs unless that is intended.", rc.ListenPort))
		}
	}
	if rc.Protocol != agentprotocol.ProtoHTTP {
		out = append(out, "Docker publishes this port directly and bypasses host firewalls such as ufw: restrict access with allowed CIDRs or a network firewall in front of the server.")
		applied, err := q.ListServerAppliedRoutePorts(ctx, &in.server.ID)
		if err != nil {
			return nil, fmt.Errorf("list applied ports: %w", err)
		}
		isNew := !slices.ContainsFunc(applied, func(p db.ListServerAppliedRoutePortsRow) bool {
			return p.Protocol == rc.Protocol && int(p.ListenPort) == rc.ListenPort
		})
		if isNew {
			out = append(out, fmt.Sprintf("New %s port %d restarts the proxy on %s (about 1s of dropped connections; HTTP is brief too).",
				upperProto(rc.Protocol), rc.ListenPort, in.server.Name))
		}
	}
	if in.server.ProxyStatus != nil && *in.server.ProxyStatus == agentprotocol.ProxyError {
		out = append(out, "The proxy on "+in.server.Name+" is not running, so this route will not publish.")
	}
	return out, nil
}

func upperProto(p string) string {
	if p == agentprotocol.ProtoUDP {
		return "UDP"
	}
	return "TCP"
}

// usesProxyV2 reports whether any domain needs a proxy-v2 agent (anything
// beyond a plain host serve rule).
func usesProxyV2(rows []db.Domain) bool {
	for _, d := range rows {
		if d.PathPrefix != "" || d.StripPrefix || d.AddPrefix != "" || d.Kind != agentprotocol.DomainServe ||
			d.TlsMode != agentprotocol.TLSAuto || d.CertificateID != nil || services.NormalizeOptions(d.Options) != "" {
			return true
		}
	}
	return false
}

// finalizeRoute promotes a successful route-only deployment. Moved to another
// server, the old server's table is pushed again without the route.
func (s *Service) finalizeRoute(ctx context.Context, tx pgx.Tx, dep db.Deployment, prev *uuid.UUID) error {
	q := db.New(tx)
	if prev == nil || *prev == dep.ID || dep.ServerID == nil {
		return nil
	}
	old, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*prev})
	if err != nil || len(old) != 1 || old[0].ServerID == nil || *old[0].ServerID == *dep.ServerID {
		return err
	}
	oldServer := *old[0].ServerID
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: oldServer})
	if err != nil {
		return err
	}
	routes, seq, err := domains.ServerTable(ctx, q, oldServer, uuid.Nil)
	if err != nil {
		return err
	}
	pc, err := proxyserver.BuildConfig(ctx, q, oldServer)
	if err != nil {
		return err
	}
	pl := agentprotocol.SyncRoutesPayload{Routes: routes, Proxy: pc, TableSeq: seq}
	if srv.AcmeEmail != nil {
		pl.ACMEEmail = *srv.AcmeEmail
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{ServerID: oldServer, Kind: agentprotocol.KindSyncRoutes, Payload: raw}); err != nil {
		s.Logger.WarnContext(ctx, "could not queue route removal on the previous server", "correlation_id", dep.ID.String(), "err", err)
	}
	return nil
}

// proxySecrets resolves the send-time secrets of route tables and a proxy
// config: basic-auth hashes, certificate keys and (when the config asks to
// provision) a freshly minted tailnet auth key for serverID.
func (s *Service) proxySecrets(ctx context.Context, serverID uuid.UUID, rules []agentprotocol.DomainRule, tables [][]agentprotocol.Route, pc *agentprotocol.ProxyConfig) (map[string]string, error) {
	out, err := domains.AuthSecrets(ctx, s.q, s.Box, tables...)
	if err != nil {
		return nil, err
	}
	if len(rules) > 0 {
		more, err := domains.RuleSecrets(ctx, s.q, s.Box, rules)
		if err != nil {
			return nil, err
		}
		for k, v := range more {
			out[k] = v
		}
	}
	certs, err := proxyserver.CertSecrets(ctx, s.q, s.Box, pc)
	if err != nil {
		return nil, err
	}
	for k, v := range certs {
		out[k] = v
	}
	// DNS-01 provider credentials: the proxy container's environment.
	dns, err := dns01.Secrets(ctx, s.q, s.Box, serverID, pc)
	if err != nil {
		return nil, err
	}
	for k, v := range dns {
		out[k] = v
	}
	if pc != nil && pc.Tailnet != nil && pc.Tailnet.Provision {
		if s.Tailnet == nil {
			return nil, errors.New("tailnet provisioning requested but no tailnet key minter is configured")
		}
		key, err := s.Tailnet.MintTailnetKey(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("mint tailnet key: %w", err)
		}
		out[agentprotocol.SecretTailnetAuthKey] = key
	}
	return out, nil
}

// ProxySecretsForServer returns the secrets of a server's whole current route
// table and applied proxy config (the reconcile frame, proxy_configure
// without routes).
func (s *Service) ProxySecretsForServer(ctx context.Context, serverID uuid.UUID, pc *agentprotocol.ProxyConfig, routes []agentprotocol.Route) (map[string]string, error) {
	return s.proxySecrets(ctx, serverID, nil, [][]agentprotocol.Route{routes}, pc)
}

// secretsForProxyCommand handles sync_routes and proxy_configure.
func (s *Service) secretsForProxyCommand(ctx context.Context, commandID uuid.UUID, kind string, payload json.RawMessage) (map[string]string, error) {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if err != nil {
		return nil, fmt.Errorf("get command: %w", err)
	}
	switch kind {
	case agentprotocol.KindSyncRoutes:
		var p agentprotocol.SyncRoutesPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode sync_routes payload: %w", err)
		}
		return s.proxySecrets(ctx, cmd.ServerID, nil, [][]agentprotocol.Route{p.Routes}, p.Proxy)
	default:
		var p agentprotocol.ProxyConfigurePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode proxy_configure payload: %w", err)
		}
		routes := p.Routes
		if routes == nil {
			// The agent keeps its own table: attach the secrets of the whole one.
			if routes, err = domains.ServerRoutes(ctx, s.q, cmd.ServerID, uuid.Nil); err != nil {
				return nil, err
			}
		}
		return s.proxySecrets(ctx, cmd.ServerID, nil, [][]agentprotocol.Route{routes}, &p.Proxy)
	}
}
