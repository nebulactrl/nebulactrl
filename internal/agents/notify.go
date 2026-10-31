package agents

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SetNotifier installs where server alerts go (offline, online, disk). Without
// one they are not announced.
func (h *Hub) SetNotifier(n notifications.Emitter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifier = n
}

func (h *Hub) emitter() notifications.Emitter {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.notifier
}

// notifyOffline announces a server that stopped sending heartbeats.
func (h *Hub) notifyOffline(ctx context.Context, serverID uuid.UUID) {
	n := h.emitter()
	if n == nil {
		return
	}
	notifications.Emit(ctx, n, notifications.Event{
		Type: notifications.TypeServerOffline, ServerID: &serverID, Severity: notifications.SeverityError,
		Title: "A server went offline",
		Body:  fmt.Sprintf("No heartbeat for more than %d seconds. Running containers keep running; deployments to it wait until it reconnects.", int(agentprotocol.OfflineAfter.Seconds())),
	})
}

// notifyOnline announces a server that connected (first time) or came back. A
// heartbeat from a server that was already online is not news.
func (h *Hub) notifyOnline(ctx context.Context, serverID uuid.UUID, name, prevStatus string) {
	n := h.emitter()
	if n == nil || prevStatus == "online" || prevStatus == "revoked" {
		return
	}
	title, body := name+" is back online", "The agent reconnected and is sending heartbeats again."
	if prevStatus == "pending" {
		title, body = name+" connected", "The agent enrolled and is sending heartbeats."
	}
	notifications.Emit(ctx, n, notifications.Event{
		Type: notifications.TypeServerOnline, ServerID: &serverID, Severity: notifications.SeverityInfo, Title: title, Body: body,
	})
}

// notifyDiskAlert announces a disk that crossed its alert threshold (once per crossing).
func (h *Hub) notifyDiskAlert(ctx context.Context, serverID uuid.UUID, usedPercent int, threshold int32, used, total uint64) {
	n := h.emitter()
	if n == nil {
		return
	}
	notifications.Emit(ctx, n, notifications.Event{
		Type: notifications.TypeServerDiskAlert, ServerID: &serverID, Severity: notifications.SeverityWarning,
		Title: fmt.Sprintf("Server disk is %d%% full", usedPercent),
		Body:  fmt.Sprintf("The root filesystem crossed the %d%% alert threshold. Builds and image pulls fail when it fills up.", threshold),
		Fields: map[string]string{
			"Disk used": fmt.Sprintf("%s of %s", humanBytes(used), humanBytes(total)),
			"Threshold": fmt.Sprintf("%d%%", threshold),
		},
	})
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
