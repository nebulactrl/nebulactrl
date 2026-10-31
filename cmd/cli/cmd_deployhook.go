package main

import (
	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newServicesDeployHookCmd(c *cli) *cobra.Command {
	var enable, disable, rotate bool
	cmd := &cobra.Command{
		Use: "deploy-hook <service> [--enable|--disable|--rotate]", Short: "Show, enable, disable or rotate a service's deploy hook", Args: exactArgs(1),
		Long: "A deploy hook is a URL (POST /hooks/deploy/<service-id>/<secret>) that deploys the service like a manual deploy,\n" +
			"for CI pipelines and container registries (Docker Hub, GHCR). Without a flag the command shows the hook's state.\n\n" +
			"--enable creates a secret and prints the URL; --rotate replaces the secret of an enabled hook and prints the new URL;\n" +
			"--disable turns the hook off and deletes the secret. The URL is shown once and cannot be read again.\n" +
			"Managing a hook needs the deployments:write scope.",
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, f := range []bool{enable, disable, rotate} {
				if f {
					n++
				}
			}
			if n > 1 {
				return usagef("give at most one of --enable, --disable and --rotate")
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
			var v *apiclient.DeployHookView
			switch {
			case enable, disable:
				r, err := c.api.SetDeployHookWithResponse(ctx, id, apiclient.DeployHookSetting{Enabled: enable})
				if err := check(r, err); err != nil {
					return err
				}
				v = r.JSON200
			case rotate:
				r, err := c.api.RotateDeployHookWithResponse(ctx, id)
				if err := check(r, err); err != nil {
					return err
				}
				v = r.JSON200
			default:
				r, err := c.api.GetDeployHookWithResponse(ctx, id)
				if err := check(r, err); err != nil {
					return err
				}
				v = r.JSON200
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			c.info("Deploy hook:  %s", map[bool]string{true: "on", false: "off"}[v.Enabled])
			if v.IsProduction {
				c.info("Environment:  production")
			}
			if v.Enabled {
				c.info("Last call:    %s (%s)", tsPtr(v.LastCalledAt), orDash(string(v.LastOutcome)))
				c.info("Secret from:  %s", tsPtr(v.SecretSetAt))
				c.info("Rate limit:   %s", v.RateLimit)
			}
			if v.Url != nil && *v.Url != "" {
				c.info("URL:          %s", *v.Url)
				c.note("This URL is shown once; anyone who has it can deploy this service. Store it as a CI secret.")
			} else if v.Enabled {
				c.info("URL:          %s (the secret is not shown again; use --rotate for a new one)", v.UrlPattern)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&enable, "enable", false, "turn the hook on with a new secret and print the URL once")
	f.BoolVar(&disable, "disable", false, "turn the hook off and delete its secret")
	f.BoolVar(&rotate, "rotate", false, "replace the secret of an enabled hook and print the new URL once")
	return cmd
}
