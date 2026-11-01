// Package uptime turns the probe results of every agent into what people see
// about a service: its current state (up, degraded, down, maintenance, paused,
// unknown), availability over time, outages (incidents) and the alerts for
// them.
//
// Agents probe each service they run from the inside, every
// agentprotocol.UptimeInterval, and send the results in Probes frames. The
// ProbeSink (ingest.go) validates and stores them in minute rollups and feeds
// them, in time order, to Step (this file): a pure state machine that debounces
// failures, never alerts for maintenance or a deployment in flight, and tells
// the caller when to open or resolve an incident. Service (service.go) applies
// those actions in the same transaction as the counts, so a frame is either
// stored completely or not at all, then publishes the live-update event and
// sends the notifications. Rollups and retention run as River jobs (jobs.go);
// http.go serves the dashboard and API.
package uptime

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// States of a service (uptime_state.state).
const (
	StateUp          = "up"          // the last checks passed
	StateDegraded    = "degraded"    // some replicas fail, at least one passes
	StateDown        = "down"        // an open incident: failure_threshold checks in a row failed
	StateMaintenance = "maintenance" // maintenance mode or a maintenance window covers the service
	StatePaused      = "paused"      // stopped on purpose, or monitoring is off
	StateUnknown     = "unknown"     // no check yet, or the agent stopped reporting
)

// Defaults of a monitor: a service without an uptime_monitors row.
const (
	DefaultFailureThreshold  = 3
	DefaultRecoveryThreshold = 2
)

// StaleAfter is how long a service may go without a check before its state
// becomes unknown: three missed cycles and a margin. An offline agent is the
// server-offline alert's business, not an outage of every service on it.
const StaleAfter = 3*agentprotocol.UptimeInterval + 30*time.Second

// maxCount bounds the consecutive counters (they only matter up to the thresholds).
const maxCount = 1000

// maxCause bounds the stored and announced error text.
const maxCause = 500

// Settings are the monitor settings Step needs.
type Settings struct {
	// Enabled: false means monitoring is off; the service is paused.
	Enabled bool
	// Alerts: send uptime.down / uptime.up for incidents opened from now on.
	Alerts            bool
	FailureThreshold  int // consecutive failed checks that open an incident
	RecoveryThreshold int // consecutive passing checks that resolve it
}

// DefaultSettings returns the settings of a service without a monitor row.
func DefaultSettings() Settings {
	return Settings{Enabled: true, Alerts: true, FailureThreshold: DefaultFailureThreshold, RecoveryThreshold: DefaultRecoveryThreshold}
}

// Context is what the caller knows about the service at the time of a check.
type Context struct {
	// InDeploy: a deployment of the service is in flight. Failing checks are
	// still counted in the rollups but cannot open an incident.
	InDeploy bool
	// InMaintenance: maintenance mode or an active maintenance window.
	InMaintenance bool
}

// State is the persisted state of one service (uptime_state plus whether it
// has an open incident).
type State struct {
	State string
	// Since is when State was entered.
	Since time.Time
	// Failures and Successes count consecutive checks.
	Failures, Successes int
	LastCheckAt         *time.Time
	LastOKAt            *time.Time
	LastError           string
	LastLatencyMs       *int

	// IncidentOpen: an uptime_incidents row of the service is open.
	// IncidentNotified: its uptime.down alert was sent, so its end is announced.
	IncidentOpen     bool
	IncidentNotified bool
}

// Action kinds returned by Step.
const (
	ActOpen    = "open"    // open an incident
	ActResolve = "resolve" // resolve the open incident
)

// Action is something the caller must record.
type Action struct {
	Kind string
	// Cause (ActOpen): what the failing check reported.
	Cause string
	// Notify: send uptime.down (ActOpen) or uptime.up (ActResolve).
	Notify bool
}

// Step applies one probe result to a service's state and returns the new
// state and the actions to take. It is pure: results must be fed in time
// order, each one once (the caller skips results at or before LastCheckAt).
//
// Rules, in order:
//   - A paused outcome (no replica wanted) or a disabled monitor pauses the
//     service: no check is counted, an open incident is closed without a
//     recovery notice.
//   - A check passes when the outcome is up or degraded. Degraded shows at
//     once and never alerts.
//   - FailureThreshold consecutive failed checks open an incident (state
//     down); RecoveryThreshold consecutive passing checks resolve it.
//     Failures during a deployment, or while the service is in maintenance,
//     do not count towards the threshold.
//   - Maintenance shows state maintenance and sends no alert. An open incident
//     stays open and resolves normally.
func Step(st State, r agentprotocol.ProbeResult, set Settings, c Context) (State, []Action) {
	at := r.At
	if r.Outcome == agentprotocol.ProbePaused || !set.Enabled {
		out, acts := Pause(st, at)
		out.LastCheckAt = &at
		return out, acts
	}

	pass := r.Outcome == agentprotocol.ProbeUp || r.Outcome == agentprotocol.ProbeDegraded
	out := st
	out.LastCheckAt = &at
	if pass {
		out.Successes = min(st.Successes+1, maxCount)
		out.Failures = 0
		out.LastOKAt = &at
	} else {
		out.Failures = min(st.Failures+1, maxCount)
		out.Successes = 0
	}
	out.LastError = ""
	if r.Outcome != agentprotocol.ProbeUp {
		out.LastError = Cause(r)
	}
	out.LastLatencyMs = nil
	if pass && r.LatencyMs > 0 {
		l := r.LatencyMs
		out.LastLatencyMs = &l
	}
	if !st.IncidentOpen {
		if c.InMaintenance {
			out.Failures, out.Successes = 0, 0 // maintenance is not judged
		} else if c.InDeploy && !pass {
			out.Failures = 0 // a deployment is expected to fail checks; start fresh when it ends
		}
	}

	var acts []Action
	switch {
	case st.IncidentOpen:
		if pass && out.Successes >= max(set.RecoveryThreshold, 1) {
			acts = append(acts, Action{Kind: ActResolve, Notify: st.IncidentNotified})
			out.IncidentOpen, out.IncidentNotified = false, false
		}
	case !pass && !c.InMaintenance && !c.InDeploy && out.Failures >= max(set.FailureThreshold, 1):
		acts = append(acts, Action{Kind: ActOpen, Cause: Cause(r), Notify: set.Alerts})
		out.IncidentOpen, out.IncidentNotified = true, set.Alerts
	}

	var next string
	switch {
	case c.InMaintenance:
		next = StateMaintenance
	case out.IncidentOpen:
		next = StateDown
	case pass && r.Outcome == agentprotocol.ProbeDegraded:
		next = StateDegraded
	case pass:
		next = StateUp
	case st.State == StateUp || st.State == StateDegraded:
		next = st.State // a failure below the threshold does not change what people see yet
	default:
		next = StateUnknown // no verdict yet: just left maintenance, pause or unknown
	}
	out.enter(next, at)
	return out, acts
}

// Pause moves a service to paused (stopped on purpose, or monitoring off). An
// open incident is closed silently: the service is not up, so no recovery is
// announced. LastCheckAt is left to the caller.
func Pause(st State, at time.Time) (State, []Action) {
	out := st
	out.Failures, out.Successes = 0, 0
	out.LastError = ""
	out.LastLatencyMs = nil
	var acts []Action
	if st.IncidentOpen {
		acts = append(acts, Action{Kind: ActResolve})
		out.IncidentOpen, out.IncidentNotified = false, false
	}
	out.enter(StatePaused, at)
	return out, acts
}

// Expire moves a service whose agent stopped reporting to unknown. An open
// incident stays open: nothing is known about the outage ending.
func Expire(st State, at time.Time) State {
	out := st
	out.Failures, out.Successes = 0, 0
	out.enter(StateUnknown, at)
	return out
}

// Resume moves a service whose monitoring was switched back on to unknown
// until its first check arrives.
func Resume(st State, at time.Time) State {
	if st.State != StatePaused {
		return st
	}
	out := st
	out.enter(StateUnknown, at)
	return out
}

// enter sets the state and, when it changed, its start time.
func (s *State) enter(next string, at time.Time) {
	if s.State != next || s.Since.IsZero() {
		s.State, s.Since = next, at
	}
}

// Cause is the text recorded for a failing check.
func Cause(r agentprotocol.ProbeResult) string {
	msg := strings.TrimSpace(r.Error)
	if msg == "" {
		if r.Outcome == agentprotocol.ProbeDegraded {
			return "some replicas failed their check"
		}
		return "no replica passed its check"
	}
	return truncate(oneLine(msg), maxCause)
}

// oneLine folds whitespace (newlines in a Docker health log) into single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncate cuts s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Availability is (up + degraded) / (checks - maintenance) as a percentage.
// ok is false when there is nothing to judge (no checks, or only maintenance):
// that is "no data", not 100 %.
func Availability(checks, up, degraded, maintenance int64) (pct float64, ok bool) {
	judged := checks - maintenance
	if judged <= 0 {
		return 0, false
	}
	return 100 * float64(up+degraded) / float64(judged), true
}
