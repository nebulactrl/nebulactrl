package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/cleanup"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// cleanableDocker is the fake daemon plus the two calls a cleanup adds to the
// Engine interface.
type cleanableDocker struct{ *fakeDocker }

func (cleanableDocker) ImagesSize(context.Context) (int64, error) { return 0, nil }
func (cleanableDocker) VolumeList(context.Context) ([]docker.VolumeInfo, error) {
	return nil, nil
}

func cleanupCmd(t *testing.T, p agentprotocol.CleanupPayload) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-1", Kind: agentprotocol.KindDockerCleanup, Payload: raw}
}

func TestDockerCleanupCommand(t *testing.T) {
	var tuned bool
	e, err := New(Config{
		Docker: cleanableDocker{newFakeDocker()}, Proxy: &fakeProxy{}, Sender: &fakeSender{},
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
		CleanupTune: func(*cleanup.Config) { tuned = true },
	})
	require.NoError(t, err)

	// Without the control plane's protected-image set the agent refuses.
	res := e.HandleCommand(context.Background(), cleanupCmd(t, agentprotocol.CleanupPayload{RunID: "r1"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "protected-image set")

	// With it, the command runs and reports.
	res = e.HandleCommand(context.Background(), cleanupCmd(t, agentprotocol.CleanupPayload{
		RunID: "r2", Trigger: agentprotocol.CleanupTriggerScheduled, Protected: &agentprotocol.CleanupProtected{},
	}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var rep agentprotocol.CleanupResult
	require.NoError(t, json.Unmarshal(res.Data, &rep))
	require.Equal(t, "r2", rep.RunID)
	require.Equal(t, agentprotocol.CleanupModeStandard, rep.Mode)
	require.NotEmpty(t, rep.Steps)
	require.True(t, tuned, "the tuning hook is applied")

	// A malformed payload fails cleanly.
	bad := agentprotocol.Command{ID: "cmd-3", Kind: agentprotocol.KindDockerCleanup, Payload: json.RawMessage(`{`)}
	require.Equal(t, agentprotocol.OutcomeFailed, e.HandleCommand(context.Background(), bad).Outcome)
}

func TestDockerCleanupNeedsASupportingDaemon(t *testing.T) {
	r := newRig(t, nil) // the plain fake daemon lacks ImagesSize and VolumeList
	res := r.eng.HandleCommand(context.Background(), cleanupCmd(t, agentprotocol.CleanupPayload{
		RunID: "r1", Protected: &agentprotocol.CleanupProtected{},
	}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "cannot run a cleanup")
}
