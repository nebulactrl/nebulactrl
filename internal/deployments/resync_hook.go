package deployments

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Resyncer pushes a server's current desired state to its connected agent
// (the agent hub implements it). The agent's reconcile then attaches running
// containers to any network they are missing.
type Resyncer interface {
	Resync(ctx context.Context, serverID uuid.UUID) error
}

// SetResync installs the desired-state pusher (wire time only).
func (s *Service) SetResync(r Resyncer) { s.resync = r }

// changesEgressState reports whether promoting dep changes which services of
// its environment block outbound internet on dep's server: a blocking
// service's first deployment on the server, or a toggle of its setting. Every
// other service of the environment must then join (or may leave) the internal
// network, which a running replica only learns from a pushed desired state.
// It must run before MarkServiceDeployed, while the service still points at
// the previous deployment.
func changesEgressState(ctx context.Context, q *db.Queries, log *slog.Logger, dep db.Deployment, prevID *uuid.UUID) bool {
	if dep.ReleaseID == nil || dep.ServerID == nil {
		return false
	}
	rel, err := q.GetReleaseByID(ctx, *dep.ReleaseID)
	if err != nil {
		log.WarnContext(ctx, "egress state: load release", "correlation_id", dep.ID.String(), "err", err)
		return false
	}
	now := services.ReleaseBlocksEgress(rel.Config)
	before := false
	if prevID != nil && *prevID != dep.ID {
		prev, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*prevID})
		if err != nil {
			log.WarnContext(ctx, "egress state: load previous deployment", "correlation_id", dep.ID.String(), "err", err)
			return false
		}
		if len(prev) == 1 && prev[0].ReleaseID != nil && prev[0].ServerID != nil && *prev[0].ServerID == *dep.ServerID {
			prevRel, err := q.GetReleaseByID(ctx, *prev[0].ReleaseID)
			if err != nil {
				log.WarnContext(ctx, "egress state: load previous release", "correlation_id", dep.ID.String(), "err", err)
				return false
			}
			before = services.ReleaseBlocksEgress(prevRel.Config)
		}
	} else if prevID != nil {
		return false
	}
	return now != before
}
