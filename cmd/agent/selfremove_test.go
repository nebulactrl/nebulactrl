package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSelfRemovalDeletesIdentityUnitAndFiles(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "var-lib", "nebulactrl-agent")
	st, err := store.Open(state)
	require.NoError(t, err)
	require.NoError(t, st.PutIdentity(store.Identity{ServerID: "srv-1", AgentID: "agent-1"}))
	require.NoError(t, st.Close())
	require.NoError(t, os.MkdirAll(filepath.Join(state, "stacks", "abc"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "stacks", "abc", "compose.yml"), []byte("x"), 0o600))

	unit := filepath.Join(root, "nebulactrl-agent.service")
	dropIns := filepath.Join(root, "nebulactrl-agent.service.d")
	env := filepath.Join(root, "nebulactrl-agent.env")
	bin := filepath.Join(root, "nebulactrl-agent")
	for _, f := range []string{unit, env, bin} {
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))
	}
	require.NoError(t, os.MkdirAll(dropIns, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dropIns, "10-installer.conf"), []byte("x"), 0o600))
	keep := filepath.Join(root, "unrelated")
	require.NoError(t, os.WriteFile(keep, []byte("x"), 0o600))

	var calls []string
	var restartStopped atomic.Bool
	r := selfRemoval{
		StateDir: state, UnitFile: unit, Files: []string{unit, env, bin}, DropIns: []string{dropIns},
		Systemctl: func(_ context.Context, args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			return nil
		},
		StopRestarting: func(context.Context) error { restartStopped.Store(true); return nil },
		Log:            quietLog(),
	}
	require.NoError(t, r.Execute(context.Background()))

	for _, gone := range []string{state, unit, dropIns, env, bin} {
		_, err := os.Stat(gone)
		require.ErrorIs(t, err, os.ErrNotExist, gone)
	}
	_, err = os.Stat(keep)
	require.NoError(t, err, "files the agent did not create stay")
	require.True(t, restartStopped.Load())
	// The service is disabled first (not stopped: the unit is this process),
	// then systemd is reloaded once the files are gone, and the unit is asked
	// to stop without waiting.
	require.Equal(t, []string{"disable " + agentUnitName, "daemon-reload", "stop --no-block " + agentUnitName}, calls)
}

func TestSelfRemovalKeepsGoingAfterAFailure(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	require.NoError(t, os.MkdirAll(state, 0o700))
	env := filepath.Join(root, "env")
	require.NoError(t, os.WriteFile(env, []byte("x"), 0o600))

	r := selfRemoval{
		StateDir: state, Files: []string{env},
		Systemctl: func(_ context.Context, args ...string) error {
			if args[0] == "daemon-reload" {
				return errors.New("no bus")
			}
			return nil
		},
		Log: quietLog(),
	}
	err := r.Execute(context.Background())
	require.ErrorContains(t, err, "reload systemd")
	_, serr := os.Stat(state)
	require.ErrorIs(t, serr, os.ErrNotExist, "the state directory goes even when systemd could not be reloaded")
	_, serr = os.Stat(env)
	require.ErrorIs(t, serr, os.ErrNotExist)
}

func TestSelfRemovalRefusesDangerousStateDirs(t *testing.T) {
	for _, dir := range []string{"", "/", "/var/lib", "/etc", "relative/dir", "/usr/local/bin/"} {
		require.False(t, safeStateDir(dir), dir)
	}
	require.True(t, safeStateDir("/var/lib/nebulactrl-agent"))

	r := selfRemoval{StateDir: "/var/lib", Log: quietLog()}
	err := r.Execute(context.Background())
	require.ErrorIs(t, err, errUnsafeStateDir)
}

func TestDecommissionResultSentEndsTheRunOrRestarts(t *testing.T) {
	var restarted atomic.Int32
	h := newUpgradeHandler(t, func() int { return 0 }, &restarted)
	cmd := agentprotocol.Command{ID: "c1", Kind: agentprotocol.KindDecommission}

	// A decommission whose host step failed ends in a restart, never in a removal.
	h.decommissioned = &restartRequest{cancel: func() {}}
	h.ResultSent(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed})
	require.Eventually(t, func() bool { return restarted.Load() == 1 }, time.Second, 5*time.Millisecond)
	require.False(t, h.decommissioned.requested.Load())

	// A success ends the run through the decommission request, without a restart.
	var ended atomic.Bool
	h.decommissioned = &restartRequest{cancel: func() { ended.Store(true) }}
	h.ResultSent(context.Background(), cmd, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded})
	require.Eventually(t, func() bool { return ended.Load() }, time.Second, 5*time.Millisecond)
	require.True(t, h.decommissioned.requested.Load())
	require.Equal(t, int32(1), restarted.Load(), "no restart behind a successful decommission")
}

func TestWithinIsStrict(t *testing.T) {
	require.True(t, within("/var/lib/a", "/var/lib/a/backups"))
	require.True(t, within("/var/lib/a/", "/var/lib/a/x/y"))
	require.False(t, within("/var/lib/a", "/var/lib/a"), "the directory itself is not inside it")
	require.False(t, within("/var/lib/a", "/var/lib/ab/backups"), "a sibling with the same prefix is not inside it")
	require.False(t, within("/var/lib/a", "/srv/backups"))
}

// Local backups are never deleted: a backup directory inside the state
// directory is moved to the keep directory, one outside it stays.
func TestSelfRemovalKeepsLocalBackups(t *testing.T) {
	newState := func(t *testing.T) (root, state, backups string) {
		root = t.TempDir()
		state = filepath.Join(root, "state")
		backups = filepath.Join(state, "backups")
		require.NoError(t, os.MkdirAll(filepath.Join(backups, "dest-1"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(backups, "dest-1", "db.dump"), []byte("data"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(state, "agent.db"), []byte("identity"), 0o600))
		return root, state, backups
	}
	read := func(t *testing.T, p string) string {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("default directory inside the state directory is moved", func(t *testing.T) {
		root, state, backups := newState(t)
		keep := filepath.Join(root, "kept")
		r := selfRemoval{StateDir: state, BackupDir: backups, KeepDir: keep, Log: quietLog()}
		require.NoError(t, r.Execute(context.Background()))
		require.NoDirExists(t, state)
		require.Equal(t, "data", read(t, filepath.Join(keep, "dest-1", "db.dump")))
	})

	t.Run("an existing keep directory is not mixed with the new backups", func(t *testing.T) {
		root, state, backups := newState(t)
		keep := filepath.Join(root, "kept")
		require.NoError(t, os.MkdirAll(keep, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(keep, "older"), []byte("old"), 0o600))
		r := selfRemoval{StateDir: state, BackupDir: backups, KeepDir: keep, Log: quietLog()}
		require.NoError(t, r.Execute(context.Background()))
		require.Equal(t, "old", read(t, filepath.Join(keep, "older")))
		subs, err := filepath.Glob(filepath.Join(keep, "agent-*", "dest-1", "db.dump"))
		require.NoError(t, err)
		require.Len(t, subs, 1)
		require.NoDirExists(t, state)
	})

	t.Run("a directory outside the state directory stays", func(t *testing.T) {
		root, state, _ := newState(t)
		outside := filepath.Join(root, "disk", "backups")
		require.NoError(t, os.MkdirAll(outside, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "b"), []byte("kept"), 0o600))
		keep := filepath.Join(root, "kept")
		r := selfRemoval{StateDir: state, BackupDir: outside, KeepDir: keep, Log: quietLog()}
		require.NoError(t, r.Execute(context.Background()))
		require.NoDirExists(t, state)
		require.NoDirExists(t, keep)
		require.Equal(t, "kept", read(t, filepath.Join(outside, "b")))
	})

	t.Run("when the move fails only the backups stay, in place", func(t *testing.T) {
		_, state, backups := newState(t)
		// A keep directory under a file cannot be created.
		blocker := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
		r := selfRemoval{StateDir: state, BackupDir: backups, KeepDir: filepath.Join(blocker, "kept"), Log: quietLog()}
		require.NoError(t, r.Execute(context.Background()))
		require.NoFileExists(t, filepath.Join(state, "agent.db"), "the identity is gone")
		require.Equal(t, "data", read(t, filepath.Join(backups, "dest-1", "db.dump")))
	})
}
