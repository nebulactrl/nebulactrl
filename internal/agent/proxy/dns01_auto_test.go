package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The control plane names the cloudflare resolver on every rule whose hostname
// is in a zone of the team's connected Cloudflare account (internal/dns01,
// ApplyAuto). What the proxy does with that name depends on the resolvers the
// server runs: the tests below pin the agent's side of that contract.

func cfResolver() []agentprotocol.DNSResolver {
	return []agentprotocol.DNSResolver{dnsResolver("cloudflare", map[string]string{"CF_DNS_API_TOKEN": "tok"}, nil)}
}

func httpsTLS(p parsed, id string) any {
	return p.HTTP.Routers["nc-"+svcApp+"-d"+id[:8]+"-https"]["tls"]
}

// A rule that names the cloudflare resolver keeps the HTTP-01 resolver until the
// server's proxy runs a Cloudflare resolver, so the switch to DNS-01 happens
// with the proxy configuration that adds the resolver, never before it: the host
// never points at a resolver that does not exist.
func TestAutoCloudflareUsesHTTP01UntilTheResolverIsApplied(t *testing.T) {
	route := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver = agentprotocol.DNSCloudflare }))

	before := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	require.Equal(t, map[string]any{"certResolver": "le"}, httpsTLS(before, domA))

	after := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", DNSResolvers: cfResolver()}))
	require.Equal(t, map[string]any{"certResolver": agentprotocol.DNSResolverName("cloudflare")}, httpsTLS(after, domA))

	// Nothing else about the routers differs: only the certificate source moves.
	n := "nc-" + svcApp + "-daaaaaaaa"
	require.Equal(t, before.HTTP.Routers[n+"-http"], after.HTTP.Routers[n+"-http"])
	require.Equal(t, before.HTTP.Routers[n+"-probe"], after.HTTP.Routers[n+"-probe"])
	require.Equal(t, before.HTTP.Routers[n+"-https"]["rule"], after.HTTP.Routers[n+"-https"]["rule"])
}

// A name the proxy already holds a certificate for keeps being served from the
// default store whichever resolver the router names, and the resolver's storage
// is its own file: the DNS-01 resolver never shares acme.json with HTTP-01.
func TestAutoCloudflareResolverHasItsOwnStorage(t *testing.T) {
	flags := StaticFlags(StaticOptions{ACMEEmail: "ops@example.com", DNSResolvers: cfResolver()})
	joined := "\n" + strings.Join(flags, "\n") + "\n"
	require.Contains(t, joined, "\n--certificatesresolvers.le.acme.storage="+ACMEStorage+"\n")
	require.Contains(t, joined, "\n--certificatesresolvers.nc-dns-cloudflare.acme.storage="+agentprotocol.DNSResolverStorage("cloudflare")+"\n")
	require.NotEqual(t, ACMEStorage, agentprotocol.DNSResolverStorage("cloudflare"))
}

// DNS-only and proxied (locked) names are issued the same way, and a locked
// origin does not depend on the HTTP-01 challenge crossing Cloudflare: the https
// router asks the DNS-01 resolver and the :80 router only redirects, behind the
// lock.
func TestAutoCloudflareProxiedAndDNSOnlyBothUseDNS01(t *testing.T) {
	route := ruleRoute(
		rule(domA, "dnsonly.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver = agentprotocol.DNSCloudflare }),
		rule(domB, "proxied.example.com", func(d *agentprotocol.DomainRule) {
			d.DNSResolver, d.OriginLocked = agentprotocol.DNSCloudflare, true
		}),
	)
	route.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: cfCIDRs()}
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", DNSResolvers: cfResolver()}))

	want := map[string]any{"certResolver": agentprotocol.DNSResolverName("cloudflare")}
	require.Equal(t, want, httpsTLS(p, domA))
	require.Equal(t, want, httpsTLS(p, domB), "a locked, proxied name gets its certificate over DNS, not through Cloudflare's edge")

	locked := "nc-" + svcApp + "-dbbbbbbbb"
	lock := strs(p.HTTP.Routers[locked+"-http"]["middlewares"])
	require.Len(t, lock, 2)
	require.Contains(t, lock[0], lockMiddlewarePrefix, "the :80 router is still behind the lock")
	require.Equal(t, "nc-https-redirect", lock[1])
}

// A wildcard host in a connected zone uses the same resolver, and asks for the
// wildcard name itself.
func TestAutoCloudflareWildcardHost(t *testing.T) {
	route := ruleRoute(rule(domA, "*.preview.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver = agentprotocol.DNSCloudflare }))
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", DNSResolvers: cfResolver()}))
	require.Equal(t, map[string]any{
		"certResolver": agentprotocol.DNSResolverName("cloudflare"),
		"domains":      []any{map[string]any{"main": "*.preview.example.com"}},
	}, httpsTLS(p, domA))
}

// A tunnelled domain keeps its behaviour whatever the rule names: no certificate
// is requested at the origin through HTTP-01, and the control plane does not
// name the automatic resolver for it at all.
func TestAutoCloudflareTunnelBranchStillWins(t *testing.T) {
	// The resolver is named but the proxy does not run it (so the builder would
	// fall back to HTTP-01): the tunnel branch still returns the default store.
	route := ruleRoute(rule(domA, "a.example.com", func(d *agentprotocol.DomainRule) {
		d.Tunnel, d.DNSResolver = true, agentprotocol.DNSCloudflare
	}))
	p := parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com"}))
	require.Equal(t, map[string]any{}, httpsTLS(p, domA))

	// Nothing named, as the control plane sends it: the default store as well.
	route.Domains[0].DNSResolver = ""
	p = parse(t, mustBuild(t, []agentprotocol.Route{route}, Options{ACMEEmail: "ops@example.com", DNSResolvers: cfResolver()}))
	require.Equal(t, map[string]any{}, httpsTLS(p, domA))
}

// The agent observes the DNS-01 status of a domain served through the automatic
// resolver only once the proxy runs it; before, it reports only which storage
// holds the certificate the host is still served with.
func TestAutoCloudflareDomainIsObservedOnceApplied(t *testing.T) {
	route := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.DNSResolver = agentprotocol.DNSCloudflare }))
	before := acmeTargets(agentprotocol.ProxyConfig{}, []agentprotocol.Route{route})
	require.Len(t, before, 1)
	require.True(t, before[0].storeOnly, "HTTP-01 is not observed for a status")

	got := acmeTargets(agentprotocol.ProxyConfig{DNSResolvers: cfResolver()}, []agentprotocol.Route{route})
	require.Len(t, got, 1)
	require.False(t, got[0].storeOnly)
	require.Equal(t, agentprotocol.AcmeKindDomain, got[0].kind)
	require.Equal(t, domA, got[0].id)
}
