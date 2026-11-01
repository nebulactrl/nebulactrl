package localproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// upstream is a TCP server standing in for the control plane's end of a
// stream: the fake dialer connects to it.
type upstream struct {
	ln      net.Listener
	handler func(c net.Conn)
	dials   atomic.Int32
}

func newUpstream(t *testing.T, handler func(c net.Conn)) *upstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	u := &upstream{ln: ln, handler: handler}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); handler(c) }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return u
}

func (u *upstream) dialer() Dialer {
	return DialerFunc(func(ctx context.Context, service string) (net.Conn, error) {
		u.dials.Add(1)
		return net.Dial("tcp", u.ln.Addr().String())
	})
}

func anyPort(string) int { return 0 }

func newManager(t *testing.T, d Dialer) *Manager {
	t.Helper()
	m := New(Config{Dialer: d, Port: anyPort})
	t.Cleanup(m.Close)
	return m
}

func TestApplyListensOnLoopbackAndReportsStatus(t *testing.T) {
	m := newManager(t, nil)
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{
		{Service: agentprotocol.TunnelS3}, {Service: agentprotocol.TunnelLoki},
	}})
	for _, svc := range []string{agentprotocol.TunnelS3, agentprotocol.TunnelLoki} {
		host, _, err := net.SplitHostPort(m.Addr(svc))
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1", host, "never listens beyond loopback")
	}
	require.Empty(t, m.Addr(agentprotocol.TunnelRegistry))

	st := m.Status()
	require.Len(t, st, 2)
	require.Equal(t, agentprotocol.TunnelLoki, st[0].Service) // sorted by service
	require.Equal(t, agentprotocol.TunnelS3, st[1].Service)
	for _, s := range st {
		require.True(t, s.Listening)
		require.NotZero(t, s.Port)
		require.Empty(t, s.Error)
	}

	// Idempotent: the same set keeps the same sockets.
	before := m.Addr(agentprotocol.TunnelS3)
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{{Service: agentprotocol.TunnelS3}}})
	require.Equal(t, before, m.Addr(agentprotocol.TunnelS3))
	require.Empty(t, m.Addr(agentprotocol.TunnelLoki), "a service that is no longer offered stops listening")
	require.Len(t, m.Status(), 1)

	// An empty, non-nil set stops everything and reports nothing.
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{}})
	require.Empty(t, m.Addr(agentprotocol.TunnelS3))
	require.Empty(t, m.Status())
	_, err := net.DialTimeout("tcp", before, 500*time.Millisecond)
	require.Error(t, err, "the port is closed again")
}

func TestOnlyTheFixedPortsOfKnownServicesAreAccepted(t *testing.T) {
	m := New(Config{}) // default policy: the protocol's fixed ports
	t.Cleanup(m.Close)
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{
		{Service: agentprotocol.TunnelS3, Port: 22},             // not the S3 port
		{Service: agentprotocol.TunnelRegistry, Port: 80},       // not the registry port
		{Service: "grafana", Port: agentprotocol.LocalPortLoki}, // unknown service
	}})
	for _, s := range m.Status() {
		require.False(t, s.Listening, s.Service)
		require.NotEmpty(t, s.Error, s.Service)
	}
	require.Len(t, m.Status(), 3)
	require.Empty(t, m.Addr(agentprotocol.TunnelS3))
	require.Empty(t, m.Addr("grafana"))
}

func TestPortInUseIsReportedAndRetried(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := taken.Addr().(*net.TCPAddr).Port
	m := New(Config{Port: func(string) int { return port }, RetryEvery: 20 * time.Millisecond})
	t.Cleanup(m.Close)

	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{{Service: agentprotocol.TunnelS3, Port: port}}})
	st := m.Status()
	require.Len(t, st, 1)
	require.False(t, st[0].Listening)
	require.Contains(t, st[0].Error, "already in use")
	require.Equal(t, port, st[0].Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	require.NoError(t, taken.Close()) // the other process lets go
	require.Eventually(t, func() bool {
		st := m.Status()
		return len(st) == 1 && st[0].Listening && st[0].Error == ""
	}, 5*time.Second, 10*time.Millisecond, "the endpoint comes up once its port is free")
}

func TestPipeCarriesRequestAndResponseWithHalfClose(t *testing.T) {
	up := newUpstream(t, func(c net.Conn) {
		req, _ := io.ReadAll(c) // until the client's FIN
		_, _ = c.Write([]byte("echo:" + string(req)))
		_ = c.(*net.TCPConn).CloseWrite()
	})
	m := newManager(t, up.dialer())
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{{Service: agentprotocol.TunnelRegistry}}})

	c, err := net.Dial("tcp", m.Addr(agentprotocol.TunnelRegistry))
	require.NoError(t, err)
	defer c.Close()
	big := make([]byte, 1<<20)
	for i := range big {
		big[i] = byte(i)
	}
	go func() {
		_, _ = c.Write(big)
		_ = c.(*net.TCPConn).CloseWrite() // a client that finished its request and waits for the answer
	}()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(10*time.Second)))
	resp, err := io.ReadAll(c)
	require.NoError(t, err)
	require.Len(t, resp, len("echo:")+len(big))
	require.Equal(t, "echo:", string(resp[:5]))
	require.Equal(t, big, resp[5:])
	require.EqualValues(t, 1, up.dials.Load())
}

func TestRefusedStreamClosesTheLocalConnectionAtOnce(t *testing.T) {
	var asked atomic.Value
	d := DialerFunc(func(ctx context.Context, service string) (net.Conn, error) {
		asked.Store(service)
		return nil, errors.New("too many open instance service connections")
	})
	m := newManager(t, d)
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{{Service: agentprotocol.TunnelLoki}}})
	c, err := net.Dial("tcp", m.Addr(agentprotocol.TunnelLoki))
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = c.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.EOF, "the client sees a closed connection, not a hang")
	require.Equal(t, agentprotocol.TunnelLoki, asked.Load(), "the stream is opened for the service of the port")
}

func TestUpstreamAbortEndsTheLocalConnection(t *testing.T) {
	up := newUpstream(t, func(c net.Conn) {
		_, _ = c.Write([]byte("partial"))
		_ = c.(*net.TCPConn).SetLinger(0) // reset instead of a clean FIN
	})
	m := newManager(t, up.dialer())
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{{Service: agentprotocol.TunnelS3}}})
	c, err := net.Dial("tcp", m.Addr(agentprotocol.TunnelS3))
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadAll(c)
	if err != nil {
		var ne net.Error
		require.False(t, errors.As(err, &ne) && ne.Timeout(), "must end, not hang: %v", err)
	}
}

func TestCloseDropsLiveConnections(t *testing.T) {
	up := newUpstream(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	m := New(Config{Dialer: up.dialer(), Port: anyPort})
	m.Apply(agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{{Service: agentprotocol.TunnelS3}}})
	addr := m.Addr(agentprotocol.TunnelS3)
	c, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Write([]byte("hello"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return up.dials.Load() == 1 }, 2*time.Second, time.Millisecond)

	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a live connection")
	}
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = c.Read(make([]byte, 1))
	require.Error(t, err)
	_, err = net.DialTimeout("tcp", addr, 500*time.Millisecond)
	require.Error(t, err)
}
