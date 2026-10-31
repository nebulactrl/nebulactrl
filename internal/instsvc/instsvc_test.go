package instsvc

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// fakeBackend stands in for SeaweedFS, the registry or Loki. It records what the
// service asked of it and answers from fields the tests set.
type fakeBackend struct {
	mu      sync.Mutex
	svc     *Service // set to check that the secret of a slice resolves while it is ensured
	ensured map[string][]ensureCall
	purged  []Slice
	usage   map[string]int64 // by Ref
	// failEnsure and failPurge make the calls fail while set.
	failEnsure, failPurge bool
	health                error
	secrets               map[string]string // Ref -> secret seen during EnsureSlice
}

type ensureCall struct {
	Slice Slice
	Quota int64
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{ensured: map[string][]ensureCall{}, usage: map[string]int64{}, secrets: map[string]string{}}
}

func (b *fakeBackend) EnsureSlice(ctx context.Context, s Slice, quota int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failEnsure {
		return errors.New("the service refused")
	}
	b.ensured[s.Ref] = append(b.ensured[s.Ref], ensureCall{s, quota})
	if b.svc != nil && s.Kind == KindS3 {
		sec, err := b.svc.Secret(ctx, s)
		if err != nil {
			return err
		}
		b.secrets[s.Ref] = sec
	}
	return nil
}

func (b *fakeBackend) Usage(_ context.Context, s Slice) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage[s.Ref], nil
}

func (b *fakeBackend) Purge(_ context.Context, s Slice) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failPurge {
		return errors.New("purge refused")
	}
	b.purged = append(b.purged, s)
	return nil
}

func (b *fakeBackend) Health(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.health
}

func (b *fakeBackend) calls(ref string) []ensureCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ensureCall(nil), b.ensured[ref]...)
}

func (b *fakeBackend) set(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f()
}

// emitter records notifications.
type emitter struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (e *emitter) Emit(_ context.Context, ev notifications.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *emitter) all() []notifications.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]notifications.Event(nil), e.events...)
}

type fakeAdmins struct{ admin uuid.UUID }

func (a fakeAdmins) IsInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) {
	return id == a.admin, nil
}

// team is a team with an owner and a server.
type team struct {
	ID     uuid.UUID
	Name   string
	User   uuid.UUID
	Server uuid.UUID
}

type fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *db.Queries
	svc   *Service
	s3    *fakeBackend
	reg   *fakeBackend
	loki  *fakeBackend
	notes *emitter
	jm    *jobs.Manager
	box   *secretbox.Box

	// a and b are teams that do not trust each other. The instance administrator
	// is a's owner; b's owner is a team owner who is not.
	a, b  team
	admin uuid.UUID
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)

	f := &fixture{t: t, pool: pool, q: db.New(pool), box: box, s3: newFakeBackend(), reg: newFakeBackend(), loki: newFakeBackend(),
		notes: &emitter{}, now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	f.jm = jobs.New(pool, nil)
	f.a = f.newTeam("Alpha", "alpha@example.com")
	f.b = f.newTeam("Beta", "beta@example.com")
	f.admin = f.a.User
	f.svc = New(Deps{
		Pool: pool, Box: box, Jobs: f.jm, Admins: fakeAdmins{admin: f.admin}, Notifier: f.notes,
		Backends:  map[string]Backend{KindS3: f.s3, KindRegistry: f.reg, KindLoki: f.loki},
		Endpoints: map[string]string{KindS3: "http://seaweedfs:8333", KindRegistry: "http://registry:5000", KindLoki: "http://loki:3100"},
	})
	f.svc.now = func() time.Time { return f.now }
	f.svc.refreshDelay = 0 // no background re-reads in tests
	f.s3.svc = f.svc
	RegisterJobs(f.jm, f.svc) // River only queues kinds that have a worker
	return f
}

func (f *fixture) newTeam(name, email string) team {
	f.t.Helper()
	ctx := context.Background()
	ws, err := f.q.CreateWorkspace(ctx, name)
	require.NoError(f.t, err)
	u, err := f.q.CreateUser(ctx, db.CreateUserParams{WorkspaceID: ws.ID, Email: email, Name: name + " owner", PasswordHash: "x"})
	require.NoError(f.t, err)
	srv, err := f.q.CreateServer(ctx, db.CreateServerParams{WorkspaceID: ws.ID, Name: name + " server"})
	require.NoError(f.t, err)
	return team{ID: ws.ID, Name: name, User: u.ID, Server: srv.ID}
}

// as returns a context for a team owner (a browser session: every scope).
func (f *fixture) as(tm team) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: tm.User, WorkspaceID: tm.ID, Label: tm.Name, Scopes: policy.AllScopes,
	})
}

// slice returns the team's slice row of a kind.
func (f *fixture) slice(tm team, kind string) db.TeamServiceSlice {
	f.t.Helper()
	r, err := f.q.GetTeamSlice(context.Background(), db.GetTeamSliceParams{TeamID: tm.ID, Kind: kind})
	require.NoError(f.t, err)
	return r
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// provision makes both teams' slices ready.
func (f *fixture) provision() {
	f.t.Helper()
	require.NoError(f.t, f.svc.Ensure(context.Background()))
}

// setQuota stores an exact quota (the HTTP minimum of 1 GiB does not apply to
// the service tests of the gate).
func (f *fixture) setQuota(tm team, quota int64) {
	f.t.Helper()
	row := f.slice(tm, KindS3)
	_, err := f.q.SetSliceQuota(context.Background(), db.SetSliceQuotaParams{ID: row.ID, QuotaBytes: &quota})
	require.NoError(f.t, err)
}

func (f *fixture) setUsed(tm team, used int64) {
	f.t.Helper()
	row := f.slice(tm, KindS3)
	require.NoError(f.t, f.q.SetSliceUsage(context.Background(), db.SetSliceUsageParams{ID: row.ID, UsedBytes: used}))
}

func uuidFrom(s string) uuid.UUID { return uuid.MustParse(s) }

func asPgErr(err error, target **pgconn.PgError) bool { return errors.As(err, target) }
