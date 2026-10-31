package agentprotocol

import "time"

// Log history and log drains.
//
// Every agent captures the runtime output of every managed container into an
// append-only, compressed, time-indexed store under <state-dir>/logs, whether
// or not anyone is watching. The control plane never stores runtime lines: it
// asks the agent (history queries, an ephemeral command with a result) and it
// pushes the retention limits and the log drains to the agent in Reconcile.
// Drains ship from the agent; their credentials ride in Reconcile.Secrets
// (send-time, never persisted).

const (
	// KindLogsQuery reads a page of a service's stored runtime log. Ephemeral:
	// the control plane writes it straight to the socket and waits for the result.
	KindLogsQuery = "logs_query"
	// KindLogsDrainTest sends one test line through a drain from the agent's host.
	// Ephemeral. The drain's secrets ride in Command.Secrets.
	KindLogsDrainTest = "logs_drain_test"
	// TypeLogStatus is the agent → control-plane frame with the log store's
	// usage and the health of every drain. Sent every LogStatusInterval and on change.
	TypeLogStatus = "log_status"
)

func init() {
	AllowedKinds[KindLogsQuery] = true
	AllowedKinds[KindLogsDrainTest] = true
}

// LogStatusInterval is how often the agent reports the log store and drains.
const LogStatusInterval = 30 * time.Second

// Retention defaults and bounds. Every limit is configurable per server.
const (
	DefaultLogRetentionDays   = 7
	MaxLogRetentionDays       = 90
	DefaultLogMaxServiceBytes = int64(500) << 20
	DefaultLogMaxServerBytes  = int64(5) << 30
	// MinLogMaxBytes is the smallest per-service or per-server cap (one segment
	// must fit).
	MinLogMaxBytes = int64(32) << 20
	// DefaultDrainBufferBytes bounds how much undelivered log a drain keeps on
	// disk (it reads the store from its own position; past the bound the oldest
	// lines are skipped and counted as dropped).
	DefaultDrainBufferBytes = int64(256) << 20
)

// Log drain kinds.
const (
	DrainLoki   = "loki"
	DrainOTLP   = "otlp"
	DrainSyslog = "syslog"
	DrainHTTP   = "http"
)

// Syslog transports.
const (
	SyslogUDP    = "udp"
	SyslogTCP    = "tcp"
	SyslogTCPTLS = "tcp+tls"
)

// Generic HTTP drain body formats.
const (
	HTTPFormatJSONArray = "json_array" // [{...},{...}]
	HTTPFormatNDJSON    = "ndjson"     // {...}\n{...}\n
)

// Drain health states reported in LogDrainStatus.State.
const (
	DrainOK       = "ok"       // the last delivery succeeded (or nothing to send yet)
	DrainRetrying = "retrying" // a delivery failed; retrying with backoff
	DrainFailing  = "failing"  // five or more consecutive failures
	DrainDisabled = "disabled"
)

// Send-time secret names of a drain. The Reconcile.Secrets key is
// LogDrainSecretKey(drainID, name).
const (
	LogDrainSecretToken    = "token"    // bearer token (Loki, OTLP, HTTP)
	LogDrainSecretUsername = "username" // basic auth user (Loki, HTTP)
	LogDrainSecretPassword = "password" // basic auth password (Loki, HTTP)
	LogDrainSecretHeaders  = "headers"  // JSON object of extra request headers (OTLP, HTTP, Loki)
)

// LogDrainSecretKey returns the Reconcile.Secrets (or Command.Secrets) key of
// one secret of a drain.
func LogDrainSecretKey(drainID, name string) string { return "logdrain:" + drainID + ":" + name }

// LogRedactSecretKey returns the Reconcile.Secrets key that carries the secret
// values of one service as a JSON array of strings. The agent masks them in every
// line a drain ships (a drain sends lines to third parties; history is redacted
// by the control plane when it is read).
func LogRedactSecretKey(serviceID string) string { return "logredact:" + serviceID }

// LogRetention bounds the store of one server. Zero values mean the defaults.
type LogRetention struct {
	Days            int   `json:"days"`
	MaxServiceBytes int64 `json:"maxServiceBytes"`
	MaxServerBytes  int64 `json:"maxServerBytes"`
}

// WithDefaults replaces zero fields by the defaults.
func (r LogRetention) WithDefaults() LogRetention {
	if r.Days <= 0 {
		r.Days = DefaultLogRetentionDays
	}
	if r.MaxServiceBytes <= 0 {
		r.MaxServiceBytes = DefaultLogMaxServiceBytes
	}
	if r.MaxServerBytes <= 0 {
		r.MaxServerBytes = DefaultLogMaxServerBytes
	}
	return r
}

// LogServiceMeta describes a service the agent hosts: it labels shipped lines
// and resolves the drain filters and the `service:` query facet.
type LogServiceMeta struct {
	ServiceID     string `json:"serviceId"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	ProjectID     string `json:"projectId"`
	Project       string `json:"project"`
	EnvironmentID string `json:"environmentId"`
	Environment   string `json:"environment"`
}

// LogsConfig is the control plane's log setting for one server, sent in every
// Reconcile (nil = unchanged).
type LogsConfig struct {
	// Capture turns history capture on. When false the agent stops capturing;
	// stored history stays until it expires.
	Capture    bool         `json:"capture"`
	Retention  LogRetention `json:"retention"`
	ServerName string       `json:"serverName,omitempty"`
	// Now is the control plane's clock when it built this config. A drain's age
	// is Now - LogDrain.CreatedAt, both on the control plane's clock, so the
	// agent can place the creation on its own clock without trusting the two
	// clocks to agree.
	Now time.Time `json:"now,omitempty"`
	// Services is every service whose current deployment runs on this server.
	Services []LogServiceMeta `json:"services,omitempty"`
	Drains   []LogDrain       `json:"drains,omitempty"`
}

// LogDrain is one destination the agent ships log lines to. The kind's own
// config is set (the others nil). A line is shipped when its service passes the
// scope (AllProjects, ProjectIDs, ServiceIDs) and Query (the log search
// language; empty matches everything). It ships what is written after it was
// created: lines older than CreatedAt are never shipped, however late the agent
// stored them.
type LogDrain struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind" enum:"loki,otlp,syslog,http"`
	Enabled bool   `json:"enabled"`
	// CreatedAt is when the control plane created the drain (zero = no lower
	// bound on line timestamps).
	CreatedAt time.Time `json:"createdAt,omitempty"`

	Loki   *LokiDrainConfig   `json:"loki,omitempty"`
	OTLP   *OTLPDrainConfig   `json:"otlp,omitempty"`
	Syslog *SyslogDrainConfig `json:"syslog,omitempty"`
	HTTP   *HTTPDrainConfig   `json:"http,omitempty"`

	AllProjects bool     `json:"allProjects"`
	ProjectIDs  []string `json:"projectIds,omitempty"`
	// ServiceIDs narrows the scope to these services (empty = every service of
	// the projects in scope).
	ServiceIDs []string `json:"serviceIds,omitempty"`
	Query      string   `json:"query,omitempty"`

	// BufferMaxBytes bounds the undelivered backlog (0 = DefaultDrainBufferBytes).
	BufferMaxBytes int64 `json:"bufferMaxBytes,omitempty"`
}

// LokiDrainConfig ships to a Loki push endpoint (POST /loki/api/v1/push, JSON).
// Credentials: LogDrainSecretUsername/Password (basic), or LogDrainSecretToken
// (bearer), plus optional LogDrainSecretHeaders.
type LokiDrainConfig struct {
	URL      string `json:"url"`
	TenantID string `json:"tenantId,omitempty"` // X-Scope-OrgID
	// Labels are static stream labels added to every stream (service, project,
	// environment, server and level are always set).
	Labels map[string]string `json:"labels,omitempty"`
}

// OTLPDrainConfig ships OTLP/HTTP logs (POST <url> with a JSON ExportLogsServiceRequest,
// normally <endpoint>/v1/logs). Credentials: LogDrainSecretToken (bearer) and/or
// LogDrainSecretHeaders (for example an API key header).
type OTLPDrainConfig struct {
	URL string `json:"url"`
	// ResourceAttributes are added to the resource of every export.
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
}

// SyslogDrainConfig ships RFC 5424 messages. TCP and TLS use octet-counting
// framing (RFC 6587); UDP sends one datagram per message (truncated to 8 KiB).
type SyslogDrainConfig struct {
	Address  string `json:"address"`  // host:port
	Protocol string `json:"protocol"` // udp, tcp, tcp+tls
	AppName  string `json:"appName,omitempty"`
	Facility int    `json:"facility,omitempty"` // 0-23, default 1 (user)
	// TLSServerName overrides the name the server certificate is checked against.
	TLSServerName string `json:"tlsServerName,omitempty"`
	// TLSInsecureSkipVerify accepts any certificate (self-signed collectors).
	TLSInsecureSkipVerify bool `json:"tlsInsecureSkipVerify,omitempty"`
}

// HTTPDrainConfig ships batches as JSON to any HTTP intake (Datadog, Better
// Stack, Axiom, a webhook of your own). Credentials: LogDrainSecretHeaders (the
// usual API key header), LogDrainSecretToken (bearer) or username and password.
type HTTPDrainConfig struct {
	URL    string `json:"url"`
	Method string `json:"method,omitempty"` // POST (default) or PUT
	Format string `json:"format,omitempty"` // json_array (default) or ndjson
	// TimestampField and MessageField name the keys of each record (defaults
	// "timestamp" and "message").
	TimestampField string `json:"timestampField,omitempty"`
	MessageField   string `json:"messageField,omitempty"`
	// ExtraFields are static fields added to every record (for example
	// {"ddsource":"nebulactrl"} for Datadog).
	ExtraFields map[string]string `json:"extraFields,omitempty"`
}

// Limits of a log query.
const (
	LogQueryDefaultLimit = 500
	LogQueryMaxLimit     = 2000
	// LogQueryMaxBytes bounds the text of one result (it travels in one frame).
	LogQueryMaxBytes = 1 << 20
)

// Log query orders.
const (
	LogOrderDesc = "desc" // newest first (default)
	LogOrderAsc  = "asc"
)

// LogsQuery is the payload of KindLogsQuery: a page of one service's stored
// runtime log, within a time range, filtered by the log search language.
type LogsQuery struct {
	ServiceID string `json:"serviceId"`
	// ServiceName is what the `service:` facet matches (the control plane sends
	// the display name, like the live view does).
	ServiceName string    `json:"serviceName,omitempty"`
	From        time.Time `json:"from,omitempty"` // inclusive; zero = the oldest line
	To          time.Time `json:"to,omitempty"`   // exclusive; zero = now
	Query       string    `json:"query,omitempty"`
	Limit       int       `json:"limit,omitempty"`
	// Cursor continues a previous page (LogsQueryResult.NextCursor).
	Cursor string `json:"cursor,omitempty"`
	Order  string `json:"order,omitempty" enum:"desc,asc"`
}

// HistoryLine is one stored log line.
type HistoryLine struct {
	// Seq orders lines of one service by arrival (it is the cursor).
	Seq          uint64    `json:"seq"`
	TS           time.Time `json:"ts"`
	Text         string    `json:"text"`
	Replica      int       `json:"replica"`
	DeploymentID string    `json:"deploymentId,omitempty"`
}

// LogsQueryResult is ResultFrame.Data of KindLogsQuery.
type LogsQueryResult struct {
	Lines []HistoryLine `json:"lines"`
	// NextCursor is set when more lines may follow (empty: the range is exhausted).
	NextCursor string `json:"nextCursor,omitempty"`
	// Partial reports that the scan budget ended before the page was full: keep
	// paging with NextCursor to search further.
	Partial bool `json:"partial,omitempty"`
	// QueryErrors are the messages of the parts of the query that were ignored.
	QueryErrors []string `json:"queryErrors,omitempty"`
	// Oldest and Newest bound what the agent retains for the service.
	Oldest *time.Time `json:"oldest,omitempty"`
	Newest *time.Time `json:"newest,omitempty"`
	// Capturing is false when history capture is switched off on this server.
	Capturing bool `json:"capturing"`
}

// LogsDrainTest is the payload of KindLogsDrainTest.
type LogsDrainTest struct {
	Drain LogDrain `json:"drain"`
}

// LogsDrainTestResult is ResultFrame.Data of KindLogsDrainTest.
type LogsDrainTestResult struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode,omitempty"`
}

// LogServiceUsage is the stored volume of one service.
type LogServiceUsage struct {
	ServiceID string     `json:"serviceId"`
	Bytes     int64      `json:"bytes"`
	Lines     int64      `json:"lines"`
	Oldest    *time.Time `json:"oldest,omitempty"`
	Newest    *time.Time `json:"newest,omitempty"`
}

// LogDrainStatus is the health of one drain on one server.
type LogDrainStatus struct {
	ID            string     `json:"id"`
	State         string     `json:"state" enum:"ok,retrying,failing,disabled"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	LastErrorAt   *time.Time `json:"lastErrorAt,omitempty"`
	SentLines     int64      `json:"sentLines"`
	DroppedLines  int64      `json:"droppedLines"`
	// BacklogBytes is how much not-yet-delivered log the drain holds on disk.
	BacklogBytes int64 `json:"backlogBytes"`
}

// LogStatus is the body of TypeLogStatus.
type LogStatus struct {
	Capturing  bool              `json:"capturing"`
	Retention  LogRetention      `json:"retention"`
	TotalBytes int64             `json:"totalBytes"`
	Services   []LogServiceUsage `json:"services"`
	Drains     []LogDrainStatus  `json:"drains"`
}
