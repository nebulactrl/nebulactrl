package backup

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// memStore is an in-memory Store with the same commit semantics as the real
// ones: an object exists only if Put's reader returned io.EOF.
type memStore struct {
	mu      sync.Mutex
	objs    map[string][]byte
	puts    int
	aborted int
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if err != nil {
		m.aborted++
		return err
	}
	m.objs[key] = b
	return nil
}

func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStore) Size(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return 0, ErrNotFound
	}
	return int64(len(b)), nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

func (m *memStore) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		out = append(out, k)
	}
	return out
}

// fakeExec scripts ContainerExec.
type fakeExec struct {
	stdout   []byte // written to stdout
	stderr   string
	exit     int
	err      error
	failMid  bool // write half of stdout, then return exit/err
	gotArgv  []string
	gotEnv   []string
	gotStdin []byte
	calls    int
}

func (f *fakeExec) ContainerExec(_ context.Context, _ string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
	f.calls++
	f.gotArgv, f.gotEnv = argv, spec.Env
	if spec.Stdin != nil {
		f.gotStdin, _ = io.ReadAll(spec.Stdin)
	}
	out := f.stdout
	if f.failMid {
		out = out[:len(out)/2]
	}
	if len(out) > 0 {
		if _, err := stdout.Write(out); err != nil {
			return -1, err
		}
	}
	if f.stderr != "" {
		_, _ = io.WriteString(stderr, f.stderr)
	}
	return f.exit, f.err
}
