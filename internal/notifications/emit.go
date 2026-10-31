package notifications

import (
	"context"

	"github.com/google/uuid"
)

// Event types. The catalogue (catalog.go) lists their labels, groups and
// defaults; a rule subscribes to a subset of them.
const (
	TypeDeploySucceeded      = "deploy.succeeded"
	TypeDeployFailed         = "deploy.failed"
	TypeDeployRolledBack     = "deploy.rolled_back"
	TypeServerOffline        = "server.offline"
	TypeServerOnline         = "server.online"
	TypeServerDiskAlert      = "server.disk_alert"
	TypeServerCleanup        = "server.cleanup"
	TypeServerRebootRequired = "server.reboot_required"
	TypeBackupFailed         = "backup.failed"
	TypeBackupSucceeded      = "backup.succeeded"
	TypeTaskFailed           = "task.failed"
	// Read replicas of a PostgreSQL database (internal/databases).
	TypeDatabaseReplicaBroken  = "database.replica.broken"
	TypeDatabaseReplicaLagging = "database.replica.lagging"
	TypePreviewCreated         = "preview.created"
	TypePreviewDeleted         = "preview.deleted"
	TypeScaleBlocked           = "scale.blocked"
	TypeCertExpiring           = "cert.expiring"
	TypeDNSSyncFailed          = "dns.sync_failed"
	// Cloudflare origin lock: the published IP ranges changed, or could not be
	// fetched for a day.
	TypeCloudflareIPsChanged = "cloudflare.ips_changed"
	TypeCloudflareIPsStale   = "cloudflare.ips_stale"
	TypeMaintenanceOn        = "maintenance.on"
	TypeMaintenanceOff       = "maintenance.off"
)

// Service health (internal/svcalerts) and missed backups (internal/backups).
// service.recovered closes an incident announced by one of the first three; it
// is sent with the severity of the alert it closes, so a channel's minimum
// severity keeps alert and recovery together.
const (
	TypeServiceCrashed     = "service.crashed"
	TypeServiceUnhealthy   = "service.unhealthy"
	TypeServiceRestartLoop = "service.restart_loop"
	TypeServiceRecovered   = "service.recovered"
	TypeBackupMissed       = "backup.missed"
)

// Built-in backup storage (internal/instsvc): a team's slice of the instance S3
// crossed 80% of its quota, or is full. Each is sent once per crossing.
const (
	TypeStorageQuotaWarning = "storage.quota_warning"
	TypeStorageQuotaFull    = "storage.quota_full"
)

// Probe-based uptime (internal/uptime). uptime.down is sent when a service
// fails its checks, uptime.up when it passes again. Like service.recovered,
// uptime.up carries the severity of the alert it closes.
const (
	TypeUptimeDown = "uptime.down"
	TypeUptimeUp   = "uptime.up"
)

// Severities, lowest first. A rule's minimum severity filters on them.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Event is something worth telling a human about. Callers fill what they know;
// Emit looks up the project, environment, service and server names and builds
// the link when URL is empty.
type Event struct {
	Type      string
	ProjectID *uuid.UUID
	ServerID  *uuid.UUID
	ServiceID *uuid.UUID
	// Severity is info, warning or error (empty: the type's default).
	Severity string
	// Title is one line ("api failed to deploy"); Body is a short sentence or
	// two. Neither may contain secrets: both are sent to third parties.
	Title, Body string
	// URL is a path inside the app ("/servers/<id>") or an absolute URL. Empty:
	// derived from ServiceID, ProjectID or ServerID.
	URL string
	// Fields are extra key facts shown as a table ("Commit": "ab12cd3").
	// Emit adds Project, Environment, Service and Server when it knows them.
	Fields map[string]string
	// WorkspaceID is optional: derived from the project, server or principal.
	WorkspaceID uuid.UUID
}

// Emitter is what domains depend on. A nil Emitter is valid and drops events,
// so use Emit instead of calling the method directly.
type Emitter interface {
	Emit(ctx context.Context, ev Event)
}

// Emit sends ev through e; a nil e is a no-op. It never fails and never blocks
// on a channel: delivery is asynchronous with retries.
func Emit(ctx context.Context, e Emitter, ev Event) {
	if e == nil {
		return
	}
	e.Emit(ctx, ev)
}
