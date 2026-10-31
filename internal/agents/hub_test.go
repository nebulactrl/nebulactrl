package agents_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ----

type undelivered struct {
	ServerID, CommandID uuid.UUID
	DeploymentID        *uuid.UUID
	Msg                 string
}

type fakeDomain struct {
	mu          sync.Mutex
	undelivered []undelivered
	results     []agentprotocol.ResultFrame
	events      []agentprotocol.EventFrame
}

func (f *fakeDomain) SecretsForCommand(_ context.Context, _ uuid.UUID, kind string, _ json.RawMessage) (map[string]string, error) {
	if kind == agentprotocol.KindDeploy {
		return map[string]string{"env:API_KEY": "sekret-canary-12345"}, nil
	}
	return nil, nil
}

func (f *fakeDomain) HandleResult(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, res agentprotocol.ResultFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, res)
	return nil
}

func (f *fakeDomain) HandleUndelivered(_ context.Context, sid, cid uuid.UUID, dep *uuid.UUID, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undelivered = append(f.undelivered, undelivered{sid, cid, dep, msg})
	return nil
}

func (f *fakeDomain) HandleEvent(_ context.Context, _ uuid.UUID, ev agentprotocol.EventFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeDomain) HandleObserved(context.Context, uuid.UUID, agentprotocol.Observed) error {
	return nil
}

func (f *fakeDomain) DesiredState(context.Context, uuid.UUID) (agentprotocol.Reconcile, error) {
	return agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{}}, nil
}

func (f *fakeDomain) RedactorFor(context.Context, uuid.UUID) (func(string) string, error) {
	return func(s string) string { return strings.ReplaceAll(s, "sekret-canary-12345", "••••••") }, nil
}

func (f *fakeDomain) undeliveredSnapshot() []undelivered {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]undelivered(nil), f.undelivered...)
}

// ---- harness ----

type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	hub    *agents.Hub
	svc    *servers.Service
	dom    *fakeDomain
	srv    *httptest.Server
	ws     uuid.UUID
	user   uuid.UUID
	ctx    context.Context
	bus    *events.Bus
	cancel context.CancelFunc
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	cfg := &config.Config{AgentBinariesDir: t.TempDir(), Listen: ":8080"}
	bus := events.NewBus()
	hub := agents.NewHub(pool, bus, nil, cfg, nil)
	t.Cleanup(hub.Close)
	dom := &fakeDomain{}
	hub.SetSecretSource(dom)
	hub.SetResultHandler(dom)
	hub.SetRedactor(dom)

	r := chi.NewRouter()
	hub.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	ws, err := q.CreateWorkspace(context.Background(), "test")
	require.NoError(t, err)
	u, err := q.CreateUser(context.Background(), db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)

	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "A", Scopes: policy.AllScopes,
	})
	return &harness{
		t: t, pool: pool, q: q, hub: hub, dom: dom, srv: srv, ws: ws.ID, user: u.ID, ctx: ctx, bus: bus,
		svc: servers.NewService(pool, bus, hub, cfg, nil),
	}
}

// enrollment is an enrolled fake agent.
type enrollment struct {
	serverID, agentID uuid.UUID
	priv              ed25519.PrivateKey
}

func (h *harness) newToken(name string) (server uuid.UUID, token string) {
	h.t.Helper()
	et, err := h.svc.CreateEnrollmentToken(h.ctx, servers.CreateTokenInput{Name: name})
	require.NoError(h.t, err)
	return et.Server.ID, et.Token
}

func (h *harness) postEnroll(token string, pub ed25519.PublicKey) (int, agentprotocol.EnrollResponse) {
	h.t.Helper()
	body, _ := json.Marshal(agentprotocol.EnrollRequest{Token: token, PublicKey: pub, Hostname: "host-1", Facts: agentprotocol.Facts{OS: "linux", CPUs: 4}})
	resp, err := http.Post(h.srv.URL+"/agent/v1/enroll", "application/json", bytes.NewReader(body))
	require.NoError(h.t, err)
	defer resp.Body.Close()
	var out agentprotocol.EnrollResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) enroll(name string) enrollment {
	h.t.Helper()
	sid, tok := h.newToken(name)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(h.t, err)
	code, out := h.postEnroll(tok, pub)
	require.Equal(h.t, http.StatusOK, code)
	require.Equal(h.t, sid.String(), out.ServerID)
	return enrollment{serverID: sid, agentID: uuid.MustParse(out.AgentID), priv: priv}
}

// agentConn is the fake agent's end of the socket.
type agentConn struct {
	t  *testing.T
	ws *websocket.Conn
}

// dial performs the handshake. tamper signs a different nonce.
func (h *harness) dial(e enrollment, tamper bool) *agentConn {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/agent/v1/connect", nil)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { ws.CloseNow() })
	ws.SetReadLimit(8 << 20)

	_, data, err := ws.Read(ctx)
	require.NoError(h.t, err)
	f, err := agentprotocol.Decode(data)
	require.NoError(h.t, err)
	require.Equal(h.t, agentprotocol.TypeChallenge, f.Type)
	var ch agentprotocol.Challenge
	require.NoError(h.t, f.Into(&ch))
	require.Len(h.t, ch.Nonce, 32)
	nonce := ch.Nonce
	if tamper {
		nonce = append([]byte(nil), nonce...)
		nonce[0] ^= 0xff
	}
	sig := ed25519.Sign(e.priv, agentprotocol.SigningPayload(nonce, e.agentID.String(), ch.ServerTime))
	ac := &agentConn{t: h.t, ws: ws}
	ac.write(agentprotocol.TypeAuth, agentprotocol.Auth{AgentID: e.agentID.String(), Signature: sig})
	// The server may already have closed the socket (rejected identity); the
	// outcome is then observed through closeStatus.
	data, err = agentprotocol.Encode(agentprotocol.TypeHello, "", agentprotocol.Hello{AgentVersion: "test", ProtocolVersion: agentprotocol.ProtocolVersion, Facts: agentprotocol.Facts{OS: "linux"}})
	require.NoError(h.t, err)
	_ = ws.Write(ctx, websocket.MessageText, data)
	return ac
}

func (a *agentConn) write(typ string, body any) {
	a.t.Helper()
	data, err := agentprotocol.Encode(typ, "", body)
	require.NoError(a.t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(a.t, a.ws.Write(ctx, websocket.MessageText, data))
}

// next reads frames until one of type typ arrives.
func (a *agentConn) next(typ string) agentprotocol.Frame {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, data, err := a.ws.Read(ctx)
		require.NoError(a.t, err, "waiting for %s", typ)
		f, err := agentprotocol.Decode(data)
		require.NoError(a.t, err)
		if f.Type == typ {
			return f
		}
	}
}

func (a *agentConn) closeStatus() websocket.StatusCode {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := a.ws.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func (h *harness) enqueue(serverID uuid.UUID, kind string, dep *uuid.UUID) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	err := db.InTx(h.ctx, h.pool, func(tx pgx.Tx) error {
		var err error
		id, err = h.hub.Enqueue(h.ctx, tx, agents.NewCommand{
			ServerID: serverID, DeploymentID: dep, Kind: kind, Payload: json.RawMessage(`{"serviceId":"x"}`),
		})
		return err
	})
	require.NoError(h.t, err)
	return id
}

func (h *harness) expireNow(id uuid.UUID) {
	h.t.Helper()
	_, err := h.pool.Exec(h.ctx, "UPDATE agent_commands SET expires_at = now() - interval '1 minute' WHERE id = $1", id)
	require.NoError(h.t, err)
}

func (h *harness) commandStatus(id uuid.UUID) string {
	h.t.Helper()
	c, err := h.q.GetAgentCommand(h.ctx, id)
	require.NoError(h.t, err)
	return c.Status
}

// ---- enrollment ----

func TestEnrollmentTokenExpiredIs401(t *testing.T) {
	h := newHarness(t)
	_, tok := h.newToken("edge-1")
	_, err := h.pool.Exec(h.ctx, "UPDATE enrollment_tokens SET expires_at = now() - interval '1 second'")
	require.NoError(t, err)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	code, _ := h.postEnroll(tok, pub)
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestEnrollmentTokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	_, tok := h.newToken("edge-1")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	code, out := h.postEnroll(tok, pub)
	require.Equal(t, http.StatusOK, code)
	require.NotEmpty(t, out.AgentID)

	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	code, _ = h.postEnroll(tok, pub2)
	require.Equal(t, http.StatusUnauthorized, code)

	// Unknown tokens are rejected the same way.
	code, _ = h.postEnroll("nct_enroll_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", pub2)
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestEnrollmentTokenIsStoredHashedAndFormatted(t *testing.T) {
	h := newHarness(t)
	_, tok := h.newToken("edge-1")
	require.Regexp(t, `^nct_enroll_[a-z2-7]{52}$`, tok)
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM enrollment_tokens WHERE token_hash = $1", agents.HashEnrollmentToken(tok)).Scan(&n))
	require.Equal(t, 1, n)
}

// ---- connect ----

func TestConnectMarksServerOnlineAndReplacesOldConnection(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	first := h.dial(e, false)
	first.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 20*time.Millisecond)
	s, err := h.q.GetServerByID(h.ctx, e.serverID)
	require.NoError(t, err)
	require.Equal(t, "online", s.Status)
	require.NotNil(t, s.AgentVersion)
	require.Equal(t, "test", *s.AgentVersion)

	second := h.dial(e, false)
	second.next(agentprotocol.TypeReconcile)
	require.EqualValues(t, agentprotocol.CloseReplaced, first.closeStatus())
	require.True(t, h.hub.Online(e.serverID))
}

func TestRevokedIdentityRejectedOnConnect(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	_, err := h.svc.Revoke(h.ctx, e.serverID)
	require.NoError(t, err)

	a := h.dial(e, false)
	require.EqualValues(t, agentprotocol.CloseRevoked, a.closeStatus())
	require.False(t, h.hub.Online(e.serverID))

	// Commands cannot be queued for a revoked server.
	err = db.InTx(h.ctx, h.pool, func(tx pgx.Tx) error {
		_, err := h.hub.Enqueue(h.ctx, tx, agents.NewCommand{ServerID: e.serverID, Kind: agentprotocol.KindRestartService})
		return err
	})
	require.ErrorContains(t, err, "server revoked")
}

func TestTamperedNonceFailsAuth(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, true)
	require.EqualValues(t, agentprotocol.CloseAuthFailed, a.closeStatus())
	require.False(t, h.hub.Online(e.serverID))
}

func TestRevokeDisconnectsLiveAgentAndCancelsPendingCommands(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	// Pending command that no agent ever received.
	_, dep := h.seedDeployment(e.serverID)
	id := h.enqueue(e.serverID, agentprotocol.KindRestartService, &dep)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	// The agent never acks, so the command is pending or sent: both are canceled.
	_, err := h.svc.Revoke(h.ctx, e.serverID)
	require.NoError(t, err)
	require.EqualValues(t, agentprotocol.CloseRevoked, a.closeStatus())
	require.Equal(t, "canceled", h.commandStatus(id))
	got := h.dom.undeliveredSnapshot()
	require.Len(t, got, 1)
	require.Equal(t, agents.RevokedMessage, got[0].Msg)
}

// ---- command delivery, expiry and results ----

func TestUnackedCommandExpiresToFailedWithNotDeliveredMessage(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1") // never connects
	_, dep := h.seedDeployment(e.serverID)
	id := h.enqueue(e.serverID, agentprotocol.KindRestartService, &dep)
	require.Equal(t, "pending", h.commandStatus(id))

	n, err := h.hub.ExpireCommands(h.ctx)
	require.NoError(t, err)
	require.Zero(t, n, "a fresh command must not expire")

	h.expireNow(id)
	n, err = h.hub.ExpireCommands(h.ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, "expired", h.commandStatus(id))
	got := h.dom.undeliveredSnapshot()
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].CommandID)
	require.Equal(t, "Not delivered: server offline. No changes were made.", got[0].Msg)
	require.Equal(t, dep, *got[0].DeploymentID)
}

func TestAckedCommandNeverExpires(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	id := h.enqueue(e.serverID, agentprotocol.KindRestartService, nil)

	f := a.next(agentprotocol.TypeCommand)
	var cmd agentprotocol.Command
	require.NoError(t, f.Into(&cmd))
	require.Equal(t, id.String(), cmd.ID)
	a.write(agentprotocol.TypeAck, agentprotocol.Ack{CommandID: cmd.ID})
	require.Eventually(t, func() bool { return h.commandStatus(id) == "acked" }, 5*time.Second, 20*time.Millisecond)

	h.expireNow(id)
	n, err := h.hub.ExpireCommands(h.ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, "acked", h.commandStatus(id))
	require.Empty(t, h.dom.undeliveredSnapshot())
}

// An agent that crashed after acking a command holds no result for it. On
// reconnect the control plane resends the command (even past its delivery
// deadline) so the agent can resume the work; a finished command is not resent.
func TestAckedCommandWithoutResultIsRedeliveredOnReconnect(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	id := h.enqueue(e.serverID, agentprotocol.KindRestartService, nil)

	var cmd agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&cmd))
	a.write(agentprotocol.TypeAck, agentprotocol.Ack{CommandID: cmd.ID})
	require.Eventually(t, func() bool { return h.commandStatus(id) == "acked" }, 5*time.Second, 20*time.Millisecond)

	a.ws.CloseNow() // the agent process died mid-command
	h.expireNow(id)

	a2 := h.dial(e, false)
	var again agentprotocol.Command
	require.NoError(t, a2.next(agentprotocol.TypeCommand).Into(&again))
	require.Equal(t, id.String(), again.ID)
	require.Equal(t, "acked", h.commandStatus(id), "resending must not change the status")

	a2.write(agentprotocol.TypeAck, agentprotocol.Ack{CommandID: again.ID})
	a2.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: again.ID, Outcome: agentprotocol.OutcomeSucceeded})
	require.Eventually(t, func() bool { return h.commandStatus(id) == "succeeded" }, 5*time.Second, 20*time.Millisecond)

	a2.ws.CloseNow()
	a3 := h.dial(e, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		_, data, err := a3.ws.Read(ctx)
		if err != nil {
			break // quiet for a second: nothing was resent
		}
		f, err := agentprotocol.Decode(data)
		require.NoError(t, err)
		require.NotEqual(t, agentprotocol.TypeCommand, f.Type, "a finished command must not be resent")
	}
}

func TestDeployCommandCarriesSecretsOnlyOnTheWire(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	_, dep := h.seedDeployment(e.serverID)
	id := h.enqueue(e.serverID, agentprotocol.KindDeploy, &dep)

	var cmd agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&cmd))
	require.Equal(t, "sekret-canary-12345", cmd.Secrets["env:API_KEY"])

	var payload, result string
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT payload::text, coalesce(result::text,'') FROM agent_commands WHERE id = $1", id).Scan(&payload, &result))
	require.NotContains(t, payload, "sekret-canary-12345")
	require.Eventually(t, func() bool { return h.commandStatus(id) == "sent" }, 5*time.Second, 20*time.Millisecond)

	// Result: stored, forwarded to the domain, acknowledged back to the agent.
	a.write(agentprotocol.TypeAck, agentprotocol.Ack{CommandID: cmd.ID})
	res := agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{"outcome":"succeeded"}`)}
	a.write(agentprotocol.TypeResult, res)
	var ack agentprotocol.ResultAck
	require.NoError(t, a.next(agentprotocol.TypeResultAck).Into(&ack))
	require.Equal(t, cmd.ID, ack.CommandID)
	require.Equal(t, "succeeded", h.commandStatus(id))
	h.dom.mu.Lock()
	require.Len(t, h.dom.results, 1)
	h.dom.mu.Unlock()

	// A duplicate (agent resent it) is acked again and does not corrupt state.
	a.write(agentprotocol.TypeResult, res)
	require.NoError(t, a.next(agentprotocol.TypeResultAck).Into(&ack))
	require.Equal(t, "succeeded", h.commandStatus(id))
}

func TestUnackedResultsInHelloAreApplied(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	id := h.enqueue(e.serverID, agentprotocol.KindRestartService, nil)
	_, err := h.pool.Exec(h.ctx, "UPDATE agent_commands SET status = 'acked', acked_at = now() WHERE id = $1", id)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/agent/v1/connect", nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	_, data, err := ws.Read(ctx)
	require.NoError(t, err)
	f, _ := agentprotocol.Decode(data)
	var ch agentprotocol.Challenge
	require.NoError(t, f.Into(&ch))
	ac := &agentConn{t: t, ws: ws}
	ac.write(agentprotocol.TypeAuth, agentprotocol.Auth{AgentID: e.agentID.String(), Signature: ed25519.Sign(e.priv, agentprotocol.SigningPayload(ch.Nonce, e.agentID.String(), ch.ServerTime))})
	ac.write(agentprotocol.TypeHello, agentprotocol.Hello{
		AgentVersion: "test", ProtocolVersion: agentprotocol.ProtocolVersion,
		UnackedResults: []agentprotocol.ResultFrame{{CommandID: id.String(), Outcome: agentprotocol.OutcomeFailed, Error: "boom"}},
	})
	var ack agentprotocol.ResultAck
	require.NoError(t, ac.next(agentprotocol.TypeResultAck).Into(&ack))
	require.Equal(t, id.String(), ack.CommandID)
	require.Equal(t, "failed", h.commandStatus(id))
}

// ---- heartbeat, stats, offline sweep ----

func TestHeartbeatPersistsMetricsAndOfflineSweepMarksServerOffline(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{
		CPUPercent: 12.5, MemUsed: 1 << 30, MemTotal: 4 << 30, DiskUsed: 10 << 30, DiskTotal: 100 << 30,
		DockerVersion: "29.0.0", Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyError, Error: "port 80 in use"},
	})
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.Latest != nil && s.Proxy.Status == "error"
	}, 5*time.Second, 20*time.Millisecond)
	s, err := h.svc.Get(h.ctx, e.serverID)
	require.NoError(t, err)
	require.Equal(t, "online", s.Status)
	require.Equal(t, "port 80 in use", s.Proxy.Error)
	require.EqualValues(t, 1<<30, s.Latest.MemUsed)
	require.EqualValues(t, 4<<30, s.Latest.MemTotal)
	require.Equal(t, "29.0.0", s.Facts.DockerVersion)

	res, err := h.svc.Metrics(h.ctx, e.serverID, "1h")
	require.NoError(t, err)
	require.Len(t, res.Points, 1)
	require.InDelta(t, 25.0, res.Points[0].MemPercent, 0.01)
	require.InDelta(t, 10.0, res.Points[0].DiskPercent, 0.01)
	require.InDelta(t, 12.5, res.Points[0].CPUMax, 0.01)
	require.Equal(t, 30, res.StepSeconds)
	require.Equal(t, 7, res.RetentionDays)
	_, err = h.svc.Metrics(h.ctx, e.serverID, "2h")
	require.Error(t, err)

	// Silent for longer than 30s -> offline.
	_, err = h.pool.Exec(h.ctx, "UPDATE servers SET last_seen_at = now() - interval '31 seconds' WHERE id = $1", e.serverID)
	require.NoError(t, err)
	n, err := h.hub.SweepOffline(h.ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	st, err := h.q.GetServerByID(h.ctx, e.serverID)
	require.NoError(t, err)
	require.Equal(t, "offline", st.Status)
}

// ---- logs ----

func (h *harness) seedDeployment(serverID uuid.UUID) (serviceID, deploymentID uuid.UUID) {
	h.t.Helper()
	var projectID, envID uuid.UUID
	require.NoError(h.t, h.pool.QueryRow(h.ctx, "INSERT INTO projects (workspace_id, name, slug) VALUES ($1, 'p', 'p') RETURNING id", h.ws).Scan(&projectID))
	require.NoError(h.t, h.pool.QueryRow(h.ctx, "INSERT INTO environments (project_id, name, slug) VALUES ($1, 'production', 'production') RETURNING id", projectID).Scan(&envID))
	require.NoError(h.t, h.pool.QueryRow(h.ctx, "INSERT INTO services (environment_id, name, slug, source_type, server_id) VALUES ($1, 'web', 'web', 'image', $2) RETURNING id", envID, serverID).Scan(&serviceID))
	require.NoError(h.t, h.pool.QueryRow(h.ctx, "INSERT INTO deployments (service_id, server_id, trigger, actor_type) VALUES ($1, $2, 'manual', 'user') RETURNING id", serviceID, serverID).Scan(&deploymentID))
	return serviceID, deploymentID
}

func TestDeploymentLogsAreRedactedCappedAndFollowable(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	follow, cancel := h.hub.FollowDeploymentLogs(context.Background(), depID)
	defer cancel()

	mk := func(n int, prefix string) agentprotocol.LogFrame {
		lines := make([]agentprotocol.LogLine, n)
		for i := range lines {
			lines[i] = agentprotocol.LogLine{TS: time.Now(), Text: fmt.Sprintf("%s %d token=sekret-canary-12345", prefix, i)}
		}
		return agentprotocol.LogFrame{DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: agentprotocol.StreamBuild, Lines: lines}
	}
	a.write(agentprotocol.TypeLog, mk(6000, "a"))
	a.write(agentprotocol.TypeLog, mk(6000, "b"))

	select {
	case fr := <-follow:
		require.Equal(t, svcID.String(), fr.ServiceID)
		require.Contains(t, fr.Lines[0].Text, "••••••")
		require.NotContains(t, fr.Lines[0].Text, "sekret")
	case <-time.After(5 * time.Second):
		t.Fatal("follower received nothing")
	}

	require.Eventually(t, func() bool {
		var n int
		_ = h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE deployment_id = $1", depID).Scan(&n)
		return n == agents.MaxDeploymentLogLines+1
	}, 10*time.Second, 50*time.Millisecond)

	var leaked, markers int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE line LIKE '%sekret%'").Scan(&leaked))
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE line = $1", agents.LogTruncatedMarker).Scan(&markers))
	require.Zero(t, leaked)
	require.Equal(t, 1, markers)

	// More lines after the cap are not stored and do not add a second marker.
	a.write(agentprotocol.TypeLog, mk(10, "c"))
	time.Sleep(300 * time.Millisecond)
	var total int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE deployment_id = $1", depID).Scan(&total))
	require.Equal(t, agents.MaxDeploymentLogLines+1, total)
}

// The debug stream has its own cap and marker: it neither counts toward the
// 10,000 ordinary lines nor lets a chatty trace grow without bound, and it is
// redacted like every other stream.
func TestDebugLogsAreRedactedAndCappedApartFromTheDeployLog(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	mk := func(stream string, n int, prefix string) agentprotocol.LogFrame {
		lines := make([]agentprotocol.LogLine, n)
		for i := range lines {
			lines[i] = agentprotocol.LogLine{TS: time.Now(), Text: fmt.Sprintf("%s %d token=sekret-canary-12345", prefix, i)}
		}
		return agentprotocol.LogFrame{DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: stream, Lines: lines}
	}
	count := func(stream string) (n int) {
		_ = h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE deployment_id = $1 AND stream = $2", depID, stream).Scan(&n)
		return n
	}
	a.write(agentprotocol.TypeLog, mk(agentprotocol.StreamDeploy, 20, "d"))
	a.write(agentprotocol.TypeLog, mk(agentprotocol.StreamDebug, 400, "x"))
	a.write(agentprotocol.TypeLog, mk(agentprotocol.StreamDebug, 400, "y"))

	require.Eventually(t, func() bool { return count(agentprotocol.StreamDebug) == agents.MaxDeploymentDebugLines+1 },
		10*time.Second, 50*time.Millisecond)
	require.Equal(t, 20, count(agentprotocol.StreamDeploy), "debug lines do not count toward the ordinary cap")

	var leaked, markers int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE line LIKE '%sekret%'").Scan(&leaked))
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE stream = 'debug' AND line = $1", agents.DebugLogTruncatedMarker).Scan(&markers))
	require.Zero(t, leaked)
	require.Equal(t, 1, markers)

	// More debug lines after the cap are dropped without a second marker.
	a.write(agentprotocol.TypeLog, mk(agentprotocol.StreamDebug, 10, "z"))
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, agents.MaxDeploymentDebugLines+1, count(agentprotocol.StreamDebug))
}

func TestLogsForADeploymentOnAnotherServerAreIgnored(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	other := h.enroll("edge-2")
	svcID, depID := h.seedDeployment(other.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{
		DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: agentprotocol.StreamDeploy,
		Lines: []agentprotocol.LogLine{{TS: time.Now(), Text: "spoofed"}},
	})
	time.Sleep(300 * time.Millisecond)
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs").Scan(&n))
	require.Zero(t, n)
}

// A build server's log lines of a deployment it builds are stored and redacted
// like the target's: the build server owns a build_image command of the
// deployment. A server without one stays ignored.
func TestLogsOfABuildServerThatOwnsACommandAreStored(t *testing.T) {
	h := newHarness(t)
	target := h.enroll("target")
	builder := h.enroll("builder")
	stranger := h.enroll("stranger")
	svcID, depID := h.seedDeployment(target.serverID)
	_, err := h.pool.Exec(h.ctx, `INSERT INTO agent_commands (server_id, deployment_id, kind, payload, idempotency_key, expires_at)
		VALUES ($1, $2, 'build_image', '{}', $3, now() + interval '15 minutes')`, builder.serverID, depID, uuid.NewString())
	require.NoError(t, err)

	frame := func(text string) agentprotocol.LogFrame {
		return agentprotocol.LogFrame{
			DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: agentprotocol.StreamBuild,
			Lines: []agentprotocol.LogLine{{TS: time.Now(), Text: text}},
		}
	}
	b := h.dial(builder, false)
	b.next(agentprotocol.TypeReconcile)
	b.write(agentprotocol.TypeLog, frame("step 1 token=sekret-canary-12345"))
	x := h.dial(stranger, false)
	x.next(agentprotocol.TypeReconcile)
	x.write(agentprotocol.TypeLog, frame("spoofed"))

	require.Eventually(t, func() bool {
		var n int
		_ = h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE deployment_id = $1", depID).Scan(&n)
		return n == 1
	}, 5*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	var line string
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT line FROM deployment_logs WHERE deployment_id = $1", depID).Scan(&line))
	require.Contains(t, line, "step 1")
	require.NotContains(t, line, "sekret")
	var spoofed int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs WHERE line = 'spoofed'").Scan(&spoofed))
	require.Zero(t, spoofed)
}

func TestSubscribeLogsStreamsAndUnsubscribes(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")

	_, _, err := h.hub.SubscribeLogs(context.Background(), e.serverID, agentprotocol.LogsSubscribe{ServiceID: uuid.NewString(), Tail: 10})
	require.Error(t, err, "offline server")

	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 20*time.Millisecond)

	ch, cancel, err := h.hub.SubscribeLogs(context.Background(), e.serverID, agentprotocol.LogsSubscribe{ServiceID: uuid.NewString(), Tail: 10, Follow: true})
	require.NoError(t, err)
	var cmd agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&cmd))
	require.Equal(t, agentprotocol.KindLogsSubscribe, cmd.Kind)
	var req agentprotocol.LogsSubscribe
	require.NoError(t, json.Unmarshal(cmd.Payload, &req))
	require.NotEmpty(t, req.StreamID)

	a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: req.StreamID, Stream: agentprotocol.StreamRuntime, Lines: []agentprotocol.LogLine{{TS: time.Now(), Text: "hello"}}})
	select {
	case fr := <-ch:
		require.Equal(t, "hello", fr.Lines[0].Text)
	case <-time.After(5 * time.Second):
		t.Fatal("no log frame")
	}
	// Ephemeral commands are never persisted.
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM agent_commands WHERE kind = 'logs_subscribe'").Scan(&n))
	require.Zero(t, n)

	cancel()
	var un agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&un))
	require.Equal(t, agentprotocol.KindLogsUnsubscribe, un.Kind)
	_, open := <-ch
	require.False(t, open)
}

// ---- install script and download ----

func TestInstallScriptAndDownload(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.srv.URL + "/install-agent.sh")
	require.NoError(t, err)
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	script := body.String()
	require.True(t, strings.HasPrefix(script, "#!/bin/sh"))
	require.Contains(t, script, "DEFAULT_URL='"+h.srv.URL+"'")
	require.Contains(t, script, "nebulactrl-agent.service")
	require.Contains(t, script, "Restart=always")
	require.Contains(t, script, "NEBULACTRL_ENROLL_TOKEN=")

	resp, err = http.Get(h.srv.URL + "/agent/v1/download/amd64")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, err = http.Get(h.srv.URL + "/agent/v1/download/riscv")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
