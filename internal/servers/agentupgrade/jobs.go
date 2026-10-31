package agentupgrade

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// RolloutArgs is the staged fleet upgrade, as a chain of jobs: each job looks
// at one server (Index) and either starts its upgrade and waits, moves on to the
// next server, or stops the rollout.
type RolloutArgs struct {
	ServerIDs []uuid.UUID `json:"serverIds"`
	Index     int         `json:"index"`
	// StartedAt separates an upgrade this rollout started from an older row.
	StartedAt time.Time  `json:"startedAt"`
	By        *uuid.UUID `json:"by,omitempty"`
	Force     bool       `json:"force,omitempty"`
}

// Kind implements river.JobArgs.
func (RolloutArgs) Kind() string { return "agent_upgrade_rollout" }

// step is what a rollout job does next.
type step int

const (
	stepDone step = iota // the rollout is over
	stepWait             // look at the same server again later
	stepNext             // continue with the next server
)

// RolloutStep advances the rollout by one look at server a.ServerIDs[a.Index].
func (s *Service) RolloutStep(ctx context.Context, a RolloutArgs) (step, error) {
	if a.Index >= len(a.ServerIDs) {
		return stepDone, nil
	}
	id := a.ServerIDs[a.Index]
	srv, err := s.q.GetServerByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return stepNext, nil // deleted meanwhile
		}
		return stepDone, err
	}
	row, err := s.q.GetServerAgentUpgrade(ctx, id)
	hasRow := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return stepDone, err
	}
	if !hasRow || row.RequestedAt.Before(a.StartedAt) {
		if _, err := s.start(ctx, srv, TriggerManual, a.By, a.Force); err != nil {
			if _, expected := apierr.As(err); expected {
				s.log.InfoContext(ctx, "rollout skips a server", "server_id", id, "reason", err.Error())
				return stepNext, nil // became offline, current or busy since the plan
			}
			return stepDone, err
		}
		return stepWait, nil
	}
	switch row.State {
	case StateSucceeded:
		return stepNext, nil
	case StateFailed:
		return stepDone, s.haltRollout(ctx, a, srv, row.Message)
	}
	if s.now().Sub(row.RequestedAt) > RolloutServerTimeout {
		msg := "The agent did not come back with the new version in time."
		if err := s.finish(ctx, row, StateFailed, msg, []string{StateQueued, StateRestarting}); err != nil {
			return stepDone, err
		}
		return stepDone, s.haltRollout(ctx, a, srv, msg)
	}
	return stepWait, nil
}

func (s *Service) haltRollout(ctx context.Context, a RolloutArgs, srv db.Server, why string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRolloutHalt, ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "position": a.Index + 1, "of": len(a.ServerIDs), "reason": why},
		})
	})
}

type rolloutWorker struct {
	river.WorkerDefaults[RolloutArgs]
	svc *Service
}

func (w *rolloutWorker) Work(ctx context.Context, job *river.Job[RolloutArgs]) error {
	st, err := w.svc.RolloutStep(ctx, job.Args)
	if err != nil {
		return err
	}
	switch st {
	case stepWait:
		return river.JobSnooze(RolloutPoll)
	case stepNext:
		next := job.Args
		next.Index++
		if next.Index >= len(next.ServerIDs) {
			return nil
		}
		return w.svc.jm.Insert(ctx, next, nil)
	}
	return nil
}

// RegisterJobs registers the rollout worker.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &rolloutWorker{svc: svc}) })
}
