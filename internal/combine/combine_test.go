package combine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const secretA, secretB = "canary-secret-aaaa", "canary-secret-bbbb"

// fakeAgents records the commands the combine enqueues and can refuse one kind.
type fakeAgents struct {
	mu       sync.Mutex
	cmds     []agents.NewCommand
	failKind string
}

func (f *fakeAgents) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKind != "" && c.Kind == f.failKind {
		return uuid.Nil, errors.New("injected: the agent queue refused " + c.Kind)
	}
	f.cmds = append(f.cmds, c)
	return uuid.New(), nil
}

func (f *fakeAgents) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, errors.New("not used")
}

func (f *fakeAgents) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.cmds))
	for i, c := range f.cmds {
		out[i] = c.Kind
	}
	return out
}

func (f *fakeAgents) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = nil
}

// fakeDeployer creates the rows a real deployment would (a release of the
// service's staged config and a queued deployment) without running anything.
type fakeDeployer struct {
	f     *fixture
	calls []deployments.DeployOptions
	err   error
	disc  []string
	// real discards the fields for real (Abort tests).
	real func(ctx context.Context, serviceID uuid.UUID, fields []string) error
}

func (d *fakeDeployer) DeployWithOptions(ctx context.Context, id uuid.UUID, o deployments.DeployOptions) (deployments.DeploymentView, error) {
	d.calls = append(d.calls, o)
	if d.err != nil {
		return deployments.DeploymentView{}, d.err
	}
	dep := d.f.release(id, "queued")
	return deployments.DeploymentView{ID: dep.ID, ServiceID: id}, nil
}

func (d *fakeDeployer) DiscardFields(ctx context.Context, id uuid.UUID, fields []string) error {
	d.disc = append(d.disc, fields...)
	if d.real != nil {
		return d.real(ctx, id, fields)
	}
	return nil
}

// failingOps makes the config write of a start fail after it ran, to prove the
// start's transaction rolls back.
type failingOps struct {
	ServiceOps
	failUpdate bool
}

func (o failingOps) UpdateTx(ctx context.Context, tx pgx.Tx, sc services.Scope, expected int64, in services.UpdateInput) (db.Service, func(context.Context), error) {
	row, after, err := o.ServiceOps.UpdateTx(ctx, tx, sc, expected, in)
	if err == nil && o.failUpdate {
		return db.Service{}, nil, errors.New("injected: the config write failed")
	}
	return row, after, err
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	box     *secretbox.Box
	ctx     context.Context
	svc     *Service
	ag      *fakeAgents
	dep     *fakeDeployer
	svcs    *services.Service
	ws      db.Workspace
	userID  uuid.UUID
	project db.Project
	env     db.Environment
	prod    db.Environment
	server  db.Server

	shop, worker, api db.Service
	shopDomain        db.Domain
	apiDomain         db.Domain
	task              db.ServiceTask
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
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	f := &fixture{t: t, pool: pool, q: q, ws: ws, userID: u.ID, project: prj, env: env, prod: prod, ag: &fakeAgents{}}
	f.setFacts(srv.ID, agentprotocol.CapProxyV2, agentprotocol.CapProcesses)
	f.server, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	f.box, err = secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	f.svcs = services.NewService(pool, bus, f.box, f.ag, logger)
	doms := domains.NewService(pool, bus, jm, f.box)
	f.dep = &fakeDeployer{f: f}
	f.svc = NewService(Deps{
		Pool: pool, Bus: bus, Box: f.box, Jobs: jm, Services: f.svcs, Domains: doms, Deployments: f.dep, Dispatcher: f.ag, Logger: logger,
	})
	RegisterJobs(jm, f.svc) // the River client refuses to insert a job kind it has no worker for
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "ada@example.com", Role: auth.RoleOwner, Scopes: policy.AllScopes})

	// shop serves shop.example.com; its siblings run a queue worker and an API.
	f.shop = f.gitService(env.ID, "shop", services.Config{Port: 3000, StartCommand: []string{"bin/web"}})
	f.worker = f.gitService(env.ID, "shop-worker", services.Config{StartCommand: []string{"bin/worker"}})
	f.api = f.gitService(env.ID, "shop-api", services.Config{Port: 4000, StartCommand: []string{"bin/api"}})
	f.shopDomain = f.domain(f.shop.ID, "shop.example.com", 3000)
	f.apiDomain = f.domain(f.api.ID, "api.example.com", 4000)
	f.task, err = q.InsertServiceTask(bg, db.InsertServiceTaskParams{
		ServiceID: f.api.ID, Name: "digest", Schedule: "0 * * * *", Timezone: "UTC", Command: json.RawMessage(`["bin/digest"]`), Enabled: true,
		TimeoutSeconds: 60, Concurrency: "forbid",
	})
	require.NoError(t, err)
	for _, s := range []db.Service{f.shop, f.worker, f.api} {
		f.deploy(s.ID)
	}
	f.shop, f.worker, f.api = f.row(f.shop.ID), f.row(f.worker.ID), f.row(f.api.ID)
	return f
}

func (f *fixture) setFacts(serverID uuid.UUID, caps ...string) {
	f.t.Helper()
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online', facts = $2, last_seen_at = now(), base_domain = 'example.com' WHERE id = $1", serverID, facts)
	require.NoError(f.t, err)
}

func (f *fixture) gitService(envID uuid.UUID, slug string, cfg services.Config) db.Service {
	f.t.Helper()
	cfg.GitURL, cfg.GitBranch = "https://github.com/acme/shop.git", "main"
	raw, _ := json.Marshal(cfg.WithDefaults())
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceGit, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) domain(serviceID uuid.UUID, host string, port int32) db.Domain {
	f.t.Helper()
	d, err := f.q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: serviceID, Hostname: host, TargetPort: port, TlsMode: "none"})
	require.NoError(f.t, err)
	return d
}

func (f *fixture) row(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

// release inserts a release of the service's current config and a deployment
// with the given status.
func (f *fixture) release(serviceID uuid.UUID, status string) db.Deployment {
	f.t.Helper()
	bg := context.Background()
	sv := f.row(serviceID)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(sv.Config), ServerID: sv.ServerID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit, CommitSHA: strings.Repeat("a", 40)})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: serviceID, Source: src, Config: snap, ImageRef: "registry/shop:1"})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: serviceID, ReleaseID: &rel.ID, ServerID: sv.ServerID, Trigger: "manual", ActorType: "system", Strategy: "rolling", Generation: sv.Generation,
	})
	require.NoError(f.t, err)
	f.setStatus(dep.ID, status, "")
	return dep
}

func (f *fixture) setStatus(dep uuid.UUID, status, msg string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		"UPDATE deployments SET status = $2, status_message = $3, finished_at = CASE WHEN $2 IN ('succeeded','failed','canceled','rolled_back','rollback_failed') THEN now() ELSE NULL END WHERE id = $1",
		dep, status, msg)
	require.NoError(f.t, err)
}

// deploy gives a service a succeeded deployment and makes it current.
func (f *fixture) deploy(id uuid.UUID) db.Deployment {
	f.t.Helper()
	dep := f.release(id, "succeeded")
	sv := f.row(id)
	require.NoError(f.t, f.q.MarkServiceDeployed(context.Background(), db.MarkServiceDeployedParams{ID: id, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	return dep
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

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func (f *fixture) ids(ss ...db.Service) []uuid.UUID {
	out := make([]uuid.UUID, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func (f *fixture) plan(ss ...db.Service) CombinePlan {
	f.t.Helper()
	if len(ss) == 0 {
		ss = []db.Service{f.shop, f.worker, f.api}
	}
	target := f.shop.ID
	p, err := f.svc.Plan(f.ctx, f.env.ID, CombinePlanRequest{ServiceIDs: f.ids(ss...), TargetServiceID: &target})
	require.NoError(f.t, err)
	return p
}

// request is the start of a plan: the suggested process names and, for every
// conflict, the value of the first option.
func (f *fixture) request(p CombinePlan) CombineRequest {
	f.t.Helper()
	req := CombineRequest{
		Fingerprint: p.Fingerprint, TargetServiceID: p.TargetServiceID, ProcessNames: map[string]string{}, VariableChoices: map[string]string{},
	}
	for _, m := range p.Members {
		req.ServiceIDs = append(req.ServiceIDs, m.ServiceID)
		if !m.IsTarget {
			req.ProcessNames[m.ServiceID.String()] = m.SuggestedProcessName
		}
	}
	for _, c := range p.Variables.Conflicts {
		req.VariableChoices[c.Key] = c.Options[0].ServiceID.String()
	}
	return req
}

func (f *fixture) start(req CombineRequest) CombineView {
	f.t.Helper()
	v, err := f.svc.Start(f.ctx, f.env.ID, req)
	require.NoError(f.t, err)
	return v
}

func (f *fixture) combine(id uuid.UUID) db.ServiceCombine {
	f.t.Helper()
	c, err := f.q.GetCombine(context.Background(), id)
	require.NoError(f.t, err)
	return c
}

func codes(bs []CombineBlocker) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Code
	}
	return out
}

func requireAPIError(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae, "%v", err)
	require.Equal(t, code, ae.Code, ae.Message)
	require.Equal(t, status, ae.Status)
	return ae
}

func targetConfig(f *fixture) services.Config {
	return services.DecodeConfig(f.row(f.shop.ID).Config)
}

// ── plan: the precondition matrix ───────────────────────────────────────────

func TestPlanOfCompatibleServicesCanStart(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	require.Empty(t, p.Blocking, "%v", p.Blocking)
	require.NotEmpty(t, p.Fingerprint)
	require.Equal(t, f.shop.ID, p.TargetServiceID)
	require.Len(t, p.Members, 3)
	names := map[string]string{}
	for _, m := range p.Members {
		names[m.Slug] = m.SuggestedProcessName
	}
	require.Equal(t, map[string]string{"shop": "web", "shop-worker": "worker", "shop-api": "api"}, names)
	require.ElementsMatch(t, []string{"shop.example.com"}, hostsOf(p.Members, f.shop.ID))
	require.Equal(t, []DomainMove{{DomainID: f.apiDomain.ID, Hostname: "api.example.com", FromServiceID: f.api.ID, Process: "api"}}, p.DomainMoves)
	require.Equal(t, []TaskMove{{TaskID: f.task.ID, Name: "digest", FromServiceID: f.api.ID}}, p.TaskMoves)

	// The plan changes nothing.
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_combines"))
	require.Equal(t, f.shop.Generation, f.row(f.shop.ID).Generation)
}

func hostsOf(ms []Member, id uuid.UUID) []string {
	for _, m := range ms {
		if m.ServiceID == id {
			var out []string
			for _, d := range m.Domains {
				out = append(out, d.Hostname)
			}
			return out
		}
	}
	return nil
}

func TestPlanDefaultTargetHasTheMostDomains(t *testing.T) {
	f := newFixture(t)
	f.domain(f.api.ID, "api2.example.com", 4000)
	p, err := f.svc.Plan(f.ctx, f.env.ID, CombinePlanRequest{ServiceIDs: f.ids(f.shop, f.worker, f.api)})
	require.NoError(t, err)
	require.Equal(t, f.api.ID, p.TargetServiceID)
}

func TestPlanBlockers(t *testing.T) {
	cases := []struct {
		name   string
		code   string
		mutate func(f *fixture)
		// members overrides the combined services.
		members func(f *fixture) []db.Service
	}{
		{"an image service", CodeNotCombinable, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), "UPDATE services SET source_type = 'image' WHERE id = $1", f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"a database", CodeNotCombinable, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), "UPDATE services SET kind = 'database' WHERE id = $1", f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"another branch", CodeSourceMismatch, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{gitBranch}', '"develop"') WHERE id = $1`, f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"another build type", CodeSourceMismatch, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{buildType}', '"dockerfile"') WHERE id = $1`, f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"another server", CodeServerMismatch, func(f *fixture) {
			other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
			require.NoError(t, err)
			f.setFacts(other.ID, agentprotocol.CapProcesses)
			_, err = f.pool.Exec(context.Background(), "UPDATE services SET server_id = $2 WHERE id = $1", f.worker.ID, other.ID)
			require.NoError(t, err)
		}, nil},
		{"an agent without processes", CodeAgentTooOld, func(f *fixture) {
			f.setFacts(f.server.ID, agentprotocol.CapProxyV2)
		}, nil},
		{"an offline server", CodeServerUnavailable, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
			require.NoError(t, err)
		}, nil},
		{"a running deployment", CodeDeploymentInProgress, func(f *fixture) { f.release(f.worker.ID, "building") }, nil},
		{"another combine", CodeCombineInProgress, func(f *fixture) {
			_, err := f.q.InsertCombine(context.Background(), db.InsertCombineParams{
				EnvironmentID: f.env.ID, TargetServiceID: f.worker.ID, SourceServiceIds: []uuid.UUID{f.api.ID}, Plan: json.RawMessage(`{}`),
			})
			require.NoError(t, err)
		}, nil},
		{"a source with volumes", CodeSourceHasVolumes, func(f *fixture) {
			_, err := f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: f.worker.ID, Name: "data", MountPath: "/data"})
			require.NoError(t, err)
		}, nil},
		{"a service with processes", CodeAlreadyHasProcesses, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{processes}', '[{"name":"x","startCommand":["a"]}]') WHERE id = $1`, f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"a source without a start command", CodeInvalidConfig, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = config - 'startCommand' WHERE id = $1`, f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"two services on one port", CodePortConflict, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{port}', '3000') WHERE id = $1`, f.api.ID)
			require.NoError(t, err)
			_, err = f.pool.Exec(context.Background(), "UPDATE domains SET target_port = 3000 WHERE id = $1", f.apiDomain.ID)
			require.NoError(t, err)
		}, nil},
		{"a task name that exists on the target", CodeTaskNameConflict, func(f *fixture) {
			_, err := f.q.InsertServiceTask(context.Background(), db.InsertServiceTaskParams{
				ServiceID: f.shop.ID, Name: "Digest", Schedule: "0 * * * *", Timezone: "UTC", Command: json.RawMessage(`["x"]`), Enabled: true,
				TimeoutSeconds: 60, Concurrency: "forbid",
			})
			require.NoError(t, err)
		}, nil},
		{"a variable reference from outside", CodeSourceReferenced, func(f *fixture) {
			other := f.gitService(f.env.ID, "billing", services.Config{StartCommand: []string{"x"}})
			f.setVar(other.ID, "UPSTREAM", "http://${{shop-worker.NEBULA_PRIVATE_HOST}}", false)
		}, nil},
		{"a canvas link from outside", CodeSourceReferenced, func(f *fixture) {
			other := f.gitService(f.env.ID, "billing", services.Config{StartCommand: []string{"x"}})
			_, err := f.q.InsertLink(context.Background(), db.InsertLinkParams{EnvironmentID: f.env.ID, FromServiceID: other.ID, ToServiceID: f.worker.ID})
			require.NoError(t, err)
		}, nil},
		{"a source on a tailnet", CodeSourceReferenced, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{tailnet}', '{"enabled":true}') WHERE id = $1`, f.worker.ID)
			require.NoError(t, err)
		}, nil},
		{"a source domain on another port", CodeInvalidConfig, func(f *fixture) {
			_, err := f.pool.Exec(context.Background(), "UPDATE domains SET target_port = 9999 WHERE id = $1", f.apiDomain.ID)
			require.NoError(t, err)
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(f)
			p := f.plan()
			require.Contains(t, codes(p.Blocking), tc.code, "%v", p.Blocking)
			// A start refuses with that problem.
			combinesBefore := f.count("SELECT count(*) FROM service_combines") // the "another combine" case seeds one
			_, err := f.svc.Start(f.ctx, f.env.ID, f.request(p))
			require.Error(t, err)
			var ae *apierr.Error
			require.ErrorAs(t, err, &ae)
			require.Equal(t, p.Blocking[0].Code, ae.Code)
			require.Equal(t, combinesBefore, f.count("SELECT count(*) FROM service_combines"), "a refused start stages nothing")
			require.Equal(t, f.shop.Generation, f.row(f.shop.ID).Generation)
		})
	}
}

func TestPlanBlockerDetails(t *testing.T) {
	f := newFixture(t)
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{gitBranch}', '"develop"') WHERE id = $1`, f.worker.ID)
	require.NoError(t, err)
	p := f.plan()
	var b CombineBlocker
	for _, x := range p.Blocking {
		if x.Code == CodeSourceMismatch {
			b = x
		}
	}
	require.Equal(t, []string{"config.gitBranch"}, b.Fields)
	require.Equal(t, f.worker.ID, *b.ServiceID)

	other := f.gitService(f.env.ID, "billing", services.Config{StartCommand: []string{"x"}})
	f.setVar(other.ID, "UPSTREAM", "${{shop-api.PORT}}", false)
	p = f.plan()
	var ref CombineBlocker
	for _, x := range p.Blocking {
		if x.Code == CodeSourceReferenced {
			ref = x
		}
	}
	require.Len(t, ref.References, 1)
	require.Equal(t, other.ID, ref.References[0].FromServiceID)
	require.Equal(t, "variable", ref.References[0].Kind)

	// A start reports the references as details.
	_, err = f.svc.Start(f.ctx, f.env.ID, f.request(p))
	ae := requireAPIError(t, err, 422, p.Blocking[0].Code)
	require.Contains(t, ae.Details, "blocking")
}

func TestPlanRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Plan(f.ctx, f.env.ID, CombinePlanRequest{ServiceIDs: []uuid.UUID{f.shop.ID}})
	requireAPIError(t, err, 422, "validation_failed")
	_, err = f.svc.Plan(f.ctx, f.env.ID, CombinePlanRequest{ServiceIDs: []uuid.UUID{f.shop.ID, f.shop.ID}})
	requireAPIError(t, err, 422, "validation_failed")
	stranger := uuid.New()
	_, err = f.svc.Plan(f.ctx, f.env.ID, CombinePlanRequest{ServiceIDs: []uuid.UUID{f.shop.ID, f.worker.ID}, TargetServiceID: &stranger})
	requireAPIError(t, err, 422, "validation_failed")
	elsewhere := f.gitService(f.prod.ID, "shop-prod", services.Config{StartCommand: []string{"x"}})
	_, err = f.svc.Plan(f.ctx, f.env.ID, CombinePlanRequest{ServiceIDs: []uuid.UUID{f.shop.ID, elsewhere.ID}})
	requireAPIError(t, err, 422, "validation_failed")

	// Without the scopes nothing is planned.
	noWrite := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsRead}})
	_, err = f.svc.Plan(noWrite, f.env.ID, CombinePlanRequest{ServiceIDs: f.ids(f.shop, f.worker)})
	requireAPIError(t, err, 403, "forbidden")
}

// ── variables ───────────────────────────────────────────────────────────────

func TestPlanClassifiesVariablesWithoutLeakingSecrets(t *testing.T) {
	f := newFixture(t)
	f.setVar(f.shop.ID, "SAME", "x", false)
	f.setVar(f.worker.ID, "SAME", "x", false)
	f.setVar(f.api.ID, "SAME", "x", false)
	f.setVar(f.shop.ID, "APP_ENV", "production", false)
	f.setVar(f.worker.ID, "APP_ENV", "staging", false)
	f.setVar(f.api.ID, "APP_ENV", "production", false)
	f.setVar(f.worker.ID, "QUEUE", "high", false)
	f.setVar(f.api.ID, "API_KEY", secretA, true)
	f.setVar(f.worker.ID, "API_KEY", secretB, true)
	f.setVar(f.worker.ID, "TOKEN", secretA, true)
	f.setVar(f.api.ID, "TOKEN", secretA, true)
	// Two services pointing at the same sibling agree, whoever they point at.
	f.setVar(f.worker.ID, "UPSTREAM", "${{shop-api.PORT}}", false)
	f.setVar(f.shop.ID, "UPSTREAM", "${{shop.PORT}}", false)

	p := f.plan()
	require.Empty(t, p.Blocking, "%v", p.Blocking)
	vp := p.Variables

	require.Equal(t, []VariableOnly{{Key: "QUEUE", ServiceID: f.worker.ID}}, onlyKeys(vp.OnlyIn, "QUEUE"))
	token := onlyKeys(vp.OnlyIn, "TOKEN")
	require.Len(t, token, 1, "a secret two sources agree on is copied to the target once")
	require.True(t, token[0].Secret)
	require.Equal(t, 2, vp.SameCount, "SAME, and UPSTREAM once the reference to a sibling is rewritten to the target")

	conflicts := map[string]VariableConflict{}
	for _, c := range vp.Conflicts {
		conflicts[c.Key] = c
	}
	require.Len(t, conflicts, 2)
	env := conflicts["APP_ENV"]
	require.False(t, env.Secret)
	require.Len(t, env.Options, 2)
	require.Equal(t, "production", env.Options[0].Preview)
	require.Equal(t, f.shop.ID, env.Options[0].ServiceID)
	require.Equal(t, []uuid.UUID{f.api.ID}, env.Options[0].SameAs)
	require.Equal(t, "staging", env.Options[1].Preview)
	key := conflicts["API_KEY"]
	require.True(t, key.Secret)
	for _, o := range key.Options {
		require.Empty(t, o.Preview, "a secret has no preview")
	}
	require.Len(t, key.Options, 2)

	raw, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(raw), secretA)
	require.NotContains(t, string(raw), secretB)
}

func onlyKeys(o []VariableOnly, key string) []VariableOnly {
	var out []VariableOnly
	for _, v := range o {
		if v.Key == key {
			out = append(out, v)
		}
	}
	return out
}

func TestSuggestedProcessNames(t *testing.T) {
	cases := []struct {
		slug, target string
		taken        []string
		want         string
	}{
		{"shop-worker", "shop", nil, "worker"},
		{"worker", "shop", nil, "worker"},
		{"shop-worker", "shop", []string{"worker"}, "worker-2"},
		{"Shop_API.v2", "x", nil, "shop-api-v2"},
		{"shop-web", "shop", []string{"web"}, "web-2"},
		{"-", "x", nil, "worker"},
		{strings.Repeat("a", 60), "x", nil, strings.Repeat("a", 27)},
	}
	for _, c := range cases {
		taken := map[string]bool{}
		for _, n := range c.taken {
			taken[n] = true
		}
		got := suggestName(c.slug, c.target, taken)
		require.Equal(t, c.want, got, c.slug)
		require.Regexp(t, services.ProcessNamePattern, got)
	}
}

// ── start (S1) ──────────────────────────────────────────────────────────────

func TestStartStagesOnlyTheTarget(t *testing.T) {
	f := newFixture(t)
	f.setVar(f.shop.ID, "APP_ENV", "production", false)
	f.setVar(f.worker.ID, "APP_ENV", "staging", false)
	f.setVar(f.worker.ID, "QUEUE", "high", false)
	f.setVar(f.api.ID, "API_KEY", secretA, true)
	// The worker scales to three replicas; the api has autoscaling on.
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET replicas = 3 WHERE id = $1", f.worker.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET autoscaling = '{"enabled":true,"min":1,"max":4,"targetCpuPercent":60}' WHERE id = $1`, f.api.ID)
	require.NoError(t, err)

	before := map[string]db.Service{}
	beforeVars := map[string]map[string]services.RawVar{}
	for _, s := range []db.Service{f.worker, f.api} {
		before[s.Slug] = f.row(s.ID)
		beforeVars[s.Slug] = f.vars(s.ID)
	}
	p := f.plan()
	req := f.request(p)
	req.VariableChoices["APP_ENV"] = f.worker.ID.String() // keep the worker's value
	req.Message = "merge it"
	v := f.start(req)

	require.Equal(t, StateStaged, v.State)
	require.ElementsMatch(t, []uuid.UUID{f.worker.ID, f.api.ID}, v.SourceServiceIDs)
	require.Equal(t, f.shop.ID, v.TargetServiceID)
	require.Nil(t, v.DeploymentID)

	// The target carries one process per source, staged (not applied).
	tgt := f.row(f.shop.ID)
	require.Greater(t, tgt.Generation, f.shop.Generation)
	require.Equal(t, f.shop.AppliedGeneration, tgt.AppliedGeneration)
	cfg := services.DecodeConfig(tgt.Config)
	require.Equal(t, []string{"web", "api", "worker"}, cfg.ProcessNames())
	api, ok := cfg.Process("api")
	require.True(t, ok)
	require.Equal(t, 4000, api.Port)
	require.Equal(t, []string{"bin/api"}, api.Command)
	require.Equal(t, services.HealthHTTP, api.Healthcheck.Type)
	require.Equal(t, 4000, api.Healthcheck.Port)
	wk, _ := cfg.Process("worker")
	require.Zero(t, wk.Port)
	require.Equal(t, []string{"bin/worker"}, wk.Command)
	require.Equal(t, 30, wk.StopGraceSeconds, "the stop grace is the target's")
	require.Equal(t, 3000, cfg.Port, "the target's own process is untouched")
	require.Equal(t, []string{"bin/web"}, cfg.StartCommand)

	// Variables: the onlyIn key, the secret and the chosen conflict value.
	tv := f.vars(f.shop.ID)
	require.Equal(t, "high", tv["QUEUE"].Value)
	require.Equal(t, secretA, tv["API_KEY"].Value)
	require.True(t, tv["API_KEY"].Secret)
	require.Equal(t, "staging", tv["APP_ENV"].Value, "the chosen value replaces the target's")

	// Scale settings of the new processes.
	rows, err := f.q.ListProcessScales(context.Background(), f.shop.ID)
	require.NoError(t, err)
	byName := map[string]db.ServiceProcessScale{}
	for _, r := range rows {
		byName[r.Process] = r
	}
	require.Len(t, byName, 2)
	require.EqualValues(t, 3, byName["worker"].Replicas)
	var as services.Autoscaling
	require.NoError(t, json.Unmarshal(byName["api"].Autoscaling, &as))
	require.Equal(t, services.Autoscaling{Enabled: true, Min: 1, Max: 4, TargetCPUPercent: 60}, as)

	// The sources are exactly as they were; nothing was deleted, moved or sent.
	for _, s := range []db.Service{f.worker, f.api} {
		now := f.row(s.ID)
		require.Equal(t, before[s.Slug].Generation, now.Generation, s.Slug)
		require.JSONEq(t, string(before[s.Slug].Config), string(now.Config), s.Slug)
		require.Equal(t, before[s.Slug].DesiredState, now.DesiredState, s.Slug)
		require.Nil(t, now.DeletedAt)
		require.Equal(t, beforeVars[s.Slug], f.vars(s.ID), s.Slug)
	}
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1", f.api.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", f.api.ID))
	require.Empty(t, f.ag.kinds())
	require.Equal(t, 1, f.count("SELECT count(*) FROM river_job WHERE kind = 'combine_services'"))
	require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'service.combine'"))

	// The stored plan holds what abort needs and no secret.
	c := f.combine(v.ID)
	require.NotContains(t, string(c.Plan), secretA)
	var sp storedPlan
	require.NoError(t, json.Unmarshal(c.Plan, &sp))
	require.Equal(t, []string{"config.processes.api", "config.processes.worker", "variables.API_KEY", "variables.APP_ENV", "variables.QUEUE"}, sp.StagedFields)
}

func TestStartRequiresAChoiceForEveryConflict(t *testing.T) {
	f := newFixture(t)
	f.setVar(f.shop.ID, "APP_ENV", "production", false)
	f.setVar(f.worker.ID, "APP_ENV", "staging", false)
	p := f.plan()
	req := f.request(p)

	delete(req.VariableChoices, "APP_ENV")
	_, err := f.svc.Start(f.ctx, f.env.ID, req)
	ae := requireAPIError(t, err, 422, CodeVariableChoiceRequired)
	require.Equal(t, []string{"APP_ENV"}, ae.Details["keys"])

	req.VariableChoices["APP_ENV"] = f.api.ID.String() // a member that does not hold the key
	_, err = f.svc.Start(f.ctx, f.env.ID, req)
	requireAPIError(t, err, 422, CodeVariableChoiceRequired)

	req.VariableChoices["APP_ENV"] = "not-an-id"
	_, err = f.svc.Start(f.ctx, f.env.ID, req)
	requireAPIError(t, err, 422, CodeVariableChoiceRequired)
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_combines"))
	require.Equal(t, f.shop.Generation, f.row(f.shop.ID).Generation)
}

func TestStartValidatesProcessNames(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	cases := map[string]func(*CombineRequest){
		"missing":            func(r *CombineRequest) { delete(r.ProcessNames, f.api.ID.String()) },
		"not a name":         func(r *CombineRequest) { r.ProcessNames[f.api.ID.String()] = "Bad_Name" },
		"twice":              func(r *CombineRequest) { r.ProcessNames[f.api.ID.String()] = r.ProcessNames[f.worker.ID.String()] },
		"the target's name":  func(r *CombineRequest) { r.ProcessNames[f.api.ID.String()] = "web" },
		"a stranger":         func(r *CombineRequest) { r.ProcessNames[uuid.NewString()] = "other" },
		"not an id":          func(r *CombineRequest) { r.ProcessNames["x"] = "other" },
		"a bad default name": func(r *CombineRequest) { r.DefaultProcessName = "Web!" },
		"a default taken by a source": func(r *CombineRequest) {
			r.DefaultProcessName = r.ProcessNames[f.api.ID.String()]
		},
	}
	for name, mut := range cases {
		req := f.request(p)
		mut(&req)
		_, err := f.svc.Start(f.ctx, f.env.ID, req)
		requireAPIError(t, err, 422, CodeInvalidProcessNames)
		require.Equal(t, 0, f.count("SELECT count(*) FROM service_combines"), name)
	}

	// Renaming the target's own process, and naming a source "web", is allowed together.
	req := f.request(p)
	req.DefaultProcessName = "frontend"
	req.ProcessNames[f.api.ID.String()] = "web"
	f.start(req)
	cfg := targetConfig(f)
	require.Equal(t, "frontend", cfg.DefaultProcessName())
	require.Equal(t, []string{"frontend", "web", "worker"}, cfg.ProcessNames())
}

func TestStartWithAStaleFingerprintStagesNothing(t *testing.T) {
	f := newFixture(t)
	p := f.plan()
	req := f.request(p)
	f.setVar(f.worker.ID, "LATE", "1", false) // the world moved after the review
	_, err := f.svc.Start(f.ctx, f.env.ID, req)
	requireAPIError(t, err, 409, CodePlanStale)
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_combines"))
	require.Equal(t, f.shop.Generation, f.row(f.shop.ID).Generation)

	// The same plan, re-read, starts.
	f.start(f.request(f.plan()))
}

func TestFingerprintFollowsEverythingThePlanRestsOn(t *testing.T) {
	f := newFixture(t)
	base := f.plan().Fingerprint
	require.Equal(t, base, f.plan().Fingerprint, "a plan is deterministic")
	f.setVar(f.worker.ID, "A", "1", false)
	v1 := f.plan().Fingerprint
	require.NotEqual(t, base, v1)
	f.setVar(f.worker.ID, "A", "2", false)
	v2 := f.plan().Fingerprint
	require.NotEqual(t, v1, v2, "a changed value")
	f.domain(f.worker.ID, "w.example.com", 0)
	require.NotEqual(t, v2, f.plan().Fingerprint, "a new domain")
}

func TestStartRollsBackWhenAStepFails(t *testing.T) {
	f := newFixture(t)
	f.setVar(f.worker.ID, "QUEUE", "high", false)
	f.svc.Services = failingOps{ServiceOps: f.svcs, failUpdate: true}
	req := f.request(f.plan())
	_, err := f.svc.Start(f.ctx, f.env.ID, req)
	require.Error(t, err)
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_combines"))
	require.Equal(t, 0, f.count("SELECT count(*) FROM river_job WHERE kind = 'combine_services'"))
	tgt := f.row(f.shop.ID)
	require.Equal(t, f.shop.Generation, tgt.Generation)
	require.False(t, services.DecodeConfig(tgt.Config).HasProcesses())
	require.NotContains(t, f.vars(f.shop.ID), "QUEUE")
	rows, err := f.q.ListProcessScales(context.Background(), f.shop.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestStartTwiceIsRefused(t *testing.T) {
	f := newFixture(t)
	req := f.request(f.plan())
	f.start(req)
	_, err := f.svc.Start(f.ctx, f.env.ID, req)
	require.Error(t, err)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_combines"))
}

func TestStartIsGuardedInProduction(t *testing.T) {
	f := newFixture(t)
	for _, s := range []*db.Service{&f.shop, &f.worker, &f.api} {
		_, err := f.pool.Exec(context.Background(), "UPDATE services SET environment_id = $2 WHERE id = $1", s.ID, f.prod.ID)
		require.NoError(t, err)
	}
	_, err := f.pool.Exec(context.Background(), "UPDATE domains SET hostname = hostname || '.prod'")
	require.NoError(t, err)
	p, err := f.svc.Plan(f.ctx, f.prod.ID, CombinePlanRequest{ServiceIDs: f.ids(f.shop, f.worker, f.api), TargetServiceID: &f.shop.ID})
	require.NoError(t, err)
	req := f.request(p)

	// An MCP principal needs a person's approval; none is installed here, and the
	// guard fails closed.
	mcp := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeMCP, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "agent", Scopes: policy.AllScopes,
	})
	_, err = f.svc.Start(mcp, f.prod.ID, req)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Code, "approval", "%v", err)
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_combines"))

	// A person in the browser is not guarded.
	_, err = f.svc.Start(f.ctx, f.prod.ID, req)
	require.NoError(t, err)
}

// ── deploy and decide (S2, S3) ──────────────────────────────────────────────

func (f *fixture) started() (CombineView, CombineRequest) {
	f.t.Helper()
	req := f.request(f.plan())
	return f.start(req), req
}

func (f *fixture) advance(id uuid.UUID) {
	f.t.Helper()
	_, err := f.svc.advance(context.Background(), id)
	require.NoError(f.t, err)
}

func TestTheJobStartsOneDeploymentOfTheTarget(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)

	c := f.combine(v.ID)
	require.Equal(t, StateDeploying, c.State)
	require.NotNil(t, c.DeploymentID)
	require.Len(t, f.dep.calls, 1)
	require.Equal(t, v.ID, f.dep.calls[0].ByCombine, "the combine's deployment passes the combine guard")
	require.Contains(t, f.dep.calls[0].Message, "Combine services: shop, shop-api, shop-worker")

	// While it runs, the job waits and does nothing else.
	wait, err := f.svc.advance(context.Background(), v.ID)
	require.NoError(t, err)
	require.Positive(t, wait)
	require.Len(t, f.dep.calls, 1, "one deployment, however often the job looks")
	require.Empty(t, f.ag.kinds())
	require.Equal(t, 1, f.count("SELECT count(*) FROM deployments WHERE service_id = $1 AND status = 'queued'", f.shop.ID))
}

func TestAFailedDeploymentLeavesTheSourcesUntouched(t *testing.T) {
	for _, status := range []string{"failed", "rolled_back", "rollback_failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			v, _ := f.started()
			f.advance(v.ID)
			c := f.combine(v.ID)
			f.setStatus(*c.DeploymentID, status, "worker: health check failed")
			f.advance(v.ID)

			c = f.combine(v.ID)
			require.Equal(t, StateFailed, c.State)
			require.Contains(t, c.Error, "worker: health check failed")
			require.NotNil(t, c.FinishedAt)

			// Nothing was deleted, nothing moved, nothing was sent to the agent.
			for _, s := range []db.Service{f.worker, f.api} {
				require.Nil(t, f.row(s.ID).DeletedAt, s.Slug)
			}
			require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1 AND component = ''", f.api.ID))
			require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1", f.shop.ID))
			require.Equal(t, 1, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", f.api.ID))
			require.Empty(t, f.ag.kinds())
			// The target keeps its staged changes.
			require.True(t, services.DecodeConfig(f.row(f.shop.ID).Config).HasProcesses())
			require.Equal(t, f.shop.AppliedGeneration, f.row(f.shop.ID).AppliedGeneration)
		})
	}
}

func TestSourcesStoppedFirstStartAgainWhenTheDeploymentFails(t *testing.T) {
	f := newFixture(t)
	req := f.request(f.plan())
	req.StopSourcesFirst = true
	v := f.start(req)

	// Only the source without a domain stops.
	var sp storedPlan
	require.NoError(t, json.Unmarshal(f.combine(v.ID).Plan, &sp))
	require.Equal(t, []uuid.UUID{f.worker.ID}, sp.StoppedServiceIDs)
	require.Equal(t, services.DesiredStopped, f.row(f.worker.ID).DesiredState)
	require.Equal(t, services.DesiredRunning, f.row(f.api.ID).DesiredState, "a source that serves a domain keeps serving")
	require.Equal(t, []string{agentprotocol.KindStopService}, f.ag.kinds())

	f.advance(v.ID)
	require.Equal(t, []string{agentprotocol.KindStopService}, f.ag.kinds(), "the job does not stop it twice")
	f.setStatus(*f.combine(v.ID).DeploymentID, "failed", "boom")
	f.advance(v.ID)

	require.Equal(t, StateFailed, f.combine(v.ID).State)
	require.Equal(t, services.DesiredRunning, f.row(f.worker.ID).DesiredState, "the job starts it again")
	require.Equal(t, []string{agentprotocol.KindStopService, agentprotocol.KindRestartService}, f.ag.kinds())
}

func TestADeploymentThatCannotStartFailsTheCombine(t *testing.T) {
	f := newFixture(t)
	f.dep.err = apierr.Unprocessable("no_server", "the service has no server")
	v, _ := f.started()
	f.advance(v.ID)
	c := f.combine(v.ID)
	require.Equal(t, StateFailed, c.State)
	require.Equal(t, "the service has no server", c.Error)
	require.Nil(t, c.DeploymentID)
}

func TestACrashedStepAdoptsTheRunningDeployment(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	// The deployment exists, but the combine never learned about it.
	dep := f.release(f.shop.ID, "building")
	f.dep.err = apierr.Conflict("deployment_in_progress", "a deployment is already in progress for this service")
	f.advance(v.ID)
	c := f.combine(v.ID)
	require.Equal(t, StateDeploying, c.State)
	require.Equal(t, dep.ID, *c.DeploymentID)
}

// ── cutover (S4) ────────────────────────────────────────────────────────────

// succeed finishes the combine's deployment: the target's release now holds
// the processes.
func (f *fixture) succeed(id uuid.UUID) {
	f.t.Helper()
	c := f.combine(id)
	require.NotNil(f.t, c.DeploymentID)
	// A real deployment's release is made of the config at its start; the fake
	// deployer's queued deployment already holds it.
	f.setStatus(*c.DeploymentID, "succeeded", "")
	sv := f.row(f.shop.ID)
	require.NoError(f.t, f.q.MarkServiceDeployed(context.Background(), db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: c.DeploymentID, AppliedGeneration: sv.Generation}))
}

func TestCutoverMovesDomainsAndTasksAndDeletesTheSources(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	f.succeed(v.ID)
	f.ag.reset()
	f.advance(v.ID)

	c := f.combine(v.ID)
	require.Equal(t, StateDone, c.State)
	require.NotNil(t, c.FinishedAt)

	// Domains: the api's hostname now routes to the target's api process.
	var comp string
	var svc uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT component, service_id FROM domains WHERE id = $1", f.apiDomain.ID).Scan(&comp, &svc))
	require.Equal(t, "api", comp)
	require.Equal(t, f.shop.ID, svc)
	require.Equal(t, 0, f.count("SELECT count(*) FROM domains WHERE service_id IN ($1, $2)", f.api.ID, f.worker.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE id = $1 AND component = ''", f.shopDomain.ID), "the target's own domain is untouched")
	// The release snapshot knows the moved domain: no phantom pending change.
	rel, ok, err := services.AppliedRelease(context.Background(), f.q, f.row(f.shop.ID))
	require.NoError(t, err)
	require.True(t, ok)
	var hosts []string
	for _, d := range services.DecodeSnapshot(rel.Config).Domains {
		hosts = append(hosts, d.Hostname)
	}
	require.Contains(t, hosts, "api.example.com")
	require.Equal(t, f.row(f.shop.ID).Generation, f.row(f.shop.ID).AppliedGeneration)

	// Tasks moved; the sources are gone, their volumes kept (no delete-volumes).
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1 AND name = 'digest'", f.shop.ID))
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", f.api.ID))
	for _, s := range []db.Service{f.worker, f.api} {
		var deleted bool
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL FROM services WHERE id = $1", s.ID).Scan(&deleted))
		require.True(t, deleted, s.Slug)
	}

	// The route table goes to the server before the containers of the sources
	// are removed, so every hostname flips at once.
	kinds := f.ag.kinds()
	require.Equal(t, []string{agentprotocol.KindSyncRoutes, agentprotocol.KindRemoveService, agentprotocol.KindRemoveService}, kinds)
	for _, cmd := range f.ag.cmds[1:] {
		var pl agentprotocol.ServiceActionPayload
		require.NoError(t, json.Unmarshal(cmd.Payload, &pl))
		require.False(t, pl.DeleteVolumes)
	}
	var table agentprotocol.SyncRoutesPayload
	require.NoError(t, json.Unmarshal(f.ag.cmds[0].Payload, &table))
	var routed []string
	for _, r := range table.Routes {
		for _, h := range r.Hosts {
			routed = append(routed, h)
		}
	}
	require.Contains(t, routed, "api.example.com")
	require.Contains(t, routed, "shop.example.com")

	// Audit: each delete names the combine, and the combine is audited once more.
	require.Equal(t, 2, f.count(`SELECT count(*) FROM audit_events WHERE action = 'service.delete' AND metadata->>'combine' = $1`, v.ID.String()))
	require.Equal(t, 2, f.count("SELECT count(*) FROM audit_events WHERE action = 'service.combine'"))

	// Running the job again changes nothing.
	f.ag.reset()
	f.advance(v.ID)
	require.Empty(t, f.ag.kinds())
	require.Equal(t, StateDone, f.combine(v.ID).State)
}

func TestCutoverIsOneTransaction(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	f.succeed(v.ID)

	// The agent queue refuses the second step of the cutover: the removal of a source.
	f.ag.failKind = agentprotocol.KindRemoveService
	f.ag.reset()
	_, err := f.svc.advance(context.Background(), v.ID)
	require.Error(t, err)

	// Nothing of it happened: not the domains, not the tasks, not one delete.
	require.Equal(t, StateCutover, f.combine(v.ID).State)
	require.Equal(t, f.api.ID, f.domainOwner(f.apiDomain.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", f.api.ID))
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_tasks WHERE service_id = $1", f.shop.ID))
	for _, s := range []db.Service{f.worker, f.api} {
		require.Nil(t, f.row(s.ID).DeletedAt, s.Slug)
	}
	require.Equal(t, 1, f.count("SELECT count(*) FROM domains WHERE service_id = $1", f.shop.ID))
	rel, _, err := services.AppliedRelease(context.Background(), f.q, f.row(f.shop.ID))
	require.NoError(t, err)
	require.Empty(t, services.DecodeSnapshot(rel.Config).Domains, "the release snapshot is not touched either")
	require.Zero(t, f.count("SELECT count(*) FROM audit_events WHERE action = 'service.delete'"))

	// Once the queue works the River retry finishes it.
	f.ag.failKind = ""
	f.advance(v.ID)
	require.Equal(t, StateDone, f.combine(v.ID).State)
	require.Equal(t, f.shop.ID, f.domainOwner(f.apiDomain.ID))
}

func (f *fixture) domainOwner(id uuid.UUID) uuid.UUID {
	f.t.Helper()
	var svc uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT service_id FROM domains WHERE id = $1", id).Scan(&svc))
	return svc
}

func TestCutoverWaitsForABusyMember(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	f.succeed(v.ID)
	f.release(f.worker.ID, "building") // someone deployed a source meanwhile

	wait, err := f.svc.advance(context.Background(), v.ID)
	require.NoError(t, err)
	require.Equal(t, busyPoll, wait)
	require.Equal(t, StateCutover, f.combine(v.ID).State)
	require.Nil(t, f.row(f.worker.ID).DeletedAt)
	require.Empty(t, f.ag.kinds())
}

func TestCutoverNeedsTheReleaseToRunEveryProcess(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	// The deployment "succeeded", but the current release is an older one
	// without the processes (the deployment was superseded).
	f.setStatus(*f.combine(v.ID).DeploymentID, "succeeded", "")
	f.advance(v.ID)
	c := f.combine(v.ID)
	require.Equal(t, StateFailed, c.State)
	require.Contains(t, c.Error, "does not run every process")
	require.Nil(t, f.row(f.worker.ID).DeletedAt)
	require.Empty(t, f.ag.kinds())
}

func TestARetryAfterAFailedCutoverSkipsTheDeployment(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	f.succeed(v.ID)

	// The cutover keeps failing until River gives up.
	f.ag.failKind = agentprotocol.KindRemoveService
	_, err := f.svc.advance(context.Background(), v.ID)
	require.Error(t, err)
	f.svc.giveUp(context.Background(), v.ID, err)
	c := f.combine(v.ID)
	require.Equal(t, StateFailed, c.State)
	require.Contains(t, c.Error, "gave up")
	require.Nil(t, f.row(f.api.ID).DeletedAt)

	// An abort is refused: the new release already runs the processes.
	_, err = f.svc.Abort(f.ctx, v.ID)
	requireAPIError(t, err, 409, CodeInvalidState)

	f.ag.failKind = ""
	rv, err := f.svc.Retry(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StateStaged, rv.State)
	f.advance(v.ID)
	require.Equal(t, StateDone, f.combine(v.ID).State)
	require.Len(t, f.dep.calls, 1, "the release already runs every process: no second deployment")
}

// ── retry and abort (S3, S5) ────────────────────────────────────────────────

func TestRetryDeploysTheSameStagedConfigurationAgain(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	f.setStatus(*f.combine(v.ID).DeploymentID, "failed", "worker never became healthy")
	f.advance(v.ID)
	require.Equal(t, StateFailed, f.combine(v.ID).State)
	staged := f.row(f.shop.ID)

	rv, err := f.svc.Retry(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StateStaged, rv.State)
	require.Empty(t, rv.Error)
	require.Nil(t, rv.FinishedAt)
	require.Equal(t, 2, f.count("SELECT count(*) FROM river_job WHERE kind = 'combine_services'"))
	require.Equal(t, staged.Generation, f.row(f.shop.ID).Generation, "the staged configuration is reused")

	f.advance(v.ID)
	require.Len(t, f.dep.calls, 2)
	require.Equal(t, StateDeploying, f.combine(v.ID).State)
	f.succeed(v.ID)
	f.advance(v.ID)
	require.Equal(t, StateDone, f.combine(v.ID).State)

	// A finished combine cannot be retried again.
	_, err = f.svc.Retry(f.ctx, v.ID)
	requireAPIError(t, err, 409, CodeInvalidState)
}

func TestAbortRestoresTheTarget(t *testing.T) {
	f := newFixture(t)
	f.setVar(f.worker.ID, "QUEUE", "high", false)
	f.setVar(f.shop.ID, "APP_ENV", "production", false)
	f.setVar(f.worker.ID, "APP_ENV", "staging", false)
	req := f.request(f.plan())
	req.StopSourcesFirst = true
	req.VariableChoices["APP_ENV"] = f.worker.ID.String()
	v := f.start(req)
	require.Equal(t, services.DesiredStopped, f.row(f.worker.ID).DesiredState)
	require.Equal(t, "staging", f.vars(f.shop.ID)["APP_ENV"].Value)

	// Abort goes through the real discard machinery of the staged fields.
	f.dep.real = func(ctx context.Context, id uuid.UUID, fields []string) error {
		return f.discard(ctx, id, fields)
	}
	av, err := f.svc.Abort(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StateAborted, av.State)
	require.NotNil(t, av.FinishedAt)

	require.ElementsMatch(t, []string{"config.processes.api", "config.processes.worker", "variables.APP_ENV", "variables.QUEUE"}, f.dep.disc)
	require.Equal(t, services.DesiredRunning, f.row(f.worker.ID).DesiredState, "the stopped source starts again")
	require.Contains(t, f.ag.kinds(), agentprotocol.KindRestartService)
	rows, err := f.q.ListProcessScales(context.Background(), f.shop.ID)
	require.NoError(t, err)
	require.Empty(t, rows, "the scale rows the combine created are gone")
	require.Zero(t, f.count("SELECT count(*) FROM river_job WHERE kind = 'combine_services' AND state = 'running'"))

	// A second abort, or a retry, of an aborted combine is refused.
	_, err = f.svc.Abort(f.ctx, v.ID)
	requireAPIError(t, err, 409, CodeInvalidState)
	_, err = f.svc.Retry(f.ctx, v.ID)
	requireAPIError(t, err, 409, CodeInvalidState)
	// The job finds nothing to do.
	f.advance(v.ID)
	require.Empty(t, f.dep.calls)
	require.Equal(t, StateAborted, f.combine(v.ID).State)
}

// discard takes the staged fields of a combine off the target: the processes
// leave the config and the variables are deleted (the release had none of them).
func (f *fixture) discard(ctx context.Context, id uuid.UUID, fields []string) error {
	f.t.Helper()
	cfg := services.DecodeConfig(f.row(id).Config)
	for _, fld := range fields {
		switch {
		case strings.HasPrefix(fld, "config.processes."):
			name := strings.TrimPrefix(fld, "config.processes.")
			cfg.Processes = slices.DeleteFunc(slices.Clone(cfg.Processes), func(p services.ProcessConfig) bool { return p.Name == name })
		case strings.HasPrefix(fld, "variables."):
			key := strings.TrimPrefix(fld, "variables.")
			if _, err := f.pool.Exec(ctx, "DELETE FROM service_variables WHERE service_id = $1 AND key = $2", id, key); err != nil {
				return err
			}
		}
	}
	raw, _ := json.Marshal(cfg)
	_, err := f.pool.Exec(ctx, "UPDATE services SET config = $2 WHERE id = $1", id, raw)
	return err
}

func TestAbortIsRefusedWhileDeployingAndAfterDone(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.advance(v.ID)
	_, err := f.svc.Abort(f.ctx, v.ID)
	ae := requireAPIError(t, err, 409, CodeInvalidState)
	require.Equal(t, StateDeploying, ae.Details["state"])

	f.succeed(v.ID)
	f.advance(v.ID)
	require.Equal(t, StateDone, f.combine(v.ID).State)
	_, err = f.svc.Abort(f.ctx, v.ID)
	requireAPIError(t, err, 409, CodeInvalidState)
	require.Empty(t, f.dep.disc, "nothing was discarded")
}

func TestAbortBeforeTheJobRunsStopsTheDeployment(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	f.dep.real = func(ctx context.Context, id uuid.UUID, fields []string) error { return f.discard(ctx, id, fields) }
	_, err := f.svc.Abort(f.ctx, v.ID)
	require.NoError(t, err)
	// The queued job wakes up late: the combine is aborted, nothing deploys.
	f.advance(v.ID)
	require.Empty(t, f.dep.calls)
	require.False(t, services.DecodeConfig(f.row(f.shop.ID).Config).HasProcesses())
}

// ── reads and authorization ─────────────────────────────────────────────────

func TestGetCombineIsScopedToTheTeam(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, v.ID, got.ID)
	require.Equal(t, StateStaged, got.State)

	_, err = f.svc.Get(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")

	otherWS, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	stranger := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: otherWS.ID, Scopes: policy.AllScopes})
	_, err = f.svc.Get(stranger, v.ID)
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.Abort(stranger, v.ID)
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.Retry(stranger, v.ID)
	requireAPIError(t, err, 404, "not_found")

	// Mutations need both write scopes.
	deployOnly := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}})
	_, err = f.svc.Abort(deployOnly, v.ID)
	requireAPIError(t, err, 403, "forbidden")
	_, err = f.svc.Start(deployOnly, f.env.ID, f.request(f.plan()))
	requireAPIError(t, err, 403, "forbidden")
}

func TestAServiceInAnActiveCombineTakesNoDeploymentOfItsOwn(t *testing.T) {
	f := newFixture(t)
	v, _ := f.started()
	for _, s := range []db.Service{f.shop, f.worker, f.api} {
		n, err := f.q.CountActiveCombinesForService(context.Background(), s.ID)
		require.NoError(t, err)
		require.EqualValues(t, 1, n, s.Slug)
	}
	f.advance(v.ID)
	f.setStatus(*f.combine(v.ID).DeploymentID, "failed", "x")
	f.advance(v.ID)
	n, err := f.q.CountActiveCombinesForService(context.Background(), f.worker.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, n, "a failed combine stops blocking its services: it is retried or aborted")
}

func TestAbortOfANeverDeployedTargetRestoresItsConfigAndVariables(t *testing.T) {
	f := newFixture(t)
	// shop was never deployed: there is no release the discard could revert to.
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET current_deployment_id = NULL, applied_generation = 0 WHERE id = $1", f.shop.ID)
	require.NoError(t, err)
	f.setVar(f.shop.ID, "APP_ENV", "production", false)
	f.setVar(f.shop.ID, "SESSION", secretA, true)
	f.setVar(f.worker.ID, "APP_ENV", "staging", false)
	f.setVar(f.worker.ID, "SESSION", secretB, true)
	f.setVar(f.worker.ID, "QUEUE", "high", false)
	cfgBefore := services.DecodeConfig(f.row(f.shop.ID).Config)
	varsBefore := f.vars(f.shop.ID)

	req := f.request(f.plan())
	req.DefaultProcessName = "frontend"
	req.VariableChoices["APP_ENV"] = f.worker.ID.String()
	req.VariableChoices["SESSION"] = f.worker.ID.String()
	v := f.start(req)
	staged := f.vars(f.shop.ID)
	require.Equal(t, "staging", staged["APP_ENV"].Value)
	require.Equal(t, secretB, staged["SESSION"].Value)
	require.Equal(t, "high", staged["QUEUE"].Value)
	require.True(t, services.DecodeConfig(f.row(f.shop.ID).Config).HasProcesses())
	c := f.combine(v.ID)
	require.NotContains(t, string(c.Plan), secretA, "the saved originals are sealed")
	require.NotContains(t, string(c.Plan), secretB)

	av, err := f.svc.Abort(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StateAborted, av.State)
	require.Empty(t, f.dep.disc, "no release: the discard machinery is not used")

	require.Equal(t, cfgBefore, services.DecodeConfig(f.row(f.shop.ID).Config), "the target's config is exactly as before the start")
	require.Equal(t, varsBefore, f.vars(f.shop.ID), "overwritten variables are back, created ones are gone")
	rows, err := f.q.ListProcessScales(context.Background(), f.shop.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}
