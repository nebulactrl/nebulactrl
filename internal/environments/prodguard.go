package environments

import (
	"context"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

// Guard operation names (prodguard approvals are bound to them).
const (
	guardDelete     = "delete_environment"
	guardUnmarkProd = "unmark_production"
)

// guard asks the production guard about a change to a production environment.
func (s *Service) guard(ctx context.Context, env db.Environment, op, summary string, irreversible bool, effects ...string) error {
	if !prodguard.Applies(ctx, env.IsProduction) {
		return nil
	}
	ws := policy.WorkspaceID(ctx)
	project := ""
	if p, err := s.q.GetProject(ctx, db.GetProjectParams{ID: env.ProjectID, WorkspaceID: ws}); err == nil {
		project = p.Name
	}
	return prodguard.Check(ctx, prodguard.ProdScope{
		WorkspaceID: ws, ProjectID: env.ProjectID, EnvironmentID: env.ID, Project: project, Environment: env.Name, IsProduction: true,
		Summary: fmt.Sprintf(summary, env.Name, project), Effects: effects, Irreversible: irreversible,
		Targets: []prodguard.ApprovalTarget{{Kind: "environment", ID: env.ID, Name: env.Name}},
	}, op, prodguard.Digest(op, env.ID))
}
