package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// healthRecorder is a HealthFunc that records which replicas were checked and
// fails the one named failName.
type healthRecorder struct {
	mu       sync.Mutex
	checked  []string
	failName string
}

func (h *healthRecorder) wait(_ context.Context, t health.Target) health.Result {
	name := t.Addrs[len(t.Addrs)-1] // the DNS fallback is the container name
	h.mu.Lock()
	h.checked = append(h.checked, name)
	h.mu.Unlock()
	if name == h.failName {
		return health.Result{Reason: "connection refused"}
	}
	return health.Result{Healthy: true}
}

func (h *healthRecorder) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string(nil), h.checked...)
	sort.Strings(out)
	return out
}

func replicaName(i int) string { return agentprotocol.ContainerName("web", depV2, i) }

func (r *rig) scale(p agentprotocol.DeployPayload, replicas int) (agentprotocol.ResultFrame, agentprotocol.DeployResult) {
	r.t.Helper()
	return r.scaleAt(p, replicas, time.Time{})
}

// scaleAt is a scale_service command the control plane stamped with at (the
// service's last_scale_at when it stored the count).
func (r *rig) scaleAt(p agentprotocol.DeployPayload, replicas int, at time.Time) (agentprotocol.ResultFrame, agentprotocol.DeployResult) {
	r.t.Helper()
	raw, err := json.Marshal(agentprotocol.ScaleServicePayload{
		ServiceID: svcID, DeploymentID: p.DeploymentID, Replicas: replicas, ScaledAt: at, Deploy: p,
	})
	require.NoError(r.t, err)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "cmd-scale", Kind: agentprotocol.KindScaleService, Payload: raw})
	var dr agentprotocol.DeployResult
	require.NoError(r.t, json.Unmarshal(res.Data, &dr))
	return res, dr
}

func upstreamsOf(t *testing.T, px *fakeProxy) []string {
	t.Helper()
	var out []string
	for _, rt := range px.Current() {
		if rt.ServiceID == svcID {
			out = append(out, rt.Upstreams...)
		}
	}
	return out
}

func TestContainerNamesAreReplicaIndexed(t *testing.T) {
	require.Equal(t, "nc-web-d2d2d2d2-0", agentprotocol.ContainerName("web", depV2, 0))
	require.Equal(t, []string{"nc-web-d2d2d2d2-0", "nc-web-d2d2d2d2-1", "nc-web-d2d2d2d2-2"}, agentprotocol.ContainerNames("web", depV2, 3))
	require.Len(t, agentprotocol.ContainerNames("web", depV2, 0), 1, "never fewer than one replica")
}

func TestRollingDeployStartsAllReplicasAndRoutesTheFullSet(t *testing.T) {
	h := &healthRecorder{}
	r := newRig(t, h.wait)
	r.previous()
	p := basePayload()
	p.Replicas = 3

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	for i := range 3 {
		require.True(t, running(t, r.dk, replicaName(i)), "replica %d runs", i)
	}
	require.Equal(t, agentprotocol.ContainerNames("web", depV2, 3), h.names(), "every replica is health-checked")
	_, oldExists := r.dk.state(oldCtr)
	require.False(t, oldExists, "old release retired after the switch")

	require.Len(t, r.dk.created, 3)
	for i, spec := range r.dk.created {
		require.Equal(t, fmt.Sprint(i), spec.Labels[agentprotocol.LabelReplica])
		require.Equal(t, []string{"web"}, spec.Networks[0].Aliases, "all replicas share the service alias")
	}

	require.Len(t, r.px.applies, 1, "one route switch for the whole set")
	route := r.px.applies[0][1]
	require.Equal(t, svcID, route.ServiceID)
	require.Equal(t, []string{
		replicaName(0) + ":80", replicaName(1) + ":80", replicaName(2) + ":80",
	}, route.Upstreams)
	require.Equal(t, "/", route.HealthPath, "http healthcheck becomes the Traefik health check")
	require.Contains(t, dr.Message, "3 replicas")

	ent := r.eng.local[svcID]
	require.Equal(t, 3, ent.Replicas)
	require.Equal(t, agentprotocol.ContainerNames("web", depV2, 3), ent.ContainerNames)
}

// A single-process service switches replica sets with one route table in which
// its route keeps the service's own ID (no Stack, no second route) and names
// only the new replicas. The old replicas keep running until that table is
// live and stop only afterwards, so the proxy's switch is the one moment
// traffic moves from v1 to v2; nothing in the agent re-adds v1 later.
func TestRollingDeploySwitchesOneRouteBeforeRetiringTheOldReplicas(t *testing.T) {
	r := newRig(t, nil)
	oldNames := agentprotocol.ContainerNames("web", depV1, 3)
	var oldUps []string
	for _, n := range oldNames {
		r.dk.seed(n, svcID, depV1, true, time.Hour)
		oldUps = append(oldUps, n+":80")
	}
	require.NoError(t, r.px.Apply([]agentprotocol.Route{
		{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}},
		{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: oldUps, HealthPath: "/"},
	}))
	r.px.applies = nil

	// What the proxy serves at the moment each old replica is stopped.
	var servedAtStop [][]string
	r.dk.onStop = func(name string) {
		if !slices.Contains(oldNames, name) {
			return
		}
		var ups []string
		for _, rt := range r.px.Current() {
			if rt.ServiceID == svcID {
				ups = append(ups, rt.Upstreams...)
			}
		}
		servedAtStop = append(servedAtStop, ups)
	}
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	require.Len(t, r.px.applies, 1, "one table replaces the route: no interim table with both releases")
	newUps := []string{replicaName(0) + ":80", replicaName(1) + ":80", replicaName(2) + ":80"}
	var own []agentprotocol.Route
	for _, rt := range r.px.applies[0] {
		if rt.ServiceID == svcID || rt.Stack == svcID {
			own = append(own, rt)
		}
	}
	require.Len(t, own, 1, "the service has exactly one route")
	require.Empty(t, own[0].Stack, "a plain service's route is not a process route")
	require.Equal(t, newUps, own[0].Upstreams, "only the new replicas are upstreams")

	require.Len(t, servedAtStop, 3, "every old replica is stopped")
	for _, ups := range servedAtStop {
		require.Equal(t, newUps, ups, "an old replica stops only after the proxy serves the new set")
	}
}

func TestReplicaHealthFailureRemovesAllNewReplicasAndKeepsOldServing(t *testing.T) {
	h := &healthRecorder{failName: replicaName(1)}
	r := newRig(t, h.wait)
	prev := r.previous()
	r.dk.logText[replicaName(1)] = "fatal: cannot connect to database\n"
	p := basePayload()
	p.Replicas = 3

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Replica 1 of 3")
	require.Contains(t, dr.Message, "connection refused")
	require.Contains(t, dr.Message, "Previous release is still serving")
	require.Contains(t, dr.Message, "fatal: cannot connect to database", "log tail of the failing replica")

	for i := range 3 {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists, "new replica %d removed", i)
	}
	require.True(t, running(t, r.dk, oldCtr), "old container untouched")
	require.Empty(t, r.dk.stopped, "old container never stopped")
	require.Empty(t, r.px.applies, "routes never changed")
	require.Equal(t, prev, r.px.Current())
}

func TestReplicaStartFailureRemovesAlreadyCreatedReplicas(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	r.dk.startErr = func(name string) error {
		if name == replicaName(2) {
			return fmt.Errorf("driver failed programming external connectivity")
		}
		return nil
	}
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "start container "+replicaName(2))
	for i := range 3 {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists)
	}
	require.True(t, running(t, r.dk, oldCtr))
}

func TestVerifyFailureWithReplicasRestoresPreviousRoutes(t *testing.T) {
	r := newRig(t, nil)
	prev := r.previous()
	r.px.probes = []int{502}
	p := basePayload()
	p.Replicas = 2

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Equal(t, prev, r.px.Current())
	for i := range 2 {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists)
	}
	require.True(t, running(t, r.dk, oldCtr))
	// One probe per upstream per try, stopping at the first failure of a try.
	require.Equal(t, 3, r.px.nProbe)
}

func TestRecreateRunsASingleReplica(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	p := basePayload()
	p.Strategy = agentprotocol.StrategyRecreate
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 1)
	require.Equal(t, replicaName(0), r.dk.created[0].Name)
	require.Len(t, r.px.applies[0][1].Upstreams, 1)
}

func TestVolumesForceRecreateAndOneReplica(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 2
	p.Volumes = []agentprotocol.VolumeMount{{Name: "nc-vol-5e5e5e5e-data", MountPath: "/data"}}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 1)
}

func TestNonHTTPHealthcheckSetsNoTraefikHealthPath(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Healthcheck = agentprotocol.HealthcheckSpec{Type: "tcp", DeadlineSeconds: 5}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, r.px.applies[0][len(r.px.applies[0])-1].HealthPath)

	// An http check on another port cannot be expressed as a Traefik health check.
	r2 := newRig(t, nil)
	p2 := basePayload()
	p2.Healthcheck = agentprotocol.HealthcheckSpec{Type: "http", Path: "/up", Port: 9000, DeadlineSeconds: 5}
	res, dr = r2.deploy(p2, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, r2.px.applies[0][len(r2.px.applies[0])-1].HealthPath)
}

// ---- scale ----

func TestScaleUpAddsTwoHealthCheckedUpstreams(t *testing.T) {
	h := &healthRecorder{}
	r := newRig(t, h.wait)
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	h.mu.Lock()
	h.checked = nil
	h.mu.Unlock()
	eventsBefore := len(r.snd.events)
	createdBefore := len(r.dk.created)

	res, dr = r.scale(p, 3)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{replicaName(1), replicaName(2)}, h.names(), "only the new replicas are health-checked")
	require.Len(t, r.dk.created, createdBefore+2)
	for i := range 3 {
		require.True(t, running(t, r.dk, replicaName(i)))
	}
	for _, spec := range r.dk.created[createdBefore:] {
		require.Equal(t, depV2, spec.Labels[agentprotocol.LabelDeployment])
		require.Equal(t, "sha256:img-traefik/whoami:latest", spec.Image, "same image, no rebuild or pull")
	}
	require.Equal(t, []string{
		replicaName(0) + ":80", replicaName(1) + ":80", replicaName(2) + ":80",
	}, upstreamsOf(t, r.px))
	require.Equal(t, "/", r.px.Current()[len(r.px.Current())-1].HealthPath, "health path survives the rewrite")
	require.Len(t, r.dk.pulled, 1, "scale never pulls")
	require.Len(t, r.snd.events, eventsBefore, "a finished deployment's history is not touched by scaling")

	ent := r.eng.local[svcID]
	require.Equal(t, 3, ent.Replicas)
	require.Len(t, ent.ContainerNames, 3)
}

func TestScaleUpHealthFailureLeavesExistingReplicasUntouched(t *testing.T) {
	h := &healthRecorder{failName: replicaName(2)}
	r := newRig(t, h.wait)
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	routes := r.px.Current()
	applies := len(r.px.applies)

	res, dr = r.scale(p, 3)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Replica 2 of 3")
	require.Contains(t, dr.Message, "No replicas were added")
	require.True(t, running(t, r.dk, replicaName(0)))
	for _, i := range []int{1, 2} {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists, "new replica %d removed", i)
	}
	require.Equal(t, routes, r.px.Current())
	require.Len(t, r.px.applies, applies, "routes never changed")
}

func TestScaleDownDropsUpstreamsBeforeStopping(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.dk.stopped = nil

	// Record what the proxy routes at the moment each container is stopped.
	var mu sync.Mutex
	routedAtStop := map[string][]string{}
	r.dk.onStop = func(name string) {
		mu.Lock()
		routedAtStop[name] = upstreamsOf(t, r.px)
		mu.Unlock()
	}

	res, dr = r.scale(p, 1)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	require.ElementsMatch(t, []string{replicaName(1), replicaName(2)}, r.dk.stopped)
	for name, ups := range routedAtStop {
		require.Equal(t, []string{replicaName(0) + ":80"}, ups, "%s was still routed when it was stopped", name)
	}
	require.Equal(t, []string{replicaName(0) + ":80"}, upstreamsOf(t, r.px))
	require.True(t, running(t, r.dk, replicaName(0)))
	for _, i := range []int{1, 2} {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists, "removed replica %d", i)
	}
	require.Equal(t, 1, r.eng.local[svcID].Replicas)
}

func TestScaleDownKeepsLowestReplicaIndices(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 4
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	res, dr = r.scale(p, 2)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{replicaName(0) + ":80", replicaName(1) + ":80"}, upstreamsOf(t, r.px))
}

func TestScaleVerifyFailureOnScaleDownKeepsEveryReplica(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	before := r.px.Current()
	r.dk.stopped = nil
	r.px.probes = []int{502}
	r.px.nProbe = 0

	res, dr = r.scale(p, 1)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "No replicas were removed")
	require.Equal(t, before, r.px.Current(), "previous routes restored")
	require.Empty(t, r.dk.stopped)
	for i := range 3 {
		require.True(t, running(t, r.dk, replicaName(i)))
	}
}

func TestScaleRejectsStatefulAndMissingDeployments(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	vol := p
	vol.Volumes = []agentprotocol.VolumeMount{{Name: "nc-vol-5e5e5e5e-data", MountPath: "/data"}}
	res, dr = r.scale(vol, 2)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "stateless")

	other := p
	other.DeploymentID = depV1
	res, dr = r.scale(other, 2)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "No running replica")

	bad, _ := json.Marshal(agentprotocol.ScaleServicePayload{ServiceID: svcID, DeploymentID: depV2, Replicas: 0, Deploy: p})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: agentprotocol.KindScaleService, Payload: bad})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "replicas")
}

func TestScaleUpNeedsEnvSecretsLikeDeploy(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.EnvKeys = []string{"API_KEY"}
	p.SecretEnvKeys = []string{"API_KEY"}
	secrets := map[string]string{"env:API_KEY": "sekret-canary-12345"}
	res, dr := r.deploy(p, secrets)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	// Without secrets the scale fails before creating anything.
	res, dr = r.scale(p, 2)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "API_KEY")
	require.Len(t, r.dk.created, 1)

	raw, _ := json.Marshal(agentprotocol.ScaleServicePayload{ServiceID: svcID, DeploymentID: depV2, Replicas: 2, Deploy: p})
	out := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "s", Kind: agentprotocol.KindScaleService, Payload: raw, Secrets: secrets})
	require.Equal(t, agentprotocol.OutcomeSucceeded, out.Outcome, out.Error)
	require.Equal(t, []string{"API_KEY=sekret-canary-12345", "NEBULA_REPLICA_INDEX=1", "NEBULA_REPLICA_COUNT=2"}, r.dk.created[1].Env)
	require.NotContains(t, string(out.Data), "sekret-canary-12345")
}

// ---- observed, stats, sweep, logs ----

func TestAggregateObserved(t *testing.T) {
	rep := func(i int, state, h string) agentprotocol.ObservedReplica {
		return agentprotocol.ObservedReplica{Index: i, ContainerID: fmt.Sprint("c", i), State: state, Health: h}
	}
	const (
		run  = agentprotocol.StateRunning
		miss = agentprotocol.StateMissing
	)
	cases := []struct {
		name       string
		reps       []agentprotocol.ObservedReplica
		state, hlt string
		ready      int
	}{
		{"all healthy", []agentprotocol.ObservedReplica{rep(0, run, "healthy"), rep(1, run, "healthy"), rep(2, run, "healthy")}, run, agentprotocol.HealthHealthy, 3},
		{"one missing", []agentprotocol.ObservedReplica{rep(0, run, "healthy"), rep(1, miss, "unknown"), rep(2, run, "healthy")}, run, agentprotocol.HealthDegraded, 2},
		{"one unhealthy", []agentprotocol.ObservedReplica{rep(0, run, "healthy"), rep(1, run, "unhealthy")}, run, agentprotocol.HealthDegraded, 1},
		{"one exited", []agentprotocol.ObservedReplica{rep(0, run, "healthy"), rep(1, agentprotocol.StateExited, "unhealthy")}, run, agentprotocol.HealthDegraded, 1},
		{"none healthy", []agentprotocol.ObservedReplica{rep(0, run, "unhealthy"), rep(1, agentprotocol.StateExited, "unhealthy")}, run, agentprotocol.HealthUnhealthy, 0},
		{"all exited", []agentprotocol.ObservedReplica{rep(0, agentprotocol.StateExited, "unhealthy"), rep(1, agentprotocol.StateExited, "unhealthy")}, agentprotocol.StateExited, agentprotocol.HealthUnhealthy, 0},
		{"all missing", []agentprotocol.ObservedReplica{rep(0, miss, "unknown"), rep(1, miss, "unknown")}, miss, agentprotocol.HealthUnknown, 0},
		{"booting", []agentprotocol.ObservedReplica{rep(0, run, "starting"), rep(1, run, "starting")}, run, agentprotocol.HealthStarting, 0},
		{"single healthy", []agentprotocol.ObservedReplica{rep(0, run, "healthy")}, run, agentprotocol.HealthHealthy, 1},
		{"single restarting", []agentprotocol.ObservedReplica{rep(0, agentprotocol.StateRestarting, "unhealthy")}, agentprotocol.StateRestarting, agentprotocol.HealthUnhealthy, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := agentprotocol.ObservedService{ServiceID: "s"}
			aggregateObserved(&o, c.reps)
			require.Equal(t, c.state, o.State)
			require.Equal(t, c.hlt, o.Health)
			require.Equal(t, c.ready, o.ReplicasReady)
			require.Equal(t, len(c.reps), o.ReplicasDesired)
			require.Len(t, o.Replicas, len(c.reps))
		})
	}
}

func TestReconcileReportsObservedPerReplicaWithDegradedAggregate(t *testing.T) {
	r := newRig(t, nil)
	names := agentprotocol.ContainerNames("web", depV2, 3)
	r.dk.seed(names[0], svcID, depV2, true, time.Hour)
	r.dk.seed(names[1], svcID, depV2, false, time.Hour) // crashed
	// names[2] never existed: missing.

	err := r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{
		{ServiceID: svcID, DeploymentID: depV2, Replicas: 3, ContainerNames: names},
	}})
	require.NoError(t, err)
	require.Len(t, r.snd.observed, 1)
	o := r.snd.observed[0].Services[0]
	require.Equal(t, agentprotocol.StateRunning, o.State)
	require.Equal(t, agentprotocol.HealthDegraded, o.Health)
	require.Equal(t, 1, o.ReplicasReady)
	require.Equal(t, 3, o.ReplicasDesired)
	require.Len(t, o.Replicas, 3)
	require.Equal(t, agentprotocol.StateRunning, o.Replicas[0].State)
	require.Equal(t, agentprotocol.HealthHealthy, o.Replicas[0].Health)
	require.Equal(t, agentprotocol.StateExited, o.Replicas[1].State)
	require.Equal(t, agentprotocol.StateMissing, o.Replicas[2].State)
	require.Equal(t, 2, o.Replicas[2].Index)
	require.NotEmpty(t, o.ContainerID)
}

func TestReconcileRemovesOldNamedAndExtraReplicasButKeepsDesiredOnes(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.OrphanMinAge = time.Nanosecond
	names := agentprotocol.ContainerNames("web", depV2, 2)
	r.dk.seed(names[0], svcID, depV2, true, time.Hour)
	r.dk.seed(names[1], svcID, depV2, true, time.Hour)
	extra := replicaName(2) // a replica beyond the desired count
	r.dk.seed(extra, svcID, depV2, true, time.Hour)
	legacy := "nc-web-d2d2d2d2" // named without a replica index
	r.dk.seed(legacy, svcID, depV2, true, time.Hour)

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{
		{ServiceID: svcID, DeploymentID: depV2, Replicas: 2, ContainerNames: names},
	}}))
	require.True(t, running(t, r.dk, names[0]))
	require.True(t, running(t, r.dk, names[1]))
	_, ok := r.dk.state(extra)
	require.False(t, ok, "replica beyond the desired count swept")
	_, ok = r.dk.state(legacy)
	require.False(t, ok, "old-named container swept")
}

func TestReportStatsOneEntryPerReplica(t *testing.T) {
	r := newRig(t, nil)
	names := agentprotocol.ContainerNames("web", depV2, 3)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour) // previous release is not reported
	for _, n := range names {
		r.dk.seed(n, svcID, depV2, true, time.Minute)
	}
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV2, Replicas: 3, ContainerNames: names}

	r.eng.reportStats(context.Background())
	require.Len(t, r.snd.stats, 1)
	got := r.snd.stats[0].Containers
	require.Len(t, got, 3)
	for i, c := range got {
		require.Equal(t, svcID, c.ServiceID)
		require.Equal(t, i, c.Replica)
	}
}

func TestLogsSubscribeMergesReplicasWithPrefix(t *testing.T) {
	r := newRig(t, nil)
	names := agentprotocol.ContainerNames("web", depV2, 2)
	for i, n := range names {
		r.dk.seed(n, svcID, depV2, true, time.Hour)
		r.dk.logText[n] = fmt.Sprintf("hello from %d\n", i)
	}
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV2, Replicas: 2, ContainerNames: names}

	payload, _ := json.Marshal(agentprotocol.LogsSubscribe{StreamID: "st-r", ServiceID: svcID, Tail: 10})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "l", Kind: agentprotocol.KindLogsSubscribe, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Eventually(t, func() bool {
		r.snd.mu.Lock()
		defer r.snd.mu.Unlock()
		return len(r.snd.logEnds) == 1
	}, 3*time.Second, 10*time.Millisecond)

	r.snd.mu.Lock()
	defer r.snd.mu.Unlock()
	require.Len(t, r.snd.logEnds, 1, "one end for the merged stream")
	var lines []string
	for _, f := range r.snd.logs {
		for _, l := range f.Lines {
			lines = append(lines, l.Text)
		}
	}
	sort.Strings(lines)
	require.Equal(t, []string{"[r0] hello from 0", "[r1] hello from 1"}, lines)
	require.False(t, strings.Contains(strings.Join(lines, ""), "[r2]"))
}

func TestRestartRestartsEveryReplicaOneAtATime(t *testing.T) {
	r := newRig(t, nil)
	names := agentprotocol.ContainerNames("web", depV2, 3)
	for _, n := range names {
		r.dk.seed(n, svcID, depV2, true, time.Hour)
	}
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV2, Replicas: 3, ContainerNames: names}
	payload, _ := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID, StopGraceSeconds: 3})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "a", Kind: agentprotocol.KindRestartService, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, names, r.dk.stopped)
	require.Equal(t, names, r.dk.started)
}

// ---- reconcile convergence ----

// desiredFor is the reconcile frame the control plane sends for the deployment
// of p running n replicas behind whoami.localhost.
func desiredFor(p agentprotocol.DeployPayload, n int) agentprotocol.Reconcile {
	names := agentprotocol.ContainerNames(p.ServiceSlug, p.DeploymentID, n)
	ups := make([]string, n)
	for i, name := range names {
		ups[i] = name + ":80"
	}
	return agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{{
		ServiceID: p.ServiceID, DeploymentID: p.DeploymentID, Replicas: n, ContainerNames: names,
		Routes: []agentprotocol.Route{{ServiceID: p.ServiceID, Hosts: p.Hosts, Upstreams: ups, HealthPath: "/"}},
	}}}
}

func TestReconcileAddsMissingReplicasHealthCheckedAndRoutesOnlyLiveOnes(t *testing.T) {
	h := &healthRecorder{}
	r := newRig(t, h.wait)
	p := basePayload()
	p.EnvKeys = []string{"API_KEY"}
	p.SecretEnvKeys = []string{"API_KEY"}
	res, dr := r.deploy(p, map[string]string{"env:API_KEY": "k-1"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	h.mu.Lock()
	h.checked = nil
	h.mu.Unlock()
	applied := len(r.px.applies)

	// The control plane wants 3 replicas (its scale_service expired offline).
	require.NoError(t, r.eng.Reconcile(context.Background(), desiredFor(p, 3)))
	r.eng.bg.Wait()

	require.Greater(t, len(r.px.applies), applied)
	first := r.px.applies[applied]
	require.Equal(t, []string{replicaName(0) + ":80"}, first[len(first)-1].Upstreams,
		"replicas that do not run yet are not routed")
	for i := range 3 {
		require.True(t, running(t, r.dk, replicaName(i)), "replica %d", i)
	}
	require.Equal(t, []string{replicaName(1), replicaName(2)}, h.names(), "only the added replicas are checked")
	require.Equal(t, []string{
		replicaName(0) + ":80", replicaName(1) + ":80", replicaName(2) + ":80",
	}, upstreamsOf(t, r.px), "added after their health check")
	for i, spec := range r.dk.created[1:] {
		require.Equal(t, []string{"API_KEY=k-1", fmt.Sprintf("NEBULA_REPLICA_INDEX=%d", i+1), "NEBULA_REPLICA_COUNT=3"}, spec.Env,
			"environment cloned from a running replica, with its own replica index")
		require.NotEmpty(t, spec.Labels[agentprotocol.LabelSpec], "new replicas carry the spec label too")
	}
	require.Equal(t, 3, r.eng.local[svcID].Replicas)
	last := r.snd.observed[len(r.snd.observed)-1].Services[0]
	require.Equal(t, 3, last.ReplicasReady)
}

func TestReconcileRemovesExtraReplicasAfterLeavingTheUpstreams(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.dk.stopped = nil
	var mu sync.Mutex
	routedAtStop := map[string][]string{}
	r.dk.onStop = func(name string) {
		mu.Lock()
		routedAtStop[name] = upstreamsOf(t, r.px)
		mu.Unlock()
	}

	require.NoError(t, r.eng.Reconcile(context.Background(), desiredFor(p, 1)))
	r.eng.bg.Wait()

	require.ElementsMatch(t, []string{replicaName(1), replicaName(2)}, r.dk.stopped)
	for name, ups := range routedAtStop {
		require.Equal(t, []string{replicaName(0) + ":80"}, ups, "%s still routed when stopped", name)
	}
	require.True(t, running(t, r.dk, replicaName(0)))
	for _, i := range []int{1, 2} {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists)
	}
	require.Equal(t, 1, r.eng.local[svcID].Replicas)
}

func TestReconcileLeavesMatchingAndUnlabelledServicesAlone(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	created := len(r.dk.created)
	require.NoError(t, r.eng.Reconcile(context.Background(), desiredFor(p, 1)))
	r.eng.bg.Wait()
	require.Len(t, r.dk.created, created, "already at the desired count")

	// A container without a spec label cannot be cloned: nothing happens.
	r2 := newRig(t, nil)
	r2.dk.seed(replicaName(0), svcID, depV2, true, time.Hour)
	require.NoError(t, r2.eng.Reconcile(context.Background(), desiredFor(p, 2)))
	r2.eng.bg.Wait()
	require.Empty(t, r2.dk.created)

	// Nothing running at all: the control plane's redeploy path owns that.
	r3 := newRig(t, nil)
	require.NoError(t, r3.eng.Reconcile(context.Background(), desiredFor(p, 2)))
	r3.eng.bg.Wait()
	require.Empty(t, r3.dk.created)
}

// ---- a scale is final ----

// staleFrame is the reconcile frame of a control plane that built it while the
// service still had n replicas, stamped with the time that count was stored.
func staleFrame(p agentprotocol.DeployPayload, n int, storedAt time.Time) agentprotocol.Reconcile {
	f := desiredFor(p, n)
	f.Services[0].ScaledAt = storedAt
	return f
}

// A convergence started by a frame that wanted 3 replicas, and held up until a
// scale to 1 had run (the service lock serializes them), must not start the
// replicas the scale removed.
func TestConvergenceOfAnOlderCountDoesNotUndoAScaleDown(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	stored3 := time.Now().Add(-time.Minute)
	stored1 := time.Now()
	frame3 := staleFrame(p, 3, stored3).Services[0]
	stale := desiredEntry{
		DeploymentID: frame3.DeploymentID, Replicas: frame3.Replicas, ContainerNames: frame3.ContainerNames, ScaledAt: frame3.ScaledAt,
	}

	// The convergence goroutine is waiting for the service lock; the scale
	// command takes it first.
	res, dr = r.scaleAt(p, 1, stored1)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	created := len(r.dk.created)

	r.eng.convergeReplicas(context.Background(), svcID, stale)

	require.Len(t, r.dk.created, created, "the convergence started a replica after the scale-down")
	require.True(t, running(t, r.dk, replicaName(0)))
	for _, i := range []int{1, 2} {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists, "replica %d came back", i)
	}
	require.Equal(t, 1, r.eng.local[svcID].Replicas)
}

// A frame the control plane built before the scale landed (the count it
// replaced, an older stamp) neither changes what runs nor the agent's record,
// whichever order the frame and the command reached the agent in.
func TestStaleReconcileFrameDoesNotUndoAScale(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Replicas = 3
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	stored3 := time.Now().Add(-time.Minute)
	stored1 := time.Now()
	res, dr = r.scaleAt(p, 1, stored1)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	created := len(r.dk.created)

	require.NoError(t, r.eng.Reconcile(context.Background(), staleFrame(p, 3, stored3)))
	r.eng.bg.Wait()

	require.Len(t, r.dk.created, created, "a stale frame started a replica")
	for _, i := range []int{1, 2} {
		_, exists := r.dk.state(replicaName(i))
		require.False(t, exists, "replica %d came back", i)
	}
	require.Equal(t, 1, r.eng.local[svcID].Replicas)
	require.Equal(t, 1, r.eng.desired[svcID].Replicas)
	for _, u := range upstreamsOf(t, r.px) {
		require.Equal(t, replicaName(0)+":80", u, "a retired replica is routed again")
	}

	// The control plane's later count does apply: scaled up again while this
	// agent was offline (a frame stamped after the scale it knows).
	require.NoError(t, r.eng.Reconcile(context.Background(), staleFrame(p, 3, stored1.Add(time.Minute))))
	r.eng.bg.Wait()
	for i := range 3 {
		require.True(t, running(t, r.dk, replicaName(i)), "replica %d", i)
	}
	require.Equal(t, 3, r.eng.local[svcID].Replicas)
}
