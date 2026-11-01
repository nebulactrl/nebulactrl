package engine

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/volbackup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// pauseContainer freezes a seeded container like a volume backup does.
func pauseContainer(t *testing.T, r *rig, name string) {
	t.Helper()
	require.NoError(t, r.dk.ContainerPause(context.Background(), name))
}

func TestReconcileLeavesAPausedContainerAloneWhileABackupHoldsTheService(t *testing.T) {
	r := newRig(t, nil)
	c := r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	pauseContainer(t, r, oldCtr)
	r.eng.cfg.OrphanMinAge = time.Nanosecond

	unlock := r.eng.lockService(svcID) // the backup's freeze window
	defer unlock()
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{
		{ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr}},
	}}))

	st, ok := r.dk.state(c.Name)
	require.True(t, ok, "a paused container is never removed")
	require.Equal(t, "paused", st.State, "reconcile must not thaw a freeze that is in progress")
	require.Empty(t, r.dk.started, "nothing may be (re)started")
	require.Empty(t, r.dk.stopped, "nothing may be stopped")
	require.Empty(t, r.dk.removed)

	// It is reported as healthy-but-running, not missing or unhealthy, so the
	// control plane does not redeploy it.
	require.NotEmpty(t, r.snd.observed)
	var got agentprotocol.ObservedService
	for _, o := range r.snd.observed[len(r.snd.observed)-1].Services {
		if o.ServiceID == svcID {
			got = o
		}
	}
	require.Equal(t, agentprotocol.StateRunning, got.State)
	require.Equal(t, agentprotocol.HealthHealthy, got.Health)
}

func TestReconcileThawsAPauseNobodyOwns(t *testing.T) {
	r := newRig(t, nil)
	c := r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	pauseContainer(t, r, oldCtr) // a dead backup left it frozen; no command holds the service
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{
		{ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr}},
	}}))
	st, _ := r.dk.state(c.Name)
	require.Equal(t, "running", st.State)
	require.Empty(t, r.dk.started, "thawing is an unpause, not a restart")
	require.Empty(t, r.dk.stopped)
}

func TestVolumeBackupCommandFreezesOnlyForTheCopy(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr}}

	var pausedDuringCopy bool
	var uploadedWhilePaused bool
	r.dk.onExec = func(id string, argv []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		st, _ := r.dk.state(id)
		if st.Image != agentprotocol.ResticImage {
			return 0, nil
		}
		joined := strings.Join(argv, " ")
		target, _ := r.dk.state(oldCtr)
		switch {
		case strings.Contains(joined, "cp --reflink=always"):
			return 0, nil
		case strings.Contains(joined, "cp -a --reflink=auto"):
			pausedDuringCopy = target.State == "paused"
		case strings.Contains(joined, " backup /vol "):
			uploadedWhilePaused = target.State == "paused"
			_, _ = io.WriteString(stdout, `{"message_type":"summary","files_new":1,"data_added":10,"total_bytes_processed":20,"snapshot_id":"`+strings.Repeat("c", 64)+`"}`+"\n")
		}
		return 0, nil
	}

	payload, _ := json.Marshal(agentprotocol.VolumeBackupPayload{
		BackupID: "bbbbbbbb-0000-0000-0000-000000000000", ServiceID: svcID,
		Repo: agentprotocol.VolumeRepo{
			Destination: agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Bucket: "bk", Endpoint: "https://s3.example.com"},
			Path:        "volumes/" + svcID,
		},
		Volumes: []agentprotocol.VolumeRef{{Name: "data", DockerName: "nc-vol-" + agentprotocol.Short(svcID) + "-data", MountPath: "/data"}},
	})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{
		ID: "vb-1", Kind: agentprotocol.KindVolumeBackup, Payload: payload,
		Secrets: map[string]string{agentprotocol.SecretResticPassword: "pw-pw-pw"},
	})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.VolumeBackupResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, strings.Repeat("c", 64), out.SnapshotID)
	require.True(t, pausedDuringCopy, "the copy runs while the service is frozen")
	require.False(t, uploadedWhilePaused, "the upload never holds the service frozen")
	paused, unpaused := r.dk.pauseLog()
	require.Equal(t, []string{oldCtr}, paused)
	require.Equal(t, []string{oldCtr}, unpaused)
	require.False(t, r.eng.isInflight(svcID), "the service lock is released after the window")
	st, _ := r.dk.state(oldCtr)
	require.Equal(t, "running", st.State)
	// Helpers and staging are gone and never carried the managed label.
	for _, spec := range r.dk.created {
		if spec.Image == agentprotocol.ResticImage {
			require.Equal(t, agentprotocol.HelperBackup, spec.Labels[agentprotocol.LabelHelper])
			require.NotContains(t, spec.Labels, agentprotocol.LabelManaged)
		}
	}
	cs, _ := r.dk.ContainerList(context.Background(), docker.ListFilter{All: true, Labels: map[string]string{agentprotocol.LabelHelper: agentprotocol.HelperBackup}})
	require.Empty(t, cs)
}

func TestVolumeHostLockServiceTimesOutAndStillReleases(t *testing.T) {
	r := newRig(t, nil)
	held := r.eng.lockService(svcID)
	h := volHost{r.eng}
	_, err := h.LockService(context.Background(), svcID, 30*time.Millisecond)
	require.ErrorIs(t, err, volbackup.ErrBusy)
	held() // the abandoned waiter takes the lock and must give it back
	deadline := time.After(2 * time.Second)
	for r.eng.isInflight(svcID) {
		select {
		case <-deadline:
			t.Fatal("the lock taken by an abandoned wait was never released")
		case <-time.After(5 * time.Millisecond):
		}
	}
	unlock, err := h.LockService(context.Background(), svcID, time.Second)
	require.NoError(t, err)
	unlock()
}
