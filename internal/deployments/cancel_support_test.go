package deployments

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestSupportsCancel(t *testing.T) {
	require.True(t, supportsCancel(nil), "no facts yet: assume a current agent")
	old, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapDockerCleanup}})
	require.False(t, supportsCancel(old), "an agent from before cancel_deploy would reject the command")
	cur, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: []string{agentprotocol.CapCancelDeploy}})
	require.True(t, supportsCancel(cur))
}
