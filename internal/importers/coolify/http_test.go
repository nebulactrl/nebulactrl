package coolify

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
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
)

func (f *fixture) post(ctx context.Context, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("t", "0")
	cfg.CreateHooks = nil
	Register(humachi.New(r, cfg), f.svc)
	raw, err := json.Marshal(body)
	require.NoError(f.t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCoolifyRoutesPlanAndApply(t *testing.T) {
	f := newFixture(t)

	w := f.post(f.ctx, "/imports/coolify/plan", CoolifyPlanRequest{Source: f.source(), ServerID: &f.srv.ID})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var plan map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &plan))
	require.Equal(t, true, plan["installable"])
	require.NotContains(t, w.Body.String(), fakeToken, "the Coolify token is never echoed")
	require.Zero(t, f.count("SELECT count(*) FROM projects"), "a plan writes nothing")

	w = f.post(f.ctx, "/imports/coolify/apply", CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), fakeToken)
	require.Positive(t, f.count("SELECT count(*) FROM services"))

	// A token that cannot write, or an unusable source, is refused.
	read := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Scopes: []string{"projects:read"}})
	require.Equal(t, http.StatusForbidden, f.post(read, "/imports/coolify/plan", CoolifyPlanRequest{Source: f.source()}).Code)
	require.Equal(t, http.StatusForbidden, f.post(read, "/imports/coolify/apply", CoolifyApplyRequest{Source: f.source(), ServerID: f.srv.ID}).Code)
	bad := f.source()
	bad.Token = "wrong-token"
	w = f.post(f.ctx, "/imports/coolify/plan", CoolifyPlanRequest{Source: bad})
	require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
	require.NotContains(t, w.Body.String(), "wrong-token")
}
