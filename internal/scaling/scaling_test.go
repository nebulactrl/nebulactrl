package scaling_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const canary = "sekret-canary-12345"

// fakeDispatcher records the commands the domain enqueues.
type fakeDispatcher struct{ cmds []agents.NewCommand }

func (f *fakeDispatcher) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	f.cmds = append(f.cmds, c)
	return uuid.New(), nil
}

func (f *fakeDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context
	box     *secretbox.Box
	disp    *fakeDispatcher
	svcs    *services.Service
	scal    *scaling.Service
	env     db.Environment
	project db.Project
	server  db.Server
	ws      db.Workspace
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
	disp := &fakeDispatcher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	svcs := services.NewService(pool, bus, box, disp, logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "dev@example.com", Scopes: policy.AllScopes})
	return &fixture{
		t: t, pool: pool, q: q, ctx: ctx, box: box, disp: disp, svcs: svcs, env: env, project: prj, server: srv, ws: ws,
		scal: scaling.NewService(pool, bus, disp, svcs, logger),
	}
}

// service inserts a service on the fixture server.
func (f *fixture) service(slug, kind string, cfg services.Config) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *fixture) web(slug string) db.Service {
	return f.service(slug, services.KindApp, services.Config{Image: "traefik/whoami:latest", Port: 80})
}

// deploy gives sv a succeeded deployment with a persisted deploy command whose
// release carries the canary secret in its encrypted environment.
func (f *fixture) deploy(sv db.Service) uuid.UUID {
	f.t.Helper()
	bg := context.Background()
	envJSON, err := json.Marshal(services.EnvSnapshot{Vars: map[string]string{"API_KEY": canary}, Secret: []string{"API_KEY"}})
	require.NoError(f.t, err)
	relID := uuid.New()
	enc, err := f.box.Seal(secretbox.ReleasesEnv, relID.String(), envJSON)
	require.NoError(f.t, err)
	cfg := services.DecodeConfig(sv.Config)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: relID, ServiceID: sv.ID, Source: src, Config: snap, EnvEnc: enc, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	payload, err := json.Marshal(agentprotocol.DeployPayload{
		DeploymentID: dep.ID.String(), ReleaseID: rel.ID.String(), ServiceID: sv.ID.String(), ServiceSlug: sv.Slug,
		Strategy: "rolling", Image: &agentprotocol.ImageSource{Ref: cfg.Image}, Port: cfg.Port, Replicas: 1,
		EnvKeys: []string{"API_KEY"}, SecretEnvKeys: []string{"API_KEY"}, Hosts: []string{sv.Slug + ".localhost"},
	})
	require.NoError(f.t, err)
	_, err = f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: uuid.New(), ServerID: f.server.ID, DeploymentID: &dep.ID, Kind: agentprotocol.KindDeploy, Payload: payload,
		IdempotencyKey: "deploy:" + dep.ID.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	return dep.ID
}

func (f *fixture) reload(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *fixture) enableAutoscaling(id uuid.UUID, a services.Autoscaling) {
	f.t.Helper()
	raw, _ := json.Marshal(a)
	_, err := f.q.SetServiceAutoscaling(context.Background(), db.SetServiceAutoscalingParams{ID: id, Autoscaling: raw})
	require.NoError(f.t, err)
}

func (f *fixture) setReplicas(id uuid.UUID, n int) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET replicas = $2 WHERE id = $1", id, n)
	require.NoError(f.t, err)
}

// sample inserts one replica sample of the service at ts.
func (f *fixture) sample(id uuid.UUID, ts time.Time, replica int, cpu float64, mem int64) {
	f.t.Helper()
	require.NoError(f.t, f.q.InsertServiceMetricSample(context.Background(), db.InsertServiceMetricSampleParams{
		ServerID: &f.server.ID, ServiceID: id, Ts: ts, Replica: int32(replica), CpuPercent: cpu, MemBytes: mem, MemLimitBytes: 1 << 30,
	}))
}

// load fills the last d with samples every 10 s for each of n replicas.
func (f *fixture) load(id uuid.UUID, n int, cpu float64, d time.Duration) {
	f.t.Helper()
	now := time.Now()
	for ts := now.Add(-d); ts.Before(now); ts = ts.Add(10 * time.Second) {
		for r := range n {
			f.sample(id, ts, r, cpu, 200<<20)
		}
	}
}

func (f *fixture) hostSample(memUsed, memTotal int64) {
	f.t.Helper()
	require.NoError(f.t, f.q.InsertServerMetricSample(context.Background(), db.InsertServerMetricSampleParams{
		ServerID: f.server.ID, CpuPercent: 10, MemBytes: memUsed, MemLimitBytes: memTotal, DiskUsedBytes: 1, DiskTotalBytes: 100,
	}))
}

func (f *fixture) scaleCommands() []agents.NewCommand {
	var out []agents.NewCommand
	for _, c := range f.disp.cmds {
		if c.Kind == agentprotocol.KindScaleService {
			out = append(out, c)
		}
	}
	return out
}

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func TestIneligibleServicesGet422(t *testing.T) {
	f := newFixture(t)
	db1 := f.service("pg", services.KindDatabase, services.Config{Image: "postgres:17", Port: 5432})
	_, err := f.scal.Set(f.ctx, db1.ID, scaling.SetInput{Replicas: new(3)})
	requireAPIError(t, err, 422, "replicas_require_stateless")

	withVol := f.web("data")
	_, err = f.q.InsertVolume(context.Background(), db.InsertVolumeParams{ServiceID: withVol.ID, Name: "d", MountPath: "/data"})
	require.NoError(t, err)
	_, err = f.scal.Set(f.ctx, withVol.ID, scaling.SetInput{Replicas: new(3)})
	requireAPIError(t, err, 422, "replicas_require_stateless")

	noPort := f.service("worker", services.KindWorker, services.Config{Image: "busybox"})
	_, err = f.scal.Set(f.ctx, noPort.ID, scaling.SetInput{Replicas: new(3)})
	requireAPIError(t, err, 422, "replicas_require_stateless")

	// Autoscaling cannot be switched on either.
	_, err = f.scal.Set(f.ctx, db1.ID, scaling.SetInput{Autoscaling: &scaling.AutoscalingPatch{Enabled: new(true)}})
	requireAPIError(t, err, 422, "replicas_require_stateless")

	v, err := f.svcs.Get(f.ctx, db1.ID)
	require.NoError(t, err)
	require.False(t, v.Scaling.Eligible)
	require.NotNil(t, v.Scaling.IneligibleReason)
	require.Empty(t, f.scaleCommands())
}

func TestScalingValidation(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	for name, in := range map[string]scaling.SetInput{
		"empty":         {},
		"replicas 0":    {Replicas: new(0)},
		"replicas 11":   {Replicas: new(11)},
		"min above max": {Autoscaling: &scaling.AutoscalingPatch{Min: new(4), Max: new(3)}},
		"max 11":        {Autoscaling: &scaling.AutoscalingPatch{Max: new(11)}},
		"target 10":     {Autoscaling: &scaling.AutoscalingPatch{TargetCPUPercent: new(10)}},
		"target 96":     {Autoscaling: &scaling.AutoscalingPatch{TargetCPUPercent: new(96)}},
	} {
		_, err := f.scal.Set(f.ctx, sv.ID, in)
		requireAPIError(t, err, 422, "validation_failed")
		_ = name
	}
}

func TestManualScaleWhileAutoscalingEnabledGets409(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	v, err := f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Autoscaling: &scaling.AutoscalingPatch{Enabled: new(true), Max: new(5)}})
	require.NoError(t, err)
	require.True(t, v.Scaling.Autoscaling.Enabled)
	require.Equal(t, 5, v.Scaling.Autoscaling.Max)
	require.Equal(t, 70, v.Scaling.Autoscaling.TargetCPUPercent, "defaults stay")

	_, err = f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 409, "autoscaling_enabled")

	// Turning autoscaling off in the same call allows the manual count.
	v, err = f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2), Autoscaling: &scaling.AutoscalingPatch{Enabled: new(false)}})
	require.NoError(t, err)
	require.Equal(t, 2, v.Scaling.Replicas)
	require.False(t, v.Scaling.Autoscaling.Enabled)
}

func TestScaleOfUndeployedServiceOnlyStoresTheSetting(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	v, err := f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(3)})
	require.NoError(t, err)
	require.Equal(t, 3, v.Scaling.Replicas)
	require.Empty(t, f.disp.cmds, "nothing runs, nothing to scale")
	require.Equal(t, sv.Generation, v.Generation, "replicas are never a staged change")
}

func TestScaleEnqueuesScaleServiceWithTheCurrentDeployment(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	depID := f.deploy(sv)

	v, err := f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(4)})
	require.NoError(t, err)
	require.Equal(t, 4, v.Scaling.Replicas)
	require.NotNil(t, v.Scaling.LastScaleEvent)
	require.Equal(t, 1, v.Scaling.LastScaleEvent.From)
	require.Equal(t, 4, v.Scaling.LastScaleEvent.To)
	require.Equal(t, "dev@example.com", v.Scaling.LastScaleEvent.Actor)

	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	c := cmds[0]
	require.Equal(t, f.server.ID, c.ServerID)
	require.Nil(t, c.DeploymentID, "a scale is not a deployment")
	require.NotContains(t, string(c.Payload), canary, "the payload never holds secret values")

	var p agentprotocol.ScaleServicePayload
	require.NoError(t, json.Unmarshal(c.Payload, &p))
	require.Equal(t, sv.ID.String(), p.ServiceID)
	require.Equal(t, depID.String(), p.DeploymentID)
	require.Equal(t, 4, p.Replicas)
	stored := f.reload(sv.ID).LastScaleAt
	require.NotNil(t, stored)
	require.True(t, stored.Equal(p.ScaledAt), "the command carries the stamp of the stored count: %v vs %v", p.ScaledAt, stored)
	require.Equal(t, 4, p.Deploy.Replicas)
	require.Equal(t, depID.String(), p.Deploy.DeploymentID)
	require.Equal(t, "traefik/whoami:latest", p.Deploy.Image.Ref)
	require.Equal(t, []string{"API_KEY"}, p.Deploy.EnvKeys)

	// The send-time secrets of a scale_service command match a deploy's.
	dep := deployments.NewService(deployments.Deps{Pool: f.pool, Box: f.box})
	sec, err := dep.SecretsForCommand(f.ctx, uuid.Nil, agentprotocol.KindScaleService, c.Payload)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"env:API_KEY": canary}, sec)

	var audited []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = 'service.scale' AND resource_id = $1", sv.ID.String()).Scan(&audited))
	var md map[string]any
	require.NoError(t, json.Unmarshal(audited, &md))
	require.EqualValues(t, 1, md["from"])
	require.EqualValues(t, 4, md["to"])

	// Deploys keep the current count.
	require.Equal(t, 4, services.EffectiveReplicas(sv.Kind, services.DecodeConfig(sv.Config), 0, int(f.reload(sv.ID).Replicas)))
}

func TestScaleRejectedDuringDeploymentAndWhileStopped(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	_, err := f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{
		ServiceID: sv.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(t, err)
	_, err = f.scal.Set(f.ctx, sv.ID, scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 409, "deployment_in_progress")

	g := newFixture(t)
	st := g.web("web")
	g.deploy(st)
	_, err = g.pool.Exec(context.Background(), "UPDATE services SET desired_state = 'stopped' WHERE id = $1", st.ID)
	require.NoError(t, err)
	_, err = g.scal.Set(g.ctx, st.ID, scaling.SetInput{Replicas: new(2)})
	requireAPIError(t, err, 409, "service_stopped")
}

func TestRouteUpstreamsListEveryReplica(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	depID := f.deploy(sv)
	_, err := f.q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: sv.ID, Hostname: "web.localhost", TargetPort: 80})
	require.NoError(t, err)
	f.setReplicas(sv.ID, 3)

	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	d8 := agentprotocol.Short(depID.String())
	require.Equal(t, []string{
		"nc-web-" + d8 + "-0:80", "nc-web-" + d8 + "-1:80", "nc-web-" + d8 + "-2:80",
	}, routes[0].Upstreams)
	require.Equal(t, []string{"web.localhost"}, routes[0].Hosts)
	require.Equal(t, "/", routes[0].HealthPath, "the default http health check path feeds the proxy health check")
}

func TestClampReplicasAfterDeployOfIneligibleService(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.setReplicas(sv.ID, 3)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	sc, err := services.LoadScopeInternal(context.Background(), f.q, sv.ID)
	require.NoError(t, err)

	err = db.InTx(context.Background(), f.pool, func(tx pgx.Tx) error {
		changed, err := services.ClampReplicas(context.Background(), tx, sc, sc.Config(), 1) // a volume was attached
		require.True(t, changed)
		return err
	})
	require.NoError(t, err)
	got := f.reload(sv.ID)
	require.EqualValues(t, 1, got.Replicas)
	require.False(t, services.DecodeAutoscaling(got.Autoscaling).Enabled)
}

func TestServiceMetricsAggregateReplicas(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	ts := time.Now().Add(-20 * time.Second)
	f.sample(sv.ID, ts, 0, 10, 100)
	f.sample(sv.ID, ts, 1, 30, 300)

	res, err := f.svcs.Metrics(f.ctx, sv.ID, "5m", "")
	require.NoError(t, err)
	require.NotEmpty(t, res.Points)
	last := res.Points[len(res.Points)-1]
	require.InDelta(t, 40, last.CPUPercent, 0.001, "CPU is the sum over the replicas")
	require.InDelta(t, 20, last.CPUPerReplica, 0.001)
	require.EqualValues(t, 400, last.MemBytes)
	require.Equal(t, 2, last.ReplicaCount)
	require.Equal(t, []services.ReplicaMetric{{Index: 0, CPUPercent: 10, MemBytes: 100}, {Index: 1, CPUPercent: 30, MemBytes: 300}}, last.Replicas)
}

func TestAutoscalerScalesOutAfterTwoMinutesAndRespectsCooldown(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})

	// 90 seconds above target is not enough.
	short := f.web("short")
	f.deploy(short)
	f.enableAutoscaling(short.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(short.ID, 1, 90, 85*time.Second)

	f.load(sv.ID, 1, 90, 3*time.Minute)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))

	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)
	require.EqualValues(t, 1, f.reload(short.ID).Replicas)
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	var p agentprotocol.ScaleServicePayload
	require.NoError(t, json.Unmarshal(cmds[0].Payload, &p))
	require.Equal(t, sv.ID.String(), p.ServiceID)
	require.Equal(t, 2, p.Replicas)

	var actorType, actorLabel string
	var md []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT actor_type, actor_label, metadata FROM audit_events WHERE action = 'service.autoscale' AND resource_id = $1", sv.ID.String()).
		Scan(&actorType, &actorLabel, &md))
	require.Equal(t, "system", actorType)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(md, &meta))
	require.Equal(t, "CPU 90% > 70% for 2m", meta["reason"])
	require.EqualValues(t, 1, meta["from"])
	require.EqualValues(t, 2, meta["to"])

	view, err := f.svcs.Get(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Equal(t, services.ActorAutoscaler, view.Scaling.LastScaleEvent.Actor)
	require.Equal(t, "CPU 90% > 70% for 2m", view.Scaling.LastScaleEvent.Reason)

	// Still hot, but the 3 minute cooldown after a scale-out holds it at 2.
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 2, f.reload(sv.ID).Replicas)
	require.Len(t, f.scaleCommands(), 1)

	// Once the cooldown is over it scales out again, up to max.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET last_scale_at = now() - interval '4 minutes' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 3, f.reload(sv.ID).Replicas)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET last_scale_at = now() - interval '4 minutes' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 3, f.reload(sv.ID).Replicas, "never above max")
}

func TestAutoscalerScalesInAfterTenMinutesBelowThreshold(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.setReplicas(sv.ID, 2)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})

	steady := f.web("steady")
	f.deploy(steady)
	f.setReplicas(steady.ID, 2)
	f.enableAutoscaling(steady.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(steady.ID, 2, 20, 6*time.Minute) // only 6 minutes of calm

	f.load(sv.ID, 2, 20, 11*time.Minute)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))

	require.EqualValues(t, 1, f.reload(sv.ID).Replicas)
	require.EqualValues(t, 2, f.reload(steady.ID).Replicas)
	cmds := f.scaleCommands()
	require.Len(t, cmds, 1)
	var p agentprotocol.ScaleServicePayload
	require.NoError(t, json.Unmarshal(cmds[0].Payload, &p))
	require.Equal(t, 1, p.Replicas)
	var reason string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		"SELECT metadata->>'reason' FROM audit_events WHERE action = 'service.autoscale' AND resource_id = $1", sv.ID.String()).Scan(&reason))
	require.Equal(t, "CPU 20% < 35% for 10m", reason)

	// At min it stays.
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET last_scale_at = now() - interval '11 minutes' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	f.load(sv.ID, 1, 20, 11*time.Minute)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas)
}

func TestAutoscalerScaleOutBlockedByCapacityAndCleared(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(sv.ID, 1, 95, 3*time.Minute) // each replica uses 200 MiB

	const total = 8 << 30
	f.hostSample(total-429496730, total) // 0.4 GiB free: less than one replica's 200 MiB? no, more
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 2, f.reload(sv.ID).Replicas, "0.4 GiB is room for another 200 MiB replica")

	// Now a configured 1 GiB memory limit does not fit into the 0.4 GiB free.
	g := newFixture(t)
	gv := g.service("limited", services.KindApp, services.Config{Image: "traefik/whoami:latest", Port: 80, MemoryMB: 1024})
	g.deploy(gv)
	g.enableAutoscaling(gv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	g.load(gv.ID, 1, 95, 3*time.Minute)
	g.hostSample(total-429496730, total)
	require.NoError(t, g.scal.RunAutoscaler(context.Background()))

	got := g.reload(gv.ID)
	require.EqualValues(t, 1, got.Replicas, "blocked")
	require.NotNil(t, got.ScaleBlockedReason)
	require.Equal(t, "Scale-out blocked: agent1 has 0.4 GiB free", *got.ScaleBlockedReason)
	require.Empty(t, g.scaleCommands())
	view, err := g.svcs.Get(g.ctx, gv.ID)
	require.NoError(t, err)
	require.Equal(t, "Scale-out blocked: agent1 has 0.4 GiB free", *view.Scaling.BlockedReason)

	// Memory frees up: the next run scales out and the reason is gone.
	g.hostSample(total/2, total)
	require.NoError(t, g.scal.RunAutoscaler(context.Background()))
	got = g.reload(gv.ID)
	require.EqualValues(t, 2, got.Replicas)
	require.Nil(t, got.ScaleBlockedReason)

	// The reason also clears when CPU calms down while still blocked.
	h := newFixture(t)
	hv := h.web("web")
	h.deploy(hv)
	h.enableAutoscaling(hv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	_, err = h.pool.Exec(context.Background(), "UPDATE services SET scale_blocked_reason = 'Scale-out blocked: agent1 has 0.1 GiB free' WHERE id = $1", hv.ID)
	require.NoError(t, err)
	h.load(hv.ID, 1, 10, 3*time.Minute)
	require.NoError(t, h.scal.RunAutoscaler(context.Background()))
	require.Nil(t, h.reload(hv.ID).ScaleBlockedReason)
}

func TestAutoscalerScaleOutBlockedByDiskAlert(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(sv.ID, 1, 95, 3*time.Minute)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET disk_alert_active = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	got := f.reload(sv.ID)
	require.EqualValues(t, 1, got.Replicas)
	require.Equal(t, "Scale-out blocked: agent1 has an active disk alert", *got.ScaleBlockedReason)
}

func TestAutoscalerSkipsOfflineServersAndIneligibleServices(t *testing.T) {
	f := newFixture(t)
	sv := f.web("web")
	f.deploy(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(sv.ID, 1, 95, 3*time.Minute)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.EqualValues(t, 1, f.reload(sv.ID).Replicas)
	require.Empty(t, f.scaleCommands())
}

func TestBlockedScaleOutIsAnnouncedOncePerBlock(t *testing.T) {
	f := newFixture(t)
	rec := &notificationstest.Recorder{}
	f.scal.SetNotifier(rec)
	sv := f.web("web")
	f.deploy(sv)
	f.enableAutoscaling(sv.ID, services.Autoscaling{Enabled: true, Min: 1, Max: 3, TargetCPUPercent: 70})
	f.load(sv.ID, 1, 95, 3*time.Minute)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET disk_alert_active = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	got := rec.OfType(notifications.TypeScaleBlocked)
	require.Len(t, got, 1)
	require.Equal(t, f.project.ID, *got[0].ProjectID)
	require.Equal(t, sv.ID, *got[0].ServiceID)
	require.Equal(t, notifications.SeverityWarning, got[0].Severity)
	require.Equal(t, "Scale-out blocked: agent1 has an active disk alert", got[0].Body)

	// The block is still there on the next tick: not announced again.
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.Len(t, rec.OfType(notifications.TypeScaleBlocked), 1)

	// The block cleared in between (capacity came back) and returned: a new block is announced.
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET scale_blocked_reason = NULL WHERE id = $1", sv.ID)
	require.NoError(t, err)
	require.NoError(t, f.scal.RunAutoscaler(context.Background()))
	require.Len(t, rec.OfType(notifications.TypeScaleBlocked), 2)
}

func TestScaleFailureIsAnnounced(t *testing.T) {
	f := newFixture(t)
	rec := &notificationstest.Recorder{}
	f.scal.SetNotifier(rec)
	sv := f.web("web")
	require.NoError(t, f.scal.RecordScaleFailure(context.Background(), sv.ID, "no free port"))
	require.NoError(t, f.scal.RecordScaleFailure(context.Background(), sv.ID, "no free port"))
	got := rec.OfType(notifications.TypeScaleBlocked)
	require.Len(t, got, 1, "the same failure is not repeated")
	require.Equal(t, "Scale failed: no free port", got[0].Body)
}
