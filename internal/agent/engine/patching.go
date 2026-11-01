package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/patching"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// patchingTimeout bounds a host_patching command: the package lists refresh, the
// upgrade itself (kernel and Docker packages take a while on a small host) and
// the follow-up check.
const patchingTimeout = 90 * time.Minute

// patchingLockKey serializes host_patching commands; no service has this ID.
const patchingLockKey = "\x00host-patching"

// handleHostPatching checks the host for pending updates, upgrades its packages
// or schedules its reboot (see package patching). It reaches the host the way
// host hardening does and carries no secrets.
func (e *Engine) handleHostPatching(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.PatchingPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid host_patching payload: "+err.Error())
	}
	runner, err := newHardeningRunner(ctx, e)
	if err != nil {
		return failedResult(cmd.ID, "host patching: "+err.Error())
	}
	unlock := e.lockService(patchingLockKey)
	defer unlock()
	pctx, cancel := context.WithTimeout(ctx, patchingTimeout)
	defer cancel()

	res, err := patching.New(runner, e.log).Handle(pctx, p)
	if err != nil {
		return failedResult(cmd.ID, "host patching "+p.Action+": "+err.Error())
	}
	e.log.Info("host patching finished", "run_id", p.RunID, "action", p.Action, "mode", res.Mode,
		"updates", res.State.Updates, "security", res.State.Security, "reboot_required", res.State.RebootRequired, "exit", res.ExitCode)
	data, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}
