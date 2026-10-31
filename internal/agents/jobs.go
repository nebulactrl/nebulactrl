package agents

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// ExpireCommandsArgs expires undelivered commands (periodic, every minute).
type ExpireCommandsArgs struct{}

// Kind implements river.JobArgs.
func (ExpireCommandsArgs) Kind() string { return "expire_commands" }

// OfflineSweepArgs marks silent servers offline (periodic, every 10 seconds).
type OfflineSweepArgs struct{}

// Kind implements river.JobArgs.
func (OfflineSweepArgs) Kind() string { return "offline_sweep" }

type expireCommandsWorker struct {
	river.WorkerDefaults[ExpireCommandsArgs]
	h *Hub
}

func (w *expireCommandsWorker) Work(ctx context.Context, _ *river.Job[ExpireCommandsArgs]) error {
	n, err := w.h.ExpireCommands(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		w.h.log.InfoContext(ctx, "expired undelivered commands", "count", n)
	}
	return nil
}

type offlineSweepWorker struct {
	river.WorkerDefaults[OfflineSweepArgs]
	h *Hub
}

func (w *offlineSweepWorker) Work(ctx context.Context, _ *river.Job[OfflineSweepArgs]) error {
	_, err := w.h.SweepOffline(ctx)
	return err
}

// RegisterJobs adds ExpireCommandsJob and OfflineSweepJob to the job manager.
func (h *Hub) RegisterJobs(jm *jobs.Manager) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &expireCommandsWorker{h: h})
		river.AddWorker(w, &offlineSweepWorker{h: h})
	})
	once := &river.InsertOpts{MaxAttempts: 1}
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return ExpireCommandsArgs{}, once },
		&river.PeriodicJobOpts{ID: "expire_commands", RunOnStart: true},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Second),
		func() (river.JobArgs, *river.InsertOpts) { return OfflineSweepArgs{}, once },
		&river.PeriodicJobOpts{ID: "offline_sweep", RunOnStart: true},
	))
}
