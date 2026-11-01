package meshlinks

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type nopDispatcher struct{}

func (nopDispatcher) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (nopDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

// The services domain drives the link domain through services.MeshLinker.
func TestServicesCanvasEdgesOpenAndCloseMeshGrants(t *testing.T) {
	f := newFx(t)
	svcs := services.NewService(f.pool, events.NewBus(), f.box, nopDispatcher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svcs.SetMeshLinker(f.s)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, nil)
	local := f.service("local", services.KindApp, &f.a, 9000, nil)

	// Preview: the plan states the devices and the blockers.
	pv, err := svcs.PreviewLink(f.ctx, f.env.ID, api.ID, pg.ID)
	require.NoError(t, err)
	require.True(t, pv.Mesh.CrossServer)
	require.Equal(t, 1, pv.Mesh.DevicesAdded)
	pv, err = svcs.PreviewLink(f.ctx, f.env.ID, api.ID, local.ID)
	require.NoError(t, err)
	require.False(t, pv.Mesh.CrossServer, "same server: no mesh and no 'different servers' warning")
	require.Empty(t, pv.Warnings)

	// A link that adds a device needs acceptMeshChanges.
	_, err = svcs.CreateLink(f.ctx, f.env.ID, api.ID, pg.ID)
	requireCode(t, err, services.CodeMeshConfirmationRequired)
	require.Empty(t, f.grants())
	res, err := svcs.CreateLink(f.ctx, f.env.ID, api.ID, pg.ID, services.AcceptMeshChanges())
	require.NoError(t, err)
	require.Equal(t, 1, res.Mesh.DevicesAdded)
	require.Equal(t, []grantRow{{"api", "postgres", SourceEdge}}, f.grants())

	// A same-server link is never a mesh matter.
	_, err = svcs.CreateLink(f.ctx, f.env.ID, api.ID, local.ID)
	require.NoError(t, err)
	require.Len(t, f.grants(), 1)

	// Deleting the edge closes its grant at once, link and exposure included.
	links, err := svcs.ListLinks(f.ctx, f.env.ID)
	require.NoError(t, err)
	var edgeID string
	for _, l := range links {
		if l.ToServiceID == pg.ID {
			edgeID = l.ID
		}
	}
	require.NotEmpty(t, edgeID)
	require.NoError(t, svcs.DeleteLink(f.ctx, edgeID))
	require.Empty(t, f.grants())
	n, e := f.counts()
	require.Zero(t, n+e)
}

func TestServicesBlockedEdgeStillCreatesTheCanvasLink(t *testing.T) {
	f := newFx(t)
	svcs := services.NewService(f.pool, events.NewBus(), f.box, nopDispatcher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svcs.SetMeshLinker(f.s)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, nil)
	f.fm.blocked[f.a.ID] = mesh.Blocker{Code: apierr.CodeMeshServerRequired, Message: "enable the mesh on srv-a first"}
	res, err := svcs.CreateLink(f.ctx, f.env.ID, api.ID, pg.ID)
	require.NoError(t, err, "blockers are reported, the deploy of the consumer refuses them")
	require.Len(t, res.Mesh.Blockers, 1)
	require.Empty(t, f.grants())
}

func TestServicesDeleteAndMoveReachTheLinkDomain(t *testing.T) {
	f := newFx(t)
	svcs := services.NewService(f.pool, events.NewBus(), f.box, nopDispatcher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svcs.SetMeshLinker(f.s)
	pg := f.service("postgres", services.KindApp, &f.b, 5432, nil)
	api := f.service("api", services.KindApp, &f.a, 8080, refVars)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Len(t, f.grants(), 1)

	// Moving the provider repairs at once: its exposure sits on the old server.
	v, err := svcs.Update(f.ctx, pg.ID, f.generation(pg), services.UpdateInput{ServerID: &f.c.ID})
	require.NoError(t, err)
	require.Equal(t, f.c.ID, *v.ServerID)
	require.Empty(t, f.grants())

	// Deleting a consumer drops what it held.
	f.move(pg, &f.b)
	f.reconcile(ModeAdd, ForConsumers(api.ID))
	require.Len(t, f.grants(), 1)
	require.NoError(t, svcs.Delete(f.ctx, api.ID, services.DeleteInput{Confirm: "api"}))
	require.Empty(t, f.grants())
	n, e := f.counts()
	require.Zero(t, n+e)
}
