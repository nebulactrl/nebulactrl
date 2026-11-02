package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// httpDo drives one request through the registered deployment endpoints as the
// principal carried by ctx (the way the auth middleware would attach it).
func (f *routeFixture) httpDo(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	p, err := policy.Authenticated(ctx)
	require.NoError(f.t, err)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.deps)

	var rd *bytes.Reader
	if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func recJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func recCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return recJSON(t, rec)["code"].(string)
}

func recItems(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items, ok := recJSON(t, rec)["items"].([]any)
	require.True(t, ok, "items is a list, never null: %s", rec.Body.String())
	return items
}

// principalWith is a token principal of the fixture's workspace holding only the scopes.
func (f *routeFixture) principalWith(scopes ...string) context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, TokenID: new(uuid.New()), UserID: p.UserID, WorkspaceID: p.WorkspaceID, Label: "tok", Scopes: scopes,
	})
}

func TestHTTPPlanReportsStrategySourceAndTargetWithoutStartingAnything(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:v2","port":80}`)
	path := "/services/" + web.ID.String() + "/deployments/plan"

	rec := f.httpDo(f.ctx, http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	plan := recJSON(t, rec)
	require.Equal(t, "rolling", plan["strategy"])
	require.Equal(t, "image", plan["source"].(map[string]any)["type"])
	require.Equal(t, "traefik/whoami:v2", plan["source"].(map[string]any)["image"])
	require.Empty(t, plan["blocking"], "an online server and a valid config block nothing")
	changes := plan["changes"].([]any)
	require.NotEmpty(t, changes, "the staged image change is listed")
	require.Equal(t, 1, f.deploymentCount(web.ID), "a dry run creates no deployment")

	rec = f.httpDo(f.ctx, http.MethodPost, "/services/"+uuid.NewString()+"/deployments/plan", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = f.httpDo(f.principalWith(policy.ScopeServersRead), http.MethodPost, path, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, "planning needs projects:read")
}

func TestHTTPDeployCancelAndTheDeploymentReadEndpoints(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	serviceDeployments := "/services/" + web.ID.String() + "/deployments"

	// The note is validated by the schema before the service runs.
	rec := f.httpDo(f.ctx, http.MethodPost, serviceDeployments, map[string]any{"message": strings.Repeat("x", 201)})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, 1, f.deploymentCount(web.ID))

	// A request without a body is a plain deploy; with one it carries the note.
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:v2","port":80}`)
	rec = f.httpDo(f.ctx, http.MethodPost, serviceDeployments, map[string]any{"message": "  ship it  "})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	dep := recJSON(t, rec)
	depID := dep["id"].(string)
	require.Equal(t, web.ID.String(), dep["serviceId"])
	require.Equal(t, "manual", dep["trigger"])
	require.Equal(t, "ship it", dep["message"], "the note is trimmed")
	require.Equal(t, 2, f.deploymentCount(web.ID))

	// Only one deployment of a service runs at a time.
	rec = f.httpDo(f.ctx, http.MethodPost, serviceDeployments, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "deployment_in_progress", recCode(t, rec))

	// The service's list is newest first and carries the new deployment.
	rec = f.httpDo(f.ctx, http.MethodGet, serviceDeployments+"?limit=1", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := recJSON(t, rec)
	require.Len(t, page["items"], 1)
	require.Equal(t, depID, page["items"].([]any)[0].(map[string]any)["id"])
	require.NotEmpty(t, page["nextCursor"], "the older (fixture) deployment is on the next page")
	rec = f.httpDo(f.ctx, http.MethodGet, serviceDeployments+"?cursor="+page["nextCursor"].(string), nil)
	older := recItems(t, rec)
	require.Len(t, older, 1)
	require.NotEqual(t, depID, older[0].(map[string]any)["id"])
	rec = f.httpDo(f.ctx, http.MethodGet, serviceDeployments+"?limit=0", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the page size is bounded by the schema")

	rec = f.httpDo(f.ctx, http.MethodGet, "/deployments/"+depID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, depID, recJSON(t, rec)["id"])
	rec = f.httpDo(f.ctx, http.MethodGet, "/deployments/"+uuid.NewString(), nil)
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Nothing ran yet: the timeline only has the status changes and the command list is an empty list.
	var statuses []string
	for _, e := range recItems(t, f.httpDo(f.ctx, http.MethodGet, "/deployments/"+depID+"/events", nil)) {
		statuses = append(statuses, e.(map[string]any)["status"].(string))
	}
	require.Equal(t, []string{"created", "queued"}, statuses)
	require.Empty(t, recItems(t, f.httpDo(f.ctx, http.MethodGet, "/deployments/"+depID+"/commands", nil)))

	// Cancelling before the agent acknowledged ends the deployment right away; a second cancel is too late.
	rec = f.httpDo(f.ctx, http.MethodPost, "/deployments/"+depID+"/cancel", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "canceled", recJSON(t, rec)["status"])
	rec = f.httpDo(f.ctx, http.MethodPost, "/deployments/"+depID+"/cancel", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "cannot_cancel", recCode(t, rec))
	rec = f.httpDo(f.ctx, http.MethodPost, "/deployments/"+uuid.NewString()+"/cancel", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodPost, "/deployments/"+depID+"/cancel", nil).Code)
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodPost, serviceDeployments, nil).Code, "deploying needs deployments:write")
}

func TestHTTPDeployOptionsThatNeedAGitSourceAreRefusedForAnImageService(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:latest","port":80}`)
	path := "/services/" + web.ID.String() + "/deployments"
	for name, body := range map[string]map[string]any{
		"commitSha": {"commitSha": strings.Repeat("a", 40)},
		"noCache":   {"noCache": true},
	} {
		rec := f.httpDo(f.ctx, http.MethodPost, path, body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name+": "+rec.Body.String())
		require.Equal(t, "validation_failed", recCode(t, rec), name)
		require.Contains(t, rec.Body.String(), "git", name)
	}
	require.Equal(t, 1, f.deploymentCount(web.ID), "a refused request starts nothing")
}

func TestHTTPReleaseEventsCommandsAndLogsOfARunDeployment(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	id, _ := f.deployWeb(web.ID)
	base := "/deployments/" + id.String()

	events := recItems(t, f.httpDo(f.ctx, http.MethodGet, base+"/events", nil))
	require.NotEmpty(t, events, "the run recorded its steps")
	for _, e := range events {
		require.NotEmpty(t, e.(map[string]any)["step"])
	}

	// The fake dispatcher stores nothing: persist the command the way the hub does.
	_, err := f.pool.Exec(context.Background(), `INSERT INTO agent_commands (server_id, deployment_id, kind, payload, idempotency_key, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '1 hour')`, f.server.ID, id, agentprotocol.KindDeploy,
		`{"serviceSlug":"api","registryPassword":"hunter2-canary"}`, uuid.NewString())
	require.NoError(t, err)
	rec := f.httpDo(f.ctx, http.MethodGet, base+"/commands", nil)
	cmds := recItems(t, rec)
	require.Len(t, cmds, 1)
	cmd := cmds[0].(map[string]any)
	require.Equal(t, agentprotocol.KindDeploy, cmd["kind"])
	require.Equal(t, "agent1", cmd["serverName"])
	require.Equal(t, "api", cmd["payload"].(map[string]any)["serviceSlug"])
	require.NotContains(t, rec.Body.String(), "hunter2-canary", "credential fields never leave the API")

	rec = f.httpDo(f.ctx, http.MethodGet, base+"/release", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rel := recJSON(t, rec)
	require.Equal(t, id.String(), rel["deploymentId"])
	require.Equal(t, web.ID.String(), rel["serviceId"])
	require.Equal(t, "traefik/whoami:latest", rel["imageRef"])

	// Persisted lines: the debug stream is only returned when asked for.
	for _, l := range []struct{ stream, line string }{{"build", "b1"}, {"deploy", "d1"}, {"debug", "dbg"}, {"runtime", "r1"}} {
		_, err = f.pool.Exec(context.Background(), `INSERT INTO deployment_logs (deployment_id, stream, line) VALUES ($1, $2, $3)`, id, l.stream, l.line)
		require.NoError(t, err)
	}
	lines := func(q string) []string {
		var out []string
		for _, it := range recItems(t, f.httpDo(f.ctx, http.MethodGet, base+"/logs"+q, nil)) {
			out = append(out, it.(map[string]any)["line"].(string))
		}
		return out
	}
	require.Equal(t, []string{"b1", "d1", "r1"}, lines(""))
	require.Equal(t, []string{"b1", "d1", "dbg", "r1"}, lines("?streams=all"))
	require.Equal(t, []string{"dbg"}, lines("?streams=debug"))
	rec = f.httpDo(f.ctx, http.MethodGet, base+"/logs?streams=bogus", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodGet, base+"/logs", nil).Code, "logs need logs:read")
	require.Equal(t, http.StatusNotFound, f.httpDo(f.ctx, http.MethodGet, "/deployments/"+uuid.NewString()+"/logs", nil).Code)

	// Following a finished deployment replays the lines and ends the stream with its status.
	rec = f.httpDo(f.ctx, http.MethodGet, base+"/logs?follow=true", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	body := rec.Body.String()
	require.Contains(t, body, `"line":"b1"`)
	require.Contains(t, body, `"line":"r1"`)
	require.NotContains(t, body, `"line":"dbg"`)
	require.Contains(t, body, "event: end")
	require.Contains(t, body, `"status":"succeeded"`)

	// The same lines as a file: NDJSON with the stream per line, named after the service.
	rec = f.httpDo(f.ctx, http.MethodGet, base+"/logs/download?format=ndjson&streams=build", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Disposition"), "api-deploy-"+id.String()[:8])
	require.Contains(t, rec.Body.String(), `"stream":"build"`)
	require.Contains(t, rec.Body.String(), `"line":"b1"`)
	require.NotContains(t, rec.Body.String(), "d1")
	require.Equal(t, http.StatusUnprocessableEntity, f.httpDo(f.ctx, http.MethodGet, base+"/logs/download?format=csv", nil).Code)
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodGet, base+"/logs/download", nil).Code)

	// A deployment that has not produced a release has no release detail.
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:v3","port":80}`)
	v, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	require.NoError(t, err)
	rec = f.httpDo(f.ctx, http.MethodGet, "/deployments/"+v.ID.String()+"/release", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestHTTPRollbackCreatesADeploymentOfTheOldReleaseWithTheDisclaimer(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:v1","port":80}`)
	first, _ := f.deployWeb(web.ID)
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:v2","port":80}`)
	second, _ := f.deployWeb(web.ID)

	// The running deployment is not a rollback target.
	rec := f.httpDo(f.ctx, http.MethodPost, "/deployments/"+second.String()+"/rollback", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "already_current", recCode(t, rec))

	rec = f.httpDo(f.ctx, http.MethodPost, "/deployments/"+first.String()+"/rollback", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	rb := recJSON(t, rec)
	require.Equal(t, RollbackDisclaimer, rb["disclaimer"])
	require.Equal(t, "rollback", rb["trigger"])
	require.Equal(t, second.String(), rb["rolledBackFrom"], "the deployment that was running is recorded")
	require.Equal(t, web.ID.String(), rb["serviceId"])

	// A rollback needs its own scope, and an unknown deployment is a 404.
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeDeploymentsWrite), http.MethodPost, "/deployments/"+first.String()+"/rollback", nil).Code)
	require.Equal(t, http.StatusNotFound, f.httpDo(f.ctx, http.MethodPost, "/deployments/"+uuid.NewString()+"/rollback", nil).Code)

	// A deployment that never succeeded cannot be rolled back to.
	rbID := rb["id"].(string)
	rec = f.httpDo(f.ctx, http.MethodPost, "/deployments/"+rbID+"/rollback", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "not_rollbackable", recCode(t, rec))
}

func TestHTTPPendingChangesListAndDiscardOne(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	base := "/services/" + web.ID.String() + "/pending-changes"

	require.Empty(t, recItems(t, f.httpDo(f.ctx, http.MethodGet, base, nil)), "a deployed, unchanged service has nothing staged")

	setConfig(t, f, web.ID, `{"image":"traefik/whoami:latest","port":8080}`)
	changes := map[string]map[string]any{}
	for _, it := range recItems(t, f.httpDo(f.ctx, http.MethodGet, base, nil)) {
		changes[it.(map[string]any)["field"].(string)] = it.(map[string]any)
	}
	require.Contains(t, changes, "config.port")
	require.Equal(t, "80", changes["config.port"]["from"])
	require.Equal(t, "8080", changes["config.port"]["to"])

	rec := f.httpDo(f.ctx, http.MethodDelete, base+"/config.port", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("ETag"), "the new generation is the ETag")
	require.Equal(t, web.ID.String(), recJSON(t, rec)["id"])
	for _, it := range recItems(t, f.httpDo(f.ctx, http.MethodGet, base, nil)) {
		require.NotEqual(t, "config.port", it.(map[string]any)["field"], "the discarded change is gone")
	}

	rec = f.httpDo(f.ctx, http.MethodDelete, base+"/config.port", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, "a field that is not pending is a 404")
	require.Equal(t, http.StatusNotFound, f.httpDo(f.ctx, http.MethodGet, "/services/"+uuid.NewString()+"/pending-changes", nil).Code)
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodDelete, base+"/config.port", nil).Code, "discarding needs projects:write")
}

func TestHTTPDeployStagedStartsServicesWithChangesOnly(t *testing.T) {
	f := newRouteFixture(t)
	changed := f.web("changed")
	f.web("unchanged")
	setConfig(t, f, changed.ID, `{"image":"traefik/whoami:v2","port":80}`)
	path := "/environments/" + f.env.ID.String() + "/deploy-staged"

	rec := f.httpDo(f.ctx, http.MethodPost, path, map[string]any{"message": strings.Repeat("y", 201)})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = f.httpDo(f.ctx, http.MethodPost, path, map[string]any{"message": "batch"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	res := recJSON(t, rec)
	items := res["items"].([]any)
	require.Len(t, items, 1, "only the service with staged changes is started")
	require.Equal(t, changed.ID.String(), items[0].(map[string]any)["serviceId"])
	require.Equal(t, "batch", items[0].(map[string]any)["message"])
	require.Equal(t, "staged", items[0].(map[string]any)["trigger"])
	require.Equal(t, []any{changed.ID.String()}, res["order"])

	// Naming a service that is already deploying is a conflict; naming a stranger is a 404.
	rec = f.httpDo(f.ctx, http.MethodPost, path, map[string]any{"serviceIds": []string{changed.ID.String()}})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "deployment_in_progress", recCode(t, rec))
	rec = f.httpDo(f.ctx, http.MethodPost, path, map[string]any{"serviceIds": []string{uuid.NewString()}})
	require.Equal(t, http.StatusNotFound, rec.Code)
	// Without a name, the busy service is skipped silently.
	rec = f.httpDo(f.ctx, http.MethodPost, path, map[string]any{})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Empty(t, recJSON(t, rec)["items"])

	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodPost, path, map[string]any{}).Code)
}

func TestHTTPEnvironmentDeploymentListFiltersAndDailyOutcomes(t *testing.T) {
	f := newRouteFixture(t)
	a := f.web("alpha")
	b := f.web("beta")
	setConfig(t, f, b.ID, `{"image":"traefik/whoami:v2","port":80}`)
	queued, err := f.deps.Deploy(f.ctx, b.ID, "", "")
	require.NoError(t, err)
	env := "/environments/" + f.env.ID.String() + "/deployments"

	all := recItems(t, f.httpDo(f.ctx, http.MethodGet, env, nil))
	require.Len(t, all, 3)
	require.Equal(t, queued.ID.String(), all[0].(map[string]any)["id"], "newest first")
	require.Equal(t, "beta", all[0].(map[string]any)["serviceSlug"])
	require.Equal(t, "beta", all[0].(map[string]any)["serviceName"])

	ok := recItems(t, f.httpDo(f.ctx, http.MethodGet, env+"?status=succeeded", nil))
	require.Len(t, ok, 2)
	for _, d := range ok {
		require.Equal(t, "succeeded", d.(map[string]any)["status"])
	}
	mixed := recItems(t, f.httpDo(f.ctx, http.MethodGet, env+"?status=succeeded,%20queued", nil))
	require.Len(t, mixed, 3, "a comma list combines statuses")
	onlyA := recItems(t, f.httpDo(f.ctx, http.MethodGet, env+"?serviceId="+a.ID.String(), nil))
	require.Len(t, onlyA, 1)
	require.Equal(t, "alpha", onlyA[0].(map[string]any)["serviceSlug"])

	rec := f.httpDo(f.ctx, http.MethodGet, env+"?status=exploded", nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusUnprocessableEntity, f.httpDo(f.ctx, http.MethodGet, env+"?serviceId=not-a-uuid", nil).Code)
	rec = f.httpDo(f.ctx, http.MethodGet, env+"?limit=2", nil)
	page := recJSON(t, rec)
	require.Len(t, page["items"], 2)
	require.NotEmpty(t, page["nextCursor"])
	require.Equal(t, http.StatusNotFound, f.httpDo(f.ctx, http.MethodGet, "/environments/"+uuid.NewString()+"/deployments", nil).Code)

	// Daily outcomes: one entry per day ending today, today's deployments counted.
	rec = f.httpDo(f.ctx, http.MethodGet, env+"/daily?days=3", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	days := recJSON(t, rec)["days"].([]any)
	require.Len(t, days, 3)
	today := days[2].(map[string]any)
	require.Equal(t, float64(2), today["succeeded"])
	require.Equal(t, float64(1), today["other"], "the queued deployment is in flight")
	require.Equal(t, float64(0), days[0].(map[string]any)["succeeded"])
	require.Len(t, recJSON(t, f.httpDo(f.ctx, http.MethodGet, env+"/daily", nil))["days"], 30, "the window defaults to 30 days")
	require.Equal(t, http.StatusUnprocessableEntity, f.httpDo(f.ctx, http.MethodGet, env+"/daily?days=0", nil).Code)
	require.Equal(t, http.StatusUnprocessableEntity, f.httpDo(f.ctx, http.MethodGet, env+"/daily?days=91", nil).Code)
}

func TestHTTPBuildPlanIsOnlyForGitServices(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	path := "/services/" + web.ID.String() + "/build-plan"
	rec := f.httpDo(f.ctx, http.MethodPost, path, nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, "not_git_source", recCode(t, rec))
	require.Equal(t, http.StatusNotFound, f.httpDo(f.ctx, http.MethodPost, "/services/"+uuid.NewString()+"/build-plan", nil).Code)
	require.Equal(t, http.StatusForbidden, f.httpDo(f.principalWith(policy.ScopeProjectsRead), http.MethodPost, path, nil).Code, "planning makes the server clone: deployments:write")
}
