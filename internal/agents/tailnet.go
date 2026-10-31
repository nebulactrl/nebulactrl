package agents

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// recordTailnet persists the observed per-server tailnet state a heartbeat
// carries (nil = no tailnet, stored as NULL). The query writes only when the
// state changed or the stored one is older than five minutes, and the server
// view is republished only on a change of what the UI shows.
func (h *Hub) recordTailnet(ctx context.Context, c *agentConn, hb agentprotocol.Heartbeat) {
	var raw json.RawMessage
	deviceID := ""
	if hb.Tailnet != nil {
		b, err := json.Marshal(hb.Tailnet)
		if err != nil {
			return
		}
		raw, deviceID = b, hb.Tailnet.DeviceID
	}
	prev, err := h.q.RecordServerTailnetHeartbeat(ctx, db.RecordServerTailnetHeartbeatParams{ID: c.serverID, Status: raw, DeviceID: deviceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return // unchanged and fresh
	}
	if err != nil {
		c.log.ErrorContext(ctx, "record tailnet status", "err", err)
		return
	}
	var before agentprotocol.TailnetStatus
	if len(prev) > 0 {
		_ = json.Unmarshal(prev, &before)
	}
	var after agentprotocol.TailnetStatus
	if hb.Tailnet != nil {
		after = *hb.Tailnet
	}
	if before.State != after.State || before.FQDN != after.FQDN || before.Message != after.Message || before.Mode != after.Mode {
		h.publishServer(c.serverID)
	}
}
