package logs

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type collector struct {
	mu     sync.Mutex
	frames []agentprotocol.LogFrame
}

func (c *collector) emit(f agentprotocol.LogFrame) {
	c.mu.Lock()
	c.frames = append(c.frames, f)
	c.mu.Unlock()
}

func (c *collector) snapshot() []agentprotocol.LogFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agentprotocol.LogFrame(nil), c.frames...)
}

func TestBatcherCapsFramesAtMaxLinesAndKeepsOrder(t *testing.T) {
	c := &collector{}
	b := NewBatcher(agentprotocol.LogFrame{DeploymentID: "d", ServiceID: "s", Stream: agentprotocol.StreamDeploy}, c.emit, nil)
	for i := range 450 {
		b.Add(strings.Repeat("x", i%5) + "|" + string(rune('a'+i%26)))
	}
	b.Close()

	frames := c.snapshot()
	total := 0
	for _, f := range frames {
		require.LessOrEqual(t, len(f.Lines), MaxLines)
		require.Equal(t, "d", f.DeploymentID)
		require.Equal(t, "s", f.ServiceID)
		require.Equal(t, agentprotocol.StreamDeploy, f.Stream)
		total += len(f.Lines)
	}
	require.Equal(t, 450, total)
	require.GreaterOrEqual(t, len(frames), 3)
	require.Equal(t, "|a", frames[0].Lines[0].Text)
}

func TestBatcherFlushesWithinInterval(t *testing.T) {
	c := &collector{}
	b := NewBatcher(agentprotocol.LogFrame{StreamID: "st", Stream: agentprotocol.StreamRuntime}, c.emit, nil)
	defer b.Close()
	b.Add("one")
	require.Eventually(t, func() bool { return len(c.snapshot()) == 1 }, 2*FlushInterval+300*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, "one", c.snapshot()[0].Lines[0].Text)
}

func TestBatcherRedactsSplitsLinesAndDropsAfterClose(t *testing.T) {
	c := &collector{}
	b := NewBatcher(agentprotocol.LogFrame{}, c.emit, func(s string) string { return strings.ReplaceAll(s, "sekret", "••••••") })
	b.Add("token=sekret\nsecond line\r\n")
	b.Close()
	b.Add("late")
	b.Close() // idempotent

	var lines []string
	for _, f := range c.snapshot() {
		for _, l := range f.Lines {
			lines = append(lines, l.Text)
		}
	}
	require.Equal(t, []string{"token=••••••", "second line"}, lines)
}

func TestReadLinesParsesTimestamps(t *testing.T) {
	in := "2026-10-25T10:00:00.123456789Z hello world\nno timestamp here\n2026-10-25T10:00:01Z last"
	var got []string
	var first time.Time
	require.NoError(t, ReadLines(strings.NewReader(in), func(ts time.Time, text string) {
		if first.IsZero() {
			first = ts
		}
		got = append(got, text)
	}))
	require.Equal(t, []string{"hello world", "no timestamp here", "last"}, got)
	require.Equal(t, 2026, first.Year())
}
