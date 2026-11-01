package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
)

const (
	configFileOK  = "schema = 1\n\n[services.web]\nimage = \"nginx:1.27\"\nport = 8080\n"
	configFileBad = "schema = 1\n\n[services.web]\nimage = \"nginx:1.27\"\nhealthcheck = 1\n"
)

func TestOpenAPIHasTheProjectConfigOperations(t *testing.T) {
	a, err := app.Build(&config.Config{}, nil, nil)
	require.NoError(t, err)
	defer a.Close()
	raw, err := json.Marshal(a.API.OpenAPI())
	require.NoError(t, err)
	var doc struct {
		Paths      map[string]map[string]struct{ OperationID string } `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	for path, ops := range map[string]map[string]string{
		"/projects/{projectId}/config/validate":             {"post": "validateProjectConfig"},
		"/projects/{projectId}/config/plan":                 {"post": "planProjectConfig"},
		"/projects/{projectId}/config/apply":                {"post": "applyProjectConfig"},
		"/projects/{projectId}/config/export":               {"get": "exportProjectConfig"},
		"/services/{serviceId}/config-source":               {"get": "getServiceConfigSource"},
		"/services/{serviceId}/config-source/release":       {"post": "releaseServiceConfigFields"},
		"/environments/{environmentId}/config-source":       {"get": "getEnvironmentConfigSource", "put": "updateEnvironmentConfigSource", "delete": "deleteEnvironmentConfigSource"},
		"/environments/{environmentId}/config-source/check": {"post": "checkEnvironmentConfigSource"},
	} {
		require.Contains(t, doc.Paths, path)
		for method, id := range ops {
			require.Equal(t, id, doc.Paths[path][method].OperationID, "%s %s", method, path)
		}
	}
	// The older Plan / PlanSource schemas of deployments live next to the config ones.
	for _, name := range []string{"Plan", "PlanSource", "ConfigPlan", "ConfigPlanSource", "ConfigApplyResult", "ConfigDiagnostic", "ServiceConfigSource", "EnvironmentConfigSource"} {
		require.Contains(t, doc.Components.Schemas, name)
	}
}

func TestSchemaEndpointIsPublicAndOriginSubstituted(t *testing.T) {
	h := newHarnessCfg(t, func(c *config.Config) { c.PublicURL = "https://nebula.example.com" })
	get := func(hdr ...string) (*http.Response, []byte) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.srv.URL+"/schema/nebula.toml.json", nil)
		require.NoError(t, err)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := (&http.Client{}).Do(req) // no cookie, no token
		require.NoError(t, err)
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		return res, b
	}

	res, body := get()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/schema+json", res.Header.Get("Content-Type"))
	require.Equal(t, "public, max-age=300", res.Header.Get("Cache-Control"))
	etag := res.Header.Get("ETag")
	require.NotEmpty(t, etag)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	require.Equal(t, "https://nebula.example.com/schema/nebula.toml.json", doc["$id"])
	require.NotContains(t, string(body), "nebulactrl.invalid")

	res, body = get("If-None-Match", etag)
	require.Equal(t, http.StatusNotModified, res.StatusCode)
	require.Empty(t, body)

	// It is a real route, not the single-page app's fallback.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, h.srv.URL+"/schema/nebula.toml.json", strings.NewReader("{}"))
	res2, err := (&http.Client{}).Do(req)
	require.NoError(t, err)
	res2.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, res2.StatusCode)
}

func TestProjectConfigValidateAndScopesOverHTTP(t *testing.T) {
	h := newHarness(t)
	h.setup()
	id := h.createProject("Shop")
	base := "/projects/" + id + "/config"

	read := h.createToken([]string{"projects:read"}, nil)
	noScope := h.createToken([]string{"servers:read"}, nil)

	// A valid file.
	r := h.do("POST", base+"/validate", read, map[string]any{"file": configFileOK})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	got := r.JSON()
	require.Equal(t, true, got["valid"], string(r.Raw))
	require.Equal(t, []any{map[string]any{"slug": "web", "kind": "image"}}, got["services"])

	// An invalid one is data: 200, with the place of the problem and what to write instead.
	r = h.do("POST", base+"/validate", read, map[string]any{"file": configFileBad})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	got = r.JSON()
	require.Equal(t, false, got["valid"])
	var found map[string]any
	for _, d := range got["diagnostics"].([]any) {
		if d.(map[string]any)["code"] == "unknown_key" {
			found = d.(map[string]any)
		}
	}
	require.NotNil(t, found, string(r.Raw))
	require.Equal(t, "services.web.healthcheck", found["path"])
	require.EqualValues(t, 5, found["line"])
	require.Contains(t, found["suggestions"], "health_check")

	// Without the scope, or without a login, nothing is answered.
	require.Equal(t, http.StatusForbidden, h.do("POST", base+"/validate", noScope, map[string]any{"file": configFileOK}).Status)
	res, err := http.Post(h.srv.URL+"/api/v1"+base+"/validate", "application/json", strings.NewReader(`{"file":"x"}`))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusUnauthorized, res.StatusCode)

	// A body without the file is a request error.
	require.Equal(t, http.StatusUnprocessableEntity, h.do("POST", base+"/validate", read, map[string]any{}).Status)
	// Another project's id is not found, never a hint.
	require.Equal(t, http.StatusNotFound, h.do("POST", "/projects/00000000-0000-4000-8000-000000000001/config/validate", read, map[string]any{"file": configFileOK}).Status)
}

func TestProjectConfigApplyNeedsWriteAndRefusesAnInvalidFile(t *testing.T) {
	h := newHarness(t)
	h.setup()
	id := h.createProject("Shop")
	base := "/projects/" + id + "/config"
	body := func(file string) map[string]any {
		return map[string]any{"environment": "production", "source": map[string]any{"file": file}}
	}

	read := h.createToken([]string{"projects:read"}, nil)
	write := h.createToken([]string{"projects:read", "projects:write"}, nil)

	r := h.do("POST", base+"/plan", read, body(configFileBad))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, false, r.JSON()["valid"], "a plan of an invalid file is a result")

	r = h.do("POST", base+"/apply", read, body(configFileOK))
	require.Equal(t, http.StatusForbidden, r.Status, "reading is not enough to apply: %s", string(r.Raw))

	r = h.do("POST", base+"/apply", write, body(configFileBad))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "config_blocked", r.JSON()["code"])
	plan := r.JSON()["details"].(map[string]any)["plan"].(map[string]any)
	require.Equal(t, false, plan["valid"])

	// The refused apply wrote nothing: the project still has no service.
	envs := h.do("GET", "/projects/"+id+"/environments", "", nil).JSON()["items"].([]any)
	envID := envs[0].(map[string]any)["id"].(string)
	svcs := h.do("GET", "/environments/"+envID+"/services", "", nil)
	require.Equal(t, http.StatusOK, svcs.Status, string(svcs.Raw))
	require.Empty(t, svcs.JSON()["items"])

	// The repository binding of a fresh environment: none, and unknown ids are 404.
	src := h.do("GET", "/environments/"+envID+"/config-source", read, nil)
	require.Equal(t, http.StatusOK, src.Status, string(src.Raw))
	require.Equal(t, "none", src.JSON()["mode"])
	require.Equal(t, false, src.JSON()["autoApply"])
	require.Equal(t, http.StatusNotFound, h.do("GET", "/environments/00000000-0000-4000-8000-000000000001/config-source", read, nil).Status)
}

func TestProjectConfigExportIsTextOrJSON(t *testing.T) {
	h := newHarnessCfg(t, func(c *config.Config) { c.PublicURL = "https://nebula.example.com" })
	h.setup()
	id := h.createProject("Shop")
	read := h.createToken([]string{"projects:read"}, nil)
	url := "/projects/" + id + "/config/export?environment=production"

	r := h.do("GET", url, read, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain"), r.Header.Get("Content-Type"))
	text := string(r.Raw)
	require.True(t, strings.HasPrefix(text, "#:schema https://nebula.example.com/schema/nebula.toml.json\n"), text)
	require.Contains(t, text, "schema = 1")

	r = h.do("GET", url, read, nil, "Accept", "application/json")
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, text, r.JSON()["file"], "the same file either way")
	require.NotNil(t, r.JSON()["warnings"])

	require.Equal(t, http.StatusForbidden, h.do("GET", url, h.createToken([]string{"servers:read"}, nil), nil).Status)
}
