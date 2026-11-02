package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSelfRemovalMatchesTheInstaller(t *testing.T) {
	stop := func(context.Context) error { return nil }
	r := newSelfRemoval("/var/lib/nebulactrl-agent", "/mnt/backups", "/opt/test/other-name", stop, quietLog())
	assert.Equal(t, "/var/lib/nebulactrl-agent", r.StateDir)
	assert.Equal(t, agentUnitFile, r.UnitFile)
	assert.Equal(t, []string{agentUnitFile, agentEnvFile, agentBinary}, r.Files, "a binary with another name is left alone")
	assert.Equal(t, []string{agentUnitDropIn}, r.DropIns)
	assert.NotNil(t, r.StopRestarting)
	assert.Equal(t, "/mnt/backups", r.BackupDir)
	assert.Equal(t, agentBackupKeepDir, r.KeepDir)
	assert.Equal(t, 2*time.Second, r.Settle)
}

func TestNewSelfRemovalAlsoRemovesTheRunningInstalledBinary(t *testing.T) {
	// Same file name as the installed binary, in another directory (a copy that was started).
	exe := filepath.Join(t.TempDir(), filepath.Base(agentBinary))
	r := newSelfRemoval("/var/lib/x", "", exe, nil, quietLog())
	assert.Equal(t, []string{agentUnitFile, agentEnvFile, agentBinary, exe}, r.Files)
	assert.Nil(t, r.StopRestarting)

	// The installed path itself is not listed twice.
	r = newSelfRemoval("/var/lib/x", "", agentBinary, nil, quietLog())
	assert.Equal(t, []string{agentUnitFile, agentEnvFile, agentBinary}, r.Files)

	r = newSelfRemoval("/var/lib/x", "", "", nil, quietLog())
	assert.Len(t, r.Files, 3)
}

func TestPreserveBackupsLeavesAnExternalBackupDirAlone(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	outside := filepath.Join(t.TempDir(), "backups")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	r := selfRemoval{StateDir: state, BackupDir: outside, KeepDir: filepath.Join(t.TempDir(), "keep"), Log: quietLog()}
	assert.Equal(t, "", r.preserveBackups())
	_, err := os.Stat(outside)
	require.NoError(t, err)
	_, err = os.Stat(r.KeepDir)
	assert.ErrorIs(t, err, os.ErrNotExist, "nothing is moved")

	r.BackupDir = ""
	assert.Equal(t, "", r.preserveBackups(), "no backup directory configured")
}

func TestPreserveBackupsIgnoresAMissingBackupDir(t *testing.T) {
	state := t.TempDir()
	keep := filepath.Join(t.TempDir(), "keep")
	r := selfRemoval{StateDir: state, BackupDir: filepath.Join(state, "backups"), KeepDir: keep, Log: quietLog()}
	assert.Equal(t, "", r.preserveBackups())
	_, err := os.Stat(keep)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestPreserveBackupsMovesToATimestampedDirWhenKeepDirExists(t *testing.T) {
	state := t.TempDir()
	backups := filepath.Join(state, "backups")
	require.NoError(t, os.MkdirAll(filepath.Join(backups, "svc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(backups, "svc", "dump.sql"), []byte("data"), 0o600))
	keep := filepath.Join(t.TempDir(), "kept")
	require.NoError(t, os.MkdirAll(keep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(keep, "older.sql"), []byte("old"), 0o600))

	r := selfRemoval{StateDir: state, BackupDir: backups, KeepDir: keep, Log: quietLog()}
	assert.Equal(t, "", r.preserveBackups())

	_, err := os.Stat(backups)
	assert.ErrorIs(t, err, os.ErrNotExist, "moved out of the state directory")
	entries, err := os.ReadDir(keep)
	require.NoError(t, err)
	var moved string
	for _, e := range entries {
		if e.IsDir() {
			moved = e.Name()
		}
	}
	require.Regexp(t, `^agent-\d{8}T\d{6}$`, moved)
	got, err := os.ReadFile(filepath.Join(keep, moved, "svc", "dump.sql"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(got))
	got, err = os.ReadFile(filepath.Join(keep, "older.sql"))
	require.NoError(t, err)
	assert.Equal(t, "old", string(got), "an earlier agent's backups are untouched")
}

func TestPreserveBackupsKeepsThemInPlaceWhenTheyCannotBeMoved(t *testing.T) {
	state := t.TempDir()
	backups := filepath.Join(state, "backups")
	require.NoError(t, os.MkdirAll(backups, 0o755))
	// The parent of the keep dir is a regular file: MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	r := selfRemoval{StateDir: state, BackupDir: backups, KeepDir: filepath.Join(blocker, "kept"), Log: quietLog()}
	assert.Equal(t, backups, r.preserveBackups(), "the removal must skip the backup directory")
	_, err := os.Stat(backups)
	require.NoError(t, err)
}

func TestRemoveAllExcept(t *testing.T) {
	t.Run("missing directory is not an error when something is kept", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "gone")
		require.NoError(t, removeAllExcept(dir, filepath.Join(dir, "backups")))
	})
	t.Run("keeps the path and its parents, removes siblings at every level", func(t *testing.T) {
		dir := t.TempDir()
		keep := filepath.Join(dir, "a", "b", "keep")
		for _, p := range []string{keep, filepath.Join(dir, "a", "b", "other"), filepath.Join(dir, "a", "side"), filepath.Join(dir, "top")} {
			require.NoError(t, os.MkdirAll(p, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(p, "f"), []byte("x"), 0o600))
		}
		require.NoError(t, removeAllExcept(dir, keep))
		_, err := os.Stat(filepath.Join(keep, "f"))
		require.NoError(t, err, "the kept directory keeps its content")
		for _, gone := range []string{filepath.Join(dir, "a", "b", "other"), filepath.Join(dir, "a", "side"), filepath.Join(dir, "top")} {
			_, err := os.Stat(gone)
			assert.ErrorIs(t, err, os.ErrNotExist, gone)
		}
	})
	t.Run("an unreadable directory reports the error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads everything")
		}
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		require.Error(t, removeAllExcept(dir, filepath.Join(dir, "keep")))
	})
	t.Run("without keep it removes everything", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "d")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "x"), 0o755))
		require.NoError(t, removeAllExcept(dir, ""))
		_, err := os.Stat(dir)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
}
