//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// adoptOrphanAge is the orphan-sweep age of the adoption test's agent: every
// container is older than this by the time the new control plane meets it, so a
// missing hold would remove it on the very first reconcile.
const adoptOrphanAge = 2 * time.Second

// newSecondControlPlane starts a further control plane on a fresh database: the
// instance a lost one is replaced by (or the one a server moves to). It has an
// admin session, no project and one pending server with its enrollment token.
// The containers a test runs through it are cleaned up by the first stack,
// which owns their service and environment IDs.
func newSecondControlPlane(t *testing.T) *Stack {
	t.Helper()
	pool := testutil.NewPool(t)
	cp, err := startControlPlane(pool, false)
	require.NoError(t, err)
	t.Cleanup(cp.stop)
	b := &Stack{t: t, Pool: pool, App: cp.App, Srv: cp.Srv, client: cp.Client, Fault: &ProbeFault{}, hostBase: hostBase(t.Name())}
	b.ServerID, b.EnrollTok = b.EnrollmentToken("moved-host")
	return b
}

// reconcileSentinel creates a stopped helper container of a host-shell session
// nobody holds. The agent's reconcile removes such helpers (sweepHostShells)
// right after the orphan sweep, so once it is gone a whole reconcile ran past
// sweepOrphans: that is how the test knows the sweep that must have spared the
// workloads has happened, without sleeping.
func reconcileSentinel(t *testing.T, app *fixtureState, envID, name string) (gone func() bool) {
	t.Helper()
	ctx := context.Background()
	session := "itest-" + name
	_, err := sh(ctx, "", "docker", "create", "--name", "nc-itest-sentinel-"+name,
		"--label", agentprotocol.LabelManaged+"="+agentprotocol.LabelManagedHostShell,
		"--label", agentprotocol.LabelHostShellSession+"="+session,
		"--label", agentprotocol.LabelEnvironment+"="+envID,
		app.AppImage)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", "nc-itest-sentinel-"+name) })
	return func() bool {
		cs, err := dockerEng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{agentprotocol.LabelHostShellSession: session}})
		return err == nil && len(cs) == 0
	}
}

// adoptArr reads a JSON array of objects.
func adoptArr(j J, key string) []J {
	arr, _ := j[key].([]any)
	out := make([]J, 0, len(arr))
	for _, v := range arr {
		m, _ := v.(map[string]any)
		out = append(out, J(m))
	}
	return out
}

// planServices indexes the services of an adoption plan by ID.
func planServices(plan J) map[string]J {
	out := map[string]J{}
	for _, env := range adoptArr(plan, "environments") {
		for _, sv := range adoptArr(env, "services") {
			out[sv.Str("id")] = sv
		}
	}
	return out
}

// containerEnv returns the environment of a container as KEY=VALUE lines. The
// values are secrets: callers compare them and never print them.
func containerEnv(t *testing.T, id string) []string {
	t.Helper()
	out, err := sh(context.Background(), "", "docker", "inspect", "-f", "{{json .Config.Env}}", id)
	require.NoError(t, err)
	var env []string
	require.NoError(t, json.Unmarshal([]byte(out), &env))
	return env
}

// Adopt a server that already runs NebulaCtrl workloads (the lost-control-plane
// case, automating the manual check of the adoption plan): a control plane A
// deploys a two-replica app behind a domain, a second service and a third, then
// A and its agent go away. A fresh control plane B gets the same host through
// the same agent state dir and a new enrollment token. The agent is told "unknown
// agent" (4404), re-enrolls with the token and restarts like systemd would; B
// holds the server because it runs workloads it does not know. With the orphan
// sweep set to 2 s a missing hold would remove every container on the first
// reconcile. The review lists the services, the apply rebuilds them with the
// IDs found on the containers (nothing recreated, no deployment), a service
// chosen `leave` keeps running unmanaged, one chosen `remove` is removed once
// the hold ends, and a normal redeploy from B replaces the adopted containers.
func TestAdoptServerKeepsRunningContainers(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	a := ExclusiveStack(t, StackOptions{NoAgent: true})
	const secret = "adopt-itest-secret-98765"

	agentOn := func(url, token string) *Agent {
		return MustStartAgent(t, AgentOptions{
			StateDir: a.StateDir, URL: url, Token: token, Fault: a.Fault,
			OrphanMinAge: adoptOrphanAge, ScopeEnv: a.EnvID,
		})
	}
	a.Agent = agentOn(a.URL(), a.EnrollTok)
	a.WaitServerOnline(60 * time.Second)
	require.Empty(t, a.Do("GET", "/servers/"+a.ServerID, nil).JSON().Obj("adoption"), "a host with nothing on it is not held")

	// ---- 1. Instance A runs the workloads --------------------------------
	host := a.Host()
	web := a.NewService(SvcSpec{Name: "web", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"},
		Secrets: map[string]string{"SESSION_SECRET": secret}, Domain: host})
	keep := a.NewService(SvcSpec{Name: "keep", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}})
	gone := a.NewService(SvcSpec{Name: "gone", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}})
	a.SetReplicas(web.ID, 2)
	webDep := a.DeployOK(web.ID)
	a.DeployOK(keep.ID)
	a.DeployOK(gone.ID)

	// snapshot maps container name → ID of a service's containers, all running.
	snapshot := func(svc Service) map[string]string {
		out := map[string]string{}
		for _, c := range a.Containers(svc.ID) {
			require.True(t, c.Running, "container %s of %s is not running", c.Name, svc.Slug)
			out[c.Name] = c.ID
		}
		return out
	}
	webBefore, keepBefore, goneBefore := snapshot(web), snapshot(keep), snapshot(gone)
	require.Len(t, webBefore, 2)
	require.Len(t, keepBefore, 1)
	require.Len(t, goneBefore, 1)

	px := NewProxyClient(host)
	px.WaitBody(t, "/", "v1", 30*time.Second)

	// The domain is polled from here to the end of the adoption: the hold must
	// keep the route serving through the agent's restarts and the re-enrollment.
	load := StartLoad(px, "/", 100*time.Millisecond)
	var stopLoad sync.Once
	var loadRes LoadResult
	stop := func() { stopLoad.Do(func() { loadRes = load.Stop() }) }
	t.Cleanup(stop)
	load.WaitMore(t, 10, 30*time.Second)

	// ---- 2. Instance A is lost ------------------------------------------
	a.Agent.Stop() // the workloads and Traefik keep running
	a.Srv.Close()  // control plane A is gone for good

	// ---- 3. A new instance, the same host --------------------------------
	b := newSecondControlPlane(t)

	// The agent still holds A's identity and gets B's token, as after
	// `install-agent.sh --url <B> --token <new>`: B answers 4404 "unknown agent",
	// the agent enrolls with the token and ends with ErrReenrolled (cmd/agent exits
	// 75 and systemd starts it again).
	sentinel1 := reconcileSentinel(t, app, a.EnvID, "one")
	first := agentOn(b.URL(), b.EnrollTok)
	require.Equal(t, a.ServerID, first.Identity.ServerID, "the state dir still holds A's identity")
	select {
	case <-first.Done():
	case <-time.After(60 * time.Second):
		t.Fatal("the agent did not re-enroll with the new control plane")
	}
	require.ErrorIs(t, first.Err(), conn.ErrReenrolled)

	restarted := agentOn(b.URL(), b.EnrollTok) // systemd's restart: the stored identity is B's now
	require.Equal(t, b.ServerID, restarted.Identity.ServerID)
	require.NotEqual(t, a.ServerID, restarted.Identity.ServerID)

	// ---- 4. B holds the server and leaves everything alone ---------------
	srv := b.WaitServer(b.ServerID, 60*time.Second)
	require.Equal(t, "held", srv.Obj("adoption").Str("state"), "server: %v", srv)
	require.Equal(t, 3, srv.Obj("adoption").Int("found"), "the host runs three services: %v", srv.Obj("adoption"))
	require.Equal(t, "held", b.adoptionState())
	require.True(t, eventually(60*time.Second, 500*time.Millisecond, sentinel1),
		"the agent never completed a reconcile with the new control plane")
	require.Equal(t, webBefore, snapshot(web), "the held server's containers were touched")
	require.Equal(t, keepBefore, snapshot(keep))
	require.Equal(t, goneBefore, snapshot(gone))

	// A held server takes no new service until the workloads are reviewed.
	b.createProject("Placement")
	r := b.Do("POST", "/environments/"+b.EnvID+"/services", map[string]any{
		"name": "nope", "serverId": b.ServerID, "sourceType": "image",
		"config": map[string]any{"image": app.AppImage, "port": 8080},
	})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "server_adoption_pending")

	// The agent restarts once more while the hold is still on (a known server's
	// hello does not hold again; the stored hold keeps the frame's KeepUnknown).
	sentinel2 := reconcileSentinel(t, app, a.EnvID, "two")
	restarted.Stop()
	restarted = agentOn(b.URL(), b.EnrollTok)
	b.WaitServer(b.ServerID, 60*time.Second)
	require.True(t, eventually(60*time.Second, 500*time.Millisecond, sentinel2), "no reconcile after the second restart")
	require.Equal(t, "held", b.adoptionState())
	require.Equal(t, webBefore, snapshot(web))
	require.Equal(t, keepBefore, snapshot(keep))
	require.Equal(t, goneBefore, snapshot(gone))

	// ---- 5. Review and adopt ---------------------------------------------
	planPath := "/servers/" + b.ServerID + "/adoption/plan"
	plan := b.Do("POST", planPath, map[string]any{})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.False(t, bytes.Contains(plan.Raw, []byte(secret)), "the plan carries a variable value")
	pj := plan.JSON()
	require.True(t, pj.Bool("held"))
	require.Empty(t, pj["blockers"], "blockers: %v", pj["blockers"])
	envs := adoptArr(pj, "environments")
	require.Len(t, envs, 1)
	require.Equal(t, a.EnvID, envs[0].Str("id"), "the environment keeps the ID found on the containers")
	require.Equal(t, "create", envs[0].Str("action"))
	require.Equal(t, "create", envs[0].Str("projectAction"))
	require.Equal(t, 3, pj.Obj("totals").Int("adopt"), "totals: %v", pj.Obj("totals"))
	require.Equal(t, 4, pj.Obj("totals").Int("containers"))
	svcs := planServices(pj)
	require.Len(t, svcs, 3)
	for _, svc := range []Service{web, keep, gone} {
		sv := svcs[svc.ID]
		require.NotNil(t, sv, "service %s is not in the plan", svc.Slug)
		require.Equal(t, svc.Slug, sv.Str("slug"))
		require.Equal(t, "adopt", sv.Str("action"), "%s: %v", svc.Slug, sv)
		require.True(t, sv.Bool("adoptable"), "%s: %v", svc.Slug, sv)
		require.Equal(t, "app", sv.Str("kind"))
		// The fixture image also carries the tags of earlier runs' registries: the
		// reference the deployment ran (the spec label's) must be the one taken.
		require.Equal(t, app.AppImage, sv.Str("image"))
	}
	w := svcs[web.ID]
	require.Equal(t, 2, w.Int("replicas"))
	require.Len(t, adoptArr(w, "containers"), 2)
	require.Equal(t, webDep.Str("id"), w.Str("deploymentId"))
	require.Equal(t, webDep.Str("releaseId"), w.Str("releaseId"))
	doms := adoptArr(w, "domains")
	require.Len(t, doms, 1, "domains: %v", w["domains"])
	require.Equal(t, host, doms[0].Str("host"))
	require.True(t, doms[0].Bool("include"), "domain: %v", doms[0])
	vars := map[string]J{}
	for _, v := range adoptArr(w, "env") {
		vars[v.Str("key")] = v
	}
	require.Contains(t, vars, "VERSION")
	require.Contains(t, vars, "SESSION_SECRET")
	require.True(t, vars["SESSION_SECRET"].Bool("secret"), "SESSION_SECRET is not a secret: %v", vars["SESSION_SECRET"])
	for k := range vars {
		require.False(t, k == "PORT" || strings.HasPrefix(k, "NEBULA_"), "platform variable %s is part of the plan", k)
	}

	// Keep one service running unmanaged, remove another.
	choices := map[string]any{"choices": map[string]any{"services": map[string]any{
		keep.ID: map[string]any{"action": "leave"},
		gone.ID: map[string]any{"action": "remove"},
	}}}
	plan = b.Do("POST", planPath, choices)
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	pj = plan.JSON()
	require.Empty(t, pj["blockers"], "blockers: %v", pj["blockers"])
	require.Equal(t, map[string]any{"adopt": 1.0, "leave": 1.0, "remove": 1.0, "containers": 4.0}, map[string]any(pj.Obj("totals")))

	adopt := b.Do("POST", "/servers/"+b.ServerID+"/adoption", choices, "Idempotency-Key", "itest-adopt-1")
	require.Equal(t, http.StatusCreated, adopt.Status, string(adopt.Raw))
	res := adopt.JSON()
	require.Len(t, adoptArr(res, "projects"), 1)
	require.Equal(t, 1, res.Int("left"))
	require.Equal(t, 1, res.Int("removed"))
	require.Equal(t, 0, res.Int("reattached"))
	adopted := adoptArr(res, "services")
	require.Len(t, adopted, 1)
	require.Equal(t, web.ID, adopted[0].Str("id"))
	require.Equal(t, a.EnvID, adopted[0].Str("environmentId"))

	// ---- 5b. The rows carry the IDs of the containers --------------------
	require.Empty(t, b.adoptionState(), "the hold is over")
	srv = b.WaitServer(b.ServerID, 60*time.Second)
	require.Equal(t, "", srv.Obj("adoption").Str("state"))
	require.Equal(t, 1, srv.Obj("adoption").Int("unmanaged"), "adoption: %v", srv.Obj("adoption"))

	var relID, trigger, status, current, envID, serverID string
	require.NoError(t, b.Pool.QueryRow(context.Background(),
		`SELECT d.release_id::text, d.trigger, d.status, s.current_deployment_id::text, s.environment_id::text, s.server_id::text
		   FROM services s JOIN deployments d ON d.service_id = s.id WHERE s.id = $1`, web.ID).
		Scan(&relID, &trigger, &status, &current, &envID, &serverID))
	require.Equal(t, webDep.Str("releaseId"), relID, "the release keeps its ID")
	require.Equal(t, "adopt", trigger)
	require.Equal(t, "succeeded", status)
	require.Equal(t, webDep.Str("id"), current, "the deployment keeps its ID")
	require.Equal(t, a.EnvID, envID)
	require.Equal(t, b.ServerID, serverID)
	var deployments, commands int
	require.NoError(t, b.Pool.QueryRow(context.Background(), `SELECT count(*) FROM deployments`).Scan(&deployments))
	require.NoError(t, b.Pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_commands WHERE kind = 'deploy'`).Scan(&commands))
	require.Equal(t, 1, deployments, "only the adopted deployment exists")
	require.Zero(t, commands, "adopting ran no deploy")
	for _, id := range []string{keep.ID, gone.ID} {
		var n int
		require.NoError(t, b.Pool.QueryRow(context.Background(), `SELECT count(*) FROM services WHERE id = $1`, id).Scan(&n))
		require.Zero(t, n, "a service that was left or removed must not be created")
	}
	sv := b.Service(web.ID)
	require.Equal(t, web.Slug, sv.Str("slug"))
	require.Equal(t, webDep.Str("id"), sv.Str("currentDeploymentId"))
	require.False(t, sv.Bool("hasStagedChanges"), "an adopted service shows no staged change: %v", sv)
	require.Equal(t, 2, sv.Obj("scaling").Int("replicas"))
	require.NotEmpty(t, b.domainID(web.ID, host))
	vr := b.Do("GET", "/services/"+web.ID+"/variables", nil)
	require.Equal(t, http.StatusOK, vr.Status, string(vr.Raw))
	require.True(t, bytes.Contains(vr.Raw, []byte("SESSION_SECRET")))
	require.False(t, bytes.Contains(vr.Raw, []byte(secret)), "a secret variable is readable through the API")
	require.Equal(t, "succeeded", b.Deployment(webDep.Str("id")).Str("status"))
	require.Equal(t, "adopt", b.Deployment(webDep.Str("id")).Str("trigger"))

	// ---- 6. Nothing was recreated, the removed service goes --------------
	// The hold is over and the sweep is live (2 s): the service chosen `remove`
	// is the control that the same agent really removes what it is allowed to.
	require.True(t, eventually(60*time.Second, 500*time.Millisecond, func() bool { return len(a.Containers(gone.ID)) == 0 }),
		"the removed service's containers are still on the host")
	require.Equal(t, webBefore, snapshot(web), "adoption recreated the app's containers")
	require.Equal(t, keepBefore, snapshot(keep), "the left service's containers were touched")
	px.WaitBody(t, "/", "v1", 30*time.Second)
	stop()
	require.Greater(t, loadRes.Total, 100, "the load generator barely ran")
	require.Zero(t, loadRes.Errors+loadRes.Non2xx, "the domain failed during the adoption: %s", loadRes.Summary())
	for _, body := range loadRes.Bodies {
		require.Equal(t, "v1", body)
	}

	// ---- 7. A normal redeploy from B replaces the adopted containers -----
	d := b.DeployOK(web.ID)
	require.Equal(t, "manual", d.Str("trigger"))
	require.NotEqual(t, webDep.Str("id"), d.Str("id"))
	require.True(t, eventually(60*time.Second, 500*time.Millisecond, func() bool { return len(a.Containers(web.ID)) == 2 }),
		"the old containers are still there: %d", len(a.Containers(web.ID)))
	after := snapshot(web)
	require.Len(t, after, 2)
	for name, id := range after {
		require.NotContains(t, webBefore, name, "a container name was reused")
		require.True(t, id != "" && !slices.Contains(mapValues(webBefore), id), "container %s is an adopted one", name)
	}
	for _, c := range a.RunningContainers(web.ID) {
		require.Equal(t, d.Str("id"), c.Labels[agentprotocol.LabelDeployment])
		env := containerEnv(t, c.ID)
		require.True(t, slices.Contains(env, "SESSION_SECRET="+secret), "the adopted secret did not reach the redeployed container")
		require.True(t, slices.Contains(env, "VERSION=v1"), "the adopted variable did not reach the redeployed container")
	}
	px.WaitBody(t, "/", "v1", 30*time.Second)
	require.Equal(t, keepBefore, snapshot(keep), "the unmanaged service survives the redeploy")
}

// adoptionState reads servers.adoption_state of the stack's server.
func (s *Stack) adoptionState() string {
	s.t.Helper()
	var st string
	require.NoError(s.t, s.Pool.QueryRow(context.Background(), `SELECT adoption_state FROM servers WHERE id = $1`, s.ServerID).Scan(&st))
	return st
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
