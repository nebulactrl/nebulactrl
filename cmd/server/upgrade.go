package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/upgrade"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

// exitRefused is the exit code of `pre-upgrade-backup` when the backup failed
// and the upgrade must not go on (the installer tells it from other failures).
const exitRefused = 3

// exitError carries a process exit code through cobra.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// forceUpgrade is NEBULACTRL_FORCE_UPGRADE: go on although the backup before the
// upgrade failed.
func forceUpgrade() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NEBULACTRL_FORCE_UPGRADE"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func preUpgradeDeps(cfg *config.Config, pool *pgxpool.Pool) (backups.PreUpgradeDeps, error) {
	box, err := secretbox.NewRing(cfg.SecretKey, cfg.PreviousSecretKeys...)
	if err != nil {
		return backups.PreUpgradeDeps{}, fmt.Errorf("secret key: %w", err)
	}
	return backups.PreUpgradeDeps{
		Pool: pool, DatabaseURL: cfg.DatabaseURL, BackupDir: cfg.BackupDir, Box: box,
		KeyFingerprint: backups.KeyFingerprint(cfg.SecretKey),
		Builtin:        instsvc.New(instsvc.Deps{Pool: pool, Box: box}), BuiltinS3URL: cfg.Instance.S3URL,
	}, nil
}

// preflightUpgrade runs before the migrations: when the version changed it backs
// the database up first, and it refuses (an error) when that failed.
func preflightUpgrade(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, logger *slog.Logger) (upgrade.Decision, error) {
	deps, err := preUpgradeDeps(cfg, pool)
	if err != nil {
		return upgrade.Decision{}, err
	}
	return upgrade.Preflight(ctx, upgrade.Deps{
		Current: version, Force: forceUpgrade(), Log: logger,
		State: func(ctx context.Context) (upgrade.State, error) { return upgrade.ReadState(ctx, pool) },
		Backup: func(ctx context.Context, from, to string) backups.PreUpgradeResult {
			return backups.RunPreUpgrade(ctx, deps, from, to)
		},
	})
}

// preUpgradeBackupCmd takes the control-plane backup an upgrade needs and exits.
// The installer runs it inside the running (old) control plane before it
// replaces it.
func preUpgradeBackupCmd() *cobra.Command {
	var to string
	var force bool
	cmd := &cobra.Command{
		Use:   "pre-upgrade-backup [--to <version>] [--force]",
		Short: "Back up the control-plane database before an upgrade",
		Long: `Take a control-plane backup (the instance backup destination, trigger
"pre_upgrade") and record it. The installer runs this in the running control plane
before it replaces it; --to names the version it is about to install, so the new
control plane does not take a second backup at start.

Exit status: 0 backed up (or no destination is configured: a warning is printed),
3 the backup failed and the upgrade must not go on, 1 anything else. --force
turns a failed backup into a warning (exit 0).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cfg, err := config.Load(os.Getenv)
			if err != nil {
				return fmt.Errorf("configuration: %w", err)
			}
			pool, err := connect(ctx, cfg.DatabaseURL, slog.Default())
			if err != nil {
				return err
			}
			defer pool.Close()
			deps, err := preUpgradeDeps(cfg, pool)
			if err != nil {
				return err
			}
			st, err := upgrade.ReadState(ctx, pool)
			if err != nil {
				return fmt.Errorf("read upgrade state: %w", err)
			}
			res := backups.RunPreUpgrade(ctx, deps, st.LastVersion, to)
			out := cmd.OutOrStdout()
			if res.Blocks(force) {
				fmt.Fprintf(out, "backup before the upgrade FAILED: %s\n", res.Message)
				return &exitError{code: exitRefused, err: errors.New("the upgrade must not go on without a backup (use --force to override)")}
			}
			if force {
				res = res.Forced()
			}
			if err := backups.RecordPreUpgrade(ctx, pool, res); err != nil {
				return fmt.Errorf("record the backup: %w", err)
			}
			fmt.Fprintf(out, "%s: %s\n", res.Status, res.Message)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the version about to be installed (so the new control plane skips its own backup)")
	cmd.Flags().BoolVar(&force, "force", false, "go on although the backup failed")
	return cmd
}
