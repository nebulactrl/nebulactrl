package mesh

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
)

const (
	// aclDebounce is how long a change waits so a burst of changes (a deploy that
	// adds a link, a grant and an exposure) produces one policy write.
	aclDebounce = 5 * time.Second
	// syncDebounce coalesces the nudges of one transaction's worth of changes.
	syncDebounce = 2 * time.Second
	// ReconcileInterval is how often desired and observed mesh state are compared.
	ReconcileInterval = time.Minute
	// aclRecheck is how often an applied or advisory policy is verified again.
	aclRecheck = 5 * time.Minute

	jobTimeout = 90 * time.Second
)

// River requires every unique-by-state set to contain the running state, so a
// change that arrives while a job runs is dropped as a duplicate; the periodic
// reconcile (which re-queues the ACL job of a managed policy every minute and
// re-sends every server's set) repairs that window.
var uniqueStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
}

// ACLApplyArgs regenerates and applies the ACL of one workspace (MeshACLApplyJob).
type ACLApplyArgs struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
}

// Kind implements river.JobArgs.
func (ACLApplyArgs) Kind() string { return "mesh_acl_apply" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ACLApplyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// SyncArgs sends the desired mesh set to one server.
type SyncArgs struct {
	ServerID uuid.UUID `json:"serverId"`
	// Force sends even when the applied state looks converged (removals).
	Force bool `json:"force,omitempty"`
}

// Kind implements river.JobArgs.
func (SyncArgs) Kind() string { return "mesh_sync" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (SyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: uniqueStates}}
}

// ReconcileArgs compares desired and observed state of every team's mesh.
type ReconcileArgs struct{}

// Kind implements river.JobArgs.
func (ReconcileArgs) Kind() string { return "mesh_reconcile" }

// KeySweepArgs revokes a node key that was never used (scheduled when the key is
// minted, to run after it expired).
type KeySweepArgs struct {
	NodeID   uuid.UUID `json:"nodeId"`
	NodeKind string    `json:"nodeKind"`
	KeyID    string    `json:"keyId"`
}

// Kind implements river.JobArgs.
func (KeySweepArgs) Kind() string { return "mesh_key_sweep" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (KeySweepArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 5} }

type aclWorker struct {
	river.WorkerDefaults[ACLApplyArgs]
	svc *Service
}

func (w *aclWorker) Timeout(*river.Job[ACLApplyArgs]) time.Duration { return jobTimeout }
func (w *aclWorker) Work(ctx context.Context, job *river.Job[ACLApplyArgs]) error {
	return w.svc.ApplyACL(ctx, job.Args.WorkspaceID)
}

type syncWorker struct {
	river.WorkerDefaults[SyncArgs]
	svc *Service
}

func (w *syncWorker) Timeout(*river.Job[SyncArgs]) time.Duration { return jobTimeout }
func (w *syncWorker) Work(ctx context.Context, job *river.Job[SyncArgs]) error {
	return w.svc.SyncServer(ctx, job.Args.ServerID, job.Args.Force)
}

type reconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]
	svc *Service
}

func (w *reconcileWorker) Timeout(*river.Job[ReconcileArgs]) time.Duration { return 4 * jobTimeout }
func (w *reconcileWorker) Work(ctx context.Context, _ *river.Job[ReconcileArgs]) error {
	return w.svc.Reconcile(ctx)
}

type sweepWorker struct {
	river.WorkerDefaults[KeySweepArgs]
	svc *Service
}

func (w *sweepWorker) Timeout(*river.Job[KeySweepArgs]) time.Duration { return jobTimeout }
func (w *sweepWorker) Work(ctx context.Context, job *river.Job[KeySweepArgs]) error {
	return w.svc.SweepKey(ctx, job.Args)
}

// RegisterJobs registers the mesh workers and the periodic reconcile.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &aclWorker{svc: svc})
		river.AddWorker(w, &syncWorker{svc: svc})
		river.AddWorker(w, &reconcileWorker{svc: svc})
		river.AddWorker(w, &sweepWorker{svc: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(ReconcileInterval),
		func() (river.JobArgs, *river.InsertOpts) { return ReconcileArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "mesh_reconcile"},
	))
}

// insertJob queues a job inside tx (nil tx: on its own). A failure is logged: a
// missed nudge is repaired by the periodic reconcile.
func (s *Service) insertJob(ctx context.Context, tx pgx.Tx, args river.JobArgs, delay time.Duration) {
	if s.jobs == nil {
		return
	}
	opts := &river.InsertOpts{ScheduledAt: s.now().Add(delay)}
	var err error
	if tx != nil {
		err = s.jobs.InsertTx(ctx, tx, args, opts)
	} else {
		err = s.jobs.Insert(ctx, args, opts)
	}
	if err != nil {
		s.log.WarnContext(ctx, "queue mesh job", "kind", args.Kind(), "err", err)
	}
}

// nudgeACL asks for the ACL of ws to be regenerated and applied (debounced).
func (s *Service) nudgeACL(ctx context.Context, tx pgx.Tx, ws uuid.UUID) {
	s.insertJob(ctx, tx, ACLApplyArgs{WorkspaceID: ws}, aclDebounce)
}

// nudgeSync asks for a server's mesh set to be sent (debounced).
func (s *Service) nudgeSync(ctx context.Context, tx pgx.Tx, serverID uuid.UUID, force bool) {
	s.insertJob(ctx, tx, SyncArgs{ServerID: serverID, Force: force}, syncDebounce)
}

// Reconcile is the periodic job: coordinator health, auto-join, convergence of
// every server's node set and the ACL of every team that uses the mesh.
func (s *Service) Reconcile(ctx context.Context) error {
	wss, err := s.q.ListMeshWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		if err := s.reconcileWorkspace(ctx, ws); err != nil {
			s.log.WarnContext(ctx, "reconcile mesh", "workspace_id", ws.String(), "err", err)
		}
	}
	return nil
}

func (s *Service) reconcileWorkspace(ctx context.Context, ws uuid.UUID) error {
	sn, err := s.load(ctx, s.q, ws)
	if err != nil {
		return err
	}
	if sn.haveInteg {
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		herr := s.ts.CoordinatorHealth(hctx, ws)
		cancel()
		ok := herr == nil
		if err := s.q.SetMeshCoordinator(ctx, db.SetMeshCoordinatorParams{WorkspaceID: ws, CoordinatorOk: &ok}); err != nil {
			return err
		}
		if sn.set.CoordinatorOk == nil || *sn.set.CoordinatorOk != ok {
			s.publish(ws)
		}
		sn.set.CoordinatorOk = &ok
	}
	if sn.set.Paused {
		return nil
	}
	if sn.set.AutoJoin && sn.haveInteg && (sn.set.CoordinatorOk == nil || *sn.set.CoordinatorOk) {
		for id, si := range sn.servers {
			if _, on := sn.nodes[id]; on || si.Status != "online" || !capable(si.Facts) || si.Facts.OS == "" {
				continue
			}
			if sn.integ.AllowedTags != nil && !contains(sn.integ.AllowedTags, sn.meshTag()) {
				continue
			}
			if err := s.enableServer(ctx, sn, db.Server{ID: id, WorkspaceID: ws, Name: si.Name}); err != nil {
				s.log.WarnContext(ctx, "mesh auto-join", "server_id", id.String(), "err", err)
			}
			sn.set.Enabled = true
		}
	}
	servers := map[uuid.UUID]bool{}
	for id := range sn.nodes {
		servers[id] = true
	}
	for _, l := range sn.links {
		servers[l.ServerID] = true
	}
	for id := range servers {
		if err := s.SyncServer(ctx, id, false); err != nil {
			s.log.WarnContext(ctx, "sync mesh", "server_id", id.String(), "err", err)
		}
	}
	stale := s.now().Sub(sn.set.UpdatedAt) >= aclRecheck
	managedHeadscale := sn.set.AclMode == ModeManaged && sn.isHeadscale() // a local hash compare: cheap, and it repairs a change dropped while a run was in flight
	if sn.set.AclState != StateApplied && sn.set.AclState != StateAdvisory || stale || managedHeadscale {
		if len(sn.nodes) > 0 || len(sn.links) > 0 || sn.set.AclState == StatePending {
			s.nudgeACL(ctx, nil, ws)
		}
	}
	return nil
}
