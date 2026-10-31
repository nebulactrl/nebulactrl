package backups

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

func tarHeader(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	require.NoError(t, w.WriteHeader(&tar.Header{Name: "toc.dat", Mode: 0o644, Size: 3, Format: tar.FormatUSTAR}))
	_, _ = w.Write([]byte("abc"))
	require.NoError(t, w.Close())
	return b.Bytes()
}

func TestSniffDump(t *testing.T) {
	mongo := append([]byte{0x6d, 0xe2, 0x99, 0x81}, bytes.Repeat([]byte{1, 2, 3}, 20)...)
	cases := []struct {
		name    string
		in      []byte
		want    DumpKind
		wantErr string
	}{
		{"pg custom", append([]byte("PGDMP\x01\x0e"), bytes.Repeat([]byte{0}, 100)...), DumpKind{Format: agentprotocol.DumpPGCustom}, ""},
		{"pg tar", tarHeader(t), DumpKind{Format: agentprotocol.DumpPGTar}, ""},
		{"mongo archive", mongo, DumpKind{Format: agentprotocol.DumpMongo}, ""},
		{"mongo gzip", gz(t, string(mongo)), DumpKind{Format: agentprotocol.DumpMongo, Gzip: true}, ""},
		{"pg plain", []byte("--\n-- PostgreSQL database dump\n--\nSET x = 1;\n"), DumpKind{Format: agentprotocol.DumpSQL, Dialect: "postgres"}, ""},
		{"pg plain gzip", gz(t, "--\n-- PostgreSQL database dump\n--\nSET x = 1;\n"), DumpKind{Format: agentprotocol.DumpSQL, Dialect: "postgres", Gzip: true}, ""},
		{"mysql plain", []byte("-- MySQL dump 10.13  Distrib 8.4\nCREATE TABLE t (a int);\n"), DumpKind{Format: agentprotocol.DumpSQL, Dialect: "mysql"}, ""},
		{"mariadb gzip", gz(t, "/*M!999999\\- enable the sandbox mode */\n-- MariaDB dump 10.19\n"), DumpKind{Format: agentprotocol.DumpSQL, Dialect: "mysql", Gzip: true}, ""},
		{"headerless sql", []byte("CREATE TABLE t (a int);\nINSERT INTO t VALUES (1);\n"), DumpKind{Format: agentprotocol.DumpSQL}, ""},
		{"sql cut inside a rune", append([]byte("SELECT 'h"), 0xc3), DumpKind{Format: agentprotocol.DumpSQL}, ""},
		{"empty", nil, DumpKind{}, "the file is empty"},
		{"redis", []byte("REDIS0011\xfa\x09redis-ver"), DumpKind{}, "RDB"},
		{"utf16", []byte{0xff, 0xfe, 'S', 0, 'E', 0}, DumpKind{}, "UTF-16"},
		{"zip", []byte("PK\x03\x04rest"), DumpKind{}, "zip"},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 1, 2, 3}, DumpKind{}, "zstd"},
		{"binary", []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}, DumpKind{}, "unrecognised"},
		{"json", []byte(`{"a": 1}`), DumpKind{}, "JSON"},
		{"pg_dumpall", []byte("--\n-- PostgreSQL database cluster dump\n--\n"), DumpKind{}, "pg_dumpall"},
		{"broken gzip", []byte{0x1f, 0x8b, 0x00}, DumpKind{}, "gzip"},
		{"double gzip", gz(t, string(gz(t, "SELECT 1;"))), DumpKind{}, "twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SniffDump(c.in)
			if c.wantErr != "" {
				ae, ok := apierr.As(err)
				require.True(t, ok, "%v", err)
				require.Equal(t, "unsupported_dump_format", ae.Code)
				require.Contains(t, ae.Message, c.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestCheckDumpEngine(t *testing.T) {
	pgc := DumpKind{Format: agentprotocol.DumpPGCustom}
	sql := DumpKind{Format: agentprotocol.DumpSQL}
	mongo := DumpKind{Format: agentprotocol.DumpMongo, Gzip: true}
	for _, ok := range []struct {
		engine string
		k      DumpKind
	}{
		{"postgres", pgc}, {"postgres", DumpKind{Format: agentprotocol.DumpPGTar}}, {"postgres", sql},
		{"postgres", DumpKind{Format: agentprotocol.DumpSQL, Dialect: "postgres", Gzip: true}},
		{"mysql", sql}, {"mariadb", DumpKind{Format: agentprotocol.DumpSQL, Dialect: "mysql"}},
		{"mongodb", mongo},
	} {
		require.NoError(t, CheckDumpEngine(ok.engine, ok.k), "%s %+v", ok.engine, ok.k)
	}
	for _, bad := range []struct {
		engine, want string
		k            DumpKind
	}{
		{"postgres", "MySQL/MariaDB SQL dump", DumpKind{Format: agentprotocol.DumpSQL, Dialect: "mysql"}},
		{"postgres", "MongoDB archive", mongo},
		{"mysql", "PostgreSQL custom-format", pgc},
		{"mariadb", "PostgreSQL plain SQL dump", DumpKind{Format: agentprotocol.DumpSQL, Dialect: "postgres"}},
		{"mongodb", "plain SQL dump", sql},
		{"valkey", "valkey", sql},
	} {
		err := CheckDumpEngine(bad.engine, bad.k)
		ae, ok := apierr.As(err)
		require.True(t, ok, "%s %+v", bad.engine, bad.k)
		require.Equal(t, 422, ae.Status)
		require.Contains(t, ae.Message, bad.want)
	}
}

func TestHumanBytesAndCleanFileName(t *testing.T) {
	require.Equal(t, "5.0 GiB", humanBytes(5<<30))
	require.Equal(t, "512 B", humanBytes(512))
	require.Equal(t, "dump.pgdump", cleanFileName(`C:\Users\me\dump.pgdump`))
	require.Equal(t, "a.sql", cleanFileName("/tmp/x/a.sql"))
	require.Equal(t, "ab", cleanFileName("a\x00b\n"))
	require.Len(t, cleanFileName(strings.Repeat("x", 500)), 200)
}

// ---- the flow, against a real database and a scripted agent ----

// dumpAgent scripts the agent side of dump restore streams.
type dumpAgent struct {
	mu       sync.Mutex
	requests []agentprotocol.DumpRestoreRequest
	data     bytes.Buffer
	eof      bool
	aborted  bool
	// onRequest answers the request; the default answers ready.
	onRequest func(fs *volFakeStream, req agentprotocol.DumpRestoreRequest)
	// onEOF runs when the last chunk arrived; the default reports done.
	onEOF func(fs *volFakeStream)
	lines []string
}

func (a *dumpAgent) hook(fs *volFakeStream, typ string, raw json.RawMessage) {
	switch typ {
	case agentprotocol.TypeDumpRestoreRequest:
		var req agentprotocol.DumpRestoreRequest
		_ = json.Unmarshal(raw, &req)
		a.mu.Lock()
		a.requests = append(a.requests, req)
		fn := a.onRequest
		a.mu.Unlock()
		if fn != nil {
			fn(fs, req)
			return
		}
		fs.push(agentprotocol.TypeDumpRestoreEvent, agentprotocol.DumpRestoreEvent{RequestID: req.RequestID, Stage: agentprotocol.DumpReady})
	case agentprotocol.TypeFSData:
		var d agentprotocol.FSData
		_ = json.Unmarshal(raw, &d)
		a.mu.Lock()
		if d.Error != "" {
			a.aborted = true
		}
		a.data.Write(d.Data)
		if d.EOF {
			a.eof = true
		}
		fn := a.onEOF
		a.mu.Unlock()
		if len(d.Data) > 0 {
			fs.push(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: d.RequestID, Bytes: len(d.Data)})
		}
		if d.EOF {
			if fn != nil {
				fn(fs)
				return
			}
			fs.push(agentprotocol.TypeDumpRestoreEvent, agentprotocol.DumpRestoreEvent{
				RequestID: d.RequestID, Lines: []string{"restoring into the scratch database", "restored"}, Stage: agentprotocol.DumpDone,
			})
		}
	}
}

func (a *dumpAgent) received() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.data.String(), a.eof
}

func (f *fixture) dumpAgent() *dumpAgent {
	a := &dumpAgent{}
	f.svc.streams = &volFakeStreams{hook: a.hook}
	return a
}

// awaitingDump creates a restore into a new database and advances it to the
// point where it awaits its file. It returns the restore and the new database.
func (f *fixture) awaitingDump(src db.Service) (DumpRestoreView, db.Service) {
	f.t.Helper()
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Name: "pg-copy", FileName: "dump.pgdump"})
	require.NoError(f.t, err)
	require.Equal(f.t, RestoreProvisioning, rv.Status)
	require.NotNil(f.t, rv.TargetServiceID)
	sib, err := f.getService(*rv.TargetServiceID)
	require.NoError(f.t, err)
	dep, err := f.q.GetDumpRestoreInternal(context.Background(), rv.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, dep.DeploymentID)
	require.NoError(f.t, f.q.MarkServiceDeployed(context.Background(), db.MarkServiceDeployedParams{ID: sib.ID, CurrentDeploymentID: dep.DeploymentID}))
	done, err := f.svc.AdvanceDumpRestore(context.Background(), rv.ID)
	require.NoError(f.t, err)
	require.True(f.t, done)
	got, err := f.svc.GetDumpRestore(f.ctx, rv.ID)
	require.NoError(f.t, err)
	require.Equal(f.t, RestoreAwaitingUpload, got.Status)
	sib, err = f.getService(sib.ID)
	require.NoError(f.t, err)
	return got, sib
}

func (f *fixture) waitDump(id uuid.UUID, status string) DumpRestoreView {
	f.t.Helper()
	var v DumpRestoreView
	require.Eventually(f.t, func() bool {
		var err error
		v, err = f.svc.GetDumpRestore(f.ctx, id)
		return err == nil && v.Status == status
	}, 5*time.Second, 10*time.Millisecond, "restore %s never reached %s (last %s: %s)", id, status, v.Status, v.Error)
	return v
}

var pgDump = append([]byte("PGDMP\x01\x0e"), bytes.Repeat([]byte("row-data;"), 40000)...) // ~360 KB: several chunks

func up(body []byte) DumpUpload {
	return DumpUpload{FileName: "/home/me/prod.pgdump", Size: int64(len(body)), Body: bytes.NewReader(body)}
}

func dumpCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestDumpRestoreToNewDatabaseStreamsTheFileToTheAgent(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	ag := f.dumpAgent()
	aw, sib := f.awaitingDump(src)
	require.Equal(t, DefaultDumpMaxBytes, aw.MaxUploadBytes)

	v, err := f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.NoError(t, err)
	require.Contains(t, []string{RestoreRestoring, RestoreSucceeded}, v.Status)
	done := f.waitDump(aw.ID, RestoreSucceeded)

	got, eof := ag.received()
	require.True(t, eof)
	require.Equal(t, string(pgDump), got, "the agent received the file byte for byte")
	sum := sha256.Sum256(pgDump)
	require.Equal(t, hex.EncodeToString(sum[:]), done.SHA256)
	require.Equal(t, "prod.pgdump", done.FileName, "only the base name is kept")
	require.Equal(t, agentprotocol.DumpPGCustom, done.Format)
	require.False(t, done.Gzip)
	require.EqualValues(t, len(pgDump), done.SizeBytes)
	require.EqualValues(t, len(pgDump), done.BytesReceived)
	require.Contains(t, done.Log, "restored")
	require.Contains(t, done.Log, "upload complete")
	require.NotNil(t, done.FinishedAt)
	require.NotEmpty(t, f.events)
	require.Equal(t, NotifyRestoreSucceeded, f.events[len(f.events)-1].Type)

	// The request addressed the NEW database with its own password.
	require.Len(t, ag.requests, 1)
	req := ag.requests[0]
	require.Equal(t, sib.ID.String(), req.Target.ServiceID)
	require.Equal(t, "postgres", req.Target.Engine)
	require.Equal(t, agentprotocol.DumpPGCustom, req.Format)
	require.False(t, req.Replace, "a new database holds nothing to replace")
	require.EqualValues(t, len(pgDump), req.Size)
	pass := f.variable(sib.ID, "POSTGRES_PASSWORD")
	require.Equal(t, pass, req.Secrets[agentprotocol.SecretDBPassword])
	require.NotEqual(t, canaryDBPass, pass)

	// The password is never persisted: not in the restore, not in a command, not in the audit log.
	row, err := f.q.GetDumpRestoreInternal(context.Background(), aw.ID)
	require.NoError(t, err)
	blob, _ := json.Marshal(row)
	require.NotContains(t, string(blob), pass)
	for _, tbl := range []string{"agent_commands", "audit_events"} {
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+tbl+" WHERE to_jsonb("+tbl+")::text LIKE '%'||$1||'%'", pass).Scan(&n))
		require.Zero(t, n, tbl)
	}

	// Audited: create, upload, success.
	for _, action := range []string{ActionDumpRestoreCreate, ActionDumpRestoreUpload, ActionDumpRestoreDone} {
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2", action, aw.ID.String()).Scan(&n))
		require.Equal(t, 1, n, action)
	}

	// A finished restore takes no second file.
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "dump_restore_finished", dumpCode(t, err))
}

func TestDumpRestoreReportsTheEngineFailure(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	ag.onEOF = func(fs *volFakeStream) {
		fs.push(agentprotocol.TypeDumpRestoreEvent, agentprotocol.DumpRestoreEvent{
			Lines: []string{"pg_restore: error: could not execute query"}, Error: "pg_restore exited with status 1: relation \"x\" already exists", Code: agentprotocol.DumpErrFailed,
		})
	}
	aw, _ := f.awaitingDump(f.database("pg"))
	_, err := f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.NoError(t, err)
	v := f.waitDump(aw.ID, RestoreFailed)
	require.Contains(t, v.Error, "already exists")
	require.Contains(t, v.Log, "could not execute query")
	require.Equal(t, NotifyRestoreFailed, f.events[len(f.events)-1].Type)
}

func TestDumpUploadRefusesWhatDoesNotFitBeforeAnythingChanges(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	aw, _ := f.awaitingDump(f.database("pg"))

	// A MySQL dump for a PostgreSQL database.
	_, err := f.svc.UploadDump(f.ctx, aw.ID, up([]byte("-- MySQL dump 10.13\nCREATE TABLE t (a int);\n")))
	require.Equal(t, "unsupported_dump_format", dumpCode(t, err))
	// A file that is not a dump at all.
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up([]byte{0, 1, 2, 3, 4, 5, 6, 7}))
	require.Equal(t, "unsupported_dump_format", dumpCode(t, err))
	// A body shorter than its Content-Length.
	_, err = f.svc.UploadDump(f.ctx, aw.ID, DumpUpload{Size: 1000, Body: bytes.NewReader([]byte("PGDMP"))})
	require.Equal(t, "short_body", dumpCode(t, err))
	// No length.
	_, err = f.svc.UploadDump(f.ctx, aw.ID, DumpUpload{Size: 0, Body: bytes.NewReader(nil)})
	require.Equal(t, "length_required", dumpCode(t, err))
	// Over the limit.
	f.svc.dumpMax = 1000
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "file_too_large", dumpCode(t, err))
	f.svc.dumpMax = 0

	require.Empty(t, ag.requests, "the agent is not asked before the file was checked")
	v, err := f.svc.GetDumpRestore(f.ctx, aw.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreAwaitingUpload, v.Status, "the restore still takes the right file")

	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.NoError(t, err)
	f.waitDump(aw.ID, RestoreSucceeded)
}

func TestDumpUploadNeedsAReadyTarget(t *testing.T) {
	f := newFixture(t)
	f.dumpAgent()
	src := f.database("pg")
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{})
	require.NoError(t, err)
	_, err = f.svc.UploadDump(f.ctx, rv.ID, up(pgDump))
	require.Equal(t, "dump_restore_not_ready", dumpCode(t, err))

	// Offline server.
	aw, _ := f.awaitingDump(src)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET status = 'offline' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "server_offline", dumpCode(t, err))
	v, _ := f.svc.GetDumpRestore(f.ctx, aw.ID)
	require.Equal(t, RestoreAwaitingUpload, v.Status)
}

func TestDumpUploadOnlyOneFileWins(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	hold := make(chan struct{})
	ag.onEOF = func(fs *volFakeStream) {
		<-hold
		fs.push(agentprotocol.TypeDumpRestoreEvent, agentprotocol.DumpRestoreEvent{Stage: agentprotocol.DumpDone})
	}
	aw, _ := f.awaitingDump(f.database("pg"))
	go func() { _, _ = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump)) }()
	f.waitDump(aw.ID, RestoreRestoring)
	_, err := f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "dump_restore_in_progress", dumpCode(t, err))
	// A running restore cannot be canceled.
	_, err = f.svc.CancelDumpRestore(f.ctx, aw.ID)
	require.Equal(t, "dump_restore_running", dumpCode(t, err))
	close(hold)
	f.waitDump(aw.ID, RestoreSucceeded)
}

func TestDumpUploadAgentRefusalBeforeDataKeepsTheTarget(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	ag.onRequest = func(fs *volFakeStream, req agentprotocol.DumpRestoreRequest) {
		fs.push(agentprotocol.TypeDumpRestoreEvent, agentprotocol.DumpRestoreEvent{
			RequestID: req.RequestID, Error: "the database is not running on this host", Code: agentprotocol.DumpErrNotRunning,
		})
	}
	aw, _ := f.awaitingDump(f.database("pg"))
	_, err := f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "database_not_running", dumpCode(t, err))
	v := f.waitDump(aw.ID, RestoreAwaitingUpload)
	require.Contains(t, v.Error, "not running")
	got, _ := ag.received()
	require.Empty(t, got)

	// The same restore takes the file once the agent is fine again.
	ag.onRequest = nil
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.NoError(t, err)
	done := f.waitDump(aw.ID, RestoreSucceeded)
	require.Empty(t, done.Error)
}

func TestDumpUploadAgentThatNeverAnswersTimesOut(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	ag.onRequest = func(*volFakeStream, agentprotocol.DumpRestoreRequest) {} // an old agent ignores the frame
	f.svc.dumpWait = 100 * time.Millisecond
	aw, _ := f.awaitingDump(f.database("pg"))
	_, err := f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "dump_restore_failed", dumpCode(t, err))
	v := f.waitDump(aw.ID, RestoreAwaitingUpload)
	require.Contains(t, v.Error, "too old")
}

type failingReader struct {
	r   io.Reader
	n   int
	err error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, f.err
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

func TestDumpUploadInterruptedMidwayFailsTheRestoreAndAbortsTheAgent(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	aw, _ := f.awaitingDump(f.database("pg"))
	body := &failingReader{r: bytes.NewReader(pgDump), n: 200_000, err: errors.New("connection reset")}
	_, err := f.svc.UploadDump(f.ctx, aw.ID, DumpUpload{Size: int64(len(pgDump)), Body: body})
	require.Equal(t, "short_body", dumpCode(t, err))
	v := f.waitDump(aw.ID, RestoreFailed)
	require.Contains(t, v.Error, "Nothing was switched over")
	ag.mu.Lock()
	aborted := ag.aborted
	ag.mu.Unlock()
	require.True(t, aborted, "the agent is told to abandon the restore")
}

func TestDumpRestoreInPlace(t *testing.T) {
	f := newFixture(t)
	ag := f.dumpAgent()
	src := f.database("pg")

	_, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: "nope"})
	require.Equal(t, "confirmation_required", dumpCode(t, err))
	_, err = f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: "pg"})
	require.Equal(t, "destination_required", dumpCode(t, err), "the safety backup needs somewhere to go")
	require.Empty(t, f.disp.cmds)

	dest := f.s3Destination()
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: "pg", DestinationID: &dest.ID, FileName: "x.pgdump"})
	require.NoError(t, err)
	require.Equal(t, RestorePreBackup, rv.Status)
	require.NotNil(t, rv.PreBackupID)
	require.Len(t, f.disp.cmds, 1)
	cmd, preID := f.disp.last()
	require.Equal(t, agentprotocol.KindDBBackup, cmd.Kind, "the safety backup runs first")

	_, err = f.svc.UploadDump(f.ctx, rv.ID, up(pgDump))
	require.Equal(t, "dump_restore_not_ready", dumpCode(t, err))

	data := agentprotocol.DBBackupResult{Outcome: "succeeded", SizeBytes: 77, SHA256: strings.Repeat("c", 64), Format: agentprotocol.FormatPGCustom}
	require.NoError(t, f.svc.HandleResult(f.ctx, preID, f.result(preID, "succeeded", "", data)))
	got, err := f.svc.GetDumpRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreAwaitingUpload, got.Status)
	require.Len(t, f.disp.cmds, 1, "no db_restore command: the file goes through the stream")

	_, err = f.svc.UploadDump(f.ctx, rv.ID, up(pgDump))
	require.NoError(t, err)
	f.waitDump(rv.ID, RestoreSucceeded)
	require.Len(t, ag.requests, 1)
	require.Equal(t, src.ID.String(), ag.requests[0].Target.ServiceID)
	require.True(t, ag.requests[0].Replace)
	require.Equal(t, canaryDBPass, ag.requests[0].Secrets[agentprotocol.SecretDBPassword])
}

func TestDumpRestoreInPlaceAbortsWhenSafetyBackupFails(t *testing.T) {
	f := newFixture(t)
	f.dumpAgent()
	src := f.database("pg")
	dest := f.s3Destination()
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: "pg", DestinationID: &dest.ID})
	require.NoError(t, err)
	_, preID := f.disp.last()
	require.NoError(t, f.svc.HandleResult(f.ctx, preID, f.result(preID, "failed", "disk full", agentprotocol.DBBackupResult{})))
	got, err := f.svc.GetDumpRestore(f.ctx, rv.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "nothing was changed")
	_, err = f.svc.UploadDump(f.ctx, rv.ID, up(pgDump))
	require.Equal(t, "dump_restore_finished", dumpCode(t, err))
}

func TestDumpRestoreInPlaceUsesTheScheduleDestinationByDefault(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	dest := f.s3Destination()
	_, err := f.svc.CreateSchedule(f.ctx, src.ID, ScheduleInput{DestinationID: dest.ID, Cron: "0 3 * * *"})
	require.NoError(t, err)
	rv, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Mode: ModeInPlace, ConfirmSlug: "pg"})
	require.NoError(t, err)
	require.Equal(t, RestorePreBackup, rv.Status)
	var p agentprotocol.DBBackupPayload
	cmd, _ := f.disp.last()
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, dest.ID.String(), p.Destination.ID)
}

func TestDumpRestoreCreateGuards(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	_, err := f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{Mode: "sideways"})
	require.Equal(t, "validation_failed", dumpCode(t, err))
	_, err = f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{SizeBytes: DefaultDumpMaxBytes + 1})
	require.Equal(t, "file_too_large", dumpCode(t, err))
	require.Empty(t, f.dep.deploys, "a file that is too large is refused before a database is created")

	// A service that is not a managed database.
	app := f.app("web", false)
	_, err = f.svc.CreateDumpRestore(f.ctx, app.ID, DumpRestoreInput{})
	require.Equal(t, "not_a_managed_database", dumpCode(t, err))

	// Valkey has no dump restore.
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{database,engine}', '"valkey"') WHERE id = $1`, src.ID)
	require.NoError(t, err)
	_, err = f.svc.CreateDumpRestore(f.ctx, src.ID, DumpRestoreInput{})
	require.Equal(t, "restore_unsupported", dumpCode(t, err))
}

func TestDumpRestoreNeedsWriteScopes(t *testing.T) {
	f := newFixture(t)
	src := f.database("pg")
	ro := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, WorkspaceID: f.ws.ID, Label: "ro", Scopes: []string{policy.ScopeProjectsRead},
	})
	_, err := f.svc.CreateDumpRestore(ro, src.ID, DumpRestoreInput{})
	require.Error(t, err)
	ae, _ := apierr.As(err)
	require.Equal(t, 403, ae.Status)
	_, err = f.svc.UploadDump(ro, uuid.New(), up(pgDump))
	require.Error(t, err)
	_, err = f.svc.CancelDumpRestore(ro, uuid.New())
	require.Error(t, err)
}

func TestCancelDumpRestore(t *testing.T) {
	f := newFixture(t)
	f.dumpAgent()
	aw, _ := f.awaitingDump(f.database("pg"))
	v, err := f.svc.CancelDumpRestore(f.ctx, aw.ID)
	require.NoError(t, err)
	require.Equal(t, RestoreFailed, v.Status)
	require.Contains(t, v.Error, "Canceled")
	_, err = f.svc.CancelDumpRestore(f.ctx, aw.ID)
	require.Equal(t, "dump_restore_finished", dumpCode(t, err))
	_, err = f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.Equal(t, "dump_restore_finished", dumpCode(t, err))
}

func TestDumpRestoreFailsWhenTheNewDatabaseCannotBeDeployed(t *testing.T) {
	f := newFixture(t)
	f.dep.status = "failed"
	rv, err := f.svc.CreateDumpRestore(f.ctx, f.database("pg").ID, DumpRestoreInput{})
	require.NoError(t, err)
	done, err := f.svc.AdvanceDumpRestore(context.Background(), rv.ID)
	require.NoError(t, err)
	require.True(t, done)
	got, _ := f.svc.GetDumpRestore(f.ctx, rv.ID)
	require.Equal(t, RestoreFailed, got.Status)
	require.Contains(t, got.Error, "could not be deployed")

	f.dep.status = "building"
	rv, err = f.svc.CreateDumpRestore(f.ctx, f.database("pg2").ID, DumpRestoreInput{})
	require.NoError(t, err)
	done, err = f.svc.AdvanceDumpRestore(context.Background(), rv.ID)
	require.NoError(t, err)
	require.False(t, done, "still waiting for the deployment")
}

func TestSweepFailsOrphanedAndForgottenRestores(t *testing.T) {
	f := newFixture(t)
	f.dumpAgent()
	src := f.database("pg")
	age := func(id uuid.UUID, d time.Duration) {
		_, err := f.pool.Exec(context.Background(), "UPDATE dump_restores SET updated_at = now() - $2::interval WHERE id = $1", id, d.String())
		require.NoError(t, err)
	}

	// Awaiting its file for too long.
	aw, _ := f.awaitingDump(src)
	age(aw.ID, 3*time.Hour)
	// Recently created: kept.
	fresh, _ := f.awaitingDump(f.database("pg2"))
	// An upload nobody drives (the control plane restarted).
	orphan, _ := f.awaitingDump(f.database("pg3"))
	_, err := f.pool.Exec(context.Background(), "UPDATE dump_restores SET status = 'restoring' WHERE id = $1", orphan.ID)
	require.NoError(t, err)
	age(orphan.ID, 10*time.Minute)
	// A live one (this process owns it) is never swept.
	live, _ := f.awaitingDump(f.database("pg4"))
	_, err = f.pool.Exec(context.Background(), "UPDATE dump_restores SET status = 'uploading' WHERE id = $1", live.ID)
	require.NoError(t, err)
	age(live.ID, 10*time.Minute)
	f.svc.dumpActive.Store(live.ID, struct{}{})

	require.NoError(t, f.svc.SweepDumpRestores(context.Background()))
	for id, want := range map[uuid.UUID]string{aw.ID: RestoreFailed, fresh.ID: RestoreAwaitingUpload, orphan.ID: RestoreFailed, live.ID: RestoreUploading} {
		v, err := f.svc.GetDumpRestore(f.ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, v.Status, id.String())
	}
	v, _ := f.svc.GetDumpRestore(f.ctx, orphan.ID)
	require.Contains(t, v.Error, "restarted")
}

func TestListDumpRestoresOmitsTheLog(t *testing.T) {
	f := newFixture(t)
	f.dumpAgent()
	src := f.database("pg")
	aw, _ := f.awaitingDump(src)
	_, err := f.svc.UploadDump(f.ctx, aw.ID, up(pgDump))
	require.NoError(t, err)
	f.waitDump(aw.ID, RestoreSucceeded)
	items, err := f.svc.ListDumpRestores(f.ctx, src.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Empty(t, items[0].Log)
	full, _ := f.svc.GetDumpRestore(f.ctx, aw.ID)
	require.NotEmpty(t, full.Log)
}
