package tunnelmux

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// wire connects two muxes the way the two connection ends do: control frames
// are delivered ahead of data, data in the order Next yields it.
type wire struct {
	t    *testing.T
	to   *Mux
	ctl  chan ctlFrame
	done chan struct{}
	from *Mux
	// intercept handles a control frame instead of the receiving mux (true).
	intercept func(ctlFrame) bool
	// gate, when closed, lets data through; nil means open. Control always flows.
	mu   sync.Mutex
	gate chan struct{}
	sent []ctlFrame
}

type ctlFrame struct {
	typ string
	raw []byte
}

func (w *wire) control(typ string, body any) error {
	b, err := agentprotocol.Encode(typ, "", body)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.sent = append(w.sent, ctlFrame{typ, b})
	w.mu.Unlock()
	select {
	case w.ctl <- ctlFrame{typ, b}:
		return nil
	case <-w.done:
		return errors.New("wire closed")
	}
}

func (w *wire) controlSeen(typ string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, f := range w.sent {
		if f.typ == typ {
			n++
		}
	}
	return n
}

func (w *wire) hold() chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gate = make(chan struct{})
	return w.gate
}

func (w *wire) release(g chan struct{}) { close(g) }
func (w *wire) deliver(f ctlFrame) {
	if w.intercept != nil && w.intercept(f) {
		return
	}
	w.to.HandleControl(f.typ, f.raw)
}

func (w *wire) pump() {
	for {
		w.mu.Lock()
		g := w.gate
		w.mu.Unlock()
		select {
		case <-w.done:
			return
		case f := <-w.ctl:
			w.deliver(f)
			continue
		default:
		}
		if g != nil {
			select {
			case <-g:
			default:
				select { // data is held: control only
				case <-w.done:
					return
				case f := <-w.ctl:
					w.deliver(f)
				case <-time.After(time.Millisecond):
				}
				continue
			}
		}
		if d, ok := w.from.Next(); ok {
			w.to.HandleData(d)
			continue
		}
		select {
		case <-w.done:
			return
		case f := <-w.ctl:
			w.deliver(f)
		case <-w.from.Ready():
		}
	}
}

type pair struct {
	a, b   *Mux // a is the dialing end (agent), b the accepting end (hub)
	ab, ba *wire
	// refuse, when set, answers tunnel_open with that code instead of accepting.
	refuse string
	// silent swallows tunnel_open without an answer.
	silent   bool
	accepted chan *Stream
}

func newPair(t *testing.T, cfg Config) *pair {
	t.Helper()
	p := &pair{accepted: make(chan *Stream, 64)}
	p.ab = &wire{t: t, ctl: make(chan ctlFrame, 1024), done: make(chan struct{})}
	p.ba = &wire{t: t, ctl: make(chan ctlFrame, 1024), done: make(chan struct{})}
	ca, cb := cfg, cfg
	ca.Control, cb.Control = p.ab.control, p.ba.control
	p.a, p.b = New(ca), New(cb)
	p.ab.from, p.ab.to = p.a, p.b
	p.ba.from, p.ba.to = p.b, p.a
	// The accepting end handles tunnel_open like the hub does.
	p.ab.intercept = func(f ctlFrame) bool {
		if f.typ != agentprotocol.TypeTunnelOpen {
			return false
		}
		var o agentprotocol.TunnelOpen
		_ = json.Unmarshal(f.raw, &o)
		if p.refuse != "" {
			_ = p.ba.control(agentprotocol.TypeTunnelOpened, agentprotocol.TunnelOpened{No: o.No, Code: p.refuse, Error: "refused"})
			return true
		}
		if p.silent { // a control plane that never answers
			return true
		}
		s, err := p.b.Accept(o.No, o.Service)
		if err != nil {
			_ = p.ba.control(agentprotocol.TypeTunnelOpened, agentprotocol.TunnelOpened{No: o.No, Code: agentprotocol.TunnelCodeBadRequest})
			return true
		}
		p.accepted <- s
		_ = p.ba.control(agentprotocol.TypeTunnelOpened, agentprotocol.TunnelOpened{No: o.No})
		return true
	}
	p.a.Up()
	p.b.Up()
	go p.ab.pump()
	go p.ba.pump()
	t.Cleanup(func() {
		close(p.ab.done)
		close(p.ba.done)
		p.a.Down(nil)
		p.b.Down(nil)
	})
	return p
}

// open dials from a; the intercepted tunnel_open is accepted on b the way the
// hub does. It returns both ends.
func (p *pair) open(t *testing.T, service string) (*Stream, *Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cs, err := p.a.Dial(ctx, service)
	require.NoError(t, err)
	select {
	case ss := <-p.accepted:
		return cs, ss
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not accepted")
		return nil, nil
	}
}
func TestOpenDataFinAndClose(t *testing.T) {
	p := newPair(t, Config{})
	c, s := p.open(t, agentprotocol.TunnelS3)
	require.Equal(t, agentprotocol.TunnelS3, s.Service())
	require.Equal(t, 1, p.a.Len())
	require.Equal(t, 1, p.b.Count(agentprotocol.TunnelS3))

	_, err := c.Write([]byte("request"))
	require.NoError(t, err)
	buf := make([]byte, 16)
	n, err := s.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "request", string(buf[:n]))

	// Half close: the server still answers after the client's FIN.
	require.NoError(t, c.CloseWrite())
	_, err = s.Read(buf)
	require.ErrorIs(t, err, io.EOF)
	_, err = s.Write([]byte("response"))
	require.NoError(t, err)
	n, err = c.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "response", string(buf[:n]))

	// Both FINs exchanged: the stream is gone on both ends.
	require.NoError(t, s.Close())
	_, err = c.Read(buf)
	require.ErrorIs(t, err, io.EOF)
	require.Eventually(t, func() bool { return p.a.Len() == 0 && p.b.Len() == 0 }, 2*time.Second, time.Millisecond)
	_, err = c.Write([]byte("x"))
	require.Error(t, err)
}

func TestLargeTransferIsOrderedAndComplete(t *testing.T) {
	p := newPair(t, Config{})
	c, s := p.open(t, agentprotocol.TunnelS3)
	data := make([]byte, 20<<20) // ten windows
	_, _ = rand.Read(data)
	go func() {
		_, _ = c.Write(data)
		_ = c.CloseWrite()
	}()
	got, err := io.ReadAll(s)
	require.NoError(t, err)
	require.True(t, bytes.Equal(data, got))
}

func TestWriterBlocksAtStreamWindowUntilTheReaderConsumes(t *testing.T) {
	p := newPair(t, Config{})
	c, s := p.open(t, agentprotocol.TunnelS3)
	total := agentprotocol.TunnelStreamWindow + 3*agentprotocol.TunnelChunk
	data := make([]byte, total)
	written := make(chan int, 1)
	go func() {
		n, _ := c.Write(data)
		written <- n
	}()
	// Nobody reads: exactly one window is accepted by the receiver, the rest waits.
	require.Eventually(t, func() bool {
		s.m.mu.Lock()
		defer s.m.mu.Unlock()
		return s.rqBytes == agentprotocol.TunnelStreamWindow
	}, 2*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	s.m.mu.Lock()
	require.Equal(t, agentprotocol.TunnelStreamWindow, s.rqBytes, "the sender must not exceed the window")
	s.m.mu.Unlock()
	select {
	case <-written:
		t.Fatal("write returned although the window is full")
	default:
	}
	// Reading credits the sender back.
	go func() { _, _ = io.CopyN(io.Discard, s, int64(total)) }()
	select {
	case n := <-written:
		require.Equal(t, total, n)
	case <-time.After(5 * time.Second):
		t.Fatal("write did not resume after the reader consumed data")
	}
}

func TestConnectionQueueLimitBoundsQueuedBytes(t *testing.T) {
	const limit = 4 * agentprotocol.TunnelChunk
	p := newPair(t, Config{QueueLimit: limit, StreamWindow: 2 << 20})
	c1, s1 := p.open(t, agentprotocol.TunnelS3)
	c2, s2 := p.open(t, agentprotocol.TunnelRegistry)
	g := p.ab.hold() // the socket is stalled: nothing leaves the queue
	chunk := make([]byte, 8*agentprotocol.TunnelChunk)
	var wg sync.WaitGroup
	for _, c := range []*Stream{c1, c2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Write(chunk)
		}()
	}
	require.Eventually(t, func() bool { return p.a.Queued() == limit }, 2*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, limit, p.a.Queued(), "queued payload of all streams stays within the connection cap")

	// Draining the socket lets both writers finish.
	p.ab.release(g)
	go func() { _, _ = io.CopyN(io.Discard, s1, int64(len(chunk))) }()
	go func() { _, _ = io.CopyN(io.Discard, s2, int64(len(chunk))) }()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writers did not finish after the queue drained")
	}
}

func TestAbortFailsBothEndsAndDropsQueuedData(t *testing.T) {
	p := newPair(t, Config{})
	c, s := p.open(t, agentprotocol.TunnelS3)
	g := p.ab.hold()
	_, err := c.Write(make([]byte, 3*agentprotocol.TunnelChunk))
	require.NoError(t, err)
	require.NotZero(t, p.a.Queued())

	c.Abort(agentprotocol.TunnelCodeClosed, "client gone")
	require.Zero(t, p.a.Queued(), "queued data of an aborted stream is dropped")
	p.ab.release(g)

	_, err = s.Read(make([]byte, 8))
	var te *Error
	require.ErrorAs(t, err, &te)
	require.Equal(t, agentprotocol.TunnelCodeClosed, te.Code)
	require.Equal(t, "client gone", te.Reason)
	_, err = s.Write([]byte("x"))
	require.ErrorAs(t, err, &te)
	require.Zero(t, p.a.Len()+p.b.Len())
}

func TestConnectionLossFailsEveryStream(t *testing.T) {
	p := newPair(t, Config{})
	c1, s1 := p.open(t, agentprotocol.TunnelS3)
	c2, _ := p.open(t, agentprotocol.TunnelLoki)
	_, err := c1.Write([]byte("in flight"))
	require.NoError(t, err)

	p.b.Down(errors.New("agent connection closed"))
	_, err = s1.Read(make([]byte, 8))
	require.EqualError(t, err, "agent connection closed")
	require.Zero(t, p.b.Len())

	// A stream cannot be accepted or dialled on a down mux.
	_, err = p.b.Accept(99, agentprotocol.TunnelS3)
	require.ErrorIs(t, err, ErrDown)
	p.a.Down(nil)
	_, err = c2.Write([]byte("x"))
	require.ErrorIs(t, err, ErrDown)
	_, err = p.a.Dial(context.Background(), agentprotocol.TunnelS3)
	require.ErrorIs(t, err, ErrDown)
	require.Zero(t, p.a.Queued(), "queued data is dropped with the connection")
}

// A stream that completed while data was still queued is no longer in the
// stream table; the connection going down must still drop and un-count it.
func TestDownDropsQueuedDataOfCompletedStream(t *testing.T) {
	m := New(Config{Control: func(string, any) error { return nil }})
	m.Up()
	s, err := m.Accept(1, agentprotocol.TunnelS3)
	require.NoError(t, err)
	_, err = s.Write([]byte("in flight"))
	require.NoError(t, err)
	require.NoError(t, s.CloseWrite())
	m.HandleData(agentprotocol.EncodeTunnelFrame(1, agentprotocol.TunnelFlagFIN, nil))
	require.Zero(t, m.Len(), "both FINs exchanged: the stream completed")
	require.Equal(t, len("in flight"), m.Queued())

	m.Down(nil)
	require.Zero(t, m.Queued())
	_, ok := m.Next()
	require.False(t, ok)
}

// Next racing with Down must leave the counter exact: a frame is either
// written (and un-counted by Next) or dropped (and un-counted by Down).
func TestQueuedStaysExactWhenNextRacesDown(t *testing.T) {
	for range 200 {
		m := New(Config{Control: func(string, any) error { return nil }})
		m.Up()
		s, err := m.Accept(1, agentprotocol.TunnelS3)
		require.NoError(t, err)
		_, err = s.Write([]byte("in flight"))
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.Next() }()
		go func() { defer wg.Done(); m.Down(nil) }()
		wg.Wait()
		require.Zero(t, m.Queued())
	}
}

func TestRefusedOpenFailsDial(t *testing.T) {
	p := newPair(t, Config{})
	p.refuse = agentprotocol.TunnelCodeLimit
	_, err := p.a.Dial(context.Background(), agentprotocol.TunnelS3)
	var te *Error
	require.ErrorAs(t, err, &te)
	require.Equal(t, agentprotocol.TunnelCodeLimit, te.Code)
	require.Zero(t, p.a.Len())
}

func TestDialTimesOutWithoutAnAnswerAndTellsThePeer(t *testing.T) {
	p := newPair(t, Config{OpenTimeout: 50 * time.Millisecond})
	p.silent = true
	_, err := p.a.Dial(context.Background(), agentprotocol.TunnelS3)
	require.Error(t, err)
	require.Zero(t, p.a.Len())
	require.Equal(t, 1, p.ab.controlSeen(agentprotocol.TypeTunnelClose), "the peer is told to forget the stream")
}

func TestPeerOverrunningTheWindowIsAborted(t *testing.T) {
	p := newPair(t, Config{})
	_, s := p.open(t, agentprotocol.TunnelS3)
	// A misbehaving peer ignores credit: feed frames straight into the receiver.
	for i := 0; i <= agentprotocol.TunnelStreamWindow/agentprotocol.TunnelChunk; i++ {
		p.b.HandleData(agentprotocol.EncodeTunnelFrame(s.No(), 0, make([]byte, agentprotocol.TunnelChunk)))
	}
	_, err := s.Read(make([]byte, 1))
	var te *Error
	for err == nil { // drain what was accepted before the overrun
		_, err = s.Read(make([]byte, agentprotocol.TunnelChunk))
	}
	require.ErrorAs(t, err, &te)
	require.Equal(t, agentprotocol.TunnelCodeProtocol, te.Code)
	require.Equal(t, 1, p.ba.controlSeen(agentprotocol.TypeTunnelClose))
}

func TestInvalidAckIsAProtocolViolation(t *testing.T) {
	p := newPair(t, Config{})
	c, _ := p.open(t, agentprotocol.TunnelS3)
	// The sender has all its credit: any ack is more than was ever sent.
	p.a.HandleControl(agentprotocol.TypeTunnelAck, []byte(`{"v":1,"type":"tunnel_ack","no":`+itoa(c.No())+`,"bytes":10}`))
	_, err := c.Write([]byte("x"))
	var te *Error
	require.ErrorAs(t, err, &te)
	require.Equal(t, agentprotocol.TunnelCodeProtocol, te.Code)
}

func itoa(n uint32) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestMalformedAndUnknownFramesAreIgnored(t *testing.T) {
	p := newPair(t, Config{})
	c, s := p.open(t, agentprotocol.TunnelS3)
	p.b.HandleData([]byte{0x01})                                          // too short
	p.b.HandleData([]byte{0x7f, 0, 0, 0, 0, 1})                           // unknown frame kind
	p.b.HandleData(agentprotocol.EncodeTunnelFrame(4242, 0, []byte("x"))) // unknown stream
	p.b.HandleControl(agentprotocol.TypeTunnelAck, []byte(`not json`))
	p.b.HandleControl(agentprotocol.TypeTunnelClose, []byte(`{"no":4242}`))
	_, err := c.Write([]byte("still works"))
	require.NoError(t, err)
	buf := make([]byte, 32)
	n, err := s.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "still works", string(buf[:n]))
}

func TestCreditStallAbortsTheStream(t *testing.T) {
	p := newPair(t, Config{CreditTimeout: 80 * time.Millisecond, StreamWindow: 4 * agentprotocol.TunnelChunk})
	c, s := p.open(t, agentprotocol.TunnelS3)
	// The receiver never reads, so after one window the writer waits for credit.
	_, err := c.Write(make([]byte, 8*agentprotocol.TunnelChunk))
	var te *Error
	require.ErrorAs(t, err, &te)
	require.Equal(t, agentprotocol.TunnelCodeStalled, te.Code)
	// The peer learns about it and fails too.
	require.Eventually(t, func() bool { return p.b.Len() == 0 }, 2*time.Second, time.Millisecond)
	var err2 error
	for err2 == nil {
		_, err2 = s.Read(make([]byte, agentprotocol.TunnelChunk))
	}
	require.ErrorAs(t, err2, &te)
}

func TestIdleStreamIsClosed(t *testing.T) {
	p := newPair(t, Config{IdleTimeout: 60 * time.Millisecond})
	c, s := p.open(t, agentprotocol.TunnelS3)
	require.Eventually(t, func() bool { return p.a.Len() == 0 && p.b.Len() == 0 }, 2*time.Second, 5*time.Millisecond)
	_, err := c.Read(make([]byte, 1))
	var te *Error
	require.ErrorAs(t, err, &te)
	_, err = s.Read(make([]byte, 1))
	require.ErrorAs(t, err, &te)
}

func TestActiveStreamIsNotIdle(t *testing.T) {
	p := newPair(t, Config{IdleTimeout: 150 * time.Millisecond})
	c, s := p.open(t, agentprotocol.TunnelS3)
	go func() { _, _ = io.Copy(io.Discard, s) }()
	for i := 0; i < 10; i++ {
		_, err := c.Write([]byte("x"))
		require.NoError(t, err)
		time.Sleep(30 * time.Millisecond)
	}
	require.Equal(t, 1, p.a.Len())
}

func TestCloseWithoutPeerFinLingersThenAborts(t *testing.T) {
	p := newPair(t, Config{Linger: 60 * time.Millisecond})
	_, s := p.open(t, agentprotocol.TunnelS3)
	require.NoError(t, s.Close()) // the peer never closes its side
	require.Equal(t, 1, p.b.Len())
	require.Eventually(t, func() bool { return p.b.Len() == 0 && p.a.Len() == 0 }, 2*time.Second, 5*time.Millisecond)
}

func TestCloseDiscardsPeerDataAndCreditsIt(t *testing.T) {
	p := newPair(t, Config{})
	c, s := p.open(t, agentprotocol.TunnelS3)
	require.NoError(t, s.Close())
	// The peer keeps uploading into a closed stream: it must not stall on credit.
	data := make([]byte, 3*agentprotocol.TunnelStreamWindow)
	done := make(chan error, 1)
	go func() { _, err := c.Write(data); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("writer stalled on a stream the other end closed")
	}
}

func TestReadAndWriteHonourDeadlines(t *testing.T) {
	p := newPair(t, Config{StreamWindow: 2 * agentprotocol.TunnelChunk})
	c, s := p.open(t, agentprotocol.TunnelS3)

	require.NoError(t, s.SetReadDeadline(time.Now().Add(30*time.Millisecond)))
	_, err := s.Read(make([]byte, 1))
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	var ne net.Error
	require.ErrorAs(t, err, &ne)
	require.True(t, ne.Timeout())

	// A deadline in the past interrupts a blocked read (what net/http does) and can be reset.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = s.SetReadDeadline(time.Unix(1, 0))
	}()
	_, err = s.Read(make([]byte, 1))
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.NoError(t, s.SetReadDeadline(time.Time{}))
	_, err = c.Write([]byte("ok"))
	require.NoError(t, err)
	n, err := s.Read(make([]byte, 8))
	require.NoError(t, err)
	require.Equal(t, 2, n)

	require.NoError(t, c.SetWriteDeadline(time.Now().Add(30*time.Millisecond)))
	_, err = c.Write(make([]byte, 6*agentprotocol.TunnelChunk)) // more than the window, nobody reads
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
}

func TestLaneServesStreamsRoundRobin(t *testing.T) {
	m := New(Config{Control: func(string, any) error { return nil }})
	l := &m.lane
	for _, no := range []uint32{1, 2, 3} {
		for i := 0; i < 3; i++ {
			l.push(no, agentprotocol.EncodeTunnelFrame(no, 0, []byte{byte(i)}), 1)
		}
	}
	var order []uint32
	for {
		f, _, ok := l.pop()
		if !ok {
			break
		}
		fr, err := agentprotocol.DecodeTunnelFrame(f)
		require.NoError(t, err)
		order = append(order, fr.No)
	}
	require.Equal(t, []uint32{1, 2, 3, 1, 2, 3, 1, 2, 3}, order)
	require.Zero(t, l.drop(2))
}

func TestDuplicateAndZeroStreamNumbersAreRefused(t *testing.T) {
	m := New(Config{Control: func(string, any) error { return nil }})
	m.Up()
	defer m.Down(nil)
	_, err := m.Accept(7, agentprotocol.TunnelS3)
	require.NoError(t, err)
	_, err = m.Accept(7, agentprotocol.TunnelS3)
	require.ErrorIs(t, err, ErrDuplicate)
	_, err = m.Accept(0, agentprotocol.TunnelS3)
	require.ErrorIs(t, err, ErrDuplicate)
}
