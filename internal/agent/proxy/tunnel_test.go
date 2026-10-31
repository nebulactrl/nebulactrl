package proxy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Cloudflare terminates the public TLS of a tunnelled domain and reaches the
// proxy over plain HTTP on the web entrypoint: the HTTP-01 resolver could never
// validate the name and an HTTPS redirect on port 80 would loop.
func TestTunnelledDomainSkipsHTTP01AndRedirect(t *testing.T) {
	route := ruleRoute(
		rule(domA, "tunnel.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel = true }),
		rule(domB, "direct.example.com", nil),
	)
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	tun := "nc-" + svcApp + "-daaaaaaaa"
	dir := "nc-" + svcApp + "-dbbbbbbbb"

	// The direct domain still gets HTTP-01 and the redirect.
	require.Equal(t, map[string]any{"certResolver": "le"}, p.HTTP.Routers[dir+"-https"]["tls"])
	require.Equal(t, "noop@internal", p.HTTP.Routers[dir+"-http"]["service"])
	require.Equal(t, []string{"nc-https-redirect"}, strs(p.HTTP.Routers[dir+"-http"]["middlewares"]))

	// The tunnelled one has a TLS router on the default certificate: no resolver.
	https := p.HTTP.Routers[tun+"-https"]
	require.Equal(t, map[string]any{}, https["tls"], "default certificate, no certResolver")
	require.NotContains(t, https["tls"], "certResolver")
	require.Equal(t, "nc-"+svcApp, https["service"])

	// Port 80 serves the app directly: no redirect middleware, no noop service.
	web := p.HTTP.Routers[tun+"-http"]
	require.Equal(t, []any{"web"}, web["entryPoints"])
	require.Equal(t, "nc-"+svcApp, web["service"])
	require.NotContains(t, web, "middlewares")

	// The probe router is unchanged.
	require.Contains(t, p.HTTP.Routers, tun+"-probe")
}

func TestTunnelledDomainNeverUsesLeResolverAnywhere(t *testing.T) {
	route := ruleRoute(
		rule(domA, "a.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel = true }),
		rule(domB, "b.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel = true; d.Options.ForceHTTPS = new(false) }),
		rule(domC, "go.example.com", func(d *agentprotocol.DomainRule) {
			d.Tunnel, d.Kind, d.RedirectTo = true, agentprotocol.DomainRedirect, "a.example.com"
		}),
	)
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	for name, r := range p.HTTP.Routers {
		if tls, ok := r["tls"].(map[string]any); ok {
			require.NotEqual(t, "le", tls["certResolver"], "router %s must not use HTTP-01", name)
		}
	}
	// A redirect-only tunnelled domain keeps its redirect on both entrypoints.
	require.Equal(t, "noop@internal", p.HTTP.Routers["nc-"+svcApp+"-dcccccccc-redir"]["service"])
	require.Equal(t, "noop@internal", p.HTTP.Routers["nc-"+svcApp+"-dcccccccc-http"]["service"])
}

// A tunnelled domain that names a DNS-01 resolver keeps it: the origin
// certificate is then valid, which allows Cloudflare's "Full (strict)" mode.
func TestTunnelledDomainKeepsDNS01Resolver(t *testing.T) {
	route := ruleRoute(rule(domA, "a.example.com", func(d *agentprotocol.DomainRule) {
		d.Tunnel, d.DNSResolver = true, "cloudflare"
	}))
	opts := Options{
		ACMEEmail:    "ops@example.com",
		DNSResolvers: []agentprotocol.DNSResolver{{ID: "cred1", Provider: "cloudflare", EnvNames: []string{"CF_DNS_API_TOKEN"}}},
	}
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, opts))
	tls, _ := p.HTTP.Routers["nc-"+svcApp+"-daaaaaaaa-https"]["tls"].(map[string]any)
	require.Equal(t, agentprotocol.DNSResolverName("cloudflare"), tls["certResolver"])
}

func TestNonTunnelledDomainsAreUnchanged(t *testing.T) {
	route := ruleRoute(rule(domA, "a.example.com", nil))
	a := mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"})
	route.Domains[0].Tunnel = false
	require.JSONEq(t, string(a), string(mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"})))
}

func mustBuild(t *testing.T, routes []agentprotocol.Route, opts Options) []byte {
	t.Helper()
	if opts.Generation == 0 {
		opts.Generation = 1
	}
	got, err := BuildConfig(routes, opts)
	require.NoError(t, err)
	return got
}
