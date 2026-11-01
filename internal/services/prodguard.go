package services

import (
	"context"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// ProdScope describes an operation on this service for the production guard.
// Callers pass it to prodguard.Check with the operation name and a digest of
// its arguments.
func (s Scope) ProdScope(summary string, irreversible bool, effects ...string) prodguard.ProdScope {
	return prodguard.ProdScope{
		WorkspaceID: s.WorkspaceID, ProjectID: s.ProjectID, EnvironmentID: s.EnvironmentID,
		Project: s.ProjectName, Environment: s.EnvName, IsProduction: s.IsProduction,
		Summary: summary, Effects: effects, Irreversible: irreversible,
		Targets: []prodguard.ApprovalTarget{{Kind: "service", ID: s.Service.ID, Name: s.Service.Name, Generation: s.Service.Generation}},
	}
}

// ProdScope describes an operation on this environment for the production
// guard. The targets are the services (or other objects) it affects.
func (e EnvScope) ProdScope(summary string, irreversible bool, targets []prodguard.ApprovalTarget, effects ...string) prodguard.ProdScope {
	return prodguard.ProdScope{
		WorkspaceID: e.WorkspaceID, ProjectID: e.Env.ProjectID, EnvironmentID: e.Env.ID,
		Project: e.ProjectSlug, Environment: e.Env.Name, IsProduction: e.Env.IsProduction,
		Summary: summary, Effects: effects, Irreversible: irreversible, Targets: targets,
	}
}

// GuardTarget is a prodguard.ApprovalTarget for an object without a generation.
func GuardTarget(kind string, id uuid.UUID, name string) prodguard.ApprovalTarget {
	return prodguard.ApprovalTarget{Kind: kind, ID: id, Name: name}
}

// Guard asks the production guard about one operation on this service. It does
// nothing (and builds no plan) unless the principal is guarded and the service
// is in a production environment. digestArgs bind the approval to the call's
// arguments in addition to the service id.
func (s Scope) Guard(ctx context.Context, op, summary string, irreversible bool, effects []string, digestArgs ...any) error {
	if !prodguard.Applies(ctx, s.IsProduction) {
		return nil
	}
	args := append([]any{s.Service.ID}, digestArgs...)
	return prodguard.Check(ctx, s.ProdScope(summary, irreversible, effects...), op, prodguard.Digest(op, args...))
}

// Guard asks the production guard about one operation on this environment. It
// does nothing (and builds no plan) unless the principal is guarded and the
// environment is production. digestArgs bind the approval to the call's
// arguments in addition to the environment id.
func (e EnvScope) Guard(ctx context.Context, op, summary string, irreversible bool, effects []string, digestArgs ...any) error {
	if !prodguard.Applies(ctx, e.Env.IsProduction) {
		return nil
	}
	targets := []prodguard.ApprovalTarget{GuardTarget("environment", e.Env.ID, e.Env.Name)}
	args := append([]any{e.Env.ID}, digestArgs...)
	return prodguard.Check(ctx, e.ProdScope(summary, irreversible, targets, effects...), op, prodguard.Digest(op, args...))
}
