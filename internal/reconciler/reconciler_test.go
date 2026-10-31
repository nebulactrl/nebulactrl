package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/svcalerts"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type noDisp struct{}

func (noDisp) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (noDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

const stackCompose = "services:\n  web:\n    image: traefik/whoami:v1.10\n    ports: [\"80:80\"]\n  sidecar:\n    image: busybox:1.36\n"

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	r      *Reconciler
	env    db.Environment
	server db.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	svcs := services.NewService(pool, bus, box, noDisp{}, logger)
	deps := deployments.NewService(deployments.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Dispatcher: noDisp{}, Services: svcs, Logger: logger})
	return &fixture{t: t, pool: pool, q: q, r: New(pool, bus, jm, deps, logger), env: env, server: srv}
}

// deployed inserts a service whose current deployment runs on the server.
func (f *fixture) deployed(name, kind, source string, cfg services.Config) db.Service {
	f.t.Helper()
	bg := context.Background()
	raw, _ := json.Marshal(cfg.WithDefaults())
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: name, Slug: name, Kind: kind, SourceType: source, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(sv.Config), ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: source})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	row, err := f.q.GetServiceScopeInternal(bg, sv.ID)
	require.NoError(f.t, err)
	return row.Service
}

func TestStackDesiredStateAndObservedComponents(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	stack := f.deployed("shop", services.KindStack, services.SourceCompose, services.Config{Stack: &services.StackConfig{Compose: stackCompose}})
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	_, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: stack.ID, Hostname: "shop.example.com", TargetPort: 80, Component: "web"})
	require.NoError(t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: app.ID, Hostname: "app.example.com", TargetPort: 80})
	require.NoError(t, err)

	out, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	byID := map[string]agentprotocol.DesiredService{}
	for _, d := range out.Services {
		byID[d.ServiceID] = d
	}
	sd := byID[stack.ID.String()]
	require.True(t, sd.Stack)
	require.Empty(t, sd.ContainerNames)
	require.Equal(t, stack.CurrentDeploymentID.String(), sd.DeploymentID)
	require.Len(t, sd.Routes, 1)
	require.Equal(t, stack.ID.String(), sd.Routes[0].Stack)
	require.Equal(t, agentprotocol.StackRouteID(stack.ID.String(), "web", 80), sd.Routes[0].ServiceID)
	require.Equal(t, []string{agentprotocol.StackContainerName(stack.ID.String(), "web") + ":80"}, sd.Routes[0].Upstreams)
	ad := byID[app.ID.String()]
	require.False(t, ad.Stack)
	require.NotEmpty(t, ad.ContainerNames)
	require.Len(t, ad.Routes, 1)
	require.Empty(t, ad.Routes[0].Stack)
	// The stack's routes are owned by the stack: none is left over as an external route.
	for _, rt := range out.ExternalRoutes {
		require.NotEqual(t, stack.ID.String(), rt.Stack)
	}

	// Observed components keep their name and index.
	err = f.r.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{
		ServiceID: stack.ID.String(), DeploymentID: stack.CurrentDeploymentID.String(), State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy,
		ReplicasReady: 2, ReplicasDesired: 2,
		Replicas: []agentprotocol.ObservedReplica{
			{Index: 0, Component: "sidecar", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy},
			{Index: 1, Component: "web", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy},
		},
	}}})
	require.NoError(t, err)
	row, err := f.q.GetServiceScopeInternal(bg, stack.ID)
	require.NoError(t, err)
	obs := services.DecodeObserved(row.Service.Observed)
	require.NotNil(t, obs)
	require.Len(t, obs.Replicas, 2)
	require.Equal(t, "sidecar", obs.Replicas[0].Component)
	require.Equal(t, 1, obs.Replicas[1].Index)
	require.Equal(t, "web", obs.Replicas[1].Component)
	require.True(t, replicasChanged(obs.Replicas, []services.ObservedReplicaView{{Index: 0, Component: "web"}, {Index: 1, Component: "sidecar"}}))
}

type captureAlerts struct{ got []svcalerts.Input }

func (c *captureAlerts) Observe(_ context.Context, in svcalerts.Input) { c.got = append(c.got, in) }

func TestObservedReportsFeedTheHealthAlerts(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	cap := &captureAlerts{}
	f.r.SetAlerts(cap)
	app := f.deployed("web", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})
	report := func(state, health string, restarts int) {
		t.Helper()
		require.NoError(t, f.r.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{
			ServiceID: app.ID.String(), DeploymentID: app.CurrentDeploymentID.String(), State: state, Health: health,
			ReplicasReady: 0, ReplicasDesired: 1,
			Replicas: []agentprotocol.ObservedReplica{{Index: 0, ContainerID: "abc", State: state, Health: health, RestartCount: restarts}},
		}}}))
	}

	report(agentprotocol.StateExited, agentprotocol.HealthUnhealthy, 2)
	require.Len(t, cap.got, 1)
	in := cap.got[0]
	require.Equal(t, app.ID, in.ServiceID)
	require.Equal(t, "web", in.ServiceName)
	require.Equal(t, app.CurrentDeploymentID.String(), in.DeploymentID)
	require.Equal(t, agentprotocol.StateExited, in.State)
	require.False(t, in.Suppressed)
	require.False(t, in.Stopped)
	require.Equal(t, []svcalerts.Replica{{Key: "/0", ContainerID: "abc", State: agentprotocol.StateExited, Health: agentprotocol.HealthUnhealthy, RestartCount: 2}}, in.Replicas)
	require.NotEqual(t, uuid.Nil, in.WorkspaceID)
	require.NotEqual(t, uuid.Nil, in.ProjectID)

	// A deployment replacing the container on purpose suppresses the report.
	_, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: app.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	report(agentprotocol.StateExited, agentprotocol.HealthUnhealthy, 2)
	require.True(t, cap.got[1].Suppressed)

	// A stopped service is marked as such.
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'canceled' WHERE service_id = $1 AND status = 'created'", app.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", app.ID)
	require.NoError(t, err)
	report(agentprotocol.StateExited, agentprotocol.HealthUnhealthy, 2)
	require.True(t, cap.got[2].Stopped)
	require.False(t, cap.got[2].Suppressed)

	// A healthy report is never suppressed (no query needed) and is still delivered.
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'running' WHERE id = $1", app.ID)
	require.NoError(t, err)
	report(agentprotocol.StateRunning, agentprotocol.HealthHealthy, 2)
	require.Len(t, cap.got, 4)
	require.False(t, cap.got[3].Suppressed)
}
