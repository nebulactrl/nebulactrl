// Package notificationstest holds a recording notifications.Emitter for tests
// of the domains that emit events.
package notificationstest

import (
	"context"
	"sync"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// Recorder remembers every event emitted to it.
type Recorder struct {
	mu     sync.Mutex
	events []notifications.Event
}

var _ notifications.Emitter = (*Recorder)(nil)

// Emit implements notifications.Emitter.
func (r *Recorder) Emit(_ context.Context, ev notifications.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// Events returns a copy of the recorded events, oldest first.
func (r *Recorder) Events() []notifications.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notifications.Event(nil), r.events...)
}

// OfType returns the recorded events of one type.
func (r *Recorder) OfType(t string) []notifications.Event {
	var out []notifications.Event
	for _, e := range r.Events() {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}
