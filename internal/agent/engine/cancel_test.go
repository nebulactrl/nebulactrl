package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// hookSender calls hook with every event after recording it. A hook that
// issues a cancel_deploy command lets a test cancel at exactly the moment a
// step starts, on the deploy's own goroutine.
type hookSender struct {
	*fakeSender
	hook func(agentprotocol.EventFrame)
}

func (h *hookSender) Event(e agentprotocol.EventFrame) {
	h.fakeSender.Event(e)
	if h.hook != nil {
		h.hook(e)
	}
}

func newCancelRig(t *testing.T, hf HealthFunc) (*rig, *hookSender) {
	t.Helper()
	r := &rig{t: t, dk: newFakeDocker(), px: &fakeProxy{}, snd: &fakeSender{}, bld: &fakeBuilder{}}
	hs := &hookSender{fakeSender: r.snd}
	if hf == nil {
		hf = healthy
	}
	e, err := New(Config{
		Docker: r.dk, Proxy: r.px, Builder: r.bld, Sender: hs, Health: hf,
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)
	r.eng = e
	return r, hs
}

// cancelDeployment sends the cancel_deploy command for deployment id.
func (r *rig) cancelDeployment(id, by string) agentprotocol.ResultFrame {
	r.t.Helper()
	raw, err := json.Marshal(agentprotocol.CancelDeployPayload{DeploymentID: id, RequestedBy: by})
	require.NoError(r.t, err)
	return r.eng.HandleCommand(context.Background(), agentprotocol.Command{
		ID: "cancel-" + id, Kind: agentprotocol.KindCancelDeploy, Payload: raw,
	})
}

// cancelWhen cancels the deployment once, when the given step starts.
func cancelWhen(r *rig, hs *hookSender, step string) {
	var once sync.Once
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == step && e.Status == agentprotocol.StepStarted {
			once.Do(func() { r.cancelDeployment(depV2, "alice") })
		}
	}
}

func notThere(t *testing.T, dk *fakeDocker, name string) {
	t.Helper()
	_, ok := dk.state(name)
	require.False(t, ok, "%s must not exist", name)
}

func recreatePayload() agentprotocol.DeployPayload {
	p := basePayload()
	p.Strategy = agentprotocol.StrategyRecreate
	p.Volumes = []agentprotocol.VolumeMount{{Name: "nc-vol-5e5e5e5e-data", MountPath: "/data"}}
	return p
}

// ---- rolling: the previous release keeps serving at every step ----

func TestCancelRollingAtEveryStepLeavesTheOldReleaseServing(t *testing.T) {
	steps := []struct {
		step     string
		payload  func() agentprotocol.DeployPayload
		routes   bool // the new route table was applied and must be withdrawn
		created  bool // containers of the new release existed
		stepName string
	}{
		{step: agentprotocol.StepPull, payload: basePayload},
		{step: agentprotocol.StepBuild, payload: gitPayload},
		{step: agentprotocol.StepPredeploy, payload: preDeployPayload},
		{step: agentprotocol.StepCreate, payload: basePayload},
		{step: agentprotocol.StepStart, payload: basePayload, created: true},
		{step: agentprotocol.StepHealth, payload: basePayload, created: true},
		{step: agentprotocol.StepRoute, payload: basePayload, created: true, routes: true},
		{step: agentprotocol.StepVerify, payload: basePayload, created: true, routes: true},
	}
	for _, tc := range steps {
		t.Run(tc.step, func(t *testing.T) {
			r, hs := newCancelRig(t, nil)
			prev := r.previous()
			exitWith(r, preCtr, 0)
			cancelWhen(r, hs, tc.step)

			res, dr := r.deploy(tc.payload(), map[string]string{"env:DB_URL": "x"})
			require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
			require.Equal(t, tc.step, dr.CanceledAt)
			require.Contains(t, dr.Message, "Canceled by alice during")
			require.Contains(t, dr.Message, "The previous release is still serving.")
			require.Equal(t, dr.Message, res.Error, "a canceled deploy carries its message as the error")

			require.True(t, running(t, r.dk, oldCtr), "the old release was never touched")
			require.Empty(t, r.dk.stopped, "nothing was stopped")
			notThere(t, r.dk, newCtr)
			notThere(t, r.dk, preCtr)
			require.Equal(t, prev, r.px.Current(), "the route table is the previous one")
			if tc.routes {
				require.Len(t, r.px.applies, 2, "new routes applied, then withdrawn")
				require.Equal(t, prev, r.px.applies[1])
				require.Contains(t, dr.Message, "The previous routes were restored.")
			} else {
				require.Empty(t, r.px.applies, "routes were never changed")
			}
			if tc.created {
				require.Contains(t, dr.Message, "The new containers were removed.")
				require.NotEmpty(t, r.dk.removed)
			}

			steps := r.snd.eventSteps()
			require.Contains(t, steps, "cancel:started")
			require.Contains(t, steps, "cancel:succeeded")
			require.NotContains(t, steps, "retire:started", "the old release is not retired")
			for _, s := range steps {
				require.NotContains(t, s, ":failed", "a cancel is not a step failure: %s", s)
			}
		})
	}
}

func TestCancelBeforeAnyStepNothingChanges(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	r.previous()
	// The cancel reaches the agent before the deploy command does.
	res := r.cancelDeployment(depV2, "alice")
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, "the cancel command itself succeeds")

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, "queued", dr.CanceledAt)
	require.Empty(t, r.dk.pulled, "nothing was pulled")
	require.Empty(t, r.dk.created)
	require.True(t, running(t, r.dk, oldCtr))
	require.Empty(t, r.px.applies)
}

func TestCancelForAnUnknownDeploymentIsHarmless(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	res := r.cancelDeployment("00000000-0000-4000-8000-0000000000ff", "alice")
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Contains(t, string(res.Data), "No deployment")

	bad := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: agentprotocol.KindCancelDeploy, Payload: json.RawMessage(`{}`)})
	require.Equal(t, agentprotocol.OutcomeFailed, bad.Outcome)
}

func TestCancelFirstDeploymentWithNothingToKeepServing(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	cancelWhen(r, hs, agentprotocol.StepHealth)
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "The new containers were removed.")
	require.NotContains(t, dr.Message, "still serving")
	notThere(t, r.dk, newCtr)
}

// ---- recreate: past "stop old" the previous release is started again ----

func TestCancelRecreateRestartsThePreviousRelease(t *testing.T) {
	for _, step := range []string{agentprotocol.StepCreate, agentprotocol.StepStart, agentprotocol.StepHealth, agentprotocol.StepRoute, agentprotocol.StepVerify} {
		t.Run(step, func(t *testing.T) {
			r, hs := newCancelRig(t, nil)
			prev := r.previous()
			cancelWhen(r, hs, step)

			res, dr := r.deploy(recreatePayload(), nil)
			require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
			require.Equal(t, step, dr.CanceledAt)
			require.Contains(t, dr.Message, "The previous release was started again.")
			require.Contains(t, r.dk.stopped, oldCtr, "recreate stopped the old release first")
			require.True(t, running(t, r.dk, oldCtr), "and started it again")
			notThere(t, r.dk, newCtr)
			require.Equal(t, prev, r.px.Current())
		})
	}
}

func TestCancelWhileTheOldReleaseIsStoppingRestartsIt(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	r.previous()
	// The cancel arrives while the old container is being stopped: the stop is
	// not interrupted, the old release is started again right after.
	var once sync.Once
	r.dk.onStop = func(string) {
		once.Do(func() { r.cancelDeployment(depV2, "alice") })
	}

	res, dr := r.deploy(recreatePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, "stop", dr.CanceledAt)
	require.Contains(t, dr.Message, "stopping the previous release")
	require.Contains(t, dr.Message, "The previous release was started again.")
	require.Empty(t, r.dk.created, "no container of the new release was created")
	require.True(t, running(t, r.dk, oldCtr))
}

func TestCancelRecreateThatCannotRestartTheOldReleaseIsRollbackFailed(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	r.dk.startErr = func(name string) error {
		if name == oldCtr {
			return errors.New("port is already allocated")
		}
		return nil
	}
	cancelWhen(r, hs, agentprotocol.StepHealth)

	res, dr := r.deploy(recreatePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Canceled by alice during the health check")
	require.Contains(t, dr.Message, "could not restart "+oldCtr)
	require.Contains(t, dr.Message, "Manual recovery")
	require.Equal(t, "health", dr.CanceledAt)
	require.Contains(t, r.snd.eventSteps(), "cancel:failed")
}

// ---- verify: routes first ----

func TestCancelDuringVerifyThatCannotRestoreRoutesIsRollbackFailed(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	r.px.applyErr = func(n int) error {
		if n == 2 {
			return errors.New("proxy unreachable")
		}
		return nil
	}
	cancelWhen(r, hs, agentprotocol.StepVerify)

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "could not restore previous routes: proxy unreachable")
	require.Contains(t, dr.Message, "Manual recovery")
	// Routes may still point at the new container: it must keep running.
	require.True(t, running(t, r.dk, newCtr))
}

func TestCancelBetweenVerifyTriesIsHonored(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	prev := r.previous()
	r.px.probes = []int{502}
	var once sync.Once
	// Cancel right after the first failed probe, while verification would retry.
	r.eng.cfg.Proxy = &probeHook{fakeProxy: r.px, after: func(n int) {
		if n == 1 {
			once.Do(func() { r.cancelDeployment(depV2, "alice") })
		}
	}}

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, "verify", dr.CanceledAt)
	require.Equal(t, prev, r.px.Current())
	require.True(t, running(t, r.dk, oldCtr))
	notThere(t, r.dk, newCtr)
	require.LessOrEqual(t, r.px.nProbe, 2, "no more tries after the cancel")
}

// probeHook runs after after every probe.
type probeHook struct {
	*fakeProxy
	after func(n int)
}

func (p *probeHook) Probe(ctx context.Context, host, path string) (int, error) {
	code, err := p.fakeProxy.Probe(ctx, host, path)
	p.fakeProxy.mu.Lock()
	n := p.fakeProxy.nProbe
	p.fakeProxy.mu.Unlock()
	p.after(n)
	return code, err
}

// ---- too late ----

func TestCancelAfterVerifyIsTooLateAndTheDeploySucceeds(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	var reply agentprotocol.ResultFrame
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == agentprotocol.StepRetire && e.Status == agentprotocol.StepStarted {
			reply = r.cancelDeployment(depV2, "alice")
		}
	}

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, agentprotocol.OutcomeSucceeded, reply.Outcome, "answering a too-late cancel is not a failure")
	require.Contains(t, string(reply.Data), "too late")
	require.Contains(t, r.snd.eventSteps(), "cancel:failed", "the timeline says it was too late")
	require.True(t, running(t, r.dk, newCtr), "the new release stays")
	notThere(t, r.dk, oldCtr)
}

// ---- post-deploy: the release is live; only the command stops ----

func TestCancelDuringPostDeployStopsOnlyTheCommand(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	cancelWhen(r, hs, agentprotocol.StepPostdeploy)

	res, dr := r.deploy(postDeployPayload(), map[string]string{"env:DB_URL": "x"}) // the one-shot never exits by itself
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "post-deploy command was canceled")
	require.True(t, running(t, r.dk, newCtr), "the new release is live and stays")
	notThere(t, r.dk, postCtr)
	require.Contains(t, r.snd.eventSteps(), "postdeploy:warning")
}

// ---- pre-deploy: the container goes ----

func TestCancelDuringPreDeployRemovesItsContainer(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	cancelWhen(r, hs, agentprotocol.StepPredeploy)
	// The command never exits on its own.
	res, dr := r.deploy(preDeployPayload(), map[string]string{"env:DB_URL": "x"})
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Equal(t, "predeploy", dr.CanceledAt)
	require.Contains(t, r.dk.removed, preCtr, "the pre-deploy container is removed, not left for adoption")
	notThere(t, r.dk, preCtr)
	require.True(t, running(t, r.dk, oldCtr))
	require.Len(t, r.dk.created, 1, "only the pre-deploy container was created")
}

// ---- real blocking calls honor the cancel ----

// blockingDocker blocks ImagePull until its context ends.
type blockingDocker struct {
	*fakeDocker
	pulling chan struct{}
}

func (b *blockingDocker) ImagePull(ctx context.Context, ref, auth string, progress func(string)) error {
	close(b.pulling)
	<-ctx.Done()
	return ctx.Err()
}

func TestCancelAbortsABlockedPull(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	bd := &blockingDocker{fakeDocker: r.dk, pulling: make(chan struct{})}
	r.eng.cfg.Docker = bd
	r.previous()

	done := make(chan agentprotocol.DeployResult, 1)
	go func() {
		_, dr := r.deploy(basePayload(), nil)
		done <- dr
	}()
	<-bd.pulling
	r.cancelDeployment(depV2, "alice")

	select {
	case dr := <-done:
		require.Equal(t, agentprotocol.OutcomeCanceled, dr.Outcome, dr.Message)
		require.Equal(t, "pull", dr.CanceledAt)
		require.Contains(t, dr.Message, "the image pull")
	case <-time.After(10 * time.Second):
		t.Fatal("the cancel did not abort the pull")
	}
	for _, s := range r.snd.eventSteps() {
		require.NotEqual(t, "pull:failed", s, "no failure event for a canceled pull")
	}
	require.True(t, running(t, r.dk, oldCtr))
}

// blockingBuilder blocks Build until its context ends and removes the "work dir"
// the way the real builder does.
type blockingBuilder struct {
	building chan struct{}
	cleaned  chan struct{}
}

func (b *blockingBuilder) Build(ctx context.Context, _ build.Request, _ func(string)) (string, error) {
	close(b.building)
	<-ctx.Done()
	close(b.cleaned)
	return "", ctx.Err()
}

func TestCancelKillsABlockedBuild(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	bb := &blockingBuilder{building: make(chan struct{}), cleaned: make(chan struct{})}
	r.eng.cfg.Builder = bb
	r.previous()

	done := make(chan agentprotocol.DeployResult, 1)
	go func() {
		_, dr := r.deploy(gitPayload(), nil)
		done <- dr
	}()
	<-bb.building
	r.cancelDeployment(depV2, "alice")

	select {
	case dr := <-done:
		require.Equal(t, agentprotocol.OutcomeCanceled, dr.Outcome, dr.Message)
		require.Equal(t, "build", dr.CanceledAt)
		require.Contains(t, dr.Message, "the build")
	case <-time.After(10 * time.Second):
		t.Fatal("the cancel did not abort the build")
	}
	select {
	case <-bb.cleaned:
	default:
		t.Fatal("the builder was not told to stop")
	}
	for _, s := range r.snd.eventSteps() {
		require.NotEqual(t, "build:failed", s)
	}
	require.True(t, running(t, r.dk, oldCtr))
	require.Empty(t, r.dk.created)
}

func TestCancelAbortsABlockedHealthCheck(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	blocking := func(ctx context.Context, _ health.Target) health.Result {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return health.Result{Reason: ctx.Err().Error()}
	}
	r, _ := newCancelRig(t, blocking)
	r.previous()

	done := make(chan agentprotocol.DeployResult, 1)
	go func() {
		_, dr := r.deploy(basePayload(), nil)
		done <- dr
	}()
	<-entered
	r.cancelDeployment(depV2, "alice")

	select {
	case dr := <-done:
		require.Equal(t, agentprotocol.OutcomeCanceled, dr.Outcome, dr.Message)
		require.Equal(t, "health", dr.CanceledAt)
	case <-time.After(10 * time.Second):
		t.Fatal("the cancel did not abort the health check")
	}
	notThere(t, r.dk, newCtr)
	require.True(t, running(t, r.dk, oldCtr))
	for _, s := range r.snd.eventSteps() {
		require.NotEqual(t, "health:failed", s)
	}
}

// A create call the cancel interrupted may still have made the container.
type lateCreateDocker struct {
	*fakeDocker
	creating chan struct{}
	once     sync.Once
}

func (l *lateCreateDocker) ContainerCreate(ctx context.Context, spec docker.ContainerSpec) (string, error) {
	if spec.Name != newCtr {
		return l.fakeDocker.ContainerCreate(ctx, spec)
	}
	l.once.Do(func() { close(l.creating) })
	<-ctx.Done()
	// The daemon finished creating it even though the request was abandoned.
	if _, err := l.fakeDocker.ContainerCreate(context.WithoutCancel(ctx), spec); err != nil {
		return "", err
	}
	return "", ctx.Err()
}

func TestCancelRemovesAContainerWhoseCreateWasInterrupted(t *testing.T) {
	r, _ := newCancelRig(t, nil)
	ld := &lateCreateDocker{fakeDocker: r.dk, creating: make(chan struct{})}
	r.eng.cfg.Docker = ld
	r.previous()

	done := make(chan agentprotocol.DeployResult, 1)
	go func() {
		_, dr := r.deploy(basePayload(), nil)
		done <- dr
	}()
	<-ld.creating
	r.cancelDeployment(depV2, "alice")

	select {
	case dr := <-done:
		require.Equal(t, agentprotocol.OutcomeCanceled, dr.Outcome, dr.Message)
		require.Equal(t, "create", dr.CanceledAt)
	case <-time.After(10 * time.Second):
		t.Fatal("the cancel did not abort the create")
	}
	notThere(t, r.dk, newCtr)
	require.True(t, running(t, r.dk, oldCtr))
}

// ---- cancel is not agent shutdown ----

func TestAgentShutdownIsNotACancel(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	ctx, stop := context.WithCancel(context.Background())
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == agentprotocol.StepHealth && e.Status == agentprotocol.StepStarted {
			stop() // the agent is shutting down
		}
	}
	raw, err := json.Marshal(basePayload())
	require.NoError(t, err)
	res := r.eng.HandleCommand(ctx, agentprotocol.Command{ID: "cmd-1", Kind: agentprotocol.KindDeploy, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "agent stopped during deployment")
	require.NotContains(t, res.Error, "Canceled")
}

func TestCancelTwiceAndAfterFinishAreHarmless(t *testing.T) {
	r, hs := newCancelRig(t, nil)
	r.previous()
	hs.hook = func(e agentprotocol.EventFrame) {
		if e.Step == agentprotocol.StepHealth && e.Status == agentprotocol.StepStarted {
			require.Equal(t, agentprotocol.OutcomeSucceeded, r.cancelDeployment(depV2, "alice").Outcome)
			require.Equal(t, agentprotocol.OutcomeSucceeded, r.cancelDeployment(depV2, "bob").Outcome)
		}
	}
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "Canceled by alice", "the first request wins")

	// The deploy is over: a late cancel finds nothing to stop.
	late := r.cancelDeployment(depV2, "carol")
	require.Equal(t, agentprotocol.OutcomeSucceeded, late.Outcome)
}
