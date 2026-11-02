package deployhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/deployhooks"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// httpCall drives one request through the registered management endpoints as
// the principal carried by ctx (a ctx without one is an anonymous caller).
func (f *fixture) httpCall(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	deployhooks.Register(humachi.New(r, cfg), f.svc)

	rd := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
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

func httpJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestHTTPManagementLifecycle(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	path := "/services/" + sv.ID.String() + "/deploy-hook"

	// Off by default; the pattern shows the URL shape without a secret.
	rec := f.httpCall(f.reader, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	require.Equal(t, false, m["enabled"])
	require.Equal(t, "https://nebula.example.com/hooks/deploy/"+sv.ID.String()+"/<secret>", m["urlPattern"])
	require.NotContains(t, m, "url")

	// Turning it on returns the URL once, and a working secret.
	rec = f.httpCall(f.ctx, http.MethodPut, path, map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m = httpJSON(t, rec)
	require.Equal(t, true, m["enabled"])
	url := m["url"].(string)
	prefix := "https://nebula.example.com/hooks/deploy/" + sv.ID.String() + "/"
	require.True(t, strings.HasPrefix(url, prefix), url)
	secret := strings.TrimPrefix(url, prefix)
	require.True(t, strings.HasPrefix(secret, deployhooks.SecretPrefix))
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, secret, "", nil, "").Status)

	rec = f.httpCall(f.reader, http.MethodGet, path, nil)
	m = httpJSON(t, rec)
	require.Equal(t, true, m["enabled"])
	require.NotContains(t, m, "url", "the secret is never shown again")
	require.NotContains(t, rec.Body.String(), secret)
	require.Equal(t, "deploying", m["lastOutcome"])
	require.NotNil(t, m["lastCalledAt"])

	// Enabling again changes nothing: same secret, no URL.
	rec = f.httpCall(f.ctx, http.MethodPut, path, map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, httpJSON(t, rec), "url")
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, secret, "", nil, "").Status, "the first secret still works")

	// Rotating gives a new URL and kills the old one at once.
	rec = f.httpCall(f.ctx, http.MethodPost, path+"/rotate", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	next := strings.TrimPrefix(httpJSON(t, rec)["url"].(string), prefix)
	require.NotEqual(t, secret, next)
	require.Equal(t, http.StatusNotFound, f.post(sv.ID, secret, "", nil, "").Status)
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, next, "", nil, "").Status)

	// Turning it off deletes the secret; rotating a disabled hook conflicts.
	rec = f.httpCall(f.ctx, http.MethodPut, path, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, false, httpJSON(t, rec)["enabled"])
	require.Equal(t, http.StatusNotFound, f.post(sv.ID, next, "", nil, "").Status)
	rec = f.httpCall(f.ctx, http.MethodPost, path+"/rotate", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "deploy_hook_disabled", httpJSON(t, rec)["code"])

	require.Len(t, f.auditActions("deploy_hook.enable"), 1, "the idempotent enable is not audited twice")
	require.Len(t, f.auditActions("deploy_hook.rotate"), 1)
	require.Len(t, f.auditActions("deploy_hook.disable"), 1)
}

func TestHTTPManagementPermissionsAndLookups(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	path := "/services/" + sv.ID.String() + "/deploy-hook"

	// projects:read may look, only deployments:write may change.
	require.Equal(t, http.StatusForbidden, f.httpCall(f.reader, http.MethodPut, path, map[string]any{"enabled": true}).Code)
	require.Equal(t, http.StatusForbidden, f.httpCall(f.reader, http.MethodPost, path+"/rotate", nil).Code)
	require.Equal(t, http.StatusUnauthorized, f.httpCall(context.Background(), http.MethodGet, path, nil).Code)
	require.Equal(t, http.StatusUnauthorized, f.httpCall(context.Background(), http.MethodPut, path, map[string]any{"enabled": true}).Code)
	require.Equal(t, http.StatusUnauthorized, f.httpCall(context.Background(), http.MethodPost, path+"/rotate", nil).Code)
	v, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled, "refused calls changed nothing")

	// Another team's service and unknown ids are the same 404.
	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: uuid.New(), Scopes: []string{"projects:read", "deployments:write"}})
	require.Equal(t, http.StatusNotFound, f.httpCall(other, http.MethodGet, path, nil).Code)
	require.Equal(t, http.StatusNotFound, f.httpCall(other, http.MethodPut, path, map[string]any{"enabled": true}).Code)
	missing := "/services/" + uuid.NewString() + "/deploy-hook"
	require.Equal(t, http.StatusNotFound, f.httpCall(f.ctx, http.MethodGet, missing, nil).Code)
	require.Equal(t, http.StatusNotFound, f.httpCall(f.ctx, http.MethodPost, missing+"/rotate", nil).Code)
}

func TestHTTPEnablingNeedsAPublicURL(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	f.svc = deployhooks.NewService(deployhooks.Deps{Pool: f.pool, Config: &config.Config{}, Deployer: f.dep})
	for _, call := range []struct{ method, path string }{{http.MethodPut, ""}, {http.MethodPost, "/rotate"}} {
		rec := f.httpCall(f.ctx, call.method, "/services/"+sv.ID.String()+"/deploy-hook"+call.path, map[string]any{"enabled": true})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		require.Equal(t, "public_url_required", httpJSON(t, rec)["code"])
	}
}

func TestCallRejectsAnOversizedPayload(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	rep := f.post(sv.ID, secret, "", nil, strings.Repeat("x", (1<<20)+1))
	require.Equal(t, http.StatusRequestEntityTooLarge, rep.Status, rep.Raw)
	require.Equal(t, "payload_too_large", rep.Body["code"])
	require.Empty(t, f.dep.snapshot(), "nothing is deployed from a payload that was not read")
	require.Empty(t, f.auditActions("deployment.hook"))
}

// failingDeployer answers every deployment with err.
type failingDeployer struct{ err error }

func (d failingDeployer) DeployWith(context.Context, uuid.UUID, string, deployments.InternalOptions) (deployments.DeploymentView, error) {
	return deployments.DeploymentView{}, d.err
}

func (f *fixture) withDeployer(d deployhooks.Deployer) {
	f.t.Helper()
	hs := deployhooks.NewService(deployhooks.Deps{
		Pool: f.pool, Config: &config.Config{PublicURL: "https://nebula.example.com/"}, Deployer: d,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	hs.SetLimit(time.Millisecond, 1000)
	r := chi.NewRouter()
	hs.Mount(r)
	ts := httptest.NewServer(r)
	f.t.Cleanup(ts.Close)
	f.svc, f.ts = hs, ts
}

func TestCallAnswers500WithoutDetailsWhenTheDeploymentFailsUnexpectedly(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	f.withDeployer(failingDeployer{err: errors.New("pq: connection refused on 10.0.0.5 with password hunter2")})

	rep := f.post(sv.ID, secret, "", nil, "")
	require.Equal(t, http.StatusInternalServerError, rep.Status, rep.Raw)
	require.NotContains(t, rep.Raw, "hunter2", "internal errors stay in the log")
	require.NotContains(t, rep.Raw, "10.0.0.5")
	require.Empty(t, f.auditActions("deployment.hook"), "a call that did not deploy is not recorded as one")
}

func TestCallPassesOnDeploymentRefusals(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	f.withDeployer(refusingDeployer{})

	rep := f.post(sv.ID, secret, "", nil, "")
	require.Equal(t, http.StatusUnprocessableEntity, rep.Status, rep.Raw)
	require.Equal(t, "server_offline", rep.Body["code"])
}

type refusingDeployer struct{}

func (refusingDeployer) DeployWith(context.Context, uuid.UUID, string, deployments.InternalOptions) (deployments.DeploymentView, error) {
	return deployments.DeploymentView{}, apierr.Unprocessable("server_offline", "the server is offline")
}

func TestAuditedReasonsAreBounded(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	longTag := strings.Repeat("t", 500)

	rep := f.post(sv.ID, secret, "", nil, dockerHubPush("acme/api", longTag))
	require.Equal(t, http.StatusAccepted, rep.Status, rep.Raw)
	require.Equal(t, "ignored", rep.Body["status"])
	require.Contains(t, rep.Body["reason"], longTag, "the caller sees the whole reason")

	rows := f.auditActions("deployment.hook")
	require.Len(t, rows, 1)
	reason := rows[0]["reason"].(string)
	require.True(t, strings.HasSuffix(reason, "…"))
	require.Len(t, []rune(reason), 301, "the audit entry keeps 300 characters and an ellipsis")
}

func TestSweepSkipsAQueuedCallItCannotUse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	garbled := f.imageService("garbled", "acme/garbled:v1")
	failing := f.imageService("failing", "acme/failing:v1")
	f.enable(garbled.ID)
	f.enable(failing.ID)
	// One queued call is not a call at all, the other cannot be deployed.
	_, err := f.pool.Exec(ctx, `UPDATE deploy_hooks SET pending = '"not a call"'::jsonb WHERE service_id = $1`, garbled.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE deploy_hooks SET pending = '{"imageRef":"acme/failing:v2","source":"generic"}'::jsonb WHERE service_id = $1`, failing.ID)
	require.NoError(t, err)
	v, err := f.svc.Get(f.reader, garbled.ID)
	require.NoError(t, err)
	require.True(t, v.Queued)

	f.withDeployer(failingDeployer{err: errors.New("registry unreachable")})
	require.NoError(t, f.svc.Sweep(ctx), "one bad call does not fail the sweep")
	for _, id := range []uuid.UUID{garbled.ID, failing.ID} {
		v, err := f.svc.Get(f.reader, id)
		require.NoError(t, err)
		require.False(t, v.Queued, "a claimed call is not retried forever")
	}
	require.Empty(t, f.dep.snapshot())
	require.Empty(t, f.auditActions("deployment.hook"))
}

func TestSweepJobDeploysQueuedCallsThroughRiver(t *testing.T) {
	require.Equal(t, "deploy_hook_sweep", deployhooks.SweepArgs{}.Kind())

	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	f.dep.setBusy(true)
	require.Equal(t, "queued", f.post(sv.ID, secret, "tag=v2", nil, "").Body["status"])
	f.dep.setBusy(false)

	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deployhooks.RegisterJobs(jm, f.svc)
	require.NoError(t, jm.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jm.Stop(ctx)
	})
	require.NoError(t, jm.Insert(context.Background(), deployhooks.SweepArgs{}, nil))
	require.Eventually(t, func() bool { return len(f.dep.snapshot()) == 1 }, 20*time.Second, 50*time.Millisecond)
	require.Equal(t, "acme/api:v2", f.dep.snapshot()[0].Opts.ImageRef)
}
