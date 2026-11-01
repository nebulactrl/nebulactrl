package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func patchingCmd(t *testing.T, p agentprotocol.PatchingPayload) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-p", Kind: agentprotocol.KindHostPatching, Payload: raw}
}

// The agent reaches the host for patching the way it does for hardening: a
// privileged helper container of its own image that enters PID 1's namespaces.
func TestHostPatchingThroughHelperContainer(t *testing.T) {
	d := &hostCmdDocker{fakeDocker: newFakeDocker(), respond: func(script string) docker.HostCommandResult {
		return docker.HostCommandResult{Stdout: "@@BOOTID boot-9\n@@MANAGER apt\n@@REFRESH 0\n@@UPDATES\nInst libssl3 [1] (2 Ubuntu:22.04/jammy-security [amd64])\n@@UPDATESRC 0\n@@REBOOT 1\n@@REBOOTPKGS\nlibssl3\n@@END\n"}
	}}
	self := d.seed(selfName, "", "", true, time.Hour)
	self.Image, self.ImageID = "nebula-agent1", "sha256:agentimage"
	e, err := New(Config{
		Docker: d, Proxy: &fakeProxy{}, Sender: &fakeSender{}, AgentContainer: selfName,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
	})
	require.NoError(t, err)

	res := e.HandleCommand(context.Background(), patchingCmd(t, agentprotocol.PatchingPayload{
		RunID: "r1", Action: agentprotocol.PatchingActionCheck, Trigger: agentprotocol.PatchingTriggerScheduled,
	}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.PatchingResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, "helper", out.Mode)
	require.Equal(t, 1, out.State.Updates)
	require.Equal(t, 1, out.State.Security)
	require.True(t, out.State.RebootRequired)
	require.Equal(t, "boot-9", out.State.BootID)

	require.Len(t, d.specs, 1)
	spec := d.specs[0]
	require.Equal(t, "sha256:agentimage", spec.Image)
	require.Equal(t, []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", "/bin/sh", "-c"}, spec.Argv[:len(spec.Argv)-1])
	require.True(t, strings.HasPrefix(spec.Name, "nebulactrl-hostcmd-"))
}

func TestHostPatchingRejectsBadPayloadAndKind(t *testing.T) {
	e, err := New(Config{
		Docker: newFakeDocker(), Proxy: &fakeProxy{}, Sender: &fakeSender{},
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
	})
	require.NoError(t, err)
	res := e.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: agentprotocol.KindHostPatching, Payload: json.RawMessage(`{`)})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "invalid host_patching payload")
	require.True(t, agentprotocol.AllowedKinds[agentprotocol.KindHostPatching])
}
