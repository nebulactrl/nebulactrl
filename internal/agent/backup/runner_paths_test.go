package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// faultStore wraps a memStore and lets a test fail or alter single operations.
type faultStore struct {
	*memStore
	mu      sync.Mutex
	putErr  func(key string) error
	getErr  func(key string) error
	sizeFn  func(key string) (int64, error)
	delErr  func(key string) error
	deleted []string
	getBody func(key string) io.ReadCloser
}

func newFaultStore() *faultStore { return &faultStore{memStore: newMemStore()} }

func (f *faultStore) Put(ctx context.Context, key string, r io.Reader) error {
	if f.putErr != nil {
		if err := f.putErr(key); err != nil {
			_, _ = io.Copy(io.Discard, r)
			return err
		}
	}
	return f.memStore.Put(ctx, key, r)
}

func (f *faultStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if f.getErr != nil {
		if err := f.getErr(key); err != nil {
			return nil, err
		}
	}
	if f.getBody != nil {
		if b := f.getBody(key); b != nil {
			return b, nil
		}
	}
	return f.memStore.Get(ctx, key)
}

func (f *faultStore) Size(ctx context.Context, key string) (int64, error) {
	if f.sizeFn != nil {
		return f.sizeFn(key)
	}
	return f.memStore.Size(ctx, key)
}

func (f *faultStore) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, key)
	f.mu.Unlock()
	if f.delErr != nil {
		if err := f.delErr(key); err != nil {
			return err
		}
	}
	return f.memStore.Delete(ctx, key)
}

func TestBackupUsesInjectedClockInUTC(t *testing.T) {
	zone := time.FixedZone("x", 5*3600)
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, zone)
	r := newRunner(&fakeExec{stdout: []byte("data")}, newMemStore())
	r.Now = func() time.Time { return t0 }
	res, err := r.Backup(context.Background(), "c", pgPayload(), nil, func(string) {})
	require.NoError(t, err)
	require.True(t, res.StartedAt.Equal(t0))
	require.Equal(t, time.UTC, res.StartedAt.Location())
	require.Equal(t, time.UTC, res.FinishedAt.Location())
	require.Equal(t, "stored 4 bytes", res.Message)
	require.Equal(t, "p/e/db/20261026-b1.dump", res.ObjectKey)
	require.Equal(t, agentprotocol.FormatPGCustom, res.Format)

	// Without an injected clock the wall clock is used.
	res, err = newRunner(&fakeExec{stdout: []byte("data")}, newMemStore()).Backup(context.Background(), "c", pgPayload(), nil, func(string) {})
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), res.StartedAt, time.Minute)
}

func TestBackupManifestCarriesTargetAndMeta(t *testing.T) {
	st := newMemStore()
	p := pgPayload()
	p.Meta = agentprotocol.ManifestMeta{Project: "proj", Environment: "prod", Service: "db", ServiceID: "svc-1", Trigger: "schedule"}
	r := newRunner(&fakeExec{stdout: []byte("data")}, st)
	r.Now = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
	_, err := r.Backup(context.Background(), "c", p, nil, func(string) {})
	require.NoError(t, err)
	var man agentprotocol.BackupManifest
	require.NoError(t, json.Unmarshal(st.objs[ManifestKey(p.ObjectKey)], &man))
	require.Equal(t, agentprotocol.BackupManifest{
		Version: 1, BackupID: "b1", Engine: "postgres", EngineVersion: "17", Database: "app",
		Format: agentprotocol.FormatPGCustom, ObjectKey: p.ObjectKey, SizeBytes: 4, SHA256: man.SHA256,
		CreatedAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), Meta: p.Meta,
	}, man)
	require.Len(t, man.SHA256, 64)
}

func TestBackupFailsWhenStoreCannotBeBuilt(t *testing.T) {
	r := &Runner{Docker: &fakeExec{}, Stores: func(agentprotocol.BackupDestination, map[string]string) (Store, error) {
		return nil, errors.New("bad destination for s3cr3t-db-pass")
	}}
	res, err := r.Backup(context.Background(), "c", pgPayload(), secretsPG, func(string) {})
	require.EqualError(t, err, "bad destination for ••••••")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, "bad destination for ••••••", res.Message)
	require.False(t, res.FinishedAt.IsZero())
}

func TestBackupRejectsUnsupportedEngine(t *testing.T) {
	p := pgPayload()
	p.Target.Engine = "cassandra"
	ex := &fakeExec{}
	res, err := newRunner(ex, newMemStore()).Backup(context.Background(), "c", p, nil, func(string) {})
	require.EqualError(t, err, `backups are not supported for engine "cassandra"`)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Zero(t, ex.calls, "nothing ran in the container")
}

func TestBackupStorePutFailureIsReported(t *testing.T) {
	st := newFaultStore()
	st.putErr = func(key string) error { return errors.New("bucket unreachable") }
	res, err := newRunner(&fakeExec{stdout: bytes.Repeat([]byte("x"), 100)}, st).Backup(context.Background(), "c", pgPayload(), nil, func(string) {})
	require.EqualError(t, err, "bucket unreachable")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Empty(t, st.keys())
}

func TestBackupRemovesObjectWhenManifestCannotBeWritten(t *testing.T) {
	st := newFaultStore()
	st.putErr = func(key string) error {
		if strings.HasSuffix(key, ".manifest.json") {
			return errors.New("quota exceeded")
		}
		return nil
	}
	p := pgPayload()
	res, err := newRunner(&fakeExec{stdout: []byte("data")}, st).Backup(context.Background(), "c", p, nil, func(string) {})
	require.EqualError(t, err, "write manifest: quota exceeded")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, st.deleted, p.ObjectKey)
	require.Empty(t, st.keys(), "no object without its manifest stays behind")
}

func TestBackupVerifiesStoredSize(t *testing.T) {
	p := pgPayload()

	st := newFaultStore()
	st.sizeFn = func(key string) (int64, error) { return 3, nil }
	res, err := newRunner(&fakeExec{stdout: []byte("data")}, st).Backup(context.Background(), "c", p, nil, func(string) {})
	require.EqualError(t, err, "verify stored object: stored size 3 differs from streamed size 4")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Empty(t, st.keys(), "object and manifest are both removed")
	require.Contains(t, st.deleted, p.ObjectKey)
	require.Contains(t, st.deleted, ManifestKey(p.ObjectKey))

	st = newFaultStore()
	st.sizeFn = func(key string) (int64, error) { return 0, ErrNotFound }
	_, err = newRunner(&fakeExec{stdout: []byte("data")}, st).Backup(context.Background(), "c", p, nil, func(string) {})
	require.EqualError(t, err, "verify stored object: "+ErrNotFound.Error())
	require.Empty(t, st.keys())
}

func TestStderrTailKeepsLastBytes(t *testing.T) {
	s := &stderrTail{}
	_, _ = s.Write([]byte("  early\n"))
	require.Equal(t, "early", s.String(), "surrounding whitespace is trimmed")
	_, _ = s.Write([]byte(strings.Repeat("a", stderrMax)))
	_, _ = s.Write([]byte("END"))
	got := s.String()
	require.Len(t, got, stderrMax)
	require.True(t, strings.HasSuffix(got, "aaEND"))
	require.NotContains(t, got, "early")
}

func TestScrubMasksOnlySecretsOfFourOrMoreCharacters(t *testing.T) {
	secrets := map[string]string{"a": "abc", "b": "longsecret", "c": ""}
	require.Equal(t, "abc and •••••• and ••••••", scrub("abc and longsecret and longsecret", secrets))
	require.Equal(t, "nothing", scrub("nothing", nil))
}

func TestCountingWriterCounts(t *testing.T) {
	c := &countingWriter{}
	n, err := c.Write([]byte("abc"))
	require.NoError(t, err)
	require.Equal(t, 3, n)
	_, _ = c.Write([]byte("de"))
	require.Equal(t, int64(5), c.n)
}

func restorePayload(engine, format string) agentprotocol.DBRestorePayload {
	return agentprotocol.DBRestorePayload{
		RestoreID: "0a1b2c3d-1111-2222-3333-444455556666",
		Target:    agentprotocol.BackupTarget{Engine: engine, Database: "app", User: "root"},
		Format:    format,
		ObjectKey: "p/e/db/x.dump",
	}
}

func TestRestoreRejectsInvalidKeyAndFactoryFailure(t *testing.T) {
	p := restorePayload("mongodb", agentprotocol.FormatMongoArchive)
	p.ObjectKey = "../etc/passwd"
	ex := &fakeExec{}
	res, err := newRunner(ex, newMemStore()).Restore(context.Background(), "c", p, nil, func(string) {})
	require.Error(t, err)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Zero(t, ex.calls)

	p.ObjectKey = "p/x.dump"
	r := &Runner{Docker: ex, Stores: func(agentprotocol.BackupDestination, map[string]string) (Store, error) {
		return nil, errors.New("no creds")
	}}
	res, err = r.Restore(context.Background(), "c", p, nil, func(string) {})
	require.EqualError(t, err, "no creds")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.False(t, res.FinishedAt.IsZero())
}

func TestRestoreFailureModes(t *testing.T) {
	gz := gzipBytes(t, "SELECT 1;")
	tests := []struct {
		name    string
		payload agentprotocol.DBRestorePayload
		object  []byte // nil: object is missing
		exec    *fakeExec
		wantErr string
		ranExec bool
	}{
		{
			name: "object is missing", payload: restorePayload("mongodb", agentprotocol.FormatMongoArchive),
			exec: &fakeExec{}, wantErr: "open backup: " + ErrNotFound.Error(),
		},
		{
			name: "mysql object that is not gzip", payload: restorePayload("mysql", agentprotocol.FormatMySQLGz),
			object: []byte("plain text"), exec: &fakeExec{}, wantErr: "backup is not a gzip stream: ",
		},
		{
			name: "client transport error", payload: restorePayload("mysql", agentprotocol.FormatMySQLGz),
			object: gz, exec: &fakeExec{err: errors.New("docker gone")}, wantErr: "run mysql: docker gone", ranExec: true,
		},
		{
			name: "client exits non-zero without output", payload: restorePayload("mongodb", agentprotocol.FormatMongoArchive),
			object: []byte("archive"), exec: &fakeExec{exit: 9}, wantErr: "mongorestore exited with status 9: no error output", ranExec: true,
		},
		{
			name: "client exits non-zero with stderr", payload: restorePayload("mongodb", agentprotocol.FormatMongoArchive),
			object: []byte("archive"), exec: &fakeExec{exit: 1, stderr: "E11000 duplicate key"}, wantErr: "mongorestore exited with status 1: E11000 duplicate key", ranExec: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newMemStore()
			if tc.object != nil {
				st.objs[tc.payload.ObjectKey] = tc.object
			}
			res, err := newRunner(tc.exec, st).Restore(context.Background(), "c", tc.payload, nil, func(string) {})
			require.Error(t, err)
			require.True(t, strings.HasPrefix(err.Error(), tc.wantErr), err.Error())
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Equal(t, err.Error(), res.Message)
			require.False(t, res.FinishedAt.IsZero())
			require.Equal(t, tc.ranExec, tc.exec.calls > 0)
		})
	}
}

func TestRestoreChecksumCoversUnreadTail(t *testing.T) {
	// A client that stops reading early must not hide a checksum mismatch.
	data := []byte("archive-bytes")
	st := newMemStore()
	p := restorePayload("mongodb", agentprotocol.FormatMongoArchive)
	st.objs[p.ObjectKey] = data
	p.SHA256 = strings.Repeat("0", 64)
	res, err := newRunner(&fakeExec{}, st).Restore(context.Background(), "c", p, nil, func(string) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum mismatch")
	require.Contains(t, err.Error(), "recorded "+strings.Repeat("0", 64))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
}

func TestRestoreSucceedsAndStreamsObjectToClient(t *testing.T) {
	st := newMemStore()
	p := restorePayload("mongodb", agentprotocol.FormatMongoArchive)
	st.objs[p.ObjectKey] = []byte("archive-bytes")
	ex := &fakeExec{}
	var lines []string
	r := newRunner(ex, st)
	r.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	res, err := r.Restore(context.Background(), "c", p, map[string]string{agentprotocol.SecretDBPassword: "pw-123"}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, "restored", res.Message)
	require.Equal(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), res.FinishedAt)
	require.Equal(t, "archive-bytes", string(ex.gotStdin))
	require.Equal(t, []string{"restoring into mongodb with mongorestore", "restored"}, lines)
}

func TestRestorePostgresSwitchOverFailureReported(t *testing.T) {
	st := newMemStore()
	p := restorePayload("postgres", agentprotocol.FormatPGCustom)
	p.Target.User = "nebula"
	st.objs[p.ObjectKey] = []byte("PGDMP")
	ex := &scriptExec{answer: pgFailOn(func(s string) (string, bool) {
		return "conn lost", strings.Contains(s, "SELECT 1 FROM pg_database")
	})}
	res, err := scriptRunner(ex, st).Restore(context.Background(), "c", p, nil, func(string) {})
	require.EqualError(t, err, "could not check the live database: conn lost")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	stmts := ex.statements()
	require.Equal(t, dropScratch, stmts[len(stmts)-1])
}

func TestRestorePostgresAnalyzeFailureDoesNotFailRestore(t *testing.T) {
	st := newMemStore()
	p := restorePayload("postgres", agentprotocol.FormatPGCustom)
	p.Target.User = "nebula"
	st.objs[p.ObjectKey] = []byte("PGDMP")
	ex := &scriptExec{answer: pgFailOn(func(s string) (string, bool) { return "no stats", s == "ANALYZE" })}
	var lg logLines
	res, err := scriptRunner(ex, st).Restore(context.Background(), "c", p, nil, lg.log)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Contains(t, lg.joined(), "ANALYZE failed (the restore is kept): no stats")
	require.Contains(t, ex.statements(), renameIn)
}

func TestRestorePostgresChecksumMismatchDropsScratchBeforeSwap(t *testing.T) {
	st := newMemStore()
	p := restorePayload("postgres", agentprotocol.FormatPGCustom)
	p.Target.User = "nebula"
	p.SHA256 = strings.Repeat("a", 64)
	st.objs[p.ObjectKey] = []byte("PGDMP")
	ex := &scriptExec{answer: existsAnswer}
	_, err := scriptRunner(ex, st).Restore(context.Background(), "c", p, nil, func(string) {})
	require.ErrorContains(t, err, "checksum mismatch")
	stmts := ex.statements()
	require.Equal(t, dropScratch, stmts[len(stmts)-1])
	for _, s := range stmts {
		require.NotContains(t, s, "RENAME", "an unverified dump never replaces the database")
	}
}

func TestDeleteRemovesManifestBeforeObject(t *testing.T) {
	st := newFaultStore()
	st.objs["a/1.dump"], st.objs["a/1.dump.manifest.json"] = []byte("x"), []byte("m")
	st.objs["a/2.dump"], st.objs["a/2.dump.manifest.json"] = []byte("x"), []byte("m")
	res, err := newRunner(&fakeExec{}, st).Delete(context.Background(), agentprotocol.BackupDeletePayload{ObjectKeys: []string{"a/1.dump", "a/2.dump"}}, nil)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, "deleted 2 backup(s)", res.Message)
	require.Equal(t, []string{"a/1.dump.manifest.json", "a/1.dump", "a/2.dump.manifest.json", "a/2.dump"}, st.deleted)
	require.Empty(t, st.keys())
}

func TestDeleteCollectsAllFailuresAndMasksSecrets(t *testing.T) {
	st := newFaultStore()
	st.delErr = func(key string) error {
		if key == "a/1.dump" || key == "a/2.dump.manifest.json" {
			return errors.New("denied " + key + " tok-12345")
		}
		return nil
	}
	res, err := newRunner(&fakeExec{}, st).Delete(context.Background(), agentprotocol.BackupDeletePayload{ObjectKeys: []string{"a/1.dump", "a/2.dump"}},
		map[string]string{"k": "tok-12345"})
	require.EqualError(t, err, "denied a/1.dump ••••••; denied a/2.dump.manifest.json ••••••")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, err.Error(), res.Message)
	require.Contains(t, st.deleted, "a/2.dump", "one failure does not stop the remaining deletions")
}

func TestDeleteFactoryFailure(t *testing.T) {
	r := &Runner{Docker: &fakeExec{}, Stores: func(agentprotocol.BackupDestination, map[string]string) (Store, error) {
		return nil, errors.New("no store tok-12345")
	}}
	res, err := r.Delete(context.Background(), agentprotocol.BackupDeletePayload{ObjectKeys: []string{"a"}}, map[string]string{"k": "tok-12345"})
	require.Error(t, err)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Equal(t, "no store ••••••", res.Message)
}

func TestDestinationTestFailureSteps(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*faultStore)
		wantErr string
	}{
		{"write fails", func(s *faultStore) { s.putErr = func(string) error { return errors.New("read-only") } }, "write: read-only"},
		{"stat fails", func(s *faultStore) { s.sizeFn = func(string) (int64, error) { return 0, errors.New("head denied") } }, "stat: head denied"},
		{"stat reports a different size", func(s *faultStore) { s.sizeFn = func(string) (int64, error) { return 1, nil } }, "stat: stored 1 bytes, wrote "},
		{"read fails", func(s *faultStore) { s.getErr = func(string) error { return errors.New("get denied") } }, "read: get denied"},
		{"read returns another object", func(s *faultStore) {
			s.getBody = func(string) io.ReadCloser { return io.NopCloser(strings.NewReader("different")) }
		}, "read back a different object than was written"},
		{"delete fails", func(s *faultStore) { s.delErr = func(string) error { return errors.New("no delete") } }, "delete: no delete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newFaultStore()
			tc.setup(st)
			res, err := newRunner(&fakeExec{}, st).Test(context.Background(), agentprotocol.BackupTestPayload{Prefix: "pfx"}, nil)
			require.Error(t, err)
			require.True(t, strings.HasPrefix(err.Error(), tc.wantErr), err.Error())
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Equal(t, err.Error(), res.Message)
		})
	}
}

func TestDestinationTestCleansUpProbeAfterFailedStep(t *testing.T) {
	st := newFaultStore()
	st.getErr = func(string) error { return errors.New("get denied") }
	_, err := newRunner(&fakeExec{}, st).Test(context.Background(), agentprotocol.BackupTestPayload{Prefix: "pfx"}, nil)
	require.Error(t, err)
	require.Empty(t, st.keys(), "the probe object is removed even though the test failed")
	require.Len(t, st.deleted, 1)
	require.True(t, strings.HasPrefix(st.deleted[0], "pfx/.nebulactrl-probe-"))
}

func TestDestinationTestFactoryFailureAndSuccess(t *testing.T) {
	r := &Runner{Docker: &fakeExec{}, Stores: func(agentprotocol.BackupDestination, map[string]string) (Store, error) {
		return nil, errors.New("no store tok-12345")
	}}
	res, err := r.Test(context.Background(), agentprotocol.BackupTestPayload{}, map[string]string{"k": "tok-12345"})
	require.EqualError(t, err, "no store ••••••")
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)

	st := newFaultStore()
	res, err = newRunner(&fakeExec{}, st).Test(context.Background(), agentprotocol.BackupTestPayload{Prefix: "pfx"}, nil)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome)
	require.Equal(t, "destination is writable, readable and deletable", res.Message)
	require.Empty(t, st.keys())
}

func TestProbeKey(t *testing.T) {
	require.Equal(t, "pfx/.nebulactrl-probe-abc", probeKey("pfx", "abc"))
	require.Equal(t, ".nebulactrl-probe-abc", probeKey("", "abc"))
}
