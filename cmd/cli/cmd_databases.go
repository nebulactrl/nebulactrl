package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newDatabasesCmd(c *cli) *cobra.Command {
	return group("databases", "Manage managed databases",
		newDatabasesEnginesCmd(c), newDatabasesCreateCmd(c), newDatabasesInfoCmd(c), newDatabasesRevealCmd(c),
		newDatabasesRotateCmd(c), newDatabasesUpgradeCmd(c),
		newDatabasesReplicasCmd(c), newDatabasesPromoteCmd(c), newDatabasesRebuildCmd(c), newDatabasesRestoreCmd(c))
}

func items[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}

func newDatabasesEnginesCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "engines", Short: "List the database engines and versions", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.ListDatabaseEnginesWithResponse(ctx)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			var rows [][]string
			for _, e := range items(r.JSON200.Engines) {
				var vs []string
				for _, v := range items(e.Versions) {
					s := v.Id
					if v.Default {
						s += "*"
					}
					vs = append(vs, s)
				}
				licence := e.License
				if e.OptIn || e.LicenseClass == apiclient.SourceAvailable {
					licence += " (source-available: --accept-license)"
				}
				rows = append(rows, []string{e.Id, e.Label, strings.Join(vs, ", "), licence})
			}
			c.table([]string{"ENGINE", "NAME", "VERSIONS (*default)", "LICENCE"}, rows)
			return nil
		},
	}
}

func newDatabasesCreateCmd(c *cli) *cobra.Command {
	var project, env, name, engine, version, flavor, dbName, server string
	var accept bool
	cmd := &cobra.Command{
		Use:   "create --project <project> --env <env> --engine <engine> [--version <v>] [--name <name>] [--flavor postgis] [--database <db>] [--server <server>] [--accept-license]",
		Short: "Create a managed database (staged until deployed)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || env == "" || engine == "" {
				return usagef("--project, --env and --engine are required (see: nebulactrl databases engines)")
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
			e, err := c.resolveEnv(ctx, p.Id, env)
			if err != nil {
				return err
			}
			in := apiclient.CreateDatabaseInputBody{Engine: apiclient.CreateDatabaseInputBodyEngine(strings.ToLower(engine))}
			if name != "" {
				in.Name = &name
			}
			if version != "" {
				in.Version = &version
			}
			if flavor != "" {
				f := apiclient.CreateDatabaseInputBodyFlavor(flavor)
				in.Flavor = &f
			}
			if dbName != "" {
				in.DatabaseName = &dbName
			}
			if accept {
				in.AcceptLicense = &accept
			}
			if server != "" {
				s, err := c.resolveServer(ctx, server)
				if err != nil {
					return err
				}
				in.ServerId = &s.Id
			}
			r, err := c.api.CreateDatabaseWithResponse(ctx, e.Id, &apiclient.CreateDatabaseParams{IdempotencyKey: c.key()}, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Created database %s/%s/%s (%s)", p.Slug, e.Slug, r.JSON201.Slug, r.JSON201.Id)
			c.info("Deploy it with: nebulactrl services deploy %s/%s/%s --wait", p.Slug, e.Slug, r.JSON201.Slug)
			c.info("Connect another service with: nebulactrl vars set DATABASE_URL='${{%s.DATABASE_URL}}' %s/%s/<service>", r.JSON201.Slug, p.Slug, e.Slug)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "project ID, slug or name")
	f.StringVar(&env, "env", "", "environment ID, slug or name")
	f.StringVar(&name, "name", "", "display name (default: the engine name)")
	f.StringVar(&engine, "engine", "", "postgres, mysql, mariadb, valkey, redis, mongodb or clickhouse")
	f.StringVar(&version, "version", "", "major version (default: the engine's default)")
	f.StringVar(&flavor, "flavor", "", "image variant, e.g. postgis")
	f.StringVar(&dbName, "database", "", "database name (default: app)")
	f.StringVar(&server, "server", "", "target server ID or name")
	f.BoolVar(&accept, "accept-license", false, "acknowledge the licence of a source-available engine (MongoDB, Redis)")
	return cmd
}

func newDatabasesInfoCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "info <service>", Short: "Show a database's engine, exports and last reported state", Args: exactArgs(1),
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
			r, err := c.api.GetDatabaseWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			d := r.JSON200
			c.info("%s %s (%s)", d.Engine.Label, d.Spec.Version, d.Image)
			if d.Info != nil {
				c.info("size %d bytes, %d connection(s), reported %s", d.Info.SizeBytes, d.Info.Connections, ts(d.Info.UpdatedAt))
			}
			var rows [][]string
			for _, e := range items(d.Exports) {
				rows = append(rows, []string{e.Key, e.Reference, fmt.Sprint(e.Secret)})
			}
			c.table([]string{"EXPORT", "REFERENCE", "SECRET"}, rows)
			if d.Rotation != nil {
				c.info("last rotation: %s", d.Rotation.Status)
			}
			if d.Upgrade != nil {
				c.info("last upgrade: %s → %s (%s)", d.Upgrade.FromVersion, d.Upgrade.ToVersion, d.Upgrade.Status)
			}
			return nil
		},
	}
}

func newDatabasesRevealCmd(c *cli) *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use: "reveal <service> [--key DATABASE_URL]", Short: "Print a connection string (audited as secret.reveal)", Args: exactArgs(1),
		Long: "Prints one derived connection variable in plain text to stdout. The action is recorded in the audit log and needs the deployments:write scope.",
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
			if key == "" {
				d, err := c.api.GetDatabaseWithResponse(ctx, id)
				if err := check(d, err); err != nil {
					return err
				}
				for _, e := range items(d.JSON200.Exports) {
					if e.Primary {
						key = e.Key
					}
				}
				if key == "" {
					return usagef("--key is required")
				}
			}
			r, err := c.api.RevealDatabaseExportWithResponse(ctx, id, apiclient.RevealInputBody{Key: key})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			_, err = fmt.Fprintln(c.out, r.JSON200.Value)
			return err
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "export to reveal (default: the connection URL)")
	return cmd
}

func newDatabasesRotateCmd(c *cli) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use: "rotate <service> [--dry-run]", Short: "Rotate the database password and stage its consumers", Args: exactArgs(1),
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
			r, err := c.api.RotateDatabaseCredentialsWithResponse(ctx, id, apiclient.RotateInputBody{DryRun: &dryRun})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			res := r.JSON200
			c.info("mode: %s (password variable %s)", res.Plan.Mode, res.Plan.PasswordKey)
			for _, s := range items(res.Plan.Consumers) {
				c.info("consumer: %s", s.Slug)
			}
			for _, w := range items(res.Plan.Warnings) {
				c.note("warning: %s", w)
			}
			switch res.Status {
			case apiclient.RotationResultStatusPlanned:
				c.info("Dry run only; nothing changed.")
			case apiclient.RotationResultStatusPending:
				c.info("Rotation requested; the agent is changing the password. Then run: nebulactrl databases info %s", args[0])
			default:
				c.info("Rotation applied. Deploy the staged changes to roll the new password out.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only print the plan")
	return cmd
}

func newDatabasesUpgradeCmd(c *cli) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "upgrade <service> <version> [--yes]", Short: "Plan (default) or run a database version upgrade", Args: exactArgs(2),
		Long: "Prints the upgrade plan. With --yes it runs the upgrade: in-place engines stage the new image; PostgreSQL majors are converted with pg_upgrade into a new volume and then staged. Take a backup first.",
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
			pr, err := c.api.PlanDatabaseUpgradeWithResponse(ctx, id, apiclient.UpgradePlanInputBody{TargetVersion: args[1]})
			if err := check(pr, err); err != nil {
				return err
			}
			plan := pr.JSON200
			if !yes {
				if c.asJSON {
					return c.printJSON(plan)
				}
				c.info("%s %s → %s: %s (%s)", plan.Engine, plan.From, plan.To, plan.Method, plan.Downtime)
				for _, s := range items(plan.Steps) {
					c.info("  - %s", s)
				}
				for _, w := range items(plan.Warnings) {
					c.note("warning: %s", w)
				}
				for _, b := range items(plan.Blockers) {
					c.note("blocked: %s", b)
				}
				c.info("Run again with --yes to apply.")
				return nil
			}
			svc, err := c.api.GetServiceWithResponse(ctx, id)
			if err := check(svc, err); err != nil {
				return err
			}
			r, err := c.api.UpgradeDatabaseWithResponse(ctx, id, apiclient.UpgradeInputBody{TargetVersion: args[1], Confirm: svc.JSON200.Slug})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			if r.JSON200.Status == apiclient.UpgradeResultStatusPending {
				c.info("Upgrade started: the database is stopped while its data is converted. When it finishes the new version is staged; deploy it.")
			} else {
				c.info("Upgrade staged. Deploy the database to apply it.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "apply the upgrade")
	return cmd
}
