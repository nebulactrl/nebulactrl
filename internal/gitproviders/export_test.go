package gitproviders

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// SignForTest returns the hex HMAC-SHA256 webhook signature of body.
func SignForTest(secret string, body []byte) string { return hmacSHA256Hex(secret, body) }

// SetNow replaces the service clock.
func (s *Service) SetNow(fn func() time.Time) { s.now = fn }

// RunSweepJobForTest runs the periodic sweep job the way the job runner does.
func (s *Service) RunSweepJobForTest(ctx context.Context) error {
	return (&sweepWorker{s: s}).Work(ctx, &river.Job[SweepArgs]{})
}

// RunReportJobForTest runs the immediate commit status job of a deployment.
func (s *Service) RunReportJobForTest(ctx context.Context, deploymentID uuid.UUID) error {
	return (&reportWorker{s: s}).Work(ctx, &river.Job[ReportStatusArgs]{Args: ReportStatusArgs{DeploymentID: deploymentID}})
}

// RunMaintenanceJobForTest runs the hourly housekeeping job.
func (s *Service) RunMaintenanceJobForTest(ctx context.Context) error {
	return (&maintenanceWorker{s: s}).Work(ctx, &river.Job[MaintenanceArgs]{})
}

// EnqueueStatusForTest schedules an immediate commit status report.
func (s *Service) EnqueueStatusForTest(ctx context.Context, deploymentID uuid.UUID) {
	s.enqueueStatus(ctx, deploymentID)
}
