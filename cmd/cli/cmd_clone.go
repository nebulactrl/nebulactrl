package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// cloneFlags are the flags services clone and envs clone share.
type cloneFlags struct {
	server, domains, suffix string
	dryRun                  bool
}

func (f *cloneFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.server, "server", "", "place the clone on this server (ID or name; default: the source's server)")
	cmd.Flags().StringVar(&f.domains, "domains", "drop", "domains of the clone: drop (attach new ones later) or suffix (suffix the first label of each hostname)")
	cmd.Flags().StringVar(&f.suffix, "domain-suffix", "", "suffix for --domains suffix (default: -copy in the same environment, -<environment slug> otherwise)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "show what the clone would do and change nothing")
}

func (f *cloneFlags) check() error {
	if f.domains != "drop" && f.domains != "suffix" {
		return usagef("--domains must be drop or suffix")
	}
	if f.suffix != "" && f.domains != "suffix" {
		return usagef("--domain-suffix only applies with --domains suffix")
	}
	return nil
}

func newServicesCloneCmd(c *cli) *cobra.Command {
	var project, env, name string
	var f cloneFlags
	cmd := &cobra.Command{
		Use:   "clone <service> [--env <env> [--project <project>]] [--name <name>] [--domains drop|suffix] [--dry-run]",
		Short: "Clone a service (staged copy; secrets are copied sealed)",
		Long: "Clone a service into the same or another environment. The clone is staged: nothing runs until it is deployed.\n\n" +
			"It gets the configuration, variables (secrets stay secret, generated values are generated again), volume definitions (new, empty volumes) and, in the same environment, the source's links. " +
			"A managed database is created new and empty: restore a dump into it. Domains are dropped by default or cloned with a suffix.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.check(); err != nil {
				return err
			}
			if project != "" && env == "" {
				return usagef("--project needs --env")
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
			body := apiclient.CloneServiceInputBody{}
			if name != "" {
				body.Name = &name
			}
			if env != "" {
				envID, err := c.envOfService(ctx, id, project, env)
				if err != nil {
					return err
				}
				body.EnvironmentId = &envID
			}
			if f.server != "" {
				s, err := c.resolveServer(ctx, f.server)
				if err != nil {
					return err
				}
				body.ServerId = &s.Id
			}
			d := apiclient.CloneServiceInputBodyDomains(f.domains)
			body.Domains = &d
			if f.suffix != "" {
				body.DomainSuffix = &f.suffix
			}
			if f.dryRun {
				body.DryRun = &f.dryRun
			}
			r, err := c.api.CloneServiceWithResponse(ctx, id, &apiclient.CloneServiceParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.printClone(r.JSON200)
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "environment of the clone (ID, slug or name; default: the source's environment)")
	cmd.Flags().StringVar(&project, "project", "", "project of --env (default: the source's project)")
	cmd.Flags().StringVar(&name, "name", "", "name of the clone (default: \"<name> copy\" in the same environment)")
	f.add(cmd)
	return cmd
}

func newEnvsCloneCmd(c *cli) *cobra.Command {
	var project, name string
	var f cloneFlags
	cmd := &cobra.Command{
		Use:   "clone <env> --project <project> --name <name> [--domains drop|suffix] [--dry-run]",
		Short: "Clone an environment into a new, non-production one",
		Long: "Create a new environment with a staged copy of every service of <env>: configuration, variables, the environment's shared variables, volume definitions, links and the canvas layout. " +
			"Slugs are kept, so ${{postgres.DATABASE_URL}} in a cloned service points at the cloned postgres. Secrets are copied sealed and generated values are generated again. " +
			"Databases are new and empty (restore a dump into them); volumes are new and empty. Nothing runs until the services are deployed.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" || name == "" {
				return usagef("--project and --name are required")
			}
			if err := f.check(); err != nil {
				return err
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
			e, err := c.resolveEnv(ctx, p.Id, args[0])
			if err != nil {
				return err
			}
			body := apiclient.CloneEnvironmentInputBody{Name: name}
			if f.server != "" {
				s, err := c.resolveServer(ctx, f.server)
				if err != nil {
					return err
				}
				body.ServerId = &s.Id
			}
			d := apiclient.CloneEnvironmentInputBodyDomains(f.domains)
			body.Domains = &d
			if f.suffix != "" {
				body.DomainSuffix = &f.suffix
			}
			if f.dryRun {
				body.DryRun = &f.dryRun
			}
			r, err := c.api.CloneEnvironmentWithResponse(ctx, e.Id, &apiclient.CloneEnvironmentParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.printClone(r.JSON200)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project ID, slug or name")
	cmd.Flags().StringVar(&name, "name", "", "name of the new environment")
	f.add(cmd)
	return cmd
}

func newServicesMoveCmd(c *cli) *cobra.Command {
	var project, env string
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:   "move <service> --env <env> [--project <project>] [--force] [--dry-run]",
		Short: "Move a service to another environment (staged until the next deploy)",
		Long: "Move a service to another environment. It keeps running where it is until its next deployment, which joins the new environment's network and variables. " +
			"References are checked first: variables that point at services staying behind, and other services' variables that point at the moved one, break. " +
			"Without --force a move that breaks references is refused; --dry-run prints the plan. Canvas links of the service are removed.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if env == "" {
				return usagef("--env is required")
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
			envID, err := c.envOfService(ctx, id, project, env)
			if err != nil {
				return err
			}
			body := apiclient.MoveServiceInputBody{EnvironmentId: envID}
			if force {
				body.Force = &force
			}
			if dryRun {
				body.DryRun = &dryRun
			}
			r, err := c.api.MoveServiceWithResponse(ctx, id, body)
			if err := check(r, err); err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Code == "move_blocked" {
					return fmt.Errorf("%w\nrun with --dry-run to see the references and links, or --force to move anyway", err)
				}
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.printMove(r.JSON200)
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "target environment (ID, slug or name)")
	cmd.Flags().StringVar(&project, "project", "", "project of --env (default: the service's project)")
	cmd.Flags().BoolVar(&force, "force", false, "move even though references from or to the service break")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan (references, links, slug) and change nothing")
	return cmd
}

// envOfService resolves env (ID, slug or name) to an environment ID in project,
// or in the project of the service when project is empty.
func (c *cli) envOfService(ctx context.Context, serviceID, project, env string) (string, error) {
	var projectID string
	if project != "" {
		p, err := c.resolveProject(ctx, project)
		if err != nil {
			return "", err
		}
		projectID = p.Id
	} else {
		r, err := c.api.GetServiceWithResponse(ctx, serviceID)
		if err := check(r, err); err != nil {
			return "", err
		}
		projectID = r.JSON200.ProjectId
	}
	e, err := c.resolveEnv(ctx, projectID, env)
	if err != nil {
		return "", err
	}
	return e.Id, nil
}

func (c *cli) printClone(r *apiclient.CloneResult) {
	verb := "Cloned"
	if r.DryRun {
		verb = "Would clone"
	}
	svcs := deref(r.Services)
	if r.Environment != nil {
		c.info("%s into the new environment %s (%s), not production.", verb, r.Environment.Name, r.Environment.Slug)
	}
	rows := make([][]string, 0, len(svcs))
	for _, s := range svcs {
		note := ""
		if s.Fresh {
			note = "new, empty database"
		}
		rows = append(rows, []string{s.Name, s.Slug, s.Kind, note})
	}
	c.table([]string{"SERVICE", "SLUG", "KIND", "NOTE"}, rows)
	for _, s := range deref(r.Skipped) {
		c.info("skipped %s: %s", s.Slug, s.Reason)
	}
	if doms := deref(r.Domains); len(doms) > 0 {
		c.info("")
		rows = rows[:0]
		for _, d := range doms {
			to := deref(d.To)
			if reason := deref(d.Reason); reason != "" {
				to = strings.TrimSpace(to + " " + reason)
			}
			rows = append(rows, []string{d.Service, d.Hostname, string(d.Action), orDash(to)})
		}
		c.table([]string{"SERVICE", "HOSTNAME", "DOMAIN", "CLONE"}, rows)
	}
	if refs := deref(r.References); len(refs) > 0 {
		c.info("")
		c.info("References that do not resolve in the target environment:")
		for _, f := range refs {
			c.info("  %s %s = ${{%s}}: %s", f.Service, f.Key, f.Ref, f.Message)
		}
	}
	if r.SharedVariables > 0 {
		c.info("%d shared variable(s) copied.", r.SharedVariables)
	}
	if r.Links > 0 {
		c.info("%d link(s) copied.", r.Links)
	}
	for _, w := range deref(r.Warnings) {
		c.info("note: %s", w)
	}
	if !r.DryRun {
		c.info("Staged: deploy the clone to start it.")
	}
}

func (c *cli) printMove(r *apiclient.MoveResult) {
	switch {
	case r.Applied:
		c.info("Moved to %s / %s as %s (staged: deploy to apply).", r.To.Name, r.To.Slug, r.Slug)
	case r.Blocked:
		c.info("Blocked: the move breaks references (use --force to move anyway).")
	default:
		c.info("The move is clean.")
	}
	if r.SlugChanged {
		c.info("The slug becomes %s.", r.Slug)
	}
	for _, f := range deref(r.Outgoing) {
		c.info("  %s %s = ${{%s}}: %s (%s)", f.Service, f.Key, f.Ref, f.Message, f.Status)
	}
	for _, in := range deref(r.Incoming) {
		c.info("  %s references the service in %s", in.Service, strings.Join(deref(in.Keys), ", "))
	}
	if l := deref(r.Links); len(l) > 0 {
		c.info("  links removed: %s", strings.Join(l, ", "))
	}
	for _, w := range deref(r.Warnings) {
		c.info("note: %s", w)
	}
}
