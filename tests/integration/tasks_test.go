//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const taskSecret = "task-canary-24680"

func (s *Stack) runFinished(runID string, timeout time.Duration) J {
	s.t.Helper()
	var run J
	ok := eventually(timeout, 500*time.Millisecond, func() bool {
		r := s.Do("GET", "/task-runs/"+runID, nil)
		if r.Status != http.StatusOK {
			return false
		}
		run = r.JSON()
		switch run.Str("status") {
		case "succeeded", "failed", "timed_out", "skipped":
			return true
		}
		return false
	})
	require.True(s.t, ok, "run %s did not finish within %s: %v", runID, timeout, run)
	return run
}

func (s *Stack) runOutput(runID string) string {
	s.t.Helper()
	r := s.Do("GET", "/task-runs/"+runID+"/logs", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	var sb strings.Builder
	for _, it := range r.JSON().Items() {
		sb.WriteString(it.Str("line") + "\n")
	}
	return sb.String()
}

// taskContainers lists leftover task or pre-deploy containers on the host.
func (s *Stack) taskContainers() int {
	s.t.Helper()
	cs, err := dockerEng.ContainerList(context.Background(), docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelTaskRun: "", agentprotocol.LabelEnvironment: s.EnvID},
	})
	require.NoError(s.t, err)
	return len(cs)
}

// removeTaskContainers cleans up after a failed run (disk is scarce).
func (s *Stack) removeTaskContainers() {
	cs, _ := dockerEng.ContainerList(context.Background(), docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelTaskRun: "", agentprotocol.LabelEnvironment: s.EnvID},
	})
	for _, c := range cs {
		_ = dockerEng.ContainerRemove(context.Background(), c.ID, true, false)
	}
}

// Tasks: a one-off command reports its exit code and (redacted) output and
// leaves no container; a cron task every minute runs; a failing pre-deploy
// command fails the deployment at step predeploy while the old release keeps
// serving.
func TestTasks(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)
	t.Cleanup(s.removeTaskContainers)
	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Domain: s.Host(),
		Vars:    map[string]string{"VERSION": "v1", "TASK_ECHO": "API_KEY"},
		Secrets: map[string]string{"API_KEY": taskSecret},
	})

	// Not deployed yet: commands are refused, not queued.
	r := s.Do("POST", "/services/"+svc.ID+"/run", map[string]any{"command": []string{"/app", "task", "0"}})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "not_deployed", r.JSON().Str("code"))

	s.DeployOK(svc.ID)
	NewProxyClient(s.Host()).WaitBody(t, "/", "v1", 20*time.Second)

	// One-off: exit code and output, secret masked, nothing left behind.
	r = s.Do("POST", "/services/"+svc.ID+"/run", map[string]any{"command": []string{"/app", "task", "3", "hello", "from", "task"}})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	run := s.runFinished(r.JSON().Str("id"), 90*time.Second)
	require.Equal(t, "failed", run.Str("status"), "%v", run)
	require.Equal(t, 3, run.Int("exitCode"))
	require.Contains(t, run.Str("error"), "exited with code 3")
	out := s.runOutput(run.Str("id"))
	require.Contains(t, out, "hello from task")
	require.Contains(t, out, "API_KEY=", "the service's environment reaches the task")
	require.NotContains(t, out, taskSecret, "secret values are redacted from stored output")
	require.True(t, eventually(10*time.Second, 500*time.Millisecond, func() bool { return s.taskContainers() == 0 }), "the task container is always removed")

	r = s.Do("POST", "/services/"+svc.ID+"/run", map[string]any{"command": []string{"/app", "task", "0", "fine"}})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	ok := s.runFinished(r.JSON().Str("id"), 90*time.Second)
	require.Equal(t, "succeeded", ok.Str("status"), "%v", ok)
	require.Equal(t, 0, ok.Int("exitCode"))

	// The persisted commands carry no secret value.
	var payloads string
	require.NoError(t, s.Pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(payload::text, ' '), '') FROM agent_commands WHERE kind = 'run_task'`).Scan(&payloads))
	require.NotEmpty(t, payloads)
	require.NotContains(t, payloads, taskSecret)

	// Cron: every minute. The scheduler ticks once a minute, so allow two.
	r = s.Do("POST", "/services/"+svc.ID+"/tasks", map[string]any{
		"name": "ping", "schedule": "* * * * *", "command": []string{"/app", "task", "0", "cron", "tick"},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	taskID := r.JSON().Str("id")
	var cronRun J
	require.True(t, eventually(150*time.Second, 2*time.Second, func() bool {
		rr := s.Do("GET", "/services/"+svc.ID+"/task-runs?taskId="+taskID, nil)
		for _, it := range rr.JSON().Items() {
			if it.Str("source") == "cron" && it.Str("status") == "succeeded" {
				cronRun = it
				return true
			}
		}
		return false
	}), "the every-minute task never ran")
	require.Contains(t, s.runOutput(cronRun.Str("id")), "cron tick")
	// Stop it: disabled tasks never run again.
	r = s.Do("PATCH", "/tasks/"+taskID, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// Pre-deploy that fails: the deployment fails at predeploy, v1 keeps serving.
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	cfg := map[string]any{
		"image": app.AppImage, "port": 8080, "stopGraceSeconds": 5,
		"healthcheck":      map[string]any{"type": "http", "path": "/health", "intervalSeconds": 1, "deadlineSeconds": 30, "successThreshold": 1},
		"preDeployCommand": []string{"/app", "task", "1", "migration", "exploded"},
	}
	r = s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": cfg}, "If-Match", s.serviceETag(svc.ID))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	d := s.WaitDeployment(s.Deploy(svc.ID), 3*time.Minute)
	require.Equal(t, "failed", d.Str("status"), "%v", d)
	require.Contains(t, d.Str("statusMessage"), "Pre-deploy command failed with exit code 1")
	require.Contains(t, d.Str("statusMessage"), "migration exploded")
	logs := s.DeploymentLogs(d.Str("id"))
	require.Contains(t, logs, "migration exploded")
	steps := s.Do("GET", "/deployments/"+d.Str("id")+"/events", nil).JSON().Items()
	var failedStep string
	for _, e := range steps {
		if e.Str("status") == "failed" && e.Str("step") != "status" {
			failedStep = e.Str("step")
		}
	}
	require.Equal(t, "predeploy", failedStep)
	NewProxyClient(s.Host()).WaitBody(t, "/", "v1", 10*time.Second) // the old release never stopped
	require.Len(t, s.RunningContainers(svc.ID), 1)
	require.True(t, eventually(10*time.Second, 500*time.Millisecond, func() bool { return s.taskContainers() == 0 }))

	// And a passing pre-deploy lets the deploy through (command output is in the deploy logs).
	cfg["preDeployCommand"] = []string{"/app", "task", "0", "migrated", "fine"}
	r = s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": cfg}, "If-Match", s.serviceETag(svc.ID))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	d = s.DeployOK(svc.ID)
	require.Contains(t, s.DeploymentLogs(d.Str("id")), "migrated fine")
	NewProxyClient(s.Host()).WaitBody(t, "/", "v2", 30*time.Second)
}

// A cron service: the deploy pulls the image and starts nothing, the schedule
// runs the service's own command in a throwaway container, Stop pauses the
// schedule without an agent command, and the reconciler never redeploys it.
func TestCronService(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	t.Cleanup(s.removeTaskContainers)
	const image = "quay.io/prometheus/busybox:latest"

	r := s.Do("POST", "/environments/"+s.EnvID+"/services", map[string]any{
		"name": "ticker", "kind": "worker", "sourceType": "image", "serverId": s.ServerID,
		"config": map[string]any{
			"image": image, "stopGraceSeconds": 2, "oneshot": true,
			"startCommand": []string{"sh", "-c", "echo tick"},
			"healthcheck":  map[string]any{"type": "none", "deadlineSeconds": 60},
			"cron":         map[string]any{"schedule": "* * * * *"},
		},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := Service{ID: r.JSON().Str("id"), Slug: r.JSON().Str("slug")}
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, svc.ID)
	s.servicesMu.Unlock()

	// Deploying prepares the image and starts nothing.
	d := s.DeployOK(svc.ID)
	require.Equal(t, "Image ready; runs on schedule", d.Str("statusMessage"), "%v", d)
	require.Empty(t, s.Containers(svc.ID), "a cron service has no container of its own")

	cron := s.Service(svc.ID).Obj("cron")
	require.NotEmpty(t, cron.Str("taskId"), "the view carries the schedule's task: %v", s.Service(svc.ID))
	require.Equal(t, "* * * * *", cron.Str("schedule"))
	require.NotEmpty(t, cron.Str("nextRunAt"))
	taskID := cron.Str("taskId")

	// The schedule ticks once a minute: within two the run happened and printed its output.
	var run J
	require.True(t, eventually(130*time.Second, 2*time.Second, func() bool {
		rr := s.Do("GET", "/services/"+svc.ID+"/task-runs?taskId="+taskID, nil)
		for _, it := range rr.JSON().Items() {
			if it.Str("source") == "cron" && it.Str("status") == "succeeded" {
				run = it
				return true
			}
		}
		return false
	}), "the cron service never ran")
	require.Contains(t, s.runOutput(run.Str("id")), "tick")
	require.True(t, eventually(10*time.Second, 500*time.Millisecond, func() bool { return s.taskContainers() == 0 }), "the run container is removed")
	require.Empty(t, s.Containers(svc.ID), "runs carry no service label: the service never has a container")

	// The card's view follows the run.
	require.True(t, eventually(15*time.Second, time.Second, func() bool {
		return s.Service(svc.ID).Obj("cron").Obj("lastRun").Str("status") == "succeeded"
	}), "cron.lastRun: %v", s.Service(svc.ID).Obj("cron"))

	// Stop pauses the schedule: no agent command, and the next tick is skipped.
	r = s.Do("POST", "/services/"+svc.ID+"/stop", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	_, hasCommand := r.JSON()["commandId"]
	require.False(t, hasCommand, "pausing a schedule queues no command: %s", r.Raw)
	require.Equal(t, "stopped", s.Service(svc.ID).Str("desiredState"))
	require.True(t, eventually(130*time.Second, 2*time.Second, func() bool {
		rr := s.Do("GET", "/services/"+svc.ID+"/task-runs?taskId="+taskID, nil)
		for _, it := range rr.JSON().Items() {
			if it.Str("status") == "skipped" && it.Str("error") == "the service is stopped" {
				return true
			}
		}
		return false
	}), "a paused schedule never recorded a skipped tick")

	// Resume.
	r = s.Do("POST", "/services/"+svc.ID+"/restart", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	require.Equal(t, "running", s.Service(svc.ID).Str("desiredState"))

	// The reconciler never saw a missing container to heal.
	var redeploys int
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'reconcile_redeploy' AND args->>'serviceId' = $1`, svc.ID).Scan(&redeploys))
	require.Zero(t, redeploys, "no reconcile_redeploy is enqueued for a cron service")
}
