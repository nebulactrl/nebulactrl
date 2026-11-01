package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// hardeningTimeout bounds a host_hardening command: installing Lynis, an audit
// (a minute on a small host, several on a large one) and the re-audit.
const hardeningTimeout = 40 * time.Minute

// hardeningLockKey serializes host_hardening commands (and Lynis's own pid
// lock would make a second audit fail anyway); no service has this ID.
const hardeningLockKey = "\x00host-hardening"

// labelManagedHostCommand is the value of LabelManaged on the one-shot helper
// that runs a host command for an agent in a container.
const labelManagedHostCommand = "hostcmd"

// newHardeningRunner returns how this agent reaches the host. Tests replace it.
var newHardeningRunner = func(ctx context.Context, e *Engine) (hardening.Runner, error) {
	self := e.selfContainer(ctx)
	if self == "" {
		return hardening.DirectRunner{}, nil
	}
	runner, ok := e.cfg.Docker.(docker.HostCommandRunner)
	if !ok {
		return nil, errors.New("this agent's Docker engine cannot run a helper container")
	}
	info, err := e.cfg.Docker.ContainerInspect(ctx, self)
	if err != nil {
		return nil, fmt.Errorf("inspect the agent's own container: %w", err)
	}
	image := info.ImageID
	if image == "" {
		image = info.Image
	}
	if image == "" {
		return nil, errors.New("the agent's own image is unknown")
	}
	return &helperRunner{runner: runner, image: image}, nil
}

// helperRunner runs scripts on the host from a privileged helper container
// that enters PID 1's namespaces with nsenter (the host shell's mechanism).
type helperRunner struct {
	runner docker.HostCommandRunner
	image  string
}

func (*helperRunner) Mode() string { return hardening.ModeHelper }

func (h *helperRunner) Run(ctx context.Context, script string) (hardening.Output, error) {
	var b [4]byte
	_, _ = rand.Read(b[:])
	res, err := h.runner.RunHostCommand(ctx, docker.HostCommandSpec{
		Name:  "nebulactrl-hostcmd-" + hex.EncodeToString(b[:]),
		Image: h.image,
		Argv:  []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", hostShellSh, "-c", script},
		Labels: map[string]string{
			agentprotocol.LabelManaged: labelManagedHostCommand,
		},
	})
	if err != nil {
		return hardening.Output{}, err
	}
	return hardening.Output{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, Truncated: res.Truncated}, nil
}

// handleHostHardening runs a Lynis audit and the curated remediations on the
// host (see package hardening). It carries no secrets and never touches SSH,
// the firewall or accounts: the catalog has no fix for them.
func (e *Engine) handleHostHardening(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.HardeningPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid host_hardening payload: "+err.Error())
	}
	runner, err := newHardeningRunner(ctx, e)
	if err != nil {
		return failedResult(cmd.ID, "host hardening: "+err.Error())
	}
	unlock := e.lockService(hardeningLockKey)
	defer unlock()
	hctx, cancel := context.WithTimeout(ctx, hardeningTimeout)
	defer cancel()

	res, err := hardening.New(runner, e.log).Handle(hctx, p)
	if err != nil {
		return failedResult(cmd.ID, "host hardening "+p.Action+": "+err.Error())
	}
	e.log.Info("host hardening finished", "run_id", p.RunID, "action", p.Action, "mode", res.Mode,
		"lynis", res.LynisInstalled, "index", indexOf(res), "outcomes", len(res.Outcomes))
	data, _ := json.Marshal(res)
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}

func indexOf(r agentprotocol.HardeningResult) int {
	if r.Report == nil {
		return -1
	}
	return r.Report.HardeningIndex
}
