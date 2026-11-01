package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Requests are ephemeral commands that are answered by a result: the control
// plane writes the command straight to the agent's socket (nothing is
// persisted or redelivered) and waits for the result. History queries and drain
// tests use them; the answer is only worth something while the asker waits.

type pendingRequest struct {
	conn *agentConn
	ch   chan agentprotocol.ResultFrame
}

// requestTable tracks the requests in flight. The zero value is ready.
type requestTable struct {
	mu sync.Mutex
	m  map[uuid.UUID]*pendingRequest
}

// ErrRequestLost is returned when the agent disconnected before it answered.
var ErrRequestLost = errors.New("the server disconnected before it answered")

// Request sends an ephemeral command to the server's agent and waits for its
// result. secrets ride in Command.Secrets (never persisted). A command that the
// agent answers with outcome "failed" is returned as a result, not an error; the
// error is for delivery problems: 503 server_offline, ErrRequestLost, ctx ending.
func (h *Hub) Request(ctx context.Context, serverID uuid.UUID, kind string, payload any, secrets map[string]string, timeout time.Duration) (agentprotocol.ResultFrame, error) {
	c := h.conn(serverID)
	if c == nil || c.closed() {
		return agentprotocol.ResultFrame{}, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return agentprotocol.ResultFrame{}, err
	}
	id := uuid.New()
	req := &pendingRequest{conn: c, ch: make(chan agentprotocol.ResultFrame, 1)}
	h.reqs.mu.Lock()
	if h.reqs.m == nil {
		h.reqs.m = map[uuid.UUID]*pendingRequest{}
	}
	h.reqs.m[id] = req
	h.reqs.mu.Unlock()
	defer func() {
		h.reqs.mu.Lock()
		delete(h.reqs.m, id)
		h.reqs.mu.Unlock()
	}()

	cmd := agentprotocol.Command{
		ID: id.String(), IdempotencyKey: id.String(), Kind: kind, Payload: raw, Secrets: secrets,
		ExpiresAt: h.now().Add(timeout + time.Minute),
	}
	if err := c.sendFrame(agentprotocol.TypeCommand, cmd.ID, cmd); err != nil {
		return agentprotocol.ResultFrame{}, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case res, ok := <-req.ch:
		if !ok {
			return agentprotocol.ResultFrame{}, ErrRequestLost
		}
		return res, nil
	case <-t.C:
		return agentprotocol.ResultFrame{}, apierr.New(http.StatusGatewayTimeout, "server_timeout", "the server did not answer in time")
	case <-ctx.Done():
		return agentprotocol.ResultFrame{}, ctx.Err()
	case <-h.rootCtx.Done():
		return agentprotocol.ResultFrame{}, ErrRequestLost
	}
}

// deliverRequest hands a result to the request waiting for it. It reports
// whether one was waiting on this connection: another server's agent cannot
// answer a request it was not sent.
func (h *Hub) deliverRequest(c *agentConn, id uuid.UUID, res agentprotocol.ResultFrame) bool {
	h.reqs.mu.Lock()
	req := h.reqs.m[id]
	h.reqs.mu.Unlock()
	if req == nil || req.conn != c {
		return false
	}
	select {
	case req.ch <- res:
	default:
	}
	return true
}

// closeConnRequests fails the requests waiting on a connection that went away.
func (h *Hub) closeConnRequests(c *agentConn) {
	h.reqs.mu.Lock()
	defer h.reqs.mu.Unlock()
	for id, req := range h.reqs.m {
		if req.conn == c {
			close(req.ch)
			delete(h.reqs.m, id)
		}
	}
}

// ---- log status ------------------------------------------------------------------

// LogStatusSink stores the log status an agent reports (usage of the log store,
// health of every drain). Implemented by internal/logs.
type LogStatusSink interface {
	HandleLogStatus(ctx context.Context, serverID uuid.UUID, st agentprotocol.LogStatus) error
}

type logStatusSlot struct {
	mu   sync.RWMutex
	sink LogStatusSink
}

// SetLogStatusSink installs the hook for log status frames (nil discards them).
func (h *Hub) SetLogStatusSink(s LogStatusSink) {
	h.logStatus.mu.Lock()
	defer h.logStatus.mu.Unlock()
	h.logStatus.sink = s
}

func (h *Hub) onLogStatus(ctx context.Context, c *agentConn, st agentprotocol.LogStatus) {
	h.logStatus.mu.RLock()
	sink := h.logStatus.sink
	h.logStatus.mu.RUnlock()
	if sink == nil {
		return
	}
	if err := sink.HandleLogStatus(ctx, c.serverID, st); err != nil {
		c.log.WarnContext(ctx, "handle log status", "err", err)
	}
}
