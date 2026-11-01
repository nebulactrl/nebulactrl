package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fleetEngine is a docker.Engine fake that keeps several named containers
// (the proxy and the tailnet node), records every lifecycle call in order and
// can fail starts and creates on demand. Embedding the interface makes any
// call the manager is not expected to make panic.
type fleetEngine struct {
	docker.Engine
	mu      sync.Mutex
	next    int
	ctrs    map[string]*fleetCtr // by name
	volumes map[string]bool

	log     []string // "create:<name>", "start:<name>", "remove:<name>", "volume+:<v>", "volume-:<v>", "pull:<ref>"
	creates []docker.ContainerSpec

	// startErr fails ContainerStart of a created container; nil = success.
	startErr func(spec docker.ContainerSpec) error
	// copies is the last archive copied into each container, by name.
	copies map[string][]byte
	// createErr fails ContainerCreate.
	createErr func(spec docker.ContainerSpec) error
	// execFn answers ContainerExec; nil = exit 0 with empty output.
	execFn func(id string, argv []string) (stdout string, code int, err error)
	// pulled images count as present.
	haveImages map[string]bool
}

type fleetCtr struct {
	info docker.ContainerInfo
	spec docker.ContainerSpec
}

func newFleetEngine() *fleetEngine {
	return &fleetEngine{ctrs: map[string]*fleetCtr{}, volumes: map[string]bool{}, haveImages: map[string]bool{}}
}

func (f *fleetEngine) record(s string) { f.log = append(f.log, s) }

func (f *fleetEngine) find(nameOrID string) *fleetCtr {
	if c, ok := f.ctrs[nameOrID]; ok {
		return c
	}
	for _, c := range f.ctrs {
		if c.info.ID == nameOrID {
			return c
		}
	}
	return nil
}

func (f *fleetEngine) EnsureNetwork(context.Context, string, map[string]string, ...docker.NetworkOption) error {
	return nil
}

func (f *fleetEngine) NetworkList(context.Context, string, map[string]string) ([]docker.NetworkInfo, error) {
	return nil, nil
}

func (f *fleetEngine) EnsureVolume(_ context.Context, name string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.volumes[name] {
		f.volumes[name] = true
		f.record("volume+:" + name)
	}
	return nil
}

func (f *fleetEngine) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.volumes[name] {
		return fmt.Errorf("volume %s: %w", name, docker.ErrNotFound)
	}
	delete(f.volumes, name)
	f.record("volume-:" + name)
	return nil
}

func (f *fleetEngine) ImageInspect(_ context.Context, ref string) (docker.ImageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(ref, "traefik:") || f.haveImages[ref] {
		return docker.ImageInfo{}, nil
	}
	return docker.ImageInfo{}, fmt.Errorf("image %s: %w", ref, docker.ErrNotFound)
}

func (f *fleetEngine) ImagePull(_ context.Context, ref, _ string, _ func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.haveImages[ref] = true
	f.record("pull:" + ref)
	return nil
}

func (f *fleetEngine) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		if err := f.createErr(spec); err != nil {
			return "", err
		}
	}
	if _, dup := f.ctrs[spec.Name]; dup {
		return "", fmt.Errorf("conflict: container name %q is already in use", spec.Name)
	}
	f.next++
	id := fmt.Sprintf("id%d", f.next)
	nets := map[string]docker.ContainerNetwork{}
	for _, n := range spec.Networks {
		nets[n.Network] = docker.ContainerNetwork{IP: "10.0.0." + fmt.Sprint(f.next+1), Aliases: n.Aliases}
	}
	f.ctrs[spec.Name] = &fleetCtr{spec: spec, info: docker.ContainerInfo{
		ID: id, Name: spec.Name, Image: spec.Image, Labels: spec.Labels, RestartMode: spec.RestartPolicy, State: "created",
		Networks: nets, Ports: spec.Ports, NetworkMode: spec.NetworkMode, ExtraHosts: spec.ExtraHosts, DNS: spec.DNS,
	}}
	f.creates = append(f.creates, spec)
	f.record("create:" + spec.Name)
	return id, nil
}

func (f *fleetEngine) CopyToContainer(_ context.Context, id, _ string, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(id); c != nil {
		raw, _ := io.ReadAll(r)
		if f.copies == nil {
			f.copies = map[string][]byte{}
		}
		f.copies[c.spec.Name] = raw
	}
	return nil
}

func (f *fleetEngine) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("container %s: %w", id, docker.ErrNotFound)
	}
	if f.startErr != nil {
		if err := f.startErr(c.spec); err != nil {
			return err
		}
	}
	c.info.State, c.info.Running = "running", true
	f.record("start:" + c.spec.Name)
	return nil
}

func (f *fleetEngine) ContainerRemove(_ context.Context, id string, _, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("container %s: %w", id, docker.ErrNotFound)
	}
	delete(f.ctrs, c.spec.Name)
	f.record("remove:" + c.spec.Name)
	return nil
}

func (f *fleetEngine) ContainerInspect(_ context.Context, nameOrID string) (docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(nameOrID)
	if c == nil {
		return docker.ContainerInfo{}, fmt.Errorf("container %s: %w", nameOrID, docker.ErrNotFound)
	}
	return c.info, nil
}

func (f *fleetEngine) ContainerExec(_ context.Context, id string, argv []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
	f.mu.Lock()
	fn := f.execFn
	f.mu.Unlock()
	if fn == nil {
		return 0, nil
	}
	out, code, err := fn(id, argv)
	if stdout != nil {
		_, _ = io.WriteString(stdout, out)
	}
	return code, err
}

func (f *fleetEngine) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.log {
		if l == prefix {
			n++
		}
	}
	return n
}

func (f *fleetEngine) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fleetEngine) container(name string) (docker.ContainerInfo, docker.ContainerSpec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.ctrs[name]
	if c == nil {
		return docker.ContainerInfo{}, docker.ContainerSpec{}, false
	}
	return c.info, c.spec, true
}

func (f *fleetEngine) hasVolume(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumes[name]
}

// newFleetManager is a Manager over a fleetEngine and the in-process Traefik.
func newFleetManager(t *testing.T, st RouteStore, mut func(*Config)) (*Manager, *fleetEngine, *fakeTraefik, *bytes.Buffer) {
	t.Helper()
	eng := newFleetEngine()
	logs := &bytes.Buffer{}
	cfg := Config{
		Engine: eng, Store: st, HTTPPort: 8880, HTTPSPort: 8443, HTTPClient: &http.Client{},
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mut != nil {
		mut(&cfg)
	}
	m, err := NewManager(cfg)
	require.NoError(t, err)
	tr := &fakeTraefik{user: AdminUser, password: m.adminSecret}
	m.http = &http.Client{Transport: tr}
	return m, eng, tr, logs
}

func tcpRoute(id string, port int) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Upstreams: []string{"192.168.1.50:5432"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: port, AllowCIDRs: []string{"10.0.0.0/8"}, Kind: agentprotocol.RouteKindExternal},
	}
}

func udpRoute(id string, port int) agentprotocol.Route {
	return agentprotocol.Route{
		ServiceID: id, Upstreams: []string{"192.168.1.50:53"},
		RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoUDP, ListenPort: port, Kind: agentprotocol.RouteKindExternal},
	}
}

func portsOf(spec docker.ContainerSpec) []string {
	var out []string
	for _, p := range spec.Ports {
		out = append(out, fmt.Sprintf("%d->%d/%s", p.HostPort, p.ContainerPort, p.Proto))
	}
	return out
}
