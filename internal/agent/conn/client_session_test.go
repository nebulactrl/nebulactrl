package conn

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// newHarness is a Client wired to a fake control plane. The caller sets
// srv.script before starting the client with runClient.
func newHarness(t *testing.T) (*Client, *store.Store, *fakeServer) {
	t.Helper()
	var srv *fakeServer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	c, st, s := newTestClient(t, ts.URL)
	srv = s
	return c, st, s
}

// runClient starts c.Run and stops it when the test ends. The returned channel
// carries Run's result.
func runClient(t *testing.T, c *Client) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	res := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		res <- c.Run(ctx)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return cancel, res
}

func wsSend(t *testing.T, ctx context.Context, ws *websocket.Conn, typ, id string, body any) {
	t.Helper()
	b, err := agentprotocol.Encode(typ, id, body)
	if err != nil {
		t.Errorf("encode %s: %v", typ, err)
		return
	}
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Errorf("write %s: %v", typ, err)
	}
}

// wsFrames feeds every decoded text frame to fn until fn returns true or the
// socket ends.
func wsFrames(ctx context.Context, ws *websocket.Conn, fn func(agentprotocol.Frame) bool) {
	for {
		mt, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if mt != websocket.MessageText {
			continue
		}
		f, err := agentprotocol.Decode(data)
		if err != nil {
			continue
		}
		if fn(f) {
			return
		}
	}
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for event")
		panic("unreachable")
	}
}

func TestHelloCarriesFactsAndUnackedResultsAndHeartbeatsFlow(t *testing.T) {
	c, st, srv := newHarness(t)
	require.NoError(t, st.PutResult(agentprotocol.ResultFrame{CommandID: "old", Outcome: agentprotocol.OutcomeSucceeded}))
	c.cfg.HeartbeatInterval = 5 * time.Millisecond
	c.cfg.Facts = func(context.Context) agentprotocol.Facts { return agentprotocol.Facts{OS: "linux", CPUs: 4} }
	c.cfg.Heartbeat = func(context.Context) agentprotocol.Heartbeat {
		return agentprotocol.Heartbeat{CPUPercent: 12.5, DockerVersion: "27.1"}
	}

	hellos := make(chan agentprotocol.Hello, 1)
	beats := make(chan agentprotocol.Heartbeat, 64)
	srv.script = func(ctx context.Context, ws *websocket.Conn, h agentprotocol.Hello) {
		hellos <- h
		wsSend(t, ctx, ws, agentprotocol.TypeResultAck, "", agentprotocol.ResultAck{CommandID: "old"})
		wsFrames(ctx, ws, func(f agentprotocol.Frame) bool {
			if f.Type == agentprotocol.TypeHeartbeat {
				var hb agentprotocol.Heartbeat
				if f.Into(&hb) == nil {
					select {
					case beats <- hb:
					default:
					}
				}
			}
			return false
		})
	}
	cancel, res := runClient(t, c)

	h := recv(t, hellos)
	require.Equal(t, "test", h.AgentVersion)
	require.Equal(t, agentprotocol.ProtocolVersion, h.ProtocolVersion)
	require.Equal(t, "linux", h.Facts.OS)
	require.Equal(t, 4, h.Facts.CPUs)
	require.Len(t, h.UnackedResults, 1, "results the control plane has not acknowledged are replayed in hello")
	require.Equal(t, "old", h.UnackedResults[0].CommandID)

	first, second := recv(t, beats), recv(t, beats)
	require.InDelta(t, 12.5, first.CPUPercent, 0.001)
	require.Equal(t, "27.1", second.DockerVersion)

	require.Eventually(t, func() bool {
		un, err := st.UnackedResults()
		return err == nil && len(un) == 0
	}, 5*time.Second, 5*time.Millisecond, "result_ack clears the stored result")
	require.True(t, c.Connected())

	cancel()
	require.ErrorIs(t, recv(t, res), context.Canceled)
	require.False(t, c.Connected())
}

func TestInboundFramesAreRoutedAndGarbageIsIgnored(t *testing.T) {
	c, _, srv := newHarness(t)
	traffic := make(chan uint64, 4)
	probes := make(chan uint64, 4)
	c.SetTrafficAckHandler(func(seq uint64) { traffic <- seq })
	c.SetProbesAckHandler(func(seq uint64) { probes <- seq })

	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		_ = ws.Write(ctx, websocket.MessageText, []byte("this is not json"))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"mystery"}`))
		_ = ws.Write(ctx, websocket.MessageBinary, []byte{0x01})
		// Malformed bodies of known types are skipped, not fatal.
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"traffic_ack","seq":"x"}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"probes_ack","seq":"x"}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"result_ack","commandId":5}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"command","id":5}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"reconcile","services":5}`))
		wsSend(t, ctx, ws, agentprotocol.TypeTrafficAck, "", agentprotocol.TrafficAck{Seq: 41})
		wsSend(t, ctx, ws, agentprotocol.TypeProbesAck, "", agentprotocol.ProbesAck{Seq: 42})
		<-ctx.Done()
	}
	runClient(t, c)

	require.EqualValues(t, 41, recv(t, traffic))
	require.EqualValues(t, 42, recv(t, probes))
	require.Empty(t, traffic)
	require.Empty(t, probes)
}

// orderedReconciler records the order in which the read loop and the
// asynchronous reconcile saw each frame.
type orderedReconciler struct {
	events chan string
}

func (r *orderedReconciler) ObserveFrame(_ context.Context, rec agentprotocol.Reconcile) {
	r.events <- "observe:" + rec.ACMEEmail
}

func (r *orderedReconciler) Reconcile(_ context.Context, rec agentprotocol.Reconcile) error {
	r.events <- "reconcile:" + rec.ACMEEmail
	return nil
}

func TestReconcileFrameIsObservedOnTheReadLoopThenReconciled(t *testing.T) {
	c, _, srv := newHarness(t)
	rec := &orderedReconciler{events: make(chan string, 8)}
	c.SetReconciler(rec)
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{ACMEEmail: "ops@example.com", Hello: true})
		<-ctx.Done()
	}
	runClient(t, c)

	require.Equal(t, "observe:ops@example.com", recv(t, rec.events), "ObserveFrame runs before the asynchronous Reconcile")
	require.Equal(t, "reconcile:ops@example.com", recv(t, rec.events))
}

type failingReconciler struct{ called chan struct{} }

func (r failingReconciler) Reconcile(context.Context, agentprotocol.Reconcile) error {
	r.called <- struct{}{}
	return errors.New("traefik down")
}

func TestReconcileErrorsDoNotEndTheSession(t *testing.T) {
	c, _, srv := newHarness(t)
	rec := failingReconciler{called: make(chan struct{}, 4)}
	c.SetReconciler(rec)
	traffic := make(chan uint64, 1)
	c.SetTrafficAckHandler(func(seq uint64) { traffic <- seq })
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{})
		wsSend(t, ctx, ws, agentprotocol.TypeTrafficAck, "", agentprotocol.TrafficAck{Seq: 1})
		<-ctx.Done()
	}
	runClient(t, c)
	recv(t, rec.called)
	require.EqualValues(t, 1, recv(t, traffic), "the connection keeps serving frames after a failed reconcile")
}

func TestReconcileWithoutReconcilerIsIgnored(t *testing.T) {
	c := New(Config{})
	// No reconciler installed: must not panic and must not block.
	c.onReconcile(agentprotocol.Reconcile{})
}

// resultObserver is a Handler that also wants to know when its result was queued.
type resultObserver struct {
	run  func(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame
	sent chan agentprotocol.ResultFrame
}

func (h *resultObserver) HandleCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	return h.run(ctx, cmd)
}

func (h *resultObserver) ResultSent(_ context.Context, _ agentprotocol.Command, res agentprotocol.ResultFrame) {
	h.sent <- res
}

func cmdPayload(id, kind string) agentprotocol.Command {
	return agentprotocol.Command{ID: id, Kind: kind, Payload: json.RawMessage(`{}`)}
}

// collectResults gathers result frames by command ID until want results are in.
func collectResults(ctx context.Context, ws *websocket.Conn, want int, out chan<- map[string][]agentprotocol.ResultFrame) {
	got := map[string][]agentprotocol.ResultFrame{}
	n := 0
	wsFrames(ctx, ws, func(f agentprotocol.Frame) bool {
		if f.Type != agentprotocol.TypeResult {
			return false
		}
		var r agentprotocol.ResultFrame
		if f.Into(&r) != nil {
			return false
		}
		got[r.CommandID] = append(got[r.CommandID], r)
		n++
		return n == want
	})
	out <- got
}

func TestCommandEdgeCases(t *testing.T) {
	c, st, srv := newHarness(t)

	var ran sync.Map
	h := &resultObserver{sent: make(chan agentprotocol.ResultFrame, 4)}
	h.run = func(_ context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
		ran.Store(cmd.ID, true)
		if cmd.ID == "panics" {
			panic("boom")
		}
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{"id":"` + cmd.ID + `"}`)}
	}
	c.SetHandler(h)

	// "seen" was received before an agent restart but never finished: it runs again.
	require.NoError(t, st.MarkCommand("seen"))

	past := time.Now().Add(-time.Minute)
	results := make(chan map[string][]agentprotocol.ResultFrame, 1)
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		expired := cmdPayload("expired", agentprotocol.KindRestartService)
		expired.ExpiresAt = past
		seenExpired := cmdPayload("seen", agentprotocol.KindRestartService)
		seenExpired.ExpiresAt = past // already delivered once: expiry no longer applies
		for _, cmd := range []agentprotocol.Command{
			expired,
			seenExpired,
			cmdPayload("panics", agentprotocol.KindRestartService),
			cmdPayload("plain", agentprotocol.KindStopService),
		} {
			wsSend(t, ctx, ws, agentprotocol.TypeCommand, cmd.ID, cmd)
		}
		collectResults(ctx, ws, 4, results)
		<-ctx.Done()
	}
	runClient(t, c)

	got := recv(t, results)
	require.Equal(t, agentprotocol.OutcomeFailed, got["expired"][0].Outcome)
	require.Equal(t, "command expired before delivery", got["expired"][0].Error)
	_, ranExpired := ran.Load("expired")
	require.False(t, ranExpired, "an expired command never runs")

	require.Equal(t, agentprotocol.OutcomeSucceeded, got["seen"][0].Outcome, "a command seen before a restart runs again")
	require.Equal(t, agentprotocol.OutcomeFailed, got["panics"][0].Outcome)
	require.Equal(t, "internal error: boom", got["panics"][0].Error, "a panicking handler becomes a failed result")
	require.Equal(t, agentprotocol.OutcomeSucceeded, got["plain"][0].Outcome)
	require.JSONEq(t, `{"id":"plain"}`, string(got["plain"][0].Data))

	// ResultSent fires once per handled command, after its result was stored.
	sentIDs := map[string]bool{}
	for range 3 {
		sentIDs[recv(t, h.sent).CommandID] = true
	}
	require.Equal(t, map[string]bool{"seen": true, "panics": true, "plain": true}, sentIDs)

	for _, id := range []string{"expired", "seen", "panics", "plain"} {
		stored, seen := st.SeenCommand(id)
		require.True(t, seen, id)
		require.NotNil(t, stored, "%s result is durable until acknowledged", id)
	}
}

func TestEphemeralResultKindsSendRowsButStoreNoData(t *testing.T) {
	c, st, srv := newHarness(t)
	rows := json.RawMessage(`{"results":[{"rows":[["1"],[null]]}]}`)
	c.SetHandler(HandlerFunc(func(_ context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: rows}
	}))
	results := make(chan map[string][]agentprotocol.ResultFrame, 1)
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "q1", cmdPayload("q1", agentprotocol.KindDBQuery))
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "l1", cmdPayload("l1", agentprotocol.KindLogsQuery))
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "f1", cmdPayload("f1", agentprotocol.KindLogsFacets))
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "r1", cmdPayload("r1", agentprotocol.KindRestartService))
		collectResults(ctx, ws, 4, results)
		<-ctx.Done()
	}
	runClient(t, c)

	got := recv(t, results)
	for _, id := range []string{"q1", "l1", "f1"} {
		require.JSONEq(t, string(rows), string(got[id][0].Data), "%s: the frame sent to the control plane carries the data", id)
		stored, seen := st.SeenCommand(id)
		require.True(t, seen, id)
		require.NotNil(t, stored, id)
		require.Equal(t, agentprotocol.OutcomeSucceeded, stored.Outcome, id)
		require.Empty(t, stored.Data, "%s: database rows and log lines never land in the agent's result store", id)
	}

	storedOther, _ := st.SeenCommand("r1")
	require.NotNil(t, storedOther)
	require.JSONEq(t, string(rows), string(storedOther.Data), "other kinds keep their data")
}

func TestCommandWithoutHandlerFails(t *testing.T) {
	c, _, srv := newHarness(t)
	results := make(chan map[string][]agentprotocol.ResultFrame, 1)
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "c1", cmdPayload("c1", agentprotocol.KindDeploy))
		collectResults(ctx, ws, 1, results)
		<-ctx.Done()
	}
	runClient(t, c)
	got := recv(t, results)
	require.Equal(t, agentprotocol.OutcomeFailed, got["c1"][0].Outcome)
	require.Equal(t, "no command handler installed", got["c1"][0].Error)
}

func TestRedeliveredRunningCommandRunsOnce(t *testing.T) {
	c, _, srv := newHarness(t)
	var slowRuns atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	c.SetHandler(HandlerFunc(func(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
		if cmd.ID == "slow" {
			slowRuns.Add(1)
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done(): // a failed assertion must not leave Run waiting on this handler
			}
		}
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded}
	}))
	results := make(chan map[string][]agentprotocol.ResultFrame, 1)
	redeliver := make(chan struct{})
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "slow", cmdPayload("slow", agentprotocol.KindDeploy))
		select {
		case <-redeliver:
		case <-ctx.Done():
			return
		}
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "slow", cmdPayload("slow", agentprotocol.KindDeploy))
		// The read loop handles frames in order: once the marker's result is back,
		// the redelivery has been fully processed.
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "marker", cmdPayload("marker", agentprotocol.KindDeploy))
		collectResults(ctx, ws, 2, results)
		<-ctx.Done()
	}
	runClient(t, c)

	recv(t, started)
	require.Equal(t, 1, c.InflightCommands())
	close(redeliver)
	require.Eventually(t, func() bool {
		stored, seen := c.cfg.Store.SeenCommand("marker")
		return seen && stored != nil
	}, 5*time.Second, 2*time.Millisecond)
	require.EqualValues(t, 1, slowRuns.Load(), "a redelivery of a running command does not start it again")
	// The marker's own handler goroutine may not have unregistered yet when its
	// result is stored; only the slow command stays in flight.
	require.Eventually(t, func() bool { return c.InflightCommands() == 1 }, 5*time.Second, 2*time.Millisecond)
	close(release)

	got := recv(t, results)
	require.Len(t, got["slow"], 1)
	require.Len(t, got["marker"], 1)
	require.EqualValues(t, 1, slowRuns.Load())
	require.Eventually(t, func() bool { return c.InflightCommands() == 0 }, 5*time.Second, 2*time.Millisecond)
}

func TestCommandInterruptedByShutdownStoresNoResult(t *testing.T) {
	c, st, srv := newHarness(t)
	started := make(chan struct{})
	c.SetHandler(HandlerFunc(func(ctx context.Context, _ agentprotocol.Command) agentprotocol.ResultFrame {
		close(started)
		<-ctx.Done()
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "interrupted"}
	}))
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeCommand, "c1", cmdPayload("c1", agentprotocol.KindDeploy))
		<-ctx.Done()
	}
	cancel, res := runClient(t, c)
	recv(t, started)
	cancel()
	require.ErrorIs(t, recv(t, res), context.Canceled)

	stored, seen := st.SeenCommand("c1")
	require.True(t, seen, "the command stays recorded so it runs again after restart")
	require.Nil(t, stored, "an interrupted handler's result is not authoritative")
}

func TestProtocolVersionMismatchEndsRunWithClose4400(t *testing.T) {
	c, _, srv := newHarness(t)
	closeCode := make(chan websocket.StatusCode, 1)
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"v":2,"type":"command"}`))
		_, _, err := ws.Read(ctx)
		closeCode <- websocket.CloseStatus(err)
	}
	_, res := runClient(t, c)
	require.ErrorIs(t, recv(t, res), ErrUnsupportedVersion)
	require.Equal(t, websocket.StatusCode(agentprotocol.CloseUnsupportedVersion), recv(t, closeCode))
}

func TestServerCloseWith4400EndsRun(t *testing.T) {
	c, _, srv := newHarness(t)
	srv.script = func(_ context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseUnsupportedVersion), "old agent")
	}
	_, res := runClient(t, c)
	require.ErrorIs(t, recv(t, res), ErrUnsupportedVersion)
}

// rawClient dials a handler that scripts the server side of the handshake.
func rawClient(t *testing.T, h func(ctx context.Context, ws *websocket.Conn)) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		h(r.Context(), ws)
	}))
	t.Cleanup(ts.Close)
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return New(Config{
		URL:      "ws" + strings.TrimPrefix(ts.URL, "http"),
		Identity: store.Identity{AgentID: "a", ServerID: "s", PrivateKey: priv},
		Store:    st,
		Version:  "test",
	})
}

func TestHandshakeRejectsBadChallenges(t *testing.T) {
	cases := []struct {
		name    string
		first   string // raw first message; "" closes without sending
		wantErr string
		wantIs  error
	}{
		{name: "closed before challenge", wantErr: "read challenge"},
		{name: "not json", first: "nope", wantErr: "decode envelope"},
		{name: "other major version", first: `{"v":2,"type":"challenge"}`, wantIs: ErrUnsupportedVersion},
		{name: "wrong frame type", first: `{"v":1,"type":"heartbeat"}`, wantErr: `expected challenge, got "heartbeat"`},
		{name: "malformed challenge", first: `{"v":1,"type":"challenge","nonce":5}`, wantErr: "decode challenge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := rawClient(t, func(ctx context.Context, ws *websocket.Conn) {
				if tc.first != "" {
					_ = ws.Write(ctx, websocket.MessageText, []byte(tc.first))
					// Keep the socket open until the client is done with it.
					_, _, _ = ws.Read(ctx)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := c.session(ctx)
			require.Error(t, err)
			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
			require.False(t, c.Connected(), "a failed handshake never marks the client connected")
		})
	}
}

func TestSessionDialFailureReportsHTTPStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	c := New(Config{URL: "ws" + strings.TrimPrefix(ts.URL, "http")})
	err := c.session(context.Background())
	require.ErrorContains(t, err, "HTTP 503")

	dead := New(Config{URL: "ws://127.0.0.1:1/agent/v1/connect"})
	err = dead.session(context.Background())
	require.ErrorContains(t, err, "dial ws://127.0.0.1:1/agent/v1/connect")
	require.NotContains(t, err.Error(), "HTTP ")
}

// A dropped connection reconnects after the backoff and flushes what was queued
// meanwhile; a fresh session replays unacknowledged state in hello.
// fakeBackoff replaces the reconnect timer: each requested delay is reported,
// and the wait ends only when the test fires it.
type fakeBackoff struct {
	delays chan time.Duration
	fire   chan chan time.Time
}

func newFakeBackoff(c *Client) *fakeBackoff {
	b := &fakeBackoff{delays: make(chan time.Duration, 8), fire: make(chan chan time.Time, 8)}
	c.cfg.ReconnectAfter = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		b.fire <- ch
		b.delays <- d
		return ch
	}
	return b
}

// next returns the delay the client is now waiting out and the func that ends it.
func (b *fakeBackoff) next(t *testing.T) (time.Duration, func()) {
	t.Helper()
	d := recv(t, b.delays)
	ch := <-b.fire
	return d, func() { ch <- time.Now() }
}

func TestRunReconnectsAfterDropAndFlushesQueuedFrames(t *testing.T) {
	c, _, srv := newHarness(t)
	backoff := newFakeBackoff(c)
	var conns atomic.Int32
	letDrop := make(chan struct{})
	events := make(chan agentprotocol.EventFrame, 4)
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		if conns.Add(1) == 1 {
			select {
			case <-letDrop:
			case <-ctx.Done():
			}
			// Drop the first connection. A close frame keeps the client from
			// waiting out its race window for a better close code.
			_ = ws.Close(websocket.StatusGoingAway, "restart")
			return
		}
		wsFrames(ctx, ws, func(f agentprotocol.Frame) bool {
			if f.Type != agentprotocol.TypeEvent {
				return false
			}
			var e agentprotocol.EventFrame
			if f.Into(&e) == nil {
				events <- e
			}
			return true
		})
		<-ctx.Done()
	}
	runClient(t, c)

	require.Eventually(t, c.Connected, 5*time.Second, time.Millisecond)
	close(letDrop)
	delay, elapse := backoff.next(t)
	require.GreaterOrEqual(t, delay, minBackoff, "the first retry waits the base backoff")
	require.LessOrEqual(t, delay, minBackoff+minBackoff/4, "plus at most 25% jitter")
	require.False(t, c.Connected())
	c.Event(agentprotocol.EventFrame{Message: "queued while offline"})

	// Nothing reconnects while the backoff is still running.
	require.Never(t, func() bool { return conns.Load() > 1 }, 30*time.Millisecond, time.Millisecond)
	elapse()

	require.Equal(t, "queued while offline", recv(t, events).Message)
	require.EqualValues(t, 2, conns.Load(), "exactly one reconnect")
	require.True(t, c.Connected())
}

func TestReconnectBackoffDoublesWhileTheLinkKeepsDropping(t *testing.T) {
	c, _, srv := newHarness(t)
	backoff := newFakeBackoff(c)
	srv.script = func(_ context.Context, ws *websocket.Conn, _ agentprotocol.Hello) { // every session ends at once
		_ = ws.Close(websocket.StatusGoingAway, "restart")
	}
	runClient(t, c)

	for _, base := range []time.Duration{minBackoff, 2 * minBackoff, 4 * minBackoff} {
		delay, elapse := backoff.next(t)
		require.GreaterOrEqual(t, delay, base)
		require.LessOrEqual(t, delay, base+base/4)
		elapse()
	}
}

type streamRecorder struct {
	frames       chan agentprotocol.Frame
	disconnected chan struct{}
}

func (s *streamRecorder) HandleCommand(context.Context, agentprotocol.Command) agentprotocol.ResultFrame {
	return agentprotocol.ResultFrame{}
}
func (s *streamRecorder) HandleStream(f agentprotocol.Frame) { s.frames <- f }
func (s *streamRecorder) Disconnected()                      { s.disconnected <- struct{}{} }

func TestStreamFramesReachTheStreamHandlerAndDisconnectEndsThem(t *testing.T) {
	c, _, srv := newHarness(t)
	sh := &streamRecorder{frames: make(chan agentprotocol.Frame, 4), disconnected: make(chan struct{}, 4)}
	c.SetHandler(sh)

	require.False(t, c.Stream(agentprotocol.TypeExecOutput, "s1", struct{}{}), "streams are dropped while disconnected")

	opened := make(chan agentprotocol.Frame, 1)
	drop := make(chan struct{})
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		wsSend(t, ctx, ws, agentprotocol.TypeExecInput, "s1", agentprotocol.ExecInput{})
		go wsFrames(ctx, ws, func(f agentprotocol.Frame) bool {
			if f.Type == agentprotocol.TypeExecOpened {
				opened <- f
				return true
			}
			return false
		})
		select {
		case <-drop:
		case <-ctx.Done():
		}
	}
	runClient(t, c)

	f := recv(t, sh.frames)
	require.Equal(t, agentprotocol.TypeExecInput, f.Type)
	require.Equal(t, "s1", f.ID)

	require.Eventually(t, c.Connected, 5*time.Second, time.Millisecond)
	require.True(t, c.Stream(agentprotocol.TypeExecOpened, "s1", agentprotocol.ExecOpened{}))
	require.Equal(t, "s1", recv(t, opened).ID)

	close(drop)
	recv(t, sh.disconnected)
}

func TestSenderMethodsQueueTheirFrameType(t *testing.T) {
	c := New(Config{})
	c.Event(agentprotocol.EventFrame{Message: "e"})
	c.Log(agentprotocol.LogFrame{})
	c.LogEnd(agentprotocol.LogEnd{})
	c.Stats(agentprotocol.Stats{})
	c.Observed(agentprotocol.Observed{})
	c.Traffic(agentprotocol.Traffic{Seq: 3})
	c.Probes(agentprotocol.Probes{Seq: 4})
	c.LogStatus(agentprotocol.LogStatus{Capturing: true, TotalBytes: 1})
	c.LogStatus(agentprotocol.LogStatus{Capturing: true, TotalBytes: 2})
	c.SourceScan(agentprotocol.SourceScan{})

	var types []string
	var status []agentprotocol.LogStatus
	for f, ok := c.out.tryNext(); ok; f, ok = c.out.tryNext() {
		types = append(types, f.typ)
		fr, err := agentprotocol.Decode(f.data)
		require.NoError(t, err)
		require.Equal(t, f.typ, fr.Type, "the encoded envelope matches the queue type")
		require.Equal(t, agentprotocol.ProtocolVersion, fr.V)
		if f.typ == agentprotocol.TypeLogStatus {
			var s agentprotocol.LogStatus
			require.NoError(t, fr.Into(&s))
			status = append(status, s)
		}
	}
	require.Equal(t, []string{
		agentprotocol.TypeEvent, agentprotocol.TypeLog, agentprotocol.TypeLogEnd, agentprotocol.TypeStats,
		agentprotocol.TypeObserved, agentprotocol.TypeTraffic, agentprotocol.TypeProbes,
		agentprotocol.TypeLogStatus, agentprotocol.TypeSourceScan,
	}, types)
	require.Len(t, status, 1, "only the latest log status is kept")
	require.EqualValues(t, 2, status[0].TotalBytes)
}

func TestSendDropsFramesItCannotEncode(t *testing.T) {
	c := New(Config{})
	c.send(agentprotocol.TypeEvent, "", make(chan int))
	_, ok := c.out.tryNext()
	require.False(t, ok)

	c.connected.Store(true)
	require.False(t, c.Stream(agentprotocol.TypeExecOutput, "s", make(chan int)))
	_, ok = c.out.tryNext()
	require.False(t, ok)
}

func TestOutboxRequeuePutsFrameBackAtTheFront(t *testing.T) {
	o := newOutbox(10)
	o.push(outFrame{typ: agentprotocol.TypeResult, data: []byte("second")})
	o.requeue(outFrame{typ: agentprotocol.TypeResult, data: []byte("first")})
	a, _ := o.tryNext()
	b, _ := o.tryNext()
	require.Equal(t, "first", string(a.data))
	require.Equal(t, "second", string(b.data))
	select {
	case <-o.signal:
	default:
		t.Fatal("requeue must wake the writer")
	}
}

func TestOutboxDropStreamsKeepsOrdinaryFrames(t *testing.T) {
	o := newOutbox(10)
	o.push(outFrame{typ: agentprotocol.TypeResult, data: []byte("r")})
	o.push(outFrame{typ: agentprotocol.TypeExecOutput, data: []byte("x")})
	o.push(outFrame{typ: agentprotocol.TypeEvent, data: []byte("e")})
	o.dropStreams()
	var got []string
	for f, ok := o.tryNext(); ok; f, ok = o.tryNext() {
		got = append(got, string(f.data))
	}
	require.Equal(t, []string{"r", "e"}, got)
}

func TestJitterAddsAtMostAQuarter(t *testing.T) {
	for range 200 {
		d := jitter(time.Second)
		require.GreaterOrEqual(t, d, time.Second)
		require.LessOrEqual(t, d, 1250*time.Millisecond)
	}
}

func TestOpenTunnelEnforcesPerServiceLimit(t *testing.T) {
	c := connectedClient(t)
	limit := agentprotocol.TunnelServiceLimit(agentprotocol.TunnelLoki)
	for range limit {
		openStream(t, c, agentprotocol.TunnelLoki)
	}
	_, err := c.OpenTunnel(context.Background(), agentprotocol.TunnelLoki)
	require.ErrorIs(t, err, ErrTunnelLimit)
	// Another service still has room.
	s := openStream(t, c, agentprotocol.TunnelS3)
	require.NotNil(t, s)
}

func TestTunnelControlReportsNotConnected(t *testing.T) {
	c := New(Config{})
	require.ErrorIs(t, c.tunnelControl(agentprotocol.TypeTunnelClose, agentprotocol.TunnelClose{No: 1}), ErrNotConnected)
	c.connected.Store(true)
	require.NoError(t, c.tunnelControl(agentprotocol.TypeTunnelClose, agentprotocol.TunnelClose{No: 1}))
	f, ok := c.out.tryNext()
	require.True(t, ok)
	require.Equal(t, agentprotocol.TypeTunnelClose, f.typ)
}

// ---- Enroll ----

func enrollServer(t *testing.T, status int, body string, got *agentprotocol.EnrollRequest) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, got)
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("enroll request: %s content-type %q", r.Method, r.Header.Get("Content-Type"))
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestEnrollRegistersTheKeyAndPersistsTheIdentity(t *testing.T) {
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	var req agentprotocol.EnrollRequest
	url := enrollServer(t, 200, `{"agentId":"ag-1","serverId":"sv-1"}`, &req)

	id, err := Enroll(context.Background(), st, url, "tok-123", "host-a", agentprotocol.Facts{OS: "linux"}, nil)
	require.NoError(t, err)
	require.Equal(t, "ag-1", id.AgentID)
	require.Equal(t, "sv-1", id.ServerID)

	require.Equal(t, "tok-123", req.Token)
	require.Equal(t, "host-a", req.Hostname)
	require.Equal(t, "linux", req.Facts.OS)
	require.Equal(t, ed25519.PublicKeySize, len(req.PublicKey))
	require.True(t, id.PrivateKey.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(req.PublicKey)),
		"the control plane received the public half of the stored key")

	stored, ok, err := st.Identity()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, id.AgentID, stored.AgentID)
	require.True(t, id.PrivateKey.Equal(stored.PrivateKey))
}

func TestEnrollFailures(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		status  int
		body    string
		wantErr string
		wantIs  error
	}{
		{name: "no token", token: "", status: 200, wantErr: "no enrollment token provided"},
		{name: "token rejected with detail", token: "t", status: 401, body: `{"title":"Unauthorized","detail":"token expired"}`, wantIs: ErrEnrollRejected, wantErr: "token expired"},
		{name: "forbidden with title only", token: "t", status: 403, body: `{"title":"Forbidden"}`, wantIs: ErrEnrollRejected, wantErr: "Forbidden"},
		{name: "server error with raw body", token: "t", status: 502, body: "  bad gateway \n", wantErr: "HTTP 502: bad gateway"},
		{name: "unreadable success", token: "t", status: 200, body: "not json", wantErr: "decode enroll response"},
		{name: "missing ids", token: "t", status: 200, body: `{"agentId":"a"}`, wantErr: "missing agentId/serverId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			require.NoError(t, err)
			defer st.Close()
			url := enrollServer(t, tc.status, tc.body, nil)
			_, err = Enroll(context.Background(), st, url, tc.token, "h", agentprotocol.Facts{}, nil)
			require.ErrorContains(t, err, tc.wantErr)
			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			}
			_, ok, err := st.Identity()
			require.NoError(t, err)
			require.False(t, ok, "a failed enrollment stores no identity")
		})
	}
}

func TestEnrollTransportErrorsAreWrapped(t *testing.T) {
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	_, err = Enroll(context.Background(), st, "http://127.0.0.1:1/agent/v1/enroll", "t", "h", agentprotocol.Facts{}, &http.Client{Timeout: 5 * time.Second})
	require.ErrorContains(t, err, "enroll request")

	_, err = Enroll(context.Background(), st, "http://bad host/", "t", "h", agentprotocol.Facts{}, nil)
	require.Error(t, err)
}
