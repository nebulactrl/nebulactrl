// Package statusroute serves public status pages through the control-plane
// host's own proxy.
//
// A published status page with a hostname is rendered by the control plane's
// status listener (NEBULACTRL_STATUS_LISTEN, package statuspublic), a second
// HTTP server that serves nothing but status pages. The control-plane server's
// Traefik carries one system route per published page: the page's hostname,
// public (no allow list), with an automatic Let's Encrypt certificate, to that
// listener at NEBULACTRL_STATUS_UPSTREAM. The dashboard listener is never an
// upstream of such a route, so the dashboard, the API and the agent endpoints
// are unreachable on a status hostname.
//
// Like the dashboard's system route (package systemroute) these routes are not
// services and have no domain row: the control plane adds them to the route
// table of the control-plane server wherever that table is built
// (domains.ServerRoutes). Their ids are derived from the page id, so a route
// keeps its Traefik object names (and its certificate) across rebuilds.
package statusroute

import (
	"strings"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HealthPath is the upstream health check: liveness only, so a database
// outage does not take the page that explains it out of the proxy.
const HealthPath = "/healthz"

// namespace derives the route and domain ids; the nibbles make it a
// version-8-style UUID no service can have (services get random v4 ids).
var namespace = uuid.MustParse("5eed0000-0000-8000-8000-0000005747a5")

var current atomic.Pointer[string]

// Configure installs the status upstream from the control-plane
// configuration. An empty upstream disables the routes.
func Configure(cfg *config.Config) {
	if cfg == nil {
		Set("")
		return
	}
	Set(cfg.Installer.StatusUpstream)
}

// Set installs the upstream ("host:port") directly (tests). Empty disables the
// routes.
func Set(u string) {
	u = strings.TrimSpace(u)
	if u == "" {
		current.Store(nil)
		return
	}
	current.Store(&u)
}

// Upstream returns the status listener's address on the edge network and
// whether status routes are enabled.
func Upstream() (string, bool) {
	u := current.Load()
	if u == nil {
		return "", false
	}
	return *u, true
}

// ServiceID is the route id of a page: stable, well-formed and derived from
// the page id only (not its hostname), so renaming the hostname keeps the
// route's identity.
func ServiceID(pageID uuid.UUID) string {
	return uuid.NewSHA1(namespace, []byte("route/"+pageID.String())).String()
}

// DomainID is the id of the route's single DomainRule.
func DomainID(pageID uuid.UUID) string {
	return uuid.NewSHA1(namespace, []byte("domain/"+pageID.String())).String()
}

// Route returns the table entry of one published page: host to upstream,
// public, with the basic security headers. resolver is the DNS-01 provider
// that issues the certificate; empty means automatic HTTP-01.
func Route(upstream, host string, pageID uuid.UUID, resolver string) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID:  ServiceID(pageID),
		Hosts:      []string{host},
		Upstreams:  []string{upstream},
		HealthPath: HealthPath,
		RouteExt: agentprotocol.RouteExt{
			// The upstream is the control plane container, which NebulaCtrl
			// does not manage by replica naming: an external upstream, like
			// the dashboard's.
			Kind: agentprotocol.RouteKindExternal,
			Domains: []agentprotocol.DomainRule{{
				ID: DomainID(pageID), Host: host, TLSMode: agentprotocol.TLSAuto, DNSResolver: resolver,
				Options: agentprotocol.RouteOptions{SecurityHeaders: "basic"},
			}},
		},
	}
}
