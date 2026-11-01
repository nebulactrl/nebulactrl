package configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// fieldPlan is the comparison of one field of a service.
type fieldPlan struct {
	fv      projectconfig.FieldValue
	desired fieldView
	live    fieldView
	present bool // present live
	base    string
	hasBase bool
	state   string
	changed bool
	remove  bool // prune: delete this live member
	skipped bool // the desired value could not be computed; a diagnostic says why
}

// svcPlan is the plan of one service of the file.
type svcPlan struct {
	slug    string
	rs      *projectconfig.ResolvedService
	ls      *liveService // nil: the service is created
	create  bool
	fields  []*fieldPlan
	removes []*fieldPlan
	// switchTo is the source type (image or git) a file moves a live service
	// to; empty when the service keeps its source type. A staged change.
	switchTo string
	// released are the ids that stop being owned.
	released []string
	blocked  bool
	server   *db.Server // effective server (nil when unknown)
	cfg      services.Config
	pl       PlanService
}

// sharedPlan is the plan of one shared variable layer.
type sharedPlan struct {
	layer   string // project | environment
	fields  []*fieldPlan
	removes []*fieldPlan
	// released are the ids (project.<KEY> / environment.<KEY>) that stop being owned.
	released []string
}

// planned is a computed plan with everything apply needs.
type planned struct {
	public  Plan
	env     services.EnvScope
	project db.Project
	res     *projectconfig.Resolved
	live    *liveEnv
	pc      *planCtx
	req     PlanRequest
	src     SourceInfo
	fileSHA string
	bytes   int

	svcs   []*svcPlan
	shared []*sharedPlan
	diags  Diagnostics

	scopes     scopeSet
	needsRole  bool // a shared project-layer write needs owner or admin
	privileged bool
	hasWork    bool
}

// diag appends a diagnostic.
func (p *planned) diag(sev, code, path string, pos projectconfig.Pos, format string, a ...any) {
	d := Diagnostic{Severity: sev, Code: code, Path: path, Message: fmt.Sprintf(format, a...)}
	p.diags = append(p.diags, d.WithPos(pos))
}

func (p *planned) errorf(code, path string, pos projectconfig.Pos, format string, a ...any) {
	p.diag(projectconfig.SeverityError, code, path, pos, format, a...)
}

func (p *planned) warnf(code, path string, pos projectconfig.Pos, format string, a ...any) {
	p.diag(projectconfig.SeverityWarning, code, path, pos, format, a...)
}

func (p *planned) infof(code, path string, pos projectconfig.Pos, format string, a ...any) {
	p.diag(projectconfig.SeverityInfo, code, path, pos, format, a...)
}

func toAttachments(m map[string]string) projectconfig.Attachments {
	if len(m) == 0 {
		return nil
	}
	out := make(projectconfig.Attachments, len(m))
	for k, v := range m {
		out[k] = []byte(v)
	}
	return out
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// attachmentsSHA is a stable digest of the attachments.
func attachmentsSHA(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(m[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadProject authorises scope on the project and returns it.
func loadProject(ctx context.Context, q *db.Queries, scope string, projectID uuid.UUID) (db.Project, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Project{}, err
	}
	ws := policy.WorkspaceID(ctx)
	proj, err := q.GetProject(ctx, db.GetProjectParams{ID: projectID, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Project{}, apierr.NotFound("project")
	}
	if err != nil {
		return db.Project{}, fmt.Errorf("get project: %w", err)
	}
	if err := policy.Require(ctx, scope, &proj.ID); err != nil {
		return db.Project{}, err
	}
	if proj.System && policy.Require(ctx, policy.ScopeAdmin, nil) != nil {
		return db.Project{}, apierr.NotFound("project")
	}
	return proj, nil
}

// pickEnvironment resolves an environment slug or id of the project; empty is allowed
// when the project has exactly one standard environment.
func pickEnvironment(ctx context.Context, q *db.Queries, projectID uuid.UUID, ref string) (db.Environment, error) {
	envs, err := q.ListEnvironments(ctx, projectID)
	if err != nil {
		return db.Environment{}, fmt.Errorf("list environments: %w", err)
	}
	slugs := make([]string, 0, len(envs))
	var standard []db.Environment
	for _, e := range envs {
		slugs = append(slugs, e.Slug)
		if e.Kind == "standard" {
			standard = append(standard, e)
		}
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(standard) == 1 {
			return standard[0], nil
		}
		return db.Environment{}, apierr.Unprocessable(CodeEnvironmentRequired, "name the environment: the project has several").
			WithDetails(map[string]any{"environments": slugs})
	}
	id, idErr := uuid.Parse(ref)
	for _, e := range envs {
		if e.Slug == ref || (idErr == nil && e.ID == id) {
			return e, nil
		}
	}
	return db.Environment{}, apierr.Unprocessable(CodeUnknownEnvironment, fmt.Sprintf("the project has no environment %q", ref)).
		WithDetails(map[string]any{"environments": slugs})
}

// parsed is a file parsed and resolved for one environment.
type parsed struct {
	file  *projectconfig.File
	idx   *projectconfig.Index
	res   *projectconfig.Resolved
	diags Diagnostics
}

// parseFor parses the input and resolves it for envSlug ("" resolves the base only).
func parseFor(in Input, envSlug string) parsed {
	f, idx, ds := projectconfig.Parse([]byte(in.File), toAttachments(in.Attachments))
	out := parsed{file: f, idx: idx}
	for _, d := range ds {
		if d.Environment == "" || d.Environment == envSlug {
			out.diags = append(out.diags, d)
		}
	}
	if f == nil {
		return out
	}
	res, more := f.Resolve(envSlug, idx)
	out.res = res
	for _, d := range more {
		if d.Environment == "" || d.Environment == envSlug {
			out.diags = append(out.diags, d)
		}
	}
	return out
}

// Validate parses and statically validates the file; with env set it also resolves that
// environment's view against the project's live names.
func (s *Service) Validate(ctx context.Context, projectID uuid.UUID, in Input, env string) (ValidateResult, error) {
	proj, err := loadProject(ctx, s.q, policy.ScopeProjectsRead, projectID)
	if err != nil {
		return ValidateResult{}, err
	}
	out := ValidateResult{Schema: projectconfig.SchemaVersion, Diagnostics: Diagnostics{}, Environments: []string{}, Services: []ValidateService{}}
	pr := parseFor(in, "")
	if pr.file == nil {
		out.Diagnostics = pr.diags
		return out, nil
	}
	out.Environments = pr.file.Environments()
	if out.Environments == nil {
		out.Environments = []string{}
	}
	all := pr.diags
	if pr.res != nil {
		for _, slug := range pr.res.SortedSlugs() {
			out.Services = append(out.Services, ValidateService{Slug: slug, Kind: pr.res.Services[slug].Kind})
		}
		if pr.res.Project != "" && pr.res.Project != proj.Slug {
			all = append(all, Diagnostic{Severity: projectconfig.SeverityError, Code: "project_mismatch", Path: "project",
				Message: fmt.Sprintf("the file is for project %q, not %q", pr.res.Project, proj.Slug)}.WithPos(pr.res.Pos("project")))
		}
	}
	if env != "" && !all.HasErrors() {
		pl, err := s.Plan(ctx, projectID, PlanRequest{Environment: env, Input: in})
		if err != nil {
			return ValidateResult{}, err
		}
		all = append(all, pl.Diagnostics...)
	}
	out.Diagnostics = all.Dedup()
	out.Valid = !out.Diagnostics.HasErrors()
	return out, nil
}

// Plan computes what applying the file to the environment would do. It writes nothing.
func (s *Service) Plan(ctx context.Context, projectID uuid.UUID, r PlanRequest) (Plan, error) {
	pl, err := s.plan(ctx, s.q, projectID, r, policy.ScopeProjectsRead)
	if err != nil {
		return Plan{}, err
	}
	return pl.public, nil
}

// plan builds the plan with q (the pool's queries, or a transaction's while apply holds the lock).
func (s *Service) plan(ctx context.Context, q *db.Queries, projectID uuid.UUID, r PlanRequest, scope string) (*planned, error) {
	proj, err := loadProject(ctx, q, scope, projectID)
	if err != nil {
		return nil, err
	}
	envRow, err := pickEnvironment(ctx, q, projectID, r.Environment)
	if err != nil {
		return nil, err
	}
	env, err := services.LoadEnvironment(ctx, q, scope, envRow.ID)
	if err != nil {
		return nil, err
	}
	p := &planned{env: env, project: proj, req: r, src: r.Input.Source, scopes: scopeSet{}, bytes: len(r.Input.File), fileSHA: sha256Hex([]byte(r.Input.File))}
	if p.src.Kind == "" {
		p.src.Kind = SourceInline
	}
	pr := parseFor(r.Input, env.Env.Slug)
	p.diags = append(p.diags, pr.diags...)
	p.res = pr.res
	p.public = Plan{
		Schema:      projectconfig.SchemaVersion,
		Project:     PlanProject{ID: proj.ID, Slug: proj.Slug},
		Environment: PlanEnvironment{ID: env.Env.ID, Slug: env.Env.Slug, IsProduction: env.Env.IsProduction},
		Source: PlanSource{Kind: p.src.Kind, Repo: p.src.Repo, Branch: p.src.Branch, Path: p.src.Path, Commit: p.src.Commit,
			FileSHA256: p.fileSHA, Bytes: p.bytes},
		Services:       []PlanService{},
		Shared:         SharedPlan{Project: []PlanChange{}, Environment: []PlanChange{}},
		RequiredScopes: []string{},
		Diagnostics:    Diagnostics{},
		Guard:          Guard{ProductionApproval: ApprovalNotRequired},
	}
	if pr.file != nil && pr.res != nil && pr.res.Project != "" && pr.res.Project != proj.Slug {
		p.errorf("project_mismatch", "project", pr.res.Pos("project"), "the file is for project %q, not %q", pr.res.Project, proj.Slug)
	}
	if p.diags.HasErrors() || pr.res == nil {
		p.finish()
		return p, nil
	}

	live, err := loadLive(ctx, q, s.Box, env)
	if err != nil {
		return nil, err
	}
	p.live = live
	p.pc = &planCtx{ctx: ctx, q: q, box: s.Box, live: live, res: pr.res, env: env, cloneURL: s.CloneURL}
	if err := s.planServices(p); err != nil {
		return nil, err
	}
	p.planShared()
	p.finish()
	return p, nil
}

// onlyMatches reports whether a field is selected by the request's only filter.
func onlyMatches(only []string, path, fieldID string) bool {
	if len(only) == 0 {
		return true
	}
	for _, o := range only {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == fieldID || path == o || strings.HasPrefix(path, o+".") || strings.HasPrefix(path, o+"[") {
			return true
		}
	}
	return false
}
