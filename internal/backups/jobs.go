package backups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// TickInterval is how often schedules are evaluated.
const TickInterval = time.Minute

// BackupTickArgs evaluates due backup schedules.
type BackupTickArgs struct{}

// Kind implements river.JobArgs.
func (BackupTickArgs) Kind() string { return "backup_tick" }

// RestoreAdvanceArgs moves a restore forward once its new database runs.
type RestoreAdvanceArgs struct {
	RestoreID uuid.UUID `json:"restoreId"`
}

// Kind implements river.JobArgs.
func (RestoreAdvanceArgs) Kind() string { return "backup_restore_advance" }

type tickWorker struct {
	river.WorkerDefaults[BackupTickArgs]
	s *Service
}

func (w *tickWorker) Work(ctx context.Context, _ *river.Job[BackupTickArgs]) error {
	return w.s.Tick(ctx)
}

type advanceWorker struct {
	river.WorkerDefaults[RestoreAdvanceArgs]
	s *Service
}

func (w *advanceWorker) Work(ctx context.Context, job *river.Job[RestoreAdvanceArgs]) error {
	done, err := w.s.AdvanceRestore(ctx, job.Args.RestoreID)
	if err != nil {
		return err
	}
	if !done {
		return river.JobSnooze(5 * time.Second)
	}
	return nil
}

// RegisterJobs adds the backup workers and the one-minute schedule tick.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &tickWorker{s: svc})
		river.AddWorker(w, &advanceWorker{s: svc})
		river.AddWorker(w, &volumeTickWorker{s: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(TickInterval),
		func() (river.JobArgs, *river.InsertOpts) { return BackupTickArgs{}, nil },
		&river.PeriodicJobOpts{ID: "backup_tick"},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(TickInterval),
		func() (river.JobArgs, *river.InsertOpts) { return VolumeBackupTickArgs{}, nil },
		&river.PeriodicJobOpts{ID: "volume_backup_tick"},
	))
	registerMissedJob(jm, svc)
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &dumpAdvanceWorker{s: svc})
		river.AddWorker(w, &dumpSweepWorker{s: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(TickInterval),
		func() (river.JobArgs, *river.InsertOpts) { return DumpRestoreSweepArgs{}, nil },
		&river.PeriodicJobOpts{ID: "dump_restore_sweep"},
	))
}

// DumpRestoreAdvanceArgs moves a dump restore forward once its new database runs.
type DumpRestoreAdvanceArgs struct {
	RestoreID uuid.UUID `json:"restoreId"`
}

// Kind implements river.JobArgs.
func (DumpRestoreAdvanceArgs) Kind() string { return "dump_restore_advance" }

// DumpRestoreSweepArgs fails dump restores that can no longer progress.
type DumpRestoreSweepArgs struct{}

// Kind implements river.JobArgs.
func (DumpRestoreSweepArgs) Kind() string { return "dump_restore_sweep" }

type dumpAdvanceWorker struct {
	river.WorkerDefaults[DumpRestoreAdvanceArgs]
	s *Service
}

func (w *dumpAdvanceWorker) Work(ctx context.Context, job *river.Job[DumpRestoreAdvanceArgs]) error {
	done, err := w.s.AdvanceDumpRestore(ctx, job.Args.RestoreID)
	if err != nil {
		return err
	}
	if !done {
		return river.JobSnooze(5 * time.Second)
	}
	return nil
}

type dumpSweepWorker struct {
	river.WorkerDefaults[DumpRestoreSweepArgs]
	s *Service
}

func (w *dumpSweepWorker) Work(ctx context.Context, _ *river.Job[DumpRestoreSweepArgs]) error {
	return w.s.SweepDumpRestores(ctx)
}

type finished struct {
	b   db.Backup
	typ string
}

// Tick starts every due scheduled backup. A schedule whose database is stopped
// or whose server is offline records a "skipped" run with the reason instead
// of pretending to have run. Stale runs are failed.
func (s *Service) Tick(ctx context.Context) error {
	now := s.now()
	var started []db.Backup
	var skipped []finished
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		due, err := q.ListDueBackupSchedules(ctx, &now)
		if err != nil {
			return fmt.Errorf("list due schedules: %w", err)
		}
		for _, sch := range due {
			sch := sch
			// A savepoint per schedule: one broken schedule must not block the rest.
			err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
				b, skip, err := s.runSchedule(ctx, sp, sch, now)
				if err != nil {
					return err
				}
				if skip {
					skipped = append(skipped, finished{b: b, typ: NotifyBackupSkipped})
				} else if b.ID != uuid.Nil {
					started = append(started, b)
				}
				return nil
			})
			if err != nil {
				s.log.ErrorContext(ctx, "scheduled backup failed to start", "schedule_id", sch.ID, "err", err)
				// Move on so a persistent error does not retry every minute.
				s.advanceAfterError(ctx, tx, sch, now)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, b := range started {
		if sc, err := services.LoadScopeInternal(ctx, s.q, b.ServiceID); err == nil {
			s.publish(sc, b.ID)
		}
	}
	for _, f := range skipped {
		s.afterBackupFinished(ctx, f.b, f.typ)
	}
	stale, err := s.q.FailStaleBackups(ctx)
	if err != nil {
		return fmt.Errorf("fail stale backups: %w", err)
	}
	for _, b := range stale {
		s.afterBackupFinished(ctx, b, NotifyBackupFailed)
	}
	return nil
}

// advanceAfterError moves next_run_at forward after a failure to start.
func (s *Service) advanceAfterError(ctx context.Context, tx pgx.Tx, sch db.BackupSchedule, now time.Time) {
	next, err := nextRun(sch.Cron, sch.Timezone, now)
	var np *time.Time
	if err == nil {
		np = &next
	}
	_ = db.New(tx).AdvanceBackupSchedule(ctx, db.AdvanceBackupScheduleParams{ID: sch.ID, LastRunAt: &now, NextRunAt: np})
}

// runSchedule advances one due schedule and starts (or skips) its backup.
func (s *Service) runSchedule(ctx context.Context, tx pgx.Tx, sch db.BackupSchedule, now time.Time) (db.Backup, bool, error) {
	q := db.New(tx)
	next, nerr := nextRun(sch.Cron, sch.Timezone, now)
	var np *time.Time
	if nerr == nil {
		np = &next
	}
	if err := q.AdvanceBackupSchedule(ctx, db.AdvanceBackupScheduleParams{ID: sch.ID, LastRunAt: &now, NextRunAt: np}); err != nil {
		return db.Backup{}, false, err
	}
	sc, err := services.LoadScopeInternal(ctx, q, sch.ServiceID)
	if apierr.IsNotFound(err) {
		return db.Backup{}, false, nil // the database was deleted after the schedule fired: skip this run, the schedule cascades away
	}
	if err != nil {
		return db.Backup{}, false, fmt.Errorf("load service: %w", err)
	}
	dest, err := q.GetBackupDestinationInternal(ctx, sch.DestinationID)
	if err != nil {
		return db.Backup{}, false, fmt.Errorf("load destination: %w", err)
	}
	in := startInput{sc: sc, dest: dest, schedule: &sch, trigger: TriggerSchedule, actor: "schedule"}
	skip := func(reason string) (db.Backup, bool, error) {
		b, err := s.insertSkipped(ctx, q, in, reason)
		return b, true, err
	}
	tgt, err := s.resolveTarget(ctx, q, sc)
	if err != nil {
		return skip("the database can not be backed up: " + err.Error())
	}
	in.tgt = tgt
	if sc.Service.ServerID == nil {
		return skip("the database is not placed on a server")
	}
	name, online, err := s.serverState(ctx, q, sc.Service.ServerID)
	if err != nil {
		return db.Backup{}, false, err
	}
	if !online {
		return skip("the server " + name + " is offline")
	}
	if ok, why := databaseRunning(sc.Service, now); !ok {
		return skip(why)
	}
	if why := s.builtinSkipReason(ctx, dest); why != "" {
		return skip(why)
	}
	b, err := s.startBackup(ctx, tx, in)
	if err != nil {
		return db.Backup{}, false, err
	}
	return b, false, nil
}

// AdvanceRestore moves a restore of a new database forward: once the target's
// deployment succeeded it sends the db_restore command; if the deployment
// failed or the wait times out the restore fails. It returns done=true when
// nothing is left to wait for.
func (s *Service) AdvanceRestore(ctx context.Context, restoreID uuid.UUID) (bool, error) {
	r, err := s.q.GetBackupRestoreInternal(ctx, restoreID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if r.Status != RestoreProvisioning {
		return true, nil
	}
	if s.now().Sub(r.CreatedAt) > restoreTimeout {
		s.finishRestore(ctx, r.ID, RestoreFailed, "The new database did not become ready within 30 minutes.")
		return true, nil
	}
	if r.DeploymentID == nil {
		return false, nil // Restore() is still starting the deployment
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*r.DeploymentID})
	if err != nil {
		return false, err
	}
	if len(deps) == 0 {
		s.finishRestore(ctx, r.ID, RestoreFailed, "The new database's deployment no longer exists.")
		return true, nil
	}
	switch dep := deps[0]; dep.Status {
	case "succeeded":
		if err := s.sendRestore(ctx, r); err != nil {
			s.finishRestore(ctx, r.ID, RestoreFailed, "could not start the restore: "+err.Error())
		}
		return true, nil
	case "failed", "canceled", "rolled_back", "rollback_failed":
		msg := dep.StatusMessage
		if msg == "" {
			msg = dep.Status
		}
		s.finishRestore(ctx, r.ID, RestoreFailed, "The new database could not be deployed: "+clip(msg))
		return true, nil
	}
	return false, nil
}
