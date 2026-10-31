package registryadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Garbage-collection window of the registry: instance-wide, weekly, in the quiet
// hours. Blobs no manifest references are removed; tags and digests the control
// plane still needs are kept (never --delete-untagged: the retention sweep
// decides which manifests go).
const (
	// GCEvery is how often the window runs.
	GCEvery = 7 * 24 * time.Hour
	// gcFromHour and gcToHour (UTC, [from, to)) bound when a due window may start.
	gcFromHour = 2
	gcToHour   = 5
	// drainGrace lets the forwarders' cached read-only flag expire before the
	// in-flight write count is trusted.
	drainGrace = 6 * time.Second
	// drainDeadline is how long the window waits for pushes in flight to finish
	// before it gives up for tonight (the flag only made new pushes wait).
	drainDeadline = 2 * time.Hour
	// collectDeadline caps the collect phase; the agent's own exec timeout is shorter.
	collectDeadline = 4 * time.Hour
	gcPoll          = 10 * time.Second
	drainPoll       = 15 * time.Second
)

// Phases of the window (registries.ManagedState.Phase).
const (
	phaseDrain   = "drain"
	phaseCollect = "collect"
)

// MaintenanceArgs is the hourly check that starts a due window.
type MaintenanceArgs struct{}

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return "instsvc_registry_maintenance" }

// GCArgs drives the window; all state lives in instance_services.options, so
// any worker can continue it.
type GCArgs struct{}

// Kind implements river.JobArgs.
func (GCArgs) Kind() string { return "instsvc_registry_gc" }

// InsertOpts implements river.JobArgsWithInsertOpts: one live window.
func (GCArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 25,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
}

type maintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	b *Backend
}

func (w *maintenanceWorker) Work(ctx context.Context, _ *river.Job[MaintenanceArgs]) error {
	return w.b.scheduleGC(ctx)
}

type gcWorker struct {
	river.WorkerDefaults[GCArgs]
	b *Backend
}

func (w *gcWorker) Work(ctx context.Context, _ *river.Job[GCArgs]) error {
	wait, done, err := w.b.AdvanceGC(ctx)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	return river.JobSnooze(wait)
}

// RegisterJobs registers the window and its hourly check.
func RegisterJobs(jm *jobs.Manager, b *Backend) {
	b.jobs = jm
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &maintenanceWorker{b: b})
		river.AddWorker(w, &gcWorker{b: b})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return MaintenanceArgs{}, &river.InsertOpts{MaxAttempts: 1} },
		&river.PeriodicJobOpts{ID: "instsvc_registry_maintenance"},
	))
}

// gcDue reports whether a window should start now.
func gcDue(now time.Time, st registries.ManagedState) bool {
	if st.Phase != "" {
		return true // resume
	}
	if st.LastGCAt != nil && now.Sub(*st.LastGCAt) < GCEvery {
		return false
	}
	h := now.UTC().Hour()
	return h >= gcFromHour && h < gcToHour
}

func (b *Backend) scheduleGC(ctx context.Context) error {
	row, err := b.q.GetInstanceService(ctx, instanceKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	st := registries.DecodeManagedState(row.Options)
	if row.Status != "ready" && st.Phase == "" {
		return nil
	}
	if !gcDue(b.now(), st) {
		return nil
	}
	return b.StartGC(ctx)
}

// StartGC enqueues a window now, whatever the schedule says.
func (b *Backend) StartGC(ctx context.Context) error {
	if b.jobs == nil {
		return errors.New("jobs are not registered")
	}
	return b.jobs.Insert(ctx, GCArgs{}, nil)
}

func (b *Backend) patch(ctx context.Context, p map[string]any) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := b.q.MergeInstanceServiceOptions(ctx, db.MergeInstanceServiceOptionsParams{Kind: instanceKind, Patch: raw}); err != nil {
		return fmt.Errorf("record registry state: %w", err)
	}
	b.invalidateState()
	return nil
}

// AdvanceGC moves the window one step: it reports how long to wait before the
// next call, or done when the window ended.
//
//	"" → drain: the read-only flag is set at once (the forwarder refuses writes
//	  with 503 and Retry-After, the deployment flow queues builds) and pushes in
//	  flight finish;
//	drain → collect: `registry garbage-collect` runs in the registry container
//	  through the control-plane host's agent;
//	collect → "": writes are allowed again.
//
// A failure ends the window at once with writes allowed again, except while the
// collector may still run: then the registry stays read-only until the command
// is finished.
func (b *Backend) AdvanceGC(ctx context.Context) (time.Duration, bool, error) {
	row, err := b.q.GetInstanceService(ctx, instanceKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	st := registries.DecodeManagedState(row.Options)
	now := b.now()
	if st.Phase == "" && row.Status != "ready" {
		return 0, true, nil
	}
	restore := func(msg string, ok bool) (time.Duration, bool, error) {
		p := map[string]any{
			"phase": nil, "readOnly": nil, "reason": nil, "since": nil, "commandId": nil,
			"lastGcMessage": msg, "error": nil,
		}
		if ok {
			p["lastGcAt"] = now.UTC()
		} else {
			p["error"] = msg
		}
		if err := b.patch(ctx, p); err != nil {
			return 0, false, err
		}
		if ok {
			b.log.Info("registry garbage collection finished", "message", msg)
		} else {
			b.log.Warn("registry garbage collection did not run", "reason", msg)
		}
		return 0, true, nil
	}

	switch st.Phase {
	case "":
		if err := b.patch(ctx, map[string]any{
			"phase": phaseDrain, "readOnly": true, "reason": "garbage collection", "since": now.UTC(), "error": nil, "commandId": nil,
		}); err != nil {
			return 0, false, err
		}
		return drainGrace, false, nil

	case phaseDrain:
		since := now
		if st.Since != nil {
			since = *st.Since
		}
		b.mu.Lock()
		gauge := b.writes
		b.mu.Unlock()
		if now.Sub(since) < drainGrace {
			return drainGrace - now.Sub(since), false, nil
		}
		if gauge != nil && gauge() > 0 {
			if now.Sub(since) > drainDeadline {
				return restore("Pushes were still running after two hours; the collection is skipped tonight.", false)
			}
			return drainPoll, false, nil
		}
		id, err := b.enqueueCollect(ctx)
		if err != nil {
			return restore(err.Error(), false)
		}
		if err := b.patch(ctx, map[string]any{"phase": phaseCollect, "commandId": id.String()}); err != nil {
			return 0, false, err
		}
		return gcPoll, false, nil

	case phaseCollect:
		id, err := uuid.Parse(st.CommandID)
		if err != nil {
			return restore("The collection command was lost.", false)
		}
		cmd, err := b.q.GetAgentCommand(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return restore("The collection command was lost.", false)
		}
		if err != nil {
			return 0, false, err
		}
		switch cmd.Status {
		case "succeeded":
			return restore("Unused image data was collected.", true)
		case "failed", "expired", "canceled":
			msg := cmd.Error
			if msg == "" {
				msg = "The collection did not run (" + cmd.Status + ")."
			}
			return restore(msg, false)
		}
		if st.Since != nil && now.Sub(*st.Since) > collectDeadline {
			return restore("The collection did not finish in time; check the registry container.", false)
		}
		return gcPoll, false, nil
	}
	return restore("Unknown garbage-collection phase "+st.Phase+".", false)
}

// enqueueCollect sends the registry_gc instance operation to the agent on the
// control-plane host, the only host with the registry container.
func (b *Backend) enqueueCollect(ctx context.Context) (uuid.UUID, error) {
	if b.cfg.Dispatcher == nil {
		return uuid.Nil, errors.New("the control plane cannot reach its host's agent")
	}
	srv, err := b.q.GetControlPlaneServer(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, errors.New("the control plane's host has no agent")
	}
	if err != nil {
		return uuid.Nil, err
	}
	if srv.Status != "online" {
		return uuid.Nil, errors.New("the agent on the control plane's host is offline")
	}
	payload, err := json.Marshal(agentprotocol.InstanceOpPayload{Op: agentprotocol.InstanceOpRegistryGC})
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = db.InTx(ctx, b.cfg.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = b.cfg.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: srv.ID, Kind: agentprotocol.KindInstanceOp, Payload: payload,
			IdempotencyKey: "registry_gc:" + uuid.NewString(),
		})
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueue registry_gc: %w", err)
	}
	return id, nil
}
