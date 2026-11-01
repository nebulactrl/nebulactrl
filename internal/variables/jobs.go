package variables

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// History retention: the newest versions per key and their maximum age.
const (
	KeepVersions   = 50
	VersionMaxAge  = 180 * 24 * time.Hour
	pruneInterval  = time.Hour
	reminderPeriod = 24 * time.Hour
)

// PruneArgs prunes variable version history.
type PruneArgs struct{}

// Kind implements river.JobArgs.
func (PruneArgs) Kind() string { return "prune_variable_versions" }

// RotationCheckArgs raises rotation reminders.
type RotationCheckArgs struct{}

// Kind implements river.JobArgs.
func (RotationCheckArgs) Kind() string { return "variable_rotation_check" }

type pruneWorker struct {
	river.WorkerDefaults[PruneArgs]
	s *Service
}

type rotationWorker struct {
	river.WorkerDefaults[RotationCheckArgs]
	s *Service
}

// RegisterJobs registers the history prune (hourly) and the rotation reminder
// check (daily). Reminders only ever notify: rotation is never automatic.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &pruneWorker{s: s})
		river.AddWorker(w, &rotationWorker{s: s})
		river.AddWorker(w, &envTemplateScanWorker{s: s})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(pruneInterval),
		func() (river.JobArgs, *river.InsertOpts) { return PruneArgs{}, nil },
		&river.PeriodicJobOpts{ID: "prune_variable_versions"},
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(reminderPeriod),
		func() (river.JobArgs, *river.InsertOpts) { return RotationCheckArgs{}, nil },
		&river.PeriodicJobOpts{ID: "variable_rotation_check"},
	))
}

func (w *pruneWorker) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	_, err := w.s.q.PruneVariableVersions(ctx, db.PruneVariableVersionsParams{Keep: KeepVersions, Before: time.Now().Add(-VersionMaxAge)})
	if err != nil {
		return fmt.Errorf("prune variable versions: %w", err)
	}
	return nil
}

func (w *rotationWorker) Work(ctx context.Context, _ *river.Job[RotationCheckArgs]) error {
	due, err := w.s.q.ListRotationDueVariables(ctx)
	if err != nil {
		return fmt.Errorf("list rotation due: %w", err)
	}
	for _, d := range due {
		var seen bool
		// One reminder per due period: skip when one was recorded after the last change.
		err := w.s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM audit_events WHERE action = 'variable.rotation_due' AND resource_id = $1
			   AND metadata->>'key' = $2 AND created_at > $3)`, d.ServiceID.String(), d.Key, d.UpdatedAt).Scan(&seen)
		if err != nil {
			return err
		}
		if seen || d.RotateEveryDays == nil {
			continue
		}
		sc, err := services.LoadScopeInternal(ctx, w.s.q, d.ServiceID)
		if err != nil {
			continue
		}
		if err := db.InTx(ctx, w.s.pool, func(tx pgx.Tx) error {
			return rotationAudit(ctx, tx, sc, d.Key, int(*d.RotateEveryDays))
		}); err != nil {
			return err
		}
		services.Publish(w.s.bus, services.EventVariableUpdated, sc.ProjectID, sc.EnvironmentID, sc.Service.ID)
	}
	return nil
}
