package stats

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeEngine answers the few Engine calls the collector makes.
type fakeEngine struct {
	docker.Engine
	info       docker.Info
	infoErr    error
	version    string
	versionErr error
	inspect    docker.ContainerInfo
	inspectErr error
	inspected  []string
}

func (f *fakeEngine) Info(context.Context) (docker.Info, error) { return f.info, f.infoErr }
func (f *fakeEngine) ServerVersion(context.Context) (string, error) {
	return f.version, f.versionErr
}
func (f *fakeEngine) ContainerInspect(_ context.Context, id string) (docker.ContainerInfo, error) {
	f.inspected = append(f.inspected, id)
	return f.inspect, f.inspectErr
}

// hostRunner is a fakeEngine that can also run a command on the host.
type hostRunner struct {
	*fakeEngine
	spec docker.HostCommandSpec
	res  docker.HostCommandResult
	err  error
}

func (h *hostRunner) RunHostCommand(_ context.Context, spec docker.HostCommandSpec) (docker.HostCommandResult, error) {
	h.spec = spec
	return h.res, h.err
}

func TestFactsReportTheDaemonsVersionAndCgroups(t *testing.T) {
	eng := &fakeEngine{info: docker.Info{ServerVersion: "27.3.1", CgroupVersion: "2", KernelVersion: "6.8.0"}}
	c := New(Options{Engine: eng, HTTPPort: 18080, HTTPSPort: 18443})
	f := c.Facts(context.Background())
	require.Equal(t, "27.3.1", f.DockerVersion)
	require.Equal(t, "2", f.CgroupVersion)
	require.NotEmpty(t, f.Kernel)

	// The daemon's version is what the next heartbeat reports until it changes.
	eng.versionErr = errors.New("daemon busy")
	require.Equal(t, "27.3.1", c.Heartbeat(context.Background()).DockerVersion)
}

func TestFactsWithAnUnreachableDaemonLeaveDockerFieldsEmpty(t *testing.T) {
	eng := &fakeEngine{infoErr: errors.New("connection refused")}
	c := New(Options{Engine: eng, HTTPPort: 18080, HTTPSPort: 18443})
	f := c.Facts(context.Background())
	require.Empty(t, f.DockerVersion)
	require.Empty(t, f.CgroupVersion)
	require.Contains(t, f.Capabilities, agentprotocol.CapProxyV2, "capabilities do not depend on the daemon answering")
	require.Empty(t, c.Heartbeat(context.Background()).DockerVersion)
}

func TestHeartbeatTracksTheDaemonVersion(t *testing.T) {
	eng := &fakeEngine{version: "26.1.0"}
	c := New(Options{Engine: eng})
	require.Equal(t, "26.1.0", c.Heartbeat(context.Background()).DockerVersion)
	eng.version = "27.0.0" // daemon upgraded in place
	require.Equal(t, "27.0.0", c.Heartbeat(context.Background()).DockerVersion)
}

func TestHeartbeatCarriesEveryObservedStatus(t *testing.T) {
	proxy := agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK}
	tunnel := &agentprotocol.TunnelStatus{}
	mesh := &agentprotocol.MeshStatus{}
	geo := &agentprotocol.GeoStatus{}
	certs := []agentprotocol.AcmeCertStatus{{}}
	c := New(Options{
		ProxyStatus:  func() agentprotocol.ProxyHealth { return proxy },
		TunnelStatus: func(context.Context) *agentprotocol.TunnelStatus { return tunnel },
		MeshStatus:   func(context.Context) *agentprotocol.MeshStatus { return mesh },
		GeoStatus:    func() *agentprotocol.GeoStatus { return geo },
		AcmeStatuses: func() []agentprotocol.AcmeCertStatus { return certs },
	})
	hb := c.Heartbeat(context.Background())
	require.Equal(t, agentprotocol.ProxyOK, hb.Proxy.Status)
	require.Same(t, tunnel, hb.Tunnel)
	require.Same(t, mesh, hb.Mesh)
	require.Same(t, geo, hb.Geo)
	require.Equal(t, certs, hb.AcmeCerts)
	require.Greater(t, hb.MemTotal, uint64(0))
	require.Greater(t, hb.DiskTotal, uint64(0))
}

func TestHeartbeatWithoutStatusSourcesStartsTheProxyAtStarting(t *testing.T) {
	hb := New(Options{}).Heartbeat(context.Background())
	require.Equal(t, agentprotocol.ProxyStarting, hb.Proxy.Status)
	require.Nil(t, hb.Tunnel)
	require.Nil(t, hb.Mesh)
	require.Nil(t, hb.Geo)
	require.Empty(t, hb.AcmeCerts)
}

func TestPortFreeReflectsWhoHoldsThePort(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.False(t, portFree(port), "a port someone listens on is not free")
	require.NoError(t, l.Close())
	require.True(t, portFree(port))
}

func TestFactsProbeTheConfiguredProxyPorts(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer busy.Close()
	free, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	freePort := free.Addr().(*net.TCPAddr).Port
	require.NoError(t, free.Close())

	c := New(Options{HTTPPort: busy.Addr().(*net.TCPAddr).Port, HTTPSPort: freePort})
	f := c.Facts(context.Background())
	require.False(t, f.Port80Free)
	require.True(t, f.Port443Free)
}

func TestNewDefaultsTheProxyPorts(t *testing.T) {
	c := New(Options{})
	require.Equal(t, 80, c.opt.HTTPPort)
	require.Equal(t, 443, c.opt.HTTPSPort)
}

func TestDiskPathPrefersTheStateDirAndFallsBackToRoot(t *testing.T) {
	dir := t.TempDir()
	require.Equal(t, dir, New(Options{StateDir: dir}).diskPath())
	require.Equal(t, "/", New(Options{StateDir: filepath.Join(dir, "missing")}).diskPath())
	require.Equal(t, "/", New(Options{}).diskPath())
}

func TestHostBinfmtAsksAHelperContainerWithTheAgentsImage(t *testing.T) {
	eng := &hostRunner{
		fakeEngine: &fakeEngine{inspect: docker.ContainerInfo{ImageID: "sha256:abc", Image: "nebula/agent:1"}},
		res:        docker.HostCommandResult{Stdout: "qemu-aarch64\nnoise\n  qemu-riscv64  \n"},
	}
	c := New(Options{Engine: eng, AgentContainer: "nebulactrl-agent"})
	names, final := c.hostBinfmt(context.Background())
	require.True(t, final)
	require.Equal(t, []string{"qemu-aarch64", "qemu-riscv64"}, names)
	require.Equal(t, []string{"nebulactrl-agent"}, eng.inspected)
	require.Equal(t, "sha256:abc", eng.spec.Image, "the helper runs the agent's own image")
	require.Contains(t, eng.spec.Name, "nebulactrl-hostcmd-binfmt-")
	require.Equal(t, "hostcmd", eng.spec.Labels[agentprotocol.LabelManaged])
	require.Equal(t, []string{"nsenter", "-t", "1"}, eng.spec.Argv[:3])
	require.Contains(t, eng.spec.Argv[len(eng.spec.Argv)-1], binfmtDir)
}

func TestHostBinfmtFallsBackToTheImageReference(t *testing.T) {
	eng := &hostRunner{fakeEngine: &fakeEngine{inspect: docker.ContainerInfo{Image: "nebula/agent:1"}}}
	c := New(Options{Engine: eng, AgentContainer: "agent"})
	_, final := c.hostBinfmt(context.Background())
	require.True(t, final)
	require.Equal(t, "nebula/agent:1", eng.spec.Image)
}

func TestHostBinfmtGivesAConclusiveEmptyAnswerWhenThereIsNothingToAsk(t *testing.T) {
	run := func(eng docker.Engine, container string) ([]string, bool) {
		return New(Options{Engine: eng, AgentContainer: container}).hostBinfmt(context.Background())
	}
	// The engine cannot run host commands.
	names, final := run(&fakeEngine{}, "agent")
	require.Empty(t, names)
	require.True(t, final)
	// The agent's own container cannot be found.
	runner := &hostRunner{fakeEngine: &fakeEngine{inspectErr: errors.New("no such container")}}
	names, final = run(runner, "agent")
	require.Empty(t, names)
	require.True(t, final)
	require.Empty(t, runner.spec.Image, "no helper was started")
	// The container has no image to reuse.
	names, final = run(&hostRunner{fakeEngine: &fakeEngine{}}, "agent")
	require.Empty(t, names)
	require.True(t, final)
}

func TestHostBinfmtFailureIsRetriedAtTheNextHello(t *testing.T) {
	eng := &hostRunner{
		fakeEngine: &fakeEngine{inspect: docker.ContainerInfo{ImageID: "sha256:abc"}},
		err:        errors.New("image pull failed"),
	}
	names, final := New(Options{Engine: eng, AgentContainer: "agent"}).hostBinfmt(context.Background())
	require.Empty(t, names)
	require.False(t, final, "a failed helper is not a statement that nothing is emulated")
}

func TestHostBinfmtDefaultsTheContainerToTheHostname(t *testing.T) {
	eng := &hostRunner{fakeEngine: &fakeEngine{inspect: docker.ContainerInfo{ImageID: "sha256:abc"}}}
	_, _ = New(Options{Engine: eng}).hostBinfmt(context.Background())
	hn, _ := os.Hostname()
	require.Equal(t, []string{hn}, eng.inspected, "inside a container the hostname is the container ID")
}

func TestPlatformsFromBinfmtPerHostArchitecture(t *testing.T) {
	all := []string{"qemu-x86_64", "qemu-aarch64", "qemu-arm", "qemu-riscv64", "qemu-i386"}
	require.Equal(t, []string{"linux/arm/v6", "linux/arm/v7", "linux/arm64", "linux/riscv64"}, PlatformsFromBinfmt(all, "amd64"))
	require.Equal(t, []string{"linux/386", "linux/amd64", "linux/riscv64"}, PlatformsFromBinfmt(all, "arm64"))
	require.Equal(t, []string{"linux/386", "linux/amd64", "linux/arm/v6", "linux/arm/v7", "linux/arm64"}, PlatformsFromBinfmt(all, "riscv64"))
	require.Empty(t, PlatformsFromBinfmt([]string{"qemu-unknown"}, "amd64"))
}

func TestEmulationForAnUnreadableDirectoryIsEmpty(t *testing.T) {
	require.Empty(t, enabledBinfmt("["), "a malformed glob pattern yields no entries")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "qemu-arm"), []byte("disabled\ninterpreter /usr/bin/qemu-arm\n"), 0o600))
	require.Empty(t, enabledBinfmt(dir))
}
