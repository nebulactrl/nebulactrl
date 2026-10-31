package agentprotocol

import (
	"fmt"
	"strings"
	"time"
)

// Volume backups: file-level backups of the named volumes of an ordinary
// service. The agent takes a point-in-time copy of the volumes (optional
// pre-hook, freeze, copy to a staging volume, thaw, optional post-hook) and
// uploads that copy in the background with restic, into one encrypted
// repository per service and destination. Everything that touches volume
// bytes runs in a short-lived helper container started from ResticImage; the
// agent itself never reads /var/lib/docker.

const (
	// KindVolumeBackup takes one backup: one restic snapshot of all volumes.
	KindVolumeBackup = "volume_backup"
	// KindVolumeForget removes snapshots (retention policy or explicit ids)
	// and prunes the repository.
	KindVolumeForget = "volume_forget"
	// KindVolumeRestore restores one volume of a snapshot into a new Docker
	// volume, or in place into the service's own volume.
	KindVolumeRestore = "volume_restore"
	// KindVolumeRemove deletes Docker volumes (retired volumes a user removed).
	KindVolumeRemove = "volume_remove"
)

func init() {
	for _, k := range []string{KindVolumeBackup, KindVolumeForget, KindVolumeRestore, KindVolumeRemove} {
		AllowedKinds[k] = true
	}
}

// ResticImage is the restic helper image: the official multi-arch index of
// restic 0.19.1 pinned by digest (checked with `docker buildx imagetools
// inspect`; BusyBox cp in it supports --reflink). It is pulled on first use
// only. Registered with PinImage so Docker cleanup never removes it.
var ResticImage = PinImage("restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510")

// Helper containers and staging volumes. Helpers carry LabelHelper=HelperBackup
// and NEVER LabelManaged, so the orphan sweep and Docker cleanup leave them to
// the backup code, which removes them when it is done (and sweeps the leftovers
// of a crashed agent).
const (
	LabelHelper = "nebulactrl.helper"
	// HelperBackup is the value of LabelHelper on backup helper containers.
	HelperBackup = "backup"
	// LabelBackupID carries the backup (or restore) id on a helper.
	LabelBackupID = "nebulactrl.backup"
	// LabelBackupService carries the service id on a helper.
	LabelBackupService = "nebulactrl.backup.service"
	// LabelBackupContainer carries the id of the service container a helper's
	// backup froze or stopped, so a sweep can thaw/start it after a crash.
	LabelBackupContainer = "nebulactrl.backup.container"
	// LabelBackupMode carries the backup mode (VolMode*) of a helper.
	LabelBackupMode = "nebulactrl.backup.mode"

	// BackupHelperPrefix names helpers: nebulactrl-bak-<backupId8>.
	BackupHelperPrefix = "nebulactrl-bak-"
	// BackupStagePrefix names staging volumes: nc-bak-<backupId8>.
	BackupStagePrefix = "nc-bak-"
	// ResticCacheVolume holds restic's cache (index) between runs.
	ResticCacheVolume = "nebulactrl-backup-cache"

	// Mount points inside a helper.
	HelperVolRoot  = "/vol"     // snapshot root: /vol/<volume name>/...
	HelperSrcRoot  = "/src"     // live source volumes (read-only), /src/<name>
	HelperDestDir  = "/nc-dest" // the agent's local backup directory (local destinations)
	HelperCacheDir = "/cache"   // restic cache
	HelperRestore  = "/restore" // restore target volume
)

// SecretResticPassword is the key of the repository password in
// Command.Secrets. The destination's S3 keys travel as SecretBackupAccessKey /
// SecretBackupSecretKey.
const SecretResticPassword = "restic_password"

// Volume backup modes.
const (
	// VolModeFreeze pauses the container (cgroup freezer) for the local copy
	// only. The default. Connections survive; requests stall.
	VolModeFreeze = "freeze"
	// VolModeStop stops the container, copies, starts it again.
	VolModeStop = "stop"
	// VolModeLive copies nothing: restic reads the live volumes, so the backup
	// can hold files from different moments.
	VolModeLive = "live"
)

// Volume backup limits and defaults shared by the control plane and the agent.
const (
	DefaultFreezeBudgetSeconds = 5
	MinFreezeBudgetSeconds     = 1
	MaxFreezeBudgetSeconds     = 60
	DefaultHookTimeoutSeconds  = 300
	MaxHookTimeoutSeconds      = 3600
	DefaultKeepDaily           = 7
	DefaultKeepWeekly          = 4
	DefaultKeepMonthly         = 6
	// VolumeBackupWaitSeconds is how long a backup waits for a deployment or
	// another mutating command of the service before it reports "skipped".
	VolumeBackupWaitSeconds = 120
	// VolumeBackupMaxSeconds bounds one backup (hooks, copy and upload).
	VolumeBackupMaxSeconds = 6 * 3600
	// HelperMemoryBytes and HelperNanoCPUs limit a helper container.
	HelperMemoryBytes = 512 << 20
	HelperNanoCPUs    = 1_000_000_000
	// VolFileListMax caps one directory listing of a snapshot.
	VolFileListMax = 5000
)

// VolumeHostName is the fixed restic --host of a service's snapshots. Without
// a stable host restic cannot find the parent snapshot and re-reads everything.
func VolumeHostName(serviceID string) string { return "nebulactrl-" + Short(serviceID) }

// VolumeRepo says where a service's restic repository lives. It carries no
// credentials: the access key, secret key and repository password arrive in
// Command.Secrets.
type VolumeRepo struct {
	Destination BackupDestination `json:"destination"`
	// Path is the repository path below the destination: the destination's key
	// prefix (if any) + "/volumes/<serviceId>". Validate with ValidObjectKey.
	Path string `json:"path"`
}

// Validate checks the repository path.
func (r VolumeRepo) Validate() error {
	if err := ValidObjectKey(r.Path); err != nil {
		return fmt.Errorf("invalid repository path: %w", err)
	}
	if r.Destination.Kind != DestS3 && r.Destination.Kind != DestLocal {
		return fmt.Errorf("unknown destination kind %q", r.Destination.Kind)
	}
	return nil
}

// VolumeRef names one volume of a service.
type VolumeRef struct {
	// Name is the volume's name in NebulaCtrl; its directory in a snapshot is
	// /vol/<Name>.
	Name string `json:"name"`
	// DockerName is the Docker volume that holds the data right now.
	DockerName string `json:"dockerName"`
	MountPath  string `json:"mountPath,omitempty"`
}

// VolumeHook is a command run inside the service's container. It is run
// without a shell (write "sh", "-c", "..." for one). Hooks write their
// artefacts inside the volume, so they are part of the copy.
type VolumeHook struct {
	Command        []string `json:"command"`
	User           string   `json:"user,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	// ContinueOnError makes a failing pre-hook a warning instead of aborting
	// the backup.
	ContinueOnError bool `json:"continueOnError,omitempty"`
}

// VolumeBackupPayload is the persisted payload of a volume_backup command.
// Secrets (SecretBackupAccessKey, SecretBackupSecretKey, SecretResticPassword)
// travel in Command.Secrets only.
type VolumeBackupPayload struct {
	BackupID  string     `json:"backupId"`
	ServiceID string     `json:"serviceId"`
	Repo      VolumeRepo `json:"repo"`
	// Mode is VolModeFreeze (default when empty), VolModeStop or VolModeLive.
	Mode string `json:"mode,omitempty"`
	// FreezeBudgetSeconds bounds the freeze (VolModeFreeze); default
	// DefaultFreezeBudgetSeconds. Over budget the agent thaws and fails.
	FreezeBudgetSeconds int `json:"freezeBudgetSeconds,omitempty"`
	// StopGraceSeconds is the stop timeout of VolModeStop (default 30).
	StopGraceSeconds int         `json:"stopGraceSeconds,omitempty"`
	Volumes          []VolumeRef `json:"volumes"`
	PreHook          *VolumeHook `json:"preHook,omitempty"`
	PostHook         *VolumeHook `json:"postHook,omitempty"`
	// Excludes are restic --exclude patterns (relative to /vol).
	Excludes []string     `json:"excludes,omitempty"`
	Meta     ManifestMeta `json:"meta"`
	// Trigger is manual, schedule or pre_restore; recorded as a snapshot tag.
	Trigger string `json:"trigger,omitempty"`
}

// Backup outcomes in VolumeBackupResult.Outcome (besides OutcomeSucceeded and
// OutcomeFailed).
const (
	// OutcomeSkipped: the backup was not attempted because the service stayed
	// busy (a deployment) for VolumeBackupWaitSeconds. The service was not touched.
	OutcomeSkipped = "skipped"
)

// Stage methods in VolumeBackupResult.StageMethod.
const (
	StageReflink = "reflink" // copy-on-write clone
	StageCopy    = "copy"    // plain copy
	StageNone    = "none"    // live mode: no copy
)

// VolumeBackupResult is ResultFrame.Data of a volume_backup command.
type VolumeBackupResult struct {
	Outcome    string `json:"outcome"`
	Message    string `json:"message,omitempty"`
	SnapshotID string `json:"snapshotId,omitempty"`
	// Consistency is the mode actually used (VolMode*); a stopped service is
	// backed up without a freeze and reports VolModeStop.
	Consistency string `json:"consistency,omitempty"`
	StageMethod string `json:"stageMethod,omitempty"`
	// FreezeMs is how long the service was paused or stopped.
	FreezeMs int64 `json:"freezeMs,omitempty"`
	// StageMs is how long the local copy took.
	StageMs        int64 `json:"stageMs,omitempty"`
	BytesProcessed int64 `json:"bytesProcessed,omitempty"`
	BytesAdded     int64 `json:"bytesAdded,omitempty"`
	FilesNew       int64 `json:"filesNew,omitempty"`
	FilesChanged   int64 `json:"filesChanged,omitempty"`
	FilesUnchanged int64 `json:"filesUnmodified,omitempty"`
	// HookOutcome is "" (no hook), "ok" or "failed_continued".
	HookOutcome string    `json:"hookOutcome,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	FinishedAt  time.Time `json:"finishedAt,omitzero"`
}

// VolumeKeep is a restic retention policy (forget --keep-*). Zero values mean
// "no limit of that kind".
type VolumeKeep struct {
	Daily   int `json:"daily,omitempty"`
	Weekly  int `json:"weekly,omitempty"`
	Monthly int `json:"monthly,omitempty"`
}

// Any reports whether any limit is set.
func (k VolumeKeep) Any() bool { return k.Daily > 0 || k.Weekly > 0 || k.Monthly > 0 }

// VolumeForgetPayload is the payload of a volume_forget command: it applies
// Keep to the service's snapshots and/or removes SnapshotIDs, then prunes.
type VolumeForgetPayload struct {
	ServiceID string     `json:"serviceId"`
	Repo      VolumeRepo `json:"repo"`
	// Keep is the retention policy to apply (nil: none).
	Keep *VolumeKeep `json:"keep,omitempty"`
	// SnapshotIDs are removed explicitly (a user deleted a backup).
	SnapshotIDs []string `json:"snapshotIds,omitempty"`
	// Protect lists snapshots (locked backups) that Keep must never remove. The
	// agent applies Keep as a dry run, drops the protected ids from the
	// victims and forgets the rest explicitly (restic re-creates a snapshot,
	// under a new id, when its tags change, so tagging cannot protect).
	Protect []string `json:"protect,omitempty"`
	// BackupIDs are the control plane's rows of SnapshotIDs; the agent ignores them.
	BackupIDs []string `json:"backupIds,omitempty"`
}

// VolumeForgetResult is ResultFrame.Data of a volume_forget command.
type VolumeForgetResult struct {
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
	// Removed lists the full ids of the snapshots that were forgotten.
	Removed []string `json:"removed,omitempty"`
}

// VolumeRestorePayload is the persisted payload of a volume_restore command.
type VolumeRestorePayload struct {
	RestoreID  string     `json:"restoreId"`
	ServiceID  string     `json:"serviceId"`
	Repo       VolumeRepo `json:"repo"`
	SnapshotID string     `json:"snapshotId"`
	// Volume.Name selects /vol/<name> in the snapshot. Volume.DockerName is the
	// Docker volume written to: a NEW volume (created and removed again on
	// failure) unless InPlace.
	Volume VolumeRef `json:"volume"`
	// InPlace stops the service's container, empties the volume, restores into
	// it and starts the container again. The control plane has taken a safety
	// backup first.
	InPlace          bool `json:"inPlace,omitempty"`
	StopGraceSeconds int  `json:"stopGraceSeconds,omitempty"`
}

// VolumeRestoreResult is ResultFrame.Data of a volume_restore command.
type VolumeRestoreResult struct {
	Outcome    string    `json:"outcome"`
	Message    string    `json:"message,omitempty"`
	DockerName string    `json:"dockerName,omitempty"`
	StoppedMs  int64     `json:"stoppedMs,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// VolumeRemovePayload is the payload of a volume_remove command.
type VolumeRemovePayload struct {
	ServiceID string   `json:"serviceId"`
	Names     []string `json:"names"`
	// RetiredIDs are the control plane's rows of Names; the agent ignores them.
	RetiredIDs []string `json:"retiredIds,omitempty"`
}

// VolumeRemoveResult is ResultFrame.Data of a volume_remove command.
type VolumeRemoveResult struct {
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
}

// Backup file browsing and download are interactive streams, not commands:
// nothing is persisted. The control plane opens a stream, sends one
// volfile_request (the only frame that carries secrets), and the agent answers
// with fs_response (and fs_data frames for a read) exactly like the container
// file browser; stream_ack credits flow control. The helper that runs restic is
// removed when the stream ends.
const (
	// TypeVolFileRequest is control plane → agent.
	TypeVolFileRequest = "volfile_request"
)

// Volume file operations.
const (
	// VolFileList lists one directory of a snapshot (answered by one
	// fs_response with Entries).
	VolFileList = "list"
	// VolFileRead streams one file (fs_response stage "ready" with Entry, then fs_data).
	VolFileRead = "read"
	// VolFileArchive streams a directory as a zip (Entry.Size is -1: unknown).
	VolFileArchive = "archive"
)

// VolFileRequest is one operation on a snapshot's files.
type VolFileRequest struct {
	RequestID  string     `json:"requestId"`
	Op         string     `json:"op"`
	ServiceID  string     `json:"serviceId"`
	Repo       VolumeRepo `json:"repo"`
	SnapshotID string     `json:"snapshotId"`
	// Volume is the volume's name; Path an absolute path inside it ("/" = root).
	Volume string `json:"volume"`
	Path   string `json:"path"`
	// MaxBytes caps a read (0 = MaxFileReadBytes).
	MaxBytes int64 `json:"maxBytes,omitempty"`
	// Secrets are the send-time secrets of the repository (same keys as
	// Command.Secrets). Never logged or persisted.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// CleanVolumePath validates a path inside a volume: absolute, no NUL, no ".."
// segments; it returns the cleaned path.
func CleanVolumePath(p string) (string, error) {
	switch {
	case p == "":
		return "/", nil
	case len(p) > 4096:
		return "", fmt.Errorf("path is too long")
	case strings.ContainsRune(p, 0):
		return "", fmt.Errorf("path contains a NUL byte")
	case !strings.HasPrefix(p, "/"):
		return "", fmt.Errorf("path must be absolute")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path must not contain '..'")
		}
	}
	out := "/" + strings.Join(splitNonEmpty(p), "/")
	return out, nil
}

func splitNonEmpty(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

// RestoredVolumeName names the Docker volume a restore into a new volume
// creates: the volume's derived name plus "-r<yymmddhhmmss>".
func RestoredVolumeName(serviceID, name string, at time.Time) string {
	return VolumeName(serviceID, name) + "-r" + at.UTC().Format("060102150405")
}
