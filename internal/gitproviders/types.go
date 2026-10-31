// Package gitproviders connects NebulaCtrl to git hosting: GitHub (App),
// GitLab, Gitea, Forgejo and Bitbucket. It verifies and logs webhooks, turns
// pushes into deployments (with watch paths for monorepos), reports commit
// statuses, mints short-lived clone credentials and offers a repository picker.
//
// Providers differ in three places only, each behind a driver (driver.go):
// webhook verification and payload parsing, the REST API, and clone
// credentials. Everything else (connections, links, deliveries, auto-deploy)
// is provider independent.
package gitproviders

import (
	"time"

	"github.com/google/uuid"
)

// Provider names a git host family.
type Provider string

// Supported providers.
const (
	GitHub    Provider = "github"
	GitLab    Provider = "gitlab"
	Gitea     Provider = "gitea"
	Forgejo   Provider = "forgejo"
	Bitbucket Provider = "bitbucket"
)

// Providers lists every supported provider.
var Providers = []Provider{GitHub, GitLab, Gitea, Forgejo, Bitbucket}

// Valid reports whether p is a supported provider.
func (p Provider) Valid() bool {
	for _, q := range Providers {
		if p == q {
			return true
		}
	}
	return false
}

// Connection auth types (git_connections.auth_type).
const (
	AuthGitHubApp   = "github_app"
	AuthOAuth       = "oauth"
	AuthToken       = "token"
	AuthWebhookOnly = "webhook_only"
)

// Connection statuses (git_connections.status).
const (
	StatusPending = "pending"
	StatusActive  = "active"
	StatusError   = "error"
)

// PushEvent is a verified push to a branch.
type PushEvent struct {
	ConnectionID  uuid.UUID
	Provider      Provider
	RepoFullName  string
	Branch        string
	CommitSHA     string
	CommitMessage string
	Author        string
	// ChangedPaths are the files added, modified or removed by the push.
	ChangedPaths []string

	// CloneURL is the repository's HTTPS clone URL as the provider reports it.
	CloneURL string
	// Before is the branch head before the push (all zeros for a new branch).
	Before string
	// PathsComplete says ChangedPaths lists every changed file. It is false when
	// the provider does not report files (Bitbucket) or capped the commit list
	// (GitHub lists at most 2048 commits, GitLab 20); watch paths cannot filter such a push.
	PathsComplete bool
}

// PullRequestEvent is a verified pull / merge request event.
type PullRequestEvent struct {
	ConnectionID uuid.UUID
	Provider     Provider
	RepoFullName string
	Number       int
	// Action is opened, synchronize, reopened, closed or merged (a merge is
	// never also reported as closed).
	Action     string
	HeadBranch string
	HeadSHA    string
	BaseBranch string
	Title      string
	Author     string
	// FromFork is true when the head repository is not the base repository.
	FromFork bool

	// HeadCloneURL and BaseCloneURL are the HTTPS clone URLs of the head and
	// base repositories (the head's is the fork for fork PRs).
	HeadCloneURL string
	BaseCloneURL string
	// HTMLURL is the pull request's web page.
	HTMLURL string
}

// Pull request actions.
const (
	PRopened      = "opened"
	PRsynchronize = "synchronize"
	PRreopened    = "reopened"
	PRclosed      = "closed"
	PRmerged      = "merged"
)

// CommitStatus is a status shown next to a commit.
type CommitStatus struct {
	// State is pending, success, failure or error.
	State       string
	TargetURL   string
	Description string
	// Context names the check, "nebulactrl/<service>".
	Context string
}

// Commit status states.
const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
)

// Repo is a repository a connection can reach.
type Repo struct {
	FullName      string `json:"fullName"`
	CloneURL      string `json:"cloneUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"htmlUrl"`
	Description   string `json:"description"`
	Archived      bool   `json:"archived"`
}

// credentials is the sealed JSON of git_connections.credentials_enc.
type credentials struct {
	// Token is the access token of auth type "token".
	Token string `json:"token,omitempty"`
	// ClientID / ClientSecret are the OAuth application (or GitHub App) credentials.
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// AccessToken / RefreshToken / ExpiresAt are the OAuth tokens.
	AccessToken  string     `json:"accessToken,omitempty"`
	RefreshToken string     `json:"refreshToken,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	// AppID and PEM are the GitHub App id and private key.
	AppID int64  `json:"appId,omitempty"`
	PEM   string `json:"pem,omitempty"`
}

// meta is the non-secret JSON of git_connections.meta.
type meta struct {
	AppID          int64  `json:"appId,omitempty"`
	AppSlug        string `json:"appSlug,omitempty"`
	HTMLURL        string `json:"htmlUrl,omitempty"`
	InstallationID int64  `json:"installationId,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	Owner          string `json:"owner,omitempty"`
}
