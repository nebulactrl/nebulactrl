package selfupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type fixture struct {
	t      *testing.T
	dir    string
	exe    string
	c      *Controller
	served atomic.Pointer[[]byte]
	status atomic.Int32
	hits   atomic.Int32
	probe  func(path string) (string, error)
	srv    *httptest.Server
}

func newFixture(t *testing.T, running string) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, exe: filepath.Join(dir, "nebulactrl-agent")}
	require.NoError(t, os.WriteFile(f.exe, []byte("binary "+running), 0o755))
	f.status.Store(http.StatusOK)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.URL.Path != "/agent/v1/download/amd64" {
			http.NotFound(w, r)
			return
		}
		if s := int(f.status.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			return
		}
		_, _ = w.Write(*f.served.Load())
	}))
	t.Cleanup(f.srv.Close)
	f.c = &Controller{
		Exe: f.exe, StateDir: filepath.Join(dir, "state"), Version: running, GOOS: "linux", GOARCH: "amd64",
		BaseURL: f.srv.URL, HTTP: f.srv.Client(),
		Probe: func(_ context.Context, path string) (string, error) {
			if f.probe != nil {
				return f.probe(path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return strings.TrimPrefix(string(b), "binary "), nil
		},
		ConfirmAfter: 30 * time.Millisecond, StartDeadline: 400 * time.Millisecond, Poll: 5 * time.Millisecond,
	}
	return f
}

func (f *fixture) serve(content string) agentprotocol.UpgradePayload {
	b := []byte(content)
	f.served.Store(&b)
	return agentprotocol.UpgradePayload{Version: strings.TrimPrefix(content, "binary "), SHA256: sum(b)}
}

func (f *fixture) content(path string) string {
	b, err := os.ReadFile(path)
	require.NoError(f.t, err)
	return string(b)
}

func (f *fixture) leftovers() []string {
	ents, _ := os.ReadDir(f.dir)
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".nebulactrl-agent.") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestUpgradeSwapsAtomicallyAndKeepsPrevious(t *testing.T) {
	f := newFixture(t, "1.0.0")
	p := f.serve("binary 1.1.0")
	res, err := f.c.Upgrade(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, "1.0.0", res.From)
	require.Equal(t, "1.1.0", res.To)
	require.Equal(t, "binary 1.1.0", f.content(f.exe))
	require.Equal(t, "binary 1.0.0", f.content(f.exe+".prev"))
	fi, err := os.Stat(f.exe)
	require.NoError(t, err)
	require.NotZero(t, fi.Mode().Perm()&0o100, "the new binary is executable")
	require.Empty(t, f.leftovers(), "no temporary files stay behind")

	rep := f.c.Report()
	require.NotNil(t, rep)
	require.Equal(t, agentprotocol.UpgradeSwapped, rep.State)
	require.Equal(t, "1.1.0", rep.To)

	// A redelivery of the same command is a no-op, not a second download.
	hits := f.hits.Load()
	res, err = f.c.Upgrade(context.Background(), p)
	require.NoError(t, err)
	require.Contains(t, res.Message, "already installed")
	require.Equal(t, hits, f.hits.Load())
}

func TestUpgradeRejectsChecksumMismatchAndLeavesBinaryAlone(t *testing.T) {
	f := newFixture(t, "1.0.0")
	p := f.serve("binary 1.1.0")
	p.SHA256 = sum([]byte("something else"))
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "checksum mismatch")
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.NoFileExists(t, f.exe+".prev")
	require.NoFileExists(t, f.c.statePath())
	require.Empty(t, f.leftovers(), "the temporary file is removed")
}

func TestUpgradeRejectsBadInputs(t *testing.T) {
	f := newFixture(t, "1.0.0")
	p := f.serve("binary 1.1.0")
	for name, mut := range map[string]func(*agentprotocol.UpgradePayload){
		"no version":   func(p *agentprotocol.UpgradePayload) { p.Version = "" },
		"short sha":    func(p *agentprotocol.UpgradePayload) { p.SHA256 = "abc" },
		"non-hex sha":  func(p *agentprotocol.UpgradePayload) { p.SHA256 = strings.Repeat("z", 64) },
		"same version": func(p *agentprotocol.UpgradePayload) { p.Version = "1.0.0" },
		"wrong size":   func(p *agentprotocol.UpgradePayload) { p.Size = 3 },
	} {
		q := p
		mut(&q)
		_, err := f.c.Upgrade(context.Background(), q)
		require.Error(t, err, name)
		require.Equal(t, "binary 1.0.0", f.content(f.exe), name)
	}
	f.c.GOOS = "darwin"
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "linux only")
}

func TestUpgradeDownloadFailure(t *testing.T) {
	f := newFixture(t, "1.0.0")
	p := f.serve("binary 1.1.0")
	f.status.Store(http.StatusNotFound)
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "HTTP 404")
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.Empty(t, f.leftovers())
}

func TestUpgradeRejectsBinaryThatDoesNotRunOrReportsWrongVersion(t *testing.T) {
	f := newFixture(t, "1.0.0")
	p := f.serve("binary 1.1.0")
	f.probe = func(string) (string, error) { return "", errors.New("exec format error") }
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "does not run")
	f.probe = func(string) (string, error) { return "v9.9.9", nil }
	_, err = f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "reports version")
	f.probe = func(string) (string, error) { return "v1.1.0", nil } // a "v" prefix is the same version
	_, err = f.c.Upgrade(context.Background(), p)
	require.NoError(t, err)
}

func TestUpgradeFailsWhenDirectoryNotWritable(t *testing.T) {
	f := newFixture(t, "1.0.0")
	p := f.serve("binary 1.1.0")
	require.NoError(t, os.Chmod(f.dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "not writable")
}

// swapped runs a successful Upgrade and returns the fixture ready for the
// restart: the "new process" is a Controller whose Version is the new one.
func swapped(t *testing.T) (*fixture, *Controller) {
	t.Helper()
	f := newFixture(t, "1.0.0")
	_, err := f.c.Upgrade(context.Background(), f.serve("binary 1.1.0"))
	require.NoError(t, err)
	return f, f.c.restarted("1.1.0")
}

// restarted is the Controller of the process that starts after the restart: the
// same files, another running version.
func (c *Controller) restarted(version string) *Controller {
	return &Controller{
		Exe: c.Exe, StateDir: c.StateDir, Version: version, GOOS: c.GOOS, GOARCH: c.GOARCH, BaseURL: c.BaseURL, HTTP: c.HTTP,
		Probe: c.Probe, ConfirmAfter: c.ConfirmAfter, StartDeadline: c.StartDeadline, Poll: c.Poll,
	}
}

func TestNewBinaryThatConnectsIsConfirmed(t *testing.T) {
	f, next := swapped(t)
	require.NoError(t, next.Start())
	require.NoError(t, next.Watch(context.Background(), func() bool { return true }))
	rep := next.Report()
	require.Equal(t, agentprotocol.UpgradeCommitted, rep.State)
	require.Equal(t, "binary 1.1.0", f.content(f.exe))
	require.FileExists(t, f.exe+".prev", "one generation stays for a manual rollback")
	// A later start is a plain start.
	require.NoError(t, next.Start())
}

func TestNewBinaryThatNeverConnectsIsRolledBack(t *testing.T) {
	f, next := swapped(t)
	require.NoError(t, next.Start())
	err := next.Watch(context.Background(), func() bool { return false })
	require.ErrorIs(t, err, ErrRestart)
	require.Equal(t, "binary 1.0.0", f.content(f.exe), "the previous binary is back")
	require.NoFileExists(t, f.exe+".prev")
	rep := next.Report()
	require.Equal(t, agentprotocol.UpgradeRolledBack, rep.State)
	require.Contains(t, rep.Message, "did not connect")

	// The restored binary starts normally and the bad version is not retried.
	old := f.c.restarted("1.0.0")
	require.NoError(t, old.Start())
	p := f.serve("binary 1.1.0")
	_, err = old.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "rolled back on this host")
	p.Force = true
	_, err = old.Upgrade(context.Background(), p)
	require.NoError(t, err, "Force retries")
}

func TestCrashLoopingNewBinaryIsRolledBackAfterMaxStarts(t *testing.T) {
	f, next := swapped(t)
	for i := 1; i <= MaxStarts; i++ {
		require.NoError(t, next.Start(), "start %d", i)
		// the process dies before it connects
	}
	require.ErrorIs(t, next.Start(), ErrRestart)
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.Equal(t, agentprotocol.UpgradeRolledBack, next.Report().State)
	require.Contains(t, next.Report().Message, "without staying connected")
}

func TestFlappingConnectionDoesNotConfirm(t *testing.T) {
	_, next := swapped(t)
	require.NoError(t, next.Start())
	var n atomic.Int32
	err := next.Watch(context.Background(), func() bool { return n.Add(1)%3 != 0 }) // never up for ConfirmAfter
	require.ErrorIs(t, err, ErrRestart)
}

func TestSwapThatNeverTookEffectIsAbandoned(t *testing.T) {
	f := newFixture(t, "1.0.0")
	_, err := f.c.Upgrade(context.Background(), f.serve("binary 1.1.0"))
	require.NoError(t, err)
	// The container was recreated from its image: the agent still runs 1.0.0.
	require.NoError(t, f.c.Start())
	rep := f.c.Report()
	require.Equal(t, agentprotocol.UpgradeAbandoned, rep.State)
	require.NoError(t, f.c.Watch(context.Background(), func() bool { return false }), "nothing is under probation")
}

func TestRollbackWithoutPreviousBinaryFailsLoudly(t *testing.T) {
	f, next := swapped(t)
	require.NoError(t, os.Remove(f.exe+".prev"))
	require.NoError(t, next.Start())
	err := next.Watch(context.Background(), func() bool { return false })
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRestart)
	require.Contains(t, next.Report().Message, "previous binary is gone")
}

func TestKeepPreviousFallsBackToCopy(t *testing.T) {
	f := newFixture(t, "1.0.0")
	require.NoError(t, copyFile(f.exe, f.exe+".prev"))
	require.Equal(t, "binary 1.0.0", f.content(f.exe+".prev"))
	fi, err := os.Stat(f.exe + ".prev")
	require.NoError(t, err)
	require.NotZero(t, fi.Mode().Perm()&0o100)
}

func TestExecProbeReadsCobraVersionOutput(t *testing.T) {
	script := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'nebulactrl-agent version 1.7.2'\n"), 0o755))
	v, err := ExecProbe(context.Background(), script)
	require.NoError(t, err)
	require.Equal(t, "1.7.2", v)
	_, err = ExecProbe(context.Background(), filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
