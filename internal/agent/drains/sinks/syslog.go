package sinks

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	syslogDialTimeout  = 10 * time.Second
	syslogWriteTimeout = 10 * time.Second
	syslogMaxUDP       = 8192
	syslogDefaultApp   = "nebulactrl"
	syslogSDID         = "nebulactrl@32473"
	syslogTimeLayout   = "2006-01-02T15:04:05.000000Z" // RFC 5424 allows at most 6 fraction digits
)

// syslogSink writes RFC 5424 messages. TCP and TCP+TLS keep one lazily dialled
// connection across Send calls and re-dial after any error or a close by the
// peer; UDP sends one datagram per message.
type syslogSink struct {
	network  string // "udp" or "tcp"
	addr     string
	tls      *tls.Config // nil unless tcp+tls
	facility int
	app      string

	mu     sync.Mutex
	conn   net.Conn
	closed bool
}

func newSyslog(c *agentprotocol.SyslogDrainConfig) (Sink, error) {
	s := &syslogSink{addr: c.Address, facility: c.Facility, app: asciiToken(c.AppName, 48, syslogDefaultApp)}
	if s.facility == 0 {
		s.facility = 1
	}
	switch c.Protocol {
	case agentprotocol.SyslogUDP:
		s.network = "udp"
	case agentprotocol.SyslogTCP:
		s.network = "tcp"
	case agentprotocol.SyslogTCPTLS:
		s.network = "tcp"
		s.tls = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         c.TLSServerName,
			InsecureSkipVerify: c.TLSInsecureSkipVerify, //nolint:gosec // explicit per-drain opt-in for self-signed collectors
		}
	}
	return s, nil
}

func (s *syslogSink) Send(ctx context.Context, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("syslog sink is closed")
	}
	if err := s.ensureConn(ctx); err != nil {
		return &SendError{Err: err}
	}
	conn := s.conn
	// ctx cancellation aborts a blocked write.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()) })
	defer stop()
	deadline := time.Now().Add(syslogWriteTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		s.drop(conn)
		return &SendError{Err: err}
	}

	if s.network == "udp" {
		for _, e := range batch {
			if _, err := conn.Write(truncateUTF8(s.format(e), syslogMaxUDP)); err != nil {
				s.drop(conn)
				return &SendError{Err: err}
			}
		}
		return nil
	}
	var buf []byte
	for _, e := range batch {
		msg := s.format(e)
		buf = strconv.AppendInt(buf, int64(len(msg)), 10)
		buf = append(buf, ' ')
		buf = append(buf, msg...)
	}
	if _, err := conn.Write(buf); err != nil {
		s.drop(conn)
		return &SendError{Err: err}
	}
	return nil
}

func (s *syslogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

// drop closes a failed connection so the next Send re-dials. Callers hold s.mu.
func (s *syslogSink) drop(c net.Conn) {
	_ = c.Close()
	if s.conn == c {
		s.conn = nil
	}
}

// ensureConn dials when there is no live connection. Callers hold s.mu.
func (s *syslogSink) ensureConn(ctx context.Context) error {
	if s.conn != nil {
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, syslogDialTimeout)
	defer cancel()
	nd := &net.Dialer{Timeout: syslogDialTimeout, KeepAlive: 30 * time.Second}
	var conn net.Conn
	var err error
	if s.tls != nil {
		conn, err = (&tls.Dialer{NetDialer: nd, Config: s.tls}).DialContext(dctx, "tcp", s.addr)
	} else {
		conn, err = nd.DialContext(dctx, s.network, s.addr)
	}
	if err != nil {
		return err
	}
	s.conn = conn
	if s.network == "tcp" {
		// Syslog collectors never answer. Reading detects a close by the peer
		// early, so the next batch is not written into a dead connection.
		go func() {
			_, _ = io.Copy(io.Discard, conn)
			s.mu.Lock()
			s.drop(conn)
			s.mu.Unlock()
		}()
	}
	return nil
}

// format builds one RFC 5424 message (without framing).
func (s *syslogSink) format(e Entry) []byte {
	pri := s.facility*8 + syslogSeverity(normLevel(e.Level))
	b := make([]byte, 0, 160+len(e.Text))
	b = append(b, '<')
	b = strconv.AppendInt(b, int64(pri), 10)
	b = append(b, ">1 "...)
	b = append(b, e.ts().UTC().Format(syslogTimeLayout)...)
	b = append(b, ' ')
	b = append(b, asciiToken(e.Server, 255, "-")...)
	b = append(b, ' ')
	b = append(b, s.app...)
	b = append(b, ' ')
	if e.Replica < 0 {
		b = append(b, '-')
	} else {
		b = strconv.AppendInt(b, int64(e.Replica), 10)
	}
	b = append(b, " - ["...)
	b = append(b, syslogSDID...)
	for _, p := range [...]struct{ k, v string }{
		{"service", e.Service}, {"project", e.Project}, {"environment", e.Environment},
	} {
		if p.v != "" {
			b = appendSDParam(b, p.k, p.v)
		}
	}
	b = appendSDParam(b, "replica", strconv.Itoa(e.Replica))
	b = append(b, ']')

	text := singleLine(e.Text)
	if e.Service != "" {
		text = "[" + singleLine(e.Service) + "] " + text
	}
	if text != "" {
		b = append(b, ' ')
		b = append(b, text...)
	}
	return b
}

func syslogSeverity(level string) int {
	switch level {
	case "error":
		return 3
	case "warn":
		return 4
	case "debug":
		return 7
	}
	return 6 // info and unknown
}

// appendSDParam appends ` name="value"` with the RFC 5424 escapes for \, " and ].
func appendSDParam(b []byte, name, value string) []byte {
	b = append(b, ' ')
	b = append(b, name...)
	b = append(b, '=', '"')
	for _, r := range singleLine(value) {
		if r == '\\' || r == '"' || r == ']' {
			b = append(b, '\\')
		}
		b = utf8.AppendRune(b, r)
	}
	return append(b, '"')
}

// singleLine makes s valid UTF-8 on one line: CR, LF and NUL
// become a space.
func singleLine(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == 0 {
			return ' '
		}
		return r
	}, s)
}

// asciiToken keeps printable ASCII (no spaces) and replaces anything else
// with '_'; it truncates to limit bytes and returns fallback when empty.
func asciiToken(s string, limit int, fallback string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= limit {
			break
		}
		if r > 32 && r < 127 {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

// truncateUTF8 cuts b to at most n bytes on a rune boundary.
func truncateUTF8(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}
	return b[:n]
}
