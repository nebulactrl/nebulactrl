package netguard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

func TestBlockedAddresses(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.9", "192.168.1.1", "169.254.169.254", "0.0.0.0", "0.1.2.3",
		"100.64.0.1", "fd00:ec2::254", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "224.0.0.1", "240.0.0.1",
	} {
		require.True(t, netguard.Blocked(netip.MustParseAddr(s), netguard.Allow{}), s)
	}
	for _, s := range []string{"8.8.8.8", "140.82.112.3", "2606:4700::1111"} {
		require.False(t, netguard.Blocked(netip.MustParseAddr(s), netguard.Allow{}), s)
	}
}

func TestAllowListPermitsPrivateButNeverMetadata(t *testing.T) {
	a := netguard.ParseAllow("10.0.0.0/8, 127.0.0.1, git.corp.example, 169.254.169.254, bogus entry/")
	require.False(t, netguard.Blocked(netip.MustParseAddr("10.2.3.4"), a))
	require.False(t, netguard.Blocked(netip.MustParseAddr("127.0.0.1"), a))
	require.True(t, netguard.Blocked(netip.MustParseAddr("127.0.0.2"), a))
	require.True(t, netguard.Blocked(netip.MustParseAddr("192.168.0.1"), a))
	require.True(t, netguard.Blocked(netip.MustParseAddr("169.254.169.254"), a), "metadata cannot be allowed")
}

func TestClientRefusesLoopbackServerAndRedirectsToIt(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "")
	hit := false
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer internal.Close()

	c := netguard.NewClient(5 * time.Second)
	_, err := c.Get(internal.URL)
	require.ErrorContains(t, err, "refused")
	require.False(t, hit, "the request never reached the loopback server")
}

func TestClientAllowsLoopbackWhenTheAdminAllowsIt(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	resp, err := netguard.NewClient(5 * time.Second).Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestCheckHost(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "")
	ctx := context.Background()
	require.ErrorIs(t, netguard.CheckHost(ctx, "127.0.0.1"), netguard.ErrBlocked)
	require.ErrorIs(t, netguard.CheckHost(ctx, "[::1]"), netguard.ErrBlocked)
	require.ErrorIs(t, netguard.CheckHost(ctx, "169.254.169.254"), netguard.ErrBlocked)
	require.NoError(t, netguard.CheckHost(ctx, "8.8.8.8"))
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	require.NoError(t, netguard.CheckHost(ctx, "127.0.0.1"))
}
