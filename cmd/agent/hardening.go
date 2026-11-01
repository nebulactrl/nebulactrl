package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// newHardeningCommand is `nebulactrl-agent hardening`: the Lynis audit and the
// curated safe fixes, run directly on this host as root. The installer uses
// `hardening run`; the control plane drives the same code through the
// host_hardening command.
func newHardeningCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hardening",
		Short: "Audit this host with Lynis and apply safe hardening fixes",
		Long: "Runs on this machine as root. It never changes SSH settings, firewall rules or user accounts.\n" +
			"Every fix can be undone with `hardening rollback`.",
	}
	var (
		asJSON  bool
		fixes   []string
		noApply bool
		noInst  bool
		install bool
	)
	runner := func() *hardening.Service { return hardening.New(hardening.DirectRunner{}, nil) }
	ctxOf := func(cmd *cobra.Command) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	}
	needRoot := func() error {
		if os.Geteuid() != 0 {
			return fmt.Errorf("hardening changes system files: run it as root (sudo)")
		}
		return nil
	}

	run := &cobra.Command{
		Use:   "run",
		Short: "Install Lynis, audit, apply the default safe fixes, audit again",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needRoot(); err != nil {
				return err
			}
			ctx, stop := ctxOf(c)
			defer stop()
			return runAll(ctx, c.OutOrStdout(), runner(), fixes, noApply, noInst, asJSON)
		},
	}
	run.Flags().StringSliceVar(&fixes, "fix", nil, "apply these fixes instead of the default set (see `hardening list`)")
	run.Flags().BoolVar(&noApply, "no-apply", false, "audit only")
	run.Flags().BoolVar(&noInst, "no-install", false, "do not install Lynis when it is missing")

	audit := &cobra.Command{
		Use:   "audit",
		Short: "Run a Lynis audit and print the hardening index",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needRoot(); err != nil {
				return err
			}
			ctx, stop := ctxOf(c)
			defer stop()
			return runOne(ctx, c.OutOrStdout(), runner(), agentprotocol.HardeningPayload{
				Action: agentprotocol.HardeningActionAudit, Trigger: agentprotocol.HardeningTriggerManual, InstallLynis: install,
			}, asJSON)
		},
	}
	audit.Flags().BoolVar(&install, "install", false, "install Lynis when it is missing")

	apply := &cobra.Command{
		Use:   "apply",
		Short: "Apply safe fixes (the default set, or --fix ...), then audit",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needRoot(); err != nil {
				return err
			}
			ctx, stop := ctxOf(c)
			defer stop()
			return runOne(ctx, c.OutOrStdout(), runner(), agentprotocol.HardeningPayload{
				Action: agentprotocol.HardeningActionApply, Trigger: agentprotocol.HardeningTriggerManual, FixIDs: fixes,
			}, asJSON)
		},
	}
	apply.Flags().StringSliceVar(&fixes, "fix", nil, "fix IDs (see `hardening list`)")

	var rbFixes []string
	rollback := &cobra.Command{
		Use:   "rollback",
		Short: "Undo fixes NebulaCtrl applied",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needRoot(); err != nil {
				return err
			}
			ctx, stop := ctxOf(c)
			defer stop()
			return runOne(ctx, c.OutOrStdout(), runner(), agentprotocol.HardeningPayload{
				Action: agentprotocol.HardeningActionRollback, Trigger: agentprotocol.HardeningTriggerManual, FixIDs: rbFixes,
			}, asJSON)
		},
	}
	rollback.Flags().StringSliceVar(&rbFixes, "fix", nil, "fix IDs to undo [required]")
	_ = rollback.MarkFlagRequired("fix")

	list := &cobra.Command{
		Use:   "list",
		Short: "List the safe fixes and whether each is part of the default set",
		Args:  cobra.NoArgs,
		Run: func(c *cobra.Command, _ []string) {
			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "FIX\tDEFAULT\tLYNIS\tTITLE")
			for _, f := range lynis.Catalog() {
				fmt.Fprintf(tw, "%s\t%v\t%s\t%s\n", f.ID, f.Default, strings.Join(f.Tests, ","), f.Title)
			}
			_ = tw.Flush()
		},
	}
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	cmd.AddCommand(run, audit, apply, rollback, list)
	return cmd
}

func call(ctx context.Context, svc *hardening.Service, p agentprotocol.HardeningPayload) (agentprotocol.HardeningResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	return svc.Handle(ctx, p)
}

func runOne(ctx context.Context, w io.Writer, svc *hardening.Service, p agentprotocol.HardeningPayload, asJSON bool) error {
	res, err := call(ctx, svc, p)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(w).Encode(res)
	}
	printResult(w, res)
	return nil
}

// runAll is the installer's step: the index before, the fixes, the index after.
func runAll(ctx context.Context, w io.Writer, svc *hardening.Service, ids []string, noApply, noInstall, asJSON bool) error {
	before, err := call(ctx, svc, agentprotocol.HardeningPayload{
		Action: agentprotocol.HardeningActionAudit, Trigger: agentprotocol.HardeningTriggerManual, InstallLynis: !noInstall,
	})
	if err != nil {
		return err
	}
	if !before.LynisInstalled {
		return fmt.Errorf("Lynis is not installed (rerun without --no-install)")
	}
	if noApply {
		if asJSON {
			return json.NewEncoder(w).Encode(before)
		}
		printResult(w, before)
		return nil
	}
	after, err := call(ctx, svc, agentprotocol.HardeningPayload{
		Action: agentprotocol.HardeningActionApply, Trigger: agentprotocol.HardeningTriggerManual, FixIDs: ids,
	})
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(w).Encode(map[string]any{"before": before, "after": after})
	}
	fmt.Fprintf(w, "Lynis %s audited this host.\n", before.LynisVersion)
	fmt.Fprintf(w, "Hardening index before: %d\n", before.Report.HardeningIndex)
	printOutcomes(w, after)
	if after.Report != nil {
		fmt.Fprintf(w, "Hardening index after:  %d (%+d)\n", after.Report.HardeningIndex, after.Report.HardeningIndex-before.Report.HardeningIndex)
		fmt.Fprintf(w, "Remaining findings: %d suggestions, %d warnings. The server's Security tab explains each one.\n",
			len(after.Report.Suggestions), len(after.Report.Warnings))
	} else if after.Message != "" {
		fmt.Fprintln(w, after.Message)
	}
	if after.RebootRequired {
		fmt.Fprintln(w, "A reboot is needed for some fixes to take effect.")
	}
	return nil
}

func printResult(w io.Writer, res agentprotocol.HardeningResult) {
	if res.Report != nil {
		fmt.Fprintf(w, "Lynis %s: hardening index %d (%d tests, %d warnings, %d suggestions)\n",
			res.LynisVersion, res.Report.HardeningIndex, res.Report.TestsDone, len(res.Report.Warnings), len(res.Report.Suggestions))
	} else if res.Message != "" {
		fmt.Fprintln(w, res.Message)
	}
	printOutcomes(w, res)
	if len(res.Outcomes) == 0 && len(res.Fixes) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, f := range res.Fixes {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", f.ID, f.State, f.Message)
		}
		_ = tw.Flush()
	}
}

func printOutcomes(w io.Writer, res agentprotocol.HardeningResult) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, o := range res.Outcomes {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", o.ID, o.Outcome, o.Message)
	}
	_ = tw.Flush()
}
