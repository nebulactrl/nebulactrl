package agents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// wsPair returns both ends of a WebSocket: the one the hub would hold and the
// agent's.
func wsPair(t *testing.T) (hubSide, agentSide *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- ws
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { client.CloseNow() })
	select {
	case hubSide = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not accept the socket")
	}
	t.Cleanup(func() { hubSide.CloseNow() })
	return hubSide, client
}

// closeOf reads the agent's socket until the hub closes it.
func closeOf(t *testing.T, ws *websocket.Conn) websocket.CloseError {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := ws.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		require.True(t, errors.As(err, &ce), "the socket ended without a close frame: %v", err)
		return ce
	}
}

func TestClosingAConnectionSendsTheFirstCodeAndAtMost120BytesOfReason(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	hubSide, agentSide := wsPair(t)
	c := newAgentConn(h, hubSide, uuid.New(), uuid.New())

	require.False(t, c.closed())
	// A close reason longer than 123 bytes is a protocol error on the wire: it is cut.
	c.closeWith(4000, strings.Repeat("r", 300))
	c.closeWith(4001, "second call is ignored")
	require.True(t, c.closed())

	ce := closeOf(t, agentSide)
	require.EqualValues(t, 4000, ce.Code)
	require.Equal(t, strings.Repeat("r", 120), ce.Reason)
}

func TestAFailedWriteClosesTheConnectionAndStopsTheWriter(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	hubSide, _ := wsPair(t)
	c := newAgentConn(h, hubSide, uuid.New(), uuid.New())
	c.mux.Up()
	t.Cleanup(func() { c.mux.Down(nil) })
	_ = hubSide.CloseNow() // the socket is gone underneath the hub

	require.NoError(t, c.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{}))
	stopped := make(chan struct{})
	go func() { c.writeLoop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer kept running on a dead socket")
	}
	require.True(t, c.closed(), "a failed write closes the connection so the agent reconnects")
	require.False(t, c.write(websocket.MessageText, []byte("late")))
}

func TestTheWriterStopsWhenTheConnectionIsClosed(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	hubSide, agentSide := wsPair(t)
	c := newAgentConn(h, hubSide, uuid.New(), uuid.New())
	c.mux.Up()
	t.Cleanup(func() { c.mux.Down(nil) })
	stopped := make(chan struct{})
	go func() { c.writeLoop(); close(stopped) }()

	// Idle writer wakes for a frame, writes it ...
	require.NoError(t, c.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := agentSide.Read(ctx)
	require.NoError(t, err)
	f, err := agentprotocol.Decode(data)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.TypeReconcile, f.Type)

	// ... and returns once the connection is closed.
	c.closeWith(int(websocket.StatusNormalClosure), "bye")
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer outlived its connection")
	}
	// The agent sees the close; without a reader its close handshake would
	// hold the test's cleanup for the websocket library's five seconds.
	ce := closeOf(t, agentSide)
	require.EqualValues(t, websocket.StatusNormalClosure, ce.Code)
	require.Equal(t, "bye", ce.Reason)
}

func TestSendFrameOnAFullQueueEndsWhenTheConnectionCloses(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	for range outQueue {
		c.out <- []byte("queued")
	}
	errc := make(chan error, 1)
	go func() { errc <- c.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{}) }()
	c.closeOnce.Do(func() { close(c.done) })
	select {
	case err := <-errc:
		require.ErrorIs(t, err, errConnClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("sendFrame did not notice the closed connection")
	}
	require.Len(t, c.out, outQueue, "nothing was squeezed into the full queue")
}

func TestSendFrameRejectsABodyThatCannotBeEncoded(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	require.Error(t, c.sendFrame(agentprotocol.TypeCommand, "x", make(chan int)))
	require.Empty(t, c.out)
}

// A connection whose send queue stays full is closed so the agent reconnects
// and resyncs. The wait is sendStallTimeout (five seconds), shortened here.
func TestAStalledSendQueueClosesTheConnection(t *testing.T) {
	t.Parallel()
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	hubSide, agentSide := wsPair(t)
	c := newAgentConn(h, hubSide, uuid.New(), uuid.New())
	require.Equal(t, 5*time.Second, c.sendTimeout, "the production wait")
	c.sendTimeout = 50 * time.Millisecond
	for range outQueue {
		c.out <- []byte("queued") // no writer runs: the queue never drains
	}
	start := time.Now()
	err := c.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{})
	require.ErrorContains(t, err, "stalled")
	require.GreaterOrEqual(t, time.Since(start), c.sendTimeout, "the sender waits for room before giving up")
	require.True(t, c.closed())
	ce := closeOf(t, agentSide)
	require.EqualValues(t, websocket.StatusInternalError, ce.Code)
	require.Equal(t, "send queue stalled", ce.Reason)
}

func TestConnectRefusesAnythingButAWebSocketUpgrade(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	w := httptest.NewRecorder()
	h.handleConnect(w, httptest.NewRequest(http.MethodGet, "/agent/v1/connect", nil))
	require.Equal(t, http.StatusUpgradeRequired, w.Code)
	require.Empty(t, h.conns, "nothing was registered")
}

// A key that stopped attempting for ten minutes is forgotten; one that keeps
// attempting is not, so the limiter's memory stays bounded without letting a
// busy address reset its allowance.
func TestRateLimiterForgetsIdleKeysAfterTenMinutes(t *testing.T) {
	l := newIPLimiter(2, time.Minute)
	require.True(t, l.Allow("idle"))
	require.True(t, l.Allow("idle"))
	require.False(t, l.Allow("idle"), "the burst of two is used up")
	require.True(t, l.Allow("busy"))
	require.True(t, l.Allow("busy"))
	require.False(t, l.Allow("busy"))

	// Time passes: the sweep is due, "idle" was last seen long ago.
	l.mu.Lock()
	l.lastGC = time.Now().Add(-6 * time.Minute)
	l.entries["idle"].seen = time.Now().Add(-11 * time.Minute)
	l.entries["busy"].seen = time.Now().Add(-9 * time.Minute)
	l.mu.Unlock()

	require.True(t, l.Allow("idle"), "an idle key starts over with a full allowance")
	require.False(t, l.Allow("busy"), "a key seen within ten minutes keeps its spent allowance")
	l.mu.Lock()
	defer l.mu.Unlock()
	require.Len(t, l.entries, 2)
}
