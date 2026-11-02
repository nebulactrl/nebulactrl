package selfupgrade

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestNewLocatesTheRunningBinaryAndTrimsTheBaseURL(t *testing.T) {
	c, err := New("/state", "1.2.3", "https://cp.example.com/nebula//", nil, nil)
	require.NoError(t, err)
	self, err := os.Executable()
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(self)
	require.NoError(t, err)
	require.Equal(t, want, c.Exe, "symlinks are resolved so the real file is replaced")
	require.Equal(t, "/state", c.StateDir)
	require.Equal(t, "1.2.3", c.Version)
	require.Equal(t, "https://cp.example.com/nebula", c.BaseURL)
}

func TestControllerDefaults(t *testing.T) {
	c := &Controller{}
	require.NotEmpty(t, c.goos())
	require.NotEmpty(t, c.goarch())
	require.WithinDuration(t, time.Now(), c.now(), time.Minute)
	require.NotNil(t, c.logger())

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c = &Controller{GOOS: "linux", GOARCH: "arm64", Now: func() time.Time { return fixed }}
	require.Equal(t, "linux", c.goos())
	require.Equal(t, "arm64", c.goarch())
	require.Equal(t, fixed, c.now())
}

func TestSupportedExplainsWhyAHostCannotUpgradeItself(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.c.Supported())

	f.c.GOARCH = "386"
	require.ErrorContains(t, f.c.Supported(), "no agent binary is shipped for 386")
	f.c.GOARCH = "arm64"
	require.NoError(t, f.c.Supported())

	f.c.Exe = ""
	require.ErrorContains(t, f.c.Supported(), "path is unknown")

	f.c.Exe = filepath.Join(f.dir, "missing-dir", "agent")
	require.ErrorContains(t, f.c.Supported(), "not writable")
}

func TestDownloadRejectsEmptyAndWronglySizedBodies(t *testing.T) {
	f := newFixture(t)
	p := f.serve()

	empty := []byte{}
	f.served.Store(&empty)
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "empty body")

	p = f.serve()
	p.Size = int64(len("binary 1.1.0")) + 5
	_, err = f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "the control plane announced")
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.Empty(t, f.leftovers())

	p.Size = int64(len("binary 1.1.0"))
	_, err = f.c.Upgrade(context.Background(), p)
	require.NoError(t, err, "the announced size matches")
}

func TestDownloadNeedsAControlPlaneURLAndAReachableServer(t *testing.T) {
	f := newFixture(t)
	p := f.serve()

	f.c.BaseURL = ""
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "no control plane URL")

	f.srv.Close()
	f.c.BaseURL = f.srv.URL
	_, err = f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "download")
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.Empty(t, f.leftovers())
}

func TestDownloadUsesTheDefaultClientAndTheArchitectureOfTheHost(t *testing.T) {
	f := newFixture(t)
	p := f.serve()
	f.c.HTTP = nil
	f.c.GOARCH = "arm64" // the fixture only serves /agent/v1/download/amd64
	_, err := f.c.Upgrade(context.Background(), p)
	require.ErrorContains(t, err, "HTTP 404")
	require.ErrorContains(t, err, "/agent/v1/download/arm64")

	f.c.GOARCH = "amd64"
	_, err = f.c.Upgrade(context.Background(), p)
	require.NoError(t, err)
}

func TestSecondUpgradeReplacesThePreviousBackup(t *testing.T) {
	f := newFixture(t)
	_, err := f.c.Upgrade(context.Background(), f.serve())
	require.NoError(t, err)
	require.Equal(t, "binary 1.0.0", f.content(f.exe+".prev"))

	// The agent restarted on 1.1.0, confirmed it, and is upgraded again.
	next := f.c.restarted("1.1.0")
	require.NoError(t, next.Start())
	require.NoError(t, next.Watch(context.Background(), func() bool { return true }))
	b := []byte("binary 1.2.0")
	f.served.Store(&b)
	_, err = next.Upgrade(context.Background(), agentprotocol.UpgradePayload{Version: "1.2.0", SHA256: sum(b)})
	require.NoError(t, err)
	require.Equal(t, "binary 1.2.0", f.content(f.exe))
	require.Equal(t, "binary 1.1.0", f.content(f.exe+".prev"), "only one generation back is kept")
}

func TestCopyFileFailures(t *testing.T) {
	dir := t.TempDir()
	require.Error(t, copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "dst")))

	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o755))
	require.Error(t, copyFile(src, filepath.Join(dir, "no-such-dir", "dst")))
}

func TestCorruptStateFileIsDiscarded(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.MkdirAll(f.c.StateDir, 0o700))
	require.NoError(t, os.WriteFile(f.c.statePath(), []byte("{not json"), 0o600))
	require.Nil(t, f.c.Report(), "a torn record is no record")
	require.NoError(t, f.c.Start(), "a torn record never blocks the agent from starting")
	_, err := f.c.Upgrade(context.Background(), f.serve())
	require.NoError(t, err)
	require.Equal(t, agentprotocol.UpgradeSwapped, f.c.Report().State, "the fresh record replaced the torn one")
}

func TestUnreadableStateFileFailsTheUpgradeBeforeAnythingChanges(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.MkdirAll(f.c.statePath(), 0o700)) // a directory where the record should be
	_, err := f.c.Upgrade(context.Background(), f.serve())
	require.ErrorContains(t, err, "read upgrade state")
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.Nil(t, f.c.Report())
}

func TestUpgradeFailsWhenTheStateCannotBeWritten(t *testing.T) {
	f := newFixture(t)
	// Between the download and the swap the record's path becomes a directory,
	// so the new record cannot be put in place.
	f.probe = func(path string) (string, error) {
		if err := os.MkdirAll(f.c.statePath(), 0o700); err != nil {
			return "", err
		}
		return "1.1.0", nil
	}
	_, err := f.c.Upgrade(context.Background(), f.serve())
	require.ErrorContains(t, err, "write upgrade state")
	require.Equal(t, "binary 1.0.0", f.content(f.exe), "no swap without a record to recover from")
	require.Empty(t, f.leftovers())
}

func TestWriteFileAtomicReplacesWholeFilesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	require.NoError(t, writeFileAtomic(path, []byte("one")))
	require.NoError(t, writeFileAtomic(path, []byte("two")))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "two", string(b))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, ents, 1, "no temporary file stays behind")

	require.Error(t, writeFileAtomic(filepath.Join(dir, "missing", "x"), []byte("x")))
	// Renaming a file over a directory fails and cleans up its temporary file.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "target"), 0o700))
	require.ErrorContains(t, writeFileAtomic(filepath.Join(dir, "target"), []byte("x")), "rename")
	ents, _ = os.ReadDir(dir)
	require.Len(t, ents, 2)
}

func TestReportDescribesTheLastUpgrade(t *testing.T) {
	f := newFixture(t)
	require.Nil(t, f.c.Report(), "an agent that never upgraded reports nothing")

	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	f.c.Now = func() time.Time { return at }
	_, err := f.c.Upgrade(context.Background(), f.serve())
	require.NoError(t, err)
	rep := f.c.Report()
	require.Equal(t, &agentprotocol.UpgradeReport{State: agentprotocol.UpgradeSwapped, From: "1.0.0", To: "1.1.0", At: at}, rep)
}

func TestWatchStopsQuietlyWhenTheAgentShutsDownDuringProbation(t *testing.T) {
	f, next := swapped(t)
	next.StartDeadline = time.Hour
	next.ConfirmAfter = time.Hour
	require.NoError(t, next.Start())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- next.Watch(ctx, func() bool { return false }) }()
	cancel()
	require.NoError(t, <-done)
	require.Equal(t, "binary 1.1.0", f.content(f.exe), "no rollback on shutdown")
	require.Equal(t, agentprotocol.UpgradeSwapped, next.Report().State, "still under probation after the next start")
}

func TestWatchUsesTheDefaultTimingsWhenNoneAreSet(t *testing.T) {
	_, next := swapped(t)
	next.ConfirmAfter, next.StartDeadline, next.Poll = 0, 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, next.Watch(ctx, func() bool { return false }), "a cancelled context ends the wait before the first poll")
}

func TestRollbackFallsBackToTheControllersPathsWhenTheRecordHasNone(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.exe+".prev", []byte("binary 0.9.0"), 0o755))
	require.NoError(t, f.c.save(state{State: agentprotocol.UpgradeSwapped, From: "0.9.0", To: "1.0.0", Starts: MaxStarts}))
	require.ErrorIs(t, f.c.Start(), ErrRestart)
	require.Equal(t, "binary 0.9.0", f.content(f.exe))
}

func TestRollbackReportsAFailedRename(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.exe+".prev", []byte("binary 0.9.0"), 0o755))
	require.NoError(t, f.c.save(state{
		State: agentprotocol.UpgradeSwapped, From: "0.9.0", To: "1.0.0", Starts: MaxStarts,
		Exe: filepath.Join(f.dir, "missing-dir", "agent"), Prev: f.exe + ".prev",
	}))
	err := f.c.Start()
	require.ErrorContains(t, err, "roll back the agent upgrade")
	require.NotErrorIs(t, err, ErrRestart)
	require.Equal(t, "binary 1.0.0", f.content(f.exe), "the running binary is untouched when the rollback cannot be done")
}

func TestStartWithAnUnreadableRecordIsAPlainStart(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.MkdirAll(f.c.statePath(), 0o700))
	require.NoError(t, f.c.Start())
	require.NoError(t, f.c.Watch(context.Background(), func() bool { return true }))
}

func TestUpgradeProbesTheDownloadedBinaryByRunningIt(t *testing.T) {
	f := newFixture(t)
	f.c.Probe = nil // the real probe: run `<binary> --version`
	script := []byte("#!/bin/sh\necho 'nebulactrl-agent version 1.1.0'\n")
	f.served.Store(&script)
	p := agentprotocol.UpgradePayload{Version: "1.1.0", SHA256: sum(script)}

	_, err := f.c.Upgrade(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, string(script), f.content(f.exe))

	// A binary that prints another version is refused and the installed one stays.
	f2 := newFixture(t)
	f2.c.Probe = nil
	wrong := []byte("#!/bin/sh\necho 'nebulactrl-agent version 9.9.9'\n")
	f2.served.Store(&wrong)
	_, err = f2.c.Upgrade(context.Background(), agentprotocol.UpgradePayload{Version: "1.1.0", SHA256: sum(wrong)})
	require.ErrorContains(t, err, `reports version "9.9.9"`)
	require.Equal(t, "binary 1.0.0", f2.content(f2.exe))

	// A binary that does not run at all is refused too.
	f3 := newFixture(t)
	f3.c.Probe = nil
	notExec := []byte("not an executable")
	f3.served.Store(&notExec)
	_, err = f3.c.Upgrade(context.Background(), agentprotocol.UpgradePayload{Version: "1.1.0", SHA256: sum(notExec)})
	require.ErrorContains(t, err, "does not run")
	require.Equal(t, "binary 1.0.0", f3.content(f3.exe))
}

func TestExecProbeRejectsSilentBinaries(t *testing.T) {
	script := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	_, err := ExecProbe(context.Background(), script)
	require.ErrorContains(t, err, "printed nothing")
}

func TestUpgradeFailsWhenThePreviousBinaryCannotBeKept(t *testing.T) {
	f := newFixture(t)
	// A non-empty directory squats on the backup path, so it cannot be replaced.
	prev := f.exe + ".prev"
	require.NoError(t, os.MkdirAll(filepath.Join(prev, "inner"), 0o700))
	_, err := f.c.Upgrade(context.Background(), f.serve())
	require.ErrorContains(t, err, "keep the current binary for rollback")
	require.Equal(t, "binary 1.0.0", f.content(f.exe))
	require.NoFileExists(t, f.c.statePath(), "no record is written for a swap that did not happen")
	require.Empty(t, f.leftovers())
}

func TestSaveFailsWhenTheStateDirCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	c := &Controller{StateDir: filepath.Join(blocker, "state")}
	require.Error(t, c.save(state{State: agentprotocol.UpgradeSwapped}))
}

func TestConfirmWithoutAnUpgradeUnderProbationWritesNothing(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.c.confirm())
	require.NoFileExists(t, f.c.statePath())
}

func TestControllerUsesItsOwnLogger(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &Controller{Log: l}
	require.Same(t, l, c.logger())
}
