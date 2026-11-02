package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// These tests drive the log intake (runtime log subscriptions, deployment
// followers, task run frames) over the DB-free connection rig of
// localtunnel_test.go: the reader goroutine of a real hub connection, a raw
// WebSocket as the agent.

func recvLog(t *testing.T, ch <-chan agentprotocol.LogFrame) agentprotocol.LogFrame {
	t.Helper()
	select {
	case f, ok := <-ch:
		require.True(t, ok, "the stream closed instead of delivering a frame")
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no log frame")
		return agentprotocol.LogFrame{}
	}
}

func requireLogClosed(t *testing.T, ch <-chan agentprotocol.LogFrame) {
	t.Helper()
	select {
	case f, ok := <-ch:
		require.False(t, ok, "expected the stream to be closed, got a frame: %+v", f)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was not closed")
	}
}

func requireLogOpenAndEmpty(t *testing.T, ch <-chan agentprotocol.LogFrame) {
	t.Helper()
	select {
	case f, ok := <-ch:
		t.Fatalf("expected an open, empty stream; got frame=%+v ok=%v", f, ok)
	default:
	}
}

func logLine(text string) []agentprotocol.LogLine {
	return []agentprotocol.LogLine{{TS: time.Now(), Text: text}}
}

// subscribed is a runtime log subscription on a rig agent.
type subscribed struct {
	ch       <-chan agentprotocol.LogFrame
	cancel   func()
	streamID string
	cmd      agentprotocol.Command
}

func subscribeOn(t *testing.T, r *tunnelRig, a *testAgent, ctx context.Context, streamID string) subscribed {
	t.Helper()
	ch, cancel, err := r.h.SubscribeLogs(ctx, a.serverID, agentprotocol.LogsSubscribe{ServiceID: uuid.NewString(), Follow: true, StreamID: streamID})
	require.NoError(t, err)
	var cmd agentprotocol.Command
	require.NoError(t, a.nextControl(agentprotocol.TypeCommand).Into(&cmd))
	require.Equal(t, agentprotocol.KindLogsSubscribe, cmd.Kind)
	var req agentprotocol.LogsSubscribe
	require.NoError(t, json.Unmarshal(cmd.Payload, &req))
	return subscribed{ch: ch, cancel: cancel, streamID: req.StreamID, cmd: cmd}
}

func TestSubscribeLogsUsesTheCallersStreamIDAndSendsAnEphemeralCommand(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})
	before := time.Now()
	s := subscribeOn(t, r, a, context.Background(), "my-stream")
	require.Equal(t, "my-stream", s.streamID)
	require.Equal(t, s.cmd.ID, s.cmd.IdempotencyKey)
	require.True(t, s.cmd.ExpiresAt.After(before), "the command is live for a while")
	require.True(t, s.cmd.ExpiresAt.Before(before.Add(2*time.Minute)), "but not forever")
	r.h.subMu.Lock()
	require.Contains(t, r.h.subs, "my-stream")
	r.h.subMu.Unlock()
}

func TestRuntimeLogFramesReachOnlyTheSubscriptionOfTheAgentThatOwnsIt(t *testing.T) {
	r := newTunnelRig(t)
	a, b := r.dial(agentOpts{}), r.dial(agentOpts{})
	subA := subscribeOn(t, r, a, context.Background(), "")
	subB := subscribeOn(t, r, b, context.Background(), "")

	// B tries to feed and to end A's stream; B's own marker proves both were processed.
	b.sendText(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: subA.streamID, Stream: agentprotocol.StreamRuntime, Lines: logLine("spoofed")})
	b.sendText(agentprotocol.TypeLogEnd, agentprotocol.LogEnd{StreamID: subA.streamID, Error: "spoofed end"})
	b.sendText(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: subB.streamID, Stream: agentprotocol.StreamRuntime, Lines: logLine("marker")})
	require.Equal(t, "marker", recvLog(t, subB.ch).Lines[0].Text)
	requireLogOpenAndEmpty(t, subA.ch)

	// Frames without lines are not delivered; frames of an unknown stream are ignored.
	a.sendText(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: subA.streamID, Stream: agentprotocol.StreamRuntime})
	a.sendText(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: "no-such-stream", Stream: agentprotocol.StreamRuntime, Lines: logLine("lost")})
	a.sendText(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: subA.streamID, Stream: agentprotocol.StreamRuntime, Lines: logLine("genuine")})
	got := recvLog(t, subA.ch)
	require.Equal(t, "genuine", got.Lines[0].Text, "the first frame to arrive is the owner's, with lines")
	require.Equal(t, subA.streamID, got.StreamID)
}

func TestLogEndClosesTheStreamAndShowsTheAgentsErrorAsALogLine(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})

	withErr := subscribeOn(t, r, a, context.Background(), "")
	a.sendText(agentprotocol.TypeLogEnd, agentprotocol.LogEnd{StreamID: withErr.streamID, Error: "container exited"})
	f := recvLog(t, withErr.ch)
	require.Equal(t, agentprotocol.StreamRuntime, f.Stream)
	require.Equal(t, withErr.streamID, f.StreamID)
	require.Len(t, f.Lines, 1)
	require.Equal(t, "[nebulactrl] log stream ended: container exited", f.Lines[0].Text)
	requireLogClosed(t, withErr.ch)

	clean := subscribeOn(t, r, a, context.Background(), "")
	a.sendText(agentprotocol.TypeLogEnd, agentprotocol.LogEnd{StreamID: clean.streamID})
	requireLogClosed(t, clean.ch) // closed without a line

	r.h.subMu.Lock()
	require.Empty(t, r.h.subs, "ended streams are forgotten")
	r.h.subMu.Unlock()
}

func TestASlowLogConsumerDropsFramesInsteadOfStallingTheAgentConnection(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})
	s := subscribeOn(t, r, a, context.Background(), "")
	const sent = subBuffer + 44
	for i := range sent {
		a.sendText(agentprotocol.TypeLog, agentprotocol.LogFrame{StreamID: s.streamID, Stream: agentprotocol.StreamRuntime, Lines: logLine(strconv.Itoa(i))})
	}
	// The reader never blocks on the full stream: it goes on to process the end that follows.
	// Nothing is consumed until the hub has ended the stream, so the buffer really overflowed.
	a.sendText(agentprotocol.TypeLogEnd, agentprotocol.LogEnd{StreamID: s.streamID})
	require.Eventually(t, func() bool {
		r.h.subMu.Lock()
		defer r.h.subMu.Unlock()
		return len(r.h.subs) == 0
	}, 10*time.Second, 5*time.Millisecond)
	var texts []string
	for f := range s.ch {
		texts = append(texts, f.Lines[0].Text)
	}
	require.Len(t, texts, subBuffer, "the buffer's worth is kept, the overflow dropped")
	require.Equal(t, "0", texts[0])
	require.Equal(t, strconv.Itoa(subBuffer-1), texts[subBuffer-1])
}

// commandsFor reads the next ephemeral command the agent receives.
func nextCommand(t *testing.T, a *testAgent) agentprotocol.Command {
	t.Helper()
	var c agentprotocol.Command
	require.NoError(t, a.nextControl(agentprotocol.TypeCommand).Into(&c))
	return c
}

func unsubscribedStream(t *testing.T, c agentprotocol.Command) string {
	t.Helper()
	require.Equal(t, agentprotocol.KindLogsUnsubscribe, c.Kind)
	var u agentprotocol.LogsUnsubscribe
	require.NoError(t, json.Unmarshal(c.Payload, &u))
	return u.StreamID
}

func TestCancelingASubscriptionTellsTheAgentOnceAndOnlyWhileTheStreamIsLive(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})

	first := subscribeOn(t, r, a, context.Background(), "")
	first.cancel()
	first.cancel() // idempotent
	requireLogClosed(t, first.ch)
	un := nextCommand(t, a)
	require.Equal(t, first.streamID, unsubscribedStream(t, un))
	require.Equal(t, un.ID, un.IdempotencyKey)

	// A stream the agent already ended needs no unsubscribe.
	ended := subscribeOn(t, r, a, context.Background(), "")
	a.sendText(agentprotocol.TypeLogEnd, agentprotocol.LogEnd{StreamID: ended.streamID})
	requireLogClosed(t, ended.ch)
	ended.cancel()

	// The next command the agent sees is the unsubscribe of this later stream,
	// not a duplicate for the first or an unsubscribe for the ended one.
	ctx, cancelCtx := context.WithCancel(context.Background())
	last := subscribeOn(t, r, a, ctx, "")
	cancelCtx() // the caller's context ending cancels like cancel()
	requireLogClosed(t, last.ch)
	require.Equal(t, last.streamID, unsubscribedStream(t, nextCommand(t, a)))
}

func TestSubscriptionsEndWhenTheHubShutsDown(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})
	s := subscribeOn(t, r, a, context.Background(), "")
	r.h.Close()
	requireLogClosed(t, s.ch)
}

func TestSubscriptionsEndWhenTheAgentDisconnects(t *testing.T) {
	r := newTunnelRig(t)
	a := r.dial(agentOpts{})
	other := r.dial(agentOpts{})
	gone := subscribeOn(t, r, a, context.Background(), "")
	stays := subscribeOn(t, r, other, context.Background(), "")

	_ = a.ws.CloseNow()
	requireLogClosed(t, gone.ch)
	requireLogOpenAndEmpty(t, stays.ch)
	r.h.subMu.Lock()
	require.Len(t, r.h.subs, 1)
	require.Contains(t, r.h.subs, stays.streamID)
	r.h.subMu.Unlock()
}

func TestFailedSubscribeResultShowsTheErrorAndEndsTheStream(t *testing.T) {
	r := newTunnelRig(t)
	a, b := r.dial(agentOpts{}), r.dial(agentOpts{})
	ca, cb := r.h.conn(a.serverID), r.h.conn(b.serverID)

	failing := subscribeOn(t, r, a, context.Background(), "")
	idOf := func(s subscribed) uuid.UUID { return uuid.MustParse(s.cmd.ID) }

	// A result from another server's agent, or a success, changes nothing.
	r.h.onEphemeralResult(cb, idOf(failing), agentprotocol.ResultFrame{CommandID: failing.cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: "spoofed"})
	r.h.onEphemeralResult(ca, idOf(failing), agentprotocol.ResultFrame{CommandID: failing.cmd.ID, Outcome: agentprotocol.OutcomeSucceeded})
	r.h.onEphemeralResult(ca, uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "unknown command"})
	requireLogOpenAndEmpty(t, failing.ch)

	r.h.onEphemeralResult(ca, idOf(failing), agentprotocol.ResultFrame{CommandID: failing.cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: "no such container"})
	f := recvLog(t, failing.ch)
	require.Equal(t, "[nebulactrl] no such container", f.Lines[0].Text)
	require.Equal(t, failing.streamID, f.StreamID)
	requireLogClosed(t, failing.ch)

	silent := subscribeOn(t, r, a, context.Background(), "")
	r.h.onEphemeralResult(ca, idOf(silent), agentprotocol.ResultFrame{CommandID: silent.cmd.ID, Outcome: agentprotocol.OutcomeFailed})
	require.Equal(t, "[nebulactrl] subscription failed", recvLog(t, silent.ch).Lines[0].Text, "a failure without a message still says so")
	requireLogClosed(t, silent.ch)
}

func TestSubscribeToAConnectionThatClosedIsOfflineAndLeavesNothingBehind(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	h.register(c)
	for range outQueue {
		c.out <- []byte("queued")
	}
	c.closeOnce.Do(func() { close(c.done) })

	_, _, err := h.SubscribeLogs(context.Background(), c.serverID, agentprotocol.LogsSubscribe{ServiceID: uuid.NewString()})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, ae.Status)
	h.subMu.Lock()
	require.Empty(t, h.subs)
	h.subMu.Unlock()
}

// ---- task run frames ----

type taskSink struct {
	mu   sync.Mutex
	got  []agentprotocol.LogFrame
	from []uuid.UUID
	err  error
}

func (s *taskSink) HandleTaskLog(_ context.Context, serverID uuid.UUID, lf agentprotocol.LogFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, lf)
	s.from = append(s.from, serverID)
	return s.err
}

func TestTaskRunFramesGoToTheTaskSinkNeverToTheDeploymentLog(t *testing.T) {
	// The hub has no database: a frame that was routed to the deployment log
	// would dereference the nil pool.
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	frame := agentprotocol.LogFrame{TaskRunID: "run-1", DeploymentID: uuid.NewString(), Stream: agentprotocol.StreamRuntime, Lines: logLine("task output")}

	h.onLog(context.Background(), c, frame) // no sink installed: dropped

	sink := &taskSink{}
	h.SetTaskLogSink(sink)
	h.onLog(context.Background(), c, frame)
	h.onLog(context.Background(), c, agentprotocol.LogFrame{TaskRunID: "run-2"}) // no lines: nothing to store
	require.Len(t, sink.got, 1)
	require.Equal(t, "run-1", sink.got[0].TaskRunID)
	require.Equal(t, c.serverID, sink.from[0], "the sink is told which server reported the lines")

	sink.err = errors.New("task run is gone")
	h.onLog(context.Background(), c, frame) // a sink failure is logged, not fatal
	require.Len(t, sink.got, 2)
}

func TestLogFramesWithUnusableIdentifiersAreIgnored(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil) // no database: a lookup would dereference the nil pool
	t.Cleanup(h.Close)
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	h.onLog(context.Background(), c, agentprotocol.LogFrame{DeploymentID: "not-a-uuid", Lines: logLine("x")})
	h.onLog(context.Background(), c, agentprotocol.LogFrame{Lines: logLine("no destination at all")})
	h.onLogEnd(context.Background(), c, agentprotocol.LogEnd{DeploymentID: "not-a-uuid"})
	h.onLogEnd(context.Background(), c, agentprotocol.LogEnd{StreamID: "no-such-stream", Error: "x"})
}

func TestLogEndForgetsOnlyThatDeploymentsCachedState(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	c := newAgentConn(h, nil, uuid.New(), uuid.New())
	a, b := uuid.New(), uuid.New()
	c.deps[a] = &depLog{}
	c.deps[b] = &depLog{}
	h.onLogEnd(context.Background(), c, agentprotocol.LogEnd{DeploymentID: a.String()})
	require.NotContains(t, c.deps, a)
	require.Contains(t, c.deps, b)
}

// ---- deployment followers ----

func TestDeploymentFollowersGetCopiesOfTheirOwnDeploymentsFramesOnly(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	depA, depB := uuid.New(), uuid.New()
	a1, cancelA1 := h.FollowDeploymentLogs(context.Background(), depA)
	a2, cancelA2 := h.FollowDeploymentLogs(context.Background(), depA)
	b1, cancelB1 := h.FollowDeploymentLogs(context.Background(), depB)
	defer cancelB1()

	h.fanOutDeployment(depA, agentprotocol.LogFrame{DeploymentID: depA.String(), Lines: logLine("for a")})
	require.Equal(t, "for a", recvLog(t, a1).Lines[0].Text)
	require.Equal(t, "for a", recvLog(t, a2).Lines[0].Text)
	requireLogOpenAndEmpty(t, b1)

	// Canceling one follower closes only its channel; the set is dropped with its last member.
	cancelA1()
	cancelA1()
	requireLogClosed(t, a1)
	h.fanOutDeployment(depA, agentprotocol.LogFrame{Lines: logLine("again")})
	require.Equal(t, "again", recvLog(t, a2).Lines[0].Text)
	cancelA2()
	requireLogClosed(t, a2)
	h.fanOutDeployment(depA, agentprotocol.LogFrame{Lines: logLine("nobody listens")})
	h.folMu.Lock()
	require.NotContains(t, h.followers, depA)
	require.Contains(t, h.followers, depB)
	h.folMu.Unlock()
}

func TestASlowFollowerMissesFramesButNeverBlocksTheFanOut(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	t.Cleanup(h.Close)
	dep := uuid.New()
	slow, cancel := h.FollowDeploymentLogs(context.Background(), dep)
	defer cancel()
	for i := range followBuffer + 50 {
		h.fanOutDeployment(dep, agentprotocol.LogFrame{Lines: logLine(strconv.Itoa(i))})
	}
	require.Len(t, slow, followBuffer)
	require.Equal(t, "0", recvLog(t, slow).Lines[0].Text, "what fit is kept in order")
}

func TestFollowersEndWhenTheirContextOrTheHubEnds(t *testing.T) {
	h := NewHub(nil, nil, nil, nil, nil)
	dep := uuid.New()
	ctx, cancelCtx := context.WithCancel(context.Background())
	byCtx, _ := h.FollowDeploymentLogs(ctx, dep)
	byHub, _ := h.FollowDeploymentLogs(context.Background(), dep)
	cancelCtx()
	requireLogClosed(t, byCtx)
	h.Close()
	requireLogClosed(t, byHub)
	h.folMu.Lock()
	require.Empty(t, h.followers)
	h.folMu.Unlock()
}
