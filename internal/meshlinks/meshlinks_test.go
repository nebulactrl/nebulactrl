package meshlinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeMesh is a mesh.Provider over the real mesh tables (no tailnet): it
// allocates ports the way the real provider does and cascades like it.
type fakeMesh struct {
	q       *db.Queries
	blocked map[uuid.UUID]mesh.Blocker
	ready   map[uuid.UUID]mesh.Readiness
	check   string
}

var _ mesh.Provider = (*fakeMesh)(nil)

func (f *fakeMesh) PlanLink(ctx context.Context, q *db.Queries, env, c, p uuid.UUID, purpose string) (mesh.LinkPlan, error) {
	lp := mesh.LinkPlan{Blockers: []mesh.Blocker{}, Warnings: []string{}}
	_, err := q.GetMeshLinkByKey(ctx, db.GetMeshLinkByKeyParams{EnvironmentID: env, ServerID: c, RemoteServerID: p, Purpose: purpose})
	lp.LinkExists = err == nil
	if !lp.LinkExists {
		lp.DevicesAdded = 1
	}
	for _, s := range []uuid.UUID{c, p} {
		if b, ok := f.blocked[s]; ok {
			lp.Blockers = append(lp.Blockers, b)
		}
	}
	return lp, nil
}

func (f *fakeMesh) EnsureExposure(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, purpose string, port int) (mesh.Exposure, error) {
	q := db.New(tx)
	sc, err := q.GetMeshServiceScope(ctx, serviceID)
	if err != nil {
		return mesh.Exposure{}, err
	}
	ex, err := q.GetMeshExposureByService(ctx, db.GetMeshExposureByServiceParams{ServiceID: serviceID, Purpose: purpose})
	if err == nil {
		return toExposure(ex), nil
	}
	used, err := q.ListMeshExposuresUsedPorts(ctx, *sc.ServerID)
	if err != nil {
		return mesh.Exposure{}, err
	}
	taken := map[int32]bool{}
	for _, u := range used {
		taken[u] = true
	}
	meshPort := int32(port)
	for p := int32(15000); taken[meshPort]; p++ {
		meshPort = p
	}
	ex, err = q.InsertMeshExposure(ctx, db.InsertMeshExposureParams{
		WorkspaceID: sc.WorkspaceID, ServerID: *sc.ServerID, ServiceID: serviceID, EnvironmentID: sc.EnvironmentID,
		Purpose: purpose, ContainerPort: int32(port), MeshPort: meshPort,
	})
	return toExposure(ex), err
}

func toExposure(e db.MeshExposure) mesh.Exposure {
	return mesh.Exposure{ID: e.ID, ServerID: e.ServerID, ServiceID: e.ServiceID, EnvironmentID: e.EnvironmentID, Purpose: e.Purpose, ContainerPort: int(e.ContainerPort), MeshPort: int(e.MeshPort)}
}

func (f *fakeMesh) EnsureLink(ctx context.Context, tx pgx.Tx, env, local, remote uuid.UUID, purpose string) (mesh.Link, error) {
	q := db.New(tx)
	e, err := q.GetMeshEnvironment(ctx, env)
	if err != nil {
		return mesh.Link{}, err
	}
	l, err := q.InsertMeshLink(ctx, db.InsertMeshLinkParams{
		WorkspaceID: e.WorkspaceID, EnvironmentID: env, ServerID: local, RemoteServerID: remote, Purpose: purpose,
		Hostname: "nc-test", IdentityHash: "x",
	})
	return mesh.Link{ID: l.ID, EnvironmentID: env, ServerID: local, RemoteServerID: remote, Purpose: purpose, State: l.State}, err
}

func (f *fakeMesh) Grant(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	return db.New(tx).InsertMeshGrant(ctx, db.InsertMeshGrantParams{LinkID: linkID, ExposureID: exposureID, RefID: refID, Source: source})
}

func (f *fakeMesh) cascade(ctx context.Context, q *db.Queries, linkID, exposureID uuid.UUID) error {
	if n, err := q.CountMeshGrantsOfLink(ctx, linkID); err != nil || n == 0 {
		if err != nil {
			return err
		}
		if _, err := q.DeleteMeshLink(ctx, linkID); err != nil {
			return err
		}
	}
	if n, err := q.CountMeshGrantsOfExposure(ctx, exposureID); err != nil || n == 0 {
		if err != nil {
			return err
		}
		if _, err := q.DeleteMeshExposure(ctx, exposureID); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeMesh) Revoke(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error {
	q := db.New(tx)
	if _, err := q.DeleteMeshGrant(ctx, db.DeleteMeshGrantParams{LinkID: linkID, ExposureID: exposureID, RefID: refID, Source: source}); err != nil {
		return err
	}
	return f.cascade(ctx, q, linkID, exposureID)
}

func (f *fakeMesh) Release(ctx context.Context, tx pgx.Tx, refID uuid.UUID) error {
	q := db.New(tx)
	rows, err := q.DeleteMeshGrantsOfRef(ctx, refID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := f.cascade(ctx, q, r.LinkID, r.ExposureID); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeMesh) Ready(_ context.Context, linkID uuid.UUID) (mesh.Readiness, error) {
	if r, ok := f.ready[linkID]; ok {
		return r, nil
	}
	return mesh.Readiness{Online: true, ACLOK: true}, nil
}

func (f *fakeMesh) Check(context.Context, uuid.UUID, uuid.UUID) (mesh.CheckResult, error) {
	if f.check == "" {
		return mesh.CheckResult{Result: agentprotocol.MeshCheckConnected}, nil
	}
	return mesh.CheckResult{Result: f.check, Message: "no route"}, nil
}

func (f *fakeMesh) RouteOf(context.Context, *db.Queries, uuid.UUID, uuid.UUID) (mesh.Route, bool, error) {
	return mesh.Route{}, false, nil
}

type fx struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	box     *secretbox.Box
	fm      *fakeMesh
	s       *Service
	ws      db.Workspace
	project db.Project
	env     db.Environment
	a, b, c db.Server
	ctx     context.Context
	clock   time.Time
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "ws")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "u@example.com", Name: "u", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	f := &fx{t: t, pool: pool, q: q, box: box, ws: ws, project: prj, env: env, clock: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	for i, dst := range []*db.Server{&f.a, &f.b, &f.c} {
		srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: fmt.Sprintf("srv-%c", 'a'+i)})
		require.NoError(t, err)
		_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
		require.NoError(t, err)
		*dst = srv
	}
	f.fm = &fakeMesh{q: q, blocked: map[uuid.UUID]mesh.Blocker{}, ready: map[uuid.UUID]mesh.Readiness{}}
	f.s = NewService(pool, box, events.NewBus(), f.fm, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.s.now = func() time.Time { return f.clock }
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "u", Scopes: policy.AllScopes})
	return f
}

// service inserts an app (or another kind) with its variables.
func (f *fx) service(slug, kind string, server *db.Server, port int, vars map[string]string) db.Service {
	f.t.Helper()
	cfg := services.Config{Image: "x:1", Port: port}
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	var sid *uuid.UUID
	if server != nil {
		sid = &server.ID
	}
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: services.SourceImage, Config: raw, ServerID: sid,
	})
	require.NoError(f.t, err)
	for k, v := range vars {
		f.setVar(sv, k, v)
	}
	return sv
}

func (f *fx) setVar(sv db.Service, key, value string) {
	f.t.Helper()
	enc, err := f.box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(sv.ID.String(), key), value)
	require.NoError(f.t, err)
	_, err = f.q.UpsertServiceVariable(context.Background(), db.UpsertServiceVariableParams{ServiceID: sv.ID, Key: key, ValueEnc: enc})
	require.NoError(f.t, err)
}

func (f *fx) delVar(sv db.Service, key string) {
	f.t.Helper()
	_, err := f.q.DeleteServiceVariable(context.Background(), db.DeleteServiceVariableParams{ServiceID: sv.ID, Key: key})
	require.NoError(f.t, err)
}

func (f *fx) move(sv db.Service, server *db.Server) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET server_id = $2 WHERE id = $1", sv.ID, server.ID)
	require.NoError(f.t, err)
}

func (f *fx) generation(sv db.Service) int64 {
	f.t.Helper()
	var g int64
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT generation FROM services WHERE id = $1", sv.ID).Scan(&g))
	return g
}

func (f *fx) reconcile(mode Mode, opts Options) Result {
	f.t.Helper()
	res, err := f.s.ReconcileEnv(context.Background(), f.env.ID, mode, opts)
	require.NoError(f.t, err)
	return res
}

type grantRow struct{ consumer, provider, source string }

func (f *fx) grants() []grantRow {
	f.t.Helper()
	gs, err := f.q.ListMeshGrantsOfEnvironment(context.Background(), f.env.ID)
	require.NoError(f.t, err)
	var out []grantRow
	for _, g := range gs {
		ex, err := f.q.GetMeshExposureByID(context.Background(), g.ExposureID)
		require.NoError(f.t, err)
		c, err := f.q.GetMeshServiceScope(context.Background(), g.RefID)
		require.NoError(f.t, err)
		p, err := f.q.GetMeshServiceScope(context.Background(), ex.ServiceID)
		require.NoError(f.t, err)
		out = append(out, grantRow{c.Slug, p.Slug, g.Source})
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]) < fmt.Sprint(out[j]) })
	return out
}

func (f *fx) counts() (links, exposures int) {
	f.t.Helper()
	l, err := f.q.ListMeshLinksOfEnvironment(context.Background(), f.env.ID)
	require.NoError(f.t, err)
	e, err := f.q.ListMeshExposuresOfEnvironment(context.Background(), f.env.ID)
	require.NoError(f.t, err)
	return len(l), len(e)
}

func (f *fx) edge(from, to db.Service) {
	f.t.Helper()
	_, err := f.q.InsertLink(context.Background(), db.InsertLinkParams{EnvironmentID: f.env.ID, FromServiceID: from.ID, ToServiceID: to.ID})
	require.NoError(f.t, err)
}

func (f *fx) tx(fn func(tx pgx.Tx) error) {
	f.t.Helper()
	require.NoError(f.t, db.InTx(context.Background(), f.pool, fn))
}

var refVars = map[string]string{
	"DB_HOST": "${{postgres.NEBULA_PRIVATE_HOST}}",
	"DB_PORT": "${{postgres.PORT}}",
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, code, ae.Code)
}

// ── Derive ──

func TestDeriveFromReferencesEdgesAndLocalServices(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.service("local", services.KindApp, &f.a, 9000, map[string]string{"X": "${{api.PORT}}"}) // same server: no want
	cache := f.service("cache", services.KindApp, &f.b, 6379, nil)
	f.service("unplaced", services.KindApp, nil, 7000, map[string]string{"X": "${{postgres.PORT}}"}) // no server: no want
	f.edge(api, cache)

	d, err := Derive(context.Background(), f.q, f.box, mustEnv(t, f))
	require.NoError(t, err)
	require.True(t, d.MultiServer)
	require.Len(t, d.Wants, 2)
	w0, w1 := d.Wants[0], d.Wants[1]
	require.Equal(t, [4]string{"api", "cache", SourceEdge, ""}, [4]string{w0.ConsumerSlug, w0.ProviderSlug, w0.Source, ""})
	require.Equal(t, [3]string{"api", "postgres", SourceReference}, [3]string{w1.ConsumerSlug, w1.ProviderSlug, w1.Source})
	require.Equal(t, []string{"DB_HOST", "DB_PORT"}, w1.Keys)
	require.Equal(t, f.a.ID, w1.ConsumerServer)
	require.Equal(t, f.b.ID, w1.ProviderServer)
	require.Equal(t, 5432, w1.ProviderPort)
	require.Equal(t, pg.ID, w1.ProviderID)
	require.True(t, d.Edges[pair{api.ID, cache.ID}])
}

func mustEnv(t *testing.T, f *fx) services.EnvInfo {
	t.Helper()
	env, err := services.LoadEnvInfo(context.Background(), f.q, f.env.ID)
	require.NoError(t, err)
	return env
}

func TestDeriveSingleServerEnvironmentNeedsNothing(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.a, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.edge(api, f.service("cache", services.KindApp, &f.a, 6379, nil))
	d, err := Derive(context.Background(), f.q, f.box, mustEnv(t, f))
	require.NoError(t, err)
	require.False(t, d.MultiServer)
	require.Empty(t, d.Wants)
}

func TestDeriveMarksProvidersThatCannotBeExposed(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 0, nil) // no TCP port
	f.service("gateway", services.KindRoute, &f.b, 0, nil)
	f.service("stack", services.KindStack, &f.b, 0, nil)
	f.service("api", services.KindApp, &f.a, 8080, map[string]string{
		"A": "${{postgres.NEBULA_PRIVATE_HOST}}", "B": "${{gateway.NEBULA_PRIVATE_HOST}}", "C": "${{stack.NEBULA_PRIVATE_HOST}}",
	})
	d, err := Derive(context.Background(), f.q, f.box, mustEnv(t, f))
	require.NoError(t, err)
	require.Len(t, d.Wants, 3)
	for _, w := range d.Wants {
		require.NotEmpty(t, w.NotExposable, w.ProviderSlug)
	}
	plan, err := f.s.PlanWants(context.Background(), f.q, d.Env, d.Wants)
	require.NoError(t, err)
	require.True(t, plan.CrossServer)
	require.Zero(t, plan.DevicesAdded)
	require.Len(t, plan.Blockers, 3)
	for _, b := range plan.Blockers {
		require.Equal(t, apierr.CodeMeshNotExposable, b.Code)
	}
	res := f.reconcile(ModeAdd, ForConsumers(d.Services[d.Wants[0].ConsumerID].ID))
	require.Len(t, res.Blockers, 3)
	require.Empty(t, f.grants())
}

func TestDeriveFollowsGroupExportTargets(t *testing.T) {
	databases.RegisterExports()
	t.Cleanup(func() { services.RegisterExportProvider(nil) })
	f := newFx(t)
	dbCfg := `{"image":"postgres:17","port":5432,"healthcheck":{"type":"tcp"},"database":{"engine":"postgres","version":"17","databaseName":"app","username":"postgres"}}`
	mkDB := func(slug string, server *db.Server) db.Service {
		sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindDatabase, SourceType: services.SourceImage,
			Config: json.RawMessage(dbCfg), ServerID: &server.ID,
		})
		require.NoError(t, err)
		for k, v := range map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_DB": "app", "POSTGRES_PASSWORD": "pw-canary"} {
			f.setVar(sv, k, v)
		}
		return sv
	}
	primary := mkDB("pg", &f.b)
	replica := mkDB("pg-r", &f.c)
	_, err := f.q.InsertDatabaseReplica(context.Background(), db.InsertDatabaseReplicaParams{
		ServiceID: replica.ID, WorkspaceID: f.ws.ID, EnvironmentID: f.env.ID, PrimaryServiceID: primary.ID, SlotName: "nc_r_x", ReplUser: "nebula_r_x",
	})
	require.NoError(t, err)
	require.NoError(t, f.q.SetDatabaseReplicaState(context.Background(), db.SetDatabaseReplicaStateParams{ServiceID: replica.ID, State: "streaming"}))
	f.service("reader", services.KindApp, &f.a, 8080, map[string]string{
		"READ": "${{pg.DATABASE_READ_URL}}", "WRITE": "${{pg.DATABASE_WRITE_URL}}",
	})
	f.service("local", services.KindApp, &f.c, 8080, map[string]string{"READ": "${{pg.DATABASE_READ_URL}}"})

	d, err := Derive(context.Background(), f.q, f.box, mustEnv(t, f))
	require.NoError(t, err)
	got := map[string][]string{}
	for _, w := range d.Wants {
		got[w.ConsumerSlug] = append(got[w.ConsumerSlug], w.ProviderSlug)
	}
	require.Equal(t, []string{"pg", "pg-r"}, got["reader"], "writes reach the primary, reads the replica on another server")
	require.Empty(t, got["local"], "the replica is on the consumer's own server: nothing crosses servers")
}

// ── Reconcile ──

func TestReconcileAddRunsOnlyForSelectedConsumers(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	worker := f.service("worker", services.KindApp, &f.a, 8081, refVars)

	res := f.reconcile(ModeAdd, Options{})
	require.Empty(t, res.Added, "nothing is selected: a variable edit alone never creates a device")
	links, exposures := f.counts()
	require.Zero(t, links+exposures)

	before := f.generation(worker)
	res = f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Equal(t, 1, res.DevicesAdded)
	require.Equal(t, []grantRow{{"api", "postgres", SourceReference}}, f.grants())
	links, exposures = f.counts()
	require.Equal(t, 1, links)
	require.Equal(t, 1, exposures)
	require.Equal(t, []uuid.UUID{worker.ID}, res.Restaged, "the other consumer resolves a new port: it is restaged, not deployed")
	require.Equal(t, before+1, f.generation(worker))

	// Idempotent; the second consumer joins the same link (no new device).
	res = f.reconcile(ModeAdd, ForConsumers(api.ID, worker.ID))
	require.Zero(t, res.DevicesAdded)
	require.Equal(t, []grantRow{{"api", "postgres", SourceReference}, {"worker", "postgres", SourceReference}}, f.grants())
	links, _ = f.counts()
	require.Equal(t, 1, links)
}

func TestReconcileAddKeepsContainerPortWhenFree(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	other := f.service("other", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, map[string]string{"A": "${{postgres.PORT}}", "B": "${{other.PORT}}"})
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	ports := map[uuid.UUID]int32{}
	for _, s := range []db.Service{pg, other} {
		ex, err := f.q.GetMeshExposureByService(context.Background(), db.GetMeshExposureByServiceParams{ServiceID: s.ID, Purpose: purposeService})
		require.NoError(t, err)
		ports[s.ID] = ex.MeshPort
	}
	require.Equal(t, int32(5432), ports[pg.ID]+ports[other.ID]-15000, "one keeps the container port, the other takes the first free of 15000-15999")
	res, err := services.LoadResolver(context.Background(), f.q, f.box, mustEnv(t, f))
	require.NoError(t, err)
	got, err := res.ResolveService("api")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"5432", "15000"}, []string{got.Vars["A"], got.Vars["B"]}, "the variables follow the allocated ports")
	for _, u := range got.MeshUses {
		require.False(t, u.Pending)
	}
}

func TestEdgeCreatesTheGrantAndRepairRemovesItAtOnce(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, nil)
	require.Empty(t, f.grants())

	f.edge(api, pg)
	f.tx(func(tx pgx.Tx) error { return f.s.EdgeCreated(context.Background(), tx, f.env.ID, api.ID, pg.ID) })
	require.Equal(t, []grantRow{{"api", "postgres", SourceEdge}}, f.grants())

	// The edge row disappears without EdgeDeleted (a crash): repair removes the grant at once, no grace.
	_, err := f.pool.Exec(context.Background(), "DELETE FROM service_links WHERE from_service_id = $1", api.ID)
	require.NoError(t, err)
	res := f.reconcile(ModeRepair, Options{})
	require.Len(t, res.Removed, 1)
	require.Equal(t, SourceEdge, res.Removed[0].Source)
	require.Empty(t, f.grants())
	links, exposures := f.counts()
	require.Zero(t, links+exposures, "the link and the exposure go with their last grant")
}

func TestEdgeDeletedRevokesOnlyTheEdgeGrant(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.edge(api, pg)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Equal(t, []grantRow{{"api", "postgres", SourceEdge}, {"api", "postgres", SourceReference}}, f.grants())
	f.tx(func(tx pgx.Tx) error { return f.s.EdgeDeleted(context.Background(), tx, f.env.ID, api.ID, pg.ID) })
	require.Equal(t, []grantRow{{"api", "postgres", SourceReference}}, f.grants(), "the reference still asks for the route")
}

func TestEdgeWithBlockersOpensNothing(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, nil)
	f.fm.blocked[f.a.ID] = mesh.Blocker{Code: apierr.CodeMeshServerRequired, Message: "enable the mesh on srv-a first"}
	f.edge(api, pg)
	plan, err := f.s.PlanEdge(context.Background(), f.env.ID, api.ID, pg.ID)
	require.NoError(t, err)
	require.True(t, plan.CrossServer)
	require.Equal(t, 1, plan.DevicesAdded)
	require.Equal(t, apierr.CodeMeshServerRequired, plan.Blockers[0].Code)
	f.tx(func(tx pgx.Tx) error { return f.s.EdgeCreated(context.Background(), tx, f.env.ID, api.ID, pg.ID) })
	require.Empty(t, f.grants())

	same := f.service("same", services.KindApp, &f.a, 1, nil)
	plan, err = f.s.PlanEdge(context.Background(), f.env.ID, api.ID, same.ID)
	require.NoError(t, err)
	require.False(t, plan.CrossServer, "services of one server need no mesh")
	require.Zero(t, plan.DevicesAdded)
}

func TestRepairRemovesReferenceGrantsAfterGraceOnly(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Len(t, f.grants(), 1)

	// Still referenced: repair keeps it, however long it waits.
	f.clock = f.clock.Add(time.Hour)
	res := f.reconcile(ModeRepair, Options{})
	require.Empty(t, res.Removed)
	require.Zero(t, res.Waiting)

	f.delVar(api, "DB_HOST")
	f.delVar(api, "DB_PORT")
	res = f.reconcile(ModeRepair, Options{})
	require.Empty(t, res.Removed, "the reference is gone, the grace starts")
	require.Equal(t, 1, res.Waiting)

	f.clock = f.clock.Add(ReferenceGrace - time.Second)
	res = f.reconcile(ModeRepair, Options{})
	require.Empty(t, res.Removed)
	require.Equal(t, 1, res.Waiting, "9 minutes 59 seconds: still within the grace")

	// The reference comes back: the clock resets.
	f.setVar(api, "DB_PORT", "${{postgres.PORT}}")
	res = f.reconcile(ModeRepair, Options{})
	require.Empty(t, res.Removed)
	require.Zero(t, res.Waiting)
	f.delVar(api, "DB_PORT")
	f.reconcile(ModeRepair, Options{})
	f.clock = f.clock.Add(ReferenceGrace)
	res = f.reconcile(ModeRepair, Options{})
	require.Len(t, res.Removed, 1)
	require.Empty(t, f.grants())
	links, exposures := f.counts()
	require.Zero(t, links+exposures)
}

func TestRepairNeverAddsALinkButRebuildsRowsOfExistingOnes(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)

	res := f.reconcile(ModeRepair, Options{})
	require.Empty(t, res.Added)
	require.Zero(t, res.DevicesAdded)
	links, exposures := f.counts()
	require.Zero(t, links+exposures, "a variable reference alone never creates a device")

	f.reconcile(ModeAdd, ForConsumers(api.ID))
	// A crash lost the grant and the exposure but the link (a confirmed pair) is still there.
	_, err := f.pool.Exec(context.Background(), "DELETE FROM mesh_grants")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "DELETE FROM mesh_exposures WHERE service_id = $1", pg.ID)
	require.NoError(t, err)
	res = f.reconcile(ModeRepair, Options{})
	require.Len(t, res.Added, 1)
	require.Zero(t, res.DevicesAdded)
	require.Equal(t, []grantRow{{"api", "postgres", SourceReference}}, f.grants())

	// The link itself is gone: repair does not create another one.
	_, err = f.pool.Exec(context.Background(), "DELETE FROM mesh_links")
	require.NoError(t, err)
	res = f.reconcile(ModeRepair, Options{})
	require.Empty(t, res.Added)
	links, _ = f.counts()
	require.Zero(t, links)
}

func TestRepairKeepsReplicaGrantsOfExistingReplicasOnly(t *testing.T) {
	f := newFx(t)
	primary := f.service("pg", services.KindApp, &f.b, 5432, nil)
	replica := f.service("pg-r", services.KindApp, &f.a, 5432, nil)
	_, err := f.q.InsertDatabaseReplica(context.Background(), db.InsertDatabaseReplicaParams{
		ServiceID: replica.ID, WorkspaceID: f.ws.ID, EnvironmentID: f.env.ID, PrimaryServiceID: primary.ID, SlotName: "s", ReplUser: "u",
	})
	require.NoError(t, err)
	f.tx(func(tx pgx.Tx) error {
		ex, err := f.fm.EnsureExposure(context.Background(), tx, primary.ID, "replication", 5432)
		require.NoError(t, err)
		l, err := f.fm.EnsureLink(context.Background(), tx, f.env.ID, f.a.ID, f.b.ID, "replication")
		require.NoError(t, err)
		return f.fm.Grant(context.Background(), tx, l.ID, ex.ID, replica.ID, SourceReplica)
	})
	require.Len(t, f.grants(), 1)

	f.clock = f.clock.Add(time.Hour)
	require.Empty(t, f.reconcile(ModeRepair, Options{}).Removed, "the replica exists: its grant stays")

	_, err = f.q.DeleteDatabaseReplica(context.Background(), replica.ID)
	require.NoError(t, err)
	require.Equal(t, 1, f.reconcile(ModeRepair, Options{}).Waiting)
	f.clock = f.clock.Add(ReferenceGrace)
	require.Len(t, f.reconcile(ModeRepair, Options{}).Removed, 1)
	require.Empty(t, f.grants())
}

func TestMovedProviderDropsItsGrantsRestagesAndIsReAddedByItsDeploy(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Len(t, f.grants(), 1)

	f.move(pg, &f.c)
	gen := f.generation(api)
	res := f.reconcile(ModeRepair, Options{})
	require.Len(t, res.Removed, 1, "the exposure sits on the old server: its grants go at once")
	require.Equal(t, []uuid.UUID{api.ID}, res.Restaged)
	require.Equal(t, gen+1, f.generation(api))
	require.Empty(t, f.grants())
	links, _ := f.counts()
	require.Zero(t, links, "repair never adds the link A -> C")

	// The deploy of the moved provider re-ensures the route for the consumers that had one.
	f.move(pg, &f.b)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	f.move(pg, &f.c)
	res = f.reconcile(ModeAdd, ForConsumers(pg.ID))
	require.Equal(t, []grantRow{{"api", "postgres", SourceReference}}, f.grants())
	require.Equal(t, 1, res.DevicesAdded)
	ex, err := f.q.GetMeshExposureByService(context.Background(), db.GetMeshExposureByServiceParams{ServiceID: pg.ID, Purpose: purposeService})
	require.NoError(t, err)
	require.Equal(t, f.c.ID, ex.ServerID)
}

func TestServiceDeletedDropsConsumerGrantsAndProviderExposures(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	cache := f.service("cache", services.KindApp, &f.b, 6379, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, map[string]string{
		"A": "${{postgres.PORT}}", "B": "${{cache.PORT}}",
	})
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Len(t, f.grants(), 2)

	f.tx(func(tx pgx.Tx) error { return f.s.ServiceDeleted(context.Background(), tx, f.env.ID, pg.ID) })
	require.Equal(t, []grantRow{{"api", "cache", SourceReference}}, f.grants())
	f.tx(func(tx pgx.Tx) error { return f.s.ServiceDeleted(context.Background(), tx, f.env.ID, api.ID) })
	require.Empty(t, f.grants())
	links, exposures := f.counts()
	require.Zero(t, links+exposures)
	_ = cache
}

// ── PlanCheck, Prepare, WaitReady, Alias ──

func TestPlanCheckListsLinksDevicesAndBlockers(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	f.service("cache", services.KindApp, &f.c, 6379, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, map[string]string{
		"A": "${{postgres.PORT}}", "B": "${{cache.PORT}}", "C": "${{postgres.NEBULA_PRIVATE_HOST}}",
	})
	sc, err := services.LoadScopeInternal(context.Background(), f.q, api.ID)
	require.NoError(t, err)
	plan, err := f.s.PlanCheck(context.Background(), f.q, sc)
	require.NoError(t, err)
	require.True(t, plan.CrossServer)
	require.Equal(t, 2, plan.DevicesAdded)
	require.Len(t, plan.Links, 2)
	require.Equal(t, "srv-a", plan.Links[0].FromServerName)
	require.Equal(t, "srv-b", plan.Links[0].ToServerName)
	require.Equal(t, []string{"postgres"}, plan.Links[0].Providers)
	require.Equal(t, "new", plan.Links[0].State)
	require.Empty(t, plan.Blockers)

	f.fm.blocked[f.c.ID] = mesh.Blocker{Code: apierr.CodeMeshServerRequired, Message: "enable the mesh on srv-c first"}
	plan, err = f.s.PlanCheck(context.Background(), f.q, sc)
	require.NoError(t, err)
	require.Len(t, plan.Blockers, 1)

	// The service that reaches nothing remote has an empty plan.
	pgSc, err := services.LoadScopeInternal(context.Background(), f.q, mustService(t, f, "postgres").ID)
	require.NoError(t, err)
	plan, err = f.s.PlanCheck(context.Background(), f.q, pgSc)
	require.NoError(t, err)
	require.False(t, plan.CrossServer)
	require.Empty(t, plan.Links)
}

func mustService(t *testing.T, f *fx, slug string) db.Service {
	t.Helper()
	svcs, err := f.q.ListAllEnvironmentServices(context.Background(), f.env.ID)
	require.NoError(t, err)
	for _, s := range svcs {
		if s.Slug == slug {
			return s
		}
	}
	t.Fatalf("no service %s", slug)
	return db.Service{}
}

func TestPrepareCreatesTheRouteAndReportsBlockers(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)

	f.fm.blocked[f.b.ID] = mesh.Blocker{Code: apierr.CodeMeshServerRequired, Message: "enable the mesh on srv-b first"}
	err := f.s.Prepare(context.Background(), api.ID, "manual")
	requireCode(t, err, apierr.CodeMeshServerRequired)
	ae, _ := apierr.As(err)
	require.Equal(t, "Not deployed: enable the mesh on srv-b first. No changes were made.", ae.Message)
	require.Empty(t, f.grants())

	f.fm.blocked[f.b.ID] = mesh.Blocker{Code: apierr.CodeMeshPaused, Message: "the mesh is paused: Headscale is being migrated"}
	ae, _ = apierr.As(f.s.Prepare(context.Background(), api.ID, "manual"))
	require.Equal(t, "Not deployed: mesh paused. No changes were made.", ae.Message)

	delete(f.fm.blocked, f.b.ID)
	require.NoError(t, f.s.Prepare(context.Background(), api.ID, TriggerReconcileForTest), "an automated redeploy adds nothing")
	require.Empty(t, f.grants())
	require.NoError(t, f.s.Prepare(context.Background(), api.ID, "manual"))
	require.Equal(t, []grantRow{{"api", "postgres", SourceReference}}, f.grants())
}

// TriggerReconcileForTest is the trigger of the reconciler's redeploys.
const TriggerReconcileForTest = "reconcile"

func TestWaitReadyWaitsThenTimesOutWithTheExactMessage(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	start := f.clock

	waiting, err := f.s.WaitReady(context.Background(), api.ID, start)
	require.NoError(t, err)
	require.Contains(t, waiting, "Waiting for the mesh link srv-a -> srv-b", "no link yet")

	require.NoError(t, f.s.Prepare(context.Background(), api.ID, "manual"))
	waiting, err = f.s.WaitReady(context.Background(), api.ID, start)
	require.NoError(t, err)
	require.Empty(t, waiting, "online and the access rules applied")

	links, err := f.q.ListMeshLinksOfEnvironment(context.Background(), f.env.ID)
	require.NoError(t, err)
	f.fm.ready[links[0].ID] = mesh.Readiness{Online: false, Reason: "node is joining"}
	waiting, err = f.s.WaitReady(context.Background(), api.ID, start)
	require.NoError(t, err)
	require.Equal(t, "Waiting for the mesh link srv-a -> srv-b to come online: node is joining.", waiting)

	f.clock = start.Add(ReadyTimeout - time.Second)
	waiting, err = f.s.WaitReady(context.Background(), api.ID, start)
	require.NoError(t, err)
	require.NotEmpty(t, waiting)

	f.clock = start.Add(ReadyTimeout)
	_, err = f.s.WaitReady(context.Background(), api.ID, start)
	requireCode(t, err, apierr.CodeMeshLinkNotReady)
	ae, _ := apierr.As(err)
	require.Equal(t, "Not deployed: mesh link not ready. No changes were made.", ae.Message)

	f.fm.ready[links[0].ID] = mesh.Readiness{Online: true, ACLOK: false}
	waiting, err = f.s.WaitReady(context.Background(), api.ID, f.clock)
	require.NoError(t, err)
	require.Contains(t, waiting, "access rules")
}

func TestWaitReadyUnderAnAdvisoryPolicyNeedsAConnection(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	_, err := f.pool.Exec(context.Background(), "INSERT INTO mesh_settings (workspace_id, acl_state) VALUES ($1, 'advisory')", f.ws.ID)
	require.NoError(t, err)
	require.NoError(t, f.s.Prepare(context.Background(), api.ID, "manual"))

	f.fm.check = agentprotocol.MeshCheckTimeout
	waiting, err := f.s.WaitReady(context.Background(), api.ID, f.clock)
	require.NoError(t, err)
	require.Contains(t, waiting, "Waiting for a connection srv-a -> srv-b to postgres (timeout)")

	f.fm.check = ""
	waiting, err = f.s.WaitReady(context.Background(), api.ID, f.clock)
	require.NoError(t, err)
	require.Empty(t, waiting)
}

func TestAliasOfAnExposedService(t *testing.T) {
	f := newFx(t)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	alias, err := f.s.Alias(context.Background(), f.q, pg.ID)
	require.NoError(t, err)
	require.Empty(t, alias)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	alias, err = f.s.Alias(context.Background(), f.q, pg.ID)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.MeshServiceAlias(pg.ID.String()), alias)
	require.Equal(t, "nc-svc-"+agentprotocol.Short(pg.ID.String()), alias)
}

// ── views and the reconcile endpoint ──

func TestReconcileNowNeedsConfirmationForDevices(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	f.service("api", services.KindApp, &f.a, 8080, refVars)

	_, err := f.s.ReconcileNow(f.ctx, f.env.ID, ModeAdd, false)
	requireCode(t, err, apierr.CodeMeshConfirmationRequired)
	require.Empty(t, f.grants())

	v, err := f.s.ReconcileNow(f.ctx, f.env.ID, ModeAdd, true)
	require.NoError(t, err)
	require.Equal(t, 1, v.DevicesAdded)
	require.Equal(t, []GrantChange{{Consumer: "api", Provider: "postgres", Source: SourceReference}}, v.Added)

	view, err := f.s.EnvironmentMesh(f.ctx, f.env.ID)
	require.NoError(t, err)
	require.Len(t, view.Links, 1)
	require.Equal(t, "srv-a", view.Links[0].From.Name)
	require.Equal(t, "srv-b", view.Links[0].To.Name)
	require.Len(t, view.Links[0].Grants, 1)
	require.Len(t, view.Routes, 1)
	require.Equal(t, 5432, view.Routes[0].Port)
	require.Equal(t, "provisioning", view.Routes[0].State, "a granted route reports its link's state")

	pg := mustService(t, f, "postgres")
	sv, err := f.s.ServiceMesh(f.ctx, pg.ID)
	require.NoError(t, err)
	require.Len(t, sv.Exposures, 1)
	require.Equal(t, "nc-svc-"+agentprotocol.Short(pg.ID.String()), sv.Exposures[0].Alias)
	require.Len(t, sv.Consumers, 1)
	require.Equal(t, "api", sv.Consumers[0].Slug)
	api := mustService(t, f, "api")
	sv, err = f.s.ServiceMesh(f.ctx, api.ID)
	require.NoError(t, err)
	require.Len(t, sv.Routes, 1)
	require.Equal(t, "postgres", sv.Routes[0].Alias)
	require.Equal(t, []string{"DB_HOST", "DB_PORT"}, sv.Routes[0].Keys)

	// A token without servers:write cannot reconcile; a repair needs no confirmation.
	ro := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ro", Scopes: []string{policy.ScopeProjectsRead}})
	_, err = f.s.ReconcileNow(ro, f.env.ID, ModeRepair, false)
	requireCode(t, err, "forbidden")
	v, err = f.s.ReconcileNow(f.ctx, f.env.ID, ModeRepair, false)
	require.NoError(t, err)
	require.Empty(t, v.Removed)
}

func TestWithoutAProviderEveryCrossServerNeedIsABlocker(t *testing.T) {
	f := newFx(t)
	f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.s.mesh = nil
	err := f.s.Prepare(context.Background(), api.ID, "manual")
	requireCode(t, err, apierr.CodeMeshNotConfigured)
	require.NoError(t, f.s.RepairAll(context.Background()))
	// Reads still work.
	_, err = f.s.EnvironmentMesh(f.ctx, f.env.ID)
	require.NoError(t, err)
}
