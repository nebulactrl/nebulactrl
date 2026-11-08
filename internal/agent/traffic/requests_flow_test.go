package traffic

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const svcExt = "44444444-4444-4444-4444-444444444444"

func flowRoutes() []agentprotocol.Route {
	return []agentprotocol.Route{
		{ServiceID: svcA},
		{ServiceID: svcB},
		{ServiceID: svcExt, RouteExt: agentprotocol.RouteExt{Kind: agentprotocol.RouteKindExternal}},
	}
}

// flowAccessLine is a Traefik access-log line of a request that started at start
// and took d, served by the router of svc.
func flowAccessLine(svc string, start time.Time, d time.Duration, path string) string {
	return fmt.Sprintf(`{"StartUTC":%q,"RouterName":"nc-%s-https@file","RequestHost":"a.example.com","RequestPath":%q,`+
		`"RequestMethod":"GET","DownstreamStatus":200,"Duration":%d,"ServiceAddr":"10.0.0.5:80","downstream_Content-Type":"text/html"}`,
		start.UTC().Format(time.RFC3339Nano), svc, path, d.Nanoseconds())
}

// dockerLine prefixes an access line with the timestamp docker adds (the time
// Traefik wrote it: the end of the request).
func dockerLine(svc string, start time.Time, d time.Duration, path string) string {
	return start.Add(d).UTC().Format(time.RFC3339Nano) + " " + flowAccessLine(svc, start, d, path)
}

func flowCollector(t *testing.T, now time.Time, sink *memSink, resume map[string]time.Time) *Collector {
	t.Helper()
	st, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	cfg := &agentprotocol.RequestLogsConfig{}
	c := NewCollector(Config{
		Buffer: st, Sender: &fakeSender{}, Now: func() time.Time { return now },
		Requests: sink, RequestLogs: func() *agentprotocol.RequestLogsConfig { return cfg },
		Routes:         flowRoutes,
		RequestsResume: func() map[string]time.Time { return resume },
		Logger:         slog.Default(),
	})
	c.reqs.resume = resume
	return c
}

// drainRequests writes what the collector queued, as the writer does on shutdown.
func drainRequests(c *Collector) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.reqs.Run(ctx)
}

func TestRequestStoredWhenItStartedBeforeTheFlushedMinutes(t *testing.T) {
	now := time.Unix(1761500090, 0).UTC() // 17:34:50
	sink := &memSink{}
	c := flowCollector(t, now, sink, nil)
	c.mu.Lock()
	c.flushedUpTo = now.Truncate(time.Minute) // the aggregator is past 17:34:00
	c.mu.Unlock()

	start := now.Add(-70 * time.Second) // 17:33:40, before flushedUpTo and the cursor
	d := 70 * time.Second               // a websocket: ends 17:34:50
	c.handleLine(dockerLine(svcA, start, d, "/ws"), now.Add(-time.Second))
	drainRequests(c)

	got := sink.recs[svcA]
	require.Len(t, got, 1, "the late-minute gate of the aggregator does not apply to request lines")
	require.Equal(t, start.Add(d).UnixNano(), got[0].TS, "the line is stamped with start + duration")
	require.InDelta(t, 70000, decodeRecord(t, got[0]).DurationMs, 0.001)

	c.Flush(context.Background())
	_, pending, err := c.cfg.Buffer.PendingTraffic(10)
	require.NoError(t, err)
	require.Empty(t, pending, "the aggregator still drops the late request")
}

func TestRequestEndsAfterTheStartCursorOfTheNextStream(t *testing.T) {
	now := time.Unix(1761500090, 0).UTC()
	sink := &memSink{}
	c := flowCollector(t, now, sink, nil)
	start := now.Add(-time.Minute)
	// The cursor (skipBefore) is after the request's start: counted before.
	c.handleLine(dockerLine(svcA, start, 90*time.Second, "/long"), start.Add(10*time.Second))
	drainRequests(c)
	require.Len(t, sink.recs[svcA], 1)
}

func TestRequestResumeIsPerService(t *testing.T) {
	base := time.Unix(1761500000, 0).UTC()
	now := base.Add(5 * time.Minute)
	sink := &memSink{}
	// svcA's newest stored line ended at base+60s; svcB is quiet: its newest
	// stored line is an hour older.
	resumeA := base.Add(60 * time.Second)
	c := flowCollector(t, now, sink, map[string]time.Time{svcA: resumeA, svcB: base.Add(-time.Hour)})
	line := func(svc string, end time.Time, path string) string {
		d := 100 * time.Millisecond
		return dockerLine(svc, end.Add(-d), d, path)
	}
	for _, l := range []string{
		line(svcA, resumeA.Add(-time.Second), "/a-old"),                             // stored by the earlier run
		line(svcA, resumeA, "/a-equal"),                                             // ends exactly at the resume point: the stored line itself
		line(svcB, resumeA.Add(-30*time.Second), "/b-between"),                      // older than svcA's newest, newer than svcB's: stored
		line(svcA, resumeA.Add(2*time.Second), "/a-new"),                            // after the restart
		line(svcB, base.Add(-2*time.Hour), "/b-ancient"),                            // stored by the earlier run
		dockerLine(svcA, resumeA.Add(-2*time.Second), 5*time.Second, "/a-inflight"), // started before, ended after the resume point
	} {
		c.handleLine(l, time.Time{})
	}
	drainRequests(c)

	paths := func(svc string) (out []string) {
		for _, r := range sink.recs[svc] {
			out = append(out, decodeRecord(t, r).Path)
		}
		return out
	}
	require.Equal(t, []string{"/a-new", "/a-inflight"}, paths(svcA))
	require.Equal(t, []string{"/b-between"}, paths(svcB), "a quiet service's newer line is not dropped by another service's resume point")
}

func TestReconnectedStreamDoesNotStoreRequestsTwice(t *testing.T) {
	base := time.Unix(1761500000, 0).UTC()
	sink := &memSink{}
	var sb strings.Builder
	for i := range 5 {
		sb.WriteString(dockerLine(svcA, base.Add(time.Duration(i)*time.Second), 10*time.Millisecond, fmt.Sprintf("/p%d", i)) + "\n")
	}
	eng := &logsOnlyEngine{body: sb.String()}
	c := flowCollector(t, base.Add(time.Minute), sink, nil)
	c.cfg.Engine = eng

	require.NoError(t, c.tail(context.Background()))
	// The stream drops and docker replays the window: the same lines come again,
	// followed by a new one.
	eng.body += dockerLine(svcA, base.Add(9*time.Second), 10*time.Millisecond, "/p-new") + "\n"
	require.NoError(t, c.tail(context.Background()))
	drainRequests(c)

	require.Len(t, sink.recs[svcA], 6, "five lines once, the new one once")
	require.Equal(t, "/p-new", decodeRecord(t, sink.recs[svcA][5]).Path)
}

func TestRunStoresInFlightRequestAfterRestartOnce(t *testing.T) {
	base := time.Unix(1761500000, 0).UTC()
	now := base.Add(5 * time.Minute)
	sink := &memSink{}
	resumeA := base.Add(time.Minute)
	// A long request that started before the restart and ended after the
	// newest stored line; plus the stored line itself, replayed by docker.
	body := dockerLine(svcA, resumeA.Add(-10*time.Second), 10*time.Second, "/stored") + "\n" +
		dockerLine(svcA, now.Add(-3*time.Minute), 3*time.Minute+time.Second, "/inflight") + "\n"
	eng := &logsOnlyEngine{body: body}
	c := flowCollector(t, now, sink, map[string]time.Time{svcA: resumeA})
	c.cfg.Engine = eng

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.recs[svcA]) > 0
	}, 5*time.Second, 20*time.Millisecond)
	cancel()
	<-done

	require.Len(t, sink.recs[svcA], 1)
	rec := sink.recs[svcA][0]
	require.Equal(t, "/inflight", decodeRecord(t, rec).Path)
	require.Equal(t, now.Add(time.Second).UnixNano(), rec.TS)
}

func TestRequestWriterSkipsExternalRoutes(t *testing.T) {
	at := time.Unix(1761500000, 0).UTC()
	sink := &memSink{}
	cfg := &agentprotocol.RequestLogsConfig{}
	res := newRouteResolver(flowRoutes, nil, func() time.Time { return at }, slog.Default())
	w := newRequestWriter(sink, func() *agentprotocol.RequestLogsConfig { return cfg }, res, slog.Default())
	ev := func(route string) Event {
		return Event{Time: at, End: at, ServiceID: route, Host: "a.example.com", Path: "/", Method: "GET", Status: 200, Upstream: "203.0.113.5:443"}
	}
	w.Add(ev(svcExt))
	w.Add(ev(svcA))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)

	require.Len(t, sink.recs, 1)
	require.Len(t, sink.recs[svcA], 1, "an external route's requests are not stored: nothing reads them")
}
