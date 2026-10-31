package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// seqTable is a table of one service, in maintenance or not.
func seqTable(maintenance bool) []agentprotocol.Route {
	rt := agentprotocol.Route{ServiceID: "svc-a", Hosts: []string{"a.localhost"}, Upstreams: []string{"nc-a-aaaaaaaa-0:80"}}
	if maintenance {
		rt.Maintenance = &agentprotocol.Maintenance{Title: "Upgrading"}
	}
	return []agentprotocol.Route{rt}
}

func (r *rig) syncSeq(seq uint64, maintenance bool) agentprotocol.ProxyConfigureResult {
	r.t.Helper()
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: seqTable(maintenance), TableSeq: seq}, nil)
	require.Equal(r.t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	return pr
}

func inMaintenance(r *rig) bool {
	cur := r.px.Current()
	return len(cur) == 1 && cur[0].Maintenance != nil
}

// Tables are applied by concurrent handlers, so a table the control plane built
// before a maintenance window can be applied after the table that opened it. It
// must not end the window.
func TestAStaleTableNeverRevertsMaintenance(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.syncSeq(5, true)
	require.True(t, inMaintenance(r))
	applies := len(r.px.applies)

	pr := r.syncSeq(4, false)
	require.Contains(t, pr.Message, "Superseded by a newer route table")
	require.True(t, inMaintenance(r), "the older table did not end the window")
	require.Len(t, r.px.applies, applies, "nothing was applied")

	r.syncSeq(6, false)
	require.False(t, inMaintenance(r), "a newer table does end it")
}

func TestEqualAndUnnumberedTablesApply(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.syncSeq(5, true)

	r.syncSeq(5, false) // a redelivery of the same number applies
	require.False(t, inMaintenance(r))

	// An unnumbered table (older control plane) applies, and moves nothing.
	r.syncSeq(0, true)
	require.True(t, inMaintenance(r))
	pr := r.syncSeq(4, false)
	require.Contains(t, pr.Message, "Superseded", "the record stayed at 5")
	require.True(t, inMaintenance(r))
}

// The agent's own tables (deploys, scales, removals) are never numbered and do
// not move the record.
func TestAgentBuiltTablesDoNotMoveTheRecord(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.previous()
	r.syncSeq(5, false)
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, uint64(5), r.px.LoadTableSeq())
	pr := r.syncSeq(5, false)
	require.NotContains(t, pr.Message, "Superseded")
}

// A route-only deployment applies its own table, whatever the record says: the
// deployment would otherwise succeed without its route.
func TestRouteOnlyDeploymentIsNotSuperseded(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.syncSeq(9, false)
	table := seqTable(false)
	table[0].Hosts = []string{"b.localhost"}
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table, TableSeq: 3, DeploymentID: "dep-route"}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.NotContains(t, pr.Message, "Superseded")
	require.Equal(t, []string{"b.localhost"}, r.px.Current()[0].Hosts)
}

// proxy_configure always applies its configuration; a table older than one
// applied is dropped, the agent's own table stays.
func TestStaleProxyConfigureKeepsTheLiveTable(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	r.syncSeq(5, true)

	cfg := agentprotocol.ProxyConfig{UpstreamPolicy: agentprotocol.DefaultUpstreamPolicy()}
	_, pr := r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{
		Proxy: cfg, Routes: seqTable(false), ReplaceRoutes: true, TableSeq: 4,
	}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Contains(t, pr.Message, "superseded")
	require.Len(t, cp.cfgs, 1, "the configuration was installed")
	require.True(t, inMaintenance(r))

	_, pr = r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{
		Proxy: cfg, Routes: seqTable(false), ReplaceRoutes: true, TableSeq: 7,
	}, nil)
	require.NotContains(t, pr.Message, "superseded")
	require.False(t, inMaintenance(r))
	require.Equal(t, uint64(7), r.px.LoadTableSeq())
}

// A reconcile frame is a table too: a resync computed before a maintenance
// window and applied after it must not end it.
func TestAStaleReconcileFrameNeverRevertsMaintenance(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	ctx := context.Background()
	r.syncSeq(5, true)
	applies := len(r.px.applies)

	require.NoError(t, r.eng.Reconcile(ctx, agentprotocol.Reconcile{ExternalRoutes: seqTable(false), TableSeq: 4}))
	require.True(t, inMaintenance(r))
	require.Len(t, r.px.applies, applies)

	require.NoError(t, r.eng.Reconcile(ctx, agentprotocol.Reconcile{ExternalRoutes: seqTable(false), TableSeq: 6}))
	require.False(t, inMaintenance(r))
	require.Equal(t, uint64(6), r.px.LoadTableSeq())

	// An unnumbered frame (older control plane) applies.
	require.NoError(t, r.eng.Reconcile(ctx, agentprotocol.Reconcile{ExternalRoutes: seqTable(true)}))
	require.True(t, inMaintenance(r))
}

// After the control plane's database was restored from a backup its numbers go
// back: the hello frame replaces the record, and what follows is numbered from
// there.
func TestHelloFrameResetsTheRecord(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	ctx := context.Background()
	r.syncSeq(50, true)

	hello := agentprotocol.Reconcile{ExternalRoutes: seqTable(false), TableSeq: 3, Hello: true}
	r.eng.ObserveFrame(ctx, hello) // on the read loop, before the frame is applied
	require.Equal(t, uint64(3), r.px.LoadTableSeq())
	require.NoError(t, r.eng.Reconcile(ctx, hello))
	require.False(t, inMaintenance(r), "the frame applied although its number is lower")

	pr := r.syncSeq(4, true)
	require.NotContains(t, pr.Message, "Superseded")
	require.True(t, inMaintenance(r))
	pr = r.syncSeq(2, false)
	require.Contains(t, pr.Message, "Superseded")
}

// A hello frame applied late, after a newer command, does not undo it: the
// record was reset on arrival, the newer command raised it again.
func TestHelloFrameAppliedAfterANewerCommandIsSuperseded(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	ctx := context.Background()
	r.syncSeq(50, false)

	hello := agentprotocol.Reconcile{ExternalRoutes: seqTable(false), TableSeq: 10, Hello: true}
	r.eng.ObserveFrame(ctx, hello)
	r.syncSeq(11, true) // built after the frame, applied before it
	require.NoError(t, r.eng.Reconcile(ctx, hello))
	require.True(t, inMaintenance(r))
}

// The record survives an agent restart: a new engine over the same proxy keeps
// refusing tables older than the one the previous run applied.
func TestTableRecordSurvivesARestart(t *testing.T) {
	r, cp := newV2Rig(t, nil)
	r.syncSeq(8, true)

	e, err := New(Config{
		Docker: r.dk, Proxy: cp, Builder: r.bld, Sender: r.snd, Health: healthy,
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)
	r.eng = e
	pr := r.syncSeq(7, false)
	require.Contains(t, pr.Message, "Superseded")
	require.True(t, inMaintenance(r))
}
