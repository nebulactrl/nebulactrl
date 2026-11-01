package agentprotocol

import "time"

// Postgres read replicas.
//
// A replica is an ordinary managed-database service of the primary's
// environment. The primary is prepared by typed db_admin actions (role,
// pg_hba block, slot cap); the replica is cloned by KindDBReplicaClone (a
// one-off pg_basebackup into its data volume) and then deployed normally; the
// agent polls both sides over the in-container socket (no password) and
// reports ObservedService.Replication.
//
// Secrets never ride in a payload: the replication password travels in
// Command.Secrets under SecretReplPassword (clone) or SecretDBAdminNewPassword
// (repl_prepare), and the replica container reads it from its environment.

const (
	// CapReplication is advertised in Facts.Capabilities by agents that can
	// prepare primaries, clone replicas and report replication status.
	CapReplication = "db.replication"

	// KindDBReplicaClone runs pg_basebackup in a one-off container into the
	// replica's data volume. Payload: ReplicaClonePayload; secret
	// SecretReplPassword; the result data is a ReplicaCloneResult.
	KindDBReplicaClone = "db_replica_clone"
)

func init() { AllowedKinds[KindDBReplicaClone] = true }

// SecretReplPassword is the Command.Secrets key of the replication role's password.
const SecretReplPassword = "repl:password"

// db_admin actions of replication (DBAdminPayload.Action).
const (
	// DBActionReplPrepare creates the replication role, the managed pg_hba.conf
	// block and max_slot_wal_keep_size on the primary.
	DBActionReplPrepare = "repl_prepare"
	// DBActionReplDrop removes a replica's slot, role and pg_hba.conf block.
	DBActionReplDrop = "repl_drop"
	// DBActionReplFreeze makes the primary read-only, ends other sessions and checkpoints.
	DBActionReplFreeze = "repl_freeze"
	// DBActionReplUnfreeze reverts repl_freeze (also on a restarted old primary).
	DBActionReplUnfreeze = "repl_unfreeze"
	// DBActionReplFinalLSN reads the final checkpoint location of a STOPPED
	// primary (a one-off pg_controldata on its volume).
	DBActionReplFinalLSN = "repl_final_lsn"
	// DBActionReplPromote runs pg_promote on a replica and cleans its replica config.
	DBActionReplPromote = "repl_promote"
	// DBActionReplReconfigure rewrites the replica's primary_conninfo include file.
	DBActionReplReconfigure = "repl_reconfigure"
	// DBActionReplStatus reads the replication status once (the poller uses the same plan).
	DBActionReplStatus = "repl_status"
)

// ReplAdmin carries the arguments of the replication actions in DBAdminPayload.
// It holds no secret.
type ReplAdmin struct {
	// Role is the replication role (nebula_r_<replicaId8>).
	Role string `json:"role,omitempty"`
	// Slot is the physical replication slot (nc_r_<replicaId8>), also the
	// replica's application_name.
	Slot string `json:"slot,omitempty"`
	// MaxSlotWALKeepMB is max_slot_wal_keep_size in MB (repl_prepare).
	MaxSlotWALKeepMB int `json:"maxSlotWalKeepMb,omitempty"`
	// LSN is the position repl_status / repl_promote compare against.
	LSN string `json:"lsn,omitempty"`
	// Host and Port are what the replica dials (repl_reconfigure).
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// DataVolume names the stopped primary's data volume (repl_final_lsn).
	DataVolume string `json:"dataVolume,omitempty"`
}

// DatabaseRuntime is the replication part of a database service's DeployPayload.
type DatabaseRuntime struct {
	Replication *ReplicationDesired `json:"replication,omitempty"`
}

// Roles of a replication member (ReplicationDesired.Role, ReplicationStatus.Role).
const (
	ReplRolePrimary = "primary"
	ReplRoleReplica = "replica"
	ReplRoleUnknown = "unknown"
)

// ReplicationDesired tells the agent to poll a database for replication status.
type ReplicationDesired struct {
	// Role is ReplRolePrimary (it has replicas) or ReplRoleReplica.
	Role string `json:"role"`
	// User is the superuser name of the local-socket queries.
	User string `json:"user"`
	// Slot is the replica's slot and application_name (replica only).
	Slot string `json:"slot,omitempty"`
	// PollSeconds defaults to 30.
	PollSeconds int `json:"pollSeconds,omitempty"`
}

// ReplicaClonePayload is Command.Payload of db_replica_clone.
type ReplicaClonePayload struct {
	// DeploymentID, when set, is a deployment on this server that receives the
	// clone output as deploy log lines. A running clone is cancelled by a
	// cancel_deploy whose DeploymentID is the clone command's id.
	DeploymentID string `json:"deploymentId,omitempty"`

	ServiceID string `json:"serviceId"`
	Engine    string `json:"engine"`
	Version   string `json:"version"`
	Flavor    string `json:"flavor,omitempty"`
	// DataVolume is the replica's data volume (not the full nc-vol- name).
	DataVolume string `json:"dataVolume"`
	// Host and Port are what the clone dials: the primary's slug and port on the
	// same server, or the replication link alias and mesh port across servers.
	Host string `json:"host"`
	Port int    `json:"port"`
	// User is the replication role, Slot the physical slot created by the clone.
	User string `json:"user"`
	Slot string `json:"slot"`
	// Networks are the networks the replica will use; the clone container joins them.
	Networks []string `json:"networks"`
	// TimeoutSeconds bounds the clone (default 6 hours).
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// ReplicaCloneResult is ResultFrame.Data of db_replica_clone.
type ReplicaCloneResult struct {
	Message    string `json:"message,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// ReplicationSlot is one physical replication slot of a primary.
type ReplicationSlot struct {
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	WALStatus   string `json:"walStatus,omitempty"` // reserved | extended | unreserved | lost
	SafeWALSize *int64 `json:"safeWalSize,omitempty"`
}

// ReplicationStandby is one connected standby as seen by the primary
// (pg_stat_replication).
type ReplicationStandby struct {
	ApplicationName string `json:"applicationName"`
	State           string `json:"state"`
	SyncState       string `json:"syncState,omitempty"`
	LagBytes        int64  `json:"lagBytes"`
	ReplayLagMs     *int64 `json:"replayLagMs,omitempty"`
}

// ReplicationStatus is the observed replication state of one database
// container, reported in ObservedService.Replication every poll.
type ReplicationStatus struct {
	Role       string `json:"role"`
	InRecovery bool   `json:"inRecovery"`
	// Replica side (pg_stat_wal_receiver and the receive/replay positions).
	ReceiverStatus string `json:"receiverStatus,omitempty"`
	ReceiveLSN     string `json:"receiveLsn,omitempty"`
	ReplayLSN      string `json:"replayLsn,omitempty"`
	ReplayLagBytes int64  `json:"replayLagBytes,omitempty"`
	// Primary side.
	Slots    []ReplicationSlot    `json:"slots,omitempty"`
	Standbys []ReplicationStandby `json:"standbys,omitempty"`
	// CheckedAt is when the agent ran the queries; Error is set when it could not.
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
}
