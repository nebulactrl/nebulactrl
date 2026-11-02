package backups

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func isSnooze(err error) bool {
	var sn *rivertype.JobSnoozeError
	return errors.As(err, &sn)
}

// A new database that is still deploying keeps the restore job waiting; once
// the deployment ended the job sends the restore (or fails it) and finishes.
func TestAdvanceRestoreWaitsForTheNewDatabaseAndFailsWhenItNeverComes(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	src := f.database("pg")
	b := f.succeededBackup(src, mustDest(t, f, f.s3Destination().ID), nil, time.Hour, "nebula/prod/pg.dump")
	w := &advanceWorker{s: f.svc}
	work := func(id uuid.UUID) error {
		return w.Work(bg, &river.Job[RestoreAdvanceArgs]{Args: RestoreAdvanceArgs{RestoreID: id}})
	}
	start := func(name string) RestoreView {
		f.dep.status = "queued"
		rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Name: name})
		require.NoError(t, err)
		return rv
	}
	status := func(id uuid.UUID) RestoreView {
		v, err := f.svc.GetRestore(f.ctx, id)
		require.NoError(t, err)
		return v
	}

	// A restore that does not exist is finished: there is nothing to wait for.
	require.NoError(t, work(uuid.New()))

	// Still deploying: the job asks to be run again later and the restore stays provisioning.
	rv := start("copy-wait")
	err := work(rv.ID)
	require.True(t, isSnooze(err), "want a snooze, got %v", err)
	require.Equal(t, RestoreProvisioning, status(rv.ID).Status)
	require.Zero(t, f.countCmds(agentprotocol.KindDBRestore))

	// The deployment failed: the restore fails with the deployment's own message.
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'failed', status_message = 'image pull failed' WHERE service_id = $1", *rv.TargetServiceID)
	require.NoError(t, err)
	require.NoError(t, work(rv.ID))
	got := status(rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "could not be deployed")
	require.Contains(t, got.Error, "image pull failed")
	f.lastEvent(NotifyRestoreFailed)
	require.NoError(t, work(rv.ID), "a finished restore is left alone")
	require.Zero(t, f.countCmds(agentprotocol.KindDBRestore))

	// A deployment that vanished fails the restore too.
	rv = start("copy-gone")
	_, err = f.pool.Exec(bg, "UPDATE backup_restores SET deployment_id = $2 WHERE id = $1", rv.ID, uuid.New())
	require.NoError(t, err)
	done, err := f.svc.AdvanceRestore(bg, rv.ID)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, RestoreFailed, status(rv.ID).Status)
	require.Contains(t, status(rv.ID).Error, "no longer exists")

	// A new database that does not come up within the limit fails the restore.
	rv = start("copy-slow")
	f.svc.now = func() time.Time { return time.Now().Add(restoreTimeout + time.Minute) }
	require.NoError(t, work(rv.ID))
	got = status(rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "30 minutes")
	f.svc.now = time.Now

	// A deployment that succeeded starts the restore on the agent.
	rv = start("copy-ok")
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE service_id = $1", *rv.TargetServiceID)
	require.NoError(t, err)
	require.NoError(t, work(rv.ID))
	require.Equal(t, RestoreRestoring, status(rv.ID).Status)
	require.Equal(t, 1, f.countCmds(agentprotocol.KindDBRestore))
}

func TestDumpAdvanceAndSweepJobsFailWhatCanNoLongerProgress(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	src := f.database("pg")
	advance := &dumpAdvanceWorker{s: f.svc}
	sweep := &dumpSweepWorker{s: f.svc}
	work := func(id uuid.UUID) error {
		return advance.Work(bg, &river.Job[DumpRestoreAdvanceArgs]{Args: DumpRestoreAdvanceArgs{RestoreID: id}})
	}
	get := func(id uuid.UUID) DumpRestoreView {
		v, err := f.svc.GetDumpRestore(f.ctx, id)
		require.NoError(t, err)
		return v
	}
	require.NoError(t, work(uuid.New()))

	f.dep.status = "queued"
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Name: "pg-wait"})
	require.NoError(t, err)
	require.True(t, isSnooze(work(rv.ID)), "still deploying")
	require.Equal(t, RestoreProvisioning, get(rv.ID).Status)

	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'failed', status_message = 'no space left' WHERE service_id = $1", *rv.TargetServiceID)
	require.NoError(t, err)
	require.NoError(t, work(rv.ID))
	got := get(rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "no space left")

	// A restore that waited for its file for too long, and one whose uploading process is gone, are failed by the sweep.
	f.dep.status = "succeeded"
	aw, _ := f.awaitingDump(f.database("pg2"))
	f.svc.now = func() time.Time { return time.Now().Add(dumpAwaitTimeout + dumpOrphanAfter + time.Minute) }
	require.NoError(t, sweep.Work(bg, &river.Job[DumpRestoreSweepArgs]{}))
	f.svc.now = time.Now
	got = get(aw.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "No file was uploaded")
}

func TestBackupTickJobStartsDueSchedules(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: f.s3Destination().ID, Cron: "@hourly"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE backup_schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1", sch.ID)
	require.NoError(t, err)

	require.NoError(t, (&tickWorker{s: f.svc}).Work(bg, &river.Job[BackupTickArgs]{}))
	list, err := f.svc.ListBackups(f.ctx, sv.ID, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, TriggerSchedule, list[0].Trigger)
}

func TestVolumeTickJobSkipsAnOfflineServerWithTheReason(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, func(in *VolumeBackupConfigInput) { in.Cron, in.Enabled = "@hourly", true })
	_, err := f.pool.Exec(bg, "UPDATE volume_backup_configs SET next_run_at = now() - interval '1 minute' WHERE service_id = $1", app.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	require.NoError(t, (&volumeTickWorker{s: f.svc}).Work(bg, &river.Job[VolumeBackupTickArgs]{}))
	list, err := f.svc.ListVolumeBackups(f.ctx, app.ID, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, StatusSkipped, list[0].Status)
	require.Contains(t, list[0].Error, "offline")
	require.Zero(t, f.countCmds(agentprotocol.KindVolumeBackup), "an offline server is sent nothing")
}

func TestInstanceBackupJobsRunTheQueuedBackupAndTheSchedule(t *testing.T) {
	e := newInstEnv(t, (&fakeTools{}).tools())
	bg := context.Background()
	e.enableLocal(t, 5)

	v, err := e.svc.RunNow(e.ctx)
	require.NoError(t, err)
	run := &instanceRunWorker{s: e.svc}
	require.Equal(t, instanceRunTimeout, run.Timeout(nil))
	require.NoError(t, run.Work(bg, &river.Job[InstanceBackupRunArgs]{Args: InstanceBackupRunArgs{BackupID: v.ID}}))
	row, err := e.svc.q.GetInstanceBackup(e.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, row.Status)

	// A failing dump is recorded on its row: the job itself succeeds, so River does not retry it.
	failing := &fakeTools{failed: errors.New("pg_dump: connection refused")}
	e2 := newInstEnv(t, failing.tools())
	e2.enableLocal(t, 5)
	v2, err := e2.svc.RunNow(e2.ctx)
	require.NoError(t, err)
	require.NoError(t, (&instanceRunWorker{s: e2.svc}).Work(bg, &river.Job[InstanceBackupRunArgs]{Args: InstanceBackupRunArgs{BackupID: v2.ID}}))
	row, err = e2.svc.q.GetInstanceBackup(e2.ctx, v2.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, row.Status)
	require.Contains(t, row.Error, "connection refused")

	// The tick starts the scheduled backup once it is due.
	e3 := newInstEnv(t, (&fakeTools{}).tools())
	e3.enableLocal(t, 5)
	_, err = e3.pool.Exec(bg, "UPDATE instance_backup_settings SET next_run_at = now() - interval '1 minute'")
	require.NoError(t, err)
	require.NoError(t, (&instanceTickWorker{s: e3.svc}).Work(bg, &river.Job[InstanceBackupTickArgs]{}))
	runs, err := e3.svc.ListRuns(e3.ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "schedule", runs[0].Trigger)
}
