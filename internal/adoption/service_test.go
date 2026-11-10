package adoption

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/reconciler"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeHub answers workload_inventory with a fixed inventory.
type fakeHub struct {
	mu      sync.Mutex
	inv     agentprotocol.WorkloadInventory
	outcome string
	err     error
	resyncs int
}

func (h *fakeHub) Request(_ context.Context, _ uuid.UUID, kind string, _ any, _ map[string]string, _ time.Duration) (agentprotocol.ResultFrame, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return agentprotocol.ResultFrame{}, h.err
	}
	if kind != agentprotocol.KindWorkloadInventory {
		panic("unexpected command " + kind)
	}
	if h.outcome == agentprotocol.OutcomeFailed {
		return agentprotocol.ResultFrame{Outcome: h.outcome, Error: "docker is down"}, nil
	}
	raw, _ := json.Marshal(h.inv)
	return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: raw}, nil
}

func (h *fakeHub) Resync(context.Context, uuid.UUID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resyncs++
	return nil
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	ctx  context.Context
	box  *secretbox.Box
	svc  *Service
	srv  db.Server
	ws   db.Workspace
	hub  *fakeHub
	rec  *reconciler.Reconciler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "homelab-1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, `UPDATE servers SET status = 'online', adoption_state = 'held', public_ip = '203.0.113.7', facts = '{"capabilities":["server.adoption"]}' WHERE id = $1`, srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID, WorkspaceID: &ws.ID})
	require.NoError(t, err)
	require.Equal(t, "held", srv.AdoptionState)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nil, logger)
	jm := jobs.New(pool, logger)
	domainSvc := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, domainSvc, logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a@example.com", Scopes: policy.AllScopes})
	hub := &fakeHub{}
	return &fixture{
		t: t, pool: pool, q: q, ctx: ctx, box: box, srv: srv, ws: ws, hub: hub,
		svc: NewService(Deps{Pool: pool, Bus: bus, Hub: hub, Domains: domainSvc, Box: box, Logger: logger}),
		rec: reconciler.New(pool, bus, jm, deployments.NewService(deployments.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Services: svcs, Logger: logger}), logger),
	}
}

// inventory is a web app (two replicas, a domain, a volume), a managed
// postgres, and a worker that the review leaves running unmanaged.
func (f *fixture) inventory() agentprotocol.WorkloadInventory {
	cs := webContainers(f.t, 2)
	cs = append(cs, ctr{
		svc: dbID, env: envA, dep: depDB, rel: relDB, slug: "db", image: "postgres@sha256:def", tags: []string{"postgres:17"}, digests: []string{"postgres@sha256:def"},
		port: 5432, running: true, imageEnv: []string{"PG_MAJOR=17"}, env_: []string{"PG_MAJOR=17", "POSTGRES_USER=app", "POSTGRES_PASSWORD=pw", "POSTGRES_DB=shop"},
		mounts: []agentprotocol.InventoryMount{{Type: "volume", Name: agentprotocol.VolumeName(dbID.String(), "data"), Destination: "/var/lib/postgresql/data"}},
	}.build(f.t))
	cs = append(cs, ctr{svc: jobID, env: envB, dep: depJob, rel: relJob, slug: "mailer", image: "mailer:2", tags: []string{"mailer:2"}, running: true,
		env_: []string{"SMTP_PASSWORD=s3cret"}}.build(f.t))
	return agentprotocol.WorkloadInventory{Containers: cs, Routes: []agentprotocol.Route{webRoute()}}
}

func apiErr(t *testing.T, err error) *apierr.Error {
	t.Helper()
	require.Error(t, err)
	e, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return e
}

func (f *fixture) containerNames(serviceID uuid.UUID) []string {
	f.t.Helper()
	rec, err := f.rec.DesiredState(context.Background(), f.srv.ID)
	require.NoError(f.t, err)
	for _, s := range rec.Services {
		if s.ServiceID == serviceID.String() {
			return s.ContainerNames
		}
	}
	return nil
}

func TestPlanNeedsCapabilityAndAnAnswer(t *testing.T) {
	f := newFixture(t)
	f.hub.inv = f.inventory()

	f.hub.outcome = agentprotocol.OutcomeFailed
	_, err := f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{})
	e := apiErr(t, err)
	require.Equal(t, http.StatusBadGateway, e.Status)
	require.Equal(t, "inventory_failed", e.Code)
	require.Contains(t, e.Message, "docker is down")

	f.hub.outcome = ""
	f.hub.err = apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	_, err = f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{})
	require.Equal(t, "server_offline", apiErr(t, err).Code)
	f.hub.err = nil

	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET facts = '{"capabilities":[]}' WHERE id = $1`, f.srv.ID)
	require.NoError(t, err)
	_, err = f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{})
	e = apiErr(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, e.Status)
	require.Equal(t, "agent_too_old", e.Code)

	_, err = f.svc.Plan(f.ctx, uuid.New(), AdoptionPlanRequest{})
	require.Equal(t, http.StatusNotFound, apiErr(t, err).Status)
}

func TestApplyBlocked(t *testing.T) {
	f := newFixture(t)
	inv := f.inventory()
	inv.Containers = append(inv.Containers, ctr{svc: uuid.New(), env: envA, dep: uuid.New(), slug: "stk", image: "redis", running: true,
		labels: map[string]string{agentprotocol.LabelComponent: "cache"}}.build(t))
	f.hub.inv = inv
	stack := inv.Containers[len(inv.Containers)-1].Labels[agentprotocol.LabelService]
	_, err := f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{Choices: AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{uuid.MustParse(stack): {Action: "adopt"}}}})
	e := apiErr(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, e.Status)
	require.Equal(t, "adoption_blocked", e.Code)
	require.NotEmpty(t, e.Details["blockers"])
	var held string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT adoption_state FROM servers WHERE id = $1`, f.srv.ID).Scan(&held))
	require.Equal(t, "held", held, "a blocked apply changes nothing")
}

func TestApplyRebuildsServicesAroundTheContainers(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.hub.inv = f.inventory()
	choices := AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{jobID: {Action: "leave"}}}

	plan, err := f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{Choices: choices})
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.True(t, plan.Held)
	require.Equal(t, AdoptionTotals{Adopt: 2, Leave: 1, Containers: 4}, plan.Totals)
	require.Equal(t, 0, f.hub.resyncs)

	res, err := f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{Choices: choices})
	require.NoError(t, err)
	require.Len(t, res.Services, 2)
	require.Len(t, res.Projects, 1)
	require.Equal(t, 1, res.Left)
	require.Equal(t, 1, f.hub.resyncs)

	// rows carry the IDs found on the containers
	env, err := f.q.GetEnvironmentInternal(bg, envA)
	require.NoError(t, err)
	require.Equal(t, "production", env.Environment.Name)
	require.True(t, env.Environment.IsProduction)
	web, err := f.q.GetServiceScopeInternal(bg, webID)
	require.NoError(t, err)
	require.Equal(t, "web", web.Service.Slug)
	require.EqualValues(t, 2, web.Service.Replicas)
	require.Equal(t, envA, web.Service.EnvironmentID)
	require.Equal(t, f.srv.ID, *web.Service.ServerID)
	require.Equal(t, depA, *web.Service.CurrentDeploymentID)
	require.Equal(t, web.Service.Generation, web.Service.AppliedGeneration)
	dbSvc, err := f.q.GetServiceScopeInternal(bg, dbID)
	require.NoError(t, err)
	require.Equal(t, "database", dbSvc.Service.Kind)
	require.Equal(t, depDB, *dbSvc.Service.CurrentDeploymentID)
	require.Equal(t, "postgres", services.DecodeConfig(dbSvc.Service.Config).Database.Engine)

	rel, err := f.q.GetReleaseByID(bg, relID)
	require.NoError(t, err)
	require.Equal(t, webID, rel.ServiceID)
	deps, err := f.q.ListDeploymentsByIDs(bg, []uuid.UUID{depA, depDB})
	require.NoError(t, err)
	require.Len(t, deps, 2)
	for _, d := range deps {
		require.Equal(t, "succeeded", d.Status)
		require.Equal(t, "adopt", d.Trigger)
		require.Equal(t, f.srv.ID, *d.ServerID)
	}

	// no recreation: the agent sees the running containers as the desired ones
	require.Equal(t, []string{"nc-web-f5555555-0", "nc-web-f5555555-1"}, f.containerNames(webID))
	require.Equal(t, []string{"nc-db-f8888888-0"}, f.containerNames(dbID))
	require.Nil(t, f.containerNames(jobID))

	// the re-plan shows no diff: nothing the adoption wrote is staged
	for _, sc := range []db.GetServiceScopeInternalRow{web, dbSvc} {
		rel, err := f.q.GetReleaseByID(bg, *releaseOf(t, f.q, *sc.Service.CurrentDeploymentID))
		require.NoError(t, err)
		cur, err := services.LoadCurrentState(bg, f.q, f.box, sc.Service)
		require.NoError(t, err)
		changes, err := services.DiffRelease(bg, f.q, f.box, rel, cur)
		require.NoError(t, err)
		require.Empty(t, changes, sc.Service.Slug)
	}

	// variables, volumes and domains were rebuilt
	cur, err := services.LoadCurrentState(bg, f.q, f.box, web.Service)
	require.NoError(t, err)
	require.Equal(t, "prod", cur.Raw["APP_MODE"].Value)
	require.True(t, cur.Raw["DB_PASSWORD"].Secret)
	require.Equal(t, "$${{x}}", cur.Raw["GREETING"].Value, "a literal value is escaped, not read as a reference")
	require.NotContains(t, cur.Raw, "PORT")
	require.Len(t, cur.Domains, 1)
	require.Equal(t, "app.example.com", cur.Domains[0].Hostname)
	require.EqualValues(t, 80, cur.Domains[0].TargetPort)
	dbCur, err := services.LoadCurrentState(bg, f.q, f.box, dbSvc.Service)
	require.NoError(t, err)
	require.Len(t, dbCur.Volumes, 1)
	require.Equal(t, "data", dbCur.Volumes[0].Name)
	require.Equal(t, "/var/lib/postgresql/data", dbCur.Volumes[0].MountPath)
	require.True(t, dbCur.Raw["POSTGRES_PASSWORD"].Secret)

	// the hold ended, the left service is unmanaged
	var state string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT adoption_state FROM servers WHERE id = $1`, f.srv.ID).Scan(&state))
	require.Equal(t, "", state)
	left, err := f.q.ListServerUnmanagedServices(bg, f.srv.ID)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{jobID}, left)
	var audits int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = 'server.adopt'`).Scan(&audits))
	require.Equal(t, 1, audits)

	// the unmanaged service can still be reviewed later; the adopted ones are not offered again
	plan, err = f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{})
	require.NoError(t, err)
	require.False(t, plan.Held)
	require.Equal(t, AdoptionTotals{Adopt: 1, Containers: 1}, plan.Totals)
	_, err = f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{})
	require.NoError(t, err)
	left, err = f.q.ListServerUnmanagedServices(bg, f.srv.ID)
	require.NoError(t, err)
	require.Empty(t, left)
	require.Equal(t, []string{"nc-mailer-fa999999-0"}, f.containerNames(jobID))

	// nothing is left to adopt
	_, err = f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{})
	e := apiErr(t, err)
	require.Equal(t, http.StatusConflict, e.Status)
	require.Equal(t, "nothing_to_adopt", e.Code)
}

func releaseOf(t *testing.T, q *db.Queries, depID uuid.UUID) *uuid.UUID {
	t.Helper()
	deps, err := q.ListDeploymentsByIDs(context.Background(), []uuid.UUID{depID})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	return deps[0].ReleaseID
}

func TestApplyRemoveWritesNothingAndEndsTheHold(t *testing.T) {
	f := newFixture(t)
	f.hub.inv = f.inventory()
	res, err := f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{Choices: AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{jobID: {Action: "remove"}}}})
	require.NoError(t, err)
	require.Equal(t, 1, res.Removed)
	left, err := f.q.ListServerUnmanagedServices(context.Background(), f.srv.ID)
	require.NoError(t, err)
	require.Empty(t, left)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM services WHERE id = $1`, jobID).Scan(&n))
	require.Zero(t, n)
	// the second environment kept no service: nothing was created for it
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM environments WHERE id = $1`, envB).Scan(&n))
	require.Zero(t, n)
}

func TestApplyReattachesAServiceTheInstanceKnows(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.hub.inv = f.inventory()
	// the service was adopted earlier on a server that is gone
	_, err := f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{})
	require.NoError(t, err)
	var oldSrv uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, `INSERT INTO servers (workspace_id, name, status) VALUES ($1, 'gone', 'revoked') RETURNING id`, f.ws.ID).Scan(&oldSrv))
	_, err = f.pool.Exec(bg, `UPDATE deployments SET server_id = $2 WHERE id = $1`, depA, oldSrv)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE services SET server_id = $2 WHERE id = $1`, webID, oldSrv)
	require.NoError(t, err)
	// the release snapshot names the server it ran on
	_, err = f.pool.Exec(bg, `UPDATE releases SET config = jsonb_set(config, '{serverId}', to_jsonb($2::text)) WHERE id = $1`, relID, oldSrv.String())
	require.NoError(t, err)
	// ... and the host runs it again: held again
	_, err = f.pool.Exec(bg, `UPDATE servers SET adoption_state = 'held' WHERE id = $1`, f.srv.ID)
	require.NoError(t, err)

	plan, err := f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{})
	require.NoError(t, err)
	var web AdoptionService
	for _, e := range plan.Environments {
		for _, s := range e.Services {
			if s.ID == webID {
				web = s
			}
		}
	}
	require.Equal(t, ActionReattach, web.Action)
	res, err := f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Reattached, "only the service this instance no longer runs anywhere is a candidate")
	row, err := f.q.GetServiceScopeInternal(bg, webID)
	require.NoError(t, err)
	require.Equal(t, f.srv.ID, *row.Service.ServerID)
	require.Equal(t, []string{"nc-web-f5555555-0", "nc-web-f5555555-1"}, f.containerNames(webID))

	// a reattached service is not staged: the release names this server now
	rel, err := f.q.GetReleaseByID(bg, relID)
	require.NoError(t, err)
	cur, err := services.LoadCurrentState(bg, f.q, f.box, row.Service)
	require.NoError(t, err)
	changes, err := services.DiffRelease(bg, f.q, f.box, rel, cur)
	require.NoError(t, err)
	require.Empty(t, changes)
}

func TestApplyRefusesWhenTheServerChangedAfterTheReview(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.hub.inv = f.inventory()
	plan, srv, state, err := f.svc.compute(f.ctx, f.srv.ID, AdoptionChoices{})
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)

	// a concurrent review leaves one of the services unmanaged
	require.NoError(t, f.q.InsertServerUnmanagedService(bg, db.InsertServerUnmanagedServiceParams{ServerID: f.srv.ID, ServiceID: jobID}))
	ap := &applier{s: f.svc, ctx: f.ctx, plan: plan, srv: srv, state: state}
	err = db.InTx(f.ctx, f.pool, ap.run)
	e := apiErr(t, err)
	require.Equal(t, http.StatusConflict, e.Status)
	require.Equal(t, "adoption_changed", e.Code)
	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM services`).Scan(&n))
	require.Zero(t, n, "nothing was written")

	// the same for a hold that ended and for services that now run here
	require.NoError(t, f.q.DeleteServerUnmanagedService(bg, db.DeleteServerUnmanagedServiceParams{ServerID: f.srv.ID, ServiceID: jobID}))
	require.NoError(t, db.InTx(f.ctx, f.pool, (&applier{s: f.svc, ctx: f.ctx, plan: plan, srv: srv, state: state}).run))
	err = db.InTx(f.ctx, f.pool, (&applier{s: f.svc, ctx: f.ctx, plan: plan, srv: srv, state: state}).run)
	require.Equal(t, "adoption_changed", apiErr(t, err).Code, "a plan cannot be applied twice")
}

func TestAdoptedDomainsAreValidatedAndNeverGenerated(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	inv := f.inventory()
	route := webRoute()
	route.Hosts = []string{"app.example.com", "not a host"}
	route.Domains = []agentprotocol.DomainRule{{ID: "1", Host: "app.example.com", Generated: true}, {ID: "2", Host: "not a host"}}
	inv.Routes = []agentprotocol.Route{route}
	f.hub.inv = inv

	plan, err := f.svc.Plan(f.ctx, f.srv.ID, AdoptionPlanRequest{})
	require.NoError(t, err)
	var web AdoptionService
	for _, e := range plan.Environments {
		for _, s := range e.Services {
			if s.ID == webID {
				web = s
			}
		}
	}
	got := map[string]AdoptionDomain{}
	for _, d := range web.Domains {
		got[d.Host] = d
	}
	require.True(t, got["app.example.com"].Include, got["app.example.com"].Note)
	require.False(t, got["not a host"].Include)
	require.Contains(t, got["not a host"].Note, "this host name is not available here: ")

	_, err = f.svc.Apply(f.ctx, f.srv.ID, AdoptionApplyRequest{})
	require.NoError(t, err)
	var generated bool
	require.NoError(t, f.pool.QueryRow(bg, `SELECT generated FROM domains WHERE hostname = 'app.example.com'`).Scan(&generated))
	require.False(t, generated, "the generated flag is never taken from the host")
	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM domains WHERE service_id = $1`, webID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestApplyIntoAnExistingProductionEnvironmentIsGuarded(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.hub.inv = f.inventory()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Prod", Slug: "prod"})
	require.NoError(t, err)
	_, err = f.q.CreateEnvironmentWithID(bg, db.CreateEnvironmentWithIDParams{ID: envA, ProjectID: prj.ID, Name: "Production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	p, _ := auth.PrincipalFrom(f.ctx)
	id := uuid.New()
	tok := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &id, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
	choices := AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{jobID: {Action: "leave"}}}

	// No guard installed on the control plane: the adoption fails closed.
	_, err = f.svc.Apply(tok, f.srv.ID, AdoptionApplyRequest{Choices: choices})
	e := apiErr(t, err)
	require.Equal(t, prodguard.CodeApprovalUnavailable, e.Code)
	require.Equal(t, http.StatusServiceUnavailable, e.Status)

	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	_, err = f.svc.Apply(tok, f.srv.ID, AdoptionApplyRequest{Choices: choices})
	staged, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want approval_required, got %v", err)
	require.Contains(t, staged.Plan.Summary, "Adopt the workloads of homelab-1 into production (Production)")
	require.Contains(t, staged.Plan.Effects[0], "db, web")
	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM services`).Scan(&n))
	require.Zero(t, n, "nothing is written before the approval")

	person := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes})
	_, err = g.Approve(person, staged.ApprovalID)
	require.NoError(t, err)
	// another choice is another approval
	_, err = f.svc.Apply(tok, f.srv.ID, AdoptionApplyRequest{Choices: AdoptionChoices{Services: map[uuid.UUID]AdoptionServiceChoice{jobID: {Action: "leave"}, dbID: {Action: "leave"}}}})
	_, asked := prodguard.AsRequired(err)
	require.True(t, asked)
	res, err := f.svc.Apply(tok, f.srv.ID, AdoptionApplyRequest{Choices: choices})
	require.NoError(t, err)
	require.Len(t, res.Services, 2)
}

func TestApplyIntoNewEnvironmentsIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	f.hub.inv = f.inventory()
	p, _ := auth.PrincipalFrom(f.ctx)
	id := uuid.New()
	tok := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &id, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
	_, err := f.svc.Apply(tok, f.srv.ID, AdoptionApplyRequest{})
	require.NoError(t, err)
}
