package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newImportCmd(c *cli) *cobra.Command {
	return group("import", "Import a project from another tool", newImportComposeCmd(c), newImportCoolifyCmd(c))
}

// composeFlags are the options of `import compose`.
type composeFlags struct {
	project, env, server, name              string
	envFile                                 string
	files                                   []string
	repo, repoURL, connection, branch, path string
	planOnly, deploy, asStack               bool

	include, exclude, managed, noManaged  []string
	publish, git, image, domain           []string
	port, convertBind                     []string
	secret, notSecret, skipVar, noRewrite []string
}

func newImportComposeCmd(c *cli) *cobra.Command {
	var f composeFlags
	cmd := &cobra.Command{
		Use:   "compose <docker-compose.yml|-> --project <p> --env <e> --server <s> [flags]",
		Short: "Import a Docker Compose project as staged services",
		Long: "Reads a Compose file (a local file, stdin with -, or a file in a git repository with --repo) and stages one NebulaCtrl service per\n" +
			"Compose service in the environment, grouped on the canvas and linked in deploy order. Nothing is deployed unless you pass --deploy.\n\n" +
			"Files next to the Compose file (.env, env_file entries, bind-mounted config files) are read from disk. Host names of other\n" +
			"services in variables are rewritten to references, and official database images become managed databases unless you turn that off.\n" +
			"Use --plan to review without changing anything. Per-service decisions are written as <service>=<value> or <service>.<KEY>.\n\n" +
			"With --as-stack the whole file becomes ONE service (a native Compose stack): the file is kept as written, the .env entries become the\n" +
			"stack's variables (used for ${VAR} interpolation only), and domains are suggested per component. A stack runs under the safe\n" +
			"allow-list (no privileged mode, host network, devices, Docker socket or host bind mounts, no build:) unless an instance admin made\n" +
			"the server unrestricted; violations are listed and block the import. In stack mode --secret, --not-secret and\n" +
			"--skip-var take the variable name (<KEY>); --domain and --port take the component name.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runImportCompose(c, cmd, &f, args[0]) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.project, "project", "", "project ID, slug or name")
	fl.StringVar(&f.env, "env", "", "environment ID, slug or name")
	fl.StringVar(&f.server, "server", "", "server ID or name the services run on")
	fl.StringVar(&f.name, "name", "", "canvas group title (default: the Compose project name)")
	fl.StringVar(&f.envFile, "env-file", "", "the .env file for ${VAR} interpolation (default: .env next to the Compose file)")
	fl.StringArrayVar(&f.files, "file", nil, "supply a referenced file: <path as written in Compose>=<local file> (repeatable)")
	fl.StringVar(&f.repo, "repo", "", "read the Compose file from owner/name of a git connection (with --connection)")
	fl.StringVar(&f.connection, "connection", "", "git connection ID or name for --repo")
	fl.StringVar(&f.repoURL, "repo-url", "", "read the Compose file from a public github.com or gitlab.com repository URL")
	fl.StringVar(&f.branch, "branch", "", "branch of --repo or --repo-url (default: the default branch)")
	fl.StringVar(&f.path, "path", "", "path of the Compose file in the repository (default: docker-compose.yml, compose.yaml, ...)")
	fl.BoolVar(&f.planOnly, "plan", false, "show what would be imported and change nothing")
	fl.BoolVar(&f.asStack, "as-stack", false, "run the whole file as one native Compose stack (one service, the file kept as written) instead of splitting it into services")
	fl.BoolVar(&f.deploy, "deploy", false, "deploy the new services right away")
	fl.StringArrayVar(&f.include, "include", nil, "<service>: also import a service that Compose gates behind a profile (repeatable)")
	fl.StringArrayVar(&f.exclude, "exclude", nil, "<service>: do not import this service (repeatable)")
	fl.StringArrayVar(&f.managed, "managed", nil, "<service>: use a managed database (repeatable; needed to accept the licence of Redis and MongoDB)")
	fl.StringArrayVar(&f.noManaged, "no-managed", nil, "<service>: keep an official database image as a plain image service (repeatable)")
	fl.StringArrayVar(&f.publish, "publish", nil, "<service>=<port>/<tcp|udp>: publish a container port through a TCP/UDP route (repeatable)")
	fl.StringArrayVar(&f.git, "git", nil, "<service>=<https url>[#branch]: build a service from this repository (repeatable)")
	fl.StringArrayVar(&f.image, "image", nil, "<service>=<image>: pull this image instead of building (repeatable)")
	fl.StringArrayVar(&f.domain, "domain", nil, "<service>=<hostname>|auto|off: set the domain (repeatable)")
	fl.StringArrayVar(&f.port, "port", nil, "<service>=<port>: the service port (repeatable)")
	fl.StringArrayVar(&f.convertBind, "convert-bind", nil, "<service>=<container path>: turn a directory bind mount into a named volume (repeatable)")
	fl.StringArrayVar(&f.secret, "secret", nil, "<service>.<KEY>: store the variable as a secret (repeatable)")
	fl.StringArrayVar(&f.notSecret, "not-secret", nil, "<service>.<KEY>: store the variable in plain text (repeatable)")
	fl.StringArrayVar(&f.skipVar, "skip-var", nil, "<service>.<KEY>: do not import the variable (repeatable)")
	fl.StringArrayVar(&f.noRewrite, "no-rewrite", nil, "<service>.<KEY>: keep the host names in the variable as they are (repeatable)")
	return cmd
}

// choices builds the per-service decisions from the flags.
func (f *composeFlags) choices() (apiclient.ComposeChoices, error) {
	svcs := map[string]apiclient.ComposeServiceChoice{}
	get := func(k string) apiclient.ComposeServiceChoice { return svcs[k] }
	t, fl := true, false
	if f.asStack {
		for _, n := range []struct {
			flag string
			vals []string
		}{
			{"include", f.include}, {"exclude", f.exclude}, {"managed", f.managed}, {"no-managed", f.noManaged}, {"publish", f.publish},
			{"git", f.git}, {"image", f.image}, {"convert-bind", f.convertBind}, {"no-rewrite", f.noRewrite},
		} {
			if len(n.vals) > 0 {
				return apiclient.ComposeChoices{}, usagef("--%s does not apply with --as-stack: the stack runs the Compose file as written", n.flag)
			}
		}
	}
	for _, k := range f.include {
		s := get(k)
		s.Include = &t
		svcs[k] = s
	}
	for _, k := range f.exclude {
		s := get(k)
		s.Include = &fl
		svcs[k] = s
	}
	for _, k := range f.managed {
		s := get(k)
		s.ManagedDatabase = &t
		svcs[k] = s
	}
	for _, k := range f.noManaged {
		s := get(k)
		s.ManagedDatabase = &fl
		svcs[k] = s
	}
	kv := func(flag string, vals []string, apply func(s *apiclient.ComposeServiceChoice, v string) error) error {
		for _, raw := range vals {
			k, v, ok := strings.Cut(raw, "=")
			if !ok || k == "" || v == "" {
				return usagef("--%s %q: use <service>=<value>", flag, raw)
			}
			s := get(k)
			if err := apply(&s, v); err != nil {
				return usagef("--%s %q: %v", flag, raw, err)
			}
			svcs[k] = s
		}
		return nil
	}
	if err := kv("publish", f.publish, func(s *apiclient.ComposeServiceChoice, v string) error {
		if p, proto, ok := strings.Cut(v, "/"); !ok || (proto != "tcp" && proto != "udp") || func() bool { _, err := strconv.Atoi(p); return err != nil }() {
			return fmt.Errorf("use <port>/<tcp|udp>")
		}
		l := append(deref(s.PublishPorts), v)
		s.PublishPorts = &l
		return nil
	}); err != nil {
		return apiclient.ComposeChoices{}, err
	}
	if err := kv("git", f.git, func(s *apiclient.ComposeServiceChoice, v string) error {
		url, branch, _ := strings.Cut(v, "#")
		s.GitUrl = &url
		if branch != "" {
			s.GitBranch = &branch
		}
		src := apiclient.ComposeServiceChoiceSource("git")
		s.Source = &src
		return nil
	}); err != nil {
		return apiclient.ComposeChoices{}, err
	}
	if err := kv("image", f.image, func(s *apiclient.ComposeServiceChoice, v string) error {
		s.Image = &v
		src := apiclient.ComposeServiceChoiceSource("image")
		s.Source = &src
		return nil
	}); err != nil {
		return apiclient.ComposeChoices{}, err
	}
	if err := kv("domain", f.domain, func(s *apiclient.ComposeServiceChoice, v string) error {
		switch v {
		case "off":
			s.Domain = &apiclient.ComposeDomainChoice{Enabled: false}
		case "auto":
			s.Domain = &apiclient.ComposeDomainChoice{Enabled: true}
		default:
			s.Domain = &apiclient.ComposeDomainChoice{Enabled: true, Host: &v}
		}
		return nil
	}); err != nil {
		return apiclient.ComposeChoices{}, err
	}
	if err := kv("port", f.port, func(s *apiclient.ComposeServiceChoice, v string) error {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("not a port")
		}
		s.Port = &n
		return nil
	}); err != nil {
		return apiclient.ComposeChoices{}, err
	}
	if err := kv("convert-bind", f.convertBind, func(s *apiclient.ComposeServiceChoice, v string) error {
		l := append(deref(s.ConvertBinds), v)
		s.ConvertBinds = &l
		return nil
	}); err != nil {
		return apiclient.ComposeChoices{}, err
	}
	vars := map[string]apiclient.ComposeEnvChoice{}
	envChoice := func(flag string, vals []string, apply func(e *apiclient.ComposeEnvChoice)) error {
		for _, raw := range vals {
			if f.asStack {
				// A stack has one set of variables (the .env entries): <KEY>.
				if raw == "" {
					return usagef("--%s %q: use <KEY>", flag, raw)
				}
				e := vars[raw]
				apply(&e)
				vars[raw] = e
				continue
			}
			svc, key, ok := strings.Cut(raw, ".")
			if !ok || svc == "" || key == "" {
				return usagef("--%s %q: use <service>.<KEY>", flag, raw)
			}
			s := get(svc)
			m := deref(s.Env)
			if m == nil {
				m = map[string]apiclient.ComposeEnvChoice{}
			}
			e := m[key]
			apply(&e)
			m[key] = e
			s.Env = &m
			svcs[svc] = s
		}
		return nil
	}
	for _, step := range []struct {
		flag  string
		vals  []string
		apply func(e *apiclient.ComposeEnvChoice)
	}{
		{"secret", f.secret, func(e *apiclient.ComposeEnvChoice) { e.Secret = &t }},
		{"not-secret", f.notSecret, func(e *apiclient.ComposeEnvChoice) { e.Secret = &fl }},
		{"skip-var", f.skipVar, func(e *apiclient.ComposeEnvChoice) { e.Skip = &t }},
		{"no-rewrite", f.noRewrite, func(e *apiclient.ComposeEnvChoice) { e.Rewrite = &fl }},
	} {
		if err := envChoice(step.flag, step.vals, step.apply); err != nil {
			return apiclient.ComposeChoices{}, err
		}
	}
	if f.asStack {
		return apiclient.ComposeChoices{Services: &svcs, Variables: &vars}, nil
	}
	return apiclient.ComposeChoices{Services: &svcs}, nil
}

// source reads the Compose file and its neighbours.
func (f *composeFlags) source(ctx context.Context, c *cli, arg string) (apiclient.ComposeSource, string, error) {
	src := apiclient.ComposeSource{}
	if f.name != "" {
		src.Name = &f.name
	}
	dir := "."
	switch {
	case f.repo != "" || f.repoURL != "":
		if arg != "-" && arg != "" && arg != "." {
			return src, "", usagef("with --repo or --repo-url the file argument is the path in the repository; pass - and use --path")
		}
		ref := apiclient.ComposeRepoRef{}
		if f.repo != "" {
			ref.Repo = &f.repo
			if f.connection == "" {
				return src, "", usagef("--repo needs --connection (ID or name of a git connection)")
			}
			id, err := c.resolveGitConnection(ctx, f.connection)
			if err != nil {
				return src, "", err
			}
			ref.ConnectionId = &id
		} else {
			ref.Url = &f.repoURL
		}
		if f.branch != "" {
			ref.Branch = &f.branch
		}
		if f.path != "" {
			ref.Path = &f.path
		}
		src.Repo = &ref
		return src, dir, nil
	case arg == "-":
		raw, err := io.ReadAll(io.LimitReader(c.in, 1<<20))
		if err != nil {
			return src, "", err
		}
		s := string(raw)
		src.Compose = &s
	default:
		raw, err := os.ReadFile(arg)
		if err != nil {
			return src, "", err
		}
		s := string(raw)
		src.Compose = &s
		dir = filepath.Dir(arg)
	}
	envPath := f.envFile
	if envPath == "" {
		if p := filepath.Join(dir, ".env"); fileExists(p) {
			envPath = p
		}
	}
	if envPath != "" {
		raw, err := os.ReadFile(envPath)
		if err != nil {
			return src, "", err
		}
		s := string(raw)
		src.Env = &s
	}
	files := map[string]string{}
	for _, spec := range f.files {
		k, v, ok := strings.Cut(spec, "=")
		if !ok || k == "" || v == "" {
			return src, "", usagef("--file %q: use <path as written in Compose>=<local file>", spec)
		}
		raw, err := os.ReadFile(v)
		if err != nil {
			return src, "", err
		}
		files[k] = string(raw)
	}
	src.Files = &files
	return src, dir, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (c *cli) resolveGitConnection(ctx context.Context, ref string) (string, error) {
	r, err := c.api.ListGitConnectionsWithResponse(ctx)
	if err := check(r, err); err != nil {
		return "", err
	}
	for _, g := range itemsOrEmpty(r.JSON200.Items) {
		if g.Id == ref || strings.EqualFold(g.Name, ref) {
			return g.Id, nil
		}
	}
	return "", fmt.Errorf("git connection %q not found (see Team settings → Integrations)", ref)
}

func runImportCompose(c *cli, cmd *cobra.Command, f *composeFlags, arg string) error {
	if err := c.connect(); err != nil {
		return err
	}
	ctx, cancel := c.ctx(cmd.Context())
	defer cancel()
	envID, serverID, err := c.templateTarget(ctx, f.project, f.env, f.server, !f.planOnly)
	if err != nil {
		return err
	}
	src, dir, err := f.source(ctx, c, arg)
	if err != nil {
		return err
	}
	choices, err := f.choices()
	if err != nil {
		return err
	}

	// Plan, reading files the plan asks for from next to the Compose file.
	var plan *apiclient.ComposePlan
	for round := 0; ; round++ {
		var mode *apiclient.ComposePlanRequestMode
		if f.asStack {
			m := apiclient.ComposePlanRequestModeStack
			mode = &m
		}
		r, err := c.api.PlanComposeImportWithResponse(ctx, envID, apiclient.ComposePlanRequest{Source: src, Choices: choices, ServerId: serverID, Mode: mode})
		if err := check(r, err); err != nil {
			return err
		}
		plan = r.JSON200
		if src.Repo != nil || round >= 3 {
			break
		}
		added := false
		files := deref(src.Files)
		if files == nil {
			files = map[string]string{}
		}
		for _, nf := range deref(plan.NeededFiles) {
			// Never read outside the Compose file's directory: the paths come from the plan.
			if _, have := files[nf.Path]; have || !filepath.IsLocal(filepath.FromSlash(nf.Path)) {
				continue
			}
			local := filepath.Join(dir, filepath.FromSlash(nf.Path))
			if raw, err := os.ReadFile(local); err == nil {
				files[nf.Path] = string(raw)
				added = true
			}
		}
		if !added {
			break
		}
		src.Files = &files
	}

	if !c.asJSON {
		printComposePlan(c, plan)
	}
	if f.planOnly {
		if c.asJSON {
			return c.printJSON(plan)
		}
		if !plan.Installable {
			return &exitError{code: 1}
		}
		return nil
	}
	if !plan.Installable {
		return fmt.Errorf("the project can't be imported yet; fix the blockers above (see --help for the flags that resolve them)")
	}

	req := apiclient.ComposeImportRequest{Source: src, Choices: choices, ServerId: *serverID}
	stackComponents := 0
	if f.asStack {
		mode := apiclient.ComposeImportRequestModeStack
		req.Mode = &mode
		stackComponents = len(deref(plan.Stack.Components))
	}
	if f.deploy {
		req.Deploy = &f.deploy
	}
	r, err := c.api.ImportComposeWithResponse(ctx, envID, &apiclient.ImportComposeParams{IdempotencyKey: c.key()}, req)
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
	c.info("")
	if f.asStack {
		c.info("Imported %q as a stack: service %s, %d components.", res.Name, strings.Join(slugs, ", "), stackComponents)
	} else {
		c.info("Imported %q: %d service(s): %s", res.Name, len(slugs), strings.Join(slugs, ", "))
	}
	if f.deploy {
		c.info("Started %d deployments; follow them with: nebulactrl deployments status <id>", len(deref(res.Deployments)))
	} else {
		c.info("The services are staged on the canvas; review them, then deploy with `nebulactrl services deploy <service> --wait` or from the staged bar.")
	}
	for _, w := range deref(res.Warnings) {
		c.note("Warning: %s", w)
	}
	return nil
}

func printComposePlan(c *cli, p *apiclient.ComposePlan) {
	if p.Mode == apiclient.ComposePlanMode("stack") && p.Stack != nil {
		printStackPlan(c, p.Stack)
	} else {
		printSplitPlan(c, p)
	}
	printPlanTail(c, p)
}

func printSplitPlan(c *cli, p *apiclient.ComposePlan) {
	rows := [][]string{}
	for _, s := range deref(p.Services) {
		src := string(s.Source.Type)
		switch {
		case s.Managed != nil && s.Managed.Enabled:
			src = "managed " + deref(s.Managed.Engine) + " " + deref(s.Managed.Version)
		case s.Source.Image != nil:
			src = *s.Source.Image
		case s.Source.Build != nil:
			src = "build " + deref(s.Source.Build.GitUrl) + " " + deref(s.Source.Build.DockerfilePath)
		}
		domain := "-"
		for _, d := range deref(s.Domains) {
			if d.Enabled {
				domain = deref(d.Host)
				if d.Generate {
					domain = "generated"
				}
			}
		}
		port := "-"
		if s.Port > 0 {
			port = strconv.FormatInt(s.Port, 10)
		}
		state := ""
		if !s.Include {
			state = " (not imported: " + deref(s.IncludeNote) + ")"
		}
		rows = append(rows, []string{s.Slug, string(s.Kind), src + state, port, domain, fmt.Sprint(len(deref(s.Volumes))), fmt.Sprint(len(deref(s.Files)))})
	}
	c.table([]string{"SERVICE", "KIND", "SOURCE", "PORT", "DOMAIN", "VOLUMES", "FILES"}, rows)

	if rw := deref(p.Rewrites); len(rw) > 0 {
		c.info("\nRewritten references:")
		for _, r := range rw {
			c.info("  %s.%s [%s]: %s -> %s", r.Service, r.Key, r.Kind, r.From, r.To)
		}
	}
	for _, s := range deref(p.Services) {
		if !s.Include {
			continue
		}
		for _, n := range deref(s.Notes) {
			if n.Severity == "info" {
				continue
			}
			c.info("  [%s] %s: %s", n.Severity, n.Key, n.Message)
		}
		if m := s.Managed; m != nil && m.Eligible && !m.Enabled {
			c.info("  [info] %s: managed %s available. %s", s.Key, deref(m.Engine), deref(m.Reason))
		}
		for _, b := range deref(s.Binds) {
			if b.Status == "rejected" || b.Status == "missing" {
				c.info("  [warning] %s: %s -> %s: %s", s.Key, b.Source, b.Target, deref(b.Reason))
			}
		}
	}
}

// printStackPlan lists what a stack-mode import would create: the components, the
// variables and the allow-list violations (the blockers that repeat them are skipped
// by printPlanTail).
func printStackPlan(c *cli, s *apiclient.ComposeStackPlan) {
	c.info("Stack %q (service %s): the compose file is kept as written and runs as one service.", s.Name, s.Slug)
	rows := [][]string{}
	for _, comp := range deref(s.Components) {
		image := deref(comp.Image)
		if image == "" {
			image = "-"
		}
		ports := []string{}
		for _, p := range deref(comp.Ports) {
			ports = append(ports, strconv.FormatInt(p, 10))
		}
		domain := "-"
		for _, d := range deref(comp.Domains) {
			if d.Enabled {
				domain = deref(d.Host)
				if d.Generate {
					domain = "generated"
				}
				domain += fmt.Sprintf(" -> :%d", comp.Port)
			}
		}
		starts := "on deploy"
		if comp.Action == "profile" {
			starts = "only with a profile"
		}
		rows = append(rows, []string{comp.Name, image, strings.Join(ports, ","), domain, starts})
	}
	c.table([]string{"COMPONENT", "IMAGE", "PORTS", "DOMAIN", "STARTS"}, rows)
	for _, comp := range deref(s.Components) {
		for _, n := range deref(comp.Notes) {
			c.info("  [info] %s: %s", comp.Name, n)
		}
	}
	if vars := deref(s.Variables); len(vars) > 0 {
		c.info("\nVariables (used for ${VAR} interpolation only):")
		for _, v := range vars {
			switch {
			case v.Skipped:
				c.info("  %s: skipped. %s", v.Key, deref(v.Note))
			case v.Secret:
				c.info("  %s: secret", v.Key)
			default:
				c.info("  %s", v.Key)
			}
		}
	}
	for _, v := range deref(s.Violations) {
		c.info("  [%s] %s", v.Kind, violationText(v))
	}
	for _, n := range deref(s.Notes) {
		c.info("  [info] %s", n)
	}
}

func violationText(v apiclient.Violation) string {
	if v.Service == nil || *v.Service == "" {
		return fmt.Sprintf("%s: %s", v.Key, v.Message)
	}
	return fmt.Sprintf("service %s: %s: %s", *v.Service, v.Key, v.Message)
}

func printPlanTail(c *cli, p *apiclient.ComposePlan) {
	for _, n := range deref(p.Notes) {
		c.info("  [%s] %s: %s", n.Severity, n.Key, n.Message)
	}
	for _, w := range deref(p.Warnings) {
		c.info("  [warning] %s", w)
	}
	for _, v := range deref(p.Missing) {
		c.info("  [warning] ${%s} has no value; an empty string was used", v)
	}
	for _, ch := range deref(p.Checks) {
		c.info("  [%s] %s", ch.Status, ch.Message)
	}
	shown := map[string]bool{}
	if p.Stack != nil {
		for _, v := range deref(p.Stack.Violations) {
			shown[violationText(v)] = true
		}
	}
	for _, b := range deref(p.Blockers) {
		if !shown[b] {
			c.info("  [blocked] %s", b)
		}
	}
}
