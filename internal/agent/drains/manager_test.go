package drains

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/drains/sinks"
	"github.com/nebulactrl/nebulactrl/internal/agent/logstore"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func init() {
	baseBackoff, maxBackoff, idleWait, persistEvery = 5*time.Millisecond, 40*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
}

// fakeSink records batches and fails on demand.
type fakeSink struct {
	mu       sync.Mutex
	batches  [][]sinks.Entry
	failNext int   // fail this many sends with a retryable error
	reject   bool  // fail with a permanent error
	err      error // what a retryable failure says
	status   int   // HTTP status of a retryable failure, 0 for a network error
}

func (f *fakeSink) Send(_ context.Context, b []sinks.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.reject:
		return &sinks.SendError{Err: errors.New("HTTP 400"), StatusCode: 400, Permanent: true}
	case f.failNext > 0:
		f.failNext--
		e := f.err
		if e == nil {
			e = errors.New("connection refused")
		}
		return &sinks.SendError{Err: e, StatusCode: f.status}
	}
	f.batches = append(f.batches, append([]sinks.Entry(nil), b...))
	return nil
}

func (f *fakeSink) Close() error { return nil }

func (f *fakeSink) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.batches {
		for _, e := range b {
			out = append(out, e.Text)
		}
	}
	return out
}

func (f *fakeSink) set(fn func(*fakeSink)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

type env struct {
	t    *testing.T
	st   *logstore.Store
	dir  string
	sink *fakeSink
	m    *Manager
	stop context.CancelFunc
	done chan struct{}
	wg   sync.WaitGroup
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := logstore.Open(logstore.Options{Dir: filepath.Join(dir, "logs"), BlockMaxLines: 20, FlushInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, dir: dir, sink: &fakeSink{}}
	e.start()
	t.Cleanup(e.shutdown)
	return e
}

func (e *env) start() {
	e.m = NewManager(e.st, filepath.Join(e.dir, "drains"), nil)
	e.m.NewSink = func(agentprotocol.LogDrain, map[string]string) (sinks.Sink, error) { return e.sink, nil }
	ctx, cancel := context.WithCancel(context.Background())
	e.stop, e.done = cancel, make(chan struct{})
	go func() { e.m.Run(ctx); close(e.done) }()
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.st.Run(ctx) }()
}

func (e *env) shutdown() {
	if e.stop != nil {
		e.stop()
		<-e.done
		e.wg.Wait()
		e.stop = nil
	}
}

func (e *env) write(svc string, n int, prefix string) {
	e.t.Helper()
	recs := make([]logstore.Record, n)
	for i := range recs {
		recs[i] = logstore.Record{TS: time.Now().UnixNano(), DeploymentID: "d1", Text: fmt.Sprintf("%s %d", prefix, i)}
	}
	if err := e.st.Append(svc, recs); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) apply(drains ...agentprotocol.LogDrain) { e.applyAt(time.Time{}, drains...) }

// applyAt is apply with the control plane's clock set to cpNow.
func (e *env) applyAt(cpNow time.Time, drains ...agentprotocol.LogDrain) {
	e.m.Apply(&agentprotocol.LogsConfig{
		Capture: true, ServerName: "edge-1", Now: cpNow, Drains: drains,
		Services: []agentprotocol.LogServiceMeta{
			{ServiceID: "web", Name: "Web", Slug: "web", ProjectID: "p1", Project: "Shop", EnvironmentID: "e1", Environment: "production"},
			{ServiceID: "blog", Name: "Blog", Slug: "blog", ProjectID: "p2", Project: "Blog", EnvironmentID: "e2", Environment: "production"},
		},
	}, nil)
}

func drain(id string) agentprotocol.LogDrain {
	return agentprotocol.LogDrain{
		ID: id, Name: id, Kind: agentprotocol.DrainHTTP, Enabled: true, AllProjects: true,
		HTTP: &agentprotocol.HTTPDrainConfig{URL: "https://intake.example.com"},
	}
}

func wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNewDrainStartsAtTheEndOfTheLogAndLabelsLines(t *testing.T) {
	e := newEnv(t)
	e.write("web", 5, "old")
	e.apply(drain("d1"))
	time.Sleep(80 * time.Millisecond)
	if got := e.sink.texts(); len(got) != 0 {
		t.Fatalf("history was shipped: %v", got)
	}
	if err := e.st.Append("web", []logstore.Record{
		{TS: time.Now().UnixNano(), Replica: 1, DeploymentID: "dep9", Text: `{"level":"error","msg":"x"}`},
	}); err != nil {
		t.Fatal(err)
	}
	wait(t, "the new line", func() bool { return len(e.sink.texts()) == 1 })
	got := e.sink.batches[0][0]
	if got.Service != "Web" || got.ServiceSlug != "web" || got.Project != "Shop" || got.Environment != "production" ||
		got.Server != "edge-1" || got.Level != "error" || got.Replica != 1 || got.DeploymentID != "dep9" || got.Stream != "runtime" {
		t.Fatalf("entry %+v", got)
	}
	// A service that appears later is shipped from its first line.
	e.write("blog", 2, "fresh")
	wait(t, "the new service", func() bool { return len(e.sink.texts()) == 3 })
}

// The collector can store a line long after the container wrote it. A drain made
// before that line was written must not ship it: it is stamped before the drain's
// creation, so it is history however late it arrived. Both a service the store
// already knows and one it has never seen (its first lines arrive after the drain
// was applied) are covered.
func TestLateStoredLinesStampedBeforeTheDrainAreNotShipped(t *testing.T) {
	e := newEnv(t)
	e.write("web", 1, "known")
	d := drain("d1")
	d.CreatedAt = time.Now()
	e.applyAt(time.Now(), d)
	time.Sleep(30 * time.Millisecond)
	old := time.Now().Add(-time.Minute).UnixNano()
	if err := e.st.Append("web", []logstore.Record{{TS: old, Text: "old web"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Append("blog", []logstore.Record{{TS: old, Text: "old blog"}}); err != nil {
		t.Fatal(err)
	}
	e.write("blog", 1, "fresh")
	wait(t, "the fresh line", func() bool { return len(e.sink.texts()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := e.sink.texts(); len(got) != 1 || got[0] != "fresh 0" {
		t.Fatalf("shipped %v", got)
	}
}

// The control plane's clock may be off: only the drain's age on that clock is
// used, so a line written after the drain ships and one before it does not.
func TestDrainStartIgnoresClockSkew(t *testing.T) {
	for _, skew := range []time.Duration{-3 * time.Hour, 3 * time.Hour} {
		e := newEnv(t)
		cpNow := time.Now().Add(skew)
		d := drain("d1")
		d.CreatedAt = cpNow.Add(-time.Hour) // created an hour before this config
		e.applyAt(cpNow, d)
		time.Sleep(30 * time.Millisecond)
		fromAfter := time.Now().Add(-30 * time.Minute).UnixNano()
		fromBefore := time.Now().Add(-90 * time.Minute).UnixNano()
		if err := e.st.Append("web", []logstore.Record{{TS: fromBefore, Text: "before"}, {TS: fromAfter, Text: "after"}}); err != nil {
			t.Fatal(err)
		}
		wait(t, "the line after the drain", func() bool { return len(e.sink.texts()) == 1 })
		time.Sleep(100 * time.Millisecond)
		if got := e.sink.texts(); len(got) != 1 || got[0] != "after" {
			t.Fatalf("skew %v: shipped %v", skew, got)
		}
	}
}

func TestDrainStart(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cp := now.Add(5 * time.Hour) // a skewed control plane clock
	for _, tc := range []struct {
		name            string
		cfgNow, created time.Time
		want            int64
	}{
		{"no clock", time.Time{}, cp, 0},
		{"no creation", cp, time.Time{}, 0},
		{"age on the agent clock", cp, cp.Add(-time.Minute), now.Add(-time.Minute).UnixNano()},
		{"created after the config was built", cp, cp.Add(time.Second), now.UnixNano()},
	} {
		if got := drainStart(tc.cfgNow, tc.created, now); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestScopeAndQueryFilters(t *testing.T) {
	e := newEnv(t)
	d := drain("d1")
	d.AllProjects, d.ProjectIDs, d.Query = false, []string{"p1"}, "level:error"
	e.apply(d)
	time.Sleep(30 * time.Millisecond)
	_ = e.st.Append("web", []logstore.Record{
		{TS: time.Now().UnixNano(), Text: "level=error disk full"},
		{TS: time.Now().UnixNano(), Text: "level=info all fine"},
	})
	_ = e.st.Append("blog", []logstore.Record{{TS: time.Now().UnixNano(), Text: "level=error other project"}})
	wait(t, "the matching line", func() bool { return len(e.sink.texts()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if got := e.sink.texts(); len(got) != 1 || got[0] != "level=error disk full" {
		t.Fatalf("shipped %v", got)
	}

	// Narrowed to a service and with an unmatched query: nothing, but the position advances.
	d.ProjectIDs, d.ServiceIDs = []string{"p1", "p2"}, []string{"blog"}
	e.apply(d)
	time.Sleep(30 * time.Millisecond)
	_ = e.st.Append("web", []logstore.Record{{TS: time.Now().UnixNano(), Text: "level=error web again"}})
	_ = e.st.Append("blog", []logstore.Record{{TS: time.Now().UnixNano(), Text: "level=error blog again"}})
	wait(t, "the blog line", func() bool { return len(e.sink.texts()) == 2 })
	if got := e.sink.texts(); got[1] != "level=error blog again" {
		t.Fatalf("shipped %v", got)
	}
}

func TestRetriesWithBackoffAndLosesNothing(t *testing.T) {
	e := newEnv(t)
	e.apply(drain("d1"))
	time.Sleep(30 * time.Millisecond)
	e.sink.set(func(f *fakeSink) { f.failNext = 7 })
	e.write("web", 30, "keep")
	wait(t, "a failing state", func() bool {
		s := e.m.Statuses()
		return len(s) == 1 && s[0].State == agentprotocol.DrainFailing && s[0].LastError != ""
	})
	wait(t, "delivery", func() bool { return len(e.sink.texts()) == 30 })
	got := e.sink.texts()
	for i, g := range got {
		if g != fmt.Sprintf("keep %d", i) {
			t.Fatalf("line %d is %q", i, g)
		}
	}
	wait(t, "ok again", func() bool { return e.m.Statuses()[0].State == agentprotocol.DrainOK })
	if s := e.m.Statuses()[0]; s.SentLines != 30 || s.LastSuccessAt == nil || s.LastError != "" {
		t.Fatalf("status %+v", s)
	}
}

func TestRejectedBatchesAreDroppedAndCounted(t *testing.T) {
	e := newEnv(t)
	e.apply(drain("d1"))
	time.Sleep(30 * time.Millisecond)
	e.sink.set(func(f *fakeSink) { f.reject = true })
	e.write("web", 3, "bad")
	wait(t, "the drop", func() bool { return e.m.Statuses()[0].DroppedLines == 3 })
	e.sink.set(func(f *fakeSink) { f.reject = false })
	e.write("web", 2, "good")
	wait(t, "good lines", func() bool { return len(e.sink.texts()) == 2 })
	if s := e.m.Statuses()[0]; s.DroppedLines != 3 || s.SentLines != 2 {
		t.Fatalf("status %+v", s)
	}
}

func TestPositionSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	e.apply(drain("d1"))
	time.Sleep(30 * time.Millisecond)
	e.write("web", 10, "first")
	wait(t, "first batch", func() bool { return len(e.sink.texts()) == 10 })
	time.Sleep(60 * time.Millisecond) // the position is persisted
	e.shutdown()

	// Written while the agent is down.
	e.write("web", 4, "second")
	if err := e.st.Flush(); err != nil {
		t.Fatal(err)
	}
	e.sink = &fakeSink{}
	e.start()
	e.apply(drain("d1"))
	wait(t, "the lines written while down", func() bool { return len(e.sink.texts()) >= 4 })
	time.Sleep(100 * time.Millisecond)
	got := e.sink.texts()
	if len(got) != 4 || got[0] != "second 0" || got[3] != "second 3" {
		t.Fatalf("after restart shipped %v", got)
	}
}

func TestBacklogBoundSkipsTheOldestAndCountsThem(t *testing.T) {
	e := newEnv(t)
	d := drain("d1")
	d.BufferMaxBytes = 4 << 10 // tiny
	e.apply(d)
	time.Sleep(30 * time.Millisecond)
	e.sink.set(func(f *fakeSink) { f.failNext = 1_000_000 }) // the destination is down
	for range 20 {
		e.write("web", 100, "padding padding padding padding padding padding")
	}
	wait(t, "the backlog to fit its bound", func() bool {
		s := e.m.Statuses()[0]
		return s.DroppedLines > 0 && s.BacklogBytes <= 8<<10
	})
	// The destination comes back: what is left is delivered, newest lines included.
	e.write("web", 1, "newest")
	e.sink.set(func(f *fakeSink) { f.failNext = 0 })
	wait(t, "the newest line", func() bool {
		got := e.sink.texts()
		return len(got) > 0 && got[len(got)-1] == "newest 0"
	})
	if s := e.m.Statuses()[0]; s.SentLines+s.DroppedLines != 2001 {
		t.Fatalf("sent %d + dropped %d != 2001 written", s.SentLines, s.DroppedLines)
	}
}

// The bound trims only as much of the oldest backlog as it must. Advancing the
// position to the end of the log whenever the backlog is over its bound would
// drop the lines written since the last pass (the newest ones too).
// No goroutines or timers: the interleaving that could lose the newest line is fixed here.
func TestBacklogBoundKeepsTheNewestLines(t *testing.T) {
	dir := t.TempDir()
	st, err := logstore.Open(logstore.Options{Dir: filepath.Join(dir, "logs"), BlockMaxLines: 20, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := NewManager(st, filepath.Join(dir, "drains"), nil)
	d := drain("d1")
	d.BufferMaxBytes = 4 << 10
	r := newRunner(m, d, nil, "", 0)
	sink := &fakeSink{}

	appendLines := func(n int, prefix string) {
		t.Helper()
		recs := make([]logstore.Record, n)
		for i := range recs {
			recs[i] = logstore.Record{TS: time.Now().UnixNano(), DeploymentID: "d1", Text: fmt.Sprintf("%s %d", prefix, i)}
		}
		if err := st.Append("web", recs); err != nil {
			t.Fatal(err)
		}
	}
	// The destination is down; the backlog outgrows the bound between two passes
	// (a pass is a backoff apart) and the newest line lands in the same window.
	sink.set(func(f *fakeSink) { f.failNext = 1 })
	appendLines(100, "padding padding padding padding padding padding")
	if _, _, err := r.pass(context.Background(), sink); err == nil {
		t.Fatal("the pass against a down destination succeeded")
	}
	appendLines(100, "padding padding padding padding padding padding")
	appendLines(1, "newest")
	// The destination is back: pass until nothing moves.
	for range 100 {
		moved, _, err := r.pass(context.Background(), sink)
		if err != nil {
			t.Fatal(err)
		}
		if !moved {
			break
		}
	}
	got := sink.texts()
	if len(got) == 0 || got[len(got)-1] != "newest 0" {
		t.Fatalf("the newest line was not delivered after the destination recovered; delivered %d lines", len(got))
	}
	if s := r.status(); s.DroppedLines == 0 || s.SentLines == 0 || s.SentLines+s.DroppedLines != 201 {
		t.Fatalf("sent %d + dropped %d != 201 written (or nothing dropped/sent)", s.SentLines, s.DroppedLines)
	}
	// Only the oldest lines were skipped: the bound left no more than it allows.
	if len(got) > 4<<10/50 {
		t.Fatalf("delivered %d lines, more than the 4 KiB bound can hold", len(got))
	}
}

func TestRemovedAndDisabledDrains(t *testing.T) {
	e := newEnv(t)
	e.apply(drain("d1"), drain("d2"))
	e.write("web", 1, "x")
	wait(t, "state file", func() bool {
		_, err := os.Stat(filepath.Join(e.dir, "drains", "d1.json"))
		return err == nil
	})

	off := drain("d2")
	off.Enabled = false
	e.apply(off) // d1 removed, d2 disabled
	st := e.m.Statuses()
	if len(st) != 1 || st[0].ID != "d2" || st[0].State != agentprotocol.DrainDisabled {
		t.Fatalf("statuses %+v", st)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "drains", "d1.json")); !os.IsNotExist(err) {
		t.Fatalf("the removed drain's position was kept (err=%v)", err)
	}
}

func TestTestSendsOneLineAndRedactsErrors(t *testing.T) {
	e := newEnv(t)
	e.apply()
	res := e.m.Test(context.Background(), drain("d1"), nil)
	if !res.OK || len(e.sink.texts()) != 1 {
		t.Fatalf("result %+v, shipped %v", res, e.sink.texts())
	}
	e.sink.set(func(f *fakeSink) {
		f.failNext, f.err = 1, errors.New("dial https://u:p@h/x?token=abcdef123456: refused, key hunter2hunter2")
	})
	secrets := map[string]string{agentprotocol.LogDrainSecretKey("d1", "token"): "hunter2hunter2"}
	res = e.m.Test(context.Background(), drain("d1"), secrets)
	if res.OK || res.Message == "" {
		t.Fatalf("result %+v", res)
	}
	for _, leak := range []string{"hunter2hunter2", "abcdef123456", "u:p@"} {
		if contains(res.Message, leak) {
			t.Fatalf("message leaks %q: %s", leak, res.Message)
		}
	}
}

func TestTestDoesNotReturnTheDestinationsAnswer(t *testing.T) {
	e := newEnv(t)
	e.apply()
	e.sink.set(func(f *fakeSink) {
		f.failNext, f.status, f.err = 1, 401, errors.New("HTTP 401 Unauthorized: internal-service-banner")
	})
	res := e.m.Test(context.Background(), drain("d1"), nil)
	if res.OK || res.StatusCode != 401 || contains(res.Message, "internal-service-banner") {
		t.Fatalf("result %+v", res)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSecretsAreMaskedBeforeLinesLeaveTheHost(t *testing.T) {
	e := newEnv(t)
	d := drain("d1")
	d.Query = "level:error" // a query cannot probe masked values either
	e.m.Apply(&agentprotocol.LogsConfig{
		Capture: true, ServerName: "edge-1", Drains: []agentprotocol.LogDrain{d},
		Services: []agentprotocol.LogServiceMeta{{ServiceID: "web", Name: "Web", ProjectID: "p1"}},
	}, map[string]string{agentprotocol.LogRedactSecretKey("web"): `["hunter2-secret"]`})
	time.Sleep(30 * time.Millisecond)
	_ = e.st.Append("web", []logstore.Record{
		{TS: time.Now().UnixNano(), Text: "level=error login failed password=hunter2-secret"},
		{TS: time.Now().UnixNano(), Text: "level=error url=https://x/?k=hunter2-secret end"},
		{TS: time.Now().UnixNano(), Text: "level=error b64=aHVudGVyMi1zZWNyZXQ= end"},
		{TS: time.Now().UnixNano(), Text: "level=info hunter2-secret"},
	})
	wait(t, "three lines", func() bool { return len(e.sink.texts()) == 3 })
	for _, got := range e.sink.texts() {
		if contains(got, "hunter2-secret") || contains(got, "aHVudGVyMi1zZWNyZXQ=") {
			t.Fatalf("a secret left the host: %q", got)
		}
		if !contains(got, "\u2022\u2022\u2022\u2022\u2022\u2022") {
			t.Fatalf("no mask in %q", got)
		}
	}
}
