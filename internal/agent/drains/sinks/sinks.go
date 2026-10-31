// Package sinks implements the delivery formats of the agent's log drains:
// Loki push, OTLP/HTTP logs (JSON), RFC 5424 syslog and generic HTTP JSON.
//
// A Sink is pure protocol and encoding plus one network send per batch. It has
// no queue, cursor, retry or disk buffer: the caller cuts batches (at
// MaxBatchLines or MaxBatchBytes), calls Send once per batch and decides from
// the returned *SendError whether to retry or drop the batch.
package sinks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Batch bounds. Callers cut batches at either bound.
const (
	MaxBatchLines = 500
	MaxBatchBytes = 1 << 20 // sum of len(Text)
)

// Entry is one log line with its labels, ready to ship.
type Entry struct {
	TS            time.Time
	Text          string
	Level         string // debug|info|warn|error or ""
	Stream        string // "runtime"
	ServiceID     string
	Service       string // display name
	ServiceSlug   string
	ProjectID     string
	Project       string
	EnvironmentID string
	Environment   string
	DeploymentID  string
	Replica       int
	Server        string // server display name
}

// ts returns the entry time, or now for an entry without one.
func (e Entry) ts() time.Time {
	if e.TS.IsZero() {
		return time.Now()
	}
	return e.TS
}

// normLevel returns the canonical level (debug, info, warn, error) or "".
func normLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return "debug"
	case "info":
		return "info"
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	}
	return ""
}

// SendError classifies a failed delivery.
type SendError struct {
	Err        error
	StatusCode int  // HTTP status when there was a response, else 0
	Permanent  bool // true: retrying the same batch cannot succeed; the caller drops the batch
	// RetryAfter is the server's Retry-After, else 0.
	RetryAfter time.Duration
}

func (e *SendError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.StatusCode != 0 {
		return "log drain delivery failed: HTTP " + strconv.Itoa(e.StatusCode)
	}
	return "log drain delivery failed"
}

func (e *SendError) Unwrap() error { return e.Err }

// Sink delivers batches to one drain.
type Sink interface {
	// Send delivers the batch (1..MaxBatchLines entries) or returns an error (a
	// *SendError for delivery failures, a plain error for misconfiguration).
	Send(ctx context.Context, batch []Entry) error
	Close() error
}

// New builds the sink of a drain. secrets is the drain's own secrets keyed by
// the short names agentprotocol.LogDrainSecretToken/Username/Password/Headers.
// It returns an error for an invalid config.
func New(d agentprotocol.LogDrain, secrets map[string]string) (Sink, error) {
	if err := Validate(d); err != nil {
		return nil, err
	}
	switch d.Kind {
	case agentprotocol.DrainLoki:
		return newLoki(d.Loki, secrets)
	case agentprotocol.DrainOTLP:
		return newOTLP(d.OTLP, secrets)
	case agentprotocol.DrainSyslog:
		return newSyslog(d.Syslog)
	default: // agentprotocol.DrainHTTP, checked by Validate
		return newHTTPJSON(d.HTTP, secrets)
	}
}

// Validate checks a drain's non-secret config exactly like New does, without
// building a client.
func Validate(d agentprotocol.LogDrain) error {
	switch d.Kind {
	case agentprotocol.DrainLoki:
		if d.Loki == nil {
			return errors.New("loki drain: missing loki config")
		}
		return validateURL("loki", d.Loki.URL)
	case agentprotocol.DrainOTLP:
		if d.OTLP == nil {
			return errors.New("otlp drain: missing otlp config")
		}
		return validateURL("otlp", d.OTLP.URL)
	case agentprotocol.DrainSyslog:
		if d.Syslog == nil {
			return errors.New("syslog drain: missing syslog config")
		}
		return validateSyslog(d.Syslog)
	case agentprotocol.DrainHTTP:
		if d.HTTP == nil {
			return errors.New("http drain: missing http config")
		}
		return validateHTTPJSON(d.HTTP)
	}
	return fmt.Errorf("unknown log drain kind %q", d.Kind)
}

// validateURL accepts absolute http(s) URLs with a host and no userinfo. The
// URL itself is never echoed in the error: it may carry a secret.
func validateURL(kind, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s drain: url is required", kind)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s drain: url is not a valid URL", kind)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s drain: url scheme must be http or https", kind)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%s drain: url has no host", kind)
	}
	if u.User != nil {
		return fmt.Errorf("%s drain: url must not contain credentials (use the credential fields)", kind)
	}
	return nil
}

func validateSyslog(c *agentprotocol.SyslogDrainConfig) error {
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || host == "" {
		return errors.New("syslog drain: address must be host:port")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return errors.New("syslog drain: address port must be 1-65535")
	}
	switch c.Protocol {
	case agentprotocol.SyslogUDP, agentprotocol.SyslogTCP, agentprotocol.SyslogTCPTLS:
	default:
		return fmt.Errorf("syslog drain: protocol must be %s, %s or %s",
			agentprotocol.SyslogUDP, agentprotocol.SyslogTCP, agentprotocol.SyslogTCPTLS)
	}
	if c.Facility < 0 || c.Facility > 23 {
		return errors.New("syslog drain: facility must be 0-23")
	}
	return nil
}

// TestEntry returns the line a drain test sends.
func TestEntry(drainName, server string) Entry {
	return Entry{
		TS:            time.Now(),
		Text:          "NebulaCtrl log drain test: " + drainName,
		Level:         "info",
		Stream:        "runtime",
		ServiceID:     "nebulactrl-drain-test",
		Service:       "nebulactrl-drain-test",
		ServiceSlug:   "nebulactrl-drain-test",
		ProjectID:     "nebulactrl-drain-test",
		Project:       "nebulactrl",
		EnvironmentID: "nebulactrl-drain-test",
		Environment:   "test",
		Server:        server,
	}
}
