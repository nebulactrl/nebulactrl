package deployhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	"github.com/nebulactrl/nebulactrl/internal/deployhooks"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

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
	Opts      deployments.InternalOptions
	Actor     string
}

// fakeDeployer records deployments; busy makes it answer like a service with a
// running deployment.
type fakeDeployer struct {
	mu    sync.Mutex
	calls []depCall
	busy  bool
}

func (f *fakeDeployer) DeployWith(ctx context.Context, id uuid.UUID, trigger string, opts deployments.InternalOptions) (deployments.DeploymentView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return deployments.DeploymentView{}, apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
	}
	actor := ""
	if p, ok := auth.PrincipalFrom(ctx); ok {
		actor = p.Type + ":" + p.Label
	}
	f.calls = append(f.calls, depCall{ServiceID: id, Trigger: trigger, Opts: opts, Actor: actor})
	return deployments.DeploymentView{ID: uuid.New(), ServiceID: id, Trigger: trigger}, nil
}

func (f *fakeDeployer) snapshot() []depCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]depCall(nil), f.calls...)
}

func (f *fakeDeployer) setBusy(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy = b
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context // every scope
	reader  context.Context // projects:read only
	svc     *deployhooks.Service
	svcs    *services.Service
	dep     *fakeDeployer
	env     db.Environment
	server  db.Server
	ts      *httptest.Server
	project db.Project
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
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
	svcs := services.NewService(pool, events.NewBus(), box, nopDispatcher{}, logger)
	dep := &fakeDeployer{}
	hs := deployhooks.NewService(deployhooks.Deps{
		Pool: pool, Config: &config.Config{PublicURL: "https://nebula.example.com/"}, Deployer: dep, Logger: logger,
	})
	hs.SetLimit(time.Millisecond, 1000) // TestRateLimitPerHook restores the real limit
	r := chi.NewRouter()
	hs.Mount(r)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	p := func(scopes []string) context.Context {
		return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "alice@example.com", Scopes: scopes})
	}
	return &fixture{
		t: t, pool: pool, q: q, ctx: p(policy.AllScopes), reader: p([]string{policy.ScopeProjectsRead}),
		svc: hs, svcs: svcs, dep: dep, env: env, server: srv, ts: ts, project: prj,
	}
}

func (f *fixture) imageService(name, image string) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, f.env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceImage, ServerID: &f.server.ID, Config: services.Config{Image: image, Port: 8080},
	})
	require.NoError(f.t, err)
	return v
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

// enable turns a hook on and returns the secret from its one-time URL.
func (f *fixture) enable(id uuid.UUID) (secret string, v deployhooks.DeployHookView) {
	f.t.Helper()
	v, err := f.svc.SetEnabled(f.ctx, id, true)
	require.NoError(f.t, err)
	prefix := "https://nebula.example.com/hooks/deploy/" + id.String() + "/"
	require.True(f.t, strings.HasPrefix(v.URL, prefix), v.URL)
	return strings.TrimPrefix(v.URL, prefix), v
}

type reply struct {
	Status int
	Body   map[string]any
	Header http.Header
	Raw    string
}

func (f *fixture) post(id uuid.UUID, secret, query string, headers map[string]string, body string) reply {
	f.t.Helper()
	u := f.ts.URL + "/hooks/deploy/" + id.String() + "/" + secret
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(body))
	require.NoError(f.t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(f.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := reply{Status: resp.StatusCode, Header: resp.Header, Raw: string(raw)}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func dockerHubPush(repo, tag string) string {
	b, _ := json.Marshal(map[string]any{
		"push_data":  map[string]any{"pushed_at": 1417566161, "pusher": "ci", "tag": tag},
		"repository": map[string]any{"repo_name": repo, "namespace": strings.Split(repo, "/")[0], "name": strings.Split(repo, "/")[1]},
	})
	return string(b)
}

func ghcrPush(action, ns, name, tag string) string {
	b, _ := json.Marshal(map[string]any{
		"action": action,
		"package": map[string]any{
			"name": name, "namespace": ns, "package_type": "CONTAINER",
			"package_version": map[string]any{"container_metadata": map[string]any{"tag": map[string]any{"name": tag, "digest": "sha256:x"}}},
		},
	})
	return string(b)
}

func (f *fixture) auditActions(action string) []map[string]any {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT actor_type, actor_label, metadata FROM audit_events WHERE action = $1 ORDER BY id", action)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var typ, label string
		var raw []byte
		require.NoError(f.t, rows.Scan(&typ, &label, &raw))
		m := map[string]any{}
		require.NoError(f.t, json.Unmarshal(raw, &m))
		m["_actorType"], m["_actorLabel"] = typ, label
		out = append(out, m)
	}
	return out
}

// ---- tests ----

func TestWrongSecretAndUnknownServiceLookTheSame(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)

	wrong := f.post(sv.ID, secret+"x", "", nil, "")
	unknown := f.post(uuid.New(), secret, "", nil, "")
	garbage := f.post(sv.ID, "ndh_short", "", nil, "")
	require.Equal(t, http.StatusNotFound, wrong.Status)
	require.Equal(t, wrong.Raw, unknown.Raw, "a wrong secret does not reveal that the service exists")
	require.Equal(t, wrong.Raw, garbage.Raw)
	require.Equal(t, http.StatusNotFound, unknown.Status)
	require.Empty(t, f.dep.snapshot())

	// The right secret deploys.
	ok := f.post(sv.ID, secret, "", nil, "")
	require.Equal(t, http.StatusAccepted, ok.Status, ok.Raw)
	require.Equal(t, "deploying", ok.Body["status"])
	require.NotEmpty(t, ok.Body["deploymentId"])
}

func TestDisabledHookIs404(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	// Never enabled.
	require.Equal(t, http.StatusNotFound, f.post(sv.ID, "ndh_whatever", "", nil, "").Status)

	secret, _ := f.enable(sv.ID)
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, secret, "", nil, "").Status)

	off, err := f.svc.SetEnabled(f.ctx, sv.ID, false)
	require.NoError(t, err)
	require.False(t, off.Enabled)
	require.Equal(t, http.StatusNotFound, f.post(sv.ID, secret, "", nil, "").Status, "the old secret does not work on a disabled hook")

	// Enabling again gives a new secret; the old one stays dead.
	secret2, _ := f.enable(sv.ID)
	require.NotEqual(t, secret, secret2)
	require.Equal(t, http.StatusNotFound, f.post(sv.ID, secret, "", nil, "").Status)
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, secret2, "", nil, "").Status)
}

func TestRotationInvalidatesTheOldSecret(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	old, _ := f.enable(sv.ID)

	rot, err := f.svc.Rotate(f.ctx, sv.ID)
	require.NoError(t, err)
	fresh := strings.TrimPrefix(rot.URL, "https://nebula.example.com/hooks/deploy/"+sv.ID.String()+"/")
	require.NotEmpty(t, fresh)
	require.NotEqual(t, old, fresh)

	require.Equal(t, http.StatusNotFound, f.post(sv.ID, old, "", nil, "").Status)
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, fresh, "", nil, "").Status)

	// A disabled hook cannot be rotated.
	_, err = f.svc.SetEnabled(f.ctx, sv.ID, false)
	require.NoError(t, err)
	_, err = f.svc.Rotate(f.ctx, sv.ID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "deploy_hook_disabled", ae.Code)
}

func TestRateLimitPerHook(t *testing.T) {
	f := newFixture(t)
	a := f.imageService("api", "acme/api:v1")
	b := f.imageService("web", "acme/web:v1")
	sa, _ := f.enable(a.ID)
	sb, _ := f.enable(b.ID)
	f.svc.SetLimit(10*time.Second, 3)

	for i := range 3 {
		require.Equal(t, http.StatusAccepted, f.post(a.ID, sa, "", nil, "").Status, "burst of 3 (call %d)", i+1)
	}
	limited := f.post(a.ID, sa, "", nil, "")
	require.Equal(t, http.StatusTooManyRequests, limited.Status)
	require.Equal(t, "rate_limited", limited.Body["code"])
	require.NotEmpty(t, limited.Header.Get("Retry-After"))
	require.Len(t, f.dep.snapshot(), 3, "a limited call deploys nothing")

	// The bucket is per hook, and wrong secrets never drain it.
	require.Equal(t, http.StatusAccepted, f.post(b.ID, sb, "", nil, "").Status)
	require.Equal(t, http.StatusNotFound, f.post(a.ID, "ndh_wrong", "", nil, "").Status)
}

func TestDockerHubPushDeploysOnlyMatchingRepoAndTag(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:stable")
	secret, _ := f.enable(sv.ID)

	hit := f.post(sv.ID, secret, "", nil, dockerHubPush("acme/api", "stable"))
	require.Equal(t, http.StatusAccepted, hit.Status, hit.Raw)
	require.Equal(t, "deploying", hit.Body["status"])
	require.Equal(t, false, hit.Body["ignored"])
	calls := f.dep.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, deployments.TriggerDeployHook, calls[0].Trigger)
	require.Empty(t, calls[0].Opts.ImageRef, "a registry push deploys the configured image")
	require.Contains(t, calls[0].Opts.Message, "Docker Hub push docker.io/acme/api:stable")
	require.Equal(t, "system:Deploy hook", calls[0].Actor)

	for name, body := range map[string]string{
		"other tag":  dockerHubPush("acme/api", "canary"),
		"other repo": dockerHubPush("acme/other", "stable"),
	} {
		miss := f.post(sv.ID, secret, "", nil, body)
		require.Equal(t, http.StatusAccepted, miss.Status, name)
		require.Equal(t, "ignored", miss.Body["status"], name)
		require.Equal(t, true, miss.Body["ignored"], name)
		require.NotEmpty(t, miss.Body["reason"], name)
	}
	require.Len(t, f.dep.snapshot(), 1, "mismatches deploy nothing")
}

func TestGHCRPushDeploysOnlyMatchingRepoAndTag(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "ghcr.io/acme/api:v2")
	secret, _ := f.enable(sv.ID)
	hdr := map[string]string{"X-GitHub-Event": "package", "Content-Type": "application/json"}

	hit := f.post(sv.ID, secret, "", hdr, ghcrPush("published", "Acme", "api", "v2"))
	require.Equal(t, http.StatusAccepted, hit.Status, hit.Raw)
	require.Equal(t, "deploying", hit.Body["status"])
	require.Len(t, f.dep.snapshot(), 1)
	require.Contains(t, f.dep.snapshot()[0].Opts.Message, "GHCR push ghcr.io/acme/api:v2")

	miss := f.post(sv.ID, secret, "", hdr, ghcrPush("published", "acme", "api", "v3"))
	require.Equal(t, "ignored", miss.Body["status"])
	// The legacy registry_package event is understood too, and a ping is ignored.
	legacy := f.post(sv.ID, secret, "", map[string]string{"X-GitHub-Event": "registry_package"},
		strings.Replace(ghcrPush("updated", "acme", "api", "v2"), `"package"`, `"registry_package"`, 1))
	require.Equal(t, http.StatusAccepted, legacy.Status)
	require.Equal(t, "deploying", legacy.Body["status"])
	require.Len(t, f.dep.snapshot(), 2)

	// A GitHub ping (sent when the webhook is created) is acknowledged and ignored.
	other := f.imageService("web", "ghcr.io/acme/web:v1")
	so, _ := f.enable(other.ID)
	ping := f.post(other.ID, so, "", map[string]string{"X-GitHub-Event": "ping"}, `{"zen":"Keep it logically awesome."}`)
	require.Equal(t, "ignored", ping.Body["status"])
	require.Len(t, f.dep.snapshot(), 2)
}

func TestTagOverrideStaysInTheServiceRepository(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "ghcr.io/acme/api:v1")
	secret, _ := f.enable(sv.ID)

	ok := f.post(sv.ID, secret, "tag=v7", nil, "")
	require.Equal(t, http.StatusAccepted, ok.Status, ok.Raw)
	require.Equal(t, "ghcr.io/acme/api:v7", ok.Body["image"])
	require.Equal(t, "ghcr.io/acme/api:v7", f.dep.snapshot()[0].Opts.ImageRef)

	digest := "sha256:" + strings.Repeat("c", 64)
	ok = f.post(sv.ID, secret, "digest="+digest, nil, "")
	require.Equal(t, http.StatusAccepted, ok.Status, ok.Raw)
	require.Equal(t, "ghcr.io/acme/api@"+digest, f.dep.snapshot()[1].Opts.ImageRef)

	bad := f.post(sv.ID, secret, "tag=evil/image:v1", nil, "")
	require.Equal(t, http.StatusUnprocessableEntity, bad.Status)
	require.Equal(t, "image_outside_repository", bad.Body["code"])
	bad = f.post(sv.ID, secret, "tag=docker.io/acme/api:v1", nil, "")
	require.Equal(t, http.StatusUnprocessableEntity, bad.Status, "same path on another registry is another repository")
	require.Len(t, f.dep.snapshot(), 2, "nothing is deployed for a rejected reference")

	// The rejected call is still audited.
	var rejected int
	for _, m := range f.auditActions("deployment.hook") {
		if m["outcome"] == "rejected" {
			rejected++
		}
	}
	require.Equal(t, 2, rejected)

	// Tag and digest are for image services.
	git := f.gitService("site")
	gs, _ := f.enable(git.ID)
	bad = f.post(git.ID, gs, "tag=v1", nil, "")
	require.Equal(t, http.StatusUnprocessableEntity, bad.Status)
}

func TestGitServiceDeploysBranchHead(t *testing.T) {
	f := newFixture(t)
	git := f.gitService("site")
	secret, _ := f.enable(git.ID)
	res := f.post(git.ID, secret, "", nil, "")
	require.Equal(t, http.StatusAccepted, res.Status, res.Raw)
	require.Equal(t, "deploying", res.Body["status"])
	calls := f.dep.snapshot()
	require.Len(t, calls, 1)
	require.Nil(t, calls[0].Opts.Commit, "no commit pin: the deployment resolves the branch head")
	require.Empty(t, calls[0].Opts.ImageRef)

	// A registry push means nothing to a git service.
	ign := f.post(git.ID, secret, "", nil, dockerHubPush("acme/api", "latest"))
	require.Equal(t, "ignored", ign.Body["status"])
	require.Len(t, f.dep.snapshot(), 1)
}

func TestBusyServiceQueuesAndNewestWins(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	f.dep.setBusy(true)

	first := f.post(sv.ID, secret, "tag=v2", nil, "")
	require.Equal(t, http.StatusAccepted, first.Status, first.Raw)
	require.Equal(t, "queued", first.Body["status"])
	second := f.post(sv.ID, secret, "tag=v3", nil, "")
	require.Equal(t, "queued", second.Body["status"])
	require.Empty(t, f.dep.snapshot())

	v, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.True(t, v.Queued)

	// While the deployment runs the sweep waits for it.
	_, err = f.pool.Exec(context.Background(), `INSERT INTO deployments (service_id, server_id, trigger, actor_type, status, strategy, generation)
		VALUES ($1, $2, 'manual', 'user', 'building', 'rolling', 1)`, sv.ID, f.server.ID)
	require.NoError(t, err)
	f.dep.setBusy(false)
	require.NoError(t, f.svc.Sweep(context.Background()))
	require.Empty(t, f.dep.snapshot(), "the service is still deploying")

	_, err = f.pool.Exec(context.Background(), "UPDATE deployments SET status = 'succeeded' WHERE service_id = $1", sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(context.Background()))
	calls := f.dep.snapshot()
	require.Len(t, calls, 1, "one deployment for the queued calls")
	require.Equal(t, "acme/api:v3", calls[0].Opts.ImageRef, "the newest call wins")
	require.Equal(t, deployments.TriggerDeployHook, calls[0].Trigger)

	require.NoError(t, f.svc.Sweep(context.Background()))
	require.Len(t, f.dep.snapshot(), 1, "the queue is empty afterwards")
	v, err = f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Queued)
}

func TestQueuedCallOutsideRepositoryIsDropped(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)
	f.dep.setBusy(true)
	require.Equal(t, "queued", f.post(sv.ID, secret, "tag=v2", nil, "").Body["status"])
	f.dep.setBusy(false)
	// The image of the service changes while the call waits.
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{image}', '"other/app:v1"') WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(context.Background()))
	require.Empty(t, f.dep.snapshot())
}

func TestEveryCallIsAudited(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, _ := f.enable(sv.ID)

	require.Equal(t, http.StatusAccepted, f.post(sv.ID, secret, "", nil, "").Status)
	f.post(sv.ID, secret, "", nil, dockerHubPush("acme/other", "x"))
	rows := f.auditActions("deployment.hook")
	require.Len(t, rows, 2)
	require.Equal(t, "deploying", rows[0]["outcome"])
	require.Equal(t, "generic", rows[0]["source"])
	require.NotEmpty(t, rows[0]["deploymentId"])
	require.Equal(t, "system", rows[0]["_actorType"])
	require.Equal(t, "Deploy hook", rows[0]["_actorLabel"])
	require.Equal(t, "ignored", rows[1]["outcome"])
	require.Equal(t, "dockerhub", rows[1]["source"])

	// Management is audited too, without the secret.
	_, err := f.svc.Rotate(f.ctx, sv.ID)
	require.NoError(t, err)
	_, err = f.svc.SetEnabled(f.ctx, sv.ID, false)
	require.NoError(t, err)
	require.Len(t, f.auditActions("deploy_hook.enable"), 1)
	require.Len(t, f.auditActions("deploy_hook.rotate"), 1)
	require.Len(t, f.auditActions("deploy_hook.disable"), 1)

	var all string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT COALESCE(string_agg(metadata::text, ' '), '') FROM audit_events").Scan(&all))
	require.NotContains(t, all, secret)

	// The hook shows what the last call did.
	v, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
}

func TestSecretIsShownOnceAndStoredOnlyAsHash(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	secret, v := f.enable(sv.ID)
	require.True(t, strings.HasPrefix(secret, deployhooks.SecretPrefix))
	require.GreaterOrEqual(t, len(secret), len(deployhooks.SecretPrefix)+43, "at least 32 random bytes")
	require.True(t, v.Enabled)
	require.Equal(t, "https://nebula.example.com/hooks/deploy/"+sv.ID.String()+"/<secret>", v.URLPattern)

	// Nothing returned afterwards carries the secret.
	for name, ctx := range map[string]context.Context{"owner": f.ctx, "reader": f.reader} {
		got, err := f.svc.Get(ctx, sv.ID)
		require.NoError(t, err, name)
		raw, _ := json.Marshal(got)
		require.NotContains(t, string(raw), secret, name)
		require.Empty(t, got.URL, name)
		require.True(t, got.Enabled)
		require.NotNil(t, got.SecretSetAt)
	}
	// Enabling an enabled hook neither changes the secret nor returns a URL.
	again, err := f.svc.SetEnabled(f.ctx, sv.ID, true)
	require.NoError(t, err)
	require.Empty(t, again.URL)
	require.Equal(t, http.StatusAccepted, f.post(sv.ID, secret, "", nil, "").Status)

	// The table holds a SHA-256, not the secret.
	var row string
	var hashLen int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT d::text, octet_length(secret_hash) FROM deploy_hooks d WHERE service_id = $1", sv.ID).Scan(&row, &hashLen))
	require.Equal(t, 32, hashLen)
	require.NotContains(t, row, secret)
	var anywhere string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT (SELECT COALESCE(string_agg(x::text, ' '), '') FROM services x)
		|| (SELECT COALESCE(string_agg(x::text, ' '), '') FROM deployments x)`).Scan(&anywhere))
	require.NotContains(t, anywhere, secret)

	// Disabling deletes the hash.
	_, err = f.svc.SetEnabled(f.ctx, sv.ID, false)
	require.NoError(t, err)
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM deploy_hooks WHERE service_id = $1 AND secret_hash IS NOT NULL", sv.ID).Scan(&n))
	require.Zero(t, n)
}

func TestManagementNeedsDeploymentsWrite(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")

	var ae *apierr.Error
	_, err := f.svc.SetEnabled(f.reader, sv.ID, true)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.svc.Rotate(f.reader, sv.ID)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)

	// Reading is allowed with projects:read; another team's service does not exist.
	v, err := f.svc.Get(f.reader, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.True(t, v.IsProduction, "the UI warns for production")
	require.Equal(t, "acme/api:v1", v.Image)

	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: uuid.New(), Scopes: policy.AllScopes})
	_, err = f.svc.Get(other, sv.ID)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)

	// A token that has deployments:write may manage the hook.
	dev := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: uuid.New(), WorkspaceID: policy.WorkspaceID(f.ctx),
		Scopes: []string{policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite}})
	_, err = f.svc.SetEnabled(dev, sv.ID, true)
	require.NoError(t, err)
}

func TestHookNeedsPublicURL(t *testing.T) {
	f := newFixture(t)
	sv := f.imageService("api", "acme/api:v1")
	noURL := deployhooks.NewService(deployhooks.Deps{Pool: f.pool, Config: &config.Config{}, Deployer: f.dep})
	_, err := noURL.SetEnabled(f.ctx, sv.ID, true)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "public_url_required", ae.Code)
}
