package meshtest_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type world struct {
	p                   *meshtest.FakeProvider
	env, other          uuid.UUID
	srvA, srvB          uuid.UUID
	pg, api, who1, who2 uuid.UUID
}

func newWorld() world {
	w := world{
		p: meshtest.NewFakeProvider(), env: uuid.New(), other: uuid.New(), srvA: uuid.New(), srvB: uuid.New(),
		pg: uuid.New(), api: uuid.New(), who1: uuid.New(), who2: uuid.New(),
	}
	w.p.RegisterService(w.pg, w.env, w.srvA, "pg")
	w.p.RegisterService(w.who1, w.env, w.srvA, "whoami")
	w.p.RegisterService(w.who2, w.env, w.srvA, "whoami2")
	w.p.RegisterService(w.api, w.env, w.srvB, "api")
	return w
}

// A service exposure keeps the container port while it is free on the server node,
// the next one with the same port gets the first free port of 15000-15999, a
// replication exposure always takes a port from the range; ensuring twice is idempotent.
func TestFakeProviderMeshPortPolicy(t *testing.T) {
	w, ctx := newWorld(), context.Background()
	e1, err := w.p.EnsureExposure(ctx, nil, w.who1, agentprotocol.MeshPurposeService, 80)
	require.NoError(t, err)
	require.Equal(t, 80, e1.MeshPort)
	e2, err := w.p.EnsureExposure(ctx, nil, w.who2, agentprotocol.MeshPurposeService, 80)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.MeshPortMin, e2.MeshPort)
	r, err := w.p.EnsureExposure(ctx, nil, w.pg, agentprotocol.MeshPurposeReplication, 5432)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.MeshPortMin+1, r.MeshPort)
	again, err := w.p.EnsureExposure(ctx, nil, w.who1, agentprotocol.MeshPurposeService, 80)
	require.NoError(t, err)
	require.Equal(t, e1, again)
	_, err = w.p.EnsureExposure(ctx, nil, uuid.New(), agentprotocol.MeshPurposeService, 80)
	require.ErrorIs(t, err, meshtest.ErrUnknownService)
}

// Grant rejects an environment mismatch, a purpose mismatch and a wrong server, like
// the real provider: a link of one environment can never open another's port.
func TestFakeProviderGrantRules(t *testing.T) {
	w, ctx := newWorld(), context.Background()
	exp, err := w.p.EnsureExposure(ctx, nil, w.who1, agentprotocol.MeshPurposeService, 80)
	require.NoError(t, err)
	link, err := w.p.EnsureLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	require.NoError(t, err)
	require.NoError(t, w.p.Grant(ctx, nil, link.ID, exp.ID, w.api, "reference"))
	require.NoError(t, w.p.Grant(ctx, nil, link.ID, exp.ID, w.api, "reference"), "idempotent")
	require.Len(t, w.p.Grants(), 1)

	otherEnv, err := w.p.EnsureLink(ctx, nil, w.other, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	require.NoError(t, err)
	require.ErrorIs(t, w.p.Grant(ctx, nil, otherEnv.ID, exp.ID, w.api, "reference"), meshtest.ErrEnvMismatch)
	repl, err := w.p.EnsureLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeReplication)
	require.NoError(t, err)
	require.ErrorIs(t, w.p.Grant(ctx, nil, repl.ID, exp.ID, w.api, "reference"), meshtest.ErrPurposeMismatch)
	wrongServer, err := w.p.EnsureLink(ctx, nil, w.env, w.srvB, uuid.New(), agentprotocol.MeshPurposeService)
	require.NoError(t, err)
	require.ErrorIs(t, w.p.Grant(ctx, nil, wrongServer.ID, exp.ID, w.api, "reference"), meshtest.ErrServerMismatch)
	require.Len(t, w.p.Links(), 4)
}

// Revoke and Release cascade by reference count: the link and the exposure go with
// their last grant, and stay while another ref needs them.
func TestFakeProviderReleaseIsRefCounted(t *testing.T) {
	w, ctx := newWorld(), context.Background()
	exp, _ := w.p.EnsureExposure(ctx, nil, w.who1, agentprotocol.MeshPurposeService, 80)
	link, _ := w.p.EnsureLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	other := uuid.New()
	require.NoError(t, w.p.Grant(ctx, nil, link.ID, exp.ID, w.api, "reference"))
	require.NoError(t, w.p.Grant(ctx, nil, link.ID, exp.ID, other, "edge"))

	require.NoError(t, w.p.Release(ctx, nil, w.api))
	require.Len(t, w.p.Links(), 1, "the other consumer still needs the link")
	require.Len(t, w.p.Exposures(), 1)
	require.NoError(t, w.p.Revoke(ctx, nil, link.ID, exp.ID, other, "edge"))
	require.Empty(t, w.p.Links())
	require.Empty(t, w.p.Exposures())
}

// RouteOf answers only for a provider on another server with a granted link of the
// consumer's server; PlanLink states devices and blockers.
func TestFakeProviderRouteAndPlan(t *testing.T) {
	w, ctx := newWorld(), context.Background()
	_, ok, err := w.p.RouteOf(ctx, nil, w.srvB, w.who1)
	require.NoError(t, err)
	require.False(t, ok, "no exposure yet")

	plan, err := w.p.PlanLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	require.NoError(t, err)
	require.Equal(t, 1, plan.DevicesAdded)
	require.Empty(t, plan.Blockers)
	w.p.EnableMeshOn(w.srvA)
	plan, _ = w.p.PlanLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	require.Len(t, plan.Blockers, 1)
	require.Equal(t, "mesh_server_required", plan.Blockers[0].Code)
	w.p.EnableMeshOn(w.srvA, w.srvB)

	exp, _ := w.p.EnsureExposure(ctx, nil, w.who1, agentprotocol.MeshPurposeService, 80)
	link, _ := w.p.EnsureLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	require.NoError(t, w.p.Grant(ctx, nil, link.ID, exp.ID, w.api, "reference"))
	w.p.SetLinkState(link.ID, agentprotocol.MeshStateOnline)
	rt, ok, err := w.p.RouteOf(ctx, nil, w.srvB, w.who1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "whoami", rt.Host)
	require.Equal(t, 80, rt.Port)
	require.Equal(t, link.ID, rt.LinkID)
	require.Equal(t, agentprotocol.MeshStateOnline, rt.State)
	_, ok, _ = w.p.RouteOf(ctx, nil, w.srvA, w.who1)
	require.False(t, ok, "a provider on the consumer's own server needs no route")

	plan, _ = w.p.PlanLink(ctx, nil, w.env, w.srvB, w.srvA, agentprotocol.MeshPurposeService)
	require.True(t, plan.LinkExists)
	require.Zero(t, plan.DevicesAdded)
	require.Equal(t, 3, w.p.Count("PlanLink"))
}
