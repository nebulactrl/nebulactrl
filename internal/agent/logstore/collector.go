package logstore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/logs"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Collector captures the runtime output of every managed service container
// into a Store, continuously: a viewer is not needed. It follows each running
// container, remembers per container how far it got (so an agent restart
// neither loses nor repeats lines) and picks up new containers, restarts and
// removals by listing the managed containers every ScanInterval.
type Collector struct {
	eng docker.Engine
	st  *Store
	log *slog.Logger

	// ScanInterval is how often the container list is refreshed (default 3 s).
	ScanInterval time.Duration
	// CheckpointFile persists the per-container positions ("" = memory only).
	CheckpointFile string
	now            func() time.Time

	capture atomic.Bool

	mu      sync.Mutex
	tailers map[string]*tailer   // container ID
	done    map[string]time.Time // container ID → StartedAt when its stream was read to the end
	ckpt    map[string]int64     // container ID → last stored timestamp (ns)
	dirty   bool
	wg      sync.WaitGroup
}

type tailer struct {
	cancel context.CancelFunc
}

// NewCollector builds a Collector. Capture is on until SetCapture(false).
func NewCollector(eng docker.Engine, st *Store, log *slog.Logger) *Collector {
	if log == nil {
		log = slog.Default()
	}
	c := &Collector{
		eng: eng, st: st, log: log, ScanInterval: 3 * time.Second, now: time.Now,
		tailers: map[string]*tailer{}, done: map[string]time.Time{}, ckpt: map[string]int64{},
	}
	c.capture.Store(true)
	return c
}

// SetCapture switches capture on or off. Off stops every tailer; the positions
// are kept, so turning it on again continues without a gap (as far as Docker
// still has the lines).
func (c *Collector) SetCapture(on bool) {
	if c.capture.Swap(on) == on {
		return
	}
	if !on {
		c.mu.Lock()
		for _, t := range c.tailers {
			t.cancel()
		}
		c.mu.Unlock()
	}
}

// Capturing reports whether capture is on.
func (c *Collector) Capturing() bool { return c.capture.Load() }

// Run scans until ctx ends, then waits for the tailers and saves the positions.
func (c *Collector) Run(ctx context.Context) {
	c.loadCheckpoints()
	t := time.NewTicker(c.ScanInterval)
	defer t.Stop()
	save := time.NewTicker(10 * time.Second)
	defer save.Stop()
	c.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			c.wg.Wait()
			c.saveCheckpoints()
			return
		case <-t.C:
			c.scan(ctx)
		case <-save.C:
			c.saveCheckpoints()
		}
	}
}

func (c *Collector) scan(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	list, err := c.eng.ContainerList(ctx, docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: ""},
	})
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("log capture: list containers", "err", err)
		}
		return
	}
	seen := make(map[string]bool, len(list))
	capturing := c.capture.Load()
	for _, ci := range list {
		seen[ci.ID] = true
		if !capturing {
			continue
		}
		c.mu.Lock()
		_, running := c.tailers[ci.ID]
		doneAt, finished := c.done[ci.ID]
		if finished && doneAt.Equal(ci.StartedAt) && !ci.Running {
			c.mu.Unlock()
			continue
		}
		if running {
			c.mu.Unlock()
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		c.tailers[ci.ID] = &tailer{cancel: cancel}
		c.wg.Add(1)
		c.mu.Unlock()
		go c.tail(tctx, ci)
	}
	// Forget containers that are gone.
	c.mu.Lock()
	for id := range c.ckpt {
		if !seen[id] {
			delete(c.ckpt, id)
			c.dirty = true
		}
	}
	for id := range c.done {
		if !seen[id] {
			delete(c.done, id)
		}
	}
	c.mu.Unlock()
}

// tail follows one container until its stream ends.
func (c *Collector) tail(ctx context.Context, ci docker.ContainerInfo) {
	defer c.wg.Done()
	defer func() {
		c.mu.Lock()
		if t := c.tailers[ci.ID]; t != nil {
			t.cancel()
			delete(c.tailers, ci.ID)
		}
		c.mu.Unlock()
	}()
	serviceID := ci.Labels[agentprotocol.LabelService]
	deployment := ci.Labels[agentprotocol.LabelDeployment]
	replica := 0
	if n, err := strconv.Atoi(ci.Labels[agentprotocol.LabelReplica]); err == nil && n >= 0 {
		replica = n
	}
	component := ci.Labels[agentprotocol.LabelComponent]
	process := ci.Labels[agentprotocol.LabelProcess]

	c.mu.Lock()
	last := c.ckpt[ci.ID]
	c.mu.Unlock()
	opts := docker.LogOptions{Follow: ci.Running, Tail: -1}
	if last > 0 {
		opts.Since = time.Unix(0, last)
	} else {
		// First sight: take what Docker still has, at most the retention window.
		days := c.st.Retention().Days
		opts.Since = c.now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	rc, err := c.eng.ContainerLogs(ctx, ci.ID, opts)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, docker.ErrNotFound) {
			c.log.Warn("log capture: open stream", "container", ci.Name, "err", err)
		}
		return
	}
	defer rc.Close()

	b := logs.NewBatcher(agentprotocol.LogFrame{ServiceID: serviceID, Stream: agentprotocol.StreamRuntime}, func(f agentprotocol.LogFrame) {
		recs := make([]Record, 0, len(f.Lines))
		var newest int64
		for _, l := range f.Lines {
			ts := l.TS.UnixNano()
			if ts <= last { // a line of the previous run that Docker repeats at the boundary
				continue
			}
			text := SanitizeLine(l.Text)
			switch {
			case component != "": // a stack's lines keep the "<component> | " prefix of the live stream
				text = component + " | " + text
			case process != "": // so do the lines of a process of a multi-process service
				text = process + " | " + text
			}
			recs = append(recs, Record{TS: ts, Replica: replica, DeploymentID: deployment, Text: text})
			newest = max(newest, ts)
		}
		if len(recs) == 0 {
			return
		}
		if err := c.st.Append(serviceID, recs); err != nil {
			c.log.Warn("log capture: store", "service_id", serviceID, "err", err)
		}
		c.mu.Lock()
		if newest > c.ckpt[ci.ID] {
			c.ckpt[ci.ID] = newest
			c.dirty = true
		}
		c.mu.Unlock()
	}, nil)
	err = logs.ReadLines(rc, func(ts time.Time, text string) {
		if ts.IsZero() {
			ts = c.now()
		}
		b.AddAt(ts, text)
	})
	b.Close()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.log.Debug("log capture: stream ended", "container", ci.Name, "err", err)
		return
	}
	// The stream ended cleanly: a stopped container is read to the end.
	if cur, ierr := c.eng.ContainerInspect(ctx, ci.ID); ierr == nil && !cur.Running && !cur.Restarting {
		c.mu.Lock()
		c.done[ci.ID] = cur.StartedAt
		c.mu.Unlock()
	}
}

// ---- positions -----------------------------------------------------------------

func (c *Collector) loadCheckpoints() {
	if c.CheckpointFile == "" {
		return
	}
	raw, err := os.ReadFile(c.CheckpointFile)
	if err != nil {
		return
	}
	var m map[string]int64
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	c.mu.Lock()
	for k, v := range m {
		c.ckpt[k] = v
	}
	c.mu.Unlock()
}

// saveCheckpoints writes the positions. The store is flushed first: a position
// never points beyond what is durable.
func (c *Collector) saveCheckpoints() {
	if c.CheckpointFile == "" {
		return
	}
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	if err := c.st.Flush(); err != nil {
		c.log.Warn("log capture: flush before saving positions", "err", err)
		return
	}
	c.mu.Lock()
	raw, _ := json.Marshal(c.ckpt)
	c.dirty = false
	c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.CheckpointFile), 0o700); err != nil {
		return
	}
	tmp := c.CheckpointFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		c.log.Warn("log capture: save positions", "err", err)
		return
	}
	if err := os.Rename(tmp, c.CheckpointFile); err != nil {
		c.log.Warn("log capture: save positions", "err", err)
	}
}
