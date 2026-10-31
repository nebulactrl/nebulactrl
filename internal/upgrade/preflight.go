// Package upgrade is what a control plane does when its own version changed:
// before it migrates the database it takes a control-plane backup, and it
// refuses to go on when that backup failed (unless forced).
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

// FreshFor is how long a backup the installer took for exactly this version
// stands in for the one the starting server would take.
const FreshFor = 24 * time.Hour

// ErrBackupFailed is returned when the pre-upgrade backup failed and the upgrade
// was not forced. The server must not migrate the database.
var ErrBackupFailed = errors.New("the control-plane backup before the upgrade failed")

// State is what the database says about earlier upgrades, read before the
// migrations. The zero value is a database that has never run a control plane.
type State struct {
	// Existing is true when the database already holds a NebulaCtrl schema.
	Existing bool
	// LastVersion is the version that last finished its startup ("" before the
	// version was tracked).
	LastVersion string
	// The latest pre-upgrade backup.
	BackupTo     string
	BackupStatus string
	BackupAt     time.Time
}

// Deps are the collaborators of Preflight.
type Deps struct {
	Current string
	// Force proceeds when the backup failed.
	Force bool
	State func(ctx context.Context) (State, error)
	// Backup takes the pre-upgrade backup (backups.RunPreUpgrade in production).
	Backup func(ctx context.Context, from, to string) backups.PreUpgradeResult
	Now    func() time.Time
	Log    *slog.Logger
}

// Decision is Preflight's verdict.
type Decision struct {
	// Result is the backup taken (nil when none was needed).
	Result *backups.PreUpgradeResult
	// Reason says why no backup was needed, or what happened.
	Reason string
}

// Preflight decides whether this start is an upgrade and, if so, takes the
// backup. A nil error means the server may go on and migrate; ErrBackupFailed
// (wrapped with the cause) means it must not.
func Preflight(ctx context.Context, d Deps) (Decision, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	if _, ok := version.Parse(d.Current); !ok {
		return Decision{Reason: "a development build (version " + quote(d.Current) + " is not a release): no pre-upgrade backup"}, nil
	}
	st, err := d.State(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("read upgrade state: %w", err)
	}
	switch {
	case !st.Existing:
		return Decision{Reason: "a new database: nothing to back up"}, nil
	case st.LastVersion == d.Current:
		return Decision{Reason: "the version did not change"}, nil
	case st.BackupTo == d.Current && (st.BackupStatus == backups.PreUpgradeSucceeded || st.BackupStatus == backups.PreUpgradeForced) && d.Now().Sub(st.BackupAt) < FreshFor:
		return Decision{Reason: "the installer already backed up for this version"}, nil
	}
	if cur, ok := version.Parse(d.Current); ok {
		if last, ok := version.Parse(st.LastVersion); ok && version.Compare(cur, last) < 0 {
			log.Warn("this control plane is OLDER than the one that last ran on this database; migrations only go forward, so it may not work (docs/installing/upgrade.md#roll-back-the-control-plane)",
				"running", d.Current, "last", st.LastVersion)
		}
	}
	log.Info("control plane version changed; backing up before the upgrade", "from", st.LastVersion, "to", d.Current)
	res := d.Backup(ctx, st.LastVersion, d.Current)
	if res.Blocks(d.Force) {
		log.Error("the backup before the upgrade failed; the database is NOT migrated. Fix the cause and restart, or set NEBULACTRL_FORCE_UPGRADE=true to upgrade without a backup.", "error", res.Message)
		return Decision{Result: &res, Reason: res.Message}, fmt.Errorf("%w: %s", ErrBackupFailed, res.Message)
	}
	if d.Force {
		res = res.Forced()
	}
	switch res.Status {
	case backups.PreUpgradeSkipped:
		log.Warn("no backup was taken before the upgrade", "reason", res.Message)
	case backups.PreUpgradeForced:
		log.Warn("upgrading without a backup because the upgrade is forced", "reason", res.Message)
	default:
		log.Info("backup before the upgrade finished", "destination", res.Destination, "bytes", res.SizeBytes)
	}
	return Decision{Result: &res, Reason: res.Message}, nil
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

// ReadState reads State from the database before the migrations ran. Every table
// it reads may be missing (an older version, or a new database).
func ReadState(ctx context.Context, pool *pgxpool.Pool) (State, error) {
	var st State
	var hasGoose, hasUpgrade bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL, to_regclass('instance_upgrade') IS NOT NULL`).Scan(&hasGoose, &hasUpgrade); err != nil {
		return st, err
	}
	if hasGoose {
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE is_applied AND version_id > 0)`).Scan(&st.Existing); err != nil {
			return st, err
		}
	}
	if !hasUpgrade {
		return st, nil
	}
	var at *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_version, pre_upgrade_to_version, pre_upgrade_status, pre_upgrade_at FROM instance_upgrade WHERE id`).
		Scan(&st.LastVersion, &st.BackupTo, &st.BackupStatus, &at); err != nil {
		return st, err
	}
	if at != nil {
		st.BackupAt = *at
	}
	return st, nil
}

// Commit runs after the migrations: it stores the pre-upgrade backup's outcome
// and the version that now runs. A failure here is returned but is not a reason
// to stop: the worst case is one more backup at the next start.
func Commit(ctx context.Context, pool *pgxpool.Pool, d Decision, current string) error {
	if d.Result != nil {
		if err := backups.RecordPreUpgrade(ctx, pool, *d.Result); err != nil {
			return err
		}
	}
	return db.New(pool).SetInstanceLastVersion(ctx, current)
}
