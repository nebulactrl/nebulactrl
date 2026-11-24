package engine

import (
	"encoding/json"
	"errors"

	"github.com/nebulactrl/nebulactrl/internal/agent/autoban"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// AutoBanService is the part of the auto-ban engine the command handler uses.
type AutoBanService interface {
	Unban(serviceID, banID string) error
}

// Messages of an autoban_unban result (ResultFrame.Data is a DeployResult
// whose Message carries it). Both outcomes succeed: a ban that is not active
// anymore (it expired, or was lifted meanwhile) is as unbanned as it can be.
const (
	AutoBanUnbanned = "unbanned"
	AutoBanNotFound = "not_found"
)

// handleAutoBanUnban ends an active ban. The ban's end event reaches the
// control plane through the engine's own outbox.
func (e *Engine) handleAutoBanUnban(cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.AutoBanUnbanPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid autoban_unban payload: "+err.Error())
	}
	if p.ServiceID == "" || p.BanID == "" {
		return failedResult(cmd.ID, "autoban_unban needs serviceId and banId")
	}
	if e.cfg.AutoBan == nil {
		return failedResult(cmd.ID, "auto-ban is not available on this agent")
	}
	switch err := e.cfg.AutoBan.Unban(p.ServiceID, p.BanID); {
	case err == nil:
		return okResult(cmd.ID, "", AutoBanUnbanned)
	case errors.Is(err, autoban.ErrNotFound):
		return okResult(cmd.ID, "", AutoBanNotFound)
	default:
		return failedResult(cmd.ID, err.Error())
	}
}
