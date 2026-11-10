package agents_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/reconciler"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// realDesired answers the hello's desired state like the control plane does:
// from the database, through the reconciler.
type realDesired struct {
	*fakeDomain
	rec *reconciler.Reconciler
}

func (d realDesired) DesiredState(ctx context.Context, serverID uuid.UUID) (agentprotocol.Reconcile, error) {
	return d.rec.DesiredState(ctx, serverID)
}

func useRealDesiredState(h *harness) {
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(h.t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jm := jobs.New(h.pool, logger)
	svcs := services.NewService(h.pool, h.bus, box, h.hub, logger)
	deps := deployments.NewService(deployments.Deps{Pool: h.pool, Bus: h.bus, Jobs: jm, Box: box, Dispatcher: h.hub, Services: svcs, Logger: logger})
	h.hub.SetResultHandler(realDesired{fakeDomain: h.dom, rec: reconciler.New(h.pool, h.bus, jm, deps, logger)})
}

func (h *harness) adoptionState(serverID uuid.UUID) string {
	h.t.Helper()
	var state string
	require.NoError(h.t, h.pool.QueryRow(h.ctx, "SELECT adoption_state FROM servers WHERE id = $1", serverID).Scan(&state))
	return state
}

func (h *harness) reconcileFrame(a *agentConn) agentprotocol.Reconcile {
	h.t.Helper()
	var rec agentprotocol.Reconcile
	require.NoError(h.t, a.next(agentprotocol.TypeReconcile).Into(&rec))
	return rec
}

func adoptionFacts(caps []string, managed ...string) agentprotocol.Facts {
	return agentprotocol.Facts{OS: "linux", Capabilities: caps, ManagedServices: managed}
}

// A server added to the instance that already runs workloads of an earlier
// NebulaCtrl is held on its first hello, and the very first reconcile frame
// tells the agent to leave them alone.
func TestAdoptionHoldOnAdd(t *testing.T) {
	h := newHarness(t)
	useRealDesiredState(h)
	e := h.enroll("edge-1")

	a := h.dialWithFacts(e, false, adoptionFacts([]string{agentprotocol.CapAdoption}, uuid.NewString(), uuid.NewString()))
	rec := h.reconcileFrame(a)
	require.True(t, rec.KeepUnknown, "the first frame already holds")
	require.Equal(t, "held", h.adoptionState(e.serverID))

	srv, err := h.svc.Get(h.ctx, e.serverID)
	require.NoError(t, err)
	require.NotNil(t, srv.Adoption)
	require.Equal(t, "held", srv.Adoption.State)
	require.Equal(t, 2, srv.Adoption.Found)
	require.Zero(t, srv.Adoption.Unmanaged)
}

// A reconnect of a server this instance already knows never holds, and a hold
// stays until it is cleared, also across reconnects.
func TestAdoptionKnownServerIsNotHeld(t *testing.T) {
	h := newHarness(t)
	useRealDesiredState(h)
	e := h.enroll("edge-1")

	// First hello: nothing managed on the host, the server becomes online.
	first := h.dialWithFacts(e, false, adoptionFacts([]string{agentprotocol.CapAdoption}))
	require.False(t, h.reconcileFrame(first).KeepUnknown)
	require.Eventually(t, func() bool { return h.hub.Online(e.serverID) }, 5*time.Second, 20*time.Millisecond)

	// A later hello that reports managed containers does not hold an online server.
	second := h.dialWithFacts(e, false, adoptionFacts([]string{agentprotocol.CapAdoption}, uuid.NewString()))
	require.False(t, h.reconcileFrame(second).KeepUnknown)
	require.Empty(t, h.adoptionState(e.serverID))
}

// An agent that cannot honour the hold (no CapAdoption), or a host with no
// managed containers, is not held.
func TestAdoptionNeedsTheCapabilityAndManagedServices(t *testing.T) {
	h := newHarness(t)
	useRealDesiredState(h)

	old := h.enroll("old-agent")
	a := h.dialWithFacts(old, false, adoptionFacts(nil, uuid.NewString()))
	require.False(t, h.reconcileFrame(a).KeepUnknown)
	require.Empty(t, h.adoptionState(old.serverID))

	empty := h.enroll("empty-host")
	b := h.dialWithFacts(empty, false, adoptionFacts([]string{agentprotocol.CapAdoption}))
	require.False(t, h.reconcileFrame(b).KeepUnknown)
	require.Empty(t, h.adoptionState(empty.serverID))
}

// Services left running unmanaged are carried in every frame, and a held
// server released from the hold stops saying KeepUnknown.
func TestAdoptionFrameCarriesUnmanagedAndHoldEnd(t *testing.T) {
	h := newHarness(t)
	useRealDesiredState(h)
	e := h.enroll("edge-1")
	svc := uuid.New()

	a := h.dialWithFacts(e, false, adoptionFacts([]string{agentprotocol.CapAdoption}, svc.String()))
	require.True(t, h.reconcileFrame(a).KeepUnknown)

	_, err := h.pool.Exec(h.ctx, "INSERT INTO server_unmanaged_services (server_id, service_id) VALUES ($1, $2)", e.serverID, svc)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, "UPDATE servers SET adoption_state = '' WHERE id = $1", e.serverID)
	require.NoError(t, err)

	b := h.dialWithFacts(e, false, adoptionFacts([]string{agentprotocol.CapAdoption}, svc.String()))
	rec := h.reconcileFrame(b)
	require.False(t, rec.KeepUnknown)
	require.Equal(t, []string{svc.String()}, rec.Unmanaged)

	srv, err := h.svc.Get(h.ctx, e.serverID)
	require.NoError(t, err)
	require.NotNil(t, srv.Adoption)
	require.Empty(t, srv.Adoption.State)
	require.Equal(t, 1, srv.Adoption.Unmanaged)
}

// When the hold cannot be recorded the hello gets no desired state at all: the
// agent sweeps unknown containers only after a reconcile frame, and a frame
// without KeepUnknown would tell it to.
func TestAdoptionHoldThatCannotBeRecordedSendsNoDesiredState(t *testing.T) {
	h := newHarness(t)
	useRealDesiredState(h)
	e := h.enroll("edge-1")

	_, err := h.pool.Exec(h.ctx, `
CREATE FUNCTION refuse_adoption_hold() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	RAISE EXCEPTION 'hold refused';
END $$;
CREATE TRIGGER refuse_adoption_hold BEFORE UPDATE OF adoption_state ON servers
	FOR EACH ROW WHEN (NEW.adoption_state = 'held') EXECUTE FUNCTION refuse_adoption_hold();`)
	require.NoError(t, err)

	a := h.dialWithFacts(e, false, adoptionFacts([]string{agentprotocol.CapAdoption}, uuid.NewString()))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, data, err := a.ws.Read(ctx)
		if err != nil {
			break // nothing but the deadline: no frame at all
		}
		f, err := agentprotocol.Decode(data)
		require.NoError(t, err)
		require.NotEqual(t, agentprotocol.TypeReconcile, f.Type, "no desired state while the hold is unrecorded")
	}
	require.Empty(t, h.adoptionState(e.serverID))
}
