package proxy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Deny rules render as one ipAllowList: (allow, or everything) minus deny.
func TestGoldenAccessDenyList(t *testing.T) {
	cases := []struct {
		name   string
		access agentprotocol.Access
		want   []any
	}{
		{"mw_access_deny_only", agentprotocol.Access{DenyCIDRs: []string{"203.0.113.0/24"}}, nil},
		{"mw_access_allow_minus_deny", agentprotocol.Access{
			AllowCIDRs: []string{"10.0.0.0/8"}, DenyCIDRs: []string{"10.1.0.0/16", "10.2.3.4"}, TrustedProxyDepth: 1, RejectStatus: 404,
		}, nil},
		{"mw_access_deny_disjoint", agentprotocol.Access{AllowCIDRs: []string{"10.1.2.3"}, DenyCIDRs: []string{"192.0.2.0/24"}}, []any{"10.1.2.3/32"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
				d.Options = agentprotocol.RouteOptions{Access: &c.access}
			}))
			p := parse(t, goldenConfig(t, c.name, []agentprotocol.Route{r}, Options{}))
			n := "nc-" + svcApp + "-daaaaaaaa"
			mw := p.HTTP.Middlewares[n+"-ipallow"]
			require.NotNil(t, mw)
			src := mw["ipAllowList"].(map[string]any)["sourceRange"].([]any)
			if c.want != nil {
				require.Equal(t, c.want, src)
			}
			require.Contains(t, strs(p.HTTP.Routers[n+"-https"]["middlewares"]), n+"-ipallow")
			require.NotContains(t, p.HTTP.Routers[n+"-probe"], "middlewares", "access rules are not on the probe router")
		})
	}
}

func TestGoldenTCPRouteWithDenyList(t *testing.T) {
	r := agentprotocol.Route{
		ServiceID: svcApp, Upstreams: []string{"nc-pg-aaaaaaaa-0:5432"},
		RouteExt: agentprotocol.RouteExt{
			Protocol: agentprotocol.ProtoTCP, ListenPort: 5432,
			AllowCIDRs: []string{"192.168.0.0/16"}, DenyCIDRs: []string{"192.168.1.0/24", "192.168.7.7"},
		},
	}
	p := parse(t, goldenConfig(t, "tcp_route_deny", []agentprotocol.Route{r}, Options{}))
	n := "nc-" + svcApp + "-tcp"
	require.Equal(t, []string{n + "-allow"}, strs(p.TCP.Routers[n]["middlewares"]))
	src := p.TCP.Middlewares[n+"-allow"]["ipAllowList"].(map[string]any)["sourceRange"].([]any)
	require.NotContains(t, src, "192.168.0.0/16", "the allowed range was split around the denied ones")
	require.NotContains(t, src, "192.168.1.0/24")
	require.Contains(t, src, "192.168.0.0/24")
	require.Contains(t, src, "192.168.2.0/23")
}

// A deny-only TCP route (open to the internet except the denied ranges).
func TestTCPRouteDenyOnlyRendersComplement(t *testing.T) {
	r := agentprotocol.Route{
		ServiceID: svcApp, Upstreams: []string{"nc-pg-aaaaaaaa-0:5432"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432, DenyCIDRs: []string{"128.0.0.0/1"}},
	}
	p := parse(t, goldenConfig(t, "tcp_route_deny_only", []agentprotocol.Route{r}, Options{}))
	src := p.TCP.Middlewares["nc-"+svcApp+"-tcp-allow"]["ipAllowList"].(map[string]any)["sourceRange"].([]any)
	require.Equal(t, []any{"0.0.0.0/1", "::/0"}, src)
}

func TestValidateRoutesRejectsBadDenyRules(t *testing.T) {
	tcp := func(mut func(*agentprotocol.RouteExt)) []agentprotocol.Route {
		r := agentprotocol.Route{ServiceID: svcApp, Upstreams: []string{"nc-pg-aaaaaaaa-0:5432"},
			RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432}}
		mut(&r.RouteExt)
		return []agentprotocol.Route{r}
	}
	def := agentprotocol.DefaultUpstreamPolicy()
	require.Error(t, ValidateRoutes(tcp(func(e *agentprotocol.RouteExt) { e.DenyCIDRs = []string{"nope"} }), def))
	require.Error(t, ValidateRoutes(tcp(func(e *agentprotocol.RouteExt) {
		e.AllowCIDRs, e.DenyCIDRs = []string{"10.0.0.0/8"}, []string{"10.0.0.0/7"}
	}), def), "deny that covers every allowed range admits nobody")
	udp := tcp(func(e *agentprotocol.RouteExt) {
		e.Protocol, e.DenyCIDRs = agentprotocol.ProtoUDP, []string{"10.0.0.0/8"}
	})
	require.Error(t, ValidateRoutes(udp, def), "udp cannot enforce deny rules")
	web := ruleRoute()
	web.DenyCIDRs = []string{"10.0.0.0/8"}
	web.Hosts = []string{"a.example.com"}
	require.Error(t, ValidateRoutes([]agentprotocol.Route{web}, def), "route-level deny is tcp only")
	require.NoError(t, ValidateRoutes(tcp(func(e *agentprotocol.RouteExt) { e.DenyCIDRs = []string{"203.0.113.0/24"} }), def))
}
