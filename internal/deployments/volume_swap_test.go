package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// withVolume attaches a volume to the service and records it in the current
// release's snapshot (as a deployment would have).
func (f *routeFixture) withVolume(sv db.Service) db.Volume {
	f.t.Helper()
	bg := context.Background()
	vol, err := f.q.InsertVolume(bg, db.InsertVolumeParams{ServiceID: sv.ID, Name: "data", MountPath: "/data"})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{
		Config: services.DecodeConfig(sv.Config), ServerID: &f.server.ID,
		Volumes: []services.SnapshotVolume{{Name: "data", MountPath: "/data"}},
	})
	_, err = f.pool.Exec(bg, "UPDATE releases SET config = $2 WHERE service_id = $1", sv.ID, snap)
	require.NoError(f.t, err)
	return vol
}

// A restore swaps the volume row to a restored Docker volume (staged). Discarding
// the pending change restores the release's volume and retires the new one.
func TestDiscardVolumeSwapRevertsAndRetiresTheRestoredVolume(t *testing.T) {
	f := newRouteFixture(t)
	bg := context.Background()
	web := f.web("web")
	vol := f.withVolume(web)
	original := agentprotocol.VolumeName(web.ID.String(), "data")
	restored := original + "-r260112033000"

	// What a finished new-volume restore stages.
	require.NoError(t, f.q.InsertRetiredVolume(bg, db.InsertRetiredVolumeParams{ServiceID: web.ID, VolumeName: "data", DockerName: original, Reason: "replaced"}))
	require.NoError(t, f.q.SetVolumeDockerName(bg, db.SetVolumeDockerNameParams{ID: vol.ID, DockerName: restored, RestoreLabel: "restored from Jan 12, 03:30"}))
	_, err := f.q.BumpServiceGeneration(bg, web.ID)
	require.NoError(t, err)

	pending, err := f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "volumes.data", pending[0].Field)
	require.Equal(t, "/data", pending[0].From)
	require.Equal(t, "/data · restored from Jan 12, 03:30", pending[0].To)

	// The deploy mounts the restored volume.
	cur, err := f.q.ListServiceVolumes(bg, web.ID)
	require.NoError(t, err)
	require.Equal(t, restored, services.DockerVolumeName(cur[0]))

	_, err = f.deps.DiscardPendingChange(f.ctx, web.ID, "volumes.data")
	require.NoError(t, err)

	vols, err := f.q.ListServiceVolumes(bg, web.ID)
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Empty(t, vols[0].DockerName, "the release's volume is mounted again")
	require.Equal(t, original, services.DockerVolumeName(vols[0]))
	require.Empty(t, vols[0].RestoreLabel)
	retired, err := f.q.ListRetiredVolumes(bg, web.ID)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, restored, retired[0].DockerName, "the discarded restore's volume is kept, retired")

	pending, err = f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// The deploy payload mounts the Docker volume a volume row points at.
func TestDeployMountsTheRestoredDockerVolume(t *testing.T) {
	f := newRouteFixture(t)
	bg := context.Background()
	web := f.web("web")
	vol := f.withVolume(web)
	restored := agentprotocol.VolumeName(web.ID.String(), "data") + "-r260112033000"
	require.NoError(t, f.q.SetVolumeDockerName(bg, db.SetVolumeDockerNameParams{ID: vol.ID, DockerName: restored, RestoreLabel: "restored from Jan 12, 03:30"}))

	v, err := f.deps.Deploy(f.ctx, web.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID))
	cmd := f.lastCommand(agentprotocol.KindDeploy)
	var p agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, []agentprotocol.VolumeMount{{Name: restored, MountPath: "/data"}}, p.Volumes)

	// Once that deployment is current, its release records the swap: nothing is pending.
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", v.ID)
	require.NoError(t, err)
	require.NoError(t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: web.ID, CurrentDeploymentID: &v.ID, AppliedGeneration: 1 << 40}))
	pending, err := f.deps.PendingChanges(f.ctx, web.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
}
