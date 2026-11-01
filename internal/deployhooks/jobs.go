package deployhooks

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// SweepArgs is the periodic job that deploys queued hook calls of services
// whose running deployment has finished.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "deploy_hook_sweep" }

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	s *Service
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return w.s.Sweep(ctx)
}

// RegisterJobs registers the sweep worker and its 10-second schedule.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &sweepWorker{s: s}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Second),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		nil,
	))
}
