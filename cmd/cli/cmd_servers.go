package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newServersCmd(c *cli) *cobra.Command {
	return group("servers", "Manage Docker hosts",
		newServersLsCmd(c), newServersEnrollCmd(c), newServersUpdateCmd(c), newServersUnrestrictedCmd(c), newServersRevokeCmd(c))
}

func newServersLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls", Short: "List servers", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			all, err := c.servers(ctx)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(all)
			}
			showDrain, showRole, showUnrestricted := false, false, false
			for _, s := range all {
				if s.Unrestricted {
					showUnrestricted = true
				}
				if s.DrainState != "none" {
					showDrain = true
				}
				if s.BuildEnabled {
					showRole = true
				}
			}
			headers := []string{"NAME", "STATUS"}
			if showRole {
				headers = append(headers, "ROLE")
			}
			if showDrain {
				headers = append(headers, "DRAIN")
			}
			if showUnrestricted {
				headers = append(headers, "MODE")
			}
			headers = append(headers, "PUBLIC IP", "SERVICES", "AGENT", "LAST SEEN", "ID")
			rows := make([][]string, 0, len(all))
			for _, s := range all {
				row := []string{s.Name, string(s.Status)}
				if showRole {
					row = append(row, serverRole(s.AcceptWorkloads, s.BuildEnabled))
				}
				if showDrain {
					row = append(row, string(s.DrainState))
				}
				if showUnrestricted {
					mode := "restricted"
					if s.Unrestricted {
						mode = "unrestricted"
					}
					row = append(row, mode)
				}
				rows = append(rows, append(row,
					orDash(deref(s.PublicIp)), fmt.Sprint(s.ServiceCount),
					orDash(deref(s.AgentVersion)), tsPtr(s.LastSeenAt), s.Id))
			}
			c.table(headers, rows)
			return nil
		},
	}
}

func newServersEnrollCmd(c *cli) *cobra.Command {
	var name, publicIP, baseDomain, acmeEmail string
	cmd := &cobra.Command{
		Use: "enroll-token --name <name>", Short: "Create a one-time agent enrollment token", Args: noArgs,
		Long: "Registers a pending server and prints the one-hour, single-use enrollment token together with the\n" +
			"command that installs the agent on the host. The token is shown once.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return usagef("--name is required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			in := apiclient.CreateEnrollmentTokenJSONRequestBody{Name: name}
			if publicIP != "" {
				in.PublicIp = &publicIP
			}
			if baseDomain != "" {
				in.BaseDomain = &baseDomain
			}
			if acmeEmail != "" {
				in.AcmeEmail = &acmeEmail
			}
			r, err := c.api.CreateEnrollmentTokenWithResponse(ctx, &apiclient.CreateEnrollmentTokenParams{IdempotencyKey: c.key()}, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			t := r.JSON201
			c.info("Server   %s (%s)", t.Server.Name, t.Server.Id)
			c.info("Token    %s", t.Token)
			c.info("Expires  %s", ts(t.ExpiresAt))
			if t.InstallCommand != "" {
				c.info("")
				c.info("Run on the host (needs Docker):")
				c.info("  %s", t.InstallCommand)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "server name")
	f.StringVar(&publicIP, "public-ip", "", "public IP, used for DNS checks and sslip.io domains")
	f.StringVar(&baseDomain, "base-domain", "", "base domain for generated hostnames")
	f.StringVar(&acmeEmail, "acme-email", "", "email for Let's Encrypt registration")
	return cmd
}

func newServersRevokeCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "revoke <server-id|name>", Short: "Revoke a server's agent identity", Args: exactArgs(1),
		Long: "Disconnects the agent and cancels its pending commands. The agent must re-enroll to come back.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			s, err := c.resolveServer(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.RevokeServerWithResponse(ctx, s.Id)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.info("Revoked server %s (%s)", r.JSON200.Name, r.JSON200.Id)
			return nil
		},
	}
}
