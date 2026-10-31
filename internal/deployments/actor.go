package deployments

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// actorKey identifies one credential behind deployments.actor_id.
type actorKey struct{ kind, id string }

// actorLabels resolves the user and token names behind deps' actors in one
// query. A credential deleted since the deployment has no entry.
func (s *Service) actorLabels(ctx context.Context, deps []db.Deployment) (map[actorKey]string, error) {
	seen := map[string]struct{}{}
	var ids []string
	for _, d := range deps {
		if d.ActorID == "" {
			continue
		}
		if _, ok := seen[d.ActorID]; !ok {
			seen[d.ActorID] = struct{}{}
			ids = append(ids, d.ActorID)
		}
	}
	out := map[actorKey]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListDeploymentActorLabels(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list deployment actors: %w", err)
	}
	for _, r := range rows {
		out[actorKey{r.Kind, r.ID}] = r.Label
	}
	return out, nil
}

// actorDisplay is the label shown for who started d: the user's name (or
// email), the token's name, "MCP · <token name>", or "System" for the control
// plane itself (reconcile, scaling, drains).
func actorDisplay(d db.Deployment, labels map[actorKey]string) string {
	switch d.ActorType {
	case auth.TypeUser:
		if l, ok := labels[actorKey{"user", d.ActorID}]; ok {
			return l
		}
		return "Removed user"
	case auth.TypeToken:
		if l, ok := labels[actorKey{"token", d.ActorID}]; ok {
			return l
		}
		return "Removed token"
	case auth.TypeMCP:
		if l, ok := labels[actorKey{"token", d.ActorID}]; ok {
			return "MCP · " + l
		}
		return "MCP"
	}
	return "System"
}

// startedBy labels who started a deployment for the secret access log ("System"
// for the control plane itself). Best effort: empty when the deployment cannot
// be read.
func (s *Service) startedBy(ctx context.Context, deploymentID string) string {
	id, err := uuid.Parse(deploymentID)
	if err != nil {
		return ""
	}
	row, err := s.q.GetDeploymentScopeInternal(ctx, id)
	if err != nil {
		return ""
	}
	labels, err := s.actorLabels(ctx, []db.Deployment{row.Deployment})
	if err != nil {
		return ""
	}
	return actorDisplay(row.Deployment, labels)
}
