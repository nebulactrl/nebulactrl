// Package patching runs the host_patching commands on the host the agent
// manages: checking for pending updates, upgrading packages and scheduling a
// reboot. The programs and parsers live in internal/hostpatch; everything runs
// through a hardening.Runner (the agent's own user when it is installed on the
// host, a privileged nsenter helper container when it runs in a container).
package patching

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/internal/hostpatch"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Timing of an upgrade. The poll interval and error tolerance are variables so
// tests do not wait.
var (
	// PollInterval is how often the agent asks the host whether the detached
	// upgrade finished.
	PollInterval = 5 * time.Second
	// RunnerErrorTolerance is how long the agent keeps trying when the host cannot
	// be reached while an upgrade runs: upgrading Docker restarts the daemon, and
	// with it the helper container's endpoint, for a minute or two.
	RunnerErrorTolerance = 15 * time.Minute
)

// Service performs the host_patching actions through a Runner.
type Service struct {
	runner hardening.Runner
	log    *slog.Logger
	// sleep waits d or until ctx ends; replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

// New returns a Service.
func New(r hardening.Runner, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{runner: r, log: log.With("component", "patching"), sleep: sleepCtx, now: time.Now}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Handle runs one host_patching command.
func (s *Service) Handle(ctx context.Context, p agentprotocol.PatchingPayload) (agentprotocol.PatchingResult, error) {
	res := agentprotocol.PatchingResult{Action: p.Action, Mode: s.runner.Mode()}
	switch p.Action {
	case agentprotocol.PatchingActionCheck:
		st, err := s.check(ctx)
		if err != nil {
			return res, err
		}
		res.State = st
	case agentprotocol.PatchingActionApply:
		return s.apply(ctx, p, res)
	case agentprotocol.PatchingActionReboot:
		return s.reboot(ctx, res)
	default:
		return res, fmt.Errorf("unknown patching action %q", p.Action)
	}
	return res, nil
}

func (s *Service) check(ctx context.Context) (agentprotocol.PatchState, error) {
	out, err := s.runner.Run(ctx, hostpatch.CheckScript)
	if err != nil {
		return agentprotocol.PatchState{}, fmt.Errorf("check for updates: %w", err)
	}
	if out.ExitCode != 0 {
		return agentprotocol.PatchState{}, fmt.Errorf("check for updates: %s", tailLine(out))
	}
	st, err := hostpatch.ParseCheck(out.Stdout)
	if err != nil {
		return agentprotocol.PatchState{}, fmt.Errorf("check for updates: %w", err)
	}
	return st, nil
}

func (s *Service) reboot(ctx context.Context, res agentprotocol.PatchingResult) (agentprotocol.PatchingResult, error) {
	out, err := s.runner.Run(ctx, hostpatch.RebootScript)
	if err != nil {
		return res, fmt.Errorf("schedule reboot: %w", err)
	}
	bootID, how, err := hostpatch.ParseReboot(out.Stdout)
	if err != nil {
		return res, fmt.Errorf("schedule reboot: %w", err)
	}
	res.State.BootID = bootID
	res.Message = "Reboot scheduled (" + how + ")."
	s.log.Info("host reboot scheduled", "how", how)
	return res, nil
}

// apply starts the upgrade detached on the host and waits for it, then checks
// again so the new state is known. A start that finds the run already
// started (the agent restarted mid-upgrade) attaches to it.
func (s *Service) apply(ctx context.Context, p agentprotocol.PatchingPayload, res agentprotocol.PatchingResult) (agentprotocol.PatchingResult, error) {
	before, err := s.check(ctx)
	if err != nil {
		return res, err
	}
	if !before.Supported {
		return res, errors.New(before.Message)
	}
	start, err := hostpatch.StartScript(p.RunID, before.Manager, p.SecurityOnly)
	if err != nil {
		return res, err
	}
	out, err := s.runner.Run(ctx, start)
	if err != nil {
		return res, fmt.Errorf("start the upgrade: %w", err)
	}
	st, err := hostpatch.ParseStart(out.Stdout)
	if err != nil {
		return res, fmt.Errorf("start the upgrade: %w", err)
	}
	s.log.Info("host upgrade", "run_id", p.RunID, "manager", before.Manager, "security_only", p.SecurityOnly, "start", st)

	status, err := s.wait(ctx, p.RunID)
	if err != nil {
		return res, err
	}
	res.Log = status.Log
	res.ExitCode = status.ExitCode
	switch status.State {
	case hostpatch.UpgradeLost:
		res.ExitCode = -1
		res.Message = "The upgrade stopped without finishing (the host may have rebooted or the process was killed). Run it again: apt and dnf resume where they stopped."
	case hostpatch.UpgradeNone:
		res.ExitCode = -1
		res.Message = "The host has no record of this upgrade."
	case hostpatch.UpgradeFinished:
		if status.ExitCode != 0 {
			res.Message = fmt.Sprintf("The package manager exited with status %d.", status.ExitCode)
		}
	}
	// The follow-up check never fails the command: the upgrade ran, and that is
	// what must be reported.
	if after, err := s.check(ctx); err != nil {
		s.log.Warn("follow-up update check failed", "err", err)
		res.State = before
		if res.Message == "" {
			res.Message = "The upgrade finished, but the follow-up check failed: " + err.Error()
		}
	} else {
		res.State = after
	}
	return res, nil
}

// wait polls the host until the detached upgrade ends. A runner that cannot
// reach the host (Docker restarting) is retried for RunnerErrorTolerance.
func (s *Service) wait(ctx context.Context, runID string) (hostpatch.UpgradeStatus, error) {
	script, err := hostpatch.StatusScript(runID)
	if err != nil {
		return hostpatch.UpgradeStatus{}, err
	}
	var firstErr time.Time
	for {
		out, err := s.runner.Run(ctx, script)
		var st hostpatch.UpgradeStatus
		if err == nil {
			st, err = hostpatch.ParseStatus(out.Stdout)
		}
		if err == nil {
			firstErr = time.Time{}
			if st.State != hostpatch.UpgradeRunning {
				return st, nil
			}
		} else {
			if ctx.Err() != nil {
				return hostpatch.UpgradeStatus{}, fmt.Errorf("wait for the upgrade: %w", ctx.Err())
			}
			if firstErr.IsZero() {
				firstErr = s.now()
			}
			s.log.Warn("host unreachable while upgrading; retrying", "err", err)
			if s.now().Sub(firstErr) > RunnerErrorTolerance {
				return hostpatch.UpgradeStatus{}, fmt.Errorf("lost contact with the host during the upgrade: %w", err)
			}
		}
		if err := s.sleep(ctx, PollInterval); err != nil {
			return hostpatch.UpgradeStatus{}, fmt.Errorf("wait for the upgrade: %w", err)
		}
	}
}

// tailLine is the last line the script printed, for error messages.
func tailLine(o hardening.Output) string {
	text := strings.TrimSpace(o.Stderr)
	if text == "" {
		text = strings.TrimSpace(o.Stdout)
	}
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = text[i+1:]
	}
	if text == "" {
		text = fmt.Sprintf("no output, exit %d", o.ExitCode)
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
