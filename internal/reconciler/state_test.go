package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (f *fixture) observe(svc db.Service, o agentprotocol.ObservedService) error {
	o.ServiceID = svc.ID.String()
	return f.r.HandleObserved(context.Background(), f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{o}})
}

func (f *fixture) observed(id uuid.UUID) *services.ObservedView {
	f.t.Helper()
	row, err := f.q.GetServiceScopeInternal(context.Background(), id)
	require.NoError(f.t, err)
	return services.DecodeObserved(row.Service.Observed)
}

type fakeSources struct {
	geo        *agentprotocol.GeoConfig
	logs       *agentprotocol.LogsConfig
	uptime     *agentprotocol.UptimeSettings
	geoSecrets map[string]string
	logSecrets map[string]string
	err        error
}

func (s fakeSources) GeoConfigFor(context.Context, uuid.UUID) (*agentprotocol.GeoConfig, map[string]string, error) {
	return s.geo, s.geoSecrets, s.err
}

func (s fakeSources) LogsConfigFor(context.Context, uuid.UUID) (*agentprotocol.LogsConfig, map[string]string, error) {
	return s.logs, s.logSecrets, s.err
}

func (s fakeSources) UptimeConfigFor(context.Context, uuid.UUID) (*agentprotocol.UptimeSettings, error) {
	return s.uptime, s.err
}

func TestDesiredStateCarriesTheOptionalSourcesAndTheirSecrets(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.exec(`UPDATE servers SET acme_email = 'ops@example.com' WHERE id = $1`, f.server.ID)

	plain, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "ops@example.com", plain.ACMEEmail)
	require.Nil(t, plain.Geo)
	require.Nil(t, plain.Logs)
	require.Nil(t, plain.Uptime)

	src := fakeSources{
		geo: &agentprotocol.GeoConfig{}, logs: &agentprotocol.LogsConfig{}, uptime: &agentprotocol.UptimeSettings{},
		geoSecrets: map[string]string{"geo.licence": "g-secret"}, logSecrets: map[string]string{"drain.token": "d-secret"},
	}
	f.r.SetGeo(src)
	f.r.SetLogs(src)
	f.r.SetUptime(src)
	out, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.Same(t, src.geo, out.Geo)
	require.Same(t, src.logs, out.Logs)
	require.Same(t, src.uptime, out.Uptime)
	require.Equal(t, "g-secret", out.Secrets["geo.licence"])
	require.Equal(t, "d-secret", out.Secrets["drain.token"], "secrets of every source ride in the same frame")

	// A source that fails fails the whole frame: the agent keeps what it has.
	boom := errors.New("source down")
	for name, set := range map[string]func(){
		"geo":    func() { f.r.SetGeo(fakeSources{err: boom}); f.r.SetLogs(nil); f.r.SetUptime(nil) },
		"logs":   func() { f.r.SetGeo(nil); f.r.SetLogs(fakeSources{err: boom}); f.r.SetUptime(nil) },
		"uptime": func() { f.r.SetGeo(nil); f.r.SetLogs(nil); f.r.SetUptime(fakeSources{err: boom}) },
	} {
		set()
		_, err := f.r.DesiredState(bg, f.server.ID)
		require.ErrorIs(t, err, boom, name)
	}
}

func TestDesiredStateOfAnUnknownServerFails(t *testing.T) {
	f := newFixture(t)
	_, err := f.r.DesiredState(context.Background(), uuid.New())
	require.ErrorContains(t, err, "load server")
}

func TestDesiredStateKeepsRouteServicesOutOfTheContainerList(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	web := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	edge := f.deployed("edge", services.KindRoute, services.SourceExternal, services.Config{
		Route: &services.RouteConfig{Protocol: "http", Upstreams: []services.RouteUpstream{{Address: "192.168.1.20:8080"}}},
	})
	_, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: edge.ID, Hostname: "nas.example.com", TargetPort: 80})
	require.NoError(t, err)

	out, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	require.Len(t, out.Services, 1, "a route has no container")
	require.Equal(t, web.ID.String(), out.Services[0].ServiceID)
	require.Len(t, out.ExternalRoutes, 1)
	require.Equal(t, []string{"nas.example.com"}, out.ExternalRoutes[0].Hosts)
	require.Equal(t, []string{"192.168.1.20:8080"}, out.ExternalRoutes[0].Upstreams)
}

func TestNewFallsBackToTheDefaultLogger(t *testing.T) {
	require.NotNil(t, New(nil, nil, nil, nil, nil).logger)
}

// ---- observed reports ----

func TestObservedReportsThatCannotBeAttributedAreDropped(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	undeployed, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "idle", Slug: "idle", Kind: services.KindApp, SourceType: services.SourceImage, Config: []byte(`{}`), ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	running := agentprotocol.ObservedService{State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy}

	require.NoError(t, f.r.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: "not-a-uuid", State: "running"}}}))
	require.NoError(t, f.observe(db.Service{ID: uuid.New()}, running), "a service that does not exist")
	require.NoError(t, f.observe(undeployed, running), "a service without a current deployment")

	elsewhere := running
	elsewhere.DeploymentID = uuid.NewString()
	require.NoError(t, f.observe(app, elsewhere), "a container of another deployment")

	otherServer, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.workspace(), Name: "agent2"})
	require.NoError(t, err)
	require.NoError(t, f.r.HandleObserved(bg, otherServer.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: app.ID.String(), State: "running"}}}),
		"a server that does not run the service cannot speak for it")

	require.Nil(t, f.observed(app.ID), "none of those was recorded")
	require.Nil(t, f.observed(undeployed.ID))
}

func (f *fixture) workspace() uuid.UUID {
	f.t.Helper()
	var ws uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT workspace_id FROM servers WHERE id = $1`, f.server.ID).Scan(&ws))
	return ws
}

func TestObservedStateStoresStartTimesAndPublishesOnlyChanges(t *testing.T) {
	f := newFixture(t)
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	feed := f.r.bus.Subscribe(context.Background())
	started := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	report := agentprotocol.ObservedService{
		State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, ContainerID: "c1", ImageID: "sha256:abc", StartedAt: started,
		ReplicasReady: 1, ReplicasDesired: 1,
		Replicas: []agentprotocol.ObservedReplica{{Index: 0, ContainerID: "c1", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, StartedAt: started, RestartCount: 2}},
	}
	require.NoError(t, f.observe(app, report))
	got := f.observed(app.ID)
	require.NotNil(t, got)
	require.Equal(t, "c1", got.ContainerID)
	require.Equal(t, "sha256:abc", got.ImageID)
	require.True(t, got.StartedAt.Equal(started))
	require.Len(t, got.Replicas, 1)
	require.True(t, got.Replicas[0].StartedAt.Equal(started))
	require.Equal(t, 2, got.Replicas[0].RestartCount)
	require.Equal(t, app.CurrentDeploymentID.String(), got.DeploymentID)
	require.Equal(t, services.EventServiceUpdated, (<-feed).Type)

	// The same report again changes nothing a viewer would see: no event.
	require.NoError(t, f.observe(app, report))
	select {
	case ev := <-feed:
		t.Fatalf("unexpected event %v", ev)
	default:
	}

	// A different replica count or state is announced.
	report.ReplicasReady = 0
	require.NoError(t, f.observe(app, report))
	require.Equal(t, services.EventServiceUpdated, (<-feed).Type)
}

func TestObservedReportsKeepGoingAfterAFailureAndReturnTheFirst(t *testing.T) {
	f := newFixture(t)
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	err := f.r.HandleObserved(dead, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{
		{ServiceID: app.ID.String(), State: "running"}, {ServiceID: app.ID.String(), State: "running"},
	}})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "load service")
}

// ---- recovery ----

func (f *fixture) reconcileDeployments(svc db.Service, n int) {
	f.t.Helper()
	for range n {
		dep, err := f.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{
			ServiceID: svc.ID, ReleaseID: nil, ServerID: &f.server.ID, Trigger: "reconcile", ActorType: "system", Strategy: "recreate",
		})
		require.NoError(f.t, err)
		f.exec(`UPDATE deployments SET status = 'succeeded' WHERE id = $1`, dep.ID)
	}
}

func TestRecoveryStopsAfterTheHourlyLimitAndSaysSoOnce(t *testing.T) {
	f := newFixture(t)
	RegisterJobs(f.r.jm, f.r)
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	f.reconcileDeployments(app, MaxRedeploysPerHour)
	missing := agentprotocol.ObservedService{State: agentprotocol.StateMissing, Health: agentprotocol.HealthUnknown}

	require.NoError(t, f.observe(app, missing))
	require.Equal(t, LimitMessage, f.observed(app.ID).Error, "the node says why it stays down")
	require.Zero(t, f.count(`SELECT count(*) FROM river_job WHERE kind = 'reconcile_redeploy'`), "no redeploy is queued past the limit")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'reconcile.limit_reached' AND resource_id = $1`, app.ID.String()))

	// The next report (every 15 seconds) does not audit again.
	require.NoError(t, f.observe(app, missing))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = 'reconcile.limit_reached' AND resource_id = $1`, app.ID.String()))
	require.Equal(t, LimitMessage, f.observed(app.ID).Error)
}

func TestRecoveryQueuesOneRedeployPerMissingService(t *testing.T) {
	f := newFixture(t)
	RegisterJobs(f.r.jm, f.r)
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	missing := agentprotocol.ObservedService{State: agentprotocol.StateMissing, Health: agentprotocol.HealthUnknown}

	require.NoError(t, f.observe(app, missing))
	require.NoError(t, f.observe(app, missing))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM river_job WHERE kind = 'reconcile_redeploy'`), "reports every 15 seconds do not pile up jobs")
	require.Empty(t, f.observed(app.ID).Error)
}

func TestRedeployJob(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	require.Equal(t, "reconcile_redeploy", RedeployArgs{}.Kind())
	work := func(id uuid.UUID) error {
		return (&redeployWorker{r: f.r}).Work(bg, &river.Job[RedeployArgs]{Args: RedeployArgs{ServiceID: id}})
	}
	started := func(svc uuid.UUID) int {
		return f.count(`SELECT count(*) FROM deployments WHERE service_id = $1 AND trigger = 'reconcile'`, svc)
	}

	require.NoError(t, work(uuid.New()), "a deleted service has nothing to redeploy")

	idle, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "idle", Slug: "idle", Kind: services.KindApp, SourceType: services.SourceImage, Config: []byte(`{}`), ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	require.NoError(t, work(idle.ID), "never deployed")
	require.Zero(t, started(idle.ID))

	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	// A deployment already running replaces the container.
	running, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: app.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, work(app.ID))
	require.Zero(t, started(app.ID))
	f.exec(`UPDATE deployments SET status = 'succeeded' WHERE id = $1`, running.ID)

	// Past the hourly limit nothing is started.
	f.reconcileDeployments(app, MaxRedeploysPerHour)
	require.NoError(t, work(app.ID))
	require.Equal(t, MaxRedeploysPerHour, started(app.ID))
}
