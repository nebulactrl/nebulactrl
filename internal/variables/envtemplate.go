package variables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/envtemplate"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The template env files (.env.example, .env.sample, .env.template, .env.dist)
// of a git service are read before its first deploy:
//
//   - through the git provider's API when the service is linked to a
//     connection (GitHub, GitLab, Gitea, Forgejo), or the URL is a public
//     repository on github.com or gitlab.com;
//   - otherwise (Bitbucket, any other HTTPS host, SSH, a private repository
//     behind a token, a provider that fails) by a scan-only agent command on
//     the service's server: a depth-1 fetch into memory, nothing checked out.
//
// The files found are stored as the service's suggestion source
// (services.suggested_env), re-read when the source changes and on every push
// to the tracked branch. .env, .env.local and .env.production are never read.

// Read methods reported by EnvTemplateScan.
const (
	TemplateViaProvider = "provider"
	TemplateViaAgent    = "agent"
	TemplateViaNone     = "none"
)

// Template scan statuses.
const (
	TemplatesFound       = "found"
	TemplatesNone        = "none"
	TemplatesUnavailable = "unavailable"
)

// GitReaders reads repository files through git providers. *gitproviders.Service
// implements it.
type GitReaders interface {
	OpenRepo(ctx context.Context, connectionID uuid.UUID, repo, branch string) (*gitproviders.RepoReader, error)
	OpenRepoInternal(ctx context.Context, connectionID uuid.UUID, repo, branch string) (*gitproviders.RepoReader, error)
	OpenPublicRepo(ctx context.Context, repoURL, branch string) (*gitproviders.RepoReader, error)
	ServiceRepo(ctx context.Context, serviceID uuid.UUID) (connectionID uuid.UUID, repo string, ok bool, err error)
}

// AgentScanner reads template files on an agent. *deployments.Service
// implements it.
type AgentScanner interface {
	ScanEnvTemplates(ctx context.Context, sc services.Scope) (agentprotocol.EnvScanResult, error)
	PreviewEnvTemplates(ctx context.Context, in deployments.EnvPreviewInput) (agentprotocol.EnvScanResult, error)
}

// EnvTemplateDeps are the collaborators of the template reads (set at wire time).
type EnvTemplateDeps struct {
	Git    GitReaders
	Agent  AgentScanner
	Jobs   *jobs.Manager
	Logger *slog.Logger
}

type envTemplates struct {
	git   GitReaders
	agent AgentScanner
	jobs  *jobs.Manager
	log   *slog.Logger
	// pending debounces the schedule of reads after source changes.
	pending *scanDebounce
}

// SetEnvTemplates installs the collaborators of the template reads. Without
// them only the agent's checkout scan (HandleSourceScan) feeds suggestions.
func (s *Service) SetEnvTemplates(d EnvTemplateDeps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s.tpl = envTemplates{git: d.Git, agent: d.Agent, jobs: d.Jobs, log: d.Logger, pending: &scanDebounce{at: map[uuid.UUID]time.Time{}}}
}

// EnvTemplateFile is one template file that was found.
type EnvTemplateFile struct {
	Path string `json:"path"`
	Keys int    `json:"keys" doc:"Variables the file lists"`
}

// EnvTemplateScan is the result of reading a repository's template env files.
type EnvTemplateScan struct {
	Status  string              `json:"status" enum:"found,none,unavailable" doc:"found: files with keys exist; none: the repository has no template file; unavailable: it could not be read (see message)"`
	Method  string              `json:"method" enum:"provider,agent,none" doc:"provider: read through the git provider's API; agent: read on the server"`
	Message string              `json:"message"`
	Commit  string              `json:"commit,omitempty" doc:"Commit the files were read from (agent reads only)"`
	Branch  string              `json:"branch,omitempty"`
	Files   []EnvTemplateFile   `json:"files"`
	Items   []SuggestedVariable `json:"items" doc:"One suggestion per key not set yet, with smart defaults (database references, generators, safe example values)"`
	Skipped []SkippedVariable   `json:"skipped"`
}

// EnvTemplatePreviewInput is the repository of a service that does not exist yet.
type EnvTemplatePreviewInput struct {
	// Either ConnectionID with Repo, or GitURL.
	ConnectionID *uuid.UUID `json:"connectionId,omitempty" doc:"Git connection the repository is read through"`
	Repo         string     `json:"repo,omitempty" doc:"owner/name on the connection"`
	GitURL       string     `json:"gitUrl,omitempty" doc:"Clone URL of the repository (https, public or with gitToken)"`
	Branch       string     `json:"branch,omitempty" doc:"Defaults to the repository's default branch"`
	BuildContext string     `json:"buildContext,omitempty" doc:"Build context directory, read as well as the repository root"`
	GitToken     string     `json:"gitToken,omitempty" doc:"Access token of a private repository, used for this read only and never stored"`
	ServerID     *uuid.UUID `json:"serverId,omitempty" doc:"The server a read that needs an agent runs on"`
}

// templateSource says where a repository's template files are read from.
type templateSource struct {
	// service is the existing service (nil for a preview).
	service    *services.Scope
	connID     uuid.UUID
	repo       string
	gitURL     string
	branch     string
	contextDir string
	// token is a pasted token (preview); hasToken a stored one (service).
	token    string
	hasToken bool
	serverID uuid.UUID
	// authorized: the caller's access to connID must be checked.
	authorized bool
}

type templateRead struct {
	files  []agentprotocol.EnvFileScan
	commit string
	branch string
	method string
}

// unavailable is why a read could not be made.
type unavailable struct {
	message string
	// retry: trying again later may succeed (server offline, timeout).
	retry bool
}

// errText returns the API message of err, else its text, and its code.
func errText(err error) (msg, code string) {
	if ae, ok := apierr.As(err); ok {
		return ae.Message, ae.Code
	}
	return err.Error(), ""
}

func (s *Service) openReader(ctx context.Context, src templateSource) (*gitproviders.RepoReader, error) {
	switch {
	case src.connID != uuid.Nil:
		if src.authorized {
			return s.tpl.git.OpenRepo(ctx, src.connID, src.repo, src.branch)
		}
		return s.tpl.git.OpenRepoInternal(ctx, src.connID, src.repo, src.branch)
	case src.token == "" && !src.hasToken && strings.HasPrefix(src.gitURL, "https://"):
		r, err := s.tpl.git.OpenPublicRepo(ctx, src.gitURL, src.branch)
		if err != nil {
			if _, code := errText(err); code == "unsupported_host" || code == "validation_failed" {
				return nil, nil // not a public GitHub/GitLab.com repository: the agent reads it
			}
			return nil, err
		}
		return r, nil
	}
	return nil, nil
}

// readTemplates reads the template files of src: through the provider when it
// can, otherwise on the agent.
func (s *Service) readTemplates(ctx context.Context, src templateSource) (templateRead, *unavailable) {
	note := "" // why the provider route did not answer; shown when the agent cannot either
	if s.tpl.git != nil {
		reader, err := s.openReader(ctx, src)
		switch {
		case err != nil:
			var code string
			if note, code = errText(err); code == "no_repo_api" {
				note = "This git connection has no API access, so the repository is read on the server."
			}
		case reader != nil:
			files, err := gitproviders.ReadEnvTemplates(ctx, reader, src.contextDir)
			if err == nil {
				return templateRead{files: files, branch: reader.Branch, method: TemplateViaProvider}, nil
			}
			note, _ = errText(err)
			note = "The git provider could not be read: " + note
		}
	}
	return s.readViaAgent(ctx, src, note)
}

func (s *Service) readViaAgent(ctx context.Context, src templateSource, note string) (templateRead, *unavailable) {
	fail := func(msg string, retry bool) (templateRead, *unavailable) {
		if note != "" && msg == "" {
			msg = note
		}
		return templateRead{}, &unavailable{message: msg, retry: retry}
	}
	if s.tpl.agent == nil {
		return fail(note, false)
	}
	var (
		res agentprotocol.EnvScanResult
		err error
	)
	switch {
	case src.service != nil:
		res, err = s.tpl.agent.ScanEnvTemplates(ctx, *src.service)
	case gitssh.LooksLikeSSH(src.gitURL):
		return fail("An SSH repository is read once the service exists and has a deploy key.", false)
	case src.connID != uuid.Nil:
		return fail("This repository is read once the service exists and is linked to the connection.", false)
	case src.serverID == uuid.Nil:
		return fail("Pick a server: this repository is read on the server that builds it.", false)
	default:
		res, err = s.tpl.agent.PreviewEnvTemplates(ctx, deployments.EnvPreviewInput{
			ServerID: src.serverID, GitURL: src.gitURL, Branch: src.branch, BuildContext: src.contextDir, Token: src.token,
		})
	}
	if err != nil {
		msg, code := errText(err)
		return fail(msg, code == "server_offline" || code == "env_scan_timeout")
	}
	return templateRead{files: res.Files, commit: res.Commit, branch: src.branch, method: TemplateViaAgent}, nil
}

// serviceSource describes the repository of an existing git service.
func (s *Service) serviceSource(ctx context.Context, sc services.Scope, authorized bool) templateSource {
	cfg := sc.Config()
	src := templateSource{
		service: &sc, gitURL: cfg.GitURL, branch: cfg.GitBranch, contextDir: cfg.BuildContext,
		hasToken: len(sc.Service.GitTokenEnc) > 0, authorized: authorized,
	}
	if s.tpl.git != nil {
		if id, repo, ok, err := s.tpl.git.ServiceRepo(ctx, sc.Service.ID); err == nil && ok {
			src.connID, src.repo = id, repo
		}
	}
	return src
}

// storeTemplates saves what was read as the service's suggestion source and
// tells the live-update feed. Reading no file at all is stored too, so keys
// that were removed from the example stop being suggested.
func (s *Service) storeTemplates(ctx context.Context, sc services.Scope, r templateRead) (services.Scope, error) {
	clean := suggestedEnv{Commit: r.commit, Branch: r.branch, Files: []agentprotocol.EnvFileScan{}}
	for _, f := range r.files {
		if len(clean.Files) >= 8 || len(f.Path) > 200 {
			break
		}
		clean.Files = append(clean.Files, f)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return sc, err
	}
	if err := s.q.SetServiceSuggestedEnv(ctx, db.SetServiceSuggestedEnvParams{ID: sc.Service.ID, SuggestedEnv: raw}); err != nil {
		return sc, fmt.Errorf("store suggested env: %w", err)
	}
	sc.Service.SuggestedEnv = raw
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return sc, nil
}

func scanView(r templateRead, items []SuggestedVariable, skipped []SkippedVariable) EnvTemplateScan {
	out := EnvTemplateScan{Method: r.method, Commit: r.commit, Branch: r.branch, Files: []EnvTemplateFile{}, Items: items, Skipped: skipped}
	if out.Items == nil {
		out.Items = []SuggestedVariable{}
	}
	if out.Skipped == nil {
		out.Skipped = []SkippedVariable{}
	}
	keys := 0
	for _, f := range r.files {
		out.Files = append(out.Files, EnvTemplateFile{Path: f.Path, Keys: len(f.Vars)})
		keys += len(f.Vars)
	}
	if len(out.Files) == 0 {
		out.Status, out.Method = TemplatesNone, TemplateViaNone
		out.Message = "No " + strings.Join(envtemplate.Files, ", ") + " in the repository root or the build context."
		return out
	}
	out.Status = TemplatesFound
	out.Message = fmt.Sprintf("%d %s in %d %s.", keys, plural(keys, "key", "keys"), len(out.Files), plural(len(out.Files), "file", "files"))
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func unavailableView(u *unavailable) EnvTemplateScan {
	msg := u.message
	if msg == "" {
		msg = "The repository could not be read."
	}
	return EnvTemplateScan{
		Status: TemplatesUnavailable, Method: TemplateViaNone, Message: msg,
		Files: []EnvTemplateFile{}, Items: []SuggestedVariable{}, Skipped: []SkippedVariable{},
	}
}

// ScanTemplates reads the template env files of a git service again, stores
// them as its suggestion source and returns the keys not set yet. A repository
// that cannot be read is not an error: the result says why (status
// unavailable) and the stored files are left alone.
func (s *Service) ScanTemplates(ctx context.Context, serviceID uuid.UUID) (EnvTemplateScan, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return EnvTemplateScan{}, err
	}
	if sc.Service.SourceType != services.SourceGit {
		return EnvTemplateScan{}, apierr.Unprocessable("not_git_source", "Template env files are read only from a git source.")
	}
	read, un := s.readTemplates(ctx, s.serviceSource(ctx, sc, true))
	if un != nil {
		return unavailableView(un), nil
	}
	if sc, err = s.storeTemplates(ctx, sc, read); err != nil {
		return EnvTemplateScan{}, err
	}
	list, err := s.suggestions(ctx, sc)
	if err != nil {
		return EnvTemplateScan{}, err
	}
	items := []SuggestedVariable{}
	for _, g := range list.Items {
		if g.Source == SuggestEnvExample {
			items = append(items, g)
		}
	}
	return scanView(read, items, list.Skipped), nil
}

// PreviewTemplates reads the template env files of a repository for a service
// that does not exist yet and returns the suggestions with the references
// the environment's databases would fill. Nothing is created or stored.
func (s *Service) PreviewTemplates(ctx context.Context, envID uuid.UUID, in EnvTemplatePreviewInput) (EnvTemplateScan, error) {
	if _, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID); err != nil {
		return EnvTemplateScan{}, err
	}
	src := templateSource{
		repo: strings.TrimSpace(in.Repo), gitURL: strings.TrimSpace(in.GitURL), branch: strings.TrimSpace(in.Branch),
		contextDir: in.BuildContext, token: in.GitToken, authorized: true,
	}
	if _, err := envtemplate.CleanContext(src.contextDir); err != nil {
		return EnvTemplateScan{}, apierr.Unprocessable("validation_failed", err.Error())
	}
	switch {
	case in.ConnectionID != nil && src.repo != "":
		src.connID = *in.ConnectionID
	case src.gitURL != "":
	default:
		return EnvTemplateScan{}, apierr.Unprocessable("validation_failed", "give a git connection and repository, or a repository URL")
	}
	if in.ServerID != nil {
		src.serverID = *in.ServerID
	}
	read, un := s.readTemplates(ctx, src)
	if un != nil {
		return unavailableView(un), nil
	}
	items, skipped, err := s.previewItems(ctx, envID, read.files)
	if err != nil {
		return EnvTemplateScan{}, err
	}
	return scanView(read, items, skipped), nil
}

// previewItems builds the suggestions of template files for a service that
// does not exist yet in environment envID: every database export of the
// environment can fill a key.
func (s *Service) previewItems(ctx context.Context, envID uuid.UUID, files []agentprotocol.EnvFileScan) ([]SuggestedVariable, []SkippedVariable, error) {
	info, err := services.LoadEnvInfo(ctx, s.q, envID)
	if err != nil {
		return nil, nil, err
	}
	res, err := services.LoadResolver(ctx, s.q, s.box, info)
	if err != nil {
		return nil, nil, err
	}
	providers, _ := exportProviders(res, "")
	items, skipped := exampleSuggestions(files, providers, nil)
	return items, skipped, nil
}

// ---- keeping the stored files current ----

// GitSourceChanged implements services.SourceHook: a git service was created
// or its repository, branch, build context, server or token changed, so its
// template files are read again. It only schedules a job.
func (s *Service) GitSourceChanged(ctx context.Context, sc services.Scope) {
	if s.tpl.jobs == nil || !s.tpl.pending.allow(sc.Service.ID, time.Now()) {
		return
	}
	// A few seconds of delay: the create dialog links the repository to a git
	// connection right after creating the service, and the job reads through
	// that link.
	opts := &river.InsertOpts{ScheduledAt: time.Now().Add(scanDelay)}
	if err := s.tpl.jobs.Insert(context.WithoutCancel(ctx), EnvTemplateScanArgs{ServiceID: sc.Service.ID}, opts); err != nil {
		s.tpl.log.Warn("schedule template scan", "service", sc.Service.ID.String(), "err", err)
	}
}

// scanDelay is how long a scheduled read waits before it runs.
const scanDelay = 3 * time.Second

// scanDebounce drops a trigger for a service whose read was scheduled less than
// scanDelay ago: that read has not run yet and will see this change too (the
// create dialog creates a service and links it a moment later).
type scanDebounce struct {
	mu sync.Mutex
	at map[uuid.UUID]time.Time
}

func (d *scanDebounce) allow(id uuid.UUID, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.at[id]; ok && now.Sub(t) < scanDelay {
		return false
	}
	for k, t := range d.at {
		if now.Sub(t) > time.Minute {
			delete(d.at, k)
		}
	}
	d.at[id] = now
	return true
}

// OnPush re-reads the template files of every service that tracks the pushed
// branch of the repository, so keys added to .env.example show up. A push that
// lists its changed files and touches no template file is skipped for services
// that were read before. Subscribe it with gitproviders.Service.SubscribePush.
func (s *Service) OnPush(ctx context.Context, ev gitproviders.PushEvent) {
	if s.tpl.git == nil {
		return
	}
	touched := !ev.PathsComplete
	for _, p := range ev.ChangedPaths {
		if isTemplateName(path.Base(p)) {
			touched = true
			break
		}
	}
	rows, err := s.q.ListServiceGitForRepo(ctx, db.ListServiceGitForRepoParams{ConnectionID: ev.ConnectionID, Lower: ev.RepoFullName})
	if err != nil {
		s.tpl.log.Warn("find services of pushed repository", "repo", ev.RepoFullName, "err", err)
		return
	}
	for _, row := range rows {
		if services.DecodeConfig(row.ServiceConfig).GitBranch != ev.Branch {
			continue
		}
		sc, err := services.LoadScopeInternal(ctx, s.q, row.ServiceGit.ServiceID)
		if err != nil || sc.Service.SourceType != services.SourceGit {
			continue
		}
		if !touched && len(sc.Service.SuggestedEnv) > 0 {
			continue
		}
		read, un := s.readTemplates(ctx, s.serviceSource(ctx, sc, false))
		if un != nil {
			s.tpl.log.Info("template re-read after push skipped", "service", sc.Service.Slug, "reason", un.message)
			continue
		}
		if _, err := s.storeTemplates(ctx, sc, read); err != nil {
			s.tpl.log.Warn("store templates after push", "service", sc.Service.Slug, "err", err)
		}
	}
}

func isTemplateName(name string) bool {
	for _, f := range envtemplate.Files {
		if name == f {
			return true
		}
	}
	return false
}

// EnvTemplateScanArgs reads one service's template env files.
type EnvTemplateScanArgs struct {
	ServiceID uuid.UUID `json:"serviceId"`
}

// Kind implements river.JobArgs.
func (EnvTemplateScanArgs) Kind() string { return "scan_env_templates" }

// InsertOpts implements river.JobArgsWithInsertOpts: a few retries for an
// offline server.
func (EnvTemplateScanArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 4}
}

type envTemplateScanWorker struct {
	river.WorkerDefaults[EnvTemplateScanArgs]
	s *Service
}

// Timeout implements river.Worker: an agent read waits up to 75 s.
func (w *envTemplateScanWorker) Timeout(*river.Job[EnvTemplateScanArgs]) time.Duration {
	return 2 * time.Minute
}

func (w *envTemplateScanWorker) Work(ctx context.Context, job *river.Job[EnvTemplateScanArgs]) error {
	sc, err := services.LoadScopeInternal(ctx, w.s.q, job.Args.ServiceID)
	if err != nil || sc.Service.SourceType != services.SourceGit {
		return nil //nolint:nilerr // deleted or no longer a git service: nothing to read
	}
	read, un := w.s.readTemplates(ctx, w.s.serviceSource(ctx, sc, false))
	if un != nil {
		w.s.tpl.log.Info("template scan unavailable", "service", sc.Service.Slug, "reason", un.message, "retry", un.retry)
		if un.retry {
			return errors.New(un.message)
		}
		return nil
	}
	_, err = w.s.storeTemplates(ctx, sc, read)
	return err
}

// ---- HTTP ----

type templateScanOutput struct{ Body EnvTemplateScan }

type templatePreviewInput struct {
	ID   uuid.UUID `path:"environmentId"`
	Body EnvTemplatePreviewInput
}

func registerEnvTemplates(h huma.API, svc *Service, errs422 []int) {
	tags := []string{"variables"}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "scanServiceEnvTemplates", Method: http.MethodPost, Path: "/services/{serviceId}/env-template-scan", Tags: tags,
		Summary: "Read a git service's .env.example files again",
		Description: "Reads `.env.example`, `.env.sample`, `.env.template` and `.env.dist` (never `.env` or `.env.local`) from the repository root and the " +
			"build context at the tracked branch: through the git provider's API when the service is linked to a connection or the repository is public on " +
			"github.com or gitlab.com, otherwise by a scan-only command on the service's server. The files become the suggestion source of " +
			"`GET /services/{serviceId}/suggested-variables`. Returns the keys not set yet with smart defaults; a repository that cannot be read " +
			"answers `status: unavailable` with the reason, and nothing changes. Reading re-runs on every push to the tracked branch.",
		Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *serviceIDInput) (*templateScanOutput, error) {
		r, err := svc.ScanTemplates(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &templateScanOutput{Body: r}, nil
	})
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "previewEnvTemplates", Method: http.MethodPost, Path: "/environments/{environmentId}/env-template-preview", Tags: tags,
		Summary: "Preview the .env.example keys of a repository before creating its service",
		Description: "Like the service scan, for a repository that has no service yet: nothing is created or stored. The suggestions can reference the databases " +
			"of the environment. Give a git connection and repository, or a repository URL (with `gitToken` for a private one); `serverId` is the server a " +
			"read that needs an agent (Bitbucket, other hosts, a pasted token) runs on. The pasted token is used for this read only and never stored. " +
			"A repository that cannot be read answers `status: unavailable` with the reason.",
		Errors: errs422,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *templatePreviewInput) (*templateScanOutput, error) {
		r, err := svc.PreviewTemplates(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &templateScanOutput{Body: r}, nil
	})
}
