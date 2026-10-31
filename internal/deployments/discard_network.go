package deployments

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// discardNetwork reverts one shared-network attachment to the release's state:
// an attachment added since is removed, one removed since is attached again
// (when the network still exists on the service's server).
func (s *Service) discardNetwork(ctx context.Context, q *db.Queries, svc db.Service, snap services.Snapshot, cur services.CurrentState, name string) error {
	var was *services.SnapshotNetwork
	for i := range snap.Networks {
		if snap.Networks[i].Name == name {
			was = &snap.Networks[i]
		}
	}
	var is *services.NetworkRef
	for i := range cur.Networks {
		if cur.Networks[i].Name == name {
			is = &cur.Networks[i]
		}
	}
	switch {
	case was == nil && is != nil:
		if _, err := q.DetachServiceNetwork(ctx, db.DetachServiceNetworkParams{ServiceID: svc.ID, NetworkID: is.ID}); err != nil {
			return fmt.Errorf("detach network: %w", err)
		}
	case was != nil && is == nil:
		rows, err := q.ListNetworksByIDs(ctx, []uuid.UUID{was.ID})
		if err != nil {
			return fmt.Errorf("get network: %w", err)
		}
		if len(rows) == 0 || svc.ServerID == nil || rows[0].ServerID != *svc.ServerID {
			return apierr.Conflict("network_gone", "the shared network was deleted or is on another server; the attachment cannot be restored")
		}
		if services.DecodeConfig(svc.Config).BlockEgress {
			return services.NetworkConflict()
		}
		if _, err := q.AttachServiceNetwork(ctx, db.AttachServiceNetworkParams{ServiceID: svc.ID, NetworkID: was.ID}); err != nil {
			return fmt.Errorf("attach network: %w", err)
		}
	default:
		return apierr.Conflict("not_discardable", "the network's alias changed with a rename; it applies on the next deployment")
	}
	if _, err := q.BumpServiceGeneration(ctx, svc.ID); err != nil {
		return fmt.Errorf("bump generation: %w", err)
	}
	return nil
}
