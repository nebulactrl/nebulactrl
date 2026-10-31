// Package events is an in-process fan-out bus used to tell the web UI which
// resources changed (the GET /events SSE feed).
package events

import (
	"context"
	"sync"
)

// Event is an invalidation hint, not a data carrier.
type Event struct {
	Type          string `json:"type"`
	ProjectID     string `json:"projectId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	ResourceID    string `json:"resourceId,omitempty"`
	// WorkspaceID names the team the event belongs to (a team id, not part of
	// the wire format). When set, only that team's live feed receives the event;
	// when empty, the feed resolves the team from the other ids.
	WorkspaceID string `json:"-"`
	// Instance marks an event about the whole instance rather than a team: only
	// instance administrators' live feeds receive it. An event with neither a
	// WorkspaceID nor Instance must carry an id some team owns, otherwise the
	// feed drops it.
	Instance bool `json:"-"`
}

// Event types published by the core domains. Later domains add their own
// "<resource>.<action>" types.
const (
	TypeProjectCreated     = "project.created"
	TypeProjectUpdated     = "project.updated"
	TypeProjectDeleted     = "project.deleted"
	TypeEnvironmentCreated = "environment.created"
	TypeEnvironmentUpdated = "environment.updated"
	TypeEnvironmentDeleted = "environment.deleted"
	TypeTokenCreated       = "token.created"
	TypeTokenRevoked       = "token.revoked"
)

const subscriberBuffer = 128

// Bus fans events out to subscribers without ever blocking publishers: a
// subscriber whose buffer is full misses the event.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewBus returns an empty Bus.
func NewBus() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

// Publish delivers e to every current subscriber, dropping it for slow ones.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of events that is closed when ctx is done.
func (b *Bus) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	}()
	return ch
}
