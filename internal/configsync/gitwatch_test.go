package configsync_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ----

// orderLog records the order of calls across fakes.
type orderLog struct {
	mu    sync.Mutex
	items []string
}

func (l *orderLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, s)
}

func (l *orderLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
}

// fakePlanner stands in for the plan/apply service.
type fakePlanner struct {
	mu      sync.Mutex
	log     *orderLog
	result  func(req configsync.PlanRequest, applied bool) configsync.Plan
	applied bool

	plans     []configsync.PlanRequest
	planBy    []*auth.Principal
	applies   []configsync.ApplyRequest
	applyBy   []*auth.Principal
	planErr   error
	applyErr  error
	applyStat string
}

func differsPlan(req configsync.PlanRequest, applied bool) configsync.Plan {
	svc := configsync.PlanService{
		Slug: "web", Kind: "git", Action: configsync.ActionUpdate, State: configsync.StateFileChanged, Managed: true,
		Changes: []configsync.PlanChange{{
			Path: "services.web.port", Field: "config.port", Op: configsync.OpSet, Applies: configsync.AppliesStaged,
			Reason: configsync.ReasonFileChanged, From: "3000", To: "8080",
		}},
	}
	if applied {
		svc.Action, svc.State, svc.Changes = configsync.ActionUnchanged, configsync.StateInSync, []configsync.PlanChange{}
	}
	sum := sha256.Sum256([]byte(req.Input.File))
	return configsync.Plan{
		PlanID: "pl_" + hex.EncodeToString(sum[:8]), Schema: 1, Valid: true,
		Environment: configsync.PlanEnvironment{Slug: req.Environment}, Services: []configsync.PlanService{svc},
	}
}

func cleanPlan(req configsync.PlanRequest, _ bool) configsync.Plan {
	p := differsPlan(req, true)
	return p
}

func (p *fakePlanner) Plan(ctx context.Context, _ uuid.UUID, r configsync.PlanRequest) (configsync.Plan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log.add("plan")
	p.plans = append(p.plans, r)
	pr, _ := auth.PrincipalFrom(ctx)
	p.planBy = append(p.planBy, pr)
	if p.planErr != nil {
		return configsync.Plan{}, p.planErr
	}
	fn := p.result
	if fn == nil {
		fn = differsPlan
	}
	return fn(r, p.applied), nil
}

func (p *fakePlanner) Apply(ctx context.Context, _ uuid.UUID, r configsync.ApplyRequest) (configsync.ApplyResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log.add("apply")
	p.applies = append(p.applies, r)
	pr, _ := auth.PrincipalFrom(ctx)
	p.applyBy = append(p.applyBy, pr)
	if p.applyErr != nil {
		return configsync.ApplyResult{}, p.applyErr
	}
	p.applied = true
	status := p.applyStat
	if status == "" {
		status = configsync.StatusStaged
	}
	return configsync.ApplyResult{PlanID: r.PlanID, Status: status}, nil
}

func (p *fakePlanner) planCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.plans)
}

func (p *fakePlanner) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans, p.planBy, p.applies, p.applyBy, p.applied = nil, nil, nil, nil, false
}

// fakeRepos serves the files of one repository per ref ("main", or a commit).
type fakeRepos struct {
	mu      sync.Mutex
	files   map[string]map[string][]byte // ref -> path -> content
	opens   []string
	reads   []string // "ref path"
	noAPI   bool
	openErr error
	failRef map[string]error
}

func newFakeRepos() *fakeRepos {
	return &fakeRepos{files: map[string]map[string][]byte{}, failRef: map[string]error{}}
}

func (f *fakeRepos) put(ref, path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files[ref] == nil {
		f.files[ref] = map[string][]byte{}
	}
	f.files[ref][path] = []byte(content)
}

func (f *fakeRepos) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opens)
}

func (f *fakeRepos) OpenRepo(_ context.Context, _ uuid.UUID, _, ref string) (configsync.FileReader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, ref)
	switch {
	case f.noAPI:
		return nil, apierr.Unprocessable("no_repo_api", "This connection has no API access, so files cannot be read from it.")
	case f.openErr != nil:
		return nil, f.openErr
	}
	return &fakeReader{f: f, ref: ref}, nil
}

func (f *fakeRepos) OpenPublicRepo(ctx context.Context, _, ref string) (configsync.FileReader, error) {
	return f.OpenRepo(ctx, uuid.Nil, "", ref)
}

type fakeReader struct {
	f   *fakeRepos
	ref string
}

func (r *fakeReader) Get(_ context.Context, path string) ([]byte, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	r.f.reads = append(r.f.reads, r.ref+" "+path)
	if err := r.f.failRef[r.ref]; err != nil {
		return nil, err
	}
	if b, ok := r.f.files[r.ref][path]; ok {
		return b, nil
	}
	return nil, gitproviders.ErrFileNotFound
}

// fakeScanner is the agent fallback.
type fakeScanner struct {
	mu     sync.Mutex
	log    *orderLog
	calls  []scanCall
	files  map[string][]byte
	commit string
	err    error
}

type scanCall struct {
	service uuid.UUID
	branch  string
	paths   []string
}

func (s *fakeScanner) ScanConfigFiles(_ context.Context, sc services.Scope, branch string, paths []string) (agentprotocol.EnvScanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log.add("scan")
	s.calls = append(s.calls, scanCall{service: sc.Service.ID, branch: branch, paths: append([]string(nil), paths...)})
	if s.err != nil {
		return agentprotocol.EnvScanResult{}, s.err
	}
	out := map[string][]byte{}
	for _, p := range paths {
		if b, ok := s.files[p]; ok {
			out[p] = b
		}
	}
	return agentprotocol.EnvScanResult{Outcome: agentprotocol.OutcomeSucceeded, Commit: s.commit, ConfigFiles: out}, nil
}

// ---- fixture ----

type gitFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	bus     *events.Bus
	log     *orderLog
	ws      db.Workspace
	user    db.CreateUserRow
	project db.Project
	staging db.Environment
	prod    db.Environment
	server  db.Server
	conn    db.GitConnection
	svcs    *services.Service
	repos   *fakeRepos
	scan    *fakeScanner
	planner *fakePlanner
	gs      *configsync.GitSync
	admin   context.Context
	events  <-chan events.Event
}

type nopDispatcher struct{}

func (nopDispatcher) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (nopDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	user, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	project, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	staging, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: project.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	server, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", server.ID)
	require.NoError(t, err)
	conn, err := q.InsertGitConnection(bg, db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Provider: "gitea", Name: "Gitea", BaseUrl: "https://git.example.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)

	f := &gitFixture{
		t: t, pool: pool, q: q, bus: bus, log: &orderLog{}, ws: ws, user: user, project: project, staging: staging, prod: prod,
		server: server, conn: conn, svcs: services.NewService(pool, bus, box, nopDispatcher{}, logger), repos: newFakeRepos(),
		events: bus.Subscribe(ctx),
	}
	f.scan = &fakeScanner{log: f.log, files: map[string][]byte{}}
	f.planner = &fakePlanner{log: f.log}
	f.gs = configsync.NewGitSync(configsync.GitSyncDeps{
		Pool: pool, Bus: bus, Repos: f.repos, Agent: f.scan, Logger: logger, ScanDelay: time.Millisecond,
	})
	f.gs.SetPlanner(f.planner)
	f.admin = f.principal(auth.TypeUser, policy.AllScopes)
	t.Cleanup(f.gs.Wait)
	return f
}

func (f *gitFixture) principal(typ string, scopes []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: typ, UserID: f.user.ID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "owner@example.com", Scopes: scopes,
	})
}

// gitService creates a git service of acme/shop in env, linked to the connection.
func (f *gitFixture) gitService(env db.Environment, name, branch, buildContext string) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.admin, env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: branch, BuildContext: buildContext, Port: 8080},
	})
	require.NoError(f.t, err)
	_, err = f.q.UpsertServiceGit(context.Background(), db.UpsertServiceGitParams{
		ServiceID: v.ID, ConnectionID: f.conn.ID, RepoFullName: "acme/shop", AutoDeploy: true, WatchPaths: []string{},
	})
	require.NoError(f.t, err)
	return v
}

func (f *gitFixture) push(sha string, complete bool, changed ...string) gitproviders.PushEvent {
	return gitproviders.PushEvent{
		ConnectionID: f.conn.ID, Provider: gitproviders.Gitea, RepoFullName: "acme/shop", Branch: "main", CommitSHA: sha,
		PathsComplete: complete, ChangedPaths: changed,
	}
}

func (f *gitFixture) settings(env db.Environment) db.EnvironmentConfigSetting {
	f.t.Helper()
	row, err := f.q.GetEnvironmentConfigSettings(context.Background(), env.ID)
	require.NoError(f.t, err)
	return row
}

func (f *gitFixture) hasSettings(env db.Environment) bool {
	_, err := f.q.GetEnvironmentConfigSettings(context.Background(), env.ID)
	return err == nil
}

func (f *gitFixture) enableAutoApply(env db.Environment) {
	f.t.Helper()
	require.NoError(f.t, f.q.SetEnvironmentConfigAutoApply(context.Background(), db.SetEnvironmentConfigAutoApplyParams{
		EnvironmentID: env.ID, WorkspaceID: f.ws.ID, AutoApply: true,
	}))
}

// awaitEvent waits for a config.updated event of env.
func (f *gitFixture) awaitEvent(env db.Environment) {
	f.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e := <-f.events:
			if e.Type == services.EventConfigUpdated && e.EnvironmentID == env.ID.String() {
				require.Equal(f.t, f.project.ID.String(), e.ProjectID)
				return
			}
		case <-timeout:
			f.t.Fatal("no config.updated event")
		}
	}
}

func shaOf(c byte) string { return strings.Repeat(string(c), 40) }

func fileSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

const tomlV1 = "schema = 1\n"
const tomlV2 = "schema = 1\nproject = \"shop\"\n"

// ---- binding resolution ----

func TestResolveExplicitDerivedAndNone(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()

	b, err := f.gs.Resolve(ctx, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingNone, b.Mode, "no git service, no binding: nothing is read")
	require.Empty(t, b.Paths)

	web := f.gitService(f.staging, "web", "main", "")
	b, err = f.gs.Resolve(ctx, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingDerived, b.Mode)
	require.Equal(t, f.conn.ID, b.ConnectionID)
	require.Equal(t, "acme/shop", b.Repo)
	require.Equal(t, "main", b.Branch)
	require.Equal(t, []string{"nebula.toml"}, b.Paths)
	require.Equal(t, []uuid.UUID{web.ID}, b.GitServices)
	require.Equal(t, f.staging.ID, b.EnvironmentID)
	require.Equal(t, "staging", b.EnvSlug)
	require.False(t, b.IsProduction)

	// Services on different branches make the binding ambiguous: no nagging.
	f.gitService(f.staging, "worker", "develop", "")
	b, err = f.gs.Resolve(ctx, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingNone, b.Mode)

	// An explicit binding wins over what the services say.
	src, err := f.gs.Put(f.admin, f.staging.ID, configsync.UpdateSourceInput{
		ConnectionID: &f.conn.ID, Repo: new("acme/other"), Branch: new("release"), Path: new("deploy/nebula.toml"),
	})
	require.NoError(t, err)
	require.Equal(t, configsync.BindingExplicit, src.Mode)
	require.Equal(t, "acme/other", src.Repo)
	require.Equal(t, "release", src.Branch)
	require.Equal(t, "deploy/nebula.toml", src.Path)
	require.Equal(t, "deploy/nebula.toml", src.ResolvedPath)
	require.Equal(t, &f.conn.ID, src.ConnectionID)
	b, err = f.gs.Resolve(ctx, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingExplicit, b.Mode)
	require.Equal(t, []string{"deploy/nebula.toml"}, b.Paths, "an explicit path is the only candidate")

	// Clearing it falls back to what the services say (still ambiguous here).
	require.NoError(t, f.gs.Clear(f.admin, f.staging.ID))
	b, err = f.gs.Resolve(ctx, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingNone, b.Mode)
}

func TestPreviewEnvironmentsAreNeverBound(t *testing.T) {
	f := newGitFixture(t)
	var previewID uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO environments (project_id, name, slug, kind, parent_environment_id) VALUES ($1, 'pr-1', 'pr-1', 'preview', $2) RETURNING id`,
		f.project.ID, f.staging.ID).Scan(&previewID))
	svc, err := f.svcs.Create(f.admin, previewID, services.CreateInput{
		Name: "web", SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 80},
	})
	require.NoError(t, err)
	_, err = f.q.UpsertServiceGit(context.Background(), db.UpsertServiceGitParams{ServiceID: svc.ID, ConnectionID: f.conn.ID, RepoFullName: "acme/shop", WatchPaths: []string{}})
	require.NoError(t, err)
	b, err := f.gs.Resolve(context.Background(), previewID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingNone, b.Mode)

	_, err = f.gs.Put(f.admin, previewID, configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("main")})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
}

func TestPathRuleRootExplicitAndBuildContext(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	paths := func() []string {
		b, err := f.gs.Resolve(ctx, f.staging.ID)
		require.NoError(t, err)
		return b.Paths
	}

	f.gitService(f.staging, "web", "main", "apps/web")
	require.Equal(t, []string{"nebula.toml", "apps/web/nebula.toml"}, paths(), "root first, then the shared build context")

	f.gitService(f.staging, "api", "main", "apps/web")
	require.Equal(t, []string{"nebula.toml", "apps/web/nebula.toml"}, paths(), "services sharing a context have one context file")

	f.gitService(f.staging, "worker", "main", "apps/worker")
	require.Equal(t, []string{"nebula.toml"}, paths(), "services in different contexts have no single context file")

	// The root file wins when it exists; the context file is read otherwise.
	f2 := newGitFixture(t)
	f2.gitService(f2.staging, "web", "main", "apps/web")
	f2.repos.put(shaOf('a'), "apps/web/nebula.toml", tomlV1)
	f2.gs.BeforePush(context.Background(), f2.push(shaOf('a'), true, "apps/web/nebula.toml"))
	row := f2.settings(f2.staging)
	require.Equal(t, configsync.EnvDiffers, row.Status)
	require.Equal(t, "apps/web/nebula.toml", row.LastPath)
	require.Equal(t, "apps/web/nebula.toml", f2.planner.plans[0].Input.Source.Path)
	view, err := f2.gs.Get(f2.admin, f2.staging.ID)
	require.NoError(t, err)
	require.Equal(t, "apps/web/nebula.toml", view.ResolvedPath)
	require.Empty(t, view.Path, "the path was derived, not set")

	f2.repos.put(shaOf('b'), "nebula.toml", tomlV2)
	f2.repos.put(shaOf('b'), "apps/web/nebula.toml", tomlV1)
	f2.gs.BeforePush(context.Background(), f2.push(shaOf('b'), true, "nebula.toml"))
	require.Equal(t, "nebula.toml", f2.settings(f2.staging).LastPath)
	require.Equal(t, tomlV2, f2.planner.plans[1].Input.File)
}

func TestPublicRepositoryBindingIsDerivedFromTheCloneURL(t *testing.T) {
	f := newGitFixture(t)
	_, err := f.svcs.Create(f.admin, f.staging.ID, services.CreateInput{
		Name: "web", SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://github.com/acme/public.git", GitBranch: "main", Port: 80},
	})
	require.NoError(t, err)
	b, err := f.gs.Resolve(context.Background(), f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingDerived, b.Mode)
	require.Equal(t, uuid.Nil, b.ConnectionID)
	require.Equal(t, "https://github.com/acme/public.git", b.GitURL)
	require.Equal(t, "acme/public", b.Repo)

	// Only an on-demand check reads it: a push can never match (no connection).
	f.repos.put("main", "nebula.toml", tomlV1)
	view, err := f.gs.Check(f.admin, f.staging.ID, "")
	require.NoError(t, err)
	require.Equal(t, configsync.EnvDiffers, view.Status)
	require.Nil(t, view.ConnectionID)
}

// ---- reading on push ----

func TestPushReadsAtThePushedCommitAndStoresTheStatus(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	sha := shaOf('a')
	f.repos.put(sha, "nebula.toml", tomlV1)
	f.repos.put("main", "nebula.toml", tomlV2) // a newer head must not be what is read

	f.gs.BeforePush(context.Background(), f.push(sha, true, "nebula.toml"))

	require.Equal(t, []string{sha}, f.repos.opens, "read at the pushed commit, not at the branch head")
	require.Len(t, f.planner.plans, 1)
	req := f.planner.plans[0]
	require.Equal(t, "staging", req.Environment)
	require.Equal(t, tomlV1, req.Input.File)
	require.Equal(t, configsync.SourceInfo{Kind: configsync.SourceGit, Repo: "acme/shop", Branch: "main", Path: "nebula.toml", Commit: sha}, req.Input.Source)
	by := f.planner.planBy[0]
	require.Equal(t, auth.TypeSystem, by.Type)
	require.Equal(t, "nebula.toml @aaaaaaa", by.Label)
	require.True(t, policy.HasScope(by, policy.ScopeProjectsRead))
	require.False(t, policy.HasScope(by, policy.ScopeProjectsWrite), "a status check only plans")

	row := f.settings(f.staging)
	require.Equal(t, configsync.EnvDiffers, row.Status)
	require.Equal(t, "1 service differs from the file", row.StatusDetail)
	require.Equal(t, sha, row.LastCommitSha)
	require.Equal(t, fileSHA(tomlV1), row.LastFileSha256)
	require.Equal(t, "nebula.toml", row.LastPath)
	require.NotNil(t, row.CheckedAt)
	require.False(t, row.AutoApply)
	var lp configsync.LastPlan
	require.NoError(t, json.Unmarshal(row.LastPlan, &lp))
	require.Equal(t, 1, lp.Services["web"].Changes)
	require.Equal(t, configsync.StateFileChanged, lp.Services["web"].State)
	require.NotEmpty(t, lp.Generations, "the state the plan was computed from")
	require.NotContains(t, string(row.LastPlan), "8080", "no values are stored")
	f.awaitEvent(f.staging)

	view, err := f.gs.Get(f.admin, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingDerived, view.Mode)
	require.Equal(t, configsync.EnvDiffers, view.Status)
	require.Equal(t, sha, view.Commit)
	require.Equal(t, fileSHA(tomlV1), view.FileSHA256)
	require.Equal(t, []configsync.EnvironmentConfigService{{Slug: "web", State: configsync.StateFileChanged, Changes: 1}}, view.Services)
}

func TestPushMatchesExplicitBindingsAndSkipsOthers(t *testing.T) {
	f := newGitFixture(t)
	// production is bound explicitly (no service builds the repository there);
	// staging is derived; other (a different branch) is not touched.
	f.gitService(f.staging, "web", "main", "")
	_, err := f.gs.Put(f.admin, f.prod.ID, configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("Acme/Shop"), Branch: new("main")})
	require.NoError(t, err)
	other, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.project.ID, Name: "dev", Slug: "dev"})
	require.NoError(t, err)
	_, err = f.gs.Put(f.admin, other.ID, configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("develop")})
	require.NoError(t, err)
	f.gs.Wait()
	f.planner.reset()

	sha := shaOf('c')
	f.repos.put(sha, "nebula.toml", tomlV1)
	f.gs.BeforePush(context.Background(), f.push(sha, true, "nebula.toml"))

	var envs []string
	for _, p := range f.planner.plans {
		envs = append(envs, p.Environment)
	}
	require.ElementsMatch(t, []string{"staging", "production"}, envs)
	require.Equal(t, configsync.EnvDiffers, f.settings(f.prod).Status, "production gets the status and the review, like any environment")
	require.NotEqual(t, configsync.EnvDiffers, f.settings(other).Status, "another branch is not this push")

	// A deleted branch (all-zero commit) and a push of another repository read nothing.
	before := f.repos.openCount()
	f.gs.BeforePush(context.Background(), f.push(strings.Repeat("0", 40), false))
	elsewhere := f.push(sha, false)
	elsewhere.RepoFullName = "acme/elsewhere"
	f.gs.BeforePush(context.Background(), elsewhere)
	require.Equal(t, before, f.repos.openCount())
}

func TestPathsCompleteSkipsOnlyWhenSafe(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	ctx := context.Background()
	f.repos.put(shaOf('a'), "nebula.toml", tomlV1)
	f.repos.put(shaOf('b'), "nebula.toml", tomlV1)

	// Never checked: the cheap path does not apply even when the paths are complete.
	f.gs.BeforePush(ctx, f.push(shaOf('a'), true, "README.md"))
	require.Equal(t, 1, f.repos.openCount(), "unknown status reads")
	require.Equal(t, configsync.EnvDiffers, f.settings(f.staging).Status)

	// Checked, complete paths, none is the file: skipped.
	f.gs.BeforePush(ctx, f.push(shaOf('b'), true, "README.md", "src/app.go"))
	require.Equal(t, 1, f.repos.openCount(), "a push that touches neither the file nor an attachment is skipped")
	require.Equal(t, shaOf('a'), f.settings(f.staging).LastCommitSha)

	// The file itself in the changed paths: read.
	f.gs.BeforePush(ctx, f.push(shaOf('b'), true, "docs/x.md", "nebula.toml"))
	require.Equal(t, 2, f.repos.openCount())

	// Incomplete paths (Bitbucket, big pushes): read whatever they list.
	f.gs.BeforePush(ctx, f.push(shaOf('b'), false, "README.md"))
	require.Equal(t, 3, f.repos.openCount())
	f.gs.BeforePush(ctx, f.push(shaOf('b'), false))
	require.Equal(t, 4, f.repos.openCount())

	// A failed read must be retried by the next push, whatever it changed.
	f.repos.openErr = errors.New("provider unavailable")
	f.gs.BeforePush(ctx, f.push(shaOf('b'), false))
	require.Equal(t, configsync.EnvUnreachable, f.settings(f.staging).Status)
	f.repos.openErr = nil
	f.gs.BeforePush(ctx, f.push(shaOf('b'), true, "README.md"))
	require.Equal(t, configsync.EnvDiffers, f.settings(f.staging).Status, "unreachable is read again even when the push does not touch the file")
}

func TestPushTouchingAnAttachmentReadsAgain(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "apps/web")
	const file = "schema = 1\n[services.stack.stack]\ncompose_file = \"docker-compose.yml\"\n"
	f.repos.put(shaOf('a'), "apps/web/nebula.toml", file)
	f.repos.put(shaOf('a'), "apps/web/docker-compose.yml", "services: {}\n")
	f.repos.put(shaOf('b'), "apps/web/nebula.toml", file)
	f.repos.put(shaOf('b'), "apps/web/docker-compose.yml", "services: { web: {} }\n")
	ctx := context.Background()

	f.gs.BeforePush(ctx, f.push(shaOf('a'), true, "apps/web/nebula.toml"))
	require.Len(t, f.planner.plans, 1)
	require.Equal(t, map[string]string{"docker-compose.yml": "services: {}\n"}, f.planner.plans[0].Input.Attachments,
		"attachments are keyed as the file names them and read next to the file")

	f.gs.BeforePush(ctx, f.push(shaOf('b'), true, "apps/web/docker-compose.yml"))
	require.Len(t, f.planner.plans, 2, "the attachment changed, so the environment is planned again although nebula.toml is identical")
	require.Equal(t, "services: { web: {} }\n", f.planner.plans[1].Input.Attachments["docker-compose.yml"])
}

func TestIdenticalFileShortCircuitsUntilAServiceMoves(t *testing.T) {
	f := newGitFixture(t)
	web := f.gitService(f.staging, "web", "main", "")
	ctx := context.Background()
	for _, c := range []byte("abc") {
		f.repos.put(shaOf(c), "nebula.toml", tomlV1)
	}

	f.gs.BeforePush(ctx, f.push(shaOf('a'), true, "nebula.toml"))
	require.Equal(t, 1, f.planner.planCount())
	first := f.settings(f.staging)

	time.Sleep(5 * time.Millisecond)
	f.gs.BeforePush(ctx, f.push(shaOf('b'), true, "nebula.toml"))
	require.Equal(t, 1, f.planner.planCount(), "same file, nothing moved: only the time is stored")
	second := f.settings(f.staging)
	require.True(t, second.CheckedAt.After(*first.CheckedAt))
	require.Equal(t, shaOf('b'), second.LastCommitSha)
	require.Equal(t, first.LastPlan, second.LastPlan)
	require.Equal(t, first.Status, second.Status)

	// A dashboard edit moves the generation: the same file is planned again.
	_, err := f.pool.Exec(ctx, `UPDATE services SET generation = generation + 1 WHERE id = $1`, web.ID)
	require.NoError(t, err)
	f.gs.BeforePush(ctx, f.push(shaOf('c'), true, "nebula.toml"))
	require.Equal(t, 2, f.planner.planCount())

	// A different file is planned again.
	f.repos.put(shaOf('d'), "nebula.toml", tomlV2)
	f.gs.BeforePush(ctx, f.push(shaOf('d'), true, "nebula.toml"))
	require.Equal(t, 3, f.planner.planCount())
	require.Equal(t, fileSHA(tomlV2), f.settings(f.staging).LastFileSha256)
}

func TestACommitTheProviderDoesNotAcceptIsReadAtTheBranchHead(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	sha := shaOf('a')
	f.repos.failRef[sha] = errors.New("422 unprocessable ref")
	f.repos.put("main", "nebula.toml", tomlV1)

	f.gs.BeforePush(context.Background(), f.push(sha, true, "nebula.toml"))

	require.Equal(t, []string{sha, "main"}, f.repos.opens)
	row := f.settings(f.staging)
	require.Equal(t, configsync.EnvDiffers, row.Status)
	require.Contains(t, row.StatusDetail, "read at branch head")
	require.Empty(t, row.LastCommitSha, "the commit of a branch-head read through an API is not known")
}

func TestStatusTransitions(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	ctx := f.admin
	status := func(sha string) configsync.EnvironmentConfigSource {
		v, err := f.gs.Check(ctx, f.staging.ID, sha)
		require.NoError(t, err)
		return v
	}

	v := status("")
	require.Equal(t, configsync.EnvNotFound, v.Status, "no file in the repository")
	require.Contains(t, v.StatusDetail, "nebula.toml")
	require.Empty(t, v.Services)

	f.repos.put("main", "nebula.toml", tomlV1)
	f.planner.result = cleanPlan
	v = status("")
	require.Equal(t, configsync.EnvInSync, v.Status)
	require.Empty(t, v.StatusDetail)
	require.Equal(t, []configsync.EnvironmentConfigService{{Slug: "web", State: configsync.StateInSync}}, v.Services)

	f.planner.result = differsPlan2
	v = status("")
	require.Equal(t, configsync.EnvDiffers, v.Status)
	require.Equal(t, "2 services differ from the file", v.StatusDetail)

	f.planner.result = func(req configsync.PlanRequest, _ bool) configsync.Plan {
		p := differsPlan(req, false)
		p.Valid = false
		p.Diagnostics = configsync.Diagnostics{{Severity: projectconfig.SeverityWarning, Code: "secret_missing_value", Message: "no value yet"},
			{Severity: projectconfig.SeverityError, Code: "unknown_key", Message: `unknown key "healthcheck"`, Path: "services.web.healthcheck", Line: 19, Column: 1}}
		return p
	}
	v = status("")
	require.Equal(t, configsync.EnvInvalid, v.Status)
	require.Contains(t, v.StatusDetail, "nebula.toml:19:1", "the first error, with its line")
	require.Contains(t, v.StatusDetail, "unknown_key")
	require.NotContains(t, v.StatusDetail, "secret_missing_value", "no warning")
	before := f.settings(f.staging)

	f.repos.openErr = errors.New("the provider is down")
	v = status("")
	require.Equal(t, configsync.EnvUnreachable, v.Status)
	require.Contains(t, v.StatusDetail, "the provider is down")
	after := f.settings(f.staging)
	require.Equal(t, before.LastPlan, after.LastPlan, "a failed read keeps what the last good read found")
	require.Equal(t, before.LastFileSha256, after.LastFileSha256)
	f.repos.openErr = nil

	f.planner.planErr = errors.New("boom")
	v = status("")
	require.Equal(t, configsync.EnvError, v.Status)
	require.Contains(t, v.StatusDetail, "boom")
	f.planner.planErr = nil

	// A file that is too large is invalid without being planned.
	f.repos.put("main", "nebula.toml", strings.Repeat("# x\n", (projectconfig.MaxFileBytes/4)+10))
	plans := f.planner.planCount()
	v = status("")
	require.Equal(t, configsync.EnvInvalid, v.Status)
	require.Contains(t, v.StatusDetail, "KiB")
	require.Equal(t, plans, f.planner.planCount())

	// Deleting the file again.
	delete(f.repos.files["main"], "nebula.toml")
	require.Equal(t, configsync.EnvNotFound, status("").Status)
}

func differsPlan2(req configsync.PlanRequest, _ bool) configsync.Plan {
	p := differsPlan(req, false)
	second := p.Services[0]
	second.Slug = "worker"
	p.Services = append(p.Services, second)
	return p
}

func TestCheckValidatesTheSHAAndNeedsAScope(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	_, err := f.gs.Check(f.admin, f.staging.ID, "not a sha")
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)

	none := f.principal(auth.TypeToken, []string{policy.ScopeServersRead})
	_, err = f.gs.Get(none, f.staging.ID)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = f.gs.Check(none, f.staging.ID, "")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)

	// An environment of another team is not found.
	_, err = f.gs.Get(f.admin, uuid.New())
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)

	// An explicit check at a commit reads that commit.
	f.repos.put(shaOf('e'), "nebula.toml", tomlV1)
	v, err := f.gs.Check(f.admin, f.staging.ID, shaOf('e'))
	require.NoError(t, err)
	require.Equal(t, configsync.EnvDiffers, v.Status)
	require.Equal(t, shaOf('e'), v.Commit)
}

func TestReadReturnsTheFileAtTheRef(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	f.repos.put(shaOf('a'), "nebula.toml", tomlV1)
	b, err := f.gs.Resolve(context.Background(), f.staging.ID)
	require.NoError(t, err)

	in, err := f.gs.Read(context.Background(), b, shaOf('a'))
	require.NoError(t, err)
	require.Equal(t, tomlV1, in.File)
	require.Equal(t, configsync.SourceGit, in.Source.Kind)
	require.Equal(t, shaOf('a'), in.Source.Commit)
	require.Equal(t, "nebula.toml", in.Source.Path)

	_, err = f.gs.Read(context.Background(), b, shaOf('f'))
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusNotFound, ae.Status)
	require.Equal(t, "config_not_found", ae.Code)

	f.repos.openErr = errors.New("down")
	_, err = f.gs.Read(context.Background(), b, shaOf('a'))
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusConflict, ae.Status)
	require.Equal(t, "config_unreachable", ae.Code)

	none, err := f.gs.Resolve(context.Background(), f.prod.ID)
	require.NoError(t, err)
	_, err = f.gs.Read(context.Background(), none, "")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "config_source_required", ae.Code)
}

// ---- auto-apply ----

func TestAutoApplyStagesBeforeThePushHookReturns(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	f.enableAutoApply(f.staging)
	sha := shaOf('a')
	f.repos.put(sha, "nebula.toml", tomlV1)

	f.gs.BeforePush(context.Background(), f.push(sha, true, "nebula.toml"))

	require.Equal(t, []string{"plan", "apply", "plan"}, f.log.all(), "plan, stage, plan again for the status")
	require.Len(t, f.planner.applies, 1)
	ap := f.planner.applies[0]
	require.Equal(t, f.planner.plans[0].Input.File, ap.Input.File)
	require.Equal(t, "pl_"+hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte(tomlV1)); return s[:8] }()), ap.PlanID, "pinned to the plan it just made")
	require.False(t, ap.Deploy, "auto-apply never deploys")
	require.False(t, ap.Prune, "auto-apply never prunes")
	require.Equal(t, configsync.OriginGit, ap.Input.Source.Origin)
	require.Equal(t, sha, ap.Input.Source.Commit)
	by := f.planner.applyBy[0]
	require.Equal(t, auth.TypeSystem, by.Type)
	require.Equal(t, "nebula.toml @aaaaaaa", by.Label)
	require.True(t, policy.HasScope(by, policy.ScopeProjectsWrite))
	require.True(t, policy.HasScope(by, policy.ScopeDeploymentsWrite))
	for _, s := range []string{policy.ScopeAdmin, policy.ScopeProxyWrite, policy.ScopeServersWrite, policy.ScopeDeploymentsRollback} {
		require.False(t, policy.HasScope(by, s), "a push cannot grant %s", s)
	}
	require.Equal(t, []uuid.UUID{f.project.ID}, by.ProjectIDs)

	row := f.settings(f.staging)
	require.Equal(t, configsync.EnvInSync, row.Status, "the status is that of the environment after the apply")
	require.Equal(t, "applied automatically", row.StatusDetail)
	require.True(t, row.AutoApply)
}

func TestAutoApplyOffOnlyRecordsTheStatus(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	f.repos.put(shaOf('a'), "nebula.toml", tomlV1)
	f.gs.BeforePush(context.Background(), f.push(shaOf('a'), true, "nebula.toml"))
	require.Empty(t, f.planner.applies)
	require.Equal(t, configsync.EnvDiffers, f.settings(f.staging).Status)
}

func TestAutoApplyRefusesWhatAPersonMustDecide(t *testing.T) {
	cases := []struct {
		name   string
		plan   func(configsync.PlanRequest, bool) configsync.Plan
		apply  error
		detail string
		status string
	}{
		{
			name: "invalid file",
			plan: func(req configsync.PlanRequest, _ bool) configsync.Plan {
				p := differsPlan(req, false)
				p.Valid = false
				p.Diagnostics = configsync.Diagnostics{{Severity: projectconfig.SeverityError, Code: "unknown_key", Message: "x", Line: 1, Column: 1}}
				return p
			},
			detail: "nebula.toml:1:1", status: configsync.EnvInvalid,
		},
		{
			name: "privileged diagnostic",
			plan: func(req configsync.PlanRequest, _ bool) configsync.Plan {
				p := differsPlan(req, false)
				p.Diagnostics = configsync.Diagnostics{{Severity: projectconfig.SeverityWarning, Code: "auto_apply_privileged", Message: "privileged: true"}}
				return p
			},
			detail: "host-level capabilities", status: configsync.EnvDiffers,
		},
		{
			name: "privileged blocker on a service",
			plan: func(req configsync.PlanRequest, _ bool) configsync.Plan {
				p := differsPlan(req, false)
				p.Services[0].Blockers = []string{"auto_apply_privileged"}
				return p
			},
			detail: "host-level capabilities", status: configsync.EnvDiffers,
		},
		{
			name:  "missing scopes",
			apply: configsync.ErrInsufficientScope([]string{"admin"}), detail: "a person applies it", status: configsync.EnvDiffers,
		},
		{
			name:  "stale plan",
			apply: configsync.ErrPlanStale(configsync.Plan{}), detail: "next push checks again", status: configsync.EnvDiffers,
		},
		{
			name:  "other failure",
			apply: errors.New("database is down"), detail: "auto-apply failed: database is down", status: configsync.EnvDiffers,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newGitFixture(t)
			f.gitService(f.staging, "web", "main", "")
			f.enableAutoApply(f.staging)
			f.repos.put(shaOf('a'), "nebula.toml", tomlV1)
			f.planner.result, f.planner.applyErr = c.plan, c.apply

			f.gs.BeforePush(context.Background(), f.push(shaOf('a'), true, "nebula.toml"))

			row := f.settings(f.staging)
			require.Equal(t, c.status, row.Status)
			require.Contains(t, row.StatusDetail, c.detail)
			if c.apply == nil {
				require.Empty(t, f.planner.applies, "nothing is applied")
			} else {
				require.Len(t, f.planner.applies, 1)
				require.Contains(t, row.StatusDetail, "auto-apply")
			}
		})
	}
}

func TestPartialAutoApplyIsReportedInTheStatus(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	f.enableAutoApply(f.staging)
	f.repos.put(shaOf('a'), "nebula.toml", tomlV1)
	f.planner.applyStat = configsync.StatusPartial
	f.gs.BeforePush(context.Background(), f.push(shaOf('a'), true, "nebula.toml"))
	require.Contains(t, f.settings(f.staging).StatusDetail, "auto-apply did not finish")
}

func TestProductionNeverAutoApplies(t *testing.T) {
	f := newGitFixture(t)
	_, err := f.gs.Put(f.admin, f.prod.ID, configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("main")})
	require.NoError(t, err)
	f.gs.Wait()
	f.planner.reset()
	// Even a row that says otherwise (written around the service layer) is not obeyed.
	f.enableAutoApply(f.prod)
	f.repos.put(shaOf('a'), "nebula.toml", tomlV1)

	f.gs.BeforePush(context.Background(), f.push(shaOf('a'), true, "nebula.toml"))

	require.Len(t, f.planner.plans, 1)
	require.Empty(t, f.planner.applies, "production shows the badge and the review; a person applies")
	require.Equal(t, configsync.EnvDiffers, f.settings(f.prod).Status)

	// The service layer refuses to turn it on, with the documented code.
	_, err = f.gs.Put(f.admin, f.prod.ID, configsync.UpdateSourceInput{AutoApply: new(true)})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, configsync.CodeProductionAutoApplyOff, ae.Code)
}

func TestMarkingAnEnvironmentProductionClearsAutoApply(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	_, err := f.gs.Put(f.admin, f.staging.ID, configsync.UpdateSourceInput{AutoApply: new(true)})
	require.NoError(t, err)
	require.True(t, f.settings(f.staging).AutoApply)

	envSvc := environments.NewService(f.pool, f.bus)
	_, err = envSvc.Update(f.admin, f.staging.ID, environments.UpdateInput{IsProduction: new(true)})
	require.NoError(t, err)
	require.False(t, f.settings(f.staging).AutoApply, "cleared in the transaction that flags the environment")

	view, err := f.gs.Get(f.admin, f.staging.ID)
	require.NoError(t, err)
	require.False(t, view.AutoApply)
	// A production environment never starts without a row either.
	_, err = envSvc.Update(f.admin, f.prod.ID, environments.UpdateInput{IsProduction: new(true)})
	require.NoError(t, err)
	require.False(t, f.hasSettings(f.prod))
}

func TestSwitchingOnAutoApplyNeedsAdminAndAPerson(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	on := configsync.UpdateSourceInput{AutoApply: new(true)}
	forbidden := func(ctx context.Context) {
		t.Helper()
		_, err := f.gs.Put(ctx, f.staging.ID, on)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae)
		require.Equal(t, http.StatusForbidden, ae.Status)
		require.False(t, f.hasSettings(f.staging) && f.settings(f.staging).AutoApply)
	}

	developer := policy.ScopesForRole(auth.RoleDeveloper)
	forbidden(f.principal(auth.TypeUser, developer))
	forbidden(f.principal(auth.TypeMCP, policy.AllScopes))
	guarded := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.user.ID, WorkspaceID: f.ws.ID, Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
	forbidden(guarded)

	// No binding at all: nothing to apply from.
	_, err := f.gs.Put(f.admin, f.prod.ID, on)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)

	v, err := f.gs.Put(f.admin, f.staging.ID, on)
	require.NoError(t, err)
	require.True(t, v.AutoApply)

	// Turning it off needs only projects:write.
	v, err = f.gs.Put(f.principal(auth.TypeUser, developer), f.staging.ID, configsync.UpdateSourceInput{AutoApply: new(false)})
	require.NoError(t, err)
	require.False(t, v.AutoApply)
}

func TestPutValidatesTheBindingAndClearDropsAutoApply(t *testing.T) {
	f := newGitFixture(t)
	bad := func(in configsync.UpdateSourceInput) {
		t.Helper()
		_, err := f.gs.Put(f.admin, f.staging.ID, in)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, "%+v", in)
	}
	bad(configsync.UpdateSourceInput{Repo: new("acme/shop")})                                                                   // all three or none
	bad(configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("not a repo"), Branch: new("main")})                   // repo shape
	bad(configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/../x"), Branch: new("main")})                    // traversal
	bad(configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("-rf")})                     // option-looking branch
	bad(configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("a b")})                     // whitespace
	bad(configsync.UpdateSourceInput{ConnectionID: new(uuid.New()), Repo: new("acme/shop"), Branch: new("main")})               // unknown connection
	bad(configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("main"), Path: new("../x")}) // path escape
	bad(configsync.UpdateSourceInput{Path: new("deploy/nebula.toml")})                                                          // a path pins a binding that exists

	v, err := f.gs.Put(f.admin, f.staging.ID, configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("main"), AutoApply: new(true)})
	require.NoError(t, err)
	require.True(t, v.AutoApply)

	// The binding's path changes without touching the rest.
	v, err = f.gs.Put(f.admin, f.staging.ID, configsync.UpdateSourceInput{Path: new("deploy/nebula.toml")})
	require.NoError(t, err)
	require.Equal(t, "deploy/nebula.toml", v.Path)
	require.Equal(t, "acme/shop", v.Repo)
	require.True(t, v.AutoApply)

	require.NoError(t, f.gs.Clear(f.admin, f.staging.ID))
	v, err = f.gs.Get(f.admin, f.staging.ID)
	require.NoError(t, err)
	require.Equal(t, configsync.BindingNone, v.Mode)
	require.False(t, v.AutoApply, "clearing the binding clears auto-apply")
	require.False(t, f.settings(f.staging).AutoApply)

	// A binding change is audited without anything secret.
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action IN ('config.source.update','config.source.clear')`).Scan(&n))
	require.GreaterOrEqual(t, n, 3)
}

// ---- Bitbucket / SSH fallback ----

func TestRepositoriesWithoutAnAPIReaderAreReadOnTheAgent(t *testing.T) {
	f := newGitFixture(t)
	web := f.gitService(f.staging, "web", "main", "apps/web")
	f.repos.noAPI = true
	const file = "schema = 1\n[services.stack.stack]\ncompose_file = \"docker-compose.yml\"\n"
	f.scan.files = map[string][]byte{
		"apps/web/nebula.toml":        []byte(file),
		"apps/web/docker-compose.yml": []byte("services: {}\n"),
	}
	f.scan.commit = shaOf('9')

	// A Bitbucket push lists no files: it reads, and the API has no reader.
	f.gs.BeforePush(context.Background(), f.push(shaOf('a'), false))

	require.Len(t, f.scan.calls, 2, "the file, then its attachment")
	require.Equal(t, scanCall{service: web.ID, branch: "main", paths: []string{"nebula.toml", "apps/web/nebula.toml"}}, f.scan.calls[0])
	require.Equal(t, []string{"apps/web/docker-compose.yml"}, f.scan.calls[1].paths)
	require.Len(t, f.planner.plans, 1)
	in := f.planner.plans[0].Input
	require.Equal(t, file, in.File)
	require.Equal(t, map[string]string{"docker-compose.yml": "services: {}\n"}, in.Attachments)
	require.Equal(t, shaOf('9'), in.Source.Commit, "the agent reports the commit it read")
	require.Equal(t, "apps/web/nebula.toml", in.Source.Path)
	row := f.settings(f.staging)
	require.Equal(t, configsync.EnvDiffers, row.Status)
	require.Equal(t, shaOf('9'), row.LastCommitSha)
}

func TestAnOfflineServerLeavesTheStatusUnreachable(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	f.repos.noAPI = true
	f.repos.put("main", "nebula.toml", tomlV1) // never reached
	f.scan.files = map[string][]byte{"nebula.toml": []byte(tomlV1)}
	f.gs.BeforePush(context.Background(), f.push(shaOf('a'), false))
	require.Equal(t, configsync.EnvDiffers, f.settings(f.staging).Status)
	prior := f.settings(f.staging)

	f.scan.err = apierr.Conflict("server_offline", "agent1 is offline; the repository is read on the server that will build it.")
	f.gs.BeforePush(context.Background(), f.push(shaOf('b'), false))
	row := f.settings(f.staging)
	require.Equal(t, configsync.EnvUnreachable, row.Status)
	require.Equal(t, "server offline", row.StatusDetail)
	require.Equal(t, prior.LastPlan, row.LastPlan)

	f.scan.err = apierr.New(http.StatusGatewayTimeout, "env_scan_timeout", "Reading the repository took too long on agent1.")
	f.gs.BeforePush(context.Background(), f.push(shaOf('b'), false))
	require.Contains(t, f.settings(f.staging).StatusDetail, "took too long")
}

func TestARepositoryNoServiceCanReadIsUnreachable(t *testing.T) {
	f := newGitFixture(t)
	_, err := f.gs.Put(f.admin, f.staging.ID, configsync.UpdateSourceInput{ConnectionID: &f.conn.ID, Repo: new("acme/shop"), Branch: new("main")})
	require.NoError(t, err)
	f.gs.Wait()
	f.repos.noAPI = true
	f.gs.BeforePush(context.Background(), f.push(shaOf('a'), false))
	row := f.settings(f.staging)
	require.Equal(t, configsync.EnvUnreachable, row.Status)
	require.Contains(t, row.StatusDetail, "no git service")
	require.Empty(t, f.scan.calls)
}

// ---- build checkouts ----

func (f *gitFixture) frame(svc uuid.UUID, commit string, files map[string][]byte, paths ...string) agentprotocol.SourceScan {
	return agentprotocol.SourceScan{ServiceID: svc.String(), DeploymentID: uuid.NewString(), Commit: commit, ConfigPaths: paths, ConfigFiles: files}
}

func TestBuildCheckoutRefreshesTheStatusWithoutApplying(t *testing.T) {
	f := newGitFixture(t)
	web := f.gitService(f.staging, "web", "main", "")
	f.enableAutoApply(f.staging)
	ctx := context.Background()
	f.repos.put(shaOf('a'), "nebula.toml", tomlV1)
	f.planner.result = func(req configsync.PlanRequest, _ bool) configsync.Plan { return differsPlan(req, false) }
	f.gs.BeforePush(ctx, f.push(shaOf('a'), true, "nebula.toml"))
	require.Len(t, f.planner.applies, 1, "the push auto-applied")
	f.planner.reset()
	f.planner.result = func(req configsync.PlanRequest, _ bool) configsync.Plan { return differsPlan(req, false) }
	first := f.settings(f.staging)

	// The built commit carries the file the status already describes... but the
	// environment moved (the apply bumped generations in real life): re-plan, no apply.
	_, err := f.pool.Exec(ctx, `UPDATE services SET generation = generation + 1 WHERE id = $1`, web.ID)
	require.NoError(t, err)
	require.NoError(t, f.gs.HandleSourceScan(ctx, f.server.ID, f.frame(web.ID, shaOf('b'), map[string][]byte{"nebula.toml": []byte(tomlV1)}, "nebula.toml")))
	f.gs.Wait()
	require.Equal(t, 1, f.planner.planCount())
	require.Empty(t, f.planner.applies, "a build refresh never applies")
	require.Equal(t, shaOf('b'), f.planner.plans[0].Input.Source.Commit)
	require.Equal(t, 0, f.repos.openCount()-1, "the file came with the frame: no provider read")

	// Same file, nothing moved: the time moves, nothing is planned.
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, f.gs.HandleSourceScan(ctx, f.server.ID, f.frame(web.ID, shaOf('c'), map[string][]byte{"nebula.toml": []byte(tomlV1)}, "nebula.toml")))
	f.gs.Wait()
	require.Equal(t, 1, f.planner.planCount())
	row := f.settings(f.staging)
	require.Equal(t, shaOf('c'), row.LastCommitSha)
	require.True(t, row.CheckedAt.After(*first.CheckedAt))

	// A commit without the file.
	require.NoError(t, f.gs.HandleSourceScan(ctx, f.server.ID, f.frame(web.ID, shaOf('d'), nil, "nebula.toml")))
	f.gs.Wait()
	require.Equal(t, configsync.EnvNotFound, f.settings(f.staging).Status)
	require.Equal(t, 1, f.planner.planCount())
}

func TestBuildCheckoutFramesAreFilteredAndBounded(t *testing.T) {
	f := newGitFixture(t)
	web := f.gitService(f.staging, "web", "main", "apps/web")
	ctx := context.Background()
	other, err := f.q.CreateServer(ctx, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "other"})
	require.NoError(t, err)
	file := map[string][]byte{"nebula.toml": []byte(tomlV1)}
	both := []string{"nebula.toml", "apps/web/nebula.toml"}

	run := func(server uuid.UUID, s agentprotocol.SourceScan) {
		t.Helper()
		require.NoError(t, f.gs.HandleSourceScan(ctx, server, s))
		f.gs.Wait()
	}
	run(f.server.ID, f.frame(web.ID, shaOf('a'), file))                                   // an agent that did not look (old agent)
	run(f.server.ID, f.frame(web.ID, shaOf('a'), file, "nebula.toml"))                    // looked at the root only: the context file is unknown
	run(other.ID, f.frame(web.ID, shaOf('a'), file, both...))                             // not this service's server
	run(f.server.ID, f.frame(web.ID, "not-a-sha", file, both...))                         // garbage commit
	run(f.server.ID, f.frame(uuid.New(), shaOf('a'), file, both...))                      // unknown service
	run(f.server.ID, agentprotocol.SourceScan{ServiceID: "nope", Commit: shaOf('a')})     // garbage id
	run(f.server.ID, f.frame(web.ID, shaOf('a'), map[string][]byte{"../x": []byte("x")})) // no candidates looked at
	require.Zero(t, f.planner.planCount())
	require.False(t, f.hasSettings(f.staging))

	// The accepted frame, with a path outside the looked-at ones ignored.
	run(f.server.ID, f.frame(web.ID, shaOf('a'), map[string][]byte{"nebula.toml": []byte(tomlV1), "other.toml": []byte("x")}, both...))
	require.Equal(t, 1, f.planner.planCount())
	require.Equal(t, tomlV1, f.planner.plans[0].Input.File)
	require.Equal(t, "nebula.toml", f.planner.plans[0].Input.Source.Path)
}

// ---- source changes ----

func TestSourceChangesRefreshTheStatus(t *testing.T) {
	f := newGitFixture(t)
	f.gitService(f.staging, "web", "main", "")
	f.svcs.AddSourceHook(f.gs)
	f.repos.put("main", "nebula.toml", tomlV1)

	// A second service of the repository appears: the hook schedules a status
	// refresh of its environment (no push, no auto-apply).
	f.enableAutoApply(f.staging)
	_, err := f.svcs.Create(f.admin, f.staging.ID, services.CreateInput{
		Name: "worker", SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 80},
	})
	require.NoError(t, err)
	f.gs.Wait()

	require.Equal(t, configsync.EnvDiffers, f.settings(f.staging).Status)
	require.Equal(t, []string{"main"}, f.repos.opens, "read at the branch head")
	require.Empty(t, f.planner.applies, "a source change refreshes the status and nothing more")
	f.awaitEvent(f.staging)
}

func TestSourceHooksCanBeChained(t *testing.T) {
	f := newGitFixture(t)
	var calls []string
	f.svcs.AddSourceHook(hookRecorder{&calls, "first"})
	f.svcs.AddSourceHook(hookRecorder{&calls, "second"})
	f.svcs.AddSourceHook(hookRecorder{&calls, "third"})
	f.gitService(f.staging, "web", "main", "")
	require.Equal(t, []string{"first", "second", "third"}, calls, "in registration order, each once")
}

type hookRecorder struct {
	calls *[]string
	name  string
}

func (h hookRecorder) GitSourceChanged(context.Context, services.Scope) {
	*h.calls = append(*h.calls, h.name)
}

// ---- the whole push ----

type logDeployer struct{ log *orderLog }

func (d logDeployer) DeployInternal(_ context.Context, id uuid.UUID, _ string, _ *deployments.CommitInfo, _ string) (deployments.DeploymentView, error) {
	d.log.add("deploy")
	return deployments.DeploymentView{ID: uuid.New(), ServiceID: id}, nil
}

// A push lands code and config together: the config of the pushed commit is
// staged before the deployment of the same push is created. The repository is
// a Bitbucket one (no file list, no API reader), so the file is read on the
// agent.
func TestPushHookStagesTheConfigBeforeThePushDeploys(t *testing.T) {
	f := newGitFixture(t)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gp := gitproviders.NewService(gitproviders.Deps{
		Pool: f.pool, Bus: f.bus, Box: box, Config: &config.Config{PublicURL: "https://nebula.example.com"},
		Services: f.svcs, Deployer: logDeployer{f.log}, Logger: logger,
	})
	gp.SetPushHook(f.gs)
	router := chi.NewRouter()
	gp.Mount(router)
	hooks := httptest.NewServer(router)
	t.Cleanup(hooks.Close)

	res, err := gp.CreateConnection(f.admin, gitproviders.CreateConnectionInput{Provider: gitproviders.Bitbucket, Name: "Bitbucket"})
	require.NoError(t, err)
	svc, err := f.svcs.Create(f.admin, f.staging.ID, services.CreateInput{
		Name: "web", SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://bitbucket.org/team/webapp.git", GitBranch: "main", Port: 8080},
	})
	require.NoError(t, err)
	_, err = gp.Link(f.admin, svc.ID, gitproviders.LinkInput{ConnectionID: res.Connection.ID, Repo: "team/webapp", Branch: "main"})
	require.NoError(t, err)
	f.enableAutoApply(f.staging)
	f.repos.noAPI = true
	const sha = "709d658dc5b6d6afcd46049c2f332ee3f515a67d"
	f.scan.files = map[string][]byte{"nebula.toml": []byte(tomlV1)}
	f.scan.commit = sha

	body, err := os.ReadFile("../gitproviders/testdata/bitbucket_push.json")
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(res.WebhookSecret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, hooks.URL+"/hooks/git/bitbucket/"+res.Connection.ID.String(), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Key", "repo:push")
	req.Header.Set("X-Request-UUID", "bb-1")
	req.Header.Set("X-Hub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	gp.Wait()

	require.Equal(t, []string{"scan", "plan", "apply", "plan", "deploy"}, f.log.all(), "read on the agent, stage the config, then deploy")
	require.Equal(t, scanCall{service: svc.ID, branch: "main", paths: []string{"nebula.toml"}}, f.scan.calls[0])
	require.Equal(t, configsync.EnvInSync, f.settings(f.staging).Status)
}
