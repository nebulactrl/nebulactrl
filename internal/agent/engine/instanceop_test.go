package engine

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func instanceOpCmd(op string) agentprotocol.Command {
	raw, _ := json.Marshal(agentprotocol.InstanceOpPayload{Op: op})
	return agentprotocol.Command{ID: "op-1", Kind: agentprotocol.KindInstanceOp, Payload: raw}
}

func TestInstanceOpRunsOnlyTheAllowListedCollectionInTheRegistryContainer(t *testing.T) {
	dk := newFakeDocker()
	dk.containers["reg1"] = &docker.ContainerInfo{ID: "reg1", Name: agentprotocol.RegistryContainerName, Running: true, State: "running"}
	var ranIn string
	var argv []string
	dk.onExec = func(id string, a []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		ranIn, argv = id, a
		_, _ = io.WriteString(stdout, "12 blobs eligible for deletion\n")
		return 0, nil
	}
	e, err := New(Config{
		Docker: dk, Proxy: &fakeProxy{}, Sender: &fakeSender{},
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
	})
	require.NoError(t, err)

	res := e.HandleCommand(context.Background(), instanceOpCmd(agentprotocol.InstanceOpRegistryGC))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, "reg1", ranIn)
	require.Equal(t, []string{"/bin/registry", "garbage-collect", "/etc/distribution/config.yml"}, argv)
	require.NotContains(t, argv, "--delete-untagged", "untagged manifests are the retention sweep's to delete")
	var out agentprotocol.InstanceOpResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Contains(t, out.Output, "blobs eligible")

	// The control plane sends a name, never a command line: anything else is refused and runs nothing.
	ranIn = ""
	for _, op := range []string{"", "shell", "registry_gc; id", "/bin/sh"} {
		res = e.HandleCommand(context.Background(), instanceOpCmd(op))
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, op)
	}
	require.Empty(t, ranIn)
	bad := agentprotocol.Command{ID: "op-2", Kind: agentprotocol.KindInstanceOp, Payload: json.RawMessage(`{`)}
	require.Equal(t, agentprotocol.OutcomeFailed, e.HandleCommand(context.Background(), bad).Outcome)

	// A failing collector reports its output; a host without the registry container fails clearly.
	dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		_, _ = io.WriteString(stdout, "storage error")
		return 3, nil
	}
	res = e.HandleCommand(context.Background(), instanceOpCmd(agentprotocol.InstanceOpRegistryGC))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "status 3")
	require.Contains(t, res.Error, "storage error")

	// An empty registry (no repositories directory yet) has nothing to collect: not a failure.
	dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		_, _ = io.WriteString(stdout, "failed to garbage collect: failed to mark: filesystem: Path not found: /docker/registry/v2/repositories")
		return 1, nil
	}
	res = e.HandleCommand(context.Background(), instanceOpCmd(agentprotocol.InstanceOpRegistryGC))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	delete(dk.containers, "reg1")
	res = e.HandleCommand(context.Background(), instanceOpCmd(agentprotocol.InstanceOpRegistryGC))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "does not exist on this host")

	dk.containers["reg2"] = &docker.ContainerInfo{ID: "reg2", Name: agentprotocol.RegistryContainerName, Running: false, State: "exited"}
	res = e.HandleCommand(context.Background(), instanceOpCmd(agentprotocol.InstanceOpRegistryGC))
	require.Contains(t, res.Error, "is not running")
}

func TestInstanceOpIsAnAllowedKind(t *testing.T) {
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindInstanceOp])
}
