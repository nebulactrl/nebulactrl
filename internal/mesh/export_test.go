package mesh

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Test hooks: the clock the staleness checks read, the polling interval of the
// connectivity check and the unexported helpers the black-box tests cannot reach.

// SetClock replaces the clock of the service.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// SetAwaitPoll replaces the polling interval of the connectivity check.
func (s *Service) SetAwaitPoll(d time.Duration) { s.awaitPoll = d }

// AwaitCommand is awaitCommand with a caller-chosen timeout.
func (s *Service) AwaitCommand(ctx context.Context, id uuid.UUID, timeout time.Duration) (db.AgentCommand, error) {
	return s.awaitCommand(ctx, id, timeout)
}

// AllocPort is allocPort.
func AllocPort(purpose string, containerPort int, used []int32) (int, bool) {
	return allocPort(purpose, containerPort, used)
}

// Sentinel errors of the connectivity check.
var (
	ErrCheckNotDelivered = errCheckNotDelivered
	ErrCheckNoAnswer     = errCheckNoAnswer
)
