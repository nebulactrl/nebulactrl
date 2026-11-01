package logstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Defaults of Options.
const (
	DefaultBlockMaxLines   = 2000
	DefaultBlockMaxBytes   = 256 << 10
	DefaultFlushInterval   = time.Second
	DefaultSegmentMaxBytes = int64(8) << 20
	DefaultSegmentMaxAge   = 6 * time.Hour
	// maxPendingLines bounds the lines held in memory while the disk refuses writes.
	maxPendingLines = 50_000
	// gcInterval is how often limits are enforced.
	gcInterval = time.Minute
)

// Options configures a Store.
type Options struct {
	// Dir is the root, <state-dir>/logs.
	Dir    string
	Logger *slog.Logger
	// BlockMaxLines and BlockMaxBytes end a block early (before the flush interval).
	BlockMaxLines int
	BlockMaxBytes int
	// FlushInterval is the longest a line waits in memory before it is written.
	FlushInterval time.Duration
	// SegmentMaxBytes (compressed) and SegmentMaxAge end a segment.
	SegmentMaxBytes int64
	SegmentMaxAge   time.Duration
	Retention       agentprotocol.LogRetention
	Now             func() time.Time
}

func (o *Options) defaults() {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.BlockMaxLines <= 0 {
		o.BlockMaxLines = DefaultBlockMaxLines
	}
	if o.BlockMaxBytes <= 0 {
		o.BlockMaxBytes = DefaultBlockMaxBytes
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.SegmentMaxBytes <= 0 {
		o.SegmentMaxBytes = DefaultSegmentMaxBytes
	}
	if o.SegmentMaxAge <= 0 {
		o.SegmentMaxAge = DefaultSegmentMaxAge
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	o.Retention = o.Retention.WithDefaults()
}

// Store is the log history of one agent.
type Store struct {
	opts Options
	log  *slog.Logger

	mu   sync.RWMutex
	svcs map[string]*svc

	rmu       sync.RWMutex
	retention agentprotocol.LogRetention

	cmu     sync.Mutex
	changed chan struct{}
}

// svc is the state of one service's directory.
type svc struct {
	id  string
	dir string

	mu      sync.RWMutex
	segs    []*segment // oldest → newest; the newest is active unless sealed
	nextSeq uint64

	pending      []Record
	pendingBytes int
	lastFlush    time.Time

	active   *os.File
	activeIx *os.File
	openedAt time.Time
	dead     bool // removed from the store by GC; appenders look the service up again
}

var serviceIDRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$`)

// Open loads (and repairs) the store under opts.Dir.
func Open(opts Options) (*Store, error) {
	opts.defaults()
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	st := &Store{opts: opts, log: opts.Logger, svcs: map[string]*svc{}, retention: opts.Retention, changed: make(chan struct{})}
	entries, err := os.ReadDir(opts.Dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || !serviceIDRe.MatchString(e.Name()) {
			continue
		}
		s, err := st.loadService(e.Name())
		if err != nil {
			st.log.Warn("log store: service directory skipped", "service_id", e.Name(), "err", err)
			continue
		}
		if len(s.segs) > 0 {
			st.svcs[e.Name()] = s
		}
	}
	return st, nil
}

func (st *Store) loadService(id string) (*svc, error) {
	dir := filepath.Join(st.opts.Dir, id)
	s := &svc{id: id, dir: dir, lastFlush: st.opts.Now()}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var starts []int64
	for _, f := range files {
		if n, ok := parseSegName(f.Name()); ok {
			starts = append(starts, n)
		}
	}
	slices.Sort(starts)
	for _, n := range starts {
		seg, err := loadSegment(dir, n, st.log)
		if errors.Is(err, errEmptySegment) {
			_ = os.Remove(filepath.Join(dir, segName(n)+".seg"))
			_ = os.Remove(filepath.Join(dir, segName(n)+".idx"))
			continue
		}
		if err != nil {
			st.log.Warn("log store: segment skipped", "service_id", id, "segment", segName(n), "err", err)
			continue
		}
		s.segs = append(s.segs, seg)
	}
	s.nextSeq = st.seedSeq(s)
	return s, nil
}

// seedSeq returns the first sequence number to hand out. Without data it is
// the clock in microseconds, so it is above any sequence a wiped store handed
// out before (drain positions refer to them).
func (st *Store) seedSeq(s *svc) uint64 {
	if n := len(s.segs); n > 0 {
		return s.segs[n-1].lastSeq() + 1
	}
	return uint64(st.opts.Now().UnixMicro())
}

// Close flushes and closes every file.
func (st *Store) Close() error {
	st.mu.RLock()
	all := make([]*svc, 0, len(st.svcs))
	for _, s := range st.svcs {
		all = append(all, s)
	}
	st.mu.RUnlock()
	var first error
	for _, s := range all {
		s.mu.Lock()
		if err := s.flushLocked(st); err != nil && first == nil {
			first = err
		}
		s.closeActiveLocked()
		s.mu.Unlock()
	}
	return first
}

// Run flushes idle lines and enforces the limits until ctx ends.
func (st *Store) Run(ctx context.Context) {
	flush := time.NewTicker(max(st.opts.FlushInterval/2, 10*time.Millisecond))
	gc := time.NewTicker(gcInterval)
	defer flush.Stop()
	defer gc.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := st.Close(); err != nil {
				st.log.Warn("log store: close", "err", err)
			}
			return
		case <-flush.C:
			st.FlushDue()
		case <-gc.C:
			if err := st.GC(); err != nil {
				st.log.Warn("log store: gc", "err", err)
			}
		}
	}
}

// SetRetention replaces the limits (zero fields take the defaults) and
// enforces them.
func (st *Store) SetRetention(r agentprotocol.LogRetention) {
	r = r.WithDefaults()
	st.rmu.Lock()
	changed := r != st.retention
	st.retention = r
	st.rmu.Unlock()
	if changed {
		if err := st.GC(); err != nil {
			st.log.Warn("log store: gc", "err", err)
		}
	}
}

// Retention returns the effective limits.
func (st *Store) Retention() agentprotocol.LogRetention {
	st.rmu.RLock()
	defer st.rmu.RUnlock()
	return st.retention
}

// Changed returns a channel that is closed at the next Append. Wait on it, then
// call Changed again.
func (st *Store) Changed() <-chan struct{} {
	st.cmu.Lock()
	defer st.cmu.Unlock()
	return st.changed
}

func (st *Store) notify() {
	st.cmu.Lock()
	close(st.changed)
	st.changed = make(chan struct{})
	st.cmu.Unlock()
}

func (st *Store) service(id string, create bool) (*svc, error) {
	if !serviceIDRe.MatchString(id) {
		return nil, fmt.Errorf("logstore: invalid service id %q", id)
	}
	st.mu.RLock()
	s := st.svcs[id]
	st.mu.RUnlock()
	if s != nil || !create {
		return s, nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if cur := st.svcs[id]; cur != nil {
		return cur, nil
	}
	s = &svc{id: id, dir: filepath.Join(st.opts.Dir, id), lastFlush: st.opts.Now()}
	s.nextSeq = st.seedSeq(s)
	st.svcs[id] = s
	return s, nil
}

// Append stores recs (their Seq is assigned here) for a service. It never
// blocks on the disk longer than one block write.
func (st *Store) Append(serviceID string, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	var s *svc
	for {
		var err error
		if s, err = st.service(serviceID, true); err != nil {
			return err
		}
		s.mu.Lock()
		if !s.dead {
			break
		}
		s.mu.Unlock()
	}
	for _, r := range recs {
		r.Seq = s.nextSeq
		s.nextSeq++
		s.pending = append(s.pending, r)
		s.pendingBytes += len(r.Text) + 40
	}
	var ferr error
	if len(s.pending) >= st.opts.BlockMaxLines || s.pendingBytes >= st.opts.BlockMaxBytes {
		ferr = s.flushLocked(st)
	}
	if over := len(s.pending) - maxPendingLines; over > 0 { // the disk keeps failing: drop the oldest
		s.pending = slices.Clone(s.pending[over:])
	}
	s.mu.Unlock()
	st.notify()
	return ferr
}

// FlushDue writes the lines that have waited for FlushInterval.
func (st *Store) FlushDue() {
	now := st.opts.Now()
	st.mu.RLock()
	all := make([]*svc, 0, len(st.svcs))
	for _, s := range st.svcs {
		all = append(all, s)
	}
	st.mu.RUnlock()
	for _, s := range all {
		s.mu.Lock()
		if len(s.pending) > 0 && now.Sub(s.lastFlush) >= st.opts.FlushInterval {
			if err := s.flushLocked(st); err != nil {
				st.log.Warn("log store: write failed", "service_id", s.id, "err", err)
			}
		}
		s.mu.Unlock()
	}
}

// Flush writes every pending line now.
func (st *Store) Flush() error {
	st.mu.RLock()
	all := make([]*svc, 0, len(st.svcs))
	for _, s := range st.svcs {
		all = append(all, s)
	}
	st.mu.RUnlock()
	var first error
	for _, s := range all {
		s.mu.Lock()
		if err := s.flushLocked(st); err != nil && first == nil {
			first = err
		}
		s.mu.Unlock()
	}
	return first
}

func (s *svc) closeActiveLocked() {
	if s.active != nil {
		_ = s.active.Close()
		s.active = nil
	}
	if s.activeIx != nil {
		_ = s.activeIx.Close()
		s.activeIx = nil
	}
	if n := len(s.segs); n > 0 {
		s.segs[n-1].sealed = true
	}
}

// ensureActiveLocked opens (or rolls to) the segment the next block goes to.
func (s *svc) ensureActiveLocked(st *Store, firstTS int64) (*segment, error) {
	now := st.opts.Now()
	if n := len(s.segs); n > 0 && s.active != nil {
		seg := s.segs[n-1]
		if seg.size < st.opts.SegmentMaxBytes && now.Sub(s.openedAt) < st.opts.SegmentMaxAge {
			return seg, nil
		}
		s.closeActiveLocked()
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	start := firstTS
	if n := len(s.segs); n > 0 && start <= s.segs[n-1].startNS {
		start = s.segs[n-1].startNS + 1
	}
	if start <= 0 {
		start = now.UnixNano()
	}
	name := segName(start)
	seg := &segment{startNS: start, path: filepath.Join(s.dir, name+".seg"), idxPath: filepath.Join(s.dir, name+".idx")}
	f, err := os.OpenFile(seg.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	ix, err := os.OpenFile(seg.idxPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(seg.path)
		return nil, err
	}
	if _, err := ix.WriteString(idxMagic); err != nil {
		_ = f.Close()
		_ = ix.Close()
		_ = os.Remove(seg.path)
		_ = os.Remove(seg.idxPath)
		return nil, err
	}
	seg.idxSize = int64(len(idxMagic))
	s.active, s.activeIx, s.openedAt = f, ix, now
	s.segs = append(s.segs, seg)
	return seg, nil
}

// flushLocked writes the pending lines, a block per BlockMaxLines or
// BlockMaxBytes. On error the lines not written yet stay pending.
func (s *svc) flushLocked(st *Store) error {
	s.lastFlush = st.opts.Now()
	for len(s.pending) > 0 {
		n, size := 0, 0
		for n < len(s.pending) && n < st.opts.BlockMaxLines && (n == 0 || size < st.opts.BlockMaxBytes) {
			size += len(s.pending[n].Text) + 40
			n++
		}
		if err := s.writeBlockLocked(st, s.pending[:n]); err != nil {
			return err
		}
		s.pending = s.pending[n:]
		s.pendingBytes = max(s.pendingBytes-size, 0)
	}
	s.pending, s.pendingBytes = nil, 0
	return nil
}

// writeBlockLocked writes recs as one gzip member and indexes it.
func (s *svc) writeBlockLocked(st *Store, recs []Record) error {
	raw := make([]byte, 0, len(recs)*64)
	for _, r := range recs {
		raw = encodeRecord(raw, r)
	}
	comp, err := compress(raw)
	if err != nil {
		return err
	}
	seg, err := s.ensureActiveLocked(st, recs[0].TS)
	if err != nil {
		return err
	}
	b := summarize(seg.size, len(comp), len(raw), recs)
	if n, err := s.active.Write(comp); err != nil || n != len(comp) {
		// A partial member is cut off by the next load; start a fresh segment.
		s.closeActiveLocked()
		if err == nil {
			err = fmt.Errorf("short write (%d of %d bytes)", n, len(comp))
		}
		return err
	}
	if _, err := s.activeIx.Write(b.marshal(nil)); err != nil {
		s.closeActiveLocked()
		return err
	}
	seg.size += int64(len(comp))
	seg.idxSize += idxEntrySize
	seg.note(b)
	return nil
}

// ---- limits ------------------------------------------------------------------

// GC deletes sealed segments older than the retention and, oldest first, until
// every service and the whole store fit their size caps. The newest segment of
// a service is never deleted while it is open for writing.
func (st *Store) GC() error {
	ret := st.Retention()
	cutoff := st.opts.Now().Add(-time.Duration(ret.Days) * 24 * time.Hour).UnixNano()

	st.mu.RLock()
	all := make([]*svc, 0, len(st.svcs))
	for _, s := range st.svcs {
		all = append(all, s)
	}
	st.mu.RUnlock()

	type cand struct {
		s   *svc
		seg *segment
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	var total int64
	var cands []cand
	for _, s := range all {
		s.mu.Lock()
		// Age.
		for len(s.segs) > 0 && s.segs[0].sealed && s.segs[0].maxTS < cutoff {
			note(s.segs[0].remove())
			s.segs = s.segs[1:]
		}
		// A segment that is not the first may be old too (out-of-order backfill): keep order, drop it.
		keep := s.segs[:0]
		for i, seg := range s.segs {
			if seg.sealed && seg.maxTS < cutoff && i < len(s.segs)-1 {
				note(seg.remove())
				continue
			}
			keep = append(keep, seg)
		}
		s.segs = keep
		// Service cap.
		var sum int64
		for _, seg := range s.segs {
			sum += seg.bytes()
		}
		for sum > ret.MaxServiceBytes && len(s.segs) > 0 && s.segs[0].sealed {
			sum -= s.segs[0].bytes()
			note(s.segs[0].remove())
			s.segs = s.segs[1:]
		}
		total += sum
		for _, seg := range s.segs {
			if seg.sealed {
				cands = append(cands, cand{s, seg})
			}
		}
		s.mu.Unlock()
	}
	// Server cap: the oldest sealed segment anywhere goes first.
	if total > ret.MaxServerBytes {
		sort.Slice(cands, func(i, j int) bool { return cands[i].seg.maxTS < cands[j].seg.maxTS })
		for _, c := range cands {
			if total <= ret.MaxServerBytes {
				break
			}
			c.s.mu.Lock()
			if i := slices.Index(c.s.segs, c.seg); i >= 0 {
				total -= c.seg.bytes()
				note(c.seg.remove())
				c.s.segs = slices.Delete(c.s.segs, i, i+1)
			}
			c.s.mu.Unlock()
		}
	}
	// Forget empty services (their directory goes with the last segment).
	st.mu.Lock()
	for id, s := range st.svcs {
		s.mu.Lock()
		empty := len(s.segs) == 0 && len(s.pending) == 0
		if empty {
			s.dead = true
		}
		s.mu.Unlock()
		if empty {
			delete(st.svcs, id)
			_ = os.Remove(filepath.Join(st.opts.Dir, id))
		}
	}
	st.mu.Unlock()
	return firstErr
}

// ---- inspection ----------------------------------------------------------------

// Usage describes the stored volume of one service.
type Usage struct {
	ServiceID string
	Bytes     int64
	Lines     int64
	Oldest    time.Time // zero when the service has no stored line
	Newest    time.Time
}

// Usage lists every service with stored lines, and the total bytes on disk.
func (st *Store) Usage() ([]Usage, int64) {
	st.mu.RLock()
	all := make([]*svc, 0, len(st.svcs))
	for _, s := range st.svcs {
		all = append(all, s)
	}
	st.mu.RUnlock()
	var out []Usage
	var total int64
	for _, s := range all {
		s.mu.RLock()
		u := Usage{ServiceID: s.id}
		var minTS, maxTS int64
		for i, seg := range s.segs {
			u.Bytes += seg.bytes()
			u.Lines += seg.lines
			if i == 0 || seg.minTS < minTS {
				minTS = seg.minTS
			}
			if i == 0 || seg.maxTS > maxTS {
				maxTS = seg.maxTS
			}
		}
		for _, r := range s.pending {
			u.Lines++
			if minTS == 0 || r.TS < minTS {
				minTS = r.TS
			}
			maxTS = max(maxTS, r.TS)
		}
		s.mu.RUnlock()
		if u.Lines == 0 {
			continue
		}
		u.Oldest, u.Newest = time.Unix(0, minTS), time.Unix(0, maxTS)
		total += u.Bytes
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out, total
}

// Bounds are what the store retains for one service.
type Bounds struct {
	FirstSeq, LastSeq uint64 // 0 when empty
	Oldest, Newest    time.Time
}

// Bounds returns the retained range of a service.
func (st *Store) Bounds(serviceID string) (Bounds, error) {
	s, err := st.service(serviceID, false)
	if err != nil || s == nil {
		return Bounds{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var b Bounds
	var minTS, maxTS int64
	for _, seg := range s.segs {
		if b.FirstSeq == 0 {
			b.FirstSeq, minTS, maxTS = seg.firstSeq(), seg.minTS, seg.maxTS
		}
		minTS, maxTS = min(minTS, seg.minTS), max(maxTS, seg.maxTS)
		b.LastSeq = seg.lastSeq()
	}
	for _, r := range s.pending {
		if b.FirstSeq == 0 {
			b.FirstSeq, minTS, maxTS = r.Seq, r.TS, r.TS
		}
		minTS, maxTS = min(minTS, r.TS), max(maxTS, r.TS)
		b.LastSeq = r.Seq
	}
	if b.FirstSeq != 0 {
		b.Oldest, b.Newest = time.Unix(0, minTS), time.Unix(0, maxTS)
	}
	return b, nil
}

// BacklogBytes approximates the uncompressed bytes of the lines after seq.
func (st *Store) BacklogBytes(serviceID string, after uint64) int64 {
	s, err := st.service(serviceID, false)
	if err != nil || s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int64
	for i := len(s.segs) - 1; i >= 0; i-- {
		seg := s.segs[i]
		if seg.lastSeq() <= after {
			break
		}
		for j := len(seg.blocks) - 1; j >= 0; j-- {
			b := seg.blocks[j]
			if b.lastSeq() <= after {
				break
			}
			if after >= b.FirstSeq { // the position is inside this block: count its share after it
				n += int64(b.ULen) * int64(b.lastSeq()-after) / int64(b.Lines)
				continue
			}
			n += int64(b.ULen)
		}
	}
	for _, r := range s.pending {
		if r.Seq > after {
			n += int64(len(r.Text) + 40)
		}
	}
	return n
}

// ServiceIDs lists the services that have stored lines.
func (st *Store) ServiceIDs() []string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]string, 0, len(st.svcs))
	for id := range st.svcs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---- scanning ------------------------------------------------------------------

// ScanOptions selects lines for Scan.
type ScanOptions struct {
	// From (inclusive) and To (exclusive) bound the line timestamps in unix
	// nanoseconds; 0 leaves the side open.
	From, To int64
	// Cursor, when non-zero, continues after a previous scan: with Desc the
	// lines with a lower sequence number, otherwise those with a higher one.
	Cursor uint64
	Desc   bool
	// MaxScanBytes and Deadline bound the work of one scan (uncompressed bytes
	// decompressed; zero = unbounded).
	MaxScanBytes int64
	Deadline     time.Time
}

// ScanResult is how a scan ended.
type ScanResult struct {
	// Done: every line in range was visited. Otherwise Cursor continues the scan.
	Done   bool
	Cursor uint64
	// Partial: the budget (bytes or time) ended the scan, not the visitor.
	Partial bool
}

// Scan calls visit for the lines of a service within the options, newest or
// oldest first by sequence number, until visit returns false (the page is
// full), the lines run out or the budget ends. Lines still in memory are
// included.
func (st *Store) Scan(serviceID string, o ScanOptions, visit func(Record) bool) (ScanResult, error) {
	s, err := st.service(serviceID, false)
	if err != nil {
		return ScanResult{}, err
	}
	if s == nil {
		return ScanResult{Done: true}, nil
	}
	type view struct {
		seg    *segment
		blocks []block
	}
	s.mu.RLock()
	views := make([]view, len(s.segs))
	for i, seg := range s.segs {
		views[i] = view{seg, seg.blocks[:len(seg.blocks):len(seg.blocks)]}
	}
	pending := slices.Clone(s.pending)
	s.mu.RUnlock()

	inCursor := func(seq uint64) bool {
		if o.Cursor == 0 {
			return true
		}
		if o.Desc {
			return seq < o.Cursor
		}
		return seq > o.Cursor
	}
	inTime := func(ts int64) bool { return (o.From == 0 || ts >= o.From) && (o.To == 0 || ts < o.To) }
	var scanned int64
	var last uint64 // last visited line
	emit := func(r Record) (stop bool) {
		if !inCursor(r.Seq) || !inTime(r.TS) {
			return false
		}
		last = r.Seq
		return !visit(r)
	}
	stopped := func() ScanResult { return ScanResult{Cursor: last} }

	if o.Desc {
		for i := len(pending) - 1; i >= 0; i-- {
			if emit(pending[i]) {
				return stopped(), nil
			}
		}
	}
	segOrder := make([]int, len(views))
	for i := range segOrder {
		segOrder[i] = i
	}
	if o.Desc {
		slices.Reverse(segOrder)
	}
	for _, si := range segOrder {
		v := views[si]
		if len(v.blocks) == 0 || (o.Cursor != 0 && ((o.Desc && v.blocks[0].FirstSeq >= o.Cursor) || (!o.Desc && v.blocks[len(v.blocks)-1].lastSeq() <= o.Cursor))) {
			continue
		}
		if (o.From != 0 && v.seg.maxTS < o.From) || (o.To != 0 && v.seg.minTS >= o.To) {
			continue
		}
		var f *os.File
		order := make([]int, len(v.blocks))
		for i := range order {
			order[i] = i
		}
		if o.Desc {
			slices.Reverse(order)
		}
		for _, bi := range order {
			b := v.blocks[bi]
			if o.Cursor != 0 && ((o.Desc && b.FirstSeq >= o.Cursor) || (!o.Desc && b.lastSeq() <= o.Cursor)) {
				continue
			}
			if (o.From != 0 && b.MaxTS < o.From) || (o.To != 0 && b.MinTS >= o.To) {
				continue
			}
			if (o.MaxScanBytes > 0 && scanned >= o.MaxScanBytes) || (!o.Deadline.IsZero() && time.Now().After(o.Deadline)) {
				if f != nil {
					_ = f.Close()
				}
				// Resume with this block: its lines were not visited.
				cur := b.lastSeq() + 1
				if !o.Desc {
					cur = b.FirstSeq - 1
				}
				return ScanResult{Cursor: cur, Partial: true}, nil
			}
			if f == nil {
				if f, err = os.Open(v.seg.path); err != nil {
					if errors.Is(err, fs.ErrNotExist) { // collected meanwhile
						break
					}
					return ScanResult{}, err
				}
			}
			raw, err := readBlock(f, b)
			scanned += int64(b.ULen)
			if err != nil {
				st.log.Warn("log store: unreadable block skipped", "service_id", serviceID, "segment", segName(v.seg.startNS), "offset", b.Offset, "err", err)
				continue
			}
			recs, err := decodeBlock(raw)
			if err != nil {
				st.log.Warn("log store: malformed block skipped", "service_id", serviceID, "segment", segName(v.seg.startNS), "offset", b.Offset, "err", err)
				continue
			}
			if o.Desc {
				for i := len(recs) - 1; i >= 0; i-- {
					if emit(recs[i]) {
						_ = f.Close()
						return stopped(), nil
					}
				}
			} else {
				for _, r := range recs {
					if emit(r) {
						_ = f.Close()
						return stopped(), nil
					}
				}
			}
		}
		if f != nil {
			_ = f.Close()
		}
	}
	if !o.Desc {
		for _, r := range pending {
			if emit(r) {
				return stopped(), nil
			}
		}
	}
	return ScanResult{Done: true}, nil
}

// SanitizeLine makes a log line safe to store: valid UTF-8 without NUL bytes.
func SanitizeLine(s string) string {
	if strings.ContainsRune(s, 0) {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}
