package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

const secretMask = "••••••"

func newVarsCmd(c *cli) *cobra.Command {
	return group("vars", "Manage service environment variables",
		newVarsLsCmd(c), newVarsSetCmd(c), newVarsRmCmd(c), newVarsRotateCmd(c), newVarsHistoryCmd(c), newVarsSharedCmd(c))
}

func newVarsLsCmd(c *cli) *cobra.Command {
	var platform, exports bool
	cmd := &cobra.Command{
		Use: "ls <service>", Short: "List variables (secret values are never shown)", Args: exactArgs(1),
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
			r, err := c.api.ListVariablesWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(map[string]any{
					"items":    itemsOrEmpty(r.JSON200.Items),
					"platform": itemsOrEmpty(r.JSON200.Platform),
					"exports":  itemsOrEmpty(r.JSON200.Exports),
				})
			}
			rows := [][]string{}
			for _, v := range itemsOrEmpty(r.JSON200.Items) {
				val := deref(v.Value)
				switch {
				case v.IsSecret:
					val = secretMask
				case v.ComputedSecret:
					val = secretMask + " (derived)"
				}
				rows = append(rows, []string{v.Key, val, yesNo(v.IsSecret), string(v.Source), string(v.Scope)})
			}
			c.table([]string{"KEY", "VALUE", "SECRET", "SOURCE", "SCOPE"}, rows)
			if platform {
				c.info("")
				prow := [][]string{}
				for _, v := range itemsOrEmpty(r.JSON200.Platform) {
					prow = append(prow, []string{v.Key, orDash(v.Value), v.Description})
				}
				c.table([]string{"PLATFORM KEY", "VALUE", "DESCRIPTION"}, prow)
			}
			if exports {
				c.info("")
				erows := [][]string{}
				for _, e := range itemsOrEmpty(r.JSON200.Exports) {
					erows = append(erows, []string{e.Key, e.Description, yesNo(e.Secret)})
				}
				c.table([]string{"EXPORT (reference as ${{slug.KEY}})", "DESCRIPTION", "SECRET"}, erows)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&platform, "platform", false, "also list the platform-provided variables")
	cmd.Flags().BoolVar(&exports, "exports", false, "also list the virtual variables other services can reference")
	return cmd
}

func newVarsSetCmd(c *cli) *cobra.Command {
	var secret bool
	var scope, comment string
	cmd := &cobra.Command{
		Use: "set <service> KEY=VALUE... [--secret] [--scope build]", Short: "Set variables (staged until the next deploy)", Args: minArgs(2),
		Long: "Upserts variables on a service. Changes are staged: run `services deploy` to apply them.\n" +
			"Values are never echoed. Use KEY=- to read a value from stdin, which keeps secrets out of the\n" +
			"shell history and process list. A variable that is already secret stays secret.\n\n" +
			"A value may contain generators that run once, on the server: KEY='${{secret(32)}}' stores a random\n" +
			"secret. Running the same command again keeps the generated value (use `vars rotate` to replace it).\n" +
			"Other forms: ${{service-slug.KEY}}, ${{KEY}}, ${{shared.KEY}}; write $${{ for a literal ${{.\n" +
			"--scope build passes the variable to image builds of git services (--build-arg, or a BuildKit secret\n" +
			"when it is secret); --scope both also keeps it in the container.",
		RunE: func(cmd *cobra.Command, args []string) error {
			inputs, err := c.parseAssignments(args[1:])
			if err != nil {
				return err
			}
			for i := range inputs {
				if cmd.Flags().Changed("secret") {
					t := secret
					inputs[i].IsSecret = &t
				}
				if scope != "" {
					sc := apiclient.VariableInputScope(scope)
					inputs[i].Scope = &sc
				}
				if cmd.Flags().Changed("comment") {
					cm := comment
					inputs[i].Comment = &cm
				}
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
			// A variable that is already secret stays secret when --secret is not given.
			if !cmd.Flags().Changed("secret") {
				cur, err := c.api.ListVariablesWithResponse(ctx, id)
				if err := check(cur, err); err != nil {
					return err
				}
				for _, v := range itemsOrEmpty(cur.JSON200.Items) {
					if !v.IsSecret {
						continue
					}
					for _, in := range inputs {
						if in.Key == v.Key {
							c.note("%s is already a secret and stays one", v.Key)
						}
					}
				}
			}
			r, err := c.api.PutVariablesWithResponse(ctx, id, apiclient.PutVariablesJSONRequestBody{Variables: &inputs})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(map[string]any{
					"items":    itemsOrEmpty(r.JSON200.Items),
					"platform": itemsOrEmpty(r.JSON200.Platform),
					"warnings": itemsOrEmpty(r.JSON200.Warnings),
					"restaged": itemsOrEmpty(r.JSON200.Restaged),
				})
			}
			c.info("Set %s", strings.Join(varLabels(inputs, r.JSON200.Items), ", "))
			for _, w := range itemsOrEmpty(r.JSON200.Warnings) {
				c.note("warning: %s", w.Message)
			}
			for _, s := range itemsOrEmpty(r.JSON200.Restaged) {
				c.note("Also staged: %s references a changed variable.", s.Slug)
			}
			c.note("Staged: run `nebulactrl services deploy %s` to apply.", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&secret, "secret", false, "mark the variables as secret (write-only)")
	cmd.Flags().StringVar(&scope, "scope", "", "where the variable is injected: runtime (default), build or both")
	cmd.Flags().StringVar(&comment, "comment", "", "a note stored with the variable")
	return cmd
}

// varLabels renders the written keys, marking the ones that are secret.
func varLabels(inputs []apiclient.VariableInput, items *[]apiclient.Variable) []string {
	secret := map[string]bool{}
	for _, v := range itemsOrEmpty(items) {
		secret[v.Key] = v.IsSecret
	}
	labels := make([]string, 0, len(inputs))
	for _, in := range inputs {
		l := in.Key
		if secret[in.Key] {
			l += " (secret)"
		}
		labels = append(labels, l)
	}
	return labels
}

// parseAssignments turns KEY=VALUE arguments into variable inputs. KEY=- reads
// the value from stdin (once).
func (c *cli) parseAssignments(args []string) ([]apiclient.VariableInput, error) {
	out := make([]apiclient.VariableInput, 0, len(args))
	seen := map[string]bool{}
	stdinUsed := false
	for _, a := range args {
		key, val, ok := strings.Cut(a, "=")
		if !ok || key == "" {
			return nil, usagef("expected KEY=VALUE arguments (values are never echoed back)")
		}
		if seen[key] {
			return nil, usagef("variable %s given twice", key)
		}
		seen[key] = true
		if val == "-" {
			if stdinUsed {
				return nil, usagef("only one KEY=- can read from stdin")
			}
			stdinUsed = true
			raw, err := io.ReadAll(c.in)
			if err != nil {
				return nil, fmt.Errorf("read value of %s from stdin: %w", key, err)
			}
			val = strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		}
		v := val
		out = append(out, apiclient.VariableInput{Key: key, Value: &v})
	}
	return out, nil
}

func newVarsRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <service> KEY...", Short: "Delete variables (staged until the next deploy)", Args: minArgs(2),
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
			for _, key := range args[1:] {
				r, err := c.api.DeleteVariableWithResponse(ctx, id, key)
				if err := check(r, err); err != nil {
					return fmt.Errorf("delete %s: %w", key, err)
				}
			}
			if c.asJSON {
				return c.printJSON(map[string]any{"deleted": args[1:]})
			}
			c.info("Deleted %s", strings.Join(args[1:], ", "))
			c.note("Staged: run `nebulactrl services deploy %s` to apply.", args[0])
			return nil
		},
	}
}

func newVarsRotateCmd(c *cli) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use: "rotate <service> KEY [--dry-run]", Short: "Replace a generated variable's value (staged, never deployed)", Args: exactArgs(2),
		Long: "Re-runs the generators of a variable created with ${{secret(..)}} and similar. --dry-run prints the plan only:\n" +
			"which services consume the value, in which order to deploy them, and any blockers. Without it the new\n" +
			"value is staged on this service and on every consumer; nothing is deployed. Database credentials\n" +
			"cannot be rotated this way once deployed.",
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
			r, err := c.api.RotateVariableWithResponse(ctx, id, args[1], apiclient.RotateVariableJSONRequestBody{DryRun: dryRun})
			if err := check(r, err); err != nil {
				return err
			}
			return c.printPlan(r.JSON200, dryRun, args[0])
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan without staging anything")
	return cmd
}

func (c *cli) printPlan(p *apiclient.VariableRotationPlan, dryRun bool, ref string) error {
	if c.asJSON {
		return c.printJSON(p)
	}
	rows := [][]string{}
	for _, a := range itemsOrEmpty(p.Affected) {
		rows = append(rows, []string{a.Slug, strings.Join(itemsOrEmpty(a.Via), ", "), string(a.Strategy), string(a.Downtime)})
	}
	c.table([]string{"AFFECTED", "KEYS", "STRATEGY", "DOWNTIME"}, rows)
	if order := itemsOrEmpty(p.DeployOrder); len(order) > 0 {
		c.info("Deploy order: %s", strings.Join(order, " → "))
	}
	for _, w := range itemsOrEmpty(p.Warnings) {
		c.note("warning: %s", w)
	}
	for _, b := range itemsOrEmpty(p.Blockers) {
		c.note("blocked: %s", b.Message)
	}
	switch {
	case p.Staged:
		c.info("Rotated %s (staged). Run `nebulactrl services deploy` for each service in the order above.", p.Key)
	case dryRun:
		c.note("Dry run: nothing changed.")
	}
	return nil
}

func newVarsHistoryCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "history <service> KEY", Short: "Show a variable's versions (secrets show a fingerprint only)", Args: exactArgs(2),
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
			r, err := c.api.GetVariableHistoryWithResponse(ctx, id, args[1])
			if err := check(r, err); err != nil {
				return err
			}
			items := itemsOrEmpty(r.JSON200.Items)
			if c.asJSON {
				return c.printJSON(map[string]any{"items": items})
			}
			rows := [][]string{}
			for _, h := range items {
				val := deref(h.Value)
				if h.IsSecret {
					val = secretMask
				}
				rows = append(rows, []string{fmt.Sprint(h.Version), string(h.Op), val, h.Fingerprint, string(h.Source), h.ActorType, h.CreatedAt.Format("2006-01-02 15:04")})
			}
			c.table([]string{"VERSION", "OP", "VALUE", "FINGERPRINT", "SOURCE", "BY", "AT"}, rows)
			return nil
		},
	}
}

// sharedRef resolves `<project>`, `<project>/<env>` or `@team` to a shared-variable owner.
type sharedRef struct {
	team    bool
	project bool
	id      string
}

// teamRef names the team layer: its variables apply to every project and are
// referenced as ${{team.KEY}}.
const teamRef = "@team"

func (c *cli) resolveShared(cmd *cobra.Command, ref string) (sharedRef, error) {
	if ref == teamRef {
		return sharedRef{team: true}, nil
	}
	ctx, cancel := c.ctx(cmd.Context())
	defer cancel()
	proj, envRef, hasEnv := strings.Cut(ref, "/")
	p, err := c.resolveProject(ctx, proj)
	if err != nil {
		return sharedRef{}, err
	}
	if !hasEnv {
		return sharedRef{project: true, id: p.Id}, nil
	}
	e, err := c.resolveEnv(ctx, p.Id, envRef)
	if err != nil {
		return sharedRef{}, err
	}
	return sharedRef{id: e.Id}, nil
}

func newVarsSharedCmd(c *cli) *cobra.Command {
	return group("shared", "Manage shared variables of the team (@team), a project (<project>) or an environment (<project>/<env>)",
		newSharedLsCmd(c), newSharedSetCmd(c), newSharedRmCmd(c), newSharedRotateCmd(c), newSharedShareCmd(c))
}

func newSharedLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <@team|project[/env]>", Short: "List shared variables", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ref, err := c.resolveShared(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			var list *apiclient.SharedVariableList
			if ref.team {
				r, err := c.api.ListTeamSharedVariablesWithResponse(ctx)
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			} else if ref.project {
				r, err := c.api.ListProjectSharedVariablesWithResponse(ctx, ref.id)
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			} else {
				r, err := c.api.ListEnvironmentSharedVariablesWithResponse(ctx, ref.id)
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			}
			return c.printShared(list)
		},
	}
}

func (c *cli) printShared(l *apiclient.SharedVariableList) error {
	if c.asJSON {
		return c.printJSON(map[string]any{"items": itemsOrEmpty(l.Items), "warnings": itemsOrEmpty(l.Warnings), "restaged": itemsOrEmpty(l.Restaged)})
	}
	rows := [][]string{}
	for _, v := range itemsOrEmpty(l.Items) {
		val := deref(v.Value)
		if v.IsSecret {
			val = secretMask
		} else if v.ComputedSecret {
			val = secretMask + " (derived)"
		}
		layer := string(v.Layer)
		if v.Inherited {
			layer += " (inherited)"
		} else if v.Overrides {
			layer += " (overrides project)"
		}
		rows = append(rows, []string{v.Key, val, layer, fmt.Sprint(v.UsedBy)})
	}
	c.table([]string{"KEY", "VALUE", "LAYER", "USED BY"}, rows)
	for _, w := range itemsOrEmpty(l.Warnings) {
		c.note("warning: %s", w.Message)
	}
	for _, s := range itemsOrEmpty(l.Restaged) {
		c.note("Also staged: %s resolves differently now.", s.Slug)
	}
	return nil
}

func newSharedSetCmd(c *cli) *cobra.Command {
	var secret bool
	cmd := &cobra.Command{
		Use: "set <@team|project[/env]> KEY=VALUE... [--secret]", Short: "Set shared variables (restages the services that use them)", Args: minArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vars, err := c.parseAssignments(args[1:])
			if err != nil {
				return err
			}
			inputs := make([]apiclient.SharedVariableInput, len(vars))
			for i, v := range vars {
				inputs[i] = apiclient.SharedVariableInput{Key: v.Key, Value: v.Value}
				if cmd.Flags().Changed("secret") {
					t := secret
					inputs[i].IsSecret = &t
				}
			}
			if err := c.connect(); err != nil {
				return err
			}
			ref, err := c.resolveShared(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			var list *apiclient.SharedVariableList
			if ref.team {
				r, err := c.api.PutTeamSharedVariablesWithResponse(ctx, apiclient.PutTeamSharedVariablesJSONRequestBody{Variables: &inputs})
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			} else if ref.project {
				r, err := c.api.PutProjectSharedVariablesWithResponse(ctx, ref.id, apiclient.PutProjectSharedVariablesJSONRequestBody{Variables: &inputs})
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			} else {
				r, err := c.api.PutEnvironmentSharedVariablesWithResponse(ctx, ref.id, apiclient.PutEnvironmentSharedVariablesJSONRequestBody{Variables: &inputs})
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			}
			return c.printShared(list)
		},
	}
	cmd.Flags().BoolVar(&secret, "secret", false, "mark the variables as secret (write-only)")
	return cmd
}

func newSharedRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <@team|project[/env]> KEY...", Short: "Delete shared variables", Args: minArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ref, err := c.resolveShared(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			for _, key := range args[1:] {
				if ref.team {
					r, err := c.api.DeleteTeamSharedVariableWithResponse(ctx, key)
					if err := check(r, err); err != nil {
						return fmt.Errorf("delete %s: %w", key, err)
					}
				} else if ref.project {
					r, err := c.api.DeleteProjectSharedVariableWithResponse(ctx, ref.id, key)
					if err := check(r, err); err != nil {
						return fmt.Errorf("delete %s: %w", key, err)
					}
				} else {
					r, err := c.api.DeleteEnvironmentSharedVariableWithResponse(ctx, ref.id, key)
					if err := check(r, err); err != nil {
						return fmt.Errorf("delete %s: %w", key, err)
					}
				}
			}
			if c.asJSON {
				return c.printJSON(map[string]any{"deleted": args[1:]})
			}
			c.info("Deleted %s", strings.Join(args[1:], ", "))
			return nil
		},
	}
}

func newSharedRotateCmd(c *cli) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use: "rotate <@team|project[/env]> KEY [--dry-run]", Short: "Replace a generated shared variable (staged, never deployed)", Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ref, err := c.resolveShared(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			var plan *apiclient.VariableRotationPlan
			if ref.team {
				r, err := c.api.RotateTeamSharedVariableWithResponse(ctx, args[1], apiclient.RotateTeamSharedVariableJSONRequestBody{DryRun: dryRun})
				if err := check(r, err); err != nil {
					return err
				}
				plan = r.JSON200
			} else if ref.project {
				r, err := c.api.RotateProjectSharedVariableWithResponse(ctx, ref.id, args[1], apiclient.RotateProjectSharedVariableJSONRequestBody{DryRun: dryRun})
				if err := check(r, err); err != nil {
					return err
				}
				plan = r.JSON200
			} else {
				r, err := c.api.RotateEnvironmentSharedVariableWithResponse(ctx, ref.id, args[1], apiclient.RotateEnvironmentSharedVariableJSONRequestBody{DryRun: dryRun})
				if err := check(r, err); err != nil {
					return err
				}
				plan = r.JSON200
			}
			return c.printPlan(plan, dryRun, args[0])
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan without staging anything")
	return cmd
}

func newSharedShareCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "share <@team|project[/env]> KEY <service>...", Short: "Stage KEY=${{shared.KEY}} on services", Args: minArgs(3),
		Long: "A shared variable reaches a service only through an explicit reference row. This stages\n" +
			"KEY=${{shared.KEY}} on each service (services given as ID or project/env/slug).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ref, err := c.resolveShared(cmd, args[0])
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			ids := make([]string, 0, len(args)-2)
			for _, s := range args[2:] {
				id, err := c.serviceID(ctx, s)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			var list *apiclient.SharedVariableList
			if ref.team {
				r, err := c.api.ShareTeamSharedVariableWithResponse(ctx, args[1], apiclient.ShareTeamSharedVariableJSONRequestBody{ServiceIds: &ids})
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			} else if ref.project {
				r, err := c.api.ShareProjectSharedVariableWithResponse(ctx, ref.id, args[1], apiclient.ShareProjectSharedVariableJSONRequestBody{ServiceIds: &ids})
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			} else {
				r, err := c.api.ShareEnvironmentSharedVariableWithResponse(ctx, ref.id, args[1], apiclient.ShareEnvironmentSharedVariableJSONRequestBody{ServiceIds: &ids})
				if err := check(r, err); err != nil {
					return err
				}
				list = r.JSON200
			}
			if !c.asJSON {
				ns := "shared"
				if ref.team {
					ns = "team"
				}
				c.info("Staged %s=${{%s.%s}} on %d service(s).", args[1], ns, args[1], len(ids))
			}
			return c.printShared(list)
		},
	}
}
