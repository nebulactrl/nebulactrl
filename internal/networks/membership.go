package networks

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Membership is what the agent needs to place a service's containers on
// networks: the three fields shared by the deploy, task, scale and reconcile
// payloads.
type Membership struct {
	BlockEgress         bool
	SharedNetworks      []agentprotocol.SharedNetworkAttachment
	EnvHasEgressBlocked bool
}

// Resolve computes the membership of a service on a server.
//
//   - blockEgress and attached come from the release (or, for a new release,
//     from the staged state).
//   - Attachments whose network no longer exists or lives on another server are
//     dropped (a rollback must not fail on a deleted network).
//   - A service that blocks egress never joins shared networks (a shared bridge
//     would give it internet access again).
//   - EnvHasEgressBlocked is true when this or another live service of the
//     environment on the server blocks egress.
func Resolve(ctx context.Context, q *db.Queries, environmentID, serverID, serviceID uuid.UUID, blockEgress bool, attached []services.SnapshotNetwork) (Membership, error) {
	m := Membership{BlockEgress: blockEgress}
	if !blockEgress && len(attached) > 0 {
		ids := make([]uuid.UUID, len(attached))
		for i, a := range attached {
			ids[i] = a.ID
		}
		rows, err := q.ListNetworksByIDs(ctx, ids)
		if err != nil {
			return m, fmt.Errorf("list networks: %w", err)
		}
		live := make(map[uuid.UUID]bool, len(rows))
		for _, r := range rows {
			live[r.ID] = r.ServerID == serverID
		}
		var keep []services.SnapshotNetwork
		for _, a := range attached {
			if live[a.ID] {
				keep = append(keep, a)
			}
		}
		m.SharedNetworks = services.NetworkAttachments(keep)
	}
	blocked, err := services.EnvHasEgressBlocked(ctx, q, environmentID, serverID, serviceID, blockEgress)
	if err != nil {
		return m, err
	}
	m.EnvHasEgressBlocked = blocked
	return m, nil
}
