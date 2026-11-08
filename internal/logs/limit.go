package logs

import (
	"encoding/json"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// maxConcurrentReadsPerPrincipal is how many log reads (history and facets, of
// services and environments together) one user or token may have in flight.
// Each read occupies a read slot on the agent's host for up to its scan budget,
// and the slots are few: one caller must not be able to take them all.
const maxConcurrentReadsPerPrincipal = 4

// readLimiter counts the log reads in flight per principal.
type readLimiter struct {
	limit int

	mu       sync.Mutex
	inFlight map[uuid.UUID]int
}

func newReadLimiter(limit int) *readLimiter {
	return &readLimiter{limit: limit, inFlight: map[uuid.UUID]int{}}
}

// acquire takes a slot of key. It returns the function that gives it back, or
// false while key already has limit reads in flight. The entry of a key whose
// count drops to zero is removed.
func (l *readLimiter) acquire(key uuid.UUID) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[key] >= l.limit {
		return nil, false
	}
	l.inFlight[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.inFlight[key] <= 1 {
				delete(l.inFlight, key)
				return
			}
			l.inFlight[key]--
		})
	}, true
}

// middleware holds a slot of the calling principal (the token, else the user)
// for the whole request and answers 429 with a Retry-After of one second when
// the principal has no slot left. A request without a principal passes: the
// service rejects it.
func (l *readLimiter) middleware(hctx huma.Context, next func(huma.Context)) {
	p, ok := auth.PrincipalFrom(hctx.Context())
	if !ok {
		next(hctx)
		return
	}
	release, ok := l.acquire(p.ID())
	if !ok {
		e := apierr.TooManyRequests("too many log reads in flight; retry shortly")
		hctx.SetHeader("Retry-After", "1")
		hctx.SetHeader("Content-Type", "application/problem+json")
		hctx.SetStatus(e.Status)
		_ = json.NewEncoder(hctx.BodyWriter()).Encode(e)
		return
	}
	defer release()
	next(hctx)
}
