package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// call runs one request against the control-plane backup endpoints as ctx's principal.
func (e *instEnv) call(t *testing.T, ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	p, err := policy.Authenticated(ctx)
	if err != nil {
		t.Fatalf("no principal: %v", err)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	RegisterInstance(humachi.New(r, cfg), e.svc)

	var rd *bytes.Reader
	if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHTTPInstanceBackupSettingsAreInstanceAdminOnlyAndNeverShowCredentials(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())

	rec := e.call(t, e.ctx, http.MethodGet, "/instance/backup", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	v := jsonBody(t, rec)
	require.Equal(t, false, v["enabled"])
	require.Equal(t, DefaultInstanceSchedule, v["schedule"])
	require.Equal(t, float64(DefaultInstanceRetention), v["retentionCount"])
	require.Equal(t, true, v["toolsAvailable"])

	// A team owner who is not the instance admin, and a token without the admin scope, see nothing.
	member := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: e.ws, Role: auth.RoleOwner, Scopes: []string{policy.ScopeAdmin}})
	token := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, TokenID: new(uuid.New()), UserID: uuid.New(), WorkspaceID: e.ws, Scopes: []string{policy.ScopeProjectsRead}})
	for name, ctx := range map[string]context.Context{"not the instance admin": member, "no admin scope": token} {
		for _, rt := range []struct{ method, path string }{
			{http.MethodGet, "/instance/backup"}, {http.MethodPatch, "/instance/backup"}, {http.MethodPost, "/instance/backup/test"},
			{http.MethodPost, "/instance/backup/key-acknowledgement"}, {http.MethodPost, "/instance/backup/runs"}, {http.MethodGet, "/instance/backup/runs"},
		} {
			var body any
			if rt.method == http.MethodPatch {
				body = map[string]any{"enabled": true}
			}
			require.Equal(t, http.StatusForbidden, e.call(t, ctx, rt.method, rt.path, body).Code, name+" "+rt.method+" "+rt.path)
		}
	}

	// Validation: the schema bounds the numbers and the kind; the service checks the rest.
	for name, body := range map[string]map[string]any{
		"retention 0":         {"retentionCount": 0},
		"retention 366":       {"retentionCount": 366},
		"unknown kind":        {"kind": "ftp"},
		"schedule too often":  {"schedule": "* * * * *"},
		"enable without keys": {"enabled": true, "kind": "s3", "bucket": "nebula-backups"},
	} {
		rec = e.call(t, e.ctx, http.MethodPatch, "/instance/backup", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name+": "+rec.Body.String())
	}
	require.Equal(t, false, jsonBody(t, e.call(t, e.ctx, http.MethodGet, "/instance/backup", nil))["enabled"], "refusals stored nothing")

	rec = e.call(t, e.ctx, http.MethodPatch, "/instance/backup", map[string]any{
		"enabled": true, "kind": "s3", "endpoint": "https://s3.example.com", "region": "eu-central-1", "bucket": "nebula-backups",
		"accessKey": "AKIACANARY", "secretKey": canaryS3Secret, "retentionCount": 30,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), canaryS3Secret)
	require.NotContains(t, rec.Body.String(), "AKIACANARY")
	updated := jsonBody(t, rec)
	require.Equal(t, true, updated["enabled"])
	require.Equal(t, float64(30), updated["retentionCount"])
	require.Equal(t, true, updated["destination"].(map[string]any)["secretKeySet"])
	require.NotNil(t, updated["nextRunAt"])

	// The key reminder is dismissed once.
	require.Nil(t, updated["keyAcknowledgedAt"])
	rec = e.call(t, e.ctx, http.MethodPost, "/instance/backup/key-acknowledgement", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, jsonBody(t, rec)["keyAcknowledgedAt"])
}

func TestHTTPInstanceBackupRunsOneAtATimeAndAreListed(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())

	// Without a complete destination there is nothing to run against.
	e.svc.backupDir = ""
	rec := e.call(t, e.ctx, http.MethodPost, "/instance/backup/runs", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	e.svc.backupDir = e.dir
	require.Empty(t, items(t, e.call(t, e.ctx, http.MethodGet, "/instance/backup/runs", nil)))

	e.enableLocal(t, 5)
	rec = e.call(t, e.ctx, http.MethodPost, "/instance/backup/runs", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	run := jsonBody(t, rec)
	require.Equal(t, "running", run["status"])
	require.Equal(t, "manual", run["trigger"])
	require.Equal(t, "alice@example.com", run["actorLabel"])

	rec = e.call(t, e.ctx, http.MethodPost, "/instance/backup/runs", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "backup_in_progress", errCode(t, rec))

	e.svc.Run(e.ctx, uuid.MustParse(run["id"].(string)))
	list := items(t, e.call(t, e.ctx, http.MethodGet, "/instance/backup/runs?limit=10", nil))
	require.Len(t, list, 1)
	done := list[0].(map[string]any)
	require.Equal(t, "succeeded", done["status"])
	require.Equal(t, float64(len(fakeDump)), done["sizeBytes"])
	require.NotEmpty(t, done["sha256"])
	require.Equal(t, http.StatusUnprocessableEntity, e.call(t, e.ctx, http.MethodGet, "/instance/backup/runs?limit=201", nil).Code)

	// The local destination passes its probe, and the verdict is stored on the settings.
	rec = e.call(t, e.ctx, http.MethodPost, "/instance/backup/test", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, true, jsonBody(t, rec)["destination"].(map[string]any)["lastTestOk"])
}
