package volbackup

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeDocker is a scriptable Docker for the runner: containers, volumes, an
// ordered event log and a ContainerExec that plays restic and the copy script.
type fakeDocker struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*docker.ContainerInfo
	volumes    map[string]map[string]string
	events     []string
	created    []docker.ContainerSpec
	pulled     []string
	hasImage   bool

	// Script.
	repoExists  bool          // `restic cat config` succeeds
	reflink     bool          // the reflink probe succeeds
	copyDelay   time.Duration // how long the copy takes
	copyExit    int
	backupExit  int
	backupOut   string // stdout of restic backup (default: one summary line)
	forgetOut   string // stdout of `restic forget --dry-run --json`
	snapshots   []string
	snapshotsAf []string // snapshots after the forget (nil: unchanged)
	snapCalls   int
	unpauseErr  error
	hookExit    map[string]int // container name -> exit code of non-restic execs
	hookOut     string
	diskProbe   string
	lsOut       string
	dumpOut     string
	restoreExit int
	envs        [][]string
}

func newFake() *fakeDocker {
	return &fakeDocker{
		containers: map[string]*docker.ContainerInfo{}, volumes: map[string]map[string]string{},
		repoExists: true, reflink: true, hasImage: true, hookExit: map[string]int{},
		diskProbe: "100 100000",
	}
}

func (f *fakeDocker) log(format string, a ...any) {
	f.events = append(f.events, fmt.Sprintf(format, a...))
}

func (f *fakeDocker) eventLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// has reports whether an event with the given prefix was logged.
func (f *fakeDocker) has(prefix string) bool {
	for _, e := range f.eventLog() {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeDocker) index(prefix string) int {
	for i, e := range f.eventLog() {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
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

// addService seeds a running service container.
func (f *fakeDocker) addService(name, serviceID string, running bool) *docker.ContainerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	c := &docker.ContainerInfo{
		ID: fmt.Sprintf("svc%03d", f.seq), Name: name, State: "exited",
		Labels: map[string]string{agentprotocol.LabelManaged: "true", agentprotocol.LabelService: serviceID},
	}
	if running {
		c.State, c.Running = "running", true
	}
	f.containers[c.ID] = c
	return c
}

func (f *fakeDocker) state(id string) docker.ContainerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.find(id)
}

func (f *fakeDocker) ImageInspect(_ context.Context, ref string) (docker.ImageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasImage {
		return docker.ImageInfo{ID: "sha256:restic"}, nil
	}
	return docker.ImageInfo{}, fmt.Errorf("%w: %s", docker.ErrNotFound, ref)
}

func (f *fakeDocker) ImagePull(_ context.Context, ref, _ string, _ func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	f.hasImage = true
	return nil
}

func (f *fakeDocker) EnsureVolume(_ context.Context, name string, labels map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.volumes[name]; !ok {
		f.volumes[name] = labels
		f.log("volume create %s", name)
	}
	return nil
}

func (f *fakeDocker) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.volumes[name]; !ok {
		return fmt.Errorf("%w: volume %s", docker.ErrNotFound, name)
	}
	delete(f.volumes, name)
	f.log("volume remove %s", name)
	return nil
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
	id := fmt.Sprintf("hlp%03d", f.seq)
	f.created = append(f.created, spec)
	f.containers[id] = &docker.ContainerInfo{ID: id, Name: spec.Name, Image: spec.Image, Labels: spec.Labels, State: "created"}
	f.log("create %s", spec.Name)
	return id, nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("%w: %s", docker.ErrNotFound, id)
	}
	c.State, c.Running = "running", true
	f.log("start %s", c.Name)
	return nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, id string, _ *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("%w: %s", docker.ErrNotFound, id)
	}
	c.State, c.Running = "exited", false
	f.log("stop %s", c.Name)
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("%w: %s", docker.ErrNotFound, id)
	}
	delete(f.containers, c.ID)
	f.log("remove %s", c.Name)
	return nil
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return docker.ContainerInfo{}, fmt.Errorf("%w: %s", docker.ErrNotFound, id)
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
	return out, nil
}

func (f *fakeDocker) ContainerPause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("%w: %s", docker.ErrNotFound, id)
	}
	c.State = "paused"
	f.log("pause %s", c.Name)
	return nil
}

func (f *fakeDocker) ContainerUnpause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return fmt.Errorf("%w: %s", docker.ErrNotFound, id)
	}
	if f.unpauseErr != nil {
		return f.unpauseErr
	}
	c.State = "running"
	f.log("unpause %s", c.Name)
	return nil
}

func summaryLine(id string) string {
	return fmt.Sprintf(`{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":10,"data_added":2048,"total_bytes_processed":4096,"snapshot_id":%q}`+"\n", id)
}

func (f *fakeDocker) ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	f.mu.Lock()
	c := f.find(id)
	if c == nil {
		f.mu.Unlock()
		return -1, fmt.Errorf("%w: %s", docker.ErrNotFound, id)
	}
	if c.State == "paused" {
		f.mu.Unlock()
		return -1, fmt.Errorf("Container %s is paused, unpause the container before exec", c.Name)
	}
	name, image := c.Name, c.Image
	f.mu.Unlock()

	if image != agentprotocol.ResticImage {
		f.mu.Lock()
		f.log("hook %s %s", name, strings.Join(argv, " "))
		code := f.hookExit[name]
		out := f.hookOut
		f.mu.Unlock()
		_, _ = io.WriteString(stdout, out)
		return code, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(argv, " ")
	sub := ""
	for _, a := range argv[1:] {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			sub = a
			break
		}
	}
	switch {
	case argv[0] == "sh" && strings.Contains(joined, "cp --reflink=always"):
		f.log("probe reflink")
		if f.reflink {
			return 0, nil
		}
		return 1, nil
	case argv[0] == "sh" && strings.Contains(joined, "du -sk"):
		f.log("probe disk")
		_, _ = io.WriteString(stdout, f.diskProbe+"\n")
		return 0, nil
	case argv[0] == "sh" && strings.Contains(joined, "cp -a --reflink=auto"):
		f.log("copy start")
		f.mu.Unlock()
		select {
		case <-time.After(f.copyDelay):
		case <-ctx.Done():
			f.mu.Lock()
			f.log("copy canceled")
			return -1, ctx.Err()
		}
		f.mu.Lock()
		f.log("copy done")
		return f.copyExit, nil
	case argv[0] == "sh" && strings.Contains(joined, "find /restore"):
		f.log("clear restore")
		return 0, nil
	case argv[0] != "restic":
		f.log("exec %s", joined)
		return 0, nil
	}
	f.log("restic %s", strings.Join(argv[1:], " "))
	f.envs = append(f.envs, append([]string(nil), spec.Env...))
	switch sub {
	case "cat":
		if f.repoExists {
			return 0, nil
		}
		_, _ = io.WriteString(stderr, "Fatal: repository does not exist")
		return exitNoRepo, nil
	case "backup":
		out := f.backupOut
		if out == "" {
			out = summaryLine(strings.Repeat("ab", 32))
		}
		_, _ = io.WriteString(stdout, out)
		return f.backupExit, nil
	case "forget":
		if contains(argv, "--dry-run") {
			_, _ = io.WriteString(stdout, f.forgetOut)
		}
		return 0, nil
	case "restore":
		return f.restoreExit, nil
	case "ls":
		_, _ = io.WriteString(stdout, f.lsOut)
		return 0, nil
	case "dump":
		_, _ = io.WriteString(stdout, f.dumpOut)
		return 0, nil
	case "snapshots":
		list := f.snapshots
		if f.snapCalls > 0 && f.snapshotsAf != nil {
			list = f.snapshotsAf
		}
		f.snapCalls++
		var parts []string
		for _, s := range list {
			parts = append(parts, fmt.Sprintf(`{"id":%q}`, s))
		}
		_, _ = io.WriteString(stdout, "["+strings.Join(parts, ",")+"]")
		return 0, nil
	}
	return 0, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// fakeHost implements Host.
type fakeHost struct {
	d       *fakeDocker
	service string // service id
	self    string
	busy    bool
	locks   int
}

func (h *fakeHost) LockService(ctx context.Context, _ string, _ time.Duration) (func(), error) {
	if h.busy {
		return nil, ErrBusy
	}
	h.d.mu.Lock()
	h.locks++
	h.d.log("lock")
	h.d.mu.Unlock()
	return func() {
		h.d.mu.Lock()
		h.d.log("unlock")
		h.d.mu.Unlock()
	}, nil
}

func (h *fakeHost) ServiceContainer(ctx context.Context, serviceID string) (docker.ContainerInfo, bool) {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	for _, c := range h.d.containers {
		if c.Labels[agentprotocol.LabelService] == serviceID && c.Labels[agentprotocol.LabelManaged] == "true" {
			return *c, true
		}
	}
	return docker.ContainerInfo{}, false
}

func (h *fakeHost) SelfContainer(context.Context) string { return h.self }
