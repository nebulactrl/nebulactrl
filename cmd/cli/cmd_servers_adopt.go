package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// adoptFlags are the options of `servers adopt`.
type adoptFlags struct {
	planOnly bool

	project, envName, production, notProduction []string
	leave, remove, name                         []string
	secret, notSecret, skipDomain               []string
}

func newServersAdoptCmd(c *cli) *cobra.Command {
	var f adoptFlags
	cmd := &cobra.Command{
		Use:   "adopt <server> [flags]",
		Short: "Review and adopt the workloads an earlier NebulaCtrl left running on a server",
		Long: "A server added to this instance that already runs NebulaCtrl workloads (its control plane was lost or reinstalled, or it comes\n" +
			"from another instance) is held: nothing on it is removed or rerouted until its workloads are reviewed. This command reads them\n" +
			"from the host and rebuilds the projects, environments, services, volumes, variables and domains with the IDs found on the\n" +
			"containers, so nothing is recreated or restarted. Services you --leave keep running unmanaged and are never removed; services\n" +
			"you --remove are removed once the review is applied (their volumes stay on the host). Variables are listed by key; values are\n" +
			"never printed.\n\n" +
			"<env> is an environment ID found on the containers or its first 8 characters; <svc> is a service slug, ID or its first 8\n" +
			"characters, as listed by --plan.",
		Example: "  nebulactrl servers adopt web-1 --plan\n" +
			"  nebulactrl servers adopt web-1 --leave postgres --name app=Website --project 1a2b3c4d=Shop",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runServersAdopt(c, cmd, args[0], &f) },
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.planOnly, "plan", false, "show what would be adopted and change nothing (exit 1 when something blocks the adoption)")
	fl.StringArrayVar(&f.project, "project", nil, "<env>=<project name|id>: adopt the environment into an existing project, or into a new project of that name (repeatable)")
	fl.StringArrayVar(&f.envName, "env-name", nil, "<env>=<name>: name of the rebuilt environment (repeatable)")
	fl.StringArrayVar(&f.production, "production", nil, "<env>: mark the rebuilt environment as production (repeatable)")
	fl.StringArrayVar(&f.notProduction, "not-production", nil, "<env>: mark the rebuilt environment as not production (repeatable)")
	fl.StringArrayVar(&f.leave, "leave", nil, "<svc>: keep the service running unmanaged (repeatable)")
	fl.StringArrayVar(&f.remove, "remove", nil, "<svc>: remove the service's containers once the review is applied; volumes stay (repeatable)")
	fl.StringArrayVar(&f.name, "name", nil, "<svc>=<name>: name of the adopted service (repeatable)")
	fl.StringArrayVar(&f.secret, "secret", nil, "<svc>.<KEY>: store the variable as a secret (repeatable)")
	fl.StringArrayVar(&f.notSecret, "not-secret", nil, "<svc>.<KEY>: store the variable in plain text (repeatable)")
	fl.StringArrayVar(&f.skipDomain, "skip-domain", nil, "<svc>/<host>: do not adopt this domain (repeatable)")
	return cmd
}

// adoptRef is an environment or a service of an adoption plan.
type adoptRef struct{ id, slug, label string }

func adoptEnvRefs(p *apiclient.AdoptionPlan) []adoptRef {
	var out []adoptRef
	for _, e := range deref(p.Environments) {
		out = append(out, adoptRef{id: e.Id, label: e.ProjectName + "/" + e.Name})
	}
	return out
}

func adoptServiceRefs(p *apiclient.AdoptionPlan) []adoptRef {
	var out []adoptRef
	for _, e := range deref(p.Environments) {
		for _, s := range deref(e.Services) {
			out = append(out, adoptRef{id: s.Id, slug: s.Slug, label: e.Name + "/" + s.Slug})
		}
	}
	return out
}

// resolveAdoptRef finds an environment or service by ID, 8-character ID
// prefix or (services) slug. kind names it in errors.
func resolveAdoptRef(kind string, refs []adoptRef, ref string) (string, error) {
	var found []adoptRef
	for _, r := range refs {
		switch {
		case strings.EqualFold(r.id, ref):
			return r.id, nil
		case len(ref) >= 8 && strings.HasPrefix(r.id, strings.ToLower(ref)), r.slug != "" && strings.EqualFold(r.slug, ref):
			found = append(found, r)
		}
	}
	switch len(found) {
	case 1:
		return found[0].id, nil
	case 0:
		list := make([]string, len(refs))
		for i, r := range refs {
			list[i] = r.label + " (" + r.id[:min(8, len(r.id))] + ")"
		}
		return "", usagef("no %s %q on this server (found: %s)", kind, ref, strings.Join(list, ", "))
	}
	list := make([]string, len(found))
	for i, r := range found {
		list[i] = r.label + " (" + r.id + ")"
	}
	return "", usagef("%s %q is ambiguous: %s; use the ID", kind, ref, strings.Join(list, ", "))
}

// choices builds the reviewer's decisions from the flags, resolving references against the plan.
func (f *adoptFlags) choices(ctx context.Context, c *cli, base *apiclient.AdoptionPlan) (apiclient.AdoptionChoices, error) {
	var ch apiclient.AdoptionChoices
	envRefs, svcRefs := adoptEnvRefs(base), adoptServiceRefs(base)
	envs := map[string]apiclient.AdoptionEnvironmentChoice{}
	svcs := map[string]apiclient.AdoptionServiceChoice{}

	editEnv := func(flag string, vals []string, withValue bool, apply func(ec *apiclient.AdoptionEnvironmentChoice, v string) error) error {
		for _, raw := range vals {
			ref, v := raw, ""
			if withValue {
				var err error
				if ref, v, err = splitKV(flag, raw); err != nil {
					return err
				}
			}
			id, err := resolveAdoptRef("environment", envRefs, ref)
			if err != nil {
				return err
			}
			ec := envs[id]
			if err := apply(&ec, v); err != nil {
				return err
			}
			envs[id] = ec
		}
		return nil
	}
	t, fl := true, false
	if err := editEnv("env-name", f.envName, true, func(ec *apiclient.AdoptionEnvironmentChoice, v string) error { ec.Name = &v; return nil }); err != nil {
		return ch, err
	}
	if err := editEnv("production", f.production, false, func(ec *apiclient.AdoptionEnvironmentChoice, _ string) error { ec.Production = &t; return nil }); err != nil {
		return ch, err
	}
	if err := editEnv("not-production", f.notProduction, false, func(ec *apiclient.AdoptionEnvironmentChoice, _ string) error { ec.Production = &fl; return nil }); err != nil {
		return ch, err
	}
	if len(f.project) > 0 {
		projects, err := c.projects(ctx)
		if err != nil {
			return ch, err
		}
		if err := editEnv("project", f.project, true, func(ec *apiclient.AdoptionEnvironmentChoice, v string) error {
			if v == "" {
				return usagef("--project: use <env>=<project name|id>")
			}
			for _, p := range projects {
				if p.Id == v || matchFold(p.Slug, v) || matchFold(p.Name, v) {
					id := p.Id
					ec.ProjectId = &id
					return nil
				}
			}
			name := v
			ec.ProjectName = &name // no such project: a new one
			return nil
		}); err != nil {
			return ch, err
		}
	}

	editSvc := func(id string, apply func(sc *apiclient.AdoptionServiceChoice)) {
		sc := svcs[id]
		apply(&sc)
		svcs[id] = sc
	}
	for _, step := range []struct {
		flag   string
		vals   []string
		action apiclient.AdoptionServiceChoiceAction
	}{{"leave", f.leave, "leave"}, {"remove", f.remove, "remove"}} {
		for _, ref := range step.vals {
			id, err := resolveAdoptRef("service", svcRefs, ref)
			if err != nil {
				return ch, err
			}
			a := step.action
			editSvc(id, func(sc *apiclient.AdoptionServiceChoice) { sc.Action = &a })
		}
	}
	for _, raw := range f.name {
		ref, v, err := splitKV("name", raw)
		if err != nil {
			return ch, err
		}
		id, err := resolveAdoptRef("service", svcRefs, ref)
		if err != nil {
			return ch, err
		}
		editSvc(id, func(sc *apiclient.AdoptionServiceChoice) { sc.Name = &v })
	}
	for _, step := range []struct {
		flag   string
		vals   []string
		secret bool
	}{{"secret", f.secret, true}, {"not-secret", f.notSecret, false}} {
		for _, raw := range step.vals {
			i := strings.LastIndex(raw, ".")
			if i <= 0 || i == len(raw)-1 {
				return ch, usagef("--%s %q: use <svc>.<KEY>", step.flag, raw)
			}
			id, err := resolveAdoptRef("service", svcRefs, raw[:i])
			if err != nil {
				return ch, err
			}
			key, secret := raw[i+1:], step.secret
			editSvc(id, func(sc *apiclient.AdoptionServiceChoice) {
				m := deref(sc.Secret)
				if m == nil {
					m = map[string]bool{}
				}
				m[key] = secret
				sc.Secret = &m
			})
		}
	}
	for _, raw := range f.skipDomain {
		ref, host, ok := strings.Cut(raw, "/")
		if !ok || ref == "" || host == "" {
			return ch, usagef("--skip-domain %q: use <svc>/<host>", raw)
		}
		id, err := resolveAdoptRef("service", svcRefs, ref)
		if err != nil {
			return ch, err
		}
		editSvc(id, func(sc *apiclient.AdoptionServiceChoice) {
			hosts := append(deref(sc.SkipHosts), host)
			sc.SkipHosts = &hosts
		})
	}
	if len(envs) > 0 {
		ch.Environments = &envs
	}
	if len(svcs) > 0 {
		ch.Services = &svcs
	}
	return ch, nil
}

func runServersAdopt(c *cli, cmd *cobra.Command, serverRef string, f *adoptFlags) error {
	if err := c.connect(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*requestTimeout)
	defer cancel()
	srv, err := c.resolveServer(ctx, serverRef)
	if err != nil {
		return err
	}
	plan := func(ch apiclient.AdoptionChoices) (*apiclient.AdoptionPlan, error) {
		r, err := c.api.PlanServerAdoptionWithResponse(ctx, srv.Id, apiclient.AdoptionPlanRequest{Choices: &ch})
		if err := check(r, err); err != nil {
			return nil, err
		}
		return r.JSON200, nil
	}
	c.note("Reading the workloads of %s ...", srv.Name)
	base, err := plan(apiclient.AdoptionChoices{})
	if err != nil {
		return err
	}
	choices, err := f.choices(ctx, c, base)
	if err != nil {
		return err
	}
	final := base
	if choices.Environments != nil || choices.Services != nil {
		if final, err = plan(choices); err != nil {
			return err
		}
	}
	blocked := len(deref(final.Blockers)) > 0
	if !c.asJSON {
		printAdoptionPlan(c, final)
	}
	if f.planOnly {
		if c.asJSON {
			if err := c.printJSON(final); err != nil {
				return err
			}
		}
		if blocked {
			return &exitError{code: 1}
		}
		return nil
	}
	if blocked {
		return fmt.Errorf("the workloads can't be adopted yet; fix the blockers above (--leave a service that cannot be adopted)")
	}
	r, err := c.api.AdoptServerWithResponse(ctx, srv.Id, &apiclient.AdoptServerParams{IdempotencyKey: c.key()}, apiclient.AdoptionApplyRequest{Choices: &choices})
	if err := check(r, err); err != nil {
		return err
	}
	res := r.JSON201
	if c.asJSON {
		return c.printJSON(res)
	}
	c.info("")
	services := deref(res.Services)
	c.info("Adopted %d service(s) into %d new project(s):", len(services), len(deref(res.Projects)))
	for _, s := range services {
		c.info("  %s/%s/%s", s.ProjectSlug, s.EnvSlug, s.Slug)
	}
	if res.Reattached > 0 {
		c.info("%d service(s) of this instance are placed on %s again.", res.Reattached, srv.Name)
	}
	if res.Left > 0 {
		c.info("%d workload(s) keep running unmanaged on %s.", res.Left, srv.Name)
	}
	if res.Removed > 0 {
		c.info("%d workload(s) are removed from %s; their volumes stay on the host.", res.Removed, srv.Name)
	}
	for _, w := range deref(res.Warnings) {
		c.note("Warning: %s", w)
	}
	return nil
}

// printAdoptionPlan prints the plan: environments, services with what happens to them, variables by key.
func printAdoptionPlan(c *cli, p *apiclient.AdoptionPlan) {
	if p.Held {
		c.info("The server is held for adoption: nothing on it changes until the review is applied.")
	}
	for _, e := range deref(p.Environments) {
		prod := ""
		if e.Production {
			prod = ", production"
		}
		c.info("\nEnvironment %s (%s) → project %s (%s), environment %s (%s%s)",
			e.Id[:min(8, len(e.Id))], string(e.Action), e.ProjectName, string(e.ProjectAction), e.Name, e.Slug, prod)
		rows := [][]string{}
		for _, s := range deref(e.Services) {
			source := deref(s.Image)
			if d := s.Database; d != nil {
				source = "managed " + d.Engine + " " + d.Version
			}
			var hosts []string
			for _, d := range deref(s.Domains) {
				if d.Include {
					hosts = append(hosts, d.Host+deref(d.PathPrefix))
				}
			}
			secrets := 0
			for _, v := range deref(s.Env) {
				if v.Secret {
					secrets++
				}
			}
			rows = append(rows, []string{
				"  " + s.Slug, s.Id[:min(8, len(s.Id))], string(s.Kind), string(s.Action), orDash(source),
				fmt.Sprintf("%d", s.Replicas), orDash(strings.Join(hosts, ",")), fmt.Sprintf("%d (%d secret)", len(deref(s.Env)), secrets),
			})
		}
		c.table([]string{"  SERVICE", "ID", "KIND", "ACTION", "SOURCE", "REPLICAS", "DOMAINS", "VARIABLES"}, rows)
		for _, s := range deref(e.Services) {
			printAdoptionServiceDetails(c, s)
		}
	}
	for _, w := range deref(p.Warnings) {
		c.info("  [warning] %s", w)
	}
	for _, b := range deref(p.Blockers) {
		c.info("  [blocked] %s", b)
	}
	t := p.Totals
	c.info("\n%d to adopt, %d left running unmanaged, %d to remove (%d container(s)).", t.Adopt, t.Leave, t.Remove, t.Containers)
}

func printAdoptionServiceDetails(c *cli, s apiclient.AdoptionService) {
	if !s.Adoptable && deref(s.Reason) != "" {
		c.info("    %s is not adopted: %s", s.Slug, *s.Reason)
	}
	if env := deref(s.Env); len(env) > 0 {
		keys := make([]string, 0, len(env))
		for _, v := range env {
			k := v.Key
			if v.Secret {
				k += "*"
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.info("    %s variables (* secret): %s", s.Slug, strings.Join(keys, " "))
	}
	for _, v := range deref(s.Volumes) {
		name := v.Name
		if deref(v.DockerName) != "" {
			name += " (" + *v.DockerName + ")"
		}
		c.info("    %s volume %s at %s", s.Slug, name, v.MountPath)
	}
	for _, d := range deref(s.Domains) {
		if !d.Include {
			c.info("    %s domain %s%s left out: %s", s.Slug, d.Host, deref(d.PathPrefix), deref(d.Note))
		}
	}
	for _, ct := range deref(s.Containers) {
		if deref(ct.Leftover) {
			c.info("    %s container %s (%s) is from an older deployment: removed after adoption", s.Slug, ct.Name, ct.State)
		}
	}
	for _, n := range deref(s.Notes) {
		c.info("    [note] %s: %s", s.Slug, n)
	}
}
