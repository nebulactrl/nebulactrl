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

// githubDriver implements GitHub and GitHub Enterprise Server through a GitHub
// App.
//
// Webhooks: https://docs.github.com/en/webhooks/webhook-events-and-payloads and
// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
// REST: https://docs.github.com/en/rest
type githubDriver struct{}

func (githubDriver) provider() Provider { return GitHub }

func (githubDriver) verify(h http.Header, body []byte, secret string) bool {
	return verifyHMAC(secret, body, h.Get("X-Hub-Signature-256"))
}

func (githubDriver) deliveryID(h http.Header) string { return h.Get("X-GitHub-Delivery") }

func (githubDriver) cloneURL(webBase, repo string) string {
	return trimBase(webBase) + "/" + repo + ".git"
}
func (githubDriver) repoURL(webBase, repo string) string { return trimBase(webBase) + "/" + repo }
func (githubDriver) cloneUsername() string               { return "x-access-token" }
func (githubDriver) hasAPI() bool                        { return true }

func (githubDriver) apiBase(webBase string) string {
	webBase = trimBase(webBase)
	if webBase == "https://github.com" || webBase == "http://github.com" {
		return "https://api.github.com"
	}
	return webBase + "/api/v3"
}

func (githubDriver) authHeader(token, _ string) string { return "Bearer " + token }

func ghClient(c *apiClient) *apiClient {
	if c.extra == nil {
		c.extra = map[string]string{}
	}
	c.extra["Accept"] = "application/vnd.github+json"
	c.extra["X-GitHub-Api-Version"] = "2022-11-28"
	return c
}

type ghRepo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description"`
	Archived      bool   `json:"archived"`
}

func (r ghRepo) repo() Repo {
	return Repo{FullName: r.FullName, CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, Private: r.Private, HTMLURL: r.HTMLURL, Description: r.Description, Archived: r.Archived}
}

// ---- webhooks ----

type ghCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Author  struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"author"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
}

type ghPushPayload struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	HeadCommit *ghCommit  `json:"head_commit"`
	Commits    []ghCommit `json:"commits"`
	Pusher     struct {
		Name string `json:"name"`
	} `json:"pusher"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

type ghRepoRef struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

type ghPRPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref  string     `json:"ref"`
			SHA  string     `json:"sha"`
			Repo *ghRepoRef `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string     `json:"ref"`
			Repo *ghRepoRef `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository ghRepoRef `json:"repository"`
}

func (githubDriver) parse(h http.Header, body []byte) (parsed, error) {
	ev := h.Get("X-GitHub-Event")
	p := parsed{event: ev}
	switch ev {
	case "ping":
		p.kind = kindPing
	case "push":
		var in ghPushPayload
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode push payload: %w", err)
		}
		branch := branchFromRef(in.Ref)
		switch {
		case branch == "":
			p.reason = "not a branch push (" + in.Ref + ")"
		case in.Deleted || isZeroSHA(in.After):
			p.reason = "branch deleted"
		default:
			push := PushEvent{
				Provider: GitHub, RepoFullName: in.Repository.FullName, Branch: branch, CommitSHA: in.After,
				CloneURL: in.Repository.CloneURL, Before: in.Before,
			}
			if in.HeadCommit != nil {
				push.CommitMessage = firstLine(in.HeadCommit.Message)
				push.Author = in.HeadCommit.Author.Name
				if push.Author == "" {
					push.Author = in.HeadCommit.Author.Username
				}
			}
			if push.Author == "" {
				push.Author = in.Pusher.Name
			}
			if push.Author == "" {
				push.Author = in.Sender.Login
			}
			set := map[string]struct{}{}
			for _, c := range in.Commits {
				addPaths(set, &push.ChangedPaths, c.Added, c.Modified, c.Removed)
			}
			// GitHub caps the commits array; an empty array (a new branch
			// pointing at an existing commit) carries no file list either.
			push.PathsComplete = len(in.Commits) > 0 && len(in.Commits) < 2048
			p.kind, p.pushes = kindPush, []PushEvent{push}
		}
	case "pull_request":
		var in ghPRPayload
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode pull_request payload: %w", err)
		}
		action := ""
		switch in.Action {
		case "opened":
			action = PRopened
		case "synchronize":
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
			Provider: GitHub, RepoFullName: in.Repository.FullName, Number: in.Number, Action: action,
			HeadBranch: in.PullRequest.Head.Ref, HeadSHA: in.PullRequest.Head.SHA, BaseBranch: in.PullRequest.Base.Ref,
			Title: in.PullRequest.Title, Author: in.PullRequest.User.Login, HTMLURL: in.PullRequest.HTMLURL,
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
			pr.FromFork = true // the fork was deleted
		}
		p.kind, p.pr = kindPullRequest, pr
	case "installation":
		var in struct {
			Action       string `json:"action"`
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return p, fmt.Errorf("decode installation payload: %w", err)
		}
		if in.Action == "created" && in.Installation.ID > 0 {
			p.kind, p.installationID = kindInstallation, in.Installation.ID
		} else {
			p.reason = "installation " + in.Action
		}
	default:
		p.reason = "event " + ev + " is not handled"
	}
	return p, nil
}

// ---- REST ----

func (githubDriver) currentUser(ctx context.Context, c *apiClient) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := ghClient(c).do(ctx, http.MethodGet, "/user", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

func (githubDriver) listRepos(ctx context.Context, c *apiClient, _ string, page int) ([]Repo, bool, error) {
	var out struct {
		Repositories []ghRepo `json:"repositories"`
	}
	q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
	if err := ghClient(c).do(ctx, http.MethodGet, "/installation/repositories", q, nil, &out); err != nil {
		return nil, false, err
	}
	repos := make([]Repo, 0, len(out.Repositories))
	for _, r := range out.Repositories {
		repos = append(repos, r.repo())
	}
	return repos, len(out.Repositories) == 100, nil
}

func (githubDriver) getRepo(ctx context.Context, c *apiClient, repo string) (Repo, error) {
	var out ghRepo
	if err := ghClient(c).do(ctx, http.MethodGet, "/repos/"+repo, nil, nil, &out); err != nil {
		return Repo{}, err
	}
	return out.repo(), nil
}

func (githubDriver) listBranches(ctx context.Context, c *apiClient, repo string) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
	}
	var names []string
	for page := 1; page <= 5; page++ {
		out = nil
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := ghClient(c).do(ctx, http.MethodGet, "/repos/"+repo+"/branches", q, nil, &out); err != nil {
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

// setStatus: https://docs.github.com/en/rest/commits/statuses#create-a-commit-status
func (githubDriver) setStatus(ctx context.Context, c *apiClient, repo, sha string, st CommitStatus) error {
	body := map[string]string{"state": st.State, "context": st.Context, "description": truncate(st.Description, 140)}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	err := ghClient(c).do(ctx, http.MethodPost, "/repos/"+repo+"/statuses/"+sha, nil, body, nil)
	return err
}

func (githubDriver) upsertComment(ctx context.Context, c *apiClient, repo string, number int, marker, body string) error {
	ghClient(c)
	base := "/repos/" + repo + "/issues"
	for page := 1; page <= 10; page++ {
		var out []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, http.MethodGet, base+"/"+strconv.Itoa(number)+"/comments", q, nil, &out); err != nil {
			return err
		}
		for _, cm := range out {
			if strings.Contains(cm.Body, marker) {
				err := c.do(ctx, http.MethodPatch, base+"/comments/"+strconv.FormatInt(cm.ID, 10), nil, map[string]string{"body": body}, nil)
				return err
			}
		}
		if len(out) < 100 {
			break
		}
	}
	err := c.do(ctx, http.MethodPost, base+"/"+strconv.Itoa(number)+"/comments", nil, map[string]string{"body": body}, nil)
	return err
}

// GitHub Apps receive their events through the App-level webhook, never a
// per-repository one.
func (githubDriver) createHook(context.Context, *apiClient, string, string, string) (string, error) {
	return "", nil
}
func (githubDriver) deleteHook(context.Context, *apiClient, string, string) error { return nil }
func (githubDriver) findHook(context.Context, *apiClient, string, string) (string, error) {
	return "", nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
