package backups

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// instanceRunTimeout bounds one dump and upload.
	instanceRunTimeout = 5 * time.Hour
	// instanceStaleAfter marks a run that never finished as failed; it is longer
	// than the job timeout, so a live run is never failed under its own feet.
	instanceStaleAfter = instanceRunTimeout + time.Hour
)

// InstanceBackupTickArgs evaluates the control-plane backup schedule.
type InstanceBackupTickArgs struct{}

// Kind implements river.JobArgs.
func (InstanceBackupTickArgs) Kind() string { return "instance_backup_tick" }

// InstanceBackupRunArgs runs one control-plane backup.
type InstanceBackupRunArgs struct {
	BackupID uuid.UUID `json:"backupId"`
}

// Kind implements river.JobArgs.
func (InstanceBackupRunArgs) Kind() string { return "instance_backup_run" }

type instanceTickWorker struct {
	river.WorkerDefaults[InstanceBackupTickArgs]
	s *InstanceService
}

func (w *instanceTickWorker) Work(ctx context.Context, _ *river.Job[InstanceBackupTickArgs]) error {
	return w.s.Tick(ctx)
}

type instanceRunWorker struct {
	river.WorkerDefaults[InstanceBackupRunArgs]
	s *InstanceService
}

func (w *instanceRunWorker) Timeout(*river.Job[InstanceBackupRunArgs]) time.Duration {
	return instanceRunTimeout
}

func (w *instanceRunWorker) Work(ctx context.Context, job *river.Job[InstanceBackupRunArgs]) error {
	w.s.Run(ctx, job.Args.BackupID)
	return nil // a failed backup is recorded on its row; retrying the job would only repeat it
}

// RegisterInstanceJobs adds the control-plane backup workers and the one-minute
// schedule tick.
func RegisterInstanceJobs(jm *jobs.Manager, svc *InstanceService) {
	jm.AddWorker(func(w *river.Workers) {
		river.AddWorker(w, &instanceTickWorker{s: svc})
		river.AddWorker(w, &instanceRunWorker{s: svc})
	})
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(TickInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return InstanceBackupTickArgs{}, &river.InsertOpts{MaxAttempts: 1}
		},
		&river.PeriodicJobOpts{ID: "instance_backup_tick"},
	))
}

// startRun records a running backup of destination c and queues its job in tx.
func (s *InstanceService) startRun(ctx context.Context, tx pgx.Tx, c db.InstanceBackupSetting, trigger, actor string) (db.InstanceBackup, error) {
	row, err := db.New(tx).InsertInstanceBackup(ctx, db.InsertInstanceBackupParams{
		Trigger: trigger, DestKind: c.DestKind, DestLabel: s.instanceLabel(c), DestFingerprint: instanceFingerprint(c), ActorLabel: actor,
	})
	if err != nil {
		return row, err
	}
	if s.jm == nil {
		return row, errors.New("no job manager")
	}
	if err := s.jm.InsertTx(ctx, tx, InstanceBackupRunArgs{BackupID: row.ID}, nil); err != nil {
		return row, fmt.Errorf("queue instance backup: %w", err)
	}
	return row, nil
}

// Tick starts the scheduled backup when it is due and fails runs that never
// finished. A missed slot (the control plane was down at 03:30) runs once, as
// soon as the control plane is back, and the next slot is computed from now.
func (s *InstanceService) Tick(ctx context.Context) error {
	now := s.now()
	stale, err := s.q.FailStaleInstanceBackups(ctx, now.Add(-instanceStaleAfter))
	if err != nil {
		return fmt.Errorf("fail stale instance backups: %w", err)
	}
	for _, b := range stale {
		s.afterRun(ctx, b)
	}
	if err := s.q.PruneFailedInstanceBackups(ctx); err != nil {
		s.log.WarnContext(ctx, "prune failed instance backups", "err", err)
	}

	var started *db.InstanceBackup
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetInstanceBackupSettings(ctx)
		if err != nil {
			return err
		}
		if !cur.Enabled || cur.NextRunAt == nil || cur.NextRunAt.After(now) {
			return nil
		}
		next, err := nextRun(cur.Schedule, "UTC", now)
		if err != nil {
			// The stored schedule is unusable: stop retrying every minute.
			s.log.ErrorContext(ctx, "instance backup schedule is invalid", "schedule", cur.Schedule, "err", err)
			next = now.Add(24 * time.Hour)
		}
		claimed, err := q.ClaimDueInstanceBackup(ctx, db.ClaimDueInstanceBackupParams{NewNextRunAt: &next, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // another tick won
		}
		if err != nil {
			return err
		}
		row, err := s.startRun(ctx, tx, claimed, InstanceTriggerSchedule, "schedule")
		if err != nil {
			return err
		}
		started = &row
		return nil
	})
	if services.IsUniqueViolation(err) {
		// A run is still going (a dump longer than the interval). The slot is
		// skipped and the next one is computed from now.
		s.log.WarnContext(ctx, "scheduled instance backup skipped: a run is still in progress")
		return nil
	}
	if err != nil {
		return fmt.Errorf("start scheduled instance backup: %w", err)
	}
	if started != nil {
		s.publish()
	}
	return nil
}

// countingWriter counts and hashes what flows through it.
type countingWriter struct {
	n    int64
	hash interface{ Write([]byte) (int, error) }
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.hash.Write(p)
}

// InstanceManifest is the sidecar written next to every control-plane dump. A
// bucket can be understood without the control plane: it names the dump format,
// its checksum and the fingerprint of the secret key the instance used.
type InstanceManifest struct {
	agentprotocol.BackupManifest
	Kind string `json:"kind"`
	// KeyFingerprint identifies the NEBULACTRL_SECRET_KEY of the instance that
	// wrote the backup. The key itself is never stored.
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
	// SchemaVersion is the newest applied goose migration of the dumped database.
	SchemaVersion int64 `json:"schemaVersion,omitempty"`
}

// Run performs the backup of a running row: pg_dump streams into the
// destination, a manifest follows, then retention prunes. The outcome is
// recorded on the row; nothing is returned because a failed backup is data, not
// an error of the job.
func (s *InstanceService) Run(ctx context.Context, id uuid.UUID) {
	row, err := s.q.GetInstanceBackup(ctx, id)
	if err != nil || row.Status != "running" {
		return // gone, or already finished (a re-delivered job)
	}
	c, err := s.q.GetInstanceBackupSettings(ctx)
	if err != nil {
		s.finishFailed(ctx, row, fmt.Errorf("load settings: %w", err), nil)
		return
	}
	secrets := s.secretValues(ctx, c)
	res, err := s.dumpToDestination(ctx, row, c)
	if err != nil {
		s.finishFailed(ctx, row, err, secrets)
		return
	}
	var done db.InstanceBackup
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		done, err = db.New(tx).FinishInstanceBackup(ctx, db.FinishInstanceBackupParams{
			ID: id, Status: "succeeded", ObjectKey: res.key, SizeBytes: res.size, Sha256: res.sha, PgVersion: res.pgVersion,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionInstanceBackupDone, ResourceType: "instance_backup", ResourceID: id.String(),
			Metadata: map[string]any{"trigger": row.Trigger, "destination": row.DestLabel, "sizeBytes": res.size, "objectKey": res.key},
		})
	})
	if err != nil {
		// The objects exist but the row could not say so; surface it loudly.
		s.log.ErrorContext(ctx, "record instance backup success", "backup_id", id, "err", err)
		return
	}
	s.afterRun(ctx, done)
	s.prune(ctx, c)
}

type dumpResult struct {
	key       string
	size      int64
	sha       string
	pgVersion string
}

func (s *InstanceService) dumpToDestination(ctx context.Context, row db.InstanceBackup, c db.InstanceBackupSetting) (dumpResult, error) {
	var res dumpResult
	if instanceFingerprint(c) != row.DestFingerprint {
		return res, errors.New("the destination changed after this backup was queued; run it again")
	}
	if err := s.tools.Available(); err != nil {
		return res, err
	}
	conn, err := ParsePGURL(s.dbURL)
	if err != nil {
		return res, err
	}
	st, err := s.store(ctx, c)
	if err != nil {
		return res, err
	}
	// Facts for the manifest; failures here must not fail the backup.
	_ = s.pool.QueryRow(ctx, `SHOW server_version`).Scan(&res.pgVersion)
	var schema int64
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&schema)

	res.key = instanceObjectKey(c.Prefix, row.StartedAt, row.ID)
	hash := sha256.New()
	cw := &countingWriter{hash: hash}
	pr, pw := io.Pipe()
	dumpErr := make(chan error, 1)
	go func() {
		err := s.tools.DumpTo(ctx, conn, io.MultiWriter(cw, pw))
		_ = pw.CloseWithError(err) // nil closes with EOF; an error aborts the upload
		dumpErr <- err
	}()
	putErr := st.Put(ctx, res.key, pr)
	if putErr != nil {
		_ = pr.CloseWithError(putErr) // unblock a dump still writing
	}
	derr := <-dumpErr
	if derr != nil {
		return res, derr
	}
	if putErr != nil {
		return res, fmt.Errorf("upload: %w", putErr)
	}
	res.size, res.sha = cw.n, hex.EncodeToString(hash.Sum(nil))
	if res.size == 0 {
		_ = st.Delete(context.WithoutCancel(ctx), res.key)
		return res, errors.New("pg_dump produced no output")
	}
	if stored, err := st.Size(ctx, res.key); err != nil {
		return res, fmt.Errorf("verify upload: %w", err)
	} else if stored != res.size {
		_ = st.Delete(context.WithoutCancel(ctx), res.key)
		return res, fmt.Errorf("verify upload: the destination holds %d bytes, the dump has %d", stored, res.size)
	}

	man := InstanceManifest{
		BackupManifest: agentprotocol.BackupManifest{
			Version: 1, BackupID: row.ID.String(), Engine: "postgres", EngineVersion: res.pgVersion, Format: agentprotocol.FormatPGCustom,
			ObjectKey: res.key, SizeBytes: res.size, SHA256: res.sha, CreatedAt: row.StartedAt.UTC(),
			Meta: agentprotocol.ManifestMeta{Service: InstanceManifestKind, Trigger: row.Trigger},
		},
		Kind: InstanceManifestKind, KeyFingerprint: s.keyFP, SchemaVersion: schema,
	}
	body, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return res, err
	}
	// The manifest is the commit marker: restores only list backups that have one.
	if err := st.Put(ctx, backup.ManifestKey(res.key), bytes.NewReader(body)); err != nil {
		_ = st.Delete(context.WithoutCancel(ctx), res.key)
		return res, fmt.Errorf("write manifest: %w", err)
	}
	return res, nil
}

func (s *InstanceService) finishFailed(ctx context.Context, row db.InstanceBackup, cause error, secrets []string) {
	msg := scrubSecrets(cause.Error(), secrets)
	if errors.Is(cause, context.DeadlineExceeded) {
		msg = "the backup ran out of time (5 hours)"
	}
	var done db.InstanceBackup
	err := db.InTx(context.WithoutCancel(ctx), s.pool, func(tx pgx.Tx) error {
		var err error
		done, err = db.New(tx).FinishInstanceBackup(ctx, db.FinishInstanceBackupParams{ID: row.ID, Status: "failed", Error: msg})
		if err != nil {
			return err
		}
		return audit.Record(context.WithoutCancel(ctx), tx, audit.Entry{
			Action: ActionInstanceBackupFailed, ResourceType: "instance_backup", ResourceID: row.ID.String(),
			Metadata: map[string]any{"trigger": row.Trigger, "destination": row.DestLabel, "error": msg},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return // already finished
	}
	if err != nil {
		s.log.ErrorContext(ctx, "record instance backup failure", "backup_id", row.ID, "err", err)
		return
	}
	s.log.ErrorContext(ctx, "instance backup failed", "backup_id", row.ID, "destination", row.DestLabel, "err", msg)
	s.afterRun(ctx, done)
}

// afterRun tells the UI and the notification channels how a run ended.
func (s *InstanceService) afterRun(ctx context.Context, b db.InstanceBackup) {
	s.publish()
	p := s.notifier.Load()
	if p == nil || *p == nil {
		return
	}
	ws, err := s.q.FirstWorkspace(ctx)
	if err != nil {
		return
	}
	ev := Event{
		WorkspaceID: ws.ID, ServiceName: "the NebulaCtrl control plane", Instance: true, BackupID: b.ID, Trigger: b.Trigger,
		Destination: b.DestLabel, Engine: "postgres", SizeBytes: b.SizeBytes, Message: b.Error, At: s.now(),
	}
	if b.Status == "succeeded" {
		ev.Type = NotifyBackupSucceeded
	} else {
		ev.Type = NotifyBackupFailed
	}
	(*p).Notify(ctx, ev)
}

// prune removes backups of the current destination beyond the retention count.
// The newest backup is never removed. A failure leaves the object and its row in
// place; the next run tries again.
func (s *InstanceService) prune(ctx context.Context, c db.InstanceBackupSetting) {
	rows, err := s.q.ListInstanceBackupCandidates(ctx, instanceFingerprint(c))
	if err != nil {
		s.log.WarnContext(ctx, "instance backup retention", "err", err)
		return
	}
	cands := make([]Candidate, len(rows))
	for i, r := range rows {
		cands[i] = Candidate{ID: r.ID, CreatedAt: r.StartedAt}
	}
	victims := Prune(cands, int(c.RetentionCount), 0, s.now())
	if len(victims) == 0 {
		return
	}
	st, err := s.store(ctx, c)
	if err != nil {
		s.log.WarnContext(ctx, "instance backup retention", "err", err)
		return
	}
	for _, r := range rows {
		if !slices.Contains(victims, r.ID) {
			continue
		}
		if err := st.Delete(ctx, r.ObjectKey); err != nil {
			s.log.WarnContext(ctx, "delete expired instance backup", "backup_id", r.ID, "err", err)
			continue
		}
		if err := st.Delete(ctx, backup.ManifestKey(r.ObjectKey)); err != nil {
			s.log.WarnContext(ctx, "delete expired instance backup manifest", "backup_id", r.ID, "err", err)
			continue
		}
		if err := s.q.MarkInstanceBackupObjectDeleted(ctx, r.ID); err != nil {
			s.log.WarnContext(ctx, "mark instance backup deleted", "backup_id", r.ID, "err", err)
		}
	}
	s.publish()
}
