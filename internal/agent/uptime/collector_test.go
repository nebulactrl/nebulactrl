package uptime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type fakeSender struct {
	mu        sync.Mutex
	connected bool
	ack       func(seq uint64) // nil: never acknowledge
	frames    []agentprotocol.Probes
}

func (f *fakeSender) Connected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }

func (f *fakeSender) Probes(p agentprotocol.Probes) {
	f.mu.Lock()
	f.frames = append(f.frames, p)
	ack := f.ack
	f.mu.Unlock()
	if ack != nil {
		ack(p.Seq)
	}
}

func (f *fakeSender) sent() []agentprotocol.Probes {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentprotocol.Probes(nil), f.frames...)
}

func (f *fakeSender) set(connected bool, ack func(uint64)) {
	f.mu.Lock()
	f.connected, f.ack = connected, ack
	f.mu.Unlock()
}

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newCollector(st *store.Store, snd *fakeSender) *Collector {
	return NewCollector(Config{Buffer: st, Sender: snd, Now: func() time.Time { return now }, AckTimeout: 50 * time.Millisecond})
}

// result is a probe result of service svc taken ago before now.
func result(svc string, ago time.Duration) agentprotocol.ProbeResult {
	return agentprotocol.ProbeResult{ServiceID: svc, At: now.Add(-ago), Outcome: agentprotocol.ProbeUp, Replicas: 1, Passed: 1}
}

func pending(t *testing.T, st *store.Store) []agentprotocol.ProbeResult {
	t.Helper()
	_, rs, err := st.PendingProbes(100000)
	require.NoError(t, err)
	return rs
}

func TestFlushSendsInTimeOrderAndDeletesOnAck(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{connected: true}
	c := newCollector(st, snd)
	snd.ack = c.Ack

	// Stored out of order; sent oldest first.
	c.Record([]agentprotocol.ProbeResult{result("b", 30*time.Second), result("a", time.Minute), result("a", 30*time.Second)})
	c.Flush(context.Background())

	frames := snd.sent()
	require.Len(t, frames, 1)
	require.EqualValues(t, 1, frames[0].Seq)
	require.Equal(t, []agentprotocol.ProbeResult{result("a", time.Minute), result("a", 30*time.Second), result("b", 30*time.Second)}, frames[0].Results)
	require.Empty(t, pending(t, st), "acknowledged results are deleted")
}

func TestUnacknowledgedResultsAreKeptAndResent(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{connected: true} // the control plane never acks
	c := newCollector(st, snd)
	c.Record([]agentprotocol.ProbeResult{result("a", time.Minute)})

	c.Flush(context.Background())
	require.Len(t, snd.sent(), 1)
	require.Len(t, pending(t, st), 1, "no ack: the result stays")

	snd.set(true, c.Ack)
	c.Flush(context.Background())
	require.Len(t, snd.sent(), 2, "resent")
	require.Equal(t, snd.sent()[0].Results, snd.sent()[1].Results)
	require.NotEqual(t, snd.sent()[0].Seq, snd.sent()[1].Seq, "every send has its own seq")
	require.Empty(t, pending(t, st))
}

func TestAckForAnotherSeqDeletesNothing(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{connected: true}
	c := newCollector(st, snd)
	snd.ack = func(seq uint64) { c.Ack(seq + 100) } // acknowledges something else
	c.Record([]agentprotocol.ProbeResult{result("a", time.Minute)})

	c.Flush(context.Background())
	require.Len(t, pending(t, st), 1)
}

func TestNothingIsSentWhileDisconnectedAndEverythingAfterReconnect(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{}
	c := newCollector(st, snd)
	c.Record([]agentprotocol.ProbeResult{result("a", time.Minute), result("a", 30*time.Second)})

	c.Flush(context.Background())
	require.Empty(t, snd.sent())
	require.Len(t, pending(t, st), 2, "results wait in the store")

	snd.set(true, c.Ack)
	c.Flush(context.Background())
	require.Len(t, snd.sent(), 1)
	require.Len(t, snd.sent()[0].Results, 2)
	require.Empty(t, pending(t, st))
}

func TestResultsSurviveAnAgentRestart(t *testing.T) {
	dir := t.TempDir()
	first, err := store.Open(dir)
	require.NoError(t, err)
	snd1 := &fakeSender{connected: true} // sent, never acknowledged, then the agent dies
	c1 := newCollector(first, snd1)
	want := []agentprotocol.ProbeResult{result("a", 2*time.Minute), result("a", time.Minute), result("b", time.Minute)}
	c1.Record(want)
	c1.Flush(context.Background())
	require.Len(t, snd1.sent(), 1)
	require.NoError(t, first.Close())

	second := openStore(t, dir)
	snd2 := &fakeSender{connected: true}
	c2 := newCollector(second, snd2)
	snd2.ack = c2.Ack
	c2.Flush(context.Background())

	require.Len(t, snd2.sent(), 1, "the new process resends what the old one left")
	require.Equal(t, want, snd2.sent()[0].Results)
	require.Empty(t, pending(t, second), "and forgets it once acknowledged")
}

func TestResultsOlderThanTheRetentionAreDropped(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{} // offline
	c := newCollector(st, snd)
	stale := result("a", agentprotocol.ProbeRetention+time.Minute)
	edge := result("a", agentprotocol.ProbeRetention-time.Minute)
	fresh := result("a", time.Minute)
	c.Record([]agentprotocol.ProbeResult{stale, edge, fresh})

	c.Flush(context.Background())
	require.Equal(t, []agentprotocol.ProbeResult{edge, fresh}, pending(t, st))

	snd.set(true, c.Ack)
	c.Flush(context.Background())
	require.Len(t, snd.sent(), 1)
	require.Equal(t, []agentprotocol.ProbeResult{edge, fresh}, snd.sent()[0].Results, "the dropped result is never sent")
}

func TestFramesAreBoundedByMaxProbeResults(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{connected: true}
	c := newCollector(st, snd)
	snd.ack = c.Ack

	total := agentprotocol.MaxProbeResults*2 + 7
	var all []agentprotocol.ProbeResult
	for i := range total { // one per second, oldest first
		all = append(all, agentprotocol.ProbeResult{
			ServiceID: fmt.Sprintf("svc-%d", i%3), At: now.Add(-time.Duration(total-i) * time.Second),
			Outcome: agentprotocol.ProbeUp, Replicas: 1, Passed: 1,
		})
	}
	c.Record(all)
	c.Flush(context.Background())

	frames := snd.sent()
	require.Len(t, frames, 3)
	var got []agentprotocol.ProbeResult
	for i, f := range frames {
		require.LessOrEqual(t, len(f.Results), agentprotocol.MaxProbeResults)
		require.EqualValues(t, i+1, f.Seq)
		got = append(got, f.Results...)
	}
	require.Len(t, frames[0].Results, agentprotocol.MaxProbeResults)
	require.Len(t, frames[2].Results, 7)
	require.Equal(t, all, got, "every result is sent once, in order")
	require.Empty(t, pending(t, st))
}

func TestAFrameThatIsNotAckedStopsTheDrain(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{connected: true}
	c := newCollector(st, snd)
	var all []agentprotocol.ProbeResult
	for i := range agentprotocol.MaxProbeResults + 1 {
		all = append(all, result(fmt.Sprintf("s%04d", i), time.Duration(i+1)*time.Second))
	}
	c.Record(all)

	c.Flush(context.Background())
	require.Len(t, snd.sent(), 1, "the second page waits for the first one's ack")
	require.Len(t, pending(t, st), len(all))
}

func TestRunSendsWhatRecordStores(t *testing.T) {
	st := openStore(t, t.TempDir())
	snd := &fakeSender{connected: true}
	c := NewCollector(Config{Buffer: st, Sender: snd, Now: func() time.Time { return now }, Interval: time.Hour})
	snd.ack = c.Ack

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	c.Record([]agentprotocol.ProbeResult{result("a", time.Second)})
	require.Eventually(t, func() bool {
		_, left, err := st.PendingProbes(10)
		return err == nil && len(snd.sent()) == 1 && len(left) == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestRecordOfNothingStoresNothing(t *testing.T) {
	st := openStore(t, t.TempDir())
	c := newCollector(st, &fakeSender{connected: true})
	c.Record(nil)
	require.Empty(t, pending(t, st))
}
