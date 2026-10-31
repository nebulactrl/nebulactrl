package svcalerts

import (
	"fmt"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Kind is what is wrong with a service, worst last.
type Kind string

// Incident kinds. An incident only ever escalates (unhealthy, crashed,
// restart_loop) while it is open.
const (
	KindUnhealthy   Kind = "unhealthy"
	KindCrashed     Kind = "crashed"
	KindRestartLoop Kind = "restart_loop"
)

func (k Kind) rank() int {
	switch k {
	case KindUnhealthy:
		return 1
	case KindCrashed:
		return 2
	case KindRestartLoop:
		return 3
	}
	return 0
}

// Resolutions recorded on a closed incident.
const (
	ResolutionRecovered = "recovered"
	ResolutionStopped   = "stopped"
	ResolutionDeleted   = "deleted"
)

// maxGap is the longest silence between two observations of a service before
// the evidence gathered so far (restart baselines, how long it has been down)
// is discarded: an agent that was offline tells nothing about the time in
// between, and a restarted daemon resets Docker's counters.
const maxGap = 2 * time.Minute

// maxRestartEntries bounds the restart log of one tracker.
const maxRestartEntries = 100

// Replica is the part of an observed replica the detector reads.
type Replica struct {
	// Key identifies the replica across observations (component and index).
	Key          string
	ContainerID  string
	State        string
	Health       string
	RestartCount int
}

// Observation is one report of a service's runtime state.
type Observation struct {
	At           time.Time
	DeploymentID string
	// State and Health are the aggregates the agent computed over Replicas.
	State, Health string
	Replicas      []Replica
	// Suppressed: a deployment is replacing the container, so state changes
	// are expected and must not start an incident.
	Suppressed bool
	// Stopped: the owner stopped the service; nothing is expected to run.
	Stopped bool
}

// Incident is the open incident of a service.
type Incident struct {
	Kind     Kind
	Flapping bool
	OpenedAt time.Time
	Detail   string
	Restarts int
	// Notified is true once a message went out for it (a recovery is sent only then).
	Notified bool
}

// ActionType is what the caller has to do.
type ActionType string

// Action types.
const (
	// ActOpen: a new incident started.
	ActOpen ActionType = "open"
	// ActEscalate: the open incident got worse (its Kind changed).
	ActEscalate ActionType = "escalate"
	// ActResolve: the incident ended.
	ActResolve ActionType = "resolve"
)

// Action is one effect of Step. Notify says whether a message goes out; an
// action without it is only recorded (a flapping incident is silent).
type Action struct {
	Type       ActionType
	Kind       Kind
	Notify     bool
	Flapping   bool
	Detail     string
	Restarts   int
	Resolution string
	// Incident is a snapshot after the action (for Resolve: the closed incident).
	Incident Incident
}

type baseline struct {
	container string
	count     int
}

// Tracker is the incident state machine of one service. It is pure: time and
// settings come in with every Step, nothing is read or written elsewhere, so
// it is tested without a clock. It is not safe for concurrent use.
type Tracker struct {
	deployment string
	last       time.Time

	base     map[string]baseline
	restarts []time.Time // one entry per Docker restart seen, inside the loop window

	downSince, restartingSince, unhealthySince time.Time
	stableSince                                time.Time

	history []time.Time // when incidents opened, inside the flap window
	inc     *Incident
}

// NewTracker returns a tracker; open resumes an incident loaded from storage.
func NewTracker(open *Incident) *Tracker {
	t := &Tracker{base: map[string]baseline{}}
	if open != nil {
		c := *open
		t.inc = &c
		t.history = []time.Time{open.OpenedAt}
	}
	return t
}

// Open returns the open incident (nil when none).
func (t *Tracker) Open() *Incident {
	if t.inc == nil {
		return nil
	}
	c := *t.inc
	return &c
}

func (t *Tracker) resetEvidence() {
	t.base = map[string]baseline{}
	t.restarts = nil
	t.resetCandidates()
}

func (t *Tracker) resetCandidates() {
	t.downSince, t.restartingSince, t.unhealthySince, t.stableSince = time.Time{}, time.Time{}, time.Time{}, time.Time{}
}

func since(cur time.Time, cond bool, now time.Time) time.Time {
	switch {
	case !cond:
		return time.Time{}
	case cur.IsZero():
		return now
	}
	return cur
}

// countRestarts compares Docker's restart counters with the last seen ones and
// returns how many restarts happened in between. A replica seen for the first
// time, with a new container or with a lower counter only sets its baseline.
func (t *Tracker) countRestarts(reps []Replica, now time.Time) int {
	seen := make(map[string]bool, len(reps))
	delta := 0
	for _, r := range reps {
		seen[r.Key] = true
		b, ok := t.base[r.Key]
		if !ok || b.container != r.ContainerID || r.RestartCount < b.count {
			t.base[r.Key] = baseline{container: r.ContainerID, count: r.RestartCount}
			continue
		}
		if n := r.RestartCount - b.count; n > 0 {
			delta += n
			for i := 0; i < n && len(t.restarts) < maxRestartEntries; i++ {
				t.restarts = append(t.restarts, now)
			}
			t.base[r.Key] = baseline{container: r.ContainerID, count: r.RestartCount}
		}
	}
	for k := range t.base {
		if !seen[k] {
			delete(t.base, k)
		}
	}
	return delta
}

func prune(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > window {
		i++
	}
	return ts[i:]
}

// flags summarises an observation.
type flags struct {
	down, restarting, unhealthy, good bool
	downReplicas, restartingReplicas  int
}

func classify(o Observation) flags {
	var f flags
	for _, r := range o.Replicas {
		switch {
		case r.State == agentprotocol.StateRestarting:
			f.restartingReplicas++
		case r.State == agentprotocol.StateExited && r.Health != agentprotocol.HealthHealthy:
			f.downReplicas++
		}
	}
	if len(o.Replicas) == 0 { // an agent that reports no replicas: use the aggregate
		switch o.State {
		case agentprotocol.StateRestarting:
			f.restartingReplicas = 1
		case agentprotocol.StateExited:
			f.downReplicas = 1
		}
	}
	missing := o.State == agentprotocol.StateMissing
	f.down = missing || f.downReplicas > 0
	f.restarting = o.State == agentprotocol.StateRestarting || f.restartingReplicas > 0
	f.unhealthy = !f.down && !f.restarting &&
		(o.Health == agentprotocol.HealthUnhealthy || o.Health == agentprotocol.HealthDegraded)
	f.good = !f.down && !f.restarting && !missing && o.Health == agentprotocol.HealthHealthy
	return f
}

// Step feeds one observation and returns what to do about it.
func (t *Tracker) Step(o Observation, th Settings) []Action {
	now := o.At
	if !t.last.IsZero() && now.Sub(t.last) > maxGap {
		t.resetEvidence()
	}
	t.last = now

	if o.Stopped {
		t.resetEvidence()
		if t.inc == nil {
			return nil
		}
		return []Action{t.close(ResolutionStopped, false)}
	}
	if o.DeploymentID != t.deployment {
		t.deployment = o.DeploymentID // a new release: counters and timers start over
		t.resetEvidence()
	}
	if o.Suppressed {
		t.resetCandidates()
		return nil
	}

	delta := t.countRestarts(o.Replicas, now)
	t.restarts = prune(t.restarts, now, th.LoopWindow)
	f := classify(o)
	t.downSince = since(t.downSince, f.down, now)
	t.restartingSince = since(t.restartingSince, f.restarting, now)
	t.unhealthySince = since(t.unhealthySince, f.unhealthy, now)
	t.stableSince = since(t.stableSince, f.good && delta == 0, now)

	var cond Kind
	switch {
	case len(t.restarts) >= th.LoopRestarts || (f.restarting && now.Sub(t.restartingSince) >= th.CrashGrace):
		cond = KindRestartLoop
	case (f.down && now.Sub(t.downSince) >= th.CrashGrace) || delta > 0:
		cond = KindCrashed
	case f.unhealthy && now.Sub(t.unhealthySince) >= th.UnhealthyGrace:
		cond = KindUnhealthy
	}

	if t.inc == nil {
		if cond == "" {
			return nil
		}
		return []Action{t.open(cond, o, f, th)}
	}

	var out []Action
	if cond.rank() > t.inc.Kind.rank() {
		t.inc.Kind = cond
		t.inc.Detail = t.describe(cond, o, f, th)
		t.inc.Restarts = len(t.restarts)
		notify := !t.inc.Flapping
		t.inc.Notified = t.inc.Notified || notify
		out = append(out, Action{Type: ActEscalate, Kind: cond, Notify: notify, Flapping: t.inc.Flapping,
			Detail: t.inc.Detail, Restarts: t.inc.Restarts, Incident: *t.inc})
	}
	need := th.RecoverAfter
	if t.inc.Flapping {
		need = th.FlapWindow
	}
	if cond == "" && f.good && !t.stableSince.IsZero() && now.Sub(t.stableSince) >= need {
		out = append(out, t.close(ResolutionRecovered, t.inc.Notified))
		t.restarts = nil
	}
	return out
}

// open starts an incident. The incident that reaches FlapIncidents within
// FlapWindow becomes a flapping one: announced once as a restart loop, then
// silent until the service has been stable for a whole window.
func (t *Tracker) open(cond Kind, o Observation, f flags, th Settings) Action {
	now := o.At
	t.history = append(prune(t.history, now, th.FlapWindow), now)
	flapping := len(t.history) >= th.FlapIncidents
	kind := cond
	detail := t.describe(cond, o, f, th)
	if flapping {
		kind = KindRestartLoop
		detail = fmt.Sprintf("The service failed %d times in the last %s. Further alerts for it are paused until it has been stable for %s.",
			len(t.history), humanDuration(th.FlapWindow), humanDuration(th.FlapWindow))
	}
	t.inc = &Incident{Kind: kind, Flapping: flapping, OpenedAt: now, Detail: detail, Restarts: len(t.restarts), Notified: true}
	return Action{Type: ActOpen, Kind: kind, Notify: true, Flapping: flapping, Detail: detail, Restarts: len(t.restarts), Incident: *t.inc}
}

func (t *Tracker) close(resolution string, notify bool) Action {
	a := Action{Type: ActResolve, Kind: t.inc.Kind, Notify: notify, Flapping: t.inc.Flapping, Resolution: resolution,
		Detail: t.inc.Detail, Restarts: t.inc.Restarts, Incident: *t.inc}
	t.inc = nil
	t.stableSince = time.Time{}
	return a
}

func (t *Tracker) describe(k Kind, o Observation, f flags, th Settings) string {
	now := o.At
	switch k {
	case KindRestartLoop:
		if n := len(t.restarts); n > 0 {
			return fmt.Sprintf("Docker restarted the container %s in the last %s.", plural(n, "time"), humanDuration(th.LoopWindow))
		}
		return fmt.Sprintf("The container has been restarting for %s.", humanDuration(now.Sub(t.restartingSince)))
	case KindCrashed:
		if f.down && !t.downSince.IsZero() && now.Sub(t.downSince) >= th.CrashGrace {
			if o.State == agentprotocol.StateMissing {
				return fmt.Sprintf("The container is gone and has been missing for %s.", humanDuration(now.Sub(t.downSince)))
			}
			if len(o.Replicas) > 1 {
				return fmt.Sprintf("%d of %d replicas exited and have been down for %s.", f.downReplicas, len(o.Replicas), humanDuration(now.Sub(t.downSince)))
			}
			return fmt.Sprintf("The container exited and has been down for %s.", humanDuration(now.Sub(t.downSince)))
		}
		return "The container crashed and was restarted by Docker."
	default:
		return fmt.Sprintf("The health check has been failing for %s (health %s).", humanDuration(now.Sub(t.unhealthySince)), orDash(o.Health))
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// humanDuration renders d coarsely: "45s", "10m", "2h".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
