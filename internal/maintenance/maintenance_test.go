package maintenance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
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
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/maintenance"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeDispatcher struct{ cmds []agents.NewCommand }

func (f *fakeDispatcher) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	f.cmds = append(f.cmds, c)
	return uuid.New(), nil
}

func (f *fakeDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ctx    context.Context
	reader context.Context
	disp   *fakeDispatcher
	svc    *maintenance.Service
	env    db.Environment
	server db.Server
	bus    *events.Bus
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
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	disp := &fakeDispatcher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, disp, logger)
	p := func(scopes []string) context.Context {
		return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "alice@example.com", Scopes: scopes})
	}
	return &fixture{
		t: t, pool: pool, q: q, disp: disp, env: env, server: srv, bus: bus,
		ctx: p(policy.AllScopes), reader: p([]string{policy.ScopeProjectsRead}),
		svc: maintenance.NewService(pool, bus, disp, svcs, logger),
	}
}

// deployedWeb inserts a service with a domain and a succeeded current deployment.
func (f *fixture) deployedWeb(slug, host string) db.Service {
	f.t.Helper()
	bg := context.Background()
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults()
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	_, err = f.pool.Exec(bg, "UPDATE services SET applied_generation = generation WHERE id = $1", sv.ID)
	require.NoError(f.t, err)
	sv.AppliedGeneration = sv.Generation
	return sv
}

func (f *fixture) syncs() []agentprotocol.SyncRoutesPayload {
	var out []agentprotocol.SyncRoutesPayload
	for _, c := range f.disp.cmds {
		if c.Kind != agentprotocol.KindSyncRoutes {
			continue
		}
		var p agentprotocol.SyncRoutesPayload
		require.NoError(f.t, json.Unmarshal(c.Payload, &p))
		out = append(out, p)
	}
	return out
}

func (f *fixture) auditActions() []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT action FROM audit_events WHERE action LIKE 'service.maintenance.%' ORDER BY id")
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

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func TestMaintenanceOnOffSyncsRoutesAndAudits(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	eta := time.Now().Add(2 * time.Hour)

	v, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "  Upgrading  ", Message: "Back at <noon>", ETA: &eta})
	require.NoError(t, err)
	require.True(t, v.Maintenance.Enabled)
	require.Equal(t, "Upgrading", v.Maintenance.Title)
	require.Equal(t, "Back at <noon>", v.Maintenance.Message, "text is stored as typed; the page escapes it")
	require.NotNil(t, v.Maintenance.Since)
	require.True(t, v.Maintenance.ETA.Equal(eta.UTC().Truncate(time.Second)))
	require.Equal(t, sv.Generation, v.Generation, "an operational setting never stages a change")
	require.False(t, v.HasStagedChanges)

	// The route sync carries the flag.
	syncs := f.syncs()
	require.Len(t, syncs, 1)
	require.Len(t, syncs[0].Routes, 1)
	m := syncs[0].Routes[0].Maintenance
	require.NotNil(t, m)
	require.Equal(t, "Upgrading", m.Title)
	require.Equal(t, []string{"app.example.com"}, syncs[0].Routes[0].Hosts)
	require.Positive(t, syncs[0].TableSeq, "every table is numbered")
	require.Equal(t, []string{"service.maintenance.on"}, f.auditActions())

	// The same request again changes nothing.
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "Upgrading", Message: "Back at <noon>", ETA: &eta})
	require.NoError(t, err)
	require.Len(t, f.syncs(), 1)
	require.Equal(t, []string{"service.maintenance.on"}, f.auditActions())

	// Editing the text while on sends a table again, and keeps "since".
	v2, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "Still upgrading"})
	require.NoError(t, err)
	require.Equal(t, v.Maintenance.Since, v2.Maintenance.Since)
	require.Nil(t, v2.Maintenance.ETA)
	require.Len(t, f.syncs(), 2)

	// Off: the route has no flag, the text is kept, the ETA is dropped.
	v3, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: false})
	require.NoError(t, err)
	require.False(t, v3.Maintenance.Enabled)
	require.Equal(t, "Still upgrading", v3.Maintenance.Title)
	require.Nil(t, v3.Maintenance.Since)
	syncs = f.syncs()
	require.Len(t, syncs, 3)
	require.Nil(t, syncs[2].Routes[0].Maintenance)
	require.Less(t, syncs[0].TableSeq, syncs[1].TableSeq, "a later table has a higher number")
	require.Less(t, syncs[1].TableSeq, syncs[2].TableSeq)
	require.Equal(t, []string{"service.maintenance.on", "service.maintenance.on", "service.maintenance.off"}, f.auditActions())

	// Off again: a no-op.
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: false})
	require.NoError(t, err)
	require.Len(t, f.syncs(), 3)
}

func TestMaintenanceValidationAndScopes(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")

	_, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: strings.Repeat("x", 81)})
	requireAPIError(t, err, 422, "validation_failed")
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Message: strings.Repeat("x", 601)})
	requireAPIError(t, err, 422, "validation_failed")
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "bell\x07"})
	requireAPIError(t, err, 422, "validation_failed")
	past := time.Now().Add(-time.Hour)
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, ETA: &past})
	requireAPIError(t, err, 422, "validation_failed")
	require.Empty(t, f.syncs())
	require.Empty(t, f.auditActions())

	// A read-only principal cannot switch it, but can read the preview.
	_, err = f.svc.Set(f.reader, sv.ID, maintenance.SetInput{Enabled: true})
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Preview(f.reader, sv.ID, maintenance.PreviewInput{})
	require.NoError(t, err)

	// Unknown services are 404.
	_, err = f.svc.Set(f.ctx, uuid.New(), maintenance.SetInput{Enabled: true})
	requireAPIError(t, err, 404, "not_found")
}

func TestMaintenanceOnUndeployedServiceOnlyStores(t *testing.T) {
	f := newFixture(t)
	cfg, _ := json.Marshal(services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: "new", Slug: "new", Kind: services.KindApp, SourceType: services.SourceImage, Config: cfg, ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	v, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true})
	require.NoError(t, err)
	require.True(t, v.Maintenance.Enabled)
	require.Empty(t, f.syncs(), "no route exists yet")
	require.Equal(t, []string{"service.maintenance.on"}, f.auditActions())
}

func TestMaintenanceRejectsL4Routes(t *testing.T) {
	f := newFixture(t)
	cfg, _ := json.Marshal(services.Config{Route: &services.RouteConfig{Protocol: agentprotocol.ProtoTCP, ListenPort: 25565}})
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: "mc", Slug: "mc", Kind: services.KindRoute, SourceType: services.SourceExternal, Config: cfg,
	})
	require.NoError(t, err)
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true})
	requireAPIError(t, err, 422, "maintenance_unsupported")
}

func TestServerRoutesCarryMaintenanceFlag(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	other := f.deployedWeb("other", "other.example.com")
	_, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Message: "soon"})
	require.NoError(t, err)

	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 2)
	for _, r := range routes {
		switch r.ServiceID {
		case sv.ID.String():
			require.NotNil(t, r.Maintenance)
			require.Equal(t, "soon", r.Maintenance.Message)
		case other.ID.String():
			require.Nil(t, r.Maintenance)
		}
	}
}

func TestPreviewRendersEscapedTextAndOverrides(t *testing.T) {
	f := newFixture(t)
	sv := f.deployedWeb("web", "app.example.com")
	_, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "Stored", Message: "<b>bold</b>"})
	require.NoError(t, err)

	html, err := f.svc.Preview(f.ctx, sv.ID, maintenance.PreviewInput{})
	require.NoError(t, err)
	require.Contains(t, string(html), "Stored")
	require.Contains(t, string(html), "&lt;b&gt;bold&lt;/b&gt;")
	require.NotContains(t, string(html), "<b>bold</b>")
	require.Contains(t, string(html), "app.example.com")

	html, err = f.svc.Preview(f.ctx, sv.ID, maintenance.PreviewInput{Draft: true, Title: "Draft title"})
	require.NoError(t, err)
	require.Contains(t, string(html), "Draft title")
	require.NotContains(t, string(html), "Stored")
	require.Contains(t, string(html), "This service is being updated", "an empty draft message shows the default copy")
}

func TestMaintenanceChangesAreAnnounced(t *testing.T) {
	f := newFixture(t)
	rec := &notificationstest.Recorder{}
	f.svc.SetNotifier(rec)
	sv := f.deployedWeb("web", "app.example.com")
	eta := time.Now().Add(2 * time.Hour)

	_, err := f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "Upgrading", ETA: &eta})
	require.NoError(t, err)
	on := rec.OfType(notifications.TypeMaintenanceOn)
	require.Len(t, on, 1)
	require.Equal(t, sv.ID, *on[0].ServiceID)
	require.Equal(t, "web is in maintenance mode", on[0].Title)
	require.Equal(t, "alice@example.com", on[0].Fields["Changed by"])
	require.NotEmpty(t, on[0].Fields["Expected back"])

	// An unchanged request is not a change: nothing more is announced.
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: true, Title: "Upgrading", ETA: &eta})
	require.NoError(t, err)
	require.Len(t, rec.Events(), 1)

	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: false})
	require.NoError(t, err)
	off := rec.OfType(notifications.TypeMaintenanceOff)
	require.Len(t, off, 1)
	require.Equal(t, "web is out of maintenance mode", off[0].Title)
	_, err = f.svc.Set(f.ctx, sv.ID, maintenance.SetInput{Enabled: false})
	require.NoError(t, err)
	require.Len(t, rec.Events(), 2)
}
