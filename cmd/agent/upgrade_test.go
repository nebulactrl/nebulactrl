package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/selfupgrade"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func newUpgradeHandler(t *testing.T, inflight func() int, restarted *atomic.Int32) *selfHandler {
	t.Helper()
	dir := t.TempDir()
	return &selfHandler{
		up: &selfupgrade.Controller{
			Exe: filepath.Join(dir, "nebulactrl-agent"), StateDir: dir, Version: "1.4.0", GOOS: "linux", GOARCH: "amd64",
			BaseURL: "http://127.0.0.1:1", // nothing listens: a download would fail
		},
		inflight: inflight,
		restart:  func() { restarted.Add(1) },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		idlePoll: 5 * time.Millisecond, flush: 5 * time.Millisecond,
	}
}

func upgradeCmd(t *testing.T, p any) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "c1", Kind: agentprotocol.KindAgentUpgrade, Payload: raw}
}

func TestUpgradeCommandFailuresAreFailedResultsNotRestarts(t *testing.T) {
	var restarted atomic.Int32
	h := newUpgradeHandler(t, func() int { return 0 }, &restarted)

	res := h.HandleCommand(context.Background(), agentprotocol.Command{ID: "c0", Kind: agentprotocol.KindAgentUpgrade, Payload: json.RawMessage(`{`)})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "invalid agent_upgrade payload")

	// Nothing to download from: the upgrade fails and the agent stays as it is.
	res = h.HandleCommand(context.Background(), upgradeCmd(t, agentprotocol.UpgradePayload{Version: "1.5.0", SHA256: "7d4f1c9a0b3e2f58a6c1d9e4b7f20a35c8d6e1f4a9b2c3d4e5f60718293a4b5c"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, "c1", res.CommandID)
	require.NotEmpty(t, res.Error)

	h.ResultSent(context.Background(), upgradeCmd(t, nil), res)
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, restarted.Load(), "a failed upgrade never restarts the agent")
}

func TestRestartWaitsForRunningCommands(t *testing.T) {
	var restarted atomic.Int32
	var busy atomic.Int32
	busy.Store(2)
	h := newUpgradeHandler(t, func() int { return int(busy.Load()) }, &restarted)

	h.ResultSent(context.Background(), upgradeCmd(t, nil), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded})
	time.Sleep(80 * time.Millisecond)
	require.Zero(t, restarted.Load(), "a build or deployment is still running")

	busy.Store(0)
	require.Eventually(t, func() bool { return restarted.Load() == 1 }, time.Second, 5*time.Millisecond)
}

func TestRestartIsCancelledWithTheAgent(t *testing.T) {
	var restarted atomic.Int32
	h := newUpgradeHandler(t, func() int { return 1 }, &restarted)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.restartWhenIdle(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("restartWhenIdle ignores a cancelled context")
	}
	require.Zero(t, restarted.Load())
}

func TestRestartRequestCancelsTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &restartRequest{cancel: cancel}
	require.False(t, r.requested.Load())
	r.Do()
	require.True(t, r.requested.Load())
	require.Error(t, ctx.Err())
}
