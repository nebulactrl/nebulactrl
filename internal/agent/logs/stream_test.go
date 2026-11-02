package logs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// logEngine serves ContainerLogs from per-container scripts. The rest of the
// docker.Engine surface is the nil embedded interface: a call to anything else
// panics, which is what the code under test must never do.
type logEngine struct {
	docker.Engine
	mu     sync.Mutex
	opts   map[string]docker.LogOptions
	open   map[string]func(ctx context.Context) (io.ReadCloser, error)
	closed map[string]bool
}

func newLogEngine() *logEngine {
	return &logEngine{
		opts:   map[string]docker.LogOptions{},
		open:   map[string]func(context.Context) (io.ReadCloser, error){},
		closed: map[string]bool{},
	}
}

func (e *logEngine) ContainerLogs(ctx context.Context, id string, o docker.LogOptions) (io.ReadCloser, error) {
	e.mu.Lock()
	e.opts[id] = o
	fn := e.open[id]
	e.mu.Unlock()
	if fn == nil {
		return nil, docker.ErrNotFound
	}
	rc, err := fn(ctx)
	if err != nil {
		return nil, err
	}
	return &trackedCloser{ReadCloser: rc, onClose: func() { e.mu.Lock(); e.closed[id] = true; e.mu.Unlock() }}, nil
}

func (e *logEngine) wasClosed(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed[id]
}

func (e *logEngine) options(id string) docker.LogOptions {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opts[id]
}

type trackedCloser struct {
	io.ReadCloser
	onClose func()
}

func (c *trackedCloser) Close() error { c.onClose(); return c.ReadCloser.Close() }

// static returns a script that serves fixed text.
func static(text string) func(context.Context) (io.ReadCloser, error) {
	return func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(text)), nil }
}

// follow returns a script whose stream stays open until the context ends,
// like a followed container log. The writer lets a test push lines.
func follow(w *io.PipeWriter, r *io.PipeReader) func(context.Context) (io.ReadCloser, error) {
	return func(ctx context.Context) (io.ReadCloser, error) {
		go func() { <-ctx.Done(); _ = w.Close() }()
		return r, nil
	}
}

type sinkEvents struct {
	mu     sync.Mutex
	frames []agentprotocol.LogFrame
	ends   chan agentprotocol.LogEnd
}

func newSink() *sinkEvents { return &sinkEvents{ends: make(chan agentprotocol.LogEnd, 8)} }

func (s *sinkEvents) Log(f agentprotocol.LogFrame) {
	s.mu.Lock()
	s.frames = append(s.frames, f)
	s.mu.Unlock()
}
func (s *sinkEvents) LogEnd(e agentprotocol.LogEnd) { s.ends <- e }

func (s *sinkEvents) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, f := range s.frames {
		for _, l := range f.Lines {
			out = append(out, l.Text)
		}
	}
	return out
}

func (s *sinkEvents) waitEnd(t *testing.T) agentprotocol.LogEnd {
	t.Helper()
	select {
	case e := <-s.ends:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never ended")
		return agentprotocol.LogEnd{}
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestReadLinesEdgeCases(t *testing.T) {
	t.Run("empty input calls nothing", func(t *testing.T) {
		calls := 0
		require.NoError(t, ReadLines(strings.NewReader(""), func(time.Time, string) { calls++ }))
		assert.Zero(t, calls)
	})
	t.Run("blank lines are delivered as empty text", func(t *testing.T) {
		var got []string
		require.NoError(t, ReadLines(strings.NewReader("a\n\nb\n"), func(_ time.Time, s string) { got = append(got, s) }))
		assert.Equal(t, []string{"a", "", "b"}, got)
	})
	t.Run("a line longer than the buffer is cut and the rest dropped", func(t *testing.T) {
		long := strings.Repeat("x", 200<<10)
		var got []string
		require.NoError(t, ReadLines(strings.NewReader(long+"\nnext\n"), func(_ time.Time, s string) { got = append(got, s) }))
		require.Len(t, got, 2)
		assert.Equal(t, 64<<10, len(got[0]), "only the first buffer-full of the line is kept")
		assert.Equal(t, "next", got[1], "the line after it is intact")
	})
	t.Run("a read error is returned after the lines read so far", func(t *testing.T) {
		boom := errors.New("connection reset")
		r := io.MultiReader(strings.NewReader("one\ntwo\n"), iotest.ErrReader(boom))
		var got []string
		err := ReadLines(r, func(_ time.Time, s string) { got = append(got, s) })
		require.ErrorIs(t, err, boom)
		assert.Equal(t, []string{"one", "two"}, got)
	})
	t.Run("a partial last line before an error is still delivered", func(t *testing.T) {
		boom := errors.New("broken pipe")
		r := io.MultiReader(strings.NewReader("done\npartial"), iotest.ErrReader(boom))
		var got []string
		err := ReadLines(r, func(_ time.Time, s string) { got = append(got, s) })
		require.ErrorIs(t, err, boom)
		assert.Equal(t, []string{"done", "partial"}, got)
	})
	t.Run("timestamps are returned per line", func(t *testing.T) {
		var tss []time.Time
		require.NoError(t, ReadLines(strings.NewReader("2026-10-25T10:00:00Z a\nplain\n"), func(ts time.Time, _ string) { tss = append(tss, ts) }))
		require.Len(t, tss, 2)
		assert.True(t, time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC).Equal(tss[0]))
		assert.True(t, tss[1].IsZero())
	})
}

func TestTail(t *testing.T) {
	eng := newLogEngine()
	eng.open["c"] = static("2026-10-25T10:00:00Z one\n2026-10-25T10:00:01Z two\n2026-10-25T10:00:02Z three\n")

	got, err := Tail(context.Background(), eng, "c", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"two", "three"}, got, "timestamps stripped, only the last n kept")
	assert.Equal(t, docker.LogOptions{Tail: 2}, eng.options("c"))
	assert.True(t, eng.wasClosed("c"), "the log stream is closed")

	got, err = Tail(context.Background(), eng, "c", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "three"}, got)
}

func TestTailErrors(t *testing.T) {
	eng := newLogEngine()
	_, err := Tail(context.Background(), eng, "missing", 5)
	require.ErrorIs(t, err, docker.ErrNotFound)

	boom := errors.New("stream broke")
	eng.open["c"] = func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(strings.NewReader("a\nb\n"), iotest.ErrReader(boom))), nil
	}
	got, err := Tail(context.Background(), eng, "c", 5)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, []string{"a", "b"}, got, "lines read before the error are returned with it")
}

func TestStartRejectsNoSources(t *testing.T) {
	s := NewSubscriptions(newLogEngine(), newSink(), quiet())
	err := s.Start(context.Background(), nil, agentprotocol.LogsSubscribe{StreamID: "x"})
	require.EqualError(t, err, "no containers to stream")
}

func TestStartTailsEverySourceMergedWithPrefixesThenEnds(t *testing.T) {
	eng := newLogEngine()
	eng.open["c1"] = static("2026-10-25T10:00:00Z hello\n")
	eng.open["c2"] = static("2026-10-25T10:00:01Z world\n")
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())

	err := s.Start(context.Background(), []Source{{ContainerID: "c1", Prefix: "[web-0] "}, {ContainerID: "c2", Prefix: "[web-1] "}},
		agentprotocol.LogsSubscribe{StreamID: "st-1", ServiceID: "svc-1"})
	require.NoError(t, err)

	end := sink.waitEnd(t)
	assert.Equal(t, agentprotocol.LogEnd{StreamID: "st-1"}, end)
	assert.ElementsMatch(t, []string{"[web-0] hello", "[web-1] world"}, sink.lines())
	for _, f := range sink.frames {
		assert.Equal(t, "st-1", f.StreamID)
		assert.Equal(t, "svc-1", f.ServiceID)
		assert.Equal(t, agentprotocol.StreamRuntime, f.Stream)
	}
	// Tail defaults, no follow.
	assert.Equal(t, docker.LogOptions{Tail: DefaultTail}, eng.options("c1"))
	assert.Equal(t, docker.LogOptions{Tail: DefaultTail}, eng.options("c2"))
	assert.Eventually(t, func() bool { return eng.wasClosed("c1") && eng.wasClosed("c2") }, 5*time.Second, time.Millisecond)

	select {
	case extra := <-sink.ends:
		t.Fatalf("a second LogEnd arrived: %+v", extra)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestStartPassesTailAndFollowToDocker(t *testing.T) {
	eng := newLogEngine()
	eng.open["c"] = static("")
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())
	require.NoError(t, s.Start(context.Background(), []Source{{ContainerID: "c"}}, agentprotocol.LogsSubscribe{StreamID: "s", Tail: 7, Follow: true}))
	sink.waitEnd(t)
	assert.Equal(t, docker.LogOptions{Tail: 7, Follow: true}, eng.options("c"))
}

func TestStartFailureToOpenOneSourceClosesTheOthersAndStartsNothing(t *testing.T) {
	eng := newLogEngine()
	pr, pw := io.Pipe()
	eng.open["c1"] = func(context.Context) (io.ReadCloser, error) { return pr, nil }
	// c2 is unknown: ContainerLogs fails with ErrNotFound.
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())

	err := s.Start(context.Background(), []Source{{ContainerID: "c1"}, {ContainerID: "c2"}}, agentprotocol.LogsSubscribe{StreamID: "s", Follow: true})
	require.ErrorIs(t, err, docker.ErrNotFound)
	assert.True(t, eng.wasClosed("c1"), "the stream that did open is closed again")
	_, werr := pw.Write([]byte("x\n"))
	assert.Error(t, werr, "nobody reads the first stream any more")
	select {
	case e := <-sink.ends:
		t.Fatalf("a failed Start must not emit LogEnd: %+v", e)
	case <-time.After(20 * time.Millisecond):
	}
	s.Stop("s") // nothing registered: harmless
}

func TestStopEndsAFollowedStreamWithoutError(t *testing.T) {
	eng := newLogEngine()
	pr, pw := io.Pipe()
	eng.open["c"] = follow(pw, pr)
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())
	require.NoError(t, s.Start(context.Background(), []Source{{ContainerID: "c", Prefix: "> "}}, agentprotocol.LogsSubscribe{StreamID: "live", Follow: true}))

	_, err := pw.Write([]byte("2026-10-25T10:00:00Z first\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(sink.lines()) == 1 }, 5*time.Second, 5*time.Millisecond)

	s.Stop("live")
	end := sink.waitEnd(t)
	assert.Equal(t, agentprotocol.LogEnd{StreamID: "live"}, end, "a cancelled stream ends cleanly")
	assert.Equal(t, []string{"> first"}, sink.lines())
	assert.True(t, eng.wasClosed("c"))

	// The registration is gone: a second Stop is a no-op and the same ID can be reused.
	s.Stop("live")
	eng.open["c"] = static("again\n")
	require.NoError(t, s.Start(context.Background(), []Source{{ContainerID: "c"}}, agentprotocol.LogsSubscribe{StreamID: "live"}))
	sink.waitEnd(t)
}

func TestStopUnknownStreamIsIgnored(t *testing.T) {
	NewSubscriptions(newLogEngine(), newSink(), nil).Stop("nope")
}

func TestParentContextCancelEndsTheStream(t *testing.T) {
	eng := newLogEngine()
	pr, pw := io.Pipe()
	eng.open["c"] = follow(pw, pr)
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx, []Source{{ContainerID: "c"}}, agentprotocol.LogsSubscribe{StreamID: "p", Follow: true}))
	cancel()
	assert.Equal(t, agentprotocol.LogEnd{StreamID: "p"}, sink.waitEnd(t))
}

func TestReadErrorEndsTheStreamWithTheErrorOnce(t *testing.T) {
	eng := newLogEngine()
	boom := errors.New("docker hung up")
	eng.open["bad1"] = func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(strings.NewReader("before\n"), iotest.ErrReader(boom))), nil
	}
	eng.open["bad2"] = func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(iotest.ErrReader(errors.New("second failure"))), nil
	}
	eng.open["ok"] = static("fine\n")
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())
	require.NoError(t, s.Start(context.Background(), []Source{{ContainerID: "ok"}, {ContainerID: "bad1"}, {ContainerID: "bad2"}}, agentprotocol.LogsSubscribe{StreamID: "e"}))

	end := sink.waitEnd(t)
	assert.Equal(t, "e", end.StreamID)
	assert.Contains(t, []string{"docker hung up", "second failure"}, end.Error, "the first failure is reported")
	assert.Contains(t, sink.lines(), "fine", "the healthy source still delivered its lines")
	assert.Contains(t, sink.lines(), "before")
}

func TestReusingAStreamIDReplacesTheEarlierSubscription(t *testing.T) {
	eng := newLogEngine()
	pr1, pw1 := io.Pipe()
	eng.open["old"] = follow(pw1, pr1)
	eng.open["new"] = static("fresh\n")
	sink := newSink()
	s := NewSubscriptions(eng, sink, quiet())

	require.NoError(t, s.Start(context.Background(), []Source{{ContainerID: "old"}}, agentprotocol.LogsSubscribe{StreamID: "same", Follow: true}))
	require.NoError(t, s.Start(context.Background(), []Source{{ContainerID: "new"}}, agentprotocol.LogsSubscribe{StreamID: "same"}))

	// Both subscriptions end: the replaced one because it was cancelled, the new one because it finished.
	a, b := sink.waitEnd(t), sink.waitEnd(t)
	assert.Equal(t, "same", a.StreamID)
	assert.Equal(t, "same", b.StreamID)
	assert.Empty(t, a.Error)
	assert.Empty(t, b.Error)
	assert.True(t, eng.wasClosed("old"), "the replaced subscription's stream is closed")
	assert.Contains(t, sink.lines(), "fresh")
}
