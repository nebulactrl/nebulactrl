package agents

import (
	"context"
	"encoding/json"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// recordProxyPorts stores the ports the proxy reports (facts.proxyPorts) and
// notifies watchers when they changed. Agents without proxy.ports send none.
func (h *Hub) recordProxyPorts(ctx context.Context, c *agentConn, hb agentprotocol.Heartbeat) {
	p := hb.Proxy.Ports
	if p == nil || p.HTTP < 1 || p.HTTPS < 1 {
		return
	}
	ports := *p
	ports.Normalize()
	raw, err := json.Marshal(ports)
	if err != nil {
		return
	}
	n, err := h.q.RecordServerProxyPorts(ctx, db.RecordServerProxyPortsParams{ID: c.serverID, Ports: raw})
	if err != nil {
		c.log.ErrorContext(ctx, "record proxy ports", "err", err)
		return
	}
	if n > 0 {
		h.publishServer(c.serverID)
	}
}
