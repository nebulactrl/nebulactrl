// Package composetest holds a fake compose.Runner for tests of the engine.
package composetest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
)

// Call is one recorded command. Files snapshots the project's compose files at
// the moment of the call (content and mode), so a test can see what was on disk
// while the command ran.
type Call struct {
	Op      string // version, config, pull, up, restart, stop, down
	Project compose.Project
	Up      compose.UpOptions
	Down    compose.DownOptions
	Grace   time.Duration
	Files   map[string]string
	Modes   map[string]fs.FileMode
}

// Fake is a scriptable compose.Runner. The Fn hooks, when set, run instead of
// the default (success) behaviour; they receive the recorded call and may
// write output lines, block on ctx, or fail.
type Fake struct {
	mu    sync.Mutex
	calls []Call

	// VersionOut and VersionErr answer Version ("2.29.7" when VersionOut is empty).
	VersionOut string
	VersionErr error
	// ConfigJSON is the normalised model Config returns when ConfigFn is nil.
	ConfigJSON string
	ConfigFn   func(ctx context.Context, c Call, out compose.Output) ([]byte, error)
	PullFn     func(ctx context.Context, c Call, out compose.Output) error
	UpFn       func(ctx context.Context, c Call, out compose.Output) error
	RestartFn  func(ctx context.Context, c Call, out compose.Output) error
	StopFn     func(ctx context.Context, c Call, out compose.Output) error
	DownFn     func(ctx context.Context, c Call, out compose.Output) error
}

var _ compose.Runner = (*Fake)(nil)

func (f *Fake) record(op string, p compose.Project) Call {
	c := Call{Op: op, Project: p}
	if len(p.Files) > 0 {
		c.Files = map[string]string{}
		c.Modes = map[string]fs.FileMode{}
		for _, name := range p.Files {
			if b, err := os.ReadFile(name); err == nil {
				c.Files[name] = string(b)
			}
			if st, err := os.Stat(name); err == nil {
				c.Modes[name] = st.Mode().Perm()
			}
		}
	}
	return c
}

func (f *Fake) add(c Call) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

// Calls returns the recorded commands in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Ops returns the operation names in order ("config", "pull", "up", ...).
func (f *Fake) Ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ops := make([]string, len(f.calls))
	for i, c := range f.calls {
		ops[i] = c.Op
	}
	return ops
}

// Of returns the calls of one operation.
func (f *Fake) Of(op string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// Version implements compose.Runner.
func (f *Fake) Version(context.Context) (string, error) {
	f.add(Call{Op: "version"})
	if f.VersionErr != nil {
		return "", f.VersionErr
	}
	if f.VersionOut == "" {
		return "2.29.7", nil
	}
	return f.VersionOut, nil
}

// Config implements compose.Runner.
func (f *Fake) Config(ctx context.Context, p compose.Project, out compose.Output) ([]byte, error) {
	c := f.record("config", p)
	f.add(c)
	if f.ConfigFn != nil {
		return f.ConfigFn(ctx, c, out)
	}
	if f.ConfigJSON == "" {
		return nil, errors.New("composetest: no ConfigJSON")
	}
	return []byte(f.ConfigJSON), nil
}

// Pull implements compose.Runner.
func (f *Fake) Pull(ctx context.Context, p compose.Project, out compose.Output) error {
	c := f.record("pull", p)
	f.add(c)
	if f.PullFn != nil {
		return f.PullFn(ctx, c, out)
	}
	return nil
}

// Up implements compose.Runner.
func (f *Fake) Up(ctx context.Context, p compose.Project, o compose.UpOptions, out compose.Output) error {
	c := f.record("up", p)
	c.Up = o
	f.add(c)
	if f.UpFn != nil {
		return f.UpFn(ctx, c, out)
	}
	return nil
}

// Restart implements compose.Runner.
func (f *Fake) Restart(ctx context.Context, p compose.Project, grace time.Duration, out compose.Output) error {
	c := f.record("restart", p)
	c.Grace = grace
	f.add(c)
	if f.RestartFn != nil {
		return f.RestartFn(ctx, c, out)
	}
	return nil
}

// Stop implements compose.Runner.
func (f *Fake) Stop(ctx context.Context, p compose.Project, grace time.Duration, out compose.Output) error {
	c := f.record("stop", p)
	c.Grace = grace
	f.add(c)
	if f.StopFn != nil {
		return f.StopFn(ctx, c, out)
	}
	return nil
}

// Down implements compose.Runner.
func (f *Fake) Down(ctx context.Context, p compose.Project, o compose.DownOptions, out compose.Output) error {
	c := f.record("down", p)
	c.Down = o
	f.add(c)
	if f.DownFn != nil {
		return f.DownFn(ctx, c, out)
	}
	return nil
}
