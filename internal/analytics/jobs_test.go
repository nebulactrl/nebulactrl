package analytics

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestJobKinds(t *testing.T) {
	require.Equal(t, "traffic_rollup_hourly", RollupHourlyArgs{}.Kind())
	require.Equal(t, "traffic_rollup_daily", RollupDailyArgs{}.Kind())
	require.Equal(t, "traffic_prune", PruneTrafficArgs{}.Kind())
}

func (f *fixture) count(table string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestRollupWorkersFoldMinutesIntoHoursAndDays(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	ctx := context.Background()
	require.Zero(t, f.count("traffic_1h"))

	// The ten-minute job recomputes the recent hours from the minute rows.
	require.NoError(t, (&rollupHourlyWorker{svc: f.svc}).Work(ctx, nil))
	require.Equal(t, 4, f.count("traffic_1h"), "one hour row per host")
	require.Zero(t, f.count("traffic_1d"), "days come from the daily job")
	var req int64
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT requests FROM traffic_1h WHERE host = 'app.example.com'").Scan(&req))
	require.EqualValues(t, 60, req)

	// The hourly job repairs older hours and rolls the hours into days.
	require.NoError(t, (&rollupDailyWorker{svc: f.svc}).Work(ctx, nil))
	require.Equal(t, 4, f.count("traffic_1d"))
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT requests FROM traffic_1d WHERE host = 'app.example.com'").Scan(&req))
	require.EqualValues(t, 60, req)

	// Running them again changes nothing.
	require.NoError(t, (&rollupHourlyWorker{svc: f.svc}).Work(ctx, nil))
	require.NoError(t, (&rollupDailyWorker{svc: f.svc}).Work(ctx, nil))
	require.Equal(t, 4, f.count("traffic_1h"))
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT requests FROM traffic_1d WHERE host = 'app.example.com'").Scan(&req))
	require.EqualValues(t, 60, req)
}

func TestRollupDailyWorkerRepairsLateUploadedHours(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Minutes of an hour that is older than the ten-minute job looks at.
	late := fixedNow.Add(-10 * time.Hour)
	require.NoError(t, f.svc.IngestTraffic(f.ctx, f.server, trafficOf(
		mkBucket(f.service.String(), "app.example.com", late, 12, sketch(t, 0, 3)),
	)))
	require.NoError(t, (&rollupHourlyWorker{svc: f.svc}).Work(ctx, nil))
	require.Zero(t, f.count("traffic_1h"), "the recent window does not reach back ten hours")

	require.NoError(t, (&rollupDailyWorker{svc: f.svc}).Work(ctx, nil))
	require.Equal(t, 1, f.count("traffic_1h"), "the daily job repairs hours up to a day old")
	require.Equal(t, 1, f.count("traffic_1d"))
}

func TestPruneWorkerDeletesExpiredMinutesAndLogsTheCount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `INSERT INTO traffic_1m (server_id, host, bucket, requests, lat_hist) VALUES
		($1, 'old.example.com', $2, 1, '{}'), ($1, 'new.example.com', $3, 1, '{}')`,
		f.server, fixedNow.Add(-30*time.Hour), fixedNow.Add(-time.Hour))
	require.NoError(t, err)

	var logs strings.Builder
	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, (&pruneTrafficWorker{svc: f.svc, jm: jm}).Work(ctx, nil))
	require.Equal(t, 1, f.count("traffic_1m"))
	var host string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT host FROM traffic_1m").Scan(&host))
	require.Equal(t, "new.example.com", host)
	require.Contains(t, logs.String(), "pruned traffic rows")

	// With nothing left to prune the job stays quiet.
	logs.Reset()
	require.NoError(t, (&pruneTrafficWorker{svc: f.svc, jm: jm}).Work(ctx, nil))
	require.NotContains(t, logs.String(), "pruned traffic rows")
}

func TestRegisterJobsRunsEveryJobOnStart(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	RegisterJobs(jm, f.svc)
	require.NoError(t, jm.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jm.Stop(ctx)
	})

	done := func() int {
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			"SELECT count(DISTINCT kind) FROM river_job WHERE state = 'completed' AND kind IN ('traffic_rollup_hourly', 'traffic_rollup_daily', 'traffic_prune')").Scan(&n))
		return n
	}
	require.Eventually(t, func() bool { return done() == 3 }, 20*time.Second, 50*time.Millisecond)
}

func trafficOf(buckets ...agentprotocol.TrafficBucket) agentprotocol.Traffic {
	return agentprotocol.Traffic{Seq: 1, Buckets: buckets}
}
