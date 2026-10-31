package traffic

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fixtureTime is just after the minute holding the fixture lines.
var fixtureTime = time.Unix(1761500000, 0).UTC().Add(90 * time.Second)

type fakeSender struct {
	mu        sync.Mutex
	connected bool
	ack       func(seq uint64) // nil = never acknowledge
	frames    []agentprotocol.Traffic
}

func (f *fakeSender) Connected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }
func (f *fakeSender) Traffic(t agentprotocol.Traffic) {
	f.mu.Lock()
	f.frames = append(f.frames, t)
	ack := f.ack
	f.mu.Unlock()
	if ack != nil {
		ack(t.Seq)
	}
}
func (f *fakeSender) sent() []agentprotocol.Traffic {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentprotocol.Traffic(nil), f.frames...)
}

func newCollector(t *testing.T, snd *fakeSender, now time.Time) (*Collector, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	c := NewCollector(Config{Buffer: st, Sender: snd, Now: func() time.Time { return now }, AckTimeout: 50 * time.Millisecond})
	if snd.ack == nil && snd.connected {
		snd.ack = c.Ack
	}
	return c, st
}

func feedFixtures(t *testing.T, c *Collector) {
	t.Helper()
	for _, l := range fixtureLines(t) {
		// docker prefixes every stdout line with its RFC 3339 timestamp
		c.handleLine("2025-10-26T17:33:20.5Z "+string(l), time.Time{})
	}
}

func TestCollectorShipsCompletedMinutesAndForgetsAcked(t *testing.T) {
	snd := &fakeSender{connected: true}
	c, st := newCollector(t, snd, fixtureTime)
	feedFixtures(t, c)

	c.Flush(context.Background())
	frames := snd.sent()
	require.Len(t, frames, 1)
	require.EqualValues(t, 1, frames[0].Seq)
	require.Len(t, frames[0].Buckets, 2)

	_, pending, err := st.PendingTraffic(100)
	require.NoError(t, err)
	require.Empty(t, pending, "acknowledged buckets are deleted from the buffer")
	ft, ok := st.TrafficFlushedThrough()
	require.True(t, ok)
	require.Equal(t, time.Unix(1761499980, 0).UTC(), ft, "the cursor is the start of the last completed minute")
}

func TestCollectorBuffersWhileDisconnectedAndResends(t *testing.T) {
	snd := &fakeSender{connected: false}
	c, st := newCollector(t, snd, fixtureTime)
	feedFixtures(t, c)
	c.Flush(context.Background())
	require.Empty(t, snd.sent(), "nothing is sent while the socket is down")
	_, pending, err := st.PendingTraffic(100)
	require.NoError(t, err)
	require.Len(t, pending, 2, "completed minutes wait in bbolt")

	// Connected but the control plane never acknowledges: keep the buckets.
	snd.mu.Lock()
	snd.connected = true
	snd.mu.Unlock()
	c.Flush(context.Background())
	require.Len(t, snd.sent(), 1)
	_, pending, _ = st.PendingTraffic(100)
	require.Len(t, pending, 2, "unacknowledged frames are resent, not forgotten")

	// The next flush resends and this time it is acknowledged.
	snd.mu.Lock()
	snd.ack = c.Ack
	snd.mu.Unlock()
	c.Flush(context.Background())
	require.Len(t, snd.sent(), 2)
	require.Equal(t, snd.sent()[0].Buckets, snd.sent()[1].Buckets)
	_, pending, _ = st.PendingTraffic(100)
	require.Empty(t, pending)
}

func TestCollectorDropsLateLinesOfFlushedMinutes(t *testing.T) {
	snd := &fakeSender{connected: true}
	c, st := newCollector(t, snd, fixtureTime)
	feedFixtures(t, c)
	c.Flush(context.Background())

	feedFixtures(t, c) // the same minute again, after it was flushed
	c.Flush(context.Background())
	require.Len(t, snd.sent(), 1, "a flushed minute is never re-counted")
	_, pending, _ := st.PendingTraffic(100)
	require.Empty(t, pending)
}

func TestCollectorSkipsLinesBeforeResumePoint(t *testing.T) {
	snd := &fakeSender{connected: true}
	c, _ := newCollector(t, snd, fixtureTime)
	skipBefore := time.Unix(1761500002, 0).UTC() // after the first two fixture lines
	for _, l := range fixtureLines(t) {
		// docker prefixes every stdout line with its RFC 3339 timestamp
		c.handleLine("2025-10-26T17:33:20.5Z "+string(l), skipBefore)
	}
	c.Flush(context.Background())
	b := snd.sent()[0].Buckets
	var total int64
	for _, bk := range b {
		total += bk.Requests
	}
	require.EqualValues(t, 2, total, "lines at or before the cursor are not counted twice")
}

func TestStoreBoundsUnsentBuffer(t *testing.T) {
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	old := agentprotocol.TrafficBucket{Host: "a.example.com", Bucket: time.Now().Add(-30 * time.Hour).Truncate(time.Minute), LatHist: make([]int64, 28)}
	fresh := agentprotocol.TrafficBucket{Host: "a.example.com", Bucket: time.Now().Add(-2 * time.Hour).Truncate(time.Minute), LatHist: make([]int64, 28)}
	require.NoError(t, st.PutTraffic([]agentprotocol.TrafficBucket{old, fresh}, time.Time{}))
	n, err := st.PruneTraffic(time.Now().Add(-agentprotocol.TrafficRetention))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, left, err := st.PendingTraffic(10)
	require.NoError(t, err)
	require.Len(t, left, 1)
	require.Equal(t, fresh.Bucket.UTC(), left[0].Bucket.UTC())
}

func TestStoreSaltIsStablePerDayAndRotates(t *testing.T) {
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	gen := func() ([]byte, error) { return []byte(strings.Repeat("x", 32)), nil }
	a, err := st.TrafficSalt("2026-01-02", gen)
	require.NoError(t, err)
	n := 0
	b, err := st.TrafficSalt("2026-01-02", func() ([]byte, error) { n++; return []byte("different-different-different-12"), nil })
	require.NoError(t, err)
	require.Equal(t, a, b, "a restart on the same day keeps the salt")
	require.Zero(t, n)
	_, err = st.TrafficSalt("2026-01-05", gen)
	require.NoError(t, err)
	c, err := st.TrafficSalt("2026-01-02", func() ([]byte, error) { return []byte("regenerated-regenerated-regener"), nil })
	require.NoError(t, err)
	require.NotEqual(t, a, c, "salts of days before yesterday are deleted")
}

type logsOnlyEngine struct {
	docker.Engine
	body string
	opts docker.LogOptions
}

func (e *logsOnlyEngine) ContainerLogs(_ context.Context, _ string, o docker.LogOptions) (io.ReadCloser, error) {
	e.opts = o
	return io.NopCloser(strings.NewReader(e.body)), nil
}

func TestTailReadsStdoutSinceCursor(t *testing.T) {
	var sb strings.Builder
	for _, l := range fixtureLines(t) {
		sb.WriteString("2025-10-26T17:33:20.5Z " + string(l) + "\n")
	}
	eng := &logsOnlyEngine{body: sb.String()}
	snd := &fakeSender{connected: true}
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer st.Close()
	c := NewCollector(Config{Engine: eng, Buffer: st, Sender: snd, Now: func() time.Time { return fixtureTime }})
	c.lastSeen = time.Unix(1761499999, 0).UTC()

	require.NoError(t, c.tail(context.Background()))
	require.True(t, eng.opts.Follow)
	require.True(t, eng.opts.StdoutOnly, "the proxy's stdout carries only access lines")
	require.Equal(t, time.Unix(1761499998, 0).UTC(), eng.opts.Since.UTC(), "resume one second before the cursor")
	got := c.agg.Flush(fixtureTime)
	var total int64
	for _, b := range got {
		total += b.Requests
	}
	require.EqualValues(t, 5, total, "five counted lines in the fixture")
}
