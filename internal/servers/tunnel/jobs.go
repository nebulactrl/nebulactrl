package tunnel

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

// SweepInterval is how often the tunnels are checked: ingress rules against the
// server's hostnames, and the connector against what the agent reports.
const SweepInterval = time.Minute

// resyncAfter is the minimum gap between two sync_tunnel commands the sweep
// queues for the same server.
const resyncAfter = 5 * time.Minute

// staleAfter is how old the last observation of a connector on an online server
// may get before the sweep re-sends the connector command.
const staleAfter = 3 * time.Minute

const jobTimeout = 90 * time.Second

var uniqueStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

// DeleteArgs deletes a removing tunnel at Cloudflare.
type DeleteArgs struct {
	ServerID uuid.UUID `json:"serverId"`
}

// Kind implements river.JobArgs.
func (DeleteArgs) Kind() string { return "server_tunnel_delete" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (DeleteArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// IngressArgs pushes a server's ingress rules to Cloudflare.
type IngressArgs struct {
	ServerID uuid.UUID `json:"serverId"`
}

// Kind implements river.JobArgs.
func (IngressArgs) Kind() string { return "server_tunnel_ingress" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (IngressArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// SweepArgs checks every tunnel.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "server_tunnel_sweep" }

type deleteWorker struct {
	river.WorkerDefaults[DeleteArgs]
	svc *Service
}

func (w *deleteWorker) Timeout(*river.Job[DeleteArgs]) time.Duration { return jobTimeout }

func (w *deleteWorker) Work(ctx context.Context, job *river.Job[DeleteArgs]) error {
	return w.svc.DeleteRemote(ctx, job.Args.ServerID)
}

type ingressWorker struct {
	river.WorkerDefaults[IngressArgs]
	svc *Service
}

func (w *ingressWorker) Timeout(*river.Job[IngressArgs]) time.Duration { return jobTimeout }

func (w *ingressWorker) Work(ctx context.Context, job *river.Job[IngressArgs]) error {
	err := w.svc.SyncIngress(ctx, job.Args.ServerID, false)
	if err != nil && permanent(err) {
		return nil // recorded on the tunnel row; the sweep retries
	}
	return err
}

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	svc *Service
}

func (w *sweepWorker) Timeout(*river.Job[SweepArgs]) time.Duration { return 4 * jobTimeout }

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return w.svc.Sweep(ctx)
}

// RegisterJobs registers the delete, ingress and sweep workers and the
// periodic sweep.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &deleteWorker{svc: svc})
		river.AddWorker(w, &ingressWorker{svc: svc})
		river.AddWorker(w, &sweepWorker{svc: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(SweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "server_tunnel_sweep"},
	))
}

// Sweep re-pushes ingress rules whose hostnames changed and re-sends the
// connector command to online servers whose connector is missing or unobserved.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.q.ListActiveServerTunnels(ctx)
	if err != nil {
		return fmt.Errorf("list tunnels: %w", err)
	}
	for _, r := range rows {
		if err := s.SyncIngress(ctx, r.ServerID, false); err != nil {
			s.log.WarnContext(ctx, "tunnel sweep: ingress sync", "server_id", r.ServerID, "err", errText(err))
		}
		if err := s.resyncConnector(ctx, r); err != nil {
			s.log.WarnContext(ctx, "tunnel sweep: connector sync", "server_id", r.ServerID, "err", err)
		}
	}
	return nil
}

// resyncConnector queues a sync_tunnel command when the server is online and its
// connector is reported offline (the container is gone or stopped) or was not
// observed for a while. A connector that needs attention is not retried: that
// state needs a person.
func (s *Service) resyncConnector(ctx context.Context, r db.ServerTunnel) error {
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: r.ServerID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.Status != "online" || !SupportsTunnel(srv) {
		return nil
	}
	now := s.now()
	stale := r.ObservedAt == nil || now.Sub(*r.ObservedAt) > staleAfter
	if r.State != "offline" && !(stale && r.State != "needs_attention") {
		return nil
	}
	if r.ConnectorSyncedAt != nil && now.Sub(*r.ConnectorSyncedAt) < resyncAfter {
		return nil
	}
	if open, err := s.q.HasOpenTunnelCommand(ctx, r.ServerID); err != nil || open {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.enqueueSync(ctx, tx, r.ServerID, r.TunnelID, r.TunnelName)
	})
}
