package uptime

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestSanitizeGroupsInTimeOrderAndClampsWhatTheAgentSays(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	a, b := uuid.New(), uuid.New()
	in := []agentprotocol.ProbeResult{
		{ServiceID: a.String(), At: now.Add(-30 * time.Second), Outcome: agentprotocol.ProbeUp, Replicas: 2, Passed: 9, LatencyMs: -5},
		{ServiceID: a.String(), At: now.Add(-90 * time.Second), Outcome: agentprotocol.ProbeDown, Replicas: 5000, Error: "line one\nline two"},
		{ServiceID: b.String(), At: now, Outcome: agentprotocol.ProbePaused},
		{ServiceID: "nope", At: now, Outcome: agentprotocol.ProbeUp},
		{ServiceID: a.String(), Outcome: agentprotocol.ProbeUp},                                                // no time
		{ServiceID: a.String(), At: now.Add(time.Hour), Outcome: agentprotocol.ProbeUp},                        // future
		{ServiceID: a.String(), At: now.Add(-24 * time.Hour), Outcome: agentprotocol.ProbeUp},                  // older than the agent buffers
		{ServiceID: a.String(), At: now, Outcome: "unknown"},                                                   // not an outcome
		{ServiceID: a.String(), At: now.Add(-time.Second), Outcome: agentprotocol.ProbeUp, LatencyMs: 1 << 30}, // absurd latency
	}
	got, dropped := sanitize(in, now)
	require.Equal(t, 5, dropped)
	require.Len(t, got[a], 3)
	require.Equal(t, []time.Time{now.Add(-90 * time.Second), now.Add(-30 * time.Second), now.Add(-time.Second)},
		[]time.Time{got[a][0].At, got[a][1].At, got[a][2].At}, "oldest first")
	require.Equal(t, 1000, got[a][0].Replicas)
	require.Equal(t, "line one line two", got[a][0].Error)
	require.Equal(t, 2, got[a][1].Passed, "passed never exceeds replicas")
	require.Zero(t, got[a][1].LatencyMs)
	require.Equal(t, maxLatencyMs, got[a][2].LatencyMs)
	require.Len(t, got[b], 1)
}

func TestMinuteCounts(t *testing.T) {
	var m minuteCounts
	m.add(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeUp, LatencyMs: 10}, false)
	m.add(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDegraded, LatencyMs: 40}, false)
	m.add(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDown, LatencyMs: 999}, false)
	m.add(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeUp, LatencyMs: 500}, true)
	m.add(agentprotocol.ProbeResult{Outcome: agentprotocol.ProbeDown}, true)
	require.Equal(t, minuteCounts{checks: 5, up: 1, degraded: 1, down: 1, maintenance: 2, latencySum: 50, latencyMax: 40}, m,
		"maintenance wins whatever the probe found; only passing checks have a latency")
}

func TestInMaintenance(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	since := t0.Add(time.Minute)
	on := db.Service{MaintenanceEnabled: true, MaintenanceSince: &since}
	require.False(t, inMaintenance(on, nil, t0))
	require.True(t, inMaintenance(on, nil, since))
	require.True(t, inMaintenance(db.Service{MaintenanceEnabled: true}, nil, t0), "mode on without a start time")
	require.False(t, inMaintenance(db.Service{MaintenanceSince: &since}, nil, t0.Add(time.Hour)), "mode off: the old start time means nothing")

	w := []window{{start: t0, end: t0.Add(time.Hour)}}
	require.True(t, inMaintenance(db.Service{}, w, t0))
	require.True(t, inMaintenance(db.Service{}, w, t0.Add(59*time.Minute)))
	require.False(t, inMaintenance(db.Service{}, w, t0.Add(time.Hour)), "the end is exclusive")
	require.False(t, inMaintenance(db.Service{}, w, t0.Add(-time.Second)))
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                               "0 seconds",
		45 * time.Second:                "45 seconds",
		time.Minute:                     "1 minute",
		150 * time.Second:               "2 minutes",
		time.Hour:                       "1 h",
		3*time.Hour + 20*time.Minute:    "3 h 20 min",
		48 * time.Hour:                  "2 d",
		50*time.Hour + 10*time.Minute:   "2 d 2 h",
		59*time.Minute + 59*time.Second: "59 minutes",
		23*time.Hour + 59*time.Minute + 40*time.Second: "23 h 59 min",
	} {
		require.Equal(t, want, humanDuration(d), d.String())
	}
}

func TestNoticeMessages(t *testing.T) {
	ws, prj, svc := uuid.New(), uuid.New(), uuid.New()
	n := notice{workspaceID: ws, projectID: prj, serviceID: svc, service: "api", environment: "staging", project: "Shop", cause: "connection refused", failures: 4}
	ev := n.event()
	require.Equal(t, notifications.TypeUptimeDown, ev.Type)
	require.Equal(t, notifications.SeverityError, ev.Severity)
	require.Equal(t, "api is down", ev.Title)
	require.Equal(t, "api in staging (project Shop) failed 4 uptime checks in a row: connection refused.", ev.Body)
	require.Equal(t, map[string]string{"Cause": "connection refused"}, ev.Fields)
	require.Equal(t, ws, ev.WorkspaceID)
	require.Equal(t, svc, *ev.ServiceID)

	n.up, n.duration = true, 7*time.Minute+10*time.Second
	ev = n.event()
	require.Equal(t, notifications.TypeUptimeUp, ev.Type)
	require.Equal(t, notifications.SeverityError, ev.Severity)
	require.Equal(t, "api is back up", ev.Title)
	require.True(t, strings.Contains(ev.Body, "after 7 minutes of downtime"), ev.Body)
	require.Equal(t, "7 minutes", ev.Fields["Duration"])
}

func TestBuildSeriesFoldsFinerRowsIntoTheirBucketAndFillsGaps(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 35, 0, 0, time.UTC)
	row := func(at time.Time, checks, up int64, lat int64, latMax int64) bucketRow {
		return bucketRow{at: at, counts: counts{checks: checks, up: up, down: checks - up, latencySum: lat, latencyMax: latMax}}
	}
	rows := []bucketRow{
		row(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC), 10, 10, 200, 40), // an hour row
		row(time.Date(2026, 10, 28, 12, 10, 0, 0, time.UTC), 4, 4, 80, 30),   // minutes of the running hour
		row(time.Date(2026, 10, 28, 12, 20, 0, 0, time.UTC), 2, 1, 10, 10),   //
		row(time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC), 1, 1, 0, 0),      // before the window
		row(time.Date(2026, 10, 28, 13, 0, 0, 0, time.UTC), 1, 1, 0, 0),      // after the running bucket
	}
	hours := buildSeries(seriesSpec{width: time.Hour, n: 3}, now, rows)
	require.Len(t, hours, 3)
	require.True(t, hours[0].At.Equal(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)))
	require.EqualValues(t, 10, hours[0].Checks)
	require.Zero(t, hours[1].Checks, "a gap is a bucket of zeros")
	require.EqualValues(t, 6, hours[2].Checks)
	require.EqualValues(t, 5, hours[2].Up)
	require.EqualValues(t, 1, hours[2].Down)
	require.EqualValues(t, 18, hours[2].AvgLatency, "90 ms over 5 passing checks")
	require.EqualValues(t, 30, hours[2].MaxLatency)

	days := buildSeries(seriesSpec{width: 24 * time.Hour, n: 2}, now, append(rows, row(time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC), 3, 3, 0, 0)))
	require.Len(t, days, 2)
	require.True(t, days[0].At.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)))
	require.EqualValues(t, 4, days[0].Checks)
	require.EqualValues(t, 17, days[1].Checks, "hour row, minute rows and the row of 13:00 all fall in today")
}

func TestAvailabilityOfPoints(t *testing.T) {
	require.Nil(t, availabilityOf(nil))
	require.Nil(t, availabilityOf([]UptimePoint{{}, {Checks: 5, Maintenance: 5}}))
	got := availabilityOf([]UptimePoint{{Checks: 10, Up: 9, Down: 1}, {Checks: 10, Up: 5, Degraded: 3, Down: 2}, {Checks: 4, Maintenance: 4}})
	require.NotNil(t, got)
	require.InDelta(t, 85.0, *got, 1e-9)
}

func TestSettingsAndMonitorValidation(t *testing.T) {
	require.NoError(t, defaultMonitor().Validate())
	require.Equal(t, DefaultSettings().FailureThreshold, defaultMonitor().FailureThreshold)
	for _, v := range []MonitorView{
		{FailureThreshold: 0, RecoveryThreshold: 1},
		{FailureThreshold: 21, RecoveryThreshold: 1},
		{FailureThreshold: 1, RecoveryThreshold: 0},
		{FailureThreshold: 1, RecoveryThreshold: 21},
	} {
		require.Error(t, v.Validate())
	}
	require.NoError(t, MonitorView{FailureThreshold: 20, RecoveryThreshold: 1}.Validate())
}
