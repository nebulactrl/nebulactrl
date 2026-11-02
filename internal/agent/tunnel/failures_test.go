package tunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	networkErr, inspectErr, startErr, stopErr, removeErr, pullErr, imageErr, copyErr error
	imagePresent                                                                     bool
	removed                                                                          []string
}

func newFailEngine() *failEngine { return &failEngine{fakeEngine: newFake()} }

func (f *failEngine) EnsureNetwork(ctx context.Context, name string, l map[string]string, o ...docker.NetworkOption) error {
	if f.networkErr != nil {
		return f.networkErr
	}
	return f.fakeEngine.EnsureNetwork(ctx, name, l, o...)
}

func (f *failEngine) ContainerInspect(ctx context.Context, id string) (docker.ContainerInfo, error) {
	if f.inspectErr != nil {
		return docker.ContainerInfo{}, f.inspectErr
	}
	return f.fakeEngine.ContainerInspect(ctx, id)
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

func (f *failEngine) ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error) {
	if f.imageErr != nil {
		return docker.ImageInfo{}, f.imageErr
	}
	if f.imagePresent {
		return docker.ImageInfo{}, nil
	}
	return f.fakeEngine.ImageInspect(ctx, ref)
}

func (f *failEngine) ImagePull(ctx context.Context, ref, auth string, progress func(string)) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	return f.fakeEngine.ImagePull(ctx, ref, auth, progress)
}

func (f *failEngine) CopyToContainer(ctx context.Context, id, dir string, r io.Reader) error {
	if f.copyErr != nil {
		return f.copyErr
	}
	return f.fakeEngine.CopyToContainer(ctx, id, dir, r)
}

func managerFor(f docker.Engine) *Manager {
	m := New(f, nil)
	m.WaitOnline, m.Poll = 30*time.Millisecond, time.Millisecond
	m.Ready = readyFn(http.StatusOK, `{"readyConnections":2}`)
	return m
}

func TestEnsureReportsEveryFailureAsNeedsAttention(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(f *failEngine)
		want    string
	}{
		{"edge network", func(f *failEngine) { f.networkErr = errors.New("net boom") }, "Could not create the edge network: net boom"},
		{"inspect", func(f *failEngine) { f.inspectErr = errors.New("socket closed") }, "Could not inspect the cloudflared container: socket closed"},
		{"image check", func(f *failEngine) { f.imageErr = errors.New("registry down") }, "Could not pull the cloudflared image: registry down"},
		{"image pull", func(f *failEngine) { f.pullErr = errors.New("rate limited") }, "Could not pull the cloudflared image: rate limited"},
		{"create", func(f *failEngine) { f.createErr = errors.New("name in use") }, "Could not create the cloudflared container: name in use"},
		{"token copy", func(f *failEngine) { f.copyErr = errors.New("no space") }, "Could not write the tunnel token: no space"},
		{"first start", func(f *failEngine) { f.startErr = errors.New("oci runtime error") }, "Could not start the cloudflared container: oci runtime error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFailEngine()
			tc.prepare(f)
			st := managerFor(f).Ensure(context.Background(), spec, testToken, nil)
			require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
			require.Equal(t, tc.want, st.Message)
			require.Equal(t, tunnelID, st.TunnelID)
			require.Equal(t, Image, st.Image)
		})
	}
}

func TestEnsureCleansUpAContainerItCouldNotFinishSettingUp(t *testing.T) {
	for name, prepare := range map[string]func(f *failEngine){
		"token copy failed": func(f *failEngine) { f.copyErr = errors.New("no space") },
		"start failed":      func(f *failEngine) { f.startErr = errors.New("cannot start") },
	} {
		f := newFailEngine()
		prepare(f)
		st := managerFor(f).Ensure(context.Background(), spec, testToken, nil)
		require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State, name)
		require.Empty(t, f.containers, "%s: the half-made container must not stay behind", name)
		require.Len(t, f.removed, 1, name)
	}
}

func TestEnsureSkipsThePullWhenTheImageIsPresent(t *testing.T) {
	f := newFailEngine()
	f.imagePresent = true
	st := managerFor(f).Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOnline, st.State, st.Message)
	require.Empty(t, f.pulled)
}

func TestEnsureStartFailureOfAnExistingConnectorDoesNotRecreateIt(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)
	f.set(func(c *docker.ContainerInfo) { c.Running, c.State = false, "exited" })
	f.startErr = errors.New("port is already allocated " + testToken)

	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Contains(t, st.Message, "Could not start the cloudflared container: port is already allocated")
	require.NotContains(t, st.Message, testToken)
	require.Len(t, f.created, 1)
}

func TestEnsureReportsAConnectorThatCannotBeReplaced(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)

	other := agentprotocol.TunnelSpec{TunnelID: "6b1b2c3d-0000-4000-8000-123456789abc"}
	f.stopErr = errors.New("daemon busy")
	st := m.Ensure(context.Background(), other, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Contains(t, st.Message, "stop the cloudflared container: daemon busy")
	require.Len(t, f.created, 1, "the old connector keeps running when it cannot be stopped")

	f.stopErr = nil
	f.removeErr = errors.New("device busy")
	st = m.Ensure(context.Background(), other, testToken, nil)
	require.Contains(t, st.Message, "remove the cloudflared container: device busy")

	// A connector that vanished meanwhile is as good as removed.
	f.removeErr = docker.ErrNotFound
	st = m.Ensure(context.Background(), other, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOnline, st.State, st.Message)
	require.Len(t, f.created, 2)
}

func TestEnsureWaitReportsAConnectorThatCannotBeInspectedOrStaysOffline(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	m.WaitOnline = time.Minute
	f.health = "" // never healthy
	calls := 0
	// The first inspect (before create) is not found; the poll after start fails.
	m.Ready = func(context.Context, string) (int, []byte, error) {
		calls++
		f.inspectErr = errors.New("daemon went away")
		return http.StatusServiceUnavailable, []byte(`{"readyConnections":0}`), nil
	}
	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Equal(t, "Cannot inspect the cloudflared container: daemon went away", st.Message)
	require.Equal(t, 1, calls)
}

func TestEnsureStopsWaitingWhenTheConnectorDies(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	m.WaitOnline = time.Minute
	m.Ready = func(context.Context, string) (int, []byte, error) {
		f.set(func(c *docker.ContainerInfo) { c.Running, c.State, c.ExitCode = false, "exited", 1 })
		return http.StatusServiceUnavailable, []byte(`{"readyConnections":0}`), nil
	}
	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateOffline, st.State, "the wait ends as soon as the container is seen stopped, without waiting a minute")
	require.Equal(t, "The cloudflared container is exited.", st.Message)
}

func TestEnsureStopsWaitingWhenTheAgentStops(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	m.WaitOnline = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	m.Ready = func(context.Context, string) (int, []byte, error) {
		cancel()
		return http.StatusServiceUnavailable, []byte(`{"readyConnections":0}`), nil
	}
	st := m.Ensure(ctx, spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Equal(t, "The agent stopped while waiting for the tunnel.", st.Message)
}

func TestEnsureTimesOutWhileTheConnectorIsStillConnecting(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { cur := now; now = now.Add(time.Second); return cur }
	m.WaitOnline = 3 * time.Second
	m.Ready = func(context.Context, string) (int, []byte, error) { return 0, nil, errors.New("connection refused") }
	f.health = "" // docker has no verdict either
	st := m.Ensure(context.Background(), spec, testToken, nil)
	require.Equal(t, agentprotocol.TunnelStateProvisioning, st.State)
	require.Equal(t, "The connector is starting.", st.Message)
}

func TestObserveUnhealthyConnectorIncludesTheLogTail(t *testing.T) {
	f := newFailEngine()
	f.edgeIP = ""
	f.health = "unhealthy"
	f.logs = "2026-10-26T10:00:00Z INF starting\n2026-10-26T10:00:01Z ERR failed to dial edge\n\n"
	m := managerFor(f)
	m.WaitOnline = time.Millisecond
	m.Ensure(context.Background(), spec, testToken, nil)

	m.forget()
	st := m.Status(context.Background())
	require.NotNil(t, st)
	require.Equal(t, agentprotocol.TunnelStateProvisioning, st.State)
	require.Equal(t, "The connector is not connected to Cloudflare. Last log lines: INF starting | ERR failed to dial edge", st.Message)
}

func TestObserveStoppedContainerIsOfflineAndALoopIsNeedsAttention(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)

	f.set(func(c *docker.ContainerInfo) { c.Running, c.State = false, "exited" })
	m.forget()
	st := m.Status(context.Background())
	require.Equal(t, agentprotocol.TunnelStateOffline, st.State)
	require.Equal(t, "The cloudflared container is exited.", st.Message)

	f.set(func(c *docker.ContainerInfo) { c.RestartCount, c.ExitCode = restartLoopAfter, 1 })
	m.forget()
	st = m.Status(context.Background())
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State, "a stopped container that crashed repeatedly needs attention")
	require.Contains(t, st.Message, "keeps restarting")
}

func TestStatusHidesInspectErrorsAsNoConnector(t *testing.T) {
	f := newFailEngine()
	f.inspectErr = errors.New("socket closed")
	require.Nil(t, managerFor(f).Status(context.Background()))
}

func TestRemoveReportsFailures(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	require.Equal(t, agentprotocol.TunnelStateOnline, m.Ensure(context.Background(), spec, testToken, nil).State)

	f.removeErr = errors.New("device busy")
	st := m.Remove(context.Background(), nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Contains(t, st.Message, "remove the cloudflared container: device busy")
	require.Equal(t, tunnelID, st.TunnelID, "the report names the tunnel whose connector is stuck")

	f.removeErr = nil
	f.inspectErr = errors.New("socket closed")
	st = m.Remove(context.Background(), nil)
	require.Equal(t, agentprotocol.TunnelStateNeedsAttn, st.State)
	require.Contains(t, st.Message, "Could not inspect the cloudflared container: socket closed")

	f.inspectErr = nil
	var logged []string
	st = m.Remove(context.Background(), func(s string) { logged = append(logged, s) })
	require.Equal(t, agentprotocol.TunnelStateRemoved, st.State)
	require.Equal(t, []string{"Tunnel: removed the cloudflared container"}, logged)
}

func TestEnsureLogsProgressWithoutTheToken(t *testing.T) {
	f := newFailEngine()
	m := managerFor(f)
	var logged []string
	m.Ensure(context.Background(), spec, testToken, func(s string) { logged = append(logged, s) })
	require.Equal(t, []string{"Tunnel: connected to Cloudflare (2 connections)"}, logged)

	// Replacing the connector says so.
	other := agentprotocol.TunnelSpec{TunnelID: "6b1b2c3d-0000-4000-8000-123456789abc"}
	logged = nil
	m.Ensure(context.Background(), other, testToken, func(s string) { logged = append(logged, s) })
	require.Equal(t, "Tunnel: recreating the connector (its configuration or tunnel changed)", logged[0])
}

func TestHTTPReadyReadsStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"readyConnections":0}`)
	}))
	defer srv.Close()

	code, body, err := httpReady(context.Background(), srv.URL+"/ready")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.JSONEq(t, `{"readyConnections":0}`, string(body))

	code, _, err = httpReady(context.Background(), srv.URL+"/other")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, code)

	srv.Close()
	_, _, err = httpReady(context.Background(), srv.URL+"/ready")
	require.Error(t, err)
	_, _, err = httpReady(context.Background(), "://bad url")
	require.Error(t, err)
}

func TestRedactOnlyHidesLongEnoughSecrets(t *testing.T) {
	require.Equal(t, "token ••••••", redact("token abcdefgh", "abcdefgh"))
	require.Equal(t, "token abc", redact("token abc", "abc"), "a short value would mask ordinary text")
	require.False(t, strings.Contains(redact(testToken+testToken, testToken), testToken))
}
