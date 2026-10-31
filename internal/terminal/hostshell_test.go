package terminal

import (
	"context"
	"encoding/json"
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

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ----

type fakeHostStore struct {
	mu       sync.Mutex
	srv      hostServer
	twoFA    map[uuid.UUID]bool
	admins   map[uuid.UUID]bool // instance admins
	audits   []audit.Entry
	auditErr error
}

func (s *fakeHostStore) hostServer(_ context.Context, id uuid.UUID) (hostServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.srv.ID {
		return hostServer{}, apierr.NotFound("server")
	}
	return s.srv, nil
}

func (s *fakeHostStore) setHostShell(_ context.Context, id, _ uuid.UUID, enabled bool, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.srv.ID {
		return apierr.NotFound("server")
	}
	s.srv.Enabled = enabled
	s.audits = append(s.audits, e)
	return nil
}

func (s *fakeHostStore) userHasTwoFactor(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.twoFA[id], nil
}

func (s *fakeHostStore) isInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admins[id], nil
}

func (s *fakeHostStore) record(_ context.Context, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditErr != nil {
		return s.auditErr
	}
	s.audits = append(s.audits, e)
	return nil
}

func (s *fakeHostStore) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.audits {
		out = append(out, a.Action)
	}
	return out
}

func (s *fakeHostStore) entry(action string) audit.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.audits {
		if a.Action == action {
			return a
		}
	}
	return audit.Entry{}
}

func (s *fakeHostStore) set(f func(*hostServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.srv)
}

type hostRig struct {
	t      *testing.T
	svc    *Service
	store  *fakeHostStore
	agents *fakeAgents
	srv    *httptest.Server
	who    atomic.Pointer[auth.Principal]
	server hostServer
}

// adminWith2FA is an Owner or Admin session whose account has 2FA.
func (r *hostRig) adminWith2FA(role string) *auth.Principal {
	p := principal(role, policy.AllScopes)
	r.store.mu.Lock()
	r.store.twoFA[p.UserID] = true
	r.store.mu.Unlock()
	return p
}

func newHostRig(t *testing.T, mutate ...func(*hostServer)) *hostRig {
	t.Helper()
	hs := hostServer{ID: uuid.New(), Name: "hetzner-01", Status: "online", Enabled: true}
	for _, m := range mutate {
		m(&hs)
	}
	r := &hostRig{t: t, store: &fakeHostStore{srv: hs, twoFA: map[uuid.UUID]bool{}, admins: map[uuid.UUID]bool{}}, agents: &fakeAgents{}, server: hs}
	r.svc = NewService(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r.svc.hs, r.svc.agents = r.store, r.agents
	r.svc.tick = 10 * time.Millisecond
	r.svc.openTimeout = 2 * time.Second
	r.who.Store(r.adminWith2FA(auth.RoleAdmin))

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
	r.agents.hook = func(fs *fakeStream) { fs.onSend = hostEchoAgent(fs) }
	return r
}

// hostEchoAgent plays an agent: it opens a host shell and echoes input.
func hostEchoAgent(fs *fakeStream) func(string, json.RawMessage) {
	return func(typ string, raw json.RawMessage) {
		switch typ {
		case agentprotocol.TypeExecOpen:
			fs.push(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: fs.id, Container: "host", Command: []string{"/bin/bash", "-l"}})
		case agentprotocol.TypeExecInput:
			var in agentprotocol.ExecInput
			_ = json.Unmarshal(raw, &in)
			fs.push(agentprotocol.TypeExecOutput, agentprotocol.ExecOutput{SessionID: fs.id, Data: append([]byte("echo:"), in.Data...)})
		}
	}
}

func (r *hostRig) url() string {
	return "ws" + strings.TrimPrefix(r.srv.URL, "http") + "/servers/" + r.server.ID.String() + "/shell/ws"
}

func (r *hostRig) dial(opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, r.url(), opts)
}

func (r *hostRig) mustDial() *websocket.Conn {
	r.t.Helper()
	c, _, err := r.dial(nil)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// open dials and opens a session; it returns the socket after "opened".
func (r *hostRig) open(confirm bool) *websocket.Conn {
	r.t.Helper()
	c := r.mustDial()
	writeJSON(r.t, c, map[string]any{"type": "open", "cols": 100, "rows": 30, "confirmProduction": confirm})
	nextJSON(r.t, c, "opened")
	return c
}

// refused dials and returns the HTTP status and problem code of a handshake
// that was refused before the upgrade.
func (r *hostRig) refused() (int, string) {
	r.t.Helper()
	c, resp, err := r.dial(nil)
	if err == nil {
		_ = c.CloseNow()
		return 0, ""
	}
	require.NotNil(r.t, resp, "the handshake must be refused with an HTTP response: %v", err)
	defer resp.Body.Close()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Code
}

func (r *hostRig) ctx() context.Context {
	return auth.WithPrincipal(context.Background(), r.who.Load())
}

// ---- opt-in ----

func TestHostShellIsOffUnlessAnAdminEnablesIt(t *testing.T) {
	r := newHostRig(t, func(s *hostServer) { s.Enabled = false })

	status, code := r.refused()
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, CodeHostShellDisabled, code)
	require.Zero(t, r.agents.count(), "nothing reaches the agent")
	require.Empty(t, r.store.actions(), "a refused attempt opens no session")

	info, err := r.svc.HostShellInfo(r.ctx(), r.server.ID)
	require.NoError(t, err)
	require.False(t, info.Enabled)
	require.False(t, info.Available)
	require.Contains(t, info.Reason, "off")

	// An admin switches it on: audited, and sessions open.
	out, err := r.svc.SetHostShell(r.ctx(), r.server.ID, HostShellSettingsView{Enabled: true})
	require.NoError(t, err)
	require.True(t, out.Enabled)
	require.Equal(t, []string{ActionHostShellEnable}, r.store.actions())
	require.Equal(t, r.server.ID.String(), r.store.entry(ActionHostShellEnable).ResourceID)
	info, err = r.svc.HostShellInfo(r.ctx(), r.server.ID)
	require.NoError(t, err)
	require.True(t, info.Available)
	r.open(false)
}

// The control-plane host holds the database and the secret key of every team:
// its shell is the instance admin's alone, even for a team Owner or Admin with
// 2FA.
func TestControlPlaneHostShellIsForTheInstanceAdminOnly(t *testing.T) {
	r := newHostRig(t, func(s *hostServer) { s.ControlPlane = true })

	// A team Admin with 2FA is refused everywhere: the socket, the setting, the info.
	status, code := r.refused()
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, CodeInstanceAdminRequired, code)
	require.Zero(t, r.agents.count(), "nothing reaches the agent")
	require.Empty(t, r.store.actions(), "a refused attempt opens no session")

	_, err := r.svc.SetHostShell(r.ctx(), r.server.ID, HostShellSettingsView{Enabled: false})
	require.Equal(t, http.StatusForbidden, statusOfErr(t, err))
	require.Empty(t, r.store.actions(), "the setting is not touched")

	info, err := r.svc.HostShellInfo(r.ctx(), r.server.ID)
	require.NoError(t, err)
	require.True(t, info.ControlPlane)
	require.True(t, info.Restricted)
	require.False(t, info.Available)
	require.Contains(t, info.Reason, "instance admin")

	// The instance admin gets through.
	r.store.mu.Lock()
	r.store.admins[r.who.Load().UserID] = true
	r.store.mu.Unlock()
	info, err = r.svc.HostShellInfo(r.ctx(), r.server.ID)
	require.NoError(t, err)
	require.True(t, info.ControlPlane)
	require.False(t, info.Restricted)
	require.True(t, info.Available)
	_, err = r.svc.SetHostShell(r.ctx(), r.server.ID, HostShellSettingsView{Enabled: true})
	require.NoError(t, err)
	r.open(false)
}

func TestSwitchingItOffEndsOpenSessionsAndRefusesNewOnes(t *testing.T) {
	r := newHostRig(t)
	c := r.open(false)
	_, err := r.svc.SetHostShell(r.ctx(), r.server.ID, HostShellSettingsView{Enabled: false})
	require.NoError(t, err)
	require.Equal(t, ReasonHostShellDisabled, nextJSON(t, c, "closed").Reason, "the browser is told why")
	eventually(t, func() bool { return len(r.store.actions()) == 3 }, "open, disable and close audited")
	require.Equal(t, ReasonHostShellDisabled, r.store.entry(ActionHostShellClose).Metadata["reason"], "and so is the audit log")
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the agent is told to kill the shell")
	status, code := r.refused()
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, CodeHostShellDisabled, code)
}

func TestHostShellSettingIsPerServer(t *testing.T) {
	r := newHostRig(t)
	_, err := r.svc.HostShellInfo(r.ctx(), uuid.New())
	require.Equal(t, http.StatusNotFound, statusOfErr(t, err), "a server outside the team is a 404")
	_, err = r.svc.SetHostShell(r.ctx(), uuid.New(), HostShellSettingsView{Enabled: true})
	require.Equal(t, http.StatusNotFound, statusOfErr(t, err))

	resp, err := http.Get(r.srv.URL + "/servers/" + uuid.NewString() + "/shell/ws")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func statusOfErr(t *testing.T, err error) int {
	t.Helper()
	require.Error(t, err)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	return ae.Status
}

// ---- who may ----

func TestHostShellRoleMatrix(t *testing.T) {
	r := newHostRig(t)
	for _, tc := range []struct {
		name   string
		p      func() *auth.Principal
		status int
		code   string
	}{
		{"owner", func() *auth.Principal { return r.adminWith2FA(auth.RoleOwner) }, 0, ""},
		{"admin", func() *auth.Principal { return r.adminWith2FA(auth.RoleAdmin) }, 0, ""},
		{"developer", func() *auth.Principal { return r.adminWith2FA(auth.RoleDeveloper) }, http.StatusForbidden, "forbidden"},
		{"viewer", func() *auth.Principal { return r.adminWith2FA(auth.RoleViewer) }, http.StatusForbidden, "forbidden"},
		{"no role", func() *auth.Principal { return r.adminWith2FA("") }, http.StatusForbidden, "forbidden"},
		{"admin without the servers scope", func() *auth.Principal {
			p := r.adminWith2FA(auth.RoleAdmin)
			p.Scopes = []string{policy.ScopeProjectsRead}
			return p
		}, http.StatusForbidden, "forbidden"},
		{"an API token of an admin", func() *auth.Principal {
			p := r.adminWith2FA(auth.RoleAdmin)
			p.Type = auth.TypeToken
			id := uuid.New()
			p.TokenID = &id
			return p
		}, http.StatusForbidden, CodeSessionRequired},
		{"an MCP principal of an admin", func() *auth.Principal {
			p := r.adminWith2FA(auth.RoleAdmin)
			p.Type = auth.TypeMCP
			return p
		}, http.StatusForbidden, CodeSessionRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.who.Store(tc.p())
			status, code := r.refused()
			require.Equal(t, tc.status, status)
			require.Equal(t, tc.code, code)

			// Configuring it needs the same.
			_, err := r.svc.SetHostShell(r.ctx(), r.server.ID, HostShellSettingsView{Enabled: true})
			_, infoErr := r.svc.HostShellInfo(r.ctx(), r.server.ID)
			if tc.status == 0 {
				require.NoError(t, err)
				require.NoError(t, infoErr)
			} else {
				require.Equal(t, http.StatusForbidden, statusOfErr(t, err))
				require.Equal(t, http.StatusForbidden, statusOfErr(t, infoErr))
			}
		})
	}
}

func TestHostShellNeedsTwoFactor(t *testing.T) {
	r := newHostRig(t)
	noTOTP := principal(auth.RoleOwner, policy.AllScopes) // never enrolled
	r.who.Store(noTOTP)

	status, code := r.refused()
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, CodeTwoFactorRequired, code)
	require.Zero(t, r.agents.count())
	require.Empty(t, r.store.actions())

	info, err := r.svc.HostShellInfo(r.ctx(), r.server.ID)
	require.NoError(t, err)
	require.False(t, info.TwoFactorEnabled)
	require.False(t, info.Available)
	require.Contains(t, info.Reason, "two-factor")

	// The same person once 2FA is on.
	r.store.mu.Lock()
	r.store.twoFA[noTOTP.UserID] = true
	r.store.mu.Unlock()
	r.open(false)
}

// ---- production ----

func TestProductionServerNeedsTheConfirmation(t *testing.T) {
	r := newHostRig(t, func(s *hostServer) { s.Production = true })
	info, err := r.svc.HostShellInfo(r.ctx(), r.server.ID)
	require.NoError(t, err)
	require.True(t, info.Production)

	c := r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 80, "rows": 24, "confirmProduction": false})
	require.Equal(t, CodeConfirmationRequired, nextJSON(t, c, "error").Code)
	require.Zero(t, r.agents.count(), "no shell is started without the confirmation")
	require.Empty(t, r.store.actions())

	c = r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 80, "rows": 24, "confirmProduction": true})
	nextJSON(t, c, "opened")
	eventually(t, func() bool { return len(r.store.actions()) == 1 }, "open audited")
	require.Equal(t, true, r.store.entry(ActionHostShellOpen).Metadata["production"])

	// A server without production services never asks.
	r.store.set(func(s *hostServer) { s.Production = false })
	r.open(false)
}

// ---- the session ----

func TestHostSessionRelaysAndAuditsOpenAndCloseWithoutIO(t *testing.T) {
	r := newHostRig(t)
	c := r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 100, "rows": 30, "confirmProduction": false})
	opened := nextJSON(t, c, "opened")
	require.Equal(t, "host", opened.Container)
	require.Equal(t, []string{"/bin/bash", "-l"}, opened.Command)

	st := r.agents.stream(0)
	var open agentprotocol.ExecOpen
	require.NoError(t, json.Unmarshal(st.sentOf(agentprotocol.TypeExecOpen)[0], &open))
	require.True(t, open.Host, "the agent is asked for the host, not a container")
	require.Empty(t, open.ServiceID)
	require.Empty(t, open.Command)
	require.Equal(t, 100, open.Cols)
	require.Equal(t, 30, open.Rows)

	require.NoError(t, c.Write(context.Background(), websocket.MessageBinary, []byte("cat /etc/shadow secret-canary-777\n")))
	typ, data := next(t, c)
	require.Equal(t, websocket.MessageBinary, typ)
	require.Equal(t, "echo:cat /etc/shadow secret-canary-777\n", string(data))
	writeJSON(t, c, map[string]any{"type": "resize", "cols": 140, "rows": 50})
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecResize)) == 1 }, "resize relayed")

	writeJSON(t, c, map[string]any{"type": "close"})
	require.Equal(t, ReasonClientClosed, nextJSON(t, c, "closed").Reason)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "open and close audited")
	require.Equal(t, []string{ActionHostShellOpen, ActionHostShellClose}, r.store.actions())

	op, cl := r.store.entry(ActionHostShellOpen), r.store.entry(ActionHostShellClose)
	for _, e := range []audit.Entry{op, cl} {
		require.Equal(t, "server", e.ResourceType)
		require.Equal(t, r.server.ID.String(), e.ResourceID)
		require.Equal(t, "hetzner-01", e.Metadata["server"])
		require.Equal(t, r.server.ID.String(), e.Metadata["serverId"])
		require.Equal(t, open.SessionID, e.Metadata["sessionId"])
	}
	require.Contains(t, cl.Metadata, "durationSeconds")
	require.Equal(t, ReasonClientClosed, cl.Metadata["reason"])
	all := op.Metadata
	for k, v := range cl.Metadata {
		all[k] = v
	}
	require.NotContains(t, jsonString(all), "secret-canary-777", "terminal I/O is never recorded")
	require.NotContains(t, jsonString(all), "shadow")
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the agent is told to end the shell")
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestHostShellTakesNoCommand(t *testing.T) {
	r := newHostRig(t)
	c := r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "command": []string{"rm", "-rf", "/"}, "cols": 80, "rows": 24})
	require.Equal(t, CodeInvalidRequest, nextJSON(t, c, "error").Code)
	require.Zero(t, r.agents.count())
}

func TestHostShellNeedsAnOnlineServer(t *testing.T) {
	r := newHostRig(t, func(s *hostServer) { s.Status = "offline" })
	c := r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 80, "rows": 24})
	require.Equal(t, CodeServerOffline, nextJSON(t, c, "error").Code)

	r = newHostRig(t)
	r.agents.err = io.ErrClosedPipe // the hub has no connection for the server
	c = r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 80, "rows": 24})
	require.Equal(t, CodeServerOffline, nextJSON(t, c, "error").Code)
	require.Empty(t, r.store.actions())
}

func TestHostShellFailureOnTheAgentIsReported(t *testing.T) {
	r := newHostRig(t)
	r.agents.hook = func(fs *fakeStream) {
		fs.onSend = func(typ string, _ json.RawMessage) {
			if typ == agentprotocol.TypeExecOpen {
				fs.push(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: fs.id, Code: "failed", Error: "could not start a host shell: no ptys left"})
			}
		}
	}
	c := r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 80, "rows": 24})
	e := nextJSON(t, c, "error")
	require.Equal(t, CodeOpenFailed, e.Code)
	require.Contains(t, e.Message, "no ptys left")
	require.Empty(t, r.store.actions(), "a shell that never started is not audited as opened")
}

func TestAuditFailureFailsClosedForTheHostShell(t *testing.T) {
	r := newHostRig(t)
	r.store.auditErr = io.ErrUnexpectedEOF
	c := r.mustDial()
	writeJSON(t, c, map[string]any{"type": "open", "cols": 80, "rows": 24})
	require.Equal(t, CodeOpenFailed, nextJSON(t, c, "error").Code)
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the unaudited shell is killed")
}

func TestHostShellIdleSessionIsClosedWithAWarning(t *testing.T) {
	r := newHostRig(t)
	require.Equal(t, 15*time.Minute, r.svc.idle, "the idle timeout is 15 minutes by default")
	r.svc.idle = 300 * time.Millisecond
	r.svc.idleWarning = 200 * time.Millisecond
	c := r.open(false)
	require.Greater(t, nextJSON(t, c, "idle_warning").SecondsLeft, -1)
	require.Equal(t, ReasonIdle, nextJSON(t, c, "closed").Reason)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	require.Equal(t, ReasonIdle, r.store.entry(ActionHostShellClose).Metadata["reason"])
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the shell is killed")
}

func TestHostShellSessionLimitIsTwoPerPerson(t *testing.T) {
	r := newHostRig(t)
	require.Equal(t, 2, r.svc.maxHostSessions, "at most 2 concurrent host shells per user")
	p1 := r.who.Load()
	a := r.open(false)
	r.open(false)
	require.Equal(t, 2, r.svc.activeHostSessions(p1.ID()))

	third := r.mustDial()
	writeJSON(t, third, map[string]any{"type": "open", "cols": 80, "rows": 24})
	require.Equal(t, CodeTooManySessions, nextJSON(t, third, "error").Code)
	require.Equal(t, 2, r.agents.count(), "the refused session never reaches the agent")

	// Another person has a budget of their own; the container terminal's budget is separate.
	r.who.Store(r.adminWith2FA(auth.RoleOwner))
	r.open(false)
	r.who.Store(p1)

	writeJSON(t, a, map[string]any{"type": "close"})
	nextJSON(t, a, "closed")
	eventually(t, func() bool { return r.svc.activeHostSessions(p1.ID()) == 1 }, "closing frees the slot")
	r.open(false)
}

func TestHostShellEndsWhenTheAgentDisconnects(t *testing.T) {
	r := newHostRig(t)
	c := r.open(false)
	r.agents.stream(0).drop()
	require.Equal(t, ReasonAgentDisconnected, nextJSON(t, c, "closed").Reason)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	require.Equal(t, ReasonAgentDisconnected, r.store.entry(ActionHostShellClose).Metadata["reason"])
	eventually(t, func() bool { return r.svc.activeHostSessions(r.who.Load().ID()) == 0 }, "slot released")
}

func TestClosingTheBrowserTabKillsTheHostShell(t *testing.T) {
	r := newHostRig(t)
	c := r.open(false)
	_ = c.CloseNow()
	st := r.agents.stream(0)
	eventually(t, func() bool { return len(st.sentOf(agentprotocol.TypeExecClose)) == 1 }, "the agent is told to kill the shell (and remove its helper container)")
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
}

func TestHostShellShellExitIsReported(t *testing.T) {
	r := newHostRig(t)
	c := r.open(false)
	st := r.agents.stream(0)
	st.push(agentprotocol.TypeExecExit, agentprotocol.ExecExit{SessionID: st.id, ExitCode: 3})
	require.Equal(t, 3, *nextJSON(t, c, "exit").ExitCode)
	eventually(t, func() bool { return len(r.store.actions()) == 2 }, "close audited")
	cl := r.store.entry(ActionHostShellClose)
	require.Equal(t, ReasonExit, cl.Metadata["reason"])
	require.Equal(t, 3, cl.Metadata["exitCode"])
}

func TestHostShellCrossOriginSocketIsRefused(t *testing.T) {
	r := newHostRig(t)
	_, resp, err := r.dial(&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Zero(t, r.agents.count())
}
