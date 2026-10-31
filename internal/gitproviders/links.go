package gitproviders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)+$`)

// normalizeRepo accepts "owner/repo", "group/sub/repo" or a clone URL and
// returns the full name without ".git".
func normalizeRepo(raw string) (string, error) {
	r := strings.TrimSpace(raw)
	if strings.Contains(r, "://") || strings.Contains(r, "@") {
		r = RepoFullNameFromURL(r)
	}
	r = strings.TrimSuffix(strings.Trim(r, "/"), ".git")
	if !repoNameRe.MatchString(r) || len(r) > 300 || strings.Contains(r, "..") {
		return "", apierr.Unprocessable("validation_failed", "repo must look like owner/name")
	}
	return r, nil
}

func providerError(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return apierr.New(http.StatusBadGateway, "provider_error", "The git provider answered: "+ae.Error())
	}
	return apierr.New(http.StatusBadGateway, "provider_error", "The git provider could not be reached: "+err.Error())
}

// ---- repository picker ----

// ListRepos lists repositories the connection can access, filtered by query
// (case-insensitive substring of the name or description), at most limit.
func (s *Service) ListRepos(ctx context.Context, connectionID uuid.UUID, query string, limit int) ([]Repo, error) {
	c, err := s.loadConnection(ctx, policy.ScopeProjectsWrite, connectionID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	drv, cl, err := s.api(ctx, c)
	if errors.Is(err, errNoAPI) {
		return nil, apierr.Unprocessable("no_repo_api", "This connection cannot list repositories: enter the repository as owner/name.")
	}
	if err != nil {
		return nil, apierr.Unprocessable("credentials_unusable", err.Error())
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := []Repo{}
	for page := 1; page <= 10 && len(out) < limit; page++ {
		repos, more, err := drv.listRepos(ctx, cl, strings.TrimSpace(query), page)
		if err != nil {
			return nil, providerError(err)
		}
		for _, r := range repos {
			if q == "" || strings.Contains(strings.ToLower(r.FullName), q) || strings.Contains(strings.ToLower(r.Description), q) {
				out = append(out, r)
				if len(out) == limit {
					break
				}
			}
		}
		if !more {
			break
		}
	}
	return out, nil
}

// ListBranches lists a repository's branches.
func (s *Service) ListBranches(ctx context.Context, connectionID uuid.UUID, repo string) ([]string, error) {
	c, err := s.loadConnection(ctx, policy.ScopeProjectsWrite, connectionID)
	if err != nil {
		return nil, err
	}
	repo, err = normalizeRepo(repo)
	if err != nil {
		return nil, err
	}
	drv, cl, err := s.api(ctx, c)
	if errors.Is(err, errNoAPI) {
		return nil, apierr.Unprocessable("no_repo_api", "This connection cannot list branches: enter the branch name.")
	}
	if err != nil {
		return nil, apierr.Unprocessable("credentials_unusable", err.Error())
	}
	names, err := drv.listBranches(ctx, cl, repo)
	if isNotFound(err) {
		return nil, apierr.Unprocessable("repo_not_found", "The connection cannot access "+repo)
	}
	if err != nil {
		return nil, providerError(err)
	}
	return names, nil
}

// ---- service links ----

// HookStatusView says how the provider delivers events for a linked repository.
type HookStatusView struct {
	// Mode is app (GitHub's App-level webhook), registered (NebulaCtrl created
	// a repository webhook through the API) or manual (you create it).
	Mode    string `json:"mode" enum:"app,registered,manual"`
	Status  string `json:"status" enum:"active,error"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

// GitLinkView is a service's repository link.
type GitLinkView struct {
	Linked         bool           `json:"linked"`
	ConnectionID   *uuid.UUID     `json:"connectionId"`
	Provider       Provider       `json:"provider,omitempty" enum:"github,gitlab,gitea,forgejo,bitbucket"`
	ConnectionName string         `json:"connectionName"`
	Repo           string         `json:"repo"`
	RepoURL        string         `json:"repoUrl"`
	Branch         string         `json:"branch"`
	AutoDeploy     bool           `json:"autoDeploy"`
	WatchPaths     []string       `json:"watchPaths"`
	Webhook        HookStatusView `json:"webhook"`
	// QueuedCommit is the newest push waiting for the running deployment to
	// finish (empty when none).
	QueuedCommit string `json:"queuedCommit"`
}

// LinkInput links a git service to a repository of a connection.
type LinkInput struct {
	ConnectionID uuid.UUID
	Repo         string
	// Branch defaults to the service's current branch when it already builds
	// this repository, else to the repository's default branch.
	Branch string
	// AutoDeploy defaults to the current setting (true for a new link).
	AutoDeploy *bool
	// WatchPaths defaults to the current patterns (none for a new link).
	WatchPaths *[]string
}

func hookMode(c db.GitConnection) string {
	switch Provider(c.Provider) {
	case GitHub:
		return "app"
	case Bitbucket:
		return "manual"
	}
	return "registered"
}

// GetLink returns a service's link ({linked:false} when it has none).
func (s *Service) GetLink(ctx context.Context, serviceID uuid.UUID) (GitLinkView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return GitLinkView{}, err
	}
	return s.linkView(ctx, sc)
}

func (s *Service) linkView(ctx context.Context, sc services.Scope) (GitLinkView, error) {
	sg, err := s.q.GetServiceGit(ctx, sc.Service.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitLinkView{WatchPaths: []string{}}, nil
	}
	if err != nil {
		return GitLinkView{}, fmt.Errorf("load git link: %w", err)
	}
	c, err := s.loadConnectionInternal(ctx, sg.ConnectionID)
	if err != nil {
		return GitLinkView{}, err
	}
	drv := driverFor(Provider(c.Provider))
	v := GitLinkView{
		Linked: true, ConnectionID: &c.ID, Provider: Provider(c.Provider), ConnectionName: c.Name, Repo: sg.RepoFullName,
		RepoURL: drv.repoURL(c.BaseUrl, sg.RepoFullName), Branch: sc.Config().GitBranch, AutoDeploy: sg.AutoDeploy,
		WatchPaths: sg.WatchPaths, Webhook: s.hookStatus(ctx, c, sg.RepoFullName),
	}
	if v.WatchPaths == nil {
		v.WatchPaths = []string{}
	}
	if len(sg.PendingCommit) > 0 {
		var pc pendingCommit
		if jsonUnmarshal(sg.PendingCommit, &pc) == nil {
			v.QueuedCommit = pc.SHA
		}
	}
	return v, nil
}

// Link connects a git service to a repository: it points the service's clone
// URL and branch at it (staged like any config change when they differ),
// stores the auto-deploy settings and makes sure the provider delivers pushes.
func (s *Service) Link(ctx context.Context, serviceID uuid.UUID, in LinkInput) (GitLinkView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return GitLinkView{}, err
	}
	if sc.Service.SourceType != services.SourceGit || (sc.Service.Kind != services.KindApp && sc.Service.Kind != services.KindWorker) {
		return GitLinkView{}, apierr.Unprocessable("not_applicable", "only services built from a git repository can be linked")
	}
	c, err := s.loadConnection(ctx, policy.ScopeProjectsWrite, in.ConnectionID)
	if err != nil {
		return GitLinkView{}, err
	}
	if c.Status != StatusActive {
		return GitLinkView{}, apierr.Conflict("connection_not_active", "the git connection is not active ("+c.Status+"): finish or repair it in Team settings → Integrations")
	}
	repo, err := normalizeRepo(in.Repo)
	if err != nil {
		return GitLinkView{}, err
	}
	drv := driverFor(Provider(c.Provider))
	info := Repo{FullName: repo, CloneURL: drv.cloneURL(c.BaseUrl, repo)}
	if drv.hasAPI() && c.AuthType != AuthWebhookOnly {
		_, cl, err := s.api(ctx, c)
		if err != nil {
			return GitLinkView{}, apierr.Unprocessable("credentials_unusable", err.Error())
		}
		r, err := drv.getRepo(ctx, cl, repo)
		switch {
		case isNotFound(err):
			return GitLinkView{}, apierr.Unprocessable("repo_not_found", "The connection cannot access "+repo+": check the name and the connection's access.")
		case err != nil:
			return GitLinkView{}, providerError(err)
		}
		info = r
		if info.CloneURL == "" {
			info.CloneURL = drv.cloneURL(c.BaseUrl, repo)
		}
		repo = info.FullName
	}

	cfg := sc.Config()
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		if strings.EqualFold(RepoFullNameFromURL(cfg.GitURL), repo) && cfg.GitBranch != "" {
			branch = cfg.GitBranch
		} else if branch = info.DefaultBranch; branch == "" {
			branch = "main"
		}
	}
	cur, curErr := s.q.GetServiceGit(ctx, serviceID)
	if curErr != nil && !errors.Is(curErr, pgx.ErrNoRows) {
		return GitLinkView{}, curErr
	}
	autoDeploy, watch := true, []string{}
	if curErr == nil {
		autoDeploy, watch = cur.AutoDeploy, cur.WatchPaths
	}
	if in.AutoDeploy != nil {
		autoDeploy = *in.AutoDeploy
	}
	if in.WatchPaths != nil {
		if watch, err = ValidateWatchPaths(*in.WatchPaths); err != nil {
			return GitLinkView{}, apierr.Unprocessable("validation_failed", err.Error())
		}
	}
	if watch == nil {
		watch = []string{}
	}
	if err := sc.Guard(ctx, guardLinkGit, fmt.Sprintf("Link %s in production to the repository %s (%s / %s)", sc.Service.Name, repo, sc.ProjectName, sc.EnvName), false,
		[]string{
			"The repository, branch and auto-deploy setting apply at once; with auto-deploy a push to the branch deploys the service.",
			"Repository: " + repo + ", branch: " + branch + ", auto-deploy: " + fmt.Sprint(autoDeploy) + ".",
		},
		c.ID, repo, branch, autoDeploy, strings.Join(watch, ",")); err != nil {
		return GitLinkView{}, err
	}

	if cfg.GitURL != info.CloneURL || cfg.GitBranch != branch {
		next := cfg
		next.GitURL, next.GitBranch = info.CloneURL, branch
		if _, err := s.svcs.Update(ctx, serviceID, sc.Service.Generation, services.UpdateInput{Config: &next}); err != nil {
			return GitLinkView{}, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpsertServiceGit(ctx, db.UpsertServiceGitParams{
			ServiceID: serviceID, ConnectionID: c.ID, RepoFullName: repo, AutoDeploy: autoDeploy, WatchPaths: watch,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.git.link", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"connection": c.Name, "provider": c.Provider, "repo": repo, "branch": branch, "autoDeploy": autoDeploy, "watchPaths": len(watch)},
		})
	})
	if err != nil {
		return GitLinkView{}, fmt.Errorf("link service to repository: %w", err)
	}
	if curErr == nil && (cur.ConnectionID != c.ID || !strings.EqualFold(cur.RepoFullName, repo)) {
		s.gcWebhook(ctx, cur.ConnectionID, cur.RepoFullName)
	}
	s.ensureWebhook(ctx, c, repo)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return s.GetLink(ctx, serviceID)
}

// Guard operation names (prodguard approvals are bound to them).
const (
	guardLinkGit   = "linkServiceGit"
	guardUnlinkGit = "unlinkServiceGit"
)

// Unlink removes a service's repository link. The clone URL and branch stay in
// the service's config; auto-deploy stops.
func (s *Service) Unlink(ctx context.Context, serviceID uuid.UUID) error {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return err
	}
	sg, err := s.q.GetServiceGit(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := sc.Guard(ctx, guardUnlinkGit, fmt.Sprintf("Unlink %s in production from the repository %s (%s / %s)", sc.Service.Name, sg.RepoFullName, sc.ProjectName, sc.EnvName), false,
		[]string{"Pushes stop deploying the service."}, sg.ConnectionID, sg.RepoFullName); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).DeleteServiceGit(ctx, serviceID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.git.unlink", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"repo": sg.RepoFullName},
		})
	})
	if err != nil {
		return fmt.Errorf("unlink service: %w", err)
	}
	s.gcWebhook(ctx, sg.ConnectionID, sg.RepoFullName)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return nil
}

// ServicesForRepo returns the live services linked to a repository of the
// connection (repo compared case-insensitively). It performs no authorization.
func (s *Service) ServicesForRepo(ctx context.Context, connectionID uuid.UUID, repo string) ([]uuid.UUID, error) {
	rows, err := s.q.ListServiceGitForRepo(ctx, db.ListServiceGitForRepoParams{ConnectionID: connectionID, Lower: repo})
	if err != nil {
		return nil, fmt.Errorf("list services of repo: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ServiceGit.ServiceID)
	}
	return out, nil
}

// CopyServiceGitTx links a service to the repository another one is linked to,
// with auto-deploy off (preview services). It does nothing when from has no
// link and when to already has one.
func (s *Service) CopyServiceGitTx(ctx context.Context, tx pgx.Tx, fromServiceID, toServiceID uuid.UUID) error {
	return db.New(tx).CopyServiceGit(ctx, db.CopyServiceGitParams{FromServiceID: fromServiceID, ToServiceID: toServiceID})
}

// ---- webhooks registered through provider APIs ----

func (s *Service) hookStatus(ctx context.Context, c db.GitConnection, repo string) HookStatusView {
	v := HookStatusView{Mode: hookMode(c), Status: "active"}
	if s.publicURL() != "" {
		v.URL = s.hookURL(c.Provider, c.ID)
	}
	if v.Mode != "registered" {
		return v
	}
	w, err := s.q.GetGitWebhook(ctx, db.GetGitWebhookParams{ConnectionID: c.ID, Lower: repo})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		v.Status, v.Message = "error", "The webhook has not been registered yet."
	case err != nil:
		v.Status, v.Message = "error", "Could not read the webhook state."
	default:
		v.Status, v.Message = w.Status, w.StatusMessage
	}
	return v
}

// ensureWebhook registers the repository webhook of a GitLab, Gitea or Forgejo
// connection (idempotent). Failures are recorded on the webhook row and shown
// in the UI; they never fail the link.
func (s *Service) ensureWebhook(ctx context.Context, c db.GitConnection, repo string) {
	if hookMode(c) != "registered" {
		return
	}
	record := func(extID, status, msg string) {
		if err := s.q.UpsertGitWebhook(ctx, db.UpsertGitWebhookParams{ConnectionID: c.ID, RepoFullName: repo, ExternalID: extID, Status: status, StatusMessage: msg}); err != nil {
			s.log.Error("store git webhook state", "err", err)
		}
	}
	if err := s.requirePublicURL(); err != nil {
		record("", "error", "NEBULACTRL_PUBLIC_URL is not set, so the provider has no address to deliver pushes to.")
		return
	}
	if w, err := s.q.GetGitWebhook(ctx, db.GetGitWebhookParams{ConnectionID: c.ID, Lower: repo}); err == nil && w.Status == "active" && w.ExternalID != "" {
		return
	}
	drv, cl, err := s.api(ctx, c)
	if err != nil {
		record("", "error", err.Error())
		return
	}
	secret, err := s.webhookSecret(c)
	if err != nil {
		record("", "error", "The webhook secret cannot be decrypted.")
		return
	}
	hookURL := s.hookURL(c.Provider, c.ID)
	id, err := drv.findHook(ctx, cl, repo, hookURL)
	if err == nil && id == "" {
		id, err = drv.createHook(ctx, cl, repo, hookURL, secret)
	}
	if err != nil {
		msg := "Could not register the webhook: " + err.Error() + ". Check the token has permission to manage webhooks (and that the instance allows webhooks to this address)."
		s.log.Warn("register git webhook failed", "connection", c.Name, "repo", repo, "err", err)
		record("", "error", msg)
		return
	}
	record(id, "active", "")
}

// gcWebhook removes the repository webhook when no live service uses the
// repository any more.
func (s *Service) gcWebhook(ctx context.Context, connectionID uuid.UUID, repo string) {
	ids, err := s.ServicesForRepo(ctx, connectionID, repo)
	if err != nil || len(ids) > 0 {
		return
	}
	c, err := s.loadConnectionInternal(ctx, connectionID)
	if err != nil {
		return
	}
	s.removeWebhook(ctx, c, repo)
}

// RepoUnlinked is services.GitLinkHook: a service stopped building from the
// repository (it moved to an image source), so its webhook goes when no other
// service uses it.
func (s *Service) RepoUnlinked(ctx context.Context, connectionID uuid.UUID, repo string) {
	s.gcWebhook(ctx, connectionID, repo)
}

func (s *Service) removeWebhook(ctx context.Context, c db.GitConnection, repo string) {
	w, err := s.q.GetGitWebhook(ctx, db.GetGitWebhookParams{ConnectionID: c.ID, Lower: repo})
	if err != nil {
		return
	}
	if w.ExternalID != "" {
		if drv, cl, err := s.api(ctx, c); err == nil {
			if err := drv.deleteHook(ctx, cl, repo, w.ExternalID); err != nil {
				s.log.Warn("remove git webhook failed", "connection", c.Name, "repo", repo, "err", err)
			}
		}
	}
	_ = s.q.DeleteGitWebhook(ctx, db.DeleteGitWebhookParams{ConnectionID: c.ID, Lower: repo})
}

func (s *Service) removeAllWebhooks(ctx context.Context, c db.GitConnection) {
	hooks, err := s.q.ListGitWebhooks(ctx, c.ID)
	if err != nil {
		return
	}
	for _, h := range hooks {
		s.removeWebhook(ctx, c, h.RepoFullName)
	}
}
