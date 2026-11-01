package logstore

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeDocker serves container lists and logs like the daemon: a stream returns
// what the container already wrote (lines at or after Since, the boundary line
// included, as Docker does) and, with Follow, whatever it writes next.
type fakeDocker struct {
	docker.Engine // everything else panics, which is the point

	mu         sync.Mutex
	containers []docker.ContainerInfo
	written    map[string][]string // container ID → "<rfc3339nano> text" lines
	followers  map[string][]chan string
	opened     map[string]int
	lastFilter docker.ListFilter
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{written: map[string][]string{}, followers: map[string][]chan string{}, opened: map[string]int{}}
}

func (f *fakeDocker) add(ci docker.ContainerInfo) {
	f.mu.Lock()
	f.containers = append(f.containers, ci)
	f.mu.Unlock()
}

func (f *fakeDocker) write(id string, ts time.Time, text string) {
	line := ts.UTC().Format(time.RFC3339Nano) + " " + text
	f.mu.Lock()
	f.written[id] = append(f.written[id], line)
	chans := append([]chan string(nil), f.followers[id]...)
	f.mu.Unlock()
	for _, c := range chans {
		c <- line
	}
}

func (f *fakeDocker) ContainerList(_ context.Context, flt docker.ListFilter) ([]docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastFilter = flt
	return append([]docker.ContainerInfo(nil), f.containers...), nil
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.ID == id {
			return c, nil
		}
	}
	return docker.ContainerInfo{}, docker.ErrNotFound
}

func (f *fakeDocker) ContainerLogs(ctx context.Context, id string, o docker.LogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opened[id]++
	backlog := append([]string(nil), f.written[id]...)
	var feed chan string
	if o.Follow {
		feed = make(chan string, 64)
		f.followers[id] = append(f.followers[id], feed)
	}
	f.mu.Unlock()
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for _, line := range backlog {
			ts, _ := time.Parse(time.RFC3339Nano, line[:len(line)-len(line[indexSpace(line):])])
			if !o.Since.IsZero() && ts.Before(o.Since.Truncate(time.Second)) {
				continue
			}
			if _, err := io.WriteString(pw, line+"\n"); err != nil {
				return
			}
		}
		for feed != nil {
			select {
			case <-ctx.Done():
				return
			case line := <-feed:
				if _, err := io.WriteString(pw, line+"\n"); err != nil {
					return
				}
			}
		}
	}()
	return pr, nil
}

func indexSpace(s string) int {
	for i := range s {
		if s[i] == ' ' {
			return i
		}
	}
	return len(s)
}

func (f *fakeDocker) opens(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened[id]
}

func managed(id, svc string, replica int, running bool) docker.ContainerInfo {
	return docker.ContainerInfo{
		ID: id, Name: "nc-" + svc + "-" + id, Running: running, State: map[bool]string{true: "running", false: "exited"}[running],
		StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Labels: map[string]string{
			agentprotocol.LabelManaged: "true", agentprotocol.LabelService: svc,
			agentprotocol.LabelDeployment: "dep-" + id, agentprotocol.LabelReplica: fmt.Sprint(replica),
		},
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func texts(t *testing.T, st *Store, svc string) []string {
	t.Helper()
	recs, _ := collect(t, st, svc, ScanOptions{})
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Text
	}
	return out
}

func newCollector(t *testing.T, fd *fakeDocker, st *Store, ckpt string) (*Collector, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	c := NewCollector(fd, st, nil)
	c.ScanInterval = 25 * time.Millisecond
	c.CheckpointFile = ckpt
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	return c, cancel, done
}

func TestCollectorCapturesWithoutAViewer(t *testing.T) {
	st := openTest(t, t.TempDir(), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	t0 := time.Now().Add(-time.Minute)
	fd.add(managed("c1", "svc1", 0, true))
	fd.write("c1", t0, "before the agent started")
	_, cancel, done := newCollector(t, fd, st, "")
	defer func() { cancel(); <-done }()

	eventually(t, "the backlog", func() bool { return len(texts(t, st, "svc1")) == 1 })
	fd.write("c1", t0.Add(time.Second), "live one")
	fd.write("c1", t0.Add(2*time.Second), "live two")
	eventually(t, "live lines", func() bool { return len(texts(t, st, "svc1")) == 3 })

	recs, _ := collect(t, st, "svc1", ScanOptions{})
	if recs[2].Text != "live two" || recs[0].DeploymentID != "dep-c1" || recs[0].Replica != 0 {
		t.Fatalf("records: %+v", recs)
	}

	// A second replica and a second service are picked up while running.
	fd.add(managed("c2", "svc1", 1, true))
	fd.add(managed("c3", "svc2", 0, true))
	fd.write("c2", t0.Add(3*time.Second), "from replica one")
	fd.write("c3", t0.Add(3*time.Second), "other service")
	eventually(t, "new containers", func() bool { return len(texts(t, st, "svc1")) == 4 && len(texts(t, st, "svc2")) == 1 })
	recs, _ = collect(t, st, "svc1", ScanOptions{})
	if last := recs[len(recs)-1]; last.Replica != 1 || last.Text != "from replica one" {
		t.Fatalf("replica not recorded: %+v", last)
	}
}

func TestCollectorPrefixesAStacksLinesWithTheirComponent(t *testing.T) {
	st := openTest(t, t.TempDir(), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	t0 := time.Now().Add(-time.Minute)
	api, worker := managed("c1", "stack1", 0, true), managed("c2", "stack1", 1, true)
	api.Labels[agentprotocol.LabelComponent] = "api"
	worker.Labels[agentprotocol.LabelComponent] = "worker"
	fd.add(api)
	fd.add(worker)
	fd.write("c1", t0, "listening")
	fd.write("c2", t0.Add(time.Second), "queue ready")
	_, cancel, done := newCollector(t, fd, st, "")
	defer func() { cancel(); <-done }()

	eventually(t, "both components", func() bool { return len(texts(t, st, "stack1")) == 2 })
	got := texts(t, st, "stack1")
	if len(got) != 2 || !(got[0] == "api | listening" && got[1] == "worker | queue ready" || got[1] == "api | listening" && got[0] == "worker | queue ready") {
		t.Fatalf("stack lines keep the component prefix: %q", got)
	}
}

func TestCollectorPrefixesAProcessLinesWithTheirProcess(t *testing.T) {
	st := openTest(t, t.TempDir(), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	t0 := time.Now().Add(-time.Minute)
	web, worker := managed("c1", "svc1", 0, true), managed("c2", "svc1", 0, true)
	web.Labels[agentprotocol.LabelProcess] = "web"
	worker.Labels[agentprotocol.LabelProcess] = "worker"
	fd.add(web)
	fd.add(worker)
	fd.write("c1", t0, "listening")
	fd.write("c2", t0.Add(time.Second), "job done")
	_, cancel, done := newCollector(t, fd, st, "")
	defer func() { cancel(); <-done }()

	eventually(t, "both processes", func() bool { return len(texts(t, st, "svc1")) == 2 })
	got := texts(t, st, "svc1")
	if len(got) != 2 || !(got[0] == "web | listening" && got[1] == "worker | job done" || got[1] == "web | listening" && got[0] == "worker | job done") {
		t.Fatalf("process lines carry the process prefix: %q", got)
	}
}

func TestCollectorListsOnlyManagedServiceContainers(t *testing.T) {
	// Task and helper containers carry no service label, infrastructure carries no
	// managed label: the list the collector asks for excludes both.
	st := openTest(t, t.TempDir(), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	_, cancel, done := newCollector(t, fd, st, "")
	eventually(t, "a scan", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return fd.lastFilter.Labels != nil
	})
	cancel()
	<-done
	fd.mu.Lock()
	defer fd.mu.Unlock()
	l := fd.lastFilter.Labels
	if !fd.lastFilter.All || l[agentprotocol.LabelManaged] != "true" {
		t.Fatalf("filter: %+v", fd.lastFilter)
	}
	if v, ok := l[agentprotocol.LabelService]; !ok || v != "" {
		t.Fatalf("the service label must be required (any value): %+v", l)
	}
}

func TestCollectorResumesAfterARestartWithoutRepeats(t *testing.T) {
	dir := t.TempDir()
	ckpt := filepath.Join(dir, "positions.json")
	st := openTest(t, filepath.Join(dir, "logs"), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	fd.add(managed("c1", "svc1", 0, true))
	for i := range 5 {
		fd.write("c1", t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("line %d", i))
	}
	_, cancel, done := newCollector(t, fd, st, ckpt)
	eventually(t, "five lines", func() bool { return len(texts(t, st, "svc1")) == 5 })
	cancel()
	<-done // saves the positions and flushes the store

	// The agent restarts: Docker still has every line (and repeats the boundary one);
	// two more were written while the agent was down.
	fd.write("c1", t0.Add(5*time.Second), "line 5")
	fd.write("c1", t0.Add(6*time.Second), "line 6")
	st2 := openTest(t, filepath.Join(dir, "logs"), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	_, cancel2, done2 := newCollector(t, fd, st2, ckpt)
	defer func() { cancel2(); <-done2 }()
	eventually(t, "the missed lines", func() bool { return len(texts(t, st2, "svc1")) == 7 })
	time.Sleep(150 * time.Millisecond) // nothing else may arrive
	got := texts(t, st2, "svc1")
	for i, g := range got {
		if g != fmt.Sprintf("line %d", i) {
			t.Fatalf("line %d is %q (all: %v)", i, g, got)
		}
	}
}

func TestCollectorReadsAStoppedContainerOnce(t *testing.T) {
	st := openTest(t, t.TempDir(), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	fd.add(managed("dead", "svc1", 0, false))
	fd.write("dead", time.Now().Add(-time.Minute), "last words")
	_, cancel, done := newCollector(t, fd, st, "")
	defer func() { cancel(); <-done }()
	eventually(t, "the exit output", func() bool { return len(texts(t, st, "svc1")) == 1 })
	time.Sleep(200 * time.Millisecond) // several scans
	if n := fd.opens("dead"); n != 1 {
		t.Fatalf("a finished container's log was opened %d times", n)
	}
}

func TestCollectorCaptureSwitch(t *testing.T) {
	st := openTest(t, t.TempDir(), &clock{t: time.Now()}, func(o *Options) { o.Now = time.Now })
	fd := newFakeDocker()
	fd.add(managed("c1", "svc1", 0, true))
	c, cancel, done := newCollector(t, fd, st, "")
	defer func() { cancel(); <-done }()
	t0 := time.Now().Add(-time.Minute)
	fd.write("c1", t0, "one")
	eventually(t, "first line", func() bool { return len(texts(t, st, "svc1")) == 1 })

	c.SetCapture(false)
	time.Sleep(100 * time.Millisecond)
	fd.write("c1", t0.Add(time.Second), "while off")
	time.Sleep(300 * time.Millisecond)
	if n := len(texts(t, st, "svc1")); n != 1 {
		t.Fatalf("captured %d lines while capture was off", n)
	}

	c.SetCapture(true) // Docker still has the line: no gap
	eventually(t, "the line written while off", func() bool { return len(texts(t, st, "svc1")) == 2 })
	if got := texts(t, st, "svc1"); got[1] != "while off" {
		t.Fatalf("got %v", got)
	}
}
