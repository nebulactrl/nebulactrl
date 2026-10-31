package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// StatsInterval is how often container stats are reported.
	StatsInterval = 10 * time.Second
	// ObservedInterval is how often observed state is reported.
	ObservedInterval = 15 * time.Second

	statsParallelism = 4
	// healthyAfter is how long a container without a docker HEALTHCHECK must
	// have been up to be reported healthy.
	healthyAfter = 10 * time.Second
)

// Reconcile implements conn.Reconciler: it re-applies the routes, reports
// observed state, and sweeps orphans.
func (e *Engine) Reconcile(ctx context.Context, desired agentprotocol.Reconcile) error {
	if e.decom.Load() {
		return nil // the host is being emptied; nothing may be recreated
	}
	e.applyEndpoints(desired.Endpoints)
	if e.cfg.BuildOnly {
		return nil // a build server hosts nothing to reconcile, sweep or route
	}
	if desired.ACMEEmail != "" {
		e.cfg.Proxy.SetACMEEmail(desired.ACMEEmail)
	}
	e.applyTrafficSetting(desired.TrafficAnalytics)
	e.applyGeo(desired.Geo, desired.Secrets)
	e.applyLogs(desired.Logs, desired.Secrets)
	e.up.apply(desired.Uptime)

	// Local state wins for a service that is mid-deploy or was deployed here
	// after the control plane computed this frame.
	e.mu.Lock()
	effective := make(map[string]desiredEntry, len(desired.Services))
	table := []agentprotocol.Route{}
	var inflightRoutes []string
	for _, ds := range desired.Services {
		ent := frameEntry(ds)
		// A local deploy newer than this frame, or a scale that ran here after
		// the control plane built it, wins: the frame still carries the state
		// it replaced.
		if loc, ok := e.local[ds.ServiceID]; ok {
			ent = localWins(loc, ent)
		}
		if e.inflight[ds.ServiceID] > 0 {
			inflightRoutes = append(inflightRoutes, ds.ServiceID)
		}
		effective[ds.ServiceID] = ent
	}
	for id, loc := range e.local { // deployed here but not (yet) desired: keep serving
		if _, ok := effective[id]; !ok {
			effective[id] = loc
		}
	}
	e.mu.Unlock()

	// The server-level proxy configuration (certificates, upstream policy,
	// tailnet) is installed before the table that depends on it is applied.
	var errs []error
	// A frame older than a table already applied brings no table and no proxy
	// configuration: both would undo what the newer table changed. The hello
	// frame set the record on arrival (ObserveFrame), so it is never older.
	if !e.repl.isObserved() {
		e.observeTableSeq(desired)
	}
	stale, _ := e.tableSuperseded(desired.TableSeq)
	var cfgErr error
	if !stale {
		cfgErr = e.installProxyConfig(desired.Proxy, desired.Secrets)
	}
	if cfgErr != nil {
		errs = append(errs, fmt.Errorf("proxy configuration: %w", cfgErr))
	}

	e.routeMu.Lock()
	cur := e.cfg.Proxy.Current()
	for id, ent := range effective {
		if contains(inflightRoutes, id) {
			for _, r := range cur { // an in-flight deploy owns this service's routes
				if r.ServiceID == id || r.Stack == id {
					table = append(table, r)
				}
			}
			continue
		}
		table = append(table, ent.Routes...)
	}
	// Route services (external upstreams, tcp/udp) have no container to
	// converge: their routes are applied as sent.
	table = append(table, desired.ExternalRoutes...)
	// A service on its first deployment is in neither the frame nor e.local
	// until its command records it, and an e.local entry carries no routes when
	// the deploy routed nothing at the time: the live routes of every service
	// mid-command or deployed here win, as for a sync_routes table.
	table = e.keepLiveRoutes(table, cur)
	sort.SliceStable(table, func(i, j int) bool { return table[i].ServiceID < table[j].ServiceID })
	table = e.dropDeadUpstreams(ctx, table)
	// A table whose basic-auth secrets are missing, or whose configuration
	// could not be installed, is never applied: the previous one keeps serving.
	if old, newest := e.tableSuperseded(desired.TableSeq); old {
		e.log.Info("reconcile: route table superseded by a newer one, not applied", "table_seq", desired.TableSeq, "newest", newest)
	} else if resolved, err := resolveRoutes(table, desired.Secrets); cfgErr != nil {
		e.log.Warn("reconcile: route table not applied", "reason", "proxy configuration rejected")
	} else if err != nil {
		errs = append(errs, fmt.Errorf("resolve routes: %w", err))
	} else if err := e.cfg.Proxy.Apply(resolved); err != nil {
		errs = append(errs, fmt.Errorf("apply routes: %w", err))
	} else {
		e.noteTable(desired.TableSeq)
	}
	e.routeMu.Unlock()

	e.mu.Lock()
	e.desired = effective
	e.mu.Unlock()
	if e.repl.appliesFrame(desired.Services) { // else a newer frame was applied on arrival
		e.applyReplicationDesired(ctx, desired.Services)
	}

	e.reportObserved(ctx)
	e.attachDesiredNetworks(ctx, desired.Services, effective)
	if err := e.sweepOrphans(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.sweepTaskContainers(ctx); err != nil {
		errs = append(errs, err)
	}
	// A host shell helper of a session that died with the previous agent.
	if err := e.sweepHostShells(ctx); err != nil {
		errs = append(errs, err)
	}
	// A service container a dead volume backup left paused or stopped.
	if err := e.sweepBackups(ctx); err != nil {
		errs = append(errs, err)
	}
	// Last: a network that only just emptied by the sweeps above can go too.
	if err := e.sweepNetworks(ctx); err != nil {
		errs = append(errs, err)
	}
	// The control plane's replica count wins: catch up on scale commands that
	// expired while this server was offline.
	converge := map[string]desiredEntry{}
	for _, ds := range desired.Services {
		if ent := effective[ds.ServiceID]; ent.DeploymentID == ds.DeploymentID {
			converge[ds.ServiceID] = ent
		}
	}
	e.startConvergence(ctx, converge)
	// The mesh nodes converge to the set that rides every hello.
	e.reconcileMesh(ctx, desired)
	return errors.Join(errs...)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (e *Engine) desiredSnapshot() map[string]desiredEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]desiredEntry, len(e.desired))
	for k, v := range e.desired {
		out[k] = v
	}
	return out
}

// ---- observed ----

func (e *Engine) reportObserved(ctx context.Context) {
	desired := e.desiredSnapshot()
	if len(desired) == 0 {
		return
	}
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	obs := agentprotocol.Observed{Services: make([]agentprotocol.ObservedService, 0, len(ids))}
	for _, id := range ids {
		obs.Services = append(obs.Services, e.observe(ctx, id, desired[id]))
	}
	e.cfg.Sender.Observed(obs)
}

// observe reports one service: a state per replica of its current deployment
// plus the aggregate (see aggregateObserved).
func (e *Engine) observe(ctx context.Context, serviceID string, ent desiredEntry) agentprotocol.ObservedService {
	o := agentprotocol.ObservedService{
		ServiceID:       serviceID,
		DeploymentID:    ent.DeploymentID,
		State:           agentprotocol.StateMissing,
		Health:          agentprotocol.HealthUnknown,
		ReplicasDesired: ent.Replicas,
	}
	o.Tailnet = e.tn.Observe(ctx, serviceID)
	o.Replication = e.repl.statusOf(serviceID)
	if ent.Stack {
		return e.observeStack(ctx, o, serviceID, ent)
	}
	if !ent.hasContainers() {
		return o
	}
	var (
		all   []agentprotocol.ObservedReplica
		first *docker.ContainerInfo
	)
	for _, pr := range ent.procs() {
		reps, lead := e.observeReplicas(ctx, pr.ContainerNames, pr.Name)
		all = append(all, reps...)
		if lead != nil && (first == nil || (lead.Running && !first.Running)) {
			first = lead
		}
		if len(ent.Processes) > 0 {
			var agg agentprotocol.ObservedService
			aggregateObserved(&agg, reps)
			o.Processes = append(o.Processes, agentprotocol.ObservedProcess{
				Name: pr.Name, State: agg.State, Health: agg.Health,
				ReplicasReady: agg.ReplicasReady, ReplicasDesired: agg.ReplicasDesired,
			})
		}
	}
	if first != nil {
		o.ImageID = first.ImageID
	}
	aggregateObserved(&o, all)
	return o
}

// observeReplicas inspects the containers of one process (named by index) and
// returns the observed replicas plus the container that leads the process: the
// first one found, a running one in preference.
func (e *Engine) observeReplicas(ctx context.Context, names []string, process string) ([]agentprotocol.ObservedReplica, *docker.ContainerInfo) {
	reps := make([]agentprotocol.ObservedReplica, len(names))
	var first *docker.ContainerInfo
	for i, name := range names {
		reps[i] = agentprotocol.ObservedReplica{Index: i, Process: process, State: agentprotocol.StateMissing, Health: agentprotocol.HealthUnknown}
		info, err := e.cfg.Docker.ContainerInspect(ctx, name)
		if err != nil {
			if !errors.Is(err, docker.ErrNotFound) {
				e.log.Warn("observe: inspect container", "container", name, "err", err)
			}
			continue
		}
		reps[i] = replicaObserved(i, info)
		reps[i].Process = process
		if first == nil || (info.Running && !first.Running) {
			first = &info
		}
	}
	return reps, first
}

// replicaObserved maps one container to its observed replica state.
func replicaObserved(index int, info docker.ContainerInfo) agentprotocol.ObservedReplica {
	r := agentprotocol.ObservedReplica{
		Index: index, ContainerID: info.ID, StartedAt: info.StartedAt, RestartCount: info.RestartCount,
		Component: info.Labels[agentprotocol.LabelComponent],
	}
	switch {
	case info.Restarting:
		r.State = agentprotocol.StateRestarting
		r.Health = agentprotocol.HealthUnhealthy
	case info.Running:
		r.State = agentprotocol.StateRunning
		r.Health = healthOf(info)
	case info.State == "created":
		r.State = agentprotocol.StateCreated
		r.Health = agentprotocol.HealthStarting
	default:
		r.State = agentprotocol.StateExited
		r.Health = agentprotocol.HealthUnhealthy
		if (info.Labels[agentprotocol.LabelOneShot] == "true" || info.Labels[agentprotocol.LabelComponent] != "") && info.ExitCode == 0 {
			r.Health = agentprotocol.HealthHealthy // a one-shot or stack component that finished cleanly ("completed")
		}
	}
	return r
}

// aggregateObserved fills o from its replicas. State is running if any replica
// runs, missing if none exists. Health is healthy only when every desired
// replica is healthy; with none healthy it is starting while any replica is
// still starting, unknown when every replica is missing, otherwise unhealthy;
// anything in between is degraded.
func aggregateObserved(o *agentprotocol.ObservedService, reps []agentprotocol.ObservedReplica) {
	o.Replicas = reps
	o.ReplicasDesired = len(reps)
	var ready, existing, starting int
	state := agentprotocol.StateMissing
	rank := func(s string) int {
		switch s {
		case agentprotocol.StateRunning:
			return 4
		case agentprotocol.StateRestarting:
			return 3
		case agentprotocol.StateCreated:
			return 2
		case agentprotocol.StateExited:
			return 1
		}
		return 0
	}
	var lead *agentprotocol.ObservedReplica
	for i := range reps {
		r := &reps[i]
		if r.State != agentprotocol.StateMissing {
			existing++
		}
		if (r.State == agentprotocol.StateRunning || r.State == agentprotocol.StateExited) && r.Health == agentprotocol.HealthHealthy {
			ready++
		}
		if r.Health == agentprotocol.HealthStarting {
			starting++
		}
		if rank(r.State) > rank(state) {
			state = r.State
		}
		if r.State != agentprotocol.StateMissing && (lead == nil || rank(r.State) > rank(lead.State)) {
			lead = r
		}
	}
	o.ReplicasReady = ready
	o.State = state
	if lead != nil {
		o.ContainerID, o.StartedAt = lead.ContainerID, lead.StartedAt
	}
	switch {
	case len(reps) > 0 && ready == len(reps):
		o.Health = agentprotocol.HealthHealthy
	case existing == 0:
		o.Health = agentprotocol.HealthUnknown
	case ready == 0 && starting > 0:
		o.Health = agentprotocol.HealthStarting
	case ready == 0:
		o.Health = agentprotocol.HealthUnhealthy
	default:
		o.Health = agentprotocol.HealthDegraded
	}
}

func healthOf(info docker.ContainerInfo) string {
	switch info.Health {
	case "healthy":
		return agentprotocol.HealthHealthy
	case "unhealthy":
		return agentprotocol.HealthUnhealthy
	case "starting":
		return agentprotocol.HealthStarting
	}
	if time.Since(info.StartedAt) >= healthyAfter {
		return agentprotocol.HealthHealthy
	}
	return agentprotocol.HealthStarting
}

// ---- stats ----

// reportStats samples every running replica container of every managed service
// and sends one entry per replica: the containers of the current deployment
// when one runs, else those of the newest running deployment.
func (e *Engine) reportStats(ctx context.Context) {
	cs, err := e.cfg.Docker.ContainerList(ctx, docker.ListFilter{
		Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
	})
	if err != nil {
		e.log.Warn("stats: list containers", "err", err)
		return
	}
	desired := e.desiredSnapshot()
	byService := map[string][]docker.ContainerInfo{}
	for _, c := range cs {
		if !c.Running {
			continue
		}
		if sid := c.Labels[agentprotocol.LabelService]; sid != "" {
			byService[sid] = append(byService[sid], c)
		}
	}
	type target struct {
		sid string
		c   docker.ContainerInfo
	}
	var pick []target
	for sid, group := range byService {
		dep := ""
		if ent, ok := desired[sid]; ok {
			for _, c := range group {
				if ent.owns(c) {
					dep = ent.DeploymentID
					break
				}
			}
		}
		if dep == "" { // nothing current runs: fall back to the newest deployment
			newest := group[0]
			for _, c := range group[1:] {
				if c.Created.After(newest.Created) {
					newest = c
				}
			}
			dep = newest.Labels[agentprotocol.LabelDeployment]
		}
		for _, c := range group {
			if c.Labels[agentprotocol.LabelDeployment] == dep {
				pick = append(pick, target{sid, c})
			}
		}
	}
	if len(pick) == 0 {
		return
	}

	// Docker reports the daemon's total memory as the limit of a container
	// that has none; normalise that to 0 ("no limit") so consumers never show
	// host RAM as a configured limit.
	var hostMem uint64
	if info, err := e.cfg.Docker.Info(ctx); err == nil {
		hostMem = info.MemTotal
	}

	var (
		mu  sync.Mutex
		out = agentprotocol.Stats{Containers: make([]agentprotocol.ContainerStats, 0, len(pick))}
		wg  sync.WaitGroup
		sem = make(chan struct{}, statsParallelism)
	)
	for _, t := range pick {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			s, err := e.cfg.Docker.ContainerStats(sctx, t.c.ID)
			if err != nil {
				if ctx.Err() == nil && !errors.Is(err, docker.ErrNotFound) {
					e.log.Debug("stats: sample container", "container", t.c.Name, "err", err)
				}
				return
			}
			limit := s.MemLimit
			if hostMem > 0 && limit >= hostMem {
				limit = 0
			}
			mu.Lock()
			out.Containers = append(out.Containers, agentprotocol.ContainerStats{
				ServiceID: t.sid, Process: t.c.Labels[agentprotocol.LabelProcess], Replica: replicaIndex(t.c),
				CPUPercent: s.CPUPercent, MemBytes: s.MemBytes, MemLimit: limit,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(out.Containers) > 0 {
		sort.Slice(out.Containers, func(i, j int) bool {
			a, b := out.Containers[i], out.Containers[j]
			if a.ServiceID != b.ServiceID {
				return a.ServiceID < b.ServiceID
			}
			if a.Process != b.Process {
				return a.Process < b.Process
			}
			return a.Replica < b.Replica
		})
		e.cfg.Sender.Stats(out)
	}
}

// ---- orphan sweep ----

// sweepOrphans removes managed service containers that belong to no desired
// deployment and no in-flight command and are older than OrphanMinAge.
func (e *Engine) sweepOrphans(ctx context.Context) error {
	cs, err := e.cfg.Docker.ContainerList(ctx, docker.ListFilter{
		All:    true,
		Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
	})
	if err != nil {
		return fmt.Errorf("sweep: list containers: %w", err)
	}
	desired := e.desiredSnapshot()
	var errs []error
	for _, c := range cs {
		// Infrastructure containers carry the managed label but no service
		// label; the sweep must never touch the proxy or the tailnet node.
		if c.Name == agentprotocol.ProxyContainer || c.Name == agentprotocol.TailscaleContainer || c.Name == agentprotocol.PagesContainer || c.Name == agentprotocol.PagesContainerAlt {
			continue
		}
		sid := c.Labels[agentprotocol.LabelService]
		if sid == "" || e.isInflight(sid) {
			continue
		}
		// A stack's containers are compose's: those of a desired stack stay
		// whatever their deployment label says.
		if ent, ok := desired[sid]; ok && (ent.owns(c) || (ent.Stack && c.Labels[agentprotocol.LabelComponent] != "")) {
			continue
		}
		if time.Since(c.Created) < e.cfg.OrphanMinAge {
			continue
		}
		e.log.Info("removing orphaned container", "container", c.Name, "service_id", sid)
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

// ---- periodic work ----

// Run reports stats every StatsInterval and observed state every
// ObservedInterval, and prunes the build cache daily, until ctx ends. It
// returns only after the work tracked in e.bg has ended too: that work is
// cancelled with the engine (background) and waited for, so the caller may
// tear the host down afterwards without a straggler recreating anything.
func (e *Engine) Run(ctx context.Context) {
	defer func() {
		e.stopLife()
		e.bg.Wait()
	}()
	if e.cfg.BuildOnly {
		<-ctx.Done()
		return
	}
	var wg sync.WaitGroup
	loop := func(every, first time.Duration, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTimer(first)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				fn(ctx)
				t.Reset(every)
			}
		}()
	}
	loop(StatsInterval, StatsInterval, e.reportStats)
	loop(ObservedInterval, ObservedInterval, e.reportObserved)
	if e.cfg.Uptime != nil {
		every := e.uptimeInterval()
		loop(every, min(UptimeFirstProbe, every), e.probeUptime)
	}
	loop(ReplPollTick, 10*time.Second, e.pollReplication)
	loop(NetworkSweepInterval, NetworkSweepInterval, func(c context.Context) {
		if err := e.sweepNetworks(c); err != nil {
			e.log.Warn("network sweep failed", "err", err)
		}
	})
	// Helpers of host shells whose agent died mid-session: the first pass
	// follows the agent's start closely.
	loop(HostShellSweepInterval, 10*time.Second, func(c context.Context) {
		if err := e.sweepHostShells(c); err != nil {
			e.log.Warn("host shell sweep failed", "err", err)
		}
	})
	// Leftovers of volume backups that died (helpers, a frozen service).
	loop(BackupSweepInterval, 30*time.Second, func(c context.Context) {
		if err := e.sweepBackups(c); err != nil {
			e.log.Warn("volume backup sweep failed", "err", err)
		}
	})
	wg.Wait()
}
