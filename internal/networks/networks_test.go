package networks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type team struct {
	ws      db.Workspace
	user    uuid.UUID
	project db.Project
	env     db.Environment
	server  db.Server
	ctx     context.Context
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	nets *networks.Service
	svcs *services.Service
	a, b team
}

func newTeam(t *testing.T, q *db.Queries, pool *pgxpool.Pool, name string) team {
	t.Helper()
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, name)
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: name + "@example.com", Name: name, PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: name + "-1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: name, Scopes: policy.AllScopes})
	return team{ws: ws, user: u.ID, project: prj, env: env, server: srv, ctx: ctx}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	bus := events.NewBus()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{
		t: t, pool: pool, q: q, nets: networks.NewService(pool, bus), svcs: services.NewService(pool, bus, box, nopDispatcher{}, logger),
		a: newTeam(t, q, pool, "alpha"), b: newTeam(t, q, pool, "beta"),
	}
}

type nopDispatcher struct{}

func (nopDispatcher) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (nopDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

// service inserts an app on the team's server (or none when server is nil).
func (f *fixture) service(tm team, slug string, server *uuid.UUID, cfg services.Config) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: tm.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: server,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) auditActions(tm team) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT action FROM audit_events WHERE workspace_id = $1 AND action LIKE 'network.%' ORDER BY id`, tm.ws.ID)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(f.t, rows.Scan(&a))
		out = append(out, a)
	}
	return out
}

func requireStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func TestAttachDetachStagesAndAudits(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.SharedNetworkName(n.ID.String()), n.DockerName)

	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	ref, err := f.nets.Attach(f.a.ctx, api.ID, n.ID)
	require.NoError(t, err)
	require.Equal(t, "api.production.shop", ref.Alias)

	// Attaching stages a change: the generation advanced past the applied one.
	sc, err := services.LoadScopeInternal(context.Background(), f.q, api.ID)
	require.NoError(t, err)
	require.Greater(t, sc.Service.Generation, sc.Service.AppliedGeneration)
	v, err := f.svcs.Get(f.a.ctx, api.ID)
	require.NoError(t, err)
	require.Len(t, v.Networks, 1)
	require.Equal(t, "Backend", v.Networks[0].Name)

	// A second attach is a no-op: the generation does not move again.
	gen := sc.Service.Generation
	_, err = f.nets.Attach(f.a.ctx, api.ID, n.ID)
	require.NoError(t, err)
	sc, _ = services.LoadScopeInternal(context.Background(), f.q, api.ID)
	require.Equal(t, gen, sc.Service.Generation)

	got, err := f.nets.Get(f.a.ctx, n.ID)
	require.NoError(t, err)
	require.Len(t, got.Services, 1)
	require.Equal(t, "api.production.shop", got.Services[0].Alias)

	// An attached network cannot be deleted.
	requireStatus(t, f.nets.Delete(f.a.ctx, n.ID), 409, "network_in_use")

	require.NoError(t, f.nets.Detach(f.a.ctx, api.ID, n.ID))
	requireStatus(t, f.nets.Detach(f.a.ctx, api.ID, n.ID), 404, "not_found")
	require.NoError(t, f.nets.Delete(f.a.ctx, n.ID))

	require.Equal(t, []string{"network.create", "network.attach", "network.detach", "network.delete"}, f.auditActions(f.a))
}

func TestAttachWrongServerIs422(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.a.ws.ID, Name: "alpha-2"})
	require.NoError(t, err)
	n, err := f.nets.Create(f.a.ctx, "Backend", other.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})

	_, err = f.nets.Attach(f.a.ctx, api.ID, n.ID)
	requireStatus(t, err, 422, "network_wrong_server")

	// A service without a server cannot attach either.
	unplaced := f.service(f.a, "worker", nil, services.Config{Image: "x:1"})
	_, err = f.nets.Attach(f.a.ctx, unplaced.ID, n.ID)
	requireStatus(t, err, 422, "service_no_server")

	// Moving an attached service to another server detaches the old network.
	home, err := f.nets.Create(f.a.ctx, "Home", f.a.server.ID)
	require.NoError(t, err)
	_, err = f.nets.Attach(f.a.ctx, api.ID, home.ID)
	require.NoError(t, err)
	v, err := f.svcs.Get(f.a.ctx, api.ID)
	require.NoError(t, err)
	_, err = f.svcs.Update(f.a.ctx, api.ID, v.Generation, services.UpdateInput{ServerID: &other.ID})
	require.NoError(t, err)
	v, err = f.svcs.Get(f.a.ctx, api.ID)
	require.NoError(t, err)
	require.Empty(t, v.Networks, "a shared network spans one server")
}

func TestCrossTeamIs404(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	bsvc := f.service(f.b, "api", &f.b.server.ID, services.Config{Image: "x:1", Port: 80})

	// Team B cannot see, attach, detach or delete team A's network.
	_, err = f.nets.Get(f.b.ctx, n.ID)
	requireStatus(t, err, 404, "not_found")
	_, err = f.nets.Attach(f.b.ctx, bsvc.ID, n.ID)
	requireStatus(t, err, 404, "not_found")
	requireStatus(t, f.nets.Detach(f.b.ctx, bsvc.ID, n.ID), 404, "not_found")
	requireStatus(t, f.nets.Delete(f.b.ctx, n.ID), 404, "not_found")
	list, err := f.nets.List(f.b.ctx, nil)
	require.NoError(t, err)
	require.Empty(t, list)

	// And team A cannot attach its network to team B's service.
	_, err = f.nets.Attach(f.a.ctx, bsvc.ID, n.ID)
	requireStatus(t, err, 404, "not_found")

	// Team A's server is not team B's to build networks on.
	_, err = f.nets.Create(f.b.ctx, "Mine", f.a.server.ID)
	requireStatus(t, err, 422, "server_not_found")
}

func TestScopes(t *testing.T) {
	f := newFixture(t)
	tok := func(scopes ...string) context.Context {
		return auth.WithPrincipal(context.Background(), &auth.Principal{
			Type: auth.TypeToken, UserID: f.a.user, TokenID: new(uuid.New()), WorkspaceID: f.a.ws.ID, Label: "ci", Scopes: scopes,
		})
	}
	// Create and delete need servers:write.
	_, err := f.nets.Create(tok(policy.ScopeServersRead, policy.ScopeDeploymentsWrite), "X", f.a.server.ID)
	requireStatus(t, err, 403, "forbidden")
	n, err := f.nets.Create(tok(policy.ScopeServersWrite), "X", f.a.server.ID)
	require.NoError(t, err)
	requireStatus(t, f.nets.Delete(tok(policy.ScopeServersRead), n.ID), 403, "forbidden")

	// Attach needs deployments:write.
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	_, err = f.nets.Attach(tok(policy.ScopeServersWrite, policy.ScopeProjectsWrite), api.ID, n.ID)
	requireStatus(t, err, 403, "forbidden")
	_, err = f.nets.Attach(tok(policy.ScopeDeploymentsWrite), api.ID, n.ID)
	require.NoError(t, err)

	// A token restricted to other projects neither sees the attachment nor attaches.
	restricted := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.a.user, TokenID: new(uuid.New()), WorkspaceID: f.a.ws.ID, Label: "ci",
		Scopes: policy.AllScopes, ProjectIDs: []uuid.UUID{uuid.New()},
	})
	_, err = f.nets.Attach(restricted, api.ID, n.ID)
	requireStatus(t, err, 404, "not_found")
	got, err := f.nets.Get(restricted, n.ID)
	require.NoError(t, err)
	require.Empty(t, got.Services)
	// Team-level changes are not for project-restricted tokens.
	_, err = f.nets.Create(restricted, "Y", f.a.server.ID)
	requireStatus(t, err, 403, "forbidden")
}

func TestBlockEgressAndSharedNetworksExclude(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	blocked := f.service(f.a, "db", &f.a.server.ID, services.Config{Image: "postgres:17", Port: 5432, BlockEgress: true})
	_, err = f.nets.Attach(f.a.ctx, blocked.ID, n.ID)
	requireStatus(t, err, 422, "egress_blocked")

	// Blocking egress on a service that is attached is refused as well.
	open := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	_, err = f.nets.Attach(f.a.ctx, open.ID, n.ID)
	require.NoError(t, err)
	v, err := f.svcs.Get(f.a.ctx, open.ID)
	require.NoError(t, err)
	cfg := v.Config
	block := true
	_, err = f.svcs.Update(f.a.ctx, open.ID, v.Generation, services.UpdateInput{Config: &cfg, BlockEgress: &block})
	requireStatus(t, err, 422, "egress_blocked")
}

func TestOutboundHints(t *testing.T) {
	got := networks.OutboundHints(map[string]string{
		"WEBHOOK_URL":         "https://hooks.slack.com/services/x",
		"STRIPE_SECRET_KEY":   "sk_test_123",
		"DATABASE_URL":        "postgres://u:p@db:5432/app",
		"REDIS_URL":           "redis://cache:6379",
		"API_BASE":            "http://10.0.0.5:8080",
		"LOCAL":               "http://localhost:3000",
		"SENTRY_DSN":          "",
		"PORT":                "8080",
		"NEBULA_PRIVATE_HOST": "api",
		"UPSTREAM":            "https://api.example.com/v1",
	})
	require.Equal(t, []string{"STRIPE_SECRET_KEY", "UPSTREAM", "WEBHOOK_URL"}, got)
}
