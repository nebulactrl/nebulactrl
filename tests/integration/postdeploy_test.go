//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stepEvents returns "step:status" of a deployment's agent events in order.
func (s *Stack) stepEvents(deploymentID string) []string {
	s.t.Helper()
	var out []string
	for _, e := range s.Do("GET", "/deployments/"+deploymentID+"/events", nil).JSON().Items() {
		if e.Str("step") != "status" {
			out = append(out, e.Str("step")+":"+e.Str("status"))
		}
	}
	return out
}

// Post-deploy command: it runs in a one-shot container of the new release after
// the swap (the service already answers with the new version when it starts),
// its output is in the deploy log, and a failure is warn-only: the deployment
// still succeeds, with a warning step and the failure in its message.
func TestPostDeployRunsAfterTheSwapAndFailureOnlyWarns(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	t.Cleanup(s.removeTaskContainers)
	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Domain: s.Host(),
		Vars: map[string]string{"VERSION": "v1"},
	})
	s.DeployOK(svc.ID)
	NewProxyClient(s.Host()).WaitBody(t, "/", "v1", 20*time.Second)

	cfg := map[string]any{
		"image": app.AppImage, "port": 8080, "stopGraceSeconds": 5,
		"healthcheck": map[string]any{"type": "http", "path": "/health", "intervalSeconds": 1, "deadlineSeconds": 30, "successThreshold": 1},
		// The command asks the service itself (by its name on the environment
		// network): it can only see v2 when the swap already happened.
		"postDeployCommand": []string{"/app", "get", "http://" + svc.Slug + ":8080/"},
	}
	patch := func() {
		r := s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": cfg}, "If-Match", s.serviceETag(svc.ID))
		require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	}

	// 1. A passing post-deploy command: output in the deploy logs, after the swap.
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	patch()
	d := s.DeployOK(svc.ID)
	logs := s.DeploymentLogs(d.Str("id"))
	require.Contains(t, logs, "-> 200 v2", "the command ran against the new release:\n"+logs)
	steps := s.stepEvents(d.Str("id"))
	started, done := indexOf(steps, "postdeploy:started"), indexOf(steps, "postdeploy:succeeded")
	require.GreaterOrEqual(t, started, 0, "%v", steps)
	require.Greater(t, done, started, "%v", steps)
	require.Greater(t, started, indexOf(steps, "route:succeeded"), "after the route swap: %v", steps)
	require.Greater(t, started, indexOf(steps, "verify:succeeded"), "after verification: %v", steps)
	require.NotContains(t, d.Str("statusMessage"), "Warning")
	NewProxyClient(s.Host()).WaitBody(t, "/", "v2", 20*time.Second)
	require.True(t, eventually(10*time.Second, 500*time.Millisecond, func() bool { return s.taskContainers() == 0 }), "the post-deploy container is removed")

	// 2. A failing one: warn-only. v3 is live, the deployment succeeded.
	s.SetVars(svc.ID, map[string]string{"VERSION": "v3"}, nil)
	cfg["postDeployCommand"] = []string{"/app", "task", "7", "cache", "warm-up", "exploded"}
	patch()
	d = s.WaitDeployment(s.Deploy(svc.ID), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	require.Contains(t, d.Str("statusMessage"), "Warning: post-deploy command failed with exit code 7")
	require.Contains(t, s.DeploymentLogs(d.Str("id")), "cache warm-up exploded")
	steps = s.stepEvents(d.Str("id"))
	require.Contains(t, steps, "postdeploy:warning")
	require.NotContains(t, steps, "postdeploy:failed")
	var warning string
	for _, e := range s.Do("GET", "/deployments/"+d.Str("id")+"/events", nil).JSON().Items() {
		if e.Str("step") == "postdeploy" && e.Str("status") == "warning" {
			warning = e.Str("message")
		}
	}
	require.Contains(t, warning, "exit code 7")
	require.Contains(t, warning, "not rolled back")
	NewProxyClient(s.Host()).WaitBody(t, "/", "v3", 20*time.Second)
	require.Len(t, s.RunningContainers(svc.ID), 1)
	require.True(t, eventually(10*time.Second, 500*time.Millisecond, func() bool { return s.taskContainers() == 0 }))

	// 3. A rollback re-runs a release without its post-deploy command.
	var releaseOfV2 string
	for _, it := range s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items() {
		if it.Str("status") == "succeeded" && strings.Contains(s.DeploymentLogs(it.Str("id")), "-> 200 v2") {
			releaseOfV2 = it.Str("id")
		}
	}
	require.NotEmpty(t, releaseOfV2)
	r := s.Do("POST", "/deployments/"+releaseOfV2+"/rollback", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rb := s.WaitDeployment(r.JSON().Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", rb.Str("status"), "%v", rb)
	require.NotContains(t, s.stepEvents(rb.Str("id")), "postdeploy:started", "rollbacks never run the post-deploy command")
}
