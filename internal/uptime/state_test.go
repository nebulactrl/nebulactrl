package uptime

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// run feeds results to Step one every 30 s and records the actions.
type run struct {
	t    *testing.T
	st   State
	set  Settings
	ctx  Context
	now  time.Time
	acts []Action
}

func newRun(t *testing.T) *run {
	return &run{t: t, st: State{State: StateUnknown, Since: t0}, set: DefaultSettings(), now: t0}
}

func (r *run) feed(outcome string, opts ...func(*agentprotocol.ProbeResult)) []Action {
	r.now = r.now.Add(agentprotocol.UptimeInterval)
	res := agentprotocol.ProbeResult{ServiceID: "s", At: r.now, Outcome: outcome, Replicas: 1}
	switch outcome {
	case agentprotocol.ProbeUp:
		res.Passed = 1
	case agentprotocol.ProbeDown:
		res.Error = "HTTP 503"
	}
	for _, o := range opts {
		o(&res)
	}
	var acts []Action
	r.st, acts = Step(r.st, res, r.set, r.ctx)
	r.acts = append(r.acts, acts...)
	return acts
}

func (r *run) times(n int, outcome string) {
	for range n {
		r.feed(outcome)
	}
}

func kinds(as []Action) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Kind)
	}
	return out
}

func TestFirstPassingCheckIsUpAtOnce(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateUp, r.st.State)
	require.Equal(t, t0.Add(30*time.Second), r.st.Since)
	require.Empty(t, r.acts)
}

func TestFailureThresholdOpensOneIncidentAndAlerts(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)

	// Two failures: debounced, still up.
	require.Empty(t, r.feed(agentprotocol.ProbeDown))
	require.Empty(t, r.feed(agentprotocol.ProbeDown))
	require.Equal(t, StateUp, r.st.State)
	require.Equal(t, 2, r.st.Failures)

	acts := r.feed(agentprotocol.ProbeDown)
	require.Equal(t, []Action{{Kind: ActOpen, Cause: "HTTP 503", Notify: true}}, acts)
	require.Equal(t, StateDown, r.st.State)
	require.True(t, r.st.IncidentOpen)
	require.True(t, r.st.IncidentNotified)

	// Further failures neither reopen nor repeat.
	r.times(10, agentprotocol.ProbeDown)
	require.Equal(t, []string{ActOpen}, kinds(r.acts))
	require.Equal(t, StateDown, r.st.State)
}

func TestAPassingCheckResetsTheFailureStreak(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.times(2, agentprotocol.ProbeDown)
	r.feed(agentprotocol.ProbeUp)
	r.times(2, agentprotocol.ProbeDown)
	require.Empty(t, r.acts, "failures must be consecutive")
	require.Equal(t, StateUp, r.st.State)
}

func TestConfiguredThresholds(t *testing.T) {
	r := newRun(t)
	r.set.FailureThreshold, r.set.RecoveryThreshold = 1, 4
	r.feed(agentprotocol.ProbeUp)
	require.Equal(t, []string{ActOpen}, kinds(r.feed(agentprotocol.ProbeDown)))

	r.times(3, agentprotocol.ProbeUp)
	require.Equal(t, StateDown, r.st.State, "three of four passing checks are not enough")
	require.Equal(t, []string{ActResolve}, kinds(r.feed(agentprotocol.ProbeUp)))
	require.Equal(t, StateUp, r.st.State)
}

func TestDegradedShowsAtOnceAndNeverAlerts(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.feed(agentprotocol.ProbeDegraded, func(p *agentprotocol.ProbeResult) {
		p.Passed, p.Replicas, p.Error = 1, 2, "replica 2: connection refused"
	})
	require.Equal(t, StateDegraded, r.st.State)
	require.Equal(t, "replica 2: connection refused", r.st.LastError)
	r.times(20, agentprotocol.ProbeDegraded)
	require.Empty(t, r.acts)
	require.Equal(t, StateDegraded, r.st.State)
	require.Zero(t, r.st.Failures)

	r.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateUp, r.st.State)
	require.Empty(t, r.st.LastError)
}

func TestRecoveryNeedsConsecutivePassesAndAnnouncesOnlyWhatWasAnnounced(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.times(3, agentprotocol.ProbeDown)
	require.Equal(t, StateDown, r.st.State)

	r.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateDown, r.st.State, "one pass is not a recovery")
	r.feed(agentprotocol.ProbeDown) // breaks the streak
	r.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateDown, r.st.State)

	acts := r.feed(agentprotocol.ProbeUp)
	require.Equal(t, []Action{{Kind: ActResolve, Notify: true}}, acts)
	require.Equal(t, StateUp, r.st.State)
	require.False(t, r.st.IncidentOpen)

	// A degraded pass recovers to degraded.
	r2 := newRun(t)
	r2.times(3, agentprotocol.ProbeDown)
	require.Equal(t, StateDown, r2.st.State)
	r2.times(2, agentprotocol.ProbeDegraded)
	require.Equal(t, StateDegraded, r2.st.State)
	require.Equal(t, []string{ActOpen, ActResolve}, kinds(r2.acts))
}

func TestAlertsOffRecordsTheIncidentWithoutNotifications(t *testing.T) {
	r := newRun(t)
	r.set.Alerts = false
	r.times(3, agentprotocol.ProbeDown)
	require.Equal(t, []Action{{Kind: ActOpen, Cause: "HTTP 503", Notify: false}}, r.acts)
	r.times(2, agentprotocol.ProbeUp)
	require.Equal(t, []Action{{Kind: ActOpen, Cause: "HTTP 503"}, {Kind: ActResolve}}, r.acts)
}

func TestSwitchingAlertsOffMidIncidentStillClosesTheAlertThatWasSent(t *testing.T) {
	r := newRun(t)
	r.times(3, agentprotocol.ProbeDown)
	r.set.Alerts = false
	r.times(2, agentprotocol.ProbeUp)
	require.Equal(t, Action{Kind: ActResolve, Notify: true}, r.acts[1], "the down alert went out, so the up alert must follow")
}

func TestFailuresDuringADeploymentAreNotAnOutage(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.ctx.InDeploy = true
	r.times(20, agentprotocol.ProbeDown)
	require.Empty(t, r.acts)
	require.Equal(t, StateUp, r.st.State)
	require.Zero(t, r.st.Failures)
	require.Equal(t, "HTTP 503", r.st.LastError, "the failure is still recorded")

	// The deployment ends with the service still failing: the threshold starts from scratch.
	r.ctx.InDeploy = false
	r.times(2, agentprotocol.ProbeDown)
	require.Empty(t, r.acts)
	r.feed(agentprotocol.ProbeDown)
	require.Equal(t, []string{ActOpen}, kinds(r.acts))
}

func TestADeploymentDoesNotHideAnOutageThatAlreadyOpen(t *testing.T) {
	r := newRun(t)
	r.times(3, agentprotocol.ProbeDown)
	r.ctx.InDeploy = true
	r.times(2, agentprotocol.ProbeUp)
	require.Equal(t, []string{ActOpen, ActResolve}, kinds(r.acts), "the new release fixed it")
}

func TestMaintenanceIsNotJudgedAndNeverAlerts(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.ctx.InMaintenance = true
	r.times(20, agentprotocol.ProbeDown)
	require.Empty(t, r.acts)
	require.Equal(t, StateMaintenance, r.st.State)
	require.Zero(t, r.st.Failures)

	// After maintenance a failing service gets no verdict before the threshold.
	r.ctx.InMaintenance = false
	r.feed(agentprotocol.ProbeDown)
	require.Equal(t, StateUnknown, r.st.State)
	r.times(2, agentprotocol.ProbeDown)
	require.Equal(t, []string{ActOpen}, kinds(r.acts))

	// And a passing service is up at once.
	r2 := newRun(t)
	r2.ctx.InMaintenance = true
	r2.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateMaintenance, r2.st.State)
	r2.ctx.InMaintenance = false
	r2.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateUp, r2.st.State)
}

func TestAnOpenIncidentStaysOpenThroughMaintenanceAndResolvesNormally(t *testing.T) {
	r := newRun(t)
	r.times(3, agentprotocol.ProbeDown)
	r.ctx.InMaintenance = true
	r.times(5, agentprotocol.ProbeDown)
	require.Equal(t, StateMaintenance, r.st.State)
	require.Equal(t, []string{ActOpen}, kinds(r.acts), "no second incident")
	require.True(t, r.st.IncidentOpen)

	r.times(2, agentprotocol.ProbeUp)
	require.Equal(t, []string{ActOpen, ActResolve}, kinds(r.acts))
	require.Equal(t, StateMaintenance, r.st.State, "still in maintenance")

	// An incident that is still open when maintenance ends shows as down again.
	r2 := newRun(t)
	r2.times(3, agentprotocol.ProbeDown)
	r2.ctx.InMaintenance = true
	r2.feed(agentprotocol.ProbeDown)
	r2.ctx.InMaintenance = false
	r2.feed(agentprotocol.ProbeDown)
	require.Equal(t, StateDown, r2.st.State)
}

func TestNoIncidentOpensInMaintenanceEvenAfterManyFailures(t *testing.T) {
	r := newRun(t)
	r.ctx.InMaintenance = true
	r.times(50, agentprotocol.ProbeDown)
	require.Empty(t, r.acts)
	require.False(t, r.st.IncidentOpen)
}

func TestPausedOutcomeIsNotACheckAndClosesTheIncidentSilently(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.times(3, agentprotocol.ProbeDown)
	r.feed(agentprotocol.ProbePaused, func(p *agentprotocol.ProbeResult) { p.Replicas = 0 })
	require.Equal(t, StatePaused, r.st.State)
	require.Equal(t, []Action{{Kind: ActOpen, Cause: "HTTP 503", Notify: true}, {Kind: ActResolve, Notify: false}}, r.acts)
	require.False(t, r.st.IncidentOpen)
	require.Zero(t, r.st.Failures)
	require.NotNil(t, r.st.LastCheckAt, "the result is remembered so a resend is recognised")

	// Back to running: a first pass is up at once.
	r.feed(agentprotocol.ProbeUp)
	require.Equal(t, StateUp, r.st.State)
}

func TestDisabledMonitorPauses(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.set.Enabled = false
	r.feed(agentprotocol.ProbeDown)
	require.Equal(t, StatePaused, r.st.State)
	require.Empty(t, r.acts)
}

func TestExpireAndResume(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp)
	r.times(2, agentprotocol.ProbeDown)
	exp := Expire(r.st, r.now.Add(3*time.Minute))
	require.Equal(t, StateUnknown, exp.State)
	require.Equal(t, r.now.Add(3*time.Minute), exp.Since)
	require.Zero(t, exp.Failures, "the streak does not span an agent outage")

	// An outage that was open stays open while nothing is known; reports resume it.
	r2 := newRun(t)
	r2.times(3, agentprotocol.ProbeDown)
	r2.st = Expire(r2.st, r2.now.Add(5*time.Minute))
	require.True(t, r2.st.IncidentOpen)
	r2.now = r2.now.Add(5 * time.Minute)
	r2.feed(agentprotocol.ProbeDown)
	require.Equal(t, StateDown, r2.st.State)
	require.Equal(t, []string{ActOpen}, kinds(r2.acts))

	paused, _ := Pause(State{State: StateUp, Since: t0}, t0)
	require.Equal(t, StateUnknown, Resume(paused, t0.Add(time.Hour)).State)
	require.Equal(t, StateUp, Resume(State{State: StateUp, Since: t0}, t0).State, "only a paused service resumes")
}

func TestStaleAfterIsThreeCyclesPlusMargin(t *testing.T) {
	require.Equal(t, 120*time.Second, StaleAfter)
}

func TestLatencyAndErrorBookkeeping(t *testing.T) {
	r := newRun(t)
	r.feed(agentprotocol.ProbeUp, func(p *agentprotocol.ProbeResult) { p.LatencyMs = 42 })
	require.NotNil(t, r.st.LastLatencyMs)
	require.Equal(t, 42, *r.st.LastLatencyMs)
	require.NotNil(t, r.st.LastOKAt)
	ok := *r.st.LastOKAt

	r.feed(agentprotocol.ProbeDown)
	require.Nil(t, r.st.LastLatencyMs)
	require.Equal(t, ok, *r.st.LastOKAt, "last ok is the last pass")
}

func TestCauseIsOneLineAndBounded(t *testing.T) {
	require.Equal(t, "no replica passed its check", Cause(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDown}))
	require.Equal(t, "some replicas failed their check", Cause(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDegraded}))
	require.Equal(t, "unhealthy: a b c", Cause(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDown, Error: "unhealthy:\n a\tb \n c  "}))
	long := Cause(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDown, Error: strings.Repeat("é", 400)})
	require.LessOrEqual(t, len(long), maxCause)
	require.True(t, utf8.ValidString(long), "never cut inside a rune")
}

func TestAvailabilityMath(t *testing.T) {
	pct, ok := Availability(100, 90, 5, 0)
	require.True(t, ok)
	require.InDelta(t, 95.0, pct, 1e-9)

	// Maintenance leaves both sides of the fraction.
	pct, ok = Availability(100, 80, 0, 20)
	require.True(t, ok)
	require.InDelta(t, 100.0, pct, 1e-9)

	pct, ok = Availability(100, 40, 0, 20)
	require.True(t, ok)
	require.InDelta(t, 50.0, pct, 1e-9)

	_, ok = Availability(0, 0, 0, 0)
	require.False(t, ok, "no checks is no data, not 100 %")
	_, ok = Availability(10, 0, 0, 10)
	require.False(t, ok, "only maintenance is no data")
}
