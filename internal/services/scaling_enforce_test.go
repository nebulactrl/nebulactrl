package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// seedProcessScale stores a scale row for a process: 3 replicas, autoscaling on.
func seedProcessScale(t *testing.T, f *txFixture, id uuid.UUID, process string) {
	t.Helper()
	bg := context.Background()
	_, err := f.q.EnsureProcessScale(bg, db.EnsureProcessScaleParams{ServiceID: id, Process: process})
	require.NoError(t, err)
	_, err = f.q.ApplyProcessScale(bg, db.ApplyProcessScaleParams{
		ServiceID: id, Process: process, Replicas: 3, ExpectedReplicas: 1, LastScaleEvent: services.NewScaleEvent(1, 3, "manual", "dev"),
	})
	require.NoError(t, err)
	_, err = f.q.SetProcessAutoscaling(bg, db.SetProcessAutoscalingParams{
		ServiceID: id, Process: process, Autoscaling: json.RawMessage(`{"enabled":true,"min":1,"max":4,"targetCpuPercent":60}`),
	})
	require.NoError(t, err)
}

func scaleRow(t *testing.T, f *txFixture, id uuid.UUID, process string) (db.ServiceProcessScale, bool) {
	t.Helper()
	for _, r := range mustScales(t, f.q, id) {
		if r.Process == process {
			return r, true
		}
	}
	return db.ServiceProcessScale{}, false
}

func clampProcesses(t *testing.T, f *txFixture, id uuid.UUID, release services.Config) {
	t.Helper()
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		return services.ClampProcessReplicas(f.ctx, tx, f.scope(tx, id), release, 0)
	}))
}

func TestClampProcessReplicasKeepsStagedProcessesOfAnOlderRelease(t *testing.T) {
	f := newTxFixture(t)
	staged := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("queue")}}.WithDefaults()
	svc := f.insert(f.prod.ID, "web", staged)
	seedProcessScale(t, f, svc.ID, "queue")

	// A rollback to a release that never had the process, or a recovery redeploy
	// of one that predates it, must not forget the scale of the staged process.
	for _, release := range []services.Config{
		services.Config{Image: "x", Port: 3000}.WithDefaults(),
		services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("other")}}.WithDefaults(),
	} {
		clampProcesses(t, f, svc.ID, release)
		row, ok := scaleRow(t, f, svc.ID, "queue")
		require.True(t, ok, "the staged process keeps its scale row")
		require.EqualValues(t, 3, row.Replicas)
		require.True(t, services.DecodeAutoscaling(row.Autoscaling).Enabled)
	}
}

func TestClampProcessReplicasPrunesProcessesDroppedEverywhere(t *testing.T) {
	f := newTxFixture(t)
	staged := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("queue")}}.WithDefaults()
	svc := f.insert(f.prod.ID, "web", staged)
	seedProcessScale(t, f, svc.ID, "queue")
	seedProcessScale(t, f, svc.ID, "gone")
	release := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("old")}}.WithDefaults()
	seedProcessScale(t, f, svc.ID, "old")

	clampProcesses(t, f, svc.ID, release)
	require.ElementsMatch(t, []string{"queue", "old"}, processesOf(mustScales(t, f.q, svc.ID)),
		"only the process neither the release nor the staged configuration has is forgotten")
}

func TestClampProcessReplicasLeavesProcessesTheStagedConfigMakesEligible(t *testing.T) {
	f := newTxFixture(t)
	staged := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("clock")}}.WithDefaults()
	svc := f.insert(f.prod.ID, "web", staged)
	seedProcessScale(t, f, svc.ID, "clock")

	// An older release ran clock as a singleton; the staged configuration does not.
	release := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{
		worker("clock", func(p *services.ProcessConfig) { p.Singleton = true }),
	}}.WithDefaults()
	clampProcesses(t, f, svc.ID, release)
	row, _ := scaleRow(t, f, svc.ID, "clock")
	require.EqualValues(t, 3, row.Replicas)
	require.True(t, services.DecodeAutoscaling(row.Autoscaling).Enabled)

	// Ineligible in both: clamped.
	both := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{
		worker("clock", func(p *services.ProcessConfig) { p.Singleton = true }),
	}}.WithDefaults()
	other := f.insert(f.prod.ID, "both", both)
	seedProcessScale(t, f, other.ID, "clock")
	clampProcesses(t, f, other.ID, both)
	row, _ = scaleRow(t, f, other.ID, "clock")
	require.EqualValues(t, 1, row.Replicas)
	require.False(t, services.DecodeAutoscaling(row.Autoscaling).Enabled)
}

func TestClampReplicasFollowsTheStagedConfigToo(t *testing.T) {
	f := newTxFixture(t)
	setDefault := func(id uuid.UUID) {
		_, err := f.pool.Exec(context.Background(), `UPDATE services SET replicas = 3, autoscaling = '{"enabled":true,"min":1,"max":4,"targetCpuPercent":60}' WHERE id = $1`, id)
		require.NoError(t, err)
	}
	clamp := func(id uuid.UUID, release services.Config) bool {
		var changed bool
		require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
			var err error
			changed, err = services.ClampReplicas(f.ctx, tx, f.scope(tx, id), release, 0)
			return err
		}))
		return changed
	}
	singleton := services.Config{Image: "x", Port: 3000, Singleton: true}.WithDefaults()

	// A rollback to a release where the default process was a singleton, with a
	// staged configuration that is not: the setting stays.
	rolled := f.insert(f.prod.ID, "rolled", services.Config{Image: "x", Port: 3000})
	setDefault(rolled.ID)
	require.False(t, clamp(rolled.ID, singleton))
	got := f.row(rolled.ID)
	require.EqualValues(t, 3, got.Replicas)
	require.True(t, services.DecodeAutoscaling(got.Autoscaling).Enabled)

	// A singleton in both is one replica without autoscaling.
	both := f.insert(f.prod.ID, "both", singleton)
	setDefault(both.ID)
	require.True(t, clamp(both.ID, singleton))
	got = f.row(both.ID)
	require.EqualValues(t, 1, got.Replicas)
	require.False(t, services.DecodeAutoscaling(got.Autoscaling).Enabled)
}
