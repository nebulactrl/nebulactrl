//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// commandStatus returns the status of the deploy command of a deployment.
func (s *Stack) commandStatus(deploymentID string) (status string) {
	s.t.Helper()
	err := s.Pool.QueryRow(context.Background(),
		`SELECT status FROM agent_commands WHERE deployment_id = $1 AND kind = 'deploy' ORDER BY created_at DESC LIMIT 1`,
		deploymentID).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

// startSlowV2 stages v2 and starts its deployment; the health start period
// keeps the deploy in flight long enough to interrupt. It returns once the
// command was acked and the deployment is past `building`.
func startSlowV2(t *testing.T, s *Stack, svcID string) string {
	t.Helper()
	s.SetVars(svcID, map[string]string{"VERSION": "v2"}, nil)
	dep := s.Deploy(svcID)
	ok := eventually(60*time.Second, 200*time.Millisecond, func() bool {
		st := s.Deployment(dep).Str("status")
		return s.commandStatus(dep) == "acked" && (st == "releasing" || st == "verifying")
	})
	require.True(t, ok, "deployment never reached an acked, in-flight state: %v (command %q)", s.Deployment(dep), s.commandStatus(dep))
	return dep
}

// 10a. The link drops after the agent acked a deploy. The deploy keeps running
// on the agent, its result is stored locally, and it is replayed (hello
// unackedResults) when the link returns.
func TestReconnectResultReplayAfterPartition(t *testing.T) {
	requireTier(t, tierFull)
	s := ExclusiveStack(t, StackOptions{Partitionable: true})
	svc, px := liveV1(t, s, map[string]any{"startPeriodSeconds": 6})

	dep := startSlowV2(t, s, svc.ID)
	s.Net.Cut()

	// The agent finishes the deploy while the control plane cannot hear it.
	require.True(t, eventually(2*time.Minute, time.Second, func() bool {
		cs := s.RunningContainers(svc.ID)
		return len(cs) == 1 && cs[0].Labels[agentprotocol.LabelDeployment] == dep
	}), "agent did not finish the deploy while partitioned")
	st := s.Deployment(dep).Str("status")
	require.False(t, terminal(st), "control plane cannot know the outcome yet, got %q", st)
	require.Equal(t, "acked", s.commandStatus(dep))

	s.Net.Heal()
	d := s.WaitDeployment(dep, 90*time.Second)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(dep))
	require.Equal(t, dep, s.Service(svc.ID).Str("currentDeploymentId"))
	require.Equal(t, "succeeded", s.commandStatus(dep))
	px.WaitBody(t, "/", "v2", 30*time.Second)
}

// 10b. The agent process dies after acking a deploy and is restarted with the
// same state dir. The command must not stay `acked` forever: the deployment
// reaches a terminal state that matches what actually runs.
func TestReconnectAgentKilledMidDeploy(t *testing.T) {
	requireTier(t, tierFull)
	s := ExclusiveStack(t)
	svc, px := liveV1(t, s, map[string]any{"startPeriodSeconds": 6})

	dep := startSlowV2(t, s, svc.ID)
	s.Agent.Stop() // crash: in-flight work is abandoned, no result is recorded
	require.False(t, terminal(s.Deployment(dep).Str("status")))

	s.StartAgent() // same state dir, same identity
	d := s.WaitDeployment(dep, 3*time.Minute)

	cs := s.RunningContainers(svc.ID)
	require.Len(t, cs, 1, "exactly one container must serve; deployment %v", d)
	switch d.Str("status") {
	case "succeeded":
		require.Equal(t, dep, cs[0].Labels[agentprotocol.LabelDeployment], "succeeded but the new release is not what runs")
		require.Equal(t, dep, s.Service(svc.ID).Str("currentDeploymentId"))
		px.WaitBody(t, "/", "v2", 30*time.Second)
	case "failed", "rolled_back":
		require.NotEqual(t, dep, cs[0].Labels[agentprotocol.LabelDeployment], "%s but the new release is what runs", d.Str("status"))
		px.WaitBody(t, "/", "v1", 30*time.Second)
	default:
		t.Fatalf("unexpected terminal status %q: %v", d.Str("status"), d)
	}
	// The command row is settled too.
	require.Contains(t, []string{"succeeded", "failed"}, s.commandStatus(dep))
}
