package fleet

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
)

const guardDrain = "start_server_drain"

// guardDrainStart asks the production guard before a drain redeploys services
// that run in production environments. It does nothing when no service on the
// server is in production. The plan lists those services and where they move.
func (s *Service) guardDrainStart(ctx context.Context, srcID, wsID uuid.UUID, srcName string, rows []db.ListFleetServicesRow, plan []PlanMove, confirmStateful bool) error {
	if !prodguard.Applies(ctx, true) {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	envIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if !seen[r.EnvironmentID] {
			seen[r.EnvironmentID] = true
			envIDs = append(envIDs, r.EnvironmentID)
		}
	}
	prodIDs, err := s.q.ListProductionEnvironmentIDs(ctx, envIDs)
	if err != nil {
		return fmt.Errorf("list production environments: %w", err)
	}
	prod := make(map[uuid.UUID]bool, len(prodIDs))
	for _, id := range prodIDs {
		prod[id] = true
	}
	moveTo := make(map[uuid.UUID]PlanMove, len(plan))
	for _, m := range plan {
		moveTo[m.ServiceID] = m
	}
	var first *db.ListFleetServicesRow
	var targets []prodguard.ApprovalTarget
	var effects, pairs []string
	for i, r := range rows {
		if !prod[r.EnvironmentID] {
			continue
		}
		if first == nil {
			first = &rows[i]
		}
		targets = append(targets, prodguard.ApprovalTarget{Kind: "service", ID: r.ID, Name: r.Name, Generation: r.Generation})
		m := moveTo[r.ID]
		effects = append(effects, fmt.Sprintf("%s (%s / %s) is redeployed on %s.", r.Name, r.ProjectName, r.EnvironmentName, m.TargetServerName))
		pairs = append(pairs, r.ID.String()+">"+m.TargetServerName)
	}
	if first == nil {
		return nil
	}
	effects = append(effects, "Each service is deployed on its new server first; then the old container on "+srcName+" is stopped.")
	effects = append(effects, "Volumes are not copied.")
	sort.Strings(pairs)
	digestArgs := []any{strconv.FormatBool(confirmStateful)}
	for _, p := range pairs {
		digestArgs = append(digestArgs, p)
	}
	return prodguard.Check(ctx, prodguard.ProdScope{
		WorkspaceID: wsID, ProjectID: first.ProjectID, EnvironmentID: first.EnvironmentID,
		Project: first.ProjectName, Environment: first.EnvironmentName, IsProduction: true,
		Summary: fmt.Sprintf("Drain the server %s: move %d production service(s) to other servers", srcName, len(targets)),
		Effects: effects, Targets: targets,
	}, guardDrain, prodguard.DigestIDs(guardDrain, []uuid.UUID{srcID}, digestArgs...))
}
