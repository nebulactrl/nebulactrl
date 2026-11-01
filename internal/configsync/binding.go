package configsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/envtemplate"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The git side of the project file: which repository, branch and path an
// environment's nebula.toml comes from, the read of that file on push, on
// source changes and on build checkouts, and the status that read leaves on the
// environment. GitSync is the one implementation; the plan/apply service
// (Service) and the HTTP layer consume it through Binding.

// Binding modes (EnvironmentConfigSource.Mode).
const (
	// BindingExplicit: the environment has its own (connection, repo, branch, path).
	BindingExplicit = "explicit"
	// BindingDerived: the environment's git services all build one repository
	// and branch, which is the binding.
	BindingDerived = "derived"
	// BindingNone: nothing is read and nothing is shown.
	BindingNone = "none"
)

// DefaultConfigPath is the file name looked for at the repository root.
const DefaultConfigPath = agentprotocol.ConfigFileName

// BindingInfo is the resolved binding of one environment.
type BindingInfo struct {
	EnvironmentID uuid.UUID
	ProjectID     uuid.UUID
	WorkspaceID   uuid.UUID
	ProjectSlug   string
	EnvSlug       string
	IsProduction  bool

	// Mode is BindingExplicit, BindingDerived or BindingNone; the fields below
	// are empty for none.
	Mode string
	// ConnectionID is the git connection the repository is read through. It is
	// uuid.Nil for a public repository (GitURL is set) and when the explicit
	// binding's connection was deleted.
	ConnectionID uuid.UUID
	// Repo is the repository's full name (owner/name).
	Repo string
	// GitURL is the https clone URL of a public repository binding (no connection).
	GitURL string
	Branch string
	// Path is the explicit path of the file ("" applies the path rule).
	Path string
	// Paths are the candidate paths of the file in search order: the explicit
	// path alone, else nebula.toml at the repository root, then
	// <buildContext>/nebula.toml when the binding's git services share one
	// build context that is not the root.
	Paths []string
	// LastPath is the path the last successful check read.
	LastPath  string
	AutoApply bool
	// GitServices are the environment's git services that build this
	// repository and branch (the ones whose server can read it for the agent
	// fallback), oldest first.
	GitServices []uuid.UUID
}

// Resolved reports whether the environment has a binding.
func (b BindingInfo) Resolved() bool { return b.Mode != BindingNone }

// ResolvedPath is the path the file was last read at, else the first candidate.
func (b BindingInfo) ResolvedPath() string {
	for _, p := range b.Paths {
		if p == b.LastPath {
			return p
		}
	}
	if len(b.Paths) > 0 {
		return b.Paths[0]
	}
	return ""
}

// Binding resolves where an environment's file lives and reads it; it also
// holds the EnvironmentConfigSource operations of the HTTP API. Resolve and
// Read perform no authorization (their callers authorized the request); Get,
// Put, Clear and Check authorize themselves.
type Binding interface {
	// Resolve returns the explicit, derived or empty binding of an environment.
	Resolve(ctx context.Context, environmentID uuid.UUID) (BindingInfo, error)
	// Read returns the file and the attachments it names at ref (a commit SHA,
	// or "" for the branch head). Input.Source is filled. A missing file is a
	// 404 config_not_found, an unreadable repository a 409 config_unreachable.
	Read(ctx context.Context, b BindingInfo, ref string) (Input, error)
	// Get returns the binding and the status of the last check (projects:read).
	Get(ctx context.Context, environmentID uuid.UUID) (EnvironmentConfigSource, error)
	// Put sets the explicit binding and/or auto-apply (projects:write; admin
	// when auto-apply turns on).
	Put(ctx context.Context, environmentID uuid.UUID, in UpdateSourceInput) (EnvironmentConfigSource, error)
	// Clear drops the explicit binding and auto-apply (projects:write).
	Clear(ctx context.Context, environmentID uuid.UUID) error
	// Check reads the file now (at sha, "" = branch head), stores the status
	// and returns it (projects:read).
	Check(ctx context.Context, environmentID uuid.UUID, sha string) (EnvironmentConfigSource, error)
}

// UpdateSourceInput is the body of PUT /environments/{environmentId}/config-source.
type UpdateSourceInput struct {
	ConnectionID *uuid.UUID `json:"connectionId,omitempty" doc:"Git connection to read the file through; give it together with repo and branch"`
	Repo         *string    `json:"repo,omitempty" doc:"Repository (owner/name) on the connection"`
	Branch       *string    `json:"branch,omitempty" doc:"Branch the file is read from"`
	Path         *string    `json:"path,omitempty" doc:"Path of the file in the repository; empty applies the default rule (nebula.toml at the root, else in the build context)"`
	AutoApply    *bool      `json:"autoApply,omitempty" doc:"Stage the file on every push to the branch. Needs the admin scope, a person (not an agent) and a non-production environment. Never deploys."`
}

// EnvironmentConfigService is one service of the last check.
type EnvironmentConfigService struct {
	Slug    string `json:"slug"`
	State   string `json:"state" enum:"in_sync,file_changed,drift,conflict,new,adopt"`
	Changes int    `json:"changes" doc:"Number of fields the file would change"`
}

// EnvironmentConfigSource is the binding of an environment and the result of
// its last check.
type EnvironmentConfigSource struct {
	Mode         string                     `json:"mode" enum:"explicit,derived,none" doc:"explicit: set on the environment; derived: the environment's git services share one repository and branch; none: nothing is read"`
	ConnectionID *uuid.UUID                 `json:"connectionId,omitempty"`
	Repo         string                     `json:"repo"`
	Branch       string                     `json:"branch"`
	Path         string                     `json:"path" doc:"Explicit path; empty when the default rule applies"`
	ResolvedPath string                     `json:"resolvedPath" doc:"Path the file is read from (or was last read from)"`
	AutoApply    bool                       `json:"autoApply"`
	Status       string                     `json:"status" enum:"unknown,in_sync,differs,invalid,not_found,unreachable,error"`
	StatusDetail string                     `json:"statusDetail" doc:"Why the status is what it is: the first error of the file, why it could not be read, or why auto-apply refused"`
	Commit       string                     `json:"commit" doc:"Commit the file was read at (empty when it was read at the branch head through an API)"`
	FileSHA256   string                     `json:"fileSha256"`
	CheckedAt    *time.Time                 `json:"checkedAt,omitempty"`
	Services     []EnvironmentConfigService `json:"services"`
}

// Planner is the plan/apply service: *Service.
type Planner interface {
	Plan(ctx context.Context, projectID uuid.UUID, r PlanRequest) (Plan, error)
	Apply(ctx context.Context, projectID uuid.UUID, r ApplyRequest) (ApplyResult, error)
}

// FileReader reads single files of one repository at one ref.
// *gitproviders.RepoReader implements it.
type FileReader interface {
	Get(ctx context.Context, path string) ([]byte, error)
}

// RepoOpener opens readers on repositories without authorizing the caller
// (the callers are background jobs and requests already authorized).
type RepoOpener interface {
	// OpenRepo reads repo through a git connection at ref (a branch or a commit SHA).
	OpenRepo(ctx context.Context, connectionID uuid.UUID, repo, ref string) (FileReader, error)
	// OpenPublicRepo reads a public https repository on github.com or gitlab.com at ref.
	OpenPublicRepo(ctx context.Context, repoURL, ref string) (FileReader, error)
}

// ProviderRepos adapts the git providers service to RepoOpener.
func ProviderRepos(s *gitproviders.Service) RepoOpener { return providerRepos{s} }

type providerRepos struct{ s *gitproviders.Service }

func (p providerRepos) OpenRepo(ctx context.Context, connectionID uuid.UUID, repo, ref string) (FileReader, error) {
	r, err := p.s.OpenRepoInternal(ctx, connectionID, repo, ref)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (p providerRepos) OpenPublicRepo(ctx context.Context, repoURL, ref string) (FileReader, error) {
	r, err := p.s.OpenPublicRepo(ctx, repoURL, ref)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ConfigScanner reads files of a git service's repository on its build host
// (the fallback for repositories without an API reader). *deployments.Service
// implements it.
type ConfigScanner interface {
	ScanConfigFiles(ctx context.Context, sc services.Scope, branch string, paths []string) (agentprotocol.EnvScanResult, error)
}

// GitSyncDeps are the collaborators of GitSync.
type GitSyncDeps struct {
	Pool   *pgxpool.Pool
	Bus    *events.Bus
	Repos  RepoOpener
	Agent  ConfigScanner
	Logger *slog.Logger
	// ScanDelay is how long a check after a binding or source change waits
	// (default 3 s); tests shorten it.
	ScanDelay time.Duration
}

// GitSync implements Binding and the git triggers of the project file: the
// pre-push hook (gitproviders.PushHook), the source-change hook
// (services.SourceHook) and the build-checkout scan (agents.SourceScanSink).
type GitSync struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	bus   *events.Bus
	repos RepoOpener
	agent ConfigScanner
	log   *slog.Logger
	now   func() time.Time

	planner Planner

	// scanDelay is how long a source-change check waits (the create dialog
	// links a new service to its repository a moment after creating it).
	scanDelay time.Duration

	mu      sync.Mutex
	envMu   map[uuid.UUID]*sync.Mutex
	attach  map[uuid.UUID][]string  // attachments the last read of an environment named
	pending map[uuid.UUID]time.Time // source-change checks scheduled, by environment
	wg      sync.WaitGroup
}

var _ Binding = (*GitSync)(nil)

// NewGitSync returns a GitSync (Pool may be nil for OpenAPI generation). Give it
// the plan/apply service with SetPlanner before it is triggered.
func NewGitSync(d GitSyncDeps) *GitSync {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	g := &GitSync{
		pool: d.Pool, bus: d.Bus, repos: d.Repos, agent: d.Agent, log: d.Logger.With("component", "configsync-git"),
		now: time.Now, scanDelay: 3 * time.Second,
		envMu: map[uuid.UUID]*sync.Mutex{}, attach: map[uuid.UUID][]string{}, pending: map[uuid.UUID]time.Time{},
	}
	if d.Pool != nil {
		g.q = db.New(d.Pool)
	}
	if d.ScanDelay > 0 {
		g.scanDelay = d.ScanDelay
	}
	return g
}

// SetPlanner installs the plan/apply service (wire time only).
func (g *GitSync) SetPlanner(p Planner) { g.planner = p }

// Wait blocks until every background check started so far has finished.
func (g *GitSync) Wait() { g.wg.Wait() }

// ---- resolution ----

// repoKey identifies the repository and branch a service builds.
type repoKey struct {
	conn   uuid.UUID
	repo   string // lower-cased owner/name of a linked repository
	url    string // normalised https URL of a public repository
	branch string
}

type gitService struct {
	id   uuid.UUID
	key  repoKey
	repo string // owner/name as stored (linked) or derived from the URL (public)
	url  string // clone URL as configured (public)
	ctx  string // cleaned build context ("" = root)
	// ctxErr is set when the build context is not a clean relative path.
	ctxErr bool
	server bool // the service has a server assigned
}

// publicURL normalises the https clone URL of a public repository; ok is false
// for anything else (SSH, credentials in the URL, other schemes).
func publicURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "https://") || strings.ContainsAny(s, "@?#") {
		return "", false
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	return strings.ToLower(s), true
}

// Resolve implements Binding.
func (g *GitSync) Resolve(ctx context.Context, environmentID uuid.UUID) (BindingInfo, error) {
	env, err := g.q.GetEnvironmentInternal(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingInfo{}, apierr.NotFound("environment")
	}
	if err != nil {
		return BindingInfo{}, fmt.Errorf("load environment: %w", err)
	}
	info := BindingInfo{
		EnvironmentID: env.Environment.ID, ProjectID: env.Environment.ProjectID, WorkspaceID: env.WorkspaceID,
		ProjectSlug: env.ProjectSlug, EnvSlug: env.Environment.Slug, IsProduction: env.Environment.IsProduction, Mode: BindingNone,
	}
	if env.Environment.Kind != "standard" {
		return info, nil // a pull-request preview never reads a file
	}
	settings, err := g.q.GetEnvironmentConfigSettings(ctx, environmentID)
	hasRow := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return BindingInfo{}, fmt.Errorf("load config settings: %w", err)
	}
	if hasRow {
		info.AutoApply, info.LastPath = settings.AutoApply, settings.LastPath
	}
	gitSvcs, err := g.gitServices(ctx, environmentID)
	if err != nil {
		return BindingInfo{}, err
	}

	var key repoKey
	switch {
	case hasRow && settings.RepoFullName != "":
		info.Mode, info.Repo, info.Branch, info.Path = BindingExplicit, settings.RepoFullName, settings.Branch, settings.Path
		if settings.ConnectionID != nil {
			info.ConnectionID = *settings.ConnectionID
		}
		key = repoKey{conn: info.ConnectionID, repo: strings.ToLower(info.Repo), branch: info.Branch}
	default:
		k, ok := deriveKey(gitSvcs)
		if !ok {
			info.AutoApply = false // there is nothing to apply from
			return info, nil
		}
		key = k
		info.Mode, info.Branch = BindingDerived, k.branch
		for _, s := range gitSvcs {
			if s.key == k {
				info.ConnectionID, info.Repo, info.GitURL = k.conn, s.repo, ""
				if k.conn == uuid.Nil {
					info.GitURL = s.url
				}
				break
			}
		}
	}

	var matching []gitService
	for _, s := range gitSvcs {
		if s.key == key {
			matching = append(matching, s)
		}
	}
	for _, s := range matching {
		info.GitServices = append(info.GitServices, s.id)
	}
	info.Paths = candidatePaths(info.Path, matching)
	return info, nil
}

// gitServices lists the environment's live git services with the repository
// each builds.
func (g *GitSync) gitServices(ctx context.Context, environmentID uuid.UUID) ([]gitService, error) {
	rows, err := g.q.ListAllEnvironmentServices(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	links, err := g.q.ListEnvironmentGitLinks(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list git links: %w", err)
	}
	linked := make(map[uuid.UUID]db.ListEnvironmentGitLinksRow, len(links))
	for _, l := range links {
		linked[l.ServiceID] = l
	}
	var out []gitService
	for _, svc := range rows {
		if svc.SourceType != services.SourceGit {
			continue
		}
		cfg := services.DecodeConfig(svc.Config)
		gs := gitService{id: svc.ID, server: svc.ServerID != nil}
		if cd, err := envtemplate.CleanContext(cfg.BuildContext); err != nil {
			gs.ctxErr = true
		} else {
			gs.ctx = cd
		}
		if l, ok := linked[svc.ID]; ok {
			gs.repo = l.RepoFullName
			gs.key = repoKey{conn: l.ConnectionID, repo: strings.ToLower(l.RepoFullName), branch: cfg.GitBranch}
		} else if u, ok := publicURL(cfg.GitURL); ok {
			gs.url = strings.TrimSpace(cfg.GitURL)
			gs.repo = gitproviders.RepoFullNameFromURL(cfg.GitURL)
			gs.key = repoKey{url: u, branch: cfg.GitBranch}
		} else {
			continue // SSH or token URL without a link: no repository to read through
		}
		out = append(out, gs)
	}
	return out, nil
}

// deriveKey is the derived binding: the repository and branch every linked git
// service of the environment shares. Without linked services it is the public
// repository all public ones share; ambiguity and emptiness give no binding.
func deriveKey(svcs []gitService) (repoKey, bool) {
	var linked, public []gitService
	for _, s := range svcs {
		if s.key.conn != uuid.Nil {
			linked = append(linked, s)
		} else {
			public = append(public, s)
		}
	}
	set := linked
	if len(set) == 0 {
		set = public
	}
	if len(set) == 0 {
		return repoKey{}, false
	}
	for _, s := range set[1:] {
		if s.key != set[0].key {
			return repoKey{}, false
		}
	}
	return set[0].key, true
}

// candidatePaths applies the path rule: an explicit path alone; else the
// repository root, then <buildContext>/nebula.toml when the binding's git
// services share one build context that is not the root.
func candidatePaths(explicit string, matching []gitService) []string {
	if explicit != "" {
		return []string{explicit}
	}
	paths := []string{DefaultConfigPath}
	if len(matching) == 0 {
		return paths
	}
	dir := matching[0].ctx
	for _, s := range matching {
		if s.ctxErr || s.ctx != dir {
			return paths
		}
	}
	if dir != "" {
		if p := dir + "/" + DefaultConfigPath; agentprotocol.ValidConfigPath(p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// ---- views ----

// view renders the binding and the last check of an environment.
func (g *GitSync) view(info BindingInfo, row *db.EnvironmentConfigSetting) EnvironmentConfigSource {
	out := EnvironmentConfigSource{
		Mode: info.Mode, Repo: info.Repo, Branch: info.Branch, Path: info.Path, ResolvedPath: info.ResolvedPath(),
		AutoApply: info.AutoApply, Status: EnvUnknown, Services: []EnvironmentConfigService{},
	}
	if info.ConnectionID != uuid.Nil {
		id := info.ConnectionID
		out.ConnectionID = &id
	}
	if info.Mode == BindingNone || row == nil {
		return out
	}
	out.Status, out.StatusDetail, out.Commit, out.FileSHA256, out.CheckedAt = row.Status, row.StatusDetail, row.LastCommitSha, row.LastFileSha256, row.CheckedAt
	if lp, ok := decodeLastPlan(row.LastPlan); ok {
		for slug, s := range lp.Services {
			if s.Action == ActionNotInFile {
				continue
			}
			state := s.State
			if state == "" {
				state = StateInSync
			}
			out.Services = append(out.Services, EnvironmentConfigService{Slug: slug, State: state, Changes: s.Changes})
		}
		sortServices(out.Services)
	}
	return out
}

func sortServices(s []EnvironmentConfigService) {
	sort.Slice(s, func(i, j int) bool { return s[i].Slug < s[j].Slug })
}

func (g *GitSync) settingsRow(ctx context.Context, environmentID uuid.UUID) (*db.EnvironmentConfigSetting, error) {
	row, err := g.q.GetEnvironmentConfigSettings(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load config settings: %w", err)
	}
	return &row, nil
}

func (g *GitSync) viewOf(ctx context.Context, environmentID uuid.UUID) (EnvironmentConfigSource, error) {
	info, err := g.Resolve(ctx, environmentID)
	if err != nil {
		return EnvironmentConfigSource{}, err
	}
	row, err := g.settingsRow(ctx, environmentID)
	if err != nil {
		return EnvironmentConfigSource{}, err
	}
	return g.view(info, row), nil
}

func (g *GitSync) publish(info BindingInfo) {
	services.Publish(g.bus, services.EventConfigUpdated, info.ProjectID, info.EnvironmentID, info.EnvironmentID)
}

// ---- Binding: Get, Put, Clear, Check ----

// Get implements Binding.
func (g *GitSync) Get(ctx context.Context, environmentID uuid.UUID) (EnvironmentConfigSource, error) {
	if _, err := services.LoadEnvironment(ctx, g.q, policy.ScopeProjectsRead, environmentID); err != nil {
		return EnvironmentConfigSource{}, err
	}
	return g.viewOf(ctx, environmentID)
}

var (
	repoNameRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	branchBadRe  = regexp.MustCompile(`[\x00-\x20\x7f~^:?*\[\\]`)
	commitShaRe  = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	errNoBinding = apierr.Unprocessable("config_source_required", "this environment has no repository binding: set a connection, repository and branch, or link a git service")
)

// Put implements Binding.
func (g *GitSync) Put(ctx context.Context, environmentID uuid.UUID, in UpdateSourceInput) (EnvironmentConfigSource, error) {
	es, err := services.LoadEnvironment(ctx, g.q, policy.ScopeProjectsWrite, environmentID)
	if err != nil {
		return EnvironmentConfigSource{}, err
	}
	if es.Env.Kind != "standard" {
		return EnvironmentConfigSource{}, apierr.Unprocessable("not_applicable", "pull-request previews do not read a nebula.toml")
	}
	cur, err := g.Resolve(ctx, environmentID)
	if err != nil {
		return EnvironmentConfigSource{}, err
	}

	bind := in.ConnectionID != nil || in.Repo != nil || in.Branch != nil
	if bind && (in.ConnectionID == nil || in.Repo == nil || in.Branch == nil) {
		return EnvironmentConfigSource{}, apierr.Unprocessable("validation_failed", "connectionId, repo and branch are set together")
	}
	next := cur // the explicit binding after this call
	if bind {
		repo, branch := strings.TrimSpace(*in.Repo), strings.TrimSpace(*in.Branch)
		if len(repo) > 200 || !repoNameRe.MatchString(repo) || strings.Contains(repo, "..") {
			return EnvironmentConfigSource{}, apierr.Unprocessable("validation_failed", "repo must look like owner/name")
		}
		if branch == "" || len(branch) > 250 || branchBadRe.MatchString(branch) || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
			return EnvironmentConfigSource{}, apierr.Unprocessable("validation_failed", "branch is not a valid branch name")
		}
		ws := es.WorkspaceID
		if _, err := g.q.GetGitConnection(ctx, db.GetGitConnectionParams{ID: *in.ConnectionID, WorkspaceID: ws}); errors.Is(err, pgx.ErrNoRows) {
			return EnvironmentConfigSource{}, apierr.Unprocessable("connection_not_found", "the git connection does not exist")
		} else if err != nil {
			return EnvironmentConfigSource{}, fmt.Errorf("load git connection: %w", err)
		}
		next.Mode, next.ConnectionID, next.Repo, next.Branch, next.GitURL = BindingExplicit, *in.ConnectionID, repo, branch, ""
		next.Path = ""
		if cur.Mode == BindingExplicit {
			next.Path = cur.Path
		}
	}
	if in.Path != nil {
		p := strings.TrimSpace(*in.Path)
		if p != "" && !agentprotocol.ValidConfigPath(p) {
			return EnvironmentConfigSource{}, apierr.Unprocessable("validation_failed", "path must be a relative file path inside the repository")
		}
		next.Path = p
	}
	pathOnly := !bind && in.Path != nil
	if pathOnly && next.Path != cur.Path {
		// A path pins the binding the environment follows today.
		if cur.Mode == BindingNone || cur.ConnectionID == uuid.Nil {
			return EnvironmentConfigSource{}, errNoBinding
		}
		next.Mode = BindingExplicit
	}
	explicitChange := bind || (pathOnly && next.Path != cur.Path)

	if in.AutoApply != nil && *in.AutoApply {
		p, err := policy.Authenticated(ctx)
		if err != nil {
			return EnvironmentConfigSource{}, err
		}
		if p.Type == auth.TypeMCP || prodguard.Guarded(p) {
			return EnvironmentConfigSource{}, apierr.New(403, "forbidden", "an agent cannot switch on auto-apply: ask a person with the admin scope")
		}
		if err := policy.Require(ctx, policy.ScopeAdmin, &es.Env.ProjectID); err != nil {
			return EnvironmentConfigSource{}, err
		}
		if es.Env.IsProduction {
			return EnvironmentConfigSource{}, apierr.Unprocessable(CodeProductionAutoApplyOff, "auto-apply is not available for a production environment: production shows the diff and a person applies it")
		}
		if next.Mode == BindingNone {
			return EnvironmentConfigSource{}, errNoBinding
		}
	}

	err = db.InTx(ctx, g.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if explicitChange {
			conn := next.ConnectionID
			if err := q.UpsertEnvironmentConfigBinding(ctx, db.UpsertEnvironmentConfigBindingParams{
				EnvironmentID: environmentID, WorkspaceID: es.WorkspaceID, ConnectionID: &conn,
				RepoFullName: next.Repo, Branch: next.Branch, Path: next.Path,
			}); err != nil {
				return err
			}
		}
		if in.AutoApply != nil {
			if err := q.SetEnvironmentConfigAutoApply(ctx, db.SetEnvironmentConfigAutoApplyParams{
				EnvironmentID: environmentID, WorkspaceID: es.WorkspaceID, AutoApply: *in.AutoApply,
			}); err != nil {
				return err
			}
		}
		meta := map[string]any{"mode": next.Mode, "repo": next.Repo, "branch": next.Branch, "path": next.Path}
		if in.AutoApply != nil {
			meta["autoApply"] = *in.AutoApply
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "config.source.update", ResourceType: "environment", ResourceID: environmentID.String(),
			ProjectID: &es.Env.ProjectID, Metadata: meta,
		})
	})
	if err != nil {
		return EnvironmentConfigSource{}, fmt.Errorf("update config source: %w", err)
	}
	g.publish(cur)
	if explicitChange {
		g.refresh(environmentID)
	}
	return g.viewOf(ctx, environmentID)
}

// Clear implements Binding.
func (g *GitSync) Clear(ctx context.Context, environmentID uuid.UUID) error {
	es, err := services.LoadEnvironment(ctx, g.q, policy.ScopeProjectsWrite, environmentID)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, g.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).ClearEnvironmentConfigBinding(ctx, environmentID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "config.source.clear", ResourceType: "environment", ResourceID: environmentID.String(), ProjectID: &es.Env.ProjectID,
		})
	})
	if err != nil {
		return fmt.Errorf("clear config source: %w", err)
	}
	services.Publish(g.bus, services.EventConfigUpdated, es.Env.ProjectID, environmentID, environmentID)
	g.refresh(environmentID)
	return nil
}

// Check implements Binding.
func (g *GitSync) Check(ctx context.Context, environmentID uuid.UUID, sha string) (EnvironmentConfigSource, error) {
	if _, err := services.LoadEnvironment(ctx, g.q, policy.ScopeProjectsRead, environmentID); err != nil {
		return EnvironmentConfigSource{}, err
	}
	if sha = strings.TrimSpace(sha); sha != "" && !commitShaRe.MatchString(sha) {
		return EnvironmentConfigSource{}, apierr.Unprocessable("validation_failed", "sha must be a commit id (7 to 64 hex digits)")
	}
	info, err := g.Resolve(ctx, environmentID)
	if err != nil {
		return EnvironmentConfigSource{}, err
	}
	if info.Mode != BindingNone {
		if g.planner == nil {
			return EnvironmentConfigSource{}, errors.New("project config planner is not configured")
		}
		g.check(ctx, info, checkOpts{ref: sha, force: true})
	}
	return g.viewOf(ctx, environmentID)
}
