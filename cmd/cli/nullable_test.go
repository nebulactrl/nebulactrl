package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// The API sends null for a service that was never deployed, has no server or
// has not been observed yet, and the generated client types those fields as
// pointers: the renderers must cope with nil.

func TestServiceRenderersHandleNullObjects(t *testing.T) {
	never := apiclient.ServiceView{Name: "web"}
	require.Equal(t, "not deployed", serviceStatus(never))
	require.Empty(t, serviceServerName(never))
	require.Equal(t, "-", orDash(serviceServerName(never)))

	deployed := apiclient.ServiceView{
		LatestDeployment: &apiclient.DeploymentRef{Id: "d1", Status: "succeeded"},
		Server:           &apiclient.ServerRef{Name: "edge-1"},
	}
	require.Equal(t, "succeeded", serviceStatus(deployed), "not observed yet: the deployment status")
	require.Equal(t, "edge-1", serviceServerName(deployed))

	deployed.Observed = &apiclient.ObservedView{State: "running", Health: "healthy"}
	require.Equal(t, "running (healthy)", serviceStatus(deployed))
}

func TestProcessRenderersHandleNullObserved(t *testing.T) {
	require.Equal(t, "-", processState(apiclient.ProcessView{Name: "worker"}))
	require.Equal(t, "staged", processState(apiclient.ProcessView{Name: "worker", Pending: true}))
	require.Equal(t, "running (healthy)", processState(apiclient.ProcessView{
		Observed: &apiclient.ObservedProcessView{State: "running", Health: "healthy"},
	}))

	// A service that has not been observed has one process without a state.
	ps := processViewsOf(apiclient.ServiceView{})
	require.Len(t, ps, 1)
	require.Nil(t, ps[0].Observed)
	require.Equal(t, "-", processState(ps[0]))

	ps = processViewsOf(apiclient.ServiceView{Observed: &apiclient.ObservedView{State: "running"}})
	require.Len(t, ps, 1)
	require.NotNil(t, ps[0].Observed)
	require.Equal(t, "running", processState(ps[0]))
}

func TestDeploymentAndDomainRenderersHandleNullObjects(t *testing.T) {
	require.Equal(t, "-", releaseOf(nil))
	require.Equal(t, "git 0123456", releaseOf(&apiclient.ReleaseView{
		Source: apiclient.ReleaseSource{Type: "git", CommitSha: new("0123456789abcdef")},
	}))

	require.Equal(t, "-", cloudflareLabel(apiclient.DomainView{}))
	require.Equal(t, "-", cloudflareLabel(apiclient.DomainView{Dns: &apiclient.DomainDNS{}}))
}
