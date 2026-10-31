package configsync

// DB-backed tests of Service.Plan and Service.Validate. Live state is built through the real
// use cases (services, variables, domains, databases, routes, tasks, scaling, uptime), the
// ownership rows (config_sources, environment_config_settings.owned_shared) the way an apply
// records them, and every plan is compared field by field.
//
// The package is tested from inside (package configsync) because the drift tests record the
// fingerprints the plan computed as the "base" of a previous apply.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/uptime"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---------------------------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------------------------

const (
	planHead       = "schema = 1\nproject = \"shop\"\n"
	planHeadServer = planHead + "server = \"agent1\"\n"
	planCompose    = "services:\n  web:\n    image: nginx:1.27\n"
	planComposeKey = "compose = '''\nservices:\n  web:\n    image: nginx:1.27\n'''\n"
)

// planFixture is a team with a project (production and staging), a restricted online server,
// a git connection, a backup destination and a shared network.
type planFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	box     *secretbox.Box
	ws      db.Workspace
	user    db.CreateUserRow
	project db.Project
	staging db.Environment
	prod    db.Environment
	server  db.Server
	conn    db.GitConnection
	dest    db.BackupDestination
	net     db.SharedNetwork

	svcs   *services.Service
	vars   *variables.Service
	doms   *domains.Service
	dbs    *databases.Service
	routes *proxyroutes.Service
	scal   *scaling.Service
	tsk    *tasks.Service
	upt    *uptime.Service
	cfg    *Service

	ctx context.Context // an owner holding every scope
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	databases.RegisterExports()
	t.Cleanup(func() { services.RegisterExportProvider(nil) })
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

	ip := "203.0.113.9"
	server, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1", PublicIp: &ip})
	require.NoError(t, err)
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', facts = $2 WHERE id = $1", server.ID, facts)
	require.NoError(t, err)

	conn, err := q.InsertGitConnection(bg, db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Provider: "gitea", Name: "github-acme", BaseUrl: "https://git.example.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	dest, err := q.InsertBackupDestination(bg, db.InsertBackupDestinationParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "offsite-s3", Kind: "local", LocalDir: "offsite", CompatMode: true,
	})
	require.NoError(t, err)
	net, err := q.InsertSharedNetwork(bg, db.InsertSharedNetworkParams{WorkspaceID: ws.ID, ServerID: server.ID, Name: "backplane"})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, nil, logger)
	vars := variables.NewService(pool, bus, box)
	jm := jobs.New(pool, logger)
	doms := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, doms, logger)
	dbs := databases.NewService(pool, bus, box, nil, svcs, logger)
	routes := proxyroutes.NewService(pool, bus, jm, nil, svcs, logger)
	svcs.SetRouteHook(routes)
	nets := networks.NewService(pool, bus)
	scal := scaling.NewService(pool, bus, nil, svcs, logger)
	tsk := tasks.NewService(tasks.Deps{Pool: pool, Bus: bus, Box: box, Logger: logger})
	upt := uptime.NewService(uptime.Deps{Pool: pool, Bus: bus, Logger: logger})
	cfg := NewService(Deps{
		Pool: pool, Bus: bus, Box: box, Logger: logger,
		Services: svcs, Variables: vars, Domains: doms, Databases: dbs, Routes: routes, Networks: nets,
		Scaling: scal, Tasks: tsk, Uptime: upt,
		CloneURL: func(_ context.Context, _ uuid.UUID, repo string) (string, string, error) {
			return "https://git.example.com/" + repo + ".git", "", nil
		},
	})

	return &planFixture{
		t: t, pool: pool, q: q, box: box, ws: ws, user: user, project: project, staging: staging, prod: prod,
		server: server, conn: conn, dest: dest, net: net,
		svcs: svcs, vars: vars, doms: doms, dbs: dbs, routes: routes, scal: scal, tsk: tsk, upt: upt, cfg: cfg,
		ctx: auth.WithPrincipal(bg, &auth.Principal{
			Type: auth.TypeUser, UserID: user.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "a@example.com", Scopes: policy.AllScopes,
		}),
	}
}

// ctxWith is a context of a token of the team holding exactly scopes.
func (f *planFixture) ctxWith(scopes ...string) context.Context {
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.user.ID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper, Label: "token", Scopes: scopes,
	})
}

func (f *planFixture) gen(id uuid.UUID) int64 {
	f.t.Helper()
	var g int64
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT generation FROM services WHERE id = $1", id).Scan(&g))
	return g
}

func (f *planFixture) sourceType(id uuid.UUID) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT source_type FROM services WHERE id = $1", id).Scan(&s))
	return s
}

func (f *planFixture) config(id uuid.UUID) services.Config {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT config FROM services WHERE id = $1", id).Scan(&raw))
	return services.DecodeConfig(raw)
}

// editConfig is a dashboard edit of the config of a service.
func (f *planFixture) editConfig(id uuid.UUID, mutate func(c *services.Config)) {
	f.t.Helper()
	cfg := f.config(id)
	mutate(&cfg)
	_, err := f.svcs.Update(f.ctx, id, f.gen(id), services.UpdateInput{Config: &cfg})
	require.NoError(f.t, err)
}

// image creates a staged image service (nginx:1.27) on the fixture's server.
func (f *planFixture) image(env db.Environment, name string, port int) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceImage, ServerID: &f.server.ID,
		Config: services.Config{Image: "nginx:1.27", Port: port},
	})
	require.NoError(f.t, err)
	return v
}

// gitService creates a git service of acme/shop linked through the fixture's connection.
func (f *planFixture) gitService(env db.Environment, name string) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, env.ID, services.CreateInput{
		Name: name, SourceType: services.SourceGit, ServerID: &f.server.ID,
		Config: services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 8080},
	})
	require.NoError(f.t, err)
	_, err = f.q.UpsertServiceGit(context.Background(), db.UpsertServiceGitParams{
		ServiceID: v.ID, ConnectionID: f.conn.ID, RepoFullName: "acme/shop", AutoDeploy: true, WatchPaths: []string{},
	})
	require.NoError(f.t, err)
	return v
}

// database creates a staged postgres 17 database (database "shop", user "shop").
func (f *planFixture) database(env db.Environment, slug string) services.ServiceView {
	f.t.Helper()
	v, err := f.dbs.Create(f.ctx, databases.CreateInput{
		EnvironmentID: env.ID, Name: slug, Slug: slug, Engine: "postgres", Version: "17", DatabaseName: "shop", Username: "shop", ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return v
}

// route creates an http route to one upstream address with the health path "/".
func (f *planFixture) route(env db.Environment, slug, upstream string) services.ServiceView {
	f.t.Helper()
	ups := []proxyroutes.UpstreamInput{{Address: upstream}}
	v, err := f.routes.Create(f.ctx, env.ID, proxyroutes.CreateInput{
		Name: slug, Slug: slug, ServerID: f.server.ID, Protocol: "http",
		RouteFields: proxyroutes.RouteFields{Upstreams: &ups, HealthPath: new("/")},
	})
	require.NoError(f.t, err)
	return v
}

// stack creates a compose stack service (planCompose).
func (f *planFixture) stack(env db.Environment, name string, wait int) services.ServiceView {
	f.t.Helper()
	v, err := f.svcs.Create(f.ctx, env.ID, services.CreateInput{
		Name: name, Kind: services.KindStack, SourceType: services.SourceCompose, ServerID: &f.server.ID,
		Config: services.Config{Stack: &services.StackConfig{Compose: planCompose, WaitSeconds: wait}},
	})
	require.NoError(f.t, err)
	return v
}

func (f *planFixture) setVar(id uuid.UUID, key, value string, secret bool) {
	f.t.Helper()
	in := variables.VariableInput{Key: key, Value: new(value)}
	if secret {
		in.IsSecret = new(true)
	}
	_, err := f.vars.Put(f.ctx, id, []variables.VariableInput{in})
	require.NoError(f.t, err)
}

func (f *planFixture) setShared(layer string, owner uuid.UUID, key, value string, secret bool) {
	f.t.Helper()
	in := variables.SharedVariableInput{Key: key, Value: new(value)}
	if secret {
		in.IsSecret = new(true)
	}
	_, err := f.vars.PutShared(f.ctx, layer, owner, []variables.SharedVariableInput{in})
	require.NoError(f.t, err)
}

func (f *planFixture) setUptime(id uuid.UUID, reminder int) {
	f.t.Helper()
	_, err := f.upt.UpdateMonitor(f.ctx, id, uptime.MonitorView{Enabled: true, Alerts: true, FailureThreshold: 3, RecoveryThreshold: 2, ReminderMinutes: reminder})
	require.NoError(f.t, err)
}

func (f *planFixture) setReplicas(id uuid.UUID, n int) {
	f.t.Helper()
	_, err := f.scal.Set(f.ctx, id, scaling.SetInput{Replicas: &n})
	require.NoError(f.t, err)
}

func (f *planFixture) task(id uuid.UUID, name, schedule string, command ...string) tasks.TaskView {
	f.t.Helper()
	v, err := f.tsk.CreateTask(f.ctx, id, tasks.TaskInput{
		Name: name, Schedule: schedule, Timezone: "UTC", Command: command, TimeoutSeconds: 1800, Concurrency: "forbid",
	})
	require.NoError(f.t, err)
	return v
}

// backup creates a nightly schedule of a database; key "" leaves the config key unset (made in the dashboard).
func (f *planFixture) backup(id uuid.UUID, key, cron string) db.BackupSchedule {
	f.t.Helper()
	row, err := f.q.InsertBackupSchedule(context.Background(), db.InsertBackupScheduleParams{
		WorkspaceID: f.ws.ID, ServiceID: id, DestinationID: f.dest.ID, Cron: cron, Timezone: "UTC", Enabled: true, RetentionCount: 7,
	})
	require.NoError(f.t, err)
	if key != "" {
		require.NoError(f.t, f.q.SetBackupScheduleConfigKey(context.Background(), db.SetBackupScheduleConfigKeyParams{ID: row.ID, ConfigKey: &key}))
	}
	return row
}

// recordSource writes the config_sources row of a service the way an apply does.
func (f *planFixture) recordSource(id uuid.UUID, env db.Environment, owned map[string]string) {
	f.t.Helper()
	_, err := f.q.UpsertConfigSource(context.Background(), db.UpsertConfigSourceParams{
		ServiceID: id, EnvironmentID: env.ID, WorkspaceID: f.ws.ID, Origin: OriginCLI, FileSha256: "recorded-by-the-test",
		SchemaVersion: 1, Owned: ownedJSON(owned), Pending: json.RawMessage("{}"), AppliedGeneration: f.gen(id),
		AppliedByType: "user", AppliedByID: f.user.ID.String(), AppliedByLabel: "a@example.com",
	})
	require.NoError(f.t, err)
}

// markApplied records file as applied to env with the live state as it is now: every field the
// file owns gets the fingerprint of its desired value as the base (what a real apply writes).
func (f *planFixture) markApplied(env db.Environment, file string) {
	f.t.Helper()
	p, err := f.cfg.plan(f.ctx, f.cfg.q, f.project.ID, PlanRequest{Environment: env.Slug, Input: Input{File: file}}, policy.ScopeProjectsRead)
	require.NoError(f.t, err)
	require.False(f.t, p.diags.HasErrors(), "the file to mark as applied must be valid: %v", p.diags)
	for _, sp := range p.svcs {
		if sp.ls == nil {
			continue
		}
		own := map[string]string{}
		for _, fp := range sp.fields {
			if fp.skipped {
				continue
			}
			own[fp.fv.ID] = fp.desired.FP
			if fp.desired.Proj != "" {
				own[fp.fv.ID+auxSuffix] = fp.desired.Proj
			}
		}
		f.recordSource(sp.ls.Row.ID, env, own)
	}
	shared := map[string]string{}
	for _, sh := range p.shared {
		for _, fp := range sh.fields {
			if !fp.skipped {
				shared[fp.fv.ID] = fp.desired.FP
			}
		}
	}
	if len(shared) > 0 {
		require.NoError(f.t, f.q.SetEnvironmentConfigOwnedShared(context.Background(), db.SetEnvironmentConfigOwnedSharedParams{
			EnvironmentID: env.ID, WorkspaceID: f.ws.ID, OwnedShared: ownedJSON(shared),
		}))
	}
}

func (f *planFixture) planReq(env db.Environment, file string) PlanRequest {
	return PlanRequest{Environment: env.Slug, Input: Input{File: file}}
}

func (f *planFixture) plan(env db.Environment, file string) Plan {
	f.t.Helper()
	return f.planWith(f.planReq(env, file))
}

func (f *planFixture) planWith(req PlanRequest) Plan {
	f.t.Helper()
	p, err := f.cfg.Plan(f.ctx, f.project.ID, req)
	require.NoError(f.t, err)
	return p
}

// snapshot counts the rows Plan must never change.
func (f *planFixture) snapshot() map[string]string {
	f.t.Helper()
	out := map[string]string{}
	for _, tbl := range []string{
		"services", "service_variables", "shared_variables", "config_sources", "environment_config_settings", "audit_events",
		"domains", "volumes", "service_tasks", "backup_schedules", "uptime_monitors", "service_links", "service_networks", "service_git",
	} {
		var n int64
		require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+tbl).Scan(&n))
		out[tbl] = fmt.Sprint(n)
	}
	var g int64
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT coalesce(sum(generation), 0)::bigint FROM services").Scan(&g))
	out["generation"] = fmt.Sprint(g)
	var cfgs string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT md5(coalesce(string_agg(config::text, '|' ORDER BY id), '')) FROM services").Scan(&cfgs))
	out["configs"] = cfgs
	var owned string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT md5(coalesce(string_agg(owned::text || pending::text, '|' ORDER BY service_id), '')) FROM config_sources").Scan(&owned))
	out["owned"] = owned
	return out
}

// auditText is the metadata of every audit row, for leak scans.
func (f *planFixture) auditText() string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT coalesce(string_agg(action || ' ' || metadata::text, ' '), '') FROM audit_events").Scan(&s))
	return s
}

// ---------------------------------------------------------------------------------------------
// assertion helpers
// ---------------------------------------------------------------------------------------------

// planWant is the expected form of one PlanChange.
type planWant struct {
	Path, Field, Op, Applies, Reason, From, To string
	Line                                       int
	Masked                                     bool
}

func requireChanges(t *testing.T, got []PlanChange, want []planWant) {
	t.Helper()
	require.Len(t, got, len(want), "changes: %v", planFieldsOf(got))
	for i, w := range want {
		g := got[i]
		require.Equal(t, w.Field, g.Field, "change %d", i)
		if w.Path != "" {
			require.Equal(t, w.Path, g.Path, "%s path", g.Field)
		}
		require.Equal(t, w.Op, g.Op, "%s op", g.Field)
		require.Equal(t, w.Applies, g.Applies, "%s applies", g.Field)
		require.Equal(t, w.Reason, g.Reason, "%s reason", g.Field)
		require.Equal(t, w.From, g.From, "%s from", g.Field)
		require.Equal(t, w.To, g.To, "%s to", g.Field)
		require.Equal(t, w.Masked, g.Masked, "%s masked", g.Field)
		if w.Line != 0 {
			require.Equal(t, w.Line, g.Line, "%s line", g.Field)
		}
	}
}

func planFieldsOf(cs []PlanChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Field
	}
	return out
}

func planSlugs(p Plan) []string {
	out := make([]string, len(p.Services))
	for i, s := range p.Services {
		out[i] = s.Slug
	}
	return out
}

func planSvc(t *testing.T, p Plan, slug string) PlanService {
	t.Helper()
	for _, s := range p.Services {
		if s.Slug == slug {
			return s
		}
	}
	t.Fatalf("the plan has no service %q (it has %v); diagnostics: %v", slug, planSlugs(p), p.Diagnostics)
	return PlanService{}
}

func planChangeOf(t *testing.T, ps PlanService, field string) PlanChange {
	t.Helper()
	for _, c := range ps.Changes {
		if c.Field == field {
			return c
		}
	}
	t.Fatalf("service %s has no change of %s (it has %v)", ps.Slug, field, planFieldsOf(ps.Changes))
	return PlanChange{}
}

func planDiag(t *testing.T, ds Diagnostics, code string) Diagnostic {
	t.Helper()
	for _, d := range ds {
		if d.Code == code {
			return d
		}
	}
	t.Fatalf("no diagnostic %q in %v", code, ds)
	return Diagnostic{}
}

func planJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// planLine is the 1-based line of the first line of file containing needle.
func planLine(t *testing.T, file, needle string) int {
	t.Helper()
	for i, l := range strings.Split(file, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("no line with %q in:\n%s", needle, file)
	return 0
}

func planWebHead(port string) string {
	return planHead + "\n[services.web]\nimage = \"nginx:1.27\"\nport = " + port + "\n"
}

func planSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------------------------
// plan per kind: create
// ---------------------------------------------------------------------------------------------

func TestPlanDB_CreateImageService(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.cache]
image = "redis:7"
port = 6379

[services.cache.variables]
LOG_LEVEL = "info"
SESSION_KEY = "${{secret(48)}}"

[services.cache.scale]
replicas = 2
`
	p := f.plan(f.staging, file)

	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, 1, p.Schema)
	require.True(t, strings.HasPrefix(p.PlanID, "pl_"), p.PlanID)
	require.Len(t, p.PlanID, 3+32)
	require.Equal(t, PlanProject{ID: f.project.ID, Slug: "shop"}, p.Project)
	require.Equal(t, PlanEnvironment{ID: f.staging.ID, Slug: "staging", IsProduction: false}, p.Environment)
	require.Equal(t, PlanSource{Kind: SourceInline, FileSHA256: planSHA(file), Bytes: len(file)}, p.Source)
	require.Empty(t, p.Diagnostics)
	require.Empty(t, p.Shared.Project)
	require.Empty(t, p.Shared.Environment)
	require.Equal(t, []string{"projects:write", "deployments:write"}, p.RequiredScopes)
	require.Equal(t, Guard{ProductionApproval: ApprovalNotRequired}, p.Guard)
	require.Equal(t, PlanSummary{Create: 1, Staged: 5, Immediate: 1, RequiresDeploy: []string{"cache"}}, p.Summary)

	require.Len(t, p.Services, 1)
	ps := p.Services[0]
	require.Equal(t, "cache", ps.Slug)
	require.Equal(t, "image", ps.Kind)
	require.Equal(t, ActionCreate, ps.Action)
	require.Equal(t, StateNew, ps.State)
	require.Nil(t, ps.ServiceID)
	require.False(t, ps.Managed)
	require.Empty(t, ps.Blockers)
	require.Empty(t, ps.Released)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.image", Path: "services.cache.image", Line: planLine(t, file, "image = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "redis:7"},
		{Field: "config.port", Path: "services.cache.port", Line: planLine(t, file, "port = 6379"), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "6379"},
		{Field: "scale.replicas", Path: "services.cache.scale.replicas", Line: planLine(t, file, "replicas = 2"), Op: OpAdd, Applies: AppliesImmediate, Reason: ReasonNew, To: "2"},
		{Field: "server", Path: "server", Line: planLine(t, file, "server = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "agent1"},
		{Field: "variables.LOG_LEVEL", Path: "services.cache.variables.LOG_LEVEL", Line: planLine(t, file, "LOG_LEVEL = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "info"},
		{Field: "variables.SESSION_KEY", Path: "services.cache.variables.SESSION_KEY", Line: planLine(t, file, "SESSION_KEY = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "generated(secret(48))", Masked: true},
	})
	require.NotContains(t, planJSON(t, p), "${{secret", "the text of a generator never reaches the plan, only its descriptor")
}

func TestPlanDB_CreateGitServiceFromURL(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.site]
port = 3000

[services.site.git]
url = "https://git.example.com/acme/site.git"
branch = "main"
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "site")
	require.Equal(t, "git", ps.Kind)
	require.Equal(t, ActionCreate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.gitBranch", Path: "services.site.git.branch", Line: planLine(t, file, "branch = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "main"},
		{Field: "config.gitUrl", Path: "services.site.git.url", Line: planLine(t, file, "url = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "https://git.example.com/acme/site.git"},
		{Field: "config.port", Path: "services.site.port", Line: planLine(t, file, "port = 3000"), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "3000"},
		{Field: "server", Path: "server", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "agent1"},
	})
	require.Equal(t, []string{"site"}, p.Summary.RequiresDeploy)
	require.Equal(t, []string{"projects:write"}, p.RequiredScopes)
}

// A git service created from a connection and repository names no clone URL: the plan must
// still be valid (the connection carries the repository) and list the immediate link.
func TestPlanDB_CreateGitServiceFromConnection(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.web]
port = 8080

[services.web.git]
connection = "github-acme"
repo = "acme/shop"
branch = "main"
auto_deploy = true
watch_paths = ["apps/web/**"]
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "a git service from a connection is a valid create: %v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionCreate, ps.Action)
	link := planChangeOf(t, ps, "git.link")
	require.Equal(t, AppliesImmediate, link.Applies)
	require.Equal(t, OpAdd, link.Op)
	require.Equal(t, "github-acme:acme/shop", link.To)
	require.Equal(t, "services.web.git.connection", link.Path)
	require.Equal(t, AppliesImmediate, planChangeOf(t, ps, "git.auto_deploy").Applies)
	wp := planChangeOf(t, ps, "git.watch_paths")
	require.Equal(t, AppliesImmediate, wp.Applies)
	require.Equal(t, `["apps/web/**"]`, wp.To)
}

func TestPlanDB_CreateDatabase(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.db.database]
engine = "postgres"
version = "17"
name = "shop"
username = "shop"

[services.db.backups.nightly]
destination = "offsite-s3"
cron = "0 2 * * *"
retention_count = 14
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "db")
	require.Equal(t, "database", ps.Kind)
	require.Equal(t, ActionCreate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "backups.nightly", Path: "services.db.backups.nightly", Op: OpAdd, Applies: AppliesImmediate, Reason: ReasonNew, To: "0 2 * * * UTC → offsite-s3"},
		{Field: "config.database.databaseName", Path: "services.db.database.name", Line: planLine(t, file, "name = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "shop"},
		{Field: "config.database.engine", Path: "services.db.database.engine", Line: planLine(t, file, "engine = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "postgres"},
		{Field: "config.database.username", Path: "services.db.database.username", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "shop"},
		{Field: "config.database.version", Path: "services.db.database.version", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "17"},
		{Field: "server", Path: "server", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "agent1"},
	})
	require.Equal(t, PlanSummary{Create: 1, Staged: 5, Immediate: 1, RequiresDeploy: []string{"db"}}, p.Summary)
	require.Equal(t, []string{"projects:write", "deployments:write"}, p.RequiredScopes, "a backup schedule needs deployments:write")
}

func TestPlanDB_CreateRoute(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.printer]
name = "Office printer"

[services.printer.route]
protocol = "http"
upstreams = [{ address = "192.168.1.20:631" }]
health_path = "/"
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "printer")
	require.Equal(t, "route", ps.Kind)
	require.Equal(t, ActionCreate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.route.healthPath", Path: "services.printer.route.health_path", Line: planLine(t, file, "health_path"), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "/"},
		{Field: "config.route.protocol", Path: "services.printer.route.protocol", Line: planLine(t, file, "protocol = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "http"},
		{Field: "config.route.upstreams", Path: "services.printer.route.upstreams", Line: planLine(t, file, "upstreams = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: `[{"address":"192.168.1.20:631"}]`},
		{Field: "name", Path: "services.printer.name", Line: planLine(t, file, "name = "), Op: OpAdd, Applies: AppliesImmediate, Reason: ReasonNew, To: "Office printer"},
		{Field: "server", Path: "server", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "agent1"},
	})
	require.Equal(t, []string{"projects:write", "proxy:write"}, p.RequiredScopes, "publishing a route needs proxy:write")
	require.Equal(t, PlanSummary{Create: 1, Staged: 4, Immediate: 1, RequiresDeploy: []string{"printer"}}, p.Summary)
}

func TestPlanDB_CreateStack(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.shop.stack]
` + planComposeKey + `wait_seconds = 120
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "shop")
	require.Equal(t, "stack", ps.Kind)
	require.Equal(t, ActionCreate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.stack.compose", Path: "services.shop.stack.compose", Line: planLine(t, file, "compose = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: planCompose},
		{Field: "config.stack.waitSeconds", Path: "services.shop.stack.wait_seconds", Line: planLine(t, file, "wait_seconds"), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "120"},
		{Field: "server", Path: "server", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "agent1"},
	})
}

// ---------------------------------------------------------------------------------------------
// plan per kind: unchanged and update of existing services
// ---------------------------------------------------------------------------------------------

const planAllKindsFile = planHeadServer + `
[services.web]
image = "nginx:1.27"
port = 8080

[services.api]
port = 8080

[services.api.git]
connection = "github-acme"
repo = "acme/shop"
branch = "main"

[services.db.database]
engine = "postgres"
version = "17"
name = "shop"
username = "shop"

[services.printer.route]
protocol = "http"
upstreams = [{ address = "192.168.1.20:631" }]
health_path = "/"

[services.shop.stack]
` + planComposeKey + `wait_seconds = 120
`

func (f *planFixture) allKinds() {
	f.image(f.staging, "web", 8080)
	f.gitService(f.staging, "api")
	f.database(f.staging, "db")
	f.route(f.staging, "printer", "192.168.1.20:631")
	f.stack(f.staging, "shop", 120)
}

func TestPlanDB_ExistingServicesUnchangedPerKind(t *testing.T) {
	f := newPlanFixture(t)
	f.allKinds()

	p := f.plan(f.staging, planAllKindsFile)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"api", "db", "printer", "shop", "web"}, planSlugs(p))
	kinds := map[string]string{"api": "git", "db": "database", "printer": "route", "shop": "stack", "web": "image"}
	for _, ps := range p.Services {
		require.Equal(t, kinds[ps.Slug], ps.Kind, ps.Slug)
		require.Equal(t, ActionUnchanged, ps.Action, "%s: %v", ps.Slug, planFieldsOf(ps.Changes))
		require.Equal(t, StateInSync, ps.State, ps.Slug)
		require.Empty(t, ps.Changes, ps.Slug)
		require.NotNil(t, ps.ServiceID, ps.Slug)
		require.False(t, ps.Managed, ps.Slug)
	}
	require.Equal(t, 5, p.Summary.Unchanged)
	require.Zero(t, p.Summary.Create+p.Summary.Update+p.Summary.Staged+p.Summary.Immediate)
	require.Empty(t, p.Summary.RequiresDeploy)
	require.Equal(t, []string{"projects:write"}, p.RequiredScopes)
}

func TestPlanDB_ExistingServicesUpdatePerKind(t *testing.T) {
	f := newPlanFixture(t)
	f.allKinds()
	file := strings.NewReplacer(
		"port = 8080\n\n[services.api]", "port = 9090\n\n[services.api]", // web
		`branch = "main"`, `branch = "develop"
watch_paths = ["apps/api/**"]`,
		"192.168.1.20:631\" }]", "192.168.1.21:631\" }]",
		"wait_seconds = 120", "wait_seconds = 200",
	).Replace(planAllKindsFile)
	require.NotEqual(t, planAllKindsFile, file)

	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"api", "db", "printer", "shop", "web"}, planSlugs(p))

	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "config.port", Path: "services.web.port", Line: planLine(t, file, "port = 9090"), Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "8080", To: "9090"},
	})
	requireChanges(t, planSvc(t, p, "api").Changes, []planWant{
		{Field: "config.gitBranch", Path: "services.api.git.branch", Line: planLine(t, file, "branch = "), Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "main", To: "develop"},
		{Field: "git.watch_paths", Path: "services.api.git.watch_paths", Line: planLine(t, file, "watch_paths"), Op: OpSet, Applies: AppliesImmediate, Reason: ReasonAdopt, From: "[]", To: `["apps/api/**"]`},
	})
	requireChanges(t, planSvc(t, p, "printer").Changes, []planWant{
		{Field: "config.route.upstreams", Path: "services.printer.route.upstreams", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt,
			From: `[{"address":"192.168.1.20:631"}]`, To: `[{"address":"192.168.1.21:631"}]`},
	})
	requireChanges(t, planSvc(t, p, "shop").Changes, []planWant{
		{Field: "config.stack.waitSeconds", Path: "services.shop.stack.wait_seconds", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "120", To: "200"},
	})
	require.Empty(t, planSvc(t, p, "db").Changes)

	for _, slug := range []string{"api", "printer", "shop", "web"} {
		ps := planSvc(t, p, slug)
		require.Equal(t, ActionUpdate, ps.Action, slug)
		require.Equal(t, StateAdopt, ps.State, "a first-apply difference keeps the adopt state: %s", slug)
	}
	require.Equal(t, ActionUnchanged, planSvc(t, p, "db").Action)
	require.Equal(t, PlanSummary{Update: 4, Unchanged: 1, Adopted: 4, Staged: 4, Immediate: 1, RequiresDeploy: []string{"api", "printer", "shop", "web"}}, p.Summary)
	require.Equal(t, []string{"projects:write", "proxy:write"}, p.RequiredScopes)
}

func TestPlanDB_ImageServiceDeclaredAsGitIsAStagedSourceSwitch(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planHeadServer + "\n[services.web]\nport = 8080\n\n[services.web.git]\nurl = \"https://git.example.com/acme/shop.git\"\nbranch = \"main\"\n"
	gen := f.gen(web.ID)

	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, "git", ps.Kind)
	require.Equal(t, ActionUpdate, ps.Action)
	require.ElementsMatch(t, []string{"source", "config.gitBranch", "config.gitUrl"}, planFieldsOf(ps.Changes))
	src := ps.Changes[0]
	require.Equal(t, "source", src.Field, "the switch comes first")
	require.Equal(t, "services.web.git", src.Path)
	require.NotZero(t, src.Line)
	require.Equal(t, OpSet, src.Op)
	require.Equal(t, AppliesStaged, src.Applies)
	require.Equal(t, "image", src.From)
	require.Equal(t, "git", src.To)
	require.Equal(t, []string{"web"}, p.Summary.RequiresDeploy)
	require.Equal(t, gen, f.gen(web.ID), "planning writes nothing")
}

func TestPlanDB_GitServiceDeclaredAsImageIsAStagedSourceSwitch(t *testing.T) {
	f := newPlanFixture(t)
	api := f.gitService(f.staging, "api")
	file := planHeadServer + "\n[services.api]\nimage = \"nginx:1.27\"\nport = 8080\n"

	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "api")
	require.Equal(t, "image", ps.Kind)
	require.Equal(t, ActionUpdate, ps.Action)
	src := ps.Changes[0]
	require.Equal(t, "source", src.Field)
	require.Equal(t, "services.api.image", src.Path)
	require.Equal(t, "git", src.From)
	require.Equal(t, "image", src.To)
	require.Equal(t, services.SourceGit, f.sourceType(api.ID), "planning writes nothing")
}

// ---------------------------------------------------------------------------------------------
// blocked
// ---------------------------------------------------------------------------------------------

func TestPlanDB_BlockedServicesContributeNoChanges(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	f.stack(f.staging, "shop", 120)
	// Two servers and two connections whose names differ only in case: a name must be unique.
	for range 2 {
		_, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "twin"})
		require.NoError(t, err)
	}
	for _, name := range []string{"Acme", "acme"} {
		_, err := f.q.InsertGitConnection(context.Background(), db.InsertGitConnectionParams{
			ID: uuid.New(), WorkspaceID: f.ws.ID, Provider: "gitea", Name: name, BaseUrl: "https://git.example.com", AuthType: "token",
			Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
		})
		require.NoError(t, err)
	}

	routeBody := "[services.printer.route]\nprotocol = \"http\"\nupstreams = [{ address = \"192.168.1.20:631\" }]\n"
	cases := []struct {
		name, file, slug, code string
	}{
		{"image: unknown server", planHead + "\n[services.web]\nimage = \"nginx:1.27\"\nserver = \"ghost\"\n", "web", "unknown_server"},
		{"image: ambiguous server", planHead + "\n[services.web]\nimage = \"nginx:1.27\"\nserver = \"twin\"\n", "web", "ambiguous_reference"},
		{"image: unknown shared network", planHeadServer + "\n[services.web]\nimage = \"nginx:1.27\"\nnetworks = [\"nope\"]\n", "web", "unknown_network"},
		{"image: depends_on a service that exists nowhere", planHeadServer + "\n[services.web]\nimage = \"nginx:1.27\"\ndepends_on = [\"ghost\"]\n", "web", "unknown_service_reference"},
		{"git: unknown connection", planHeadServer + "\n[services.site]\n[services.site.git]\nconnection = \"nope\"\nrepo = \"acme/site\"\nbranch = \"main\"\n", "site", "unknown_connection"},
		{"git: ambiguous connection", planHeadServer + "\n[services.site]\n[services.site.git]\nconnection = \"ACME\"\nrepo = \"acme/site\"\nbranch = \"main\"\n", "site", "ambiguous_reference"},
		{"git: unknown registry", planHeadServer + "\n[services.site]\n[services.site.git]\nurl = \"https://git.example.com/acme/site.git\"\nbranch = \"main\"\n[services.site.build]\nregistry = \"nope\"\n", "site", "unknown_registry"},
		{"database: unknown destination", planHeadServer + "\n[services.db.database]\nengine = \"postgres\"\n[services.db.backups.nightly]\ndestination = \"nope\"\ncron = \"0 2 * * *\"\n", "db", "unknown_destination"},
		{"stack: unknown shared network", planHeadServer + "\n[services.shop]\nnetworks = [\"nope\"]\n[services.shop.stack]\n" + planComposeKey, "shop", "unknown_network"},
		{"route: unknown server", planHead + "\n[services.printer]\nserver = \"ghost\"\n" + routeBody, "printer", "unknown_server"},
		{"image service declared as a database", planHeadServer + "\n[services.web.database]\nengine = \"postgres\"\nversion = \"17\"\n", "web", "source_type_change"},
		{"stack service declared as an image", planHeadServer + "\n[services.shop]\nimage = \"nginx:1.27\"\n", "shop", "source_type_change"},
		{"stack service declared as git", planHeadServer + "\n[services.shop.git]\nurl = \"https://git.example.com/acme/shop.git\"\nbranch = \"main\"\n", "shop", "source_type_change"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			require.False(t, p.Valid)
			require.GreaterOrEqual(t, p.Summary.Errors, 1)
			require.Zero(t, p.Summary.Create+p.Summary.Update+p.Summary.Unchanged, "a blocked service is none of create, update, unchanged")
			require.Zero(t, p.Summary.Staged+p.Summary.Immediate, "a blocked service contributes no changes")
			d := planDiag(t, p.Diagnostics, c.code)
			require.Equal(t, "error", d.Severity)
			require.NotZero(t, d.Line, "the diagnostic points into the file: %+v", d)
			ps := planSvc(t, p, c.slug)
			require.Equal(t, ActionBlocked, ps.Action)
			require.Empty(t, ps.Changes)
			require.Contains(t, ps.Blockers, c.code)
		})
	}
}

// The checks that run against the merged value (what the platform would refuse when writing).
func planCheckCases(f *planFixture) []struct {
	name, file, slug, code, pathHas string
} {
	f.image(f.staging, "web", 8080)
	other := f.image(f.staging, "other", 8080)
	_, err := f.doms.Create(f.ctx, other.ID, "taken.example.com", 0, domains.RuleInput{})
	require.NoError(f.t, err)
	store := f.image(f.staging, "store", 8080)
	_, err = f.svcs.AddVolume(f.ctx, store.ID, "data", "/data")
	require.NoError(f.t, err)
	f.database(f.staging, "db")
	f.route(f.staging, "printer", "192.168.1.20:631")

	return []struct{ name, file, slug, code, pathHas string }{
		{"docker_unrestricted on a restricted server", planHead + "\n[services.web]\nimage = \"nginx:1.27\"\n[services.web.docker]\nprivileged = true\n", "web", "docker_unrestricted", "services.web.docker.privileged"},
		{"immutable database version", planHead + "\n[services.db.database]\nengine = \"postgres\"\nversion = \"16\"\n", "db", "immutable_field", "services.db.database.version"},
		{"immutable route protocol", planHeadServer + "\n[services.printer.route]\nprotocol = \"udp\"\nlisten_port = 25565\nopen_to_internet = true\nupstreams = [{ address = \"192.168.1.20:631\" }]\n", "printer", "immutable_field", "services.printer.route.protocol"},
		{"hostname_in_use", planHead + "\n[services.web]\nimage = \"nginx:1.27\"\nport = 8080\n[services.web.domains.\"taken.example.com\"]\ntarget_port = 8080\n", "web", "hostname_in_use", "taken.example.com"},
		{"replicas_require_stateless (live volume)", planHead + "\n[services.store]\nimage = \"nginx:1.27\"\nport = 8080\n[services.store.scale]\nreplicas = 2\n", "store", "replicas_require_stateless", "services.store.scale.replicas"},
		{"egress_blocked (live block_egress)", planHead + "\n[services.db]\nnetworks = [\"backplane\"]\n[services.db.database]\nengine = \"postgres\"\n", "db", "egress_blocked", "services.db.networks"},
	}
}

func TestPlanDB_ChecksReportErrorsAtTheKeyThatCausesThem(t *testing.T) {
	f := newPlanFixture(t)
	for _, c := range planCheckCases(f) {
		t.Run(c.name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			require.False(t, p.Valid)
			d := planDiag(t, p.Diagnostics, c.code)
			require.Equal(t, "error", d.Severity)
			require.Contains(t, d.Path, c.pathHas)
			require.NotZero(t, d.Line)
			require.GreaterOrEqual(t, p.Summary.Errors, 1)
		})
	}
}

func TestPlanDB_ChecksBlockTheService(t *testing.T) {
	f := newPlanFixture(t)
	for _, c := range planCheckCases(f) {
		t.Run(c.name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			ps := planSvc(t, p, c.slug)
			require.Equal(t, ActionBlocked, ps.Action, "an error under a service's key blocks it")
			require.Contains(t, ps.Blockers, c.code)
			require.Empty(t, ps.Changes, "a blocked service contributes no changes")
		})
	}
}

// ---------------------------------------------------------------------------------------------
// not in file, adoption
// ---------------------------------------------------------------------------------------------

func TestPlanDB_NotInFileWithAndWithoutPriorManagement(t *testing.T) {
	f := newPlanFixture(t)
	legacy := f.image(f.staging, "legacy", 8080)
	f.image(f.staging, "adhoc", 8080)
	f.recordSource(legacy.ID, f.staging, map[string]string{"config.port": projectconfig.Fingerprint(8080)})

	p := f.plan(f.staging, planHead)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"legacy", "adhoc"}, planSlugs(p), "live services in creation order")
	for _, ps := range p.Services {
		require.Equal(t, ActionNotInFile, ps.Action)
		require.Equal(t, "image", ps.Kind)
		require.NotNil(t, ps.ServiceID)
		require.Equal(t, "kept; remove it in the dashboard", ps.Note)
		require.NotNil(t, ps.Changes)
		require.Empty(t, ps.Changes)
	}
	require.True(t, p.Services[0].Managed, "a service with an apply row was managed by a file")
	require.False(t, p.Services[1].Managed)
	require.Equal(t, 2, p.Summary.NotInFile)
	require.Zero(t, p.Summary.Create+p.Summary.Update+p.Summary.Unchanged)
}

func TestPlanDB_NotInFileNamesTheLiveKind(t *testing.T) {
	f := newPlanFixture(t)
	f.allKinds()
	p := f.plan(f.staging, planHead)
	got := map[string]string{}
	for _, ps := range p.Services {
		require.Equal(t, ActionNotInFile, ps.Action)
		got[ps.Slug] = ps.Kind
	}
	require.Equal(t, map[string]string{"web": "image", "api": "git", "db": "database", "printer": "route", "shop": "stack"}, got)
}

func TestPlanDB_NotInFileFollowsTheServicesOfTheFile(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	f.image(f.staging, "adhoc", 8080)
	p := f.plan(f.staging, planWebHead("8080"))
	require.Equal(t, []string{"web", "adhoc"}, planSlugs(p))
	require.Equal(t, ActionUnchanged, p.Services[0].Action)
	require.Equal(t, ActionNotInFile, p.Services[1].Action)
	require.Equal(t, 1, p.Summary.Unchanged)
	require.Equal(t, 1, p.Summary.NotInFile)
}

func TestPlanDB_AdoptExistingService(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planHead + `
[services.web]
image = "nginx:1.28"
port = 9090

[services.web.variables]
NEW_VAR = "1"
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUpdate, ps.Action)
	require.False(t, ps.Managed)
	require.Equal(t, StateAdopt, ps.State)
	require.Equal(t, web.ID, *ps.ServiceID)
	require.Equal(t, f.gen(web.ID), ps.Generation)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.image", Path: "services.web.image", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "nginx:1.27", To: "nginx:1.28"},
		{Field: "config.port", Path: "services.web.port", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "8080", To: "9090"},
		{Field: "variables.NEW_VAR", Path: "services.web.variables.NEW_VAR", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "1"},
	})
	require.Equal(t, 1, p.Summary.Adopted)
	require.Equal(t, 1, p.Summary.Update)
	require.Equal(t, []string{"web"}, p.Summary.RequiresDeploy)

	// Once an apply recorded the file, the same service is managed and no longer adopted.
	f.recordSource(web.ID, f.staging, map[string]string{})
	p = f.plan(f.staging, file)
	require.Equal(t, 0, p.Summary.Adopted)
	require.True(t, planSvc(t, p, "web").Managed)
}

func TestPlanDB_MovingAServiceToAnotherServerIsAStagedChange(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	_, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	p := f.plan(f.staging, planWebHead("8080")+"server = \"agent2\"\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)
	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "server", Path: "services.web.server", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "agent1", To: "agent2"},
	})
}

// ---------------------------------------------------------------------------------------------
// immutable and project errors
// ---------------------------------------------------------------------------------------------

func TestPlanDB_ImmutableFieldsOfExistingServices(t *testing.T) {
	f := newPlanFixture(t)
	f.database(f.staging, "db")
	f.route(f.staging, "printer", "192.168.1.20:631")
	for name, c := range map[string]struct{ file, path string }{
		"database engine": {
			planHeadServer + "\n[services.db.database]\nengine = \"mysql\"\nversion = \"8.4\"\nname = \"shop\"\nusername = \"shop\"\n",
			"services.db.database.engine",
		},
		"database version": {planHeadServer + "\n[services.db.database]\nengine = \"postgres\"\nversion = \"16\"\n", "services.db.database.version"},
		"database name":    {planHeadServer + "\n[services.db.database]\nengine = \"postgres\"\nname = \"other\"\n", "services.db.database.name"},
		"route protocol": {
			planHeadServer + "\n[services.printer.route]\nprotocol = \"tcp\"\nlisten_port = 2222\nopen_to_internet = true\nupstreams = [{ address = \"192.168.1.20:22\" }]\n",
			"services.printer.route.protocol",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			require.False(t, p.Valid)
			found := false
			for _, d := range p.Diagnostics {
				if d.Code == "immutable_field" && d.Path == c.path {
					found = true
					require.Equal(t, "error", d.Severity)
					require.NotZero(t, d.Line)
				}
			}
			require.True(t, found, "immutable_field at %s in %v", c.path, p.Diagnostics)
		})
	}
}

func TestPlanDB_ProjectMismatchIsABlocker(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	file := "schema = 1\nproject = \"other\"\n\n[services.web]\nimage = \"nginx:1.27\"\n"
	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "project_mismatch")
	require.Equal(t, "error", d.Severity)
	require.Equal(t, "project", d.Path)
	require.Equal(t, planLine(t, file, "project = "), d.Line)
	require.Empty(t, p.Services, "a plan with file errors lists no services")
	require.Equal(t, 1, p.Summary.Errors)
}

// ---------------------------------------------------------------------------------------------
// three-way drift
// ---------------------------------------------------------------------------------------------

type planDriftCase struct {
	name    string
	field   string
	path    string // "" = not asserted
	applies string
	file    func(v string) string // the complete file with the value v
	a, b, c string                // file value at the last apply (= live), live value after a dashboard edit, newer file value
	show    func(v string) string // how the plan renders v (default v)
	loose   bool                  // compare renderings with Contains
	setup   func(f *planFixture, id uuid.UUID)
	edit    func(f *planFixture, id uuid.UUID)
	// undeclared edits a part of the live value the file does not declare; it must not drift.
	undeclared func(f *planFixture, id uuid.UUID)
	// extra is TOML appended to file(a) for a member that does not exist live.
	extra, extraField, extraTo string
}

func planDriftCases() []planDriftCase {
	var domainID, taskID uuid.UUID
	headers := func(v string) []agentprotocol.Header { return []agentprotocol.Header{{Name: "X-Env", Value: v}} }
	editDomain := func(f *planFixture, id uuid.UUID, rule domains.RuleInput) {
		f.t.Helper()
		_, _, err := f.doms.Update(f.ctx, domainID, f.gen(id), domains.UpdateInput{Rule: rule})
		require.NoError(f.t, err)
	}
	return []planDriftCase{
		{
			name: "config scalar", field: "config.port", path: "services.web.port", applies: AppliesStaged,
			file: func(v string) string { return planWebHead(v) },
			a:    "8080", b: "7000", c: "9090",
			setup: func(*planFixture, uuid.UUID) {},
			edit:  func(f *planFixture, id uuid.UUID) { f.editConfig(id, func(c *services.Config) { c.Port = 7000 }) },
		},
		{
			name: "nested health check key", field: "config.healthcheck.path", path: "services.web.health_check.path", applies: AppliesStaged,
			file: func(v string) string {
				return planWebHead("8080") + "\n[services.web.health_check]\npath = \"" + v + "\"\n"
			},
			a: "/healthz", b: "/live", c: "/ready",
			setup: func(f *planFixture, id uuid.UUID) {
				f.editConfig(id, func(c *services.Config) { c.Healthcheck.Path = "/healthz" })
			},
			edit: func(f *planFixture, id uuid.UUID) {
				f.editConfig(id, func(c *services.Config) { c.Healthcheck.Path = "/live" })
			},
		},
		{
			name: "variable", field: "variables.LOG_LEVEL", path: "services.web.variables.LOG_LEVEL", applies: AppliesStaged,
			file: func(v string) string {
				return planWebHead("8080") + "\n[services.web.variables]\nLOG_LEVEL = \"" + v + "\"\n"
			},
			a: "info", b: "debug", c: "warn",
			setup: func(f *planFixture, id uuid.UUID) { f.setVar(id, "LOG_LEVEL", "info", false) },
			edit:  func(f *planFixture, id uuid.UUID) { f.setVar(id, "LOG_LEVEL", "debug", false) },
			extra: "EXTRA = \"x\"\n", extraField: "variables.EXTRA", extraTo: "x",
		},
		{
			name: "domain (declared keys only)", field: "domains.shop.example.com", applies: AppliesStaged, loose: true,
			file: func(v string) string {
				return planWebHead("8080") + "\n[services.web.domains.\"shop.example.com\".options]\ncompress = true\nrequest_headers = { X-Env = \"" + v + "\" }\n"
			},
			a: "alpha", b: "bravo", c: "charlie",
			setup: func(f *planFixture, id uuid.UUID) {
				v, err := f.doms.Create(f.ctx, id, "shop.example.com", 0, domains.RuleInput{
					Options: &agentprotocol.RouteOptions{Compress: true, RequestHeaders: headers("alpha")},
				})
				require.NoError(f.t, err)
				domainID = v.ID
			},
			edit: func(f *planFixture, id uuid.UUID) {
				editDomain(f, id, domains.RuleInput{Options: &agentprotocol.RouteOptions{Compress: true, RequestHeaders: headers("bravo")}})
			},
			undeclared: func(f *planFixture, id uuid.UUID) { editDomain(f, id, domains.RuleInput{AddPrefix: new("/v1")}) },
			extra:      "\n[services.web.domains.\"api.example.com\".options]\ncompress = true\n", extraField: "domains.api.example.com", extraTo: "compress",
		},
		{
			name: "task", field: "tasks.digest", path: "services.web.tasks.digest", applies: AppliesImmediate,
			file: func(v string) string {
				return planWebHead("8080") + "\n[services.web.tasks.digest]\nschedule = \"" + v + "\"\ncommand = [\"echo\", \"a\"]\n"
			},
			a: "0 7 * * *", b: "0 8 * * *", c: "0 9 * * *",
			show: func(v string) string { return v + ` UTC ["echo","a"]` },
			setup: func(f *planFixture, id uuid.UUID) {
				taskID = f.task(id, "digest", "0 7 * * *", "echo", "a").ID
			},
			edit: func(f *planFixture, id uuid.UUID) {
				_, err := f.tsk.UpdateTask(f.ctx, taskID, tasks.TaskPatch{Schedule: new("0 8 * * *")})
				require.NoError(f.t, err)
			},
			extra: "\n[services.web.tasks.report]\nschedule = \"0 5 * * *\"\ncommand = [\"echo\", \"r\"]\n", extraField: "tasks.report", extraTo: `0 5 * * * UTC ["echo","r"]`,
		},
		{
			name: "uptime", field: "uptime.reminderMinutes", path: "services.web.uptime.reminder_minutes", applies: AppliesImmediate,
			file: func(v string) string {
				return planWebHead("8080") + "\n[services.web.uptime]\nreminder_minutes = " + v + "\n"
			},
			a: "30", b: "45", c: "60",
			setup: func(f *planFixture, id uuid.UUID) { f.setUptime(id, 30) },
			edit:  func(f *planFixture, id uuid.UUID) { f.setUptime(id, 45) },
		},
		{
			name: "scale", field: "scale.replicas", path: "services.web.scale.replicas", applies: AppliesImmediate,
			file: func(v string) string { return planWebHead("8080") + "\n[services.web.scale]\nreplicas = " + v + "\n" },
			a:    "2", b: "3", c: "4",
			setup: func(f *planFixture, id uuid.UUID) { f.setReplicas(id, 2) },
			edit:  func(f *planFixture, id uuid.UUID) { f.setReplicas(id, 3) },
		},
	}
}

func TestPlanDB_ThreeWayDriftMatrix(t *testing.T) {
	for _, dc := range planDriftCases() {
		t.Run(dc.name, func(t *testing.T) { runPlanDrift(t, dc) })
	}
}

func runPlanDrift(t *testing.T, dc planDriftCase) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	dc.setup(f, web.ID)
	fileA, fileC := dc.file(dc.a), dc.file(dc.c)
	f.markApplied(f.staging, fileA)

	show := dc.show
	if show == nil {
		show = func(v string) string { return v }
	}
	eq := func(want, got, what string) {
		t.Helper()
		if dc.loose {
			require.Contains(t, got, want, what)
			return
		}
		require.Equal(t, show(want), got, what)
	}
	// one returns the only change of the plan after the usual checks of a valid single-field set.
	one := func(p Plan, state string) PlanChange {
		t.Helper()
		require.True(t, p.Valid, "%v", p.Diagnostics)
		ps := planSvc(t, p, "web")
		require.Equal(t, ActionUpdate, ps.Action)
		require.Equal(t, state, ps.State)
		require.Len(t, ps.Changes, 1, "%v", planFieldsOf(ps.Changes))
		c := ps.Changes[0]
		require.Equal(t, dc.field, c.Field)
		require.Equal(t, OpSet, c.Op)
		require.Equal(t, dc.applies, c.Applies)
		require.False(t, c.Masked)
		if dc.path != "" {
			require.Equal(t, dc.path, c.Path)
		}
		return c
	}

	// in_sync: the file is what was applied and what is live.
	p := f.plan(f.staging, fileA)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUnchanged, ps.Action)
	require.Equal(t, StateInSync, ps.State)
	require.True(t, ps.Managed)
	require.Empty(t, ps.Changes)
	require.Equal(t, 1, p.Summary.Unchanged)

	// file_changed: only the file moved.
	c := one(f.plan(f.staging, fileC), StateFileChanged)
	require.Equal(t, ReasonFileChanged, c.Reason)
	eq(dc.a, c.From, "from")
	eq(dc.c, c.To, "to")

	// new: a member of the file that does not exist live.
	if dc.extra != "" {
		p := f.plan(f.staging, fileA+dc.extra)
		require.True(t, p.Valid, "%v", p.Diagnostics)
		ps := planSvc(t, p, "web")
		require.Equal(t, StateNew, ps.State, "a member absent live is a first-apply state, not a newer file")
		require.Len(t, ps.Changes, 1, "%v", planFieldsOf(ps.Changes))
		x := ps.Changes[0]
		require.Equal(t, dc.extraField, x.Field)
		require.Equal(t, OpAdd, x.Op)
		require.Equal(t, ReasonNew, x.Reason)
		require.Equal(t, dc.applies, x.Applies)
		require.Empty(t, x.From)
		require.Contains(t, x.To, dc.extraTo)
	}

	// An edit of a part of the live value the file does not declare is not drift.
	if dc.undeclared != nil {
		dc.undeclared(f, web.ID)
		p := f.plan(f.staging, fileA)
		require.Equal(t, StateInSync, planSvc(t, p, "web").State, "undeclared keys are not compared")
		require.Empty(t, planSvc(t, p, "web").Changes)
	}

	// drift: the dashboard changed it since the last apply; the file still says what it said.
	dc.edit(f, web.ID)
	c = one(f.plan(f.staging, fileA), StateDrift)
	require.Equal(t, ReasonDrift, c.Reason)
	eq(dc.b, c.From, "from")
	eq(dc.a, c.To, "to")

	// conflict: both changed.
	c = one(f.plan(f.staging, fileC), StateConflict)
	require.Equal(t, ReasonConflict, c.Reason)
	eq(dc.b, c.From, "from")
	eq(dc.c, c.To, "to")

	// adopt: without a base (never applied) any difference is an adoption.
	require.NoError(t, f.q.DeleteConfigSource(context.Background(), web.ID))
	p = f.plan(f.staging, fileC)
	c = one(p, StateAdopt)
	require.Equal(t, ReasonAdopt, c.Reason)
	require.False(t, planSvc(t, p, "web").Managed)
	eq(dc.b, c.From, "from")
	eq(dc.c, c.To, "to")
}

func TestPlanDB_OwnedBaseIsTheFingerprintOfTheNormalisedValue(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planWebHead("9090")
	port := func(base int) PlanService {
		f.recordSource(web.ID, f.staging, map[string]string{"config.port": projectconfig.Fingerprint(base)})
		return planSvc(t, f.plan(f.staging, file), "web")
	}
	require.True(t, strings.HasPrefix(projectconfig.Fingerprint(8080), "sha256:"))

	ps := port(8080) // live == base, file differs
	require.Equal(t, StateFileChanged, ps.State)
	require.Equal(t, ReasonFileChanged, planChangeOf(t, ps, "config.port").Reason)

	ps = port(9090) // live != base, file == base
	require.Equal(t, StateDrift, ps.State)
	require.Equal(t, ReasonDrift, planChangeOf(t, ps, "config.port").Reason)

	ps = port(7000) // everything differs
	require.Equal(t, StateConflict, ps.State)
	require.Equal(t, ReasonConflict, planChangeOf(t, ps, "config.port").Reason)
}

func TestPlanDB_ServiceStateIsTheWorstOfItsFields(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.setVar(web.ID, "LOG_LEVEL", "info", false)
	v1 := planWebHead("8080") + "\n[services.web.variables]\nLOG_LEVEL = \"info\"\n"
	f.markApplied(f.staging, v1)

	f.setVar(web.ID, "LOG_LEVEL", "debug", false)                                             // the variable drifts
	fileChanged := planWebHead("9090") + "\n[services.web.variables]\nLOG_LEVEL = \"info\"\n" // the port: file_changed
	ps := planSvc(t, f.plan(f.staging, fileChanged), "web")
	require.Equal(t, StateDrift, ps.State, "drift outranks file_changed")
	require.Equal(t, ReasonFileChanged, planChangeOf(t, ps, "config.port").Reason)
	require.Equal(t, ReasonDrift, planChangeOf(t, ps, "variables.LOG_LEVEL").Reason)

	f.editConfig(web.ID, func(c *services.Config) { c.Port = 7000 }) // port: conflict
	ps = planSvc(t, f.plan(f.staging, fileChanged), "web")
	require.Equal(t, StateConflict, ps.State, "conflict outranks drift")
	require.Equal(t, ReasonConflict, planChangeOf(t, ps, "config.port").Reason)

	// Back to the applied file: both fields were edited in the dashboard since, the file did not move.
	ps = planSvc(t, f.plan(f.staging, v1), "web")
	require.Equal(t, StateDrift, ps.State)
	require.Equal(t, ReasonDrift, planChangeOf(t, ps, "config.port").Reason)
	require.Equal(t, ReasonDrift, planChangeOf(t, ps, "variables.LOG_LEVEL").Reason)
}

// ---------------------------------------------------------------------------------------------
// defaults, no wiping
// ---------------------------------------------------------------------------------------------

func TestPlanDB_WritingADefaultExplicitlyPlansNoChange(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.setVar(web.ID, "LOG_LEVEL", "info", false)
	f.gitService(f.staging, "api")
	file := planHead + `
[services.web]
image = "nginx:1.27"
port = 8080
command = []
one_shot = false
stop_grace_seconds = 30
memory_mb = 0
cpus = 0.0
image_retention = 5
block_egress = false

[services.web.health_check]
type = "http"
path = "/"
port = 8080
interval_seconds = 2
timeout_seconds = 3
start_period_seconds = 0
deadline_seconds = 120
success_threshold = 2

[services.web.variables]
LOG_LEVEL = { value = "info", scope = "runtime" }

[services.web.scale]
replicas = 1

[services.web.uptime]
enabled = true
alerts = true
failure_threshold = 3
recovery_threshold = 2
reminder_minutes = 0

[services.api]
port = 8080

[services.api.git]
connection = "github-acme"
repo = "acme/shop"
branch = "main"
submodules = "auto"
auto_deploy = true
watch_paths = []

[services.api.build]
type = "auto"
dockerfile = "Dockerfile"
context = "."
location = "inherit"
`
	req := f.planReq(f.staging, file)
	p := f.planWith(req)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, p.Diagnostics)
	require.Equal(t, []string{"api", "web"}, planSlugs(p))
	for _, ps := range p.Services {
		require.Equal(t, ActionUnchanged, ps.Action, "%s: %v", ps.Slug, planFieldsOf(ps.Changes))
		require.Equal(t, StateInSync, ps.State, ps.Slug)
		require.Empty(t, ps.Changes, ps.Slug)
	}
	require.Zero(t, p.Summary.Staged+p.Summary.Immediate+p.Summary.Update)

	// The zero changes are a real comparison: every written default was looked at and found equal.
	pl, err := f.cfg.plan(f.ctx, f.cfg.q, f.project.ID, req, policy.ScopeProjectsRead)
	require.NoError(t, err)
	compared := map[string]int{}
	for _, sp := range pl.svcs {
		for _, fp := range sp.fields {
			require.False(t, fp.skipped, "%s %s", sp.slug, fp.fv.ID)
			require.Equal(t, StateInSync, fp.state, "%s %s", sp.slug, fp.fv.ID)
			compared[sp.slug]++
		}
	}
	require.GreaterOrEqual(t, compared["web"], 20)
	require.GreaterOrEqual(t, compared["api"], 9)
}

func TestPlanDB_OwnedKeysOnlyNeverWipeUISetFields(t *testing.T) {
	f := newPlanFixture(t)
	builder, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "builder"})
	require.NoError(t, err)
	// A service whose config was set up in the dashboard: a file mount, docker options, a tailnet and a build server.
	cfg := services.Config{
		Image: "nginx:1.27", Port: 8080,
		Files:         []services.FileMount{{Path: "/etc/app.conf", Mode: "0644", Content: "listen = 8080\n"}},
		Docker:        &agentprotocol.DockerOptions{Hostname: "web-1", Init: true, ShmSizeMB: 64},
		Tailnet:       &tailnetcfg.TailnetConfig{Enabled: true, Hostname: "web-ts", Web: true},
		BuildServerID: &builder.ID,
	}.WithDefaults()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	row, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.staging.ID, Name: "web", Slug: "web", Kind: services.KindApp, SourceType: services.SourceImage,
		Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(t, err)

	// The file owns the image and the port only.
	file := planWebHead("9090")
	before := f.snapshot()
	pl, err := f.cfg.plan(f.ctx, f.cfg.q, f.project.ID, f.planReq(f.staging, file), policy.ScopeProjectsRead)
	require.NoError(t, err)
	require.False(t, pl.diags.HasErrors(), "%v", pl.diags)
	require.Len(t, pl.svcs, 1)

	live, merged := f.config(row.ID), pl.svcs[0].cfg
	var diff []string
	for _, d := range services.DiffConfig("config", live, merged) {
		diff = append(diff, d.Field)
	}
	require.Equal(t, []string{"config.port"}, diff, "the config an apply would write differs from the live one in the owned key only")
	require.Equal(t, live.Files, merged.Files)
	require.Equal(t, live.Docker, merged.Docker)
	require.Equal(t, live.Tailnet, merged.Tailnet)
	require.Equal(t, live.BuildServerID, merged.BuildServerID)
	require.Equal(t, 9090, merged.Port)

	p := pl.public
	require.True(t, p.Valid, "%v", p.Diagnostics)
	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "config.port", Path: "services.web.port", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "8080", To: "9090"},
	})
	require.Equal(t, before, f.snapshot(), "planning writes nothing")
}

// ---------------------------------------------------------------------------------------------
// variables
// ---------------------------------------------------------------------------------------------

func TestPlanDB_VariablesReferencesGeneratorsAndSecretDeclarations(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.setVar(web.ID, "LOG_LEVEL", "info", false)
	f.setVar(web.ID, "SESSION_SECRET", "${{secret(48)}}", false)
	f.setVar(web.ID, "PLAIN_KEY", "v", false)
	file := planWebHead("8080") + `
[services.web.variables]
LOG_LEVEL = "info"
FULL = "${{LOG_LEVEL}}-suffix"
SESSION_SECRET = "${{secret(48)}}"
NEW_GEN = "${{secret(32)}}"

[services.web.secrets]
keys = ["PLAIN_KEY", "MISSING_ONE"]
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "warnings never block: %v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	keysLine := planLine(t, file, "keys = ")
	requireChanges(t, ps.Changes, []planWant{
		{Field: "variables.FULL", Path: "services.web.variables.FULL", Line: planLine(t, file, "FULL = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "${{LOG_LEVEL}}-suffix"},
		{Field: "variables.MISSING_ONE#secret", Path: "services.web.secrets.keys[1]", Line: keysLine, Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "secret"},
		{Field: "variables.NEW_GEN", Path: "services.web.variables.NEW_GEN", Line: planLine(t, file, "NEW_GEN = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "generated(secret(32))", Masked: true},
		{Field: "variables.PLAIN_KEY#secret", Path: "services.web.secrets.keys[0]", Line: keysLine, Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "not secret", To: "secret"},
	})
	// The generator text of SESSION_SECRET equals the stored one: its value is kept, nothing to do.
	for _, c := range ps.Changes {
		require.NotEqual(t, "variables.SESSION_SECRET", c.Field)
		require.NotEqual(t, "variables.LOG_LEVEL", c.Field)
	}
	flag := planDiag(t, p.Diagnostics, "secret_flag_differs")
	require.Equal(t, "warning", flag.Severity)
	require.Equal(t, "services.web.secrets.keys[0]", flag.Path)
	require.Equal(t, keysLine, flag.Line)
	missing := planDiag(t, p.Diagnostics, "secret_missing_value")
	require.Equal(t, "warning", missing.Severity)
	require.Equal(t, "services.web.secrets.keys[1]", missing.Path)
	require.Contains(t, missing.Message, "MISSING_ONE")
	require.Equal(t, 2, p.Summary.Warnings)
	require.Zero(t, p.Summary.Errors)
	require.Equal(t, 4, p.Summary.Staged)
	require.Zero(t, p.Summary.Immediate)

	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, web.ID)
	require.NoError(t, err)
	generated := raw["SESSION_SECRET"].Value
	require.NotEmpty(t, generated)
	require.NotContains(t, planJSON(t, p), generated, "a generated value never reaches the plan")

	// A different generator text is a change; both sides stay masked.
	file2 := strings.Replace(file, `SESSION_SECRET = "${{secret(48)}}"`, `SESSION_SECRET = "${{secret(32)}}"`, 1)
	c := planChangeOf(t, planSvc(t, f.plan(f.staging, file2), "web"), "variables.SESSION_SECRET")
	require.Equal(t, planWant{
		Field: "variables.SESSION_SECRET", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "generated(secret(48))", To: "generated(secret(32))", Masked: true,
	}, planWant{Field: c.Field, Op: c.Op, Applies: c.Applies, Reason: c.Reason, From: c.From, To: c.To, Masked: c.Masked})
}

// ---------------------------------------------------------------------------------------------
// shared variable layers
// ---------------------------------------------------------------------------------------------

func TestPlanDB_SharedLayersAreListedSeparately(t *testing.T) {
	f := newPlanFixture(t)
	f.setShared(variables.LayerProject, f.project.ID, "APP_NAME", "Acme", false)
	file := planHead + `
[variables]
APP_NAME = "Acme"
REGION = "eu"

[secrets]
keys = ["SHARED_TOKEN"]

[environments.staging.variables]
STAGE_ONLY = "yes"
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, p.Services)
	requireChanges(t, p.Shared.Project, []planWant{
		{Field: "project.REGION", Path: "variables.REGION", Line: planLine(t, file, "REGION = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "eu"},
		{Field: "project.SHARED_TOKEN#secret", Path: "secrets.keys", Line: planLine(t, file, "keys = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "secret"},
	})
	requireChanges(t, p.Shared.Environment, []planWant{
		{Field: "environment.STAGE_ONLY", Path: "environments.staging.variables.STAGE_ONLY", Line: planLine(t, file, "STAGE_ONLY = "), Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "yes"},
	})
	w := planDiag(t, p.Diagnostics, "secret_missing_value")
	require.Equal(t, "secrets.keys", w.Path)
	require.Equal(t, 1, p.Summary.Warnings)
	require.Equal(t, 3, p.Summary.Staged)
	require.Equal(t, []string{"projects:write"}, p.RequiredScopes)
}

func TestPlanDB_SharedOwnershipDriftAndPrune(t *testing.T) {
	f := newPlanFixture(t)
	f.setShared(variables.LayerProject, f.project.ID, "APP_NAME", "Acme", false)
	fileA := planHead + "\n[variables]\nAPP_NAME = \"Acme\"\n"
	fileC := planHead + "\n[variables]\nAPP_NAME = \"Acme2\"\n"
	f.markApplied(f.staging, fileA)
	settings, err := f.q.GetEnvironmentConfigSettings(context.Background(), f.staging.ID)
	require.NoError(t, err)
	require.Contains(t, string(settings.OwnedShared), "project.APP_NAME", "owned_shared records the shared keys the file owns")

	one := func(p Plan, reason, from, to string) {
		t.Helper()
		require.True(t, p.Valid, "%v", p.Diagnostics)
		requireChanges(t, p.Shared.Project, []planWant{
			{Field: "project.APP_NAME", Path: "variables.APP_NAME", Op: OpSet, Applies: AppliesStaged, Reason: reason, From: from, To: to},
		})
	}
	// in_sync
	require.Empty(t, f.plan(f.staging, fileA).Shared.Project)
	// file_changed
	one(f.plan(f.staging, fileC), ReasonFileChanged, "Acme", "Acme2")
	// drift and conflict: the dashboard edited the shared variable
	f.setShared(variables.LayerProject, f.project.ID, "APP_NAME", "Other", false)
	one(f.plan(f.staging, fileA), ReasonDrift, "Other", "Acme")
	one(f.plan(f.staging, fileC), ReasonConflict, "Other", "Acme2")
	// adopt: nothing recorded
	require.NoError(t, f.q.SetEnvironmentConfigOwnedShared(context.Background(), db.SetEnvironmentConfigOwnedSharedParams{
		EnvironmentID: f.staging.ID, WorkspaceID: f.ws.ID, OwnedShared: ownedJSON(nil),
	}))
	one(f.plan(f.staging, fileC), ReasonAdopt, "Other", "Acme2")

	// Release and prune: the file stops listing the variable it owned.
	f.markApplied(f.staging, fileA)
	gone := planHead
	p := f.plan(f.staging, gone)
	require.Empty(t, p.Shared.Project, "a key that leaves the file is released, not removed")
	require.Zero(t, p.Summary.Pruned)
	req := f.planReq(f.staging, gone)
	req.Prune = true
	p = f.planWith(req)
	requireChanges(t, p.Shared.Project, []planWant{
		{Field: "project.APP_NAME", Path: "variables.APP_NAME", Op: OpRemove, Applies: AppliesStaged, Reason: ReasonFileChanged, From: "Other"},
	})
	require.Equal(t, 1, p.Summary.Pruned)

	// The environment layer works the same way.
	f.setShared(variables.LayerEnvironment, f.staging.ID, "REGION", "eu", false)
	envFile := func(v string) string {
		return planHead + "\n[environments.staging.variables]\nREGION = \"" + v + "\"\n"
	}
	f.markApplied(f.staging, envFile("eu"))
	require.Empty(t, f.plan(f.staging, envFile("eu")).Shared.Environment)
	p = f.plan(f.staging, envFile("us"))
	requireChanges(t, p.Shared.Environment, []planWant{
		{Field: "environment.REGION", Path: "environments.staging.variables.REGION", Op: OpSet, Applies: AppliesStaged, Reason: ReasonFileChanged, From: "eu", To: "us"},
	})
}

// ---------------------------------------------------------------------------------------------
// release, prune, unmanaged members
// ---------------------------------------------------------------------------------------------

const planReleaseV1Tail = `
[services.web.variables]
A = "1"
B = "2"

[services.web.tasks.digest]
schedule = "0 7 * * *"
command = ["echo", "a"]

[services.web.volumes]
data = "/data"
`

func newPlanReleaseFixture(t *testing.T) (*planFixture, services.ServiceView) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	for k, v := range map[string]string{"A": "1", "B": "2", "C": "3"} {
		f.setVar(web.ID, k, v, false)
	}
	f.task(web.ID, "digest", "0 7 * * *", "echo", "a")
	f.task(web.ID, "ui-task", "0 6 * * *", "echo", "ui")
	_, err := f.svcs.AddVolume(f.ctx, web.ID, "data", "/data")
	require.NoError(t, err)
	f.markApplied(f.staging, planWebHead("8080")+planReleaseV1Tail)
	return f, web
}

func TestPlanDB_ReleasedKeysKeepTheirLiveValue(t *testing.T) {
	f, _ := newPlanReleaseFixture(t)
	v2 := planWebHead("8080") + "\n[services.web.variables]\nA = \"1\"\n"
	before := f.snapshot()

	p := f.plan(f.staging, v2)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUnchanged, ps.Action)
	require.Empty(t, ps.Changes, "releasing nothing live")
	require.Equal(t, []string{"services.web.tasks.digest", "services.web.variables.B", "services.web.volumes.data"}, ps.Released)
	require.Equal(t, map[string]int{"variables": 2, "tasks": 2, "volumes": 1}, ps.Unmanaged, "counts of live members the file does not mention")
	require.Zero(t, p.Summary.Pruned)
	require.Equal(t, before, f.snapshot())

	// Dropping a scalar releases it as well, and even with prune a scalar is never reset.
	v3 := planHead + "\n[services.web]\nimage = \"nginx:1.27\"\n" + strings.TrimPrefix(planReleaseV1Tail, "\n")
	req := f.planReq(f.staging, v3)
	req.Prune = true
	p = f.planWith(req)
	ps = planSvc(t, p, "web")
	require.Len(t, ps.Released, 1)
	require.Empty(t, ps.Changes)
}

func TestPlanDB_PruneRemovesOnlyMembersTheFilePreviouslyOwned(t *testing.T) {
	f, _ := newPlanReleaseFixture(t)
	v2 := planWebHead("8080") + "\n[services.web.variables]\nA = \"1\"\n"
	before := f.snapshot()
	req := f.planReq(f.staging, v2)
	req.Prune = true
	p := f.planWith(req)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUpdate, ps.Action)
	// B, the task and the volume were owned and left the file; C and ui-task were never owned.
	requireChanges(t, ps.Changes, []planWant{
		{Field: "tasks.digest", Path: "services.web.tasks.digest", Op: OpRemove, Applies: AppliesImmediate, Reason: ReasonFileChanged, From: `0 7 * * * UTC ["echo","a"]`},
		{Field: "variables.B", Path: "services.web.variables.B", Op: OpRemove, Applies: AppliesStaged, Reason: ReasonFileChanged, From: "2"},
		{Field: "volumes.data", Path: "services.web.volumes.data", Op: OpRemove, Applies: AppliesStaged, Reason: ReasonFileChanged, From: "/data"},
	})
	require.Equal(t, PlanSummary{Update: 1, Pruned: 3, Staged: 2, Immediate: 1, Adopted: 0, RequiresDeploy: []string{"web"}}, p.Summary)
	require.Equal(t, before, f.snapshot(), "planning a prune deletes nothing")

	// prune changes the plan id: an apply without prune must not be confused with one with it.
	require.NotEqual(t, f.plan(f.staging, v2).PlanID, p.PlanID)
}

func TestPlanDB_PruneNeverRemovesServices(t *testing.T) {
	f, web := newPlanReleaseFixture(t)
	f.database(f.staging, "db")
	f.recordSource(web.ID, f.staging, map[string]string{"config.port": projectconfig.Fingerprint(8080), "variables.A": projectconfig.Fingerprint("x")})
	// The file no longer mentions any service.
	req := f.planReq(f.staging, planHead)
	req.Prune = true
	p := f.planWith(req)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"web", "db"}, planSlugs(p))
	for _, ps := range p.Services {
		require.Equal(t, ActionNotInFile, ps.Action, ps.Slug)
		require.Empty(t, ps.Changes, "%s: a service that left the file keeps everything", ps.Slug)
	}
	require.True(t, p.Services[0].Managed)
	require.False(t, p.Services[1].Managed)
	require.Zero(t, p.Summary.Pruned)
}

func TestPlanDB_UnmanagedMembersAreCountedNeverListed(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.setVar(web.ID, "LOG_LEVEL", "info", false)
	f.setVar(web.ID, "EXTRA1", "secret-looking-value-1", false)
	f.setVar(web.ID, "EXTRA2", "secret-looking-value-2", false)
	_, err := f.doms.Create(f.ctx, web.ID, "extra.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	f.task(web.ID, "ui-task", "0 6 * * *", "echo", "ui")
	_, err = f.svcs.AddVolume(f.ctx, web.ID, "data", "/data")
	require.NoError(t, err)

	p := f.plan(f.staging, planWebHead("8080")+"\n[services.web.variables]\nLOG_LEVEL = \"info\"\n")
	ps := planSvc(t, p, "web")
	require.Equal(t, map[string]int{"variables": 2, "domains": 1, "tasks": 1, "volumes": 1}, ps.Unmanaged)
	require.Empty(t, ps.Changes)
	require.NotContains(t, planJSON(t, p), "secret-looking-value")
}

func TestPlanDB_BackupSchedulesAreMatchedByKey(t *testing.T) {
	f := newPlanFixture(t)
	dbSvc := f.database(f.staging, "db")
	f.backup(dbSvc.ID, "nightly", "0 2 * * *")
	f.backup(dbSvc.ID, "", "0 3 * * *") // made in the dashboard: its identity is the synthesised key schedule-1
	head := planHeadServer + "\n[services.db.database]\nengine = \"postgres\"\n"
	nightly := "\n[services.db.backups.nightly]\ndestination = \"offsite-s3\"\ncron = \"0 2 * * *\"\n"

	p := f.plan(f.staging, head+nightly)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "db")
	require.Empty(t, ps.Changes, "the schedule with the key nightly equals the file")
	require.Equal(t, 1, ps.Unmanaged["backups"], "the key-less schedule is not managed")
	require.Equal(t, 1, ps.Unmanaged["volumes"], "the data volume of the database")

	// A file that writes schedule-1 adopts the schedule made in the dashboard.
	adopt := nightly + "\n[services.db.backups.schedule-1]\ndestination = \"offsite-s3\"\ncron = \"0 4 * * *\"\n"
	p = f.plan(f.staging, head+adopt)
	requireChanges(t, planSvc(t, p, "db").Changes, []planWant{
		{Field: "backups.schedule-1", Path: "services.db.backups.schedule-1", Op: OpSet, Applies: AppliesImmediate, Reason: ReasonAdopt,
			From: "0 3 * * * UTC → offsite-s3", To: "0 4 * * * UTC → offsite-s3"},
	})
	require.Zero(t, planSvc(t, p, "db").Unmanaged["backups"])
}

// ---------------------------------------------------------------------------------------------
// the only filter, plan id
// ---------------------------------------------------------------------------------------------

func TestPlanDB_OnlyFilterLimitsTheServicesAndFields(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	f.image(f.staging, "worker", 0)
	file := planHead + `
[variables]
APP_NAME = "Acme"

[services.web]
image = "nginx:1.27"
port = 9090

[services.web.variables]
LOG_LEVEL = "debug"

[services.worker]
image = "nginx:1.27"
port = 9091
`
	all := f.plan(f.staging, file)
	require.Equal(t, []string{"config.port", "variables.LOG_LEVEL"}, planFieldsOf(planSvc(t, all, "web").Changes))
	require.Equal(t, []string{"config.port"}, planFieldsOf(planSvc(t, all, "worker").Changes))
	require.Len(t, all.Shared.Project, 1)

	only := func(paths ...string) Plan {
		req := f.planReq(f.staging, file)
		req.Only = paths
		return f.planWith(req)
	}

	p := only("services.web")
	require.Equal(t, []string{"web"}, planSlugs(p))
	require.Equal(t, []string{"config.port", "variables.LOG_LEVEL"}, planFieldsOf(p.Services[0].Changes))
	require.Empty(t, p.Shared.Project, "a file key path outside the filter is not planned")

	p = only("config.port") // a field id selects the field of every service
	require.Equal(t, []string{"web", "worker"}, planSlugs(p))
	require.Equal(t, []string{"config.port"}, planFieldsOf(p.Services[0].Changes))
	require.Equal(t, []string{"config.port"}, planFieldsOf(p.Services[1].Changes))
	require.Empty(t, p.Shared.Project)

	p = only("services.web.variables.LOG_LEVEL")
	require.Equal(t, []string{"web"}, planSlugs(p))
	require.Equal(t, []string{"variables.LOG_LEVEL"}, planFieldsOf(p.Services[0].Changes))

	p = only("variables.APP_NAME") // the shared layers are filtered by their key path too
	require.Empty(t, p.Services)
	require.Len(t, p.Shared.Project, 1)

	require.NotEqual(t, all.PlanID, only("services.web").PlanID)
	require.Equal(t, only("config.port", "services.web.variables.LOG_LEVEL").PlanID, only("services.web.variables.LOG_LEVEL", "config.port").PlanID, "the order of the filter is irrelevant")
}

func TestPlanDB_PlanIDIsStableAndFollowsEverythingTheApplyDependsOn(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planWebHead("9090")
	id := func(req PlanRequest) string { return f.planWith(req).PlanID }

	base := id(f.planReq(f.staging, file))
	require.Equal(t, base, id(f.planReq(f.staging, file)), "same inputs, same id")
	require.NotEqual(t, base, id(f.planReq(f.staging, file+"\n# a comment\n")), "the text of the file")
	r := f.planReq(f.staging, file)
	r.Prune = true
	require.NotEqual(t, base, id(r), "prune")
	r = f.planReq(f.staging, file)
	r.Only = []string{"config.port"}
	require.NotEqual(t, base, id(r), "the filter")

	// A dashboard edit of a compared field changes the live fingerprint.
	f.editConfig(web.ID, func(c *services.Config) { c.Port = 7000 })
	edited := id(f.planReq(f.staging, file))
	require.NotEqual(t, base, edited)
	// A generation bump moves the id even when nothing compared changed.
	f.editConfig(web.ID, func(c *services.Config) { c.MemoryMB = 64 })
	require.NotEqual(t, edited, id(f.planReq(f.staging, file)))
	// Another environment.
	f.image(f.prod, "web", 8080)
	require.NotEqual(t, base, id(f.planReq(f.prod, file)))
}

// ---------------------------------------------------------------------------------------------
// scopes and the production guard
// ---------------------------------------------------------------------------------------------

func TestPlanDB_RequiredScopes(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	scale := "\n[services.web.scale]\nreplicas = 2\n"
	uptimeT := "\n[services.web.uptime]\nreminder_minutes = 5\n"
	domainOpts := "\n[services.web.domains.\"a.example.com\".options]\ncompress = true\n"
	nets := "networks = [\"backplane\"]\n"
	cases := []struct {
		name, file string
		want       []string
	}{
		{"no change", planWebHead("8080"), []string{"projects:write"}},
		{"staged change", planWebHead("9090"), []string{"projects:write"}},
		{"scale", planWebHead("8080") + scale, []string{"projects:write", "deployments:write"}},
		{"shared network", planWebHead("8080") + nets, []string{"projects:write", "deployments:write"}},
		{"uptime", planWebHead("8080") + uptimeT, []string{"projects:write", "admin"}},
		{"domain with proxy options", planWebHead("8080") + domainOpts, []string{"projects:write", "proxy:write"}},
		{"domain with a target port only", planWebHead("8080") + "\n[services.web.domains.\"b.example.com\"]\ntarget_port = 8080\n", []string{"projects:write"}},
		{"route", planHeadServer + "\n[services.printer.route]\nprotocol = \"http\"\nupstreams = [{ address = \"192.168.1.20:631\" }]\n", []string{"projects:write", "proxy:write"}},
		{"everything", planWebHead("9090") + nets + scale + uptimeT + domainOpts, []string{"projects:write", "deployments:write", "proxy:write", "admin"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := f.plan(f.staging, c.file)
			require.True(t, p.Valid, "%v", p.Diagnostics)
			require.Equal(t, c.want, p.RequiredScopes)
		})
	}
}

func TestPlanDB_GuardNeedsApprovalForImmediateChangesInProduction(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	f.image(f.prod, "web", 8080)
	staged := planWebHead("9090")
	immediate := planWebHead("8080") + "\n[services.web.scale]\nreplicas = 2\n"

	p := f.plan(f.prod, staged)
	require.True(t, p.Environment.IsProduction)
	require.Equal(t, ApprovalNotRequired, p.Guard.ProductionApproval, "staging in production is free")
	require.Empty(t, p.Guard.Reason)
	require.False(t, p.HasImmediateChanges())

	p = f.plan(f.prod, planWebHead("8080"))
	require.Equal(t, ApprovalNotRequired, p.Guard.ProductionApproval, "nothing to do")

	p = f.plan(f.prod, immediate)
	require.Equal(t, 1, p.Summary.Immediate)
	require.Equal(t, ApprovalRequired, p.Guard.ProductionApproval)
	require.NotEmpty(t, p.Guard.Reason)
	require.True(t, p.HasImmediateChanges())

	p = f.plan(f.staging, immediate)
	require.Equal(t, ApprovalNotRequired, p.Guard.ProductionApproval, "only production asks")
	require.True(t, p.HasImmediateChanges())

	p = f.plan(f.prod, staged)
	require.Equal(t, ApprovalNotRequired, p.Guard.ProductionApproval)
}

// ---------------------------------------------------------------------------------------------
// safety
// ---------------------------------------------------------------------------------------------

func TestPlanDB_PrivilegedDockerOptions(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	ctx := context.Background()

	// A restricted server refuses host-level options; the file can never switch it to unrestricted.
	for name, opts := range map[string]string{"privileged": "privileged = true", "NET_ADMIN": "cap_add = [\"NET_ADMIN\"]"} {
		p := f.plan(f.staging, planWebHead("8080")+"\n[services.web.docker]\n"+opts+"\n")
		require.False(t, p.Valid, name)
		require.True(t, p.Diagnostics.HasCode("docker_unrestricted"), "%s: %v", name, p.Diagnostics)
	}

	_, err := f.pool.Exec(ctx, "UPDATE servers SET unrestricted = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	file := planWebHead("8080") + "\n[services.web.docker]\nprivileged = true\n"
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	d := planDiag(t, p.Diagnostics, "auto_apply_privileged")
	require.Equal(t, "info", d.Severity)
	require.Equal(t, "services.web.docker.privileged", d.Path)
	require.Equal(t, planLine(t, file, "privileged"), d.Line)
	require.Zero(t, p.Summary.Errors+p.Summary.Warnings, "info is neither an error nor a warning")
	require.False(t, p.Diagnostics.HasCode("docker_unrestricted"))

	// An unprivileged option is not flagged.
	p = f.plan(f.staging, planWebHead("8080")+"\n[services.web.docker]\nhostname = \"web-1\"\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.False(t, p.Diagnostics.HasCode("auto_apply_privileged"))
}

func TestPlanDB_StaticSafetyDiagnostics(t *testing.T) {
	f := newPlanFixture(t)
	many := map[string]string{}
	for i := range 9 {
		many[fmt.Sprintf("f%d.txt", i)] = "x"
	}
	composeAtt := map[string]string{"../docker-compose.yml": planCompose}
	stackFile := func(path string) string {
		return planHeadServer + "\n[services.shop.stack]\ncompose_file = \"" + path + "\"\n"
	}
	cases := []struct {
		name, file string
		att        map[string]string
		code       string
		needle     string // the line the diagnostic points at ("" = not asserted)
		leak       []string
	}{
		{"credential_in_url", planHeadServer + "\n[services.web]\nport = 8080\n[services.web.git]\nurl = \"https://deploy:s3cr3tpw9X@git.example.com/acme/shop.git\"\nbranch = \"main\"\n",
			nil, "credential_in_url", "url = ", []string{"s3cr3tpw9X"}},
		{"literal_secret", planHeadServer + "\n[services.web]\nimage = \"nginx:1.27\"\n[services.web.variables]\nDB_PASSWORD = \"hunter2hunter2\"\n",
			nil, "literal_secret", "DB_PASSWORD", []string{"hunter2hunter2", "unter2h"}},
		{"secret_in_file", planHeadServer + "\n[services.web]\nimage = \"nginx:1.27\"\n[services.web.variables]\nNOTES = \"ghp_0123456789abcdefghijklmnopqrstuvwxyz\"\n",
			nil, "secret_in_file", "NOTES", []string{"0123456789abcdefghijklmnopqrstuvwxyz"}},
		{"file_too_large", "schema = 1\n# " + strings.Repeat("x", 300<<10) + "\n", nil, "file_too_large", "", nil},
		{"path_escape with ..", stackFile("../docker-compose.yml"), composeAtt, "path_escape", "compose_file", nil},
		{"path_escape with an absolute path", stackFile("/etc/passwd"), map[string]string{"/etc/passwd": planCompose}, "path_escape", "compose_file", nil},
		{"attachment_missing", stackFile("compose.yml"), nil, "attachment_missing", "compose_file", nil},
		{"limit_exceeded (attachments)", planHead, many, "limit_exceeded", "", nil},
		{"license_not_accepted", planHeadServer + "\n[services.cache.database]\nengine = \"redis\"\n", nil, "license_not_accepted", "", nil},
		{"egress_blocked", planHeadServer + "\n[services.web]\nimage = \"nginx:1.27\"\nblock_egress = true\nnetworks = [\"backplane\"]\n", nil, "egress_blocked", "networks", nil},
		{"replicas_require_stateless", planHeadServer + "\n[services.web]\nimage = \"nginx:1.27\"\nport = 8080\n[services.web.volumes]\ndata = \"/data\"\n[services.web.scale]\nreplicas = 2\n",
			nil, "replicas_require_stateless", "replicas", nil},
		{"missing_source", planHeadServer + "\n[services.web]\nport = 8080\n", nil, "missing_source", "", nil},
		{"schema_unsupported", "schema = 2\n", nil, "schema_unsupported", "schema", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := Input{File: c.file, Attachments: c.att}
			p := f.planWith(PlanRequest{Environment: "staging", Input: in})
			require.False(t, p.Valid)
			d := planDiag(t, p.Diagnostics, c.code)
			require.Equal(t, "error", d.Severity)
			if c.needle != "" {
				require.Equal(t, planLine(t, c.file, c.needle), d.Line, "%+v", d)
			}
			require.GreaterOrEqual(t, p.Summary.Errors, 1)
			require.Empty(t, p.Services, "a file with errors plans nothing")
			body := planJSON(t, p)
			for _, l := range c.leak {
				require.NotContains(t, body, l, "no part of a rejected literal is echoed")
			}

			v, err := f.cfg.Validate(f.ctx, f.project.ID, in, "")
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.True(t, v.Diagnostics.HasCode(c.code), "%v", v.Diagnostics)
		})
	}

	// An accepted licence makes the same database valid.
	p := f.plan(f.staging, planHeadServer+"\n[services.cache.database]\nengine = \"redis\"\naccept_license = true\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.False(t, p.Diagnostics.HasCode("license_not_accepted"))

	// A compose file sent as an attachment is read into the plan.
	p = f.planWith(PlanRequest{Environment: "staging", Input: Input{File: stackFile("compose.yml"), Attachments: map[string]string{"compose.yml": planCompose}}})
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, planCompose, planChangeOf(t, planSvc(t, p, "shop"), "config.stack.compose").To)
}

func TestPlanDB_HostnameInUseByAnotherService(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	other := f.image(f.staging, "other", 8080)
	_, err := f.doms.Create(f.ctx, other.ID, "taken.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)

	file := planWebHead("8080") + "\n[services.web.domains.\"taken.example.com\"]\ntarget_port = 8080\n"
	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "hostname_in_use")
	require.Contains(t, d.Message, "other", "names the service that holds the hostname")
	require.GreaterOrEqual(t, d.Line, planLine(t, file, "taken.example.com"), "points at the domain table")

	// A free hostname is a plain staged change.
	p = f.plan(f.staging, strings.ReplaceAll(file, "taken.example.com", "free.example.com"))
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"domains.free.example.com"}, planFieldsOf(planSvc(t, p, "web").Changes))
}

func TestPlanDB_ReplicasNeedStatelessServices(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planWebHead("8080") + "\n[services.web.scale]\nreplicas = 2\n"
	require.True(t, f.plan(f.staging, file).Valid)
	_, err := f.svcs.AddVolume(f.ctx, web.ID, "data", "/data")
	require.NoError(t, err)
	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "replicas_require_stateless")
	require.Equal(t, "services.web.scale.replicas", d.Path)
	require.Equal(t, planLine(t, file, "replicas = 2"), d.Line)
}

func TestPlanDB_LiveBlockEgressRefusesSharedNetworks(t *testing.T) {
	f := newPlanFixture(t)
	f.database(f.staging, "db") // databases start with outbound traffic blocked
	file := planHeadServer + "\n[services.db]\nnetworks = [\"backplane\"]\n[services.db.database]\nengine = \"postgres\"\n"
	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "egress_blocked")
	require.Equal(t, "services.db.networks[0]", d.Path)

	// Allowing egress in the same file makes the attachment valid.
	ok := strings.Replace(file, "networks = [\"backplane\"]\n", "networks = [\"backplane\"]\nblock_egress = false\n", 1)
	p = f.plan(f.staging, ok)
	require.True(t, p.Valid, "%v", p.Diagnostics)
}

func TestPlanDB_UnknownCertificate(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	file := planWebHead("8080") + "\n[services.web.domains.\"a.example.com\"]\ntls = \"custom\"\ncertificate = \"nope\"\n"
	p := f.plan(f.staging, file)
	require.False(t, p.Valid, "a certificate that does not exist on the service's server is a plan error, not an apply failure: %v", p.Diagnostics)
	d := planDiag(t, p.Diagnostics, "unknown_certificate")
	require.Equal(t, "error", d.Severity)
	require.NotZero(t, d.Line)
}

// ---------------------------------------------------------------------------------------------
// environments
// ---------------------------------------------------------------------------------------------

func TestPlanDB_EnvironmentOverridesResolveForTheTargetEnvironment(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)
	f.image(f.prod, "web", 8080)
	file := planHead + `
[services.web]
image = "nginx:1.27"
port = 8080

[services.job]
only_in = ["production"]
image = "nginx:1.27"

[environments.staging.services.web]
port = 9090
`
	st := f.plan(f.staging, file)
	require.True(t, st.Valid, "%v", st.Diagnostics)
	require.Equal(t, []string{"web"}, planSlugs(st), "only_in keeps job out of staging")
	c := planChangeOf(t, planSvc(t, st, "web"), "config.port")
	require.Equal(t, "9090", c.To)
	require.Equal(t, planLine(t, file, "port = 9090"), c.Line, "the line of the override that won")

	pr := f.plan(f.prod, file)
	require.True(t, pr.Valid, "%v", pr.Diagnostics)
	require.Equal(t, []string{"job", "web"}, planSlugs(pr))
	require.Equal(t, ActionCreate, planSvc(t, pr, "job").Action)
	require.Equal(t, ActionUnchanged, planSvc(t, pr, "web").Action)
}

func TestPlanDB_EnvironmentSelection(t *testing.T) {
	f := newPlanFixture(t)
	codeOf := func(err error) string {
		t.Helper()
		ae, ok := apierr.As(err)
		require.True(t, ok, "%v", err)
		return ae.Code
	}
	_, err := f.cfg.Plan(f.ctx, f.project.ID, PlanRequest{Input: Input{File: planHead}})
	require.Equal(t, CodeEnvironmentRequired, codeOf(err), "two standard environments: the caller must name one")
	_, err = f.cfg.Plan(f.ctx, f.project.ID, PlanRequest{Environment: "nope", Input: Input{File: planHead}})
	require.Equal(t, CodeUnknownEnvironment, codeOf(err))

	p := f.planWith(PlanRequest{Environment: f.prod.ID.String(), Input: Input{File: planHead}})
	require.Equal(t, PlanEnvironment{ID: f.prod.ID, Slug: "production", IsProduction: true}, p.Environment, "an id works as well as a slug")
}

func TestPlanDB_NeedsProjectsReadOnTheProject(t *testing.T) {
	f := newPlanFixture(t)
	req := f.planReq(f.staging, planHead)
	statusOf := func(err error) int {
		t.Helper()
		ae, ok := apierr.As(err)
		require.True(t, ok, "%v", err)
		return ae.Status
	}
	_, err := f.cfg.Plan(f.ctxWith(policy.ScopeProjectsWrite), f.project.ID, req)
	require.Equal(t, 403, statusOf(err), "write does not imply read")
	_, err = f.cfg.Validate(f.ctxWith(policy.ScopeDeploymentsWrite), f.project.ID, req.Input, "")
	require.Equal(t, 403, statusOf(err))

	p, err := f.cfg.Plan(f.ctxWith(policy.ScopeProjectsRead), f.project.ID, req)
	require.NoError(t, err, "a read-only token can plan")
	require.True(t, p.Valid)

	tok := uuid.New()
	restricted := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.user.ID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper,
		Scopes: policy.AllScopes, ProjectIDs: []uuid.UUID{uuid.New()},
	})
	_, err = f.cfg.Plan(restricted, f.project.ID, req)
	require.Equal(t, 404, statusOf(err), "a token restricted to other projects does not see this one")

	// The control plane's own principal (a push-triggered check) is bound to one project.
	sys := auth.WithPrincipal(context.Background(), auth.System(f.ws.ID, f.project.ID, "nebula.toml @3f9c2a1", policy.ScopeProjectsRead))
	p, err = f.cfg.Plan(sys, f.project.ID, req)
	require.NoError(t, err)
	require.True(t, p.Valid)
	elsewhere := auth.WithPrincipal(context.Background(), auth.System(f.ws.ID, uuid.New(), "nebula.toml @3f9c2a1", policy.ScopeProjectsRead))
	_, err = f.cfg.Plan(elsewhere, f.project.ID, req)
	require.Equal(t, 404, statusOf(err))
}

// ---------------------------------------------------------------------------------------------
// validate
// ---------------------------------------------------------------------------------------------

func TestPlanDB_ValidateResultShape(t *testing.T) {
	f := newPlanFixture(t)
	good := planHead + `
[services.web]
image = "nginx:1.27"
port = 8080

[services.db.database]
engine = "postgres"

[services.site.git]
url = "https://git.example.com/acme/site.git"
branch = "main"

[environments.production.services.web]
port = 80

[environments.staging.variables]
REGION = "eu"
`
	v, err := f.cfg.Validate(f.ctx, f.project.ID, Input{File: good}, "")
	require.NoError(t, err)
	require.True(t, v.Valid, "%v", v.Diagnostics)
	require.Equal(t, 1, v.Schema)
	require.NotNil(t, v.Diagnostics)
	require.Empty(t, v.Diagnostics)
	require.ElementsMatch(t, []string{"production", "staging"}, v.Environments)
	require.Equal(t, []ValidateService{{Slug: "db", Kind: "database"}, {Slug: "site", Kind: "git"}, {Slug: "web", Kind: "image"}}, v.Services)
	body := planJSON(t, v)
	require.Contains(t, body, `"diagnostics":[]`)

	// A file without blocks lists no environments (an empty list, not null).
	v, err = f.cfg.Validate(f.ctx, f.project.ID, Input{File: planHead}, "")
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.NotNil(t, v.Environments)
	require.Empty(t, v.Environments)
	require.NotNil(t, v.Services)
	require.Empty(t, v.Services)

	// Invalid files are data: valid=false with positioned diagnostics.
	bad := "schema = 1\n\n[services.web]\nimage = \"nginx:1.27\"\n\n[services.web.healthcheck]\npath = \"/\"\n"
	v, err = f.cfg.Validate(f.ctx, f.project.ID, Input{File: bad}, "")
	require.NoError(t, err)
	require.False(t, v.Valid)
	d := planDiag(t, v.Diagnostics, "unknown_key")
	require.Equal(t, "error", d.Severity)
	require.Equal(t, "services.web.healthcheck", d.Path)
	require.Equal(t, planLine(t, bad, "[services.web.healthcheck]"), d.Line)
	require.Contains(t, d.Suggestions, "health_check")

	v, err = f.cfg.Validate(f.ctx, f.project.ID, Input{File: "schema = 1\n[services.web\nport = 1\n"}, "")
	require.NoError(t, err)
	require.False(t, v.Valid)
	d = planDiag(t, v.Diagnostics, "toml_syntax")
	require.GreaterOrEqual(t, d.Line, 2)

	// The project slug of the file is checked against the target project.
	mismatch := "schema = 1\nproject = \"other\"\n"
	v, err = f.cfg.Validate(f.ctx, f.project.ID, Input{File: mismatch}, "")
	require.NoError(t, err)
	require.False(t, v.Valid)
	require.Equal(t, 2, planDiag(t, v.Diagnostics, "project_mismatch").Line)
}

func TestPlanDB_ValidateAgainstAnEnvironmentAddsTheLiveChecks(t *testing.T) {
	f := newPlanFixture(t)
	file := planHead + "\n[services.web]\nimage = \"nginx:1.27\"\nserver = \"ghost\"\n"
	v, err := f.cfg.Validate(f.ctx, f.project.ID, Input{File: file}, "")
	require.NoError(t, err)
	require.True(t, v.Valid, "statically the file is fine: %v", v.Diagnostics)

	v, err = f.cfg.Validate(f.ctx, f.project.ID, Input{File: file}, "staging")
	require.NoError(t, err)
	require.False(t, v.Valid)
	d := planDiag(t, v.Diagnostics, "unknown_server")
	require.NotZero(t, d.Line)
	require.Equal(t, []ValidateService{{Slug: "web", Kind: "image"}}, v.Services)

	_, err = f.cfg.Validate(f.ctx, f.project.ID, Input{File: file}, "nope")
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeUnknownEnvironment, ae.Code)
}

// ---------------------------------------------------------------------------------------------
// planning writes nothing and leaks nothing
// ---------------------------------------------------------------------------------------------

func TestPlanDB_PlanNeverWritesAndNeverLeaksSecrets(t *testing.T) {
	const planted, plantedShared = "zq7Kd29Xm41Lp08Wv", "Tn5Hy63Rb92Cs70Ue"
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.setVar(web.ID, "API_TOKEN", planted, true)
	f.setVar(web.ID, "GEN", "${{secret(48)}}", false)
	f.setVar(web.ID, "LOG_LEVEL", "info", false)
	f.setShared(variables.LayerProject, f.project.ID, "SHARED_TOKEN", plantedShared, true)
	f.task(web.ID, "digest", "0 7 * * *", "echo", "a")

	applied := planHead + `
[secrets]
keys = ["SHARED_TOKEN"]

[services.web]
image = "nginx:1.27"
port = 8080

[services.web.secrets]
keys = ["API_TOKEN"]

[services.web.variables]
GEN = "${{secret(48)}}"
LOG_LEVEL = "info"

[services.web.tasks.digest]
schedule = "0 7 * * *"
command = ["echo", "a"]
`
	f.markApplied(f.staging, applied)
	next := strings.NewReplacer(
		`GEN = "${{secret(48)}}"`, `GEN = "${{secret(32)}}"`,
		`LOG_LEVEL = "info"`, `LOG_LEVEL = "debug"`,
		`keys = ["API_TOKEN"]`, `keys = ["API_TOKEN", "NEW_ONE"]`,
		"port = 8080", "port = 9090",
	).Replace(applied) + `
[services.fresh]
image = "nginx:1.27"
`

	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, web.ID)
	require.NoError(t, err)
	secrets := []string{planted, plantedShared, raw["GEN"].Value}
	require.NotEmpty(t, raw["GEN"].Value)

	before := f.snapshot()
	var out strings.Builder
	run := func(req PlanRequest) Plan {
		p, err := f.cfg.Plan(f.ctx, f.project.ID, req)
		require.NoError(t, err)
		out.WriteString(planJSON(t, p))
		return p
	}
	p := run(f.planReq(f.staging, next))
	require.True(t, p.Valid, "%v", p.Diagnostics)
	prune := f.planReq(f.staging, next)
	prune.Prune = true
	run(prune)
	only := f.planReq(f.staging, next)
	only.Only = []string{"services.web"}
	run(only)
	run(f.planReq(f.staging, applied))
	run(f.planReq(f.staging, planHead))
	v, err := f.cfg.Validate(f.ctx, f.project.ID, Input{File: next}, "staging")
	require.NoError(t, err)
	out.WriteString(planJSON(t, v))

	require.Equal(t, before, f.snapshot(), "plan and validate write no row and bump no generation")
	body := out.String()
	for _, s := range secrets {
		for _, part := range []string{s, s[:6], s[len(s)-6:]} {
			require.NotContains(t, body, part, "a secret value, or a piece of it, is in an output")
			require.NotContains(t, f.auditText(), part, "a secret value, or a piece of it, is in an audit row")
		}
	}

	// The secret changes are masked, the declared ones show the flag.
	ps := planSvc(t, p, "web")
	gen := planChangeOf(t, ps, "variables.GEN")
	require.True(t, gen.Masked)
	require.Equal(t, "generated(secret(48))", gen.From)
	require.Equal(t, "generated(secret(32))", gen.To)
	require.Equal(t, ReasonFileChanged, gen.Reason)
	missing := planChangeOf(t, ps, "variables.NEW_ONE#secret")
	require.Equal(t, "secret", missing.To)
	require.Equal(t, "secret_missing_value", planDiag(t, p.Diagnostics, "secret_missing_value").Code)
	for _, c := range ps.Changes {
		require.NotEqual(t, "variables.API_TOKEN#secret", c.Field, "a declared secret that is stored as secret is in sync")
	}
}

// A stored secret whose text the file cannot see is compared by flag only; a stored secret
// never turns into a plain variable by writing a literal.
func TestPlanDB_StoredSecretStaysSecretWhenTheFileWritesItsName(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.setVar(web.ID, "SMTP_LOGIN", "stored-literal-value", true)
	p := f.plan(f.staging, planWebHead("8080")+"\n[services.web.variables]\nSMTP_LOGIN = \"someone@example.com\"\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)
	c := planChangeOf(t, planSvc(t, p, "web"), "variables.SMTP_LOGIN")
	require.Equal(t, OpSet, c.Op)
	require.True(t, c.Masked, "the live side is a secret")
	require.Equal(t, Mask, c.From, "the stored value is never shown")
	require.NotContains(t, planJSON(t, p), "stored-literal-value")
}

// A route to an address the server's upstream policy refuses (loopback by default) is a plan
// error, not an apply failure.
func TestPlanDB_RouteUpstreamPolicyIsCheckedAtPlanTime(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + "\n[services.printer.route]\nprotocol = \"http\"\nupstreams = [{ address = \"127.0.0.1:8080\" }]\n"
	p := f.plan(f.staging, file)
	require.False(t, p.Valid, "%v", p.Diagnostics)
	d := planDiag(t, p.Diagnostics, "upstream_denied")
	require.Equal(t, "error", d.Severity)
	require.Equal(t, planLine(t, file, "upstreams = "), d.Line)

	ok := strings.Replace(file, "127.0.0.1:8080", "192.168.1.20:631", 1)
	p = f.plan(f.staging, ok)
	require.True(t, p.Valid, "%v", p.Diagnostics)
}

// ---------------------------------------------------------------------------------------------
// processes: one service, several processes
// ---------------------------------------------------------------------------------------------

const planSidekiq = `["bundle","exec","sidekiq"]`

// planProcess is the [services.web.processes.<name>] table of a file: its command, then the rest of its lines.
func planProcess(name, command, rest string) string {
	return "\n[services.web.processes." + name + "]\ncommand = " + command + "\n" + rest
}

const planWorkerTable = `["bundle", "exec", "sidekiq"]`

func planWorker() services.ProcessConfig {
	return services.ProcessConfig{Name: "worker", StartCommand: []string{"bundle", "exec", "sidekiq"}}
}

func planMetrics() services.ProcessConfig {
	return services.ProcessConfig{Name: "metrics", StartCommand: []string{"bin/metrics"}, Port: 9100}
}

// withProcesses adds processes to a service as a dashboard edit does.
func (f *planFixture) withProcesses(id uuid.UUID, ps ...services.ProcessConfig) {
	f.t.Helper()
	f.editConfig(id, func(c *services.Config) { c.Processes = append(c.Processes, ps...) })
}

func TestPlanDB_AddingAProcessIsAStagedChange(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	file := planWebHead("8080") + planProcess("worker", planWorkerTable, "stop_grace_seconds = 120\n")
	before, gen := f.snapshot(), f.gen(web.ID)

	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUpdate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.processes.worker", Path: "services.web.processes.worker", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: planSidekiq + " grace 120s"},
	})
	require.Equal(t, 1, p.Summary.Update)
	require.Equal(t, 1, p.Summary.Staged)
	require.Zero(t, p.Summary.Immediate)
	require.Equal(t, []string{"web"}, p.Summary.RequiresDeploy, "a new process needs a deployment")
	require.Equal(t, []string{"projects:write"}, p.RequiredScopes)
	require.Equal(t, before, f.snapshot(), "planning writes nothing")
	require.Equal(t, gen, f.gen(web.ID))

	// Once the service has the process, the same file changes nothing.
	f.withProcesses(web.ID, services.ProcessConfig{Name: "worker", StartCommand: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: 120})
	p = f.plan(f.staging, file)
	ps = planSvc(t, p, "web")
	require.Equal(t, ActionUnchanged, ps.Action, "%v", planFieldsOf(ps.Changes))
	require.Empty(t, ps.Changes)
}

func TestPlanDB_ProcessNameAndSingletonArePlainStagedKeys(t *testing.T) {
	f := newPlanFixture(t)
	f.image(f.staging, "web", 8080)

	p := f.plan(f.staging, planWebHead("8080")+"process_name = \"app\"\nsingleton = true\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)
	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "config.processName", Path: "services.web.process_name", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "web", To: "app"},
		{Field: "config.singleton", Path: "services.web.singleton", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: "false", To: "true"},
	})

	// Spelling out the defaults is not a change.
	p = f.plan(f.staging, planWebHead("8080")+"process_name = \"web\"\nsingleton = false\n")
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, planSvc(t, p, "web").Changes)
}

func TestPlanDB_ChangingTheCommandOfAProcess(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.withProcesses(web.ID, planWorker())
	v1 := planWebHead("8080") + planProcess("worker", planWorkerTable, "")
	v2 := planWebHead("8080") + planProcess("worker", `["bundle", "exec", "sidekiq", "-c", "5"]`, "")
	const concurrent = `["bundle","exec","sidekiq","-c","5"]`

	// The file that says what is live plans to nothing.
	p := f.plan(f.staging, v1)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, ActionUnchanged, planSvc(t, p, "web").Action)

	// A different command is one change of the process as a whole.
	p = f.plan(f.staging, v2)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "config.processes.worker", Path: "services.web.processes.worker", Op: OpSet, Applies: AppliesStaged, Reason: ReasonAdopt, From: planSidekiq, To: concurrent},
	})
	require.Equal(t, []string{"web"}, p.Summary.RequiresDeploy)

	// After v1 was applied the new command is the file's doing ...
	f.markApplied(f.staging, v1)
	p = f.plan(f.staging, v2)
	ps := planSvc(t, p, "web")
	require.Equal(t, StateFileChanged, ps.State)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.processes.worker", Op: OpSet, Applies: AppliesStaged, Reason: ReasonFileChanged, From: planSidekiq, To: concurrent},
	})

	// ... a command edited in the dashboard is drift the file would put back ...
	f.editConfig(web.ID, func(c *services.Config) { c.Processes[0].StartCommand = []string{"sh", "-c", "sleep 1"} })
	p = f.plan(f.staging, v1)
	ps = planSvc(t, p, "web")
	require.Equal(t, StateDrift, ps.State)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.processes.worker", Op: OpSet, Applies: AppliesStaged, Reason: ReasonDrift, From: `["sh","-c","sleep 1"]`, To: planSidekiq},
	})

	// ... and when both moved, a conflict.
	p = f.plan(f.staging, v2)
	ps = planSvc(t, p, "web")
	require.Equal(t, StateConflict, ps.State)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.processes.worker", Op: OpSet, Applies: AppliesStaged, Reason: ReasonConflict, From: `["sh","-c","sleep 1"]`, To: concurrent},
	})
}

func TestPlanDB_RemovingAProcessFromTheFile(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	api := f.image(f.staging, "api", 8081)
	// The dashboard made the clock, and every process of the api; the file owns the web's worker only.
	f.withProcesses(web.ID, planWorker(), services.ProcessConfig{Name: "clock", StartCommand: []string{"clockwork"}, Singleton: true})
	f.withProcesses(api.ID, planWorker())
	v1 := planWebHead("8080") + planProcess("worker", planWorkerTable, "")
	f.markApplied(f.staging, v1)
	v2 := planWebHead("8080")
	before := f.snapshot()

	// Without prune the worker is only released; both live processes are counted as unmanaged.
	p := f.plan(f.staging, v2)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionUnchanged, ps.Action)
	require.Empty(t, ps.Changes)
	require.Equal(t, []string{"services.web.processes.worker"}, ps.Released)
	require.Equal(t, map[string]int{"processes": 2}, ps.Unmanaged, "counts of live processes the file does not mention")
	require.Zero(t, p.Summary.Pruned)

	// With prune only the process the file owned goes; the clock was never the file's.
	req := f.planReq(f.staging, v2)
	req.Prune = true
	p = f.planWith(req)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps = planSvc(t, p, "web")
	require.Equal(t, ActionUpdate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.processes.worker", Path: "services.web.processes.worker", Op: OpRemove, Applies: AppliesStaged, Reason: ReasonFileChanged, From: planSidekiq},
	})
	require.Equal(t, 1, p.Summary.Pruned)
	require.Equal(t, 1, p.Summary.Staged)
	require.Equal(t, []string{"web"}, p.Summary.RequiresDeploy)
	require.Equal(t, before, f.snapshot(), "planning a prune deletes nothing")

	// A service the file never managed keeps its processes, prune or not.
	withAPI := v2 + "\n[services.api]\nimage = \"nginx:1.27\"\nport = 8081\n"
	req = f.planReq(f.staging, withAPI)
	req.Prune = true
	p = f.planWith(req)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps = planSvc(t, p, "api")
	require.Empty(t, ps.Changes)
	require.Empty(t, ps.Released)
	require.Equal(t, map[string]int{"processes": 1}, ps.Unmanaged)
}

func TestPlanDB_ProcessScaleIsImmediate(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.withProcesses(web.ID, planWorker())
	two := 2
	_, err := f.scal.SetProcess(f.ctx, web.ID, "worker", scaling.SetInput{Replicas: &two})
	require.NoError(t, err)
	scaled := func(body string) string {
		return planWebHead("8080") + planProcess("worker", planWorkerTable, "\n[services.web.processes.worker.scale]\n"+body)
	}

	// The file says what is live: nothing to do.
	p := f.plan(f.staging, scaled("replicas = 2\n"))
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, ActionUnchanged, planSvc(t, p, "web").Action)

	// More replicas of the process: an immediate change, no deployment.
	file := scaled("replicas = 3\n")
	p = f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "processes.worker.scale.replicas", Path: "services.web.processes.worker.scale.replicas", Line: planLine(t, file, "replicas = 3"), Op: OpSet, Applies: AppliesImmediate, Reason: ReasonAdopt, From: "2", To: "3"},
	})
	require.Equal(t, 1, p.Summary.Immediate)
	require.Zero(t, p.Summary.Staged)
	require.Empty(t, p.Summary.RequiresDeploy)
	require.Equal(t, []string{"projects:write", "deployments:write"}, p.RequiredScopes)

	// Autoscaling keys are separate immediate fields; a process that was never autoscaled reads the defaults.
	auto := scaled("\n[services.web.processes.worker.scale.autoscaling]\nenabled = true\nmin = 2\nmax = 5\ntarget_cpu_percent = 60\n")
	p = f.plan(f.staging, auto)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	ps := planSvc(t, p, "web")
	require.ElementsMatch(t, []string{
		"processes.worker.scale.autoscaling.enabled", "processes.worker.scale.autoscaling.min",
		"processes.worker.scale.autoscaling.max", "processes.worker.scale.autoscaling.targetCpuPercent",
	}, planFieldsOf(ps.Changes))
	enabled := planChangeOf(t, ps, "processes.worker.scale.autoscaling.enabled")
	require.Equal(t, "false", enabled.From)
	require.Equal(t, "true", enabled.To)
	require.Equal(t, AppliesImmediate, enabled.Applies)
	require.Equal(t, "3", planChangeOf(t, ps, "processes.worker.scale.autoscaling.max").From, "the default of a process never autoscaled")
	require.Equal(t, 4, p.Summary.Immediate)
}

func TestPlanDB_ProcessScaleInProductionNeedsApproval(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.prod, "web", 8080)
	f.withProcesses(web.ID, planWorker())
	file := planWebHead("8080") + planProcess("worker", planWorkerTable, "\n[services.web.processes.worker.scale]\nreplicas = 2\n")

	p := f.plan(f.prod, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, 1, p.Summary.Immediate)
	require.Equal(t, ApprovalRequired, p.Guard.ProductionApproval)
	require.True(t, p.HasImmediateChanges())
}

func TestPlanDB_ReplicasOfAProcessNeedStatelessServices(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.withProcesses(web.ID, planWorker())
	file := planWebHead("8080") + planProcess("worker", planWorkerTable, "\n[services.web.processes.worker.scale]\nreplicas = 2\n")
	require.True(t, f.plan(f.staging, file).Valid)

	_, err := f.svcs.AddVolume(f.ctx, web.ID, "data", "/data")
	require.NoError(t, err)
	p := f.plan(f.staging, file)
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "replicas_require_stateless")
	require.Equal(t, "services.web.processes.worker.scale.replicas", d.Path)
	require.Equal(t, planLine(t, file, "replicas = 2"), d.Line)
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionBlocked, ps.Action)
	require.Contains(t, ps.Blockers, "replicas_require_stateless")

	// One replica of a process of a service with a volume is fine.
	one := strings.Replace(file, "replicas = 2", "replicas = 1", 1)
	require.True(t, f.plan(f.staging, one).Valid)
}

// A process the file does not mention still takes part in the platform's checks of the merged config.
func TestPlanDB_ProcessPortsAreCheckedAgainstLiveProcesses(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.withProcesses(web.ID, planMetrics())

	p := f.plan(f.staging, planWebHead("9100"))
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "invalid_value")
	require.Equal(t, "services.web", d.Path)
	require.Contains(t, d.Message, "port 9100")
	ps := planSvc(t, p, "web")
	require.Equal(t, ActionBlocked, ps.Action)
	require.Empty(t, ps.Changes)

	require.True(t, f.plan(f.staging, planWebHead("9200")).Valid)
}

func TestPlanDB_DomainOfAProcess(t *testing.T) {
	f := newPlanFixture(t)
	web := f.image(f.staging, "web", 8080)
	f.withProcesses(web.ID, planWorker(), planMetrics(), services.ProcessConfig{Name: "admin", StartCommand: []string{"bin/admin"}, Port: 9200})
	tables := planProcess("worker", planWorkerTable, "") + planProcess("metrics", `["bin/metrics"]`, "port = 9100\n") + planProcess("admin", `["bin/admin"]`, "port = 9200\n")
	metrics := "\n[services.web.domains.\"metrics.example.com\"]\nprocess = \"metrics\"\n"

	// A new domain of a process is a staged addition that names the process; a process is no proxy feature.
	p := f.plan(f.staging, planWebHead("8080")+tables+metrics)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	requireChanges(t, planSvc(t, p, "web").Changes, []planWant{
		{Field: "domains.metrics.example.com", Path: `services.web.domains."metrics.example.com"`, Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "process=metrics"},
	})
	require.Equal(t, []string{"projects:write"}, p.RequiredScopes, "a domain that only names its process needs no proxy:write")

	// Live domains: one of the service's own process, one of metrics.
	for host, comp := range map[string]string{"app.example.com": "", "metrics.example.com": "metrics"} {
		port := int32(8080)
		if comp != "" {
			port = 9100
		}
		_, err := f.q.InsertDomain(context.Background(), db.InsertDomainParams{
			ServiceID: web.ID, Hostname: host, TargetPort: port, Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto",
			Options: json.RawMessage(`{}`), Component: comp,
		})
		require.NoError(t, err)
	}
	app := "\n[services.web.domains.\"app.example.com\"]\nprocess = \"web\"\n"

	// The service's own process, by its default name, is the stored "": nothing changes.
	p = f.plan(f.staging, planWebHead("8080")+tables+metrics+app)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, planSvc(t, p, "web").Changes)

	// A live domain does not move to another process through the file.
	moved := "\n[services.web.domains.\"metrics.example.com\"]\nprocess = \"admin\"\n"
	p = f.plan(f.staging, planWebHead("8080")+tables+moved)
	require.False(t, p.Valid)
	d := planDiag(t, p.Diagnostics, "immutable_field")
	require.Equal(t, `services.web.domains."metrics.example.com"`, d.Path)
	require.Contains(t, planSvc(t, p, "web").Blockers, "immutable_field")

	// The new name of the default process is taken into account: its domain stays a no-op under it.
	renamed := planWebHead("8080") + "process_name = \"app\"\n" + tables + metrics + "\n[services.web.domains.\"app.example.com\"]\nprocess = \"app\"\n"
	p = f.plan(f.staging, renamed)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Equal(t, []string{"config.processName"}, planFieldsOf(planSvc(t, p, "web").Changes))
}

func TestPlanDB_CreateServiceWithProcesses(t *testing.T) {
	f := newPlanFixture(t)
	file := planHeadServer + `
[services.jobs]
image = "ghcr.io/acme/jobs:2"
port = 8080

[services.jobs.processes.worker]
command = ["bundle", "exec", "sidekiq"]
stop_grace_seconds = 120

[services.jobs.processes.worker.scale]
replicas = 2

[services.jobs.processes.metrics]
command = ["bin/metrics"]
port = 9100
`
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.Empty(t, p.Diagnostics)
	require.Equal(t, PlanSummary{Create: 1, Staged: 5, Immediate: 1, RequiresDeploy: []string{"jobs"}}, p.Summary)
	require.Equal(t, []string{"projects:write", "deployments:write"}, p.RequiredScopes)
	ps := planSvc(t, p, "jobs")
	require.Equal(t, ActionCreate, ps.Action)
	requireChanges(t, ps.Changes, []planWant{
		{Field: "config.image", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "ghcr.io/acme/jobs:2"},
		{Field: "config.port", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "8080"},
		{Field: "config.processes.metrics", Path: "services.jobs.processes.metrics", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: `["bin/metrics"] port 9100`},
		{Field: "config.processes.worker", Path: "services.jobs.processes.worker", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: planSidekiq + " grace 120s"},
		{Field: "processes.worker.scale.replicas", Path: "services.jobs.processes.worker.scale.replicas", Op: OpAdd, Applies: AppliesImmediate, Reason: ReasonNew, To: "2"},
		{Field: "server", Op: OpAdd, Applies: AppliesStaged, Reason: ReasonNew, To: "agent1"},
	})
}
