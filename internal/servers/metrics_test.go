package servers_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestPlanRangeTiers(t *testing.T) {
	cases := map[string]struct {
		tier servers.MetricTier
		step time.Duration
	}{
		"5m": {servers.TierRaw, 10 * time.Second}, "10m": {servers.TierRaw, 10 * time.Second},
		"30m": {servers.TierRaw, 10 * time.Second}, "1h": {servers.TierRaw, 30 * time.Second},
		"6h": {servers.Tier1m, 2 * time.Minute}, "12h": {servers.Tier1m, 5 * time.Minute}, "24h": {servers.Tier1m, 5 * time.Minute},
		"7d": {servers.Tier15m, time.Hour}, "30d": {servers.Tier15m, 3 * time.Hour},
	}
	for name, want := range cases {
		p, err := servers.PlanRange(name)
		require.NoError(t, err, name)
		require.Equal(t, want.tier, p.Tier, name)
		require.Equal(t, want.step, p.Step, name)
		require.LessOrEqual(t, int(p.Window/p.Step), servers.MaxMetricPoints, name)
	}
	p, err := servers.PlanRange("")
	require.NoError(t, err)
	require.Equal(t, time.Hour, p.Window, "default range is 1h")
	_, err = servers.PlanRange("2h")
	require.Error(t, err)
}

func TestDownsampleTiersAndMetricsEndpoint(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge"})
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Scopes: policy.AllScopes})
	svc := servers.NewService(pool, events.NewBus(), nil, nil, nil)

	// Two raw samples inside one complete minute, ten minutes ago.
	_, err = pool.Exec(bg, `
		INSERT INTO metric_samples (server_id, ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes) VALUES
		($1, date_trunc('minute', now() - interval '10 minutes') + interval '5 seconds',  10, 100, 1000, 50, 100),
		($1, date_trunc('minute', now() - interval '10 minutes') + interval '20 seconds', 30, 300, 1000, 60, 100)`, srv.ID)
	require.NoError(t, err)

	n, err := servers.Downsample1m(bg, q, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	var cpuAvg, cpuMax float64
	var memAvg, memMax int64
	var samples int
	require.NoError(t, pool.QueryRow(bg, "SELECT cpu_avg, cpu_max, mem_avg, mem_max, samples FROM metric_samples_1m WHERE server_id = $1", srv.ID).
		Scan(&cpuAvg, &cpuMax, &memAvg, &memMax, &samples))
	require.InDelta(t, 20, cpuAvg, 0.001)
	require.InDelta(t, 30, cpuMax, 0.001, "peaks survive downsampling")
	require.EqualValues(t, 200, memAvg)
	require.EqualValues(t, 300, memMax)
	require.Equal(t, 2, samples)

	// Re-running is idempotent.
	_, err = servers.Downsample1m(bg, q, time.Now())
	require.NoError(t, err)
	var rows int
	require.NoError(t, pool.QueryRow(bg, "SELECT count(*) FROM metric_samples_1m").Scan(&rows))
	require.Equal(t, 1, rows)

	_, err = servers.Downsample15m(bg, q)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(bg, "SELECT cpu_avg, cpu_max, samples FROM metric_samples_15m WHERE server_id = $1", srv.ID).Scan(&cpuAvg, &cpuMax, &samples))
	require.InDelta(t, 20, cpuAvg, 0.001, "15m average is sample-weighted")
	require.InDelta(t, 30, cpuMax, 0.001)
	require.Equal(t, 2, samples)

	// 1 h reads the raw tier, 6 h the 1m tier, 7 d the 15m tier.
	for _, tc := range []struct {
		rng  string
		step int
	}{{"1h", 30}, {"6h", 120}, {"7d", 3600}} {
		res, err := svc.Metrics(ctx, srv.ID, tc.rng)
		require.NoError(t, err, tc.rng)
		require.Equal(t, tc.step, res.StepSeconds, tc.rng)
		require.Equal(t, 7, res.RetentionDays, tc.rng)
		require.NotEmpty(t, res.Points, tc.rng)
		require.InDelta(t, 30, res.Points[0].CPUMax, 0.001, tc.rng)
		require.InDelta(t, 20, res.Points[0].CPUPercent, 0.001, tc.rng)
		require.EqualValues(t, 300, res.Points[0].MemMax, tc.rng)
		require.InDelta(t, 20.0, res.Points[0].MemPercent, 0.001, "mem avg 200 of 1000: "+tc.rng)
	}
	_, err = svc.Metrics(ctx, srv.ID, "2h")
	require.Error(t, err)
}

func TestPruneMetricsHonorsPerServerRetention(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	short, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "short"})
	require.NoError(t, err)
	long, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "long"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET metrics_retention_days = 1 WHERE id = $1", short.ID)
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET metrics_retention_days = 30 WHERE id = $1", long.ID)
	require.NoError(t, err)

	for _, id := range []uuid.UUID{short.ID, long.ID} {
		for _, tbl := range []string{"metric_samples_1m", "metric_samples_15m"} {
			_, err = pool.Exec(bg, "INSERT INTO "+tbl+" (server_id, bucket) VALUES ($1, now() - interval '3 days'), ($1, now() - interval '1 hour')", id)
			require.NoError(t, err)
		}
		_, err = pool.Exec(bg, "INSERT INTO metric_samples (server_id, ts) VALUES ($1, now() - interval '49 hours'), ($1, now() - interval '1 hour')", id)
		require.NoError(t, err)
	}
	n, err := servers.PruneMetrics(bg, q, time.Now())
	require.NoError(t, err)
	// raw: 2 old rows; 1m and 15m: the 3-day row of the 1-day server only.
	require.EqualValues(t, 2+1+1, n)
	var c int
	require.NoError(t, pool.QueryRow(bg, "SELECT count(*) FROM metric_samples_1m WHERE server_id = $1", short.ID).Scan(&c))
	require.Equal(t, 1, c)
	require.NoError(t, pool.QueryRow(bg, "SELECT count(*) FROM metric_samples_1m WHERE server_id = $1", long.ID).Scan(&c))
	require.Equal(t, 2, c)
}
