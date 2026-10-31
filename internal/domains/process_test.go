package domains

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// processConfig is a service with the default process (port 80) and api (port
// 4000, http health check on /ready), plus worker without a port.
func processConfig() services.Config {
	return services.Config{
		Image: "traefik/whoami:latest", Port: 80,
		Processes: []services.ProcessConfig{
			{Name: "api", StartCommand: []string{"bin/api"}, Port: 4000, Healthcheck: services.Healthcheck{Type: services.HealthHTTP, Path: "/ready"}},
			{Name: "worker", StartCommand: []string{"bin/worker"}},
		},
	}.WithDefaults()
}

// setConfig stores cfg as the live configuration of svcA.
func (f *wwwFixture) setConfig(cfg services.Config) {
	f.t.Helper()
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET config = $2::jsonb WHERE id = $1", f.svcA.ID, raw)
	require.NoError(f.t, err)
}

// deployService makes the service run a release of cfg on the fixture's server and
// returns the deployment.
func (f *wwwFixture) deployService(id uuid.UUID, cfg services.Config) db.Deployment {
	f.t.Helper()
	bg := context.Background()
	snap, _ := json.Marshal(services.Snapshot{Config: cfg, ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: id, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: id, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: id, CurrentDeploymentID: &dep.ID}))
	return dep
}

func (f *wwwFixture) routes() map[string]agentprotocol.Route {
	f.t.Helper()
	rs, err := ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(f.t, err)
	out := map[string]agentprotocol.Route{}
	for _, r := range rs {
		for _, h := range r.Hosts {
			out[h] = r
		}
	}
	return out
}

func TestProcessUpstreamsNameTheProcessContainers(t *testing.T) {
	dep := uuid.New().String()
	require.Equal(t, []string{
		agentprotocol.ProcessContainerName("site", dep, "api", false, 0) + ":4000",
		agentprotocol.ProcessContainerName("site", dep, "api", false, 1) + ":4000",
	}, ProcessUpstreams("site", dep, "api", 2, 4000))
	require.Len(t, ProcessUpstreams("site", dep, "api", 0, 4000), 1, "at least one replica")
	// The default process keeps the service's own names.
	require.Equal(t, []string{agentprotocol.ContainerName("site", dep, 0) + ":80"}, Upstreams("site", dep, 1, 80))
}

func TestCreateDomainResolvesTheProcess(t *testing.T) {
	f := newWWWFixture(t)
	f.setConfig(processConfig())

	// No process named: the default process when it has a port.
	d, err := f.svc.Create(f.ctx, f.svcA.ID, "site.example.com", 0, RuleInput{})
	require.NoError(t, err)
	require.Equal(t, 80, d.TargetPort)
	require.Empty(t, d.Process)
	require.Equal(t, "", f.rows()["site.example.com"].Component)

	// A named process: its own port, stored as the row's component.
	d, err = f.svc.CreateForComponent(f.ctx, f.svcA.ID, "api.example.com", 0, "api", RuleInput{})
	require.NoError(t, err)
	require.Equal(t, 4000, d.TargetPort)
	require.Equal(t, "api", d.Process)
	require.Equal(t, "api", f.rows()["api.example.com"].Component)
	require.EqualValues(t, 4000, f.rows()["api.example.com"].TargetPort)

	// The default process by name stores the empty component; a matching port is accepted.
	d, err = f.svc.CreateForComponent(f.ctx, f.svcA.ID, "web.example.com", 80, "web", RuleInput{})
	require.NoError(t, err)
	require.Empty(t, d.Process)
	require.Equal(t, "", f.rows()["web.example.com"].Component)

	for _, c := range []struct {
		name      string
		host      string
		port      int
		component string
		code      string
	}{
		{"unknown", "u.example.com", 0, "nope", "unknown_process"},
		{"no port", "n.example.com", 0, "worker", "process_has_no_port"},
		{"wrong port", "p.example.com", 5000, "api", "invalid_target_port"},
		{"default's port on a named process", "q.example.com", 80, "api", "invalid_target_port"},
	} {
		_, err := f.svc.CreateForComponent(f.ctx, f.svcA.ID, c.host, c.port, c.component, RuleInput{})
		requireCode(t, err, c.code)
		_ = c.name
	}

	// Several processes with a port and none named: the caller must choose.
	cfg := processConfig()
	cfg.Port = 0
	cfg.Processes = append(cfg.Processes, services.ProcessConfig{Name: "admin", StartCommand: []string{"bin/admin"}, Port: 4100})
	f.setConfig(cfg.WithDefaults())
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "x.example.com", 0, RuleInput{})
	requireCode(t, err, "process_required")
	d, err = f.svc.CreateForComponent(f.ctx, f.svcA.ID, "admin.example.com", 0, "admin", RuleInput{})
	require.NoError(t, err)
	require.Equal(t, 4100, d.TargetPort)

	// Only one process with a port: it is the default route even when it is not the default process.
	cfg.Processes = cfg.Processes[:1]
	f.setConfig(cfg.WithDefaults())
	d, err = f.svc.Create(f.ctx, f.svcA.ID, "only.example.com", 0, RuleInput{})
	require.NoError(t, err)
	require.Equal(t, "api", d.Process)
	require.Equal(t, 4000, d.TargetPort)

	// No process has a port.
	cfg.Processes = []services.ProcessConfig{{Name: "worker", StartCommand: []string{"bin/worker"}}}
	f.setConfig(cfg.WithDefaults())
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "none.example.com", 0, RuleInput{})
	requireCode(t, err, "no_http_port")
}

func TestCreateDomainProcessOnAServiceWithoutProcessesIsInvalid(t *testing.T) {
	f := newWWWFixture(t)
	_, err := f.svc.CreateForComponent(f.ctx, f.svcA.ID, "a.example.com", 0, "api", RuleInput{})
	requireCode(t, err, "invalid_process")
	_, err = f.svc.Generate(f.ctx, f.svcA.ID, "api")
	requireCode(t, err, "invalid_process")

	// Unchanged for the plain service.
	d, err := f.svc.Create(f.ctx, f.svcA.ID, "b.example.com", 0, RuleInput{})
	require.NoError(t, err)
	require.Equal(t, 80, d.TargetPort)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "c.example.com", 8080, RuleInput{})
	requireCode(t, err, "invalid_target_port")
}

func TestGenerateNamesTheProcessInTheHostname(t *testing.T) {
	f := newWWWFixture(t)
	f.setConfig(processConfig())

	def, err := f.svc.Generate(f.ctx, f.svcA.ID, "")
	require.NoError(t, err)
	require.Equal(t, "site-production-shop.203.0.113.9.sslip.io", def.Hostname, "the default process is the service's own host")
	require.Empty(t, def.Process)
	named, err := f.svc.Generate(f.ctx, f.svcA.ID, "api")
	require.NoError(t, err)
	require.Equal(t, "site-api-production-shop.203.0.113.9.sslip.io", named.Hostname)
	require.Equal(t, "api", named.Process)
	require.Equal(t, 4000, named.TargetPort)
	byName, err := f.svc.Generate(f.ctx, f.svcA.ID, "web")
	require.NoError(t, err)
	require.Empty(t, byName.Process, "the default process by name is the service's own")
	require.Equal(t, "site-production-shop-2.203.0.113.9.sslip.io", byName.Hostname)
	_, err = f.svc.Generate(f.ctx, f.svcA.ID, "worker")
	requireCode(t, err, "process_has_no_port")
}

func TestServerRoutesBuildOneRoutePerProcess(t *testing.T) {
	f := newWWWFixture(t)
	cfg := processConfig()
	f.setConfig(cfg)
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "site.example.com", 0, RuleInput{})
	require.NoError(t, err)
	_, err = f.svc.CreateForComponent(f.ctx, f.svcA.ID, "api.example.com", 0, "api", RuleInput{})
	require.NoError(t, err)
	_, err = f.svc.CreateForComponent(f.ctx, f.svcA.ID, "api2.example.com", 0, "api", RuleInput{})
	require.NoError(t, err)
	dep := f.deployService(f.svcA.ID, cfg)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET replicas = 2, maintenance_enabled = true, maintenance_title = 'Back soon' WHERE id = $1", f.svcA.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "INSERT INTO service_process_scale (service_id, process, replicas) VALUES ($1, 'api', 3), ($1, 'worker', 5)", f.svcA.ID)
	require.NoError(t, err)

	routes := f.routes()
	require.Len(t, routes, 3)
	own, api := routes["site.example.com"], routes["api.example.com"]
	require.Equal(t, f.svcA.ID.String(), own.ServiceID, "the default process routes under the service's own id")
	require.Empty(t, own.Stack)
	require.Equal(t, Upstreams("site", dep.ID.String(), 2, 80), own.Upstreams)
	require.Equal(t, "/", own.HealthPath, "the default process has the defaulted http check on /")
	require.Equal(t, []string{"site.example.com"}, own.Hosts)

	require.Equal(t, agentprotocol.ProcessRouteID(f.svcA.ID.String(), "api"), api.ServiceID)
	require.Equal(t, f.svcA.ID.String(), api.Stack, "the process' route names its owner")
	require.Equal(t, ProcessUpstreams("site", dep.ID.String(), "api", 3, 4000), api.Upstreams)
	require.Equal(t, "/ready", api.HealthPath, "the health path of that process")
	require.Equal(t, []string{"api.example.com", "api2.example.com"}, api.Hosts, "both domains of the process share its route")
	require.Len(t, api.Domains, 2)
	require.Equal(t, routes["api2.example.com"].ServiceID, api.ServiceID)

	// Maintenance mode is the service's: every route of it carries it.
	require.NotNil(t, own.Maintenance)
	require.NotNil(t, api.Maintenance)
	require.Equal(t, "Back soon", api.Maintenance.Title)
}

func TestServerRoutesSkipDomainsOfProcessesTheReleaseDoesNotHave(t *testing.T) {
	f := newWWWFixture(t)
	f.setConfig(processConfig())
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "site.example.com", 0, RuleInput{})
	require.NoError(t, err)
	_, err = f.svc.CreateForComponent(f.ctx, f.svcA.ID, "api.example.com", 0, "api", RuleInput{})
	require.NoError(t, err)
	// The running release predates the processes.
	f.deployService(f.svcA.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())

	routes := f.routes()
	require.Contains(t, routes, "site.example.com")
	require.NotContains(t, routes, "api.example.com", "nothing runs that process yet")
}

func TestServerRoutesOfAServiceWithoutProcessesAreUnchanged(t *testing.T) {
	f := newWWWFixture(t)
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "site.example.com", 0, RuleInput{})
	require.NoError(t, err)
	dep := f.deployService(f.svcA.ID, services.Config{Image: "traefik/whoami:latest", Port: 80}.WithDefaults())
	rt := f.routes()["site.example.com"]
	require.Equal(t, f.svcA.ID.String(), rt.ServiceID)
	require.Empty(t, rt.Stack)
	require.Equal(t, Upstreams("site", dep.ID.String(), 1, 80), rt.Upstreams)
}

func TestMoveToServiceReHomesDomainsWithoutStagingAChange(t *testing.T) {
	f := newWWWFixture(t)
	bg := context.Background()

	// The source: a plain service with a domain.
	src := f.svcA
	d, err := f.svc.Create(f.ctx, src.ID, "shop.example.com", 0, RuleInput{})
	require.NoError(t, err)
	f.deployService(src.ID, services.DecodeConfig(src.Config))

	// The target runs the default process and api, deployed with a domain of its own.
	cfg := processConfig()
	raw, _ := json.Marshal(cfg)
	target, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "target", Slug: "target", Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	own, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: target.ID, Hostname: "target.example.com", TargetPort: 80})
	require.NoError(t, err)
	f.deployService(target.ID, cfg)
	// The target's release snapshot already lists its domain.
	rel := func() services.Snapshot {
		dep, err := f.q.ListDeploymentsByIDs(bg, []uuid.UUID{*mustCurrent(t, f.q, target.ID)})
		require.NoError(t, err)
		r, err := f.q.GetReleaseByID(bg, *dep[0].ReleaseID)
		require.NoError(t, err)
		return services.DecodeSnapshot(r.Config)
	}
	snap := rel()
	snap.Domains = []services.SnapshotDomain{services.SnapshotDomainOf(own, nil)}
	snapRaw, _ := json.Marshal(snap)
	curDep, err := f.q.ListDeploymentsByIDs(bg, []uuid.UUID{*mustCurrent(t, f.q, target.ID)})
	require.NoError(t, err)
	require.NoError(t, f.q.UpdateReleaseConfig(bg, db.UpdateReleaseConfigParams{ID: *curDep[0].ReleaseID, Config: snapRaw}))
	before, err := f.q.GetServiceScopeInternal(bg, target.ID)
	require.NoError(t, err)

	err = pgxTx(t, f, func(tx pgx.Tx) error {
		return f.svc.MoveToService(bg, tx, []uuid.UUID{d.ID}, target.ID, "api")
	})
	require.NoError(t, err)

	rows, err := f.q.ListServiceDomains(bg, target.ID)
	require.NoError(t, err)
	byHost := map[string]db.Domain{}
	for _, r := range rows {
		byHost[r.Hostname] = r
	}
	moved := byHost["shop.example.com"]
	require.Equal(t, target.ID, moved.ServiceID)
	require.Equal(t, "api", moved.Component, "the domain now routes to the process it was moved to")
	require.EqualValues(t, 80, moved.TargetPort, "the port is the caller's to keep right: it is not rewritten")
	left, err := f.q.ListServiceDomains(bg, src.ID)
	require.NoError(t, err)
	require.Empty(t, left)

	after, err := f.q.GetServiceScopeInternal(bg, target.ID)
	require.NoError(t, err)
	require.Equal(t, before.Service.Generation, after.Service.Generation, "no staged change")
	require.Equal(t, before.Service.AppliedGeneration, after.Service.AppliedGeneration)
	var hosts []string
	for _, sd := range rel().Domains {
		hosts = append(hosts, sd.Hostname)
	}
	require.ElementsMatch(t, []string{"target.example.com", "shop.example.com"}, hosts, "the release snapshot lists the moved domain")
	for _, sd := range rel().Domains {
		if sd.Hostname == "shop.example.com" {
			require.Equal(t, "api", sd.Component)
		}
	}

	// A process that does not exist or cannot be routed to is refused, and nothing moves.
	d2, err := f.svc.Create(f.ctx, src.ID, "blog.example.com", 0, RuleInput{})
	require.NoError(t, err)
	for _, process := range []string{"nope", "worker"} {
		err = pgxTx(t, f, func(tx pgx.Tx) error {
			return f.svc.MoveToService(bg, tx, []uuid.UUID{d2.ID}, target.ID, process)
		})
		requireCode(t, err, "invalid_process")
	}
	still, err := f.q.ListServiceDomains(bg, src.ID)
	require.NoError(t, err)
	require.Len(t, still, 1)

	// Moving nothing is a no-op.
	require.NoError(t, pgxTx(t, f, func(tx pgx.Tx) error { return f.svc.MoveToService(bg, tx, nil, target.ID, "api") }))
}

func mustCurrent(t *testing.T, q *db.Queries, id uuid.UUID) *uuid.UUID {
	t.Helper()
	row, err := q.GetServiceScopeInternal(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, row.Service.CurrentDeploymentID)
	return row.Service.CurrentDeploymentID
}

// pgxTx runs fn in a transaction that commits when fn succeeds.
func pgxTx(t *testing.T, f *wwwFixture, fn func(tx pgx.Tx) error) error {
	t.Helper()
	return db.InTx(context.Background(), f.pool, fn)
}

func TestComponentOrProcessFoldsTheTwoRequestFields(t *testing.T) {
	got, err := componentOrProcess("", "")
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = componentOrProcess("web", "")
	require.NoError(t, err)
	require.Equal(t, "web", got)
	got, err = componentOrProcess("", "api")
	require.NoError(t, err)
	require.Equal(t, "api", got)
	_, err = componentOrProcess("web", "api")
	requireCode(t, err, "validation_failed")
}
