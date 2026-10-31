package gitproviders

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type ConnectionPath struct {
	ID uuid.UUID `path:"connectionId"`
}

type connectionOutput struct{ Body ConnectionView }

type listConnectionsOutput struct {
	Body struct {
		Items []ConnectionView `json:"items"`
	}
}

type createConnectionInput struct {
	Body struct {
		Provider     Provider `json:"provider" enum:"gitlab,gitea,forgejo,bitbucket" doc:"GitHub connects through a GitHub App: use POST /git/github/manifest"`
		Name         string   `json:"name,omitempty" maxLength:"80" doc:"Defaults to the provider and host"`
		BaseURL      string   `json:"baseUrl,omitempty" maxLength:"300" doc:"The instance's web URL. Required for Gitea and Forgejo; defaults to https://gitlab.com and https://bitbucket.org"`
		AuthType     string   `json:"authType,omitempty" enum:"token,oauth" doc:"token: a personal/group access token (needs api scope on GitLab; repository, webhook and status access on Gitea and Forgejo). oauth: an OAuth application. Ignored for Bitbucket."`
		Token        string   `json:"token,omitempty" maxLength:"512" doc:"Write-only access token (authType token; for Bitbucket an optional repository access token to clone private repositories)"`
		ClientID     string   `json:"clientId,omitempty" maxLength:"200" doc:"OAuth application client id"`
		ClientSecret string   `json:"clientSecret,omitempty" maxLength:"512" doc:"Write-only OAuth application client secret"`
	}
}

type createConnectionOutput struct{ Body CreateConnectionResult }

type updateConnectionInput struct {
	ConnectionPath
	Body struct {
		Name         *string `json:"name,omitempty" maxLength:"80"`
		Token        *string `json:"token,omitempty" maxLength:"512" doc:"Write-only; replaces the access token"`
		ClientSecret *string `json:"clientSecret,omitempty" maxLength:"512" doc:"Write-only; replaces the OAuth client secret"`
	}
}

type deleteConnectionInput struct {
	ConnectionPath
	Force bool `query:"force" doc:"Also unlink the services that use the connection"`
}

type authorizeOutput struct {
	Body struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
}

type secretOutput struct {
	Body struct {
		WebhookSecret string `json:"webhookSecret" doc:"Shown once"`
	}
}

type reposInput struct {
	ConnectionPath
	Query string `query:"q" maxLength:"100" doc:"Filter by name"`
	Limit int    `query:"limit" default:"100" minimum:"1" maximum:"200"`
}

type reposOutput struct {
	Body struct {
		Items []Repo `json:"items"`
	}
}

type branchesInput struct {
	ConnectionPath
	Repo string `query:"repo" required:"true" doc:"owner/name"`
}

type branchesOutput struct {
	Body struct {
		Items []string `json:"items"`
	}
}

type deliveriesInput struct {
	ConnectionPath
	api.PageQuery
}

type deliveriesOutput struct{ Body api.Page[WebhookDeliveryView] }

type manifestInput struct {
	Body struct {
		Owner   string `json:"owner,omitempty" maxLength:"64" doc:"GitHub organization that should own the App (empty: your own account)"`
		BaseURL string `json:"baseUrl,omitempty" maxLength:"300" doc:"Web URL of a GitHub Enterprise Server (empty: github.com)"`
		Name    string `json:"name,omitempty" maxLength:"80" doc:"Connection name (default GitHub)"`
	}
}

type manifestOutput struct{ Body ManifestStart }

type ServiceGitPath struct {
	ID uuid.UUID `path:"serviceId"`
}

type linkOutput struct{ Body GitLinkView }

type linkInput struct {
	ServiceGitPath
	Body struct {
		ConnectionID uuid.UUID `json:"connectionId"`
		Repo         string    `json:"repo" minLength:"3" maxLength:"300" doc:"owner/name (or a clone URL)"`
		Branch       string    `json:"branch,omitempty" maxLength:"250" doc:"Defaults to the service's branch for the same repository, else the repository's default branch"`
		AutoDeploy   *bool     `json:"autoDeploy,omitempty" doc:"Deploy on every push to the branch (default: keep the current setting, true for a new link)"`
		WatchPaths   *[]string `json:"watchPaths,omitempty" maxItems:"50" doc:"Monorepo watch paths: globs relative to the repository root ('apps/api/**', 'packages/*/src', '!**/*.md'); a push only deploys when a changed file matches. Empty watches everything."`
	}
}

// Register attaches the git provider endpoints under /api/v1.
func Register(h huma.API, s *Service) {
	tags := []string{"git"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listGitConnections", Method: http.MethodGet, Path: "/git/connections", Tags: tags,
		Summary: "List git connections", Description: "Connections to GitHub, GitLab, Gitea, Forgejo and Bitbucket. Credentials and secrets are never returned.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*listConnectionsOutput, error) {
		items, err := s.ListConnections(ctx)
		if err != nil {
			return nil, err
		}
		out := &listConnectionsOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createGitConnection", Method: http.MethodPost, Path: "/git/connections", Tags: tags, DefaultStatus: http.StatusCreated,
		Summary: "Connect GitLab, Gitea, Forgejo or Bitbucket",
		Description: "Token connections are verified against the provider first (422 `credentials_invalid`). OAuth connections start `pending` and return an `authorizeUrl` to open in the browser; register " +
			"`oauth.redirectUri` as the OAuth application's redirect URI. The response carries the webhook secret once; GitLab, Gitea and Forgejo webhooks are registered automatically " +
			"when a service is linked, a Bitbucket webhook is created by hand (URL `webhookUrl`, this secret). Requires the `admin` scope.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *createConnectionInput) (*createConnectionOutput, error) {
		res, err := s.CreateConnection(ctx, CreateConnectionInput{
			Provider: in.Body.Provider, Name: in.Body.Name, BaseURL: in.Body.BaseURL, AuthType: in.Body.AuthType, Token: in.Body.Token,
			ClientID: in.Body.ClientID, ClientSecret: in.Body.ClientSecret,
		})
		if err != nil {
			return nil, err
		}
		return &createConnectionOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getGitConnection", Method: http.MethodGet, Path: "/git/connections/{connectionId}", Tags: tags,
		Summary: "Get a git connection", Errors: append(errs, http.StatusNotFound),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *ConnectionPath) (*connectionOutput, error) {
		v, err := s.GetConnection(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateGitConnection", Method: http.MethodPatch, Path: "/git/connections/{connectionId}", Tags: tags,
		Summary: "Rename a git connection or replace its credentials", Description: "Requires the `admin` scope. Secrets are write-only.",
		Errors: append(errs, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateConnectionInput) (*connectionOutput, error) {
		v, err := s.UpdateConnection(ctx, in.ID, UpdateConnectionInput{Name: in.Body.Name, Token: in.Body.Token, ClientSecret: in.Body.ClientSecret})
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteGitConnection", Method: http.MethodDelete, Path: "/git/connections/{connectionId}", Tags: tags,
		Summary: "Remove a git connection", DefaultStatus: http.StatusNoContent,
		Description: "Refused with 409 `connection_in_use` while services use it, unless `force` is set (it unlinks them). Webhooks NebulaCtrl registered are removed; a GitHub App stays installed at GitHub. Requires the `admin` scope.",
		Errors:      append(errs, http.StatusNotFound, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *deleteConnectionInput) (*struct{}, error) {
		return nil, s.DeleteConnection(ctx, in.ID, in.Force)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testGitConnection", Method: http.MethodPost, Path: "/git/connections/{connectionId}/test", Tags: tags,
		Summary: "Test a git connection", Description: "Calls the provider with the stored credentials and records the outcome in `status`.",
		Errors: append(errs, http.StatusNotFound),
	}, policy.ScopeAdmin), func(ctx context.Context, in *ConnectionPath) (*connectionOutput, error) {
		v, err := s.TestConnection(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "startGitConnectionOAuth", Method: http.MethodPost, Path: "/git/connections/{connectionId}/oauth/start", Tags: tags,
		Summary: "Start (or restart) the OAuth authorization of a connection", Description: "Returns the provider URL to open in the browser.",
		Errors: append(errs, http.StatusNotFound, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *ConnectionPath) (*authorizeOutput, error) {
		u, err := s.StartOAuth(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &authorizeOutput{}
		out.Body.AuthorizeURL = u
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "refreshGitHubInstallation", Method: http.MethodPost, Path: "/git/connections/{connectionId}/refresh", Tags: tags,
		Summary:     "Look up a GitHub App's installation again",
		Description: "For an App installed without returning to NebulaCtrl: finds the installation through the GitHub API and verifies the connection.",
		Errors:      append(errs, http.StatusNotFound, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *ConnectionPath) (*connectionOutput, error) {
		if err := s.RefreshInstallation(ctx, in.ID); err != nil {
			return nil, err
		}
		v, err := s.GetConnection(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "rotateGitWebhookSecret", Method: http.MethodPost, Path: "/git/connections/{connectionId}/webhook-secret/rotate", Tags: tags,
		Summary:     "Issue a new webhook secret (Bitbucket)",
		Description: "Only for providers whose webhook is created by hand (Bitbucket). The new secret is returned once; update the webhook at the provider.",
		Errors:      append(errs, http.StatusNotFound, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *ConnectionPath) (*secretOutput, error) {
		secret, err := s.RotateWebhookSecret(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &secretOutput{}
		out.Body.WebhookSecret = secret
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listGitRepos", Method: http.MethodGet, Path: "/git/connections/{connectionId}/repos", Tags: tags,
		Summary:     "List the repositories a connection can access",
		Description: "The repository picker. 422 `no_repo_api` for Bitbucket (enter owner/name instead). Requires `projects:write`.",
		Errors:      append(errs, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *reposInput) (*reposOutput, error) {
		repos, err := s.ListRepos(ctx, in.ID, in.Query, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &reposOutput{}
		out.Body.Items = repos
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listGitBranches", Method: http.MethodGet, Path: "/git/connections/{connectionId}/branches", Tags: tags,
		Summary: "List a repository's branches", Errors: append(errs, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *branchesInput) (*branchesOutput, error) {
		names, err := s.ListBranches(ctx, in.ID, in.Repo)
		if err != nil {
			return nil, err
		}
		out := &branchesOutput{}
		out.Body.Items = names
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listGitDeliveries", Method: http.MethodGet, Path: "/git/connections/{connectionId}/deliveries", Tags: tags,
		Summary:     "Webhook delivery log of a connection",
		Description: "The last 200 deliveries (14 days), newest first: event, outcome (processed, ignored, rejected, replayed, error), what they started. Headers are redacted. Requires the `admin` scope.",
		Errors:      append(errs, http.StatusNotFound),
	}, policy.ScopeAdmin), func(ctx context.Context, in *deliveriesInput) (*deliveriesOutput, error) {
		items, next, err := s.ListDeliveries(ctx, in.ID, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &deliveriesOutput{Body: api.Page[WebhookDeliveryView]{Items: items, NextCursor: next}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "startGitHubAppManifest", Method: http.MethodPost, Path: "/git/github/manifest", Tags: tags,
		Summary: "Start creating a GitHub App",
		Description: "Creates a pending GitHub connection and returns a manifest: submit it as the form field `manifest` with a POST to `postUrl` (the browser does this; GitHub asks the user to confirm). " +
			"GitHub then redirects back to NebulaCtrl, which stores the App and sends the user to install it. The App gets read access to code and metadata and write access to " +
			"pull requests and commit statuses, and subscribes to push and pull_request events. Needs NEBULACTRL_PUBLIC_URL (422 `public_url_required`). Requires the `admin` scope.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *manifestInput) (*manifestOutput, error) {
		m, err := s.StartGitHubManifest(ctx, ManifestInput{Owner: in.Body.Owner, BaseURL: in.Body.BaseURL, Name: in.Body.Name})
		if err != nil {
			return nil, err
		}
		return &manifestOutput{Body: m}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceGit", Method: http.MethodGet, Path: "/services/{serviceId}/git", Tags: []string{"services", "git"},
		Summary: "Get a service's repository link", Description: "`linked: false` when the service has no link.", Errors: append(errs, http.StatusNotFound),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *ServiceGitPath) (*linkOutput, error) {
		v, err := s.GetLink(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &linkOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "linkServiceGit", Method: http.MethodPut, Path: "/services/{serviceId}/git", Tags: []string{"services", "git"},
		Summary: "Link a service to a repository and set auto-deploy",
		Description: "Points the service's clone URL and branch at the repository (a staged change when they differ) and stores the auto-deploy settings, which are operational: they are applied immediately, never staged and never rolled back. " +
			"A push to the branch deploys the service (trigger `git_push`) when a changed file matches the watch paths; a push during a running deployment is queued and the newest one deploys next. " +
			"Webhooks are registered for GitLab, Gitea and Forgejo; GitHub uses the App's webhook and Bitbucket a webhook you create (see the connection). The `webhook` field reports the state.",
		Errors: append(errs, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *linkInput) (*linkOutput, error) {
		v, err := s.Link(ctx, in.ID, LinkInput{
			ConnectionID: in.Body.ConnectionID, Repo: in.Body.Repo, Branch: in.Body.Branch, AutoDeploy: in.Body.AutoDeploy, WatchPaths: in.Body.WatchPaths,
		})
		if err != nil {
			return nil, err
		}
		return &linkOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "unlinkServiceGit", Method: http.MethodDelete, Path: "/services/{serviceId}/git", Tags: []string{"services", "git"},
		Summary: "Unlink a service from its repository", DefaultStatus: http.StatusNoContent,
		Description: "Stops auto-deploy. The clone URL and branch stay in the service's config.", Errors: append(errs, http.StatusNotFound),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *ServiceGitPath) (*struct{}, error) {
		return nil, s.Unlink(ctx, in.ID)
	})
}

// MountAPI attaches the browser redirects of the connection flows under
// /api/v1 (session authenticated): the GitHub manifest and installation
// callbacks and the OAuth callback. They answer with a redirect to
// Team settings → Integrations.
func (s *Service) MountAPI(r chi.Router) {
	r.Get("/git/github/manifest/callback", s.handleManifestCallback)
	r.Get("/git/github/installed", s.handleInstalledCallback)
	r.Get("/git/oauth/callback", s.handleOAuthCallback)
}

func (s *Service) redirectSettings(w http.ResponseWriter, r *http.Request, q url.Values) {
	http.Redirect(w, r, s.settingsURL(q), http.StatusSeeOther)
}

func (s *Service) flowError(w http.ResponseWriter, r *http.Request, err error) {
	msg := "The connection could not be completed."
	if ae, ok := apierr.As(err); ok {
		msg = ae.Message
	} else {
		s.log.Error("git connection flow", "err", err)
	}
	s.redirectSettings(w, r, url.Values{"git_error": {truncate(msg, 300)}})
}

func (s *Service) handleManifestCallback(w http.ResponseWriter, r *http.Request) {
	next, err := s.CompleteManifest(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"))
	if err != nil {
		s.flowError(w, r, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Service) handleInstalledCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	iid, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	id, note, err := s.CompleteInstallation(r.Context(), q.Get("state"), iid, q.Get("setup_action"))
	if err != nil {
		s.flowError(w, r, err)
		return
	}
	out := url.Values{"git_connected": {id.String()}}
	if note != "" {
		out = url.Values{"git_notice": {note}}
	}
	s.redirectSettings(w, r, out)
}

func (s *Service) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.redirectSettings(w, r, url.Values{"git_error": {truncate("The provider reported: "+e+" "+q.Get("error_description"), 300)}})
		return
	}
	id, err := s.CompleteOAuth(r.Context(), q.Get("code"), q.Get("state"))
	if err != nil {
		s.flowError(w, r, err)
		return
	}
	s.redirectSettings(w, r, url.Values{"git_connected": {id.String()}})
}
