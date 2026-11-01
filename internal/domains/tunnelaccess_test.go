package domains

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Behind a Cloudflare Tunnel the proxy sees the connector's private
// Docker address, so an IP allow list cannot tell visitors apart. It fails closed.
func TestAnIPAllowListOnATunnelledDomainAdmitsNobody(t *testing.T) {
	internal := &agentprotocol.Access{AllowCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"}, RejectStatus: 404, TrustedProxyDepth: 2}
	routes := []agentprotocol.Route{{ServiceID: "svc", Domains: []agentprotocol.DomainRule{
		{ID: "tunnelled", Host: "admin.example.com", Tunnel: true, Options: agentprotocol.RouteOptions{Access: internal}},
		{ID: "direct", Host: "direct.example.com", Options: agentprotocol.RouteOptions{Access: internal}},
		{ID: "tunnel-open", Host: "www.example.com", Tunnel: true},
		{ID: "tunnel-deny", Host: "deny.example.com", Tunnel: true, Options: agentprotocol.RouteOptions{Access: &agentprotocol.Access{DenyCIDRs: []string{"198.51.100.0/24"}}}},
	}}}

	got := withTunnelAccess(routes)[0].Domains
	require.Equal(t, &agentprotocol.Access{AllowCIDRs: []string{tunnelDeniedCIDR}, RejectStatus: 404}, got[0].Options.Access, "nobody passes; the reject status is kept; no X-Forwarded-For depth")
	require.NoError(t, agentprotocol.ValidateAccessLists(got[0].Options.Access.AllowCIDRs, nil, true), "the proxy accepts it")
	require.Equal(t, internal, got[1].Options.Access, "a domain that is not tunnelled keeps its rule")
	require.Nil(t, got[2].Options.Access)
	require.Equal(t, []string{"198.51.100.0/24"}, got[3].Options.Access.DenyCIDRs, "a deny-only rule hides nothing and is left as it is")
	require.Equal(t, []string{"10.0.0.0/8", "172.16.0.0/12"}, internal.AllowCIDRs, "the stored options are not mutated")
}

// A platform service domain always has its allow list (private ranges and the
// servers): over a tunnel that list would pass every visitor, so the domain is
// unreachable through a tunnel, and the user's X-Forwarded-For depth does not
// survive either.
func TestAPlatformDomainIsNeverReachableThroughATunnel(t *testing.T) {
	rules := []agentprotocol.DomainRule{{
		ID: "hs", Host: "hs.example.com", Tunnel: true,
		Options: agentprotocol.RouteOptions{Access: &agentprotocol.Access{TrustedProxyDepth: 1}},
	}}
	restrictRules(rules, PlatformAllowList([]string{"203.0.113.10"}))
	require.Zero(t, rules[0].Options.Access.TrustedProxyDepth, "a tunnelled domain does not read the visitor from X-Forwarded-For")
	require.Contains(t, rules[0].Options.Access.AllowCIDRs, "10.0.0.0/8", "the platform list is what it is before the tunnel rule")

	got := withTunnelAccess([]agentprotocol.Route{{ServiceID: "svc", Domains: rules}})[0].Domains[0]
	require.Equal(t, []string{tunnelDeniedCIDR}, got.Options.Access.AllowCIDRs)

	// An origin-locked domain still reads the visitor Cloudflare saw.
	locked := []agentprotocol.DomainRule{{
		ID: "hs2", Host: "hs2.example.com", OriginLocked: true,
		Options: agentprotocol.RouteOptions{Access: &agentprotocol.Access{TrustedProxyDepth: 1}},
	}}
	restrictRules(locked, PlatformAllowList(nil))
	require.Equal(t, 1, locked[0].Options.Access.TrustedProxyDepth)
}
