package engine

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/decommission"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// decommissionTimeout bounds the removal of everything on the host.
const decommissionTimeout = 30 * time.Minute

// errDecommissioning is the answer to every command that arrives while the
// host is being emptied.
const errDecommissioning = "this server is being removed from NebulaCtrl"

// Decommissioning reports whether a decommission command is running or has
// emptied the host. It stays set after a success (the agent exits behind it)
// and is cleared again by a failed run.
func (e *Engine) Decommissioning() bool { return e.decom.Load() }

// handleDecommission removes every container, network and volume NebulaCtrl
// manages on the host (see package decommission). The agent's own removal
// (identity, unit, files) is the process's job, after the result is sent.
func (e *Engine) handleDecommission(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DecommissionPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid decommission payload: "+err.Error())
	}
	if e.cfg.BuildOnly {
		return failedResult(cmd.ID, "a build-only agent hosts nothing to remove")
	}
	d, ok := e.cfg.Docker.(decommission.Docker)
	if !ok {
		return failedResult(cmd.ID, "this agent's Docker backend cannot list volumes")
	}
	// From here on nothing recreates what is removed: the proxy's repair loop
	// stops, reconcile frames and new commands are refused.
	e.decom.Store(true)
	if e.cfg.StopProxy != nil {
		e.cfg.StopProxy()
	}

	cfg := decommission.Config{
		Docker: d, Logger: e.log, Self: e.selfContainer(ctx), IsManagedNetwork: isManagedNetwork,
	}
	if e.cfg.Compose != nil {
		cfg.ComposeDown = func(ctx context.Context, project string) error {
			return e.cfg.Compose.Down(ctx, compose.Project{Name: project}, compose.DownOptions{RemoveVolumes: true, Grace: decommission.DefaultGrace}, func(string) {})
		}
	}
	if e.cfg.DecommissionTune != nil {
		e.cfg.DecommissionTune(&cfg)
	}
	cctx, cancel := context.WithTimeout(ctx, decommissionTimeout)
	defer cancel()

	res, err := decommission.Run(cctx, cfg)
	if err != nil {
		e.decom.Store(false)
		return failedResult(cmd.ID, "decommission: "+err.Error())
	}
	frame := agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded}
	if len(res.Failed) > 0 {
		e.decom.Store(false)
		msgs := make([]string, 0, len(res.Failed))
		for _, s := range res.Failed {
			msgs = append(msgs, s.Kind+" "+s.Name+": "+s.Error)
		}
		frame.Outcome, frame.Error = agentprotocol.OutcomeFailed, res.Message+" "+strings.Join(msgs, "; ")
	}
	frame.Data, _ = json.Marshal(res)
	e.log.Info("decommission finished", "containers", res.Containers, "networks", res.Networks, "volumes", res.Volumes,
		"failed", len(res.Failed), "server_id", p.ServerID)
	return frame
}

// SelfContainer is the ID of the container the agent runs in, "" when it runs
// on the host. The process uses it to stop its restart policy before it exits
// for good.
func (e *Engine) SelfContainer(ctx context.Context) string { return e.selfContainer(ctx) }
