package gitproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// gitlabDriver implements gitlab.com and self-managed GitLab.
//
// Webhooks: https://docs.gitlab.com/user/project/integrations/webhooks/ and
// https://docs.gitlab.com/user/project/integrations/webhook_events/
// REST: https://docs.gitlab.com/api/
type gitlabDriver struct{}

func (gitlabDriver) provider() Provider { return GitLab }

// GitLab authenticates a webhook with the secret token it echoes verbatim in
// X-Gitlab-Token (there is no payload signature).
func (gitlabDriver) verify(h http.Header, _ []byte, secret string) bool {
	return secureEqual(h.Get("X-Gitlab-Token"), secret)
}

// deliveryID prefers Idempotency-Key, which GitLab keeps stable across retries
// of one delivery, over X-Gitlab-Event-UUID.
func (gitlabDriver) deliveryID(h http.Header) string {
	if v := h.Get("Idempotency-Key"); v != "" {
		return v
	}
	return h.Get("X-Gitlab-Event-UUID")
}

func (gitlabDriver) cloneURL(webBase, repo string) string {
	return trimBase(webBase) + "/" + repo + ".git"
}
func (gitlabDriver) repoURL(webBase, repo string) string { return trimBase(webBase) + "/" + repo }
func (gitlabDriver) cloneUsername() string               { return "oauth2" }
func (gitlabDriver) hasAPI() bool                        { return true }
func (gitlabDriver) apiBase(webBase string) string       { return trimBase(webBase) + "/api/v4" }
func (gitlabDriver) authHeader(token, _ string) string   { return "Bearer " + token }

func glProject(repo string) string { return url.PathEscape(repo) }

// ---- webhooks ----

type glProjectRef struct {
	PathWithNamespace string `json:"path_with_namespace"`
	GitHTTPURL        string `json:"git_http_url"`
}

type glPushPayload struct {
	ObjectKind        string       `json:"object_kind"`
	Before            string       `json:"before"`
	After             string       `json:"after"`
	Ref               string       `json:"ref"`
	CheckoutSHA       string       `json:"checkout_sha"`
	UserName          string       `json:"user_name"`
	TotalCommitsCount int          `json:"total_commits_count"`
	Project           glProjectRef `json:"project"`
	Commits           []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Title   string `json:"title"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

type glMRPayload struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Project          glProjectRef `json:"project"`
	ObjectAttributes struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		URL          string `json:"url"`
		State        string `json:"state"`
		Action       string `json:"action"`
		OldRev       string `json:"oldrev"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		Source *glProjectRef `json:"source"`
		Target *glProjectRef `json:"target"`
	} `json:"object_attributes"`
}

func (gitlabDriver) parse(h http.Header, body []byte) (parsed, error) {
	ev := h.Get("X-Gitlab-Event")
	p := parsed{event: ev}
	var kind struct {
		ObjectKind string `json:"object_kind"`
	}
	if err := json.Unmarshal(body, &kind); err != nil {
		return p, fmt.Errorf("decode payload: %w", err)
	}
	switch kind.ObjectKind {
	case "push":
		var in glPushPayload
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode push payload: %w", err)
		}
		branch := branchFromRef(in.Ref)
		switch {
		case branch == "":
			p.reason = "not a branch push (" + in.Ref + ")"
		case isZeroSHA(in.After):
			p.reason = "branch deleted"
		default:
			sha := in.After
			if sha == "" {
				sha = in.CheckoutSHA
			}
			push := PushEvent{
				Provider: GitLab, RepoFullName: in.Project.PathWithNamespace, Branch: branch, CommitSHA: sha,
				CloneURL: in.Project.GitHTTPURL, Before: in.Before, Author: in.UserName,
			}
			set := map[string]struct{}{}
			for _, c := range in.Commits {
				addPaths(set, &push.ChangedPaths, c.Added, c.Modified, c.Removed)
				if c.ID == sha {
					push.CommitMessage = firstLine(c.Message)
					if c.Author.Name != "" {
						push.Author = c.Author.Name
					}
				}
			}
			if push.CommitMessage == "" && len(in.Commits) > 0 {
				last := in.Commits[len(in.Commits)-1]
				push.CommitMessage = firstLine(last.Message)
			}
			// GitLab lists at most 20 commits and reports the real count.
			push.PathsComplete = len(in.Commits) > 0 && in.TotalCommitsCount <= len(in.Commits)
			p.kind, p.pushes = kindPush, []PushEvent{push}
		}
	case "merge_request":
		var in glMRPayload
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode merge_request payload: %w", err)
		}
		oa := in.ObjectAttributes
		action := ""
		switch oa.Action {
		case "open":
			action = PRopened
		case "reopen":
			action = PRreopened
		case "update":
			if oa.OldRev != "" { // new commits were pushed to the source branch
				action = PRsynchronize
			}
		case "close":
			action = PRclosed
		case "merge":
			action = PRmerged
		}
		if action == "" {
			p.reason = "merge request action " + oa.Action + " is not handled"
			break
		}
		repo := in.Project.PathWithNamespace
		pr := &PullRequestEvent{
			Provider: GitLab, RepoFullName: repo, Number: oa.IID, Action: action,
			HeadBranch: oa.SourceBranch, HeadSHA: oa.LastCommit.ID, BaseBranch: oa.TargetBranch,
			Title: oa.Title, Author: in.User.Username, HTMLURL: oa.URL,
		}
		if oa.Target != nil {
			pr.BaseCloneURL = oa.Target.GitHTTPURL
		}
		if oa.Source != nil {
			pr.HeadCloneURL = oa.Source.GitHTTPURL
			pr.FromFork = !strings.EqualFold(oa.Source.PathWithNamespace, repo)
		} else {
			pr.FromFork = true
		}
		p.kind, p.pr = kindPullRequest, pr
	default:
		p.reason = "event " + ev + " is not handled"
	}
	return p, nil
}

// ---- REST ----

type glRepo struct {
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	WebURL            string `json:"web_url"`
	Description       string `json:"description"`
	Archived          bool   `json:"archived"`
}

func (r glRepo) repo() Repo {
	return Repo{
		FullName: r.PathWithNamespace, CloneURL: r.HTTPURLToRepo, DefaultBranch: r.DefaultBranch,
		Private: r.Visibility != "public", HTMLURL: r.WebURL, Description: r.Description, Archived: r.Archived,
	}
}

func (gitlabDriver) currentUser(ctx context.Context, c *apiClient) (string, error) {
	var out struct {
		Username string `json:"username"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Username, nil
}

func (gitlabDriver) listRepos(ctx context.Context, c *apiClient, query string, page int) ([]Repo, bool, error) {
	q := url.Values{
		"membership": {"true"}, "simple": {"true"}, "archived": {"false"}, "order_by": {"last_activity_at"},
		"per_page": {"100"}, "page": {strconv.Itoa(page)},
	}
	if query != "" {
		q.Set("search", query)
		q.Set("search_namespaces", "true")
	}
	var out []glRepo
	if err := c.do(ctx, http.MethodGet, "/projects", q, nil, &out); err != nil {
		return nil, false, err
	}
	repos := make([]Repo, 0, len(out))
	for _, r := range out {
		repos = append(repos, r.repo())
	}
	return repos, len(out) == 100, nil
}

func (gitlabDriver) getRepo(ctx context.Context, c *apiClient, repo string) (Repo, error) {
	var out glRepo
	if err := c.do(ctx, http.MethodGet, "/projects/"+glProject(repo), nil, nil, &out); err != nil {
		return Repo{}, err
	}
	return out.repo(), nil
}

func (gitlabDriver) listBranches(ctx context.Context, c *apiClient, repo string) ([]string, error) {
	var names []string
	for page := 1; page <= 5; page++ {
		var out []struct {
			Name string `json:"name"`
		}
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, http.MethodGet, "/projects/"+glProject(repo)+"/repository/branches", q, nil, &out); err != nil {
			return nil, err
		}
		for _, b := range out {
			names = append(names, b.Name)
		}
		if len(out) < 100 {
			break
		}
	}
	return names, nil
}

// setStatus: https://docs.gitlab.com/api/commits/#set-the-pipeline-status-of-a-commit
func (gitlabDriver) setStatus(ctx context.Context, c *apiClient, repo, sha string, st CommitStatus) error {
	state := map[string]string{StatePending: "running", StateSuccess: "success", StateFailure: "failed", StateError: "canceled"}[st.State]
	if state == "" {
		state = "failed"
	}
	body := map[string]string{"state": state, "name": st.Context, "description": truncate(st.Description, 255)}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	err := c.do(ctx, http.MethodPost, "/projects/"+glProject(repo)+"/statuses/"+sha, nil, body, nil)
	return err
}

func (gitlabDriver) upsertComment(ctx context.Context, c *apiClient, repo string, number int, marker, body string) error {
	base := "/projects/" + glProject(repo) + "/merge_requests/" + strconv.Itoa(number) + "/notes"
	for page := 1; page <= 10; page++ {
		var out []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, http.MethodGet, base, q, nil, &out); err != nil {
			return err
		}
		for _, n := range out {
			if strings.Contains(n.Body, marker) {
				err := c.do(ctx, http.MethodPut, base+"/"+strconv.FormatInt(n.ID, 10), nil, map[string]string{"body": body}, nil)
				return err
			}
		}
		if len(out) < 100 {
			break
		}
	}
	err := c.do(ctx, http.MethodPost, base, nil, map[string]string{"body": body}, nil)
	return err
}

// createHook: https://docs.gitlab.com/api/project_webhooks/#add-a-webhook-to-a-project
func (gitlabDriver) createHook(ctx context.Context, c *apiClient, repo, hookURL, secret string) (string, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{
		"url": hookURL, "token": secret, "push_events": true, "merge_requests_events": true,
		"tag_push_events": false, "enable_ssl_verification": strings.HasPrefix(hookURL, "https://"),
	}
	if err := c.do(ctx, http.MethodPost, "/projects/"+glProject(repo)+"/hooks", nil, body, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (gitlabDriver) deleteHook(ctx context.Context, c *apiClient, repo, id string) error {
	err := c.do(ctx, http.MethodDelete, "/projects/"+glProject(repo)+"/hooks/"+id, nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (gitlabDriver) findHook(ctx context.Context, c *apiClient, repo, hookURL string) (string, error) {
	var out []struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := c.do(ctx, http.MethodGet, "/projects/"+glProject(repo)+"/hooks", url.Values{"per_page": {"100"}}, nil, &out); err != nil {
		return "", err
	}
	for _, h := range out {
		if h.URL == hookURL {
			return strconv.FormatInt(h.ID, 10), nil
		}
	}
	return "", nil
}
