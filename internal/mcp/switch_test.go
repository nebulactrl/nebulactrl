package mcp_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/mcp"
)

// switchEnv is a control plane with its instance admin (the first user, who
// owns the workspace) and a second account that is not the instance admin.
type switchEnv struct {
	*harness
	admin, member context.Context
}

func newSwitchEnv(t *testing.T, mcpEnabled bool) *switchEnv {
	t.Helper()
	h := newHarness(t, mcpEnabled)
	bg := context.Background()
	var adminID, ws uuid.UUID
	require.NoError(t, h.app.Pool.QueryRow(bg, `SELECT id FROM users ORDER BY created_at, id LIMIT 1`).Scan(&adminID))
	require.NoError(t, h.app.Pool.QueryRow(bg, `SELECT id FROM workspaces LIMIT 1`).Scan(&ws))
	var memberID uuid.UUID
	require.NoError(t, h.app.Pool.QueryRow(bg,
		`INSERT INTO users (email, name, password_hash) VALUES ('member@example.com', 'Member', 'x') RETURNING id`).Scan(&memberID))
	principal := func(id uuid.UUID) context.Context {
		return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: id, WorkspaceID: ws, Role: auth.RoleOwner, Scopes: []string{"admin"}})
	}
	return &switchEnv{harness: h, admin: principal(adminID), member: principal(memberID)}
}

// mcpStatus is the HTTP status /mcp answers a bearer request with.
func (h *harness) mcpStatus(token string) int {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/mcp", strings.NewReader("{}"))
	require.NoError(h.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	_ = res.Body.Close()
	return res.StatusCode
}

func (e *switchEnv) setting(t *testing.T) bool {
	t.Helper()
	var on bool
	require.NoError(t, e.app.Pool.QueryRow(context.Background(), `SELECT mcp_enabled FROM instance_settings`).Scan(&on))
	return on
}

func (e *switchEnv) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	require.NoError(t, e.app.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_type = 'instance'`, action).Scan(&n))
	return n
}

func requireStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Status, ae.Error())
	if code != "" {
		require.Equal(t, code, ae.Code)
	}
}

func TestSwitchLockedByEnvironment(t *testing.T) {
	for _, on := range []bool{true, false} {
		e := newSwitchEnv(t, on)
		sw := mcp.NewSwitch(&config.Config{MCPEnabled: on, MCPFromEnv: true}, e.app.Pool, e.app.Auth)
		require.True(t, sw.Locked())
		require.Equal(t, on, sw.Enabled())

		// Even the instance admin cannot change what the environment decides.
		requireStatus(t, sw.Set(e.admin, !on), http.StatusConflict, "mcp_locked")
		require.Equal(t, on, sw.Enabled())
		require.Zero(t, e.auditCount(t, "instance.mcp.enable")+e.auditCount(t, "instance.mcp.disable"))

		// Load keeps the environment's value over the stored one.
		_, err := e.app.Pool.Exec(context.Background(), `UPDATE instance_settings SET mcp_enabled = $1`, !on)
		require.NoError(t, err)
		require.NoError(t, sw.Load(context.Background()))
		require.Equal(t, on, sw.Enabled())
	}

	// The same through REST: the harness' environment decides (true locks it on).
	e := newSwitchEnv(t, true)
	body := e.rest("GET", "/system", nil).json()
	require.Equal(t, true, body["mcpEnabled"])
	require.Equal(t, true, body["mcpLocked"])
	r := e.rest("PUT", "/system/mcp", map[string]any{"enabled": false})
	require.Equal(t, http.StatusConflict, r.status, string(r.raw))
	require.Equal(t, "mcp_locked", r.json()["code"])
	require.Equal(t, true, e.rest("GET", "/system", nil).json()["mcpEnabled"])
}

func TestSwitchIsInstanceAdminOnly(t *testing.T) {
	e := newSwitchEnv(t, false)
	sw := mcp.NewSwitch(&config.Config{}, e.app.Pool, e.app.Auth)
	require.False(t, sw.Locked())

	// A team owner who is not the instance admin.
	requireStatus(t, sw.Set(e.member, true), http.StatusForbidden, "")
	// A token without the admin scope, even the instance admin's own.
	var adminID uuid.UUID
	require.NoError(t, e.app.Pool.QueryRow(context.Background(), `SELECT id FROM users ORDER BY created_at, id LIMIT 1`).Scan(&adminID))
	narrow := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: adminID, Scopes: []string{"projects:read"}})
	requireStatus(t, sw.Set(narrow, true), http.StatusForbidden, "")
	// Not signed in.
	requireStatus(t, sw.Set(context.Background(), true), http.StatusUnauthorized, "")

	require.False(t, sw.Enabled())
	require.False(t, e.setting(t))
	require.Zero(t, e.auditCount(t, "instance.mcp.enable"))
}

func TestSwitchAdminTogglesAtOnce(t *testing.T) {
	e := newSwitchEnv(t, false)
	tok := e.token([]string{"admin"}, nil)

	body := e.rest("GET", "/system", nil).json()
	require.Equal(t, false, body["mcpEnabled"])
	require.Equal(t, false, body["mcpLocked"])
	require.Equal(t, http.StatusNotFound, e.mcpStatus(tok), "off: /mcp answers 404")

	// On: the response, the next GET /system and /mcp itself change at once.
	r := e.rest("PUT", "/system/mcp", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, r.status, string(r.raw))
	require.Equal(t, true, r.json()["mcpEnabled"])
	require.Equal(t, true, e.rest("GET", "/system", nil).json()["mcpEnabled"])
	require.NotEqual(t, http.StatusNotFound, e.mcpStatus(tok), "on: /mcp serves")
	cs := session(t, e.srv.URL, tok)
	require.NotEmpty(t, toolNames(t, cs))
	require.True(t, e.setting(t), "the choice is stored")
	require.Equal(t, 1, e.auditCount(t, "instance.mcp.enable"))

	// A restart (a new Switch over the same database) restores the choice.
	restarted := mcp.NewSwitch(&config.Config{}, e.app.Pool, e.app.Auth)
	require.False(t, restarted.Enabled(), "nothing is read before Load")
	require.NoError(t, restarted.Load(context.Background()))
	require.True(t, restarted.Enabled())

	// Off again.
	r = e.rest("PUT", "/system/mcp", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, r.status, string(r.raw))
	require.Equal(t, false, r.json()["mcpEnabled"])
	require.Equal(t, http.StatusNotFound, e.mcpStatus(tok))
	require.False(t, e.setting(t))
	require.Equal(t, 1, e.auditCount(t, "instance.mcp.disable"))
	require.NoError(t, restarted.Load(context.Background()))
	require.False(t, restarted.Enabled())
}
