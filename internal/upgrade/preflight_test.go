package upgrade

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/backups"
)

var (
	bg  = context.Background()
	now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
)

type harness struct {
	state  State
	result backups.PreUpgradeResult
	calls  []string // "from->to"
	d      Deps
}

func newHarness(current string, st State, res backups.PreUpgradeResult) *harness {
	h := &harness{state: st, result: res}
	h.d = Deps{
		Current: current,
		State:   func(context.Context) (State, error) { return h.state, nil },
		Backup: func(_ context.Context, from, to string) backups.PreUpgradeResult {
			h.calls = append(h.calls, from+"->"+to)
			r := h.result
			r.From, r.To = from, to
			return r
		},
		Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h
}

var (
	ok     = backups.PreUpgradeResult{Status: backups.PreUpgradeSucceeded, Message: "backed up"}
	failed = backups.PreUpgradeResult{Status: backups.PreUpgradeFailed, Message: "pg_dump: connection refused"}
	skip   = backups.PreUpgradeResult{Status: backups.PreUpgradeSkipped, Message: "no destination"}
)

func TestPreflightBacksUpOnAVersionChange(t *testing.T) {
	h := newHarness("1.5.0", State{Existing: true, LastVersion: "1.4.0"}, ok)
	dec, err := Preflight(bg, h.d)
	require.NoError(t, err)
	require.Equal(t, []string{"1.4.0->1.5.0"}, h.calls)
	require.Equal(t, backups.PreUpgradeSucceeded, dec.Result.Status)
}

func TestPreflightBacksUpWhenThePreviousVersionIsUnknown(t *testing.T) {
	// A database created before the version was tracked: the first start of a
	// version that tracks it is an upgrade.
	h := newHarness("1.5.0", State{Existing: true}, ok)
	_, err := Preflight(bg, h.d)
	require.NoError(t, err)
	require.Equal(t, []string{"->1.5.0"}, h.calls)
}

func TestPreflightDoesNothingWithoutAChange(t *testing.T) {
	for name, tc := range map[string]struct {
		current string
		st      State
	}{
		"same version":     {"1.5.0", State{Existing: true, LastVersion: "1.5.0"}},
		"new database":     {"1.5.0", State{}},
		"development":      {"dev", State{Existing: true, LastVersion: "1.4.0"}},
		"describe build":   {"v1.4.0-3-gabc1234", State{Existing: true, LastVersion: "1.4.0"}},
		"installer did it": {"1.5.0", State{Existing: true, LastVersion: "1.4.0", BackupTo: "1.5.0", BackupStatus: backups.PreUpgradeSucceeded, BackupAt: now.Add(-time.Hour)}},
	} {
		h := newHarness(tc.current, tc.st, ok)
		dec, err := Preflight(bg, h.d)
		require.NoError(t, err, name)
		require.Empty(t, h.calls, name)
		require.Nil(t, dec.Result, name)
		require.NotEmpty(t, dec.Reason, name)
	}
}

func TestInstallerBackupForAnotherVersionOrTooOldDoesNotCount(t *testing.T) {
	for name, st := range map[string]State{
		"other version": {Existing: true, LastVersion: "1.4.0", BackupTo: "1.6.0", BackupStatus: backups.PreUpgradeSucceeded, BackupAt: now.Add(-time.Hour)},
		"too old":       {Existing: true, LastVersion: "1.4.0", BackupTo: "1.5.0", BackupStatus: backups.PreUpgradeSucceeded, BackupAt: now.Add(-48 * time.Hour)},
		"it failed":     {Existing: true, LastVersion: "1.4.0", BackupTo: "1.5.0", BackupStatus: backups.PreUpgradeFailed, BackupAt: now.Add(-time.Hour)},
	} {
		h := newHarness("1.5.0", st, ok)
		_, err := Preflight(bg, h.d)
		require.NoError(t, err, name)
		require.Len(t, h.calls, 1, name)
	}
}

func TestPreflightRefusesWhenTheBackupFails(t *testing.T) {
	h := newHarness("1.5.0", State{Existing: true, LastVersion: "1.4.0"}, failed)
	dec, err := Preflight(bg, h.d)
	require.ErrorIs(t, err, ErrBackupFailed)
	require.ErrorContains(t, err, "connection refused")
	require.Equal(t, backups.PreUpgradeFailed, dec.Result.Status)
}

func TestPreflightForcePassesAFailedBackup(t *testing.T) {
	h := newHarness("1.5.0", State{Existing: true, LastVersion: "1.4.0"}, failed)
	h.d.Force = true
	dec, err := Preflight(bg, h.d)
	require.NoError(t, err)
	require.Equal(t, backups.PreUpgradeForced, dec.Result.Status)
	require.Contains(t, dec.Result.Message, "Forced past a failed backup")
}

func TestPreflightProceedsWhenThereIsNoDestination(t *testing.T) {
	// Nothing to back up to is not a failed backup: refusing would make every
	// instance that never configured one unable to upgrade.
	h := newHarness("1.5.0", State{Existing: true, LastVersion: "1.4.0"}, skip)
	dec, err := Preflight(bg, h.d)
	require.NoError(t, err)
	require.Equal(t, backups.PreUpgradeSkipped, dec.Result.Status)
}

func TestPreflightPropagatesAStateReadError(t *testing.T) {
	h := newHarness("1.5.0", State{}, ok)
	h.d.State = func(context.Context) (State, error) { return State{}, errors.New("connection reset") }
	_, err := Preflight(bg, h.d)
	require.ErrorContains(t, err, "connection reset")
	require.NotErrorIs(t, err, ErrBackupFailed)
	require.Empty(t, h.calls)
}

func TestPreflightAllowsADowngradeButBacksUp(t *testing.T) {
	h := newHarness("1.3.0", State{Existing: true, LastVersion: "1.4.0"}, ok)
	_, err := Preflight(bg, h.d)
	require.NoError(t, err)
	require.Equal(t, []string{"1.4.0->1.3.0"}, h.calls)
}

func TestResultGating(t *testing.T) {
	require.True(t, failed.Blocks(false))
	require.False(t, failed.Blocks(true))
	require.False(t, ok.Blocks(false))
	require.False(t, skip.Blocks(false))
	require.Equal(t, backups.PreUpgradeSucceeded, ok.Forced().Status, "only a failure becomes forced")
}
