package gitproviders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ----

type nopDispatcher struct{}

func (nopDispatcher) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (nopDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type depCall struct {
	ServiceID uuid.UUID
	Trigger   string
	Commit    deployments.CommitInfo
}

// fakeDeployer records deployments; fail, when set, decides the error per call.
type fakeDeployer struct {
	mu    sync.Mutex
	calls []depCall
	tries int
	fail  func(n int, c depCall) error
}

func (f *fakeDeployer) DeployInternal(_ context.Context, id uuid.UUID, trigger string, commit *deployments.CommitInfo, _ string) (deployments.DeploymentView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := depCall{ServiceID: id, Trigger: trigger}
	if commit != nil {
		c.Commit = *commit
	}
	n := f.tries
	f.tries++
	if f.fail != nil {
		if err := f.fail(n, c); err != nil {
			return deployments.DeploymentView{}, err // a refused deployment is not a deployment
		}
	}
	f.calls = append(f.calls, c)
	return deployments.DeploymentView{ID: uuid.New(), ServiceID: id}, nil
}

func (f *fakeDeployer) snapshot() []depCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]depCall(nil), f.calls...)
}

type call struct {
	Method, Path, Query, Auth string
	Body                      string
}

// fakeProvider is an httptest provider API recording every request. Routes are
// matched by "METHOD path" (query ignored).
type fakeProvider struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []call
	routes map[string]func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{routes: map[string]func(http.ResponseWriter, *http.Request, []byte){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: string(body)})
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		h(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) on(route string, h func(w http.ResponseWriter, r *http.Request, body []byte)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

func (f *fakeProvider) json(route string, status int, v any) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	})
}

func (f *fakeProvider) callsTo(method, path string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// ---- fixture ----

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *db.Queries
	box      *secretbox.Box
	ctx      context.Context
	reader   context.Context
	svc      *gitproviders.Service
	svcs     *services.Service
	dep      *fakeDeployer
	env      db.Environment
	project  db.Project
	server   db.Server
	ws       db.Workspace
	hooks    *httptest.Server
	provider *fakeProvider
}

func newFixture(t *testing.T, opts ...func(*gitproviders.Deps)) *fixture {
	t.Helper()
	// The fake providers listen on loopback, which the control plane's HTTP
	// client refuses unless the instance admin allows it.
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nopDispatcher{}, logger)
	dep := &fakeDeployer{}
	provider := newFakeProvider(t)
	deps := gitproviders.Deps{
		Pool: pool, Bus: bus, Box: box, Config: &config.Config{PublicURL: "https://nebula.example.com"},
		Services: svcs, Deployer: dep, Logger: logger,
	}
	for _, o := range opts {
		o(&deps)
	}
	gp := gitproviders.NewService(deps)
	r := chi.NewRouter()
	gp.Mount(r)
	hooks := httptest.NewServer(r)
	t.Cleanup(hooks.Close)
	p := func(scopes []string) context.Context {
		return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "alice@example.com", Scopes: scopes})
	}
	return &fixture{
		t: t, pool: pool, q: q, box: box, ctx: p(policy.AllScopes), reader: p([]string{policy.ScopeProjectsRead}),
		svc: gp, svcs: svcs, dep: dep, env: env, project: prj, server: srv, ws: ws, hooks: hooks, provider: provider,
	}
}

// giteaConnection connects a Gitea instance (the fake provider) with a token.
func (f *fixture) giteaConnection() gitproviders.CreateConnectionResult {
	f.t.Helper()
	f.provider.json("GET /api/v1/user", 200, map[string]string{"login": "alice"})
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{
		Provider: gitproviders.Gitea, Name: "My Gitea", BaseURL: f.provider.srv.URL, AuthType: gitproviders.AuthToken, Token: "gta_secret_token_123456",
	})
	require.NoError(f.t, err)
	return res
}

// repo makes the fake Gitea know acme/shop and its webhooks.
func (f *fixture) giteaRepo(branch string) {
	f.provider.json("GET /api/v1/repos/acme/shop", 200, map[string]any{
		"full_name": "acme/shop", "clone_url": "https://git.example.com/acme/shop.git", "default_branch": branch, "private": true,
	})
	f.provider.json("GET /api/v1/repos/acme/shop/hooks", 200, []any{})
	f.provider.json("POST /api/v1/repos/acme/shop/hooks", 201, map[string]any{"id": 7})
	f.provider.on("DELETE /api/v1/repos/acme/shop/hooks/7", func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(204) })
}

func (f *fixture) gitService(name string) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, f.env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 8080},
	})
	require.NoError(f.t, err)
	return v
}

func (f *fixture) link(svc uuid.UUID, conn uuid.UUID, in gitproviders.LinkInput) gitproviders.GitLinkView {
	f.t.Helper()
	in.ConnectionID, in.Repo = conn, "acme/shop"
	v, err := f.svc.Link(f.ctx, svc, in)
	require.NoError(f.t, err)
	return v
}

func (f *fixture) post(provider string, conn uuid.UUID, headers map[string]string, body []byte) (int, map[string]string) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.hooks.URL+"/hooks/git/"+provider+"/"+conn.String(), bytes.NewReader(body))
	require.NoError(f.t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(f.t, err)
	defer resp.Body.Close()
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

func giteaHeaders(secret string, body []byte, delivery string) map[string]string {
	return map[string]string{
		"X-Gitea-Event": "push", "X-Gitea-Delivery": delivery,
		"X-Gitea-Signature": gitproviders.SignForTest(secret, body),
	}
}

// ---- tests ----

func TestConnectionViewNeverContainsSecrets(t *testing.T) {
	f := newFixture(t)
	res := f.giteaConnection()
	require.NotEmpty(t, res.WebhookSecret, "the secret is shown once, at creation")
	list, err := f.svc.ListConnections(f.reader)
	require.NoError(t, err)
	require.Len(t, list, 1)
	raw, _ := json.Marshal(list)
	require.NotContains(t, string(raw), "gta_secret_token_123456")
	require.NotContains(t, string(raw), res.WebhookSecret)
	require.Equal(t, "alice", list[0].AccountLogin, "the token was verified against the provider")
	require.Equal(t, "https://nebula.example.com/hooks/git/gitea/"+res.Connection.ID.String(), list[0].WebhookURL)

	var stored string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT credentials_enc::text FROM git_connections").Scan(&stored))
	require.NotContains(t, stored, "gta_secret_token_123456", "credentials are sealed at rest")

	// The tokens reach the provider as the Authorization header.
	require.Equal(t, "token gta_secret_token_123456", f.provider.callsTo("GET", "/api/v1/user")[0].Auth)

	// Reading is allowed with projects:read, changing is not.
	_, err = f.svc.CreateConnection(f.reader, gitproviders.CreateConnectionInput{Provider: gitproviders.Gitea, BaseURL: "https://x.example.com", AuthType: "token", Token: "t"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
}

func TestCreateConnectionValidation(t *testing.T) {
	f := newFixture(t)
	f.provider.json("GET /api/v1/user", 401, map[string]string{"message": "bad token"})
	_, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Gitea, BaseURL: f.provider.srv.URL, AuthType: "token", Token: "nope-nope-nope"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "credentials_invalid", ae.Code)
	require.NotContains(t, ae.Message, "nope-nope-nope")

	_, err = f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Gitea, AuthType: "token", Token: "t"})
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Message, "baseUrl is required", "self-hosted providers always need a base URL")

	_, err = f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.GitHub})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "use_github_app", ae.Code)

	_, err = f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Gitea, BaseURL: "ftp://x", AuthType: "token", Token: "t"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "validation_failed", ae.Code)

	bb, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "Bitbucket"})
	require.NoError(t, err)
	require.Equal(t, gitproviders.AuthWebhookOnly, bb.Connection.AuthType)
	require.True(t, bb.Connection.ManualWebhook)
	require.False(t, bb.Connection.CanListRepos)
	require.NotEmpty(t, bb.WebhookSecret)
	_, err = f.svc.ListRepos(f.ctx, bb.Connection.ID, "", 10)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "no_repo_api", ae.Code)
}

func TestLinkRegistersOneWebhookAndUnlinkRemovesIt(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("trunk")
	a, b := f.gitService("api"), f.gitService("web")

	v := f.link(a.ID, conn.Connection.ID, gitproviders.LinkInput{Branch: "main"})
	require.True(t, v.Linked)
	require.Equal(t, "registered", v.Webhook.Mode)
	require.Equal(t, "active", v.Webhook.Status, v.Webhook.Message)
	require.True(t, v.AutoDeploy, "auto-deploy defaults to on")
	f.link(b.ID, conn.Connection.ID, gitproviders.LinkInput{})

	posts := f.provider.callsTo("POST", "/api/v1/repos/acme/shop/hooks")
	require.Len(t, posts, 1, "two services of one repo share one webhook")
	var body struct {
		Type   string            `json:"type"`
		Events []string          `json:"events"`
		Config map[string]string `json:"config"`
	}
	require.NoError(t, json.Unmarshal([]byte(posts[0].Body), &body))
	require.Equal(t, "gitea", body.Type)
	require.ElementsMatch(t, []string{"push", "pull_request"}, body.Events)
	require.Equal(t, "https://nebula.example.com/hooks/git/gitea/"+conn.Connection.ID.String(), body.Config["url"])
	require.Equal(t, conn.WebhookSecret, body.Config["secret"], "the hook is signed with the connection's secret")
	require.Equal(t, "json", body.Config["content_type"])

	// The service now builds the repository's clone URL on the right branch.
	got, err := f.svcs.Get(f.ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, "https://git.example.com/acme/shop.git", got.Config.GitURL)
	require.Equal(t, "main", got.Config.GitBranch, "the existing branch of the same repository is kept")
	require.NotNil(t, got.Git)
	require.Equal(t, "acme/shop", got.Git.Repo)
	require.Equal(t, "My Gitea", got.Git.ConnectionName)

	// A fresh service picks the repository's default branch.
	c := f.gitService("worker")
	c.Config.GitURL = "https://git.example.com/other/repo.git"
	f.link(c.ID, conn.Connection.ID, gitproviders.LinkInput{})
	got, _ = f.svcs.Get(f.ctx, c.ID)
	require.Equal(t, "main", got.Config.GitBranch)

	require.NoError(t, f.svc.Unlink(f.ctx, a.ID))
	require.Empty(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), "other services still use the repository")
	require.NoError(t, f.svc.Unlink(f.ctx, b.ID))
	require.NoError(t, f.svc.Unlink(f.ctx, c.ID))
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1, "the last unlink removes the webhook")
	v, err = f.svc.GetLink(f.ctx, a.ID)
	require.NoError(t, err)
	require.False(t, v.Linked)
}

func TestLinkRejectsUnknownRepoAndNonGitServices(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.provider.json("GET /api/v1/repos/acme/missing", 404, map[string]string{"message": "not found"})
	a := f.gitService("api")
	_, err := f.svc.Link(f.ctx, a.ID, gitproviders.LinkInput{ConnectionID: conn.Connection.ID, Repo: "acme/missing"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "repo_not_found", ae.Code)

	img, err := f.svcs.Create(f.ctx, f.env.ID, services.CreateInput{Name: "redis", SourceType: services.SourceImage, Config: services.Config{Image: "redis:7"}})
	require.NoError(t, err)
	_, err = f.svc.Link(f.ctx, img.ID, gitproviders.LinkInput{ConnectionID: conn.Connection.ID, Repo: "acme/shop"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "not_applicable", ae.Code)

	_, err = f.svc.Link(f.ctx, a.ID, gitproviders.LinkInput{ConnectionID: conn.Connection.ID, Repo: "no-slash"})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "validation_failed", ae.Code)
}

func TestPushDeploysMatchingServicesWithWatchPaths(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	billing, web, all, off, dev := f.gitService("billing"), f.gitService("web"), f.gitService("all"), f.gitService("off"), f.gitService("dev")
	paths := func(p ...string) *[]string { return &p }
	no := false
	f.link(billing.ID, conn.Connection.ID, gitproviders.LinkInput{WatchPaths: paths("services/billing/**")})
	f.link(web.ID, conn.Connection.ID, gitproviders.LinkInput{WatchPaths: paths("services/web/**", "!**/*.md")})
	f.link(all.ID, conn.Connection.ID, gitproviders.LinkInput{})
	f.link(off.ID, conn.Connection.ID, gitproviders.LinkInput{AutoDeploy: &no})
	f.link(dev.ID, conn.Connection.ID, gitproviders.LinkInput{Branch: "develop"})

	var pushes []gitproviders.PushEvent
	var mu sync.Mutex
	f.svc.SubscribePush(func(_ context.Context, ev gitproviders.PushEvent) {
		mu.Lock()
		pushes = append(pushes, ev)
		mu.Unlock()
	})

	body := readTestdata(t, "gitea_push.json")
	code, reply := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "d-1"), body)
	require.Equal(t, 200, code)
	require.Equal(t, "processed", reply["status"])
	f.svc.Wait()

	calls := f.dep.snapshot()
	deployed := map[uuid.UUID]depCall{}
	for _, c := range calls {
		deployed[c.ServiceID] = c
	}
	require.Len(t, calls, 2, "billing (its path changed) and all (no watch paths); not web, off or dev: %+v", calls)
	require.Contains(t, deployed, billing.ID)
	require.Contains(t, deployed, all.ID)
	c := deployed[billing.ID]
	require.Equal(t, deployments.TriggerGitPush, c.Trigger)
	require.Equal(t, deployments.CommitInfo{
		SHA: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", Message: "Add services folder", Author: "Ada Lovelace", Branch: "main",
	}, c.Commit)

	mu.Lock()
	require.Len(t, pushes, 1, "subscribers get the verified push")
	require.Equal(t, conn.Connection.ID, pushes[0].ConnectionID)
	require.Equal(t, gitproviders.Gitea, pushes[0].Provider)
	require.Equal(t, "acme/shop", pushes[0].RepoFullName)
	mu.Unlock()

	// The delivery log explains every decision and carries no signature.
	items, _, err := f.svc.ListDeliveries(f.ctx, conn.Connection.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "processed", items[0].Outcome)
	require.Equal(t, "push", items[0].Event)
	require.Equal(t, "d-1", items[0].DeliveryID)
	require.Equal(t, "acme/shop", items[0].Repo)
	require.Equal(t, "main", items[0].Branch)
	require.Contains(t, items[0].Summary, "billing")
	require.Contains(t, items[0].Summary, "watch paths")
	require.Contains(t, items[0].Summary, "auto-deploy is off")
	require.Equal(t, "[redacted]", items[0].Headers["X-Gitea-Signature"])
	require.Len(t, items[0].Deployments, 2)
}

func TestWebhookRejectsTamperedUnsignedAndReplayed(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	body := readTestdata(t, "gitea_push.json")

	tampered := bytes.Replace(body, []byte("Add services folder"), []byte("Add a backdoor"), 1)
	code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "t-1"), tampered)
	require.Equal(t, 401, code, "a tampered payload fails verification")

	code, _ = f.post("gitea", conn.Connection.ID, map[string]string{"X-Gitea-Event": "push", "X-Gitea-Delivery": "t-2"}, body)
	require.Equal(t, 401, code, "an unsigned delivery is rejected")

	code, _ = f.post("gitea", conn.Connection.ID, giteaHeaders("wrong-secret", body, "t-3"), body)
	require.Equal(t, 401, code)
	require.Empty(t, f.dep.snapshot(), "nothing deployed for rejected deliveries")

	code, _ = f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "ok-1"), body)
	require.Equal(t, 200, code)
	require.Len(t, f.dep.snapshot(), 1)

	code, reply := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "ok-1"), body)
	require.Equal(t, 409, code, "the same delivery id is a replay")
	require.Equal(t, "replayed", reply["status"])
	require.Len(t, f.dep.snapshot(), 1, "a replay deploys nothing")

	// The providers sign the body only: a captured payload sent again under a
	// new delivery id is still a replay (it would roll the service back).
	code, reply = f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "ok-2"), body)
	require.Equal(t, 409, code, "the same payload under a new delivery id is a replay")
	require.Equal(t, "replayed", reply["status"])
	require.Len(t, f.dep.snapshot(), 1)

	// Replay protection expires after 7 days.
	_, err := f.pool.Exec(context.Background(), "UPDATE git_delivery_ids SET seen_at = now() - interval '8 days'")
	require.NoError(t, err)
	require.NoError(t, f.svc.Maintain(context.Background()))
	code, _ = f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "ok-1"), body)
	require.Equal(t, 200, code, "the id and the payload were forgotten after the TTL")

	// Unknown connection / provider mismatch look like nothing is there.
	code, _ = f.post("gitea", uuid.New(), giteaHeaders(conn.WebhookSecret, body, "x"), body)
	require.Equal(t, 404, code)
	code, _ = f.post("gitlab", conn.Connection.ID, map[string]string{"X-Gitlab-Token": conn.WebhookSecret}, body)
	require.Equal(t, 404, code)

	items, _, err := f.svc.ListDeliveries(f.ctx, conn.Connection.ID, "", 50)
	require.NoError(t, err)
	outcomes := map[string]int{}
	for _, it := range items {
		outcomes[it.Outcome]++
		raw, _ := json.Marshal(it)
		require.NotContains(t, string(raw), conn.WebhookSecret)
		require.NotContains(t, string(raw), gitproviders.SignForTest(conn.WebhookSecret, body), "signatures are redacted")
	}
	require.Equal(t, 3, outcomes["rejected"])
	require.Equal(t, 2, outcomes["replayed"])
	require.Equal(t, 2, outcomes["processed"])
}

func TestPushDuringDeploymentIsQueuedAndDeployedNext(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	body := readTestdata(t, "gitea_push.json")

	f.dep.fail = func(n int, _ depCall) error {
		if n == 0 {
			return apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
		}
		return nil
	}
	code, reply := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "q-1"), body)
	require.Equal(t, 200, code)
	require.Contains(t, reply["summary"], "queued to deploy next")
	require.Empty(t, f.dep.snapshot())
	v, err := f.svc.GetLink(f.ctx, svc.ID)
	require.NoError(t, err)
	require.Equal(t, "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", v.QueuedCommit)

	// While a deployment is still active nothing is deployed.
	dep, err := f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{ServiceID: svc.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(context.Background()))
	require.Empty(t, f.dep.snapshot())

	_, err = f.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(context.Background()))
	calls := f.dep.snapshot()
	require.Len(t, calls, 1, "the queued push deploys once the running deployment ended")
	require.Equal(t, "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", calls[0].Commit.SHA)
	require.Equal(t, deployments.TriggerGitPush, calls[0].Trigger)

	require.NoError(t, f.svc.Sweep(context.Background()))
	require.Len(t, f.dep.snapshot(), 1, "and only once")
}

func TestSkipCiCommitsDoNotDeploy(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	f.link(f.gitService("api").ID, conn.Connection.ID, gitproviders.LinkInput{})
	body := bytes.Replace(readTestdata(t, "gitea_push.json"), []byte("Add services folder"), []byte("docs only [skip ci]"), -1)
	code, reply := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, "s-1"), body)
	require.Equal(t, 200, code)
	require.Contains(t, reply["summary"], "[skip ci]")
	require.Empty(t, f.dep.snapshot())
}

func TestBitbucketPushDeploysEverythingBecauseNoFileList(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "Bitbucket"})
	require.NoError(t, err)
	svc := f.gitService("api")
	v, err := f.svc.Link(f.ctx, svc.ID, gitproviders.LinkInput{ConnectionID: res.Connection.ID, Repo: "team/webapp", Branch: "main",
		WatchPaths: &[]string{"apps/never-matches/**"}})
	require.NoError(t, err)
	require.Equal(t, "manual", v.Webhook.Mode)
	got, _ := f.svcs.Get(f.ctx, svc.ID)
	require.Equal(t, "https://bitbucket.org/team/webapp.git", got.Config.GitURL)

	body := readTestdata(t, "bitbucket_push.json")
	sig := "sha256=" + gitproviders.SignForTest(res.WebhookSecret, body)
	code, _ := f.post("bitbucket", res.Connection.ID, map[string]string{"X-Event-Key": "repo:push", "X-Request-UUID": "bb-1", "X-Hub-Signature": sig}, body)
	require.Equal(t, 200, code)
	calls := f.dep.snapshot()
	require.Len(t, calls, 1, "Bitbucket reports no changed files, so watch paths cannot exclude the push")
	require.Equal(t, "709d658dc5b6d6afcd46049c2f332ee3f515a67d", calls[0].Commit.SHA)
	code, _ = f.post("bitbucket", res.Connection.ID, map[string]string{"X-Event-Key": "repo:push", "X-Request-UUID": "bb-2", "X-Hub-Signature": "sha256=00"}, body)
	require.Equal(t, 401, code)
}

func TestGitLabWebhookAndPullRequestSubscribers(t *testing.T) {
	f := newFixture(t)
	f.provider.json("GET /api/v4/user", 200, map[string]string{"username": "alice"})
	res, err := f.svc.CreateConnection(f.ctx, gitproviders.CreateConnectionInput{Provider: gitproviders.GitLab, BaseURL: f.provider.srv.URL, AuthType: "token", Token: "glpat-abcdefghij"})
	require.NoError(t, err)
	require.Equal(t, "Bearer glpat-abcdefghij", f.provider.callsTo("GET", "/api/v4/user")[0].Auth)

	var got gitproviders.PullRequestEvent
	done := make(chan struct{})
	f.svc.SubscribePullRequest(func(_ context.Context, ev gitproviders.PullRequestEvent) { got = ev; close(done) })

	body := readTestdata(t, "gitlab_merge_request.json")
	code, _ := f.post("gitlab", res.Connection.ID, map[string]string{"X-Gitlab-Token": "wrong", "X-Gitlab-Event": "Merge Request Hook", "Idempotency-Key": "m-1"}, body)
	require.Equal(t, 401, code)
	code, _ = f.post("gitlab", res.Connection.ID, map[string]string{"X-Gitlab-Token": res.WebhookSecret, "X-Gitlab-Event": "Merge Request Hook", "Idempotency-Key": "m-2"}, body)
	require.Equal(t, 200, code)
	<-done
	require.Equal(t, res.Connection.ID, got.ConnectionID)
	require.Equal(t, gitproviders.GitLab, got.Provider)
	require.Equal(t, 7, got.Number)
	require.Equal(t, gitproviders.PRsynchronize, got.Action)
	require.True(t, got.FromFork)
	require.Empty(t, f.dep.snapshot(), "pull requests never trigger the auto-deploy of base services")
}

func TestCommitStatusReportedOncePerStateWithDeploymentLink(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	f.provider.json("POST /api/v1/repos/acme/shop/statuses/bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", 201, map[string]string{})
	statuses := func() []map[string]string {
		var out []map[string]string
		for _, c := range f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/bffeb74224043ba2feb48e12f2ac4a0a9de3e04c") {
			var m map[string]string
			require.NoError(t, json.Unmarshal([]byte(c.Body), &m))
			out = append(out, m)
		}
		return out
	}

	bg := context.Background()
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: svc.ID, ServerID: &f.server.ID, Trigger: "git_push", ActorType: "system", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, f.q.SetDeploymentCommit(bg, db.SetDeploymentCommitParams{ID: dep.ID, CommitSha: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c", CommitBranch: "main"}))

	require.NoError(t, f.svc.Sweep(bg))
	require.NoError(t, f.svc.Sweep(bg))
	got := statuses()
	require.Len(t, got, 1, "pending is reported once")
	require.Equal(t, "pending", got[0]["state"])
	require.Equal(t, "nebulactrl/api", got[0]["context"])
	require.Contains(t, got[0]["target_url"], "https://nebula.example.com/p/"+f.project.ID.String()+"/production?")
	require.Contains(t, got[0]["target_url"], dep.ID.String(), "the status links to the deployment")
	require.Equal(t, "token gta_secret_token_123456", f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/bffeb74224043ba2feb48e12f2ac4a0a9de3e04c")[0].Auth)

	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'building' WHERE id = $1", dep.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Len(t, statuses(), 1, "still pending: not repeated")

	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.NoError(t, f.svc.Sweep(bg))
	got = statuses()
	require.Len(t, got, 2)
	require.Equal(t, "success", got[1]["state"])
	require.Equal(t, "Deployed", got[1]["description"])

	// A failed deployment reports failure with the reason.
	dep2, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: svc.ID, ServerID: &f.server.ID, Trigger: "git_push", ActorType: "system", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, f.q.SetDeploymentCommit(bg, db.SetDeploymentCommitParams{ID: dep2.ID, CommitSha: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"}))
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'failed', status_message = 'health check failed' WHERE id = $1", dep2.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	got = statuses()
	require.Equal(t, "failure", got[len(got)-1]["state"])
	require.Equal(t, "health check failed", got[len(got)-1]["description"])

	// Reconcile deployments redeploy an old release: no status for them.
	dep3, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: svc.ID, ServerID: &f.server.ID, Trigger: "reconcile", ActorType: "system", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, f.q.SetDeploymentCommit(bg, db.SetDeploymentCommitParams{ID: dep3.ID, CommitSha: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"}))
	n := len(statuses())
	require.NoError(t, f.svc.Sweep(bg))
	require.Len(t, statuses(), n)
}

func TestCommitStatusFailureIsRetriedAndBounded(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})
	sha := "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c"
	f.provider.json("POST /api/v1/repos/acme/shop/statuses/"+sha, 500, map[string]string{"message": "boom"})
	bg := context.Background()
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: svc.ID, ServerID: &f.server.ID, Trigger: "git_push", ActorType: "system", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, f.q.SetDeploymentCommit(bg, db.SetDeploymentCommitParams{ID: dep.ID, CommitSha: sha}))

	require.NoError(t, f.svc.Sweep(bg), "a provider outage never fails the sweep")
	require.Len(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha), 1)
	require.NoError(t, f.svc.Sweep(bg))
	require.Len(t, f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha), 1, "retries back off for a minute")

	_, err = f.pool.Exec(bg, "UPDATE git_status_reports SET reported_at = now() - interval '2 minutes'")
	require.NoError(t, err)
	f.provider.json("POST /api/v1/repos/acme/shop/statuses/"+sha, 201, map[string]string{})
	require.NoError(t, f.svc.Sweep(bg))
	calls := f.provider.callsTo("POST", "/api/v1/repos/acme/shop/statuses/"+sha)
	require.Len(t, calls, 2)
	var state string
	require.NoError(t, f.pool.QueryRow(bg, "SELECT state FROM git_status_reports WHERE deployment_id = $1", dep.ID).Scan(&state))
	require.Equal(t, "pending", state)
}

func TestDeleteConnectionInUseNeedsForce(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	svc := f.gitService("api")
	f.link(svc.ID, conn.Connection.ID, gitproviders.LinkInput{})

	err := f.svc.DeleteConnection(f.ctx, conn.Connection.ID, false)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "connection_in_use", ae.Code)

	require.NoError(t, f.svc.DeleteConnection(f.ctx, conn.Connection.ID, true))
	require.Len(t, f.provider.callsTo("DELETE", "/api/v1/repos/acme/shop/hooks/7"), 1, "registered webhooks are removed")
	v, err := f.svc.GetLink(f.ctx, svc.ID)
	require.NoError(t, err)
	require.False(t, v.Linked, "the services were unlinked")
	list, _ := f.svc.ListConnections(f.ctx)
	require.Empty(t, list)
	code, _ := f.post("gitea", conn.Connection.ID, map[string]string{}, []byte(`{}`))
	require.Equal(t, 404, code)
}

func TestServicesForRepoAndCopy(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	a, b := f.gitService("api"), f.gitService("clone")
	f.link(a.ID, conn.Connection.ID, gitproviders.LinkInput{})
	ids, err := f.svc.ServicesForRepo(context.Background(), conn.Connection.ID, "ACME/Shop")
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{a.ID}, ids, "the repository is matched case-insensitively")

	require.NoError(t, db.InTx(context.Background(), f.pool, func(tx pgx.Tx) error {
		return f.svc.CopyServiceGitTx(context.Background(), tx, a.ID, b.ID)
	}))
	v, err := f.svc.GetLink(f.ctx, b.ID)
	require.NoError(t, err)
	require.True(t, v.Linked)
	require.False(t, v.AutoDeploy, "preview clones do not auto-deploy")
	require.Equal(t, "acme/shop", v.Repo)
	ids, _ = f.svc.ServicesForRepo(context.Background(), conn.Connection.ID, "acme/shop")
	require.Len(t, ids, 2)
}

func TestReadScopeCannotLink(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	a := f.gitService("api")
	_, err := f.svc.Link(f.reader, a.ID, gitproviders.LinkInput{ConnectionID: conn.Connection.ID, Repo: "acme/shop"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.svc.ListRepos(f.reader, conn.Connection.ID, "", 10)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, _, err = f.svc.ListDeliveries(f.reader, conn.Connection.ID, "", 10)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
}

// The clone URL is a join of the connection's base URL and the repository: no
// API call, no credential. A read-only principal (the system actor behind a
// config check) may resolve it; a caller without a principal may not.
func TestCloneURLForNeedsOnlyReadScope(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()

	url, branch, err := f.svc.CloneURLFor(f.reader, conn.Connection.ID, "acme/shop")
	require.NoError(t, err, "a read-only principal can resolve a clone URL")
	require.Equal(t, f.provider.srv.URL+"/acme/shop.git", url)
	require.Empty(t, branch)

	_, _, err = f.svc.CloneURLFor(context.Background(), conn.Connection.ID, "acme/shop")
	require.Error(t, err, "no principal, no clone URL")
}

func TestRepoPickerListsAndFilters(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.provider.json("GET /api/v1/user/repos", 200, []map[string]any{
		{"full_name": "acme/shop", "clone_url": "https://git.example.com/acme/shop.git", "default_branch": "main", "private": true},
		{"full_name": "acme/docs", "clone_url": "https://git.example.com/acme/docs.git", "default_branch": "main", "description": "Shop documentation"},
		{"full_name": "alice/dotfiles", "clone_url": "https://git.example.com/alice/dotfiles.git", "default_branch": "master"},
	})
	repos, err := f.svc.ListRepos(f.ctx, conn.Connection.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, repos, 3)
	repos, err = f.svc.ListRepos(f.ctx, conn.Connection.ID, "SHOP", 100)
	require.NoError(t, err)
	require.Len(t, repos, 2, "name or description contains the query")
	f.provider.json("GET /api/v1/repos/acme/shop/branches", 200, []map[string]string{{"name": "main"}, {"name": "develop"}})
	branches, err := f.svc.ListBranches(f.ctx, conn.Connection.ID, "acme/shop")
	require.NoError(t, err)
	require.Equal(t, []string{"main", "develop"}, branches)
	require.Contains(t, f.provider.callsTo("GET", "/api/v1/user/repos")[0].Query, "limit=50")
}
