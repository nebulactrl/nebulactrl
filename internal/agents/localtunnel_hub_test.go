package agents_test

import (
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// These tests use the real handshake and database-backed dispatch: the tunnel
// capability comes from the authenticated hello and the server from the
// enrolled identity.

type recordingAcceptor struct {
	peers chan agents.TunnelPeer
}

func (r *recordingAcceptor) AcceptTunnel(_ context.Context, p agents.TunnelPeer, c net.Conn) error {
	r.peers <- p
	n, err := io.Copy(io.Discard, c)
	if err != nil {
		return err
	}
	_, err = c.Write([]byte(strings.Repeat("x", int(n))))
	return err
}

func signFor(e enrollment, ch agentprotocol.Challenge) []byte {
	return ed25519.Sign(e.priv, agentprotocol.SigningPayload(ch.Nonce, e.agentID.String(), ch.ServerTime))
}

// dialCaps is harness.dial with capabilities in the hello.
func (h *harness) dialCaps(e enrollment, caps ...string) *agentConn {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/agent/v1/connect", nil)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { ws.CloseNow() })
	ws.SetReadLimit(16 << 20)
	ac := &agentConn{t: h.t, ws: ws}
	_, data, err := ws.Read(ctx)
	require.NoError(h.t, err)
	f, err := agentprotocol.Decode(data)
	require.NoError(h.t, err)
	var ch agentprotocol.Challenge
	require.NoError(h.t, f.Into(&ch))
	ac.write(agentprotocol.TypeAuth, agentprotocol.Auth{AgentID: e.agentID.String(), Signature: signFor(e, ch)})
	ac.write(agentprotocol.TypeHello, agentprotocol.Hello{
		AgentVersion: "test", ProtocolVersion: agentprotocol.ProtocolVersion,
		Facts: agentprotocol.Facts{OS: "linux", Capabilities: caps},
	})
	return ac
}

func (a *agentConn) writeBinary(b []byte) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(a.t, a.ws.Write(ctx, websocket.MessageBinary, b))
}

// nextAny reads until a text frame of typ or a binary frame arrives.
func (a *agentConn) nextBinary() agentprotocol.TunnelFrame {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		mt, data, err := a.ws.Read(ctx)
		require.NoError(a.t, err)
		if mt == websocket.MessageBinary {
			f, err := agentprotocol.DecodeTunnelFrame(data)
			require.NoError(a.t, err)
			return f
		}
	}
}

func openOn(t *testing.T, a *agentConn, no uint32, service string) agentprotocol.TunnelOpened {
	t.Helper()
	a.write(agentprotocol.TypeTunnelOpen, agentprotocol.TunnelOpen{No: no, Service: service})
	var o agentprotocol.TunnelOpened
	require.NoError(t, a.next(agentprotocol.TypeTunnelOpened).Into(&o))
	return o
}

func TestTunnelOverTheRealHandshake(t *testing.T) {
	h := newHarness(t)
	acc := &recordingAcceptor{peers: make(chan agents.TunnelPeer, 4)}
	h.hub.SetTunnelAcceptor(acc)

	e1, e2 := h.enroll("edge-1"), h.enroll("edge-2")
	a1 := h.dialCaps(e1, agentprotocol.CapInstanceTunnel)
	a2 := h.dialCaps(e2, agentprotocol.CapInstanceTunnel)
	require.Eventually(t, func() bool { return h.hub.TunnelCapable(e1.serverID) && h.hub.TunnelCapable(e2.serverID) }, 5*time.Second, 10*time.Millisecond)

	// Each stream is attributed to the server whose connection carried it.
	for i, tc := range []struct {
		a *agentConn
		e enrollment
	}{{a1, e1}, {a2, e2}} {
		no := uint32(i + 1)
		require.Empty(t, openOn(t, tc.a, no, agentprotocol.TunnelS3).Code)
		tc.a.writeBinary(agentprotocol.EncodeTunnelFrame(no, 0, []byte("12345")))
		tc.a.writeBinary(agentprotocol.EncodeTunnelFrame(no, agentprotocol.TunnelFlagFIN, nil))
		select {
		case p := <-acc.peers:
			require.Equal(t, tc.e.serverID, p.ServerID)
			require.Equal(t, tc.e.agentID, p.AgentID)
			require.Equal(t, agentprotocol.TunnelS3, p.Service)
		case <-time.After(5 * time.Second):
			t.Fatal("acceptor was not called")
		}
		f := tc.a.nextBinary()
		require.Equal(t, no, f.No)
		require.Equal(t, "xxxxx", string(f.Payload))
	}
}

func TestTunnelNeedsTheCapabilityFromTheHello(t *testing.T) {
	h := newHarness(t)
	h.hub.SetTunnelAcceptor(&recordingAcceptor{peers: make(chan agents.TunnelPeer, 1)})
	e := h.enroll("old-edge")
	old := h.dialCaps(e) // an agent that predates the feature
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
	require.False(t, h.hub.TunnelCapable(e.serverID))
	o := openOn(t, old, 1, agentprotocol.TunnelS3)
	require.Equal(t, agentprotocol.TunnelCodeUnsupported, o.Code)
	// The old agent's connection works as before.
	old.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{Proxy: agentprotocol.ProxyHealth{Status: "running"}})
	require.Empty(t, h.hub.TunnelEndpoints(e.serverID))
}

func TestHeartbeatEndpointsAreKeptForTheServerPage(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dialCaps(e, agentprotocol.CapInstanceTunnel)
	require.Eventually(t, func() bool { return h.hub.TunnelCapable(e.serverID) }, 5*time.Second, 10*time.Millisecond)
	want := []agentprotocol.EndpointStatus{
		{Service: agentprotocol.TunnelS3, Port: 18333, Listening: true},
		{Service: agentprotocol.TunnelLoki, Port: 18100, Error: "port 18100 is already in use on this server"},
	}
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{Proxy: agentprotocol.ProxyHealth{Status: "running"}, Endpoints: want})
	require.Eventually(t, func() bool {
		got := h.hub.TunnelEndpoints(e.serverID)
		return len(got) == 2 && got[0] == want[0] && got[1] == want[1]
	}, 5*time.Second, 10*time.Millisecond)
	require.Empty(t, h.hub.TunnelEndpoints(uuid.New()), "an unknown or offline server has none")
}
