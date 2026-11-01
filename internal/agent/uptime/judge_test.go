package uptime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var at = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func TestJudgeOutcomes(t *testing.T) {
	pass := Replica{Passed: true}
	fail := Replica{Error: "HTTP 503 from 10.0.0.2:80/"}
	stopped := Replica{Stopped: true, Error: "stopped"}

	cases := []struct {
		name            string
		reps            []Replica
		outcome         string
		replicas, ok    int
		wantErrContains string
	}{
		{"all pass", []Replica{pass, pass}, agentprotocol.ProbeUp, 2, 2, ""},
		{"single replica passes", []Replica{pass}, agentprotocol.ProbeUp, 1, 1, ""},
		{"some pass", []Replica{pass, fail, pass}, agentprotocol.ProbeDegraded, 3, 2, "HTTP 503"},
		{"none pass", []Replica{fail, fail}, agentprotocol.ProbeDown, 2, 0, "HTTP 503"},
		{"not running though wanted", []Replica{{Error: "not running: container nc-web-0 does not exist"}}, agentprotocol.ProbeDown, 1, 0, "not running"},
		{"no replicas wanted", nil, agentprotocol.ProbePaused, 0, 0, ""},
		{"all stopped on purpose", []Replica{stopped, stopped}, agentprotocol.ProbePaused, 0, 0, ""},
		{"one stopped replica among running ones is a failure", []Replica{pass, stopped}, agentprotocol.ProbeDegraded, 2, 1, "stopped"},
		{"stopped and failing is down", []Replica{stopped, fail}, agentprotocol.ProbeDown, 2, 0, "stopped"},
		{"a failure without a reason still says why", []Replica{{}}, agentprotocol.ProbeDown, 1, 0, "unhealthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Judge("svc-1", at, c.reps)
			require.Equal(t, "svc-1", got.ServiceID)
			require.Equal(t, at, got.At)
			require.Equal(t, c.outcome, got.Outcome)
			require.Equal(t, c.replicas, got.Replicas)
			require.Equal(t, c.ok, got.Passed)
			if c.wantErrContains == "" {
				require.Empty(t, got.Error)
			} else {
				require.Contains(t, got.Error, c.wantErrContains)
			}
		})
	}
}

func TestJudgeReportsTheFirstFailureAndTheSlowestPass(t *testing.T) {
	got := Judge("svc", at.In(time.FixedZone("x", 3600)), []Replica{
		{Passed: true, Latency: 12 * time.Millisecond},
		{Error: "first failure"},
		{Passed: true, Latency: 340 * time.Millisecond},
		{Error: "second failure"},
	})
	require.Equal(t, agentprotocol.ProbeDegraded, got.Outcome)
	require.Equal(t, "first failure", got.Error)
	require.Equal(t, 340, got.LatencyMs, "the slowest passing probe")
	require.Equal(t, time.UTC, got.At.Location(), "results carry UTC")
}

func TestJudgePausedIsNotACheck(t *testing.T) {
	got := Judge("svc", at, []Replica{{Stopped: true, Error: "stopped"}})
	require.Equal(t, agentprotocol.ProbePaused, got.Outcome)
	require.Zero(t, got.Replicas, "a paused result counts no replicas")
	require.Zero(t, got.Passed)
	require.Empty(t, got.Error)
}

func TestDockerHealthMapping(t *testing.T) {
	const startPeriod = 30 * time.Second
	cases := []struct {
		name       string
		status     string
		sinceStart time.Duration
		pass       bool
		reason     string
	}{
		{"healthy", "healthy", time.Hour, true, ""},
		{"healthy within the start period", "healthy", time.Second, true, ""},
		{"starting within the start period passes", "starting", 10 * time.Second, true, ""},
		{"starting just before the end of the start period passes", "starting", startPeriod - time.Millisecond, true, ""},
		{"starting after the start period fails", "starting", startPeriod, false, "still starting"},
		{"starting long after fails", "starting", time.Hour, false, "still starting"},
		{"unhealthy fails even within the start period", "unhealthy", time.Second, false, "unhealthy: docker health status is unhealthy"},
		{"no HEALTHCHECK defined: running is enough", "", time.Hour, true, ""},
		{"unknown status fails", "weird", time.Hour, false, "docker health status is weird"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pass, reason := DockerHealth(c.status, c.sinceStart, startPeriod)
			require.Equal(t, c.pass, pass)
			if c.reason == "" {
				require.Empty(t, reason)
			} else {
				require.Contains(t, reason, c.reason)
			}
		})
	}

	pass, _ := DockerHealth("starting", 0, 0)
	require.False(t, pass, "without a start period, starting is not yet healthy")
}
