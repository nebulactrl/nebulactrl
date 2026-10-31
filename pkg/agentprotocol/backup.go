package agentprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Backup and restore command kinds.
const (
	// KindDBBackup streams a logical dump of a database container to a
	// destination (S3-compatible bucket or a directory on the host).
	KindDBBackup = "db_backup"
	// KindDBRestore streams a stored dump into a database container.
	KindDBRestore = "db_restore"
	// KindBackupDelete removes a stored dump (and its manifest) from a destination.
	KindBackupDelete = "backup_delete"
	// KindBackupTest writes, reads and deletes a probe object on a destination.
	KindBackupTest = "backup_test"
)

func init() {
	for _, k := range []string{KindDBBackup, KindDBRestore, KindBackupDelete, KindBackupTest} {
		AllowedKinds[k] = true
	}
}

// Reserved keys in Command.Secrets for backup commands.
const (
	SecretBackupAccessKey = "backup_access_key"
	SecretBackupSecretKey = "backup_secret_key"
	// SecretDBPassword is the password of BackupTarget.User.
	SecretDBPassword = "db_password"
)

// Destination kinds.
const (
	DestS3    = "s3"
	DestLocal = "local"
)

// Dump formats recorded in the manifest.
const (
	FormatPGCustom     = "pg_custom"        // pg_dump --format=custom
	FormatMySQLGz      = "mysql_sql_gz"     // mysqldump | gzip
	FormatMariaDBGz    = "mariadb_sql_gz"   // mariadb-dump | gzip
	FormatMongoArchive = "mongo_archive_gz" // mongodump --archive --gzip
	FormatRDB          = "rdb"              // valkey-cli --rdb -
)

// BackupDestination tells the agent where to store or read backups. It never
// carries credentials: the access and secret keys of an S3 destination arrive in
// Command.Secrets (SecretBackupAccessKey / SecretBackupSecretKey).
type BackupDestination struct {
	// ID is the control plane's destination id; it lets the control plane
	// attach the right credentials and attribute results. The agent ignores it.
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind"` // DestS3 | DestLocal

	// S3.
	Endpoint  string `json:"endpoint,omitempty"` // empty = AWS
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
	// CompatMode disables the SDK's default checksums (WhenRequired), which
	// non-AWS S3 implementations often reject.
	CompatMode bool `json:"compatMode,omitempty"`

	// Local: a relative sub-directory of the agent's backup directory.
	LocalDir string `json:"localDir,omitempty"`
}

// BackupTarget identifies the database inside its container. Passwords are in
// Command.Secrets (SecretDBPassword), never here.
type BackupTarget struct {
	ServiceID string `json:"serviceId"`
	// Engine is the engine id: postgres, mysql, mariadb, mongodb, valkey or redis.
	Engine   string `json:"engine"`
	Version  string `json:"version,omitempty"`
	Database string `json:"database,omitempty"`
	User     string `json:"user,omitempty"`
}

// DBBackupPayload is the persisted payload of a db_backup command.
type DBBackupPayload struct {
	BackupID    string            `json:"backupId"`
	Target      BackupTarget      `json:"target"`
	Destination BackupDestination `json:"destination"`
	// ObjectKey is the full key (prefix applied) of the dump object;
	// ObjectKey+".manifest.json" holds the manifest.
	ObjectKey string `json:"objectKey"`
	// Meta is copied into the manifest so a bucket can be understood without
	// the control plane.
	Meta ManifestMeta `json:"meta"`
}

// ManifestMeta is control-plane knowledge copied into a manifest.
type ManifestMeta struct {
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	ServiceID   string `json:"serviceId,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
}

// BackupManifest is the sidecar written next to every successfully stored dump.
type BackupManifest struct {
	Version       int          `json:"version"`
	BackupID      string       `json:"backupId"`
	Engine        string       `json:"engine"`
	EngineVersion string       `json:"engineVersion,omitempty"`
	Database      string       `json:"database,omitempty"`
	Format        string       `json:"format"`
	ObjectKey     string       `json:"objectKey"`
	SizeBytes     int64        `json:"sizeBytes"`
	SHA256        string       `json:"sha256"`
	CreatedAt     time.Time    `json:"createdAt"`
	Meta          ManifestMeta `json:"meta"`
}

// DBBackupResult is ResultFrame.Data of a db_backup command.
type DBBackupResult struct {
	Outcome    string    `json:"outcome"`
	Message    string    `json:"message,omitempty"`
	SizeBytes  int64     `json:"sizeBytes,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Format     string    `json:"format,omitempty"`
	ObjectKey  string    `json:"objectKey,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// DBRestorePayload is the persisted payload of a db_restore command.
type DBRestorePayload struct {
	RestoreID   string            `json:"restoreId"`
	Target      BackupTarget      `json:"target"` // the database to restore INTO
	Destination BackupDestination `json:"destination"`
	ObjectKey   string            `json:"objectKey"`
	Format      string            `json:"format"`
	// SHA256, when set, is verified while streaming; a mismatch fails the restore.
	SHA256 string `json:"sha256,omitempty"`
	// Clean drops existing objects first (in-place restores). PostgreSQL does not
	// need it: the dump is restored into a scratch database that replaces the
	// target, in either mode.
	Clean bool `json:"clean,omitempty"`
}

// DBRestoreResult is ResultFrame.Data of a db_restore command.
type DBRestoreResult struct {
	Outcome    string    `json:"outcome"`
	Message    string    `json:"message,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// BackupDeletePayload is the payload of a backup_delete command.
type BackupDeletePayload struct {
	Destination BackupDestination `json:"destination"`
	ObjectKeys  []string          `json:"objectKeys"`
	// BackupIDs are the control plane's rows of ObjectKeys (same order); the
	// agent ignores them.
	BackupIDs []string `json:"backupIds,omitempty"`
}

// BackupTestPayload is the payload of a backup_test command.
type BackupTestPayload struct {
	Destination BackupDestination `json:"destination"`
	// Prefix is the destination's key prefix under which the probe is written.
	Prefix string `json:"prefix,omitempty"`
}

// BackupSimpleResult is ResultFrame.Data of backup_delete and backup_test.
type BackupSimpleResult struct {
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
}

// MarshalResultData encodes a result body for ResultFrame.Data.
func MarshalResultData(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// BackupSupported reports whether databases of engine can be backed up.
func BackupSupported(engine string) bool {
	return RestoreSupported(engine) || engine == "valkey" || engine == "redis"
}

// RestoreSupported reports whether dumps of engine can be restored by the agent.
func RestoreSupported(engine string) bool {
	switch engine {
	case "postgres", "mysql", "mariadb", "mongodb":
		return true
	}
	return false
}

// ValidObjectKey reports whether key is a safe relative object key: non-empty,
// no absolute path, no "." / ".." / empty segments, no backslashes or NUL. The
// control plane and the agent both enforce it on keys, key prefixes and local
// sub-directories.
func ValidObjectKey(key string) error {
	if key == "" || len(key) > 1024 {
		return errors.New("invalid object key length")
	}
	if strings.ContainsAny(key, "\\\x00") || strings.HasPrefix(key, "/") {
		return fmt.Errorf("invalid object key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid object key %q", key)
		}
	}
	return nil
}
