package previews_test

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
	"github.com/nebulactrl/nebulactrl/internal/previews"
)

// callAPI drives one request through the preview endpoints as the principal in ctx.
func (f *fixture) callAPI(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
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
	previews.Register(humachi.New(r, cfg), f.pv)
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

func (f *fixture) as(scopes ...string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: uuid.New(), TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Scopes: scopes,
	})
}

func TestHTTPPreviewSettings(t *testing.T) {
	f := newFixture(t)
	path := "/projects/" + f.project.ID.String() + "/previews/settings"

	rec := f.callAPI(f.ctx, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var st previews.PreviewSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.False(t, st.Enabled)
	require.Equal(t, f.project.ID, st.ProjectID)
	require.Equal(t, 5, st.MaxConcurrent)
	require.Equal(t, 7, st.IdleDays)
	require.Equal(t, f.base.ID, *st.BaseEnvironmentID)

	rec = f.callAPI(f.ctx, http.MethodPut, path, map[string]any{
		"enabled": true, "baseEnvironmentId": f.base.ID, "forkPrs": true, "maxConcurrent": 3, "idleDays": 14,
		"seedCommands":     []map[string]any{{"service": "api", "command": "npm run migrate", "runOn": "push"}},
		"excludedServices": []string{"web"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.True(t, st.Enabled)
	require.True(t, st.ForkPRs)
	require.Equal(t, 3, st.MaxConcurrent)
	require.Equal(t, 14, st.IdleDays)
	require.Equal(t, []previews.PreviewSeedCommand{{Service: "api", Command: "npm run migrate", RunOn: "push"}}, st.SeedCommands)
	require.Equal(t, []string{"web"}, st.ExcludedServices)

	// What was written is what the next read returns.
	rec = f.callAPI(f.ctx, http.MethodGet, path, nil)
	var again previews.PreviewSettings
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &again))
	require.Equal(t, st.SeedCommands, again.SeedCommands)
	require.True(t, again.Enabled)

	// Refused input.
	rec = f.callAPI(f.ctx, http.MethodPut, path, map[string]any{"enabled": true, "baseEnvironmentId": f.base.ID, "maxConcurrent": 51})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = f.callAPI(f.ctx, http.MethodPut, path, map[string]any{
		"enabled": true, "baseEnvironmentId": f.base.ID, "seedCommands": []map[string]any{{"service": "nope", "command": "x"}},
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = f.callAPI(f.ctx, http.MethodGet, "/projects/"+uuid.NewString()+"/previews/settings", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Reading needs projects:read, writing projects:write.
	ro := f.as(policy.ScopeProjectsRead)
	require.Equal(t, http.StatusOK, f.callAPI(ro, http.MethodGet, path, nil).Code)
	require.Equal(t, http.StatusForbidden, f.callAPI(ro, http.MethodPut, path, map[string]any{"enabled": false}).Code)
	none := f.as(policy.ScopeLogsRead)
	require.Equal(t, http.StatusForbidden, f.callAPI(none, http.MethodGet, path, nil).Code)
}

func TestHTTPPreviewListAndDelete(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker", "postgres"} })
	list := "/projects/" + f.project.ID.String() + "/previews"

	rec := f.callAPI(f.ctx, http.MethodGet, list, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Items []previews.PreviewView `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Empty(t, out.Items)

	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(41, "opened", "4141414141414141414141414141414141414141")))
	pv := f.previewOf(41)
	rec = f.callAPI(f.ctx, http.MethodGet, list, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Items, 1)
	v := out.Items[0]
	require.Equal(t, pv.ID, v.ID)
	require.Equal(t, 41, v.PRNumber)
	require.Equal(t, "pr-41", v.EnvironmentSlug)
	require.Equal(t, "acme/shop", v.Repo)
	require.Equal(t, "Add checkout", v.PRTitle)
	require.Equal(t, "octocat", v.PRAuthor)
	require.Equal(t, "feature-4141414141414141414141414141414141414141", v.HeadBranch)
	require.Equal(t, []string{"https://api-pr-41.example.com"}, v.URLs)

	// A reader cannot delete; a writer starts the teardown (202) and may repeat it.
	ro := f.as(policy.ScopeProjectsRead)
	require.Equal(t, http.StatusOK, f.callAPI(ro, http.MethodGet, list, nil).Code)
	require.Equal(t, http.StatusForbidden, f.callAPI(ro, http.MethodDelete, "/previews/"+pv.ID.String(), nil).Code)
	require.Equal(t, http.StatusNotFound, f.callAPI(f.ctx, http.MethodDelete, "/previews/"+uuid.NewString(), nil).Code)
	for range 2 {
		rec = f.callAPI(f.ctx, http.MethodDelete, "/previews/"+pv.ID.String(), nil)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		var del previews.PreviewView
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &del))
		require.Equal(t, previews.StatusDeleting, del.Status)
		require.Equal(t, pv.ID, del.ID)
	}

	// Another project's previews are not visible through this project's id.
	require.Equal(t, http.StatusNotFound, f.callAPI(f.ctx, http.MethodGet, "/projects/"+uuid.NewString()+"/previews", nil).Code)
}
