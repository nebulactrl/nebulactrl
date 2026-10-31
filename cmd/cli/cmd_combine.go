package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// `services combine` merges several git services of one environment into one
// service with a process per former service. The plan is read-only; only
// --yes starts it, and every conflicting variable then needs a --var choice.

func newServicesCombineCmd(c *cli) *cobra.Command {
	var into string
	var procNames, vars []string
	var stopFirst, yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "combine <service>... [--into <target>] [--process-name <service>=<name>]... [--var KEY=<service>]... [--stop-sources-first] [--yes] [--wait]",
		Short: "Merge several services into one service with a process each",
		Long: "Merges git services of one environment that share a repository and build settings into one service. The target (--into;\n" +
			"default: the service with the most domains) keeps running and gains one process per other service, with that service's\n" +
			"start command, port, health check, limits and replicas. They share the target's one build, image, variables and release.\n\n" +
			"Without --yes this only prints the plan: what blocks the merge, which variables differ, which domains and tasks move.\n" +
			"With --yes it stages the target, deploys it once (all processes, all or nothing) and, only when that succeeded, moves the\n" +
			"other services' domains and scheduled tasks to the target and deletes those services (their volumes are kept). If the\n" +
			"deployment fails nothing was deleted: retry or abort with `services combine retry|abort <combine-id>`. After the merge\n" +
			"is done there is no way back.\n\n" +
			"A variable that the services set to different values must be chosen: --var KEY=<service> keeps that service's value.\n" +
			"Every other service becomes a process named after its slug (--process-name <service>=<name> changes it; naming the\n" +
			"target renames its own process, default web). --stop-sources-first stops services without domains before the deployment,\n" +
			"so a worker or scheduler never runs twice at the same time.",
		Example: "  nebulactrl services combine acme/production/worker acme/production/scheduler --into acme/production/web\n" +
			"  nebulactrl services combine worker --into web --var APP_ENV=web --process-name worker=queue --yes --wait",
		Args: minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.combine(cmd, combineOpts{
				services: args, into: into, processNames: procNames, vars: vars,
				stopFirst: stopFirst, yes: yes, wait: wait, timeout: timeout,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&into, "into", "", "the service that keeps running and gains the others as processes")
	f.StringArrayVar(&procNames, "process-name", nil, "process name of a service: <service>=<name> (the target's own process too)")
	f.StringArrayVar(&vars, "var", nil, "for a variable that differs between the services: KEY=<service> keeps that service's value")
	f.BoolVar(&stopFirst, "stop-sources-first", false, "stop the services without domains before the deployment")
	f.BoolVar(&yes, "yes", false, "start the merge (without it the plan is only printed)")
	f.BoolVar(&wait, "wait", false, "wait until the merge is done; exit non-zero if it failed")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (with --wait)")
	cmd.AddCommand(newCombineStatusCmd(c), newCombineAbortCmd(c), newCombineRetryCmd(c))
	return cmd
}

type combineOpts struct {
	services, processNames, vars []string
	into                         string
	stopFirst, yes, wait         bool
	timeout                      time.Duration
}

// splitPair splits "left=right" for --process-name and --var.
func splitPair(flag, v string) (string, string, error) {
	l, r, ok := strings.Cut(v, "=")
	l, r = strings.TrimSpace(l), strings.TrimSpace(r)
	if !ok || l == "" || r == "" {
		return "", "", usagef("--%s %q: use the form <left>=<right>", flag, v)
	}
	return l, r, nil
}

func (c *cli) combine(cmd *cobra.Command, o combineOpts) error {
	if err := c.connect(); err != nil {
		return err
	}
	ctx, cancel := c.ctx(cmd.Context())
	defer cancel()

	var ids []string
	add := func(ref string) (string, error) {
		id, err := c.serviceID(ctx, ref)
		if err != nil {
			return "", err
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
		return id, nil
	}
	for _, s := range o.services {
		if _, err := add(s); err != nil {
			return err
		}
	}
	var targetID string
	if o.into != "" {
		var err error
		if targetID, err = add(o.into); err != nil {
			return err
		}
	}
	if len(ids) < 2 {
		return usagef("give at least two services: the ones to merge and --into <target>")
	}
	first, err := c.api.GetServiceWithResponse(ctx, ids[0])
	if err := check(first, err); err != nil {
		return err
	}
	envID := first.JSON200.EnvironmentId

	req := apiclient.CombinePlanRequest{ServiceIds: &ids}
	if targetID != "" {
		req.TargetServiceId = &targetID
	}
	pr, err := c.api.PlanCombineServicesWithResponse(ctx, envID, req)
	if err := check(pr, err); err != nil {
		return err
	}
	plan := pr.JSON200
	members := items(plan.Members)
	byID := make(map[string]apiclient.Member, len(members))
	for _, m := range members {
		byID[m.ServiceId] = m
	}
	// member resolves a service named on the command line to one of the members:
	// by id, slug or name first, then as project/env/slug.
	member := func(ref string) (string, error) {
		var hit []string
		for _, m := range members {
			if m.ServiceId == ref || m.Slug == ref || m.Name == ref {
				hit = append(hit, m.ServiceId)
			}
		}
		id := ""
		switch {
		case len(hit) == 1:
			id = hit[0]
		case len(hit) > 1:
			return "", usagef("%q matches several of the services: use their ID", ref)
		default:
			var err error
			if id, err = c.serviceID(ctx, ref); err != nil {
				return "", err
			}
		}
		if _, ok := byID[id]; !ok {
			return "", usagef("%s is not one of the services being combined", ref)
		}
		return id, nil
	}

	// Process names: the flag, else the suggestion of the plan.
	names := map[string]string{}
	var defaultName string
	for _, v := range o.processNames {
		ref, name, err := splitPair("process-name", v)
		if err != nil {
			return err
		}
		id, err := member(ref)
		if err != nil {
			return err
		}
		if id == plan.TargetServiceId {
			defaultName = name
		} else {
			names[id] = name
		}
	}
	for _, m := range members {
		if m.ServiceId == plan.TargetServiceId {
			continue
		}
		if names[m.ServiceId] == "" {
			names[m.ServiceId] = m.SuggestedProcessName
		}
	}

	// Variable choices.
	conflicts := items(plan.Variables.Conflicts)
	choices := map[string]string{}
	for _, v := range o.vars {
		key, ref, err := splitPair("var", v)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(conflicts, func(k apiclient.VariableConflict) bool { return k.Key == key })
		if i < 0 {
			return usagef("--var %s: the services do not set %s to different values", v, key)
		}
		id, err := member(ref)
		if err != nil {
			return err
		}
		held := false
		for _, opt := range items(conflicts[i].Options) {
			held = held || opt.ServiceId == id || slices.Contains(items(opt.SameAs), id)
		}
		if !held {
			return usagef("--var %s: %s does not set %s", v, ref, key)
		}
		choices[key] = id
	}

	blocking := items(plan.Blocking)
	if !o.yes || len(blocking) > 0 {
		if c.asJSON {
			if err := c.printJSON(plan); err != nil {
				return err
			}
		} else {
			c.printCombinePlan(plan, names, defaultName, choices)
		}
		if len(blocking) > 0 {
			return &exitError{1, fmt.Errorf("the services cannot be combined yet: %d blocking problem(s) above", len(blocking))}
		}
		if !o.yes {
			c.note("Nothing changed. Run again with --yes to combine.")
			return nil
		}
	}

	var missing []string
	for _, k := range conflicts {
		if _, ok := choices[k.Key]; ok {
			continue
		}
		var opts []string
		for _, opt := range items(k.Options) {
			opts = append(opts, opt.Slug)
		}
		missing = append(missing, fmt.Sprintf("  %s: --var %s=<%s>", k.Key, k.Key, strings.Join(opts, "|")))
	}
	if len(missing) > 0 {
		return usagef("choose the value to keep for every variable that differs:\n%s", strings.Join(missing, "\n"))
	}

	memberIDs := make([]string, 0, len(members))
	for _, m := range members {
		memberIDs = append(memberIDs, m.ServiceId)
	}
	body := apiclient.CombineRequest{
		Fingerprint:     plan.Fingerprint,
		TargetServiceId: plan.TargetServiceId,
		ServiceIds:      &memberIDs,
		ProcessNames:    names,
	}
	if defaultName != "" {
		body.DefaultProcessName = &defaultName
	}
	if len(choices) > 0 {
		body.VariableChoices = &choices
	}
	if o.stopFirst {
		body.StopSourcesFirst = &o.stopFirst
	}
	r, err := c.api.CombineServicesWithResponse(ctx, envID, &apiclient.CombineServicesParams{IdempotencyKey: c.key()}, body)
	if err := check(r, err); err != nil {
		return err
	}
	v := r.JSON202
	if !o.wait {
		if c.asJSON {
			return c.printJSON(v)
		}
		c.info("Combine %s started (%s)", v.Id, v.State)
		c.info("Follow it with: nebulactrl services combine status %s", v.Id)
		return nil
	}
	c.note("Combine %s: %s", v.Id, v.State)
	return c.waitCombine(cmd.Context(), v.Id, string(v.State), o.timeout)
}

// printCombinePlan writes the plan: members, blockers, variables, moves.
func (c *cli) printCombinePlan(p *apiclient.CombinePlan, names map[string]string, defaultName string, choices map[string]string) {
	members := items(p.Members)
	slug := func(id string) string {
		for _, m := range members {
			if m.ServiceId == id {
				return m.Slug
			}
		}
		return id
	}
	c.info("Combine into %s", slug(p.TargetServiceId))
	rows := make([][]string, 0, len(members))
	for _, m := range members {
		role := "process " + orDash(names[m.ServiceId])
		if m.IsTarget {
			role = "target"
			if defaultName != "" {
				role += " (its process becomes " + defaultName + ")"
			}
		}
		var hosts []string
		for _, d := range items(m.Domains) {
			hosts = append(hosts, d.Hostname)
		}
		port := "-"
		if m.Port > 0 {
			port = fmt.Sprint(m.Port)
		}
		rows = append(rows, []string{m.Slug, role, port, commandLabel(m.Command), orDash(strings.Join(hosts, ",")), yesNo(m.HasStagedChanges)})
	}
	c.table([]string{"SERVICE", "ROLE", "PORT", "COMMAND", "DOMAINS", "STAGED"}, rows)

	if b := items(p.Blocking); len(b) > 0 {
		c.info("")
		c.info("Blocking:")
		for _, x := range b {
			line := "  [" + x.Code + "] " + x.Message
			if len(items(x.Fields)) > 0 {
				line += " (" + strings.Join(items(x.Fields), ", ") + ")"
			}
			c.info("%s", line)
			for _, ref := range items(x.References) {
				c.info("      %s: %s", ref.Kind, ref.Detail)
			}
		}
	}
	if w := items(p.Warnings); len(w) > 0 {
		c.info("")
		c.info("Warnings:")
		for _, x := range w {
			c.info("  %s", x)
		}
	}
	vs := p.Variables
	c.info("")
	c.info("Variables: %d identical in every service that sets them", vs.SameCount)
	for _, o := range items(vs.OnlyIn) {
		c.info("  %s only in %s: carried over", o.Key, slug(o.ServiceId))
	}
	for _, k := range items(vs.Conflicts) {
		label := k.Key
		if k.Secret {
			label += " (secret)"
		}
		var opts []string
		for _, opt := range items(k.Options) {
			s := opt.Slug
			if opt.Preview != "" {
				s += "=" + opt.Preview
			}
			opts = append(opts, s)
		}
		if id, ok := choices[k.Key]; ok {
			c.info("  %s differs: %s; keeping %s", label, strings.Join(opts, ", "), slug(id))
		} else {
			c.info("  %s differs: %s; choose with --var %s=<service>", label, strings.Join(opts, ", "), k.Key)
		}
	}
	if s := items(p.SettingsIgnored); len(s) > 0 {
		c.info("")
		c.info("Settings of the other services that are not kept:")
		for _, x := range s {
			c.info("  %s %s: %s", slug(x.ServiceId), x.Field, x.Note)
		}
	}
	if d := items(p.DomainMoves); len(d) > 0 {
		c.info("")
		c.info("Domains that move to the target:")
		for _, x := range d {
			c.info("  %s (from %s) -> process %s", x.Hostname, slug(x.FromServiceId), orDash(x.Process))
		}
	}
	if t := items(p.TaskMoves); len(t) > 0 {
		c.info("")
		c.info("Tasks that move to the target:")
		for _, x := range t {
			c.info("  %s (from %s)", x.Name, slug(x.FromServiceId))
		}
	}
}

func (c *cli) getCombine(ctx context.Context, id string) (*apiclient.CombineView, error) {
	rctx, cancel := c.ctx(ctx)
	defer cancel()
	r, err := c.api.GetCombineWithResponse(rctx, id)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return r.JSON200, nil
}

// waitCombine polls until the combine is done, failed or aborted.
func (c *cli) waitCombine(parent context.Context, id, last string, timeout time.Duration) error {
	ctx := parent
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	failures := 0
	for {
		v, err := c.getCombine(ctx, id)
		switch {
		case err == nil:
			failures = 0
		case ctx.Err() != nil:
			if parent.Err() != nil {
				return &exitError{130, fmt.Errorf("interrupted; combine %s keeps running on the server", id)}
			}
			return &exitError{1, fmt.Errorf("timed out after %s waiting for combine %s (last state %s); it may still be running", timeout, id, last)}
		default:
			var ae *apiError
			failures++
			if (errors.As(err, &ae) && ae.Status < http.StatusInternalServerError) || failures > 3 {
				return err
			}
		}
		if err == nil {
			if string(v.State) != last {
				c.note("Combine %s: %s", id, v.State)
				last = string(v.State)
			}
			switch v.State {
			case apiclient.CombineViewStateDone:
				if c.asJSON {
					return c.printJSON(v)
				}
				c.info("Combine %s done", id)
				return nil
			case apiclient.CombineViewStateFailed, apiclient.CombineViewStateAborted:
				if c.asJSON {
					if err := c.printJSON(v); err != nil {
						return err
					}
				}
				msg := "combine " + id + " " + string(v.State)
				if v.Error != "" {
					msg += ": " + v.Error
				}
				return &exitError{1, errors.New(msg)}
			}
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return &exitError{130, fmt.Errorf("interrupted; combine %s keeps running on the server", id)}
			}
			return &exitError{1, fmt.Errorf("timed out after %s waiting for combine %s (last state %s); it may still be running", timeout, id, last)}
		case <-time.After(c.pollEvery):
		}
	}
}

func (c *cli) printCombine(v *apiclient.CombineView) error {
	if c.asJSON {
		return c.printJSON(v)
	}
	c.info("Combine %s: %s", v.Id, v.State)
	c.info("Target:     %s", v.TargetServiceId)
	c.info("Sources:    %s", orDash(strings.Join(items(v.SourceServiceIds), ", ")))
	if v.DeploymentId != "" {
		c.info("Deployment: %s (nebulactrl deployments status %s)", v.DeploymentId, v.DeploymentId)
	}
	if v.Error != "" {
		c.info("Error:      %s", v.Error)
	}
	switch v.State {
	case apiclient.CombineViewStateFailed:
		c.info("Nothing was deleted. Retry with `services combine retry %s` or take it back with `services combine abort %s`.", v.Id, v.Id)
	case apiclient.CombineViewStateStaged:
		c.info("Staged; the deployment starts next. Take it back with `services combine abort %s`.", v.Id)
	}
	return nil
}

func newCombineStatusCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "status <combine-id>", Short: "Show the state of a combine", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			v, err := c.getCombine(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return c.printCombine(v)
		},
	}
}

func newCombineAbortCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "abort <combine-id>", Short: "Take back a staged or failed combine", Args: exactArgs(1),
		Long: "Restarts the services the combine stopped, discards what it staged on the target and removes the scale settings it\n" +
			"created. Not possible while the deployment or the cutover runs, and not once the services are merged.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.AbortCombineWithResponse(ctx, args[0], &apiclient.AbortCombineParams{IdempotencyKey: c.key()})
			if err := check(r, err); err != nil {
				return err
			}
			return c.printCombine(r.JSON200)
		},
	}
}

func newCombineRetryCmd(c *cli) *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "retry <combine-id> [--wait]", Short: "Run a failed combine again", Args: exactArgs(1),
		Long: "Deploys the target again with the configuration the combine staged, then moves the domains and tasks and deletes the\n" +
			"other services. When the target already runs every process only that last step runs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			r, err := c.api.RetryCombineWithResponse(ctx, args[0], &apiclient.RetryCombineParams{IdempotencyKey: c.key()})
			cancel()
			if err := check(r, err); err != nil {
				return err
			}
			if !wait {
				return c.printCombine(r.JSON202)
			}
			c.note("Combine %s: %s", r.JSON202.Id, r.JSON202.State)
			return c.waitCombine(cmd.Context(), r.JSON202.Id, string(r.JSON202.State), timeout)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the merge is done; exit non-zero if it failed")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (with --wait)")
	return cmd
}
