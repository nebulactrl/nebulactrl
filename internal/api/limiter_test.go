package api

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLimiterAllowsTheBurstThenRefuses(t *testing.T) {
	l := NewIPLimiter(3, time.Hour) // one token per 20 minutes: nothing refills during the test
	for i := range 3 {
		require.True(t, l.Allow("198.51.100.7"), "event %d", i+1)
	}
	require.False(t, l.Allow("198.51.100.7"), "the fourth event inside the interval is refused")
	require.False(t, l.Allow("198.51.100.7"), "and stays refused")
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l := newIPLimiter(1, time.Hour)
	require.True(t, l.Allow("a"))
	require.False(t, l.Allow("a"))
	require.True(t, l.Allow("b"), "another visitor has their own allowance")
	require.False(t, l.Allow("b"))
	require.True(t, l.Allow(""), "an unknown address is a key like any other")
	require.False(t, l.Allow(""))
}

func TestLimiterPeekNeverSpends(t *testing.T) {
	l := newIPLimiter(2, time.Hour)
	require.True(t, l.Peek("k"), "a key never seen has its full allowance")
	require.True(t, l.Peek("k"))
	require.Empty(t, l.entries, "peeking records nothing")

	require.True(t, l.Allow("k"))
	require.True(t, l.Peek("k"), "one event left")
	require.True(t, l.Peek("k"), "peeking again still shows it")
	require.True(t, l.Allow("k"), "the peeked event was not spent")
	require.False(t, l.Peek("k"), "spent")
	require.False(t, l.Allow("k"))
	require.True(t, l.Peek("other"))
}

func TestLimiterForgetsIdleKeysOnItsNextCollection(t *testing.T) {
	l := newIPLimiter(1, time.Hour)
	require.True(t, l.Allow("idle"))
	require.True(t, l.Allow("recent"))

	// Nothing is collected while the last collection is recent.
	l.entries["idle"].seen = time.Now().Add(-time.Hour)
	require.True(t, l.Allow("new"))
	require.Contains(t, l.entries, "idle", "no collection yet: the first Allow just ran one")

	// Once the last collection is old enough, keys idle for over ten minutes go; recent ones stay.
	l.lastGC = time.Now().Add(-10 * time.Minute)
	l.entries["recent"].seen = time.Now().Add(-time.Minute)
	require.True(t, l.Allow("another"))
	require.NotContains(t, l.entries, "idle")
	require.Contains(t, l.entries, "recent")
	require.Contains(t, l.entries, "new")
	require.False(t, l.Allow("recent"), "a kept key keeps its spent allowance")
	require.True(t, l.Allow("idle"), "a forgotten key starts over with a full allowance")
}

func TestLimiterIsSafeForConcurrentUse(t *testing.T) {
	l := newIPLimiter(50, time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("shared") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
			l.Peek("shared")
		}()
	}
	wg.Wait()
	require.Equal(t, 50, allowed, "exactly the burst gets through, however the callers interleave")
}
