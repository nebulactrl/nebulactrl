package backups

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

func (e *instEnv) preUpgradeDeps(tools PGTools) PreUpgradeDeps {
	return PreUpgradeDeps{
		Pool: e.pool, DatabaseURL: "postgres://nebula:" + canaryDBPassword + "@db.internal:5432/nebulactrl", BackupDir: e.dir,
		Box: e.svc.box, KeyFingerprint: KeyFingerprint(bytes.Repeat([]byte{3}, 32)), Tools: tools,
	}
}

func TestPreUpgradeBackupWritesADumpAndManifestAndIsRecorded(t *testing.T) {
	ft := &fakeTools{}
	e := newInstEnv(t, ft.tools())
	e.enableLocal(t, 14)

	res := RunPreUpgrade(context.Background(), e.preUpgradeDeps(ft.tools()), "1.4.0", "1.5.0")
	require.Equal(t, PreUpgradeSucceeded, res.Status, res.Message)
	require.Equal(t, "1.4.0", res.From)
	require.Equal(t, "1.5.0", res.To)
	require.Greater(t, res.SizeBytes, int64(0))
	require.Equal(t, 1, ft.calls)

	// The dump and its manifest are in the destination directory.
	dump := filepath.Join(e.dir, "nightly", filepath.FromSlash(res.ObjectKey))
	got, err := os.ReadFile(dump)
	require.NoError(t, err)
	require.Equal(t, fakeDump, got)
	require.FileExists(t, dump+".manifest.json")
	man, err := os.ReadFile(dump + ".manifest.json")
	require.NoError(t, err)
	require.Contains(t, string(man), `"trigger": "pre_upgrade"`)
	require.NotContains(t, string(man), canaryDBPassword)

	// Recording after the migrations adds the run to the instance history.
	require.NoError(t, RecordPreUpgrade(context.Background(), e.pool, res))
	runs, err := e.svc.q.ListInstanceBackups(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "pre_upgrade", runs[0].Trigger)
	require.Equal(t, "succeeded", runs[0].Status)
	require.Equal(t, res.SHA256, runs[0].Sha256)
	up, err := e.svc.q.GetInstanceUpgrade(context.Background())
	require.NoError(t, err)
	require.Equal(t, PreUpgradeSucceeded, up.PreUpgradeStatus)
	require.Equal(t, "1.5.0", up.PreUpgradeToVersion)
	require.Equal(t, "1.4.0", up.PreUpgradeFromVersion)
	// Recording twice is harmless.
	require.NoError(t, RecordPreUpgrade(context.Background(), e.pool, res))
}

func TestPreUpgradeBackupFailureIsAFailedResultWithoutSecrets(t *testing.T) {
	ft := &fakeTools{failed: errors.New("pg_dump: password " + canaryDBPassword + " rejected")}
	e := newInstEnv(t, ft.tools())
	e.enableLocal(t, 14)

	res := RunPreUpgrade(context.Background(), e.preUpgradeDeps(ft.tools()), "1.4.0", "1.5.0")
	require.Equal(t, PreUpgradeFailed, res.Status)
	require.True(t, res.Blocks(false))
	require.False(t, res.Blocks(true))
	require.NotContains(t, res.Message, canaryDBPassword)
	require.Contains(t, res.Message, "rejected")

	// A failed result records only the summary: no run row, no object.
	require.NoError(t, RecordPreUpgrade(context.Background(), e.pool, res.Forced()))
	runs, err := e.svc.q.ListInstanceBackups(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, runs)
	up, err := e.svc.q.GetInstanceUpgrade(context.Background())
	require.NoError(t, err)
	require.Equal(t, PreUpgradeForced, up.PreUpgradeStatus)
}

func TestPreUpgradeBackupFailsWhenTheToolsAreMissing(t *testing.T) {
	missing := PGTools{Check: func() error { return errors.New("pg_dump is not installed") }}
	e := newInstEnv(t, missing)
	e.enableLocal(t, 14)
	res := RunPreUpgrade(context.Background(), e.preUpgradeDeps(missing), "", "1.5.0")
	require.Equal(t, PreUpgradeFailed, res.Status)
	require.Contains(t, res.Message, "pg_dump is not installed")
}

func TestPreUpgradeBackupIsSkippedWithoutADestination(t *testing.T) {
	ft := &fakeTools{}
	e := newInstEnv(t, ft.tools())
	// Local destination by default, but the control plane has no backup directory.
	deps := e.preUpgradeDeps(ft.tools())
	deps.BackupDir = ""
	res := RunPreUpgrade(context.Background(), deps, "1.4.0", "1.5.0")
	require.Equal(t, PreUpgradeSkipped, res.Status)
	require.False(t, res.Blocks(false), "no destination is not a failed backup")
	require.Zero(t, ft.calls)
}

func TestPreUpgradeRunsAgainstAnOlderSchema(t *testing.T) {
	// The backup runs before the migrations: it must work when columns a later
	// migration adds to the settings table do not exist yet.
	ft := &fakeTools{}
	e := newInstEnv(t, ft.tools())
	e.enableLocal(t, 14)
	_, err := e.pool.Exec(context.Background(), `ALTER TABLE instance_backup_settings ADD COLUMN future_column text`)
	require.NoError(t, err)
	c, err := readInstanceDestination(context.Background(), e.pool)
	require.NoError(t, err)
	require.Equal(t, "local", c.DestKind)
	require.Equal(t, "nightly", c.LocalDir)
	var _ db.InstanceBackupSetting = c
}
