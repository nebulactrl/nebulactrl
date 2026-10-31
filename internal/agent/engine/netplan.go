package engine

import (
	"context"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Network isolation. Containers of services, tasks and tailnet nodes never join
// the shared edge network (only Traefik, the pages server and the tunnel
// connector do). A service joins its environment's network under its slug; a
// service that blocks outbound internet joins only the environment's internal
// network; when another service of the environment blocks egress every service
// also joins the internal network (same alias) so it can still reach it.
// Traefik joins the networks named by Route.Networks.

// netPlan is the network membership of one service's containers.
type netPlan struct {
	// Alias is the DNS alias on the environment networks (the service slug,
	// or "<process>.<slug>" for a named process); empty for one-shot task
	// containers.
	Alias string
	// Process is the name of a NAMED process; "" for the service's only or
	// default process. A named process never claims the service's own alias,
	// which belongs to the default process.
	Process             string
	EnvironmentID       string
	BlockEgress         bool
	EnvHasEgressBlocked bool
	Shared              []agentprotocol.SharedNetworkAttachment
	// MeshAlias (nc-svc-<serviceId8>) is an extra alias on every network the
	// service's containers join: the service is exposed on the mesh and the
	// server node forwards to it. Empty for services that are not exposed, for
	// tasks and for named processes (the mesh targets the default process).
	MeshAlias string
}

// planOfDeploy is the plan of a payload. A payload that carries exactly one
// process is the projection of one process of a multi-process service (see
// processView); its containers get that process' alias.
func planOfDeploy(p agentprotocol.DeployPayload) netPlan {
	n := netPlan{
		Alias: p.ServiceSlug, EnvironmentID: p.EnvironmentID, BlockEgress: p.BlockEgress,
		EnvHasEgressBlocked: p.EnvHasEgressBlocked, Shared: p.SharedNetworks, MeshAlias: p.MeshAlias,
	}
	if len(p.Processes) == 1 {
		pr := p.Processes[0]
		n.Alias = agentprotocol.ProcessAlias(p.ServiceSlug, pr.Name, pr.Default)
		if !pr.Default {
			n.Process, n.MeshAlias = pr.Name, ""
		}
	}
	return n
}

func planOfTask(p agentprotocol.RunTaskPayload) netPlan {
	return netPlan{
		EnvironmentID: p.EnvironmentID, BlockEgress: p.BlockEgress,
		EnvHasEgressBlocked: p.EnvHasEgressBlocked, Shared: p.SharedNetworks,
	}
}

func aliases(a string) []string {
	if a == "" {
		return nil
	}
	return []string{a}
}

// aliasesOf is the alias list of an attachment: the given alias plus the mesh
// alias.
func (n netPlan) aliasesOf(a string) []string {
	out := aliases(a)
	if n.MeshAlias != "" {
		out = append(out, n.MeshAlias)
	}
	return out
}

// envNet is the environment network, intNet its internal sibling.
func (n netPlan) envNet() string { return agentprotocol.EnvNetwork(n.EnvironmentID) }
func (n netPlan) intNet() string { return agentprotocol.EnvInternalNetwork(n.EnvironmentID) }

// ignoredShared lists the shared networks that are not joined because the
// service blocks egress: a shared bridge would give it a route out again.
func (n netPlan) ignoredShared() []agentprotocol.SharedNetworkAttachment {
	if n.BlockEgress {
		return n.Shared
	}
	return nil
}

// Attachments is the container's network list; the first entry is primary.
func (n netPlan) Attachments() []docker.NetworkAttachment {
	a := n.aliasesOf(n.Alias)
	switch {
	case n.BlockEgress:
		return []docker.NetworkAttachment{{Network: n.intNet(), Aliases: a}}
	case n.EnvHasEgressBlocked:
		out := []docker.NetworkAttachment{{Network: n.envNet(), Aliases: a}, {Network: n.intNet(), Aliases: a}}
		return n.withShared(out)
	default:
		return n.withShared([]docker.NetworkAttachment{{Network: n.envNet(), Aliases: a}})
	}
}

func (n netPlan) withShared(out []docker.NetworkAttachment) []docker.NetworkAttachment {
	for _, s := range n.Shared {
		if s.Name == "" {
			continue
		}
		alias := s.Alias
		switch {
		case alias == "":
			alias = n.Alias
		case n.Process != "": // the shared alias names the default process; a named one sits beside it
			alias = n.Process + "." + alias
		}
		out = append(out, docker.NetworkAttachment{Network: s.Name, Aliases: n.aliasesOf(alias)})
	}
	return out
}

// RouteNetworks are the networks Traefik needs to reach the service's
// upstreams.
func (n netPlan) RouteNetworks() []string {
	if n.BlockEgress {
		return []string{n.intNet()}
	}
	return []string{n.envNet()}
}

// ProbeNetwork is the network the agent reaches the container through for
// health probes: the primary one, which is also the network Traefik routes on.
func (n netPlan) ProbeNetwork() string { return n.Attachments()[0].Network }

// ensure creates the networks of the plan that do not exist yet, each with a
// subnet out of the agent's pool (see ensureNetwork).
func (n netPlan) ensure(ctx context.Context, ensureNetwork networkEnsurer) error {
	env := map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelEnvironment: n.EnvironmentID}
	for _, a := range n.Attachments() {
		var err error
		switch {
		case a.Network == n.intNet():
			err = ensureNetwork(ctx, a.Network, env, true)
		case a.Network == n.envNet():
			err = ensureNetwork(ctx, a.Network, env, false)
		default: // team shared network
			err = ensureNetwork(ctx, a.Network, map[string]string{agentprotocol.LabelManaged: "true"}, false)
		}
		if err != nil {
			return fmt.Errorf("ensure network %s: %w", a.Network, err)
		}
	}
	return nil
}

// networkEnsurer creates a managed network when it does not exist.
type networkEnsurer func(ctx context.Context, name string, labels map[string]string, internal bool) error

// ensureNetwork creates the managed network name with a subnet from the agent's
// address pool. A network that exists is reused whatever its subnet.
func (e *Engine) ensureNetwork(ctx context.Context, name string, labels map[string]string, internal bool) error {
	return e.cfg.Networks.Ensure(ctx, e.cfg.Docker, name, labels, internal)
}

// probeIPs lists the container's IPs on the given networks, in order. The
// agent never probes through the edge network: app containers are not on it.
func probeIPs(info docker.ContainerInfo, networks ...string) []string {
	var out []string
	for _, name := range networks {
		if ep, ok := info.Networks[name]; ok && ep.IP != "" {
			out = append(out, ep.IP)
		}
	}
	return out
}
