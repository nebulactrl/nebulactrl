package previews

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// SweepInterval is how often idle previews are deleted and the concurrency
// limit is enforced.
const SweepInterval = 10 * time.Minute

const wavePoll = 3 * time.Second

// PRArgs applies one verified pull-request event.
type PRArgs struct {
	Event PullRequest `json:"event"`
}

// Kind implements river.JobArgs.
func (PRArgs) Kind() string { return "preview_pr_event" }

// WaveArgs advances the deploy wave of a preview (deploys, seeds, status).
type WaveArgs struct {
	PreviewID uuid.UUID `json:"previewId"`
}

// Kind implements river.JobArgs.
func (WaveArgs) Kind() string { return "preview_wave" }

// PublishArgs refreshes the PR comment and the commit status of a preview.
type PublishArgs struct {
	PreviewID uuid.UUID `json:"previewId"`
}

// Kind implements river.JobArgs.
func (PublishArgs) Kind() string { return "preview_publish" }

// DeleteArgs tears a preview down.
type DeleteArgs struct {
	PreviewID uuid.UUID `json:"previewId"`
	// Reason is closed, merged, idle or manual.
	Reason string `json:"reason"`
}

// Kind implements river.JobArgs.
func (DeleteArgs) Kind() string { return "preview_delete" }

// SweepArgs deletes idle previews and pauses the oldest ones over the limit.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "preview_sweep" }

type prWorker struct {
	river.WorkerDefaults[PRArgs]
	s *Service
}

func (w *prWorker) Work(ctx context.Context, job *river.Job[PRArgs]) error {
	return w.s.ProcessPullRequest(ctx, job.Args.Event)
}

type waveWorker struct {
	river.WorkerDefaults[WaveArgs]
	s *Service
}

func (w *waveWorker) Work(ctx context.Context, job *river.Job[WaveArgs]) error {
	wait, done, err := w.s.advance(ctx, job.Args.PreviewID)
	if err != nil {
		return err
	}
	if !done {
		return river.JobSnooze(wait)
	}
	return nil
}

type publishWorker struct {
	river.WorkerDefaults[PublishArgs]
	s *Service
}

func (w *publishWorker) Work(ctx context.Context, job *river.Job[PublishArgs]) error {
	return w.s.publishToGit(ctx, job.Args.PreviewID)
}

type deleteWorker struct {
	river.WorkerDefaults[DeleteArgs]
	s *Service
}

func (w *deleteWorker) Work(ctx context.Context, job *river.Job[DeleteArgs]) error {
	retry, err := w.s.runDelete(ctx, job.Args.PreviewID, job.Args.Reason)
	if err != nil {
		return err
	}
	if retry {
		return river.JobSnooze(5 * time.Second)
	}
	return nil
}

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	s *Service
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return w.s.Sweep(ctx)
}

// RegisterJobs registers the preview workers and the periodic idle sweep.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &prWorker{s: s})
		river.AddWorker(w, &waveWorker{s: s})
		river.AddWorker(w, &publishWorker{s: s})
		river.AddWorker(w, &deleteWorker{s: s})
		river.AddWorker(w, &sweepWorker{s: s})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(SweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, nil },
		&river.PeriodicJobOpts{ID: "preview_sweep"},
	))
}

func (s *Service) enqueuePublish(ctx context.Context, id uuid.UUID) error {
	return s.Jobs.Insert(context.WithoutCancel(ctx), PublishArgs{PreviewID: id}, &river.InsertOpts{MaxAttempts: 6})
}
