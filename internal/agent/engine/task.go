package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Task containers: run_task commands (cron, one-off) and the pre-deploy step of
// a deploy run a command in a throwaway container of a service image. They get
// the service's environment and join the service's networks (never the shared
// edge network), publish no ports and never restart. Their labels deliberately omit
// agentprotocol.LabelService, so they are never mistaken for a replica of the
// service (inventory, orphan sweep, observed state ignore them).

// labelTaskDeadline carries the unix time after which a task container is
// overdue; the orphan sweep of task containers uses it.
const labelTaskDeadline = "nebulactrl.task-deadline"

const (
	// taskStopGrace is how long a timed-out task gets between SIGTERM and SIGKILL.
	taskStopGrace = 10 * time.Second
	// taskLogDrain bounds the wait for trailing log lines after the container exited.
	taskLogDrain = 5 * time.Second
	// taskOverdueGrace is added to a task container's deadline before the sweep
	// removes a still-running one.
	taskOverdueGrace = 10 * time.Minute
	taskLogMaxSize   = "10m"
)

// taskPollInterval is how often a running task container is inspected.
var taskPollInterval = 500 * time.Millisecond

// oneShot describes one throwaway container.
type oneShot struct {
	Name          string
	Image         string // image ID or reference
	Argv          []string
	Env           []string
	Kind          string // "task" or "predeploy"
	RunID         string // task run ID, or the deployment ID for pre-deploy
	ServiceID     string
	EnvironmentID string
	MemoryMB      int
	CPUs          float64
	Timeout       time.Duration
	// Log receives every output line (stdout and stderr, in order).
	Log func(ts time.Time, text string)
	// Net is the network membership (the service's plan, without an alias).
	Net netPlan
}

// oneShotOutcome is how a one-shot container ended.
type oneShotOutcome struct {
	ExitCode   int
	TimedOut   bool
	OOMKilled  bool
	StartedAt  time.Time
	FinishedAt time.Time
	// Err is set when the command could not be run at all (create or start
	// failed, the daemon failed).
	Err error
	// Interrupted is set when ctx ended (the agent is shutting down). The
	// container is left running so the redelivered command can adopt it.
	Interrupted bool
	// Canceled is set (with Interrupted) when a person canceled the deploy the
	// command belongs to: the container was removed, nothing may adopt it.
	Canceled bool
}

// plan returns the oneShot's network plan: the service's plan without an alias
// (a task is a client, not something other services dial).
func (r oneShot) plan() netPlan {
	p := r.Net
	p.Alias = ""
	if p.EnvironmentID == "" {
		p.EnvironmentID = r.EnvironmentID
	}
	return p
}

// runOneShot creates (or adopts) the container, streams its output, waits for
// it to exit or time out and removes it. A container named r.Name that already
// exists is adopted instead of started again: a redelivered command must never
// run a user's command twice.
func (e *Engine) runOneShot(ctx context.Context, r oneShot) oneShotOutcome {
	eng := e.cfg.Docker
	out := oneShotOutcome{}
	deadline := time.Now().Add(r.Timeout)

	var id string
	adopted := false
	if info, err := eng.ContainerInspect(ctx, r.Name); err == nil {
		id, adopted = info.ID, true
		if !info.StartedAt.IsZero() && info.StartedAt.Add(r.Timeout).Before(deadline) {
			deadline = info.StartedAt.Add(r.Timeout)
		}
	} else if !errors.Is(err, docker.ErrNotFound) {
		out.Err = fmt.Errorf("inspect container %s: %w", r.Name, err)
		return out
	}

	if !adopted {
		plan := r.plan()
		if err := plan.ensure(ctx, e.ensureNetwork); err != nil {
			out.Err = err
			return out
		}
		spec := docker.ContainerSpec{
			Name: r.Name, Image: r.Image, Env: r.Env, Entrypoint: r.Argv[:1], Cmd: r.Argv[1:],
			Labels: map[string]string{
				agentprotocol.LabelManaged:     "true",
				agentprotocol.LabelEnvironment: r.EnvironmentID,
				agentprotocol.LabelTaskRun:     r.RunID,
				agentprotocol.LabelTaskService: r.ServiceID,
				agentprotocol.LabelTaskKind:    r.Kind,
				labelTaskDeadline:              strconv.FormatInt(deadline.Unix(), 10),
			},
			Networks:  plan.Attachments(),
			LogConfig: &docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": taskLogMaxSize, "max-file": "2"}},
		}
		if r.MemoryMB > 0 {
			spec.MemoryBytes = int64(r.MemoryMB) * 1024 * 1024
		}
		if r.CPUs > 0 {
			spec.NanoCPUs = int64(r.CPUs * 1e9)
		}
		var err error
		id, err = eng.ContainerCreate(ctx, spec)
		if err != nil {
			out.Err = fmt.Errorf("create container: %w", err)
			return out
		}
		if err := eng.ContainerStart(ctx, id); err != nil {
			out.Err = fmt.Errorf("start container: %w", err)
			e.removeTaskContainer(ctx, id)
			return out
		}
	}
	out.StartedAt = time.Now()

	// Stream the output until the container stops.
	lctx, lcancel := context.WithCancel(context.WithoutCancel(ctx))
	defer lcancel()
	logsDone := make(chan struct{})
	tail := -1
	if adopted {
		tail = 0 // lines before the agent restarted were already sent
	}
	rc, lerr := eng.ContainerLogs(lctx, id, docker.LogOptions{Tail: tail, Follow: true})
	if lerr != nil {
		close(logsDone)
		if r.Log != nil {
			r.Log(time.Now(), "[nebulactrl] could not read the container output: "+lerr.Error())
		}
	} else {
		go func() {
			defer close(logsDone)
			defer rc.Close()
			_ = logs.ReadLines(rc, func(ts time.Time, text string) {
				if r.Log != nil {
					r.Log(ts, text)
				}
			})
		}()
	}

	info, timedOut, interrupted, err := e.waitOneShot(ctx, id, deadline)
	switch {
	case interrupted:
		lcancel()
		<-logsDone
		out.Interrupted = true
		if canceled(ctx) {
			e.removeTaskContainer(ctx, id)
			out.Canceled = true
		}
		return out
	case err != nil:
		out.Err = err
	case timedOut:
		out.TimedOut = true
		sctx, scancel := detached(ctx, taskStopGrace+30*time.Second)
		grace := taskStopGrace
		if err := eng.ContainerStop(sctx, id, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) && r.Log != nil {
			r.Log(time.Now(), "[nebulactrl] could not stop the container: "+err.Error())
		}
		scancel()
		if after, err := eng.ContainerInspect(ctx, id); err == nil {
			info = after
		}
	}
	out.ExitCode, out.OOMKilled = info.ExitCode, info.OOMKilled
	out.FinishedAt = time.Now()
	if !info.FinishedAt.IsZero() && info.FinishedAt.After(out.StartedAt) {
		out.FinishedAt = info.FinishedAt
	}

	select {
	case <-logsDone:
	case <-time.After(taskLogDrain):
		lcancel()
		<-logsDone
	}
	e.removeTaskContainer(ctx, id)
	return out
}

// waitOneShot polls the container until it is no longer running, the deadline
// passes (timedOut) or ctx ends (interrupted).
func (e *Engine) waitOneShot(ctx context.Context, id string, deadline time.Time) (info docker.ContainerInfo, timedOut, interrupted bool, err error) {
	t := time.NewTicker(taskPollInterval)
	defer t.Stop()
	for {
		info, err = e.cfg.Docker.ContainerInspect(ctx, id)
		switch {
		case ctx.Err() != nil:
			return info, false, true, nil
		case err != nil:
			return info, false, false, fmt.Errorf("inspect container: %w", err)
		case !info.Running && !info.Restarting && info.State != "created":
			return info, false, false, nil
		case !time.Now().Before(deadline):
			return info, true, false, nil
		}
		select {
		case <-ctx.Done():
			return info, false, true, nil
		case <-t.C:
		}
	}
}

func (e *Engine) removeTaskContainer(ctx context.Context, id string) {
	cctx, cancel := detached(ctx, time.Minute)
	defer cancel()
	if err := e.cfg.Docker.ContainerRemove(cctx, id, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		e.log.Warn("remove task container", "container", id, "err", err)
	}
}

// sweepTaskContainers removes task containers nobody is waiting for: exited
// ones older than OrphanMinAge and running ones long past their deadline (the
// agent that started them is gone and no redelivered command adopted them).
func (e *Engine) sweepTaskContainers(ctx context.Context) error {
	cs, err := e.cfg.Docker.ContainerList(ctx, docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelTaskRun: ""},
	})
	if err != nil {
		return fmt.Errorf("sweep: list task containers: %w", err)
	}
	var errs []error
	for _, c := range cs {
		overdue := time.Since(c.Created) >= e.cfg.OrphanMinAge
		if c.Running || c.Restarting {
			secs, perr := strconv.ParseInt(c.Labels[labelTaskDeadline], 10, 64)
			overdue = perr == nil && time.Now().After(time.Unix(secs, 0).Add(taskOverdueGrace))
		}
		if !overdue {
			continue
		}
		e.log.Info("removing orphaned task container", "container", c.Name)
		grace := 10 * time.Second
		if c.Running || c.Restarting {
			if err := e.cfg.Docker.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
				errs = append(errs, fmt.Errorf("sweep: stop %s: %w", c.Name, err))
				continue
			}
		}
		if err := e.cfg.Docker.ContainerRemove(ctx, c.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Errorf("sweep: remove %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// ---- run_task ----

func taskResultFrame(id string, r agentprotocol.RunTaskResult, msg string) agentprotocol.ResultFrame {
	data, _ := json.Marshal(r)
	f := agentprotocol.ResultFrame{CommandID: id, Outcome: r.Outcome, Data: data}
	if r.Outcome != agentprotocol.OutcomeSucceeded {
		f.Error = msg
	}
	return f
}

func (e *Engine) handleRunTask(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.RunTaskPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid run_task payload: "+err.Error())
	}
	switch {
	case p.RunID == "" || p.ServiceID == "" || p.ServiceSlug == "" || p.EnvironmentID == "":
		return failedResult(cmd.ID, "invalid run_task payload: runId, serviceId, serviceSlug and environmentId are required")
	}
	if err := agentprotocol.ValidTaskCommand(p.Command); err != nil {
		return failedResult(cmd.ID, "invalid run_task payload: "+err.Error())
	}
	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = agentprotocol.DefaultTaskTimeoutSeconds * time.Second
	}
	if timeout > agentprotocol.MaxTaskTimeoutSeconds*time.Second {
		timeout = agentprotocol.MaxTaskTimeoutSeconds * time.Second
	}

	red := newRedactor(cmd.Secrets, p.SecretEnvKeys)
	lb := logs.NewBatcher(agentprotocol.LogFrame{
		TaskRunID: p.RunID, ServiceID: p.ServiceID, Stream: "task",
	}, e.cfg.Sender.Log, red.apply)
	defer lb.Close()
	log := e.log.With("task_run_id", p.RunID, "service_id", p.ServiceID)

	fail := func(msg string) agentprotocol.ResultFrame {
		msg = red.apply(msg)
		lb.Add("[nebulactrl] " + msg)
		return taskResultFrame(cmd.ID, agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeFailed, ExitCode: -1, Message: msg}, msg)
	}

	env := make([]string, 0, len(p.EnvKeys)+2)
	for _, k := range p.EnvKeys {
		v, ok := cmd.Secrets[agentprotocol.SecretEnvPrefix+k]
		if !ok {
			return fail("variable " + k + " was not delivered with the command")
		}
		env = append(env, k+"="+v)
	}
	env = replicaEnv(env, 0, 1)

	image, err := e.taskImage(ctx, p, cmd.Secrets, lb.Add)
	if err != nil {
		if ctx.Err() != nil {
			return fail("agent stopped before the task could start")
		}
		return fail(err.Error())
	}

	lb.Add("[nebulactrl] $ " + red.apply(shellJoin(p.Command)))
	log.Info("task started", "container", agentprotocol.TaskContainerName(p.ServiceSlug, p.RunID))
	o := e.runOneShot(ctx, oneShot{
		Name: agentprotocol.TaskContainerName(p.ServiceSlug, p.RunID), Image: image, Argv: p.Command, Env: env,
		Kind: "task", RunID: p.RunID, ServiceID: p.ServiceID, EnvironmentID: p.EnvironmentID,
		MemoryMB: p.MemoryMB, CPUs: p.CPUs, Timeout: timeout, Net: planOfTask(p),
		Log: func(ts time.Time, text string) { lb.AddAt(ts, text) },
	})
	res := agentprotocol.RunTaskResult{StartedAt: o.StartedAt, FinishedAt: o.FinishedAt, ExitCode: o.ExitCode}
	switch {
	case o.Interrupted:
		log.Info("task interrupted by shutdown; the container is left for adoption")
		return fail("agent stopped while the task was running")
	case o.Err != nil:
		return fail(o.Err.Error())
	case o.TimedOut:
		res.Outcome, res.TimedOut = agentprotocol.OutcomeFailed, true
		res.Message = "timed out after " + timeout.String()
	case o.ExitCode != 0:
		res.Outcome = agentprotocol.OutcomeFailed
		res.Message = fmt.Sprintf("exited with code %d", o.ExitCode)
		if o.OOMKilled {
			res.Message += " (out of memory: raise the service's memory limit)"
		}
	default:
		res.Outcome = agentprotocol.OutcomeSucceeded
		res.Message = "exited with code 0"
	}
	lb.Add("[nebulactrl] " + res.Message)
	log.Info("task finished", "outcome", res.Outcome, "exit_code", o.ExitCode, "timed_out", o.TimedOut)
	return taskResultFrame(cmd.ID, res, res.Message)
}

// taskImage picks the image a task runs: the image of the service's current
// container on this host (exactly what serves traffic), else the release's
// image reference.
func (e *Engine) taskImage(ctx context.Context, p agentprotocol.RunTaskPayload, secrets map[string]string, logf func(string)) (string, error) {
	if cur := e.currentReplicas(ctx, p.ServiceID); len(cur) > 0 && cur[0].ImageID != "" {
		return cur[0].ImageID, nil
	}
	if p.ImageRef == "" {
		return "", errors.New("the service has no container on this server and no image to run; deploy it first")
	}
	eng := e.cfg.Docker
	if info, err := eng.ImageInspect(ctx, p.ImageRef); err == nil {
		return info.ID, nil
	}
	logf("[nebulactrl] pulling " + p.ImageRef)
	auth := ""
	if p.HasRegistryAuth {
		auth = secrets[agentprotocol.SecretRegistryAuth]
	}
	if err := eng.ImagePull(ctx, p.ImageRef, auth, logf); err != nil {
		return "", fmt.Errorf("the service's image %s is not on this server and could not be pulled: %w", p.ImageRef, err)
	}
	info, err := eng.ImageInspect(ctx, p.ImageRef)
	if err != nil {
		return "", fmt.Errorf("inspect pulled image: %w", err)
	}
	return info.ID, nil
}

// shellJoin renders argv for display, quoting words that need it.
func shellJoin(argv []string) string {
	words := make([]string, len(argv))
	for i, w := range argv {
		words[i] = shellQuoteWord(w)
	}
	return strings.Join(words, " ")
}

// ---- pre-deploy ----

// tailRing keeps the last n lines written to it.
type tailRing struct {
	mu    sync.Mutex
	n     int
	lines []string
}

func (t *tailRing) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, s)
	if len(t.lines) > t.n {
		t.lines = t.lines[len(t.lines)-t.n:]
	}
}

func (t *tailRing) get() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

// preDeployTailLines is how many output lines a failed pre-deploy command
// puts into the deployment's message.
const preDeployTailLines = 20

// preDeploy runs the pre-deploy command in a container of the new image with
// the deploy's environment, before any container of the new release exists. It
// returns ok=false with the deployment's failure message when the command
// failed; the previous release was not touched.
func (d *deploy) preDeploy(ctx context.Context, env []string) (msg string, ok bool) {
	pd := d.p.PreDeploy
	if pd == nil || len(pd.Command) == 0 {
		return "", true
	}
	if err := agentprotocol.ValidTaskCommand(pd.Command); err != nil {
		d.event(agentprotocol.StepPredeploy, agentprotocol.StepFailed, "Invalid pre-deploy command: "+err.Error())
		return "Invalid pre-deploy command: " + err.Error(), false
	}
	timeout := time.Duration(pd.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = agentprotocol.DefaultPreDeployTimeoutSeconds * time.Second
	}
	if timeout > agentprotocol.MaxPreDeployTimeoutSeconds*time.Second {
		timeout = agentprotocol.MaxPreDeployTimeoutSeconds * time.Second
	}
	cmdline := d.red.apply(shellJoin(pd.Command))
	d.event(agentprotocol.StepPredeploy, agentprotocol.StepStarted, "Running pre-deploy command: "+cmdline)

	ring := &tailRing{n: preDeployTailLines}
	o := d.e.runOneShot(ctx, oneShot{
		Name: agentprotocol.PreDeployContainerName(d.p.ServiceSlug, d.p.DeploymentID), Image: d.imageID, Argv: pd.Command,
		Env: replicaEnv(env, 0, len(d.names)), Kind: "predeploy", RunID: d.p.DeploymentID, ServiceID: d.p.ServiceID,
		EnvironmentID: d.p.EnvironmentID, MemoryMB: d.p.MemoryMB, CPUs: d.p.CPUs, Timeout: timeout, Net: d.plan(),
		Log: func(ts time.Time, text string) {
			text = d.red.apply(text)
			ring.add(text)
			d.dlog.AddAt(ts, text)
		},
	})

	var reason string
	switch {
	case o.Interrupted:
		return "agent stopped during deployment", false
	case o.Err != nil:
		reason = "Pre-deploy command could not run: " + o.Err.Error()
	case o.TimedOut:
		reason = "Pre-deploy command timed out after " + timeout.String()
	case o.ExitCode != 0:
		reason = fmt.Sprintf("Pre-deploy command failed with exit code %d", o.ExitCode)
		if o.OOMKilled {
			reason += " (out of memory: raise the service's memory limit)"
		}
	default:
		d.event(agentprotocol.StepPredeploy, agentprotocol.StepSucceeded, "Pre-deploy command finished")
		return "", true
	}
	d.event(agentprotocol.StepPredeploy, agentprotocol.StepFailed, reason)

	var b strings.Builder
	b.WriteString(d.red.apply(reason))
	if running, err := d.e.containersOf(ctx, d.p.ServiceID, false); err == nil && len(running) > 0 {
		b.WriteString("\nNothing was changed: the previous release is still serving.")
	}
	if tail := ring.get(); len(tail) > 0 {
		fmt.Fprintf(&b, "\n\nLast %d output lines:\n%s", len(tail), strings.Join(tail, "\n"))
	}
	return b.String(), false
}

// postDeploy runs the post-deploy command in a one-shot container of the new
// image with the deploy's environment, after the new release is live and the
// previous one retired. Failure is warn-only: the problem goes to the deploy
// log, a `warning` step event (the control plane raises a notification) and
// the result message, and the deployment still succeeds. The new release is
// already serving, so nothing here may fail the deploy.
func (d *deploy) postDeploy(ctx context.Context, env []string) {
	pd := d.p.PostDeploy
	if pd == nil || len(pd.Command) == 0 {
		return
	}
	if err := agentprotocol.ValidTaskCommand(pd.Command); err != nil {
		d.postDeployWarn("Invalid post-deploy command: " + err.Error())
		return
	}
	timeout := time.Duration(pd.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = agentprotocol.DefaultPostDeployTimeoutSeconds * time.Second
	}
	if timeout > agentprotocol.MaxPostDeployTimeoutSeconds*time.Second {
		timeout = agentprotocol.MaxPostDeployTimeoutSeconds * time.Second
	}
	// A cancel request while this step runs (before its first event, so a cancel keyed to it lands) stops just the command: the release is live.
	ctx, stop := d.ctl.postDeployContext(ctx)
	defer stop()
	d.event(agentprotocol.StepPostdeploy, agentprotocol.StepStarted, "Running post-deploy command: "+d.red.apply(shellJoin(pd.Command)))

	o := d.e.runOneShot(ctx, oneShot{
		Name: agentprotocol.PostDeployContainerName(d.p.ServiceSlug, d.p.DeploymentID), Image: d.imageID, Argv: pd.Command,
		Env: replicaEnv(env, 0, len(d.names)), Kind: "postdeploy", RunID: d.p.DeploymentID, ServiceID: d.p.ServiceID,
		EnvironmentID: d.p.EnvironmentID, MemoryMB: d.p.MemoryMB, CPUs: d.p.CPUs, Timeout: timeout, Net: d.plan(),
		Log: func(ts time.Time, text string) {
			text = d.red.apply(text)
			d.dlog.AddAt(ts, text)
		},
	})

	var reason string
	switch {
	case o.Canceled:
		reason = "Post-deploy command was canceled"
	case o.Interrupted:
		reason = "Post-deploy command was interrupted: the agent stopped"
	case o.Err != nil:
		reason = "Post-deploy command could not run: " + o.Err.Error()
	case o.TimedOut:
		reason = "Post-deploy command timed out after " + timeout.String()
	case o.ExitCode != 0:
		reason = fmt.Sprintf("Post-deploy command failed with exit code %d", o.ExitCode)
		if o.OOMKilled {
			reason += " (out of memory: raise the service's memory limit)"
		}
	default:
		d.event(agentprotocol.StepPostdeploy, agentprotocol.StepSucceeded, "Post-deploy command finished")
		return
	}
	d.postDeployWarn(reason)
}

// postDeployWarn records a post-deploy problem as a warning: the deploy
// continues and the result carries it. The command's output is already in the
// deploy log.
func (d *deploy) postDeployWarn(reason string) {
	reason = d.red.apply(reason)
	d.event(agentprotocol.StepPostdeploy, agentprotocol.StepWarning, reason+". The new release is live; the deployment was not rolled back.")
	if d.warning != "" {
		d.warning += "; "
	}
	d.warning += strings.ToLower(reason[:1]) + reason[1:]
}
