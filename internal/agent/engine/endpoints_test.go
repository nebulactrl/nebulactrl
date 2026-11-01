package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type endpointRecorder struct {
	got []agentprotocol.InstanceEndpoints
}

func (r *endpointRecorder) Apply(eps agentprotocol.InstanceEndpoints) { r.got = append(r.got, eps) }

func TestReconcileHandsInstanceEndpointsToTheListenerManager(t *testing.T) {
	r := newRig(t, nil)
	rec := &endpointRecorder{}
	r.eng.cfg.Endpoints = rec

	// An older control plane's frame has none: the listeners are left alone.
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.Empty(t, rec.got)

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Endpoints: agentprotocol.StandardEndpoints()}))
	require.Len(t, rec.got, 1)
	require.Equal(t, agentprotocol.StandardEndpoints().Listen, rec.got[0].Listen)

	// An explicit empty set reaches the manager too: it stops the listeners.
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Endpoints: &agentprotocol.InstanceEndpoints{Listen: []agentprotocol.Endpoint{}}}))
	require.Len(t, rec.got, 2)
	require.Empty(t, rec.got[1].Listen)
}

func TestReconcileWithoutAListenerManagerIsFine(t *testing.T) {
	r := newRig(t, nil)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Endpoints: agentprotocol.StandardEndpoints()}))
}

// A build server pushes images to the built-in registry through 127.0.0.1, so
// it gets its endpoints although it reconciles nothing else.
func TestBuildServerStillGetsInstanceEndpoints(t *testing.T) {
	dk, snd := newFakeDocker(), &fakeSender{}
	rec := &endpointRecorder{}
	eng, err := New(Config{Docker: dk, Sender: snd, BuildOnly: true, Builder: &fakePusher{fakeBuilder: &fakeBuilder{}}, Endpoints: rec})
	require.NoError(t, err)
	require.NoError(t, eng.Reconcile(context.Background(), agentprotocol.Reconcile{Endpoints: agentprotocol.StandardEndpoints()}))
	require.Len(t, rec.got, 1)
}

func TestDecommissioningAgentStartsNoListeners(t *testing.T) {
	r := newRig(t, nil)
	rec := &endpointRecorder{}
	r.eng.cfg.Endpoints = rec
	r.eng.decom.Store(true)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Endpoints: agentprotocol.StandardEndpoints()}))
	require.Empty(t, rec.got)
}
