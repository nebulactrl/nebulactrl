// Package tunnelmux is the transport-independent core of the instance service
// tunnel (pkg/agentprotocol/localtunnel.go): many byte streams multiplexed over
// one agent connection, each a net.Conn. It owns the credit windows, the
// outbound data lane and the stream state machine; the two ends (the agent's
// conn.Client and the control plane's agents.Hub) only move frames between a
// Mux and their WebSocket.
//
// Frames. Control frames (tunnel_open, tunnel_opened, tunnel_ack,
// tunnel_close) are JSON text frames the owner sends through Config.Control,
// ahead of data. Data frames are binary and come out of Next in round-robin
// order across streams, one chunk at a time, so the owner's writer can serve
// its own control queue between any two chunks.
//
// Flow control. A sender may have at most StreamWindow unacknowledged payload
// bytes per stream; the receiver acknowledges bytes once the consumer read them
// (batched at a quarter window). Independently, QueueLimit bounds the payload
// that is queued for the socket but not yet written, over all streams of the
// connection. A writer that gets no credit for CreditTimeout aborts its stream.
//
// Ending. Each direction ends with a FIN (CloseWrite); a stream is complete
// when both FINs were exchanged. tunnel_close aborts both directions at once
// and may overtake queued data.
package tunnelmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Errors of the mux itself. A stream aborted by either end reports *Error.
var (
	ErrDown      = errors.New("tunnel connection is down")
	ErrDuplicate = errors.New("tunnel stream number already in use")
)

// Error is the reason a stream was aborted.
type Error struct{ Code, Reason string }

func (e *Error) Error() string {
	if e.Reason == "" {
		return "tunnel stream closed: " + e.Code
	}
	return "tunnel stream closed: " + e.Reason
}

// Config configures a Mux. Zero sizes and timeouts take the protocol defaults.
type Config struct {
	// Control sends a JSON control frame to the peer, ahead of data. An error
	// means the connection is gone.
	Control func(typ string, body any) error
	Log     *slog.Logger

	StreamWindow  int
	QueueLimit    int
	IdleTimeout   time.Duration
	CreditTimeout time.Duration
	OpenTimeout   time.Duration
	Linger        time.Duration
}

// Mux multiplexes the tunnel streams of one connection. It starts down; Up
// brings it up and Down ends every stream.
type Mux struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	down    bool
	streams map[uint32]*Stream
	nextNo  uint32
	queued  int // payload bytes in lane
	waiters int // writers parked for credit or queue space
	stop    context.CancelFunc

	lane lane
}

// New returns a Mux that is down.
func New(cfg Config) *Mux {
	if cfg.StreamWindow <= 0 {
		cfg.StreamWindow = agentprotocol.TunnelStreamWindow
	}
	if cfg.QueueLimit <= 0 {
		cfg.QueueLimit = agentprotocol.TunnelConnWindow
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = agentprotocol.TunnelIdleTimeout
	}
	if cfg.CreditTimeout <= 0 {
		cfg.CreditTimeout = agentprotocol.TunnelCreditTimeout
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = agentprotocol.TunnelOpenTimeout
	}
	if cfg.Linger <= 0 {
		cfg.Linger = agentprotocol.TunnelLinger
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Mux{cfg: cfg, log: log, down: true, streams: map[uint32]*Stream{}, lane: newLane()}
}

// Up makes the mux usable and starts its idle sweeper. Calling it on a mux
// that is up does nothing.
func (m *Mux) Up() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.down {
		return
	}
	m.down = false
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	go m.sweepLoop(ctx)
}

// Down aborts every stream with err (the connection ended) and drops queued
// data. Streams opened afterwards fail with ErrDown until Up.
func (m *Mux) Down(err error) {
	if err == nil {
		err = ErrDown
	}
	m.mu.Lock()
	m.down = true
	if m.stop != nil {
		m.stop()
		m.stop = nil
	}
	for _, s := range m.streams {
		s.endLocked(err)
	}
	m.streams = map[uint32]*Stream{}
	// Frames of streams that already completed can still sit in the lane;
	// bytes reserved by a writer that has not pushed yet are returned by
	// that writer once it sees the stream error.
	m.queued -= m.lane.reset()
	m.mu.Unlock()
}

// Len is the number of live streams.
func (m *Mux) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

// Count is the number of live streams of service.
func (m *Mux) Count(service string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.streams {
		if s.service == service {
			n++
		}
	}
	return n
}

// Queued is the payload bytes waiting for the socket.
func (m *Mux) Queued() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queued
}

// Ready is signalled when Next may have a frame. The owner's writer drains
// Next until it reports false, then waits on Ready.
func (m *Mux) Ready() <-chan struct{} { return m.lane.sig }

// Next returns the next binary data frame to write, round-robin over streams.
func (m *Mux) Next() ([]byte, bool) {
	// Popping and the byte accounting happen under one hold of m.mu so a
	// concurrent Down or abort sees either both or neither.
	m.mu.Lock()
	defer m.mu.Unlock()
	f, payload, ok := m.lane.pop()
	if !ok {
		return nil, false
	}
	if payload > 0 {
		m.queued -= payload
		m.wakeWritersLocked()
	}
	return f, true
}

// Dial opens a stream to service through the peer and waits for tunnel_opened.
func (m *Mux) Dial(ctx context.Context, service string) (*Stream, error) {
	m.mu.Lock()
	if m.down {
		m.mu.Unlock()
		return nil, ErrDown
	}
	m.nextNo++
	if m.nextNo == 0 {
		m.nextNo = 1
	}
	s := m.newStreamLocked(m.nextNo, service)
	s.opening = true
	m.streams[s.no] = s
	m.mu.Unlock()

	if err := m.cfg.Control(agentprotocol.TypeTunnelOpen, agentprotocol.TunnelOpen{No: s.no, Service: service}); err != nil {
		m.finish(s, &Error{Code: agentprotocol.TunnelCodeClosed, Reason: "connection lost"}, false)
		return nil, err
	}
	t := time.NewTimer(m.cfg.OpenTimeout)
	defer t.Stop()
	select {
	case <-s.opened:
		return s, nil
	case <-s.done:
		return nil, s.cause()
	case <-ctx.Done():
		m.finish(s, &Error{Code: agentprotocol.TunnelCodeClosed, Reason: "open canceled"}, true)
		return nil, ctx.Err()
	case <-t.C:
		m.finish(s, &Error{Code: agentprotocol.TunnelCodeClosed, Reason: "the control plane did not answer"}, true)
		return nil, s.cause()
	}
}

// Accept registers an incoming stream the peer opened as no. The caller sends
// tunnel_opened afterwards.
func (m *Mux) Accept(no uint32, service string) (*Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, ErrDown
	}
	if _, dup := m.streams[no]; dup || no == 0 {
		return nil, ErrDuplicate
	}
	s := m.newStreamLocked(no, service)
	m.streams[no] = s
	return s, nil
}

// HandleControl applies tunnel_opened, tunnel_ack and tunnel_close from the
// peer. raw is the whole JSON frame. It never blocks.
func (m *Mux) HandleControl(typ string, raw []byte) {
	switch typ {
	case agentprotocol.TypeTunnelOpened:
		var o agentprotocol.TunnelOpened
		if json.Unmarshal(raw, &o) != nil {
			return
		}
		m.mu.Lock()
		s := m.streams[o.No]
		if s == nil || !s.opening {
			m.mu.Unlock()
			return
		}
		if o.Code != "" {
			delete(m.streams, o.No)
			s.endLocked(&Error{Code: o.Code, Reason: o.Error})
			m.mu.Unlock()
			return
		}
		s.opening = false
		s.lastActive = time.Now()
		close(s.opened)
		m.mu.Unlock()
	case agentprotocol.TypeTunnelAck:
		var a agentprotocol.TunnelAck
		if json.Unmarshal(raw, &a) != nil {
			return
		}
		m.mu.Lock()
		s := m.streams[a.No]
		if s == nil {
			m.mu.Unlock()
			return
		}
		if a.Bytes <= 0 || s.credit+a.Bytes > m.cfg.StreamWindow {
			m.mu.Unlock()
			m.finish(s, &Error{Code: agentprotocol.TunnelCodeProtocol, Reason: "invalid tunnel_ack"}, true)
			return
		}
		s.credit += a.Bytes
		s.nudge(s.wnotify)
		m.mu.Unlock()
	case agentprotocol.TypeTunnelClose:
		var c agentprotocol.TunnelClose
		if json.Unmarshal(raw, &c) != nil {
			return
		}
		m.mu.Lock()
		s := m.streams[c.No]
		m.mu.Unlock()
		if s != nil {
			m.finish(s, &Error{Code: c.Code, Reason: c.Reason}, false)
		}
	}
}

// HandleData applies one binary frame from the peer. It never blocks: a frame
// that would overrun the stream window aborts the stream.
func (m *Mux) HandleData(b []byte) {
	f, err := agentprotocol.DecodeTunnelFrame(b)
	if err != nil {
		m.log.Warn("dropping malformed tunnel frame", "err", err)
		return
	}
	m.mu.Lock()
	s := m.streams[f.No]
	if s == nil || s.opening {
		m.mu.Unlock() // unknown or already aborted: nothing to do
		return
	}
	var ack int
	if n := len(f.Payload); n > 0 {
		switch {
		case s.closedLocal:
			// Nobody reads any more: the bytes count as consumed at once.
			s.pendingAck += n
			ack = s.takeAckLocked(m.cfg.StreamWindow)
		case s.rqBytes+s.pendingAck+n > m.cfg.StreamWindow:
			m.mu.Unlock()
			m.finish(s, &Error{Code: agentprotocol.TunnelCodeProtocol, Reason: "stream window overrun"}, true)
			return
		default:
			s.rq = append(s.rq, f.Payload)
			s.rqBytes += n
			s.nudge(s.rnotify)
		}
		s.lastActive = time.Now()
	}
	if f.FIN() && !s.rxFIN {
		s.rxFIN = true
		s.nudge(s.rnotify)
		s.maybeCompleteLocked(m)
	}
	m.mu.Unlock()
	if ack > 0 {
		m.sendAck(s, ack)
	}
}

func (m *Mux) sendAck(s *Stream, n int) {
	_ = m.cfg.Control(agentprotocol.TypeTunnelAck, agentprotocol.TunnelAck{No: s.no, Bytes: n})
}

// finish aborts s with err; notify also sends tunnel_close to the peer.
func (m *Mux) finish(s *Stream, err *Error, notify bool) {
	m.mu.Lock()
	if s.ended {
		m.mu.Unlock()
		return
	}
	delete(m.streams, s.no)
	s.endLocked(err)
	m.mu.Unlock()
	if notify {
		_ = m.cfg.Control(agentprotocol.TypeTunnelClose, agentprotocol.TunnelClose{No: s.no, Code: err.Code, Reason: err.Reason})
	}
}

func (m *Mux) newStreamLocked(no uint32, service string) *Stream {
	return &Stream{
		m: m, no: no, service: service,
		credit:     m.cfg.StreamWindow,
		lastActive: time.Now(),
		rnotify:    make(chan struct{}, 1), wnotify: make(chan struct{}, 1),
		done: make(chan struct{}), opened: make(chan struct{}),
		rdl: newDeadline(), wdl: newDeadline(),
	}
}

func (m *Mux) wakeWritersLocked() {
	if m.waiters == 0 {
		return
	}
	for _, s := range m.streams {
		if s.waiting {
			s.nudge(s.wnotify)
		}
	}
}

func (m *Mux) sweepLoop(ctx context.Context) {
	period := min(m.cfg.IdleTimeout, m.cfg.Linger, m.cfg.CreditTimeout) / 4
	period = max(period, 5*time.Millisecond)
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweep(time.Now())
		}
	}
}

// sweep aborts streams that idled out or lingered after a local close.
func (m *Mux) sweep(now time.Time) {
	type victim struct {
		s    *Stream
		code string
	}
	var vs []victim
	m.mu.Lock()
	for _, s := range m.streams {
		switch {
		case s.opening:
		case s.closedLocal && now.Sub(s.closedAt) > m.cfg.Linger:
			vs = append(vs, victim{s, agentprotocol.TunnelCodeClosed})
		case now.Sub(s.lastActive) > m.cfg.IdleTimeout:
			vs = append(vs, victim{s, agentprotocol.TunnelCodeIdle})
		}
	}
	m.mu.Unlock()
	for _, v := range vs {
		m.finish(v.s, &Error{Code: v.code, Reason: "tunnel stream ended: " + v.code}, true)
	}
}

// Stream is one tunnel stream. It implements net.Conn, plus CloseWrite for the
// half-close that ends one direction (a FIN).
type Stream struct {
	m       *Mux
	no      uint32
	service string

	// Guarded by m.mu.
	opening     bool
	credit      int
	rq          [][]byte
	rqBytes     int
	pendingAck  int
	rxFIN       bool
	txFIN       bool
	closedLocal bool
	closedAt    time.Time
	lastActive  time.Time
	waiting     bool
	ended       bool
	err         error

	rnotify, wnotify chan struct{}
	done             chan struct{}
	opened           chan struct{}
	rdl, wdl         *deadline
}

var _ net.Conn = (*Stream)(nil)

// No is the stream number.
func (s *Stream) No() uint32 { return s.no }

// Service is the service the stream was opened for.
func (s *Stream) Service() string { return s.service }

// Done is closed when the stream is over (both FINs, an abort or the
// connection ending). Buffered data may still be readable.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Err is the abort reason (*Error or the connection error) or nil.
func (s *Stream) Err() error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.err
}

func (s *Stream) cause() error {
	if err := s.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (s *Stream) nudge(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// endLocked marks the stream over. err nil means a graceful end.
func (s *Stream) endLocked(err error) {
	if s.ended {
		return
	}
	s.ended = true
	if err != nil {
		s.err = err
		s.freeQueuedLocked()
		s.rq, s.rqBytes = nil, 0
	}
	if s.opening {
		s.opening = false
	}
	close(s.done)
	s.nudge(s.rnotify)
	s.nudge(s.wnotify)
}

func (s *Stream) freeQueuedLocked() {
	m := s.m
	if freed := m.lane.drop(s.no); freed > 0 {
		m.queued -= freed
		m.wakeWritersLocked()
	}
}

func (s *Stream) maybeCompleteLocked(m *Mux) {
	if s.txFIN && s.rxFIN && !s.ended {
		delete(m.streams, s.no)
		s.endLocked(nil)
	}
}

// takeAckLocked returns the bytes to acknowledge once a quarter window is pending.
func (s *Stream) takeAckLocked(window int) int {
	if s.pendingAck >= window/4 {
		n := s.pendingAck
		s.pendingAck = 0
		return n
	}
	return 0
}

// Read implements net.Conn.
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	m := s.m
	for {
		m.mu.Lock()
		switch {
		case s.closedLocal:
			m.mu.Unlock()
			return 0, net.ErrClosed
		case len(s.rq) > 0:
			n := 0
			for n < len(p) && len(s.rq) > 0 {
				c := copy(p[n:], s.rq[0])
				n += c
				if c == len(s.rq[0]) {
					s.rq[0] = nil
					s.rq = s.rq[1:]
				} else {
					s.rq[0] = s.rq[0][c:]
				}
			}
			s.rqBytes -= n
			s.pendingAck += n
			s.lastActive = time.Now()
			ack := s.takeAckLocked(m.cfg.StreamWindow)
			ended := s.ended
			m.mu.Unlock()
			if ack > 0 && !ended {
				m.sendAck(s, ack)
			}
			return n, nil
		case s.rxFIN:
			m.mu.Unlock()
			return 0, io.EOF
		case s.err != nil:
			err := s.err
			m.mu.Unlock()
			return 0, err
		case s.ended:
			m.mu.Unlock()
			return 0, io.EOF
		}
		m.mu.Unlock()
		select {
		case <-s.rnotify:
		case <-s.done:
		case <-s.rdl.wait():
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// writeErrLocked reports why the stream cannot take more data.
func (s *Stream) writeErrLocked() error {
	switch {
	case s.err != nil:
		return s.err
	case s.txFIN || s.closedLocal:
		return net.ErrClosed
	case s.ended:
		return io.ErrClosedPipe
	}
	return nil
}

// reserve waits for up to n bytes of stream credit and queue space and takes them.
func (s *Stream) reserve(n int) (int, error) {
	m := s.m
	var stall *time.Timer
	defer func() {
		if stall != nil {
			stall.Stop()
		}
	}()
	for {
		m.mu.Lock()
		if err := s.writeErrLocked(); err != nil {
			m.mu.Unlock()
			return 0, err
		}
		if avail := min(n, s.credit, m.cfg.QueueLimit-m.queued); avail > 0 {
			s.credit -= avail
			m.queued += avail
			s.lastActive = time.Now()
			m.mu.Unlock()
			return avail, nil
		}
		s.waiting = true
		m.waiters++
		m.mu.Unlock()
		if stall == nil {
			stall = time.NewTimer(m.cfg.CreditTimeout)
		}
		var stalled bool
		var derr error
		select {
		case <-s.wnotify:
		case <-s.done:
		case <-s.wdl.wait():
			derr = os.ErrDeadlineExceeded
		case <-stall.C:
			stalled = true
		}
		m.mu.Lock()
		s.waiting = false
		m.waiters--
		m.mu.Unlock()
		if derr != nil {
			return 0, derr
		}
		if stalled {
			e := &Error{Code: agentprotocol.TunnelCodeStalled, Reason: "no flow-control credit from the peer"}
			m.finish(s, e, true)
			return 0, e
		}
	}
}

// Write implements net.Conn. It blocks while the peer's window is full.
func (s *Stream) Write(p []byte) (int, error) {
	m := s.m
	m.mu.Lock()
	err := s.writeErrLocked()
	m.mu.Unlock()
	if err != nil {
		return 0, err
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > agentprotocol.TunnelChunk {
			n = agentprotocol.TunnelChunk
		}
		buf := make([]byte, agentprotocol.TunnelHeaderLen+n)
		granted, err := s.reserve(n)
		if err != nil {
			return total, err
		}
		copy(buf[agentprotocol.TunnelHeaderLen:], p[:granted])
		buf = buf[:agentprotocol.TunnelHeaderLen+granted]
		agentprotocol.PutTunnelHeader(buf, s.no, 0)

		m.mu.Lock()
		if err := s.writeErrLocked(); err != nil {
			// Aborted while copying: the lane dropped this stream already.
			m.queued -= granted
			m.wakeWritersLocked()
			m.mu.Unlock()
			return total, err
		}
		m.lane.push(s.no, buf, granted)
		m.mu.Unlock()
		total += granted
		p = p[granted:]
	}
	return total, nil
}

// CloseWrite sends FIN: the peer reads EOF after the data already written.
func (s *Stream) CloseWrite() error {
	m := s.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.txFIN {
		return nil
	}
	s.txFIN = true
	m.lane.push(s.no, agentprotocol.EncodeTunnelFrame(s.no, agentprotocol.TunnelFlagFIN, nil), 0)
	s.maybeCompleteLocked(m)
	return nil
}

// Close ends the stream: it sends FIN, discards what the peer still sends
// (crediting it at once) and waits for the peer's FIN in the background for at
// most the linger time, then aborts.
func (s *Stream) Close() error {
	m := s.m
	m.mu.Lock()
	if s.closedLocal || s.err != nil {
		m.mu.Unlock()
		return nil
	}
	s.closedLocal = true
	s.closedAt = time.Now()
	s.pendingAck += s.rqBytes
	s.rq, s.rqBytes = nil, 0
	ack := s.pendingAck
	s.pendingAck = 0
	if !s.txFIN && !s.ended {
		s.txFIN = true
		m.lane.push(s.no, agentprotocol.EncodeTunnelFrame(s.no, agentprotocol.TunnelFlagFIN, nil), 0)
	}
	s.maybeCompleteLocked(m)
	ended := s.ended
	s.nudge(s.rnotify)
	s.nudge(s.wnotify)
	m.mu.Unlock()
	if ack > 0 && !ended {
		m.sendAck(s, ack)
	}
	return nil
}

// Abort ends the stream in both directions with a reason the peer sees.
func (s *Stream) Abort(code, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	s.m.finish(s, &Error{Code: code, Reason: reason}, true)
}

type addr struct{ s *Stream }

func (a addr) Network() string { return "tunnel" }
func (a addr) String() string  { return fmt.Sprintf("tunnel:%s/%d", a.s.service, a.s.no) }

func (s *Stream) LocalAddr() net.Addr  { return addr{s} }
func (s *Stream) RemoteAddr() net.Addr { return addr{s} }

func (s *Stream) SetDeadline(t time.Time) error {
	s.rdl.set(t)
	s.wdl.set(t)
	return nil
}
func (s *Stream) SetReadDeadline(t time.Time) error  { s.rdl.set(t); return nil }
func (s *Stream) SetWriteDeadline(t time.Time) error { s.wdl.set(t); return nil }
