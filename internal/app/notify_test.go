package app

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

func TestBackupEventMapping(t *testing.T) {
	ws, prj, svc := uuid.New(), uuid.New(), uuid.New()
	base := backups.Event{
		WorkspaceID: ws, ProjectID: prj, ServiceID: svc, ServiceName: "postgres", Destination: "s3-eu", Engine: "postgres",
		Trigger: "schedule", SizeBytes: 5 << 20,
	}
	cases := []struct {
		typ, msg, wantType, wantSeverity, wantTitle string
	}{
		{backups.NotifyBackupSucceeded, "", notifications.TypeBackupSucceeded, notifications.SeverityInfo, "Backup of postgres succeeded"},
		{backups.NotifyRestoreSucceeded, "", notifications.TypeBackupSucceeded, notifications.SeverityInfo, "Restore of postgres succeeded"},
		{backups.NotifyBackupFailed, "bucket not found", notifications.TypeBackupFailed, notifications.SeverityError, "Backup of postgres failed"},
		{backups.NotifyRestoreFailed, "disk full", notifications.TypeBackupFailed, notifications.SeverityError, "Restore of postgres failed"},
		{backups.NotifyBackupSkipped, "the database is stopped", notifications.TypeBackupFailed, notifications.SeverityWarning, "Backup of postgres was skipped"},
		{backups.NotifyBackupMissed, "no backup in 3 days", notifications.TypeBackupMissed, notifications.SeverityError, "No recent backup of postgres"},
	}
	for _, tc := range cases {
		ev := base
		ev.Type, ev.Message = tc.typ, tc.msg
		got := backupEvent(ev)
		require.Equal(t, tc.wantType, got.Type, tc.typ)
		require.Equal(t, tc.wantSeverity, got.Severity, tc.typ)
		require.Equal(t, tc.wantTitle, got.Title, tc.typ)
		require.Equal(t, ws, got.WorkspaceID)
		require.Equal(t, prj, *got.ProjectID)
		require.Equal(t, svc, *got.ServiceID)
		require.Equal(t, "?tab=backups", got.URL)
		require.Equal(t, "s3-eu", got.Fields["Destination"])
		require.Equal(t, "5.0 MiB", got.Fields["Size"])
		if tc.msg != "" {
			require.Equal(t, tc.msg, got.Body)
		}
	}
	// A backup without a project (deleted service) still produces a valid event.
	got := backupEvent(backups.Event{Type: backups.NotifyBackupFailed, WorkspaceID: ws})
	require.Nil(t, got.ProjectID)
	require.Nil(t, got.ServiceID)
	require.Equal(t, "Backup of database failed", got.Title)

	// A missed scheduled backup is its own event, an error, and names the kind of backup.
	missed := backupEvent(backups.Event{Type: backups.NotifyBackupMissed, WorkspaceID: ws, ProjectID: prj, ServiceID: svc, ServiceName: "postgres", Message: "No backup in 3 days."})
	require.Equal(t, notifications.TypeBackupMissed, missed.Type)
	require.Equal(t, notifications.SeverityError, missed.Severity)
	require.Equal(t, "No recent backup of postgres", missed.Title)
	require.Equal(t, "No backup in 3 days.", missed.Body)
	vol := backupEvent(backups.Event{Type: backups.NotifyBackupMissed, WorkspaceID: ws, ServiceName: "files", Volume: true})
	require.Equal(t, "No recent volume backup of files", vol.Title)
	require.Equal(t, "The scheduled backup did not succeed within its window.", vol.Body)
}
