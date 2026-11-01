package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const svc2 = "5e5e5e5e-0000-4000-8000-000000000002"

// recorder is an UptimeRecorder that keeps the batches it was handed.
type recorder struct {
	mu      sync.Mutex
	batches [][]agentprotocol.ProbeResult
}

func (r *recorder) Record(rs []agentprotocol.ProbeResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, append([]agentprotocol.ProbeResult(nil), rs...))
}

func (r *recorder) all() [][]agentprotocol.ProbeResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]agentprotocol.ProbeResult(nil), r.batches...)
}

func newUptimeRig(t *testing.T, tweak func(*Config)) (*rig, *recorder) {
	t.Helper()
	rec := &recorder{}
	r := newNetRig(t, nil, func(c *Config) {
		c.Uptime = rec
		if tweak != nil {
			tweak(c)
		}
	})
	return r, rec
}

// statusServer answers every request with status and returns its port.
func statusServer(t *testing.T, status int) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)
	return portOf(t, srv)
}

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return n
}

// closedPort is a loopback port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

var httpCheck = agentprotocol.HealthcheckSpec{Type: "http", Path: "/", TimeoutSeconds: 2}

// seedReplica seeds a running replica of service svc whose spec label carries
// hc and port, reachable at 127.0.0.1 on its environment network.
func seedReplica(r *rig, svc, name string, hc agentprotocol.HealthcheckSpec, port int) *docker.ContainerInfo {
	c := r.dk.seed(name, svc, depV2, true, time.Hour)
	p := basePayload()
	p.ServiceID, p.Healthcheck, p.Port = svc, hc, port
	r.dk.mu.Lock()
	defer r.dk.mu.Unlock()
	c.Labels[agentprotocol.LabelSpec] = specLabelOf(p)
	c.Labels[agentprotocol.LabelEnvironment] = envID
	c.Networks = map[string]docker.ContainerNetwork{netEnv: {IP: "127.0.0.1"}}
	return c
}

// edit changes a seeded container under the fake daemon's lock.
func edit(r *rig, c *docker.ContainerInfo, fn func(*docker.ContainerInfo)) {
	r.dk.mu.Lock()
	defer r.dk.mu.Unlock()
	fn(c)
}

func want(r *rig, svc string, names ...string) {
	r.eng.mu.Lock()
	defer r.eng.mu.Unlock()
	r.eng.desired[svc] = desiredEntry{DeploymentID: depV2, Replicas: len(names), ContainerNames: names}
}

func wantStack(r *rig, svc string) {
	r.eng.mu.Lock()
	defer r.eng.mu.Unlock()
	r.eng.desired[svc] = desiredEntry{DeploymentID: depV2, Replicas: 1, Stack: true}
}

// cycle runs one probe cycle and indexes the results by service.
func cycle(t *testing.T, r *rig) map[string]agentprotocol.ProbeResult {
	t.Helper()
	out := map[string]agentprotocol.ProbeResult{}
	at := time.Now().UTC()
	for _, res := range r.eng.uptimeCycle(context.Background(), at) {
		require.NotContains(t, out, res.ServiceID, "one result per service")
		require.Equal(t, at, res.At)
		out[res.ServiceID] = res
	}
	return out
}

func upName(i int) string { return fmt.Sprintf("nc-web-d2d2d2d2-%d", i) }

func TestUptimeAppOutcomeFollowsTheReplicasHealthChecks(t *testing.T) {
	ok, bad := statusServer(t, 200), statusServer(t, 503)
	cases := []struct {
		name    string
		ports   []int
		outcome string
		passed  int
		errPart string
	}{
		{"all replicas pass", []int{ok, ok}, agentprotocol.ProbeUp, 2, ""},
		{"a single replica", []int{ok}, agentprotocol.ProbeUp, 1, ""},
		{"some replicas pass", []int{ok, bad, ok}, agentprotocol.ProbeDegraded, 2, "HTTP 503"},
		{"no replica passes", []int{bad, bad}, agentprotocol.ProbeDown, 0, "HTTP 503"},
		{"a replica that refuses connections", []int{closedPort(t)}, agentprotocol.ProbeDown, 0, "refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newUptimeRig(t, nil)
			var names []string
			for i, p := range c.ports {
				names = append(names, upName(i))
				seedReplica(r, svcID, upName(i), httpCheck, p)
			}
			want(r, svcID, names...)

			got := cycle(t, r)[svcID]
			require.Equal(t, c.outcome, got.Outcome)
			require.Equal(t, len(c.ports), got.Replicas)
			require.Equal(t, c.passed, got.Passed)
			if c.errPart == "" {
				require.Empty(t, got.Error)
			} else {
				require.Contains(t, got.Error, c.errPart)
			}
		})
	}
}

func TestUptimeTCPCheck(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	tcp := agentprotocol.HealthcheckSpec{Type: "tcp", TimeoutSeconds: 2}
	seedReplica(r, svcID, upName(0), tcp, statusServer(t, 500)) // a 500 still accepts the connection
	seedReplica(r, svcID, upName(1), tcp, closedPort(t))
	want(r, svcID, upName(0), upName(1))

	got := cycle(t, r)[svcID]
	require.Equal(t, agentprotocol.ProbeDegraded, got.Outcome)
	require.Equal(t, 1, got.Passed)
}

func TestUptimeContainerStatesOfAppReplicas(t *testing.T) {
	ok := statusServer(t, 200)
	cases := []struct {
		name    string
		setup   func(r *rig)
		outcome string
		replic  int
		passed  int
		errPart string
	}{
		{"container missing though wanted", func(r *rig) { want(r, svcID, upName(0)) }, agentprotocol.ProbeDown, 1, 0, "does not exist"},
		{"restarting container", func(r *rig) {
			c := seedReplica(r, svcID, upName(0), httpCheck, ok)
			edit(r, c, func(c *docker.ContainerInfo) { c.State, c.Restarting = "restarting", true })
			want(r, svcID, upName(0))
		}, agentprotocol.ProbeDown, 1, 0, "restarting"},
		{"container created but never started", func(r *rig) {
			c := seedReplica(r, svcID, upName(0), httpCheck, ok)
			edit(r, c, func(c *docker.ContainerInfo) { c.State, c.Running = "created", false })
			want(r, svcID, upName(0))
		}, agentprotocol.ProbeDown, 1, 0, "created"},
		{"every replica stopped on purpose: paused", func(r *rig) {
			for i := range 2 {
				c := seedReplica(r, svcID, upName(i), httpCheck, ok)
				edit(r, c, func(c *docker.ContainerInfo) { c.State, c.Running, c.ExitCode = "exited", false, 137 })
			}
			want(r, svcID, upName(0), upName(1))
		}, agentprotocol.ProbePaused, 0, 0, ""},
		{"no replicas wanted: paused", func(r *rig) { want(r, svcID) }, agentprotocol.ProbePaused, 0, 0, ""},
		{"one replica stopped among running ones: degraded", func(r *rig) {
			seedReplica(r, svcID, upName(0), httpCheck, ok)
			c := seedReplica(r, svcID, upName(1), httpCheck, ok)
			edit(r, c, func(c *docker.ContainerInfo) { c.State, c.Running = "exited", false })
			want(r, svcID, upName(0), upName(1))
		}, agentprotocol.ProbeDegraded, 2, 1, "stopped"},
		{"a replica that is gone among running ones: degraded", func(r *rig) {
			seedReplica(r, svcID, upName(0), httpCheck, ok)
			want(r, svcID, upName(0), upName(1))
		}, agentprotocol.ProbeDegraded, 2, 1, "does not exist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newUptimeRig(t, nil)
			c.setup(r)
			got := cycle(t, r)[svcID]
			require.Equal(t, c.outcome, got.Outcome)
			require.Equal(t, c.replic, got.Replicas)
			require.Equal(t, c.passed, got.Passed)
			require.Contains(t, got.Error, c.errPart)
		})
	}
}

func TestUptimeDockerAndExecChecksUseTheDockerHealthStatus(t *testing.T) {
	cases := []struct {
		name        string
		typ         string
		health      string
		startedAgo  time.Duration
		startPeriod int
		outcome     string
		errPart     string
	}{
		{"healthy", "docker", "healthy", time.Hour, 30, agentprotocol.ProbeUp, ""},
		{"exec checks are judged the same", "exec", "healthy", time.Hour, 30, agentprotocol.ProbeUp, ""},
		{"unhealthy", "docker", "unhealthy", time.Hour, 30, agentprotocol.ProbeDown, "unhealthy"},
		{"unhealthy exec", "exec", "unhealthy", time.Hour, 30, agentprotocol.ProbeDown, "unhealthy"},
		{"starting within the start period passes", "docker", "starting", 10 * time.Second, 30, agentprotocol.ProbeUp, ""},
		{"starting beyond the start period fails", "docker", "starting", 31 * time.Second, 30, agentprotocol.ProbeDown, "still starting"},
		{"starting without a start period fails", "exec", "starting", time.Second, 0, agentprotocol.ProbeDown, "still starting"},
		{"image without HEALTHCHECK: running is enough", "docker", "", time.Hour, 30, agentprotocol.ProbeUp, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newUptimeRig(t, nil)
			hc := agentprotocol.HealthcheckSpec{Type: c.typ, StartPeriodSeconds: c.startPeriod}
			ctr := seedReplica(r, svcID, upName(0), hc, 80)
			edit(r, ctr, func(i *docker.ContainerInfo) { i.Health, i.StartedAt = c.health, time.Now().Add(-c.startedAgo) })
			want(r, svcID, upName(0))

			got := cycle(t, r)[svcID]
			require.Equal(t, c.outcome, got.Outcome)
			require.Contains(t, got.Error, c.errPart)
			require.Zero(t, got.LatencyMs, "no network probe")
			require.Empty(t, r.dk.connects, "no network access needed")
		})
	}
}

func TestUptimeNoneCheckAndChecksWithoutAPortMeanRunning(t *testing.T) {
	for name, hc := range map[string]agentprotocol.HealthcheckSpec{
		"none":                   {Type: "none"},
		"empty type":             {},
		"http without a port":    {Type: "http"},
		"tcp without a port":     {Type: "tcp"},
		"a type the agent lacks": {Type: "carrier-pigeon"},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newUptimeRig(t, nil)
			seedReplica(r, svcID, upName(0), hc, 0)
			want(r, svcID, upName(0))
			got := cycle(t, r)[svcID]
			require.Equal(t, agentprotocol.ProbeUp, got.Outcome, got.Error)
		})
	}

	// "none" still needs a running container.
	r, _ := newUptimeRig(t, nil)
	c := seedReplica(r, svcID, upName(0), agentprotocol.HealthcheckSpec{Type: "none"}, 0)
	edit(r, c, func(c *docker.ContainerInfo) { c.Restarting = true })
	want(r, svcID, upName(0))
	require.Equal(t, agentprotocol.ProbeDown, cycle(t, r)[svcID].Outcome)
}

func TestUptimeContainerWithoutSpecLabelIsJudgedByRunning(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	r.dk.seed(upName(0), svcID, depV2, true, time.Hour) // carries no spec label
	want(r, svcID, upName(0))
	require.Equal(t, agentprotocol.ProbeUp, cycle(t, r)[svcID].Outcome)
}

func TestUptimeFencedServiceIsPaused(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	seedReplica(r, svcID, upName(0), httpCheck, closedPort(t)) // would be down
	want(r, svcID, upName(0))
	r.eng.repl.mu.Lock()
	r.eng.repl.fenced = map[string]bool{svcID: true}
	r.eng.repl.mu.Unlock()

	require.Equal(t, agentprotocol.ProbePaused, cycle(t, r)[svcID].Outcome)
}

func TestUptimeSkipsServicesTheControlPlaneDisabled(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	for _, svc := range []string{svcID, svc2} {
		name := "nc-" + svc[:8] + "-d2d2d2d2-0"
		seedReplica(r, svc, name, agentprotocol.HealthcheckSpec{Type: "none"}, 0)
		want(r, svc, name)
	}
	require.Len(t, cycle(t, r), 2, "nil settings: every service is probed")

	frame := func(up *agentprotocol.UptimeSettings) agentprotocol.Reconcile {
		var ds []agentprotocol.DesiredService
		for _, svc := range []string{svcID, svc2} {
			ds = append(ds, agentprotocol.DesiredService{ServiceID: svc, DeploymentID: depV2, Replicas: 1, ContainerNames: []string{"nc-" + svc[:8] + "-d2d2d2d2-0"}})
		}
		return agentprotocol.Reconcile{Services: ds, Uptime: up}
	}

	require.NoError(t, r.eng.Reconcile(context.Background(), frame(&agentprotocol.UptimeSettings{Disabled: []string{svc2}})))
	r.eng.bg.Wait()
	got := cycle(t, r)
	require.Len(t, got, 1)
	require.Contains(t, got, svcID)

	require.NoError(t, r.eng.Reconcile(context.Background(), frame(&agentprotocol.UptimeSettings{Disabled: []string{svcID, svc2}})))
	r.eng.bg.Wait()
	require.Empty(t, cycle(t, r), "everything switched off")

	// An older control plane sends no settings: back to probing every service.
	require.NoError(t, r.eng.Reconcile(context.Background(), frame(nil)))
	r.eng.bg.Wait()
	require.Len(t, cycle(t, r), 2)

	// An empty list means nothing is disabled either.
	require.NoError(t, r.eng.Reconcile(context.Background(), frame(&agentprotocol.UptimeSettings{})))
	r.eng.bg.Wait()
	require.Len(t, cycle(t, r), 2)
}

func TestUptimeDisabledRouteServiceIsSkipped(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{{ServiceID: "route-a", Upstreams: []string{"127.0.0.1:" + strconv.Itoa(statusServer(t, 200))}}}))
	require.Len(t, cycle(t, r), 1)
	r.eng.up.apply(&agentprotocol.UptimeSettings{Disabled: []string{"route-a"}})
	require.Empty(t, cycle(t, r))
}

func TestUptimeRouteServices(t *testing.T) {
	ok, bad, closed := statusServer(t, 200), statusServer(t, 503), closedPort(t)
	up := func(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(tlsSrv.Close)
	https := func(tr *agentprotocol.UpstreamTransport) agentprotocol.RouteExt {
		return agentprotocol.RouteExt{Kind: agentprotocol.RouteKindExternal, Scheme: "https", Transport: tr}
	}

	r, _ := newUptimeRig(t, nil)
	// An app service and a stack route to prove they are not route services.
	seedReplica(r, svcID, upName(0), agentprotocol.HealthcheckSpec{Type: "none"}, 0)
	want(r, svcID, upName(0))
	wantStack(r, svc2)
	r.dk.seed("nc-stack-web", svc2, depV2, true, time.Hour).Labels[agentprotocol.LabelComponent] = "web"

	routes := []agentprotocol.Route{
		{ServiceID: "r-http", Upstreams: []string{up(ok), up(ok)}, HealthPath: "/health"},
		{ServiceID: "r-503", Upstreams: []string{up(bad)}, HealthPath: "/"},
		{ServiceID: "r-tcp", Upstreams: []string{up(bad)}}, // no health path: connecting is the check
		{ServiceID: "r-closed", Upstreams: []string{up(closed)}},
		{ServiceID: "r-partial", Upstreams: []string{up(ok), up(closed)}},
		{ServiceID: "r-garbage", Upstreams: []string{"not-an-address"}},
		{ServiceID: "r-l4", Upstreams: []string{up(ok)}, RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432, Kind: agentprotocol.RouteKindExternal}},
		{ServiceID: "r-udp", Upstreams: []string{up(ok)}, RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoUDP, ListenPort: 5353}},
		{ServiceID: "r-tls", Upstreams: []string{up(portOf(t, tlsSrv))}, HealthPath: "/", RouteExt: https(&agentprotocol.UpstreamTransport{InsecureSkipVerify: true})},
		{ServiceID: "r-tls-untrusted", Upstreams: []string{up(portOf(t, tlsSrv))}, HealthPath: "/", RouteExt: https(nil)},
		{ServiceID: svcID, Hosts: []string{"app.example.com"}, Upstreams: []string{upName(0) + ":80"}}, // an app service's route
		{ServiceID: "nc-stack-route", Stack: svc2, Upstreams: []string{"nc-stack-web:80"}},
	}
	require.NoError(t, r.px.Apply(routes))

	got := cycle(t, r)
	type exp struct {
		outcome        string
		replicas, pass int
		err            string
	}
	for id, e := range map[string]exp{
		"r-http":          {agentprotocol.ProbeUp, 2, 2, ""},
		"r-503":           {agentprotocol.ProbeDown, 1, 0, "HTTP 503"},
		"r-tcp":           {agentprotocol.ProbeUp, 1, 1, ""},
		"r-closed":        {agentprotocol.ProbeDown, 1, 0, "refused"},
		"r-partial":       {agentprotocol.ProbeDegraded, 2, 1, "refused"},
		"r-garbage":       {agentprotocol.ProbeDown, 1, 0, "invalid upstream"},
		"r-l4":            {agentprotocol.ProbeUp, 1, 1, ""},
		"r-tls":           {agentprotocol.ProbeUp, 1, 1, ""},
		"r-tls-untrusted": {agentprotocol.ProbeDown, 1, 0, "certificate"},
	} {
		res, found := got[id]
		require.True(t, found, id)
		require.Equal(t, e.outcome, res.Outcome, "%s: %s", id, res.Error)
		require.Equal(t, e.replicas, res.Replicas, id)
		require.Equal(t, e.pass, res.Passed, id)
		require.Contains(t, res.Error, e.err, id)
	}
	require.NotContains(t, got, "r-udp", "udp cannot be probed")
	require.NotContains(t, got, "nc-stack-route", "a stack's routes belong to the stack")
	require.Contains(t, got, svcID)
	require.Contains(t, got, svc2)
	require.Len(t, got, 11, "9 route services + the app + the stack")
}

func TestUptimeRouteServiceMidDeployIsNotProbed(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{{ServiceID: "r-new", Upstreams: []string{"127.0.0.1:" + strconv.Itoa(closedPort(t))}}}))
	unlock := r.eng.lockService("r-new") // a deploy is putting its route in place
	require.Empty(t, cycle(t, r))
	unlock()
	require.Len(t, cycle(t, r), 1)
}

func TestUptimeStacksJudgeEveryMemberContainer(t *testing.T) {
	type member struct {
		running bool
		exit    int
		health  string
		state   string
		started time.Duration
		restart bool
	}
	run := member{running: true, started: time.Hour}
	cases := []struct {
		name    string
		members []member
		outcome string
		replic  int
		passed  int
		errPart string
	}{
		{"all members running", []member{run, run}, agentprotocol.ProbeUp, 2, 2, ""},
		{"healthy members", []member{{running: true, health: "healthy", started: time.Hour}, run}, agentprotocol.ProbeUp, 2, 2, ""},
		{"an unhealthy member", []member{run, {running: true, health: "unhealthy", started: time.Hour}}, agentprotocol.ProbeDegraded, 2, 1, "unhealthy"},
		{"a crashed member", []member{run, {state: "exited", exit: 1}}, agentprotocol.ProbeDegraded, 2, 1, "exited with code 1"},
		{"a restarting member", []member{run, {running: true, restart: true, started: time.Hour}}, agentprotocol.ProbeDegraded, 2, 1, "restarting"},
		{"a completed one-shot member does not count against the stack", []member{run, {state: "exited", exit: 0}}, agentprotocol.ProbeUp, 2, 2, ""},
		{"starting just after the start", []member{{running: true, health: "starting", started: 10 * time.Second}}, agentprotocol.ProbeUp, 1, 1, ""},
		{"starting for too long", []member{{running: true, health: "starting", started: time.Hour}}, agentprotocol.ProbeDown, 1, 0, "still starting"},
		{"everything crashed", []member{{state: "exited", exit: 1}, {state: "exited", exit: 2}}, agentprotocol.ProbeDown, 2, 0, "exited with code 1"},
		{"stopped with SIGTERM", []member{{state: "exited", exit: 143}, {state: "exited", exit: 143}}, agentprotocol.ProbePaused, 0, 0, ""},
		{"stopped with SIGKILL and finished cleanly", []member{{state: "exited", exit: 137}, {state: "exited", exit: 0}}, agentprotocol.ProbePaused, 0, 0, ""},
		{"one member killed, another crashed", []member{{state: "exited", exit: 137}, {state: "exited", exit: 1}}, agentprotocol.ProbeDown, 2, 0, "is stopped"},
		{"a stopped member among running ones", []member{run, {state: "exited", exit: 137}}, agentprotocol.ProbeDegraded, 2, 1, "stopped"},
		{"no containers though the stack is wanted", nil, agentprotocol.ProbeDown, 1, 0, "no containers"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newUptimeRig(t, nil)
			for i, m := range c.members {
				ctr := r.dk.seed(fmt.Sprintf("nc-5e5e5e5e-c%d", i), svcID, depV2, false, time.Hour)
				edit(r, ctr, func(i2 *docker.ContainerInfo) {
					i2.Labels[agentprotocol.LabelComponent] = fmt.Sprintf("c%d", i)
					i2.Labels[agentprotocol.LabelReplica] = strconv.Itoa(i)
					if m.running {
						i2.State, i2.Running, i2.StartedAt, i2.Health = "running", true, time.Now().Add(-m.started), m.health
						i2.Restarting = m.restart
					} else {
						i2.State, i2.ExitCode = m.state, m.exit
					}
				})
			}
			wantStack(r, svcID)

			got := cycle(t, r)[svcID]
			require.Equal(t, c.outcome, got.Outcome, got.Error)
			require.Equal(t, c.replic, got.Replicas)
			require.Equal(t, c.passed, got.Passed)
			require.Contains(t, got.Error, c.errPart)
		})
	}
}

func TestUptimeStackOfAnotherDeploymentIsIgnored(t *testing.T) {
	r, _ := newUptimeRig(t, nil)
	old := r.dk.seed("nc-5e5e5e5e-old", svcID, depV1, true, time.Hour)
	edit(r, old, func(i *docker.ContainerInfo) { i.Labels[agentprotocol.LabelComponent] = "web" })
	wantStack(r, svcID)
	got := cycle(t, r)[svcID]
	require.Equal(t, agentprotocol.ProbeDown, got.Outcome, "the current deployment has no containers")
}

// ---- network access of a containerized agent ----

func TestUptimeCycleHoldsNetworkAccessWhileProbingAndReleasesAfter(t *testing.T) {
	r, _ := newUptimeRig(t, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")
	addNet(r, netEnv, false, time.Hour)

	// While a probe is in flight the network sweeper runs (as it does every
	// minute). The agent holds a reference on the network for the whole cycle,
	// so the sweeper must leave it attached.
	var (
		mu       sync.Mutex
		duringIn []string
		sweepErr error
		hits     int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		err := r.eng.sweepNetworks(context.Background())
		mu.Lock()
		sweepErr = err
		duringIn = r.dk.networksOf(agentCtr)
		hits++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	for i := range 3 { // three replicas, two services: one join
		seedReplica(r, svcID, upName(i), httpCheck, portOf(t, srv))
	}
	want(r, svcID, upName(0), upName(1), upName(2))
	seedReplica(r, svc2, "nc-svc2-d2d2d2d2-0", httpCheck, portOf(t, srv))
	want(r, svc2, "nc-svc2-d2d2d2d2-0")

	got := cycle(t, r)
	require.Equal(t, agentprotocol.ProbeUp, got[svcID].Outcome, got[svcID].Error)
	require.Equal(t, agentprotocol.ProbeUp, got[svc2].Outcome, got[svc2].Error)

	mu.Lock()
	require.NoError(t, sweepErr)
	require.Equal(t, 4, hits)
	require.Contains(t, duringIn, netEnv, "the sweeper did not detach the agent mid-cycle")
	mu.Unlock()
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.connects, "one join for the whole cycle")
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.disconnects, "left once, after the last probe")
	require.Equal(t, []string{agentprotocol.EdgeNetwork}, r.dk.networksOf(agentCtr))
}

func TestUptimeCycleJoinsEveryNetworkItProbesThrough(t *testing.T) {
	r, _ := newUptimeRig(t, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")
	addNet(r, netEnv, false, time.Hour)
	addNet(r, netInt, true, time.Hour)
	ok := statusServer(t, 200)

	seedReplica(r, svcID, upName(0), httpCheck, ok)
	// A service that blocks egress sits on the internal network only.
	blocked := seedReplica(r, svc2, "nc-svc2-d2d2d2d2-0", httpCheck, ok)
	p := basePayload()
	p.ServiceID, p.Healthcheck, p.Port, p.BlockEgress = svc2, httpCheck, ok, true
	edit(r, blocked, func(c *docker.ContainerInfo) {
		c.Labels[agentprotocol.LabelSpec] = specLabelOf(p)
		c.Networks = map[string]docker.ContainerNetwork{netInt: {IP: "127.0.0.1"}}
	})
	want(r, svcID, upName(0))
	want(r, svc2, "nc-svc2-d2d2d2d2-0")

	got := cycle(t, r)
	require.Equal(t, agentprotocol.ProbeUp, got[svcID].Outcome, got[svcID].Error)
	require.Equal(t, agentprotocol.ProbeUp, got[svc2].Outcome, got[svc2].Error)
	require.ElementsMatch(t, []string{netEnv + "<-" + agentCtr, netInt + "<-" + agentCtr}, r.dk.connects)
	require.ElementsMatch(t, r.dk.connects, r.dk.disconnects)
}

func TestUptimeCycleNeedsNoNetworkWithoutANetworkProbe(t *testing.T) {
	r, _ := newUptimeRig(t, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")
	addNet(r, netEnv, false, time.Hour)
	seedReplica(r, svcID, upName(0), agentprotocol.HealthcheckSpec{Type: "docker"}, 80)
	want(r, svcID, upName(0))
	require.NoError(t, r.px.Apply([]agentprotocol.Route{{ServiceID: "r-ext", Upstreams: []string{"127.0.0.1:" + strconv.Itoa(statusServer(t, 200))}}}))

	got := cycle(t, r)
	require.Equal(t, agentprotocol.ProbeUp, got[svcID].Outcome)
	require.Equal(t, agentprotocol.ProbeUp, got["r-ext"].Outcome, "an external upstream is reached from the agent's own networks")
	require.Empty(t, r.dk.connects)
}

func TestUptimeCycleOnABareHostNeverAttachesTheAgent(t *testing.T) {
	r, _ := newUptimeRig(t, func(c *Config) { c.Hostname = "my-laptop" })
	seedAgent(r, "agent-id")
	addNet(r, netEnv, false, time.Hour)
	seedReplica(r, svcID, upName(0), httpCheck, statusServer(t, 200))
	want(r, svcID, upName(0))

	require.Equal(t, agentprotocol.ProbeUp, cycle(t, r)[svcID].Outcome)
	require.Empty(t, r.dk.connects)
	require.Empty(t, r.dk.disconnects)
}

func TestUptimeRouteServiceNetworksAreHeldToo(t *testing.T) {
	r, _ := newUptimeRig(t, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")
	addNet(r, netEnv, false, time.Hour)
	// A route service whose upstream is a managed container on the env network.
	require.NoError(t, r.px.Apply([]agentprotocol.Route{{
		ServiceID: "r-managed", Upstreams: []string{"127.0.0.1:" + strconv.Itoa(statusServer(t, 200))}, Networks: []string{netEnv},
	}}))
	require.Equal(t, agentprotocol.ProbeUp, cycle(t, r)["r-managed"].Outcome)
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.connects)
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.disconnects)
}

// ---- recording ----

func TestProbeUptimeRecordsOneBatchPerCycle(t *testing.T) {
	r, rec := newUptimeRig(t, nil)
	for _, svc := range []string{svcID, svc2} {
		name := "nc-" + svc[:8] + "-d2d2d2d2-0"
		seedReplica(r, svc, name, agentprotocol.HealthcheckSpec{Type: "none"}, 0)
		want(r, svc, name)
	}
	before := time.Now().UTC()
	r.eng.probeUptime(context.Background())

	batches := rec.all()
	require.Len(t, batches, 1)
	require.Len(t, batches[0], 2)
	require.Equal(t, batches[0][0].At, batches[0][1].At, "a cycle shares one timestamp")
	require.False(t, batches[0][0].At.Before(before))
	require.Equal(t, svcID, batches[0][0].ServiceID, "results come in service order")
}

func TestProbeUptimeDropsACycleCutShortByShutdown(t *testing.T) {
	r, rec := newUptimeRig(t, nil)
	seedReplica(r, svcID, upName(0), httpCheck, closedPort(t))
	want(r, svcID, upName(0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.eng.probeUptime(ctx)
	require.Empty(t, rec.all(), "probes that failed because the agent is stopping are not outages")
}

func TestProbeUptimeWithNothingToProbeRecordsNothingUseful(t *testing.T) {
	r, rec := newUptimeRig(t, nil)
	r.eng.probeUptime(context.Background())
	for _, b := range rec.all() {
		require.Empty(t, b)
	}
}
