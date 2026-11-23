package traffic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Observe sees exactly the events the aggregation counts: the replay gate
// (cursor and flushed minutes) applies to it as to the counts, so a restart
// that re-reads the access log does not strike twice.
func TestObserveFollowsTheReplayGateOfTheAggregation(t *testing.T) {
	snd := &fakeSender{connected: true}
	c, _ := newCollector(t, snd, fixtureTime)
	var seen []Event
	c.cfg.Observe = func(ev Event) { seen = append(seen, ev) }

	lines := fixtureLines(t)
	for _, l := range lines {
		c.handleLine("2025-10-26T17:33:20.5Z "+string(l), time.Time{})
	}
	all := len(seen)
	require.NotZero(t, all, "counted events are observed")

	// Lines at or before the cursor are not counted, so not observed.
	seen = nil
	for _, l := range lines {
		c.handleLine("2025-10-26T17:33:20.5Z "+string(l), time.Unix(1761500002, 0).UTC())
	}
	require.Less(t, len(seen), all)

	// Late lines of a flushed minute are dropped by the aggregation and not observed.
	c.Flush(context.Background())
	seen = nil
	for _, l := range lines {
		c.handleLine("2025-10-26T17:33:20.5Z "+string(l), time.Time{})
	}
	require.Empty(t, seen)
}
