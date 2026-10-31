package servers

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// PruneMetricsArgs deletes metric samples past their retention: the raw tier
// after RawMetricsRetention, the 1-minute and 15-minute tiers after each
// server's metrics_retention_days.
type PruneMetricsArgs struct{}

// Kind implements river.JobArgs.
func (PruneMetricsArgs) Kind() string { return "prune_metrics" }

type pruneMetricsWorker struct {
	river.WorkerDefaults[PruneMetricsArgs]
	jm *jobs.Manager
}

func (w *pruneMetricsWorker) Work(ctx context.Context, _ *river.Job[PruneMetricsArgs]) error {
	n, err := PruneMetrics(ctx, db.New(w.jm.Pool()), time.Now())
	if err != nil {
		return err
	}
	if n > 0 {
		w.jm.Logger().InfoContext(ctx, "pruned metric samples", "count", n)
	}
	return nil
}

// PruneMetrics deletes expired rows of every metric tier and returns the count.
func PruneMetrics(ctx context.Context, q *db.Queries, now time.Time) (int64, error) {
	raw, err := q.DeleteMetricSamplesBefore(ctx, now.Add(-RawMetricsRetention))
	if err != nil {
		return 0, fmt.Errorf("prune raw metric samples: %w", err)
	}
	m1, err := q.PruneMetricSamples1m(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune 1m metric samples: %w", err)
	}
	m15, err := q.PruneMetricSamples15m(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune 15m metric samples: %w", err)
	}
	return raw + m1 + m15, nil
}

// DownsampleMetrics1mArgs folds raw samples into the 1-minute tier.
type DownsampleMetrics1mArgs struct{}

// Kind implements river.JobArgs.
func (DownsampleMetrics1mArgs) Kind() string { return "downsample_metrics_1m" }

// DownsampleMetrics15mArgs folds the 1-minute tier into the 15-minute tier.
type DownsampleMetrics15mArgs struct{}

// Kind implements river.JobArgs.
func (DownsampleMetrics15mArgs) Kind() string { return "downsample_metrics_15m" }

// Downsample1m recomputes every complete minute since the newest stored one
// (or since the oldest raw sample on first run). Idempotent.
func Downsample1m(ctx context.Context, q *db.Queries, now time.Time) (int64, error) {
	latest, err := q.LatestMetricBucket1m(ctx)
	if err != nil {
		return 0, fmt.Errorf("latest 1m bucket: %w", err)
	}
	since := latest.Add(-time.Minute)
	if floor := now.Add(-RawMetricsRetention); since.Before(floor) {
		since = floor
	}
	n, err := q.DownsampleMetrics1m(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("downsample 1m: %w", err)
	}
	return n, nil
}

// Downsample15m recomputes the 15-minute buckets touched since the newest
// stored one (everything on first run). Idempotent.
func Downsample15m(ctx context.Context, q *db.Queries) (int64, error) {
	latest, err := q.LatestMetricBucket15m(ctx)
	if err != nil {
		return 0, fmt.Errorf("latest 15m bucket: %w", err)
	}
	n, err := q.DownsampleMetrics15m(ctx, latest.Add(-15*time.Minute))
	if err != nil {
		return 0, fmt.Errorf("downsample 15m: %w", err)
	}
	return n, nil
}

type downsample1mWorker struct {
	river.WorkerDefaults[DownsampleMetrics1mArgs]
	jm *jobs.Manager
}

func (w *downsample1mWorker) Work(ctx context.Context, _ *river.Job[DownsampleMetrics1mArgs]) error {
	_, err := Downsample1m(ctx, db.New(w.jm.Pool()), time.Now())
	return err
}

type downsample15mWorker struct {
	river.WorkerDefaults[DownsampleMetrics15mArgs]
	jm *jobs.Manager
}

func (w *downsample15mWorker) Work(ctx context.Context, _ *river.Job[DownsampleMetrics15mArgs]) error {
	_, err := Downsample15m(ctx, db.New(w.jm.Pool()))
	return err
}

// RegisterJobs adds the metrics jobs: PruneMetricsJob (hourly) and the
// raw→1m (every minute) and 1m→15m (every 15 minutes) downsamplers.
func RegisterJobs(jm *jobs.Manager) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &pruneMetricsWorker{jm: jm})
		river.AddWorker(w, &downsample1mWorker{jm: jm})
		river.AddWorker(w, &downsample15mWorker{jm: jm})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) {
			return PruneMetricsArgs{}, &river.InsertOpts{MaxAttempts: 1}
		},
		&river.PeriodicJobOpts{ID: "prune_metrics", RunOnStart: true},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) {
			return DownsampleMetrics1mArgs{}, &river.InsertOpts{MaxAttempts: 1}
		},
		&river.PeriodicJobOpts{ID: "downsample_metrics_1m", RunOnStart: true},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(15*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) {
			return DownsampleMetrics15mArgs{}, &river.InsertOpts{MaxAttempts: 1}
		},
		&river.PeriodicJobOpts{ID: "downsample_metrics_15m", RunOnStart: true},
	))
}
