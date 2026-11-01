package configsync

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
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// httpFakeService records the calls the HTTP layer makes and answers from canned values.
type httpFakeService struct {
	validateIn   Input
	validateEnv  string
	validateRes  ValidateResult
	planReq      PlanRequest
	planRes      Plan
	planErr      error
	applyReq     ApplyRequest
	applyRes     ApplyResult
	applyErr     error
	exportArgs   []any
	exportRes    ExportResult
	sourceID     uuid.UUID
	releasedID   uuid.UUID
	releasedKeys []string
}

func (f *httpFakeService) Validate(_ context.Context, _ uuid.UUID, in Input, env string) (ValidateResult, error) {
	f.validateIn, f.validateEnv = in, env
	return f.validateRes, nil
}

func (f *httpFakeService) Plan(_ context.Context, _ uuid.UUID, r PlanRequest) (Plan, error) {
	f.planReq = r
	return f.planRes, f.planErr
}

func (f *httpFakeService) ApplyGuarded(_ context.Context, _ uuid.UUID, r ApplyRequest) (ApplyResult, error) {
	f.applyReq = r
	return f.applyRes, f.applyErr
}

func (f *httpFakeService) Export(_ context.Context, _ uuid.UUID, env, base string, all, defaults bool) (ExportResult, error) {
	f.exportArgs = []any{env, base, all, defaults}
	return f.exportRes, nil
}

func (f *httpFakeService) ServiceSource(_ context.Context, id uuid.UUID) (ServiceConfigSource, error) {
	f.sourceID = id
	return ServiceConfigSource{Managed: true, State: StateDrift, Fields: []FieldState{{Field: "config.port", State: StateDrift}}, Pending: []string{}}, nil
}

func (f *httpFakeService) ReleaseFields(_ context.Context, id uuid.UUID, keys []string) (ServiceConfigSource, error) {
	f.releasedID, f.releasedKeys = id, keys
	return ServiceConfigSource{Managed: true, State: StateInSync, Fields: []FieldState{}, Pending: []string{}}, nil
}

type httpFakeBinding struct {
	resolved   uuid.UUID
	readRef    string
	readInput  Input
	put        UpdateSourceInput
	putID      uuid.UUID
	cleared    uuid.UUID
	checkedSHA string
	checks     int
}

func (b *httpFakeBinding) Resolve(_ context.Context, id uuid.UUID) (BindingInfo, error) {
	b.resolved = id
	return BindingInfo{EnvironmentID: id, Mode: BindingExplicit, Repo: "acme/shop", Branch: "main"}, nil
}

func (b *httpFakeBinding) Read(_ context.Context, _ BindingInfo, ref string) (Input, error) {
	b.readRef = ref
	return b.readInput, nil
}

func (b *httpFakeBinding) Get(_ context.Context, _ uuid.UUID) (EnvironmentConfigSource, error) {
	return EnvironmentConfigSource{Mode: BindingExplicit, Repo: "acme/shop", Branch: "main", Status: EnvInSync, Services: []EnvironmentConfigService{}}, nil
}

func (b *httpFakeBinding) Put(_ context.Context, id uuid.UUID, in UpdateSourceInput) (EnvironmentConfigSource, error) {
	b.putID, b.put = id, in
	return EnvironmentConfigSource{Mode: BindingExplicit, Status: EnvUnknown, AutoApply: in.AutoApply != nil && *in.AutoApply, Services: []EnvironmentConfigService{}}, nil
}

func (b *httpFakeBinding) Clear(_ context.Context, id uuid.UUID) error {
	b.cleared = id
	return nil
}

func (b *httpFakeBinding) Refresh(uuid.UUID) {}

func (b *httpFakeBinding) Check(_ context.Context, _ uuid.UUID, sha string) (EnvironmentConfigSource, error) {
	b.checks++
	b.checkedSHA = sha
	return EnvironmentConfigSource{Mode: BindingExplicit, Status: EnvDiffers, Services: []EnvironmentConfigService{}}, nil
}

type httpFakeEnvs struct{ list []environments.Environment }

func (e httpFakeEnvs) List(context.Context, uuid.UUID) ([]environments.Environment, error) {
	return e.list, nil
}

type httpHarness struct {
	t      *testing.T
	svc    *httpFakeService
	bind   *httpFakeBinding
	router chi.Router
	prod   environments.Environment
	stage  environments.Environment
	huma   huma.API
}

func newHTTPHarness(t *testing.T, scopes ...string) *httpHarness {
	t.Helper()
	h := &httpHarness{
		t: t, svc: &httpFakeService{}, bind: &httpFakeBinding{},
		prod:  environments.Environment{ID: uuid.New(), Name: "production", Slug: "production", IsProduction: true, Kind: "standard"},
		stage: environments.Environment{ID: uuid.New(), Name: "staging", Slug: "staging", Kind: "standard"},
	}
	preview := environments.Environment{ID: uuid.New(), Name: "pr-7", Slug: "pr-7", Kind: "preview"}
	if len(scopes) == 0 {
		scopes = policy.AllScopes
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{Type: auth.TypeToken, UserID: uuid.New(), WorkspaceID: uuid.New(), Label: "t", Scopes: scopes}
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	h.huma = humachi.New(r, cfg)
	RegisterHTTP(h.huma, h.svc, h.bind, httpFakeEnvs{list: []environments.Environment{h.prod, h.stage, preview}})
	h.router = r
	return h
}

func (h *httpHarness) do(method, path string, body any, header ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		require.NoError(h.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func httpDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

const httpFile = "schema = 1\n\n[services.web]\nimage = \"nginx:1\"\n"

func TestValidateIsDataNotAnError(t *testing.T) {
	h := newHTTPHarness(t)
	h.svc.validateRes = ValidateResult{
		Valid: false, Schema: 1, Environments: []string{"staging"}, Services: []ValidateService{},
		Diagnostics: Diagnostics{{Severity: "error", Code: "unknown_key", Message: "unknown key", Path: "services.web.healthcheck", Line: 4, Column: 1, Suggestions: []string{"health_check"}}},
	}
	pid := uuid.New()
	rec := h.do(http.MethodPost, "/projects/"+pid.String()+"/config/validate",
		map[string]any{"file": httpFile, "attachments": map[string]string{"docker-compose.yml": "services: {}"}, "environment": " staging "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := httpDecode(t, rec)
	require.Equal(t, false, body["valid"])
	diag := body["diagnostics"].([]any)[0].(map[string]any)
	require.Equal(t, "unknown_key", diag["code"])
	require.EqualValues(t, 4, diag["line"])
	require.Equal(t, []any{"health_check"}, diag["suggestions"])

	require.Equal(t, httpFile, h.svc.validateIn.File)
	require.Equal(t, map[string]string{"docker-compose.yml": "services: {}"}, h.svc.validateIn.Attachments)
	require.Equal(t, SourceInline, h.svc.validateIn.Source.Kind)
	require.Equal(t, "staging", h.svc.validateEnv, "the environment is trimmed")

	// A body without a file is a request error, not a validation result.
	rec = h.do(http.MethodPost, "/projects/"+pid.String()+"/config/validate", map[string]any{"environment": "x"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestPlanInlineFileMapsTheRequest(t *testing.T) {
	h := newHTTPHarness(t)
	h.svc.planRes = Plan{PlanID: "pl_abc", Schema: 1, Valid: true, Services: []PlanService{}, Diagnostics: Diagnostics{}, RequiredScopes: []string{"projects:write"}}
	rec := h.do(http.MethodPost, "/projects/"+uuid.NewString()+"/config/plan", map[string]any{
		"environment": "production", "source": map[string]any{"file": httpFile}, "only": []string{"services.web"}, "prune": true,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "pl_abc", httpDecode(t, rec)["planId"])
	require.Equal(t, PlanRequest{
		Environment: "production", Only: []string{"services.web"}, Prune: true,
		Input: Input{File: httpFile, Source: SourceInfo{Kind: SourceInline}},
	}, h.svc.planReq)
}

func TestPlanNeedsExactlyOneSource(t *testing.T) {
	h := newHTTPHarness(t)
	url := "/projects/" + uuid.NewString() + "/config/plan"
	for name, src := range map[string]map[string]any{
		"none":  {},
		"blank": {"file": "  \n"},
		"both":  {"file": httpFile, "git": map[string]any{"ref": "abc"}},
	} {
		rec := h.do(http.MethodPost, url, map[string]any{"environment": "production", "source": src})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
		require.Equal(t, "invalid_source", httpDecode(t, rec)["code"], name)
	}
}

func TestPlanFromGitReadsThroughTheBinding(t *testing.T) {
	h := newHTTPHarness(t)
	h.bind.readInput = Input{File: httpFile, Source: SourceInfo{Kind: SourceGit, Repo: "acme/shop", Branch: "main", Commit: "3f9c2a1d", Path: "nebula.toml"}}
	h.svc.planRes = Plan{PlanID: "pl_git", Valid: true, Services: []PlanService{}, Diagnostics: Diagnostics{}}
	url := "/projects/" + uuid.NewString() + "/config/plan"

	rec := h.do(http.MethodPost, url, map[string]any{"environment": "staging", "source": map[string]any{"git": map[string]any{"ref": " 3f9c2a1d "}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, h.stage.ID, h.bind.resolved, "the environment is resolved by slug inside the project")
	require.Equal(t, "3f9c2a1d", h.bind.readRef)
	require.Equal(t, "staging", h.svc.planReq.Environment)
	require.Equal(t, SourceGit, h.svc.planReq.Input.Source.Kind)
	require.Equal(t, "3f9c2a1d", h.svc.planReq.Input.Source.Commit)

	// The environment may also be an id.
	rec = h.do(http.MethodPost, url, map[string]any{"environment": h.prod.ID.String(), "source": map[string]any{"git": map[string]any{}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, h.prod.ID, h.bind.resolved)
	require.Equal(t, "", h.bind.readRef, "no ref reads the branch head")
	require.Equal(t, "production", h.svc.planReq.Environment)

	// Several standard environments and none named: previews do not count, so it is the project's call.
	rec = h.do(http.MethodPost, url, map[string]any{"source": map[string]any{"git": map[string]any{}}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := httpDecode(t, rec)
	require.Equal(t, "environment_required", body["code"])
	require.Contains(t, body["detail"], "production, staging")

	rec = h.do(http.MethodPost, url, map[string]any{"environment": "nope", "source": map[string]any{"git": map[string]any{}}})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPlanFromGitNeedsWriteAccess(t *testing.T) {
	h := newHTTPHarness(t, policy.ScopeProjectsRead)
	rec := h.do(http.MethodPost, "/projects/"+uuid.NewString()+"/config/plan",
		map[string]any{"environment": "staging", "source": map[string]any{"git": map[string]any{}}})
	require.Equal(t, http.StatusForbidden, rec.Code, "reading a repository through a connection is more than reading a plan")
	require.Zero(t, h.bind.resolved)

	// An inline plan needs nothing the use case does not check itself.
	h.svc.planRes = Plan{PlanID: "pl_x", Services: []PlanService{}, Diagnostics: Diagnostics{}}
	rec = h.do(http.MethodPost, "/projects/"+uuid.NewString()+"/config/plan",
		map[string]any{"environment": "staging", "source": map[string]any{"file": httpFile}})
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestApplyStatusAndRequest(t *testing.T) {
	h := newHTTPHarness(t)
	url := "/projects/" + uuid.NewString() + "/config/apply"
	body := map[string]any{
		"environment": "production", "source": map[string]any{"file": httpFile}, "planId": " pl_abc ", "deploy": true, "prune": true, "only": []string{"services.web"},
	}
	svcID := uuid.New()
	for status, want := range map[string]int{StatusStaged: 201, StatusPartial: 201, StatusNoop: 200, StatusFailed: 200} {
		h.svc.applyRes = ApplyResult{
			PlanID: "pl_abc", Status: status, Steps: []ApplyStep{{Step: "config:web", Status: StepApplied}},
			Services: []AppliedService{{Slug: "web", ServiceID: svcID, Action: ActionUpdate, Generation: 4}}, Deployments: []uuid.UUID{},
			Plan: Plan{PlanID: "pl_abc", Services: []PlanService{}, Diagnostics: Diagnostics{}},
		}
		rec := h.do(http.MethodPost, url, body)
		require.Equal(t, want, rec.Code, "%s: %s", status, rec.Body.String())
		got := httpDecode(t, rec)
		require.Equal(t, status, got["status"])
		require.Equal(t, svcID.String(), got["services"].([]any)[0].(map[string]any)["serviceId"])
	}
	require.Equal(t, ApplyRequest{
		PlanRequest: PlanRequest{Environment: "production", Only: []string{"services.web"}, Prune: true, Input: Input{File: httpFile, Source: SourceInfo{Kind: SourceInline}}},
		PlanID:      "pl_abc", Deploy: true,
	}, h.svc.applyReq, "planId is trimmed; deploy and prune reach the use case, which decides who may")
}

func TestApplyErrorBodies(t *testing.T) {
	h := newHTTPHarness(t)
	url := "/projects/" + uuid.NewString() + "/config/apply"
	body := map[string]any{"environment": "production", "source": map[string]any{"file": httpFile}}
	plan := Plan{PlanID: "pl_zzz", Valid: false, Services: []PlanService{}, Diagnostics: Diagnostics{{Severity: "error", Code: "unknown_key", Message: "m"}}}

	h.svc.applyErr = ErrConfigBlocked(plan)
	rec := h.do(http.MethodPost, url, body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	got := httpDecode(t, rec)
	require.Equal(t, "config_blocked", got["code"])
	require.Equal(t, "pl_zzz", got["details"].(map[string]any)["plan"].(map[string]any)["planId"])

	h.svc.applyErr = ErrPlanStale(plan)
	rec = h.do(http.MethodPost, url, body)
	require.Equal(t, http.StatusConflict, rec.Code)
	got = httpDecode(t, rec)
	require.Equal(t, "plan_stale", got["code"])
	require.Equal(t, "pl_zzz", got["details"].(map[string]any)["plan"].(map[string]any)["planId"], "the fresh plan rides along")

	h.svc.applyErr = ErrInsufficientScope([]string{"uptime", "admin"})
	rec = h.do(http.MethodPost, url, body)
	require.Equal(t, http.StatusForbidden, rec.Code)
	got = httpDecode(t, rec)
	require.Equal(t, "insufficient_scope", got["code"])
	require.Equal(t, []any{"admin", "uptime"}, got["details"].(map[string]any)["scopes"])
}

func TestExportNegotiatesTextAndJSON(t *testing.T) {
	h := newHTTPHarness(t)
	h.svc.exportRes = ExportResult{File: "schema = 1\n", Warnings: Diagnostics{{Severity: "warning", Code: "secret_flag_differs", Message: "m"}}}
	pid := uuid.NewString()

	rec := h.do(http.MethodGet, "/projects/"+pid+"/config/export?environment=production&defaults=true", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "schema = 1\n", rec.Body.String(), "plain text is the file and nothing else")
	require.Equal(t, "1", rec.Header().Get("X-Config-Warnings"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, []any{"production", "", false, true}, h.svc.exportArgs)

	rec = h.do(http.MethodGet, "/projects/"+pid+"/config/export?environment=all&base=staging", nil, "Accept", "application/json")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	got := httpDecode(t, rec)
	require.Equal(t, "schema = 1\n", got["file"])
	require.Len(t, got["warnings"], 1)
	require.Equal(t, []any{"", "staging", true, false}, h.svc.exportArgs, "environment=all asks for every environment, factored on the base")

	// No warnings is an empty list, never null.
	h.svc.exportRes = ExportResult{File: "x"}
	rec = h.do(http.MethodGet, "/projects/"+pid+"/config/export", nil, "Accept", "application/json")
	require.Equal(t, []any{}, httpDecode(t, rec)["warnings"])
}

func TestAcceptsJSON(t *testing.T) {
	for _, c := range []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"*/*", false},
		{"text/plain", false},
		{"application/json", true},
		{"application/json, text/plain", false}, // a tie goes to the default
		{"application/json;q=0.9, text/*", false},
		{"text/plain;q=0.5, application/json", true},
		{"APPLICATION/JSON", true},
		{"application/*", true},
		{"text/html", false},
		{"application/json;q=0", false},
	} {
		require.Equal(t, c.want, acceptsJSON(c.accept), "Accept: %q", c.accept)
	}
}

func TestServiceConfigSourceEndpoints(t *testing.T) {
	h := newHTTPHarness(t)
	id := uuid.New()
	rec := h.do(http.MethodGet, "/services/"+id.String()+"/config-source", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, id, h.svc.sourceID)
	got := httpDecode(t, rec)
	require.Equal(t, "drift", got["state"])
	require.Equal(t, []any{}, got["pending"])

	rec = h.do(http.MethodPost, "/services/"+id.String()+"/config-source/release", map[string]any{"paths": []string{"config.port", "variables.NODE_ENV"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, id, h.svc.releasedID)
	require.Equal(t, []string{"config.port", "variables.NODE_ENV"}, h.svc.releasedKeys)

	for name, b := range map[string]any{"empty": map[string]any{"paths": []string{}}, "missing": map[string]any{}} {
		rec = h.do(http.MethodPost, "/services/"+id.String()+"/config-source/release", b)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
	}
}

func TestEnvironmentConfigSourceEndpoints(t *testing.T) {
	h := newHTTPHarness(t)
	env := uuid.New()
	base := "/environments/" + env.String() + "/config-source"

	rec := h.do(http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "in_sync", httpDecode(t, rec)["status"])

	conn := uuid.New()
	rec = h.do(http.MethodPut, base, map[string]any{"connectionId": conn.String(), "repo": "acme/shop", "branch": "develop", "path": "ops/nebula.toml", "autoApply": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, env, h.bind.putID)
	require.Equal(t, &conn, h.bind.put.ConnectionID)
	require.Equal(t, "acme/shop", *h.bind.put.Repo)
	require.Equal(t, "develop", *h.bind.put.Branch)
	require.Equal(t, "ops/nebula.toml", *h.bind.put.Path)
	require.True(t, *h.bind.put.AutoApply)
	require.Equal(t, true, httpDecode(t, rec)["autoApply"])

	// Absent keys stay nil: the binding use case treats them as "unchanged".
	h.do(http.MethodPut, base, map[string]any{"autoApply": false})
	require.Nil(t, h.bind.put.Repo)
	require.Nil(t, h.bind.put.ConnectionID)
	require.False(t, *h.bind.put.AutoApply)

	rec = h.do(http.MethodDelete, base, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, env, h.bind.cleared)

	rec = h.do(http.MethodPost, base+"/check", map[string]any{"sha": " abc123 "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "abc123", h.bind.checkedSHA)
	require.Equal(t, "differs", httpDecode(t, rec)["status"])

	rec = h.do(http.MethodPost, base+"/check", nil)
	require.Equal(t, http.StatusOK, rec.Code, "the body of a check is optional: "+rec.Body.String())
	require.Equal(t, "", h.bind.checkedSHA, "no body reads the branch head")
	require.Equal(t, 2, h.bind.checks)
}

func TestOperationsDeclareTheirContract(t *testing.T) {
	h := newHTTPHarness(t)
	type want struct {
		method, path string
		scopes       []string
		idempotent   bool
	}
	ops := map[string]want{
		"validateProjectConfig":         {"POST", "/projects/{projectId}/config/validate", []string{policy.ScopeProjectsRead}, false},
		"planProjectConfig":             {"POST", "/projects/{projectId}/config/plan", []string{policy.ScopeProjectsRead}, false},
		"applyProjectConfig":            {"POST", "/projects/{projectId}/config/apply", []string{policy.ScopeProjectsWrite}, true},
		"exportProjectConfig":           {"GET", "/projects/{projectId}/config/export", []string{policy.ScopeProjectsRead}, false},
		"getServiceConfigSource":        {"GET", "/services/{serviceId}/config-source", []string{policy.ScopeProjectsRead}, false},
		"releaseServiceConfigFields":    {"POST", "/services/{serviceId}/config-source/release", []string{policy.ScopeProjectsWrite}, false},
		"getEnvironmentConfigSource":    {"GET", "/environments/{environmentId}/config-source", []string{policy.ScopeProjectsRead}, false},
		"updateEnvironmentConfigSource": {"PUT", "/environments/{environmentId}/config-source", []string{policy.ScopeProjectsWrite}, false},
		"deleteEnvironmentConfigSource": {"DELETE", "/environments/{environmentId}/config-source", []string{policy.ScopeProjectsWrite}, false},
		"checkEnvironmentConfigSource":  {"POST", "/environments/{environmentId}/config-source/check", []string{policy.ScopeProjectsRead}, false},
	}
	seen := map[string]bool{}
	for path, item := range h.huma.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{"GET": item.Get, "POST": item.Post, "PUT": item.Put, "DELETE": item.Delete} {
			if op == nil {
				continue
			}
			w, ok := ops[op.OperationID]
			require.True(t, ok, "unexpected operation %s", op.OperationID)
			seen[op.OperationID] = true
			require.Equal(t, w.method, method, op.OperationID)
			require.Equal(t, w.path, path, op.OperationID)
			require.Equal(t, w.scopes, api.ScopesOf(op), op.OperationID)
			require.Equal(t, w.idempotent, api.IsIdempotent(op), op.OperationID)
			require.Equal(t, []string{"config"}, op.Tags, op.OperationID)
			require.NotEmpty(t, op.Summary, op.OperationID)
			require.GreaterOrEqual(t, len(op.Description), 60, op.OperationID)
		}
	}
	require.Len(t, seen, len(ops))

	exp := h.huma.OpenAPI().Paths["/projects/{projectId}/config/export"].Get
	require.Contains(t, exp.Responses["200"].Content, "text/plain")
	require.Contains(t, exp.Responses["200"].Content, "application/json")
	for _, p := range exp.Parameters {
		require.NotEqual(t, "Accept", p.Name, "content negotiation stays out of the generated clients")
	}
}

// The schema names of the config types must not collide with the older Plan and
// PlanSource schemas of other domains (huma panics on a duplicate name).
func TestSchemaNamesDoNotCollideWithOtherDomains(t *testing.T) {
	h := newHTTPHarness(t)
	type out struct{ Body deployments.Plan }
	require.NotPanics(t, func() {
		huma.Register(h.huma, huma.Operation{OperationID: "planDeploymentForTest", Method: http.MethodGet, Path: "/dp"},
			func(context.Context, *struct{}) (*out, error) { return &out{}, nil })
	})
	schemas := h.huma.OpenAPI().Components.Schemas.Map()
	for _, name := range []string{"ConfigPlan", "ConfigPlanSource", "ConfigPlanChange", "ConfigPlanService", "ConfigApplyResult", "ConfigValidateResult", "ConfigDiagnostic", "ConfigExportResult", "Plan", "PlanSource"} {
		require.Contains(t, schemas, name)
	}
	for _, name := range []string{"ServiceConfigSource", "EnvironmentConfigSource"} {
		require.Contains(t, schemas, name, "names that already say Config keep them")
	}
}
