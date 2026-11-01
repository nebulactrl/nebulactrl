package services_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestResolveProcessFilter(t *testing.T) {
	proc := func(name string) services.ProcessConfig {
		return services.ProcessConfig{Name: name, StartCommand: []string{name}}
	}
	single := services.Config{Image: "app:1"}.WithDefaults()
	multi := services.Config{Image: "app:1", Processes: []services.ProcessConfig{proc("worker")}}.WithDefaults()
	renamedSingle := services.Config{Image: "app:1", ProcessName: "http"}.WithDefaults()
	renamed := services.Config{Image: "app:1", ProcessName: "http", Processes: []services.ProcessConfig{proc("worker")}}.WithDefaults()

	cases := []struct {
		name            string
		staged, release services.Config
		requested       string
		wantName        string
		wantDefault     bool
		wantAll         bool
	}{
		{name: "no filter", staged: multi, release: multi, requested: "", wantAll: true},
		{name: "default of a multi-process release", staged: multi, release: multi, requested: "web", wantName: "web", wantDefault: true},
		{name: "named process passes through", staged: multi, release: multi, requested: "worker", wantName: "worker"},
		{
			name: "named process just staged on a single-process release", staged: multi, release: single,
			requested: "worker", wantName: "worker",
		},
		{
			name: "default just staged on a single-process release is the whole service", staged: multi, release: single,
			requested: "web", wantDefault: true, wantAll: true,
		},
		{
			name: "renamed default while the release runs processes", staged: renamed, release: multi,
			requested: "http", wantName: "web", wantDefault: true,
		},
		{
			name: "renamed default, release still single-process", staged: renamedSingle, release: single,
			requested: "http", wantDefault: true, wantAll: true,
		},
		{
			name: "release default renamed, staged back", staged: multi, release: renamed,
			requested: "web", wantName: "http", wantDefault: true,
		},
		{name: "named process of a renamed default", staged: renamed, release: multi, requested: "worker", wantName: "worker"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, isDefault, all := services.ResolveProcessFilter(c.staged, c.release, c.requested)
			require.Equal(t, c.wantName, name)
			require.Equal(t, c.wantDefault, isDefault)
			require.Equal(t, c.wantAll, all)
		})
	}
}

// run makes cfg the release the service runs now.
func (f *txFixture) run(sv db.Service, cfg services.Config) db.Service {
	f.t.Helper()
	bg := context.Background()
	snap, err := json.Marshal(services.Snapshot{Config: cfg.WithDefaults()})
	require.NoError(f.t, err)
	var relID, depID uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(bg, `INSERT INTO releases (service_id, source, config, env_enc, image_ref) VALUES ($1, '{"type":"image"}', $2, ''::bytea, '') RETURNING id`, sv.ID, snap).Scan(&relID))
	require.NoError(f.t, f.pool.QueryRow(bg, `INSERT INTO deployments (service_id, release_id, trigger, actor_type, status) VALUES ($1, $2, 'manual', 'user', 'succeeded') RETURNING id`, sv.ID, relID).Scan(&depID))
	_, err = f.pool.Exec(bg, "UPDATE services SET current_deployment_id = $2 WHERE id = $1", sv.ID, depID)
	require.NoError(f.t, err)
	return f.row(sv.ID)
}

func TestResolveServiceProcessFilterReadsTheRunningRelease(t *testing.T) {
	f := newTxFixture(t)
	staged := services.Config{Image: "app:1", ProcessName: "http", Processes: []services.ProcessConfig{worker("worker")}}
	sv := f.insert(f.prod.ID, "web", staged)

	// Nothing runs yet: the staged configuration stands for the release.
	name, isDefault, all, err := services.ResolveServiceProcessFilter(f.ctx, f.q, sv, "http")
	require.NoError(t, err)
	require.Equal(t, "http", name)
	require.True(t, isDefault)
	require.False(t, all)

	// A release of the old name with processes: the rename is not deployed.
	sv = f.run(sv, services.Config{Image: "app:1", Processes: []services.ProcessConfig{worker("worker")}})
	name, isDefault, all, err = services.ResolveServiceProcessFilter(f.ctx, f.q, sv, "http")
	require.NoError(t, err)
	require.Equal(t, "web", name)
	require.True(t, isDefault)
	require.False(t, all)

	// A single-process release: the default process is the whole service.
	sv = f.run(sv, services.Config{Image: "app:1"})
	_, isDefault, all, err = services.ResolveServiceProcessFilter(f.ctx, f.q, sv, "http")
	require.NoError(t, err)
	require.True(t, isDefault)
	require.True(t, all)
}

func TestMetricsFollowTheRunningReleaseProcesses(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	srv, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent1"})
	require.NoError(t, err)
	staged := services.Config{Image: "app:1", ProcessName: "http", Processes: []services.ProcessConfig{worker("worker")}}
	sv := f.insert(f.prod.ID, "web", staged)
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", sv.ID, srv.ID)
	require.NoError(t, err)
	put := func(ts time.Time, process string, replica int, cpu float64) {
		require.NoError(t, f.q.InsertServiceMetricSample(bg, db.InsertServiceMetricSampleParams{
			ServerID: &srv.ID, ServiceID: sv.ID, Ts: ts, Replica: int32(replica), Process: process, CpuPercent: cpu, MemBytes: 100 << 20, MemLimitBytes: 1 << 30,
		}))
	}
	now := time.Now().UTC()

	// A single-process release samples without a process: its default process,
	// which the dashboard offers by the staged name, is the whole series.
	sv = f.run(sv, services.Config{Image: "app:1"})
	put(now.Add(-time.Minute), "", 0, 10)
	put(now.Add(-time.Minute), "", 1, 20)
	res, err := f.svcs.Metrics(f.ctx, sv.ID, "1h", "http")
	require.NoError(t, err)
	require.Len(t, res.Points, 1)
	require.InDelta(t, 30, res.Points[0].CPUPercent, 0.01)
	require.Len(t, res.Points[0].Replicas, 2, "the per-replica list belongs to the one process")
	res, err = f.svcs.Metrics(f.ctx, sv.ID, "1h", "worker")
	require.NoError(t, err)
	require.Empty(t, res.Points, "the worker is not deployed yet")

	// A release with processes samples by the name it runs: a staged rename of
	// the default process still reads the old name's series.
	sv = f.run(sv, services.Config{Image: "app:1", Processes: []services.ProcessConfig{worker("worker")}})
	put(now, "web", 0, 7)
	put(now, "worker", 0, 5)
	res, err = f.svcs.Metrics(f.ctx, sv.ID, "1h", "http")
	require.NoError(t, err)
	require.Len(t, res.Points, 1)
	require.InDelta(t, 7, res.Points[0].CPUPercent, 0.01)
}
