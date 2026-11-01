package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	canary     = "sekret-canary-tasks-12345"
	canaryPass = "sekret-canary-registry-67890"
)

// realDispatcher persists commands like the hub does (without delivering), so
// the run's command_id foreign key and the result handlers work.
type realDispatcher struct {
	cmds []agents.NewCommand
	ids  []uuid.UUID
}

func (d *realDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	id := uuid.New()
	key := c.IdempotencyKey
	if key == "" {
		key = id.String()
	}
	row, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload, IdempotencyKey: key,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		return uuid.Nil, err
	}
	d.cmds = append(d.cmds, c)
	d.ids = append(d.ids, row.ID)
	return row.ID, nil
}

func (d *realDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *realDispatcher) last() (agents.NewCommand, uuid.UUID) {
	return d.cmds[len(d.cmds)-1], d.ids[len(d.ids)-1]
}

type recNotifier struct {
	mu  sync.Mutex
	evs []notifications.Event
}

func (r *recNotifier) Emit(_ context.Context, ev notifications.Event) {
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	r.mu.Unlock()
}

func (r *recNotifier) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		out = append(out, e.Type)
	}
	return out
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ctx    context.Context
	box    *secretbox.Box
	disp   *realDispatcher
	notif  *recNotifier
	svc    *Service
	svcs   *services.Service
	ws     db.Workspace
	proj   db.Project
	env    db.Environment
	server db.Server
	clock  time.Time
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
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)

	disp := &realDispatcher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, disp, logger)
	notif := &recNotifier{}
	f := &fixture{t: t, pool: pool, q: q, box: box, disp: disp, notif: notif, svcs: svcs, ws: ws, proj: prj, env: env, server: srv,
		clock: time.Date(2026, 10, 26, 12, 3, 10, 0, time.UTC)}
	f.svc = NewService(Deps{Pool: pool, Bus: bus, Box: box, Dispatcher: disp, Redactor: svcs, Notifier: notif, Logger: logger})
	f.svc.now = func() time.Time { return f.clock }
	f.ctx = auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "owner@example.com", Scopes: policy.AllScopes})
	return f
}

// deployed inserts an image service on the server with a succeeded deployment
// whose release carries a secret variable (canary), a plain variable and a
// build-only variable.
func (f *fixture) deployed() db.Service {
	f.t.Helper()
	const slug = "api"
	bg := context.Background()
	raw, _ := json.Marshal(services.Config{Image: "ghcr.io/acme/api:1", Port: 8080, MemoryMB: 256, CPUs: 0.5}.WithDefaults())
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	enc, err := f.box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(sv.ID.String(), "API_KEY"), canary)
	require.NoError(f.t, err)
	_, err = f.q.UpsertServiceVariable(bg, db.UpsertServiceVariableParams{ServiceID: sv.ID, Key: "API_KEY", ValueEnc: enc, IsSecret: true})
	require.NoError(f.t, err)

	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(sv.Config), ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: "ghcr.io/acme/api:1", Digest: "ghcr.io/acme/api@sha256:abc"})
	envSnap, _ := json.Marshal(services.EnvSnapshot{
		Vars:   map[string]string{"API_KEY": canary, "MODE": "production", "BUILD_ONLY": "x", "NEBULA_SERVICE_NAME": slug},
		Secret: []string{"API_KEY"}, Scope: map[string]string{"BUILD_ONLY": services.ScopeBuild},
	})
	relID := uuid.New()
	envEnc, err := f.box.Seal(secretbox.ReleasesEnv, relID.String(), envSnap)
	require.NoError(f.t, err)
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: relID, ServiceID: sv.ID, Source: src, Config: snap, EnvEnc: envEnc, ImageRef: "ghcr.io/acme/api:1"})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	row, err := f.q.GetServiceScopeInternal(bg, sv.ID)
	require.NoError(f.t, err)
	sv = row.Service
	return sv
}

func (f *fixture) result(id uuid.UUID, outcome, errMsg string, data agentprotocol.RunTaskResult) agentprotocol.ResultFrame {
	raw, _ := json.Marshal(data)
	return agentprotocol.ResultFrame{CommandID: id.String(), Outcome: outcome, Data: raw, Error: errMsg}
}

func (f *fixture) runs(serviceID uuid.UUID) []RunView {
	f.t.Helper()
	out, err := f.svc.ListRuns(f.ctx, serviceID, nil, 200)
	require.NoError(f.t, err)
	return out
}

func (f *fixture) task(serviceID uuid.UUID, schedule string, mut ...func(*TaskInput)) TaskView {
	f.t.Helper()
	in := TaskInput{Name: "nightly", Schedule: schedule, Command: []string{"rake", "cleanup"}}
	for _, m := range mut {
		m(&in)
	}
	v, err := f.svc.CreateTask(f.ctx, serviceID, in)
	require.NoError(f.t, err)
	return v
}

// ---- scheduler ----

func TestSchedulerFiresOnTheMinuteWithFakeClock(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "*/5 * * * *") // created at 12:03:10
	require.NotNil(t, task.NextRunAt)
	require.Equal(t, time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC), *task.NextRunAt)

	f.clock = time.Date(2026, 10, 26, 12, 4, 59, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Empty(t, f.runs(sv.ID), "nothing is due before 12:05")

	f.clock = time.Date(2026, 10, 26, 12, 5, 1, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	runs := f.runs(sv.ID)
	require.Len(t, runs, 1)
	require.Equal(t, SourceCron, runs[0].Source)
	require.Equal(t, StatusQueued, runs[0].Status)
	require.Equal(t, "nightly", runs[0].Name)
	require.NotNil(t, runs[0].ScheduledFor)
	require.Equal(t, time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC), *runs[0].ScheduledFor)
	cmd, _ := f.disp.last()
	require.Equal(t, agentprotocol.KindRunTask, cmd.Kind)
	require.Equal(t, f.server.ID, cmd.ServerID)

	got, err := f.svc.GetTask(f.ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 26, 12, 10, 0, 0, time.UTC), *got.NextRunAt)

	// The same minute again does nothing.
	f.clock = f.clock.Add(20 * time.Second)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Len(t, f.runs(sv.ID), 1)

	// A control plane that was away for two hours runs the task once, not 24 times.
	_, err = f.pool.Exec(context.Background(), "UPDATE task_runs SET status = 'succeeded', finished_at = now()")
	require.NoError(t, err)
	f.clock = time.Date(2026, 10, 26, 14, 0, 20, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Len(t, f.runs(sv.ID), 2)
	got, _ = f.svc.GetTask(f.ctx, task.ID)
	require.Equal(t, time.Date(2026, 10, 26, 14, 5, 0, 0, time.UTC), *got.NextRunAt)
}

func TestSchedulerEveryMinuteTask(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	f.task(sv.ID, "* * * * *", func(in *TaskInput) { in.Concurrency = ConcurrencyAllow })
	for i := 1; i <= 3; i++ {
		f.clock = f.clock.Truncate(time.Minute).Add(time.Minute + time.Second)
		require.NoError(t, f.svc.Tick(f.ctx))
		require.Len(t, f.runs(sv.ID), i, "one run per minute")
	}
}

func TestDisabledTaskNeverRuns(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "* * * * *", func(in *TaskInput) { in.Enabled = new(false) })
	require.Nil(t, task.NextRunAt)
	f.clock = f.clock.Add(time.Hour)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Empty(t, f.runs(sv.ID))

	// Enabling it schedules the next minute from now.
	v, err := f.svc.UpdateTask(f.ctx, task.ID, TaskPatch{Enabled: new(true)})
	require.NoError(t, err)
	require.NotNil(t, v.NextRunAt)
	require.True(t, v.NextRunAt.After(f.clock))
}

func TestOverlapForbidSkipsAndAllowStarts(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "* * * * *") // forbid is the default

	f.clock = time.Date(2026, 10, 26, 12, 4, 1, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Len(t, f.runs(sv.ID), 1)
	require.Equal(t, StatusQueued, f.runs(sv.ID)[0].Status)
	require.Len(t, f.disp.cmds, 1)

	// The run is still active at the next tick: the tick is skipped and says why.
	f.clock = f.clock.Add(time.Minute)
	require.NoError(t, f.svc.Tick(f.ctx))
	runs := f.runs(sv.ID)
	require.Len(t, runs, 2)
	require.Equal(t, StatusSkipped, runs[0].Status)
	require.Contains(t, runs[0].Error, "previous run is still running")
	require.Len(t, f.disp.cmds, 1, "no second container")

	// A streak of the same skip reason is recorded once.
	f.clock = f.clock.Add(time.Minute)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Len(t, f.runs(sv.ID), 2)

	// concurrency: allow starts a second run while the first is active.
	_, err := f.svc.UpdateTask(f.ctx, task.ID, TaskPatch{Concurrency: new(ConcurrencyAllow)})
	require.NoError(t, err)
	f.clock = f.clock.Add(time.Minute)
	require.NoError(t, f.svc.Tick(f.ctx))
	require.Len(t, f.disp.cmds, 2)
}

func TestTickSkipsWhenServerOfflineOrNotDeployed(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	f.task(sv.ID, "* * * * *")
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	f.clock = f.clock.Add(2 * time.Minute)
	require.NoError(t, f.svc.Tick(f.ctx))
	runs := f.runs(sv.ID)
	require.Len(t, runs, 1)
	require.Equal(t, StatusSkipped, runs[0].Status)
	require.Contains(t, runs[0].Error, "agent1 is offline")
	require.Empty(t, f.disp.cmds)
	require.Empty(t, f.notif.types(), "a skipped tick is not a failure")

	// Not deployed yet.
	raw, _ := json.Marshal(services.Config{Image: "x:1"}.WithDefaults())
	fresh, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "fresh", Slug: "fresh", Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	_, err = f.svc.RunCommand(f.ctx, fresh.ID, []string{"ls"}, 0)
	requireAPIError(t, err, "not_deployed")
}

func requireAPIError(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, code, ae.Code)
}

// ---- runs ----

func TestRunCommandPayloadNeverCarriesSecrets(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()

	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"rake", "db:migrate"}, 120)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, v.Status)
	require.Equal(t, SourceOneOff, v.Source)

	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindRunTask, cmd.Kind)
	var persisted string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT payload::text FROM agent_commands WHERE id = $1", cmdID).Scan(&persisted))
	require.NotContains(t, persisted, canary, "no secret value in the persisted command payload")
	require.NotContains(t, persisted, "production", "no variable value at all")

	var p agentprotocol.RunTaskPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, v.ID.String(), p.RunID)
	require.Equal(t, []string{"rake", "db:migrate"}, p.Command)
	require.Equal(t, 120, p.TimeoutSeconds)
	require.Equal(t, 256, p.MemoryMB)
	require.Equal(t, 0.5, p.CPUs)
	require.Equal(t, "ghcr.io/acme/api@sha256:abc", p.ImageRef, "the digest of the release")
	require.Contains(t, p.EnvKeys, "API_KEY")
	require.Contains(t, p.EnvKeys, "MODE")
	require.Contains(t, p.EnvKeys, services.VarDeploymentID)
	require.NotContains(t, p.EnvKeys, "BUILD_ONLY", "build-only variables are not part of the container environment")
	require.Equal(t, []string{"API_KEY"}, p.SecretEnvKeys)

	// Values are attached at send time only.
	secrets, err := f.svc.SecretsForCommand(context.Background(), agentprotocol.KindRunTask, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, canary, secrets["env:API_KEY"])
	require.Equal(t, "production", secrets["env:MODE"])
	require.Equal(t, p.DeploymentID, secrets["env:"+services.VarDeploymentID])
	require.NotContains(t, secrets, "env:BUILD_ONLY")
}

func TestRunCommandNeedsDeploymentsWriteAndAuditsTheCommand(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()

	readOnly := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ci", Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite},
	})
	_, err := f.svc.RunCommand(readOnly, sv.ID, []string{"ls"}, 0)
	require.Error(t, err)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, 403, ae.Status)
	_, err = f.svc.RunCommand(readOnly, sv.ID, nil, 0)
	require.Error(t, err)

	_, err = f.svc.RunCommand(f.ctx, sv.ID, []string{"sh", "-c", "echo hi"}, 0)
	require.NoError(t, err)
	var action, meta string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT action, metadata::text FROM audit_events WHERE action = $1", ActionRunCommand).Scan(&action, &meta))
	require.Equal(t, ActionRunCommand, action)
	require.Contains(t, meta, "sh -c echo hi")
	require.Equal(t, 1, len(f.disp.cmds), "the forbidden call enqueued nothing")

	_, err = f.svc.RunCommand(f.ctx, sv.ID, []string{" "}, 0)
	requireAPIError(t, err, "invalid_command")
	_, err = f.svc.RunCommand(f.ctx, sv.ID, []string{"ls"}, 999999)
	requireAPIError(t, err, "invalid_timeout")
}

func TestRunTaskNowIgnoresScheduleAndPolicy(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "0 3 * * *")
	v, err := f.svc.RunTaskNow(f.ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, SourceManual, v.Source)
	require.NotNil(t, v.TaskID)
	_, err = f.svc.RunTaskNow(f.ctx, task.ID) // forbid applies to ticks only
	require.NoError(t, err)
	require.Len(t, f.disp.cmds, 2)
	// "Run now" does not move the schedule.
	got, _ := f.svc.GetTask(f.ctx, task.ID)
	require.Equal(t, time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC), *got.NextRunAt)
}

func TestResultLifecycleAndNotifications(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	started := time.Date(2026, 10, 26, 12, 5, 1, 0, time.UTC)
	ended := started.Add(42 * time.Second)

	// A user-started failure does not page anyone.
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"false"}, 0)
	require.NoError(t, err)
	_, cmdID := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "exited with code 1",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeFailed, ExitCode: 1, Message: "exited with code 1", StartedAt: started, FinishedAt: ended})))
	got, err := f.svc.GetRun(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.NotNil(t, got.ExitCode)
	require.Equal(t, 1, *got.ExitCode)
	require.Equal(t, "exited with code 1", got.Error)
	require.Empty(t, f.notif.types())

	// A duplicate result changes nothing.
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeSucceeded, StartedAt: started, FinishedAt: ended})))
	got, _ = f.svc.GetRun(f.ctx, v.ID)
	require.Equal(t, StatusFailed, got.Status)

	// A failing cron run notifies, with the task facts.
	f.task(sv.ID, "* * * * *")
	f.clock = f.clock.Add(2 * time.Minute)
	require.NoError(t, f.svc.Tick(f.ctx))
	_, cronCmd := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, cronCmd, f.result(cronCmd, agentprotocol.OutcomeFailed, "exited with code 2",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeFailed, ExitCode: 2, Message: "exited with code 2", StartedAt: started, FinishedAt: ended})))
	require.Equal(t, []string{notifications.TypeTaskFailed}, f.notif.types())
	ev := f.notif.evs[0]
	require.Equal(t, "nightly", ev.Fields["Task"])
	require.Equal(t, "2", ev.Fields["Exit code"])
	require.Equal(t, "42s", ev.Fields["Duration"])
	require.NotNil(t, ev.ServiceID)
	require.Equal(t, sv.ID, *ev.ServiceID)

	// Success and timeouts.
	f.clock = f.clock.Add(2 * time.Minute)
	_, err = f.pool.Exec(context.Background(), "UPDATE task_runs SET status = 'failed', finished_at = now() WHERE status = 'queued'")
	require.NoError(t, err)
	require.NoError(t, f.svc.Tick(f.ctx))
	_, okCmd := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, okCmd, f.result(okCmd, agentprotocol.OutcomeSucceeded, "",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeSucceeded, StartedAt: started, FinishedAt: ended})))
	list, err := f.svc.ListTasks(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].LastRun)
	require.Equal(t, StatusSucceeded, list[0].LastRun.Status)
	require.Len(t, f.notif.types(), 1, "success does not notify")

	f.clock = f.clock.Add(2 * time.Minute)
	require.NoError(t, f.svc.Tick(f.ctx))
	_, toCmd := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, toCmd, f.result(toCmd, agentprotocol.OutcomeFailed, "timed out after 30m0s",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeFailed, TimedOut: true, ExitCode: 137, Message: "timed out", StartedAt: started, FinishedAt: ended})))
	list, _ = f.svc.ListTasks(f.ctx, sv.ID)
	require.Equal(t, StatusTimedOut, list[0].LastRun.Status)
	require.Len(t, f.notif.types(), 2)
}

func TestUndeliveredRunFailsAndStaleRunsAreFailed(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"ls"}, 60)
	require.NoError(t, err)
	_, cmdID := f.disp.last()
	require.NoError(t, f.svc.HandleUndelivered(f.ctx, cmdID, "Not delivered: server offline. No changes were made."))
	got, _ := f.svc.GetRun(f.ctx, v.ID)
	require.Equal(t, StatusFailed, got.Status)
	require.Contains(t, got.Error, "Not delivered")

	// An acked run whose agent never answers is failed once timeout + grace passed.
	v2, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"sleep", "9"}, 60)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE task_runs SET created_at = now() - interval '2 hours' WHERE id = $1", v2.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Tick(f.ctx))
	got, _ = f.svc.GetRun(f.ctx, v2.ID)
	require.Equal(t, StatusFailed, got.Status)
	require.Contains(t, got.Error, "never reported")
}

func TestHistoryKeepsTheNewest50Runs(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	task := f.task(sv.ID, "0 3 * * *")
	var ids []uuid.UUID
	for i := range HistoryPerTask + 7 {
		v, err := f.svc.RunTaskNow(f.ctx, task.ID)
		require.NoError(t, err)
		ids = append(ids, v.ID)
		_, cmdID := f.disp.last()
		// Distinct creation times keep "newest" unambiguous.
		_, err = f.pool.Exec(context.Background(), "UPDATE task_runs SET created_at = now() - make_interval(secs => $2) WHERE id = $1", v.ID, float64(1000-i))
		require.NoError(t, err)
		require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "",
			agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeSucceeded, StartedAt: f.clock, FinishedAt: f.clock})))
	}
	runs, err := f.svc.ListRuns(f.ctx, sv.ID, &task.ID, 200)
	require.NoError(t, err)
	require.Len(t, runs, HistoryPerTask)
	kept := map[uuid.UUID]bool{}
	for _, r := range runs {
		kept[r.ID] = true
	}
	require.True(t, kept[ids[len(ids)-1]], "the newest run is kept")
	require.False(t, kept[ids[0]], "the oldest run was pruned")

	// Another task's history is separate; one-offs have their own.
	other := f.task(sv.ID, "0 4 * * *", func(in *TaskInput) { in.Name = "other" })
	_, err = f.svc.RunTaskNow(f.ctx, other.ID)
	require.NoError(t, err)
	runs, _ = f.svc.ListRuns(f.ctx, sv.ID, &other.ID, 200)
	require.Len(t, runs, 1)

	// A pruned run's output goes with it.
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM task_runs WHERE id = $1", ids[0]).Scan(&n))
	require.Zero(t, n)
}

func TestRunActiveRunsAreNeverPruned(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"sleep", "1000"}, 0)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE task_runs SET created_at = now() - interval '10 minutes' WHERE id = $1", v.ID)
	require.NoError(t, err)
	for range HistoryPerTask + 3 {
		r, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"true"}, 0)
		require.NoError(t, err)
		_, cmdID := f.disp.last()
		require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "",
			agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeSucceeded, StartedAt: f.clock, FinishedAt: f.clock})))
		_ = r
	}
	got, err := f.svc.GetRun(f.ctx, v.ID)
	require.NoError(t, err, "the still-active run survives pruning")
	require.Equal(t, StatusQueued, got.Status)
}

// ---- output ----

func TestOutputIsRedactedAndCapped(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"env"}, 0)
	require.NoError(t, err)

	frame := agentprotocol.LogFrame{TaskRunID: v.ID.String(), Stream: "task", Lines: []agentprotocol.LogLine{
		{Text: "API_KEY=" + canary}, {Text: "MODE=production"}, {Text: "bad\x00bytes"},
	}}
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, frame))
	lines, err := f.svc.RunLogs(f.ctx, v.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, lines, 3)
	require.NotContains(t, lines[0].Line, canary, "redacted with the service's secrets")
	require.Contains(t, lines[0].Line, "••••••")
	require.Equal(t, "MODE=production", lines[1].Line)
	require.Equal(t, "badbytes", lines[2].Line)
	got, _ := f.svc.GetRun(f.ctx, v.ID)
	require.Equal(t, 3, got.LogLines)

	// Frames from another server, for unknown runs or finished runs are ignored.
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "other"})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), other.ID, frame))
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{TaskRunID: uuid.NewString(), Lines: frame.Lines}))
	lines, _ = f.svc.RunLogs(f.ctx, v.ID, 0, 100)
	require.Len(t, lines, 3)

	// The cap: after 10,000 lines one marker, then nothing more.
	big := make([]agentprotocol.LogLine, 5000)
	for i := range big {
		big[i] = agentprotocol.LogLine{Text: fmt.Sprintf("line %d", i)}
	}
	for range 3 {
		require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, agentprotocol.LogFrame{TaskRunID: v.ID.String(), Lines: big}))
	}
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM task_run_logs WHERE run_id = $1", v.ID).Scan(&count))
	require.Equal(t, MaxRunLogLines+1, count)
	var last string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT line FROM task_run_logs WHERE run_id = $1 ORDER BY id DESC LIMIT 1", v.ID).Scan(&last))
	require.Equal(t, LogTruncatedMarker, last)

	// Finished runs take no more output.
	_, cmdID := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeSucceeded, StartedAt: f.clock, FinishedAt: f.clock})))
	require.NoError(t, f.svc.HandleTaskLog(context.Background(), f.server.ID, frame))
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM task_run_logs WHERE run_id = $1", v.ID).Scan(&count))
	require.Equal(t, MaxRunLogLines+1, count)
}

func TestResultMessageIsRedacted(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	v, err := f.svc.RunCommand(f.ctx, sv.ID, []string{"x"}, 0)
	require.NoError(t, err)
	_, cmdID := f.disp.last()
	msg := "could not run: token " + canary + " rejected"
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, msg,
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeFailed, ExitCode: -1, Message: msg})))
	got, _ := f.svc.GetRun(f.ctx, v.ID)
	require.NotContains(t, got.Error, canary)
	require.Nil(t, got.ExitCode, "no exit code when the container never ran")
}

// ---- RunOneOff (previews) ----

func TestRunOneOffForInternalCallers(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()

	var runID uuid.UUID
	err := db.InTx(context.Background(), f.pool, func(tx pgx.Tx) error {
		var err error
		runID, err = f.svc.RunOneOff(context.Background(), tx, sv.ID, []string{"rake", "db:seed"}, RunOpts{Source: "preview_seed", Timeout: 5 * time.Minute})
		return err
	})
	require.NoError(t, err)
	status, exit, msg, err := f.svc.RunStatus(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, status)
	require.Nil(t, exit)
	require.Empty(t, msg)
	var p agentprotocol.RunTaskPayload
	cmd, cmdID := f.disp.last()
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, 300, p.TimeoutSeconds)

	// The agent acked the command: the run is running.
	_, err = f.pool.Exec(context.Background(), "UPDATE agent_commands SET status = 'acked' WHERE id = $1", cmdID)
	require.NoError(t, err)
	status, _, _, _ = f.svc.RunStatus(context.Background(), runID)
	require.Equal(t, StatusRunning, status)

	require.NoError(t, f.svc.HandleResult(context.Background(), cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "exited with code 3",
		agentprotocol.RunTaskResult{Outcome: agentprotocol.OutcomeFailed, ExitCode: 3, Message: "exited with code 3", StartedAt: f.clock, FinishedAt: f.clock})))
	status, exit, msg, err = f.svc.RunStatus(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, status)
	require.Equal(t, 3, *exit)
	require.Equal(t, "exited with code 3", msg)
	require.Equal(t, []string{notifications.TypeTaskFailed}, f.notif.types(), "an unattended feature run that fails notifies")

	// Not deployed: the caller gets a 409 and its transaction rolls back.
	raw, _ := json.Marshal(services.Config{Image: "x:1"}.WithDefaults())
	fresh, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "fresh", Slug: "fresh", Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	err = db.InTx(context.Background(), f.pool, func(tx pgx.Tx) error {
		_, err := f.svc.RunOneOff(context.Background(), tx, fresh.ID, []string{"x"}, RunOpts{})
		return err
	})
	requireAPIError(t, err, "not_deployed")
}

// ---- cron task CRUD ----

func TestTaskValidationAndIsolation(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()

	_, err := f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "x", Schedule: "61 * * * *", Command: []string{"a"}})
	requireAPIError(t, err, "invalid_cron")
	_, err = f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "x", Schedule: "0 0 30 2 *", Command: []string{"a"}})
	requireAPIError(t, err, "cron_never_fires")
	_, err = f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "x", Schedule: "* * * * *"})
	requireAPIError(t, err, "invalid_command")
	_, err = f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "", Schedule: "* * * * *", Command: []string{"a"}})
	requireAPIError(t, err, "invalid_name")
	_, err = f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "x", Schedule: "* * * * *", Command: []string{"a"}, Concurrency: "sometimes"})
	requireAPIError(t, err, "invalid_concurrency")

	f.task(sv.ID, "@daily")
	_, err = f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "nightly", Schedule: "@hourly", Command: []string{"a"}})
	requireAPIError(t, err, "task_exists")

	// Another workspace sees nothing.
	ws2, err := f.q.CreateWorkspace(context.Background(), "w2")
	require.NoError(t, err)
	other := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, WorkspaceID: ws2.ID, Label: "x", Scopes: policy.AllScopes})
	_, err = f.svc.ListTasks(other, sv.ID)
	require.Error(t, err)
	ae, _ := apierr.As(err)
	require.Equal(t, 404, ae.Status)

	// Deleting a task removes its runs.
	list, _ := f.svc.ListTasks(f.ctx, sv.ID)
	_, err = f.svc.RunTaskNow(f.ctx, list[0].ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteTask(f.ctx, list[0].ID))
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM task_runs WHERE service_id = $1", sv.ID).Scan(&n))
	require.Zero(t, n)
}

func TestTaskLimitPerService(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	for i := range MaxTasksPerService {
		f.task(sv.ID, "@daily", func(in *TaskInput) { in.Name = fmt.Sprintf("t%d", i) })
	}
	_, err := f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "one-too-many", Schedule: "@daily", Command: []string{"a"}})
	requireAPIError(t, err, "too_many_tasks")
}

// ---- schedule description ----

func TestDescribeAndPreviewSchedule(t *testing.T) {
	cases := map[string]string{
		"* * * * *":     "Every minute (UTC)",
		"*/5 * * * *":   "Every 5 minutes (UTC)",
		"0 * * * *":     "Every hour, on the hour (UTC)",
		"15 * * * *":    "Every hour at minute 15 (UTC)",
		"0 */6 * * *":   "Every 6 hours at minute 0 (UTC)",
		"30 3 * * *":    "Every day at 03:30 UTC",
		"@daily":        "Every day at 00:00 UTC",
		"0 9 * * 1-5":   "Every weekday (Monday to Friday) at 09:00 UTC",
		"0 9 * * mon":   "Every Monday at 09:00 UTC",
		"0 9 * * 1,3,5": "Every Monday, Wednesday and Friday at 09:00 UTC",
		"@weekly":       "Every Sunday at 00:00 UTC",
		"0 4 1 * *":     "On day 1 of every month at 04:00 UTC",
		"@monthly":      "On day 1 of every month at 00:00 UTC",
		"0 0 25 dec *":  "On December 25 at 00:00 UTC",
		"1-5 0 * * *":   "Custom schedule: 1-5 0 * * * (UTC)",
	}
	for expr, want := range cases {
		require.Equal(t, want, Describe(expr, ""), expr)
	}
	require.Equal(t, "Every day at 03:30 Europe/Berlin", Describe("30 3 * * *", "Europe/Berlin"))
	require.Equal(t, "Custom schedule: 1-5 0 * * * (Europe/Berlin)", Describe("1-5 0 * * *", "Europe/Berlin"))

	from := time.Date(2026, 10, 26, 12, 3, 10, 0, time.UTC)
	p := PreviewSchedule("*/20 * * * *", "", from, 3)
	require.True(t, p.Valid)
	require.Equal(t, "UTC", p.Timezone)
	require.Equal(t, []time.Time{
		time.Date(2026, 10, 26, 12, 20, 0, 0, time.UTC),
		time.Date(2026, 10, 26, 12, 40, 0, 0, time.UTC),
		time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC),
	}, p.NextRuns)

	bad := PreviewSchedule("nope", "", from, 3)
	require.False(t, bad.Valid)
	require.NotEmpty(t, bad.Error)
	require.Empty(t, bad.NextRuns)
	require.False(t, strings.HasPrefix(bad.Error, "cron:"))

	badZone := PreviewSchedule("@daily", "Mars/Base", from, 3)
	require.False(t, badZone.Valid)
	require.Contains(t, badZone.Error, "Mars/Base")
	require.False(t, strings.HasPrefix(badZone.Error, "cron:"))
}

func TestNextRunHonoursTimezoneAndDST(t *testing.T) {
	// 03:00 Berlin: CEST (UTC+2) in July, CET (UTC+1) in January.
	got, err := nextRun("0 3 * * *", "Europe/Berlin", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC), got)
	got, err = nextRun("0 3 * * *", "Europe/Berlin", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC), got)

	// "Every day at 03:00" keeps its wall-clock time across the autumn change
	// (2026-10-25, CEST to CET): the UTC instant moves by an hour.
	before, err := nextRun("0 3 * * *", "Europe/Berlin", time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	after, err := nextRun("0 3 * * *", "Europe/Berlin", before)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC), before) // 03:00 CEST
	require.Equal(t, time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC), after)  // 03:00 CET

	// Spring-forward gap in New York: 02:30 does not exist on 2026-03-08, so that day is skipped.
	gap, err := nextRun("30 2 * * *", "America/New_York", time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC), gap) // 02:30 EDT on the 9th

	_, err = nextRun("0 3 * * *", "Mars/Base", time.Now())
	requireAPIError(t, err, "invalid_timezone")
}

func TestNormalizeTimezone(t *testing.T) {
	for in, want := range map[string]string{"": "UTC", "utc": "UTC", " UTC ": "UTC", "Europe/Berlin": "Europe/Berlin"} { //nolint:gocritic // the padded key is a deliberate fixture: NormalizeTimezone must trim whitespace
		got, err := NormalizeTimezone(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := NormalizeTimezone("Not/AZone")
	requireAPIError(t, err, "invalid_timezone")
}

func TestTaskTimezoneLifecycle(t *testing.T) {
	f := newFixture(t)
	sv := f.deployed()
	// The fixture clock is 2026-10-26 12:03:10 UTC. 09:00 Europe/Berlin (CET) = 08:00 UTC.
	task := f.task(sv.ID, "0 9 * * *", func(in *TaskInput) { in.Timezone = "Europe/Berlin" })
	require.Equal(t, "Europe/Berlin", task.Timezone)
	require.Equal(t, time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC), *task.NextRunAt)

	utcTask := f.task(sv.ID, "0 9 * * *", func(in *TaskInput) { in.Name = "utc-default" })
	require.Equal(t, "UTC", utcTask.Timezone, "the default zone is UTC")
	require.Equal(t, time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC), *utcTask.NextRunAt)

	// Changing the zone recomputes the next run; leaving it alone keeps the pending tick.
	tz := "America/New_York"
	upd, err := f.svc.UpdateTask(f.ctx, task.ID, TaskPatch{Timezone: &tz})
	require.NoError(t, err)
	require.Equal(t, tz, upd.Timezone)
	require.Equal(t, time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC), *upd.NextRunAt) // 09:00 EDT, later today
	name := "renamed"
	upd, err = f.svc.UpdateTask(f.ctx, task.ID, TaskPatch{Name: &name})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC), *upd.NextRunAt)

	bad := "Mars/Base"
	_, err = f.svc.UpdateTask(f.ctx, task.ID, TaskPatch{Timezone: &bad})
	requireAPIError(t, err, "invalid_timezone")
	_, err = f.svc.CreateTask(f.ctx, sv.ID, TaskInput{Name: "x", Schedule: "@daily", Timezone: bad, Command: []string{"a"}})
	requireAPIError(t, err, "invalid_timezone")

	// The scheduler advances a due task inside its own zone.
	f.clock = time.Date(2026, 10, 27, 13, 0, 5, 0, time.UTC)
	require.NoError(t, f.svc.Tick(f.ctx))
	got, err := f.svc.GetTask(f.ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 28, 13, 0, 0, 0, time.UTC), *got.NextRunAt)
	require.Equal(t, "America/New_York", got.Timezone)
}
