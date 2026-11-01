package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// MaxDeploymentLogLines caps the persisted build/deploy/runtime lines per
	// deployment.
	MaxDeploymentLogLines = 10000
	// LogTruncatedMarker is the last line stored once the cap is reached.
	LogTruncatedMarker = "… log truncated at 10000 lines"
	// MaxDeploymentDebugLines caps the persisted debug-stream lines per
	// deployment, on top of (and counted apart from) MaxDeploymentLogLines.
	MaxDeploymentDebugLines = 500
	// DebugLogTruncatedMarker is the last debug line stored once its cap is
	// reached.
	DebugLogTruncatedMarker = "… debug log truncated at 500 lines"

	maxLogLineBytes = 16 << 10
	subBuffer       = 256
	followBuffer    = 256
	maxDepStates    = 128
)

// streamCap is how many lines of one class (ordinary or debug) a deployment
// has stored and whether its truncation marker is out.
type streamCap struct {
	count     int
	truncated bool
}

// depLog is the per-deployment state of a connection's log intake.
type depLog struct {
	serviceID uuid.UUID
	redact    func(string) string
	main      streamCap
	debug     streamCap
}

// cleanLine makes a log line safe to store: no NUL bytes, valid UTF-8, bounded.
func cleanLine(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) > maxLogLineBytes {
		s = s[:maxLogLineBytes] + "…"
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// depState returns the log state for a deployment reported by this connection,
// or nil when the deployment does not run on the server.
func (h *Hub) depState(ctx context.Context, c *agentConn, depID uuid.UUID) *depLog {
	if st, ok := c.deps[depID]; ok {
		return st
	}
	sid, err := h.q.GetDeploymentServiceForReporter(ctx, db.GetDeploymentServiceForReporterParams{ID: depID, ServerID: &c.serverID})
	if errors.Is(err, pgx.ErrNoRows) {
		c.log.WarnContext(ctx, "log frame for a deployment that does not run on this server", "deployment_id", depID)
		return nil
	}
	if err != nil {
		c.log.ErrorContext(ctx, "load deployment for logs", "deployment_id", depID, "err", err)
		return nil
	}
	rd := h.logRedactor()
	if rd == nil {
		// Without a redactor secrets could leak into stored logs: fail closed.
		c.log.ErrorContext(ctx, "no log redactor configured; dropping deployment logs")
		return nil
	}
	redact, err := rd.RedactorFor(ctx, sid)
	if err != nil {
		c.log.ErrorContext(ctx, "build log redactor", "service_id", sid, "err", err)
		return nil
	}
	n, err := h.q.CountDeploymentLogs(ctx, depID)
	if err != nil {
		c.log.ErrorContext(ctx, "count deployment logs", "deployment_id", depID, "err", err)
		return nil
	}
	nd, err := h.q.CountDeploymentDebugLogs(ctx, depID)
	if err != nil {
		c.log.ErrorContext(ctx, "count deployment debug logs", "deployment_id", depID, "err", err)
		return nil
	}
	if len(c.deps) >= maxDepStates {
		clear(c.deps)
	}
	st := &depLog{
		serviceID: sid, redact: redact,
		main:  streamCap{count: int(n), truncated: n > MaxDeploymentLogLines},
		debug: streamCap{count: int(nd), truncated: nd > MaxDeploymentDebugLines},
	}
	c.deps[depID] = st
	return st
}

func (h *Hub) onLog(ctx context.Context, c *agentConn, lf agentprotocol.LogFrame) {
	if len(lf.Lines) == 0 {
		return
	}
	if lf.TaskRunID != "" {
		if sink := h.taskLogSink(); sink != nil {
			if err := sink.HandleTaskLog(ctx, c.serverID, lf); err != nil {
				c.log.ErrorContext(ctx, "store task run log", "task_run_id", lf.TaskRunID, "err", err)
			}
		}
		return
	}
	if lf.DeploymentID != "" {
		h.onDeploymentLog(ctx, c, lf)
		return
	}
	if lf.StreamID != "" {
		h.subMu.Lock()
		sub := h.subs[lf.StreamID]
		h.subMu.Unlock()
		if sub != nil && sub.serverID == c.serverID {
			sub.deliver(lf)
		}
	}
}

func (h *Hub) onDeploymentLog(ctx context.Context, c *agentConn, lf agentprotocol.LogFrame) {
	depID, err := uuid.Parse(lf.DeploymentID)
	if err != nil {
		return
	}
	st := h.depState(ctx, c, depID)
	if st == nil {
		return
	}
	stream := lf.Stream
	if stream != agentprotocol.StreamBuild && stream != agentprotocol.StreamDeploy && stream != agentprotocol.StreamRuntime && stream != agentprotocol.StreamDebug {
		stream = agentprotocol.StreamDeploy
	}
	// The debug stream has its own cap and marker: a chatty trace must neither
	// crowd out the build log nor grow without bound.
	limit, markerText, cp := MaxDeploymentLogLines, LogTruncatedMarker, &st.main
	if stream == agentprotocol.StreamDebug {
		limit, markerText, cp = MaxDeploymentDebugLines, DebugLogTruncatedMarker, &st.debug
	}

	clean := make([]agentprotocol.LogLine, 0, len(lf.Lines)+1)
	for _, l := range lf.Lines {
		if l.TS.IsZero() {
			l.TS = h.now()
		}
		l.Text = cleanLine(st.redact(l.Text))
		clean = append(clean, l)
	}

	// Persist up to the cap, then one marker line.
	room := limit - cp.count
	if room < 0 {
		room = 0
	}
	store := clean
	addMarker := false
	if len(store) > room {
		store = store[:room]
	}
	if !cp.truncated && len(clean) > room {
		addMarker = true
		cp.truncated = true
	}
	rows := make([][]any, 0, len(store)+1)
	for _, l := range store {
		rows = append(rows, []any{depID, stream, l.Text, l.TS})
	}
	var marker agentprotocol.LogLine
	if addMarker {
		marker = agentprotocol.LogLine{TS: h.now(), Text: markerText}
		rows = append(rows, []any{depID, stream, marker.Text, marker.TS})
	}
	if len(rows) > 0 {
		if _, err := h.pool.CopyFrom(ctx, pgx.Identifier{"deployment_logs"},
			[]string{"deployment_id", "stream", "line", "created_at"}, pgx.CopyFromRows(rows)); err != nil {
			c.log.ErrorContext(ctx, "persist deployment logs", "deployment_id", depID, "err", err)
		} else {
			cp.count += len(store)
		}
	}

	out := agentprotocol.LogFrame{DeploymentID: lf.DeploymentID, ServiceID: st.serviceID.String(), Stream: stream, Lines: clean}
	if addMarker {
		out.Lines = append(out.Lines, marker)
	}
	h.fanOutDeployment(depID, out)
}

func (h *Hub) onLogEnd(_ context.Context, c *agentConn, le agentprotocol.LogEnd) {
	if le.DeploymentID != "" {
		if id, err := uuid.Parse(le.DeploymentID); err == nil {
			delete(c.deps, id)
		}
		return
	}
	if le.StreamID != "" {
		h.subMu.Lock()
		sub := h.subs[le.StreamID]
		h.subMu.Unlock()
		if sub != nil && sub.serverID == c.serverID {
			if le.Error != "" {
				sub.deliver(agentprotocol.LogFrame{StreamID: le.StreamID, Stream: agentprotocol.StreamRuntime,
					Lines: []agentprotocol.LogLine{{TS: h.now(), Text: "[nebulactrl] log stream ended: " + le.Error}}})
			}
			sub.end()
		}
	}
}

// ---- deployment log followers ----

type follower struct {
	ch     chan agentprotocol.LogFrame
	closed bool
}

// FollowDeploymentLogs streams the redacted log frames of a deployment as they
// arrive. Lines stored before the call are not replayed: read deployment_logs
// first, then follow. cancel stops the stream and closes the channel.
func (h *Hub) FollowDeploymentLogs(ctx context.Context, deploymentID uuid.UUID) (<-chan agentprotocol.LogFrame, func()) {
	f := &follower{ch: make(chan agentprotocol.LogFrame, followBuffer)}
	h.folMu.Lock()
	set := h.followers[deploymentID]
	if set == nil {
		set = map[*follower]struct{}{}
		h.followers[deploymentID] = set
	}
	set[f] = struct{}{}
	h.folMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.folMu.Lock()
			defer h.folMu.Unlock()
			if set := h.followers[deploymentID]; set != nil {
				delete(set, f)
				if len(set) == 0 {
					delete(h.followers, deploymentID)
				}
			}
			f.closed = true
			close(f.ch)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-h.rootCtx.Done():
			cancel()
		}
	}()
	return f.ch, cancel
}

func (h *Hub) fanOutDeployment(depID uuid.UUID, frame agentprotocol.LogFrame) {
	h.folMu.Lock()
	defer h.folMu.Unlock()
	for f := range h.followers[depID] {
		if f.closed {
			continue
		}
		select {
		case f.ch <- frame:
		default: // slow follower: it can refetch from deployment_logs
		}
	}
}

// ---- runtime log subscriptions ----

type logSub struct {
	h        *Hub
	id       string
	cmdID    uuid.UUID
	serverID uuid.UUID
	conn     *agentConn

	mu     sync.Mutex
	ch     chan agentprotocol.LogFrame
	closed bool
}

func (s *logSub) deliver(f agentprotocol.LogFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- f:
	default: // consumer too slow: drop rather than stall the agent connection
	}
}

// end closes the stream (idempotent) and forgets the subscription.
func (s *logSub) end() bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()
	s.h.subMu.Lock()
	delete(s.h.subs, s.id)
	s.h.subMu.Unlock()
	return true
}

// SubscribeLogs asks the server's agent for runtime logs of a service. The
// command is ephemeral: it is written straight to the socket and never
// persisted. The channel closes at log_end, on cancel, when ctx ends or when
// the agent disconnects. Redaction is the caller's job.
func (h *Hub) SubscribeLogs(ctx context.Context, serverID uuid.UUID, req agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	c := h.conn(serverID)
	if c == nil {
		return nil, nil, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	}
	if req.StreamID == "" {
		req.StreamID = uuid.NewString()
	}
	sub := &logSub{h: h, id: req.StreamID, cmdID: uuid.New(), serverID: serverID, conn: c, ch: make(chan agentprotocol.LogFrame, subBuffer)}
	h.subMu.Lock()
	h.subs[sub.id] = sub
	h.subMu.Unlock()

	payload, _ := json.Marshal(req)
	cmd := agentprotocol.Command{
		ID: sub.cmdID.String(), IdempotencyKey: sub.cmdID.String(), Kind: agentprotocol.KindLogsSubscribe,
		Payload: payload, ExpiresAt: h.now().Add(time.Minute),
	}
	if err := c.sendFrame(agentprotocol.TypeCommand, cmd.ID, cmd); err != nil {
		sub.end()
		return nil, nil, apierr.New(http.StatusServiceUnavailable, "server_offline", "the server is offline")
	}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			if !sub.end() {
				return // the stream already ended; nothing to unsubscribe
			}
			if cc := h.conn(serverID); cc != nil {
				un := agentprotocol.Command{
					ID: uuid.NewString(), Kind: agentprotocol.KindLogsUnsubscribe,
					Payload: mustJSON(agentprotocol.LogsUnsubscribe{StreamID: sub.id}), ExpiresAt: h.now().Add(time.Minute),
				}
				un.IdempotencyKey = un.ID
				_ = cc.sendFrame(agentprotocol.TypeCommand, un.ID, un)
			}
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-h.rootCtx.Done():
			cancel()
		}
	}()
	return sub.ch, cancel, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// onEphemeralResult handles the result of a non-persisted command. A failed
// logs_subscribe ends its stream with a visible error line. Only the server the
// request or subscription was sent to is heard.
func (h *Hub) onEphemeralResult(c *agentConn, id uuid.UUID, res agentprotocol.ResultFrame) {
	if h.deliverRequest(c, id, res) {
		return
	}
	if res.Outcome == agentprotocol.OutcomeSucceeded {
		return
	}
	h.subMu.Lock()
	var sub *logSub
	for _, s := range h.subs {
		if s.cmdID == id && s.serverID == c.serverID {
			sub = s
			break
		}
	}
	h.subMu.Unlock()
	if sub == nil {
		return
	}
	msg := res.Error
	if msg == "" {
		msg = "subscription failed"
	}
	sub.deliver(agentprotocol.LogFrame{StreamID: sub.id, Stream: agentprotocol.StreamRuntime,
		Lines: []agentprotocol.LogLine{{TS: h.now(), Text: "[nebulactrl] " + msg}}})
	sub.end()
}

// closeConnSubs ends every log subscription served by a connection that went away.
func (h *Hub) closeConnSubs(c *agentConn) {
	h.subMu.Lock()
	var subs []*logSub
	for _, s := range h.subs {
		if s.conn == c {
			subs = append(subs, s)
		}
	}
	h.subMu.Unlock()
	for _, s := range subs {
		s.end()
	}
}
