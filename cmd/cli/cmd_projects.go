package main

import (
	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newProjectsCmd(c *cli) *cobra.Command {
	ls := &cobra.Command{
		Use: "ls", Short: "List projects", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			all, err := c.projects(ctx)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(all)
			}
			rows := make([][]string, 0, len(all))
			for _, p := range all {
				rows = append(rows, []string{p.Name, p.Slug, p.Id, orDash(p.Description)})
			}
			c.table([]string{"NAME", "SLUG", "ID", "DESCRIPTION"}, rows)
			return nil
		},
	}

	var description string
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a project (with a production environment)", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			in := apiclient.CreateProjectInputBody{Name: args[0]}
			if description != "" {
				in.Description = &description
			}
			r, err := c.api.CreateProjectWithResponse(ctx, &apiclient.CreateProjectParams{IdempotencyKey: c.key()}, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Created project %s (%s)", r.JSON201.Name, r.JSON201.Id)
			return nil
		},
	}
	create.Flags().StringVar(&description, "description", "", "project description")

	return group("projects", "Manage projects", ls, create, newProjectsUpdateCmd(c))
}

func newEnvsCmd(c *cli) *cobra.Command {
	var project string
	ls := &cobra.Command{
		Use: "ls --project <project>", Short: "List the environments of a project", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" {
				return usagef("--project is required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			p, err := c.resolveProject(ctx, project)
			if err != nil {
				return err
			}
			envs, err := c.environments(ctx, p.Id)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(envs)
			}
			rows := make([][]string, 0, len(envs))
			for _, e := range envs {
				rows = append(rows, []string{e.Name, e.Slug, yesNo(e.IsProduction), e.Id})
			}
			c.table([]string{"NAME", "SLUG", "PRODUCTION", "ID"}, rows)
			return nil
		},
	}
	ls.Flags().StringVar(&project, "project", "", "project ID, slug or name")
	return group("envs", "Manage environments", ls, newEnvsCloneCmd(c))
}
