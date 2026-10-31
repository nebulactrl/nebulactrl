package previews_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/previews"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// --- fakes -----------------------------------------------------------------

// rowDispatcher stores commands as real agent_commands rows, like the hub.
type rowDispatcher struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
}

func (d *rowDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	d.cmds = append(d.cmds, c)
	d.mu.Unlock()
	id := uuid.New()
	_, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	return id, err
}

func (d *rowDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *rowDispatcher) kinds() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.cmds {
		out = append(out, c.Kind)
	}
	return out
}

func (d *rowDispatcher) count(kind string) int {
	n := 0
	for _, k := range d.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// fakeGit is the provider: it records comments, statuses and links.
type fakeGit struct {
	mu       sync.Mutex
	services []uuid.UUID
	comments map[string]string // marker → body
	statuses []previews.CommitStatus
	statusOn []string // sha per status
	linked   []uuid.UUID
}

func (g *fakeGit) ServicesForRepo(context.Context, uuid.UUID, string) ([]uuid.UUID, error) {
	return g.services, nil
}

func (g *fakeGit) CopyServiceGitTx(_ context.Context, _ pgx.Tx, _, to uuid.UUID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.linked = append(g.linked, to)
	return nil
}

func (g *fakeGit) SetCommitStatus(_ context.Context, _ uuid.UUID, _, sha string, st previews.CommitStatus) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.statuses = append(g.statuses, st)
	g.statusOn = append(g.statusOn, sha)
	return nil
}

func (g *fakeGit) UpsertPRComment(_ context.Context, _ uuid.UUID, _ string, _ int, marker, body string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.comments == nil {
		g.comments = map[string]string{}
	}
	g.comments[marker] = body
	return nil
}

func (g *fakeGit) onlyComment() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range g.comments {
		return b
	}
	return ""
}

type deployCall struct {
	Service                      uuid.UUID
	SHA, Message, Author, Branch string
}

// fakeDeployer creates real deployment rows; tests finish them.
type fakeDeployer struct {
	t     *testing.T
	pool  *pgxpool.Pool
	srvID uuid.UUID
	calls []deployCall
}

func (d *fakeDeployer) Deploy(ctx context.Context, serviceID uuid.UUID, commit previews.Commit, _ string) (uuid.UUID, error) {
	q := db.New(d.pool)
	if n, _ := q.CountActiveDeployments(ctx, serviceID); n > 0 {
		return uuid.Nil, apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
	}
	dep, err := q.InsertDeployment(ctx, db.InsertDeploymentParams{
		ServiceID: serviceID, ServerID: &d.srvID, Trigger: "preview", ActorType: "system", Strategy: "recreate",
	})
	if err != nil {
		return uuid.Nil, err
	}
	d.calls = append(d.calls, deployCall{Service: serviceID, SHA: commit.SHA, Message: commit.Message, Author: commit.Author, Branch: commit.Branch})
	return dep.ID, nil
}

func (d *fakeDeployer) Cancel(context.Context, uuid.UUID) error { return nil }

func (d *fakeDeployer) callsFor(id uuid.UUID) []deployCall {
	var out []deployCall
	for _, c := range d.calls {
		if c.Service == id {
			out = append(out, c)
		}
	}
	return out
}

type fakeSeeder struct {
	mu       sync.Mutex
	runs     []seedRun
	statuses map[uuid.UUID]string
}

type seedRun struct {
	ID      uuid.UUID
	Service uuid.UUID
	Command []string
}

func (s *fakeSeeder) RunSeed(_ context.Context, _ pgx.Tx, serviceID uuid.UUID, command []string) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.New()
	s.runs = append(s.runs, seedRun{ID: id, Service: serviceID, Command: command})
	return id, nil
}

func (s *fakeSeeder) RunStatus(_ context.Context, id uuid.UUID) (string, *int, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.statuses[id]
	if st == "" {
		st = "running"
	}
	if st == "failed" {
		return st, new(3), "", nil
	}
	return st, nil, "", nil
}

func (s *fakeSeeder) finishAll(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statuses == nil {
		s.statuses = map[uuid.UUID]string{}
	}
	for _, r := range s.runs {
		s.statuses[r.ID] = status
	}
}

// noopDNSWorker lets the clone enqueue DNS checks (River validates job kinds).
type noopDNSWorker struct {
	river.WorkerDefaults[domains.DNSCheckArgs]
}

func (*noopDNSWorker) Work(context.Context, *river.Job[domains.DNSCheckArgs]) error { return nil }

type fakeNotifier struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (n *fakeNotifier) Emit(_ context.Context, ev notifications.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
}

func (n *fakeNotifier) types() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, e := range n.events {
		out = append(out, e.Type)
	}
	return out
}

// --- fixture ---------------------------------------------------------------

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *db.Queries
	ctx      context.Context
	box      *secretbox.Box
	disp     *rowDispatcher
	svcs     *services.Service
	dbs      *databases.Service
	git      *fakeGit
	deployer *fakeDeployer
	seeder   *fakeSeeder
	notifier *fakeNotifier
	pv       *previews.Service
	project  db.Project
	base     db.Environment
	server   db.Server
	ws       db.Workspace
	conn     uuid.UUID

	postgres db.Service
	api      db.Service
	worker   db.Service
	web      db.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	databases.RegisterExports()
	t.Cleanup(func() { services.RegisterExportProvider(nil) })
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	base, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', base_domain = 'example.com' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	disp := &rowDispatcher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, disp, logger)
	dbs := databases.NewService(pool, bus, box, disp, svcs, logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "dev@example.com", Scopes: policy.AllScopes})

	f := &fixture{
		t: t, pool: pool, q: q, ctx: ctx, box: box, disp: disp, svcs: svcs, dbs: dbs, project: prj, base: base, server: srv, ws: ws,
		conn: uuid.New(), git: &fakeGit{}, seeder: &fakeSeeder{}, notifier: &fakeNotifier{},
		deployer: &fakeDeployer{t: t, pool: pool, srvID: srv.ID},
	}
	jm := jobs.New(pool, logger)
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &noopDNSWorker{}) })
	f.pv = previews.NewService(previews.Deps{
		Pool: pool, Bus: bus, Jobs: jm, Box: box, Config: &config.Config{PublicURL: "https://nebula.example.com"},
		Services: svcs, Databases: dbs, Git: f.git, Deployer: f.deployer, Seeder: f.seeder, Notifier: f.notifier, Logger: logger,
	})

	previews.RegisterJobs(jm, f.pv)

	// Base environment: postgres, api (git, builds the PR repo), worker (git of
	// another repo), web (image, public).
	pgView, err := dbs.Create(ctx, databases.CreateInput{EnvironmentID: base.ID, Name: "postgres", Engine: "postgres", Version: "17", ServerID: &srv.ID, AcceptLicense: true})
	require.NoError(t, err)
	f.postgres = f.row(pgView.ID)
	f.api = f.insert("api", services.SourceGit, services.Config{GitURL: "https://github.com/acme/shop.git", GitBranch: "main", Port: 8080})
	f.worker = f.insert("worker", services.SourceGit, services.Config{GitURL: "https://github.com/acme/jobs.git", GitBranch: "main"})
	f.web = f.insert("web", services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})
	_, err = q.InsertDomain(bg, db.InsertDomainParams{ServiceID: f.api.ID, Hostname: "api.shop.example.com", TargetPort: 8080, Generated: true})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE services SET git_token_enc = $2 WHERE id = $1", f.api.ID, mustSeal(t, box, secretbox.ServicesGitToken, f.api.ID.String(), "ghp_basetoken"))
	require.NoError(t, err)
	f.git.services = []uuid.UUID{f.api.ID}
	return f
}

func mustSeal(t *testing.T, box *secretbox.Box, col secretbox.Column, rowID, s string) []byte {
	b, err := box.SealString(col, rowID, s)
	require.NoError(t, err)
	return b
}

func (f *fixture) row(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *fixture) insert(slug, source string, cfg services.Config) db.Service {
	f.t.Helper()
	raw, _ := json.Marshal(cfg.WithDefaults())
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.base.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: source, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) setVar(serviceID uuid.UUID, key, value string, secret bool) {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, serviceID)
	require.NoError(f.t, err)
	_, err = services.WriteServiceVariable(context.Background(), f.q, f.box, sc, services.VarWrite{Key: key, Value: value, Secret: secret})
	require.NoError(f.t, err)
}

func (f *fixture) vars(serviceID uuid.UUID) map[string]services.RawVar {
	f.t.Helper()
	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, serviceID)
	require.NoError(f.t, err)
	return raw
}

func (f *fixture) enable(mutate func(*previews.PreviewSettings)) {
	f.t.Helper()
	st := previews.PreviewSettings{Enabled: true, BaseEnvironmentID: &f.base.ID, MaxConcurrent: 5, IdleDays: 7}
	if mutate != nil {
		mutate(&st)
	}
	_, err := f.pv.PutSettings(f.ctx, f.project.ID, st)
	require.NoError(f.t, err)
}

func (f *fixture) pr(number int, action, sha string) previews.PullRequest {
	return previews.PullRequest{
		ConnectionID: f.conn, Provider: "github", Repo: "acme/shop", Action: action, Number: number, Title: "Add checkout",
		Author: "octocat", HeadBranch: "feature-" + sha, HeadSHA: sha, BaseBranch: "main",
		HeadCloneURL: "https://github.com/acme/shop.git", HTMLURL: "https://github.com/acme/shop/pull/12",
	}
}

func (f *fixture) previewOf(number int) db.Preview {
	f.t.Helper()
	pv, err := f.q.GetPreviewByPR(context.Background(), db.GetPreviewByPRParams{ProjectID: f.project.ID, ConnectionID: f.conn, Repo: "acme/shop", PrNumber: int32(number)})
	require.NoError(f.t, err)
	return pv
}

func (f *fixture) envServices(envID uuid.UUID) map[string]db.Service {
	f.t.Helper()
	rows, err := f.q.ListAllEnvironmentServices(context.Background(), envID)
	require.NoError(f.t, err)
	out := map[string]db.Service{}
	for _, r := range rows {
		out[r.Slug] = r
	}
	return out
}

// finish completes the newest deployment of a service like the agent would.
func (f *fixture) finish(serviceID uuid.UUID, status string) {
	f.t.Helper()
	bg := context.Background()
	dep, err := f.q.GetActiveDeployment(bg, serviceID)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = $2, status_message = $3 WHERE id = $1", dep.ID, status, map[string]string{"failed": "build failed: exit 1"}[status])
	require.NoError(f.t, err)
	if status == "succeeded" {
		sv := f.row(serviceID)
		require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: serviceID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	}
}

// step advances the wave once and reports whether it ended.
func (f *fixture) step(id uuid.UUID) bool {
	f.t.Helper()
	_, done, err := f.pv.AdvanceForTest(context.Background(), id)
	require.NoError(f.t, err)
	return done
}

// runToReady drives a wave: every deployment succeeds, seeds succeed.
func (f *fixture) runToReady(number int) db.Preview {
	f.t.Helper()
	pv := f.previewOf(number)
	for range 20 {
		if f.step(pv.ID) {
			break
		}
		rows := f.envServices(pv.EnvironmentID)
		for _, r := range rows {
			if n, _ := f.q.CountActiveDeployments(context.Background(), r.ID); n > 0 {
				f.finish(r.ID, "succeeded")
			}
		}
		f.seeder.finishAll("succeeded")
	}
	return f.previewOf(number)
}

// --- tests -----------------------------------------------------------------

func TestLifecycleOpenSyncClose(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.setVar(f.api.ID, "DATABASE_URL", "${{postgres.DATABASE_URL}}", false)
	f.setVar(f.api.ID, "API_KEY", "sk-live-base", true)
	f.setVar(f.api.ID, "FEATURE", "on", false)
	f.setVar(f.postgres.ID, "EXTRA", "base-only", false)
	f.enable(func(s *previews.PreviewSettings) {
		s.ExcludedServices = []string{"web"}
		s.SeedCommands = []previews.PreviewSeedCommand{{Service: "api", Command: "npm run migrate && npm run seed"}}
	})

	// opened: a preview environment is cloned and the first wave starts.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(12, "opened", "aaaaaaa1111111111111111111111111111111aa")))
	pv := f.previewOf(12)
	require.Equal(t, previews.StatusDeploying, pv.Status)
	env, err := f.q.GetEnvironmentScoped(bg, db.GetEnvironmentScopedParams{ID: pv.EnvironmentID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)
	require.Equal(t, "preview", env.Kind)
	require.Equal(t, "pr-12", env.Slug)
	require.Equal(t, "PR #12 · Add checkout", env.Name)
	require.Equal(t, f.base.ID, *env.ParentEnvironmentID)
	require.EqualValues(t, 12, *env.PrNumber)
	require.False(t, env.IsProduction)

	svcs := f.envServices(env.ID)
	require.Len(t, svcs, 3, "web is excluded; postgres, api and worker are cloned")
	require.Contains(t, svcs, "postgres")
	require.Contains(t, svcs, "api")
	require.Contains(t, svcs, "worker")
	require.NotContains(t, svcs, "web")

	// The database is fresh: new credentials, its own variables not copied.
	basePW := f.vars(f.postgres.ID)["POSTGRES_PASSWORD"].Value
	prPW := f.vars(svcs["postgres"].ID)["POSTGRES_PASSWORD"].Value
	require.NotEmpty(t, basePW)
	require.NotEmpty(t, prPW)
	require.NotEqual(t, basePW, prPW)
	require.NotContains(t, f.vars(svcs["postgres"].ID), "EXTRA")
	vols, err := f.q.ListServiceVolumes(bg, svcs["postgres"].ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.NotEqual(t, f.postgres.ID, svcs["postgres"].ID)

	// The matching service builds the PR branch; the other git service does not.
	apiCfg := services.DecodeConfig(svcs["api"].Config)
	require.Equal(t, "feature-aaaaaaa1111111111111111111111111111111aa", apiCfg.GitBranch)
	require.Equal(t, "main", services.DecodeConfig(svcs["worker"].Config).GitBranch)
	require.Contains(t, f.git.linked, svcs["api"].ID, "non-fork previews keep the repository link (clone credentials)")

	// Variables are inherited (the secret too, same-repo PR); references follow the new database.
	av := f.vars(svcs["api"].ID)
	require.Equal(t, "sk-live-base", av["API_KEY"].Value)
	require.True(t, av["API_KEY"].Secret)
	require.Equal(t, "on", av["FEATURE"].Value)
	require.Equal(t, "${{postgres.DATABASE_URL}}", av["DATABASE_URL"].Value)
	require.NotEmpty(t, f.row(f.api.ID).GitTokenEnc)
	cloneTok, err := f.box.OpenString(secretbox.ServicesGitToken, svcs["api"].ID.String(), svcs["api"].GitTokenEnc)
	require.NoError(t, err)
	require.Equal(t, "ghp_basetoken", cloneTok, "a same-repo preview keeps the git token (sealed for its own row)")

	// A generated domain `<service>-pr-<n>.<base>` replaces the base domain.
	doms, err := f.q.ListServiceDomains(bg, svcs["api"].ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)
	require.Equal(t, "api-pr-12.example.com", doms[0].Hostname)
	require.True(t, doms[0].Generated)
	none, err := f.q.ListServiceDomains(bg, svcs["worker"].ID)
	require.NoError(t, err)
	require.Empty(t, none)

	// Wave: the database first, then the consumers once it succeeded.
	require.False(t, f.step(pv.ID))
	require.Len(t, f.deployer.calls, 2, "postgres and worker have no providers; api waits for postgres")
	require.Empty(t, f.deployer.callsFor(svcs["api"].ID))
	f.finish(svcs["postgres"].ID, "succeeded")
	f.finish(svcs["worker"].ID, "succeeded")
	require.False(t, f.step(pv.ID))
	require.Len(t, f.deployer.callsFor(svcs["api"].ID), 1)
	require.Equal(t, "aaaaaaa1111111111111111111111111111111aa", f.deployer.callsFor(svcs["api"].ID)[0].SHA, "the matching service is pinned to the head commit")
	require.Empty(t, f.deployer.callsFor(svcs["worker"].ID)[0].SHA, "another repository is not pinned to the PR commit")
	apiCall := f.deployer.callsFor(svcs["api"].ID)[0]
	require.Equal(t, "Add checkout", apiCall.Message)
	require.Equal(t, "octocat", apiCall.Author)
	f.finish(svcs["api"].ID, "succeeded")

	// Seed: runs after the deployments succeeded; the preview stays seeding until it ends.
	require.False(t, f.step(pv.ID))
	require.Len(t, f.seeder.runs, 1)
	require.Equal(t, svcs["api"].ID, f.seeder.runs[0].Service)
	require.Equal(t, []string{"sh", "-c", "npm run migrate && npm run seed"}, f.seeder.runs[0].Command)
	require.Equal(t, previews.StatusSeeding, f.previewOf(12).Status)
	require.False(t, f.step(pv.ID))
	f.seeder.finishAll("succeeded")
	require.True(t, f.step(pv.ID))
	pv = f.previewOf(12)
	require.Equal(t, previews.StatusReady, pv.Status)

	// The sticky comment and the commit status.
	require.NoError(t, f.pv.PublishForTest(bg, pv.ID))
	body := f.git.onlyComment()
	require.Contains(t, body, "<!-- nebulactrl-preview:"+f.project.ID.String()+" -->")
	require.Contains(t, body, "https://api-pr-12.example.com")
	require.Contains(t, body, "ready")
	require.Contains(t, body, "https://nebula.example.com/p/"+f.project.ID.String()+"/pr-12")
	last := f.git.statuses[len(f.git.statuses)-1]
	require.Equal(t, "success", last.State)
	require.Equal(t, previews.CommitContext, last.Context)
	require.Equal(t, "https://api-pr-12.example.com", last.TargetURL)
	require.Len(t, f.git.comments, 1, "one sticky comment per PR")

	// A duplicate delivery does nothing; a push redeploys only the matching service.
	before := len(f.deployer.calls)
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(12, "synchronize", "aaaaaaa1111111111111111111111111111111aa")))
	require.Len(t, f.deployer.calls, before)
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(12, "synchronize", "bbbbbbb2222222222222222222222222222222bb")))
	pv = f.previewOf(12)
	require.Equal(t, previews.StatusDeploying, pv.Status)
	require.Equal(t, "bbbbbbb2222222222222222222222222222222bb", pv.HeadSha)
	require.False(t, f.step(pv.ID))
	require.Len(t, f.deployer.calls, before+1)
	last2 := f.deployer.calls[len(f.deployer.calls)-1]
	require.Equal(t, svcs["api"].ID, last2.Service)
	require.Equal(t, "bbbbbbb2222222222222222222222222222222bb", last2.SHA)
	f.finish(svcs["api"].ID, "succeeded")
	require.True(t, f.step(pv.ID))
	require.Equal(t, previews.StatusReady, f.previewOf(12).Status)
	require.Len(t, f.seeder.runs, 1, "a create-time seed does not run again on push")

	// closed: the environment, its services, volumes and domains go away.
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(12, "closed", "bbbbbbb2222222222222222222222222222222bb")))
	require.Equal(t, previews.StatusDeleting, f.previewOf(12).Status)
	retry, err := f.pv.RunDeleteForTest(bg, pv.ID, "closed")
	require.NoError(t, err)
	require.False(t, retry)
	_, err = f.q.GetPreview(bg, pv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = f.q.GetEnvironmentScoped(bg, db.GetEnvironmentScopedParams{ID: env.ID, WorkspaceID: f.ws.ID})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	var domains int
	require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM domains WHERE hostname = 'api-pr-12.example.com'").Scan(&domains))
	require.Zero(t, domains)
	// remove_service with the volumes for every deployed service.
	require.Equal(t, 3, f.disp.count(agentprotocol.KindRemoveService))
	for _, c := range f.disp.cmds {
		if c.Kind != agentprotocol.KindRemoveService {
			continue
		}
		var p agentprotocol.ServiceActionPayload
		require.NoError(t, json.Unmarshal(c.Payload, &p))
		require.True(t, p.DeleteVolumes)
	}
	require.Contains(t, f.git.onlyComment(), "deleted")
	require.Equal(t, []string{notifications.TypePreviewCreated, notifications.TypePreviewDeleted}, f.notifier.types())
	// The base environment is untouched.
	require.Len(t, f.envServices(f.base.ID), 4)
}

func TestLimitPausesOldestAndResumesOnPush(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) {
		s.MaxConcurrent = 2
		s.ExcludedServices = []string{"web", "worker", "postgres"}
	})

	open := func(n int, sha string) db.Preview {
		require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(n, "opened", sha)))
		return f.runToReady(n)
	}
	one := open(1, "1111111111111111111111111111111111111111")
	two := open(2, "2222222222222222222222222222222222222222")
	require.Equal(t, previews.StatusReady, one.Status)
	require.Equal(t, previews.StatusReady, two.Status)
	require.Zero(t, f.disp.count(agentprotocol.KindStopService))

	// The third preview exceeds the limit of 2: the oldest (PR 1) is paused.
	open(3, "3333333333333333333333333333333333333333")
	one = f.previewOf(1)
	require.Equal(t, previews.StatusPaused, one.Status)
	require.NotNil(t, one.PausedAt)
	require.Equal(t, previews.StatusReady, f.previewOf(2).Status)
	require.Equal(t, previews.StatusReady, f.previewOf(3).Status)
	require.Equal(t, 1, f.disp.count(agentprotocol.KindStopService), "the paused preview's service is stopped")
	svc1 := f.envServices(one.EnvironmentID)["api"]
	require.Equal(t, services.DesiredStopped, f.row(svc1.ID).DesiredState)

	// A paused preview is not advanced by stale wave jobs.
	require.True(t, f.step(one.ID))

	// A push to PR 1 resumes it and pauses the now-oldest running one (PR 2).
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(1, "synchronize", "4444444444444444444444444444444444444444")))
	one = f.previewOf(1)
	require.Equal(t, previews.StatusDeploying, one.Status)
	require.Nil(t, one.PausedAt)
	require.Equal(t, previews.StatusPaused, f.previewOf(2).Status)
	require.Equal(t, previews.StatusReady, f.previewOf(3).Status)
	one = f.runToReady(1)
	require.Equal(t, previews.StatusReady, one.Status)
	last := f.deployer.calls[len(f.deployer.calls)-1]
	require.Equal(t, "4444444444444444444444444444444444444444", last.SHA)
}

func TestIdleSweepDeletes(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) {
		s.IdleDays = 3
		s.ExcludedServices = []string{"web", "worker", "postgres"}
	})
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(7, "opened", "7777777777777777777777777777777777777777")))
	pv := f.runToReady(7)
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(8, "opened", "8888888888888888888888888888888888888888")))
	f.runToReady(8)

	// PR 7 saw its last push 4 days ago, PR 8 two days ago.
	_, err := f.pool.Exec(bg, "UPDATE previews SET last_push_at = now() - interval '4 days' WHERE id = $1", pv.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE previews SET last_push_at = now() - interval '2 days' WHERE pr_number = 8")
	require.NoError(t, err)

	require.NoError(t, f.pv.Sweep(bg))
	require.Equal(t, previews.StatusDeleting, f.previewOf(7).Status)
	require.Equal(t, previews.StatusReady, f.previewOf(8).Status)
	retry, err := f.pv.RunDeleteForTest(bg, pv.ID, "idle")
	require.NoError(t, err)
	require.False(t, retry)
	_, err = f.q.GetPreview(bg, pv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Contains(t, f.git.onlyComment(), "idle")
}

func TestForkPreviewGetsNoSecrets(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.setVar(f.api.ID, "API_KEY", "sk-live-base", true)
	f.setVar(f.api.ID, "FEATURE", "on", false)
	// A non-secret variable that pulls in a secret shared variable is dropped too.
	f.setVar(f.api.ID, "TOKEN_COPY", "${{shared.STRIPE}}", false)
	f.setVar(f.api.ID, "PUBLIC_COPY", "${{shared.REGION}}", false)
	f.setVar(f.api.ID, "DATABASE_URL", "${{postgres.DATABASE_URL}}", false)
	for _, sv := range []struct {
		key    string
		secret bool
	}{{"STRIPE", true}, {"REGION", false}} {
		id := uuid.New()
		enc, err := f.box.SealString(secretbox.SharedVariablesValue, id.String(), "shared-"+sv.key)
		require.NoError(t, err)
		_, err = f.q.InsertSharedVariable(bg, db.InsertSharedVariableParams{
			ID: id, WorkspaceID: f.ws.ID, ProjectID: &f.project.ID, Key: sv.key, ValueEnc: enc, IsSecret: sv.secret,
		})
		require.NoError(t, err)
	}
	reg, err := f.box.SealString(secretbox.ServicesRegistryAuth, f.web.ID.String(), `{"username":"u","password":"registry-secret"}`)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET registry_auth_enc = $2 WHERE id = $1", f.web.ID, reg)
	require.NoError(t, err)

	// Fork previews are off by default: the PR is ignored.
	f.enable(nil)
	fork := f.pr(21, "opened", "2121212121212121212121212121212121212121")
	fork.FromFork = true
	fork.HeadCloneURL = "https://github.com/mallory/shop.git"
	fork.HeadBranch = "evil"
	require.NoError(t, f.pv.ProcessPullRequest(bg, fork))
	_, err = f.q.GetPreviewByPR(bg, db.GetPreviewByPRParams{ProjectID: f.project.ID, ConnectionID: f.conn, Repo: "acme/shop", PrNumber: 21})
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// An admin enables them: the preview is created, without secrets.
	f.enable(func(s *previews.PreviewSettings) { s.ForkPRs = true })
	require.NoError(t, f.pv.ProcessPullRequest(bg, fork))
	pv := f.previewOf(21)
	require.True(t, pv.FromFork)
	svcs := f.envServices(pv.EnvironmentID)
	require.Len(t, svcs, 4)

	api := svcs["api"]
	cfg := services.DecodeConfig(api.Config)
	require.Equal(t, "https://github.com/mallory/shop.git", cfg.GitURL, "a fork builds its own repository")
	require.Equal(t, "evil", cfg.GitBranch)
	require.Empty(t, api.GitTokenEnc, "a fork preview gets no git token beyond a public clone")
	require.Empty(t, svcs["web"].RegistryAuthEnc, "no registry credentials either")
	require.NotContains(t, f.git.linked, api.ID, "no repository link means no provider clone credentials")
	require.Empty(t, f.git.linked)

	vars := f.vars(api.ID)
	require.Equal(t, "on", vars["FEATURE"].Value)
	require.Equal(t, "${{shared.REGION}}", vars["PUBLIC_COPY"].Value)
	require.Equal(t, "${{postgres.DATABASE_URL}}", vars["DATABASE_URL"].Value, "database access stays: it is a fresh, empty database")
	require.NotContains(t, vars, "API_KEY")
	require.NotContains(t, vars, "TOKEN_COPY")
	for slug, sv := range svcs {
		for k, v := range f.vars(sv.ID) {
			require.NotEqual(t, "sk-live-base", v.Value, "%s.%s", slug, k)
			if v.Secret && slug != "postgres" {
				t.Fatalf("fork preview service %s received secret variable %s", slug, k)
			}
		}
	}
	// A fork preview database has fresh credentials like any other.
	require.NotEqual(t, f.vars(f.postgres.ID)["POSTGRES_PASSWORD"].Value, f.vars(svcs["postgres"].ID)["POSTGRES_PASSWORD"].Value)
	// Nothing secret in the stored copy of the shared variables either.
	envShared, err := f.q.ListEnvironmentSharedVariablesRaw(bg, &pv.EnvironmentID)
	require.NoError(t, err)
	for _, v := range envShared {
		require.False(t, v.IsSecret)
	}
}

func TestSeedFailureFailsPreview(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) {
		s.ExcludedServices = []string{"web", "worker", "postgres"}
		s.SeedCommands = []previews.PreviewSeedCommand{{Service: "api", Command: "false", RunOn: "push"}}
	})
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(5, "opened", "5555555555555555555555555555555555555555")))
	pv := f.previewOf(5)
	svc := f.envServices(pv.EnvironmentID)["api"]
	require.False(t, f.step(pv.ID))
	f.finish(svc.ID, "succeeded")
	require.False(t, f.step(pv.ID)) // starts the seed
	f.seeder.finishAll("failed")
	require.True(t, f.step(pv.ID))
	pv = f.previewOf(5)
	require.Equal(t, previews.StatusFailed, pv.Status)
	require.Contains(t, pv.StatusMessage, "Seed command on api failed (exit 3)")
	require.NoError(t, f.pv.PublishForTest(bg, pv.ID))
	require.Equal(t, "failure", f.git.statuses[len(f.git.statuses)-1].State)
}

func TestDeploymentFailureFailsPreview(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker"} })
	f.setVar(f.api.ID, "DATABASE_URL", "${{postgres.DATABASE_URL}}", false)
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(6, "opened", "6666666666666666666666666666666666666666")))
	pv := f.previewOf(6)
	svcs := f.envServices(pv.EnvironmentID)
	require.False(t, f.step(pv.ID))
	f.finish(svcs["postgres"].ID, "failed")
	require.True(t, f.step(pv.ID), "the dependent is not started once its provider failed")
	pv = f.previewOf(6)
	require.Equal(t, previews.StatusFailed, pv.Status)
	require.Contains(t, pv.StatusMessage, "postgres")
	require.Empty(t, f.deployer.callsFor(svcs["api"].ID))
}

func TestSettingsValidationAndDefaults(t *testing.T) {
	f := newFixture(t)
	st, err := f.pv.GetSettings(f.ctx, f.project.ID)
	require.NoError(t, err)
	require.False(t, st.Enabled)
	require.Equal(t, 5, st.MaxConcurrent)
	require.Equal(t, 7, st.IdleDays)
	require.Equal(t, f.base.ID, *st.BaseEnvironmentID)

	_, err = f.pv.PutSettings(f.ctx, f.project.ID, previews.PreviewSettings{Enabled: true, BaseEnvironmentID: &f.base.ID, SeedCommands: []previews.PreviewSeedCommand{{Service: "nope", Command: "x"}}})
	require.Error(t, err)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 422, ae.Status)
	_, err = f.pv.PutSettings(f.ctx, f.project.ID, previews.PreviewSettings{Enabled: true, BaseEnvironmentID: &f.base.ID, MaxConcurrent: 99})
	require.Error(t, err)

	saved, err := f.pv.PutSettings(f.ctx, f.project.ID, previews.PreviewSettings{Enabled: true, BaseEnvironmentID: &f.base.ID, ForkPRs: true,
		SeedCommands: []previews.PreviewSeedCommand{{Service: "api", Command: "npm run migrate"}}, ExcludedServices: []string{"web"}})
	require.NoError(t, err)
	require.Equal(t, 5, saved.MaxConcurrent)
	require.Equal(t, "create", saved.SeedCommands[0].RunOn)
	require.True(t, saved.ForkPRs)

	// A token restricted to another project cannot read them.
	other := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: uuid.New(), TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Scopes: policy.AllScopes, ProjectIDs: []uuid.UUID{uuid.New()},
	})
	_, err = f.pv.GetSettings(other, f.project.ID)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Status)
}

func TestBaseEnvironmentCannotBeDeletedWhilePreviewsExist(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker", "postgres"} })
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(9, "opened", "9999999999999999999999999999999999999999")))
	pv := f.previewOf(9)

	envSvc := environments.NewService(f.pool, events.NewBus())
	err := envSvc.Delete(f.ctx, f.base.ID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	// Not empty comes first; remove the base services to reach the previews check.
	require.Equal(t, "environment_not_empty", ae.Code)
	_, err = f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE environment_id = $1", f.base.ID)
	require.NoError(t, err)
	err = envSvc.Delete(f.ctx, f.base.ID)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "environment_has_previews", ae.Code)

	list, err := envSvc.List(f.ctx, f.project.ID)
	require.NoError(t, err)
	var found bool
	for _, e := range list {
		if e.ID == pv.EnvironmentID {
			found = true
			require.Equal(t, "preview", e.Kind)
			require.Equal(t, f.base.ID, *e.ParentEnvironmentID)
			require.Equal(t, 9, *e.PRNumber)
			require.Equal(t, "Add checkout", e.PRTitle)
		}
	}
	require.True(t, found)
}

func TestManualDeleteAPI(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker", "postgres"} })
	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(3, "opened", "3333333333333333333333333333333333333333")))
	pv := f.previewOf(3)

	list, err := f.pv.List(f.ctx, f.project.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 3, list[0].PRNumber)
	require.Equal(t, "pr-3", list[0].EnvironmentSlug)
	require.Equal(t, []string{"https://api-pr-3.example.com"}, list[0].URLs)

	v, err := f.pv.Delete(f.ctx, pv.ID)
	require.NoError(t, err)
	require.Equal(t, previews.StatusDeleting, v.Status)
	// Idempotent while deleting.
	_, err = f.pv.Delete(f.ctx, pv.ID)
	require.NoError(t, err)

	// A read-only token cannot delete.
	ro := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: uuid.New(), TokenID: new(uuid.New()), WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsRead},
	})
	_, err = f.pv.Delete(ro, pv.ID)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 403, ae.Status)
}

func TestGeneratedVariablesAreGeneratedAgain(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web", "worker", "postgres"} })

	const tmpl = "${{secret(32)}}"
	m, err := varexpr.Materialize(tmpl)
	require.NoError(t, err)
	sc, err := services.LoadScopeInternal(bg, f.q, f.api.ID)
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(bg, f.q, f.box, sc, services.VarWrite{
		Key: "SESSION_SECRET", Value: m.Stored, Secret: true, Source: services.SourceGenerated, Template: tmpl, GenCalls: m.Calls,
	})
	require.NoError(t, err)

	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(30, "opened", "3030303030303030303030303030303030303030")))
	pv := f.previewOf(30)
	got := f.vars(f.envServices(pv.EnvironmentID)["api"].ID)["SESSION_SECRET"]
	require.True(t, got.Secret)
	require.Len(t, got.Value, 32)
	require.NotEqual(t, m.Stored, got.Value, "a preview never shares a generated secret with its base environment")
}

func TestPreviewCloneSkipsComposeStacks(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	raw, err := json.Marshal(services.Config{Stack: &services.StackConfig{Compose: "services:\n  web:\n    image: traefik/whoami:v1.10\n"}}.WithDefaults())
	require.NoError(t, err)
	_, err = f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.base.ID, Name: "stack", Slug: "stack", Kind: services.KindStack, SourceType: services.SourceCompose, Config: raw,
	})
	require.NoError(t, err)
	f.enable(func(s *previews.PreviewSettings) { s.ExcludedServices = []string{"web"} })

	require.NoError(t, f.pv.ProcessPullRequest(bg, f.pr(21, "opened", "aaaaaaa1111111111111111111111111111111aa")))
	pv := f.previewOf(21)
	for _, s := range f.envServices(pv.EnvironmentID) {
		require.NotEqual(t, services.KindStack, s.Kind, "a stack is skipped by the preview clone")
		require.NotEqual(t, "stack", s.Slug)
	}
}
