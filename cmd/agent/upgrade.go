package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/internal/agent/selfupgrade"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// errUpgradeRestart ends run() when the agent restarts to start a binary that
// was just installed (or restored after a failed upgrade).
var errUpgradeRestart = errors.New("restarting to apply an agent upgrade")

// exitUpgradeRestart is the process exit code for errUpgradeRestart: non-zero,
// so even a supervisor that only restarts on failure brings the agent back.
const exitUpgradeRestart = 75

const (
	// upgradeIdlePoll is how often the restart checks for running commands.
	upgradeIdlePoll = 2 * time.Second
	// upgradeIdleLimit bounds the wait for running commands (a build has 30 min).
	upgradeIdleLimit = 45 * time.Minute
	// upgradeFlush gives the result frame time to leave before the socket closes.
	upgradeFlush = 3 * time.Second
)

// selfHandler adds the commands that act on the agent itself to the engine's
// handler: agent_upgrade (replace the binary and restart) and decommission
// (the engine empties the host; the process then removes itself). It embeds
// the engine, so every optional interface the connection looks for (streams,
// result observer) is still implemented.
type selfHandler struct {
	*engine.Engine
	// up is nil when this agent cannot replace its own binary.
	up       *selfupgrade.Controller
	inflight func() int
	restart  func()
	// decommissioned is marked when a decommission emptied the host and ends
	// the run (run() then removes the agent).
	decommissioned *restartRequest
	log            *slog.Logger
	// idlePoll and flush default to upgradeIdlePoll and upgradeFlush.
	idlePoll, flush time.Duration
}

func (h *selfHandler) HandleCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	switch {
	case cmd.Kind == agentprotocol.KindAgentUpgrade && h.up != nil:
		return h.upgrade(ctx, cmd)
	case cmd.Kind == agentprotocol.KindDecommission && h.decommissioned != nil:
		res := h.Engine.HandleCommand(ctx, cmd)
		if res.Outcome == agentprotocol.OutcomeSucceeded {
			// Marked before the result leaves: whatever ends the connection
			// next (the control plane deleting the server closes it), the run
			// ends in the agent's removal, not in a restart.
			h.decommissioned.Mark()
		}
		return res
	}
	return h.Engine.HandleCommand(ctx, cmd)
}

func (h *selfHandler) upgrade(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	fail := func(msg string) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: msg}
	}
	var p agentprotocol.UpgradePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return fail("invalid agent_upgrade payload: " + err.Error())
	}
	res, err := h.up.Upgrade(ctx, p)
	if err != nil {
		h.log.Warn("agent upgrade failed", "to", p.Version, "err", err)
		return fail(err.Error())
	}
	data, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

// ResultSent restarts the agent behind a successful upgrade, once the other
// running commands are done, and ends the run behind a decommission (the
// agent removes itself; a failed decommission restarts the agent, which brings
// the proxy's repair loop back).
func (h *selfHandler) ResultSent(ctx context.Context, cmd agentprotocol.Command, res agentprotocol.ResultFrame) {
	switch {
	case cmd.Kind == agentprotocol.KindAgentUpgrade && h.up != nil:
		if res.Outcome == agentprotocol.OutcomeSucceeded {
			go h.restartWhenIdle(ctx)
		}
	case cmd.Kind == agentprotocol.KindDecommission && h.decommissioned != nil:
		flush := h.flush
		if flush == 0 {
			flush = upgradeFlush
		}
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(flush):
			}
			if res.Outcome == agentprotocol.OutcomeSucceeded {
				h.log.Info("host emptied; the agent removes itself now")
				h.decommissioned.Do()
				return
			}
			h.log.Warn("decommission failed; restarting the agent")
			h.restart()
		}()
	default:
		h.Engine.ResultSent(ctx, cmd, res)
	}
}

func (h *selfHandler) restartWhenIdle(ctx context.Context) {
	poll, flush := h.idlePoll, h.flush
	if poll == 0 {
		poll = upgradeIdlePoll
	}
	if flush == 0 {
		flush = upgradeFlush
	}
	deadline := time.Now().Add(upgradeIdleLimit)
	for h.inflight() > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(flush):
	}
	h.log.Info("restarting to run the upgraded agent binary")
	h.restart()
}

// restartRequest turns "end the run for a reason" (a restart to run an
// upgraded binary, the removal after a decommission) into a cancelled run
// context. requested says that the reason holds, whatever ended the run.
type restartRequest struct {
	requested atomic.Bool
	cancel    context.CancelFunc
}

// Mark records the reason without ending the run.
func (r *restartRequest) Mark() { r.requested.Store(true) }

// Do records the reason and ends the run.
func (r *restartRequest) Do() {
	r.Mark()
	r.cancel()
}
