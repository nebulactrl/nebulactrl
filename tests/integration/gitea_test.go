//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const giteaImage = "gitea/gitea:1.22-rootless"

// giteaAPI is a tiny client of the real Gitea started by the test.
type giteaAPI struct {
	t     *testing.T
	base  string
	user  string
	pass  string
	token string
}

func (g *giteaAPI) do(method, path string, body any) (int, []byte) {
	g.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(g.t, err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, g.base+"/api/v1"+path, rd)
	require.NoError(g.t, err)
	req.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "token "+g.token)
	} else {
		req.SetBasicAuth(g.user, g.pass)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(g.t, err)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

// startGitea runs the official Gitea image (SQLite, no install wizard),
// creates an admin and an access token, and removes the container and the
// image when the test ends.
func startGitea(t *testing.T) *giteaAPI {
	t.Helper()
	port, err := freePort()
	require.NoError(t, err)
	name := fmt.Sprintf("nctest-gitea-%d", port)
	ctx := context.Background()
	out, err := sh(ctx, "", "docker", "run", "-d", "--name", name,
		"--add-host", "host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:3000", port),
		"-e", "GITEA__security__INSTALL_LOCK=true",
		"-e", "GITEA__database__DB_TYPE=sqlite3",
		"-e", "GITEA__webhook__ALLOWED_HOST_LIST=*",
		"-e", "GITEA__server__ROOT_URL="+fmt.Sprintf("http://127.0.0.1:%d/", port),
		"-e", "GITEA__server__DISABLE_SSH=true",
		"-e", "GITEA__service__DISABLE_REGISTRATION=true",
		"-e", "GITEA__log__LEVEL=Warn",
		giteaImage)
	require.NoError(t, err, out)
	t.Cleanup(func() {
		_, _ = sh(context.Background(), "", "docker", "rm", "-f", "-v", name)
		_, _ = sh(context.Background(), "", "docker", "image", "rm", giteaImage)
	})

	g := &giteaAPI{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), user: "tester", pass: "correct-horse-battery"}
	require.True(t, eventually(120*time.Second, time.Second, func() bool {
		res, err := http.Get(g.base + "/api/v1/version")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == 200
	}), "gitea did not become ready")

	out, err = sh(ctx, "", "docker", "exec", name, "gitea", "admin", "user", "create", "--admin",
		"--username", g.user, "--password", g.pass, "--email", "tester@example.com", "--must-change-password=false")
	require.NoError(t, err, out)

	code, raw := g.do("POST", "/users/"+g.user+"/tokens", map[string]any{"name": "nebulactrl", "scopes": []string{"all"}})
	require.Equal(t, http.StatusCreated, code, string(raw))
	var tok struct {
		Sha1 string `json:"sha1"`
	}
	require.NoError(t, json.Unmarshal(raw, &tok))
	require.NotEmpty(t, tok.Sha1)
	g.token = tok.Sha1
	return g
}

// TestGiteaPushDeploysAndReportsStatus runs the official Gitea image: the
// connection registers the repository webhook through NebulaCtrl's API, a
// commit pushed to Gitea triggers a git_push deployment carrying the commit,
// and the deployment's commit status shows up on that commit in Gitea.
func TestGiteaPushDeploysAndReportsStatus(t *testing.T) {
	requireTier(t, tierFull)
	g := startGitea(t)
	s := ExclusiveStack(t, StackOptions{ListenAll: true})
	// Gitea runs in a container: it reaches the control plane through the host.
	port := s.Srv.Listener.Addr().(interface{ String() string }).String()
	port = port[strings.LastIndex(port, ":")+1:]
	s.App.Config.PublicURL = "http://host.docker.internal:" + port

	code, raw := g.do("POST", "/user/repos", map[string]any{"name": "app", "auto_init": true, "default_branch": "main", "private": true})
	require.Equal(t, http.StatusCreated, code, string(raw))

	r := s.Do("POST", "/git/connections", map[string]any{"provider": "gitea", "name": "itest gitea", "baseUrl": g.base, "authType": "token", "token": g.token})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	connID := r.JSON().Obj("connection").Str("id")
	require.Equal(t, g.user, r.JSON().Obj("connection").Str("accountLogin"))

	svc := s.NewService(SvcSpec{
		Name: "app", GitURL: g.base + "/tester/app.git", Health: map[string]any{"type": "none"},
		Config: map[string]any{"stopGraceSeconds": 1},
	})
	r = s.Do("PUT", "/services/"+svc.ID+"/git", map[string]any{"connectionId": connID, "repo": "tester/app", "branch": "main"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "active", r.JSON().Obj("webhook").Str("status"), string(r.Raw))

	// The webhook exists at Gitea, registered through our API.
	code, raw = g.do("GET", "/repos/tester/app/hooks", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	var hooks []struct {
		Config map[string]string `json:"config"`
		Events []string          `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &hooks))
	require.Len(t, hooks, 1)
	require.Equal(t, s.App.Config.PublicURL+"/hooks/git/gitea/"+connID, hooks[0].Config["url"])
	// Gitea expands pull_request into its sub-events (pull_request_sync, ...).
	require.Subset(t, hooks[0].Events, []string{"push", "pull_request"})

	// Push a commit (a Dockerfile that builds offline).
	dockerfile := base64.StdEncoding.EncodeToString([]byte("FROM scratch\nLABEL nebulactrl.test=gitea\n"))
	code, raw = g.do("POST", "/repos/tester/app/contents/Dockerfile", map[string]any{
		"content": dockerfile, "message": "Add Dockerfile\n\nfrom the itest", "branch": "main",
	})
	require.Equal(t, http.StatusCreated, code, string(raw))
	var pushed struct {
		Commit struct {
			Sha string `json:"sha"`
		} `json:"commit"`
	}
	require.NoError(t, json.Unmarshal(raw, &pushed))
	sha := pushed.Commit.Sha
	require.Len(t, sha, 40)

	var dep J
	require.True(t, eventually(90*time.Second, time.Second, func() bool {
		r := s.Do("GET", "/services/"+svc.ID+"/deployments", nil)
		for _, d := range r.JSON().Items() {
			if d.Str("trigger") == "git_push" {
				dep = d
				return true
			}
		}
		return false
	}), "the push did not trigger a deployment")
	require.Equal(t, sha, dep.Obj("commit").Str("sha"))
	require.Equal(t, "Add Dockerfile", dep.Obj("commit").Str("message"))
	require.Equal(t, "main", dep.Obj("commit").Str("branch"))
	require.NotEmpty(t, dep.Obj("commit").Str("author"))
	s.WaitDeployment(dep.Str("id"), 3*time.Minute)

	// The delivery is logged as processed and started that deployment.
	r = s.Do("GET", "/git/connections/"+connID+"/deliveries", nil)
	var processed bool
	for _, d := range r.JSON().Items() {
		if d.Str("outcome") == "processed" && d.Str("event") == "push" {
			processed = true
			require.Equal(t, "tester/app", d.Str("repo"))
		}
	}
	require.True(t, processed, string(r.Raw))

	// The deployment's status reaches the commit at Gitea, linking back.
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		code, raw := g.do("GET", "/repos/tester/app/commits/"+sha+"/statuses", nil)
		if code != http.StatusOK {
			return false
		}
		var sts []struct {
			Context   string `json:"context"`
			Status    string `json:"status"`
			TargetURL string `json:"target_url"`
		}
		if json.Unmarshal(raw, &sts) != nil {
			return false
		}
		for _, st := range sts {
			if st.Context == "nebulactrl/"+svc.Slug && (st.Status == "success" || st.Status == "failure") {
				return strings.Contains(st.TargetURL, dep.Str("id"))
			}
		}
		return false
	}), "no final commit status for the deployment")

	// Unlinking removes the webhook from Gitea again.
	require.Equal(t, http.StatusNoContent, s.Do("DELETE", "/services/"+svc.ID+"/git", nil).Status)
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		_, raw := g.do("GET", "/repos/tester/app/hooks", nil)
		return strings.TrimSpace(string(raw)) == "[]"
	}), "the webhook was not removed")
}

// commitFile adds a file to the main branch of tester/app and returns the commit sha.
func (g *giteaAPI) commitFile(path, message string) string {
	g.t.Helper()
	return g.commitContent(path, "FROM scratch\nLABEL nebulactrl.test="+path+"\n", message)
}

// commitContent adds a file with the given content to the main branch of tester/app and returns the commit sha.
func (g *giteaAPI) commitContent(path, content, message string) string {
	g.t.Helper()
	code, raw := g.do("POST", "/repos/tester/app/contents/"+path, map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)), "message": message, "branch": "main",
	})
	require.Equal(g.t, http.StatusCreated, code, string(raw))
	var out struct {
		Commit struct {
			Sha string `json:"sha"`
		} `json:"commit"`
	}
	require.NoError(g.t, json.Unmarshal(raw, &out))
	require.Len(g.t, out.Commit.Sha, 40)
	return out.Commit.Sha
}

// setStatus sets the ci/test commit status (the CI result) on a commit.
func (g *giteaAPI) setStatus(sha, state string) {
	g.t.Helper()
	code, raw := g.do("POST", "/repos/tester/app/statuses/"+sha, map[string]any{"state": state, "context": "ci/test", "description": "itest " + state})
	require.Equal(g.t, http.StatusCreated, code, string(raw))
}

// TestGiteaWaitForCI: with Wait for CI on, a pushed commit is parked while its
// status is pending, deploys once the status turns green, and is not deployed
// (and announced as deployment.ci_blocked) when the status turns red.
func TestGiteaWaitForCI(t *testing.T) {
	requireTier(t, tierFull)
	g := startGitea(t)
	s := ExclusiveStack(t, StackOptions{ListenAll: true})
	port := s.Srv.Listener.Addr().(interface{ String() string }).String()
	port = port[strings.LastIndex(port, ":")+1:]
	s.App.Config.PublicURL = "http://host.docker.internal:" + port

	code, raw := g.do("POST", "/user/repos", map[string]any{"name": "app", "auto_init": true, "default_branch": "main", "private": true})
	require.Equal(t, http.StatusCreated, code, string(raw))

	// A webhook channel that receives deployment.ci_blocked.
	var mu sync.Mutex
	var blocked []string // the raw bodies
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var b struct {
			Type string `json:"type"`
			Test bool   `json:"test"`
		}
		if json.Unmarshal(body, &b) == nil && !b.Test && b.Type == "deployment.ci_blocked" {
			mu.Lock()
			blocked = append(blocked, string(body))
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sink.Close)
	r := s.Do("POST", "/notifications/channels", map[string]any{
		"name": "ci-" + s.hostBase, "kind": "webhook", "secrets": map[string]any{"url": sink.URL},
		"rule": map[string]any{"eventTypes": []string{"deployment.ci_blocked"}, "allProjects": false, "projectIds": []string{s.ProjectID}},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	channelID := r.JSON().Obj("channel").Str("id")
	t.Cleanup(func() { s.Do("DELETE", "/notifications/channels/"+channelID, nil) })

	r = s.Do("POST", "/git/connections", map[string]any{"provider": "gitea", "name": "itest gitea", "baseUrl": g.base, "authType": "token", "token": g.token})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	connID := r.JSON().Obj("connection").Str("id")

	svc := s.NewService(SvcSpec{
		Name: "app", GitURL: g.base + "/tester/app.git", Health: map[string]any{"type": "none"},
		Config: map[string]any{"stopGraceSeconds": 1},
	})
	r = s.Do("PUT", "/services/"+svc.ID+"/git", map[string]any{"connectionId": connID, "repo": "tester/app", "branch": "main", "waitForCi": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("waitForCi"), string(r.Raw))

	pushDeployments := func() []J {
		var out []J
		for _, d := range s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items() {
			if d.Str("trigger") == "git_push" {
				out = append(out, d)
			}
		}
		return out
	}
	waiting := func() string {
		return s.Do("GET", "/services/"+svc.ID+"/git", nil).JSON().Obj("waitingForCi").Str("sha")
	}

	// 1. Pending: the push is parked, nothing deploys.
	sha := g.commitFile("Dockerfile", "Add Dockerfile")
	g.setStatus(sha, "pending")
	require.True(t, eventually(30*time.Second, time.Second, func() bool { return waiting() == sha }), "the push was not parked")
	time.Sleep(40 * time.Second)
	require.Empty(t, pushDeployments(), "a push with pending checks must not deploy")
	require.Equal(t, sha, waiting())

	// 2. Green: the parked push deploys.
	g.setStatus(sha, "success")
	var dep J
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		if ds := pushDeployments(); len(ds) > 0 {
			dep = ds[0]
			return true
		}
		return false
	}), "the green push did not deploy")
	require.Equal(t, sha, dep.Obj("commit").Str("sha"))
	require.Empty(t, waiting())
	s.WaitDeployment(dep.Str("id"), 3*time.Minute)

	// 3. Red: nothing deploys and the block is announced.
	sha2 := g.commitFile("second.txt", "Break CI")
	g.setStatus(sha2, "pending")
	require.True(t, eventually(30*time.Second, time.Second, func() bool { return waiting() == sha2 }), "the second push was not parked")
	g.setStatus(sha2, "failure")
	require.True(t, eventually(60*time.Second, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(blocked) > 0
	}), "no deployment.ci_blocked notification")
	mu.Lock()
	require.Contains(t, blocked[0], "CI failed: ci/test")
	mu.Unlock()
	require.Empty(t, waiting(), "a failed push is dropped")
	time.Sleep(15 * time.Second)
	require.Len(t, pushDeployments(), 1, "the failing commit must not deploy")
}

// TestGitPollDeploysWithoutWebhook: a service created from a plain clone URL
// (no connection, so no webhook) checks the branch head on a schedule and
// deploys a commit it did not know. With "deploy" off, a new commit is only
// shown (updateAvailable) and never deployed.
func TestGitPollDeploysWithoutWebhook(t *testing.T) {
	requireTier(t, tierFull)
	g := startGitea(t)
	app := appFixture(t)
	s := ExclusiveStack(t)

	// A public repository: the plain clone URL needs no token. Its Dockerfile builds on the fixture app image.
	code, raw := g.do("POST", "/user/repos", map[string]any{"name": "app", "auto_init": true, "default_branch": "main", "private": false})
	require.Equal(t, http.StatusCreated, code, string(raw))
	first := g.commitContent("Dockerfile", "FROM "+app.AppImage+"\n", "Add Dockerfile")

	svc := s.NewService(SvcSpec{Name: "app", GitURL: g.base + "/tester/app.git"})
	dep := s.DeployOK(svc.ID)
	require.Equal(t, first, dep.Obj("commit").Str("sha"), "the first deployment builds the branch head")
	pollPath := "/services/" + svc.ID + "/git-poll"
	view := func() J {
		r := s.Do("GET", pollPath, nil)
		require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
		return r.JSON()
	}
	deploymentsOf := func(sha string) []J {
		var out []J
		for _, d := range s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items() {
			if d.Obj("commit").Str("sha") == sha {
				out = append(out, d)
			}
		}
		return out
	}

	// 1. Off: nothing checks, the view says so.
	require.False(t, view().Bool("enabled"))
	r := s.Do("GET", "/services/"+svc.ID, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Nil(t, r.JSON()["gitPoll"], "no poll on the service view while off")

	// 2. Every minute (the harness shortens a minute to a second), deploy on: a pushed commit deploys within ~90 s.
	r = s.Do("PUT", pollPath, map[string]any{"enabled": true, "intervalMinutes": 1})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("enabled"))
	require.Equal(t, 1, r.JSON().Int("intervalMinutes"))
	require.True(t, r.JSON().Bool("deploy"), "deploys by default")

	sha := g.commitContent("second.txt", "two\n", "Second commit")
	var found J
	require.True(t, eventually(90*time.Second, time.Second, func() bool {
		if ds := deploymentsOf(sha); len(ds) > 0 {
			found = ds[0]
			return true
		}
		return false
	}), "checking did not find and deploy the new commit")
	require.Equal(t, "git_push", found.Str("trigger"))
	require.Equal(t, "main", found.Obj("commit").Str("branch"))
	require.Equal(t, "New commit found by checking main every minute", found.Str("message"))
	d := s.WaitDeployment(found.Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "logs:\n%s", s.DeploymentLogs(d.Str("id")))
	require.True(t, eventually(30*time.Second, time.Second, func() bool {
		v := view()
		return v.Str("headSha") == sha && !v.Bool("updateAvailable")
	}), "the view should show the running commit as the head: %s", s.Do("GET", pollPath, nil).Raw)
	time.Sleep(15 * time.Second)
	require.Len(t, deploymentsOf(sha), 1, "a commit is deployed once")

	// 3. Deploy off: the next commit is only shown.
	r = s.Do("PUT", pollPath, map[string]any{"enabled": true, "intervalMinutes": 1, "deploy": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.False(t, r.JSON().Bool("deploy"))
	sha2 := g.commitContent("third.txt", "three\n", "Third commit")
	require.True(t, eventually(90*time.Second, time.Second, func() bool {
		v := view()
		return v.Str("headSha") == sha2 && v.Bool("updateAvailable")
	}), "the new commit was not shown: %s", s.Do("GET", pollPath, nil).Raw)
	time.Sleep(25 * time.Second)
	require.Empty(t, deploymentsOf(sha2), "notify-only never deploys")

	// "Check now" reports the head without deploying, and the service view carries the update.
	r = s.Do("POST", pollPath+"/check", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, sha2, r.JSON().Str("headSha"))
	require.True(t, r.JSON().Bool("updateAvailable"))
	r = s.Do("GET", "/services/"+svc.ID, nil)
	require.True(t, r.JSON().Obj("gitPoll").Bool("updateAvailable"), string(r.Raw))
	require.Empty(t, deploymentsOf(sha2))

	// Deploying the latest commit by hand (what "Deploy latest" does) clears the update.
	s.DeployOK(svc.ID)
	require.True(t, eventually(30*time.Second, time.Second, func() bool { return !view().Bool("updateAvailable") }), string(s.Do("GET", pollPath, nil).Raw))
}
