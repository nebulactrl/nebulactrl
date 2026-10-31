package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Browser ⇄ control plane protocol of GET /api/v1/services/{serviceId}/terminal/ws.
//
// Client → server:
//   - the first message is text JSON {"type":"open", process?, replica, command?, cols, rows, confirmProduction};
//   - binary messages are terminal input;
//   - text JSON {"type":"resize", cols, rows} and {"type":"close"}.
//
// Server → client:
//   - text JSON {"type":"opened", process?, replica, container, command};
//   - binary messages are terminal output;
//   - text JSON {"type":"idle_warning", secondsLeft}, {"type":"exit", exitCode},
//     {"type":"error", code, message} and a final {"type":"closed", reason}.
const (
	maxClientMessage = 64 << 10
	openReadTimeout  = 15 * time.Second
	pingEvery        = 30 * time.Second
	pingTimeout      = 10 * time.Second
	writeTimeout     = 10 * time.Second
	closeTimeout     = 3 * time.Second
)

// clientMsg is a text message from the browser.
type clientMsg struct {
	Type string `json:"type"`
	// Process is the process of a service with several processes ("" = the
	// default process); Replica counts within it.
	Process           string   `json:"process,omitempty"`
	Replica           int      `json:"replica"`
	Command           []string `json:"command,omitempty"`
	Cols              int      `json:"cols"`
	Rows              int      `json:"rows"`
	ConfirmProduction bool     `json:"confirmProduction"`
}

// serverMsg is a text message to the browser.
type serverMsg struct {
	Type        string   `json:"type"`
	Code        string   `json:"code,omitempty"`
	Message     string   `json:"message,omitempty"`
	Process     string   `json:"process,omitempty"`
	Replica     *int     `json:"replica,omitempty"`
	Container   string   `json:"container,omitempty"`
	Command     []string `json:"command,omitempty"`
	ExitCode    *int     `json:"exitCode,omitempty"`
	SecondsLeft int      `json:"secondsLeft,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

func clampSize(v, def int) int {
	if v <= 0 {
		return def
	}
	return min(max(v, agentprotocol.MinTermSize), agentprotocol.MaxTermSize)
}

// validateCommand checks a user-chosen argv.
func validateCommand(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	if len(argv) > MaxCommandArgs {
		return fmt.Errorf("a command has at most %d arguments", MaxCommandArgs)
	}
	for _, a := range argv {
		if len(a) > MaxCommandArgLen {
			return errors.New("a command argument is too long")
		}
	}
	if argv[0] == "" {
		return errors.New("the command is empty")
	}
	return nil
}

// commandLabel is what the audit log keeps of a command: the executable only
// (arguments may carry secrets).
func commandLabel(argv []string) string {
	if len(argv) == 0 {
		return "auto"
	}
	return path.Base(argv[0])
}

func writeHTTPError(w http.ResponseWriter, err error) {
	if ae, ok := apierr.As(err); ok {
		apierr.Write(w, ae)
		return
	}
	apierr.Write(w, apierr.Internal())
}

// serveWS upgrades GET /services/{serviceId}/terminal/ws and runs one session.
func (s *Service) serveWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		apierr.Write(w, apierr.NotFound("service"))
		return
	}
	t, p, err := s.authorize(ctx, id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if t.Kind == "route" {
		apierr.Write(w, apierr.Conflict("no_container", "this service has no container (it only routes traffic)"))
		return
	}
	// Accept verifies the Origin header against the request host (and the
	// configured public host): a page of another site cannot open a terminal
	// with the caller's session cookie.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxClientMessage)
	s.session(ctx, ws, t, p, r.URL.Query().Get("process"))
}

func sendJSON(ctx context.Context, ws *websocket.Conn, m serverMsg) error {
	b, _ := json.Marshal(m)
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return ws.Write(wctx, websocket.MessageText, b)
}

// reject tells the browser why no session was opened and closes the socket.
func (s *Service) reject(ctx context.Context, ws *websocket.Conn, code, msg string) {
	_ = sendJSON(ctx, ws, serverMsg{Type: "error", Code: code, Message: msg})
	_ = ws.Close(websocket.StatusNormalClosure, code)
}

func (s *Service) readOpen(ctx context.Context, ws *websocket.Conn) (clientMsg, error) {
	rctx, cancel := context.WithTimeout(ctx, openReadTimeout)
	defer cancel()
	typ, data, err := ws.Read(rctx)
	if err != nil {
		return clientMsg{}, errors.New("no open message received")
	}
	var m clientMsg
	if typ != websocket.MessageText || json.Unmarshal(data, &m) != nil || m.Type != "open" {
		return clientMsg{}, errors.New(`the first message must be {"type":"open",…}`)
	}
	if m.Replica < 0 || m.Replica > MaxReplica {
		return clientMsg{}, errors.New("invalid replica")
	}
	if err := validateCommand(m.Command); err != nil {
		return clientMsg{}, err
	}
	return m, nil
}

// session runs one terminal session on an accepted socket. queryProcess is the
// `process` query parameter of the upgrade request; the open message's own
// process wins over it.
func (s *Service) session(ctx context.Context, ws *websocket.Conn, t target, p *auth.Principal, queryProcess string) {
	open, err := s.readOpen(ctx, ws)
	if err != nil {
		s.reject(ctx, ws, CodeInvalidRequest, err.Error())
		return
	}
	if t.Production && !open.ConfirmProduction {
		s.reject(ctx, ws, CodeConfirmationRequired, "This is a production service: opening a terminal needs an explicit confirmation.")
		return
	}
	if open.Process == "" {
		open.Process = queryProcess
	}
	if err := t.requireRunning(open.Process, open.Replica); err != nil {
		ae, _ := apierr.As(err)
		code := CodeReplicaNotRunning
		if ae != nil && (ae.Code == CodeNotDeployed || ae.Code == CodeProcessNotFound) {
			code = ae.Code
		}
		s.reject(ctx, ws, code, err.Error())
		return
	}
	release := s.acquire(p.ID())
	if release == nil {
		s.reject(ctx, ws, CodeTooManySessions, fmt.Sprintf("You already have %d terminal sessions open. Close one first.", s.maxSessions))
		return
	}
	defer release()

	sid := uuid.NewString()
	st, err := s.agents.OpenStream(*t.ServerID, sid)
	if err != nil {
		s.reject(ctx, ws, CodeServerOffline, "The server is offline, so a terminal cannot be opened.")
		return
	}
	defer st.Close()

	cols, rows := clampSize(open.Cols, 80), clampSize(open.Rows, 24)
	if err := st.Send(agentprotocol.TypeExecOpen, agentprotocol.ExecOpen{
		SessionID: sid, ServiceID: t.ServiceID.String(), Replica: open.Replica, Process: t.agentProcess(open.Process), Command: open.Command, Cols: cols, Rows: rows,
	}); err != nil {
		s.reject(ctx, ws, CodeServerOffline, "The server is offline, so a terminal cannot be opened.")
		return
	}
	opened, ok := s.awaitOpened(ctx, st)
	if !ok {
		s.reject(ctx, ws, CodeServerOffline, "The server did not answer, so a terminal could not be opened.")
		return
	}
	if opened.Error != "" {
		code := CodeOpenFailed
		if opened.Code == "not_found" {
			code = CodeReplicaNotRunning
		}
		s.reject(ctx, ws, code, opened.Error)
		return
	}

	started := s.now()
	meta := map[string]any{
		"sessionId": sid, "process": open.Process, "replica": open.Replica, "container": opened.Container, "command": commandLabel(open.Command),
		"production": t.Production, "serverId": t.ServerID.String(), "environmentId": t.EnvironmentID.String(), "service": t.Slug,
	}
	if err := s.st.record(ctx, audit.Entry{
		Action: ActionOpen, ResourceType: "service", ResourceID: t.ServiceID.String(), ProjectID: &t.ProjectID, Metadata: meta,
	}); err != nil {
		// Fail closed: a session that cannot be audited does not run.
		s.log.ErrorContext(ctx, "audit terminal.open", "service_id", t.ServiceID, "err", err)
		_ = st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: sid})
		s.reject(ctx, ws, CodeOpenFailed, "The session could not be recorded in the audit log, so it was not opened.")
		return
	}
	rep := open.Replica
	_ = sendJSON(ctx, ws, serverMsg{Type: "opened", Process: open.Process, Replica: &rep, Container: opened.Container, Command: opened.Command})

	out := s.pump(ctx, ws, st, sid)

	// The close entry must be written even when the request context is gone.
	bg := context.WithoutCancel(ctx)
	closeMeta := map[string]any{
		"sessionId": sid, "process": open.Process, "replica": open.Replica, "container": opened.Container,
		"durationSeconds": int(s.now().Sub(started).Seconds()), "reason": out.reason, "production": t.Production,
	}
	if out.exit != nil {
		closeMeta["exitCode"] = *out.exit
	}
	if err := s.st.record(bg, audit.Entry{
		Action: ActionClose, ResourceType: "service", ResourceID: t.ServiceID.String(), ProjectID: &t.ProjectID, Metadata: closeMeta,
	}); err != nil {
		s.log.ErrorContext(bg, "audit terminal.close", "service_id", t.ServiceID, "err", err)
	}
}

// awaitOpened waits for the agent's answer to exec_open.
func (s *Service) awaitOpened(ctx context.Context, st Stream) (agentprotocol.ExecOpened, bool) {
	timer := time.NewTimer(s.openTimeout)
	defer timer.Stop()
	for {
		select {
		case f := <-st.Frames():
			if f.Type != agentprotocol.TypeExecOpened {
				continue
			}
			var o agentprotocol.ExecOpened
			if f.Into(&o) != nil {
				return o, false
			}
			return o, true
		case <-st.Done():
			return agentprotocol.ExecOpened{}, false
		case <-timer.C:
			return agentprotocol.ExecOpened{}, false
		case <-ctx.Done():
			return agentprotocol.ExecOpened{}, false
		}
	}
}

type outcome struct {
	reason string
	exit   *int
}

// pump relays input and output until the session ends and returns why. It
// closes the socket and the agent session before returning.
func (s *Service) pump(ctx context.Context, ws *websocket.Conn, st Stream, sid string) outcome {
	return s.pumpUntil(ctx, ws, st, sid, nil, "")
}

// pumpUntil is pump with an extra way to end the session: when halt closes,
// the session ends with haltReason (the browser is told, the agent kills the
// process). Cancelling ctx instead would drop the socket without a word, as
// the WebSocket library closes a connection whose context is cancelled.
func (s *Service) pumpUntil(ctx context.Context, ws *websocket.Conn, st Stream, sid string, halt <-chan struct{}, haltReason string) outcome {
	stop := make(chan struct{})
	var lastInput atomic.Int64
	lastInput.Store(s.now().UnixNano())
	res := make(chan outcome, 2)
	finish := func(o outcome) {
		select {
		case res <- o:
		default:
		}
	}
	var wg sync.WaitGroup

	// Browser → agent. Not part of wg: it ends when the socket does, and the
	// close handshake below must not hold the session (and its audit entry and
	// concurrency slot) hostage to a peer that is slow to answer.
	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				finish(outcome{reason: ReasonClientClosed})
				return
			}
			if typ == websocket.MessageBinary {
				lastInput.Store(s.now().UnixNano())
				for len(data) > 0 {
					n := min(len(data), agentprotocol.ExecChunk)
					if err := st.Send(agentprotocol.TypeExecInput, agentprotocol.ExecInput{SessionID: sid, Data: data[:n]}); err != nil {
						finish(outcome{reason: ReasonAgentDisconnected})
						return
					}
					data = data[n:]
				}
				continue
			}
			var m clientMsg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "resize":
				_ = st.Send(agentprotocol.TypeExecResize, agentprotocol.ExecResize{SessionID: sid, Cols: clampSize(m.Cols, 80), Rows: clampSize(m.Rows, 24)})
			case "close":
				finish(outcome{reason: ReasonClientClosed})
				return
			}
		}
	}()

	// Agent → browser.
	handle := func(f agentprotocol.Frame) (done bool) {
		switch f.Type {
		case agentprotocol.TypeExecOutput:
			var o agentprotocol.ExecOutput
			if f.Into(&o) != nil {
				return false
			}
			if len(o.Data) > agentprotocol.ExecChunk {
				// A frame above the chunk size is not from a well-behaved agent.
				finish(outcome{reason: ReasonError})
				return true
			}
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(wctx, websocket.MessageBinary, o.Data)
			wcancel()
			if err != nil {
				finish(outcome{reason: ReasonClientClosed})
				return true
			}
			_ = st.Send(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: sid, Bytes: len(o.Data)})
		case agentprotocol.TypeExecExit:
			var e agentprotocol.ExecExit
			if f.Into(&e) != nil {
				return false
			}
			code := e.ExitCode
			if e.Error != "" {
				finish(outcome{reason: ReasonError, exit: &code})
			} else {
				finish(outcome{reason: ReasonExit, exit: &code})
			}
			return true
		}
		return false
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case f := <-st.Frames():
				if handle(f) {
					return
				}
			case <-st.Done():
				for { // frames queued before the connection dropped (the exit among them)
					select {
					case f := <-st.Frames():
						if handle(f) {
							return
						}
					default:
						finish(outcome{reason: ReasonAgentDisconnected})
						return
					}
				}
			case <-stop:
				return
			}
		}
	}()

	// Idle timeout and liveness.
	tick := time.NewTicker(s.tick)
	defer tick.Stop()
	lastPing := s.now()
	warned := false
	var o outcome
wait:
	for {
		select {
		case o = <-res:
			break wait
		case <-halt:
			o = outcome{reason: haltReason}
			break wait
		case <-ctx.Done():
			o = outcome{reason: ReasonClientClosed}
			break wait
		case <-tick.C:
		}
		now := s.now()
		idleFor := now.Sub(time.Unix(0, lastInput.Load()))
		switch {
		case idleFor >= s.idle:
			o = outcome{reason: ReasonIdle}
			break wait
		case !warned && s.idle-idleFor <= s.idleWarning:
			warned = true
			_ = sendJSON(ctx, ws, serverMsg{Type: "idle_warning", SecondsLeft: int((s.idle - idleFor).Seconds())})
		case warned && s.idle-idleFor > s.idleWarning:
			warned = false // input arrived after the warning
		}
		if now.Sub(lastPing) >= pingEvery {
			lastPing = now
			pctx, pcancel := context.WithTimeout(ctx, pingTimeout)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				o = outcome{reason: ReasonClientClosed}
				break wait
			}
		}
	}

	close(stop)
	if o.reason != ReasonExit && o.reason != ReasonAgentDisconnected {
		_ = st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: sid})
	}
	bg, bgCancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer bgCancel()
	if o.exit != nil {
		_ = sendJSON(bg, ws, serverMsg{Type: "exit", ExitCode: o.exit})
	}
	_ = sendJSON(bg, ws, serverMsg{Type: "closed", Reason: o.reason})
	// The close handshake waits for the peer's close frame, which a browser
	// that stopped reading never sends; finish it in the background so the
	// session can be audited and its slot released right away.
	go func() { _ = ws.Close(websocket.StatusNormalClosure, o.reason) }()
	wg.Wait()
	return o
}
