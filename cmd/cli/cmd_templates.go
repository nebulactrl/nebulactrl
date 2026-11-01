package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newTemplatesCmd(c *cli) *cobra.Command {
	return group("templates", "Browse and install one-click templates",
		newTemplatesLsCmd(c), newTemplatesShowCmd(c), newTemplatesPlanCmd(c), newTemplatesInstallCmd(c),
		newTemplatesInstalledCmd(c), newTemplatesUpdateCmd(c), newTemplatesRmCmd(c))
}

// parseInputs turns repeated KEY=VALUE flags into the inputs map.
func parseInputs(kvs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, usagef("--input %q: use KEY=VALUE", kv)
		}
		out[k] = v
	}
	return out, nil
}

// templateTarget resolves --project, --env and --server.
func (c *cli) templateTarget(ctx context.Context, project, env, server string, serverRequired bool) (envID string, serverID *string, err error) {
	if project == "" || env == "" {
		return "", nil, usagef("--project and --env are required")
	}
	p, err := c.resolveProject(ctx, project)
	if err != nil {
		return "", nil, err
	}
	e, err := c.resolveEnv(ctx, p.Id, env)
	if err != nil {
		return "", nil, err
	}
	if server == "" {
		if serverRequired {
			return "", nil, usagef("--server is required")
		}
		return e.Id, nil, nil
	}
	s, err := c.resolveServer(ctx, server)
	if err != nil {
		return "", nil, err
	}
	return e.Id, &s.Id, nil
}

func newTemplatesLsCmd(c *cli) *cobra.Command {
	var category, search, license string
	cmd := &cobra.Command{
		Use: "ls [--category c] [--license class] [--search text]", Short: "List catalog templates", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			params := &apiclient.ListCatalogTemplatesParams{}
			if search != "" {
				params.Q = &search
			}
			if category != "" {
				cat := apiclient.ListCatalogTemplatesParamsCategory(category)
				params.Category = &cat
			}
			if license != "" {
				params.License = &license
			}
			r, err := c.api.ListCatalogTemplatesWithResponse(ctx, params)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			rows := [][]string{}
			for _, t := range itemsOrEmpty(r.JSON200.Items) {
				licence := t.License
				if t.LicenseClass == "source-available" || t.LicenseClass == "proprietary" {
					licence += " (" + t.LicenseClass + ")"
				}
				rows = append(rows, []string{t.Id, t.Category, fmt.Sprint(t.Services), licence, t.Summary})
			}
			c.table([]string{"ID", "CATEGORY", "SERVICES", "LICENCE", "SUMMARY"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "homelab, media, storage, games, devtools, ai, data, web, analytics, automation, productivity or security")
	cmd.Flags().StringVar(&license, "license", "", "licence class: permissive, copyleft, source-available or proprietary")
	cmd.Flags().StringVar(&search, "search", "", "search words (name, summary, tags, licence, database engines)")
	return cmd
}

func newTemplatesShowCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "show <template>", Short: "Show a template: services, inputs and licence", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetCatalogTemplateWithResponse(ctx, args[0])
			if err := check(r, err); err != nil {
				return err
			}
			t := r.JSON200
			if c.asJSON {
				return c.printJSON(t)
			}
			c.info("%s (%s) — %s", t.Name, t.Id, t.Summary)
			c.info("Licence: %s [%s]", t.Upstream.License, t.Upstream.Class)
			rows := [][]string{}
			for _, s := range deref(t.Services) {
				what := deref(s.Image)
				if s.Database != nil {
					what = "managed " + s.Database.Engine + " " + s.Database.Version
				}
				extra := []string{}
				if deref(s.Oneshot) {
					extra = append(extra, "one-shot")
				}
				for _, p := range deref(s.Ports) {
					if p.Publish {
						extra = append(extra, fmt.Sprintf("publishes %s/%d", p.Protocol, p.Port))
					}
				}
				rows = append(rows, []string{s.Key, s.Kind, what, strings.Join(extra, ", ")})
			}
			c.table([]string{"SERVICE", "KIND", "SOURCE", "NOTES"}, rows)
			inputs := [][]string{}
			for _, in := range deref(t.Inputs) {
				def := in.Default
				if in.Secret {
					def = "(secret)"
				}
				inputs = append(inputs, []string{in.Key, yesNo(deref(&in.Required)), def, in.Label})
			}
			if len(inputs) > 0 {
				c.info("")
				c.table([]string{"INPUT", "REQUIRED", "DEFAULT", "LABEL"}, inputs)
			}
			for _, n := range deref(t.Notes) {
				c.info("Note: %s", n)
			}
			return nil
		},
	}
}

func newTemplatesPlanCmd(c *cli) *cobra.Command {
	var project, env, server string
	var inputs []string
	cmd := &cobra.Command{
		Use: "plan <template> --project <p> --env <e> [--server <s>] [--input KEY=VALUE]...", Short: "Preflight an install (changes nothing)", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := parseInputs(inputs)
			if err != nil {
				return err
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			envID, serverID, err := c.templateTarget(ctx, project, env, server, false)
			if err != nil {
				return err
			}
			r, err := c.api.PlanTemplateWithResponse(ctx, envID, args[0], apiclient.TemplatePlanRequest{ServerId: serverID, Inputs: &in})
			if err := check(r, err); err != nil {
				return err
			}
			p := r.JSON200
			if c.asJSON {
				return c.printJSON(p)
			}
			rows := [][]string{}
			for _, s := range deref(p.Services) {
				rows = append(rows, []string{s.Slug, s.Kind, deref(s.Image) + deref(s.Database), deref(s.Domain)})
			}
			c.table([]string{"SERVICE", "KIND", "SOURCE", "DOMAIN"}, rows)
			for _, ch := range deref(p.Checks) {
				c.info("[%s] %s", ch.Status, ch.Message)
			}
			if !p.Installable {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "project ID, slug or name")
	f.StringVar(&env, "env", "", "environment ID, slug or name")
	f.StringVar(&server, "server", "", "server ID or name (enables capacity and port checks)")
	f.StringArrayVar(&inputs, "input", nil, "install input KEY=VALUE (repeatable)")
	return cmd
}

func newTemplatesInstallCmd(c *cli) *cobra.Command {
	var project, env, server, name string
	var inputs []string
	var deploy bool
	cmd := &cobra.Command{
		Use:   "install <template> --project <p> --env <e> --server <s> [--input KEY=VALUE]... [--deploy]",
		Short: "Install a template as staged services (or deploy right away)", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := parseInputs(inputs)
			if err != nil {
				return err
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			envID, serverID, err := c.templateTarget(ctx, project, env, server, true)
			if err != nil {
				return err
			}
			body := apiclient.TemplateInstallRequest{ServerId: *serverID, Inputs: &in}
			if deploy {
				body.Deploy = &deploy
			}
			if name != "" {
				body.Name = &name
			}
			r, err := c.api.InstallTemplateWithResponse(ctx, envID, args[0], &apiclient.InstallTemplateParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			res := r.JSON201
			if c.asJSON {
				return c.printJSON(res)
			}
			slugs := []string{}
			for _, s := range deref(res.Services) {
				slugs = append(slugs, s.Slug)
			}
			sort.Strings(slugs)
			c.info("Installed %s as %s (%d services: %s)", args[0], res.Instance.Id, len(slugs), strings.Join(slugs, ", "))
			if deploy {
				c.info("Started %d deployments; follow them with: nebulactrl deployments status <id>", len(deref(res.Deployments)))
			} else {
				c.info("The services are staged; deploy each with `nebulactrl services deploy <service> --wait`.")
			}
			for _, w := range deref(res.Warnings) {
				c.note("Warning: %s", w)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "project ID, slug or name")
	f.StringVar(&env, "env", "", "environment ID, slug or name")
	f.StringVar(&server, "server", "", "server ID or name the stack runs on")
	f.StringVar(&name, "name", "", "canvas group title")
	f.StringArrayVar(&inputs, "input", nil, "install input KEY=VALUE (repeatable)")
	f.BoolVar(&deploy, "deploy", false, "deploy the new services right away")
	return cmd
}

func newTemplatesInstalledCmd(c *cli) *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use: "installed --project <p> --env <e>", Short: "List the template stacks of an environment", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			envID, _, err := c.templateTarget(ctx, project, env, "", false)
			if err != nil {
				return err
			}
			r, err := c.api.ListTemplateInstancesWithResponse(ctx, envID)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			rows := [][]string{}
			for _, i := range itemsOrEmpty(r.JSON200) {
				update := ""
				switch {
				case i.UpdateAvailable:
					update = fmt.Sprintf("rev %d available", i.CurrentRevision)
				case deref(i.UpdateBlocked) != "":
					update = "newer revision (reinstall)"
				}
				rows = append(rows, []string{i.Id, i.TemplateId, i.TemplateVersion, fmt.Sprint(i.TemplateRevision), update, fmt.Sprint(len(deref(i.Services))), ts(i.CreatedAt)})
			}
			c.table([]string{"ID", "TEMPLATE", "VERSION", "REV", "UPDATE", "SERVICES", "CREATED"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project ID, slug or name")
	cmd.Flags().StringVar(&env, "env", "", "environment ID, slug or name")
	return cmd
}

func newTemplatesUpdateCmd(c *cli) *cobra.Command {
	var inputs []string
	var apply bool
	cmd := &cobra.Command{
		Use:   "update <instance-id> [--apply] [--input KEY=VALUE]...",
		Short: "Show what a newer template revision changes, or stage it (--apply)", Args: exactArgs(1),
		Long: "Without --apply this prints the diff between the template as installed and the catalog's current revision and changes nothing.\n" +
			"With --apply the changes marked 'apply' are staged as ordinary edits (nothing is deployed). Fields you changed are kept; " +
			"databases, routes and services are never touched.",
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := parseInputs(inputs)
			if err != nil {
				return err
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetTemplateUpdateWithResponse(ctx, args[0])
			if err := check(r, err); err != nil {
				return err
			}
			u := r.JSON200
			if !apply {
				if c.asJSON {
					return c.printJSON(u)
				}
				printTemplateUpdate(c, u)
				return nil
			}
			if deref(u.Blocked) != "" {
				return usagef("%s", deref(u.Blocked))
			}
			ar, err := c.api.ApplyTemplateUpdateWithResponse(ctx, args[0], &apiclient.ApplyTemplateUpdateParams{IdempotencyKey: c.key()},
				apiclient.TemplateUpdateRequest{Revision: u.CurrentRevision, Inputs: &in})
			if err := check(ar, err); err != nil {
				return err
			}
			res := ar.JSON200
			if c.asJSON {
				return c.printJSON(res)
			}
			c.info("Staged %d change(s) in %s. Review them in the staged bar, then deploy.", res.Staged, strings.Join(deref(res.Services), ", "))
			for _, w := range deref(res.Warnings) {
				c.note("Warning: %s", w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "stage the changes (default: only show them)")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "value for an input the new revision added, KEY=VALUE (repeatable)")
	return cmd
}

func printTemplateUpdate(c *cli, u *apiclient.TemplateUpdate) {
	if b := deref(u.Blocked); b != "" {
		c.info("%s: %s", u.TemplateId, b)
		return
	}
	c.info("%s: revision %d (%s) -> %d (%s)", u.TemplateId, u.InstalledRevision, u.InstalledVersion, u.CurrentRevision, u.CurrentVersion)
	if !u.Available {
		c.info("Up to date.")
		return
	}
	rows := [][]string{}
	for _, ch := range deref(u.Changes) {
		what := ch.Field
		if ch.Service != "" {
			what = ch.Service + " " + ch.Field
		}
		change := ch.From + " -> " + ch.To
		if ch.Secret {
			change = "(secret)"
		}
		rows = append(rows, []string{string(ch.Status), what, change, deref(ch.Reason)})
	}
	c.table([]string{"STATUS", "WHAT", "CHANGE", "NOTE"}, rows)
	c.info("%d to stage, %d kept, %d manual, %d already applied.", u.Apply, u.Kept, u.Manual, u.Applied)
}

func newTemplatesRmCmd(c *cli) *cobra.Command {
	var confirm string
	var volumes bool
	cmd := &cobra.Command{
		Use: "rm <instance-id> --confirm <template-id> [--delete-volumes]", Short: "Delete a template stack", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if confirm == "" {
				return usagef("--confirm <template-id> is required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.DeleteTemplateInstanceWithResponse(ctx, args[0], &apiclient.DeleteTemplateInstanceParams{Confirm: &confirm, DeleteVolumes: &volumes})
			if err := check(r, err); err != nil {
				return err
			}
			c.info("Deleted stack %s", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the template id, to confirm")
	cmd.Flags().BoolVar(&volumes, "delete-volumes", false, "also remove the stack's Docker volumes (data is lost)")
	return cmd
}
