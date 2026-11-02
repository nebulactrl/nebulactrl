package proxyroutes

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestUpstreamNameChecksLeaveWhatOtherChecksOwnAlone(t *testing.T) {
	dev := principalCtx(policy.ScopeProjectsWrite)
	def := agentprotocol.DefaultUpstreamPolicy()

	// The shape of the address is CheckUpstream's business.
	require.NoError(t, checkUpstreamName(dev, "no-port", def))
	// Addresses are judged by CheckUpstream, not by a lookup.
	require.NoError(t, checkUpstreamName(dev, "192.168.1.20:80", def))
	require.NoError(t, checkUpstreamName(dev, "[::1]:80", def))
	// The host gateway alias is the policy's allowHostGateway decision, not a name to resolve.
	require.NoError(t, checkUpstreamName(dev, agentprotocol.HostGatewayName+":80", def))
	require.NoError(t, checkUpstreamName(dev, "Host.Docker.Internal.:80", def), "names compare case-insensitively and without the root dot")
}

// The system resolver answers localhost with a loopback address, so a developer
// cannot reach the control plane's own loopback through a name.
func TestUpstreamNameResolvedByTheSystemResolverIsJudged(t *testing.T) {
	err := checkUpstreamName(principalCtx(policy.ScopeProjectsWrite), "localhost:8080", agentprotocol.DefaultUpstreamPolicy())
	ae, ok := apierr.As(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	require.Equal(t, "upstream_denied", ae.Code)
	require.Equal(t, "localhost:8080", ae.Details["address"])
}
