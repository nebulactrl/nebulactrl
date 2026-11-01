package agents_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeProbeSink struct {
	mu       sync.Mutex
	attempts int
	got      []agentprotocol.Probes
	srv      []uuid.UUID
	fail     bool
}

func (f *fakeProbeSink) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeProbeSink) IngestProbes(_ context.Context, serverID uuid.UUID, p agentprotocol.Probes) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.fail {
		return errors.New("database unavailable")
	}
	f.got = append(f.got, p)
	f.srv = append(f.srv, serverID)
	return nil
}

func TestProbeFramesAreSunkWithTheAuthenticatedServerAndAckedAfterStorage(t *testing.T) {
	h := newHarness(t)
	sink := &fakeProbeSink{}
	h.hub.SetProbeSink(sink)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	res := agentprotocol.ProbeResult{ServiceID: uuid.NewString(), At: time.Now(), Outcome: agentprotocol.ProbeUp, Replicas: 1, Passed: 1}
	a.write(agentprotocol.TypeProbes, agentprotocol.Probes{Seq: 4, Results: []agentprotocol.ProbeResult{res}})
	var ack agentprotocol.ProbesAck
	require.NoError(t, a.next(agentprotocol.TypeProbesAck).Into(&ack))
	require.EqualValues(t, 4, ack.Seq)
	sink.mu.Lock()
	require.Len(t, sink.got, 1)
	require.Equal(t, e.serverID, sink.srv[0], "the sink gets the server of the authenticated connection")
	sink.mu.Unlock()

	// A failing sink withholds the ack so the agent resends.
	sink.mu.Lock()
	sink.fail = true
	sink.mu.Unlock()
	a.write(agentprotocol.TypeProbes, agentprotocol.Probes{Seq: 5, Results: []agentprotocol.ProbeResult{res}})
	require.Eventually(t, func() bool { return sink.attemptCount() == 2 }, 5*time.Second, 5*time.Millisecond)
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	a.write(agentprotocol.TypeProbes, agentprotocol.Probes{Seq: 6, Results: []agentprotocol.ProbeResult{res}})
	require.NoError(t, a.next(agentprotocol.TypeProbesAck).Into(&ack))
	require.EqualValues(t, 6, ack.Seq, "seq 5 was not acknowledged")
}

func TestProbeFramesWithoutASinkAreAcknowledged(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeProbes, agentprotocol.Probes{Seq: 1})
	var ack agentprotocol.ProbesAck
	require.NoError(t, a.next(agentprotocol.TypeProbesAck).Into(&ack))
	require.EqualValues(t, 1, ack.Seq)
}
