package agents_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// rawSocket is a connection that has read the challenge and nothing more: the
// test writes whatever bytes it wants as the agent's answer.
type rawSocket struct {
	t         *testing.T
	ws        *websocket.Conn
	challenge agentprotocol.Challenge
}

func (h *harness) rawSocket() *rawSocket {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/agent/v1/connect", nil)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { ws.CloseNow() })
	_, data, err := ws.Read(ctx)
	require.NoError(h.t, err)
	f, err := agentprotocol.Decode(data)
	require.NoError(h.t, err)
	require.Equal(h.t, agentprotocol.TypeChallenge, f.Type)
	s := &rawSocket{t: h.t, ws: ws}
	require.NoError(h.t, f.Into(&s.challenge))
	return s
}

func (s *rawSocket) send(raw string) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(s.t, s.ws.Write(ctx, websocket.MessageText, []byte(raw)))
}

func (s *rawSocket) sendFrame(typ string, body any) {
	s.t.Helper()
	data, err := agentprotocol.Encode(typ, "", body)
	require.NoError(s.t, err)
	s.send(string(data))
}

func (s *rawSocket) auth(e enrollment) {
	s.t.Helper()
	s.sendFrame(agentprotocol.TypeAuth, agentprotocol.Auth{AgentID: e.agentID.String(), Signature: signFor(e, s.challenge)})
}

// closed reads until the hub closes the socket and returns its close code and reason.
func (s *rawSocket) closed() (websocket.StatusCode, string) {
	s.t.Helper()
	return closeInfo(s.t, s.ws)
}

func closeInfo(t *testing.T, ws *websocket.Conn) (websocket.StatusCode, string) {
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
		return ce.Code, ce.Reason
	}
}

func TestHandshakeClosesTheSocketOnEveryBadAuthAnswer(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name   string
		answer string
		code   int
		reason string
	}{
		{"not JSON", `this is not a frame`, agentprotocol.CloseAuthFailed, "malformed frame"},
		{"another protocol major", `{"v":99,"type":"auth"}`, agentprotocol.CloseUnsupportedVersion, "unsupported protocol version"},
		{"a hello where the auth belongs", `{"v":1,"type":"hello"}`, agentprotocol.CloseAuthFailed, "expected auth"},
		{"an auth body of the wrong shape", `{"v":1,"type":"auth","agentId":42}`, agentprotocol.CloseAuthFailed, "expected auth"},
		{"an agent id that is no uuid", `{"v":1,"type":"auth","agentId":"nope","signature":"AAAA"}`, agentprotocol.CloseUnknownAgent, "unknown agent"},
		{"an agent that never enrolled", `{"v":1,"type":"auth","agentId":"` + uuid.NewString() + `","signature":"AAAA"}`, agentprotocol.CloseUnknownAgent, "unknown agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.t = t
			s := h.rawSocket()
			s.send(tc.answer)
			code, reason := s.closed()
			require.EqualValues(t, tc.code, code)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestHandshakeRefusesAChallengeAnsweredAfterItExpired(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	var skew atomic.Int64
	h.hub.SetClockForTest(func() time.Time { return time.Now().Add(time.Duration(skew.Load())) })

	late := h.rawSocket()
	skew.Store(int64(agentprotocol.NonceMaxAge + time.Second)) // the agent took too long to answer
	late.auth(e)
	code, reason := late.closed()
	require.EqualValues(t, agentprotocol.CloseAuthFailed, code)
	require.Equal(t, "challenge expired", reason)
	require.False(t, h.hub.Online(e.serverID))

	// A prompt answer on the same clock is fine.
	prompt := h.rawSocket()
	prompt.auth(e)
	prompt.sendFrame(agentprotocol.TypeHello, agentprotocol.Hello{AgentVersion: "test", ProtocolVersion: agentprotocol.ProtocolVersion})
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
}

func TestHandshakeChecksTheStoredIdentityAndServer(t *testing.T) {
	h := newHarness(t)

	badKey := h.enroll("bad-key")
	_, err := h.pool.Exec(h.ctx, "UPDATE agent_identities SET public_key = '\\x00' WHERE server_id = $1", badKey.serverID)
	require.NoError(t, err)
	s := h.rawSocket()
	s.auth(badKey)
	code, reason := s.closed()
	require.EqualValues(t, agentprotocol.CloseAuthFailed, code)
	require.Equal(t, "bad signature", reason, "a stored key of the wrong size can never verify")

	revokedAgent := h.enroll("revoked-agent")
	_, err = h.pool.Exec(h.ctx, "UPDATE agent_identities SET revoked_at = now() WHERE server_id = $1", revokedAgent.serverID)
	require.NoError(t, err)
	s = h.rawSocket()
	s.auth(revokedAgent)
	code, reason = s.closed()
	require.EqualValues(t, agentprotocol.CloseRevoked, code)
	require.Equal(t, "agent revoked", reason)

	revokedServer := h.enroll("revoked-server")
	_, err = h.pool.Exec(h.ctx, "UPDATE servers SET status = 'revoked' WHERE id = $1", revokedServer.serverID)
	require.NoError(t, err)
	s = h.rawSocket()
	s.auth(revokedServer)
	code, reason = s.closed()
	require.EqualValues(t, agentprotocol.CloseRevoked, code)
	require.Equal(t, "agent revoked", reason)

	for _, e := range []enrollment{badKey, revokedAgent, revokedServer} {
		require.False(t, h.hub.Online(e.serverID))
	}
}

func TestHandshakeNeedsAWellFormedHelloRightAfterAuth(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	cases := []struct {
		name   string
		hello  string
		code   int
		reason string
	}{
		{"not JSON", `hello?`, agentprotocol.CloseAuthFailed, "expected hello"},
		{"a heartbeat instead", `{"v":1,"type":"heartbeat"}`, agentprotocol.CloseAuthFailed, "expected hello"},
		{"another frame major", `{"v":2,"type":"hello","protocolVersion":1}`, agentprotocol.CloseUnsupportedVersion, "unsupported protocol version"},
		{"another protocol version in the body", `{"v":1,"type":"hello","protocolVersion":2}`, agentprotocol.CloseUnsupportedVersion, "unsupported protocol version"},
		{"facts of the wrong shape", `{"v":1,"type":"hello","protocolVersion":1,"facts":"linux"}`, agentprotocol.CloseAuthFailed, "malformed hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.t = t
			s := h.rawSocket()
			s.auth(e)
			s.send(tc.hello)
			code, reason := s.closed()
			require.EqualValues(t, tc.code, code)
			require.Equal(t, tc.reason, reason)
			require.False(t, h.hub.Online(e.serverID))
		})
	}
}

func TestAnAgentThatLeavesTheHandshakeUnansweredDoesNotDisturbTheHub(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	quiet := h.rawSocket()
	require.NoError(t, quiet.ws.Close(websocket.StatusNormalClosure, "never mind"))

	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
}

func TestAConnectedAgentSurvivesGarbageAndUnknownFramesButNotAProtocolMajorChange(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, a.ws.Write(ctx, websocket.MessageText, []byte(`{{{ not a frame`)))
	require.NoError(t, a.ws.Write(ctx, websocket.MessageText, []byte(`{"v":1,"type":"mystery"}`)))
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{
		DockerVersion: "29.1.0", Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyError, Error: "after the garbage"},
	})
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.Proxy.Error == "after the garbage"
	}, 5*time.Second, 20*time.Millisecond, "the heartbeat behind the garbage frames was processed")

	require.NoError(t, a.ws.Write(ctx, websocket.MessageText, []byte(`{"v":2,"type":"heartbeat"}`)))
	code, reason := closeInfo(t, a.ws)
	require.EqualValues(t, agentprotocol.CloseUnsupportedVersion, code)
	require.Equal(t, "unsupported protocol version", reason)
	require.Eventually(t, func() bool { return !h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
}

func TestDisconnectClosesWithTheGivenCodeAndAReasonCutToWhatTheProtocolAllows(t *testing.T) {
	h := newHarness(t)
	h.hub.Disconnect(uuid.New(), 4000, "nobody is connected") // no connection: nothing happens

	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)

	h.hub.Disconnect(e.serverID, 4000, strings.Repeat("why ", 100))
	code, reason := closeInfo(t, a.ws)
	require.EqualValues(t, 4000, code)
	require.Len(t, reason, 120)
	require.Eventually(t, func() bool { return !h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
}

func TestTheServerGoesOfflineForRequestsAndSubscriptionsWhenTheAgentLeaves(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)

	ch, _, err := h.hub.SubscribeLogs(context.Background(), e.serverID, agentprotocol.LogsSubscribe{ServiceID: uuid.NewString()})
	require.NoError(t, err)
	a.next(agentprotocol.TypeCommand)
	type answer struct {
		res agentprotocol.ResultFrame
		err error
	}
	got := make(chan answer, 1)
	go func() {
		res, err := h.hub.Request(context.Background(), e.serverID, agentprotocol.KindLogsSubscribe, map[string]string{}, nil, time.Minute)
		got <- answer{res, err}
	}()
	a.next(agentprotocol.TypeCommand) // the request reached the agent

	_ = a.ws.Close(websocket.StatusNormalClosure, "bye")
	select {
	case ans := <-got:
		require.ErrorIs(t, ans.err, agents.ErrRequestLost)
	case <-time.After(10 * time.Second):
		t.Fatal("the request outlived the connection")
	}
	select {
	case _, open := <-ch:
		require.False(t, open, "the log stream ended with the connection")
	case <-time.After(10 * time.Second):
		t.Fatal("the log stream outlived the connection")
	}
	require.Eventually(t, func() bool { return !h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
}
