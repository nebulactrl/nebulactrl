package proxyserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ProxyRouteHost is one hostname rule of a route in the read-only table. Auth
// is shown as a flag only: hashes never leave the control plane.
type ProxyRouteHost struct {
	Host          string `json:"host"`
	PathPrefix    string `json:"pathPrefix"`
	Kind          string `json:"kind" enum:"serve,redirect"`
	RedirectTo    string `json:"redirectTo,omitempty"`
	TLSMode       string `json:"tlsMode" enum:"auto,custom,none"`
	BasicAuth     bool   `json:"basicAuth"`
	AccessList    bool   `json:"accessList" doc:"An IP allowlist is set"`
	RateLimit     bool   `json:"rateLimit"`
	CertificateID string `json:"certificateId,omitempty"`
}

// ProxyRoute is one route of the effective proxy table of a server.
type ProxyRoute struct {
	ServiceID     uuid.UUID        `json:"serviceId"`
	ServiceName   string           `json:"serviceName"`
	Protocol      string           `json:"protocol" enum:"http,tcp,udp"`
	Kind          string           `json:"kind" enum:"service,external"`
	Hosts         []ProxyRouteHost `json:"hosts"`
	ListenPort    int              `json:"listenPort,omitempty"`
	Upstreams     []string         `json:"upstreams"`
	AllowCIDRs    []string         `json:"allowCidrs"`
	DenyCIDRs     []string         `json:"denyCidrs"`
	ProxyProtocol int              `json:"proxyProtocol,omitempty"`
}

// Routes returns the effective route table of a server (what the next push
// carries), read-only. Secrets are masked by construction: the table never
// contains them.
func (s *Service) Routes(ctx context.Context, serverID uuid.UUID) ([]ProxyRoute, error) {
	if _, err := s.loadServer(ctx, s.q, policy.ScopeServersRead, serverID); err != nil {
		return nil, err
	}
	if s.routes == nil {
		return []ProxyRoute{}, nil
	}
	table, err := s.routes(ctx, s.q, serverID)
	if err != nil {
		return nil, fmt.Errorf("build route table: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(table))
	for _, r := range table {
		if id, err := uuid.Parse(r.ServiceID); err == nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	names := map[uuid.UUID]string{}
	rows, err := s.q.ListServiceNamesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("service names: %w", err)
	}
	for _, r := range rows {
		names[r.ID] = r.Name
	}
	if id, err := uuid.Parse(systemroute.ServiceID); err == nil {
		names[id] = systemroute.Name
	}
	// Status page routes are not services either: name them after their host.
	pages, err := s.q.ListPublishedStatusPageHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("status page hosts: %w", err)
	}
	for _, p := range pages {
		if id, err := uuid.Parse(statusroute.ServiceID(p.ID)); err == nil {
			names[id] = "Status page " + p.Hostname
		}
	}
	out := make([]ProxyRoute, 0, len(table))
	for _, r := range table {
		sid, _ := uuid.Parse(r.ServiceID)
		rs := ProxyRoute{
			ServiceID: sid, ServiceName: names[sid], Protocol: orDefault(r.Protocol, agentprotocol.ProtoHTTP),
			Kind: orDefault(r.Kind, agentprotocol.RouteKindService), Hosts: []ProxyRouteHost{}, ListenPort: r.ListenPort,
			Upstreams: nonNilStrings(r.Upstreams), AllowCIDRs: nonNilStrings(r.AllowCIDRs), DenyCIDRs: nonNilStrings(r.DenyCIDRs), ProxyProtocol: r.ProxyProtocol,
		}
		if len(r.Domains) == 0 {
			for _, h := range r.Hosts {
				rs.Hosts = append(rs.Hosts, ProxyRouteHost{Host: h, Kind: agentprotocol.DomainServe, TLSMode: agentprotocol.TLSAuto})
			}
		}
		for _, d := range r.Domains {
			rs.Hosts = append(rs.Hosts, ProxyRouteHost{
				Host: d.Host, PathPrefix: d.PathPrefix, Kind: orDefault(d.Kind, agentprotocol.DomainServe), RedirectTo: d.RedirectTo,
				TLSMode: orDefault(d.TLSMode, agentprotocol.TLSAuto), BasicAuth: d.Options.BasicAuth,
				AccessList: d.Options.Access != nil, RateLimit: d.Options.RateLimit != nil, CertificateID: d.CertificateID,
			})
		}
		out = append(out, rs)
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// PushRoutes queues a sync_routes command carrying the server's full current
// route table and applied proxy config, inside tx. Features that change how
// domains are rendered without touching a service (the Cloudflare origin lock
// and its IP list) use it: a route update only, no redeploy. Revoked servers
// and a Service without a route source are skipped.
func (s *Service) PushRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error {
	if s.routes == nil {
		return nil
	}
	q := db.New(tx)
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return nil
	}
	seq, err := q.NumberRouteTable(ctx, serverID) // before the routes are read; the counter lock is the last this transaction takes (see domains.ServerTable)
	if err != nil {
		return fmt.Errorf("number the route table: %w", err)
	}
	routes, err := s.routes(ctx, q, serverID)
	if err != nil {
		return err
	}
	pc, err := BuildConfig(ctx, q, serverID)
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
