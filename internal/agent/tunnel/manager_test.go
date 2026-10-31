package tunnel

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeEngine implements the part of docker.Engine the manager uses; any other
// call panics through the nil embedded interface.
type fakeEngine struct {
	docker.Engine

	mu         sync.Mutex
	containers map[string]*docker.ContainerInfo // by name
	events     []string
	created    []docker.ContainerSpec
	copies     []copyRec
	pulled     []string
	createErr  error
	logs       string
	health     string // health status given to a started container
	edgeIP     string // IP on the edge network of a started container
}

type copyRec struct {
	id, dir string
	file    string
	mode    int64
	content string
}

func newFake() *fakeEngine {
	return &fakeEngine{containers: map[string]*docker.ContainerInfo{}, edgeIP: "172.20.0.9"}
}

func (f *fakeEngine) log(format string, a ...any) {
	f.events = append(f.events, fmt.Sprintf(format, a...))
}

func (f *fakeEngine) find(idOrName string) *docker.ContainerInfo {
	for _, c := range f.containers {
		if c.ID == idOrName || c.Name == idOrName {
			return c
		}
	}
	return nil
}

func notFound(what string) error { return fmt.Errorf("%s: %w", what, docker.ErrNotFound) }

func (f *fakeEngine) EnsureNetwork(_ context.Context, name string, _ map[string]string, _ ...docker.NetworkOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("network %s", name)
	return nil
}

func (f *fakeEngine) ImageInspect(context.Context, string) (docker.ImageInfo, error) {
	return docker.ImageInfo{}, notFound("image")
}

func (f *fakeEngine) ImagePull(_ context.Context, ref, _ string, _ func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	f.log("pull %s", ref)
	return nil
}

func (f *fakeEngine) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created = append(f.created, spec)
	f.containers[spec.Name] = &docker.ContainerInfo{
		ID: "id-" + spec.Name + fmt.Sprint(len(f.created)), Name: spec.Name, Image: spec.Image, Labels: spec.Labels, Env: spec.Env, Cmd: spec.Cmd, State: "created",
	}
	f.log("create %s", spec.Name)
	return f.containers[spec.Name].ID, nil
}

func (f *fakeEngine) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	c.Running, c.State, c.Health = true, "running", f.health
	c.Networks = map[string]docker.ContainerNetwork{agentprotocol.EdgeNetwork: {IP: f.edgeIP}}
	f.log("start %s", c.Name)
	return nil
}

func (f *fakeEngine) ContainerStop(_ context.Context, id string, _ *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	c.Running, c.State = false, "exited"
	f.log("stop %s", c.Name)
	return nil
}

func (f *fakeEngine) ContainerRemove(_ context.Context, id string, _, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	delete(f.containers, c.Name)
	f.log("remove %s", c.Name)
	return nil
}

func (f *fakeEngine) ContainerInspect(_ context.Context, id string) (docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return docker.ContainerInfo{}, notFound(id)
	}
	return *c, nil
}

func (f *fakeEngine) CopyToContainer(_ context.Context, id, dir string, content io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	tr := tar.NewReader(content)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		f.copies = append(f.copies, copyRec{id: id, dir: dir, file: h.Name, mode: h.Mode, content: string(b)})
		f.log("copy %s %s%s", id, dir, h.Name)
	}
	return nil
}

func (f *fakeEngine) ContainerLogs(context.Context, string, docker.LogOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

func (f *fakeEngine) set(fn func(c *docker.ContainerInfo)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.containers[agentprotocol.TunnelContainer])
}

func (f *fakeEngine) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, strings.SplitN(e, " ", 2)[0])
	}
	return out
}

func readyFn(code int, body string) func(context.Context, string) (int, []byte, error) {
	return func(context.Context, string) (int, []byte, error) { return code, []byte(body), nil }
}

func newManager(f *fakeEngine) *Manager {
	m := New(f, nil)
	m.WaitOnline, m.Poll = 30*time.Millisecond, time.Millisecond
	m.Ready = readyFn(http.StatusOK, `{"status":200,"readyConnections":4,"connectorId":"c1"}`)
	return m
}

var spec = agentprotocol.TunnelSpec{TunnelID: tunnelID, Name: "edge-1"}

func TestEnsureCreatesAndWaitsOnline(t *testing.T) {
	f := newFake()
	m := newManager(f)
	var logged []string
	st := m.Ensure(context.Background(), spec, testToken, func(s string) { logged = append(logged, s) })

	require.Equal(t, agentprotocol.TunnelStateOnline, st.State, st.Message)
	require.Equal(t, 4, st.Connections)
	require.Equal(t, tunnelID, st.TunnelID)
	require.Equal(t, []string{"network", "pull", "create", "copy", "copy", "start"}, f.verbs())
	require.Equal(t, []string{Image}, f.pulled)
	require.Equal(t, []docker.NetworkAttachment{{Network: agentprotocol.EdgeNetwork, GwPriority: agentprotocol.EdgeGwPriority}}, f.created[0].Networks)

	// The token is delivered as a file before the first start, never in the spec.
	require.Len(t, f.created, 1)
	require.Empty(t, f.created[0].Env)
	require.NotContains(t, strings.Join(f.created[0].Cmd, " "), testToken)
	for k, v := range f.created[0].Labels {
		require.NotContains(t, k+v, testToken)
	}
	require.Len(t, f.copies, 2)
	require.Equal(t, "/", f.copies[1].dir)
	require.Equal(t, "etc/cloudflared/token", f.copies[1].file)
	require.Equal(t, testToken, f.copies[1].content)
	require.Equal(t, int64(0o400), f.copies[1].mode)

	for _, l := range logged {
		require.NotContains(t, l, testToken)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	f := newFake()
	m := newManager(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)
	before := len(f.events)

	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOnline, st.State)
	require.Equal(t, []string{"network"}, f.verbs()[before:], "an unchanged connector is left alone")
	require.Len(t, f.copies, 2, "the token is not rewritten")

	// A stopped container with the right spec is started again, not recreated.
	f.set(func(c *docker.ContainerInfo) { c.Running, c.State = false, "exited" })
	st = m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOnline, st.State)
	require.Len(t, f.created, 1)
	require.Contains(t, f.verbs()[before:], "start")
}

func TestEnsureRecreatesForAnotherTunnel(t *testing.T) {
	f := newFake()
	m := newManager(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)

	other := agentprotocol.TunnelSpec{TunnelID: "6b1b2c3d-0000-4000-8000-123456789abc"}
	st := m.Ensure(context.Background(), other, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOnline, st.State)
	require.Len(t, f.created, 2)
	require.Equal(t, other.TunnelID, f.created[1].Labels[agentprotocol.LabelTunnel])
	require.Contains(t, f.verbs(), "stop")
	require.Contains(t, f.verbs(), "remove")
}

func TestEnsureReportsConnectingWhenNotReady(t *testing.T) {
	f := newFake()
	m := newManager(f)
	m.Ready = readyFn(http.StatusServiceUnavailable, `{"status":503,"readyConnections":0}`)
	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateProvisioning, st.State)
	require.Equal(t, 0, st.Connections)
	require.NotEmpty(t, st.Message)
}

func TestEnsureFallsBackToDockerHealth(t *testing.T) {
	f := newFake()
	f.edgeIP = "" // a host that cannot route to container IPs
	f.health = "healthy"
	m := newManager(f)
	m.Ready = func(context.Context, string) (int, []byte, error) { return 0, nil, errors.New("unreachable") }
	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOnline, st.State)
	require.Equal(t, 0, st.Connections)
}

func TestEnsureRestartLoopNeedsAttention(t *testing.T) {
	f := newFake()
	f.logs = "2026-10-26T10:00:00.000000000Z ERR Unauthorized: Invalid tunnel secret\n"
	m := newManager(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)

	f.set(func(c *docker.ContainerInfo) { c.Running, c.Restarting, c.State = false, true, "restarting" })
	st := m.Status(context.Background())
	require.NotNil(t, st)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Contains(t, st.Message, "Invalid tunnel secret")
}

func TestEnsureInvalidInputTouchesNothing(t *testing.T) {
	f := newFake()
	m := newManager(f)
	st := m.Ensure(context.Background(), spec, "", nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	st = m.Ensure(context.Background(), agentprotocol.TunnelSpec{TunnelID: "x"}, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Empty(t, f.events)
}

func TestEnsureRedactsTheToken(t *testing.T) {
	f := newFake()
	f.createErr = errors.New("daemon rejected config " + testToken)
	m := newManager(f)
	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.NotContains(t, st.Message, testToken)
	require.Contains(t, st.Message, "••••••")
}

func TestRemove(t *testing.T) {
	f := newFake()
	m := newManager(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)

	st := m.Remove(context.Background(), nil)
	require.Equal(t, agentprotocol.TunnelStateRemoved, st.State)
	require.Empty(t, f.containers)
	require.Nil(t, m.Status(context.Background()))

	// Removing again is a no-op that still succeeds.
	require.Equal(t, agentprotocol.TunnelStateRemoved, m.Remove(context.Background(), nil).State)
}

func TestStatusNilWithoutContainerAndCached(t *testing.T) {
	f := newFake()
	m := newManager(f)
	require.Nil(t, m.Status(context.Background()))

	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)
	calls := 0
	m.Ready = func(context.Context, string) (int, []byte, error) {
		calls++
		return http.StatusOK, []byte(`{"readyConnections":2}`), nil
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	first := m.Status(context.Background())
	require.Equal(t, 4, first.Connections, "served from the cache filled by Ensure")
	require.Zero(t, calls)

	now = now.Add(statusEvery + time.Second)
	second := m.Status(context.Background())
	require.Equal(t, 2, second.Connections)
	require.Equal(t, 1, calls)
	m.Status(context.Background())
	require.Equal(t, 1, calls, "cached for statusEvery")

	f.set(func(c *docker.ContainerInfo) { c.Running, c.State = false, "exited" })
	now = now.Add(time.Second)
	require.Equal(t, agentprotocol.TunnelStateOffline, m.Status(context.Background()).State)
}
