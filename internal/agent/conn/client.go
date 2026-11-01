// Package conn is the agent's side of the control-plane protocol (ADR 0002): the
// authenticated websocket, heartbeats, durable command intake and the outbound
// frame queue.
package conn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/internal/tunnelmux"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Sentinel errors returned by Client.Run; both are fatal and map to a non-zero exit.
var (
	// ErrRevoked: the control plane closed the socket with 4401.
	ErrRevoked = errors.New("agent revoked; re-enroll")
	// ErrUnsupportedVersion: the control plane closed with 4400, or sent a frame
	// with a different major protocol version.
	ErrUnsupportedVersion = errors.New("protocol version not supported by control plane (close 4400)")
)

const (
	minBackoff     = time.Second
	maxBackoff     = 60 * time.Second
	stableAfter    = 30 * time.Second // a connection this old resets the backoff
	handshakeLimit = 15 * time.Second
	writeTimeout   = 15 * time.Second
	readLimit      = 16 << 20 // commands carry payload + secrets
)

// Config configures a Client.
type Config struct {
	// URL is the full websocket URL (wss://host/agent/v1/connect).
	URL      string
	Identity store.Identity
	Store    *store.Store
	Version  string
	// Facts and Heartbeat supply hello facts and periodic heartbeats.
	Facts     func(ctx context.Context) agentprotocol.Facts
	Heartbeat func(ctx context.Context) agentprotocol.Heartbeat
	Logger    *slog.Logger
	// MaxBuffered bounds frames queued while disconnected (default DefaultMaxBuffered).
	MaxBuffered int
	// HTTPClient is used for the websocket dial (optional).
	HTTPClient *http.Client
	// HeartbeatInterval overrides agentprotocol.HeartbeatInterval (tests).
	HeartbeatInterval time.Duration
}

// Client maintains the agent's connection to the control plane. It implements Sender.
type Client struct {
	cfg Config
	log *slog.Logger
	out *outbox

	mu         sync.RWMutex
	handler    Handler
	reconciler Reconciler
	streams    StreamHandler

	connected    atomic.Bool
	onTrafficAck func(seq uint64)
	onProbesAck  func(seq uint64)

	inflight sync.Map       // command ID -> struct{}
	handlers sync.WaitGroup // running command handlers; Run waits for them
	hmu      sync.Mutex     // orders handlers.Add against Run's final Wait
	stopping bool           // Run is returning: no new handler starts
	reconMu  sync.Mutex
	tun      *tunnelmux.Mux // instance service tunnel streams (tunnel.go)
	rootCtx  context.Context
}

var _ Sender = (*Client)(nil)

// New creates a Client. Set the Handler and Reconciler (SetHandler,
// SetReconciler) before Run; both default to rejecting everything.
func New(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = agentprotocol.HeartbeatInterval
	}
	c := &Client{
		cfg:     cfg,
		log:     cfg.Logger.With("component", "conn"),
		out:     newOutbox(cfg.MaxBuffered),
		rootCtx: context.Background(),
	}
	c.tun = tunnelmux.New(tunnelmux.Config{Control: c.tunnelControl, Log: c.log})
	return c
}

// SetHandler installs the command handler.
func (c *Client) SetHandler(h Handler) {
	c.mu.Lock()
	c.handler = h
	// A handler that also serves interactive streams (terminal, file browser)
	// is installed for them too.
	if sh, ok := h.(StreamHandler); ok {
		c.streams = sh
	}
	c.mu.Unlock()
}

// SetReconciler installs the reconcile hook.
func (c *Client) SetReconciler(r Reconciler) {
	c.mu.Lock()
	c.reconciler = r
	c.mu.Unlock()
}

// SetTrafficAckHandler installs the callback for traffic_ack frames.
func (c *Client) SetTrafficAckHandler(fn func(seq uint64)) {
	c.mu.Lock()
	c.onTrafficAck = fn
	c.mu.Unlock()
}

// SetProbesAckHandler installs the callback for probes_ack frames.
func (c *Client) SetProbesAckHandler(fn func(seq uint64)) {
	c.mu.Lock()
	c.onProbesAck = fn
	c.mu.Unlock()
}

// Connected reports whether the authenticated socket is currently up.
func (c *Client) Connected() bool { return c.connected.Load() }

// Run connects, authenticates and serves until ctx ends or a fatal condition
// occurs (ErrRevoked, ErrUnsupportedVersion). Other failures reconnect with
// exponential backoff (1s doubling to 60s, plus jitter). It returns only after
// every command handler it started has returned: a stopped agent must not go
// on changing containers or the proxy (handlers see ctx canceled and unwind).
func (c *Client) Run(ctx context.Context) error {
	defer func() {
		// The read loop of the last session may still be delivering a command.
		c.hmu.Lock()
		c.stopping = true
		c.hmu.Unlock()
		c.handlers.Wait()
	}()
	c.rootCtx = ctx
	backoff := minBackoff
	for {
		started := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case websocket.CloseStatus(err) == agentprotocol.CloseRevoked, errors.Is(err, ErrRevoked):
			return ErrRevoked
		case websocket.CloseStatus(err) == agentprotocol.CloseUnsupportedVersion, errors.Is(err, ErrUnsupportedVersion):
			return ErrUnsupportedVersion
		}
		if time.Since(started) >= stableAfter {
			backoff = minBackoff
		}
		delay := jitter(backoff)
		c.log.Warn("disconnected from control plane", "err", err, "retry_in", delay.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// jitter adds up to 25% random delay.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/4+1)) //nolint:gosec // backoff jitter needs no secrecy
}

// session runs one connection: handshake, hello, then reader/writer/heartbeat
// until any of them fails.
func (c *Client) session(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, handshakeLimit)
	opts := &websocket.DialOptions{HTTPClient: c.cfg.HTTPClient, HTTPHeader: http.Header{"User-Agent": {"nebulactrl-agent/" + c.cfg.Version}}}
	ws, resp, err := websocket.Dial(dctx, c.cfg.URL, opts) //nolint:bodyclose // Dial owns the handshake body
	cancel()
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial %s: HTTP %d: %w", c.cfg.URL, resp.StatusCode, err)
		}
		return fmt.Errorf("dial %s: %w", c.cfg.URL, err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(readLimit)

	if err := c.handshake(ctx, ws); err != nil {
		return err
	}
	c.log.Info("connected to control plane", "agent_id", c.cfg.Identity.AgentID, "server_id", c.cfg.Identity.ServerID)
	c.tun.Up()
	c.connected.Store(true)
	defer func() {
		c.connected.Store(false)
		// Streams are ephemeral: a dropped connection ends every session, and
		// frames queued for them must not leak into the next connection.
		c.out.dropStreams()
		c.tun.Down(ErrNotConnected)
		c.mu.RLock()
		sh := c.streams
		c.mu.RUnlock()
		if sh != nil {
			sh.Disconnected()
		}
	}()

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	errc := make(chan error, 3)
	go func() { errc <- c.writeLoop(sctx, ws) }()
	go func() { errc <- c.heartbeatLoop(sctx) }()
	go func() { errc <- c.readLoop(sctx, ws) }()

	err = <-errc
	if websocket.CloseStatus(err) == -1 && !errors.Is(err, ErrUnsupportedVersion) && ctx.Err() == nil {
		// A write error can race the reader's close frame; prefer the close code.
		select {
		case e2 := <-errc:
			if websocket.CloseStatus(e2) != -1 {
				err = e2
			}
		case <-time.After(250 * time.Millisecond):
		}
	}
	stop()
	if errors.Is(err, ErrUnsupportedVersion) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseUnsupportedVersion), "unsupported protocol version")
	} else {
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
	return err
}

// handshake answers the server's challenge and sends hello.
func (c *Client) handshake(ctx context.Context, ws *websocket.Conn) error {
	hctx, cancel := context.WithTimeout(ctx, handshakeLimit)
	defer cancel()

	_, data, err := ws.Read(hctx)
	if err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}
	f, err := agentprotocol.Decode(data)
	if err != nil {
		return err
	}
	if f.V != agentprotocol.ProtocolVersion {
		return ErrUnsupportedVersion
	}
	if f.Type != agentprotocol.TypeChallenge {
		return fmt.Errorf("expected challenge, got %q", f.Type)
	}
	var ch agentprotocol.Challenge
	if err := f.Into(&ch); err != nil {
		return fmt.Errorf("decode challenge: %w", err)
	}
	auth := agentprotocol.Auth{AgentID: c.cfg.Identity.AgentID, Signature: SignChallenge(c.cfg.Identity.PrivateKey, c.cfg.Identity.AgentID, ch)}
	if err := writeFrame(hctx, ws, agentprotocol.TypeAuth, "", auth); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	unacked, err := c.cfg.Store.UnackedResults()
	if err != nil {
		return fmt.Errorf("load unacked results: %w", err)
	}
	var facts agentprotocol.Facts
	if c.cfg.Facts != nil {
		facts = c.cfg.Facts(hctx)
	}
	hello := agentprotocol.Hello{
		AgentVersion:    c.cfg.Version,
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Facts:           facts,
		UnackedResults:  unacked,
	}
	if err := writeFrame(hctx, ws, agentprotocol.TypeHello, "", hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	return nil
}

func writeFrame(ctx context.Context, ws *websocket.Conn, typ, id string, body any) error {
	data, err := agentprotocol.Encode(typ, id, body)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, data)
}

// nextWrite picks the next message without waiting: the outbox first, then one
// tunnel data chunk. Control frames (heartbeats, acks, results) therefore wait
// for at most one chunk, however much bulk data the tunnel streams queued.
// ctl is set for outbox frames (they are requeued when the write fails).
func (c *Client) nextWrite() (mt websocket.MessageType, data []byte, ctl *outFrame, ok bool) {
	if f, ok := c.out.tryNext(); ok {
		return websocket.MessageText, f.data, &f, true
	}
	if data, ok := c.tun.Next(); ok {
		return websocket.MessageBinary, data, nil, true
	}
	return 0, nil, nil, false
}

// writeLoop is the single writer: it drains nextWrite onto the socket.
func (c *Client) writeLoop(ctx context.Context, ws *websocket.Conn) error {
	for {
		if mt, data, ctl, ok := c.nextWrite(); ok {
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(wctx, mt, data)
			cancel()
			if err != nil {
				if ctl != nil {
					c.out.requeue(*ctl)
					return fmt.Errorf("write %s: %w", ctl.typ, err)
				}
				return fmt.Errorf("write tunnel data: %w", err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.out.signal:
		case <-c.tun.Ready():
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context) error {
	if c.cfg.Heartbeat == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(c.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		c.send(agentprotocol.TypeHeartbeat, "", c.cfg.Heartbeat(ctx))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (c *Client) readLoop(ctx context.Context, ws *websocket.Conn) error {
	for {
		mt, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		if mt == websocket.MessageBinary {
			c.tun.HandleData(data)
			continue
		}
		f, err := agentprotocol.Decode(data)
		if err != nil {
			c.log.Warn("undecodable frame", "err", err)
			continue
		}
		if f.V != agentprotocol.ProtocolVersion {
			return ErrUnsupportedVersion
		}
		switch f.Type {
		case agentprotocol.TypeCommand:
			var cmd agentprotocol.Command
			if err := f.Into(&cmd); err != nil {
				c.log.Warn("bad command frame", "err", err)
				continue
			}
			c.onCommand(cmd)
		case agentprotocol.TypeResultAck:
			var ack agentprotocol.ResultAck
			if err := f.Into(&ack); err != nil {
				continue
			}
			if err := c.cfg.Store.AckResult(ack.CommandID); err != nil {
				c.log.Error("ack result", "command_id", ack.CommandID, "err", err)
			}
		case agentprotocol.TypeTrafficAck:
			var ack agentprotocol.TrafficAck
			if err := f.Into(&ack); err != nil {
				continue
			}
			c.mu.RLock()
			fn := c.onTrafficAck
			c.mu.RUnlock()
			if fn != nil {
				fn(ack.Seq)
			}
		case agentprotocol.TypeProbesAck:
			var ack agentprotocol.ProbesAck
			if err := f.Into(&ack); err != nil {
				continue
			}
			c.mu.RLock()
			fn := c.onProbesAck
			c.mu.RUnlock()
			if fn != nil {
				fn(ack.Seq)
			}
		case agentprotocol.TypeTunnelOpened, agentprotocol.TypeTunnelAck, agentprotocol.TypeTunnelClose:
			c.tun.HandleControl(f.Type, f.Raw)
		case agentprotocol.TypeReconcile:
			var rec agentprotocol.Reconcile
			if err := f.Into(&rec); err != nil {
				c.log.Warn("bad reconcile frame", "err", err)
				continue
			}
			c.mu.RLock()
			r := c.reconciler
			c.mu.RUnlock()
			if fo, ok := r.(FrameObserver); ok {
				fo.ObserveFrame(c.rootCtx, rec)
			}
			go c.onReconcile(rec)
		default:
			if agentprotocol.IsStreamType(f.Type) {
				c.mu.RLock()
				sh := c.streams
				c.mu.RUnlock()
				if sh != nil {
					sh.HandleStream(f)
				}
				continue
			}
			c.log.Debug("ignoring frame", "type", f.Type)
		}
	}
}

func (c *Client) onReconcile(rec agentprotocol.Reconcile) {
	c.mu.RLock()
	r := c.reconciler
	c.mu.RUnlock()
	if r == nil {
		return
	}
	c.reconMu.Lock()
	defer c.reconMu.Unlock()
	if err := r.Reconcile(c.rootCtx, rec); err != nil {
		c.log.Error("reconcile failed", "err", err)
	}
}

// onCommand implements durable, idempotent command intake: ack immediately,
// dedupe by command ID, record, execute, store the result, send it.
func (c *Client) onCommand(cmd agentprotocol.Command) {
	log := c.log.With("command_id", cmd.ID, "kind", cmd.Kind)
	c.send(agentprotocol.TypeAck, cmd.ID, agentprotocol.Ack{CommandID: cmd.ID})

	stored, seen := c.cfg.Store.SeenCommand(cmd.ID)
	if seen && stored != nil {
		log.Info("redelivered command, replaying stored result")
		c.send(agentprotocol.TypeResult, cmd.ID, *stored)
		return
	}
	if _, running := c.inflight.Load(cmd.ID); running {
		log.Info("redelivered command already running")
		return
	}

	if !agentprotocol.AllowedKinds[cmd.Kind] {
		log.Warn("rejecting command kind")
		c.finish(cmd.ID, failed(cmd.ID, "command kind not allowed"))
		return
	}
	if !seen && !cmd.ExpiresAt.IsZero() && time.Now().After(cmd.ExpiresAt) {
		// The control plane has expired (or will expire) this command as
		// "not delivered"; running it now would contradict that.
		log.Warn("command expired before first delivery", "expires_at", cmd.ExpiresAt)
		c.finish(cmd.ID, failed(cmd.ID, "command expired before delivery"))
		return
	}
	if err := c.cfg.Store.MarkCommand(cmd.ID); err != nil {
		log.Error("record command", "err", err)
		c.finish(cmd.ID, failed(cmd.ID, "agent could not persist command: "+err.Error()))
		return
	}

	c.mu.RLock()
	h := c.handler
	c.mu.RUnlock()
	if h == nil {
		c.finish(cmd.ID, failed(cmd.ID, "no command handler installed"))
		return
	}
	c.hmu.Lock()
	if c.stopping {
		c.hmu.Unlock()
		// Shutting down: no result, so the command runs again (idempotently)
		// after restart, as for a handler interrupted by shutdown.
		log.Info("command arrived during shutdown; not started")
		return
	}
	c.handlers.Add(1)
	c.hmu.Unlock()
	c.inflight.Store(cmd.ID, struct{}{})
	go func() {
		defer c.handlers.Done()
		defer c.inflight.Delete(cmd.ID)
		res := c.runHandler(h, cmd)
		if c.rootCtx.Err() != nil {
			// The agent is shutting down: the handler was interrupted, so its
			// result is not authoritative. Keep no result so the command runs
			// again (idempotently) after restart.
			log.Info("command interrupted by shutdown; result discarded")
			return
		}
		res.CommandID = cmd.ID
		c.finish(cmd.ID, res)
		if a, ok := h.(ResultObserver); ok {
			// Queued after the result, so the control plane applies it afterwards.
			a.ResultSent(c.rootCtx, cmd, res)
		}
	}()
}

func (c *Client) runHandler(h Handler, cmd agentprotocol.Command) (res agentprotocol.ResultFrame) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("command handler panic", "command_id", cmd.ID, "kind", cmd.Kind, "panic", r)
			res = failed(cmd.ID, fmt.Sprintf("internal error: %v", r))
		}
	}()
	return h.HandleCommand(c.rootCtx, cmd)
}

// finish stores the result durably and queues it for the control plane. The
// stored copy is resent in hello.unackedResults until result_ack arrives.
func (c *Client) finish(id string, res agentprotocol.ResultFrame) {
	res.CommandID = id
	if err := c.cfg.Store.PutResult(res); err != nil {
		c.log.Error("store result", "command_id", id, "err", err)
	}
	c.send(agentprotocol.TypeResult, id, res)
}

func (c *Client) send(typ, id string, body any) {
	data, err := agentprotocol.Encode(typ, id, body)
	if err != nil {
		c.log.Error("encode frame", "type", typ, "err", err)
		return
	}
	c.out.push(outFrame{typ: typ, data: data})
}

// Sender implementation.

func (c *Client) Event(e agentprotocol.EventFrame)  { c.send(agentprotocol.TypeEvent, "", e) }
func (c *Client) Log(l agentprotocol.LogFrame)      { c.send(agentprotocol.TypeLog, "", l) }
func (c *Client) LogEnd(l agentprotocol.LogEnd)     { c.send(agentprotocol.TypeLogEnd, "", l) }
func (c *Client) Stats(s agentprotocol.Stats)       { c.send(agentprotocol.TypeStats, "", s) }
func (c *Client) Observed(o agentprotocol.Observed) { c.send(agentprotocol.TypeObserved, "", o) }
func (c *Client) Traffic(t agentprotocol.Traffic)   { c.send(agentprotocol.TypeTraffic, "", t) }

// Probes queues a Probes frame. Like Traffic it is neither superseded nor
// dropped by the outbox: the collector buffers results and waits for the ack.
func (c *Client) Probes(p agentprotocol.Probes) { c.send(agentprotocol.TypeProbes, "", p) }

// Stream queues an interactive stream frame (exec_*, fs_*, stream_ack) whose
// envelope ID is id. Unlike other frames it is dropped when the control plane
// is unreachable, because the stream died with the connection. It reports
// whether the frame was queued.
func (c *Client) Stream(typ, id string, body any) bool {
	if !c.connected.Load() {
		return false
	}
	data, err := agentprotocol.Encode(typ, id, body)
	if err != nil {
		c.log.Error("encode stream frame", "type", typ, "err", err)
		return false
	}
	c.out.push(outFrame{typ: typ, data: data})
	return true
}
