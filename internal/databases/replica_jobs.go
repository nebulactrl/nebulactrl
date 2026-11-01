package databases

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// ReplicaAdvanceArgs moves the saga of one replica (create, remove, rebuild).
type ReplicaAdvanceArgs struct {
	ServiceID uuid.UUID `json:"serviceId"`
}

// Kind implements river.JobArgs.
func (ReplicaAdvanceArgs) Kind() string { return "database_replica_advance" }

// PromotionAdvanceArgs moves one promotion.
type PromotionAdvanceArgs struct {
	PromotionID uuid.UUID `json:"promotionId"`
}

// Kind implements river.JobArgs.
func (PromotionAdvanceArgs) Kind() string { return "database_promotion_advance" }

// ReplicaSweepArgs is the periodic check of replicas and promotions.
type ReplicaSweepArgs struct{}

// Kind implements river.JobArgs.
func (ReplicaSweepArgs) Kind() string { return "database_replica_sweep" }

// ReplicaSweepInterval is how often the sweep runs.
const ReplicaSweepInterval = 30 * time.Second

// snoozeEvery is how long a saga waits before it looks again.
const snoozeEvery = 5 * time.Second

type replicaAdvanceWorker struct {
	river.WorkerDefaults[ReplicaAdvanceArgs]
	s *Service
}

func (w *replicaAdvanceWorker) Work(ctx context.Context, job *river.Job[ReplicaAdvanceArgs]) error {
	done, err := w.s.AdvanceReplica(ctx, job.Args.ServiceID)
	if err != nil {
		return err
	}
	if !done {
		return river.JobSnooze(snoozeEvery)
	}
	return nil
}

type promotionAdvanceWorker struct {
	river.WorkerDefaults[PromotionAdvanceArgs]
	s *Service
}

func (w *promotionAdvanceWorker) Work(ctx context.Context, job *river.Job[PromotionAdvanceArgs]) error {
	done, err := w.s.AdvancePromotion(ctx, job.Args.PromotionID)
	if err != nil {
		return err
	}
	if !done {
		return river.JobSnooze(snoozeEvery)
	}
	return nil
}

type replicaSweepWorker struct {
	river.WorkerDefaults[ReplicaSweepArgs]
	s *Service
}

func (w *replicaSweepWorker) Work(ctx context.Context, _ *river.Job[ReplicaSweepArgs]) error {
	return w.s.SweepReplicas(ctx)
}

// RegisterReplicaJobs adds the replica saga, promotion and sweep workers and
// the periodic sweep.
func RegisterReplicaJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &replicaAdvanceWorker{s: svc})
		river.AddWorker(w, &promotionAdvanceWorker{s: svc})
		river.AddWorker(w, &replicaSweepWorker{s: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(ReplicaSweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return ReplicaSweepArgs{}, nil },
		&river.PeriodicJobOpts{ID: "database_replica_sweep"},
	))
}

// uniqueOpts keeps one pending job per args, so the sweep re-kicking a saga
// never piles up duplicates.
func uniqueOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		},
	}}
}

// SweepReplicas re-kicks the sagas that lost their job (a replica stuck in a
// transitional state, a running promotion) and applies what the observations
// alone cannot: a replica whose primary reports its slot lost needs a rebuild.
// Stale observations are not a state: the view shows them as unknown.
func (s *Service) SweepReplicas(ctx context.Context) error {
	if s.rep.Jobs == nil {
		return nil
	}
	rows, err := s.q.ListDatabaseReplicasInSaga(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.rep.Jobs.Insert(ctx, ReplicaAdvanceArgs{ServiceID: r.ServiceID}, uniqueOpts()); err != nil {
			s.logger.ErrorContext(ctx, "kick replica saga", "service_id", r.ServiceID, "err", err)
		}
	}
	promos, err := s.q.ListRunningDatabasePromotions(ctx)
	if err != nil {
		return err
	}
	for _, p := range promos {
		if err := s.rep.Jobs.Insert(ctx, PromotionAdvanceArgs{PromotionID: p.ID}, uniqueOpts()); err != nil {
			s.logger.ErrorContext(ctx, "kick promotion", "promotion_id", p.ID, "err", err)
		}
	}
	obs, err := s.q.ListDatabaseReplicasObservable(ctx)
	if err != nil {
		return err
	}
	for _, r := range obs {
		if r.WalStatus != nil && *r.WalStatus == "lost" && r.State != ReplicaNeedsRebuild {
			if err := s.transition(ctx, r, ReplicaNeedsRebuild, "The primary dropped the replica's replication slot (WAL was removed): rebuild the replica.", r.Step); err != nil {
				s.logger.ErrorContext(ctx, "replica needs rebuild", "service_id", r.ServiceID, "err", err)
			}
		}
	}
	return nil
}
