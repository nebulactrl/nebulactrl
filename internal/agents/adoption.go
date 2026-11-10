package agents

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// holdForAdoption holds a server that was just added and already runs
// workloads of an earlier NebulaCtrl. While it is held the desired state tells
// the agent to leave every managed container and live route it does not know
// alone (Reconcile.KeepUnknown), until the person adopts or leaves them.
//
// Only the first hello after enrollment holds (before is the server as it was
// before this hello): "when the server is added". A reconnect of a known
// server never holds, so its orphan GC is unchanged. An agent that does not
// advertise CapAdoption cannot honour the hold, so it is not held.
//
// An error means the hold could not be recorded: the caller must not send a
// desired state, because a frame without KeepUnknown tells the agent to sweep.
func (h *Hub) holdForAdoption(ctx context.Context, serverID uuid.UUID, before *db.Server, facts agentprotocol.Facts) error {
	if before == nil || before.Status != "pending" {
		return nil
	}
	if !slices.Contains(facts.Capabilities, agentprotocol.CapAdoption) || len(facts.ManagedServices) == 0 {
		return nil
	}
	n, err := h.q.SetServerAdoptionHeld(ctx, serverID)
	if err != nil {
		return fmt.Errorf("hold server for adoption: %w", err)
	}
	if n == 0 {
		return nil
	}
	h.publishServer(serverID)
	h.log.InfoContext(ctx, "server held for adoption", "server_id", serverID, "services", len(facts.ManagedServices))
	return nil
}
