package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The tunnel tests drive the real hub connection code (reader, writer, limits,
// acceptor hand-off) with a raw WebSocket as the agent. They need no database:
// the connection is registered directly instead of going through the
// challenge/response handshake, which the tests in hub_test.go cover.

type tunnelRig struct {
	t   *testing.T
	h   *Hub
	srv *httptest.Server
}

func newTunnelRig(t *testing.T) *tunnelRig {
	t.Helper()
	h := NewHub(nil, nil, nil, nil, nil)
	r := &tunnelRig{t: t, h: h}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(readLimit)
		q := req.URL.Query()
		serverID := uuid.MustParse(q.Get("server"))
		c := newAgentConn(h, ws, serverID, uuid.MustParse(q.Get("agent")))
		c.teamID = uuid.MustParse(q.Get("team"))
		if owner := q.Get("owner"); owner != "" {
			c.ownerID = uuid.MustParse(owner)
		}
		c.tunnelOK = q.Get("cap") != "0"
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.register(c)
		defer h.unregister(c)
		c.mux.Up()
		go c.writeLoop()
		c.readLoop(ctx)
		c.closeWith(int(websocket.StatusNormalClosure), "")
	}))
	t.Cleanup(func() { h.Close(); r.srv.Close() })
	return r
}

type testAgent struct {
	t                 *testing.T
	ws                *websocket.Conn
	serverID, agentID uuid.UUID
	team              uuid.UUID
	frames            chan readResult
}

type agentOpts struct {
	team  uuid.UUID
	owner uuid.UUID // who created the team; zero: the team itself
	noCap bool
}

func (r *tunnelRig) dial(o agentOpts) *testAgent {
	r.t.Helper()
	a := &testAgent{t: r.t, serverID: uuid.New(), agentID: uuid.New(), team: o.team}
	if a.team == uuid.Nil {
		a.team = uuid.New()
	}
	cap := "1"
	if o.noCap {
		cap = "0"
	}
	u := fmt.Sprintf("ws%s/?server=%s&agent=%s&team=%s&cap=%s", strings.TrimPrefix(r.srv.URL, "http"), a.serverID, a.agentID, a.team, cap)
	if o.owner != uuid.Nil {
		u += "&owner=" + o.owner.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, u, nil)
	require.NoError(r.t, err)
	ws.SetReadLimit(16 << 20)
	r.t.Cleanup(func() { ws.CloseNow() })
	a.ws = ws
	a.frames = make(chan readResult, 1<<16)
	go a.pump()
	require.Eventually(r.t, func() bool { return r.h.Online(a.serverID) }, 2*time.Second, time.Millisecond)
	return a
}

func (a *testAgent) sendText(typ string, body any) {
	a.t.Helper()
	b, err := agentprotocol.Encode(typ, "", body)
	require.NoError(a.t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(a.t, a.ws.Write(ctx, websocket.MessageText, b))
}

func (a *testAgent) sendData(no uint32, flags byte, payload []byte) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(a.t, a.ws.Write(ctx, websocket.MessageBinary, agentprotocol.EncodeTunnelFrame(no, flags, payload)))
}

// frame is what the hub sent: a control frame or a data frame.
type frame struct {
	ctl  agentprotocol.Frame
	data agentprotocol.TunnelFrame
	bin  bool
}

type readResult struct {
	f   frame
	err error
}

// pump reads the socket for good: a read that times out would close it.
func (a *testAgent) pump() {
	for {
		mt, b, err := a.ws.Read(context.Background())
		if err != nil {
			a.frames <- readResult{err: err}
			return
		}
		if mt == websocket.MessageBinary {
			f, err := agentprotocol.DecodeTunnelFrame(b)
			a.frames <- readResult{f: frame{data: f, bin: true}, err: err}
			continue
		}
		f, err := agentprotocol.Decode(b)
		a.frames <- readResult{f: frame{ctl: f}, err: err}
	}
}

func (a *testAgent) read(timeout time.Duration) (frame, error) {
	select {
	case r := <-a.frames:
		return r.f, r.err
	case <-time.After(timeout):
		return frame{}, context.DeadlineExceeded
	}
}

// nextControl reads until a control frame of typ arrives (data is skipped).
func (a *testAgent) nextControl(typ string) agentprotocol.Frame {
	a.t.Helper()
	for {
		f, err := a.read(5 * time.Second)
		require.NoError(a.t, err, "waiting for %s", typ)
		if !f.bin && f.ctl.Type == typ {
			return f.ctl
		}
	}
}

func (a *testAgent) open(no uint32, service string) agentprotocol.TunnelOpened {
	a.t.Helper()
	a.sendText(agentprotocol.TypeTunnelOpen, agentprotocol.TunnelOpen{No: no, Service: service})
	for {
		f := a.nextControl(agentprotocol.TypeTunnelOpened)
		var o agentprotocol.TunnelOpened
		require.NoError(a.t, f.Into(&o))
		if o.No == no {
			return o
		}
	}
}

// funcAcceptor adapts a function to TunnelAcceptor.
type funcAcceptor func(ctx context.Context, p TunnelPeer, c net.Conn) error

func (f funcAcceptor) AcceptTunnel(ctx context.Context, p TunnelPeer, c net.Conn) error {
	return f(ctx, p, c)
}

func idle(ctx context.Context, _ TunnelPeer, c net.Conn) error {
	_, _ = io.Copy(io.Discard, c)
	<-ctx.Done()
	return nil
}

func TestTunnelOpenDataAckAndClose(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})
	var gotPeer TunnelPeer
	var received atomic.Int64
	done := make(chan struct{})
	r.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, p TunnelPeer, c net.Conn) error {
		gotPeer = p
		n, err := io.Copy(io.Discard, c) // until the agent's FIN
		received.Store(n)
		if err != nil {
			return err
		}
		_, err = c.Write([]byte("stored"))
		close(done)
		return err
	}))

	o := a.open(1, agentprotocol.TunnelS3)
	require.Empty(t, o.Code)

	const total = 12 * agentprotocol.TunnelChunk // 768 KiB: one batched ack at 512 KiB
	for range total / agentprotocol.TunnelChunk {
		a.sendData(1, 0, make([]byte, agentprotocol.TunnelChunk))
	}
	a.sendData(1, agentprotocol.TunnelFlagFIN, nil)

	// The hub credits bytes in batches of a quarter window, not per frame.
	var acked, acks int
	var sawFIN bool
	var reply []byte
	for !sawFIN {
		f, err := a.read(5 * time.Second)
		require.NoError(t, err)
		switch {
		case f.bin:
			require.EqualValues(t, 1, f.data.No)
			reply = append(reply, f.data.Payload...)
			sawFIN = f.data.FIN()
		case f.ctl.Type == agentprotocol.TypeTunnelAck:
			var ack agentprotocol.TunnelAck
			require.NoError(t, f.ctl.Into(&ack))
			require.EqualValues(t, 1, ack.No)
			acked += ack.Bytes
			acks++
		}
	}
	<-done
	require.EqualValues(t, total, received.Load())
	require.Equal(t, "stored", string(reply))
	require.GreaterOrEqual(t, acked, agentprotocol.TunnelStreamWindow/4)
	require.LessOrEqual(t, acks, 2, "acks are batched")

	// Identity comes from the connection, with the service from the frame.
	require.Equal(t, TunnelPeer{ServerID: a.serverID, AgentID: a.agentID, Service: agentprotocol.TunnelS3}, gotPeer)

	// Both directions finished: nothing is left on the hub.
	require.Eventually(t, func() bool { return r.h.conn(a.serverID).mux.Len() == 0 }, 2*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { r.h.tun.mu.Lock(); defer r.h.tun.mu.Unlock(); return r.h.tun.global == 0 }, 2*time.Second, time.Millisecond)
}

func TestTunnelStreamLimits(t *testing.T) {
	t.Run("per service", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{rate: 1000}
		a := r.dial(agentOpts{})
		for no := uint32(1); no <= 16; no++ {
			require.Empty(t, a.open(no, agentprotocol.TunnelS3).Code, "s3 stream %d", no)
		}
		o := a.open(17, agentprotocol.TunnelS3)
		require.Equal(t, agentprotocol.TunnelCodeLimit, o.Code)
		// Another service is unaffected.
		require.Empty(t, a.open(18, agentprotocol.TunnelRegistry).Code)
		// Loki is capped lower.
		for no := uint32(20); no < 24; no++ {
			require.Empty(t, a.open(no, agentprotocol.TunnelLoki).Code)
		}
		require.Equal(t, agentprotocol.TunnelCodeLimit, a.open(24, agentprotocol.TunnelLoki).Code)
	})

	t.Run("per agent", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{rate: 1000}
		a := r.dial(agentOpts{})
		no := uint32(0)
		for range 16 {
			no++
			require.Empty(t, a.open(no, agentprotocol.TunnelS3).Code)
		}
		for range 12 {
			no++
			require.Empty(t, a.open(no, agentprotocol.TunnelRegistry).Code)
		}
		for range 4 { // 28 + 4 loki = 32
			no++
			require.Empty(t, a.open(no, agentprotocol.TunnelLoki).Code)
		}
		require.Equal(t, agentprotocol.MaxTunnelStreams, r.h.conn(a.serverID).mux.Len())
		no++
		o := a.open(no, agentprotocol.TunnelRegistry)
		require.Equal(t, agentprotocol.TunnelCodeLimit, o.Code, "33rd stream of the agent")
	})

	t.Run("a closed stream frees its slot", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, _ TunnelPeer, c net.Conn) error { return nil }))
		r.h.tun.limits = tunnelLimits{rate: 1000, perTeam: 2}
		a := r.dial(agentOpts{})
		for no := uint32(1); no <= 6; no++ { // the acceptor returns at once: each stream ends
			o := a.open(no, agentprotocol.TunnelS3)
			require.Empty(t, o.Code, "stream %d", no)
			a.sendData(no, agentprotocol.TunnelFlagFIN, nil) // agent's FIN completes it
			require.Eventually(t, func() bool { return r.h.conn(a.serverID).mux.Len() == 0 }, 2*time.Second, time.Millisecond)
		}
	})

	t.Run("open rate", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{rate: 3}
		a := r.dial(agentOpts{})
		var codes []string
		for no := uint32(1); no <= 5; no++ {
			codes = append(codes, a.open(no, agentprotocol.TunnelS3).Code)
		}
		require.Equal(t, []string{"", "", "", agentprotocol.TunnelCodeRateLimited, agentprotocol.TunnelCodeRateLimited}, codes)
	})

	t.Run("per team and global", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{perTeam: 3, global: 5, rate: 1000}
		teamA, teamB, teamC := uuid.New(), uuid.New(), uuid.New()
		a1 := r.dial(agentOpts{team: teamA})
		a2 := r.dial(agentOpts{team: teamA})
		b := r.dial(agentOpts{team: teamB})
		c := r.dial(agentOpts{team: teamC})
		require.Empty(t, a1.open(1, agentprotocol.TunnelS3).Code)
		require.Empty(t, a2.open(1, agentprotocol.TunnelS3).Code)
		require.Empty(t, a2.open(2, agentprotocol.TunnelS3).Code)
		// Team A holds 3 streams over two servers: its fourth is refused ...
		require.Equal(t, agentprotocol.TunnelCodeLimit, a1.open(2, agentprotocol.TunnelS3).Code)
		// ... while another team is not affected by it.
		require.Empty(t, b.open(1, agentprotocol.TunnelS3).Code)
		require.Empty(t, b.open(2, agentprotocol.TunnelS3).Code)
		// 5 streams exist control-plane wide: the global cap refuses team C.
		require.Equal(t, agentprotocol.TunnelCodeLimit, c.open(1, agentprotocol.TunnelS3).Code)
	})

	t.Run("teams of one owner share one quota", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{perTeam: 8, perOwner: 3, global: 100, rate: 1000}
		owner := uuid.New()
		a := r.dial(agentOpts{team: uuid.New(), owner: owner})
		b := r.dial(agentOpts{team: uuid.New(), owner: owner})
		other := r.dial(agentOpts{team: uuid.New(), owner: uuid.New()})
		require.Empty(t, a.open(1, agentprotocol.TunnelS3).Code)
		require.Empty(t, a.open(2, agentprotocol.TunnelS3).Code)
		require.Empty(t, b.open(1, agentprotocol.TunnelS3).Code)
		// Creating more teams does not multiply what one person may hold.
		require.Equal(t, agentprotocol.TunnelCodeLimit, b.open(2, agentprotocol.TunnelS3).Code)
		require.Equal(t, agentprotocol.TunnelCodeLimit, a.open(3, agentprotocol.TunnelS3).Code)
		require.Empty(t, other.open(1, agentprotocol.TunnelS3).Code, "another person is not affected")
	})

	t.Run("a crowded pool is shared fairly", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{perTeam: 8, perOwner: 8, global: 8, rate: 1000}
		x := r.dial(agentOpts{})
		y := r.dial(agentOpts{})
		for no := uint32(1); no <= 4; no++ {
			require.Empty(t, x.open(no, agentprotocol.TunnelS3).Code, "x %d", no)
		}
		require.Empty(t, y.open(1, agentprotocol.TunnelS3).Code)
		require.Empty(t, y.open(2, agentprotocol.TunnelS3).Code)
		// 6 of 8 slots are used: more than three quarters. x already holds its
		// half of the pool, y does not.
		require.Equal(t, agentprotocol.TunnelCodeLimit, x.open(5, agentprotocol.TunnelS3).Code)
		require.Empty(t, y.open(3, agentprotocol.TunnelS3).Code)
	})

	t.Run("bad requests", func(t *testing.T) {
		r := newTunnelRig(t)
		r.h.SetTunnelAcceptor(funcAcceptor(idle))
		r.h.tun.limits = tunnelLimits{rate: 1000}
		a := r.dial(agentOpts{})
		require.Equal(t, agentprotocol.TunnelCodeBadRequest, a.open(1, "grafana").Code, "unknown service")
		require.Equal(t, agentprotocol.TunnelCodeBadRequest, a.open(0, agentprotocol.TunnelS3).Code, "stream number 0")
		require.Empty(t, a.open(2, agentprotocol.TunnelS3).Code)
		require.Equal(t, agentprotocol.TunnelCodeBadRequest, a.open(2, agentprotocol.TunnelS3).Code, "duplicate stream number")
		require.Equal(t, 1, r.h.conn(a.serverID).mux.Len())
	})
}

func TestTunnelFromAnAgentWithoutTheCapabilityIsRefused(t *testing.T) {
	r := newTunnelRig(t)
	var accepted atomic.Int32
	r.h.SetTunnelAcceptor(funcAcceptor(func(context.Context, TunnelPeer, net.Conn) error { accepted.Add(1); return nil }))
	a := r.dial(agentOpts{noCap: true})
	require.False(t, r.h.TunnelCapable(a.serverID))

	o := a.open(1, agentprotocol.TunnelS3)
	require.Equal(t, agentprotocol.TunnelCodeUnsupported, o.Code)
	// Stray data frames of such a peer are ignored; the connection stays usable.
	a.sendData(1, 0, []byte("x"))
	a.sendText(agentprotocol.TypeTunnelAck, agentprotocol.TunnelAck{No: 1, Bytes: 5})
	require.Equal(t, agentprotocol.TunnelCodeUnsupported, a.open(2, agentprotocol.TunnelS3).Code)
	require.Zero(t, accepted.Load())
	require.Zero(t, r.h.conn(a.serverID).mux.Len())
}

func TestTunnelWithoutAcceptorIsUnavailable(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})
	require.True(t, r.h.TunnelCapable(a.serverID))
	require.Equal(t, agentprotocol.TunnelCodeUnavailable, a.open(1, agentprotocol.TunnelS3).Code)
}

func TestTunnelAcceptorErrorAbortsTheStreamWithItsReason(t *testing.T) {
	r := newTunnelRig(t)
	r.h.SetTunnelAcceptor(funcAcceptor(func(context.Context, TunnelPeer, net.Conn) error {
		return errors.New("built-in backup storage is not set up for this team")
	}))
	a := r.dial(agentOpts{})
	require.Empty(t, a.open(1, agentprotocol.TunnelS3).Code)
	f := a.nextControl(agentprotocol.TypeTunnelClose)
	var c agentprotocol.TunnelClose
	require.NoError(t, f.Into(&c))
	require.EqualValues(t, 1, c.No)
	require.Equal(t, agentprotocol.TunnelCodeError, c.Code)
	require.Equal(t, "built-in backup storage is not set up for this team", c.Reason)
	require.Eventually(t, func() bool { return r.h.conn(a.serverID).mux.Len() == 0 }, 2*time.Second, time.Millisecond)
}

func TestTunnelStreamsDropWhenTheAgentDisconnects(t *testing.T) {
	r := newTunnelRig(t)
	type result struct {
		readErr error
		ctxErr  error
	}
	results := make(chan result, 3)
	var started sync.WaitGroup
	started.Add(3)
	r.h.SetTunnelAcceptor(funcAcceptor(func(ctx context.Context, _ TunnelPeer, c net.Conn) error {
		started.Done()
		_, err := c.Read(make([]byte, 16)) // blocks until the stream dies
		<-ctx.Done()
		results <- result{readErr: err, ctxErr: ctx.Err()}
		return err
	}))
	a := r.dial(agentOpts{})
	for no := uint32(1); no <= 3; no++ {
		require.Empty(t, a.open(no, agentprotocol.TunnelS3).Code)
	}
	started.Wait()
	require.NoError(t, a.ws.Close(websocket.StatusGoingAway, "agent gone"))

	for range 3 {
		select {
		case res := <-results:
			require.Error(t, res.readErr)
			require.ErrorIs(t, res.ctxErr, context.Canceled)
		case <-time.After(5 * time.Second):
			t.Fatal("an acceptor kept running after its agent disconnected")
		}
	}
	require.Eventually(t, func() bool { return !r.h.Online(a.serverID) }, 2*time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		r.h.tun.mu.Lock()
		defer r.h.tun.mu.Unlock()
		return r.h.tun.global == 0 && len(r.h.tun.perTeam) == 0
	}, 2*time.Second, time.Millisecond, "limit counters are released")
}

func TestTunnelAgentOverrunningItsWindowIsAborted(t *testing.T) {
	r := newTunnelRig(t)
	release := make(chan struct{})
	r.h.SetTunnelAcceptor(funcAcceptor(func(ctx context.Context, _ TunnelPeer, c net.Conn) error {
		select { // never reads, so credit is never returned
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}))
	defer close(release)
	a := r.dial(agentOpts{})
	require.Empty(t, a.open(1, agentprotocol.TunnelS3).Code)
	// More than one window without a single ack from the hub.
	for range agentprotocol.TunnelStreamWindow/agentprotocol.TunnelChunk + 2 {
		a.sendData(1, 0, make([]byte, agentprotocol.TunnelChunk))
	}
	f := a.nextControl(agentprotocol.TypeTunnelClose)
	var c agentprotocol.TunnelClose
	require.NoError(t, f.Into(&c))
	require.Equal(t, agentprotocol.TunnelCodeProtocol, c.Code)
}

func TestTunnelHubSendsOnlyWhatTheAgentHasCredited(t *testing.T) {
	r := newTunnelRig(t)
	const total = 6 << 20
	r.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, _ TunnelPeer, c net.Conn) error {
		_, err := c.Write(make([]byte, total))
		return err
	}))
	a := r.dial(agentOpts{})
	require.Empty(t, a.open(1, agentprotocol.TunnelS3).Code)

	read := func(budget time.Duration) int {
		n := 0
		for {
			f, err := a.read(budget)
			if err != nil {
				return n
			}
			if f.bin {
				n += len(f.data.Payload)
			}
		}
	}
	// Without acks the hub stops after exactly one window.
	require.Equal(t, agentprotocol.TunnelStreamWindow, read(300*time.Millisecond))

	// Credit lets it continue until the whole body arrived.
	got := agentprotocol.TunnelStreamWindow
	for got < total {
		a.sendText(agentprotocol.TypeTunnelAck, agentprotocol.TunnelAck{No: 1, Bytes: agentprotocol.TunnelStreamWindow})
		n := read(300 * time.Millisecond)
		require.NotZero(t, n, "no data after credit was granted")
		got += n
	}
	require.Equal(t, total, got)
}

func TestTunnelStreamOfAnotherAgentCannotBeTouched(t *testing.T) {
	r := newTunnelRig(t)
	got := make(chan int, 1)
	r.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, _ TunnelPeer, c net.Conn) error {
		n, _ := io.Copy(io.Discard, c)
		got <- int(n)
		return nil
	}))
	victim := r.dial(agentOpts{})
	attacker := r.dial(agentOpts{})
	require.Empty(t, victim.open(1, agentprotocol.TunnelS3).Code)
	// The attacker guesses the victim's stream number: its frames hit its own table only.
	attacker.sendData(1, 0, []byte("injected"))
	attacker.sendText(agentprotocol.TypeTunnelClose, agentprotocol.TunnelClose{No: 1, Code: agentprotocol.TunnelCodeClosed})
	victim.sendData(1, 0, []byte("genuine"))
	victim.sendData(1, agentprotocol.TunnelFlagFIN, nil)
	select {
	case n := <-got:
		require.Equal(t, len("genuine"), n)
	case <-time.After(5 * time.Second):
		t.Fatal("victim stream did not complete")
	}
}

func TestHubWriterServesControlFramesBeforeTunnelData(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	h.tun.mux.StreamWindow = 16 << 20
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	c.mux.Up()
	defer c.mux.Down(nil)
	s, err := c.mux.Accept(1, agentprotocol.TunnelS3)
	require.NoError(t, err)
	_, err = s.Write(make([]byte, 40*agentprotocol.TunnelChunk))
	require.NoError(t, err)

	// A command queued behind forty chunks of bulk data is written first.
	require.NoError(t, c.sendFrame(agentprotocol.TypeCommand, "c1", agentprotocol.Command{ID: "c1"}))
	mt, data, ok := c.nextWrite()
	require.True(t, ok)
	require.Equal(t, websocket.MessageText, mt)
	f, err := agentprotocol.Decode(data)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.TypeCommand, f.Type)

	for range 10 {
		mt, data, ok = c.nextWrite()
		require.True(t, ok)
		require.Equal(t, websocket.MessageBinary, mt)
		require.Len(t, data, agentprotocol.TunnelHeaderLen+agentprotocol.TunnelChunk)
	}
	// Control that arrives mid-transfer jumps the remaining data too.
	require.NoError(t, c.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{}))
	mt, _, ok = c.nextWrite()
	require.True(t, ok)
	require.Equal(t, websocket.MessageText, mt)

	n := 0
	for {
		mt, _, ok = c.nextWrite()
		if !ok {
			break
		}
		require.Equal(t, websocket.MessageBinary, mt)
		n++
	}
	require.Equal(t, 30, n)
}

func TestHubSendQueueCannotBeFilledByBulkData(t *testing.T) {
	// Bulk data is bounded by credit and the connection cap, never by the
	// 512-frame control queue whose overflow closes the connection.
	h := NewHub(nil, nil, nil, nil, nil)
	h.tun.mux.StreamWindow = 64 << 20
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	c.mux.Up()
	defer c.mux.Down(nil)
	s, err := c.mux.Accept(1, agentprotocol.TunnelS3)
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Write(make([]byte, agentprotocol.TunnelConnWindow+4*agentprotocol.TunnelChunk))
	}()
	require.Eventually(t, func() bool { return c.mux.Queued() == agentprotocol.TunnelConnWindow }, 5*time.Second, time.Millisecond)
	require.Empty(t, c.out, "no control frame was used up by the bulk transfer")
	select {
	case <-done:
		t.Fatal("the writer was not held back at the connection cap")
	case <-time.After(50 * time.Millisecond):
	}
	for { // the socket drains: the writer finishes
		if _, _, ok := c.nextWrite(); !ok {
			select {
			case <-done:
				return
			default:
			}
		}
	}
}
