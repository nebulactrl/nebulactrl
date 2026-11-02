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

const applyRun = "0b1c2d3e-aaaa"

func apply(t *testing.T, r hardening.Runner) (agentprotocol.PatchingResult, error) {
	t.Helper()
	return newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{RunID: applyRun, Action: agentprotocol.PatchingActionApply})
}

func TestSleepCtxWaitsForTheTimerOrTheContext(t *testing.T) {
	require.NoError(t, sleepCtx(context.Background(), time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepCtx(ctx, time.Hour), context.Canceled)
}

func TestTailLine(t *testing.T) {
	long := strings.Repeat("x", 400)
	for name, tc := range map[string]struct {
		out  hardening.Output
		want string
	}{
		"stderr wins over stdout":   {hardening.Output{Stdout: "out", Stderr: "first\nlast error\n"}, "last error"},
		"stdout when stderr empty":  {hardening.Output{Stdout: "a\nb\n", Stderr: "  \n"}, "b"},
		"single line":               {hardening.Output{Stderr: "boom"}, "boom"},
		"truncated at 300":          {hardening.Output{Stderr: long}, long[:300]},
		"nothing printed":           {hardening.Output{ExitCode: 7}, "no output, exit 7"},
		"trailing newlines ignored": {hardening.Output{Stderr: "oops\n\n\n"}, "oops"},
	} {
		require.Equal(t, tc.want, tailLine(tc.out), name)
	}
}

func TestCheckFailsWithTheScriptsLastLineWhenItExitsNonZero(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: checkMarker, out: "E: could not lock\n", exit: 100}}}
	_, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{Action: agentprotocol.PatchingActionCheck})
	require.ErrorContains(t, err, "check for updates: E: could not lock")
}

func TestApplyStopsWhenTheFirstCheckFails(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: checkMarker, err: errors.New("helper container failed")}}}
	_, err := apply(t, r)
	require.ErrorContains(t, err, "helper container failed")
	require.Len(t, r.ran, 1, "nothing is started on a host that cannot be checked")
}

func TestApplyReportsAStartThatFailsOrIsNotUnderstood(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, err: errors.New("exec failed")},
	}}
	_, err := apply(t, r)
	require.ErrorContains(t, err, "start the upgrade: exec failed")

	r = &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@FAILED another upgrade holds the lock\n"},
	}}
	_, err = apply(t, r)
	require.ErrorContains(t, err, "another upgrade holds the lock")

	r = &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "garbage\n"},
	}}
	_, err = apply(t, r)
	require.ErrorContains(t, err, "did not start the upgrade")
}

func TestApplyWithoutARecordOfTheUpgrade(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@NONE\n"},
	}}
	res, err := apply(t, r)
	require.NoError(t, err)
	require.Equal(t, -1, res.ExitCode)
	require.Contains(t, res.Message, "no record of this upgrade")
}

func TestApplyKeepsTheUpgradeResultWhenTheFollowUpCheckFails(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, err: errors.New("docker is restarting")},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@RC 0\n@@LOG\nall done\n"},
	}}
	res, err := apply(t, r)
	require.NoError(t, err, "the upgrade ran; a failed re-check must not turn it into a failed command")
	require.Contains(t, res.Log, "all done")
	require.Equal(t, 2, res.State.Updates, "the state from before the upgrade is reported")
	require.Contains(t, res.Message, "follow-up check failed: check for updates: docker is restarting")
}

func TestApplyFollowUpFailureDoesNotOverwriteAMoreSpecificMessage(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, err: errors.New("docker is restarting")},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "@@RC 1\n@@LOG\nfailed\n"},
	}}
	res, err := apply(t, r)
	require.NoError(t, err)
	require.Equal(t, "The package manager exited with status 1.", res.Message)
}

func TestApplyTreatsAnUnintelligibleStatusAsAnUnreachableHost(t *testing.T) {
	r := &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: checkMarker, out: checkOut("", "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
		{match: statusMarker, out: "static from a restarting daemon\n"},
		{match: statusMarker, out: "@@RC 0\n@@LOG\nok\n"},
	}}
	res, err := apply(t, r)
	require.NoError(t, err)
	require.Contains(t, res.Log, "ok")
}

func TestApplyEndsWhenTheContextIsCancelledWhileTheHostIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancellingRunner{fakeRunner: &fakeRunner{steps: []step{
		{match: checkMarker, out: checkOut(twoUpdates, "b", false)},
		{match: startMarker, out: "@@STARTED\n"},
	}}, cancel: cancel}
	_, err := newSvc(r).Handle(ctx, agentprotocol.PatchingPayload{RunID: applyRun, Action: agentprotocol.PatchingActionApply})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "wait for the upgrade")
}

// cancellingRunner cancels the context the first time the status script runs
// and fails it, like a daemon that went away because the agent is stopping.
type cancellingRunner struct {
	*fakeRunner
	cancel context.CancelFunc
}

func (c *cancellingRunner) Run(ctx context.Context, script string) (hardening.Output, error) {
	if strings.Contains(script, statusMarker) && !strings.Contains(script, startMarker) {
		c.cancel()
		return hardening.Output{}, errors.New("connection reset")
	}
	return c.fakeRunner.Run(ctx, script)
}

func TestRebootReportsRunnerAndParseFailures(t *testing.T) {
	r := &fakeRunner{steps: []step{{match: rebootMarker, err: errors.New("no helper")}}}
	_, err := newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{Action: agentprotocol.PatchingActionReboot})
	require.ErrorContains(t, err, "schedule reboot: no helper")

	r = &fakeRunner{steps: []step{{match: rebootMarker, out: "nothing useful\n"}}}
	_, err = newSvc(r).Handle(context.Background(), agentprotocol.PatchingPayload{Action: agentprotocol.PatchingActionReboot})
	require.ErrorContains(t, err, "schedule reboot")
}

func TestNewDefaultsTheLoggerAndTheRealClock(t *testing.T) {
	s := New(&fakeRunner{}, nil)
	require.NotNil(t, s.log)
	require.WithinDuration(t, time.Now(), s.now(), time.Minute)
	require.NoError(t, s.sleep(context.Background(), time.Millisecond))
}
