package statuspublic

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

// entry is one host's rendered page (or the fact that it has none). The bodies
// are rendered once, when the entry is built, so serving is a map lookup.
type entry struct {
	notFound bool

	html, htmlGz []byte
	htmlETag     string
	json, jsonGz []byte
	jsonETag     string
	feed, feedGz []byte
	feedETag     string
	// page backs the badges, which are rendered per request from their query;
	// nil when the host has no page.
	page *statuspages.PublicPage

	fetchedAt time.Time
	gen       uint64
}

// flight is one in-progress build of a host's entry; callers that arrive
// meanwhile wait for it instead of querying the database again.
type flight struct {
	done chan struct{}
	e    *entry
	err  error
}

// cache holds one entry per host for ttl, with these properties:
//   - one build per host at a time (singleflight);
//   - invalidate marks every entry stale at once (a page or uptime event), but
//     a stale entry is kept for staleGrace and served when the rebuild fails,
//     so a database blip does not take the status page down;
//   - at most max hosts, evicting unknown hosts first so requests with invented
//     Host headers cannot push real pages out.
type cache struct {
	ttl        time.Duration
	staleGrace time.Duration
	max        int
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
	flights map[string]*flight
	gen     uint64
}

func newCache(ttl, staleGrace time.Duration, max int, now func() time.Time) *cache {
	return &cache{
		ttl: ttl, staleGrace: staleGrace, max: max, now: now,
		entries: map[string]*entry{}, flights: map[string]*flight{},
	}
}

// minRebuildInterval is how long an entry is served at the very least, even
// when an event made it stale: a busy instance publishes uptime events all the
// time, and without a floor every request would rebuild the page.
const minRebuildInterval = 2 * time.Second

// fresh reports whether e may be served without a rebuild: it is younger than
// minRebuildInterval, or it is younger than the TTL and no event came since.
func (c *cache) fresh(e *entry, now time.Time) bool {
	age := now.Sub(e.fetchedAt)
	return age < minRebuildInterval || (e.gen == c.gen && age < c.ttl)
}

// invalidate makes every entry stale; the next request per host rebuilds.
func (c *cache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
}

// get returns the entry of host, building it with build when it is missing or
// stale. build runs without the lock, once per host at a time, under a context
// that outlives the first caller's request.
func (c *cache) get(ctx context.Context, host string, build func(context.Context) (*entry, error)) (*entry, error) {
	c.mu.Lock()
	now := c.now()
	old := c.entries[host]
	if old != nil && c.fresh(old, now) {
		c.mu.Unlock()
		return old, nil
	}
	if f, ok := c.flights[host]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return c.settle(f, old)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[host] = f
	gen := c.gen
	c.mu.Unlock()

	c.build(ctx, host, f, gen, build)
	return c.settle(f, old)
}

// build runs the builder as the flight's leader and always finishes the flight,
// also when the builder panics: a flight left open would hang every later
// request of the host.
func (c *cache) build(ctx context.Context, host string, f *flight, gen uint64, build func(context.Context) (*entry, error)) {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			f.e, f.err = nil, fmt.Errorf("build status page: panic: %v", r)
		}
		c.mu.Lock()
		delete(c.flights, host)
		if f.err == nil {
			f.e.fetchedAt = c.now()
			f.e.gen = gen
			c.store(host, f.e)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.e, f.err = build(bctx)
}

// settle turns a finished flight into a result: its entry, or on error the
// previous entry when it is still within the grace period.
func (c *cache) settle(f *flight, old *entry) (*entry, error) {
	if f.err == nil {
		return f.e, nil
	}
	if old != nil && c.now().Sub(old.fetchedAt) < c.staleGrace {
		return old, nil
	}
	return nil, f.err
}

// store adds e under the lock, evicting to stay within max.
func (c *cache) store(host string, e *entry) {
	if _, ok := c.entries[host]; !ok && len(c.entries) >= c.max {
		c.evict()
	}
	c.entries[host] = e
}

// evict removes one entry: one past its grace period, else the oldest unknown
// host, else the oldest entry.
func (c *cache) evict() {
	now := c.now()
	var oldestMiss, oldest string
	for h, e := range c.entries {
		if now.Sub(e.fetchedAt) >= c.staleGrace {
			delete(c.entries, h)
			return
		}
		if e.notFound && (oldestMiss == "" || e.fetchedAt.Before(c.entries[oldestMiss].fetchedAt)) {
			oldestMiss = h
		}
		if oldest == "" || e.fetchedAt.Before(c.entries[oldest].fetchedAt) {
			oldest = h
		}
	}
	switch {
	case oldestMiss != "":
		delete(c.entries, oldestMiss)
	case oldest != "":
		delete(c.entries, oldest)
	}
}

// size is the number of cached hosts (tests).
func (c *cache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
