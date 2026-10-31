package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

// exitError makes run return a specific exit code. A nil err prints nothing.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// usageError marks wrong flags or arguments (exit code 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	c := &cli{in: in, out: out, errOut: errOut, pollEvery: 2 * time.Second}
	return c.execute(ctx, args)
}

// execute runs one invocation on c and returns the exit code.
func (c *cli) execute(ctx context.Context, args []string) int {
	root := newRoot(c)
	root.SetArgs(args)
	root.SetIn(c.in)
	root.SetOut(c.out)
	root.SetErr(c.errOut)
	errOut := c.errOut
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(errOut, "Error:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(errOut, "Error:", err)
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(errOut, "Run with --help for usage.")
		return 2
	}
	return 1
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return usageError{fmt.Errorf("%w\nusage: %s", err, cmd.UseLine())}
		}
		return nil
	}
}

func minArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MinimumNArgs(n)(cmd, args); err != nil {
			return usageError{fmt.Errorf("%w\nusage: %s", err, cmd.UseLine())}
		}
		return nil
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.NoArgs(cmd, args); err != nil {
		return usageError{err}
	}
	return nil
}

// group is a command that only holds subcommands.
func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError{fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(subs...)
	return cmd
}

func newRoot(c *cli) *cobra.Command {
	root := &cobra.Command{
		Use:           "nebulactrl",
		Short:         "NebulaCtrl command line client",
		Long:          "Manage NebulaCtrl projects, services, deployments and servers through the control plane REST API.\n\nA <service> is a service ID or project/env/slug. Configuration lives in ~/.config/nebulactrl/config.json (override with NEBULACTRL_CONFIG; NEBULACTRL_URL and NEBULACTRL_TOKEN override its fields).",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError{fmt.Errorf("unknown command %q", args[0])}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.PersistentFlags().BoolVar(&c.asJSON, "json", false, "print machine-readable JSON")
	root.PersistentFlags().StringVar(&c.idemKey, "idempotency-key", "", "Idempotency-Key for mutating calls (default: random per invocation)")
	root.PersistentFlags().BoolVar(&c.insecure, "insecure", false, "allow the API token to travel over plain http to a host other than this machine (default: https only)")
	root.AddCommand(
		newLoginCmd(c),
		newProjectsCmd(c),
		newEnvsCmd(c),
		newServicesCmd(c),
		newBuildSettingsCmd(c),
		newVarsCmd(c),
		newDomainsCmd(c),
		newBackupsCmd(c),
		newTasksCmd(c),
		newDatabasesCmd(c),
		newDeploymentsCmd(c),
		newServersCmd(c),
		newMeshCmd(c),
		newTemplatesCmd(c),
		newImportCmd(c),
		newConfigCmd(c),
		newTeamsCmd(c),
		newMembersCmd(c),
		newInstanceCmd(c),
		newDocsCmd(c),
	)
	return root
}
