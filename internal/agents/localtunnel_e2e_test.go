package agents

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/internal/agent/localproxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// These tests run the real agent client and listener manager against the real
// hub connection code over a loopback WebSocket. Only the database-backed
// parts of the hub are replaced: the handshake accepts any identity and
// heartbeats are measured instead of persisted.

// e2e is a control plane for one agent.
type e2e struct {
	t        *testing.T
	h        *Hub
	srv      *httptest.Server
	serverID uuid.UUID
	team     uuid.UUID

	// endpoints go into the hello reconcile (nil: like an older control plane).
	endpoints *agentprotocol.InstanceEndpoints
	// onFrame sees frames the hub does not handle itself.
	mu        sync.Mutex
	hbLatency []time.Duration
	hbEndpts  [][]agentprotocol.EndpointStatus
	hello     chan agentprotocol.Hello
	conn      chan *agentConn
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	e := &e2e{t: t, h: NewHub(nil, nil, nil, nil, nil), serverID: uuid.New(), team: uuid.New(), hello: make(chan agentprotocol.Hello, 8), conn: make(chan *agentConn, 8)}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(func() { e.h.Close(); e.srv.Close() })
	return e
}

func (e *e2e) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	b, _ := agentprotocol.Encode(agentprotocol.TypeChallenge, "", agentprotocol.Challenge{Nonce: nonce, ServerTime: time.Now().UnixNano()})
	if ws.Write(ctx, websocket.MessageText, b) != nil {
		return
	}
	if _, _, err = ws.Read(ctx); err != nil { // auth: any identity is fine here
		return
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		return
	}
	f, _ := agentprotocol.Decode(data)
	var hello agentprotocol.Hello
	_ = f.Into(&hello)

	c := newAgentConn(e.h, ws, e.serverID, uuid.New())
	c.teamID = e.team
	c.tunnelOK = slices.Contains(hello.Facts.Capabilities, agentprotocol.CapInstanceTunnel)
	e.h.register(c)
	defer e.h.unregister(c)
	c.mux.Up()
	go c.writeLoop()
	e.hello <- hello
	e.conn <- c
	_ = c.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{Hello: true, Endpoints: e.endpoints})

	for {
		mt, data, err := ws.Read(ctx)
		if err != nil {
			break
		}
		if mt == websocket.MessageBinary {
			e.h.tunnelData(c, data)
			continue
		}
		f, err := agentprotocol.Decode(data)
		if err != nil {
			continue
		}
		switch f.Type {
		case agentprotocol.TypeHeartbeat:
			var hb agentprotocol.Heartbeat
			_ = f.Into(&hb)
			if ns, err := strconv.ParseInt(hb.Proxy.Error, 10, 64); err == nil {
				e.mu.Lock()
				e.hbLatency = append(e.hbLatency, time.Since(time.Unix(0, ns)))
				e.hbEndpts = append(e.hbEndpts, hb.Endpoints)
				e.mu.Unlock()
			}
		case agentprotocol.TypeTunnelOpen:
			var o agentprotocol.TunnelOpen
			if f.Into(&o) == nil {
				e.h.onTunnelOpen(c, o)
			}
		case agentprotocol.TypeTunnelAck, agentprotocol.TypeTunnelClose:
			e.h.tunnelControl(c, f)
		}
	}
	c.closeWith(int(websocket.StatusNormalClosure), "")
}

func (e *e2e) maxHeartbeatLatency() (time.Duration, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var m time.Duration
	for _, d := range e.hbLatency {
		m = max(m, d)
	}
	return m, len(e.hbLatency)
}

// e2eAgent is a real client with a real listener manager on random ports.
type e2eAgent struct {
	c      *conn.Client
	lp     *localproxy.Manager
	cancel context.CancelFunc
	done   chan error

	mu         sync.Mutex
	reconciles int
	latency    []time.Duration
}

type agentOptions struct {
	caps      []string
	heartbeat time.Duration
}

func (e *e2e) startAgent(o agentOptions) *e2eAgent {
	e.t.Helper()
	st, err := store.Open(e.t.TempDir())
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = st.Close() })
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(e.t, err)
	if o.heartbeat == 0 {
		o.heartbeat = 20 * time.Millisecond
	}
	a := &e2eAgent{done: make(chan error, 1)}
	a.lp = localproxy.New(localproxy.Config{
		Dialer: localproxy.DialerFunc(func(ctx context.Context, service string) (net.Conn, error) { return a.c.OpenTunnel(ctx, service) }),
		Port:   func(string) int { return 0 }, // random free ports: tests must not depend on 18333
	})
	a.c = conn.New(conn.Config{
		URL:      "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/agent/v1/connect",
		Identity: store.Identity{AgentID: uuid.NewString(), ServerID: e.serverID.String(), PrivateKey: priv},
		Store:    st, Version: "test",
		Facts: func(context.Context) agentprotocol.Facts { return agentprotocol.Facts{Capabilities: o.caps} },
		Heartbeat: func(context.Context) agentprotocol.Heartbeat {
			return agentprotocol.Heartbeat{
				Proxy:     agentprotocol.ProxyHealth{Status: "running", Error: strconv.FormatInt(time.Now().UnixNano(), 10)},
				Endpoints: a.lp.Status(),
			}
		},
		HeartbeatInterval: o.heartbeat,
	})
	a.c.SetReconciler(reconcilerFunc(func(_ context.Context, d agentprotocol.Reconcile) error {
		if ns, err := strconv.ParseInt(d.ACMEEmail, 10, 64); err == nil {
			a.mu.Lock()
			a.latency = append(a.latency, time.Since(time.Unix(0, ns)))
			a.mu.Unlock()
		}
		if d.Endpoints != nil {
			a.lp.Apply(*d.Endpoints)
		}
		a.mu.Lock()
		a.reconciles++
		a.mu.Unlock()
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() { a.done <- a.c.Run(ctx) }()
	e.t.Cleanup(func() { cancel(); <-a.done; a.lp.Close() })
	return a
}

type reconcilerFunc func(context.Context, agentprotocol.Reconcile) error

func (f reconcilerFunc) Reconcile(ctx context.Context, d agentprotocol.Reconcile) error {
	return f(ctx, d)
}

func endpointsOf(services ...string) *agentprotocol.InstanceEndpoints {
	eps := &agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{}}
	for _, s := range services {
		eps.Listen = append(eps.Listen, agentprotocol.Endpoint{Service: s, Port: 0})
	}
	return eps
}

var tunnelCaps = []string{agentprotocol.CapInstanceTunnel}

func (e *e2e) waitListening(a *e2eAgent, service string) string {
	e.t.Helper()
	require.Eventually(e.t, func() bool { return a.lp.Addr(service) != "" }, 5*time.Second, 5*time.Millisecond, "%s listener", service)
	return a.lp.Addr(service)
}

func TestTunnelCarriesTrafficEndToEnd(t *testing.T) {
	e := newE2E(t)
	e.endpoints = endpointsOf(agentprotocol.TunnelS3)
	var peer atomic.Pointer[TunnelPeer]
	e.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, p TunnelPeer, c net.Conn) error {
		peer.Store(&p)
		// An S3-like exchange: read the whole request, answer, close.
		n, err := io.Copy(io.Discard, c)
		if err != nil {
			return err
		}
		_, err = c.Write([]byte("received " + strconv.FormatInt(n, 10)))
		return err
	}))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	addr := e.waitListening(a, agentprotocol.TunnelS3)
	require.Contains(t, addr, "127.0.0.1:", "listeners are loopback only")

	c, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer c.Close()
	payload := make([]byte, 3<<20+17) // more than a stream window
	_, _ = rand.Read(payload)
	go func() {
		_, _ = c.Write(payload)
		_ = c.(*net.TCPConn).CloseWrite()
	}()
	resp, err := io.ReadAll(c)
	require.NoError(t, err)
	require.Equal(t, "received "+strconv.Itoa(len(payload)), string(resp))
	require.Equal(t, e.serverID, peer.Load().ServerID, "the hub names the server from the connection")
	require.Equal(t, agentprotocol.TunnelS3, peer.Load().Service)
}

func TestHeartbeatLatencyStaysLowWhileFourStreamsPushBulkData(t *testing.T) {
	e := newE2E(t)
	e.endpoints = endpointsOf(agentprotocol.TunnelS3)
	var got atomic.Int64
	e.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, _ TunnelPeer, c net.Conn) error {
		n, err := io.Copy(io.Discard, c)
		got.Add(n)
		return err
	}))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	addr := e.waitListening(a, agentprotocol.TunnelS3)
	require.Eventually(t, func() bool { _, n := e.maxHeartbeatLatency(); return n >= 3 }, 5*time.Second, 5*time.Millisecond, "heartbeats flow before the load")

	const streams, each = 4, 32 << 20
	start := time.Now()
	var wg sync.WaitGroup
	for range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", addr)
			if !assertNoError(t, err) {
				return
			}
			defer c.Close()
			buf := make([]byte, 256<<10)
			for sent := 0; sent < each; sent += len(buf) {
				if _, err := c.Write(buf); !assertNoError(t, err) {
					return
				}
			}
			_ = c.(*net.TCPConn).CloseWrite()
			_, _ = io.Copy(io.Discard, c) // until the stream is closed
		}()
	}
	// Heartbeats are measured only while the bulk transfer is running.
	e.mu.Lock()
	first := len(e.hbLatency)
	e.mu.Unlock()
	wg.Wait()
	elapsed := time.Since(start)
	require.EqualValues(t, streams*each, got.Load(), "all bulk data arrived")

	e.mu.Lock()
	during := slices.Clone(e.hbLatency[first:])
	e.mu.Unlock()
	require.GreaterOrEqual(t, len(during), 5, "heartbeats kept arriving during the %s transfer", elapsed)
	var worst time.Duration
	for _, d := range during {
		worst = max(worst, d)
	}
	require.Less(t, worst, time.Second, "heartbeat latency under bulk load (%d heartbeats, transfer %s)", len(during), elapsed)
}

func TestControlFramesFromTheHubStayFastWhileFourStreamsDownload(t *testing.T) {
	e := newE2E(t)
	e.endpoints = endpointsOf(agentprotocol.TunnelRegistry)
	const streams, each = 4, 32 << 20
	e.h.SetTunnelAcceptor(funcAcceptor(func(_ context.Context, _ TunnelPeer, c net.Conn) error {
		buf := make([]byte, 256<<10)
		for sent := 0; sent < each; sent += len(buf) {
			if _, err := c.Write(buf); err != nil {
				return err
			}
		}
		return nil
	}))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	addr := e.waitListening(a, agentprotocol.TunnelRegistry)
	hubConn := <-e.conn

	stop := make(chan struct{})
	var sender sync.WaitGroup
	sender.Add(1)
	go func() { // the hub keeps sending small control frames, as a busy control plane does
		defer sender.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond): // often enough for several samples while the transfer runs
				_ = hubConn.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{ACMEEmail: strconv.FormatInt(time.Now().UnixNano(), 10)})
			}
		}
	}()

	var wg sync.WaitGroup
	var total atomic.Int64
	for range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", addr)
			if !assertNoError(t, err) {
				return
			}
			defer c.Close()
			n, _ := io.Copy(io.Discard, c)
			total.Add(n)
		}()
	}
	wg.Wait()
	close(stop)
	sender.Wait()
	require.EqualValues(t, streams*each, total.Load())

	a.mu.Lock()
	lat := slices.Clone(a.latency)
	a.mu.Unlock()
	require.GreaterOrEqual(t, len(lat), 5)
	var worst time.Duration
	for _, d := range lat {
		worst = max(worst, d)
	}
	require.Less(t, worst, time.Second, "control frame latency under download load (%d frames)", len(lat))
}

func assertNoError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return false
	}
	return true
}

func TestHeartbeatReportsEndpointStatus(t *testing.T) {
	e := newE2E(t)
	e.endpoints = endpointsOf(agentprotocol.TunnelS3, agentprotocol.TunnelLoki)
	e.h.SetTunnelAcceptor(funcAcceptor(idle))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	e.waitListening(a, agentprotocol.TunnelS3)
	e.waitListening(a, agentprotocol.TunnelLoki)
	require.Eventually(t, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.hbEndpts) == 0 {
			return false
		}
		last := e.hbEndpts[len(e.hbEndpts)-1]
		return len(last) == 2 && last[0].Service == agentprotocol.TunnelLoki && last[0].Listening && last[1].Service == agentprotocol.TunnelS3 && last[1].Listening
	}, 5*time.Second, 10*time.Millisecond)
}

// An agent connected to a control plane that never offers endpoints (an older
// one) opens no listener, reports none and never sends a tunnel frame.
func TestNoEndpointsOfferedMeansNoTunnel(t *testing.T) {
	e := newE2E(t)
	e.endpoints = nil
	var accepted atomic.Int32
	e.h.SetTunnelAcceptor(funcAcceptor(func(context.Context, TunnelPeer, net.Conn) error { accepted.Add(1); return nil }))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	require.Eventually(t, func() bool { _, n := e.maxHeartbeatLatency(); return n >= 3 }, 5*time.Second, 5*time.Millisecond)
	for _, svc := range agentprotocol.TunnelServices {
		require.Empty(t, a.lp.Addr(svc))
	}
	require.Empty(t, a.lp.Status())
	e.mu.Lock()
	for _, eps := range e.hbEndpts {
		require.Empty(t, eps)
	}
	e.mu.Unlock()
	require.Zero(t, accepted.Load())
}

// An old agent: no capability in its hello. The hub refuses its streams and
// reports the peer as not capable.
func TestOldAgentWithoutCapabilityGetsNoTunnel(t *testing.T) {
	e := newE2E(t)
	e.h.SetTunnelAcceptor(funcAcceptor(idle))
	a := e.startAgent(agentOptions{caps: nil})
	hello := <-e.hello
	require.NotContains(t, hello.Facts.Capabilities, agentprotocol.CapInstanceTunnel)
	require.False(t, e.h.TunnelCapable(e.serverID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.Eventually(t, a.c.Connected, 5*time.Second, 5*time.Millisecond)
	_, err := a.c.OpenTunnel(ctx, agentprotocol.TunnelS3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not advertise")
}

func TestAgentSideStreamLimitFailsFast(t *testing.T) {
	e := newE2E(t)
	e.h.SetTunnelAcceptor(funcAcceptor(idle))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	require.Eventually(t, a.c.Connected, 5*time.Second, 5*time.Millisecond)
	e.h.tun.limits = tunnelLimits{rate: 1000}
	ctx := context.Background()
	for range agentprotocol.TunnelServiceLimit(agentprotocol.TunnelLoki) {
		c, err := a.c.OpenTunnel(ctx, agentprotocol.TunnelLoki)
		require.NoError(t, err)
		defer c.Close()
	}
	_, err := a.c.OpenTunnel(ctx, agentprotocol.TunnelLoki)
	require.ErrorIs(t, err, conn.ErrTunnelLimit)
	_, err = a.c.OpenTunnel(ctx, "grafana")
	require.Error(t, err)
}

func TestLocalConnectionsDropWhenTheLinkDrops(t *testing.T) {
	e := newE2E(t)
	e.endpoints = endpointsOf(agentprotocol.TunnelS3)
	blocked := make(chan struct{}, 1)
	e.h.SetTunnelAcceptor(funcAcceptor(func(ctx context.Context, _ TunnelPeer, c net.Conn) error {
		blocked <- struct{}{}
		<-ctx.Done()
		return nil
	}))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	addr := e.waitListening(a, agentprotocol.TunnelS3)
	c, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\n"))
	<-blocked

	hubConn := <-e.conn
	hubConn.closeWith(int(websocket.StatusGoingAway), "control plane restarting")

	// The agent's local client sees the connection end instead of hanging.
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = c.Read(make([]byte, 1))
	require.Error(t, err)
	var ne net.Error
	if errors.As(err, &ne) {
		require.False(t, ne.Timeout(), "the local connection must be closed, not left hanging")
	}
	// After the reconnect the agent offers the endpoint again and carries traffic.
	require.Eventually(t, func() bool {
		select {
		case <-e.conn:
			return true
		default:
			return false
		}
	}, 15*time.Second, 20*time.Millisecond, "the agent reconnects")
	require.Eventually(t, func() bool {
		c2, err := net.Dial("tcp", a.lp.Addr(agentprotocol.TunnelS3))
		if err != nil {
			return false
		}
		defer c2.Close()
		select {
		case <-blocked:
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}, 10*time.Second, 50*time.Millisecond)
}

func TestHelloReconcileIsRepeatedWithEndpointsAfterAReconnect(t *testing.T) {
	// Listeners die with the agent process; the control plane resends them on
	// every hello, which is what makes a restarted agent serve again.
	e := newE2E(t)
	e.endpoints = endpointsOf(agentprotocol.TunnelRegistry)
	e.h.SetTunnelAcceptor(funcAcceptor(idle))
	a := e.startAgent(agentOptions{caps: tunnelCaps})
	e.waitListening(a, agentprotocol.TunnelRegistry)
	// An explicit empty set stops everything (the control plane removed the service).
	e.endpoints = endpointsOf()
	hubConn := <-e.conn
	require.NoError(t, hubConn.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{Endpoints: e.endpoints}))
	require.Eventually(t, func() bool { return a.lp.Addr(agentprotocol.TunnelRegistry) == "" }, 5*time.Second, 10*time.Millisecond)
	// A reconcile without the field leaves things unchanged.
	e.endpoints = endpointsOf(agentprotocol.TunnelRegistry)
	require.NoError(t, hubConn.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{Endpoints: e.endpoints}))
	e.waitListening(a, agentprotocol.TunnelRegistry)
	require.NoError(t, hubConn.sendFrame(agentprotocol.TypeReconcile, "", agentprotocol.Reconcile{}))
	time.Sleep(100 * time.Millisecond)
	require.NotEmpty(t, a.lp.Addr(agentprotocol.TunnelRegistry))
}
