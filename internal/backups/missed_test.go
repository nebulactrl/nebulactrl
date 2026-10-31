package backups

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/svcalerts"
)

func ts(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestMissedDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, cron string
		ref        string
		grace      time.Duration
		want       string
	}{
		{"daily: a quarter of 24h is 6h", "0 3 * * *", "2026-10-27 03:05", 0, "2026-10-28 09:00"},
		{"hourly: the 15 minute floor", "0 * * * *", "2026-10-28 10:05", 0, "2026-10-28 11:15"},
		{"weekly: the 6 hour cap", "0 3 * * 0", "2026-10-25 03:10", 0, "2026-11-01 09:00"},
		{"irregular: friday to monday, grace from the monday-tuesday gap", "0 3 * * 1-5", "2026-10-23 03:05", 0, "2026-10-26 09:00"},
		{"explicit grace wins", "0 3 * * *", "2026-10-27 03:05", 2 * time.Hour, "2026-10-28 05:00"},
	} {
		got, err := MissedDeadline(tc.cron, "UTC", ts(tc.ref), tc.grace)
		require.NoError(t, err, tc.name)
		require.Equal(t, ts(tc.want), got, tc.name)
	}
	_, err := MissedDeadline("not a cron", "UTC", ts("2026-10-27 03:05"), 0)
	require.Error(t, err)
}

func TestEvaluateMissed(t *testing.T) {
	since := ts("2026-10-20 10:00")
	c := missedCandidate{kind: missedKindDatabase, cron: "0 3 * * *", tz: "UTC"}

	// Not tracked yet: only the baseline is recorded, however old the schedule is.
	st, _, _, err := evaluateMissed(c, ts("2026-11-30 00:00"), 0)
	require.NoError(t, err)
	require.Equal(t, missedTrack, st)

	c.since = &since
	// Never succeeded: due 2026-10-21 03:00, +6h grace = 09:00.
	st, ref, deadline, err := evaluateMissed(c, ts("2026-10-21 08:59"), 0)
	require.NoError(t, err)
	require.Equal(t, missedNothing, st)
	require.Equal(t, since, ref)
	require.Equal(t, ts("2026-10-21 09:00"), deadline)
	st, _, _, _ = evaluateMissed(c, ts("2026-10-21 09:01"), 0)
	require.Equal(t, missedAlert, st)

	// Announced for this streak: silent however long it lasts.
	n := ts("2026-10-21 09:15")
	c.notifiedAt = &n
	st, _, _, _ = evaluateMissed(c, ts("2026-11-30 00:00"), 0)
	require.Equal(t, missedNothing, st)

	// A success ends the streak; the next miss is announced again.
	ok := ts("2026-10-22 03:05")
	c.lastOK = &ok
	st, ref, _, _ = evaluateMissed(c, ts("2026-10-23 08:00"), 0)
	require.Equal(t, missedNothing, st, "the next run is due 10-23 03:00, grace until 09:00")
	require.Equal(t, ok, ref)
	st, _, _, _ = evaluateMissed(c, ts("2026-10-23 09:30"), 0)
	require.Equal(t, missedAlert, st)

	// A run in flight is not a miss.
	c.inflight = true
	st, _, _, _ = evaluateMissed(c, ts("2026-10-23 09:30"), 0)
	require.Equal(t, missedNothing, st)
	c.inflight = false

	// A bad cron is an error, not an alert.
	c.cron = "nope"
	_, _, _, err = evaluateMissed(c, ts("2026-10-23 09:30"), 0)
	require.Error(t, err)
}

func (f *fixture) missedEvents() []Event {
	var out []Event
	for _, e := range f.events {
		if e.Type == NotifyBackupMissed {
			out = append(out, e)
		}
	}
	return out
}

func TestCheckMissedAlertsOnceUntilTheNextSuccess(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	dest := f.s3Destination()
	start := ts("2026-10-20 10:00")
	f.svc.now = func() time.Time { return start }
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "0 3 * * *"})
	require.NoError(t, err)

	at := func(s string) { f.svc.now = func() time.Time { return ts(s) } }

	// First look: tracked, nothing sent, even long after "creation".
	at("2026-10-20 10:15")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents())
	require.Equal(t, 1, rowCount(t, f, `SELECT count(*) FROM backup_missed_alerts WHERE kind = 'database' AND subject_id = $1`, sch.ID))

	// Inside the window (due 10-21 03:00 + 6h): nothing.
	at("2026-10-21 08:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents())

	// Past it: one alert with the facts, and no repeat on later checks.
	at("2026-10-21 12:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	got := f.missedEvents()
	require.Len(t, got, 1)
	require.Equal(t, sv.ID, got[0].ServiceID)
	require.Equal(t, f.proj.ID, got[0].ProjectID)
	require.Equal(t, "minio", got[0].Destination)
	require.False(t, got[0].Volume)
	require.Contains(t, got[0].Message, "No backup has succeeded since the schedule was enabled")
	require.Contains(t, got[0].Message, "0 3 * * *")
	at("2026-10-25 12:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Len(t, f.missedEvents(), 1)

	// A failed run does not count as a success: still the same streak.
	_, err = f.pool.Exec(bg, `INSERT INTO backups (workspace_id, service_id, schedule_id, destination_id, trigger, status, finished_at)
		VALUES ($1,$2,$3,$4,'schedule','failed',$5)`, f.ws.ID, sv.ID, sch.ID, dest.ID, ts("2026-10-25 03:00"))
	require.NoError(t, err)
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Len(t, f.missedEvents(), 1)

	// A success resets it; missing the next window alerts again.
	_, err = f.pool.Exec(bg, `INSERT INTO backups (workspace_id, service_id, schedule_id, destination_id, trigger, status, finished_at)
		VALUES ($1,$2,$3,$4,'schedule','succeeded',$5)`, f.ws.ID, sv.ID, sch.ID, dest.ID, ts("2026-10-25 13:00"))
	require.NoError(t, err)
	at("2026-10-26 08:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Len(t, f.missedEvents(), 1, "the next run is not due yet")
	at("2026-10-26 10:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	got = f.missedEvents()
	require.Len(t, got, 2)
	require.Contains(t, got[1].Message, "The last successful backup finished")
}

func TestCheckMissedIgnoresRunningBackupsStoppedDatabasesAndDisabledSchedules(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	dest := f.s3Destination()
	f.svc.now = func() time.Time { return ts("2026-10-20 10:00") }
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "0 3 * * *"})
	require.NoError(t, err)
	at := func(s string) { f.svc.now = func() time.Time { return ts(s) } }
	at("2026-10-20 10:15")
	require.NoError(t, f.svc.CheckMissed(bg))

	// A backup that is still running at the deadline is not a miss.
	_, err = f.pool.Exec(bg, `INSERT INTO backups (workspace_id, service_id, schedule_id, destination_id, trigger, status)
		VALUES ($1,$2,$3,$4,'schedule','running')`, f.ws.ID, sv.ID, sch.ID, dest.ID)
	require.NoError(t, err)
	at("2026-10-22 12:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents())
	_, err = f.pool.Exec(bg, `UPDATE backups SET status = 'failed' WHERE schedule_id = $1`, sch.ID)
	require.NoError(t, err)

	// A database the owner stopped is not checked, and its tracking restarts later.
	_, err = f.pool.Exec(bg, `UPDATE services SET desired_state = 'stopped' WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents())
	require.Equal(t, 0, rowCount(t, f, `SELECT count(*) FROM backup_missed_alerts`))
	_, err = f.pool.Exec(bg, `UPDATE services SET desired_state = 'running' WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents(), "a fresh baseline: a whole interval before it can be missed")

	// A disabled schedule is not checked either.
	_, err = f.pool.Exec(bg, `UPDATE backup_schedules SET enabled = false WHERE id = $1`, sch.ID)
	require.NoError(t, err)
	at("2026-11-30 12:00")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents())
	require.Equal(t, 0, rowCount(t, f, `SELECT count(*) FROM backup_missed_alerts`))
}

func TestCheckMissedHonoursTheConfiguredGraceAndWatchesVolumeBackups(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	dest := f.s3Destination()

	cfg := svcalerts.DefaultSettings().View()
	cfg.BackupGraceMinutes = 30
	_, err := svcalerts.NewService(svcalerts.Deps{Pool: f.pool}).UpdateSettings(f.ctx, cfg)
	require.NoError(t, err)

	_, err = f.pool.Exec(bg, `INSERT INTO volume_backup_configs (service_id, workspace_id, destination_id, cron, timezone, enabled)
		VALUES ($1,$2,$3,'0 3 * * *','UTC',true)`, sv.ID, f.ws.ID, dest.ID)
	require.NoError(t, err)
	at := func(s string) { f.svc.now = func() time.Time { return ts(s) } }
	at("2026-10-20 10:00")
	require.NoError(t, f.svc.CheckMissed(bg))

	// Due 10-21 03:00; with a 30 minute grace it is missed from 03:30 (the automatic 6h would wait until 09:00).
	at("2026-10-21 03:20")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Empty(t, f.missedEvents())
	at("2026-10-21 03:40")
	require.NoError(t, f.svc.CheckMissed(bg))
	got := f.missedEvents()
	require.Len(t, got, 1)
	require.True(t, got[0].Volume)
	require.Equal(t, sv.ID, got[0].ServiceID)
	at("2026-10-22 03:40")
	require.NoError(t, f.svc.CheckMissed(bg))
	require.Len(t, f.missedEvents(), 1, "once per streak")
}

func rowCount(t *testing.T, f *fixture, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), q, args...).Scan(&n))
	return n
}
