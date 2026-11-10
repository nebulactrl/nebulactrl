package engine

import (
	"context"
	"sort"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Adoption: a server added to a control plane may already run NebulaCtrl
// workloads (a lost or reinstalled control plane, or another live instance).
// While the control plane holds the server (Reconcile.KeepUnknown) every
// managed container and live route of a service the frame does not name stays
// as it is; services the operator chose to leave (Reconcile.Unmanaged) stay
// untouched for good.

// applyAdoption records the adoption state of a reconcile frame.
func (e *Engine) applyAdoption(desired agentprotocol.Reconcile) {
	unmanaged := make(map[string]bool, len(desired.Unmanaged))
	for _, id := range desired.Unmanaged {
		unmanaged[id] = true
	}
	e.mu.Lock()
	e.unmanaged = unmanaged
	e.mu.Unlock()
	if was := e.keepUnknown.Swap(desired.KeepUnknown); was != desired.KeepUnknown {
		e.log.Info("adoption hold changed", "held", desired.KeepUnknown)
	}
	e.frameSeen.Store(true)
}

// preserved reports whether the sweeps and the route merge must leave a service
// alone: it runs unmanaged, or the server is held for adoption and the service
// is not among effective (the services this server runs per the control plane).
func (e *Engine) preserved(serviceID string, effective map[string]desiredEntry) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.preservedLocked(serviceID, effective)
}

// preservedLocked is preserved for a caller that holds e.mu.
func (e *Engine) preservedLocked(serviceID string, effective map[string]desiredEntry) bool {
	if e.unmanaged[serviceID] {
		return true
	}
	if !e.keepUnknown.Load() {
		return false
	}
	_, known := effective[serviceID]
	return !known
}

// unmanagedNetworks lists the networks of every container, running or not, of
// a service left running unmanaged: their networks are never swept.
func (e *Engine) unmanagedNetworks(ctx context.Context) (map[string]bool, error) {
	e.mu.Lock()
	n := len(e.unmanaged)
	e.mu.Unlock()
	if n == 0 {
		return nil, nil
	}
	cs, err := e.managedContainers(ctx)
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, c := range cs {
		e.mu.Lock()
		leave := e.unmanaged[c.Labels[agentprotocol.LabelService]]
		e.mu.Unlock()
		if !leave {
			continue
		}
		for name := range c.Networks {
			kept[name] = true
		}
	}
	return kept, nil
}

// managedContainers lists every service container (running or not) that
// carries the managed label and a service label, infrastructure excluded.
func (e *Engine) managedContainers(ctx context.Context) ([]docker.ContainerInfo, error) {
	return managedServiceContainers(ctx, e.cfg.Docker)
}

func managedServiceContainers(ctx context.Context, d docker.Engine) ([]docker.ContainerInfo, error) {
	cs, err := d.ContainerList(ctx, docker.ListFilter{
		All:    true,
		Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
	})
	if err != nil {
		return nil, err
	}
	out := cs[:0]
	for _, c := range cs {
		if !isInfraContainer(c.Name) && c.Labels[agentprotocol.LabelService] != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// isInfraContainer reports whether a managed container is infrastructure (the
// proxy, the tailnet node, the pages server): it carries the managed label but
// belongs to no service, and no sweep or inventory may touch it.
func isInfraContainer(name string) bool {
	return name == agentprotocol.ProxyContainer || name == agentprotocol.TailscaleContainer ||
		name == agentprotocol.PagesContainer || name == agentprotocol.PagesContainerAlt
}

// ManagedServiceIDs returns the sorted, distinct service IDs (the
// nebulactrl.service label) of every managed container on the host, running or
// not. It rides on the hello (Facts.ManagedServices): the control plane holds a
// newly added server that already runs workloads. A failing daemon yields none.
func ManagedServiceIDs(ctx context.Context, d docker.Engine) []string {
	cs, err := managedServiceContainers(ctx, d)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, c := range cs {
		if id := c.Labels[agentprotocol.LabelService]; !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
