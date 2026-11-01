package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// TickInterval is how often cron tasks are evaluated.
const TickInterval = time.Minute

// TaskTickArgs evaluates due cron tasks and fails runs that were lost.
type TaskTickArgs struct{}

// Kind implements river.JobArgs.
func (TaskTickArgs) Kind() string { return "task_tick" }

type tickWorker struct {
	river.WorkerDefaults[TaskTickArgs]
	s *Service
}

func (w *tickWorker) Work(ctx context.Context, _ *river.Job[TaskTickArgs]) error {
	return w.s.Tick(ctx)
}

// RegisterJobs adds the task worker and the one-minute scheduler tick.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &tickWorker{s: svc}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(TickInterval),
		func() (river.JobArgs, *river.InsertOpts) { return TaskTickArgs{}, nil },
		&river.PeriodicJobOpts{ID: "task_tick"},
	))
}

// Tick starts every due cron task, then fails runs whose agent never reported
// back. A task that is due is advanced to its next run first and runs at most
// once per tick, however long the control plane was away (a missed hour is not
// replayed). A tick that can not run is recorded as a skipped run with the
// reason instead of silently disappearing: the previous run is still going
// (concurrency forbid), the service is stopped or not deployed, or its server
// is offline.
func (s *Service) Tick(ctx context.Context) error {
	now := s.now()
	var recorded []db.TaskRun
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		due, err := q.ListDueServiceTasks(ctx, &now)
		if err != nil {
			return fmt.Errorf("list due tasks: %w", err)
		}
		for _, t := range due {
			// A savepoint per task: one broken task must not block the rest.
			err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
				run, err := s.runDue(ctx, sp, t, now)
				if err != nil {
					return err
				}
				if run.ID != uuid.Nil {
					recorded = append(recorded, run)
				}
				return nil
			})
			if err != nil {
				s.log.ErrorContext(ctx, "scheduled task failed to start", "task_id", t.ID, "err", err)
				// Move on so a persistent error does not retry every minute.
				s.advance(ctx, tx, t, now)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range recorded {
		if sc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID); err == nil {
			s.publishRun(sc, r.ID)
		}
	}
	stale, err := s.q.FailStaleTaskRuns(ctx)
	if err != nil {
		return fmt.Errorf("fail stale task runs: %w", err)
	}
	for _, r := range stale {
		if sc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID); err == nil {
			s.publishRun(sc, r.ID)
			s.notifyFailure(ctx, sc, r)
		}
	}
	return nil
}

// advance moves a task's next run past now.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, t db.ServiceTask, now time.Time) {
	var np *time.Time
	if next, err := nextRun(t.Schedule, t.Timezone, now); err == nil {
		np = &next
	}
	_ = db.New(tx).AdvanceServiceTask(ctx, db.AdvanceServiceTaskParams{ID: t.ID, LastRunAt: &now, NextRunAt: np})
}

// runDue advances one due task and starts (or skips) its run. The returned run
// is zero when nothing was recorded (the service was deleted, or the same skip
// reason is already the task's latest entry).
func (s *Service) runDue(ctx context.Context, tx pgx.Tx, t db.ServiceTask, now time.Time) (db.TaskRun, error) {
	q := db.New(tx)
	next, nerr := nextRun(t.Schedule, t.Timezone, now)
	var np *time.Time
	if nerr == nil {
		np = &next
	}
	if err := q.AdvanceServiceTask(ctx, db.AdvanceServiceTaskParams{ID: t.ID, LastRunAt: &now, NextRunAt: np}); err != nil {
		return db.TaskRun{}, err
	}
	sc, err := services.LoadScopeInternal(ctx, q, t.ServiceID)
	if err != nil {
		return db.TaskRun{}, nil // service deleted
	}
	in := startInput{
		sc: sc, task: &t, name: t.Name, source: SourceCron, command: decodeArgv(t.Command),
		timeout: int(t.TimeoutSeconds), actor: "schedule", scheduledFor: t.NextRunAt,
	}
	skip := func(reason string) (db.TaskRun, error) {
		// One skipped row per streak: a task that can not run every minute for
		// hours must not push its real history out of the 50 kept runs.
		last, err := q.ListServiceTaskRuns(ctx, db.ListServiceTaskRunsParams{ServiceID: t.ServiceID, TaskID: &t.ID, RowLimit: 1})
		if err == nil && len(last) == 1 && last[0].TaskRun.Status == StatusSkipped && last[0].TaskRun.Error == reason {
			return db.TaskRun{}, nil
		}
		r, err := s.insertSkipped(ctx, q, in, reason)
		if err != nil {
			return r, err
		}
		if _, err := q.PruneTaskRuns(ctx, db.PruneTaskRunsParams{ServiceID: t.ServiceID, TaskID: &t.ID, Keep: HistoryPerTask}); err != nil {
			s.log.WarnContext(ctx, "prune task run history", "task_id", t.ID, "err", err)
		}
		return r, nil
	}
	if t.Concurrency == ConcurrencyForbid {
		n, err := q.CountActiveTaskRuns(ctx, &t.ID)
		if err != nil {
			return db.TaskRun{}, err
		}
		if n > 0 {
			return skip("the previous run is still running (concurrency: forbid)")
		}
	}
	if sc.Service.DesiredState == "stopped" {
		return skip("the service is stopped")
	}
	if _, b, err := s.runnable(ctx, q, sc); err != nil {
		return db.TaskRun{}, err
	} else if b != nil {
		return skip(b.msg)
	}
	return s.startRun(ctx, tx, in)
}
