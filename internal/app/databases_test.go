package app_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newDatabase creates a project, a server and a managed Postgres on it.
func (h *harness) newDatabase(name, engine, version string) (dbID, envID, serverID string, created resp) {
	h.t.Helper()
	pid := h.createProject("P " + name)
	r := h.do("GET", "/projects/"+pid+"/environments", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	envID = r.JSON()["items"].([]any)[0].(map[string]any)["id"].(string)
	r = h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "srv-" + name})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	serverID = r.JSON()["server"].(map[string]any)["id"].(string)
	created = h.do("POST", "/environments/"+envID+"/databases", "", map[string]any{
		"name": name, "engine": engine, "version": version, "serverId": serverID,
	}, "Idempotency-Key", "create-"+name)
	require.Equal(h.t, http.StatusCreated, created.Status, string(created.Raw))
	return created.JSON()["id"].(string), envID, serverID, created
}

func TestDatabaseEndpointsCatalogCreateRevealAndSecrecy(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, h.app.Start(ctx))

	r := h.do("GET", "/database-engines", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	engines := r.JSON()["engines"].([]any)
	require.GreaterOrEqual(t, len(engines), 7)

	dbID, envID, serverID, created := h.newDatabase("pg", "postgres", "17")
	require.NotEmpty(t, created.Header.Get("ETag"))
	require.Equal(t, "postgres:17", created.JSON()["config"].(map[string]any)["image"])

	// Replaying the create returns the identical body (idempotency).
	again := h.do("POST", "/environments/"+envID+"/databases", "", map[string]any{
		"name": "pg", "engine": "postgres", "version": "17", "serverId": serverID,
	}, "Idempotency-Key", "create-pg")
	require.Equal(t, http.StatusCreated, again.Status)
	require.JSONEq(t, string(created.Raw), string(again.Raw))

	// Variables list: the generated password is write-only.
	vars := h.do("GET", "/services/"+dbID+"/variables", "", nil)
	require.Equal(t, http.StatusOK, vars.Status, string(vars.Raw))

	// Reveal: no-store, audited, restricted by scope.
	rev := h.do("POST", "/services/"+dbID+"/database/reveal", "", map[string]any{"key": "DATABASE_URL"}, "Idempotency-Key", "reveal-1")
	require.Equal(t, http.StatusOK, rev.Status, string(rev.Raw))
	require.Contains(t, rev.Header.Get("Cache-Control"), "no-store")
	value := rev.JSON()["value"].(string)
	u, err := url.Parse(value)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	require.Len(t, pw, 32)
	require.NotContains(t, string(vars.Raw), pw, "secret variables are never returned")

	ro := h.createToken([]string{"projects:read", "servers:read"}, nil)
	denied := h.do("POST", "/services/"+dbID+"/database/reveal", ro, map[string]any{"key": "DATABASE_URL"})
	require.Equal(t, http.StatusForbidden, denied.Status, string(denied.Raw))
	// The read-only token can read the panel, which carries no values.
	view := h.do("GET", "/services/"+dbID+"/database", ro, nil)
	require.Equal(t, http.StatusOK, view.Status, string(view.Raw))
	require.NotContains(t, string(view.Raw), pw)
	require.Contains(t, string(view.Raw), "${{pg.DATABASE_URL}}")
	rw := h.createToken([]string{"deployments:write"}, nil)
	ok := h.do("POST", "/services/"+dbID+"/database/reveal", rw, map[string]any{"key": "DATABASE_URL"})
	require.Equal(t, http.StatusOK, ok.Status, string(ok.Raw))

	// The reveal is never stored: not in idempotency bodies, not in the audit log.
	var leaked int
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM idempotency_keys WHERE body::text LIKE '%' || $1 || '%') + (SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%' || $1 || '%')", pw).Scan(&leaked))
	require.Zero(t, leaked)
	var audited int
	require.NoError(t, h.app.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action = 'secret.reveal'").Scan(&audited))
	require.Equal(t, 2, audited)

	// A plain service has no database panel.
	plain, _ := h.newService("web")
	nope := h.do("GET", "/services/"+plain+"/database", "", nil)
	require.Equal(t, http.StatusConflict, nope.Status, string(nope.Raw))
	require.Equal(t, "not_a_managed_database", nope.JSON()["code"])
}

func TestDatabaseCredentialsNeverReachADeployCommandPayload(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, h.app.Start(ctx))

	dbID, _, serverID, _ := h.newDatabase("pg", "postgres", "17")
	rev := h.do("POST", "/services/"+dbID+"/database/reveal", "", map[string]any{"key": "DATABASE_URL"})
	u, err := url.Parse(rev.JSON()["value"].(string))
	require.NoError(t, err)
	pw, _ := u.User.Password()

	r := h.do("POST", "/services/"+dbID+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	var payload string
	require.Eventually(t, func() bool {
		return h.app.Pool.QueryRow(ctx, "SELECT payload::text FROM agent_commands WHERE server_id = $1 AND kind = 'deploy'", serverID).Scan(&payload) == nil
	}, 15*time.Second, 100*time.Millisecond)
	require.NotContains(t, payload, pw)
	require.Contains(t, payload, `"POSTGRES_PASSWORD"`, "only the key is listed; the value arrives with the command at send time")
	require.Contains(t, payload, "/var/lib/postgresql/data")
	require.Contains(t, payload, `"recreate"`, "a database has a volume, so it never rolls")
}
