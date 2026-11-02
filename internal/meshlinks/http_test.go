package meshlinks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// httpCall drives one request through the registered endpoints as the principal
// carried by ctx (a ctx without one is an anonymous caller).
func httpCall(t *testing.T, ctx context.Context, svc *Service, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	p, _ := auth.PrincipalFrom(ctx)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), svc)

	rd := bytes.NewReader(nil)
	if body != nil {
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

func httpJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func (f *fx) reader() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ro", Scopes: []string{policy.ScopeProjectsRead}})
}

func TestHTTPEnvironmentAndServiceMesh(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api.ID))

	rec := httpCall(t, f.ctx, f.s, http.MethodGet, "/environments/"+f.env.ID.String()+"/mesh", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	links := m["links"].([]any)
	require.Len(t, links, 1)
	link := links[0].(map[string]any)
	require.Equal(t, "srv-a", link["from"].(map[string]any)["name"])
	require.Equal(t, "srv-b", link["to"].(map[string]any)["name"])
	grants := m["grants"].([]any)
	require.Len(t, grants, 1)
	require.Equal(t, "api", grants[0].(map[string]any)["consumerSlug"])
	require.Equal(t, "postgres", grants[0].(map[string]any)["providerSlug"])
	require.Equal(t, "reference", grants[0].(map[string]any)["source"])
	routes := m["routes"].([]any)
	require.Len(t, routes, 1)
	require.EqualValues(t, 5432, routes[0].(map[string]any)["port"])

	// The provider lists its exposure and consumer; the consumer its route.
	rec = httpCall(t, f.reader(), f.s, http.MethodGet, "/services/"+pg.ID.String()+"/mesh", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m = httpJSON(t, rec)
	require.Len(t, m["exposures"], 1)
	require.Equal(t, "api", m["consumers"].([]any)[0].(map[string]any)["slug"])
	rec = httpCall(t, f.ctx, f.s, http.MethodGet, "/services/"+api.ID.String()+"/mesh", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "postgres", httpJSON(t, rec)["routes"].([]any)[0].(map[string]any)["alias"])

	// An environment with nothing to link answers empty lists, never null.
	empty, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	rec = httpCall(t, f.ctx, f.s, http.MethodGet, "/environments/"+empty.ID.String()+"/mesh", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"links":[]`)
	require.Contains(t, rec.Body.String(), `"grants":[]`)

	// Unknown ids and missing credentials.
	for _, path := range []string{"/environments/" + uuid.NewString() + "/mesh", "/services/" + uuid.NewString() + "/mesh"} {
		require.Equal(t, http.StatusNotFound, httpCall(t, f.ctx, f.s, http.MethodGet, path, nil).Code, path)
	}
	for _, path := range []string{"/environments/" + f.env.ID.String() + "/mesh", "/services/" + pg.ID.String() + "/mesh"} {
		require.Equal(t, http.StatusUnauthorized, httpCall(t, context.Background(), f.s, http.MethodGet, path, nil).Code, path)
		noScope := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "x", Scopes: []string{policy.ScopeServersRead}})
		require.Equal(t, http.StatusForbidden, httpCall(t, noScope, f.s, http.MethodGet, path, nil).Code, path)
	}
}

func TestHTTPReconcileMesh(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	f.service("api", services.KindApp, &f.a, 8080, refVars)
	path := "/environments/" + f.env.ID.String() + "/mesh/reconcile"

	// Adding devices needs the caller's confirmation, and nothing changes without it.
	rec := httpCall(t, f.ctx, f.s, http.MethodPost, path, map[string]any{"mode": "add"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeMeshConfirmationRequired, httpJSON(t, rec)["code"])
	require.Empty(t, f.grants())

	rec = httpCall(t, f.ctx, f.s, http.MethodPost, path, map[string]any{"mode": "add", "acceptMeshChanges": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := httpJSON(t, rec)
	require.Equal(t, "add", m["mode"])
	require.EqualValues(t, 1, m["devicesAdded"])
	added := m["added"].([]any)
	require.Len(t, added, 1)
	require.Equal(t, "api", added[0].(map[string]any)["consumer"])
	require.Len(t, f.grants(), 1)

	// Repair needs no confirmation and removes nothing that is still referenced.
	rec = httpCall(t, f.ctx, f.s, http.MethodPost, path, map[string]any{"mode": "repair"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, httpJSON(t, rec)["removed"])
	require.Len(t, f.grants(), 1)

	// Bad mode (enum), unknown environment, missing scope, anonymous.
	rec = httpCall(t, f.ctx, f.s, http.MethodPost, path, map[string]any{"mode": "destroy"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = httpCall(t, f.ctx, f.s, http.MethodPost, "/environments/"+uuid.NewString()+"/mesh/reconcile", map[string]any{"mode": "repair"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = httpCall(t, f.reader(), f.s, http.MethodPost, path, map[string]any{"mode": "repair"})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = httpCall(t, context.Background(), f.s, http.MethodPost, path, map[string]any{"mode": "repair"})
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

func TestHTTPReconcileNamesTheBlockerWhenTheMeshCannotServeANeed(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.s.mesh = nil

	rec := httpCall(t, f.ctx, f.s, http.MethodPost, "/environments/"+f.env.ID.String()+"/mesh/reconcile", map[string]any{"mode": "add", "acceptMeshChanges": true})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Equal(t, apierr.CodeMeshNotConfigured, httpJSON(t, rec)["code"])
	require.Empty(t, f.grants())
}

// revokeFails is a mesh whose Revoke fails for the listed consumers.
type revokeFails struct {
	*fakeMesh
	consumers map[uuid.UUID]bool
}

func (r *revokeFails) Revoke(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	if r.consumers[refID] {
		return errors.New("tailnet unreachable")
	}
	return r.fakeMesh.Revoke(ctx, tx, linkID, exposureID, refID, source)
}

func TestRepairAllRepairsEveryEnvironmentAndJoinsTheFailures(t *testing.T) {
	f := newFx(t)
	// Environment one (the fixture's) and a second one, each with a reference grant.
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api1 := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api1.ID))

	first := f.env
	staging, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	f.env = staging
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api2 := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api2.ID))
	require.Len(t, f.grants(), 1)
	f.env = first
	require.Len(t, f.grants(), 1)

	// Both consumers drop their references: the grace starts, then runs out.
	for _, api := range []db.Service{api1, api2} {
		f.delVar(api, "DB_HOST")
		f.delVar(api, "DB_PORT")
	}
	ctx := context.Background()
	require.NoError(t, f.s.RepairAll(ctx))
	f.clock = f.clock.Add(ReferenceGrace)

	// Removing the first environment's grant fails; the second one is still repaired.
	good := f.s.mesh
	f.s.mesh = &revokeFails{fakeMesh: f.fm, consumers: map[uuid.UUID]bool{api1.ID: true}}
	err = f.s.RepairAll(ctx)
	require.ErrorContains(t, err, "tailnet unreachable")
	require.Len(t, f.grants(), 1, "the failing environment keeps its grant")
	f.env = staging
	require.Empty(t, f.grants(), "the other environment was repaired regardless")
	f.env = first

	// Once the mesh works again the next run finishes the job.
	f.s.mesh = good
	require.NoError(t, f.s.RepairAll(ctx))
	require.Empty(t, f.grants())
}

func TestReconcileJob(t *testing.T) {
	require.Equal(t, "mesh_links_reconcile", ReconcileArgs{}.Kind())

	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	f.delVar(api, "DB_HOST")
	f.delVar(api, "DB_PORT")
	require.NoError(t, (&reconcileWorker{s: f.s}).Work(context.Background(), nil))
	require.Len(t, f.grants(), 1, "the grace has only started")
	f.clock = f.clock.Add(ReferenceGrace)
	require.NoError(t, (&reconcileWorker{s: f.s}).Work(context.Background(), nil))
	require.Empty(t, f.grants(), "the periodic job removes grants whose source is gone")

	// The registered worker is reachable through River (it refuses kinds without one).
	jm := jobs.New(f.pool, nil)
	RegisterJobs(jm, f.s)
	require.NoError(t, jm.Start(context.Background()))
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jm.Stop(sctx)
	})
	require.NoError(t, jm.Insert(context.Background(), ReconcileArgs{}, nil))
	require.Eventually(t, func() bool {
		var n int
		err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'mesh_links_reconcile' AND state = 'completed'").Scan(&n)
		return err == nil && n == 1
	}, 20*time.Second, 50*time.Millisecond)
}
