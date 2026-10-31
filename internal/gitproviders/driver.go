package gitproviders

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type parsedKind int

const (
	kindIgnored parsedKind = iota
	kindPing
	kindPush
	kindPullRequest
	kindInstallation
)

// parsed is a webhook payload reduced to what NebulaCtrl acts on.
type parsed struct {
	kind   parsedKind
	event  string // the provider's event name, for the delivery log
	reason string // why an event was ignored
	pushes []PushEvent
	pr     *PullRequestEvent
	// installationID is set for GitHub App installation events.
	installationID int64
}

// driver is everything provider specific. Webhook methods are pure; API
// methods talk to the provider through an authenticated apiClient.
type driver interface {
	provider() Provider

	// verify authenticates a webhook delivery with the connection's secret.
	verify(h http.Header, body []byte, secret string) bool
	// deliveryID is the provider's unique id of a delivery ("" if none).
	deliveryID(h http.Header) string
	// parse reduces a verified payload.
	parse(h http.Header, body []byte) (parsed, error)

	// cloneURL is the HTTPS clone URL of repo on a host with the web base webBase.
	cloneURL(webBase, repo string) string
	// repoURL is the repository's web page.
	repoURL(webBase, repo string) string
	// cloneUsername is the basic-auth user for a clone with an access token.
	cloneUsername() string
	// apiBase is the REST API base URL for a web base URL.
	apiBase(webBase string) string
	// authHeader builds the Authorization header for an access token of authType.
	authHeader(token, authType string) string
	// hasAPI says whether the REST methods below are implemented.
	hasAPI() bool

	currentUser(ctx context.Context, c *apiClient) (string, error)
	listRepos(ctx context.Context, c *apiClient, query string, page int) ([]Repo, bool, error)
	getRepo(ctx context.Context, c *apiClient, repo string) (Repo, error)
	listBranches(ctx context.Context, c *apiClient, repo string) ([]string, error)
	setStatus(ctx context.Context, c *apiClient, repo, sha string, st CommitStatus) error
	upsertComment(ctx context.Context, c *apiClient, repo string, number int, marker, body string) error
	// createHook registers a push + pull request webhook and returns its id.
	createHook(ctx context.Context, c *apiClient, repo, hookURL, secret string) (string, error)
	deleteHook(ctx context.Context, c *apiClient, repo, id string) error
	// findHook returns the id of a hook already delivering to hookURL ("" if none).
	findHook(ctx context.Context, c *apiClient, repo, hookURL string) (string, error)
}

// driverFor returns the driver of p.
func driverFor(p Provider) driver {
	switch p {
	case GitHub:
		return githubDriver{}
	case GitLab:
		return gitlabDriver{}
	case Gitea:
		return giteaDriver{name: Gitea}
	case Forgejo:
		return giteaDriver{name: Forgejo}
	case Bitbucket:
		return bitbucketDriver{}
	}
	return nil
}

// noAPI implements the REST half of a driver that has none.
type noAPI struct{}

func (noAPI) hasAPI() bool { return false }
func (noAPI) currentUser(context.Context, *apiClient) (string, error) {
	return "", errNoAPI
}
func (noAPI) listRepos(context.Context, *apiClient, string, int) ([]Repo, bool, error) {
	return nil, false, errNoAPI
}
func (noAPI) getRepo(context.Context, *apiClient, string) (Repo, error) { return Repo{}, errNoAPI }
func (noAPI) listBranches(context.Context, *apiClient, string) ([]string, error) {
	return nil, errNoAPI
}
func (noAPI) setStatus(context.Context, *apiClient, string, string, CommitStatus) error {
	return errNoAPI
}
func (noAPI) upsertComment(context.Context, *apiClient, string, int, string, string) error {
	return errNoAPI
}
func (noAPI) createHook(context.Context, *apiClient, string, string, string) (string, error) {
	return "", errNoAPI
}
func (noAPI) deleteHook(context.Context, *apiClient, string, string) error { return errNoAPI }
func (noAPI) findHook(context.Context, *apiClient, string, string) (string, error) {
	return "", errNoAPI
}

// RepoFullNameFromURL returns "owner/repo" (GitLab: "group/subgroup/repo") from
// an https://, http://, git:// or ssh (git@host:owner/repo.git, ssh://) clone
// URL, without a trailing ".git". It returns "" when the URL has no path.
func RepoFullNameFromURL(gitURL string) string {
	s := strings.TrimSpace(gitURL)
	if s == "" {
		return ""
	}
	var path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		path = u.Path
	case strings.Contains(s, ":"): // scp-like: git@host:owner/repo.git
		path = s[strings.Index(s, ":")+1:]
	default:
		path = s
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if !strings.Contains(path, "/") {
		return ""
	}
	return path
}
