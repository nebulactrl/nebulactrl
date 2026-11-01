package backups

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type volSentFrame struct {
	typ string
	raw json.RawMessage
}

// volFakeStream is the agent's side of one file stream: onSend lets a test
// answer the request.
type volFakeStream struct {
	id     string
	frames chan agentprotocol.Frame
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	sent   []volSentFrame
	onSend func(fs *volFakeStream, typ string, raw json.RawMessage)
}

func (f *volFakeStream) Frames() <-chan agentprotocol.Frame { return f.frames }
func (f *volFakeStream) Done() <-chan struct{}              { return f.done }
func (f *volFakeStream) Close()                             { f.once.Do(func() { close(f.done) }) }
func (f *volFakeStream) Send(typ string, body any) error {
	raw, _ := json.Marshal(body)
	f.mu.Lock()
	f.sent = append(f.sent, volSentFrame{typ, raw})
	hook := f.onSend
	f.mu.Unlock()
	if hook != nil {
		hook(f, typ, raw)
	}
	return nil
}

func (f *volFakeStream) push(typ string, body any) {
	data, _ := agentprotocol.Encode(typ, f.id, body)
	fr, _ := agentprotocol.Decode(data)
	f.frames <- fr
}

func (f *volFakeStream) sentOf(typ string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for _, s := range f.sent {
		if s.typ == typ {
			out = append(out, s.raw)
		}
	}
	return out
}

type volFakeStreams struct {
	err     error
	hook    func(fs *volFakeStream, typ string, raw json.RawMessage)
	streams []*volFakeStream
	server  uuid.UUID
}

func (a *volFakeStreams) OpenStream(serverID uuid.UUID, id string) (Stream, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.server = serverID
	fs := &volFakeStream{id: id, frames: make(chan agentprotocol.Frame, 64), done: make(chan struct{}), onSend: a.hook}
	a.streams = append(a.streams, fs)
	return fs, nil
}

// answers makes the fake agent reply to a volfile_request with frames.
func answers(fn func(fs *volFakeStream, req agentprotocol.VolFileRequest)) func(*volFakeStream, string, json.RawMessage) {
	return func(fs *volFakeStream, typ string, raw json.RawMessage) {
		if typ != agentprotocol.TypeVolFileRequest {
			return
		}
		var req agentprotocol.VolFileRequest
		_ = json.Unmarshal(raw, &req)
		fn(fs, req)
	}
}

func (f *fixture) succeededVolumeBackup() (uuid.UUID, VolumeBackupView) {
	f.t.Helper()
	dest := f.s3Destination()
	app := f.app("web", true)
	f.volConfig(app, dest, nil)
	b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
	require.NoError(f.t, err)
	f.finishBackup(b, snap('a'))
	return app.ID, b
}

func TestListVolumeBackupFilesStreamsTheRequestWithSecrets(t *testing.T) {
	f := newFixture(t)
	app, b := f.succeededVolumeBackup()
	fake := &volFakeStreams{}
	f.svc.streams = fake
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: req.RequestID, Stage: agentprotocol.FSDone, Path: req.Path, Truncated: true,
			Entries: []agentprotocol.FSEntry{{Name: "db", Type: agentprotocol.FSTypeDir}, {Name: "a.txt", Type: agentprotocol.FSTypeFile, Size: 12, Mode: "0644", MTime: 1700000000}},
		})
	})

	got, err := f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/sub//dir/")
	require.NoError(t, err)
	require.Equal(t, "/sub/dir", got.Path)
	require.True(t, got.Truncated)
	require.Len(t, got.Entries, 2)
	require.Equal(t, "a.txt", got.Entries[1].Name)
	require.EqualValues(t, 12, got.Entries[1].Size)
	require.NotNil(t, got.Entries[1].ModifiedAt)
	require.Equal(t, f.server.ID, fake.server)

	reqs := fake.streams[0].sentOf(agentprotocol.TypeVolFileRequest)
	require.Len(t, reqs, 1)
	var req agentprotocol.VolFileRequest
	require.NoError(t, json.Unmarshal(reqs[0], &req))
	require.Equal(t, agentprotocol.VolFileList, req.Op)
	require.Equal(t, snap('a'), req.SnapshotID)
	require.Equal(t, "data", req.Volume)
	require.Equal(t, app.String(), req.ServiceID)
	require.Equal(t, "nebula/prod/volumes/"+app.String(), req.Repo.Path)
	require.Equal(t, canarySecret, req.Secrets[agentprotocol.SecretBackupSecretKey])
	require.NotEmpty(t, req.Secrets[agentprotocol.SecretResticPassword])
	require.NotEmpty(t, fake.streams[0].sentOf(agentprotocol.TypeExecClose), "the stream is closed with an exec_close")

	// Validation.
	_, err = f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "other", "/")
	status, _ := apiCode(t, err)
	require.Equal(t, 422, status)
	_, err = f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/a/../b")
	_, code := apiCode(t, err)
	require.Equal(t, "invalid_path", code)

	// Offline server, and no stream support at all.
	fake.err = agents.ErrNotConnected
	_, err = f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/")
	status, code = apiCode(t, err)
	require.Equal(t, 409, status)
	require.Equal(t, "server_offline", code)
	f.svc.streams = nil
	_, err = f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/")
	_, code = apiCode(t, err)
	require.Equal(t, "server_offline", code)

	// An agent error is mapped.
	f.svc.streams = fake
	fake.err = nil
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{RequestID: req.RequestID, Error: "no such path", Code: agentprotocol.FSErrNotFound})
	})
	_, err = f.svc.ListVolumeBackupFiles(f.ctx, b.ID, "data", "/missing")
	status, _ = apiCode(t, err)
	require.Equal(t, 404, status)
}

func TestDownloadVolumeFileAuditsBeforeBytesAndAborts(t *testing.T) {
	f := newFixture(t)
	_, b := f.succeededVolumeBackup()
	fake := &volFakeStreams{}
	f.svc.streams = fake
	f.svc.fsWait = 300 * time.Millisecond

	// A file: Content-Length and acks.
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		require.Equal(t, agentprotocol.VolFileRead, req.Op)
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: req.RequestID, Stage: agentprotocol.FSReady, Path: req.Path, Entry: &agentprotocol.FSEntry{Name: "a b.txt", Type: agentprotocol.FSTypeFile, Size: 11},
		})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: []byte("hello ")})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: []byte("world"), EOF: true})
	})
	rec := httptest.NewRecorder()
	require.NoError(t, f.svc.DownloadVolumeFile(f.ctx, rec, b.ID, "data", "/a b.txt", false))
	require.Equal(t, "hello world", rec.Body.String())
	require.Equal(t, "11", rec.Header().Get("Content-Length"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	require.Contains(t, rec.Header().Get("Content-Disposition"), "a%20b.txt")
	require.Len(t, fake.streams[0].sentOf(agentprotocol.TypeStreamAck), 2, "every frame is credited")
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = 'volume_backup.file.download'").Scan(&n))
	require.Equal(t, 1, n)

	// A directory as a zip: no Content-Length.
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		require.Equal(t, agentprotocol.VolFileArchive, req.Op)
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: req.RequestID, Stage: agentprotocol.FSReady, Path: req.Path, Entry: &agentprotocol.FSEntry{Name: "dir", Type: agentprotocol.FSTypeDir, Size: -1},
		})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: []byte("PK"), EOF: true})
	})
	rec = httptest.NewRecorder()
	require.NoError(t, f.svc.DownloadVolumeFile(f.ctx, rec, b.ID, "data", "/dir", true))
	require.Empty(t, rec.Header().Get("Content-Length"))
	require.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), "dir.zip")

	// A short body must not look complete.
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: req.RequestID, Stage: agentprotocol.FSReady, Path: req.Path, Entry: &agentprotocol.FSEntry{Name: "x", Type: agentprotocol.FSTypeFile, Size: 100},
		})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: []byte("short"), EOF: true})
	})
	err := f.svc.DownloadVolumeFile(f.ctx, httptest.NewRecorder(), b.ID, "data", "/x", false)
	require.ErrorIs(t, err, errVolumeAbort)

	// The stream breaking mid-transfer aborts too.
	fake.hook = answers(func(fs *volFakeStream, req agentprotocol.VolFileRequest) {
		fs.push(agentprotocol.TypeFSResponse, agentprotocol.FSResponse{
			RequestID: req.RequestID, Stage: agentprotocol.FSReady, Path: req.Path, Entry: &agentprotocol.FSEntry{Name: "x", Type: agentprotocol.FSTypeFile, Size: 100},
		})
		fs.push(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: []byte("part")})
	})
	inner := fake.hook
	fake.hook = func(fs *volFakeStream, typ string, raw json.RawMessage) {
		inner(fs, typ, raw)
		if typ == agentprotocol.TypeStreamAck {
			fs.Close() // the connection drops after the first chunk
		}
	}
	err = f.svc.DownloadVolumeFile(f.ctx, httptest.NewRecorder(), b.ID, "data", "/x", false)
	require.ErrorIs(t, err, errVolumeAbort)
}
