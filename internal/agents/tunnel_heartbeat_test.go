package agents_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// tunnelBeat is a connected agent plus a way to know that the heartbeats sent
// so far were processed: the reader handles frames one at a time and every
// heartbeat stores one metric sample.
type tunnelBeat struct {
	h    *harness
	e    enrollment
	a    *agentConn
	sent int
}

func (h *harness) tunnelBeat(name string, withRow bool) *tunnelBeat {
	h.t.Helper()
	e := h.enroll(name)
	if withRow {
		_, err := h.q.InsertServerTunnel(h.ctx, db.InsertServerTunnelParams{
			ServerID: e.serverID, WorkspaceID: h.ws, AccountID: "acct", TunnelID: uuid.NewString(), TunnelName: "nebula-" + name,
		})
		require.NoError(h.t, err)
	}
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	return &tunnelBeat{h: h, e: e, a: a}
}

func (b *tunnelBeat) beat(ts *agentprotocol.TunnelStatus) {
	b.a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK}, Tunnel: ts})
	b.sent++
}

// flush returns once every heartbeat sent before it has been fully handled: it
// sends one more, whose tunnel state the hub ignores, and waits for its sample
// (stored before the tunnel is looked at, after the earlier heartbeats finished).
func (b *tunnelBeat) flush(t *testing.T) {
	t.Helper()
	b.beat(&agentprotocol.TunnelStatus{State: "not-a-state"})
	require.Eventually(t, func() bool {
		var n int
		_ = b.h.pool.QueryRow(b.h.ctx, "SELECT count(*) FROM metric_samples WHERE server_id = $1", b.e.serverID).Scan(&n)
		return n >= b.sent
	}, 10*time.Second, 10*time.Millisecond)
}

func (b *tunnelBeat) row(t *testing.T) db.ServerTunnel {
	t.Helper()
	r, err := b.h.q.GetServerTunnel(b.h.ctx, b.e.serverID)
	require.NoError(t, err)
	return r
}

type activations chan uuid.UUID

func (a activations) TunnelActivated(_ context.Context, serverID uuid.UUID) { a <- serverID }

func TestTunnelHeartbeatRecordsTheConnectorAndActivatesTheTunnelExactlyOnce(t *testing.T) {
	h := newHarness(t)
	obs := make(activations, 8)
	h.hub.SetTunnelObserver(obs)
	b := h.tunnelBeat("edge-1", true)
	require.Equal(t, "provisioning", b.row(t).State)

	b.beat(&agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOnline, Connections: 2, Image: "cloudflare/cloudflared:2025.1.0"})
	require.Eventually(t, func() bool { return b.row(t).State == "online" }, 10*time.Second, 20*time.Millisecond)
	r := b.row(t)
	require.EqualValues(t, 2, r.Connections)
	require.Equal(t, "cloudflare/cloudflared:2025.1.0", r.Image)
	require.NotNil(t, r.ActivatedAt, "the first online report activates the tunnel")
	require.NotNil(t, r.ObservedAt)
	select {
	case got := <-obs:
		require.Equal(t, b.e.serverID, got)
	case <-time.After(10 * time.Second):
		t.Fatal("the observer was not told about the activation")
	}
	activatedAt := *r.ActivatedAt

	// The connector drops: the state, the message and a clamped connection count are recorded.
	b.beat(&agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOffline, Connections: -3, Message: "container stopped"})
	require.Eventually(t, func() bool { return b.row(t).State == "offline" }, 10*time.Second, 20*time.Millisecond)
	r = b.row(t)
	require.Equal(t, "container stopped", r.Message)
	require.Zero(t, r.Connections)
	require.Equal(t, "cloudflare/cloudflared:2025.1.0", r.Image, "an empty image keeps the one on record")

	// It comes back: the tunnel was activated already, nobody is told again.
	b.beat(&agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOnline, Connections: 4})
	require.Eventually(t, func() bool { return b.row(t).State == "online" }, 10*time.Second, 20*time.Millisecond)
	b.flush(t)
	require.True(t, b.row(t).ActivatedAt.Equal(activatedAt), "the activation time is the first one")
	select {
	case got := <-obs:
		t.Fatalf("a second activation was announced for %s", got)
	default:
	}

	// Needs-attention and provisioning are accepted states too.
	for _, st := range []string{agentprotocol.TunnelStateNeedsAttn, agentprotocol.TunnelStateProvisioning} {
		b.beat(&agentprotocol.TunnelStatus{State: st, Message: "state " + st})
		require.Eventually(t, func() bool { return b.row(t).State == st }, 10*time.Second, 20*time.Millisecond)
	}
}

func TestTunnelHeartbeatWithoutAConnectorMarksTheTunnelOfflineOnlyWhenItWasMissed(t *testing.T) {
	const missing = "The cloudflared container is not running on this server."
	h := newHarness(t)
	b := h.tunnelBeat("edge-1", true)

	// A fresh tunnel has no container yet: not news.
	b.beat(nil)
	b.flush(t)
	r := b.row(t)
	require.Equal(t, "provisioning", r.State)
	require.Nil(t, r.ObservedAt)

	// Three minutes without a container is.
	_, err := h.pool.Exec(h.ctx, "UPDATE server_tunnels SET created_at = now() - interval '4 minutes' WHERE server_id = $1", b.e.serverID)
	require.NoError(t, err)
	b.beat(nil)
	require.Eventually(t, func() bool { return b.row(t).State == "offline" }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, missing, b.row(t).Message)

	// A connector that was seen before and vanishes is recorded at once.
	_, err = h.pool.Exec(h.ctx, "UPDATE server_tunnels SET state = 'online', message = '', connections = 2, observed_at = now(), created_at = now() WHERE server_id = $1", b.e.serverID)
	require.NoError(t, err)
	b.beat(nil)
	require.Eventually(t, func() bool { return b.row(t).State == "offline" }, 10*time.Second, 20*time.Millisecond)
	r = b.row(t)
	require.Equal(t, missing, r.Message)
	require.Zero(t, r.Connections)
}

func TestTunnelHeartbeatsOfServersWithoutATunnelRowOrWithAnUnknownStateChangeNothing(t *testing.T) {
	h := newHarness(t)

	// No tunnel row: any report is dropped and the connection carries on.
	none := h.tunnelBeat("no-tunnel", false)
	none.beat(&agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOnline, Connections: 1})
	none.beat(nil)
	none.flush(t)
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM server_tunnels").Scan(&n))
	require.Zero(t, n)
	require.True(t, h.hub.Online(none.e.serverID))

	// A tunnel row exists: states the control plane does not accept are ignored
	// ("removed" only ever travels in the result of remove_tunnel).
	obs := make(activations, 1)
	h.hub.SetTunnelObserver(obs)
	b := h.tunnelBeat("with-tunnel", true)
	b.beat(&agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateRemoved, Connections: 9, Message: "gone"})
	b.beat(&agentprotocol.TunnelStatus{State: "bogus", Connections: 9})
	b.flush(t)
	r := b.row(t)
	require.Equal(t, "provisioning", r.State)
	require.Zero(t, r.Connections)
	require.Empty(t, r.Message)
	require.Nil(t, r.ObservedAt)
	require.Nil(t, r.ActivatedAt)

	// A tunnel being removed is not revived by heartbeats.
	_, err := h.q.MarkServerTunnelRemoving(h.ctx, b.e.serverID)
	require.NoError(t, err)
	b.beat(&agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateOnline, Connections: 3})
	b.flush(t)
	require.Equal(t, "removing", b.row(t).State)
	select {
	case got := <-obs:
		t.Fatalf("a tunnel being removed was activated for %s", got)
	default:
	}
}
