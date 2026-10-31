package backups

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Event types passed to a Notifier.
const (
	NotifyBackupSucceeded  = "backup.succeeded"
	NotifyBackupFailed     = "backup.failed"
	NotifyBackupSkipped    = "backup.skipped"
	NotifyRestoreSucceeded = "restore.succeeded"
	NotifyRestoreFailed    = "restore.failed"
	// NotifyBackupMissed: a schedule produced no successful backup within its window.
	NotifyBackupMissed = "backup.missed"
)

// Event describes a finished backup or restore for notification channels.
// It never carries credentials or object contents.
type Event struct {
	Type        string
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	ProjectName string
	Environment string
	ServiceID   uuid.UUID
	ServiceName string
	BackupID    uuid.UUID
	RestoreID   uuid.UUID
	Trigger     string
	Destination string
	Engine      string
	SizeBytes   int64
	Message     string // the error or skip reason; empty on success
	// Volume marks a volume backup (a restic snapshot) rather than a database dump.
	Volume bool
	// Instance marks a backup of the control plane's own database (no project or service).
	Instance bool
	At       time.Time
}

// Notifier receives backup and restore outcomes and delivers them to the
// notification channels (email, Slack, ...). Without a Notifier events are
// dropped. Notify must not block for long and must not fail the caller.
type Notifier interface {
	Notify(ctx context.Context, ev Event)
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(ctx context.Context, ev Event)

// Notify implements Notifier.
func (f NotifierFunc) Notify(ctx context.Context, ev Event) { f(ctx, ev) }
