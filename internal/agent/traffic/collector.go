package traffic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// FlushInterval is how often completed minutes are stored and sent.
	FlushInterval = 60 * time.Second
	// flushGrace is how long after a minute ends late lines are still accepted.
	flushGrace = 5 * time.Second
	// framePage is the number of buckets per traffic frame.
	framePage      = 100
	defaultAckWait = 30 * time.Second
	// maxLineBytes bounds one access log line as docker delivers it (chunk
	// timestamps included); a longer line is dropped and counted. Traefik
	// accepts headers of up to 1 MiB, and JSON can escape a byte to six.
	maxLineBytes = 8 << 20
	// dockerChunk is the size docker cuts a log message into: every chunk but
	// the last carries exactly this many bytes of the message.
	dockerChunk = 16 << 10
)

// Buffer is the durable store for unsent minutes, the log cursor and the
// visitor salt. *store.Store implements it.
type Buffer interface {
	PutTraffic(buckets []agentprotocol.TrafficBucket, flushedThrough time.Time) error
	TrafficFlushedThrough() (time.Time, bool)
	PendingTraffic(limit int) (keys [][]byte, buckets []agentprotocol.TrafficBucket, err error)
	DeleteTraffic(keys [][]byte) error
	PruneTraffic(cutoff time.Time) (int, error)
	TrafficSalt(day string, gen func() ([]byte, error)) ([]byte, error)
}

// Sender queues a traffic frame for the control plane. *conn.Client implements it.
type Sender interface {
	Traffic(agentprotocol.Traffic)
	// Connected reports whether the control-plane socket is currently up.
	Connected() bool
}

// Config configures a Collector.
type Config struct {
	Engine    docker.Engine
	Buffer    Buffer
	Sender    Sender
	Container string // proxy container whose stdout carries the access log (required)
	Logger    *slog.Logger
	Now       func() time.Time
	// Interval overrides FlushInterval (tests).
	Interval time.Duration
	// AckTimeout overrides how long a frame waits for its traffic_ack (default 30 s).
	AckTimeout time.Duration
	// Geo, when set, counts request origins by country (and city) from the
	// client IP; the IP itself is never stored or shipped. *GeoDB implements it.
	Geo GeoLocator
	// Requests, when set together with RequestLogs and Routes, receives one
	// line per routed request (see RequestRecord). *logstore.Store implements it.
	Requests RequestSink
	// RequestLogs returns the server's request-log setting; nil means off.
	RequestLogs func() *agentprotocol.RequestLogsConfig
	// Routes returns the proxy's current route table, to find the service that
	// owns the route of a request. (*proxy.Manager).Current implements it.
	Routes func() []agentprotocol.Route
	// RequestsResume returns, per service, the end time of the newest stored
	// request line (services without a line are absent). A request that ended
	// at or before its service's entry was stored by an earlier run and is not
	// stored again; the later ones are, whatever the access-log cursor says.
	RequestsResume func() map[string]time.Time
}

// Collector tails the proxy's access log, aggregates it and ships completed minutes.
type Collector struct {
	cfg Config
	log *slog.Logger
	agg *Aggregator
	// reqs is nil when request logs are not configured.
	reqs *requestWriter

	mu          sync.Mutex
	flushedUpTo time.Time // events before this minute start are late and dropped
	lastSeen    time.Time // newest event time read
	// reqSeen is the newest docker timestamp of a line handled; the tail
	// that follows a dropped stream skips lines up to reqSkip, the value of
	// reqSeen when that tail began (they were handled by the stream before).
	reqSeen time.Time
	reqSkip time.Time
	seq     uint64
	acks    chan uint64

	// oversized and unparsed count access lines dropped since the last report.
	oversized atomic.Int64
	unparsed  atomic.Int64
}

// NewCollector returns a Collector.
func NewCollector(cfg Config) *Collector {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = FlushInterval
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = defaultAckWait
	}
	c := &Collector{cfg: cfg, log: cfg.Logger.With("component", "traffic"), acks: make(chan uint64, 1)}
	c.agg = NewAggregator(NewClassifier(), &daySalts{buf: cfg.Buffer, cache: map[string][]byte{}})
	c.agg.SetGeo(cfg.Geo)
	if cfg.Requests != nil && cfg.RequestLogs != nil && cfg.Routes != nil {
		list := func(ctx context.Context) ([]docker.ContainerInfo, error) {
			return cfg.Engine.ContainerList(ctx, docker.ListFilter{
				Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
			})
		}
		c.reqs = newRequestWriter(cfg.Requests, cfg.RequestLogs, newRouteResolver(cfg.Routes, list, cfg.Now, c.log), c.log)
	}
	return c
}

// Ack records the control plane's acknowledgement of a traffic frame.
func (c *Collector) Ack(seq uint64) {
	select {
	case c.acks <- seq:
	default:
		select { // replace a stale ack
		case <-c.acks:
		default:
		}
		select {
		case c.acks <- seq:
		default:
		}
	}
}

// Run tails and flushes until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	start := c.cfg.Now().UTC()
	if ft, ok := c.cfg.Buffer.TrafficFlushedThrough(); ok {
		// Resume at the first unflushed minute: nothing lost, nothing counted twice.
		start = ft.Add(time.Minute)
	}
	c.mu.Lock()
	c.flushedUpTo = start.Truncate(time.Minute)
	c.lastSeen = start.Add(-time.Nanosecond)
	c.mu.Unlock()
	if c.reqs != nil && c.cfg.RequestsResume != nil {
		c.reqs.resume = c.cfg.RequestsResume()
	}

	var tail, writer sync.WaitGroup
	tail.Add(1)
	go func() { defer tail.Done(); c.tailLoop(ctx) }()
	// The writer outlives the tail: it stops once the tail can queue no more
	// lines, and then writes what is left.
	stopWriter := func() {}
	if c.reqs != nil {
		var wctx context.Context
		wctx, stopWriter = context.WithCancel(context.WithoutCancel(ctx))
		writer.Add(1)
		go func() { defer writer.Done(); c.reqs.Run(wctx) }()
	}

	timer := time.NewTimer(c.nextFlushDelay())
	defer timer.Stop()
	report := time.NewTicker(requestDropReport)
	defer report.Stop()
	for {
		select {
		case <-ctx.Done():
			tail.Wait()
			stopWriter()
			writer.Wait()
			c.reportDrops()
			return
		case <-timer.C:
			c.Flush(ctx)
			timer.Reset(c.nextFlushDelay())
		case <-report.C:
			c.reportDrops()
		}
	}
}

// reportDrops logs, and resets, the access lines that were left out because
// they were longer than maxLineBytes or could not be read: their requests are
// in no count and no request log.
func (c *Collector) reportDrops() {
	oversized, unparsed := c.oversized.Swap(0), c.unparsed.Swap(0)
	if oversized > 0 || unparsed > 0 {
		c.log.Warn("access log: lines dropped", "oversized", oversized, "unparseable", unparsed)
	}
}

// nextFlushDelay is the wait until the next flush. With the production
// interval, flushes land just after each minute boundary plus the late-line
// grace, so a minute is shipped as soon as it is complete (traffic is visible
// within about a minute) instead of up to one whole interval later, as an
// unaligned ticker would do. Other intervals (tests) tick as given.
func (c *Collector) nextFlushDelay() time.Duration {
	if c.cfg.Interval != FlushInterval {
		return c.cfg.Interval
	}
	now := c.cfg.Now()
	next := now.Truncate(time.Minute).Add(flushGrace + 100*time.Millisecond)
	if !next.After(now) {
		next = next.Add(time.Minute)
	}
	return next.Sub(now)
}

// Flush stores every completed minute and sends what the control plane has not
// acknowledged. It is exported for tests.
func (c *Collector) Flush(ctx context.Context) {
	now := c.cfg.Now().UTC()
	cutoff := now.Add(-flushGrace).Truncate(time.Minute)
	buckets := c.agg.Flush(cutoff)
	if err := c.cfg.Buffer.PutTraffic(buckets, cutoff.Add(-time.Minute)); err != nil {
		c.log.Error("store traffic buckets", "err", err)
	} else {
		c.mu.Lock()
		c.flushedUpTo = cutoff
		c.mu.Unlock()
	}
	if n, err := c.cfg.Buffer.PruneTraffic(now.Add(-agentprotocol.TrafficRetention)); err != nil {
		c.log.Warn("prune traffic buffer", "err", err)
	} else if n > 0 {
		c.log.Warn("dropped unsent traffic older than the buffer bound", "buckets", n)
	}
	c.drain(ctx)
}

// drain sends pending buckets page by page, deleting each page once acknowledged.
func (c *Collector) drain(ctx context.Context) {
	for ctx.Err() == nil && c.cfg.Sender.Connected() {
		keys, buckets, err := c.cfg.Buffer.PendingTraffic(framePage)
		if err != nil {
			c.log.Error("read traffic buffer", "err", err)
			return
		}
		if len(buckets) == 0 {
			return
		}
		c.mu.Lock()
		c.seq++
		seq := c.seq
		c.mu.Unlock()
		select { // forget a stale ack
		case <-c.acks:
		default:
		}
		c.cfg.Sender.Traffic(agentprotocol.Traffic{Seq: seq, Buckets: buckets})
		if !c.waitAck(ctx, seq) {
			return
		}
		if err := c.cfg.Buffer.DeleteTraffic(keys); err != nil {
			c.log.Error("delete sent traffic", "err", err)
			return
		}
	}
}

func (c *Collector) waitAck(ctx context.Context, seq uint64) bool {
	timer := time.NewTimer(c.cfg.AckTimeout)
	defer timer.Stop()
	for {
		select {
		case got := <-c.acks:
			if got == seq {
				return true
			}
		case <-timer.C:
			c.log.Warn("traffic frame not acknowledged; will resend", "seq", seq)
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (c *Collector) tailLoop(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := c.tail(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			c.log.Warn("proxy access log stream ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// tail follows the proxy container's stdout until the stream ends.
func (c *Collector) tail(ctx context.Context) error {
	c.mu.Lock()
	skipBefore := c.lastSeen // events at or before this were already counted
	c.reqSkip = c.reqSeen
	c.mu.Unlock()
	rc, err := c.cfg.Engine.ContainerLogs(ctx, c.cfg.Container, docker.LogOptions{
		Tail: -1, Follow: true, StdoutOnly: true, Since: skipBefore.Add(-time.Second),
	})
	if err != nil {
		return err
	}
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer rc.Close()
	go func() { <-tctx.Done(); _ = rc.Close() }()

	return c.consume(rc, skipBefore)
}

// consume handles the lines of a log stream until it ends. It returns nil at
// the end of the stream.
func (c *Collector) consume(rc io.Reader, skipBefore time.Time) error {
	r := bufio.NewReaderSize(rc, 64<<10)
	for {
		line, oversize, err := readLine(r)
		if oversize {
			c.oversized.Add(1)
		}
		if len(line) > 0 {
			c.handleLine(line, skipBefore)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// handleLine parses one timestamped docker log line and counts it. When
// request logs are on, it also queues the line as a request line; that
// decision is independent of the counting gate above (the cursor and the
// flushed minutes are keyed by the request's start, but a request is stored
// when it ends: one that began before the cursor or a flush and ends after is
// stored). The writer drops what an earlier run stored (RequestsResume); the
// docker timestamp drops what a dropped stream delivers a second time.
func (c *Collector) handleLine(line string, skipBefore time.Time) {
	dts, text := docker.ParseLogLine(line)
	ev, err := ParseLine([]byte(text))
	if err != nil {
		if !errors.Is(err, ErrIgnored) {
			c.unparsed.Add(1) // an access entry that could not be read
		}
		return // ignored or not an access entry
	}
	c.mu.Lock()
	count := ev.Time.After(skipBefore) && !ev.Time.Before(c.flushedUpTo)
	if count && ev.Time.After(c.lastSeen) {
		c.lastSeen = ev.Time
	}
	storeRequest := c.reqs != nil && (dts.IsZero() || dts.After(c.reqSkip))
	if dts.After(c.reqSeen) {
		c.reqSeen = dts
	}
	c.mu.Unlock()
	if count {
		c.agg.Add(ev)
	}
	if storeRequest {
		c.reqs.Add(ev)
	}
}

// readLine returns the next line without its newline; oversize is true when
// the line was longer than maxLineBytes, which is consumed and returned empty.
// Docker cuts a log message of more than dockerChunk bytes into chunks and,
// with timestamps on, starts every chunk with its own: the stream then holds
// "<ts> <16 KiB><ts> <16 KiB><ts> <rest>" as one line. The continuation
// timestamps are removed, so the line is the message again.
func readLine(r *bufio.Reader) (line string, oversize bool, err error) {
	var buf []byte
	skipping := false
	for {
		part, isPrefix, rerr := r.ReadLine()
		if rerr != nil {
			return unchunk(buf), skipping, rerr
		}
		if !skipping {
			if len(buf)+len(part) > maxLineBytes {
				skipping, buf = true, nil
			} else {
				buf = append(buf, part...)
			}
		}
		if !isPrefix {
			return unchunk(buf), skipping, nil
		}
	}
}

// unchunk removes the timestamps docker put in the middle of a message it
// split: a line "<ts> <text>" whose text is longer than dockerChunk continues,
// after exactly dockerChunk bytes, with another "<ts> ". A line that does not
// follow that shape is returned as it is.
func unchunk(line []byte) string {
	i := bytes.IndexByte(line, ' ')
	if i <= 0 || len(line)-i-1 <= dockerChunk || !isTimestamp(line[:i]) {
		return string(line)
	}
	out := make([]byte, 0, len(line))
	out = append(out, line[:i+1]...)
	rest := line[i+1:]
	for len(rest) > dockerChunk {
		out = append(out, rest[:dockerChunk]...)
		rest = rest[dockerChunk:]
		j := bytes.IndexByte(rest, ' ')
		if j <= 0 || !isTimestamp(rest[:j]) {
			break // not a chunk boundary after all: keep the bytes
		}
		rest = rest[j+1:]
	}
	return string(append(out, rest...))
}

func isTimestamp(b []byte) bool {
	_, err := time.Parse(time.RFC3339Nano, string(b))
	return err == nil
}

// daySalts persists one random salt per UTC day.
type daySalts struct {
	buf   Buffer
	mu    sync.Mutex
	cache map[string][]byte
}

func (d *daySalts) Salt(t time.Time) []byte {
	day := t.UTC().Format("2006-01-02")
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.cache[day]; ok {
		return s
	}
	gen := func() ([]byte, error) {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return b, err
	}
	salt, err := d.buf.TrafficSalt(day, gen)
	if err != nil || len(salt) == 0 {
		salt, _ = gen() // memory-only: visitors split on restart, but never reuse a predictable salt
	}
	if len(d.cache) > 3 {
		clear(d.cache)
	}
	d.cache[day] = salt
	return salt
}
