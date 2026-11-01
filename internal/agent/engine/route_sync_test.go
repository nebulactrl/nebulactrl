package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// staleTable is the route table the control plane computes while the database
// still names the previous deployment (v1): the service's own route points at
// v1's container, and the change that triggered the push is a new host.
func staleTable() []agentprotocol.Route {
	return []agentprotocol.Route{
		{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}},
		{ServiceID: svcID, Hosts: []string{"whoami.localhost", "www.localhost"}, Upstreams: []string{oldCtr + ":80"}, HealthPath: "/old"},
	}
}

func ownRoute(t *testing.T, table []agentprotocol.Route) agentprotocol.Route {
	t.Helper()
	for _, r := range table {
		if r.ServiceID == svcID {
			return r
		}
	}
	require.FailNow(t, "no route of the service", "%v", table)
	return agentprotocol.Route{}
}

// A deploy that finished here after the control plane queued its table: the
// stale table must not put the retired release's containers back.
func TestSyncRoutesKeepsTheUpstreamsOfAServiceDeployedHere(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.px.applies = nil

	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: staleTable()}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)

	own := ownRoute(t, r.px.Current())
	require.Equal(t, []string{newCtr + ":80"}, own.Upstreams, "the live (v2) upstreams win over the control plane's stale v1")
	require.Equal(t, []string{agentprotocol.EnvNetwork(envID)}, own.Networks)
	require.Equal(t, "/", own.HealthPath, "the health path is part of the replica set")
	require.Equal(t, []string{"whoami.localhost", "www.localhost"}, own.Hosts, "what the push is for is still applied")
}

// A command that is still running for the service owns its upstreams, even
// before the agent has recorded the deployment.
func TestSyncRoutesKeepsTheUpstreamsOfAServiceMidCommand(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	// The in-flight deploy has switched the route; the database still says v1.
	require.NoError(t, r.px.Apply([]agentprotocol.Route{
		{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}},
		{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{newCtr + ":80"}, HealthPath: "/"},
	}))

	unlock := r.eng.lockService(svcID)
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: staleTable()}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	own := ownRoute(t, r.px.Current())
	require.Equal(t, []string{newCtr + ":80"}, own.Upstreams)
	require.Equal(t, []string{"whoami.localhost", "www.localhost"}, own.Hosts)

	// Nothing is running and nothing was deployed here: the control plane's
	// table is the only authority.
	unlock()
	_, pr = r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: staleTable()}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{oldCtr + ":80"}, ownRoute(t, r.px.Current()).Upstreams)
}

// The control plane's own replica set is respected when it matches what the
// agent deployed: replicas added by a scale are not reverted, and a table that
// names exactly the deployed containers is applied as sent.
func TestSyncRoutesAppliesATableThatNamesTheDeployedContainers(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	table := staleTable()
	table[1].Upstreams = []string{newCtr + ":80"}
	table[1].HealthPath = "/new"
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	own := ownRoute(t, r.px.Current())
	require.Equal(t, []string{newCtr + ":80"}, own.Upstreams)
	require.Equal(t, "/new", own.HealthPath, "no stale view: nothing is overridden")
}

// External upstreams are never the agent's to keep, in flight or not.
func TestSyncRoutesNeverKeepsExternalUpstreams(t *testing.T) {
	r := newRig(t, nil)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80")}))

	unlock := r.eng.lockService(routeSvc)
	defer unlock()
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{
		Routes: []agentprotocol.Route{extHTTP(routeSvc, "nas.example.com", "192.168.1.21:80")},
	}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{"192.168.1.21:80"}, r.px.Current()[0].Upstreams)
}

// A service whose first deployment has just been routed is missing from a table
// the control plane computed before it recorded the deployment. Applying that
// table as sent removed the route.
func TestSyncRoutesKeepsTheRouteOfAServiceDeployedHereThatTheTableLacks(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	table := staleTable()[:1] // only "other"
	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Len(t, r.px.Current(), 2)
	require.Equal(t, []string{newCtr + ":80"}, ownRoute(t, r.px.Current()).Upstreams)

	// Mid-command counts the same, before anything is recorded.
	r2 := newRig(t, nil)
	r2.previous()
	unlock := r2.eng.lockService(svcID)
	_, pr = r2.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: table}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{oldCtr + ":80"}, ownRoute(t, r2.px.Current()).Upstreams)
	unlock()
}

// A reconcile frame (a settings change resyncs mid-run) is as old as any queued
// table. A service on its first deployment, routed but not yet recorded here,
// is in neither the frame nor the agent's records: only its running command
// says it is live. Reconcile dropped its route, the proxy left its network, and
// requests to it hung or got the 404 page until the next push.
func TestReconcileKeepsTheRouteOfAServiceMidCommandThatTheFrameLacks(t *testing.T) {
	r := newRig(t, nil)
	other := agentprotocol.Route{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}
	r.dk.seed("nc-other-aaaaaaaa-0", "other", "dep-other", true, time.Hour)
	r.dk.seed(newCtr, svcID, depV2, true, time.Minute)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{
		other,
		{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{newCtr + ":80"}, Networks: []string{agentprotocol.EnvNetwork(envID)}},
	}))

	unlock := r.eng.lockService(svcID)
	frame := agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{{
		ServiceID: "other", DeploymentID: "dep-other", Replicas: 1, ContainerNames: []string{"nc-other-aaaaaaaa-0"}, Routes: []agentprotocol.Route{other},
	}}}
	require.NoError(t, r.eng.Reconcile(context.Background(), frame))
	require.Len(t, r.px.Current(), 2, "the in-flight service's route survives a frame that lacks it")
	own := ownRoute(t, r.px.Current())
	require.Equal(t, []string{newCtr + ":80"}, own.Upstreams)
	require.Equal(t, []string{agentprotocol.EnvNetwork(envID)}, own.Networks, "the proxy stays on the service's network")
	unlock()
}

// Absence still removes the routes of services this agent did not deploy:
// nothing is running for them, and external routes are the control plane's.
func TestSyncRoutesRemovesRoutesOfServicesNotDeployedHere(t *testing.T) {
	r := newRig(t, nil)
	live := append(r.previous(), extHTTP(routeSvc, "nas.example.com", "192.168.1.20:80"))
	require.NoError(t, r.px.Apply(live))

	_, pr := r.command(agentprotocol.KindSyncRoutes, agentprotocol.SyncRoutesPayload{Routes: live[:1]}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, live[:1], r.px.Current())
}

// proxy_configure with replacement routes follows the same rule.
func TestProxyConfigureReplacementKeepsTheUpstreamsOfAServiceDeployedHere(t *testing.T) {
	r, _ := newV2Rig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	_, pr := r.command(agentprotocol.KindProxyConfigure, agentprotocol.ProxyConfigurePayload{Routes: staleTable(), ReplaceRoutes: true}, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, pr.Outcome, pr.Message)
	require.Equal(t, []string{newCtr + ":80"}, ownRoute(t, r.px.Current()).Upstreams)
}
