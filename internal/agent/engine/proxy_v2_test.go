package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	routeSvc   = "77777777-0000-4000-8000-000000000001"
	routeSvc2  = "88888888-0000-4000-8000-000000000002"
	domID      = "99999999-1111-4111-8111-111111111111"
	authHash   = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
	authSecret = agentprotocol.SecretAuthPrefix + domID
)

// cfgProxy adds the optional proxy v2 surface to fakeProxy and records probes.
type cfgProxy struct {
	*fakeProxy
	pmu    sync.Mutex
	cfgs   []agentprotocol.ProxyConfig
	keys   []string
	last   agentprotocol.ProxyConfigureResult
	probed []string // host+path per probe
}

func (c *cfgProxy) SetProxyConfig(cfg agentprotocol.ProxyConfig) {
	c.pmu.Lock()
	c.cfgs = append(c.cfgs, cfg)
	c.pmu.Unlock()
}
func (c *cfgProxy) SetTailnetAuthKey(k string) {
	c.pmu.Lock()
	c.keys = append(c.keys, k)
	c.pmu.Unlock()
}
func (c *cfgProxy) LastApply() agentprotocol.ProxyConfigureResult {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	return c.last
}
func (c *cfgProxy) Probe(ctx context.Context, host, path string) (int, error) {
	c.pmu.Lock()
	c.probed = append(c.probed, host+path)
	c.pmu.Unlock()
	return c.fakeProxy.Probe(ctx, host, path)
}

func newV2Rig(t *testing.T, dial func(context.Context, string, string) (net.Conn, error)) (*rig, *cfgProxy) {
	t.Helper()
	r := newRig(t, nil)
	cp := &cfgProxy{fakeProxy: r.px}
	e, err := New(Config{
		Docker: r.dk, Proxy: cp, Builder: r.bld, Sender: r.snd, Health: healthy,
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond, Dial: dial,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)
	r.eng = e
	return r, cp
}

func extHTTP(id, host, upstream string) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Hosts: []string{host}, Upstreams: []string{upstream},
		RouteExt: agentprotocol.RouteExt{Kind: agentprotocol.RouteKindExternal},
	}
}

func tcpL4(id string) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Upstreams: []string{"192.168.1.5:5432"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432},
	}
}

func udpL4(id string, port int) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Upstreams: []string{"192.168.1.5:53"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoUDP, ListenPort: port},
	}
}

func (r *rig) command(kind string, payload any, secrets map[string]string) (agentprotocol.ResultFrame, agentprotocol.ProxyConfigureResult) {
	r.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(r.t, err)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "cmd-p", Kind: kind, Payload: raw, Secrets: secrets})
	var pr agentprotocol.ProxyConfigureResult
	require.NoError(r.t, json.Unmarshal(res.Data, &pr))
	require.Equal(r.t, res.Outcome, pr.Outcome)
	// The same data decodes as a DeployResult (HandleResult on the control plane).
	var dr agentprotocol.DeployResult
	require.NoError(r.t, json.Unmarshal(res.Data, &dr))
	require.Equal(r.t, pr.Outcome, dr.Outcome)
	require.Equal(r.t, pr.Message, dr.Message)
	return res, pr
}

func TestSyncRoutesRouteOnlyDeploymentEmitsEventsAndVerifies(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	routes := []agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.20:8123")}

	res, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: routes, DeploymentID: depV2}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, pr.Message)
	require.Empty(t, res.Error)
	require.Equal(t, routes, r.px.Current())
	require.Equal(t, []string{"nas.example.com/"}, cp.probed)

	steps := r.snd.eventSteps()
	for _, want := range []string{"route:started", "route:succeeded", "verify:started", "verify:succeeded"} {
		require.Contains(t, steps, want)
	}
	for _, ev := range r.snd.events {
		require.Equal(t, depV2, ev.DeploymentID)
	}
}

func TestSyncRoutesWithoutDeploymentIDEmitsNoEvents(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: []agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")}}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome)
	require.Empty(t, r.snd.events)
	require.Empty(t, cp.probed, "a plain table sync is not verified")
	require.Equal(t, "Applied 1 route(s)", pr.Message)
}

func TestSyncRoutesVerifyFailureRollsBackToPreviousTable(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	prev := []agentprotocol.Route{{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}}
	require.NoError(t, r.px.Apply(prev))
	r.px.applies = nil
	r.px.probes = []int{502}

	next := append(append([]agentprotocol.Route(nil), prev...), extHTTP(routeSvc, "nas.example.com", "192.168.1.20:8123"))
	res, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: next, DeploymentID: depV2}, nil)

	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome)
	require.Equal(t, agentprotocol.OutcomeRolledBack, pr.Outcome)
	require.Contains(t, pr.Message, "HTTP 502")
	require.Contains(t, pr.Message, "Rolled back")
	require.Equal(t, res.Error, pr.Message)
	require.Equal(t, prev, r.px.Current(), "the previous table is live again")
	require.Equal(t, [][]agentprotocol.Route{next, prev}, r.px.applies, "new table applied, then the previous one restored")
	require.Len(t, cp.probed, 3, "tries from the engine config")
	steps := r.snd.eventSteps()
	require.Contains(t, steps, "verify:failed")
	require.NotContains(t, steps, "verify:succeeded")
}

func TestSyncRoutesRollbackFailureReportsRollbackFailed(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	prev := []agentprotocol.Route{{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}}
	require.NoError(t, r.px.Apply(prev))
	r.px.applies = nil
	r.px.probes = []int{500}
	r.px.applyErr = func(n int) error {
		if n == 2 {
			return errors.New("proxy down")
		}
		return nil
	}
	res, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{
		Routes: append(append([]agentprotocol.Route(nil), prev...), extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")), DeploymentID: depV2,
	}, nil)
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome)
	require.Contains(t, pr.Message, "proxy down")
}

func TestSyncRoutesApplyRejectionFailsWithRouteEvent(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.px.applyErr = func(int) error { return errors.New("proxy disabled routers: nc-x-https: bad") }
	res, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{
		Routes: []agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")}, DeploymentID: depV2,
	}, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, pr.Message, "disabled routers")
	require.Contains(t, r.snd.eventSteps(), "route:failed")
	require.NotContains(t, r.snd.eventSteps(), "verify:started")
}

func TestSyncRoutesVerifySkipAndUDPAndRedirectOnly(t *testing.T) {
	r, cp := newV2Rig(t, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("must not dial") })
	r.px.probes = []int{503}

	skip := extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")
	skip.VerifySkip = true
	redirectOnly := extHTTP(routeSvc2, "old.example.com", "192.168.1.21:80")
	redirectOnly.Domains = []agentprotocol.DomainRule{{ID: domID, Host: "old.example.com", Kind: agentprotocol.DomainRedirect, RedirectTo: "example.com"}}
	table := []agentprotocol.Route{skip, redirectOnly, udpL4("dns", 5353)}

	res, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table, DeploymentID: depV2}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, pr.Message)
	require.Empty(t, cp.probed, "verifySkip, redirect-only and udp routes are never probed or dialed")
	require.Contains(t, r.snd.eventSteps(), "verify:succeeded")
}

func TestSyncRoutesVerifyProbesRulePathPrefix(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	route := extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")
	route.HealthPath = "/health"
	route.Domains = []agentprotocol.DomainRule{
		{ID: domID, Host: "nas.example.com", Kind: agentprotocol.DomainRedirect, RedirectTo: "x.example.com"},
		{ID: "aaaaaaaa-1111-4111-8111-111111111111", Host: "nas.example.com", PathPrefix: "/app"},
	}
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: []agentprotocol.Route{route}, DeploymentID: depV2}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{"nas.example.com/app/health"}, cp.probed, "the first serving rule, with its prefix")
}

func TestSyncRoutesTCPVerifyDialsEdgeThenLoopback(t *testing.T) {
	var dialed []string
	ok := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	r, _ := newV2Rig(t, ok)
	r.dk.seed(agentprotocol.ProxyContainer, "", "", true, time.Hour)
	r.dk.mu.Lock()
	for _, c := range r.dk.containers {
		if c.Name == agentprotocol.ProxyContainer {
			c.Networks = map[string]docker.ContainerNetwork{agentprotocol.EdgeNetwork: {IP: "172.18.0.2"}}
		}
	}
	r.dk.mu.Unlock()

	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: []agentprotocol.Route{tcpL4(routeSvc)}, DeploymentID: depV2}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{"172.18.0.2:5432"}, dialed, "one dial through the proxy's edge address")
	require.Len(t, r.px.applies, 1, "applied once")
}

func TestSyncRoutesTCPVerifyFailureRollsBack(t *testing.T) {
	r, _ := newV2Rig(t, func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") })
	table := []agentprotocol.Route{tcpL4(routeSvc)}
	res, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table, DeploymentID: depV2}, nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome)
	require.Contains(t, pr.Message, "tcp port 5432")
	require.Empty(t, r.px.Current(), "the empty previous table is restored")
}

func TestSyncRoutesReDeployVerifiesEveryExternalRouteWhenNothingChanged(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	table := []agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80"), {ServiceID: "other", Hosts: []string{"o.localhost"}, Upstreams: []string{"nc-o-aaaaaaaa-0:80"}}}
	require.NoError(t, r.px.Apply(table))
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table, DeploymentID: depV2}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome)
	require.Equal(t, []string{"nas.example.com/"}, cp.probed, "only the external route is re-verified, not managed services")
}

func authRoute() agentprotocol.Route {
	r := extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")
	r.Domains = []agentprotocol.DomainRule{{
		ID: domID, Host: "nas.example.com", AuthRef: authSecret, Options: agentprotocol.RouteOptions{BasicAuth: true},
	}}
	return r
}

func TestSyncRoutesResolvesAuthSecretsAndNeverAppliesWithoutThem(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: []agentprotocol.Route{authRoute()}, DeploymentID: depV2},
		map[string]string{authSecret: "alice:" + authHash + "\n"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	got := r.px.Current()
	require.Equal(t, []string{"alice:" + authHash}, got[0].Domains[0].AuthUsers)
	require.NotNil(t, cp)

	// No secret: the table is not applied and the failure carries no hash.
	r2, _ := newV2Rig(t, nil)
	res, pr := r2.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: []agentprotocol.Route{authRoute()}, DeploymentID: depV2}, map[string]string{})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Empty(t, r2.px.applies, "never applied unprotected")
	require.Contains(t, pr.Message, domID)
	require.Contains(t, r2.snd.eventSteps(), "route:failed")
}

func TestProxyConfigureInstallsConfigAppliesAndReports(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	cp.last = agentprotocol.ProxyConfigureResult{
		Restarted:    true,
		Certificates: []agentprotocol.CertStatus{{ID: "c1", Loaded: true}},
		Tailnet:      &agentprotocol.TailnetStatus{Mode: agentprotocol.TailnetManaged, State: agentprotocol.TailnetStateOnline, DeviceID: "nXYZ"},
	}
	current := []agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")}
	require.NoError(t, r.px.Apply(current))
	r.px.applies = nil

	payload := agentprotocol.ProxyConfigurePayload{
		Proxy: agentprotocol.ProxyConfig{
			UpstreamPolicy: agentprotocol.UpstreamPolicy{AllowHostGateway: true},
			Certificates:   []agentprotocol.CertRef{{ID: "c1", Name: "wild", CertPEM: "CERT", KeyRef: agentprotocol.SecretCertPrefix + "c1"}},
			Tailnet:        &agentprotocol.TailnetSpec{Mode: agentprotocol.TailnetManaged, Hostname: "nebula-a", Provision: true},
		},
		ACMEEmail: "ops@example.com",
	}
	res, pr := r.command(agentprotocol.KindProxyConfigure, payload, map[string]string{
		agentprotocol.SecretCertPrefix + "c1": "KEY-PEM", agentprotocol.SecretTailnetAuthKey: "tskey-auth-abc",
	})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, pr.Message)

	require.Len(t, cp.cfgs, 1)
	require.Equal(t, "KEY-PEM", cp.cfgs[0].Certificates[0].KeyPEM, "key resolved from the command's secrets")
	require.Equal(t, []string{"tskey-auth-abc"}, cp.keys)
	require.Equal(t, "ops@example.com", r.px.email)
	require.Equal(t, [][]agentprotocol.Route{current}, r.px.applies, "the agent's own table is re-applied")

	require.True(t, pr.Restarted)
	require.Equal(t, cp.last.Certificates, pr.Certificates)
	require.Equal(t, "nXYZ", pr.Tailnet.DeviceID)
	require.Contains(t, pr.Message, "restarted")
}

func TestProxyConfigureReplacesRoutesWhenSent(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{extHTTP(routeSvc, "old.example.com", "192.168.1.20:80")}))
	r.px.applies = nil

	newTable := []agentprotocol.Route{authRoute()}
	_, pr := r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{
		Proxy: agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}, Routes: newTable,
	}, map[string]string{authSecret: "alice:" + authHash})
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{"alice:" + authHash}, r.px.Current()[0].Domains[0].AuthUsers)

	// ReplaceRoutes with an empty table clears it.
	_, pr = r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{
		Proxy: agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}, ReplaceRoutes: true,
	}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Empty(t, r.px.Current())
}

func TestProxyConfigureRefreshesAuthSecretsOfHeldRoutes(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	held := authRoute()
	held.Domains[0].AuthUsers = []string{"old:" + authHash}
	require.NoError(t, r.px.Apply([]agentprotocol.Route{held}))

	_, pr := r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{Proxy: agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}},
		map[string]string{authSecret: "new:" + authHash})
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{"new:" + authHash}, r.px.Current()[0].Domains[0].AuthUsers)
}

func TestProxyConfigureMissingCertKeyOrApplyErrorFails(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	payload := agentprotocol.ProxyConfigurePayload{Proxy: agentprotocol.ProxyConfig{
		Certificates: []agentprotocol.CertRef{{ID: "c1", Name: "wild", KeyRef: agentprotocol.SecretCertPrefix + "c1"}},
	}}
	res, _ := r.command(agentprotocol.KindProxyConfigure, payload, map[string]string{})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "c1")
	require.Empty(t, cp.cfgs, "an unresolvable configuration is never installed")
	require.Empty(t, r.px.applies)

	r.px.applyErr = func(int) error {
		return errors.New("port 5432 is already in use; the previous configuration was restored")
	}
	res, pr := r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{Proxy: agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}}, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, pr.Message, "5432")
}

func TestProxyConfigureOnProxyWithoutConfigurationSupportFails(t *testing.T) {
	r := newRig(t, nil) // fakeProxy has no SetProxyConfig
	res, _ := r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{Proxy: agentprotocol.ProxyConfig{}}, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "does not accept")
}

func TestReconcileAppliesExternalRoutesProxyConfigAndSecrets(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	svcRoute := agentprotocol.Route{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{oldCtr + ":80"}}
	cfg := &agentprotocol.ProxyConfig{
		UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy(),
		Certificates:   []agentprotocol.CertRef{{ID: "c1", Name: "wild", KeyRef: agentprotocol.SecretCertPrefix + "c1"}},
	}
	err := r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{
		Services:       []agentprotocol.DesiredService{{ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr}, Routes: []agentprotocol.Route{svcRoute}}},
		ExternalRoutes: []agentprotocol.Route{authRoute(), tcpL4(routeSvc2)},
		Proxy:          cfg,
		Secrets:        map[string]string{authSecret: "alice:" + authHash, agentprotocol.SecretCertPrefix + "c1": "KEY"},
	})
	require.NoError(t, err)

	require.Len(t, cp.cfgs, 1)
	require.Equal(t, "KEY", cp.cfgs[0].Certificates[0].KeyPEM)
	table := r.px.Current()
	require.Len(t, table, 3, "the service route plus both route-service routes")
	ids := []string{table[0].ServiceID, table[1].ServiceID, table[2].ServiceID}
	require.ElementsMatch(t, []string{svcID, routeSvc, routeSvc2}, ids)
	for _, rt := range table {
		if rt.ServiceID == routeSvc {
			require.Equal(t, []string{"alice:" + authHash}, rt.Domains[0].AuthUsers)
		}
	}
	require.NotContains(t, r.snd.eventSteps(), "route:failed")
}

func TestReconcileKeepsExternalUpstreamsWhenNotManagedContainers(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	multi := extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")
	multi.Upstreams = []string{"192.168.1.20:80", "192.168.1.21:80", "nas.lan:80"}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{ExternalRoutes: []agentprotocol.Route{multi}}))
	require.Equal(t, multi.Upstreams, r.px.Current()[0].Upstreams, "only managed replica containers can be dropped as dead")
}

func TestReconcileDoesNotApplyWhenSecretsOrConfigAreMissing(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	prev := []agentprotocol.Route{extHTTP(routeSvc2, "keep.example.com", "192.168.1.9:80")}
	require.NoError(t, r.px.Apply(prev))
	r.px.applies = nil

	// Missing basic-auth secret.
	err := r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{ExternalRoutes: []agentprotocol.Route{authRoute()}, Secrets: map[string]string{}})
	require.Error(t, err)
	require.Contains(t, err.Error(), domID)
	require.Empty(t, r.px.applies, "the previous table keeps serving")
	require.Equal(t, prev, r.px.Current())

	// Missing certificate key: config not installed, table not applied.
	err = r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{
		ExternalRoutes: prev,
		Proxy:          &agentprotocol.ProxyConfig{Certificates: []agentprotocol.CertRef{{ID: "c9", Name: "x", KeyRef: "cert:c9"}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "c9")
	require.Empty(t, cp.cfgs)
	require.Empty(t, r.px.applies)
}

func TestSweepNeverRemovesTheTailnetNodeOrProxy(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	// A tailscale container that (wrongly) carries a service label and is old
	// enough to be an orphan; plus the proxy and a real orphan.
	r.dk.seed(agentprotocol.TailscaleContainer, "svc-ts", "dep-ts", true, time.Hour)
	r.dk.seed(agentprotocol.ProxyContainer, "svc-px", "dep-px", true, time.Hour)
	orphan := r.dk.seed("nc-gone-aaaaaaaa", "svc-gone", "dep-gone", true, time.Hour)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))

	_, tsOK := r.dk.state(agentprotocol.TailscaleContainer)
	_, pxOK := r.dk.state(agentprotocol.ProxyContainer)
	_, orphanOK := r.dk.state(orphan.Name)
	require.True(t, tsOK, "the tailnet node is infrastructure, not an orphan")
	require.True(t, pxOK)
	require.False(t, orphanOK, "real orphans are still swept")
}

func TestDeployResolvesAuthSecretsOfOtherServicesAndAppliesProxyConfig(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	// A fresh proxy has no state: the payload's table seeds it.
	p := basePayload()
	other := authRoute() // a route service on the same server, sent with the deploy table
	p.Routes = append(p.Routes, other)
	p.Proxy = &agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}

	res, dr := r.deploy(p, map[string]string{authSecret: "alice:" + authHash})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, cp.cfgs, 1, "the payload's proxy configuration reaches the proxy")
	var found bool
	for _, rt := range r.px.Current() {
		if rt.ServiceID == routeSvc {
			found = true
			require.Equal(t, []string{"alice:" + authHash}, rt.Domains[0].AuthUsers)
		}
	}
	require.True(t, found)
}

func TestDeployFailsBeforeApplyWhenAuthSecretMissing(t *testing.T) {
	r, _ := newV2Rig(t, nil) // a fresh proxy: the payload's table would seed it
	p := basePayload()
	p.Routes = append(p.Routes, authRoute())

	res, dr := r.deploy(p, nil)
	require.NotEqual(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Contains(t, dr.Message, domID)
	require.Empty(t, r.px.applies, "a table with an unprotected domain is never applied")
	require.Empty(t, r.px.Current())
}

func TestDeployCarriesDomainRulesAndVerifiesFirstServingRule(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	r.previous()
	p := basePayload()
	p.Hosts = []string{"whoami.localhost", "www.whoami.localhost"}
	p.DomainRules = []agentprotocol.DomainRule{
		{ID: domID, Host: "www.whoami.localhost", Kind: agentprotocol.DomainRedirect, RedirectTo: "whoami.localhost"},
		{ID: "aaaaaaaa-1111-4111-8111-111111111111", Host: "whoami.localhost", PathPrefix: "/api"},
	}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	var own agentprotocol.Route
	for _, rt := range r.px.Current() {
		if rt.ServiceID == svcID {
			own = rt
		}
	}
	require.Equal(t, p.DomainRules, own.Domains)
	require.Equal(t, []string{"whoami.localhost/api/"}, cp.probed[:1], "redirect rules are skipped; the serving rule's prefix is probed")
}

// The deploying service's own route carries the origin lock of its payload, so
// a locked domain is locked from its first deployment, not from the next sync.
func TestDeployCarriesTheOriginLock(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.previous()
	p := basePayload()
	p.Hosts = []string{"whoami.localhost"}
	p.DomainRules = []agentprotocol.DomainRule{{ID: "aaaaaaaa-1111-4111-8111-111111111111", Host: "whoami.localhost", OriginLocked: true}}
	p.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: []string{"103.21.244.0/22"}}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	var own agentprotocol.Route
	for _, rt := range r.px.Current() {
		if rt.ServiceID == svcID {
			own = rt
		}
	}
	require.Equal(t, p.OriginLock, own.OriginLock)
	require.True(t, own.Domains[0].OriginLocked)
}

func TestDeployWithOnlyRedirectDomainsSkipsVerification(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	r.previous()
	p := basePayload()
	p.Hosts = []string{"www.whoami.localhost"}
	p.DomainRules = []agentprotocol.DomainRule{{ID: domID, Host: "www.whoami.localhost", Kind: agentprotocol.DomainRedirect, RedirectTo: "whoami.localhost"}}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, cp.probed)
}

func TestVerifyTargetAndOwnRoutes(t *testing.T) {
	h, prefix, ok := verifyTarget(agentprotocol.Route{Hosts: []string{"a.example.com"}})
	require.True(t, ok)
	require.Equal(t, "a.example.com", h)
	require.Empty(t, prefix)

	_, _, ok = verifyTarget(agentprotocol.Route{
		Hosts:    []string{"a.example.com"},
		RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{Host: "a.example.com", Kind: agentprotocol.DomainRedirect}}},
	})
	require.False(t, ok)

	h, _, ok = verifyTarget(agentprotocol.Route{
		Hosts:    []string{"a.example.com", "plain.example.com"},
		RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{Host: "a.example.com", Kind: agentprotocol.DomainRedirect}}},
	})
	require.True(t, ok)
	require.Equal(t, "plain.example.com", h, "a host without a rule is served with defaults")

	table := []agentprotocol.Route{tcpL4(svcID), {ServiceID: svcID, Hosts: []string{"h.example.com"}, Upstreams: []string{"x:1"}}}
	require.Len(t, ownRoutes(table, svcID), 1, "L4 routes are never verified through the probe entrypoint")
}

func TestRoutesEqualComparesAllNewFields(t *testing.T) {
	base := agentprotocol.Route{ServiceID: "s", Hosts: []string{"h"}, Upstreams: []string{"u:1"}}
	same := base
	require.True(t, routesEqual([]agentprotocol.Route{base}, []agentprotocol.Route{same}))
	for name, mut := range map[string]func(*agentprotocol.Route){
		"protocol":   func(r *agentprotocol.Route) { r.Protocol = "tcp" },
		"kind":       func(r *agentprotocol.Route) { r.Kind = "external" },
		"scheme":     func(r *agentprotocol.Route) { r.Scheme = "https" },
		"transport":  func(r *agentprotocol.Route) { r.Transport = &agentprotocol.UpstreamTransport{InsecureSkipVerify: true} },
		"passHost":   func(r *agentprotocol.Route) { r.PassHost = new(false) },
		"domains":    func(r *agentprotocol.Route) { r.Domains = []agentprotocol.DomainRule{{ID: "x", Host: "h"}} },
		"listenPort": func(r *agentprotocol.Route) { r.ListenPort = 5432 },
		"allowCidrs": func(r *agentprotocol.Route) { r.AllowCIDRs = []string{"10.0.0.0/8"} },
		"proxyProto": func(r *agentprotocol.Route) { r.ProxyProtocol = 2 },
		"verifySkip": func(r *agentprotocol.Route) { r.VerifySkip = true },
		"authUsers": func(r *agentprotocol.Route) {
			r.Domains = []agentprotocol.DomainRule{{ID: "x", Host: "h", AuthUsers: []string{"a:b"}}}
		},
	} {
		changed := base
		mut(&changed)
		require.False(t, routesEqual([]agentprotocol.Route{base}, []agentprotocol.Route{changed}), name)
	}
}

func TestRedactorMasksAuthHashesAndKeyLines(t *testing.T) {
	const keyLine = "MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgFAKEKEYLINE0123456789"
	red := newRedactor(map[string]string{
		authSecret:                            "alice:" + authHash + "\nbob:" + authHash + "x",
		agentprotocol.SecretCertPrefix + "c1": "-----BEGIN PRIVATE KEY-----\n" + keyLine + "\n-----END PRIVATE KEY-----\n",
		agentprotocol.SecretTailnetAuthKey:    "tskey-auth-kSECRETxxxxxxxx",
	}, nil)
	for _, leak := range []string{
		"proxy said alice:" + authHash, "hash " + authHash + "x", "line " + keyLine, "key tskey-auth-kSECRETxxxxxxxx",
	} {
		out := red.apply(leak)
		require.NotContains(t, out, authHash)
		require.NotContains(t, out, keyLine)
		require.NotContains(t, out, "kSECRET")
	}
	require.Equal(t, "-----BEGIN PRIVATE KEY-----", red.apply("-----BEGIN PRIVATE KEY-----"), "PEM armor lines stay readable")
}
