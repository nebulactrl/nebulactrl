package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// An agent on the bare host reaches the bridge IPs of containers directly. An
// agent running in a container is on the networks of its own compose project
// only, so before probing a container it attaches itself to the container's
// network and leaves it again once nothing needs it (a network with a lingering
// member cannot be removed by the orphan sweep either).

// agentNet tracks the agent's own container and the networks it joined for
// probing.
type agentNet struct {
	mu       sync.Mutex
	resolved bool
	self     string // container ID; "" when the agent is not containerized
	refs     map[string]int
}

// selfContainer resolves (once) the agent's own container: Config.AgentContainer
// (NEBULACTRL_AGENT_CONTAINER) when set, else the container whose ID starts with
// the hostname. A hostname that matches no container means the agent runs on
// the bare host.
func (e *Engine) selfContainer(ctx context.Context) string {
	e.an.mu.Lock()
	defer e.an.mu.Unlock()
	if e.an.resolved {
		return e.an.self
	}
	candidate, explicit := e.cfg.AgentContainer, e.cfg.AgentContainer != ""
	if !explicit {
		candidate = e.cfg.Hostname
	}
	if candidate == "" {
		e.an.resolved = true
		return ""
	}
	info, err := e.cfg.Docker.ContainerInspect(ctx, candidate)
	switch {
	case err == nil && (explicit || strings.HasPrefix(info.ID, candidate)):
		e.an.self = info.ID
		e.log.Info("agent runs in a container; it attaches to networks it probes", "container", info.Name)
	case err == nil, errors.Is(err, docker.ErrNotFound):
		if explicit {
			e.log.Warn("NEBULACTRL_AGENT_CONTAINER matches no container; treating the agent as running on the host", "value", candidate)
		}
	default:
		e.log.Warn("could not detect whether the agent runs in a container", "err", err)
		return "" // unresolved: try again next time
	}
	e.an.resolved = true
	return e.an.self
}

// probeAccess makes the container network reachable from the agent for the
// duration of a probe and returns the release function. On the bare host (or
// when the agent is already on the network) it does nothing.
func (e *Engine) probeAccess(ctx context.Context, networks ...string) (release func()) {
	self := e.selfContainer(ctx)
	if self == "" || len(networks) == 0 {
		return func() {}
	}
	eng := e.cfg.Docker
	e.an.mu.Lock()
	defer e.an.mu.Unlock()
	if e.an.refs == nil {
		e.an.refs = map[string]int{}
	}
	info, err := eng.ContainerInspect(ctx, self)
	if err != nil {
		e.log.Warn("agent: inspect own container", "err", err)
		return func() {}
	}
	var held []string
	for _, n := range networks {
		if e.an.refs[n] == 0 {
			if _, joined := info.Networks[n]; !joined {
				if err := eng.NetworkConnect(ctx, n, self, nil); err != nil {
					e.log.Warn("agent: cannot join network for health probes; falling back to container DNS names", "network", n, "err", err)
					continue
				}
			}
		}
		e.an.refs[n]++
		held = append(held, n)
	}
	return func() {
		cctx, cancel := detached(ctx, 30*time.Second)
		defer cancel()
		e.an.mu.Lock()
		defer e.an.mu.Unlock()
		for _, n := range held {
			if e.an.refs[n]--; e.an.refs[n] > 0 {
				continue
			}
			delete(e.an.refs, n)
			if err := eng.NetworkDisconnect(cctx, n, self); err != nil {
				e.log.Warn("agent: leave network", "network", n, "err", err)
			}
		}
	}
}

// leaveIdleNetworks disconnects the agent's own container from every managed
// environment or shared network it still sits on without holding a probe
// reference (a previous run crashed before releasing).
func (e *Engine) leaveIdleNetworks(ctx context.Context) {
	self := e.selfContainer(ctx)
	if self == "" {
		return
	}
	info, err := e.cfg.Docker.ContainerInspect(ctx, self)
	if err != nil {
		return
	}
	e.an.mu.Lock()
	defer e.an.mu.Unlock()
	for n := range info.Networks {
		if !isManagedNetwork(n) || e.an.refs[n] > 0 {
			continue
		}
		if err := e.cfg.Docker.NetworkDisconnect(ctx, n, self); err != nil {
			e.log.Warn("agent: leave idle network", "network", n, "err", err)
		}
	}
}

// isManagedNetwork reports whether name is an environment, internal or team
// shared network, under its current name or the legacy nc-* one (which only
// exists as leftovers to sweep).
func isManagedNetwork(name string) bool {
	for _, prefix := range []string{
		agentprotocol.EnvNetworkPrefix, agentprotocol.SharedNetworkPrefix,
		agentprotocol.LegacyEnvNetworkPrefix, agentprotocol.LegacySharedNetworkPrefix,
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// NetworkSweepInterval is how often unused environment and shared networks are
// removed (they are also swept on every reconcile). Every network holds a
// subnet of the agent's address pool, so empty ones are released promptly.
const NetworkSweepInterval = time.Minute

// sweepNetworks removes managed environment, internal and shared networks that
// have no containers (a service, environment or shared network was deleted, or
// a deploy that created the network failed). A network younger than
// NetworkMinAge (60 s by default) is left alone: a deploy creates the network
// a moment before its first container. The agent leaves networks it only
// joined to probe first, and Traefik leaves them when its route table no
// longer names them.
func (e *Engine) sweepNetworks(ctx context.Context) error {
	e.leaveIdleNetworks(ctx)
	eng := e.cfg.Docker
	nets, err := eng.NetworkList(ctx, "", map[string]string{agentprotocol.LabelManaged: "true"})
	if err != nil {
		return fmt.Errorf("sweep: list networks: %w", err)
	}
	for _, n := range nets {
		if !isManagedNetwork(n.Name) || len(n.Containers) > 0 {
			continue
		}
		if !n.Created.IsZero() && time.Since(n.Created) < e.cfg.NetworkMinAge {
			continue
		}
		if err := eng.NetworkRemove(ctx, n.Name); err != nil && !errors.Is(err, docker.ErrNotFound) {
			e.log.Warn("sweep: remove network", "network", n.Name, "err", err)
			continue
		}
		e.log.Info("removed unused network", "network", n.Name)
	}
	return nil
}

// attachDesiredNetworks connects the running replicas of the desired services to
// the networks the control plane says they belong on and are not on yet (for
// example the internal network after another service of the environment began
// blocking egress). It never disconnects: leaving a network needs a redeploy.
func (e *Engine) attachDesiredNetworks(ctx context.Context, services []agentprotocol.DesiredService, effective map[string]desiredEntry) {
	eng := e.cfg.Docker
	for _, ds := range services {
		if ent := effective[ds.ServiceID]; ent.Stack || ent.DeploymentID != ds.DeploymentID || e.isInflight(ds.ServiceID) {
			continue
		}
		cs, err := e.containersOf(ctx, ds.ServiceID, false)
		if err != nil {
			e.log.Warn("reconcile: list containers for network attachment", "service_id", ds.ServiceID, "err", err)
			continue
		}
		ensured := false
		for _, c := range cs {
			if !c.Running || c.Labels[agentprotocol.LabelDeployment] != ds.DeploymentID {
				continue
			}
			p, ok := payloadFromContainer(c)
			if !ok {
				continue
			}
			p.BlockEgress, p.EnvHasEgressBlocked, p.SharedNetworks = ds.BlockEgress, ds.EnvHasEgressBlocked, ds.SharedNetworks
			plan := planOfDeploy(p)
			for _, a := range plan.Attachments() {
				if _, on := c.Networks[a.Network]; on {
					continue
				}
				if !ensured {
					if err := plan.ensure(ctx, e.ensureNetwork); err != nil {
						e.log.Warn("reconcile: ensure networks", "service_id", ds.ServiceID, "err", err)
						break
					}
					ensured = true
				}
				if err := eng.NetworkConnect(ctx, a.Network, c.ID, a.Aliases); err != nil {
					e.log.Warn("reconcile: attach replica to network", "container", c.Name, "network", a.Network, "err", err)
					continue
				}
				e.log.Info("attached replica to network", "container", c.Name, "network", a.Network)
			}
		}
	}
}
