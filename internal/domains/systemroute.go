package domains

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// withSystemRoute appends the dashboard's system route when serverID is the
// control-plane host and the route is configured (see package systemroute).
func withSystemRoute(ctx context.Context, q *db.Queries, serverID uuid.UUID, routes []agentprotocol.Route) ([]agentprotocol.Route, error) {
	cfg, ok := systemroute.Current()
	if !ok {
		return routes, nil
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return routes, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load server for the system route: %w", err)
	}
	if !srv.IsControlPlane {
		return routes, nil
	}
	// A route may name a DNS-01 resolver only when the server has one of that
	// provider (the same rule ordinary domains are saved under); otherwise the
	// dashboard keeps the automatic HTTP-01 certificate. A resolver that is
	// staged but not yet applied is also safe: the agent's builder serves the
	// host over HTTP-01 until Deploy proxy applies it.
	if cfg.DNSResolver != "" {
		has, err := dns01.HasResolver(ctx, q, serverID, cfg.DNSResolver)
		if err != nil {
			return nil, err
		}
		if !has {
			cfg.DNSResolver = ""
		}
	}
	rt := cfg.Route()
	rt.System = true // the control plane's own container: the agent does not resolve and pin it
	return append(routes, rt), nil
}

// withStatusRoutes appends one public route per published status page that has
// a hostname when serverID is the control-plane host (see package
// statusroute): the page's hostname goes to the status listener, never to the
// dashboard. A page whose hostname another route of the table already serves
// (the dashboard's, or a service domain) is skipped, so a status page can
// never shadow or duplicate an existing route.
//
// The certificate is DNS-01 through Cloudflare when the page's host is in a zone
// of the control-plane team's own connected Cloudflare account
// (dns01.AutoStatusPages); otherwise HTTP-01. The dashboard's DNS-01 provider is
// the instance's own credential: it is never used for a status page, whatever
// the hostname, because a team could otherwise make the instance issue
// certificates for any name under the dashboard's domain and use up its
// Let's Encrypt allowance. Other teams' pages use HTTP-01 (their DNS check must
// pass first); a team that wants DNS-01 connects its own Cloudflare account.
func withStatusRoutes(ctx context.Context, q *db.Queries, serverID uuid.UUID, routes []agentprotocol.Route) ([]agentprotocol.Route, error) {
	upstream, ok := statusroute.Upstream()
	if !ok {
		return routes, nil
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return routes, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load server for the status routes: %w", err)
	}
	if !srv.IsControlPlane {
		return routes, nil
	}
	pages, err := q.ListPublishedStatusPageHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list published status pages: %w", err)
	}
	if len(pages) == 0 {
		return routes, nil
	}
	auto, err := dns01.AutoStatusPages(ctx, q, serverID)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, rt := range routes {
		for _, h := range rt.Hosts {
			taken[strings.ToLower(h)] = true
		}
	}
	for _, p := range pages {
		if taken[p.Hostname] {
			continue
		}
		taken[p.Hostname] = true
		useResolver := ""
		if auto[p.ID] {
			useResolver = dns01.AutoProvider
		}
		rt := statusroute.Route(upstream, p.Hostname, p.ID, useResolver)
		rt.System = true // the control plane's own container: the agent does not resolve and pin it
		routes = append(routes, rt)
	}
	return routes, nil
}
