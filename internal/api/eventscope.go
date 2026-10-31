package api

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/events"
)

// eventScopeCacheLimit bounds the id → team cache; it is dropped when full.
const eventScopeCacheLimit = 20000

// eventScope keeps the live feed inside one team. An event is delivered to the
// team it names (Event.WorkspaceID) or, when it names none, to the team that
// owns the project, environment or resource it carries. An event that names no
// team and carries no id a team owns (a deleted row, an unknown id, a hint with
// no id) is dropped: it cannot be shown to the right team, and showing it to
// every team would tell each of them what another is doing. Publishers stamp
// WorkspaceID on deletions for that reason. Events marked Event.Instance reach
// instance administrators only.
type eventScope struct {
	// lookup resolves the team that owns a resource id (auth.Service.EventWorkspace);
	// nil resolves nothing.
	lookup func(ctx context.Context, id uuid.UUID) (uuid.UUID, bool)
	// isAdmin reports whether a user is the instance administrator; nil means nobody is.
	isAdmin func(ctx context.Context, userID uuid.UUID) (bool, error)
	mu      sync.Mutex
	cache   map[uuid.UUID]uuid.UUID
}

func newEventScope(svc *auth.Service) *eventScope {
	e := &eventScope{cache: map[uuid.UUID]uuid.UUID{}}
	if svc != nil {
		e.lookup = svc.EventWorkspace
		e.isAdmin = svc.IsInstanceAdmin
	}
	return e
}

// deliver reports whether ev may be shown on the live feed of the caller p.
func (e *eventScope) deliver(ctx context.Context, p *auth.Principal, ev events.Event) bool {
	if ev.Instance {
		if e == nil || e.isAdmin == nil {
			return false
		}
		ok, err := e.isAdmin(ctx, p.UserID)
		return err == nil && ok
	}
	return e.allows(ctx, p.WorkspaceID, ev)
}

// allows reports whether the team-scoped ev may be shown to a caller acting in team ws.
func (e *eventScope) allows(ctx context.Context, ws uuid.UUID, ev events.Event) bool {
	if ev.Instance {
		return false
	}
	if ev.WorkspaceID != "" {
		id, err := uuid.Parse(ev.WorkspaceID)
		return err == nil && id == ws
	}
	if e == nil || e.lookup == nil {
		return true // no team directory (OpenAPI generation): nothing to scope by
	}
	for _, raw := range []string{ev.ProjectID, ev.EnvironmentID, ev.ResourceID} {
		id, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		if owner, ok := e.owner(ctx, id); ok {
			return owner == ws
		}
	}
	return false
}

func (e *eventScope) owner(ctx context.Context, id uuid.UUID) (uuid.UUID, bool) {
	e.mu.Lock()
	owner, ok := e.cache[id]
	e.mu.Unlock()
	if ok {
		return owner, true
	}
	if e.lookup == nil {
		return uuid.Nil, false
	}
	owner, ok = e.lookup(ctx, id)
	if !ok {
		return uuid.Nil, false
	}
	e.mu.Lock()
	if len(e.cache) >= eventScopeCacheLimit {
		e.cache = map[uuid.UUID]uuid.UUID{}
	}
	e.cache[id] = owner
	e.mu.Unlock()
	return owner, true
}
