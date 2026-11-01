package proxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Network isolation: app containers are not on the shared edge network, so
// Traefik joins the environment network (or the internal network of an
// environment with an egress-blocked service) of every service it routes to,
// and leaves it once no route needs it. Only networks this manager owns by
// naming convention are ever connected or disconnected: the edge network, the
// networks of the tailnet node and anything a user attached stay untouched.

// IsRoutedNetwork reports whether name is a network the proxy joins on demand:
// an environment network (nebulactrl-env-<id8>, nebulactrl-env-<id8>-int) or a team shared
// network (nebulactrl-shared-<id8>).
func IsRoutedNetwork(name string) bool {
	return strings.HasPrefix(name, agentprotocol.EnvNetworkPrefix) || strings.HasPrefix(name, agentprotocol.SharedNetworkPrefix)
}

// RouteNetworks is the sorted union of the on-demand networks the routes name.
func RouteNetworks(routes []agentprotocol.Route) []string {
	seen := map[string]bool{}
	for _, r := range routes {
		for _, n := range r.Networks {
			if IsRoutedNetwork(n) {
				seen[n] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// syncNetworks makes the proxy container's on-demand network memberships match
// the routes: missing networks are joined and, when prune is set, networks no
// route needs are left. A network that does not exist (its containers are gone)
// is skipped with a warning: nothing could be reached through it anyway.
func (m *Manager) syncNetworks(ctx context.Context, routes []agentprotocol.Route, prune bool) error {
	want := RouteNetworks(routes)
	if len(want) == 0 && !prune {
		return nil
	}
	eng := m.cfg.Engine
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	info, err := eng.ContainerInspect(ictx, agentprotocol.ProxyContainer)
	cancel()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", agentprotocol.ProxyContainer, err)
	}
	wanted := make(map[string]bool, len(want))
	for _, n := range want {
		wanted[n] = true
		if _, ok := info.Networks[n]; ok {
			continue
		}
		nctx, ncancel := context.WithTimeout(ctx, 30*time.Second)
		err := eng.NetworkConnect(nctx, n, agentprotocol.ProxyContainer, nil)
		ncancel()
		switch {
		case err == nil:
			m.log.Info("proxy joined network", "network", n)
		case errors.Is(err, docker.ErrNotFound):
			m.log.Warn("proxy cannot join a network that does not exist; routes through it will not work", "network", n)
		default:
			return fmt.Errorf("join network %s: %w", n, err)
		}
	}
	if !prune {
		return nil
	}
	var stale []string
	for n := range info.Networks {
		if IsRoutedNetwork(n) && !wanted[n] {
			stale = append(stale, n)
		}
	}
	sort.Strings(stale)
	for _, n := range stale {
		nctx, ncancel := context.WithTimeout(ctx, 30*time.Second)
		err := eng.NetworkDisconnect(nctx, n, agentprotocol.ProxyContainer)
		ncancel()
		if err != nil {
			m.log.Warn("proxy could not leave a network", "network", n, "err", err)
			continue
		}
		m.log.Info("proxy left network", "network", n)
	}
	return nil
}
