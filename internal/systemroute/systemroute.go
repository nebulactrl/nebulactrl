// Package systemroute serves the dashboard through the control-plane host's own
// proxy.
//
// The installer puts the control plane in a container on the host's edge
// network and enrolls the host as the first server. That server's Traefik (the
// only proxy on the machine, so it owns ports 80 and 443) then carries one
// system route: the dashboard's hostname, with an automatic Let's Encrypt
// certificate, to the control-plane container. The route is not a service and
// has no domain row; the control plane adds it to the route table of the
// control-plane server wherever that table is built (domains.ServerRoutes), so
// every route push, deploy and reconcile carries it and no other route can
// displace it.
//
// The route exists only when NEBULACTRL_PUBLIC_URL is an https URL with a DNS
// name and NEBULACTRL_DASHBOARD_UPSTREAM is set (the installer sets both in
// domain mode). Without a domain the dashboard is served directly on
// http://<ip>:8000 and there is no system route.
package systemroute

import (
	"slices"
	"strings"
	"sync/atomic"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// ServiceID names the route in the table. It is a fixed, well-formed UUID
	// that no service can have (services get random v4 ids; this one has a
	// version-8 nibble), so API views and agent object names treat it like any
	// other route.
	ServiceID = "5eed0000-0000-8000-8000-00000000da5b"
	// DomainID is the id of the route's single DomainRule.
	DomainID = "5eed0000-0000-8000-8000-00000000d0a1"
	// Name is what the UI shows for the route.
	Name = "NebulaCtrl dashboard"
	// HealthPath is the upstream health check: liveness only, so a database
	// outage does not take the dashboard (and the page that explains it) down.
	HealthPath = "/healthz"
)

// Config is the system route's configuration.
type Config struct {
	// Host is the dashboard's hostname.
	Host string
	// Upstream is "host:port" of the control-plane container on the edge network.
	Upstream string
	// AllowCIDRs restricts who may reach the dashboard (an ipAllowList on the
	// route, evaluated against the TCP peer, which is the client because this
	// proxy is the edge). Empty means everybody.
	AllowCIDRs []string
	// DNSResolver is the DNS-01 provider that issues the dashboard's
	// certificate; empty means automatic HTTP-01. The proxy falls back to
	// HTTP-01 while it has not applied that provider's resolver.
	DNSResolver string
}

var current atomic.Pointer[Config]

// Configure installs the process-wide system route configuration from the
// control-plane configuration. A configuration without a dashboard hostname or
// upstream disables the route.
func Configure(cfg *config.Config) {
	if cfg == nil {
		current.Store(nil)
		return
	}
	// The installer's allow list applies from the first route push; the stored
	// settings (dashboardaccess) replace it once they are loaded.
	c := Config{Host: cfg.DashboardHost(), Upstream: strings.TrimSpace(cfg.Installer.DashboardUpstream), AllowCIDRs: slices.Clone(cfg.Installer.DashboardAllowCIDRs)}
	if c.Host == "" || c.Upstream == "" {
		current.Store(nil)
		return
	}
	current.Store(&c)
}

// SetAccess replaces the access settings of the installed configuration (the
// dashboardaccess row: the allow list and the DNS-01 provider) and returns the
// previous ones. It does nothing when the route is disabled.
func SetAccess(allow []string, resolver string) (prevAllow []string, prevResolver string) {
	for {
		old := current.Load()
		if old == nil {
			return nil, ""
		}
		next := *old
		next.AllowCIDRs = append([]string(nil), allow...)
		next.DNSResolver = resolver
		if current.CompareAndSwap(old, &next) {
			return old.AllowCIDRs, old.DNSResolver
		}
	}
}

// Set installs c directly (tests). The zero Config disables the route.
func Set(c Config) {
	if c.Host == "" || c.Upstream == "" {
		current.Store(nil)
		return
	}
	current.Store(&c)
}

// Current returns the configuration and whether the route is enabled.
func Current() (Config, bool) {
	c := current.Load()
	if c == nil {
		return Config{}, false
	}
	return *c, true
}

// Reserved reports whether host is the dashboard's hostname: no service domain
// may take it.
func Reserved(host string) bool {
	c, ok := Current()
	return ok && strings.EqualFold(strings.TrimSuffix(host, "."), c.Host)
}

// IsRoute reports whether the route's service id is ServiceID.
func IsRoute(serviceID string) bool { return serviceID == ServiceID }

// Route returns the system route for the control-plane server's table.
func (c Config) Route() agentprotocol.Route {
	// "strict" is "basic" plus HSTS. The route exists only with a domain (HTTPS),
	// so the header is always valid here: it stops the first visit and a typed
	// http:// URL from being downgraded before the 308 to https.
	opts := agentprotocol.RouteOptions{SecurityHeaders: "strict"}
	if len(c.AllowCIDRs) > 0 {
		opts.Access = &agentprotocol.Access{AllowCIDRs: slices.Clone(c.AllowCIDRs)}
	}
	return agentprotocol.Route{
		ServiceID:  ServiceID,
		Hosts:      []string{c.Host},
		Upstreams:  []string{c.Upstream},
		HealthPath: HealthPath,
		RouteExt: agentprotocol.RouteExt{
			// The upstream is a container NebulaCtrl does not manage by replica
			// naming, so it is an external upstream. It still passes the upstream
			// policy: the name is neither an address nor a nebulactrl-* container.
			Kind: agentprotocol.RouteKindExternal,
			Domains: []agentprotocol.DomainRule{{
				ID: DomainID, Host: c.Host, TLSMode: agentprotocol.TLSAuto, DNSResolver: c.DNSResolver,
				Options: opts,
			}},
		},
	}
}
