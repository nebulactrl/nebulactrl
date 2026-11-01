package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newBackupsCmd(c *cli) *cobra.Command {
	return group("backups", "Back up and restore managed databases",
		newBackupsLsCmd(c), newBackupsCreateCmd(c), newBackupsRestoreCmd(c), newBackupsRmCmd(c),
		newBackupsLockCmd(c, true), newBackupsLockCmd(c, false),
		group("destinations", "Manage backup destinations (S3-compatible buckets, server directories)",
			newDestLsCmd(c), newDestAddCmd(c), newDestRmCmd(c), newDestTestCmd(c)),
		group("schedules", "Manage recurring backups",
			newSchedLsCmd(c), newSchedAddCmd(c), newSchedRmCmd(c)))
}

func (c *cli) listDestinations(ctx context.Context) ([]apiclient.DestinationView, error) {
	r, err := c.api.ListBackupDestinationsWithResponse(ctx)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return itemsOrEmpty(r.JSON200.Items), nil
}

// destinationID resolves an id or a (case-insensitive) name.
func (c *cli) destinationID(ctx context.Context, ref string) (string, error) {
	ds, err := c.listDestinations(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if d.Id == ref || strings.EqualFold(d.Name, ref) {
			return d.Id, nil
		}
	}
	return "", fmt.Errorf("backup destination %q not found (see `nebulactrl backups destinations ls`)", ref)
}

func backupTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "skipped", "expired":
		return true
	}
	return false
}

// waitBackup polls a backup until it finishes; the exit code is non-zero unless it succeeded.
func (c *cli) waitBackup(parent context.Context, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	last := ""
	for {
		r, err := c.api.GetBackupWithResponse(ctx, id)
		if err := check(r, err); err != nil {
			return err
		}
		b := r.JSON200
		status := string(b.Status)
		if status != last {
			c.info("backup %s: %s", id, status)
			last = status
		}
		if backupTerminal(status) {
			if c.asJSON {
				if err := c.printJSON(b); err != nil {
					return err
				}
			}
			if status != "succeeded" {
				return &exitError{1, fmt.Errorf("backup %s %s: %s", id, status, deref(b.Error))}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return &exitError{1, fmt.Errorf("timed out after %s waiting for backup %s (last status %s); it may still be running", timeout, id, last)}
		case <-time.After(c.pollEvery):
		}
	}
}

func (c *cli) waitRestore(parent context.Context, id string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	last := ""
	for {
		r, err := c.api.GetBackupRestoreWithResponse(ctx, id)
		if err := check(r, err); err != nil {
			return err
		}
		rs := r.JSON200
		status := string(rs.Status)
		if status != last {
			c.info("restore %s: %s", id, status)
			last = status
		}
		if status == "succeeded" || status == "failed" {
			if c.asJSON {
				if err := c.printJSON(rs); err != nil {
					return err
				}
			}
			if status != "succeeded" {
				return &exitError{1, fmt.Errorf("restore %s failed: %s", id, deref(rs.Error))}
			}
			if rs.TargetServiceId != nil && rs.Mode == "new" {
				c.info("restored into the new database %s", *rs.TargetServiceId)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return &exitError{1, fmt.Errorf("timed out after %s waiting for restore %s (last status %s); it may still be running", timeout, id, last)}
		case <-time.After(c.pollEvery):
		}
	}
}

func newBackupsLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <database>", Short: "List a database's backups", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.ListBackupsWithResponse(ctx, id, &apiclient.ListBackupsParams{})
			if err := check(r, err); err != nil {
				return err
			}
			items := itemsOrEmpty(r.JSON200.Items)
			if c.asJSON {
				return c.printJSON(items)
			}
			rows := make([][]string, 0, len(items))
			for _, b := range items {
				rows = append(rows, []string{
					b.CreatedAt.Format(time.RFC3339), string(b.Status), string(b.Trigger), orDash(deref(b.DestinationName)),
					fmt.Sprint(b.SizeBytes), yesNo(b.Locked), b.Id, deref(b.Error),
				})
			}
			c.table([]string{"CREATED", "STATUS", "TRIGGER", "DESTINATION", "BYTES", "LOCKED", "ID", "ERROR"}, rows)
			return nil
		},
	}
}

func newBackupsCreateCmd(c *cli) *cobra.Command {
	var dest string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "create <database> --destination <name|id>", Short: "Back up a database now", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dest == "" {
				return usagef("--destination is required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			did, err := c.destinationID(ctx, dest)
			if err != nil {
				return err
			}
			r, err := c.api.CreateBackupWithResponse(ctx, id, &apiclient.CreateBackupParams{IdempotencyKey: c.key()},
				apiclient.CreateBackupJSONRequestBody{DestinationId: did})
			if err := check(r, err); err != nil {
				return err
			}
			if !wait {
				if c.asJSON {
					return c.printJSON(r.JSON202)
				}
				c.info("Backup %s queued", r.JSON202.Id)
				return nil
			}
			return c.waitBackup(cmd.Context(), r.JSON202.Id, timeout)
		},
	}
	cmd.Flags().StringVar(&dest, "destination", "", "destination name or id")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the backup to finish; exit non-zero unless it succeeded")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Hour, "give up waiting after this long (with --wait)")
	return cmd
}

func newBackupsRestoreCmd(c *cli) *cobra.Command {
	var inPlace, wait bool
	var name, server, confirm string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "restore <backup-id>", Short: "Restore a backup into a new database (default) or in place", Args: exactArgs(1),
		Long: "By default the backup is restored into a NEW sibling database with fresh credentials; the original is\n" +
			"untouched. With --in-place the database is overwritten after a safety backup; --confirm must equal its slug.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			var body apiclient.RestoreBackupJSONRequestBody
			if inPlace {
				if confirm == "" {
					return usagef("--confirm <database slug> is required with --in-place")
				}
				mode := apiclient.RestoreInputBodyMode("in_place")
				body.Mode = &mode
				body.ConfirmSlug = &confirm
			} else {
				mode := apiclient.RestoreInputBodyMode("new")
				body.Mode = &mode
				if name != "" {
					body.Name = &name
				}
				if server != "" {
					body.ServerId = &server
				}
			}
			r, err := c.api.RestoreBackupWithResponse(ctx, args[0], &apiclient.RestoreBackupParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if !wait {
				if c.asJSON {
					return c.printJSON(r.JSON202)
				}
				c.info("Restore %s started", r.JSON202.Id)
				return nil
			}
			return c.waitRestore(cmd.Context(), r.JSON202.Id, timeout)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&inPlace, "in-place", false, "overwrite the database instead of restoring into a new one")
	f.StringVar(&confirm, "confirm", "", "the database's slug (required with --in-place)")
	f.StringVar(&name, "name", "", "name of the new database")
	f.StringVar(&server, "server", "", "server id for the new database")
	f.BoolVar(&wait, "wait", false, "wait for the restore to finish; exit non-zero unless it succeeded")
	f.DurationVar(&timeout, "timeout", 2*time.Hour, "give up waiting after this long (with --wait)")
	return cmd
}

func newBackupsRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <backup-id>", Short: "Delete a backup and its stored object", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.DeleteBackupWithResponse(ctx, args[0])
			if err := check(r, err); err != nil {
				return err
			}
			c.info("Deleting backup %s (the agent removes the stored object)", args[0])
			return nil
		},
	}
}

func newBackupsLockCmd(c *cli, lock bool) *cobra.Command {
	use, short := "unlock <backup-id>", "Allow retention to prune a backup again"
	if lock {
		use, short = "lock <backup-id>", "Keep a backup forever (never pruned or deleted)"
	}
	return &cobra.Command{
		Use: use, Short: short, Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.LockBackupWithResponse(ctx, args[0], apiclient.LockBackupJSONRequestBody{Locked: lock})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.info("Backup %s locked=%v", args[0], r.JSON200.Locked)
			return nil
		},
	}
}

// ---- destinations ------------------------------------------------------------

func newDestLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls", Short: "List backup destinations", Args: noArgs,
		Long: "OFF-SITE is \"yes\" for an external S3 bucket and \"no\" for a directory on a server and for the built-in backup storage, which\n" +
			"lives on the control-plane host: backups kept there are lost with that host, so add an external destination as well.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			ds, err := c.listDestinations(ctx)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(ds)
			}
			rows := make([][]string, 0, len(ds))
			for _, d := range ds {
				where := deref(d.Bucket)
				switch d.Kind {
				case "local":
					where = "agent backup directory/" + deref(d.LocalDir)
				case "builtin":
					where = "built-in storage"
				}
				test := "untested"
				if d.LastTestOk != nil {
					test = map[bool]string{true: "ok", false: "failed"}[*d.LastTestOk]
				}
				rows = append(rows, []string{d.Name, string(d.Kind), where, yesNo(d.OffSite), test, d.Id})
			}
			c.table([]string{"NAME", "KIND", "WHERE", "OFF-SITE", "TEST", "ID"}, rows)
			return nil
		},
	}
}

func newDestAddCmd(c *cli) *cobra.Command {
	var name, kind, endpoint, region, bucket, prefix, accessKey, secretFile, localDir string
	var pathStyle bool
	cmd := &cobra.Command{
		Use: "add --name <n> --kind s3|local", Short: "Add a backup destination", Args: noArgs,
		Long: "The S3 secret key is never taken as an argument (it would show in process lists): pass --secret-key-file\n" +
			"(`-` reads standard input) or set NEBULACTRL_BACKUP_SECRET_KEY.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || kind == "" {
				return usagef("--name and --kind are required")
			}
			body := apiclient.CreateBackupDestinationJSONRequestBody{Name: name, Kind: apiclient.CreateDestinationInputBodyKind(kind)}
			if kind == "s3" {
				secret := os.Getenv("NEBULACTRL_BACKUP_SECRET_KEY")
				if secretFile != "" {
					var raw []byte
					var err error
					if secretFile == "-" {
						raw, err = io.ReadAll(c.in)
					} else {
						raw, err = os.ReadFile(secretFile)
					}
					if err != nil {
						return fmt.Errorf("read secret key: %w", err)
					}
					secret = strings.TrimSpace(string(raw))
				}
				if accessKey == "" || secret == "" || bucket == "" {
					return usagef("an s3 destination needs --bucket, --access-key and a secret key (--secret-key-file or NEBULACTRL_BACKUP_SECRET_KEY)")
				}
				body.Bucket, body.AccessKey, body.SecretKey = &bucket, &accessKey, &secret
				body.PathStyle = &pathStyle
				if endpoint != "" {
					body.Endpoint = &endpoint
				}
				if region != "" {
					body.Region = &region
				}
			} else if localDir != "" {
				body.LocalDir = &localDir
			}
			if prefix != "" {
				body.Prefix = &prefix
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.CreateBackupDestinationWithResponse(ctx, &apiclient.CreateBackupDestinationParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Added destination %s (%s)", r.JSON201.Name, r.JSON201.Id)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "destination name")
	f.StringVar(&kind, "kind", "", "s3 or local")
	f.StringVar(&endpoint, "endpoint", "", "S3 endpoint URL (empty: AWS)")
	f.StringVar(&region, "region", "", "S3 region")
	f.StringVar(&bucket, "bucket", "", "S3 bucket")
	f.StringVar(&prefix, "prefix", "", "key prefix")
	f.BoolVar(&pathStyle, "path-style", false, "path-style addressing (MinIO, SeaweedFS, Garage)")
	f.StringVar(&accessKey, "access-key", "", "S3 access key id")
	f.StringVar(&secretFile, "secret-key-file", "", "file holding the S3 secret key (`-` for stdin)")
	f.StringVar(&localDir, "local-dir", "", "local destinations: sub-directory of the agent's backup directory")
	return cmd
}

func newDestRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <name|id>", Short: "Remove a backup destination", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.destinationID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.DeleteBackupDestinationWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			c.info("Removed destination %s", args[0])
			return nil
		},
	}
}

func newDestTestCmd(c *cli) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use: "test <name|id> --server <server-id>", Short: "Write, read and delete a probe object through a server's agent", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if server == "" {
				return usagef("--server is required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.destinationID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.TestBackupDestinationWithResponse(ctx, id, apiclient.TestBackupDestinationJSONRequestBody{ServerId: server})
			if err := check(r, err); err != nil {
				return err
			}
			d := r.JSON200
			if c.asJSON {
				if err := c.printJSON(d); err != nil {
					return err
				}
			}
			switch {
			case d.LastTestOk == nil:
				return &exitError{1, fmt.Errorf("no answer from the agent yet; check `nebulactrl backups destinations ls`")}
			case !*d.LastTestOk:
				return &exitError{1, fmt.Errorf("destination test failed: %s", deref(d.LastTestMessage))}
			}
			c.info("Destination %s works: %s", d.Name, deref(d.LastTestMessage))
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "server whose agent runs the test")
	return cmd
}

// ---- schedules ---------------------------------------------------------------

func newSchedLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <database>", Short: "List a database's backup schedules", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.ListBackupSchedulesWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			items := itemsOrEmpty(r.JSON200.Items)
			if c.asJSON {
				return c.printJSON(items)
			}
			rows := make([][]string, 0, len(items))
			for _, s := range items {
				rows = append(rows, []string{
					s.Cron, s.Timezone, orDash(deref(s.DestinationName)), fmt.Sprintf("%d / %dd", s.RetentionCount, s.RetentionDays),
					yesNo(s.Enabled), tsPtr(s.NextRunAt), s.Id,
				})
			}
			c.table([]string{"CRON", "TZ", "DESTINATION", "KEEP / DAYS", "ENABLED", "NEXT RUN", "ID"}, rows)
			return nil
		},
	}
}

func newSchedAddCmd(c *cli) *cobra.Command {
	var dest, cron, tz string
	var keep, days int
	cmd := &cobra.Command{
		Use: "add <database> --destination <name|id> --cron \"0 3 * * *\"", Short: "Schedule recurring backups", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dest == "" || cron == "" {
				return usagef("--destination and --cron are required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			did, err := c.destinationID(ctx, dest)
			if err != nil {
				return err
			}
			k, d := int64(keep), int64(days)
			r, err := c.api.CreateBackupScheduleWithResponse(ctx, id, &apiclient.CreateBackupScheduleParams{IdempotencyKey: c.key()},
				apiclient.CreateBackupScheduleJSONRequestBody{DestinationId: did, Cron: cron, Timezone: &tz, RetentionCount: &k, RetentionDays: &d})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Scheduled %q (%s); next run %s", cron, tz, tsPtr(r.JSON201.NextRunAt))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dest, "destination", "", "destination name or id")
	f.StringVar(&cron, "cron", "", "5-field cron expression or @daily/@hourly/…")
	f.StringVar(&tz, "timezone", "UTC", "IANA timezone")
	f.IntVar(&keep, "keep", 7, "keep the newest N backups (0 = no limit)")
	f.IntVar(&days, "keep-days", 0, "delete backups older than N days (0 = never)")
	return cmd
}

func newSchedRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <schedule-id>", Short: "Remove a backup schedule", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.DeleteBackupScheduleWithResponse(ctx, args[0])
			if err := check(r, err); err != nil {
				return err
			}
			c.info("Removed schedule %s", args[0])
			return nil
		},
	}
}
