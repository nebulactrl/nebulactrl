package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/hardening"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// hostCmdDocker adds docker.HostCommandRunner to the fake daemon.
type hostCmdDocker struct {
	*fakeDocker
	mu      sync.Mutex
	specs   []docker.HostCommandSpec
	respond func(script string) docker.HostCommandResult
}

func (d *hostCmdDocker) RunHostCommand(_ context.Context, spec docker.HostCommandSpec) (docker.HostCommandResult, error) {
	d.mu.Lock()
	d.specs = append(d.specs, spec)
	d.mu.Unlock()
	return d.respond(spec.Argv[len(spec.Argv)-1]), nil
}

func hardeningCmd(t *testing.T, p agentprotocol.HardeningPayload) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-h", Kind: agentprotocol.KindHostHardening, Payload: raw}
}

func TestHostHardeningThroughHelperContainer(t *testing.T) {
	d := &hostCmdDocker{fakeDocker: newFakeDocker(), respond: func(script string) docker.HostCommandResult {
		if strings.Contains(script, "@@NOLYNIS") {
			return docker.HostCommandResult{Stdout: "@@NOLYNIS\n"}
		}
		return docker.HostCommandResult{Stdout: "@@FIX umask pending\n@@FIX sysctl-kernel unavailable this host is a container\n"}
	}}
	self := d.seed(selfName, "", "", true, time.Hour)
	self.Image, self.ImageID = "nebula-agent1", "sha256:agentimage"
	e, err := New(Config{
		Docker: d, Proxy: &fakeProxy{}, Sender: &fakeSender{}, AgentContainer: selfName,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
	})
	require.NoError(t, err)

	res := e.HandleCommand(context.Background(), hardeningCmd(t, agentprotocol.HardeningPayload{
		RunID: "r1", Action: agentprotocol.HardeningActionAudit, Trigger: agentprotocol.HardeningTriggerScheduled,
	}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.HardeningResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, "helper", out.Mode)
	require.False(t, out.LynisInstalled)
	require.Len(t, out.Fixes, 2)
	require.Equal(t, agentprotocol.HardeningFixUnavailable, out.Fixes[1].State)

	require.Len(t, d.specs, 2, "one run for the audit, one for the fix states")
	spec := d.specs[0]
	require.Equal(t, "sha256:agentimage", spec.Image, "the helper is the agent's own image")
	require.Equal(t, []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", "/bin/sh", "-c"}, spec.Argv[:len(spec.Argv)-1])
	require.True(t, strings.HasPrefix(spec.Name, "nebulactrl-hostcmd-"))
	require.Equal(t, labelManagedHostCommand, spec.Labels[agentprotocol.LabelManaged])
}

func TestHostHardeningHostModeAndErrors(t *testing.T) {
	e, err := New(Config{
		Docker: newFakeDocker(), Proxy: &fakeProxy{}, Sender: &fakeSender{},
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
	})
	require.NoError(t, err)

	// Without a container the agent talks to the host itself.
	r, err := newHardeningRunner(context.Background(), e)
	require.NoError(t, err)
	require.IsType(t, hardening.DirectRunner{}, r)

	// A malformed payload fails cleanly.
	bad := agentprotocol.Command{ID: "c", Kind: agentprotocol.KindHostHardening, Payload: json.RawMessage(`{`)}
	require.Equal(t, agentprotocol.OutcomeFailed, e.HandleCommand(context.Background(), bad).Outcome)

	// A rollback without fix IDs is refused before anything touches the host.
	res := e.HandleCommand(context.Background(), hardeningCmd(t, agentprotocol.HardeningPayload{Action: agentprotocol.HardeningActionRollback}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "at least one fix")
}
