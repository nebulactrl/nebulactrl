package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	prevWeb    = "nc-web-d1d1d1d1-0"
	prevWorker = "nc-web-d1d1d1d1-worker-0"
	prevSched  = "nc-web-d1d1d1d1-scheduler-0"
)

// pname is the container name of replica i of a named process of the test service.
func pname(dep, process string, i int) string {
	return agentprotocol.ProcessContainerName("web", dep, process, false, i)
}

// processPayload is a service with three processes: the default web process
// (port 80, two replicas), a port-less worker (two replicas) and a singleton
// scheduler that recreates. The top-level fields equal the default process.
func processPayload() agentprotocol.DeployPayload {
	p := basePayload()
	none := agentprotocol.HealthcheckSpec{Type: "none"}
	p.Processes = []agentprotocol.ProcessSpec{
		{
			Name: "web", Default: true, Replicas: 2, Strategy: agentprotocol.StrategyRolling, Port: 80,
			Healthcheck: p.Healthcheck, StopGraceSeconds: 5, Hosts: p.Hosts,
		},
		{
			Name: "worker", Replicas: 2, Strategy: agentprotocol.StrategyRolling, StartCommand: []string{"app", "worker"},
			Healthcheck: none, StopGraceSeconds: 7,
		},
		{
			Name: "scheduler", Replicas: 1, Strategy: agentprotocol.StrategyRecreate, Singleton: true,
			StartCommand: []string{"app", "scheduler"}, Healthcheck: none, StopGraceSeconds: 11,
		},
	}
	p.Replicas = 2
	return p
}

// previousProcesses seeds a running multi-process release (v1) plus the route
// table that points at its web process, and returns that table.
func (r *rig) previousProcesses() []agentprotocol.Route {
	for _, c := range []struct{ name, process string }{{prevWeb, "web"}, {prevWorker, "worker"}, {prevSched, "scheduler"}} {
		r.dk.seed(c.name, svcID, depV1, true, time.Hour).Labels[agentprotocol.LabelProcess] = c.process
	}
	routes := []agentprotocol.Route{
		{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}},
		{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{prevWeb + ":80"}, HealthPath: "/"},
	}
	require.NoError(r.t, r.px.Apply(routes))
	r.px.applies = nil
	return routes
}

// ownRoutesNow lists the service's own routes (named processes' included) in
// the proxy's live table.
func ownRoutesNow(r *rig) []agentprotocol.Route {
	var out []agentprotocol.Route
	for _, rt := range r.px.Current() {
		if ownedByService(rt, svcID) {
			out = append(out, rt)
		}
	}
	return out
}

func createdNames(dk *fakeDocker) []string {
	dk.mu.Lock()
	defer dk.mu.Unlock()
	out := make([]string, len(dk.created))
	for i, s := range dk.created {
		out[i] = s.Name
	}
	return out
}

func createdSpec(t *testing.T, dk *fakeDocker, name string) docker.ContainerSpec {
	t.Helper()
	dk.mu.Lock()
	defer dk.mu.Unlock()
	for _, s := range dk.created {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no container %s was created", name)
	return docker.ContainerSpec{}
}

func opIndex(ops []string, op string) int { return slices.Index(ops, op) }

func outcomesOf(rs []agentprotocol.ProcessResult) map[string]string {
	out := map[string]string{}
	for _, r := range rs {
		out[r.Name] = r.Outcome
	}
	return out
}

func quietOneShots(r *rig) {
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if c.Name == preCtr || c.Name == postCtr {
			c.State, c.Running, c.ExitCode = "exited", false, 0
		}
	}
}

// ---- happy path ----

func TestProcessDeployRollsEveryProcessInOrder(t *testing.T) {
	r := newRig(t, nil)
	r.previousProcesses()
	quietOneShots(r)
	p := processPayload()
	p.PreDeploy = &agentprotocol.PreDeploySpec{Command: []string{"rake", "db:migrate"}, TimeoutSeconds: 30}
	p.PostDeploy = &agentprotocol.PostDeploySpec{Command: []string{"rake", "cache:warm"}, TimeoutSeconds: 30}

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Deployed 3 processes (web ×2, worker ×2, scheduler)")
	require.Equal(t, []agentprotocol.ProcessResult{
		{Name: "web", Outcome: "succeeded", Message: "2 replicas"},
		{Name: "worker", Outcome: "succeeded", Message: "2 replicas"},
		{Name: "scheduler", Outcome: "succeeded", Message: "1 replica"},
	}, dr.Processes)
	require.Equal(t, replicaName(depV2, 0), r.dk.created[1].Name)

	// Pulled once, pre- and post-deploy once; the port process rolls first.
	require.Len(t, r.dk.pulled, 1)
	require.Equal(t, []string{
		preCtr,
		replicaName(depV2, 0), replicaName(depV2, 1),
		pname(depV2, "worker", 0), pname(depV2, "worker", 1),
		pname(depV2, "scheduler", 0),
		postCtr,
	}, createdNames(r.dk))

	// Names, labels, environment, command, stop grace and aliases per process.
	for _, tc := range []struct {
		name, process, alias string
		replica              int
		cmd                  []string
		grace                int
	}{
		{replicaName(depV2, 0), "web", "web", 0, nil, 5},
		{replicaName(depV2, 1), "web", "web", 1, nil, 5},
		{pname(depV2, "worker", 1), "worker", "worker.web", 1, []string{"app", "worker"}, 7},
		{pname(depV2, "scheduler", 0), "scheduler", "scheduler.web", 0, []string{"app", "scheduler"}, 11},
	} {
		spec := createdSpec(t, r.dk, tc.name)
		require.Equal(t, tc.process, spec.Labels[agentprotocol.LabelProcess], tc.name)
		require.Equal(t, fmt.Sprint(tc.replica), spec.Labels[agentprotocol.LabelReplica], tc.name)
		require.Equal(t, depV2, spec.Labels[agentprotocol.LabelDeployment])
		require.Contains(t, spec.Env, "NEBULA_PROCESS="+tc.process, tc.name)
		require.Contains(t, spec.Env, fmt.Sprintf("NEBULA_REPLICA_INDEX=%d", tc.replica), tc.name)
		require.Equal(t, tc.cmd, spec.Cmd, tc.name)
		require.Equal(t, tc.grace, *spec.StopTimeout, tc.name)
		require.Equal(t, []string{tc.alias}, spec.Networks[0].Aliases, tc.name)
	}
	require.Contains(t, createdSpec(t, r.dk, pname(depV2, "worker", 0)).Env, "NEBULA_REPLICA_COUNT=2")

	// The label rebuilds that process alone (scale, convergence, uptime).
	c, ok := r.dk.state(pname(depV2, "worker", 1))
	require.True(t, ok)
	got, ok := payloadFromContainer(c)
	require.True(t, ok)
	require.Len(t, got.Processes, 1)
	require.Equal(t, "worker", got.Processes[0].Name)
	require.False(t, got.Processes[0].Default)
	require.Equal(t, 7, got.StopGraceSeconds)
	require.Zero(t, got.Port)
	require.Equal(t, "worker.web", planOfDeploy(got).Alias)

	// The previous release retires after the last process rolled; the singleton
	// was stopped before its replacement was created and is not stopped twice.
	ops := r.dk.ops
	lastStart := opIndex(ops, "start:"+pname(depV2, "scheduler", 0))
	require.Greater(t, opIndex(ops, "stop:"+prevWeb), lastStart, "rolling processes retire after every process rolled")
	require.Greater(t, opIndex(ops, "stop:"+prevWorker), lastStart)
	require.Less(t, opIndex(ops, "stop:"+prevSched), opIndex(ops, "create:"+pname(depV2, "scheduler", 0)))
	require.Equal(t, 1, strings.Count(strings.Join(ops, ","), "stop:"+prevSched))
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		_, exists := r.dk.state(n)
		require.False(t, exists, "%s retired", n)
	}
	require.Equal(t, 5*time.Second, r.dk.stopGrace[prevWeb])
	require.Equal(t, 7*time.Second, r.dk.stopGrace[prevWorker])
	require.Equal(t, 11*time.Second, r.dk.stopGrace[prevSched])

	// One route for the one process with a port; nothing else changed.
	own := ownRoutesNow(r)
	require.Len(t, own, 1)
	require.Equal(t, svcID, own[0].ServiceID)
	require.Equal(t, []string{replicaName(depV2, 0) + ":80", replicaName(depV2, 1) + ":80"}, own[0].Upstreams)
	require.Len(t, r.px.Current(), 2, "the other service's route stays")

	// The recorded entry carries every process; the top level mirrors the default.
	ent := r.eng.local[svcID]
	require.Len(t, ent.Processes, 3)
	require.Equal(t, 2, ent.Replicas)
	require.Equal(t, []string{replicaName(depV2, 0), replicaName(depV2, 1)}, ent.ContainerNames)
	require.Equal(t, []string{pname(depV2, "scheduler", 0)}, ent.Processes[2].ContainerNames)

	steps := strings.Join(r.snd.eventSteps(), ",")
	require.Equal(t, 1, strings.Count(steps, "predeploy:started"))
	require.Equal(t, 1, strings.Count(steps, "postdeploy:started"))
	require.Equal(t, 1, strings.Count(steps, "retire:started"))
	require.Less(t, strings.Index(steps, "retire:started"), strings.Index(steps, "postdeploy:started"))
	var workerHealth bool
	for _, e := range r.snd.events {
		if e.Step == agentprotocol.StepHealth && strings.HasPrefix(e.Message, "[worker] ") {
			workerHealth = true
		}
	}
	require.True(t, workerHealth, "events of a process carry its name")
	require.Contains(t, r.snd.allLogText(), "[worker] ")
}

func TestProcessDeployNamedProcessRoutesUnderItsOwnID(t *testing.T) {
	r := newRig(t, nil)
	p := processPayload()
	p.Processes = append(p.Processes, agentprotocol.ProcessSpec{
		Name: "api", Replicas: 1, Strategy: agentprotocol.StrategyRolling, Port: 8081, StartCommand: []string{"app", "api"},
		Healthcheck: agentprotocol.HealthcheckSpec{Type: "http", Path: "/up", DeadlineSeconds: 5}, StopGraceSeconds: 5,
		Hosts: []string{"api.localhost"},
	})

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	byID := map[string]agentprotocol.Route{}
	for _, rt := range r.px.Current() {
		byID[rt.ServiceID] = rt
	}
	apiID := agentprotocol.ProcessRouteID(svcID, "api")
	require.Contains(t, byID, svcID)
	require.Contains(t, byID, apiID)
	require.Equal(t, svcID, byID[apiID].Stack, "a named process' route belongs to the service")
	require.Equal(t, []string{pname(depV2, "api", 0) + ":8081"}, byID[apiID].Upstreams)
	require.Equal(t, "/up", byID[apiID].HealthPath)
	require.Equal(t, []string{"api.localhost"}, byID[apiID].Hosts)
	require.Empty(t, byID[svcID].Stack)
	require.Equal(t, []string{replicaName(depV2, 0) + ":80", replicaName(depV2, 1) + ":80"}, byID[svcID].Upstreams)
	require.Contains(t, byID, "other")
}

func TestLegacyPayloadStaysSingleProcess(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Nil(t, dr.Processes)
	spec := createdSpec(t, r.dk, newCtr)
	require.NotContains(t, spec.Labels, agentprotocol.LabelProcess)
	for _, e := range spec.Env {
		require.False(t, strings.HasPrefix(e, "NEBULA_PROCESS="))
	}
	require.NotContains(t, spec.Labels[agentprotocol.LabelSpec], `"process"`)
	require.Nil(t, r.eng.local[svcID].Processes)
}

// ---- singleton ----

func TestSingletonIsStoppedBeforeItsReplacementStarts(t *testing.T) {
	r := newRig(t, nil)
	r.previousProcesses()
	var mu sync.Mutex
	var overlaps []string
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if c.Labels[agentprotocol.LabelProcess] != "scheduler" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, other := range r.dk.containers { // the fake's lock is held
			if other.ID != c.ID && other.Running && other.Labels[agentprotocol.LabelProcess] == "scheduler" {
				overlaps = append(overlaps, other.Name)
			}
		}
	}

	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, overlaps, "two scheduler containers ran at once")

	newSched := pname(depV2, "scheduler", 0)
	stop, create := opIndex(r.dk.ops, "stop:"+prevSched), opIndex(r.dk.ops, "create:"+newSched)
	require.GreaterOrEqual(t, stop, 0)
	require.Less(t, stop, create, "stop(old) strictly before create(new)")
	require.Equal(t, 11*time.Second, r.dk.stopGrace[prevSched], "the process' own stop grace reaches ContainerStop")
	require.True(t, running(t, r.dk, replicaName(depV2, 0)))
	require.True(t, running(t, r.dk, newSched))
}

// ---- failure ----

func healthFailing(names ...string) HealthFunc {
	h := &healthRecorder{}
	if len(names) > 0 {
		h.failName = names[0]
	}
	return h.wait
}

func TestWebHealthFailureStartsNothingElseAndLeavesTheOldReleaseServing(t *testing.T) {
	r := newRig(t, healthFailing(replicaName(depV2, 0)))
	prev := r.previousProcesses()

	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	require.True(t, strings.HasPrefix(dr.Message, "Process web failed: "), dr.Message)
	require.Contains(t, dr.Message, "Previous release is still serving")
	require.Equal(t, map[string]string{"web": "failed", "worker": "skipped", "scheduler": "skipped"}, outcomesOf(dr.Processes))
	require.Equal(t, "not started: process web failed", dr.Processes[1].Message)

	for _, n := range []string{replicaName(depV2, 0), replicaName(depV2, 1), pname(depV2, "worker", 0), pname(depV2, "scheduler", 0)} {
		notThere(t, r.dk, n)
	}
	require.NotContains(t, createdNames(r.dk), pname(depV2, "worker", 0), "later processes never started")
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		require.True(t, running(t, r.dk, n), "%s untouched", n)
	}
	require.Empty(t, r.dk.stopped)
	require.Empty(t, r.px.applies)
	require.Equal(t, prev, r.px.Current())
}

func TestWorkerFailureAfterWebWasRoutedRestoresTheRoutesAndRemovesEverythingNew(t *testing.T) {
	r := newRig(t, healthFailing(pname(depV2, "worker", 0)))
	prev := r.previousProcesses()

	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.True(t, strings.HasPrefix(dr.Message, "Process worker failed: "), dr.Message)
	require.Contains(t, dr.Message, "Rolled back to the previous release.")
	require.Equal(t, map[string]string{"web": "rolled_back", "worker": "failed", "scheduler": "skipped"}, outcomesOf(dr.Processes))

	require.Len(t, r.px.applies, 2, "web's route applied, then the previous own routes restored")
	require.Equal(t, []string{replicaName(depV2, 0) + ":80", replicaName(depV2, 1) + ":80"}, r.px.applies[0][len(r.px.applies[0])-1].Upstreams)
	require.Equal(t, []agentprotocol.Route{prev[1]}, ownRoutesNow(r), "the previous own routes are back")
	require.Contains(t, r.px.Current(), prev[0], "other services' routes are untouched")

	for _, n := range []string{replicaName(depV2, 0), replicaName(depV2, 1), pname(depV2, "worker", 0), pname(depV2, "worker", 1)} {
		notThere(t, r.dk, n)
	}
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		require.True(t, running(t, r.dk, n), "%s untouched", n)
	}
	require.Empty(t, r.dk.stopped, "the singleton was never reached")
	require.NotContains(t, createdNames(r.dk), pname(depV2, "scheduler", 0))
}

func TestSingletonIsRestartedWhenALaterProcessFails(t *testing.T) {
	r := newRig(t, healthFailing(pname(depV2, "worker", 0)))
	r.previousProcesses()
	p := processPayload()
	p.Processes = []agentprotocol.ProcessSpec{p.Processes[0], p.Processes[2], p.Processes[1]} // web, scheduler, worker

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Equal(t, map[string]string{"web": "rolled_back", "scheduler": "rolled_back", "worker": "failed"}, outcomesOf(dr.Processes))

	stop, restart := opIndex(r.dk.ops, "stop:"+prevSched), opIndex(r.dk.ops, "start:"+prevSched)
	require.GreaterOrEqual(t, stop, 0, "the old singleton was stopped for its replacement")
	require.Greater(t, restart, stop, "and started again when the deploy failed")
	require.True(t, running(t, r.dk, prevSched))
	notThere(t, r.dk, pname(depV2, "scheduler", 0))
	notThere(t, r.dk, pname(depV2, "worker", 0))
}

func TestRouteRestoreFailureEndsAsRollbackFailed(t *testing.T) {
	r := newRig(t, healthFailing(pname(depV2, "worker", 0)))
	r.previousProcesses()
	r.px.applyErr = func(n int) error {
		if n == 2 {
			return errors.New("proxy went away")
		}
		return nil
	}

	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "could not restore previous routes: proxy went away")
	require.Contains(t, dr.Message, "docker ps --filter label=nebulactrl.service="+svcID)
	require.True(t, strings.HasPrefix(dr.Message, "Process worker failed: "), dr.Message)
	require.Equal(t, "failed", outcomesOf(dr.Processes)["web"], "an unwound process whose routes could not be restored is not claimed as rolled back")
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		require.True(t, running(t, r.dk, n), "%s untouched", n)
	}
}

func TestVerifyFailureOfALaterRoutedProcessUnwindsTheEarlierRoute(t *testing.T) {
	r := newRig(t, nil)
	prev := r.previousProcesses()
	p := processPayload()
	p.Processes = append(p.Processes, agentprotocol.ProcessSpec{
		Name: "api", Replicas: 1, Strategy: agentprotocol.StrategyRolling, Port: 8081, StartCommand: []string{"app", "api"},
		Healthcheck: agentprotocol.HealthcheckSpec{Type: "http", Path: "/up", DeadlineSeconds: 5}, StopGraceSeconds: 5,
		Hosts: []string{"api.localhost"},
	})
	p.Processes = []agentprotocol.ProcessSpec{p.Processes[0], p.Processes[3], p.Processes[1], p.Processes[2]} // web, api, worker, scheduler
	r.px.probes = []int{200, 200, 502}                                                                        // web's two probes pass, api's fail

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Equal(t, "failed", outcomesOf(dr.Processes)["api"])
	require.Equal(t, "rolled_back", outcomesOf(dr.Processes)["web"])
	require.Equal(t, []agentprotocol.Route{prev[1]}, ownRoutesNow(r), "web's new route went with the failed api verification")
	require.Equal(t, "skipped", outcomesOf(dr.Processes)["worker"])
}

// ---- cancel ----

func TestCancelDuringALaterProcessUnwindsEverything(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	prev := r.previousProcesses()
	var once sync.Once
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == agentprotocol.StepHealth && e.Status == agentprotocol.StepStarted && strings.HasPrefix(e.Message, "[worker] ") {
			once.Do(func() { r.cancelDeployment(depV2, "alice") })
		}
	}

	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, agentprotocol.StepHealth, dr.CanceledAt)
	require.Contains(t, dr.Message, "Canceled by alice during the health check")
	require.Contains(t, dr.Message, "The previous routes were restored.")
	require.Contains(t, dr.Message, "The new containers were removed.")
	require.Equal(t, map[string]string{"web": "rolled_back", "worker": "rolled_back", "scheduler": "skipped"}, outcomesOf(dr.Processes))

	require.Equal(t, []agentprotocol.Route{prev[1]}, ownRoutesNow(r))
	for _, n := range []string{replicaName(depV2, 0), pname(depV2, "worker", 0), pname(depV2, "worker", 1)} {
		notThere(t, r.dk, n)
	}
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		require.True(t, running(t, r.dk, n), "%s untouched", n)
	}
	require.Empty(t, r.dk.stopped)
	require.NotContains(t, r.snd.eventSteps(), "retire:started")
}

func TestCancelBetweenProcessesTouchesNothingOfTheNextOne(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	prev := r.previousProcesses()
	var once sync.Once
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == agentprotocol.StepVerify && e.Status == agentprotocol.StepSucceeded {
			once.Do(func() { r.cancelDeployment(depV2, "alice") })
		}
	}
	p := processPayload()
	p.Processes = []agentprotocol.ProcessSpec{p.Processes[0], p.Processes[2], p.Processes[1]} // web, scheduler, worker

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, map[string]string{"web": "rolled_back", "scheduler": "skipped", "worker": "skipped"}, outcomesOf(dr.Processes))
	require.Empty(t, r.dk.stopped, "the old singleton was not stopped after the cancel")
	require.Equal(t, []agentprotocol.Route{prev[1]}, ownRoutesNow(r), "web's route is back on the previous release")
	notThere(t, r.dk, replicaName(depV2, 0))
	require.NotContains(t, createdNames(r.dk), pname(depV2, "scheduler", 0))
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		require.True(t, running(t, r.dk, n))
	}
}

func TestCancelWhileTheSingletonIsReplacedRestartsTheOldOne(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previousProcesses()
	var once sync.Once
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == agentprotocol.StepCreate && e.Status == agentprotocol.StepStarted && strings.HasPrefix(e.Message, "[scheduler] ") {
			once.Do(func() { r.cancelDeployment(depV2, "alice") })
		}
	}
	p := processPayload()
	p.Processes = []agentprotocol.ProcessSpec{p.Processes[2], p.Processes[0], p.Processes[1]} // scheduler first

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "The previous release was started again.")
	require.True(t, running(t, r.dk, prevSched), "the old singleton runs again")
	notThere(t, r.dk, pname(depV2, "scheduler", 0))
}

// ---- resume ----

func TestRepeatedDeployCommandContinuesRunningContainersOfEveryProcess(t *testing.T) {
	r := newRig(t, nil)
	p := processPayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	created := createdNames(r.dk)
	require.Len(t, created, 5)

	res, dr = r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, created, createdNames(r.dk), "nothing was created again")
	log := r.snd.allLogText()
	for _, n := range []string{pname(depV2, "worker", 1), pname(depV2, "scheduler", 0), replicaName(depV2, 1)} {
		require.Contains(t, log, "Container "+n+" from an earlier attempt is running; continuing")
	}
	for _, n := range created {
		require.True(t, running(t, r.dk, n), n)
	}
}

// ---- orphans ----

func TestProcessDroppedByThePayloadIsRetiredWithItsRoute(t *testing.T) {
	r := newRig(t, nil)
	r.previousProcesses()
	const prevOld = "nc-web-d1d1d1d1-legacy-0"
	r.dk.seed(prevOld, svcID, depV1, true, time.Hour).Labels[agentprotocol.LabelProcess] = "legacy"
	oldID := agentprotocol.ProcessRouteID(svcID, "legacy")
	require.NoError(t, r.px.Apply(append(r.px.Current(), agentprotocol.Route{
		ServiceID: oldID, Stack: svcID, Hosts: []string{"legacy.localhost"}, Upstreams: []string{prevOld + ":9000"},
	})))
	r.px.applies = nil

	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	_, exists := r.dk.state(prevOld)
	require.False(t, exists, "the dropped process' container is retired")
	for _, rt := range r.px.Current() {
		require.NotEqual(t, oldID, rt.ServiceID, "its route is gone")
	}
	require.Equal(t, 5*time.Second, r.dk.stopGrace[prevOld], "an orphan gets the default process' stop grace")
	require.Len(t, ownRoutesNow(r), 1)
	require.Greater(t, opIndex(r.dk.ops, "stop:"+prevOld), opIndex(r.dk.ops, "start:"+pname(depV2, "scheduler", 0)), "retired after the last process")
}

func TestLegacyReleaseIsRetiredByAMultiProcessDeploy(t *testing.T) {
	r := newRig(t, nil)
	r.previous() // an unlabelled single-process release
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	_, exists := r.dk.state(oldCtr)
	require.False(t, exists, "the unlabelled container belongs to the default process and is retired")
	require.True(t, running(t, r.dk, replicaName(depV2, 0)))
}

func TestMultiProcessReleaseIsRetiredByALegacyDeploy(t *testing.T) {
	r := newRig(t, nil)
	r.previousProcesses()
	oldProc := agentprotocol.ProcessRouteID(svcID, "api")
	require.NoError(t, r.px.Apply(append(r.px.Current(), agentprotocol.Route{
		ServiceID: oldProc, Stack: svcID, Hosts: []string{"api.localhost"}, Upstreams: []string{"nc-web-d1d1d1d1-api-0:8081"},
	})))
	r.px.applies = nil

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	for _, n := range []string{prevWeb, prevWorker, prevSched} {
		_, exists := r.dk.state(n)
		require.False(t, exists, "%s retired", n)
	}
	for _, rt := range r.px.Current() {
		require.NotEqual(t, oldProc, rt.ServiceID, "the process route goes with the release")
	}
}

// ---- payload validation ----

func TestProcessPayloadValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(p *agentprotocol.DeployPayload)
		want string
	}{
		{"one process", func(p *agentprotocol.DeployPayload) { p.Processes = p.Processes[:1] }, "needs 2 to 10 processes"},
		{"two defaults", func(p *agentprotocol.DeployPayload) { p.Processes[1].Default = true }, "exactly one process must be the default"},
		{"no default", func(p *agentprotocol.DeployPayload) { p.Processes[0].Default = false }, "exactly one process must be the default"},
		{"duplicate name", func(p *agentprotocol.DeployPayload) { p.Processes[1].Name = "web" }, "listed twice"},
		{"bad name", func(p *agentprotocol.DeployPayload) { p.Processes[1].Name = "Bad_Name" }, "invalid"},
		{"duplicate port", func(p *agentprotocol.DeployPayload) { p.Processes[1].Port = 80 }, "both use port 80"},
		{"zero replicas", func(p *agentprotocol.DeployPayload) { p.Processes[1].Replicas = 0 }, "out of range"},
		{"singleton replicas", func(p *agentprotocol.DeployPayload) { p.Processes[2].Replicas = 2 }, "runs one replica"},
		{"volumes replicas", func(p *agentprotocol.DeployPayload) {
			p.Volumes = []agentprotocol.VolumeMount{{Name: "nc-vol-5e5e5e5e-data", MountPath: "/data"}}
		}, "runs one replica"},
		{"unknown strategy", func(p *agentprotocol.DeployPayload) { p.Processes[1].Strategy = "blue-green" }, "unknown strategy"},
		{"one-shot", func(p *agentprotocol.DeployPayload) { p.OneShot = true }, "one-shot"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := processPayload()
			c.edit(&p)
			err := validatePayload(p)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)

			r := newRig(t, nil)
			res, dr := r.deploy(p, nil)
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Contains(t, dr.Message, "invalid deploy payload")
			require.Empty(t, r.dk.created)
		})
	}
	require.NoError(t, validatePayload(processPayload()))
}

func TestSharedNetworkAliasOfANamedProcessSitsBesideTheDefaultOne(t *testing.T) {
	p := processPayload()
	p.SharedNetworks = []agentprotocol.SharedNetworkAttachment{{Name: "nc-shared-team", Alias: "app"}}
	web, err := projectProcessByName(p, "")
	require.NoError(t, err)
	worker, err := projectProcessByName(p, "worker")
	require.NoError(t, err)
	_, err = projectProcessByName(p, "nope")
	require.Error(t, err)

	aliasOn := func(pl agentprotocol.DeployPayload, network string) []string {
		for _, a := range planOfDeploy(pl).Attachments() {
			if a.Network == network {
				return a.Aliases
			}
		}
		return nil
	}
	require.Equal(t, []string{"app"}, aliasOn(web, "nc-shared-team"))
	require.Equal(t, []string{"worker.app"}, aliasOn(worker, "nc-shared-team"))
	require.Equal(t, []string{"web"}, aliasOn(web, agentprotocol.EnvNetwork(envID)))
	require.Equal(t, []string{"worker.web"}, aliasOn(worker, agentprotocol.EnvNetwork(envID)))
}

// ---- runtime outside deploys ----

func TestSweepOrphansKeepsEveryProcessContainer(t *testing.T) {
	r := newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	stray := r.dk.seed(pname("d9d9d9d9-0000-4000-8000-000000000009", "worker", 0), svcID, "d9d9d9d9-0000-4000-8000-000000000009", true, time.Hour)
	stray.Labels[agentprotocol.LabelProcess] = "worker"
	r.eng.cfg.OrphanMinAge = time.Nanosecond
	time.Sleep(5 * time.Millisecond)

	require.NoError(t, r.eng.sweepOrphans(context.Background()))

	for _, n := range createdNames(r.dk) {
		require.True(t, running(t, r.dk, n), "%s is owned by the desired entry and must survive", n)
	}
	notThere(t, r.dk, stray.Name)
}

func TestDesiredEntryOwnsTheContainersOfEveryProcess(t *testing.T) {
	ent := desiredEntry{DeploymentID: depV2, Processes: []desiredProcess{
		{Name: "web", Default: true, Replicas: 1, ContainerNames: []string{replicaName(depV2, 0)}},
		{Name: "worker", Replicas: 2, ContainerNames: []string{pname(depV2, "worker", 0), pname(depV2, "worker", 1)}},
	}}
	ent.mirrorDefault()
	require.Equal(t, []string{replicaName(depV2, 0)}, ent.ContainerNames)
	for _, n := range []string{replicaName(depV2, 0), pname(depV2, "worker", 1)} {
		require.True(t, ent.owns(docker.ContainerInfo{Name: n}), n)
	}
	require.False(t, ent.owns(docker.ContainerInfo{Name: pname(depV2, "worker", 2)}))
	legacy := desiredEntry{DeploymentID: depV2, Replicas: 1, ContainerNames: []string{replicaName(depV2, 0)}}
	require.Len(t, legacy.procs(), 1)
	require.Equal(t, "", legacy.procs()[0].Name)
}

func TestObserveReportsEveryProcess(t *testing.T) {
	r := newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	obs := r.eng.observe(context.Background(), svcID, r.eng.desired[svcID])
	require.Equal(t, agentprotocol.StateRunning, obs.State)
	require.Equal(t, agentprotocol.HealthHealthy, obs.Health)
	require.Equal(t, 5, obs.ReplicasDesired)
	require.Equal(t, 5, obs.ReplicasReady)
	require.Len(t, obs.Replicas, 5)
	require.Equal(t, []agentprotocol.ObservedProcess{
		{Name: "web", State: "running", Health: "healthy", ReplicasReady: 2, ReplicasDesired: 2},
		{Name: "worker", State: "running", Health: "healthy", ReplicasReady: 2, ReplicasDesired: 2},
		{Name: "scheduler", State: "running", Health: "healthy", ReplicasReady: 1, ReplicasDesired: 1},
	}, obs.Processes)
	var got []string
	for _, rep := range obs.Replicas {
		got = append(got, fmt.Sprintf("%s/%d", rep.Process, rep.Index))
	}
	require.Equal(t, []string{"web/0", "web/1", "worker/0", "worker/1", "scheduler/0"}, got, "Index counts within the process")

	// A process whose containers vanished is missing while the service still runs.
	for _, n := range []string{pname(depV2, "worker", 0), pname(depV2, "worker", 1)} {
		require.NoError(t, r.dk.ContainerRemove(context.Background(), n, true, false))
	}
	obs = r.eng.observe(context.Background(), svcID, r.eng.desired[svcID])
	require.Equal(t, agentprotocol.StateRunning, obs.State)
	require.Equal(t, agentprotocol.HealthDegraded, obs.Health)
	require.Equal(t, agentprotocol.StateMissing, obs.Processes[1].State)
	require.Equal(t, agentprotocol.HealthUnknown, obs.Processes[1].Health)
	require.Equal(t, 3, obs.ReplicasReady)
}

func TestStatsCarryTheProcessAndTheReplicaWithinIt(t *testing.T) {
	r := newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	r.eng.reportStats(context.Background())
	require.Len(t, r.snd.stats, 1)
	var got []string
	for _, c := range r.snd.stats[0].Containers {
		got = append(got, fmt.Sprintf("%s/%d", c.Process, c.Replica))
	}
	sort.Strings(got)
	require.Equal(t, []string{"scheduler/0", "web/0", "web/1", "worker/0", "worker/1"}, got)
}

func TestReplicaContainerResolvesByProcess(t *testing.T) {
	r := newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	ctx := context.Background()

	for _, tc := range []struct {
		process string
		replica int
		want    string
	}{
		{"", 0, replicaName(depV2, 0)}, // the default process
		{"", 1, replicaName(depV2, 1)},
		{"web", 1, replicaName(depV2, 1)},
		{"worker", 1, pname(depV2, "worker", 1)},
		{"scheduler", 0, pname(depV2, "scheduler", 0)},
	} {
		c, err := r.eng.replicaContainer(ctx, svcID, tc.process, tc.replica)
		require.NoError(t, err, "%q/%d", tc.process, tc.replica)
		require.Equal(t, tc.want, c.Name)
	}
	for _, tc := range []struct {
		process string
		replica int
	}{{"worker", 2}, {"scheduler", 1}, {"nope", 0}} {
		_, err := r.eng.replicaContainer(ctx, svcID, tc.process, tc.replica)
		require.ErrorIs(t, err, errNoReplica, "%q/%d", tc.process, tc.replica)
	}
	require.Equal(t, `replica 3 of process "worker" is not running`, noReplicaMessage("worker", 3))
	require.Equal(t, "replica 3 is not running", noReplicaMessage("", 3))
	require.Len(t, r.eng.currentReplicas(ctx, svcID), 5)
	require.Len(t, r.eng.currentProcessReplicas(ctx, svcID, "worker"), 2)
}

func (r *rig) subscribe(stream, process string) agentprotocol.ResultFrame {
	r.t.Helper()
	raw, err := json.Marshal(agentprotocol.LogsSubscribe{StreamID: stream, ServiceID: svcID, Tail: 10, Process: process})
	require.NoError(r.t, err)
	return r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "log-" + stream, Kind: agentprotocol.KindLogsSubscribe, Payload: raw})
}

func (r *rig) streamLines(stream string) []string {
	r.snd.mu.Lock()
	defer r.snd.mu.Unlock()
	var lines []string
	for _, f := range r.snd.logs {
		if f.StreamID != stream {
			continue
		}
		for _, l := range f.Lines {
			lines = append(lines, l.Text)
		}
	}
	sort.Strings(lines)
	return lines
}

func (r *rig) streamEnded(stream string) bool {
	r.snd.mu.Lock()
	defer r.snd.mu.Unlock()
	for _, e := range r.snd.logEnds {
		if e.StreamID == stream {
			return true
		}
	}
	return false
}

func TestLogsFilterAndPrefixByProcess(t *testing.T) {
	r := newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.dk.mu.Lock()
	r.dk.logText[replicaName(depV2, 0)] = "serving"
	r.dk.logText[replicaName(depV2, 1)] = "serving"
	r.dk.logText[pname(depV2, "worker", 0)] = "job a"
	r.dk.logText[pname(depV2, "worker", 1)] = "job b"
	r.dk.logText[pname(depV2, "scheduler", 0)] = "tick"
	r.dk.mu.Unlock()

	// Every process: each line names its process, replicas of a process with several replicas too.
	require.Equal(t, agentprotocol.OutcomeSucceeded, r.subscribe("all", "").Outcome)
	require.Eventually(t, func() bool { return r.streamEnded("all") }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{
		"scheduler | tick",
		"web | [r0] serving", "web | [r1] serving",
		"worker | [r0] job a", "worker | [r1] job b",
	}, r.streamLines("all"))

	// One process: no process prefix, replicas told apart.
	require.Equal(t, agentprotocol.OutcomeSucceeded, r.subscribe("worker", "worker").Outcome)
	require.Eventually(t, func() bool { return r.streamEnded("worker") }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"[r0] job a", "[r1] job b"}, r.streamLines("worker"))

	// A process with one replica needs no replica prefix.
	require.Equal(t, agentprotocol.OutcomeSucceeded, r.subscribe("sched", "scheduler").Outcome)
	require.Eventually(t, func() bool { return r.streamEnded("sched") }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"tick"}, r.streamLines("sched"))

	res2 := r.subscribe("none", "nope")
	require.Equal(t, agentprotocol.OutcomeFailed, res2.Outcome)
	require.Contains(t, res2.Error, `the service has no process "nope" on this host`)
	require.True(t, r.streamEnded("none"))
}

func TestUptimeJudgesEveryProcessAndNamesTheFailingOne(t *testing.T) {
	ok, bad := statusServer(t, 200), statusServer(t, 503)
	r, _ := newUptimeRig(t, nil)
	seed := func(name, process string, port int) {
		c := seedReplica(r, svcID, name, httpCheck, port)
		edit(r, c, func(c *docker.ContainerInfo) { c.Labels[agentprotocol.LabelProcess] = process })
	}
	seed(replicaName(depV2, 0), "web", ok)
	seed(pname(depV2, "worker", 0), "worker", bad)
	r.eng.mu.Lock()
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV2, Replicas: 1, ContainerNames: []string{replicaName(depV2, 0)}, Processes: []desiredProcess{
		{Name: "web", Default: true, Replicas: 1, ContainerNames: []string{replicaName(depV2, 0)}},
		{Name: "worker", Replicas: 1, ContainerNames: []string{pname(depV2, "worker", 0)}},
	}}
	r.eng.mu.Unlock()

	got := cycle(t, r)[svcID]
	require.Equal(t, agentprotocol.ProbeDegraded, got.Outcome)
	require.Equal(t, 2, got.Replicas)
	require.Equal(t, 1, got.Passed)
	require.Contains(t, got.Error, "worker: ")
	require.Contains(t, got.Error, "HTTP 503")
}

func TestRouteSupersedesOnlyByTheProcessThatOwnsIt(t *testing.T) {
	apiID := agentprotocol.ProcessRouteID(svcID, "api")
	loc := desiredEntry{DeploymentID: depV2, Processes: []desiredProcess{
		{Name: "web", Default: true, Replicas: 2, ContainerNames: []string{replicaName(depV2, 0), replicaName(depV2, 1)}},
		{Name: "api", Replicas: 1, ContainerNames: []string{pname(depV2, "api", 0)}},
		{Name: "worker", Replicas: 3, ContainerNames: []string{pname(depV2, "worker", 0), pname(depV2, "worker", 1), pname(depV2, "worker", 2)}},
	}}
	loc.mirrorDefault()
	web := agentprotocol.Route{ServiceID: svcID, Upstreams: []string{replicaName(depV2, 0) + ":80", replicaName(depV2, 1) + ":80"}}
	api := agentprotocol.Route{ServiceID: apiID, Stack: svcID, Upstreams: []string{pname(depV2, "api", 0) + ":8081"}}

	require.False(t, supersedesTable(loc, web), "web's route names web's replicas, not every container of the service")
	require.False(t, supersedesTable(loc, api))
	web.Upstreams = web.Upstreams[:1]
	require.True(t, supersedesTable(loc, web))
	api.Upstreams = []string{pname(depV2, "api", 1) + ":8081"}
	require.True(t, supersedesTable(loc, api))
	require.False(t, supersedesTable(loc, agentprotocol.Route{ServiceID: agentprotocol.ProcessRouteID(svcID, "ghost"), Stack: svcID, Upstreams: []string{"x:1"}}), "a route no process owns")
	require.False(t, supersedesTable(desiredEntry{DeploymentID: depV2, Stack: true}, api), "a compose stack never supersedes")
}

// ---- scale and convergence per process ----

func (r *rig) scaleProcess(p agentprotocol.DeployPayload, process string, replicas int, at time.Time) (agentprotocol.ResultFrame, agentprotocol.DeployResult) {
	r.t.Helper()
	raw, err := json.Marshal(agentprotocol.ScaleServicePayload{
		ServiceID: svcID, DeploymentID: p.DeploymentID, Replicas: replicas, ScaledAt: at, Process: process, Deploy: p,
	})
	require.NoError(r.t, err)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "cmd-scale", Kind: agentprotocol.KindScaleService, Payload: raw})
	var dr agentprotocol.DeployResult
	require.NoError(r.t, json.Unmarshal(res.Data, &dr))
	return res, dr
}

func TestScaleActsOnOneProcess(t *testing.T) {
	r := newRig(t, nil)
	p := processPayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	applies := len(r.px.applies)

	// A port-less process scales without touching the routes.
	res, dr = r.scaleProcess(p, "worker", 3, time.Time{})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.True(t, running(t, r.dk, pname(depV2, "worker", 2)))
	require.Len(t, r.px.applies, applies, "no route phase for a process without a port")
	spec := createdSpec(t, r.dk, pname(depV2, "worker", 2))
	require.Equal(t, "worker", spec.Labels[agentprotocol.LabelProcess])
	require.Contains(t, spec.Env, "NEBULA_PROCESS=worker")
	require.Contains(t, spec.Env, "NEBULA_REPLICA_INDEX=2")
	require.Contains(t, spec.Env, "NEBULA_REPLICA_COUNT=3")
	require.Equal(t, []string{"app", "worker"}, spec.Cmd)
	require.Equal(t, []string{"worker.web"}, spec.Networks[0].Aliases)
	ent := r.eng.local[svcID]
	require.Len(t, ent.Processes, 3, "the other processes keep their entries")
	worker, _ := ent.proc("worker")
	require.Equal(t, 3, worker.Replicas)
	web, _ := ent.proc("")
	require.Equal(t, 2, web.Replicas)
	require.Equal(t, 2, ent.Replicas, "the top level still mirrors the default process")

	// Down again: only the worker's replicas leave.
	res, dr = r.scaleProcess(p, "worker", 1, time.Time{})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.True(t, running(t, r.dk, pname(depV2, "worker", 0)))
	notThere(t, r.dk, pname(depV2, "worker", 1))
	notThere(t, r.dk, pname(depV2, "worker", 2))
	require.True(t, running(t, r.dk, replicaName(depV2, 1)), "web untouched")
	require.True(t, running(t, r.dk, pname(depV2, "scheduler", 0)), "scheduler untouched")

	// A singleton refuses replicas.
	res, dr = r.scaleProcess(p, "scheduler", 2, time.Time{})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "stateless process")
	require.Len(t, createdNames(r.dk), 6, "nothing was created for the refused scale")

	// A process with a port re-derives its own route from the live table.
	res, dr = r.scaleProcess(p, "web", 3, time.Time{})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{
		replicaName(depV2, 0) + ":80", replicaName(depV2, 1) + ":80", replicaName(depV2, 2) + ":80",
	}, upstreamsOf(t, r.px, svcID))

	// The default process may be addressed by an empty name too.
	res, dr = r.scaleProcess(p, "", 2, time.Time{})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	notThere(t, r.dk, replicaName(depV2, 2))

	res, dr = r.scaleProcess(p, "nope", 2, time.Time{})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, `no process "nope"`)
}

func processFrame(p agentprotocol.DeployPayload, replicas map[string]int, at map[string]time.Time) agentprotocol.Reconcile {
	ds := agentprotocol.DesiredService{ServiceID: p.ServiceID, DeploymentID: p.DeploymentID}
	for _, spec := range p.Processes {
		n := spec.Replicas
		if v, ok := replicas[spec.Name]; ok {
			n = v
		}
		dp := agentprotocol.DesiredProcess{
			Name: spec.Name, Default: spec.Default, Replicas: n, ScaledAt: at[spec.Name],
			ContainerNames: agentprotocol.ProcessContainerNames(p.ServiceSlug, p.DeploymentID, spec.Name, spec.Default, n),
		}
		ds.Processes = append(ds.Processes, dp)
		if spec.Default {
			ds.Replicas, ds.ContainerNames, ds.ScaledAt = dp.Replicas, dp.ContainerNames, dp.ScaledAt
			ups := make([]string, n)
			for i, name := range dp.ContainerNames {
				ups[i] = name + ":80"
			}
			ds.Routes = []agentprotocol.Route{{ServiceID: p.ServiceID, Hosts: p.Hosts, Upstreams: ups, HealthPath: "/"}}
		}
	}
	return agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{ds}}
}

func TestReconcileConvergesEachProcessToItsOwnCount(t *testing.T) {
	r := newRig(t, nil)
	p := processPayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	created := len(r.dk.created)

	require.NoError(t, r.eng.Reconcile(context.Background(), processFrame(p, map[string]int{"worker": 3}, nil)))
	r.eng.bg.Wait()

	require.Len(t, r.dk.created, created+1, "only the worker gained a replica")
	require.True(t, running(t, r.dk, pname(depV2, "worker", 2)))
	spec := r.dk.created[created]
	require.Equal(t, pname(depV2, "worker", 2), spec.Name)
	require.Contains(t, spec.Env, "NEBULA_PROCESS=worker")
	require.Contains(t, spec.Env, "NEBULA_REPLICA_INDEX=2")
	require.Contains(t, spec.Env, "NEBULA_REPLICA_COUNT=3")
	ent := r.eng.local[svcID]
	worker, _ := ent.proc("worker")
	require.Equal(t, 3, worker.Replicas)
	require.Len(t, worker.ContainerNames, 3)
	for _, n := range []string{replicaName(depV2, 0), replicaName(depV2, 1), pname(depV2, "scheduler", 0)} {
		require.True(t, running(t, r.dk, n), n)
	}

	// And back down.
	require.NoError(t, r.eng.Reconcile(context.Background(), processFrame(p, map[string]int{"worker": 1}, nil)))
	r.eng.bg.Wait()
	notThere(t, r.dk, pname(depV2, "worker", 1))
	notThere(t, r.dk, pname(depV2, "worker", 2))
	require.True(t, running(t, r.dk, pname(depV2, "worker", 0)))
}

func TestStaleFrameDoesNotUndoAProcessScaleButOtherProcessesStillConverge(t *testing.T) {
	r := newRig(t, nil)
	p := processPayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	older, newer := time.Now().Add(-time.Minute), time.Now()

	res, dr = r.scaleProcess(p, "worker", 1, newer)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	notThere(t, r.dk, pname(depV2, "worker", 1))
	created := len(r.dk.created)

	// The frame was built before the scale: the worker count it carries is old.
	// The web count in it has no stamp that is older than anything, so it applies.
	frame := processFrame(p, map[string]int{"worker": 2, "web": 3}, map[string]time.Time{"worker": older, "web": newer.Add(time.Minute)})
	require.NoError(t, r.eng.Reconcile(context.Background(), frame))
	r.eng.bg.Wait()

	notThere(t, r.dk, pname(depV2, "worker", 1))
	require.True(t, running(t, r.dk, replicaName(depV2, 2)), "web converged to the frame's count")
	require.Len(t, r.dk.created, created+1)
	worker, _ := r.eng.local[svcID].proc("worker")
	require.Equal(t, 1, worker.Replicas, "the scale is final until a newer frame replaces it")
	desiredWorker, _ := r.eng.desired[svcID].proc("worker")
	require.Equal(t, 1, desiredWorker.Replicas)
}

func TestFrameEntryMirrorsTheDefaultProcess(t *testing.T) {
	p := processPayload()
	ent := frameEntry(processFrame(p, map[string]int{"web": 3}, nil).Services[0])
	require.Len(t, ent.Processes, 3)
	require.Equal(t, 3, ent.Replicas)
	require.Len(t, ent.ContainerNames, 3)

	legacy := frameEntry(desiredFor(basePayload(), 2).Services[0])
	require.Empty(t, legacy.Processes)
	require.Equal(t, 2, legacy.Replicas)
}

func TestProcessLogsAreStoredWithTheProcessPrefix(t *testing.T) {
	// The collector's prefix rule is covered in logstore; here the label it keys on.
	r := newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	c, _ := r.dk.state(pname(depV2, "worker", 0))
	require.Equal(t, "worker", c.Labels[agentprotocol.LabelProcess])
	require.Empty(t, c.Labels[agentprotocol.LabelComponent])
}
