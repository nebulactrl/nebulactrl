package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	svcA = "aaaaaaaa-0000-4000-8000-00000000000a"
	svcB = "bbbbbbbb-0000-4000-8000-00000000000b"
	depA = "a1a1a1a1-0000-4000-8000-00000000000a"
	depB = "b1b1b1b1-0000-4000-8000-00000000000b"
)

// servicePayload is a rolling image deploy of one service with one host. Its
// Routes snapshot is empty: it was dispatched before any other route existed.
func servicePayload(serviceID, deploymentID, slug string) agentprotocol.DeployPayload {
	p := basePayload()
	p.ServiceID, p.DeploymentID, p.ServiceSlug = serviceID, deploymentID, slug
	p.Hosts = []string{slug + ".localhost"}
	p.Routes = nil
	return p
}

func (r *rig) send(kind, id string, payload any) agentprotocol.ResultFrame {
	r.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(r.t, err)
	return r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: id, Kind: kind, Payload: raw})
}

func routeServices(table []agentprotocol.Route) []string {
	var ids []string
	for _, rt := range table {
		ids = append(ids, rt.ServiceID)
	}
	return ids
}

// Two deploys of different services run at once on one server. Each payload
// was built before the other's route existed, so neither knows about it; the
// proxy is slow to apply. Both routes must be live at the end.
func TestConcurrentDeploysOfDifferentServicesKeepBothRoutes(t *testing.T) {
	r := newRig(t, nil)
	r.px.applyDelay = 40 * time.Millisecond

	var wg sync.WaitGroup
	results := make([]agentprotocol.ResultFrame, 2)
	for i, p := range []agentprotocol.DeployPayload{
		servicePayload(svcA, depA, "alpha"),
		servicePayload(svcB, depB, "beta"),
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = r.send(agentprotocol.KindDeploy, "cmd-"+p.ServiceSlug, p)
		}()
	}
	wg.Wait()
	for _, res := range results {
		require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	}

	cur := r.px.Current()
	require.ElementsMatch(t, []string{svcA, svcB}, routeServices(cur), "neither deploy may drop the other's route")
	for _, rt := range cur {
		require.Len(t, rt.Upstreams, 1)
	}
}

// A deploy dispatched before another route was applied must not remove that
// route, and the restore after a failed verification must keep it too.
func TestDeployBuildsItsTableFromTheLiveRoutes(t *testing.T) {
	r := newRig(t, nil)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{
		{ServiceID: svcB, Hosts: []string{"beta.localhost"}, Upstreams: []string{"nc-beta-b1b1b1b1-0:80"}},
	}))
	r.px.applies = nil

	res := r.send(agentprotocol.KindDeploy, "cmd-a", servicePayload(svcA, depA, "alpha"))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Len(t, r.px.applies, 1)
	require.Equal(t, []string{svcB, svcA}, routeServices(r.px.applies[0]))

	// Verification fails: the table goes back to what was live, with svcB.
	r2 := newRig(t, nil)
	require.NoError(t, r2.px.Apply([]agentprotocol.Route{
		{ServiceID: svcB, Hosts: []string{"beta.localhost"}, Upstreams: []string{"nc-beta-b1b1b1b1-0:80"}},
	}))
	r2.px.probes = []int{502}
	res = r2.send(agentprotocol.KindDeploy, "cmd-a", servicePayload(svcA, depA, "alpha"))
	require.NotEqual(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, []string{svcB}, routeServices(r2.px.Current()))
	require.Len(t, r2.px.applies, 3, "new table, then the restore of the live one")
}

// The payload's routes seed a proxy that has no state yet (a fresh agent).
func TestDeploySeedsAnEmptyProxyFromThePayload(t *testing.T) {
	r := newRig(t, nil)
	p := servicePayload(svcA, depA, "alpha")
	p.Routes = []agentprotocol.Route{{ServiceID: svcB, Hosts: []string{"beta.localhost"}, Upstreams: []string{"nc-beta-b1b1b1b1-0:80"}}}

	res := r.send(agentprotocol.KindDeploy, "cmd-a", p)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.ElementsMatch(t, []string{svcA, svcB}, routeServices(r.px.Current()))
}

// Once the proxy has state the payload's snapshot is ignored: a route it
// lists that the proxy no longer has stays gone.
func TestDeployIgnoresPayloadRoutesOnceTheProxyHasState(t *testing.T) {
	r := newRig(t, nil)
	require.NoError(t, r.px.Apply([]agentprotocol.Route{
		{ServiceID: svcB, Hosts: []string{"beta.localhost"}, Upstreams: []string{"nc-beta-b1b1b1b1-0:80"}},
	}))
	p := servicePayload(svcA, depA, "alpha")
	p.Routes = []agentprotocol.Route{{ServiceID: "gone", Hosts: []string{"gone.localhost"}, Upstreams: []string{"nc-gone-0:80"}}}

	res := r.send(agentprotocol.KindDeploy, "cmd-a", p)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.ElementsMatch(t, []string{svcA, svcB}, routeServices(r.px.Current()))
}

// Removing one service drops only its routes.
func TestRemoveKeepsTheOtherServicesRoutes(t *testing.T) {
	r := newRig(t, nil)
	r.px.applyDelay = 5 * time.Millisecond
	for _, p := range []agentprotocol.DeployPayload{
		servicePayload(svcA, depA, "alpha"),
		servicePayload(svcB, depB, "beta"),
		servicePayload(svcID, depV2, "web"),
	} {
		res := r.send(agentprotocol.KindDeploy, "cmd-"+p.ServiceSlug, p)
		require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	}
	require.ElementsMatch(t, []string{svcA, svcB, svcID}, routeServices(r.px.Current()))

	res := r.send(agentprotocol.KindRemoveService, "rm", agentprotocol.ServiceActionPayload{ServiceID: svcB, StopGraceSeconds: 1})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.ElementsMatch(t, []string{svcA, svcID}, routeServices(r.px.Current()))
}
