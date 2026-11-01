package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	svcDB   = "55555555-5555-5555-5555-555555555555"
	svcDNS2 = "66666666-6666-6666-6666-666666666666"
)

func withL4(base []agentprotocol.Route, extra ...agentprotocol.Route) []agentprotocol.Route {
	return append(append([]agentprotocol.Route(nil), base...), extra...)
}

func TestL4PortChangeRecreatesContainer(t *testing.T) {
	m, eng, tr, _ := newFleetManager(t, nil, nil)
	base := twoRoutes()
	px := agentprotocol.ProxyContainer

	require.NoError(t, m.Apply(base))
	require.Equal(t, 1, eng.count("create:"+px))
	_, spec0, _ := eng.container(px)
	require.NotContains(t, strings.Join(spec0.Cmd, " "), "tcp-")

	// A new tcp port: static entrypoint, published port, container recreated.
	table := withL4(base, tcpRoute(5432))
	require.NoError(t, m.Apply(table))
	require.Equal(t, 2, eng.count("create:"+px), "adding a port recreates the proxy")
	require.Equal(t, 1, eng.count("remove:"+px))
	_, spec1, _ := eng.container(px)
	require.Contains(t, spec1.Cmd, "--entrypoints.tcp-5432.address=:5432/tcp")
	require.Contains(t, portsOf(spec1), "5432->5432/tcp")
	require.NotEqual(t, spec0.Labels[LabelSpec], spec1.Labels[LabelSpec])
	require.True(t, m.LastApply().Restarted)
	require.Contains(t, tr.puts[len(tr.puts)-1], `"tcp-5432"`, "the tcp router is pushed to the recreated proxy")
	require.Equal(t, table, m.Current())

	// The same table again changes nothing.
	require.NoError(t, m.Apply(table))
	require.Equal(t, 2, eng.count("create:"+px))
	require.False(t, m.LastApply().Restarted)

	// A udp port, then removing the tcp port: each change of the port set recreates.
	table = withL4(table, udpRoute())
	require.NoError(t, m.Apply(table))
	_, spec2, _ := eng.container(px)
	require.Contains(t, spec2.Cmd, "--entrypoints.udp-5353.address=:5353/udp")
	require.Contains(t, portsOf(spec2), "5353->5353/udp")
	require.Equal(t, 3, eng.count("create:"+px))

	table = withL4(base, udpRoute())
	require.NoError(t, m.Apply(table))
	_, spec3, _ := eng.container(px)
	require.NotContains(t, portsOf(spec3), "5432->5432/tcp")
	require.Equal(t, 4, eng.count("create:"+px))

	// A change of routes only (same ports) is hot.
	table = withL4(twoRoutes()[:1], udpRoute())
	require.NoError(t, m.Apply(table))
	require.Equal(t, 4, eng.count("create:"+px))
}

func TestStartFailureRestoresPreviousContainerAndRoutes(t *testing.T) {
	st := &memRouteStore{}
	m, eng, tr, _ := newFleetManager(t, st, nil)
	px := agentprotocol.ProxyContainer
	base := twoRoutes()
	require.NoError(t, m.Apply(base))
	_, before, _ := eng.container(px)
	cfgBefore := m.ProxyConfig()

	// Port 5432 is taken on the host: the new container cannot start.
	eng.startErr = func(spec docker.ContainerSpec) error {
		for _, p := range spec.Ports {
			if p.HostPort == 5432 {
				return errors.New("driver failed programming external connectivity: Bind for 0.0.0.0:5432 failed: port is already allocated")
			}
		}
		return nil
	}
	n := len(eng.events())
	err := m.Apply(withL4(base, tcpRoute(5432)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "5432", "the error names the port")
	require.Contains(t, err.Error(), "tcp")
	require.Contains(t, err.Error(), "restored")
	var restored *restoredError
	require.True(t, errors.As(err, &restored))

	// The previous container is back, with the previous spec, and runs.
	info, after, ok := eng.container(px)
	require.True(t, ok, "the proxy is never left down")
	require.True(t, info.Running)
	require.Equal(t, before.Labels[LabelSpec], after.Labels[LabelSpec])
	require.Equal(t, before.Cmd, after.Cmd)
	require.Equal(t, portsOf(before), portsOf(after))
	require.Equal(t, []string{"remove:" + px, "create:" + px, "remove:" + px, "create:" + px, "start:" + px}, eng.events()[n:],
		"old removed, new created and failed, new discarded, previous recreated and started")

	// The previous route table is live again and remembered.
	require.Equal(t, base, m.Current())
	require.Equal(t, base, st.routes, "the persisted table is untouched")
	last := tr.puts[len(tr.puts)-1]
	require.Contains(t, last, "nc-"+svcApp+"-https")
	require.NotContains(t, last, "tcp-5432")
	require.Equal(t, cfgBefore, m.ProxyConfig(), "the pending configuration is reverted")

	// Once the port is free the same change goes through.
	eng.startErr = nil
	require.NoError(t, m.Apply(withL4(base, tcpRoute(5432))))
	_, spec, _ := eng.container(px)
	require.Contains(t, portsOf(spec), "5432->5432/tcp")
}

func TestFirstStartFailureHasNothingToRestore(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.startErr = func(docker.ContainerSpec) error {
		return errors.New("Bind for 0.0.0.0:80 failed: port is already allocated")
	}
	err := m.Apply(twoRoutes())
	require.Error(t, err)
	var restored *restoredError
	require.False(t, errors.As(err, &restored))
	require.Contains(t, err.Error(), "already in use")
}

func TestRejectedL4RoutersRestorePreviousContainerAndTable(t *testing.T) {
	m, eng, tr, _ := newFleetManager(t, nil, nil)
	px := agentprotocol.ProxyContainer
	base := withL4(twoRoutes(), tcpRoute(5432))
	require.NoError(t, m.Apply(base))
	_, before, _ := eng.container(px)

	// Traefik starts but disables the new udp router.
	tr.disabledL4 = true
	err := m.Apply(withL4(base, udpRoute()))
	require.Error(t, err)
	require.Contains(t, err.Error(), "disabled routers")

	_, after, ok := eng.container(px)
	require.True(t, ok)
	require.Equal(t, before.Labels[LabelSpec], after.Labels[LabelSpec], "the container of the previous ports is back")
	require.Contains(t, portsOf(after), "5432->5432/tcp")
	require.NotContains(t, portsOf(after), "5353->5353/udp")
	require.Equal(t, base, m.Current())
}

func TestValidationFailureLeavesContainerAndConfigAlone(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	require.NoError(t, m.Apply(twoRoutes()))
	creates := len(eng.creates)
	cfg := m.ProxyConfig()

	pending := cfg
	pending.UpstreamPolicy = agentprotocol.UpstreamPolicy{}
	m.SetProxyConfig(pending)
	err := m.Apply(withL4(twoRoutes(), agentprotocol.Route{
		ServiceID: svcDB, Upstreams: []string{"127.0.0.1:5432"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432},
	}))
	require.Error(t, err)
	require.Len(t, eng.creates, creates, "nothing was touched")
	require.Equal(t, cfg, m.ProxyConfig(), "the rejected pending configuration is dropped")
	require.Equal(t, twoRoutes(), m.Current())
}

func TestL4PortCollidingWithProxyListenerIsRejected(t *testing.T) {
	m, _, _, _ := newFleetManager(t, nil, nil)
	for _, port := range []int{8880, 8443, agentprotocol.ProxyAdminPort} {
		err := m.Apply(withL4(twoRoutes(), tcpRoute(port)))
		require.Error(t, err, port)
	}
}

func TestSpecHashIncludesExtraHostsAndDNS(t *testing.T) {
	m, _, _, _ := newFleetManager(t, nil, nil)
	base := m.desiredSpec()
	require.Equal(t, []string{"host.docker.internal:host-gateway"}, base.ExtraHosts, "host gateway is on by default")
	require.Empty(t, base.DNS)

	set := func(mut func(c *agentprotocol.ProxyConfig)) docker.ContainerSpec {
		c := agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}
		mut(&c)
		m.SetProxyConfig(c)
		return m.desiredSpec()
	}

	noGateway := set(func(c *agentprotocol.ProxyConfig) { c.UpstreamPolicy.AllowHostGateway = false })
	require.Empty(t, noGateway.ExtraHosts)
	require.NotEqual(t, base.Labels[LabelSpec], noGateway.Labels[LabelSpec], "ExtraHosts are part of the spec hash")

	magic := set(func(c *agentprotocol.ProxyConfig) {
		c.Tailnet = &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "n", MagicDNS: true}
	})
	require.Equal(t, []string{"100.100.100.100"}, magic.DNS)
	require.NotEqual(t, base.Labels[LabelSpec], magic.Labels[LabelSpec], "DNS is part of the spec hash")

	plain := set(func(c *agentprotocol.ProxyConfig) {
		c.Tailnet = &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "n"}
	})
	require.Empty(t, plain.DNS, "MagicDNS is opt-in")
	require.Equal(t, base.Labels[LabelSpec], plain.Labels[LabelSpec], "a managed node without MagicDNS needs no recreate")

	host := set(func(c *agentprotocol.ProxyConfig) {
		c.Tailnet = &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetHost, MagicDNS: true}
	})
	require.Empty(t, host.DNS, "MagicDNS only applies to the managed node")

	// Certificates and the allow-loopback toggle are dynamic: no recreate.
	dyn := set(func(c *agentprotocol.ProxyConfig) {
		c.UpstreamPolicy.AllowLoopback = true
		c.Certificates = []agentprotocol.CertRef{{ID: "c1"}}
	})
	require.Equal(t, base.Labels[LabelSpec], dyn.Labels[LabelSpec])
}

func TestPolicyHostGatewayChangeRecreatesAndRollsBack(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	px := agentprotocol.ProxyContainer
	require.NoError(t, m.Apply(twoRoutes()))
	_, withGateway, _ := eng.container(px)
	require.Equal(t, []string{"host.docker.internal:host-gateway"}, withGateway.ExtraHosts)

	cfg := m.ProxyConfig()
	cfg.UpstreamPolicy.AllowHostGateway = false
	m.SetProxyConfig(cfg)
	require.NoError(t, m.Apply(twoRoutes()))
	_, without, _ := eng.container(px)
	require.Empty(t, without.ExtraHosts)
	require.True(t, m.LastApply().Restarted)

	// Enabling it again, but the container cannot start: the previous one returns.
	cfg.UpstreamPolicy.AllowHostGateway = true
	m.SetProxyConfig(cfg)
	eng.startErr = func(s docker.ContainerSpec) error {
		if len(s.ExtraHosts) > 0 {
			return errors.New("invalid IP address in add-host: host-gateway unsupported")
		}
		return nil
	}
	require.Error(t, m.Apply(twoRoutes()))
	_, restored, ok := eng.container(px)
	require.True(t, ok)
	require.Empty(t, restored.ExtraHosts)
	require.False(t, m.ProxyConfig().UpstreamPolicy.AllowHostGateway, "the failed configuration is not kept")
}

func TestProxyConfigPersistsAndIsRestored(t *testing.T) {
	crt, key := testCert(t)
	st := &memRouteStore{}
	m, _, tr, _ := newFleetManager(t, st, nil)
	cfg := agentprotocol.ProxyConfig{
		UpstreamPolicy: agentprotocol.UpstreamPolicy{AllowLoopback: true, AllowHostGateway: true},
		Certificates:   []agentprotocol.CertRef{{ID: "c1", Name: "wild", Domains: []string{"*.example.com"}, CertPEM: crt, KeyRef: "cert:c1", KeyPEM: key}},
	}
	m.SetProxyConfig(cfg)
	require.NoError(t, m.Apply(twoRoutes()))
	require.NotNil(t, st.cfg, "the configuration is persisted with the table")
	require.Equal(t, key, st.cfg.Certificates[0].KeyPEM, "resolved keys are kept so a restart converges offline")
	require.Equal(t, cfg, m.ProxyConfig())

	// A restarted agent picks it up from the store and renders the certificate.
	m2, _, tr2, _ := newFleetManager(t, st, nil)
	require.Equal(t, cfg, m2.ProxyConfig())
	require.Equal(t, cfg, m2.appliedConfig())
	require.NoError(t, m2.Bootstrap(t.Context()))
	require.Contains(t, tr2.puts[len(tr2.puts)-1], `"certFile"`)
	_ = tr
}

func TestCertificatesReportedAndBadPairExcluded(t *testing.T) {
	crt, key := testCert(t)
	m, _, tr, _ := newFleetManager(t, nil, nil)
	m.SetProxyConfig(agentprotocol.ProxyConfig{
		UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(),
		Certificates: []agentprotocol.CertRef{
			{ID: "good", Name: "good", Domains: []string{"example.com"}, CertPEM: crt, KeyPEM: key},
			{ID: "bad", Name: "bad", Domains: []string{"x.example.com"}, CertPEM: crt, KeyPEM: "garbage"},
		},
	})
	require.NoError(t, m.Apply(twoRoutes()), "a bad pair must not break the apply")
	rep := m.LastApply()
	require.Len(t, rep.Certificates, 2)
	require.Equal(t, agentprotocol.CertStatus{ID: "good", Loaded: true}, rep.Certificates[0])
	require.False(t, rep.Certificates[1].Loaded)
	require.NotEmpty(t, rep.Certificates[1].Error)
	require.Equal(t, 1, strings.Count(tr.puts[len(tr.puts)-1], `"certFile"`))
	require.NotContains(t, rep.Certificates[1].Error, "garbage", "key material is never echoed")
}

func TestFailedApplyRevertsPendingConfigAndKeepsPreviousTable(t *testing.T) {
	m, _, tr, _ := newFleetManager(t, nil, nil)
	require.NoError(t, m.Apply(twoRoutes()))
	good := m.ProxyConfig()

	cfg := good
	cfg.UpstreamPolicy.AllowLoopback = true
	m.SetProxyConfig(cfg)
	tr.rejectPUT = 500
	require.Error(t, m.Apply(oneRoute("nc-x-1:80")))
	require.Equal(t, good, m.ProxyConfig(), "the config and the table are applied together or not at all")
	require.Equal(t, twoRoutes(), m.Current())
}

func TestSecretsNeverReachLogs(t *testing.T) {
	crt, key := testCert(t)
	const secretHash = "$2a$10$LOGLEAKCHECKabcdefghijklmnopqrstuvwxyz0123456789ABCDE"
	m, _, _, logs := newFleetManager(t, &memRouteStore{}, nil)
	m.SetProxyConfig(agentprotocol.ProxyConfig{
		UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(),
		Certificates:   []agentprotocol.CertRef{{ID: "c1", Domains: []string{"example.com"}, CertPEM: crt, KeyPEM: key}},
	})
	r := ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
		d.Options.BasicAuth, d.AuthRef, d.AuthUsers = true, "auth:"+domA, []string{"alice:" + secretHash}
	}))
	require.NoError(t, m.Apply([]agentprotocol.Route{r}))
	// Provoke the failure path, too.
	r.Domains[0].AuthUsers = []string{"alice:" + secretHash}
	r.Upstreams = []string{"bad host:80"}
	require.Error(t, m.Apply([]agentprotocol.Route{r}))

	out := logs.String()
	require.NotContains(t, out, secretHash)
	require.NotContains(t, out, "LOGLEAKCHECK")
	require.NotContains(t, out, strings.TrimSpace(strings.Split(key, "\n")[1]), "private key lines are never logged")
}

func TestRouteStoreOptionalConfigNilStore(t *testing.T) {
	m, _, _, _ := newFleetManager(t, nil, nil)
	require.Equal(t, agentprotocol.DefaultUpstreamPolicy(), m.ProxyConfig().UpstreamPolicy)
	require.NoError(t, m.Apply(twoRoutes()))
}

// desiredSpec is the container the agent wants for the current table and
// configuration.
func (m *Manager) desiredSpec() docker.ContainerSpec {
	return m.specFor(m.inputsFor(m.Current(), m.ProxyConfig()))
}
