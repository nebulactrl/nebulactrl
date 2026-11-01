package traffic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// With the production interval a flush lands flushGrace after every minute
// boundary, so a completed minute is shipped within ~one minute of ending.
func TestNextFlushDelayAlignsToMinuteBoundary(t *testing.T) {
	base := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"mid minute", base.Add(20 * time.Second), 40*time.Second + flushGrace + 100*time.Millisecond},
		{"before grace", base.Add(2 * time.Second), flushGrace + 100*time.Millisecond - 2*time.Second},
		{"just after the flush point", base.Add(flushGrace + 150*time.Millisecond), 60*time.Second - 50*time.Millisecond},
		{"at the boundary", base, flushGrace + 100*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCollector(Config{Now: func() time.Time { return tc.now }})
			require.Equal(t, tc.want, c.nextFlushDelay())
		})
	}

	t.Run("custom interval ticks as given", func(t *testing.T) {
		c := NewCollector(Config{Interval: 250 * time.Millisecond, Now: func() time.Time { return base }})
		require.Equal(t, 250*time.Millisecond, c.nextFlushDelay())
	})
}
