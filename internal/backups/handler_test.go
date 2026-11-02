package backups

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestResultsOfUnknownOrForeignCommandsChangeNothing(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	b := f.succeededBackup(sv, dest, nil, time.Hour, "k/x")

	require.NoError(t, f.svc.HandleResult(bg, uuid.New(), f.result(uuid.New(), agentprotocol.OutcomeFailed, "boom", nil)))
	require.NoError(t, f.svc.HandleUndelivered(bg, uuid.New(), "expired"))

	// A deploy command is not a backup command.
	cmdID := uuid.New()
	_, err := f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: cmdID, ServerID: f.server.ID, Kind: agentprotocol.KindDeploy, Payload: []byte(`{}`), IdempotencyKey: "deploy-1", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(bg, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "boom", nil)))
	require.NoError(t, f.svc.HandleUndelivered(bg, cmdID, "expired"))
	got, err := f.svc.GetBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, got.Status)
	require.Empty(t, f.events())
}

func TestAFailedRestoreResultFailsTheRestoreWithoutLeakingCredentials(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	b := f.succeededBackup(sv, mustDest(t, f, f.s3Destination().ID), nil, time.Hour, "k/x")

	start := func(name string) (RestoreView, uuid.UUID) {
		rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Name: name})
		require.NoError(t, err)
		done, err := f.svc.AdvanceRestore(bg, rv.ID)
		require.NoError(t, err)
		require.True(t, done)
		_, id := f.disp.last()
		return rv, id
	}
	rv, cmdID := start("copy-a")
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "pg_restore: connection to "+canarySecret+" refused", nil)))
	got, err := f.svc.GetRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "connection to ••••••")
	require.NotContains(t, got.Error, canarySecret)
	require.NotNil(t, got.FinishedAt)
	f.lastEvent(NotifyRestoreFailed)

	rv, cmdID = start("copy-b")
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "", nil)))
	got, err = f.svc.GetRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, "the agent reported a failed restore", got.Error)

	// A result that arrives twice does not reopen or re-notify a finished restore.
	events := len(f.events())
	require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", agentprotocol.DBRestoreResult{Outcome: "succeeded"})))
	got, err = f.svc.GetRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, got.Status)
	require.Len(t, f.events(), events)
}

func TestUndeliveredBackupCommandsRecordWhyAndKeepWhatWasStored(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	destV := f.s3Destination()
	dest := mustDest(t, f, destV.ID)
	b := f.succeededBackup(sv, dest, nil, time.Hour, "k/x")

	// A restore whose command never arrived fails with the reason.
	rv, err := f.svc.Restore(f.ctx, b.ID, RestoreInput{Name: "copy"})
	require.NoError(t, err)
	done, err := f.svc.AdvanceRestore(bg, rv.ID)
	require.NoError(t, err)
	require.True(t, done)
	_, cmdID := f.disp.last()
	require.NoError(t, f.svc.HandleUndelivered(bg, cmdID, "the server stayed offline"))
	got, err := f.svc.GetRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, got.Status)
	require.Equal(t, "the server stayed offline", got.Error)

	// A destination test that never arrived is a failed test, with the reason.
	payload, _ := json.Marshal(agentprotocol.BackupTestPayload{Destination: agentprotocol.BackupDestination{ID: destV.ID.String()}})
	testID := uuid.New()
	_, err = f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: testID, ServerID: f.server.ID, Kind: agentprotocol.KindBackupTest, Payload: payload, IdempotencyKey: "t-undelivered", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleUndelivered(bg, testID, "the command expired"))
	d, err := f.svc.GetDestination(f.ctx, destV.ID)
	require.NoError(t, err)
	require.NotNil(t, d.LastTestOK)
	require.False(t, *d.LastTestOK)
	require.Equal(t, "the command expired", d.LastTestMessage)

	// A deletion that never arrived leaves the object restorable and says why.
	require.NoError(t, f.svc.DeleteBackup(f.ctx, b.ID))
	_, delID := f.disp.last()
	require.NoError(t, f.svc.HandleUndelivered(bg, delID, "the server stayed offline"))
	v, err := f.svc.GetBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.False(t, v.ObjectDeleted)
	require.True(t, v.Restorable)
	require.Contains(t, v.Error, "Deleting the stored object failed: the server stayed offline")

	// A result that cannot be matched to a destination is ignored, not guessed at.
	badID := uuid.New()
	_, err = f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: badID, ServerID: f.server.ID, Kind: agentprotocol.KindBackupTest, Payload: []byte(`{"destination":{"id":"nope"}}`), IdempotencyKey: "t-bad", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(bg, badID, f.result(badID, agentprotocol.OutcomeSucceeded, "", nil)))
	require.NoError(t, f.svc.HandleUndelivered(bg, badID, "x"))
}

func TestAScheduledBackupThatSucceedsAppliesTheSchedulesRetention(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	dest := mustDest(t, f, f.s3Destination().ID)
	sch, err := f.svc.CreateSchedule(f.ctx, sv.ID, ScheduleInput{DestinationID: dest.ID, Cron: "@hourly", RetentionCount: new(1)})
	require.NoError(t, err)
	f.succeededBackup(sv, dest, &sch.ID, 48*time.Hour, "k/old")
	_, err = f.pool.Exec(bg, "UPDATE backup_schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1", sch.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Tick(bg))
	cmd, cmdID := f.disp.last()
	require.Equal(t, agentprotocol.KindDBBackup, cmd.Kind)
	require.Zero(t, f.countCmds(agentprotocol.KindBackupDelete), "nothing is pruned while the backup runs")

	data := agentprotocol.DBBackupResult{Outcome: agentprotocol.OutcomeSucceeded, SizeBytes: 99, SHA256: strings.Repeat("c", 64), Format: agentprotocol.FormatPGCustom, ObjectKey: "k/new", StartedAt: time.Now().Add(-time.Minute)}
	require.NoError(t, f.svc.HandleResult(bg, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", data)))
	f.lastEvent(NotifyBackupSucceeded)
	del, _ := f.disp.last()
	require.Equal(t, agentprotocol.KindBackupDelete, del.Kind, "the older backup beyond the retention goes")
	var p agentprotocol.BackupDeletePayload
	require.NoError(t, json.Unmarshal(del.Payload, &p))
	require.Equal(t, []string{"k/old"}, p.ObjectKeys)

	// The same result again is a duplicate: nothing is notified twice.
	events := len(f.events())
	require.NoError(t, f.svc.HandleResult(bg, cmdID, f.result(cmdID, agentprotocol.OutcomeSucceeded, "", data)))
	require.Len(t, f.events(), events)
}

func TestAFailedBackupWithoutAMessageGetsADefaultAndIsNotified(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	sv := f.database("pg")
	dest := f.s3Destination()
	b, err := f.svc.CreateBackup(f.ctx, sv.ID, dest.ID)
	require.NoError(t, err)
	_, cmdID := f.disp.last()
	require.NoError(t, f.svc.HandleResult(bg, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "", agentprotocol.DBBackupResult{})))
	got, err := f.svc.GetBackup(f.ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "the agent reported a failed backup", got.Error)
	f.lastEvent(NotifyBackupFailed)
}
