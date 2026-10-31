package agentprotocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidTaskCommand(t *testing.T) {
	require.NoError(t, ValidTaskCommand([]string{"rake", "db:migrate"}))
	require.Error(t, ValidTaskCommand(nil))
	require.Error(t, ValidTaskCommand([]string{"  "}))
	require.Error(t, ValidTaskCommand([]string{"a", "b\x00"}))
	require.Error(t, ValidTaskCommand([]string{strings.Repeat("x", 70<<10)}))
}

func TestRunTaskIsAnAllowedKindAndPayloadCarriesNoValues(t *testing.T) {
	require.True(t, AllowedKinds[KindRunTask])
	raw, err := json.Marshal(RunTaskPayload{RunID: "r", EnvKeys: []string{"API_KEY"}, SecretEnvKeys: []string{"API_KEY"}})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "value")
	require.Equal(t, "nc-task-web-a1a1a1a1", TaskContainerName("web", "a1a1a1a1-0000"))
	require.Equal(t, "nc-predeploy-web-d2d2d2d2", PreDeployContainerName("web", "d2d2d2d2-0000"))
}
