package backups

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Backup statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
	StatusExpired   = "expired"
)

// Backup triggers.
const (
	TriggerManual     = "manual"
	TriggerSchedule   = "schedule"
	TriggerPreRestore = "pre_restore"
)

// Restore modes and statuses.
const (
	ModeNew     = "new"
	ModeInPlace = "in_place"

	RestoreProvisioning = "provisioning"
	RestorePreBackup    = "pre_backup"
	RestoreRestoring    = "restoring"
	RestoreSucceeded    = "succeeded"
	RestoreFailed       = "failed"
)

// Event types published on the bus.
const (
	EventBackupUpdated      = "backup.updated"
	EventDestinationUpdated = "backup.destination.updated"
)

// Audit actions.
const (
	ActionDestinationCreate = "backup.destination.create"
	ActionDestinationUpdate = "backup.destination.update"
	ActionDestinationDelete = "backup.destination.delete"
	ActionDestinationTest   = "backup.destination.test"
	ActionScheduleCreate    = "backup.schedule.create"
	ActionScheduleUpdate    = "backup.schedule.update"
	ActionScheduleDelete    = "backup.schedule.delete"
	ActionBackupCreate      = "backup.create"
	ActionBackupDelete      = "backup.delete"
	ActionBackupLock        = "backup.lock"
	ActionRestore           = "backup.restore"
)

// DestinationView is the API view of a backup destination. Credentials are
// never returned, only whether they are set.
type DestinationView struct {
	ID               uuid.UUID  `json:"id"`
	Name             string     `json:"name"`
	Kind             string     `json:"kind" enum:"s3,local,builtin" doc:"builtin is the team's slice of the built-in backup storage on the control-plane host. The instance administrator manages it: it can not be created, changed or deleted here, and its endpoint, bucket and keys are never shown."`
	Endpoint         string     `json:"endpoint,omitempty"`
	Region           string     `json:"region,omitempty"`
	Bucket           string     `json:"bucket,omitempty"`
	Prefix           string     `json:"prefix,omitempty"`
	PathStyle        bool       `json:"pathStyle"`
	CompatMode       bool       `json:"compatMode"`
	AccessKeySet     bool       `json:"accessKeySet"`
	SecretKeySet     bool       `json:"secretKeySet"`
	LocalDir         string     `json:"localDir,omitempty"`
	OffSite          bool       `json:"offSite" doc:"False for local directories and the built-in storage: a backup on the database's own host or on the control-plane host does not survive the loss of that host."`
	LastTestAt       *time.Time `json:"lastTestAt,omitempty"`
	LastTestOK       *bool      `json:"lastTestOk,omitempty"`
	LastTestMessage  string     `json:"lastTestMessage,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	InUseBySchedules int        `json:"inUseBySchedules"`
	StoredBackups    int        `json:"storedBackups"`
}

// ScheduleView is the API view of a backup schedule.
type ScheduleView struct {
	ID              uuid.UUID  `json:"id"`
	ServiceID       uuid.UUID  `json:"serviceId"`
	DestinationID   uuid.UUID  `json:"destinationId"`
	DestinationName string     `json:"destinationName,omitempty"`
	Cron            string     `json:"cron"`
	Timezone        string     `json:"timezone"`
	Enabled         bool       `json:"enabled"`
	RetentionCount  int        `json:"retentionCount"`
	RetentionDays   int        `json:"retentionDays"`
	NextRunAt       *time.Time `json:"nextRunAt,omitempty"`
	LastRunAt       *time.Time `json:"lastRunAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// BackupView is the API view of one backup run.
type BackupView struct {
	ID              uuid.UUID  `json:"id"`
	ServiceID       uuid.UUID  `json:"serviceId"`
	ScheduleID      *uuid.UUID `json:"scheduleId,omitempty"`
	DestinationID   uuid.UUID  `json:"destinationId"`
	DestinationName string     `json:"destinationName,omitempty"`
	DestinationKind string     `json:"destinationKind,omitempty"`
	ServerID        *uuid.UUID `json:"serverId,omitempty"`
	Trigger         string     `json:"trigger" enum:"manual,schedule,pre_restore"`
	Status          string     `json:"status" enum:"queued,running,succeeded,failed,skipped,expired"`
	Engine          string     `json:"engine"`
	EngineVersion   string     `json:"engineVersion,omitempty"`
	Database        string     `json:"database,omitempty"`
	Format          string     `json:"format,omitempty"`
	ObjectKey       string     `json:"objectKey,omitempty"`
	SizeBytes       int64      `json:"sizeBytes"`
	SHA256          string     `json:"sha256,omitempty"`
	Error           string     `json:"error,omitempty"`
	Locked          bool       `json:"locked"`
	ObjectDeleted   bool       `json:"objectDeleted" doc:"True once retention or a user removed the stored object; the row stays as history."`
	Restorable      bool       `json:"restorable"`
	Actor           string     `json:"actor,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// RestoreView is the API view of a restore.
type RestoreView struct {
	ID              uuid.UUID  `json:"id"`
	BackupID        uuid.UUID  `json:"backupId"`
	Mode            string     `json:"mode" enum:"new,in_place"`
	SourceServiceID uuid.UUID  `json:"sourceServiceId"`
	TargetServiceID *uuid.UUID `json:"targetServiceId,omitempty"`
	Status          string     `json:"status" enum:"provisioning,pre_backup,restoring,succeeded,failed"`
	PreBackupID     *uuid.UUID `json:"preBackupId,omitempty"`
	Error           string     `json:"error,omitempty"`
	Actor           string     `json:"actor,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

func destinationView(d db.BackupDestination, schedules, stored int) DestinationView {
	v := DestinationView{
		ID: d.ID, Name: d.Name, Kind: d.Kind, Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
		PathStyle: d.PathStyle, CompatMode: d.CompatMode, AccessKeySet: len(d.AccessKeyEnc) > 0, SecretKeySet: len(d.SecretKeyEnc) > 0,
		LocalDir: d.LocalDir, OffSite: d.Kind == "s3", LastTestAt: d.LastTestAt, LastTestOK: d.LastTestOk,
		LastTestMessage: d.LastTestMessage, CreatedAt: d.CreatedAt, InUseBySchedules: schedules, StoredBackups: stored,
	}
	if d.Kind == DestBuiltin {
		return builtinDestinationView(v)
	}
	return v
}

func scheduleView(s db.BackupSchedule, destName string) ScheduleView {
	return ScheduleView{
		ID: s.ID, ServiceID: s.ServiceID, DestinationID: s.DestinationID, DestinationName: destName, Cron: s.Cron, Timezone: s.Timezone,
		Enabled: s.Enabled, RetentionCount: int(s.RetentionCount), RetentionDays: int(s.RetentionDays),
		NextRunAt: s.NextRunAt, LastRunAt: s.LastRunAt, CreatedAt: s.CreatedAt,
	}
}

func backupView(b db.Backup, status, destName, destKind string) BackupView {
	return BackupView{
		ID: b.ID, ServiceID: b.ServiceID, ScheduleID: b.ScheduleID, DestinationID: b.DestinationID, DestinationName: destName,
		DestinationKind: destKind, ServerID: b.ServerID, Trigger: b.Trigger, Status: status, Engine: b.Engine,
		EngineVersion: b.EngineVersion, Database: b.DatabaseName, Format: b.Format, ObjectKey: b.ObjectKey, SizeBytes: b.SizeBytes,
		SHA256: b.Sha256, Error: b.Error, Locked: b.Locked, ObjectDeleted: b.ObjectDeletedAt != nil,
		Restorable: b.Status == StatusSucceeded && b.ObjectDeletedAt == nil, Actor: b.ActorLabel,
		StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, CreatedAt: b.CreatedAt,
	}
}

func restoreView(r db.BackupRestore) RestoreView {
	return RestoreView{
		ID: r.ID, BackupID: r.BackupID, Mode: r.Mode, SourceServiceID: r.SourceServiceID, TargetServiceID: r.TargetServiceID,
		Status: r.Status, PreBackupID: r.PreBackupID, Error: r.Error, Actor: r.ActorLabel, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
}
