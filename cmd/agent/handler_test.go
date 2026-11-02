package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/internal/agent/selfupgrade"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// hostDocker is a Docker backend for an engine that only ever decommissions:
// the embedded nil docker.Engine panics on any call the decommission must not
// make, and the eight decommission.Docker methods are served from the lists.
type hostDocker struct {
	docker.Engine
	mu         sync.Mutex
	containers []docker.ContainerInfo
	removeErr  error
	removed    []string
}

func (d *hostDocker) ContainerList(context.Context, docker.ListFilter) ([]docker.ContainerInfo, error) {
	return d.containers, nil
}
func (d *hostDocker) ContainerStop(context.Context, string, *time.Duration) error { return nil }
func (d *hostDocker) ContainerRemove(_ context.Context, id string, _, _ bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.removeErr != nil {
		return d.removeErr
	}
	d.removed = append(d.removed, id)
	return nil
}
func (d *hostDocker) NetworkList(context.Context, string, map[string]string) ([]docker.NetworkInfo, error) {
	return nil, nil
}
func (d *hostDocker) NetworkDisconnect(context.Context, string, string) error { return nil }
func (d *hostDocker) NetworkRemove(context.Context, string) error             { return nil }
func (d *hostDocker) VolumeList(context.Context) ([]docker.VolumeInfo, error) { return nil, nil }
func (d *hostDocker) RemoveVolume(context.Context, string, bool) error        { return nil }

type nopProxy struct{}

func (nopProxy) Apply([]agentprotocol.Route) error                  { return nil }
func (nopProxy) Current() []agentprotocol.Route                     { return nil }
func (nopProxy) Probe(context.Context, string, string) (int, error) { return 0, nil }
func (nopProxy) SetACMEEmail(string)                                {}

type nopSender struct{}

func (nopSender) Event(agentprotocol.EventFrame)  {}
func (nopSender) Log(agentprotocol.LogFrame)      {}
func (nopSender) LogEnd(agentprotocol.LogEnd)     {}
func (nopSender) Stats(agentprotocol.Stats)       {}
func (nopSender) Observed(agentprotocol.Observed) {}

func newSelfHandlerWithEngine(t *testing.T, d *hostDocker) (*selfHandler, *restartRequest, *atomic.Int32) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.New(engine.Config{Docker: d, Proxy: nopProxy{}, Sender: nopSender{}, Logger: log})
	require.NoError(t, err)
	dec := &restartRequest{cancel: func() {}}
	var restarted atomic.Int32
	return &selfHandler{
		Engine:         eng,
		decommissioned: dec,
		inflight:       func() int { return 0 },
		restart:        func() { restarted.Add(1) },
		log:            log,
		flush:          time.Millisecond,
	}, dec, &restarted
}

func decommissionCmd(t *testing.T) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.DecommissionPayload{ServerID: "srv-1"})
	require.NoError(t, err)
	return agentprotocol.Command{ID: "d1", Kind: agentprotocol.KindDecommission, Payload: raw}
}

func TestHandleCommandDecommissionSuccessMarksTheRun(t *testing.T) {
	d := &hostDocker{containers: []docker.ContainerInfo{{ID: "c1", Name: "web", Labels: map[string]string{"nebulactrl.managed": "true"}}}}
	h, dec, _ := newSelfHandlerWithEngine(t, d)

	res := h.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	assert.Equal(t, "d1", res.CommandID)
	assert.Equal(t, []string{"c1"}, d.removed)
	assert.True(t, dec.requested.Load(), "a successful decommission is marked before the result leaves")

	var data agentprotocol.DecommissionResult
	require.NoError(t, json.Unmarshal(res.Data, &data))
	assert.Equal(t, 1, data.Containers)
}

func TestHandleCommandDecommissionFailureIsNotMarked(t *testing.T) {
	d := &hostDocker{
		containers: []docker.ContainerInfo{{ID: "c1", Name: "web", Labels: map[string]string{"nebulactrl.managed": "true"}}},
		removeErr:  errors.New("device busy"),
	}
	h, dec, _ := newSelfHandlerWithEngine(t, d)

	res := h.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Contains(t, res.Error, "device busy")
	assert.False(t, dec.requested.Load(), "a failed decommission keeps the agent")
}

func TestHandleCommandDecommissionBadPayloadIsNotMarked(t *testing.T) {
	h, dec, _ := newSelfHandlerWithEngine(t, &hostDocker{})
	res := h.HandleCommand(context.Background(), agentprotocol.Command{ID: "d2", Kind: agentprotocol.KindDecommission, Payload: json.RawMessage(`{`)})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Contains(t, res.Error, "invalid decommission payload")
	assert.False(t, dec.requested.Load())
}

func TestHandleCommandDecommissionWithoutRequestGoesToTheEngine(t *testing.T) {
	d := &hostDocker{containers: []docker.ContainerInfo{{ID: "c1", Name: "web", Labels: map[string]string{"nebulactrl.managed": "true"}}}}
	h, _, _ := newSelfHandlerWithEngine(t, d)
	h.decommissioned = nil // an agent that cannot remove itself
	res := h.HandleCommand(context.Background(), decommissionCmd(t))
	// The engine still empties the host; nothing is marked because there is nothing to mark.
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	assert.Equal(t, []string{"c1"}, d.removed)
}

func TestHandleCommandOtherKindsAreTheEngines(t *testing.T) {
	h, dec, _ := newSelfHandlerWithEngine(t, &hostDocker{})
	res := h.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: "no_such_kind"})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Equal(t, "unsupported command kind: no_such_kind", res.Error)
	assert.False(t, dec.requested.Load())
}

func TestHandleCommandRefusesWorkAfterDecommission(t *testing.T) {
	h, _, _ := newSelfHandlerWithEngine(t, &hostDocker{})
	require.Equal(t, agentprotocol.OutcomeSucceeded, h.HandleCommand(context.Background(), decommissionCmd(t)).Outcome)

	res := h.HandleCommand(context.Background(), agentprotocol.Command{ID: "late", Kind: agentprotocol.KindDeploy})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Equal(t, "this server is being removed from NebulaCtrl", res.Error)
}

func TestHandleCommandUpgradeWithoutControllerFallsThroughToTheEngine(t *testing.T) {
	h, _, _ := newSelfHandlerWithEngine(t, &hostDocker{})
	h.up = nil
	res := h.HandleCommand(context.Background(), agentprotocol.Command{ID: "u", Kind: agentprotocol.KindAgentUpgrade, Payload: json.RawMessage(`{}`)})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.Contains(t, res.Error, "unsupported command kind")
}

func TestHandleCommandUpgradeIsHandledByTheController(t *testing.T) {
	h, _, _ := newSelfHandlerWithEngine(t, &hostDocker{})
	h.up = &selfupgrade.Controller{Exe: "", StateDir: t.TempDir(), Version: "1.0.0", GOOS: "linux", GOARCH: "amd64"}
	// The controller (not the engine) answers, and it rejects a payload without a digest.
	res := h.HandleCommand(context.Background(), upgradeCmd(t, agentprotocol.UpgradePayload{Version: "1.1.0"}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	assert.NotContains(t, res.Error, "unsupported command kind")
	assert.NotEmpty(t, res.Error)
}

func TestResultSentDecommissionSuccessEndsTheRun(t *testing.T) {
	h, dec, restarted := newSelfHandlerWithEngine(t, &hostDocker{})
	ended := make(chan struct{})
	dec.cancel = func() { close(ended) }

	h.ResultSent(context.Background(), decommissionCmd(t), agentprotocol.ResultFrame{CommandID: "d1", Outcome: agentprotocol.OutcomeSucceeded})
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a delivered successful decommission result never ended the run")
	}
	assert.True(t, dec.requested.Load())
	assert.Zero(t, restarted.Load(), "a decommission is not a restart")
}

func TestResultSentDecommissionFailureRestartsTheAgent(t *testing.T) {
	h, dec, restarted := newSelfHandlerWithEngine(t, &hostDocker{})
	var ended atomic.Bool
	dec.cancel = func() { ended.Store(true) }

	h.ResultSent(context.Background(), decommissionCmd(t), agentprotocol.ResultFrame{CommandID: "d1", Outcome: agentprotocol.OutcomeFailed})
	require.Eventually(t, func() bool { return restarted.Load() == 1 }, 5*time.Second, time.Millisecond)
	assert.False(t, ended.Load())
	assert.False(t, dec.requested.Load())
}

func TestResultSentDecommissionCancelledBeforeFlushDoesNothing(t *testing.T) {
	h, dec, restarted := newSelfHandlerWithEngine(t, &hostDocker{})
	h.flush = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	var ended atomic.Bool
	dec.cancel = func() { ended.Store(true) }

	h.ResultSent(ctx, decommissionCmd(t), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded})
	cancel()
	// Give the goroutine a chance to (wrongly) act; it must observe the cancelled context and stop.
	time.Sleep(20 * time.Millisecond)
	assert.False(t, ended.Load())
	assert.Zero(t, restarted.Load())
}

func TestResultSentOtherCommandsGoToTheEngine(t *testing.T) {
	h, dec, restarted := newSelfHandlerWithEngine(t, &hostDocker{})
	// A succeeded non-deploy command: the engine's observer does nothing, and neither restart nor decommission fires.
	h.ResultSent(context.Background(), agentprotocol.Command{ID: "s", Kind: agentprotocol.KindStopService}, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded})
	assert.False(t, dec.requested.Load())
	assert.Zero(t, restarted.Load())
}

func TestResultSentSuccessfulUpgradeRestartsWhenIdle(t *testing.T) {
	var restarted atomic.Int32
	h := newUpgradeHandler(t, func() int { return 0 }, &restarted)
	h.ResultSent(context.Background(), upgradeCmd(t, nil), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded})
	require.Eventually(t, func() bool { return restarted.Load() == 1 }, 5*time.Second, time.Millisecond)
}
