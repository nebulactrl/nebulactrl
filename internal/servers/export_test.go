package servers

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// RunPruneJobForTest runs the hourly metrics prune job the way the runner does.
func RunPruneJobForTest(ctx context.Context, jm *jobs.Manager) error {
	return (&pruneMetricsWorker{jm: jm}).Work(ctx, &river.Job[PruneMetricsArgs]{})
}

// RunDownsample1mJobForTest runs the raw→1m downsampling job.
func RunDownsample1mJobForTest(ctx context.Context, jm *jobs.Manager) error {
	return (&downsample1mWorker{jm: jm}).Work(ctx, &river.Job[DownsampleMetrics1mArgs]{})
}

// RunDownsample15mJobForTest runs the 1m→15m downsampling job.
func RunDownsample15mJobForTest(ctx context.Context, jm *jobs.Manager) error {
	return (&downsample15mWorker{jm: jm}).Work(ctx, &river.Job[DownsampleMetrics15mArgs]{})
}

// RunRemovalJobForTest runs the server removal job for args.
func RunRemovalJobForTest(ctx context.Context, svc *Service, args RemoveServerArgs) error {
	return (&removeServerWorker{svc: svc}).Work(ctx, &river.Job[RemoveServerArgs]{Args: args})
}

// RemovalJobTimeoutForTest is how long the runner lets a removal job run.
func RemovalJobTimeoutForTest(svc *Service) time.Duration {
	return (&removeServerWorker{svc: svc}).Timeout(&river.Job[RemoveServerArgs]{})
}
