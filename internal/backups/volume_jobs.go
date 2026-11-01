package backups

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// VolumeBackupTickArgs evaluates due volume backup schedules.
type VolumeBackupTickArgs struct{}

// Kind implements river.JobArgs.
func (VolumeBackupTickArgs) Kind() string { return "volume_backup_tick" }

type volumeTickWorker struct {
	river.WorkerDefaults[VolumeBackupTickArgs]
	s *Service
}

func (w *volumeTickWorker) Work(ctx context.Context, _ *river.Job[VolumeBackupTickArgs]) error {
	return w.s.VolumeTick(ctx)
}

// volumeStaleAfter is how long a queued/running volume backup may take before
// it is failed: the agent's maximum plus a grace period.
const volumeStaleAfter = time.Duration(agentprotocol.VolumeBackupMaxSeconds)*time.Second + 10*time.Minute

// VolumeTick starts every due scheduled volume backup. A schedule whose server
// is offline or whose service has no volumes records a "skipped" run with the
// reason instead of pretending to have run. Stale runs are failed.
func (s *Service) VolumeTick(ctx context.Context) error {
	now := s.now()
	var started []db.VolumeBackup
	var skipped []db.VolumeBackup
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		due, err := q.ListDueVolumeBackupConfigs(ctx, &now)
		if err != nil {
			return fmt.Errorf("list due volume backup configs: %w", err)
		}
		for _, cfg := range due {
			cfg := cfg
			// A savepoint per config: one broken config must not block the rest.
			err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
				b, skip, err := s.runVolumeSchedule(ctx, sp, cfg, now)
				if err != nil {
					return err
				}
				if skip {
					skipped = append(skipped, b)
				} else if b.ID != uuid.Nil {
					started = append(started, b)
				}
				return nil
			})
			if err != nil {
				s.log.ErrorContext(ctx, "scheduled volume backup failed to start", "service_id", cfg.ServiceID, "err", err)
				// Move on so a persistent error does not retry every minute.
				s.advanceVolumeAfterError(ctx, tx, cfg, now)
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
	for _, b := range skipped {
		s.afterVolumeBackupFinished(ctx, b, NotifyBackupSkipped)
	}
	stale, err := s.q.FailStaleVolumeBackups(ctx, now.Add(-volumeStaleAfter))
	if err != nil {
		return fmt.Errorf("fail stale volume backups: %w", err)
	}
	for _, b := range stale {
		s.afterVolumeBackupFinished(ctx, b, NotifyBackupFailed)
	}
	return nil
}

// advanceVolumeAfterError moves next_run_at forward after a failure to start.
func (s *Service) advanceVolumeAfterError(ctx context.Context, tx pgx.Tx, cfg db.VolumeBackupConfig, now time.Time) {
	next, err := nextRun(cfg.Cron, cfg.Timezone, now)
	var np *time.Time
	if err == nil {
		np = &next
	}
	_ = db.New(tx).AdvanceVolumeBackupConfig(ctx, db.AdvanceVolumeBackupConfigParams{ServiceID: cfg.ServiceID, LastRunAt: &now, NextRunAt: np})
}

// runVolumeSchedule advances one due config and starts (or skips) its backup.
func (s *Service) runVolumeSchedule(ctx context.Context, tx pgx.Tx, cfg db.VolumeBackupConfig, now time.Time) (db.VolumeBackup, bool, error) {
	q := db.New(tx)
	next, nerr := nextRun(cfg.Cron, cfg.Timezone, now)
	var np *time.Time
	if nerr == nil {
		np = &next
	}
	if err := q.AdvanceVolumeBackupConfig(ctx, db.AdvanceVolumeBackupConfigParams{ServiceID: cfg.ServiceID, LastRunAt: &now, NextRunAt: np}); err != nil {
		return db.VolumeBackup{}, false, err
	}
	sc, err := services.LoadScopeInternal(ctx, q, cfg.ServiceID)
	if apierr.IsNotFound(err) {
		return db.VolumeBackup{}, false, nil // the service was deleted after the config fired: skip this run, the config cascades away
	}
	if err != nil {
		return db.VolumeBackup{}, false, fmt.Errorf("load service: %w", err)
	}
	dest, err := q.GetBackupDestinationInternal(ctx, cfg.DestinationID)
	if err != nil {
		return db.VolumeBackup{}, false, fmt.Errorf("load destination: %w", err)
	}
	vols, err := q.ListServiceVolumes(ctx, cfg.ServiceID)
	if err != nil {
		return db.VolumeBackup{}, false, err
	}
	in := volumeStart{sc: sc, vols: vols, cfg: cfg, dest: dest, trigger: TriggerSchedule, actor: "schedule"}
	skip := func(reason string) (db.VolumeBackup, bool, error) {
		b, err := s.insertVolumeSkipped(ctx, q, in, reason)
		return b, true, err
	}
	if why := volumeIneligibleReason(sc, vols); why != "" {
		return skip("the service can not be backed up: " + why)
	}
	name, online, err := s.serverState(ctx, q, sc.Service.ServerID)
	if err != nil {
		return db.VolumeBackup{}, false, err
	}
	if !online {
		return skip("the server " + name + " is offline")
	}
	if why := s.builtinSkipReason(ctx, dest); why != "" {
		return skip(why)
	}
	b, err := s.startVolumeBackup(ctx, tx, in)
	if err != nil {
		return db.VolumeBackup{}, false, err
	}
	return b, false, nil
}
