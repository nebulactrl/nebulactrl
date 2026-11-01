package uptime

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// Retention of the rollup tiers.
const (
	MinuteRetention = 48 * time.Hour
	HourRetention   = 35 * 24 * time.Hour
	DayRetention    = 400 * 24 * time.Hour
)

// Rollup windows. Hour rows are recomputed from minute rows every ten minutes
// for the last few hours, so the running hour is always fresh. The hourly
// day pass also repairs hours up to a day old (late uploads of an agent that
// was offline) and rolls the hours of today and yesterday into day rows.
const (
	hourlyRecent = 3 * time.Hour
	// hourlyRepair stays below the minute retention so a repaired hour never
	// loses minutes to pruning and overwrites a complete row with a partial one.
	hourlyRepair = 24 * time.Hour
	// dailyRecentDays: hour rows are kept for weeks, so rolling up yesterday
	// and today always sees every hour of both.
	dailyRecentDays = 1
)

// RollupHourlyArgs folds uptime_1m into uptime_1h.
type RollupHourlyArgs struct{}

// Kind implements river.JobArgs.
func (RollupHourlyArgs) Kind() string { return "uptime_rollup_hourly" }

// RollupDailyArgs repairs recent hours and folds uptime_1h into uptime_1d.
type RollupDailyArgs struct{}

// Kind implements river.JobArgs.
func (RollupDailyArgs) Kind() string { return "uptime_rollup_daily" }

// PruneArgs deletes rollup rows past their retention.
type PruneArgs struct{}

// Kind implements river.JobArgs.
func (PruneArgs) Kind() string { return "uptime_prune" }

// ExpireArgs marks services without recent checks unknown.
type ExpireArgs struct{}

// Kind implements river.JobArgs.
func (ExpireArgs) Kind() string { return "uptime_expire" }

// RollupHourly recomputes the uptime_1h rows of every hour in [from, to) from
// uptime_1m. Idempotent.
func (s *Service) RollupHourly(ctx context.Context, from, to time.Time) (int64, error) {
	n, err := s.q.RollupUptime1h(ctx, db.RollupUptime1hParams{FromTs: from.UTC().Truncate(time.Hour), ToTs: to.UTC()})
	if err != nil {
		return 0, fmt.Errorf("rollup uptime_1h: %w", err)
	}
	return n, nil
}

// RollupDaily recomputes the uptime_1d rows of every UTC day in [from, to) from
// uptime_1h. Idempotent.
func (s *Service) RollupDaily(ctx context.Context, from, to time.Time) (int64, error) {
	n, err := s.q.RollupUptime1d(ctx, db.RollupUptime1dParams{FromTs: from.UTC().Truncate(24 * time.Hour), ToTs: to.UTC()})
	if err != nil {
		return 0, fmt.Errorf("rollup uptime_1d: %w", err)
	}
	return n, nil
}

// Prune deletes minute rows after 48 hours, hour rows after 35 days and day
// rows after 400 days.
func (s *Service) Prune(ctx context.Context, now time.Time) (int64, error) {
	m, err := s.q.PruneUptime1m(ctx, now.Add(-MinuteRetention))
	if err != nil {
		return 0, fmt.Errorf("prune uptime_1m: %w", err)
	}
	h, err := s.q.PruneUptime1h(ctx, now.Add(-HourRetention))
	if err != nil {
		return 0, fmt.Errorf("prune uptime_1h: %w", err)
	}
	d, err := s.q.PruneUptime1d(ctx, now.Add(-DayRetention))
	if err != nil {
		return 0, fmt.Errorf("prune uptime_1d: %w", err)
	}
	return m + h + d, nil
}

// ExpireStale moves every service whose last check is older than StaleAfter to
// unknown and returns how many it moved. No alert: a server that stopped
// reporting is the server-offline alert's business.
func (s *Service) ExpireStale(ctx context.Context) (int, error) {
	now := s.now().UTC()
	var ids []uuid.UUID
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		ids = nil
		q := db.New(tx)
		rows, err := q.LockStaleUptimeStates(ctx, now.Add(-s.staleAfter))
		if err != nil {
			return fmt.Errorf("lock stale uptime states: %w", err)
		}
		for _, r := range rows {
			if err := q.SaveUptimeState(ctx, saveParams(r.ServiceID, Expire(stateOf(r), now))); err != nil {
				return fmt.Errorf("save uptime state: %w", err)
			}
			ids = append(ids, r.ServiceID)
		}
		return nil
	})
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	refs, err := s.q.ListUptimeServiceRefs(ctx, ids)
	if err != nil {
		return len(ids), fmt.Errorf("load service refs: %w", err)
	}
	for _, r := range refs {
		s.publish(r.ProjectID, r.EnvironmentID, r.ServiceID)
	}
	return len(ids), nil
}

type rollupHourlyWorker struct {
	river.WorkerDefaults[RollupHourlyArgs]
	svc *Service
}

func (w *rollupHourlyWorker) Work(ctx context.Context, _ *river.Job[RollupHourlyArgs]) error {
	now := w.svc.now()
	_, err := w.svc.RollupHourly(ctx, now.Add(-hourlyRecent), now.Add(time.Minute))
	return err
}

type rollupDailyWorker struct {
	river.WorkerDefaults[RollupDailyArgs]
	svc *Service
}

func (w *rollupDailyWorker) Work(ctx context.Context, _ *river.Job[RollupDailyArgs]) error {
	now := w.svc.now().UTC()
	// Repair hours that late uploads touched (whole hours only: the hour that
	// the hourly job still owns is left alone), then roll the hours into days.
	if _, err := w.svc.RollupHourly(ctx, now.Add(-hourlyRepair), now.Add(-hourlyRecent).Truncate(time.Hour)); err != nil {
		return err
	}
	_, err := w.svc.RollupDaily(ctx, now.AddDate(0, 0, -dailyRecentDays), now.Add(time.Minute))
	return err
}

type pruneWorker struct {
	river.WorkerDefaults[PruneArgs]
	svc *Service
	jm  *jobs.Manager
}

func (w *pruneWorker) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	n, err := w.svc.Prune(ctx, w.svc.now())
	if err != nil {
		return err
	}
	if n > 0 {
		w.jm.Logger().InfoContext(ctx, "pruned uptime rows", "count", n)
	}
	return nil
}

type expireWorker struct {
	river.WorkerDefaults[ExpireArgs]
	svc *Service
}

func (w *expireWorker) Work(ctx context.Context, _ *river.Job[ExpireArgs]) error {
	_, err := w.svc.ExpireStale(ctx)
	return err
}

type remindWorker struct {
	river.WorkerDefaults[RemindArgs]
	svc *Service
}

func (w *remindWorker) Work(ctx context.Context, _ *river.Job[RemindArgs]) error {
	_, err := w.svc.SendReminders(ctx)
	return err
}

// RegisterJobs adds the uptime jobs: the hour rollup (every 10 minutes, so the
// running hour is current), the day rollup and prune (hourly) and the
// unknown-state sweep (every minute).
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &rollupHourlyWorker{svc: svc})
		river.AddWorker(w, &rollupDailyWorker{svc: svc})
		river.AddWorker(w, &pruneWorker{svc: svc, jm: jm})
		river.AddWorker(w, &expireWorker{svc: svc})
		river.AddWorker(w, &remindWorker{svc: svc})
	})
	periodic := func(id string, every time.Duration, args river.JobArgs) {
		jm.AddPeriodic(river.NewPeriodicJob(
			river.PeriodicInterval(every),
			func() (river.JobArgs, *river.InsertOpts) { return args, &river.InsertOpts{MaxAttempts: 1} },
			&river.PeriodicJobOpts{ID: id, RunOnStart: true},
		))
	}
	periodic("uptime_rollup_hourly", 10*time.Minute, RollupHourlyArgs{})
	periodic("uptime_rollup_daily", time.Hour, RollupDailyArgs{})
	periodic("uptime_prune", time.Hour, PruneArgs{})
	periodic("uptime_expire", time.Minute, ExpireArgs{})
	periodic("uptime_remind", remindEvery, RemindArgs{})
}
