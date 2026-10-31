package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// tunnelTimeout bounds a sync_tunnel command: image pull, container start and
// the wait for the first connection to Cloudflare.
const tunnelTimeout = 4 * time.Minute

func tunnelResult(id string, st agentprotocol.TunnelStatus) agentprotocol.ResultFrame {
	data, _ := json.Marshal(st)
	return agentprotocol.ResultFrame{CommandID: id, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// TunnelStatus is the observed connector state for heartbeats (nil when the
// host has no cloudflared container).
func (e *Engine) TunnelStatus(ctx context.Context) *agentprotocol.TunnelStatus {
	return e.tun.Status(ctx)
}

// handleSyncTunnel ensures the cloudflared container with the send-time token.
// Problems become the reported state, not a failed command: the control plane
// shows the state and the operator retries.
func (e *Engine) handleSyncTunnel(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.SyncTunnelPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid sync_tunnel payload: "+err.Error())
	}
	token := cmd.Secrets[agentprotocol.SecretTunnelToken]
	red := newRedactor(cmd.Secrets, nil)
	e.tunMu.Lock()
	defer e.tunMu.Unlock()
	tctx, cancel := context.WithTimeout(ctx, tunnelTimeout)
	defer cancel()
	logf := func(line string) { e.log.Info(red.apply(line), "tunnel_id", p.Tunnel.TunnelID) }
	return tunnelResult(cmd.ID, e.tun.Ensure(tctx, p.Tunnel, token, logf))
}

// handleRemoveTunnel removes the cloudflared container.
func (e *Engine) handleRemoveTunnel(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.RemoveTunnelPayload
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return failedResult(cmd.ID, "invalid remove_tunnel payload: "+err.Error())
		}
	}
	e.tunMu.Lock()
	defer e.tunMu.Unlock()
	tctx, cancel := context.WithTimeout(ctx, tunnelTimeout)
	defer cancel()
	return tunnelResult(cmd.ID, e.tun.Remove(tctx, func(line string) { e.log.Info(line, "tunnel_id", p.TunnelID) }))
}
