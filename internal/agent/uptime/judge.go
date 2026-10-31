// Package uptime turns the probes of a service's replicas into the results the
// control plane keeps availability from, and delivers those results durably.
//
// The probing itself (finding the containers, reaching them over the
// environment network) lives in the engine, which owns the Docker state; this
// package holds the pure judgement (Judge, DockerHealth) and the Collector
// that buffers results in the agent's store until the control plane
// acknowledges them, exactly like the traffic collector.
package uptime

import (
	"fmt"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Replica is the probe of one wanted replica (or one member of a stack, or
// one upstream of a route service).
type Replica struct {
	// Passed is true when the replica is healthy.
	Passed bool
	// Stopped marks a replica that was stopped on purpose (an exited
	// container that is not restarting). A service whose every replica is
	// stopped is paused, not down.
	Stopped bool
	// Latency is the duration of the successful HTTP or TCP probe; 0 for
	// checks without a network probe.
	Latency time.Duration
	// Error describes why the replica failed.
	Error string
}

// Judge folds the replicas of one service into one result at time at.
//
//   - no replica wanted, or every replica stopped on purpose: paused;
//   - every replica passed: up;
//   - some passed: degraded;
//   - none passed (including a replica that is not running though wanted): down.
//
// A paused result carries no replica counts: it is not a check. LatencyMs is
// the slowest passing network probe; Error the first failure.
func Judge(serviceID string, at time.Time, reps []Replica) agentprotocol.ProbeResult {
	res := agentprotocol.ProbeResult{ServiceID: serviceID, At: at.UTC()}
	paused := true
	for _, r := range reps {
		if !r.Stopped {
			paused = false
			break
		}
	}
	if paused {
		res.Outcome = agentprotocol.ProbePaused
		return res
	}
	res.Replicas = len(reps)
	var slowest time.Duration
	for _, r := range reps {
		if r.Passed {
			res.Passed++
			slowest = max(slowest, r.Latency)
			continue
		}
		if res.Error == "" {
			res.Error = r.Error
			if res.Error == "" {
				res.Error = "unhealthy"
			}
		}
	}
	res.LatencyMs = int(slowest.Milliseconds())
	switch {
	case res.Passed == res.Replicas:
		res.Outcome = agentprotocol.ProbeUp
	case res.Passed > 0:
		res.Outcome = agentprotocol.ProbeDegraded
	default:
		res.Outcome = agentprotocol.ProbeDown
	}
	return res
}

// DockerHealth judges a running container by its Docker health status, for
// the docker and exec check types.
//
//	healthy   passes;
//	starting  passes while the container is within its start period
//	          (sinceStart < startPeriod), fails after it;
//	unhealthy fails;
//	""        the image defines no HEALTHCHECK: a running container passes.
func DockerHealth(status string, sinceStart, startPeriod time.Duration) (pass bool, reason string) {
	switch status {
	case "healthy", "":
		return true, ""
	case "starting":
		if sinceStart < startPeriod {
			return true, ""
		}
		return false, "unhealthy: health check still starting after the start period"
	default:
		return false, fmt.Sprintf("unhealthy: docker health status is %s", status)
	}
}
