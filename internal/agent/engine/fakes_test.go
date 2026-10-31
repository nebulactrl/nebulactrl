package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fake docker ----

// fakeCopy is one CopyToContainer call.
type fakeCopy struct {
	id, dir string
	data    []byte
}

// fakeNet is one network of the fake daemon.
type fakeNet struct {
	internal bool
	labels   map[string]string
	created  time.Time
	subnet   string // IPAM subnet the network was created with ("" = daemon default)
}

type fakeDocker struct {
	mu         sync.Mutex
	hostMem    uint64 // docker info MemTotal; ContainerStats reports 1 GiB limits
	seq        int
	containers map[string]*docker.ContainerInfo // by id
	images     map[string]docker.ImageInfo      // by ref
	logText    map[string]string                // container name -> log output
	pulled     []string
	removed    []string
	started    []string
	stopped    []string
	volsGone   []string
	// volInUse names a volume RemoveVolume refuses, like a daemon whose
	// container still holds it.
	volInUse string
	paused   []string
	unpaused []string
	created  []docker.ContainerSpec
	copies   []fakeCopy
	// ops is every create, start, stop and remove in call order ("stop:nc-web-...");
	// stopGrace is the grace each stop was given.
	ops       []string
	stopGrace map[string]time.Duration

	nets        map[string]*fakeNet // networks by name
	netsEnsured []string            // network names in creation order
	// refuseSubnets are subnets the fake daemon refuses as overlapping a host
	// route (EnsureNetwork with WithSubnet fails with "Pool overlaps").
	refuseSubnets []string
	netsRemoved   []string
	connects      []string // "<network><-<container>"
	disconnects   []string

	startErr func(name string) error
	// onStart runs after a container is started and may change its state.
	onStart func(c *docker.ContainerInfo)
	// onExec, when set, answers ContainerExec calls.
	onExec func(id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error)
	// onStop runs when a container is stopped (the docker mutex is held).
	onStop func(name string)
}

// paused and unpaused record ContainerPause / ContainerUnpause calls.
func (f *fakeDocker) pauseLog() (paused, unpaused []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paused...), append([]string(nil), f.unpaused...)
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: map[string]*docker.ContainerInfo{},
		images:     map[string]docker.ImageInfo{},
		logText:    map[string]string{},
	}
}

func notFound(what string) error { return fmt.Errorf("%w: %s", docker.ErrNotFound, what) }

func (f *fakeDocker) Ping(context.Context) error { return nil }
func (f *fakeDocker) Info(context.Context) (docker.Info, error) {
	return docker.Info{MemTotal: f.hostMem}, nil
}
func (f *fakeDocker) ServerVersion(context.Context) (string, error) { return "29.0", nil }
func (f *fakeDocker) EnsureNetwork(_ context.Context, name string, labels map[string]string, opts ...docker.NetworkOption) error {
	return f.ensureNet(name, labels, false, docker.ResolveNetworkOptions(opts...))
}

func (f *fakeDocker) EnsureInternalNetwork(_ context.Context, name string, labels map[string]string, opts ...docker.NetworkOption) error {
	return f.ensureNet(name, labels, true, docker.ResolveNetworkOptions(opts...))
}

// ensureNet creates the network like the daemon: an existing name is left as
// it is, and a subnet that overlaps another network (or is listed in
// refuseSubnets) is refused as overlapping.
func (f *fakeDocker) ensureNet(name string, labels map[string]string, internal bool, o docker.NetworkOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nets == nil {
		f.nets = map[string]*fakeNet{}
	}
	if _, ok := f.nets[name]; ok {
		return nil
	}
	if o.Subnet != "" {
		want := netip.MustParsePrefix(o.Subnet)
		if slices.Contains(f.refuseSubnets, o.Subnet) {
			return fmt.Errorf("create network %s: Error response from daemon: invalid pool request: Pool overlaps with other one on this address space", name)
		}
		for other, n := range f.nets {
			if n.subnet != "" && netip.MustParsePrefix(n.subnet).Overlaps(want) {
				return fmt.Errorf("create network %s: Error response from daemon: invalid pool request: Pool overlaps with other one on this address space (%s)", name, other)
			}
		}
	}
	f.nets[name] = &fakeNet{internal: internal, labels: labels, created: time.Now(), subnet: o.Subnet}
	f.netsEnsured = append(f.netsEnsured, name)
	return nil
}

func (f *fakeDocker) EnsureVolume(context.Context, string, map[string]string) error { return nil }
func (f *fakeDocker) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.volInUse != "" && name == f.volInUse {
		return fmt.Errorf("remove volume %s: volume is in use", name)
	}
	f.volsGone = append(f.volsGone, name)
	return nil
}

// NetworkConnect joins an existing container to an existing network.
func (f *fakeDocker) NetworkConnect(_ context.Context, network, container string, aliases []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.nets[network]; !ok {
		return notFound("network " + network)
	}
	c := f.find(container)
	if c == nil {
		return notFound(container)
	}
	if c.Networks == nil {
		c.Networks = map[string]docker.ContainerNetwork{}
	}
	c.Networks[network] = docker.ContainerNetwork{IP: "10.9.0." + fmt.Sprint(len(c.Networks)+1), Aliases: aliases}
	f.connects = append(f.connects, network+"<-"+c.Name)
	return nil
}

func (f *fakeDocker) NetworkDisconnect(_ context.Context, network, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(container); c != nil {
		delete(c.Networks, network)
		f.disconnects = append(f.disconnects, network+"<-"+c.Name)
	}
	return nil
}

func (f *fakeDocker) NetworkList(_ context.Context, prefix string, labels map[string]string) ([]docker.NetworkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.NetworkInfo
	for name, n := range f.nets {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		match := true
		for k, v := range labels {
			if n.labels[k] != v {
				match = false
			}
		}
		if !match {
			continue
		}
		ni := docker.NetworkInfo{ID: "net-" + name, Name: name, Internal: n.internal, Labels: n.labels, Created: n.created}
		if n.subnet != "" {
			ni.Subnets = []string{n.subnet}
		}
		for _, c := range f.containers {
			if _, on := c.Networks[name]; on {
				ni.Containers = append(ni.Containers, c.ID)
			}
		}
		out = append(out, ni)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.nets[name]; !ok {
		return notFound("network " + name)
	}
	delete(f.nets, name)
	f.netsRemoved = append(f.netsRemoved, name)
	return nil
}

// networksOf lists the networks of a container by name, sorted.
func (f *fakeDocker) networksOf(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(name)
	if c == nil {
		return nil
	}
	var out []string
	for n := range c.Networks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (f *fakeDocker) ImagePull(_ context.Context, ref, _ string, progress func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	if strings.Contains(ref, "nonexistent") {
		return errors.New("pull access denied")
	}
	if progress != nil {
		progress("downloaded " + ref)
	}
	repo := ref
	if i := strings.IndexAny(repo, ":@"); i > 0 {
		repo = repo[:i]
	}
	f.images[ref] = docker.ImageInfo{ID: "sha256:img-" + ref, RepoDigests: []string{repo + "@sha256:abc123"}}
	return nil
}

func (f *fakeDocker) ImageInspect(_ context.Context, ref string) (docker.ImageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.images[ref]; ok {
		return i, nil
	}
	for _, i := range f.images {
		if i.ID == ref {
			return i, nil
		}
	}
	return docker.ImageInfo{}, notFound("image " + ref)
}

func (f *fakeDocker) ImageRemove(context.Context, string, bool) error { return nil }
func (f *fakeDocker) ImageList(context.Context, string) ([]docker.ImageSummary, error) {
	return nil, nil
}
func (f *fakeDocker) CopyToContainer(_ context.Context, id, dir string, r io.Reader) error {
	b, _ := io.ReadAll(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copies = append(f.copies, fakeCopy{id: id, dir: dir, data: b})
	return nil
}
func (f *fakeDocker) ContainerExec(_ context.Context, id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	if f.onExec != nil {
		return f.onExec(id, argv, spec, stdout, stderr)
	}
	return 0, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, spec docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.Name == spec.Name {
			return "", fmt.Errorf("name %s already in use", spec.Name)
		}
	}
	f.seq++
	id := fmt.Sprintf("ctr%03d", f.seq)
	f.created = append(f.created, spec)
	f.ops = append(f.ops, "create:"+spec.Name)
	f.containers[id] = &docker.ContainerInfo{
		ID: id, Name: spec.Name, Image: spec.Image, ImageID: spec.Image, Labels: spec.Labels, Env: spec.Env,
		State: "created", Created: time.Now(),
		Networks: fakeNetworks(spec, f.seq),
	}
	return id, nil
}

// fakeNetworks is the endpoint map a created container has: one IP per network.
func fakeNetworks(spec docker.ContainerSpec, seq int) map[string]docker.ContainerNetwork {
	out := map[string]docker.ContainerNetwork{}
	for i, n := range spec.Networks {
		out[n.Network] = docker.ContainerNetwork{IP: fmt.Sprintf("10.%d.0.%d", i, seq), Aliases: n.Aliases}
	}
	return out
}

func (f *fakeDocker) find(idOrName string) *docker.ContainerInfo {
	if c, ok := f.containers[idOrName]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.Name == idOrName {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	if f.startErr != nil {
		if err := f.startErr(c.Name); err != nil {
			return err
		}
	}
	f.started = append(f.started, c.Name)
	f.ops = append(f.ops, "start:"+c.Name)
	c.State, c.Running, c.StartedAt = "running", true, time.Now().Add(-time.Minute)
	if f.onStart != nil {
		f.onStart(c)
	}
	return nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, id string, grace *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	f.stopped = append(f.stopped, c.Name)
	f.ops = append(f.ops, "stop:"+c.Name)
	if grace != nil {
		if f.stopGrace == nil {
			f.stopGrace = map[string]time.Duration{}
		}
		f.stopGrace[c.Name] = *grace
	}
	if f.onStop != nil {
		f.onStop(c.Name)
	}
	c.State, c.Running = "exited", false
	return nil
}

// ContainerPause freezes a container like the daemon: it stays Running=true
// with State "paused".
func (f *fakeDocker) ContainerPause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	if !c.Running || c.State == "paused" {
		return fmt.Errorf("container %s is not running", c.Name)
	}
	f.paused = append(f.paused, c.Name)
	c.State = "paused"
	return nil
}

func (f *fakeDocker) ContainerUnpause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	if c.State != "paused" {
		return fmt.Errorf("container %s is not paused", c.Name)
	}
	f.unpaused = append(f.unpaused, c.Name)
	c.State = "running"
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	f.removed = append(f.removed, c.Name)
	f.ops = append(f.ops, "remove:"+c.Name)
	delete(f.containers, c.ID)
	return nil
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return docker.ContainerInfo{}, notFound(id)
	}
	return *c, nil
}

func (f *fakeDocker) ContainerList(_ context.Context, flt docker.ListFilter) ([]docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.ContainerInfo
	for _, c := range f.containers {
		if !flt.All && !c.Running {
			continue
		}
		ok := true
		for k, v := range flt.Labels {
			got, has := c.Labels[k]
			if !has || (v != "" && got != v) {
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

func (f *fakeDocker) ContainerLogs(_ context.Context, id string, _ docker.LogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return nil, notFound(id)
	}
	var sb strings.Builder
	for _, l := range strings.Split(strings.TrimRight(f.logText[c.Name], "\n"), "\n") {
		if l != "" {
			sb.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + " " + l + "\n")
		}
	}
	return io.NopCloser(strings.NewReader(sb.String())), nil
}

func (f *fakeDocker) ContainerStats(_ context.Context, id string) (docker.ContainerStats, error) {
	return docker.ContainerStats{CPUPercent: 12.5, MemBytes: 1 << 20, MemLimit: 1 << 30}, nil
}

// seed adds a container directly (e.g. the "previous release").
func (f *fakeDocker) seed(name, serviceID, deploymentID string, running bool, age time.Duration) *docker.ContainerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("ctr%03d", f.seq)
	c := &docker.ContainerInfo{
		ID: id, Name: name, Image: "old", ImageID: "sha256:old",
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelService: serviceID,
			agentprotocol.LabelDeployment: deploymentID,
		},
		Created: time.Now().Add(-age),
	}
	if running {
		c.State, c.Running, c.StartedAt = "running", true, time.Now().Add(-age)
	} else {
		c.State = "exited"
	}
	f.containers[id] = c
	return c
}

func (f *fakeDocker) state(name string) (docker.ContainerInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(name); c != nil {
		return *c, true
	}
	return docker.ContainerInfo{}, false
}

// ---- fake proxy ----

type fakeProxy struct {
	mu      sync.Mutex
	current []agentprotocol.Route
	applies [][]agentprotocol.Route
	// applyDelay makes every Apply take that long before it lands, like a real
	// proxy waiting for Traefik: Current() keeps answering the old table.
	applyDelay time.Duration
	applyErr   func(n int) error // n is the 1-based Apply call number
	probes     []int             // status per probe call; the last repeats
	probeErr   error
	nProbe     int
	email      string
	tableSeq   uint64 // the persisted route table number (tableSeqKeeper)
}

func (p *fakeProxy) LoadTableSeq() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tableSeq
}

func (p *fakeProxy) SaveTableSeq(n uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tableSeq = n
}

func (p *fakeProxy) Apply(r []agentprotocol.Route) error {
	time.Sleep(p.applyDelay)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applies = append(p.applies, append([]agentprotocol.Route(nil), r...))
	if p.applyErr != nil {
		if err := p.applyErr(len(p.applies)); err != nil {
			return err
		}
	}
	p.current = append([]agentprotocol.Route(nil), r...)
	return nil
}

func (p *fakeProxy) Current() []agentprotocol.Route {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agentprotocol.Route(nil), p.current...)
}

func (p *fakeProxy) Probe(context.Context, string, string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probeErr != nil {
		return 0, p.probeErr
	}
	code := 200
	if len(p.probes) > 0 {
		i := p.nProbe
		if i >= len(p.probes) {
			i = len(p.probes) - 1
		}
		code = p.probes[i]
	}
	p.nProbe++
	return code, nil
}

func (p *fakeProxy) SetACMEEmail(e string) { p.mu.Lock(); p.email = e; p.mu.Unlock() }

// ---- fake sender ----

type fakeSender struct {
	mu       sync.Mutex
	events   []agentprotocol.EventFrame
	logs     []agentprotocol.LogFrame
	logEnds  []agentprotocol.LogEnd
	stats    []agentprotocol.Stats
	observed []agentprotocol.Observed
}

func (s *fakeSender) Event(e agentprotocol.EventFrame) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}
func (s *fakeSender) Log(l agentprotocol.LogFrame) {
	s.mu.Lock()
	s.logs = append(s.logs, l)
	s.mu.Unlock()
}
func (s *fakeSender) LogEnd(l agentprotocol.LogEnd) {
	s.mu.Lock()
	s.logEnds = append(s.logEnds, l)
	s.mu.Unlock()
}
func (s *fakeSender) Stats(st agentprotocol.Stats) {
	s.mu.Lock()
	s.stats = append(s.stats, st)
	s.mu.Unlock()
}
func (s *fakeSender) Observed(o agentprotocol.Observed) {
	s.mu.Lock()
	s.observed = append(s.observed, o)
	s.mu.Unlock()
}

func (s *fakeSender) allLogText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb strings.Builder
	for _, f := range s.logs {
		for _, l := range f.Lines {
			sb.WriteString(l.Text + "\n")
		}
	}
	return sb.String()
}

func (s *fakeSender) eventSteps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		out = append(out, e.Step+":"+e.Status)
	}
	return out
}

// ---- fake builder ----

type fakeBuilder struct {
	mu    sync.Mutex
	calls []build.Request
	out   []string // lines to emit
	err   error
	id    string
}

func (b *fakeBuilder) Build(_ context.Context, req build.Request, logf func(string)) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, req)
	b.mu.Unlock()
	for _, l := range b.out {
		logf(l)
	}
	if b.err != nil {
		return "", b.err
	}
	if b.id == "" {
		return "sha256:built", nil
	}
	return b.id, nil
}
