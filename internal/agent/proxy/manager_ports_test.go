package proxy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ContainerList lists the running containers of the fake (the port check asks
// which containers publish a port).
func (f *fleetEngine) ContainerList(_ context.Context, _ docker.ListFilter) ([]docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.ContainerInfo
	for _, c := range f.ctrs {
		if c.info.Running {
			out = append(out, c.info)
		}
	}
	return out, nil
}

// foreign adds a running container of another program that publishes ports.
func (f *fleetEngine) foreign(name string, ports ...docker.PortPublish) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctrs[name] = &fleetCtr{info: docker.ContainerInfo{ID: "x-" + name, Name: name, Running: true, State: "running", Ports: ports}}
}

func hostPortsOf(spec docker.ContainerSpec) []string {
	var out []string
	for _, p := range spec.Ports {
		if p.ContainerPort == 80 || p.ContainerPort == 443 {
			out = append(out, fmt.Sprintf("%d->%d/%s", p.HostPort, p.ContainerPort, p.Proto))
		}
	}
	return out
}

func free(string, int) bool { return false }

func withPorts(m *Manager, httpPort, httpsPort int) {
	cfg := m.ProxyConfig()
	cfg.HTTPPort, cfg.HTTPSPort = httpPort, httpsPort
	m.SetProxyConfig(cfg)
}

func TestServerPortsOverrideTheFlagsInSpecHashAndStaticFlags(t *testing.T) {
	st := &memRouteStore{}
	m, eng, tr, _ := newFleetManager(t, st, func(c *Config) { c.PortInUse = free })
	px := agentprotocol.ProxyContainer

	require.NoError(t, m.Apply(twoRoutes()))
	_, spec0, _ := eng.container(px)
	require.Equal(t, []string{"8880->80/tcp", "8443->443/tcp", "8443->443/udp"}, hostPortsOf(spec0), "the flags: no override yet")
	require.Contains(t, spec0.Cmd, "--entrypoints.websecure.http3.advertisedport=8443")
	p := m.Ports()
	require.Equal(t, agentprotocol.ProxyPortsFlag, p.Source)
	require.Equal(t, 8880, p.HTTP)
	require.Equal(t, 8443, p.HTTPS)

	// The server setting wins over the flags.
	withPorts(m, 9080, 9443)
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, 2, eng.count("create:"+px), "a port change recreates the proxy")
	require.Equal(t, 1, eng.count("remove:"+px))
	_, spec1, _ := eng.container(px)
	require.Equal(t, []string{"9080->80/tcp", "9443->443/tcp", "9443->443/udp"}, hostPortsOf(spec1))
	require.Contains(t, spec1.Cmd, "--entrypoints.websecure.http3.advertisedport=9443")
	require.NotContains(t, spec1.Cmd, "--entrypoints.websecure.http3.advertisedport=8443", "PublicHTTPSPort follows the override")
	require.NotEqual(t, spec0.Labels[LabelSpec], spec1.Labels[LabelSpec], "the spec fingerprint includes the ports")
	require.True(t, m.LastApply().Restarted)
	require.Contains(t, tr.puts[len(tr.puts)-1], "9443", "redirects target the new public HTTPS port")

	p = m.Ports()
	require.Equal(t, agentprotocol.ProxyPortsServer, p.Source)
	require.Equal(t, 9080, p.HTTP)
	require.Equal(t, 9443, p.HTTPS)
	require.Equal(t, 8880, p.FlagHTTP, "what a reset returns to")
	require.Equal(t, 8443, p.FlagHTTPS)
	require.NotNil(t, st.cfg, "the override is persisted with the proxy configuration")
	require.Equal(t, 9080, st.cfg.HTTPPort)
	require.Equal(t, 9443, st.cfg.HTTPSPort)
	hp, sp := m.WebPorts()
	require.Equal(t, 9080, hp)
	require.Equal(t, 9443, sp)

	// The same configuration again changes nothing.
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, 2, eng.count("create:"+px))

	// Resetting to 0 returns to the flags: the very same container spec as before.
	withPorts(m, 0, 0)
	require.NoError(t, m.Apply(twoRoutes()))
	_, spec2, _ := eng.container(px)
	require.Equal(t, spec0.Labels[LabelSpec], spec2.Labels[LabelSpec])
	require.Equal(t, []string{"8880->80/tcp", "8443->443/tcp", "8443->443/udp"}, hostPortsOf(spec2))
	require.Equal(t, agentprotocol.ProxyPortsFlag, m.Ports().Source)
	require.Zero(t, st.cfg.HTTPPort)
}

func TestOverrideIsRestoredAfterARestart(t *testing.T) {
	st := &memRouteStore{}
	m, eng, _, _ := newFleetManager(t, st, func(c *Config) { c.PortInUse = free })
	require.NoError(t, m.Apply(twoRoutes()))
	withPorts(m, 9080, 9443)
	require.NoError(t, m.Apply(twoRoutes()))
	creates := eng.count("create:" + agentprotocol.ProxyContainer)

	// A new agent process with the flags of before: the persisted override rules.
	m2, err := NewManager(Config{Engine: eng, Store: st, HTTPPort: 8880, HTTPSPort: 8443, PortInUse: free})
	require.NoError(t, err)
	hp, sp := m2.WebPorts()
	require.Equal(t, 9080, hp)
	require.Equal(t, 9443, sp)
	require.Equal(t, agentprotocol.ProxyPortsServer, m2.Ports().Source)
	recreated, err := m2.ensureContainer(context.Background(), m2.inputsFor(m2.Current(), m2.ProxyConfig()))
	require.NoError(t, err)
	require.False(t, recreated, "the running container already matches the persisted configuration")
	require.Equal(t, creates, eng.count("create:"+agentprotocol.ProxyContainer))
}

func TestOccupiedPortRefusedBeforeTouchingTheRunningProxy(t *testing.T) {
	var busy int
	m, eng, _, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) {
		c.PortInUse = func(_ string, port int) bool { return port == busy }
	})
	px := agentprotocol.ProxyContainer
	require.NoError(t, m.Apply(twoRoutes()))
	_, before, _ := eng.container(px)
	cfgBefore := m.ProxyConfig()
	n := len(eng.events())

	busy = 9080
	withPorts(m, 9080, 9443)
	err := m.Apply(twoRoutes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "port 9080")
	require.Contains(t, err.Error(), "already in use")
	require.Contains(t, err.Error(), "keeps running on :8880 and :8443")
	var restored *restoredError
	require.False(t, errors.As(err, &restored), "nothing was removed, so nothing needed restoring")

	info, after, ok := eng.container(px)
	require.True(t, ok)
	require.True(t, info.Running, "the old container keeps running")
	require.Equal(t, before.Labels[LabelSpec], after.Labels[LabelSpec])
	require.Equal(t, n, len(eng.events()), "no container call at all: not removed, not recreated")
	require.Equal(t, cfgBefore, m.ProxyConfig(), "the pending configuration is reverted")
	require.Equal(t, 8880, m.Ports().HTTP, "reporting stays accurate")

	// A foreign container (Coolify's proxy; Docker holds its ports without a listening socket).
	busy = 0
	eng.foreign("coolify-proxy", docker.PortPublish{HostPort: 9443, ContainerPort: 443, Proto: "tcp"})
	withPorts(m, 9080, 9443)
	err = m.Apply(twoRoutes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "port 9443")
	require.Contains(t, err.Error(), "coolify-proxy")
	require.Equal(t, 1, eng.count("create:"+px))

	// Once it is gone the move goes through.
	eng.mu.Lock()
	delete(eng.ctrs, "coolify-proxy")
	eng.mu.Unlock()
	withPorts(m, 9080, 9443)
	require.NoError(t, m.Apply(twoRoutes()))
	_, spec, _ := eng.container(px)
	require.Equal(t, []string{"9080->80/tcp", "9443->443/tcp", "9443->443/udp"}, hostPortsOf(spec))
}

func TestPortsTheProxyAlreadyPublishesAreNotProbed(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) {
		// Every port looks busy (our own proxy holds 8880): only a fresh port could be refused.
		c.PortInUse = func(_ string, port int) bool { return port == 8880 || port == 8443 }
	})
	require.NoError(t, m.Apply(twoRoutes()))
	withPorts(m, 0, 9443) // only HTTPS moves; 8880 stays ours
	require.NoError(t, m.Apply(twoRoutes()))
	_, spec, _ := eng.container(agentprotocol.ProxyContainer)
	require.Equal(t, []string{"8880->80/tcp", "9443->443/tcp", "9443->443/udp"}, hostPortsOf(spec))
}

func TestBindFailureOnNewPortsRestoresThePreviousContainer(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) { c.PortInUse = free })
	px := agentprotocol.ProxyContainer
	require.NoError(t, m.Apply(twoRoutes()))
	_, before, _ := eng.container(px)

	// Docker refuses the bind although the pre-check saw nothing (a race).
	eng.startErr = func(spec docker.ContainerSpec) error {
		for _, p := range spec.Ports {
			if p.HostPort == 9080 {
				return errors.New("driver failed programming external connectivity: Bind for 0.0.0.0:9080 failed: port is already allocated")
			}
		}
		return nil
	}
	withPorts(m, 9080, 9443)
	err := m.Apply(twoRoutes())
	require.Error(t, err)
	var restored *restoredError
	require.True(t, errors.As(err, &restored))
	require.Contains(t, err.Error(), "9080")

	info, after, ok := eng.container(px)
	require.True(t, ok, "the proxy is never left down")
	require.True(t, info.Running)
	require.Equal(t, before.Labels[LabelSpec], after.Labels[LabelSpec])
	require.Equal(t, 8880, m.Ports().HTTP)
	require.Zero(t, m.ProxyConfig().HTTPPort)
}

func TestPortValidationLeavesTheProxyAlone(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) { c.PortInUse = free })
	require.NoError(t, m.Apply(twoRoutes()))
	n := len(eng.events())

	for _, c := range []struct {
		name        string
		http, https int
		want        string
	}{
		{"equal", 9080, 9080, "must differ"},
		{"out of range", 70000, 9443, "not between"},
		{"admin port", agentprotocol.ProxyAdminPort, 9443, "admin port"},
		{"equals the other flag port", 8443, 0, "must differ"},
	} {
		withPorts(m, c.http, c.https)
		err := m.Apply(twoRoutes())
		require.ErrorContains(t, err, "proxy ports rejected", c.name)
		require.ErrorContains(t, err, c.want, c.name)
		require.Equal(t, n, len(eng.events()), c.name)
	}

	// A tcp route on the new HTTPS port is a clash too.
	withPorts(m, 9080, 9443)
	err := m.Apply(withL4(twoRoutes(), tcpRoute(9443)))
	require.ErrorContains(t, err, "is the proxy's own HTTPS port")
	require.Equal(t, n, len(eng.events()))
}

func TestPortsReportSourceAndForeignListeners(t *testing.T) {
	m, _, _, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) {
		c.PortInUse = func(_ string, port int) bool { return port == 80 || port == 443 }
	})
	st := m.Status()
	require.NotNil(t, st.Ports)
	require.Equal(t, []int{80, 443}, st.Ports.BusyOther, "another process holds the well-known ports")

	// The proxy's own ports are not foreign.
	m2, _, _, _ := newFleetManager(t, &memRouteStore{}, func(c *Config) {
		c.HTTPPort, c.HTTPSPort = 80, 443
		c.PortInUse = func(_ string, port int) bool { return port == 80 || port == 443 }
	})
	require.Empty(t, m2.Status().Ports.BusyOther)
}
