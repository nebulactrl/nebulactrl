// Package composeimport turns a Docker Compose file into staged NebulaCtrl
// services. It is an import wizard, not a Compose runtime: every Compose
// service becomes an ordinary service of the chosen environment, grouped on the
// canvas and linked in deploy order, and everything after the import (deploy,
// rollback, variables, domains, logs) is the normal service path.
//
// The pipeline is Source → Analyze (pure: parse, interpolate, map, rewrite
// references, flag what is unsupported) → Plan (what the wizard shows) →
// Import (one transaction that stages everything).
package composeimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// GitSource reads Compose files and clones from git (gitproviders.Service).
type GitSource interface {
	OpenRepo(ctx context.Context, connectionID uuid.UUID, repo, branch string) (*gitproviders.RepoReader, error)
	OpenPublicRepo(ctx context.Context, repoURL, branch string) (*gitproviders.RepoReader, error)
	CloneURLFor(ctx context.Context, connectionID uuid.UUID, repo string) (cloneURL, branch string, err error)
	Link(ctx context.Context, serviceID uuid.UUID, in gitproviders.LinkInput) (gitproviders.GitLinkView, error)
}

// SharedNetworks lists and attaches shared networks (networks.Service).
type SharedNetworks interface {
	List(ctx context.Context, serverID *uuid.UUID) ([]networks.NetworkView, error)
	Attach(ctx context.Context, serviceID, networkID uuid.UUID) (services.NetworkRef, error)
}

// Scaler sets replica counts (scaling.Service).
type Scaler interface {
	Set(ctx context.Context, id uuid.UUID, in scaling.SetInput) (services.ServiceView, error)
}

// DatabaseCreator creates a managed database inside the caller's transaction.
type DatabaseCreator interface {
	CreateTx(ctx context.Context, tx pgx.Tx, in databases.CreateInput) (db.Service, error)
}

// Deps are the collaborators of the import service.
type Deps struct {
	Pool      *pgxpool.Pool
	Bus       *events.Bus
	Jobs      *jobs.Manager
	Services  *services.Service
	Variables templates.VariableWriter
	Databases DatabaseCreator
	Ports     templates.PortRouter
	Deploy    templates.Deployer
	Git       GitSource
	Networks  SharedNetworks
	Scaling   Scaler
	Logger    *slog.Logger
}

// Service implements the Compose import use cases.
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

// ComposePlanRequest is the body of a plan.
type ComposePlanRequest struct {
	Source  ComposeSource  `json:"source"`
	Choices ComposeChoices `json:"choices"`
	// ServerID adds checks against the server the stack would run on.
	ServerID *uuid.UUID `json:"serverId,omitempty"`
	// Mode picks how the project is imported: "split" (default) makes one service per
	// Compose service, "stack" runs the whole file as one Compose stack.
	Mode string `json:"mode,omitempty" enum:"split,stack" default:"split"`
}

// ComposeImportRequest is the body of an import.
type ComposeImportRequest struct {
	Source   ComposeSource  `json:"source"`
	Choices  ComposeChoices `json:"choices"`
	ServerID uuid.UUID      `json:"serverId"`
	// Deploy deploys the new services right away (needs deployments:write);
	// otherwise they stay staged on the canvas.
	Deploy bool `json:"deploy,omitempty"`
	// Mode is "split" (default) or "stack"; see ComposePlanRequest.
	Mode string `json:"mode,omitempty" enum:"split,stack" default:"split"`
}

// ComposeImportResult is the outcome of an import.
type ComposeImportResult struct {
	Name    string `json:"name"`
	GroupID string `json:"groupId"`
	// Services are the created services in creation order (route services last).
	Services    []services.ServiceView       `json:"services"`
	Deployments []deployments.DeploymentView `json:"deployments"`
	Rewrites    []ComposeRewrite             `json:"rewrites"`
	Warnings    []string                     `json:"warnings"`
}

// ---- analysis with database and git context ----

type analysis struct {
	res    *Result
	env    services.EnvScope
	server *db.Server
	checks []ComposeCheck
	repo   *RepoInfo

	// Stack mode: the Compose file and .env as given (resolved from git when
	// the source is a repository), the project name, and why the split
	// analysis (which only feeds domain suggestions there) failed, if it did.
	compose  string
	dotenv   string
	name     string
	splitErr error
}

func (s *Service) analyze(ctx context.Context, q *db.Queries, env services.EnvScope, src ComposeSource, ch ComposeChoices, serverID *uuid.UUID, mode string) (*analysis, error) {
	a := &analysis{env: env}
	cx := Context{
		Taken: func(base string) []string {
			rows, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: env.Env.ID, Base: base})
			if err != nil {
				return nil
			}
			return rows
		},
	}
	if serverID != nil {
		row, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID, WorkspaceID: &env.WorkspaceID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, apierr.Unprocessable("server_not_found", "the selected server does not exist")
		case err != nil:
			return nil, fmt.Errorf("load server: %w", err)
		}
		a.server = &row
		cx.Unrestricted = row.Unrestricted
		if s.Networks != nil {
			if nets, err := s.Networks.List(ctx, serverID); err == nil {
				cx.SharedNetworks = map[string]uuid.UUID{}
				for _, n := range nets {
					cx.SharedNetworks[n.Name] = n.ID
				}
			}
		}
	}
	if s.Git != nil {
		cx.ResolveRepo = func(conn uuid.UUID, repo string) (string, string, error) {
			if r, err := s.Git.OpenRepo(ctx, conn, repo, ""); err == nil {
				return r.CloneURL, r.Branch, nil
			}
			return s.Git.CloneURLFor(ctx, conn, repo)
		}
	}

	if src.Repo != nil {
		rd, err := s.openRepo(ctx, *src.Repo)
		if err != nil {
			return nil, err
		}
		composePath := strings.TrimSpace(src.Repo.Path)
		if src.Compose == "" {
			content, found, err := readCompose(ctx, rd, composePath)
			if err != nil {
				return nil, err
			}
			src.Compose, composePath = content, found
		}
		if composePath == "" {
			composePath = "docker-compose.yml"
		}
		a.repo = &RepoInfo{ConnectionID: src.Repo.ConnectionID, Repo: rd.FullName, Branch: rd.Branch, CloneURL: rd.CloneURL}
		cx.Repo, cx.ComposeDir = a.repo, dirOf(composePath)
		cx.Files = newRepoFiles(ctx, rd, cx.ComposeDir).get
		cx.DefaultName = path.Base(rd.FullName)
		if src.Env == "" {
			if raw, ok := cx.Files(".env"); ok {
				src.Env = string(raw)
			}
		}
	}
	if strings.TrimSpace(src.Compose) == "" {
		return nil, apierr.Unprocessable("validation_failed", "Paste a docker-compose.yml, or point at one in a git repository.")
	}
	a.compose, a.dotenv, a.name = src.Compose, src.Env, strings.TrimSpace(src.Name)
	if a.name == "" {
		a.name = cx.DefaultName
	}
	res, err := Analyze(src, ch, cx)
	if err != nil {
		if mode == ModeStack {
			// The stack checks the file itself; the split analysis only suggests domains.
			a.splitErr = err
			return a, nil
		}
		return nil, apierr.Unprocessable("invalid_compose", err.Error())
	}
	a.res = res
	if mode != ModeStack {
		a.checks = s.serverChecks(ctx, q, a)
	}
	return a, nil
}

func (s *Service) openRepo(ctx context.Context, ref ComposeRepoRef) (*gitproviders.RepoReader, error) {
	if s.Git == nil {
		return nil, apierr.Unprocessable("git_unavailable", "this server build cannot read from git")
	}
	switch {
	case ref.ConnectionID != nil:
		if ref.Repo == "" {
			return nil, apierr.Unprocessable("validation_failed", "repo is required with a git connection")
		}
		return s.Git.OpenRepo(ctx, *ref.ConnectionID, ref.Repo, ref.Branch)
	case ref.URL != "":
		return s.Git.OpenPublicRepo(ctx, ref.URL, ref.Branch)
	}
	return nil, apierr.Unprocessable("validation_failed", "give a git connection and repository, or the URL of a public repository")
}

var composeNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yaml", "compose.yml"}

// readCompose reads the Compose file; without a path it tries the usual names.
func readCompose(ctx context.Context, rd *gitproviders.RepoReader, p string) (content, found string, err error) {
	candidates := composeNames
	if p != "" {
		clean := strings.TrimPrefix(path.Clean(p), "/")
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return "", "", apierr.Unprocessable("validation_failed", "The Compose file path must stay inside the repository.")
		}
		candidates = []string{clean}
	}
	for _, c := range candidates {
		raw, err := rd.Get(ctx, c)
		if errors.Is(err, gitproviders.ErrFileNotFound) {
			continue
		}
		if err != nil {
			return "", "", apierr.Unprocessable("repo_read_failed", "Could not read "+c+": "+err.Error())
		}
		return string(raw), c, nil
	}
	return "", "", apierr.Unprocessable("compose_not_found", "No Compose file ("+strings.Join(candidates, ", ")+") was found in "+rd.FullName+" on "+rd.Branch+".")
}

// repoFiles reads referenced files of the repository relative to the Compose
// file's directory, with a cap on the number of reads.
type repoFiles struct {
	ctx   context.Context
	rd    *gitproviders.RepoReader
	dir   string
	cache map[string][]byte
	miss  map[string]bool
	reads int
}

const maxRepoReads = 40

func newRepoFiles(ctx context.Context, rd *gitproviders.RepoReader, dir string) *repoFiles {
	return &repoFiles{ctx: ctx, rd: rd, dir: dir, cache: map[string][]byte{}, miss: map[string]bool{}}
}

func (f *repoFiles) get(p string) ([]byte, bool) {
	p = cleanRel(p)
	if p == "" || strings.HasPrefix(p, "..") || path.IsAbs(p) {
		return nil, false
	}
	if v, ok := f.cache[p]; ok {
		return v, true
	}
	if f.miss[p] || f.reads >= maxRepoReads {
		return nil, false
	}
	f.reads++
	raw, err := f.rd.Get(f.ctx, path.Join(f.dir, p))
	if err != nil {
		f.miss[p] = true
		return nil, false
	}
	f.cache[p] = raw
	return raw, true
}

// serverChecks are the preflight checks that need the chosen server.
func (s *Service) serverChecks(ctx context.Context, q *db.Queries, a *analysis) []ComposeCheck {
	checks := []ComposeCheck{}
	add := func(id, status, msg string) {
		checks = append(checks, ComposeCheck{ID: id, Status: status, Message: msg})
	}
	p := a.res.plan
	secretCount := 0
	for _, ps := range p.Services {
		if !ps.Include {
			continue
		}
		for _, e := range ps.Env {
			if e.Secret && !e.Skipped {
				secretCount++
			}
		}
	}
	if secretCount > 0 {
		add("secrets", CheckOK, fmt.Sprintf("%d variable(s) will be stored encrypted as secrets; their values are never shown again.", secretCount))
	}
	srv := a.server
	if srv == nil {
		add("server", CheckWarn, "Choose a server to check capacity, domains and published ports.")
		return checks
	}
	if stop := serverStatusCheck(srv, add); stop {
		return checks
	}
	wantsDomain, publishes := false, false
	for _, ps := range p.Services {
		if !ps.Include {
			continue
		}
		for _, d := range ps.Domains {
			if !d.Enabled {
				continue
			}
			wantsDomain = true
			if d.Host == "" {
				continue
			}
			h, err := domains.ValidateHostname(d.Host)
			if err != nil {
				add("domain", CheckFail, fmt.Sprintf("%s: %v", ps.Name, err))
				continue
			}
			if n, err := q.CountDomainsByHostname(ctx, h); err == nil && n > 0 {
				add("domain", CheckFail, fmt.Sprintf("The hostname %s is already in use. Turn the domain of %s off or change it.", h, ps.Name))
			}
		}
		for _, pt := range ps.Ports {
			if pt.Publish {
				publishes = true
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
	if publishes {
		if s.Ports == nil || !domains.SupportsProxyV2(*srv) {
			add("agent", CheckFail, fmt.Sprintf("The agent on %s cannot publish TCP/UDP ports; update the agent first or turn the ports off.", srv.Name))
		} else {
			for _, ps := range p.Services {
				for _, pt := range ps.Ports {
					if !ps.Include || !pt.Publish {
						continue
					}
					busy, err := s.Ports.PortInUse(ctx, q, srv.ID, pt.Protocol, pt.Listen)
					switch {
					case err != nil:
						add("port-"+strconv.Itoa(pt.Listen), CheckWarn, fmt.Sprintf("Could not check whether %s/%d is free: %v", pt.Protocol, pt.Listen, err))
					case busy:
						add("port-"+strconv.Itoa(pt.Listen), CheckFail, fmt.Sprintf("%s/%d is already published by another route on %s.", pt.Protocol, pt.Listen, srv.Name))
					default:
						add("port-"+strconv.Itoa(pt.Listen), CheckOK, fmt.Sprintf("%s/%d (%s) will be published on %s.", pt.Protocol, pt.Listen, ps.Name, srv.Name))
					}
				}
			}
			add("proxy-restart", CheckWarn, "Publishing TCP/UDP ports restarts the server's proxy briefly (about a second) the first time a new port is added.")
		}
	}
	return checks
}

// serverStatusCheck reports whether the server can take new services at all.
// stop is true when the remaining checks are pointless.
func serverStatusCheck(srv *db.Server, add func(id, status, msg string)) (stop bool) {
	switch {
	case srv.Status == "revoked":
		add("server", CheckFail, fmt.Sprintf("%s has been revoked.", srv.Name))
		return true
	case srv.DrainState == services.DrainStateDraining || srv.DrainState == services.DrainStateDrained:
		add("server", CheckFail, fmt.Sprintf("%s is being drained and cannot take new services.", srv.Name))
		return true
	case !srv.AcceptWorkloads:
		add("server", CheckFail, fmt.Sprintf("%s does not accept workloads (Accept workloads is off in its settings).", srv.Name))
		return true
	case srv.Status != "online":
		add("server", CheckWarn, fmt.Sprintf("%s is %s: the services are staged, but deploying waits until the agent connects.", srv.Name, srv.Status))
	default:
		add("server", CheckOK, fmt.Sprintf("%s is online.", srv.Name))
	}
	return false
}

// finishImport publishes the change events of the created services, loads their
// views and, when asked, deploys them.
func (s *Service) finishImport(ctx context.Context, env services.EnvScope, envID uuid.UUID, created []db.Service, deploy bool, res *ComposeImportResult) error {
	for _, row := range created {
		services.PublishService(s.Bus, services.EventServiceUpdated, services.ScopeOf(env, row))
	}
	services.Publish(s.Bus, services.EventCanvasUpdated, env.Env.ProjectID, envID, envID)

	res.Services = make([]services.ServiceView, 0, len(created))
	for _, row := range created {
		sc, err := services.LoadScopeInternal(ctx, s.q, row.ID)
		if err != nil {
			return err
		}
		v, err := s.Services.ViewFor(ctx, sc)
		if err != nil {
			return err
		}
		res.Services = append(res.Services, v)
	}
	if deploy {
		ids := make([]uuid.UUID, len(created))
		for i, r := range created {
			ids[i] = r.ID
		}
		staged, err := s.Deploy.DeployStaged(ctx, envID, ids, "Imported from Compose")
		if err != nil {
			res.Warnings = append(res.Warnings, "The services were added but deploying failed: "+err.Error())
		} else {
			res.Deployments = staged.Items
		}
	}
	return nil
}

// Plan reports what importing a Compose project into an environment would do.
// Nothing is written.
func (s *Service) Plan(ctx context.Context, envID uuid.UUID, req ComposePlanRequest) (ComposePlan, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID)
	if err != nil {
		return ComposePlan{}, err
	}
	if req.Source.Repo != nil {
		if err := policy.Require(ctx, policy.ScopeProjectsWrite, &env.Env.ProjectID); err != nil {
			return ComposePlan{}, err
		}
	}
	a, err := s.analyze(ctx, s.q, env, req.Source, req.Choices, req.ServerID, req.Mode)
	if err != nil {
		return ComposePlan{}, err
	}
	if req.Mode == ModeStack {
		return s.planStack(ctx, s.q, a, req.Choices)
	}
	p := a.res.Plan()
	p.Checks = a.checks
	for _, c := range a.checks {
		if c.Status == CheckFail {
			p.Installable = false
			p.Blockers = append(p.Blockers, c.Message)
		}
	}
	return p, nil
}

// ---- import ----

// Import stages the services of a Compose project in an environment: services,
// volumes, files, variables, domains, published ports, links, a canvas group
// and an audit entry, all in one transaction. Replica counts, shared networks
// and repository links follow once it is committed.
func (s *Service) Import(ctx context.Context, envID uuid.UUID, req ComposeImportRequest) (ComposeImportResult, error) {
	env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return ComposeImportResult{}, err
	}
	if req.Deploy {
		if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &env.Env.ProjectID); err != nil {
			return ComposeImportResult{}, err
		}
	}
	sid := req.ServerID
	a, err := s.analyze(ctx, s.q, env, req.Source, req.Choices, &sid, req.Mode)
	if err != nil {
		return ComposeImportResult{}, err
	}
	if req.Deploy && prodguard.Applies(ctx, env.Env.IsProduction) {
		var names []string
		for _, ps := range a.res.plan.Services {
			if ps.Include {
				names = append(names, ps.Slug)
			}
		}
		body, err := json.Marshal(req)
		if err != nil {
			return ComposeImportResult{}, fmt.Errorf("digest import request: %w", err)
		}
		if err := prodguard.Check(ctx,
			env.ProdScope(fmt.Sprintf("Import a Compose project into production and deploy it (%s)", env.Env.Name), false, nil,
				"Creates "+strconv.Itoa(len(names))+" service(s): "+strings.Join(names, ", ")+".",
				"Starts them right away on the chosen server, databases first."),
			"import_compose", prodguard.Digest("import_compose", envID, body)); err != nil {
			return ComposeImportResult{}, err
		}
	}
	if req.Mode == ModeStack {
		return s.importStack(ctx, env, envID, req, a)
	}
	for _, c := range a.checks {
		if c.Status == CheckFail {
			return ComposeImportResult{}, apierr.Unprocessable("preflight_failed", c.Message).WithDetails(map[string]any{"checks": a.checks})
		}
	}
	plan := a.res.Plan()
	if len(plan.Blockers) > 0 {
		return ComposeImportResult{}, apierr.Unprocessable("import_blocked", plan.Blockers[0]).WithDetails(map[string]any{"blockers": plan.Blockers})
	}

	b := &importer{s: s, env: env, a: a, srv: *a.server, slugs: map[string]string{}, byKey: map[string]db.Service{}, warnings: []string{}}
	if err := db.InTx(ctx, s.Pool, b.run(ctx)); err != nil {
		if _, ok := apierr.As(err); ok {
			return ComposeImportResult{}, err
		}
		if services.IsUniqueViolation(err) {
			return ComposeImportResult{}, apierr.Conflict("import_conflict", "a hostname or port of this project was just taken; review the plan and retry")
		}
		return ComposeImportResult{}, fmt.Errorf("import compose project: %w", err)
	}
	b.afterCommit(ctx)

	res := ComposeImportResult{Name: a.res.groupName, GroupID: b.groupID, Rewrites: plan.Rewrites, Warnings: b.warnings, Deployments: []deployments.DeploymentView{}}
	if err := s.finishImport(ctx, env, envID, b.created, req.Deploy, &res); err != nil {
		return ComposeImportResult{}, err
	}
	return res, nil
}

type importer struct {
	s   *Service
	env services.EnvScope
	a   *analysis
	srv db.Server

	groupID  string
	created  []db.Service
	byKey    map[string]db.Service
	slugs    map[string]string // planned slug → actual slug
	warnings []string
	// after commit
	replicas []replicaTask
	networks []networkTask
	links    []linkTask
}

type replicaTask struct {
	id   uuid.UUID
	name string
	n    int
}

type networkTask struct {
	id      uuid.UUID
	name    string
	network uuid.UUID
}

type linkTask struct {
	id     uuid.UUID
	name   string
	conn   uuid.UUID
	repo   string
	branch string
}

func (b *importer) included() []ComposePlanService {
	var out []ComposePlanService
	for _, ps := range b.a.res.plan.Services {
		if ps.Include {
			out = append(out, ps)
		}
	}
	return out
}

func (b *importer) run(ctx context.Context) func(pgx.Tx) error {
	return func(tx pgx.Tx) error {
		q := db.New(tx)
		b.created, b.replicas, b.networks, b.links, b.warnings = nil, nil, nil, nil, []string{}
		b.byKey, b.slugs = map[string]db.Service{}, map[string]string{}
		svcs := b.included()

		// 1. Services (every service has its real slug before variables are written).
		for _, ps := range svcs {
			row, err := b.createService(ctx, tx, ps)
			if err != nil {
				return err
			}
			b.byKey[ps.Key] = row
			b.slugs[ps.Slug] = row.Slug
			b.created = append(b.created, row)
		}

		// 2. Volumes, variables, domains, links, ports.
		var hosts []string
		for _, ps := range svcs {
			row := b.byKey[ps.Key]
			for _, v := range ps.Volumes {
				if v.From == "managed" {
					continue
				}
				if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: v.Name, MountPath: v.Path}); err != nil {
					return fmt.Errorf("insert volume: %w", err)
				}
			}
			if err := b.writeEnv(ctx, tx, ps, row); err != nil {
				return err
			}
			for _, d := range ps.Domains {
				if !d.Enabled {
					continue
				}
				host, warn, err := b.addDomain(ctx, tx, row, d)
				if err != nil {
					return err
				}
				if warn != "" {
					b.warnings = append(b.warnings, warn)
				}
				if host != "" {
					hosts = append(hosts, host)
				}
			}
			for _, dep := range ps.DependsOn {
				if _, err := q.InsertLink(ctx, db.InsertLinkParams{EnvironmentID: b.env.Env.ID, FromServiceID: row.ID, ToServiceID: b.byKey[dep].ID}); err != nil {
					return fmt.Errorf("insert link: %w", err)
				}
			}
			for _, p := range ps.Ports {
				if !p.Publish {
					continue
				}
				if b.s.Ports == nil {
					return apierr.Unprocessable("ports_unavailable", "this server build cannot publish TCP/UDP ports")
				}
				rid, err := b.s.Ports.AddPortRouteTx(ctx, tx, templates.PortRoute{
					EnvironmentID: b.env.Env.ID, TargetServiceID: row.ID, Name: row.Slug + "-" + strconv.Itoa(p.Listen) + "-" + p.Protocol,
					Protocol: p.Protocol, ListenPort: p.Listen, TargetPort: p.Port,
				})
				if err != nil {
					return err
				}
				routeScope, err := services.LoadScopeInternal(ctx, q, rid)
				if err != nil {
					return err
				}
				b.created = append(b.created, routeScope.Service)
			}
			b.collectAfterCommit(ps, row)
		}

		// 3. Canvas group (route services stay outside).
		ids := make([]uuid.UUID, 0, len(svcs))
		for _, ps := range svcs {
			ids = append(ids, b.byKey[ps.Key].ID)
		}
		b.groupID = "cmp-" + uuid.NewString()[:8]
		if err := canvas.AddGroupTx(ctx, tx, b.env.Env.ID, b.groupID, b.a.res.groupName, ids); err != nil {
			return err
		}

		meta := map[string]any{"name": b.a.res.groupName, "server": b.srv.Name, "services": len(b.created), "hostnames": hosts, "source": b.a.res.plan.SourceKind, "mode": ModeSplit}
		if r := b.a.repo; r != nil {
			meta["repo"], meta["branch"] = r.Repo, r.Branch
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "compose.import", ResourceType: "environment", ResourceID: b.env.Env.ID.String(), ProjectID: &b.env.Env.ProjectID, Metadata: meta,
		})
	}
}

func (b *importer) collectAfterCommit(ps ComposePlanService, row db.Service) {
	if ps.Replicas > 1 {
		b.replicas = append(b.replicas, replicaTask{id: row.ID, name: ps.Name, n: ps.Replicas})
	}
	if bd := ps.Source.Build; ps.Source.Type == "git" && bd != nil && bd.ConnectionID != nil && bd.Repo != "" {
		b.links = append(b.links, linkTask{id: row.ID, name: ps.Name, conn: *bd.ConnectionID, repo: bd.Repo, branch: bd.GitBranch})
	}
	for _, n := range b.a.res.plan.Networks {
		if n.SharedNetworkID == nil {
			continue
		}
		for _, k := range n.Services {
			if k == ps.Key {
				b.networks = append(b.networks, networkTask{id: row.ID, name: ps.Name, network: *n.SharedNetworkID})
			}
		}
	}
}

// afterCommit applies what needs its own transaction; problems become warnings.
func (b *importer) afterCommit(ctx context.Context) {
	for _, t := range b.links {
		if b.s.Git == nil {
			continue
		}
		auto := false
		// The service was created by this import and is not deployed; its link
		// has auto-deploy off, so it changes nothing that already runs.
		if _, err := b.s.Git.Link(prodguard.Covered(ctx), t.id, gitproviders.LinkInput{ConnectionID: t.conn, Repo: t.repo, Branch: t.branch, AutoDeploy: &auto}); err != nil {
			b.warnings = append(b.warnings, "Could not link "+t.name+" to "+t.repo+": "+err.Error()+". Link it under Settings → Source so builds can clone it.")
		}
	}
	for _, t := range b.replicas {
		if b.s.Scaling == nil {
			continue
		}
		n := t.n
		if _, err := b.s.Scaling.Set(ctx, t.id, scaling.SetInput{Replicas: &n}); err != nil {
			b.warnings = append(b.warnings, fmt.Sprintf("Could not set %d replicas on %s: %v", t.n, t.name, err))
		}
	}
	for _, t := range b.networks {
		if b.s.Networks == nil {
			continue
		}
		if _, err := b.s.Networks.Attach(ctx, t.id, t.network); err != nil {
			b.warnings = append(b.warnings, "Could not attach "+t.name+" to the shared network: "+err.Error())
		}
	}
}

func (b *importer) createService(ctx context.Context, tx pgx.Tx, ps ComposePlanService) (db.Service, error) {
	serverID := b.srv.ID
	if ps.Source.Type == "managed" {
		if b.s.Databases == nil {
			return db.Service{}, apierr.Unprocessable("databases_unavailable", "this server build cannot create managed databases")
		}
		m := ps.Managed
		row, err := b.s.Databases.CreateTx(ctx, tx, databases.CreateInput{
			EnvironmentID: b.env.Env.ID, Name: ps.Name, Engine: m.Engine, Version: m.Version, DatabaseName: m.Database, Username: m.Username,
			ServerID: &serverID, AcceptLicense: true, // source-available engines are only enabled by an explicit choice
		})
		if err != nil {
			return db.Service{}, fmt.Errorf("create database %s: %w", ps.Key, err)
		}
		return row, nil
	}
	cfg := configFor(ps)
	sourceType := services.SourceImage
	if ps.Source.Type == "git" {
		sourceType = services.SourceGit
	}
	return b.s.Services.CreateInTx(ctx, tx, b.env, services.CreateInput{
		Name: ps.Name, Kind: ps.Kind, SourceType: sourceType, Config: cfg, ServerID: &serverID,
	})
}

// configFor builds the service configuration of an image or git service.
func configFor(ps ComposePlanService) services.Config {
	cfg := services.Config{
		Port: ps.Port, StartCommand: ps.Command, StopGraceSeconds: ps.StopGraceSeconds, MemoryMB: ps.MemoryMB, CPUs: ps.CPUs, OneShot: ps.OneShot,
	}
	if ps.Source.Type == "git" && ps.Source.Build != nil {
		bd := ps.Source.Build
		cfg.GitURL, cfg.GitBranch = bd.GitURL, bd.GitBranch
		cfg.BuildContext, cfg.DockerfilePath = bd.BuildContext, bd.DockerfilePath
		cfg.BuildType = agentprotocol.BuildDockerfile
	} else {
		cfg.Image = ps.Source.Image
	}
	cfg.Docker = ps.Docker
	for _, f := range ps.Files {
		cfg.Files = append(cfg.Files, services.FileMount{Path: f.Path, Mode: f.Mode, Content: f.content})
	}
	if h := ps.Health; h != nil {
		cfg.Healthcheck = services.Healthcheck{
			Type: h.Type, Command: h.Command, IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
			StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds, SuccessThreshold: h.SuccessThreshold,
		}
	}
	return cfg
}

// writeEnv writes the service's variables, pointing references at the real slugs.
func (b *importer) writeEnv(ctx context.Context, tx pgx.Tx, ps ComposePlanService, row db.Service) error {
	has := false
	for _, e := range ps.Env {
		if !e.Skipped {
			has = true
		}
	}
	if !has {
		return nil
	}
	if b.s.Variables == nil {
		return apierr.Unprocessable("variables_unavailable", "this server build cannot write variables")
	}
	for _, e := range ps.Env {
		if e.Skipped {
			continue
		}
		val := remapRefs(e.raw, b.slugs)
		if err := b.s.Variables.UpsertTx(ctx, tx, row.ID, e.Key, val, e.Secret); err != nil {
			return fmt.Errorf("write variable %s.%s: %w", ps.Key, e.Key, err)
		}
	}
	return nil
}

// remapRefs renames the namespace of ${{ns.KEY}} references (planned slug →
// actual slug), leaving escapes and other text alone.
func remapRefs(v string, slugs map[string]string) string {
	if !strings.Contains(v, "${{") {
		return v
	}
	var out strings.Builder
	for i := 0; i < len(v); {
		switch {
		case strings.HasPrefix(v[i:], "$${{"):
			out.WriteString("$${{")
			i += 4
		case strings.HasPrefix(v[i:], "${{"):
			end := strings.Index(v[i:], "}}")
			if end < 0 {
				out.WriteString(v[i:])
				return out.String()
			}
			inner := v[i+3 : i+end]
			if ns, key, ok := strings.Cut(inner, "."); ok {
				if real, found := slugs[strings.TrimSpace(ns)]; found {
					inner = real + "." + key
				}
			}
			out.WriteString("${{" + inner + "}}")
			i += end + 2
		default:
			out.WriteByte(v[i])
			i++
		}
	}
	return out.String()
}

// domainTarget is what a domain points at: the service's port, or a
// component and port of a stack.
type domainTarget struct {
	port      int
	component string
	// slugLabel replaces the service slug as the first part of a generated hostname.
	slugLabel string
}

func (t domainTarget) label(slug string) string {
	if t.slugLabel != "" {
		return t.slugLabel
	}
	return slug
}

// addDomain attaches a hostname to the service's port (staged), like a template install.
func (b *importer) addDomain(ctx context.Context, tx pgx.Tx, row db.Service, d ComposePlanDomain) (host, warn string, err error) {
	port := services.DecodeConfig(row.Config).Port
	if port <= 0 {
		return "", "", nil
	}
	return b.addDomainTo(ctx, tx, row, d, domainTarget{port: port})
}

func (b *importer) addDomainTo(ctx context.Context, tx pgx.Tx, row db.Service, d ComposePlanDomain, t domainTarget) (host, warn string, err error) {
	q := db.New(tx)
	srv := b.srv
	if srv.ProxyStatus != nil && *srv.ProxyStatus == agentprotocol.ProxyError {
		return "", "", apierr.Unprocessable("proxy_unavailable", "the proxy on "+srv.Name+" is not running")
	}
	generated := d.Generate
	if generated {
		base, ok := domains.BaseDomain(srv)
		if !ok {
			return "", "No hostname was generated for " + row.Name + ": the server has neither a base domain nor a public IP. Add a domain in its settings.", nil
		}
		first := domains.GenerateHostname(t.label(row.Slug), b.env.Env.Slug, b.env.ProjectSlug, base)
		host = first
		for i := 2; ; i++ {
			n, err := q.CountDomainsByHostname(ctx, host)
			if err != nil {
				return "", "", err
			}
			if n == 0 {
				break
			}
			if i > 9 {
				return "", "", apierr.Conflict("domain_conflict", "no free generated hostname for "+row.Name)
			}
			label, rest, _ := strings.Cut(first, ".")
			suffix := "-" + strconv.Itoa(i)
			if len(label)+len(suffix) > 63 {
				label = strings.Trim(label[:63-len(suffix)], "-")
			}
			host = label + suffix + "." + rest
		}
	} else {
		host, err = domains.ValidateHostname(d.Host)
		if err != nil {
			return "", "", err
		}
	}
	dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
		ServiceID: row.ID, Hostname: host, TargetPort: int32(t.port), Component: t.component, Generated: generated, StatusMessage: domains.DNSGuidance(host, srv.PublicIp),
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return "", "", apierr.Conflict("domain_conflict", "the hostname "+host+" is already in use")
		}
		return "", "", err
	}
	if _, err := q.BumpServiceGeneration(ctx, row.ID); err != nil {
		return "", "", err
	}
	if err := b.s.Jobs.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
		return "", "", fmt.Errorf("enqueue dns check: %w", err)
	}
	return host, "", nil
}
