package agentprotocol

// Database data access: the Data tab of a managed SQL database lists tables,
// pages through one table and runs SQL, all through ephemeral commands the
// control plane writes straight to the agent's socket (Hub.Request).

const (
	// KindDBQuery runs one data operation (tables, browse or sql) against a
	// managed SQL database on the agent's host. Ephemeral: the rows ride in the
	// result and are never persisted, neither by the control plane nor by the
	// agent's own result store (see EphemeralResultKinds).
	KindDBQuery = "db_query"
)

func init() { AllowedKinds[KindDBQuery] = true }

// EphemeralResultKinds are command kinds whose result Data is sent to the
// control plane but never stored by the agent (its result store replays results
// after a reconnect; rows of a customer database must not sit in it).
var EphemeralResultKinds = map[string]bool{KindDBQuery: true}

// Reserved keys in Command.Secrets of db_query commands (attached at send time,
// never persisted). The password key is SecretDBPassword, shared with the
// backup commands.
const SecretDBUser = "db_user"

// db_query operations.
const (
	DBOpTables = "tables"
	DBOpBrowse = "browse"
	DBOpSQL    = "sql"
)

// DBQueryPayload is Command.Payload of db_query.
type DBQueryPayload struct {
	ServiceID string `json:"serviceId"`
	Engine    string `json:"engine"` // postgres | mysql | mariadb | clickhouse
	Database  string `json:"database"`
	Op        string `json:"op"` // DBOpTables | DBOpBrowse | DBOpSQL
	// Schema and Table name the table of a browse. MySQL and MariaDB have no
	// schema level below the database: Schema is then empty.
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table,omitempty"`
	Offset int    `json:"offset,omitempty"`
	// SQL is the statement text of a sql op (several statements allowed).
	SQL string `json:"sql,omitempty"`
	// AllowWrites lifts the read-only session of a sql op. Tables and browse are
	// always read-only.
	AllowWrites    bool `json:"allowWrites,omitempty"`
	TimeoutSeconds int  `json:"timeoutSeconds"`
	// MaxRows bounds the rows kept per result set.
	MaxRows int `json:"maxRows"`
}

// DBTable is one entry of the table listing.
type DBTable struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Kind        string `json:"kind"` // table | view | other
	RowEstimate *int64 `json:"rowEstimate,omitempty"`
}

// DBColumn is the name and type of one result column.
type DBColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// DBResultSet is one result of a statement. A nil cell is SQL NULL; every other
// value is its text form.
type DBResultSet struct {
	Columns  []DBColumn  `json:"columns"`
	Rows     [][]*string `json:"rows"`
	RowCount int         `json:"rowCount"`
	// Truncated is set when more rows existed than were kept.
	Truncated bool `json:"truncated,omitempty"`
	// CommandTag is the server's completion tag of a statement that returns no
	// rows (for example "INSERT 0 1").
	CommandTag string `json:"commandTag,omitempty"`
}

// DBQueryResult is ResultFrame.Data of db_query.
type DBQueryResult struct {
	Tables         []DBTable     `json:"tables,omitempty"`
	Results        []DBResultSet `json:"results,omitempty"`
	DurationMs     int64         `json:"durationMs"`
	TruncatedCells bool          `json:"truncatedCells,omitempty"`
}
