package agentprotocol

// Managed-database administration commands.
const (
	// KindDBAdmin runs a typed administrative action inside a database
	// container (rotate the application password, report version/size).
	KindDBAdmin = "db_admin"
	// KindDBUpgrade converts a PostgreSQL data volume to a new major version into a
	// new volume with pg_upgrade (tianon/postgres-upgrade). The old volume is kept.
	KindDBUpgrade = "db_upgrade"
)

func init() {
	AllowedKinds[KindDBAdmin] = true
	AllowedKinds[KindDBUpgrade] = true
}

// db_admin actions.
const (
	DBActionRotatePassword = "rotate_password"
	DBActionInfo           = "info"
)

// Reserved keys in Command.Secrets for db_admin commands. The values are
// attached at send time and never persisted.
const (
	SecretDBAdminPassword     = "dbadmin:password"      // current application/root credential
	SecretDBAdminRootPassword = "dbadmin:root_password" // current root password (MySQL, MariaDB)
	SecretDBAdminNewPassword  = "dbadmin:new_password"  // password to apply
)

// DBAdminPayload is Command.Payload of db_admin. It names the target and the
// action; the agent builds the exact argv from the engine catalog (typed
// commands only, no shell strings from the control plane).
type DBAdminPayload struct {
	ServiceID  string `json:"serviceId"`
	Action     string `json:"action"`
	RotationID string `json:"rotationId,omitempty"`
	Engine     string `json:"engine"`
	Version    string `json:"version"`
	Flavor     string `json:"flavor,omitempty"`
	Database   string `json:"database,omitempty"`
	User       string `json:"user,omitempty"`
	// Repl carries the arguments of the replication actions (repl_*).
	Repl *ReplAdmin `json:"repl,omitempty"`
}

// DBInfo is the snapshot reported by the info action.
type DBInfo struct {
	Version     string `json:"version"`
	SizeBytes   int64  `json:"sizeBytes"`
	Connections int    `json:"connections"`
}

// DBAdminResult is ResultFrame.Data of db_admin.
type DBAdminResult struct {
	Action  string  `json:"action"`
	Message string  `json:"message,omitempty"`
	Info    *DBInfo `json:"info,omitempty"`
	// Replication is the status of repl_status; FinalLSN the position of
	// repl_final_lsn; LSN the position reached by repl_freeze / repl_promote.
	Replication *ReplicationStatus `json:"replication,omitempty"`
	FinalLSN    string             `json:"finalLsn,omitempty"`
	LSN         string             `json:"lsn,omitempty"`
}

// DBUpgradePayload is Command.Payload of db_upgrade.
type DBUpgradePayload struct {
	ServiceID        string `json:"serviceId"`
	UpgradeID        string `json:"upgradeId"`
	FromVersion      string `json:"fromVersion"`
	ToVersion        string `json:"toVersion"`
	HelperImage      string `json:"helperImage"` // tianon/postgres-upgrade:<from>-to-<to>
	OldVolume        string `json:"oldVolume"`   // volume name (not the nc-vol- full name)
	NewVolume        string `json:"newVolume"`
	StopGraceSeconds int    `json:"stopGraceSeconds,omitempty"`
}

// DBUpgradeResult is ResultFrame.Data of db_upgrade.
type DBUpgradeResult struct {
	Message string `json:"message,omitempty"`
	Log     string `json:"log,omitempty"` // tail of the helper's output
}
