package instsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

const jobTimeout = 10 * time.Minute

// EnsureArgs is the periodic run of Ensure.
type EnsureArgs struct{}

// Kind implements river.JobArgs.
func (EnsureArgs) Kind() string { return "instsvc_ensure" }

// EnsureTeamArgs provisions one team's slices (queued when a team is created, so
// backups work from the first minute); a failure is retried with back-off.
type EnsureTeamArgs struct {
	TeamID uuid.UUID `json:"teamId"`
}

// Kind implements river.JobArgs.
func (EnsureTeamArgs) Kind() string { return "instsvc_ensure_team" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (EnsureTeamArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8}
}

// UsageArgs is the periodic read of every S3 slice's usage.
type UsageArgs struct{}

// Kind implements river.JobArgs.
func (UsageArgs) Kind() string { return "instsvc_usage" }

// PurgeArgs is the periodic purge of deleted teams' slices past 30 days.
type PurgeArgs struct{}

// Kind implements river.JobArgs.
func (PurgeArgs) Kind() string { return "instsvc_purge" }

type ensureWorker struct {
	river.WorkerDefaults[EnsureArgs]
	svc *Service
}

func (w *ensureWorker) Timeout(*river.Job[EnsureArgs]) time.Duration { return jobTimeout }
func (w *ensureWorker) Work(ctx context.Context, _ *river.Job[EnsureArgs]) error {
	// The outcome of every service is stored on its row; the next run retries.
	if err := w.svc.Ensure(ctx); err != nil {
		w.svc.log.WarnContext(ctx, "ensure instance services", "err", err)
	}
	return nil
}

type ensureTeamWorker struct {
	river.WorkerDefaults[EnsureTeamArgs]
	svc *Service
}

func (w *ensureTeamWorker) Timeout(*river.Job[EnsureTeamArgs]) time.Duration { return jobTimeout }
func (w *ensureTeamWorker) Work(ctx context.Context, job *river.Job[EnsureTeamArgs]) error {
	return w.svc.EnsureTeam(ctx, job.Args.TeamID)
}

type usageWorker struct {
	river.WorkerDefaults[UsageArgs]
	svc *Service
}

func (w *usageWorker) Timeout(*river.Job[UsageArgs]) time.Duration { return jobTimeout }
func (w *usageWorker) Work(ctx context.Context, _ *river.Job[UsageArgs]) error {
	if err := w.svc.Usage(ctx); err != nil {
		w.svc.log.WarnContext(ctx, "read slice usage", "err", err)
	}
	return nil
}

type purgeWorker struct {
	river.WorkerDefaults[PurgeArgs]
	svc *Service
}

func (w *purgeWorker) Timeout(*river.Job[PurgeArgs]) time.Duration { return jobTimeout }
func (w *purgeWorker) Work(ctx context.Context, _ *river.Job[PurgeArgs]) error {
	if err := w.svc.Purge(ctx); err != nil {
		w.svc.log.WarnContext(ctx, "purge team slices", "err", err)
	}
	return nil
}

// RegisterJobs registers the workers and the periodic runs: Ensure every
// minute, the usage read every 5 minutes and the purge hourly.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &ensureWorker{svc: svc})
		river.AddWorker(w, &ensureTeamWorker{svc: svc})
		river.AddWorker(w, &usageWorker{svc: svc})
		river.AddWorker(w, &purgeWorker{svc: svc})
	})
	periodic := func(id string, every time.Duration, args river.JobArgs) {
		jm.AddPeriodic(river.NewPeriodicJob(
			river.PeriodicInterval(every),
			func() (river.JobArgs, *river.InsertOpts) { return args, &river.InsertOpts{MaxAttempts: 1} },
			&river.PeriodicJobOpts{ID: id},
		))
	}
	periodic("instsvc_ensure", EnsureInterval, EnsureArgs{})
	periodic("instsvc_usage", UsageInterval, UsageArgs{})
	periodic("instsvc_purge", PurgeInterval, PurgeArgs{})
}

// EnqueueEnsureTx queues the provisioning of a team's slices inside tx (the
// team's creation), so it exists exactly when the team does.
func (s *Service) EnqueueEnsureTx(ctx context.Context, tx pgx.Tx, teamID uuid.UUID) error {
	if s.jobs == nil || !s.Enabled() {
		return nil
	}
	if err := s.jobs.InsertTx(ctx, tx, EnsureTeamArgs{TeamID: teamID}, nil); err != nil {
		return fmt.Errorf("queue slice provisioning: %w", err)
	}
	return nil
}
