package agentprotocol

// Restore of a database from an uploaded dump file. The file never touches a
// disk or a destination: the control plane relays the upload to the agent as
// an interactive stream (like the file browser's upload) and the agent pipes
// it into the engine's own client inside the database container.
//
// Frames of one exchange carry the request ID in Envelope.ID:
//
//	control plane → agent: dumprestore_request, then fs_data (the file, under
//	the stream window; Error aborts, EOF ends it)
//	agent → control plane: dumprestore_event (ready, log lines, then done or an
//	error), stream_ack per consumed chunk
//
// The database password travels inside the request only (never persisted).
const (
	// TypeDumpRestoreRequest is control plane → agent.
	TypeDumpRestoreRequest = "dumprestore_request"
	// TypeDumpRestoreEvent is agent → control plane.
	TypeDumpRestoreEvent = "dumprestore_event"
)

// Formats of an uploaded dump (after removing an optional gzip layer).
const (
	DumpPGCustom = "pg_custom"     // pg_dump --format=custom (-Fc)
	DumpPGTar    = "pg_tar"        // pg_dump --format=tar (-Ft)
	DumpSQL      = "sql"           // plain SQL: pg_dump -Fp, mysqldump, mariadb-dump
	DumpMongo    = "mongo_archive" // mongodump --archive
)

// DumpFormatSupported reports whether an uploaded dump of format can be
// restored into a database of engine.
func DumpFormatSupported(engine, format string) bool {
	switch engine {
	case "postgres":
		return format == DumpPGCustom || format == DumpPGTar || format == DumpSQL
	case "mysql", "mariadb":
		return format == DumpSQL
	case "mongodb":
		return format == DumpMongo
	}
	return false
}

// DumpRestoreSupported reports whether engine can be restored from an uploaded
// dump (the engines whose backups can be restored).
func DumpRestoreSupported(engine string) bool { return RestoreSupported(engine) }

// Dump restore event stages.
const (
	// DumpReady: the agent accepted the request and waits for fs_data frames.
	DumpReady = "ready"
	// DumpProgress: Lines carries progress output.
	DumpProgress = "progress"
	// DumpDone: the restore finished; Message summarises it.
	DumpDone = "done"
)

// Dump restore error codes.
const (
	DumpErrNotRunning = "not_running" // the database container is not running (nothing was changed)
	DumpErrBadRequest = "bad_request" // the request itself is invalid (nothing was changed)
	DumpErrFailed     = "failed"      // the restore ran and failed
)

// DumpRestoreRequest starts a restore from an uploaded dump.
type DumpRestoreRequest struct {
	RequestID string       `json:"requestId"`
	RestoreID string       `json:"restoreId"`
	Target    BackupTarget `json:"target"` // the database to restore INTO
	Format    string       `json:"format"` // DumpPGCustom, DumpPGTar, DumpSQL, DumpMongo
	// Gzip: the file is a gzip stream of Format.
	Gzip bool `json:"gzip,omitempty"`
	// Replace: the database already holds data that the restore replaces
	// (in-place mode). A new, empty database sets it to false.
	Replace bool `json:"replace,omitempty"`
	// Size is the exact length of the file the fs_data frames carry.
	Size int64 `json:"size"`
	// Secrets holds SecretDBPassword. It is a stream frame, never persisted.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// DumpRestoreEvent is one agent → control plane message of a dump restore.
type DumpRestoreEvent struct {
	RequestID string   `json:"requestId"`
	Stage     string   `json:"stage,omitempty"`
	Lines     []string `json:"lines,omitempty"`
	Message   string   `json:"message,omitempty"`
	// Error ends the exchange. Code classifies it (DumpErr*).
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}
