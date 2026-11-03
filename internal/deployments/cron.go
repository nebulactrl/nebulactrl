package deployments

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// syncCronTask makes the service's hidden schedule task (action "service") match
// the config of the release that now runs: a cron release upserts it with the
// release's schedule, any other release (a rollback to before cron, or cron
// turned off) deletes it. It runs in the transaction that marks the deployment
// succeeded, so the schedule never gets ahead of the deployed release.
func syncCronTask(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg services.Config, now time.Time) error {
	cr := cfg.Cron
	if cr == nil {
		return q.DeleteServiceCronTask(ctx, serviceID)
	}
	sch, err := cronexpr.Parse(cr.Schedule)
	if err != nil {
		return fmt.Errorf("cron schedule of the release: %w", err)
	}
	tz := strings.TrimSpace(cr.Timezone)
	if tz == "" || strings.EqualFold(tz, "utc") {
		tz = "UTC"
	}
	next, err := sch.NextIn(now, tz)
	if err != nil {
		return fmt.Errorf("cron timezone of the release: %w", err)
	}
	timeout := cr.TimeoutSeconds
	if timeout == 0 {
		timeout = services.DefaultCronTimeoutSeconds
	}
	concurrency := cr.Concurrency
	if concurrency == "" {
		concurrency = services.CronConcurrencyForbid
	}
	var nextRun *time.Time
	if !next.IsZero() {
		nextRun = &next
	}
	return q.UpsertServiceCronTask(ctx, db.UpsertServiceCronTaskParams{
		ServiceID: serviceID, Schedule: cr.Schedule, Timezone: tz,
		TimeoutSeconds: int32(timeout), Concurrency: concurrency, NextRunAt: nextRun,
	})
}
