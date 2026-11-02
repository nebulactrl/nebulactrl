package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const gib = int64(1) << 30

// fx is a workspace with a production environment, a fleet Service wired to
// the real server, service and deployment services (no job client runs: the
// tests call Step and report deployment results themselves, the way the agent
// hub would).
type fx struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	box  *secretbox.Box
	jm   *jobs.Manager
	svc  *Service
	svcs *services.Service
	deps *deployments.Service
	ws   db.Workspace
	user db.CreateUserRow
	prj  db.Project
	env  db.Environment
	ctx  context.Context
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	cfg := &config.Config{SecretKey: bytes.Repeat([]byte{7}, 32), Listen: ":0"}
	hub := agents.NewHub(pool, bus, jm, cfg, logger)
	svcs := services.NewService(pool, bus, box, hub, logger)
	deps := deployments.NewService(deployments.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Dispatcher: hub, Services: svcs})
	deployments.RegisterJobs(jm, deps)
	f := &fx{
		t: t, pool: pool, q: q, box: box, jm: jm, svcs: svcs, deps: deps, ws: ws, user: u, prj: prj, env: env,
		svc: NewService(pool, bus, jm, servers.NewService(pool, bus, hub, cfg, logger), svcs, deps, logger),
	}
	RegisterJobs(jm, f.svc)
	f.ctx = f.owner()
	return f
}

func (f *fx) owner() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.user.ID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

// token is a REST token principal; projects nil means unrestricted.
func (f *fx) token(scopes []string, projects []uuid.UUID) *auth.Principal {
	tok := uuid.New()
	return &auth.Principal{
		Type: auth.TypeToken, UserID: f.user.ID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: scopes, ProjectIDs: projects, RequireProductionApproval: true,
	}
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

// server enrolls an online server with the given memory numbers.
func (f *fx) server(name string, memTotal, memUsed int64) uuid.UUID {
	f.t.Helper()
	s, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: name})
	require.NoError(f.t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{CPUs: 4, MemTotal: uint64(memTotal), DiskTotal: uint64(100 * gib)})
	f.exec(`UPDATE servers SET status='online', last_seen_at=now(), facts=$2 WHERE id=$1`, s.ID, facts)
	require.NoError(f.t, f.q.InsertServerMetricSample(context.Background(), db.InsertServerMetricSampleParams{
		ServerID: s.ID, CpuPercent: 12, MemBytes: memUsed, MemLimitBytes: memTotal, DiskUsedBytes: 10 * gib, DiskTotalBytes: 100 * gib,
	}))
	return s.ID
}

func (f *fx) setDrain(server uuid.UUID, state string) {
	f.t.Helper()
	f.exec(`UPDATE servers SET drain_state=$2 WHERE id=$1`, server, state)
}

func (f *fx) drainState(server uuid.UUID) (string, string) {
	f.t.Helper()
	var state, msg string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT drain_state, drain_message FROM servers WHERE id=$1`, server).Scan(&state, &msg))
	return state, msg
}

func (f *fx) app(name string, server uuid.UUID, memoryMB int) uuid.UUID {
	f.t.Helper()
	return f.appIn(f.env.ID, name, server, memoryMB)
}

func (f *fx) appIn(env uuid.UUID, name string, server uuid.UUID, memoryMB int) uuid.UUID {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, env, services.CreateInput{
		Name: name, Kind: services.KindApp, SourceType: services.SourceImage, ServerID: &server,
		Config: services.Config{Image: "traefik/whoami:latest", Port: 80, MemoryMB: memoryMB},
	})
	require.NoError(f.t, err)
	return v.ID
}

func (f *fx) serviceServer(id uuid.UUID) uuid.UUID {
	f.t.Helper()
	var s *uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT server_id FROM services WHERE id=$1`, id).Scan(&s))
	require.NotNil(f.t, s)
	return *s
}

func (f *fx) generation(id uuid.UUID) (generation, applied int64) {
	f.t.Helper()
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT generation, applied_generation FROM services WHERE id=$1`, id).Scan(&generation, &applied))
	return generation, applied
}

// deployed deploys a service and reports success from its server.
func (f *fx) deployed(id, server uuid.UUID) {
	f.t.Helper()
	v, err := f.deps.Deploy(f.ctx, id, "", "")
	require.NoError(f.t, err)
	f.report(v.ID, server, agentprotocol.OutcomeSucceeded, "")
}

func (f *fx) report(dep, server uuid.UUID, outcome, msg string) {
	f.t.Helper()
	require.NoError(f.t, f.deps.HandleResult(context.Background(), server, uuid.New(), &dep, agentprotocol.ResultFrame{Outcome: outcome, Error: msg}))
}

// activeDrainDeployment is the in-flight deployment a drain started for the service.
func (f *fx) activeDrainDeployment(svc uuid.UUID) (uuid.UUID, bool) {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT id, status FROM deployments WHERE service_id=$1 AND trigger='drain' ORDER BY created_at DESC, id DESC`, svc)
	require.NoError(f.t, err)
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var st string
		require.NoError(f.t, rows.Scan(&id, &st))
		if deployments.Status(st).IsActive() {
			return id, true
		}
	}
	require.NoError(f.t, rows.Err())
	return uuid.Nil, false
}

func (f *fx) start(src uuid.UUID, confirm bool, pairs ...uuid.UUID) (DrainView, error) {
	f.t.Helper()
	in := StartInput{ConfirmStateful: confirm}
	for i := 0; i+1 < len(pairs); i += 2 {
		in.Moves = append(in.Moves, MoveInput{ServiceID: pairs[i], TargetServerID: pairs[i+1]})
	}
	return f.svc.Start(f.ctx, src, in)
}

func (f *fx) mustStart(src uuid.UUID, pairs ...uuid.UUID) DrainView {
	f.t.Helper()
	v, err := f.start(src, false, pairs...)
	require.NoError(f.t, err)
	return v
}

// run steps the drain to its end; onWait is called whenever the job would snooze.
func (f *fx) run(drain uuid.UUID, onWait func()) {
	f.t.Helper()
	for range 200 {
		res, err := f.svc.Step(context.Background(), drain)
		require.NoError(f.t, err)
		if res.Done {
			return
		}
		if res.Wait > 0 && onWait != nil {
			onWait()
		}
	}
	f.t.Fatal("drain did not finish")
}

// completeDrainDeployments reports every in-flight drain deployment of the services as outcome.
func (f *fx) completeDrainDeployments(outcome, msg string, svcs ...uuid.UUID) func() {
	return func() {
		for _, s := range svcs {
			if id, ok := f.activeDrainDeployment(s); ok {
				f.report(id, f.serviceServer(s), outcome, msg)
			}
		}
	}
}

func (f *fx) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (f *fx) audits(action string) int {
	f.t.Helper()
	return f.count(`SELECT count(*) FROM audit_events WHERE action=$1`, action)
}

func (f *fx) storedMoves(drain uuid.UUID) []storedMove {
	f.t.Helper()
	var raw json.RawMessage
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT moves FROM server_drains WHERE id=$1`, drain).Scan(&raw))
	m, err := decodeMoves(raw)
	require.NoError(f.t, err)
	return m
}

func (f *fx) setStoredMoves(drain uuid.UUID, moves []storedMove) {
	f.t.Helper()
	raw, err := json.Marshal(moves)
	require.NoError(f.t, err)
	f.exec(`UPDATE server_drains SET moves=$2 WHERE id=$1`, drain, raw)
}

func requireAPIError(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code, ae.Message)
	return ae
}

func TestDrainMovesServicesOneAtATimeAndRecordsIt(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha, bravo := f.app("alpha", src, 0), f.app("bravo", src, 0)
	f.deployed(alpha, src)
	f.deployed(bravo, src)
	genBefore, _ := f.generation(alpha)

	v := f.mustStart(src, alpha, dst, bravo, dst)
	require.Equal(t, RunRunning, v.State)
	require.Equal(t, "Ada", v.ActorLabel)
	require.Len(t, v.Moves, 2)
	for _, m := range v.Moves {
		require.Equal(t, MovePending, m.Status)
		require.Equal(t, src, m.SourceServerID)
		require.Equal(t, dst, m.TargetServerID)
		require.Equal(t, "dst", m.TargetServerName)
		require.Equal(t, "Shop", m.ProjectName)
		require.Equal(t, "production", m.EnvironmentName)
	}
	state, _ := f.drainState(src)
	require.Equal(t, "draining", state)
	require.Equal(t, 1, f.audits("server.drain.start"))

	// A second drain of the same server is refused while one runs.
	_, err := f.start(src, false, alpha, dst, bravo, dst)
	requireAPIError(t, err, 409, "drain_in_progress")

	// Undrain refuses while draining.
	_, err = f.svc.Undrain(f.ctx, src)
	requireAPIError(t, err, 409, "drain_in_progress")

	// Sequential: a service only starts moving once the previous one is done.
	var order []uuid.UUID
	f.run(v.ID, func() {
		var active []uuid.UUID
		for _, s := range []uuid.UUID{alpha, bravo} {
			if id, ok := f.activeDrainDeployment(s); ok {
				active = append(active, s)
				require.Equal(t, dst, f.serviceServer(s))
				f.report(id, dst, agentprotocol.OutcomeSucceeded, "")
			}
		}
		require.LessOrEqual(t, len(active), 1, "moves must not overlap")
		order = append(order, active...)
	})
	require.Equal(t, []uuid.UUID{alpha, bravo}, order)

	state, msg := f.drainState(src)
	require.Equal(t, "drained", state)
	require.Empty(t, msg)
	require.Equal(t, dst, f.serviceServer(alpha))
	require.Equal(t, dst, f.serviceServer(bravo))
	gen, applied := f.generation(alpha)
	require.Greater(t, gen, genBefore)
	require.Equal(t, gen, applied, "the move was deployed, nothing stays staged")
	require.Equal(t, 2, f.count(`SELECT count(*) FROM agent_commands WHERE server_id=$1 AND kind='remove_service'`, src),
		"the old containers are removed from the drained server")

	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, RunCompleted, got.State)
	require.NotNil(t, got.FinishedAt)
	for _, m := range got.Moves {
		require.Equal(t, MoveDone, m.Status)
		require.Equal(t, "succeeded", m.DeploymentStatus)
		require.NotNil(t, m.DeploymentID)
	}
	require.Equal(t, 1, f.audits("server.drain.complete"))

	// A finished drain does nothing when stepped again.
	res, err := f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, StepResult{Done: true}, res)

	// Undrain reopens the server and says where it was.
	srv, err := f.svc.Undrain(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, "none", srv.DrainState)
	require.Equal(t, 1, f.audits("server.undrain"))
	// Undraining a server that is not drained changes nothing and records nothing.
	srv, err = f.svc.Undrain(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, "none", srv.DrainState)
	require.Equal(t, 1, f.audits("server.undrain"))
}

func TestDrainStartRefusals(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	web, dbSvc := f.app("web", src, 0), f.app("data", src, 0)
	_, err := f.svcs.AddVolume(f.ctx, dbSvc, "data", "/data")
	require.NoError(t, err)
	other := f.server("other", 8*gib, 1*gib)
	stray := f.app("stray", other, 0)

	expect := func(name string, confirm bool, status int, code string, pairs ...uuid.UUID) {
		t.Helper()
		_, err := f.start(src, confirm, pairs...)
		requireAPIError(t, err, status, code)
		require.Zero(t, f.count(`SELECT count(*) FROM server_drains`), name+": no drain row")
		state, _ := f.drainState(src)
		require.Equal(t, "none", state, name)
	}

	expect("stateful needs confirmation", false, 422, "confirmation_required", web, dst, dbSvc, dst)
	ae := requireAPIError(t, errOf(f.start(src, true, web, dst)), 422, "incomplete_moves")
	require.Contains(t, ae.Message, "data", "names the service left out")
	expect("duplicate service", true, 422, "invalid_moves", web, dst, web, dst, dbSvc, dst)
	expect("service of another server", true, 422, "invalid_moves", web, dst, dbSvc, dst, stray, dst)
	expect("onto itself", true, 422, "invalid_target", web, src, dbSvc, src)
	expect("unknown target", true, 422, "server_not_found", web, uuid.New(), dbSvc, uuid.New())

	f.setDrain(dst, "draining")
	expect("draining target", true, 422, "server_draining", web, dst, dbSvc, dst)
	f.setDrain(dst, "none")
	f.exec(`UPDATE servers SET status='revoked' WHERE id=$1`, dst)
	expect("revoked target", true, 422, "server_unavailable", web, dst, dbSvc, dst)
	f.exec(`UPDATE servers SET status='online', accept_workloads=false WHERE id=$1`, dst)
	expect("target not accepting workloads", true, 422, "server_not_accepting_workloads", web, dst, dbSvc, dst)
	f.exec(`UPDATE servers SET accept_workloads=true, last_seen_at=now() - interval '1 hour' WHERE id=$1`, dst)
	expect("silent target is offline", true, 422, BlockTargetOffline, web, dst, dbSvc, dst)
	f.exec(`UPDATE servers SET last_seen_at=now() WHERE id=$1`, dst)

	// Unknown server, another team's server, and callers without the rights.
	_, err = f.svc.Start(f.ctx, uuid.New(), StartInput{})
	requireAPIError(t, err, 404, "not_found")
	ws2, err := f.q.CreateWorkspace(context.Background(), "w2")
	require.NoError(t, err)
	foreign, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: ws2.ID, Name: "foreign"})
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, foreign.ID, StartInput{})
	requireAPIError(t, err, 404, "not_found")

	readOnly := auth.WithPrincipal(context.Background(), f.token([]string{policy.ScopeServersRead, policy.ScopeProjectsRead}, nil))
	_, err = f.svc.Start(readOnly, src, StartInput{Moves: []MoveInput{{ServiceID: web, TargetServerID: dst}}})
	requireAPIError(t, err, 403, "forbidden")
	restricted := auth.WithPrincipal(context.Background(), f.token(policy.AllScopes, []uuid.UUID{f.prj.ID}))
	_, err = f.svc.Start(restricted, src, StartInput{Moves: []MoveInput{{ServiceID: web, TargetServerID: dst}}})
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Start(context.Background(), src, StartInput{})
	require.Error(t, err, "anonymous callers are refused")
	require.Zero(t, f.count(`SELECT count(*) FROM server_drains`))

	// The refusals left the server usable: a complete, confirmed drain starts.
	v, err := f.start(src, true, web, dst, dbSvc, dst)
	require.NoError(t, err)
	require.Equal(t, RunRunning, v.State)
	require.True(t, v.Moves[1].Stateful, "the service with a volume is flagged")
	require.False(t, v.Moves[0].Stateful)
	require.Equal(t, "data", v.Moves[1].ServiceName, "stateless services move first, the stateful one last")
}

func errOf[T any](_ T, err error) error { return err }

func TestDrainStartFromRESTTokenNeedsApprovalForProductionServices(t *testing.T) {
	f := newFx(t)
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)

	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	staging, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	beta := f.appIn(staging.ID, "beta", src, 0)
	rest := auth.WithPrincipal(context.Background(), f.token(policy.AllScopes, nil))
	in := StartInput{Moves: []MoveInput{{ServiceID: alpha, TargetServerID: dst}, {ServiceID: beta, TargetServerID: dst}}}

	_, err = f.svc.Start(rest, src, in)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, "Drain the server src: move 1 production service(s) to other servers", ar.Plan.Summary)
	require.Contains(t, ar.Plan.Effects, "alpha (Shop / production) is redeployed on dst.")
	require.NotContains(t, ar.Plan.Effects, "beta (Shop / staging) is redeployed on dst.", "only production services are listed")
	require.Contains(t, ar.Plan.Effects, "Volumes are not copied.")
	state, _ := f.drainState(src)
	require.Equal(t, "none", state, "nothing started before the approval")

	person := f.owner()
	_, err = g.Approve(person, ar.ApprovalID)
	require.NoError(t, err)
	v, err := f.svc.Start(rest, src, in)
	require.NoError(t, err)
	require.Equal(t, RunRunning, v.State)
	state, _ = f.drainState(src)
	require.Equal(t, "draining", state)

	// The approval was consumed: after canceling and finishing, a new drain asks again.
	_, err = f.svc.Cancel(rest, src)
	require.NoError(t, err)
	f.run(v.ID, nil)
	_, err = f.svc.Start(rest, src, in)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "a used approval does not carry over: %v", err)

	// A server with only non-production services never asks.
	src2 := f.server("src2", 8*gib, 1*gib)
	gamma := f.appIn(staging.ID, "gamma", src2, 0)
	_, err = f.svc.Start(rest, src2, StartInput{Moves: []MoveInput{{ServiceID: gamma, TargetServerID: dst}}})
	require.NoError(t, err)
}

func TestDrainFailureStopsRevertsPlacementAndLeavesTheRest(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha, bravo := f.app("alpha", src, 0), f.app("bravo", src, 0)
	f.deployed(alpha, src)
	f.deployed(bravo, src)
	genAlpha, _ := f.generation(alpha)

	v := f.mustStart(src, alpha, dst, bravo, dst)
	f.run(v.ID, f.completeDrainDeployments(agentprotocol.OutcomeFailed, "image pull failed: no such image", alpha, bravo))

	state, msg := f.drainState(src)
	require.Equal(t, "failed", state)
	require.Equal(t, "Moving alpha to dst failed: image pull failed: no such image", msg)
	require.Equal(t, src, f.serviceServer(alpha), "the failed service is back where it ran")
	gen, _ := f.generation(alpha)
	require.Equal(t, genAlpha, gen, "with its old generation")
	require.Equal(t, src, f.serviceServer(bravo), "bravo was never touched")
	_, ok := f.activeDrainDeployment(bravo)
	require.False(t, ok)
	require.Zero(t, f.count(`SELECT count(*) FROM deployments WHERE service_id=$1 AND trigger='drain'`, bravo))

	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, RunFailed, got.State)
	require.Equal(t, msg, got.Message)
	require.Equal(t, MoveFailed, got.Moves[0].Status)
	require.Equal(t, "failed", got.Moves[0].DeploymentStatus)
	require.Equal(t, MovePending, got.Moves[1].Status)
	require.Equal(t, 1, f.audits("server.drain.fail"))

	// A failed drain does not block placement and can be undrained.
	srv, err := f.svc.Undrain(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, "none", srv.DrainState)
	require.Equal(t, 1, f.audits("server.undrain"))
}

func TestDrainFailureWithoutAReasonNamesTheDeploymentOutcome(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	v := f.mustStart(src, alpha, dst)

	f.run(v.ID, func() {
		id, ok := f.activeDrainDeployment(alpha)
		require.True(t, ok)
		f.exec(`UPDATE deployments SET status='canceled', status_message='' WHERE id=$1`, id)
	})
	_, msg := f.drainState(src)
	require.Equal(t, "Moving alpha to dst failed: the deployment ended as canceled", msg)
}

func TestDrainFailureReportsAPlacementChangedMeanwhile(t *testing.T) {
	f := newFx(t)
	src, dst, third := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib), f.server("third", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	v := f.mustStart(src, alpha, dst)

	f.run(v.ID, func() {
		id, ok := f.activeDrainDeployment(alpha)
		require.True(t, ok)
		// Someone moves the service elsewhere while the drain deploys it.
		f.exec(`UPDATE services SET server_id=$2 WHERE id=$1`, alpha, third)
		f.report(id, dst, agentprotocol.OutcomeFailed, "boom")
	})
	_, msg := f.drainState(src)
	require.Contains(t, msg, "Moving alpha to dst failed: boom")
	require.Contains(t, msg, "the service's placement was changed meanwhile and was not reverted")
	require.Equal(t, third, f.serviceServer(alpha), "the other placement is left alone")
	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, msg, got.Moves[0].Message, "the stored move carries the same note")
}

func TestDrainFailsWhenThePlacementChangeIsRefused(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	v := f.mustStart(src, alpha, dst)

	// The target starts draining after the plan was accepted: the service layer refuses the move.
	f.setDrain(dst, "draining")
	res, err := f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.True(t, res.Done)
	state, msg := f.drainState(src)
	require.Equal(t, "failed", state)
	require.Contains(t, msg, "Moving alpha to dst failed: dst is being drained")
	require.NotContains(t, msg, "changed meanwhile", "a refused move never changed the placement, so there was nothing to revert")
	require.Equal(t, src, f.serviceServer(alpha))
	require.Zero(t, f.count(`SELECT count(*) FROM deployments WHERE service_id=$1 AND trigger='drain'`, alpha), "nothing was deployed")
}

func TestDrainCancelBeforeAndAfterTheFirstMove(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha, bravo := f.app("alpha", src, 0), f.app("bravo", src, 0)
	f.deployed(alpha, src)
	f.deployed(bravo, src)

	// Nothing running: cancel conflicts.
	_, err := f.svc.Cancel(f.ctx, src)
	requireAPIError(t, err, 409, "no_drain_in_progress")

	// Canceled before anything moved: the server is schedulable again.
	v := f.mustStart(src, alpha, dst, bravo, dst)
	c, err := f.svc.Cancel(f.ctx, src)
	require.NoError(t, err)
	require.True(t, c.CancelRequested)
	require.Equal(t, RunRunning, c.State, "it stops at the next step, not immediately")
	f.run(v.ID, nil)
	state, msg := f.drainState(src)
	require.Equal(t, "none", state)
	require.Empty(t, msg)
	require.Equal(t, src, f.serviceServer(alpha))
	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, RunCanceled, got.State)
	require.Empty(t, got.Message)
	require.Equal(t, 1, f.audits("server.drain.cancel"))

	// Canceled during the first move: that move finishes, the rest stays.
	v = f.mustStart(src, alpha, dst, bravo, dst)
	canceled := false
	f.run(v.ID, func() {
		if !canceled {
			canceled = true
			_, err := f.svc.Cancel(f.ctx, src)
			require.NoError(t, err)
		}
		f.completeDrainDeployments(agentprotocol.OutcomeSucceeded, "", alpha, bravo)()
	})
	state, msg = f.drainState(src)
	require.Equal(t, "failed", state)
	require.Equal(t, "Canceled after 1 of 2 moves", msg)
	require.Equal(t, dst, f.serviceServer(alpha))
	require.Equal(t, src, f.serviceServer(bravo))
	got, err = f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, RunCanceled, got.State)
	require.Equal(t, "Canceled after 1 of 2 moves", got.Message)
	require.Equal(t, MoveDone, got.Moves[0].Status)
	require.Equal(t, MovePending, got.Moves[1].Status)

	// The newest drain wins once none runs; and a canceled server can be undrained.
	_, err = f.svc.Cancel(f.ctx, src)
	requireAPIError(t, err, 409, "no_drain_in_progress")
	srv, err := f.svc.Undrain(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, "none", srv.DrainState)
}

func TestDrainGetAndCancelAuthorization(t *testing.T) {
	f := newFx(t)
	src := f.server("src", 8*gib, 1*gib)

	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Nil(t, got, "a server that was never drained has no drain")

	_, err = f.svc.Get(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.Cancel(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.Undrain(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")

	restricted := auth.WithPrincipal(context.Background(), f.token(policy.AllScopes, []uuid.UUID{f.prj.ID}))
	_, err = f.svc.Get(restricted, src)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Cancel(restricted, src)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Undrain(restricted, src)
	requireAPIError(t, err, 403, "forbidden")

	readOnly := auth.WithPrincipal(context.Background(), f.token([]string{policy.ScopeServersRead, policy.ScopeProjectsRead}, nil))
	_, err = f.svc.Cancel(readOnly, src)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Undrain(readOnly, src)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Get(readOnly, src)
	require.NoError(t, err, "reading a drain needs only the read scopes")
}

func TestDrainStepEndsForUnknownOrFinishedDrainsAndRejectsBrokenState(t *testing.T) {
	f := newFx(t)
	res, err := f.svc.Step(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, StepResult{Done: true}, res, "a drain that no longer exists is done")

	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	v := f.mustStart(src, alpha, dst)
	f.exec(`UPDATE server_drains SET moves='{"not":"a list"}' WHERE id=$1`, v.ID)
	_, err = f.svc.Step(context.Background(), v.ID)
	require.ErrorContains(t, err, "decode drain moves")
	_, err = f.svc.Get(f.ctx, src)
	require.ErrorContains(t, err, "decode drain moves")
}

func TestDrainOfAServiceThatNeverRanOnlyChangesItsPlacement(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	idle := f.app("idle", src, 0)
	v := f.mustStart(src, idle, dst)

	f.run(v.ID, nil)
	require.Equal(t, dst, f.serviceServer(idle))
	require.Zero(t, f.count(`SELECT count(*) FROM deployments WHERE service_id=$1`, idle), "nothing is started for a service that never ran")
	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, RunCompleted, got.State)
	require.Equal(t, "Not deployed yet: only the placement changed.", got.Moves[0].Message)
	require.Equal(t, MoveDone, got.Moves[0].Status)
	state, _ := f.drainState(src)
	require.Equal(t, "drained", state)
}

func TestDrainSkipsAServiceDeletedMeanwhile(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	gone, kept := f.app("gone", src, 0), f.app("kept", src, 0)
	v := f.mustStart(src, gone, dst, kept, dst)
	require.NoError(t, f.svcs.Delete(f.ctx, gone, services.DeleteInput{Confirm: "gone"}))

	f.run(v.ID, nil)
	got, err := f.svc.Get(f.ctx, src)
	require.NoError(t, err)
	require.Equal(t, RunCompleted, got.State)
	byName := map[string]Move{}
	for _, m := range got.Moves {
		byName[m.ServiceName] = m
	}
	require.Equal(t, MoveDone, byName["gone"].Status)
	require.Equal(t, "The service no longer exists.", byName["gone"].Message)
	require.Equal(t, dst, f.serviceServer(kept))
}

func TestDrainWaitsForARunningDeploymentBeforeMoving(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	busy, err := f.deps.Deploy(f.ctx, alpha, "", "")
	require.NoError(t, err)
	v := f.mustStart(src, alpha, dst)

	res, err := f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, StepResult{Wait: busyInterval}, res)
	require.Equal(t, src, f.serviceServer(alpha), "the placement is not touched under a running deployment")
	require.Equal(t, MovePending, f.storedMoves(v.ID)[0].Status)

	f.report(busy.ID, src, agentprotocol.OutcomeSucceeded, "")
	f.run(v.ID, f.completeDrainDeployments(agentprotocol.OutcomeSucceeded, "", alpha))
	require.Equal(t, dst, f.serviceServer(alpha))
}

func TestDrainResumesAfterACrashWithoutDeployingTwice(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	v := f.mustStart(src, alpha, dst)

	// Step until the drain's deployment exists and is stored.
	res, err := f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, StepResult{Wait: pollInterval}, res)
	depID, ok := f.activeDrainDeployment(alpha)
	require.True(t, ok)
	moves := f.storedMoves(v.ID)
	require.Equal(t, MoveDeploying, moves[0].Status)
	require.Equal(t, depID, *moves[0].DeploymentID)

	// Still running: the next step only waits.
	res, err = f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, StepResult{Wait: pollInterval}, res)

	// A crash after the deployment started but before it was stored: the step adopts it.
	moves[0].Status, moves[0].DeploymentID = MoveMoving, nil
	f.setStoredMoves(v.ID, moves)
	res, err = f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, StepResult{Wait: pollInterval}, res)
	moves = f.storedMoves(v.ID)
	require.Equal(t, MoveDeploying, moves[0].Status)
	require.Equal(t, depID, *moves[0].DeploymentID, "the deployment that already runs is reused")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM deployments WHERE service_id=$1 AND trigger='drain'`, alpha))

	// Someone else's deployment (not the drain's) on the target is waited for, never adopted.
	f.exec(`UPDATE deployments SET trigger='manual' WHERE id=$1`, depID)
	moves[0].Status, moves[0].DeploymentID = MoveMoving, nil
	f.setStoredMoves(v.ID, moves)
	res, err = f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, StepResult{Wait: busyInterval}, res)
	require.Equal(t, MoveMoving, f.storedMoves(v.ID)[0].Status)
}

func TestDrainFailsAMoveWhoseDeploymentDisappeared(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	v := f.mustStart(src, alpha, dst)
	_, err := f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	depID, ok := f.activeDrainDeployment(alpha)
	require.True(t, ok)
	f.exec(`DELETE FROM deployments WHERE id=$1`, depID)

	res, err := f.svc.Step(context.Background(), v.ID)
	require.NoError(t, err)
	require.True(t, res.Done)
	_, msg := f.drainState(src)
	require.Equal(t, "Moving alpha to dst failed: its deployment disappeared.", msg)
	require.Equal(t, src, f.serviceServer(alpha))
}

func TestDrainWorkerStepsUntilItMustWait(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	idle := f.app("idle", src, 0)
	v := f.mustStart(src, alpha, dst, idle, dst)
	w := &drainWorker{s: f.svc}
	work := func() error {
		return w.Work(context.Background(), &river.Job[DrainArgs]{Args: DrainArgs{DrainID: v.ID}})
	}

	// The deployed service needs an agent round trip: the job snoozes for the poll interval.
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, work(), &snooze)
	require.Equal(t, pollInterval, snooze.Duration)
	id, ok := f.activeDrainDeployment(alpha)
	require.True(t, ok)

	// Once the deployment succeeded the same job finishes the remaining moves and ends.
	f.report(id, dst, agentprotocol.OutcomeSucceeded, "")
	require.NoError(t, work())
	state, _ := f.drainState(src)
	require.Equal(t, "drained", state)
	require.Equal(t, dst, f.serviceServer(idle))

	// Working a drain that is gone or finished is a no-op, not an error.
	require.NoError(t, work())
	require.NoError(t, w.Work(context.Background(), &river.Job[DrainArgs]{Args: DrainArgs{DrainID: uuid.New()}}))

	// Errors from a step are returned so River retries.
	f.exec(`UPDATE server_drains SET state='running', moves='{}' WHERE id=$1`, v.ID)
	require.ErrorContains(t, work(), "decode drain moves")
}

func TestDrainJobIdentityAndRegistration(t *testing.T) {
	require.Equal(t, "drain_server", DrainArgs{}.Kind(), "queued jobs are matched by this name")
	require.Equal(t, drainMaxAttempts, DrainArgs{}.InsertOpts().MaxAttempts)

	// Starting a drain queues its job with the drain id, inside the same transaction.
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	_, err := f.start(src, false, alpha, uuid.New())
	require.Error(t, err)
	require.Zero(t, f.count(`SELECT count(*) FROM river_job WHERE kind='drain_server'`), "a refused start queues nothing")
	v := f.mustStart(src, alpha, dst)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM river_job WHERE kind='drain_server' AND args->>'drainId'=$1 AND max_attempts=$2`, v.ID.String(), drainMaxAttempts))
}

func TestActorContextCarriesTheInitiatorAndOnlyTheScopesAMoveNeeds(t *testing.T) {
	user, tokenID, ws := uuid.New(), uuid.New(), uuid.New()
	p, ok := auth.PrincipalFrom(actorCtx(context.Background(), db.ServerDrain{
		ActorType: auth.TypeUser, ActorID: user.String(), ActorLabel: "Ada", WorkspaceID: ws,
	}))
	require.True(t, ok)
	require.Equal(t, user, p.UserID)
	require.Nil(t, p.TokenID)
	require.Equal(t, "Ada", p.Label)
	require.Equal(t, ws, p.WorkspaceID)
	require.ElementsMatch(t, []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite}, p.Scopes)

	p, _ = auth.PrincipalFrom(actorCtx(context.Background(), db.ServerDrain{ActorType: auth.TypeToken, ActorID: tokenID.String(), WorkspaceID: ws}))
	require.NotNil(t, p.TokenID)
	require.Equal(t, tokenID, *p.TokenID)
	require.Equal(t, uuid.Nil, p.UserID)

	p, _ = auth.PrincipalFrom(actorCtx(context.Background(), db.ServerDrain{ActorType: auth.TypeToken, ActorID: "not-a-uuid", WorkspaceID: ws}))
	require.Nil(t, p.TokenID)
	require.Equal(t, uuid.Nil, p.UserID)
}

func TestMovePairsListsServiceAndTarget(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	pairs := movePairs([]storedMove{{Move: Move{ServiceID: a, TargetServerID: b}}})
	require.Equal(t, []map[string]string{{"serviceId": a.String(), "targetServerId": b.String()}}, pairs)
	require.Empty(t, movePairs(nil))
}

// httpDo sends one request through the registered fleet routes as p (nil: not signed in).
func (f *fx) httpDo(p *auth.Principal, method, path string, body any) (int, []byte) {
	f.t.Helper()
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
	Register(humachi.New(r, cfg), f.svc)
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *fx) owned() *auth.Principal {
	p, _ := auth.PrincipalFrom(f.ctx)
	return p
}

func jsonOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	return m
}

func TestDrainEndpoints(t *testing.T) {
	f := newFx(t)
	src, dst := f.server("src", 8*gib, 1*gib), f.server("dst", 8*gib, 1*gib)
	alpha := f.app("alpha", src, 0)
	f.deployed(alpha, src)
	o := f.owned()
	base := "/servers/" + src.String()

	// Never drained: the drain is null, not an error.
	code, raw := f.httpDo(o, http.MethodGet, base+"/drain", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	body := jsonOf(t, raw)
	drain, present := body["drain"]
	require.True(t, present)
	require.Nil(t, drain)

	// Plan: a dry run that picks the only other server.
	code, raw = f.httpDo(o, http.MethodPost, base+"/drain/plan", map[string]any{})
	require.Equal(t, http.StatusOK, code, string(raw))
	mv := jsonOf(t, raw)["moves"].([]any)
	require.Len(t, mv, 1)
	require.Equal(t, dst.String(), mv[0].(map[string]any)["targetServerId"])
	code, raw = f.httpDo(o, http.MethodPost, base+"/drain/plan", map[string]any{"targetServerId": src.String()})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	require.Equal(t, "invalid_target", jsonOf(t, raw)["code"])
	require.Zero(t, f.count(`SELECT count(*) FROM server_drains`), "planning changes nothing")

	// Start answers 202 with the running drain; the second start conflicts.
	startBody := map[string]any{"moves": []map[string]any{{"serviceId": alpha.String(), "targetServerId": dst.String()}}}
	code, raw = f.httpDo(o, http.MethodPost, base+"/drain", startBody)
	require.Equal(t, http.StatusAccepted, code, string(raw))
	started := jsonOf(t, raw)
	require.Equal(t, "running", started["state"])
	require.Len(t, started["moves"], 1)
	code, raw = f.httpDo(o, http.MethodPost, base+"/drain", startBody)
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Equal(t, "drain_in_progress", jsonOf(t, raw)["code"])

	code, raw = f.httpDo(o, http.MethodGet, base+"/drain", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, started["id"], jsonOf(t, raw)["drain"].(map[string]any)["id"])

	code, raw = f.httpDo(o, http.MethodPost, base+"/undrain", nil)
	require.Equal(t, http.StatusConflict, code, string(raw))

	code, raw = f.httpDo(o, http.MethodPost, base+"/drain/cancel", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, true, jsonOf(t, raw)["cancelRequested"])
	f.run(uuid.MustParse(started["id"].(string)), nil)

	code, raw = f.httpDo(o, http.MethodPost, base+"/drain/cancel", nil)
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Equal(t, "no_drain_in_progress", jsonOf(t, raw)["code"])
	code, raw = f.httpDo(o, http.MethodPost, base+"/undrain", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Equal(t, "none", jsonOf(t, raw)["drainState"])

	// Unknown server, no sign-in, missing scope, project-restricted token.
	code, _ = f.httpDo(o, http.MethodGet, "/servers/"+uuid.NewString()+"/drain", nil)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = f.httpDo(nil, http.MethodGet, base+"/drain", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = f.httpDo(nil, http.MethodGet, "/fleet", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	ro := f.token([]string{policy.ScopeServersRead, policy.ScopeProjectsRead}, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, base + "/drain"}, {http.MethodPost, base + "/drain/cancel"}, {http.MethodPost, base + "/undrain"},
	} {
		code, raw = f.httpDo(ro, c.method, c.path, map[string]any{"moves": []any{}})
		require.Equal(t, http.StatusForbidden, code, c.path+" "+string(raw))
	}
	restricted := f.token(policy.AllScopes, []uuid.UUID{f.prj.ID})
	code, _ = f.httpDo(restricted, http.MethodPost, base+"/drain/plan", map[string]any{})
	require.Equal(t, http.StatusForbidden, code)
	code, _ = f.httpDo(restricted, http.MethodGet, base+"/drain", nil)
	require.Equal(t, http.StatusForbidden, code)
}

func TestFleetOverviewCommitmentsStatusesAndVisibility(t *testing.T) {
	f := newFx(t)
	a, b := f.server("a", 4*gib, 1*gib), f.server("b", 8*gib, 1*gib)
	x, y, z := f.app("x", a, 3072), f.app("y", a, 2048), f.app("z", a, 0)
	w := f.app("w", b, 512)
	f.exec(`UPDATE servers SET status='offline' WHERE id=$1`, b)

	o := f.owned()
	code, raw := f.httpDo(o, http.MethodGet, "/fleet", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	byName := map[string]map[string]any{}
	for _, s := range jsonOf(t, raw)["servers"].([]any) {
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
	require.Equal(t, "online", sa["status"])
	require.Equal(t, "offline", sb["status"])
	require.Equal(t, false, sb["overcommitted"])
	require.Equal(t, float64(512*(1<<20)), sb["committedMemBytes"])
	tot := jsonOf(t, raw)["totals"].(map[string]any)
	require.Equal(t, float64(2), tot["servers"])
	require.Equal(t, float64(4), tot["services"])
	require.Equal(t, float64(12*gib), tot["memTotalBytes"])
	require.Equal(t, float64(5*gib+512*(1<<20)), tot["committedMemBytes"])
	for _, s := range sa["services"].([]any) {
		require.Equal(t, "undeployed", s.(map[string]any)["status"], "a service that never deployed")
	}

	// A silent server reads offline even before the sweep marked it.
	f.exec(`UPDATE servers SET status='online', last_seen_at=now() - interval '1 hour' WHERE id=$1`, b)
	ov, err := f.svc.Overview(f.ctx)
	require.NoError(t, err)
	for _, s := range ov.Servers {
		if s.Name == "b" {
			require.Equal(t, "offline", s.Status)
		}
	}
	f.exec(`UPDATE servers SET status='online', last_seen_at=now() WHERE id=$1`, b)

	// The status of a service condenses the deployment and the observed container.
	f.deployed(x, a)
	f.deployed(y, a)
	f.deployed(w, b)
	status := func() map[string]string {
		ov, err := f.svc.Overview(f.ctx)
		require.NoError(t, err)
		out := map[string]string{}
		for _, s := range ov.Servers {
			for _, sv := range s.Services {
				out[sv.Name] = sv.Status
			}
		}
		return out
	}
	observe := func(id uuid.UUID, obs string) {
		if obs == "" {
			f.exec(`UPDATE services SET observed=NULL WHERE id=$1`, id)
			return
		}
		f.exec(`UPDATE services SET observed=$2::jsonb WHERE id=$1`, id, obs)
	}
	require.Equal(t, "unknown", status()["x"], "deployed but no container report yet")
	for obs, want := range map[string]string{
		`{"state":"running"}`:                      "running",
		`{"state":"running","health":"unhealthy"}`: "unhealthy",
		`{"state":"restarting"}`:                   "unhealthy",
		`{"state":"missing"}`:                      "missing",
		`{"state":"exited"}`:                       "stopped",
		`{"state":""}`:                             "unknown",
		`not json`:                                 "unknown",
	} {
		if obs == "not json" {
			f.exec(`UPDATE services SET observed='"x"'::jsonb WHERE id=$1`, x)
		} else {
			observe(x, obs)
		}
		require.Equal(t, want, status()["x"], obs)
	}
	observe(x, `{"state":"running"}`)
	f.exec(`UPDATE servers SET status='offline' WHERE id=$1`, a)
	require.Equal(t, "unknown", status()["x"], "nothing is known about a container on an offline server")
	f.exec(`UPDATE servers SET status='online' WHERE id=$1`, a)

	// Deploying and failed deployments override what the container says.
	dep, err := f.deps.Deploy(f.ctx, x, "", "")
	require.NoError(t, err)
	require.Equal(t, "deploying", status()["x"])
	f.report(dep.ID, a, agentprotocol.OutcomeFailed, "nope")
	require.Equal(t, "failed", status()["x"])
	require.Equal(t, "undeployed", status()["z"])
	_ = z

	// Service rows only show projects the token may see; server numbers stay whole.
	other, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Other", Slug: "other"})
	require.NoError(t, err)
	restricted := f.token([]string{policy.ScopeServersRead}, []uuid.UUID{other.ID})
	code, raw = f.httpDo(restricted, http.MethodGet, "/fleet", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	for _, s := range jsonOf(t, raw)["servers"].([]any) {
		m := s.(map[string]any)
		require.Empty(t, m["services"], "no visible services")
		if m["name"] == "a" {
			require.Equal(t, float64(5*gib), m["committedMemBytes"])
		}
	}
	require.NotZero(t, jsonOf(t, raw)["totals"].(map[string]any)["servers"])
	visible := f.token([]string{policy.ScopeServersRead}, []uuid.UUID{f.prj.ID})
	ov, err = f.svc.Overview(auth.WithPrincipal(context.Background(), visible))
	require.NoError(t, err)
	n := 0
	for _, s := range ov.Servers {
		n += len(s.Services)
	}
	require.Equal(t, 4, n)

	// Without the servers scope there is no overview.
	code, _ = f.httpDo(f.token([]string{policy.ScopeProjectsRead}, nil), http.MethodGet, "/fleet", nil)
	require.Equal(t, http.StatusForbidden, code)
}

func TestDrainPlanWarnsAboutStatefulStagedAndUndeployedServices(t *testing.T) {
	f := newFx(t)
	src, big, small := f.server("src", 8*gib, 2*gib), f.server("big", 16*gib, 2*gib), f.server("small", 8*gib, 6*gib)
	web, data := f.app("web", src, 0), f.app("data", src, 1024)
	f.app("idle", src, 0)
	_, err := f.svcs.AddVolume(f.ctx, data, "data", "/data")
	require.NoError(t, err)
	f.deployed(web, src)
	f.deployed(data, src)
	f.exec(`UPDATE servers SET public_ip='203.0.113.1' WHERE id=$1`, src)
	f.exec(`UPDATE servers SET public_ip='203.0.113.9' WHERE id=$1`, big)
	f.exec(`INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, 'web.example.com', 80)`, web)

	plan, err := f.svc.Plan(f.ctx, src, nil)
	require.NoError(t, err)
	require.Len(t, plan.Moves, 3)
	by := map[string]PlanMove{}
	for _, m := range plan.Moves {
		by[m.ServiceName] = m
	}
	require.True(t, by["data"].Stateful)
	require.Equal(t, "data", plan.Moves[2].ServiceName, "stateful services move last")
	require.Equal(t, big, *by["web"].TargetServerID, "the server with the most free memory is chosen")
	require.Contains(t, by["data"].Warnings, "Volumes are not copied. data will start with empty volumes on big; move data first.")
	require.Contains(t, by["web"].Warnings, "big was chosen automatically: it has the most free memory (14 GiB free after this move).")
	require.Contains(t, by["web"].Warnings, "web has no memory limit and no usage data, so the capacity check cannot account for it.")
	require.Contains(t, by["web"].Warnings, "web has 1 domain(s): point their A records to big (203.0.113.9) after the move.")
	require.Contains(t, by["idle"].Warnings, "idle has not been deployed yet: only its placement changes, nothing is started on big.")
	require.True(t, plan.Capacity.TargetFits)

	// Staged changes and an in-flight deployment are called out.
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, MemoryMB: 128}
	_, err = f.svcs.Update(f.ctx, web, mustGeneration(f, web), services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	_, err = f.deps.Deploy(f.ctx, data, "", "")
	require.NoError(t, err)
	plan, err = f.svc.Plan(f.ctx, src, nil)
	require.NoError(t, err)
	by = map[string]PlanMove{}
	for _, m := range plan.Moves {
		by[m.ServiceName] = m
	}
	require.Contains(t, by["web"].Warnings, "web has staged changes; they are deployed together with the move.")
	require.Contains(t, by["data"].Warnings, "A deployment of data is in progress; the drain waits for it to finish.")

	// An explicit target that is silent is planned but blocks; the source being offline is a warning.
	f.exec(`UPDATE servers SET last_seen_at=now() - interval '1 hour' WHERE id=$1`, small)
	plan, err = f.svc.Plan(f.ctx, src, &small)
	require.NoError(t, err)
	for _, m := range plan.Moves {
		require.Len(t, m.Blocking, 1, m.ServiceName)
		require.Equal(t, BlockTargetOffline, m.Blocking[0].Code)
	}
	f.exec(`UPDATE servers SET last_seen_at=now() - interval '1 hour' WHERE id=$1`, src)
	plan, err = f.svc.Plan(f.ctx, src, nil)
	require.NoError(t, err)
	for _, m := range plan.Moves {
		if m.ServiceName == "web" {
			require.Contains(t, m.Warnings, "src is offline: its old container cannot be stopped until the agent reconnects.")
		}
	}

	// A drain cannot be started onto an offline target, but is planned.
	f.exec(`UPDATE servers SET last_seen_at=now() WHERE id=$1`, src)
	_, err = f.svc.Plan(f.ctx, uuid.New(), nil)
	requireAPIError(t, err, 404, "not_found")
	f.setDrain(big, "draining")
	f.setDrain(small, "draining")
	plan, err = f.svc.Plan(f.ctx, src, nil)
	require.NoError(t, err)
	for _, m := range plan.Moves {
		require.Nil(t, m.TargetServerID)
		require.Equal(t, BlockNoTarget, m.Blocking[0].Code)
	}
}

func mustGeneration(f *fx, id uuid.UUID) int64 {
	g, _ := f.generation(id)
	return g
}
