package logstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func openTest(t *testing.T, dir string, ck *clock, mod func(*Options)) *Store {
	t.Helper()
	o := Options{Dir: dir, Now: ck.now, BlockMaxLines: 10, FlushInterval: time.Hour}
	if mod != nil {
		mod(&o)
	}
	st, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func lines(n int, from time.Time, prefix string) []Record {
	out := make([]Record, n)
	for i := range out {
		out[i] = Record{TS: from.Add(time.Duration(i) * time.Second).UnixNano(), Replica: i % 2, DeploymentID: "dep1", Text: fmt.Sprintf("%s %d", prefix, i)}
	}
	return out
}

func collect(t *testing.T, st *Store, svc string, o ScanOptions) ([]Record, ScanResult) {
	t.Helper()
	var got []Record
	res, err := st.Scan(svc, o, func(r Record) bool { got = append(got, r); return true })
	if err != nil {
		t.Fatal(err)
	}
	return got, res
}

func TestAppendScanOrderCursorAndRange(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, nil)
	base := ck.now()
	if err := st.Append("svc1", lines(25, base, "line")); err != nil { // 2 full blocks + 5 pending
		t.Fatal(err)
	}

	asc, res := collect(t, st, "svc1", ScanOptions{})
	if !res.Done || len(asc) != 25 {
		t.Fatalf("asc: done=%v n=%d", res.Done, len(asc))
	}
	for i, r := range asc {
		if r.Text != fmt.Sprintf("line %d", i) || i > 0 && r.Seq != asc[i-1].Seq+1 {
			t.Fatalf("asc[%d] = %+v", i, r)
		}
	}
	desc, _ := collect(t, st, "svc1", ScanOptions{Desc: true})
	if len(desc) != 25 || desc[0].Text != "line 24" || desc[24].Text != "line 0" {
		t.Fatalf("desc order wrong: first %q last %q", desc[0].Text, desc[len(desc)-1].Text)
	}

	// Cursor paging, newest first, 7 at a time: no gaps, no repeats.
	var paged []string
	cursor := uint64(0)
	for {
		var page []Record
		res, err := st.Scan("svc1", ScanOptions{Desc: true, Cursor: cursor}, func(r Record) bool {
			page = append(page, r)
			return len(page) < 7
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page {
			paged = append(paged, r.Text)
		}
		if res.Done {
			break
		}
		cursor = res.Cursor
	}
	if len(paged) != 25 || paged[0] != "line 24" || paged[24] != "line 0" {
		t.Fatalf("paged = %v", paged)
	}

	// Time range: [base+5s, base+10s) is lines 5..9.
	rng, _ := collect(t, st, "svc1", ScanOptions{From: base.Add(5 * time.Second).UnixNano(), To: base.Add(10 * time.Second).UnixNano()})
	if len(rng) != 5 || rng[0].Text != "line 5" || rng[4].Text != "line 9" {
		t.Fatalf("range = %+v", rng)
	}
}

func TestReopenKeepsDataAndSequence(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.SegmentMaxBytes = 1 }) // a segment per block
	if err := st.Append("svc1", lines(40, ck.now(), "a")); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "svc1", "*.seg"))
	if len(segs) < 2 {
		t.Fatalf("expected several segments, got %d", len(segs))
	}

	st2 := openTest(t, dir, ck, nil)
	got, _ := collect(t, st2, "svc1", ScanOptions{})
	if len(got) != 40 {
		t.Fatalf("after reopen: %d lines", len(got))
	}
	last := got[39].Seq
	if err := st2.Append("svc1", lines(3, ck.now().Add(time.Hour), "b")); err != nil {
		t.Fatal(err)
	}
	if err := st2.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ = collect(t, st2, "svc1", ScanOptions{})
	if len(got) != 43 || got[40].Seq != last+1 {
		t.Fatalf("sequence did not continue: n=%d seq=%d want %d", len(got), got[40].Seq, last+1)
	}
}

func TestRecoversTornTailAndMissingIndex(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	st := openTest(t, dir, ck, nil)
	if err := st.Append("svc1", lines(30, ck.now(), "x")); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	seg, _ := filepath.Glob(filepath.Join(dir, "svc1", "*.seg"))
	idx, _ := filepath.Glob(filepath.Join(dir, "svc1", "*.idx"))
	if len(seg) != 1 || len(idx) != 1 {
		t.Fatalf("files: %v %v", seg, idx)
	}
	// A crash tore the last write: garbage after the last whole member, and the index is gone.
	f, err := os.OpenFile(seg[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad})
	_ = f.Close()
	if err := os.Remove(idx[0]); err != nil {
		t.Fatal(err)
	}

	st2 := openTest(t, dir, ck, nil)
	got, _ := collect(t, st2, "svc1", ScanOptions{})
	if len(got) != 30 {
		t.Fatalf("recovered %d lines, want 30", len(got))
	}
	if b, _ := st2.Bounds("svc1"); b.FirstSeq != got[0].Seq || b.LastSeq != got[29].Seq {
		t.Fatalf("bounds %+v", b)
	}
	// The data file lost the torn bytes and the index is back.
	if st, _ := os.Stat(idx[0]); st == nil {
		t.Fatal("index was not rebuilt")
	}
}

func TestPendingLinesAreVisibleBeforeTheyAreWritten(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.BlockMaxLines = 1000 })
	if err := st.Append("svc1", lines(3, ck.now(), "p")); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "svc1", "*")); len(files) != 0 {
		t.Fatalf("nothing should be on disk yet: %v", files)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{Desc: true})
	if len(got) != 3 || got[0].Text != "p 2" {
		t.Fatalf("pending not visible: %+v", got)
	}
	st.opts.FlushInterval = time.Millisecond
	ck.advance(time.Second)
	st.FlushDue()
	got, _ = collect(t, st, "svc1", ScanOptions{Desc: true})
	if len(got) != 3 {
		t.Fatalf("after flush: %d lines", len(got))
	}
}

func TestScanBudgetReturnsAResumableCursor(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, nil)
	if err := st.Append("svc1", lines(100, ck.now(), "budget")); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, desc := range []bool{true, false} {
		var got []string
		cursor := uint64(0)
		rounds := 0
		for {
			res, err := st.Scan("svc1", ScanOptions{Desc: desc, Cursor: cursor, MaxScanBytes: 1}, func(r Record) bool {
				got = append(got, r.Text)
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			rounds++
			if res.Done {
				break
			}
			if !res.Partial {
				t.Fatalf("a visitor that never stops cannot end a scan other than by budget: %+v", res)
			}
			cursor = res.Cursor
			if rounds > 50 {
				t.Fatal("scan does not make progress")
			}
		}
		if len(got) != 100 {
			t.Fatalf("desc=%v: %d lines in %d rounds", desc, len(got), rounds)
		}
		seen := map[string]bool{}
		for _, g := range got {
			if seen[g] {
				t.Fatalf("desc=%v: %q repeated", desc, g)
			}
			seen[g] = true
		}
	}
}

func TestGCByAgeAndSizeCaps(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.SegmentMaxBytes = 1; o.Retention = agentprotocol.LogRetention{Days: 7} })
	// Ten daily blocks (one segment each) of one service, oldest 9 days ago.
	for d := 9; d >= 0; d-- {
		at := ck.now().Add(-time.Duration(d) * 24 * time.Hour)
		if err := st.Append("svc1", lines(10, at, fmt.Sprintf("day%d", d))); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	for _, s := range st.svcs {
		s.mu.Lock()
		s.closeActiveLocked() // every segment sealed
		s.mu.Unlock()
	}
	st.mu.Unlock()

	if err := st.GC(); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	// Retention 7 days: day9 and day8 are older than the cutoff (each ends a few seconds into its day).
	if len(got) != 80 || got[0].Text != "day7 0" {
		t.Fatalf("age gc kept %d lines starting at %q", len(got), got[0].Text)
	}

	// A per-service cap below the data deletes the oldest first. The minimum cap
	// is one segment, so set the limits directly.
	_, total := st.Usage()
	st.rmu.Lock()
	st.retention.MaxServiceBytes = total / 2
	st.rmu.Unlock()
	if err := st.GC(); err != nil {
		t.Fatal(err)
	}
	after, _ := collect(t, st, "svc1", ScanOptions{})
	if len(after) >= 80 || len(after) == 0 || after[len(after)-1].Text != "day0 9" {
		t.Fatalf("service cap kept %d lines (newest %q)", len(after), after[len(after)-1].Text)
	}
	if _, now := st.Usage(); now > total/2 && len(after) > 10 {
		t.Fatalf("usage %d is above the cap %d with %d lines left", now, total/2, len(after))
	}

	// A server cap removes the oldest sealed segment of any service.
	if err := st.Append("svc2", lines(10, ck.now().Add(-6*24*time.Hour), "other")); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	for _, s := range st.svcs {
		s.mu.Lock()
		s.closeActiveLocked()
		s.mu.Unlock()
	}
	st.mu.Unlock()
	st.rmu.Lock()
	st.retention.MaxServiceBytes = 1 << 40
	st.retention.MaxServerBytes = 1
	st.rmu.Unlock()
	if err := st.GC(); err != nil {
		t.Fatal(err)
	}
	if usage, _ := st.Usage(); len(usage) != 0 {
		t.Fatalf("server cap of 1 byte should delete every sealed segment, left %+v", usage)
	}
	if _, err := os.Stat(filepath.Join(dir, "svc1")); err == nil {
		t.Fatal("empty service directory was kept")
	}
}

func TestActiveSegmentIsNeverCollected(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, func(o *Options) { o.Retention = agentprotocol.LogRetention{Days: 1} })
	if err := st.Append("svc1", lines(10, ck.now().Add(-72*time.Hour), "old")); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := st.GC(); err != nil {
		t.Fatal(err)
	}
	if got, _ := collect(t, st, "svc1", ScanOptions{}); len(got) != 10 {
		t.Fatalf("the segment still being written was collected: %d lines", len(got))
	}
}

func TestInvalidServiceIDIsRejected(t *testing.T) {
	ck := &clock{t: time.Now()}
	st := openTest(t, t.TempDir(), ck, nil)
	for _, id := range []string{"", "../x", "a/b", ".hidden"} {
		if err := st.Append(id, lines(1, ck.now(), "x")); err == nil {
			t.Errorf("Append(%q) accepted", id)
		}
	}
}

func TestConcurrentAppendAndScan(t *testing.T) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	st := openTest(t, t.TempDir(), ck, func(o *Options) { o.SegmentMaxBytes = 500 })
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = st.Append("svc1", lines(7, ck.now(), fmt.Sprintf("w%d", w)))
			}
		}()
	}
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = st.Scan("svc1", ScanOptions{Desc: true}, func(Record) bool { return true })
				st.FlushDue()
				_ = st.GC()
			}
		}
	}()
	wg.Wait()
	close(stop)
	rwg.Wait()
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	if len(got) != 4*50*7 {
		t.Fatalf("%d lines, want %d", len(got), 4*50*7)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("sequence gap at %d: %d -> %d", i, got[i-1].Seq, got[i].Seq)
		}
	}
}

func TestLongAndOddLines(t *testing.T) {
	ck := &clock{t: time.Now()}
	st := openTest(t, t.TempDir(), ck, nil)
	long := []byte(strings.Repeat("é", MaxLineBytes)) // 2 bytes per rune
	recs := []Record{
		{TS: ck.now().UnixNano(), Text: string(long)},
		{TS: ck.now().UnixNano(), Text: "tab\tinside\rand cr"},
		{TS: ck.now().UnixNano(), Text: ""},
	}
	if err := st.Append("svc1", recs); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	if len(got) != 3 || len(got[0].Text) > MaxLineBytes || got[1].Text != "tab\tinside and cr" || got[2].Text != "" {
		t.Fatalf("got %+v", got)
	}
}
