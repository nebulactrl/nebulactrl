package previews

import (
	"context"
	"time"

	"github.com/google/uuid"
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
