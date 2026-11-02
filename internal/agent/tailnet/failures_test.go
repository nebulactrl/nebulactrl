package tailnet

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// failEngine is the fake engine with an injectable error per call.
type failEngine struct {
	*fakeEngine
	networkErrs                                                   map[string]error // by network name
	internalCalls                                                 []string
	inspectErr, startErr, stopErr, removeErr, volumeErr, rmVolErr error
	imageErr, pullErr, copyErr, execErr, listErr                  error
	imageMissing                                                  bool
	removed                                                       []string
}

func newFailEngine(status string) *failEngine {
	return &failEngine{fakeEngine: newFake(status), networkErrs: map[string]error{}}
}

func (f *failEngine) EnsureNetwork(ctx context.Context, name string, l map[string]string, o ...docker.NetworkOption) error {
	if err := f.networkErrs[name]; err != nil {
		return err
	}
	return f.fakeEngine.EnsureNetwork(ctx, name, l, o...)
}

func (f *failEngine) EnsureInternalNetwork(_ context.Context, name string, _ map[string]string, _ ...docker.NetworkOption) error {
	f.internalCalls = append(f.internalCalls, name)
	return f.networkErrs[name]
}

func (f *failEngine) ContainerInspect(ctx context.Context, id string) (docker.ContainerInfo, error) {
	if f.inspectErr != nil {
		return docker.ContainerInfo{}, f.inspectErr
	}
	return f.fakeEngine.ContainerInspect(ctx, id)
}

func (f *failEngine) ContainerList(ctx context.Context, flt docker.ListFilter) ([]docker.ContainerInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.fakeEngine.ContainerList(ctx, flt)
}

func (f *failEngine) ContainerStart(ctx context.Context, id string) error {
	if f.startErr != nil {
		return f.startErr
	}
	return f.fakeEngine.ContainerStart(ctx, id)
}

func (f *failEngine) ContainerStop(ctx context.Context, id string, d *time.Duration) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	return f.fakeEngine.ContainerStop(ctx, id, d)
}

func (f *failEngine) ContainerRemove(ctx context.Context, id string, force, vols bool) error {
	f.removed = append(f.removed, id)
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.fakeEngine.ContainerRemove(ctx, id, force, vols)
}

func (f *failEngine) EnsureVolume(ctx context.Context, name string, l map[string]string) error {
	if f.volumeErr != nil {
		return f.volumeErr
	}
	return f.fakeEngine.EnsureVolume(ctx, name, l)
}

func (f *failEngine) RemoveVolume(ctx context.Context, name string, force bool) error {
	if f.rmVolErr != nil {
		return f.rmVolErr
	}
	return f.fakeEngine.RemoveVolume(ctx, name, force)
}

func (f *failEngine) ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error) {
	if f.imageErr != nil {
		return docker.ImageInfo{}, f.imageErr
	}
	if f.imageMissing {
		return docker.ImageInfo{}, notFound("image")
	}
	return f.fakeEngine.ImageInspect(ctx, ref)
}

func (f *failEngine) ImagePull(ctx context.Context, ref, auth string, p func(string)) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	return f.fakeEngine.ImagePull(ctx, ref, auth, p)
}

func (f *failEngine) CopyToContainer(ctx context.Context, id, dir string, r io.Reader) error {
	if f.copyErr != nil {
		return f.copyErr
	}
	return f.fakeEngine.CopyToContainer(ctx, id, dir, r)
}

func (f *failEngine) ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, o, e io.Writer) (int, error) {
	if f.execErr != nil {
		return 0, f.execErr
	}
	return f.fakeEngine.ContainerExec(ctx, id, argv, spec, o, e)
}

func (f *failEngine) seedSidecar(spec agentprotocol.ServiceTailnetSpec, mutate func(*docker.ContainerInfo)) *docker.ContainerInfo {
	want := ContainerSpec(spec, "")
	c := &docker.ContainerInfo{ID: "id-old", Name: want.Name, Running: true, Labels: want.Labels}
	if mutate != nil {
		mutate(c)
	}
	f.containers[want.Name] = c
	return c
}

func TestEnsureReportsFailuresToSetUpTheNode(t *testing.T) {
	envNet := agentprotocol.EnvNetwork(envID)
	internalNet := agentprotocol.EnvInternalNetwork(envID)
	cases := []struct {
		name    string
		spec    func() agentprotocol.ServiceTailnetSpec
		prepare func(f *failEngine)
		want    string
	}{
		{"environment network", baseSpec, func(f *failEngine) { f.networkErrs[envNet] = errors.New("pool exhausted") }, "Could not create the environment network: pool exhausted"},
		{"internal network", func() agentprotocol.ServiceTailnetSpec { s := baseSpec(); s.BlockEgress = true; return s },
			func(f *failEngine) { f.networkErrs[internalNet] = errors.New("pool exhausted") }, "Could not create the internal environment network: pool exhausted"},
		{"inspect", baseSpec, func(f *failEngine) { f.inspectErr = errors.New("socket closed") }, "Could not inspect the tailnet sidecar: socket closed"},
		{"image check", provisionSpec, func(f *failEngine) { f.imageErr = errors.New("daemon busy") }, "Could not pull the tailscale image: daemon busy"},
		{"image pull", provisionSpec, func(f *failEngine) { f.imageMissing, f.pullErr = true, errors.New("rate limited") }, "Could not pull the tailscale image: rate limited"},
		{"volume", provisionSpec, func(f *failEngine) { f.volumeErr = errors.New("disk full") }, "Could not create the sidecar volume: disk full"},
		{"create", provisionSpec, func(f *failEngine) { f.createErr = errors.New("name in use") }, "Could not create the tailnet sidecar: name in use"},
		{"serve config", provisionSpec, func(f *failEngine) { f.copyErr = errors.New("no space") }, "Could not write the serve config: no space"},
		{"start", provisionSpec, func(f *failEngine) { f.startErr = errors.New("oci error") }, "Could not start the tailnet sidecar: oci error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFailEngine(statusOnline)
			tc.prepare(f)
			st := newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), tc.spec(), testKey, nil)
			require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
			require.Equal(t, tc.want, st.Message)
			require.Equal(t, "abc", st.IdentityHash, "the identity hash of the request is echoed back")
		})
	}
}

// withEngine points the manager at the failing wrapper (newManager takes the plain fake).
func (m *Manager) withEngine(e docker.Engine) *Manager { m.eng = e; return m }

func TestEnsureCleansUpASidecarItCouldNotFinishSettingUp(t *testing.T) {
	for name, prepare := range map[string]func(f *failEngine){
		"serve config failed": func(f *failEngine) { f.copyErr = errors.New("no space") },
		"start failed":        func(f *failEngine) { f.startErr = errors.New("cannot start") },
	} {
		f := newFailEngine(statusOnline)
		prepare(f)
		st := newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), provisionSpec(), testKey, nil)
		require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State, name)
		require.Empty(t, f.containers, "%s: no half-made sidecar stays behind", name)
	}
}

func TestEnsureUsesTheEnginesNetworkAllocatorWhenSet(t *testing.T) {
	f := newFailEngine(statusOnline)
	m := newManager(f.fakeEngine).withEngine(f)
	type call struct {
		name     string
		internal bool
	}
	var calls []call
	m.EnsureNetwork = func(_ context.Context, name string, labels map[string]string, internal bool) error {
		calls = append(calls, call{name, internal})
		require.Equal(t, envID, labels[agentprotocol.LabelEnvironment])
		return nil
	}
	spec := baseSpec()
	spec.BlockEgress = true
	st := m.Ensure(context.Background(), spec, "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.Equal(t, []call{{agentprotocol.EnvNetwork(envID), false}, {agentprotocol.EnvInternalNetwork(envID), true}}, calls)
	require.Empty(t, f.internalCalls, "the engine's own network creation is bypassed")
}

func TestEnsureCreatesTheInternalNetworkOnlyWhenEgressIsBlocked(t *testing.T) {
	f := newFailEngine(statusOnline)
	m := newManager(f.fakeEngine).withEngine(f)
	spec := baseSpec()
	m.Ensure(context.Background(), spec, "", nil)
	require.Empty(t, f.internalCalls)

	spec.BlockEgress = true
	m.Ensure(context.Background(), spec, "", nil)
	require.Equal(t, []string{agentprotocol.EnvInternalNetwork(envID)}, f.internalCalls)
}

func TestProvisionStopsWhenTheOldNodeCannotBeRemoved(t *testing.T) {
	spec := provisionSpec()
	f := newFailEngine(statusOnline)
	f.seedSidecar(spec, nil)
	f.stopErr = errors.New("daemon busy")
	st := newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, testKey, nil)
	require.Equal(t, "stop the tailnet sidecar: daemon busy", st.Message)
	require.Empty(t, f.created, "nothing new is created over a node that is still running")

	f = newFailEngine(statusOnline)
	f.seedSidecar(spec, nil)
	f.removeErr = errors.New("device busy")
	st = newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, testKey, nil)
	require.Equal(t, "remove the tailnet sidecar: device busy", st.Message)

	f = newFailEngine(statusOnline)
	f.volumes["nc-ts-11111111-state"] = true
	f.rmVolErr = errors.New("volume in use")
	st = newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, testKey, nil)
	require.Equal(t, "remove volume nc-ts-11111111-state: volume in use", st.Message)
}

func TestChangedSidecarIsReplacedEvenIfItVanishedMeanwhile(t *testing.T) {
	spec := baseSpec()
	f := newFailEngine(statusOnline)
	f.seedSidecar(spec, func(c *docker.ContainerInfo) { c.Labels = map[string]string{agentprotocol.LabelTailnetSpec: "stale"} })
	f.removeErr = docker.ErrNotFound
	st := newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.Len(t, f.created, 1)

	f = newFailEngine(statusOnline)
	f.seedSidecar(spec, func(c *docker.ContainerInfo) { c.Labels = map[string]string{agentprotocol.LabelTailnetSpec: "stale"} })
	f.removeErr = errors.New("device busy")
	st = newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, "Could not remove the old sidecar: device busy", st.Message)
	require.Empty(t, f.created)
}

func TestEnsureOfAnExistingStoppedNode(t *testing.T) {
	spec := baseSpec()
	f := newFailEngine(statusOnline)
	f.seedSidecar(spec, func(c *docker.ContainerInfo) { c.Running = false })
	st := newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.Empty(t, f.created, "a stopped node with the right configuration is started, not recreated")
	require.GreaterOrEqual(t, f.index("start "), 0)

	f = newFailEngine(statusOnline)
	f.seedSidecar(spec, func(c *docker.ContainerInfo) { c.Running = false })
	f.startErr = errors.New("port clash")
	st = newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, "Could not start the tailnet sidecar: port clash", st.Message)

	f = newFailEngine(statusOnline)
	f.seedSidecar(spec, nil)
	f.copyErr = errors.New("read-only")
	st = newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, "Could not update the serve config: read-only", st.Message)
}

func TestRunningHeadscaleNodeKeepsItsNameInTheRewrittenServeConfig(t *testing.T) {
	spec := baseSpec()
	spec.Provider, spec.LoginServer = agentprotocol.TailnetProviderHeadscale, "https://hs.example.com"
	f := newFailEngine(`{"BackendState":"Running","Self":{"ID":"7","DNSName":"web-prod.hs.example.com.","TailscaleIPs":["100.64.0.3"]}}`)
	f.seedSidecar(spec, nil)
	st := newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.NotEmpty(t, f.copies)
	require.Contains(t, f.copies[0].content, "web-prod.hs.example.com:80", "the already-known DNS name is used at once, the exposure is not interrupted")
}

func TestWaitOnlineOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("inspect fails", func(t *testing.T) {
		f := newFailEngine(statusOnline)
		f.inspectErr = errors.New("socket closed")
		st := newManager(f.fakeEngine).withEngine(f).waitOnline(ctx, "id")
		require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
		require.Equal(t, "Cannot inspect the tailnet sidecar: socket closed", st.Message)
	})

	t.Run("exited cleanly", func(t *testing.T) {
		f := newFailEngine(statusOnline)
		f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.ID, c.Running, c.ExitCode = "id", false, 0 })
		st := newManager(f.fakeEngine).withEngine(f).waitOnline(ctx, "id")
		require.Equal(t, agentprotocol.ServiceTailnetOffline, st.State)
		require.Contains(t, st.Message, "exited with code 0")
		require.Contains(t, st.Message, "not logged in", "the last log lines are attached")
		require.NotContains(t, st.Message, "Re-provision")
	})

	t.Run("crashed", func(t *testing.T) {
		f := newFailEngine(statusOnline)
		f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.ID, c.Running, c.ExitCode = "id", false, 3 })
		st := newManager(f.fakeEngine).withEngine(f).waitOnline(ctx, "id")
		require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
		require.Contains(t, st.Message, "exited with code 3")
		require.Contains(t, st.Message, "Re-provision")
	})

	t.Run("tailscaled never answers", func(t *testing.T) {
		f := newFailEngine(statusOnline)
		f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.ID = "id" })
		f.execErr = errors.New("exec failed")
		st := newManager(f.fakeEngine).withEngine(f).waitOnline(ctx, "id")
		require.Equal(t, agentprotocol.ServiceTailnetProvisioning, st.State)
		require.Equal(t, "The node is still coming up.", st.Message)
	})

	t.Run("restarting container never answers", func(t *testing.T) {
		f := newFailEngine(statusOnline)
		f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.ID, c.Running, c.Restarting = "id", false, true })
		m := newManager(f.fakeEngine).withEngine(f)
		st := m.waitOnline(ctx, "id")
		require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State, "no status can be read from a node that never runs")
		require.Equal(t, "The tailnet node did not answer in time.", st.Message)
	})

	t.Run("agent stops", func(t *testing.T) {
		f := newFailEngine(`{"BackendState":"Starting"}`)
		f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.ID = "id" })
		m := newManager(f.fakeEngine).withEngine(f)
		m.WaitRunning = time.Minute
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		st := m.waitOnline(cctx, "id")
		require.Equal(t, "The agent stopped while waiting for the tailnet node.", st.Message)
	})

	t.Run("needs login at the deadline", func(t *testing.T) {
		f := newFailEngine(statusLogin)
		f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.ID = "id" })
		st := newManager(f.fakeEngine).withEngine(f).waitOnline(ctx, "id")
		require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
		require.Contains(t, st.Message, "Re-provision")
		require.Contains(t, st.Message, "Last log lines: tailscaled: control: not logged in")
	})
}

func TestReadStatusAnswersWhileTailscaledIsStarting(t *testing.T) {
	f := newFailEngine(statusOnline)
	m := newManager(f.fakeEngine).withEngine(f)
	f.execErr = errors.New("exec failed")
	st := m.readStatus(context.Background(), "id")
	require.Equal(t, agentprotocol.ServiceTailnetProvisioning, st.State)
	require.Equal(t, "Waiting for tailscaled to answer.", st.Message)
}

func TestRemoveReportsWhatItCouldNotDelete(t *testing.T) {
	f := newFailEngine(statusOnline)
	f.inspectErr = errors.New("socket closed")
	st := newManager(f.fakeEngine).withEngine(f).Remove(context.Background(), svcID, nil)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.Equal(t, "Could not remove the tailnet sidecar: inspect the tailnet sidecar: socket closed", st.Message)
	require.False(t, st.Removed)

	f = newFailEngine(statusOnline)
	var logged []string
	st = newManager(f.fakeEngine).withEngine(f).Remove(context.Background(), svcID, func(s string) { logged = append(logged, s) })
	require.True(t, st.Removed)
	require.Equal(t, []string{"Tailnet: removed the sidecar and its volumes"}, logged)
}

func TestObserveHandlesEngineErrorsAndRestartLoops(t *testing.T) {
	f := newFailEngine(statusOnline)
	f.listErr = errors.New("socket closed")
	require.Nil(t, newManager(f.fakeEngine).withEngine(f).Observe(context.Background(), svcID))

	f = newFailEngine(statusOnline)
	f.seedSidecar(baseSpec(), func(c *docker.ContainerInfo) { c.Running, c.Restarting, c.State = false, true, "restarting" })
	st := newManager(f.fakeEngine).withEngine(f).Observe(context.Background(), svcID)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.Contains(t, st.Message, "keeps restarting")
}

func TestObserveRereadsTheStatusAfterTheCacheExpires(t *testing.T) {
	f := newFailEngine(statusOnline)
	f.seedSidecar(baseSpec(), nil)
	m := newManager(f.fakeEngine).withEngine(f)
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	require.Equal(t, agentprotocol.ServiceTailnetOnline, m.Observe(context.Background(), svcID).State)
	execs := f.execs
	now = now.Add(statusEvery - time.Second)
	m.Observe(context.Background(), svcID)
	require.Equal(t, execs, f.execs)
	now = now.Add(2 * time.Second)
	f.statusJSON = `{"BackendState":"Stopped"}`
	require.Equal(t, agentprotocol.ServiceTailnetOffline, m.Observe(context.Background(), svcID).State, "a stale cache entry is replaced by a fresh read")
	require.Greater(t, f.execs, execs)
}

func TestLimitWriterDropsWhatDoesNotFit(t *testing.T) {
	var b strings.Builder
	w := &limitWriter{w: &b, n: 5}
	n, err := w.Write([]byte("abc"))
	require.NoError(t, err)
	require.Equal(t, 3, n)
	n, err = w.Write([]byte("defgh"))
	require.NoError(t, err)
	require.Equal(t, 5, n, "the writer is told everything was taken")
	n, err = w.Write([]byte("zzz"))
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, "abcde", b.String())
}

func TestEnsureReportsOnlineLogAndRedactsTheKey(t *testing.T) {
	f := newFailEngine(statusOnline)
	var logged []string
	newManager(f.fakeEngine).withEngine(f).Ensure(context.Background(), provisionSpec(), testKey, func(s string) { logged = append(logged, s) })
	require.Contains(t, logged, "Tailnet: provisioning web-prod")
	require.Contains(t, logged, "Tailnet: web-prod is online as web-prod.tail1234.ts.net")
	require.Equal(t, "abcdefgh", redactKey("abcdefgh", "abc"), "a key too short to be a key is not masked")
	require.Equal(t, "x ••••••", redactKey("x "+testKey, testKey))
}
