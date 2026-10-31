package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/meshlinks"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// backupNotifier adapts the notification service to the backups hook. It lives
// here because notifications must not import backups (backups imports the
// domains that emit notifications).
//
// Event mapping: a failed backup or restore is backup.failed (error); a skipped
// scheduled backup is backup.failed too, as a warning ("nothing ran"); a
// finished backup or restore is backup.succeeded.
func backupNotifier(n notifications.Emitter) backups.Notifier {
	return backups.NotifierFunc(func(ctx context.Context, ev backups.Event) {
		notifications.Emit(ctx, n, backupEvent(ev))
	})
}

func backupEvent(ev backups.Event) notifications.Event {
	out := notifications.Event{WorkspaceID: ev.WorkspaceID, URL: "?tab=backups", Fields: map[string]string{}}
	if ev.Instance {
		out.URL = "/admin/backup"
	}
	if ev.ProjectID != uuid.Nil {
		out.ProjectID = &ev.ProjectID
	}
	if ev.ServiceID != uuid.Nil {
		out.ServiceID = &ev.ServiceID
	}
	name := ev.ServiceName
	if name == "" {
		name = "database"
	}
	set := func(k, v string) {
		if v != "" {
			out.Fields[k] = v
		}
	}
	set("Destination", ev.Destination)
	set("Engine", ev.Engine)
	set("Trigger", ev.Trigger)
	if ev.SizeBytes > 0 {
		out.Fields["Size"] = byteSize(ev.SizeBytes)
	}
	switch ev.Type {
	case backups.NotifyBackupSucceeded:
		out.Type, out.Severity = notifications.TypeBackupSucceeded, notifications.SeverityInfo
		out.Title, out.Body = "Backup of "+name+" succeeded", "The backup was written to the destination."
	case backups.NotifyRestoreSucceeded:
		out.Type, out.Severity = notifications.TypeBackupSucceeded, notifications.SeverityInfo
		out.Title, out.Body = "Restore of "+name+" succeeded", "The backup was restored."
	case backups.NotifyBackupSkipped:
		out.Type, out.Severity = notifications.TypeBackupFailed, notifications.SeverityWarning
		out.Title, out.Body = "Backup of "+name+" was skipped", firstNonEmpty(ev.Message, "The scheduled backup did not run.")
	case backups.NotifyBackupMissed:
		out.Type, out.Severity = notifications.TypeBackupMissed, notifications.SeverityError
		what := "backup"
		if ev.Volume {
			what = "volume backup"
		}
		out.Title, out.Body = "No recent "+what+" of "+name, firstNonEmpty(ev.Message, "The scheduled backup did not succeed within its window.")
	case backups.NotifyRestoreFailed:
		out.Type, out.Severity = notifications.TypeBackupFailed, notifications.SeverityError
		out.Title, out.Body = "Restore of "+name+" failed", firstNonEmpty(ev.Message, "The restore failed.")
	default: // backups.NotifyBackupFailed
		out.Type, out.Severity = notifications.TypeBackupFailed, notifications.SeverityError
		out.Title, out.Body = "Backup of "+name+" failed", firstNonEmpty(ev.Message, "The backup failed.")
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}

// replicaNotifier adapts the notification service to the replica alerts of
// internal/databases (database.replica.lagging and database.replica.broken).
func replicaNotifier(n notifications.Emitter) databases.ReplicaNotifier {
	return func(ctx context.Context, ev databases.ReplicaEvent) {
		out := notifications.Event{
			WorkspaceID: ev.WorkspaceID, URL: "?tab=database", Fields: map[string]string{"Primary": ev.Primary},
		}
		if ev.ProjectID != uuid.Nil {
			out.ProjectID = &ev.ProjectID
		}
		if ev.ServiceID != uuid.Nil {
			out.ServiceID = &ev.ServiceID
		}
		name := firstNonEmpty(ev.ServiceName, "read replica")
		if ev.Type == databases.NotifyReplicaLagging {
			out.Type, out.Severity = notifications.TypeDatabaseReplicaLagging, notifications.SeverityWarning
			out.Title, out.Body = "Read replica "+name+" is lagging", firstNonEmpty(ev.Message, "The replica is far behind its primary.")
		} else {
			out.Type, out.Severity = notifications.TypeDatabaseReplicaBroken, notifications.SeverityError
			out.Title, out.Body = "Read replica "+name+" is broken", firstNonEmpty(ev.Message, "The replica is not streaming from its primary.")
		}
		notifications.Emit(ctx, n, out)
	}
}

// meshLinkReconciler adapts the cross-server link domain to the Promote saga:
// an explicit "add" reconciliation for the repointed consumers. A refused add
// (mesh not enabled on a server, ...) is an error, so Promote reports the links
// as pending.
type meshLinkReconciler struct{ s *meshlinks.Service }

func (m meshLinkReconciler) ReconcileAdd(ctx context.Context, envID uuid.UUID, consumers []uuid.UUID) error {
	res, err := m.s.ReconcileEnv(ctx, envID, meshlinks.ModeAdd, meshlinks.ForConsumers(consumers...))
	if err != nil {
		return err
	}
	if len(res.Blockers) > 0 {
		return errors.New(res.Blockers[0].Message)
	}
	return nil
}
