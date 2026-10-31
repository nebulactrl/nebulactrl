package api

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/events"
)

func scopeWith(owners map[uuid.UUID]uuid.UUID) *eventScope {
	return &eventScope{
		cache: map[uuid.UUID]uuid.UUID{},
		lookup: func(_ context.Context, id uuid.UUID) (uuid.UUID, bool) {
			ws, ok := owners[id]
			return ws, ok
		},
	}
}

func TestEventScopeKeepsEventsInTheirTeam(t *testing.T) {
	teamA, teamB := uuid.New(), uuid.New()
	registry, project, gone := uuid.New(), uuid.New(), uuid.New()
	s := scopeWith(map[uuid.UUID]uuid.UUID{registry: teamA, project: teamA})
	ctx := context.Background()

	// An id the directory resolves goes to its team only.
	ev := events.Event{Type: "registry.updated", ResourceID: registry.String()}
	require.True(t, s.allows(ctx, teamA, ev))
	require.False(t, s.allows(ctx, teamB, ev), "another team must not see a resource event")

	// A resource id nobody owns is not delivered to every team.
	ev = events.Event{Type: "registry.updated", ResourceID: gone.String()}
	require.False(t, s.allows(ctx, teamA, ev))
	require.False(t, s.allows(ctx, teamB, ev))

	// A deletion's row is gone and so is its owner: without a stamped team it
	// reaches nobody, and a hint with no id at all neither.
	ev = events.Event{Type: "project.deleted", ProjectID: gone.String(), ResourceID: gone.String()}
	require.False(t, s.allows(ctx, teamA, ev))
	require.False(t, s.allows(ctx, teamB, ev))
	require.False(t, s.allows(ctx, teamB, events.Event{Type: "status_page.updated"}))
	require.False(t, s.allows(ctx, teamB, events.Event{Type: "instance.backup.updated", ResourceID: "instance"}))
}

func TestEventScopeDeletionOfOneTeamNeverReachesAnother(t *testing.T) {
	teamA, teamB := uuid.New(), uuid.New()
	gone := uuid.New()
	s := scopeWith(nil) // the rows are deleted: nothing resolves
	bus := events.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feedA, feedB := bus.Subscribe(ctx), bus.Subscribe(ctx)
	pA := &auth.Principal{UserID: uuid.New(), WorkspaceID: teamA}
	pB := &auth.Principal{UserID: uuid.New(), WorkspaceID: teamB}

	for _, typ := range []string{"project.deleted", "environment.deleted", "service.deleted", "team.deleted", "server.removed", "member.removed"} {
		bus.Publish(events.Event{Type: typ, ProjectID: gone.String(), ResourceID: gone.String(), WorkspaceID: teamA.String()})
		a, b := <-feedA, <-feedB
		require.True(t, s.deliver(ctx, pA, a), "%s reaches the team that owned it", typ)
		require.False(t, s.deliver(ctx, pB, b), "%s of team A must never reach team B's feed", typ)
	}
}

func TestEventScopeInstanceEventsReachInstanceAdminsOnly(t *testing.T) {
	admin, member := uuid.New(), uuid.New()
	s := scopeWith(nil)
	s.isAdmin = func(_ context.Context, id uuid.UUID) (bool, error) { return id == admin, nil }
	ctx := context.Background()
	ev := events.Event{Type: "instance.backup.updated", ResourceID: "instance", Instance: true}
	require.True(t, s.deliver(ctx, &auth.Principal{UserID: admin, WorkspaceID: uuid.New()}, ev))
	require.False(t, s.deliver(ctx, &auth.Principal{UserID: member, WorkspaceID: uuid.New()}, ev))
	require.False(t, scopeWith(nil).deliver(ctx, &auth.Principal{UserID: admin}, ev), "no directory means nobody is an admin")
}

func TestEventScopeHonoursTheStampedWorkspace(t *testing.T) {
	teamA, teamB := uuid.New(), uuid.New()
	s := scopeWith(nil)
	ev := events.Event{Type: "anything.updated", ResourceID: uuid.NewString(), WorkspaceID: teamA.String()}
	require.True(t, s.allows(context.Background(), teamA, ev))
	require.False(t, s.allows(context.Background(), teamB, ev))

	// A malformed stamp reaches nobody.
	ev.WorkspaceID = "not-a-team"
	require.False(t, s.allows(context.Background(), teamA, ev))
}
