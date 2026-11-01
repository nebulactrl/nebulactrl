package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// A token belongs to one team, so "switching" teams in the CLI means choosing
// which saved token to use: `login` saves the token under its team's slug and
// `teams switch <slug>` selects another saved one.

func newTeamsCmd(c *cli) *cobra.Command {
	ls := &cobra.Command{
		Use: "ls", Short: "List your teams and the saved token per team", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.ListTeamsWithResponse(ctx)
			if err := check(r, err); err != nil {
				return err
			}
			items := deref(r.JSON200.Items)
			if c.asJSON {
				return c.printJSON(items)
			}
			var rows [][]string
			for _, t := range items {
				active, saved := "", "no"
				if t.Current {
					active = "*"
				}
				if _, ok := c.cfg.Profiles[t.Slug]; ok || t.Current {
					saved = "yes"
				}
				rows = append(rows, []string{active, t.Name, t.Slug, string(t.Role), fmt.Sprint(t.MemberCount), saved, t.Id})
			}
			c.table([]string{"", "NAME", "SLUG", "ROLE", "MEMBERS", "TOKEN SAVED", "ID"}, rows)
			return nil
		},
	}

	create := &cobra.Command{
		Use: "create <name>", Short: "Create a team (you become its owner; needs an admin-scope token)", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.CreateTeamWithResponse(ctx, &apiclient.CreateTeamParams{IdempotencyKey: c.key()}, apiclient.CreateTeamInputBody{Name: args[0]})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Created team %s (%s)", r.JSON201.Name, r.JSON201.Slug)
			c.info("API tokens belong to one team: create a token inside it (console: Account settings → API tokens), then run `nebulactrl login --token …`.")
			return nil
		},
	}

	switchCmd := &cobra.Command{
		Use: "switch <slug>", Short: "Use the saved token of another team", Args: exactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			slug := strings.TrimSpace(args[0])
			tok, ok := cfg.Profiles[slug]
			if !ok {
				return fmt.Errorf("no API token saved for team %q: create one in that team (Account settings → API tokens) and run `nebulactrl login --url %s --token <token>`", slug, cfg.URL)
			}
			cfg.Team, cfg.Token = slug, tok
			if _, err := saveConfig(cfg); err != nil {
				return err
			}
			c.info("Now using team %s", slug)
			return nil
		},
	}
	return group("teams", "Manage teams", ls, create, switchCmd)
}

func newMembersCmd(c *cli) *cobra.Command {
	currentTeam := func(cmd *cobra.Command) (string, error) {
		if err := c.connect(); err != nil {
			return "", err
		}
		ctx, cancel := c.ctx(cmd.Context())
		defer cancel()
		r, err := c.api.GetMeWithResponse(ctx)
		if err := check(r, err); err != nil {
			return "", err
		}
		if r.JSON200.WorkspaceId == "" || strings.Trim(r.JSON200.WorkspaceId, "0-") == "" {
			return "", errors.New("the token belongs to no team")
		}
		return r.JSON200.WorkspaceId, nil
	}

	ls := &cobra.Command{
		Use: "ls", Short: "List the members of the token's team", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			team, err := currentTeam(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.ListTeamMembersWithResponse(ctx, team)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(deref(r.JSON200.Items))
			}
			var rows [][]string
			for _, m := range deref(r.JSON200.Items) {
				access := "all projects"
				if m.ProjectIds != nil {
					access = fmt.Sprintf("%d projects", len(*m.ProjectIds))
				}
				rows = append(rows, []string{m.Name, m.Email, string(m.Role), access, tsPtr(&m.JoinedAt)})
			}
			c.table([]string{"NAME", "EMAIL", "ROLE", "PROJECTS", "JOINED"}, rows)
			return nil
		},
	}

	var email, role string
	var uses, days int64
	invite := &cobra.Command{
		Use: "invite [--email <address>] --role <role>", Short: "Invite someone by email, or create a link invite", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if role == "" {
				return usagef("--role is required (owner, admin, developer or viewer)")
			}
			team, err := currentTeam(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			body := apiclient.CreateInviteInputBody{Role: apiclient.CreateInviteInputBodyRole(role)}
			if email != "" {
				body.Email = &email
			}
			if uses > 0 {
				body.MaxUses = &uses
			}
			if days > 0 {
				body.ExpiresInDays = &days
			}
			r, err := c.api.CreateTeamInviteWithResponse(ctx, team, &apiclient.CreateTeamInviteParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Invite link (shown once): %s", r.JSON201.Link)
			c.info("Email: %s", r.JSON201.Delivery)
			return nil
		},
	}
	invite.Flags().StringVar(&email, "email", "", "mail the invite to this address (single use); omit for a link invite")
	invite.Flags().StringVar(&role, "role", "", "owner, admin, developer or viewer")
	invite.Flags().Int64Var(&uses, "max-uses", 0, "link invites: how many people may use it (default 1)")
	invite.Flags().Int64Var(&days, "expires-in-days", 0, "validity in days (default 7)")
	return group("members", "Manage the members of your team", ls, invite)
}
