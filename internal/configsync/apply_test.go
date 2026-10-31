package configsync_test

// DB-backed integration tests of Service.Apply, ServiceSource, ReleaseFields,
// RunPending and RunPendingLoop (apply, ownership, release/prune,
// partial/deferred phases, scopes, stale plans, concurrency).
//
// Everything runs against a real schema through the real use cases. The
// immediate-kind collaborators (scaling, tasks, backups) are the real services
// wrapped in structs that can be told to fail.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
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

// ---- fakes ----

// applyDisp is the agent dispatcher: it records commands and delivers nothing.
type applyDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
}

func (d *applyDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	return uuid.New(), nil
}

func (d *applyDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *applyDisp) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cmds)
}

// applyFailure makes a wrapped use case fail on demand.
type applyFailure struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (a *applyFailure) set(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *applyFailure) hit() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.err
}

func (a *applyFailure) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

type applyScaler struct {
	*scaling.Service
	fail     applyFailure
	procFail applyFailure // makes SetProcess fail, apart from Set

	mu    sync.Mutex
	procs []string // the process of every SetProcess call, in order
}

func (s *applyScaler) SetProcess(ctx context.Context, id uuid.UUID, process string, in scaling.SetInput) (services.ServiceView, error) {
	s.mu.Lock()
	s.procs = append(s.procs, process)
	s.mu.Unlock()
	if err := s.procFail.hit(); err != nil {
		return services.ServiceView{}, err
	}
	return s.Service.SetProcess(ctx, id, process, in)
}

// processCalls lists the processes SetProcess was called for.
func (s *applyScaler) processCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.procs...)
}

func (s *applyScaler) Set(ctx context.Context, id uuid.UUID, in scaling.SetInput) (services.ServiceView, error) {
	if err := s.fail.hit(); err != nil {
		return services.ServiceView{}, err
	}
	return s.Service.Set(ctx, id, in)
}

type applyTasks struct {
	*tasks.Service
	fail applyFailure
}

func (s *applyTasks) CreateTask(ctx context.Context, serviceID uuid.UUID, in tasks.TaskInput) (tasks.TaskView, error) {
	if err := s.fail.hit(); err != nil {
		return tasks.TaskView{}, err
	}
	return s.Service.CreateTask(ctx, serviceID, in)
}

func (s *applyTasks) UpdateTask(ctx context.Context, id uuid.UUID, in tasks.TaskPatch) (tasks.TaskView, error) {
	if err := s.fail.hit(); err != nil {
		return tasks.TaskView{}, err
	}
	return s.Service.UpdateTask(ctx, id, in)
}

func (s *applyTasks) DeleteTask(ctx context.Context, id uuid.UUID) error {
	if err := s.fail.hit(); err != nil {
		return err
	}
	return s.Service.DeleteTask(ctx, id)
}

type applyBackups struct {
	*backups.Service
	fail applyFailure
}

func (s *applyBackups) CreateSchedule(ctx context.Context, serviceID uuid.UUID, in backups.ScheduleInput) (backups.ScheduleView, error) {
	if err := s.fail.hit(); err != nil {
		return backups.ScheduleView{}, err
	}
	return s.Service.CreateSchedule(ctx, serviceID, in)
}

func (s *applyBackups) UpdateSchedule(ctx context.Context, id uuid.UUID, p backups.SchedulePatch) (backups.ScheduleView, error) {
	if err := s.fail.hit(); err != nil {
		return backups.ScheduleView{}, err
	}
	return s.Service.UpdateSchedule(ctx, id, p)
}

func (s *applyBackups) DeleteSchedule(ctx context.Context, id uuid.UUID) error {
	if err := s.fail.hit(); err != nil {
		return err
	}
	return s.Service.DeleteSchedule(ctx, id)
}

type applyDeployCall struct {
	Env     uuid.UUID
	IDs     []uuid.UUID
	Message string
}

// applyDeployer records what an apply deploys.
type applyDeployer struct {
	mu    sync.Mutex
	calls []applyDeployCall
	made  []uuid.UUID
}

func (d *applyDeployer) DeployStaged(_ context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string) (deployments.StagedResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, applyDeployCall{Env: envID, IDs: append([]uuid.UUID(nil), serviceIDs...), Message: message})
	items := make([]deployments.DeploymentView, 0, len(serviceIDs))
	for _, id := range serviceIDs {
		v := deployments.DeploymentView{ID: uuid.New(), ServiceID: id}
		d.made = append(d.made, v.ID)
		items = append(items, v)
	}
	return deployments.StagedResult{Items: items, Order: append([]uuid.UUID(nil), serviceIDs...)}, nil
}

func (d *applyDeployer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *applyDeployer) lastCall() applyDeployCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[len(d.calls)-1]
}

// applyLogBuf is a concurrency-safe log sink.
type applyLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *applyLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *applyLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ---- fixture ----

type applyFixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	box  *secretbox.Box
	bus  *events.Bus
	logs *applyLogBuf
	cfg  *configsync.Service
	disp *applyDisp

	svcs *services.Service
	vars *variables.Service
	doms *domains.Service
	nets *networks.Service
	upt  *uptime.Service

	scaler *applyScaler
	tsk    *applyTasks
	bkp    *applyBackups
	deploy *applyDeployer

	ws      db.Workspace
	userID  uuid.UUID
	project db.Project
	staging db.Environment
	prod    db.Environment
	server  db.Server
	dest    backups.DestinationView
	admin   context.Context
}

func newApplyFixture(t *testing.T) *applyFixture {
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
	staging, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	ip := "203.0.113.9"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1", PublicIp: &ip})
	require.NoError(t, err)
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapProxyV2}})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', base_domain = 'example.com', facts = $2 WHERE id = $1", srv.ID, facts)
	require.NoError(t, err)
	srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logs := &applyLogBuf{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	disp := &applyDisp{}

	svcs := services.NewService(pool, bus, box, disp, logger)
	doms := domains.NewService(pool, bus, jm, box)
	domains.RegisterJobs(jm, doms, logger)
	routes := proxyroutes.NewService(pool, bus, jm, disp, svcs, logger)
	svcs.SetRouteHook(routes)
	vars := variables.NewService(pool, bus, box)
	nets := networks.NewService(pool, bus)
	dbs := databases.NewService(pool, bus, box, nil, svcs, logger)
	upt := uptime.NewService(uptime.Deps{Pool: pool, Bus: bus, Logger: logger})
	scaler := &applyScaler{Service: scaling.NewService(pool, bus, disp, svcs, logger)}
	tsk := &applyTasks{Service: tasks.NewService(tasks.Deps{Pool: pool, Bus: bus, Box: box, Logger: logger})}
	bkp := &applyBackups{Service: backups.NewService(backups.Deps{Pool: pool, Bus: bus, Box: box, Logger: logger})}
	deploy := &applyDeployer{}

	cfg := configsync.NewService(configsync.Deps{
		Pool: pool, Bus: bus, Box: box, Logger: logger, PublicURL: "https://nebula.example.com",
		Services: svcs, Variables: vars, Domains: doms, Databases: dbs, Routes: routes, Networks: nets,
		Scaling: scaler, Tasks: tsk, Backups: bkp, Uptime: upt, Deploy: deploy,
	})

	admin := auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "owner@example.com", Scopes: policy.AllScopes,
	})
	dest, err := bkp.Service.CreateDestination(admin, backups.DestinationInput{Name: "offsite", Kind: "local", LocalDir: "nightly"})
	require.NoError(t, err)

	return &applyFixture{
		t: t, pool: pool, q: q, box: box, bus: bus, logs: logs, cfg: cfg, disp: disp,
		svcs: svcs, vars: vars, doms: doms, nets: nets, upt: upt,
		scaler: scaler, tsk: tsk, bkp: bkp, deploy: deploy,
		ws: ws, userID: u.ID, project: prj, staging: staging, prod: prod, server: srv, dest: dest, admin: admin,
	}
}

// ---- principals ----

func (f *applyFixture) token(scopes ...string) context.Context {
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &id, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "ci-token", Scopes: scopes,
	})
}

func (f *applyFixture) mcp() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeMCP, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "claude", Scopes: policy.AllScopes,
	})
}

// ---- requests ----

type applyOpt func(*configsync.ApplyRequest)

func applyWithPlanID(id string) applyOpt {
	return func(r *configsync.ApplyRequest) { r.PlanID = id }
}

func applyWithPrune() applyOpt { return func(r *configsync.ApplyRequest) { r.Prune = true } }

func applyWithDeploy() applyOpt { return func(r *configsync.ApplyRequest) { r.Deploy = true } }

func applyWithSource(s configsync.SourceInfo) applyOpt {
	return func(r *configsync.ApplyRequest) { r.Input.Source = s }
}

func applyReq(env, file string, opts ...applyOpt) configsync.ApplyRequest {
	r := configsync.ApplyRequest{PlanRequest: configsync.PlanRequest{Environment: env, Input: configsync.Input{File: file}}}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func (f *applyFixture) plan(env, file string, opts ...applyOpt) configsync.Plan {
	f.t.Helper()
	p, err := f.cfg.Plan(f.admin, f.project.ID, applyReq(env, file, opts...).PlanRequest)
	require.NoError(f.t, err)
	return p
}

func (f *applyFixture) apply(ctx context.Context, env, file string, opts ...applyOpt) (configsync.ApplyResult, error) {
	return f.cfg.Apply(ctx, f.project.ID, applyReq(env, file, opts...))
}

func (f *applyFixture) mustApply(env, file string, opts ...applyOpt) configsync.ApplyResult {
	f.t.Helper()
	res, err := f.apply(f.admin, env, file, opts...)
	require.NoError(f.t, err)
	return res
}

// ---- reads ----

func (f *applyFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func (f *applyFixture) hasSvc(env db.Environment, slug string) bool {
	f.t.Helper()
	rows, err := f.q.ListEnvironmentServicesAll(context.Background(), env.ID)
	require.NoError(f.t, err)
	for _, r := range rows {
		if r.Slug == slug {
			return true
		}
	}
	return false
}

func (f *applyFixture) svcRow(env db.Environment, slug string) db.Service {
	f.t.Helper()
	rows, err := f.q.ListEnvironmentServicesAll(context.Background(), env.ID)
	require.NoError(f.t, err)
	for _, r := range rows {
		if r.Slug == slug {
			return r
		}
	}
	f.t.Fatalf("no service %q in %s", slug, env.Slug)
	return db.Service{}
}

func (f *applyFixture) row(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *applyFixture) cfgOf(id uuid.UUID) services.Config {
	f.t.Helper()
	return services.DecodeConfig(f.row(id).Config)
}

func (f *applyFixture) hasSource(id uuid.UUID) bool {
	f.t.Helper()
	return f.count("SELECT count(*) FROM config_sources WHERE service_id = $1", id) == 1
}

func (f *applyFixture) source(id uuid.UUID) db.ConfigSource {
	f.t.Helper()
	s, err := f.q.GetConfigSource(context.Background(), id)
	require.NoError(f.t, err)
	return s
}

func (f *applyFixture) owned(id uuid.UUID) map[string]string {
	f.t.Helper()
	out := map[string]string{}
	require.NoError(f.t, json.Unmarshal(f.source(id).Owned, &out))
	return out
}

func (f *applyFixture) pendingOf(id uuid.UUID) map[string]json.RawMessage {
	f.t.Helper()
	out := map[string]json.RawMessage{}
	require.NoError(f.t, json.Unmarshal(f.source(id).Pending, &out))
	return out
}

func (f *applyFixture) rawVars(id uuid.UUID) map[string]services.RawVar {
	f.t.Helper()
	raw, err := services.LoadRawVars(context.Background(), f.q, f.box, id)
	require.NoError(f.t, err)
	return raw
}

func (f *applyFixture) domainRows(id uuid.UUID) map[string]db.Domain {
	f.t.Helper()
	rows, err := f.q.ListServiceDomains(context.Background(), id)
	require.NoError(f.t, err)
	out := map[string]db.Domain{}
	for _, r := range rows {
		out[r.Hostname+r.PathPrefix] = r
	}
	return out
}

func (f *applyFixture) volumeRows(id uuid.UUID) map[string]string {
	f.t.Helper()
	rows, err := f.q.ListServiceVolumes(context.Background(), id)
	require.NoError(f.t, err)
	out := map[string]string{}
	for _, r := range rows {
		out[r.Name] = r.MountPath
	}
	return out
}

func (f *applyFixture) taskRows(id uuid.UUID) map[string]db.ServiceTask {
	f.t.Helper()
	rows, err := f.q.ListServiceTasks(context.Background(), id)
	require.NoError(f.t, err)
	out := map[string]db.ServiceTask{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

func (f *applyFixture) scheduleRows(id uuid.UUID) []db.BackupSchedule {
	f.t.Helper()
	rows, err := f.q.ListBackupSchedulesByService(context.Background(), id)
	require.NoError(f.t, err)
	return rows
}

func (f *applyFixture) linkPairs(env db.Environment) map[string]bool {
	f.t.Helper()
	rows, err := f.q.ListEnvironmentLinks(context.Background(), env.ID)
	require.NoError(f.t, err)
	out := map[string]bool{}
	for _, l := range rows {
		out[l.FromServiceID.String()+">"+l.ToServiceID.String()] = true
	}
	return out
}

func (f *applyFixture) monitor(id uuid.UUID) db.UptimeMonitor {
	f.t.Helper()
	m, err := f.q.GetUptimeMonitor(context.Background(), id)
	require.NoError(f.t, err)
	return m
}

func (f *applyFixture) ownedShared(env db.Environment) map[string]string {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT owned_shared FROM environment_config_settings WHERE environment_id = $1", env.ID).Scan(&raw))
	out := map[string]string{}
	require.NoError(f.t, json.Unmarshal(raw, &out))
	return out
}

// ---- audit ----

type applyAudit struct {
	ID         int64
	Action     string
	Resource   string
	ResourceID string
	ActorType  string
	ActorLabel string
	Meta       map[string]any
}

func (f *applyFixture) audits(where string, args ...any) []applyAudit {
	f.t.Helper()
	query := "SELECT id, action, resource_type, resource_id, actor_type, actor_label, metadata FROM audit_events"
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY id"
	rows, err := f.pool.Query(context.Background(), query, args...)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []applyAudit
	for rows.Next() {
		var a applyAudit
		var raw []byte
		require.NoError(f.t, rows.Scan(&a.ID, &a.Action, &a.Resource, &a.ResourceID, &a.ActorType, &a.ActorLabel, &raw))
		require.NoError(f.t, json.Unmarshal(raw, &a.Meta))
		out = append(out, a)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *applyFixture) auditMark() int64 {
	f.t.Helper()
	var n int64
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT coalesce(max(id), 0) FROM audit_events").Scan(&n))
	return n
}

func (f *applyFixture) auditText() string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT coalesce(string_agg(action || ' ' || resource_id || ' ' || metadata::text, ' '), '') FROM audit_events").Scan(&s))
	return s
}

// applyState is a snapshot of everything an apply may write.
type applyState struct {
	Gens   map[string]int64
	Counts map[string]int
}

var applyCountTables = []string{
	"services", "config_sources", "environment_config_settings", "service_variables", "shared_variables", "domains", "volumes",
	"service_links", "service_networks", "service_tasks", "backup_schedules", "uptime_monitors", "audit_events", "river_job",
}

func (f *applyFixture) state() applyState {
	f.t.Helper()
	st := applyState{Gens: map[string]int64{}, Counts: map[string]int{}}
	rows, err := f.pool.Query(context.Background(), "SELECT id::text, generation FROM services")
	require.NoError(f.t, err)
	defer rows.Close()
	for rows.Next() {
		var id string
		var gen int64
		require.NoError(f.t, rows.Scan(&id, &gen))
		st.Gens[id] = gen
	}
	require.NoError(f.t, rows.Err())
	for _, tbl := range applyCountTables {
		st.Counts[tbl] = f.count("SELECT count(*) FROM " + tbl)
	}
	return st
}

// ---- UI-side changes (what the dashboard does) ----

func (f *applyFixture) uiService(env db.Environment, slug, kind, source string, cfg services.Config) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: source, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *applyFixture) setVar(id uuid.UUID, key, value string, secret bool) {
	f.t.Helper()
	in := variables.VariableInput{Key: key, Value: &value}
	if secret {
		t := true
		in.IsSecret = &t
	}
	_, err := f.vars.Put(f.admin, id, []variables.VariableInput{in})
	require.NoError(f.t, err)
}

func (f *applyFixture) network(name string) networks.NetworkView {
	f.t.Helper()
	v, err := f.nets.Create(f.admin, name, f.server.ID)
	require.NoError(f.t, err)
	return v
}

func (f *applyFixture) setPort(id uuid.UUID, port int) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		"UPDATE services SET config = jsonb_set(config, '{port}', to_jsonb($2::int)), generation = generation + 1 WHERE id = $1", id, port)
	require.NoError(f.t, err)
}

// storeLastPlan stores p as the environment's last plan (what the git check does).
func (f *applyFixture) storeLastPlan(env db.Environment, p configsync.Plan) {
	f.t.Helper()
	raw, err := json.Marshal(configsync.NewLastPlan(p))
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), `
		INSERT INTO environment_config_settings (environment_id, workspace_id, last_plan) VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (environment_id) DO UPDATE SET last_plan = EXCLUDED.last_plan`, env.ID, f.ws.ID, string(raw))
	require.NoError(f.t, err)
}

// ---- small helpers ----

// applyQuietCount counts rows without failing the test (for polling from another goroutine); -1 on error.
func applyQuietCount(pool *pgxpool.Pool, query string, args ...any) int {
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		return -1
	}
	return n
}

func applyJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return string(b)
}

func applyFileSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func applyRequireError(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	require.Error(t, err)
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %T: %v", err, err)
	require.Equal(t, status, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code)
	return ae
}

func applyStepIndex(res configsync.ApplyResult, name string) int {
	for i, s := range res.Steps {
		if s.Step == name {
			return i
		}
	}
	return -1
}

func applyStepStatus(res configsync.ApplyResult, name string) string {
	for _, s := range res.Steps {
		if s.Step == name {
			return s.Status
		}
	}
	return ""
}

func applyStepMessage(res configsync.ApplyResult, name string) string {
	for _, s := range res.Steps {
		if s.Step == name {
			return s.Message
		}
	}
	return ""
}

func applyPlanService(t *testing.T, p configsync.Plan, slug string) configsync.PlanService {
	t.Helper()
	for _, s := range p.Services {
		if s.Slug == slug {
			return s
		}
	}
	t.Fatalf("no service %q in the plan: %s", slug, applyJSON(p.Services))
	return configsync.PlanService{}
}

func applyChangeFields(s configsync.PlanService) []string {
	out := []string{}
	for _, c := range s.Changes {
		out = append(out, c.Field)
	}
	sort.Strings(out)
	return out
}

func applyRemovedFields(p configsync.Plan) map[string][]string {
	out := map[string][]string{}
	for _, s := range p.Services {
		for _, c := range s.Changes {
			if c.Op == configsync.OpRemove {
				out[s.Slug] = append(out[s.Slug], c.Field)
			}
		}
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

func applyAppliedService(t *testing.T, res configsync.ApplyResult, slug string) configsync.AppliedService {
	t.Helper()
	for _, s := range res.Services {
		if s.Slug == slug {
			return s
		}
	}
	t.Fatalf("no service %q in the result: %s", slug, applyJSON(res.Services))
	return configsync.AppliedService{}
}

func applyOwnedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func applyRequireNoText(t *testing.T, haystackName, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		require.NotEmpty(t, n, "a planted secret must not be empty")
		require.NotContains(t, haystack, n, "%s leaks %q", haystackName, n)
	}
}

// fp is the fingerprint the plan uses for a value.
func applyFP(v any) string { return projectconfig.Fingerprint(v) }

// varFP is the fingerprint of a plain variable without metadata.
func applyVarFP(text string, secret bool) string {
	return applyFP(map[string]any{"t": text, "secret": secret, "scope": "runtime"})
}

// ---- files ----

const applyKindsFile = `schema = 1
server = "agent1"

[services.pg.database]
engine = "postgres"
version = "17"

[services.app]
image = "nginx:1.27"
port = 8080

[services.app.variables]
DATABASE_URL = "${{pg.DATABASE_URL}}"

[services.site]
port = 3000

[services.site.git]
url = "https://github.com/acme/shop.git"
branch = "main"

[services.suite.stack]
compose = '''
services:
  web:
    image: nginx:1.27-alpine
'''

[services.a-edge.route]
protocol = "http"
upstreams = [{ service = "app", port = 8080 }]
`

const applyFullFile = `schema = 1
server = "agent1"

[variables]
APP_NAME = "shared-sentinel"

[environments.staging.variables]
APP_ENV = "staging-sentinel"

[services.pg.database]
engine = "postgres"
version = "17"

[services.pg.uptime]
reminder_minutes = 30

[services.app]
name = "Application"
image = "nginx:1.27"
port = 8080
depends_on = ["pg"]
networks = ["backplane"]

[services.app.variables]
GREETING = "hello-sentinel"
DATABASE_URL = "${{pg.DATABASE_URL}}"
SESSION_SECRET = "${{secret(24)}}"

[services.app.secrets]
keys = ["STRIPE_SECRET_KEY"]

[services.app.volumes]
data = "/data"

[services.app.domains."app.example.com"]

[services.api]
image = "nginx:1.27"
port = 9090

[services.api.scale]
replicas = 2

[services.api.uptime]
failure_threshold = 5

[services.api.tasks.digest]
schedule = "0 7 * * 1-5"
command = ["echo", "digest"]
timeout_seconds = 900
`

func applyWebFile(port int, extra string) string {
	return fmt.Sprintf("schema = 1\nserver = \"agent1\"\n\n[services.web]\nimage = \"nginx:1.27\"\nport = %d\n%s", port, extra)
}

// ---- tests ----

func TestApplyCreatesServicesOfEveryKindInOneTransaction(t *testing.T) {
	f := newApplyFixture(t)

	pl := f.plan("staging", applyKindsFile)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Len(t, pl.Services, 5)
	for _, s := range pl.Services {
		require.Equal(t, configsync.ActionCreate, s.Action, s.Slug)
	}
	require.Equal(t, 5, pl.Summary.Create)

	mark := f.auditMark()
	res := f.mustApply("staging", applyKindsFile, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, pl.PlanID, res.PlanID)
	for _, s := range res.Steps {
		require.NotEqual(t, configsync.StepFailed, s.Status, applyJSON(s))
	}

	want := map[string][2]string{
		"pg":     {services.KindDatabase, services.SourceImage},
		"app":    {services.KindApp, services.SourceImage},
		"site":   {services.KindApp, services.SourceGit},
		"suite":  {services.KindStack, services.SourceCompose},
		"a-edge": {services.KindRoute, services.SourceExternal},
	}
	for slug, ks := range want {
		row := f.svcRow(f.staging, slug)
		require.Equal(t, slug, row.Slug, "the slug of the file is used verbatim")
		require.Equal(t, ks[0], row.Kind, slug)
		require.Equal(t, ks[1], row.SourceType, slug)
		require.NotNil(t, row.ServerID, slug)
		require.Equal(t, f.server.ID, *row.ServerID, slug)
		require.Equal(t, configsync.StepApplied, applyStepStatus(res, "service.create:"+slug), slug)
		as := applyAppliedService(t, res, slug)
		require.Equal(t, configsync.ActionCreate, as.Action)
		require.Equal(t, row.ID, as.ServiceID)
		require.Equal(t, row.Generation, as.Generation, slug)
		require.True(t, f.hasSource(row.ID), "an ownership row for "+slug)
	}

	// Providers of references are created first, inside the same transaction.
	require.Less(t, applyStepIndex(res, "service.create:pg"), applyStepIndex(res, "service.create:app"), "the database comes before the service referencing it")
	require.Less(t, applyStepIndex(res, "service.create:app"), applyStepIndex(res, "service.create:a-edge"), "the upstream comes before the route")

	app := f.svcRow(f.staging, "app")
	require.Equal(t, "${{pg.DATABASE_URL}}", f.rawVars(app.ID)["DATABASE_URL"].Value)

	// The route's placeholder upstream id was replaced by the created service's id.
	edge := f.cfgOf(f.svcRow(f.staging, "a-edge").ID)
	require.NotNil(t, edge.Route)
	require.Equal(t, "http", edge.Route.Protocol)
	require.Len(t, edge.Route.Upstreams, 1)
	require.NotNil(t, edge.Route.Upstreams[0].ServiceID)
	require.Equal(t, app.ID, *edge.Route.Upstreams[0].ServiceID)
	require.Equal(t, 8080, edge.Route.Upstreams[0].Port)

	site := f.cfgOf(f.svcRow(f.staging, "site").ID)
	require.Equal(t, "https://github.com/acme/shop.git", site.GitURL)
	require.Equal(t, "main", site.GitBranch)
	require.Equal(t, 5, f.count("SELECT count(*) FROM config_sources"))

	// One config.apply entry, the others tagged with the file as their origin.
	applies := f.audits("action = 'config.apply'")
	require.Len(t, applies, 1)
	require.Equal(t, "environment", applies[0].Resource)
	require.Equal(t, f.staging.ID.String(), applies[0].ResourceID)
	for _, a := range f.audits("id > $1", mark) {
		require.Equal(t, "nebula.toml", a.Meta["via"], "%s must carry via=nebula.toml", a.Action)
	}
}

func TestApplyWritesEveryFieldFamilyAndRecordsOwnership(t *testing.T) {
	f := newApplyFixture(t)
	f.network("backplane")

	pl := f.plan("staging", applyFullFile)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.ElementsMatch(t, []string{"projects:write", "deployments:write", "admin"}, pl.RequiredScopes)
	require.Equal(t, configsync.ApprovalNotRequired, pl.Guard.ProductionApproval)
	require.Positive(t, pl.Summary.Immediate)
	require.ElementsMatch(t, []string{"pg", "app", "api"}, pl.Summary.RequiresDeploy)

	mark := f.auditMark()
	res := f.mustApply("staging", applyFullFile, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	for _, s := range res.Steps {
		require.NotEqual(t, configsync.StepFailed, s.Status, applyJSON(s))
	}
	for _, name := range []string{
		"service.create:pg", "service.create:app", "service.create:api", "variables:app", "domains:app", "volumes:app", "networks:app",
		"links:app", "shared:project", "shared:environment", "scale:api", "tasks:api:digest", "uptime:pg", "uptime:api",
		"ownership:pg", "ownership:app", "ownership:api",
	} {
		require.Equal(t, configsync.StepApplied, applyStepStatus(res, name), name+" in "+applyJSON(res.Steps))
	}
	require.Less(t, applyStepIndex(res, "service.create:pg"), applyStepIndex(res, "service.create:app"))

	pg, app, api := f.svcRow(f.staging, "pg"), f.svcRow(f.staging, "app"), f.svcRow(f.staging, "api")
	require.Equal(t, services.KindDatabase, pg.Kind)
	require.Equal(t, "Application", app.Name)
	require.Equal(t, "api", api.Name, "the name defaults to the slug")

	// Variables, secrets and generators.
	raw := f.rawVars(app.ID)
	require.Equal(t, "hello-sentinel", raw["GREETING"].Value)
	require.Equal(t, "${{pg.DATABASE_URL}}", raw["DATABASE_URL"].Value)
	require.True(t, raw["SESSION_SECRET"].Secret)
	require.Len(t, raw["SESSION_SECRET"].Value, 24, "the generator ran once")
	require.Equal(t, "secret(24)", raw["SESSION_SECRET"].Gen)
	require.True(t, raw["STRIPE_SECRET_KEY"].Secret, "a declared secret exists without a value")

	// Domains, volumes, networks, links.
	doms := f.domainRows(app.ID)
	require.Len(t, doms, 1)
	require.Equal(t, int32(8080), doms["app.example.com"].TargetPort)
	require.Equal(t, map[string]string{"data": "/data"}, f.volumeRows(app.ID))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_networks WHERE service_id = $1", app.ID))
	require.True(t, f.linkPairs(f.staging)[app.ID.String()+">"+pg.ID.String()], "depends_on links the consumer to the provider")

	// Immediate settings ran after the commit.
	require.Equal(t, int32(2), api.Replicas)
	require.Contains(t, f.taskRows(api.ID), "digest")
	require.Equal(t, "0 7 * * 1-5", f.taskRows(api.ID)["digest"].Schedule)
	require.EqualValues(t, 5, f.monitor(api.ID).FailureThreshold)
	require.EqualValues(t, 30, f.monitor(pg.ID).ReminderMinutes)
	require.EqualValues(t, 3, f.monitor(pg.ID).FailureThreshold, "the keys the file does not write keep the defaults")

	// Both shared layers.
	proj, err := f.q.ListSharedVariablesProject(context.Background(), f.project.ID)
	require.NoError(t, err)
	require.Len(t, proj, 1)
	require.Equal(t, "APP_NAME", proj[0].Key)
	envVars, err := f.q.ListSharedVariablesEnv(context.Background(), &f.staging.ID)
	require.NoError(t, err)
	require.Len(t, envVars, 1)
	require.Equal(t, "APP_ENV", envVars[0].Key)
	require.Equal(t, map[string]string{
		"project.APP_NAME":    applyVarFP("shared-sentinel", false),
		"environment.APP_ENV": applyVarFP("staging-sentinel", false),
	}, f.ownedShared(f.staging))

	// The ownership row: where it came from and the fingerprints of what was written.
	src := f.source(app.ID)
	require.Equal(t, configsync.OriginUI, src.Origin)
	require.Equal(t, "user", src.AppliedByType)
	require.Equal(t, "owner@example.com", src.AppliedByLabel)
	require.Equal(t, applyFileSHA(applyFullFile), src.FileSha256)
	require.EqualValues(t, 1, src.SchemaVersion)
	require.Equal(t, f.row(app.ID).Generation, src.AppliedGeneration)
	require.Equal(t, map[string]json.RawMessage{}, f.pendingOf(app.ID))
	ownedApp := f.owned(app.ID)
	require.Equal(t, map[string]string{
		"name":                               applyFP("Application"),
		"server":                             applyFP(f.server.ID.String()),
		"config.image":                       applyFP("nginx:1.27"),
		"config.port":                        applyFP(8080),
		"links.pg":                           applyFP(map[string]any{"dependsOn": "pg"}),
		"networks.backplane":                 applyFP(map[string]any{"attached": "backplane"}),
		"variables.GREETING":                 applyVarFP("hello-sentinel", false),
		"variables.DATABASE_URL":             applyVarFP("${{pg.DATABASE_URL}}", false),
		"variables.SESSION_SECRET":           applyVarFP("${{secret(24)}}", true),
		"variables.STRIPE_SECRET_KEY#secret": applyFP(map[string]any{"secret": true, "scope": "runtime"}),
		"volumes.data":                       applyFP(projectconfig.Volume{Name: "data", MountPath: "/data"}),
		"domains.app.example.com":            applyFP(map[string]any{}),
	}, ownedApp)
	ownedAPI := f.owned(api.ID)
	require.Equal(t, applyFP(2), ownedAPI["scale.replicas"], "an immediate field is owned once its step succeeded")
	require.Equal(t, applyFP(5), ownedAPI["uptime.failureThreshold"])
	require.Contains(t, ownedAPI, "tasks.digest")
	require.Equal(t, applyFP(30), f.owned(pg.ID)["uptime.reminderMinutes"])

	// One audit entry for the apply: field ids and counts, never values.
	applies := f.audits("action = 'config.apply'")
	require.Len(t, applies, 1)
	meta := applies[0].Meta
	require.Equal(t, "ui", meta["origin"])
	require.Equal(t, res.PlanID, meta["planId"])
	require.Equal(t, "nebula.toml", meta["via"])
	require.Equal(t, []any{"api", "app", "pg"}, meta["services"])
	fields, ok := meta["fields"].(map[string]any)
	require.True(t, ok, "fields are listed per service")
	require.Contains(t, fields["app"], "config.port")
	require.Contains(t, fields["app"], "variables.GREETING")
	metaText := applyJSON(meta)
	applyRequireNoText(t, "config.apply metadata", metaText, "hello-sentinel", "shared-sentinel", "staging-sentinel", "nginx:1.27", raw["SESSION_SECRET"].Value)
	for _, a := range f.audits("id > $1", mark) {
		require.Equal(t, "nebula.toml", a.Meta["via"], "%s must carry via=nebula.toml", a.Action)
	}

	// The result and the logs never carry the generated secret either.
	applyRequireNoText(t, "result", applyJSON(res), raw["SESSION_SECRET"].Value)
	applyRequireNoText(t, "plan", applyJSON(pl), raw["SESSION_SECRET"].Value)
	applyRequireNoText(t, "logs", f.logs.String(), raw["SESSION_SECRET"].Value)
	for _, c := range applyPlanService(t, pl, "app").Changes {
		if c.Field == "variables.SESSION_SECRET" {
			require.True(t, c.Masked)
			require.Equal(t, "generated(secret(24))", c.To)
		}
	}
	require.True(t, pl.Diagnostics.HasCode("secret_missing_value"), "the declared secret has no value yet")
}

func TestApplySecondIdenticalApplyIsANoopWithNoTrace(t *testing.T) {
	f := newApplyFixture(t)
	f.network("backplane")
	f.mustApply("staging", applyFullFile)
	app := f.svcRow(f.staging, "app")
	appliedAt := f.source(app.ID).AppliedAt
	before := f.state()

	pl := f.plan("staging", applyFullFile)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	for _, s := range pl.Services {
		require.Equal(t, configsync.ActionUnchanged, s.Action, s.Slug+": "+applyJSON(s.Changes))
		require.Empty(t, s.Changes, s.Slug)
		require.Equal(t, configsync.StateInSync, s.State, s.Slug)
	}

	res := f.mustApply("staging", applyFullFile, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusNoop, res.Status)
	require.Equal(t, pl.PlanID, res.PlanID, "the same plan id")
	require.Empty(t, res.Steps)
	for _, s := range res.Services {
		require.Equal(t, configsync.ActionUnchanged, s.Action, s.Slug)
	}
	require.Equal(t, before, f.state(), "no row, no generation, no audit entry, no job")
	require.Equal(t, appliedAt, f.source(app.ID).AppliedAt)
	require.Len(t, f.audits("action = 'config.apply'"), 1)

	// And again, without a plan id.
	again := f.mustApply("staging", applyFullFile)
	require.Equal(t, configsync.StatusNoop, again.Status)
	require.Equal(t, pl.PlanID, again.PlanID)
	require.Equal(t, before, f.state())
}

func TestApplyNeverWipesSettingsTheFileDoesNotOwn(t *testing.T) {
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceGit, services.Config{
		GitURL: "https://github.com/acme/shop.git", GitBranch: "main", Port: 3000, MemoryMB: 256, StartCommand: []string{"bun", "run", "start"},
		Files:         []services.FileMount{{Path: "/etc/app/app.conf", Mode: "0640", Content: "listen = 3000\n"}},
		Docker:        &agentprotocol.DockerOptions{NoNewPrivileges: true, CapDrop: []string{"ALL"}},
		Tailnet:       &tailnetcfg.TailnetConfig{Enabled: true, Hostname: "web-tailnet", Web: true},
		BuildServerID: &f.server.ID,
	})
	f.setVar(web.ID, "UI_ONLY", "kept", false)
	before := f.row(web.ID)

	const file = "schema = 1\n\n[services.web]\nport = 8080\n\n[services.web.git]\nurl = \"https://github.com/acme/shop.git\"\nbranch = \"main\"\n"
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, 1, pl.Summary.Adopted)
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, configsync.ActionUpdate, ps.Action)
	require.Len(t, ps.Changes, 1, applyJSON(ps.Changes))
	c := ps.Changes[0]
	require.Equal(t, "config.port", c.Field)
	require.Equal(t, configsync.OpSet, c.Op)
	require.Equal(t, configsync.AppliesStaged, c.Applies)
	require.Equal(t, configsync.ReasonAdopt, c.Reason)
	require.Equal(t, "3000", c.From)
	require.Equal(t, "8080", c.To)

	res := f.mustApply("staging", file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	after := f.row(web.ID)

	diff := services.DiffConfig("config", services.DecodeConfig(before.Config).WithDefaults(), services.DecodeConfig(after.Config).WithDefaults())
	require.Len(t, diff, 1, applyJSON(diff))
	require.Equal(t, "config.port", diff[0].Field)
	require.Equal(t, "3000", diff[0].From)
	require.Equal(t, "8080", diff[0].To)

	cfg := services.DecodeConfig(after.Config)
	require.NotNil(t, cfg.Tailnet, "the tailnet survives")
	require.Equal(t, "web-tailnet", cfg.Tailnet.Hostname)
	require.Len(t, cfg.Files, 1, "the files survive")
	require.Equal(t, "0640", cfg.Files[0].Mode)
	require.NotNil(t, cfg.Docker, "the docker options survive")
	require.Equal(t, []string{"ALL"}, cfg.Docker.CapDrop)
	require.NotNil(t, cfg.BuildServerID, "the build server survives")
	require.Equal(t, f.server.ID, *cfg.BuildServerID)
	require.Equal(t, 256, cfg.MemoryMB)
	require.Equal(t, []string{"bun", "run", "start"}, cfg.StartCommand)
	require.Equal(t, before.Name, after.Name)
	require.Equal(t, before.ServerID, after.ServerID)
	require.Equal(t, "kept", f.rawVars(web.ID)["UI_ONLY"].Value)

	require.Equal(t, before.Generation+1, after.Generation, "the generation advances once")
	require.Equal(t, after.Generation, applyAppliedService(t, res, "web").Generation)

	owned := f.owned(web.ID)
	require.Equal(t, []string{"config.gitBranch", "config.gitUrl", "config.port"}, applyOwnedKeys(owned), "only the keys of the file are owned (git.branch is required with a git source)")
	require.Equal(t, applyFP(8080), owned["config.port"])

	// A second change of the same key keeps everything else too.
	const file2 = "schema = 1\n\n[services.web]\nport = 9000\n\n[services.web.git]\nurl = \"https://github.com/acme/shop.git\"\nbranch = \"main\"\n"
	f.mustApply("staging", file2)
	again := f.row(web.ID)
	diff = services.DiffConfig("config", services.DecodeConfig(after.Config).WithDefaults(), services.DecodeConfig(again.Config).WithDefaults())
	require.Len(t, diff, 1, applyJSON(diff))
	require.Equal(t, "config.port", diff[0].Field)
	require.Equal(t, after.Generation+1, again.Generation)
}

func TestApplySwitchesAnImageServiceToGitAsAStagedChange(t *testing.T) {
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000, MemoryMB: 256})
	f.setVar(web.ID, "KEEP", "1", false)
	vol, err := f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: web.ID, Name: "data", MountPath: "/data"})
	require.NoError(t, err)
	before := f.row(web.ID)

	const file = "schema = 1\n\n[services.web]\nport = 8080\n\n[services.web.git]\nurl = \"https://github.com/acme/shop.git\"\nbranch = \"main\"\n"
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, configsync.ActionUpdate, ps.Action)
	require.Equal(t, "git", ps.Kind)
	src := ps.Changes[0]
	require.Equal(t, services.FieldSource, src.Field, "the plan shows the switch first: %s", applyJSON(ps.Changes))
	require.Equal(t, configsync.OpSet, src.Op)
	require.Equal(t, configsync.AppliesStaged, src.Applies)
	require.Equal(t, "image", src.From)
	require.Equal(t, "git", src.To)
	require.Equal(t, "services.web.git", src.Path)
	require.Contains(t, pl.Summary.RequiresDeploy, "web")

	res := f.mustApply("staging", file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "source:web"))

	after := f.row(web.ID)
	require.Equal(t, services.SourceGit, after.SourceType)
	cfg := services.DecodeConfig(after.Config)
	require.Equal(t, "https://github.com/acme/shop.git", cfg.GitURL)
	require.Equal(t, "main", cfg.GitBranch)
	require.Empty(t, cfg.Image, "the image is cleared")
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, 256, cfg.MemoryMB, "settings the file does not own are kept")
	require.Equal(t, "1", f.rawVars(web.ID)["KEEP"].Value)
	require.Len(t, f.volumeRows(web.ID), 1)
	require.Contains(t, f.volumeRows(web.ID), vol.Name)
	require.Equal(t, before.Generation+1, after.Generation, "the generation advances once")
	require.Len(t, f.audits("action = 'service.source_changed'"), 1)

	// The file now agrees with the service.
	again := f.plan("staging", file)
	require.Equal(t, configsync.ActionUnchanged, applyPlanService(t, again, "web").Action, applyJSON(again.Services))
}

func TestApplySwitchesAGitServiceToAnImageAsAStagedChange(t *testing.T) {
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceGit, services.Config{
		GitURL: "https://github.com/acme/shop.git", GitBranch: "main", Port: 3000, BuildType: "dockerfile", DockerfilePath: "docker/Dockerfile",
	})
	f.setVar(web.ID, "KEEP", "1", false)

	file := applyWebFile(3000, "")
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	src := applyPlanService(t, pl, "web").Changes[0]
	require.Equal(t, services.FieldSource, src.Field)
	require.Equal(t, "git", src.From)
	require.Equal(t, "image", src.To)
	require.Equal(t, "services.web.image", src.Path)

	res := f.mustApply("staging", file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))

	after := f.row(web.ID)
	require.Equal(t, services.SourceImage, after.SourceType)
	cfg := services.DecodeConfig(after.Config)
	require.Equal(t, "nginx:1.27", cfg.Image)
	require.Empty(t, cfg.GitURL, "the repository is cleared")
	require.Empty(t, cfg.GitBranch)
	require.Equal(t, "auto", cfg.BuildType, "build settings are back to their defaults")
	require.Equal(t, "Dockerfile", cfg.DockerfilePath)
	require.Equal(t, 3000, cfg.Port)
	require.Equal(t, "1", f.rawVars(web.ID)["KEEP"].Value)
	require.Len(t, f.audits("action = 'service.source_changed'"), 1)
}

func TestApplyRefusesToTurnAStackIntoAnImage(t *testing.T) {
	f := newApplyFixture(t)
	f.mustApply("staging", applyKindsFile)
	stack := f.svcRow(f.staging, "suite")
	require.Equal(t, services.SourceCompose, stack.SourceType)
	before := f.state()

	file := "schema = 1\nserver = \"agent1\"\n\n[services.suite]\nimage = \"nginx:1.27\"\n"
	pl := f.plan("staging", file)
	require.False(t, pl.Valid)
	var codes []string
	for _, d := range pl.Diagnostics {
		codes = append(codes, d.Code)
	}
	require.Contains(t, codes, "source_type_change")
	require.Equal(t, configsync.ActionBlocked, applyPlanService(t, pl, "suite").Action)
	require.Equal(t, before, f.state(), "planning writes nothing")
	require.Equal(t, services.SourceCompose, f.row(stack.ID).SourceType)
}

func TestApplyGenerationAdvancesOncePerServiceWhateverIsChanged(t *testing.T) {
	// The generation advances only if something changed, and advances once.
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})
	f.setVar(web.ID, "KEEP", "1", false)
	before := f.row(web.ID)

	file := applyWebFile(8080, "\n[services.web.variables]\nKEEP = \"2\"\nNEW = \"x\"\n\n[services.web.volumes]\ndata = \"/data\"\n\n[services.web.domains.\"web.example.com\"]\n")
	res := f.mustApply("staging", file)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	for _, name := range []string{"config:web", "variables:web", "volumes:web", "domains:web"} {
		require.Equal(t, configsync.StepApplied, applyStepStatus(res, name), name)
	}
	after := f.row(web.ID)
	require.Equal(t, before.Generation+1, after.Generation, "one generation for one apply of one service")
	require.Equal(t, after.Generation, applyAppliedService(t, res, "web").Generation)
	require.Equal(t, after.Generation, f.source(web.ID).AppliedGeneration)
}

func TestApplyStalePlanIsRefusedWithoutAnyWrite(t *testing.T) {
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})
	file := applyWebFile(8080, "")
	pl := f.plan("staging", file)
	require.Equal(t, configsync.ActionUpdate, applyPlanService(t, pl, "web").Action)

	// An edit lands between plan and apply.
	f.setVar(web.ID, "EDIT", "1", false)
	before := f.state()
	_, err := f.apply(f.admin, "staging", file, applyWithPlanID(pl.PlanID))
	ae := applyRequireError(t, err, 409, configsync.CodePlanStale)
	fresh, ok := ae.Details["plan"].(configsync.Plan)
	require.True(t, ok, "details.plan carries the fresh plan")
	require.NotEqual(t, pl.PlanID, fresh.PlanID)
	require.Equal(t, f.plan("staging", file).PlanID, fresh.PlanID)
	require.Equal(t, before, f.state(), "zero writes")
	require.Equal(t, 3000, f.cfgOf(web.ID).Port)

	// An id that never matched is stale too; so is the id of another file.
	_, err = f.apply(f.admin, "staging", file, applyWithPlanID("pl_00000000000000000000000000000000"))
	applyRequireError(t, err, 409, configsync.CodePlanStale)
	other := f.plan("staging", applyWebFile(8081, ""))
	_, err = f.apply(f.admin, "staging", file, applyWithPlanID(other.PlanID))
	applyRequireError(t, err, 409, configsync.CodePlanStale)
	require.Equal(t, before, f.state())

	// The fresh plan applies.
	res := f.mustApply("staging", file, applyWithPlanID(fresh.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status)
	require.Equal(t, 8080, f.cfgOf(web.ID).Port)

	// Without a plan id the apply plans and applies atomically.
	res = f.mustApply("staging", applyWebFile(8090, ""))
	require.Equal(t, configsync.StatusStaged, res.Status)
	require.Equal(t, 8090, f.cfgOf(web.ID).Port)
}

func TestApplyRefusesAPlanWithBlockersAndWritesNothing(t *testing.T) {
	f := newApplyFixture(t)
	const file = `schema = 1
server = "agent1"

[services.good]
image = "nginx:1.27"
port = 8080

[services.bad]
image = "nginx:1.27"
server = "no-such-server"
`
	pl := f.plan("staging", file)
	require.False(t, pl.Valid)
	before := f.state()

	_, err := f.apply(f.admin, "staging", file)
	ae := applyRequireError(t, err, 422, configsync.CodeConfigBlocked)
	blocked, ok := ae.Details["plan"].(configsync.Plan)
	require.True(t, ok, "details.plan carries the plan")
	require.False(t, blocked.Valid)
	bad := applyPlanService(t, blocked, "bad")
	require.Equal(t, configsync.ActionBlocked, bad.Action)
	require.Contains(t, bad.Blockers, "unknown_server")
	require.True(t, blocked.Diagnostics.HasCode("unknown_server"))

	require.Equal(t, before, f.state(), "the valid service is not created either")
	require.False(t, f.hasSvc(f.staging, "good"))
	require.Equal(t, 0, f.count("SELECT count(*) FROM config_sources"))
}

func TestApplyPhaseOneIsAllOrNothing(t *testing.T) {
	f := newApplyFixture(t)
	// An agent without the proxy-v2 feature refuses a domain option at write time.
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux"})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)

	const file = `schema = 1
server = "agent1"

[services.a-first]
image = "nginx:1.27"
port = 8080

[services.a-first.variables]
K = "v"

[services.b-second]
image = "nginx:1.27"
port = 8081

[services.b-second.domains."b.example.com".options]
compress = true
`
	before := f.state()
	_, err = f.apply(f.admin, "staging", file)
	applyRequireError(t, err, 422, "agent_too_old")
	require.Equal(t, before, f.state(), "the first service, its variable and the ownership rows are rolled back")
	require.False(t, f.hasSvc(f.staging, "a-first"))
	require.Empty(t, f.audits("action = 'config.apply'"))
}

func TestApplyChecksEveryScopeBeforeWritingAnything(t *testing.T) {
	f := newApplyFixture(t)
	const file = `schema = 1
server = "agent1"

[services.app]
image = "nginx:1.27"
port = 8080

[services.app.scale]
replicas = 2

[services.app.uptime]
failure_threshold = 4

[services.a-edge.route]
protocol = "http"
upstreams = [{ address = "192.168.1.20:631" }]
`
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.ElementsMatch(t, []string{"projects:write", "deployments:write", "proxy:write", "admin"}, pl.RequiredScopes)
	before := f.state()

	_, err := f.apply(f.token(policy.ScopeProjectsWrite), "staging", file)
	ae := applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"admin", "deployments:write", "proxy:write"}, ae.Details["scopes"])
	require.Equal(t, before, f.state())

	_, err = f.apply(f.token(policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite), "staging", file)
	ae = applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"admin"}, ae.Details["scopes"], "uptime needs admin")
	require.Equal(t, before, f.state())

	// A deploy asks for deployments:write on top.
	_, err = f.apply(f.token(policy.ScopeProjectsWrite), "staging", applyWebFile(8080, ""), applyWithDeploy())
	ae = applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"deployments:write"}, ae.Details["scopes"])
	require.Equal(t, before, f.state())
	require.Equal(t, 0, f.deploy.callCount())

	// Admin implies every scope.
	res, err := f.apply(f.token(policy.ScopeAdmin), "staging", file)
	require.NoError(t, err)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, int32(2), f.svcRow(f.staging, "app").Replicas)
	require.Equal(t, services.KindRoute, f.svcRow(f.staging, "a-edge").Kind)
}

func TestApplyRefusesTheProjectLayerToPeopleWhoAreNotOwnersOrAdmins(t *testing.T) {
	f := newApplyFixture(t)
	const file = "schema = 1\n\n[variables]\nAPP_NAME = \"x\"\n"
	dev := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper, Label: "dev", Scopes: policy.ScopesForRole(auth.RoleDeveloper),
	})
	before := f.state()
	_, err := f.apply(dev, "staging", file)
	ae := applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"role:owner-or-admin"}, ae.Details["scopes"])
	require.Equal(t, before, f.state())
}

func TestApplyRefusesPruneAndDeployForAgents(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(8080, "")
	before := f.state()

	_, err := f.apply(f.mcp(), "staging", file, applyWithPrune())
	applyRequireError(t, err, 422, configsync.CodePruneNotAllowed)
	_, err = f.apply(f.mcp(), "staging", file, applyWithDeploy())
	applyRequireError(t, err, 422, configsync.CodeDeployNotAllowed)
	require.Equal(t, before, f.state())
	require.Equal(t, 0, f.deploy.callCount())

	// A staged-only apply by an agent works and is recorded as an agent's.
	res, err := f.apply(f.mcp(), "staging", file)
	require.NoError(t, err)
	require.Equal(t, configsync.StatusStaged, res.Status)
	web := f.svcRow(f.staging, "web")
	src := f.source(web.ID)
	require.Equal(t, configsync.OriginMCP, src.Origin)
	require.Equal(t, "mcp", src.AppliedByType)
	require.Equal(t, "claude", src.AppliedByLabel)
}

func TestApplyRecordsOriginAndActorOfEveryKindOfCaller(t *testing.T) {
	f := newApplyFixture(t)
	tokenID := uuid.New()
	tok := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &tokenID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "ci-token", Scopes: policy.AllScopes,
	})
	sys := auth.WithPrincipal(context.Background(), auth.System(f.ws.ID, f.project.ID, "nebula.toml @3f9c2a1",
		policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite))

	type caller struct {
		name       string
		ctx        context.Context
		opts       []applyOpt
		origin     string
		actorType  string
		actorID    string
		actorLabel string
	}
	userID := f.userID.String()
	callers := []caller{
		{"person", f.admin, nil, configsync.OriginUI, "user", userID, "owner@example.com"},
		{"token", tok, nil, configsync.OriginAPI, "token", tokenID.String(), "ci-token"},
		{"agent", f.mcp(), nil, configsync.OriginMCP, "mcp", userID, "claude"},
		{"cli", f.admin, []applyOpt{applyWithSource(configsync.SourceInfo{Origin: configsync.OriginCLI})}, configsync.OriginCLI, "user", userID, "owner@example.com"},
		{"git", sys, []applyOpt{applyWithSource(configsync.SourceInfo{Repo: "acme/shop", Branch: "main", Path: "nebula.toml", Commit: "3f9c2a1deadbeef00"})},
			configsync.OriginGit, "system", "", "nebula.toml @3f9c2a1"},
	}
	var web db.Service
	for i, c := range callers {
		file := applyWebFile(8000+i, "")
		_, err := f.apply(c.ctx, "staging", file, c.opts...)
		require.NoError(t, err, c.name)
		web = f.svcRow(f.staging, "web")
		src := f.source(web.ID)
		require.Equal(t, c.origin, src.Origin, c.name)
		require.Equal(t, c.actorType, src.AppliedByType, c.name)
		require.Equal(t, c.actorID, src.AppliedByID, c.name)
		require.Equal(t, c.actorLabel, src.AppliedByLabel, c.name)
		require.Equal(t, applyFileSHA(file), src.FileSha256, c.name)
		applies := f.audits("action = 'config.apply'")
		last := applies[len(applies)-1]
		require.Equal(t, c.actorType, last.ActorType, c.name)
		require.Equal(t, c.origin, last.Meta["origin"], c.name)
	}

	// The last one came from git: repository, branch, path and commit are recorded.
	src := f.source(web.ID)
	require.Equal(t, "acme/shop", src.RepoFullName)
	require.Equal(t, "main", src.Branch)
	require.Equal(t, "nebula.toml", src.Path)
	require.Equal(t, "3f9c2a1deadbeef00", src.CommitSha)
	got, err := f.cfg.ServiceSource(f.admin, web.ID)
	require.NoError(t, err)
	require.Equal(t, "nebula.toml @3f9c2a1", got.AppliedBy)
	require.Equal(t, configsync.OriginGit, got.Origin)
	last := f.audits("action = 'config.apply'")
	require.Equal(t, "acme/shop", last[len(last)-1].Meta["repo"])
	require.Equal(t, "3f9c2a1deadbeef00", last[len(last)-1].Meta["commit"])
}

func TestApplyInProductionByAPersonsTokenWorksAndNeedsNoApproval(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(8080, "\n[services.web.scale]\nreplicas = 2\n")

	pl := f.plan("production", file)
	require.True(t, pl.Environment.IsProduction)
	require.Equal(t, configsync.ApprovalRequired, pl.Guard.ProductionApproval, "immediate changes in production")

	res, err := f.apply(f.token(policy.AllScopes...), "production", file, applyWithPlanID(pl.PlanID))
	require.NoError(t, err)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	web := f.svcRow(f.prod, "web")
	require.Equal(t, int32(2), web.Replicas)
	src := f.source(web.ID)
	require.Equal(t, configsync.OriginAPI, src.Origin)
	require.Equal(t, "token", src.AppliedByType)
	require.Equal(t, f.prod.ID, src.EnvironmentID)
	require.False(t, f.hasSvc(f.staging, "web"), "the other environment is untouched")
}

func TestApplyReleaseKeepsTheLiveValueAndOnlyDropsOwnership(t *testing.T) {
	f := newApplyFixture(t)
	const v1 = `schema = 1
server = "agent1"

[services.web]
image = "nginx:1.27"
port = 8080

[services.web.variables]
KEEP = "k"
GONE = "g"

[services.web.volumes]
keep = "/keep"
gone = "/gone"

[services.web.domains."keep.example.com"]

[services.web.domains."gone.example.com"]

[services.web.tasks.keep-task]
schedule = "0 7 * * *"
command = ["echo", "keep"]

[services.web.tasks.gone-task]
schedule = "0 8 * * *"
command = ["echo", "gone"]
`
	const v2 = `schema = 1
server = "agent1"

[services.web]
image = "nginx:1.27"
port = 8080

[services.web.variables]
KEEP = "k"

[services.web.volumes]
keep = "/keep"

[services.web.domains."keep.example.com"]

[services.web.tasks.keep-task]
schedule = "0 7 * * *"
command = ["echo", "keep"]
`
	f.mustApply("staging", v1)
	web := f.svcRow(f.staging, "web")
	require.Contains(t, f.owned(web.ID), "variables.GONE")
	applies := len(f.audits("action = 'config.apply'"))

	pl := f.plan("staging", v2)
	ps := applyPlanService(t, pl, "web")
	require.Empty(t, ps.Changes, "a removed key is no change")
	require.ElementsMatch(t, []string{
		"services.web.variables.GONE", "services.web.volumes.gone", `services.web.domains."gone.example.com"`, "services.web.tasks.gone-task",
	}, ps.Released)

	f.mustApply("staging", v2, applyWithPlanID(pl.PlanID))

	owned := f.owned(web.ID)
	for _, id := range []string{"variables.GONE", "volumes.gone", "domains.gone.example.com", "tasks.gone-task"} {
		require.NotContains(t, owned, id, "released")
	}
	for _, id := range []string{"variables.KEEP", "volumes.keep", "domains.keep.example.com", "tasks.keep-task", "config.port"} {
		require.Contains(t, owned, id, "still owned")
	}
	// The live values stay.
	require.Equal(t, "g", f.rawVars(web.ID)["GONE"].Value)
	require.Equal(t, map[string]string{"keep": "/keep", "gone": "/gone"}, f.volumeRows(web.ID))
	require.Len(t, f.domainRows(web.ID), 2)
	require.Len(t, f.taskRows(web.ID), 2)
	require.Equal(t, applies+1, len(f.audits("action = 'config.apply'")), "a release is a recorded change of ownership")

	after := f.plan("staging", v2)
	as := applyPlanService(t, after, "web")
	require.Empty(t, as.Released)
	require.Equal(t, map[string]int{"variables": 1, "volumes": 1, "domains": 1, "tasks": 1}, as.Unmanaged, "what the file no longer mentions is counted, never listed")
}

func TestApplyPruneDeletesOnlyWhatTheFilePreviouslyOwned(t *testing.T) {
	f := newApplyFixture(t)
	f.network("net-a")
	f.network("net-b")
	const v1 = `schema = 1
server = "agent1"

[services.pg.database]
engine = "postgres"
version = "17"

[services.pg.backups.keep]
destination = "offsite"
cron = "0 2 * * *"

[services.pg.backups.drop]
destination = "offsite"
cron = "0 3 * * *"

[services.aux]
image = "nginx:1.27"

[services.web]
image = "nginx:1.27"
port = 8080
depends_on = ["pg", "aux"]
networks = ["net-a", "net-b"]

[services.web.variables]
KEEP = "k"
DROP = "d"

[services.web.volumes]
keep = "/keep"
drop = "/drop"

[services.web.domains."keep.example.com"]

[services.web.domains."drop.example.com"]

[services.web.uptime]
reminder_minutes = 15

[services.web.tasks.keep-task]
schedule = "0 7 * * *"
command = ["echo", "keep"]

[services.web.tasks.drop-task]
schedule = "0 8 * * *"
command = ["echo", "drop"]
`
	const v2 = `schema = 1
server = "agent1"

[services.pg.database]
engine = "postgres"
version = "17"

[services.pg.backups.keep]
destination = "offsite"
cron = "0 2 * * *"

[services.web]
image = "nginx:1.27"
port = 8080
depends_on = ["pg"]
networks = ["net-a"]

[services.web.variables]
KEEP = "k"

[services.web.volumes]
keep = "/keep"

[services.web.domains."keep.example.com"]

[services.web.tasks.keep-task]
schedule = "0 7 * * *"
command = ["echo", "keep"]
`
	res1 := f.mustApply("staging", v1)
	require.Equal(t, configsync.StatusStaged, res1.Status, applyJSON(res1.Steps))
	pg, web, aux := f.svcRow(f.staging, "pg"), f.svcRow(f.staging, "web"), f.svcRow(f.staging, "aux")
	require.Len(t, f.scheduleRows(pg.ID), 2)

	// What people added in the dashboard.
	f.setVar(web.ID, "UI_VAR", "ui", false)
	_, err := f.doms.Create(f.admin, web.ID, "ui.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.svcs.AddVolume(f.admin, web.ID, "ui-vol", "/ui")
	require.NoError(t, err)
	_, err = f.tsk.Service.CreateTask(f.admin, web.ID, tasks.TaskInput{Name: "ui-task", Schedule: "*/15 * * * *", Timezone: "UTC", Command: []string{"echo", "ui"}})
	require.NoError(t, err)
	uiSchedule, err := f.bkp.Service.CreateSchedule(f.admin, pg.ID, backups.ScheduleInput{DestinationID: f.dest.ID, Cron: "0 5 * * *"})
	require.NoError(t, err)
	commands := f.disp.count()

	pl := f.plan("staging", v2, applyWithPrune())
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, map[string][]string{
		"web": {"domains.drop.example.com", "links.aux", "networks.net-b", "tasks.drop-task", "uptime.reminderMinutes", "variables.DROP", "volumes.drop"},
		"pg":  {"backups.drop"},
	}, applyRemovedFields(pl))
	require.Equal(t, 8, pl.Summary.Pruned)
	require.Equal(t, configsync.ActionNotInFile, applyPlanService(t, pl, "aux").Action, "a service that left the file is listed, never pruned")
	require.True(t, applyPlanService(t, pl, "aux").Managed)

	res := f.mustApply("staging", v2, applyWithPrune(), applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "tasks:web:drop-task"))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "backups:pg:drop"))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "uptime:web:reset"))

	// Previously owned members are gone.
	require.NotContains(t, f.rawVars(web.ID), "DROP")
	require.NotContains(t, f.domainRows(web.ID), "drop.example.com")
	require.Equal(t, map[string]string{"keep": "/keep", "ui-vol": "/ui"}, f.volumeRows(web.ID), "the volume is detached; the volumes the file did not own stay")
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_networks WHERE service_id = $1", web.ID))
	links := f.linkPairs(f.staging)
	require.True(t, links[web.ID.String()+">"+pg.ID.String()])
	require.False(t, links[web.ID.String()+">"+aux.ID.String()])
	require.NotContains(t, f.taskRows(web.ID), "drop-task")
	require.EqualValues(t, 0, f.monitor(web.ID).ReminderMinutes, "the monitor is reset to its defaults")
	keys := map[string]bool{}
	for _, s := range f.scheduleRows(pg.ID) {
		if s.ConfigKey != nil {
			keys[*s.ConfigKey] = true
		}
	}
	require.Equal(t, map[string]bool{"keep": true}, keys)

	// What the file never owned is untouched.
	require.Equal(t, "ui", f.rawVars(web.ID)["UI_VAR"].Value)
	require.Contains(t, f.rawVars(web.ID), "KEEP")
	require.Contains(t, f.domainRows(web.ID), "ui.example.com")
	require.Contains(t, f.domainRows(web.ID), "keep.example.com")
	require.Contains(t, f.taskRows(web.ID), "ui-task")
	require.Contains(t, f.taskRows(web.ID), "keep-task")
	found := false
	for _, s := range f.scheduleRows(pg.ID) {
		if s.ID == uiSchedule.ID {
			found = true
			require.Nil(t, s.ConfigKey)
		}
	}
	require.True(t, found, "the schedule made in the dashboard stays")

	// Never services, databases or data.
	for _, slug := range []string{"pg", "web", "aux"} {
		require.True(t, f.hasSvc(f.staging, slug), slug)
	}
	require.Equal(t, commands, f.disp.count(), "no command reaches an agent: detaching a volume deletes no data")
	owned := f.owned(web.ID)
	for _, id := range []string{"variables.DROP", "volumes.drop", "domains.drop.example.com", "networks.net-b", "links.aux", "tasks.drop-task", "uptime.reminderMinutes"} {
		require.NotContains(t, owned, id)
	}
	require.Contains(t, owned, "variables.KEEP")

	// Without --prune the same removal would only have released.
	after := f.plan("staging", v2, applyWithPrune())
	require.Empty(t, applyRemovedFields(after))
}

func TestApplySharedVariablesAreOwnedReleasedAndPruned(t *testing.T) {
	f := newApplyFixture(t)
	v1 := applyWebFile(8080, `
[services.web.variables]
SITE = "${{shared.APP_NAME}}"

[variables]
APP_NAME = "Acme"

[environments.staging.variables]
APP_ENV = "staging"
`)
	res := f.mustApply("staging", v1)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "shared:project"))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "shared:environment"))
	require.Equal(t, map[string]string{
		"project.APP_NAME":    applyVarFP("Acme", false),
		"environment.APP_ENV": applyVarFP("staging", false),
	}, f.ownedShared(f.staging))

	// A shared variable made in the dashboard is not the file's.
	ui := "ui"
	_, err := f.vars.PutShared(f.admin, variables.LayerProject, f.project.ID, []variables.SharedVariableInput{{Key: "UI_SHARED", Value: &ui}})
	require.NoError(t, err)

	// Again: nothing to do.
	again := f.mustApply("staging", v1)
	require.Equal(t, configsync.StatusNoop, again.Status, applyJSON(again))

	// The environment variable leaves the file: released, then pruned.
	v2 := applyWebFile(8080, `
[services.web.variables]
SITE = "${{shared.APP_NAME}}"

[variables]
APP_NAME = "Acme"
`)
	f.mustApply("staging", v2)
	require.Equal(t, map[string]string{"project.APP_NAME": applyVarFP("Acme", false)}, f.ownedShared(f.staging))
	envVars, err := f.q.ListSharedVariablesEnv(context.Background(), &f.staging.ID)
	require.NoError(t, err)
	require.Len(t, envVars, 1, "released, not deleted")

	f.mustApply("staging", v1)
	f.mustApply("staging", v2, applyWithPrune())
	envVars, err = f.q.ListSharedVariablesEnv(context.Background(), &f.staging.ID)
	require.NoError(t, err)
	require.Empty(t, envVars, "pruned: the file owned it")
	proj, err := f.q.ListSharedVariablesProject(context.Background(), f.project.ID)
	require.NoError(t, err)
	keys := []string{}
	for _, v := range proj {
		keys = append(keys, v.Key)
	}
	require.Equal(t, []string{"APP_NAME", "UI_SHARED"}, keys, "the dashboard's shared variable stays")
}

func TestApplyPartialWhenAnImmediateStepFailsThenConverges(t *testing.T) {
	f := newApplyFixture(t)
	f.scaler.fail.set(errors.New("scale boom"))
	file := applyWebFile(8080, "\n[services.web.variables]\nGREETING = \"hi\"\n\n[services.web.scale]\nreplicas = 2\n")

	res := f.mustApply("staging", file)
	require.Equal(t, configsync.StatusPartial, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepFailed, applyStepStatus(res, "scale:web"))
	require.Contains(t, applyStepMessage(res, "scale:web"), "scale boom")
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "variables:web"), "the staged parts are committed")

	web := f.svcRow(f.staging, "web")
	require.Equal(t, int32(1), web.Replicas)
	require.Equal(t, "hi", f.rawVars(web.ID)["GREETING"].Value)
	owned := f.owned(web.ID)
	require.Contains(t, owned, "config.port")
	require.Contains(t, owned, "variables.GREETING")
	require.NotContains(t, owned, "scale.replicas", "a failed step records no ownership")

	// The next plan still shows it.
	pl := f.plan("staging", file)
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, []string{"scale.replicas"}, applyChangeFields(ps))
	require.Equal(t, configsync.AppliesImmediate, ps.Changes[0].Applies)

	// Healthy again: re-applying converges.
	f.scaler.fail.set(nil)
	res = f.mustApply("staging", file, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "scale:web"))
	require.Equal(t, int32(2), f.row(web.ID).Replicas)
	require.Equal(t, applyFP(2), f.owned(web.ID)["scale.replicas"])
	res = f.mustApply("staging", file)
	require.Equal(t, configsync.StatusNoop, res.Status, applyJSON(res.Steps))
}

func TestApplyPartialWhenTheTaskStoreFailsThenConverges(t *testing.T) {
	f := newApplyFixture(t)
	f.tsk.fail.set(errors.New("tasks down"))
	file := applyWebFile(8080, "\n[services.web.tasks.digest]\nschedule = \"0 7 * * *\"\ncommand = [\"echo\", \"digest\"]\n")

	res := f.mustApply("staging", file)
	require.Equal(t, configsync.StatusPartial, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepFailed, applyStepStatus(res, "tasks:web:digest"))
	web := f.svcRow(f.staging, "web")
	require.Empty(t, f.taskRows(web.ID))
	require.NotContains(t, f.owned(web.ID), "tasks.digest")
	require.Contains(t, f.owned(web.ID), "config.port")

	f.tsk.fail.set(nil)
	res = f.mustApply("staging", file)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Contains(t, f.taskRows(web.ID), "digest")
	require.Contains(t, f.owned(web.ID), "tasks.digest")
	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", file).Status)
}

const applyDeferFile = `schema = 1
server = "agent1"

[services.pg.database]
engine = "postgres"
version = "17"

[services.pg.backups.nightly]
destination = "offsite"
cron = "0 2 * * *"
retention_count = 14
`

func applyNotDeployed() error {
	return apierr.Unprocessable("service_not_deployed", "the database has not been deployed yet")
}

func TestApplyDefersWhatNeedsADeployedServiceAndRunPendingCompletesIt(t *testing.T) {
	f := newApplyFixture(t)
	f.bkp.fail.set(applyNotDeployed())

	res := f.mustApply("staging", applyDeferFile)
	require.Equal(t, configsync.StepDeferred, applyStepStatus(res, "backups:pg:nightly"), applyJSON(res.Steps))
	require.Equal(t, configsync.StatusStaged, res.Status, "the service was created")
	var deferred *configsync.Diagnostic
	for i := range res.Plan.Diagnostics {
		if res.Plan.Diagnostics[i].Code == "deferred" {
			deferred = &res.Plan.Diagnostics[i]
		}
	}
	require.NotNil(t, deferred, applyJSON(res.Plan.Diagnostics))
	require.Equal(t, projectconfig.SeverityInfo, deferred.Severity)

	pg := f.svcRow(f.staging, "pg")
	require.Empty(t, f.scheduleRows(pg.ID))
	require.NotContains(t, f.owned(pg.ID), "backups.nightly")

	// The pending record holds the desired value and nothing else.
	pending := f.pendingOf(pg.ID)
	require.Len(t, pending, 1)
	var asMap map[string]any
	require.NoError(t, json.Unmarshal(pending["backups.nightly"], &asMap))
	keys := []string{}
	for k := range asMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	require.Equal(t, []string{"cron", "destination", "enabled", "key", "retentionCount", "retentionDays", "timezone"}, keys)
	require.Equal(t, "nightly", asMap["key"])
	require.Equal(t, "0 2 * * *", asMap["cron"])
	require.EqualValues(t, 14, asMap["retentionCount"])
	src, err := f.cfg.ServiceSource(f.admin, pg.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"backups.nightly"}, src.Pending)

	// Running a later apply defers again without duplicating the record.
	res = f.mustApply("staging", applyDeferFile)
	require.Equal(t, configsync.StepDeferred, applyStepStatus(res, "backups:pg:nightly"))
	require.Len(t, f.pendingOf(pg.ID), 1)

	// The service is deployed: the replay creates the schedule under its key.
	f.bkp.fail.set(nil)
	require.NoError(t, f.cfg.RunPending(f.admin, pg.ID))
	rows := f.scheduleRows(pg.ID)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].ConfigKey)
	require.Equal(t, "nightly", *rows[0].ConfigKey)
	require.Equal(t, "0 2 * * *", rows[0].Cron)
	require.EqualValues(t, 14, rows[0].RetentionCount)
	require.Equal(t, f.dest.ID, rows[0].DestinationID)
	require.Empty(t, f.pendingOf(pg.ID), "the record is cleared")
	require.Contains(t, f.owned(pg.ID), "backups.nightly", "and the field is owned")
	src, err = f.cfg.ServiceSource(f.admin, pg.ID)
	require.NoError(t, err)
	require.Empty(t, src.Pending)

	pl := f.plan("staging", applyDeferFile)
	require.Empty(t, applyPlanService(t, pl, "pg").Changes, "nothing is left to do")

	// Replaying again is harmless.
	require.NoError(t, f.cfg.RunPending(f.admin, pg.ID))
	require.Len(t, f.scheduleRows(pg.ID), 1)
}

func TestApplyAfterADeferralConvergesWhenTheStoreIsHealthy(t *testing.T) {
	f := newApplyFixture(t)
	f.bkp.fail.set(applyNotDeployed())
	f.mustApply("staging", applyDeferFile)
	pg := f.svcRow(f.staging, "pg")
	require.Len(t, f.pendingOf(pg.ID), 1)

	f.bkp.fail.set(nil)
	res := f.mustApply("staging", applyDeferFile)
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "backups:pg:nightly"), applyJSON(res.Steps))
	require.Equal(t, configsync.StatusStaged, res.Status)
	require.Len(t, f.scheduleRows(pg.ID), 1)
	require.Empty(t, f.pendingOf(pg.ID))
	require.Contains(t, f.owned(pg.ID), "backups.nightly")
	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", applyDeferFile).Status)
}

func TestRunPendingLoopReplaysWhenADeploymentSucceeds(t *testing.T) {
	f := newApplyFixture(t)
	f.bkp.fail.set(applyNotDeployed())
	f.mustApply("staging", applyDeferFile)
	pg := f.svcRow(f.staging, "pg")
	f.bkp.fail.set(nil)

	bg := context.Background()
	failed, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: pg.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	// deployments_one_active: a service has one active deployment at a time, so the first ends before the second starts.
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'failed' WHERE id = $1", failed.ID)
	require.NoError(t, err)
	ok, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: pg.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", ok.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.cfg.RunPendingLoop(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("RunPendingLoop did not return after its context ended")
		}
	}()

	// A deployment that did not succeed replays nothing (the loop may not have subscribed yet: keep publishing).
	for range 5 {
		f.bus.Publish(events.Event{Type: services.EventDeploymentUpdated, ResourceID: failed.ID.String()})
		time.Sleep(20 * time.Millisecond)
	}
	require.Len(t, f.pendingOf(pg.ID), 1, "a failed deployment replays nothing")
	require.Empty(t, f.scheduleRows(pg.ID))

	require.Eventually(t, func() bool {
		f.bus.Publish(events.Event{Type: services.EventDeploymentUpdated, ResourceID: ok.ID.String()})
		return applyQuietCount(f.pool, "SELECT count(*) FROM backup_schedules WHERE service_id = $1", pg.ID) == 1
	}, 10*time.Second, 50*time.Millisecond, "a succeeded deployment replays the pending step")
	require.Eventually(t, func() bool {
		return applyQuietCount(f.pool, "SELECT count(*) FROM config_sources WHERE service_id = $1 AND pending <> '{}'::jsonb", pg.ID) == 0
	}, 5*time.Second, 20*time.Millisecond)
	rows := f.scheduleRows(pg.ID)
	require.Len(t, rows, 1)
	require.Equal(t, "nightly", *rows[0].ConfigKey)
	require.Contains(t, f.owned(pg.ID), "backups.nightly")
}

func TestApplyDeployDeploysExactlyTheTouchedServicesWithStagedChanges(t *testing.T) {
	f := newApplyFixture(t)
	const two = `schema = 1
server = "agent1"

[services.web]
image = "nginx:1.27"
port = %d

[services.other]
image = "nginx:1.27"
port = 8081
`
	res := f.mustApply("staging", fmt.Sprintf(two, 8080), applyWithDeploy())
	web, other := f.svcRow(f.staging, "web"), f.svcRow(f.staging, "other")
	require.Equal(t, 1, f.deploy.callCount())
	call := f.deploy.lastCall()
	require.Equal(t, f.staging.ID, call.Env)
	require.ElementsMatch(t, []uuid.UUID{web.ID, other.ID}, call.IDs)
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "deploy"))
	require.Len(t, res.Deployments, 2)

	// Only web changes now: other has staged changes of its own but this apply did not touch it.
	res = f.mustApply("staging", fmt.Sprintf(two, 8082), applyWithDeploy())
	require.Equal(t, 2, f.deploy.callCount())
	call = f.deploy.lastCall()
	require.Equal(t, []uuid.UUID{web.ID}, call.IDs)
	require.Len(t, res.Deployments, 1)

	// Nothing changed: nothing is deployed.
	res = f.mustApply("staging", fmt.Sprintf(two, 8082), applyWithDeploy())
	require.Equal(t, configsync.StatusNoop, res.Status)
	require.Equal(t, configsync.StepUnchanged, applyStepStatus(res, "deploy"))
	require.Equal(t, 2, f.deploy.callCount())
	require.Empty(t, res.Deployments)

	// Without the switch an apply never deploys.
	f.mustApply("staging", fmt.Sprintf(two, 8083))
	require.Equal(t, 2, f.deploy.callCount())
}

func TestApplyConcurrentAppliesOfOneEnvironmentSerialize(t *testing.T) {
	f := newApplyFixture(t)
	files := []string{applyWebFile(8001, ""), applyWebFile(8002, "")}
	results := make([]configsync.ApplyResult, len(files))
	errs := make([]error, len(files))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range files {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.apply(f.admin, "staging", files[i])
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "apply %d", i)
		require.Equal(t, configsync.StatusStaged, results[i].Status, applyJSON(results[i].Steps))
	}

	// One of the two orders, never a mixture: the second apply saw the service the first one made.
	actions := []string{applyAppliedService(t, results[0], "web").Action, applyAppliedService(t, results[1], "web").Action}
	sort.Strings(actions)
	require.Equal(t, []string{configsync.ActionCreate, configsync.ActionUpdate}, actions)
	require.Equal(t, 1, f.count("SELECT count(*) FROM services"))
	web := f.svcRow(f.staging, "web")
	port := f.cfgOf(web.ID).Port
	require.Contains(t, []int{8001, 8002}, port)
	require.Equal(t, applyFP(port), f.owned(web.ID)["config.port"], "the ownership matches the final value")
	require.EqualValues(t, 2, web.Generation, "one creation and one update")
	require.Equal(t, 1, f.count("SELECT count(*) FROM config_sources"))
	require.Len(t, f.audits("action = 'config.apply'"), 2)
}

func TestApplyAdoptsAnExistingServiceOwningOnlyTheKeysPresent(t *testing.T) {
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})
	f.setVar(web.ID, "NODE_ENV", "development", false)
	f.setVar(web.ID, "UNTOUCHED", "1", false)
	_, err := f.doms.Create(f.admin, web.ID, "old.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)

	file := applyWebFile(3000, `
[services.web.variables]
NODE_ENV = "production"
NEW = "x"

[services.web.domains."old.example.com"]
`)
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, 1, pl.Summary.Adopted)
	ps := applyPlanService(t, pl, "web")
	require.Equal(t, configsync.ActionUpdate, ps.Action)
	require.False(t, ps.Managed)
	require.Equal(t, []string{"variables.NEW", "variables.NODE_ENV"}, applyChangeFields(ps), "the plan shows only what differs")
	for _, c := range ps.Changes {
		switch c.Field {
		case "variables.NEW":
			require.Equal(t, configsync.ReasonNew, c.Reason)
			require.Equal(t, configsync.OpAdd, c.Op)
		case "variables.NODE_ENV":
			require.Equal(t, configsync.ReasonAdopt, c.Reason)
			require.Equal(t, configsync.OpSet, c.Op)
			require.Equal(t, "development", c.From)
			require.Equal(t, "production", c.To)
		}
	}
	require.Equal(t, map[string]int{"variables": 1}, ps.Unmanaged)

	f.mustApply("staging", file, applyWithPlanID(pl.PlanID))
	require.Equal(t, []string{
		"config.image", "config.port", "domains.old.example.com", "server", "variables.NEW", "variables.NODE_ENV",
	}, applyOwnedKeys(f.owned(web.ID)), "owned = the keys present (the file's root server included), in sync or not")
	raw := f.rawVars(web.ID)
	require.Equal(t, "production", raw["NODE_ENV"].Value)
	require.Equal(t, "1", raw["UNTOUCHED"].Value)
	require.Equal(t, "x", raw["NEW"].Value)

	after := f.plan("staging", file)
	as := applyPlanService(t, after, "web")
	require.True(t, as.Managed)
	require.Equal(t, configsync.StateInSync, as.State)
	require.Empty(t, as.Changes)
}

func TestServiceSourceReportsDriftFileChangedAndConflict(t *testing.T) {
	f := newApplyFixture(t)

	// A service no file manages.
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})
	src, err := f.cfg.ServiceSource(f.admin, web.ID)
	require.NoError(t, err)
	require.False(t, src.Managed)
	require.NotNil(t, src.Fields)
	require.Empty(t, src.Fields)
	require.NotNil(t, src.Pending)
	require.Empty(t, src.Pending)
	require.Empty(t, src.State)

	file := applyWebFile(8080, "\n[services.web.variables]\nNODE_ENV = \"production\"\n")
	f.mustApply("staging", file, applyWithSource(configsync.SourceInfo{Repo: "acme/shop", Branch: "main", Path: "nebula.toml", Commit: "abcdef1234567"}))
	states := func() (configsync.ServiceConfigSource, map[string]string) {
		s, err := f.cfg.ServiceSource(f.admin, web.ID)
		require.NoError(t, err)
		m := map[string]string{}
		for _, fs := range s.Fields {
			m[fs.Field] = fs.State
		}
		return s, m
	}

	// In sync after an apply.
	src, m := states()
	require.True(t, src.Managed)
	require.Equal(t, configsync.StateInSync, src.State)
	require.Equal(t, configsync.OriginUI, src.Origin)
	require.Equal(t, "acme/shop", src.Repo)
	require.Equal(t, "abcdef1234567", src.Commit)
	require.Equal(t, applyFileSHA(file), src.FileSHA256)
	require.NotNil(t, src.AppliedAt)
	require.Equal(t, map[string]string{
		"config.image": configsync.StateInSync, "config.port": configsync.StateInSync, "server": configsync.StateInSync, "variables.NODE_ENV": configsync.StateInSync,
	}, m)

	// Edited in the dashboard since the last apply: drift.
	f.setVar(web.ID, "NODE_ENV", "development", false)
	src, m = states()
	require.Equal(t, configsync.StateDrift, src.State)
	require.Equal(t, configsync.StateDrift, m["variables.NODE_ENV"])
	require.Equal(t, configsync.StateInSync, m["config.port"])

	// Put it back; a newer file is waiting (the last plan of the environment): file_changed.
	f.setVar(web.ID, "NODE_ENV", "production", false)
	newer := f.plan("staging", applyWebFile(9090, "\n[services.web.variables]\nNODE_ENV = \"production\"\n"))
	f.storeLastPlan(f.staging, newer)
	src, m = states()
	require.Equal(t, configsync.StateFileChanged, src.State)
	require.Equal(t, configsync.StateFileChanged, m["config.port"])
	require.Equal(t, configsync.StateInSync, m["variables.NODE_ENV"])

	// Both moved: conflict, which is worse than a drifted variable.
	f.setPort(web.ID, 7000)
	f.setVar(web.ID, "NODE_ENV", "staging", false)
	src, m = states()
	require.Equal(t, configsync.StateConflict, m["config.port"])
	require.Equal(t, configsync.StateDrift, m["variables.NODE_ENV"])
	require.Equal(t, configsync.StateConflict, src.State)
}

func TestReleaseFieldsStopsOwningOnlyTheListedFields(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(8080, "\n[services.web.variables]\nA = \"1\"\nB = \"2\"\n")
	f.mustApply("staging", file)
	web := f.svcRow(f.staging, "web")
	before := f.owned(web.ID)
	require.Contains(t, before, "variables.A")
	mark := f.auditMark()

	// Unknown ids refuse the whole release.
	for _, ids := range [][]string{{"nope"}, {"variables.A", "nope"}, {"variables.A#keys"}} {
		_, err := f.cfg.ReleaseFields(f.admin, web.ID, ids)
		ae := applyRequireError(t, err, 422, configsync.CodeFieldNotOwned)
		require.NotEmpty(t, ae.Details["fields"])
		require.Equal(t, before, f.owned(web.ID), "nothing was released")
	}
	require.Empty(t, f.audits("action = 'config.release'"))

	// A read-only token cannot release.
	_, err := f.cfg.ReleaseFields(f.token(policy.ScopeProjectsRead), web.ID, []string{"variables.A"})
	applyRequireError(t, err, 403, "forbidden")

	got, err := f.cfg.ReleaseFields(f.admin, web.ID, []string{"variables.A"})
	require.NoError(t, err)
	require.True(t, got.Managed)
	for _, fs := range got.Fields {
		require.NotEqual(t, "variables.A", fs.Field)
	}
	owned := f.owned(web.ID)
	require.NotContains(t, owned, "variables.A")
	delete(before, "variables.A")
	require.Equal(t, before, owned, "only the listed field was released")
	require.Equal(t, "1", f.rawVars(web.ID)["A"].Value, "the live value stays")

	audits := f.audits("action = 'config.release' AND id > $1", mark)
	require.Len(t, audits, 1)
	require.Equal(t, "service", audits[0].Resource)
	require.Equal(t, web.ID.String(), audits[0].ResourceID)
	require.Equal(t, []any{"variables.A"}, audits[0].Meta["fields"])
	require.Equal(t, "nebula.toml", audits[0].Meta["via"])

	// Releasing it again is an unknown id now; a service no file manages owns nothing.
	_, err = f.cfg.ReleaseFields(f.admin, web.ID, []string{"variables.A"})
	applyRequireError(t, err, 422, configsync.CodeFieldNotOwned)
	lone := f.uiService(f.staging, "lone", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 80})
	_, err = f.cfg.ReleaseFields(f.admin, lone.ID, []string{"config.port"})
	applyRequireError(t, err, 422, configsync.CodeFieldNotOwned)

	// A released field can be owned again: the dashboard's value is then a difference the plan shows.
	f.setVar(web.ID, "A", "changed", false)
	ps := applyPlanService(t, f.plan("staging", file), "web")
	require.Equal(t, []string{"variables.A"}, applyChangeFields(ps), "the file owns the key again, so the dashboard's value is a difference")
	require.Equal(t, configsync.ReasonAdopt, ps.Changes[0].Reason)
}

func TestApplyBackupSchedulesAreIdentifiedByTheirKey(t *testing.T) {
	f := newApplyFixture(t)
	f.mustApply("staging", applyDeferFile)
	pg := f.svcRow(f.staging, "pg")
	rows := f.scheduleRows(pg.ID)
	require.Len(t, rows, 1)
	require.Equal(t, "nightly", *rows[0].ConfigKey)
	nightly := rows[0]

	ui, err := f.bkp.Service.CreateSchedule(f.admin, pg.ID, backups.ScheduleInput{DestinationID: f.dest.ID, Cron: "0 5 * * *"})
	require.NoError(t, err)

	v2 := strings.Replace(strings.Replace(applyDeferFile, `cron = "0 2 * * *"`, `cron = "0 3 * * *"`, 1), "retention_count = 14", "retention_count = 21", 1)
	pl := f.plan("staging", v2)
	ps := applyPlanService(t, pl, "pg")
	require.Equal(t, []string{"backups.nightly"}, applyChangeFields(ps))
	// Unmanaged counts every live member the file does not mention, a database's own variables and data volume included.
	require.Equal(t, 1, ps.Unmanaged["backups"], "the schedule made in the dashboard is counted, never touched")
	f.mustApply("staging", v2, applyWithPlanID(pl.PlanID))

	rows = f.scheduleRows(pg.ID)
	require.Len(t, rows, 2)
	for _, r := range rows {
		switch r.ID {
		case nightly.ID:
			require.Equal(t, "nightly", *r.ConfigKey, "an update keeps the key")
			require.Equal(t, "0 3 * * *", r.Cron)
			require.EqualValues(t, 21, r.RetentionCount)
		case ui.ID:
			require.Nil(t, r.ConfigKey)
			require.Equal(t, "0 5 * * *", r.Cron)
		default:
			t.Fatalf("unexpected schedule %s", r.ID)
		}
	}
}

func TestApplyUptimeOverlaysTheOwnedKeysOnTheLiveMonitor(t *testing.T) {
	f := newApplyFixture(t)
	web := f.uiService(f.staging, "web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3000})
	_, err := f.upt.UpdateMonitor(f.admin, web.ID, uptime.MonitorView{Enabled: true, Alerts: false, FailureThreshold: 5, RecoveryThreshold: 4, ReminderMinutes: 10})
	require.NoError(t, err)

	f.mustApply("staging", applyWebFile(3000, "\n[services.web.uptime]\nreminder_minutes = 60\n"))
	m := f.monitor(web.ID)
	require.True(t, m.Enabled)
	require.False(t, m.Alerts, "alerts is not owned: the dashboard's value stays")
	require.EqualValues(t, 5, m.FailureThreshold)
	require.EqualValues(t, 4, m.RecoveryThreshold)
	require.EqualValues(t, 60, m.ReminderMinutes)
	owned := f.owned(web.ID)
	require.Equal(t, applyFP(60), owned["uptime.reminderMinutes"])
	require.NotContains(t, owned, "uptime.alerts")
	require.NotContains(t, owned, "uptime.failureThreshold")

	// A service with no monitor row gets the defaults for what the file leaves out.
	other := f.uiService(f.staging, "other", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1.27", Port: 3001})
	f.mustApply("staging", applyWebFile(3000, "\n[services.web.uptime]\nreminder_minutes = 60\n\n[services.other]\nimage = \"nginx:1.27\"\nport = 3001\n\n[services.other.uptime]\nfailure_threshold = 5\n"))
	o := f.monitor(other.ID)
	require.True(t, o.Enabled)
	require.True(t, o.Alerts)
	require.EqualValues(t, 5, o.FailureThreshold)
	require.EqualValues(t, 2, o.RecoveryThreshold)
	require.EqualValues(t, 0, o.ReminderMinutes)
}

func TestApplyGenerateDomainAttachesOneGeneratedHostname(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(8080, "generate_domain = true\n")
	res := f.mustApply("staging", file)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "domains:web"))
	web := f.svcRow(f.staging, "web")
	doms := f.domainRows(web.ID)
	require.Len(t, doms, 1)
	for host, d := range doms {
		require.True(t, d.Generated, host)
		require.True(t, strings.HasSuffix(host, ".example.com"), host)
	}
	require.Equal(t, applyFP(true), f.owned(web.ID)["generate_domain"])
	again := f.mustApply("staging", file)
	require.Equal(t, configsync.StatusNoop, again.Status, applyJSON(again.Steps))
	require.Len(t, f.domainRows(web.ID), 1)
}

func TestApplyNeverLetsAPlantedSecretReachResultsAuditOrLogs(t *testing.T) {
	f := newApplyFixture(t)
	const canary = "canary-7f3a9c1e-planted-secret"
	const canary2 = "canary-91bd24ce-unmanaged-secret"
	file := func(port int) string {
		return applyWebFile(port, `
[services.web.variables]
GREETING = "hello"
SESSION_SECRET = "${{secret(32)}}"

[services.web.secrets]
keys = ["STRIPE_SECRET_KEY"]
`)
	}

	var outputs []string
	pl := f.plan("staging", file(8080))
	outputs = append(outputs, applyJSON(pl))
	res := f.mustApply("staging", file(8080), applyWithPlanID(pl.PlanID))
	outputs = append(outputs, applyJSON(res))
	web := f.svcRow(f.staging, "web")

	// The values arrive the way they do in real life: through the dashboard.
	f.setVar(web.ID, "STRIPE_SECRET_KEY", canary, true)
	f.setVar(web.ID, "LEGACY_TOKEN", canary2, true)
	generated := f.rawVars(web.ID)["SESSION_SECRET"].Value
	require.Len(t, generated, 32)
	require.Equal(t, canary, f.rawVars(web.ID)["STRIPE_SECRET_KEY"].Value)

	// Plan, apply and read the state again with the secrets in place.
	pl2 := f.plan("staging", file(9090))
	outputs = append(outputs, applyJSON(pl2))
	res2 := f.mustApply("staging", file(9090), applyWithPlanID(pl2.PlanID))
	outputs = append(outputs, applyJSON(res2))
	src, err := f.cfg.ServiceSource(f.admin, web.ID)
	require.NoError(t, err)
	outputs = append(outputs, applyJSON(src))
	rel, err := f.cfg.ReleaseFields(f.admin, web.ID, []string{"variables.GREETING"})
	require.NoError(t, err)
	outputs = append(outputs, applyJSON(rel))
	ex, err := f.cfg.Export(f.admin, f.project.ID, "staging", "", false, false)
	require.NoError(t, err)
	outputs = append(outputs, applyJSON(ex), ex.File)

	secrets := []string{canary, canary2, generated}
	for i, out := range outputs {
		applyRequireNoText(t, fmt.Sprintf("output %d", i), out, secrets...)
	}
	applyRequireNoText(t, "audit metadata", f.auditText(), secrets...)
	applyRequireNoText(t, "logs", f.logs.String(), secrets...)

	// What a plan may say about them: that they exist, masked.
	for _, c := range applyPlanService(t, pl, "web").Changes {
		if c.Field == "variables.SESSION_SECRET" {
			require.True(t, c.Masked)
			require.Equal(t, "generated(secret(32))", c.To)
		}
	}
}

func TestApplyAfterServiceDeleteStartsFromScratch(t *testing.T) {
	f := newApplyFixture(t)
	file := applyWebFile(8080, "\n[services.web.variables]\nK = \"v\"\n")
	f.mustApply("staging", file)
	old := f.svcRow(f.staging, "web")
	require.True(t, f.hasSource(old.ID))

	require.NoError(t, f.svcs.Delete(f.admin, old.ID, services.DeleteInput{Confirm: "web"}))
	require.False(t, f.hasSource(old.ID), "deleting the service drops its ownership row")

	pl := f.plan("staging", file)
	require.Equal(t, configsync.ActionCreate, applyPlanService(t, pl, "web").Action)
	res := f.mustApply("staging", file)
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "service.create:web"))
	fresh := f.svcRow(f.staging, "web")
	require.NotEqual(t, old.ID, fresh.ID)
	require.True(t, f.hasSource(fresh.ID))
	require.Equal(t, applyFP(8080), f.owned(fresh.ID)["config.port"])
}

// ---- processes: one service, several processes ----

const applyProcessesFile = `schema = 1
server = "agent1"

[services.jobs]
image = "nginx:1.27"
port = 8080
process_name = "app"

[services.jobs.domains."app.example.com"]

[services.jobs.domains."metrics.example.com"]
process = "metrics"

[services.jobs.processes.worker]
command = ["bundle", "exec", "sidekiq"]
stop_grace_seconds = 120

[services.jobs.processes.worker.scale]
replicas = 2

[services.jobs.processes.clock]
command = ["bundle", "exec", "clockwork", "clock.rb"]
singleton = true

[services.jobs.processes.metrics]
command = ["bin/metrics"]
port = 9100
`

func (f *applyFixture) processScales(id uuid.UUID) map[string]db.ServiceProcessScale {
	f.t.Helper()
	rows, err := f.q.ListProcessScales(context.Background(), id)
	require.NoError(f.t, err)
	out := map[string]db.ServiceProcessScale{}
	for _, r := range rows {
		out[r.Process] = r
	}
	return out
}

func applyProcessNames(c services.Config) []string {
	names := make([]string, 0, len(c.Processes))
	for _, p := range c.Processes {
		names = append(names, p.Name)
	}
	return names
}

func TestApplyCreatesAServiceWithProcessesDomainsAndScale(t *testing.T) {
	f := newApplyFixture(t)
	pl := f.plan("staging", applyProcessesFile)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, 1, pl.Summary.Create)

	res := f.mustApply("staging", applyProcessesFile, applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	for _, s := range res.Steps {
		require.NotEqual(t, configsync.StepFailed, s.Status, applyJSON(s))
	}

	row := f.svcRow(f.staging, "jobs")
	require.Equal(t, services.KindApp, row.Kind, "a process with an HTTP port makes an app")
	cfg := f.cfgOf(row.ID)
	require.Equal(t, "app", cfg.ProcessName)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, []string{"clock", "metrics", "worker"}, applyProcessNames(cfg), "the processes are stored sorted by name")
	worker, clock, metrics := cfg.Processes[2], cfg.Processes[0], cfg.Processes[1]
	require.Equal(t, []string{"bundle", "exec", "sidekiq"}, worker.StartCommand)
	require.Equal(t, 120, worker.StopGraceSeconds)
	require.Equal(t, "none", worker.Healthcheck.Type, "a process without a port has no health check")
	require.True(t, clock.Singleton)
	require.Equal(t, 9100, metrics.Port)
	require.Equal(t, "http", metrics.Healthcheck.Type, "the health check follows the process' own port")
	require.Equal(t, 9100, metrics.Healthcheck.Port)
	require.Equal(t, "/", metrics.Healthcheck.Path)

	// The domain of the default process stores "", the one of a named process its name and port.
	doms := f.domainRows(row.ID)
	require.Len(t, doms, 2)
	require.Equal(t, "", doms["app.example.com"].Component)
	require.EqualValues(t, 8080, doms["app.example.com"].TargetPort)
	require.Equal(t, "metrics", doms["metrics.example.com"].Component)
	require.EqualValues(t, 9100, doms["metrics.example.com"].TargetPort)

	// The scale of the worker went through Scaler.SetProcess; the others keep their (missing) rows.
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "scale:jobs:worker"))
	require.Equal(t, -1, applyStepIndex(res, "scale:jobs:clock"))
	require.Equal(t, []string{"worker"}, f.scaler.processCalls())
	scales := f.processScales(row.ID)
	require.Len(t, scales, 1)
	require.EqualValues(t, 2, scales["worker"].Replicas)
	require.EqualValues(t, 1, row.Replicas, "the service's own replicas are the default process'")

	owned := f.owned(row.ID)
	for _, id := range []string{
		"config.processName", "config.processes.worker", "config.processes.clock", "config.processes.metrics",
		"processes.worker.scale.replicas", "domains.app.example.com", "domains.metrics.example.com",
	} {
		require.Contains(t, owned, id)
	}
	require.Equal(t, applyFP(2), owned["processes.worker.scale.replicas"], "the scale of a process is an immediate key of its own")

	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", applyProcessesFile).Status, "the applied file plans to nothing")
}

// A service none of whose processes listens on a port is a worker; one process with a port makes an app.
func TestApplyServiceKindFollowsTheProcessPorts(t *testing.T) {
	f := newApplyFixture(t)
	const file = `schema = 1
server = "agent1"

[services.queue]
image = "busybox:1"

[services.queue.processes.sidekiq]
command = ["bundle", "exec", "sidekiq"]

[services.site]
image = "nginx:1.27"

[services.site.processes.http]
command = ["bin/serve"]
port = 8000
`
	pl := f.plan("staging", file)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	f.mustApply("staging", file)
	require.Equal(t, services.KindWorker, f.svcRow(f.staging, "queue").Kind)
	require.Equal(t, services.KindApp, f.svcRow(f.staging, "site").Kind)
	require.Equal(t, []string{"sidekiq"}, applyProcessNames(f.cfgOf(f.svcRow(f.staging, "queue").ID)))
}

func TestApplyProcessScaleIsAnImmediateStepPerProcess(t *testing.T) {
	f := newApplyFixture(t)
	base := applyWebFile(8080, `
[services.web.processes.worker]
command = ["bundle", "exec", "sidekiq"]

[services.web.processes.mailer]
command = ["bin/mailer"]
`)
	f.mustApply("staging", base)
	web := f.svcRow(f.staging, "web")
	require.Empty(t, f.processScales(web.ID), "a process that was never scaled has no row")
	require.Empty(t, f.scaler.processCalls())
	commands := f.disp.count()

	scaled := base + `
[services.web.processes.worker.scale]
replicas = 3

[services.web.processes.mailer.scale.autoscaling]
enabled = true
min = 1
max = 4
target_cpu_percent = 60
`
	// A file that is not allowed to scale is refused before anything is written.
	before := f.state()
	_, err := f.apply(f.token(policy.ScopeProjectsWrite), "staging", scaled)
	ae := applyRequireError(t, err, 403, configsync.CodeInsufficientScope)
	require.Equal(t, []string{"deployments:write"}, ae.Details["scopes"], "a process' scale needs deployments:write like the service's own")
	require.Equal(t, before, f.state())
	require.Empty(t, f.scaler.processCalls())

	pl := f.plan("staging", scaled)
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.ElementsMatch(t, []string{
		"processes.mailer.scale.autoscaling.enabled", "processes.mailer.scale.autoscaling.max",
		"processes.mailer.scale.autoscaling.targetCpuPercent", "processes.worker.scale.replicas", // min = 1 is the live default: no change
	}, applyChangeFields(applyPlanService(t, pl, "web")))

	res := f.mustApply("staging", scaled, applyWithPlanID(pl.PlanID))
	require.NotEqual(t, configsync.StatusPartial, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "scale:web:worker"))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "scale:web:mailer"))
	require.Less(t, applyStepIndex(res, "scale:web:mailer"), applyStepIndex(res, "scale:web:worker"), "one step per process, in name order")
	require.Equal(t, []string{"mailer", "worker"}, f.scaler.processCalls(), "each step calls Scaler.SetProcess once")

	scales := f.processScales(web.ID)
	require.EqualValues(t, 3, scales["worker"].Replicas)
	require.EqualValues(t, 1, scales["mailer"].Replicas)
	as := services.DecodeAutoscaling(scales["mailer"].Autoscaling)
	require.True(t, as.Enabled)
	require.Equal(t, 4, as.Max)
	require.Equal(t, 60, as.TargetCPUPercent)
	require.EqualValues(t, 1, f.row(web.ID).Replicas, "the default process' scale is untouched")
	require.Equal(t, commands, f.disp.count(), "a process that is not deployed yet only stores the setting: no agent command")

	owned := f.owned(web.ID)
	require.Equal(t, applyFP(3), owned["processes.worker.scale.replicas"])
	require.Equal(t, applyFP(true), owned["processes.mailer.scale.autoscaling.enabled"])
	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", scaled).Status)

	// A failing step is reported and records no ownership; the next apply converges.
	f.scaler.procFail.set(errors.New("scale boom"))
	more := strings.Replace(scaled, "replicas = 3", "replicas = 4", 1)
	res = f.mustApply("staging", more)
	require.Equal(t, configsync.StatusFailed, res.Status, applyJSON(res.Steps)+" (nothing staged was written, so the apply failed rather than partially applied)")
	require.Equal(t, configsync.StepFailed, applyStepStatus(res, "scale:web:worker"))
	require.Contains(t, applyStepMessage(res, "scale:web:worker"), "scale boom")
	require.EqualValues(t, 3, f.processScales(web.ID)["worker"].Replicas)
	require.Equal(t, applyFP(3), f.owned(web.ID)["processes.worker.scale.replicas"], "a failed step records no ownership")

	f.scaler.procFail.set(nil)
	res = f.mustApply("staging", more)
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "scale:web:worker"))
	require.EqualValues(t, 4, f.processScales(web.ID)["worker"].Replicas)
	require.Equal(t, applyFP(4), f.owned(web.ID)["processes.worker.scale.replicas"])
	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", more).Status)
}

func TestApplyPruneDropsOnlyProcessesTheFilePreviouslyOwned(t *testing.T) {
	f := newApplyFixture(t)
	const worker = `
[services.web.processes.worker]
command = ["bundle", "exec", "sidekiq"]
`
	v1 := applyWebFile(8080, worker+`
[services.web.processes.clock]
command = ["bundle", "exec", "clockwork", "clock.rb"]
singleton = true

[services.web.processes.metrics]
command = ["bin/metrics"]
port = 9100

[services.web.domains."metrics.example.com"]
process = "metrics"
`)
	v2 := applyWebFile(8080, worker)
	res := f.mustApply("staging", v1)
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	web := f.svcRow(f.staging, "web")
	require.Equal(t, []string{"clock", "metrics", "worker"}, applyProcessNames(f.cfgOf(web.ID)))

	// A process made in the dashboard is not the file's.
	cfg := f.cfgOf(web.ID)
	cfg.Processes = append(cfg.Processes, services.ProcessConfig{Name: "adhoc", StartCommand: []string{"bin/adhoc"}})
	_, err := f.svcs.Update(f.admin, web.ID, f.row(web.ID).Generation, services.UpdateInput{Config: &cfg})
	require.NoError(t, err)
	commands := f.disp.count()

	pl := f.plan("staging", v2, applyWithPrune())
	require.True(t, pl.Valid, applyJSON(pl.Diagnostics))
	require.Equal(t, map[string][]string{
		"web": {"config.processes.clock", "config.processes.metrics", "domains.metrics.example.com"},
	}, applyRemovedFields(pl))
	require.Equal(t, 3, pl.Summary.Pruned)
	require.Equal(t, map[string]int{"processes": 3, "domains": 1}, applyPlanService(t, pl, "web").Unmanaged, "every live process and domain the file does not name is counted, the pruned ones included")

	res = f.mustApply("staging", v2, applyWithPrune(), applyWithPlanID(pl.PlanID))
	require.Equal(t, configsync.StatusStaged, res.Status, applyJSON(res.Steps))
	require.Equal(t, configsync.StepApplied, applyStepStatus(res, "processes:web"))
	require.Equal(t, []string{"adhoc", "worker"}, applyProcessNames(f.cfgOf(web.ID)))
	require.NotContains(t, f.domainRows(web.ID), "metrics.example.com", "the domain of a dropped process goes first")
	owned := f.owned(web.ID)
	require.NotContains(t, owned, "config.processes.clock")
	require.NotContains(t, owned, "config.processes.metrics")
	require.Contains(t, owned, "config.processes.worker")
	require.Equal(t, commands, f.disp.count(), "pruning a process is a staged change: nothing reaches an agent")

	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", v2, applyWithPrune()).Status)
}

func TestApplyWithoutPruneAProcessThatLeftTheFileStays(t *testing.T) {
	f := newApplyFixture(t)
	worker := `
[services.web.processes.worker]
command = ["bundle", "exec", "sidekiq"]
`
	v1 := applyWebFile(8080, worker+`
[services.web.processes.clock]
command = ["clockwork"]
`)
	v2 := applyWebFile(8080, worker)
	f.mustApply("staging", v1)
	web := f.svcRow(f.staging, "web")

	pl := f.plan("staging", v2)
	require.Empty(t, applyRemovedFields(pl))
	require.Equal(t, []string{"services.web.processes.clock"}, applyPlanService(t, pl, "web").Released)
	f.mustApply("staging", v2)
	require.Equal(t, []string{"clock", "worker"}, applyProcessNames(f.cfgOf(web.ID)), "released, not removed")
	require.NotContains(t, f.owned(web.ID), "config.processes.clock")

	// Ownership is gone, so even a prune never touches it.
	require.Empty(t, applyRemovedFields(f.plan("staging", v2, applyWithPrune())))
	require.Equal(t, configsync.StatusNoop, f.mustApply("staging", v2, applyWithPrune()).Status)
	require.Equal(t, []string{"clock", "worker"}, applyProcessNames(f.cfgOf(web.ID)))
}
