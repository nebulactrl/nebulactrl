package deployhooks

import (
	"time"

	"github.com/google/uuid"
)

// SetLimit changes the token bucket of every hook (the defaults are 1 call per
// 10 s, burst 3).
func (s *Service) SetLimit(every time.Duration, burst int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.every, s.burst = every, burst
	s.limiters = map[uuid.UUID]*limiterEntry{}
}
