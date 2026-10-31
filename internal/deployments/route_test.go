package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type recDisp struct{ cmds []agents.NewCommand }

func (f *recDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	f.cmds = append(f.cmds, c)
	return uuid.New(), nil
}

func (f *recDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type routeFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ctx    context.Context
	disp   *recDisp
	svcs   *services.Service
	doms   *domains.Service
	routes *proxyroutes.Service
	deps   *Service
	env    db.Environment
	server db.Server
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $2 WHERE id = $1", srv.ID, facts)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	disp := &recDisp{}
	svcs := services.NewService(pool, bus, box, disp, logger)
	doms := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, doms, logger)
	routes := proxyroutes.NewService(pool, bus, jm, disp, svcs, logger)
	svcs.SetRouteHook(routes)
	deps := NewService(Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Dispatcher: disp, Services: svcs, Logger: logger})
	RegisterJobs(jm, deps)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: policy.AllScopes})
	return &routeFixture{t: t, pool: pool, q: q, ctx: ctx, disp: disp, svcs: svcs, doms: doms, routes: routes, deps: deps, env: env, server: srv}
}

// web inserts a managed service on the server with a succeeded deployment.
func (f *routeFixture) web(slug string) db.Service {
	f.t.Helper()
	bg := context.Background()
	raw, _ := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(sv.Config), ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: "traefik/whoami:latest"})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: "traefik/whoami:latest"})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	return sv
}

func (f *routeFixture) lastCommand(kind string) agents.NewCommand {
	f.t.Helper()
	for i := len(f.disp.cmds) - 1; i >= 0; i-- {
		if f.disp.cmds[i].Kind == kind {
			return f.disp.cmds[i]
		}
	}
	f.t.Fatalf("no %s command was enqueued", kind)
	return agents.NewCommand{}
}

// deployRoute deploys a route service end to end against the fake agent: it
// prepares the deployment, delivers the agent's route event and success result.
func (f *routeFixture) deployRoute(id uuid.UUID) (uuid.UUID, agents.NewCommand) {
	f.t.Helper()
	v, err := f.deps.Deploy(f.ctx, id, "", "")
	require.NoError(f.t, err)
	require.Equal(f.t, StrategyRouteOnly, v.Strategy)
	require.NoError(f.t, f.deps.runDeploy(context.Background(), v.ID, ""))
	cmd := f.lastCommand(agentprotocol.KindSyncRoutes)
	f.finish(v.ID)
	return v.ID, cmd
}

func (f *routeFixture) finish(depID uuid.UUID) {
	f.t.Helper()
	require.NoError(f.t, f.deps.HandleEvent(context.Background(), f.server.ID, agentprotocol.EventFrame{DeploymentID: depID.String(), Step: "route", Status: agentprotocol.StepStarted}))
	data, _ := json.Marshal(agentprotocol.ProxyConfigureResult{Outcome: agentprotocol.OutcomeSucceeded})
	require.NoError(f.t, f.deps.HandleResult(context.Background(), f.server.ID, uuid.New(), &depID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
}

func TestRouteOnlyDeployEndToEnd(t *testing.T) {
	f := newRouteFixture(t)
	ctx := f.ctx
	web := f.web("site")

	// A protected domain on the managed service: its hash must never be persisted.
	d, err := f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.doms.SetAuthUsers(ctx, d.ID, []domains.AuthUserInput{{Username: "alice", Password: "correct horse"}})
	require.NoError(t, err)
	gen := func() int64 {
		row, err := f.q.GetServiceScopeInternal(context.Background(), web.ID)
		require.NoError(t, err)
		return row.Service.Generation
	}
	_, _, err = f.doms.Update(ctx, d.ID, gen(), domains.UpdateInput{Rule: domains.RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}})
	require.NoError(t, err)
	require.NoError(t, f.q.MarkServiceDeployed(context.Background(), db.MarkServiceDeployedParams{ID: web.ID, CurrentDeploymentID: new(f.currentDeployment(web.ID)), AppliedGeneration: gen()}))

	// An external http route without access control.
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
	rv, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "nas", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "nas.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	})
	require.NoError(t, err)

	plan, err := f.deps.Plan(ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, StrategyRouteOnly, plan.Strategy)
	require.Empty(t, plan.Blocking)
	require.Equal(t, services.SourceExternal, plan.Source.Type)
	joined := ""
	for _, w := range plan.Warnings {
		joined += w + "\n"
	}
	require.Contains(t, joined, "no access control")
	require.Contains(t, joined, "routing, not access rules")
	require.NotContains(t, joined, "health check", "container warnings do not apply")

	depID, cmd := f.deployRoute(rv.ID)

	// The persisted payload is a full table plus proxy config; no hashes or keys.
	require.Equal(t, "deploy:"+depID.String(), cmd.IdempotencyKey)
	require.NotNil(t, cmd.DeploymentID)
	require.NotContains(t, string(cmd.Payload), "$2a$")
	require.NotContains(t, string(cmd.Payload), "PRIVATE KEY")
	var pl agentprotocol.SyncRoutesPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &pl))
	require.Equal(t, depID.String(), pl.DeploymentID)
	require.NotNil(t, pl.Proxy)
	require.Len(t, pl.Routes, 2, "the managed service's route and the new external route")
	var ext agentprotocol.Route
	for _, r := range pl.Routes {
		if r.ServiceID == rv.ID.String() {
			ext = r
		}
	}
	require.Equal(t, agentprotocol.RouteKindExternal, ext.Kind)
	require.Equal(t, []string{"192.168.1.20:8080"}, ext.Upstreams)
	require.Equal(t, []string{"nas.example.com"}, ext.Hosts)

	// Success promotes the route: current deployment, applied generation, active domains.
	row, err := f.q.GetServiceScopeInternal(context.Background(), rv.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Service.CurrentDeploymentID)
	require.Equal(t, depID, *row.Service.CurrentDeploymentID)
	require.Equal(t, row.Service.Generation, row.Service.AppliedGeneration)
	doms, err := f.q.ListServiceDomains(context.Background(), rv.ID)
	require.NoError(t, err)
	require.Equal(t, "active", doms[0].Status)
	deps, err := f.q.ListDeploymentsByIDs(context.Background(), []uuid.UUID{depID})
	require.NoError(t, err)
	dep := deps[0]
	require.Equal(t, "succeeded", dep.Status)
	require.Equal(t, StrategyRouteOnly, dep.Strategy)
	rel, err := f.q.GetReleaseByID(context.Background(), *dep.ReleaseID)
	require.NoError(t, err)
	require.Equal(t, services.SourceExternal, services.DecodeSource(rel.Source).Type)
	require.Equal(t, "nas.example.com", services.DecodeSnapshot(rel.Config).Domains[0].Hostname)

	// Send time: the table now carries the managed service's auth rule; the
	// secrets (and only they) hold the hash.
	pl2cmd, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, Kind: agentprotocol.KindSyncRoutes, Payload: cmd.Payload, IdempotencyKey: "x", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	secrets, err := f.deps.SecretsForCommand(context.Background(), pl2cmd.ID, agentprotocol.KindSyncRoutes, cmd.Payload)
	require.NoError(t, err)
	require.Contains(t, secrets[agentprotocol.SecretAuthPrefix+d.ID.String()], "alice:$2")

	// Edit the upstream, redeploy, then roll back: the old table is re-sent.
	up2 := []proxyroutes.UpstreamInput{{Address: "192.168.1.21:8080"}}
	rv, err = f.routes.Update(ctx, rv.ID, rv.Generation, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{Upstreams: &up2}})
	require.NoError(t, err)
	plan, err = f.deps.Plan(ctx, rv.ID)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Changes)
	dep2, cmd2 := f.deployRoute(rv.ID)
	require.Contains(t, string(cmd2.Payload), "192.168.1.21:8080")

	_, err = f.deps.Rollback(ctx, depID)
	require.NoError(t, err)
	var rb uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM deployments WHERE service_id = $1 AND trigger = 'rollback'`, rv.ID).Scan(&rb))
	require.NotEqual(t, dep2, rb)
	require.NoError(t, f.deps.runDeploy(context.Background(), rb, ""))
	cmd3 := f.lastCommand(agentprotocol.KindSyncRoutes)
	require.Contains(t, string(cmd3.Payload), "192.168.1.20:8080")
	require.NotContains(t, string(cmd3.Payload), "192.168.1.21:8080")
}

func (f *routeFixture) currentDeployment(id uuid.UUID) uuid.UUID {
	f.t.Helper()
	row, err := f.q.GetServiceScopeInternal(context.Background(), id)
	require.NoError(f.t, err)
	require.NotNil(f.t, row.Service.CurrentDeploymentID)
	return *row.Service.CurrentDeploymentID
}

func TestRoutePlanWarningsAndBlocking(t *testing.T) {
	f := newRouteFixture(t)
	ctx := f.ctx
	open := true
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:5432"}}
	tcp, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "pg", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(25432), OpenToInternet: &open},
	})
	require.NoError(t, err)
	udp, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "dns", ServerID: f.server.ID, Protocol: "udp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(5353), OpenToInternet: &open},
	})
	require.NoError(t, err)

	has := func(ws []string, sub string) bool {
		for _, w := range ws {
			if bytes.Contains([]byte(w), []byte(sub)) {
				return true
			}
		}
		return false
	}
	p, err := f.deps.Plan(ctx, tcp.ID)
	require.NoError(t, err)
	require.True(t, has(p.Warnings, "open to the internet"), "%v", p.Warnings)
	require.True(t, has(p.Warnings, "New TCP port 25432 restarts the proxy on agent1"), "%v", p.Warnings)
	require.True(t, has(p.Warnings, "bypasses host firewalls"), "%v", p.Warnings)
	p, err = f.deps.Plan(ctx, udp.ID)
	require.NoError(t, err)
	require.True(t, has(p.Warnings, "UDP routes cannot be restricted by IP"), "%v", p.Warnings)
	require.True(t, has(p.Warnings, "New UDP port 5353 restarts the proxy"), "%v", p.Warnings)

	// Once the port is published the restart warning goes away.
	f.deployRoute(tcp.ID)
	p, err = f.deps.Plan(ctx, tcp.ID)
	require.NoError(t, err)
	require.False(t, has(p.Warnings, "restarts the proxy"), "%v", p.Warnings)

	// An http route without a domain, and a managed target that is not deployed, block the deploy.
	nodom, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "nodom", ServerID: f.server.ID, Protocol: "http", RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	})
	require.NoError(t, err)
	p, err = f.deps.Plan(ctx, nodom.ID)
	require.NoError(t, err)
	require.Len(t, p.Blocking, 1)
	require.Equal(t, "route_needs_domain", p.Blocking[0].Code)

	idle := f.web("idle")
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET current_deployment_id = NULL WHERE id = $1`, idle.ID)
	require.NoError(t, err)
	mup := []proxyroutes.UpstreamInput{{ServiceID: &idle.ID, Port: 80}}
	managed, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "managed", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &mup, ListenPort: new(26000), OpenToInternet: &open},
	})
	require.NoError(t, err)
	p, err = f.deps.Plan(ctx, managed.ID)
	require.NoError(t, err)
	require.Len(t, p.Blocking, 1)
	require.Equal(t, "upstream_not_deployed", p.Blocking[0].Code)
	_, err = f.deps.Deploy(ctx, managed.ID, "", "")
	require.Error(t, err)

	// A service that uses domain options needs a proxy-v2 agent.
	web := f.web("site")
	_, err = f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{PathPrefix: new("/api")})
	require.NoError(t, err)
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux"})
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, old)
	require.NoError(t, err)
	p, err = f.deps.Plan(ctx, web.ID)
	require.NoError(t, err)
	found := false
	for _, b := range p.Blocking {
		found = found || b.Code == "agent_too_old"
	}
	require.True(t, found, "%v", p.Blocking)
}

func TestDeployStagedOrdersRoutesAfterTheirTargets(t *testing.T) {
	f := newRouteFixture(t)
	ctx := f.ctx
	open := true
	// A target that has staged changes and a route that points at it.
	web := f.web("db")
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET generation = generation + 1 WHERE id = $1`, web.ID)
	require.NoError(t, err)
	up := []proxyroutes.UpstreamInput{{ServiceID: &web.ID, Port: 5432}}
	rv, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "aaa public db", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(25432), OpenToInternet: &open},
	})
	require.NoError(t, err)

	res, err := f.deps.DeployStaged(ctx, f.env.ID, nil, "")
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{web.ID, rv.ID}, res.Order, "the route deploys after the service it exposes")
}

// TestDeployStagedAcceptsARouteToANeverDeployedTargetInTheBatch: installing a
// template with a published port stages the service and its route together
// and deploys them explicitly. The route's target is not deployed yet, but it
// is in the batch and deploys first, so the route is not blocked (it would be
// outside the batch).
func TestDeployStagedAcceptsARouteToANeverDeployedTargetInTheBatch(t *testing.T) {
	f := newRouteFixture(t)
	ctx := f.ctx
	open := true
	target := f.web("game")
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET current_deployment_id = NULL, applied_generation = 0, generation = 1 WHERE id = $1`, target.ID)
	require.NoError(t, err)
	up := []proxyroutes.UpstreamInput{{ServiceID: &target.ID, Port: 80}}
	rv, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "game port", ServerID: f.server.ID, Protocol: "udp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(27015), OpenToInternet: &open},
	})
	require.NoError(t, err)

	// The route alone, with its target outside the batch, stays blocked.
	_, err = f.deps.DeployStaged(ctx, f.env.ID, []uuid.UUID{rv.ID}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not deployed")

	// Together they deploy, the target first.
	res, err := f.deps.DeployStaged(ctx, f.env.ID, []uuid.UUID{target.ID, rv.ID}, "")
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{target.ID, rv.ID}, res.Order)
	require.Len(t, res.Items, 2)
}
