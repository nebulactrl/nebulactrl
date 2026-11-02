package backups

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestVolumeBackupConfigRefusesWhatCannotBeRunOrStored(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	dest := f.s3Destination()
	app := f.app("web", true)
	put := func(mut func(*VolumeBackupConfigInput)) error {
		in := VolumeBackupConfigInput{DestinationID: dest.ID}
		mut(&in)
		_, err := f.svc.PutVolumeBackupConfig(f.ctx, app.ID, in)
		return err
	}

	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) {
		in.PreHook = &VolumeBackupHook{Command: []string{"sh", "a\x00b"}}
	}), "preHook: a command argument is too long or contains a NUL byte")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) {
		in.PostHook = &VolumeBackupHook{Command: []string{"true"}, User: "root user"}
	}), "postHook: user is not valid")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) {
		in.PreHook = &VolumeBackupHook{Command: []string{"true"}, TimeoutSeconds: agentprotocol.MaxHookTimeoutSeconds + 1}
	}), "timeoutSeconds must be 1-")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) { in.Excludes = []string{"a\nb"} }), "control character")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) { in.Excludes = []string{strings.Repeat("x", 5000)} }), "too long")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) {
		for i := 0; i <= maxExcludes; i++ {
			in.Excludes = append(in.Excludes, "p"+string(rune('a'+i%26))+strings.Repeat("x", i))
		}
	}), "at most")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) { in.KeepDaily = new(3651) }), "keepDaily must be 0-3650")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) { in.KeepWeekly = new(-1) }), "keepWeekly must be 0-")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) { in.FreezeBudgetSeconds = 61 }), "freezeBudgetSeconds must be")
	require.ErrorContains(t, put(func(in *VolumeBackupConfigInput) { in.Cron, in.Timezone, in.Enabled = "@daily", "Mars/Olympus", true }), "")
	v, err := f.svc.GetVolumeBackupConfig(f.ctx, app.ID)
	require.NoError(t, err)
	require.False(t, v.Configured, "every refusal saved nothing")

	// Deleting a configuration that was never saved is a 404.
	err = f.svc.DeleteVolumeBackupConfig(f.ctx, app.ID)
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status)

	// The reason a service cannot use volume backups, per kind.
	for kind, want := range map[string]string{"route": "no files to back up", "stack": "Compose stacks"} {
		sv, err := f.q.InsertService(bg, db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: f.env.ID, Name: kind, Slug: kind, Kind: kind, SourceType: "image", Config: []byte(`{}`), ServerID: &f.server.ID,
		})
		require.NoError(t, err)
		got, err := f.svc.GetVolumeBackupConfig(f.ctx, sv.ID)
		require.NoError(t, err)
		require.False(t, got.Eligible, kind)
		require.Contains(t, got.IneligibleReason, want, kind)
	}
	unplaced := f.app("floating", true)
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = NULL WHERE id = $1", unplaced.ID)
	require.NoError(t, err)
	got, err := f.svc.GetVolumeBackupConfig(f.ctx, unplaced.ID)
	require.NoError(t, err)
	require.Contains(t, got.IneligibleReason, "not placed on a server")
}

func TestVolumeBackupStartsOnlyOnAnOnlineServerAndKeepsTheRepositoryAfterTheFirstRun(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)

	_, err := f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.CreateVolumeBackup(f.ctx, app.ID)
	status, code := apiCode(t, err)
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeBackup))
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(b, snap('a'))
	v, err := f.svc.GetVolumeBackupConfig(f.ctx, app.ID)
	require.NoError(t, err)
	require.True(t, v.RepoReady, "the first backup created the repository")

	list, err := f.svc.ListVolumeBackups(f.ctx, app.ID, 0)
	require.NoError(t, err)
	require.Len(t, list, 1, "a zero limit asks for the default page")

	// A backup whose server is gone cannot have its snapshot forgotten.
	_, err = f.pool.Exec(bg, "UPDATE volume_backups SET server_id = NULL WHERE id = $1", b.ID)
	require.NoError(t, err)
	err = f.svc.DeleteVolumeBackup(f.ctx, b.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "server_unavailable", code)
	_, err = f.pool.Exec(bg, "UPDATE volume_backups SET server_id = $2 WHERE id = $1", b.ID, f.server.ID)
	require.NoError(t, err)

	// …nor can one while the server is offline.
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	err = f.svc.DeleteVolumeBackup(f.ctx, b.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "server_offline", code)
	got, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.False(t, got.ObjectDeleted, "nothing was marked deleted")
}

func TestVolumeRestoreRefusalsStartNothing(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	app, b := f.succeededVolumeBackup()
	restore := func(in VolumeRestoreInput) (int, string) {
		_, err := f.svc.RestoreVolumeBackup(f.ctx, b.ID, in)
		return apiCode(t, err)
	}
	started := func() int { return f.countCmds(agentprotocol.KindVolumeRestore) }

	status, code := restore(VolumeRestoreInput{Volume: "data", Mode: "teleport"})
	require.Equal(t, 422, status)
	require.Equal(t, "validation_failed", code)
	status, _ = restore(VolumeRestoreInput{Volume: "nope"})
	require.Equal(t, 422, status, "the service has no such volume")

	// The service has the volume now but the backup does not contain it.
	_, err := f.q.InsertVolume(bg, db.InsertVolumeParams{ServiceID: app, Name: "logs", MountPath: "/logs"})
	require.NoError(t, err)
	status, code = restore(VolumeRestoreInput{Volume: "logs"})
	require.Equal(t, 422, status)
	require.Equal(t, "validation_failed", code)

	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	status, code = restore(VolumeRestoreInput{Volume: "data"})
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	// A backup in a directory of another server's disk only restores there.
	_, err = f.pool.Exec(bg, "UPDATE backup_destinations SET kind = 'local', local_dir = 'nightly', bucket = '', endpoint = ''")
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE volume_backups SET server_id = NULL WHERE id = $1", b.ID)
	require.NoError(t, err)
	status, code = restore(VolumeRestoreInput{Volume: "data"})
	require.Equal(t, 422, status)
	require.Equal(t, "local_backup_server", code)
	require.Zero(t, started())

	// A restore of the service that is still running blocks another one.
	_, err = f.pool.Exec(bg, "UPDATE volume_backups SET server_id = $2 WHERE id = $1", b.ID, f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE backup_destinations SET kind = 's3', local_dir = '', bucket = 'backups', endpoint = 'https://s3.example.com'")
	require.NoError(t, err)
	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	status, code = restore(VolumeRestoreInput{Volume: "data"})
	require.Equal(t, 409, status)
	require.Equal(t, "backup_in_progress", code)
	require.Equal(t, 1, started())

	// In place needs a service that was deployed.
	f2 := newFixture(t)
	_, b2 := f2.succeededVolumeBackup()
	_, err = f2.pool.Exec(bg, "UPDATE services SET current_deployment_id = NULL")
	require.NoError(t, err)
	_, err = f2.svc.RestoreVolumeBackup(f2.ctx, b2.ID, VolumeRestoreInput{Volume: "data", Mode: VolumeRestoreInPlace, ConfirmSlug: "web"})
	_, code = apiCode(t, err)
	require.Equal(t, "service_not_deployed", code)
}

func TestVolumeResultsRecordWhatTheAgentReportedAndKeepRestoredDataReachable(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	appID, b := f.succeededVolumeBackup()
	var cmdID uuid.UUID

	// A backup the agent skipped (the service stayed busy) says so; a failure without a message gets a default; no snapshot id is a failure.
	start := func() VolumeBackupView {
		v, err := f.svc.CreateVolumeBackup(f.ctx, appID)
		require.NoError(t, err)
		require.NoError(t, f.pool.QueryRow(bg, "SELECT command_id FROM volume_backups WHERE id = $1", v.ID).Scan(&cmdID))
		return v
	}
	result := func(outcome, msg string, data agentprotocol.VolumeBackupResult) {
		require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, outcome, msg, data)))
	}
	get := func(id uuid.UUID) VolumeBackupView {
		v, err := f.svc.GetVolumeBackup(f.ctx, id)
		require.NoError(t, err)
		return v
	}
	v := start()
	result(agentprotocol.OutcomeSkipped, "", agentprotocol.VolumeBackupResult{Outcome: agentprotocol.OutcomeSkipped})
	require.Equal(t, StatusSkipped, get(v.ID).Status)
	require.Contains(t, get(v.ID).Error, "service stayed busy")
	v = start()
	result(agentprotocol.OutcomeFailed, "", agentprotocol.VolumeBackupResult{Outcome: agentprotocol.OutcomeFailed})
	require.Equal(t, StatusFailed, get(v.ID).Status)
	require.Equal(t, "the agent reported a failed backup", get(v.ID).Error)
	v = start()
	result(agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeBackupResult{Outcome: agentprotocol.OutcomeSucceeded})
	require.Equal(t, StatusFailed, get(v.ID).Status)
	require.Contains(t, get(v.ID).Error, "did not report a snapshot id")
	// A result for a backup that is already finished changes nothing.
	before := get(v.ID)
	result(agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeBackupResult{Outcome: agentprotocol.OutcomeSucceeded, SnapshotID: snap('c')})
	require.Equal(t, before.Status, get(v.ID).Status)

	// A restore into a new volume whose volume was removed meanwhile keeps the data as a retired volume.
	rv, err := f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(bg, "SELECT command_id FROM volume_restores WHERE id = $1", rv.ID).Scan(&cmdID))
	_, err = f.pool.Exec(bg, "DELETE FROM volumes WHERE service_id = $1", appID)
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeRestoreResult{Outcome: agentprotocol.OutcomeSucceeded, DockerName: "nc-vol-restored"})))
	restores, err := f.svc.ListVolumeRestores(f.ctx, appID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, restores[0].Status)
	require.Contains(t, restores[0].Error, "was removed from the service while the restore ran")
	require.Contains(t, restores[0].Error, "nc-vol-restored")
	retired, err := f.svc.ListRetiredVolumes(f.ctx, appID)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, "nc-vol-restored", retired[0].DockerName)

	// A failed restore without a message gets the default one; an unknown command is ignored.
	f2 := newFixture(t)
	_, b2 := f2.succeededVolumeBackup()
	rv2, err := f2.svc.RestoreVolumeBackup(f2.ctx, b2.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	require.NoError(t, f2.pool.QueryRow(bg, "SELECT command_id FROM volume_restores WHERE id = $1", rv2.ID).Scan(&cmdID))
	require.NoError(t, f2.svc.HandleResult(f2.ctx, cmdID, f2.result(cmdID, agentprotocol.OutcomeFailed, "", agentprotocol.VolumeRestoreResult{})))
	r2, err := f2.svc.ListVolumeRestores(f2.ctx, b2.ServiceID)
	require.NoError(t, err)
	require.Equal(t, "the agent reported a failed restore", r2[0].Error)
	require.NoError(t, f2.svc.HandleResult(f2.ctx, uuid.New(), f2.result(uuid.New(), agentprotocol.OutcomeFailed, "x", nil)))
}

func TestVolumeUndeliveredCommandsFailTheirRowsAndKeepSnapshots(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	appID, b := f.succeededVolumeBackup()
	var cmdID uuid.UUID

	// A restore command that never reached its agent fails the restore.
	rv, err := f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(bg, "SELECT command_id FROM volume_restores WHERE id = $1", rv.ID).Scan(&cmdID))
	require.NoError(t, f.svc.HandleUndelivered(f.ctx, cmdID, "the command expired before the agent came back"))
	restores, err := f.svc.ListVolumeRestores(f.ctx, appID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, restores[0].Status)
	require.Contains(t, restores[0].Error, "expired")

	// A forget command that never arrived leaves the snapshot restorable, with the reason on the row.
	require.NoError(t, f.svc.DeleteVolumeBackup(f.ctx, b.ID))
	_, cmdID = f.lastCmd(agentprotocol.KindVolumeForget)
	require.NoError(t, f.svc.HandleUndelivered(f.ctx, cmdID, "the server stayed offline"))
	got, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.False(t, got.ObjectDeleted, "the snapshot is still there")
	require.Contains(t, got.Error, "stayed offline")

	// A command nobody knows is not an error.
	require.NoError(t, f.svc.HandleUndelivered(f.ctx, uuid.New(), "x"))
}

func TestRecoveryKeyNamesWhereTheRepositoryLivesPerDestinationKind(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	dest := f.s3Destination()
	app := f.app("web", true)

	_, err := f.svc.RevealVolumeBackupRecoveryKey(f.ctx, app.ID)
	_, code := apiCode(t, err)
	require.Equal(t, "volume_backup_unavailable", code, "no configuration yet")

	f.volConfig(app, dest, nil)
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(b, snap('a'))

	// Behind an endpoint the repository URL names that endpoint; without one it is AWS S3 of the region.
	k, err := f.svc.RevealVolumeBackupRecoveryKey(f.ctx, app.ID)
	require.NoError(t, err)
	require.Contains(t, k.Repository, "s3.example.com")
	require.Contains(t, k.Repository, "backups/nebula/prod/volumes/"+app.ID.String())
	_, err = f.pool.Exec(bg, "UPDATE backup_destinations SET endpoint = '', region = 'eu-west-1'")
	require.NoError(t, err)
	k, err = f.svc.RevealVolumeBackupRecoveryKey(f.ctx, app.ID)
	require.NoError(t, err)
	require.Contains(t, k.Repository, "s3.eu-west-1.amazonaws.com")
	_, err = f.pool.Exec(bg, "UPDATE backup_destinations SET region = ''")
	require.NoError(t, err)
	k, err = f.svc.RevealVolumeBackupRecoveryKey(f.ctx, app.ID)
	require.NoError(t, err)
	require.Contains(t, k.Repository, "s3.amazonaws.com")

	// A local directory says where on the server's disk, and warns it is not off-site.
	_, err = f.pool.Exec(bg, "UPDATE backup_destinations SET kind = 'local', local_dir = 'nightly', bucket = ''")
	require.NoError(t, err)
	k, err = f.svc.RevealVolumeBackupRecoveryKey(f.ctx, app.ID)
	require.NoError(t, err)
	require.Contains(t, k.Repository, "<the agent's backup directory>/nightly/")
	require.Contains(t, k.Note, "server's disk")
	require.NotEmpty(t, k.Password)
}

func TestRetiredVolumesStayWhileTheServiceMountsThemOrIsNotPlaced(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	app := f.app("web", true)
	vols, err := f.q.ListServiceVolumes(bg, app.ID)
	require.NoError(t, err)

	// A Docker volume the service mounts again (a discarded swap) must never be removed.
	require.NoError(t, f.q.InsertRetiredVolume(bg, db.InsertRetiredVolumeParams{
		ServiceID: app.ID, VolumeName: "data", DockerName: services.DockerVolumeName(vols[0]), Reason: "test",
	}))
	retired, err := f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	err = f.svc.DeleteRetiredVolume(f.ctx, retired[0].ID)
	status, code := apiCode(t, err)
	require.Equal(t, 409, status)
	require.Equal(t, "volume_in_use", code)
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeRemove))

	// A service that is not placed on a server has nobody to remove it.
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = NULL WHERE id = $1", app.ID)
	require.NoError(t, err)
	err = f.svc.DeleteRetiredVolume(f.ctx, retired[0].ID)
	_, code = apiCode(t, err)
	require.Equal(t, "server_unavailable", code)
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeRemove))
}

func TestVolumeFileErrorsFromTheAgentBecomeSpecificAPIErrors(t *testing.T) {
	f := newFixture(t)
	_, b := f.succeededVolumeBackup()
	fake := &volFakeStreams{}
	f.svc.streams = fake
	for _, tc := range []struct {
		code, msg   string
		status      int
		wantCode    string
		wantMessage string
	}{
		{agentprotocol.FSErrNotFound, "no such path", 404, "not_found", ""},
		{agentprotocol.FSErrNotDir, "a.txt is a file", 422, "not_a_directory", "a.txt is a file"},
		{agentprotocol.FSErrIsDir, "dir is a directory", 422, "is_a_directory", "dir is a directory"},
		{agentprotocol.FSErrTooLarge, "larger than the limit", 413, "file_too_large", "larger than the limit"},
		{agentprotocol.FSErrPermission, "denied", 422, "file_permission_denied", "denied"},
		{agentprotocol.FSErrFailed, "disk error", 422, "file_operation_failed", "disk error"},
	} {
		fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
			fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: req.RequestID, Error: tc.msg, Code: tc.code})
		})
		_, err := f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/x")
		require.Error(t, err, tc.code)
		status, code := apiCode(t, err)
		require.Equal(t, tc.status, status, tc.code)
		require.Equal(t, tc.wantCode, code, tc.code)
		if tc.wantMessage != "" {
			require.Contains(t, err.Error(), tc.wantMessage, tc.code)
		}
	}

	// A backup that is not stored (any more) cannot be browsed.
	_, err := f.pool.Exec(context.Background(), "UPDATE volume_backups SET status = 'failed' WHERE id = $1", b.ID)
	require.NoError(t, err)
	_, err = f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/")
	_, code := apiCode(t, err)
	require.Equal(t, "backup_not_restorable", code)
}
