package backups

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// app inserts a deployed ordinary service with one volume on the fixture server.
func (f *fixture) app(slug string, withVolume bool) db.Service {
	f.t.Helper()
	bg := context.Background()
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	if withVolume {
		_, err = f.q.InsertVolume(bg, db.InsertVolumeParams{ServiceID: sv.ID, Name: "data", MountPath: "/data"})
		require.NoError(f.t, err)
	}
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	sv, err = f.getService(sv.ID)
	require.NoError(f.t, err)
	return sv
}

// volConfig saves a configuration for sv writing to dest.
func (f *fixture) volConfig(sv db.Service, dest DestinationView, mut func(*VolumeBackupConfigInput)) VolumeBackupConfigView {
	f.t.Helper()
	in := VolumeBackupConfigInput{DestinationID: dest.ID}
	if mut != nil {
		mut(&in)
	}
	v, err := f.svc.PutVolumeBackupConfig(f.ctx, sv.ID, in)
	require.NoError(f.t, err)
	return v
}

func (f *fixture) lastCmd(kind string) (agents.NewCommand, uuid.UUID) {
	f.t.Helper()
	for i := len(f.disp.cmds) - 1; i >= 0; i-- {
		if f.disp.cmds[i].Kind == kind {
			return f.disp.cmds[i], f.disp.ids[i]
		}
	}
	f.t.Fatalf("no %s command was enqueued", kind)
	return agents.NewCommand{}, uuid.Nil
}

func (f *fixture) countCmds(kind string) int {
	n := 0
	for _, c := range f.disp.cmds {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

func snap(c byte) string { return strings.Repeat(string(c), 64) }

// finishBackup reports a successful volume_backup result for the backup.
func (f *fixture) finishBackup(b VolumeBackupView, snapshot string) {
	f.t.Helper()
	var cmdID uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT command_id FROM volume_backups WHERE id = $1", b.ID).Scan(&cmdID))
	require.NoError(f.t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeBackupResult{
		Outcome: agentprotocol.OutcomeSucceeded, SnapshotID: snapshot, Consistency: agentprotocol.VolModeFreeze, StageMethod: agentprotocol.StageReflink,
		FreezeMs: 420, StageMs: 380, BytesProcessed: 5000, BytesAdded: 1200, FilesNew: 3, FilesChanged: 1, FilesUnchanged: 6,
		HookOutcome: "ok", Warnings: []string{"a file changed while reading"},
	})))
}

func apiCode(t *testing.T, err error) (int, string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Status, ae.Code
}

func TestVolumeConfigEligibilityAndDefaults(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	pg := f.database("pg")
	noVol := f.app("empty", false)
	app := f.app("web", true)

	// Managed databases and services without volumes are ineligible.
	v, err := f.svc.GetVolumeBackupConfig(f.ctx, pg.ID)
	require.NoError(t, err)
	require.False(t, v.Eligible)
	require.Contains(t, v.IneligibleReason, "database backups")
	v, err = f.svc.GetVolumeBackupConfig(f.ctx, noVol.ID)
	require.NoError(t, err)
	require.False(t, v.Eligible)
	require.Contains(t, v.IneligibleReason, "no volumes")
	for _, id := range []uuid.UUID{pg.ID, noVol.ID} {
		_, err = f.svc.PutVolumeBackupConfig(f.ctx, id, VolumeBackupConfigInput{DestinationID: dest.ID})
		status, code := apiCode(t, err)
		require.Equal(t, 422, status)
		require.Equal(t, "volume_backup_unavailable", code)
		_, err = f.svc.CreateVolumeBackup(f.ctx, id)
		status, code = apiCode(t, err)
		require.Equal(t, 422, status)
		require.Equal(t, "volume_backup_unavailable", code)
	}

	// Defaults for an eligible service without a configuration.
	v, err = f.svc.GetVolumeBackupConfig(f.ctx, app.ID)
	require.NoError(t, err)
	require.True(t, v.Eligible)
	require.False(t, v.Configured)
	require.Equal(t, agentprotocol.VolModeFreeze, v.Mode)
	require.Equal(t, 5, v.FreezeBudgetSeconds)
	require.Equal(t, []int{7, 4, 6}, []int{v.KeepDaily, v.KeepWeekly, v.KeepMonthly})
	require.Len(t, v.Volumes, 1)
	_, err = f.svc.CreateVolumeBackup(f.ctx, app.ID)
	_, code := apiCode(t, err)
	require.Equal(t, "volume_backup_unavailable", code, "a backup needs a saved configuration")

	// Saving: validation, then the schedule.
	for _, bad := range []VolumeBackupConfigInput{
		{DestinationID: uuid.New()},
		{DestinationID: dest.ID, Cron: "not a cron", Enabled: true},
		{DestinationID: dest.ID, Enabled: true},
		{DestinationID: dest.ID, PreHook: &VolumeBackupHook{Command: []string{""}}},
		{DestinationID: dest.ID, Mode: "weird"},
		{DestinationID: dest.ID, Cron: "* * * * *"},
	} {
		_, err := f.svc.PutVolumeBackupConfig(f.ctx, app.ID, bad)
		status, _ := apiCode(t, err)
		require.Equal(t, 422, status, "%+v", bad)
	}
	zero := 0
	v = f.volConfig(app, dest, func(in *VolumeBackupConfigInput) {
		in.Mode = agentprotocol.VolModeStop
		in.PreHook = &VolumeBackupHook{Command: []string{"sh", "-c", "sqlite3 /data/a.db .backup /data/b.db"}, ContinueOnError: true}
		in.Excludes = []string{" cache/** ", ""}
		in.KeepDaily, in.KeepWeekly = &zero, &zero
		in.Cron, in.Timezone, in.Enabled = "30 3 * * *", "Europe/Berlin", true
	})
	require.True(t, v.Configured)
	require.Equal(t, agentprotocol.VolModeStop, v.Mode)
	require.Equal(t, 300, v.PreHook.TimeoutSeconds, "the default hook timeout is stored")
	require.Equal(t, []string{"cache/**"}, v.Excludes)
	require.Equal(t, []int{0, 0, 6}, []int{v.KeepDaily, v.KeepWeekly, v.KeepMonthly})
	require.NotNil(t, v.NextRunAt)
	require.True(t, v.NextRunAt.After(time.Now()))
	require.Equal(t, "minio", v.DestinationName)
	require.False(t, v.RepoReady, "the repository is created by the first backup")

	// Turning the schedule off clears the next run; deleting removes the config.
	v = f.volConfig(app, dest, nil)
	require.Nil(t, v.NextRunAt)
	require.False(t, v.Enabled)
	require.NoError(t, f.svc.DeleteVolumeBackupConfig(f.ctx, app.ID))
	v, err = f.svc.GetVolumeBackupConfig(f.ctx, app.ID)
	require.NoError(t, err)
	require.False(t, v.Configured)
}

func TestVolumeBackupPayloadHasNoSecretsButSecretsForCommandDoes(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, func(in *VolumeBackupConfigInput) {
		in.PostHook = &VolumeBackupHook{Command: []string{"touch", "/data/.done"}}
	})

	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, b.Status)
	require.Equal(t, []string{"data"}, b.Volumes)

	cmd, _ := f.lastCmd(agentprotocol.KindVolumeBackup)
	require.Equal(t, "volume_backup:"+b.ID.String(), cmd.IdempotencyKey)
	var p agentprotocol.VolumeBackupPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, "nebula/prod/volumes/"+app.ID.String(), p.Repo.Path)
	require.Equal(t, "backups", p.Repo.Destination.Bucket)
	require.Equal(t, agentprotocol.VolModeFreeze, p.Mode)
	require.Equal(t, 5, p.FreezeBudgetSeconds)
	require.Equal(t, []agentprotocol.VolumeRef{{Name: "data", DockerName: agentprotocol.VolumeName(app.ID.String(), "data"), MountPath: "/data"}}, p.Volumes)
	require.Equal(t, []string{"touch", "/data/.done"}, p.PostHook.Command)
	require.Equal(t, TriggerManual, p.Trigger)
	require.NoError(t, p.Repo.Validate())

	// The secrets exist in the dispatcher's view only.
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, canaryAccess, secrets[agentprotocol.SecretBackupAccessKey])
	require.Equal(t, canarySecret, secrets[agentprotocol.SecretBackupSecretKey])
	password := secrets[agentprotocol.SecretResticPassword]
	require.GreaterOrEqual(t, len(password), 40, "32 random bytes, base64url")
	for _, canary := range []string{canaryAccess, canarySecret, password} {
		require.NotContains(t, string(cmd.Payload), canary)
		for _, q := range []string{
			"SELECT string_agg(t::text, ' ') FROM volume_backups t", "SELECT string_agg(t::text, ' ') FROM volume_backup_configs t",
			"SELECT string_agg(t::text, ' ') FROM volume_repos t", "SELECT string_agg(t::text, ' ') FROM agent_commands t",
		} {
			var dump *string
			require.NoError(t, f.pool.QueryRow(context.Background(), q).Scan(&dump))
			if dump != nil {
				require.NotContains(t, *dump, canary, q)
			}
		}
	}
	// The password is stable: a second backup reuses the repository.
	f.finishBackup(b, snap('a'))
	_, err = f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	cmd2, _ := f.lastCmd(agentprotocol.KindVolumeBackup)
	secrets2, err := f.svc.SecretsForCommand(f.ctx, cmd2.Kind, cmd2.Payload)
	require.NoError(t, err)
	require.Equal(t, password, secrets2[agentprotocol.SecretResticPassword])

	// A failure message is scrubbed of the secrets before it is stored.
	var second uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT command_id FROM volume_backups ORDER BY created_at DESC LIMIT 1").Scan(&second))
	require.NoError(t, f.svc.HandleResult(f.ctx, second, f.result(second, agentprotocol.OutcomeFailed, "restic: password "+password+" key "+canarySecret+" rejected", nil)))
	list, err := f.svc.ListVolumeBackups(f.ctx, app.ID, 10)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, list[0].Status)
	require.NotContains(t, list[0].Error, password)
	require.NotContains(t, list[0].Error, canarySecret)
	require.Contains(t, list[0].Error, "••••••")

	rk, err := f.svc.RevealVolumeBackupRecoveryKey(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, password, rk.Password)
	require.Equal(t, "s3:https://s3.example.com/backups/nebula/prod/volumes/"+app.ID.String(), rk.Repository)
}

func TestVolumeBackupResultFillsTheRowAndNotifies(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(b, snap('a'))

	got, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, got.Status)
	require.Equal(t, snap('a'), got.SnapshotID)
	require.Equal(t, agentprotocol.VolModeFreeze, got.Consistency)
	require.EqualValues(t, 5000, got.SizeBytes)
	require.EqualValues(t, 1200, got.BytesAdded)
	require.EqualValues(t, 10, got.Files)
	require.Equal(t, 420, got.FreezeMs)
	require.Equal(t, 380, got.StageMs)
	require.Equal(t, agentprotocol.StageReflink, got.StageMethod)
	require.Equal(t, "ok", got.HookOutcome)
	require.Equal(t, []string{"a file changed while reading"}, got.Warnings)
	require.True(t, got.Restorable)
	require.NotNil(t, got.FinishedAt)
	require.Len(t, f.events, 1)
	require.Equal(t, NotifyBackupSucceeded, f.events[0].Type)
	require.Equal(t, "volume files", f.events[0].Engine)

	// A duplicate result changes nothing.
	f.finishBackup(b, snap('b'))
	got, _ = f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.Equal(t, snap('a'), got.SnapshotID)
	require.Len(t, f.events, 1)
}

func TestVolumeBackupSkippedFailedAndExpired(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)

	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	_, cmdID := f.lastCmd(agentprotocol.KindVolumeBackup)
	// The agent reports a skipped backup (a deployment kept the service busy).
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeBackupResult{
		Outcome: agentprotocol.OutcomeSkipped, Message: "The service stayed busy for 120 seconds.",
	})))
	got, _ := f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.Equal(t, StatusSkipped, got.Status)
	require.Contains(t, got.Error, "busy")
	require.False(t, got.Restorable)
	require.Equal(t, NotifyBackupSkipped, f.events[len(f.events)-1].Type)

	b2, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	_, cmd2 := f.lastCmd(agentprotocol.KindVolumeBackup)
	require.NoError(t, f.svc.HandleUndelivered(f.ctx, cmd2, "Not delivered: server offline. No changes were made."))
	got, _ = f.svc.GetVolumeBackup(f.ctx, b2.ID)
	require.Equal(t, StatusExpired, got.Status)

	b3, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	_, cmd3 := f.lastCmd(agentprotocol.KindVolumeBackup)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmd3, f.result(cmd3, agentprotocol.OutcomeFailed, "freeze budget exceeded", nil)))
	got, _ = f.svc.GetVolumeBackup(f.ctx, b3.ID)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "freeze budget exceeded", got.Error)
	require.Equal(t, NotifyBackupFailed, f.events[len(f.events)-1].Type)

	// Stale queued backups are failed by the tick.
	b4, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE volume_backups SET created_at = now() - interval '8 hours' WHERE id = $1", b4.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.VolumeTick(f.ctx))
	got, _ = f.svc.GetVolumeBackup(f.ctx, b4.ID)
	require.Equal(t, StatusFailed, got.Status)
	require.Contains(t, got.Error, "No result was received")
}

func TestBuildForgetPayload(t *testing.T) {
	svcID := uuid.New()
	repo := agentprotocol.VolumeRepo{Path: "volumes/x"}
	now := time.Now()
	stored := []db.VolumeBackup{
		{SnapshotID: snap('a'), Locked: true},
		{SnapshotID: snap('b')},
		{SnapshotID: snap('c'), Locked: true, ObjectDeletedAt: &now},
		{SnapshotID: "", Locked: true},
	}
	p, ok := buildForgetPayload(svcID, repo, db.VolumeBackupConfig{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}, stored)
	require.True(t, ok)
	require.Equal(t, &agentprotocol.VolumeKeep{Daily: 7, Weekly: 4, Monthly: 6}, p.Keep)
	require.Equal(t, []string{snap('a')}, p.Protect, "only locked, non-deleted snapshots are protected")
	require.Empty(t, p.SnapshotIDs, "retention never removes explicit ids")

	_, ok = buildForgetPayload(svcID, repo, db.VolumeBackupConfig{}, stored)
	require.False(t, ok, "all limits 0 keeps everything")
}

func TestVolumeRetentionRunsAfterSuccessAndMarksRemoved(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)

	old, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(old, snap('a'))
	require.Equal(t, 1, f.countCmds(agentprotocol.KindVolumeForget))
	_, err = f.svc.SetVolumeBackupLocked(f.ctx, old.ID, true)
	require.NoError(t, err)
	mid, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(mid, snap('b'))
	cur, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(cur, snap('c'))

	cmd, forgetID := f.lastCmd(agentprotocol.KindVolumeForget)
	require.Equal(t, 3, f.countCmds(agentprotocol.KindVolumeForget))
	var p agentprotocol.VolumeForgetPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, &agentprotocol.VolumeKeep{Daily: 7, Weekly: 4, Monthly: 6}, p.Keep)
	require.Equal(t, []string{snap('a')}, p.Protect, "the locked backup is protected")
	require.Empty(t, p.SnapshotIDs)
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.NotEmpty(t, secrets[agentprotocol.SecretResticPassword])

	// The agent reports which snapshots it forgot.
	require.NoError(t, f.svc.HandleResult(f.ctx, forgetID, f.result(forgetID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeForgetResult{
		Outcome: agentprotocol.OutcomeSucceeded, Removed: []string{snap('b')},
	})))
	list, err := f.svc.ListVolumeBackups(f.ctx, app.ID, 10)
	require.NoError(t, err)
	byID := map[uuid.UUID]VolumeBackupView{}
	for _, v := range list {
		byID[v.ID] = v
	}
	require.True(t, byID[mid.ID].ObjectDeleted)
	require.False(t, byID[mid.ID].Restorable)
	require.False(t, byID[old.ID].ObjectDeleted, "locked backups stay")
	require.False(t, byID[cur.ID].ObjectDeleted)
}

func TestDeleteVolumeBackupFlow(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)

	err = f.svc.DeleteVolumeBackup(f.ctx, b.ID)
	_, code := apiCode(t, err)
	require.Equal(t, "backup_in_progress", code)
	f.finishBackup(b, snap('a'))

	_, err = f.svc.SetVolumeBackupLocked(f.ctx, b.ID, true)
	require.NoError(t, err)
	err = f.svc.DeleteVolumeBackup(f.ctx, b.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "backup_locked", code)
	_, err = f.svc.SetVolumeBackupLocked(f.ctx, b.ID, false)
	require.NoError(t, err)

	before := f.countCmds(agentprotocol.KindVolumeForget)
	require.NoError(t, f.svc.DeleteVolumeBackup(f.ctx, b.ID))
	require.Equal(t, before+1, f.countCmds(agentprotocol.KindVolumeForget))
	cmd, cmdID := f.lastCmd(agentprotocol.KindVolumeForget)
	var p agentprotocol.VolumeForgetPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, []string{snap('a')}, p.SnapshotIDs)
	require.Nil(t, p.Keep)
	got, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.True(t, got.ObjectDeleted, "marked while the agent works")

	// A failure brings the backup back; a success removes the row.
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "repository locked", nil)))
	got, err = f.svc.GetVolumeBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.False(t, got.ObjectDeleted)
	require.Contains(t, got.Error, "repository locked")

	require.NoError(t, f.svc.DeleteVolumeBackup(f.ctx, b.ID))
	_, cmdID = f.lastCmd(agentprotocol.KindVolumeForget)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeForgetResult{
		Outcome: agentprotocol.OutcomeSucceeded, Removed: []string{snap('a')},
	})))
	_, err = f.svc.GetVolumeBackup(f.ctx, b.ID)
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status)

	// A destination used by volume backups can not be deleted.
	err = f.svc.DeleteDestination(f.ctx, dest.ID)
	_, code = apiCode(t, err)
	require.Equal(t, "destination_in_use", code)
}

func TestRestoreNewVolumeStagesTheSwap(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)

	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	_, code := apiCode(t, err)
	require.Equal(t, "backup_not_restorable", code, "a running backup can not be restored")
	f.finishBackup(b, snap('a'))

	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "nope"})
	status, _ := apiCode(t, err)
	require.Equal(t, 422, status)

	genBefore := f.generation(app.ID)
	r, err := f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	require.Equal(t, VolumeRestoreNewVolume, r.Mode)
	require.Equal(t, RestoreRestoring, r.Status)
	require.False(t, r.SwapStaged)
	require.Contains(t, r.DockerName, agentprotocol.VolumeName(app.ID.String(), "data")+"-r")
	require.Equal(t, genBefore, f.generation(app.ID), "nothing changes before the agent confirmed")

	cmd, cmdID := f.lastCmd(agentprotocol.KindVolumeRestore)
	var p agentprotocol.VolumeRestorePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.False(t, p.InPlace)
	require.Equal(t, snap('a'), p.SnapshotID)
	require.Equal(t, "data", p.Volume.Name)
	require.Equal(t, r.DockerName, p.Volume.DockerName)
	require.Equal(t, r.ID.String(), p.RestoreID)
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.NotEmpty(t, secrets[agentprotocol.SecretResticPassword])
	require.NotContains(t, string(cmd.Payload), secrets[agentprotocol.SecretResticPassword])

	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeRestoreResult{
		Outcome: agentprotocol.OutcomeSucceeded, DockerName: r.DockerName,
	})))

	// The row points at the restored volume, the old one is retired, the change is staged.
	vols, err := f.q.ListServiceVolumes(context.Background(), app.ID)
	require.NoError(t, err)
	require.Equal(t, r.DockerName, vols[0].DockerName)
	require.True(t, strings.HasPrefix(vols[0].RestoreLabel, "restored from "), vols[0].RestoreLabel)
	require.Equal(t, r.DockerName, services.DockerVolumeName(vols[0]))
	require.Greater(t, f.generation(app.ID), genBefore)
	retired, err := f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.NoError(t, err)
	require.Len(t, retired, 1)
	require.Equal(t, agentprotocol.VolumeName(app.ID.String(), "data"), retired[0].DockerName)
	require.Equal(t, "data", retired[0].VolumeName)
	list, err := f.svc.ListVolumeRestores(f.ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreSucceeded, list[0].Status)
	require.True(t, list[0].SwapStaged)
	require.Equal(t, NotifyRestoreSucceeded, f.events[len(f.events)-1].Type)

	// Pending changes show the swap against the release that still mounts the old volume.
	cfg := services.DecodeConfig(app.Config)
	snapJSON, _ := json.Marshal(services.Snapshot{Config: cfg, Volumes: []services.SnapshotVolume{{Name: "data", MountPath: "/data"}}})
	changes, err := services.DiffRelease(context.Background(), f.q, f.box, db.Release{Config: snapJSON}, services.CurrentState{Config: cfg, Volumes: vols})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "volumes.data", changes[0].Field)
	require.Equal(t, "/data", changes[0].From)
	require.Equal(t, "/data · "+vols[0].RestoreLabel, changes[0].To)

	// A second restore retires the first restored volume too (the name carries the second).
	f.svc.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	r2, err := f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	_, cmd2 := f.lastCmd(agentprotocol.KindVolumeRestore)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmd2, f.result(cmd2, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeRestoreResult{
		Outcome: agentprotocol.OutcomeSucceeded, DockerName: r2.DockerName,
	})))
	retired, _ = f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.Len(t, retired, 2)
}

func (f *fixture) generation(id uuid.UUID) int64 {
	f.t.Helper()
	sv, err := f.getService(id)
	require.NoError(f.t, err)
	return sv.Generation
}

func TestRestoreNewVolumeFailureChangesNothing(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(b, snap('a'))
	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	require.NoError(t, err)
	_, cmdID := f.lastCmd(agentprotocol.KindVolumeRestore)
	gen := f.generation(app.ID)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "no space left", nil)))
	list, _ := f.svc.ListVolumeRestores(f.ctx, app.ID)
	require.Equal(t, RestoreFailed, list[0].Status)
	require.Equal(t, "no space left", list[0].Error)
	require.Equal(t, gen, f.generation(app.ID))
	vols, _ := f.q.ListServiceVolumes(context.Background(), app.ID)
	require.Empty(t, vols[0].DockerName)
	retired, _ := f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.Empty(t, retired)
}

func TestRestoreInPlaceNeedsSlugAndWaitsForTheSafetyBackup(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, func(in *VolumeBackupConfigInput) { in.Mode = agentprotocol.VolModeStop })
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(t, err)
	f.finishBackup(b, snap('a'))

	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data", Mode: VolumeRestoreInPlace})
	status, code := apiCode(t, err)
	require.Equal(t, 422, status)
	require.Equal(t, "confirmation_required", code)
	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data", Mode: VolumeRestoreInPlace, ConfirmSlug: "other"})
	_, code = apiCode(t, err)
	require.Equal(t, "confirmation_required", code)
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeRestore))

	r, err := f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data", Mode: VolumeRestoreInPlace, ConfirmSlug: "web"})
	require.NoError(t, err)
	require.Equal(t, RestorePreBackup, r.Status)
	require.NotNil(t, r.PreBackupID)
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeRestore), "no restore before the safety backup succeeded")
	pre, preCmd := f.lastCmd(agentprotocol.KindVolumeBackup)
	var pp agentprotocol.VolumeBackupPayload
	require.NoError(t, json.Unmarshal(pre.Payload, &pp))
	require.Equal(t, TriggerPreRestore, pp.Trigger)
	require.Equal(t, agentprotocol.VolModeStop, pp.Mode, "the safety backup uses the configured mode")

	// Another restore of the service has to wait.
	_, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data"})
	_, code = apiCode(t, err)
	require.Equal(t, "backup_in_progress", code)

	// The safety backup fails: nothing is restored, the restore fails.
	require.NoError(t, f.svc.HandleResult(f.ctx, preCmd, f.result(preCmd, agentprotocol.OutcomeFailed, "disk full", nil)))
	list, _ := f.svc.ListVolumeRestores(f.ctx, app.ID)
	require.Equal(t, RestoreFailed, list[0].Status)
	require.Contains(t, list[0].Error, "nothing was changed")
	require.Contains(t, list[0].Error, "disk full")
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeRestore))

	// Second attempt: the safety backup succeeds, then the restore command goes out.
	r, err = f.svc.RestoreVolumeBackup(f.ctx, b.ID, VolumeRestoreInput{Volume: "data", Mode: VolumeRestoreInPlace, ConfirmSlug: "web"})
	require.NoError(t, err)
	_, preCmd = f.lastCmd(agentprotocol.KindVolumeBackup)
	require.NoError(t, f.svc.HandleResult(f.ctx, preCmd, f.result(preCmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeBackupResult{
		Outcome: agentprotocol.OutcomeSucceeded, SnapshotID: snap('p'), Consistency: agentprotocol.VolModeStop,
	})))
	cmd, restoreCmd := f.lastCmd(agentprotocol.KindVolumeRestore)
	var rp agentprotocol.VolumeRestorePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &rp))
	require.True(t, rp.InPlace)
	require.Equal(t, snap('a'), rp.SnapshotID)
	require.Equal(t, agentprotocol.VolumeName(app.ID.String(), "data"), rp.Volume.DockerName)
	list, _ = f.svc.ListVolumeRestores(f.ctx, app.ID)
	require.Equal(t, RestoreRestoring, list[0].Status)

	gen := f.generation(app.ID)
	require.NoError(t, f.svc.HandleResult(f.ctx, restoreCmd, f.result(restoreCmd, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeRestoreResult{Outcome: agentprotocol.OutcomeSucceeded})))
	list, _ = f.svc.ListVolumeRestores(f.ctx, app.ID)
	require.Equal(t, RestoreSucceeded, list[0].Status)
	require.False(t, list[0].SwapStaged, "an in-place restore stages nothing")
	require.Equal(t, gen, f.generation(app.ID))
	retired, _ := f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.Empty(t, retired)
}

func TestVolumeTickStartsDueConfigsAndSkipsOfflineServers(t *testing.T) {
	f := newFixture(t)
	dest := f.s3Destination()
	online := f.app("web", true)
	f.volConfig(online, dest, func(in *VolumeBackupConfigInput) { in.Cron, in.Enabled = "30 3 * * *", true })
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	offline := f.app("api", true)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET server_id = $2 WHERE id = $1", offline.ID, other.ID)
	require.NoError(t, err)
	f.volConfig(offline, dest, func(in *VolumeBackupConfigInput) { in.Cron, in.Enabled = "30 3 * * *", true })
	noVolume := f.app("worker", true)
	f.volConfig(noVolume, dest, func(in *VolumeBackupConfigInput) { in.Cron, in.Enabled = "30 3 * * *", true })
	_, err = f.pool.Exec(context.Background(), "DELETE FROM volumes WHERE service_id = $1", noVolume.ID)
	require.NoError(t, err)

	// Nothing is due yet.
	require.NoError(t, f.svc.VolumeTick(f.ctx))
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeBackup))

	_, err = f.pool.Exec(context.Background(), "UPDATE volume_backup_configs SET next_run_at = now() - interval '1 minute'")
	require.NoError(t, err)
	require.NoError(t, f.svc.VolumeTick(f.ctx))

	require.Equal(t, 1, f.countCmds(agentprotocol.KindVolumeBackup))
	cmd, _ := f.lastCmd(agentprotocol.KindVolumeBackup)
	var p agentprotocol.VolumeBackupPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, online.ID.String(), p.ServiceID)
	require.Equal(t, TriggerSchedule, p.Trigger)

	started, err := f.svc.ListVolumeBackups(f.ctx, online.ID, 10)
	require.NoError(t, err)
	require.Len(t, started, 1)
	require.Equal(t, StatusQueued, started[0].Status)
	require.Equal(t, "schedule", started[0].Actor)
	skipped, err := f.svc.ListVolumeBackups(f.ctx, offline.ID, 10)
	require.NoError(t, err)
	require.Len(t, skipped, 1)
	require.Equal(t, StatusSkipped, skipped[0].Status)
	require.Contains(t, skipped[0].Error, "offline")
	noVol, err := f.svc.ListVolumeBackups(f.ctx, noVolume.ID, 10)
	require.NoError(t, err)
	require.Len(t, noVol, 1)
	require.Equal(t, StatusSkipped, noVol[0].Status)
	require.Contains(t, noVol[0].Error, "no volumes")
	var skips int
	for _, ev := range f.events {
		if ev.Type == NotifyBackupSkipped {
			skips++
		}
	}
	require.Equal(t, 2, skips)

	// The next run moved forward before anything started: a second tick does nothing.
	var due int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM volume_backup_configs WHERE next_run_at <= now()").Scan(&due))
	require.Zero(t, due)
	require.NoError(t, f.svc.VolumeTick(f.ctx))
	require.Equal(t, 1, f.countCmds(agentprotocol.KindVolumeBackup))
}

func TestRetiredVolumeRemoveFlow(t *testing.T) {
	f := newFixture(t)
	app := f.app("web", true)
	require.NoError(t, f.q.InsertRetiredVolume(context.Background(), db.InsertRetiredVolumeParams{
		ServiceID: app.ID, VolumeName: "data", DockerName: "nc-vol-old-data", Reason: "test",
	}))
	retired, err := f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.NoError(t, err)
	require.Len(t, retired, 1)

	// Offline server: 409, nothing enqueued.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	err = f.svc.DeleteRetiredVolume(f.ctx, retired[0].ID)
	status, code := apiCode(t, err)
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeRemove))
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	require.NoError(t, f.svc.DeleteRetiredVolume(f.ctx, retired[0].ID))
	cmd, cmdID := f.lastCmd(agentprotocol.KindVolumeRemove)
	var p agentprotocol.VolumeRemovePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, []string{"nc-vol-old-data"}, p.Names)
	require.Equal(t, []string{retired[0].ID.String()}, p.RetiredIDs)
	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Empty(t, secrets)
	retired, _ = f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.Len(t, retired, 1, "the row stays until the agent confirmed")

	// A failure keeps the row; a success removes it.
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "volume in use", nil)))
	retired, _ = f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.Len(t, retired, 1)
	require.NoError(t, f.svc.DeleteRetiredVolume(f.ctx, retired[0].ID))
	_, cmdID = f.lastCmd(agentprotocol.KindVolumeRemove)
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.VolumeRemoveResult{Outcome: agentprotocol.OutcomeSucceeded})))
	retired, _ = f.svc.ListRetiredVolumes(f.ctx, app.ID)
	require.Empty(t, retired)
}

func TestHandlesVolumeKinds(t *testing.T) {
	for _, k := range []string{agentprotocol.KindVolumeBackup, agentprotocol.KindVolumeForget, agentprotocol.KindVolumeRestore, agentprotocol.KindVolumeRemove} {
		require.True(t, Handles(k), k)
	}
}

// Deleting a service with its volumes removes the restored volume the row
// mounts and the retired ones, not the derived name of a swapped row.
func TestServiceDeleteRemovesRestoredAndRetiredVolumes(t *testing.T) {
	f := newFixture(t)
	app := f.app("web", true)
	bg := context.Background()
	vols, err := f.q.ListServiceVolumes(bg, app.ID)
	require.NoError(t, err)
	derived := agentprotocol.VolumeName(app.ID.String(), "data")
	restored := derived + "-r260112033000"
	require.NoError(t, f.q.SetVolumeDockerName(bg, db.SetVolumeDockerNameParams{ID: vols[0].ID, DockerName: restored, RestoreLabel: "restored from Jan 12, 03:30"}))
	require.NoError(t, f.q.InsertRetiredVolume(bg, db.InsertRetiredVolumeParams{ServiceID: app.ID, VolumeName: "data", DockerName: derived, Reason: "replaced"}))

	svcs := services.NewService(f.pool, nil, f.box, f.disp, f.svc.log)
	require.NoError(t, svcs.Delete(f.ctx, app.ID, services.DeleteInput{Confirm: "web", DeleteVolumes: true}))
	cmd, _ := f.lastCmd(agentprotocol.KindRemoveService)
	var p agentprotocol.ServiceActionPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.True(t, p.DeleteVolumes)
	require.ElementsMatch(t, []string{restored, derived}, p.VolumeNames)
}
