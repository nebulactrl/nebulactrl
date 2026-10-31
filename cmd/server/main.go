// Command nebulactrl-server is the NebulaCtrl control plane.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // IANA zones for task and backup schedules on images without /usr/share/zoneinfo

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "nebulactrl-server",
		Short:         "NebulaCtrl control plane",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		RunE:          runServe, // `serve` is the default command
	}
	root.AddCommand(serveCmd(), keygenCmd(), openapiCmd(), healthcheckCmd(), restoreCmd(), rekeyCmd(), resetPasswordCmd(), reset2FACmd(), preUpgradeBackupCmd())
	return root
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the control plane (default)",
		Args:  cobra.NoArgs,
		RunE:  runServe,
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx)
}
