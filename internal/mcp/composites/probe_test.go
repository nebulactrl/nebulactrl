package composites

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProbeAddrAllowed(t *testing.T) {
	server := netip.MustParseAddr("192.168.1.10")
	cases := []struct {
		ip      string
		servers []netip.Addr
		want    bool
	}{
		{"93.184.216.34", nil, true},
		{"2606:2800:220:1:248:1893:25c8:1946", nil, true},
		{"127.0.0.1", nil, false},
		{"::1", nil, false},
		{"10.0.0.5", nil, false},
		{"172.16.0.1", nil, false},
		{"192.168.1.10", nil, false},
		{"192.168.1.10", []netip.Addr{server}, true}, // the service's own server may be private
		{"169.254.169.254", nil, false},              // cloud metadata
		{"100.64.0.1", nil, false},                   // CGNAT
		{"0.0.0.0", nil, false},
		{"224.0.0.1", nil, false},
		{"fd00::1", nil, false},
		{"::ffff:127.0.0.1", nil, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, probeAddrAllowed(netip.MustParseAddr(tc.ip).Unmap(), tc.servers), tc.ip)
	}
}

func TestValidProbeHost(t *testing.T) {
	for _, ok := range []string{"app.example.com", "a-b.example.io", "localhost", "x1.y2.z3"} {
		require.True(t, validProbeHost(ok), ok)
	}
	for _, bad := range []string{"", "http://evil.test", "evil.test:8080", "user@evil.test", "127.0.0.1", "::1", "a..b", "-a.test", "a-.test", "evil.test/x", "UPPER.test", "ex ample.test", "a.test."} {
		require.False(t, validProbeHost(bad), bad)
	}
}

func TestProbeRejectsBadPaths(t *testing.T) {
	c := &composites{}
	for _, p := range []string{"relative", "//evil.test/x", "/a b", "/a\r\nHost: evil"} {
		res := c.probe(context.Background(), ProbeTarget{Hostname: "app.example.com", Path: p}, nil)
		require.False(t, res.OK, p)
		require.Contains(t, res.Error, "path", p)
	}
}

// With the default client a hostname that resolves to loopback is refused
// before any connection is made.
func TestProbeDefaultClientRefusesLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	c := &composites{}
	// "localhost" resolves to 127.0.0.1/::1 only.
	res := c.probe(context.Background(), ProbeTarget{Hostname: "localhost", HTTP: true, Path: "/"}, nil)
	require.False(t, res.OK)
	require.Contains(t, res.Error, "refused")
	require.Zero(t, hits.Load())
}

// A transport override keeps the redirect policy: a redirect to another host is
// reported, not followed; a same-host redirect is followed.
func TestProbeRedirectPolicy(t *testing.T) {
	var other atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/leave":
			http.Redirect(w, r, "http://evil.example.net/steal", http.StatusFound)
		case "/same":
			http.Redirect(w, r, "http://app.example.com/final", http.StatusFound)
		case "/final":
			w.WriteHeader(http.StatusOK)
		default:
			other.Add(1)
		}
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	c := &composites{Deps: Deps{Probe: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}}}

	res := c.probe(context.Background(), ProbeTarget{Hostname: "app.example.com", HTTP: true, Path: "/leave"}, nil)
	require.Equal(t, http.StatusFound, res.Status)
	require.Equal(t, "http://evil.example.net", res.RedirectedTo)
	require.Zero(t, other.Load())

	res = c.probe(context.Background(), ProbeTarget{Hostname: "app.example.com", HTTP: true, Path: "/same"}, nil)
	require.True(t, res.OK, res.Error)
	require.Equal(t, http.StatusOK, res.Status)
	require.Empty(t, res.RedirectedTo)
}

// A same-host redirect to another port is another service: it is reported,
// not followed.
func TestProbeRedirectToOtherPortIsNotFollowed(t *testing.T) {
	var other atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/port" {
			http.Redirect(w, r, "http://app.example.com:8080/admin", http.StatusFound)
			return
		}
		other.Add(1)
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	c := &composites{Deps: Deps{Probe: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}}}

	res := c.probe(context.Background(), ProbeTarget{Hostname: "app.example.com", HTTP: true, Path: "/port"}, nil)
	require.Equal(t, http.StatusFound, res.Status)
	require.Equal(t, "http://app.example.com:8080", res.RedirectedTo)
	require.Zero(t, other.Load())
}

// 6to4 and Teredo addresses are as public as the IPv4 addresses they embed.
func TestProbeAddrAllowedEmbeddedIPv4(t *testing.T) {
	for _, ip := range []string{
		"2002:7f00:1::1",                 // 6to4 of 127.0.0.1
		"2002:0a00:0001::1",              // 6to4 of 10.0.0.1
		"2002:a9fe:a9fe::1",              // 6to4 of 169.254.169.254
		"2001:0:5ef5:79fd:0:0:f5ff:fffe", // Teredo, client 10.0.0.1
		"2001:0:0a00:0001:0:0:8c52:7c2d", // Teredo, server 10.0.0.1
	} {
		require.False(t, probeAddrAllowed(netip.MustParseAddr(ip), nil), ip)
	}
	require.True(t, probeAddrAllowed(netip.MustParseAddr("2001:0:5ef5:79fd:0:0:7300:7a3c"), nil), "Teredo with public server and client")
}
