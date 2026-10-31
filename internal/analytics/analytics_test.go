package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/axiomhq/hyperloglog"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fixedNow is a Tuesday, 12:30 UTC.
var fixedNow = time.Date(2026, 3, 10, 12, 30, 15, 0, time.UTC)

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *db.Queries
	svc      *Service
	ws       uuid.UUID
	user     uuid.UUID
	server   uuid.UUID
	project  uuid.UUID
	env      uuid.UUID
	service  uuid.UUID
	ctx      context.Context
	otherPrj uuid.UUID
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
	f := &fixture{t: t, pool: pool, q: q, ws: ws.ID, user: u.ID, server: srv.ID}
	f.mustScan("INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'p', 'p') RETURNING id", &f.project, ws.ID)
	f.mustScan("INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'q', 'q') RETURNING id", &f.otherPrj, ws.ID)
	f.mustScan("INSERT INTO environments (project_id, name, slug) VALUES ($1, 'production', 'production') RETURNING id", &f.env, f.project)
	f.mustScan("INSERT INTO services (environment_id, name, slug, source_type, server_id) VALUES ($1, 'web', 'web', 'image', $2) RETURNING id", &f.service, f.env, srv.ID)
	_, err = pool.Exec(bg, "INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, 'www.example.com', 80)", f.service)
	require.NoError(t, err)
	f.svc = NewService(pool, nil, nil)
	f.svc.now = func() time.Time { return fixedNow }
	f.ctx = f.principal(policy.AllScopes, nil)
	return f
}

func (f *fixture) mustScan(sql string, dst *uuid.UUID, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(dst))
}

func (f *fixture) principal(scopes []string, projects []uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.user, WorkspaceID: f.ws, Label: "A", Scopes: scopes, ProjectIDs: projects,
	})
}

// sketch builds a marshaled visitor sketch of ids [lo, hi).
func sketch(t *testing.T, lo, hi int) []byte {
	t.Helper()
	sk, err := hyperloglog.NewSketch(agentprotocol.HLLPrecision, true)
	require.NoError(t, err)
	for i := lo; i < hi; i++ {
		sk.InsertHash(mix(uint64(i)))
	}
	b, err := sk.MarshalBinary()
	require.NoError(t, err)
	return b
}

func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// mkBucket builds a minute bucket whose requests all took 100 ms and succeeded.
func mkBucket(service, host string, at time.Time, requests int64, hll []byte) agentprotocol.TrafficBucket {
	hist := make([]int64, agentprotocol.LatencyBuckets)
	hist[agentprotocol.LatencyBucket(100)] = requests
	return agentprotocol.TrafficBucket{
		ServiceID: service, Host: host, Bucket: at.Truncate(time.Minute),
		Requests: requests, Documents: requests / 2, BytesOut: requests * 1000, S2xx: requests,
		LatHist: hist, LatSumMs: float64(requests) * 100, HLL: hll,
		StatusCodes: map[string]int64{"200": requests},
		Top: agentprotocol.TrafficTop{
			Pages: []agentprotocol.TopEntry{{K: "/", N: requests / 2}},
			Paths: []agentprotocol.TopEntry{{K: "/", N: requests / 2}, {K: "/app.js", N: requests - requests/2}},
		},
	}
}

func (f *fixture) seedTraffic() {
	f.t.Helper()
	svc := f.service.String()
	m0, m1, m2 := fixedNow.Add(-3*time.Minute), fixedNow.Add(-2*time.Minute), fixedNow.Add(-time.Minute)
	scan := mkBucket("", "scan.example.net", m1, 7, nil)
	scan.S2xx, scan.S4xx = 0, 7
	frame := agentprotocol.Traffic{Seq: 1, Buckets: []agentprotocol.TrafficBucket{
		mkBucket(svc, "app.example.com", m0, 10, sketch(f.t, 0, 5)),
		mkBucket(svc, "app.example.com", m1, 20, sketch(f.t, 3, 10)),
		mkBucket(svc, "app.example.com", m2, 30, sketch(f.t, 8, 12)),
		mkBucket("", "www.example.com", m1, 5, sketch(f.t, 100, 102)), // attributed through the domain table
		scan, // no stamp, no domain: unrouted
		mkBucket(uuid.NewString(), "ghost.example.net", m1, 3, nil), // stamp of a service that does not exist
	}}
	require.NoError(f.t, f.svc.IngestTraffic(f.ctx, f.server, frame))
}

func TestIngestAttributesAndDropsMalformedBuckets(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()

	type rec struct {
		service *uuid.UUID
		n       int64
	}
	rows, err := f.pool.Query(context.Background(), "SELECT host, service_id, requests FROM traffic_1m")
	require.NoError(t, err)
	got := map[string]rec{}
	for rows.Next() {
		var host string
		var r rec
		require.NoError(t, rows.Scan(&host, &r.service, &r.n))
		got[host] = r
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 4)
	require.Equal(t, f.service, *got["app.example.com"].service, "stamped service")
	require.Equal(t, f.service, *got["www.example.com"].service, "attributed through the domain table")
	require.Nil(t, got["scan.example.net"].service, "unknown host is unrouted")
	require.Nil(t, got["ghost.example.net"].service, "a stamp of a service outside the workspace is not trusted")

	// Malformed and out-of-range buckets are dropped, the rest of the frame is stored.
	bad := mkBucket(f.service.String(), "bad.example.com", fixedNow.Add(-time.Minute), 1, nil)
	bad.LatHist = []int64{1, 2, 3}
	future := mkBucket(f.service.String(), "future.example.com", fixedNow.Add(time.Hour), 1, nil)
	stale := mkBucket(f.service.String(), "stale.example.com", fixedNow.Add(-48*time.Hour), 1, nil)
	fine := mkBucket(f.service.String(), "fine.example.com", fixedNow.Add(-time.Minute), 1, nil)
	require.NoError(t, f.svc.IngestTraffic(f.ctx, f.server, agentprotocol.Traffic{Seq: 2, Buckets: []agentprotocol.TrafficBucket{bad, future, stale, fine}}))
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM traffic_1m WHERE host IN ('bad.example.com','future.example.com','stale.example.com')").Scan(&n))
	require.Zero(t, n)
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM traffic_1m WHERE host = 'fine.example.com'").Scan(&n))
	require.Equal(t, 1, n)
}

func TestIngestIsIdempotentAcrossResends(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	f.seedTraffic() // the agent resends after a reconnect
	var n, req int64
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*), sum(requests) FROM traffic_1m").Scan(&n, &req))
	require.EqualValues(t, 6, n, "three app minutes + www + scan + ghost")
	require.EqualValues(t, 10+20+30+5+7+3, req)
}

func TestIngestStoresNoIdentifiers(t *testing.T) {
	f := newFixture(t)
	b := mkBucket(f.service.String(), "app.example.com", fixedNow.Add(-time.Minute), 4, sketch(t, 0, 4))
	b.Top.Referrers = []agentprotocol.TopEntry{{K: "news.example.org", N: 2}}
	require.NoError(t, f.svc.IngestTraffic(f.ctx, f.server, agentprotocol.Traffic{Buckets: []agentprotocol.TrafficBucket{b}}))
	var dump string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT row_to_json(t)::text FROM traffic_1m t").Scan(&dump))
	// Columns are counters, sketches and top lists only: no ip / user_agent / referrer URL columns exist.
	var cols []string
	rows, err := f.pool.Query(context.Background(), "SELECT column_name FROM information_schema.columns WHERE table_name = 'traffic_1m' AND table_schema = current_schema()")
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	for _, c := range cols {
		require.NotContains(t, []string{"ip", "client_ip", "user_agent", "referrer", "query"}, c)
	}
	require.Contains(t, dump, "news.example.org")
}

func TestIngestDropsFramesOfServersWithAnalyticsOff(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET traffic_analytics_enabled = false WHERE id = $1", f.server)
	require.NoError(t, err)
	f.seedTraffic()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM traffic_1m").Scan(&n))
	require.Zero(t, n)
}

func TestServiceReportFromMinuteTier(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()

	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "1h", Compare: true})
	require.NoError(t, err)
	require.EqualValues(t, 65, rep.Summary.Requests, "app + www host of the service")
	require.InDelta(t, 14, rep.Summary.Visitors, 1, "sketches union per UTC day: ids 0..11 plus 100,101")
	require.EqualValues(t, 65*1000, rep.Summary.BytesOut)
	require.Greater(t, rep.Summary.P95Ms, 75.0)
	require.LessOrEqual(t, rep.Summary.P95Ms, 100.0)
	require.Zero(t, rep.Summary.ErrorRate)
	require.Equal(t, 60, rep.Range.StepSeconds)
	require.False(t, rep.Range.Truncated)
	require.Equal(t, Retention{MinuteHours: 24, HourlyDays: 7, DailyDays: 90}, rep.Range.Retention)
	require.Nil(t, rep.Unrouted, "service reports have no unrouted section")

	var sum int64
	for _, p := range rep.Series {
		sum += p.Requests
	}
	require.EqualValues(t, 65, sum)
	require.Len(t, rep.Series, 61, "zero-filled minute steps from from..to")
	require.NotNil(t, rep.Comparison)
	require.Zero(t, rep.Comparison.Requests, "nothing in the previous hour")

	require.Equal(t, "pages", rep.TopPathsKind)
	require.Equal(t, "/", rep.TopPaths[0].K)
	require.EqualValues(t, 5+10+15+2, rep.TopPaths[0].N, "pages = requests/2 per bucket")
	require.Equal(t, []StatusCount{{Code: 200, N: 65}}, rep.StatusCodes)
	require.Equal(t, "app.example.com", rep.TopHosts[0].K)
	require.Len(t, rep.Sparklines.Requests, len(rep.Series)/2+1, "sparklines regroup to at most 40 points")
	require.LessOrEqual(t, len(rep.Sparklines.Requests), 40)
	require.Nil(t, rep.ActiveHours)

	all, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "1h", AllPaths: true})
	require.NoError(t, err)
	require.Equal(t, "requests", all.TopPathsKind)

	host, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "1h", Host: "App.Example.com"})
	require.NoError(t, err)
	require.EqualValues(t, 60, host.Summary.Requests, "host filter is case-insensitive")
}

func TestServerReportReportsUnrouted(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	rep, err := f.svc.ServerTraffic(f.ctx, f.server, Query{Range: "1h"})
	require.NoError(t, err)
	require.EqualValues(t, 75, rep.Summary.Requests)
	require.NotNil(t, rep.Unrouted)
	require.EqualValues(t, 10, rep.Unrouted.Requests)
	require.Equal(t, "scan.example.net", rep.Unrouted.Hosts[0].K)
	require.InDelta(t, 7.0, rep.Summary.ClientErrorRate*75, 0.001, "scan requests were 4xx")

	ws, err := f.svc.WorkspaceTraffic(f.ctx, Filter{}, Query{Range: "1h"})
	require.NoError(t, err)
	require.EqualValues(t, 75, ws.Summary.Requests)
	one, err := f.svc.WorkspaceTraffic(f.ctx, Filter{ServiceID: &f.service}, Query{Range: "1h"})
	require.NoError(t, err)
	require.EqualValues(t, 65, one.Summary.Requests)
	envRep, err := f.svc.WorkspaceTraffic(f.ctx, Filter{EnvironmentID: &f.env}, Query{Range: "1h"})
	require.NoError(t, err)
	require.EqualValues(t, 65, envRep.Summary.Requests)
}

func TestRollupsAndLongRanges(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()
	ctx := context.Background()

	n, err := f.svc.RollupHourly(ctx, fixedNow.Add(-3*time.Hour), fixedNow.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 4, n, "one hour row per host")
	var req, vis int64
	var hour time.Time
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT bucket, requests, visitors FROM traffic_1h WHERE host = 'app.example.com'").Scan(&hour, &req, &vis))
	require.Equal(t, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), hour.UTC())
	require.EqualValues(t, 60, req)
	require.InDelta(t, 12, vis, 1, "hour sketch is the union of the minute sketches")
	var hist []int64
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT lat_hist FROM traffic_1h WHERE host = 'app.example.com'").Scan(&hist))
	require.EqualValues(t, 60, hist[agentprotocol.LatencyBucket(100)], "histograms merge by addition")

	// Idempotent: recomputing does not double count.
	_, err = f.svc.RollupHourly(ctx, fixedNow.Add(-3*time.Hour), fixedNow.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT requests FROM traffic_1h WHERE host = 'app.example.com'").Scan(&req))
	require.EqualValues(t, 60, req)

	n, err = f.svc.RollupDaily(ctx, fixedNow.AddDate(0, 0, -1), fixedNow.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT bucket, requests, visitors FROM traffic_1d WHERE host = 'app.example.com'").Scan(&hour, &req, &vis))
	require.Equal(t, time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), hour.UTC())
	require.EqualValues(t, 60, req)
	require.InDelta(t, 12, vis, 1)

	// 7d reads the hourly tier.
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "7d"})
	require.NoError(t, err)
	require.Equal(t, 3600, rep.Range.StepSeconds)
	require.EqualValues(t, 65, rep.Summary.Requests)
	require.InDelta(t, 14, rep.Summary.Visitors, 1)
	require.Equal(t, "/", rep.TopPaths[0].K, "top lists survive the rollup")
	require.NotNil(t, rep.ActiveHours)
	require.Len(t, rep.ActiveHours, 7)
	require.EqualValues(t, 65, rep.ActiveHours[int(time.Tuesday)][12])

	// 30d exceeds the default 7-day hourly retention, so daily rows answer.
	rep, err = f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "30d"})
	require.NoError(t, err)
	require.Equal(t, 86400, rep.Range.StepSeconds)
	require.False(t, rep.Range.Truncated)
	require.EqualValues(t, 65, rep.Summary.Requests)
	require.NotNil(t, rep.ActiveHours, "heatmap comes from the hourly rows that still exist")
}

func TestPlanRangeFallsBackToCoarserTiers(t *testing.T) {
	def := Retention{MinuteHours: 24, HourlyDays: 7, DailyDays: 90}
	p, trunc, err := planRange("24h", def)
	require.NoError(t, err)
	require.Equal(t, rangePlan{24 * time.Hour, 5 * time.Minute, TierMinute}, p)
	require.False(t, trunc)

	p, _, _ = planRange("7d", def)
	require.Equal(t, TierHour, p.tier)
	p, trunc, _ = planRange("30d", def)
	require.Equal(t, TierDay, p.tier, "hourly tier only keeps 7 days")
	require.False(t, trunc)
	p, _, _ = planRange("30d", Retention{HourlyDays: 30, DailyDays: 90})
	require.Equal(t, TierHour, p.tier)
	require.Equal(t, 3*time.Hour, p.step)
	_, trunc, _ = planRange("90d", Retention{HourlyDays: 7, DailyDays: 30})
	require.True(t, trunc, "range longer than the daily retention is flagged")

	_, _, err = planRange("2h", def)
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae))
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	p, _, _ = planRange("", def)
	require.Equal(t, 24*time.Hour, p.window, "default range is 24h")
}

func TestPolicyScopesAndIDOR(t *testing.T) {
	f := newFixture(t)
	f.seedTraffic()

	statusOf := func(err error) int {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return ae.Status
		}
		return 0
	}

	// Missing scope: 403.
	noProjects := f.principal([]string{policy.ScopeServersRead}, nil)
	_, err := f.svc.ServiceTraffic(noProjects, f.service, Query{Range: "1h"})
	require.Equal(t, http.StatusForbidden, statusOf(err))
	noServers := f.principal([]string{policy.ScopeProjectsRead}, nil)
	_, err = f.svc.ServerTraffic(noServers, f.server, Query{Range: "1h"})
	require.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = f.svc.WorkspaceTraffic(noServers, Filter{}, Query{Range: "1h"})
	require.Equal(t, http.StatusForbidden, statusOf(err))
	// A service filter switches the global endpoint to projects:read.
	_, err = f.svc.WorkspaceTraffic(noServers, Filter{ServiceID: &f.service}, Query{Range: "1h"})
	require.NoError(t, err)

	// A token restricted to another project cannot see the service: 404, same as a missing one.
	other := f.principal(policy.AllScopes, []uuid.UUID{f.otherPrj})
	_, err = f.svc.ServiceTraffic(other, f.service, Query{Range: "1h"})
	require.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = f.svc.ServiceTraffic(f.ctx, uuid.New(), Query{Range: "1h"})
	require.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = f.svc.WorkspaceTraffic(other, Filter{ServiceID: &f.service}, Query{Range: "1h"})
	require.Equal(t, http.StatusNotFound, statusOf(err))
	_, err = f.svc.WorkspaceTraffic(other, Filter{EnvironmentID: &f.env}, Query{Range: "1h"})
	require.Equal(t, http.StatusNotFound, statusOf(err))

	// Server-level reports of a restricted token only count visible services and hide unrouted traffic.
	rep, err := f.svc.ServerTraffic(other, f.server, Query{Range: "1h"})
	require.NoError(t, err)
	require.Zero(t, rep.Summary.Requests)
	require.Nil(t, rep.Unrouted)
	own := f.principal(policy.AllScopes, []uuid.UUID{f.project})
	rep, err = f.svc.ServerTraffic(own, f.server, Query{Range: "1h"})
	require.NoError(t, err)
	require.EqualValues(t, 65, rep.Summary.Requests)
	require.Nil(t, rep.Unrouted)

	// Unknown server: 404.
	_, err = f.svc.ServerTraffic(f.ctx, uuid.New(), Query{Range: "1h"})
	require.Equal(t, http.StatusNotFound, statusOf(err))
}

func TestPruneHonorsPerServerRetention(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, "UPDATE servers SET traffic_hourly_retention_days = 2, traffic_daily_retention_days = 7 WHERE id = $1", f.server)
	require.NoError(t, err)
	hist := make([]int64, agentprotocol.LatencyBuckets)
	ins := func(tbl string, age time.Duration) {
		_, err := f.pool.Exec(ctx, "INSERT INTO "+tbl+" (server_id, host, bucket, requests, lat_hist) VALUES ($1, 'h.example.com', now() - $2::interval, 1, $3)",
			f.server, fmt.Sprintf("%d seconds", int(age.Seconds())), hist)
		require.NoError(t, err)
	}
	ins("traffic_1m", 25*time.Hour)
	ins("traffic_1m", 2*time.Hour)
	ins("traffic_1h", 3*24*time.Hour)
	ins("traffic_1h", time.Hour)
	ins("traffic_1d", 8*24*time.Hour)
	ins("traffic_1d", 3*24*time.Hour)

	n, err := NewService(f.pool, nil, nil).PruneTraffic(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	for _, tbl := range []string{"traffic_1m", "traffic_1h", "traffic_1d"} {
		var c int
		require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM "+tbl).Scan(&c))
		require.Equal(t, 1, c, tbl)
	}
}

func TestReportJSONShape(t *testing.T) {
	f := newFixture(t)
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"range", "summary", "sparklines", "series", "deployments", "statusCodes", "topPaths", "topHosts", "topReferrers", "topBrowsers", "topOS", "topDevices", "notFound"} {
		require.Contains(t, m, k)
		require.NotNil(t, m[k], "%s must serialize as [] or {}, never null", k)
	}
	require.NotContains(t, m, "comparison")
	require.NotContains(t, m, "unrouted")
}

func TestDeploymentMarkers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var relID uuid.UUID
	f.mustScan(`INSERT INTO releases (service_id, source, config, env_enc, image_ref) VALUES ($1, '{"commitSha":"a1b2c3d4e5f6"}', '{}', ''::bytea, '') RETURNING id`, &relID, f.service)
	_, err := f.pool.Exec(ctx, "INSERT INTO deployments (service_id, release_id, server_id, trigger, actor_type, status, started_at) VALUES ($1, $2, $3, 'manual', 'user', 'succeeded', $4)",
		f.service, relID, f.server, fixedNow.Add(-20*time.Minute))
	require.NoError(t, err)
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "1h"})
	require.NoError(t, err)
	require.Len(t, rep.Deployments, 1)
	require.Equal(t, "a1b2c3d", rep.Deployments[0].Sha7)
	require.Equal(t, "succeeded", rep.Deployments[0].Status)
}
