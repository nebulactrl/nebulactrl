package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

const guardDelete = "delete_project"

// guardDelete asks the production guard before a project that has a production
// environment is deleted. An empty-project precondition is checked first so
// nobody is asked to approve a delete that would fail anyway.
func (s *Service) guardDelete(ctx context.Context, p db.Project) error {
	if !prodguard.Applies(ctx, true) {
		return nil
	}
	envs, err := s.q.ListEnvironments(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}
	var prod *db.Environment
	for i := range envs {
		if envs[i].IsProduction {
			prod = &envs[i]
			break
		}
	}
	if prod == nil {
		return nil
	}
	n, err := s.q.CountProjectLiveServices(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("count services: %w", err)
	}
	if n > 0 {
		return apierr.Conflict("project_not_empty", "delete the project's services first")
	}
	return prodguard.Check(ctx, prodguard.ProdScope{
		WorkspaceID: policy.WorkspaceID(ctx), ProjectID: p.ID, EnvironmentID: prod.ID, Project: p.Name, Environment: prod.Name, IsProduction: true,
		Summary:      fmt.Sprintf("Delete the project %s and its production environment", p.Name),
		Effects:      []string{"Removes the project and all of its environments, including production.", "The project holds no services."},
		Irreversible: true,
		Targets:      []prodguard.ApprovalTarget{{Kind: "project", ID: p.ID, Name: p.Name}},
	}, guardDelete, prodguard.Digest(guardDelete, p.ID))
}

const guardDeleteCascade = "delete_project_cascade"

// guardCascade asks the production guard before a project that has a
// production environment is deleted with everything in it. The plan lists what
// goes, so the approver sees the services, data and domains.
func (s *Service) guardCascade(ctx context.Context, p db.Project) error {
	if !prodguard.Applies(ctx, true) {
		return nil
	}
	envs, err := s.q.ListEnvironments(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list environments: %w", err)
	}
	var prod *db.Environment
	for i := range envs {
		if envs[i].IsProduction {
			prod = &envs[i]
			break
		}
	}
	if prod == nil {
		return nil
	}
	plan, err := s.plan(ctx, s.q, p)
	if err != nil {
		return err
	}
	return prodguard.Check(ctx, prodguard.ProdScope{
		WorkspaceID: policy.WorkspaceID(ctx), ProjectID: p.ID, EnvironmentID: prod.ID, Project: p.Name, Environment: prod.Name, IsProduction: true,
		Summary: fmt.Sprintf("Delete the project %s with everything in it, including its production environment", p.Name),
		Effects: []string{
			fmt.Sprintf("Deletes %d services, %d databases and %d previews, and removes their containers from the servers.", plan.Services, plan.Databases, plan.Previews),
			fmt.Sprintf("Deletes %d data volumes with their data. This cannot be undone.", plan.Volumes),
			fmt.Sprintf("Removes %d domains and %d DNS records NebulaCtrl manages for them.", plan.Domains, plan.ManagedDNSRecords),
			"Backups already written to backup destinations stay there.",
		},
		Irreversible: true,
		Targets:      []prodguard.ApprovalTarget{{Kind: "project", ID: p.ID, Name: p.Name}},
	}, guardDeleteCascade, cascadeDigest(plan))
}

// cascadeDigest binds the approval to what the approver saw: the project, its
// environments and every count of the plan. A service, volume, domain or
// environment added after the approval changes the plan, so the delete asks
// again instead of removing what nobody reviewed.
func cascadeDigest(plan DeletionPlan) []byte {
	envs := make([]uuid.UUID, len(plan.Environments))
	for i, e := range plan.Environments {
		envs[i] = e.ID
	}
	return prodguard.DigestIDs(guardDeleteCascade, append(envs, plan.Project.ID),
		plan.Services, plan.Databases, plan.Volumes, plan.Domains, plan.Previews, plan.StatusPageComponents, plan.ManagedDNSRecords)
}
