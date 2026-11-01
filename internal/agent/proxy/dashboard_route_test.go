package proxy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The dashboard's system route, rendered through the real builder: the default
// route has no allow list and uses the HTTP-01 resolver, an allow list becomes an
// ipAllowList on the https router (not the verification probe), and a DNS-01
// provider becomes its certResolver.
func TestDashboardSystemRouteGolden(t *testing.T) {
	base := systemroute.Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"}
	n := "nc-" + systemroute.ServiceID + "-d" + systemroute.DomainID[:8]
	opts := Options{ACMEEmail: "ops@example.com", DNSResolvers: []agentprotocol.DNSResolver{dnsResolver("cloudflare", map[string]string{"CF_DNS_API_TOKEN": "tok"}, nil)}}

	t.Run("default", func(t *testing.T) {
		raw := goldenConfig(t, "dashboard_default", []agentprotocol.Route{base.Route()}, opts)
		p := parse(t, raw)
		require.NotContains(t, p.HTTP.Middlewares, n+"-ipallow")
		require.Equal(t, map[string]any{"certResolver": CertResolver}, p.HTTP.Routers[n+"-https"]["tls"])
	})

	t.Run("allow list and DNS-01", func(t *testing.T) {
		cfg := base
		cfg.AllowCIDRs = []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}
		cfg.DNSResolver = "cloudflare"
		raw := goldenConfig(t, "dashboard_allowlist_dns01", []agentprotocol.Route{cfg.Route()}, opts)
		p := parse(t, raw)
		mw := p.HTTP.Middlewares[n+"-ipallow"]["ipAllowList"].(map[string]any)
		require.Equal(t, []any{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, mw["sourceRange"])
		require.NotContains(t, mw, "ipStrategy", "the allow list checks the TCP peer: the proxy is the edge")
		require.Contains(t, strs(p.HTTP.Routers[n+"-https"]["middlewares"]), n+"-ipallow")
		require.NotContains(t, p.HTTP.Routers[n+"-probe"], "middlewares")
		require.Equal(t, map[string]any{"certResolver": "nc-dns-cloudflare"}, p.HTTP.Routers[n+"-https"]["tls"])
		require.NotContains(t, string(raw), "tok\"", "no credential in the dynamic config")
	})

	t.Run("resolver the proxy has not applied falls back to HTTP-01", func(t *testing.T) {
		cfg := base
		cfg.DNSResolver = "cloudflare"
		p := parse(t, goldenConfigNoFile(t, []agentprotocol.Route{cfg.Route()}, Options{ACMEEmail: "ops@example.com"}))
		require.Equal(t, map[string]any{"certResolver": CertResolver}, p.HTTP.Routers[n+"-https"]["tls"])
	})
}
