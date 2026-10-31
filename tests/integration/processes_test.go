//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// processView indexes the `processes` of a service view by name.
func processView(sv J) map[string]J {
	out := map[string]J{}
	arr, _ := sv["processes"].([]any)
	for _, a := range arr {
		m, _ := a.(map[string]any)
		out[J(m).Str("name")] = J(m)
	}
	return out
}

// containersByProcess groups the running containers of a service by their
// nebulactrl.process label ("" for containers without one).
func (s *Stack) containersByProcess(serviceID string) map[string][]string {
	out := map[string][]string{}
	for _, c := range s.RunningContainers(serviceID) {
		p := c.Labels[agentprotocol.LabelProcess]
		out[p] = append(out[p], c.Name)
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

// deploymentContainers lists every container (any state) of a deployment.
func (s *Stack) deploymentContainers(serviceID, deploymentID string) []string {
	var out []string
	for _, c := range s.Containers(serviceID) {
		if c.Labels[agentprotocol.LabelDeployment] == deploymentID {
			out = append(out, c.Name)
		}
	}
	return out
}

// firstText reads the first text message of a terminal session.
func (c *termConn) firstText(timeout time.Duration) J {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		mt, data, err := c.ws.Read(ctx)
		require.NoError(c.t, err, "waiting for a text message")
		if mt != websocket.MessageText {
			continue
		}
		var m J
		require.NoError(c.t, json.Unmarshal(data, &m))
		return m
	}
}

// TestProcessesDeployRollRollback: one service, three processes from one image: the
// default process "web" on a port, "worker" (no port, two replicas) and "clock" (a
// singleton). They deploy together in order, scale one by one, a singleton never
// runs two instances, a failing process fails the whole deployment, and a rollback
// reverts every process in one deployment.
func TestProcessesDeployRollRollback(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	app := appFixture(t)
	s := SharedStack(t)

	// The clock tag is part of its command: changing it is a release of its own.
	procs := func(workerTag, clockTag string) []map[string]any {
		return []map[string]any{
			{"name": "worker", "startCommand": []string{"worker", workerTag}, "healthcheck": map[string]any{"type": "none"}, "stopGraceSeconds": 5},
			{"name": "clock", "startCommand": []string{"worker", clockTag}, "singleton": true, "healthcheck": map[string]any{"type": "none"}, "stopGraceSeconds": 5},
		}
	}
	config := func(workerTag, clockTag string) map[string]any {
		return map[string]any{
			"image": app.AppImage, "port": 8080, "stopGraceSeconds": 5,
			"healthcheck":       map[string]any{"type": "http", "path": "/health", "intervalSeconds": 1, "deadlineSeconds": 30, "successThreshold": 1},
			"processes":         procs(workerTag, clockTag),
			"preDeployCommand":  []string{"/app", "task", "0", "predeploy-ran"},
			"postDeployCommand": []string{"/app", "task", "0", "postdeploy-ran"},
		}
	}
	svc := s.NewService(SvcSpec{
		Name: "multi", Image: app.AppImage, Domain: s.Host(),
		Vars:   map[string]string{"VERSION": "v1", "GRACE_MS": "0"},
		Config: map[string]any{"processes": procs("w1", "c1"), "preDeployCommand": config("w1", "c1")["preDeployCommand"], "postDeployCommand": config("w1", "c1")["postDeployCommand"]},
	})
	patch := func(workerTag, clockTag string) {
		t.Helper()
		r := s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": config(workerTag, clockTag)}, "If-Match", s.serviceETag(svc.ID))
		require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	}
	px := NewProxyClient(s.Host())

	// The worker's replica count is a setting of its own and may be stored before the first deploy.
	r := s.Do("PUT", "/services/"+svc.ID+"/processes/worker/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// 1. Deploy: every container carries its process, the names follow the scheme.
	d1 := s.DeployOK(svc.ID)
	d1ID := d1.Str("id")
	px.WaitBody(t, "/", "v1", 30*time.Second)
	byProc := s.containersByProcess(svc.ID)
	require.Len(t, byProc["web"], 1, "%v", byProc)
	require.Len(t, byProc["worker"], 2, "%v", byProc)
	require.Len(t, byProc["clock"], 1, "%v", byProc)
	require.Len(t, byProc, 3, "no container without a process label: %v", byProc)
	require.Equal(t, []string{agentprotocol.ProcessContainerName(svc.Slug, d1ID, "web", true, 0)}, byProc["web"])
	require.Equal(t, agentprotocol.ProcessContainerNames(svc.Slug, d1ID, "worker", false, 2), byProc["worker"])
	require.Equal(t, []string{agentprotocol.ProcessContainerName(svc.Slug, d1ID, "clock", false, 0)}, byProc["clock"])
	for _, c := range s.RunningContainers(svc.ID) {
		require.Contains(t, c.Env, "NEBULA_PROCESS="+c.Labels[agentprotocol.LabelProcess])
	}
	logs := s.DeploymentLogs(d1ID)
	require.Equal(t, 1, strings.Count(logs, "predeploy-ran"), "the pre-deploy command runs once per deployment:\n"+logs)
	require.Equal(t, 1, strings.Count(logs, "postdeploy-ran"), "the post-deploy command runs once per deployment:\n"+logs)

	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		sv := s.Service(svc.ID)
		ps := processView(sv)
		observed, _ := sv.Obj("observed")["processes"].([]any)
		if len(ps) != 3 || len(observed) != 3 {
			return false
		}
		for _, name := range []string{"web", "worker", "clock"} {
			if o := ps[name].Obj("observed"); o.Str("state") != agentprotocol.StateRunning || o.Str("health") != agentprotocol.HealthHealthy {
				return false
			}
		}
		return ps["worker"].Obj("observed").Int("replicasReady") == 2
	}), "processes never became healthy: %v", s.Service(svc.ID)["processes"])
	ps := processView(s.Service(svc.ID))
	require.True(t, ps["web"].Bool("default"))
	require.False(t, ps["worker"].Bool("default"))
	require.Equal(t, 2, ps["worker"].Obj("scaling").Int("replicas"))
	require.False(t, ps["clock"].Obj("scaling").Bool("eligible"), "a singleton does not scale")

	// 2. Logs: one process' lines, or every process' lines prefixed with the process.
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		lr := s.Do("GET", "/services/"+svc.ID+"/logs?process=worker&tail=200", nil)
		return lr.Status == http.StatusOK && strings.Contains(string(lr.Raw), "process=worker")
	}), "no worker lines")
	lr := s.Do("GET", "/services/"+svc.ID+"/logs?process=worker&tail=200", nil)
	require.NotContains(t, string(lr.Raw), "process=clock")
	require.NotContains(t, string(lr.Raw), "listening on", "the web process' lines are filtered out")
	all := s.Do("GET", "/services/"+svc.ID+"/logs?tail=200", nil)
	require.Equal(t, http.StatusOK, all.Status, string(all.Raw))
	require.Contains(t, string(all.Raw), "worker | ")
	require.Contains(t, string(all.Raw), "clock | ")
	require.Equal(t, http.StatusUnprocessableEntity, s.Do("GET", "/services/"+svc.ID+"/logs?process=nope&tail=10", nil).Status)

	// 3. Scale one process; the others are not touched.
	web0, clock0 := byProc["web"][0], byProc["clock"][0]
	r = s.Do("PUT", "/services/"+svc.ID+"/processes/worker/scaling", map[string]any{"replicas": 3})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, eventually(90*time.Second, time.Second, func() bool { return len(s.containersByProcess(svc.ID)["worker"]) == 3 }), "%v", s.containersByProcess(svc.ID))
	now := s.containersByProcess(svc.ID)
	require.Equal(t, []string{web0}, now["web"])
	require.Equal(t, []string{clock0}, now["clock"])
	r = s.Do("PUT", "/services/"+svc.ID+"/processes/worker/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, eventually(90*time.Second, time.Second, func() bool { return len(s.containersByProcess(svc.ID)["worker"]) == 2 }), "%v", s.containersByProcess(svc.ID))
	r = s.Do("PUT", "/services/"+svc.ID+"/processes/clock/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "replicas_require_stateless", r.JSON().Str("code"))
	r = s.Do("PUT", "/services/"+svc.ID+"/processes/nope/scaling", map[string]any{"replicas": 2})
	require.Equal(t, http.StatusNotFound, r.Status, string(r.Raw))

	// 4. A new clock command: the old instance is gone before the new one starts,
	// the web process is served throughout, pre- and post-deploy run once.
	patch("w1", "c2")
	var (
		mu      sync.Mutex
		maxClok int
		stop    = make(chan struct{})
		done    = make(chan struct{})
	)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			n := len(s.containersByProcess(svc.ID)["clock"])
			mu.Lock()
			maxClok = max(maxClok, n)
			mu.Unlock()
		}
	}()
	load := StartLoad(px, "/", 20*time.Millisecond)
	load.WaitMore(t, 40, 30*time.Second)
	d2 := s.WaitDeployment(s.Deploy(svc.ID), 4*time.Minute)
	require.Equal(t, "succeeded", d2.Str("status"), "%v\n%s", d2, s.DeploymentLogs(d2.Str("id")))
	load.WaitMore(t, 40, 30*time.Second)
	res := load.Stop()
	close(stop)
	<-done
	d2ID := d2.Str("id")
	require.LessOrEqual(t, maxClok, 1, "a singleton never runs two instances")
	require.Zero(t, res.Non2xx, "web was not served continuously: %s", res.Summary())
	require.Zero(t, res.Errors, "web was not served continuously: %s", res.Summary())
	logs = s.DeploymentLogs(d2ID)
	require.Equal(t, 1, strings.Count(logs, "predeploy-ran"), logs)
	require.Equal(t, 1, strings.Count(logs, "postdeploy-ran"), logs)
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		b := s.containersByProcess(svc.ID)
		return len(b["web"]) == 1 && len(b["worker"]) == 2 && len(b["clock"]) == 1 && len(s.RunningContainers(svc.ID)) == 4 &&
			strings.Contains(b["clock"][0], d2ID[:8])
	}), "the old release' containers were not retired: %v", s.containersByProcess(svc.ID))
	for _, c := range s.RunningContainers(svc.ID) {
		require.Equal(t, d2ID, c.Labels[agentprotocol.LabelDeployment])
		if c.Labels[agentprotocol.LabelProcess] == "clock" {
			require.Equal(t, []string{"worker", "c2"}, c.Cmd)
		}
	}

	// 5a. The web process fails its health check: it goes first, so the workers and
	// the clock of the new release are never created and the old release serves.
	s.SetVars(svc.ID, map[string]string{"FAIL_HEALTH": "1"}, nil)
	d3 := s.WaitDeployment(s.Deploy(svc.ID), 4*time.Minute)
	require.Equal(t, "failed", d3.Str("status"), "%v\n%s", d3, s.DeploymentLogs(d3.Str("id")))
	require.True(t, eventually(30*time.Second, time.Second, func() bool { return len(s.deploymentContainers(svc.ID, d3.Str("id"))) == 0 }),
		"containers of the failed deployment remain: %v", s.deploymentContainers(svc.ID, d3.Str("id")))
	require.Equal(t, 4, len(s.RunningContainers(svc.ID)), "the previous release keeps running: %v", s.containersByProcess(svc.ID))
	for _, c := range s.RunningContainers(svc.ID) {
		require.Equal(t, d2ID, c.Labels[agentprotocol.LabelDeployment])
	}
	px.WaitBody(t, "/", "v1", 15*time.Second)

	// 5b. The worker fails after web switched and the clock was replaced: the whole
	// deployment unwinds, routes go back to the previous web, the new containers go away.
	s.SetVars(svc.ID, map[string]string{"FAIL_HEALTH": "0", "CRASH_PROCESS": "worker"}, nil)
	d4 := s.WaitDeployment(s.Deploy(svc.ID), 4*time.Minute)
	require.Equal(t, "rolled_back", d4.Str("status"), "%v\n%s", d4, s.DeploymentLogs(d4.Str("id")))
	require.Contains(t, d4.Str("statusMessage"), "worker")
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		b := s.containersByProcess(svc.ID)
		return len(s.deploymentContainers(svc.ID, d4.Str("id"))) == 0 && len(b["web"]) == 1 && len(b["worker"]) == 2 && len(b["clock"]) == 1
	}), "the unwind left %v (new: %v)", s.containersByProcess(svc.ID), s.deploymentContainers(svc.ID, d4.Str("id")))
	for _, c := range s.RunningContainers(svc.ID) {
		require.Equal(t, d2ID, c.Labels[agentprotocol.LabelDeployment], "the old singleton is back, nothing of the new release runs")
	}
	px.WaitBody(t, "/", "v1", 15*time.Second)
	evs := s.Do("GET", "/deployments/"+d4.Str("id")+"/events", nil).JSON().Items()
	var failedProcess bool
	for _, e := range evs {
		if e.Str("step") == "release" && e.Str("status") == "failed" && strings.Contains(e.Str("message"), "[worker]") {
			failedProcess = true
		}
	}
	require.True(t, failedProcess, "the timeline names the failed process: %v", evs)
	s.SetVars(svc.ID, map[string]string{"CRASH_PROCESS": "none"}, nil)

	// 6. Rollback to the first deployment: every process runs the first release again,
	// in one deployment.
	before := len(s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items())
	r = s.Do("POST", "/deployments/"+d1ID+"/rollback", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rb := s.WaitDeployment(r.JSON().Str("id"), 4*time.Minute)
	require.Equal(t, "succeeded", rb.Str("status"), "rollback: %v\n%s", rb, s.DeploymentLogs(rb.Str("id")))
	require.Equal(t, before+1, len(s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items()), "one deployment row")
	require.Equal(t, d1.Obj("release").Str("id"), rb.Obj("release").Str("id"))
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		b := s.containersByProcess(svc.ID)
		return len(b["web"]) == 1 && len(b["worker"]) == 2 && len(b["clock"]) == 1 && len(s.RunningContainers(svc.ID)) == 4
	}), "%v", s.containersByProcess(svc.ID))
	for _, c := range s.RunningContainers(svc.ID) {
		require.Equal(t, rb.Str("id"), c.Labels[agentprotocol.LabelDeployment])
		switch c.Labels[agentprotocol.LabelProcess] {
		case "clock":
			require.Equal(t, []string{"worker", "c1"}, c.Cmd, "the first release' command")
		case "worker":
			require.Equal(t, []string{"worker", "w1"}, c.Cmd)
		}
	}

	// 7. Terminal: a process and replica pick the container; an unknown process is not found.
	c := s.dialTerminal(svc.ID)
	c.sendJSON(map[string]any{"type": "open", "process": "nope", "replica": 0, "cols": 80, "rows": 24, "confirmProduction": true})
	require.Equal(t, "not_found", c.firstText(15*time.Second).Str("code"))
	c = s.dialTerminal(svc.ID)
	c.sendJSON(map[string]any{"type": "open", "process": "worker", "replica": 1, "cols": 80, "rows": 24, "confirmProduction": true})
	// The fixture image has no shell: the session either opens on the worker's second
	// replica or fails to start the shell there, but it never reports "not found".
	first := c.firstText(30 * time.Second)
	if first.Str("type") == "error" {
		require.NotEqual(t, "not_found", first.Str("code"), "%v", first)
	} else {
		require.Equal(t, agentprotocol.ProcessContainerName(svc.Slug, rb.Str("id"), "worker", false, 1), first.Str("container"))
	}

	// 8. An agent without the processes capability is refused before anything is persisted.
	var facts string
	require.NoError(t, s.Pool.QueryRow(context.Background(), "SELECT facts::text FROM servers WHERE id = $1", s.ServerID).Scan(&facts))
	_, err := s.Pool.Exec(context.Background(),
		`UPDATE servers SET facts = jsonb_set(facts, '{capabilities}', (facts->'capabilities') - $2::text) WHERE id = $1`, s.ServerID, agentprotocol.CapProcesses)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = s.Pool.Exec(context.Background(), "UPDATE servers SET facts = $2::jsonb WHERE id = $1", s.ServerID, facts)
	})
	rows := len(s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items())
	r = s.Do("POST", "/services/"+svc.ID+"/deployments", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "agent_too_old", r.JSON().Str("code"))
	require.Equal(t, rows, len(s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items()), "nothing was persisted")
	plan := s.Do("POST", "/services/"+svc.ID+"/deployments/plan", map[string]any{})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	var blocked bool
	for _, b := range plan.JSON()["blocking"].([]any) {
		blocked = blocked || J(b.(map[string]any)).Str("code") == "agent_too_old"
	}
	require.True(t, blocked, "the plan reports the blocker: %s", plan.Raw)
	_, err = s.Pool.Exec(context.Background(), "UPDATE servers SET facts = $2::jsonb WHERE id = $1", s.ServerID, facts)
	require.NoError(t, err)
}
