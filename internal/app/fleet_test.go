package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/fleet"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const gib = int64(1) << 30

// fleetRig drives drains without River: the harness never starts the job
// client, so the test calls Step itself and completes deployments the way the
// agent hub would (HandleResult).
type fleetRig struct {
	h     *harness
	fleet *fleet.Service
	deps  *deployments.Service
}

func (h *harness) fleetRig() *fleetRig {
	c := h.app.Core
	hub := agents.NewHub(c.Pool, c.Bus, c.Jobs, c.Config, nil)
	serverSvc := servers.NewService(c.Pool, c.Bus, hub, c.Config, nil)
	serviceSvc := services.NewService(c.Pool, c.Bus, c.Box, hub, nil)
	deps := deployments.NewService(deployments.Deps{Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Dispatcher: hub, Services: serviceSvc})
	return &fleetRig{h: h, deps: deps, fleet: fleet.NewService(c.Pool, c.Bus, c.Jobs, serverSvc, serviceSvc, deps, nil)}
}

// newOnlineServer enrolls a server row and makes it look online with the given
// memory numbers.
func (h *harness) newOnlineServer(name string, memTotal, memUsed int64) string {
	h.t.Helper()
	r := h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": name})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	id := r.JSON()["server"].(map[string]any)["id"].(string)
	ctx := context.Background()
	facts, _ := json.Marshal(agentprotocol.Facts{CPUs: 4, MemTotal: uint64(memTotal), DiskTotal: uint64(100 * gib)})
	_, err := h.app.Pool.Exec(ctx, `UPDATE servers SET status='online', last_seen_at=now(), facts=$2 WHERE id=$1`, uuid.MustParse(id), facts)
	require.NoError(h.t, err)
	h.sample(id, memTotal, memUsed)
	return id
}

func (h *harness) sample(serverID string, memTotal, memUsed int64) {
	h.t.Helper()
	err := db.New(h.app.Pool).InsertServerMetricSample(context.Background(), db.InsertServerMetricSampleParams{
		ServerID: uuid.MustParse(serverID), CpuPercent: 12, MemBytes: memUsed, MemLimitBytes: memTotal, DiskUsedBytes: 10 * gib, DiskTotalBytes: 100 * gib,
	})
	require.NoError(h.t, err)
}

func (h *harness) setDrainState(serverID, state string) {
	h.t.Helper()
	_, err := h.app.Pool.Exec(context.Background(), `UPDATE servers SET drain_state=$2 WHERE id=$1`, uuid.MustParse(serverID), state)
	require.NoError(h.t, err)
}

// newEnv returns a fresh project's production environment.
func (h *harness) newEnv(project string) string {
	h.t.Helper()
	pid := h.createProject(project)
	r := h.do("GET", "/projects/"+pid+"/environments", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()["items"].([]any)[0].(map[string]any)["id"].(string)
}

func (h *harness) newServiceOn(envID, name, serverID string, cfg map[string]any) string {
	h.t.Helper()
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["image"], cfg["port"] = "traefik/whoami:latest", 80
	r := h.do("POST", "/environments/"+envID+"/services", "", map[string]any{"name": name, "sourceType": "image", "serverId": serverID, "config": cfg})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	return r.JSON()["id"].(string)
}

// deployAndSucceed deploys a service and reports success from its server.
func (rig *fleetRig) deployAndSucceed(serviceID, serverID string) {
	rig.h.t.Helper()
	r := rig.h.do("POST", "/services/"+serviceID+"/deployments", "", map[string]any{})
	require.Equal(rig.h.t, http.StatusAccepted, r.Status, string(r.Raw))
	rig.succeed(r.JSON()["id"].(string), serverID, agentprotocol.OutcomeSucceeded, "")
}

func (rig *fleetRig) succeed(depID, serverID, outcome, msg string) {
	rig.h.t.Helper()
	d := uuid.MustParse(depID)
	require.NoError(rig.h.t, rig.deps.HandleResult(context.Background(), uuid.MustParse(serverID), uuid.New(), &d,
		agentprotocol.ResultFrame{Outcome: outcome, Error: msg}))
}

func (h *harness) serviceServer(serviceID string) string {
	h.t.Helper()
	r := h.do("GET", "/services/"+serviceID, "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	s, _ := r.JSON()["serverId"].(string)
	return s
}

func (h *harness) latestDeployment(serviceID string) map[string]any {
	h.t.Helper()
	r := h.do("GET", "/services/"+serviceID+"/deployments", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	items := r.JSON()["items"].([]any)
	require.NotEmpty(h.t, items)
	return items[0].(map[string]any)
}

func (h *harness) drainState(serverID string) (string, string) {
	h.t.Helper()
	r := h.do("GET", "/servers/"+serverID, "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	j := r.JSON()
	return j["drainState"].(string), j["drainMessage"].(string)
}

func moves(pairs ...string) []map[string]any {
	out := []map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]any{"serviceId": pairs[i], "targetServerId": pairs[i+1]})
	}
	return out
}

func TestPlacementOnDrainingServerGets422(t *testing.T) {
	h := newHarness(t)
	h.setup()
	rig := h.fleetRig()
	a := h.newOnlineServer("a", 8*gib, 1*gib)
	b := h.newOnlineServer("b", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	onB := h.newServiceOn(env, "on-b", b, nil)
	onA := h.newServiceOn(env, "on-a", a, nil)
	rig.deployAndSucceed(onB, b)

	h.setDrainState(b, "draining")

	// Creating on the draining server is refused.
	r := h.do("POST", "/environments/"+env+"/services", "", map[string]any{
		"name": "new", "sourceType": "image", "serverId": b, "config": map[string]any{"image": "traefik/whoami:latest", "port": 80},
	})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "server_draining", r.JSON()["code"])
	require.Contains(t, string(r.Raw), "b is being drained")

	// Moving a service onto it is refused.
	g := h.do("GET", "/services/"+onA, "", nil)
	gen := int64(g.JSON()["generation"].(float64))
	r = h.do("PATCH", "/services/"+onA, "", map[string]any{"serverId": b}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "server_draining", r.JSON()["code"])

	// Services that already live there still deploy (so a drain can be aborted safely).
	r = h.do("POST", "/services/"+onB+"/deployments", "", map[string]any{})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))

	// A drained server is not schedulable either; a failed drain is.
	h.setDrainState(b, "drained")
	r = h.do("PATCH", "/services/"+onA, "", map[string]any{"serverId": b}, "If-Match", services.ETag(gen))
	require.Equal(t, "server_draining", r.JSON()["code"])
	h.setDrainState(b, "failed")
	r = h.do("PATCH", "/services/"+onA, "", map[string]any{"serverId": b}, "If-Match", services.ETag(gen))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
}

func TestDrainPlanPicksMostFreeMemoryAndFlagsStateful(t *testing.T) {
	h := newHarness(t)
	h.setup()
	src := h.newOnlineServer("src", 8*gib, 2*gib)
	tight := h.newOnlineServer("tight", 8*gib, 6*gib)
	roomy := h.newOnlineServer("roomy", 16*gib, 2*gib)
	env := h.newEnv("Shop")
	web := h.newServiceOn(env, "web", src, nil)
	db1 := h.newServiceOn(env, "db", src, map[string]any{"memoryMb": 1024})
	r := h.do("POST", "/services/"+db1+"/volumes", "", map[string]any{"name": "data", "mountPath": "/data"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	r = h.do("POST", "/servers/"+src+"/drain/plan", "", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	j := r.JSON()
	mv := j["moves"].([]any)
	require.Len(t, mv, 2)
	// Stateless first.
	first, second := mv[0].(map[string]any), mv[1].(map[string]any)
	require.Equal(t, web, first["serviceId"])
	require.Equal(t, db1, second["serviceId"])
	require.Equal(t, roomy, first["targetServerId"])
	require.Equal(t, roomy, second["targetServerId"])
	require.NotEqual(t, tight, first["targetServerId"])
	require.Equal(t, false, first["stateful"])
	require.Equal(t, true, second["stateful"])
	require.Contains(t, strings.Join(toStrings(second["warnings"]), "\n"),
		"Volumes are not copied. db will start with empty volumes on roomy; move data first.")
	require.Contains(t, strings.Join(toStrings(first["warnings"]), "\n"), "roomy was chosen automatically")
	require.Equal(t, true, j["capacity"].(map[string]any)["targetFits"])

	// An explicit target is honoured, and the capacity check catches a target that is too small.
	r = h.do("POST", "/servers/"+src+"/drain/plan", "", map[string]any{"targetServerId": tight})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, tight, r.JSON()["moves"].([]any)[0].(map[string]any)["targetServerId"])
	// tight has 2 GiB free; db is limited to 1 GiB and web has no usage data: still fits.
	require.Equal(t, true, r.JSON()["capacity"].(map[string]any)["targetFits"])
	h.do("PATCH", "/services/"+db1, "", map[string]any{"config": map[string]any{"image": "traefik/whoami:latest", "port": 80, "memoryMb": 3072}},
		"If-Match", services.ETag(int64(h.do("GET", "/services/"+db1, "", nil).JSON()["generation"].(float64))))
	r = h.do("POST", "/servers/"+src+"/drain/plan", "", map[string]any{"targetServerId": tight})
	cap := r.JSON()["capacity"].(map[string]any)
	require.Equal(t, false, cap["targetFits"], string(r.Raw))
	require.Contains(t, strings.Join(toStrings(cap["reasons"]), "\n"), "tight would be")

	// Draining or unknown targets are rejected.
	h.setDrainState(roomy, "draining")
	r = h.do("POST", "/servers/"+src+"/drain/plan", "", map[string]any{"targetServerId": roomy})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status)
	require.Equal(t, "server_draining", r.JSON()["code"])
	r = h.do("POST", "/servers/"+src+"/drain/plan", "", map[string]any{})
	require.Equal(t, tight, r.JSON()["moves"].([]any)[0].(map[string]any)["targetServerId"], "a draining server is never chosen")

	// Nothing to go to: blocking no_target.
	h.setDrainState(tight, "drained")
	r = h.do("POST", "/servers/"+src+"/drain/plan", "", map[string]any{})
	blocking := r.JSON()["moves"].([]any)[0].(map[string]any)["blocking"].([]any)
	require.Len(t, blocking, 1)
	require.Equal(t, "no_target", blocking[0].(map[string]any)["code"])
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestDrainStartValidation(t *testing.T) {
	h := newHarness(t)
	h.setup()
	src := h.newOnlineServer("src", 8*gib, 1*gib)
	dst := h.newOnlineServer("dst", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	web := h.newServiceOn(env, "web", src, nil)
	db1 := h.newServiceOn(env, "db", src, nil)
	r := h.do("POST", "/services/"+db1+"/volumes", "", map[string]any{"name": "data", "mountPath": "/data"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	// A stateful move needs confirmStateful.
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(web, dst, db1, dst)})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "confirmation_required", r.JSON()["code"])
	state, _ := h.drainState(src)
	require.Equal(t, "none", state)

	// Every service must be moved.
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(web, dst), "confirmStateful": true})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "incomplete_moves", r.JSON()["code"])

	// Not onto itself, not onto a draining server.
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(web, src, db1, src), "confirmStateful": true})
	require.Equal(t, "invalid_target", r.JSON()["code"])
	h.setDrainState(dst, "draining")
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(web, dst, db1, dst), "confirmStateful": true})
	require.Equal(t, "server_draining", r.JSON()["code"])
	h.setDrainState(dst, "none")

	// Valid: accepted, server is draining, a second start conflicts.
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(web, dst, db1, dst), "confirmStateful": true})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	require.Equal(t, "running", r.JSON()["state"])
	state, _ = h.drainState(src)
	require.Equal(t, "draining", state)
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(web, dst, db1, dst), "confirmStateful": true})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "drain_in_progress", r.JSON()["code"])

	// Undrain refuses while draining.
	r = h.do("POST", "/servers/"+src+"/undrain", "", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))

	// Read scope cannot drain; restricted tokens cannot either.
	ro := h.createToken([]string{"servers:read", "projects:read"}, nil)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/servers/"+src+"/drain", ro, map[string]any{"moves": moves()}).Status)
	pid := h.do("GET", "/projects", "", nil).JSON()["items"].([]any)[0].(map[string]any)["id"].(string)
	restricted := h.createToken([]string{"admin"}, []string{pid})
	require.Equal(t, http.StatusForbidden, h.do("POST", "/servers/"+src+"/drain/plan", restricted, map[string]any{}).Status)
}

// runDrain steps the drain; onWait is called whenever the job would snooze.
func (rig *fleetRig) runDrain(drainID string, onWait func()) {
	rig.h.t.Helper()
	id := uuid.MustParse(drainID)
	for range 200 {
		res, err := rig.fleet.Step(context.Background(), id)
		require.NoError(rig.h.t, err)
		if res.Done {
			return
		}
		if res.Wait > 0 && onWait != nil {
			onWait()
		}
	}
	rig.h.t.Fatal("drain did not finish")
}

func TestDrainJobMovesServicesSequentially(t *testing.T) {
	h := newHarness(t)
	h.setup()
	rig := h.fleetRig()
	src := h.newOnlineServer("src", 8*gib, 1*gib)
	dst := h.newOnlineServer("dst", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	alpha := h.newServiceOn(env, "alpha", src, nil)
	bravo := h.newServiceOn(env, "bravo", src, nil)
	rig.deployAndSucceed(alpha, src)
	rig.deployAndSucceed(bravo, src)
	before := int64(h.do("GET", "/services/"+alpha, "", nil).JSON()["generation"].(float64))

	r := h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(alpha, dst, bravo, dst)}, "Idempotency-Key", "drain-1")
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	drainID := r.JSON()["id"].(string)
	// An idempotent replay returns the same drain.
	r2 := h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(alpha, dst, bravo, dst)}, "Idempotency-Key", "drain-1")
	require.Equal(t, http.StatusAccepted, r2.Status, string(r2.Raw))
	require.Equal(t, drainID, r2.JSON()["id"])

	// Sequential: only one deployment may be in flight at any time.
	var order []string
	rig.runDrain(drainID, func() {
		active := 0
		var cur string
		for _, svc := range []string{alpha, bravo} {
			d := h.latestDeployment(svc)
			if d["trigger"] == "drain" && deployments.Status(d["status"].(string)).IsActive() {
				active++
				cur = svc
				require.Equal(t, dst, d["serverId"])
				rig.succeed(d["id"].(string), dst, agentprotocol.OutcomeSucceeded, "")
			}
		}
		require.LessOrEqual(t, active, 1, "moves must not overlap")
		if cur != "" {
			order = append(order, cur)
		}
	})
	require.Equal(t, []string{alpha, bravo}, order)

	state, msg := h.drainState(src)
	require.Equal(t, "drained", state)
	require.Empty(t, msg)
	require.Equal(t, dst, h.serviceServer(alpha))
	require.Equal(t, dst, h.serviceServer(bravo))
	g := h.do("GET", "/services/"+alpha, "", nil).JSON()
	require.Greater(t, int64(g["generation"].(float64)), before)
	require.Equal(t, g["generation"], g["appliedGeneration"], "the move was deployed, nothing stays staged")

	// The old containers are removed from the drained server.
	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_commands WHERE server_id=$1 AND kind='remove_service'`, uuid.MustParse(src)).Scan(&n))
	require.Equal(t, 2, n)

	// Progress and audit.
	r = h.do("GET", "/servers/"+src+"/drain", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	dr := r.JSON()["drain"].(map[string]any)
	require.Equal(t, "completed", dr["state"])
	for _, m := range dr["moves"].([]any) {
		mm := m.(map[string]any)
		require.Equal(t, "done", mm["status"])
		require.Equal(t, "succeeded", mm["deploymentStatus"])
	}
	for _, action := range []string{"server.drain.start", "server.drain.complete"} {
		require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action=$1`, action).Scan(&n))
		require.Equal(t, 1, n, action)
	}

	// The drained server rejects new placements; undrain reopens it.
	r = h.do("POST", "/environments/"+env+"/services", "", map[string]any{
		"name": "late", "sourceType": "image", "serverId": src, "config": map[string]any{"image": "x:1", "port": 80},
	})
	require.Equal(t, "server_draining", r.JSON()["code"])
	r = h.do("POST", "/servers/"+src+"/undrain", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "none", r.JSON()["drainState"])
}

func TestDrainFailureStopsAndLeavesTheRest(t *testing.T) {
	h := newHarness(t)
	h.setup()
	rig := h.fleetRig()
	src := h.newOnlineServer("src", 8*gib, 1*gib)
	dst := h.newOnlineServer("dst", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	alpha := h.newServiceOn(env, "alpha", src, nil)
	bravo := h.newServiceOn(env, "bravo", src, nil)
	rig.deployAndSucceed(alpha, src)
	rig.deployAndSucceed(bravo, src)
	genAlpha := int64(h.do("GET", "/services/"+alpha, "", nil).JSON()["generation"].(float64))

	r := h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(alpha, dst, bravo, dst)})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	drainID := r.JSON()["id"].(string)

	rig.runDrain(drainID, func() {
		d := h.latestDeployment(alpha)
		if d["trigger"] == "drain" && deployments.Status(d["status"].(string)).IsActive() {
			rig.succeed(d["id"].(string), dst, agentprotocol.OutcomeFailed, "image pull failed: no such image")
		}
	})

	state, msg := h.drainState(src)
	require.Equal(t, "failed", state)
	require.Contains(t, msg, "alpha")
	require.Contains(t, msg, "image pull failed")

	// The failed service is back on its server with its old generation; bravo was never touched.
	require.Equal(t, src, h.serviceServer(alpha))
	require.Equal(t, genAlpha, int64(h.do("GET", "/services/"+alpha, "", nil).JSON()["generation"].(float64)))
	require.Equal(t, src, h.serviceServer(bravo))
	require.Equal(t, "succeeded", h.latestDeployment(bravo)["status"])
	require.NotEqual(t, "drain", h.latestDeployment(bravo)["trigger"])

	dr := h.do("GET", "/servers/"+src+"/drain", "", nil).JSON()["drain"].(map[string]any)
	require.Equal(t, "failed", dr["state"])
	ms := dr["moves"].([]any)
	require.Equal(t, "failed", ms[0].(map[string]any)["status"])
	require.Equal(t, "pending", ms[1].(map[string]any)["status"])

	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='server.drain.fail'`).Scan(&n))
	require.Equal(t, 1, n)

	// A failed drain does not block placement (it is retried or undrained).
	r = h.do("POST", "/servers/"+src+"/undrain", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
}

func TestDrainCancelBeforeAndAfterFirstMove(t *testing.T) {
	h := newHarness(t)
	h.setup()
	rig := h.fleetRig()
	src := h.newOnlineServer("src", 8*gib, 1*gib)
	dst := h.newOnlineServer("dst", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	alpha := h.newServiceOn(env, "alpha", src, nil)
	bravo := h.newServiceOn(env, "bravo", src, nil)
	rig.deployAndSucceed(alpha, src)
	rig.deployAndSucceed(bravo, src)

	// Canceled before anything moved: back to none.
	r := h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(alpha, dst, bravo, dst)})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	id := r.JSON()["id"].(string)
	require.Equal(t, http.StatusOK, h.do("POST", "/servers/"+src+"/drain/cancel", "", nil).Status)
	rig.runDrain(id, nil)
	state, _ := h.drainState(src)
	require.Equal(t, "none", state)
	require.Equal(t, src, h.serviceServer(alpha))

	// Canceled during the first move: that move finishes, the rest stays.
	r = h.do("POST", "/servers/"+src+"/drain", "", map[string]any{"moves": moves(alpha, dst, bravo, dst)})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	id = r.JSON()["id"].(string)
	canceled := false
	rig.runDrain(id, func() {
		if !canceled {
			canceled = true
			require.Equal(t, http.StatusOK, h.do("POST", "/servers/"+src+"/drain/cancel", "", nil).Status)
		}
		d := h.latestDeployment(alpha)
		if d["trigger"] == "drain" && deployments.Status(d["status"].(string)).IsActive() {
			rig.succeed(d["id"].(string), dst, agentprotocol.OutcomeSucceeded, "")
		}
	})
	state, msg := h.drainState(src)
	require.Equal(t, "failed", state)
	require.Equal(t, "Canceled after 1 of 2 moves", msg)
	require.Equal(t, dst, h.serviceServer(alpha))
	require.Equal(t, src, h.serviceServer(bravo))

	// Nothing running: cancel conflicts.
	require.Equal(t, http.StatusConflict, h.do("POST", "/servers/"+src+"/drain/cancel", "", nil).Status)
}

func TestFleetOverviewOvercommit(t *testing.T) {
	h := newHarness(t)
	h.setup()
	a := h.newOnlineServer("a", 4*gib, 1*gib)
	b := h.newOnlineServer("b", 8*gib, 1*gib)
	env := h.newEnv("Shop")
	h.newServiceOn(env, "x", a, map[string]any{"memoryMb": 3072})
	h.newServiceOn(env, "y", a, map[string]any{"memoryMb": 2048})
	h.newServiceOn(env, "z", a, nil)
	h.newServiceOn(env, "w", b, map[string]any{"memoryMb": 512})

	r := h.do("GET", "/fleet", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	byName := map[string]map[string]any{}
	for _, s := range r.JSON()["servers"].([]any) {
		m := s.(map[string]any)
		byName[m["name"].(string)] = m
	}
	sa, sb := byName["a"], byName["b"]
	require.Equal(t, float64(5*gib), sa["committedMemBytes"])
	require.Equal(t, true, sa["overcommitted"])
	require.Equal(t, float64(1), sa["unlimitedServices"])
	require.Len(t, sa["services"], 3)
	require.Equal(t, float64(4*gib), sa["memTotalBytes"])
	require.Equal(t, float64(4), sa["cpus"])
	require.Equal(t, false, sb["overcommitted"])
	require.Equal(t, float64(512*(1<<20)), sb["committedMemBytes"])
	tot := r.JSON()["totals"].(map[string]any)
	require.Equal(t, float64(2), tot["servers"])
	require.Equal(t, float64(4), tot["services"])
	require.Equal(t, float64(12*gib), tot["memTotalBytes"])

	// Service rows only show projects the token may see; server numbers stay whole.
	pid := h.createProject("Other")
	restricted := h.createToken([]string{"servers:read"}, []string{pid})
	r = h.do("GET", "/fleet", restricted, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	for _, s := range r.JSON()["servers"].([]any) {
		m := s.(map[string]any)
		require.Empty(t, m["services"], "no visible services")
		if m["name"] == "a" {
			require.Equal(t, float64(5*gib), m["committedMemBytes"])
		}
	}
	// A server in an unreadable scope is refused.
	require.Equal(t, http.StatusForbidden, h.do("GET", "/fleet", h.createToken([]string{"projects:read"}, nil), nil).Status)
}
