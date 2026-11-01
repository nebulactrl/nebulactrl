package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RollupHourlyArgs folds traffic_1m into traffic_1h.
type RollupHourlyArgs struct{}

// Kind implements river.JobArgs.
func (RollupHourlyArgs) Kind() string { return "traffic_rollup_hourly" }

// RollupDailyArgs folds traffic_1h into traffic_1d.
type RollupDailyArgs struct{}

// Kind implements river.JobArgs.
func (RollupDailyArgs) Kind() string { return "traffic_rollup_daily" }

// PruneTrafficArgs deletes traffic rows past each server's retention.
type PruneTrafficArgs struct{}

// Kind implements river.JobArgs.
func (PruneTrafficArgs) Kind() string { return "traffic_prune" }

// Rollup windows. Hour rows are recomputed from minute rows every ten minutes
// for the last few hours, so the running hour is always fresh; a daily pass
// also repairs hours up to a day old (late agent uploads after an outage).
const (
	hourlyRecent = 3 * time.Hour
	// hourlyRepair reaches back less than the 24 h minute retention so a repaired
	// hour never loses minutes to pruning and overwrites a complete row.
	hourlyRepair = 23 * time.Hour
	// dailyRecentDays: hour rows are kept at least two days, so rolling up
	// yesterday and today always sees every hour of both.
	dailyRecentDays = 1
)

type rollKey struct {
	server uuid.UUID
	host   string
}

// merged is a rolled-up row under construction.
type merged struct {
	service   *uuid.UUID
	serviceAt time.Time
	sum       *sum
	rows      int
}

// fold groups source rows by (server, host, bucket start of width) and
// merges each group into one row. Merging is exact for counters, histograms
// and visitor sketches; top lists merge approximately and are cut back to the
// top N plus "Other".
func fold(srcs []db.Traffic1m, width time.Duration) []db.UpsertTraffic1hParams {
	type key struct {
		rollKey
		bucket int64
	}
	groups := map[key]*merged{}
	order := []key{}
	for _, src := range srcs {
		r := rowFrom(src)
		k := key{rollKey{src.ServerID, r.Host}, r.Bucket.Truncate(width).Unix()}
		g := groups[k]
		if g == nil {
			g = &merged{sum: newSum(true)}
			groups[k] = g
			order = append(order, k)
		}
		g.sum.add(&r, decodeHLL(r.HLL))
		g.rows++
		// The newest non-null attribution wins (a domain may move services).
		if r.ServiceID != nil && (g.service == nil || r.Bucket.After(g.serviceAt)) {
			g.service, g.serviceAt = r.ServiceID, r.Bucket
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].bucket != order[j].bucket {
			return order[i].bucket < order[j].bucket
		}
		return order[i].host < order[j].host
	})
	out := make([]db.UpsertTraffic1hParams, 0, len(order))
	for _, k := range order {
		g := groups[k]
		s := g.sum
		var hll []byte
		var visitors int64
		if s.hll != nil {
			hll, _ = s.hll.MarshalBinary()
			visitors = int64(s.hll.Estimate())
		}
		if hll == nil {
			hll = []byte{}
		}
		codes := map[string]int64{}
		for c, n := range s.codes {
			codes[c] = n
		}
		top := agentprotocol.TrafficTop{
			Paths: s.ranked(dimPaths), Pages: s.ranked(dimPages),
			Referrers: s.ranked(dimReferrers), Browsers: s.ranked(dimBrowsers),
			OS: s.ranked(dimOS), Devices: s.ranked(dimDevices),
			NotFound: s.ranked(dimNotFound),
		}
		codesJSON, _ := json.Marshal(codes)
		topJSON, _ := json.Marshal(top)
		out = append(out, db.UpsertTraffic1hParams{
			ServerID: k.server, ServiceID: g.service, Host: k.host, Bucket: time.Unix(k.bucket, 0).UTC(),
			Requests: s.Requests, Documents: s.Documents, BotRequests: s.Bots, BytesIn: s.BytesIn, BytesOut: s.BytesOut,
			S1xx: s.S[0], S2xx: s.S[1], S3xx: s.S[2], S4xx: s.S[3], S5xx: s.S[4], SOther: s.S[5],
			LatHist: append([]int64(nil), s.Hist[:]...), LatSumMs: s.LatSum, Visitors: visitors, Hll: hll,
			StatusCodes: codesJSON, Top: topJSON, Geo: marshalGeo(s.geo.bucket(maxRolledCities)),
		})
	}
	return out
}

// RollupHourly recomputes traffic_1h rows for every hour in [from, to) from
// traffic_1m. Idempotent.
func (s *Service) RollupHourly(ctx context.Context, from, to time.Time) (int, error) {
	from, to = from.UTC().Truncate(time.Hour), to.UTC()
	srcs, err := s.q.RollupSourceTraffic1m(ctx, db.RollupSourceTraffic1mParams{FromTs: from, ToTs: to})
	if err != nil {
		return 0, fmt.Errorf("read traffic_1m: %w", err)
	}
	params := fold(srcs, time.Hour)
	if err := s.upsertAll(ctx, params, func(q *db.Queries, p db.UpsertTraffic1hParams) error { return q.UpsertTraffic1h(ctx, p) }); err != nil {
		return 0, err
	}
	return len(params), nil
}

// RollupDaily recomputes traffic_1d rows for every UTC day in [from, to) from
// traffic_1h. Idempotent.
func (s *Service) RollupDaily(ctx context.Context, from, to time.Time) (int, error) {
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC()
	srcs, err := s.q.RollupSourceTraffic1h(ctx, db.RollupSourceTraffic1hParams{FromTs: from, ToTs: to})
	if err != nil {
		return 0, fmt.Errorf("read traffic_1h: %w", err)
	}
	conv := make([]db.Traffic1m, len(srcs))
	for i, r := range srcs {
		conv[i] = db.Traffic1m(r)
	}
	params := fold(conv, 24*time.Hour)
	if err := s.upsertAll(ctx, params, func(q *db.Queries, p db.UpsertTraffic1hParams) error {
		return q.UpsertTraffic1d(ctx, db.UpsertTraffic1dParams(p))
	}); err != nil {
		return 0, err
	}
	return len(params), nil
}

func (s *Service) upsertAll(ctx context.Context, params []db.UpsertTraffic1hParams, fn func(*db.Queries, db.UpsertTraffic1hParams) error) error {
	if len(params) == 0 {
		return nil
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		for _, p := range params {
			if err := fn(q, p); err != nil {
				return fmt.Errorf("upsert rollup: %w", err)
			}
		}
		return nil
	})
}

// PruneTraffic deletes rows older than their retention: minute rows after 24 h,
// hour and day rows after each server's configured days.
func (s *Service) PruneTraffic(ctx context.Context, now time.Time) (int64, error) {
	m, err := s.q.PruneTraffic1m(ctx, now.Add(-MinuteRetention))
	if err != nil {
		return 0, fmt.Errorf("prune traffic_1m: %w", err)
	}
	h, err := s.q.PruneTraffic1h(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune traffic_1h: %w", err)
	}
	d, err := s.q.PruneTraffic1d(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune traffic_1d: %w", err)
	}
	return m + h + d, nil
}

type rollupHourlyWorker struct {
	river.WorkerDefaults[RollupHourlyArgs]
	svc *Service
}

func (w *rollupHourlyWorker) Work(ctx context.Context, _ *river.Job[RollupHourlyArgs]) error {
	now := w.svc.now()
	_, err := w.svc.RollupHourly(ctx, now.Add(-hourlyRecent), now.Add(time.Minute))
	return err
}

type rollupDailyWorker struct {
	river.WorkerDefaults[RollupDailyArgs]
	svc *Service
}

func (w *rollupDailyWorker) Work(ctx context.Context, _ *river.Job[RollupDailyArgs]) error {
	now := w.svc.now().UTC()
	// Repair hours that late uploads touched, then roll the hours into days.
	if _, err := w.svc.RollupHourly(ctx, now.Add(-hourlyRepair), now.Add(-hourlyRecent)); err != nil {
		return err
	}
	_, err := w.svc.RollupDaily(ctx, now.AddDate(0, 0, -dailyRecentDays), now.Add(time.Minute))
	return err
}

type pruneTrafficWorker struct {
	river.WorkerDefaults[PruneTrafficArgs]
	svc *Service
	jm  *jobs.Manager
}

func (w *pruneTrafficWorker) Work(ctx context.Context, _ *river.Job[PruneTrafficArgs]) error {
	n, err := w.svc.PruneTraffic(ctx, w.svc.now())
	if err != nil {
		return err
	}
	if n > 0 {
		w.jm.Logger().InfoContext(ctx, "pruned traffic rows", "count", n)
	}
	return nil
}

// RegisterJobs adds the traffic jobs: hourly rollup (every 10 minutes, so the
// running hour is current), daily rollup (hourly) and prune (hourly).
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &rollupHourlyWorker{svc: svc})
		river.AddWorker(w, &rollupDailyWorker{svc: svc})
		river.AddWorker(w, &pruneTrafficWorker{svc: svc, jm: jm})
	})
	periodic := func(id string, every time.Duration, args river.JobArgs) {
		jm.AddPeriodic(river.NewPeriodicJob(
			river.PeriodicInterval(every),
			func() (river.JobArgs, *river.InsertOpts) { return args, &river.InsertOpts{MaxAttempts: 1} },
			&river.PeriodicJobOpts{ID: id, RunOnStart: true},
		))
	}
	periodic("traffic_rollup_hourly", 10*time.Minute, RollupHourlyArgs{})
	periodic("traffic_rollup_daily", time.Hour, RollupDailyArgs{})
	periodic("traffic_prune", time.Hour, PruneTrafficArgs{})
}
