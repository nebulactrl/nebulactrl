package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/systemroute"
)

// The dashboard access endpoints end to end: instance admin only, the real
// client address behind the host proxy, the lock-out guard and the route push.
func TestDashboardAccessAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()
	systemroute.Set(systemroute.Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"})
	t.Cleanup(func() { systemroute.Set(systemroute.Config{}) })
	ctx := context.Background()
	cp := h.newOnlineServer("cp", 8*gib, 1*gib)
	_, err := h.app.Pool.Exec(ctx, `UPDATE servers SET is_control_plane = true WHERE id = $1`, uuid.MustParse(cp))
	require.NoError(t, err)
	pushes := func() int {
		var n int
		require.NoError(t, h.app.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_commands WHERE server_id = $1 AND kind = 'sync_routes'`, uuid.MustParse(cp)).Scan(&n))
		return n
	}
	// The test server sees the loopback as its peer, a private address whose
	// X-Forwarded-For is trusted exactly like the host proxy's.
	xff := func(ip string) []string { return []string{"X-Forwarded-For", ip} }
	tailnet := []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}

	r := h.do("GET", "/instance/dashboard-access", "", nil, xff("203.0.113.9")...)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	body := r.JSON()
	require.Equal(t, true, body["enabled"])
	require.Equal(t, "nebula.example.com", body["host"])
	require.Equal(t, "203.0.113.9", body["clientIp"])
	require.Equal(t, true, body["viaDashboardRoute"])
	require.Equal(t, cp, body["proxyServerId"])
	require.Empty(t, body["allowCidrs"])

	// A token that is not the instance admin's, or lacks the admin scope: 403.
	narrow := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusForbidden, h.do("GET", "/instance/dashboard-access", narrow, nil).Status)
	require.Equal(t, http.StatusForbidden, h.do("PUT", "/instance/dashboard-access", narrow, map[string]any{"allowCidrs": tailnet}).Status)
	require.Equal(t, http.StatusUnauthorized, h.otherBrowser().do("GET", "/instance/dashboard-access", "", nil).Status)

	// Locking the caller out needs a confirmation, and changes nothing before it.
	r = h.do("PUT", "/instance/dashboard-access", "", map[string]any{"allowCidrs": tailnet}, xff("203.0.113.9")...)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "dashboard_lockout", r.JSON()["code"])
	require.Contains(t, string(r.Raw), "203.0.113.9")
	require.Zero(t, pushes())

	// A resolver the server does not have: 422.
	r = h.do("PUT", "/instance/dashboard-access", "", map[string]any{"allowCidrs": tailnet, "dnsResolver": "cloudflare"}, xff("100.101.102.103")...)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "resolver_not_configured", r.JSON()["code"])

	// The caller's own address in the list: saved, and the control-plane server's routes re-sent.
	r = h.do("PUT", "/instance/dashboard-access", "", map[string]any{"allowCidrs": tailnet}, xff("100.101.102.103")...)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, []any{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, r.JSON()["allowCidrs"])
	require.Equal(t, true, r.JSON()["clientIpAllowed"])
	require.Equal(t, "admin", r.JSON()["source"])
	require.Equal(t, 1, pushes())

	// Confirmed lock-out is saved too, and the list can be cleared again.
	r = h.do("PUT", "/instance/dashboard-access", "", map[string]any{"allowCidrs": []string{"192.0.2.0/24"}, "confirmLockout": true}, xff("203.0.113.9")...)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["clientIpAllowed"])
	r = h.do("PUT", "/instance/dashboard-access", "", map[string]any{"allowCidrs": []string{}}, xff("203.0.113.9")...)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, 3, pushes())
	require.Equal(t, 3, h.auditCount("dashboard.access.update"))
}
