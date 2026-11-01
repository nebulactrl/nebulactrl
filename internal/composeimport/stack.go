package composeimport

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

// Import modes.
const (
	// ModeSplit turns every Compose service into a service of its own (the default).
	ModeSplit = "split"
	// ModeStack runs the whole Compose file as one native Compose stack.
	ModeStack = "stack"
)

// ComposeStackComponent is one Compose service of a stack and what happens to it.
type ComposeStackComponent struct {
	Name  string `json:"name" doc:"The service name in the Compose file; also the component's DNS name on the environment network"`
	Index int    `json:"index" doc:"Position among the components sorted by name"`
	Image string `json:"image,omitempty"`
	// Ports are the container ports the file declares (ports and expose).
	Ports    []int    `json:"ports"`
	Profiles []string `json:"profiles"`
	// Action is what a deploy does with the component.
	Action string `json:"action" enum:"run,profile" doc:"run: started by every deploy; profile: has Compose profiles and is not started by a default deploy"`
	// Port is the HTTP port a domain would target; 0 when the component has no HTTP-like port.
	Port int `json:"port"`
	// Domains are the suggested hostnames; only enabled ones are created.
	Domains []ComposePlanDomain `json:"domains"`
	Notes   []string            `json:"notes"`
}

// ComposeStackPlan is the stack-mode part of a plan.
type ComposeStackPlan struct {
	Name string `json:"name" doc:"Name of the stack service"`
	Slug string `json:"slug" doc:"The slug the stack will get"`
	// Components are the Compose services, sorted by name.
	Components []ComposeStackComponent `json:"components"`
	// Variables are the .env entries; the stack uses them for Compose interpolation only.
	Variables []ComposePlanEnv `json:"variables"`
	// Violations are the allow-list findings of the file as written.
	Violations []composestack.Violation `json:"violations"`
	Notes      []string                 `json:"notes"`
	// Unrestricted is the chosen server's Unrestricted Compose flag (false without a server).
	Unrestricted bool `json:"unrestricted"`
	// ComposeAvailable says whether the chosen server's agent has the Docker Compose plugin;
	// null without a server or before the agent reported its facts.
	ComposeAvailable *bool `json:"composeAvailable"`
}

// stackBuild is a checked stack-mode analysis.
type stackBuild struct {
	plan   ComposeStackPlan
	vars   []ComposePlanEnv
	checks []ComposeCheck
	// compose is the file as given; it is stored verbatim.
	compose string
}

// reservedComposeEnv are the variable names compose never receives from a stack
// (the agent drops DOCKER_* and COMPOSE_* except COMPOSE_PROFILES).
func reservedComposeEnv(k string) bool {
	return (strings.HasPrefix(k, "DOCKER_") || strings.HasPrefix(k, "COMPOSE_")) && k != "COMPOSE_PROFILES"
}

// buildStack analyses the Compose file for stack mode: components, suggested
// domains, the .env variables, the allow-list violations and the preflight checks.
func (s *Service) buildStack(ctx context.Context, q *db.Queries, a *analysis, ch ComposeChoices) (*stackBuild, error) {
	proj, err := composestack.Parse([]byte(a.compose))
	if err != nil {
		return nil, apierr.Unprocessable("invalid_compose", err.Error())
	}
	unrestricted := a.server != nil && a.server.Unrestricted
	violations := composestack.Check(proj, services.StackPolicy(unrestricted))
	if violations == nil {
		violations = []composestack.Violation{}
	}

	name := a.name
	if a.res != nil {
		name = a.res.groupName
	}
	if name == "" {
		name = "Compose project"
	}
	taken, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: a.env.Env.ID, Base: projects.Slugify(name, "service")})
	if err != nil {
		return nil, fmt.Errorf("list service slugs: %w", err)
	}
	for _, r := range []string{"shared", "team", "vault"} {
		if services.IsReservedSlug(r) {
			taken = append(taken, r)
		}
	}
	plan := ComposeStackPlan{
		Name: name, Slug: projects.UniqueSlug(projects.Slugify(name, "service"), taken), Violations: violations, Unrestricted: unrestricted,
		Variables: []ComposePlanEnv{}, Notes: []string{},
	}
	if a.server != nil {
		var f agentprotocol.Facts
		if len(a.server.Facts) > 0 && json.Unmarshal(a.server.Facts, &f) == nil && (f.ComposeVersion != "" || len(f.Capabilities) > 0) {
			ok := slices.Contains(f.Capabilities, agentprotocol.CapCompose)
			plan.ComposeAvailable = &ok
		}
	}

	suggested := map[string]ComposePlanService{}
	if a.res != nil {
		for _, ps := range a.res.plan.Services {
			suggested[ps.Key] = ps
		}
	} else if a.splitErr != nil {
		plan.Notes = append(plan.Notes, "Domains could not be suggested: "+a.splitErr.Error()+". Add them after the import.")
	}
	for _, c := range proj.Components() {
		plan.Components = append(plan.Components, stackComponent(c, suggested[c.Name], suggested[c.Name].Key != ""))
	}
	if plan.Components == nil {
		plan.Components = []ComposeStackComponent{}
	}

	vars, notes := stackVariables(a.dotenv, ch)
	plan.Variables, plan.Notes = vars, append(plan.Notes, notes...)
	b := &stackBuild{plan: plan, vars: vars, compose: a.compose}
	if !unrestricted && len(composestack.Restricted(violations)) > 0 {
		b.plan.Notes = append(b.plan.Notes, "An instance admin can enable Unrestricted Compose for the server to lift the allow-list.")
	}
	b.checks, err = s.stackChecks(ctx, q, a, b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func stackComponent(c agentprotocol.StackComponent, ps ComposePlanService, found bool) ComposeStackComponent {
	out := ComposeStackComponent{
		Name: c.Name, Index: c.Index, Image: c.Image, Ports: c.Ports, Profiles: c.Profiles, Action: "run",
		Domains: []ComposePlanDomain{}, Notes: []string{},
	}
	if out.Ports == nil {
		out.Ports = []int{}
	}
	if out.Profiles == nil {
		out.Profiles = []string{}
	}
	if len(c.Profiles) > 0 {
		out.Action = "profile"
		out.Notes = append(out.Notes, "Has Compose profiles ("+strings.Join(c.Profiles, ", ")+"), so a deploy does not start it unless COMPOSE_PROFILES selects one.")
	}
	if !found {
		return out
	}
	if out.Action == "run" && ps.PortHTTP && ps.Port > 0 {
		out.Port = ps.Port
		out.Domains = append(out.Domains, ps.Domains...)
	}
	for _, p := range ps.Ports {
		if p.Role != "http" && p.HostPort > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("Port %d/%s is not published on the host: stacks only route HTTP domains to their components.", p.HostPort, p.Protocol))
		}
	}
	return out
}

// stackVariables lists the .env entries as stack variables. They are used for
// Compose interpolation only and are never rewritten.
func stackVariables(dotenv string, ch ComposeChoices) ([]ComposePlanEnv, []string) {
	vals, dotenvNotes := ParseDotenv(dotenv, nil)
	var notes []string
	for _, n := range dotenvNotes {
		notes = append(notes, ".env: "+n)
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ComposePlanEnv, 0, len(keys))
	for _, k := range keys {
		v := vals[k]
		e := ComposePlanEnv{Key: k, Value: v, Origin: "dotenv", raw: v}
		switch {
		case !validEnvKey(k):
			e.Skipped, e.Note = true, "Not a valid variable name."
		case reservedComposeEnv(k):
			e.Skipped, e.Note = true, "Compose never receives DOCKER_* or COMPOSE_* variables from a stack."
		default:
			guess := looksSecret(k, v)
			e.Secret, e.SecretGuess = guess, guess
			if c, ok := ch.Variables[k]; ok {
				if c.Secret != nil {
					e.Secret, e.SecretGuess = *c.Secret, false
				}
				e.Skipped = c.Skip
			}
		}
		if e.Secret {
			e.Value = ""
		}
		out = append(out, e)
	}
	return out, notes
}

// stackChecks are the preflight checks of a stack import.
func (s *Service) stackChecks(ctx context.Context, q *db.Queries, a *analysis, b *stackBuild) ([]ComposeCheck, error) {
	checks := []ComposeCheck{}
	add := func(id, status, msg string) {
		checks = append(checks, ComposeCheck{ID: id, Status: status, Message: msg})
	}
	secrets := 0
	for _, e := range b.vars {
		if e.Secret && !e.Skipped {
			secrets++
		}
	}
	if secrets > 0 {
		add("secrets", CheckOK, fmt.Sprintf("%d variable(s) will be stored encrypted as secrets; their values are never shown again.", secrets))
	}
	for _, c := range b.plan.Components {
		rows, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: a.env.Env.ID, Base: c.Name})
		if err != nil {
			return nil, fmt.Errorf("list service slugs: %w", err)
		}
		if slices.Contains(rows, c.Name) {
			add("alias-"+c.Name, CheckWarn, fmt.Sprintf("The component %s has the same name as a service of this environment; one of them has to be renamed before the stack can deploy.", c.Name))
		}
	}
	srv := a.server
	if srv == nil {
		add("server", CheckWarn, "Choose a server to check the allow-list, domains and the Docker Compose plugin. Without one, every restricted option blocks the import.")
		return checks, nil
	}
	if stop := serverStatusCheck(srv, add); stop {
		return checks, nil
	}
	if ok := b.plan.ComposeAvailable; ok != nil && !*ok {
		add("compose", CheckWarn, fmt.Sprintf("The agent on %s does not report the Docker Compose plugin (2.24 or newer). The stack is staged, but deploying needs it.", srv.Name))
	}
	wantsDomain := false
	for _, c := range b.plan.Components {
		for _, d := range c.Domains {
			if !d.Enabled {
				continue
			}
			wantsDomain = true
			if d.Host == "" {
				continue
			}
			h, err := domains.ValidateHostname(d.Host)
			if err != nil {
				add("domain", CheckFail, fmt.Sprintf("%s: %v", c.Name, err))
				continue
			}
			if n, err := q.CountDomainsByHostname(ctx, h); err == nil && n > 0 {
				add("domain", CheckFail, fmt.Sprintf("The hostname %s is already in use. Turn the domain of %s off or change it.", h, c.Name))
			}
		}
	}
	if wantsDomain {
		if srv.ProxyStatus != nil && *srv.ProxyStatus == agentprotocol.ProxyError {
			add("proxy", CheckFail, fmt.Sprintf("The proxy on %s is not running; domains cannot be attached.", srv.Name))
		}
		if _, ok := domains.BaseDomain(*srv); !ok {
			add("domain", CheckWarn, "The server has neither a base domain nor a public IP, so generated hostnames are skipped. Add domains after the import.")
		}
	}
	return checks, nil
}

// planStack is Plan for stack mode: nothing is written.
func (s *Service) planStack(ctx context.Context, q *db.Queries, a *analysis, ch ComposeChoices) (ComposePlan, error) {
	b, err := s.buildStack(ctx, q, a, ch)
	if err != nil {
		return ComposePlan{}, err
	}
	p := ComposePlan{
		Name: b.plan.Name, Mode: ModeStack, Stack: &b.plan, Services: []ComposePlanService{}, Networks: []ComposePlanNetwork{},
		Rewrites: []ComposeRewrite{}, Missing: []string{}, NeededFiles: []ComposeNeededFile{}, Notes: []ComposeNote{},
		Warnings: []string{}, Checks: b.checks, Blockers: []string{}, SourceKind: "paste",
	}
	if a.res != nil {
		p.Missing = a.res.plan.Missing
		p.Warnings = a.res.plan.Warnings
	}
	if a.repo != nil {
		p.SourceKind, p.Commit = "repo", a.repo.Commit
	}
	for _, v := range b.plan.Violations {
		p.Blockers = append(p.Blockers, v.String())
	}
	for _, c := range b.checks {
		if c.Status == CheckFail {
			p.Blockers = append(p.Blockers, c.Message)
		}
	}
	p.Installable = len(p.Blockers) == 0
	return p, nil
}

// importStack is Import for stack mode: one stack service, its variables and
// domains, all staged in one transaction. Nothing deploys unless asked to.
func (s *Service) importStack(ctx context.Context, env services.EnvScope, envID uuid.UUID, req ComposeImportRequest, a *analysis) (ComposeImportResult, error) {
	b, err := s.buildStack(ctx, s.q, a, req.Choices)
	if err != nil {
		return ComposeImportResult{}, err
	}
	if vs := b.plan.Violations; len(vs) > 0 {
		return ComposeImportResult{}, apierr.Unprocessable("invalid_compose", "The compose file cannot run as a stack:\n"+composestack.Summary(vs)).
			WithDetails(map[string]any{"violations": vs})
	}
	for _, c := range b.checks {
		if c.Status == CheckFail {
			return ComposeImportResult{}, apierr.Unprocessable("preflight_failed", c.Message).WithDetails(map[string]any{"checks": b.checks})
		}
	}

	imp := &importer{s: s, env: env, a: a, srv: *a.server, warnings: []string{}}
	var row db.Service
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		imp.warnings = []string{}
		if row, err = imp.createStack(ctx, tx, b); err != nil {
			return err
		}
		hosts, err := imp.stackDomains(ctx, tx, row, b)
		if err != nil {
			return err
		}
		if err := imp.stackVariables(ctx, tx, row, b); err != nil {
			return err
		}
		meta := map[string]any{
			"name": b.plan.Name, "server": imp.srv.Name, "services": 1, "hostnames": hosts, "source": a.sourceKind(), "mode": ModeStack,
			"components": len(b.plan.Components),
		}
		if r := a.repo; r != nil {
			meta["repo"], meta["branch"] = r.Repo, r.Branch
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "compose.import", ResourceType: "environment", ResourceID: env.Env.ID.String(), ProjectID: &env.Env.ProjectID, Metadata: meta,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ComposeImportResult{}, err
		}
		if services.IsUniqueViolation(err) {
			return ComposeImportResult{}, apierr.Conflict("import_conflict", "a hostname of this project was just taken; review the plan and retry")
		}
		return ComposeImportResult{}, fmt.Errorf("import compose stack: %w", err)
	}
	res := ComposeImportResult{Name: b.plan.Name, Rewrites: []ComposeRewrite{}, Warnings: imp.warnings, Deployments: []deployments.DeploymentView{}}
	if err := s.finishImport(ctx, env, envID, []db.Service{row}, req.Deploy, &res); err != nil {
		return ComposeImportResult{}, err
	}
	return res, nil
}

func (a *analysis) sourceKind() string {
	if a.repo != nil {
		return "repo"
	}
	return "paste"
}

// createStack inserts the stack service with the Compose file verbatim.
func (b *importer) createStack(ctx context.Context, tx pgx.Tx, sb *stackBuild) (db.Service, error) {
	serverID := b.srv.ID
	return b.s.Services.CreateInTx(ctx, tx, b.env, services.CreateInput{
		Name: sb.plan.Name, Kind: services.KindStack, SourceType: services.SourceCompose, ServerID: &serverID,
		Config: services.Config{Stack: &services.StackConfig{Compose: sb.compose}},
	})
}

// stackVariables writes the .env entries (never rewritten).
func (b *importer) stackVariables(ctx context.Context, tx pgx.Tx, row db.Service, sb *stackBuild) error {
	for _, e := range sb.vars {
		if e.Skipped {
			continue
		}
		if b.s.Variables == nil {
			return apierr.Unprocessable("variables_unavailable", "this server build cannot write variables")
		}
		if err := b.s.Variables.UpsertTx(ctx, tx, row.ID, e.Key, e.raw, e.Secret); err != nil {
			return fmt.Errorf("write variable %s: %w", e.Key, err)
		}
	}
	return nil
}

// stackDomains stages the enabled suggested domains, one per component and port.
func (b *importer) stackDomains(ctx context.Context, tx pgx.Tx, row db.Service, sb *stackBuild) ([]string, error) {
	withDomain := 0
	for _, c := range sb.plan.Components {
		if slices.ContainsFunc(c.Domains, func(d ComposePlanDomain) bool { return d.Enabled }) {
			withDomain++
		}
	}
	hosts := []string{}
	for _, c := range sb.plan.Components {
		for _, d := range c.Domains {
			if !d.Enabled || c.Port <= 0 {
				continue
			}
			t := domainTarget{port: c.Port, component: c.Name}
			if withDomain > 1 {
				t.slugLabel = row.Slug + "-" + c.Name
			}
			host, warn, err := b.addDomainTo(ctx, tx, row, d, t)
			if err != nil {
				return nil, err
			}
			if warn != "" {
				b.warnings = append(b.warnings, warn)
			}
			if host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts, nil
}
