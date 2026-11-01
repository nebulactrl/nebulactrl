package patching

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// SweepArgs starts the checks that are due.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "server_patch_sweep" }

// WatchArgs looks at the runs waiting for a rebooted server.
type WatchArgs struct{}

// Kind implements river.JobArgs.
func (WatchArgs) Kind() string { return "server_patch_reboot_watch" }

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	svc *Service
}

func (w *sweepWorker) Timeout(*river.Job[SweepArgs]) time.Duration { return 2 * time.Minute }

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return w.svc.Sweep(ctx)
}

type watchWorker struct {
	river.WorkerDefaults[WatchArgs]
	svc *Service
}

func (w *watchWorker) Timeout(*river.Job[WatchArgs]) time.Duration { return time.Minute }

func (w *watchWorker) Work(ctx context.Context, _ *river.Job[WatchArgs]) error {
	return w.svc.WatchReboots(ctx)
}

// RegisterJobs registers the sweep and the reboot watcher with their periodic jobs.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &sweepWorker{svc: svc})
		river.AddWorker(w, &watchWorker{svc: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(SweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "server_patch_sweep", RunOnStart: true},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(WatchInterval),
		func() (river.JobArgs, *river.InsertOpts) { return WatchArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "server_patch_reboot_watch"},
	))
}
