package agents

import (
	"context"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SourceScanSink handles what an agent found in a checked-out git source: the
// template env files (.env.example, …, internal/variables) and the project
// config file (nebula.toml, internal/configsync).
type SourceScanSink interface {
	HandleSourceScan(ctx context.Context, serverID uuid.UUID, scan agentprotocol.SourceScan) error
}

// AddSourceScanSink registers a source scan hook (wire time only). Without
// one, scans are discarded; every registered hook sees every scan.
func (h *Hub) AddSourceScanSink(s SourceScanSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scanSinks = append(h.scanSinks, s)
}

func (h *Hub) sourceScanSinks() []SourceScanSink {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.scanSinks
}

func (h *Hub) onSourceScan(ctx context.Context, c *agentConn, scan agentprotocol.SourceScan) {
	for _, sink := range h.sourceScanSinks() {
		if err := sink.HandleSourceScan(ctx, c.serverID, scan); err != nil {
			c.log.WarnContext(ctx, "handle source scan", "service_id", scan.ServiceID, "err", err)
		}
	}
}
