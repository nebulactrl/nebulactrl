package backups

import (
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Volume restore modes and statuses (the statuses are shared with RestorePreBackup etc.).
const (
	VolumeRestoreNewVolume = "new_volume"
	VolumeRestoreInPlace   = "in_place"
)

// Audit actions of volume backups.
const (
	ActionVolumeConfigUpdate = "volume_backup.config.update"
	ActionVolumeCreate       = "volume_backup.create"
	ActionVolumeDelete       = "volume_backup.delete"
	ActionVolumeLock         = "volume_backup.lock"
	ActionVolumeRestore      = "volume_backup.restore"
	ActionVolumeRecoveryKey  = "volume_backup.recovery_key.reveal"
	ActionVolumeFileDownload = "volume_backup.file.download"
	ActionConfigDelete       = "volume_backup.config.delete"
	ActionRetiredDelete      = "retired_volume.delete"
)

// volumeEngineLabel is the engine shown in notifications of volume backups.
const volumeEngineLabel = "volume files"

// VolumeBackupHook is a command run inside the service's container around the
// snapshot. It runs without a shell: write "sh", "-c", "…" for one.
type VolumeBackupHook struct {
	Command         []string `json:"command" minItems:"1" maxItems:"64" doc:"The argv, run without a shell."`
	User            string   `json:"user,omitempty" maxLength:"128" doc:"User inside the container (default: the container's user)."`
	TimeoutSeconds  int      `json:"timeoutSeconds,omitempty" minimum:"1" maximum:"3600" doc:"Default 300."`
	ContinueOnError bool     `json:"continueOnError" required:"false" doc:"A failing pre-hook becomes a warning instead of aborting the backup."`
}

// VolumeBackupVolumeRef is a volume of the service that a backup covers.
type VolumeBackupVolumeRef struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	MountPath string    `json:"mountPath"`
}

// VolumeBackupConfigView is a service's volume backup configuration. With no
// saved configuration it carries the defaults and configured=false.
type VolumeBackupConfigView struct {
	ServiceID           uuid.UUID               `json:"serviceId"`
	Configured          bool                    `json:"configured"`
	Eligible            bool                    `json:"eligible"`
	IneligibleReason    string                  `json:"ineligibleReason,omitempty"`
	Volumes             []VolumeBackupVolumeRef `json:"volumes"`
	DestinationID       *uuid.UUID              `json:"destinationId,omitempty"`
	DestinationName     string                  `json:"destinationName,omitempty"`
	DestinationKind     string                  `json:"destinationKind,omitempty" enum:"s3,local"`
	Mode                string                  `json:"mode" enum:"freeze,stop,live"`
	FreezeBudgetSeconds int                     `json:"freezeBudgetSeconds"`
	PreHook             *VolumeBackupHook       `json:"preHook,omitempty"`
	PostHook            *VolumeBackupHook       `json:"postHook,omitempty"`
	Excludes            []string                `json:"excludes"`
	KeepDaily           int                     `json:"keepDaily"`
	KeepWeekly          int                     `json:"keepWeekly"`
	KeepMonthly         int                     `json:"keepMonthly"`
	Cron                string                  `json:"cron"`
	Timezone            string                  `json:"timezone"`
	Enabled             bool                    `json:"enabled"`
	NextRunAt           *time.Time              `json:"nextRunAt,omitempty"`
	LastRunAt           *time.Time              `json:"lastRunAt,omitempty"`
	RepoReady           bool                    `json:"repoReady" doc:"A repository password exists for this destination."`
}

// VolumeBackupConfigInput saves a volume backup configuration (PUT replaces it).
type VolumeBackupConfigInput struct {
	DestinationID       uuid.UUID         `json:"destinationId"`
	Mode                string            `json:"mode,omitempty" enum:"freeze,stop,live" default:"freeze" doc:"freeze pauses the container while the local copy is taken (default); stop stops it; live copies nothing and may be inconsistent."`
	FreezeBudgetSeconds int               `json:"freezeBudgetSeconds,omitempty" minimum:"1" maximum:"60" doc:"Longest pause of mode freeze (default 5). Over budget the copy is abandoned and the container thawed."`
	PreHook             *VolumeBackupHook `json:"preHook,omitempty" doc:"Runs inside the container before the freeze, e.g. a database dump into the volume."`
	PostHook            *VolumeBackupHook `json:"postHook,omitempty" doc:"Runs inside the container after it was thawed."`
	Excludes            []string          `json:"excludes,omitempty" maxItems:"100" doc:"restic --exclude patterns relative to /vol, e.g. data/cache/**."`
	KeepDaily           *int              `json:"keepDaily,omitempty" minimum:"0" maximum:"3650" doc:"Keep the newest backup of each of the last N days (default 7, 0 = no limit)."`
	KeepWeekly          *int              `json:"keepWeekly,omitempty" minimum:"0" maximum:"520" doc:"Default 4, 0 = no limit."`
	KeepMonthly         *int              `json:"keepMonthly,omitempty" minimum:"0" maximum:"120" doc:"Default 6, 0 = no limit. With all three at 0 nothing is ever removed."`
	Cron                string            `json:"cron,omitempty" doc:"5-field cron expression or @hourly/@daily/@weekly/@monthly/@yearly; empty = manual backups only. At most one run every 5 minutes."`
	Timezone            string            `json:"timezone,omitempty" doc:"IANA zone, default UTC."`
	Enabled             bool              `json:"enabled,omitempty" doc:"Run on the schedule (needs a cron)."`
}

// VolumeBackupView is one volume backup run.
type VolumeBackupView struct {
	ID              uuid.UUID  `json:"id"`
	ServiceID       uuid.UUID  `json:"serviceId"`
	DestinationID   uuid.UUID  `json:"destinationId"`
	DestinationName string     `json:"destinationName,omitempty"`
	DestinationKind string     `json:"destinationKind,omitempty"`
	ServerID        *uuid.UUID `json:"serverId,omitempty"`
	Trigger         string     `json:"trigger" enum:"manual,schedule,pre_restore"`
	Status          string     `json:"status" enum:"queued,running,succeeded,failed,skipped,expired"`
	Mode            string     `json:"mode" doc:"The requested mode."`
	Consistency     string     `json:"consistency,omitempty" doc:"The mode the agent actually used (a stopped service is backed up without a freeze)."`
	Volumes         []string   `json:"volumes"`
	SnapshotID      string     `json:"snapshotId,omitempty"`
	SizeBytes       int64      `json:"sizeBytes"`
	BytesAdded      int64      `json:"bytesAdded"`
	Files           int64      `json:"files"`
	FreezeMs        int        `json:"freezeMs" doc:"How long the container was paused or stopped."`
	StageMs         int        `json:"stageMs" doc:"How long the local copy took."`
	StageMethod     string     `json:"stageMethod,omitempty" enum:"reflink,copy,none"`
	HookOutcome     string     `json:"hookOutcome,omitempty"`
	Warnings        []string   `json:"warnings"`
	Error           string     `json:"error,omitempty"`
	Locked          bool       `json:"locked"`
	ObjectDeleted   bool       `json:"objectDeleted" doc:"True once retention or a user removed the snapshot; the row stays as history."`
	Restorable      bool       `json:"restorable"`
	Actor           string     `json:"actor,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// VolumeRestoreView is one restore of a volume of a backup.
type VolumeRestoreView struct {
	ID          uuid.UUID  `json:"id"`
	BackupID    uuid.UUID  `json:"backupId"`
	ServiceID   uuid.UUID  `json:"serviceId"`
	VolumeName  string     `json:"volumeName"`
	Mode        string     `json:"mode" enum:"new_volume,in_place"`
	Status      string     `json:"status" enum:"pre_backup,restoring,succeeded,failed"`
	DockerName  string     `json:"dockerName,omitempty"`
	PreBackupID *uuid.UUID `json:"preBackupId,omitempty"`
	Error       string     `json:"error,omitempty"`
	Actor       string     `json:"actor,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	SwapStaged  bool       `json:"swapStaged" doc:"A restore into a new volume succeeded and the swap is staged: apply it with Deploy."`
}

// RetiredVolumeView is a Docker volume a restore swapped out. Its data is kept
// until the user deletes it.
type RetiredVolumeView struct {
	ID         uuid.UUID `json:"id"`
	ServiceID  uuid.UUID `json:"serviceId"`
	VolumeName string    `json:"volumeName"`
	DockerName string    `json:"dockerName"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// VolumeFileEntry is one entry of a directory of a backup.
type VolumeFileEntry struct {
	Name       string     `json:"name"`
	Type       string     `json:"type" enum:"file,dir,link,other"`
	Size       int64      `json:"size"`
	Mode       string     `json:"mode,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
}

// VolumeFileListing is one directory of a volume inside a backup.
type VolumeFileListing struct {
	BackupID  uuid.UUID         `json:"backupId"`
	Volume    string            `json:"volume"`
	Path      string            `json:"path"`
	Entries   []VolumeFileEntry `json:"entries"`
	Truncated bool              `json:"truncated"`
}

// VolumeBackupRecoveryKey is what a restic client needs to open the service's
// repository without NebulaCtrl.
type VolumeBackupRecoveryKey struct {
	Repository string `json:"repository" doc:"The restic repository URL or path as the agent uses it."`
	Password   string `json:"password"`
	Note       string `json:"note"`
}

// VolumeRestoreInput describes a restore of one volume.
type VolumeRestoreInput struct {
	// Volume is the volume's name in the service.
	Volume string
	// Mode is new_volume (default) or in_place.
	Mode string
	// ConfirmSlug must equal the service's slug for an in-place restore.
	ConfirmSlug string
}

func volumeBackupView(b db.VolumeBackup, status, destName, destKind string) VolumeBackupView {
	return VolumeBackupView{
		ID: b.ID, ServiceID: b.ServiceID, DestinationID: b.DestinationID, DestinationName: destName, DestinationKind: destKind,
		ServerID: b.ServerID, Trigger: b.Trigger, Status: status, Mode: b.Mode, Consistency: b.Consistency,
		Volumes: nonNil(b.Volumes), SnapshotID: b.SnapshotID, SizeBytes: b.SizeBytes, BytesAdded: b.BytesAdded, Files: b.Files,
		FreezeMs: int(b.FreezeMs), StageMs: int(b.StageMs), StageMethod: b.StageMethod, HookOutcome: b.HookOutcome,
		Warnings: nonNil(b.Warnings), Error: b.Error, Locked: b.Locked, ObjectDeleted: b.ObjectDeletedAt != nil,
		Restorable: b.Status == StatusSucceeded && b.ObjectDeletedAt == nil, Actor: b.ActorLabel,
		StartedAt: b.StartedAt, FinishedAt: b.FinishedAt, CreatedAt: b.CreatedAt,
	}
}

func volumeRestoreView(r db.VolumeRestore) VolumeRestoreView {
	return VolumeRestoreView{
		ID: r.ID, BackupID: r.BackupID, ServiceID: r.ServiceID, VolumeName: r.VolumeName, Mode: r.Mode, Status: r.Status,
		DockerName: r.DockerName, PreBackupID: r.PreBackupID, Error: r.Error, Actor: r.ActorLabel, CreatedAt: r.CreatedAt,
		FinishedAt: r.FinishedAt, SwapStaged: r.Mode == VolumeRestoreNewVolume && r.Status == RestoreSucceeded,
	}
}

func retiredVolumeView(r db.RetiredVolume) RetiredVolumeView {
	return RetiredVolumeView{
		ID: r.ID, ServiceID: r.ServiceID, VolumeName: r.VolumeName, DockerName: r.DockerName, Reason: r.Reason, CreatedAt: r.CreatedAt,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// protoHook converts an API hook to the agent's, defaulting the timeout.
func protoHook(h *VolumeBackupHook) *agentprotocol.VolumeHook {
	if h == nil {
		return nil
	}
	t := h.TimeoutSeconds
	if t <= 0 {
		t = agentprotocol.DefaultHookTimeoutSeconds
	}
	return &agentprotocol.VolumeHook{Command: h.Command, User: h.User, TimeoutSeconds: t, ContinueOnError: h.ContinueOnError}
}
