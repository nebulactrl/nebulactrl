package databases_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// httpCall drives one request through the registered database endpoints as the
// principal carried by ctx (the way the auth middleware would attach it).
func httpCall(t *testing.T, ctx context.Context, svc *databases.Service, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	p, err := policy.Authenticated(ctx)
	require.NoError(t, err)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	databases.Register(humachi.New(r, cfg), svc)

	var rd *bytes.Reader
	if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
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

func httpErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e.Code
}

func httpJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestHTTPCatalogListsEnginesAndNeedsReadScope(t *testing.T) {
	f := newFixture(t)
	rec := httpCall(t, f.ctx, f.dbs, http.MethodGet, "/database-engines", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Engines []struct {
			ID string `json:"id"`
		} `json:"engines"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	ids := map[string]bool{}
	for _, e := range out.Engines {
		ids[e.ID] = true
	}
	require.True(t, ids["postgres"] && ids["valkey"] && ids["mongodb"], "catalog lists the engines: %v", ids)

	none := f.principal([]string{policy.ScopeServersRead}, nil)
	rec = httpCall(t, none, f.dbs, http.MethodGet, "/database-engines", nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHTTPCreateDatabaseReturnsStagedServiceWithETag(t *testing.T) {
	f := newFixture(t)
	path := "/environments/" + f.env.ID.String() + "/databases"

	rec := httpCall(t, f.ctx, f.dbs, http.MethodPost, path, map[string]any{
		"name": "Main DB", "engine": "postgres", "version": "17", "databaseName": "shop", "serverId": f.server.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	body := httpJSON(t, rec)
	require.Equal(t, "database", body["kind"])
	require.NotEmpty(t, rec.Header().Get("ETag"), "the generation is exposed as an ETag")
	id := uuid.MustParse(body["id"].(string))
	require.Equal(t, "shop", f.getVar(id, "POSTGRES_DB"))

	// A source-available engine without the licence acknowledgement is a 422 with its machine code.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, path, map[string]any{"engine": "mongodb"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "license_not_accepted", httpErrCode(t, rec))
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, path, map[string]any{"engine": "mongodb", "acceptLicense": true})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// The schema rejects an engine outside the catalogue and a malformed database name before the service runs.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, path, map[string]any{"engine": "oracle"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, path, map[string]any{"engine": "postgres", "databaseName": "Bad Name"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	// An unsupported version passes the schema and is refused by the catalogue check.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, path, map[string]any{"engine": "postgres", "version": "9.6"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "invalid_database", httpErrCode(t, rec))

	ro := f.principal([]string{policy.ScopeProjectsRead}, nil)
	rec = httpCall(t, ro, f.dbs, http.MethodPost, path, map[string]any{"engine": "valkey"})
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHTTPGetRefreshAndRevealOfADatabase(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	base := "/services/" + pg.ID.String() + "/database"
	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")

	rec := httpCall(t, f.ctx, f.dbs, http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), pw, "reads stay masked")
	require.Equal(t, "postgres", httpJSON(t, rec)["engine"].(map[string]any)["id"])

	// A database that never deployed has no agent to ask.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/refresh", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "not_deployed", httpErrCode(t, rec))
	f.deploy(pg.ID)
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/refresh", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	cmd, ok := f.lastCommand(agentprotocol.KindDBAdmin)
	require.True(t, ok)
	require.Equal(t, cmd.ID.String(), httpJSON(t, rec)["commandId"])

	// Reveal is uncached and returns the plaintext export.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/reveal", map[string]any{"key": "DATABASE_URL"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-store, max-age=0", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-cache", rec.Header().Get("Pragma"))
	require.Equal(t, "postgres://postgres:"+pw+"@pg:5432/app", httpJSON(t, rec)["value"])

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/reveal", map[string]any{"key": "POSTGRES_PASSWORD"})
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/reveal", map[string]any{"key": ""})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an empty key fails the schema")

	// Without deployments:write the plaintext stays hidden.
	ro := f.principal([]string{policy.ScopeProjectsRead}, nil)
	rec = httpCall(t, ro, f.dbs, http.MethodPost, base+"/reveal", map[string]any{"key": "DATABASE_URL"})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), pw)

	// A plain service is not a database.
	app := f.app("plain")
	rec = httpCall(t, f.ctx, f.dbs, http.MethodGet, "/services/"+app.ID.String()+"/database", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "not_a_managed_database", httpErrCode(t, rec))
}

func TestHTTPRotateCredentialsDryRunAndApply(t *testing.T) {
	f := newFixture(t)
	kv := f.createDB("cache", "valkey", "9")
	base := "/services/" + kv.ID.String() + "/database/rotate-credentials"
	oldPW := f.getVar(kv.ID, "VALKEY_PASSWORD")

	rec := httpCall(t, f.ctx, f.dbs, http.MethodPost, base, map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := httpJSON(t, rec)
	require.Equal(t, "planned", out["status"])
	require.Equal(t, true, out["dryRun"])
	require.Equal(t, oldPW, f.getVar(kv.ID, "VALKEY_PASSWORD"), "a dry run changes nothing")

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base, map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "applied", httpJSON(t, rec)["status"])
	require.NotEqual(t, oldPW, f.getVar(kv.ID, "VALKEY_PASSWORD"))

	pwOnly := f.principal([]string{policy.ScopeProjectsWrite}, nil)
	rec = httpCall(t, pwOnly, f.dbs, http.MethodPost, base, map[string]any{})
	require.Equal(t, http.StatusForbidden, rec.Code, "rotation needs deployments:write as well")
}

func TestHTTPUpgradePlanAndUpgrade(t *testing.T) {
	f := newFixture(t)
	pg := f.createDB("pg", "postgres", "17")
	f.deploy(pg.ID)
	base := "/services/" + pg.ID.String() + "/database/upgrade"

	rec := httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/plan", map[string]any{"targetVersion": "18"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	plan := httpJSON(t, rec)
	require.Equal(t, "pg_upgrade", plan["method"])
	require.Equal(t, true, plan["executable"])

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base, map[string]any{"targetVersion": "18", "confirm": "wrong"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "confirm_mismatch", httpErrCode(t, rec))
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base, map[string]any{"targetVersion": "", "confirm": pg.Slug})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an empty target fails the schema")

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base, map[string]any{"targetVersion": "18", "confirm": pg.Slug})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "pending", httpJSON(t, rec)["status"])
	_, ok := f.lastCommand(agentprotocol.KindDBUpgrade)
	require.True(t, ok, "the conversion was sent to the agent")

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base, map[string]any{"targetVersion": "18", "confirm": pg.Slug})
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "upgrade_blocked", httpErrCode(t, rec))
}

func TestHTTPPublicPortLifecycle(t *testing.T) {
	f := newFixture(t)
	f.proxyServer("198.51.100.7")
	pg := f.createDB("pg", "postgres", "17")
	base := "/services/" + pg.ID.String() + "/database/public-port"

	rec := httpCall(t, f.ctx, f.dbs, http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, false, httpJSON(t, rec)["enabled"])

	// The allow-list is mandatory: the schema rejects an empty one.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPut, base, map[string]any{"listenPort": 15432, "allowCidrs": []string{}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPut, base, map[string]any{"listenPort": 80, "allowCidrs": []string{"10.0.0.0/8"}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "below the minimum listen port")
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPut, base, map[string]any{"listenPort": 15432, "allowCidrs": []string{"0.0.0.0/0"}})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "open_confirmation_required", httpErrCode(t, rec))

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPut, base, map[string]any{
		"listenPort": 15432, "allowCidrs": []string{"203.0.113.0/24"}, "denyCidrs": []string{"203.0.113.66"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	view := httpJSON(t, rec)
	require.Equal(t, true, view["enabled"])
	require.Equal(t, float64(15432), view["listenPort"])
	require.Equal(t, []any{"203.0.113.66"}, view["denyCidrs"])

	// The connection reveal names the public address and is uncached.
	pw := f.getVar(pg.ID, "POSTGRES_PASSWORD")
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/connection", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-store, max-age=0", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-cache", rec.Header().Get("Pragma"))
	conn := httpJSON(t, rec)
	require.Equal(t, "postgres://postgres:"+pw+"@198.51.100.7:15432/app", conn["value"])
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/connection", map[string]any{"key": "NOPE"})
	require.Equal(t, http.StatusNotFound, rec.Code)

	// A caller with projects:write but without proxy:write cannot publish or close it.
	pw2 := f.principal([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}, nil)
	rec = httpCall(t, pw2, f.dbs, http.MethodDelete, base, nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = httpCall(t, pw2, f.dbs, http.MethodPost, base+"/connection", map[string]any{})
	require.Equal(t, http.StatusForbidden, rec.Code, "the reveal needs deployments:write")

	rec = httpCall(t, f.ctx, f.dbs, http.MethodDelete, base, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, false, httpJSON(t, rec)["enabled"])
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/connection", map[string]any{})
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "public_port_disabled", httpErrCode(t, rec))
}

func TestHTTPReplicationAddRemoveAndGet(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	base := "/services/" + pg.ID.String() + "/database"

	rec := httpCall(t, f.ctx, f.dbs, http.MethodGet, base+"/replication", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	view := httpJSON(t, rec)
	require.Equal(t, "standalone", view["role"])
	require.Equal(t, []any{}, view["replicas"], "no replicas is an empty list, not null")

	// A dry run is a 200 with the plan and creates nothing.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/replicas", map[string]any{"serverId": f.server.ID, "dryRun": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	dry := httpJSON(t, rec)
	require.Equal(t, true, dry["dryRun"])
	require.Equal(t, true, dry["plan"].(map[string]any)["sameServer"])
	require.Nil(t, dry["serviceId"])

	// A real request is a 202 with the new replica service.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/replicas", map[string]any{"serverId": f.server.ID, "name": "pg reader"})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	created := httpJSON(t, rec)
	rid := uuid.MustParse(created["serviceId"].(string))
	require.Equal(t, "pending", f.replica(rid).State)

	rec = httpCall(t, f.ctx, f.dbs, http.MethodGet, base+"/replication", nil)
	view = httpJSON(t, rec)
	require.Equal(t, "primary", view["role"])
	require.Len(t, view["replicas"], 1)
	rec = httpCall(t, f.ctx, f.dbs, http.MethodGet, "/services/"+rid.String()+"/database/replication", nil)
	view = httpJSON(t, rec)
	require.Equal(t, "replica", view["role"])
	require.Equal(t, pg.Slug, view["primary"].(map[string]any)["slug"])

	// A server of another workspace member that does not exist is refused with the plan's blocker.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/replicas", map[string]any{"serverId": uuid.New()})
	require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
	// Both scopes are needed.
	pwOnly := f.principal([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}, nil)
	rec = httpCall(t, pwOnly, f.dbs, http.MethodPost, base+"/replicas", map[string]any{"serverId": f.server.ID})
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Removing is called on the replica; the primary is not a replica.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodDelete, base+"/replica", nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "not_a_replica", httpErrCode(t, rec))
	rec = httpCall(t, f.ctx, f.dbs, http.MethodDelete, "/services/"+rid.String()+"/database/replica", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, "removing", httpJSON(t, rec)["status"])
	require.Equal(t, databases.ReplicaRemoving, f.replica(rid).State)
}

func TestHTTPPromoteDryRunConfirmAndRebuild(t *testing.T) {
	f := newReplicaFixture(t)
	pg := f.primary("pg")
	id := f.streamingReplica(pg.ID, f.server.ID)
	slug := f.reload(id).Slug
	base := "/services/" + id.String() + "/database"

	// The dry run is a 200 with the plan; nothing changes.
	rec := httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/promote", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	plan := httpJSON(t, rec)
	require.Equal(t, true, plan["dryRun"])
	require.Equal(t, "switchover", plan["plan"].(map[string]any)["mode"])
	require.Equal(t, databases.ReplicaStreaming, f.replica(id).State)

	// A real promote needs the typed slug.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/promote", map[string]any{"confirm": "nope"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "promote_confirmation_required", httpErrCode(t, rec))

	// A streaming replica is not rebuilt without waiting? It may be; a primary may not be.
	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, "/services/"+pg.ID.String()+"/database/rebuild", map[string]any{})
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "replica_not_rebuildable", httpErrCode(t, rec))

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/rebuild", map[string]any{})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, databases.ReplicaPending, httpJSON(t, rec)["status"])

	rec = httpCall(t, f.ctx, f.dbs, http.MethodPost, base+"/rebuild", map[string]any{})
	require.Equal(t, http.StatusConflict, rec.Code, "a rebuild in flight cannot be repeated")

	// The real promote answers 202 with the running promotion.
	f2 := newReplicaFixture(t)
	pg2 := f2.primary("pg")
	id2 := f2.streamingReplica(pg2.ID, f2.server.ID)
	rec = httpCall(t, f2.ctx, f2.dbs, http.MethodPost, "/services/"+id2.String()+"/database/promote", map[string]any{"confirm": f2.reload(id2).Slug})
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	started := httpJSON(t, rec)
	require.Equal(t, "running", started["status"])
	require.NotNil(t, started["promotionId"])
	_ = slug
}
