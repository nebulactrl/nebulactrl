package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// entrypointsOfHost returns the entrypoints of every router whose rule names host.
func entrypointsOfHost(p parsed, host string) map[string]bool {
	out := map[string]bool{}
	for _, r := range p.HTTP.Routers {
		if rule, _ := r["rule"].(string); strings.Contains(rule, "Host(`"+host+"`)") {
			for _, e := range strs(r["entryPoints"]) {
				out[e] = true
			}
		}
	}
	return out
}

// A tunnelled domain is reachable only through cloudflared: the proxy serves it
// on the unpublished tunnel entrypoint (plain HTTP, no redirect, no
// certificate) and on no public entrypoint, so a request for the name at the
// server's address never reaches the app around Cloudflare.
func TestTunnelledDomainIsServedOnTheTunnelEntrypointOnly(t *testing.T) {
	route := ruleRoute(
		rule(domA, "tunnel.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel = true }),
		rule(domB, "direct.example.com", nil),
	)
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	tun := "nc-" + svcApp + "-daaaaaaaa"
	dir := "nc-" + svcApp + "-dbbbbbbbb"

	require.Equal(t, map[string]bool{EntryTunnel: true, EntryProbe: true}, entrypointsOfHost(p, "tunnel.example.com"))
	served := p.HTTP.Routers[tun+"-tunnel"]
	require.Equal(t, "nc-"+svcApp, served["service"])
	require.NotContains(t, served, "tls")
	mws, _ := served["middlewares"].([]any)
	require.NotContains(t, mws, redirectMiddleware)

	// The direct domain keeps the public entrypoints, HTTP-01 and the redirect.
	require.Equal(t, map[string]bool{EntryWeb: true, EntryWebSecure: true, EntryProbe: true}, entrypointsOfHost(p, "direct.example.com"))
	require.Equal(t, map[string]any{"certResolver": "le"}, p.HTTP.Routers[dir+"-https"]["tls"])
	require.Equal(t, []string{redirectMiddleware}, strs(p.HTTP.Routers[dir+"-http"]["middlewares"]))
}

// Whatever a tunnelled rule asks for (a DNS-01 resolver the proxy runs, no
// forced HTTPS, a redirect), it gets no router on a public entrypoint and no
// certificate at the origin.
func TestTunnelledVariantsStayOffThePublicEntrypoints(t *testing.T) {
	route := ruleRoute(
		rule(domA, "a.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel, d.DNSResolver = true, agentprotocol.DNSCloudflare }),
		rule(domB, "b.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel = true; d.Options.ForceHTTPS = new(false) }),
		rule(domC, "go.example.com", func(d *agentprotocol.DomainRule) {
			d.Tunnel, d.Kind, d.RedirectTo = true, agentprotocol.DomainRedirect, "a.example.com"
		}),
	)
	opts := Options{
		ACMEEmail:    "ops@example.com",
		DNSResolvers: []agentprotocol.DNSResolver{{ID: "cred1", Provider: agentprotocol.DNSCloudflare, EnvNames: []string{"CF_DNS_API_TOKEN"}}},
	}
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, opts))
	for _, host := range []string{"a.example.com", "b.example.com", "go.example.com"} {
		eps := entrypointsOfHost(p, host)
		require.False(t, eps[EntryWeb] || eps[EntryWebSecure], "%s must not be on a public entrypoint: %v", host, eps)
		require.True(t, eps[EntryTunnel], host)
	}
	for name, r := range p.HTTP.Routers {
		if strings.Contains(name, "-daaaaaaaa") || strings.Contains(name, "-dbbbbbbbb") || strings.Contains(name, "-dcccccccc") {
			require.NotContains(t, r, "tls", "router %s requests no origin certificate", name)
		}
	}
	require.Equal(t, "noop@internal", p.HTTP.Routers["nc-"+svcApp+"-dcccccccc-redir"]["service"])
	require.Empty(t, acmeTargets(agentprotocol.ProxyConfig{DNSResolvers: opts.DNSResolvers}, []agentprotocol.Route{route}), "no origin certificate to observe")
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
