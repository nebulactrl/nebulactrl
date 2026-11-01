package cleanup

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bg = context.Background()

// slotAtOrBefore is the latest 04:00 UTC at or before t.
func slotAtOrBefore(t time.Time) time.Time {
	t = t.UTC()
	slot := time.Date(t.Year(), t.Month(), t.Day(), 4, 0, 0, 0, time.UTC)
	if slot.After(t) {
		slot = slot.AddDate(0, 0, -1)
	}
	return slot
}

func TestScheduleText(t *testing.T) {
	require.Equal(t, "Daily at 04:00 UTC", ScheduleText(DefaultSchedule))
	require.Equal(t, "Daily at 23:30 UTC", ScheduleText("30  23 * * *"))
	require.Equal(t, "Cron 0 */6 * * * (UTC)", ScheduleText("0 */6 * * *"))
}

func TestParseScheduleRefusesDenseAndInvalidSchedules(t *testing.T) {
	for _, ok := range []string{DefaultSchedule, "@daily", "0 */6 * * *", "*/15 * * * *", "0 3 * * 0", "30 2 1 * *"} {
		_, err := ParseSchedule(ok)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{"", "* * * * *", "*/5 * * * *", "0,10 * * * *", "nonsense", "0 4 * *", "0 25 * * *", "0 0 31 2 *"} {
		_, err := ParseSchedule(bad)
		require.Error(t, err, bad)
	}
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	daily := cronexpr.MustParse(DefaultSchedule)
	slot := time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)
	ptr := func(t time.Time) *time.Time { return &t }
	cases := []struct {
		name string
		ref  time.Time
		sch  *cronexpr.Schedule
		hot  bool
		any  *time.Time
		want string
	}{
		{"due: the last run was yesterday", slot.Add(-24 * time.Hour), daily, false, nil, agentprotocol.CleanupTriggerScheduled},
		{"done today", slot.Add(time.Minute), daily, false, nil, ""},
		{"a server or schedule newer than the slot waits for the next one", slot.Add(time.Hour), daily, false, nil, ""},
		{"emergency: disk hot, nothing ran lately", slot.Add(time.Minute), daily, true, ptr(now.Add(-7 * time.Hour)), agentprotocol.CleanupTriggerEmergency},
		{"emergency: nothing ever ran", slot.Add(time.Hour), daily, true, nil, agentprotocol.CleanupTriggerEmergency},
		{"emergency respects the cooldown", slot.Add(time.Minute), daily, true, ptr(now.Add(-time.Hour)), ""},
		{"no hot disk, no emergency", slot.Add(time.Minute), daily, false, ptr(now.Add(-7 * time.Hour)), ""},
		{"scheduled wins over emergency", slot.Add(-24 * time.Hour), daily, true, nil, agentprotocol.CleanupTriggerScheduled},
		{"a six-hourly schedule fires again", slot.Add(-time.Hour), cronexpr.MustParse("0 */6 * * *"), false, nil, agentprotocol.CleanupTriggerScheduled},
		{"weekly schedule not due midweek", time.Date(2026, 10, 25, 3, 5, 0, 0, time.UTC), cronexpr.MustParse("0 3 * * 0"), false, nil, ""},
		{"weekly schedule due after Sunday 03:00", time.Date(2026, 10, 18, 3, 5, 0, 0, time.UTC), cronexpr.MustParse("0 3 * * 0"), false, nil, agentprotocol.CleanupTriggerScheduled},
		{"no schedule, only the disk", slot.Add(-24 * time.Hour), nil, false, nil, ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, Decide(now, c.ref, c.sch, c.hot, c.any), c.name)
	}
}

func TestDiskHot(t *testing.T) {
	trigger := func(p int32) *int32 { return &p }
	require.True(t, DiskHot(true, nil, 0, 0), "no custom trigger follows the alert flag")
	require.False(t, DiskHot(false, nil, 99, 100))
	require.True(t, DiskHot(false, trigger(60), 60, 100), "at the trigger")
	require.False(t, DiskHot(true, trigger(60), 59, 100), "a custom trigger ignores the alert flag")
	require.False(t, DiskHot(false, trigger(60), 10, 0), "no heartbeat sample yet")
}

func TestSettingsNormalize(t *testing.T) {
	got, err := Settings{Enabled: true}.Normalize()
	require.NoError(t, err)
	require.Equal(t, Settings{Enabled: true, Cron: DefaultSchedule, UnusedImageMinAgeHours: 72, BuildCacheMinAgeDays: 7}, got)
	got, err = Settings{Enabled: true, Cron: " 0   */6 * * * ", UnusedImageMinAgeHours: 24, BuildCacheMinAgeDays: 2, DiskTriggerPercent: 70}.Normalize()
	require.NoError(t, err)
	require.Equal(t, "0 */6 * * *", got.Cron)
	for name, in := range map[string]Settings{
		"cron":    {Cron: "* * * * *"},
		"hours":   {UnusedImageMinAgeHours: 9000},
		"days":    {BuildCacheMinAgeDays: 400},
		"trigger": {DiskTriggerPercent: 20},
		"negtrig": {DiskTriggerPercent: -1},
		"neghour": {UnusedImageMinAgeHours: -5},
	} {
		_, err := in.Normalize()
		require.Equal(t, "validation_failed", apiCode(t, err), name)
	}
}

func TestSupportsCleanup(t *testing.T) {
	require.True(t, SupportsCleanup(nil), "no facts yet: assume a current agent")
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapTunnel}})
	require.False(t, SupportsCleanup(old))
	cur, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapDockerCleanup}})
	require.True(t, SupportsCleanup(cur))
}

// ---- database-backed ----

type fakeDisp struct {
	mu   sync.Mutex
	pool *pgxpool.Pool
	cmds []agents.NewCommand
}

func (d *fakeDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	// A real command row, so the run's command_id is a valid reference.
	id := uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO agent_commands (id, server_id, kind, payload, idempotency_key, expires_at) VALUES ($1,$2,$3,$4,$5, now() + interval '15 minutes')`,
		id, c.ServerID, c.Kind, c.Payload, c.IdempotencyKey)
	return id, err
}

func (d *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *fakeDisp) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cmds)
}

func (d *fakeDisp) last(t *testing.T) agentprotocol.CleanupPayload {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.NotEmpty(t, d.cmds)
	c := d.cmds[len(d.cmds)-1]
	require.Equal(t, agentprotocol.KindDockerCleanup, c.Kind)
	var p agentprotocol.CleanupPayload
	require.NoError(t, json.Unmarshal(c.Payload, &p))
	return p
}

type recorder struct {
	mu sync.Mutex
	ev []notifications.Event
}

func (r *recorder) Emit(_ context.Context, ev notifications.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, ev)
}

func (r *recorder) all() []notifications.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notifications.Event(nil), r.ev...)
}

type fixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	q         *db.Queries
	ws        uuid.UUID
	env       db.Environment
	server    db.Server
	svc       *Service
	disp      *fakeDisp
	rec       *recorder
	admin     context.Context
	developer context.Context
	viewer    context.Context
}

func principal(ws, user uuid.UUID, role string, scopes []string) context.Context {
	return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Role: role, Label: "t", Scopes: scopes})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Agent 1"})
	require.NoError(t, err)
	// Online, enrolled ten days ago.
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online', created_at = now() - interval '10 days' WHERE id = $1", srv.ID)
	require.NoError(t, err)
	srv, err = q.GetServerByID(bg, srv.ID)
	require.NoError(t, err)
	disp := &fakeDisp{pool: pool}
	rec := &recorder{}
	svc := NewService(Deps{Pool: pool, Bus: events.NewBus(), Dispatcher: disp, Notifier: rec})
	return &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, env: env, server: srv, svc: svc, disp: disp, rec: rec,
		admin:     principal(ws.ID, u.ID, auth.RoleOwner, policy.AllScopes),
		developer: principal(ws.ID, u.ID, auth.RoleDeveloper, policy.AllScopes),
		viewer:    principal(ws.ID, u.ID, auth.RoleViewer, []string{policy.ScopeProjectsRead, policy.ScopeServersRead}),
	}
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

// release inserts a release of service sv with the given source.
func (f *fixture) release(sv db.Service, src map[string]string, imageRef string) db.Release {
	f.t.Helper()
	raw, _ := json.Marshal(src)
	r, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: raw, Config: json.RawMessage(`{}`), ImageRef: imageRef})
	require.NoError(f.t, err)
	return r
}

// deployment inserts a deployment of rel on server srv with a status and the age of its finish.
func (f *fixture) deployment(sv db.Service, rel db.Release, srv uuid.UUID, status string, finishedAgo time.Duration) db.Deployment {
	f.t.Helper()
	d, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &srv, Trigger: "manual", ActorType: "user", Strategy: "recreate",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = $2, finished_at = now() - $3::interval WHERE id = $1", d.ID, status, finishedAgo.String())
	require.NoError(f.t, err)
	return d
}

func (f *fixture) service(slug, sourceType string, cfg string) db.Service {
	f.t.Helper()
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: sourceType, Config: json.RawMessage(cfg), ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func TestProtectedSetCoversCurrentRetainedAndActiveReleases(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws, Name: "Agent 2"})
	require.NoError(t, err)

	// A git service keeping the newest 2 releases.
	web := f.service("web", "git", `{"imageRetention":2}`)
	rels := make([]db.Release, 5)
	for i := range rels {
		rels[i] = f.release(web, map[string]string{"type": "git", "imageId": "sha256:web" + string(rune('a'+i))}, "nc-build/web:"+string(rune('a'+i)))
		// rels[4] is the newest.
		f.deployment(web, rels[i], f.server.ID, "succeeded", time.Duration(5-i)*time.Hour)
	}
	// The current deployment points at the oldest release (the newer ones were rolled away from), so
	// that release is current though not among the newest 2 by finish time.
	cur := f.deployment(web, rels[0], f.server.ID, "succeeded", 10*time.Hour)
	_, err = f.pool.Exec(bg, "UPDATE services SET current_deployment_id = $2 WHERE id = $1", web.ID, cur.ID)
	require.NoError(t, err)
	// A release that failed is never a rollback candidate.
	failedRel := f.release(web, map[string]string{"type": "git", "imageId": "sha256:failed"}, "")
	f.deployment(web, failedRel, f.server.ID, "failed", time.Second)

	// An image service with the default retention (5) and an in-flight deployment.
	api := f.service("api", "image", `{}`)
	old := f.release(api, map[string]string{"type": "image", "image": "ghcr.io/acme/api:1", "digest": "ghcr.io/acme/api@sha256:aaa"}, "ghcr.io/acme/api:1")
	f.deployment(api, old, f.server.ID, "succeeded", 2*time.Hour)
	next := f.release(api, map[string]string{"type": "image", "image": "ghcr.io/acme/api:2"}, "ghcr.io/acme/api:2")
	f.deployment(api, next, f.server.ID, "building", 0)

	// Not this server's, and a deleted service's: not protected.
	api2 := f.service("api2", "image", `{}`)
	elsewhere := f.release(api2, map[string]string{"type": "image", "image": "elsewhere:1"}, "elsewhere:1")
	f.deployment(api2, elsewhere, other.ID, "succeeded", time.Hour)
	gone := f.service("gone", "image", `{}`)
	goneRel := f.release(gone, map[string]string{"type": "image", "image": "deleted/app:1"}, "deleted/app:1")
	f.deployment(gone, goneRel, f.server.ID, "succeeded", time.Hour)
	_, err = f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE id = $1", gone.ID)
	require.NoError(t, err)

	// Volumes of services on the server, deleted or not.
	_, err = f.pool.Exec(bg, "INSERT INTO volumes (service_id, name, mount_path) VALUES ($1,'data','/data'),($2,'old','/old')", web.ID, gone.ID)
	require.NoError(t, err)

	prot, err := f.svc.Protected(bg, f.q, f.server.ID)
	require.NoError(t, err)

	// Newest 2 (d, e) plus the current release (a); not b, c or the failed one.
	require.ElementsMatch(t, []string{"sha256:weba", "sha256:webd", "sha256:webe"}, prot.ImageIDs)
	require.ElementsMatch(t, []string{
		"ghcr.io/acme/api:1", "ghcr.io/acme/api@sha256:aaa", "ghcr.io/acme/api:2",
		"nc-build/web:a", "nc-build/web:d", "nc-build/web:e", // a release's image_ref (its build tag)
	}, prot.Images)
	require.ElementsMatch(t, []string{
		agentprotocol.VolumeName(web.ID.String(), "data"), agentprotocol.VolumeName(gone.ID.String(), "old"),
	}, prot.Volumes)
}

// A release built on a build server is pulled from a registry by digest: its
// registry ref is protected on the target, and keeps the same retention.
func TestProtectedSetIncludesRegistryRefsOfBuildServerReleases(t *testing.T) {
	f := newFixture(t)
	web := f.service("web", "git", `{"imageRetention":1}`)
	ref := func(n string) map[string]string {
		return map[string]string{"type": "git", "registryRef": "registry.example.com/shop-web@sha256:" + n}
	}
	older := f.release(web, ref("old"), "registry.example.com/shop-web:r-old")
	f.deployment(web, older, f.server.ID, "succeeded", 3*time.Hour)
	newest := f.release(web, ref("new"), "registry.example.com/shop-web:r-new")
	f.deployment(web, newest, f.server.ID, "succeeded", time.Hour)

	prot, err := f.svc.Protected(bg, f.q, f.server.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"registry.example.com/shop-web@sha256:new", "registry.example.com/shop-web:r-new"}, prot.Images)
	require.Empty(t, prot.ImageIDs)
}

func (f *fixture) lastRun() db.ServerCleanupRun {
	f.t.Helper()
	runs, err := f.q.ListServerCleanupRuns(bg, db.ListServerCleanupRunsParams{ServerID: f.server.ID, Limit: 1})
	require.NoError(f.t, err)
	require.NotEmpty(f.t, runs)
	return runs[0]
}

func (f *fixture) finish(run db.ServerCleanupRun, res agentprotocol.CleanupResult) {
	f.t.Helper()
	raw, _ := json.Marshal(res)
	require.NoError(f.t, f.svc.HandleResult(bg, f.server.ID, *run.CommandID, agentprotocol.ResultFrame{CommandID: run.CommandID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: raw}))
}

func TestSweepStartsOneScheduledRunPerDayWithTheProtectedSet(t *testing.T) {
	f := newFixture(t)
	web := f.service("web", "git", `{"imageRetention":1}`)
	rel := f.release(web, map[string]string{"type": "git", "imageId": "sha256:cur"}, "")
	d := f.deployment(web, rel, f.server.ID, "succeeded", time.Minute)
	_, err := f.pool.Exec(bg, "UPDATE services SET current_deployment_id = $2 WHERE id = $1", web.ID, d.ID)
	require.NoError(t, err)

	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.CleanupModeStandard, p.Mode)
	require.Equal(t, agentprotocol.CleanupTriggerScheduled, p.Trigger)
	require.False(t, p.RemoveStoppedUnmanaged, "stopped unmanaged containers are kept unless switched on")
	require.NotNil(t, p.Protected)
	require.Equal(t, []string{"sha256:cur"}, p.Protected.ImageIDs, "the current release is in the command")
	require.Equal(t, agentprotocol.DefaultUnusedImageMinAgeHours, p.UnusedImageMinAgeHours)
	require.Equal(t, agentprotocol.DefaultBuildCacheMinAgeHours, p.BuildCacheMinAgeHours)
	require.Equal(t, 168, p.BuildCacheMinAgeHours, "7 days")
	run := f.lastRun()
	require.Equal(t, "pending", run.Status)
	require.Equal(t, p.RunID, run.ID.String())

	// While the run is in flight the sweep starts nothing else.
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())

	// Once it is done, nothing is due until the next slot.
	f.finish(run, agentprotocol.CleanupResult{RunID: run.ID.String(), Steps: []agentprotocol.CleanupStep{{Name: agentprotocol.CleanupStepDangling}}})
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())
}

func TestSweepSkipsOfflineDisabledOldAgentAndNewServers(t *testing.T) {
	f := newFixture(t)
	// Offline.
	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count())

	// Online but cleanup switched off.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.Update(f.admin, f.server.ID, Settings{Enabled: false})
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count())
	_, err = f.pool.Exec(bg, "UPDATE servers SET disk_alert_active = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count(), "no emergency run either while it is off")

	// On again, but the agent is too old.
	_, err = f.svc.Update(f.admin, f.server.ID, Settings{Enabled: true})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapTunnel}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count())

	// A server that enrolled after today's slot waits for the next one.
	cur, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapDockerCleanup}})
	_, err = f.pool.Exec(bg, "UPDATE servers SET facts = $2, created_at = now(), disk_alert_active = false WHERE id = $1", f.server.ID, cur)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Zero(t, f.disp.count())
}

func TestDiskThresholdTriggersAnEmergencyRunOnceUntilTheCooldownPasses(t *testing.T) {
	f := newFixture(t)
	// Today's scheduled run is done.
	require.NoError(t, f.svc.Sweep(bg))
	f.finish(f.lastRun(), agentprotocol.CleanupResult{})
	require.Equal(t, 1, f.disp.count())
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count(), "no alert, nothing to do")

	// The disk crosses the threshold (the hub sets the flag on the heartbeat).
	_, err := f.pool.Exec(bg, "UPDATE servers SET disk_alert_active = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	// The scheduled run just finished: inside the cooldown, so no emergency yet.
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())

	// Seven hours later (the clock is moved, and the scheduled run sits right after the slot) the alert is still on.
	base := slotAtOrBefore(time.Now())
	_, err = f.pool.Exec(bg, "UPDATE server_cleanup_runs SET created_at = $1", base.Add(time.Minute))
	require.NoError(t, err)
	f.svc.now = func() time.Time { return base.Add(7 * time.Hour) }
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count(), "the disk alert starts an emergency cleanup")
	p := f.disp.last(t)
	require.Equal(t, agentprotocol.CleanupTriggerEmergency, p.Trigger)
	require.Equal(t, agentprotocol.CleanupModeStandard, p.Mode)
	require.Equal(t, "emergency", f.lastRun().Trigger)

	// And it does not loop.
	f.finish(f.lastRun(), agentprotocol.CleanupResult{})
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count())
}

func TestRunNowAndVolumeActionGuards(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.RunNow(f.viewer, f.server.ID)
	require.Equal(t, "forbidden", apiCode(t, err))
	_, err = f.svc.PruneVolumes(f.viewer, f.server.ID, "Agent 1")
	require.Equal(t, "forbidden", apiCode(t, err))
	// Deleting data needs the Owner or Admin role, not just the write scope.
	_, err = f.svc.PruneVolumes(f.developer, f.server.ID, "Agent 1")
	require.Equal(t, "forbidden", apiCode(t, err))
	require.Zero(t, f.disp.count())

	// Switch the optional container step on: manual runs carry it.
	_, err = f.svc.Update(f.admin, f.server.ID, Settings{Enabled: true, RemoveStoppedUnmanaged: true})
	require.NoError(t, err)
	run, err := f.svc.RunNow(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "manual", run.Trigger)
	require.Equal(t, "pending", run.Status)
	p := f.disp.last(t)
	require.True(t, p.RemoveStoppedUnmanaged)
	require.Equal(t, agentprotocol.CleanupTriggerManual, p.Trigger)

	_, err = f.svc.RunNow(f.admin, f.server.ID)
	require.Equal(t, "cleanup_running", apiCode(t, err))

	// Finish it, then the volume action: the name must be typed.
	f.finish(f.lastRun(), agentprotocol.CleanupResult{})
	_, err = f.svc.PruneVolumes(f.admin, f.server.ID, "wrong")
	require.Equal(t, "confirmation_mismatch", apiCode(t, err))
	count := f.disp.count()
	v, err := f.svc.PruneVolumes(f.admin, f.server.ID, "Agent 1")
	require.NoError(t, err)
	require.Equal(t, "volumes", v.Mode)
	require.Equal(t, count+1, f.disp.count())
	vp := f.disp.last(t)
	require.Equal(t, agentprotocol.CleanupModeVolumes, vp.Mode)
	require.False(t, vp.RemoveStoppedUnmanaged, "the volume action does nothing else")
	f.finish(f.lastRun(), agentprotocol.CleanupResult{})

	// A volume run never counts as the day's cleanup, and the schedule never sends one.
	// (Saving the settings above started the schedule from "now": move that back.)
	_, err = f.pool.Exec(bg, "UPDATE server_cleanup_settings SET schedule_changed_at = now() - interval '10 days' WHERE server_id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, agentprotocol.CleanupModeStandard, f.disp.last(t).Mode)

	// Offline and revoked servers.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.RunNow(f.admin, f.server.ID)
	require.Equal(t, "server_offline", apiCode(t, err))
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.RunNow(f.admin, f.server.ID)
	require.Equal(t, "server_revoked", apiCode(t, err))

	// Another team's server is not found.
	ws2, err := f.q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	u2, err := f.q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws2.ID, Email: "b@example.com", Name: "B", PasswordHash: "x"})
	require.NoError(t, err)
	_, err = f.svc.RunNow(principal(ws2.ID, u2.ID, auth.RoleOwner, policy.AllScopes), f.server.ID)
	require.Equal(t, "not_found", apiCode(t, err))
}

func TestSettingsDefaultsAndView(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.Get(f.viewer, f.server.ID)
	require.NoError(t, err)
	require.True(t, v.Enabled, "on by default")
	require.False(t, v.RemoveStoppedUnmanaged, "off by default")
	require.Equal(t, "Daily at 04:00 UTC", v.Schedule)
	require.NotNil(t, v.NextRunAt)
	require.True(t, v.NextRunAt.After(time.Now()))
	require.Equal(t, 72, v.UnusedImageMinAgeHours)
	require.Equal(t, 7, v.BuildCacheMinAgeDays)
	require.Equal(t, DefaultSchedule, v.Cron)
	require.Nil(t, v.DiskTriggerPercent, "follows the disk alert threshold by default")
	require.True(t, v.AgentSupported)
	require.Empty(t, v.Runs)

	// A viewer reads but cannot change.
	_, err = f.svc.Update(f.viewer, f.server.ID, Settings{Enabled: false})
	require.Equal(t, "forbidden", apiCode(t, err))

	v, err = f.svc.Update(f.admin, f.server.ID, Settings{Enabled: false, RemoveStoppedUnmanaged: true})
	require.NoError(t, err)
	require.False(t, v.Enabled)
	require.True(t, v.RemoveStoppedUnmanaged)
	require.Nil(t, v.NextRunAt)

	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, ActionSettings, f.server.ID.String()).Scan(&n))
	require.Equal(t, 1, n)
}

func TestResultRecordsReportAuditsAndNotifies(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Sweep(bg))
	run := f.lastRun()
	rep := agentprotocol.CleanupResult{
		RunID: run.ID.String(), Mode: "standard", Trigger: "scheduled", ReclaimedBytes: 3 << 30, Items: 4,
		Steps: []agentprotocol.CleanupStep{
			{Name: agentprotocol.CleanupStepDangling, Items: 3, ReclaimedBytes: 1 << 30, Removed: []string{"a", "b", "c"}},
			{Name: agentprotocol.CleanupStepUnusedImages, Items: 1, ReclaimedBytes: 2 << 30, Protected: 6, Removed: []string{"old:1"}},
		},
	}
	f.finish(run, rep)
	// A duplicate result changes nothing and does not notify again.
	f.finish(run, agentprotocol.CleanupResult{ReclaimedBytes: 1})

	got := f.lastRun()
	require.Equal(t, "succeeded", got.Status)
	require.Equal(t, int64(3<<30), got.ReclaimedBytes)
	require.EqualValues(t, 4, got.Items)
	require.NotNil(t, got.FinishedAt)

	v, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, v.Running)
	require.Len(t, v.Runs, 1)
	rv := v.Runs[0]
	require.Equal(t, "succeeded", rv.Status)
	require.Len(t, rv.Steps, 2)
	require.Equal(t, 6, rv.Steps[1].Protected)
	require.Equal(t, []string{"a", "b", "c"}, rv.Steps[0].Removed)

	var actor string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT actor_type FROM audit_events WHERE action = $1 AND resource_id = $2`, ActionRun, f.server.ID.String()).Scan(&actor))
	require.Equal(t, "system", actor)

	evs := f.rec.all()
	require.Len(t, evs, 1, "more than 1 GB reclaimed notifies once")
	require.Equal(t, notifications.TypeServerCleanup, evs[0].Type)
	require.Equal(t, notifications.SeverityInfo, evs[0].Severity)
	require.Equal(t, f.server.ID, *evs[0].ServerID)
	require.Contains(t, evs[0].Title, "3.0 GiB")
}

func TestSmallSuccessIsQuietAndFailuresNotify(t *testing.T) {
	f := newFixture(t)

	// 300 MB reclaimed: no notification.
	require.NoError(t, f.svc.Sweep(bg))
	f.finish(f.lastRun(), agentprotocol.CleanupResult{ReclaimedBytes: 300 << 20, Items: 2})
	require.Empty(t, f.rec.all())

	// A failed run reports and notifies, keeping what was reclaimed.
	run, err := f.svc.RunNow(f.admin, f.server.ID)
	require.NoError(t, err)
	r := f.lastRun()
	require.Equal(t, run.ID, r.ID)
	raw, _ := json.Marshal(agentprotocol.CleanupResult{
		ReclaimedBytes: 50 << 20, Items: 1, Message: "images_unused: daemon exploded",
		Steps: []agentprotocol.CleanupStep{{Name: agentprotocol.CleanupStepUnusedImages, Error: "daemon exploded"}},
	})
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, *r.CommandID, agentprotocol.ResultFrame{
		CommandID: r.CommandID.String(), Outcome: agentprotocol.OutcomeFailed, Error: "images_unused: daemon exploded", Data: raw,
	}))
	got := f.lastRun()
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "images_unused: daemon exploded", got.Error)
	require.Equal(t, int64(50<<20), got.ReclaimedBytes)
	evs := f.rec.all()
	require.Len(t, evs, 1)
	require.Equal(t, notifications.SeverityError, evs[0].Severity)
	require.Contains(t, evs[0].Body, "daemon exploded")
}

func TestUndeliveredCommandFailsTheRun(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Sweep(bg))
	run := f.lastRun()
	require.NoError(t, f.svc.HandleUndelivered(bg, *run.CommandID, "Not delivered: server offline. No changes were made."))
	got := f.lastRun()
	require.Equal(t, "failed", got.Status)
	require.Contains(t, got.Error, "Not delivered")
	// Commands of other kinds and unknown commands are not ours.
	require.NoError(t, f.svc.HandleUndelivered(bg, uuid.New(), "x"))
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, uuid.New(), agentprotocol.ResultFrame{}))
}

func TestHistoryIsPruned(t *testing.T) {
	f := newFixture(t)
	for range HistoryKept + 5 {
		_, err := f.q.InsertServerCleanupRun(bg, db.InsertServerCleanupRunParams{ServerID: f.server.ID, WorkspaceID: f.ws, Mode: "standard", Trigger: "manual"})
		require.NoError(t, err)
	}
	require.NoError(t, f.q.PruneServerCleanupRuns(bg, db.PruneServerCleanupRunsParams{ServerID: f.server.ID, Keep: HistoryKept}))
	var n int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM server_cleanup_runs WHERE server_id = $1`, f.server.ID).Scan(&n))
	require.Equal(t, HistoryKept, n)
}

func TestHandles(t *testing.T) {
	require.True(t, Handles(agentprotocol.KindDockerCleanup))
	require.False(t, Handles(agentprotocol.KindDeploy))
}

func TestCustomLimitsReachTheCommandAndTheView(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.Update(f.admin, f.server.ID, Settings{
		Enabled: true, Cron: "0 */6 * * *", UnusedImageMinAgeHours: 24, BuildCacheMinAgeDays: 2, DiskTriggerPercent: 65,
	})
	require.NoError(t, err)
	require.Equal(t, "0 */6 * * *", v.Cron)
	require.Equal(t, "Cron 0 */6 * * * (UTC)", v.Schedule)
	require.Equal(t, 24, v.UnusedImageMinAgeHours)
	require.Equal(t, 2, v.BuildCacheMinAgeDays)
	require.NotNil(t, v.DiskTriggerPercent)
	require.Equal(t, 65, *v.DiskTriggerPercent)
	require.NotNil(t, v.NextRunAt)
	require.Zero(t, v.NextRunAt.Minute())
	require.Zero(t, v.NextRunAt.Hour()%6)

	// A manual run carries the limits.
	_, err = f.svc.RunNow(f.admin, f.server.ID)
	require.NoError(t, err)
	p := f.disp.last(t)
	require.Equal(t, 24, p.UnusedImageMinAgeHours)
	require.Equal(t, 48, p.BuildCacheMinAgeHours)

	// A bad schedule is refused and nothing changes.
	_, err = f.svc.Update(f.admin, f.server.ID, Settings{Enabled: true, Cron: "* * * * *"})
	require.Equal(t, "validation_failed", apiCode(t, err))
	got, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "0 */6 * * *", got.Cron)

	// A PUT that omits everything restores the defaults.
	v, err = f.svc.Update(f.admin, f.server.ID, Settings{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, DefaultSchedule, v.Cron)
	require.Equal(t, 72, v.UnusedImageMinAgeHours)
	require.Equal(t, 7, v.BuildCacheMinAgeDays)
	require.Nil(t, v.DiskTriggerPercent)
}

func TestSweepFollowsTheCustomScheduleAndDiskTrigger(t *testing.T) {
	f := newFixture(t)
	// Hourly is too dense; every six hours is allowed. The schedule is "changed"
	// ten days ago so the first firing is due now.
	_, err := f.svc.Update(f.admin, f.server.ID, Settings{Enabled: true, Cron: "0 */6 * * *", DiskTriggerPercent: 60})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE server_cleanup_settings SET schedule_changed_at = now() - interval '10 days' WHERE server_id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())
	require.Equal(t, agentprotocol.CleanupTriggerScheduled, f.disp.last(t).Trigger)
	f.finish(f.lastRun(), agentprotocol.CleanupResult{})

	// Just ran: nothing is due and the disk is cold.
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 1, f.disp.count())

	// Seven hours later a six-hourly slot has passed (the run moves back in
	// time instead of the clock).
	_, err = f.pool.Exec(bg, "UPDATE server_cleanup_runs SET created_at = now() - interval '7 hours'")
	require.NoError(t, err)
	next := cronexpr.MustParse("0 */6 * * *").Next(time.Now().Add(-7 * time.Hour))
	require.True(t, next.Before(time.Now()), "a six-hourly slot passed within seven hours")
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count(), "the schedule fires again")
	f.finish(f.lastRun(), agentprotocol.CleanupResult{})

	// A custom trigger of 60% ignores the alert flag: 59% is cold, 61% is hot.
	sample := func(used int64) {
		_, err := f.pool.Exec(bg, `INSERT INTO metric_samples (server_id, service_id, ts, cpu_percent, mem_bytes, mem_limit_bytes, disk_used_bytes, disk_total_bytes)
			VALUES ($1, NULL, now(), 1, 1, 1, $2, 100)`, f.server.ID, used)
		require.NoError(t, err)
	}
	_, err = f.pool.Exec(bg, "UPDATE server_cleanup_runs SET created_at = now() - interval '7 hours'")
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE server_cleanup_settings SET schedule_changed_at = now() WHERE server_id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET disk_alert_active = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	sample(59)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 2, f.disp.count(), "59% is under the custom trigger even though the alert flag is on")
	sample(61)
	require.NoError(t, f.svc.Sweep(bg))
	require.Equal(t, 3, f.disp.count())
	require.Equal(t, agentprotocol.CleanupTriggerEmergency, f.disp.last(t).Trigger)
}
