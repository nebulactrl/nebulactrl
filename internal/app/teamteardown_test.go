package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/statuspages"
	"github.com/nebulactrl/nebulactrl/internal/teams"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type teardownFx struct {
	t    *testing.T
	q    *db.Queries
	pool *pgxpool.Pool
	ws   db.Workspace
	ctx  context.Context
	td   *teamTeardown
}

func newTeardownFx(t *testing.T) (*teardownFx, *db.Queries, db.Workspace, context.Context) {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(context.Background(), "doomed")
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	cfg := &config.Config{SecretKey: bytes.Repeat([]byte{7}, 32)}
	hub := agents.NewHub(pool, bus, jm, cfg, logger)
	ts := tailscale.NewService(pool, box, jm, logger)
	td := &teamTeardown{
		q: q, projects: projects.NewService(pool, bus), servers: servers.NewService(pool, bus, hub, cfg, logger),
		pages: statuspages.NewService(statuspages.Deps{Pool: pool, Bus: bus, Logger: logger}), cloudflare: cloudflare.NewService(pool, box, jm, logger), tailscale: ts,
		platform: platform.NewService(platform.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Logger: logger}),
	}
	// The principal the team deletion job acts as.
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeSystem, WorkspaceID: ws.ID, Label: "Team deletion", Scopes: []string{policy.ScopeAdmin}})
	return &teardownFx{t: t, q: q, pool: pool, ws: ws, ctx: ctx, td: td}, q, ws, ctx
}

func TestTeardownIntegrationsThatWereNeverConnectedAreFine(t *testing.T) {
	f, _, _, ctx := newTeardownFx(t)
	require.NoError(t, f.td.DisconnectCloudflare(ctx, false), "no Cloudflare connection: nothing to disconnect")
	require.NoError(t, f.td.DisconnectCloudflare(ctx, true))
	require.NoError(t, f.td.DisconnectTailscale(ctx), "no tailnet integration: nothing to disconnect")
	require.NoError(t, f.td.DeleteStatusPages(ctx), "no status pages")
	require.NoError(t, f.td.RemovePlatformServices(ctx), "no platform services")
}

func TestTeardownRemovesThePlatformServiceRowsOfTheTeam(t *testing.T) {
	f, q, ws, ctx := newTeardownFx(t)
	row, err := q.InsertPlatformService(ctx, db.InsertPlatformServiceParams{
		WorkspaceID: ws.ID, Kind: platform.KindHeadscale, ServiceIds: []uuid.UUID{}, Hostname: "hs.example.test", PublicUrl: "https://hs.example.test", Options: []byte(`{}`), ID: uuid.New(),
	})
	require.NoError(t, err)

	require.NoError(t, f.td.RemovePlatformServices(ctx))
	_, err = q.GetPlatformServiceInternal(ctx, row.ID)
	require.Error(t, err, "the platform service is gone")
}

func TestTeardownRemovesServersAndSaysWhetherTheirHostWasTouched(t *testing.T) {
	f, q, ws, ctx := newTeardownFx(t)
	srv, err := q.CreateServer(ctx, db.CreateServerParams{WorkspaceID: ws.ID, Name: "offline-1"})
	require.NoError(t, err)

	// An offline server is removed without touching its host.
	outcome, err := f.td.RemoveServer(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, teams.ServerForgotten, outcome)
	_, err = q.GetServerByID(ctx, srv.ID)
	require.Error(t, err)
	// Removing what is gone already is not an error.
	outcome, err = f.td.RemoveServer(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, teams.ServerForgotten, outcome)

	// The server the control plane runs on cannot be removed by deleting its team.
	cp, err := q.CreateServer(ctx, db.CreateServerParams{WorkspaceID: ws.ID, Name: "control-plane"})
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE servers SET is_control_plane = true WHERE id = $1`, cp.ID)
	require.NoError(t, err)
	_, err = f.td.RemoveServer(ctx, cp.ID)
	require.Error(t, err)
	_, err = q.GetServerByID(ctx, cp.ID)
	require.NoError(t, err, "it is still there")
}

func TestTeardownProjectDeletionNeedsTheCascadeWiring(t *testing.T) {
	f, q, ws, ctx := newTeardownFx(t)
	p, err := q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	err = f.td.DeleteProject(ctx, p.ID)
	require.ErrorContains(t, err, "not configured", "an unwired cascade fails loudly instead of skipping the project")
	_, err = q.GetProject(ctx, db.GetProjectParams{ID: p.ID, WorkspaceID: ws.ID})
	require.NoError(t, err)
}
