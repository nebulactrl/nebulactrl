package tailnet

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const testKey = "tskey-auth-SECRETKEY-1234567890"

const (
	statusOnline = `{"BackendState":"Running","Self":{"ID":"nNODE1CNTRL","DNSName":"web-prod.tail1234.ts.net.","TailscaleIPs":["100.64.0.7","fd7a:115c:a1e0::7"]}}`
	statusLogin  = `{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/xyz"}`
)

// fakeEngine implements the part of docker.Engine the manager uses; any other
// call panics through the nil embedded interface.
type fakeEngine struct {
	docker.Engine

	mu         sync.Mutex
	containers map[string]*docker.ContainerInfo // by name
	volumes    map[string]bool
	events     []string // ordered "verb arg" log
	copies     []copyRec
	created    []docker.ContainerSpec
	statusJSON string
	execs      int
	createErr  error
	logs       string
}

type copyRec struct {
	id, dir string
	file    string
	content string
}

func newFake(status string) *fakeEngine {
	return &fakeEngine{containers: map[string]*docker.ContainerInfo{}, volumes: map[string]bool{}, statusJSON: status,
		logs: "2026-10-26T10:00:00.000000000Z tailscaled: control: not logged in\n"}
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

func (f *fakeEngine) EnsureVolume(_ context.Context, name string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[name] = true
	f.log("volume+ %s", name)
	return nil
}

func (f *fakeEngine) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.volumes[name] {
		return notFound(name)
	}
	delete(f.volumes, name)
	f.log("volume- %s", name)
	return nil
}

func (f *fakeEngine) ImageInspect(context.Context, string) (docker.ImageInfo, error) {
	return docker.ImageInfo{ID: "sha256:ts"}, nil
}

func (f *fakeEngine) ImagePull(context.Context, string, string, func(string)) error { return nil }

func (f *fakeEngine) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created = append(f.created, spec)
	f.containers[spec.Name] = &docker.ContainerInfo{ID: "id-" + spec.Name, Name: spec.Name, Labels: spec.Labels, Env: spec.Env, State: "created"}
	f.log("create %s", spec.Name)
	return "id-" + spec.Name, nil
}

func (f *fakeEngine) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	c.Running, c.State = true, "running"
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

func (f *fakeEngine) ContainerList(_ context.Context, flt docker.ListFilter) ([]docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.ContainerInfo
	for _, c := range f.containers {
		if !flt.All && !c.Running {
			continue
		}
		ok := true
		for k, v := range flt.Labels {
			if got, has := c.Labels[k]; !has || (v != "" && got != v) {
				ok = false
			}
		}
		if ok {
			out = append(out, *c)
		}
	}
	return out, nil
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
		f.copies = append(f.copies, copyRec{id: id, dir: dir, file: h.Name, content: string(b)})
		f.log("copy %s %s/%s", id, dir, h.Name)
	}
	return nil
}

func (f *fakeEngine) ContainerExec(_ context.Context, _ string, argv []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs++
	if strings.Join(argv, " ") != "tailscale status --json --peers=false" {
		return 1, errors.New("unexpected exec " + strings.Join(argv, " "))
	}
	_, _ = io.WriteString(stdout, f.statusJSON)
	return 0, nil
}

func (f *fakeEngine) ContainerLogs(context.Context, string, docker.LogOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

func (f *fakeEngine) index(prefix string) int {
	for i, e := range f.events {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

func newManager(f *fakeEngine) *Manager {
	m := New(f, nil)
	m.WaitRunning, m.Poll = 40*time.Millisecond, time.Millisecond
	return m
}

func provisionSpec() agentprotocol.ServiceTailnetSpec {
	s := baseSpec()
	s.Provision = true
	s.TCP = []agentprotocol.TailnetTCP{{Listen: 5432, Target: 5432}}
	return s
}

func TestProvisionCreatesNodeWithKeyAndServeBeforeStart(t *testing.T) {
	f := newFake(statusOnline)
	var logged []string
	st := newManager(f).Ensure(context.Background(), provisionSpec(), testKey, func(s string) { logged = append(logged, s) })

	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.Equal(t, "web-prod.tail1234.ts.net", st.FQDN, "the name is read back, never assumed; trailing dot trimmed")
	require.Equal(t, []string{"100.64.0.7", "fd7a:115c:a1e0::7"}, st.IPs)
	require.Equal(t, "nNODE1CNTRL", st.DeviceID)
	require.True(t, st.Provisioned)
	require.Equal(t, "abc", st.IdentityHash)

	require.Len(t, f.created, 1)
	require.Contains(t, f.created[0].Env, "TS_AUTHKEY="+testKey)
	require.True(t, f.volumes["nc-ts-11111111-state"] && f.volumes["nc-ts-11111111-config"])

	// create → copy serve.json → start: the config is in place at first start.
	c, cp, s := f.index("create "), f.index("copy "), f.index("start ")
	require.True(t, c >= 0 && c < cp && cp < s, "%v", f.events)
	require.Equal(t, "/config", f.copies[0].dir)
	require.Equal(t, "serve.json", f.copies[0].file)
	require.Contains(t, f.copies[0].content, `"TCPForward": "web:5432"`)
	require.Contains(t, f.copies[0].content, "${TS_CERT_DOMAIN}:443")

	for _, l := range logged {
		require.NotContains(t, l, testKey)
	}
	require.NotContains(t, st.Message, testKey)
}

func TestProvisionRecreatesFromScratch(t *testing.T) {
	f := newFake(statusOnline)
	f.containers["nc-ts-11111111"] = &docker.ContainerInfo{ID: "old", Name: "nc-ts-11111111", Running: true, Labels: map[string]string{}}
	f.volumes["nc-ts-11111111-state"], f.volumes["nc-ts-11111111-config"] = true, true

	st := newManager(f).Ensure(context.Background(), provisionSpec(), testKey, nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)

	rm, vs, vc := f.index("remove nc-ts-11111111"), f.index("volume- nc-ts-11111111-state"), f.index("create ")
	require.True(t, rm >= 0 && vs > rm && vc > vs, "old container and state go before the new node is created: %v", f.events)
}

func TestProvisionNeedsAKeyAndErrorsAreRedacted(t *testing.T) {
	f := newFake(statusOnline)
	st := newManager(f).Ensure(context.Background(), provisionSpec(), "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.Contains(t, st.Message, "needs an auth key")
	require.Empty(t, f.created)

	f.createErr = errors.New("docker rejected env TS_AUTHKEY=" + testKey)
	st = newManager(f).Ensure(context.Background(), provisionSpec(), testKey, nil)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.NotContains(t, st.Message, testKey, "the key never leaves the agent through an error")
	require.Contains(t, st.Message, "••••••")
}

func TestInvalidSpecNeverTouchesDocker(t *testing.T) {
	f := newFake(statusOnline)
	s := baseSpec()
	s.Hostname = "Bad Host"
	st := newManager(f).Ensure(context.Background(), s, "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.Empty(t, f.events)
}

func TestExistingNodeOnlyRewritesTheServeConfig(t *testing.T) {
	f := newFake(statusOnline)
	spec := baseSpec() // no provision
	want := ContainerSpec(spec, "")
	f.containers[want.Name] = &docker.ContainerInfo{ID: "id1", Name: want.Name, Running: true, Labels: want.Labels}

	spec.Funnel = true
	st := newManager(f).Ensure(context.Background(), spec, "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.False(t, st.Provisioned)
	require.Empty(t, f.created, "no recreate for an exposure change")
	require.Equal(t, -1, f.index("remove "))
	require.Len(t, f.copies, 1)
	require.Contains(t, f.copies[0].content, "AllowFunnel")
}

func TestChangedIdentityRecreatesTheContainerButKeepsVolumes(t *testing.T) {
	f := newFake(statusOnline)
	f.containers["nc-ts-11111111"] = &docker.ContainerInfo{ID: "old", Name: "nc-ts-11111111", Running: true,
		Labels: map[string]string{agentprotocol.LabelTailnetSpec: "stale"}}
	f.volumes["nc-ts-11111111-state"] = true

	st := newManager(f).Ensure(context.Background(), baseSpec(), "", nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)
	require.Len(t, f.created, 1)
	for _, e := range f.created[0].Env {
		require.False(t, strings.HasPrefix(e, "TS_AUTHKEY="), "an existing identity needs no key")
	}
	require.Equal(t, -1, f.index("volume- "), "the state volume (the device identity) survives")
}

func TestNeedsLoginIsNeedsAttentionWithLogs(t *testing.T) {
	f := newFake(statusLogin)
	st := newManager(f).Ensure(context.Background(), provisionSpec(), testKey, nil)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.Contains(t, st.Message, "Re-provision")
	require.Contains(t, st.Message, "not logged in", "the last log lines explain why")
	require.True(t, st.Provisioned)
}

func TestHeadscaleServesTheRealNameOnceItIsKnown(t *testing.T) {
	f := newFake(`{"BackendState":"Running","Self":{"ID":"7","DNSName":"web-prod.hs.example.com.","TailscaleIPs":["100.64.0.3"]}}`)
	s := provisionSpec()
	s.Provider, s.LoginServer = agentprotocol.TailnetProviderHeadscale, "https://hs.example.com"
	st := newManager(f).Ensure(context.Background(), s, testKey, nil)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State, st.Message)

	require.Len(t, f.copies, 2)
	require.NotContains(t, f.copies[0].content, `"Web"`, "first start: the name is unknown")
	require.Contains(t, f.copies[1].content, `"web-prod.hs.example.com:80"`)
	require.NotContains(t, f.copies[1].content, "HTTPS")
	require.Contains(t, f.created[0].Env, "TS_EXTRA_ARGS=--login-server=https://hs.example.com")
}

func TestRemoveDeletesContainerAndVolumes(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, m.Ensure(context.Background(), provisionSpec(), testKey, nil).State)

	st := m.Remove(context.Background(), svcID, nil)
	require.True(t, st.Removed)
	require.Equal(t, agentprotocol.ServiceTailnetRemoved, st.State)
	require.Empty(t, f.containers)
	require.Empty(t, f.volumes)

	// Removing what is not there is fine.
	require.True(t, m.Remove(context.Background(), svcID, nil).Removed)
	require.Nil(t, m.Observe(context.Background(), svcID))
}

func TestObserve(t *testing.T) {
	f := newFake(statusOnline)
	m := newManager(f)
	require.Nil(t, m.Observe(context.Background(), svcID), "no sidecar, nothing to report")

	want := ContainerSpec(baseSpec(), "")
	c := &docker.ContainerInfo{ID: "id1", Name: want.Name, Running: true, Labels: want.Labels}
	f.containers[want.Name] = c

	st := m.Observe(context.Background(), svcID)
	require.NotNil(t, st)
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State)
	before := f.execs
	m.Observe(context.Background(), svcID)
	require.Equal(t, before, f.execs, "the status is cached between observations")

	// A sidecar that keeps dying (device deleted: tailscaled exits) needs attention.
	c.Running, c.Restarting, c.RestartCount, c.ExitCode, c.State = false, false, 3, 1, "exited"
	st = m.Observe(context.Background(), svcID)
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)
	require.Contains(t, st.Message, "Re-provision")

	// A cleanly stopped one is just offline.
	c.RestartCount, c.ExitCode = 0, 0
	st = m.Observe(context.Background(), svcID)
	require.Equal(t, agentprotocol.ServiceTailnetOffline, st.State)

	// The orphan sweep and replica listings must not see it.
	cs, err := f.ContainerList(context.Background(), docker.ListFilter{All: true, Labels: map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelService: "",
	}})
	require.NoError(t, err)
	require.Empty(t, cs)
}

func TestParseStatus(t *testing.T) {
	st := ParseStatus([]byte(statusOnline))
	require.Equal(t, agentprotocol.ServiceTailnetOnline, st.State)

	st = ParseStatus([]byte(statusLogin))
	require.Equal(t, agentprotocol.ServiceTailnetNeedsAttn, st.State)

	st = ParseStatus([]byte(`{"BackendState":"Stopped"}`))
	require.Equal(t, agentprotocol.ServiceTailnetOffline, st.State)

	st = ParseStatus([]byte(`{"BackendState":"Starting"}`))
	require.Equal(t, agentprotocol.ServiceTailnetProvisioning, st.State)

	st = ParseStatus([]byte(`not json`))
	require.Equal(t, agentprotocol.ServiceTailnetProvisioning, st.State)
}

func TestFakeUsesThePinnedImage(t *testing.T) {
	f := newFake(statusOnline)
	newManager(f).Ensure(context.Background(), provisionSpec(), testKey, nil)
	require.Equal(t, proxy.TailscaleImage, f.created[0].Image)
	var buf bytes.Buffer
	_, _ = buf.WriteString(f.created[0].Name)
	require.Equal(t, "nc-ts-11111111", buf.String())
}
