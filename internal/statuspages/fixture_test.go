package statuspages

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// fakeRoutes records route pushes (and can fail them).
type fakeRoutes struct {
	mu    sync.Mutex
	calls []uuid.UUID
	err   error
}

func (f *fakeRoutes) PushRoutes(_ context.Context, _ pgx.Tx, serverID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, serverID)
	return nil
}

func (f *fakeRoutes) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeResolver struct {
	addrs []string
	err   error
}

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]net.IPAddr, 0, len(f.addrs))
	for _, a := range f.addrs {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	return out, nil
}

// fx is a workspace with three services, a second workspace with one service,
// and a Service whose clock the test controls.
type fx struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	routes *fakeRoutes
	svc    *Service
	now    time.Time

	ctx      context.Context // owner of the workspace: every scope
	ws       uuid.UUID
	project  uuid.UUID
	api      uuid.UUID
	web      uuid.UUID
	database uuid.UUID
	foreign  uuid.UUID // a service of another workspace
	foreignP uuid.UUID // a status page of another workspace
}

// Names that must never reach a public page.
const (
	secretProject = "proj-secret"
	secretEnv     = "env-secret"
)

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()

	seed := func(name string, services ...string) (ws uuid.UUID, user uuid.UUID, project uuid.UUID, ids []uuid.UUID) {
		w, err := q.CreateWorkspace(bg, name)
		require.NoError(t, err)
		u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: w.ID, Email: name + "@example.com", Name: name, PasswordHash: "x"})
		require.NoError(t, err)
		prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: w.ID, Name: secretProject, Slug: "shop"})
		require.NoError(t, err)
		env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: secretEnv, Slug: "production", IsProduction: true})
		require.NoError(t, err)
		for _, s := range services {
			sv, err := q.InsertService(bg, db.InsertServiceParams{
				ID: uuid.New(), EnvironmentID: env.ID, Name: s, Slug: s, Kind: "app", SourceType: "image",
				Config: []byte(`{"image":"nginx:1","port":80}`),
			})
			require.NoError(t, err)
			ids = append(ids, sv.ID)
		}
		return w.ID, u.ID, prj.ID, ids
	}

	ws, user, project, ids := seed("acme", "svc-secret-api", "svc-secret-web", "svc-secret-db")
	ws2, _, _, ids2 := seed("other", "svc-secret-foreign")
	foreignPage, err := q.InsertStatusPage(bg, db.InsertStatusPageParams{WorkspaceID: ws2, Title: "Other", Slug: "other", Description: "", Published: false, ShowUptime: true, Theme: "auto"})
	require.NoError(t, err)

	f := &fx{
		t: t, pool: pool, q: q, bus: events.NewBus(), routes: &fakeRoutes{}, now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		ws: ws, project: project, api: ids[0], web: ids[1], database: ids[2], foreign: ids2[0], foreignP: foreignPage.ID,
	}
	f.svc = NewService(Deps{Pool: pool, Bus: f.bus, Routes: f.routes, Resolver: fakeResolver{}})
	f.svc.now = func() time.Time { return f.now }
	f.svc.started = f.now.Add(-24 * time.Hour)
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Role: "owner", Label: "owner@example.com", Scopes: policy.AllScopes})
	return f
}

// as returns a context for another principal of the workspace.
func (f *fx) as(scopes []string, projects []uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: uuid.New(), WorkspaceID: f.ws, Label: "token", Scopes: scopes, ProjectIDs: projects,
	})
}

// controlPlane adds the control-plane server with a public address.
func (f *fx) controlPlane(publicIP string) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	var ip *string
	if publicIP != "" {
		ip = &publicIP
	}
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`INSERT INTO servers (workspace_id, name, status, is_control_plane, public_ip) VALUES ($1, 'cp', 'online', true, $2) RETURNING id`,
		f.ws, ip).Scan(&id))
	return id
}

func (f *fx) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *fx) auditCount(action string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1`, action).Scan(&n))
	return n
}

// createPage creates a page and fails the test on error.
func (f *fx) createPage(title, hostname string) StatusPageView {
	f.t.Helper()
	v, err := f.svc.Create(f.ctx, PageCreate{Title: title, Hostname: hostname})
	require.NoError(f.t, err)
	return v
}

// components sets the page's components to the services, in order, with
// default names.
func (f *fx) components(page uuid.UUID, services ...uuid.UUID) StatusPageView {
	f.t.Helper()
	in := make([]ComponentInput, len(services))
	for i, s := range services {
		in[i] = ComponentInput{ServiceID: s}
	}
	v, err := f.svc.SetComponents(f.ctx, page, in)
	require.NoError(f.t, err)
	return v
}

// publish publishes a page.
func (f *fx) publish(page uuid.UUID) StatusPageView {
	f.t.Helper()
	yes := true
	v, err := f.svc.Update(f.ctx, page, PagePatch{Published: &yes})
	require.NoError(f.t, err)
	return v
}

// drain returns the status_page.updated events published so far.
func drain(ch <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}
