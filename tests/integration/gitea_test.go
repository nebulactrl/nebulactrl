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
	"strings"
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
