package svcalerts

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeLogs struct {
	lines []string
	err   error
	calls int
}

func (f *fakeLogs) TailRuntimeLogsInternal(_ context.Context, _ uuid.UUID, n int) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.lines) > n {
		return f.lines[len(f.lines)-n:], nil
	}
	return f.lines, nil
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

type fx struct {
	t    *testing.T
	pool *pgxpool.Pool
	ctx  context.Context
	rec  *notificationstest.Recorder
	logs *fakeLogs
	svc  *Service
	in   Input
	now  time.Time
	dep  string
	cid  string
	cnt  int
}

func newFx(t *testing.T) *fx {
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
	sv, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "api", Slug: "api", Kind: "app", SourceType: "image",
		Config: []byte(`{"image":"nginx:1","port":80}`),
	})
	require.NoError(t, err)
	f := &fx{t: t, pool: pool, rec: &notificationstest.Recorder{}, logs: &fakeLogs{}, now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), dep: uuid.NewString(), cid: "c1"}
	f.svc = f.newService()
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "user@example.com", Scopes: policy.AllScopes})
	f.in = Input{WorkspaceID: ws.ID, ProjectID: prj.ID, EnvironmentID: env.ID, ServiceID: sv.ID, ServiceName: sv.Name}
	return f
}

func (f *fx) newService() *Service {
	return NewService(Deps{
		Pool: f.pool, Bus: events.NewBus(), Notifier: f.rec, Logs: f.logs,
		Logger: slog.New(slog.NewTextHandler(testWriter{f.t}, nil)),
	})
}

func (f *fx) observe(state, health string) {
	f.t.Helper()
	f.now = f.now.Add(every)
	in := f.in
	in.Observation = Observation{
		At: f.now, DeploymentID: f.dep, State: state, Health: health,
		Replicas: []Replica{{Key: "/0", ContainerID: f.cid, State: state, Health: health, RestartCount: f.cnt}},
	}
	f.svc.Observe(context.Background(), in)
	f.svc.Wait()
}

func (f *fx) ok()     { f.observe(agentprotocol.StateRunning, agentprotocol.HealthHealthy) }
func (f *fx) exited() { f.observe(agentprotocol.StateExited, agentprotocol.HealthUnhealthy) }

func (f *fx) repeat(d time.Duration, fn func()) {
	end := f.now.Add(d)
	for f.now.Before(end) {
		fn()
	}
}

func (f *fx) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), q, args...).Scan(&n))
	return n
}

func TestCrashIncidentIsRecordedAuditedAnnouncedOnceAndRecovered(t *testing.T) {
	f := newFx(t)
	f.logs.lines = []string{"booting", "panic: cannot open /data\x1b[0m", "exit status 2"}
	f.ok()
	f.repeat(time.Minute, f.exited)

	crashed := f.rec.OfType(notifications.TypeServiceCrashed)
	require.Len(t, crashed, 1)
	ev := crashed[0]
	require.Equal(t, f.in.ServiceID, *ev.ServiceID)
	require.Equal(t, f.in.ProjectID, *ev.ProjectID)
	require.Equal(t, notifications.SeverityError, ev.Severity)
	require.Equal(t, "api crashed", ev.Title)
	require.Contains(t, ev.Body, "The container exited")
	require.Contains(t, ev.Body, "Last output:\nbooting\npanic: cannot open /data[0m\nexit status 2", "control characters are stripped")
	require.NotEmpty(t, ev.Fields["Incident"])

	// One open incident row, one audit entry, nothing repeated.
	require.Equal(t, 1, f.count(`SELECT count(*) FROM service_incidents WHERE service_id = $1 AND resolved_at IS NULL AND kind = 'crashed'`, f.in.ServiceID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, ActionIncidentOpened, f.in.ServiceID.String()))
	f.repeat(10*time.Minute, f.exited)
	require.Len(t, f.rec.OfType(notifications.TypeServiceCrashed), 1)

	// Recovery: after the stable window, once, with the severity of the alert.
	f.repeat(DefaultSettings().RecoverAfter+2*every, f.ok)
	rec := f.rec.OfType(notifications.TypeServiceRecovered)
	require.Len(t, rec, 1)
	require.Equal(t, "api recovered", rec[0].Title)
	require.Equal(t, notifications.SeverityError, rec[0].Severity, "a channel that got the alert gets the recovery")
	require.Equal(t, 0, f.count(`SELECT count(*) FROM service_incidents WHERE service_id = $1 AND resolved_at IS NULL`, f.in.ServiceID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM service_incidents WHERE service_id = $1 AND resolution = 'recovered' AND recovery_notified_at IS NOT NULL`, f.in.ServiceID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, ActionIncidentResolved, f.in.ServiceID.String()))
	f.repeat(time.Hour, f.ok)
	require.Len(t, f.rec.Events(), 2)

	// The timeline: the service's incidents, newest first.
	items, err := f.svc.ListIncidents(f.ctx, f.in.ServiceID, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "crashed", items[0].Kind)
	require.Equal(t, "recovered", items[0].Resolution)
	require.NotNil(t, items[0].ResolvedAt)
}

func TestUnhealthyAlertCarriesNoLogsAndWarns(t *testing.T) {
	f := newFx(t)
	f.logs.lines = []string{"should not appear"}
	f.ok()
	f.repeat(DefaultSettings().UnhealthyGrace+2*every, func() { f.observe(agentprotocol.StateRunning, agentprotocol.HealthUnhealthy) })
	got := f.rec.OfType(notifications.TypeServiceUnhealthy)
	require.Len(t, got, 1)
	require.Equal(t, notifications.SeverityWarning, got[0].Severity)
	require.NotContains(t, got[0].Body, "Last output")
	require.Zero(t, f.logs.calls)
}

func TestLogTailFailureStillSendsTheAlert(t *testing.T) {
	f := newFx(t)
	f.logs.err = errors.New("agent offline")
	f.ok()
	f.repeat(time.Minute, f.exited)
	got := f.rec.OfType(notifications.TypeServiceCrashed)
	require.Len(t, got, 1)
	require.NotContains(t, got[0].Body, "Last output")
}

func TestRestartLoopEscalatesOnceAndAuditsIt(t *testing.T) {
	f := newFx(t)
	f.ok()
	for range 3 {
		f.cnt++
		f.ok()
	}
	require.Len(t, f.rec.OfType(notifications.TypeServiceCrashed), 1)
	loop := f.rec.OfType(notifications.TypeServiceRestartLoop)
	require.Len(t, loop, 1)
	require.Equal(t, "api is restart-looping", loop[0].Title)
	require.Equal(t, "3", loop[0].Fields["Restarts"])
	f.cnt++
	f.ok()
	require.Len(t, f.rec.OfType(notifications.TypeServiceRestartLoop), 1)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM service_incidents WHERE service_id = $1 AND kind = 'restart_loop' AND resolved_at IS NULL`, f.in.ServiceID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionIncidentEscalated))
}

func TestOpenIncidentSurvivesAControlPlaneRestart(t *testing.T) {
	f := newFx(t)
	f.ok()
	f.repeat(time.Minute, f.exited)
	require.Len(t, f.rec.Events(), 1)

	f.svc = f.newService() // fresh process: no in-memory state
	f.repeat(5*time.Minute, f.exited)
	require.Len(t, f.rec.Events(), 1, "the stored incident is resumed, not announced again")
	f.repeat(DefaultSettings().RecoverAfter+2*every, f.ok)
	require.Len(t, f.rec.OfType(notifications.TypeServiceRecovered), 1)
}

func TestStoppedServiceRaisesNothingAndClosesTheIncidentSilently(t *testing.T) {
	f := newFx(t)
	f.ok()
	f.repeat(time.Minute, f.exited)
	require.Len(t, f.rec.Events(), 1)
	f.now = f.now.Add(every)
	in := f.in
	in.Observation = Observation{At: f.now, DeploymentID: f.dep, State: agentprotocol.StateExited, Health: agentprotocol.HealthUnhealthy, Stopped: true}
	f.svc.Observe(context.Background(), in)
	f.svc.Wait()
	require.Len(t, f.rec.Events(), 1, "no recovery message for a deliberate stop")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM service_incidents WHERE resolution = 'stopped' AND recovery_notified_at IS NULL`))
}

func TestSettingsRoundTripValidateAndApply(t *testing.T) {
	f := newFx(t)
	got, err := f.svc.GetSettings(f.ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultSettings().View(), got, "defaults until saved")

	bad := got
	bad.LoopRestarts = 1
	_, err = f.svc.UpdateSettings(f.ctx, bad)
	require.Error(t, err)
	bad = got
	bad.LogLines = 99
	_, err = f.svc.UpdateSettings(f.ctx, bad)
	require.Error(t, err)

	cur := got
	cur.CrashGraceSeconds = 0
	cur.LogLines = 0
	saved, err := f.svc.UpdateSettings(f.ctx, cur)
	require.NoError(t, err)
	require.Equal(t, cur, saved)
	back, err := f.svc.GetSettings(f.ctx)
	require.NoError(t, err)
	require.Equal(t, cur, back)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionSettingsUpdate))

	// The detector uses them at once: no grace, no log lines.
	f.logs.lines = []string{"x"}
	f.ok()
	f.exited()
	got2 := f.rec.OfType(notifications.TypeServiceCrashed)
	require.Len(t, got2, 1)
	require.NotContains(t, got2[0].Body, "Last output")
	require.Zero(t, f.logs.calls)
}

func TestSettingsNeedTheAdminScope(t *testing.T) {
	f := newFx(t)
	reader := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, WorkspaceID: f.in.WorkspaceID, Scopes: []string{policy.ScopeProjectsRead},
	})
	_, err := f.svc.GetSettings(reader)
	require.Error(t, err)
	_, err = f.svc.UpdateSettings(reader, DefaultSettings().View())
	require.Error(t, err)
	items, err := f.svc.ListIncidents(reader, f.in.ServiceID, 10)
	require.NoError(t, err, "incidents are readable with projects:read")
	require.Empty(t, items)
	_, err = f.svc.ListIncidents(reader, uuid.New(), 10)
	require.Error(t, err)
}

func TestLongLogLinesAreClipped(t *testing.T) {
	f := newFx(t)
	f.logs.lines = []string{strings.Repeat("é", 500)}
	f.ok()
	f.repeat(time.Minute, f.exited)
	got := f.rec.OfType(notifications.TypeServiceCrashed)
	require.Len(t, got, 1)
	at := strings.Index(got[0].Body, "Last output:\n")
	require.GreaterOrEqual(t, at, 0, "no last output in %q", got[0].Body)
	line := strings.TrimPrefix(got[0].Body[at:], "Last output:\n")
	require.Equal(t, maxLogLine, len([]rune(line)))
	require.True(t, strings.HasSuffix(line, "…"))
}
