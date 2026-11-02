package agents_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// writeID sends a frame whose envelope carries id (a stream or command id).
func (a *agentConn) writeID(typ, id string, body any) {
	a.t.Helper()
	data, err := agentprotocol.Encode(typ, id, body)
	require.NoError(a.t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(a.t, a.ws.Write(ctx, websocket.MessageText, data))
}

// connected enrolls a server, dials it and waits until the hub sees it online.
func (h *harness) connected() (enrollment, *agentConn) {
	h.t.Helper()
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(h.t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
	return e, a
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		require.FailNow(t, "timed out waiting for "+what)
	}
	var zero T
	return zero
}

func waitDone(t *testing.T, s *agents.Stream) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the stream did not end")
	}
}

func TestStreamsRouteFramesBothWaysAndOnlyForTheirOwner(t *testing.T) {
	h := newHarness(t)
	e, a := h.connected()

	_, err := h.hub.OpenStream(uuid.New(), "nobody-home")
	require.ErrorIs(t, err, agents.ErrNotConnected, "a server that is offline has no streams")

	st, err := h.hub.OpenStream(e.serverID, "stream-1")
	require.NoError(t, err)
	_, err = h.hub.OpenStream(e.serverID, "stream-1")
	require.ErrorContains(t, err, "already in use")

	// Frames of the stream arrive in order; frames of other streams, of unknown
	// streams and of types only the control plane sends are dropped.
	a.writeID(agentprotocol.TypeExecOpened, "stream-1", map[string]any{"ok": true})
	a.writeID(agentprotocol.TypeExecOutput, "other-stream", map[string]any{"data": "lost"})
	a.writeID(agentprotocol.TypeExecInput, "stream-1", map[string]any{"data": "injected"})
	a.writeID(agentprotocol.TypeExecOutput, "stream-1", map[string]any{"data": "hello"})
	first := recv(t, st.Frames(), "the first stream frame")
	require.Equal(t, agentprotocol.TypeExecOpened, first.Type)
	require.Equal(t, "stream-1", first.ID)
	second := recv(t, st.Frames(), "the second stream frame")
	require.Equal(t, agentprotocol.TypeExecOutput, second.Type, "the injected input frame was dropped")
	var body struct {
		Data string `json:"data"`
	}
	require.NoError(t, second.Into(&body))
	require.Equal(t, "hello", body.Data)
	select {
	case extra := <-st.Frames():
		require.Failf(t, "unexpected frame", "%s %s", extra.Type, extra.ID)
	default:
	}

	// The owner's frames go to the agent under the stream's id.
	require.NoError(t, st.Send(agentprotocol.TypeExecInput, map[string]any{"data": "ls"}))
	got := a.next(agentprotocol.TypeExecInput)
	require.Equal(t, "stream-1", got.ID)

	// Closing ends it: no more sends, the id is free again, closing twice is fine.
	st.Close()
	waitDone(t, st)
	require.False(t, st.Lost(), "the owner closed it")
	require.Error(t, st.Send(agentprotocol.TypeExecInput, map[string]any{"data": "late"}))
	st.Close()
	a.writeID(agentprotocol.TypeExecOutput, "stream-1", map[string]any{"data": "after close"})
	again, err := h.hub.OpenStream(e.serverID, "stream-1")
	require.NoError(t, err)
	select {
	case f := <-again.Frames():
		require.Failf(t, "a closed stream's frame leaked into its successor", "%s", f.Type)
	default:
	}
	again.Close()
}

func TestStreamsEndAsLostWhenTheConnectionDropsOrTheAgentOverrunsThem(t *testing.T) {
	h := newHarness(t)
	e, a := h.connected()

	// An agent that never stops sending overruns the queue: the stream ends, lost.
	flood, err := h.hub.OpenStream(e.serverID, "flood")
	require.NoError(t, err)
	for range 300 {
		a.writeID(agentprotocol.TypeExecOutput, "flood", map[string]any{"data": "x"})
	}
	waitDone(t, flood)
	require.True(t, flood.Lost(), "overflowing the window is not a close by the owner")
	require.True(t, h.hub.Online(e.serverID), "the connection itself survives")

	// A dropped connection ends every stream of it.
	st, err := h.hub.OpenStream(e.serverID, "drop")
	require.NoError(t, err)
	require.NoError(t, a.ws.Close(websocket.StatusNormalClosure, "bye"))
	waitDone(t, st)
	require.True(t, st.Lost())
	require.Eventually(t, func() bool { return !h.hub.Online(e.serverID) }, 5*time.Second, 10*time.Millisecond)
	_, err = h.hub.OpenStream(e.serverID, "late")
	require.ErrorIs(t, err, agents.ErrNotConnected)
}

func TestInstallerScriptsAreServedAsShell(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/install.sh", "/uninstall.sh"} {
		resp, err := http.Get(h.srv.URL + path)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Equal(t, "text/x-shellscript; charset=utf-8", resp.Header.Get("Content-Type"), path)
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"), path)
		require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), path)
		require.True(t, len(body) > 100 && string(body[:2]) == "#!", "%s is a script: %q", path, body[:min(len(body), 40)])
	}
	first, err := http.Get(h.srv.URL + "/install.sh")
	require.NoError(t, err)
	a, _ := io.ReadAll(first.Body)
	_ = first.Body.Close()
	second, err := http.Get(h.srv.URL + "/install.sh")
	require.NoError(t, err)
	b, _ := io.ReadAll(second.Body)
	_ = second.Body.Close()
	require.Equal(t, a, b)
}

type scanSink struct {
	err   error
	scans chan agentprotocol.SourceScan
	srv   chan uuid.UUID
}

func (s *scanSink) HandleSourceScan(_ context.Context, serverID uuid.UUID, scan agentprotocol.SourceScan) error {
	s.scans <- scan
	s.srv <- serverID
	return s.err
}

func TestSourceScansReachEverySinkAndAFailingSinkDoesNotDropTheConnection(t *testing.T) {
	h := newHarness(t)
	ok := &scanSink{scans: make(chan agentprotocol.SourceScan, 4), srv: make(chan uuid.UUID, 4)}
	bad := &scanSink{scans: make(chan agentprotocol.SourceScan, 4), srv: make(chan uuid.UUID, 4), err: errors.New("boom")}
	h.hub.AddSourceScanSink(bad)
	h.hub.AddSourceScanSink(ok)
	e, a := h.connected()

	scan := agentprotocol.SourceScan{ServiceID: "svc-1", Commit: "abc123", Files: []agentprotocol.EnvFileScan{
		{Path: ".env.example", Vars: []agentprotocol.EnvVarHint{{Key: "A", Value: "1"}}},
	}}
	a.write(agentprotocol.TypeSourceScan, scan)
	for _, s := range []*scanSink{bad, ok} {
		got := recv(t, s.scans, "the scan")
		require.Equal(t, "svc-1", got.ServiceID)
		require.Equal(t, "abc123", got.Commit)
		require.Equal(t, ".env.example", got.Files[0].Path)
		require.Equal(t, e.serverID, recv(t, s.srv, "the server id"), "the sink knows which server reported it")
	}

	// The connection is still serving frames.
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{DockerVersion: "29.1.0"})
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.Facts.DockerVersion == "29.1.0" || err == nil && s.Latest != nil
	}, 5*time.Second, 20*time.Millisecond)
	require.True(t, h.hub.Online(e.serverID))
}

func TestHelloHooksRunWithTheAgentsHello(t *testing.T) {
	h := newHarness(t)
	type seen struct {
		server uuid.UUID
		hello  agentprotocol.Hello
	}
	ch := make(chan seen, 4)
	h.hub.OnHello(func(_ context.Context, id uuid.UUID, hello agentprotocol.Hello) { ch <- seen{id, hello} })
	h.hub.OnHello(func(context.Context, uuid.UUID, agentprotocol.Hello) { ch <- seen{} })
	e, _ := h.connected()

	got := []seen{recv(t, ch, "the first hook"), recv(t, ch, "the second hook")}
	var real seen
	for _, s := range got {
		if s.server != uuid.Nil {
			real = s
		}
	}
	require.Equal(t, e.serverID, real.server)
	require.Equal(t, "test", real.hello.AgentVersion)
	require.Equal(t, "linux", real.hello.Facts.OS)
}

type meshSeen struct {
	server uuid.UUID
	status *agentprotocol.MeshStatus
}

type meshObserver struct{ ch chan meshSeen }

func (m *meshObserver) ObserveHeartbeat(_ context.Context, id uuid.UUID, st *agentprotocol.MeshStatus) error {
	m.ch <- meshSeen{id, st}
	return nil
}

func TestHeartbeatsHandTheMeshStatusToTheMesh(t *testing.T) {
	h := newHarness(t)
	obs := &meshObserver{ch: make(chan meshSeen, 4)}
	h.hub.SetMeshObserver(obs)
	e, a := h.connected()

	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{Mesh: &agentprotocol.MeshStatus{TunDevice: true}})
	got := recv(t, obs.ch, "the mesh status")
	require.Equal(t, e.serverID, got.server)
	require.NotNil(t, got.status)
	require.True(t, got.status.TunDevice)

	// A heartbeat without mesh status tells the mesh the agent runs none.
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{})
	got = recv(t, obs.ch, "the empty mesh status")
	require.Nil(t, got.status)
}

func TestResultsOfCommandsThatAreNotQueuedOrNotTheirs(t *testing.T) {
	h := newHarness(t)
	e, a := h.connected()
	other := h.enroll("edge-2")

	// A result for a command that is not persisted (a log subscription) is
	// acknowledged without touching the domain.
	ephemeral := uuid.New().String()
	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: ephemeral, Outcome: agentprotocol.OutcomeSucceeded})
	var ack agentprotocol.ResultAck
	require.NoError(t, a.next(agentprotocol.TypeResultAck).Into(&ack))
	require.Equal(t, ephemeral, ack.CommandID)

	// A result naming another server's command is ignored: the command stays as it was.
	foreign := h.enqueue(other.serverID, agentprotocol.KindRestartService, nil)
	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: foreign.String(), Outcome: agentprotocol.OutcomeSucceeded})
	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: "not-a-uuid", Outcome: agentprotocol.OutcomeSucceeded})
	// Frames are applied in order, so once this heartbeat is visible both results were seen.
	a.write(agentprotocol.TypeHeartbeat, agentprotocol.Heartbeat{DockerVersion: "29.2.0"})
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.Facts.DockerVersion == "29.2.0"
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, "pending", h.commandStatus(foreign))
	h.dom.mu.Lock()
	defer h.dom.mu.Unlock()
	require.Empty(t, h.dom.results)
}

func TestExpireAndSweepWorkersDoTheirJobs(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1") // never connects
	_, dep := h.seedDeployment(e.serverID)
	id := h.enqueue(e.serverID, agentprotocol.KindRestartService, &dep)
	h.expireNow(id)

	require.NoError(t, h.hub.WorkExpireCommandsForTest(h.ctx))
	require.Equal(t, "expired", h.commandStatus(id))
	require.Len(t, h.dom.undeliveredSnapshot(), 1)
	require.NoError(t, h.hub.WorkExpireCommandsForTest(h.ctx), "nothing left to expire")
	require.Len(t, h.dom.undeliveredSnapshot(), 1)

	_, err := h.pool.Exec(h.ctx, "UPDATE servers SET status = 'online', last_seen_at = now() - interval '5 minutes' WHERE id = $1", e.serverID)
	require.NoError(t, err)
	require.NoError(t, h.hub.WorkOfflineSweepForTest(h.ctx))
	st, err := h.q.GetServerByID(h.ctx, e.serverID)
	require.NoError(t, err)
	require.Equal(t, "offline", st.Status)

	// Registering the jobs makes both kinds insertable.
	jm := jobs.New(h.pool, nil)
	h.hub.RegisterJobs(jm)
	require.NoError(t, jm.Insert(h.ctx, agents.ExpireCommandsArgs{}, nil))
	require.NoError(t, jm.Insert(h.ctx, agents.OfflineSweepArgs{}, nil))
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM river_job WHERE kind IN ('expire_commands', 'offline_sweep')").Scan(&n))
	require.Equal(t, 2, n)
}
