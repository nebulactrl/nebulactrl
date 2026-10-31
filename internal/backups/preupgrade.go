package backups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// The pre-upgrade backup is an instance backup (same destination, same dump and
// manifest, trigger "pre_upgrade") that the control plane takes BEFORE it
// migrates its database, when it finds that its version changed, and that the
// installer asks for before it replaces the running control plane.
//
// It runs before the migrations, so it must not depend on the schema the new
// version expects: it reads the destination with one fixed query (columns that
// have existed since the instance backup shipped) instead of sqlc's SELECT *,
// and it records its row only after the migrations ran (RecordPreUpgrade).

// Pre-upgrade statuses (instance_upgrade.pre_upgrade_status).
const (
	PreUpgradeSucceeded = "succeeded"
	// PreUpgradeFailed: a destination is configured and the backup did not work.
	// The upgrade is refused unless forced.
	PreUpgradeFailed = "failed"
	// PreUpgradeSkipped: nothing to back up to (no destination configured yet),
	// or no database to back up. The upgrade proceeds, loudly.
	PreUpgradeSkipped = "skipped"
	// PreUpgradeForced: the backup failed and the operator forced the upgrade.
	PreUpgradeForced = "forced"

	// InstanceTriggerPreUpgrade is the trigger of the run row and the manifest.
	InstanceTriggerPreUpgrade = "pre_upgrade"
)

// PreUpgradeTimeout bounds the dump and upload of a pre-upgrade backup.
const PreUpgradeTimeout = 30 * time.Minute

// PreUpgradeResult is the outcome of a pre-upgrade backup.
type PreUpgradeResult struct {
	Status  string
	Message string
	From    string
	To      string

	// Set when a dump was written (Status succeeded, or forced after a later failure).
	RunID       uuid.UUID
	Destination string
	DestKind    string
	DestFP      string
	ObjectKey   string
	SizeBytes   int64
	SHA256      string
	PGVersion   string
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Blocks reports whether the upgrade must not go on: the backup failed and the
// operator did not force.
func (r PreUpgradeResult) Blocks(force bool) bool {
	return r.Status == PreUpgradeFailed && !force
}

// Forced returns r with a failed status turned into PreUpgradeForced.
func (r PreUpgradeResult) Forced() PreUpgradeResult {
	if r.Status == PreUpgradeFailed {
		r.Status = PreUpgradeForced
		r.Message = "Forced past a failed backup: " + r.Message
	}
	return r
}

// PreUpgradeDeps are what the pre-upgrade backup needs.
type PreUpgradeDeps struct {
	Pool           *pgxpool.Pool
	DatabaseURL    string
	BackupDir      string
	Box            *secretbox.Box
	KeyFingerprint string
	Tools          PGTools // zero value: pg_dump from PATH
	// Builtin and BuiltinS3URL reach the built-in S3 when the destination is the
	// "builtin" kind (see InstanceDeps). Without them such a destination fails the backup.
	Builtin      BuiltinStorage
	BuiltinS3URL string
}

// RunPreUpgrade takes the backup. It never returns an error: every outcome,
// including a failure, is a PreUpgradeResult the caller gates on.
func RunPreUpgrade(ctx context.Context, d PreUpgradeDeps, from, to string) PreUpgradeResult {
	res := PreUpgradeResult{From: from, To: to, Status: PreUpgradeFailed}
	svc := NewInstanceService(InstanceDeps{
		Pool: d.Pool, Box: d.Box, DatabaseURL: d.DatabaseURL, BackupDir: d.BackupDir, KeyFingerprint: d.KeyFingerprint, Tools: d.Tools,
		Builtin: d.Builtin, BuiltinS3URL: d.BuiltinS3URL,
	})
	c, err := readInstanceDestination(ctx, d.Pool)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		res.Status, res.Message = PreUpgradeSkipped, "The database has no control-plane backup settings yet, so there is nothing to back up to."
		return res
	case err != nil:
		res.Message = "read the backup destination: " + err.Error()
		return res
	}
	if c.DestKind == DestBuiltin && !svc.instanceConfigured(c) {
		res.Message = "The control-plane backup goes to the built-in storage, which is not reachable from here."
		return res
	}
	if !svc.instanceConfigured(c) {
		res.Status = PreUpgradeSkipped
		res.Message = "No control-plane backup destination is configured (Admin → Control-plane backup), so no backup was taken before the upgrade."
		return res
	}
	if err := svc.tools.Available(); err != nil {
		res.Message = err.Error()
		return res
	}
	row := db.InstanceBackup{
		ID: uuid.New(), Trigger: InstanceTriggerPreUpgrade, DestKind: c.DestKind, DestLabel: svc.instanceLabel(c),
		DestFingerprint: instanceFingerprint(c), StartedAt: time.Now().UTC(),
	}
	rctx, cancel := context.WithTimeout(ctx, PreUpgradeTimeout)
	defer cancel()
	dump, err := svc.dumpToDestination(rctx, row, c)
	if err != nil {
		res.Message = scrubSecrets(err.Error(), svc.secretValues(rctx, c))
		if errors.Is(err, context.DeadlineExceeded) {
			res.Message = fmt.Sprintf("the backup ran out of time (%s)", PreUpgradeTimeout)
		}
		return res
	}
	res.Status = PreUpgradeSucceeded
	res.Message = fmt.Sprintf("Backed up to %s (%d bytes) before upgrading.", row.DestLabel, dump.size)
	res.RunID, res.Destination, res.DestKind, res.DestFP = row.ID, row.DestLabel, row.DestKind, row.DestFingerprint
	res.ObjectKey, res.SizeBytes, res.SHA256, res.PGVersion = dump.key, dump.size, dump.sha, dump.pgVersion
	res.StartedAt, res.FinishedAt = row.StartedAt, time.Now().UTC()
	return res
}

// readInstanceDestination reads the destination of the control-plane backup with
// a query that does not depend on columns later migrations add.
func readInstanceDestination(ctx context.Context, pool *pgxpool.Pool) (db.InstanceBackupSetting, error) {
	var c db.InstanceBackupSetting
	err := pool.QueryRow(ctx, `SELECT dest_kind, endpoint, region, bucket, prefix, path_style, compat_mode, access_key_enc, secret_key_enc, local_dir
FROM instance_backup_settings WHERE id`).Scan(
		&c.DestKind, &c.Endpoint, &c.Region, &c.Bucket, &c.Prefix, &c.PathStyle, &c.CompatMode, &c.AccessKeyEnc, &c.SecretKeyEnc, &c.LocalDir)
	return c, err
}

// RecordPreUpgrade stores the outcome after the migrations ran: the run row
// (so the backup shows in the instance backup history and retention covers it)
// and the instance_upgrade summary the UI shows.
func RecordPreUpgrade(ctx context.Context, pool *pgxpool.Pool, r PreUpgradeResult) error {
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if r.RunID != uuid.Nil {
			if _, err := tx.Exec(ctx, `INSERT INTO instance_backups
(id, trigger, status, dest_kind, dest_label, dest_fingerprint, object_key, size_bytes, sha256, pg_version, actor_label, started_at, finished_at)
VALUES ($1, 'pre_upgrade', 'succeeded', $2, $3, $4, $5, $6, $7, $8, 'upgrade', $9, $10)
ON CONFLICT (id) DO NOTHING`,
				r.RunID, r.DestKind, r.Destination, r.DestFP, r.ObjectKey, r.SizeBytes, r.SHA256, r.PGVersion, r.StartedAt, r.FinishedAt); err != nil {
				return fmt.Errorf("record pre-upgrade backup run: %w", err)
			}
		}
		return db.New(tx).RecordPreUpgradeBackup(ctx, db.RecordPreUpgradeBackupParams{
			PreUpgradeStatus: r.Status, PreUpgradeMessage: r.Message, PreUpgradeFromVersion: r.From, PreUpgradeToVersion: r.To,
		})
	})
}
