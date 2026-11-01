package coolify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DatabaseCreator creates a managed database inside the caller's transaction.
type DatabaseCreator interface {
	CreateTx(ctx context.Context, tx pgx.Tx, in databases.CreateInput) (db.Service, error)
}

// Deployer deploys staged services (the deployments package).
type Deployer interface {
	DeployStaged(ctx context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string) (deployments.StagedResult, error)
}

// GitLinker links a service to a connected repository (gitproviders.Service).
type GitLinker interface {
	Link(ctx context.Context, serviceID uuid.UUID, in gitproviders.LinkInput) (gitproviders.GitLinkView, error)
}

// Deps are the collaborators of the import service.
type Deps struct {
	Pool      *pgxpool.Pool
	Bus       *events.Bus
	Jobs      *jobs.Manager
	Services  *services.Service
	Databases DatabaseCreator
	Deploy    Deployer
	Git       GitLinker
	Box       *secretbox.Box
	// HTTP overrides the client used to read Coolify (tests); nil uses the guarded default.
	HTTP   *http.Client
	Logger *slog.Logger
}

// Service implements the Coolify import use cases.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{Deps: d}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// authorize requires projects:write on an unrestricted principal: the import
// creates projects and sends a credential to another host.
func authorize(ctx context.Context) error {
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, nil); err != nil {
		return err
	}
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Restricted() {
		return apierr.Forbidden()
	}
	return nil
}

func (s *Service) load(ctx context.Context, src CoolifySource, serverID *uuid.UUID, choices CoolifyChoices) (*CoolifyPlan, db.Server, error) {
	var srv db.Server
	cl, err := NewClient(src.URL, src.Token, s.HTTP)
	if err != nil {
		return nil, srv, err
	}
	inv, err := cl.Fetch(ctx)
	if err != nil {
		return nil, srv, err
	}
	tg, srv, err := s.loadTarget(ctx, serverID)
	if err != nil {
		return nil, srv, err
	}
	return Analyze(inv, choices, tg), srv, nil
}

// loadTarget reads the projects of the workspace and the chosen server.
func (s *Service) loadTarget(ctx context.Context, serverID *uuid.UUID) (Target, db.Server, error) {
	var srv db.Server
	ws := policy.WorkspaceID(ctx)
	var tg Target
	rows, err := s.q.ListProjects(ctx, db.ListProjectsParams{WorkspaceID: ws, Limit: 5000})
	if err != nil {
		return tg, srv, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range rows {
		tp := TargetProject{ID: p.ID, Name: p.Name, Slug: p.Slug}
		envs, err := s.q.ListEnvironments(ctx, p.ID)
		if err != nil {
			return tg, srv, fmt.Errorf("list environments: %w", err)
		}
		for _, e := range envs {
			te := TargetEnv{ID: e.ID, Name: e.Name, Slug: e.Slug, Production: e.IsProduction}
			svcs, err := s.q.ListAllEnvironmentServices(ctx, e.ID)
			if err != nil {
				return tg, srv, fmt.Errorf("list services: %w", err)
			}
			for _, sv := range svcs {
				te.ServiceSlugs = append(te.ServiceSlugs, sv.Slug)
			}
			tp.Envs = append(tp.Envs, te)
		}
		tg.Projects = append(tg.Projects, tp)
	}
	tg.HostTaken = func(host string) bool {
		n, err := s.q.CountDomainsByHostname(ctx, host)
		return err == nil && n > 0
	}
	if serverID != nil {
		srv, err = s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID, WorkspaceID: &ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return tg, srv, apierr.Unprocessable("server_not_found", "the selected server does not exist")
		}
		if err != nil {
			return tg, srv, fmt.Errorf("load server: %w", err)
		}
		if srv.Status == "revoked" {
			return tg, srv, apierr.Unprocessable("server_revoked", srv.Name+" has been revoked")
		}
		tg.Server = &TargetServer{ID: srv.ID, Name: srv.Name, Unrestricted: srv.Unrestricted}
	}
	return tg, srv, nil
}

// Plan reads Coolify and reports what an import would do. Nothing is written
// and the plan carries no variable values.
func (s *Service) Plan(ctx context.Context, req CoolifyPlanRequest) (CoolifyPlan, error) {
	if err := authorize(ctx); err != nil {
		return CoolifyPlan{}, err
	}
	p, _, err := s.load(ctx, req.Source, req.ServerID, req.Choices)
	if err != nil {
		return CoolifyPlan{}, err
	}
	return *p, nil
}

// Apply stages the plan: projects, environments, services, managed databases,
// variables, domains and links, in one transaction; git links, registry
// credentials-by-host and the optional deploy follow once it is committed.
func (s *Service) Apply(ctx context.Context, req CoolifyApplyRequest) (CoolifyApplyResult, error) {
	if err := authorize(ctx); err != nil {
		return CoolifyApplyResult{}, err
	}
	if req.Deploy {
		if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, nil); err != nil {
			return CoolifyApplyResult{}, err
		}
	}
	sid := req.ServerID
	plan, srv, err := s.load(ctx, req.Source, &sid, req.Choices)
	if err != nil {
		return CoolifyApplyResult{}, err
	}
	if len(plan.Blockers) > 0 {
		return CoolifyApplyResult{}, apierr.Unprocessable("import_blocked", plan.Blockers[0]).WithDetails(map[string]any{"blockers": plan.Blockers})
	}
	if err := s.guardExisting(ctx, plan, req); err != nil {
		return CoolifyApplyResult{}, err
	}

	ap := &applier{s: s, ctx: ctx, plan: plan, req: req, srv: srv, slugs: map[string]string{}}
	if err := db.InTx(ctx, s.Pool, ap.run); err != nil {
		if _, ok := apierr.As(err); ok {
			return CoolifyApplyResult{}, err
		}
		if services.IsUniqueViolation(err) {
			return CoolifyApplyResult{}, apierr.Conflict("import_conflict", "a project, service or hostname was just created by someone else; review the plan and retry")
		}
		return CoolifyApplyResult{}, fmt.Errorf("import from coolify: %w", err)
	}
	ap.afterCommit()
	return ap.result(), nil
}

// guardExisting applies the production guard to environments the import writes
// into that already exist (a new environment has nothing to protect). The
// approval is bound to the choices, never to the Coolify credential.
func (s *Service) guardExisting(ctx context.Context, plan *CoolifyPlan, req CoolifyApplyRequest) error {
	for _, pp := range plan.Projects {
		for _, pe := range pp.Environments {
			if pe.Action != "existing" || pe.existingID == nil || !pe.Production {
				continue
			}
			if !prodguard.Applies(ctx, true) {
				continue
			}
			env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, *pe.existingID)
			if err != nil {
				return err
			}
			var slugs []string
			for _, r := range pe.Resources {
				if r.Include {
					slugs = append(slugs, r.Slug)
				}
			}
			sort.Strings(slugs)
			body, err := json.Marshal(struct {
				Env    uuid.UUID
				Slugs  []string
				Deploy bool
			}{*pe.existingID, slugs, req.Deploy})
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			if err := prodguard.Check(ctx,
				env.ProdScope("Import from Coolify into production ("+env.Env.Name+")", false, nil,
					"Creates "+strconv.Itoa(len(slugs))+" service(s): "+strings.Join(slugs, ", ")+".",
					map[bool]string{true: "Deploys them right away.", false: "They stay staged."}[req.Deploy]),
				"import_coolify", prodguard.Digest("import_coolify", *pe.existingID, sum[:])); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- apply ----

type created struct {
	row   db.Service
	env   services.EnvScope
	res   *CoolifyPlanResource
	byUID string
}

type applier struct {
	s    *Service
	ctx  context.Context
	plan *CoolifyPlan
	req  CoolifyApplyRequest
	srv  db.Server

	slugs    map[string]string // planned slug → actual slug, per environment (reset)
	projects []CoolifyAppliedProject
	svcs     []created
	warnings []string
	deploys  map[uuid.UUID][]uuid.UUID
	gitLinks []created
}

func (ap *applier) run(tx pgx.Tx) error {
	ctx, q := ap.ctx, db.New(tx)
	ap.projects, ap.svcs, ap.warnings, ap.gitLinks = nil, nil, nil, nil
	ws := policy.WorkspaceID(ctx)
	counts := map[string]int{}
	for i := range ap.plan.Projects {
		pp := &ap.plan.Projects[i]
		if pp.Action == "skip" {
			continue
		}
		applied := CoolifyAppliedProject{Name: pp.Name, Slug: pp.Slug, Environments: []CoolifyAppliedEnvironment{}}
		if pp.Action == "existing" {
			applied.ID = *pp.ExistingID
		} else {
			row, err := q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: ws, Name: pp.Name, Slug: pp.Slug, Description: "Imported from Coolify"})
			if err != nil {
				return fmt.Errorf("create project: %w", err)
			}
			applied.ID, applied.Created = row.ID, true
			if err := audit.Record(ctx, tx, audit.Entry{
				Action: "project.create", ResourceType: "project", ResourceID: row.ID.String(), ProjectID: &row.ID,
				Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "via": "coolify-import"},
			}); err != nil {
				return err
			}
		}
		for j := range pp.Environments {
			pe := &pp.Environments[j]
			if pe.Action == "skip" {
				continue
			}
			ae := CoolifyAppliedEnvironment{Name: pe.Name, Slug: pe.Slug}
			if pe.Action == "existing" {
				ae.ID = *pe.existingID
			} else {
				row, err := q.CreateEnvironment(ctx, db.CreateEnvironmentParams{ProjectID: applied.ID, Name: pe.Name, Slug: pe.Slug, IsProduction: pe.Production})
				if err != nil {
					return fmt.Errorf("create environment: %w", err)
				}
				ae.ID, ae.Created = row.ID, true
				if err := audit.Record(ctx, tx, audit.Entry{
					Action: "environment.create", ResourceType: "environment", ResourceID: row.ID.String(), ProjectID: &applied.ID,
					Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "via": "coolify-import"},
				}); err != nil {
					return err
				}
			}
			applied.Environments = append(applied.Environments, ae)
			env, err := services.LoadEnvironment(ctx, q, policy.ScopeProjectsWrite, ae.ID)
			if err != nil {
				return err
			}
			if err := ap.environment(tx, q, env, pe, counts); err != nil {
				return err
			}
		}
		ap.projects = append(ap.projects, applied)
	}
	meta := map[string]any{"projects": len(ap.projects), "services": counts["services"], "databases": counts["databases"], "variables": counts["variables"],
		"domains": counts["domains"], "server": ap.srv.Name, "deploy": ap.req.Deploy}
	first := uuid.Nil
	if len(ap.projects) > 0 {
		first = ap.projects[0].ID
	}
	e := audit.Entry{Action: "coolify.import", ResourceType: "workspace", ResourceID: ws.String(), Metadata: meta}
	if first != uuid.Nil && len(ap.projects) == 1 {
		e.ProjectID = &first
	}
	return audit.Record(ctx, tx, e)
}

func (ap *applier) environment(tx pgx.Tx, q *db.Queries, env services.EnvScope, pe *CoolifyPlanEnvironment, counts map[string]int) error {
	ctx := ap.ctx
	ap.slugs = map[string]string{}
	byUID := map[string]db.Service{}
	var order []*CoolifyPlanResource
	for i := range pe.Resources {
		r := &pe.Resources[i]
		if !r.Include {
			continue
		}
		row, err := ap.createService(tx, env, r)
		if err != nil {
			return err
		}
		byUID[r.UUID] = row
		ap.slugs[r.Slug] = row.Slug
		ap.svcs = append(ap.svcs, created{row: row, env: env, res: r, byUID: r.UUID})
		order = append(order, r)
		if r.Kind == "database" {
			counts["databases"]++
		} else {
			counts["services"]++
		}
	}
	bySlug := map[string]db.Service{}
	for _, r := range order {
		bySlug[r.Slug] = byUID[r.UUID]
	}
	for _, r := range order {
		row := byUID[r.UUID]
		if r.app == nil {
			continue
		}
		sc := services.ScopeOf(env, row)
		for _, v := range r.Volumes {
			if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: v.Name, MountPath: v.MountPath}); err != nil {
				if services.IsUniqueViolation(err) {
					ap.warnings = append(ap.warnings, fmt.Sprintf("%s: volume %s was skipped (name or mount path already used).", r.Name, v.Name))
					continue
				}
				return fmt.Errorf("insert volume: %w", err)
			}
		}
		wrote := 0
		for _, e := range r.Env {
			if e.Skipped {
				continue
			}
			val := remapRefs(e.value, ap.slugs)
			if _, err := services.WriteServiceVariable(ctx, q, ap.s.Box, sc, services.VarWrite{
				Key: e.Key, Value: val, Secret: e.Secret, Scope: e.Scope, Source: services.SourceImported,
			}); err != nil {
				return fmt.Errorf("write variable %s.%s: %w", r.Slug, e.Key, err)
			}
			wrote++
		}
		counts["variables"] += wrote
		if wrote > 0 {
			if _, err := q.BumpServiceGeneration(ctx, row.ID); err != nil {
				return err
			}
		}
		for _, d := range r.Domains {
			if !d.Enabled {
				continue
			}
			warn, err := ap.addDomain(tx, env, row, r, d)
			if err != nil {
				return err
			}
			if warn != "" {
				ap.warnings = append(ap.warnings, warn)
			} else {
				counts["domains"]++
			}
		}
		for _, dep := range r.DependsOn {
			to, ok := bySlug[dep]
			if !ok {
				continue
			}
			if _, err := q.InsertLink(ctx, db.InsertLinkParams{EnvironmentID: env.Env.ID, FromServiceID: row.ID, ToServiceID: to.ID}); err != nil {
				return fmt.Errorf("insert link: %w", err)
			}
		}
		if g := r.Source.Git; g != nil && g.Repo != "" && ap.req.Choices.GitConnectionID != nil {
			ap.gitLinks = append(ap.gitLinks, created{row: row, env: env, res: r})
		}
	}
	return nil
}

func (ap *applier) createService(tx pgx.Tx, env services.EnvScope, r *CoolifyPlanResource) (db.Service, error) {
	serverID := ap.srv.ID
	if r.Kind == "database" {
		if ap.s.Databases == nil {
			return db.Service{}, apierr.Unprocessable("databases_unavailable", "this server build cannot create managed databases")
		}
		m := r.Source.Managed
		row, err := ap.s.Databases.CreateTx(ap.ctx, tx, databases.CreateInput{
			EnvironmentID: env.Env.ID, Name: r.Name, Engine: m.Engine, Version: m.Version, Flavor: m.Flavor, DatabaseName: m.Database, Username: m.Username,
			ServerID: &serverID, AcceptLicense: r.licence && r.choice.Include != nil && *r.choice.Include,
		})
		if err != nil {
			return db.Service{}, fmt.Errorf("create database %s: %w", r.Name, err)
		}
		return row, nil
	}
	cfg := configFor(r)
	in := services.CreateInput{Name: r.Name, Kind: r.Kind, SourceType: services.SourceImage, Config: cfg, ServerID: &serverID}
	if r.Source.Type == "git" {
		in.SourceType = services.SourceGit
	} else if ra, ok := ap.req.Choices.Registries[r.registryHost]; ok && (ra.Username != "" || ra.Password != "") {
		in.RegistryAuth = &services.RegistryAuth{Username: ra.Username, Password: ra.Password}
	}
	return ap.s.Services.CreateInTx(ap.ctx, tx, env, in)
}

// configFor builds the service configuration of an application.
func configFor(r *CoolifyPlanResource) services.Config {
	cfg := services.Config{Port: r.Port, MemoryMB: r.MemoryMB, CPUs: r.CPUs, StartCommand: r.Command, Docker: r.Docker,
		PreDeployCommand: r.PreDeploy, PostDeployCommand: r.PostDeploy}
	if g := r.Source.Git; g != nil {
		cfg.GitURL, cfg.GitBranch, cfg.BuildType, cfg.BuildContext = g.URL, g.Branch, g.BuildType, g.BuildContext
		if g.DockerfilePath != "" {
			cfg.DockerfilePath = g.DockerfilePath
		}
	} else {
		cfg.Image = r.Source.Image
	}
	if h := r.Health; h != nil {
		cfg.Healthcheck = services.Healthcheck{
			Type: h.Type, Path: h.Path, Port: h.Port, IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
			StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds, Command: h.command,
		}
	}
	return cfg
}

// addDomain attaches a hostname to the service (staged), like a template install.
func (ap *applier) addDomain(tx pgx.Tx, env services.EnvScope, row db.Service, r *CoolifyPlanResource, d CoolifyPlanDomain) (warn string, err error) {
	ctx, q := ap.ctx, db.New(tx)
	port := d.Port
	if port == 0 {
		port = services.DecodeConfig(row.Config).Port
	}
	if port <= 0 {
		return r.Name + ": the domain " + d.Host + " was skipped (the service has no port).", nil
	}
	srv := ap.srv
	if srv.ProxyStatus != nil && *srv.ProxyStatus == agentprotocol.ProxyError {
		return "", apierr.Unprocessable("proxy_unavailable", "the proxy on "+srv.Name+" is not running")
	}
	host := d.Host
	if d.Generate {
		base, ok := domains.BaseDomain(srv)
		if !ok {
			return "No hostname was generated for " + r.Name + ": the server has neither a base domain nor a public IP. Add a domain in its settings.", nil
		}
		first := domains.GenerateHostname(row.Slug, env.Env.Slug, env.ProjectSlug, base)
		host = first
		for i := 2; ; i++ {
			n, err := q.CountDomainsByHostname(ctx, host)
			if err != nil {
				return "", err
			}
			if n == 0 {
				break
			}
			if i > 9 {
				return "", apierr.Conflict("domain_conflict", "no free generated hostname for "+r.Name)
			}
			label, rest, _ := strings.Cut(first, ".")
			suffix := "-" + strconv.Itoa(i)
			if len(label)+len(suffix) > 63 {
				label = strings.Trim(label[:63-len(suffix)], "-")
			}
			host = label + suffix + "." + rest
		}
	} else if host, err = domains.ValidateHostname(host); err != nil {
		return "", err
	}
	var opts agentprotocol.RouteOptions
	if !d.ForceHTTPS {
		f := false
		opts.ForceHTTPS = &f
	}
	opts.Compress, opts.Noindex = d.Compress, d.Noindex
	var optsJSON json.RawMessage
	if !opts.IsZero() {
		if optsJSON, err = json.Marshal(opts); err != nil {
			return "", err
		}
	}
	dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
		ServiceID: row.ID, Hostname: host, TargetPort: int32(port), Generated: d.Generate, StatusMessage: domains.DNSGuidance(host, srv.PublicIp),
		PathPrefix: d.Path, Options: optsJSON,
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return r.Name + ": the hostname " + host + " is already in use and was skipped.", nil
		}
		return "", err
	}
	if _, err := q.BumpServiceGeneration(ctx, row.ID); err != nil {
		return "", err
	}
	if err := ap.s.Jobs.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
		return "", fmt.Errorf("enqueue dns check: %w", err)
	}
	return "", nil
}

func (ap *applier) afterCommit() {
	ctx := ap.ctx
	for _, c := range ap.svcs {
		services.PublishService(ap.s.Bus, services.EventServiceUpdated, services.ScopeOf(c.env, c.row))
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range ap.svcs {
		if !seen[c.env.Env.ID] {
			seen[c.env.Env.ID] = true
			services.Publish(ap.s.Bus, services.EventCanvasUpdated, c.env.Env.ProjectID, c.env.Env.ID, c.env.Env.ID)
		}
	}
	if ap.s.Git != nil && ap.req.Choices.GitConnectionID != nil {
		auto := false
		for _, c := range ap.gitLinks {
			g := c.res.Source.Git
			// The service was created by this import, is not deployed and its
			// link has auto-deploy off: nothing that already runs changes.
			if _, err := ap.s.Git.Link(prodguard.Covered(ctx), c.row.ID, gitproviders.LinkInput{ConnectionID: *ap.req.Choices.GitConnectionID, Repo: g.Repo, Branch: g.Branch, AutoDeploy: &auto}); err != nil {
				ap.warnings = append(ap.warnings, "Could not link "+c.res.Name+" to "+g.Repo+": "+err.Error()+". Link it under Settings → Source so builds can clone it.")
			}
		}
	}
	if ap.req.Deploy && ap.s.Deploy != nil {
		ap.deploys = map[uuid.UUID][]uuid.UUID{}
		perEnv := map[uuid.UUID][]uuid.UUID{}
		var envOrder []uuid.UUID
		for _, c := range ap.svcs {
			if _, ok := perEnv[c.env.Env.ID]; !ok {
				envOrder = append(envOrder, c.env.Env.ID)
			}
			perEnv[c.env.Env.ID] = append(perEnv[c.env.Env.ID], c.row.ID)
		}
		for _, envID := range envOrder {
			staged, err := ap.s.Deploy.DeployStaged(ctx, envID, perEnv[envID], "Imported from Coolify")
			if err != nil {
				ap.warnings = append(ap.warnings, "The services were added but deploying failed: "+err.Error())
				continue
			}
			for _, d := range staged.Items {
				ap.deploys[envID] = append(ap.deploys[envID], d.ID)
			}
		}
	}
}

func (ap *applier) result() CoolifyApplyResult {
	res := CoolifyApplyResult{Projects: ap.projects, Services: []CoolifyAppliedService{}, Deployments: []uuid.UUID{}, Rewrites: ap.plan.Rewrites, Followups: []string{}}
	if res.Projects == nil {
		res.Projects = []CoolifyAppliedProject{}
	}
	res.Warnings = append(append([]string{}, ap.plan.Warnings...), ap.warnings...)
	for _, c := range ap.svcs {
		res.Services = append(res.Services, CoolifyAppliedService{
			ID: c.row.ID, Name: c.row.Name, Slug: c.row.Slug, Kind: c.row.Kind, ProjectID: c.env.Env.ProjectID, ProjectSlug: c.env.ProjectSlug,
			EnvironmentID: c.env.Env.ID, EnvSlug: c.env.Env.Slug, CoolifyUUID: c.byUID,
		})
		if c.res.Kind == "database" {
			res.Followups = append(res.Followups, databaseFollowup(c.res.dbRec, c.row.Slug))
		}
	}
	res.Followups = append(res.Followups, ap.plan.general...)
	envs := make([]uuid.UUID, 0, len(ap.deploys))
	for e := range ap.deploys {
		envs = append(envs, e)
	}
	for _, e := range envs {
		res.Deployments = append(res.Deployments, ap.deploys[e]...)
	}
	return res
}
