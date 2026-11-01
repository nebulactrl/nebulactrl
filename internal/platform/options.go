package platform

import (
	"encoding/json"
	"time"
)

// options are the non-secret choices of a platform service
// (platform_services.options).
type options struct {
	// KeyExpiresAt is when the Headscale API key stops working.
	KeyExpiresAt *time.Time `json:"keyExpiresAt,omitempty"`
	// ReplaceIntegration lets the Headscale wiring replace an existing Tailscale
	// integration (confirmed at set-up).
	ReplaceIntegration bool `json:"replaceIntegration,omitempty"`
	// StepSince is when the current step started (its deadline counts from it).
	StepSince *time.Time `json:"stepSince,omitempty"`
}

// wiring records what a platform service was connected to
// (platform_services.wiring), so removal can unwire it.
type wiring struct {
	// Type is tailscale_integration; empty until wired.
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// ServerID is the server the wiring test ran from.
	ServerID string `json:"serverId,omitempty"`
	// BackupDestinationID is the destination of the volume backup schedule of the
	// service's data.
	BackupDestinationID string `json:"backupDestinationId,omitempty"`
}

func decodeJSON[T any](raw []byte) T {
	var v T
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func encodeJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
