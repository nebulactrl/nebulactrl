package patching

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// step is one scripted answer of the fake runner: it is used when the script it
// is asked to run contains match.
type step struct {
	match string
	out   string
	err   error
	exit  int
}

type fakeRunner struct {
	steps []step
	ran   []string
}

func (f *fakeRunner) Mode() string { return hardening.ModeHelper }

func (f *fakeRunner) Run(_ context.Context, script string) (hardening.Output, error) {
	f.ran = append(f.ran, script)
	for i, st := range f.steps {
		if strings.Contains(script, st.match) {
			// A step answers once, except the last matching one, which repeats.
			rest := f.steps[i+1:]
			more := false
			for _, r := range rest {
				if r.match == st.match {
					more = true
				}
			}
			if more {
				f.steps = append(append([]step{}, f.steps[:i]...), f.steps[i+1:]...)
			}
			if st.err != nil {
				return hardening.Output{}, st.err
			}
			return hardening.Output{Stdout: st.out, ExitCode: st.exit}, nil
		}
	}
	return hardening.Output{}, errors.New("unexpected script: " + script[:min(80, len(script))])
}

const (
	checkMarker  = "@@MANAGER apt" // CheckScript prints this literal text
	statusMarker = `.rc"`          // StatusScript reads $D/$ID.rc
	startMarker  = "NEBULACTRL_EOF"
	rebootMarker = "nebulactrl-reboot"
)

func checkOut(updates, boot string, reboot bool) string {
	r := "0"
	if reboot {
		r = "1"
	}
	return "@@BOOTID " + boot + "\n@@MANAGER apt\n@@REFRESH 0\n@@UPDATES\n" + updates + "@@UPDATESRC 0\n@@REBOOT " + r + "\n@@END\n"
}

const twoUpdates = "Inst libssl3 [1] (2 Ubuntu:22.04/jammy-security [amd64])\nInst vim [1] (2 Ubuntu:22.04/jammy-updates [amd64])\n"

func newSvc(r hardening.Runner) *Service {
	s := New(r, nil)
	s.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return s
}

func TestCheck(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: checkMarker, out: checkOut(twoUpdates, "boot-1", true)}}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "r1", Action: agentprotocol.PatchingActionCheck})
	require.NoError(t, err)
	require.Equal(t, hardening.ModeHelper, res.Mode)
	require.Equal(t, 2, res.State.Updates)
	require.Equal(t, 1, res.State.Security)
	require.True(t, res.State.RebootRequired)
	require.Equal(t, "boot-1", res.State.BootID)
	require.Len(t, r.ran, 1, "a check runs one script and changes nothing")
}

func TestCheckHostFailure(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: checkMarker, err: errors.New("docker unreachable")}}}
	_, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{Action: agentprotocol.PatchingActionCheck})
	require.ErrorContains(t, err, "docker unreachable")

	r = &fakeRunner{steps: []step{{match: checkMarker, out: "@@BOOTID b\n@@MANAGER apt\n"}}}
	_, err = newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{Action: agentprotocol.PatchingActionCheck})
	require.ErrorContains(t, err, "cut short")
}

func TestApplyWaitsForTheDetachedUpgrade(t *testing.T) {
	runID := "0b1c2d3e-aaaa-bbbb-cccc-0123456789ab"
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "boot-1", false)}, // before
		{match: checkMarker, out: checkOut("", "boot-1", true)},          // after
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@RUNNING\n@@LOG\nUnpacking\n"},
		{match: statusMarker, out: "@@RUNNING\n@@LOG\nSetting up\n"},
		{match: statusMarker, out: "@@RC 0\n@@LOG\nSetting up libssl3\nDone\n"},
	}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: runID, Action: agentprotocol.PatchingActionApply})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode)
	require.Contains(t, res.Log, "Setting up libssl3")
	require.Zero(t, res.State.Updates, "the state is the one after the upgrade")
	require.True(t, res.State.RebootRequired)
	require.Empty(t, res.Message)

	var start string
	statusCalls := 0
	for _, s := range r.ran {
		if strings.Contains(s, startMarker) {
			start = s
		}
		if strings.Contains(s, statusMarker) && !strings.Contains(s, startMarker) {
			statusCalls++
		}
	}
	require.Contains(t, start, runID, "the run id names the state files on the host")
	require.Contains(t, start, "apt-get", "the manager comes from the check")
	require.Equal(t, 3, statusCalls, "polled until the exit code appeared")
}

func TestApplySecurityOnly(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@RC 0\n@@LOG\nok\n"},
	}}
	_, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply, SecurityOnly: true})
	require.NoError(t, err)
	var start string
	for _, s := range r.ran {
		if strings.Contains(s, startMarker) {
			start = s
		}
	}
	require.Contains(t, start, "--only-upgrade")
}

func TestApplyFailureIsReportedWithTheLog(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@RC 100\n@@LOG\nE: Sub-process /usr/bin/dpkg returned an error code (1)\n"},
	}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.NoError(t, err, "a failed upgrade is a result, not a failed command")
	require.Equal(t, 100, res.ExitCode)
	require.Contains(t, res.Log, "dpkg returned an error")
	require.Contains(t, res.Message, "status 100")
	require.Equal(t, 2, res.State.Updates, "the follow-up check still reports what is pending")
}

func TestApplyAttachesToARunningUpgrade(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut("", "b", false)},
		{match: startMarker, out: "@@RUNNING\n"}, // the agent restarted: the host already runs it
		{match: statusMarker, out: "@@RC 0\n@@LOG\ndone\n"},
	}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode)
}

func TestApplyToleratesTheHostGoingAwayWhileDockerRestarts(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut("", "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, err: errors.New("cannot connect to the Docker daemon")},
		{match: statusMarker, err: errors.New("cannot connect to the Docker daemon")},
		{match: statusMarker, out: "@@RC 0\n@@LOG\nfinished\n"},
	}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.NoError(t, err)
	require.Contains(t, res.Log, "finished")
}

func TestApplyGivesUpWhenTheHostStaysUnreachable(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, err: errors.New("cannot connect to the Docker daemon")},
	}}
	s := newSvc(r)
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	s.sleep = func(ctx context.Context, d time.Duration) error { now = now.Add(5 * time.Minute); return ctx.Err() }
	_, err := s.Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.ErrorContains(t, err, "lost contact with the host")
}

func TestApplyLostUpgrade(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@LOST\n@@LOG\npartial output\n"},
	}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode, "an upgrade that vanished is a failure")
	require.Contains(t, res.Message, "stopped without finishing")
	require.Contains(t, res.Log, "partial output")
}

func TestApplyRefusesAnUnsupportedHost(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: checkMarker, out: "@@BOOTID b\n@@MANAGER none\n@@END\n"}}}
	// The apt marker is not in a "none" answer, so route any check script to it.
	r.steps[0].match = "@@BOOTID"
	_, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.ErrorContains(t, err, "No supported package manager")
}

func TestApplyCancelStopsWaiting(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@RUNNING\n@@LOG\n"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	s := New(r, nil)
	s.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	_, err := s.Handle(ctx, agentprotocol.PatchingPayload{RunID: "0b1c2d3e-aaaa", Action: agentprotocol.PatchingActionApply})
	require.ErrorIs(t, err, context.Canceled)
}

func TestApplyRejectsAHostileRunID(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: checkMarker, out: checkOut(twoUpdates, "b", false)}}}
	_, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "x; reboot", Action: agentprotocol.PatchingActionApply})
	require.ErrorContains(t, err, "invalid run id")
	for _, s := range r.ran {
		require.NotContains(t, s, "x; reboot")
	}
}

func TestReboot(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: rebootMarker, out: "@@BOOTID boot-1\n@@SCHEDULED systemd\n"}}}
	res, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "r", Action: agentprotocol.PatchingActionReboot})
	require.NoError(t, err)
	require.Equal(t, "boot-1", res.State.BootID, "the boot id the reboot will replace")
	require.Contains(t, res.Message, "scheduled")

	r = &fakeRunner{steps: []step{{match: rebootMarker, out: "@@BOOTID boot-1\n@@FAILED no way to schedule a reboot\n"}}}
	_, err = newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: "r", Action: agentprotocol.PatchingActionReboot})
	require.ErrorContains(t, err, "no way to schedule")
}

func TestUnknownAction(t *testing.T) {
	_, err := newSvc(&fakeRunner{}).Handle(context.Background(), agentprotocol.PatchingPayload{Action: "nuke"})
	require.ErrorContains(t, err, "unknown patching action")
}
