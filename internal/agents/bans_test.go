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

type fakeBanSink struct {
	mu       sync.Mutex
	attempts int
	got      []agentprotocol.BanFrame
	srv      []uuid.UUID
	fail     bool
}

func (f *fakeBanSink) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeBanSink) IngestBans(_ context.Context, serverID uuid.UUID, b agentprotocol.BanFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.fail {
		return errors.New("database unavailable")
	}
	f.got = append(f.got, b)
	f.srv = append(f.srv, serverID)
	return nil
}

func TestBanFramesAreSunkWithTheAuthenticatedServerAndAckedAfterStorage(t *testing.T) {
	h := newHarness(t)
	sink := &fakeBanSink{}
	h.hub.SetBanSink(sink)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	now := time.Now().UTC().Truncate(time.Second)
	ev := agentprotocol.BanEvent{EventID: uuid.NewString(), Kind: agentprotocol.BanEventBan, At: now, Ban: agentprotocol.Ban{
		ID: uuid.NewString(), ServiceID: uuid.NewString(), RouteID: "r", Prefix: "203.0.113.7/32", StartedAt: now, ExpiresAt: now.Add(time.Hour), Strikes: 10,
	}}
	a.write(agentprotocol.TypeBans, agentprotocol.BanFrame{Seq: 7, Events: []agentprotocol.BanEvent{ev}, Snapshot: true, Services: []string{ev.Ban.ServiceID}})
	var ack agentprotocol.BanAck
	require.NoError(t, a.next(agentprotocol.TypeBansAck).Into(&ack))
	require.EqualValues(t, 7, ack.Seq)
	sink.mu.Lock()
	require.Len(t, sink.got, 1)
	require.Equal(t, e.serverID, sink.srv[0], "the sink gets the server of the authenticated connection")
	require.True(t, sink.got[0].Snapshot)
	require.Equal(t, ev.Ban.ID, sink.got[0].Events[0].Ban.ID)
	sink.mu.Unlock()

	// A failing sink withholds the ack so the agent resends the frame.
	sink.mu.Lock()
	sink.fail = true
	sink.mu.Unlock()
	a.write(agentprotocol.TypeBans, agentprotocol.BanFrame{Seq: 8, Events: []agentprotocol.BanEvent{ev}})
	require.Eventually(t, func() bool { return sink.attemptCount() == 2 }, 5*time.Second, 5*time.Millisecond)
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	a.write(agentprotocol.TypeBans, agentprotocol.BanFrame{Seq: 8, Events: []agentprotocol.BanEvent{ev}})
	require.NoError(t, a.next(agentprotocol.TypeBansAck).Into(&ack))
	require.EqualValues(t, 8, ack.Seq, "the first attempt of seq 8 was not acknowledged; the resend is")
}

func TestBanFramesWithoutASinkAreAcknowledged(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeBans, agentprotocol.BanFrame{Seq: 1})
	var ack agentprotocol.BanAck
	require.NoError(t, a.next(agentprotocol.TypeBansAck).Into(&ack))
	require.EqualValues(t, 1, ack.Seq)
}
