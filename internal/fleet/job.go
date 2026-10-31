package fleet

import (
	"context"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// drainMaxAttempts bounds River retries after errors (snoozes do not count).
const drainMaxAttempts = 50

// maxStepsPerRun bounds the steps one Work call takes before it yields.
const maxStepsPerRun = 100

// DrainArgs is the River job that drives a server drain move by move.
type DrainArgs struct {
	DrainID uuid.UUID `json:"drainId"`
}

// Kind implements river.JobArgs.
func (DrainArgs) Kind() string { return "drain_server" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (DrainArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: drainMaxAttempts}
}

type drainWorker struct {
	river.WorkerDefaults[DrainArgs]
	s *Service
}

// RegisterJobs registers the DrainJob.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &drainWorker{s: s}) })
}

func (w *drainWorker) Work(ctx context.Context, job *river.Job[DrainArgs]) error {
	for range maxStepsPerRun {
		res, err := w.s.Step(ctx, job.Args.DrainID)
		if err != nil {
			return err
		}
		if res.Done {
			return nil
		}
		if res.Wait > 0 {
			return river.JobSnooze(res.Wait)
		}
	}
	return river.JobSnooze(pollInterval)
}
