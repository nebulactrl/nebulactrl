package configsync

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// GuardApplyConfig is the operation name production approvals of an apply are
// bound to.
const GuardApplyConfig = "apply_config"

// maxGuardEffects bounds the consequences listed on the approval page.
const maxGuardEffects = 12

// planApplier is the part of *Service the guard drives.
type planApplier interface {
	Plan(ctx context.Context, projectID uuid.UUID, r PlanRequest) (Plan, error)
	Apply(ctx context.Context, projectID uuid.UUID, r ApplyRequest) (ApplyResult, error)
}

// ApplyGuarded is Apply behind the production guard. Every entry point (REST
// and MCP) calls it instead of Apply.
//
//   - A principal that is not guarded (a person signed in, or a token an
//     instance admin or team admin let skip approvals) gets plain Apply. Every
//     other token, REST or MCP, is guarded.
//   - A guarded principal may stage in production without approval (same as
//     stage_changes), but a plan with an immediate-kind change (scale, uptime,
//     tasks, backups, git link, DNS, name) needs a person's approval of this
//     exact plan: the approval is bound to the plan id, so a changed plan, or a
//     service that moved on, asks again.
//   - Deploying is never available to a guarded principal, and an MCP
//     principal can neither deploy nor prune.
func (s *Service) ApplyGuarded(ctx context.Context, projectID uuid.UUID, r ApplyRequest) (ApplyResult, error) {
	return guardedApply(ctx, s, projectID, r)
}

func guardedApply(ctx context.Context, s planApplier, projectID uuid.UUID, r ApplyRequest) (ApplyResult, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return ApplyResult{}, apierr.Unauthorized()
	}
	if p.Type == auth.TypeMCP && r.Deploy {
		return ApplyResult{}, apierr.Unprocessable(CodeDeployNotAllowed, "agents cannot deploy from a config apply; use apply_changes")
	}
	if p.Type == auth.TypeMCP && r.Prune {
		return ApplyResult{}, apierr.Unprocessable(CodePruneNotAllowed, "agents cannot prune")
	}
	if !prodguard.Guarded(p) {
		return s.Apply(ctx, projectID, r)
	}
	if r.Deploy {
		return ApplyResult{}, apierr.New(http.StatusForbidden, CodeDeployNotAllowed, "A principal that needs production approval cannot deploy from an apply: stage it, review the staged changes, then deploy them.")
	}
	plan, err := s.Plan(ctx, projectID, r.PlanRequest)
	if err != nil {
		return ApplyResult{}, err
	}
	if !plan.Valid || !plan.Environment.IsProduction || !plan.HasImmediateChanges() {
		// Nothing for a person to approve (blockers refuse, staged-only is free).
		return s.Apply(ctx, projectID, r)
	}
	if r.PlanID != "" && r.PlanID != plan.PlanID {
		return ApplyResult{}, ErrPlanStale(plan)
	}
	// An approval must never be requested for a call the principal may not make.
	if err := requireScopes(ctx, p, projectID, plan.RequiredScopes); err != nil {
		return ApplyResult{}, err
	}
	if err := prodguard.Check(ctx, guardScope(ctx, plan), GuardApplyConfig, prodguard.Digest(GuardApplyConfig, plan.PlanID)); err != nil {
		return ApplyResult{}, err
	}
	// Pin the apply to the approved plan: anything that changed since fails with plan_stale.
	r.PlanID = plan.PlanID
	// The approval covers every immediate change of this plan, so the domain
	// services (tasks, backups, git link, DNS, …) do not ask again for each.
	return s.Apply(prodguard.Covered(ctx), projectID, r)
}

// requireScopes checks projects:write and every scope the plan needs, so the
// guard asks for approval only of a call that would otherwise run.
func requireScopes(ctx context.Context, p *auth.Principal, projectID uuid.UUID, needed []string) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, &projectID); err != nil {
		return err
	}
	var missing []string
	for _, sc := range needed {
		if !policy.HasScope(p, sc) {
			missing = append(missing, sc)
		}
	}
	if len(missing) > 0 {
		return ErrInsufficientScope(missing)
	}
	return nil
}

// HasImmediateChanges reports whether the plan writes anything that takes
// effect at once (or later, when the service is deployed) instead of being
// staged. Such a plan needs a person's approval in production for a guarded
// principal.
func (p Plan) HasImmediateChanges() bool {
	writes := func(cs []PlanChange) bool {
		for _, c := range cs {
			if c.Applies == AppliesStaged {
				continue
			}
			if c.Op == OpSet || c.Op == OpAdd || c.Op == OpRemove {
				return true
			}
		}
		return false
	}
	for _, sp := range p.Services {
		if sp.Action == ActionBlocked || sp.Action == ActionNotInFile {
			continue
		}
		if writes(sp.Changes) {
			return true
		}
	}
	return writes(p.Shared.Project) || writes(p.Shared.Environment)
}

// guardScope is what the approver reads: the environment, the services the
// apply touches (with the generation they were planned at) and the immediate
// fields by id, never values.
func guardScope(ctx context.Context, plan Plan) prodguard.ProdScope {
	targets := []prodguard.ApprovalTarget{{Kind: "environment", ID: plan.Environment.ID, Name: plan.Environment.Slug}}
	var effects []string
	for _, sp := range plan.Services {
		if sp.Action == ActionBlocked || sp.Action == ActionNotInFile || sp.Action == ActionUnchanged {
			continue
		}
		if sp.ServiceID != nil {
			targets = append(targets, prodguard.ApprovalTarget{Kind: "service", ID: *sp.ServiceID, Name: sp.Slug, Generation: sp.Generation})
		}
		var now []string
		staged := 0
		for _, c := range sp.Changes {
			if c.Op != OpSet && c.Op != OpAdd && c.Op != OpRemove {
				continue
			}
			if c.Applies == AppliesStaged {
				staged++
				continue
			}
			now = append(now, c.Field)
		}
		if len(now) > 0 {
			sort.Strings(now)
			effects = append(effects, fmt.Sprintf("%s: changes at once: %s.", sp.Slug, strings.Join(now, ", ")))
		}
		if staged > 0 {
			effects = append(effects, fmt.Sprintf("%s: stages %d change(s); nothing deploys.", sp.Slug, staged))
		}
	}
	if len(effects) > maxGuardEffects {
		more := len(effects) - maxGuardEffects
		effects = append(effects[:maxGuardEffects], fmt.Sprintf("… and %d more service(s).", more))
	}
	return prodguard.ProdScope{
		WorkspaceID: policy.WorkspaceID(ctx), ProjectID: plan.Project.ID, EnvironmentID: plan.Environment.ID,
		Project: plan.Project.Slug, Environment: plan.Environment.Slug, IsProduction: true,
		Summary: fmt.Sprintf("Apply nebula.toml to %s / %s (plan %s)", plan.Project.Slug, plan.Environment.Slug, shortPlanID(plan.PlanID)),
		Effects: effects, Targets: targets,
	}
}

// shortPlanID is the id as shown to people: pl_ and the first eight hex digits.
func shortPlanID(id string) string {
	if len(id) > 11 {
		return id[:11]
	}
	return id
}
