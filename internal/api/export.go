package api

import "time"

// IPLimiter is a per-key token bucket (see NewIPLimiter).
type IPLimiter = ipLimiter

// NewIPLimiter allows n events per interval per key. Domain packages use it to
// protect public endpoints (invite preview and sign-up).
func NewIPLimiter(n int, interval time.Duration) *IPLimiter { return newIPLimiter(n, interval) }
