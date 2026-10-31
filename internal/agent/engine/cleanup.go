package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/cleanup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// CleanupTuner adjusts a cleanup's configuration (Config.CleanupTune).
type CleanupTuner func(*cleanup.Config)

// cleanupTimeout bounds a docker_cleanup command (a big image store and a
// build cache prune can take a while).
const cleanupTimeout = 45 * time.Minute

// cleanupLockKey serializes cleanups through the engine's per-key locks; no
// service has this ID.
const cleanupLockKey = "\x00docker-cleanup"

// handleDockerCleanup runs a cleanup (see package cleanup). It never prunes
// without the control plane's protected-image set, and never touches volumes
// outside the explicit volume mode.
func (e *Engine) handleDockerCleanup(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.CleanupPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid docker_cleanup payload: "+err.Error())
	}
	d, ok := e.cfg.Docker.(cleanup.Docker)
	if !ok {
		return failedResult(cmd.ID, "this agent's Docker backend cannot run a cleanup")
	}
	cfg := cleanup.Config{
		Docker: d, Logger: e.log, IsManagedNetwork: isManagedNetwork, NetworkMinAge: e.cfg.NetworkMinAge,
		Busy: e.builds.busy,
	}
	if e.cfg.Builder != nil {
		cfg.PruneBuildCache = func(ctx context.Context, older time.Duration) (int64, error) {
			return build.Prune(ctx, older, func(line string) { e.log.Debug("builder prune", "output", line) })
		}
	}
	if e.cfg.CleanupTune != nil {
		e.cfg.CleanupTune(&cfg)
	}
	unlock := e.lockService(cleanupLockKey)
	defer unlock()
	cctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()

	res, err := cleanup.New(cfg).Run(cctx, p)
	if err != nil {
		if errors.Is(err, cleanup.ErrNoProtectedSet) {
			return failedResult(cmd.ID, err.Error())
		}
		return failedResult(cmd.ID, "docker cleanup: "+err.Error())
	}
	frame := agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded}
	if cleanup.Failed(res) {
		var msgs []string
		for _, s := range res.Steps {
			if s.Error != "" {
				msgs = append(msgs, s.Name+": "+s.Error)
			}
		}
		res.Message = strings.Join(msgs, "; ")
		frame.Outcome, frame.Error = agentprotocol.OutcomeFailed, res.Message
	}
	frame.Data, _ = json.Marshal(res)
	e.log.Info("docker cleanup finished", "run_id", res.RunID, "trigger", res.Trigger, "mode", res.Mode,
		"items", res.Items, "reclaimed_bytes", res.ReclaimedBytes, "failed", frame.Outcome != agentprotocol.OutcomeSucceeded)
	return frame
}
