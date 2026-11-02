package backups

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

func TestDumpRestoreRefusalsStartNothing(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	create := func(in DumpRestoreInput) (int, string) {
		_, err := f.svc.CreateDumpRestore(f.ctx, sv.ID, in)
		return apiCode(t, err)
	}
	none := func(t *testing.T) {
		t.Helper()
		require.Zero(t, f.count("SELECT count(*) FROM dump_restores"))
		require.Empty(t, f.dep.deploys)
	}

	status, code := create(DumpRestoreInput{Mode: "sideways"})
	require.Equal(t, 422, status)
	require.Equal(t, "validation_failed", code)
	status, _ = create(DumpRestoreInput{Name: strings.Repeat("n", 81)})
	require.Equal(t, 422, status)

	// A target server that does not exist, or never came online.
	missing := uuid.New()
	status, code = create(DumpRestoreInput{ServerID: &missing})
	require.Equal(t, 422, status)
	require.Equal(t, "server_not_found", code)
	srv2, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "agent2"})
	require.NoError(t, err)
	status, code = create(DumpRestoreInput{ServerID: &srv2.ID})
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)

	// A database that is not placed anywhere needs a server named.
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = NULL WHERE id = $1", sv.ID)
	require.NoError(t, err)
	status, code = create(DumpRestoreInput{})
	require.Equal(t, 422, status)
	require.Equal(t, "no_server", code)
	_, err = f.pool.Exec(bg, "UPDATE services SET server_id = $2 WHERE id = $1", sv.ID, f.server.ID)
	require.NoError(t, err)
	none(t)

	// In place: offline server, stopped database, no destination for the safety backup, read replica.
	inPlace := DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug}
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	status, code = create(inPlace)
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	status, code = create(inPlace)
	require.Equal(t, 409, status)
	require.Equal(t, "database_not_running", code)
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'running' WHERE id = $1", sv.ID)
	require.NoError(t, err)
	status, code = create(inPlace)
	require.Equal(t, 422, status)
	require.Equal(t, "destination_required", code)
	_, err = f.pool.Exec(bg, `UPDATE services SET config = jsonb_set(config, '{database,replica}', '{"primaryServiceId":"`+uuid.NewString()+`"}') WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	status, code = create(inPlace)
	require.Equal(t, 409, status)
	require.Equal(t, "replica_read_only", code)
	none(t)
}

// An in-place restore takes its safety backup at the destination of the
// database's schedule when none is named.
func TestDumpRestoreInPlaceNeedsADestinationForTheSafetyBackup(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	dest := f.s3Destination()
	_, err := f.svc.CreateDumpRestore(f.ctx, sv.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug, DestinationID: new(uuid.New())})
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status, "a destination of someone else's is not found")

	_, err = f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@daily"})
	require.NoError(t, err)
	rv, err := f.svc.CreateDumpRestore(f.ctx, sv.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: sv.Slug, FileName: "x.dump"})
	require.NoError(t, err)
	var destID uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT destination_id FROM backups WHERE id = (SELECT pre_backup_id FROM dump_restores WHERE id = $1)", rv.ID).Scan(&destID))
	require.Equal(t, dest.ID, destID, "the schedule's destination takes the safety backup")
}

func TestDumpUploadIsRefusedUntilTheTargetIsReadyAndRunning(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.dumpAgent()
	src := f.database("pg")

	// The target is still being provisioned.
	f.dep.status = "queued"
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Name: "pg-wait"})
	require.NoError(t, err)
	_, err = f.svc.UploadDump(f.ctx, rv.ID, up(pgDump))
	require.Equal(t, "dump_restore_not_ready", dumpCode(t, err))
	f.dep.status = "succeeded"

	aw, sib := f.awaitingDump(src)
	_, err = f.svc.UploadDump(f.ctx, aw.ID, DumpUpload{FileName: "x", Size: 0, Body: strings.NewReader("")})
	status, code := apiCode(t, err)
	require.Equal(t, 411, status)
	require.Equal(t, "length_required", code)

	// No stream support at all, an offline server and a stopped database all keep the file from being sent.
	streams := f.svc.streams
	f.svc.streams = nil
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "server_offline", dumpCode(t, err))
	f.svc.streams = streams
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "server_offline", dumpCode(t, err))
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'stopped' WHERE id = $1", sib.ID)
	require.NoError(t, err)
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "database_not_running", dumpCode(t, err))
	got, err := f.svc.GetDumpRestore(f.ctx, aw.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreAwaitingUpload, got.Status, "a refused upload leaves the restore waiting for a good one")

	// A restore that was uploaded cannot be canceled, and a finished one is over.
	_, err = f.pool.Exec(bg, "UPDATE services SET desired_state = 'running' WHERE id = $1", sib.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE dump_restores SET status = 'restoring' WHERE id = $1", aw.ID)
	require.NoError(t, err)
	_, err = f.svc.CancelDumpRestore(f.ctx, aw.ID)
	require.Equal(t, "dump_restore_running", dumpCode(t, err))
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "dump_restore_in_progress", dumpCode(t, err))
	_, err = f.pool.Exec(bg, "UPDATE dump_restores SET status = 'succeeded' WHERE id = $1", aw.ID)
	require.NoError(t, err)
	_, err = f.svc.CancelDumpRestore(f.ctx, aw.ID)
	require.Equal(t, "dump_restore_finished", dumpCode(t, err))
}

func TestDumpRestoreListsAndReadsAreScopedToTheDatabase(t *testing.T) {
	f := newFixture(t)
	a := f.database("pg-a")
	b := f.database("pg-b")
	ra, err := f.svc.CreateDumpRestore(f.ctx, a.ID, DumpRestoreInput{Name: "copy-a", FileName: "a.dump"})
	require.NoError(t, err)
	_, err = f.svc.CreateDumpRestore(f.ctx, b.ID, DumpRestoreInput{Name: "copy-b", FileName: "b.dump"})
	require.NoError(t, err)

	list, err := f.svc.ListDumpRestores(f.ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, ra.ID, list[0].ID)
	_, err = f.svc.ListDumpRestores(f.ctx, uuid.New())
	status, _ := apiCode(t, err)
	require.Equal(t, 404, status)
	_, err = f.svc.GetDumpRestore(f.ctx, uuid.New())
	status, _ = apiCode(t, err)
	require.Equal(t, 404, status)
}
