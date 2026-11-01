package conn

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// connectedClient is a Client that believes it is connected, for tests that
// look at what its writer would send. The socket itself is not needed:
// nextWrite is the writer's whole decision.
func connectedClient(t *testing.T) *Client {
	t.Helper()
	c := New(Config{HeartbeatInterval: time.Hour})
	c.tun.Up()
	c.connected.Store(true)
	t.Cleanup(func() { c.tun.Down(nil) })
	return c
}

// openStream opens a tunnel stream, answering tunnel_opened the way the control plane does.
func openStream(t *testing.T, c *Client, service string) io.ReadWriteCloser {
	t.Helper()
	type res struct {
		s   io.ReadWriteCloser
		err error
	}
	got := make(chan res, 1)
	go func() {
		s, err := c.OpenTunnel(context.Background(), service)
		got <- res{s, err}
	}()
	var open agentprotocol.TunnelOpen
	require.Eventually(t, func() bool {
		f, ok := c.out.tryNext()
		if !ok || f.typ != agentprotocol.TypeTunnelOpen {
			return false
		}
		fr, err := agentprotocol.Decode(f.data)
		require.NoError(t, err)
		require.NoError(t, fr.Into(&open))
		return true
	}, 2*time.Second, time.Millisecond)
	require.Equal(t, service, open.Service)
	b, err := agentprotocol.Encode(agentprotocol.TypeTunnelOpened, "", agentprotocol.TunnelOpened{No: open.No})
	require.NoError(t, err)
	c.tun.HandleControl(agentprotocol.TypeTunnelOpened, b)
	r := <-got
	require.NoError(t, r.err)
	return r.s
}

func TestWriterServesControlFramesBeforeTunnelData(t *testing.T) {
	c := connectedClient(t)
	s := openStream(t, c, agentprotocol.TunnelS3)
	_, err := s.Write(make([]byte, 20*agentprotocol.TunnelChunk))
	require.NoError(t, err)

	// A heartbeat queued behind twenty chunks of bulk data goes out first ...
	c.send(agentprotocol.TypeHeartbeat, "", agentprotocol.Heartbeat{})
	mt, _, ctl, ok := c.nextWrite()
	require.True(t, ok)
	require.Equal(t, websocket.MessageText, mt)
	require.Equal(t, agentprotocol.TypeHeartbeat, ctl.typ)

	// ... and so does one that arrives while the data is already being written.
	for range 5 {
		mt, data, ctl, ok := c.nextWrite()
		require.True(t, ok)
		require.Equal(t, websocket.MessageBinary, mt)
		require.Nil(t, ctl)
		require.Len(t, data, agentprotocol.TunnelHeaderLen+agentprotocol.TunnelChunk)
	}
	c.send(agentprotocol.TypeResult, "cmd", agentprotocol.ResultFrame{CommandID: "cmd"})
	mt, _, ctl, ok = c.nextWrite()
	require.True(t, ok)
	require.Equal(t, websocket.MessageText, mt)
	require.Equal(t, agentprotocol.TypeResult, ctl.typ)

	// Data follows once control is idle; nothing is lost.
	n := 0
	for {
		mt, _, _, ok := c.nextWrite()
		if !ok {
			break
		}
		require.Equal(t, websocket.MessageBinary, mt)
		n++
	}
	require.Equal(t, 15, n)
}

func TestWriterAlternatesBetweenStreams(t *testing.T) {
	c := connectedClient(t)
	s1 := openStream(t, c, agentprotocol.TunnelS3)
	s2 := openStream(t, c, agentprotocol.TunnelRegistry)
	for _, s := range []io.Writer{s1, s2} {
		_, err := s.Write(make([]byte, 6*agentprotocol.TunnelChunk))
		require.NoError(t, err)
	}
	var order []uint32
	for {
		mt, data, _, ok := c.nextWrite()
		if !ok {
			break
		}
		require.Equal(t, websocket.MessageBinary, mt)
		f, err := agentprotocol.DecodeTunnelFrame(data)
		require.NoError(t, err)
		order = append(order, f.No)
	}
	require.Len(t, order, 12)
	for i := 1; i < len(order); i++ {
		require.NotEqual(t, order[i-1], order[i], "a deep queue must not starve the other stream: %v", order)
	}
}

func TestOpenTunnelNeedsAConnection(t *testing.T) {
	c := New(Config{})
	_, err := c.OpenTunnel(context.Background(), agentprotocol.TunnelS3)
	require.ErrorIs(t, err, ErrNotConnected)
	_, err = c.OpenTunnel(context.Background(), "grafana")
	require.Error(t, err)
}

func TestConnectionLossFailsOpenStreamsAndDropsTheirFrames(t *testing.T) {
	c := connectedClient(t)
	s := openStream(t, c, agentprotocol.TunnelS3)
	_, err := s.Write(make([]byte, 3*agentprotocol.TunnelChunk))
	require.NoError(t, err)
	c.send(agentprotocol.TypeHeartbeat, "", agentprotocol.Heartbeat{})
	c.Stream(agentprotocol.TypeTunnelAck, "", agentprotocol.TunnelAck{No: 1, Bytes: 10})

	// What session() does when the socket ends.
	c.connected.Store(false)
	c.out.dropStreams()
	c.tun.Down(ErrNotConnected)

	_, err = s.Write([]byte("more"))
	require.ErrorIs(t, err, ErrNotConnected)
	_, err = s.Read(make([]byte, 1))
	require.ErrorIs(t, err, ErrNotConnected)
	_, _, _, ok := c.nextWrite()
	require.True(t, ok, "the heartbeat is an ordinary frame and survives for the next connection")
	_, _, _, ok = c.nextWrite()
	require.False(t, ok, "tunnel frames of the dead connection never reach the next one")
	_, err = c.OpenTunnel(context.Background(), agentprotocol.TunnelS3)
	require.ErrorIs(t, err, ErrNotConnected)
}
