package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A service can run several processes from one build, one image and one
// release. The deploy command carries them all (DeployPayload.Processes): the
// agent pulls or builds once, runs pre-deploy once, rolls the processes one
// after the other in the order the control plane sent (processes with a port
// first, so a broken web process stops the deploy before anything else is
// touched), commits once, then retires the previous release and runs
// post-deploy once. A process that fails undoes every process that already
// rolled: the deploy is all or nothing.
//
// Each process rolls through a per-process view of the deploy (forProcess): the
// payload projected onto that process, so the single-process helpers
// (createAndStartAll, healthPhase, spec, stopOld, ...) work on it unchanged.
// A payload carrying exactly one ProcessSpec is such a projection; payloads
// rebuilt from container labels (scale, convergence) are projections too.

// validateProcessSpecs checks the processes of a deploy payload; a payload
// without them is a single-process deploy and always passes.
func validateProcessSpecs(p agentprotocol.DeployPayload) error {
	if len(p.Processes) == 0 {
		return nil
	}
	if p.Stack != nil || p.OneShot {
		return errors.New("processes cannot be combined with a compose stack or a one-shot task")
	}
	if n := len(p.Processes); n < 2 || n > agentprotocol.MaxProcesses {
		return fmt.Errorf("a multi-process deploy needs 2 to %d processes, got %d", agentprotocol.MaxProcesses, n)
	}
	names := map[string]bool{}
	ports := map[int]string{}
	defaults := 0
	for _, pr := range p.Processes {
		switch {
		case !agentprotocol.ProcessNamePattern.MatchString(pr.Name):
			return fmt.Errorf("process name %q is invalid", pr.Name)
		case names[pr.Name]:
			return fmt.Errorf("process %q is listed twice", pr.Name)
		case pr.Port < 0 || pr.Port > 65535:
			return fmt.Errorf("process %q: port %d is out of range", pr.Name, pr.Port)
		case pr.Replicas < 1 || pr.Replicas > agentprotocol.MaxReplicas:
			return fmt.Errorf("process %q: replicas %d is out of range", pr.Name, pr.Replicas)
		case (pr.Singleton || len(p.Volumes) > 0) && pr.Replicas != 1:
			return fmt.Errorf("process %q: a singleton, or a service with volumes, runs one replica", pr.Name)
		case pr.Strategy != agentprotocol.StrategyRolling && pr.Strategy != agentprotocol.StrategyRecreate:
			return fmt.Errorf("process %q: unknown strategy %q", pr.Name, pr.Strategy)
		}
		if pr.Port > 0 {
			if other, ok := ports[pr.Port]; ok {
				return fmt.Errorf("processes %q and %q both use port %d", other, pr.Name, pr.Port)
			}
			ports[pr.Port] = pr.Name
		}
		names[pr.Name] = true
		if pr.Default {
			defaults++
		}
	}
	if defaults != 1 {
		return fmt.Errorf("exactly one process must be the default, got %d", defaults)
	}
	return nil
}

// projectProcess is the payload as one process sees it: the process' values
// stand where the service-level runtime fields are, and Processes holds that
// process alone.
func projectProcess(p agentprotocol.DeployPayload, spec agentprotocol.ProcessSpec) agentprotocol.DeployPayload {
	p.Processes = []agentprotocol.ProcessSpec{spec}
	p.Port, p.StartCommand, p.Healthcheck = spec.Port, spec.StartCommand, spec.Healthcheck
	p.StopGraceSeconds, p.MemoryMB, p.CPUs = spec.StopGraceSeconds, spec.MemoryMB, spec.CPUs
	p.Replicas, p.Strategy = spec.Replicas, spec.Strategy
	p.Hosts, p.DomainRules, p.OriginLock = spec.Hosts, spec.DomainRules, spec.OriginLock
	return p
}

// projectProcessByName projects a payload onto a process of it; "" is the
// default process.
func projectProcessByName(p agentprotocol.DeployPayload, name string) (agentprotocol.DeployPayload, error) {
	for _, spec := range p.Processes {
		if spec.Name == name || (name == "" && spec.Default) {
			return projectProcess(p, spec), nil
		}
	}
	return p, fmt.Errorf("the service has no process %q", name)
}

// forProcess is the deploy as one process sees it. It shares what belongs to
// the whole deployment (engine, command, redactor, log streams, cancel handle,
// image) and owns the payload projection, names, ids, created containers and
// index.
func (d *deploy) forProcess(spec agentprotocol.ProcessSpec) *deploy {
	return &deploy{
		e: d.e, cmd: d.cmd, p: projectProcess(d.p, spec), red: d.red, dlog: d.dlog, log: d.log, dbg: d.dbg, ctl: d.ctl,
		silent: d.silent, live: d.live, scaledAt: d.scaledAt, cloneEnv: d.cloneEnv,
		imageID: d.imageID, imageDigest: d.imageDigest, buildInfo: d.buildInfo,
		parent: d,
	}
}

// proc is the process this deploy is the view of; nil for a single-process
// deploy and for the command's own deploy of a multi-process service.
func (d *deploy) proc() *agentprotocol.ProcessSpec {
	if len(d.p.Processes) == 1 {
		return &d.p.Processes[0]
	}
	return nil
}

// processName is the name of the view's process ("" for a single-process deploy).
func (d *deploy) processName() string {
	if pr := d.proc(); pr != nil {
		return pr.Name
	}
	return ""
}

// root is the command's own deploy, which a view reports its timings to.
func (d *deploy) root() *deploy {
	for d.parent != nil {
		d = d.parent
	}
	return d
}

// procPrefix starts every event message and log line of a process.
func (d *deploy) procPrefix() string {
	if pr := d.proc(); pr != nil {
		return "[" + pr.Name + "] "
	}
	return ""
}

// containerName is the name of replica idx of the deploy's process.
func (d *deploy) containerName(idx int) string {
	if pr := d.proc(); pr != nil {
		return agentprotocol.ProcessContainerName(d.p.ServiceSlug, d.p.DeploymentID, pr.Name, pr.Default, idx)
	}
	return agentprotocol.ContainerName(d.p.ServiceSlug, d.p.DeploymentID, idx)
}

// routeID is the ServiceID of the http route of this deploy: the service's own
// for the default process, a derived one for each named process.
func (d *deploy) routeID() string {
	if pr := d.proc(); pr != nil && !pr.Default {
		return agentprotocol.ProcessRouteID(d.p.ServiceID, pr.Name)
	}
	return d.p.ServiceID
}

// ownsRoute reports whether r is this deploy's route (its process' route).
func (d *deploy) ownsRoute(r agentprotocol.Route) bool { return r.ServiceID == d.routeID() }

// verifiableRoutes are the http routes of this deploy in table: the ones a
// verification probes. A single-process deploy owns every route of the service.
func (d *deploy) verifiableRoutes(table []agentprotocol.Route) []agentprotocol.Route {
	if d.proc() == nil {
		return ownRoutes(table, d.p.ServiceID)
	}
	var out []agentprotocol.Route
	for _, r := range table {
		if d.ownsRoute(r) && !r.IsL4() && len(r.Hosts) > 0 && len(r.Upstreams) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// scaleRoutePhase is the route phase of a scale: a process without a port has
// no route to change.
func (d *deploy) scaleRoutePhase(ctx context.Context, st *deployState) (string, bool) {
	if pr := d.proc(); pr != nil && pr.Port <= 0 {
		return "", true
	}
	return d.routePhase(ctx, st)
}

// ownedByService reports whether a route belongs to the service: its own
// route, or the route of one of its named processes.
func ownedByService(r agentprotocol.Route, serviceID string) bool {
	return r.ServiceID == serviceID || r.Stack == serviceID
}

// routedProcess reports whether a process of the payload has an http route.
func routedProcess(spec agentprotocol.ProcessSpec) bool { return spec.Port > 0 && len(spec.Hosts) > 0 }

func (d *deploy) anyRouted() bool {
	return slices.ContainsFunc(d.p.Processes, routedProcess)
}

// debugProcesses describes the processes of the payload in the debug stream.
func (d *deploy) debugProcesses() {
	for _, pr := range d.p.Processes {
		d.debugf("process %s%s: %s, %d replica(s), port %d, stop grace %ds, singleton %t, %d host(s)", pr.Name,
			map[bool]string{true: " (default)", false: ""}[pr.Default], pr.Strategy, pr.Replicas, pr.Port, pr.StopGraceSeconds, pr.Singleton, len(pr.Hosts))
	}
}

// ---- which process a container runs ----

// processOf names the process a container runs and whether it is the default
// one. A container without the label (a single-process deployment, or a
// release older than processes) runs the default process, named "".
func processOf(c docker.ContainerInfo) (name string, isDefault bool) {
	name = c.Labels[agentprotocol.LabelProcess]
	if name == "" {
		return "", true
	}
	var sl specLabel
	_ = json.Unmarshal([]byte(c.Labels[agentprotocol.LabelSpec]), &sl)
	return name, sl.ProcessDefault
}

// inProcess reports whether a container runs the named process; "" is the
// default process.
func inProcess(c docker.ContainerInfo, process string) bool {
	name, isDefault := processOf(c)
	if process == "" {
		return isDefault
	}
	return name == process
}

// sortByProcessReplica orders containers by process name, then replica index.
func sortByProcessReplica(cs []docker.ContainerInfo) {
	sort.SliceStable(cs, func(i, j int) bool {
		if a, b := cs[i].Labels[agentprotocol.LabelProcess], cs[j].Labels[agentprotocol.LabelProcess]; a != b {
			return a < b
		}
		return replicaIndex(cs[i]) < replicaIndex(cs[j])
	})
}

// ---- desired state with processes ----

// hasContainers reports whether the entry names any container.
func (ent desiredEntry) hasContainers() bool {
	return slices.ContainsFunc(ent.procs(), func(p desiredProcess) bool { return len(p.ContainerNames) > 0 })
}

// namesOfRoute lists the containers the route r is meant to point at: those
// of the process that owns it. A route no process owns has none.
func (ent desiredEntry) namesOfRoute(r agentprotocol.Route) []string {
	if len(ent.Processes) == 0 {
		if r.Stack != "" {
			return nil // a compose stack's route, or a process route this agent has no processes for
		}
		return ent.ContainerNames
	}
	for _, p := range ent.Processes {
		switch {
		case r.Stack != "":
			if !p.Default && agentprotocol.ProcessRouteID(r.Stack, p.Name) == r.ServiceID {
				return p.ContainerNames
			}
		case p.Default:
			return p.ContainerNames
		}
	}
	return nil
}

// frameEntry is the desired entry of one service of a reconcile frame.
func frameEntry(ds agentprotocol.DesiredService) desiredEntry {
	ent := desiredEntry{
		DeploymentID: ds.DeploymentID, Replicas: ds.Replicas, ContainerNames: ds.ContainerNames, Routes: ds.Routes, Stack: ds.Stack,
		ScaledAt: ds.ScaledAt,
	}
	for _, dp := range ds.Processes {
		pr := desiredProcess{Name: dp.Name, Default: dp.Default, Replicas: dp.Replicas, ContainerNames: dp.ContainerNames, ScaledAt: dp.ScaledAt}
		if pr.Replicas == 0 {
			pr.Replicas = len(pr.ContainerNames)
		}
		ent.Processes = append(ent.Processes, pr)
	}
	ent.mirrorDefault()
	if ent.Replicas == 0 {
		ent.Replicas = len(ent.ContainerNames)
	}
	return ent
}

// withScaledProcess is the entry the agent records after scaling one process:
// the service's recorded entry with that process replaced, the others as they
// were. ent carries the deployment and the freshly read routes. The engine
// lock is held.
func (e *Engine) withScaledProcess(serviceID string, ent desiredEntry, pr desiredProcess) desiredEntry {
	base, ok := e.local[serviceID]
	if !ok {
		base, ok = e.desired[serviceID]
	}
	if !ok || base.DeploymentID != ent.DeploymentID || len(base.Processes) == 0 {
		ent.Processes = []desiredProcess{pr}
		ent.mirrorDefault()
		return ent
	}
	merged := base.withProcess(pr)
	merged.Routes = ent.Routes
	return merged
}

// processEntry is the replica set of every process that rolled, in rollout order.
func (d *deploy) processEntry() desiredEntry {
	ent := desiredEntry{DeploymentID: d.p.DeploymentID}
	for _, v := range d.views {
		pr := v.proc()
		ent.Processes = append(ent.Processes, desiredProcess{
			Name: pr.Name, Default: pr.Default, Replicas: len(v.names), ContainerNames: slices.Clone(v.names), ScaledAt: d.scaledAt,
		})
	}
	ent.mirrorDefault()
	return ent
}

// ---- the rollout ----

// procRun is one process of a multi-process deploy while it rolls.
type procRun struct {
	spec  agentprotocol.ProcessSpec // Replicas already clamped to what runs
	names []string
	d     *deploy // the process' view, set once the image is known
	// st holds the previous containers of this process and whether it
	// recreates (stops the old before starting the new) instead of rolling.
	st       *deployState
	existing []*docker.ContainerInfo // containers of this deployment from an earlier attempt, by replica
	started  bool                    // its rollout began
}

// processLedger is what a multi-process deploy has changed so far. The
// processes share it, so a failure anywhere can undo everything.
type processLedger struct {
	runs      []*procRun
	prev      []docker.ContainerInfo // every container of the service from other deployments
	serving   []docker.ContainerInfo // those that were running when the deploy began
	orphans   []docker.ContainerInfo // previous containers no process of the payload owns
	toRestart []docker.ContainerInfo // containers a recreate stopped; restarted when the deploy fails

	orphansStopped bool // the serving orphans a recreate must not overlap were stopped

	// Routes. ownPrev is the service's own routes in the live table before the
	// first change to them; a restore puts exactly these back and leaves every
	// other service's routes as they are now.
	havePrev       bool
	ownPrev        []agentprotocol.Route
	routePhased    bool // a route phase has run, so stale routes of the service are gone
	applied        bool // a route change was applied
	routesRestored bool
	routeErr       error // restoring the previous routes failed
}

// ownedBy finds the process one of whose planned containers has this name.
func (led *processLedger) ownedBy(name string) (*procRun, int) {
	for _, pr := range led.runs {
		if i := indexOfName(pr.names, name); i >= 0 {
			return pr, i
		}
	}
	return nil, -1
}

// processFor finds the process a previous container belongs to, by the same
// rule as processOf: a container the default process produced (or one without
// the label) belongs to the payload's default process whatever it is named
// now, any other to the process it is labelled with. nil means an orphan.
func (led *processLedger) processFor(c docker.ContainerInfo) *procRun {
	name, isDefault := processOf(c)
	for _, pr := range led.runs {
		if (isDefault && pr.spec.Default) || (!isDefault && pr.spec.Name == name) {
			return pr
		}
	}
	return nil
}

func (led *processLedger) hadPrevious() bool {
	return len(led.serving) > 0 || len(led.toRestart) > 0
}

// state is the ledger as the single-process failure handling (abort,
// abortCanceled) reads it: one deployState for the whole deploy, and the
// created containers of every process gathered on the command's deploy.
func (led *processLedger) state(d *deploy) *deployState {
	d.created = nil
	for _, v := range d.views {
		d.created = append(d.created, v.created...)
	}
	return &deployState{
		recreate:   len(led.toRestart) > 0,
		prev:       led.prev,
		serving:    led.serving,
		toRestart:  led.toRestart,
		prevRoutes: led.ownPrev,
		// A route that was swapped and put back is a rollback of the previous release.
		verifyFailed:   led.applied,
		routeErr:       led.routeErr,
		applied:        led.applied,
		routesRestored: led.routesRestored,
	}
}

// restoreRoutes puts the service's previous own routes back (the route lock is
// held): the live table minus the service's routes plus the snapshot, never a
// stale whole table. A failure is recorded in routeErr, which ends the deploy
// as rollback_failed.
func (led *processLedger) restoreRoutes(d *deploy) {
	if !led.applied || led.routesRestored || led.routeErr != nil {
		return
	}
	e := d.e
	cur := e.cfg.Proxy.Current()
	table := make([]agentprotocol.Route, 0, len(cur)+len(led.ownPrev))
	for _, r := range cur {
		if !ownedByService(r, d.p.ServiceID) {
			table = append(table, r)
		}
	}
	table = append(table, led.ownPrev...)
	err := e.cfg.Proxy.Apply(table)
	d.debugErrf(err, "proxy restore of the service's previous routes (%s): %s, generation now %s", routeSummary(led.ownPrev), errLabel(err), proxyGeneration(e.cfg.Proxy))
	if err != nil {
		led.routeErr = err
		d.logf("Restoring previous routes failed: %v", err)
		return
	}
	led.routesRestored = true
	d.logf("Previous routes restored")
}

// unwindRoutes restores the previous routes if any route changed.
func (led *processLedger) unwindRoutes(d *deploy) {
	d.e.routeMu.Lock()
	defer d.e.routeMu.Unlock()
	led.restoreRoutes(d)
}

// results lists every process' outcome for a deploy that did not succeed.
// failed is the index of the process that failed (-1 when none did, e.g. a
// cancel); skipped is why the ones that never started did not.
func (led *processLedger) results(failed int, reason, skipped, outcome string) []agentprotocol.ProcessResult {
	out := make([]agentprotocol.ProcessResult, len(led.runs))
	for i, pr := range led.runs {
		r := agentprotocol.ProcessResult{Name: pr.spec.Name}
		switch {
		case i == failed:
			r.Outcome, r.Message = agentprotocol.OutcomeFailed, reason
		case !pr.started:
			r.Outcome, r.Message = processSkipped, skipped
		case outcome == agentprotocol.OutcomeRollbackFailed:
			r.Outcome, r.Message = agentprotocol.OutcomeFailed, "could not be rolled back"
		default:
			r.Outcome, r.Message = agentprotocol.OutcomeRolledBack, "rolled back"
		}
		out[i] = r
	}
	return out
}

// processSkipped is the ProcessResult outcome of a process that never started
// because an earlier one failed.
const processSkipped = "skipped"

// rollStatus is how one process' rollout ended.
type rollStatus int

const (
	rollOK rollStatus = iota
	rollInterrupted
	rollFailed
)

// runProcesses is the deploy of a payload with processes.
func (d *deploy) runProcesses(ctx context.Context) agentprotocol.DeployResult {
	e := d.e
	led := &processLedger{}
	d.names = nil
	for _, spec := range d.p.Processes {
		// A singleton, or a service with volumes, never runs two instances of a process.
		if spec.Singleton || len(d.p.Volumes) > 0 {
			spec.Replicas = 1
		}
		spec.Replicas = max(spec.Replicas, 1)
		pr := &procRun{
			spec:  spec,
			names: agentprotocol.ProcessContainerNames(d.p.ServiceSlug, d.p.DeploymentID, spec.Name, spec.Default, spec.Replicas),
			st: &deployState{
				// Recreate stops the previous containers first: a singleton must never run twice, not even briefly.
				recreate: spec.Strategy != agentprotocol.StrategyRolling || spec.Singleton || len(d.p.Volumes) > 0,
			},
		}
		pr.existing = make([]*docker.ContainerInfo, len(pr.names))
		led.runs = append(led.runs, pr)
		d.names = append(d.names, pr.names...)
		d.debugf("plan: process %s: %s, %d replica(s)", spec.Name, map[bool]string{true: "recreate", false: "rolling"}[pr.st.recreate], spec.Replicas)
	}
	d.ids = make([]string, len(d.names))

	env, err := d.buildEnv()
	if err != nil {
		return d.fail(err.Error())
	}

	// 1. Pull or build, once for every process. Failure leaves everything untouched.
	if err := d.fetchImage(ctx); err != nil {
		if ctx.Err() != nil {
			return d.processesInterrupted(ctx, led)
		}
		return d.fail(err.Error())
	}

	// 1b. Pre-deploy, once, before anything of the previous release is touched.
	if msg, ok := d.preDeploy(ctx, env); !ok {
		if ctx.Err() != nil {
			return d.processesInterrupted(ctx, led)
		}
		return d.fail(msg)
	}
	if ctx.Err() != nil {
		return d.processesInterrupted(ctx, led)
	}

	// 2. Inventory, once. A container of this deployment with a process' name
	// is resumable; every other container of the service is previous: it
	// belongs to the process it is labelled with, or is an orphan (a removed or
	// renamed process, or a container of a release without processes whose
	// default process no longer exists).
	all, err := e.containersOf(ctx, d.p.ServiceID, true)
	if err != nil {
		return d.fail("list containers: " + err.Error())
	}
	for i := range all {
		c := all[i]
		if c.Labels[agentprotocol.LabelDeployment] == d.p.DeploymentID {
			if pr, idx := led.ownedBy(c.Name); pr != nil {
				pr.existing[idx] = &all[i]
				continue
			}
		}
		serving := c.Running || c.Restarting
		led.prev = append(led.prev, c)
		if serving {
			led.serving = append(led.serving, c)
		}
		pr := led.processFor(c)
		if pr == nil {
			led.orphans = append(led.orphans, c)
			continue
		}
		pr.st.prev = append(pr.st.prev, c)
		if serving {
			pr.st.serving = append(pr.st.serving, c)
		}
	}
	d.debugf("inventory: %d other managed container(s) of this service, %d serving, %d orphaned: %s",
		len(led.prev), len(led.serving), len(led.orphans), containerNames(led.prev))
	for _, pr := range led.runs {
		pr.d = d.forProcess(pr.spec)
		pr.d.names, pr.d.ids = pr.names, make([]string, len(pr.names))
		d.views = append(d.views, pr.d)
	}

	// 3. Roll the processes in the order sent.
	for i, pr := range led.runs {
		status, failedID, reason := d.rollProcess(ctx, env, led, pr)
		if d.warning == "" {
			d.warning = pr.d.warning
		}
		switch status {
		case rollInterrupted:
			return d.processesInterrupted(ctx, led)
		case rollFailed:
			return d.processFailed(ctx, led, i, failedID, reason)
		}
	}
	// No process has a route: the service's earlier routes still have to go.
	if !led.routePhased {
		if reason, ok := d.processRoutePhase(ctx, led, false); !ok {
			if ctx.Err() != nil {
				return d.processesInterrupted(ctx, led)
			}
			return d.processFailed(ctx, led, -1, "", reason)
		}
	}

	// 4. Commit point, once: from here the new release is live and a cancel is
	// too late. A cancel that got in first wins and unwinds everything.
	if ctx.Err() != nil || !d.ctl.commit() {
		return d.processesInterrupted(ctx, led)
	}

	// 5. Retire the previous release, once.
	stopRetire := d.phase("retire")
	d.retireAll(ctx, led)
	stopRetire()

	// 6. Bookkeeping, tailnet and post-deploy, once.
	d.recordCurrent()
	d.pruneImages(ctx)
	d.tailnetStep(ctx)
	d.postDeploy(ctx, env)

	parts := make([]string, len(led.runs))
	res := make([]agentprotocol.ProcessResult, len(led.runs))
	containerID := ""
	for i, pr := range led.runs {
		parts[i] = pr.spec.Name
		msg := "1 replica"
		if n := len(pr.names); n > 1 {
			parts[i] += fmt.Sprintf(" ×%d", n)
			msg = fmt.Sprintf("%d replicas", n)
		}
		res[i] = agentprotocol.ProcessResult{Name: pr.spec.Name, Outcome: agentprotocol.OutcomeSucceeded, Message: msg}
		if pr.spec.Default {
			containerID = pr.d.firstID()
		}
	}
	msg := fmt.Sprintf("Deployed %d processes (%s)", len(led.runs), strings.Join(parts, ", "))
	if d.warning != "" {
		msg += ". Warning: " + d.warning
	}
	d.log.Info("deploy succeeded", "processes", len(led.runs), "containers", len(d.names))
	out := d.result(agentprotocol.OutcomeSucceeded, msg, containerID)
	out.Processes = res
	return out
}

// rollProcess brings one process up next to (rolling) or instead of
// (recreate) its previous containers, up to and including its route. On
// failure it returns the failing container's ID ("" when none) and the reason.
// The previous containers of a rolling process are not touched; the commit
// and the retire happen once, after the last process.
func (d *deploy) rollProcess(ctx context.Context, env []string, led *processLedger, pr *procRun) (rollStatus, string, string) {
	v, st := pr.d, pr.st
	if ctx.Err() != nil {
		return rollInterrupted, "", "" // canceled between processes: nothing of this one is touched
	}
	pr.started = true
	resumed := make([]bool, len(v.names))
	allResumed := true
	for i, ex := range pr.existing {
		if ex == nil {
			allResumed = false
			continue
		}
		if ex.Running && ex.ImageID == v.imageID {
			v.ids[i], resumed[i] = ex.ID, true
			v.created = append(v.created, ex.ID)
			v.logf("Container %s from an earlier attempt is running; continuing", v.names[i])
			continue
		}
		allResumed = false
		v.logf("Removing stale container %s from an earlier attempt", v.names[i])
		v.removeContainer(ctx, ex.ID)
	}

	if st.recreate && !allResumed {
		v.stopOld(ctx, st)
	} else if st.recreate && allResumed && len(st.serving) == 0 && len(st.prev) > 0 {
		// An earlier attempt already stopped the old container; remember the
		// newest as the restore target.
		sort.Slice(st.prev, func(i, j int) bool { return st.prev[i].Created.After(st.prev[j].Created) })
		st.toRestart = []docker.ContainerInfo{st.prev[0]}
	}
	if st.recreate && !led.orphansStopped {
		led.orphansStopped = true
		d.stopOrphans(ctx, led)
	}
	led.toRestart = append(led.toRestart, st.toRestart...)
	if ctx.Err() != nil {
		return rollInterrupted, "", "" // canceled while the old container stopped
	}

	if !allResumed {
		stop := v.phase("create and start")
		failedID, err := v.createAndStartAll(ctx, env, resumed)
		stop()
		if err != nil {
			if ctx.Err() != nil {
				return rollInterrupted, "", ""
			}
			return rollFailed, failedID, err.Error()
		}
	}
	if ctx.Err() != nil {
		return rollInterrupted, "", ""
	}

	stopHealth := v.phase("health")
	failedID, reason, healthy := v.healthPhase(ctx, v.ids)
	stopHealth()
	if !healthy {
		if ctx.Err() != nil {
			return rollInterrupted, "", ""
		}
		return rollFailed, failedID, reason
	}

	if ctx.Err() != nil {
		return rollInterrupted, "", ""
	}
	if routedProcess(pr.spec) {
		stopRoute := v.phase("route and verify")
		reason, ok := v.processRoutePhase(ctx, led, true)
		stopRoute()
		if !ok {
			if ctx.Err() != nil {
				return rollInterrupted, "", ""
			}
			return rollFailed, "", reason
		}
	}
	return rollOK, "", ""
}

// routedIDs are the route IDs the payload's processes will have.
func (d *deploy) routedIDs() map[string]bool {
	ids := map[string]bool{}
	for _, spec := range d.root().p.Processes {
		if !routedProcess(spec) {
			continue
		}
		if spec.Default {
			ids[d.p.ServiceID] = true
		} else {
			ids[agentprotocol.ProcessRouteID(d.p.ServiceID, spec.Name)] = true
		}
	}
	return ids
}

// processRoutePhase changes the service's routes for a process (own) and
// verifies the new route through the proxy; with own false it only removes the
// service's routes that no process of the payload wants. The table is the
// proxy's live table without this process' route, plus its new one; the first
// phase of a deploy also drops the service's routes that match no process of
// the payload (a removed process, an old default route). Nothing is committed
// here: the commit is the deploy's, after the last process.
func (d *deploy) processRoutePhase(ctx context.Context, led *processLedger, own bool) (string, bool) {
	e := d.e
	e.routeMu.Lock()
	defer e.routeMu.Unlock()
	if ctx.Err() != nil {
		return "", false
	}

	live := e.cfg.Proxy.Current()
	if !led.havePrev {
		for _, r := range live {
			if ownedByService(r, d.p.ServiceID) {
				led.ownPrev = append(led.ownPrev, r)
			}
		}
		led.havePrev = true
	}
	first := !led.routePhased
	d.debugf("routes before: %s (proxy generation %s)", routeSummary(live), proxyGeneration(e.cfg.Proxy))
	if first && d.p.Proxy != nil {
		if err := e.installProxyConfig(d.p.Proxy, d.cmd.Secrets); err != nil {
			d.event(agentprotocol.StepRoute, agentprotocol.StepFailed, "Proxy configuration rejected: "+err.Error())
			return "Proxy configuration rejected: " + err.Error(), false
		}
	}

	base := live
	if len(base) == 0 {
		base = d.p.Routes // a proxy with no state yet is seeded from the payload
	}
	wanted := d.routedIDs()
	table := make([]agentprotocol.Route, 0, len(base)+1)
	for _, r := range base {
		switch {
		case own && d.ownsRoute(r):
			continue // replaced below
		case first && ownedByService(r, d.p.ServiceID) && !wanted[r.ServiceID]:
			continue // a removed process, or a route nothing in this release serves
		}
		table = append(table, r)
	}
	if own {
		table = append(table, d.route())
	}
	table, err := resolveRoutes(table, d.cmd.Secrets)
	if err != nil {
		d.event(agentprotocol.StepRoute, agentprotocol.StepFailed, "Route table rejected: "+err.Error())
		return "Route table rejected: " + err.Error(), false
	}
	if len(live) > 0 {
		// The live table is the authority on maintenance state; a route built
		// from the payload carries the state it was dispatched with.
		table = inheritMaintenance(table, live)
	}
	d.debugf("routes after: %s; own %s", routeSummary(table), routeSummary(d.verifiableRoutes(table)))
	if (first && d.p.Proxy != nil) || !routesEqual(table, live) {
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
		led.applied = true
		d.event(agentprotocol.StepRoute, agentprotocol.StepSucceeded, "")
	}
	led.routePhased = true
	if canceled(ctx) {
		led.restoreRoutes(d) // the cancel arrived while the table was applied
		return "", false
	}
	if !own {
		return "", true
	}

	var (
		host, prefix string
		target       agentprotocol.Route
		ok           bool
	)
	for _, r := range d.verifiableRoutes(table) {
		if host, prefix, ok = verifyTarget(r); ok {
			target = r
			break
		}
	}
	if !ok {
		return "", true // redirect-only: nothing is served to probe
	}
	switch res, last := d.verifyThroughProxy(ctx, host, prefix+d.healthPath(), len(target.Upstreams)); res {
	case verifyOK:
		return "", true
	case verifyCanceled:
		led.restoreRoutes(d) // canceled during verification: back to the previous routes first
		return "", false
	case verifyStopped:
		return "agent stopped during deployment", false
	default:
		led.restoreRoutes(d) // before anything else changes
		return "Verification through the proxy failed: " + last, false
	}
}

// stopOrphans stops the serving orphans a recreate would overlap: a singleton
// of a removed or renamed process, and every orphan when the service has
// volumes (the replacement mounts the same ones). They are remembered for a
// restart should the deploy fail; the rest of the orphans wait for retireAll.
func (d *deploy) stopOrphans(ctx context.Context, led *processLedger) {
	var serving []docker.ContainerInfo
	for _, c := range led.orphans {
		if !c.Running && !c.Restarting {
			continue
		}
		var sl specLabel
		_ = json.Unmarshal([]byte(c.Labels[agentprotocol.LabelSpec]), &sl)
		if sl.Singleton || len(d.p.Volumes) > 0 {
			serving = append(serving, c)
		}
	}
	if len(serving) == 0 {
		return
	}
	d.ctl.setStep(stepStop)
	led.toRestart = append(led.toRestart, d.stopContainers(ctx, serving, func(c docker.ContainerInfo) time.Duration {
		return ownGrace(c, d.p.StopGraceSeconds)
	})...)
}

// retireAll stops and removes the previous containers of every process and
// every orphan, in parallel, once the whole new release is live. A rolling
// process' old replicas get the drain delay first; each container gets the stop
// grace it was started with (its spec label), the grace of the process it
// belonged to (an orphan: the default process') when it has none.
func (d *deploy) retireAll(ctx context.Context, led *processLedger) {
	if len(led.prev) == 0 {
		return
	}
	e := d.e
	d.event(agentprotocol.StepRetire, agentprotocol.StepStarted, "Retiring previous release")
	drain := false
	for _, pr := range led.runs {
		if !pr.st.recreate && len(pr.st.serving) > 0 {
			drain = true
		}
	}
	if drain {
		select { // drain in-flight requests that were routed to the old containers
		case <-time.After(e.cfg.DrainDelay):
		case <-ctx.Done():
		}
		d.debugf("retire: waited up to %s for in-flight requests to drain", e.cfg.DrainDelay)
	}

	type target struct {
		c     docker.ContainerInfo
		grace time.Duration
	}
	var targets []target
	longest := time.Duration(0)
	for _, pr := range led.runs {
		for _, c := range pr.st.prev {
			g := ownGrace(c, pr.spec.StopGraceSeconds)
			longest = max(longest, g)
			targets = append(targets, target{c, g})
		}
	}
	for _, c := range led.orphans {
		g := ownGrace(c, d.p.StopGraceSeconds)
		longest = max(longest, g)
		targets = append(targets, target{c, g})
	}
	stopped := map[string]bool{} // already stopped by a recreate
	for _, c := range led.toRestart {
		stopped[c.ID] = true
	}
	cctx, cancel := detached(ctx, longest+2*time.Minute)
	defer cancel()
	parallel(len(targets), func(i int) {
		c, grace := targets[i].c, targets[i].grace
		if (c.Running || c.Restarting) && !stopped[c.ID] {
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

// ---- failure and cancel ----

// processFailed unwinds a deploy in which process k failed (k < 0: no process
// did, a route change failed): the routes go back, the new containers of every
// process are removed, stopped singletons start again. A process that was never
// started is reported as skipped. The previous release's rolling containers
// were never touched.
func (d *deploy) processFailed(ctx context.Context, led *processLedger, k int, failedID, reason string) agentprotocol.DeployResult {
	text := reason
	skipped := "not started: " + reason
	if k >= 0 {
		text = fmt.Sprintf("Process %s failed: %s", led.runs[k].spec.Name, reason)
		skipped = "not started: process " + led.runs[k].spec.Name + " failed"
	}
	led.unwindRoutes(d)
	res := d.abort(ctx, led.state(d), led.hadPrevious(), failedID, text)
	res.Processes = led.results(k, d.red.apply(reason), skipped, res.Outcome)
	return res
}

// processesInterrupted ends a deploy whose context ended: a cancel unwinds
// everything and reports canceled; anything else (the agent is stopping) leaves
// the state for the redelivered command to continue.
func (d *deploy) processesInterrupted(ctx context.Context, led *processLedger) agentprotocol.DeployResult {
	if !canceled(ctx) {
		return d.fail("agent stopped during deployment")
	}
	led.unwindRoutes(d)
	res := d.abortCanceled(ctx, led.state(d), led.hadPrevious())
	res.Processes = led.results(-1, "", "not started: the deployment was canceled", res.Outcome)
	return res
}
