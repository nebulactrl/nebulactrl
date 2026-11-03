package stats

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func ipnet(cidr string) net.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestHostTailscaleFromDetectsCGNATAddresses(t *testing.T) {
	got := hostTailscaleFrom([]net.Addr{
		ipnet("127.0.0.1/8"), ipnet("192.168.1.10/24"), ipnet("10.0.0.2/8"),
		ipnet("100.63.255.255/32"), // just below the range
		ipnet("100.101.102.103/32"),
		ipnet("100.127.255.254/32"),
		ipnet("100.128.0.1/32"), // just above the range
		ipnet("fd7a:115c:a1e0::1/128"),
		&net.IPAddr{IP: net.ParseIP("100.64.0.1")},
	})
	require.True(t, got.Detected)
	require.Equal(t, []string{"100.101.102.103", "100.127.255.254", "100.64.0.1"}, got.IPs)

	none := hostTailscaleFrom([]net.Addr{ipnet("192.168.1.10/24"), ipnet("172.17.0.1/16")})
	require.False(t, none.Detected)
	require.Empty(t, none.IPs)
	require.False(t, hostTailscaleFrom(nil).Detected)
}

func TestFactsAdvertiseProxyV2AndHostTailscale(t *testing.T) {
	c := New(Options{HTTPPort: 18080, HTTPSPort: 18443})
	f := c.Facts(context.Background())
	require.Contains(t, f.Capabilities, agentprotocol.CapProxyV2)
	require.Contains(t, f.Capabilities, agentprotocol.CapProcesses, "several processes per service is advertised unconditionally")
	require.NotNil(t, f.HostTailscale, "host tailscale detection always reports a result")
}

func TestFactsComposeCapability(t *testing.T) {
	for _, c := range []struct {
		name    string
		probe   func(context.Context) (string, error)
		version string
		cap     bool
	}{
		{"current", func(context.Context) (string, error) { return "2.29.7\n", nil }, "2.29.7", true},
		{"leading v", func(context.Context) (string, error) { return "v2.24.0", nil }, "v2.24.0", true},
		{"too old", func(context.Context) (string, error) { return "2.23.3", nil }, "2.23.3", false},
		{"unparsable", func(context.Context) (string, error) { return "dev", nil }, "dev", false},
		{"plugin missing", func(context.Context) (string, error) { return "", errors.New("not a docker command") }, "", false},
		{"no runner", nil, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := New(Options{HTTPPort: 18080, HTTPSPort: 18443, ComposeVersion: c.probe}).Facts(context.Background())
			require.Equal(t, c.version, f.ComposeVersion)
			require.Equal(t, c.cap, slices.Contains(f.Capabilities, agentprotocol.CapCompose))
		})
	}
}

func TestComposeVersionIsProbedOnce(t *testing.T) {
	calls := 0
	c := New(Options{HTTPPort: 18080, HTTPSPort: 18443, ComposeVersion: func(context.Context) (string, error) {
		calls++
		return "2.30.0", nil
	}})
	c.Facts(context.Background())
	f := c.Facts(context.Background())
	require.Equal(t, 1, calls)
	require.Equal(t, "2.30.0", f.ComposeVersion)
	require.Contains(t, f.Capabilities, agentprotocol.CapCompose)
}

func TestHeartbeatCarriesTailnetStatus(t *testing.T) {
	var st *agentprotocol.TailnetStatus
	c := New(Options{TailnetStatus: func() *agentprotocol.TailnetStatus { return st }})
	require.Nil(t, c.Heartbeat(context.Background()).Tailnet, "nil when no tailnet")

	st = &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetManaged, State: agentprotocol.TailnetStateOnline, FQDN: "n.ts.net", IPs: []string{"100.64.0.5"}}
	require.Equal(t, st, c.Heartbeat(context.Background()).Tailnet)
}

func TestInstanceTunnelCapabilityAndEndpointStatus(t *testing.T) {
	// Without a listener manager the agent cannot carry instance tunnels.
	plain := New(Options{HTTPPort: 18080, HTTPSPort: 18443})
	require.NotContains(t, plain.Facts(context.Background()).Capabilities, agentprotocol.CapInstanceTunnel)
	require.Empty(t, plain.Heartbeat(context.Background()).Endpoints)

	want := []agentprotocol.EndpointStatus{
		{Service: agentprotocol.TunnelS3, Port: agentprotocol.LocalPortS3, Listening: true},
		{Service: agentprotocol.TunnelLoki, Port: agentprotocol.LocalPortLoki, Error: "port 18100 is already in use on this server"},
	}
	c := New(Options{HTTPPort: 18080, HTTPSPort: 18443, EndpointStatus: func() []agentprotocol.EndpointStatus { return want }})
	require.Contains(t, c.Facts(context.Background()).Capabilities, agentprotocol.CapInstanceTunnel)
	require.Equal(t, want, c.Heartbeat(context.Background()).Endpoints)
}
