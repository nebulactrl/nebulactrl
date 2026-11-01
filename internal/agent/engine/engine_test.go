package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	svcID  = "5e5e5e5e-0000-4000-8000-000000000001"
	envID  = "e0e0e0e0-0000-4000-8000-000000000002"
	depV1  = "d1d1d1d1-0000-4000-8000-000000000001"
	depV2  = "d2d2d2d2-0000-4000-8000-000000000002"
	oldCtr = "nc-web-d1d1d1d1-0"
	newCtr = "nc-web-d2d2d2d2-0"
)

type rig struct {
	t   *testing.T
	dk  *fakeDocker
	px  *fakeProxy
	snd *fakeSender
	bld *fakeBuilder
	eng *Engine
}

func healthy(context.Context, health.Target) health.Result { return health.Result{Healthy: true} }

// hermeticPool is a network allocator that ignores the test machine's own
// interfaces, so subnet choices do not depend on where the tests run (an
// OrbStack or Docker Desktop host routes some 10.213.x.0/24 networks itself).
func hermeticPool(t *testing.T, pool string, prefixLen int) *netpool.Allocator {
	t.Helper()
	a, err := netpool.New(pool, prefixLen)
	require.NoError(t, err)
	return a.WithHostPrefixes(func() []netip.Prefix { return nil })
}

func newRig(t *testing.T, hf HealthFunc) *rig {
	t.Helper()
	r := &rig{t: t, dk: newFakeDocker(), px: &fakeProxy{}, snd: &fakeSender{}, bld: &fakeBuilder{}}
	if hf == nil {
		hf = healthy
	}
	e, err := New(Config{
		Docker: r.dk, Proxy: r.px, Builder: r.bld, Sender: r.snd, Health: hf,
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)
	r.eng = e
	return r
}

func basePayload() agentprotocol.DeployPayload {
	return agentprotocol.DeployPayload{
		DeploymentID: depV2, ReleaseID: "rel-2", ServiceID: svcID, ServiceSlug: "web", EnvironmentID: envID,
		Strategy: agentprotocol.StrategyRolling,
		Image:    &agentprotocol.ImageSource{Ref: "traefik/whoami:latest"},
		Port:     80, StopGraceSeconds: 5, ImageRetention: 5,
		Healthcheck: agentprotocol.HealthcheckSpec{Type: "http", Path: "/", DeadlineSeconds: 5},
		Hosts:       []string{"whoami.localhost"},
		Routes:      []agentprotocol.Route{{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}}},
	}
}

func (r *rig) deploy(p agentprotocol.DeployPayload, secrets map[string]string) (agentprotocol.ResultFrame, agentprotocol.DeployResult) {
	r.t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(r.t, err)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{
		ID: "cmd-1", Kind: agentprotocol.KindDeploy, Payload: raw, Secrets: secrets,
	})
	var dr agentprotocol.DeployResult
	require.NoError(r.t, json.Unmarshal(res.Data, &dr))
	require.Equal(r.t, res.Outcome, dr.Outcome)
	return res, dr
}

// previous seeds a running v1 container plus the route table that points at it.
func (r *rig) previous() []agentprotocol.Route {
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	routes := []agentprotocol.Route{
		{ServiceID: "other", Hosts: []string{"other.localhost"}, Upstreams: []string{"nc-other-aaaaaaaa-0:80"}},
		{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{oldCtr + ":80"}, HealthPath: "/"},
	}
	require.NoError(r.t, r.px.Apply(routes))
	r.px.applies = nil
	return routes
}

func running(t *testing.T, dk *fakeDocker, name string) bool {
	t.Helper()
	c, ok := dk.state(name)
	return ok && c.Running
}

func TestRollingDeploySucceedsAndRetiresOldRelease(t *testing.T) {
	r := newRig(t, nil)
	r.previous()

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.True(t, running(t, r.dk, newCtr))
	_, oldExists := r.dk.state(oldCtr)
	require.False(t, oldExists, "old container retired")
	require.Equal(t, "traefik/whoami@sha256:abc123", dr.ImageDigest)
	require.NotEmpty(t, dr.ContainerID)

	require.Len(t, r.px.applies, 1)
	table := r.px.applies[0]
	require.Len(t, table, 2)
	require.Equal(t, agentprotocol.Route{
		ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{newCtr + ":80"}, HealthPath: "/",
		Networks: []string{agentprotocol.EnvNetwork(envID)},
	}, table[1])

	// The old container is stopped before removal and the new one carries the labels.
	require.Contains(t, r.dk.stopped, oldCtr)
	spec := r.dk.created[0]
	require.Equal(t, svcID, spec.Labels[agentprotocol.LabelService])
	require.Equal(t, depV2, spec.Labels[agentprotocol.LabelDeployment])
	require.Equal(t, "unless-stopped", spec.RestartPolicy)
	require.Len(t, spec.Networks, 1, "the app joins its environment network only")
	require.Equal(t, agentprotocol.EnvNetwork(envID), spec.Networks[0].Network)
	require.Equal(t, []string{"web"}, spec.Networks[0].Aliases)

	steps := r.snd.eventSteps()
	for _, want := range []string{"pull:started", "pull:succeeded", "create:succeeded", "start:succeeded", "health:succeeded", "route:succeeded", "verify:succeeded", "retire:succeeded"} {
		require.Contains(t, steps, want)
	}
	for _, f := range r.snd.logs {
		require.Equal(t, depV2, f.DeploymentID)
		require.Equal(t, svcID, f.ServiceID)
	}
}

func TestHealthFailureRemovesNewContainerAndLeavesOldServing(t *testing.T) {
	r := newRig(t, health.Wait) // real check: the container exits right after start
	r.previous()
	r.dk.logText[newCtr] = "boot\nfatal: cannot bind port\n"
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if c.Name == newCtr {
			c.State, c.Running, c.ExitCode = "exited", false, 1
		}
	}

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "Previous release is still serving")
	require.Contains(t, dr.Message, "exited with code 1")
	require.Contains(t, dr.Message, "fatal: cannot bind port")
	require.Equal(t, dr.Message, res.Error)

	_, newExists := r.dk.state(newCtr)
	require.False(t, newExists, "new container removed")
	require.True(t, running(t, r.dk, oldCtr), "old container untouched")
	require.Empty(t, r.dk.stopped)
	require.Empty(t, r.px.applies, "routes never changed")
}

func TestProxyRejectLeavesOldReleaseServing(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	r.px.applyErr = func(int) error { return errors.New("invalid config") }

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "invalid config")
	require.Contains(t, dr.Message, "Previous release is still serving")

	_, newExists := r.dk.state(newCtr)
	require.False(t, newExists)
	require.True(t, running(t, r.dk, oldCtr))
	require.Empty(t, r.dk.stopped)
}

func TestVerifyFailureRestoresPreviousRoutesAndRollsBack(t *testing.T) {
	r := newRig(t, nil)
	prev := r.previous()
	r.px.probes = []int{502}

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Equal(t, 3, r.px.nProbe, "three verify tries")

	require.Len(t, r.px.applies, 2)
	require.Equal(t, []string{newCtr + ":80"}, r.px.applies[0][1].Upstreams)
	require.Equal(t, prev, r.px.applies[1], "previous routes restored")
	require.Equal(t, prev, r.px.Current())

	_, newExists := r.dk.state(newCtr)
	require.False(t, newExists)
	require.True(t, running(t, r.dk, oldCtr))
}

func TestFailedRouteRestoreReportsRollbackFailed(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	r.px.probes = []int{503}
	r.px.applyErr = func(n int) error {
		if n == 2 {
			return errors.New("proxy unreachable")
		}
		return nil
	}

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome)
	require.Contains(t, dr.Message, "proxy unreachable")
	require.Contains(t, dr.Message, "Manual recovery")
	// Routes may point at the new container: it must keep running.
	require.True(t, running(t, r.dk, newCtr))
}

func TestRecreateFailureRestartsOldContainer(t *testing.T) {
	unhealthy := func(context.Context, health.Target) health.Result {
		return health.Result{Reason: "health check did not pass within 5s"}
	}
	r := newRig(t, unhealthy)
	r.previous()
	p := basePayload()
	p.Strategy = agentprotocol.StrategyRecreate
	p.Volumes = []agentprotocol.VolumeMount{{Name: "nc-vol-5e5e5e5e-data", MountPath: "/data"}}

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeRolledBack, res.Outcome, dr.Message)
	require.Contains(t, r.dk.stopped, oldCtr, "old stopped first")
	require.Contains(t, r.dk.started, oldCtr, "old started again")
	require.True(t, running(t, r.dk, oldCtr))
	_, newExists := r.dk.state(newCtr)
	require.False(t, newExists)
	require.Equal(t, "/data", r.dk.created[0].Mounts[0].Target)
}

func TestRecreateRestoreFailureIsRollbackFailed(t *testing.T) {
	unhealthy := func(context.Context, health.Target) health.Result { return health.Result{Reason: "nope"} }
	r := newRig(t, unhealthy)
	r.previous()
	r.dk.startErr = func(name string) error {
		if name == oldCtr {
			return errors.New("port is already allocated")
		}
		return nil
	}
	p := basePayload()
	p.Strategy = agentprotocol.StrategyRecreate

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeRollbackFailed, res.Outcome, dr.Message)
	require.Contains(t, dr.Message, "could not restart "+oldCtr)
	require.Contains(t, dr.Message, "Manual recovery")
}

func TestRecreateSuccessRemovesOldContainer(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	p := basePayload()
	p.Strategy = agentprotocol.StrategyRecreate

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	_, oldExists := r.dk.state(oldCtr)
	require.False(t, oldExists)
	require.True(t, running(t, r.dk, newCtr))
}

func TestRollingFallsBackToRecreateWithoutPort(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	p := basePayload()
	p.Port, p.Hosts = 0, nil
	res, _ := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	// Recreate stops the old container before the new one is created.
	require.Equal(t, oldCtr, r.dk.stopped[0])
}

func TestFirstDeploymentFailureHasNothingToRollBackTo(t *testing.T) {
	r := newRig(t, nil)
	r.px.probes = []int{500}
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, dr.Message)
	_, newExists := r.dk.state(newCtr)
	require.False(t, newExists)
}

func TestPathTraversalInDockerfilePathIsRejected(t *testing.T) {
	for _, bad := range []string{"../Dockerfile", "a/../../x", "/etc/passwd"} {
		r := newRig(t, nil)
		p := basePayload()
		p.Image = nil
		p.Git = &agentprotocol.GitSource{
			URL: "https://example.com/r.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
			DockerfilePath: bad, BuildContext: ".",
		}
		res, dr := r.deploy(p, nil)
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, bad)
		require.Contains(t, dr.Message, "dockerfilePath", bad)
		require.Empty(t, r.bld.calls, "builder must not run for %q", bad)
		require.Empty(t, r.dk.created)
	}
	r := newRig(t, nil)
	p := basePayload()
	p.Image = nil
	p.Git = &agentprotocol.GitSource{
		URL: "https://example.com/r.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		DockerfilePath: "Dockerfile", BuildContext: "../outside",
	}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "buildContext")
}

func TestGitBuildStreamsBuildLogsAndReportsImageID(t *testing.T) {
	r := newRig(t, nil)
	r.bld.out = []string{"#1 building", "#2 done"}
	r.bld.id = "sha256:feedbeef"
	p := basePayload()
	p.Image = nil
	p.Git = &agentprotocol.GitSource{
		URL: "https://example.com/r.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		DockerfilePath: "Dockerfile", BuildContext: ".", HasToken: true,
	}
	res, dr := r.deploy(p, map[string]string{agentprotocol.SecretGitToken: "ghp_supersecrettoken"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, "sha256:feedbeef", dr.ImageDigest)
	require.Len(t, r.bld.calls, 1)
	require.Equal(t, "ghp_supersecrettoken", r.bld.calls[0].Token)

	var sawBuild bool
	for _, f := range r.snd.logs {
		if f.Stream == agentprotocol.StreamBuild {
			sawBuild = true
			require.Equal(t, depV2, f.DeploymentID)
		}
	}
	require.True(t, sawBuild)
	require.Contains(t, r.snd.allLogText(), "#2 done")
	require.NotContains(t, r.snd.allLogText(), "ghp_supersecrettoken")
}

func TestExistingImageIDSkipsBuildAndMissingImageFails(t *testing.T) {
	r := newRig(t, nil)
	r.dk.images["nc-build/web:old"] = docker.ImageInfo{ID: "sha256:oldimg"}
	p := basePayload()
	p.Image = nil
	p.Git = &agentprotocol.GitSource{URL: "u", CommitSHA: "0123456789abcdef", ExistingImageID: "sha256:oldimg"}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, r.bld.calls)

	p.Git.ExistingImageID = "sha256:gone"
	p.DeploymentID = "d3d3d3d3-0000-4000-8000-000000000003"
	res, dr = r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "rebuild the commit")
}

func TestSecretsReachContainerButNeverLogsOrResults(t *testing.T) {
	const secret = "sekret-canary-12345"
	r := newRig(t, nil)
	r.bld.out = []string{"echo " + secret}
	p := basePayload()
	p.EnvKeys = []string{"API_KEY", "NEBULA_SERVICE_NAME", "PORT"}
	p.SecretEnvKeys = []string{"API_KEY"}
	secrets := map[string]string{"env:API_KEY": secret, "env:NEBULA_SERVICE_NAME": "webapp-visible", "env:PORT": "80"}

	res, dr := r.deploy(p, secrets)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{"API_KEY=" + secret, "NEBULA_SERVICE_NAME=webapp-visible", "PORT=80", "NEBULA_REPLICA_INDEX=0", "NEBULA_REPLICA_COUNT=1"}, r.dk.created[0].Env)

	// A failing health check dumps container logs into the message: still masked.
	r2 := newRig(t, func(context.Context, health.Target) health.Result { return health.Result{Reason: "boom"} })
	r2.previous()
	r2.dk.logText[newCtr] = "connecting with " + secret + "\n"
	res, dr = r2.deploy(p, secrets)
	require.NotEqual(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	raw, _ := json.Marshal(res)
	require.NotContains(t, string(raw), secret)
	require.NotContains(t, r2.snd.allLogText(), secret)
	require.Contains(t, dr.Message, redactMark)
	for _, e := range r2.snd.events {
		require.NotContains(t, e.Message, secret)
	}
}

func TestMissingEnvSecretFailsBeforeAnyChange(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	p := basePayload()
	p.EnvKeys = []string{"DATABASE_URL"}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "DATABASE_URL")
	require.Empty(t, r.dk.pulled)
	require.True(t, running(t, r.dk, oldCtr))
}

func TestBadImagePullFailsWithPullError(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	p := basePayload()
	p.Image.Ref = "nonexistent/nope:1"
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "pull access denied")
	require.True(t, running(t, r.dk, oldCtr))
	require.Empty(t, r.dk.created)
}

func TestRollbackPullsPinnedDigestOnlyWhenMissing(t *testing.T) {
	r := newRig(t, nil)
	p := basePayload()
	p.Image = &agentprotocol.ImageSource{Ref: "traefik/whoami:latest", Digest: "traefik/whoami@sha256:abc123"}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{"traefik/whoami@sha256:abc123"}, r.dk.pulled)
	require.Equal(t, "traefik/whoami@sha256:abc123", dr.ImageDigest)

	// Second rollback finds it locally.
	p.DeploymentID = "d3d3d3d3-0000-4000-8000-000000000003"
	r.deploy(p, nil)
	require.Len(t, r.dk.pulled, 1)
}

func TestRedeliveredDeployContinuesRunningContainer(t *testing.T) {
	r := newRig(t, nil)
	// Pre-pull so the image ID matches what the seeded container was created from.
	require.NoError(t, r.dk.ImagePull(context.Background(), "traefik/whoami:latest", "", nil))
	c := r.dk.seed(newCtr, svcID, depV2, true, time.Minute)
	c.ImageID = "sha256:img-traefik/whoami:latest"

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, r.dk.created, "no duplicate container")
	require.Equal(t, c.ID, dr.ContainerID)
}

func TestRestartStopAndRemove(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	payload, _ := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID, StopGraceSeconds: 3})

	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "a", Kind: agentprotocol.KindRestartService, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{oldCtr}, r.dk.stopped)
	require.Equal(t, []string{oldCtr}, r.dk.started)

	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "b", Kind: agentprotocol.KindStopService, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.False(t, running(t, r.dk, oldCtr))

	rp, _ := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID, DeleteVolumes: true, VolumeNames: []string{"nc-vol-5e5e5e5e-data", "not-ours"}})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "c", Kind: agentprotocol.KindRemoveService, Payload: rp})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, "non-managed volume refused")
	_, exists := r.dk.state(oldCtr)
	require.False(t, exists)
	require.Len(t, r.px.Current(), 1, "service route dropped, other kept")
	require.Equal(t, "other", r.px.Current()[0].ServiceID)
}

// A volume the daemon still holds is reported as an incomplete removal (the
// control plane keeps the replica and sends the command again), and the same
// command sent again finishes the job: nothing is left half-removed for good.
func TestRemoveServiceReportsAVolumeThatStaysAndFinishesWhenSentAgain(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	vol := agentprotocol.VolumeName(svcID, "data")
	rp, _ := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID, DeleteVolumes: true, VolumeNames: []string{vol}})
	r.dk.volInUse = vol

	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rm-1", Kind: agentprotocol.KindRemoveService, Payload: rp})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "remove incomplete")
	require.Contains(t, res.Error, vol)
	require.NotContains(t, r.dk.volumesGone(), vol, "the volume is still there")
	_, exists := r.dk.state(oldCtr)
	require.False(t, exists, "the container is gone either way")

	r.dk.mu.Lock()
	r.dk.volInUse = "" // the daemon let go of it
	r.dk.mu.Unlock()
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rm-2", Kind: agentprotocol.KindRemoveService, Payload: rp})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Contains(t, r.dk.volumesGone(), vol)
}

func TestSyncRoutesAppliesTable(t *testing.T) {
	r := newRig(t, nil)
	payload, _ := json.Marshal(agentprotocol.SyncRoutesPayload{
		Routes: []agentprotocol.Route{{ServiceID: "s", Hosts: []string{"a.localhost"}, Upstreams: []string{"x:80"}}}, ACMEEmail: "ops@example.com",
	})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "1", Kind: agentprotocol.KindSyncRoutes, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, "ops@example.com", r.px.email)
	require.Len(t, r.px.Current(), 1)
}

func TestLogsSubscribeStreamsRuntimeLinesThenEnds(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	r.dk.logText[oldCtr] = "hello\nworld\n"
	payload, _ := json.Marshal(agentprotocol.LogsSubscribe{StreamID: "st-1", ServiceID: svcID, Tail: 10})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "l", Kind: agentprotocol.KindLogsSubscribe, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	require.Eventually(t, func() bool {
		r.snd.mu.Lock()
		defer r.snd.mu.Unlock()
		return len(r.snd.logEnds) == 1
	}, 3*time.Second, 10*time.Millisecond)
	r.snd.mu.Lock()
	require.Equal(t, "st-1", r.snd.logEnds[0].StreamID)
	var lines []string
	for _, f := range r.snd.logs {
		require.Equal(t, "st-1", f.StreamID)
		require.Equal(t, svcID, f.ServiceID)
		require.Equal(t, agentprotocol.StreamRuntime, f.Stream)
		for _, l := range f.Lines {
			lines = append(lines, l.Text)
		}
	}
	r.snd.mu.Unlock()
	require.Equal(t, []string{"hello", "world"}, lines)

	missing, _ := json.Marshal(agentprotocol.LogsSubscribe{StreamID: "st-2", ServiceID: "nope"})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "m", Kind: agentprotocol.KindLogsSubscribe, Payload: missing})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
}

func TestLogsSubscribeDefaultProcessMatchesWhatTheContainerIsCalled(t *testing.T) {
	subscribe := func(r *rig, stream, process string, isDefault bool) agentprotocol.ResultFrame {
		raw, err := json.Marshal(agentprotocol.LogsSubscribe{StreamID: stream, ServiceID: svcID, Tail: 10, Process: process, Default: isDefault})
		require.NoError(t, err)
		return r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "log-" + stream, Kind: agentprotocol.KindLogsSubscribe, Payload: raw})
	}

	// A single-process release has unlabeled containers: the default process
	// is one of them, whatever the staged config names it.
	r := newRig(t, nil)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	r.dk.logText[oldCtr] = "hello"
	require.Equal(t, agentprotocol.OutcomeSucceeded, subscribe(r, "d", "web", true).Outcome)
	require.Eventually(t, func() bool { return r.streamEnded("d") }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"hello"}, r.streamLines("d"))
	require.Equal(t, agentprotocol.OutcomeFailed, subscribe(r, "n", "web", false).Outcome, "a named process is not the unlabeled container")

	// A release with processes labels the default one by its running name; a
	// rename that is still staged asks for it by the new name.
	r = newRig(t, nil)
	res, dr := r.deploy(processPayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.dk.mu.Lock()
	r.dk.logText[replicaName(depV2, 0)] = "serving"
	r.dk.logText[replicaName(depV2, 1)] = "serving"
	r.dk.logText[pname(depV2, "worker", 0)] = "job a"
	r.dk.mu.Unlock()
	require.Equal(t, agentprotocol.OutcomeSucceeded, subscribe(r, "http", "http", true).Outcome)
	require.Eventually(t, func() bool { return r.streamEnded("http") }, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"[r0] serving", "[r1] serving"}, r.streamLines("http"))
	require.Equal(t, agentprotocol.OutcomeFailed, subscribe(r, "lit", "http", false).Outcome, "without Default the name is compared literally")
}

func TestUnknownKindFails(t *testing.T) {
	r := newRig(t, nil)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: "format_disk"})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.True(t, strings.Contains(res.Error, "unsupported"))
}

// ---- reconcile ----

func TestReconcileAppliesRoutesReportsObservedAndSweepsOrphans(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	orphanOld := r.dk.seed("nc-gone-aaaaaaaa", "svc-gone", "dep-gone", true, time.Hour)
	orphanNew := r.dk.seed("nc-fresh-bbbbbbbb", "svc-fresh", "dep-fresh", true, time.Minute)
	busy := r.dk.seed("nc-busy-cccccccc", "svc-busy", "dep-busy", true, time.Hour)
	unlock := r.eng.lockService("svc-busy")
	defer unlock()

	route := agentprotocol.Route{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{oldCtr + ":80"}}
	err := r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{
		ACMEEmail: "a@b.c",
		Services: []agentprotocol.DesiredService{
			{ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr}, Routes: []agentprotocol.Route{route}},
			{ServiceID: "svc-missing", DeploymentID: "dep-m", Replicas: 1, ContainerNames: []string{"nc-missing-dddddddd-0"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []agentprotocol.Route{route}, r.px.Current())
	require.Equal(t, "a@b.c", r.px.email)

	require.Len(t, r.snd.observed, 1)
	byID := map[string]agentprotocol.ObservedService{}
	for _, o := range r.snd.observed[0].Services {
		byID[o.ServiceID] = o
	}
	require.Equal(t, agentprotocol.StateRunning, byID[svcID].State)
	require.Equal(t, agentprotocol.HealthHealthy, byID[svcID].Health)
	require.NotEmpty(t, byID[svcID].ContainerID)
	require.Equal(t, agentprotocol.StateMissing, byID["svc-missing"].State)

	_, ok := r.dk.state(orphanOld.Name)
	require.False(t, ok, "old orphan removed")
	_, ok = r.dk.state(orphanNew.Name)
	require.True(t, ok, "young container kept")
	_, ok = r.dk.state(busy.Name)
	require.True(t, ok, "in-flight service kept")
	require.True(t, running(t, r.dk, oldCtr), "desired container kept")
}

func TestReconcileKeepsLocalDeploymentNewerThanDesired(t *testing.T) {
	r := newRig(t, nil)
	r.previous()
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	// A stale frame still naming v1 must not move traffic back or delete v2.
	stale := agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{{
		ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr},
		Routes: []agentprotocol.Route{{ServiceID: svcID, Hosts: []string{"whoami.localhost"}, Upstreams: []string{oldCtr + ":80"}}},
	}}}
	r.eng.cfg.OrphanMinAge = time.Nanosecond
	require.NoError(t, r.eng.Reconcile(context.Background(), stale))
	cur := r.px.Current()
	require.Len(t, cur, 1)
	require.Equal(t, []string{newCtr + ":80"}, cur[0].Upstreams)
	require.True(t, running(t, r.dk, newCtr))
}

func TestReportStatsOnePerServicePreferringCurrentDeployment(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	cur := r.dk.seed(newCtr, svcID, depV2, true, time.Minute)
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV2, Replicas: 1, ContainerNames: []string{cur.Name}}
	r.dk.seed("nc-stopped-eeeeeeee", "svc-stopped", "d", false, time.Hour)

	r.eng.reportStats(context.Background())
	require.Len(t, r.snd.stats, 1)
	require.Len(t, r.snd.stats[0].Containers, 1)
	s := r.snd.stats[0].Containers[0]
	require.Equal(t, svcID, s.ServiceID)
	require.InDelta(t, 12.5, s.CPUPercent, 0.001)
	require.EqualValues(t, 1<<30, s.MemLimit)
}

// Docker reports host RAM as the limit of an unlimited container; the agent
// must send 0 so the UI never shows host memory as a configured limit.
func TestReportStatsTreatsHostMemoryLimitAsUnlimited(t *testing.T) {
	r := newRig(t, nil)
	r.dk.hostMem = 1 << 30 // equals the fake's reported container limit
	cur := r.dk.seed(newCtr, svcID, depV2, true, time.Minute)
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV2, Replicas: 1, ContainerNames: []string{cur.Name}}

	r.eng.reportStats(context.Background())
	require.Len(t, r.snd.stats, 1)
	require.Zero(t, r.snd.stats[0].Containers[0].MemLimit)
}
