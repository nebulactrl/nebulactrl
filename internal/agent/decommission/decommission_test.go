package decommission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeDocker records every mutation and fails the calls named in the err maps.
type fakeDocker struct {
	mu         sync.Mutex
	containers []docker.ContainerInfo
	networks   []docker.NetworkInfo
	volumes    []docker.VolumeInfo

	listContainersErr error
	listNetworksErr   error
	listVolumesErr    error
	stopErr           map[string]error
	removeErr         map[string]error
	netRemoveErr      map[string]error
	volRemoveErr      map[string]error
	disconnectErr     error

	stopped      map[string]time.Duration
	removed      []string
	removeForce  map[string]bool
	removeVols   map[string]bool
	order        []string
	netRemoved   []string
	disconnected []string
	volRemoved   []string
	volForce     map[string]bool
}

func newFake() *fakeDocker {
	return &fakeDocker{
		stopped: map[string]time.Duration{}, removeForce: map[string]bool{}, removeVols: map[string]bool{}, volForce: map[string]bool{},
	}
}

func (f *fakeDocker) ContainerList(_ context.Context, flt docker.ListFilter) ([]docker.ContainerInfo, error) {
	if f.listContainersErr != nil {
		return nil, f.listContainersErr
	}
	if !flt.All {
		return nil, errors.New("decommission must list stopped containers too")
	}
	return f.containers, nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, id string, timeout *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "stop:"+id)
	f.stopped[id] = *timeout
	return f.stopErr[id]
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, force, removeVolumes bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "rm:"+id)
	f.removed = append(f.removed, id)
	f.removeForce[id] = force
	f.removeVols[id] = removeVolumes
	return f.removeErr[id]
}

func (f *fakeDocker) NetworkList(context.Context, string, map[string]string) ([]docker.NetworkInfo, error) {
	return f.networks, f.listNetworksErr
}

func (f *fakeDocker) NetworkDisconnect(_ context.Context, network, container string) error {
	f.disconnected = append(f.disconnected, network+"/"+container)
	return f.disconnectErr
}

func (f *fakeDocker) NetworkRemove(_ context.Context, name string) error {
	f.netRemoved = append(f.netRemoved, name)
	return f.netRemoveErr[name]
}

func (f *fakeDocker) VolumeList(context.Context) ([]docker.VolumeInfo, error) {
	return f.volumes, f.listVolumesErr
}

func (f *fakeDocker) RemoveVolume(_ context.Context, name string, force bool) error {
	f.volRemoved = append(f.volRemoved, name)
	f.volForce[name] = force
	return f.volRemoveErr[name]
}

var managed = map[string]string{"nebulactrl.managed": "true"}

func TestRunRemovesOnlyNebulaCtrlResources(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{
		{ID: "c-svc", Name: "web", Labels: managed, Running: true},
		{ID: "c-dead", Name: "old", Labels: map[string]string{"nebulactrl.service": "x"}, State: "exited"},
		{ID: "c-proxy", Name: "/" + agentprotocol.ProxyContainer, State: "exited"},
		{ID: "c-tunnel", Name: agentprotocol.TunnelContainer, Running: true},
		{ID: "c-other", Name: "postgres", Labels: map[string]string{"com.docker.compose.project": "foreign"}, Running: true},
		{ID: "c-nolabel", Name: "nginx", Running: true},
	}
	f.networks = []docker.NetworkInfo{
		{Name: agentprotocol.EdgeNetwork},
		{Name: agentprotocol.MeshNetwork},
		{Name: "env-net", Labels: managed},
		{Name: "bridge"},
		{Name: "foreign-net", Labels: map[string]string{"app": "x"}},
	}
	f.volumes = []docker.VolumeInfo{
		{Name: agentprotocol.ProxyACMEVolume},
		{Name: agentprotocol.TailscaleStateVolume},
		{Name: agentprotocol.VolumePrefix + "abc-data"},
		{Name: "labelled", Labels: managed},
		{Name: "pgdata"},
		{Name: "nc-ts-abc-state"},
	}

	res, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"c-svc", "c-dead", "c-proxy", "c-tunnel"}, f.removed)
	assert.Equal(t, 4, res.Containers)
	for _, id := range f.removed {
		assert.True(t, f.removeForce[id], "%s removed with force", id)
		assert.True(t, f.removeVols[id], "%s removed with its anonymous volumes", id)
	}
	// Only running containers are stopped, with the default grace.
	assert.Equal(t, map[string]time.Duration{"c-svc": DefaultGrace, "c-tunnel": DefaultGrace}, f.stopped)

	assert.Equal(t, []string{agentprotocol.EdgeNetwork, agentprotocol.MeshNetwork, "env-net"}, f.netRemoved)
	assert.Equal(t, 3, res.Networks)
	assert.Equal(t, []string{agentprotocol.ProxyACMEVolume, agentprotocol.TailscaleStateVolume, agentprotocol.VolumePrefix + "abc-data", "labelled"}, f.volRemoved)
	assert.Equal(t, 4, res.Volumes)
	for name, force := range f.volForce {
		assert.False(t, force, "volume %s must not be force-removed", name)
	}
	assert.Empty(t, res.Failed)
	assert.Equal(t, "Removed 4 container(s), 3 network(s) and 4 volume(s).", res.Message)
}

func TestRunStopsRunningRestartingAndPausedOnly(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{
		{ID: "run", Name: "a", Labels: managed, Running: true},
		{ID: "rst", Name: "b", Labels: managed, Restarting: true},
		{ID: "pau", Name: "c", Labels: managed, State: "paused"},
		{ID: "ext", Name: "d", Labels: managed, State: "exited"},
		{ID: "crt", Name: "e", Labels: managed, State: "created"},
	}
	_, err := Run(context.Background(), Config{Docker: f, Grace: 5 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Duration{"run": 5 * time.Second, "rst": 5 * time.Second, "pau": 5 * time.Second}, f.stopped)
	assert.Len(t, f.removed, 5)
}

func TestRunStopsBeforeRemoving(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{{ID: "a", Name: "a", Labels: managed, Running: true}}
	_, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)
	assert.Equal(t, []string{"stop:a", "rm:a"}, f.order)
}

func TestRunStopFailureStillRemovesByForce(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{
		{ID: "a", Name: "a", Labels: managed, Running: true},
		{ID: "b", Name: "b", Labels: managed, Running: true},
	}
	f.stopErr = map[string]error{"a": errors.New("stuck"), "b": fmt.Errorf("gone: %w", docker.ErrNotFound)}
	res, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b"}, f.removed)
	assert.Equal(t, 2, res.Containers)
	assert.Empty(t, res.Failed)
}

func TestRunContainerRemoveErrors(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{
		{ID: "ok", Name: "ok", Labels: managed},
		{ID: "gone", Name: "gone", Labels: managed},
		{ID: "bad", Name: "bad", Labels: managed},
	}
	f.removeErr = map[string]error{"gone": fmt.Errorf("x: %w", docker.ErrNotFound), "bad": errors.New("device busy")}
	res, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Containers, "an already-gone container counts as removed")
	assert.Equal(t, []agentprotocol.DecommissionStep{{Kind: "container", Name: "bad", Error: "device busy"}}, res.Failed)
	assert.Equal(t, "Removed 2 container(s), 0 network(s) and 0 volume(s); 1 resource(s) could not be removed.", res.Message)
}

func TestRunNeverRemovesSelfAndLeavesItsNetworks(t *testing.T) {
	f := newFake()
	self := "abcdef0123456789"
	f.containers = []docker.ContainerInfo{
		{ID: self, Name: "agent", Labels: managed, Running: true},
		{ID: "abcdef012345", Name: "agent-short", Labels: managed, Running: true},
		{ID: "other", Name: "svc", Labels: managed},
	}
	f.networks = []docker.NetworkInfo{
		{Name: agentprotocol.EdgeNetwork, Containers: []string{"abcdef012345"}},
		{Name: "env", Labels: managed, Containers: []string{self}},
	}
	res, err := Run(context.Background(), Config{Docker: f, Self: self})
	require.NoError(t, err)
	assert.Equal(t, []string{"other"}, f.removed, "full and short IDs of self are both skipped")
	assert.Equal(t, []string{agentprotocol.EdgeNetwork + "/abcdef012345", "env/" + self}, f.disconnected)
	assert.Equal(t, []string{agentprotocol.EdgeNetwork, "env"}, f.netRemoved)
	assert.Equal(t, 2, res.Networks)
	assert.Empty(t, res.Failed)
}

func TestRunDisconnectFailureStillRemovesNetwork(t *testing.T) {
	f := newFake()
	f.networks = []docker.NetworkInfo{{Name: "env", Labels: managed, Containers: []string{"me"}}}
	f.disconnectErr = errors.New("nope")
	res, err := Run(context.Background(), Config{Docker: f, Self: "me"})
	require.NoError(t, err)
	assert.Equal(t, []string{"env"}, f.netRemoved)
	assert.Equal(t, 1, res.Networks)
}

func TestRunSelfEmptyMatchesNothing(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{{ID: "", Name: "x", Labels: managed}, {ID: "abc", Name: "y", Labels: managed}}
	res, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Containers, "without Self no container is spared")
}

func TestRunKeepsNetworkWithForeignContainers(t *testing.T) {
	f := newFake()
	f.networks = []docker.NetworkInfo{
		{Name: "shared", Labels: managed, Containers: []string{"foreign1", "foreign2", "me"}},
		{Name: "empty", Labels: managed},
	}
	res, err := Run(context.Background(), Config{Docker: f, Self: "me"})
	require.NoError(t, err)
	assert.Equal(t, []string{"empty"}, f.netRemoved)
	assert.Equal(t, 1, res.Networks)
	require.Len(t, res.Failed, 1)
	assert.Equal(t, "network", res.Failed[0].Kind)
	assert.Equal(t, "shared", res.Failed[0].Name)
	assert.Equal(t, "2 container(s) that NebulaCtrl does not manage are connected", res.Failed[0].Error)
}

func TestRunNetworkAndVolumeRemoveErrors(t *testing.T) {
	f := newFake()
	f.networks = []docker.NetworkInfo{
		{Name: "n-gone", Labels: managed}, {Name: "n-bad", Labels: managed},
	}
	f.volumes = []docker.VolumeInfo{
		{Name: "v-gone", Labels: managed}, {Name: "v-bad", Labels: managed},
	}
	f.netRemoveErr = map[string]error{"n-gone": docker.ErrNotFound, "n-bad": errors.New("in use")}
	f.volRemoveErr = map[string]error{"v-gone": docker.ErrNotFound, "v-bad": errors.New("volume in use")}
	res, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Networks)
	assert.Equal(t, 1, res.Volumes)
	assert.Equal(t, []agentprotocol.DecommissionStep{
		{Kind: "network", Name: "n-bad", Error: "in use"},
		{Kind: "volume", Name: "v-bad", Error: "volume in use"},
	}, res.Failed)
}

func TestRunListFailures(t *testing.T) {
	t.Run("containers abort the run", func(t *testing.T) {
		f := newFake()
		f.listContainersErr = errors.New("daemon down")
		res, err := Run(context.Background(), Config{Docker: f})
		require.Error(t, err)
		assert.Equal(t, "list containers: daemon down", err.Error())
		assert.Empty(t, f.netRemoved)
		assert.Zero(t, res.Containers)
	})
	t.Run("networks and volumes are reported and the run goes on", func(t *testing.T) {
		f := newFake()
		f.containers = []docker.ContainerInfo{{ID: "a", Name: "a", Labels: managed}}
		f.listNetworksErr = errors.New("net list")
		f.listVolumesErr = errors.New("vol list")
		res, err := Run(context.Background(), Config{Docker: f})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Containers)
		assert.Equal(t, []agentprotocol.DecommissionStep{
			{Kind: "network", Name: "(list)", Error: "net list"},
			{Kind: "volume", Name: "(list)", Error: "vol list"},
		}, res.Failed)
	})
}

func TestRunIsManagedNetworkCatchesUnlabelledNetworks(t *testing.T) {
	f := newFake()
	f.networks = []docker.NetworkInfo{{Name: "env-prod"}, {Name: "bridge"}}
	res, err := Run(context.Background(), Config{Docker: f, IsManagedNetwork: func(n string) bool { return n == "env-prod" }})
	require.NoError(t, err)
	assert.Equal(t, []string{"env-prod"}, f.netRemoved)
	assert.Equal(t, 1, res.Networks)
}

func TestRunComposeDown(t *testing.T) {
	newContainers := func() []docker.ContainerInfo {
		return []docker.ContainerInfo{
			{ID: "1", Name: "a", Labels: map[string]string{"nebulactrl.managed": "true", ComposeProjectLabel: "zeta"}},
			{ID: "2", Name: "b", Labels: map[string]string{"nebulactrl.managed": "true", ComposeProjectLabel: "alpha"}},
			{ID: "3", Name: "c", Labels: map[string]string{"nebulactrl.managed": "true", ComposeProjectLabel: "alpha"}},
			// A foreign compose project is never taken down.
			{ID: "4", Name: "d", Labels: map[string]string{ComposeProjectLabel: "foreign"}},
			// The proxy container is infra but not a deployed stack.
			{ID: "5", Name: agentprotocol.ProxyContainer, Labels: map[string]string{ComposeProjectLabel: "proxy-proj"}},
		}
	}
	t.Run("each deployed project once, sorted, before the containers", func(t *testing.T) {
		f := newFake()
		f.containers = newContainers()
		var calls []string
		res, err := Run(context.Background(), Config{Docker: f, ComposeDown: func(_ context.Context, p string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			assert.Empty(t, f.removed, "compose down runs before containers are removed")
			calls = append(calls, p)
			return nil
		}})
		require.NoError(t, err)
		assert.Equal(t, []string{"alpha", "zeta"}, calls)
		assert.Equal(t, 4, res.Containers)
	})
	t.Run("a failure is recorded and the run continues", func(t *testing.T) {
		f := newFake()
		f.containers = newContainers()
		res, err := Run(context.Background(), Config{Docker: f, ComposeDown: func(_ context.Context, p string) error {
			if p == "alpha" {
				return errors.New("compose exploded")
			}
			return nil
		}})
		require.NoError(t, err)
		assert.Equal(t, []agentprotocol.DecommissionStep{{Kind: "compose", Name: "alpha", Error: "compose exploded"}}, res.Failed)
		assert.Equal(t, 4, res.Containers)
	})
	t.Run("nil ComposeDown still removes the containers", func(t *testing.T) {
		f := newFake()
		f.containers = newContainers()
		res, err := Run(context.Background(), Config{Docker: f})
		require.NoError(t, err)
		assert.Equal(t, 4, res.Containers)
		assert.Empty(t, res.Failed)
	})
}

func TestRunReturnsContextError(t *testing.T) {
	f := newFake()
	f.containers = []docker.ContainerInfo{{ID: "a", Name: "a", Labels: managed}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Run(ctx, Config{Docker: f})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, res.Containers, "the partial result is still returned")
}

func TestRunManyContainersAllRemoved(t *testing.T) {
	f := newFake()
	for i := range 40 {
		f.containers = append(f.containers, docker.ContainerInfo{ID: fmt.Sprintf("c%02d", i), Name: fmt.Sprintf("n%d", i), Labels: managed, Running: i%2 == 0})
	}
	res, err := Run(context.Background(), Config{Docker: f})
	require.NoError(t, err)
	assert.Equal(t, 40, res.Containers)
	assert.Len(t, f.removed, 40)
	assert.Len(t, f.stopped, 20)
}
