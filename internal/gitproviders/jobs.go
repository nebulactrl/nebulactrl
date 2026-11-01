package gitproviders

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// SweepArgs is the periodic job that deploys queued pushes and brings the
// providers' commit statuses up to date.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "git_sweep" }

// ReportStatusArgs reports one deployment's commit status right away (the
// sweep catches up on later states).
type ReportStatusArgs struct {
	DeploymentID uuid.UUID `json:"deploymentId"`
}

// Kind implements river.JobArgs.
func (ReportStatusArgs) Kind() string { return "git_report_status" }

// MaintenanceArgs is the hourly housekeeping job: it forgets old delivery ids
// and log rows, removes webhooks nobody uses and drops abandoned connection flows.
type MaintenanceArgs struct{}

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return "git_maintenance" }

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	s *Service
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return w.s.Sweep(ctx)
}

type reportWorker struct {
	river.WorkerDefaults[ReportStatusArgs]
	s *Service
}

func (w *reportWorker) Work(ctx context.Context, j *river.Job[ReportStatusArgs]) error {
	return w.s.reportDeployment(ctx, j.Args.DeploymentID)
}

type maintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	s *Service
}

func (w *maintenanceWorker) Work(ctx context.Context, _ *river.Job[MaintenanceArgs]) error {
	return w.s.Maintain(ctx)
}

// RegisterJobs registers the workers and periodic jobs.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &sweepWorker{s: s})
		river.AddWorker(w, &reportWorker{s: s})
		river.AddWorker(w, &maintenanceWorker{s: s})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Second),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		nil,
	))
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return MaintenanceArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		nil,
	))
}

// enqueueStatus schedules an immediate commit status report.
func (s *Service) enqueueStatus(ctx context.Context, deploymentID uuid.UUID) {
	if s.jobs == nil {
		return
	}
	if err := s.jobs.Insert(ctx, ReportStatusArgs{DeploymentID: deploymentID}, &river.InsertOpts{MaxAttempts: 3}); err != nil {
		s.log.Warn("enqueue commit status report", "deployment", deploymentID.String(), "err", err)
	}
}

// Sweep deploys queued pushes of idle services and reports the commit status
// of every linked deployment whose provider status is behind.
func (s *Service) Sweep(ctx context.Context) error {
	if _, err := s.deployQueued(ctx); err != nil {
		return err
	}
	ids, err := s.q.ListDeploymentsNeedingGitStatus(ctx)
	if err != nil {
		return fmt.Errorf("list deployments needing a commit status: %w", err)
	}
	for _, id := range ids {
		if err := s.reportDeployment(ctx, id); err != nil {
			s.log.Warn("report commit status", "deployment", id.String(), "err", err)
		}
	}
	return nil
}

// Maintain runs the hourly housekeeping.
func (s *Service) Maintain(ctx context.Context) error {
	if _, err := s.q.PruneGitDeliveryIDs(ctx, s.now().Add(-DeliveryIDTTL)); err != nil {
		return fmt.Errorf("prune delivery ids: %w", err)
	}
	if _, err := s.q.PruneGitDeliveries(ctx); err != nil {
		return fmt.Errorf("prune deliveries: %w", err)
	}
	if _, err := s.q.DeleteExpiredPendingGitConnections(ctx); err != nil {
		return fmt.Errorf("drop abandoned connections: %w", err)
	}
	hooks, err := s.q.ListOrphanGitWebhooks(ctx)
	if err != nil {
		return fmt.Errorf("list orphan webhooks: %w", err)
	}
	for _, h := range hooks {
		c, err := s.loadConnectionInternal(ctx, h.ConnectionID)
		if err != nil {
			continue
		}
		s.removeWebhook(ctx, c, h.RepoFullName)
	}
	return nil
}
