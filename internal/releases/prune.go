// Package releases keeps the secret values stored in releases from living
// forever (docs/security/secrets-at-rest.md, "Release secret retention").
//
// Every release holds the sealed environment it was deployed with
// (releases.env_enc), secret values included, so a rollback restores exactly
// what ran. Without retention a variable that was deleted or rotated stays in
// the database, and in every dump, as long as the service exists. The prune job
// drops env_enc from every release outside its service's rollback window, the
// newest imageRetention succeeded releases, and keeps the running release and any
// release a deployment in flight uses.
package releases

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// PruneEvery is how often the retention job runs.
const PruneEvery = time.Hour

// PrunedNotice is the sentence shown with a rollback to a release whose
// environment was pruned: that rollback deploys the old image and configuration
// with the service's current variables.
const PrunedNotice = "The variable values of this release were removed after the rollback window (older than the newest image-retention releases); " +
	"it is deployed with the service's current variables instead."

// Service prunes release environments.
type Service struct {
	q      *db.Queries
	logger *slog.Logger
}

// NewService returns the retention service.
func NewService(pool *pgxpool.Pool, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{q: db.New(pool), logger: logger}
}

// Prune drops the sealed environment of every release outside its service's
// rollback window and returns how many releases it cleared.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	n, err := s.q.PruneReleaseEnv(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune release environments: %w", err)
	}
	if n > 0 {
		s.logger.InfoContext(ctx, "pruned release environments outside the rollback window", "releases", n)
	}
	return n, nil
}

// PruneArgs drives the retention job (periodic).
type PruneArgs struct{}

// Kind implements river.JobArgs.
func (PruneArgs) Kind() string { return "release_env_prune" }

type pruneWorker struct {
	river.WorkerDefaults[PruneArgs]
	s *Service
}

func (w *pruneWorker) Work(ctx context.Context, _ *river.Job[PruneArgs]) error {
	_, err := w.s.Prune(ctx)
	return err
}

// RegisterJobs registers the hourly retention job.
func RegisterJobs(jm *jobs.Manager, s *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &pruneWorker{s: s}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(PruneEvery),
		func() (river.JobArgs, *river.InsertOpts) { return PruneArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "release_env_prune"},
	))
}
