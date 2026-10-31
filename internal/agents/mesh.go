package agents

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// MeshObserver receives the mesh status an agent reports with every heartbeat
// (nil status: the agent runs no mesh container). internal/mesh implements it.
type MeshObserver interface {
	ObserveHeartbeat(ctx context.Context, serverID uuid.UUID, status *agentprotocol.MeshStatus) error
}

// SetMeshObserver installs the heartbeat hook of the mesh. Without one the mesh
// status is ignored.
func (h *Hub) SetMeshObserver(o MeshObserver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.meshObs = o
}

func (h *Hub) meshObserver() MeshObserver {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.meshObs
}

// recordMesh hands a heartbeat's mesh status to the mesh. It runs off the
// heartbeat path: the mesh may call the tailnet coordinator.
func (h *Hub) recordMesh(ctx context.Context, c *agentConn, hb agentprotocol.Heartbeat) {
	obs := h.meshObserver()
	if obs == nil {
		return
	}
	go func() {
		mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := obs.ObserveHeartbeat(mctx, c.serverID, hb.Mesh); err != nil {
			c.log.ErrorContext(mctx, "record mesh status", "err", err)
		}
	}()
}
