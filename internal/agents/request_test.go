package agents_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRequestRoundTripIsEphemeralAndCarriesSecrets(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")

	_, err := h.hub.Request(context.Background(), e.serverID, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "s"}, nil, time.Second)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusServiceUnavailable, ae.Status, "offline server")

	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 20*time.Millisecond)

	type answer struct {
		res agentprotocol.ResultFrame
		err error
	}
	got := make(chan answer, 1)
	go func() {
		res, err := h.hub.Request(context.Background(), e.serverID, agentprotocol.KindLogsDrainTest,
			agentprotocol.LogsDrainTest{Drain: agentprotocol.LogDrain{ID: "d1"}}, map[string]string{"logdrain:d1:token": "s3cret-token"}, 10*time.Second)
		got <- answer{res, err}
	}()

	var cmd agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&cmd))
	require.Equal(t, agentprotocol.KindLogsDrainTest, cmd.Kind)
	require.Equal(t, "s3cret-token", cmd.Secrets["logdrain:d1:token"])
	require.NotContains(t, string(cmd.Payload), "s3cret-token")

	data, _ := json.Marshal(agentprotocol.LogsDrainTestResult{OK: true, Message: "delivered"})
	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data})
	var ack agentprotocol.ResultAck
	require.NoError(t, a.next(agentprotocol.TypeResultAck).Into(&ack))
	require.Equal(t, cmd.ID, ack.CommandID)

	select {
	case ans := <-got:
		require.NoError(t, ans.err)
		require.Equal(t, agentprotocol.OutcomeSucceeded, ans.res.Outcome)
		var tr agentprotocol.LogsDrainTestResult
		require.NoError(t, json.Unmarshal(ans.res.Data, &tr))
		require.True(t, tr.OK)
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
	}
	// Never persisted: not in the durable queue, and its secrets are nowhere in the database.
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT count(*) FROM agent_commands WHERE kind IN ('logs_query','logs_drain_test')").Scan(&n))
	require.Zero(t, n)
}

// Command IDs are not secrets once they leak: another server's agent must not
// be able to acknowledge a command it was not sent or answer a request that
// went to someone else.
func TestAnotherServersAgentCannotAckOrAnswerOurCommands(t *testing.T) {
	h := newHarness(t)
	victim, intruder := h.enroll("edge-1"), h.enroll("edge-2")
	a, b := h.dial(victim, false), h.dial(intruder, false)
	a.next(agentprotocol.TypeReconcile)
	b.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(victim.serverID) && h.hub.Online(intruder.serverID) }, 5*time.Second, 20*time.Millisecond)
	// barrier makes the hub finish every frame b sent before it: an unknown
	// result is answered with a result_ack, in order.
	barrier := func() {
		b.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: uuid.NewString(), Outcome: agentprotocol.OutcomeSucceeded})
		b.next(agentprotocol.TypeResultAck)
	}

	// A command's ack counts only from its own server.
	id := h.enqueue(victim.serverID, agentprotocol.KindRestartService, nil)
	var cmd agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&cmd))
	b.write(agentprotocol.TypeAck, agentprotocol.Ack{CommandID: cmd.ID})
	barrier()
	require.NotEqual(t, "acked", h.commandStatus(id), "a foreign ack must not mark the command")
	a.write(agentprotocol.TypeAck, agentprotocol.Ack{CommandID: cmd.ID})
	require.Eventually(t, func() bool { return h.commandStatus(id) == "acked" }, 5*time.Second, 20*time.Millisecond)

	// A request is answered only by the connection it was sent to.
	type answer struct {
		res agentprotocol.ResultFrame
		err error
	}
	got := make(chan answer, 1)
	go func() {
		res, err := h.hub.Request(context.Background(), victim.serverID, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "s"}, nil, 10*time.Second)
		got <- answer{res, err}
	}()
	var req agentprotocol.Command
	require.NoError(t, a.next(agentprotocol.TypeCommand).Into(&req))
	b.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: req.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{"forged":true}`)})
	barrier()
	select {
	case ans := <-got:
		t.Fatalf("a foreign agent answered the request: %+v %v", ans.res, ans.err)
	default:
	}
	a.write(agentprotocol.TypeResult, agentprotocol.ResultFrame{CommandID: req.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{"forged":false}`)})
	select {
	case ans := <-got:
		require.NoError(t, ans.err)
		require.JSONEq(t, `{"forged":false}`, string(ans.res.Data))
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
	}
}

func TestRequestFailsWhenTheAgentDisconnectsOrIsSlow(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 20*time.Millisecond)

	// Slow agent: 504.
	_, err := h.hub.Request(context.Background(), e.serverID, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "s"}, nil, 150*time.Millisecond)
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusGatewayTimeout, ae.Status)
	a.next(agentprotocol.TypeCommand) // the command the slow agent never answered

	// The agent drops while the request waits.
	errc := make(chan error, 1)
	go func() {
		_, err := h.hub.Request(context.Background(), e.serverID, agentprotocol.KindLogsQuery, agentprotocol.LogsQuery{ServiceID: "s"}, nil, 10*time.Second)
		errc <- err
	}()
	a.next(agentprotocol.TypeCommand)
	_ = a.ws.CloseNow() // the agent drops
	select {
	case err := <-errc:
		require.ErrorIs(t, err, agents.ErrRequestLost)
	case <-time.After(5 * time.Second):
		t.Fatal("request did not end when the agent went away")
	}
}

type captureSink struct {
	mu  sync.Mutex
	got []agentprotocol.LogStatus
	ids []uuid.UUID
}

func (c *captureSink) HandleLogStatus(_ context.Context, id uuid.UUID, st agentprotocol.LogStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got, c.ids = append(c.got, st), append(c.ids, id)
	return nil
}

func TestLogStatusFramesReachTheSink(t *testing.T) {
	h := newHarness(t)
	sink := &captureSink{}
	h.hub.SetLogStatusSink(sink)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeLogStatus, agentprotocol.LogStatus{Capturing: true, TotalBytes: 42, Drains: []agentprotocol.LogDrainStatus{{ID: "d", State: agentprotocol.DrainOK}}})
	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.got) == 1
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, int64(42), sink.got[0].TotalBytes)
	require.Equal(t, e.serverID, sink.ids[0], "attributed to the connection's server, not to anything in the frame")
}
