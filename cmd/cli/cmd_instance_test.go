package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute/systemroutetest"
)

func TestInstanceDashboardAccess(t *testing.T) {
	e := newEnv(t)
	e.login()

	// No system route (no dashboard domain): nothing to restrict, and a clear message.
	r := e.mustRun("", "instance", "dashboard-access", "get")
	require.Contains(t, r.stdout, "nothing to restrict")

	systemroutetest.Set(systemroute.Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"})
	t.Cleanup(func() { systemroutetest.Set(systemroute.Config{}) })

	r = e.mustRun("", "instance", "dashboard-access", "get")
	require.Contains(t, r.stdout, "nebula.example.com")
	require.Contains(t, r.stdout, "everybody")
	require.Contains(t, r.stdout, "automatic HTTP-01")

	r = e.mustRun("", "instance", "dashboard-access", "set", "--tailnet-only")
	require.Contains(t, r.stdout, "100.64.0.0/10, fd7a:115c:a1e0::/48")
	require.Contains(t, r.stdout, "admin")

	// --allow replaces the list; commas and repeats both work; the certificate setting stays.
	r = e.mustRun("", "--json", "instance", "dashboard-access", "set", "--allow", "203.0.113.0/24,192.0.2.7", "--allow", "2001:db8::/32")
	got := decode[map[string]any](t, r.stdout)
	require.Equal(t, []any{"203.0.113.0/24", "192.0.2.7", "2001:db8::/32"}, got["allowCidrs"])
	require.Equal(t, "", got["dnsResolver"])

	r = e.mustRun("", "instance", "dashboard-access", "set", "--clear-allow")
	require.Contains(t, r.stdout, "everybody")

	// A resolver the control-plane server does not have is refused with the API's message.
	r = e.run("", "instance", "dashboard-access", "set", "--dns-resolver", "cloudflare")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.all(), "resolver")

	// Usage errors.
	require.NotEqual(t, 0, e.run("", "instance", "dashboard-access", "set").code)
	require.NotEqual(t, 0, e.run("", "instance", "dashboard-access", "set", "--tailnet-only", "--clear-allow").code)
	require.NotEqual(t, 0, e.run("", "instance", "dashboard-access", "set", "--http-01", "--dns-resolver", "cloudflare").code)
	r = e.run("", "instance", "dashboard-access", "set", "--allow", "not-a-cidr")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.all(), "allowCidrs")
}
