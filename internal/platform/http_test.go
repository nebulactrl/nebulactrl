package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func (f *fixture) serve() func(p *auth.Principal, method, path string, body any) (int, map[string]any) {
	r := chi.NewRouter()
	var cur *auth.Principal
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if cur != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), cur))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	platform.Register(humachi.New(r, cfg), f.svc)
	return func(p *auth.Principal, method, path string, body any) (int, map[string]any) {
		f.t.Helper()
		cur = p
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(f.t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		if rec.Body.Len() > 0 && rec.Body.Bytes()[0] == '{' {
			require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		}
		return rec.Code, out
	}
}

func (f *fixture) principal() *auth.Principal {
	p, _ := auth.PrincipalFrom(f.ctx)
	return p
}

func TestPlatformServiceEndpoints(t *testing.T) {
	f := newFixture(t)
	do := f.serve()
	o := f.principal()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "hskey-api-http\n"

	code, body := do(o, http.MethodGet, "/platform-services", nil)
	require.Equal(t, http.StatusOK, code, body)
	require.NotEmpty(t, body["kinds"])

	code, body = do(o, http.MethodPost, "/platform-services", map[string]any{"kind": "headscale", "serverId": uuid.NewString()})
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Equal(t, "server_not_found", body["code"])

	code, body = do(o, http.MethodPost, "/platform-services", map[string]any{"kind": "headscale", "serverId": f.server.ID.String(), "hostname": "hs.example.test"})
	require.Equal(t, http.StatusAccepted, code, body)
	view := body["service"].(map[string]any)
	id := view["id"].(string)
	require.Equal(t, "provisioning", view["status"])
	require.Equal(t, "https://hs.example.test", view["publicUrl"])
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, uuid.MustParse(id), srv.URL)
	require.NoError(t, err)

	code, body = do(o, http.MethodPost, "/platform-services", map[string]any{"kind": "headscale", "serverId": f.server.ID.String()})
	require.Equal(t, http.StatusConflict, code, "one Headscale per team: %v", body)
	require.Equal(t, "platform_service_exists", body["code"])

	code, body = do(o, http.MethodGet, "/platform-services/"+id, nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "provisioning", body["status"])
	code, body = do(o, http.MethodGet, "/platform-services/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, code, body)

	// Not failed, not ready: neither retry nor reconnect applies yet.
	code, body = do(o, http.MethodPost, "/platform-services/"+id+"/retry", nil)
	require.Equal(t, http.StatusConflict, code, body)
	require.Equal(t, "not_failed", body["code"])
	code, body = do(o, http.MethodPost, "/platform-services/"+id+"/reconnect", nil)
	require.Equal(t, http.StatusConflict, code, body)
	require.Equal(t, "not_ready", body["code"])

	f.run(uuid.MustParse(id), 8)
	code, body = do(o, http.MethodGet, "/platform-services/"+id, nil)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "ready", body["status"])
	code, body = do(o, http.MethodPost, "/platform-services/"+id+"/reconnect", nil)
	require.Equal(t, http.StatusAccepted, code, body)
	require.Equal(t, "provisioning", body["status"])
	require.Equal(t, "wire", body["step"])

	f.run(uuid.MustParse(id), 8)
	code, body = do(o, http.MethodDelete, "/platform-services/"+id+"?confirm=nope", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Equal(t, "confirm_mismatch", body["code"])
	code, _ = do(o, http.MethodDelete, "/platform-services/"+id+"?confirm=headscale&deleteData=true", nil)
	require.Equal(t, http.StatusNoContent, code)
	code, _ = do(o, http.MethodGet, "/platform-services/"+id, nil)
	require.Equal(t, http.StatusNotFound, code)
	require.True(t, f.tailnet.deleted, "the integration headscale created goes with it")

	// Signed out and without the admin scope.
	code, _ = do(nil, http.MethodGet, "/platform-services", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	dev := *o
	dev.Scopes = []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}
	code, _ = do(&dev, http.MethodGet, "/platform-services", nil)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(&dev, http.MethodPost, "/platform-services", map[string]any{"kind": "headscale", "serverId": f.server.ID.String()})
	require.Equal(t, http.StatusForbidden, code)
}

func (f *fixture) readyFlow(url string) uuid.UUID {
	f.t.Helper()
	r := f.provision()
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET public_url = $2 WHERE id = $1`, r.View.ID, url)
	require.NoError(f.t, err)
	return r.View.ID
}

func TestWiringWaitsForHeadscaleToAnswerThenGivesUp(t *testing.T) {
	f := newFixture(t)
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "hskey-api-wait\n"
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	id := f.readyFlow(srv.URL)

	// Not answering yet: the job waits and says what for; nothing is wired.
	wait, done := f.run(id, 8)
	require.False(t, done)
	require.Positive(t, wait)
	row := f.row(id)
	require.Equal(t, platform.StatusProvisioning, row.Status)
	require.Equal(t, platform.StepWire, row.Step)
	require.Contains(t, row.StatusMessage, "Waiting for Headscale to answer")
	require.Contains(t, row.StatusMessage, "status 502")
	require.Empty(t, f.tailnet.put)

	// It answers: the same step finishes.
	healthy.Store(true)
	_, done = f.run(id, 4)
	require.True(t, done)
	require.Equal(t, platform.StatusReady, f.row(id).Status)
	require.Len(t, f.tailnet.put, 1)

	// A server that never answers ends in a timeout, not an endless wait.
	healthy.Store(false)
	f.svc.Now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	_, err := f.svc.Rewire(f.ctx, id)
	require.NoError(t, err)
	f.svc.Now = func() time.Time { return time.Now().Add(40 * time.Minute) }
	_, done = f.run(id, 4)
	require.True(t, done)
	row = f.row(id)
	require.Equal(t, platform.StatusFailed, row.Status)
	require.Contains(t, row.StatusMessage, "Timed out")
	require.Contains(t, row.StatusMessage, "Waiting for Headscale")
}

func TestWiringRefusesWhenAnIntegrationAppearedMeanwhile(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	id := f.readyFlow(srv.URL)
	f.tailnet.cur = tailscale.TailscaleIntegration{Configured: true, Provider: "tailscale_oauth"}

	_, done := f.run(id, 8)
	require.True(t, done)
	row := f.row(id)
	require.Equal(t, platform.StatusFailed, row.Status)
	require.Contains(t, row.StatusMessage, "Tailscale integration was added in the meantime")
	require.Empty(t, f.tailnet.put, "the other integration is not overwritten")
}

func TestWiringWaitsWhenHeadscaleCannotMintAKeyYet(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	id := f.readyFlow(srv.URL)

	// The first user cannot be created for a reason other than "it exists".
	f.agents.out["headscale users create"] = "database is locked"
	f.agents.code["headscale users create"] = 1
	wait, done := f.run(id, 8)
	require.False(t, done)
	require.Positive(t, wait)
	require.Contains(t, f.row(id).StatusMessage, "not ready to create its first user: database is locked")

	// "already exists" is fine; a key that is not one is not.
	f.agents.out["headscale users create"] = "user already exists"
	f.agents.out["headscale apikeys create"] = "Error: no such thing"
	f.agents.code["headscale apikeys create"] = 1
	wait, done = f.run(id, 4)
	require.False(t, done)
	require.Positive(t, wait)
	require.Contains(t, f.row(id).StatusMessage, "did not create an API key")
	require.Empty(t, f.tailnet.put)

	f.agents.out["headscale apikeys create"] = "hskey-api-ok\n"
	f.agents.code["headscale apikeys create"] = 0
	_, done = f.run(id, 4)
	require.True(t, done)
	require.Equal(t, platform.StatusReady, f.row(id).Status)
}

// failingTest is the tailnet integration whose connection test answers res.
type failingTest struct {
	*fakeTailnet
	res tailscale.TailscaleTestResult
}

func (f failingTest) Test(context.Context) (tailscale.TailscaleTestResult, error) { return f.res, nil }

func TestTheConnectionTestMustPassForAServiceToBeReady(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	f.agents.out["headscale users create"] = "User created"
	f.agents.out["headscale apikeys create"] = "hskey-api-test\n"
	f.svc.Tailnet = failingTest{f.tailnet, tailscale.TailscaleTestResult{OK: false, Checks: []tailscale.TailscaleCheck{{OK: true, Message: "fine"}, {OK: false, Message: "API key rejected"}}}}
	id := f.readyFlow(srv.URL)

	wait, done := f.run(id, 8)
	require.False(t, done)
	require.Positive(t, wait)
	require.Contains(t, f.row(id).StatusMessage, "The connection test failed: API key rejected")
	require.NotContains(t, f.row(id).StatusMessage, "fine")
}

func TestAServiceDeletedOutsideThePageFailsWiringWithAnExplanation(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	id := f.readyFlow(srv.URL)
	f.makeReady(id)
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET status = 'provisioning', step = 'wire' WHERE id = $1`, id)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET deleted_at = now() WHERE id = ANY($1)`, f.row(id).ServiceIds)
	require.NoError(t, err)

	_, done := f.run(id, 4)
	require.True(t, done)
	row := f.row(id)
	require.Equal(t, platform.StatusFailed, row.Status)
	require.Contains(t, row.StatusMessage, "removed outside this page")
}

func TestAdvanceIgnoresServicesThatAreGoneOrDone(t *testing.T) {
	f := newFixture(t)
	wait, done, err := f.svc.Advance(context.Background(), uuid.New())
	require.NoError(t, err)
	require.True(t, done, "a platform service that was removed ends its job")
	require.Zero(t, wait)

	r := f.provision()
	f.makeReady(r.View.ID)
	_, done, err = f.svc.Advance(context.Background(), r.View.ID)
	require.NoError(t, err)
	require.True(t, done, "a ready one has nothing left to do")
}

func TestListShowsFailureDetails(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	_, err := f.pool.Exec(context.Background(), `UPDATE platform_services SET status = 'failed', status_message = 'The deployment failed: boom' WHERE id = $1`, r.View.ID)
	require.NoError(t, err)
	l, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	require.NotEmpty(t, l.Kinds)
	var found bool
	for _, k := range l.Kinds {
		if k.Instance != nil && k.Instance.ID == r.View.ID {
			found = true
			require.Equal(t, platform.StatusFailed, k.Instance.Status)
			require.Equal(t, "The deployment failed: boom", k.Instance.Message)
		}
	}
	require.True(t, found)
	v, err := f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.Equal(t, platform.StatusFailed, v.Status)
}
