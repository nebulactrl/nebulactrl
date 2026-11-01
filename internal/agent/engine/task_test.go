package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func init() { taskPollInterval = 5 * time.Millisecond }

const (
	runID    = "a1a1a1a1-0000-4000-8000-0000000000aa"
	taskCtr  = "nc-task-web-a1a1a1a1"
	preCtr   = "nc-predeploy-web-d2d2d2d2"
	envToken = "tok-super-secret-value"
)

func taskCmd(t *testing.T, p agentprotocol.RunTaskPayload, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-task", Kind: agentprotocol.KindRunTask, Payload: raw, Secrets: secrets}
}

func basicTask() agentprotocol.RunTaskPayload {
	return agentprotocol.RunTaskPayload{
		RunID: runID, ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		Command: []string{"rake", "db:migrate", "--trace"}, EnvKeys: []string{"API_KEY", "MODE"}, SecretEnvKeys: []string{"API_KEY"},
		MemoryMB: 256, CPUs: 0.5, TimeoutSeconds: 30,
	}
}

// exitWith makes every container named name exit with code when started.
func exitWith(r *rig, name string, code int) {
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if c.Name == name {
			c.State, c.Running, c.ExitCode = "exited", false, code
		}
	}
}

func decodeTaskResult(t *testing.T, res agentprotocol.ResultFrame) agentprotocol.RunTaskResult {
	t.Helper()
	var rr agentprotocol.RunTaskResult
	require.NoError(t, json.Unmarshal(res.Data, &rr))
	return rr
}

func TestRunTaskRunsInServiceImageAndRemovesContainer(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // the running container's image is what tasks run in
	r.dk.logText[taskCtr] = "migrating\nAPI_KEY=" + envToken + "\ndone\n"
	exitWith(r, taskCtr, 0)

	secrets := map[string]string{"env:API_KEY": envToken, "env:MODE": "production"}
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, basicTask(), secrets))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	rr := decodeTaskResult(t, res)
	require.Equal(t, 0, rr.ExitCode)
	require.False(t, rr.TimedOut)

	require.Len(t, r.dk.created, 1)
	spec := r.dk.created[0]
	require.Equal(t, taskCtr, spec.Name)
	require.Equal(t, "sha256:old", spec.Image, "the image of the running container")
	require.Equal(t, []string{"rake"}, spec.Entrypoint)
	require.Equal(t, []string{"db:migrate", "--trace"}, spec.Cmd)
	require.Contains(t, spec.Env, "API_KEY="+envToken)
	require.Contains(t, spec.Env, "MODE=production")
	require.Empty(t, spec.Ports)
	require.Empty(t, spec.RestartPolicy, "a task never restarts")
	require.Equal(t, int64(256<<20), spec.MemoryBytes)
	require.Equal(t, int64(0.5*1e9), spec.NanoCPUs)
	require.Len(t, spec.Networks, 1, "a task joins its environment network only")
	require.Equal(t, agentprotocol.EnvNetwork(envID), spec.Networks[0].Network)
	_, hasService := spec.Labels[agentprotocol.LabelService]
	require.False(t, hasService, "a task container is not a replica of the service")
	require.Equal(t, runID, spec.Labels[agentprotocol.LabelTaskRun])

	_, exists := r.dk.state(taskCtr)
	require.False(t, exists, "always removed")
	require.True(t, running(t, r.dk, oldCtr), "the serving container is untouched")

	text := r.snd.allLogText()
	require.Contains(t, text, "migrating")
	require.NotContains(t, text, envToken, "secret values never reach the logs")
	require.Contains(t, text, "••••••")
	for _, f := range r.snd.logs {
		require.Equal(t, runID, f.TaskRunID)
		require.Empty(t, f.DeploymentID)
	}
}

func TestRunTaskNonZeroExitFails(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	exitWith(r, taskCtr, 3)

	res := r.eng.HandleCommand(context.Background(), taskCmd(t, basicTask(), map[string]string{"env:API_KEY": envToken, "env:MODE": "x"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "exited with code 3")
	require.Equal(t, 3, decodeTaskResult(t, res).ExitCode)
	_, exists := r.dk.state(taskCtr)
	require.False(t, exists, "removed even when it failed")
}

func TestRunTaskTimeoutStopsAndRemoves(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // the task container keeps running: it never exits by itself
	p := basicTask()
	p.EnvKeys, p.SecretEnvKeys = nil, nil
	p.TimeoutSeconds = 1

	start := time.Now()
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Less(t, time.Since(start), 10*time.Second)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	rr := decodeTaskResult(t, res)
	require.True(t, rr.TimedOut)
	require.Contains(t, res.Error, "timed out")
	require.Contains(t, r.dk.stopped, taskCtr)
	_, exists := r.dk.state(taskCtr)
	require.False(t, exists)
}

func TestRunTaskMissingVariableFailsBeforeAnyContainer(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, basicTask(), map[string]string{"env:MODE": "x"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "API_KEY was not delivered")
	require.Empty(t, r.dk.created)
}

func TestRunTaskWithoutContainerPullsReleaseImage(t *testing.T) {
	r := newRig(t, nil)
	p := basicTask()
	p.EnvKeys, p.SecretEnvKeys = nil, nil
	p.ImageRef = "traefik/whoami:latest"
	exitWith(r, taskCtr, 0)

	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{"traefik/whoami:latest"}, r.dk.pulled)
	require.Equal(t, "sha256:img-traefik/whoami:latest", r.dk.created[0].Image)

	// No container and no image reference: nothing to run.
	p.ImageRef = ""
	r2 := newRig(t, nil)
	res = r2.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "deploy it first")
}

func TestRunTaskAdoptsContainerOfRedeliveredCommand(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	c := r.dk.seed(taskCtr, "", "", false, time.Minute) // finished while the agent was down
	c.ExitCode = 0
	c.Labels = map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTaskRun: runID}

	p := basicTask()
	p.EnvKeys, p.SecretEnvKeys = nil, nil
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Empty(t, r.dk.created, "the user's command must not run a second time")
	_, exists := r.dk.state(taskCtr)
	require.False(t, exists)
}

func TestRunTaskInterruptedLeavesContainerForAdoption(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // the task container keeps running
	p := basicTask()
	p.EnvKeys, p.SecretEnvKeys = nil, nil

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()
	res := r.eng.HandleCommand(ctx, taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "agent stopped")
	require.True(t, running(t, r.dk, taskCtr), "the container is left running")
}

func TestRunTaskRejectsInvalidPayload(t *testing.T) {
	r := newRig(t, nil)
	p := basicTask()
	p.Command = nil
	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "command is empty")
}

func TestSweepRemovesOverdueTaskContainers(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.OrphanMinAge = time.Minute
	old := r.dk.seed("nc-task-web-old00000", "", "", false, time.Hour)
	old.Labels = map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTaskRun: "x"}
	fresh := r.dk.seed("nc-task-web-fresh000", "", "", false, time.Second)
	fresh.Labels = map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTaskRun: "y"}
	live := r.dk.seed("nc-task-web-live0000", "", "", true, time.Hour)
	live.Labels = map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelTaskRun: "z",
		labelTaskDeadline: "9999999999",
	}

	require.NoError(t, r.eng.sweepTaskContainers(context.Background()))
	_, oldExists := r.dk.state("nc-task-web-old00000")
	_, freshExists := r.dk.state("nc-task-web-fresh000")
	_, liveExists := r.dk.state("nc-task-web-live0000")
	require.False(t, oldExists)
	require.True(t, freshExists)
	require.True(t, liveExists, "a task still within its deadline is never swept")
}

// ---- pre-deploy ----

func preDeployPayload() agentprotocol.DeployPayload {
	p := basePayload()
	p.PreDeploy = &agentprotocol.PreDeploySpec{Command: []string{"rake", "db:migrate"}, TimeoutSeconds: 30}
	p.EnvKeys, p.SecretEnvKeys = []string{"DB_URL"}, []string{"DB_URL"}
	return p
}

func TestPreDeployFailureFailsDeployAndKeepsOldReleaseServing(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	r.dk.logText[preCtr] = "connecting\nERROR: relation missing\n"
	exitWith(r, preCtr, 1)

	res, dr := r.deploy(preDeployPayload(), map[string]string{"env:DB_URL": "postgres://u:supersecretpw@db/x"})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Pre-deploy command failed with exit code 1")
	require.Contains(t, dr.Message, "ERROR: relation missing", "the log tail is in the message")
	require.Contains(t, dr.Message, "previous release is still serving")

	require.Contains(t, r.snd.eventSteps(), "predeploy:started")
	require.Contains(t, r.snd.eventSteps(), "predeploy:failed")
	for _, s := range r.snd.eventSteps() {
		require.NotContains(t, s, "create:", "no container of the new release was created")
	}
	require.True(t, running(t, r.dk, oldCtr), "old container untouched")
	require.Empty(t, r.dk.stopped)
	_, preExists := r.dk.state(preCtr)
	require.False(t, preExists, "the pre-deploy container is removed")
	_, newExists := r.dk.state(newCtr)
	require.False(t, newExists)
	require.Empty(t, r.px.applies, "routes never changed")
	require.NotContains(t, dr.Message, "supersecretpw")
}

func TestPreDeployRunsInNewImageBeforeTheSwap(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	exitWith(r, preCtr, 0)

	res, dr := r.deploy(preDeployPayload(), map[string]string{"env:DB_URL": "postgres://u:supersecretpw@db/x"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	require.Len(t, r.dk.created, 2)
	pre, svc := r.dk.created[0], r.dk.created[1]
	require.Equal(t, preCtr, pre.Name, "pre-deploy first")
	require.Equal(t, svc.Image, pre.Image, "the new image")
	require.Equal(t, []string{"rake"}, pre.Entrypoint)
	require.Contains(t, pre.Env, "DB_URL=postgres://u:supersecretpw@db/x")
	require.Equal(t, newCtr, svc.Name)

	steps := strings.Join(r.snd.eventSteps(), ",")
	require.Less(t, strings.Index(steps, "predeploy:succeeded"), strings.Index(steps, "create:started"))
	require.NotContains(t, r.snd.allLogText(), "supersecretpw")
}

func TestDeployWithoutPreDeployCreatesNoTaskContainer(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 1)
	for _, s := range r.snd.eventSteps() {
		require.NotContains(t, s, "predeploy")
	}
}

func TestPreDeployTimeoutFailsTheDeploy(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // the pre-deploy container never exits
	p := preDeployPayload()
	p.PreDeploy.TimeoutSeconds = 1

	res, dr := r.deploy(p, map[string]string{"env:DB_URL": "x"})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "timed out after 1s")
	require.True(t, running(t, r.dk, oldCtr))
	_, preExists := r.dk.state(preCtr)
	require.False(t, preExists)
}

// ---- post-deploy ----

const postCtr = "nc-postdeploy-web-d2d2d2d2"

func postDeployPayload() agentprotocol.DeployPayload {
	p := basePayload()
	p.PostDeploy = &agentprotocol.PostDeploySpec{Command: []string{"rake", "cache:warm"}, TimeoutSeconds: 30}
	p.EnvKeys, p.SecretEnvKeys = []string{"DB_URL"}, []string{"DB_URL"}
	return p
}

func TestPostDeployRunsInNewImageAfterTheSwap(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	exitWith(r, postCtr, 0)

	res, dr := r.deploy(postDeployPayload(), map[string]string{"env:DB_URL": "postgres://u:supersecretpw@db/x"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.NotContains(t, dr.Message, "Warning")

	require.Len(t, r.dk.created, 2)
	svc, post := r.dk.created[0], r.dk.created[1]
	require.Equal(t, newCtr, svc.Name, "the release first")
	require.Equal(t, postCtr, post.Name)
	require.Equal(t, svc.Image, post.Image, "the new image")
	require.Equal(t, []string{"rake"}, post.Entrypoint)
	require.Contains(t, post.Env, "DB_URL=postgres://u:supersecretpw@db/x")

	steps := strings.Join(r.snd.eventSteps(), ",")
	require.Less(t, strings.Index(steps, "retire"), strings.Index(steps, "postdeploy:started"), "after the swap and retire")
	require.Contains(t, steps, "postdeploy:succeeded")
	_, exists := r.dk.state(postCtr)
	require.False(t, exists, "the post-deploy container is removed")
	require.NotContains(t, r.snd.allLogText(), "supersecretpw")
}

func TestPostDeployFailureIsWarnOnly(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	r.dk.logText[postCtr] = "warming\nERROR: cache unreachable\n"
	exitWith(r, postCtr, 3)

	res, dr := r.deploy(postDeployPayload(), map[string]string{"env:DB_URL": "x"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Warning: post-deploy command failed with exit code 3")

	steps := r.snd.eventSteps()
	require.Contains(t, steps, "postdeploy:warning")
	require.NotContains(t, steps, "postdeploy:failed", "a warning, never a failed step")
	require.Contains(t, r.snd.allLogText(), "ERROR: cache unreachable", "the command output is in the deploy log")
	require.True(t, running(t, r.dk, newCtr), "the new release keeps serving")
	_, exists := r.dk.state(postCtr)
	require.False(t, exists)
}

func TestPostDeployTimeoutIsWarnOnly(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // the post-deploy container never exits
	p := postDeployPayload()
	p.PostDeploy.TimeoutSeconds = 1

	res, dr := r.deploy(p, map[string]string{"env:DB_URL": "x"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "timed out after 1s")
	require.Contains(t, r.snd.eventSteps(), "postdeploy:warning")
	require.True(t, running(t, r.dk, newCtr))
}

func TestPostDeployInvalidCommandIsWarnOnly(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	p := postDeployPayload()
	p.PostDeploy.Command = []string{" "}

	res, dr := r.deploy(p, map[string]string{"env:DB_URL": "x"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "invalid post-deploy command")
	require.Len(t, r.dk.created, 1, "no task container")
}

// A failed deploy never reaches the post-deploy step.
func TestPostDeploySkippedWhenTheDeployFails(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	r.dk.logText[preCtr] = "boom\n"
	exitWith(r, preCtr, 1)
	p := preDeployPayload()
	p.PostDeploy = &agentprotocol.PostDeploySpec{Command: []string{"warm"}}

	res, _ := r.deploy(p, map[string]string{"env:DB_URL": "x"})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	for _, s := range r.snd.eventSteps() {
		require.NotContains(t, s, "postdeploy")
	}
}

func TestDeployWithoutPostDeployCreatesNoTaskContainer(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 1)
	for _, s := range r.snd.eventSteps() {
		require.NotContains(t, s, "postdeploy")
	}
}
