package meshlinks

import (
	"context"
	"errors"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// ReconcileArgs is the periodic River job that repairs the grants of every
// environment that still has links or exposures (mode repair: it only removes
// grants without a source and re-creates rows of existing links).
type ReconcileArgs struct{}

// Kind implements river.JobArgs.
func (ReconcileArgs) Kind() string { return "mesh_links_reconcile" }

type reconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]
	s *Service
}

// RegisterJobs registers MeshLinksReconcileJob (every RepairInterval).
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &reconcileWorker{s: s}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(RepairInterval),
		func() (river.JobArgs, *river.InsertOpts) { return ReconcileArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "mesh_links_reconcile"},
	))
}

func (w *reconcileWorker) Work(ctx context.Context, _ *river.Job[ReconcileArgs]) error {
	return w.s.RepairAll(ctx)
}

// RepairAll runs the repair of every environment with links or exposures. One
// failing environment does not stop the others; the errors are joined.
func (s *Service) RepairAll(ctx context.Context) error {
	if s.mesh == nil {
		return nil
	}
	ids, err := s.q.ListMeshEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("list mesh environments: %w", err)
	}
	var errs []error
	for _, id := range ids {
		if _, err := s.ReconcileEnv(ctx, id, ModeRepair, Options{}); err != nil {
			s.logger.WarnContext(ctx, "mesh link repair", "environment_id", id.String(), "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
