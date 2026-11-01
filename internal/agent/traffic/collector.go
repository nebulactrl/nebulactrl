package traffic

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"sync"
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
	// maxLineBytes bounds one access log line; longer lines are skipped.
	maxLineBytes = 256 << 10
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
}

// Collector tails the proxy's access log, aggregates it and ships completed minutes.
type Collector struct {
	cfg Config
	log *slog.Logger
	agg *Aggregator

	mu          sync.Mutex
	flushedUpTo time.Time // events before this minute start are late and dropped
	lastSeen    time.Time // newest event time read
	seq         uint64
	acks        chan uint64
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

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); c.tailLoop(ctx) }()

	timer := time.NewTimer(c.nextFlushDelay())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-timer.C:
			c.Flush(ctx)
			timer.Reset(c.nextFlushDelay())
		}
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

	r := bufio.NewReaderSize(rc, 64<<10)
	for {
		line, err := readLine(r)
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

// handleLine parses one timestamped docker log line and counts it.
func (c *Collector) handleLine(line string, skipBefore time.Time) {
	_, text := docker.ParseLogLine(line)
	ev, err := ParseLine([]byte(text))
	if err != nil {
		return // ignored or not an access entry
	}
	c.mu.Lock()
	if !ev.Time.After(skipBefore) || ev.Time.Before(c.flushedUpTo) {
		c.mu.Unlock()
		return
	}
	if ev.Time.After(c.lastSeen) {
		c.lastSeen = ev.Time
	}
	c.mu.Unlock()
	c.agg.Add(ev)
}

// readLine returns the next line without its newline. Lines longer than
// maxLineBytes are consumed and returned empty.
func readLine(r *bufio.Reader) (string, error) {
	var buf []byte
	skipping := false
	for {
		part, isPrefix, err := r.ReadLine()
		if err != nil {
			return string(buf), err
		}
		if !skipping {
			if len(buf)+len(part) > maxLineBytes {
				skipping, buf = true, nil
			} else {
				buf = append(buf, part...)
			}
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
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
