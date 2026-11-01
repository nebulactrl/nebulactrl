package clientip

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func cfOnly(prefix string) func(netip.Addr) bool {
	p := netip.MustParsePrefix(prefix)
	return func(a netip.Addr) bool { return p.Contains(a) }
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestResolve(t *testing.T) {
	SetCloudflareRanges(cfOnly("173.245.48.0/20"))
	t.Cleanup(func() { SetCloudflareRanges(nil) })

	tests := []struct {
		name   string
		remote string
		h      http.Header
		want   string
	}{
		{"direct public peer ignores every header", "203.0.113.9:4000",
			hdr("X-Forwarded-For", "1.2.3.4", "X-Real-IP", "5.6.7.8", "CF-Connecting-IP", "9.9.9.9"), "203.0.113.9"},
		{"proxy peer without headers is the peer", "172.18.0.4:5555", hdr(), "172.18.0.4"},
		{"proxy appends the visitor last", "172.18.0.4:5555", hdr("X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"entries before the last are the visitor's claim", "172.18.0.4:5555",
			hdr("X-Forwarded-For", "6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		{"malformed last entry does not fall back to an earlier one", "172.18.0.4:5555",
			hdr("X-Forwarded-For", "6.6.6.6, nonsense"), "172.18.0.4"},
		{"x-real-ip only when there is no forwarded-for", "172.18.0.4:5555", hdr("X-Real-IP", "198.51.100.8"), "198.51.100.8"},
		{"forwarded-for wins over x-real-ip", "172.18.0.4:5555",
			hdr("X-Forwarded-For", "198.51.100.7", "X-Real-IP", "198.51.100.8"), "198.51.100.7"},
		{"cloudflare edge: connecting ip is the visitor", "172.18.0.4:5555",
			hdr("X-Forwarded-For", "6.6.6.6, 198.51.100.7, 173.245.48.10", "CF-Connecting-IP", "198.51.100.7"), "198.51.100.7"},
		{"cloudflare edge without cf header: entry before the edge", "172.18.0.4:5555",
			hdr("X-Forwarded-For", "198.51.100.7, 173.245.48.10"), "198.51.100.7"},
		{"cf-connecting-ip from a non-cloudflare peer is ignored", "172.18.0.4:5555",
			hdr("X-Forwarded-For", "198.51.100.7", "CF-Connecting-IP", "9.9.9.9"), "198.51.100.7"},
		{"ipv6 and mapped peers", "[::ffff:172.18.0.4]:80", hdr("X-Forwarded-For", "2001:db8::1"), "2001:db8::1"},
		{"garbage remote", "not-an-address", hdr("X-Forwarded-For", "1.2.3.4"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.remote, tc.h)
			if tc.want == "" {
				require.False(t, got.IsValid())
				return
			}
			require.Equal(t, tc.want, got.String())
		})
	}
}

func TestResolveWithoutCloudflareKnowledge(t *testing.T) {
	SetCloudflareRanges(nil)
	got := Resolve("172.18.0.4:1", hdr("X-Forwarded-For", "198.51.100.7, 173.245.48.10", "CF-Connecting-IP", "198.51.100.7"))
	require.Equal(t, "173.245.48.10", got.String(), "without the range list the last entry stands")
}
