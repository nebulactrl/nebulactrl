package conn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestSignChallengeVerifiesAndRejectsTamperedNonce(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	ch := agentprotocol.Challenge{Nonce: []byte("0123456789abcdef0123456789abcdef"), ServerTime: time.Now().UnixNano()}
	const agentID = "agent-1"
	sig := SignChallenge(priv, agentID, ch)

	require.True(t, ed25519.Verify(pub, agentprotocol.SigningPayload(ch.Nonce, agentID, ch.ServerTime), sig))

	tampered := append([]byte(nil), ch.Nonce...)
	tampered[0] ^= 0xff
	require.False(t, ed25519.Verify(pub, agentprotocol.SigningPayload(tampered, agentID, ch.ServerTime), sig), "tampered nonce must fail")
	require.False(t, ed25519.Verify(pub, agentprotocol.SigningPayload(ch.Nonce, "agent-2", ch.ServerTime), sig), "different agent id must fail")
	require.False(t, ed25519.Verify(pub, agentprotocol.SigningPayload(ch.Nonce, agentID, ch.ServerTime+1), sig), "different server time must fail")
}

func TestParseEndpoints(t *testing.T) {
	cases := []struct {
		in       string
		insecure bool
		enroll   string
		connect  string
		wantErr  bool
	}{
		{in: "https://cp.example.com", enroll: "https://cp.example.com/agent/v1/enroll", connect: "wss://cp.example.com/agent/v1/connect"},
		{in: "wss://cp.example.com/", enroll: "https://cp.example.com/agent/v1/enroll", connect: "wss://cp.example.com/agent/v1/connect"},
		{in: "https://cp.example.com/nebula", enroll: "https://cp.example.com/nebula/agent/v1/enroll", connect: "wss://cp.example.com/nebula/agent/v1/connect"},
		{in: "http://host.docker.internal:8080", insecure: true, enroll: "http://host.docker.internal:8080/agent/v1/enroll", connect: "ws://host.docker.internal:8080/agent/v1/connect"},
		{in: "ws://localhost:8080", insecure: true, enroll: "http://localhost:8080/agent/v1/enroll", connect: "ws://localhost:8080/agent/v1/connect"},
		{in: "http://127.0.0.1:8080", enroll: "http://127.0.0.1:8080/agent/v1/enroll", connect: "ws://127.0.0.1:8080/agent/v1/connect"},
		{in: "http://[::1]:8000", enroll: "http://[::1]:8000/agent/v1/enroll", connect: "ws://[::1]:8000/agent/v1/connect"},
		{in: "http://localhost:8080", enroll: "http://localhost:8080/agent/v1/enroll", connect: "ws://localhost:8080/agent/v1/connect"},
		{in: "http://127.0.0.1.example.com", wantErr: true},
		{in: "http://cp.example.com", wantErr: true},
		{in: "ws://cp.example.com", wantErr: true},
		{in: "ftp://cp.example.com", wantErr: true},
		{in: "", wantErr: true},
		{in: "https://", wantErr: true},
	}
	for _, tc := range cases {
		ep, err := ParseEndpoints(tc.in, tc.insecure)
		if tc.wantErr {
			require.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.enroll, ep.Enroll, tc.in)
		require.Equal(t, tc.connect, ep.Connect, tc.in)
	}
}

func TestOutboxLatestOnlyAndDropOldest(t *testing.T) {
	o := newOutbox(2)
	drain := func() []string {
		var got []string
		for {
			f, ok := o.tryNext()
			if !ok {
				return got
			}
			got = append(got, string(f.data))
		}
	}
	o.push(outFrame{typ: agentprotocol.TypeStats, data: []byte("s1")})
	o.push(outFrame{typ: agentprotocol.TypeStats, data: []byte("s2")})
	require.Equal(t, []string{"s2"}, drain(), "stats frames replace each other")

	o.push(outFrame{typ: agentprotocol.TypeResult, data: []byte("r")})
	for _, l := range []string{"l1", "l2", "l3"} {
		o.push(outFrame{typ: agentprotocol.TypeLog, data: []byte(l)})
	}
	// max 2 droppable frames; the result is never dropped.
	got := drain()
	require.Contains(t, got, "r")
	require.NotContains(t, got, "l1")
	require.Contains(t, got, "l3")
}

// fakeServer is a minimal control plane: challenge → verify auth → read hello,
// then runs script with the live socket.
type fakeServer struct {
	t      *testing.T
	pub    ed25519.PublicKey
	agent  string
	script func(ctx context.Context, ws *websocket.Conn, hello agentprotocol.Hello)
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()

	ch := agentprotocol.Challenge{Nonce: make([]byte, 32), ServerTime: time.Now().UnixNano()}
	_, _ = rand.Read(ch.Nonce)
	b, _ := agentprotocol.Encode(agentprotocol.TypeChallenge, "", ch)
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		return
	}

	_, data, err := ws.Read(ctx)
	if err != nil {
		return
	}
	f, _ := agentprotocol.Decode(data)
	var auth agentprotocol.Auth
	_ = f.Into(&auth)
	if f.Type != agentprotocol.TypeAuth || auth.AgentID != s.agent ||
		!ed25519.Verify(s.pub, agentprotocol.SigningPayload(ch.Nonce, auth.AgentID, ch.ServerTime), auth.Signature) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseAuthFailed), "bad signature")
		return
	}

	_, data, err = ws.Read(ctx)
	if err != nil {
		return
	}
	f, _ = agentprotocol.Decode(data)
	var hello agentprotocol.Hello
	_ = f.Into(&hello)
	s.script(ctx, ws, hello)
}

func newTestClient(t *testing.T, srvURL string) (*Client, *store.Store, *fakeServer, store.Identity) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := store.Identity{AgentID: "agent-1", ServerID: "server-1", PrivateKey: priv}
	srv := &fakeServer{t: t, pub: pub, agent: id.AgentID}

	c := New(Config{
		URL:               "ws" + strings.TrimPrefix(srvURL, "http") + "/agent/v1/connect",
		Identity:          id,
		Store:             st,
		Version:           "test",
		HeartbeatInterval: time.Hour,
	})
	return c, st, srv, id
}

func TestRevokedCloseCodeEndsRun(t *testing.T) {
	var srv *fakeServer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	defer ts.Close()
	c, _, s, _ := newTestClient(t, ts.URL)
	srv = s
	srv.script = func(_ context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseRevoked), "revoked")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.ErrorIs(t, c.Run(ctx), ErrRevoked)
}

func TestCommandFlowAckResultDedupeAndKindCheck(t *testing.T) {
	var srv *fakeServer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	defer ts.Close()
	c, st, s, _ := newTestClient(t, ts.URL)
	srv = s

	runs := make(chan string, 4)
	c.SetHandler(HandlerFunc(func(_ context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
		runs <- cmd.ID
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{"ok":true}`)}
	}))

	got := make(chan agentprotocol.Frame, 16)
	done := make(chan struct{})
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		defer close(done)
		send := func(cmd agentprotocol.Command) {
			b, err := agentprotocol.Encode(agentprotocol.TypeCommand, cmd.ID, cmd)
			require.NoError(t, err)
			require.NoError(t, ws.Write(ctx, websocket.MessageText, b))
		}
		send(agentprotocol.Command{ID: "c1", Kind: agentprotocol.KindRestartService, Payload: json.RawMessage(`{}`)})
		send(agentprotocol.Command{ID: "c2", Kind: "format_disk", Payload: json.RawMessage(`{}`)})
		// Redelivery of c1 must replay the stored result without running again.
		time.Sleep(300 * time.Millisecond)
		send(agentprotocol.Command{ID: "c1", Kind: agentprotocol.KindRestartService, Payload: json.RawMessage(`{}`)})
		for {
			rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, data, err := ws.Read(rctx)
			cancel()
			if err != nil {
				return
			}
			f, _ := agentprotocol.Decode(data)
			got <- f
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	<-done
	cancel()
	close(got)

	var acks []string
	results := map[string][]agentprotocol.ResultFrame{}
	for f := range got {
		switch f.Type {
		case agentprotocol.TypeAck:
			var a agentprotocol.Ack
			require.NoError(t, f.Into(&a))
			acks = append(acks, a.CommandID)
		case agentprotocol.TypeResult:
			var r agentprotocol.ResultFrame
			require.NoError(t, f.Into(&r))
			results[r.CommandID] = append(results[r.CommandID], r)
		}
	}

	require.ElementsMatch(t, []string{"c1", "c2", "c1"}, acks, "every delivery is acked")
	require.Len(t, results["c1"], 2, "original and replayed result")
	require.Equal(t, agentprotocol.OutcomeSucceeded, results["c1"][1].Outcome)
	require.Len(t, results["c2"], 1)
	require.Equal(t, agentprotocol.OutcomeFailed, results["c2"][0].Outcome)
	require.Equal(t, "command kind not allowed", results["c2"][0].Error)
	require.Len(t, runs, 1, "handler ran once for c1 and never for the disallowed kind")

	stored, seen := st.SeenCommand("c1")
	require.True(t, seen)
	require.NotNil(t, stored)
}

// A stopping agent must not go on changing the host after Run returned: the
// integration harness removed Traefik once its agent's Run had returned, and a
// route command still running re-created it. Run waits for its handlers, which
// see the canceled context and unwind.
func TestRunReturnsOnlyAfterRunningHandlersFinish(t *testing.T) {
	var srv *fakeServer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	defer ts.Close()
	c, _, s, _ := newTestClient(t, ts.URL)
	srv = s

	started := make(chan struct{})
	var finished atomic.Bool
	c.SetHandler(HandlerFunc(func(ctx context.Context, _ agentprotocol.Command) agentprotocol.ResultFrame {
		close(started)
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // work that outlives the cancel, like a proxy Apply
		finished.Store(true)
		return agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded}
	}))
	srv.script = func(ctx context.Context, ws *websocket.Conn, _ agentprotocol.Hello) {
		b, err := agentprotocol.Encode(agentprotocol.TypeCommand, "c1", agentprotocol.Command{ID: "c1", Kind: agentprotocol.KindSyncRoutes, Payload: json.RawMessage(`{}`)})
		require.NoError(t, err)
		require.NoError(t, ws.Write(ctx, websocket.MessageText, b))
		<-ctx.Done()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- c.Run(ctx) }()
	<-started
	cancel()
	require.ErrorIs(t, <-ran, context.Canceled)
	require.True(t, finished.Load(), "Run returned while a command handler was still running")
}
