package app

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type recordedEmitter struct {
	mu  sync.Mutex
	evs []notifications.Event
}

func (r *recordedEmitter) Emit(_ context.Context, ev notifications.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func TestByteSizeUsesBinaryUnits(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 1: "1 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 1 << 20: "1.0 MiB", 5 << 30: "5.0 GiB", 3 << 40: "3.0 TiB", 1<<50 + 1<<49: "1.5 PiB",
	} {
		require.Equal(t, want, byteSize(n), "%d", n)
	}
}

func TestFirstNonEmptySkipsBlanks(t *testing.T) {
	require.Equal(t, "b", firstNonEmpty("", "  ", "b", "c"))
	require.Equal(t, " x ", firstNonEmpty(" x "), "the value is returned as written")
	require.Empty(t, firstNonEmpty("", "\t"))
	require.Empty(t, firstNonEmpty())
}

func TestBackupEventCarriesWhereAndHowBig(t *testing.T) {
	ws, prj, svc := uuid.New(), uuid.New(), uuid.New()
	ev := backupEvent(backups.Event{
		Type: backups.NotifyBackupSucceeded, WorkspaceID: ws, ProjectID: prj, ServiceID: svc, ServiceName: "postgres",
		Destination: "s3-eu", Engine: "postgres", Trigger: "schedule", SizeBytes: 3 << 20,
	})
	require.Equal(t, ws, ev.WorkspaceID)
	require.Equal(t, prj, *ev.ProjectID)
	require.Equal(t, svc, *ev.ServiceID)
	require.Equal(t, map[string]string{"Destination": "s3-eu", "Engine": "postgres", "Trigger": "schedule", "Size": "3.0 MiB"}, ev.Fields)
	require.Equal(t, "?tab=backups", ev.URL)

	// The instance's own backup points at the admin page; empty details are not shown.
	inst := backupEvent(backups.Event{Type: backups.NotifyBackupFailed, WorkspaceID: ws, Instance: true})
	require.Equal(t, "/admin/backup", inst.URL)
	require.Empty(t, inst.Fields)
	require.Nil(t, inst.ProjectID)
	require.Nil(t, inst.ServiceID)
	require.Equal(t, "Backup of database failed", inst.Title, "an unnamed service is a database")
	require.Equal(t, "The backup failed.", inst.Body)

	skipped := backupEvent(backups.Event{Type: backups.NotifyBackupSkipped, ServiceName: "db"})
	require.Equal(t, "The scheduled backup did not run.", skipped.Body)
	restore := backupEvent(backups.Event{Type: backups.NotifyRestoreFailed, ServiceName: "db"})
	require.Equal(t, "The restore failed.", restore.Body)
	missed := backupEvent(backups.Event{Type: backups.NotifyBackupMissed, ServiceName: "db"})
	require.Equal(t, "The scheduled backup did not succeed within its window.", missed.Body)
}

func TestBackupNotifierEmitsTheMappedEvent(t *testing.T) {
	em := &recordedEmitter{}
	backupNotifier(em).Notify(context.Background(), backups.Event{Type: backups.NotifyBackupFailed, WorkspaceID: uuid.New(), ServiceName: "db", Message: "bucket gone"})
	require.Len(t, em.evs, 1)
	require.Equal(t, notifications.TypeBackupFailed, em.evs[0].Type)
	require.Equal(t, "bucket gone", em.evs[0].Body)
}

func TestReplicaNotifierMapsLagAndBreakage(t *testing.T) {
	em := &recordedEmitter{}
	notify := replicaNotifier(em)
	ws, prj, svc := uuid.New(), uuid.New(), uuid.New()

	notify(context.Background(), databases.ReplicaEvent{
		Type: databases.NotifyReplicaLagging, WorkspaceID: ws, ProjectID: prj, ServiceID: svc, ServiceName: "pg-replica", Primary: "pg", Message: "30s behind",
	})
	notify(context.Background(), databases.ReplicaEvent{Type: databases.NotifyReplicaBroken, WorkspaceID: ws, Primary: "pg"})
	require.Len(t, em.evs, 2)
	lag, broken := em.evs[0], em.evs[1]
	require.Equal(t, notifications.TypeDatabaseReplicaLagging, lag.Type)
	require.Equal(t, notifications.SeverityWarning, lag.Severity)
	require.Equal(t, "Read replica pg-replica is lagging", lag.Title)
	require.Equal(t, "30s behind", lag.Body)
	require.Equal(t, map[string]string{"Primary": "pg"}, lag.Fields)
	require.Equal(t, prj, *lag.ProjectID)
	require.Equal(t, svc, *lag.ServiceID)
	require.Equal(t, "?tab=database", lag.URL)
	require.Equal(t, notifications.TypeDatabaseReplicaBroken, broken.Type)
	require.Equal(t, notifications.SeverityError, broken.Severity)
	require.Equal(t, "Read replica read replica is broken", broken.Title, "an unnamed replica gets a generic name")
	require.Equal(t, "The replica is not streaming from its primary.", broken.Body)
	require.Nil(t, broken.ProjectID)
	require.Nil(t, broken.ServiceID)
}

// A handler with no services wired proves the routing: commands that carry no
// secrets are answered by the kind alone, without calling any service.
func TestAgentHandlerAnswersSecretFreeCommandsWithoutAService(t *testing.T) {
	h := &agentHandler{}
	for _, kind := range []string{
		agentprotocol.KindDockerCleanup, agentprotocol.KindHostHardening, agentprotocol.KindAgentUpgrade,
		agentprotocol.KindHostPatching, agentprotocol.KindUpdateControlPlane,
	} {
		secrets, err := h.SecretsForCommand(context.Background(), uuid.New(), kind, []byte(`{}`))
		require.NoError(t, err, kind)
		require.Nil(t, secrets, kind)
	}
}
