package agents

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TunnelObserver is told when a server's Cloudflare connector reports its first
// connection (the tunnel becomes active). The tunnel service implements it.
type TunnelObserver interface {
	TunnelActivated(ctx context.Context, serverID uuid.UUID)
}

// SetTunnelObserver installs the activation hook. Without one an activation is
// only recorded.
func (h *Hub) SetTunnelObserver(o TunnelObserver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tunnelObs = o
}

func (h *Hub) tunnelObserver() TunnelObserver {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.tunnelObs
}

const tunnelMissingMessage = "The cloudflared container is not running on this server."

// recordTunnel persists the connector state a heartbeat carries. A heartbeat
// without one means the host has no cloudflared container: that is only news
// for a server that has a tunnel row. The query writes when the state changed
// or the stored one is older than a minute; a first online report activates
// the tunnel.
func (h *Hub) recordTunnel(ctx context.Context, c *agentConn, hb agentprotocol.Heartbeat) {
	p := db.RecordServerTunnelHeartbeatParams{ServerID: c.serverID}
	if t := hb.Tunnel; t != nil {
		switch t.State {
		case agentprotocol.TunnelStateProvisioning, agentprotocol.TunnelStateOnline,
			agentprotocol.TunnelStateOffline, agentprotocol.TunnelStateNeedsAttn:
		default:
			return
		}
		p.State, p.Message, p.Connections, p.Image = t.State, t.Message, int32(max(t.Connections, 0)), t.Image
	} else {
		p.State, p.Message, p.Missing = agentprotocol.TunnelStateOffline, tunnelMissingMessage, true
	}
	prev, err := h.q.RecordServerTunnelHeartbeat(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return // no tunnel, being removed, unchanged and fresh, or not yet missed
	}
	if err != nil {
		c.log.ErrorContext(ctx, "record tunnel status", "err", err)
		return
	}
	if prev.PrevState != p.State {
		h.publishServer(c.serverID)
	}
	if prev.PrevActivatedAt == nil && prev.ActivatedAt != nil {
		if obs := h.tunnelObserver(); obs != nil {
			go func() {
				actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				obs.TunnelActivated(actx, c.serverID)
			}()
		}
	}
}
