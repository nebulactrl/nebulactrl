package previews

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// AdvanceForTest runs one step of a preview's deploy wave.
func (s *Service) AdvanceForTest(ctx context.Context, id uuid.UUID) (time.Duration, bool, error) {
	return s.advance(ctx, id)
}

// PublishForTest refreshes the PR comment and commit status.
func (s *Service) PublishForTest(ctx context.Context, id uuid.UUID) error {
	return s.publishToGit(ctx, id)
}

// RunDeleteForTest runs the teardown job body.
func (s *Service) RunDeleteForTest(ctx context.Context, id uuid.UUID, reason string) (bool, error) {
	return s.runDelete(ctx, id, reason)
}

// WorkWaveForTest runs the wave worker for one job attempt, returning what River would see.
func (s *Service) WorkWaveForTest(ctx context.Context, id uuid.UUID) error {
	return (&waveWorker{s: s}).Work(ctx, &river.Job[WaveArgs]{Args: WaveArgs{PreviewID: id}})
}

// WorkDeleteForTest runs the delete worker for one job attempt.
func (s *Service) WorkDeleteForTest(ctx context.Context, id uuid.UUID, reason string) error {
	return (&deleteWorker{s: s}).Work(ctx, &river.Job[DeleteArgs]{Args: DeleteArgs{PreviewID: id, Reason: reason}})
}

// WorkPublishForTest runs the publish worker for one job attempt.
func (s *Service) WorkPublishForTest(ctx context.Context, id uuid.UUID) error {
	return (&publishWorker{s: s}).Work(ctx, &river.Job[PublishArgs]{Args: PublishArgs{PreviewID: id}})
}

// WorkPRForTest runs the pull-request event worker for one job attempt.
func (s *Service) WorkPRForTest(ctx context.Context, ev PullRequest) error {
	return (&prWorker{s: s}).Work(ctx, &river.Job[PRArgs]{Args: PRArgs{Event: ev}})
}

// WorkSweepForTest runs the sweep worker for one job attempt.
func (s *Service) WorkSweepForTest(ctx context.Context) error {
	return (&sweepWorker{s: s}).Work(ctx, &river.Job[SweepArgs]{})
}
