package services_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// A deletion names its team at the publish site: the feed cannot look the owner
// of a deleted row up, so without the stamp the event would reach nobody.
func TestPublishStampsTheOwningTeam(t *testing.T) {
	bus := events.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := bus.Subscribe(ctx)
	team, project, env, svc := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	sc := services.Scope{Service: db.Service{ID: svc}, WorkspaceID: team, ProjectID: project, EnvironmentID: env}
	services.PublishService(bus, services.EventServiceDeleted, sc)
	ev := <-feed
	require.Equal(t, services.EventServiceDeleted, ev.Type)
	require.Equal(t, team.String(), ev.WorkspaceID)

	services.PublishIn(bus, team, services.EventServiceDeleted, project, env, svc)
	require.Equal(t, team.String(), (<-feed).WorkspaceID)

	// No team known: the event stays unstamped (the feed resolves it from the ids).
	services.Publish(bus, services.EventServiceUpdated, project, env, svc)
	require.Empty(t, (<-feed).WorkspaceID)
	services.PublishIn(bus, uuid.Nil, services.EventServiceUpdated, project, env, svc)
	require.Empty(t, (<-feed).WorkspaceID)
}
