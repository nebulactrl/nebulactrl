package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ----

type fakeStream struct {
	id     string
	frames chan agentprotocol.Frame
	done   chan struct{}
	once   sync.Once
	lost   atomic.Bool
	mu     sync.Mutex
	sent   []sentFrame
	onSend func(typ string, raw json.RawMessage)
	sendOK atomic.Bool
}

type sentFrame struct {
	typ string
	raw json.RawMessage
}

func newFakeStream(id string) *fakeStream {
	f := &fakeStream{id: id, frames: make(chan agentprotocol.Frame, 256), done: make(chan struct{})}
	f.sendOK.Store(true)
	return f
}

func (f *fakeStream) Frames() <-chan agentprotocol.Frame { return f.frames }
func (f *fakeStream) Done() <-chan struct{}              { return f.done }
func (f *fakeStream) Lost() bool                         { return f.lost.Load() }
func (f *fakeStream) Close()                             { f.once.Do(func() { close(f.done) }) }

// drop simulates the agent connection dropping.
func (f *fakeStream) drop() {
	f.lost.Store(true)
	f.Close()
}

func (f *fakeStream) Send(typ string, body any) error {
	if !f.sendOK.Load() {
		return errors.New("closed")
	}
	raw, _ := json.Marshal(body)
	f.mu.Lock()
	f.sent = append(f.sent, sentFrame{typ, raw})
	hook := f.onSend
	f.mu.Unlock()
	if hook != nil {
		hook(typ, raw)
	}
	return nil
}

// push delivers an agent frame.
func (f *fakeStream) push(typ string, body any) {
	data, _ := agentprotocol.Encode(typ, f.id, body)
	fr, _ := agentprotocol.Decode(data)
	f.frames <- fr
}

func (f *fakeStream) sentOf(typ string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for _, s := range f.sent {
		if s.typ == typ {
			out = append(out, s.raw)
		}
	}
	return out
}

type fakeAgents struct {
	mu      sync.Mutex
	err     error
	streams []*fakeStream
	hook    func(fs *fakeStream) // installs the agent's behaviour on a new stream
}

func (a *fakeAgents) OpenStream(_ uuid.UUID, id string) (Stream, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	fs := newFakeStream(id)
	if a.hook != nil {
		a.hook(fs)
	}
	a.streams = append(a.streams, fs)
	return fs, nil
}

func (a *fakeAgents) stream(i int) *fakeStream {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.streams[i]
}

func (a *fakeAgents) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.streams)
}

type fakeStore struct {
	mu         sync.Mutex
	t          target
	prodWrites bool
	audits     []audit.Entry
	auditErr   error
}

func (s *fakeStore) load(_ context.Context, id uuid.UUID) (target, error) {
	if id != s.t.ServiceID {
		return target{}, apierr.NotFound("service")
	}
	return s.t, nil
}
func (s *fakeStore) serverName(context.Context, uuid.UUID) string { return "hetzner-01" }
func (s *fakeStore) record(_ context.Context, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditErr != nil {
		return s.auditErr
	}
	s.audits = append(s.audits, e)
	return nil
}
func (s *fakeStore) allowProductionWrites(context.Context, uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prodWrites, nil
}
func (s *fakeStore) projectInScope(context.Context, uuid.UUID) error { return nil }
func (s *fakeStore) setAllowProductionWrites(_ context.Context, _ uuid.UUID, allow bool, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prodWrites = allow
	s.audits = append(s.audits, e)
	return nil
}

func (s *fakeStore) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.audits {
		out = append(out, a.Action)
	}
	return out
}

func (s *fakeStore) audit(action string) audit.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.audits {
		if a.Action == action {
			return a
		}
	}
	return audit.Entry{}
}

type rig struct {
	t      *testing.T
	svc    *Service
	store  *fakeStore
	agents *fakeAgents
	srv    *httptest.Server
	who    atomic.Pointer[auth.Principal]
	target target
}

func principal(role string, scopes []string, projects ...uuid.UUID) *auth.Principal {
	p := &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: uuid.New(), Label: "dev@example.com", Role: role, Scopes: scopes}
	if len(projects) > 0 {
		p.ProjectIDs = projects
	}
	return p
}

func devPrincipal() *auth.Principal { return principal(auth.RoleDeveloper, policy.AllScopes) }

func newRig(t *testing.T, production bool) *rig {
	t.Helper()
	server := uuid.New()
	tg := target{
		ServiceID: uuid.New(), ProjectID: uuid.New(), EnvironmentID: uuid.New(), ServerID: &server,
		Name: "Web", Slug: "web", Kind: "app", Production: production, Deployed: true,
		Replicas: []ReplicaView{{Index: 0, State: "running", Running: true}, {Index: 1, State: "exited"}},
	}
	r := &rig{t: t, store: &fakeStore{t: tg}, agents: &fakeAgents{}, target: tg}
	r.svc = NewService(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r.svc.st, r.svc.agents = r.store, r.agents
	r.svc.tick = 10 * time.Millisecond
	r.svc.openTimeout = 2 * time.Second
	r.svc.fsTimeout = 2 * time.Second
	r.who.Store(devPrincipal())

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p := r.who.Load(); p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.svc.MountAPI(router)
	r.srv = httptest.NewServer(router)
	t.Cleanup(r.srv.Close)
	r.agents.hook = func(fs *fakeStream) { fs.onSend = echoAgent(fs) }
	return r
}

// echoAgent plays a shell: it opens, echoes input back with a prefix.
func echoAgent(fs *fakeStream) func(string, json.RawMessage) {
	return func(typ string, raw json.RawMessage) {
		switch typ {
		case agentprotocol.TypeExecOpen:
			fs.push(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: fs.id, Container: "nc-web-abcd1234-0", Command: []string{"sh"}})
		case agentprotocol.TypeExecInput:
			var in agentprotocol.ExecInput
			_ = json.Unmarshal(raw, &in)
			fs.push(agentprotocol.TypeExecOutput, agentprotocol.ExecOutput{SessionID: fs.id, Data: append([]byte("echo:"), in.Data...)})
		}
	}
}

func (r *rig) url(path string) string {
	return r.srv.URL + "/services/" + r.target.ServiceID.String() + path
}

func (r *rig) dial(opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.url("/terminal/ws"), "http"), opts)
}

func (r *rig) mustDial() *websocket.Conn {
	r.t.Helper()
	c, _, err := r.dial(nil)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func writeJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, c.Write(ctx, websocket.MessageText, b))
}

func openMsgFor(replica int, confirm bool) map[string]any {
	return map[string]any{"type": "open", "replica": replica, "cols": 100, "rows": 30, "confirmProduction": confirm}
}

// next reads the next message.
func next(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	return typ, data
}

// nextJSON reads text messages until one of type want (skipping others).
func nextJSON(t *testing.T, c *websocket.Conn, want string) serverMsg {
	t.Helper()
	for {
		typ, data := next(t, c)
		if typ != websocket.MessageText {
			continue
		}
		var m serverMsg
		require.NoError(t, json.Unmarshal(data, &m))
		if m.Type == want {
			return m
		}
	}
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 3*time.Second, 5*time.Millisecond, msg)
}

// ---- terminal sessions ----

func TestSessionRelaysInputOutputResizeAndAuditsOpenClose(t *testing.T) {
	r := newRig(t, false)
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	opened := nextJSON(t, c, "opened")
	require.Equal(t, "nc-web-abcd1234-0", opened.Container)
	require.Equal(t, 0, *opened.Replica)

	ctx := context.Background()
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte("ls\n")))
	typ, data := next(t, c)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Equal(t, "echo:ls\n", string(data))

	writeJSON(t, c, map[string]any{"type": "resize", "cols": 140, "rows": 50})
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecResize)) == 1 }, "resize relayed")
	var rz agentprotocol.ExecResize
	require.NoError(t, json.Unmarshal(st.sentOf(agentprotocol.TypeExecResize)[0], &rz))
	require.Equal(t, 140, rz.Cols)
	require.Equal(t, 50, rz.Rows)

	// The agent is credited for what was written to the browser (flow control).
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeStreamAck)) == 1 }, "output acknowledged")
	var ack agentprotocol.StreamAck
	require.NoError(t, json.Unmarshal(st.sentOf(agentprotocol.TypeStreamAck)[0], &ack))
	require.Equal(t, st.id, ack.StreamID)
	require.Equal(t, len("echo:ls\n"), ack.Bytes)

	// The open entry exists while the session runs.
	open := r.store.audit(ActionOpen)
	require.Equal(t, "service", open.ResourceType)
	require.Equal(t, r.target.ServiceID.String(), open.ResourceID)
	require.Equal(t, 0, open.Metadata["replica"])
	require.Equal(t, "nc-web-abcd1234-0", open.Metadata["container"])
	require.Equal(t, "auto", open.Metadata["command"])
	require.Equal(t, false, open.Metadata["production"])

	writeJSON(t, c, map[string]any{"type": "close"})
	require.Equal(t, ReasonClientClosed, nextJSON(t, c, "closed").Reason)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the agent is told to kill the process")
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	require.Equal(t, []string{ActionOpen, ActionClose}, r.store.actions())
	cl := r.store.audit(ActionClose)
	require.Equal(t, ReasonClientClosed, cl.Metadata["reason"])
	require.Equal(t, 0, cl.Metadata["replica"])
	_, ok := cl.Metadata["durationSeconds"]
	require.True(t, ok, "the close entry carries the duration")

	// Nothing the user typed or the shell printed is recorded anywhere.
	for _, a := range r.store.audits {
		b, _ := json.Marshal(a)
		require.NotContains(t, string(b), "ls\\n")
		require.NotContains(t, string(b), "echo:")
	}
}

func TestSessionCommandIsAuditedByExecutableOnly(t *testing.T) {
	r := newRig(t, false)
	c := r.mustDial()
	m := openMsgFor(0, false)
	m["command"] = []string{"/usr/bin/psql", "postgres://admin:sekret-canary-12345@db/app"}
	writeJSON(t, c, m)
	nextJSON(t, c, "opened")
	var op agentprotocol.ExecOpen
	require.NoError(t, json.Unmarshal(r.agents.stream(0).sentOf(agentprotocol.TypeExecOpen)[0], &op))
	require.Equal(t, m["command"], op.Command, "the agent receives the full argv")
	require.Equal(t, "psql", r.store.audit(ActionOpen).Metadata["command"])
	b, _ := json.Marshal(r.store.audits)
	require.NotContains(t, string(b), "sekret-canary-12345", "arguments may hold secrets: the audit keeps the executable only")
}

func TestProductionNeedsConfirmation(t *testing.T) {
	r := newRig(t, true)
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	e := nextJSON(t, c, "error")
	require.Equal(t, CodeConfirmationRequired, e.Code)
	require.Zero(t, r.agents.count(), "nothing is opened on the agent without the confirmation")
	require.Empty(t, r.store.actions())

	c2 := r.mustDial()
	writeJSON(t, c2, openMsgFor(0, true))
	nextJSON(t, c2, "opened")
	require.Equal(t, true, r.store.audit(ActionOpen).Metadata["production"])
}

func TestSessionRefusals(t *testing.T) {
	t.Run("replica not running", func(t *testing.T) {
		r := newRig(t, false)
		c := r.mustDial()
		writeJSON(t, c, openMsgFor(1, false))
		require.Equal(t, CodeReplicaNotRunning, nextJSON(t, c, "error").Code)
		require.Zero(t, r.agents.count())
	})
	t.Run("not deployed", func(t *testing.T) {
		r := newRig(t, false)
		r.store.t.Deployed = false
		c := r.mustDial()
		writeJSON(t, c, openMsgFor(0, false))
		require.Equal(t, CodeNotDeployed, nextJSON(t, c, "error").Code)
	})
	t.Run("server offline", func(t *testing.T) {
		r := newRig(t, false)
		r.agents.err = agents.ErrNotConnected
		c := r.mustDial()
		writeJSON(t, c, openMsgFor(0, false))
		require.Equal(t, CodeServerOffline, nextJSON(t, c, "error").Code)
		require.Empty(t, r.store.actions())
	})
	t.Run("the agent refuses", func(t *testing.T) {
		r := newRig(t, false)
		r.agents.hook = func(fs *fakeStream) {
			fs.onSend = func(typ string, _ json.RawMessage) {
				if typ == agentprotocol.TypeExecOpen {
					fs.push(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: fs.id, Code: "failed", Error: "could not start a shell: no sh"})
				}
			}
		}
		c := r.mustDial()
		writeJSON(t, c, openMsgFor(0, false))
		e := nextJSON(t, c, "error")
		require.Equal(t, CodeOpenFailed, e.Code)
		require.Contains(t, e.Message, "no sh")
		require.Empty(t, r.store.actions(), "a session that never ran is not audited as opened")
	})
	t.Run("bad first message", func(t *testing.T) {
		r := newRig(t, false)
		c := r.mustDial()
		writeJSON(t, c, map[string]any{"type": "resize"})
		require.Equal(t, CodeInvalidRequest, nextJSON(t, c, "error").Code)
	})
	t.Run("oversized command", func(t *testing.T) {
		r := newRig(t, false)
		c := r.mustDial()
		m := openMsgFor(0, false)
		m["command"] = make([]string, MaxCommandArgs+1)
		writeJSON(t, c, m)
		require.Equal(t, CodeInvalidRequest, nextJSON(t, c, "error").Code)
	})
}

func TestAuditFailureFailsClosed(t *testing.T) {
	r := newRig(t, false)
	r.store.auditErr = errors.New("database down")
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	require.Equal(t, CodeOpenFailed, nextJSON(t, c, "error").Code)
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the unaudited process is killed")
}

func TestSessionLimitPerPrincipal(t *testing.T) {
	r := newRig(t, false)
	r.svc.maxSessions = 2
	var conns []*websocket.Conn
	for range 2 {
		c := r.mustDial()
		writeJSON(t, c, openMsgFor(0, false))
		nextJSON(t, c, "opened")
		conns = append(conns, c)
	}
	third := r.mustDial()
	writeJSON(t, third, openMsgFor(0, false))
	require.Equal(t, CodeTooManySessions, nextJSON(t, third, "error").Code)

	// Another principal has its own budget.
	p1 := r.who.Load()
	r.who.Store(devPrincipal())
	other := r.mustDial()
	writeJSON(t, other, openMsgFor(0, false))
	nextJSON(t, other, "opened")

	// Closing one frees a slot.
	writeJSON(t, conns[0], map[string]any{"type": "close"})
	nextJSON(t, conns[0], "closed")
	eventually(t, func() bool { return r.svc.activeSessions(p1.ID()) < 2 }, "slot released")
}

func TestIdleSessionIsClosedWithAWarning(t *testing.T) {
	r := newRig(t, false)
	r.svc.idle = 300 * time.Millisecond
	r.svc.idleWarning = 200 * time.Millisecond
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	nextJSON(t, c, "opened")
	w := nextJSON(t, c, "idle_warning")
	require.Greater(t, w.SecondsLeft, -1)
	require.Equal(t, ReasonIdle, nextJSON(t, c, "closed").Reason)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	require.Equal(t, ReasonIdle, r.store.audit(ActionClose).Metadata["reason"])
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the process is killed")
}

func TestInputKeepsTheSessionAlive(t *testing.T) {
	r := newRig(t, false)
	r.svc.idle = 400 * time.Millisecond
	r.svc.idleWarning = 100 * time.Millisecond
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	nextJSON(t, c, "opened")
	deadline := time.Now().Add(900 * time.Millisecond) // more than twice the idle timeout
	for time.Now().Before(deadline) {
		require.NoError(t, c.Write(context.Background(), websocket.MessageBinary, []byte("x")))
		typ, _ := next(t, c)
		_ = typ
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, []string{ActionOpen}, r.store.actions(), "still open: typing resets the idle timer")
}

func TestShellExitIsReportedAndAudited(t *testing.T) {
	r := newRig(t, false)
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	nextJSON(t, c, "opened")
	st := r.agents.stream(0)
	st.push(agentprotocol.TypeExecOutput, agentprotocol.ExecOutput{SessionID: st.id, Data: []byte("bye\n")})
	st.push(agentprotocol.TypeExecExit, agentprotocol.ExecExit{SessionID: st.id, ExitCode: 7})
	typ, data := next(t, c)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Equal(t, "bye\n", string(data), "output that preceded the exit is delivered first")
	require.Equal(t, 7, *nextJSON(t, c, "exit").ExitCode)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	cl := r.store.audit(ActionClose)
	require.Equal(t, ReasonExit, cl.Metadata["reason"])
	require.Equal(t, 7, cl.Metadata["exitCode"])
	require.Empty(t, st.sentOf(agentprotocol.TypeExecClose), "an exited process needs no kill")
}

func TestAgentDisconnectEndsTheSession(t *testing.T) {
	r := newRig(t, false)
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	nextJSON(t, c, "opened")
	r.agents.stream(0).drop()
	require.Equal(t, ReasonAgentDisconnected, nextJSON(t, c, "closed").Reason)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	require.Equal(t, ReasonAgentDisconnected, r.store.audit(ActionClose).Metadata["reason"])
}

func TestBrowserDisconnectKillsTheProcess(t *testing.T) {
	r := newRig(t, false)
	c := r.mustDial()
	writeJSON(t, c, openMsgFor(0, false))
	nextJSON(t, c, "opened")
	_ = c.CloseNow()
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "process killed")
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	eventually(t, func() bool { return r.svc.activeSessions(r.who.Load().ID()) == 0 }, "the session slot is released")
}

// ---- access control ----

func TestTerminalAccessMatrix(t *testing.T) {
	r := newRig(t, false)
	status := func() int {
		_, resp, err := r.dial(nil)
		if err == nil {
			t.Fatalf("expected the handshake to be refused")
		}
		return resp.StatusCode
	}

	r.who.Store(principal(auth.RoleViewer, []string{policy.ScopeProjectsRead, policy.ScopeServersRead}))
	require.Equal(t, http.StatusForbidden, status(), "a Viewer never gets a terminal")

	r.who.Store(principal(auth.RoleDeveloper, []string{policy.ScopeProjectsRead}))
	require.Equal(t, http.StatusForbidden, status(), "without deployments:write there is no terminal")

	r.who.Store(principal(auth.RoleDeveloper, policy.AllScopes, uuid.New()))
	require.Equal(t, http.StatusNotFound, status(), "a principal restricted to other projects cannot even see the service")

	r.who.Store(nil)
	require.Equal(t, http.StatusUnauthorized, status())

	r.who.Store(principal("", policy.AllScopes))
	require.Equal(t, http.StatusForbidden, status(), "a principal without a team role (system) is refused")

	for _, role := range []string{auth.RoleOwner, auth.RoleAdmin, auth.RoleDeveloper} {
		r.who.Store(principal(role, policy.AllScopes))
		c, _, err := r.dial(nil)
		require.NoError(t, err, role)
		_ = c.CloseNow()
	}
}

func TestForeignServiceIs404(t *testing.T) {
	r := newRig(t, false)
	resp, err := http.Get(r.srv.URL + "/services/" + uuid.NewString() + "/files/content?path=/etc/hosts")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCrossOriginSocketIsRefused(t *testing.T) {
	r := newRig(t, false)
	_, resp, err := r.dial(&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "a page of another site must not open a terminal with the session cookie")
	require.Zero(t, r.agents.count())
}

func TestRouteServicesHaveNoTerminal(t *testing.T) {
	r := newRig(t, false)
	r.store.t.Kind = "route"
	_, resp, err := r.dial(nil)
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

// ---- file browser ----

func fsAgent(handle func(fs *fakeStream, req agentprotocol.FSRequest)) func(*fakeStream) {
	return func(fs *fakeStream) {
		fs.onSend = func(typ string, raw json.RawMessage) {
			if typ != agentprotocol.TypeFSRequest {
				return
			}
			var req agentprotocol.FSRequest
			_ = json.Unmarshal(raw, &req)
			handle(fs, req)
		}
	}
}

func get(t *testing.T, url string) (int, []byte, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func TestListFiles(t *testing.T) {
	r := newRig(t, false)
	var got agentprotocol.FSRequest
	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		got = req
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: fs.id, Stage: agentprotocol.FSDone, Path: "/app",
			Entries: []agentprotocol.FSEntry{
				{Name: "src", Type: agentprotocol.FSTypeDir, Mode: "0755", MTime: 1700000000},
				{Name: "main.go", Type: agentprotocol.FSTypeFile, Size: 42, Mode: "0644"},
			},
		})
	})
	v, err := r.svc.ListFiles(auth.WithPrincipal(context.Background(), devPrincipal()), r.target.ServiceID, "", 0, "/app/../app")
	require.NoError(t, err)
	require.Equal(t, agentprotocol.FSList, got.Op)
	require.Equal(t, "/app", got.Path, "the path is normalized before it reaches the agent")
	require.Equal(t, r.target.ServiceID.String(), got.ServiceID)
	require.Equal(t, "/app", v.Path)
	require.Len(t, v.Entries, 2)
	require.Equal(t, "src", v.Entries[0].Name)
	require.NotNil(t, v.Entries[0].ModifiedAt)
	require.True(t, v.Writable)
	require.Equal(t, 1, r.agents.count())
	st := r.agents.stream(0)
	select {
	case <-st.Done():
	default:
		t.Fatal("the stream is closed after the listing")
	}
}

func TestListFilesErrors(t *testing.T) {
	r := newRig(t, false)
	ctx := auth.WithPrincipal(context.Background(), devPrincipal())
	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		code := agentprotocol.FSErrNotFound
		if strings.HasSuffix(req.Path, "file") {
			code = agentprotocol.FSErrNotDir
		}
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Code: code, Error: "x"})
	})
	_, err := r.svc.ListFiles(ctx, r.target.ServiceID, "", 0, "/missing")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, ae.Status)
	_, err = r.svc.ListFiles(ctx, r.target.ServiceID, "", 0, "/etc/file")
	ae, _ = apierr.As(err)
	require.Equal(t, "not_a_directory", ae.Code)

	for _, bad := range []string{"", "relative", "/a\x00b"} {
		_, err = r.svc.ListFiles(ctx, r.target.ServiceID, "", 0, bad)
		ae, ok = apierr.As(err)
		require.True(t, ok, bad)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, bad)
	}
	_, err = r.svc.ListFiles(ctx, r.target.ServiceID, "", 1, "/")
	ae, _ = apierr.As(err)
	require.Equal(t, CodeReplicaNotRunning, ae.Code, "a stopped replica has no files to browse")
}

func TestFilesNeedTheTerminalRole(t *testing.T) {
	r := newRig(t, false)
	viewer := auth.WithPrincipal(context.Background(), principal(auth.RoleViewer, []string{policy.ScopeProjectsRead}))
	_, err := r.svc.ListFiles(viewer, r.target.ServiceID, "", 0, "/")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
	_, err = r.svc.Info(viewer, r.target.ServiceID)
	ae, _ = apierr.As(err)
	require.Equal(t, http.StatusForbidden, ae.Status)
	require.Zero(t, r.agents.count(), "a forbidden caller never reaches the agent")
}

func TestDownloadStreamsAndAuditsTheRead(t *testing.T) {
	r := newRig(t, false)
	body := bytes.Repeat([]byte("0123456789"), 30_000) // 300 kB
	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: fs.id, Stage: agentprotocol.FSReady, Path: req.Path,
			Entry: &agentprotocol.FSEntry{Name: "app.log", Type: agentprotocol.FSTypeFile, Size: int64(len(body))},
		})
		for off := 0; off < len(body); off += agentprotocol.FSChunk {
			end := min(off+agentprotocol.FSChunk, len(body))
			fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: fs.id, Data: body[off:end]})
		}
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: fs.id, EOF: true})
	})
	status, got, h := get(t, r.url("/files/content?path=/var/log/app.log"))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, body, got)
	require.Contains(t, h.Get("Content-Disposition"), "attachment")
	require.Contains(t, h.Get("Content-Disposition"), "app.log")
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	e := r.store.audit(ActionFileRead)
	require.Equal(t, "/var/log/app.log", e.Metadata["path"])
	require.EqualValues(t, len(body), e.Metadata["size"])
	require.NotContains(t, e.Metadata, "content")
	// The ack of the last chunk is sent after the chunk was written to the
	// response, so it can land just after the client read the whole body.
	acked := func() int {
		n := 0
		for _, raw := range r.agents.stream(0).sentOf(agentprotocol.TypeStreamAck) {
			var a agentprotocol.StreamAck
			require.NoError(t, json.Unmarshal(raw, &a))
			n += a.Bytes
		}
		return n
	}
	require.Eventually(t, func() bool { return acked() == len(body) }, 5*time.Second, 10*time.Millisecond,
		"every byte is credited back to the agent")

	// Preview is inline text, capped by the agent through MaxBytes.
	var maxBytes atomic.Int64
	r.agents.hook = fsAgent(func(fs *fakeStream, rq agentprotocol.FSRequest) {
		maxBytes.Store(rq.MaxBytes)
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Code: agentprotocol.FSErrTooLarge, Error: "file is too big"})
	})
	status, _, _ = get(t, r.url("/files/content?path=/var/log/app.log&preview=true"))
	require.Equal(t, http.StatusRequestEntityTooLarge, status)
	require.EqualValues(t, PreviewLimit, maxBytes.Load())
}

func TestDownloadAbortedWhenTheAgentFailsMidway(t *testing.T) {
	r := newRig(t, false)
	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: fs.id, Stage: agentprotocol.FSReady, Path: req.Path,
			Entry: &agentprotocol.FSEntry{Name: "x", Type: agentprotocol.FSTypeFile, Size: 100},
		})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: fs.id, Data: []byte("only ten b")})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: fs.id, Error: "read file: broken"})
	})
	resp, err := http.Get(r.url("/files/content?path=/x"))
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	require.Error(t, err, "a truncated download must fail, not look complete")
}

// An agent that sends more than it announced, or a frame above the chunk size,
// must not get its bytes relayed to the browser.
func TestDownloadRefusesMoreThanTheAgentAnnounced(t *testing.T) {
	for name, data := range map[string][]byte{
		"past the announced size": bytes.Repeat([]byte("x"), 20),
		"above the chunk size":    bytes.Repeat([]byte("x"), agentprotocol.FSChunk+1),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, false)
			size := int64(10)
			if len(data) > agentprotocol.FSChunk {
				size = int64(len(data)) // announced honestly; the single frame is the problem
			}
			r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
				fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
					RequestID: fs.id, Stage: agentprotocol.FSReady, Path: req.Path,
					Entry: &agentprotocol.FSEntry{Name: "x", Type: agentprotocol.FSTypeFile, Size: size},
				})
				fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: fs.id, Data: data})
				fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: fs.id, EOF: true})
			})
			resp, err := http.Get(r.url("/files/content?path=/x"))
			var got []byte
			if err == nil {
				got, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			require.Error(t, err, "the download must fail, not look complete")
			require.Empty(t, got, "nothing of the oversized frame reaches the browser")
		})
	}
}

func upload(t *testing.T, r *rig, query string, body []byte) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(r.url("/files/upload?"+query), "application/octet-stream", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

// uploadAgent plays the agent side of an upload: ready, ack every chunk,
// final response at EOF.
func uploadAgent(collected *bytes.Buffer, mu *sync.Mutex, fail string) func(*fakeStream) {
	return func(fs *fakeStream) {
		fs.onSend = func(typ string, raw json.RawMessage) {
			switch typ {
			case agentprotocol.TypeFSRequest:
				var req agentprotocol.FSRequest
				_ = json.Unmarshal(raw, &req)
				fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Stage: agentprotocol.FSReady, Path: req.Path})
			case agentprotocol.TypeFSData:
				var d agentprotocol.FSData
				_ = json.Unmarshal(raw, &d)
				mu.Lock()
				collected.Write(d.Data)
				mu.Unlock()
				if len(d.Data) > 0 {
					fs.push(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: fs.id, Bytes: len(d.Data)})
				}
				if d.EOF {
					if fail != "" {
						fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Code: agentprotocol.FSErrFailed, Error: fail})
						return
					}
					fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Stage: agentprotocol.FSDone, Path: "/data/in.bin"})
				}
			}
		}
	}
}

func TestUploadStreamsTheBody(t *testing.T) {
	r := newRig(t, false)
	var buf bytes.Buffer
	var mu sync.Mutex
	r.agents.hook = uploadAgent(&buf, &mu, "")
	payload := bytes.Repeat([]byte("abcdefgh"), 100_000) // 800 kB: several chunks
	status, res := upload(t, r, "path=/data&name=in.bin", payload)
	require.Equal(t, http.StatusCreated, status, res)
	require.Equal(t, "/data/in.bin", res["path"])
	require.EqualValues(t, len(payload), res["size"])
	mu.Lock()
	require.Equal(t, payload, buf.Bytes())
	mu.Unlock()
	e := r.store.audit(ActionFileUpload)
	require.Equal(t, "/data/in.bin", e.Metadata["path"])
	require.EqualValues(t, len(payload), e.Metadata["size"])
	var req agentprotocol.FSRequest
	require.NoError(t, json.Unmarshal(r.agents.stream(0).sentOf(agentprotocol.TypeFSRequest)[0], &req))
	require.Equal(t, agentprotocol.FSWrite, req.Op)
	require.EqualValues(t, len(payload), req.Size)
}

func TestUploadRefusals(t *testing.T) {
	r := newRig(t, false)
	var buf bytes.Buffer
	var mu sync.Mutex
	r.agents.hook = uploadAgent(&buf, &mu, "")

	status, res := upload(t, r, "path=/data&name=../evil", []byte("x"))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_name", res["code"])
	status, res = upload(t, r, "path=data&name=x", []byte("x"))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "invalid_path", res["code"])
	require.Zero(t, r.agents.count())

	// Over the limit: refused before anything is sent (the Content-Length says so).
	req, _ := http.NewRequest(http.MethodPost, r.url("/files/upload?path=/data&name=big"), io.NopCloser(strings.NewReader("")))
	req.ContentLength = agentprotocol.MaxUploadBytes + 1
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	}
	require.Zero(t, r.agents.count())

	// A failure the agent reports comes back as an error, not a success.
	r.agents.hook = uploadAgent(&buf, &mu, "no space left on device")
	status, res = upload(t, r, "path=/data&name=full", []byte("data"))
	require.GreaterOrEqual(t, status, 400)
	require.Contains(t, res["detail"], "no space left")
}

func TestUploadIsReadOnlyInProductionUnlessAllowed(t *testing.T) {
	r := newRig(t, true)
	var buf bytes.Buffer
	var mu sync.Mutex

	// Reads still work (listing reports the read-only state).
	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Stage: agentprotocol.FSDone, Path: req.Path})
	})
	v, err := r.svc.ListFiles(auth.WithPrincipal(context.Background(), devPrincipal()), r.target.ServiceID, "", 0, "/")
	require.NoError(t, err)
	require.False(t, v.Writable)
	require.Contains(t, v.ReadOnlyReason, "read-only in production")

	r.agents.hook = uploadAgent(&buf, &mu, "")
	n := r.agents.count()
	status, res := upload(t, r, "path=/data&name=x", []byte("x"))
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, CodeProductionReadOnly, res["code"])
	require.Equal(t, n, r.agents.count(), "the agent never hears about a refused upload")
	require.Empty(t, r.store.actions())

	r.store.prodWrites = true // an admin allowed writes for the project
	status, _ = upload(t, r, "path=/data&name=x", []byte("x"))
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, true, r.store.audit(ActionFileUpload).Metadata["production"])
}

func TestInfo(t *testing.T) {
	r := newRig(t, true)
	p := devPrincipal()
	ctx := auth.WithPrincipal(context.Background(), p)
	v, err := r.svc.Info(ctx, r.target.ServiceID)
	require.NoError(t, err)
	require.True(t, v.Available)
	require.True(t, v.Production)
	require.Len(t, v.Replicas, 2)
	require.Equal(t, "hetzner-01", v.ServerName)
	require.Equal(t, MaxSessionsPerUser, v.MaxSessions)
	require.Equal(t, int(IdleTimeout.Seconds()), v.IdleTimeoutSeconds)
	require.False(t, v.FileWrites.Allowed)
	require.EqualValues(t, agentprotocol.MaxUploadBytes, v.MaxUploadBytes)

	r.store.t.Deployed = false
	v, err = r.svc.Info(ctx, r.target.ServiceID)
	require.NoError(t, err)
	require.False(t, v.Available)
	require.NotEmpty(t, v.Reason)
}

func TestSettingsAreAdminOnly(t *testing.T) {
	r := newRig(t, true)
	pid := r.target.ProjectID
	dev := auth.WithPrincipal(context.Background(), devPrincipal())
	_, err := r.svc.SetSettings(dev, pid, SettingsView{AllowProductionFileWrites: true})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
	require.False(t, r.store.prodWrites)

	admin := auth.WithPrincipal(context.Background(), principal(auth.RoleAdmin, policy.AllScopes))
	got, err := r.svc.SetSettings(admin, pid, SettingsView{AllowProductionFileWrites: true})
	require.NoError(t, err)
	require.True(t, got.AllowProductionFileWrites)
	require.Equal(t, ActionSettingsUpdate, r.store.audit(ActionSettingsUpdate).Action)
	read, err := r.svc.Settings(dev, pid)
	require.NoError(t, err)
	require.True(t, read.AllowProductionFileWrites)
}

// ---- pure helpers ----

func TestValidateCommand(t *testing.T) {
	require.NoError(t, validateCommand(nil))
	require.NoError(t, validateCommand([]string{"bash", "-l"}))
	require.Error(t, validateCommand([]string{""}))
	require.Error(t, validateCommand(make([]string, MaxCommandArgs+1)))
	require.Error(t, validateCommand([]string{"sh", strings.Repeat("a", MaxCommandArgLen+1)}))
	require.Equal(t, "auto", commandLabel(nil))
	require.Equal(t, "bash", commandLabel([]string{"/bin/bash", "-c", "secret"}))
}

func TestCleanPathAndNames(t *testing.T) {
	got, err := cleanPath("/a/../b//c/")
	require.NoError(t, err)
	require.Equal(t, "/b/c", got)
	for _, bad := range []string{"", "x", "./x", "/a\x00", "/" + strings.Repeat("a", maxPathLen)} {
		_, err := cleanPath(bad)
		require.Error(t, err, bad)
	}
	for _, ok := range []string{"a.txt", ".env", "with space"} {
		require.NoError(t, validName(ok), ok)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00", strings.Repeat("a", maxNameLen+1)} {
		require.Error(t, validName(bad), bad)
	}
}

func TestReplicasOf(t *testing.T) {
	multi := replicasOf(json.RawMessage(`{"serviceId":"s","state":"running","health":"degraded","replicas":[{"index":0,"state":"running","health":"healthy"},{"index":1,"state":"exited","health":"unhealthy"}]}`))
	require.Equal(t, []ReplicaView{
		{Index: 0, State: "running", Health: "healthy", Running: true},
		{Index: 1, State: "exited", Health: "unhealthy"},
	}, multi)
	single := replicasOf(json.RawMessage(`{"serviceId":"s","state":"running","health":"healthy"}`))
	require.Equal(t, []ReplicaView{{Index: 0, State: "running", Health: "healthy", Running: true}}, single)
	require.Empty(t, replicasOf(nil))
	require.Empty(t, replicasOf(json.RawMessage(`not json`)))
}

func TestSessionSlots(t *testing.T) {
	s := NewService(Deps{})
	s.maxSessions = 2
	id := uuid.New()
	a, b := s.acquire(id), s.acquire(id)
	require.NotNil(t, a)
	require.NotNil(t, b)
	require.Nil(t, s.acquire(id))
	a()
	a() // releasing twice frees one slot only
	require.NotNil(t, s.acquire(id))
	require.Nil(t, s.acquire(id))
	require.NotNil(t, s.acquire(uuid.New()))
}

// ---- processes ----

// newProcessRig is a rig whose service runs the default process web and the
// process worker; web has one replica running and one exited, worker two running.
func newProcessRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, false)
	r.store.t.DefaultProcess = "web"
	r.store.t.Processes = []string{"web", "worker"}
	r.store.t.Replicas = []ReplicaView{
		{Index: 0, Process: "web", State: "running", Running: true},
		{Index: 1, Process: "web", State: "exited"},
		{Index: 0, Process: "worker", State: "running", Running: true},
		{Index: 1, Process: "worker", State: "running", Running: true},
	}
	return r
}

func TestRequireRunningByProcess(t *testing.T) {
	tg := newProcessRig(t).store.t

	// "" and the default process' own name are the same process.
	require.NoError(t, tg.requireRunning("", 0))
	require.NoError(t, tg.requireRunning("web", 0))
	require.NoError(t, tg.requireRunning("worker", 1))

	// Replica indexes count within the process: web has no running replica 1.
	for _, p := range []string{"", "web"} {
		ae, ok := apierr.As(tg.requireRunning(p, 1))
		require.True(t, ok)
		require.Equal(t, CodeReplicaNotRunning, ae.Code)
	}
	ae, ok := apierr.As(tg.requireRunning("worker", 2))
	require.True(t, ok)
	require.Equal(t, CodeReplicaNotRunning, ae.Code)
	require.Contains(t, ae.Message, `process "worker"`)

	// A process the service does not have is not found, not "not running".
	ae, ok = apierr.As(tg.requireRunning("cron", 0))
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, ae.Status)
	require.Equal(t, CodeProcessNotFound, ae.Code)
	require.Contains(t, ae.Message, `"cron"`)

	// A configured process that has not been deployed yet has nothing running.
	tg.Processes = append(tg.Processes, "clock")
	ae, _ = apierr.As(tg.requireRunning("clock", 0))
	require.Equal(t, CodeReplicaNotRunning, ae.Code)

	// A process dropped from the config that still runs stays reachable.
	tg.Processes = []string{"web"}
	require.NoError(t, tg.requireRunning("worker", 0))

	// A plain service has no processes at all, not even one named after a default.
	plain := newRig(t, false).store.t
	require.NoError(t, plain.requireRunning("", 0))
	ae, _ = apierr.As(plain.requireRunning("web", 0))
	require.Equal(t, CodeProcessNotFound, ae.Code)

	// Replica bounds and route services are checked as before.
	ae, _ = apierr.As(tg.requireRunning("worker", -1))
	require.Equal(t, "invalid_replica", ae.Code)
	route := tg
	route.Kind = "route"
	ae, _ = apierr.As(route.requireRunning("", 0))
	require.Equal(t, "no_container", ae.Code)
}

func TestReplicasOfKeepsProcess(t *testing.T) {
	got := replicasOf(json.RawMessage(`{"serviceId":"s","state":"running","health":"healthy","replicas":[` +
		`{"index":0,"process":"web","state":"running","health":"healthy"},` +
		`{"index":0,"process":"worker","state":"exited","health":"unhealthy"}]}`))
	require.Equal(t, []ReplicaView{
		{Index: 0, Process: "web", State: "running", Health: "healthy", Running: true},
		{Index: 0, Process: "worker", State: "exited", Health: "unhealthy"},
	}, got)
}

func TestSessionOpensTheNamedProcess(t *testing.T) {
	r := newProcessRig(t)
	c := r.mustDial()
	m := openMsgFor(1, false)
	m["process"] = "worker"
	writeJSON(t, c, m)
	opened := nextJSON(t, c, "opened")
	require.Equal(t, "worker", opened.Process)
	require.Equal(t, 1, *opened.Replica)

	var op agentprotocol.ExecOpen
	require.NoError(t, json.Unmarshal(r.agents.stream(0).sentOf(agentprotocol.TypeExecOpen)[0], &op))
	require.Equal(t, "worker", op.Process)
	require.Equal(t, 1, op.Replica, "the replica index counts within the process")
	require.Equal(t, "worker", r.store.audit(ActionOpen).Metadata["process"])

	writeJSON(t, c, map[string]any{"type": "close"})
	nextJSON(t, c, "closed")
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	require.Equal(t, "worker", r.store.audit(ActionClose).Metadata["process"])
}

func TestSessionProcessFromTheQueryAndTheDefaultName(t *testing.T) {
	r := newProcessRig(t)

	// The upgrade request's ?process= applies when the open message names none.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.url("/terminal/ws?process=worker"), "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.CloseNow() })
	writeJSON(t, c, openMsgFor(0, false))
	require.Equal(t, "worker", nextJSON(t, c, "opened").Process)
	var op agentprotocol.ExecOpen
	require.NoError(t, json.Unmarshal(r.agents.stream(0).sentOf(agentprotocol.TypeExecOpen)[0], &op))
	require.Equal(t, "worker", op.Process)

	// The default process is sent as "": the agent matches it with or without a label.
	c2 := r.mustDial()
	m := openMsgFor(0, false)
	m["process"] = "web"
	writeJSON(t, c2, m)
	nextJSON(t, c2, "opened")
	var op2 agentprotocol.ExecOpen
	require.NoError(t, json.Unmarshal(r.agents.stream(1).sentOf(agentprotocol.TypeExecOpen)[0], &op2))
	require.Equal(t, "", op2.Process)
}

func TestSessionUnknownProcessIsRefused(t *testing.T) {
	r := newProcessRig(t)
	c := r.mustDial()
	m := openMsgFor(0, false)
	m["process"] = "cron"
	writeJSON(t, c, m)
	e := nextJSON(t, c, "error")
	require.Equal(t, CodeProcessNotFound, e.Code)
	require.Contains(t, e.Message, `"cron"`)
	require.Zero(t, r.agents.count(), "no stream is opened for a process the service does not have")

	// A process whose replica is not running is the existing replica_not_running.
	c2 := r.mustDial()
	m = openMsgFor(1, false)
	m["process"] = "web"
	writeJSON(t, c2, m)
	require.Equal(t, CodeReplicaNotRunning, nextJSON(t, c2, "error").Code)
}

func TestFilesAddressAProcessReplica(t *testing.T) {
	r := newProcessRig(t)
	var got []agentprotocol.FSRequest
	r.agents.hook = fsAgent(func(fs *fakeStream, req agentprotocol.FSRequest) {
		got = append(got, req)
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: fs.id, Stage: agentprotocol.FSDone, Path: req.Path})
	})
	ctx := auth.WithPrincipal(context.Background(), devPrincipal())

	_, err := r.svc.ListFiles(ctx, r.target.ServiceID, "worker", 1, "/app")
	require.NoError(t, err)
	require.Equal(t, "worker", got[0].Process)
	require.Equal(t, 1, got[0].Replica)

	_, err = r.svc.ListFiles(ctx, r.target.ServiceID, "web", 0, "/app")
	require.NoError(t, err)
	require.Equal(t, "", got[1].Process, "the default process goes to the agent unnamed")

	_, err = r.svc.ListFiles(ctx, r.target.ServiceID, "cron", 0, "/app")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, ae.Status)
	require.Len(t, got, 2, "an unknown process never reaches the agent")

	// The HTTP endpoints take ?process= next to ?replica=.
	code, _, _ := get(t, r.url("/files/content?process=cron&replica=0&path=/app/x"))
	require.Equal(t, http.StatusNotFound, code)
	code, _, _ = get(t, r.url("/files/content?process=worker&replica=2&path=/app/x"))
	require.Equal(t, http.StatusConflict, code, "worker has no replica 2")
	require.Len(t, got, 2)
}
