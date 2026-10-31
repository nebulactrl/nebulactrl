package agents

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/clientip"
	"github.com/nebulactrl/nebulactrl/internal/tunnelmux"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	handshakeTimeout = 10 * time.Second
	writeTimeout     = 15 * time.Second
	// idleTimeout closes a connection that stayed silent for several heartbeat
	// intervals (the agent heartbeats every 10s).
	idleTimeout = 90 * time.Second
	readLimit   = 8 << 20
	// preAuthReadLimit bounds a frame until the agent's signature verified: the
	// challenge response is a few hundred bytes, so an unauthenticated peer
	// cannot make the control plane buffer megabytes.
	preAuthReadLimit = 64 << 10
	// connectsPerMinute is how many handshakes one source address may start per
	// minute (a fleet behind one NAT reconnecting after a restart fits).
	connectsPerMinute = 120
	outQueue          = 512
)

var errConnClosed = errors.New("agent connection closed")

// agentConn is one authenticated agent WebSocket. A single writer goroutine
// drains out; every other goroutine enqueues frames with sendFrame.
type agentConn struct {
	h        *Hub
	ws       *websocket.Conn
	serverID uuid.UUID
	agentID  uuid.UUID
	log      *slog.Logger

	out       chan []byte
	done      chan struct{}
	closeOnce sync.Once

	kick       chan struct{}
	resendSent atomic.Bool

	// Instance service tunnel (localtunnel.go). tunnelOK is the agent's
	// CapInstanceTunnel, teamID the server's team (limit counters only; the
	// forwarder resolves the team itself).
	mux       *tunnelmux.Mux
	tunnelOK  bool
	teamID    uuid.UUID
	ownerID   uuid.UUID // who created the team (zero when unknown); see tunnelOwner
	opens     openLimiter
	endpoints atomic.Pointer[[]agentprotocol.EndpointStatus]
	// geo is the GeoIP database the agent last reported (nil while geolocation is off).
	geo atomic.Pointer[agentprotocol.GeoStatus]

	// reader-goroutine state
	deps map[uuid.UUID]*depLog
}

func newAgentConn(h *Hub, ws *websocket.Conn, serverID, agentID uuid.UUID) *agentConn {
	c := &agentConn{
		h: h, ws: ws, serverID: serverID, agentID: agentID,
		log:  h.log.With("server_id", serverID, "agent_id", agentID),
		out:  make(chan []byte, outQueue),
		done: make(chan struct{}),
		kick: make(chan struct{}, 1),
		deps: map[uuid.UUID]*depLog{},
	}
	c.mux = c.newTunnelMux()
	return c
}

// closeWith closes the connection with a WebSocket close code. It is safe to
// call from any goroutine and more than once.
func (c *agentConn) closeWith(code int, reason string) {
	c.closeOnce.Do(func() {
		close(c.done)
		if len(reason) > 120 {
			reason = reason[:120]
		}
		go func() { _ = c.ws.Close(websocket.StatusCode(code), reason) }()
	})
}

func (c *agentConn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// sendFrame queues a frame for the writer. A connection whose queue stays full
// is closed so it can reconnect and resync.
func (c *agentConn) sendFrame(typ, id string, body any) error {
	data, err := agentprotocol.Encode(typ, id, body)
	if err != nil {
		return err
	}
	select {
	case c.out <- data:
		return nil
	case <-c.done:
		return errConnClosed
	default:
	}
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case c.out <- data:
		return nil
	case <-c.done:
		return errConnClosed
	case <-t.C:
		c.closeWith(int(websocket.StatusInternalError), "send queue stalled")
		return errors.New("agent send queue stalled")
	}
}

// write sends one WebSocket message; a failed write closes the connection.
func (c *agentConn) write(mt websocket.MessageType, data []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	err := c.ws.Write(ctx, mt, data)
	cancel()
	if err != nil {
		c.closeWith(int(websocket.StatusInternalError), "write failed")
		return false
	}
	return true
}

// nextWrite picks the next message without waiting. Control frames (out) always
// go before tunnel data: a data chunk is at most TunnelChunk bytes, so a
// heartbeat reply or command waits for at most one of them, however much bulk
// data the tunnel streams have queued.
func (c *agentConn) nextWrite() (websocket.MessageType, []byte, bool) {
	select {
	case data := <-c.out:
		return websocket.MessageText, data, true
	default:
	}
	if data, ok := c.mux.Next(); ok {
		return websocket.MessageBinary, data, true
	}
	return 0, nil, false
}

// writeLoop is the connection's only writer.
func (c *agentConn) writeLoop() {
	for {
		if mt, data, ok := c.nextWrite(); ok {
			if !c.write(mt, data) {
				return
			}
			continue
		}
		select {
		case <-c.done:
			return
		case data := <-c.out:
			if !c.write(websocket.MessageText, data) {
				return
			}
		case <-c.mux.Ready():
		}
	}
}

// kickDelivery wakes the delivery goroutine. resendSent also re-sends commands
// that were written to an earlier connection but never acknowledged.
func (c *agentConn) kickDelivery(resendSent bool) {
	if resendSent {
		c.resendSent.Store(true)
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *agentConn) deliveryLoop(ctx context.Context) {
	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case <-c.kick:
		}
		c.h.deliver(ctx, c, c.resendSent.Swap(false))
	}
}

// handleConnect upgrades /agent/v1/connect and serves the agent session.
func (h *Hub) handleConnect(w http.ResponseWriter, r *http.Request) {
	remote := clientip.String(r)
	if remote == "" {
		remote = r.RemoteAddr
	}
	if !h.connectLimit.Allow(remote) {
		w.Header().Set("Retry-After", "60")
		problem(w, apierr.TooManyRequests("too many connection attempts; retry later"))
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.log.Debug("websocket accept failed", "err", err)
		return
	}
	ws.SetReadLimit(preAuthReadLimit)
	ctx, cancel := context.WithCancel(h.rootCtx)
	defer cancel()

	c, hello, err := h.authenticate(ctx, ws)
	if err != nil {
		h.log.Info("agent handshake rejected", "remote", r.RemoteAddr, "err", err)
		return
	}
	h.startLoop()
	h.register(c)
	defer h.unregister(c)
	c.mux.Up()
	go c.writeLoop()
	go c.deliveryLoop(ctx)

	c.log.InfoContext(ctx, "agent connected", "agent_version", hello.AgentVersion, "remote", r.RemoteAddr)
	h.onHello(ctx, c, hello)
	c.readLoop(ctx)
	c.closeWith(int(websocket.StatusNormalClosure), "")
	c.log.InfoContext(context.Background(), "agent disconnected")
}

// reject closes ws with a protocol close code before registration.
func reject(ws *websocket.Conn, code int, reason string) error {
	_ = ws.Close(websocket.StatusCode(code), reason)
	return fmt.Errorf("close %d: %s", code, reason)
}

// authenticate runs the challenge/response handshake and reads hello.
func (h *Hub) authenticate(ctx context.Context, ws *websocket.Conn) (*agentConn, agentprotocol.Hello, error) {
	var hello agentprotocol.Hello
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, hello, reject(ws, int(websocket.StatusInternalError), "internal error")
	}
	serverTime := h.now().UnixNano()
	data, err := agentprotocol.Encode(agentprotocol.TypeChallenge, "", agentprotocol.Challenge{Nonce: nonce, ServerTime: serverTime})
	if err != nil {
		return nil, hello, reject(ws, int(websocket.StatusInternalError), "internal error")
	}
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := ws.Write(hctx, websocket.MessageText, data); err != nil {
		return nil, hello, fmt.Errorf("write challenge: %w", err)
	}

	_, data, err = ws.Read(hctx)
	if err != nil {
		return nil, hello, fmt.Errorf("read auth: %w", err)
	}
	f, err := agentprotocol.Decode(data)
	if err != nil {
		return nil, hello, reject(ws, int(agentprotocol.CloseAuthFailed), "malformed frame")
	}
	if f.V != agentprotocol.ProtocolVersion {
		return nil, hello, reject(ws, agentprotocol.CloseUnsupportedVersion, "unsupported protocol version")
	}
	var au agentprotocol.Auth
	if f.Type != agentprotocol.TypeAuth || f.Into(&au) != nil {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "expected auth")
	}
	if h.now().Sub(time.Unix(0, serverTime)) > agentprotocol.NonceMaxAge {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "challenge expired")
	}
	agentID, err := uuid.Parse(au.AgentID)
	if err != nil {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "unknown agent")
	}
	ident, err := h.q.GetAgentIdentity(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "unknown agent")
	}
	if err != nil {
		h.log.Error("load agent identity", "err", err)
		return nil, hello, reject(ws, int(websocket.StatusInternalError), "internal error")
	}
	if len(ident.PublicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ident.PublicKey, agentprotocol.SigningPayload(nonce, au.AgentID, serverTime), au.Signature) {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "bad signature")
	}
	if ident.RevokedAt != nil {
		return nil, hello, reject(ws, agentprotocol.CloseRevoked, "agent revoked")
	}
	srv, err := h.q.GetServerByID(ctx, ident.ServerID)
	if err != nil {
		h.log.Error("load server for agent", "err", err)
		return nil, hello, reject(ws, int(websocket.StatusInternalError), "internal error")
	}
	if srv.Status == "revoked" {
		return nil, hello, reject(ws, agentprotocol.CloseRevoked, "agent revoked")
	}

	// The signature verified: this is the agent that enrolled, so it may send
	// its hello (and later every frame) up to the session's read limit.
	ws.SetReadLimit(readLimit)
	_, data, err = ws.Read(hctx)
	if err != nil {
		return nil, hello, fmt.Errorf("read hello: %w", err)
	}
	f, err = agentprotocol.Decode(data)
	if err != nil || f.Type != agentprotocol.TypeHello {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "expected hello")
	}
	if f.V != agentprotocol.ProtocolVersion {
		return nil, hello, reject(ws, agentprotocol.CloseUnsupportedVersion, "unsupported protocol version")
	}
	if err := f.Into(&hello); err != nil {
		return nil, hello, reject(ws, agentprotocol.CloseAuthFailed, "malformed hello")
	}
	if hello.ProtocolVersion != agentprotocol.ProtocolVersion {
		return nil, hello, reject(ws, agentprotocol.CloseUnsupportedVersion, "unsupported protocol version")
	}
	c := newAgentConn(h, ws, ident.ServerID, agentID)
	c.teamID = srv.WorkspaceID
	if team, err := h.q.GetWorkspace(ctx, srv.WorkspaceID); err == nil && team.CreatedBy != nil {
		c.ownerID = *team.CreatedBy
	} else if err != nil {
		h.log.Warn("load team owner for tunnel limits", "team_id", srv.WorkspaceID, "err", err)
	}
	c.tunnelOK = slices.Contains(hello.Facts.Capabilities, agentprotocol.CapInstanceTunnel)
	return c, hello, nil
}

// register makes c the server's only connection, closing a previous one.
func (h *Hub) register(c *agentConn) {
	h.mu.Lock()
	old := h.conns[c.serverID]
	h.conns[c.serverID] = c
	h.mu.Unlock()
	if old != nil {
		old.log.Info("agent connection replaced by a newer one")
		old.closeWith(agentprotocol.CloseReplaced, "replaced by a newer connection")
	}
}

func (h *Hub) unregister(c *agentConn) {
	h.mu.Lock()
	if h.conns[c.serverID] == c {
		delete(h.conns, c.serverID)
	}
	h.mu.Unlock()
	h.closeConnSubs(c)
	h.closeConnRequests(c)
	h.closeStreams(c)
	h.closeTunnels(c)
}

func (c *agentConn) readLoop(ctx context.Context) {
	for {
		rctx, cancel := context.WithTimeout(ctx, idleTimeout)
		mt, data, err := c.ws.Read(rctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil && !c.closed() {
				c.log.Info("agent read ended", "err", err)
			}
			return
		}
		if mt == websocket.MessageBinary {
			c.h.tunnelData(c, data)
			continue
		}
		f, err := agentprotocol.Decode(data)
		if err != nil {
			c.log.Warn("undecodable frame from agent", "err", err)
			continue
		}
		if f.V != agentprotocol.ProtocolVersion {
			c.closeWith(agentprotocol.CloseUnsupportedVersion, "unsupported protocol version")
			return
		}
		c.h.dispatch(ctx, c, f)
	}
}
