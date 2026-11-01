package proxyroutes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/reconciler"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeDisp struct{ cmds []agents.NewCommand }

func (f *fakeDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	f.cmds = append(f.cmds, c)
	return uuid.New(), nil
}

func (f *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	box     *secretbox.Box
	disp    *fakeDisp
	svcs    *services.Service
	doms    *domains.Service
	routes  *proxyroutes.Service
	deps    *deployments.Service
	env     db.Environment
	project db.Project
	server  db.Server
	ws      db.Workspace
	admin   context.Context // every scope
	noProxy context.Context // projects/servers scopes but no proxy:write
	other   context.Context // another workspace
}

func principal(ws, user uuid.UUID, scopes []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Label: "t", Scopes: scopes})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	ws2, err := q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	u2, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws2.ID, Email: "b@example.com", Name: "B", PasswordHash: "x"})
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
	disp := &fakeDisp{}
	svcs := services.NewService(pool, bus, box, disp, logger)
	doms := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, doms, logger)
	routes := proxyroutes.NewService(pool, bus, jm, disp, svcs, logger)
	svcs.SetRouteHook(routes)
	deps := deployments.NewService(deployments.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Dispatcher: disp, Services: svcs, Logger: logger})
	return &fixture{
		t: t, pool: pool, q: q, box: box, disp: disp, svcs: svcs, doms: doms, routes: routes, deps: deps, env: env, project: prj, server: srv, ws: ws,
		admin:   principal(ws.ID, u.ID, policy.AllScopes),
		noProxy: principal(ws.ID, u.ID, []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeServersRead}),
		other:   principal(ws2.ID, u2.ID, policy.AllScopes),
	}
}

// web inserts and "deploys" a managed service on the fixture server.
func (f *fixture) web(slug string) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	f.markDeployed(sv)
	return sv
}

// markDeployed gives sv a succeeded deployment on the fixture server whose
// release snapshots the service's current config.
func (f *fixture) markDeployed(sv db.Service) {
	f.t.Helper()
	bg := context.Background()
	cfg := services.DecodeConfig(sv.Config)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg, ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: sv.SourceType, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
}

func (f *fixture) reloadService(id uuid.UUID) db.Service {
	f.t.Helper()
	row, err := f.q.GetServiceScopeInternal(context.Background(), id)
	require.NoError(f.t, err)
	return row.Service
}

func apiCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae), "want an API error, got %v", err)
	return ae.Status, ae.Code
}

func (f *fixture) addPort(ctx context.Context, in proxyroutes.PortRouteInput) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.InTx(ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		id, err = proxyroutes.AddPortRouteTx(ctx, tx, in)
		return err
	})
	return id, err
}

func TestAddPortRouteTx(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	in := proxyroutes.PortRouteInput{
		EnvironmentID: f.env.ID, TargetServiceID: web.ID, Name: "db public", Protocol: "tcp", ListenPort: 25432, TargetPort: 5432,
		AllowCIDRs: []string{"203.0.113.0/24"},
	}
	id, err := f.addPort(f.admin, in)
	require.NoError(t, err)

	row := f.reloadService(id)
	require.Equal(t, services.KindRoute, row.Kind)
	require.Equal(t, services.SourceExternal, row.SourceType)
	require.Equal(t, int64(1), row.Generation)
	require.EqualValues(t, 0, row.AppliedGeneration, "staged, not deployed")
	rc := services.DecodeConfig(row.Config).Route
	require.NotNil(t, rc)
	require.Equal(t, 25432, rc.ListenPort)
	require.Equal(t, web.ID, *rc.Upstreams[0].ServiceID)

	used, err := proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.NoError(t, err)
	require.True(t, used)
	used, err = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "udp", 25432)
	require.NoError(t, err)
	require.False(t, used, "ports are per protocol")

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'route.create' AND resource_id = $1`, id.String()).Scan(&n))
	require.Equal(t, 1, n)

	// Conflict: a typed error naming the holder, surfaced as 409 port_in_use.
	_, err = f.addPort(f.admin, in)
	var pe *proxyroutes.PortInUseError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, "tcp", pe.Protocol)
	require.Equal(t, 25432, pe.Port)
	require.Equal(t, f.server.ID, pe.ServerID)
	require.Equal(t, "db public", pe.HeldBy)
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "port_in_use", code)

	// The same port over udp is a different registry entry.
	udp := in
	udp.Protocol, udp.AllowCIDRs, udp.OpenToInternet, udp.Name = "udp", nil, true, "db udp"
	_, err = f.addPort(f.admin, udp)
	require.NoError(t, err)
}

func TestAddPortRouteTxRejectsReservedPortsAndMissingScope(t *testing.T) {
	f := newFixture(t)
	web := f.web("db")
	in := proxyroutes.PortRouteInput{
		EnvironmentID: f.env.ID, TargetServiceID: web.ID, Protocol: "tcp", ListenPort: 443, TargetPort: 5432, OpenToInternet: true,
	}
	_, err := f.addPort(f.admin, in)
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_listen_port", code)
	require.Error(t, proxyroutes.ValidateListenPort(80))
	require.Error(t, proxyroutes.ValidateListenPort(22))
	require.NoError(t, proxyroutes.ValidateListenPort(25565))

	in.ListenPort = 25565
	_, err = f.addPort(f.noProxy, in)
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusForbidden, status, "proxy:write is required")

	_, err = f.addPort(f.other, in)
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status, "another workspace cannot see the environment")

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM route_ports`).Scan(&n))
	require.Equal(t, 0, n)
}

func TestCreateRouteUpstreamPolicyAndValidation(t *testing.T) {
	f := newFixture(t)
	base := func() proxyroutes.CreateInput {
		up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
		return proxyroutes.CreateInput{
			Name: "nas", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "nas.example.com"}},
			RouteFields: proxyroutes.RouteFields{Upstreams: &up},
		}
	}
	for _, addr := range []string{"127.0.0.1:8080", "169.254.169.254:80", "localhost:80", "nebulactrl-traefik:8080"} {
		in := base()
		in.Name = "bad " + addr
		up := []proxyroutes.UpstreamInput{{Address: addr}}
		in.Upstreams = &up
		_, err := f.routes.Create(f.admin, f.env.ID, in)
		status, code := apiCode(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, status, addr)
		require.Equal(t, "upstream_denied", code, addr)
	}

	v, err := f.routes.Create(f.admin, f.env.ID, base())
	require.NoError(t, err)
	require.Equal(t, services.KindRoute, v.Kind)
	require.Len(t, v.Domains, 1)
	require.Equal(t, 0, v.Domains[0].TargetPort)
	require.Equal(t, "nas.example.com", v.Domains[0].Hostname)
	require.NotNil(t, v.Config.Route)

	// Duplicate hostname.
	dup := base()
	dup.Name = "again"
	_, err = f.routes.Create(f.admin, f.env.ID, dup)
	_, code := apiCode(t, err)
	require.Equal(t, "domain_conflict", code)

	// tcp needs a restriction or an explicit "open to the internet".
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:5432"}}
	port := 25432
	tcp := proxyroutes.CreateInput{Name: "pg", ServerID: f.server.ID, Protocol: "tcp", RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: &port}}
	_, err = f.routes.Create(f.admin, f.env.ID, tcp)
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_config", code)

	open := true
	tcp.OpenToInternet = &open
	_, err = f.routes.Create(f.admin, f.env.ID, tcp)
	require.NoError(t, err)

	tcp.Name = "pg2"
	_, err = f.routes.Create(f.admin, f.env.ID, tcp)
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "port_in_use", code)

	// UDP cannot carry an allowlist.
	udp := proxyroutes.CreateInput{Name: "dns", ServerID: f.server.ID, Protocol: "udp"}
	cidrs := []string{"10.0.0.0/8"}
	p53 := 5353
	udp.RouteFields = proxyroutes.RouteFields{Upstreams: &up, ListenPort: &p53, AllowCIDRs: &cidrs}
	_, err = f.routes.Create(f.admin, f.env.ID, udp)
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_config", code)

	// A managed upstream must live on the route's server.
	web := f.web("api")
	managed := []proxyroutes.UpstreamInput{{ServiceID: &web.ID, Port: 80}}
	mr := proxyroutes.CreateInput{Name: "api tcp", ServerID: f.server.ID, Protocol: "tcp", RouteFields: proxyroutes.RouteFields{Upstreams: &managed, ListenPort: new(26000), OpenToInternet: &open}}
	_, err = f.routes.Create(f.admin, f.env.ID, mr)
	require.NoError(t, err)

	// Scope.
	_, err = f.routes.Create(f.noProxy, f.env.ID, base())
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusForbidden, status)
}

func TestCreateRouteRequiresNewAgent(t *testing.T) {
	f := newFixture(t)
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux"})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, old)
	require.NoError(t, err)
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
	_, err = f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
		Name: "nas", ServerID: f.server.ID, Protocol: "http", RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	})
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "agent_too_old", code)

	// Domain options need the new agent too, a plain domain does not.
	web := f.web("site")
	_, err = f.doms.Create(f.admin, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.doms.Create(f.admin, web.ID, "p.example.com", 0, domains.RuleInput{PathPrefix: new("/api")})
	_, code = apiCode(t, err)
	require.Equal(t, "agent_too_old", code)
}

func TestUpdateRouteIfMatchAndPortRegistry(t *testing.T) {
	f := newFixture(t)
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:5432"}}
	open := true
	v, err := f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
		Name: "pg", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &up, ListenPort: new(25432), OpenToInternet: &open},
	})
	require.NoError(t, err)

	_, err = f.routes.Update(f.admin, v.ID, v.Generation+5, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(25433)}})
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusPreconditionFailed, status)
	require.Equal(t, "generation_conflict", code)

	u, err := f.routes.Update(f.admin, v.ID, v.Generation, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(25433)}})
	require.NoError(t, err)
	require.Equal(t, v.Generation+1, u.Generation)
	require.Equal(t, 25433, u.Config.Route.ListenPort)
	used, _ := proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25432)
	require.False(t, used, "the old port is released")
	used, _ = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25433)
	require.True(t, used)

	// A no-op patch keeps the generation.
	same, err := f.routes.Update(f.admin, v.ID, u.Generation, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{ListenPort: new(25433)}})
	require.NoError(t, err)
	require.Equal(t, u.Generation, same.Generation)

	// Upstream policy applies on update too.
	bad := []proxyroutes.UpstreamInput{{Address: "127.0.0.1:5432"}}
	_, err = f.routes.Update(f.admin, v.ID, u.Generation, proxyroutes.UpdateInput{RouteFields: proxyroutes.RouteFields{Upstreams: &bad}})
	_, code = apiCode(t, err)
	require.Equal(t, "upstream_denied", code)

	// IDOR: another workspace sees nothing.
	_, err = f.routes.Get(f.other, v.ID)
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)
	_, err = f.routes.Update(f.other, v.ID, u.Generation, proxyroutes.UpdateInput{Name: new("x")})
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Error(t, f.routes.Delete(f.other, v.ID))

	// Ports listing.
	ports, err := f.routes.ServerPorts(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Len(t, ports, 1)
	require.Equal(t, 25433, ports[0].Port)
	require.Equal(t, "pg", ports[0].ServiceName)
	require.False(t, ports[0].Deployed)
	_, err = f.routes.ServerPorts(f.other, f.server.ID)
	require.Error(t, err)

	// Delete frees the port; a never-deployed route sends nothing to the agent.
	require.NoError(t, f.routes.Delete(f.admin, v.ID))
	used, _ = proxyroutes.PortInUse(context.Background(), f.q, f.server.ID, "tcp", 25433)
	require.False(t, used)
	require.Empty(t, f.disp.cmds)
}

func TestDeleteDeployedRouteSyncsRemainingTable(t *testing.T) {
	f := newFixture(t)
	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
	v, err := f.routes.Create(f.admin, f.env.ID, proxyroutes.CreateInput{
		Name: "nas", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "nas.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(v.ID))
	web := f.web("site")
	_, err = f.doms.Create(f.admin, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)

	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 2)

	// The generic service delete does the same as DELETE /routes/{id}.
	require.NoError(t, f.svcs.Delete(f.admin, v.ID, services.DeleteInput{Confirm: v.Slug}))
	require.Len(t, f.disp.cmds, 1)
	cmd := f.disp.cmds[0]
	require.Equal(t, agentprotocol.KindSyncRoutes, cmd.Kind, "a route has no container: no remove_service")
	var pl agentprotocol.SyncRoutesPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &pl))
	require.Len(t, pl.Routes, 1)
	require.Equal(t, web.ID.String(), pl.Routes[0].ServiceID)
	require.NotNil(t, pl.Proxy)
}

func TestDomainRulesValidationAndPendingChanges(t *testing.T) {
	f := newFixture(t)
	web := f.web("site")
	ctx := f.admin
	d, err := f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	require.Equal(t, "serve", d.Kind)
	require.Equal(t, 308, d.RedirectCode)
	require.Equal(t, "auto", d.TLSMode)
	require.Empty(t, d.AuthUsers)

	gen := f.reloadService(web.ID).Generation
	patch := func(in domains.UpdateInput) (services.DomainView, error) {
		v, g, err := f.doms.Update(ctx, d.ID, gen, in)
		if err == nil && g > gen {
			gen = g
		}
		return v, err
	}

	_, _, err = f.doms.Update(ctx, d.ID, gen+3, domains.UpdateInput{Rule: domains.RuleInput{Options: &agentprotocol.RouteOptions{Compress: true}}})
	status, code := apiCode(t, err)
	require.Equal(t, http.StatusPreconditionFailed, status)
	require.Equal(t, "generation_conflict", code)

	for name, tc := range map[string]struct {
		in   domains.RuleInput
		code string
	}{
		"bad path":             {domains.RuleInput{PathPrefix: new("api/")}, "invalid_path_prefix"},
		"strip without path":   {domains.RuleInput{StripPrefix: new(true)}, "invalid_path_prefix"},
		"self redirect":        {domains.RuleInput{Kind: new("redirect"), RedirectTo: new("site.example.com")}, "invalid_redirect"},
		"redirect no host":     {domains.RuleInput{Kind: new("redirect"), RedirectTo: new("not a host")}, "invalid_redirect"},
		"bad header":           {domains.RuleInput{Options: &agentprotocol.RouteOptions{RequestHeaders: []agentprotocol.Header{{Name: "Host", Value: "x"}}}}, "invalid_options"},
		"bad cidr":             {domains.RuleInput{Options: &agentprotocol.RouteOptions{Access: &agentprotocol.Access{AllowCIDRs: []string{"nope"}}}}, "invalid_options"},
		"bad tls":              {domains.RuleInput{TLSMode: new("bogus")}, "invalid_tls_mode"},
		"basic auth w/o users": {domains.RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}, "basic_auth_needs_users"},
	} {
		_, err := patch(domains.UpdateInput{Rule: tc.in})
		status, code := apiCode(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, status, name)
		require.Equal(t, tc.code, code, name)
	}

	// Certificate pin must cover the hostname and belong to the server.
	covers := f.insertCert("wild", []string{"*.example.com"})
	other := f.insertCert("other", []string{"*.other.com"})
	_, err = patch(domains.UpdateInput{Rule: domains.RuleInput{TLSMode: new("custom"), CertificateID: new(other.String())}})
	_, code = apiCode(t, err)
	require.Equal(t, "certificate_mismatch", code)
	_, err = patch(domains.UpdateInput{Rule: domains.RuleInput{TLSMode: new("auto"), CertificateID: new(covers.String())}})
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_tls_mode", code)
	v, err := patch(domains.UpdateInput{Rule: domains.RuleInput{TLSMode: new("custom"), CertificateID: new(covers.String())}})
	require.NoError(t, err)
	require.Equal(t, covers, *v.CertificateID)

	// Path, strip, options.
	opts := agentprotocol.RouteOptions{Compress: true, RateLimit: &agentprotocol.RateLimit{Average: 10, PeriodSeconds: 1, Burst: 20}}
	v, err = patch(domains.UpdateInput{Rule: domains.RuleInput{PathPrefix: new("/api"), StripPrefix: new(true), Options: &opts}})
	require.NoError(t, err)
	require.Equal(t, "/api", v.PathPrefix)
	require.True(t, v.StripPrefix)
	require.True(t, v.Options.Compress)
	require.Equal(t, 20, v.Options.RateLimit.Burst)

	// Snapshot the "released" state (after the path change) to compare pending changes with.
	row, err := f.q.GetDomainByID(context.Background(), d.ID)
	require.NoError(t, err)
	rel := db.Release{Config: mustJSON(services.Snapshot{Config: services.DecodeConfig(web.Config), Domains: []services.SnapshotDomain{services.SnapshotDomainOf(row, nil)}})}

	// Same host+path conflicts, a different path does not.
	_, err = f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{PathPrefix: new("/api")})
	_, code = apiCode(t, err)
	require.Equal(t, "domain_conflict", code)
	_, err = f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{PathPrefix: new("/admin")})
	require.NoError(t, err)

	// Basic auth: users first, then the option; hashes never leave.
	names, err := f.doms.SetAuthUsers(ctx, d.ID, []domains.AuthUserInput{{Username: "alice", Password: "correct horse"}, {Username: "bob", Password: "battery staple"}})
	require.NoError(t, err)
	require.Equal(t, []string{"alice", "bob"}, names)
	_, err = f.doms.SetAuthUsers(ctx, d.ID, []domains.AuthUserInput{{Username: "carol"}})
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_auth_users", code, "a new user needs a password")
	_, err = f.doms.SetAuthUsers(ctx, d.ID, []domains.AuthUserInput{{Username: "bad name", Password: "longenough"}})
	_, code = apiCode(t, err)
	require.Equal(t, "invalid_auth_users", code)
	_, err = f.doms.SetAuthUsers(f.noProxy, d.ID, []domains.AuthUserInput{{Username: "alice"}})
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var enc []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT hash_enc FROM domain_auth_users WHERE domain_id=$1 AND username='alice'`, d.ID).Scan(&enc))
	require.NotContains(t, string(enc), "$2", "stored sealed")

	gen = f.reloadService(web.ID).Generation // creating a domain and setting users advanced it
	opts.BasicAuth = true
	v, err = patch(domains.UpdateInput{Rule: domains.RuleInput{Options: &opts}})
	require.NoError(t, err)
	require.Equal(t, []string{"alice", "bob"}, v.AuthUsers)
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), "$2a$")

	// The last user cannot be removed while basic auth is on; others can.
	require.NoError(t, f.doms.DeleteAuthUser(ctx, d.ID, "bob"))
	err = f.doms.DeleteAuthUser(ctx, d.ID, "alice")
	_, code = apiCode(t, err)
	require.Equal(t, "basic_auth_needs_users", code)
	err = f.doms.DeleteAuthUser(ctx, d.ID, "nobody")
	status, _ = apiCode(t, err)
	require.Equal(t, http.StatusNotFound, status)

	// Auth secrets are attached at send time only, from the rule's AuthRef.
	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	rt := routes[0]
	require.Len(t, rt.Domains, 2)
	raw, _ = json.Marshal(rt)
	require.NotContains(t, string(raw), "$2a$", "route tables carry no hashes")
	var authRule agentprotocol.DomainRule
	for _, r := range rt.Domains {
		if r.ID == d.ID.String() {
			authRule = r
		}
	}
	require.Equal(t, agentprotocol.SecretAuthPrefix+d.ID.String(), authRule.AuthRef)
	secrets, err := domains.AuthSecrets(context.Background(), f.q, f.box, routes)
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^alice:\$2[aby]\$10\$`), secrets[authRule.AuthRef])
	require.Len(t, secrets, 1)

	// Pending changes show per-field domain diffs and auth user names.
	cur, err := services.LoadCurrentState(context.Background(), f.q, f.box, f.reloadService(web.ID))
	require.NoError(t, err)
	changes, err := services.DiffRelease(context.Background(), f.q, f.box, rel, cur)
	require.NoError(t, err)
	fields := map[string]services.FieldChange{}
	for _, c := range changes {
		fields[c.Field] = c
	}
	require.Contains(t, fields, "domains.site.example.com/api.options")
	require.Contains(t, fields, "domains.site.example.com/api.authUsers")
	require.Equal(t, "alice", fields["domains.site.example.com/api.authUsers"].To, "bob was removed")
	require.Equal(t, "added", fields["domains.site.example.com/admin"].To, "a path variant is a separate domain")
	require.NotContains(t, fields, "domains.site.example.com/api", "the released domain is still attached")
	require.NotContains(t, fields["domains.site.example.com/api.options"].To, "$2a$")
}

func TestRedirectLoopDetection(t *testing.T) {
	f := newFixture(t)
	web := f.web("site")
	ctx := f.admin
	_, err := f.doms.Create(ctx, web.ID, "example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	www, err := f.doms.Create(ctx, web.ID, "www.example.com", 0, domains.RuleInput{Kind: new("redirect"), RedirectTo: new("example.com")})
	require.NoError(t, err)
	require.Equal(t, "redirect", www.Kind)
	require.Equal(t, "example.com", www.RedirectTo)

	// example.com → www.example.com would close the loop.
	apex, err := f.q.GetServiceDomainByKey(context.Background(), db.GetServiceDomainByKeyParams{ServiceID: web.ID, Hostname: "example.com"})
	require.NoError(t, err)
	_, _, err = f.doms.Update(ctx, apex.ID, f.reloadService(web.ID).Generation, domains.UpdateInput{Rule: domains.RuleInput{Kind: new("redirect"), RedirectTo: new("www.example.com")}})
	_, code := apiCode(t, err)
	require.Equal(t, "redirect_loop", code)

	// A redirect domain renders as a redirect rule on the service's route.
	f.markDeployed(f.reloadService(web.ID))
	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	var red agentprotocol.DomainRule
	for _, r := range routes[0].Domains {
		if r.Host == "www.example.com" {
			red = r
		}
	}
	require.Equal(t, "redirect", red.Kind)
	require.Equal(t, "example.com", red.RedirectTo)
	require.Equal(t, 308, red.RedirectCode)
	require.Equal(t, []string{"example.com", "www.example.com"}, routes[0].Hosts)
}

func (f *fixture) insertCert(name string, names []string) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `
		INSERT INTO certificates (workspace_id, server_id, name, domains, cert_pem, key_enc, fingerprint, not_before, not_after)
		VALUES ($1, $2, $3, $4, 'pem', '\x00', 'fp', now() - interval '1 day', now() + interval '60 days') RETURNING id`,
		f.ws.ID, f.server.ID, name, names).Scan(&id))
	return id
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// containerRe matches nc-<slug>-<deployment id8>-<replica>: the deployment prefix changes per run.
	containerRe = regexp.MustCompile(`(nc-[a-z0-9-]+?)-[0-9a-f]{8}-([0-9]+:)`)
	// envNetRe matches nebulactrl-env-<environment id8> (optionally followed by -int): the environment ID is random per run.
	envNetRe = regexp.MustCompile(`nebulactrl-env-[0-9a-f]{8}`)
)

// normalize replaces every UUID (and the deployment prefix of container names
// and the environment prefix of network names) with a stable placeholder in
// order of first appearance.
func normalize(raw []byte) []byte {
	seen := map[string]string{}
	out := uuidRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		k := string(m)
		if _, ok := seen[k]; !ok {
			seen[k] = "<id" + string(rune('A'+len(seen))) + ">"
		}
		return []byte(seen[k])
	})
	out = envNetRe.ReplaceAll(out, []byte("nebulactrl-env-<envNet>"))
	return containerRe.ReplaceAll(out, []byte("$1-<dep>-$2"))
}

func TestServerRoutesGolden(t *testing.T) {
	f := newFixture(t)
	ctx := f.admin
	bg := context.Background()

	web := f.web("web")
	for _, in := range []struct {
		host string
		rule domains.RuleInput
	}{
		{"app.example.com", domains.RuleInput{Options: &agentprotocol.RouteOptions{Compress: true, SecurityHeaders: "basic"}}},
		{"www.example.com", domains.RuleInput{Kind: new("redirect"), RedirectTo: new("app.example.com"), RedirectCode: new(301)}},
		{"app.example.com", domains.RuleInput{PathPrefix: new("/api"), StripPrefix: new(true), AddPrefix: new("/v1")}},
	} {
		_, err := f.doms.Create(ctx, web.ID, in.host, 0, in.rule)
		require.NoError(t, err)
	}
	f.markDeployed(f.reloadService(web.ID))

	open := true
	nas := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8443"}}
	ext, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "nas", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "nas.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &nas, Scheme: new("https"), InsecureSkipVerify: &open, HealthPath: new("/health")},
	})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(ext.ID))

	pg := []proxyroutes.UpstreamInput{{ServiceID: &web.ID, Port: 5432}}
	cidrs := []string{"203.0.113.0/24"}
	tcp, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "pg", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &pg, ListenPort: new(25432), AllowCIDRs: &cidrs, ProxyProtocol: new(2)},
	})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(tcp.ID))

	dns := []proxyroutes.UpstreamInput{{Address: "10.0.0.5:53"}}
	udp, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "dns", ServerID: f.server.ID, Protocol: "udp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &dns, ListenPort: new(5353), OpenToInternet: &open},
	})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(udp.ID))

	// A route whose managed target is not deployed on the server is left out.
	idle := f.web("idle")
	_, err = f.pool.Exec(bg, `UPDATE services SET current_deployment_id = NULL WHERE id = $1`, idle.ID)
	require.NoError(t, err)
	orphan := []proxyroutes.UpstreamInput{{ServiceID: &idle.ID, Port: 80}}
	o, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "orphan", ServerID: f.server.ID, Protocol: "tcp",
		RouteFields: proxyroutes.RouteFields{Upstreams: &orphan, ListenPort: new(26000), OpenToInternet: &open},
	})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(o.ID))
	_, err = f.pool.Exec(bg, `UPDATE services SET current_deployment_id = NULL WHERE id = $1`, idle.ID)
	require.NoError(t, err)

	routes, err := domains.ServerRoutes(bg, f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 4, "service, external http, tcp and udp; the orphan has no upstream")

	// Excluding a service drops its route; overrides repoint managed upstreams.
	without, err := domains.ServerRoutes(bg, f.q, f.server.ID, tcp.ID)
	require.NoError(t, err)
	require.Len(t, without, 3)
	over, err := domains.ServerRoutesWith(bg, f.q, f.server.ID, web.ID, domains.Overrides{web.ID: {Slug: "web", DeploymentID: "11111111-2222-3333-4444-555555555555", Replicas: 2}})
	require.NoError(t, err)
	var tcpRoute agentprotocol.Route
	for _, r := range over {
		if r.Protocol == "tcp" {
			tcpRoute = r
		}
	}
	require.Len(t, tcpRoute.Upstreams, 2, "the L4 route follows the new deployment's replicas")
	require.Contains(t, tcpRoute.Upstreams[0], "11111111")

	got, err := json.MarshalIndent(routes, "", "  ")
	require.NoError(t, err)
	got = append(normalize(got), '\n')
	path := "testdata/server_routes.golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run with UPDATE_GOLDEN=1 to create the golden file")
	require.Equal(t, string(want), string(got))
}

func TestSecretsForCommandAttachesAuthSecrets(t *testing.T) {
	f := newFixture(t)
	ctx := f.admin
	web := f.web("site")
	d, err := f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.doms.SetAuthUsers(ctx, d.ID, []domains.AuthUserInput{{Username: "alice", Password: "correct horse"}})
	require.NoError(t, err)
	_, _, err = f.doms.Update(ctx, d.ID, f.reloadService(web.ID).Generation, domains.UpdateInput{Rule: domains.RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(web.ID))

	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	payload, err := json.Marshal(agentprotocol.SyncRoutesPayload{Routes: routes, Proxy: &agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}})
	require.NoError(t, err)
	require.NotContains(t, string(payload), "$2a$", "the persisted payload never carries hashes")

	cmd, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, Kind: agentprotocol.KindSyncRoutes, Payload: payload, IdempotencyKey: "k1", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	secrets, err := f.deps.SecretsForCommand(context.Background(), cmd.ID, agentprotocol.KindSyncRoutes, payload)
	require.NoError(t, err)
	require.Regexp(t, `^alice:\$2[aby]\$`, secrets[agentprotocol.SecretAuthPrefix+d.ID.String()])

	// proxy_configure without routes attaches the secrets of the whole table.
	pc, _ := json.Marshal(agentprotocol.ProxyConfigurePayload{Proxy: agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}})
	cmd2, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, Kind: agentprotocol.KindProxyConfigure, Payload: pc, IdempotencyKey: "k2", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	secrets, err = f.deps.SecretsForCommand(context.Background(), cmd2.ID, agentprotocol.KindProxyConfigure, pc)
	require.NoError(t, err)
	require.Contains(t, secrets, agentprotocol.SecretAuthPrefix+d.ID.String())

	// Provisioning a tailnet key without a minter fails the send instead of leaking a half command.
	prov, _ := json.Marshal(agentprotocol.ProxyConfigurePayload{Proxy: agentprotocol.ProxyConfig{Tailnet: &agentprotocol.TailnetSpec{Mode: "managed", Provision: true}}})
	cmd3, err := f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, Kind: agentprotocol.KindProxyConfigure, Payload: prov, IdempotencyKey: "k3", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	_, err = f.deps.SecretsForCommand(context.Background(), cmd3.ID, agentprotocol.KindProxyConfigure, prov)
	require.Error(t, err)
}

func TestReconcileCarriesExternalRoutesProxyAndSecrets(t *testing.T) {
	f := newFixture(t)
	ctx := f.admin
	web := f.web("site")
	d, err := f.doms.Create(ctx, web.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.doms.SetAuthUsers(ctx, d.ID, []domains.AuthUserInput{{Username: "alice", Password: "correct horse"}})
	require.NoError(t, err)
	_, _, err = f.doms.Update(ctx, d.ID, f.reloadService(web.ID).Generation, domains.UpdateInput{Rule: domains.RuleInput{Options: &agentprotocol.RouteOptions{BasicAuth: true}}})
	require.NoError(t, err)

	up := []proxyroutes.UpstreamInput{{Address: "192.168.1.20:8080"}}
	rv, err := f.routes.Create(ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "nas", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "nas.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	})
	require.NoError(t, err)
	f.markDeployed(f.reloadService(rv.ID))

	rec := reconciler.New(f.pool, events.NewBus(), nil, f.deps, nil)
	got, err := rec.DesiredState(context.Background(), f.server.ID)
	require.NoError(t, err)
	require.Len(t, got.Services, 1, "a route service has no container")
	require.Equal(t, web.ID.String(), got.Services[0].ServiceID)
	require.Len(t, got.Services[0].Routes, 1)
	require.Len(t, got.ExternalRoutes, 1)
	require.Equal(t, rv.ID.String(), got.ExternalRoutes[0].ServiceID)
	require.Equal(t, agentprotocol.RouteKindExternal, got.ExternalRoutes[0].Kind)
	require.NotNil(t, got.Proxy)
	require.Regexp(t, `^alice:\$2`, got.Secrets[agentprotocol.SecretAuthPrefix+d.ID.String()])
}
