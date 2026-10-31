package main

import (
	"strings"

	"github.com/spf13/cobra"
)

func newServicesBuildPlanCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "build-plan <service>", Short: "Preview how a git service would be built", Args: exactArgs(1),
		Long: "Asks the service's server to clone the branch head and report the build method (Dockerfile, Railpack or a\n" +
			"starter Dockerfile), the detected providers and the start command. Nothing is built or deployed.",
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
			r, err := c.api.PreviewBuildPlanWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			p := r.JSON200
			if p.Info == nil {
				c.info("No plan: %s", deref(p.Message))
				return nil
			}
			c.info("Method:   %s", p.Info.Method)
			c.info("Why:      %s", deref(p.Info.Reason))
			if p.Info.RailpackVersion != nil {
				c.info("Railpack: %s", *p.Info.RailpackVersion)
			}
			if p.Info.Providers != nil {
				c.info("Provider: %s", strings.Join(*p.Info.Providers, ", "))
			}
			if p.Info.StartCommand != nil {
				c.info("Start:    %s", *p.Info.StartCommand)
			}
			if p.Outcome != "succeeded" {
				c.info("Problem:  %s", deref(p.Message))
			}
			return nil
		},
	}
}
