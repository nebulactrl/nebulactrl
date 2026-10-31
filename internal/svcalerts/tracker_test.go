package svcalerts

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const every = 15 * time.Second // the agent reports every 15 seconds

// rig drives a Tracker with a clock and one replica.
type rig struct {
	t     *testing.T
	tr    *Tracker
	th    Settings
	now   time.Time
	dep   string
	cid   string
	count int
}

func newRig(t *testing.T) *rig {
	return &rig{t: t, tr: NewTracker(nil), th: DefaultSettings(), now: t0, dep: "dep-1", cid: "c1"}
}

// step advances the clock by every and feeds one single-replica observation.
func (r *rig) step(state, health string) []Action {
	r.now = r.now.Add(every)
	return r.feed(Observation{State: state, Health: health})
}

func (r *rig) feed(o Observation) []Action {
	o.At, o.DeploymentID = r.now, r.dep
	if o.Replicas == nil {
		o.Replicas = []Replica{{Key: "/0", ContainerID: r.cid, State: o.State, Health: o.Health, RestartCount: r.count}}
	}
	return r.tr.Step(o, r.th)
}

func (r *rig) ok() []Action { return r.step(agentprotocol.StateRunning, agentprotocol.HealthHealthy) }

func (r *rig) exited() []Action {
	return r.step(agentprotocol.StateExited, agentprotocol.HealthUnhealthy)
}

// restart records one Docker restart and reports the container running again.
func (r *rig) restart() []Action {
	r.count++
	return r.ok()
}

// until steps with f until d has passed and returns every action on the way.
func (r *rig) until(d time.Duration, f func() []Action) []Action {
	var out []Action
	end := r.now.Add(d)
	for r.now.Before(end) {
		out = append(out, f()...)
	}
	return out
}

func types(as []Action) []ActionType {
	out := make([]ActionType, len(as))
	for i, a := range as {
		out[i] = a.Type
	}
	return out
}

func notified(as []Action) []Action {
	var out []Action
	for _, a := range as {
		if a.Notify {
			out = append(out, a)
		}
	}
	return out
}

func TestHealthyServiceNeverAlerts(t *testing.T) {
	r := newRig(t)
	require.Empty(t, r.until(time.Hour, r.ok))
	require.Nil(t, r.tr.Open())
}

func TestCrashIsAnnouncedOnceAfterTheGracePeriod(t *testing.T) {
	r := newRig(t)
	r.ok()
	require.Empty(t, r.exited(), "first sighting starts the timer")
	require.Empty(t, r.exited(), "15s down")
	acts := r.exited()
	require.Len(t, acts, 1, "45s after the first sighting is past the 30s grace")
	require.Equal(t, ActOpen, acts[0].Type)
	require.Equal(t, KindCrashed, acts[0].Kind)
	require.True(t, acts[0].Notify)
	require.Contains(t, acts[0].Detail, "exited")

	require.Empty(t, r.until(10*time.Minute, r.exited), "no repeat while it stays down")
	require.NotNil(t, r.tr.Open())
}

func TestShortOutageBelowTheGraceIsIgnored(t *testing.T) {
	r := newRig(t)
	r.ok()
	r.exited()
	require.Empty(t, r.ok())
	require.Empty(t, r.until(time.Hour, r.ok))
	require.Nil(t, r.tr.Open())
}

func TestRecoveryNeedsAStableWindowAndIsSentOnce(t *testing.T) {
	r := newRig(t)
	r.ok()
	r.until(time.Minute, r.exited)
	require.NotNil(t, r.tr.Open())

	require.Empty(t, r.until(r.th.RecoverAfter-every, r.ok), "not yet stable for long enough")
	// A blip resets the stable window.
	r.exited()
	require.Empty(t, r.until(r.th.RecoverAfter-every, r.ok))
	acts := r.until(2*every, r.ok)
	require.Len(t, acts, 1)
	require.Equal(t, ActResolve, acts[0].Type)
	require.True(t, acts[0].Notify)
	require.Equal(t, ResolutionRecovered, acts[0].Resolution)
	require.Equal(t, KindCrashed, acts[0].Kind)
	require.Nil(t, r.tr.Open())
	require.Empty(t, r.until(time.Hour, r.ok), "nothing more once recovered")
}

func TestUnhealthyNeedsItsOwnGraceAndEscalatesToCrashedWithoutRepeating(t *testing.T) {
	r := newRig(t)
	r.ok()
	acts := r.until(r.th.UnhealthyGrace-every, func() []Action {
		return r.step(agentprotocol.StateRunning, agentprotocol.HealthUnhealthy)
	})
	require.Empty(t, acts, "health must fail for the whole grace period")
	acts = r.until(2*every, func() []Action { return r.step(agentprotocol.StateRunning, agentprotocol.HealthUnhealthy) })
	require.Len(t, acts, 1)
	require.Equal(t, KindUnhealthy, acts[0].Kind)
	require.True(t, acts[0].Notify)

	// The container then dies: a worse kind is announced once.
	acts = r.until(2*time.Minute, r.exited)
	n := notified(acts)
	require.Len(t, n, 1)
	require.Equal(t, ActEscalate, n[0].Type)
	require.Equal(t, KindCrashed, n[0].Kind)

	// It never de-escalates: coming back unhealthy is still the crashed incident.
	require.Empty(t, r.until(5*time.Minute, func() []Action {
		return r.step(agentprotocol.StateRunning, agentprotocol.HealthUnhealthy)
	}))
	require.Equal(t, KindCrashed, r.tr.Open().Kind)
}

func TestStartingHealthIsNotUnhealthy(t *testing.T) {
	r := newRig(t)
	r.ok()
	require.Empty(t, r.until(10*time.Minute, func() []Action {
		return r.step(agentprotocol.StateRunning, agentprotocol.HealthStarting)
	}))
}

func TestDockerRestartAfterACrashIsAnnouncedAtOnce(t *testing.T) {
	r := newRig(t)
	require.Empty(t, r.ok(), "the first sighting sets the baseline")
	acts := r.restart()
	require.Len(t, acts, 1)
	require.Equal(t, ActOpen, acts[0].Type)
	require.Equal(t, KindCrashed, acts[0].Kind)
	require.Contains(t, acts[0].Detail, "restarted by Docker")
	require.True(t, acts[0].Notify)

	require.Empty(t, r.until(r.th.RecoverAfter-every, r.ok))
	acts = r.until(2*every, r.ok)
	require.Equal(t, []ActionType{ActResolve}, types(acts))
}

func TestRestartLoopEscalatesOnceAtTheThreshold(t *testing.T) {
	r := newRig(t)
	r.ok()
	require.Equal(t, KindCrashed, r.restart()[0].Kind)
	require.Empty(t, r.restart(), "a second restart is the same incident")
	acts := r.restart()
	require.Len(t, acts, 1)
	require.Equal(t, ActEscalate, acts[0].Type)
	require.Equal(t, KindRestartLoop, acts[0].Kind)
	require.True(t, acts[0].Notify)
	require.Equal(t, 3, acts[0].Restarts)
	require.Empty(t, r.restart(), "restarts beyond the threshold stay silent")
	require.Empty(t, r.restart())
}

func TestRestartsOutsideTheWindowDoNotMakeALoop(t *testing.T) {
	r := newRig(t)
	r.th.RecoverAfter = 3 * time.Hour // keep the incident open: only the window is under test
	r.ok()
	require.Equal(t, KindCrashed, r.restart()[0].Kind)
	r.stableFor(6 * time.Minute)
	require.Empty(t, r.restart(), "two restarts 6 minutes apart")
	r.stableFor(6 * time.Minute)
	require.Empty(t, r.restart(), "the first restart is 12 minutes old: only 2 are inside the 10 minute window")
	require.Equal(t, KindCrashed, r.tr.Open().Kind)
	r.stableFor(3 * time.Minute)
	acts := r.restart()
	require.Len(t, acts, 1, "restarts 9, 3 and 0 minutes old are three inside the window")
	require.Equal(t, KindRestartLoop, acts[0].Kind)
}

func TestRestartingStateSustainedIsALoop(t *testing.T) {
	r := newRig(t)
	r.ok()
	require.Empty(t, r.step(agentprotocol.StateRestarting, agentprotocol.HealthUnhealthy))
	require.Empty(t, r.step(agentprotocol.StateRestarting, agentprotocol.HealthUnhealthy))
	acts := r.until(r.th.CrashGrace, func() []Action {
		return r.step(agentprotocol.StateRestarting, agentprotocol.HealthUnhealthy)
	})
	require.Len(t, acts, 1)
	require.Equal(t, KindRestartLoop, acts[0].Kind)
	require.Contains(t, acts[0].Detail, "restarting")
}

func TestNewContainerOrLowerCounterIsNotARestart(t *testing.T) {
	r := newRig(t)
	r.count = 5
	r.ok()
	r.cid, r.count = "c2", 0 // replaced container: counter starts at 0
	require.Empty(t, r.ok())
	r.count = 2
	require.Equal(t, KindCrashed, r.ok()[0].Kind, "a real increase on the same container counts")
}

func TestNewDeploymentResetsTheEvidence(t *testing.T) {
	r := newRig(t)
	r.count = 4
	r.ok()
	r.dep, r.count, r.cid = "dep-2", 1, "c9"
	require.Empty(t, r.ok(), "the first report of a new release only sets the baseline")
	r.count = 2
	require.Len(t, r.ok(), 1)
}

func TestDeploymentInProgressSuppressesAlerts(t *testing.T) {
	r := newRig(t)
	r.ok()
	for range 20 {
		r.now = r.now.Add(every)
		require.Empty(t, r.feed(Observation{State: agentprotocol.StateExited, Health: agentprotocol.HealthUnhealthy, Suppressed: true}))
	}
	// Once the deployment is over, the grace starts from scratch.
	require.Empty(t, r.exited())
	require.Empty(t, r.exited())
	require.Nil(t, r.tr.Open())
}

func TestStoppingTheServiceClosesTheIncidentSilently(t *testing.T) {
	r := newRig(t)
	r.ok()
	r.until(time.Minute, r.exited)
	require.NotNil(t, r.tr.Open())
	r.now = r.now.Add(every)
	acts := r.feed(Observation{State: agentprotocol.StateExited, Health: agentprotocol.HealthUnhealthy, Stopped: true})
	require.Len(t, acts, 1)
	require.Equal(t, ActResolve, acts[0].Type)
	require.Equal(t, ResolutionStopped, acts[0].Resolution)
	require.False(t, acts[0].Notify, "no recovery message for a service the owner stopped")
	require.Empty(t, r.until(time.Hour, func() []Action {
		r.now = r.now.Add(every)
		return r.feed(Observation{State: agentprotocol.StateExited, Health: agentprotocol.HealthUnhealthy, Stopped: true})
	}))
}

func TestSilenceBetweenReportsDiscardsTheEvidence(t *testing.T) {
	r := newRig(t)
	r.ok()
	r.exited()
	r.exited()
	r.now = r.now.Add(10 * time.Minute) // the agent was offline
	require.Empty(t, r.exited(), "the 30s the container may have been down before do not count")
	require.Empty(t, r.exited())
	r.count = 7
	r.now = r.now.Add(10 * time.Minute)
	require.Empty(t, r.ok(), "a counter that moved while nobody looked is only a new baseline")
}

func TestCompletedOneShotComponentIsNotACrash(t *testing.T) {
	r := newRig(t)
	o := func() []Action {
		r.now = r.now.Add(every)
		return r.feed(Observation{State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, Replicas: []Replica{
			{Key: "web/0", ContainerID: "a", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy},
			{Key: "migrate/0", ContainerID: "b", State: agentprotocol.StateExited, Health: agentprotocol.HealthHealthy}, // finished cleanly
		}})
	}
	require.Empty(t, r.until(time.Hour, o))
}

func TestOneReplicaDownIsACrash(t *testing.T) {
	r := newRig(t)
	o := func() []Action {
		r.now = r.now.Add(every)
		return r.feed(Observation{State: agentprotocol.StateRunning, Health: agentprotocol.HealthDegraded, Replicas: []Replica{
			{Key: "/0", ContainerID: "a", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy},
			{Key: "/1", ContainerID: "b", State: agentprotocol.StateExited, Health: agentprotocol.HealthUnhealthy},
		}})
	}
	acts := notified(r.until(time.Minute, o))
	require.Len(t, acts, 1)
	require.Equal(t, KindCrashed, acts[0].Kind)
	require.Contains(t, acts[0].Detail, "1 of 2 replicas")
}

func TestMissingContainerIsACrashAfterTheGrace(t *testing.T) {
	r := newRig(t)
	r.ok()
	acts := notified(r.until(time.Minute, func() []Action {
		return r.feed2(agentprotocol.StateMissing, agentprotocol.HealthUnknown)
	}))
	require.Len(t, acts, 1)
	require.Contains(t, acts[0].Detail, "missing")
}

func (r *rig) feed2(state, health string) []Action {
	r.now = r.now.Add(every)
	return r.feed(Observation{State: state, Health: health, Replicas: []Replica{{Key: "/0", State: agentprotocol.StateMissing, Health: agentprotocol.HealthUnknown}}})
}

// stableFor feeds healthy reports for d and returns the actions.
func (r *rig) stableFor(d time.Duration) []Action { return r.until(d, r.ok) }

func TestFlappingServiceIsAnnouncedOnceThenSilentUntilStable(t *testing.T) {
	r := newRig(t)
	r.ok()
	var all []Action
	incident := func() []Action {
		acts := r.restart()
		acts = append(acts, r.stableFor(r.th.RecoverAfter+2*every)...)
		return acts
	}

	a1 := incident() // alert + recovery
	a2 := incident() // alert + recovery
	require.Equal(t, []ActionType{ActOpen, ActResolve}, types(a1))
	require.Equal(t, []ActionType{ActOpen, ActResolve}, types(a2))
	require.Len(t, notified(a1), 2)
	require.Len(t, notified(a2), 2)

	// The third incident inside the hour is the flapping one.
	acts := r.restart()
	require.Len(t, acts, 1)
	require.Equal(t, ActOpen, acts[0].Type)
	require.True(t, acts[0].Flapping)
	require.Equal(t, KindRestartLoop, acts[0].Kind, "flapping is announced as a restart loop")
	require.True(t, acts[0].Notify)
	require.Contains(t, acts[0].Detail, "paused")

	// Until it is stable for a whole window it never speaks again, however often it fails.
	for range 6 {
		all = append(all, r.restart()...)
		all = append(all, r.stableFor(r.th.RecoverAfter+2*every)...)
		all = append(all, r.exited()...)
		all = append(all, r.until(2*time.Minute, r.exited)...)
		all = append(all, r.stableFor(3*time.Minute)...)
	}
	require.Empty(t, notified(all), "no alert and no recovery while flapping")
	require.NotNil(t, r.tr.Open())

	// The recovery comes after the service has been stable for the flap window.
	acts = r.stableFor(r.th.FlapWindow - 5*time.Minute)
	require.Empty(t, acts)
	acts = r.stableFor(5 * time.Minute)
	n := notified(acts)
	require.Len(t, n, 1)
	require.Equal(t, ActResolve, n[0].Type)
	require.True(t, n[0].Flapping)
	require.Nil(t, r.tr.Open())

	// After a calm hour the next failure is a normal incident again.
	acts = r.restart()
	require.Len(t, acts, 1)
	require.False(t, acts[0].Flapping)
	require.Equal(t, KindCrashed, acts[0].Kind)
}

func TestIncidentsOutsideTheFlapWindowAreNotFlapping(t *testing.T) {
	r := newRig(t)
	r.ok()
	for range 5 {
		acts := r.restart()
		require.Len(t, acts, 1)
		require.False(t, acts[0].Flapping)
		r.stableFor(r.th.RecoverAfter + 2*every)
		r.now = r.now.Add(r.th.FlapWindow) // an hour of calm
		r.ok()
	}
}

func TestResumedIncidentDoesNotAnnounceAgain(t *testing.T) {
	r := newRig(t)
	r.ok()
	r.until(time.Minute, r.exited)
	open := r.tr.Open()
	require.NotNil(t, open)

	// A control plane restart: a new tracker resumes the stored incident.
	r.tr = NewTracker(open)
	require.Empty(t, r.until(5*time.Minute, r.exited), "still down: no second alert")
	acts := r.until(r.th.RecoverAfter+2*every, r.ok)
	require.Len(t, acts, 1)
	require.Equal(t, ActResolve, acts[0].Type)
	require.True(t, acts[0].Notify, "the resumed incident was announced, so its recovery is")
}

func TestThresholdsAreConfigurable(t *testing.T) {
	r := newRig(t)
	r.th.CrashGrace = 0
	r.th.LoopRestarts = 2
	r.ok()
	acts := r.exited()
	require.Len(t, acts, 1, "no grace: the first sighting is enough")
	r2 := newRig(t)
	r2.th.LoopRestarts = 2
	r2.ok()
	require.Equal(t, KindCrashed, r2.restart()[0].Kind)
	require.Equal(t, KindRestartLoop, r2.restart()[0].Kind)
}

func TestHumanDuration(t *testing.T) {
	for in, want := range map[time.Duration]string{
		45 * time.Second: "45s", 2 * time.Minute: "2m", 10 * time.Minute: "10m", time.Hour: "1h", 90 * time.Minute: "1h30m", 72 * time.Hour: "3d",
	} {
		require.Equal(t, want, humanDuration(in))
	}
}
