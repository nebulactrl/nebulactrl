// Package decommission removes everything NebulaCtrl put on a Docker host:
// the managed containers (services, databases, helpers, the proxy and the
// other infrastructure containers), the networks and the volumes. It is what
// the decommission command does before the agent deletes itself.
//
// A resource is NebulaCtrl's when it carries a label whose key starts with
// "nebulactrl." (nebulactrl.managed on everything the agent creates), or has
// one of the fixed names the agent gives its own infrastructure (the proxy and
// its siblings, the edge and mesh networks, the certificate and tailnet state
// volumes, the nc-vol-* data volumes). Docker, images, build cache and every
// container, network and volume that is not NebulaCtrl's stay as they are.
package decommission

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// LabelPrefix starts the key of every label NebulaCtrl puts on a resource.
const LabelPrefix = "nebulactrl."

// ComposeProjectLabel is the label Docker Compose puts on a stack's resources.
const ComposeProjectLabel = "com.docker.compose.project"

// DefaultGrace is how long a running container gets to stop before it is killed.
const DefaultGrace = 30 * time.Second

// parallel is how many containers are stopped at once.
const parallel = 8

// Docker is the part of the daemon a decommission uses. *docker.SDKEngine
// implements it; tests use a fake.
type Docker interface {
	ContainerList(ctx context.Context, f docker.ListFilter) ([]docker.ContainerInfo, error)
	ContainerStop(ctx context.Context, id string, timeout *time.Duration) error
	ContainerRemove(ctx context.Context, id string, force, removeVolumes bool) error
	NetworkList(ctx context.Context, prefix string, labels map[string]string) ([]docker.NetworkInfo, error)
	NetworkDisconnect(ctx context.Context, network, container string) error
	NetworkRemove(ctx context.Context, name string) error
	VolumeList(ctx context.Context) ([]docker.VolumeInfo, error)
	RemoveVolume(ctx context.Context, name string, force bool) error
}

// Config wires a Runner.
type Config struct {
	Docker Docker
	Logger *slog.Logger
	// Self is the ID of the container the agent itself runs in ("" when it runs
	// on the host). It is never removed, and it leaves the networks that are.
	Self string
	// Grace is the stop timeout of a running container (default DefaultGrace).
	Grace time.Duration
	// IsManagedNetwork reports the environment and shared networks by name, so
	// one that lost its label is still removed. nil: labels only.
	IsManagedNetwork func(name string) bool
	// ComposeDown removes a Compose project's containers, networks and volumes
	// (`docker compose down --volumes`). nil: the stack's containers are removed
	// one by one and its volumes and networks stay.
	ComposeDown func(ctx context.Context, project string) error
}

// Run decommissions the host. It returns the counts of what was removed and
// every resource it could not remove; it never stops at the first failure.
func Run(ctx context.Context, cfg Config) (agentprotocol.DecommissionResult, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}
	r := &runner{cfg: cfg, log: cfg.Logger}
	return r.run(ctx)
}

type runner struct {
	cfg Config
	log *slog.Logger
	res agentprotocol.DecommissionResult
	mu  sync.Mutex
}

func (r *runner) fail(kind, name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.Warn("decommission: could not remove a resource", "kind", kind, "name", name, "err", err)
	r.res.Failed = append(r.res.Failed, agentprotocol.DecommissionStep{Kind: kind, Name: name, Error: err.Error()})
}

// infraContainers are the fixed names of the agent's own infrastructure
// containers (the proxy and its siblings).
var infraContainers = map[string]bool{
	agentprotocol.ProxyContainer:     true,
	agentprotocol.PagesContainer:     true,
	agentprotocol.PagesContainerAlt:  true,
	agentprotocol.TunnelContainer:    true,
	agentprotocol.TailscaleContainer: true,
}

// infraNetworks and infraVolumes are the fixed names of the agent's own
// networks and volumes.
var (
	infraNetworks = map[string]bool{agentprotocol.EdgeNetwork: true, agentprotocol.MeshNetwork: true}
	infraVolumes  = map[string]bool{agentprotocol.ProxyACMEVolume: true, agentprotocol.TailscaleStateVolume: true}
)

// ours reports whether a label set carries a NebulaCtrl label.
func ours(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, LabelPrefix) {
			return true
		}
	}
	return false
}

// isSelf reports whether id (full or short) is the agent's own container.
func (r *runner) isSelf(id string) bool {
	return r.cfg.Self != "" && id != "" && (id == r.cfg.Self || strings.HasPrefix(r.cfg.Self, id) || strings.HasPrefix(id, r.cfg.Self))
}

func (r *runner) run(ctx context.Context) (agentprotocol.DecommissionResult, error) {
	all, err := r.cfg.Docker.ContainerList(ctx, docker.ListFilter{All: true})
	if err != nil {
		return r.res, fmt.Errorf("list containers: %w", err)
	}
	var mine []docker.ContainerInfo
	projects := map[string]bool{}
	for _, c := range all {
		if r.isSelf(c.ID) {
			continue
		}
		if !ours(c.Labels) && !infraContainers[strings.TrimPrefix(c.Name, "/")] {
			continue
		}
		mine = append(mine, c)
		// Only a stack NebulaCtrl deployed: its containers carry our labels.
		if p := c.Labels[ComposeProjectLabel]; p != "" && ours(c.Labels) {
			projects[p] = true
		}
	}

	// Compose stacks first: `down --volumes` takes the stack's own networks and
	// volumes too, which carry no NebulaCtrl label.
	if r.cfg.ComposeDown != nil {
		names := make([]string, 0, len(projects))
		for p := range projects {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			if err := r.cfg.ComposeDown(ctx, p); err != nil {
				r.fail("compose", p, err)
			}
		}
	}

	r.removeContainers(ctx, mine)
	r.removeNetworks(ctx)
	r.removeVolumes(ctx)

	switch {
	case len(r.res.Failed) > 0:
		r.res.Message = fmt.Sprintf("Removed %d container(s), %d network(s) and %d volume(s); %d resource(s) could not be removed.",
			r.res.Containers, r.res.Networks, r.res.Volumes, len(r.res.Failed))
	default:
		r.res.Message = fmt.Sprintf("Removed %d container(s), %d network(s) and %d volume(s).", r.res.Containers, r.res.Networks, r.res.Volumes)
	}
	return r.res, ctx.Err()
}

func (r *runner) removeContainers(ctx context.Context, cs []docker.ContainerInfo) {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if c.Running || c.Restarting || c.State == "paused" {
				grace := r.cfg.Grace
				if err := r.cfg.Docker.ContainerStop(ctx, c.ID, &grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
					r.log.Warn("decommission: stop failed, removing by force", "container", c.Name, "err", err)
				}
			}
			// -v: the anonymous volumes of a NebulaCtrl container go with it.
			err := r.cfg.Docker.ContainerRemove(ctx, c.ID, true, true)
			switch {
			case err == nil, errors.Is(err, docker.ErrNotFound):
				r.mu.Lock()
				r.res.Containers++
				r.mu.Unlock()
			default:
				r.fail("container", c.Name, err)
			}
		}()
	}
	wg.Wait()
}

func (r *runner) removeNetworks(ctx context.Context) {
	nets, err := r.cfg.Docker.NetworkList(ctx, "", nil)
	if err != nil {
		r.fail("network", "(list)", err)
		return
	}
	for _, n := range nets {
		if !ours(n.Labels) && !infraNetworks[n.Name] && !(r.cfg.IsManagedNetwork != nil && r.cfg.IsManagedNetwork(n.Name)) {
			continue
		}
		members := 0
		for _, id := range n.Containers {
			if r.isSelf(id) {
				// The agent's own container joined this network to probe; leave it.
				if err := r.cfg.Docker.NetworkDisconnect(ctx, n.Name, id); err != nil {
					r.log.Warn("decommission: leave network", "network", n.Name, "err", err)
				}
				continue
			}
			members++
		}
		if members > 0 {
			// Someone else's container is on this network: it is theirs to move.
			r.fail("network", n.Name, fmt.Errorf("%d container(s) that NebulaCtrl does not manage are connected", members))
			continue
		}
		err := r.cfg.Docker.NetworkRemove(ctx, n.Name)
		switch {
		case err == nil, errors.Is(err, docker.ErrNotFound):
			r.res.Networks++
		default:
			r.fail("network", n.Name, err)
		}
	}
}

func (r *runner) removeVolumes(ctx context.Context) {
	vols, err := r.cfg.Docker.VolumeList(ctx)
	if err != nil {
		r.fail("volume", "(list)", err)
		return
	}
	for _, v := range vols {
		if !ours(v.Labels) && !infraVolumes[v.Name] && !strings.HasPrefix(v.Name, agentprotocol.VolumePrefix) {
			continue
		}
		err := r.cfg.Docker.RemoveVolume(ctx, v.Name, false)
		switch {
		case err == nil, errors.Is(err, docker.ErrNotFound):
			r.res.Volumes++
		default:
			r.fail("volume", v.Name, err)
		}
	}
}
