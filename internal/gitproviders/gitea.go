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

// giteaDriver implements Gitea and Forgejo, whose APIs and webhook payloads are
// compatible. Forgejo sends X-Forgejo-* headers next to Gitea's X-Gitea-* ones.
//
// Webhooks: https://docs.gitea.com/usage/webhooks and
// https://forgejo.org/docs/latest/user/webhooks/
// REST: <base>/api/swagger (both ship the same OpenAPI document).
type giteaDriver struct{ name Provider }

func (d giteaDriver) provider() Provider { return d.name }

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// verify checks the HMAC-SHA256 hex signature of the body. Gitea sends it in
// X-Gitea-Signature, Forgejo in X-Forgejo-Signature, and both mirror it in the
// GitHub-compatible X-Hub-Signature-256 ("sha256=<hex>").
func (giteaDriver) verify(h http.Header, body []byte, secret string) bool {
	sig := firstHeader(h, "X-Gitea-Signature", "X-Forgejo-Signature", "X-Hub-Signature-256")
	return verifyHMAC(secret, body, sig)
}

func (giteaDriver) deliveryID(h http.Header) string {
	return firstHeader(h, "X-Gitea-Delivery", "X-Forgejo-Delivery", "X-GitHub-Delivery")
}

func (giteaDriver) cloneURL(webBase, repo string) string {
	return trimBase(webBase) + "/" + repo + ".git"
}
func (giteaDriver) repoURL(webBase, repo string) string { return trimBase(webBase) + "/" + repo }
func (giteaDriver) cloneUsername() string               { return "oauth2" }
func (giteaDriver) hasAPI() bool                        { return true }
func (giteaDriver) apiBase(webBase string) string       { return trimBase(webBase) + "/api/v1" }

func (giteaDriver) authHeader(token, authType string) string {
	if authType == AuthOAuth {
		return "Bearer " + token
	}
	return "token " + token
}

// ---- webhooks ----

type gtUser struct {
	Login    string `json:"login"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (u gtUser) display() string {
	for _, s := range []string{u.Name, u.Username, u.Login} {
		if s != "" {
			return s
		}
	}
	return ""
}

type gtRepoRef struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	Fork     bool   `json:"fork"`
}

type gtPushPayload struct {
	Ref        string    `json:"ref"`
	Before     string    `json:"before"`
	After      string    `json:"after"`
	Repository gtRepoRef `json:"repository"`
	Pusher     gtUser    `json:"pusher"`
	Sender     gtUser    `json:"sender"`
	Total      int       `json:"total_commits"`
	HeadCommit *struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Author  gtUser `json:"author"`
	} `json:"head_commit"`
	Commits []struct {
		ID       string   `json:"id"`
		Message  string   `json:"message"`
		Author   gtUser   `json:"author"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
}

type gtPRPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
		User    gtUser `json:"user"`
		Head    struct {
			Ref  string     `json:"ref"`
			SHA  string     `json:"sha"`
			Repo *gtRepoRef `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string     `json:"ref"`
			Repo *gtRepoRef `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository gtRepoRef `json:"repository"`
}

func (d giteaDriver) parse(h http.Header, body []byte) (parsed, error) {
	ev := firstHeader(h, "X-Gitea-Event", "X-Forgejo-Event", "X-GitHub-Event")
	p := parsed{event: ev}
	switch ev {
	case "ping":
		p.kind = kindPing
	case "push":
		var in gtPushPayload
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
			push := PushEvent{
				Provider: d.name, RepoFullName: in.Repository.FullName, Branch: branch, CommitSHA: in.After,
				CloneURL: in.Repository.CloneURL, Before: in.Before, Author: in.Pusher.display(),
			}
			if in.HeadCommit != nil {
				push.CommitMessage = firstLine(in.HeadCommit.Message)
				if a := in.HeadCommit.Author.display(); a != "" {
					push.Author = a
				}
			}
			if push.Author == "" {
				push.Author = in.Sender.display()
			}
			set := map[string]struct{}{}
			for _, c := range in.Commits {
				addPaths(set, &push.ChangedPaths, c.Added, c.Modified, c.Removed)
			}
			total := in.Total
			if total == 0 {
				total = len(in.Commits)
			}
			push.PathsComplete = len(in.Commits) > 0 && total <= len(in.Commits)
			p.kind, p.pushes = kindPush, []PushEvent{push}
		}
	case "pull_request", "pull_request_sync", "pull_request_approved", "pull_request_rejected", "pull_request_comment", "pull_request_assign", "pull_request_label", "pull_request_milestone", "pull_request_review_request":
		var in gtPRPayload
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode pull_request payload: %w", err)
		}
		action := ""
		switch in.Action {
		case "opened":
			action = PRopened
		case "synchronized", "synchronize":
			action = PRsynchronize
		case "reopened":
			action = PRreopened
		case "closed":
			action = PRclosed
			if in.PullRequest.Merged {
				action = PRmerged
			}
		}
		if action == "" {
			p.reason = "pull request action " + in.Action + " is not handled"
			break
		}
		pr := &PullRequestEvent{
			Provider: d.name, RepoFullName: in.Repository.FullName, Number: in.Number, Action: action,
			HeadBranch: in.PullRequest.Head.Ref, HeadSHA: in.PullRequest.Head.SHA, BaseBranch: in.PullRequest.Base.Ref,
			Title: in.PullRequest.Title, Author: in.PullRequest.User.display(), HTMLURL: in.PullRequest.HTMLURL,
		}
		if pr.Number == 0 {
			pr.Number = in.PullRequest.Number
		}
		if in.PullRequest.Base.Repo != nil {
			pr.BaseCloneURL = in.PullRequest.Base.Repo.CloneURL
		}
		if hr := in.PullRequest.Head.Repo; hr != nil {
			pr.HeadCloneURL = hr.CloneURL
			pr.FromFork = !strings.EqualFold(hr.FullName, in.Repository.FullName)
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

type gtRepo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description"`
	Archived      bool   `json:"archived"`
}

func (r gtRepo) repo() Repo {
	return Repo{FullName: r.FullName, CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, Private: r.Private, HTMLURL: r.HTMLURL, Description: r.Description, Archived: r.Archived}
}

const giteaPageSize = 50

func (giteaDriver) currentUser(ctx context.Context, c *apiClient) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

func (giteaDriver) listRepos(ctx context.Context, c *apiClient, _ string, page int) ([]Repo, bool, error) {
	var out []gtRepo
	q := url.Values{"limit": {strconv.Itoa(giteaPageSize)}, "page": {strconv.Itoa(page)}}
	if err := c.do(ctx, http.MethodGet, "/user/repos", q, nil, &out); err != nil {
		return nil, false, err
	}
	repos := make([]Repo, 0, len(out))
	for _, r := range out {
		repos = append(repos, r.repo())
	}
	return repos, len(out) >= giteaPageSize, nil
}

func (giteaDriver) getRepo(ctx context.Context, c *apiClient, repo string) (Repo, error) {
	var out gtRepo
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo, nil, nil, &out); err != nil {
		return Repo{}, err
	}
	return out.repo(), nil
}

func (giteaDriver) listBranches(ctx context.Context, c *apiClient, repo string) ([]string, error) {
	var names []string
	for page := 1; page <= 10; page++ {
		var out []struct {
			Name string `json:"name"`
		}
		q := url.Values{"limit": {strconv.Itoa(giteaPageSize)}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/branches", q, nil, &out); err != nil {
			return nil, err
		}
		for _, b := range out {
			names = append(names, b.Name)
		}
		if len(out) < giteaPageSize {
			break
		}
	}
	return names, nil
}

func (giteaDriver) setStatus(ctx context.Context, c *apiClient, repo, sha string, st CommitStatus) error {
	body := map[string]string{"state": st.State, "context": st.Context, "description": truncate(st.Description, 255)}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, nil, body, nil)
	return err
}

func (giteaDriver) upsertComment(ctx context.Context, c *apiClient, repo string, number int, marker, body string) error {
	base := "/repos/" + repo + "/issues"
	for page := 1; page <= 10; page++ {
		var out []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		q := url.Values{"limit": {strconv.Itoa(giteaPageSize)}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, http.MethodGet, base+"/"+strconv.Itoa(number)+"/comments", q, nil, &out); err != nil {
			return err
		}
		for _, cm := range out {
			if strings.Contains(cm.Body, marker) {
				err := c.do(ctx, http.MethodPatch, base+"/comments/"+strconv.FormatInt(cm.ID, 10), nil, map[string]string{"body": body}, nil)
				return err
			}
		}
		if len(out) < giteaPageSize {
			break
		}
	}
	err := c.do(ctx, http.MethodPost, base+"/"+strconv.Itoa(number)+"/comments", nil, map[string]string{"body": body}, nil)
	return err
}

// createHook registers a push + pull request webhook signed with secret
// (HMAC-SHA256 in X-Gitea-Signature / X-Forgejo-Signature).
func (d giteaDriver) createHook(ctx context.Context, c *apiClient, repo, hookURL, secret string) (string, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{
		"type":   string(d.name),
		"active": true,
		"events": []string{"push", "pull_request"},
		"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret},
	}
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/hooks", nil, body, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (giteaDriver) deleteHook(ctx context.Context, c *apiClient, repo, id string) error {
	err := c.do(ctx, http.MethodDelete, "/repos/"+repo+"/hooks/"+id, nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (giteaDriver) findHook(ctx context.Context, c *apiClient, repo, hookURL string) (string, error) {
	var out []struct {
		ID     int64             `json:"id"`
		Config map[string]string `json:"config"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/hooks", url.Values{"limit": {"50"}}, nil, &out); err != nil {
		return "", err
	}
	for _, h := range out {
		if h.Config["url"] == hookURL {
			return strconv.FormatInt(h.ID, 10), nil
		}
	}
	return "", nil
}
