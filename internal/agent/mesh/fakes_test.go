package mesh

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Fixed ids: server-node ids and link ids are UUIDs; Short() is the first 8.
const (
	srvID  = "5e5e5e5e-1111-2222-3333-444444444444"
	linkID = "1a1a1a1a-1111-2222-3333-444444444444"
	remote = "7b7b7b7b-1111-2222-3333-444444444444"
	envA   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	envB   = "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	svcPG  = "99999999-1111-2222-3333-444444444444"
	svcAPI = "88888888-1111-2222-3333-444444444444"

	testKey = "tskey-auth-MESHSECRET-1234567890"

	statusOnline = `{"BackendState":"Running","Self":{"ID":"nNODE1CNTRL","DNSName":"nc-hetzner.tail1234.ts.net.","TailscaleIPs":["100.64.0.7","fd7a:115c:a1e0::7"]},` +
		`"Peer":{"k1":{"HostName":"nc-other","DNSName":"nc-other.tail1234.ts.net.","TailscaleIPs":["100.64.0.9"],"Tags":["tag:nebula-mesh"],"Online":true,"CurAddr":"1.2.3.4:41641"},` +
		`"k2":{"HostName":"laptop","DNSName":"laptop.tail1234.ts.net.","TailscaleIPs":["100.64.0.2"],"Tags":["tag:other"],"Online":true}}}`
	statusLogin = `{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/xyz"}`
)

type copyRec struct {
	id, dir, file, content string
}

// fakeEngine implements the part of docker.Engine the mesh manager uses; any
// other call panics through the nil embedded interface.
type fakeEngine struct {
	docker.Engine

	mu         sync.Mutex
	containers map[string]*docker.ContainerInfo // by name
	volumes    map[string]bool
	networks   map[string]bool
	events     []string // ordered "verb arg" log
	copies     []copyRec
	created    []docker.ContainerSpec
	pulls      int
	seq        int

	statusJSON string
	pingOut    string
	pingCode   int
	execs      int
	createErr  error
	startErr   error
	logs       string
}

func newFake(status string) *fakeEngine {
	return &fakeEngine{
		containers: map[string]*docker.ContainerInfo{}, volumes: map[string]bool{}, networks: map[string]bool{},
		statusJSON: status, pingOut: "pong from nc-other (100.64.0.9) via 1.2.3.4:41641 in 3ms\n",
		logs: "2026-10-26T10:00:00.000000000Z tailscaled: control: not logged in\n",
	}
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
	if !f.networks[name] {
		f.networks[name] = true
		f.log("network %s", name)
	}
	return nil
}

func (f *fakeEngine) EnsureInternalNetwork(_ context.Context, name string, _ map[string]string, _ ...docker.NetworkOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.networks[name] {
		f.networks[name] = true
		f.log("network-internal %s", name)
	}
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

func (f *fakeEngine) ImagePull(context.Context, string, string, func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls++
	return nil
}

func (f *fakeEngine) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.seq++
	id := fmt.Sprintf("id-%d-%s", f.seq, spec.Name)
	nets := map[string]docker.ContainerNetwork{}
	for i, n := range spec.Networks {
		nets[n.Network] = docker.ContainerNetwork{IP: fmt.Sprintf("10.%d.0.%d", i, f.seq), Aliases: append([]string(nil), n.Aliases...)}
	}
	f.created = append(f.created, spec)
	f.containers[spec.Name] = &docker.ContainerInfo{
		ID: id, Name: spec.Name, Labels: spec.Labels, Env: spec.Env, State: "created", Networks: nets,
	}
	f.log("create %s", spec.Name)
	return id, nil
}

func (f *fakeEngine) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	if f.startErr != nil {
		return f.startErr
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
	out := *c
	out.Networks = map[string]docker.ContainerNetwork{}
	for k, v := range c.Networks {
		out.Networks[k] = docker.ContainerNetwork{IP: v.IP, Aliases: append([]string(nil), v.Aliases...)}
	}
	return out, nil
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeEngine) NetworkConnect(_ context.Context, network, container string, aliases []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.networks[network] {
		return notFound("network " + network)
	}
	c := f.find(container)
	if c == nil {
		return notFound(container)
	}
	if c.Networks == nil {
		c.Networks = map[string]docker.ContainerNetwork{}
	}
	c.Networks[network] = docker.ContainerNetwork{IP: fmt.Sprintf("10.9.0.%d", len(c.Networks)+1), Aliases: append([]string(nil), aliases...)}
	f.log("connect %s %s %s", network, c.Name, strings.Join(aliases, ","))
	return nil
}

func (f *fakeEngine) NetworkDisconnect(_ context.Context, network, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(container)
	if c == nil {
		return notFound(container)
	}
	delete(c.Networks, network)
	f.log("disconnect %s %s", network, c.Name)
	return nil
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
	switch {
	case len(argv) >= 3 && strings.Join(argv[:3], " ") == "tailscale status --json":
		_, _ = io.WriteString(stdout, f.statusJSON)
		return 0, nil
	case len(argv) >= 2 && argv[0] == "tailscale" && argv[1] == "ping":
		_, _ = io.WriteString(stdout, f.pingOut)
		return f.pingCode, nil
	}
	return 1, errors.New("unexpected exec " + strings.Join(argv, " "))
}

func (f *fakeEngine) ContainerLogs(context.Context, string, docker.LogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

// index returns the position of the first event with the prefix, or -1.
func (f *fakeEngine) index(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, e := range f.events {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

// count returns how many events start with prefix.
func (f *fakeEngine) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// mark clears the event log (the next assertions look at what happens next).
func (f *fakeEngine) mark() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events, f.copies, f.created = nil, nil, nil
}

func (f *fakeEngine) lastServe() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.copies) - 1; i >= 0; i-- {
		if f.copies[i].file == serveFile {
			return f.copies[i].content
		}
	}
	return ""
}

// seed adds a container that is not ours (or a stray node) to the fake daemon.
func (f *fakeEngine) seed(name string, running bool, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.containers[name] = &docker.ContainerInfo{ID: fmt.Sprintf("seed-%d", f.seq), Name: name, Running: running, Labels: labels,
		State: map[bool]string{true: "running", false: "exited"}[running]}
}

func (f *fakeEngine) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.find(name) != nil
}

func newManager(f *fakeEngine) *Manager {
	m := New(f, nil)
	m.WaitRunning, m.Poll = 40*time.Millisecond, time.Millisecond
	m.TunDevice = func() bool { return true }
	return m
}

func serverSpec() agentprotocol.MeshNodeSpec {
	return agentprotocol.MeshNodeSpec{
		NodeID: srvID, Kind: agentprotocol.MeshKindServer, Provider: agentprotocol.TailnetProviderTailscale,
		Hostname: "nc-hetzner", Tags: []string{"tag:nebula-mesh"}, IdentityHash: "id1",
		Exposures: []agentprotocol.MeshExposure{
			{ServiceID: svcPG, ServiceSlug: "postgres", EnvironmentID: envA, Purpose: agentprotocol.MeshPurposeReplication, ContainerPort: 5432, MeshPort: 15001},
			{ServiceID: svcAPI, ServiceSlug: "api", EnvironmentID: envA, Purpose: agentprotocol.MeshPurposeService, ContainerPort: 8080, MeshPort: 8080},
		},
	}
}

func linkSpec() agentprotocol.MeshNodeSpec {
	return agentprotocol.MeshNodeSpec{
		NodeID: linkID, Kind: agentprotocol.MeshKindLink, Provider: agentprotocol.TailnetProviderHeadscale,
		LoginServer: "https://hs.example.com", Hostname: "nc-aaaaaaaa-web-to-db", Tags: []string{"tag:nebula-mesh"},
		EnvironmentID: envA, RemoteServerID: remote, TargetIP: "100.64.0.9",
		Aliases: []string{"postgres", "api"}, HasInternalNet: true,
	}
}

func provision(s agentprotocol.MeshNodeSpec) agentprotocol.MeshNodeSpec {
	s.Provision = true
	return s
}

func keysFor(ids ...string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		out[agentprotocol.SecretMeshKeyPrefix+id] = testKey
	}
	return out
}

func sync1(t *testing.T, m *Manager, keys map[string]string, logf func(string), nodes ...agentprotocol.MeshNodeSpec) agentprotocol.MeshStatus {
	t.Helper()
	return m.Sync(context.Background(), agentprotocol.SyncMeshPayload{Nodes: nodes}, keys, logf)
}

// pipeDial is a Dial that answers with a fixed error or a pipe.
func fakeDial(err error) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		if err != nil {
			return nil, err
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
}
