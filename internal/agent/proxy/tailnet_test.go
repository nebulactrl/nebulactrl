package proxy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	tsName      = agentprotocol.TailscaleContainer
	tsVolume    = agentprotocol.TailscaleStateVolume
	secretKey   = "tskey-auth-kSECRETxxxxxxxxxxxxxxxxxxxxxxxx"
	runningJSON = `{"BackendState":"Running","Self":{"ID":"nABC123CNTRL","DNSName":"nebula-a.tail1234.ts.net.","TailscaleIPs":["100.101.102.103","fd7a:115c:a1e0::1"]}}`
)

func managedSpec(mut func(*agentprotocol.TailnetSpec)) agentprotocol.ProxyConfig {
	t := &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "nebula-a"}
	if mut != nil {
		mut(t)
	}
	return agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(), Tailnet: t}
}

func statusExec(js string) func(string, []string) (string, int, error) {
	return func(_ string, argv []string) (string, int, error) {
		if len(argv) >= 3 && argv[0] == "tailscale" && argv[1] == "status" {
			return js, 0, nil
		}
		return "", 1, fmt.Errorf("unexpected exec %v", argv)
	}
}

func TestTailscaleContainerSpecExactly(t *testing.T) {
	spec := tailscaleSpec(agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "nebula-a"}, "px-id", "")
	require.Equal(t, "nebulactrl-tailscale", spec.Name)
	require.Equal(t, "tailscale/tailscale:v1.104.2@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d", spec.Image)
	require.Equal(t, "container:nebulactrl-traefik", spec.NetworkMode, "shares Traefik's network namespace")
	require.Empty(t, spec.Networks, "no network of its own")
	require.Empty(t, spec.Ports, "publishes nothing")
	require.Equal(t, []docker.DeviceMapping{{HostPath: "/dev/net/tun"}}, spec.Devices)
	require.Equal(t, []string{"NET_ADMIN", "NET_RAW"}, spec.CapAdd)
	require.Empty(t, spec.CapDrop)
	require.Equal(t, []docker.VolumeMount{{Volume: "nebulactrl-tailscale-state", Target: "/var/lib/tailscale"}}, spec.Mounts, "the only mount is the state volume (no docker socket)")
	require.Equal(t, "unless-stopped", spec.RestartPolicy)
	require.Equal(t, []string{
		"TS_USERSPACE=false",
		"TS_STATE_DIR=/var/lib/tailscale",
		"TS_AUTH_ONCE=true",
		"TS_ACCEPT_DNS=false",
		"TS_HOSTNAME=nebula-a",
		"TS_ENABLE_HEALTH_CHECK=true",
		"TS_LOCAL_ADDR_PORT=127.0.0.1:9002",
	}, spec.Env, "no auth key outside provisioning")
	require.Equal(t, "px-id", spec.Labels[LabelTailnetNetns])
	require.Equal(t, "true", spec.Labels[agentprotocol.LabelManaged])
	require.Equal(t, []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:9002/healthz"}, spec.Healthcheck.Test)

	withKey := tailscaleSpec(agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "nebula-a"}, "px-id", secretKey)
	require.Equal(t, append(append([]string(nil), spec.Env...), "TS_AUTHKEY="+secretKey), withKey.Env)
	require.Equal(t, spec.Labels[LabelTailnetSpec], withKey.Labels[LabelTailnetSpec], "the key is not part of the spec fingerprint")
	for _, v := range withKey.Labels {
		require.NotContains(t, v, "tskey-", "the key is never a label")
	}

	renamed := tailscaleSpec(agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "other"}, "px-id", "")
	require.NotEqual(t, spec.Labels[LabelTailnetSpec], renamed.Labels[LabelTailnetSpec])
}

func TestProvisionCreatesNodeWithKeyThenForgetsIt(t *testing.T) {
	st := &memRouteStore{}
	m, eng, _, logs := newFleetManager(t, st, nil)
	eng.execFn = statusExec(runningJSON)

	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))

	info, spec, ok := eng.container(tsName)
	require.True(t, ok)
	require.True(t, info.Running)
	require.Contains(t, spec.Env, "TS_AUTHKEY="+secretKey)
	require.Equal(t, "container:nebulactrl-traefik", info.NetworkMode)
	require.True(t, eng.hasVolume(tsVolume))
	require.Equal(t, 1, eng.count("pull:"+TailscaleImage))
	px, _, _ := eng.container(agentprotocol.ProxyContainer)
	require.Equal(t, px.ID, spec.Labels[LabelTailnetNetns])

	// Status comes from `tailscale status --json`.
	want := &agentprotocol.TailnetStatus{
		Mode: agentprotocol.TailnetManaged, State: agentprotocol.TailnetStateOnline,
		FQDN: "nebula-a.tail1234.ts.net", IPs: []string{"100.101.102.103", "fd7a:115c:a1e0::1"}, DeviceID: "nABC123CNTRL",
	}
	require.Equal(t, want, m.TailnetStatus())
	require.Equal(t, want, m.LastApply().Tailnet)

	// The one-shot flags and the key are gone, in memory and on disk.
	require.False(t, m.ProxyConfig().Tailnet.Provision)
	require.False(t, st.cfg.Tailnet.Provision)
	require.Equal(t, "nebula-a", st.cfg.Tailnet.Hostname)
	require.Empty(t, m.tailnetKey)
	require.NotContains(t, logs.String(), secretKey)
	require.NotContains(t, fmt.Sprintf("%+v", *st.cfg), "tskey-")
}

func TestTailnetReconcileIsIdempotent(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, 1, eng.count("create:"+tsName))

	for range 3 {
		require.NoError(t, m.Apply(twoRoutes()))
	}
	require.Equal(t, 1, eng.count("create:"+tsName), "no recreate while nothing changed")
	require.Equal(t, 0, eng.count("remove:"+tsName))
	require.Equal(t, 1, eng.count("volume+:"+tsVolume))
	require.Equal(t, 0, eng.count("volume-:"+tsVolume))
}

func TestTailnetMissingContainerIsRecreatedWithoutKeyOrVolumeLoss(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))

	// Someone removed the container; the identity is still on the volume.
	require.NoError(t, eng.ContainerRemove(t.Context(), tsName, true, false))
	eng.log = nil
	require.NoError(t, m.Apply(twoRoutes()))
	_, spec, ok := eng.container(tsName)
	require.True(t, ok)
	for _, e := range spec.Env {
		require.False(t, strings.HasPrefix(e, "TS_AUTHKEY="), "recreation reuses the node identity, no new key")
	}
	require.Equal(t, 0, eng.count("volume-:"+tsVolume), "the state volume is kept")
}

func TestTailnetReprovisionReplacesNodeAndVolume(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey("tskey-auth-FIRST")
	require.NoError(t, m.Apply(twoRoutes()))
	eng.log = nil

	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey("tskey-auth-SECOND")
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, []string{"remove:" + tsName, "volume-:" + tsVolume, "volume+:" + tsVolume, "create:" + tsName, "start:" + tsName}, filterEvents(eng.events(), tsName, tsVolume))
	_, spec, _ := eng.container(tsName)
	require.Contains(t, spec.Env, "TS_AUTHKEY=tskey-auth-SECOND")
}

func filterEvents(ev []string, needles ...string) []string {
	var out []string
	for _, e := range ev {
		for _, n := range needles {
			if strings.HasSuffix(e, ":"+n) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func TestTailnetProvisionWithoutKeyFails(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	err := m.Apply(twoRoutes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "auth key")
	_, _, ok := eng.container(tsName)
	require.False(t, ok)
	require.Equal(t, twoRoutes(), m.Current(), "a tailnet failure does not undo a good route table")
	require.False(t, m.ProxyConfig().Tailnet.Provision, "the attempt is consumed")
}

func TestTailnetErrorsRedactTheAuthKey(t *testing.T) {
	m, eng, _, logs := newFleetManager(t, nil, nil)
	eng.createErr = func(spec docker.ContainerSpec) error {
		if spec.Name == tsName {
			return errors.New("create failed with env TS_AUTHKEY=" + secretKey)
		}
		return nil
	}
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	err := m.Apply(twoRoutes())
	require.Error(t, err)
	require.NotContains(t, err.Error(), secretKey)
	require.Contains(t, err.Error(), "[redacted]")
	require.NotContains(t, logs.String(), secretKey)
}

func TestTailnetRemoveDeletesContainerAndVolume(t *testing.T) {
	st := &memRouteStore{}
	m, eng, _, _ := newFleetManager(t, st, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))

	m.SetProxyConfig(agentprotocol.ProxyConfig{
		UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(),
		Tailnet:        &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetOff, Remove: true},
	})
	require.NoError(t, m.Apply(twoRoutes()))
	_, _, ok := eng.container(tsName)
	require.False(t, ok)
	require.False(t, eng.hasVolume(tsVolume))
	require.Equal(t, &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetOff, State: agentprotocol.TailnetStateOff}, m.TailnetStatus())
	require.False(t, st.cfg.Tailnet.Remove, "removal is a one-shot")

	// Removing again is a no-op, not an error.
	m.SetProxyConfig(agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(), Tailnet: &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetOff, Remove: true}})
	require.NoError(t, m.Apply(twoRoutes()))
}

func TestTailnetModeOffWithoutRemoveNeverDeletesAnIdentity(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))

	// An agent that lost its state sees a default configuration: the node survives.
	m.SetProxyConfig(agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()})
	require.NoError(t, m.Apply(twoRoutes()))
	_, _, ok := eng.container(tsName)
	require.True(t, ok)
	require.True(t, eng.hasVolume(tsVolume))
}

func TestTailnetNodeFollowsProxyContainerRecreation(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))
	oldPX, _, _ := eng.container(agentprotocol.ProxyContainer)
	eng.log = nil

	// A new tcp port recreates Traefik: its netns is gone, so the node is recreated.
	require.NoError(t, m.Apply(withL4(twoRoutes(), tcpRoute(5432))))
	newPX, _, _ := eng.container(agentprotocol.ProxyContainer)
	require.NotEqual(t, oldPX.ID, newPX.ID)
	_, spec, ok := eng.container(tsName)
	require.True(t, ok)
	require.Equal(t, newPX.ID, spec.Labels[LabelTailnetNetns], "attached to the new proxy container")
	for _, e := range spec.Env {
		require.False(t, strings.HasPrefix(e, "TS_AUTHKEY="), "identity persists in the volume, no key needed")
	}
	ev := filterEvents(eng.events(), tsName, agentprotocol.ProxyContainer)
	require.Equal(t, []string{
		"remove:" + tsName,
		"remove:" + agentprotocol.ProxyContainer,
		"create:" + agentprotocol.ProxyContainer,
		"start:" + agentprotocol.ProxyContainer,
		"create:" + tsName,
		"start:" + tsName,
	}, ev, "the node leaves before its namespace owner goes and joins the new one")
	require.Equal(t, 0, eng.count("volume-:"+tsVolume))
}

func TestTailnetNodeIsRestoredWithRestoredProxyContainer(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))

	eng.startErr = func(spec docker.ContainerSpec) error {
		for _, p := range spec.Ports {
			if p.HostPort == 5432 {
				return errors.New("Bind for 0.0.0.0:5432 failed: port is already allocated")
			}
		}
		return nil
	}
	require.Error(t, m.Apply(withL4(twoRoutes(), tcpRoute(5432))))
	px, _, ok := eng.container(agentprotocol.ProxyContainer)
	require.True(t, ok)
	_, spec, ok := eng.container(tsName)
	require.True(t, ok, "the node is back after the proxy was restored")
	require.Equal(t, px.ID, spec.Labels[LabelTailnetNetns])
}

func TestParseTailnetStatus(t *testing.T) {
	base := func() *agentprotocol.TailnetStatus {
		return &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetManaged}
	}
	cases := []struct {
		name, js, state, msg string
	}{
		{"running", runningJSON, agentprotocol.TailnetStateOnline, ""},
		{"needs login", `{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/x"}`, agentprotocol.TailnetStateNeedsAuth, "Device removed from the tailnet or key rejected. Re-provision."},
		{"needs machine auth", `{"BackendState":"NeedsMachineAuth"}`, agentprotocol.TailnetStateNeedsAuth, "Device removed from the tailnet or key rejected. Re-provision."},
		{"stopped", `{"BackendState":"Stopped"}`, agentprotocol.TailnetStateOffline, "Tailscale is stopped."},
		{"starting", `{"BackendState":"Starting"}`, agentprotocol.TailnetStateProvisioning, "Tailscale is starting."},
		{"no state", `{"BackendState":"NoState"}`, agentprotocol.TailnetStateProvisioning, "Tailscale is starting."},
		{"garbage", `not json`, agentprotocol.TailnetStateProvisioning, "Waiting for tailscaled to answer."},
		{"empty", ``, agentprotocol.TailnetStateProvisioning, "Waiting for tailscaled to answer."},
	}
	for _, c := range cases {
		got := parseTailnetStatus([]byte(c.js), base())
		require.Equal(t, c.state, got.State, c.name)
		require.Equal(t, c.msg, got.Message, c.name)
	}
	got := parseTailnetStatus([]byte(runningJSON), base())
	require.Equal(t, "nebula-a.tail1234.ts.net", got.FQDN, "trailing dot trimmed")
	require.Equal(t, "nABC123CNTRL", got.DeviceID)
	require.Equal(t, []string{"100.101.102.103", "fd7a:115c:a1e0::1"}, got.IPs)
}

func TestTailnetStatusRefreshReportsNeedsAttention(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))
	require.Equal(t, agentprotocol.TailnetStateOnline, m.TailnetStatus().State)

	// The device was removed in the admin console: the node asks to log in again.
	eng.execFn = statusExec(`{"BackendState":"NeedsLogin"}`)
	m.refreshTailnet(t.Context())
	st := m.TailnetStatus()
	require.Equal(t, agentprotocol.TailnetStateNeedsAuth, st.State)
	require.Equal(t, "Device removed from the tailnet or key rejected. Re-provision.", st.Message)

	// A stopped container reads as offline, a missing one too.
	info, _, _ := eng.container(tsName)
	eng.mu.Lock()
	eng.ctrs[tsName].info.Running, eng.ctrs[tsName].info.State = false, "exited"
	eng.mu.Unlock()
	_ = info
	m.refreshTailnet(t.Context())
	require.Equal(t, agentprotocol.TailnetStateOffline, m.TailnetStatus().State)
	require.NoError(t, eng.ContainerRemove(t.Context(), tsName, true, false))
	m.refreshTailnet(t.Context())
	require.Equal(t, agentprotocol.TailnetStateOffline, m.TailnetStatus().State)
}

func TestTailnetExecFailureReadsAsProvisioning(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = func(string, []string) (string, int, error) { return "", 0, errors.New("exec failed") }
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()), "an unreadable status never fails the apply")
	require.Equal(t, agentprotocol.TailnetStateProvisioning, m.TailnetStatus().State)
}

func TestTailnetHostModeUsesDetectionAndRunsNothing(t *testing.T) {
	detected := &agentprotocol.HostTailscale{Detected: true, IPs: []string{"100.64.0.9"}}
	m, eng, _, _ := newFleetManager(t, nil, func(c *Config) { c.HostTailscale = func() *agentprotocol.HostTailscale { return detected } })
	m.SetProxyConfig(agentprotocol.ProxyConfig{
		UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(), Tailnet: &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetHost},
	})
	require.NoError(t, m.Apply(twoRoutes()))
	_, _, ok := eng.container(tsName)
	require.False(t, ok, "host mode starts no container")
	require.Equal(t, &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetHost, State: agentprotocol.TailnetStateOnline, IPs: []string{"100.64.0.9"}}, m.TailnetStatus())

	detected = &agentprotocol.HostTailscale{}
	st := m.TailnetStatus()
	require.Equal(t, agentprotocol.TailnetStateNeedsAuth, st.State)
	require.Contains(t, st.Message, "not detected")
}

func TestTailnetStatusNilWithoutTailnet(t *testing.T) {
	m, _, _, _ := newFleetManager(t, nil, nil)
	require.Nil(t, m.TailnetStatus())
	require.NoError(t, m.Apply(twoRoutes()))
	require.Nil(t, m.TailnetStatus())
	require.Nil(t, m.LastApply().Tailnet)
}

func TestTailnetHealthLoopRefreshesStatusWhenDue(t *testing.T) {
	m, eng, _, _ := newFleetManager(t, nil, nil)
	eng.execFn = statusExec(runningJSON)
	m.SetProxyConfig(managedSpec(func(s *agentprotocol.TailnetSpec) { s.Provision = true }))
	m.SetTailnetAuthKey(secretKey)
	require.NoError(t, m.Apply(twoRoutes()))

	eng.execFn = statusExec(`{"BackendState":"Stopped"}`)
	m.refreshTailnetIfDue(t.Context())
	require.Equal(t, agentprotocol.TailnetStateOnline, m.TailnetStatus().State, "not due yet")
	m.mu.Lock()
	m.tn.checkedAt = m.tn.checkedAt.Add(-2 * tailnetStatusEvery)
	m.mu.Unlock()
	m.refreshTailnetIfDue(t.Context())
	require.Equal(t, agentprotocol.TailnetStateOffline, m.TailnetStatus().State)
}
