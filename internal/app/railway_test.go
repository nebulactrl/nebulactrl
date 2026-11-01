package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (h *harness) startJobs() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	require.NoError(h.t, h.app.Start(ctx))
}

func (h *harness) deploy(serviceID string, body map[string]any) map[string]any {
	h.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	r := h.do("POST", "/services/"+serviceID+"/deployments", "", body)
	require.Equal(h.t, http.StatusAccepted, r.Status, string(r.Raw))
	return r.JSON()
}

func (h *harness) putVars(serviceID string, vars ...map[string]any) {
	h.t.Helper()
	for _, v := range vars {
		if _, ok := v["isSecret"]; !ok {
			v["isSecret"] = false
		}
	}
	r := h.do("PUT", "/services/"+serviceID+"/variables", "", map[string]any{"variables": vars})
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
}

func (h *harness) variables(serviceID string) map[string]map[string]any {
	h.t.Helper()
	r := h.do("GET", "/services/"+serviceID+"/variables", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	out := map[string]map[string]any{}
	for _, it := range r.JSON()["items"].([]any) {
		m := it.(map[string]any)
		out[m["key"].(string)] = m
	}
	return out
}

func (h *harness) pendingFields(serviceID string) []string {
	h.t.Helper()
	r := h.do("GET", "/services/"+serviceID+"/pending-changes", "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	var out []string
	for _, it := range r.JSON()["items"].([]any) {
		out = append(out, it.(map[string]any)["field"].(string))
	}
	return out
}

// awaitPrepared waits until the DeployJob wrote the release of a deployment.
func (h *harness) awaitPrepared(depID string) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		var rel *uuid.UUID
		err := h.app.Pool.QueryRow(context.Background(), "SELECT release_id FROM deployments WHERE id = $1", uuid.MustParse(depID)).Scan(&rel)
		return err == nil && rel != nil
	}, 20*time.Second, 100*time.Millisecond)
}

func (h *harness) deploymentStatus(depID string) string {
	h.t.Helper()
	r := h.do("GET", "/deployments/"+depID, "", nil)
	require.Equal(h.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()["status"].(string)
}

// deployedService creates a service on a fresh online server and brings it to
// a successful deployment. It returns (serviceID, serverID).
func (h *harness) deployedService(rig *fleetRig, envID, name string, cfg map[string]any) (string, string) {
	h.t.Helper()
	srv := h.newOnlineServer("srv-"+name, 8*gib, gib)
	svc := h.newServiceOn(envID, name, srv, cfg)
	dep := h.deploy(svc, nil)["id"].(string)
	h.awaitPrepared(dep)
	rig.succeed(dep, srv, agentprotocol.OutcomeSucceeded, "")
	return svc, srv
}

func TestCancelBeforeAndAfterAck(t *testing.T) {
	h := newHarness(t)
	h.setup()
	ctx := context.Background()
	svc, serverID := h.newService("web")

	// Queued, nothing dispatched yet: cancel works and records who canceled.
	dep := h.deploy(svc, nil)["id"].(string)
	r := h.do("POST", "/deployments/"+dep+"/cancel", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "canceled", r.JSON()["status"])
	require.Equal(t, "Canceled by admin@example.com", r.JSON()["statusMessage"])

	var audited int
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_events WHERE action = 'deployment.cancel' AND resource_id = $1", dep).Scan(&audited))
	require.Equal(t, 1, audited)

	// Already terminal.
	r = h.do("POST", "/deployments/"+dep+"/cancel", "", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "cannot_cancel", r.JSON()["code"])

	// Command written but not acked: cancel stops the command too.
	dep = h.deploy(svc, nil)["id"].(string)
	cmd := uuid.New()
	_, err := h.app.Pool.Exec(ctx, `INSERT INTO agent_commands (id, server_id, deployment_id, kind, payload, idempotency_key, status, expires_at)
		VALUES ($1, $2, $3, 'deploy', '{}', $4, 'sent', now() + interval '15 minutes')`, cmd, uuid.MustParse(serverID), uuid.MustParse(dep), "deploy:"+dep)
	require.NoError(t, err)
	_, err = h.app.Pool.Exec(ctx, `UPDATE deployments SET status = 'preparing' WHERE id = $1`, uuid.MustParse(dep))
	require.NoError(t, err)
	r = h.do("POST", "/deployments/"+dep+"/cancel", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	var cmdStatus string
	require.NoError(t, h.app.Pool.QueryRow(ctx, "SELECT status FROM agent_commands WHERE id = $1", cmd).Scan(&cmdStatus))
	require.Equal(t, "canceled", cmdStatus)

	// Acked: the agent owns it now. The cancel is recorded and a cancel_deploy
	// command is queued for the agent; the deployment keeps its status until the
	// agent reports the abort.
	dep = h.deploy(svc, nil)["id"].(string)
	_, err = h.app.Pool.Exec(ctx, `INSERT INTO agent_commands (id, server_id, deployment_id, kind, payload, idempotency_key, status, acked_at, expires_at)
		VALUES ($1, $2, $3, 'deploy', '{}', $4, 'acked', now(), now() + interval '15 minutes')`, uuid.New(), uuid.MustParse(serverID), uuid.MustParse(dep), "deploy:"+dep)
	require.NoError(t, err)
	_, err = h.app.Pool.Exec(ctx, `UPDATE deployments SET status = 'building' WHERE id = $1`, uuid.MustParse(dep))
	require.NoError(t, err)
	r = h.do("POST", "/deployments/"+dep+"/cancel", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "building", r.JSON()["status"], "the agent has not aborted yet")
	require.NotNil(t, r.JSON()["cancelRequestedAt"])
	require.Equal(t, "admin@example.com", r.JSON()["cancelRequestedBy"])
	require.Equal(t, "building", h.deploymentStatus(dep))
	var payload string
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		"SELECT payload::text FROM agent_commands WHERE kind = 'cancel_deploy' AND server_id = $1 ORDER BY created_at DESC LIMIT 1", uuid.MustParse(serverID)).Scan(&payload))
	require.Contains(t, payload, dep)
	require.Contains(t, payload, "admin@example.com")
	var agentCancels int
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_events WHERE action = 'deployment.cancel' AND resource_id = $1 AND metadata->>'mode' = 'agent'", dep).Scan(&agentCancels))
	require.Equal(t, 1, agentCancels)

	// The agent's result decides: a canceled outcome ends the deployment as
	// canceled, with the step it reached, audited.
	res, err := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeCanceled, CanceledAt: "build", Message: "Canceled by admin@example.com during the build. The previous release is still serving."})
	require.NoError(t, err)
	depID := uuid.MustParse(dep)
	require.NoError(t, h.fleetRig().deps.HandleResult(ctx, uuid.MustParse(serverID), uuid.New(), &depID,
		agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeCanceled, Data: res}))
	require.Equal(t, "canceled", h.deploymentStatus(dep))
	r = h.do("GET", "/deployments/"+dep, "", nil)
	require.Equal(t, "build", r.JSON()["canceledAtStep"])
	require.Contains(t, r.JSON()["statusMessage"], "The previous release is still serving.")
	var canceled int
	require.NoError(t, h.app.Pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_events WHERE action = 'deployment.canceled' AND resource_id = $1 AND metadata->>'step' = 'build'", dep).Scan(&canceled))
	require.Equal(t, 1, canceled)

	// A token without deployments:write is refused.
	ro := h.createToken([]string{"projects:read"}, nil)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/deployments/"+dep+"/cancel", ro, nil).Status)
}

func TestDeployStagedOrdersProviderFirstAndConsumerWaits(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startJobs()
	env := h.newEnv("Ordered")
	srv := h.newOnlineServer("srv-ordered", 8*gib, gib)

	// The consumer is created first, so the input order is api, db.
	api := h.newServiceOn(env, "api", srv, nil)
	db := h.newServiceOn(env, "db", srv, nil)
	h.putVars(api, map[string]any{"key": "DB_HOST", "value": "${{db.NEBULA_PRIVATE_HOST}}"})

	r := h.do("POST", "/environments/"+env+"/deploy-staged", "", map[string]any{"message": "ship it"})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	body := r.JSON()
	require.Equal(t, []any{db, api}, body["order"], "the provider deploys first")
	items := body["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, db, items[0].(map[string]any)["serviceId"])
	require.Equal(t, "ship it", items[0].(map[string]any)["message"])
	dbDep, apiDep := items[0].(map[string]any)["id"].(string), items[1].(map[string]any)["id"].(string)

	// The provider is prepared and waits for its (absent) agent; the consumer's
	// job snoozes and its deployment stays queued.
	h.awaitPrepared(dbDep)
	var waitFor string
	require.NoError(t, h.app.Pool.QueryRow(context.Background(),
		`SELECT args->'waitFor'->>0 FROM river_job WHERE kind = 'deploy_service' AND args->>'deploymentId' = $1`, apiDep).Scan(&waitFor))
	require.Equal(t, dbDep, waitFor)
	time.Sleep(1500 * time.Millisecond)
	require.Equal(t, "queued", h.deploymentStatus(apiDep), "consumer waits for the provider")

	// Once the provider reaches a terminal state the consumer proceeds.
	require.Equal(t, http.StatusOK, h.do("POST", "/deployments/"+dbDep+"/cancel", "", nil).Status)
	require.Eventually(t, func() bool { return h.deploymentStatus(apiDep) == "preparing" }, 30*time.Second, 200*time.Millisecond)
}

func TestDiscardSinglePendingVariable(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startJobs()
	rig := h.fleetRig()
	env := h.newEnv("Discard")
	svc, _ := h.deployedService(rig, env, "web", nil)

	// First deployment: variables to restore later.
	h.putVars(svc,
		map[string]any{"key": "KEEP", "value": "k1"},
		map[string]any{"key": "COLOR", "value": "blue"},
	)
	srv := h.serviceServer(svc)
	dep := h.deploy(svc, nil)["id"].(string)
	h.awaitPrepared(dep)
	rig.succeed(dep, srv, agentprotocol.OutcomeSucceeded, "")
	require.Empty(t, h.pendingFields(svc))

	// Stage three changes.
	h.putVars(svc,
		map[string]any{"key": "COLOR", "value": "red"},
		map[string]any{"key": "KEEP", "value": "k2"},
		map[string]any{"key": "FRESH", "value": "new", "isSecret": true},
	)
	require.ElementsMatch(t, []string{"variables.COLOR", "variables.KEEP", "variables.FRESH"}, h.pendingFields(svc))
	v := h.do("GET", "/services/"+svc, "", nil).JSON()
	require.Equal(t, "edited", v["stagedKind"])
	require.Equal(t, map[string]any{"variables": float64(3), "settings": float64(0)}, v["pendingChangeCounts"])
	gen := v["generation"].(float64)

	// Discard one: the previous value comes back and the others stay staged.
	r := h.do("DELETE", "/services/"+svc+"/pending-changes/variables.COLOR", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Greater(t, r.JSON()["generation"].(float64), gen)
	vars := h.variables(svc)
	require.Equal(t, "blue", vars["COLOR"]["value"])
	require.Equal(t, "k2", vars["KEEP"]["value"])
	require.ElementsMatch(t, []string{"variables.KEEP", "variables.FRESH"}, h.pendingFields(svc))
	require.Equal(t, map[string]any{"variables": float64(2), "settings": float64(0)}, r.JSON()["pendingChangeCounts"])

	// A variable added since the release is removed; secrets stay write-only.
	r = h.do("DELETE", "/services/"+svc+"/pending-changes/variables.FRESH", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	_, still := h.variables(svc)["FRESH"]
	require.False(t, still)
	require.Equal(t, []string{"variables.KEEP"}, h.pendingFields(svc))

	// Not pending: 404. Audited once per discard.
	require.Equal(t, http.StatusNotFound, h.do("DELETE", "/services/"+svc+"/pending-changes/variables.COLOR", "", nil).Status)
	require.Equal(t, http.StatusNotFound, h.do("DELETE", "/services/"+svc+"/pending-changes/bogus", "", nil).Status)
	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_events WHERE action = 'service.discard_change' AND resource_id = $1", svc).Scan(&n))
	require.Equal(t, 2, n)

	// A config field and a volume go back too.
	cur := h.do("GET", "/services/"+svc, "", nil)
	cfg := cur.JSON()["config"].(map[string]any)
	cfg["memoryMb"] = 512
	r = h.do("PATCH", "/services/"+svc, "", map[string]any{"config": cfg}, "If-Match", cur.Header.Get("ETag"))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, http.StatusCreated, h.do("POST", "/services/"+svc+"/volumes", "", map[string]any{"name": "data", "mountPath": "/data"}).Status)
	require.ElementsMatch(t, []string{"variables.KEEP", "config.memoryMb", "volumes.data"}, h.pendingFields(svc))

	require.Equal(t, http.StatusOK, h.do("DELETE", "/services/"+svc+"/pending-changes/config.memoryMb", "", nil).Status)
	r = h.do("DELETE", "/services/"+svc+"/pending-changes/volumes.data", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Empty(t, r.JSON()["volumes"])
	require.Equal(t, []string{"variables.KEEP"}, h.pendingFields(svc))
	require.Nil(t, h.do("GET", "/services/"+svc, "", nil).JSON()["config"].(map[string]any)["memoryMb"])
}

func TestDerivedReferenceLinks(t *testing.T) {
	h := newHarness(t)
	h.setup()
	env := h.newEnv("Links")
	srv := h.newOnlineServer("srv-links", 8*gib, gib)
	api := h.newServiceOn(env, "api", srv, nil)
	db := h.newServiceOn(env, "db", srv, nil)
	cache := h.newServiceOn(env, "cache", srv, nil)

	h.putVars(api,
		map[string]any{"key": "DB_HOST", "value": "${{db.NEBULA_PRIVATE_HOST}}"},
		map[string]any{"key": "DB_PORT", "value": "${{db.PORT}}", "isSecret": true},
		map[string]any{"key": "SELF", "value": "${{api.PORT}}"},
	)
	list := func() []map[string]any {
		r := h.do("GET", "/environments/"+env+"/links", "", nil)
		require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
		var out []map[string]any
		for _, it := range r.JSON()["items"].([]any) {
			out = append(out, it.(map[string]any))
		}
		return out
	}

	links := list()
	require.Len(t, links, 1, "one derived edge per pair; self references are ignored")
	l := links[0]
	require.Equal(t, "reference", l["source"])
	require.Equal(t, "ref:"+api+":"+db, l["id"])
	require.Equal(t, api, l["fromServiceId"])
	require.Equal(t, db, l["toServiceId"])
	// The variables are saved but not deployed, so each reference is staged.
	require.Equal(t, []any{
		map[string]any{"key": "DB_HOST", "staged": true},
		map[string]any{"key": "DB_PORT", "staged": true},
	}, l["references"])

	// Derived links cannot be deleted.
	require.Equal(t, http.StatusNotFound, h.do("DELETE", "/links/"+l["id"].(string), "", nil).Status)
	require.Len(t, list(), 1)

	// A manual link for the same pair merges into one item that stays deletable.
	r := h.do("POST", "/environments/"+env+"/links", "", map[string]any{"fromServiceId": api, "toServiceId": db})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	manualID := r.JSON()["link"].(map[string]any)["id"].(string)
	links = list()
	require.Len(t, links, 1)
	require.Equal(t, "manual", links[0]["source"])
	require.Equal(t, manualID, links[0]["id"])
	require.Len(t, links[0]["references"], 2)

	// A manual-only pair has no references.
	require.Equal(t, http.StatusCreated, h.do("POST", "/environments/"+env+"/links", "", map[string]any{"fromServiceId": db, "toServiceId": cache}).Status)
	require.Len(t, list(), 2)

	require.Equal(t, http.StatusNoContent, h.do("DELETE", "/links/"+manualID, "", nil).Status)
	links = list()
	require.Len(t, links, 2)
	require.Equal(t, "manual", links[0]["source"])
	require.Empty(t, links[0]["references"])
	require.Equal(t, "reference", links[1]["source"], "the derived edge outlives the manual link")
	require.Equal(t, "ref:"+api+":"+db, links[1]["id"])
}

func TestAuditFilters(t *testing.T) {
	h := newHarness(t)
	h.setup()
	svc, _ := h.newService("auditme")
	other, _ := h.newService("other")
	dep := h.deploy(svc, nil)["id"].(string)
	h.deploy(other, nil)

	actions := func(query string) []string {
		r := h.do("GET", "/audit-events?"+query, "", nil)
		require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
		var out []string
		for _, it := range r.JSON()["items"].([]any) {
			out = append(out, it.(map[string]any)["action"].(string))
		}
		return out
	}

	byType := actions("type=deployment.")
	require.Len(t, byType, 2)
	for _, a := range byType {
		require.True(t, strings.HasPrefix(a, "deployment."), a)
	}
	require.Contains(t, actions("type=service.create"), "service.create")
	require.NotContains(t, actions("type=service.create"), "deployment.create")

	require.Equal(t, []string{"deployment.create"}, actions("resourceId="+dep))
	require.Equal(t, []string{"service.create"}, actions("resourceId="+svc))

	r := h.do("GET", "/services/"+svc, "", nil)
	project := r.JSON()["projectId"].(string)
	inProject := actions("projectId=" + project)
	require.NotEmpty(t, inProject)
	require.Len(t, actions("projectId="+project+"&type=deployment."), 1)
	require.Empty(t, actions("projectId="+uuid.NewString()))

	// A token restricted to another project sees nothing of this one.
	otherProject := h.createProject("Elsewhere")
	tok := h.createToken([]string{"admin"}, []string{otherProject})
	rr := h.do("GET", "/audit-events?projectId="+project, tok, nil)
	require.Equal(t, http.StatusOK, rr.Status, string(rr.Raw))
	require.Empty(t, rr.JSON()["items"])
}

func TestDeployMessageAndEnvironmentDeployments(t *testing.T) {
	h := newHarness(t)
	h.setup()
	env := h.newEnv("List")
	srv := h.newOnlineServer("srv-list", 8*gib, gib)
	a := h.newServiceOn(env, "alpha", srv, nil)
	b := h.newServiceOn(env, "beta", srv, nil)

	d1 := h.deploy(a, map[string]any{"message": "first try"})
	require.Equal(t, "first try", d1["message"])
	d2 := h.deploy(b, nil)
	require.Nil(t, d2["message"])

	long := h.do("POST", "/services/"+a+"/deployments", "", map[string]any{"message": strings.Repeat("x", 201)})
	require.Equal(t, http.StatusUnprocessableEntity, long.Status, string(long.Raw))

	r := h.do("GET", "/environments/"+env+"/deployments", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	items := r.JSON()["items"].([]any)
	require.Len(t, items, 2)
	newest := items[0].(map[string]any)
	require.Equal(t, d2["id"], newest["id"], "newest first")
	require.Equal(t, "beta", newest["serviceName"])
	require.Equal(t, "beta", newest["serviceSlug"])
	require.Equal(t, "first try", items[1].(map[string]any)["message"])

	r = h.do("GET", "/environments/"+env+"/deployments?serviceId="+a, "", nil)
	require.Len(t, r.JSON()["items"], 1)
	r = h.do("GET", "/environments/"+env+"/deployments?status=failed,succeeded", "", nil)
	require.Empty(t, r.JSON()["items"])
	r = h.do("GET", "/environments/"+env+"/deployments?status=queued", "", nil)
	require.Len(t, r.JSON()["items"], 2)
	require.Equal(t, http.StatusUnprocessableEntity, h.do("GET", "/environments/"+env+"/deployments?status=bogus", "", nil).Status)

	r = h.do("GET", "/environments/"+env+"/deployments?limit=1", "", nil)
	require.Len(t, r.JSON()["items"], 1)
	next := r.JSON()["nextCursor"].(string)
	r = h.do("GET", "/environments/"+env+"/deployments?limit=1&cursor="+next, "", nil)
	require.Len(t, r.JSON()["items"], 1)

	// Another project's token cannot read it.
	other := h.createToken([]string{"projects:read"}, []string{h.createProject("Nope")})
	require.Equal(t, http.StatusNotFound, h.do("GET", "/environments/"+env+"/deployments", other, nil).Status)
}

func TestServiceResourceRailwayFields(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startJobs()
	rig := h.fleetRig()
	env := h.newEnv("Fields")

	srv := h.newOnlineServer("srv-fields", 8*gib, gib)
	svc := h.newServiceOn(env, "web", srv, nil)
	v := h.do("GET", "/services/"+svc, "", nil).JSON()
	require.Equal(t, "new", v["stagedKind"])
	require.Equal(t, map[string]any{"variables": float64(0), "settings": float64(1)}, v["pendingChangeCounts"])
	require.Equal(t, "running", v["desiredState"])
	require.Empty(t, v["volumes"])

	require.Equal(t, http.StatusCreated, h.do("POST", "/services/"+svc+"/volumes", "", map[string]any{"name": "data", "mountPath": "/data"}).Status)
	vols := h.do("GET", "/services/"+svc, "", nil).JSON()["volumes"].([]any)
	require.Len(t, vols, 1)
	require.Equal(t, "/data", vols[0].(map[string]any)["mountPath"])
	require.Equal(t, "data", vols[0].(map[string]any)["name"])
	require.NotEmpty(t, vols[0].(map[string]any)["id"])

	// A failed deployment reports the step it failed at and its message.
	dep := h.deploy(svc, map[string]any{"message": "try"})["id"].(string)
	h.awaitPrepared(dep)
	_, err := h.app.Pool.Exec(context.Background(),
		`INSERT INTO deployment_events (deployment_id, step, status, message) VALUES ($1, 'verify', 'failed', 'boom')`, uuid.MustParse(dep))
	require.NoError(t, err)
	rig.succeed(dep, srv, agentprotocol.OutcomeFailed, "verify failed")
	latest := h.do("GET", "/services/"+svc, "", nil).JSON()["latestDeployment"].(map[string]any)
	require.Equal(t, "failed", latest["status"])
	require.Equal(t, "verify", latest["failedStep"])
	require.Equal(t, "try", latest["message"])

	// Deploy again and succeed: nothing staged, stop/restart flip the desired state.
	dep = h.deploy(svc, nil)["id"].(string)
	h.awaitPrepared(dep)
	rig.succeed(dep, srv, agentprotocol.OutcomeSucceeded, "")
	v = h.do("GET", "/services/"+svc, "", nil).JSON()
	require.Nil(t, v["stagedKind"])
	require.Nil(t, v["latestDeployment"].(map[string]any)["failedStep"])
	require.Equal(t, http.StatusAccepted, h.do("POST", "/services/"+svc+"/stop", "", nil).Status)
	require.Equal(t, "stopped", h.do("GET", "/services/"+svc, "", nil).JSON()["desiredState"])
	require.Equal(t, http.StatusAccepted, h.do("POST", "/services/"+svc+"/restart", "", nil).Status)
	require.Equal(t, "running", h.do("GET", "/services/"+svc, "", nil).JSON()["desiredState"])
}
