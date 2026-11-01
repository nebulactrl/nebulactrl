package statuspublic

import (
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limit defaults: a token bucket of 60 requests per client, refilled at
// one per second (60 per minute sustained). A page view is the document, the
// stylesheet and up to three fonts, so a browser is far below it.
const (
	defaultRatePerMinute = 60
	// maxClients bounds the table of per-client buckets.
	maxClients = 10000
	// overflowFactor sizes the shared bucket that serves clients that arrive
	// while the table is full, as a multiple of one client's rate.
	overflowFactor = 20
)

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// limiter is a per-client token bucket with a bounded table. A client is an
// IPv4 address or an IPv6 /64, so one host cannot multiply its allowance by
// rotating through the addresses of its prefix.
//
// When the table is full of recently active clients (a spray from many
// addresses), new clients share one overflow bucket instead of growing the
// table or being let through unlimited.
type limiter struct {
	mu       sync.Mutex
	clients  map[netip.Addr]*bucket
	overflow *rate.Limiter
	limit    rate.Limit
	burst    int
	max      int
	idle     time.Duration // a bucket idle this long is full again, so it can be dropped
	lastGC   time.Time
}

func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		perMinute = defaultRatePerMinute
	}
	lim := rate.Limit(float64(perMinute) / 60)
	return &limiter{
		clients:  map[netip.Addr]*bucket{},
		overflow: rate.NewLimiter(lim*overflowFactor, perMinute*overflowFactor),
		limit:    lim,
		burst:    perMinute,
		max:      maxClients,
		idle:     time.Minute + time.Second,
	}
}

// clientKey reduces an address to the unit that is limited.
func clientKey(ip netip.Addr) netip.Addr {
	if !ip.IsValid() {
		return netip.Addr{}
	}
	if ip.Is6() && !ip.Is4In6() {
		p, _ := ip.Prefix(64)
		return p.Addr()
	}
	return ip.Unmap()
}

// allow takes one token for ip at time now and reports whether the request may
// proceed.
func (l *limiter) allow(ip netip.Addr, now time.Time) bool {
	key := clientKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.clients[key]
	if !ok {
		if len(l.clients) >= l.max {
			l.gc(now)
		}
		if len(l.clients) >= l.max {
			return l.overflow.AllowN(now, 1)
		}
		b = &bucket{lim: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = b
	}
	b.seen = now
	return b.lim.AllowN(now, 1)
}

// gc drops idle buckets, at most once a second so a spray cannot make every
// request pay for a sweep.
func (l *limiter) gc(now time.Time) {
	if now.Sub(l.lastGC) < time.Second {
		return
	}
	l.lastGC = now
	for k, b := range l.clients {
		if now.Sub(b.seen) > l.idle {
			delete(l.clients, k)
		}
	}
}
