package uptime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type rollRow struct {
	Bucket                                  time.Time
	Checks, Up, Degraded, Down, Maintenance int
	LatencySum                              int64
	LatencyMax                              int
}

func (f *fixture) rolled(table string) []rollRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT bucket, checks, up, degraded, down, maintenance, latency_sum_ms, latency_max_ms
		FROM `+table+` WHERE service_id = $1 ORDER BY bucket`, f.service)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []rollRow
	for rows.Next() {
		var r rollRow
		require.NoError(f.t, rows.Scan(&r.Bucket, &r.Checks, &r.Up, &r.Degraded, &r.Down, &r.Maintenance, &r.LatencySum, &r.LatencyMax))
		r.Bucket = r.Bucket.UTC()
		out = append(out, r)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *fixture) minuteRowAt(at time.Time, checks, up, down int, latSum int64, latMax int) {
	f.t.Helper()
	f.exec(`INSERT INTO uptime_1m (service_id, bucket, checks, up, down, latency_sum_ms, latency_max_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, f.service, at, checks, up, down, latSum, latMax)
}

func TestHourlyAndDailyRollupsSumTheTierBelowAndAreIdempotent(t *testing.T) {
	f := newFixture(t)
	day := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	// Two minutes in hour 23:00 of the 27th, one minute in hour 00:00 of the 28th.
	f.minuteRowAt(day.Add(23*time.Hour+10*time.Minute), 2, 2, 0, 40, 25)
	f.minuteRowAt(day.Add(23*time.Hour+11*time.Minute), 2, 1, 1, 30, 30)
	f.minuteRowAt(day.Add(24*time.Hour+5*time.Minute), 2, 2, 0, 10, 6)
	f.exec("UPDATE uptime_1m SET maintenance = 1, up = 1 WHERE service_id = $1 AND bucket = $2", f.service, day.Add(24*time.Hour+5*time.Minute))
	f.exec("UPDATE uptime_1m SET degraded = 1, up = 0 WHERE service_id = $1 AND bucket = $2", f.service, day.Add(23*time.Hour+10*time.Minute))

	from, to := day, day.Add(25*time.Hour)
	n, err := f.svc.RollupHourly(context.Background(), from.Add(10*time.Minute), to) // from is cut back to the hour
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	wantHours := []rollRow{
		{Bucket: day.Add(23 * time.Hour), Checks: 4, Up: 1, Degraded: 1, Down: 1, Maintenance: 0, LatencySum: 70, LatencyMax: 30},
		{Bucket: day.Add(24 * time.Hour), Checks: 2, Up: 1, Maintenance: 1, LatencySum: 10, LatencyMax: 6},
	}
	require.Equal(t, wantHours, f.rolled("uptime_1h"))

	_, err = f.svc.RollupHourly(context.Background(), from, to)
	require.NoError(t, err)
	require.Equal(t, wantHours, f.rolled("uptime_1h"), "running it again changes nothing")

	n, err = f.svc.RollupDaily(context.Background(), day.Add(3*time.Hour), to)
	require.NoError(t, err)
	require.EqualValues(t, 2, n, "two UTC days")
	require.Equal(t, []rollRow{
		{Bucket: day, Checks: 4, Up: 1, Degraded: 1, Down: 1, LatencySum: 70, LatencyMax: 30},
		{Bucket: day.Add(24 * time.Hour), Checks: 2, Up: 1, Maintenance: 1, LatencySum: 10, LatencyMax: 6},
	}, f.rolled("uptime_1d"))

	// A late upload (an agent that was offline) adds a minute to an old hour: the
	// repair recomputes the hour and the day to the new truth instead of adding to it.
	f.minuteRowAt(day.Add(23*time.Hour+30*time.Minute), 1, 0, 1, 0, 0)
	_, err = f.svc.RollupHourly(context.Background(), day.Add(23*time.Hour), day.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = f.svc.RollupDaily(context.Background(), day, day.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 5, f.rolled("uptime_1h")[0].Checks)
	require.Equal(t, 2, f.rolled("uptime_1h")[0].Down)
	require.Equal(t, 5, f.rolled("uptime_1d")[0].Checks)
	require.Equal(t, 2, f.rolled("uptime_1d")[1].Checks, "the next day is untouched")
}

func TestRollupsKeepServicesApart(t *testing.T) {
	f := newFixture(t)
	other := f.newService(f.env, "web", f.server)
	at := time.Date(2026, 10, 28, 10, 5, 0, 0, time.UTC)
	f.minuteRowAt(at, 2, 2, 0, 20, 10)
	f.exec("INSERT INTO uptime_1m (service_id, bucket, checks, down) VALUES ($1, $2, 3, 3)", other, at)
	_, err := f.svc.RollupHourly(context.Background(), at, at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, f.rolled("uptime_1h")[0].Checks)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT checks FROM uptime_1h WHERE service_id = $1", other).Scan(&n))
	require.Equal(t, 3, n)
}

func TestPruneDropsRowsPastTheirRetentionOnly(t *testing.T) {
	f := newFixture(t)
	now := f.now
	put := func(table string, at time.Time) {
		f.exec("INSERT INTO "+table+" (service_id, bucket, checks, up) VALUES ($1, $2, 1, 1)", f.service, at)
	}
	put("uptime_1m", now.Add(-MinuteRetention-time.Minute))
	put("uptime_1m", now.Add(-MinuteRetention+time.Minute))
	put("uptime_1h", now.Add(-HourRetention-time.Hour))
	put("uptime_1h", now.Add(-HourRetention+time.Hour))
	put("uptime_1d", now.Add(-DayRetention-24*time.Hour))
	put("uptime_1d", now.Add(-DayRetention+24*time.Hour))

	n, err := f.svc.Prune(context.Background(), now)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	for _, table := range []string{"uptime_1m", "uptime_1h", "uptime_1d"} {
		require.Equal(t, 1, f.count("SELECT count(*) FROM "+table), table)
	}
	require.Equal(t, 48*time.Hour, MinuteRetention)
	require.Equal(t, 35*24*time.Hour, HourRetention)
	require.Equal(t, 400*24*time.Hour, DayRetention)
}
