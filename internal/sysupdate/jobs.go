package sysupdate

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// CheckArgs reads the release feed (every CheckInterval, and once at start).
type CheckArgs struct{}

// Kind implements river.JobArgs.
func (CheckArgs) Kind() string { return "update_check" }

type checkWorker struct {
	river.WorkerDefaults[CheckArgs]
	svc *Service
}

// Work reads the feed. A feed that cannot be read is recorded and is not a job
// failure (the next run is a day away); a failed database write is.
func (w *checkWorker) Work(ctx context.Context, _ *river.Job[CheckArgs]) error {
	return w.svc.Check(ctx)
}

// RegisterJobs registers the check worker and, unless NEBULACTRL_UPDATE_CHECK is
// off, its daily run (which also runs at start, so a fresh control plane knows
// about a release within moments).
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &checkWorker{svc: svc}) })
	if !svc.CheckEnabled() {
		return
	}
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(CheckInterval),
		func() (river.JobArgs, *river.InsertOpts) { return CheckArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "update_check", RunOnStart: true},
	))
}
