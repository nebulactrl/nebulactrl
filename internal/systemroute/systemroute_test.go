package systemroute

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func cfg(publicURL, upstream string) *config.Config {
	return &config.Config{PublicURL: publicURL, Installer: config.Installer{DashboardUpstream: upstream}}
}

func TestConfigureEnablesOnlyForAnHTTPSDNSNameWithAnUpstream(t *testing.T) {
	t.Cleanup(func() { Set(Config{}) })
	cases := []struct {
		url, upstream string
		host          string
	}{
		{"https://nebula.example.com", "nebulactrl:8080", "nebula.example.com"},
		{"https://Nebula.Example.com/", "nebulactrl:8080", "nebula.example.com"},
		{"http://nebula.example.com", "nebulactrl:8080", ""}, // plain HTTP: no ACME route
		{"https://203.0.113.9", "nebulactrl:8080", ""},       // an IP address gets no certificate
		{"https://localhost", "nebulactrl:8080", ""},
		{"https://nebula.example.com", "", ""}, // no upstream configured
		{"", "nebulactrl:8080", ""},
	}
	for _, tc := range cases {
		Configure(cfg(tc.url, tc.upstream))
		c, ok := Current()
		require.Equal(t, tc.host != "", ok, "%s %s", tc.url, tc.upstream)
		require.Equal(t, tc.host, c.Host, tc.url)
		require.Equal(t, tc.host != "", Reserved(tc.host+"."), tc.url)
	}
	Configure(nil)
	_, ok := Current()
	require.False(t, ok)
}

func TestRouteIsAValidExternalRoute(t *testing.T) {
	rt := Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"}.Route()
	require.True(t, IsRoute(rt.ServiceID))
	require.Equal(t, agentprotocol.RouteKindExternal, rt.Kind)
	require.Equal(t, []string{"nebula.example.com"}, rt.Hosts)
	require.Equal(t, "nebula.example.com", rt.Domains[0].Host)
	require.Equal(t, agentprotocol.TLSAuto, rt.Domains[0].TLSMode)
	require.NoError(t, agentprotocol.CheckUpstream(rt.Upstreams[0], agentprotocol.DefaultUpstreamPolicy()),
		"the policy lets the container name through")
	require.True(t, agentprotocol.ValidRouteHost(rt.Hosts[0]))
}

// The default route has no access rule and no DNS-01 resolver, and sends HSTS
// ("strict" security headers: the dashboard is only routed with a domain, over HTTPS).
func TestDefaultRouteIsUnchanged(t *testing.T) {
	got := Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"}.Route()
	want := agentprotocol.Route{
		ServiceID: ServiceID, Hosts: []string{"nebula.example.com"}, Upstreams: []string{"nebulactrl:8080"}, HealthPath: HealthPath,
		RouteExt: agentprotocol.RouteExt{
			Kind: agentprotocol.RouteKindExternal,
			Domains: []agentprotocol.DomainRule{{
				ID: DomainID, Host: "nebula.example.com", TLSMode: agentprotocol.TLSAuto,
				Options: agentprotocol.RouteOptions{SecurityHeaders: "strict"},
			}},
		},
	}
	require.Equal(t, want, got)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "access")
	require.NotContains(t, string(raw), "dnsResolver")
}

func TestRouteRendersAccessAndResolver(t *testing.T) {
	c := Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080", AllowCIDRs: []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, DNSResolver: "cloudflare"}
	rt := c.Route()
	d := rt.Domains[0]
	require.Equal(t, "cloudflare", d.DNSResolver)
	require.Equal(t, &agentprotocol.Access{AllowCIDRs: []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}}, d.Options.Access)
	require.Equal(t, "strict", d.Options.SecurityHeaders, "HSTS on the dashboard")
	require.NoError(t, d.Options.Validate())
	// The route owns a copy: editing the config's slice later does not change a built route.
	c.AllowCIDRs[0] = "0.0.0.0/0"
	require.Equal(t, "100.64.0.0/10", d.Options.Access.AllowCIDRs[0])
}

func TestSetAccessUpdatesTheInstalledConfig(t *testing.T) {
	t.Cleanup(func() { Set(Config{}) })
	allow, resolver := SetAccess([]string{"10.0.0.0/8"}, "cloudflare")
	require.Nil(t, allow, "a disabled route ignores access settings")
	require.Empty(t, resolver)
	_, ok := Current()
	require.False(t, ok)

	Configure(&config.Config{PublicURL: "https://nebula.example.com", Installer: config.Installer{
		DashboardUpstream: "nebulactrl:8080", DashboardAllowCIDRs: []string{"192.0.2.0/24"},
	}})
	c, ok := Current()
	require.True(t, ok)
	require.Equal(t, []string{"192.0.2.0/24"}, c.AllowCIDRs, "the installer's list applies until the stored settings load")

	prevAllow, prevResolver := SetAccess([]string{"10.0.0.0/8"}, "cloudflare")
	require.Equal(t, []string{"192.0.2.0/24"}, prevAllow)
	require.Empty(t, prevResolver)
	c, _ = Current()
	require.Equal(t, []string{"10.0.0.0/8"}, c.AllowCIDRs)
	require.Equal(t, "cloudflare", c.DNSResolver)
	require.Equal(t, "nebula.example.com", c.Host)
}
