package uptime

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

type fakeResync struct {
	mu      sync.Mutex
	servers []uuid.UUID
}

func (r *fakeResync) Resync(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers = append(r.servers, id)
	return nil
}

func (r *fakeResync) calls() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.servers...)
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	svc     *Service
	rec     *notificationstest.Recorder
	bus     *events.Bus
	resync  *fakeResync
	ws      uuid.UUID
	user    uuid.UUID
	server  uuid.UUID
	project uuid.UUID
	env     uuid.UUID
	service uuid.UUID
	ctx     context.Context
	// now is the control plane's clock; base is the start of the minute the
	// results are cut from (five minutes earlier).
	now, base time.Time
	seq       uint64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "test")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge-1"})
	require.NoError(t, err)
	f := &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, user: u.ID, server: srv.ID,
		rec: &notificationstest.Recorder{}, bus: events.NewBus(), resync: &fakeResync{},
		now:  time.Date(2026, 10, 28, 12, 35, 0, 0, time.UTC),
		base: time.Date(2026, 10, 28, 12, 25, 0, 0, time.UTC),
	}
	f.mustScan(&f.project, "INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'Shop', 'shop') RETURNING id", ws.ID)
	f.mustScan(&f.env, "INSERT INTO environments (project_id, name, slug) VALUES ($1, 'production', 'production') RETURNING id", f.project)
	f.service = f.newService(f.env, "api", srv.ID)
	f.svc = NewService(Deps{
		Pool: pool, Bus: f.bus, Notifier: f.rec, Resync: f.resync,
		Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	})
	f.svc.now = func() time.Time { return f.now }
	f.ctx = f.principal(policy.AllScopes)
	return f
}

func (f *fixture) mustScan(dst *uuid.UUID, sql string, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(dst))
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (f *fixture) newService(env uuid.UUID, name string, server uuid.UUID) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	f.mustScan(&id, "INSERT INTO services (environment_id, name, slug, source_type, server_id) VALUES ($1, $2, $2, 'image', $3) RETURNING id", env, name, server)
	return id
}

func (f *fixture) principal(scopes []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.user, WorkspaceID: f.ws, Label: "A", Scopes: scopes,
	})
}

// at is the check time sec seconds after base.
func (f *fixture) at(sec int) time.Time { return f.base.Add(time.Duration(sec) * time.Second) }

func (f *fixture) probe(svc uuid.UUID, sec int, outcome string) agentprotocol.ProbeResult {
	r := agentprotocol.ProbeResult{ServiceID: svc.String(), At: f.at(sec), Outcome: outcome, Replicas: 1}
	switch outcome {
	case agentprotocol.ProbeUp:
		r.Passed, r.LatencyMs = 1, 20
	case agentprotocol.ProbeDown:
		r.Error = "HTTP 503"
	case agentprotocol.ProbePaused:
		r.Replicas = 0
	}
	return r
}

// run feeds one result per entry for the fixture's service: "u" up, "d" down,
// "g" degraded, at sec, sec+30, ...
func (f *fixture) run(sec int, outcomes string) int {
	f.t.Helper()
	var rs []agentprotocol.ProbeResult
	for _, c := range outcomes {
		o := map[rune]string{'u': agentprotocol.ProbeUp, 'd': agentprotocol.ProbeDown, 'g': agentprotocol.ProbeDegraded}[c]
		rs = append(rs, f.probe(f.service, sec, o))
		sec += 30
	}
	f.ingest(rs...)
	return sec
}

func (f *fixture) ingest(rs ...agentprotocol.ProbeResult) {
	f.t.Helper()
	f.seq++
	require.NoError(f.t, f.svc.IngestProbes(context.Background(), f.server, agentprotocol.Probes{Seq: f.seq, Results: rs}))
}

type minuteRow struct {
	Bucket                                  time.Time
	Checks, Up, Degraded, Down, Maintenance int
	LatencySum                              int64
	LatencyMax                              int
}

func (f *fixture) minutes(svc uuid.UUID) []minuteRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT bucket, checks, up, degraded, down, maintenance, latency_sum_ms, latency_max_ms
		FROM uptime_1m WHERE service_id = $1 ORDER BY bucket`, svc)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []minuteRow
	for rows.Next() {
		var r minuteRow
		require.NoError(f.t, rows.Scan(&r.Bucket, &r.Checks, &r.Up, &r.Degraded, &r.Down, &r.Maintenance, &r.LatencySum, &r.LatencyMax))
		r.Bucket = r.Bucket.UTC()
		out = append(out, r)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *fixture) state(svc uuid.UUID) db.UptimeState {
	f.t.Helper()
	st, err := f.q.GetUptimeState(context.Background(), svc)
	require.NoError(f.t, err)
	return st
}

func (f *fixture) incidents(svc uuid.UUID) []db.UptimeIncident {
	f.t.Helper()
	rows, err := f.q.ListUptimeIncidents(context.Background(), db.ListUptimeIncidentsParams{ServiceID: svc, RowLimit: 50})
	require.NoError(f.t, err)
	return rows
}

func TestResendsAreNotCountedTwice(t *testing.T) {
	f := newFixture(t)
	sub := f.bus.Subscribe(context.Background())

	r1, r2, r3 := f.probe(f.service, 5, agentprotocol.ProbeUp), f.probe(f.service, 35, agentprotocol.ProbeUp), f.probe(f.service, 65, agentprotocol.ProbeDegraded)
	r1.LatencyMs, r2.LatencyMs, r3.LatencyMs = 10, 30, 50
	f.ingest(r1, r2, r3)
	f.ingest(r1, r2, r3) // the ack was lost: the agent sends the same frame again
	r4 := f.probe(f.service, 95, agentprotocol.ProbeUp)
	f.ingest(r2, r3, r4) // after a reconnect: an overlapping frame with one new result

	// r1, r2 in the first minute; r3 (degraded) and r4 in the second: each counted once.
	require.Equal(t, []minuteRow{
		{Bucket: f.base, Checks: 2, Up: 2, LatencySum: 40, LatencyMax: 30},
		{Bucket: f.base.Add(time.Minute), Checks: 2, Up: 1, Degraded: 1, LatencySum: 70, LatencyMax: 50},
	}, f.minutes(f.service))
	st := f.state(f.service)
	require.Equal(t, StateUp, st.State)
	require.True(t, f.at(95).Equal(*st.LastCheckAt))

	select {
	case ev := <-sub:
		require.Equal(t, EventStateChanged, ev.Type)
		require.Equal(t, f.service.String(), ev.ResourceID)
		require.Equal(t, f.project.String(), ev.ProjectID)
		require.Equal(t, f.env.String(), ev.EnvironmentID)
	case <-time.After(time.Second):
		t.Fatal("no uptime.state.changed event")
	}
}

func TestMaintenanceModeCountsAsMaintenanceAndNeverAlerts(t *testing.T) {
	f := newFixture(t)
	f.exec("UPDATE services SET maintenance_enabled = true, maintenance_since = $2 WHERE id = $1", f.service, f.base)
	f.run(5, "dddddd")

	m := f.minutes(f.service)
	require.Len(t, m, 3)
	var checks, maint, down int
	for _, r := range m {
		checks, maint, down = checks+r.Checks, maint+r.Maintenance, down+r.Down
	}
	require.Equal(t, 6, checks)
	require.Equal(t, 6, maint, "every check in maintenance counts as maintenance")
	require.Zero(t, down)
	require.Equal(t, StateMaintenance, f.state(f.service).State)
	require.Empty(t, f.incidents(f.service))
	require.Empty(t, f.rec.Events())

	pct, ok := Availability(int64(checks), 0, 0, int64(maint))
	require.False(t, ok, "%v: all maintenance is no data", pct)
}

func TestMaintenanceModeOnlyCoversChecksAfterItBegan(t *testing.T) {
	f := newFixture(t)
	f.exec("UPDATE services SET maintenance_enabled = true, maintenance_since = $2 WHERE id = $1", f.service, f.at(60))
	f.ingest(f.probe(f.service, 5, agentprotocol.ProbeDown), f.probe(f.service, 65, agentprotocol.ProbeDown))
	m := f.minutes(f.service)
	require.Len(t, m, 2)
	require.Equal(t, minuteRow{Bucket: f.base, Checks: 1, Down: 1}, m[0])
	require.Equal(t, minuteRow{Bucket: f.base.Add(time.Minute), Checks: 1, Maintenance: 1}, m[1])
}

func TestMaintenanceWindowCoversItsChecksOnly(t *testing.T) {
	f := newFixture(t)
	var win, canceled uuid.UUID
	f.mustScan(&win, "INSERT INTO maintenance_windows (workspace_id, title, starts_at, ends_at) VALUES ($1, 'db upgrade', $2, $3) RETURNING id", f.ws, f.at(60), f.at(120))
	f.exec("INSERT INTO maintenance_window_services (window_id, service_id) VALUES ($1, $2)", win, f.service)
	// A canceled window covering the first check never applies.
	f.mustScan(&canceled, "INSERT INTO maintenance_windows (workspace_id, title, starts_at, ends_at, canceled_at) VALUES ($1, 'canceled', $2, $3, now()) RETURNING id", f.ws, f.at(0), f.at(30))
	f.exec("INSERT INTO maintenance_window_services (window_id, service_id) VALUES ($1, $2)", canceled, f.service)

	f.ingest(
		f.probe(f.service, 5, agentprotocol.ProbeDown),  // before the window, canceled window ignored
		f.probe(f.service, 65, agentprotocol.ProbeDown), // inside
		f.probe(f.service, 125, agentprotocol.ProbeUp),  // after: the end is exclusive
	)
	m := f.minutes(f.service)
	require.Len(t, m, 3)
	require.Equal(t, 1, m[0].Down)
	require.Equal(t, 1, m[1].Maintenance)
	require.Equal(t, 1, m[2].Up)
	require.Equal(t, StateUp, f.state(f.service).State)
	require.Empty(t, f.incidents(f.service))
}

func TestResultsOfServicesTheServerDoesNotRunAreDroppedAndTheFrameStillStored(t *testing.T) {
	f := newFixture(t)
	q := f.q
	srv2, err := q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws, Name: "edge-2"})
	require.NoError(t, err)
	elsewhere := f.newService(f.env, "elsewhere", srv2.ID) // same workspace, another server

	ws2, err := q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	var prj2, env2 uuid.UUID
	f.mustScan(&prj2, "INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'p', 'p') RETURNING id", ws2.ID)
	f.mustScan(&env2, "INSERT INTO environments (project_id, name, slug) VALUES ($1, 'production', 'production') RETURNING id", prj2)
	foreign := f.newService(env2, "foreign", f.server) // another workspace, even though it names this server

	deleted := f.newService(f.env, "gone", f.server)
	f.exec("UPDATE services SET deleted_at = now() WHERE id = $1", deleted)

	garbage := f.probe(f.service, 5, agentprotocol.ProbeUp)
	garbage.ServiceID = "not-a-uuid"
	f.ingest(
		f.probe(elsewhere, 5, agentprotocol.ProbeDown),
		f.probe(foreign, 5, agentprotocol.ProbeDown),
		f.probe(deleted, 5, agentprotocol.ProbeDown),
		f.probe(uuid.New(), 5, agentprotocol.ProbeDown), // the dashboard route, a status page, a UDP port ...
		garbage,
		f.probe(f.service, 35, agentprotocol.ProbeUp),
	)
	for _, id := range []uuid.UUID{elsewhere, foreign, deleted} {
		require.Empty(t, f.minutes(id))
		require.Zero(t, f.count("SELECT count(*) FROM uptime_state WHERE service_id = $1", id))
	}
	require.Equal(t, 1, f.count("SELECT count(*) FROM uptime_state"), "only the server's own service has state")
	m := f.minutes(f.service)
	require.Len(t, m, 1)
	require.Equal(t, 1, m[0].Checks, "the valid result was stored; the malformed one was not")
}

func TestMalformedAndOutOfBoundsResultsAreDropped(t *testing.T) {
	f := newFixture(t)
	ancient := f.probe(f.service, 5, agentprotocol.ProbeUp)
	ancient.At = f.now.Add(-agentprotocol.ProbeRetention - 2*time.Hour)
	future := f.probe(f.service, 5, agentprotocol.ProbeUp)
	future.At = f.now.Add(10 * time.Minute)
	badOutcome := f.probe(f.service, 5, "sideways")
	f.ingest(ancient, future, badOutcome, f.probe(f.service, 35, agentprotocol.ProbeUp))
	require.Equal(t, 1, f.minutes(f.service)[0].Checks)
	st := f.state(f.service)
	require.True(t, f.at(35).Equal(*st.LastCheckAt), "a result from the future must not hide honest ones")

	err := f.svc.IngestProbes(context.Background(), f.server, agentprotocol.Probes{Results: make([]agentprotocol.ProbeResult, agentprotocol.MaxProbeResults+1)})
	e, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, 400, e.Status)
}

func TestEachOutageIsAnnouncedOnceAndItsEndOnce(t *testing.T) {
	f := newFixture(t)
	f.run(0, "u")
	downs := []agentprotocol.ProbeResult{f.probe(f.service, 30, agentprotocol.ProbeDown), f.probe(f.service, 60, agentprotocol.ProbeDown), f.probe(f.service, 90, agentprotocol.ProbeDown)}
	f.ingest(downs...)

	down := f.rec.OfType(notifications.TypeUptimeDown)
	require.Len(t, down, 1)
	ev := down[0]
	require.Equal(t, notifications.SeverityError, ev.Severity)
	require.Equal(t, "api is down", ev.Title)
	for _, want := range []string{"api", "production", "Shop", "3 uptime checks", "HTTP 503"} {
		require.Contains(t, ev.Body, want)
	}
	require.Equal(t, "HTTP 503", ev.Fields["Cause"])
	require.Equal(t, f.service, *ev.ServiceID)
	require.Equal(t, f.project, *ev.ProjectID)
	require.Equal(t, StateDown, f.state(f.service).State)

	f.ingest(downs...) // resent
	f.run(120, "ddd")  // the outage goes on
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1, "one alert per outage")
	incs := f.incidents(f.service)
	require.Len(t, incs, 1)
	require.True(t, f.at(90).Equal(incs[0].StartedAt), "the outage starts at the check that crossed the threshold")
	require.Equal(t, "HTTP 503", incs[0].Cause)
	require.NotNil(t, incs[0].NotifiedAt)
	require.Nil(t, incs[0].ResolvedAt)

	f.run(210, "uu")
	up := f.rec.OfType(notifications.TypeUptimeUp)
	require.Len(t, up, 1)
	require.Equal(t, "api is back up", up[0].Title)
	require.Equal(t, notifications.SeverityError, up[0].Severity, "a channel that got the alert gets the end of it")
	require.Equal(t, "2 minutes", up[0].Fields["Duration"], "from the 90 s check to the 240 s check")
	require.Contains(t, up[0].Body, "after 2 minutes")
	incs = f.incidents(f.service)
	require.True(t, f.at(240).Equal(*incs[0].ResolvedAt))
	require.NotNil(t, incs[0].RecoveryNotifiedAt)
	require.Equal(t, StateUp, f.state(f.service).State)

	f.run(270, "uuu")
	require.Len(t, f.rec.OfType(notifications.TypeUptimeUp), 1)

	f.run(360, "ddd") // a second outage
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 2)
	require.Len(t, f.incidents(f.service), 2)
}

func TestAlertsOffStillRecordsTheOutage(t *testing.T) {
	f := newFixture(t)
	f.exec("INSERT INTO uptime_monitors (service_id, workspace_id, alerts) VALUES ($1, $2, false)", f.service, f.ws)
	f.run(0, "uddduu")
	require.Empty(t, f.rec.Events())
	incs := f.incidents(f.service)
	require.Len(t, incs, 1)
	require.Nil(t, incs[0].NotifiedAt)
	require.NotNil(t, incs[0].ResolvedAt)
	require.Nil(t, incs[0].RecoveryNotifiedAt)
}

func TestConfiguredThresholdsApply(t *testing.T) {
	f := newFixture(t)
	f.exec("INSERT INTO uptime_monitors (service_id, workspace_id, failure_threshold, recovery_threshold) VALUES ($1, $2, 1, 3)", f.service, f.ws)
	f.run(0, "ud")
	require.Len(t, f.incidents(f.service), 1, "one failed check is enough")
	f.run(60, "uu")
	require.Nil(t, f.incidents(f.service)[0].ResolvedAt, "two passes are not enough")
	f.run(120, "u")
	require.NotNil(t, f.incidents(f.service)[0].ResolvedAt)
}

func TestFailuresDuringADeploymentAreCountedButOpenNoIncident(t *testing.T) {
	f := newFixture(t)
	f.run(0, "u")
	f.exec("INSERT INTO deployments (service_id, trigger, actor_type, status) VALUES ($1, 'manual', 'user', 'building')", f.service)
	sec := f.run(30, "dddddd")
	require.Empty(t, f.incidents(f.service))
	require.Empty(t, f.rec.Events())
	require.Equal(t, StateUp, f.state(f.service).State)
	var down int
	for _, r := range f.minutes(f.service) {
		down += r.Down
	}
	require.Equal(t, 6, down, "availability tells the truth: the failed checks are in the rollup")

	// The deployment is over and the service is still down: an outage after the threshold.
	f.exec("UPDATE deployments SET status = 'failed' WHERE service_id = $1", f.service)
	f.run(sec, "ddd")
	require.Len(t, f.incidents(f.service), 1)
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
}

func TestPausedResultsAreNotCountedAndCloseTheOutageSilently(t *testing.T) {
	f := newFixture(t)
	f.run(0, "ddd")
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
	f.ingest(f.probe(f.service, 120, agentprotocol.ProbePaused), f.probe(f.service, 150, agentprotocol.ProbePaused))
	require.Equal(t, StatePaused, f.state(f.service).State)
	var checks int
	for _, r := range f.minutes(f.service) {
		checks += r.Checks
	}
	require.Equal(t, 3, checks, "a stopped service has no checks")
	incs := f.incidents(f.service)
	require.NotNil(t, incs[0].ResolvedAt)
	require.Nil(t, incs[0].RecoveryNotifiedAt)
	require.Empty(t, f.rec.OfType(notifications.TypeUptimeUp), "it did not recover, it was stopped")
}

func TestResultsOfAServiceWithMonitoringOffAreDropped(t *testing.T) {
	f := newFixture(t)
	f.exec("INSERT INTO uptime_monitors (service_id, workspace_id, enabled) VALUES ($1, $2, false)", f.service, f.ws)
	f.run(0, "ddd")
	require.Empty(t, f.minutes(f.service))
	require.Empty(t, f.incidents(f.service))
}

func TestServicesWithoutRecentChecksGoUnknownWithoutAnAlert(t *testing.T) {
	f := newFixture(t)
	f.run(0, "uddd") // an open outage, last check at +90 s
	sub := f.bus.Subscribe(context.Background())

	f.now = f.at(90).Add(StaleAfter - time.Second)
	n, err := f.svc.ExpireStale(context.Background())
	require.NoError(t, err)
	require.Zero(t, n, "not stale yet")

	f.now = f.at(90).Add(StaleAfter + time.Second)
	n, err = f.svc.ExpireStale(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	st := f.state(f.service)
	require.Equal(t, StateUnknown, st.State)
	require.True(t, f.now.Equal(st.Since))
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1, "no new alert")
	require.Nil(t, f.incidents(f.service)[0].ResolvedAt, "the outage stays open: nothing is known about its end")
	select {
	case ev := <-sub:
		require.Equal(t, EventStateChanged, ev.Type)
	case <-time.After(time.Second):
		t.Fatal("no event")
	}

	n, err = f.svc.ExpireStale(context.Background())
	require.NoError(t, err)
	require.Zero(t, n, "unknown stays unknown")

	// The agent returns with the service healthy: the open outage ends normally.
	f.now = f.base.Add(9 * time.Minute)
	f.run(480, "uu")
	require.Equal(t, StateUp, f.state(f.service).State)
	require.NotNil(t, f.incidents(f.service)[0].ResolvedAt)
	require.Len(t, f.rec.OfType(notifications.TypeUptimeUp), 1)
}

func TestStateRowsExistOnlyForServicesThatReported(t *testing.T) {
	f := newFixture(t)
	other := f.newService(f.env, "web", f.server)
	f.run(0, "u")
	require.Equal(t, 1, f.count("SELECT count(*) FROM uptime_state"))
	_, err := f.q.GetUptimeState(context.Background(), other)
	require.Error(t, err)
}
