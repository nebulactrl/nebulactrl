package logstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func segFiles(t *testing.T, dir, svc string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, svc, "*.seg"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRunWritesIdleLinesAndFlushesOnShutdown(t *testing.T) {
	ck := &clock{t: t0}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.FlushInterval = 20 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { st.Run(ctx); close(done) }()

	if err := st.Append("svc1", lines(3, t0, "idle")); err != nil { // below BlockMaxLines: stays in memory
		t.Fatal(err)
	}
	if len(segFiles(t, dir, "svc1")) != 0 {
		t.Fatal("lines below the block size are held in memory")
	}
	ck.advance(time.Second) // they have now waited longer than FlushInterval
	eventually(t, "idle lines written by the background flush", func() bool { return len(segFiles(t, dir, "svc1")) == 1 })

	// Lines that arrive just before shutdown are written by Run's final close.
	if err := st.Append("svc1", lines(2, t0.Add(time.Minute), "late")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run ignored its context")
	}
	st2 := openTest(t, dir, ck, nil)
	got, _ := collect(t, st2, "svc1", ScanOptions{})
	if len(got) != 5 {
		t.Fatalf("after restart %d lines, want 5", len(got))
	}
}

func TestSetRetentionAppliesNewLimitsAtOnce(t *testing.T) {
	ck := &clock{t: t0}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.SegmentMaxAge = time.Hour })
	if err := st.Append("svc1", lines(5, t0, "old")); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	ck.advance(2 * time.Hour) // the first segment is now too old to be appended to
	if err := st.Append("svc1", lines(5, ck.now(), "new")); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := len(segFiles(t, dir, "svc1")); n != 2 {
		t.Fatalf("%d segments, want 2", n)
	}
	ck.advance(72 * time.Hour)

	st.SetRetention(agentprotocol.LogRetention{Days: 1})
	if r := st.Retention(); r.Days != 1 || r.MaxServiceBytes == 0 || r.MaxServerBytes == 0 {
		t.Fatalf("zero fields take their defaults: %+v", r)
	}
	// The old segment is sealed and older than a day: gone. The one still open for writing stays.
	files := segFiles(t, dir, "svc1")
	if len(files) != 1 {
		t.Fatalf("%d segments after the new retention, want 1: %v", len(files), files)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	if len(got) != 5 || !strings.HasPrefix(got[0].Text, "new") {
		t.Fatalf("remaining lines %v", got)
	}
}

func TestChangedFiresAtTheNextAppendOnly(t *testing.T) {
	ck := &clock{t: t0}
	st := openTest(t, t.TempDir(), ck, nil)
	ch := st.Changed()
	select {
	case <-ch:
		t.Fatal("Changed fired without an append")
	default:
	}
	if err := st.Append("svc1", nil); err != nil { // nothing appended
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("an empty append is not a change")
	default:
	}
	if err := st.Append("svc1", lines(1, t0, "a")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("Changed did not fire after an append")
	}
	if next := st.Changed(); next == ch {
		t.Fatal("a fresh channel is handed out after each change")
	}
}

func TestBacklogBytesCountsLinesAfterAPosition(t *testing.T) {
	ck := &clock{t: t0}
	st := openTest(t, t.TempDir(), ck, nil) // blocks of 10 lines
	if err := st.Append("svc1", lines(25, t0, "backlog")); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil { // 3 blocks: 10, 10, 5
		t.Fatal(err)
	}
	b, err := st.Bounds("svc1")
	if err != nil {
		t.Fatal(err)
	}
	all := st.BacklogBytes("svc1", 0)
	if all <= 0 {
		t.Fatal("nothing counted for a position before the first line")
	}
	if got := st.BacklogBytes("svc1", b.LastSeq); got != 0 {
		t.Fatalf("backlog after the last line is %d, want 0", got)
	}
	// Positions further in leave less behind; the middle of a block counts its share.
	atBlock := st.BacklogBytes("svc1", b.FirstSeq+9) // end of the first block
	mid := st.BacklogBytes("svc1", b.FirstSeq+4)     // inside the first block
	if !(all > mid && mid > atBlock && atBlock > 0) {
		t.Fatalf("backlog not decreasing: all=%d mid=%d atBlockEnd=%d", all, mid, atBlock)
	}

	// Lines still in memory count too.
	if err := st.Append("svc1", lines(2, t0.Add(time.Hour), "pending")); err != nil {
		t.Fatal(err)
	}
	if got := st.BacklogBytes("svc1", b.LastSeq); got <= 0 {
		t.Fatalf("pending lines are not in the backlog: %d", got)
	}

	if st.BacklogBytes("ghost", 0) != 0 || st.BacklogBytes("bad id!", 0) != 0 {
		t.Fatal("unknown or invalid services have no backlog")
	}
}

func TestServiceIDsListsServicesWithData(t *testing.T) {
	ck := &clock{t: t0}
	st := openTest(t, t.TempDir(), ck, nil)
	if got := st.ServiceIDs(); len(got) != 0 {
		t.Fatalf("empty store lists %v", got)
	}
	for _, id := range []string{"svc-b", "svc-a", "svc-c"} {
		if err := st.Append(id, lines(1, t0, id)); err != nil {
			t.Fatal(err)
		}
	}
	got := st.ServiceIDs()
	if strings.Join(got, ",") != "svc-a,svc-b,svc-c" {
		t.Fatalf("ids %v, want them sorted", got)
	}
}

func TestSanitizeLineKeepsOnlyValidTextWithoutNULs(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain":           {"hello world", "hello world"},
		"unicode":         {"héllo 世界", "héllo 世界"},
		"nul bytes":       {"a\x00b\x00", "ab"},
		"invalid utf-8":   {"ok\xffbad\xc3", "ok\uFFFDbad\uFFFD"},
		"nul and invalid": {"\x00\xfe", "\uFFFD"},
		"empty":           {"", ""},
	} {
		if got := SanitizeLine(tc.in); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestTruncateUTF8CutsOnRuneBoundaries(t *testing.T) {
	s := "ab世界" // a b + two 3-byte runes = 8 bytes
	for n, want := range map[int]string{0: "", 1: "a", 2: "ab", 3: "ab", 4: "ab", 5: "ab世", 6: "ab世", 7: "ab世", 8: s, 100: s} {
		got := truncateUTF8(s, n)
		if got != want {
			t.Errorf("truncateUTF8(%d) = %q, want %q", n, got, want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateUTF8(%d) split a rune", n)
		}
	}
}

func TestOversizedLinesAreStoredTruncatedOnARuneBoundary(t *testing.T) {
	ck := &clock{t: t0}
	st := openTest(t, t.TempDir(), ck, nil)
	long := strings.Repeat("世", MaxLineBytes) // 3 bytes per rune: the cut falls mid-rune
	if err := st.Append("svc1", []Record{{TS: t0.UnixNano(), Text: long}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	if len(got) != 1 || len(got[0].Text) > MaxLineBytes || !utf8.ValidString(got[0].Text) || !strings.HasPrefix(long, got[0].Text) {
		t.Fatalf("stored %d bytes valid=%v", len(got[0].Text), utf8.ValidString(got[0].Text))
	}
}

func TestDecodeRecordRejectsMalformedLines(t *testing.T) {
	good := string(encodeRecord(nil, Record{Seq: 7, TS: 99, Replica: 2, DeploymentID: "dep", Text: "a\tb"}))
	r, err := decodeRecord([]byte(strings.TrimSuffix(good, "\n")))
	if err != nil || r.Seq != 7 || r.TS != 99 || r.Replica != 2 || r.DeploymentID != "dep" || r.Text != "a\tb" {
		t.Fatalf("round trip: %+v %v", r, err)
	}
	for name, line := range map[string]string{
		"too few fields": "1\t2\t3",
		"bad seq":        "x\t2\t3\tdep\ttext",
		"bad ts":         "1\ty\t3\tdep\ttext",
		"bad replica":    "1\t2\tz\tdep\ttext",
		"empty":          "",
	} {
		if _, err := decodeRecord([]byte(line)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := decodeBlock([]byte("1\t2\t3\tdep\tno newline")); err == nil {
		t.Error("a block that does not end in a newline was accepted")
	}
	recs, err := decodeBlock([]byte("1\t2\t3\td\tok\nbroken\n"))
	if err == nil || len(recs) != 1 {
		t.Errorf("a bad line keeps the lines before it and reports the error: %v %v", recs, err)
	}
}

func TestParseIndexStopsAtTheFirstBadEntry(t *testing.T) {
	b1 := block{Offset: 8, CLen: 10, ULen: 20, Lines: 3, FirstSeq: 1, MinTS: 1, MaxTS: 2}
	b2 := block{Offset: b1.end(), CLen: 10, ULen: 20, Lines: 3, FirstSeq: 4, MinTS: 3, MaxTS: 4}
	build := func(bs ...block) []byte {
		out := []byte(idxMagic)
		for _, b := range bs {
			out = b.marshal(out)
		}
		return out
	}
	got, err := parseIndex(build(b1, b2))
	if err != nil || len(got) != 2 || got[1] != b2 {
		t.Fatalf("clean index: %v %v", got, err)
	}
	// A torn trailing entry is ignored.
	torn := append(build(b1, b2), 1, 2, 3)
	if got, _ := parseIndex(torn); len(got) != 2 {
		t.Fatalf("torn tail: %d entries", len(got))
	}
	// A gap in the offsets, a sequence that goes back, or an empty block ends the index.
	gap := b2
	gap.Offset++
	if got, _ := parseIndex(build(b1, gap)); len(got) != 1 {
		t.Fatalf("offset gap kept %d entries", len(got))
	}
	back := b2
	back.FirstSeq = 2
	if got, _ := parseIndex(build(b1, back)); len(got) != 1 {
		t.Fatalf("sequence going back kept %d entries", len(got))
	}
	empty := b2
	empty.Lines = 0
	if got, _ := parseIndex(build(b1, empty)); len(got) != 1 {
		t.Fatalf("an empty block kept %d entries", len(got))
	}
	for name, data := range map[string][]byte{"short": []byte("NLI"), "wrong magic": []byte("NLIDX999" + strings.Repeat("x", 48)), "nil": nil} {
		if _, err := parseIndex(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOpenIgnoresForeignEntriesAndDropsUnreadableSegments(t *testing.T) {
	ck := &clock{t: t0}
	dir := t.TempDir()
	st := openTest(t, dir, ck, nil)
	if err := st.Append("good", lines(4, t0, "keep")); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Things that are not services, and a service whose only segment is garbage.
	if err := os.WriteFile(filepath.Join(dir, "stray-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "not a service!"), 0o700); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(dir, "junk")
	if err := os.Mkdir(junk, 0o700); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(junk, segName(5)+".seg")
	if err := os.WriteFile(seg, []byte("this is not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "README"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	st2 := openTest(t, dir, ck, nil)
	if ids := st2.ServiceIDs(); strings.Join(ids, ",") != "good" {
		t.Fatalf("services after reopen: %v", ids)
	}
	if _, err := os.Stat(seg); !os.IsNotExist(err) {
		t.Fatal("a segment without a single readable block is removed")
	}
	got, _ := collect(t, st2, "good", ScanOptions{})
	if len(got) != 4 {
		t.Fatalf("good service has %d lines, want 4", len(got))
	}
	// New lines continue the sequence of what was stored.
	if err := st2.Append("good", lines(1, t0.Add(time.Hour), "more")); err != nil {
		t.Fatal(err)
	}
	all, _ := collect(t, st2, "good", ScanOptions{})
	if len(all) != 5 || all[4].Seq != got[3].Seq+1 {
		t.Fatalf("sequence after reopen: %v", all)
	}
}

func TestFailedWritesKeepLinesPendingUntilTheDiskRecovers(t *testing.T) {
	ck := &clock{t: t0}
	dir := t.TempDir()
	st := openTest(t, dir, ck, nil)
	// A regular file where the service directory should be.
	blocker := filepath.Join(dir, "svc1")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Append("svc1", lines(10, t0, "stuck")); err == nil {
		t.Fatal("a full block that cannot be written must report the error")
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	if len(got) != 10 {
		t.Fatalf("pending lines visible to readers: %d, want 10", len(got))
	}
	if err := st.Flush(); err == nil {
		t.Fatal("Flush hid the write error")
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatalf("flush after the disk recovered: %v", err)
	}
	if len(segFiles(t, dir, "svc1")) != 1 {
		t.Fatal("the stuck lines were not written")
	}
	got, _ = collect(t, st, "svc1", ScanOptions{})
	if len(got) != 10 || got[0].Text != "stuck 0" || got[9].Text != "stuck 9" {
		t.Fatalf("lines after recovery: %d %v", len(got), got)
	}
}

func TestFlushDueLogsAndKeepsLinesWhenTheWriteFails(t *testing.T) {
	ck := &clock{t: t0}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.FlushInterval = time.Minute })
	if err := os.WriteFile(filepath.Join(dir, "svc1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Append("svc1", lines(2, t0, "x")); err != nil {
		t.Fatal(err)
	}
	ck.advance(time.Hour)
	st.FlushDue() // the write fails; the lines must stay readable
	if got, _ := collect(t, st, "svc1", ScanOptions{}); len(got) != 2 {
		t.Fatalf("lines lost after a failed background flush: %d", len(got))
	}
}

func TestSegmentsRollWhenTheyReachTheirSizeLimit(t *testing.T) {
	ck := &clock{t: t0}
	dir := t.TempDir()
	st := openTest(t, dir, ck, func(o *Options) { o.SegmentMaxBytes = 1 })
	for i := range 3 {
		if err := st.Append("svc1", lines(10, t0.Add(time.Duration(i)*time.Minute), "roll")); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(segFiles(t, dir, "svc1")); n != 3 {
		t.Fatalf("%d segments, want one per block when the limit is tiny", n)
	}
	got, _ := collect(t, st, "svc1", ScanOptions{})
	if len(got) != 30 {
		t.Fatalf("%d lines across segments, want 30", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("sequence gap at %d", i)
		}
	}
}

func TestQueryErrorsReadAsText(t *testing.T) {
	if got := errBadCursor.Error(); !strings.Contains(got, "invalid cursor") {
		t.Fatalf("error text %q", got)
	}
	ck := &clock{t: t0}
	st := openTest(t, t.TempDir(), ck, nil)
	_, err := st.Query(agentprotocol.LogsQuery{ServiceID: "svc1", Cursor: "nope"})
	if err == nil || !strings.Contains(err.Error(), "invalid cursor") {
		t.Fatalf("bad cursor error: %v", err)
	}
}
