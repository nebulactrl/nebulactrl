package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/riverqueue/river"
)

// IdempotencyTTL is how long Idempotency-Key records are kept.
const IdempotencyTTL = 24 * time.Hour

// PruneIdempotencyArgs deletes expired idempotency keys.
type PruneIdempotencyArgs struct{}

// Kind implements river.JobArgs.
func (PruneIdempotencyArgs) Kind() string { return "prune_idempotency" }

type pruneIdempotencyWorker struct {
	river.WorkerDefaults[PruneIdempotencyArgs]
	m *Manager
}

func (w *pruneIdempotencyWorker) Work(ctx context.Context, _ *river.Job[PruneIdempotencyArgs]) error {
	n, err := newQueries(w.m.pool).DeleteIdempotencyKeysBefore(ctx, time.Now().Add(-IdempotencyTTL))
	if err != nil {
		return fmt.Errorf("prune idempotency keys: %w", err)
	}
	if n > 0 {
		w.m.logger.InfoContext(ctx, "pruned idempotency keys", "count", n)
	}
	return nil
}

func (m *Manager) registerBuiltins() {
	river.AddWorker(m.workers, &pruneIdempotencyWorker{m: m})
	m.periodic = append(m.periodic, river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return PruneIdempotencyArgs{}, nil },
		&river.PeriodicJobOpts{ID: "prune_idempotency", RunOnStart: true},
	))
}
