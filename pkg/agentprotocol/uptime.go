package agentprotocol

import "time"

// CapUptime marks an agent that probes its services for uptime and sends
// Probes frames.
const CapUptime = "uptime"

// TypeProbes (agent → control plane) carries probe results; TypeProbesAck
// (control plane → agent) acknowledges their durable storage.
const (
	TypeProbes    = "probes"
	TypeProbesAck = "probes_ack"
)

// UptimeInterval is how often an agent probes each service.
const UptimeInterval = 30 * time.Second

// UptimeSettings rides Reconcile: the services the agent must not probe
// (monitoring switched off). Nil from an older control plane: probe every
// service.
type UptimeSettings struct {
	Disabled []string `json:"disabled,omitempty"`
}

// Probe outcomes of one service in one cycle.
const (
	ProbeUp       = "up"       // every replica passed
	ProbeDegraded = "degraded" // some replicas failed, at least one passed
	ProbeDown     = "down"     // no replica passed (or none is running though some are wanted)
	ProbePaused   = "paused"   // no replica is wanted (stopped on purpose); not a check
)

// ProbeResult is one probe cycle of one service: its replicas probed the way
// its health check says (HTTP/TCP probe over the environment network, the
// container's Docker health for docker/exec checks, running for none; a
// route service's upstreams and health path).
type ProbeResult struct {
	ServiceID string    `json:"serviceId"`
	At        time.Time `json:"at"`
	Outcome   string    `json:"outcome"`
	// Replicas probed and how many passed.
	Replicas int `json:"replicas"`
	Passed   int `json:"passed"`
	// LatencyMs is the slowest passing HTTP/TCP probe; 0 for checks without a
	// network probe.
	LatencyMs int `json:"latencyMs,omitempty"`
	// Error describes the first failure ("HTTP 503", "connection refused",
	// "unhealthy: <docker health output>", "not running"); empty when up.
	Error string `json:"error,omitempty"`
}

// Probes is the agent → control-plane frame with probe results in time
// order. The agent keeps results until a ProbesAck for Seq arrives and sends
// them again after a reconnect (results older than ProbeRetention are
// dropped); the control plane ignores results it already stored.
type Probes struct {
	Seq     uint64        `json:"seq"`
	Results []ProbeResult `json:"results"`
}

// ProbesAck acknowledges durable storage of a Probes frame.
type ProbesAck struct {
	Seq uint64 `json:"seq"`
}

// ProbeRetention is how long the agent buffers unacknowledged results.
const ProbeRetention = 6 * time.Hour

// MaxProbeResults bounds the results of one Probes frame.
const MaxProbeResults = 1000
