package agents

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/tunnelmux"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Instance service tunnels (pkg/agentprotocol/localtunnel.go). An agent opens a
// stream per local TCP connection it accepts on 127.0.0.1; the hub applies the
// limits, tells the acceptor which server (never anything from the frame
// beyond the service name) the stream belongs to and hands it a net.Conn. The
// acceptor (internal/instsvc/forward) maps the server to its team and talks to
// the service. State dies with the connection, like every other stream.

// TunnelPeer identifies the far end of a tunnel stream. ServerID and AgentID
// come from the authenticated handshake of the connection.
type TunnelPeer struct {
	ServerID, AgentID uuid.UUID
	Service           string
}

// TunnelAcceptor serves one tunnel stream. AcceptTunnel blocks for the life of
// the stream and returns when it is done; the hub closes c afterwards. ctx ends
// when the stream ends (connection lost, abort, idle, stalled). A non-nil error
// aborts the stream and its text (truncated) is shown to the agent.
type TunnelAcceptor interface {
	AcceptTunnel(ctx context.Context, p TunnelPeer, c net.Conn) error
}

// tunnelLimits are the open-time limits; zero fields take the protocol defaults.
type tunnelLimits struct {
	perAgent int
	perTeam  int
	perOwner int
	global   int
	rate     int // opens per second per agent
}

func (l tunnelLimits) filled() tunnelLimits {
	if l.perAgent <= 0 {
		l.perAgent = agentprotocol.MaxTunnelStreams
	}
	if l.perTeam <= 0 {
		l.perTeam = agentprotocol.MaxTunnelStreamsPerTeam
	}
	if l.perOwner <= 0 {
		l.perOwner = agentprotocol.MaxTunnelStreamsPerOwner
	}
	if l.global <= 0 {
		l.global = agentprotocol.MaxTunnelStreamsGlobal
	}
	if l.rate <= 0 {
		l.rate = agentprotocol.MaxTunnelOpensPerSecond
	}
	return l
}

// tunnelState is the hub's tunnel bookkeeping.
type tunnelState struct {
	mu       sync.Mutex
	acceptor TunnelAcceptor
	perTeam  map[uuid.UUID]int
	perOwner map[uuid.UUID]int
	global   int

	// limits and mux override the defaults (tests).
	limits tunnelLimits
	mux    tunnelmux.Config
}

// SetTunnelAcceptor installs the service side of instance tunnels. Without one
// every tunnel_open is refused with TunnelCodeUnavailable.
func (h *Hub) SetTunnelAcceptor(a TunnelAcceptor) {
	h.tun.mu.Lock()
	defer h.tun.mu.Unlock()
	h.tun.acceptor = a
}

func (h *Hub) tunnelAcceptor() TunnelAcceptor {
	h.tun.mu.Lock()
	defer h.tun.mu.Unlock()
	return h.tun.acceptor
}

// tunnelOwner is the key tunnel streams are shared by: the person who created
// the team, or the team itself when that is unknown.
func (c *agentConn) tunnelOwner() uuid.UUID {
	if c.ownerID != uuid.Nil {
		return c.ownerID
	}
	return c.teamID
}

// reserve takes one team, one owner and one global slot, or reports false at a
// limit. Teams are free to create, so the pool is shared by owner (the person
// who created the team) rather than by team, and once the pool is more than
// three quarters used an owner who already holds their fair share of it (the
// pool divided by the owners with streams) is refused: nobody can use up the
// control plane's streams and cut every other team off from S3, the registry
// and Loki.
func (t *tunnelState) reserve(team, owner uuid.UUID, l tunnelLimits) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.global >= l.global || t.perTeam[team] >= l.perTeam || t.perOwner[owner] >= l.perOwner {
		return false
	}
	if t.global*4 >= l.global*3 {
		owners := len(t.perOwner)
		if t.perOwner[owner] == 0 {
			owners++
		}
		if t.perOwner[owner] >= max(1, l.global/owners) {
			return false
		}
	}
	if t.perTeam == nil {
		t.perTeam = map[uuid.UUID]int{}
	}
	if t.perOwner == nil {
		t.perOwner = map[uuid.UUID]int{}
	}
	t.perTeam[team]++
	t.perOwner[owner]++
	t.global++
	return true
}

func (t *tunnelState) release(team, owner uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.global--
	if t.perTeam[team]--; t.perTeam[team] <= 0 {
		delete(t.perTeam, team)
	}
	if t.perOwner[owner]--; t.perOwner[owner] <= 0 {
		delete(t.perOwner, owner)
	}
}

// openLimiter is a token bucket for tunnel_open frames of one connection.
type openLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (o *openLimiter) allow(now time.Time, rate int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	burst := float64(rate)
	if o.last.IsZero() {
		o.tokens = burst
	} else {
		o.tokens = min(burst, o.tokens+now.Sub(o.last).Seconds()*burst)
	}
	o.last = now
	if o.tokens < 1 {
		return false
	}
	o.tokens--
	return true
}

// newTunnelMux builds the connection's mux. It is brought up by handleConnect.
func (c *agentConn) newTunnelMux() *tunnelmux.Mux {
	cfg := c.h.tun.mux
	cfg.Log = c.log
	cfg.Control = func(typ string, body any) error { return c.sendFrame(typ, "", body) }
	return tunnelmux.New(cfg)
}

// onTunnelOpen handles tunnel_open on the connection's reader goroutine.
func (h *Hub) onTunnelOpen(c *agentConn, o agentprotocol.TunnelOpen) {
	refuse := func(code, msg string) {
		_ = c.sendFrame(agentprotocol.TypeTunnelOpened, "", agentprotocol.TunnelOpened{No: o.No, Code: code, Error: msg})
	}
	if !c.tunnelOK {
		refuse(agentprotocol.TunnelCodeUnsupported, "this agent did not advertise instance tunnels")
		return
	}
	acceptor := h.tunnelAcceptor()
	if acceptor == nil {
		refuse(agentprotocol.TunnelCodeUnavailable, "instance services are not available")
		return
	}
	svcLimit := agentprotocol.TunnelServiceLimit(o.Service)
	if svcLimit == 0 || o.No == 0 {
		refuse(agentprotocol.TunnelCodeBadRequest, "unknown service")
		return
	}
	lim := h.tun.limits.filled()
	if !c.opens.allow(time.Now(), lim.rate) {
		refuse(agentprotocol.TunnelCodeRateLimited, "too many connections opened at once")
		return
	}
	if c.mux.Len() >= lim.perAgent || c.mux.Count(o.Service) >= svcLimit {
		refuse(agentprotocol.TunnelCodeLimit, fmt.Sprintf("too many open %s connections on this server", o.Service))
		return
	}
	owner := c.tunnelOwner()
	if !h.tun.reserve(c.teamID, owner, lim) {
		refuse(agentprotocol.TunnelCodeLimit, "too many open instance service connections")
		return
	}
	s, err := c.mux.Accept(o.No, o.Service)
	if err != nil {
		h.tun.release(c.teamID, owner)
		refuse(agentprotocol.TunnelCodeBadRequest, "stream number in use")
		return
	}
	if err := c.sendFrame(agentprotocol.TypeTunnelOpened, "", agentprotocol.TunnelOpened{No: o.No}); err != nil {
		s.Abort(agentprotocol.TunnelCodeClosed, "connection lost")
		h.tun.release(c.teamID, owner)
		return
	}
	peer := TunnelPeer{ServerID: c.serverID, AgentID: c.agentID, Service: o.Service}
	ctx, cancel := context.WithCancel(h.rootCtx)
	h.wg.Add(2)
	go func() { // counters and ctx follow the stream, not the acceptor
		defer h.wg.Done()
		<-s.Done()
		cancel()
		h.tun.release(c.teamID, owner)
	}()
	go func() {
		defer h.wg.Done()
		h.serveTunnel(ctx, acceptor, peer, s)
	}()
}

func (h *Hub) serveTunnel(ctx context.Context, a TunnelAcceptor, p TunnelPeer, s *tunnelmux.Stream) {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("internal error: %v", r)
				h.log.Error("tunnel acceptor panic", "service", p.Service, "server_id", p.ServerID, "panic", r)
			}
		}()
		err = a.AcceptTunnel(ctx, p, s)
	}()
	if err != nil && ctx.Err() == nil {
		s.Abort(agentprotocol.TunnelCodeError, err.Error())
	}
	_ = s.Close()
}

// tunnelData handles one binary message on the reader goroutine.
func (h *Hub) tunnelData(c *agentConn, data []byte) {
	if c.tunnelOK {
		c.mux.HandleData(data)
	}
}

// tunnelControl handles tunnel_ack and tunnel_close.
func (h *Hub) tunnelControl(c *agentConn, f agentprotocol.Frame) {
	if c.tunnelOK {
		c.mux.HandleControl(f.Type, f.Raw)
	}
}

// closeTunnels ends every tunnel stream of a connection that went away.
func (h *Hub) closeTunnels(c *agentConn) {
	c.mux.Down(errConnClosed)
}

// TunnelEndpoints returns the instance service listeners the server's agent
// reported in its last heartbeat (nil when offline or none).
func (h *Hub) TunnelEndpoints(serverID uuid.UUID) []agentprotocol.EndpointStatus {
	c := h.conn(serverID)
	if c == nil {
		return nil
	}
	if p := c.endpoints.Load(); p != nil {
		return append([]agentprotocol.EndpointStatus(nil), (*p)...)
	}
	return nil
}

// TunnelCapable reports whether the server's connected agent advertised
// instance tunnels.
func (h *Hub) TunnelCapable(serverID uuid.UUID) bool {
	c := h.conn(serverID)
	return c != nil && c.tunnelOK
}
