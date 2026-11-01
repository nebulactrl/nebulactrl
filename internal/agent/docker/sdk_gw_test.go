package docker

import (
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func engineAt(t *testing.T, apiVersion string) *SDKEngine {
	t.Helper()
	cli, err := client.New(client.WithHost("tcp://127.0.0.1:1"), client.WithAPIVersion(apiVersion))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return &SDKEngine{cli: cli}
}

// GwPriority exists from API 1.48 (Docker 28); an older daemon gets the unset value.
func TestGwPriorityNeedsAPI148(t *testing.T) {
	want := agentprotocol.EdgeGwPriority
	require.Equal(t, want, engineAt(t, "1.48").gwPriority(want))
	require.Equal(t, want, engineAt(t, "1.52").gwPriority(want))
	require.Zero(t, engineAt(t, "1.47").gwPriority(want))
	require.Zero(t, engineAt(t, "1.44").gwPriority(want))
	require.Zero(t, engineAt(t, "1.52").gwPriority(0), "an unset priority stays unset")
}
