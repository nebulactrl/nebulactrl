package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const backupPass = "db-secret-pass-98765"

func backupCmd(t *testing.T, key string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.DBBackupPayload{
		BackupID:    "b1",
		Target:      agentprotocol.BackupTarget{ServiceID: svcID, Engine: "postgres", Version: "17", Database: "app", User: "postgres"},
		Destination: agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal, LocalDir: "nightly"},
		ObjectKey:   key,
	})
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-bk", Kind: agentprotocol.KindDBBackup, Payload: raw, Secrets: map[string]string{agentprotocol.SecretDBPassword: backupPass}}
}

func TestDBBackupStreamsIntoLocalDestination(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.BackupDir = t.TempDir()
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var gotID string
	var gotEnv []string
	r.dk.onExec = func(id string, argv []string, spec docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		gotID, gotEnv = id, spec.Env
		require.Equal(t, "pg_dump", argv[0])
		_, err := stdout.Write([]byte(strings.Repeat("PGDMP", 1000)))
		return 0, err
	}
	res := r.eng.HandleCommand(context.Background(), backupCmd(t, "p/e/pg/one.dump"))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, ctr.ID, gotID)
	require.Equal(t, []string{"PGPASSWORD=" + backupPass}, gotEnv)
	var data agentprotocol.DBBackupResult
	require.NoError(t, json.Unmarshal(res.Data, &data))
	require.EqualValues(t, 5000, data.SizeBytes)
	require.NotContains(t, string(res.Data), backupPass)

	file := filepath.Join(r.eng.cfg.BackupDir, "nightly", "p", "e", "pg", "one.dump")
	st, err := os.Stat(file)
	require.NoError(t, err)
	require.EqualValues(t, 5000, st.Size())
	_, err = os.Stat(file + ".manifest.json")
	require.NoError(t, err)
}

func TestDBBackupFailureLeavesNothing(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.BackupDir = t.TempDir()
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	r.dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
		_, _ = stdout.Write([]byte("PGDMP partial"))
		_, _ = io.WriteString(stderr, "pg_dump: error: server closed the connection for "+backupPass)
		return 1, nil
	}
	res := r.eng.HandleCommand(context.Background(), backupCmd(t, "p/e/pg/bad.dump"))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "server closed the connection")
	require.NotContains(t, res.Error, backupPass)
	require.NotContains(t, string(res.Data), backupPass)
	var files []string
	_ = filepath.WalkDir(r.eng.cfg.BackupDir, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	require.Empty(t, files, "a failed dump must leave no file, not even a partial one")
}

func TestDBBackupNeedsRunningDatabase(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.BackupDir = t.TempDir()
	res := r.eng.HandleCommand(context.Background(), backupCmd(t, "p/e/pg/none.dump"))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "not running")

	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, false, time.Hour) // exited container
	res = r.eng.HandleCommand(context.Background(), backupCmd(t, "p/e/pg/none.dump"))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
}

func TestBackupDeleteAndTestCommands(t *testing.T) {
	r := newRig(t, nil)
	r.eng.cfg.BackupDir = t.TempDir()
	dir := filepath.Join(r.eng.cfg.BackupDir, "nightly")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dump"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dump.manifest.json"), []byte("{}"), 0o600))
	dest := agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal, LocalDir: "nightly"}

	raw, _ := json.Marshal(agentprotocol.BackupTestPayload{Destination: dest, Prefix: "pre"})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "t1", Kind: agentprotocol.KindBackupTest, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	raw, _ = json.Marshal(agentprotocol.BackupDeletePayload{Destination: dest, ObjectKeys: []string{"a.dump"}})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "d1", Kind: agentprotocol.KindBackupDelete, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	_, err := os.Stat(filepath.Join(dir, "a.dump"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "a.dump.manifest.json"))
	require.True(t, os.IsNotExist(err))

	// Traversal is refused.
	raw, _ = json.Marshal(agentprotocol.BackupDeletePayload{Destination: dest, ObjectKeys: []string{"../../etc/passwd"}})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "d2", Kind: agentprotocol.KindBackupDelete, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
}
