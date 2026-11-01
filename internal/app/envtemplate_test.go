package app_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const exampleEnv = `# Name shown in the page title
APP_NAME=demo
# Postgres connection string
DATABASE_URL=
SECRET_KEY=changeme
PORT=3000
# Error reporting (optional)
SENTRY_DSN=
STRIPE_API_KEY=sk_test_example
`

// fakeGitea is a Gitea instance that serves the raw files of acme/shop.
type fakeGitea struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string]string
	reads []string // "ref path" of every raw read
}

func newFakeGitea(t *testing.T, files map[string]string) *fakeGitea {
	t.Helper()
	t.Setenv(netguard.AllowEnv, "127.0.0.1") // the control plane's provider client refuses loopback otherwise
	g := &fakeGitea{files: files}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const raw = "/api/v1/repos/acme/shop/raw/"
		switch {
		case r.URL.Path == "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
		case r.URL.Path == "/api/v1/repos/acme/shop":
			_, _ = w.Write([]byte(`{"full_name":"acme/shop","clone_url":"https://git.example.com/acme/shop.git","default_branch":"main","private":true}`))
		case r.URL.Path == "/api/v1/repos/acme/shop/hooks" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/repos/acme/shop/hooks" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":7}`))
		case strings.HasPrefix(r.URL.Path, raw):
			p := strings.TrimPrefix(r.URL.Path, raw)
			g.mu.Lock()
			body, ok := g.files[p]
			g.reads = append(g.reads, r.URL.Query().Get("ref")+" "+p)
			g.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitea) set(path, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[path] = body
}

func (g *fakeGitea) del(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.files, path)
}

func (g *fakeGitea) readsOf(path string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, r := range g.reads {
		if _, p, _ := strings.Cut(r, " "); p == path {
			out = append(out, r)
		}
	}
	return out
}

// connect registers the fake as a Gitea connection; it returns its id and webhook secret.
func (g *fakeGitea) connect(h *harness) (id, secret string) {
	h.t.Helper()
	r := h.do("POST", "/git/connections", "", map[string]any{
		"provider": "gitea", "name": "Gitea", "baseUrl": g.srv.URL, "authType": "token", "token": "gta_token_canary_123456",
	})
	require.Equal(h.t, http.StatusCreated, r.Status, string(r.Raw))
	return r.JSON()["connection"].(map[string]any)["id"].(string), r.JSON()["webhookSecret"].(string)
}

func withDatabaseExports(t *testing.T) {
	t.Helper()
	prev := services.RegisterExportProvider(func(in services.ExportInput) []services.Export {
		if in.Kind != services.KindDatabase {
			return nil
		}
		return []services.Export{{Key: "DATABASE_URL", Value: "postgres://" + in.Host, Description: "url"}}
	})
	t.Cleanup(func() { services.RegisterExportProvider(prev) })
}

func itemsByKey(r resp) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, it := range r.JSON()["items"].([]any) {
		m := it.(map[string]any)
		out[m["key"].(string)] = m
	}
	return out
}

func (h *harness) suggestedEnv(svc string) string {
	h.t.Helper()
	var raw *string
	require.NoError(h.t, h.app.Pool.QueryRow(context.Background(), `SELECT suggested_env::text FROM services WHERE id = $1`, uuid.MustParse(svc)).Scan(&raw))
	if raw == nil {
		return ""
	}
	return *raw
}

// giteaPush delivers a signed push to acme/shop's main branch. modified, when
// given, replaces the modified files the push lists.
func giteaPush(t *testing.T, h *harness, connID, secret, delivery string, modified ...string) int {
	t.Helper()
	body, err := os.ReadFile("../gitproviders/testdata/gitea_push.json")
	require.NoError(t, err)
	if len(modified) > 0 {
		var push map[string]any
		require.NoError(t, json.Unmarshal(body, &push))
		for _, c := range push["commits"].([]any) {
			c.(map[string]any)["modified"] = modified
		}
		body, err = json.Marshal(push)
		require.NoError(t, err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/hooks/git/gitea/"+connID, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", delivery)
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	return res.StatusCode
}

// The create flow: preview the keys of a repository before the service exists,
// create it with the accepted keys staged, see the deploy plan warn about the
// keys left empty, and keep the keys current (scan, push).
func TestEnvExamplePrefillThroughTheProviderAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()
	withDatabaseExports(t)
	gitea := newFakeGitea(t, map[string]string{".env.example": exampleEnv, "api/.env.sample": "API_URL=http://localhost:3000\n", ".env": "REAL_SECRET=hunter2\n"})
	connID, secret := gitea.connect(h)
	e := h.newVarEnv("prefill")
	e.service("postgres", "database", nil)

	// 1. Preview: smart defaults, nothing created.
	r := h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{
		"connectionId": connID, "repo": "acme/shop", "branch": "main", "buildContext": "api",
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "found", r.JSON()["status"], string(r.Raw))
	require.Equal(t, "provider", r.JSON()["method"])
	var files []string
	for _, f := range r.JSON()["files"].([]any) {
		files = append(files, f.(map[string]any)["path"].(string))
	}
	require.Equal(t, []string{".env.example", "api/.env.sample"}, files)
	byKey := itemsByKey(r)
	require.Equal(t, "demo", byKey["APP_NAME"]["value"], "a safe example value is kept")
	require.Equal(t, "Name shown in the page title", byKey["APP_NAME"]["comment"], "the comment above the key is the hint")
	require.Equal(t, "${{postgres.DATABASE_URL}}", byKey["DATABASE_URL"]["value"], "a database export of the environment fills the key")
	require.Equal(t, "${{secret(32)}}", byKey["SECRET_KEY"]["value"], "a secret-looking placeholder becomes a generator")
	require.Equal(t, true, byKey["SECRET_KEY"]["proposedSecret"])
	require.Equal(t, true, byKey["SENTRY_DSN"]["needsInput"], "an empty example value needs your own")
	require.Equal(t, "Error reporting (optional)", byKey["SENTRY_DSN"]["comment"])
	require.Equal(t, true, byKey["API_URL"]["needsInput"], "a localhost value is cleared")
	require.Equal(t, "", byKey["API_URL"]["value"])
	require.NotEmpty(t, byKey["API_URL"]["warnings"])
	require.Equal(t, "sk_test_example", byKey["STRIPE_API_KEY"]["value"])
	require.Equal(t, true, byKey["STRIPE_API_KEY"]["proposedSecret"])
	require.NotContains(t, byKey, "PORT", "platform keys are not proposed")
	require.NotContains(t, byKey, "REAL_SECRET")
	var skipped []string
	for _, s := range r.JSON()["skipped"].([]any) {
		skipped = append(skipped, s.(map[string]any)["key"].(string))
	}
	require.Equal(t, []string{"PORT"}, skipped, "the platform key is reported, not silently dropped")
	require.Empty(t, gitea.readsOf(".env"), "a real .env is never requested")
	var services, scans int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT count(*) FROM services WHERE deleted_at IS NULL`).Scan(&services))
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_commands WHERE kind = 'env_scan'`).Scan(&scans))
	require.Equal(t, 1, services, "a preview creates nothing")
	require.Zero(t, scans, "the provider route needs no agent")

	// A preview without a repository is refused; an unreadable one says why.
	r = h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	r = h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{"connectionId": connID, "repo": "acme/shop", "buildContext": "../x"})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))

	// 2. Create the service with the accepted keys staged (source suggested).
	r = h.do("POST", "/environments/"+e.envID+"/services", "", map[string]any{
		"name": "shop", "sourceType": "git", "serverId": e.srvID,
		"config": map[string]any{"gitUrl": "https://git.example.com/acme/shop.git", "gitBranch": "main", "buildContext": "api", "port": 3000},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := r.JSON()["id"].(string)
	r = h.do("PUT", "/services/"+svc+"/git", "", map[string]any{"connectionId": connID, "repo": "acme/shop", "branch": "main", "autoDeploy": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	gen := h.generation(svc)

	r = h.put(svc,
		v("APP_NAME", "demo", "source", "suggested"),
		v("DATABASE_URL", "${{postgres.DATABASE_URL}}", "source", "suggested"),
		v("SECRET_KEY", "${{secret(32)}}", "isSecret", true, "source", "suggested"),
		v("SENTRY_DSN", "", "source", "suggested"),
	)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Greater(t, h.generation(svc), gen, "accepted keys are staged changes")
	vars := h.variables(svc)
	require.Equal(t, "suggested", vars["APP_NAME"]["source"])
	require.Equal(t, "suggested", vars["DATABASE_URL"]["source"])
	require.Equal(t, "generated", vars["SECRET_KEY"]["source"], "a generator stays a generated variable")
	require.Equal(t, "suggested", vars["SENTRY_DSN"]["source"])
	require.Equal(t, true, vars["SENTRY_DSN"]["set"])
	require.NotContains(t, string(h.do("GET", "/services/"+svc+"/variables", "", nil).Raw), "changeme")

	r = h.put(svc, v("BAD", "x", "source", "imported"))
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, "clients may only mark a variable as suggested")

	// 3. The plan warns, and names the keys that have no value.
	plan := func() []any {
		r := h.do("POST", "/services/"+svc+"/deployments/plan", "", nil)
		require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
		return r.JSON()["warnings"].([]any)
	}
	var found string
	for _, w := range plan() {
		if strings.Contains(w.(string), "from .env.example") {
			found = w.(string)
		}
	}
	require.Contains(t, found, "1 variable from .env.example has no value: SENTRY_DSN")
	r = h.do("POST", "/services/"+svc+"/deployments/plan", "", nil)
	require.Empty(t, r.JSON()["blocking"], "an empty suggested variable warns, it does not block")

	// Setting the value (any normal write) makes it a manual variable and clears the warning.
	require.Equal(t, http.StatusOK, h.put(svc, v("SENTRY_DSN", "https://sentry.example/1")).Status)
	require.Equal(t, "manual", h.variables(svc)["SENTRY_DSN"]["source"])
	for _, w := range plan() {
		require.NotContains(t, w.(string), "from .env.example")
	}

	// 4. Scanning again stores the files and offers only the keys not set yet.
	r = h.do("POST", "/services/"+svc+"/env-template-scan", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "found", r.JSON()["status"])
	require.Equal(t, "provider", r.JSON()["method"])
	left := itemsByKey(r)
	require.ElementsMatch(t, []string{"API_URL", "STRIPE_API_KEY"}, keysOf(left), "keys that are set are not offered again")
	require.Contains(t, h.suggestedEnv(svc), "STRIPE_API_KEY")
	r = h.do("GET", "/services/"+svc+"/suggested-variables", "", nil)
	require.ElementsMatch(t, []string{"API_URL", "STRIPE_API_KEY"}, keysOf(itemsByKey(r)))

	// 5. A push to the tracked branch re-reads the files: new keys show up.
	gitea.set(".env.example", exampleEnv+"# Added later\nNEW_FEATURE_FLAG=on\n")
	require.Equal(t, http.StatusOK, giteaPush(t, h, connID, secret, "prefill-push-1", ".env.example"))
	require.Eventually(t, func() bool { return strings.Contains(h.suggestedEnv(svc), "NEW_FEATURE_FLAG") }, 20*time.Second, 100*time.Millisecond)
	r = h.do("GET", "/services/"+svc+"/suggested-variables", "", nil)
	require.Contains(t, keysOf(itemsByKey(r)), "NEW_FEATURE_FLAG")

	// Removing a file's keys drops them on the next read.
	gitea.set(".env.example", "APP_NAME=demo\n")
	gitea.del("api/.env.sample")
	r = h.do("POST", "/services/"+svc+"/env-template-scan", "", nil)
	require.Equal(t, "found", r.JSON()["status"])
	require.Empty(t, r.JSON()["items"], "every key still listed is set already")
	require.NotContains(t, h.suggestedEnv(svc), "NEW_FEATURE_FLAG")

	// A service without a git source has nothing to scan.
	img := e.service("cache", "app", nil)
	r = h.do("POST", "/services/"+img+"/env-template-scan", "", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	require.Equal(t, "not_git_source", r.JSON()["code"])
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A push that lists its changed files and touches no template file does not
// read the repository again once the files were read.
func TestPushWithoutTemplateChangesSkipsTheRead(t *testing.T) {
	h := newHarness(t)
	h.setup()
	gitea := newFakeGitea(t, map[string]string{".env.example": "A=1\n"})
	connID, secret := gitea.connect(h)
	e := h.newVarEnv("pushskip")
	svc := e.service("shop", "app", map[string]any{"sourceType": "git", "config": map[string]any{"gitUrl": "https://git.example.com/acme/shop.git", "gitBranch": "main", "port": 80}})
	require.Equal(t, http.StatusOK, h.do("PUT", "/services/"+svc+"/git", "", map[string]any{"connectionId": connID, "repo": "acme/shop", "branch": "main", "autoDeploy": false}).Status)
	require.Equal(t, "found", h.do("POST", "/services/"+svc+"/env-template-scan", "", nil).JSON()["status"])
	reads := len(gitea.readsOf(".env.example"))

	// gitea_push.json changes services/..., not an env file; Gitea lists the files.
	require.Equal(t, http.StatusOK, giteaPush(t, h, connID, secret, "skip-1"))
	time.Sleep(500 * time.Millisecond)
	require.Len(t, gitea.readsOf(".env.example"), reads, "no template file changed: no re-read")
}

// answerEnvScan plays the agent for the next env_scan command of a server: it
// finishes it with result (or error) and reports the persisted payload and the
// send-time secrets the control plane would attach.
func (h *harness) answerEnvScan(serverID string, result agentprotocol.EnvScanResult, cmdErr string) <-chan [2]string {
	h.t.Helper()
	out := make(chan [2]string, 1)
	go func() {
		ctx := context.Background()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var id uuid.UUID
			var payload []byte
			err := h.app.Pool.QueryRow(ctx, `SELECT id, payload FROM agent_commands WHERE server_id = $1 AND kind = 'env_scan' AND status IN ('pending','sent') ORDER BY created_at LIMIT 1`,
				uuid.MustParse(serverID)).Scan(&id, &payload)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			secrets, _ := h.deps().SecretsForCommand(ctx, id, agentprotocol.KindEnvScan, payload)
			sj, _ := json.Marshal(secrets)
			out <- [2]string{string(payload), string(sj)}
			if cmdErr != "" {
				frame, _ := json.Marshal(agentprotocol.ResultFrame{CommandID: id.String(), Outcome: agentprotocol.OutcomeFailed, Error: cmdErr})
				_, _ = h.app.Pool.Exec(ctx, `UPDATE agent_commands SET status='failed', error=$2, result=$3, finished_at=now() WHERE id=$1`, id, cmdErr, frame)
				return
			}
			data, _ := json.Marshal(result)
			frame, _ := json.Marshal(agentprotocol.ResultFrame{CommandID: id.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data})
			_, _ = h.app.Pool.Exec(ctx, `UPDATE agent_commands SET status='succeeded', result=$2, finished_at=now() WHERE id=$1`, id, frame)
			return
		}
		close(out)
	}()
	return out
}

// Hosts without a file API (Bitbucket, generic HTTPS) and private repositories
// behind a pasted token are read by the agent; the token never rides in the
// persisted command.
func TestEnvTemplatesReadByTheAgent(t *testing.T) {
	h := newHarness(t)
	h.setup()
	withDatabaseExports(t)
	e := h.newVarEnv("agentscan")
	e.service("postgres", "database", nil)
	online := h.newOnlineServer("builder", 8*gib, gib)
	const token = "pasted_token_canary_987654"
	result := agentprotocol.EnvScanResult{
		Outcome: agentprotocol.OutcomeSucceeded, Commit: "0123456789abcdef0123456789abcdef01234567",
		Files: []agentprotocol.EnvFileScan{{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{
			{Key: "DATABASE_URL", Value: "postgres://localhost/app"}, {Key: "SECRET_KEY", Value: "changeme"}, {Key: "APP_NAME", Value: "demo"},
		}}},
	}

	// Preview with a pasted token on a host that has no file API.
	seen := h.answerEnvScan(online, result, "")
	r := h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{
		"gitUrl": "https://git.example.org/acme/private.git", "branch": "main", "gitToken": token, "serverId": online,
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	frames := <-seen
	require.NotContains(t, frames[0], token, "the pasted token is never persisted")
	require.Contains(t, frames[1], token, "it is attached at send time")
	var p agentprotocol.EnvScanPayload
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &p))
	require.True(t, p.HasToken)
	require.Empty(t, p.ServiceID)
	require.Equal(t, "https://git.example.org/acme/private.git", p.GitURL)
	require.Equal(t, "found", r.JSON()["status"], string(r.Raw))
	require.Equal(t, "agent", r.JSON()["method"])
	require.Equal(t, result.Commit, r.JSON()["commit"])
	byKey := itemsByKey(r)
	require.Equal(t, "${{postgres.DATABASE_URL}}", byKey["DATABASE_URL"]["value"], "the localhost URL is replaced by the managed database")
	require.Equal(t, "${{secret(32)}}", byKey["SECRET_KEY"]["value"])
	require.Equal(t, "demo", byKey["APP_NAME"]["value"])
	require.NotContains(t, string(r.Raw), token)

	// The token is gone once the scan ended: a redelivery would fail instead of leaking.
	var cmd uuid.UUID
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT id FROM agent_commands WHERE kind = 'env_scan'`).Scan(&cmd))
	_, err := h.deps().SecretsForCommand(context.Background(), cmd, agentprotocol.KindEnvScan, []byte(frames[0]))
	require.Error(t, err)

	// An agent failure is reported, not raised: the dialog keeps working.
	seen = h.answerEnvScan(online, agentprotocol.EnvScanResult{}, "git fetch: authentication required")
	r = h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{
		"gitUrl": "https://git.example.org/acme/private.git", "serverId": online,
	})
	<-seen
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "unavailable", r.JSON()["status"])
	require.Contains(t, r.JSON()["message"], "authentication required")

	// A read that needs an agent needs a server, and an offline one cannot answer.
	r = h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{"gitUrl": "https://git.example.org/acme/x.git"})
	require.Equal(t, "unavailable", r.JSON()["status"])
	require.Contains(t, r.JSON()["message"], "Pick a server")
	offline := h.newOnlineServer("sleeper", 8*gib, gib)
	_, err = h.app.Pool.Exec(context.Background(), `UPDATE servers SET status='offline' WHERE id = $1`, uuid.MustParse(offline))
	require.NoError(t, err)
	r = h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{"gitUrl": "https://git.example.org/acme/x.git", "serverId": offline})
	require.Equal(t, "unavailable", r.JSON()["status"])
	require.Contains(t, r.JSON()["message"], "offline")
	r = h.do("POST", "/environments/"+e.envID+"/env-template-preview", "", map[string]any{"gitUrl": "git@example.org:acme/x.git", "serverId": online})
	require.Equal(t, "unavailable", r.JSON()["status"])
	require.Contains(t, r.JSON()["message"], "deploy key")

	// An existing service reads with its stored token (also send-time only).
	r = h.do("POST", "/environments/"+e.envID+"/services", "", map[string]any{
		"name": "shop", "sourceType": "git", "serverId": online, "gitToken": token,
		"config": map[string]any{"gitUrl": "https://git.example.org/acme/private.git", "gitBranch": "main", "port": 80},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := r.JSON()["id"].(string)
	_, err = h.app.Pool.Exec(context.Background(), `DELETE FROM agent_commands WHERE kind = 'env_scan'`)
	require.NoError(t, err)
	seen = h.answerEnvScan(online, result, "")
	r = h.do("POST", "/services/"+svc+"/env-template-scan", "", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	frames = <-seen
	require.NotContains(t, frames[0], token)
	require.Contains(t, frames[1], token)
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &p))
	require.Equal(t, svc, p.ServiceID)
	require.Equal(t, "found", r.JSON()["status"])
	require.Contains(t, h.suggestedEnv(svc), "0123456789abcdef0123456789abcdef01234567", "the agent's commit is stored with the files")
	r = h.do("GET", "/services/"+svc+"/suggested-variables", "", nil)
	require.Equal(t, result.Commit, r.JSON()["commit"])
	require.Equal(t, "${{postgres.DATABASE_URL}}", itemsByKey(r)["DATABASE_URL"]["value"])
}

// Creating a git service, or changing its repository or branch, reads the
// template files in the background through the git provider.
func TestSourceChangesScheduleATemplateRead(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.startJobs()
	gitea := newFakeGitea(t, map[string]string{".env.example": "FROM_CREATE=1\n"})
	connID, _ := gitea.connect(h)
	e := h.newVarEnv("sourcejob")
	r := h.do("POST", "/environments/"+e.envID+"/services", "", map[string]any{
		"name": "shop", "sourceType": "git", "serverId": e.srvID,
		"config": map[string]any{"gitUrl": "https://git.example.com/acme/shop.git", "gitBranch": "main", "port": 80},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svc := r.JSON()["id"].(string)
	require.Equal(t, http.StatusOK, h.do("PUT", "/services/"+svc+"/git", "", map[string]any{"connectionId": connID, "repo": "acme/shop", "branch": "main", "autoDeploy": false}).Status)

	require.Eventually(t, func() bool { return strings.Contains(h.suggestedEnv(svc), "FROM_CREATE") }, 30*time.Second, 200*time.Millisecond,
		"a new git service's template files are read without being asked")

	// A branch change reads that branch.
	gitea.set(".env.example", "ON_NEXT_BRANCH=1\n")
	r = h.do("GET", "/services/"+svc, "", nil)
	cfg := r.JSON()["config"].(map[string]any)
	cfg["gitBranch"] = "next"
	r = h.do("PATCH", "/services/"+svc, "", map[string]any{"config": cfg}, "If-Match", `"`+strconv.Itoa(int(r.JSON()["generation"].(float64)))+`"`)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Eventually(t, func() bool { return strings.Contains(h.suggestedEnv(svc), "ON_NEXT_BRANCH") }, 30*time.Second, 200*time.Millisecond,
		"a branch change reads the new branch")
	require.Contains(t, gitea.readsOf(".env.example")[len(gitea.readsOf(".env.example"))-1], "next .env.example")
	require.NotContains(t, h.suggestedEnv(svc), "FROM_CREATE")
}
