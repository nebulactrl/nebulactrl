package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// useFakeDocker points DOCKER_HOST at a daemon that answers the ping and an
// empty container list and rejects every other call: enough for run() to get
// past its start-up check and say hello (the hello lists the managed
// containers), while the managed containers (proxy, tunnel, mesh) simply never
// come up.
func useFakeDocker(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.52")
			w.Header().Set("OSType", "linux")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = io.WriteString(w, "OK")
			}
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "[]")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"fake daemon: not implemented"}`)
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	t.Setenv("DOCKER_HOST", "tcp://"+srv.Listener.Addr().String())
	t.Setenv("DOCKER_API_VERSION", "1.52")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
}

// fakeControlPlane serves /agent/v1/enroll and /agent/v1/connect.
type fakeControlPlane struct {
	srv *httptest.Server

	mu       sync.Mutex
	enrolled []agentprotocol.EnrollRequest
	pub      ed25519.PublicKey
	status   int // enroll response status (0 = 200)
	// knownAgent, when set, is the only agent ID this control plane knows: any
	// other one is closed with 4404 (unknown agent), the way a control plane
	// that lost its database or never enrolled the agent answers.
	knownAgent string
	unknown    chan struct{} // one entry per agent turned away

	hellos chan agentprotocol.Hello
	// script runs after the handshake; it gets the live socket.
	script func(ctx context.Context, ws *websocket.Conn)
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	cp := &fakeControlPlane{hellos: make(chan agentprotocol.Hello, 8), unknown: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/enroll", cp.enroll)
	mux.HandleFunc("/agent/v1/connect", cp.connect)
	cp.srv = httptest.NewServer(mux)
	t.Cleanup(cp.srv.Close)
	return cp
}

func (cp *fakeControlPlane) enroll(w http.ResponseWriter, r *http.Request) {
	var req agentprotocol.EnrollRequest
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &req)
	cp.mu.Lock()
	cp.enrolled = append(cp.enrolled, req)
	cp.pub = ed25519.PublicKey(req.PublicKey)
	status := cp.status
	cp.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"detail":"enrollment token already used"}`)
		return
	}
	_, _ = io.WriteString(w, `{"agentId":"agent-1","serverId":"server-1"}`)
}

func (cp *fakeControlPlane) connect(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	ch := agentprotocol.Challenge{Nonce: []byte("0123456789abcdef0123456789abcdef"), ServerTime: time.Now().UnixNano()}
	b, _ := agentprotocol.Encode(agentprotocol.TypeChallenge, "", ch)
	if ws.Write(ctx, websocket.MessageText, b) != nil {
		return
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		return
	}
	f, _ := agentprotocol.Decode(data)
	var auth agentprotocol.Auth
	_ = f.Into(&auth)
	cp.mu.Lock()
	known := cp.knownAgent
	cp.mu.Unlock()
	if known != "" && auth.AgentID != known {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseUnknownAgent), "unknown agent")
		cp.unknown <- struct{}{}
		return
	}
	cp.mu.Lock()
	pub := cp.pub
	cp.mu.Unlock()
	if pub == nil || !ed25519.Verify(pub, agentprotocol.SigningPayload(ch.Nonce, auth.AgentID, ch.ServerTime), auth.Signature) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseAuthFailed), "bad signature")
		return
	}
	_, data, err = ws.Read(ctx)
	if err != nil {
		return
	}
	f, _ = agentprotocol.Decode(data)
	var hello agentprotocol.Hello
	_ = f.Into(&hello)
	cp.hellos <- hello
	if cp.script != nil {
		cp.script(ctx, ws)
	}
}

func runOptions(t *testing.T, cp *fakeControlPlane, token string) options {
	t.Helper()
	return options{
		URL:            cp.srv.URL,
		InsecureDev:    true,
		Token:          token,
		StateDir:       t.TempDir(),
		ProxyAdminPort: agentprotocol.ProxyAdminPort,
		ProxyHTTPPort:  80,
		ProxyHTTPSPort: 443,
		NetworkPool:    netpool.DefaultPool,
		NetworkPrefix:  netpool.DefaultPrefixLen,
		UptimeInterval: agentprotocol.UptimeInterval,
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func runAsync(ctx context.Context, o options) <-chan error {
	res := make(chan error, 1)
	go func() { res <- run(ctx, quietLogger(), o) }()
	return res
}

func waitFor[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatal("timed out")
		panic("unreachable")
	}
}

func openState(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRunEnrollsConnectsAndStopsCleanlyOnCancel(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.script = func(ctx context.Context, ws *websocket.Conn) { <-ctx.Done() }
	o := runOptions(t, cp, "tok-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := runAsync(ctx, o)

	hello := waitFor(t, cp.hellos)
	assert.Equal(t, version, hello.AgentVersion)
	assert.Equal(t, agentprotocol.ProtocolVersion, hello.ProtocolVersion)
	assert.NotEmpty(t, hello.Facts.OS, "the hello carries the host facts")

	cp.mu.Lock()
	require.Len(t, cp.enrolled, 1)
	assert.Equal(t, "tok-1", cp.enrolled[0].Token)
	cp.mu.Unlock()

	cancel()
	require.NoError(t, waitFor(t, res), "a cancelled run is a clean stop")

	id, ok, err := openState(t, o.StateDir).Identity()
	require.NoError(t, err)
	require.True(t, ok, "the identity from enrollment is persisted")
	assert.Equal(t, "agent-1", id.AgentID)
	assert.Equal(t, "server-1", id.ServerID)
	assert.False(t, id.Revoked)
}

func TestRunReusesAStoredIdentityWithoutEnrolling(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.script = func(ctx context.Context, ws *websocket.Conn) { <-ctx.Done() }
	o := runOptions(t, cp, "")

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	cp.pub = pub
	st := openState(t, o.StateDir)
	require.NoError(t, st.PutIdentity(store.Identity{AgentID: "agent-1", ServerID: "server-1", PrivateKey: priv}))
	require.NoError(t, st.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := runAsync(ctx, o)
	waitFor(t, cp.hellos)
	cancel()
	require.NoError(t, waitFor(t, res))
	cp.mu.Lock()
	defer cp.mu.Unlock()
	assert.Empty(t, cp.enrolled, "a stored identity needs no enrollment token")
}

func TestRunMarksTheIdentityRevokedWhenTheControlPlaneRevokesIt(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.script = func(_ context.Context, ws *websocket.Conn) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseRevoked), "revoked")
	}
	o := runOptions(t, cp, "tok-1")

	err := waitFor(t, runAsync(context.Background(), o))
	require.ErrorIs(t, err, conn.ErrRevoked)

	id, ok, serr := openState(t, o.StateDir).Identity()
	require.NoError(t, serr)
	require.True(t, ok)
	assert.True(t, id.Revoked, "the next start must not reuse a revoked identity")
}

func TestRunRefusesARevokedIdentityWithoutAToken(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	o := runOptions(t, cp, "")
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	st := openState(t, o.StateDir)
	require.NoError(t, st.PutIdentity(store.Identity{AgentID: "a", ServerID: "s", PrivateKey: priv, Revoked: true}))
	require.NoError(t, st.Close())

	err = waitFor(t, runAsync(context.Background(), o))
	require.ErrorIs(t, err, conn.ErrRevoked)
	assert.Empty(t, cp.hellos, "a revoked agent never reconnects")
	cp.mu.Lock()
	defer cp.mu.Unlock()
	assert.Empty(t, cp.enrolled)
}

func TestRunReenrollsARevokedIdentityWhenGivenAToken(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.script = func(ctx context.Context, ws *websocket.Conn) { <-ctx.Done() }
	o := runOptions(t, cp, "fresh-token")
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	st := openState(t, o.StateDir)
	require.NoError(t, st.PutIdentity(store.Identity{AgentID: "old", ServerID: "old", PrivateKey: priv, Revoked: true}))
	require.NoError(t, st.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := runAsync(ctx, o)
	waitFor(t, cp.hellos)
	cancel()
	require.NoError(t, waitFor(t, res))

	id, ok, err := openState(t, o.StateDir).Identity()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "agent-1", id.AgentID, "the revoked identity was replaced")
	assert.False(t, id.Revoked)
}

func TestRunWithoutIdentityOrTokenExplainsTheTokenFlag(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	err := waitFor(t, runAsync(context.Background(), runOptions(t, cp, "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--token")
}

func TestRunReportsARejectedEnrollmentToken(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.status = http.StatusUnauthorized
	o := runOptions(t, cp, "used-token")
	err := waitFor(t, runAsync(context.Background(), o))
	require.ErrorIs(t, err, conn.ErrEnrollRejected)
	assert.Contains(t, err.Error(), "enrollment token already used")

	_, ok, serr := openState(t, o.StateDir).Identity()
	require.NoError(t, serr)
	assert.False(t, ok, "no identity is stored after a rejected enrollment")
}

func TestRunEndsWithUnsupportedVersionWhenTheControlPlaneRejectsTheProtocol(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.script = func(_ context.Context, ws *websocket.Conn) {
		_ = ws.Close(websocket.StatusCode(agentprotocol.CloseUnsupportedVersion), "too old")
	}
	err := waitFor(t, runAsync(context.Background(), runOptions(t, cp, "tok")))
	require.ErrorIs(t, err, conn.ErrUnsupportedVersion)
}

func TestRunFailsWhenTheStateDirCannotBeOpened(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	o := runOptions(t, cp, "tok")
	o.StateDir = "/dev/null/state"
	err := waitFor(t, runAsync(context.Background(), o))
	require.ErrorContains(t, err, "create state dir")
}

// storeGhostIdentity writes the identity of a control plane that no longer
// knows it, plus the state a running proxy keeps: a route table, its number
// and the admin secret. Re-enrolling must change the identity and nothing else.
func storeGhostIdentity(t *testing.T, dir string) []agentprotocol.Route {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	routes := []agentprotocol.Route{{ServiceID: "svc-1", Hosts: []string{"old.example.com"}, Upstreams: []string{"nc-old-abcdef12-0:80"}}}
	st := openState(t, dir)
	require.NoError(t, st.PutIdentity(store.Identity{AgentID: "ghost", ServerID: "ghost-server", PrivateKey: priv}))
	require.NoError(t, st.SaveRoutes(routes))
	require.NoError(t, st.SaveTableSeq(7))
	require.NoError(t, st.SaveProxyAdminSecret("admin-secret"))
	require.NoError(t, st.Close())
	return routes
}

func TestRunReenrollsWithTheTokenWhenTheControlPlaneDoesNotKnowTheIdentity(t *testing.T) {
	clearAgentEnv(t)
	useFakeDocker(t)
	cp := newFakeControlPlane(t)
	cp.knownAgent = "agent-1"
	o := runOptions(t, cp, "new-token")
	routes := storeGhostIdentity(t, o.StateDir)

	err := waitFor(t, runAsync(context.Background(), o))
	require.ErrorIs(t, err, errUpgradeRestart, "the process restarts to use the new identity")
	<-cp.unknown

	cp.mu.Lock()
	require.Len(t, cp.enrolled, 1)
	assert.Equal(t, "new-token", cp.enrolled[0].Token)
	cp.mu.Unlock()

	st := openState(t, o.StateDir)
	id, ok, err := st.Identity()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "agent-1", id.AgentID, "the identity the control plane did not know was replaced")
	assert.Equal(t, "server-1", id.ServerID)
	assert.False(t, id.Revoked)
	got, err := st.LoadRoutes()
	require.NoError(t, err)
	assert.Equal(t, routes, got, "the route table survives: the proxy keeps serving")
	seq, err := st.LoadTableSeq()
	require.NoError(t, err)
	assert.EqualValues(t, 7, seq)
	secret, err := st.LoadProxyAdminSecret()
	require.NoError(t, err)
	assert.Equal(t, "admin-secret", secret)
}

func TestRunKeepsAnUnknownIdentityWithoutATokenOrWhenTheTokenIsRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		token  string
		status int
	}{
		"no token":       {token: ""},
		"rejected token": {token: "used-token", status: http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			clearAgentEnv(t)
			useFakeDocker(t)
			cp := newFakeControlPlane(t)
			cp.knownAgent, cp.status = "agent-1", tc.status
			o := runOptions(t, cp, tc.token)
			storeGhostIdentity(t, o.StateDir)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := runAsync(ctx, o)
			waitFor(t, cp.unknown)
			if tc.token != "" {
				require.Eventually(t, func() bool {
					cp.mu.Lock()
					defer cp.mu.Unlock()
					return len(cp.enrolled) == 1
				}, 10*time.Second, 5*time.Millisecond, "the token is tried once")
			}
			select {
			case err := <-res:
				t.Fatalf("run ended with %v; the agent keeps retrying with its identity", err)
			default:
			}
			cancel()
			require.NoError(t, waitFor(t, res))

			id, ok, err := openState(t, o.StateDir).Identity()
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, "ghost", id.AgentID, "the current identity is kept")
			assert.False(t, id.Revoked)
			if tc.token == "" {
				cp.mu.Lock()
				defer cp.mu.Unlock()
				assert.Empty(t, cp.enrolled)
			}
		})
	}
}
