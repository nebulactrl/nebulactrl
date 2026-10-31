package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// PostgreSQL read replicas, agent side:
// the replication db_admin actions, the replication poller state and the fence.
// The commands are typed: argv, SQL and scripts come from engines/replication.go.

// replState is what the engine knows about replication members and fences.
type replState struct {
	mu      sync.Mutex
	desired map[string]agentprotocol.ReplicationDesired // serviceID -> poll request
	status  map[string]agentprotocol.ReplicationStatus  // serviceID -> last poll
	polled  map[string]time.Time                        // serviceID -> last poll start
	fenced  map[string]bool
	// observed is set once the connection hands frames to ObserveFrame; latest
	// then identifies the newest frame (by the first element of its services),
	// so a slower, older Reconcile never re-applies a stale fence.
	observed bool
	latest   *agentprotocol.DesiredService
}

// setDesired records (or clears) the poll request of a service.
func (r *replState) setDesired(serviceID string, d *agentprotocol.ReplicationDesired) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d == nil {
		r.forget(serviceID)
		return
	}
	if r.desired == nil {
		r.desired = map[string]agentprotocol.ReplicationDesired{}
	}
	r.desired[serviceID] = *d
}

// forget drops a service's poll state; the caller holds r.mu.
func (r *replState) forget(serviceID string) {
	delete(r.desired, serviceID)
	delete(r.status, serviceID)
	delete(r.polled, serviceID)
}

// statusOf returns a copy of the last poll result of a service.
func (r *replState) statusOf(serviceID string) *agentprotocol.ReplicationStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.status[serviceID]
	if !ok {
		return nil
	}
	return &st
}

func (r *replState) isFenced(serviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fenced[serviceID]
}

// frameKey identifies a reconcile frame by its services slice (copies of a frame
// share the backing array). Empty frames share the nil key.
func frameKey(services []agentprotocol.DesiredService) *agentprotocol.DesiredService {
	if len(services) == 0 {
		return nil
	}
	return &services[0]
}

// appliesFrame reports whether a Reconcile of this frame should still set the
// fences and poll requests: always when frames are not observed on arrival
// (direct callers), else only for the newest frame.
func (r *replState) appliesFrame(services []agentprotocol.DesiredService) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.observed || frameKey(services) == r.latest
}

// isObserved reports whether frames reach the engine on arrival (ObserveFrame
// ran at least once); direct callers of Reconcile see false.
func (r *replState) isObserved() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observed
}

// ObserveFrame applies the fences and poll requests of a reconcile frame the
// moment it arrives, in frame order, on the connection's read loop. The rest of
// the reconcile runs asynchronously and can take seconds (proxy configuration,
// sweeps); a command that follows the frame, like the deploy that lifts a fence
// by rebuilding the old primary as a replica, must not see the previous state.
func (e *Engine) ObserveFrame(ctx context.Context, desired agentprotocol.Reconcile) {
	e.observeTableSeq(desired)
	if e.cfg.BuildOnly {
		return
	}
	e.repl.mu.Lock()
	e.repl.observed = true
	e.repl.latest = frameKey(desired.Services)
	e.repl.mu.Unlock()
	e.applyReplicationDesired(ctx, desired.Services)
}

// fencedMessage is the refusal of a deploy, restart or scale of a fenced service.
const fencedMessage = "this service is fenced: it was the primary of a database that has been promoted elsewhere and must not run as a primary again; rebuild it as a replica or delete it"

func (e *Engine) fencedResult(cmdID, serviceID string) (agentprotocol.ResultFrame, bool) {
	if !e.repl.isFenced(serviceID) {
		return agentprotocol.ResultFrame{}, false
	}
	return failedResult(cmdID, fencedMessage), true
}

// applyReplicationDesired replaces the poll requests and fences with the ones of
// a reconcile frame. A service that is not in the frame keeps what a local
// deploy recorded; fences come only from the control plane. The newly fenced
// services are stopped in the background (behind the service lock, so a deploy
// that started before the fence finishes first).
func (e *Engine) applyReplicationDesired(ctx context.Context, services []agentprotocol.DesiredService) {
	e.mu.Lock()
	local := make(map[string]bool, len(e.local))
	for id := range e.local {
		local[id] = true
	}
	e.mu.Unlock()

	inFrame := make(map[string]bool, len(services))
	var toFence []string
	nextFenced := map[string]bool{}
	e.repl.mu.Lock()
	if e.repl.desired == nil {
		e.repl.desired = map[string]agentprotocol.ReplicationDesired{}
	}
	for _, ds := range services {
		inFrame[ds.ServiceID] = true
		if ds.Replication != nil {
			e.repl.desired[ds.ServiceID] = *ds.Replication
		} else {
			e.repl.forget(ds.ServiceID)
		}
		if ds.Fenced {
			nextFenced[ds.ServiceID] = true
			toFence = append(toFence, ds.ServiceID)
		}
	}
	for id := range e.repl.desired {
		if !inFrame[id] && !local[id] {
			e.repl.forget(id)
		}
	}
	e.repl.fenced = nextFenced
	e.repl.mu.Unlock()

	sort.Strings(toFence)
	for _, id := range toFence {
		fctx, cancel := e.background(ctx, 0)
		e.bg.Add(1)
		go func() {
			defer e.bg.Done()
			defer cancel()
			e.fenceService(fctx, id)
		}()
	}
}

// fenceService stops every container of the service and sets its restart policy
// to "no", so neither Docker nor the agent starts it again.
func (e *Engine) fenceService(ctx context.Context, serviceID string) {
	defer e.lockService(serviceID)()
	if !e.repl.isFenced(serviceID) {
		return // un-fenced while waiting for the lock
	}
	cs, err := e.containersOf(ctx, serviceID, true)
	if err != nil {
		e.log.Warn("fence: list containers", "service_id", serviceID, "err", err)
		return
	}
	setter, canSet := e.cfg.Docker.(docker.RestartPolicySetter)
	grace := graceOf(0)
	for _, c := range cs {
		if c.RestartMode != "no" {
			if !canSet {
				e.log.Error("fence: the Docker engine cannot change restart policies; the container may be restarted by Docker", "container", c.Name)
			} else if err := setter.ContainerSetRestartPolicy(ctx, c.ID, "no"); err != nil && !errors.Is(err, docker.ErrNotFound) {
				e.log.Warn("fence: set restart policy", "container", c.Name, "err", err)
			}
		}
		if c.Running || c.Restarting {
			e.log.Info("fence: stopping the fenced database", "service_id", serviceID, "container", c.Name)
			if err := e.cfg.Docker.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
				e.log.Warn("fence: stop container", "container", c.Name, "err", err)
			}
		}
	}
	e.repl.mu.Lock()
	e.repl.forget(serviceID) // a fenced database is not polled
	e.repl.mu.Unlock()
}

// runningDB returns the running container of the service's current deployment.
func (e *Engine) runningDB(ctx context.Context, serviceID string) *docker.ContainerInfo {
	reps := e.currentReplicas(ctx, serviceID)
	for i := range reps {
		if reps[i].Running {
			return &reps[i]
		}
	}
	return nil
}

// execPlanIn runs a plan in the container and returns its stdout. A non-zero
// exit code is an error carrying the (redacted, truncated) output.
func (e *Engine) execPlanIn(ctx context.Context, containerID string, plan engines.ExecPlan, rd *redactor, timeout time.Duration) (string, error) {
	if plan.User != "" && plan.User != "postgres" {
		return "", fmt.Errorf("refusing to exec as user %q", plan.User)
	}
	var stdout, stderr limitedBuffer
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	code, err := e.cfg.Docker.ContainerExec(execCtx, containerID, plan.Argv,
		docker.ExecSpec{Env: plan.Env, User: plan.User, Stdin: strings.NewReader(plan.Stdin)}, &stdout, &stderr)
	if err != nil {
		return "", errors.New(rd.apply("exec in database container: " + err.Error()))
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
		return "", errors.New(rd.apply(fmt.Sprintf("%s exited with code %d: %s", plan.Argv[0], code, msg)))
	}
	return stdout.String(), nil
}

// handleReplAdmin runs the replication actions of db_admin (repl_*).
func (e *Engine) handleReplAdmin(ctx context.Context, cmd agentprotocol.Command, p agentprotocol.DBAdminPayload, spec engines.Spec, rd *redactor) agentprotocol.ResultFrame {
	res := agentprotocol.DBAdminResult{Action: p.Action}
	fail := func(msg string) agentprotocol.ResultFrame {
		return dbAdminResult(cmd.ID, agentprotocol.OutcomeFailed, res, rd.apply(msg))
	}
	if spec.Engine != engines.Postgres {
		return fail(engines.ErrNotPostgres.Error())
	}
	var a engines.ReplArgs
	if r := p.Repl; r != nil {
		a = engines.ReplArgs{Role: r.Role, Slot: r.Slot, MaxSlotWALKeepMB: r.MaxSlotWALKeepMB, LSN: r.LSN, Host: r.Host, Port: r.Port}
	}
	defer e.lockService(p.ServiceID)()

	if p.Action == agentprotocol.DBActionReplFinalLSN {
		return e.replFinalLSN(ctx, cmd, p, spec, rd)
	}

	target := e.runningDB(ctx, p.ServiceID)
	if target == nil {
		return fail("the database container is not running on this host")
	}
	const execTimeout = 3 * time.Minute
	run := func(plan engines.ExecPlan, err error) (string, error) {
		if err != nil {
			return "", err
		}
		return e.execPlanIn(ctx, target.ID, plan, rd, execTimeout)
	}

	switch p.Action {
	case agentprotocol.DBActionReplPrepare:
		a.NewPassword = cmd.Secrets[agentprotocol.SecretDBAdminNewPassword]
		if _, err := run(engines.HBAPlan(spec, true, a.Role)); err != nil {
			return fail(err.Error())
		}
		if _, err := run(engines.ReplPlan(spec, p.Action, a)); err != nil {
			return fail(err.Error())
		}
		res.Message = "Replication role, pg_hba.conf entry and WAL retention limit are in place."
	case agentprotocol.DBActionReplDrop:
		if _, err := run(engines.HBAPlan(spec, false, a.Role)); err != nil {
			return fail(err.Error())
		}
		if _, err := run(engines.ReplPlan(spec, p.Action, a)); err != nil {
			return fail(err.Error())
		}
		res.Message = "Replication slot, role and pg_hba.conf entry removed."
	case agentprotocol.DBActionReplFreeze, agentprotocol.DBActionReplPromote:
		out, err := run(engines.ReplPlan(spec, p.Action, a))
		if err != nil {
			return fail(err.Error())
		}
		lsn, err := engines.ParseLSN(out)
		if err != nil {
			return fail(err.Error())
		}
		res.LSN = lsn
		if p.Action == agentprotocol.DBActionReplFreeze {
			res.Message = "The database is read-only for new transactions; client sessions were ended."
		} else {
			if _, err := run(engines.PromoteCleanupPlan(spec)); err != nil {
				return fail("promoted, but cleaning the replica configuration failed: " + err.Error())
			}
			res.Message = "Promoted to primary."
		}
	case agentprotocol.DBActionReplUnfreeze:
		if _, err := run(engines.ReplPlan(spec, p.Action, a)); err != nil {
			return fail(err.Error())
		}
		res.Message = "The database accepts writes again."
	case agentprotocol.DBActionReplReconfigure:
		if _, err := run(engines.ReconfigurePlan(spec, a)); err != nil {
			return fail(err.Error())
		}
		if _, err := run(engines.ReplPlan(spec, p.Action, a)); err != nil {
			return fail(err.Error())
		}
		res.Message = "Replica upstream updated."
	case agentprotocol.DBActionReplStatus:
		out, err := run(engines.ReplPlan(spec, p.Action, a))
		if err != nil {
			return fail(err.Error())
		}
		st, err := engines.ParseReplStatus(out)
		if err != nil {
			return fail(err.Error())
		}
		st.CheckedAt = time.Now().UTC()
		res.Replication = &st
	default:
		return fail("unsupported replication action " + p.Action)
	}
	return dbAdminResult(cmd.ID, agentprotocol.OutcomeSucceeded, res, "")
}

// replFinalLSN reads the final checkpoint location of a stopped primary with a
// one-off pg_controldata container on its data volume (mounted read-only). It
// fails unless the cluster was shut down cleanly: after a crash the checkpoint
// location is not the last WAL position.
func (e *Engine) replFinalLSN(ctx context.Context, cmd agentprotocol.Command, p agentprotocol.DBAdminPayload, spec engines.Spec, rd *redactor) agentprotocol.ResultFrame {
	res := agentprotocol.DBAdminResult{Action: p.Action}
	fail := func(msg string) agentprotocol.ResultFrame {
		return dbAdminResult(cmd.ID, agentprotocol.OutcomeFailed, res, rd.apply(msg))
	}
	if p.Repl == nil || !engines.ValidVolumeName(p.Repl.DataVolume) {
		return fail("repl_final_lsn needs the data volume name")
	}
	d := e.cfg.Docker
	for _, c := range e.currentReplicas(ctx, p.ServiceID) {
		if c.Running || c.Restarting {
			return fail("the database is still running: stop it first")
		}
	}
	image := ""
	if reps := e.currentReplicas(ctx, p.ServiceID); len(reps) > 0 {
		image = reps[0].ImageID
	}
	if image == "" {
		image = spec.Image()
		if image == "" {
			return fail("unknown database image")
		}
		if _, err := d.ImageInspect(ctx, image); err != nil {
			if err := d.ImagePull(ctx, image, "", nil); err != nil {
				return fail("pull " + image + ": " + err.Error())
			}
		}
	}
	plan, err := engines.ReplPlan(spec, p.Action, engines.ReplArgs{})
	if err != nil {
		return fail(err.Error())
	}
	vol := agentprotocol.VolumeName(p.ServiceID, p.Repl.DataVolume)
	_, mount := spec.DataMount()
	cs := docker.ContainerSpec{
		Name: "nc-pgctl-" + agentprotocol.Short(p.ServiceID), Image: image,
		Entrypoint: plan.Argv,
		Labels:     map[string]string{agentprotocol.LabelManaged: "pg-replication", agentprotocol.LabelService: p.ServiceID},
		Mounts:     []docker.VolumeMount{{Volume: vol, Target: mount, ReadOnly: true}},
		// Root reads files owned by postgres; nothing else is needed.
		CapDrop: []string{"ALL"}, CapAdd: []string{"DAC_OVERRIDE", "DAC_READ_SEARCH"},
		SecurityOpt: []string{"no-new-privileges:true"},
	}
	code, out, err := e.runHelper(ctx, cs, 2*time.Minute, nil)
	if err != nil {
		return fail("pg_controldata: " + err.Error())
	}
	if code != 0 {
		return fail(fmt.Sprintf("pg_controldata exited with code %d: %s", code, tail(out)))
	}
	cd, err := engines.ParseControlData(out)
	if err != nil {
		return fail(err.Error())
	}
	if !cd.CleanShutdown() {
		return fail(fmt.Sprintf("the database did not shut down cleanly (cluster state %q), so its final position is unknown", cd.State))
	}
	res.FinalLSN = cd.CheckpointLSN
	res.Message = "Final checkpoint location read from the stopped database."
	return dbAdminResult(cmd.ID, agentprotocol.OutcomeSucceeded, res, "")
}

// runHelper runs a one-off container to completion and returns its exit code and
// output. Lines are passed to onLine as they appear. The container is always
// removed, also when ctx ends.
func (e *Engine) runHelper(ctx context.Context, spec docker.ContainerSpec, timeout time.Duration, onLine func(string)) (int, string, error) {
	d := e.cfg.Docker
	_ = d.ContainerRemove(ctx, spec.Name, true, false)
	id, err := d.ContainerCreate(ctx, spec)
	if err != nil {
		return 0, "", fmt.Errorf("create helper container: %w", err)
	}
	defer func() {
		cctx, cancel := detached(ctx, time.Minute)
		defer cancel()
		_ = d.ContainerRemove(cctx, id, true, false)
	}()
	if err := d.ContainerStart(ctx, id); err != nil {
		return 0, "", fmt.Errorf("start helper container: %w", err)
	}

	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out limitedBuffer
	var outMu sync.Mutex
	followed := make(chan struct{})
	go func() {
		defer close(followed)
		rc, err := d.ContainerLogs(hctx, id, docker.LogOptions{Tail: -1, Follow: true})
		if err != nil {
			return
		}
		defer rc.Close()
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if i := strings.IndexByte(line, ' '); i > 0 && strings.Contains(line[:i], "T") {
				line = line[i+1:] // drop the RFC 3339 timestamp prefix
			}
			outMu.Lock()
			_, _ = out.Write([]byte(line + "\n"))
			outMu.Unlock()
			if onLine != nil {
				onLine(line)
			}
		}
	}()

	text := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return out.String()
	}
	for hctx.Err() == nil {
		info, err := d.ContainerInspect(hctx, id)
		if err != nil {
			if hctx.Err() != nil {
				break
			}
			return 0, "", fmt.Errorf("inspect helper container: %w", err)
		}
		if !info.Running && !info.Restarting && info.State != "created" {
			select { // let the log follower drain
			case <-followed:
			case <-time.After(5 * time.Second):
			}
			return info.ExitCode, text(), nil
		}
		select {
		case <-hctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	if ctx.Err() != nil {
		return 0, text(), context.Cause(ctx)
	}
	return 0, text(), fmt.Errorf("timed out after %s", timeout)
}
