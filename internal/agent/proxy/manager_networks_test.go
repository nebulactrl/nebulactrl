package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	envA   = "nebulactrl-env-aaaaaaaa"
	envAIn = "nebulactrl-env-aaaaaaaa-int"
	envB   = "nebulactrl-env-bbbbbbbb"
	shared = "nebulactrl-shared-cccccccc"
)

// netEngine is fakeEngine with network membership: NetworkConnect and
// NetworkDisconnect change the proxy container's networks.
type netEngine struct {
	*fakeEngine
	missing     map[string]bool // networks that do not exist
	connects    []string
	disconnects []string
}

func (n *netEngine) NetworkConnect(_ context.Context, network, ctr string, _ []string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.missing[network] {
		return fmt.Errorf("%w: network %s", docker.ErrNotFound, network)
	}
	if ctr != agentprotocol.ProxyContainer || n.container == nil {
		return fmt.Errorf("unexpected connect of %q", ctr)
	}
	n.container.Networks[network] = docker.ContainerNetwork{IP: "10.9.0.2"}
	n.connects = append(n.connects, network)
	return nil
}

func (n *netEngine) NetworkDisconnect(_ context.Context, network, ctr string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ctr != agentprotocol.ProxyContainer || n.container == nil {
		return fmt.Errorf("unexpected disconnect of %q", ctr)
	}
	delete(n.container.Networks, network)
	n.disconnects = append(n.disconnects, network)
	return nil
}

func (n *netEngine) networks() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for name := range n.container.Networks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func newNetManager(t *testing.T) (*Manager, *netEngine, *fakeTraefik) {
	t.Helper()
	eng := &netEngine{fakeEngine: &fakeEngine{}, missing: map[string]bool{}}
	m, err := NewManager(Config{Engine: eng, HTTPPort: 8880, HTTPSPort: 8443, HTTPClient: &http.Client{}})
	require.NoError(t, err)
	tr := &fakeTraefik{user: AdminUser, password: m.adminSecret}
	m.http = &http.Client{Transport: tr}
	return m, eng, tr
}

func routeOn(id, host string, networks ...string) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Hosts: []string{host}, Upstreams: []string{"nc-" + id + "-0:80"}, Networks: networks,
	}
}

func TestApplyJoinsTheNetworksOfTheRoutes(t *testing.T) {
	m, eng, _ := newNetManager(t)

	require.NoError(t, m.Apply([]agentprotocol.Route{
		routeOn("a", "a.localhost", envA), routeOn("b", "b.localhost", envB), routeOn("a2", "a2.localhost", envA),
	}))

	require.Equal(t, []string{envA, envB}, eng.connects, "each network once, sorted")
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork, envA, envB}, eng.networks(), "the edge network stays")
	require.Empty(t, eng.disconnects)
}

func TestApplyLeavesNetworksNoRouteNeedsAnymore(t *testing.T) {
	m, eng, _ := newNetManager(t)
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA), routeOn("b", "b.localhost", envB)}))

	// The last route of environment B is gone: the proxy leaves B, keeps A and
	// never touches the edge network.
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA)}))
	require.Equal(t, []string{envB}, eng.disconnects)
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork, envA}, eng.networks())

	require.NoError(t, m.Apply(nil))
	require.Equal(t, []string{envB, envA}, eng.disconnects)
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork}, eng.networks(), "no routes: only the edge network is left")
}

func TestApplyMovesBetweenEnvAndInternalNetwork(t *testing.T) {
	m, eng, _ := newNetManager(t)
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("db", "db.localhost", envA)}))

	// The service started blocking egress: its route now names the internal network.
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("db", "db.localhost", envAIn)}))
	require.Equal(t, []string{envA, envAIn}, eng.connects)
	require.Equal(t, []string{envA}, eng.disconnects)
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork, envAIn}, eng.networks())
}

func TestApplyOnlyManagesEnvironmentAndSharedNetworks(t *testing.T) {
	m, eng, _ := newNetManager(t)
	require.NoError(t, m.Apply(oneRoute("x:80"))) // creates the container
	eng.mu.Lock()
	eng.container.Networks["somebody-elses"] = docker.ContainerNetwork{IP: "10.7.0.2"}
	eng.mu.Unlock()

	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA, "bridge", shared)}))
	require.Equal(t, []string{envA, shared}, eng.connects, "a route can name a shared network; foreign names are ignored")
	require.NoError(t, m.Apply(nil))
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork, "somebody-elses"}, eng.networks(), "foreign networks are never disconnected")
}

func TestApplySkipsAMissingNetworkWithoutFailing(t *testing.T) {
	m, eng, _ := newNetManager(t)
	eng.missing[envB] = true

	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA), routeOn("b", "b.localhost", envB)}))
	require.Equal(t, []string{envA}, eng.connects)
	require.Len(t, m.Current(), 2, "the table is still applied")
}

func TestRejectedTableDropsTheNetworksOnlyItNeeded(t *testing.T) {
	m, eng, tr := newNetManager(t)
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA)}))

	tr.rejectPUT = http.StatusBadRequest
	err := m.Apply([]agentprotocol.Route{routeOn("b", "b.localhost", envB)})
	require.Error(t, err)

	require.Equal(t, []string{envA}, m.Current()[0].Networks, "previous table is still the applied one")
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork, envA}, eng.networks(), "B was joined for the attempt and left again")
	require.Equal(t, []string{envB}, eng.disconnects)
}

func TestRecreatedProxyRejoinsTheNetworks(t *testing.T) {
	m, eng, _ := newNetManager(t)
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA)}))

	// A static setting changes: Traefik is recreated and starts on the edge only.
	m.SetTrafficAnalytics(false)
	require.NoError(t, m.Apply([]agentprotocol.Route{routeOn("a", "a.localhost", envA)}))
	require.Equal(t, []string{envA, envA}, eng.connects, "joined again after the recreate")
	require.ElementsMatch(t, []string{agentprotocol.EdgeNetwork, envA}, eng.networks())
}

func TestRouteNetworks(t *testing.T) {
	routes := []agentprotocol.Route{
		routeOn("a", "a.localhost", envB, envA), routeOn("b", "b.localhost", envA, agentprotocol.EdgeNetwork), {ServiceID: "ext"},
	}
	require.Equal(t, []string{envA, envB}, RouteNetworks(routes))
	require.True(t, IsRoutedNetwork(envA))
	require.True(t, IsRoutedNetwork(envAIn))
	require.True(t, IsRoutedNetwork(shared))
	require.False(t, IsRoutedNetwork(agentprotocol.EdgeNetwork))
}
