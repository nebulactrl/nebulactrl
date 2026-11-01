package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretbox/rekey"
)

// rekeyCmd re-encrypts every sealed column to the current NEBULACTRL_SECRET_KEY
// and the v2 ciphertext format. It is the second step of a key rotation
// (docs/security/secrets-at-rest.md).
func rekeyCmd() *cobra.Command {
	var (
		dryRun bool
		batch  int
	)
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Re-encrypt every sealed value to the current secret key",
		Long: `Re-encrypt every sealed value in the database to the current NEBULACTRL_SECRET_KEY.

Key rotation:
  1. nebulactrl-server keygen                      print a new key
  2. Set NEBULACTRL_SECRET_KEY to the new key and NEBULACTRL_SECRET_KEY_PREVIOUS
     to the old one (or the *_FILE variants), then restart the control plane.
  3. nebulactrl-server rekey --dry-run             see what would change
  4. nebulactrl-server rekey                       rewrite everything
  5. Remove NEBULACTRL_SECRET_KEY_PREVIOUS and restart. Keep a backup of the old key
     until your database backups from before the rotation are no longer needed.

rekey also upgrades values written before key ids and row binding existed (the
v1 format) under the current key. It works in batches, each in a transaction,
resumes where it stopped, and a second run changes nothing. It can run next to
a running control plane. Values that cannot be decrypted are left untouched and
reported; the command then exits with an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// The process holds the keys: no core dumps, not dumpable.
			_ = hardenProcess()
			cfg, err := config.Load(os.Getenv)
			if err != nil {
				return fmt.Errorf("configuration: %w", err)
			}
			box, err := secretbox.NewRing(cfg.SecretKey, cfg.PreviousSecretKeys...)
			if err != nil {
				return err
			}
			only, err := secretbox.New(cfg.SecretKey)
			if err != nil {
				return err
			}
			secretbox.Wipe(cfg.SecretKey)
			for _, k := range cfg.PreviousSecretKeys {
				secretbox.Wipe(k)
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
			pool, err := connect(ctx, cfg.DatabaseURL, logger)
			if err != nil {
				return err
			}
			defer pool.Close()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "current key id %s; key ring %v\n", box.KeyID(), box.KeyIDs())
			rep, err := rekey.Run(ctx, pool, box, rekey.Options{Batch: batch, DryRun: dryRun, Verify: verifyBox(dryRun, only), Logger: logger})
			printRekeyReport(out, rep)
			if err != nil {
				return err
			}
			switch {
			case rep.Failed() > 0:
				return fmt.Errorf("%d value(s) could not be decrypted with the keys in the ring; they were left unchanged", rep.Failed())
			case dryRun:
				fmt.Fprintf(out, "dry run: %d value(s) would be re-encrypted\n", rep.Rewritten())
			default:
				fmt.Fprintf(out, "done: every sealed value opens with the current key alone; NEBULACTRL_SECRET_KEY_PREVIOUS can be removed\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "decrypt every value that needs rewriting, write nothing")
	cmd.Flags().IntVar(&batch, "batch", rekey.DefaultBatch, "rows per transaction")
	return cmd
}

func verifyBox(dryRun bool, only *secretbox.Box) *secretbox.Box {
	if dryRun {
		return nil
	}
	return only
}

func printRekeyReport(w io.Writer, rep rekey.Report) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "COLUMN\tVALUES\tCURRENT BEFORE\tV1 BEFORE\tREWRITTEN\tFAILED")
	for _, c := range rep.Columns {
		if c.Total == 0 && c.Failed == 0 {
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\n", c.Column, c.Total, c.Current, c.Legacy, c.Rewritten, c.Failed)
	}
	_ = tw.Flush()
	for _, c := range rep.Columns {
		for _, id := range c.FailedRows {
			fmt.Fprintf(w, "cannot decrypt %s row %s\n", c.Column, id)
		}
	}
}
