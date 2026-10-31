package uptime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// seedHistory stores three finished days, one hour of today and the running
// hour's minutes, all for the fixture's service (now is 2026-10-28 12:35).
//
//	25th, 26th, 27th (day rows):     100 checks each (90 up, 10 down)
//	today 10:00 hour row:            10 checks, 10 up
//	12:10 and 12:11 minute rows:     6 checks, 4 up, 2 down
func (f *fixture) seedHistory() {
	f.t.Helper()
	for _, d := range []int{25, 26, 27} {
		f.exec("INSERT INTO uptime_1d (service_id, bucket, checks, up, down, latency_sum_ms, latency_max_ms) VALUES ($1, $2, 100, 90, 10, 1800, 80)",
			f.service, time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC))
	}
	f.exec("INSERT INTO uptime_1h (service_id, bucket, checks, up, latency_sum_ms, latency_max_ms) VALUES ($1, $2, 10, 10, 200, 40)",
		f.service, time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))
	f.minuteRowAt(time.Date(2026, 10, 28, 12, 10, 0, 0, time.UTC), 4, 4, 0, 80, 30)
	f.minuteRowAt(time.Date(2026, 10, 28, 12, 11, 0, 0, time.UTC), 2, 0, 2, 0, 0)
}

func TestServiceReportMergesTheTiersAndComputesAvailability(t *testing.T) {
	f := newFixture(t)
	f.seedHistory()
	f.run(0, "uddd") // not in the rollups (12:25 minute rows are real, ingested ones)

	rep, err := f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Equal(t, StateDown, rep.State)
	require.Equal(t, "hour", rep.Granularity)
	require.Len(t, rep.Series, 24)
	last := rep.Series[23]
	require.True(t, last.At.Equal(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)))
	require.EqualValues(t, 10, last.Checks, "minute rows of the running hour: 4 + 2 seeded, 4 ingested")
	require.EqualValues(t, 5, last.Up)
	require.EqualValues(t, 5, last.Down)
	require.EqualValues(t, 30, last.MaxLatency)
	// Hour 10:00 comes from the hour rollup.
	require.True(t, rep.Series[21].At.Equal(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)))
	require.EqualValues(t, 10, rep.Series[21].Checks)
	require.EqualValues(t, 20, rep.Series[21].AvgLatency, "200 ms over 10 passing checks")

	// 24 h: hour 10 (10/10) + hour 12 (5/10) = 15 of 20.
	require.NotNil(t, rep.Availability.Day)
	require.InDelta(t, 75.0, *rep.Availability.Day, 1e-9)
	// 7 d, 30 d and 90 d are UTC days: three days of 90/100, today 10 + 10 (5 of 10 + 10 of 10).
	require.NotNil(t, rep.Availability.Week)
	require.InDelta(t, float64(3*90+15)/float64(3*100+20)*100, *rep.Availability.Week, 1e-9)
	require.InDelta(t, *rep.Availability.Week, *rep.Availability.Month, 1e-9)
	require.InDelta(t, *rep.Availability.Week, *rep.Availability.Quarter, 1e-9)

	require.Len(t, rep.Incidents, 1)
	require.Equal(t, "HTTP 503", rep.Incidents[0].Cause)
	require.Nil(t, rep.Incidents[0].ResolvedAt)
	require.True(t, rep.Incidents[0].Notified)
	require.EqualValues(t, f.now.Sub(f.at(90)).Seconds(), rep.Incidents[0].DurationSeconds, "an open outage lasts until now")
	require.Equal(t, "HTTP 503", rep.LastError)
	require.True(t, rep.Monitor.Enabled)

	d30, err := f.svc.GetServiceUptime(f.ctx, f.service, "30d")
	require.NoError(t, err)
	require.Equal(t, "day", d30.Granularity)
	require.Len(t, d30.Series, 30)
	require.EqualValues(t, 100, d30.Series[26].Checks)
	require.EqualValues(t, 0, d30.Series[0].Checks, "a day without rows has no data")
	require.EqualValues(t, 20, d30.Series[29].Checks, "today: the hour row (10) and the minute rows (10)")
	d90, err := f.svc.GetServiceUptime(f.ctx, f.service, "90d")
	require.NoError(t, err)
	require.Len(t, d90.Series, 90)
	w7, err := f.svc.GetServiceUptime(f.ctx, f.service, "7d")
	require.NoError(t, err)
	require.Len(t, w7.Series, 168)
	require.Equal(t, "hour", w7.Granularity)

	_, err = f.svc.GetServiceUptime(f.ctx, f.service, "1y")
	require.Error(t, err)
}

func TestServiceWithoutDataReportsUnknownAndNoAvailability(t *testing.T) {
	f := newFixture(t)
	rep, err := f.svc.GetServiceUptime(f.ctx, f.service, "")
	require.NoError(t, err)
	require.Equal(t, StateUnknown, rep.State)
	require.Nil(t, rep.Since)
	require.Nil(t, rep.Availability.Day, "no checks is no data, not 100 %")
	require.Nil(t, rep.Availability.Quarter)
	require.Len(t, rep.Series, 24)
	require.Empty(t, rep.Incidents)
	require.Equal(t, "24h", rep.Range)

	f.exec("UPDATE services SET desired_state = 'stopped' WHERE id = $1", f.service)
	rep, err = f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Equal(t, StatePaused, rep.State)
}

func TestServiceReportNeedsProjectReadAndHidesOtherWorkspaces(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.GetServiceUptime(f.principal([]string{policy.ScopeServersRead}), f.service, "24h")
	e, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, e.Status)

	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.user, WorkspaceID: uuid.New(), Scopes: policy.AllScopes})
	_, err = f.svc.GetServiceUptime(other, f.service, "24h")
	e, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, e.Status)
}

func TestEnvironmentOverviewListsEveryServiceWithThirtyDays(t *testing.T) {
	f := newFixture(t)
	f.seedHistory()
	idle := f.newService(f.env, "worker", f.server)
	off := f.newService(f.env, "cron", f.server)
	f.exec("INSERT INTO uptime_monitors (service_id, workspace_id, enabled) VALUES ($1, $2, false)", off, f.ws)

	items, err := f.svc.GetEnvironmentUptime(f.ctx, f.env)
	require.NoError(t, err)
	require.Len(t, items, 3)
	by := map[uuid.UUID]EnvironmentService{}
	for _, it := range items {
		by[it.ServiceID] = it
		require.Len(t, it.Days, 30)
	}
	api := by[f.service]
	require.Equal(t, "api", api.Name)
	require.NotNil(t, api.Availability30d)
	require.InDelta(t, float64(3*90+14)/float64(3*100+16)*100, *api.Availability30d, 1e-9)
	require.True(t, api.Monitored)
	require.Equal(t, StateUnknown, api.State, "no state row yet")

	require.Equal(t, StateUnknown, by[idle].State)
	require.Nil(t, by[idle].Availability30d)
	require.Equal(t, StatePaused, by[off].State)
	require.False(t, by[off].Monitored)

	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.user, WorkspaceID: uuid.New(), Scopes: policy.AllScopes})
	_, err = f.svc.GetEnvironmentUptime(other, f.env)
	require.Error(t, err)
}

func TestMonitorDefaultsUpdateAuditAndResync(t *testing.T) {
	f := newFixture(t)
	f.exec("UPDATE services SET server_id = $2 WHERE id = $1", f.service, f.server)

	m, err := f.svc.GetMonitor(f.ctx, f.service)
	require.NoError(t, err)
	require.Equal(t, MonitorView{Enabled: true, Alerts: true, FailureThreshold: 3, RecoveryThreshold: 2}, m)

	// Thresholds only: nothing to push to the agent.
	_, err = f.svc.UpdateMonitor(f.ctx, f.service, MonitorView{Enabled: true, Alerts: false, FailureThreshold: 5, RecoveryThreshold: 4})
	require.NoError(t, err)
	require.Empty(t, f.resync.calls())
	m, err = f.svc.GetMonitor(f.ctx, f.service)
	require.NoError(t, err)
	require.Equal(t, MonitorView{Enabled: true, Alerts: false, FailureThreshold: 5, RecoveryThreshold: 4}, m)
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2", ActionMonitorUpdate, f.service.String()))

	// Out of bounds, and not allowed without the admin scope.
	for _, bad := range []MonitorView{
		{Enabled: true, FailureThreshold: 0, RecoveryThreshold: 2},
		{Enabled: true, FailureThreshold: 3, RecoveryThreshold: 21},
		{Enabled: true, FailureThreshold: 3, RecoveryThreshold: 2, ReminderMinutes: 1441},
		{Enabled: true, FailureThreshold: 3, RecoveryThreshold: 2, ReminderMinutes: -1},
	} {
		_, err = f.svc.UpdateMonitor(f.ctx, f.service, bad)
		e, ok := apierr.As(err)
		require.True(t, ok)
		require.Equal(t, http.StatusUnprocessableEntity, e.Status)
	}
	_, err = f.svc.UpdateMonitor(f.principal([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}), f.service, m)
	e, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, e.Status)
}

func TestSwitchingMonitoringOffPausesClosesTheOutageAndTellsTheAgent(t *testing.T) {
	f := newFixture(t)
	f.run(0, "uddd")
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
	sub := f.bus.Subscribe(context.Background())

	off := MonitorView{Enabled: false, Alerts: true, FailureThreshold: 3, RecoveryThreshold: 2}
	_, err := f.svc.UpdateMonitor(f.ctx, f.service, off)
	require.NoError(t, err)
	require.Equal(t, StatePaused, f.state(f.service).State)
	incs := f.incidents(f.service)
	require.NotNil(t, incs[0].ResolvedAt)
	require.Nil(t, incs[0].RecoveryNotifiedAt)
	require.Empty(t, f.rec.OfType(notifications.TypeUptimeUp), "monitoring off is not a recovery")
	require.Equal(t, []uuid.UUID{f.server}, f.resync.calls())
	select {
	case ev := <-sub:
		require.Equal(t, EventStateChanged, ev.Type)
	case <-time.After(time.Second):
		t.Fatal("no event")
	}

	cfg, err := f.svc.UptimeConfigFor(context.Background(), f.server)
	require.NoError(t, err)
	require.Equal(t, []string{f.service.String()}, cfg.Disabled)

	rep, err := f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Equal(t, StatePaused, rep.State)
	require.False(t, rep.Monitor.Enabled)

	// Switching it on again: unknown until the first check.
	on := off
	on.Enabled = true
	_, err = f.svc.UpdateMonitor(f.ctx, f.service, on)
	require.NoError(t, err)
	require.Equal(t, StateUnknown, f.state(f.service).State)
	require.Len(t, f.resync.calls(), 2)
	cfg, err = f.svc.UptimeConfigFor(context.Background(), f.server)
	require.NoError(t, err)
	require.Empty(t, cfg.Disabled)
	f.run(300, "u")
	require.Equal(t, StateUp, f.state(f.service).State)
}

// apiRequest drives the registered operations through a real router.
func (f *fixture) apiRequest(method, path string, body any) (int, []byte) {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{Type: auth.TypeUser, UserID: f.user, WorkspaceID: f.ws, Label: "A", Scopes: policy.AllScopes}
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	Register(humachi.New(r, cfg), f.svc)
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(b)
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

func TestAPISmoke(t *testing.T) {
	f := newFixture(t)
	f.seedHistory()
	f.run(0, "u")

	code, body := f.apiRequest(http.MethodGet, "/services/"+f.service.String()+"/uptime?range=7d", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var rep struct {
		State        string
		Granularity  string
		Range        string
		Series       []map[string]any
		Incidents    []any
		Availability map[string]*float64
		Monitor      map[string]any
	}
	require.NoError(t, json.Unmarshal(body, &rep))
	require.Equal(t, StateUp, rep.State)
	require.Equal(t, "hour", rep.Granularity)
	require.Equal(t, "7d", rep.Range)
	require.Len(t, rep.Series, 168)
	require.Contains(t, rep.Series[0], "avgLatencyMs")
	require.Contains(t, rep.Availability, "24h")
	require.Contains(t, rep.Availability, "90d")
	require.NotNil(t, rep.Availability["30d"])
	require.NotNil(t, rep.Incidents, "an empty list, not null")
	require.Equal(t, true, rep.Monitor["enabled"])

	code, _ = f.apiRequest(http.MethodGet, "/services/"+f.service.String()+"/uptime?range=2y", nil)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	code, _ = f.apiRequest(http.MethodGet, "/services/"+uuid.NewString()+"/uptime", nil)
	require.Equal(t, http.StatusNotFound, code)

	code, body = f.apiRequest(http.MethodGet, "/environments/"+f.env.String()+"/uptime", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	var env struct {
		Items []struct {
			ServiceID       string
			State           string
			Availability30d *float64
			Days            []any
		}
	}
	require.NoError(t, json.Unmarshal(body, &env))
	require.Len(t, env.Items, 1)
	require.Equal(t, StateUp, env.Items[0].State)
	require.Len(t, env.Items[0].Days, 30)
	require.NotNil(t, env.Items[0].Availability30d)

	code, body = f.apiRequest(http.MethodPut, "/services/"+f.service.String()+"/uptime-monitor",
		map[string]any{"enabled": true, "alerts": false, "failureThreshold": 4, "recoveryThreshold": 3, "reminderMinutes": 15})
	require.Equal(t, http.StatusOK, code, string(body))
	code, body = f.apiRequest(http.MethodGet, "/services/"+f.service.String()+"/uptime-monitor", nil)
	require.Equal(t, http.StatusOK, code)
	var mon MonitorView
	require.NoError(t, json.Unmarshal(body, &mon))
	require.Equal(t, MonitorView{Enabled: true, Alerts: false, FailureThreshold: 4, RecoveryThreshold: 3, ReminderMinutes: 15}, mon)

	code, _ = f.apiRequest(http.MethodPut, "/services/"+f.service.String()+"/uptime-monitor",
		map[string]any{"enabled": true, "alerts": true, "failureThreshold": 0, "recoveryThreshold": 3, "reminderMinutes": 0})
	require.Equal(t, http.StatusUnprocessableEntity, code)
}
