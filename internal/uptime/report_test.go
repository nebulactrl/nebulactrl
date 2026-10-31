package uptime

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRangeOneHourIsSixtyMinuteCellsEvenWithoutData(t *testing.T) {
	f := newFixture(t) // now is 12:35:00
	f.run(0, "ugd")    // 12:25:00 up, 12:25:30 degraded, 12:26:00 down
	// A check older than the window must not leak in.
	f.minuteRowAt(time.Date(2026, 10, 28, 11, 30, 0, 0, time.UTC), 2, 2, 0, 0, 0)

	rep, err := f.svc.GetServiceUptime(f.ctx, f.service, "1h")
	require.NoError(t, err)
	require.Equal(t, "1h", rep.Range)
	require.Equal(t, "minute", rep.Granularity)
	require.Len(t, rep.Series, 60)
	require.True(t, rep.Series[0].At.Equal(time.Date(2026, 10, 28, 11, 36, 0, 0, time.UTC)), "oldest first: 59 minutes back")
	require.True(t, rep.Series[59].At.Equal(time.Date(2026, 10, 28, 12, 35, 0, 0, time.UTC)), "ending with the running minute")
	for i, p := range rep.Series {
		if i != 49 && i != 50 {
			require.Zero(t, p.Checks, "minute %d has no checks but still has a cell", i)
		}
	}
	require.True(t, rep.Series[49].At.Equal(time.Date(2026, 10, 28, 12, 25, 0, 0, time.UTC)))
	require.EqualValues(t, 2, rep.Series[49].Checks)
	require.EqualValues(t, 1, rep.Series[49].Up)
	require.EqualValues(t, 1, rep.Series[49].Degraded)
	require.EqualValues(t, 1, rep.Series[50].Down)
	require.NotNil(t, rep.Availability.Day, "the availability figures are there for every range")

	// A service that never reported still has its 60 cells.
	empty := f.newService(f.env, "idle", f.server)
	rep, err = f.svc.GetServiceUptime(f.ctx, empty, "1h")
	require.NoError(t, err)
	require.Len(t, rep.Series, 60)
	require.Empty(t, rep.Incidents)

	// The other ranges keep their shape.
	for rng, want := range map[string]int{"24h": 24, "7d": 168, "30d": 30, "90d": 90} {
		rep, err = f.svc.GetServiceUptime(f.ctx, f.service, rng)
		require.NoError(t, err)
		require.Len(t, rep.Series, want, rng)
	}
}

func TestRangeOneHourOverHTTP(t *testing.T) {
	f := newFixture(t)
	code, body := f.apiRequest(http.MethodGet, "/services/"+f.service.String()+"/uptime?range=1h", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	require.Contains(t, string(body), `"granularity":"minute"`)
}

func TestPendingFailuresAreReportedBeforeTheyBecomeAnOutage(t *testing.T) {
	f := newFixture(t)

	rep, err := f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Zero(t, rep.ConsecutiveFailures)
	require.Equal(t, 3, rep.FailureThreshold, "the default without a monitor")

	f.run(0, "udd") // two failures of three
	rep, err = f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Equal(t, StateUp, rep.State, "still up: failing 2/3")
	require.Equal(t, 2, rep.ConsecutiveFailures)
	require.Equal(t, 3, rep.FailureThreshold)

	f.exec("INSERT INTO uptime_monitors (service_id, workspace_id, failure_threshold) VALUES ($1, $2, 7)", f.service, f.ws)
	rep, err = f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Equal(t, 7, rep.FailureThreshold, "the effective threshold of the monitor")

	f.run(90, "u")
	rep, err = f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	require.Zero(t, rep.ConsecutiveFailures, "a passing check ends the streak")
}

func TestAverageLatencyDividesByPassingChecksOnly(t *testing.T) {
	// 10 checks, but only 4 passed and measured: 400 ms / 4, not / 10.
	c := counts{checks: 10, up: 3, degraded: 1, down: 4, maintenance: 2, latencySum: 400, latencyMax: 150}
	p := c.point(time.Time{})
	require.EqualValues(t, 100, p.AvgLatency)
	require.EqualValues(t, 150, p.MaxLatency)
	require.Zero(t, counts{checks: 5, down: 5}.point(time.Time{}).AvgLatency, "no passing check, no latency")
	require.Zero(t, counts{checks: 5, maintenance: 5}.point(time.Time{}).AvgLatency)
}

func TestAverageLatencyThroughIngestIgnoresFailingAndMaintenanceChecks(t *testing.T) {
	f := newFixture(t)
	up1, up2 := f.probe(f.service, 5, agentprotocol.ProbeUp), f.probe(f.service, 35, agentprotocol.ProbeUp)
	up1.LatencyMs, up2.LatencyMs = 10, 30
	down := f.probe(f.service, 65, agentprotocol.ProbeDown)
	down.LatencyMs = 9000 // never measured by a failing probe, but a buggy agent could say so
	f.ingest(up1, up2, down)
	f.exec("UPDATE services SET maintenance_enabled = true, maintenance_since = $2 WHERE id = $1", f.service, f.at(90))
	slow := f.probe(f.service, 95, agentprotocol.ProbeUp)
	slow.LatencyMs = 5000
	f.ingest(slow)

	rep, err := f.svc.GetServiceUptime(f.ctx, f.service, "24h")
	require.NoError(t, err)
	last := rep.Series[23]
	require.EqualValues(t, 4, last.Checks)
	require.EqualValues(t, 20, last.AvgLatency, "(10 + 30) / 2 passing checks")
	require.EqualValues(t, 30, last.MaxLatency)
}

func TestReminderMinutesBounds(t *testing.T) {
	ok := MonitorView{FailureThreshold: 3, RecoveryThreshold: 2}
	require.NoError(t, ok.Validate())
	ok.ReminderMinutes = MaxReminderMinutes
	require.NoError(t, ok.Validate())
	ok.ReminderMinutes = MaxReminderMinutes + 1
	require.Error(t, ok.Validate())
	ok.ReminderMinutes = -5
	require.Error(t, ok.Validate())
	require.Zero(t, defaultMonitor().ReminderMinutes, "reminders are off by default")
}
