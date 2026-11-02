package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// httpRoute describes one request: a JSON body, or a raw one with headers.
type httpRoute struct {
	method, path string
	body         any
	raw          []byte
	rawLen       int64 // the declared Content-Length of a raw body (-1: none)
}

// serve builds the API router of the fixture's service the way the server does
// (the huma endpoints plus the raw routes) and runs one request as ctx's principal.
func (f *fixture) serve(ctx context.Context, rt httpRoute) *httptest.ResponseRecorder {
	f.t.Helper()
	p, err := policy.Authenticated(ctx)
	require.NoError(f.t, err)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	h := humachi.New(r, cfg)
	Register(h, f.svc)
	RegisterVolume(h, f.svc)
	f.svc.MountAPI(r)

	var body *bytes.Reader
	switch {
	case rt.raw != nil:
		body = bytes.NewReader(rt.raw)
	case rt.body != nil:
		raw, err := json.Marshal(rt.body)
		require.NoError(f.t, err)
		body = bytes.NewReader(raw)
	default:
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(rt.method, rt.path, body)
	switch {
	case rt.raw != nil:
		req.Header.Set("Content-Type", "application/octet-stream")
		if rt.rawLen != 0 {
			req.ContentLength = rt.rawLen
		}
	case rt.body != nil:
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) call(method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.serve(f.ctx, httpRoute{method: method, path: path, body: body})
}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	code, _ := jsonBody(t, rec)["code"].(string)
	return code
}

func items(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list, ok := jsonBody(t, rec)["items"].([]any)
	require.True(t, ok, "items is a list, never null: %s", rec.Body.String())
	return list
}

// reader is a token holding only projects:read.
func (f *fixture) reader() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Label: "ro", Scopes: []string{policy.ScopeProjectsRead}})
}

// agentAnswers plays the agent: it waits for the next command of kind and
// delivers the result frame. The returned func waits until it was delivered.
func (f *fixture) agentAnswers(kind, outcome, errMsg string) func() {
	f.t.Helper()
	done := make(chan error, 1)
	go func() {
		bg := context.Background()
		for range 500 {
			var id uuid.UUID
			err := f.pool.QueryRow(bg, "SELECT id FROM agent_commands WHERE kind = $1 AND status = 'pending' ORDER BY created_at LIMIT 1", kind).Scan(&id)
			if err == nil {
				done <- f.svc.HandleResult(bg, id, f.result(id, outcome, errMsg, agentprotocol.BackupSimpleResult{}))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- context.DeadlineExceeded
	}()
	return func() { require.NoError(f.t, <-done) }
}

func TestHTTPDestinationsNeverReturnCredentialsAndNeedAdmin(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{
		"name": "minio", "kind": "s3", "endpoint": "https://s3.example.com", "region": "eu-1", "bucket": "backups", "prefix": "nebula/prod",
		"pathStyle": true, "accessKey": canaryAccess, "secretKey": canarySecret,
	}

	rec := f.call(http.MethodPost, "/backup-destinations", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), canaryAccess)
	require.NotContains(t, rec.Body.String(), canarySecret)
	dest := jsonBody(t, rec)
	require.Equal(t, "minio", dest["name"])
	require.Equal(t, true, dest["accessKeySet"])
	require.Equal(t, true, dest["secretKeySet"])
	require.Equal(t, true, dest["offSite"])
	id := dest["id"].(string)

	// Validation: the schema bounds the name and the kind; the service checks the rest.
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, "/backup-destinations", map[string]any{"name": "", "kind": "s3"}).Code)
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, "/backup-destinations", map[string]any{"name": "x", "kind": "ftp"}).Code)
	rec = f.call(http.MethodPost, "/backup-destinations", map[string]any{"name": "nobucket", "kind": "s3", "accessKey": "a", "secretKey": "b"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	list := items(t, f.call(http.MethodGet, "/backup-destinations", nil))
	require.Len(t, list, 1)
	require.Equal(t, id, list[0].(map[string]any)["id"])
	require.NotContains(t, f.call(http.MethodGet, "/backup-destinations", nil).Body.String(), canarySecret)

	// A patch keeps the stored credentials unless new ones are sent.
	rec = f.call(http.MethodPatch, "/backup-destinations/"+id, map[string]any{"name": "minio-eu", "region": "eu-2"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patched := jsonBody(t, rec)
	require.Equal(t, "minio-eu", patched["name"])
	require.Equal(t, "eu-2", patched["region"])
	require.Equal(t, true, patched["secretKeySet"])
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPatch, "/backup-destinations/"+uuid.NewString(), map[string]any{"name": "x"}).Code)

	// Everything below the admin scope is refused, reads need projects:read.
	ro := f.reader()
	require.Equal(t, http.StatusOK, f.serve(ro, httpRoute{method: http.MethodGet, path: "/backup-destinations"}).Code)
	for _, rt := range []httpRoute{
		{method: http.MethodPost, path: "/backup-destinations", body: body},
		{method: http.MethodPatch, path: "/backup-destinations/" + id, body: map[string]any{"name": "hacked"}},
		{method: http.MethodDelete, path: "/backup-destinations/" + id},
		{method: http.MethodPost, path: "/backup-destinations/" + id + "/test", body: map[string]any{"serverId": f.server.ID}},
	} {
		require.Equal(t, http.StatusForbidden, f.serve(ro, rt).Code, rt.method+" "+rt.path)
	}
	require.Equal(t, "minio-eu", items(t, f.call(http.MethodGet, "/backup-destinations", nil))[0].(map[string]any)["name"])

	// Delete: a destination in use keeps existing; an unused one goes.
	sv := f.database("pg")
	rec = f.call(http.MethodPost, "/services/"+sv.ID.String()+"/backup-schedules", map[string]any{"destinationId": id, "cron": "@daily"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	rec = f.call(http.MethodDelete, "/backup-destinations/"+id, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "destination_in_use", errCode(t, rec))
	scheduleID := items(t, f.call(http.MethodGet, "/services/"+sv.ID.String()+"/backup-schedules", nil))[0].(map[string]any)["id"].(string)
	require.Equal(t, http.StatusNoContent, f.call(http.MethodDelete, "/backup-schedules/"+scheduleID, nil).Code)
	require.Equal(t, http.StatusNoContent, f.call(http.MethodDelete, "/backup-destinations/"+id, nil).Code)
	require.Empty(t, items(t, f.call(http.MethodGet, "/backup-destinations", nil)))
	require.Equal(t, http.StatusNotFound, f.call(http.MethodDelete, "/backup-destinations/"+id, nil).Code)
}

func TestHTTPDestinationTestWaitsForTheAgentVerdict(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	path := "/backup-destinations/" + dest.ID.String() + "/test"

	// An unknown server and an offline one are refused before anything is sent.
	rec := f.call(http.MethodPost, path, map[string]any{"serverId": uuid.New()})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "server_not_found", errCode(t, rec))
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	rec = f.call(http.MethodPost, path, map[string]any{"serverId": f.server.ID})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "server_offline", errCode(t, rec))
	require.Empty(t, f.disp.cmds)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	// The agent answers: the response carries the verdict, with the agent's message and no secret.
	wait := f.agentAnswers(agentprotocol.KindBackupTest, "failed", "write probe: AccessDenied for "+canarySecret)
	rec = f.call(http.MethodPost, path, map[string]any{"serverId": f.server.ID})
	wait()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := jsonBody(t, rec)
	require.Equal(t, false, out["lastTestOk"])
	require.Contains(t, out["lastTestMessage"], "AccessDenied")
	require.NotContains(t, rec.Body.String(), canarySecret)

	wait = f.agentAnswers(agentprotocol.KindBackupTest, "succeeded", "")
	rec = f.call(http.MethodPost, path, map[string]any{"serverId": f.server.ID})
	wait()
	require.Equal(t, true, jsonBody(t, rec)["lastTestOk"])
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, "/backup-destinations/"+uuid.NewString()+"/test", map[string]any{"serverId": f.server.ID}).Code)
}

func TestHTTPSchedulesValidateTheirCronAndRetention(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	base := "/services/" + sv.ID.String() + "/backup-schedules"

	require.Empty(t, items(t, f.call(http.MethodGet, base, nil)))
	rec := f.call(http.MethodPost, base, map[string]any{"destinationId": dest.ID, "cron": "not a cron"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, base, map[string]any{"destinationId": dest.ID, "cron": "@daily", "retentionCount": 1001}).Code, "the schema bounds the count")
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, base, map[string]any{"destinationId": uuid.New(), "cron": "@daily"}).Code)
	require.Empty(t, items(t, f.call(http.MethodGet, base, nil)), "refusals created nothing")

	rec = f.call(http.MethodPost, base, map[string]any{"destinationId": dest.ID, "cron": "30 2 * * *", "timezone": "Europe/Berlin", "retentionCount": 3, "retentionDays": 30})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	s := jsonBody(t, rec)
	require.Equal(t, "30 2 * * *", s["cron"])
	require.Equal(t, "Europe/Berlin", s["timezone"])
	require.Equal(t, true, s["enabled"])
	require.Equal(t, float64(3), s["retentionCount"])
	require.Equal(t, "minio", s["destinationName"])
	require.NotNil(t, s["nextRunAt"], "an enabled schedule knows its next run")
	id := s["id"].(string)

	rec = f.call(http.MethodPatch, "/backup-schedules/"+id, map[string]any{"enabled": false, "cron": "@weekly"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	upd := jsonBody(t, rec)
	require.Equal(t, false, upd["enabled"])
	require.Equal(t, "@weekly", upd["cron"])
	require.NotEqual(t, s["nextRunAt"], upd["nextRunAt"], "a new cron recomputes the next run")
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPatch, "/backup-schedules/"+id, map[string]any{"cron": "* * * * *"}).Code, "at most one run every five minutes")
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPatch, "/backup-schedules/"+uuid.NewString(), map[string]any{"enabled": true}).Code)

	ro := f.reader()
	require.Len(t, items(t, f.serve(ro, httpRoute{method: http.MethodGet, path: base})), 1)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPost, path: base, body: map[string]any{"destinationId": dest.ID, "cron": "@daily"}}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPatch, path: "/backup-schedules/" + id, body: map[string]any{"enabled": true}}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodDelete, path: "/backup-schedules/" + id}).Code)

	require.Equal(t, http.StatusNoContent, f.call(http.MethodDelete, "/backup-schedules/"+id, nil).Code)
	require.Equal(t, http.StatusNotFound, f.call(http.MethodDelete, "/backup-schedules/"+id, nil).Code)
	require.Equal(t, http.StatusNotFound, f.call(http.MethodGet, "/services/"+uuid.NewString()+"/backup-schedules", nil).Code)
}

func TestHTTPBackupLifecycleFromQueueToDeletion(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	base := "/services/" + sv.ID.String() + "/backups"

	require.Empty(t, items(t, f.call(http.MethodGet, base, nil)))
	rec := f.call(http.MethodPost, base, map[string]any{"destinationId": dest.ID})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	b := jsonBody(t, rec)
	require.Equal(t, "queued", b["status"])
	require.Equal(t, "manual", b["trigger"])
	require.Equal(t, "postgres", b["engine"])
	require.Equal(t, "alice@example.com", b["actor"])
	id := b["id"].(string)
	cmd, _ := f.disp.last()
	require.Equal(t, agentprotocol.KindDBBackup, cmd.Kind)

	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, base, map[string]any{"destinationId": uuid.New()}).Code)
	require.Equal(t, http.StatusNotFound, f.call(http.MethodGet, "/backups/"+uuid.NewString(), nil).Code)
	require.Equal(t, id, jsonBody(t, f.call(http.MethodGet, "/backups/"+id, nil))["id"])
	require.Len(t, items(t, f.call(http.MethodGet, base+"?limit=1", nil)), 1)
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodGet, base+"?limit=201", nil).Code)

	// A backup that is not stored yet cannot be restored, and its lock toggles.
	rec = f.call(http.MethodPut, "/backups/"+id+"/lock", map[string]any{"locked": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, true, jsonBody(t, rec)["locked"])
	rec = f.call(http.MethodDelete, "/backups/"+id, nil)
	require.Equal(t, http.StatusConflict, rec.Code, "a locked backup is never deleted")
	require.Equal(t, "backup_locked", errCode(t, rec))
	require.Equal(t, false, jsonBody(t, f.call(http.MethodPut, "/backups/"+id+"/lock", map[string]any{"locked": false}))["locked"])

	// Mark it stored, then deleting enqueues the object's removal through the agent.
	_, err := f.pool.Exec(context.Background(), "UPDATE backups SET status = 'succeeded', object_key = 'nebula/prod/x.dump', size_bytes = 10 WHERE id = $1", id)
	require.NoError(t, err)
	require.Equal(t, true, jsonBody(t, f.call(http.MethodGet, "/backups/"+id, nil))["restorable"])
	require.Equal(t, http.StatusNoContent, f.call(http.MethodDelete, "/backups/"+id, nil).Code)
	del, _ := f.disp.last()
	require.Equal(t, agentprotocol.KindBackupDelete, del.Kind)
	require.Equal(t, http.StatusNotFound, f.call(http.MethodDelete, "/backups/"+uuid.NewString(), nil).Code)

	// Reads need projects:read, writes deployments:write.
	ro := f.reader()
	require.Len(t, items(t, f.serve(ro, httpRoute{method: http.MethodGet, path: base})), 1)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPost, path: base, body: map[string]any{"destinationId": dest.ID}}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPut, path: "/backups/" + id + "/lock", body: map[string]any{"locked": true}}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodDelete, path: "/backups/" + id}).Code)
}

func TestHTTPRestoreToANewDatabaseAndTheRestoreReads(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	b := f.succeededBackup(sv, mustDest(t, f, dest.ID), nil, time.Hour, "nebula/prod/pg.dump")
	path := "/backups/" + b.ID.String() + "/restore"

	require.Empty(t, items(t, f.call(http.MethodGet, "/services/"+sv.ID.String()+"/backup-restores", nil)))
	require.Equal(t, http.StatusUnprocessableEntity, f.call(http.MethodPost, path, map[string]any{"mode": "sideways"}).Code, "the schema lists the modes")
	rec := f.call(http.MethodPost, path, map[string]any{"mode": "in_place"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusNotFound, f.call(http.MethodPost, "/backups/"+uuid.NewString()+"/restore", map[string]any{}).Code)

	rec = f.call(http.MethodPost, path, map[string]any{"name": "pg-copy"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	r := jsonBody(t, rec)
	require.Equal(t, "new", r["mode"], "the default mode never touches the original")
	require.Equal(t, "provisioning", r["status"])
	require.Equal(t, b.ID.String(), r["backupId"])
	require.Equal(t, sv.ID.String(), r["sourceServiceId"])
	require.NotNil(t, r["targetServiceId"])
	id := r["id"].(string)

	require.Equal(t, id, jsonBody(t, f.call(http.MethodGet, "/backup-restores/"+id, nil))["id"])
	require.Equal(t, http.StatusNotFound, f.call(http.MethodGet, "/backup-restores/"+uuid.NewString(), nil).Code)
	list := items(t, f.call(http.MethodGet, "/services/"+sv.ID.String()+"/backup-restores", nil))
	require.Len(t, list, 1)
	require.Equal(t, id, list[0].(map[string]any)["id"])

	ro := f.reader()
	require.Equal(t, http.StatusOK, f.serve(ro, httpRoute{method: http.MethodGet, path: "/backup-restores/" + id}).Code)
	require.Equal(t, http.StatusForbidden, f.serve(ro, httpRoute{method: http.MethodPost, path: path, body: map[string]any{}}).Code)
	// Restores create a database: projects:write is needed as well as deployments:write.
	dw := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Label: "dw", Scopes: []string{policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite}})
	require.Equal(t, http.StatusForbidden, f.serve(dw, httpRoute{method: http.MethodPost, path: path, body: map[string]any{}}).Code)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM backup_restores").Scan(&n))
	require.Equal(t, 1, n)
}
