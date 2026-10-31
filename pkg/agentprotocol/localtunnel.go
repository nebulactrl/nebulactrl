package agentprotocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Instance services tunnel. The built-in S3, image registry and Loki run beside
// the control plane and are never published. A server reaches them through the
// agent's existing outbound WebSocket: the agent listens on fixed 127.0.0.1
// ports and carries every accepted TCP connection to the control plane as one
// tunnel stream; the control plane hands the stream to the service. Unlike the
// interactive streams of exec.go, tunnel streams are initiated by the agent,
// carry raw bytes in WebSocket binary messages (no base64) and are flow
// controlled with credit windows, so a bulk upload cannot starve heartbeats.
//
// Control frames (open, opened, ack, close) are ordinary JSON text frames; the
// stream is identified by No in the body, unique per connection and chosen by
// the agent. Data frames are binary messages (see TunnelFrame).
//
// Capability negotiation: an agent that can tunnel advertises
// CapInstanceTunnel in Facts.Capabilities. The control plane offers endpoints
// (Reconcile.Endpoints) only to such agents and answers tunnel_open from any
// other peer with TunnelCodeUnsupported. A control plane that does not know the
// feature never sends Endpoints, so the agent opens no listener and never sends
// a tunnel frame.

// CapInstanceTunnel is advertised in Facts.Capabilities by agents that can
// carry instance service tunnels.
const CapInstanceTunnel = "instance.tunnel"

// Tunnel frame types.
const (
	// Agent → control plane.
	TypeTunnelOpen = "tunnel_open"
	// Control plane → agent.
	TypeTunnelOpened = "tunnel_opened"
	// Both directions.
	TypeTunnelAck   = "tunnel_ack"
	TypeTunnelClose = "tunnel_close"
)

// Tunnel services and their fixed agent-local ports (127.0.0.1 only).
const (
	TunnelS3       = "s3"
	TunnelRegistry = "registry"
	TunnelLoki     = "loki"

	LocalPortS3       = 18333
	LocalPortRegistry = 18500
	LocalPortLoki     = 18100
)

// LocalHost is the only address the agent listens on for tunnel endpoints.
const LocalHost = "127.0.0.1"

// Tunnel sizes and limits shared by both ends.
const (
	// TunnelChunk is the most payload bytes in one binary data frame.
	TunnelChunk = 64 << 10
	// TunnelStreamWindow is the unacknowledged payload a sender may have in
	// flight per stream and direction.
	TunnelStreamWindow = 2 << 20
	// TunnelConnWindow bounds the payload bytes of all streams of one
	// connection that are queued for the socket but not yet written.
	TunnelConnWindow = 16 << 20
	// MaxTunnelStreams is the most simultaneous tunnel streams per agent.
	MaxTunnelStreams = 32
	// MaxTunnelStreamsPerTeam, MaxTunnelStreamsPerOwner and
	// MaxTunnelStreamsGlobal bound the streams of all agents of one team, of all
	// the teams one person created, and of the whole control plane.
	MaxTunnelStreamsPerTeam  = 64
	MaxTunnelStreamsPerOwner = 64
	MaxTunnelStreamsGlobal   = 256
	// MaxTunnelOpensPerSecond is the sustained open rate per agent.
	MaxTunnelOpensPerSecond = 20
)

// Tunnel timeouts shared by both ends.
const (
	// TunnelIdleTimeout closes a stream without payload in either direction.
	TunnelIdleTimeout = 5 * time.Minute
	// TunnelCreditTimeout closes a stream whose sender waited this long for credit.
	TunnelCreditTimeout = 120 * time.Second
	// TunnelOpenTimeout is how long the agent waits for tunnel_opened.
	TunnelOpenTimeout = 10 * time.Second
	// TunnelLinger is how long a locally closed stream waits for the peer's FIN.
	TunnelLinger = 30 * time.Second
)

// Result codes of tunnel_opened and tunnel_close.
const (
	TunnelCodeUnsupported = "unsupported"  // peer did not advertise CapInstanceTunnel
	TunnelCodeUnavailable = "unavailable"  // no acceptor installed
	TunnelCodeBadRequest  = "bad_request"  // unknown service or duplicate stream number
	TunnelCodeRateLimited = "rate_limited" // more than MaxTunnelOpensPerSecond
	TunnelCodeLimit       = "limit"        // a stream limit is reached
	TunnelCodeError       = "error"        // the service side failed; Reason says why
	TunnelCodeStalled     = "stalled"      // no credit for TunnelCreditTimeout
	TunnelCodeIdle        = "idle"         // idle for TunnelIdleTimeout
	TunnelCodeProtocol    = "protocol"     // the peer broke the stream protocol
	TunnelCodeClosed      = "closed"       // closed without a graceful FIN exchange
)

// TunnelOpen asks the control plane to open a stream to Service.
type TunnelOpen struct {
	No      uint32 `json:"no"`
	Service string `json:"service"`
}

// TunnelOpened answers TunnelOpen. Code is empty when the stream is open.
type TunnelOpened struct {
	No    uint32 `json:"no"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// TunnelAck credits Bytes of payload back to the sender of a stream.
type TunnelAck struct {
	No    uint32 `json:"no"`
	Bytes int    `json:"bytes"`
}

// TunnelClose aborts a stream in both directions. Data still queued is lost;
// a graceful end is a FIN per direction on the data frames instead.
type TunnelClose struct {
	No     uint32 `json:"no"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Endpoint is one local listener the agent offers.
type Endpoint struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
}

// InstanceEndpoints is the complete set of listeners the control plane wants
// on this agent. An empty Listen stops all of them.
type InstanceEndpoints struct {
	Listen []Endpoint `json:"listen"`
}

// EndpointStatus is the observed state of one endpoint (Heartbeat.Endpoints).
type EndpointStatus struct {
	Service   string `json:"service"`
	Port      int    `json:"port,omitempty"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

// TunnelServices lists the services in a stable order.
var TunnelServices = []string{TunnelS3, TunnelRegistry, TunnelLoki}

// LocalPort returns the fixed agent-local port of service (0 for an unknown one).
func LocalPort(service string) int {
	switch service {
	case TunnelS3:
		return LocalPortS3
	case TunnelRegistry:
		return LocalPortRegistry
	case TunnelLoki:
		return LocalPortLoki
	}
	return 0
}

// TunnelServiceLimit is the most simultaneous streams of service per agent (0
// for an unknown service).
func TunnelServiceLimit(service string) int {
	switch service {
	case TunnelS3, TunnelRegistry:
		return 16
	case TunnelLoki:
		return 4
	}
	return 0
}

// StandardEndpoints is the endpoint set of every instance service.
func StandardEndpoints() *InstanceEndpoints {
	eps := make([]Endpoint, 0, len(TunnelServices))
	for _, s := range TunnelServices {
		eps = append(eps, Endpoint{Service: s, Port: LocalPort(s)})
	}
	return &InstanceEndpoints{Listen: eps}
}

// Data frames are WebSocket binary messages:
//
//	[0]   TunnelFrameData
//	[1]   flags (TunnelFlagFIN)
//	[2:6] stream number, big endian
//	[6:]  payload, at most TunnelChunk bytes
const (
	TunnelFrameData byte = 0x01
	// TunnelFlagFIN half-closes the sender's direction after this payload.
	TunnelFlagFIN byte = 1 << 0

	// TunnelHeaderLen is the length of the binary frame header.
	TunnelHeaderLen = 6
)

// ErrTunnelFrame reports a malformed binary frame.
var ErrTunnelFrame = errors.New("malformed tunnel frame")

// TunnelFrame is a decoded binary data frame.
type TunnelFrame struct {
	No      uint32
	Flags   byte
	Payload []byte
}

// FIN reports the half-close flag.
func (f TunnelFrame) FIN() bool { return f.Flags&TunnelFlagFIN != 0 }

// PutTunnelHeader writes the header of a data frame into b[:TunnelHeaderLen].
func PutTunnelHeader(b []byte, no uint32, flags byte) {
	b[0] = TunnelFrameData
	b[1] = flags
	binary.BigEndian.PutUint32(b[2:6], no)
}

// EncodeTunnelFrame builds a binary data frame (header plus a copy of payload).
func EncodeTunnelFrame(no uint32, flags byte, payload []byte) []byte {
	b := make([]byte, TunnelHeaderLen+len(payload))
	PutTunnelHeader(b, no, flags)
	copy(b[TunnelHeaderLen:], payload)
	return b
}

// DecodeTunnelFrame parses a binary data frame. Payload aliases b.
func DecodeTunnelFrame(b []byte) (TunnelFrame, error) {
	if len(b) < TunnelHeaderLen || b[0] != TunnelFrameData {
		return TunnelFrame{}, ErrTunnelFrame
	}
	if len(b)-TunnelHeaderLen > TunnelChunk {
		return TunnelFrame{}, fmt.Errorf("%w: payload of %d bytes exceeds %d", ErrTunnelFrame, len(b)-TunnelHeaderLen, TunnelChunk)
	}
	return TunnelFrame{No: binary.BigEndian.Uint32(b[2:6]), Flags: b[1], Payload: b[TunnelHeaderLen:]}, nil
}
