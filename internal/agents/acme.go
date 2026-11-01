package agents

import (
	"context"

	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// recordAcme persists the DNS-01 certificate states a heartbeat carries
// (wildcard certificates and domains served through a DNS-01 resolver). The
// queries write only on a change or every five minutes; the server view is
// republished when anything was written.
func (h *Hub) recordAcme(ctx context.Context, c *agentConn, hb agentprotocol.Heartbeat) {
	if len(hb.AcmeCerts) == 0 {
		return
	}
	changed, err := dns01.Observe(ctx, h.q, c.serverID, hb.AcmeCerts)
	if err != nil {
		c.log.ErrorContext(ctx, "record acme certificate status", "err", err)
		return
	}
	if changed {
		h.publishServer(c.serverID)
	}
}
