package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// coolifyFlags are the options of `import coolify`.
type coolifyFlags struct {
	url, token, tokenFile, tokenEnv string
	server, gitConnection           string
	planOnly, deploy                bool

	skipProject, into, renameProject []string
	include, exclude, rename         []string
	port, kind, engineVersion        []string
	domain                           []string
	secret, notSecret, skipVar       []string
	noRewrite, set, setEnv           []string
	registryUser, registryTokenEnv   []string
}

func newImportCoolifyCmd(c *cli) *cobra.Command {
	var f coolifyFlags
	cmd := &cobra.Command{
		Use:   "coolify --url <coolify url> --server <server> [flags]",
		Short: "Import projects, applications and databases from a Coolify v4 instance as staged services",
		Long: "Reads a Coolify instance through its API and stages the same projects and environments here: applications become image or git services\n" +
			"(Dockerfile builds stay Dockerfile builds, Nixpacks becomes Railpack), databases become managed databases of the same engine and version,\n" +
			"domains, health checks, resource limits, custom Docker options, deployment commands and variables come along. Variables that point at a Coolify\n" +
			"database or application are rewritten to ${{service.EXPORT}} references; credentials, locked values and secret-looking keys are stored as secrets.\n" +
			"Nothing is deployed unless you pass --deploy, and database and volume DATA is never copied (the plan prints the follow-up commands).\n\n" +
			"The Coolify API token needs the `read` and `read:sensitive` abilities (Keys & Tokens → API tokens in Coolify); read:sensitive is what returns\n" +
			"variable values and custom Docker options. The token is read from --token-env (default COOLIFY_TOKEN), --token-file or --token, sent to your\n" +
			"NebulaCtrl server for this one request, and never stored or printed. Registry pull credentials cannot be read from Coolify: give them with\n" +
			"--registry-user and --registry-token-env.\n\n" +
			"Use --plan (or --dry-run) to review without changing anything: it lists every resource, its variables by key (never values), the rewritten\n" +
			"references and everything that is not imported. Per-resource decisions name the resource by its Coolify name or UUID, variables as <resource>.<KEY>.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runImportCoolify(c, cmd, &f) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.url, "url", "", "address of the Coolify instance (default: $COOLIFY_URL)")
	fl.StringVar(&f.token, "token", "", "Coolify API token (visible in the process list: prefer --token-env or --token-file)")
	fl.StringVar(&f.tokenFile, "token-file", "", "read the Coolify API token from this file (- for stdin)")
	fl.StringVar(&f.tokenEnv, "token-env", "COOLIFY_TOKEN", "environment variable that holds the Coolify API token")
	fl.StringVar(&f.server, "server", "", "server ID or name the services run on (required to import)")
	fl.StringVar(&f.gitConnection, "git-connection", "", "git connection ID or name used to clone private GitHub/GitLab repositories")
	fl.BoolVar(&f.planOnly, "plan", false, "show what would be imported and change nothing")
	fl.BoolVar(&f.planOnly, "dry-run", false, "same as --plan")
	fl.BoolVar(&f.deploy, "deploy", false, "deploy the new services right away (default: everything stays staged)")

	fl.StringArrayVar(&f.skipProject, "skip-project", nil, "<coolify project>: do not import this project (repeatable)")
	fl.StringArrayVar(&f.into, "into", nil, "<coolify project>=<project>: import into an existing project instead of creating one (repeatable)")
	fl.StringArrayVar(&f.renameProject, "rename-project", nil, "<coolify project>=<name>: name of the new project (default: the same name, plus \" (Coolify)\" when it exists)")
	fl.StringArrayVar(&f.exclude, "exclude", nil, "<resource>: do not import this application or database (repeatable)")
	fl.StringArrayVar(&f.include, "include", nil, "<resource>: import it although it is off by default; for Redis and MongoDB this accepts their source-available licence (repeatable)")
	fl.StringArrayVar(&f.rename, "rename", nil, "<resource>=<name>: service name (repeatable)")
	fl.StringArrayVar(&f.port, "port", nil, "<resource>=<port>: service port; 0 makes it a worker without a port (repeatable)")
	fl.StringArrayVar(&f.kind, "kind", nil, "<resource>=<app|worker> (repeatable)")
	fl.StringArrayVar(&f.engineVersion, "engine-version", nil, "<database>=<version>: managed database version (repeatable)")
	fl.StringArrayVar(&f.domain, "domain", nil, "<resource>/<coolify host>=<new host>|off: change or drop a domain (repeatable)")
	fl.StringArrayVar(&f.secret, "secret", nil, "<resource>.<KEY>: store the variable as a secret (repeatable)")
	fl.StringArrayVar(&f.notSecret, "not-secret", nil, "<resource>.<KEY>: store the variable in plain text (repeatable)")
	fl.StringArrayVar(&f.skipVar, "skip-var", nil, "<resource>.<KEY>: do not import the variable (repeatable)")
	fl.StringArrayVar(&f.noRewrite, "no-rewrite", nil, "<resource>.<KEY>: keep host names and credentials of Coolify resources in the value as they are (repeatable)")
	fl.StringArrayVar(&f.set, "set", nil, "<resource>.<KEY>=<value>: supply the value of a locked variable (visible in the process list: prefer --set-env) (repeatable)")
	fl.StringArrayVar(&f.setEnv, "set-env", nil, "<resource>.<KEY>=<ENV_VAR>: supply the value of a locked variable from an environment variable (repeatable)")
	fl.StringArrayVar(&f.registryUser, "registry-user", nil, "<registry host>=<username>: pull credential user, for example ghcr.io=octocat (repeatable)")
	fl.StringArrayVar(&f.registryTokenEnv, "registry-token-env", nil, "<registry host>=<ENV_VAR>: environment variable with that registry's token or password (repeatable)")
	return cmd
}

func (f *coolifyFlags) readToken(c *cli) (string, error) {
	switch {
	case f.token != "":
		return strings.TrimSpace(f.token), nil
	case f.tokenFile == "-":
		raw, err := io.ReadAll(io.LimitReader(c.in, 1<<16))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	case f.tokenFile != "":
		raw, err := os.ReadFile(f.tokenFile)
		if err != nil {
			return "", fmt.Errorf("read --token-file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if v := strings.TrimSpace(os.Getenv(f.tokenEnv)); v != "" {
		return v, nil
	}
	return "", usagef("a Coolify API token is required: set $%s, or use --token-file (create one in Coolify under Keys & Tokens → API tokens with the read and read:sensitive abilities)", f.tokenEnv)
}

// resRef is a resource of a plan, for resolving names to UUIDs.
type resRef struct{ uuid, name, slug, project, env string }

func (r resRef) String() string { return r.project + "/" + r.env + "/" + r.name }

func planResources(p *apiclient.CoolifyPlan) []resRef {
	var out []resRef
	for _, pr := range deref(p.Projects) {
		for _, e := range deref(pr.Environments) {
			for _, r := range deref(e.Resources) {
				out = append(out, resRef{uuid: r.Uuid, name: r.Name, slug: r.Slug, project: pr.Name, env: e.Name})
			}
		}
	}
	return out
}

// resolveResource finds a resource by UUID, name, slug or project/env/name.
func resolveResource(refs []resRef, ref string) (resRef, error) {
	var found []resRef
	for _, r := range refs {
		switch {
		case r.uuid == ref:
			return r, nil
		case strings.EqualFold(r.name, ref), strings.EqualFold(r.slug, ref), strings.EqualFold(r.String(), ref):
			found = append(found, r)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		names := make([]string, len(refs))
		for i, r := range refs {
			names[i] = r.name
		}
		return resRef{}, usagef("no Coolify application or database %q (found: %s)", ref, strings.Join(names, ", "))
	}
	list := make([]string, len(found))
	for i, r := range found {
		list[i] = r.String() + " (" + r.uuid + ")"
	}
	return resRef{}, usagef("%q is ambiguous: %s; use the UUID", ref, strings.Join(list, ", "))
}

func resolveProjectRef(p *apiclient.CoolifyPlan, ref string) (string, error) {
	for _, pr := range deref(p.Projects) {
		if pr.Uuid == ref || strings.EqualFold(pr.Name, ref) {
			return pr.Uuid, nil
		}
	}
	return "", usagef("no Coolify project %q", ref)
}

func splitKV(flag, raw string) (k, v string, err error) {
	k, v, ok := strings.Cut(raw, "=")
	if !ok || k == "" {
		return "", "", usagef("--%s %q: use <name>=<value>", flag, raw)
	}
	return k, v, nil
}

// choices builds the reviewer's decisions from the flags, resolving names against the plan.
func (f *coolifyFlags) choices(ctx context.Context, c *cli, base *apiclient.CoolifyPlan) (apiclient.CoolifyChoices, error) {
	var ch apiclient.CoolifyChoices
	t, fl := true, false
	refs := planResources(base)
	projects := map[string]apiclient.CoolifyProjectChoice{}
	resources := map[string]apiclient.CoolifyResourceChoice{}
	res := func(ref string) (string, apiclient.CoolifyResourceChoice, error) {
		r, err := resolveResource(refs, ref)
		if err != nil {
			return "", apiclient.CoolifyResourceChoice{}, err
		}
		return r.uuid, resources[r.uuid], nil
	}
	editRes := func(flag string, vals []string, withValue bool, apply func(rc *apiclient.CoolifyResourceChoice, v string) error) error {
		for _, raw := range vals {
			ref, v := raw, ""
			if withValue {
				var err error
				if ref, v, err = splitKV(flag, raw); err != nil {
					return err
				}
			}
			id, rc, err := res(ref)
			if err != nil {
				return err
			}
			if err := apply(&rc, v); err != nil {
				return usagef("--%s %q: %v", flag, raw, err)
			}
			resources[id] = rc
		}
		return nil
	}
	for _, step := range []struct {
		flag  string
		vals  []string
		value bool
		apply func(rc *apiclient.CoolifyResourceChoice, v string) error
	}{
		{"exclude", f.exclude, false, func(rc *apiclient.CoolifyResourceChoice, _ string) error { rc.Include = &fl; return nil }},
		{"include", f.include, false, func(rc *apiclient.CoolifyResourceChoice, _ string) error { rc.Include = &t; return nil }},
		{"rename", f.rename, true, func(rc *apiclient.CoolifyResourceChoice, v string) error { rc.Name = &v; return nil }},
		{"engine-version", f.engineVersion, true, func(rc *apiclient.CoolifyResourceChoice, v string) error { rc.EngineVersion = &v; return nil }},
		{"port", f.port, true, func(rc *apiclient.CoolifyResourceChoice, v string) error {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 || n > 65535 {
				return fmt.Errorf("not a port")
			}
			rc.Port = &n
			return nil
		}},
		{"kind", f.kind, true, func(rc *apiclient.CoolifyResourceChoice, v string) error {
			if v != "app" && v != "worker" {
				return fmt.Errorf("use app or worker")
			}
			k := apiclient.CoolifyResourceChoiceKind(v)
			rc.Kind = &k
			return nil
		}},
	} {
		if err := editRes(step.flag, step.vals, step.value, step.apply); err != nil {
			return ch, err
		}
	}
	for _, raw := range f.domain {
		k, v, err := splitKV("domain", raw)
		if err != nil {
			return ch, err
		}
		ref, host, ok := strings.Cut(k, "/")
		if !ok || host == "" || v == "" {
			return ch, usagef("--domain %q: use <resource>/<coolify host>=<new host>|off", raw)
		}
		id, rc, err := res(ref)
		if err != nil {
			return ch, err
		}
		m := deref(rc.Domains)
		if m == nil {
			m = map[string]apiclient.CoolifyDomainChoice{}
		}
		if v == "off" {
			m[host] = apiclient.CoolifyDomainChoice{Enabled: &fl}
		} else {
			m[host] = apiclient.CoolifyDomainChoice{Host: &v}
		}
		rc.Domains = &m
		resources[id] = rc
	}
	// variables: <resource>.<KEY>
	envEdit := func(flag, raw string, apply func(e *apiclient.CoolifyEnvChoice) error) error {
		ref, key, ok := strings.Cut(raw, ".")
		if !ok || ref == "" || key == "" {
			return usagef("--%s %q: use <resource>.<KEY>", flag, raw)
		}
		// resource names may contain dots: split at the last one instead
		if i := strings.LastIndex(raw, "."); i > 0 {
			if _, err := resolveResource(refs, raw[:i]); err == nil {
				ref, key = raw[:i], raw[i+1:]
			}
		}
		id, rc, err := res(ref)
		if err != nil {
			return err
		}
		m := deref(rc.Env)
		if m == nil {
			m = map[string]apiclient.CoolifyEnvChoice{}
		}
		e := m[key]
		if err := apply(&e); err != nil {
			return usagef("--%s %q: %v", flag, raw, err)
		}
		m[key] = e
		rc.Env = &m
		resources[id] = rc
		return nil
	}
	for _, step := range []struct {
		flag  string
		vals  []string
		apply func(e *apiclient.CoolifyEnvChoice) error
	}{
		{"secret", f.secret, func(e *apiclient.CoolifyEnvChoice) error { e.Secret = &t; return nil }},
		{"not-secret", f.notSecret, func(e *apiclient.CoolifyEnvChoice) error { e.Secret = &fl; return nil }},
		{"skip-var", f.skipVar, func(e *apiclient.CoolifyEnvChoice) error { e.Skip = &t; return nil }},
		{"no-rewrite", f.noRewrite, func(e *apiclient.CoolifyEnvChoice) error { e.Rewrite = &fl; return nil }},
	} {
		for _, raw := range step.vals {
			if err := envEdit(step.flag, raw, step.apply); err != nil {
				return ch, err
			}
		}
	}
	for _, raw := range f.set {
		k, v, err := splitKV("set", raw)
		if err != nil {
			return ch, err
		}
		val := v
		if err := envEdit("set", k, func(e *apiclient.CoolifyEnvChoice) error { e.Value = &val; return nil }); err != nil {
			return ch, err
		}
	}
	for _, raw := range f.setEnv {
		k, name, err := splitKV("set-env", raw)
		if err != nil {
			return ch, err
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			return ch, usagef("--set-env %q: $%s is not set", raw, name)
		}
		if err := envEdit("set-env", k, func(e *apiclient.CoolifyEnvChoice) error { e.Value = &val; return nil }); err != nil {
			return ch, err
		}
	}

	// projects
	pedit := func(flag string, vals []string, withValue bool, apply func(pc *apiclient.CoolifyProjectChoice, v string) error) error {
		for _, raw := range vals {
			ref, v := raw, ""
			if withValue {
				var err error
				if ref, v, err = splitKV(flag, raw); err != nil {
					return err
				}
			}
			id, err := resolveProjectRef(base, ref)
			if err != nil {
				return err
			}
			pc := projects[id]
			if err := apply(&pc, v); err != nil {
				return err
			}
			projects[id] = pc
		}
		return nil
	}
	if err := pedit("skip-project", f.skipProject, false, func(pc *apiclient.CoolifyProjectChoice, _ string) error { pc.Skip = &t; return nil }); err != nil {
		return ch, err
	}
	if err := pedit("rename-project", f.renameProject, true, func(pc *apiclient.CoolifyProjectChoice, v string) error { pc.Name = &v; return nil }); err != nil {
		return ch, err
	}
	if len(f.into) > 0 {
		if err := pedit("into", f.into, true, func(pc *apiclient.CoolifyProjectChoice, v string) error {
			p, err := c.resolveProject(ctx, v)
			if err != nil {
				return err
			}
			pc.ExistingProjectId = &p.Id
			return nil
		}); err != nil {
			return ch, err
		}
	}

	// registries
	regs := map[string]apiclient.CoolifyRegistryAuth{}
	hints := map[string]string{}
	for _, r := range deref(base.Registries) {
		hints[strings.ToLower(r.Host)] = deref(r.Username)
	}
	users := map[string]string{}
	for _, raw := range f.registryUser {
		h, u, err := splitKV("registry-user", raw)
		if err != nil {
			return ch, err
		}
		users[strings.ToLower(h)] = u
	}
	for _, raw := range f.registryTokenEnv {
		h, name, err := splitKV("registry-token-env", raw)
		if err != nil {
			return ch, err
		}
		pw, ok := os.LookupEnv(name)
		if !ok || pw == "" {
			return ch, usagef("--registry-token-env %q: $%s is not set", raw, name)
		}
		h = strings.ToLower(h)
		user := firstNonEmpty(users[h], hints[h])
		if user == "" {
			return ch, usagef("--registry-token-env %q: no username known for %s; add --registry-user %s=<username>", raw, h, h)
		}
		regs[h] = apiclient.CoolifyRegistryAuth{Username: user, Password: pw}
	}
	for h := range users {
		if _, ok := regs[h]; !ok {
			return ch, usagef("--registry-user %s needs --registry-token-env %s=<ENV_VAR>", h, h)
		}
	}

	if f.gitConnection != "" {
		id, err := c.resolveGitConnection(ctx, f.gitConnection)
		if err != nil {
			return ch, err
		}
		ch.GitConnectionId = &id
	}
	if len(projects) > 0 {
		ch.Projects = &projects
	}
	if len(resources) > 0 {
		ch.Resources = &resources
	}
	if len(regs) > 0 {
		ch.Registries = &regs
	}
	return ch, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func runImportCoolify(c *cli, cmd *cobra.Command, f *coolifyFlags) error {
	if f.url == "" {
		f.url = os.Getenv("COOLIFY_URL")
	}
	if f.url == "" {
		return usagef("--url is required (or set $COOLIFY_URL)")
	}
	if !f.planOnly && f.server == "" {
		return usagef("--server is required to import (use --plan to review without one)")
	}
	token, err := f.readToken(c)
	if err != nil {
		return err
	}
	if err := c.connect(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*requestTimeout)
	defer cancel()

	var serverID *string
	if f.server != "" {
		s, err := c.resolveServer(ctx, f.server)
		if err != nil {
			return err
		}
		serverID = &s.Id
	}
	src := apiclient.CoolifySource{Url: f.url, Token: token}
	plan := func(ch apiclient.CoolifyChoices) (*apiclient.CoolifyPlan, error) {
		r, err := c.api.PlanCoolifyImportWithResponse(ctx, apiclient.CoolifyPlanRequest{Source: src, Choices: ch, ServerId: serverID})
		if err := check(r, err); err != nil {
			return nil, err
		}
		return r.JSON200, nil
	}
	c.note("Reading %s ...", f.url)
	base, err := plan(apiclient.CoolifyChoices{})
	if err != nil {
		return err
	}
	choices, err := f.choices(ctx, c, base)
	if err != nil {
		return err
	}
	final := base
	if choices.Projects != nil || choices.Resources != nil || choices.Registries != nil || choices.GitConnectionId != nil {
		if final, err = plan(choices); err != nil {
			return err
		}
	}

	if !c.asJSON {
		printCoolifyPlan(c, final)
	}
	if f.planOnly {
		if c.asJSON {
			if err := c.printJSON(final); err != nil {
				return err
			}
		}
		if !final.Installable {
			return &exitError{code: 1}
		}
		return nil
	}
	if !final.Installable {
		return fmt.Errorf("the instance can't be imported yet; fix the blockers above (see --help for the flags that resolve them)")
	}

	req := apiclient.CoolifyApplyRequest{Source: src, Choices: choices, ServerId: *serverID}
	if f.deploy {
		req.Deploy = &f.deploy
	}
	r, err := c.api.ImportCoolifyWithResponse(ctx, &apiclient.ImportCoolifyParams{IdempotencyKey: c.key()}, req)
	if err := check(r, err); err != nil {
		return err
	}
	res := r.JSON201
	if c.asJSON {
		return c.printJSON(res)
	}
	c.info("")
	var slugs []string
	for _, s := range deref(res.Services) {
		slugs = append(slugs, s.ProjectSlug+"/"+s.EnvSlug+"/"+s.Slug)
	}
	c.info("Imported %d project(s) and %d service(s):", len(deref(res.Projects)), len(slugs))
	for _, s := range slugs {
		c.info("  %s", s)
	}
	if f.deploy {
		c.info("Started %d deployment(s); follow them with: nebulactrl deployments status <id>", len(deref(res.Deployments)))
	} else {
		c.info("Everything is staged, nothing is deployed. Review the services, then deploy with `nebulactrl services deploy <service> --wait` or from the staged bar.")
	}
	for _, w := range deref(res.Warnings) {
		c.note("Warning: %s", w)
	}
	if fu := deref(res.Followups); len(fu) > 0 {
		c.info("\nNext steps:")
		for _, s := range fu {
			c.info("  - %s", s)
		}
	}
	return nil
}

// printCoolifyPlan prints the plan: what is created, variables by key, references, what is not imported.
func printCoolifyPlan(c *cli, p *apiclient.CoolifyPlan) {
	if p.CoolifyVersion != nil {
		c.info("Coolify %s", *p.CoolifyVersion)
	}
	for _, pr := range deref(p.Projects) {
		c.info("\nProject %s → %s (%s)", pr.Name, pr.Slug, coolifyAction(string(pr.Action), pr.Collision))
		for _, e := range deref(pr.Environments) {
			c.info("  Environment %s (%s)", e.Slug, string(e.Action))
			rows := [][]string{}
			for _, r := range deref(e.Resources) {
				state := ""
				if !r.Include {
					state = " (not imported: " + deref(r.IncludeNote) + ")"
				}
				port := "-"
				if deref(r.Port) > 0 {
					port = strconv.FormatInt(*r.Port, 10)
				}
				var hosts []string
				for _, d := range deref(r.Domains) {
					if d.Enabled {
						hosts = append(hosts, d.Host)
					}
				}
				domain := "-"
				if len(hosts) > 0 {
					domain = strings.Join(hosts, ",")
				}
				secrets := 0
				vars := 0
				for _, v := range deref(r.Env) {
					if !deref(v.Skipped) {
						vars++
						if v.Secret {
							secrets++
						}
					}
				}
				rows = append(rows, []string{r.Slug, string(r.Kind), coolifySource(r) + state, port, domain, fmt.Sprintf("%d (%d secret)", vars, secrets)})
			}
			c.table([]string{"  SERVICE", "KIND", "SOURCE", "PORT", "DOMAINS", "VARIABLES"}, rows)
			for _, r := range deref(e.Resources) {
				if r.Include {
					printCoolifyResourceDetails(c, r)
				}
			}
		}
	}
	if rw := deref(p.Rewrites); len(rw) > 0 {
		c.info("\nRewritten references (values are never shown):")
		for _, r := range rw {
			c.info("  %s.%s [%s] → %s", r.Resource, r.Key, string(r.Kind), r.To)
		}
	}
	if un := deref(p.Unsupported); len(un) > 0 {
		c.info("\nNot imported:")
		for _, u := range un {
			who := string(u.Kind) + " " + u.Name
			if deref(u.Project) != "" {
				who += " (" + *u.Project + ")"
			}
			c.info("  %s: %s", who, u.Reason)
		}
	}
	if regs := deref(p.Registries); len(regs) > 0 {
		c.info("\nRegistries that need a pull credential:")
		for _, r := range regs {
			state := "missing: --registry-token-env " + r.Host + "=<ENV_VAR>"
			if r.Provided {
				state = "provided"
			}
			user := ""
			if deref(r.Username) != "" {
				user = " (Coolify logs in as " + *r.Username + ")"
			}
			c.info("  %s%s: %s; used by %s", r.Host, user, state, strings.Join(deref(r.Services), ", "))
		}
	}
	for _, ch := range deref(p.Checks) {
		c.info("  [%s] %s", string(ch.Status), ch.Message)
	}
	for _, w := range deref(p.Warnings) {
		c.info("  [warning] %s", w)
	}
	for _, b := range deref(p.Blockers) {
		c.info("  [blocked] %s", b)
	}
	t := p.Totals
	c.info("\n%d project(s), %d service(s), %d database(s), %d variable(s) (%d secret), %d domain(s).", t.Projects, t.Services, t.Databases, t.Variables, t.Secrets, t.Domains)
	if fu := deref(p.Followups); len(fu) > 0 {
		c.info("\nAfter the import:")
		for _, s := range fu {
			c.info("  - %s", s)
		}
	}
}

func coolifyAction(action string, collision bool) string {
	if collision && action == "create" {
		return "new project, the name was taken"
	}
	return action
}

func coolifySource(r apiclient.CoolifyPlanResource) string {
	s := r.Source
	switch {
	case s.Managed != nil:
		m := s.Managed
		out := "managed " + m.Engine + " " + m.Version
		if deref(m.Flavor) != "" {
			out += " (" + *m.Flavor + ")"
		}
		return out
	case s.Git != nil:
		return "git " + s.Git.Url + "@" + s.Git.Branch + " (" + s.Git.BuildType + ")"
	case s.Image != nil:
		return *s.Image
	}
	return string(s.Type)
}

func printCoolifyResourceDetails(c *cli, r apiclient.CoolifyPlanResource) {
	// variables by key only: * secret, → rewritten to a reference, ? value missing, ~ skipped
	if env := deref(r.Env); len(env) > 0 {
		keys := make([]string, 0, len(env))
		for _, v := range env {
			k := v.Key
			switch {
			case deref(v.Skipped) && deref(v.NeedsValue):
				k += "?"
			case deref(v.Skipped):
				k += "~"
			default:
				if v.Secret {
					k += "*"
				}
				if deref(v.Rewritten) {
					k += "→"
				}
				if v.Scope != "runtime" {
					k += "[" + string(v.Scope) + "]"
				}
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.info("    %s variables: %s", r.Slug, strings.Join(keys, " "))
		c.info("      (* secret, → reference to another service, ? needs a value (--set-env), ~ not imported, [build|both] build-time)")
	}
	if len(deref(r.DependsOn)) > 0 {
		c.info("    %s deploys after: %s", r.Slug, strings.Join(deref(r.DependsOn), ", "))
	}
	for _, n := range deref(r.Notes) {
		if string(n.Severity) == "info" {
			continue
		}
		c.info("    [%s] %s: %s: %s", string(n.Severity), r.Slug, n.Key, n.Message)
	}
	if m := r.Source.Managed; m != nil {
		for _, n := range deref(m.Notes) {
			c.info("    [note] %s: %s", r.Slug, n)
		}
	}
	if nu := deref(r.NeedsUnrestricted); len(nu) > 0 {
		c.info("    %s needs an unrestricted server for: %s", r.Slug, strings.Join(nu, ", "))
	}
}
