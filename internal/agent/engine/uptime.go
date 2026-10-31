package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/uptime"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Uptime probing. Every UptimeInterval (default agentprotocol.UptimeInterval) the engine probes each
// managed service it runs the way the service's health check says and hands
// the outcome to Config.Uptime, which keeps it until the control plane has
// stored it (internal/agent/uptime).
//
//   - An app service is judged per wanted replica (Desired.ContainerNames): an
//     http or tcp check is probed over the environment network (container IP
//     first, then its DNS name), docker and exec checks read Docker's health
//     status, none (and a check without a port) means running and not
//     restarting. A replica whose container exited and is not restarting was
//     stopped on purpose; a service whose replicas are all stopped is paused.
//   - A stack is judged per member container: running and healthy, or finished
//     with exit code 0. Members all stopped (exit 0, SIGTERM or SIGKILL) pause it.
//   - A route service has no container: each upstream of its route in the
//     current proxy table is probed (an HTTP GET of the route's health path
//     when it has one, else a TCP connect). UDP routes cannot be probed and
//     yield no result.
//
// A containerized agent reaches the replicas only through the environment
// network. The cycle therefore holds probeAccess for every network it needs
// from the first probe to the last, so the network sweeper (which leaves
// networks the agent holds no reference on) cannot detach the agent mid-cycle.

// UptimeFirstProbe is the delay before the first probe cycle after the agent
// starts: long enough for the first reconcile to have described the services.
const UptimeFirstProbe = 15 * time.Second

// uptimeInterval is the probe period: Config.UptimeInterval, else the protocol default.
func (e *Engine) uptimeInterval() time.Duration {
	if e.cfg.UptimeInterval > 0 {
		return e.cfg.UptimeInterval
	}
	return agentprotocol.UptimeInterval
}

const (
	// uptimeParallelism bounds the probes of one cycle running at once.
	uptimeParallelism = 16
	// stackStartingGrace is how long a stack member whose compose health check
	// still reports "starting" counts as healthy after it started (the compose
	// start period is not known to the agent).
	stackStartingGrace = 2 * time.Minute
)

// agentUptime holds the services the control plane switched monitoring off for.
type agentUptime struct {
	mu       sync.Mutex
	disabled map[string]struct{}
}

// apply installs the settings of a reconcile frame. nil (an older control
// plane) means every service is probed.
func (u *agentUptime) apply(s *agentprotocol.UptimeSettings) {
	var off map[string]struct{}
	if s != nil && len(s.Disabled) > 0 {
		off = make(map[string]struct{}, len(s.Disabled))
		for _, id := range s.Disabled {
			off[id] = struct{}{}
		}
	}
	u.mu.Lock()
	u.disabled = off
	u.mu.Unlock()
}

func (u *agentUptime) isDisabled(serviceID string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, off := u.disabled[serviceID]
	return off
}

// probeUptime runs one probe cycle and records its results.
func (e *Engine) probeUptime(ctx context.Context) {
	results := e.uptimeCycle(ctx, time.Now().UTC())
	if ctx.Err() != nil {
		return // the agent is stopping: probes that failed on shutdown are not outages
	}
	e.cfg.Uptime.Record(results)
}

// uptimeReplica is one replica (container, stack member or route upstream) of
// a service: already judged (done) or still to be probed (probe).
type uptimeReplica struct {
	done  uptime.Replica
	probe func(ctx context.Context) uptime.Replica
}

// uptimeJob is the probe of one service.
type uptimeJob struct {
	serviceID string
	reps      []uptimeReplica
	// networks are the networks the agent must be able to reach for probe.
	networks []string
}

// uptimeCycle probes every monitored service and returns one result per
// service, at time at.
func (e *Engine) uptimeCycle(ctx context.Context, at time.Time) []agentprotocol.ProbeResult {
	jobs := e.uptimeJobs(ctx)
	if len(jobs) == 0 {
		return nil
	}
	var nets []string
	seen := map[string]bool{}
	for _, j := range jobs {
		for _, n := range j.networks {
			if !seen[n] {
				seen[n] = true
				nets = append(nets, n)
			}
		}
	}
	sort.Strings(nets)
	release := e.probeAccess(ctx, nets...)
	defer release()

	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, uptimeParallelism)
	)
	for ji := range jobs {
		for ri := range jobs[ji].reps {
			r := &jobs[ji].reps[ri]
			if r.probe == nil {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				r.done = r.probe(ctx)
			}()
		}
	}
	wg.Wait()

	out := make([]agentprotocol.ProbeResult, 0, len(jobs))
	for _, j := range jobs {
		reps := make([]uptime.Replica, len(j.reps))
		for i, r := range j.reps {
			reps[i] = r.done
		}
		out = append(out, uptime.Judge(j.serviceID, at, reps))
	}
	return out
}

// uptimeJobs describes the probes of this cycle: every desired service and
// every route service of the current proxy table, except the services
// monitoring is switched off for. Services come in ID order.
func (e *Engine) uptimeJobs(ctx context.Context) []uptimeJob {
	desired := e.desiredSnapshot()
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var jobs []uptimeJob
	for _, id := range ids {
		if e.up.isDisabled(id) {
			continue
		}
		ent := desired[id]
		switch {
		case e.repl.isFenced(id):
			jobs = append(jobs, uptimeJob{serviceID: id}) // a fenced database is stopped on purpose
		case ent.Stack:
			jobs = append(jobs, e.stackUptimeJob(ctx, id, ent))
		default:
			jobs = append(jobs, e.appUptimeJob(ctx, id, ent))
		}
	}
	return append(jobs, e.routeUptimeJobs(desired)...)
}

// appUptimeJob inspects the wanted replicas of an app service: those of every
// process of a multi-process service. Any failing replica of any process counts
// toward the service, and its failure names the process.
func (e *Engine) appUptimeJob(ctx context.Context, id string, ent desiredEntry) uptimeJob {
	job := uptimeJob{serviceID: id}
	for _, pr := range ent.procs() {
		label := pr.Name // empty for a single-process service
		for _, name := range pr.ContainerNames {
			info, err := e.cfg.Docker.ContainerInspect(ctx, name)
			switch {
			case errors.Is(err, docker.ErrNotFound):
				job.reps = append(job.reps, namedReplica(label, uptimeReplica{done: uptime.Replica{Error: "not running: container " + name + " does not exist"}}))
				continue
			case err != nil:
				job.reps = append(job.reps, namedReplica(label, uptimeReplica{done: uptime.Replica{Error: "inspect container " + name + ": " + err.Error()}}))
				continue
			}
			rep, network := appReplicaProbe(name, info)
			if network != "" {
				job.networks = append(job.networks, network)
			}
			job.reps = append(job.reps, namedReplica(label, rep))
		}
	}
	return job
}

// namedReplica prefixes the failure text of a replica with its process.
func namedReplica(process string, r uptimeReplica) uptimeReplica {
	if process == "" {
		return r
	}
	if r.done.Error != "" {
		r.done.Error = process + ": " + r.done.Error
	}
	if probe := r.probe; probe != nil {
		r.probe = func(ctx context.Context) uptime.Replica {
			res := probe(ctx)
			if res.Error != "" {
				res.Error = process + ": " + res.Error
			}
			return res
		}
	}
	return r
}

// appReplicaProbe judges the state of one replica's container and, when it
// runs, returns the probe of its health check plus the network that probe
// needs ("" when none).
func appReplicaProbe(name string, info docker.ContainerInfo) (uptimeReplica, string) {
	switch {
	case info.Restarting:
		return uptimeReplica{done: uptime.Replica{Error: "not running: container " + name + " is restarting (crash loop)"}}, ""
	case !info.Running && info.State == "exited":
		return uptimeReplica{done: uptime.Replica{Stopped: true, Error: "stopped"}}, ""
	case !info.Running:
		return uptimeReplica{done: uptime.Replica{Error: fmt.Sprintf("not running: container %s is %s", name, info.State)}}, ""
	}
	var sl specLabel
	_ = json.Unmarshal([]byte(info.Labels[agentprotocol.LabelSpec]), &sl) // a container without a spec label: running is the check
	hc := sl.Healthcheck
	switch hc.Type {
	case health.TypeDocker, health.TypeExec:
		since := time.Since(info.StartedAt)
		start := time.Duration(hc.StartPeriodSeconds) * time.Second
		return uptimeReplica{done: dockerHealthReplica(info.Health, since, start)}, ""
	case health.TypeHTTP, health.TypeTCP:
		network := probeNetworkOf(info)
		addrs := append(probeIPs(info, network), name) // the IP first, then the DNS name (Docker Desktop cannot route to the IP)
		return uptimeReplica{probe: func(ctx context.Context) uptime.Replica {
			_, took, err := health.ProbeOnce(ctx, hc, sl.Port, addrs)
			switch {
			case errors.Is(err, health.ErrNoNetworkProbe):
				return uptime.Replica{Passed: true} // no port to probe: running is the check
			case err != nil:
				return uptime.Replica{Error: err.Error()}
			}
			return uptime.Replica{Passed: true, Latency: took}
		}}, network
	}
	return uptimeReplica{done: uptime.Replica{Passed: true}}, ""
}

func dockerHealthReplica(status string, sinceStart, startPeriod time.Duration) uptime.Replica {
	pass, why := uptime.DockerHealth(status, sinceStart, startPeriod)
	return uptime.Replica{Passed: pass, Error: why}
}

// probeNetworkOf is the network the agent reaches a container through: the
// primary network of its network plan, else (a container without a spec
// label) the first managed network it sits on.
func probeNetworkOf(info docker.ContainerInfo) string {
	if p, ok := payloadFromContainer(info); ok {
		return planOfDeploy(p).ProbeNetwork()
	}
	var names []string
	for n := range info.Networks {
		if isManagedNetwork(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// stackUptimeJob judges every member container of a stack's deployment.
func (e *Engine) stackUptimeJob(ctx context.Context, id string, ent desiredEntry) uptimeJob {
	job := uptimeJob{serviceID: id}
	conts, err := e.stackContainers(ctx, id, ent.DeploymentID)
	switch {
	case err != nil:
		job.reps = []uptimeReplica{{done: uptime.Replica{Error: "list stack containers: " + err.Error()}}}
	case len(conts) == 0:
		job.reps = []uptimeReplica{{done: uptime.Replica{Error: "not running: the stack has no containers"}}}
	default:
		for _, c := range conts {
			job.reps = append(job.reps, uptimeReplica{done: stackMemberReplica(c, time.Now())})
		}
	}
	return job
}

// stackMemberReplica judges one member container of a stack.
func stackMemberReplica(c docker.ContainerInfo, now time.Time) uptime.Replica {
	label := c.Labels[agentprotocol.LabelComponent]
	if label == "" {
		label = c.Name
	}
	switch {
	case c.Restarting:
		return uptime.Replica{Error: label + " is restarting (crash loop)"}
	case c.Running && c.Health == "starting":
		if now.Sub(c.StartedAt) < stackStartingGrace {
			return uptime.Replica{Passed: true}
		}
		return uptime.Replica{Error: "unhealthy: " + label + " health check still starting"}
	case c.Running:
		pass, why := uptime.DockerHealth(c.Health, 0, 0)
		if !pass {
			why = label + ": " + why
		}
		return uptime.Replica{Passed: pass, Error: why}
	case c.State == "exited" && c.ExitCode == 0:
		return uptime.Replica{Passed: true, Stopped: true} // completed (or stopped cleanly)
	case c.State == "exited" && (c.ExitCode == 137 || c.ExitCode == 143):
		return uptime.Replica{Stopped: true, Error: label + " is stopped"} // SIGKILL or SIGTERM: `compose stop`
	case c.State == "exited":
		return uptime.Replica{Error: fmt.Sprintf("%s exited with code %d", label, c.ExitCode)}
	}
	return uptime.Replica{Error: fmt.Sprintf("not running: %s is %s", label, c.State)}
}

// routeUptimeJobs builds the probes of route services: the routes of the
// current proxy table that belong to no app service or stack of this server.
func (e *Engine) routeUptimeJobs(desired map[string]desiredEntry) []uptimeJob {
	byService := map[string][]agentprotocol.Route{}
	for _, r := range e.cfg.Proxy.Current() {
		if r.Stack != "" || r.Protocol == agentprotocol.ProtoUDP {
			continue
		}
		if _, app := desired[r.ServiceID]; app || e.isInflight(r.ServiceID) || e.up.isDisabled(r.ServiceID) {
			continue
		}
		byService[r.ServiceID] = append(byService[r.ServiceID], r)
	}
	pol := agentprotocol.DefaultUpstreamPolicy()
	if pp, ok := e.cfg.Proxy.(interface {
		UpstreamPolicy() agentprotocol.UpstreamPolicy
	}); ok {
		pol = pp.UpstreamPolicy()
	}
	ids := make([]string, 0, len(byService))
	for id := range byService {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	jobs := make([]uptimeJob, 0, len(ids))
	for _, id := range ids {
		job := uptimeJob{serviceID: id}
		for _, r := range byService[id] {
			job.networks = append(job.networks, r.Networks...)
			for _, up := range r.Upstreams {
				job.reps = append(job.reps, uptimeReplica{probe: routeUpstreamProbe(r, up, pol)})
			}
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// routeUpstreamProbe probes one upstream ("host:port") of a route: an HTTP GET
// of the route's health path when it has one (over TLS for an https route),
// else a TCP connect. A name is resolved and held to the server's upstream
// policy first (proxy.VetProbeHost) and only the vetted addresses are dialled:
// the probe runs in the agent's network namespace, so it must not reach what
// the proxy refuses.
func routeUpstreamProbe(r agentprotocol.Route, upstream string, pol agentprotocol.UpstreamPolicy) func(ctx context.Context) uptime.Replica {
	return func(ctx context.Context) uptime.Replica {
		host, portStr, err := net.SplitHostPort(upstream)
		port, perr := strconv.Atoi(portStr)
		if err != nil || perr != nil {
			return uptime.Replica{Error: "invalid upstream " + upstream}
		}
		addrs, err := proxy.VetProbeHost(ctx, r, host, pol)
		if err != nil {
			return uptime.Replica{Error: err.Error()}
		}
		p := health.Probe{Spec: agentprotocol.HealthcheckSpec{Type: health.TypeTCP, Port: port}, Addrs: addrs}
		if !r.IsL4() && r.HealthPath != "" {
			p.Spec = agentprotocol.HealthcheckSpec{Type: health.TypeHTTP, Path: r.HealthPath, Port: port}
			if r.Scheme == "https" {
				p.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
				if len(addrs) != 1 || addrs[0] != host {
					p.TLS.ServerName = host // the name was replaced by its addresses
				}
				if t := r.Transport; t != nil {
					if t.ServerName != "" {
						p.TLS.ServerName = t.ServerName
					}
					p.TLS.InsecureSkipVerify = t.InsecureSkipVerify //nolint:gosec // the route's own opt-in, same as the proxy's transport
				}
			}
		}
		_, took, err := p.Once(ctx)
		if err != nil {
			return uptime.Replica{Error: err.Error()}
		}
		return uptime.Replica{Passed: true, Latency: took}
	}
}
