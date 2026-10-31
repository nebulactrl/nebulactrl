package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	failureLogLines = 50
	// maxReplicas is a sanity bound; the control plane enforces the product
	// limit (1..10).
	maxReplicas = 100
)

// deploy is the state of one deploy (or scale) command.
type deploy struct {
	e    *Engine
	cmd  agentprotocol.Command
	p    agentprotocol.DeployPayload
	red  *redactor
	dlog *logs.Batcher // stream "deploy"; nil when silent
	log  *slog.Logger  // carries correlation_id = deployment ID

	// names are the replica container names nc-<slug>-<dep8>-<i>; the slice
	// index is the replica index. For a scale command they are the final set.
	names []string
	// ids holds the container ID per replica index once known (created by this
	// attempt, resumed, or - for scale - already running). created lists only
	// the containers this attempt made, which a failure must remove.
	ids     []string
	created []string
	// index maps a slot in names/ids to its replica index; nil means identity.
	// A scale-up creates replicas whose indices are not 0..n-1.
	index []int
	// cloneEnv, when set, is the environment new replicas get instead of the
	// one resolved from the command's secrets (reconcile convergence copies it
	// from a running replica).
	cloneEnv []string

	// silent suppresses deploy events and deploy-stream logs: a scale command
	// targets an already finished deployment whose history must not change.
	silent bool
	// live makes routeTable derive this service's routes from the proxy's live
	// table (scale) instead of the payload.
	live bool
	// scaledAt is the control plane's stamp of the replica count a scale or
	// convergence applies (zero for a deploy); recordCurrent keeps it with the
	// local entry so an older frame cannot undo the count.
	scaledAt time.Time

	imageID     string
	imageDigest string
	warning     string
	// buildInfo describes the build of a git source (nil otherwise).
	buildInfo *agentprotocol.BuildInfo
	// tailnet is the outcome of the tailnet step (nil without a tailnet spec).
	tailnet *agentprotocol.ServiceTailnetStatus
	// shellEntry caches whether the image's entrypoint is `<shell> -c`
	// (Railpack images), see startCommand.
	shellEntry *bool
	// ctl is the cancel handle of a running deploy command (nil for scale and
	// other silent deploys, which cannot be canceled).
	ctl *deployRun
	// dbg is the deployment's debug stream (nil for scale and other silent
	// deploys; every debugLog method accepts nil).
	dbg *debugLog
	// timings are the recorded phase durations ("pull 1.2s"), in finishing order.
	timingsMu sync.Mutex
	timings   []string
	// parent is the deploy a per-process view was made from (see forProcess);
	// nil for the command's own deploy. views are the per-process views of a
	// multi-process deploy, in rollout order.
	parent *deploy
	views  []*deploy
}

// deployState tracks what a failed deployment must undo.
type deployState struct {
	recreate     bool
	prev         []docker.ContainerInfo // every other managed container of the service
	serving      []docker.ContainerInfo // prev containers that were running when we started
	toRestart    []docker.ContainerInfo // containers we stopped (recreate) and must restart on failure
	prevRoutes   []agentprotocol.Route
	verifyFailed bool  // the new route went live, failed verification and was withdrawn
	routeErr     error // restoring the previous table failed

	applied        bool // the new route table was applied (so a restore is due)
	routesRestored bool // the previous table is back after a cancel or a failed verification
}

func (e *Engine) handleDeploy(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DeployPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid deploy payload: "+err.Error())
	}
	if p.Stack != nil {
		return e.handleStackDeploy(ctx, cmd, p)
	}
	if err := validatePayload(p); err != nil {
		return failedResult(cmd.ID, "invalid deploy payload: "+err.Error())
	}
	// Registered before the service lock: a cancel can reach a deploy that
	// still waits for another operation on the service.
	ctx, ctl, release := e.cancel.start(ctx, p.DeploymentID)
	defer release()
	unlock := e.lockService(p.ServiceID)
	defer unlock()
	if res, fenced := e.fencedResult(cmd.ID, p.ServiceID); fenced {
		return res
	}

	red := newRedactor(cmd.Secrets, secretKeysForRedaction(p))
	d := &deploy{e: e, cmd: cmd, p: p, red: red, ctl: ctl}
	d.dlog = logs.NewBatcher(agentprotocol.LogFrame{
		DeploymentID: p.DeploymentID, ServiceID: p.ServiceID, Stream: agentprotocol.StreamDeploy,
	}, e.cfg.Sender.Log, red.apply)
	defer d.dlog.Close()
	d.dbg = newDebugLog(p, e.cfg.Sender.Log, red.apply)
	defer d.dbg.Close()

	d.log = e.log.With("correlation_id", p.DeploymentID, "service_id", p.ServiceID)
	d.log.Info("deploy started", "strategy", p.Strategy, "replicas", p.Replicas)
	began := time.Now()
	d.debugf("command %s: deploy %s (release %s), strategy %s, %d replica(s), port %d, stop grace %ds",
		cmd.ID, p.ServiceSlug, shortID(p.ReleaseID), p.Strategy, p.Replicas, p.Port, p.StopGraceSeconds)
	d.debugf("source: %s", sourceLabel(p))
	d.debugf("health check: %s (path %q, port %d, deadline %ds, interval %ds)", healthLabel(p.Healthcheck),
		p.Healthcheck.Path, p.Healthcheck.Port, p.Healthcheck.DeadlineSeconds, p.Healthcheck.IntervalSeconds)
	d.debugf("delivered: %d variable(s) (%d secret, values never logged), %d file(s), %d volume(s), %d host(s)",
		len(p.EnvKeys), len(p.SecretEnvKeys), len(p.Files), len(p.Volumes), len(p.Hosts))
	d.debugProcesses()
	var res agentprotocol.DeployResult
	if canceled(ctx) {
		res = d.abortCanceled(ctx, &deployState{}, false) // canceled before it started
	} else {
		res = d.run(ctx)
	}
	d.debugf("finished: outcome %s in %s; phases: %s", res.Outcome, time.Since(began).Round(time.Millisecond), d.timingSummary())
	d.log.Info("deploy finished", "outcome", res.Outcome)
	return deployResultFrame(cmd.ID, res)
}

func validatePayload(p agentprotocol.DeployPayload) error {
	switch {
	case p.DeploymentID == "":
		return errors.New("deploymentId is required")
	case p.ServiceID == "":
		return errors.New("serviceId is required")
	case p.ServiceSlug == "":
		return errors.New("serviceSlug is required")
	case p.EnvironmentID == "":
		return errors.New("environmentId is required")
	case p.Replicas < 0 || p.Replicas > maxReplicas:
		return fmt.Errorf("replicas %d is out of range", p.Replicas)
	case (p.Image == nil) == (p.Git == nil):
		return errors.New("exactly one of image or git is required")
	}
	if err := validateProcessSpecs(p); err != nil {
		return err
	}
	if err := validateDockerOptions(p); err != nil {
		return fmt.Errorf("docker options: %w", err)
	}
	return nil
}

func (d *deploy) logf(format string, a ...any) {
	d.emit(d.procPrefix() + fmt.Sprintf(format, a...))
}

// emit writes one line to the deploy's log stream (the agent's own log when
// the deploy is silent).
func (d *deploy) emit(line string) {
	if d.dlog == nil {
		d.log.Info(d.red.apply(line))
		return
	}
	d.dlog.Add(line)
}

func (d *deploy) event(step, status, msg string) {
	if status == agentprotocol.StepStarted && step != agentprotocol.StepCancel {
		d.ctl.setStep(step)
	}
	if msg != "" {
		msg = d.procPrefix() + msg
	}
	if d.silent {
		if msg != "" {
			d.log.Info(d.red.apply(msg), "step", step, "status", status)
		}
		return
	}
	d.e.cfg.Sender.Event(agentprotocol.EventFrame{
		DeploymentID: d.p.DeploymentID, Step: step, Status: status, Message: d.red.apply(msg),
	})
	if msg != "" {
		d.emit(fmt.Sprintf("[%s] %s", step, msg))
	}
}

func (d *deploy) result(outcome, msg string, containerID string) agentprotocol.DeployResult {
	return agentprotocol.DeployResult{
		Outcome: outcome, ImageDigest: d.imageDigest, ContainerID: containerID, Message: d.red.apply(msg), Build: d.buildInfo, Tailnet: d.tailnet,
	}
}

func (d *deploy) fail(msg string) agentprotocol.DeployResult {
	return d.result(agentprotocol.OutcomeFailed, msg, "")
}

// firstID is the container ID reported in the result (replica 0, else the first known).
func (d *deploy) firstID() string {
	for _, id := range d.ids {
		if id != "" {
			return id
		}
	}
	return ""
}

func (d *deploy) run(ctx context.Context) agentprotocol.DeployResult {
	if len(d.p.Processes) > 0 {
		return d.runProcesses(ctx)
	}
	e := d.e

	// Strategy and replica count. Recreate (volumes, no port) is always a
	// single replica: two containers cannot share a volume or an absent port.
	st := &deployState{recreate: d.p.Strategy != agentprotocol.StrategyRolling}
	if !st.recreate && (len(d.p.Volumes) > 0 || d.p.Port <= 0) {
		st.recreate = true
		d.logf("Using recreate strategy: rolling needs an HTTP port and no volumes")
	}
	replicas := max(d.p.Replicas, 1)
	if st.recreate && replicas > 1 {
		d.logf("Recreate runs a single replica; ignoring replicas=%d", replicas)
		replicas = 1
	}
	d.debugf("plan: %s, %d replica(s)", map[bool]string{true: "recreate", false: "rolling"}[st.recreate], replicas)
	d.names = agentprotocol.ContainerNames(d.p.ServiceSlug, d.p.DeploymentID, replicas)
	d.ids = make([]string, replicas)

	env, err := d.buildEnv()
	if err != nil {
		return d.fail(err.Error())
	}

	// 1. Pull or build. Failure leaves everything untouched.
	err = d.fetchImage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return d.interrupted(ctx, st, false)
		}
		return d.fail(err.Error())
	}

	// 1b. Pre-deploy command (migrations) in a container of the new image.
	// Nothing of the previous release has been touched yet, so a failure
	// leaves it serving.
	if msg, ok := d.preDeploy(ctx, env); !ok {
		if ctx.Err() != nil {
			return d.interrupted(ctx, st, false)
		}
		return d.fail(msg)
	}
	if ctx.Err() != nil {
		return d.interrupted(ctx, st, false)
	}

	// 2. Inventory.
	all, err := e.containersOf(ctx, d.p.ServiceID, true)
	if err != nil {
		return d.fail("list containers: " + err.Error())
	}
	existing := make([]*docker.ContainerInfo, replicas)
	for i := range all {
		if idx := indexOfName(d.names, all[i].Name); idx >= 0 {
			existing[idx] = &all[i]
			continue
		}
		st.prev = append(st.prev, all[i])
		if all[i].Running || all[i].Restarting {
			st.serving = append(st.serving, all[i])
		}
	}
	d.debugf("inventory: %d other managed container(s) of this service, %d serving: %s",
		len(st.prev), len(st.serving), containerNames(st.prev))
	// The route table is read under the route lock in routePhase: a snapshot
	// taken here would predate other deployments' routes.

	// 3. Create and start (idempotent per deployment: a running container from
	// an earlier attempt is continued, a dead one replaced).
	resumed := make([]bool, replicas)
	allResumed := true
	for i, ex := range existing {
		if ex == nil {
			allResumed = false
			continue
		}
		if ex.Running && ex.ImageID == d.imageID {
			d.ids[i], resumed[i] = ex.ID, true
			d.created = append(d.created, ex.ID)
			d.logf("Container %s from an earlier attempt is running; continuing", d.names[i])
			continue
		}
		allResumed = false
		d.logf("Removing stale container %s from an earlier attempt", d.names[i])
		d.removeContainer(ctx, ex.ID)
	}

	if st.recreate && !allResumed {
		d.stopOld(ctx, st)
	} else if st.recreate && allResumed && len(st.serving) == 0 && len(st.prev) > 0 {
		// Earlier attempt already stopped the old release; remember the newest
		// as the restore target.
		sort.Slice(st.prev, func(i, j int) bool { return st.prev[i].Created.After(st.prev[j].Created) })
		st.toRestart = []docker.ContainerInfo{st.prev[0]}
	}
	hadPrevious := len(st.serving) > 0 || len(st.toRestart) > 0
	if ctx.Err() != nil {
		return d.interrupted(ctx, st, hadPrevious) // canceled while the old release stopped
	}

	if !allResumed {
		stop := d.phase("create and start")
		failedID, err := d.createAndStartAll(ctx, env, resumed)
		stop()
		if err != nil {
			if ctx.Err() != nil {
				return d.interrupted(ctx, st, hadPrevious)
			}
			return d.abort(ctx, st, hadPrevious, failedID, err.Error())
		}
	}
	if ctx.Err() != nil {
		return d.interrupted(ctx, st, hadPrevious)
	}

	// 4. Health check every replica (from outside the container).
	stopHealth := d.phase("health")
	failedID, reason, healthy := d.healthPhase(ctx, d.ids)
	stopHealth()
	if !healthy {
		if ctx.Err() != nil {
			return d.interrupted(ctx, st, hadPrevious)
		}
		return d.abort(ctx, st, hadPrevious, failedID, reason)
	}

	// 5. Route and verify. Past this point the new release is live: the point
	// of no return for a cancel.
	stopRoute := d.phase("route and verify")
	routeReason, routed := d.routePhase(ctx, st)
	stopRoute()
	if !routed {
		if ctx.Err() != nil {
			return d.interrupted(ctx, st, hadPrevious)
		}
		return d.abort(ctx, st, hadPrevious, "", routeReason)
	}

	// 6. Retire the previous release; the new one is live.
	stopRetire := d.phase("retire")
	d.retire(ctx, st)
	stopRetire()
	d.recordCurrent()
	d.pruneImages(ctx)
	d.tailnetStep(ctx)
	// 7. Post-deploy command. The release is live: its failure only warns.
	d.postDeploy(ctx, env)

	msg := "Deployed " + d.names[0]
	if len(d.names) > 1 {
		msg = fmt.Sprintf("Deployed %d replicas (%s … %s)", len(d.names), d.names[0], d.names[len(d.names)-1])
	}
	if d.warning != "" {
		msg += ". Warning: " + d.warning
	}
	d.log.Info("deploy succeeded", "container", d.names[0], "replicas", len(d.names))
	return d.result(agentprotocol.OutcomeSucceeded, msg, d.firstID())
}

// replicaIdx is the replica index of slot i of names/ids.
func (d *deploy) replicaIdx(i int) int {
	if d.index == nil {
		return i
	}
	return d.index[i]
}

func indexOfName(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

// parallel runs fn(0..n-1) concurrently and waits for all of them.
func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(i)
		}()
	}
	wg.Wait()
}

// buildEnv resolves the variables listed in the payload from the command's
// secrets. A missing value aborts: deploying with silently absent config is
// worse than failing.
func (d *deploy) buildEnv() ([]string, error) {
	keys := append([]string(nil), d.p.EnvKeys...)
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		v, ok := d.cmd.Secrets[agentprotocol.SecretEnvPrefix+k]
		if !ok {
			return nil, fmt.Errorf("variable %s was not delivered with the command", k)
		}
		env = append(env, k+"="+v)
	}
	return env, nil
}

// ---- image ----

// fetchImage pulls or builds the deploy's image: once per deploy, whatever the
// number of processes.
func (d *deploy) fetchImage(ctx context.Context) error {
	if d.p.Image != nil {
		defer d.phase("pull")()
		return d.pullImage(ctx)
	}
	defer d.phase("build")()
	return d.buildImage(ctx)
}

func (d *deploy) pullImage(ctx context.Context) error {
	src := d.p.Image
	eng := d.e.cfg.Docker
	ref := src.Ref
	if src.Digest != "" {
		ref = src.Digest
	}
	if ref == "" {
		return errors.New("image reference is empty")
	}
	d.event(agentprotocol.StepPull, agentprotocol.StepStarted, "Pulling "+ref)
	d.debugf("image: ref %s, pinned digest %q, registry auth %t", src.Ref, src.Digest, src.HasRegistryAuth)

	var info docker.ImageInfo
	haveLocal := false
	if src.Digest != "" { // rollback: reuse the pinned image when it is still here
		if i, err := eng.ImageInspect(ctx, ref); err == nil {
			info, haveLocal = i, true
			d.logf("Image %s already present locally", ref)
			d.debugf("docker image inspect %s: found locally, id %s", ref, shortID(i.ID))
		} else {
			d.debugf("docker image inspect %s: %v; pulling instead", ref, err)
		}
	}
	if !haveLocal {
		auth := ""
		if src.HasRegistryAuth {
			auth = d.cmd.Secrets[agentprotocol.SecretRegistryAuth]
		}
		began := time.Now()
		d.debugf("docker image pull %s", ref)
		if err := eng.ImagePull(ctx, ref, auth, d.logf0); err != nil {
			d.debugErrf(err, "docker image pull %s: failed after %s: %v", ref, time.Since(began).Round(time.Millisecond), err)
			if ctx.Err() != nil {
				return err // run() reports the cancellation or shutdown
			}
			msg := "pull failed: " + err.Error()
			d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
			return errors.New(msg)
		}
		i, err := eng.ImageInspect(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			msg := "inspect pulled image: " + err.Error()
			d.event(agentprotocol.StepPull, agentprotocol.StepFailed, msg)
			return errors.New(msg)
		}
		info = i
		d.debugf("docker image pull %s: done in %s, inspected id %s", ref, time.Since(began).Round(time.Millisecond), shortID(i.ID))
	}
	d.imageID = info.ID
	if src.Digest != "" {
		d.imageDigest = src.Digest
	} else {
		d.imageDigest = pickDigest(src.Ref, info)
	}
	d.debugf("image resolved: id %s, digest %s, %d repo digest(s)", shortID(d.imageID), d.imageDigest, len(info.RepoDigests))
	d.event(agentprotocol.StepPull, agentprotocol.StepSucceeded, "Image "+d.imageDigest)
	return nil
}

func (d *deploy) logf0(line string) { d.dlog.Add(line) }

func (d *deploy) buildImage(ctx context.Context) error {
	g := d.p.Git
	eng := d.e.cfg.Docker
	defer d.e.builds.begin()() // Docker cleanup leaves images and build cache alone meanwhile
	d.event(agentprotocol.StepBuild, agentprotocol.StepStarted, "Building "+g.URL+" @ "+shortSHA(g.CommitSHA))
	d.debugf("build: %s @ %s, dockerfile %q, context %q, type %q, existing image %q",
		g.URL, shortSHA(g.CommitSHA), g.DockerfilePath, g.BuildContext, g.BuildType, g.ExistingImageID)

	fail := func(msg string) error {
		d.event(agentprotocol.StepBuild, agentprotocol.StepFailed, msg)
		return errors.New(msg)
	}
	if g.ExistingImageID != "" {
		info, err := eng.ImageInspect(ctx, g.ExistingImageID)
		if err != nil {
			if errors.Is(err, docker.ErrNotFound) {
				return fail("image " + g.ExistingImageID + " is no longer on this host (pruned); rebuild the commit")
			}
			return fail("inspect image: " + err.Error())
		}
		d.imageID, d.imageDigest = info.ID, info.ID
		d.debugf("build skipped: reusing image %s", shortID(info.ID))
		d.event(agentprotocol.StepBuild, agentprotocol.StepSucceeded, "Reusing previously built image "+shortID(info.ID))
		return nil
	}
	if d.e.cfg.Builder == nil {
		return fail("this agent cannot build images (builder not configured)")
	}
	req := build.Request{
		DeploymentID:   d.p.DeploymentID,
		ServiceID:      d.p.ServiceID,
		ServiceSlug:    d.p.ServiceSlug,
		GitURL:         g.URL,
		CommitSHA:      g.CommitSHA,
		DockerfilePath: g.DockerfilePath,
		BuildContext:   g.BuildContext,
		BuildType:      g.BuildType,
		NoCache:        g.NoCache,
		Submodules:     g.Submodules,
		CacheKey:       d.p.ProjectID,
		Vars:           d.buildVars(),
	}
	var buildInfo agentprotocol.BuildInfo
	req.Info = &buildInfo
	if g.HasToken {
		req.Token = d.cmd.Secrets[agentprotocol.SecretGitToken]
		req.TokenUser = d.cmd.Secrets[agentprotocol.SecretGitUsername]
	}
	if g.HasSSHKey {
		req.SSHKey = d.cmd.Secrets[agentprotocol.SecretGitSSHKey]
		req.KnownHosts = g.KnownHosts
	}
	if err := req.Validate(); err != nil {
		return fail("invalid git source: " + err.Error())
	}
	bl := logs.NewBatcher(agentprotocol.LogFrame{
		DeploymentID: d.p.DeploymentID, ServiceID: d.p.ServiceID, Stream: agentprotocol.StreamBuild,
	}, d.e.cfg.Sender.Log, d.red.apply)
	id, err := d.e.cfg.Builder.Build(ctx, req, bl.Add)
	bl.Close()
	if err != nil {
		if ctx.Err() != nil {
			return err // canceled or shut down: the build killed itself and removed its work directory
		}
		return fail(d.red.apply(err.Error()))
	}
	d.imageID, d.imageDigest = id, id
	d.debugf("build finished: image %s", shortID(id))
	if buildInfo.Method != "" {
		d.buildInfo = &buildInfo
		d.event(agentprotocol.StepBuild, agentprotocol.StepSucceeded, "Built "+shortID(id)+" with "+buildMethodLabel(buildInfo))
		return nil
	}
	d.event(agentprotocol.StepBuild, agentprotocol.StepSucceeded, "Built "+shortID(id))
	return nil
}

// ---- containers ----

func (d *deploy) stopOld(ctx context.Context, st *deployState) {
	grace := graceOf(d.p.StopGraceSeconds)
	d.ctl.setStep(stepStop)
	for _, c := range st.serving {
		d.logf("Stopping previous container %s (grace %s)", c.Name, grace)
		// A stop is not interrupted by a cancel: half a stop leaves the old
		// release in a state that cannot be undone reliably. The cancel is
		// honored right after, by starting the old release again.
		sctx, cancel := detached(ctx, grace+30*time.Second)
		began := time.Now()
		err := d.e.cfg.Docker.ContainerStop(sctx, c.ID, &grace)
		cancel()
		d.debugErrf(err, "docker container stop %s (%s), grace %s: %s in %s", c.Name, shortID(c.ID), grace, errLabel(err), time.Since(began).Round(time.Millisecond))
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			d.logf("Warning: stop %s: %v", c.Name, err)
		}
		st.toRestart = append(st.toRestart, c)
	}
}

// plan is the network membership of the deploy's containers.
func (d *deploy) plan() netPlan { return planOfDeploy(d.p) }

// ensureNetworks makes sure the environment (or internal) and shared networks
// the replicas join exist. The edge network is not one of them: only the proxy
// sits on it.
func (d *deploy) ensureNetworks(ctx context.Context) error {
	plan := d.plan()
	d.debugf("networks: %s", attachmentNames(plan.Attachments()))
	if ign := plan.ignoredShared(); len(ign) > 0 {
		d.logf("Warning: %d shared network(s) not joined: a service that blocks outbound internet stays on the internal network only", len(ign))
	}
	if err := plan.ensure(ctx, d.e.ensureNetwork); err != nil {
		return d.stepErr(agentprotocol.StepCreate, "ensure networks", err)
	}
	return nil
}

// createAndStartAll creates and then starts every replica that was not
// resumed. It records each created container in d.ids/d.created as soon as it
// exists so a failure can remove it; on failure it returns the ID of the
// container that failed ("" when none exists).
func (d *deploy) createAndStartAll(ctx context.Context, env []string, resumed []bool) (string, error) {
	eng := d.e.cfg.Docker
	p := d.p
	d.ctl.setStep(agentprotocol.StepCreate)
	if err := d.ensureNetworks(ctx); err != nil {
		return "", err
	}
	mounts := make([]docker.VolumeMount, 0, len(p.Volumes))
	for _, v := range p.Volumes {
		if err := eng.EnsureVolume(ctx, v.Name, map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelService: p.ServiceID,
		}); err != nil {
			return "", d.stepErr(agentprotocol.StepCreate, "ensure volume "+v.Name, err)
		}
		mounts = append(mounts, docker.VolumeMount{Volume: v.Name, Target: v.MountPath})
		d.debugf("docker volume %s ensured, mounted at %s", v.Name, v.MountPath)
	}

	var todo []int
	for i := range d.names {
		if !resumed[i] {
			todo = append(todo, i)
		}
	}
	if len(d.names) == 1 {
		d.event(agentprotocol.StepCreate, agentprotocol.StepStarted, "Creating container "+d.names[0])
	} else {
		d.event(agentprotocol.StepCreate, agentprotocol.StepStarted, fmt.Sprintf("Creating %d replica containers", len(todo)))
	}
	for _, i := range todo {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		spec := d.spec(i, env, mounts)
		for _, l := range specSummary(spec) {
			d.debugf("%s", l)
		}
		began := time.Now()
		id, err := eng.ContainerCreate(ctx, spec)
		d.debugErrf(err, "docker container create %s: %s in %s", d.names[i], createLabel(id, err), time.Since(began).Round(time.Millisecond))
		if err != nil {
			return "", d.stepErr(agentprotocol.StepCreate, "create container "+d.names[i], err)
		}
		d.ids[i] = id
		d.created = append(d.created, id)
		if err := d.writeFiles(ctx, id); err != nil {
			return id, d.stepErr(agentprotocol.StepCreate, "write files into "+d.names[i], err)
		}
	}
	d.event(agentprotocol.StepCreate, agentprotocol.StepSucceeded, "")

	d.event(agentprotocol.StepStart, agentprotocol.StepStarted, "")
	for _, i := range todo {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		began := time.Now()
		err := eng.ContainerStart(ctx, d.ids[i])
		d.debugErrf(err, "docker container start %s (%s): %s in %s", d.names[i], shortID(d.ids[i]), errLabel(err), time.Since(began).Round(time.Millisecond))
		if err != nil {
			return d.ids[i], d.stepErr(agentprotocol.StepStart, "start container "+d.names[i], err)
		}
	}
	d.event(agentprotocol.StepStart, agentprotocol.StepSucceeded, "")
	return "", nil
}

// spec is the container spec of replica i.
func (d *deploy) spec(i int, env []string, mounts []docker.VolumeMount) docker.ContainerSpec {
	p := d.p
	grace := int(graceOf(p.StopGraceSeconds) / time.Second)
	spec := docker.ContainerSpec{
		Name:  d.names[i],
		Image: d.imageID,
		Env:   processEnv(replicaEnv(env, d.replicaIdx(i), d.replicaCount()), d.processName()),
		Cmd:   d.startCommand(),
		Labels: map[string]string{
			agentprotocol.LabelManaged:     "true",
			agentprotocol.LabelService:     p.ServiceID,
			agentprotocol.LabelDeployment:  p.DeploymentID,
			agentprotocol.LabelRelease:     p.ReleaseID,
			agentprotocol.LabelEnvironment: p.EnvironmentID,
			agentprotocol.LabelReplica:     strconv.Itoa(d.replicaIdx(i)),
		},
		Networks:      d.plan().Attachments(),
		Mounts:        mounts,
		RestartPolicy: "unless-stopped",
		StopTimeout:   &grace,
	}
	if pr := d.proc(); pr != nil {
		spec.Labels[agentprotocol.LabelProcess] = pr.Name
	}
	d.applyTemplateFeatures(&spec)
	applyDockerOptions(&spec, p.Docker, p.ServiceSlug, d.replicaIdx(i), d.replicaCount())
	if lbl := specLabelOf(p); lbl != "" {
		spec.Labels[agentprotocol.LabelSpec] = lbl
	}
	if p.MemoryMB > 0 {
		spec.MemoryBytes = int64(p.MemoryMB) * 1024 * 1024
	}
	if p.CPUs > 0 {
		spec.NanoCPUs = int64(p.CPUs * 1e9)
	}
	return spec
}

func (d *deploy) stepErr(step, what string, err error) error {
	msg := what + ": " + err.Error()
	d.event(step, agentprotocol.StepFailed, msg)
	return errors.New(msg)
}

func (d *deploy) removeContainer(ctx context.Context, id string) {
	cctx, cancel := detached(ctx, 60*time.Second)
	defer cancel()
	if err := d.e.cfg.Docker.ContainerRemove(cctx, id, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		d.logf("Warning: remove container %s: %v", shortID(id), err)
	}
}

// ---- health ----

// healthPhase checks every replica concurrently. The first failure cancels the
// others and is reported with the failing replica's container ID (for its log
// tail). ids is index-aligned with d.names.
func (d *deploy) healthPhase(ctx context.Context, ids []string) (failedID, reason string, ok bool) {
	eng := d.e.cfg.Docker
	n := len(d.names)
	if n == 1 {
		d.event(agentprotocol.StepHealth, agentprotocol.StepStarted, "Checking health ("+healthLabel(d.p.Healthcheck)+")")
	} else {
		d.event(agentprotocol.StepHealth, agentprotocol.StepStarted,
			fmt.Sprintf("Checking health of %d replicas (%s)", n, healthLabel(d.p.Healthcheck)))
	}

	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu      sync.Mutex
		results = make([]health.Result, n)
		failIdx = -1
	)
	// A containerised agent joins the replicas' network for the probes; the
	// bare-host agent reaches the bridge IPs directly.
	probeNet := d.plan().ProbeNetwork()
	release := d.e.probeAccess(ctx, probeNet)
	defer release()
	parallel(n, func(i int) {
		id := ids[i]
		var addrs []string
		if info, err := eng.ContainerInspect(hctx, id); err == nil {
			addrs = probeIPs(info, probeNet)
		}
		addrs = append(addrs, d.names[i]) // DNS fallback when the IP is not routable from the agent
		d.debugf("health %s: targets %s", d.names[i], strings.Join(addrs, ", "))
		res := d.e.cfg.Health(hctx, health.Target{
			Spec:        d.p.Healthcheck,
			DefaultPort: d.p.Port,
			Addrs:       addrs,
			Inspect:     func(c context.Context) (docker.ContainerInfo, error) { return eng.ContainerInspect(c, id) },
			OneShot:     d.p.OneShot,
			Trace:       func(line string) { d.debugf("%s", leveled(line, "health "+d.names[i])) },
		})
		d.debugErrf(unhealthy(res), "health %s: %s", d.names[i], healthResultLabel(res))
		mu.Lock()
		results[i] = res
		if !res.Healthy && failIdx < 0 {
			failIdx = i
			cancel()
		}
		mu.Unlock()
	})

	if failIdx >= 0 && ctx.Err() != nil {
		return "", "", false // canceled or shut down mid-check: not a health failure
	}
	if failIdx >= 0 {
		msg := "Health check failed: " + results[failIdx].Reason
		if n > 1 || d.index != nil {
			total := n
			if d.index != nil { // scale-up: n counts only the new replicas
				total = d.p.Replicas
			}
			msg = fmt.Sprintf("Replica %d of %d: %s", d.replicaIdx(failIdx), total, msg)
		}
		d.event(agentprotocol.StepHealth, agentprotocol.StepFailed, msg)
		return ids[failIdx], msg, false
	}
	for _, r := range results {
		if r.Warning != "" {
			d.warning = r.Warning
			break
		}
	}
	msg := "Healthy"
	if n > 1 {
		msg = fmt.Sprintf("All %d replicas healthy", n)
	}
	if d.warning != "" {
		msg += " (" + d.warning + ")"
	}
	d.event(agentprotocol.StepHealth, agentprotocol.StepSucceeded, msg)
	return "", "", true
}

func healthLabel(h agentprotocol.HealthcheckSpec) string {
	if h.Type == "" {
		return health.TypeNone
	}
	return h.Type
}

// ---- routing and verification ----

// routePhase applies the new route table and verifies it through the probe
// server. On verification failure it restores the previous table itself (still
// holding the route lock); a failed restore is recorded in st.routeErr.
func (d *deploy) routePhase(ctx context.Context, st *deployState) (string, bool) {
	e := d.e
	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	if ctx.Err() != nil {
		return "", false
	}

	// Every table is built from the proxy's live state under the route lock,
	// so a deploy only replaces its own service's routes and never drops a
	// route another deployment applied since this one was dispatched.
	st.prevRoutes = e.cfg.Proxy.Current()
	d.debugf("routes before: %s (proxy generation %s)", routeSummary(st.prevRoutes), proxyGeneration(e.cfg.Proxy))
	if d.p.Proxy != nil {
		if err := e.installProxyConfig(d.p.Proxy, d.cmd.Secrets); err != nil {
			d.event(agentprotocol.StepRoute, agentprotocol.StepFailed, "Proxy configuration rejected: "+err.Error())
			return "Proxy configuration rejected: " + err.Error(), false
		}
	}
	table, err := resolveRoutes(d.routeTable(st.prevRoutes), d.cmd.Secrets)
	if err != nil {
		d.event(agentprotocol.StepRoute, agentprotocol.StepFailed, "Route table rejected: "+err.Error())
		return "Route table rejected: " + err.Error(), false
	}
	if !d.live && len(st.prevRoutes) > 0 {
		// The payload's own route carries the maintenance state it was
		// dispatched with; the proxy's live table is the authority. A proxy
		// with no state yet was seeded from the payload, which stays as sent.
		table = inheritMaintenance(table, st.prevRoutes)
	}
	d.debugf("routes after: %s; own %s", routeSummary(table), routeSummary(d.verifiableRoutes(table)))
	if d.p.Proxy != nil || !routesEqual(table, st.prevRoutes) {
		if ctx.Err() != nil {
			return "", false // canceled before the route changed
		}
		d.event(agentprotocol.StepRoute, agentprotocol.StepStarted, "Updating proxy routes")
		if d.p.ACMEEmail != "" {
			e.cfg.Proxy.SetACMEEmail(d.p.ACMEEmail)
		}
		e.applyTrafficSetting(d.p.TrafficAnalytics)
		began := time.Now()
		err := e.cfg.Proxy.Apply(table)
		d.debugErrf(err, "proxy apply: %s in %s, generation now %s", errLabel(err), time.Since(began).Round(time.Millisecond), proxyGeneration(e.cfg.Proxy))
		if err != nil {
			d.event(agentprotocol.StepRoute, agentprotocol.StepFailed, "Proxy rejected the route table: "+err.Error())
			return "Proxy rejected the new route table: " + err.Error(), false
		}
		st.applied = true
		d.event(agentprotocol.StepRoute, agentprotocol.StepSucceeded, "")
	}
	if canceled(ctx) {
		d.restoreRoutes(st) // the cancel arrived while the table was applied
		return "", false
	}

	own := d.verifiableRoutes(table)
	if len(own) == 0 {
		return d.settle(st)
	}
	// A stack has one route per (component, port): verify through the first
	// one that serves something (not a redirect-only route).
	var (
		host, prefix string
		target       agentprotocol.Route
		ok           bool
	)
	for _, r := range own {
		if host, prefix, ok = verifyTarget(r); ok {
			target = r
			break
		}
	}
	if !ok {
		return d.settle(st) // redirect-only: nothing is served to probe
	}
	path := prefix + d.healthPath()
	switch res, last := d.verifyThroughProxy(ctx, host, path, len(target.Upstreams)); res {
	case verifyOK:
		return d.settle(st)
	case verifyCanceled:
		d.restoreRoutes(st) // canceled during verification: back to the previous routes first
		return "", false
	case verifyStopped:
		return "agent stopped during deployment", false
	default:
		// Restore the previous routes before anything else changes.
		st.verifyFailed = true
		d.restoreRoutes(st)
		return "Verification through the proxy failed: " + last, false
	}
}

// verifyResult is how a verification through the proxy ended.
type verifyResult int

const (
	verifyOK       verifyResult = iota // the route answered
	verifyCanceled                     // a person canceled the deploy meanwhile
	verifyStopped                      // the agent is shutting down
	verifyFailed                       // every try failed
)

// verifyThroughProxy probes host and path on the proxy's probe entrypoint, up
// to VerifyTries times. Traefik round-robins across the upstreams, so one
// probe per upstream reaches each replica once. For verifyFailed it returns
// the last failure.
func (d *deploy) verifyThroughProxy(ctx context.Context, host, path string, probes int) (verifyResult, string) {
	e := d.e
	d.event(agentprotocol.StepVerify, agentprotocol.StepStarted, "Verifying http://"+host+path+" through the proxy")
	var last string
	for try := 1; try <= e.cfg.VerifyTries; try++ {
		last = ""
		for k := range probes {
			began := time.Now()
			code, err := e.cfg.Proxy.Probe(ctx, host, path)
			d.debugErrf(probeFailure(code, err), "verify %d/%d probe %d/%d: GET http://%s%s on the probe entrypoint: %s in %s",
				try, e.cfg.VerifyTries, k+1, probes, host, path, probeLabel(code, err), time.Since(began).Round(time.Millisecond))
			if err != nil {
				last = err.Error()
				break
			}
			if code >= 500 {
				last = fmt.Sprintf("HTTP %d", code)
				break
			}
		}
		if last == "" {
			d.event(agentprotocol.StepVerify, agentprotocol.StepSucceeded, "Reachable through the proxy")
			return verifyOK, ""
		}
		if canceled(ctx) {
			return verifyCanceled, ""
		}
		d.logf("Verify attempt %d/%d failed: %s", try, e.cfg.VerifyTries, last)
		if try < e.cfg.VerifyTries {
			select {
			case <-ctx.Done():
				if canceled(ctx) {
					return verifyCanceled, ""
				}
				return verifyStopped, ""
			case <-time.After(e.cfg.VerifyInterval):
			}
		}
	}
	d.event(agentprotocol.StepVerify, agentprotocol.StepFailed, "Verification through the proxy failed: "+last)
	return verifyFailed, last
}

// restoreRoutes puts the previous route table back (the route lock is held).
// A failure is recorded in st.routeErr, which ends the deploy as rollback_failed.
func (d *deploy) restoreRoutes(st *deployState) {
	if !st.applied {
		return
	}
	err := d.e.cfg.Proxy.Apply(st.prevRoutes)
	d.debugErrf(err, "proxy restore of previous routes (%s): %s, generation now %s", routeSummary(st.prevRoutes), errLabel(err), proxyGeneration(d.e.cfg.Proxy))
	if err != nil {
		st.routeErr = err
		d.logf("Restoring previous routes failed: %v", err)
		return
	}
	st.routesRestored = true
	d.logf("Previous routes restored")
}

// settle ends the route phase successfully: the new release is live, the point
// of no return for a cancel. A cancel that got in first wins: the new routes
// are withdrawn and the deploy unwinds.
func (d *deploy) settle(st *deployState) (string, bool) {
	if d.ctl.commit() {
		return "", true
	}
	d.restoreRoutes(st)
	return "", false
}

func (d *deploy) healthPath() string {
	path := d.p.Healthcheck.Path
	switch {
	case path == "":
		return "/"
	case !strings.HasPrefix(path, "/"):
		return "/" + path
	}
	return path
}

// ownRoutes returns the http routes of one service (tcp/udp routes have no
// hosts and are never verified through the probe entrypoint).
func ownRoutes(table []agentprotocol.Route, serviceID string) []agentprotocol.Route {
	var out []agentprotocol.Route
	for _, r := range table {
		if (r.ServiceID == serviceID || (r.Stack != "" && r.Stack == serviceID)) && !r.IsL4() && len(r.Hosts) > 0 && len(r.Upstreams) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// verifyTarget picks the host and path prefix a verification probe uses for an
// http route: the first serving domain rule, else the first host that has no
// rule. A route whose domains are all redirects serves nothing to probe.
func verifyTarget(r agentprotocol.Route) (host, prefix string, ok bool) {
	ruled := map[string]bool{}
	for _, dr := range r.Domains {
		ruled[dr.Host] = true
		if dr.Kind != agentprotocol.DomainRedirect {
			return dr.Host, dr.PathPrefix, true
		}
	}
	for _, h := range r.Hosts {
		if !ruled[h] {
			return h, "", true
		}
	}
	return "", "", false
}

// routeTable merges the other services' routes with this service's new route.
// The others always come from the live table (the proxy's current state under
// the route lock); the payload's Routes only seed a proxy that has no state yet
// (a fresh agent). A scale only rewrites this service's upstream sets.
func (d *deploy) routeTable(live []agentprotocol.Route) []agentprotocol.Route {
	if d.p.Stack != nil {
		return d.stackRouteTable(live)
	}
	if !d.live {
		base := live
		if len(base) == 0 {
			base = d.p.Routes
		}
		table := make([]agentprotocol.Route, 0, len(base)+1)
		for _, r := range base {
			if r.ServiceID != d.p.ServiceID && r.Stack != d.p.ServiceID { // the service's process routes go too
				table = append(table, r)
			}
		}
		if len(d.p.Hosts) > 0 && d.p.Port > 0 {
			table = append(table, d.route())
		}
		return table
	}

	table := make([]agentprotocol.Route, 0, len(live)+1)
	hadOwn := false
	for _, r := range live {
		if !d.ownsRoute(r) {
			table = append(table, r)
			continue
		}
		hadOwn = true
		port := d.p.Port
		if len(r.Upstreams) > 0 {
			if _, ps, err := net.SplitHostPort(r.Upstreams[0]); err == nil {
				if n, err := strconv.Atoi(ps); err == nil {
					port = n
				}
			}
		}
		r.Upstreams = d.upstreams(port)
		r.Networks = d.plan().RouteNetworks()
		r.Hosts = append([]string(nil), r.Hosts...)
		table = append(table, r)
	}
	if !hadOwn && len(d.p.Hosts) > 0 && d.p.Port > 0 {
		table = append(table, d.route())
	}
	return table
}

// upstreams lists "<container>:<port>" for every replica in d.names.
func (d *deploy) upstreams(port int) []string {
	ups := make([]string, len(d.names))
	for i, n := range d.names {
		ups[i] = net.JoinHostPort(n, strconv.Itoa(port))
	}
	return ups
}

func (d *deploy) route() agentprotocol.Route {
	r := agentprotocol.Route{
		ServiceID:  d.routeID(),
		Hosts:      append([]string(nil), d.p.Hosts...),
		Upstreams:  d.upstreams(d.p.Port),
		HealthPath: d.traefikHealthPath(),
		Networks:   d.plan().RouteNetworks(),
		RouteExt:   agentprotocol.RouteExt{Domains: append([]agentprotocol.DomainRule(nil), d.p.DomainRules...), OriginLock: d.p.OriginLock},
	}
	if pr := d.proc(); pr != nil && !pr.Default {
		r.Stack = d.p.ServiceID // the route of a named process belongs to the service like a stack's does
	}
	return r
}

// traefikHealthPath is the path Traefik should probe on every upstream: only
// for http checks that target the service port itself (Traefik cannot probe
// another port with the server URL's host).
func (d *deploy) traefikHealthPath() string {
	h := d.p.Healthcheck
	if h.Type != health.TypeHTTP || d.p.Port <= 0 || (h.Port != 0 && h.Port != d.p.Port) {
		return ""
	}
	return d.healthPath()
}

func routesEqual(a, b []agentprotocol.Route) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// ---- failure handling ----

// abort undoes a failed deployment: removes every container this attempt
// created (with the last log lines of the failing one as evidence), restarts a
// stopped previous release (recreate) and derives the outcome. Route
// restoration already happened in routePhase.
func (d *deploy) abort(ctx context.Context, st *deployState, hadPrevious bool, failedID, reason string) agentprotocol.DeployResult {
	e := d.e
	cctx, cancel := detached(ctx, 2*time.Minute)
	defer cancel()

	tailID := failedID
	if tailID == "" && len(d.created) > 0 {
		tailID = d.created[0]
	}
	var tail []string
	if tailID != "" {
		tail, _ = logs.Tail(cctx, e.cfg.Docker, tailID, failureLogLines)
	}

	restoreFailed := st.routeErr != nil
	d.debugf("ERROR abort: %q; restore failed %t, %d created container(s), %d to restart", reason, st.routeErr != nil, len(d.created), len(st.toRestart))
	var problems []string
	if st.routeErr != nil {
		problems = append(problems, "could not restore previous routes: "+st.routeErr.Error())
	}

	if st.routeErr == nil {
		// Routes are back on the previous release (or never moved): the new
		// containers can go.
		d.removeCreated(cctx)
		if st.recreate {
			for _, c := range st.toRestart {
				d.logf("Restarting previous container %s", c.Name)
				if err := e.cfg.Docker.ContainerStart(cctx, c.ID); err != nil {
					restoreFailed = true
					problems = append(problems, fmt.Sprintf("could not restart %s: %v", c.Name, err))
				}
			}
		}
	}

	outcome := agentprotocol.OutcomeFailed
	var msg strings.Builder
	msg.WriteString(reason)
	switch {
	case restoreFailed:
		outcome = agentprotocol.OutcomeRollbackFailed
		msg.WriteString("\nAutomatic rollback failed: " + strings.Join(problems, "; ") +
			". Manual recovery is required: check `docker ps --filter label=nebulactrl.service=" + d.p.ServiceID + "` and redeploy.")
	case hadPrevious && (st.recreate || st.verifyFailed):
		outcome = agentprotocol.OutcomeRolledBack
		msg.WriteString("\nRolled back to the previous release.")
	case hadPrevious:
		msg.WriteString("\nPrevious release is still serving.")
	}
	if len(tail) > 0 {
		d.logf("--- last %d container log lines ---", len(tail))
		for _, l := range tail {
			d.logf("%s", l)
		}
		msg.WriteString(fmt.Sprintf("\n\nLast %d container log lines:\n%s", len(tail), strings.Join(tail, "\n")))
	}
	d.log.Warn("deploy failed", "outcome", outcome, "reason", d.red.apply(reason))
	return d.result(outcome, msg.String(), "")
}

// removeCreated force-removes every container this attempt created or resumed.
func (d *deploy) removeCreated(ctx context.Context) {
	eng := d.e.cfg.Docker
	parallel(len(d.created), func(i int) {
		err := eng.ContainerRemove(ctx, d.created[i], true, false)
		d.debugErrf(err, "docker container remove %s (force): %s", shortID(d.created[i]), errLabel(err))
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			d.logf("Warning: remove failed container: %v", err)
		}
	})
	d.created = nil
}

// ---- retire, bookkeeping ----

func (d *deploy) retire(ctx context.Context, st *deployState) {
	if len(st.prev) == 0 {
		return
	}
	e := d.e
	d.event(agentprotocol.StepRetire, agentprotocol.StepStarted, "Retiring previous release")
	grace := graceOf(d.p.StopGraceSeconds)
	if !st.recreate && len(st.serving) > 0 {
		select { // drain in-flight requests that were routed to the old containers
		case <-time.After(e.cfg.DrainDelay):
		case <-ctx.Done():
		}
		d.debugf("retire: waited up to %s for in-flight requests to drain", e.cfg.DrainDelay)
	}
	cctx, cancel := detached(ctx, grace+2*time.Minute)
	defer cancel()
	parallel(len(st.prev), func(i int) {
		c := st.prev[i]
		if c.Running || c.Restarting {
			began := time.Now()
			err := e.cfg.Docker.ContainerStop(cctx, c.ID, &grace)
			d.debugErrf(err, "docker container stop %s (%s), grace %s: %s in %s", c.Name, shortID(c.ID), grace, errLabel(err), time.Since(began).Round(time.Millisecond))
			if err != nil && !errors.Is(err, docker.ErrNotFound) {
				d.logf("Warning: stop %s: %v", c.Name, err)
			}
		}
		began := time.Now()
		err := e.cfg.Docker.ContainerRemove(cctx, c.ID, true, false)
		d.debugErrf(err, "docker container remove %s (%s): %s in %s", c.Name, shortID(c.ID), errLabel(err), time.Since(began).Round(time.Millisecond))
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			d.logf("Warning: remove %s: %v", c.Name, err)
		}
	})
	d.event(agentprotocol.StepRetire, agentprotocol.StepSucceeded, "")
}

// recordCurrent remembers the replica set that is now live for the service.
func (d *deploy) recordCurrent() {
	ent := desiredEntry{
		DeploymentID:   d.p.DeploymentID,
		Replicas:       len(d.names),
		ContainerNames: append([]string(nil), d.names...),
		ScaledAt:       d.scaledAt,
	}
	routed := d.live || (len(d.p.Hosts) > 0 && d.p.Port > 0)
	if len(d.p.Processes) > 1 {
		ent = d.processEntry()
		routed = d.live || d.anyRouted()
	}
	// The applied table carries the resolved basic-auth users, which a later
	// reconcile that prefers this local entry cannot re-derive.
	if routed {
		ent.Routes = ownRoutes(d.e.cfg.Proxy.Current(), d.p.ServiceID)
	}
	d.e.mu.Lock()
	if pr := d.proc(); pr != nil && d.live {
		ent = d.e.withScaledProcess(d.p.ServiceID, ent, desiredProcess{
			Name: pr.Name, Default: pr.Default, Replicas: len(d.names),
			ContainerNames: append([]string(nil), d.names...), ScaledAt: d.scaledAt,
		})
	}
	d.e.local[d.p.ServiceID] = ent
	d.e.desired[d.p.ServiceID] = ent
	d.e.mu.Unlock()
	if !d.live { // a live scale rebuilds the payload from labels and knows no replication role
		var rep *agentprotocol.ReplicationDesired
		if d.p.Database != nil {
			rep = d.p.Database.Replication
		}
		d.e.repl.setDesired(d.p.ServiceID, rep)
	}
}

// pruneImages keeps the newest imageRetention nc-build/<slug> images.
func (d *deploy) pruneImages(ctx context.Context) {
	keep := d.p.ImageRetention
	if keep <= 0 {
		keep = 5
	}
	cctx, cancel := detached(ctx, 2*time.Minute)
	defer cancel()
	eng := d.e.cfg.Docker
	repo := build.ImageRepo(d.p.ServiceSlug)
	imgs, err := eng.ImageList(cctx, repo)
	if err != nil {
		d.log.Debug("list build images", "err", err)
		return
	}
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Created.After(imgs[j].Created) })
	d.debugf("image retention: %d image(s) of %s, keeping the newest %d", len(imgs), repo, keep)
	kept := 0
	for _, img := range imgs {
		var tags []string
		for _, t := range img.RepoTags {
			if strings.HasPrefix(t, repo+":") {
				tags = append(tags, t)
			}
		}
		if len(tags) == 0 {
			continue
		}
		if img.ID == d.imageID || kept < keep {
			kept++
			continue
		}
		for _, t := range tags {
			// Not forced: an image still used by a container is left alone.
			if err := eng.ImageRemove(cctx, t, false); err != nil {
				d.log.Debug("remove old build image", "tag", t, "err", err)
			}
		}
		d.logf("Removed old image %s", strings.Join(tags, ", "))
	}
}

// ---- helpers ----

func pickDigest(ref string, info docker.ImageInfo) string {
	repo := normRepo(ref)
	for _, rd := range info.RepoDigests {
		if i := strings.IndexByte(rd, '@'); i > 0 && normRepo(rd[:i]) == repo {
			return rd
		}
	}
	if len(info.RepoDigests) > 0 {
		return info.RepoDigests[0]
	}
	return info.ID
}

// normRepo strips digest, tag and the implicit docker.io/library prefixes.
func normRepo(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		ref = ref[:i]
	}
	for _, p := range []string{"index.docker.io/", "docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, p)
	}
	return strings.TrimPrefix(ref, "library/")
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
