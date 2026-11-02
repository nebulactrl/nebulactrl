package agents

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// WorkExpireCommandsForTest runs the expire-commands worker for one job attempt.
func (h *Hub) WorkExpireCommandsForTest(ctx context.Context) error {
	return (&expireCommandsWorker{h: h}).Work(ctx, &river.Job[ExpireCommandsArgs]{})
}

// WorkOfflineSweepForTest runs the offline-sweep worker for one job attempt.
func (h *Hub) WorkOfflineSweepForTest(ctx context.Context) error {
	return (&offlineSweepWorker{h: h}).Work(ctx, &river.Job[OfflineSweepArgs]{})
}

// SetClockForTest replaces the hub's clock. Call it before any agent connects.
func (h *Hub) SetClockForTest(now func() time.Time) { h.now = now }
