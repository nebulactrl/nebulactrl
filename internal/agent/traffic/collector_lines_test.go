package traffic

import (
	"bufio"
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// chunked is what the demultiplexed docker log stream holds for one message
// docker split: every chunk of dockerChunk bytes carries its own timestamp and
// only the last one ends in a newline.
func chunked(msg string, firstTS time.Time) string {
	var b strings.Builder
	ts := firstTS
	for len(msg) > dockerChunk {
		b.WriteString(ts.UTC().Format(time.RFC3339Nano) + " " + msg[:dockerChunk])
		msg = msg[dockerChunk:]
		ts = ts.Add(time.Microsecond)
	}
	b.WriteString(ts.UTC().Format(time.RFC3339Nano) + " " + msg + "\n")
	return b.String()
}

func TestConsumeReassemblesChunksDockerSplit(t *testing.T) {
	now := time.Unix(1761500090, 0).UTC()
	start := now.Add(-time.Second)
	msg := flowAccessLine(svcA, start, 5*time.Millisecond, "/"+strings.Repeat("a", 40<<10))
	require.Greater(t, len(msg), 2*dockerChunk, "the line spans three chunks")

	sink := &memSink{}
	c := flowCollector(t, now, sink, nil)
	stream := chunked(msg, start) + dockerLine(svcA, start, 5*time.Millisecond, "/next")
	require.NoError(t, c.consume(strings.NewReader(stream), time.Time{}))
	drainRequests(c)

	got := sink.recs[svcA]
	require.Len(t, got, 2, "the split line is one request, and the line after it is read")
	require.Equal(t, "/"+strings.Repeat("a", maxRequestPath-1), decodeRecord(t, got[0]).Path)
	require.Equal(t, "/next", decodeRecord(t, got[1]).Path)
	require.Zero(t, c.unparsed.Load())
	require.Zero(t, c.oversized.Load())
}

func TestReadLineUnchunk(t *testing.T) {
	ts := "2025-10-26T17:33:20.5Z"
	ts2 := "2025-10-26T17:33:20.6Z"
	body := strings.Repeat("x", dockerChunk)
	read := func(stream string) string {
		line, oversize, err := readLine(bufio.NewReader(strings.NewReader(stream)))
		require.NoError(t, err)
		require.False(t, oversize)
		return line
	}

	t.Run("two chunks", func(t *testing.T) {
		require.Equal(t, ts+" "+body+"tail", read(ts+" "+body+ts2+" tail\n"))
	})
	t.Run("a message of exactly one chunk is one line", func(t *testing.T) {
		require.Equal(t, ts+" "+body, read(ts+" "+body+"\n"))
	})
	t.Run("an empty last chunk", func(t *testing.T) {
		require.Equal(t, ts+" "+body, read(ts+" "+body+ts2+" \n"))
	})
	t.Run("a long line that is no chunking is kept", func(t *testing.T) {
		long := body + "no timestamp here"
		require.Equal(t, ts+" "+long, read(ts+" "+long+"\n"))
	})
	t.Run("a line without a docker timestamp is kept", func(t *testing.T) {
		long := "not-a-timestamp " + body + ts2 + " tail"
		require.Equal(t, long, read(long+"\n"))
	})
	t.Run("short lines are untouched", func(t *testing.T) {
		require.Equal(t, ts+" hello", read(ts+" hello\n"))
	})
}

func TestConsumeDropsAndCountsOverLimitLines(t *testing.T) {
	now := time.Unix(1761500090, 0).UTC()
	start := now.Add(-time.Second)
	prefix := start.UTC().Format(time.RFC3339Nano) + " "
	sink := &memSink{}
	c := flowCollector(t, now, sink, nil)

	huge := prefix + strings.Repeat("x", maxLineBytes) + "\n"
	stream := dockerLine(svcA, start, time.Millisecond, "/before") + "\n" + huge + dockerLine(svcA, start, time.Millisecond, "/after") + "\n"
	require.NoError(t, c.consume(strings.NewReader(stream), time.Time{}))
	drainRequests(c)

	require.EqualValues(t, 1, c.oversized.Load())
	got := sink.recs[svcA]
	require.Len(t, got, 2, "the lines around the over-limit one are read")
	require.Equal(t, "/before", decodeRecord(t, got[0]).Path)
	require.Equal(t, "/after", decodeRecord(t, got[1]).Path)

	// A line of exactly the limit is read.
	c.oversized.Store(0)
	msg := flowAccessLine(svcA, start, time.Millisecond, "/big")
	line := prefix + msg[:len(msg)-1] + `,"pad":"` + strings.Repeat("p", maxLineBytes-len(prefix)-len(msg)-9) + `"}`
	require.Len(t, line, maxLineBytes)
	require.NoError(t, c.consume(strings.NewReader(line+"\n"), time.Time{}))
	require.Zero(t, c.oversized.Load())
	require.Zero(t, c.unparsed.Load())
}

func TestUnparseableAccessLinesAreCountedAndReported(t *testing.T) {
	now := time.Unix(1761500090, 0).UTC()
	c := flowCollector(t, now, &memSink{}, nil)
	var out bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&out, nil))

	stream := "2025-10-26T17:33:20.5Z {\"DownstreamStatus\": 200, \"broken\n" + // an access entry that is not JSON
		"2025-10-26T17:33:20.5Z {\"level\":\"info\",\"msg\":\"proxy log line\"}\n" // not an access entry: not counted
	require.NoError(t, c.consume(strings.NewReader(stream), time.Time{}))
	c.oversized.Add(2)
	require.EqualValues(t, 1, c.unparsed.Load())

	c.reportDrops()
	require.Contains(t, out.String(), "access log: lines dropped")
	require.Contains(t, out.String(), "oversized=2")
	require.Contains(t, out.String(), "unparseable=1")
	require.Zero(t, c.oversized.Load())
	require.Zero(t, c.unparsed.Load())

	out.Reset()
	c.reportDrops()
	require.Empty(t, out.String(), "nothing dropped, nothing logged")
}
