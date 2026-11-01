//go:build integration

package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeGitea is a minimal Gitea API: repository metadata, webhook registration,
// commit statuses and issue comments. It records what NebulaCtrl wrote.
type fakeGitea struct {
	srv      *httptest.Server
	cloneURL string

	mu       sync.Mutex
	hookURL  string
	secret   string
	comments map[int][]*fakeComment // issue number → comments
	statuses []fakeStatus
	nextID   int64
}

type fakeComment struct {
	ID   int64
	Body string
}

type fakeStatus struct {
	SHA, State, Context, Description, Target string
	At                                       time.Time
}

func newFakeGitea(t *testing.T, cloneURL string) *fakeGitea {
	g := &fakeGitea{cloneURL: cloneURL, comments: map[int][]*fakeComment{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitea) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (g *fakeGitea) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && path == "/user":
		g.writeJSON(w, 200, map[string]any{"login": "itest"})
	case r.Method == http.MethodGet && path == "/repos/acme/app":
		g.writeJSON(w, 200, map[string]any{
			"full_name": "acme/app", "clone_url": g.cloneURL, "default_branch": "main", "private": false,
		})
	case r.Method == http.MethodGet && path == "/repos/acme/app/hooks":
		if g.hookURL == "" {
			g.writeJSON(w, 200, []any{})
			return
		}
		g.writeJSON(w, 200, []any{map[string]any{"id": 1, "config": map[string]string{"url": g.hookURL}}})
	case r.Method == http.MethodPost && path == "/repos/acme/app/hooks":
		var in struct {
			Config map[string]string `json:"config"`
		}
		_ = json.Unmarshal(body, &in)
		g.hookURL, g.secret = in.Config["url"], in.Config["secret"]
		g.writeJSON(w, 201, map[string]any{"id": 1})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/repos/acme/app/statuses/"):
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		g.statuses = append(g.statuses, fakeStatus{
			SHA: strings.TrimPrefix(path, "/repos/acme/app/statuses/"), State: in["state"], Context: in["context"],
			Description: in["description"], Target: in["target_url"], At: time.Now(),
		})
		g.writeJSON(w, 201, map[string]any{})
	case strings.HasPrefix(path, "/repos/acme/app/issues/"):
		g.issues(w, r, strings.TrimPrefix(path, "/repos/acme/app/issues/"), body)
	default:
		g.writeJSON(w, 404, map[string]any{"message": "not found: " + r.Method + " " + path})
	}
}

func (g *fakeGitea) issues(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	var in struct {
		Body string `json:"body"`
	}
	_ = json.Unmarshal(body, &in)
	if id, ok := strings.CutPrefix(rest, "comments/"); ok && r.Method == http.MethodPatch {
		cid, _ := strconv.ParseInt(id, 10, 64)
		for _, list := range g.comments {
			for _, c := range list {
				if c.ID == cid {
					c.Body = in.Body
					g.writeJSON(w, 200, map[string]any{"id": c.ID})
					return
				}
			}
		}
		g.writeJSON(w, 404, map[string]any{"message": "no such comment"})
		return
	}
	numStr, tail, _ := strings.Cut(rest, "/")
	n, _ := strconv.Atoi(numStr)
	if tail != "comments" {
		g.writeJSON(w, 404, map[string]any{"message": "not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := make([]map[string]any, 0, len(g.comments[n]))
		for _, c := range g.comments[n] {
			out = append(out, map[string]any{"id": c.ID, "body": c.Body})
		}
		g.writeJSON(w, 200, out)
	case http.MethodPost:
		g.nextID++
		g.comments[n] = append(g.comments[n], &fakeComment{ID: g.nextID, Body: in.Body})
		g.writeJSON(w, 201, map[string]any{"id": g.nextID})
	default:
		g.writeJSON(w, 405, map[string]any{"message": "method not allowed"})
	}
}

func (g *fakeGitea) webhookSecret() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.secret
}

func (g *fakeGitea) commentOn(n int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.comments[n]) == 0 {
		return ""
	}
	return g.comments[n][0].Body
}

func (g *fakeGitea) commentCount(n int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.comments[n])
}

// statusesOf returns the recorded statuses in write order.
func (g *fakeGitea) statusesOf() []fakeStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]fakeStatus(nil), g.statuses...)
}

// lastStatusFor is the latest status written for a context: the provider shows
// one status per context, and the per-deployment statuses
// ("nebulactrl/<service>") interleave with the preview's aggregate one.
func (g *fakeGitea) lastStatusFor(context string) (fakeStatus, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.statuses) - 1; i >= 0; i-- {
		if g.statuses[i].Context == context {
			return g.statuses[i], true
		}
	}
	return fakeStatus{}, false
}

// giteaPullRequest is a Gitea `pull_request` webhook body (shape of
// internal/gitproviders/testdata/gitea_pull_request.json, which follows
// https://docs.gitea.com/usage/webhooks).
func giteaPullRequest(action string, number int, title, sha, branch, cloneURL string, merged bool) []byte {
	repo := map[string]any{"full_name": "acme/app", "clone_url": cloneURL, "fork": false}
	b, _ := json.Marshal(map[string]any{
		"action": action, "number": number,
		"pull_request": map[string]any{
			"id": 11, "number": number, "html_url": "https://git.example.com/acme/app/pulls/" + strconv.Itoa(number),
			"user":  map[string]any{"id": 2, "login": "grace", "username": "grace"},
			"title": title, "state": "open", "merged": merged,
			"head": map[string]any{"label": branch, "ref": branch, "sha": sha, "repo": repo},
			"base": map[string]any{"label": "main", "ref": "main", "sha": sha, "repo": repo},
		},
		"repository": repo, "sender": map[string]any{"id": 2, "login": "grace"},
	})
	return b
}

// deliverWebhook posts a signed Gitea delivery to the control plane.
func (s *Stack) deliverWebhook(connID, secret string, body []byte) {
	s.t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, s.Srv.URL+"/hooks/git/gitea/"+connID, bytes.NewReader(body))
	require.NoError(s.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Delivery", uuid.NewString())
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	require.Equal(s.t, http.StatusOK, res.StatusCode, string(out))
}

func (s *Stack) previewList() []J {
	s.t.Helper()
	r := s.Do("GET", "/projects/"+s.ProjectID+"/previews", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON().Items()
}

// A pull request opened against a connected repository gets its own
// environment with a FRESH database (new credentials, not the base's), its own
// URL, a sticky comment and a commit status; closing the pull request removes
// the environment, its containers, volumes and domains.
func TestPreviewLifecycleWithFreshDatabase(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	git := gitServer(t)
	s := ExclusiveStack(t)
	_ = app

	// Generated preview hostnames live under <base domain>; *.localhost
	// resolves to the harness proxy.
	r := s.Do("PATCH", "/servers/"+s.ServerID, map[string]any{"baseDomain": "localhost"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// A fake Gitea (API + clone URL of the fixture git server) and its connection.
	gt := newFakeGitea(t, git.URL)
	r = s.Do("POST", "/git/connections", map[string]any{"provider": "gitea", "baseUrl": gt.srv.URL, "authType": "token", "token": "gt-token", "name": "fake gitea"})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	connID := r.JSON().Obj("connection").Str("id")
	secret := r.JSON().Str("webhookSecret")
	require.NotEmpty(t, secret)

	// Base (production) environment: a database and an app that uses it. Neither
	// is deployed: previews are cloned from the definition.
	pg := s.createDB("pg", "postgres", "17")
	api := s.NewService(SvcSpec{
		Name: "api", GitURL: git.URL, Domain: "api.localhost",
		Vars: map[string]string{"VERSION": "v1", "DATABASE_URL": "${{" + pg.Slug + ".DATABASE_URL}}"},
	})
	r = s.Do("PUT", "/services/"+api.ID+"/git", map[string]any{"connectionId": connID, "repo": "acme/app", "branch": "main", "autoDeploy": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotEmpty(t, gt.webhookSecret(), "linking registers the webhook on the provider")

	r = s.Do("PUT", "/projects/"+s.ProjectID+"/previews/settings", map[string]any{"enabled": true, "baseEnvironmentId": s.EnvID})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.EqualValues(t, 5, r.JSON()["maxConcurrent"])
	require.EqualValues(t, 7, r.JSON()["idleDays"])

	// PR #12 opens.
	require.Equal(t, secret, gt.webhookSecret(), "the provider's webhook is signed with the connection's secret")
	s.deliverWebhook(connID, secret, giteaPullRequest("opened", 12, "Add checkout", git.CommitSHA, "main", git.URL, false))

	var preview J
	require.Truef(t, eventually(6*time.Minute, time.Second, func() bool {
		for _, p := range s.previewList() {
			if p.Int("prNumber") == 12 {
				preview = p
				return p.Str("status") == "ready" || p.Str("status") == "failed"
			}
		}
		return false
	}), "preview did not finish: %v", preview)
	if id := preview.Str("environmentId"); id != "" {
		s.TrackEnv(id) // a failed preview leaves its containers and networks behind otherwise
	}
	require.Equal(t, "ready", preview.Str("status"), "preview: %v", preview)
	envID := preview.Str("environmentId")
	require.Equal(t, "pr-12", preview.Str("environmentSlug"))
	require.Equal(t, []any{"https://api-pr-12.localhost"}, preview["urls"])

	// The environment is a real one, grouped under its base.
	var env J
	for _, e := range s.Do("GET", "/projects/"+s.ProjectID+"/environments", nil).JSON().Items() {
		if e.Str("id") == envID {
			env = e
		}
	}
	require.NotNil(t, env)
	require.Equal(t, "preview", env.Str("kind"))
	require.Equal(t, "PR #12 · Add checkout", env.Str("name"))
	require.Equal(t, s.EnvID, env.Str("parentEnvironmentId"))

	// Services were cloned; the preview's database is fresh.
	svcs := s.Do("GET", "/environments/"+envID+"/services", nil).JSON().Items()
	require.Len(t, svcs, 2)
	var pvPG, pvAPI J
	for _, sv := range svcs {
		switch sv.Str("slug") {
		case pg.Slug:
			pvPG = sv
		case api.Slug:
			pvAPI = sv
		}
		s.servicesMu.Lock()
		s.serviceIDs = append(s.serviceIDs, sv.Str("id"))
		s.servicesMu.Unlock()
	}
	require.NotNil(t, pvPG)
	require.NotNil(t, pvAPI)
	require.NotEqual(t, pg.ID, pvPG.Str("id"))
	baseURL := s.Do("POST", "/services/"+pg.ID+"/database/reveal", map[string]any{"key": "DATABASE_URL"}).JSON().Str("value")
	previewURL := s.Do("POST", "/services/"+pvPG.Str("id")+"/database/reveal", map[string]any{"key": "DATABASE_URL"}).JSON().Str("value")
	require.NotEmpty(t, baseURL)
	require.NotEmpty(t, previewURL)
	require.NotEqual(t, passwordOf(t, baseURL), passwordOf(t, previewURL), "the preview database has its own credentials")

	// The app in the preview talks to the preview database, at its own URL.
	waitConsumer(t, NewProxyClient("api-pr-12.localhost"), "/db", "db ok postgres", 90*time.Second)
	// The base environment was never deployed.
	require.Nil(t, s.Service(api.ID)["currentDeploymentId"])
	// The deployments of the preview carry the pull request's commit and the preview trigger.
	deps := s.Do("GET", "/services/"+pvAPI.Str("id")+"/deployments", nil).JSON().Items()
	require.NotEmpty(t, deps)
	require.Equal(t, "preview", deps[0].Str("trigger"))

	// The sticky comment and the aggregate commit status. The provider keeps one
	// status per context: the linked api service reports its own deployment
	// ("nebulactrl/api") from the 10 s git sweep, which can land after the
	// preview's aggregate status, so the assertion is on the preview context.
	if !eventually(30*time.Second, 500*time.Millisecond, func() bool {
		st, ok := gt.lastStatusFor("nebulactrl/preview")
		return ok && st.State == "success" && st.SHA == git.CommitSHA
	}) {
		t.Fatalf("the preview commit status never reached success on %s\n%s", git.CommitSHA, s.previewDiagnostics(gt, 12, envID))
	}
	comment := gt.commentOn(12)
	require.Contains(t, comment, "<!-- nebulactrl-preview:"+s.ProjectID+" -->")
	require.Contains(t, comment, "https://api-pr-12.localhost")
	require.Contains(t, comment, "ready")
	require.Equal(t, 1, gt.commentCount(12))

	// PR #12 closes: everything goes away.
	s.deliverWebhook(connID, secret, giteaPullRequest("closed", 12, "Add checkout", git.CommitSHA, "main", git.URL, false))
	require.Truef(t, eventually(3*time.Minute, time.Second, func() bool { return len(s.previewList()) == 0 }), "preview was not deleted: %v", s.previewList())
	for _, e := range s.Do("GET", "/projects/"+s.ProjectID+"/environments", nil).JSON().Items() {
		require.NotEqual(t, envID, e.Str("id"), "the preview environment must be gone")
	}
	for _, id := range []string{pvPG.Str("id"), pvAPI.Str("id")} {
		require.Truef(t, eventually(2*time.Minute, time.Second, func() bool {
			out, _ := sh(t.Context(), "", "docker", "ps", "-aq", "--filter", "label=nebulactrl.service="+id)
			return out == ""
		}), "containers of %s were not removed", id)
	}
	// remove_service deleted the preview database's volume with its data.
	require.Truef(t, eventually(time.Minute, time.Second, func() bool {
		out, _ := sh(t.Context(), "", "docker", "volume", "ls", "-q", "--filter", "name=nc-vol-"+agentprotocol.Short(pvPG.Str("id"))+"-")
		return out == ""
	}), "the preview database volume was not removed")
	require.Contains(t, gt.commentOn(12), "deleted")
	require.Equal(t, 1, gt.commentCount(12), "the comment was updated, not duplicated")
	// The base environment and its definitions are untouched.
	require.Len(t, s.Do("GET", "/environments/"+s.EnvID+"/services", nil).JSON().Items(), 2)
}

func passwordOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	require.NotEmpty(t, pw, fmt.Sprintf("no password in %q", dsn))
	return pw
}

// previewDiagnostics describes why the preview's commit status is not what the
// test expects: every status the fake provider recorded (in write order), the
// preview environment's deployments with their per-deployment status reports,
// and the preview record itself.
func (s *Stack) previewDiagnostics(gt *fakeGitea, pr int, envID string) string {
	var b strings.Builder
	b.WriteString("commit statuses recorded by the provider, oldest first:\n")
	for i, st := range gt.statusesOf() {
		fmt.Fprintf(&b, "  %d. %s context=%s state=%s sha=%.12s description=%q target=%s\n", i+1, st.At.Format("15:04:05.000"), st.Context, st.State, st.SHA, st.Description, st.Target)
	}
	ctx := s.t.Context()
	b.WriteString("deployments of the preview environment, oldest first:\n")
	rows, err := s.Pool.Query(ctx, `
		SELECT sv.slug, d.status, d.status_message, d.trigger, d.commit_sha,
		       coalesce(r.state, ''), coalesce(r.error, ''), coalesce(r.attempts, 0), d.created_at, d.finished_at
		FROM deployments d
		JOIN services sv ON sv.id = d.service_id
		LEFT JOIN git_status_reports r ON r.deployment_id = d.id
		WHERE sv.environment_id = $1
		ORDER BY d.created_at`, envID)
	if err != nil {
		fmt.Fprintf(&b, "  query failed: %v\n", err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var slug, status, msg, trigger, sha, reported, reportErr string
			var attempts int
			var created time.Time
			var finished *time.Time
			if err := rows.Scan(&slug, &status, &msg, &trigger, &sha, &reported, &reportErr, &attempts, &created, &finished); err != nil {
				fmt.Fprintf(&b, "  scan failed: %v\n", err)
				break
			}
			fin := "-"
			if finished != nil {
				fin = finished.Format("15:04:05.000")
			}
			fmt.Fprintf(&b, "  %s: status=%s statusMessage=%q trigger=%s sha=%.12s created=%s finished=%s statusReport{state=%q attempts=%d error=%q}\n",
				slug, status, msg, trigger, sha, created.Format("15:04:05.000"), fin, reported, attempts, reportErr)
		}
	}
	b.WriteString("preview record:\n")
	var status, msg, headSHA string
	var updated time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT status, status_message, head_sha, updated_at FROM previews WHERE environment_id = $1`, envID).
		Scan(&status, &msg, &headSHA, &updated); err != nil {
		fmt.Fprintf(&b, "  query failed: %v\n", err)
	} else {
		fmt.Fprintf(&b, "  pr=%d status=%s statusMessage=%q headSha=%.12s updatedAt=%s\n", pr, status, msg, headSHA, updated.Format("15:04:05.000"))
	}
	b.WriteString("preview publish jobs:\n")
	jr, err := s.Pool.Query(ctx, `SELECT state, attempt, coalesce(errors::text, ''), created_at, finalized_at FROM river_job WHERE kind = 'preview_publish' ORDER BY id`)
	if err != nil {
		fmt.Fprintf(&b, "  query failed: %v\n", err)
	} else {
		defer jr.Close()
		for jr.Next() {
			var state, errs string
			var attempt int
			var created time.Time
			var fin *time.Time
			if err := jr.Scan(&state, &attempt, &errs, &created, &fin); err != nil {
				fmt.Fprintf(&b, "  scan failed: %v\n", err)
				break
			}
			fmt.Fprintf(&b, "  state=%s attempt=%d created=%s errors=%s\n", state, attempt, created.Format("15:04:05.000"), errs)
		}
	}
	return b.String()
}
