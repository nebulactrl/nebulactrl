package agentprotocol

import (
	"math/rand/v2"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func contains(t *testing.T, cidrs []string, ip netip.Addr) bool {
	t.Helper()
	for _, c := range cidrs {
		p, err := parsePrefix(c)
		require.NoError(t, err)
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func TestEffectiveAllowWithoutDenyKeepsAllowAsWritten(t *testing.T) {
	in := []string{"10.0.0.0/8", "203.0.113.7"}
	out, err := EffectiveAllow(in, nil)
	require.NoError(t, err)
	require.Equal(t, in, out)
}

func TestEffectiveAllowSubtracts(t *testing.T) {
	out, err := EffectiveAllow([]string{"10.0.0.0/8"}, []string{"10.0.0.0/9"})
	require.NoError(t, err)
	require.Equal(t, []string{"10.128.0.0/9"}, out)

	out, err = EffectiveAllow(nil, []string{"0.0.0.0/1"})
	require.NoError(t, err)
	require.Equal(t, []string{"128.0.0.0/1", "::/0"}, out, "an empty allow list is every IPv4 and IPv6 address")

	out, err = EffectiveAllow([]string{"::/0"}, []string{"2001:db8::/32"})
	require.NoError(t, err)
	require.NotContains(t, out, "::/0")
	require.Len(t, out, 32)
}

func TestEffectiveAllowDecisionMatchesDenyFirstEvaluation(t *testing.T) {
	allow := []string{"10.0.0.0/8", "192.168.0.0/16", "203.0.113.0/24"}
	deny := []string{"10.1.0.0/16", "10.200.5.0/24", "192.168.1.1", "203.0.113.128/25"}
	eff, err := EffectiveAllow(allow, deny)
	require.NoError(t, err)

	rng := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		var ip netip.Addr
		switch rng.IntN(3) {
		case 0:
			ip = netip.AddrFrom4([4]byte{10, byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))})
		case 1:
			ip = netip.AddrFrom4([4]byte{192, 168, byte(rng.IntN(3)), byte(rng.IntN(256))})
		default:
			ip = netip.AddrFrom4([4]byte{203, 0, 113, byte(rng.IntN(256))})
		}
		want := contains(t, allow, ip) && !contains(t, deny, ip)
		require.Equal(t, want, contains(t, eff, ip), "ip %s", ip)
	}
	// Addresses outside every allow range stay denied.
	require.False(t, contains(t, eff, netip.MustParseAddr("8.8.8.8")))
}

func TestEffectiveAllowDeny32InsideWideRange(t *testing.T) {
	eff, err := EffectiveAllow([]string{"0.0.0.0/0"}, []string{"198.51.100.9"})
	require.NoError(t, err)
	require.Len(t, eff, 32)
	require.False(t, contains(t, eff, netip.MustParseAddr("198.51.100.9")))
	require.True(t, contains(t, eff, netip.MustParseAddr("198.51.100.8")))
	require.True(t, contains(t, eff, netip.MustParseAddr("1.1.1.1")))
}

func TestEffectiveAllowErrors(t *testing.T) {
	_, err := EffectiveAllow([]string{"10.0.0.0/8"}, []string{"10.0.0.0/8"})
	require.ErrorContains(t, err, "deny every address")
	_, err = EffectiveAllow([]string{"10.0.0.0/8"}, []string{"10.0.0.0/7"})
	require.ErrorContains(t, err, "deny every address")
	_, err = EffectiveAllow(nil, []string{"bogus"})
	require.Error(t, err)

	// Many narrow deny entries inside one wide range blow past the cap.
	var deny []string
	for i := range 200 {
		deny = append(deny, netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, byte(i), byte(i * 7), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}), 128).String())
	}
	_, err = EffectiveAllow([]string{"::/0"}, deny)
	require.ErrorContains(t, err, "more than")
}

func TestValidateAccessLists(t *testing.T) {
	require.NoError(t, ValidateAccessLists([]string{"10.0.0.0/8"}, nil, true))
	require.NoError(t, ValidateAccessLists(nil, []string{"203.0.113.0/24"}, true))
	require.Error(t, ValidateAccessLists(nil, nil, true), "needs one entry")
	require.NoError(t, ValidateAccessLists(nil, nil, false))
	require.Error(t, ValidateAccessLists([]string{"x"}, nil, false))
	require.Error(t, ValidateAccessLists(nil, []string{"10.0.0.0/33"}, false))
	big := make([]string, MaxAccessCIDRs+1)
	for i := range big {
		big[i] = "10.0.0.1"
	}
	require.Error(t, ValidateAccessLists(big, nil, false))
	require.Error(t, ValidateAccessLists(nil, big, false))
}

func TestOpenCIDR(t *testing.T) {
	for _, s := range []string{"0.0.0.0/0", "::/0", "1.2.3.4/0"} {
		require.True(t, IsOpenCIDR(s), s)
	}
	for _, s := range []string{"10.0.0.0/8", "1.2.3.4", "nope", "0.0.0.0/1"} {
		require.False(t, IsOpenCIDR(s), s)
	}
	require.True(t, AnyOpenCIDR([]string{"10.0.0.0/8", "0.0.0.0/0"}))
	require.False(t, AnyOpenCIDR(nil))
}

func TestRouteOptionsAccessValidation(t *testing.T) {
	require.NoError(t, RouteOptions{Access: &Access{DenyCIDRs: []string{"203.0.113.0/24"}}}.Validate())
	require.NoError(t, RouteOptions{Access: &Access{AllowCIDRs: []string{"10.0.0.0/8"}, DenyCIDRs: []string{"10.1.0.0/16"}}}.Validate())
	require.Error(t, RouteOptions{Access: &Access{}}.Validate(), "an access block with no rules")
	require.Error(t, RouteOptions{Access: &Access{AllowCIDRs: []string{"10.0.0.0/8"}, DenyCIDRs: []string{"10.0.0.0/8"}}}.Validate())
	require.Error(t, RouteOptions{Access: &Access{DenyCIDRs: []string{"x"}}}.Validate())
}
