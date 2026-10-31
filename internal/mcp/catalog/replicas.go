package catalog

// PostgreSQL read replicas. Reading the replica group is allowed
// (database_get also carries it); creating, removing, rebuilding and promoting
// replicas move data and fence a database, so they are reserved to people in
// the web UI or the CLI. Promote is manual and needs a typed confirmation.
const (
	whyReplicaWrite = "Adds, removes, rebuilds or promotes a PostgreSQL read replica: it clones or deletes data, fences the old primary and rewrites consumers' variables. Reserved to people (web UI or CLI); promote is manual and needs a typed confirmation."
)

// replicaGenericEntries are the allowed replica operations without a curated tool.
var replicaGenericEntries = map[string]Generic{
	"getDatabaseReplication": rd(tData),
}

// replicaDenyEntries are the replica operations never callable over MCP.
var replicaDenyEntries = map[string]Deny{
	"addDatabaseReplica":     {Reason: ReasonInstanceAdmin, Why: whyReplicaWrite},
	"removeDatabaseReplica":  {Reason: ReasonInstanceAdmin, Why: whyReplicaWrite},
	"promoteDatabaseReplica": {Reason: ReasonInstanceAdmin, Why: whyReplicaWrite},
	"rebuildDatabaseReplica": {Reason: ReasonInstanceAdmin, Why: whyReplicaWrite},
}
