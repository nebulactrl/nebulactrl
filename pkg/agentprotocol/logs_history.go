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
	// KindLogsFacets counts the values of a service's stored log lines that match
	// a query (facets, attributes and volume over time). Ephemeral, like
	// KindLogsQuery.
	KindLogsFacets = "logs_facets"
	// KindLogsDrainTest sends one test line through a drain from the agent's host.
	// Ephemeral. The drain's secrets ride in Command.Secrets.
	KindLogsDrainTest = "logs_drain_test"
	// TypeLogStatus is the agent → control-plane frame with the log store's
	// usage and the health of every drain. Sent every LogStatusInterval and on change.
	TypeLogStatus = "log_status"
)

func init() {
	AllowedKinds[KindLogsQuery] = true
	AllowedKinds[KindLogsFacets] = true
	AllowedKinds[KindLogsDrainTest] = true
	// Log lines and their counts are customer data the log store already keeps
	// under its own retention: the agent's result store must not keep a second,
	// unredacted copy for as long as it keeps command records.
	EphemeralResultKinds[KindLogsQuery] = true
	EphemeralResultKinds[KindLogsFacets] = true
	EphemeralResultKinds[KindLogsDrainTest] = true
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

// Request log byte caps. Request lines live in a store of their own
// (<state-dir>/requests), so their volume never evicts runtime lines; the age
// limit is the server's LogRetention.Days.
const (
	DefaultRequestLogMaxServiceBytes = int64(200) << 20
	DefaultRequestLogMaxServerBytes  = int64(2) << 30
)

// Log sources: which store a query reads. A source is also the `stream` facet
// value of its lines.
const (
	LogSourceRuntime  = StreamRuntime
	LogSourceRequests = StreamRequests
)

// CapRequestLogs is advertised in Facts.Capabilities by agents that keep the
// request store and honour LogsQuery.Source. An older agent ignores Source and
// answers from its runtime store, so the control plane reports such a server
// agent_outdated for the requests source instead of asking it.
const CapRequestLogs = "logs.requests"

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
	// Requests switches request logs on (nil: off). Request lines are stored on
	// the host only: no drain and no central log ever reads them.
	Requests *RequestLogsConfig `json:"requests,omitempty"`
}

// RequestLogsConfig is how the agent stores one line per proxied request of a
// routed service (RequestRecord as JSON). Unrouted requests are never stored.
type RequestLogsConfig struct {
	// KeepQuery stores the query string in RequestRecord.Query; off, the query
	// string is dropped before anything is written.
	KeepQuery bool `json:"keepQuery"`
	// KeepAssets stores successful (2xx, 304) requests of RequestKindAsset;
	// off, only the failed ones are stored.
	KeepAssets bool `json:"keepAssets"`
	// Retention: Days follows the server's log retention; the byte caps default
	// to DefaultRequestLogMaxServiceBytes and DefaultRequestLogMaxServerBytes.
	Retention LogRetention `json:"retention"`
}

// RequestRecord is the text of one stored request line, as JSON with these
// keys in this order, so `@method:`, `@path:`, `@duration_ms:>500` and
// `status:5*` query it. The client address is never stored whole: IP is the
// /24 (IPv4) or /48 (IPv6) network it belongs to.
type RequestRecord struct {
	Method string `json:"method"`
	Host   string `json:"host"`
	Path   string `json:"path"`
	// Kind is what was requested: one of the RequestKind* values.
	Kind string `json:"kind"`
	// Level follows the status (error for 5xx, warn for 4xx, info otherwise),
	// so `level:` means the same for request lines as for runtime lines and
	// nothing in the path or user agent can set it.
	Level      string  `json:"level"`
	Query      string  `json:"query,omitempty"`
	Status     int     `json:"status"`
	DurationMs float64 `json:"duration_ms"`
	Bytes      int64   `json:"bytes"`
	BytesIn    int64   `json:"bytes_in,omitempty"`
	UserAgent  string  `json:"ua,omitempty"`
	IP         string  `json:"ip,omitempty"`
	Process    string  `json:"process,omitempty"`
	Component  string  `json:"component,omitempty"`
}

// Request kinds (RequestRecord.Kind), from the response's Content-Type and,
// without one, the path's extension.
const (
	// RequestKindPage is an HTML document.
	RequestKindPage = "page"
	// RequestKindAPI is a JSON, XML, form, gRPC or event-stream exchange.
	RequestKindAPI = "api"
	// RequestKindAsset is a script, stylesheet, image, font, media file,
	// source map or web manifest.
	RequestKindAsset = "asset"
	// RequestKindOther is anything else (a redirect without a body, plain text).
	RequestKindOther = "other"
)

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
// log, within a time range, filtered by the log search language.
type LogsQuery struct {
	ServiceID string `json:"serviceId"`
	// ServiceName is what the `service:` facet matches (the control plane sends
	// the display name, like the live view does).
	ServiceName string `json:"serviceName,omitempty"`
	// Source picks the store: LogSourceRuntime ("" too) or LogSourceRequests.
	Source string    `json:"source,omitempty" enum:"runtime,requests"`
	From   time.Time `json:"from,omitempty"` // inclusive; zero = the oldest line
	To     time.Time `json:"to,omitempty"`   // exclusive; zero = now
	Query  string    `json:"query,omitempty"`
	Limit  int       `json:"limit,omitempty"`
	// Cursor continues a previous page (LogsQueryResult.NextCursor).
	Cursor string `json:"cursor,omitempty"`
	Order  string `json:"order,omitempty" enum:"desc,asc"`
}

// Limits of a facets query.
const (
	LogFacetsDefaultBuckets = 60
	LogFacetsMaxBuckets     = 240
	// LogFacetsMaxValues is how many values one facet or attribute reports.
	LogFacetsMaxValues = 20
	// LogFacetsMaxAttrs is how many attribute paths a result reports.
	LogFacetsMaxAttrs = 50
)

// LogsFacetsQuery is the payload of KindLogsFacets: the counts of one
// service's stored lines of one source that match Query within [From, To).
type LogsFacetsQuery struct {
	ServiceID   string    `json:"serviceId"`
	ServiceName string    `json:"serviceName,omitempty"`
	Source      string    `json:"source,omitempty" enum:"runtime,requests"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Query       string    `json:"query,omitempty"`
	// Buckets is the number of equal histogram buckets over [From, To)
	// (0: LogFacetsDefaultBuckets, at most LogFacetsMaxBuckets).
	Buckets int `json:"buckets,omitempty"`
}

// FacetValue is one value of a facet or attribute and how many matching lines carry it.
type FacetValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// FacetField is the top values of one facet (Key without "@": level, stream,
// status, replica, deployment, service) or attribute (Key with "@", e.g.
// "@method"), most frequent first. Count is how many matching lines carry the
// key at all. Numeric keys report Min and Max over the matching lines.
type FacetField struct {
	Key     string       `json:"key"`
	Count   int64        `json:"count"`
	Numeric bool         `json:"numeric,omitempty"`
	Min     *float64     `json:"min,omitempty"`
	Max     *float64     `json:"max,omitempty"`
	Values  []FacetValue `json:"values"`
}

// HistogramBucket is the matching lines in [Start, Start+width). Errors counts
// those whose level is error or fatal, or whose status is 500 or more.
type HistogramBucket struct {
	Start  time.Time `json:"start"`
	Count  int64     `json:"count"`
	Errors int64     `json:"errors"`
}

// LogsFacetsResult is ResultFrame.Data of KindLogsFacets.
type LogsFacetsResult struct {
	// Matched is how many lines matched; Scanned how many were read.
	Matched int64 `json:"matched"`
	Scanned int64 `json:"scanned"`
	// Partial reports that the scan budget ended before the range was read
	// (newest first): the counts cover [PartialFrom, To) only.
	Partial     bool              `json:"partial,omitempty"`
	PartialFrom *time.Time        `json:"partialFrom,omitempty"`
	Facets      []FacetField      `json:"facets"`
	Attrs       []FacetField      `json:"attrs"`
	Histogram   []HistogramBucket `json:"histogram"`
	QueryErrors []string          `json:"queryErrors,omitempty"`
	Capturing   bool              `json:"capturing"`
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
	// RequestsCapturing reports request logs on; RequestsTotalBytes and
	// Requests are the request store's usage (separate from the runtime store).
	RequestsCapturing  bool              `json:"requestsCapturing,omitempty"`
	RequestsTotalBytes int64             `json:"requestsTotalBytes,omitempty"`
	Requests           []LogServiceUsage `json:"requests,omitempty"`
}
