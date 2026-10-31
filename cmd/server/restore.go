package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/backups"
)

// restoreCmd restores a control-plane backup (a scheduled pg_dump written by the
// instance backup, see docs/operations/backup-restore.md) into an EMPTY
// database. It does not load the server configuration: there is no database to
// read settings from, no secret key is needed (the key is not in the backup) and
// credentials for the source come from the environment, never the command line.
func restoreCmd() *cobra.Command {
	var (
		from, at, backupID, databaseURL, tmpDir string
		endpoint, region                        string
		pathStyle, list                         bool
	)
	cmd := &cobra.Command{
		Use:   "restore --from <source> [--at <timestamp>]",
		Short: "Restore a control-plane backup into an empty database",
		Long: `Restore a control-plane backup into an empty database.

--from names where the backups are: an s3://bucket/prefix URL or a directory.
For S3 the credentials are read from NEBULACTRL_RESTORE_ACCESS_KEY and
NEBULACTRL_RESTORE_SECRET_KEY (or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY);
use --endpoint for an S3-compatible store.

--at picks the newest backup created at or before a time: an RFC 3339 timestamp
(2026-10-27T03:30:00Z), "2026-10-27 03:30" (UTC), a date (the end of that UTC
day) or "latest" (default). --list shows what is available and exits.

The target is --database-url, default $NEBULACTRL_DATABASE_URL. It must be empty;
the restore runs in one transaction, so a failure leaves it empty. The backup does
not contain NEBULACTRL_SECRET_KEY: start the control plane with the original key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			src, err := backups.ParseRestoreSource(from)
			if err != nil {
				return err
			}
			if src.Bucket != "" {
				src.Endpoint, src.Region, src.PathStyle = endpoint, region, pathStyle
				src.AccessKey = firstEnv("NEBULACTRL_RESTORE_ACCESS_KEY", "AWS_ACCESS_KEY_ID")
				src.SecretKey = firstEnv("NEBULACTRL_RESTORE_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")
				if src.AccessKey == "" || src.SecretKey == "" {
					return fmt.Errorf("S3 credentials are missing: set NEBULACTRL_RESTORE_ACCESS_KEY and NEBULACTRL_RESTORE_SECRET_KEY")
				}
			} else if endpoint != "" || region != "" || pathStyle {
				return fmt.Errorf("--endpoint, --region and --path-style only apply to s3:// sources")
			}
			when, err := backups.ParseAt(at)
			if err != nil {
				return err
			}
			opts := backups.RestoreOptions{
				Source: src, At: when, BackupID: backupID, DatabaseURL: databaseURL, TmpDir: tmpDir,
				Tools: backups.DefaultPGTools(), Out: cmd.ErrOrStderr(),
			}
			if list {
				return listBackups(cmd, opts)
			}
			if databaseURL == "" {
				return fmt.Errorf("the target database is required: set NEBULACTRL_DATABASE_URL or pass --database-url")
			}
			res, err := backups.RestoreInstance(ctx, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored backup %s (%d tables)\n", res.Backup.Manifest.BackupID, res.Tables)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&from, "from", "", "where the backups are: s3://bucket/prefix or a directory (required)")
	f.StringVar(&at, "at", "latest", "restore the newest backup at or before this time (RFC 3339, \"YYYY-MM-DD HH:MM\" UTC, a date, or latest)")
	f.StringVar(&backupID, "backup-id", "", "restore exactly this backup (id or its first 8 characters); overrides --at")
	f.StringVar(&databaseURL, "database-url", os.Getenv("NEBULACTRL_DATABASE_URL"), "the empty target database (default $NEBULACTRL_DATABASE_URL)")
	f.StringVar(&tmpDir, "tmp-dir", "", "where the dump is downloaded and verified before restoring (default the system temp directory; needs room for one dump)")
	f.StringVar(&endpoint, "endpoint", "", "S3-compatible endpoint URL (s3:// sources; empty = AWS)")
	f.StringVar(&region, "region", "", "S3 region (s3:// sources)")
	f.BoolVar(&pathStyle, "path-style", false, "S3 path-style addressing (MinIO, SeaweedFS, Garage and most self-hosted stores)")
	f.BoolVar(&list, "list", false, "list the backups at the source and exit")
	return cmd
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func listBackups(cmd *cobra.Command, o backups.RestoreOptions) error {
	st, err := o.Source.Store()
	if err != nil {
		return err
	}
	found, err := backups.FindBackups(cmd.Context(), st, o.Source.Prefix)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("no control-plane backups were found at the source")
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCREATED (UTC)\tSIZE\tPOSTGRES\tKEY FINGERPRINT")
	for _, b := range found {
		m := b.Manifest
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.BackupID[:8], m.CreatedAt.UTC().Format(time.RFC3339), backups.HumanBytes(m.SizeBytes), m.EngineVersion, m.KeyFingerprint)
	}
	return tw.Flush()
}
