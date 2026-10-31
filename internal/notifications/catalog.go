package notifications

import (
	"slices"
	"strings"
)

// Groups of the event matrix, in display order.
const (
	GroupDeployments  = "Deployments"
	GroupServers      = "Servers"
	GroupBackups      = "Backups"
	GroupDatabases    = "Databases"
	GroupTasks        = "Tasks"
	GroupPreviews     = "Previews"
	GroupScaling      = "Scaling"
	GroupCertificates = "Certificates"
	GroupDNS          = "DNS"
	GroupMaintenance  = "Maintenance"
	GroupServices     = "Services"
)

// Tones colour a message: ok (green), info (brand), warn (amber), bad (red).
const (
	toneOK   = "ok"
	toneInfo = "info"
	toneWarn = "warn"
	toneBad  = "bad"
)

// ChannelEventType describes one subscribable event.
type ChannelEventType struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Group       string `json:"group"`
	// Severity is what Emit assumes when the caller sets none.
	Severity string `json:"severity" enum:"info,warning,error"`
	// DefaultOn marks the types a new channel subscribes to: failures and
	// alerts on, successes and informational events off.
	DefaultOn bool `json:"defaultOn"`
	tone      string
}

// catalog lists every event type, in matrix order.
var catalog = []ChannelEventType{
	{Type: TypeDeployFailed, Label: "Deployment failed", Group: GroupDeployments, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A deployment failed or could not be restored to the previous release."},
	{Type: TypeDeployRolledBack, Label: "Deployment rolled back", Group: GroupDeployments, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "A new release did not verify and the previous release was put back."},
	{Type: TypeDeploySucceeded, Label: "Deployment succeeded", Group: GroupDeployments, Severity: SeverityInfo, tone: toneOK,
		Description: "A deployment finished and the service is running the new release."},

	{Type: TypeServerOffline, Label: "Server offline", Group: GroupServers, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A server stopped sending heartbeats. Server events belong to no project, so only workspace-wide channels receive them."},
	{Type: TypeServerDiskAlert, Label: "Server disk almost full", Group: GroupServers, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "A server's disk usage crossed its alert threshold."},
	{Type: TypeServerCleanup, Label: "Docker cleanup", Group: GroupServers, Severity: SeverityInfo, DefaultOn: true, tone: toneInfo,
		Description: "A server's Docker cleanup failed, or freed more than 1 GB (sent as info; failures are errors)."},
	{Type: TypeServerRebootRequired, Label: "Server needs a reboot", Group: GroupServers, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "Installed updates (a new kernel, libc, systemd ...) only take effect after a reboot. Sent once each time a server starts needing one; services keep running until you reboot it."},
	{Type: TypeServerOnline, Label: "Server online", Group: GroupServers, Severity: SeverityInfo, tone: toneOK,
		Description: "A server connected, or came back after being offline."},

	{Type: TypeBackupFailed, Label: "Backup or restore failed", Group: GroupBackups, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A database backup failed, was skipped, or a restore failed."},
	{Type: TypeBackupSucceeded, Label: "Backup or restore succeeded", Group: GroupBackups, Severity: SeverityInfo, tone: toneOK,
		Description: "A database backup or restore completed."},
	{Type: TypeBackupMissed, Label: "Backup missed", Group: GroupBackups, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A scheduled database or volume backup did not succeed within its window: it did not run, or every run since failed. Sent once until the next successful backup."},

	{Type: TypeStorageQuotaWarning, Label: "Built-in backup storage nearly full", Group: GroupBackups, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "The team's built-in backup storage passed 80% of its quota. Sent once per crossing; free space by deleting old backups or ask the instance administrator for a larger quota."},
	{Type: TypeStorageQuotaFull, Label: "Built-in backup storage full", Group: GroupBackups, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "The team's built-in backup storage reached its quota: new backups fail until space is freed. Existing backups are never deleted to make room."},

	{Type: TypeDatabaseReplicaBroken, Label: "Read replica broken", Group: GroupDatabases, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A PostgreSQL read replica stopped streaming, lost its replication slot, or its primary was lost. Replication is manual: nothing is promoted automatically."},
	{Type: TypeDatabaseReplicaLagging, Label: "Read replica lagging", Group: GroupDatabases, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "A PostgreSQL read replica stayed more than 64 MiB or 60 seconds behind its primary for 5 minutes."},

	{Type: TypeTaskFailed, Label: "Task failed", Group: GroupTasks, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A cron job, one-off command or pre-deploy command exited with an error, or a post-deploy command failed (the deployment still succeeds; sent as a warning)."},

	{Type: TypePreviewCreated, Label: "Preview created", Group: GroupPreviews, Severity: SeverityInfo, tone: toneOK,
		Description: "A pull request preview environment was created."},
	{Type: TypePreviewDeleted, Label: "Preview deleted", Group: GroupPreviews, Severity: SeverityInfo, tone: toneInfo,
		Description: "A preview environment was removed (pull request closed, or idle too long)."},

	{Type: TypeScaleBlocked, Label: "Autoscaling blocked", Group: GroupScaling, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "The autoscaler wants more replicas but the server lacks the headroom. Sent once per block."},

	{Type: TypeCertExpiring, Label: "Certificate expiring", Group: GroupCertificates, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "A custom certificate expires within 30 days, within 7 days, or has expired."},

	{Type: TypeDNSSyncFailed, Label: "DNS sync failed", Group: GroupDNS, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "NebulaCtrl could not update a Cloudflare DNS record of a domain, or Cloudflare rejected the API token. Sent once per failure."},

	{Type: TypeCloudflareIPsChanged, Label: "Cloudflare IP ranges changed", Group: GroupDNS, Severity: SeverityInfo, tone: toneInfo,
		Description: "Cloudflare published new IP ranges and origin-locked routes were updated. No redeploy is needed."},
	{Type: TypeCloudflareIPsStale, Label: "Cloudflare IP ranges stale", Group: GroupDNS, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "NebulaCtrl could not fetch Cloudflare's IP ranges for 24 hours. Origin-locked domains keep using the last good list."},

	{Type: TypeMaintenanceOn, Label: "Maintenance on", Group: GroupMaintenance, Severity: SeverityInfo, tone: toneInfo,
		Description: "A service was put into maintenance mode."},
	{Type: TypeMaintenanceOff, Label: "Maintenance off", Group: GroupMaintenance, Severity: SeverityInfo, tone: toneOK,
		Description: "A service left maintenance mode."},

	{Type: TypeServiceCrashed, Label: "Service crashed", Group: GroupServices, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A running service's container exited and stayed down, or was restarted by Docker after a crash. Sent once per incident, with the last log lines."},
	{Type: TypeServiceUnhealthy, Label: "Service unhealthy", Group: GroupServices, Severity: SeverityWarning, DefaultOn: true, tone: toneWarn,
		Description: "A running service failed its health check for longer than the unhealthy grace period (2 minutes by default)."},
	{Type: TypeServiceRestartLoop, Label: "Service restart loop", Group: GroupServices, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A service keeps restarting (3 restarts in 10 minutes by default), or keeps failing again and again. Further alerts for it pause until it has been stable for an hour."},
	{Type: TypeServiceRecovered, Label: "Service recovered", Group: GroupServices, Severity: SeverityInfo, DefaultOn: true, tone: toneOK,
		Description: "A service that was announced as crashed, unhealthy or restart-looping has been healthy again for 2 minutes. Sent with the severity of the alert it closes."},

	{Type: TypeUptimeDown, Label: "Service down (uptime)", Group: GroupServices, Severity: SeverityError, DefaultOn: true, tone: toneBad,
		Description: "A service failed its uptime checks (health probes run from the server every 30 seconds) several times in a row. Sent once per outage, and again as a reminder every few minutes while it lasts if the service's monitor sets a reminder interval; maintenance and deployments are not outages."},
	{Type: TypeUptimeUp, Label: "Service up (uptime)", Group: GroupServices, Severity: SeverityInfo, DefaultOn: true, tone: toneOK,
		Description: "A service that was announced as down passes its uptime checks again. Includes how long the outage lasted. Sent with the severity of the alert it closes."},
}

// Catalog returns the event types in display order.
func Catalog() []ChannelEventType { return slices.Clone(catalog) }

// lookupType returns the catalogue entry of a type.
func lookupType(t string) (ChannelEventType, bool) {
	for _, e := range catalog {
		if e.Type == t {
			return e, true
		}
	}
	return ChannelEventType{}, false
}

// DefaultEventTypes returns the event types a new channel subscribes to.
func DefaultEventTypes() []string {
	var out []string
	for _, e := range catalog {
		if e.DefaultOn {
			out = append(out, e.Type)
		}
	}
	return out
}

// NormalizeSeverity maps spellings callers use to a known severity ("" when unknown).
func NormalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info", "information", "ok", "success":
		return SeverityInfo
	case "warning", "warn":
		return SeverityWarning
	case "error", "err", "critical", "fatal", "failure":
		return SeverityError
	}
	return ""
}

func severityRank(s string) int {
	switch s {
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	}
	return 0
}

// toneOf picks the message colour: red and amber follow the severity; an
// informational event is green when its type reports good news. A recovery is
// always green: it carries the severity of the alert it closes (so channel
// filters keep the pair together), not its colour.
func toneOf(eventType, severity string) string {
	if eventType == TypeServiceRecovered || eventType == TypeUptimeUp {
		return toneOK
	}
	switch severity {
	case SeverityError:
		return toneBad
	case SeverityWarning:
		return toneWarn
	}
	if e, ok := lookupType(eventType); ok && e.tone != "" {
		if e.tone == toneBad || e.tone == toneWarn {
			return toneInfo
		}
		return e.tone
	}
	return toneInfo
}
