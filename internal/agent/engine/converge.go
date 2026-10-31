package engine

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// specLabel is the non-secret runtime spec stored on every managed container
// (agentprotocol.LabelSpec). It lets the agent add replicas on its own, when a
// reconcile frame says more are wanted than run, without the deploy command.
type specLabel struct {
	Slug             string                        `json:"slug"`
	Port             int                           `json:"port,omitempty"`
	Healthcheck      agentprotocol.HealthcheckSpec `json:"healthcheck"`
	StopGraceSeconds int                           `json:"stopGraceSeconds,omitempty"`
	MemoryMB         int                           `json:"memoryMb,omitempty"`
	// Docker holds the allow-listed container options (replicas keep them).
	Docker *agentprotocol.DockerOptions `json:"docker,omitempty"`
	// DockerPolicy is the server policy the options were validated against.
	DockerPolicy *agentprotocol.DockerPolicy `json:"dockerPolicy,omitempty"`
	CPUs         float64                     `json:"cpus,omitempty"`
	// MeshAlias is the exposed-service alias (nc-svc-<id8>): a replica added
	// later carries it too.
	MeshAlias string `json:"meshAlias,omitempty"`
	// Network membership (see netplan.go): a replica added later must join the
	// same networks as its siblings.
	BlockEgress         bool                                    `json:"blockEgress,omitempty"`
	EnvHasEgressBlocked bool                                    `json:"envHasEgressBlocked,omitempty"`
	SharedNetworks      []agentprotocol.SharedNetworkAttachment `json:"sharedNetworks,omitempty"`
	// Process names the process of a multi-process deployment this container
	// runs ("" for a single-process service). The projected fields above
	// (Port, Healthcheck, StopGraceSeconds, MemoryMB, CPUs) are then the
	// process' own, so the label rebuilds that process alone.
	Process        string `json:"process,omitempty"`
	ProcessDefault bool   `json:"processDefault,omitempty"`
	Singleton      bool   `json:"singleton,omitempty"`
}

func specLabelOf(p agentprotocol.DeployPayload) string {
	sl := specLabel{
		Slug: p.ServiceSlug, Port: p.Port, Healthcheck: p.Healthcheck,
		StopGraceSeconds: p.StopGraceSeconds, MemoryMB: p.MemoryMB, CPUs: p.CPUs, Docker: p.Docker.Normalized(), DockerPolicy: p.DockerPolicy,
		BlockEgress: p.BlockEgress, EnvHasEgressBlocked: p.EnvHasEgressBlocked, SharedNetworks: p.SharedNetworks, MeshAlias: p.MeshAlias,
	}
	if len(p.Processes) == 1 {
		pr := p.Processes[0]
		sl.Process, sl.ProcessDefault, sl.Singleton = pr.Name, pr.Default, pr.Singleton
	}
	raw, err := json.Marshal(sl)
	if err != nil {
		return ""
	}
	return string(raw)
}

// payloadFromContainer rebuilds the deploy payload of a running replica from
// its labels and spec label. ok is false for containers without a usable spec
// label.
func payloadFromContainer(c docker.ContainerInfo) (agentprotocol.DeployPayload, bool) {
	var sl specLabel
	if err := json.Unmarshal([]byte(c.Labels[agentprotocol.LabelSpec]), &sl); err != nil || sl.Slug == "" {
		return agentprotocol.DeployPayload{}, false
	}
	p := agentprotocol.DeployPayload{
		ServiceID:        c.Labels[agentprotocol.LabelService],
		DeploymentID:     c.Labels[agentprotocol.LabelDeployment],
		ReleaseID:        c.Labels[agentprotocol.LabelRelease],
		EnvironmentID:    c.Labels[agentprotocol.LabelEnvironment],
		ServiceSlug:      sl.Slug,
		Strategy:         agentprotocol.StrategyRolling,
		Port:             sl.Port,
		Healthcheck:      sl.Healthcheck,
		StopGraceSeconds: sl.StopGraceSeconds,
		MemoryMB:         sl.MemoryMB,
		Docker:           sl.Docker,
		DockerPolicy:     sl.DockerPolicy,
		CPUs:             sl.CPUs,
		StartCommand:     c.Cmd,

		BlockEgress:         sl.BlockEgress,
		MeshAlias:           sl.MeshAlias,
		EnvHasEgressBlocked: sl.EnvHasEgressBlocked,
		SharedNetworks:      sl.SharedNetworks,
	}
	if p.ServiceID == "" || p.DeploymentID == "" || p.EnvironmentID == "" {
		return agentprotocol.DeployPayload{}, false
	}
	if sl.Process != "" { // one process of a multi-process deployment: the payload is that process alone
		p.Processes = []agentprotocol.ProcessSpec{{
			Name: sl.Process, Default: sl.ProcessDefault, Replicas: 1, Strategy: agentprotocol.StrategyRolling,
			Port: sl.Port, StartCommand: c.Cmd, Healthcheck: sl.Healthcheck, StopGraceSeconds: sl.StopGraceSeconds,
			Singleton: sl.Singleton, MemoryMB: sl.MemoryMB, CPUs: sl.CPUs,
		}}
	}
	return p, true
}

// startConvergence checks every desired service in the background: the control
// plane's stored replica count is authoritative, so when a scale_service
// command expired while the server was offline the agent catches up here.
func (e *Engine) startConvergence(ctx context.Context, entries map[string]desiredEntry) {
	ids := make([]string, 0, len(entries))
	for id, ent := range entries {
		if !ent.Stack && ent.Replicas >= 1 && len(ent.ContainerNames) > 0 && !e.repl.isFenced(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		ent := entries[id]
		e.bg.Add(1)
		go func() {
			defer e.bg.Done()
			e.convergeReplicas(ctx, id, ent)
		}()
	}
}

// scaleSuperseded reports whether ent, a desired entry from a reconcile frame,
// carries a replica count older than the scale the agent recorded in loc for
// the same deployment. Both must be stamped: a frame or command from a control
// plane without ScaledAt orders nothing.
func scaleSuperseded(loc, ent desiredEntry) bool {
	return loc.DeploymentID == ent.DeploymentID && !ent.ScaledAt.IsZero() && loc.ScaledAt.After(ent.ScaledAt)
}

// procSuperseded is scaleSuperseded for one process of ent: each process has
// its own count and its own stamp.
func procSuperseded(loc, ent desiredEntry, pr desiredProcess) bool {
	lp, ok := loc.proc(pr.Name)
	return ok && loc.DeploymentID == ent.DeploymentID && !pr.ScaledAt.IsZero() && lp.ScaledAt.After(pr.ScaledAt)
}

// localWins is what the agent acts on for a service of a reconcile frame: the
// frame's entry, except where a local deploy is newer (another deployment) or
// a scale that ran here after the control plane built the frame replaced the
// count the frame still carries. With processes that is decided per process.
func localWins(loc, ent desiredEntry) desiredEntry {
	if loc.DeploymentID != ent.DeploymentID {
		return loc
	}
	if len(ent.Processes) == 0 {
		if scaleSuperseded(loc, ent) {
			return loc
		}
		return ent
	}
	merged := ent
	merged.Processes = slices.Clone(ent.Processes)
	changed := false
	for i, pr := range merged.Processes {
		if procSuperseded(loc, ent, pr) {
			merged.Processes[i], _ = loc.proc(pr.Name)
			changed = true
		}
	}
	if !changed {
		return ent
	}
	merged.Routes = loc.Routes
	merged.mirrorDefault()
	return merged
}

// convergeReplicas makes the running replica count of each process of a
// service's desired deployment (a single-process service has one) equal the
// desired count through the scale_service code path: missing replicas are
// created and health-checked before they join the upstreams; extra ones leave
// the upstreams first, drain, then stop. A service with no running replica is
// left to the control plane (it redeploys on "missing").
func (e *Engine) convergeReplicas(ctx context.Context, serviceID string, ent desiredEntry) {
	if e.isInflight(serviceID) {
		return // a command is working on it; the next reconcile re-checks
	}
	unlock := e.lockService(serviceID)
	defer unlock()

	e.mu.Lock()
	loc, haveLoc := e.local[serviceID]
	e.mu.Unlock()
	if haveLoc && loc.DeploymentID != ent.DeploymentID {
		return // a newer deployment landed locally meanwhile
	}
	cs, err := e.containersOf(ctx, serviceID, false)
	if err != nil {
		e.log.Warn("converge: list containers", "service_id", serviceID, "err", err)
		return
	}
	acted := false
	for _, pr := range ent.procs() {
		if haveLoc && procSuperseded(loc, ent, pr) {
			// The count in ent is from a frame that was built (or arrived) before
			// a scale command that has run since, possibly while this goroutine
			// waited for the lock: acting on it would undo that scale. The
			// command's count is final until a newer frame replaces it.
			lp, _ := loc.proc(pr.Name)
			e.log.Info("converge: replica count superseded by a later scale, skipping",
				"service_id", serviceID, "process", pr.Name, "frame_replicas", pr.Replicas, "scaled_replicas", lp.Replicas)
			continue
		}
		acted = e.convergeProcess(ctx, serviceID, ent.DeploymentID, pr, cs) || acted
	}
	if acted {
		e.reportObserved(ctx)
	}
}

// convergeProcess scales one process to its desired count; it reports whether
// it changed anything.
func (e *Engine) convergeProcess(ctx context.Context, serviceID, deploymentID string, pr desiredProcess, cs []docker.ContainerInfo) bool {
	var live []docker.ContainerInfo
	for _, c := range cs {
		if c.Running && c.Labels[agentprotocol.LabelDeployment] == deploymentID && inProcess(c, pr.Name) {
			live = append(live, c)
		}
	}
	if len(live) == 0 || len(live) == pr.Replicas {
		return false
	}
	sortByReplica(live)
	base := live[0]
	p, ok := payloadFromContainer(base)
	if !ok {
		e.log.Warn("converge: replicas differ from desired but the container predates spec labels; redeploy to fix",
			"service_id", serviceID, "process", pr.Name, "running", len(live), "desired", pr.Replicas)
		return false
	}
	p.Replicas = pr.Replicas
	env := base.Env
	if env == nil {
		env = []string{}
	}
	d := &deploy{
		e: e, p: p, red: newRedactor(nil, nil), silent: true, live: true, cloneEnv: env, scaledAt: pr.ScaledAt,
		log: e.log.With("correlation_id", p.DeploymentID, "service_id", serviceID),
	}
	d.log.Info("converging replicas to the desired count", "process", pr.Name, "running", len(live), "desired", pr.Replicas)
	res := d.scale(ctx)
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		d.log.Warn("converge failed", "process", pr.Name, "outcome", res.Outcome, "message", strings.SplitN(res.Message, "\n", 2)[0])
	}
	return true
}

// dropDeadUpstreams removes upstreams whose container is not running from a
// route table, so a desired replica that is missing (and not yet restored)
// never receives traffic. A route that would lose every upstream is kept as is.
func (e *Engine) dropDeadUpstreams(ctx context.Context, table []agentprotocol.Route) []agentprotocol.Route {
	cs, err := e.cfg.Docker.ContainerList(ctx, docker.ListFilter{
		Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
	})
	if err != nil {
		return table
	}
	running := map[string]bool{}
	for _, c := range cs {
		if c.Running {
			running[c.Name] = true
		}
	}
	out := make([]agentprotocol.Route, len(table))
	for i, r := range table {
		out[i] = r
		if len(r.Upstreams) < 2 {
			continue
		}
		var keep []string
		for _, u := range r.Upstreams {
			host := u
			if j := strings.LastIndexByte(u, ':'); j > 0 {
				host = u[:j]
			}
			// Only managed replica containers can be "dead"; addresses of
			// external and tcp/udp upstreams are never dropped.
			if running[host] || !strings.HasPrefix(host, agentprotocol.ContainerPrefix) {
				keep = append(keep, u)
			}
		}
		if len(keep) > 0 && len(keep) < len(r.Upstreams) {
			out[i].Upstreams = keep
		}
	}
	return out
}
