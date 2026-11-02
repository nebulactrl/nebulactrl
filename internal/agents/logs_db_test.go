package agents_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// barrier makes "nothing happened" observable: frames of one connection are
// processed in order, so once a task-run frame sent after the frames under
// test has reached the task sink, they have all been handled.
type barrier struct {
	a   *agentConn
	ch  chan string
	seq int
}

func (b *barrier) HandleTaskLog(_ context.Context, _ uuid.UUID, lf agentprotocol.LogFrame) error {
	b.ch <- lf.TaskRunID
	return nil
}

func (h *harness) newBarrier(a *agentConn) *barrier {
	b := &barrier{a: a, ch: make(chan string, 64)}
	h.hub.SetTaskLogSink(b)
	return b
}

func (b *barrier) wait(t *testing.T) {
	t.Helper()
	b.seq++
	id := fmt.Sprintf("barrier-%d", b.seq)
	b.a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{TaskRunID: id, Lines: []agentprotocol.LogLine{{TS: time.Now(), Text: "x"}}})
	select {
	case got := <-b.ch:
		require.Equal(t, id, got)
	case <-time.After(10 * time.Second):
		t.Fatal("the connection stopped processing frames")
	}
}

func (h *harness) extraDeployment(serviceID, serverID uuid.UUID) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	require.NoError(h.t, h.pool.QueryRow(h.ctx,
		"INSERT INTO deployments (service_id, server_id, trigger, actor_type, status) VALUES ($1, $2, 'manual', 'user', 'failed') RETURNING id",
		serviceID, serverID).Scan(&id))
	return id
}

func (h *harness) seedLines(dep uuid.UUID, stream string, n int) {
	h.t.Helper()
	_, err := h.pool.Exec(h.ctx,
		"INSERT INTO deployment_logs (deployment_id, stream, line) SELECT $1, $2, 'old ' || g FROM generate_series(1, $3::int) g", dep, stream, n)
	require.NoError(h.t, err)
}

func (h *harness) lineCount(dep uuid.UUID, where string, args ...any) (n int) {
	h.t.Helper()
	q := "SELECT count(*) FROM deployment_logs WHERE deployment_id = $1"
	if where != "" {
		q += " AND " + where
	}
	require.NoError(h.t, h.pool.QueryRow(h.ctx, q, append([]any{dep}, args...)...).Scan(&n))
	return n
}

func (h *harness) waitLines(dep uuid.UUID, want int) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return h.lineCount(dep, "") == want }, 10*time.Second, 20*time.Millisecond)
}

func lines(texts ...string) []agentprotocol.LogLine {
	out := make([]agentprotocol.LogLine, len(texts))
	for i, s := range texts {
		out[i] = agentprotocol.LogLine{TS: time.Now(), Text: s}
	}
	return out
}

func TestDeploymentLogLinesAreCleanedStampedAndRedactedBeforeTheyAreStored(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	follow, cancel := h.hub.FollowDeploymentLogs(context.Background(), depID)
	defer cancel()

	fixed := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{
		DeploymentID: depID.String(), ServiceID: uuid.NewString(), Stream: "not-a-stream",
		Lines: []agentprotocol.LogLine{
			{Text: "a\x00b"}, // a zero timestamp: the control plane stamps it
			{TS: fixed, Text: strings.Repeat("x", 20000)},
			{TS: fixed, Text: strings.Repeat("€", 6000)},
			{TS: fixed, Text: "api token=sekret-canary-12345 ok"},
		},
	})
	h.waitLines(depID, 4)

	rows, err := h.pool.Query(h.ctx, "SELECT stream, line, created_at FROM deployment_logs WHERE deployment_id = $1 ORDER BY id", depID)
	require.NoError(t, err)
	defer rows.Close()
	type row struct {
		stream, line string
		at           time.Time
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.stream, &r.line, &r.at))
		got = append(got, r)
	}
	require.Len(t, got, 4)
	for _, r := range got {
		require.Equal(t, agentprotocol.StreamDeploy, r.stream, "an unknown stream name is stored as the deploy log")
		require.True(t, utf8.ValidString(r.line))
	}
	require.Equal(t, "ab", got[0].line, "NUL bytes cannot be stored and are dropped")
	require.WithinDuration(t, time.Now(), got[0].at, time.Minute)
	require.Equal(t, strings.Repeat("x", 16<<10)+"…", got[1].line, "a line is bounded at 16 KiB and says so")
	require.True(t, got[1].at.Equal(fixed), "the agent's timestamp is kept")
	require.True(t, strings.HasPrefix(got[2].line, "€€€") && strings.HasSuffix(got[2].line, "…"), "a cut inside a multibyte rune still stores valid UTF-8")
	require.Less(t, len(got[2].line), 16<<10+8)
	require.Equal(t, "api token=•••••• ok", got[3].line)

	select {
	case fr := <-follow:
		require.Equal(t, svcID.String(), fr.ServiceID, "the service comes from the deployment, not from the agent's claim")
		require.Equal(t, agentprotocol.StreamDeploy, fr.Stream)
		require.Len(t, fr.Lines, 4)
		require.Equal(t, "ab", fr.Lines[0].Text)
		require.Equal(t, "api token=•••••• ok", fr.Lines[3].Text)
		require.False(t, fr.Lines[0].TS.IsZero())
	case <-time.After(5 * time.Second):
		t.Fatal("the follower received nothing")
	}
}

func TestEveryNamedLogStreamIsStoredUnderItsName(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	streams := []string{agentprotocol.StreamBuild, agentprotocol.StreamDeploy, agentprotocol.StreamRuntime, agentprotocol.StreamDebug}
	for _, s := range streams {
		a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: s, Lines: lines("line of " + s)})
	}
	h.waitLines(depID, len(streams))
	for _, s := range streams {
		require.Equal(t, 1, h.lineCount(depID, "stream = $2 AND line = $3", s, "line of "+s), s)
	}
}

func TestLogCapsAreRecoveredFromStoredLinesAfterAReconnect(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, exact := h.seedDeployment(e.serverID)
	over := h.extraDeployment(svcID, e.serverID)
	debug := h.extraDeployment(svcID, e.serverID)
	h.seedLines(exact, agentprotocol.StreamDeploy, agents.MaxDeploymentLogLines)  // full, but no marker yet
	h.seedLines(over, agentprotocol.StreamDeploy, agents.MaxDeploymentLogLines+1) // already past the cap
	h.seedLines(debug, agentprotocol.StreamDebug, agents.MaxDeploymentDebugLines) // debug full, deploy empty
	send := func(a *agentConn, dep uuid.UUID, stream string, n int) {
		a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{DeploymentID: dep.String(), ServiceID: svcID.String(), Stream: stream, Lines: lines(make([]string, n)...)})
	}

	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	bar := h.newBarrier(a)
	send(a, exact, agentprotocol.StreamDeploy, 3)
	send(a, over, agentprotocol.StreamDeploy, 3)
	send(a, debug, agentprotocol.StreamDebug, 2)
	send(a, debug, agentprotocol.StreamDeploy, 1)
	bar.wait(t)

	require.Equal(t, agents.MaxDeploymentLogLines+1, h.lineCount(exact, ""), "the full log gets its marker and nothing else")
	require.Equal(t, 1, h.lineCount(exact, "line = $2", agents.LogTruncatedMarker))
	require.Equal(t, agents.MaxDeploymentLogLines+1, h.lineCount(over, ""), "a log already past the cap stores nothing new")
	require.Zero(t, h.lineCount(over, "line = $2", agents.LogTruncatedMarker), "and does not add a marker the earlier run owes")
	require.Equal(t, agents.MaxDeploymentDebugLines+1, h.lineCount(debug, "stream = 'debug'"))
	require.Equal(t, 1, h.lineCount(debug, "line = $2", agents.DebugLogTruncatedMarker))
	require.Equal(t, 1, h.lineCount(debug, "stream = 'deploy'"), "a full debug stream does not block the deploy log")

	// A new connection reads the state from the stored rows: the marker is not added twice.
	b := h.dial(e, false)
	b.next(agentprotocol.TypeReconcile)
	bar = h.newBarrier(b)
	send(b, exact, agentprotocol.StreamDeploy, 5)
	send(b, debug, agentprotocol.StreamDebug, 5)
	bar.wait(t)
	require.Equal(t, agents.MaxDeploymentLogLines+1, h.lineCount(exact, ""))
	require.Equal(t, 1, h.lineCount(exact, "line = $2", agents.LogTruncatedMarker))
	require.Equal(t, agents.MaxDeploymentDebugLines+1, h.lineCount(debug, "stream = 'debug'"))
}

func TestLogEndMakesTheNextFramesReloadWhatIsStored(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	bar := h.newBarrier(a)
	frame := func(texts ...string) {
		a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: agentprotocol.StreamDeploy, Lines: lines(texts...)})
	}

	frame("first")
	h.waitLines(depID, 1)
	// Meanwhile another process (a second control-plane instance, a retried
	// build) fills the log up to the cap. The cached count does not know.
	h.seedLines(depID, agentprotocol.StreamDeploy, agents.MaxDeploymentLogLines)
	a.write(agentprotocol.TypeLogEnd, agentprotocol.LogEnd{DeploymentID: depID.String()})
	frame("second", "third", "fourth")
	bar.wait(t)
	require.Equal(t, agents.MaxDeploymentLogLines+1, h.lineCount(depID, ""), "after log_end the count is read again and the cap holds")
}

func TestDeploymentLogsAreDroppedWhenTheyCannotBeRedacted(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	bar := h.newBarrier(a)
	follow, cancel := h.hub.FollowDeploymentLogs(context.Background(), depID)
	defer cancel()
	frame := func(text string) {
		a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: agentprotocol.StreamDeploy, Lines: lines(text)})
	}

	h.hub.SetRedactor(nil) // no redactor at all: fail closed
	frame("leaks token=sekret-canary-12345")
	bar.wait(t)
	h.hub.SetRedactor(failingRedactor{})
	frame("leaks token=sekret-canary-12345 again")
	bar.wait(t)
	require.Zero(t, h.lineCount(depID, ""), "unredacted lines are never stored")
	select {
	case fr := <-follow:
		t.Fatalf("unredacted lines were published: %+v", fr)
	default:
	}

	// The connection is fine and the next frame, with a working redactor, is stored.
	h.hub.SetRedactor(h.dom)
	frame("fine token=sekret-canary-12345")
	h.waitLines(depID, 1)
	var line string
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT line FROM deployment_logs WHERE deployment_id = $1", depID).Scan(&line))
	require.Equal(t, "fine token=••••••", line)
}

type failingRedactor struct{}

func (failingRedactor) RedactorFor(context.Context, uuid.UUID) (func(string) string, error) {
	return nil, errors.New("secrets store unavailable")
}

func TestDeploymentLogsOfUnknownOrMalformedDeploymentsAreIgnored(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	svcID, depID := h.seedDeployment(e.serverID)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	bar := h.newBarrier(a)
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{DeploymentID: id, ServiceID: svcID.String(), Stream: agentprotocol.StreamDeploy, Lines: lines("lost")})
	}
	a.write(agentprotocol.TypeLog, agentprotocol.LogFrame{DeploymentID: depID.String(), ServiceID: svcID.String(), Stream: agentprotocol.StreamDeploy, Lines: lines("kept")})
	bar.wait(t)
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM deployment_logs").Scan(&n))
	require.Equal(t, 1, n, "only the deployment that runs on this server got a line")
}

func TestAFailedLogSubscriptionIsReportedAsALogLineAndAcknowledged(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	other := h.enroll("edge-2")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	b := h.dial(other, false)
	b.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) && h.hub.Online(other.serverID) }, 5*time.Second, 10*time.Millisecond)

	ch, _, err := h.hub.SubscribeLogs(context.Background(), e.serverID, agentprotocol.LogsSubscribe{ServiceID: uuid.NewString(), Follow: true})
	require.NoError(t, err)
	var cmd agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&cmd))

	ackOf := func(c *agentConn, id string) {
		t.Helper()
		var ack agentprotocol.ResultAck
		require.NoError(t, c.next(agentprotocol.TypeResultAck).Into(&ack))
		require.Equal(t, id, ack.CommandID)
	}
	// Another server's agent answering for this command is acknowledged and ignored.
	b.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: "spoofed"})
	ackOf(b, cmd.ID)
	// So is a result for a command nobody knows.
	unknown := uuid.NewString()
	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: unknown, Outcome: agentprotocol.OutcomeFailed, Error: "unknown"})
	ackOf(a, unknown)
	select {
	case fr, ok := <-ch:
		t.Fatalf("the stream was touched: %+v ok=%v", fr, ok)
	default:
	}

	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeFailed, Error: "no such service"})
	ackOf(a, cmd.ID)
	select {
	case fr := <-ch:
		require.Equal(t, "[nebulactrl] no such service", fr.Lines[0].Text)
	case <-time.After(5 * time.Second):
		t.Fatal("the failure was not shown")
	}
	select {
	case _, open := <-ch:
		require.False(t, open)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open")
	}
	var persisted int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM agent_commands WHERE kind = $1", agentprotocol.KindLogsSubscribe).Scan(&persisted))
	require.Zero(t, persisted, "ephemeral commands leave no rows")
}
