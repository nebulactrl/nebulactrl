package configsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// maxHTTPConfigBody bounds a request body: the file (256 KiB) plus its attachments
// (1 MiB) with room for JSON escaping.
const maxHTTPConfigBody = 2 << 20

// HTTPService is what the HTTP layer needs from *Service.
type HTTPService interface {
	Validate(ctx context.Context, projectID uuid.UUID, in Input, env string) (ValidateResult, error)
	Plan(ctx context.Context, projectID uuid.UUID, r PlanRequest) (Plan, error)
	ApplyGuarded(ctx context.Context, projectID uuid.UUID, r ApplyRequest) (ApplyResult, error)
	Export(ctx context.Context, projectID uuid.UUID, env, base string, all, defaults bool) (ExportResult, error)
	ServiceSource(ctx context.Context, serviceID uuid.UUID) (ServiceConfigSource, error)
	ReleaseFields(ctx context.Context, serviceID uuid.UUID, fieldIDs []string) (ServiceConfigSource, error)
}

// EnvironmentLister lists a project's environments (*environments.Service).
type EnvironmentLister interface {
	List(ctx context.Context, projectID uuid.UUID) ([]environments.Environment, error)
}

var (
	_ HTTPService       = (*Service)(nil)
	_ planApplier       = (*Service)(nil)
	_ EnvironmentLister = (*environments.Service)(nil)
)

// ValidateConfigRequest is the body of a validate call.
type ValidateConfigRequest struct {
	File        string            `json:"file" doc:"The nebula.toml text" maxLength:"262144"`
	Attachments map[string]string `json:"attachments,omitempty" doc:"Files the file refers to (compose_file, files.*.source), keyed by their path relative to the file"`
	Environment string            `json:"environment,omitempty" doc:"Also validate this environment's resolved view and resolve names against the project (slug)" example:"staging"`
}

// PlanGitRef reads the file through the environment's repository binding.
type PlanGitRef struct {
	Ref string `json:"ref,omitempty" doc:"Commit SHA to read the file at; empty reads the head of the bound branch" example:"3f9c2a1d"`
}

// PlanFileSource is where the file of a plan comes from: send the text
// inline, or name a git ref to read it through the environment's binding.
type PlanFileSource struct {
	File        string            `json:"file,omitempty" doc:"The nebula.toml text (inline source)" maxLength:"262144"`
	Attachments map[string]string `json:"attachments,omitempty" doc:"Files the file refers to, keyed by their relative path (inline source)"`
	Git         *PlanGitRef       `json:"git,omitempty" doc:"Read the file from the repository bound to the environment instead (needs projects:write)"`
}

// PlanConfigRequest is the body of a plan call.
type PlanConfigRequest struct {
	Environment string         `json:"environment,omitempty" doc:"Environment slug (or id); may be omitted when the project has exactly one standard environment" example:"production"`
	Source      PlanFileSource `json:"source"`
	Only        []string       `json:"only,omitempty" doc:"Restrict the plan to these file key paths (prefixes), e.g. services.web" example:"services.web"`
	Prune       bool           `json:"prune,omitempty" doc:"Also delete sub-resources the file owned before and no longer lists (variables, domains, volumes, networks, links, tasks, backups, uptime). Never deletes services or databases"`
}

// ApplyConfigRequest is the body of an apply call: a plan request plus the
// stale-plan guard and the deploy switch.
type ApplyConfigRequest struct {
	Environment string         `json:"environment,omitempty" doc:"Environment slug (or id); may be omitted when the project has exactly one standard environment" example:"production"`
	Source      PlanFileSource `json:"source"`
	Only        []string       `json:"only,omitempty" doc:"Restrict the apply to these file key paths (prefixes)"`
	Prune       bool           `json:"prune,omitempty" doc:"Also delete sub-resources the file owned before and no longer lists"`
	PlanID      string         `json:"planId,omitempty" doc:"Plan id from a plan call: the apply fails with 409 plan_stale (and the fresh plan in details.plan) when anything changed since" example:"pl_7d3c1f0a9b2e4c55e6a1d8f0b3c47a92"`
	Deploy      bool           `json:"deploy,omitempty" doc:"Deploy the services this apply touched that now have staged changes (needs deployments:write; not available to principals that need production approval)"`
}

// ReleaseConfigFieldsRequest names the fields a service stops taking from the file.
type ReleaseConfigFieldsRequest struct {
	Paths []string `json:"paths" minItems:"1" maxItems:"500" doc:"Field ids to stop managing (the ids of fields in the service's config source)" example:"config.port"`
}

// CheckConfigSourceRequest reads the bound file now.
type CheckConfigSourceRequest struct {
	SHA string `json:"sha,omitempty" doc:"Commit to read; empty reads the head of the bound branch" example:"3f9c2a1d"`
}

type httpValidateIn struct {
	ProjectID uuid.UUID `path:"projectId"`
	Body      ValidateConfigRequest
}

type httpPlanIn struct {
	ProjectID uuid.UUID `path:"projectId"`
	Body      PlanConfigRequest
}

type httpApplyIn struct {
	ProjectID uuid.UUID `path:"projectId"`
	Body      ApplyConfigRequest
}

type httpExportIn struct {
	ProjectID   uuid.UUID `path:"projectId"`
	Environment string    `query:"environment" doc:"Environment slug, or \"all\" for every standard environment factored into a base plus [environments.<slug>] overrides; may be omitted when the project has exactly one standard environment" example:"production"`
	Base        string    `query:"base" doc:"With environment=all: the environment whose values form the base (default: production, else the first)" example:"production"`
	Defaults    bool      `query:"defaults" doc:"Also write keys whose value equals the default"`
}

type httpServiceSourceIn struct {
	ServiceID uuid.UUID `path:"serviceId"`
}

type httpReleaseIn struct {
	ServiceID uuid.UUID `path:"serviceId"`
	Body      ReleaseConfigFieldsRequest
}

type httpEnvSourceIn struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
}

type httpUpdateSourceIn struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          UpdateSourceInput
}

type httpCheckSourceIn struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          *CheckConfigSourceRequest
}

type httpValidateOut struct{ Body ValidateResult }

type httpPlanOut struct{ Body Plan }

type httpApplyOut struct {
	Status int
	Body   ApplyResult
}

type httpServiceSourceOut struct{ Body ServiceConfigSource }

type httpEnvSourceOut struct{ Body EnvironmentConfigSource }

type httpAPI struct {
	svc  HTTPService
	bind Binding
	envs EnvironmentLister
}

// RegisterHTTP attaches the project file endpoints: validate, plan, apply and
// export of a file, the config source of a service and the repository binding
// of an environment. Authorization happens in the use cases; the scopes
// declared here are the documentation and the MCP catalog's filter.
func RegisterHTTP(h huma.API, svc HTTPService, bind Binding, envs EnvironmentLister) {
	a := &httpAPI{svc: svc, bind: bind, envs: envs}
	tags := []string{"config"}
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, conflict, uc := http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "validateProjectConfig", Method: http.MethodPost, Path: "/projects/{projectId}/config/validate", Tags: tags,
		Summary: "Validate a nebula.toml",
		Description: "Parses the project file strictly (unknown keys, wrong types, limits), resolves it, and checks it statically: kinds, references, bounds, secrets, paths. " +
			"With `environment` it also resolves that environment's view and the names the file uses (servers, git connections, destinations) against the project. " +
			"Invalid input is data, not an HTTP error: the answer is 200 with `valid: false` and diagnostics (code, file line and column, \"did you mean\" suggestions). " +
			"Nothing is written.",
		MaxBodyBytes: maxHTTPConfigBody, Errors: errs(nf, uc),
	}, policy.ScopeProjectsRead), a.validate)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planProjectConfig", Method: http.MethodPost, Path: "/projects/{projectId}/config/plan", Tags: tags,
		Summary: "Plan a nebula.toml against an environment",
		Description: "Computes what applying the file would do to one environment: services to create, update or leave alone, every changed field labelled `staged` (takes effect on the next deploy) or `immediate` (takes effect at once, like the dashboard control), " +
			"drift against the dashboard, services that are not in the file, and the scopes an apply needs. Secret and generated values are masked. The answer is always 200: check `valid` and the diagnostics. " +
			"`planId` identifies the plan; pass it to apply to refuse an apply whose inputs changed meanwhile. The file is inline (`source.file`) or read through the environment's repository binding (`source.git`, needs projects:write). Nothing is written.",
		MaxBodyBytes: maxHTTPConfigBody, Errors: errs(nf, uc),
	}, policy.ScopeProjectsRead), a.plan)

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "applyProjectConfig", Method: http.MethodPost, Path: "/projects/{projectId}/config/apply", Tags: tags,
		Summary: "Apply a nebula.toml to an environment",
		Description: "Re-plans inside one transaction and stages the file exactly like dashboard edits (generation bumps, discardable, deployed with the next deploy), then runs the immediate-kind settings (scale, uptime, tasks, backups, git link, DNS) one by one. " +
			"It never deploys unless `deploy: true`. Without `planId` it plans and applies atomically; with it, a plan that changed since answers 409 `plan_stale` (fresh plan in `details.plan`) and writes nothing. " +
			"A plan with errors answers 422 `config_blocked` (plan in `details.plan`); missing scopes answer 403 `insufficient_scope` before any write. " +
			"Principals that need production approval get `approval_required` for a plan with immediate changes in production. " +
			"Status 201: `staged` or `partial` (read `steps`); 200: `noop` or `failed`. Running an apply again converges.",
		DefaultStatus: http.StatusCreated, MaxBodyBytes: maxHTTPConfigBody, Errors: errs(nf, conflict, uc),
	}), policy.ScopeProjectsWrite), a.apply)

	exportResponses := map[string]*huma.Response{
		"200": {
			Description: "The project as a nebula.toml: plain text by default, or `{ file, warnings }` JSON with `Accept: application/json`. " +
				"A clean export plans to zero changes against the state it was generated from.",
			Content: map[string]*huma.MediaType{
				"text/plain":       {Schema: &huma.Schema{Type: "string"}},
				"application/json": {Schema: h.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[ExportResult](), true, "")},
			},
		},
	}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "exportProjectConfig", Method: http.MethodGet, Path: "/projects/{projectId}/config/export", Tags: tags,
		Summary: "Export a project as a nebula.toml",
		Description: "Writes the live state of one environment, or of all standard environments (`environment=all`, factored into a base plus overrides), as a nebula.toml. " +
			"Keys equal to their default are left out unless `defaults=true`. Secret values are never written: secrets become `secrets.keys` entries. " +
			"The output is deterministic (same state, same bytes). Names that are not unique make the export fail with `ambiguous_reference`.",
		Responses: exportResponses, Errors: errs(nf, uc),
	}, policy.ScopeProjectsRead), a.export)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceConfigSource", Method: http.MethodGet, Path: "/services/{serviceId}/config-source", Tags: tags,
		Summary: "Whether a service is managed by a nebula.toml",
		Description: "Which file last applied to the service, when and by whom, and the drift state of every field the file owns " +
			"(`in_sync`, `file_changed`, `drift`, `conflict`). Computed from stored fingerprints, without reading the file.",
		Errors: errs(nf),
	}, policy.ScopeProjectsRead), a.serviceSource)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "releaseServiceConfigFields", Method: http.MethodPost, Path: "/services/{serviceId}/config-source/release", Tags: tags,
		Summary:     "Stop managing fields of a service with the file",
		Description: "Releases the named fields: the file no longer owns them and later applies leave their live value alone. Changes nothing else.",
		Errors:      errs(nf, uc),
	}, policy.ScopeProjectsWrite), a.release)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getEnvironmentConfigSource", Method: http.MethodGet, Path: "/environments/{environmentId}/config-source", Tags: tags,
		Summary: "Repository binding and status of an environment's nebula.toml",
		Description: "How the environment finds its file (`explicit`, `derived` from the services' repositories, or `none`), whether auto-apply is on, the status of the last check " +
			"(`in_sync`, `differs`, `invalid`, `not_found`, `unreachable`, `error`) and the state of each service.",
		Errors: errs(nf),
	}, policy.ScopeProjectsRead), a.getEnvironmentSource)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateEnvironmentConfigSource", Method: http.MethodPut, Path: "/environments/{environmentId}/config-source", Tags: tags,
		Summary: "Bind an environment to a nebula.toml in a repository",
		Description: "Sets the repository, branch and path the environment's file is read from, and whether a push stages it (`autoApply`). " +
			"`connectionId`, `repo` and `branch` are set together or not at all. Auto-apply needs the admin scope, is refused for principals that need production approval (403) " +
			"and for production environments (422 `production_auto_apply_unsupported`); it stages, it never deploys.",
		Errors: errs(nf, uc),
	}, policy.ScopeProjectsWrite), a.updateEnvironmentSource)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteEnvironmentConfigSource", Method: http.MethodDelete, Path: "/environments/{environmentId}/config-source", Tags: tags,
		Summary:       "Clear the explicit binding of an environment",
		Description:   "Removes the explicit repository binding and switches auto-apply off. Which fields services own stays as it is.",
		DefaultStatus: http.StatusNoContent,
		Errors:        errs(nf),
	}, policy.ScopeProjectsWrite), a.deleteEnvironmentSource)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkEnvironmentConfigSource", Method: http.MethodPost, Path: "/environments/{environmentId}/config-source/check", Tags: tags,
		Summary:     "Read the bound nebula.toml now",
		Description: "Reads the file at the branch head (or at `sha`), validates it, compares it with the environment and stores the status. Nothing is applied.",
		Errors:      errs(nf, uc),
	}, policy.ScopeProjectsRead), a.checkEnvironmentSource)
}

func (a *httpAPI) validate(ctx context.Context, in *httpValidateIn) (*httpValidateOut, error) {
	res, err := a.svc.Validate(ctx, in.ProjectID, Input{
		File: in.Body.File, Attachments: in.Body.Attachments, Source: SourceInfo{Kind: SourceInline},
	}, strings.TrimSpace(in.Body.Environment))
	if err != nil {
		return nil, err
	}
	return &httpValidateOut{Body: res}, nil
}

func (a *httpAPI) plan(ctx context.Context, in *httpPlanIn) (*httpPlanOut, error) {
	req, err := a.planRequest(ctx, in.ProjectID, in.Body)
	if err != nil {
		return nil, err
	}
	p, err := a.svc.Plan(ctx, in.ProjectID, req)
	if err != nil {
		return nil, err
	}
	return &httpPlanOut{Body: p}, nil
}

func (a *httpAPI) apply(ctx context.Context, in *httpApplyIn) (*httpApplyOut, error) {
	b := in.Body
	req, err := a.planRequest(ctx, in.ProjectID, PlanConfigRequest{Environment: b.Environment, Source: b.Source, Only: b.Only, Prune: b.Prune})
	if err != nil {
		return nil, err
	}
	res, err := a.svc.ApplyGuarded(ctx, in.ProjectID, ApplyRequest{PlanRequest: req, PlanID: strings.TrimSpace(b.PlanID), Deploy: b.Deploy})
	if err != nil {
		return nil, err
	}
	return &httpApplyOut{Status: httpApplyStatus(res), Body: res}, nil
}

// httpApplyStatus is 201 when the apply wrote something (staged, partial) and 200
// when it wrote nothing new (noop) or only reports failed steps.
func httpApplyStatus(r ApplyResult) int {
	switch r.Status {
	case StatusStaged, StatusPartial:
		return http.StatusCreated
	}
	return http.StatusOK
}

// planRequest turns the HTTP body into a use-case request, reading the file
// through the environment's binding when the body asks for it.
func (a *httpAPI) planRequest(ctx context.Context, projectID uuid.UUID, b PlanConfigRequest) (PlanRequest, error) {
	env := strings.TrimSpace(b.Environment)
	src := b.Source
	req := PlanRequest{Environment: env, Only: b.Only, Prune: b.Prune}
	switch {
	case src.Git != nil && (src.File != "" || len(src.Attachments) > 0):
		return PlanRequest{}, apierr.Unprocessable("invalid_source", "give either source.file or source.git, not both")
	case src.Git != nil:
		// Reading a repository goes through the workspace's git connection: the
		// same privilege as reading a Compose file from a repository.
		if err := policy.Require(ctx, policy.ScopeProjectsWrite, &projectID); err != nil {
			return PlanRequest{}, err
		}
		e, err := a.environment(ctx, projectID, env)
		if err != nil {
			return PlanRequest{}, err
		}
		info, err := a.bind.Resolve(ctx, e.ID)
		if err != nil {
			return PlanRequest{}, err
		}
		in, err := a.bind.Read(ctx, info, strings.TrimSpace(src.Git.Ref))
		if err != nil {
			return PlanRequest{}, err
		}
		req.Environment, req.Input = e.Slug, in
	case strings.TrimSpace(src.File) == "":
		return PlanRequest{}, apierr.Unprocessable("invalid_source", "give source.file (the nebula.toml text) or source.git")
	default:
		req.Input = Input{File: src.File, Attachments: src.Attachments, Source: SourceInfo{Kind: SourceInline}}
	}
	return req, nil
}

// environment finds the standard environment a request names by slug or id; an
// empty selector is allowed when the project has exactly one.
func (a *httpAPI) environment(ctx context.Context, projectID uuid.UUID, sel string) (environments.Environment, error) {
	all, err := a.envs.List(ctx, projectID)
	if err != nil {
		return environments.Environment{}, err
	}
	var standard []environments.Environment
	for _, e := range all {
		if e.Kind == "standard" {
			standard = append(standard, e)
		}
	}
	if sel == "" {
		if len(standard) == 1 {
			return standard[0], nil
		}
		slugs := make([]string, len(standard))
		for i, e := range standard {
			slugs[i] = e.Slug
		}
		return environments.Environment{}, apierr.Unprocessable("environment_required",
			fmt.Sprintf("environment is required: the project has %d standard environments (%s)", len(standard), strings.Join(slugs, ", ")))
	}
	for _, e := range all {
		if e.Slug == sel || e.ID.String() == sel {
			return e, nil
		}
	}
	return environments.Environment{}, apierr.NotFound("environment")
}

func (a *httpAPI) export(ctx context.Context, in *httpExportIn) (*huma.StreamResponse, error) {
	env, all := strings.TrimSpace(in.Environment), false
	if strings.EqualFold(env, "all") {
		env, all = "", true
	}
	res, err := a.svc.Export(ctx, in.ProjectID, env, strings.TrimSpace(in.Base), all, in.Defaults)
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		// Every header before SetStatus: the status writes them out, so a header set
		// afterwards is lost (the JSON envelope would go out as text/plain).
		hctx.SetHeader("Cache-Control", "no-store")
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		hctx.SetHeader("Vary", "Accept")
		if acceptsJSON(hctx.Header("Accept")) {
			hctx.SetHeader("Content-Type", "application/json")
			hctx.SetStatus(http.StatusOK)
			if res.Warnings == nil {
				res.Warnings = Diagnostics{}
			}
			_ = json.NewEncoder(hctx.BodyWriter()).Encode(res)
			return
		}
		hctx.SetHeader("Content-Type", "text/plain; charset=utf-8")
		hctx.SetHeader("X-Config-Warnings", fmt.Sprint(len(res.Warnings)))
		hctx.SetStatus(http.StatusOK)
		_, _ = hctx.BodyWriter().Write([]byte(res.File))
	}}, nil
}

// acceptsJSON reports whether an Accept header prefers application/json over
// text/plain. The default (no header, */*, ties) is plain text.
func acceptsJSON(accept string) bool {
	jsonQ, textQ := -1.0, -1.0
	wild := -1.0
	for _, part := range strings.Split(accept, ",") {
		mt, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		mt = strings.ToLower(strings.TrimSpace(mt))
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		if q <= 0 { // q=0 means "not acceptable"
			continue
		}
		switch mt {
		case "application/json", "application/*":
			jsonQ = max(jsonQ, q)
		case "text/plain", "text/*":
			textQ = max(textQ, q)
		case "*/*":
			wild = max(wild, q)
		}
	}
	if jsonQ < 0 {
		jsonQ = wild
	}
	if textQ < 0 {
		textQ = wild
	}
	return jsonQ > textQ
}

func (a *httpAPI) serviceSource(ctx context.Context, in *httpServiceSourceIn) (*httpServiceSourceOut, error) {
	s, err := a.svc.ServiceSource(ctx, in.ServiceID)
	if err != nil {
		return nil, err
	}
	return &httpServiceSourceOut{Body: s}, nil
}

func (a *httpAPI) release(ctx context.Context, in *httpReleaseIn) (*httpServiceSourceOut, error) {
	s, err := a.svc.ReleaseFields(ctx, in.ServiceID, in.Body.Paths)
	if err != nil {
		return nil, err
	}
	return &httpServiceSourceOut{Body: s}, nil
}

func (a *httpAPI) getEnvironmentSource(ctx context.Context, in *httpEnvSourceIn) (*httpEnvSourceOut, error) {
	s, err := a.bind.Get(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	return &httpEnvSourceOut{Body: s}, nil
}

func (a *httpAPI) updateEnvironmentSource(ctx context.Context, in *httpUpdateSourceIn) (*httpEnvSourceOut, error) {
	s, err := a.bind.Put(ctx, in.EnvironmentID, in.Body)
	if err != nil {
		return nil, err
	}
	return &httpEnvSourceOut{Body: s}, nil
}

func (a *httpAPI) deleteEnvironmentSource(ctx context.Context, in *httpEnvSourceIn) (*struct{}, error) {
	if err := a.bind.Clear(ctx, in.EnvironmentID); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

func (a *httpAPI) checkEnvironmentSource(ctx context.Context, in *httpCheckSourceIn) (*httpEnvSourceOut, error) {
	sha := ""
	if in.Body != nil {
		sha = strings.TrimSpace(in.Body.SHA)
	}
	s, err := a.bind.Check(ctx, in.EnvironmentID, sha)
	if err != nil {
		return nil, err
	}
	return &httpEnvSourceOut{Body: s}, nil
}
