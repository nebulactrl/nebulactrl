package dashboardaccess

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRealClientIP(t *testing.T) {
	cases := []struct {
		name, remote, xff string
		want              string
		via               bool
	}{
		{"behind the host proxy: the last entry is the peer Traefik saw", "10.212.0.3:41234", "203.0.113.9", "203.0.113.9", true},
		{"entries the client sent before it are never read", "10.212.0.3:41234", "1.2.3.4, 5.6.7.8, 100.101.102.103", "100.101.102.103", true},
		{"spaces and empty entries", "172.20.0.2:5", " 1.1.1.1 ,  ,198.51.100.4 , ", "198.51.100.4", true},
		{"IPv6 peer", "10.212.0.3:41234", "fd7a:115c:a1e0::1", "fd7a:115c:a1e0::1", true},
		{"IPv6 with brackets and port", "[fd00::2]:9", "[2001:db8::5]:443", "2001:db8::5", true},
		{"IPv4-mapped IPv6 is unmapped", "10.0.0.2:5", "::ffff:192.0.2.1", "192.0.2.1", true},
		{"loopback peer is trusted (SSH tunnel to the proxy)", "127.0.0.1:5000", "192.0.2.77", "192.0.2.77", true},
		{"IPv6 loopback peer", "[::1]:5000", "192.0.2.77", "192.0.2.77", true},
		{"link-local peer", "[fe80::1]:5000", "192.0.2.77", "192.0.2.77", true},
		{"no header: the request did not come through the route", "10.212.0.3:41234", "", "10.212.0.3", false},
		{"public peer: the header is not trusted", "198.51.100.20:443", "100.64.0.9", "198.51.100.20", false},
		{"public IPv6 peer: the header is not trusted", "[2001:db8::1]:443", "100.64.0.9", "2001:db8::1", false},
		{"a garbage last entry is not skipped for an earlier one", "10.0.0.2:5", "192.0.2.1, not-an-ip", "10.0.0.2", false},
		{"peer without a port", "10.0.0.2", "192.0.2.1", "192.0.2.1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, via := RealClientIP(c.remote, c.xff)
			require.Equal(t, c.want, ip.String())
			require.Equal(t, c.via, via)
		})
	}
	ip, via := RealClientIP("", "1.2.3.4")
	require.False(t, ip.IsValid())
	require.False(t, via)
}

func TestAllows(t *testing.T) {
	ip := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	tailnet := []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}
	cases := []struct {
		name  string
		allow []string
		ip    string
		want  bool
	}{
		{"empty list admits everybody", nil, "203.0.113.9", true},
		{"tailnet IPv4", tailnet, "100.101.102.103", true},
		{"tailnet range edges", tailnet, "100.127.255.255", true},
		{"just outside the tailnet range", tailnet, "100.128.0.0", false},
		{"below the tailnet range", tailnet, "100.63.255.255", false},
		{"tailnet IPv6", tailnet, "fd7a:115c:a1e0:ab12::1", true},
		{"outside the tailnet IPv6", tailnet, "fd7a:115c:a1e1::1", false},
		{"public address", tailnet, "203.0.113.9", false},
		{"single address entry", []string{"203.0.113.9"}, "203.0.113.9", true},
		{"single address entry, other address", []string{"203.0.113.9"}, "203.0.113.10", false},
		{"single IPv6 address entry", []string{"2001:db8::1"}, "2001:db8::1", true},
		{"/32 and /128 entries", []string{"203.0.113.9/32", "2001:db8::1/128"}, "2001:db8::1", true},
		{"IPv4-mapped client matches an IPv4 entry", []string{"192.0.2.0/24"}, "::ffff:192.0.2.77", true},
		{"unmasked CIDR entry", []string{"10.1.2.3/8"}, "10.200.0.1", true},
		{"IPv4 client never matches an IPv6 range", []string{"::/0"}, "203.0.113.9", false},
		{"0.0.0.0/0 admits every IPv4 address", []string{"0.0.0.0/0"}, "203.0.113.9", true},
		{"an unparsable entry matches nothing", []string{"nonsense"}, "203.0.113.9", false},
		{"blank padding is ignored", []string{" 203.0.113.9 "}, "203.0.113.9", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, Allows(c.allow, ip(c.ip)))
		})
	}
	require.False(t, Allows(tailnet, netip.Addr{}), "an unknown client is admitted by no list")
	require.True(t, Allows(nil, netip.Addr{}))
}
