package gitproviders

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// ErrFileNotFound is returned by RepoReader.Get for a path that does not exist.
var ErrFileNotFound = errors.New("file not found in the repository")

// maxRepoFile bounds one file read through a provider.
const maxRepoFile = 1 << 20

// RepoReader reads single files of one repository at one branch through the
// provider's API (or the public raw host), without cloning.
type RepoReader struct {
	hc       *http.Client
	read     func(ctx context.Context, path string) ([]byte, error)
	FullName string
	Branch   string
	CloneURL string
}

// Get returns the file at path (relative to the repository root).
func (r *RepoReader) Get(ctx context.Context, path string) ([]byte, error) {
	path = strings.TrimPrefix(strings.TrimSpace(path), "/")
	if path == "" || strings.Contains(path, "..") {
		return nil, ErrFileNotFound
	}
	return r.read(ctx, path)
}

// OpenRepo returns a reader for repo of a git connection. An empty branch means
// the repository's default branch.
func (s *Service) OpenRepo(ctx context.Context, connectionID uuid.UUID, repo, branch string) (*RepoReader, error) {
	c, err := s.loadConnection(ctx, policy.ScopeProjectsWrite, connectionID)
	if err != nil {
		return nil, err
	}
	return s.openRepo(ctx, c, repo, branch)
}

// OpenRepoInternal is OpenRepo without authorization, for background jobs and
// push subscribers that already hold a verified connection id.
func (s *Service) OpenRepoInternal(ctx context.Context, connectionID uuid.UUID, repo, branch string) (*RepoReader, error) {
	c, err := s.loadConnectionInternal(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	return s.openRepo(ctx, c, repo, branch)
}

func (s *Service) openRepo(ctx context.Context, c db.GitConnection, repo, branch string) (*RepoReader, error) {
	var err error
	if c.Status != StatusActive {
		return nil, apierr.Conflict("connection_not_active", "the git connection is not active ("+c.Status+"): finish or repair it in Team settings → Integrations")
	}
	repo, err = normalizeRepo(repo)
	if err != nil {
		return nil, err
	}
	drv, cl, err := s.api(ctx, c)
	if errors.Is(err, errNoAPI) {
		return nil, apierr.Unprocessable("no_repo_api", "This connection has no API access, so files cannot be read from it: paste the Compose file instead.")
	}
	if err != nil {
		return nil, apierr.Unprocessable("credentials_unusable", err.Error())
	}
	r := &RepoReader{hc: s.hc, FullName: repo, Branch: strings.TrimSpace(branch), CloneURL: drv.cloneURL(c.BaseUrl, repo)}
	info, err := drv.getRepo(ctx, cl, repo)
	switch {
	case isNotFound(err):
		return nil, apierr.Unprocessable("repo_not_found", "The connection cannot access "+repo)
	case err != nil:
		return nil, providerError(err)
	}
	if info.CloneURL != "" {
		r.CloneURL = info.CloneURL
	}
	if r.Branch == "" {
		if r.Branch = info.DefaultBranch; r.Branch == "" {
			r.Branch = "main"
		}
	}
	r.read = func(ctx context.Context, p string) ([]byte, error) {
		var (
			path string
			q    = url.Values{"ref": {r.Branch}}
			hdr  = map[string]string{"Accept": "*/*"}
		)
		switch drv.provider() {
		case GitHub:
			path = "/repos/" + repo + "/contents/" + escapePath(p)
			hdr["Accept"] = "application/vnd.github.raw+json"
			hdr["X-GitHub-Api-Version"] = "2022-11-28"
		case GitLab:
			path = "/projects/" + glProject(repo) + "/repository/files/" + url.PathEscape(p) + "/raw"
		case Gitea, Forgejo:
			path = "/repos/" + repo + "/raw/" + escapePath(p)
		default:
			return nil, errNoAPI
		}
		return rawGet(ctx, cl, path, q, hdr)
	}
	return r, nil
}

// OpenPublicRepo returns a reader for a public repository on github.com or
// gitlab.com, given its https URL. No credentials are used.
func (s *Service) OpenPublicRepo(ctx context.Context, repoURL, branch string) (*RepoReader, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Scheme != "https" {
		return nil, apierr.Unprocessable("validation_failed", "The repository URL must be an https URL of a public GitHub or GitLab.com repository.")
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	var rawBase string
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "www.github.com":
		rawBase = "https://raw.githubusercontent.com/" + repo + "/%s/%s"
	case "gitlab.com":
		rawBase = "https://gitlab.com/" + repo + "/-/raw/%s/%s"
	default:
		return nil, apierr.Unprocessable("unsupported_host", "Without a git connection only public repositories on github.com and gitlab.com can be read. Connect the host under Team settings → Integrations, or paste the Compose file.")
	}
	if !repoNameRe.MatchString(repo) || strings.Contains(repo, "..") {
		return nil, apierr.Unprocessable("validation_failed", "repo must look like owner/name")
	}
	if branch = strings.TrimSpace(branch); branch == "" {
		branch = "main"
	}
	r := &RepoReader{hc: s.hc, FullName: repo, Branch: branch, CloneURL: "https://" + u.Hostname() + "/" + repo + ".git"}
	r.read = func(ctx context.Context, p string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(rawBase, url.PathEscape(branch), escapePath(p)), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "NebulaCtrl")
		return doRaw(s.hc, req)
	}
	return r, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func rawGet(ctx context.Context, c *apiClient, path string, q url.Values, hdr map[string]string) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.authz != "" {
		req.Header.Set("Authorization", c.authz)
	}
	req.Header.Set("User-Agent", "NebulaCtrl")
	for k, v := range c.extra {
		req.Header.Set(k, v)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	out, err := doRaw(c.hc, req)
	if err != nil && !errors.Is(err, ErrFileNotFound) {
		return nil, c.scrub(err)
	}
	return out, err
}

func doRaw(hc *http.Client, req *http.Request) ([]byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrFileNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, newAPIError(resp.StatusCode, resp.Header)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRepoFile+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRepoFile {
		return nil, fmt.Errorf("the file is larger than %d KiB", maxRepoFile>>10)
	}
	// A directory listing (GitHub returns JSON for directories) is not a file.
	if len(body) > 0 && body[0] == '[' && strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return nil, ErrFileNotFound
	}
	return body, nil
}

// CloneURLFor returns the clone URL of repo on a connection without any API
// call or credential (it joins the connection's base URL and the repository);
// the branch is left empty (the caller chooses). Like GetConnection it needs
// only projects:read, so a read-only config check can resolve it.
func (s *Service) CloneURLFor(ctx context.Context, connectionID uuid.UUID, repo string) (cloneURL, branch string, err error) {
	c, err := s.loadConnection(ctx, policy.ScopeProjectsRead, connectionID)
	if err != nil {
		return "", "", err
	}
	repo, err = normalizeRepo(repo)
	if err != nil {
		return "", "", err
	}
	drv := driverFor(Provider(c.Provider))
	if drv == nil {
		return "", "", apierr.Unprocessable("validation_failed", "unknown provider")
	}
	return drv.cloneURL(c.BaseUrl, repo), "", nil
}
