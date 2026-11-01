package proxy

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// admits reports whether any range contains ip.
func admits(ranges []string, ip string) bool {
	a := netip.MustParseAddr(ip)
	for _, r := range ranges {
		if p, err := netip.ParsePrefix(r); err == nil {
			if p.Contains(a) {
				return true
			}
		} else if netip.MustParseAddr(r) == a {
			return true
		}
	}
	return false
}

// A client on the host (127.0.0.1 through the port publishing) or on the LAN
// behind the host's NAT reaches the proxy as the edge network's gateway. An
// allow list that holds the private ranges is written for exactly those
// clients, so the gateway is never taken out of it (see edge.go for how the
// IPv6-as-gateway and tunnel paths are closed instead).
func TestPeerAllowListsAdmitAClientArrivingViaTheGateway(t *testing.T) {
	private := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	opts := Options{ACMEEmail: "ops@example.com"}

	got := effectiveSourceRange(private, nil)
	require.True(t, admits(got, "172.18.0.1"), "the edge gateway")
	require.True(t, admits(got, "172.17.0.1"), "the default bridge gateway")
	require.True(t, admits(got, "192.168.5.5"))
	require.False(t, admits(got, "203.0.113.7"), "a public address is still refused")

	build := func(a *agentprotocol.Access) map[string]any {
		r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.Options.Access = a }))
		p := parse(t, mustBuild(t, []agentprotocol.Route{r}, opts))
		return p.HTTP.Middlewares["nc-"+svcApp+"-daaaaaaaa-ipallow"]["ipAllowList"].(map[string]any)
	}

	peer := build(&agentprotocol.Access{AllowCIDRs: private})
	require.NotContains(t, peer, "ipStrategy", "depth 0: the peer is judged")
	require.Equal(t, private, strs(peer["sourceRange"]), "rendered as written")
	require.True(t, admits(strs(peer["sourceRange"]), "172.18.0.1"), "a client arriving via the gateway")

	forwarded := build(&agentprotocol.Access{AllowCIDRs: private, TrustedProxyDepth: 1})
	require.Equal(t, private, strs(forwarded["sourceRange"]), "depth 1 reads X-Forwarded-For: the list is as written")
	require.Equal(t, map[string]any{"depth": float64(1)}, forwarded["ipStrategy"])

	denyOnly := build(&agentprotocol.Access{DenyCIDRs: []string{"198.51.100.0/24"}})
	require.True(t, admits(strs(denyOnly["sourceRange"]), "172.18.0.1"))

	// The user's own deny list still applies.
	withDeny := build(&agentprotocol.Access{AllowCIDRs: private, DenyCIDRs: []string{"10.9.0.0/16"}})
	require.False(t, admits(strs(withDeny["sourceRange"]), "10.9.1.1"))
	require.True(t, admits(strs(withDeny["sourceRange"]), "172.18.0.1"))

	// TCP routes judge the peer too: a database allowed from the private ranges
	// answers a client on the host.
	tcp := agentprotocol.Route{
		ServiceID: svcApp, Upstreams: []string{"nc-pg-aaaaaaaa-0:5432"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432, AllowCIDRs: private},
	}
	p := parse(t, mustBuild(t, []agentprotocol.Route{tcp}, opts))
	mw := p.TCP.Middlewares["nc-"+svcApp+"-tcp-allow"]["ipAllowList"].(map[string]any)
	require.True(t, admits(strs(mw["sourceRange"]), "172.18.0.1"), "the host reaches its own database")
	require.True(t, admits(strs(mw["sourceRange"]), "10.0.0.7"))
	require.False(t, admits(strs(mw["sourceRange"]), "203.0.113.7"))
}

func TestTheManagerReadsWhetherTheEdgeNetworkHasIPv6(t *testing.T) {
	eng := &fakeEngine{networks: []docker.NetworkInfo{
		{Name: agentprotocol.EdgeNetwork, Subnets: []string{"172.18.0.1/16"}},
		{Name: agentprotocol.EdgeNetwork + "-2", Subnets: []string{"fd99::/64"}}, // a lookalike
		{Name: "bridge", Subnets: []string{"172.17.0.0/16", "fd00:1::/64"}},
		{Name: "someone-else", Subnets: []string{"fd10::/64"}},
	}}
	m, err := NewManager(Config{Engine: eng})
	require.NoError(t, err)
	require.NoError(t, m.refreshEdge(context.Background()))
	require.False(t, m.edgeIPv6(), "only the exact edge network counts")

	eng.networks[0].Subnets = append(eng.networks[0].Subnets, "fd12:3456::/64")
	require.NoError(t, m.refreshEdge(context.Background()))
	require.True(t, m.edgeIPv6())

	eng.networks = nil
	require.NoError(t, m.refreshEdge(context.Background()))
	require.False(t, m.edgeIPv6(), "no edge network yet")
}

// An IPv6 client of a port published on :: reaches an IPv4-only network through
// docker-proxy as the network gateway, so the public ports are published on
// 0.0.0.0 (no IPv6 listener) until the edge network carries IPv6 itself. That,
// not an exclusion of the gateway, is what keeps a public IPv6 visitor from
// arriving as a private address.
func TestPublicPortsAreBoundToIPv4UnlessTheEdgeHasIPv6(t *testing.T) {
	m := &Manager{cfg: Config{HTTPPort: 80, HTTPSPort: 443, AdminPort: 9000, AdminURL: "http://127.0.0.1:9000"}, adminURL: "http://127.0.0.1:9000", adminSecret: "s"}
	l4 := []L4Port{{Proto: agentprotocol.ProtoTCP, Port: 5432}}

	v4 := m.specFor(specInputs{L4: l4})
	for _, p := range v4.Ports {
		if p.ContainerPort == agentprotocol.ProxyAdminPort {
			require.Equal(t, "127.0.0.1", p.HostIP, "the admin port keeps its loopback bind")
			continue
		}
		require.Equal(t, "0.0.0.0", p.HostIP, "no IPv6 listener: %d/%s", p.ContainerPort, p.Proto)
	}

	v6 := m.specFor(specInputs{L4: l4, EdgeIPv6: true})
	for _, p := range v6.Ports {
		if p.ContainerPort != agentprotocol.ProxyAdminPort {
			require.Empty(t, p.HostIP, "all interfaces: docker keeps the real IPv6 source on a dual-stack network")
		}
	}
	require.NotEqual(t, v4.Labels[LabelSpec], v6.Labels[LabelSpec], "a change of family recreates the container")
}
