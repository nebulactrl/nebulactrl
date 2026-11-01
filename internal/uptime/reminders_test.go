package uptime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// remindAt runs the reminder sweep with the control plane's clock at t.
func (f *fixture) remindAt(t time.Time) int {
	f.t.Helper()
	f.now = t
	n, err := f.svc.SendReminders(context.Background())
	require.NoError(f.t, err)
	return n
}

// openOutage opens an outage at at(90) with the alert sent, and gives the
// monitor the reminder interval (and alerts as given).
func (f *fixture) openOutage(reminderMinutes int, alerts bool) time.Time {
	f.t.Helper()
	f.exec("INSERT INTO uptime_monitors (service_id, workspace_id, alerts, reminder_minutes) VALUES ($1, $2, $3, $4)", f.service, f.ws, alerts, reminderMinutes)
	f.run(0, "uddd")
	return f.at(90)
}

func TestRemindersRepeatTheAlertWhileTheOutageLastsCountedFromTheLastOne(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(30, true)
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)

	require.Zero(t, f.remindAt(opened.Add(29*time.Minute)), "not due yet")
	require.Equal(t, 1, f.remindAt(opened.Add(30*time.Minute)))
	downs := f.rec.OfType(notifications.TypeUptimeDown)
	require.Len(t, downs, 2)
	ev := downs[1]
	require.Equal(t, "api is still down", ev.Title)
	require.Equal(t, notifications.SeverityError, ev.Severity)
	for _, want := range []string{"Reminder:", "api", "production", "Shop", "30 minutes", "HTTP 503"} {
		require.Contains(t, ev.Body, want)
	}
	require.Equal(t, "30 minutes", ev.Fields["Duration"])
	require.Equal(t, "HTTP 503", ev.Fields["Cause"])
	require.Equal(t, f.service, *ev.ServiceID)

	inc := f.incidents(f.service)[0]
	require.NotNil(t, inc.RemindedAt)
	require.True(t, opened.Add(30*time.Minute).Equal(*inc.RemindedAt))

	require.Zero(t, f.remindAt(opened.Add(30*time.Minute)), "the same sweep again sends nothing")
	require.Zero(t, f.remindAt(opened.Add(59*time.Minute)), "the interval counts from the last reminder, not from the start")
	require.Equal(t, 1, f.remindAt(opened.Add(60*time.Minute)))
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 3)
	require.Contains(t, f.rec.OfType(notifications.TypeUptimeDown)[2].Body, "down for 1 h")
}

func TestNoRemindersWithoutAnInterval(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(0, true)
	require.Zero(t, f.remindAt(opened.Add(48*time.Hour)))
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
}

func TestNoRemindersForAnOutageThatWasNeverAnnounced(t *testing.T) {
	// Alerts were off when the outage opened: nothing was announced, so there is nothing to remind of.
	f := newFixture(t)
	opened := f.openOutage(30, false)
	f.exec("UPDATE uptime_monitors SET alerts = true WHERE service_id = $1", f.service)
	require.Zero(t, f.remindAt(opened.Add(2*time.Hour)))
	require.Empty(t, f.rec.Events())
}

func TestSwitchingAlertsOffSilencesTheRemindersOfAnAnnouncedOutage(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(30, true)
	f.exec("UPDATE uptime_monitors SET alerts = false WHERE service_id = $1", f.service)
	require.Zero(t, f.remindAt(opened.Add(2*time.Hour)))
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
}

func TestNoRemindersDuringMaintenance(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(30, true)
	f.exec("UPDATE services SET maintenance_enabled = true, maintenance_since = $2 WHERE id = $1", f.service, f.at(100))
	f.run(120, "d")
	require.Equal(t, StateMaintenance, f.state(f.service).State)
	require.Zero(t, f.remindAt(opened.Add(time.Hour)), "no reminders in maintenance")
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
}

func TestNoRemindersWhileTheAgentIsSilent(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(30, true)
	f.now = opened.Add(StaleAfter + time.Second)
	n, err := f.svc.ExpireStale(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Zero(t, f.remindAt(opened.Add(time.Hour)), "an unknown state is not a reason to nag")
}

func TestNoRemindersAfterTheOutageEnded(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(30, true)
	f.run(120, "uu")
	require.Equal(t, StateUp, f.state(f.service).State)
	require.Zero(t, f.remindAt(opened.Add(3*time.Hour)))
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 1)
	require.Len(t, f.rec.OfType(notifications.TypeUptimeUp), 1)
}

func TestAReminderIsSentAtMostOnceWhenSweepsOverlap(t *testing.T) {
	f := newFixture(t)
	opened := f.openOutage(30, true)
	f.now = opened.Add(31 * time.Minute)
	// Both sweeps listed the same due outage; only the first claim wins.
	rows, err := f.q.ListUptimeReminders(context.Background(), f.now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	n, err := f.svc.SendReminders(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	won, err := f.q.ClaimUptimeReminder(context.Background(), db.ClaimUptimeReminderParams{
		ID: rows[0].UptimeIncident.ID, RemindedAt: f.now.Add(time.Second), Previous: rows[0].UptimeIncident.RemindedAt,
	})
	require.NoError(t, err)
	require.Zero(t, won, "the stale claim loses")
	require.Len(t, f.rec.OfType(notifications.TypeUptimeDown), 2)
}
