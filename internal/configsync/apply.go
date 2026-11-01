package configsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// viaFile is the audit marker of every change an apply makes.
const viaFile = "nebula.toml"

// evConfigUpdated is the live-update event of an environment whose config state changed.
const evConfigUpdated = "config.updated"

// applier runs phase 1 (one transaction) of an apply and collects what phase 2 needs.
type applier struct {
	s   *Service
	ctx context.Context
	tx  pgx.Tx
	q   *db.Queries
	p   *planned
	out *ApplyResult

	prin  *auth.Principal
	after []func(context.Context)

	// rows maps a slug to its row after phase 1 (created or locked); the plan's live rows go stale.
	rows map[string]db.Service
	// scopes caches the scope of each service of the environment.
	scopes map[string]services.Scope
	// owned is the ownership record of each service after phase 1, by service id.
	owned   map[uuid.UUID]map[string]string
	created map[string]bool
	changed map[string]bool // services with at least one change written in phase 1

	imm []*immStep
	// wrote is set when ownership rows were written (an apply with no change may still adopt).
	wrote bool
	// sharedOwned is the owned_shared record after phase 1.
	sharedOwned map[string]string
}

func (a *applier) step(name, status, msg string) {
	a.out.Steps = append(a.out.Steps, ApplyStep{Step: name, Status: status, Message: msg})
}

func (a *applier) later(f func(context.Context)) {
	if f != nil {
		a.after = append(a.after, f)
	}
}

// originOf derives the origin of an apply from the request and the caller.
func originOf(src SourceInfo, p *auth.Principal) string {
	switch src.Origin {
	case OriginGit, OriginCLI, OriginAPI, OriginMCP, OriginUI:
		return src.Origin
	}
	if p == nil {
		return OriginAPI
	}
	switch p.Type {
	case auth.TypeSystem:
		return OriginGit
	case auth.TypeMCP:
		return OriginMCP
	case auth.TypeUser:
		return OriginUI
	}
	return OriginAPI
}

// actorOf is the actor columns of config_sources.
func actorOf(p *auth.Principal) (typ, id, label string) {
	if p == nil {
		return "system", "", "system"
	}
	switch p.Type {
	case auth.TypeUser:
		return "user", p.ID().String(), p.Label
	case auth.TypeMCP:
		return "mcp", p.ID().String(), p.Label
	case auth.TypeSystem:
		return "system", "", p.Label
	}
	return "token", p.ID().String(), p.Label
}

// Apply stages the file in the environment and runs its immediate-kind settings.
func (s *Service) Apply(ctx context.Context, projectID uuid.UUID, r ApplyRequest) (ApplyResult, error) {
	ctx = audit.WithVia(ctx, viaFile)
	prin, err := policy.Authenticated(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if _, err := loadProject(ctx, s.q, policy.ScopeProjectsWrite, projectID); err != nil {
		return ApplyResult{}, err
	}
	if prin.Type == auth.TypeMCP {
		if r.Prune {
			return ApplyResult{}, apierr.Unprocessable(CodePruneNotAllowed, "agents cannot prune")
		}
		if r.Deploy {
			return ApplyResult{}, apierr.Unprocessable(CodeDeployNotAllowed, "agents cannot deploy from a config apply; use apply_changes")
		}
	}

	// Phase 0: plan, scopes and the stale-plan check before anything is written.
	pre, err := s.plan(ctx, s.q, projectID, r.PlanRequest, policy.ScopeProjectsWrite)
	if err != nil {
		return ApplyResult{}, err
	}
	if !pre.public.Valid {
		return ApplyResult{}, ErrConfigBlocked(pre.public)
	}
	if err := s.authorizePlan(ctx, prin, pre, r.Deploy); err != nil {
		return ApplyResult{}, err
	}
	if r.PlanID != "" && r.PlanID != pre.public.PlanID {
		return ApplyResult{}, ErrPlanStale(pre.public)
	}

	// Phase 1: one transaction.
	var (
		a      *applier
		result ApplyResult
	)
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.LockConfigApply(ctx, pre.env.Env.ID.String()); err != nil {
			return fmt.Errorf("lock environment: %w", err)
		}
		if _, err := q.LockEnvironmentServices(ctx, pre.env.Env.ID); err != nil {
			return fmt.Errorf("lock services: %w", err)
		}
		p, err := s.plan(ctx, q, projectID, r.PlanRequest, policy.ScopeProjectsWrite)
		if err != nil {
			return err
		}
		if !p.public.Valid {
			return ErrConfigBlocked(p.public)
		}
		if r.PlanID != "" && r.PlanID != p.public.PlanID {
			return ErrPlanStale(p.public)
		}
		a = &applier{s: s, ctx: ctx, tx: tx, q: q, p: p, prin: prin,
			out:  &ApplyResult{PlanID: p.public.PlanID, Steps: []ApplyStep{}, Services: []AppliedService{}, Deployments: []uuid.UUID{}},
			rows: map[string]db.Service{}, scopes: map[string]services.Scope{}, owned: map[uuid.UUID]map[string]string{},
			created: map[string]bool{}, changed: map[string]bool{}}
		if err := a.phase1(r); err != nil {
			return err
		}
		result = *a.out
		return nil
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ApplyResult{}, err
		}
		return ApplyResult{}, fmt.Errorf("apply nebula.toml: %w", err)
	}
	for _, f := range a.after {
		f(ctx)
	}
	result = *a.out

	// Phase 2: immediate settings, each independent.
	a.runImmediate(ctx, &result)

	// Phase 3: deploy what this apply touched.
	if r.Deploy {
		a.deploy(ctx, &result)
	}

	// Status.
	result.Status = a.status(&result)
	result.Plan = a.p.public

	// Phase 4: events.
	a.publish()
	return result, nil
}

// authorizePlan checks the scopes the plan needs (and deploy) against the caller before any write.
func (s *Service) authorizePlan(ctx context.Context, prin *auth.Principal, p *planned, deploy bool) error {
	required := append([]string(nil), p.public.RequiredScopes...)
	if deploy {
		required = append(required, policy.ScopeDeploymentsWrite)
	}
	if missing := missingScopes(prin, required); len(missing) > 0 {
		return ErrInsufficientScope(missing)
	}
	// A project restriction of the token applies to the project the plan is about.
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, &p.project.ID); err != nil {
		return err
	}
	if p.needsRole {
		if err := policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin); err != nil {
			return ErrInsufficientScope([]string{"role:owner-or-admin"})
		}
	}
	return nil
}

// status summarises the steps: failed steps make the apply partial, nothing written is noop.
func (a *applier) status(res *ApplyResult) string {
	failed, applied := false, false
	for _, st := range res.Steps {
		if strings.HasPrefix(st.Step, "ownership:") && st.Status == StepApplied {
			continue // adopting a field is not a change
		}
		switch st.Status {
		case StepFailed:
			failed = true
		case StepApplied:
			applied = true
		}
	}
	switch {
	case failed && applied:
		return StatusPartial
	case failed:
		return StatusFailed
	case !applied && !a.anyWrite():
		return StatusNoop
	}
	return StatusStaged
}

func (a *applier) anyWrite() bool {
	return a.p.hasWork || len(a.created) > 0
}

// phase1 writes every staged change in the caller's transaction.
func (a *applier) phase1(r ApplyRequest) error {
	p := a.p
	gen0 := map[uuid.UUID]int64{}
	for _, ls := range p.live.Services {
		gen0[ls.Row.ID] = ls.Row.Generation
	}
	// The shared layers go first so references to them resolve while the services are written.
	if err := a.applyShared(); err != nil {
		return err
	}
	for _, sp := range p.svcs {
		if sp.pl.Action == ActionBlocked {
			continue
		}
		if err := a.applyService(sp); err != nil {
			return fmt.Errorf("service %s: %w", sp.slug, err)
		}
	}
	if err := a.collapseGenerations(gen0); err != nil {
		return err
	}
	if err := a.recordOwnership(); err != nil {
		return err
	}
	return a.auditApply(r)
}

// stepError carries the step a failure belongs to, so an error reads "variables:web: ...".
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func (a *applier) fail(step string, err error) error {
	a.step(step, StepFailed, err.Error())
	if ae, ok := apierr.As(err); ok {
		return ae
	}
	return &stepError{step: step, err: err}
}

// auditApply writes the environment's config.apply entry: counts and field ids, never values.
func (a *applier) auditApply(r ApplyRequest) error {
	if !a.anyWrite() && !a.wrote {
		return nil // nothing was written: a repeat of the same file leaves no trace
	}
	p := a.p
	changedServices := []string{}
	fields := map[string][]string{}
	for _, sp := range p.svcs {
		if sp.pl.Action != ActionCreate && sp.pl.Action != ActionUpdate {
			continue
		}
		changedServices = append(changedServices, sp.slug)
		for _, c := range sp.pl.Changes {
			fields[sp.slug] = append(fields[sp.slug], c.Field)
		}
	}
	sort.Strings(changedServices)
	meta := map[string]any{
		"origin": originOf(p.src, a.prin), "repo": p.src.Repo, "branch": p.src.Branch, "commit": p.src.Commit, "path": p.src.Path,
		"fileSha256": p.fileSHA, "planId": p.public.PlanID, "prune": r.Prune, "deploy": r.Deploy,
		"summary": map[string]int{
			"create": p.public.Summary.Create, "update": p.public.Summary.Update, "unchanged": p.public.Summary.Unchanged,
			"staged": p.public.Summary.Staged, "immediate": p.public.Summary.Immediate, "pruned": p.public.Summary.Pruned,
		},
		"services": changedServices, "fields": fields, "environment": p.env.Env.Slug,
	}
	return audit.Record(a.ctx, a.tx, audit.Entry{
		Action: "config.apply", ResourceType: "environment", ResourceID: p.env.Env.ID.String(), ProjectID: &p.env.Env.ProjectID,
		WorkspaceID: p.env.WorkspaceID, Metadata: meta,
	})
}

// publish announces the touched services and the environment's config state.
func (a *applier) publish() {
	p := a.p
	for slug := range a.changed {
		if sc, ok := a.scopes[slug]; ok {
			services.PublishService(a.s.Bus, services.EventServiceUpdated, sc)
		}
	}
	services.Publish(a.s.Bus, evConfigUpdated, p.env.Env.ProjectID, p.env.Env.ID, p.env.Env.ID)
}

// scopeFor returns the (cached) scope of a service of the environment, loaded inside the transaction.
func (a *applier) scopeFor(slug string) (services.Scope, error) {
	if sc, ok := a.scopes[slug]; ok {
		return sc, nil
	}
	ls := a.p.live.Services[slug]
	if ls == nil {
		return services.Scope{}, fmt.Errorf("service %q does not exist", slug)
	}
	sc, err := services.LoadScope(a.ctx, a.q, policy.ScopeProjectsWrite, ls.Row.ID)
	if err != nil {
		return services.Scope{}, err
	}
	a.scopes[slug] = sc
	return sc, nil
}

// reload refreshes the cached scope of a service after a write.
func (a *applier) reload(slug string, id uuid.UUID) (services.Scope, error) {
	sc, err := services.LoadScopeInternal(a.ctx, a.q, id)
	if err != nil {
		return services.Scope{}, err
	}
	a.scopes[slug] = sc
	a.rows[slug] = sc.Service
	return sc, nil
}

func jsonOf(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func stepName(kind, slug string, rest ...string) string {
	return strings.Join(append([]string{kind + ":" + slug}, rest...), ":")
}

// collapseGenerations makes the generation of every service of the environment advance by exactly one
// for the whole apply: each use case bumps it when it writes, and one apply is one logical change.
// Services created by the apply end at generation 1. Nothing else reads the intermediate values
// (the transaction holds the rows locked).
func (a *applier) collapseGenerations(gen0 map[uuid.UUID]int64) error {
	want := make(map[uuid.UUID]int64, len(gen0)+len(a.created))
	for id, g := range gen0 {
		want[id] = g + 1
	}
	for slug := range a.created {
		want[a.rows[slug].ID] = 1
	}
	for id, w := range want {
		sc, err := services.LoadScopeInternal(a.ctx, a.q, id)
		if err != nil {
			return err
		}
		if sc.Service.Generation > w {
			if err := a.q.SetServiceGeneration(a.ctx, db.SetServiceGenerationParams{ID: id, Generation: w}); err != nil {
				return fmt.Errorf("set generation: %w", err)
			}
		}
	}
	for i := range a.out.Services {
		sc, err := a.reload(a.out.Services[i].Slug, a.out.Services[i].ServiceID)
		if err != nil {
			return err
		}
		a.out.Services[i].Generation = sc.Service.Generation
	}
	return nil
}
