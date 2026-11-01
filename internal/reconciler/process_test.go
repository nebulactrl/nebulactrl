package reconciler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/svcalerts"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// processesService is a deployed service whose release runs the default process
// "web" (port 3000) and api (port 4000), clock (a singleton) and worker.
func (f *fixture) processesService(name string) db.Service {
	f.t.Helper()
	return f.deployed(name, services.KindApp, services.SourceImage, services.Config{
		Image: "nginx:1", Port: 3000,
		Processes: []services.ProcessConfig{
			{Name: "worker", StartCommand: []string{"bin/worker"}},
			{Name: "clock", StartCommand: []string{"bin/clock"}, Singleton: true},
			{Name: "api", StartCommand: []string{"bin/api"}, Port: 4000},
		},
	})
}

func (f *fixture) setScale(id uuid.UUID, process string, replicas int) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO service_process_scale (service_id, process, replicas) VALUES ($1, $2, $3)
		 ON CONFLICT (service_id, process) DO UPDATE SET replicas = $3`, id, process, replicas)
	require.NoError(f.t, err)
}

func desiredOf(t *testing.T, out agentprotocol.Reconcile, id string) agentprotocol.DesiredService {
	t.Helper()
	for _, d := range out.Services {
		if d.ServiceID == id {
			return d
		}
	}
	t.Fatalf("service %s is not in the desired state", id)
	return agentprotocol.DesiredService{}
}

func TestDesiredStateListsEveryProcessWithItsReplicasAndScaledAt(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	app := f.processesService("shop")
	plain := f.deployed("blog", services.KindApp, services.SourceImage, services.Config{Image: "nginx:1", Port: 80})

	_, err := f.pool.Exec(bg, "UPDATE services SET replicas = 2, last_scale_at = now() - interval '1 hour' WHERE id = $1", app.ID)
	require.NoError(t, err)
	f.setScale(app.ID, "worker", 3)
	f.setScale(app.ID, "clock", 4) // a singleton: the release runs one
	f.setScale(app.ID, "api", 2)
	_, err = f.pool.Exec(bg, "UPDATE service_process_scale SET last_scale_at = '2026-01-02T03:04:05Z' WHERE service_id = $1 AND process = 'worker'", app.ID)
	require.NoError(t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: app.ID, Hostname: "shop.example.com", TargetPort: 3000})
	require.NoError(t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: app.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)

	out, err := f.r.DesiredState(bg, f.server.ID)
	require.NoError(t, err)
	ds := desiredOf(t, out, app.ID.String())
	dep := app.CurrentDeploymentID.String()

	names := make([]string, len(ds.Processes))
	for i, p := range ds.Processes {
		names[i] = p.Name
	}
	require.Equal(t, []string{"web", "api", "clock", "worker"}, names, "the default process first")
	web, api, clock, worker := ds.Processes[0], ds.Processes[1], ds.Processes[2], ds.Processes[3]
	require.True(t, web.Default)
	require.False(t, worker.Default)
	require.Equal(t, 2, web.Replicas)
	require.Equal(t, agentprotocol.ProcessContainerNames("shop", dep, "web", true, 2), web.ContainerNames)
	require.Equal(t, []string{agentprotocol.ContainerName("shop", dep, 0), agentprotocol.ContainerName("shop", dep, 1)}, web.ContainerNames, "the default process keeps the legacy names")
	require.Equal(t, 2, api.Replicas)
	require.Equal(t, agentprotocol.ProcessContainerNames("shop", dep, "api", false, 2), api.ContainerNames)
	require.Equal(t, 1, clock.Replicas, "a singleton runs one whatever its stored count")
	require.Len(t, clock.ContainerNames, 1)
	require.Equal(t, 3, worker.Replicas)
	require.Len(t, worker.ContainerNames, 3)
	require.True(t, worker.ScaledAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), "each process carries its own stamp: %v", worker.ScaledAt)
	require.True(t, api.ScaledAt.IsZero(), "never scaled")
	require.False(t, web.ScaledAt.IsZero())

	// The top-level fields mirror the default process.
	require.Equal(t, web.Replicas, ds.Replicas)
	require.Equal(t, web.ContainerNames, ds.ContainerNames)
	require.True(t, ds.ScaledAt.Equal(web.ScaledAt))

	// Routes stay flat: the service's own and the api process' one, which names its owner.
	require.Len(t, ds.Routes, 2)
	var own, proc agentprotocol.Route
	for _, rt := range ds.Routes {
		if rt.Stack == "" {
			own = rt
		} else {
			proc = rt
		}
	}
	require.Equal(t, app.ID.String(), own.ServiceID)
	require.Equal(t, agentprotocol.ProcessRouteID(app.ID.String(), "api"), proc.ServiceID)
	require.Equal(t, app.ID.String(), proc.Stack)
	require.Equal(t, []string{
		agentprotocol.ProcessContainerName("shop", dep, "api", false, 0) + ":4000",
		agentprotocol.ProcessContainerName("shop", dep, "api", false, 1) + ":4000",
	}, proc.Upstreams)
	for _, rt := range out.ExternalRoutes {
		require.NotEqual(t, app.ID.String(), rt.Stack, "a process' route is owned by its service, not external")
	}

	// A service without processes is unchanged.
	pd := desiredOf(t, out, plain.ID.String())
	require.Empty(t, pd.Processes)
	require.NotEmpty(t, pd.ContainerNames)
}

func TestObservedProcessesAreStoredAndAMissingProcessTriggersRecovery(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	RegisterJobs(f.r.jm, f.r)
	app := f.processesService("shop")
	dep := app.CurrentDeploymentID.String()
	redeploys := func() int {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, "SELECT count(*) FROM river_job WHERE kind = 'reconcile_redeploy'").Scan(&n))
		return n
	}
	report := func(web, worker string) {
		t.Helper()
		healthy := func(state string) string {
			if state == agentprotocol.StateRunning {
				return agentprotocol.HealthHealthy
			}
			return agentprotocol.HealthUnknown
		}
		require.NoError(t, f.r.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{
			ServiceID: app.ID.String(), DeploymentID: dep,
			// The aggregate is the best replica: running while any process runs.
			State: agentprotocol.StateRunning, Health: agentprotocol.HealthDegraded, ReplicasReady: 1, ReplicasDesired: 2,
			Replicas: []agentprotocol.ObservedReplica{
				{Index: 0, Process: "web", State: web, Health: healthy(web)},
				{Index: 0, Process: "worker", State: worker, Health: healthy(worker)},
			},
			Processes: []agentprotocol.ObservedProcess{
				{Name: "web", State: web, Health: healthy(web), ReplicasReady: 1, ReplicasDesired: 1},
				{Name: "worker", State: worker, Health: healthy(worker), ReplicasReady: 0, ReplicasDesired: 1},
			},
		}}}))
	}

	report(agentprotocol.StateRunning, agentprotocol.StateRunning)
	row, err := f.q.GetServiceScopeInternal(bg, app.ID)
	require.NoError(t, err)
	obs := services.DecodeObserved(row.Service.Observed)
	require.NotNil(t, obs)
	require.Equal(t, []services.ObservedProcessView{
		{Name: "web", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, ReplicasReady: 1, ReplicasDesired: 1},
		{Name: "worker", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, ReplicasReady: 0, ReplicasDesired: 1},
	}, obs.Processes)
	require.Len(t, obs.Replicas, 2)
	require.Equal(t, "worker", obs.Replicas[1].Process)
	require.Equal(t, 0, obs.Replicas[1].Index, "the index counts within the process")
	require.Equal(t, 0, redeploys(), "nothing is missing")

	// Every container of one process vanished while the other still runs: the
	// aggregate says running, the service needs the redeploy of its current release.
	report(agentprotocol.StateRunning, agentprotocol.StateMissing)
	require.Equal(t, 1, redeploys())
	row, err = f.q.GetServiceScopeInternal(bg, app.ID)
	require.NoError(t, err)
	obs = services.DecodeObserved(row.Service.Observed)
	require.Equal(t, agentprotocol.StateMissing, obs.Processes[1].State)
	require.Empty(t, obs.Error)

	// A deployment already replacing it holds recovery back.
	_, err = f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: app.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "DELETE FROM river_job WHERE kind = 'reconcile_redeploy'")
	require.NoError(t, err)
	report(agentprotocol.StateRunning, agentprotocol.StateMissing)
	require.Equal(t, 0, redeploys())
}

func TestObservedReplicaProcessChangesArePublishedAndAlertKeysCarryTheProcess(t *testing.T) {
	require.True(t, replicasChanged(
		[]services.ObservedReplicaView{{Index: 0, Process: "web"}},
		[]services.ObservedReplicaView{{Index: 0, Process: "worker"}}))
	require.False(t, replicasChanged(
		[]services.ObservedReplicaView{{Index: 0, Process: "web", State: "running"}},
		[]services.ObservedReplicaView{{Index: 0, Process: "web", State: "running"}}))
	a := []services.ObservedProcessView{{Name: "web", State: "running", ReplicasReady: 1, ReplicasDesired: 1}}
	require.False(t, processesChanged(a, append([]services.ObservedProcessView(nil), a...)))
	require.True(t, processesChanged(a, []services.ObservedProcessView{{Name: "web", State: "exited", ReplicasDesired: 1}}))
	require.True(t, processesChanged(nil, a))
	require.False(t, anyProcessMissing([]agentprotocol.ObservedProcess{{Name: "web", State: agentprotocol.StateRunning}}))
	require.True(t, anyProcessMissing([]agentprotocol.ObservedProcess{{Name: "web", State: agentprotocol.StateRunning}, {Name: "x", State: agentprotocol.StateMissing}}))

	f := newFixture(t)
	bg := context.Background()
	cap := &captureAlerts{}
	f.r.SetAlerts(cap)
	app := f.processesService("shop")
	stack := f.deployed("stk", services.KindStack, services.SourceCompose, services.Config{Stack: &services.StackConfig{Compose: stackCompose}})
	require.NoError(t, f.r.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{
		{
			ServiceID: app.ID.String(), DeploymentID: app.CurrentDeploymentID.String(), State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy,
			Replicas: []agentprotocol.ObservedReplica{
				{Index: 0, Process: "web", ContainerID: "w0", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy},
				{Index: 1, Process: "worker", ContainerID: "k1", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy, RestartCount: 3},
			},
		},
		{
			ServiceID: stack.ID.String(), DeploymentID: stack.CurrentDeploymentID.String(), State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy,
			Replicas: []agentprotocol.ObservedReplica{{Index: 0, Component: "web", ContainerID: "c0", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy}},
		},
	}}))
	require.Len(t, cap.got, 2)
	require.Equal(t, []string{"web/0", "worker/1"}, []string{cap.got[0].Replicas[0].Key, cap.got[0].Replicas[1].Key}, "keyed by process and index within it")
	require.Equal(t, svcalerts.Replica{Key: "web/0", ContainerID: "c0", State: agentprotocol.StateRunning, Health: agentprotocol.HealthHealthy}, cap.got[1].Replicas[0], "stacks keep their component key")
}
