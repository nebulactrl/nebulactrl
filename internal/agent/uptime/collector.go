package uptime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const defaultAckWait = 30 * time.Second

// Buffer is the durable store of unacknowledged probe results.
// *store.Store implements it.
type Buffer interface {
	PutProbes(results []agentprotocol.ProbeResult) error
	PendingProbes(limit int) (keys [][]byte, results []agentprotocol.ProbeResult, err error)
	DeleteProbes(keys [][]byte) error
	PruneProbes(cutoff time.Time) (int, error)
}

// Sender queues a Probes frame for the control plane. *conn.Client implements it.
type Sender interface {
	Probes(agentprotocol.Probes)
	// Connected reports whether the control-plane socket is currently up.
	Connected() bool
}

// Config configures a Collector.
type Config struct {
	Buffer Buffer
	Sender Sender
	Logger *slog.Logger
	Now    func() time.Time
	// Interval overrides how often pending results are (re)sent when nothing
	// new arrives, e.g. after a reconnect (default agentprotocol.UptimeInterval).
	Interval time.Duration
	// AckTimeout overrides how long a frame waits for its probes_ack
	// (default 30 s).
	AckTimeout time.Duration
}

// Collector keeps probe results until the control plane acknowledges them. A
// result is stored before it is sent, so an agent restart or a lost
// connection loses nothing (within agentprotocol.ProbeRetention); the control
// plane ignores results it already holds, so resending is safe.
type Collector struct {
	cfg  Config
	log  *slog.Logger
	kick chan struct{}

	mu   sync.Mutex
	seq  uint64
	acks chan uint64
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
		cfg.Interval = agentprotocol.UptimeInterval
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = defaultAckWait
	}
	return &Collector{cfg: cfg, log: cfg.Logger.With("component", "uptime"), kick: make(chan struct{}, 1), acks: make(chan uint64, 1)}
}

// Ack records the control plane's acknowledgement of a Probes frame.
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

// Record stores the results of one probe cycle and wakes Run to send them. It
// never blocks on the control plane, so a slow or absent one cannot delay the
// next probe cycle.
func (c *Collector) Record(results []agentprotocol.ProbeResult) {
	if len(results) == 0 {
		return
	}
	if err := c.cfg.Buffer.PutProbes(results); err != nil {
		c.log.Error("store probe results", "err", err)
		return
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Run sends pending results when Record stores new ones and every Interval
// (which resends what a reconnect left unacknowledged), until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	c.Flush(ctx) // results a previous run left behind
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.kick:
		case <-t.C:
		}
		c.Flush(ctx)
	}
}

// Flush drops results older than the retention bound and sends the rest page
// by page while the control plane is connected, deleting each page once it is
// acknowledged. It is exported for tests.
func (c *Collector) Flush(ctx context.Context) {
	now := c.cfg.Now().UTC()
	if n, err := c.cfg.Buffer.PruneProbes(now.Add(-agentprotocol.ProbeRetention)); err != nil {
		c.log.Warn("prune probe buffer", "err", err)
	} else if n > 0 {
		c.log.Warn("dropped unsent probe results older than the retention bound", "results", n)
	}
	for ctx.Err() == nil && c.cfg.Sender.Connected() {
		keys, results, err := c.cfg.Buffer.PendingProbes(agentprotocol.MaxProbeResults)
		if err != nil {
			c.log.Error("read probe buffer", "err", err)
			return
		}
		if len(results) == 0 {
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
		c.cfg.Sender.Probes(agentprotocol.Probes{Seq: seq, Results: results})
		if !c.waitAck(ctx, seq) {
			return
		}
		if err := c.cfg.Buffer.DeleteProbes(keys); err != nil {
			c.log.Error("delete sent probe results", "err", err)
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
			c.log.Warn("probes frame not acknowledged; will resend", "seq", seq)
			return false
		case <-ctx.Done():
			return false
		}
	}
}
