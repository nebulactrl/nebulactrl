package variables_test

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

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// callAPI drives one request through the registered variable endpoints as the
// principal carried by ctx (the way the auth middleware attaches it).
func callAPI(t *testing.T, ctx context.Context, svc *variables.Service, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	p, err := policy.Authenticated(ctx)
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	variables.Register(humachi.New(r, cfg), svc)

	rd := bytes.NewReader(nil)
	if body != nil {
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

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

// keysOf lists the "key" of every element of the "items" array of a response.
func keysOf(t *testing.T, m map[string]any) []string {
	t.Helper()
	items, _ := m["items"].([]any)
	out := []string{}
	for _, it := range items {
		out = append(out, it.(map[string]any)["key"].(string))
	}
	return out
}

func okStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	require.Equal(t, want, rec.Code, rec.Body.String())
}

func TestHTTPServiceVariableRoutes(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	base := "/services/" + app.ID.String()
	call := func(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
		return callAPI(t, ctx, f.svc, method, base+path, body)
	}

	rec := call(f.ctx, http.MethodPut, "/variables", map[string]any{"variables": []map[string]any{
		{"key": "URL", "value": "http://a"},
		{"key": "TOKEN", "value": "hunter2", "isSecret": true},
	}})
	okStatus(t, rec, http.StatusOK)
	out := decode(t, rec)
	require.Equal(t, []string{"TOKEN", "URL"}, keysOf(t, out))
	require.NotContains(t, rec.Body.String(), "hunter2", "a secret value never leaves the API")
	require.Equal(t, "hunter2", f.value(app.ID, "TOKEN"))

	rec = call(f.ctx, http.MethodGet, "/variables", nil)
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, []string{"TOKEN", "URL"}, keysOf(t, decode(t, rec)))
	require.NotContains(t, rec.Body.String(), "hunter2")

	// Bad input is a 422 with the stable code.
	rec = call(f.ctx, http.MethodPut, "/variables", map[string]any{"variables": []map[string]any{{"key": "1bad", "value": "x"}}})
	okStatus(t, rec, http.StatusUnprocessableEntity)
	require.Equal(t, "invalid_variable_key", decode(t, rec)["code"])

	// A viewer reads but cannot write.
	viewer := f.as(auth.RoleViewer, nil, policy.ScopeProjectsRead)
	okStatus(t, call(viewer, http.MethodGet, "/variables", nil), http.StatusOK)
	okStatus(t, call(viewer, http.MethodPut, "/variables", map[string]any{"variables": []map[string]any{{"key": "X", "value": "1"}}}), http.StatusForbidden)
	okStatus(t, call(viewer, http.MethodDelete, "/variables/URL", nil), http.StatusForbidden)
	require.Equal(t, "http://a", f.value(app.ID, "URL"))

	rec = call(f.ctx, http.MethodPost, "/variables/validate", map[string]any{"key": "X", "value": "${{secret(8)}}"})
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, true, decode(t, rec)["ok"])
	rec = call(f.ctx, http.MethodPost, "/variables/validate", map[string]any{"key": "X", "value": "${{unclosed"})
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, false, decode(t, rec)["ok"])

	// History and restore of a plain variable.
	okStatus(t, call(f.ctx, http.MethodPut, "/variables", map[string]any{"variables": []map[string]any{{"key": "URL", "value": "http://b"}}}), http.StatusOK)
	rec = call(f.ctx, http.MethodGet, "/variables/URL/history", nil)
	okStatus(t, rec, http.StatusOK)
	hist := decode(t, rec)["items"].([]any)
	require.Len(t, hist, 2)
	require.EqualValues(t, 2, hist[0].(map[string]any)["version"], "newest first")
	okStatus(t, call(f.ctx, http.MethodPost, "/variables/URL/history/1/restore", nil), http.StatusOK)
	require.Equal(t, "http://a", f.value(app.ID, "URL"))
	okStatus(t, call(f.ctx, http.MethodPost, "/variables/TOKEN/history/1/restore", nil), http.StatusUnprocessableEntity)

	// Rotation: only generated variables, dry run first.
	okStatus(t, call(f.ctx, http.MethodPut, "/variables", map[string]any{"variables": []map[string]any{{"key": "SESSION", "value": "${{secret(16)}}"}}}), http.StatusOK)
	old := f.value(app.ID, "SESSION")
	rec = call(f.ctx, http.MethodPost, "/variables/SESSION/rotate", map[string]any{"dryRun": true})
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, false, decode(t, rec)["staged"])
	require.Equal(t, old, f.value(app.ID, "SESSION"))
	rec = call(f.ctx, http.MethodPost, "/variables/SESSION/rotate", map[string]any{"dryRun": false})
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, true, decode(t, rec)["staged"])
	require.NotEqual(t, old, f.value(app.ID, "SESSION"))
	okStatus(t, call(f.ctx, http.MethodPost, "/variables/URL/rotate", map[string]any{"dryRun": true}), http.StatusUnprocessableEntity)

	// Suggestions for a service with no repository and no database are empty.
	rec = call(f.ctx, http.MethodGet, "/suggested-variables", nil)
	okStatus(t, rec, http.StatusOK)
	require.Empty(t, decode(t, rec)["items"])

	rec = call(f.ctx, http.MethodDelete, "/variables/URL", nil)
	okStatus(t, rec, http.StatusNoContent)
	for _, v := range f.vars(app.ID) {
		require.NotEqual(t, "URL", v.Key, "the deleted variable is gone")
	}
	okStatus(t, call(f.ctx, http.MethodDelete, "/variables/URL", nil), http.StatusNotFound)

	okStatus(t, callAPI(t, f.ctx, f.svc, http.MethodGet, "/services/00000000-0000-0000-0000-000000000000/variables", nil), http.StatusNotFound)
}

func TestHTTPEnvTemplateRoutes(t *testing.T) {
	f := newTplFx(t, false)
	app := f.addGitSvc(f.env, "app", "https://github.com/acme/shop.git", "", false)
	img := f.addSvc(f.env, "img")

	rec := callAPI(t, f.ctx, f.svc, http.MethodPost, "/services/"+app.ID.String()+"/env-template-scan", nil)
	okStatus(t, rec, http.StatusOK)
	out := decode(t, rec)
	require.Equal(t, "found", out["status"])
	require.Equal(t, "provider", out["method"])
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL", "DEBUG"}, keysOf(t, out))

	rec = callAPI(t, f.ctx, f.svc, http.MethodGet, "/services/"+app.ID.String()+"/suggested-variables", nil)
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL", "DEBUG"}, keysOf(t, decode(t, rec)), "the scan is the suggestion source")

	rec = callAPI(t, f.ctx, f.svc, http.MethodPost, "/services/"+img.ID.String()+"/env-template-scan", nil)
	okStatus(t, rec, http.StatusUnprocessableEntity)
	require.Equal(t, "not_git_source", decode(t, rec)["code"])

	path := "/environments/" + f.env.ID.String() + "/env-template-preview"
	rec = callAPI(t, f.ctx, f.svc, http.MethodPost, path, map[string]any{"gitUrl": "https://github.com/acme/shop"})
	okStatus(t, rec, http.StatusOK)
	require.Equal(t, []string{"SECRET_KEY", "DATABASE_URL", "DEBUG"}, keysOf(t, decode(t, rec)))
	rec = callAPI(t, f.ctx, f.svc, http.MethodPost, path, map[string]any{})
	okStatus(t, rec, http.StatusUnprocessableEntity)
	require.Equal(t, "validation_failed", decode(t, rec)["code"])

	viewer := f.as(auth.RoleViewer, nil, policy.ScopeProjectsRead)
	okStatus(t, callAPI(t, viewer, f.svc, http.MethodPost, path, map[string]any{"gitUrl": "https://github.com/acme/shop"}), http.StatusForbidden)
}

func TestHTTPSharedVariableRoutes(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	call := func(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
		return callAPI(t, ctx, f.svc, method, path, body)
	}
	puts := func(kvs ...string) map[string]any {
		vs := []map[string]any{}
		for i := 0; i < len(kvs); i += 2 {
			vs = append(vs, map[string]any{"key": kvs[i], "value": kvs[i+1]})
		}
		return map[string]any{"variables": vs}
	}
	admin := f.as(auth.RoleAdmin, nil)

	layers := []struct {
		name, base string
		ctx        context.Context
	}{
		{"project", "/projects/" + f.prj.ID.String() + "/shared-variables", f.ctx},
		{"environment", "/environments/" + f.env.ID.String() + "/shared-variables", f.ctx},
		{"team", "/team/shared-variables", admin},
	}
	for _, l := range layers {
		t.Run(l.name, func(t *testing.T) {
			key := "KEY_" + l.name
			rec := call(l.ctx, http.MethodPut, l.base, puts(key, "v1", "GEN_"+l.name, "${{secret(10)}}"))
			okStatus(t, rec, http.StatusOK)
			require.Subset(t, keysOf(t, decode(t, rec)), []string{"GEN_" + l.name, key}, "the written values are listed (an environment also lists inherited project values)")

			rec = call(l.ctx, http.MethodGet, l.base, nil)
			okStatus(t, rec, http.StatusOK)
			require.Contains(t, keysOf(t, decode(t, rec)), key)

			rec = call(l.ctx, http.MethodPost, l.base+"/"+key+"/share", map[string]any{"serviceIds": []string{app.ID.String()}})
			okStatus(t, rec, http.StatusOK)
			ns := "shared"
			if l.name == "team" {
				ns = "team"
			}
			require.Equal(t, "${{"+ns+"."+key+"}}", f.value(app.ID, key))

			rec = call(l.ctx, http.MethodPost, l.base+"/GEN_"+l.name+"/rotate", map[string]any{"dryRun": true})
			okStatus(t, rec, http.StatusOK)
			require.Equal(t, false, decode(t, rec)["staged"])
			rec = call(l.ctx, http.MethodPost, l.base+"/GEN_"+l.name+"/rotate", map[string]any{"dryRun": false})
			okStatus(t, rec, http.StatusOK)
			require.Equal(t, true, decode(t, rec)["staged"])
			okStatus(t, call(l.ctx, http.MethodPost, l.base+"/"+key+"/rotate", map[string]any{"dryRun": true}), http.StatusUnprocessableEntity)

			rec = call(l.ctx, http.MethodGet, l.base+"/"+key+"/history", nil)
			okStatus(t, rec, http.StatusOK)
			require.Len(t, decode(t, rec)["items"], 1)

			rec = call(l.ctx, http.MethodDelete, l.base+"/"+key, nil)
			okStatus(t, rec, http.StatusOK)
			require.NotContains(t, keysOf(t, decode(t, rec)), key)
			okStatus(t, call(l.ctx, http.MethodDelete, l.base+"/"+key, nil), http.StatusNotFound)

			okStatus(t, call(l.ctx, http.MethodPut, l.base, puts("1bad", "x")), http.StatusUnprocessableEntity)
			viewer := f.as(auth.RoleViewer, nil, policy.ScopeProjectsRead)
			okStatus(t, call(viewer, http.MethodPut, l.base, puts("X", "1")), http.StatusForbidden)
		})
	}

	// Team values are closed to members below admin, readable by developers.
	dev := f.as(auth.RoleDeveloper, nil, policy.ScopeProjectsRead, policy.ScopeProjectsWrite)
	okStatus(t, call(dev, http.MethodPut, "/team/shared-variables", puts("T", "1")), http.StatusForbidden)
	okStatus(t, call(dev, http.MethodGet, "/team/shared-variables", nil), http.StatusOK)

	// Who references a shared variable in an environment.
	okStatus(t, call(f.ctx, http.MethodPut, "/projects/"+f.prj.ID.String()+"/shared-variables", puts("REGION", "eu")), http.StatusOK)
	okStatus(t, call(f.ctx, http.MethodPut, "/services/"+app.ID.String()+"/variables", puts("R", "${{shared.REGION}}")), http.StatusOK)
	usage := "/environments/" + f.env.ID.String() + "/variable-usage"
	rec := call(f.ctx, http.MethodGet, usage+"?key=REGION", nil)
	okStatus(t, rec, http.StatusOK)
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, "app", items[0].(map[string]any)["slug"])
	require.Equal(t, "R", items[0].(map[string]any)["key"])
	rec = call(f.ctx, http.MethodGet, usage+"?ns=shared&key=NOPE", nil)
	okStatus(t, rec, http.StatusOK)
	require.Empty(t, decode(t, rec)["items"])
	okStatus(t, call(f.ctx, http.MethodGet, usage, nil), http.StatusUnprocessableEntity)
}
