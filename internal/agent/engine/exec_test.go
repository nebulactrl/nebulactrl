package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- fakes ----

type streamFrame struct {
	typ, id string
	data    []byte
}

// streamRec is a Sender that also records stream frames.
type streamRec struct {
	*fakeSender
	smu    sync.Mutex
	frames []streamFrame
}

func (r *streamRec) Stream(typ, id string, body any) bool {
	data, err := agentprotocol.Encode(typ, id, body)
	if err != nil {
		return false
	}
	r.smu.Lock()
	r.frames = append(r.frames, streamFrame{typ: typ, id: id, data: data})
	r.smu.Unlock()
	return true
}

// of returns the decoded bodies of every frame of typ and id.
func (r *streamRec) of(typ, id string) []agentprotocol.Frame {
	r.smu.Lock()
	defer r.smu.Unlock()
	var out []agentprotocol.Frame
	for _, f := range r.frames {
		if f.typ == typ && f.id == id {
			fr, _ := agentprotocol.Decode(f.data)
			out = append(out, fr)
		}
	}
	return out
}

func (r *streamRec) waitFor(t *testing.T, typ, id string, n int) []agentprotocol.Frame {
	t.Helper()
	var got []agentprotocol.Frame
	require.Eventually(t, func() bool {
		got = r.of(typ, id)
		return len(got) >= n
	}, 3*time.Second, 5*time.Millisecond, "waiting for %d %s frames", n, typ)
	return got
}

type fakeTTY struct {
	out      chan []byte
	mu       sync.Mutex
	rest     []byte
	input    bytes.Buffer
	resizes  [][2]int
	closed   bool
	exitCode int
	echo     bool
}

func newFakeTTY(echo bool) *fakeTTY { return &fakeTTY{out: make(chan []byte, 4096), echo: echo} }

func (f *fakeTTY) Read(p []byte) (int, error) {
	f.mu.Lock()
	if len(f.rest) > 0 {
		n := copy(p, f.rest)
		f.rest = f.rest[n:]
		f.mu.Unlock()
		return n, nil
	}
	f.mu.Unlock()
	b, ok := <-f.out
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, b)
	f.mu.Lock()
	f.rest = append(f.rest, b[n:]...)
	f.mu.Unlock()
	return n, nil
}

func (f *fakeTTY) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.input.Write(p)
	f.mu.Unlock()
	if f.echo {
		f.out <- append([]byte("echo:"), p...)
	}
	return len(p), nil
}

func (f *fakeTTY) Resize(_ context.Context, cols, rows int) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	f.mu.Unlock()
	return nil
}

func (f *fakeTTY) Wait(context.Context) (int, error) { return f.exitCode, nil }

func (f *fakeTTY) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.out)
	}
	return nil
}

func (f *fakeTTY) isClosed() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.closed }

func (f *fakeTTY) gotInput() string { f.mu.Lock(); defer f.mu.Unlock(); return f.input.String() }

func (f *fakeTTY) sizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resizes...)
}

// termFake adds the Terminal surface to fakeDocker.
type termFake struct {
	*fakeDocker
	tmu     sync.Mutex
	ttys    []*fakeTTY
	nextTTY *fakeTTY
	argvs   [][]string
	stats   map[string]docker.PathStat
	archive map[string][]byte // path -> tar archive
}

func (d *termFake) ContainerExecTTY(_ context.Context, _ string, spec docker.TTYSpec) (docker.TTYSession, error) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	t := d.nextTTY
	if t == nil {
		t = newFakeTTY(true)
	}
	d.nextTTY = nil
	d.ttys = append(d.ttys, t)
	d.argvs = append(d.argvs, spec.Argv)
	return t, nil
}

func (d *termFake) ContainerStatPath(_ context.Context, _, p string) (docker.PathStat, error) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	if st, ok := d.stats[p]; ok {
		return st, nil
	}
	return docker.PathStat{}, notFound(p)
}

func (d *termFake) CopyFromContainer(_ context.Context, _, p string) (io.ReadCloser, docker.PathStat, error) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	if b, ok := d.archive[p]; ok {
		return io.NopCloser(bytes.NewReader(b)), d.stats[p], nil
	}
	return nil, docker.PathStat{}, notFound(p)
}

func (d *termFake) tty(i int) *fakeTTY {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	return d.ttys[i]
}

func tarOf(t *testing.T, files map[string][]byte, dirs ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, d := range dirs {
		require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d, Mode: 0o755}))
	}
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

const (
	termSvc  = "11111111-1111-1111-1111-111111111111"
	termDep  = "22222222-2222-2222-2222-222222222222"
	termName = "nc-web-22222222-0"
)

func termSetup(t *testing.T) (*Engine, *termFake, *streamRec) {
	t.Helper()
	d := &termFake{fakeDocker: newFakeDocker(), stats: map[string]docker.PathStat{}, archive: map[string][]byte{}}
	c := d.seed(termName, termSvc, termDep, true, time.Minute)
	c.Labels[agentprotocol.LabelReplica] = "0"
	rec := &streamRec{fakeSender: &fakeSender{}}
	e, err := New(Config{Docker: d, Proxy: &fakeProxy{}, Sender: rec})
	require.NoError(t, err)
	require.NotNil(t, e.stream, "the engine must pick the stream sender up from Config.Sender")
	return e, d, rec
}

func frameOf(t *testing.T, typ, id string, body any) agentprotocol.Frame {
	t.Helper()
	data, err := agentprotocol.Encode(typ, id, body)
	require.NoError(t, err)
	f, err := agentprotocol.Decode(data)
	require.NoError(t, err)
	return f
}

func decode[T any](t *testing.T, f agentprotocol.Frame) T {
	t.Helper()
	var v T
	require.NoError(t, f.Into(&v))
	return v
}

func openTerm(t *testing.T, e *Engine, rec *streamRec, sid string) agentprotocol.ExecOpened {
	t.Helper()
	e.HandleStream(frameOf(t, agentprotocol.TypeExecOpen, sid, agentprotocol.ExecOpen{SessionID: sid, ServiceID: termSvc, Replica: 0, Cols: 100, Rows: 30}))
	return decode[agentprotocol.ExecOpened](t, rec.waitFor(t, agentprotocol.TypeExecOpened, sid, 1)[0])
}

// ---- terminal ----

func TestTerminalSessionInputOutputResizeClose(t *testing.T) {
	e, d, rec := termSetup(t)
	opened := openTerm(t, e, rec, "s1")
	require.Empty(t, opened.Error)
	require.Equal(t, termName, opened.Container)
	require.Equal(t, []string{"sh", "-c", autoShell}, d.argvs[0], "an empty command picks bash, else sh")

	e.HandleStream(frameOf(t, agentprotocol.TypeExecInput, "s1", agentprotocol.ExecInput{SessionID: "s1", Data: []byte("ls\n")}))
	out := decode[agentprotocol.ExecOutput](t, rec.waitFor(t, agentprotocol.TypeExecOutput, "s1", 1)[0])
	require.Equal(t, "echo:ls\n", string(out.Data))
	require.Equal(t, "ls\n", d.tty(0).gotInput())

	e.HandleStream(frameOf(t, agentprotocol.TypeExecResize, "s1", agentprotocol.ExecResize{SessionID: "s1", Cols: 132, Rows: 43}))
	require.Eventually(t, func() bool { return len(d.tty(0).sizes()) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, [2]int{132, 43}, d.tty(0).sizes()[0])

	e.HandleStream(frameOf(t, agentprotocol.TypeStreamAck, "s1", agentprotocol.StreamAck{StreamID: "s1", Bytes: len(out.Data)}))
	e.HandleStream(frameOf(t, agentprotocol.TypeExecClose, "s1", agentprotocol.ExecClose{SessionID: "s1"}))
	require.Eventually(t, d.tty(0).isClosed, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return e.term.session("s1") == nil }, 2*time.Second, 5*time.Millisecond, "a closed session is forgotten")
	require.Empty(t, rec.of(agentprotocol.TypeExecExit, "s1"), "the control plane closed it: no exit frame is owed")
}

func TestTerminalReportsExit(t *testing.T) {
	e, d, rec := termSetup(t)
	d.nextTTY = newFakeTTY(false)
	d.nextTTY.exitCode = 3
	require.Empty(t, openTerm(t, e, rec, "s2").Error)
	d.tty(0).out <- []byte("bye\n")
	_ = d.tty(0).Close() // the shell exited
	exit := decode[agentprotocol.ExecExit](t, rec.waitFor(t, agentprotocol.TypeExecExit, "s2", 1)[0])
	require.Equal(t, 3, exit.ExitCode)
	require.Empty(t, exit.Error)
	require.Equal(t, "bye\n", string(decode[agentprotocol.ExecOutput](t, rec.waitFor(t, agentprotocol.TypeExecOutput, "s2", 1)[0]).Data))
}

func TestTerminalNeedsARunningReplica(t *testing.T) {
	e, d, rec := termSetup(t)
	d.fakeDocker.containers[d.find(termName).ID].Running = false
	opened := openTerm(t, e, rec, "s3")
	require.Equal(t, "not_found", opened.Code)
	require.NotEmpty(t, opened.Error)
	require.Empty(t, d.ttys, "nothing is started in a stopped replica")

	// A container that is not the service's own replica is unreachable too.
	d.seed("stranger", "99999999-9999-9999-9999-999999999999", termDep, true, time.Minute)
	e.HandleStream(frameOf(t, agentprotocol.TypeExecOpen, "s4", agentprotocol.ExecOpen{SessionID: "s4", ServiceID: "99999999-9999-9999-9999-999999999999", Replica: 3}))
	require.Equal(t, "not_found", decode[agentprotocol.ExecOpened](t, rec.waitFor(t, agentprotocol.TypeExecOpened, "s4", 1)[0]).Code)
}

func TestTerminalOutputHonorsTheStreamWindow(t *testing.T) {
	e, d, rec := termSetup(t)
	d.nextTTY = newFakeTTY(false)
	require.Empty(t, openTerm(t, e, rec, "s5").Error)
	// 2 MiB of output with no acknowledgement: only a window's worth goes out.
	chunk := bytes.Repeat([]byte("x"), agentprotocol.ExecChunk)
	for range 128 {
		d.tty(0).out <- chunk
	}
	time.Sleep(300 * time.Millisecond)
	var sent int
	for _, f := range rec.of(agentprotocol.TypeExecOutput, "s5") {
		sent += len(decode[agentprotocol.ExecOutput](t, f).Data)
	}
	require.LessOrEqual(t, sent, agentprotocol.StreamWindow, "unacknowledged output must stay within the window")
	require.Greater(t, sent, 0)

	// Acknowledging lets the rest through.
	ackd := 0
	deadline := time.Now().Add(5 * time.Second)
	for total := 0; total < 128*agentprotocol.ExecChunk; {
		require.True(t, time.Now().Before(deadline), "the output did not drain")
		total = 0
		for _, f := range rec.of(agentprotocol.TypeExecOutput, "s5") {
			total += len(decode[agentprotocol.ExecOutput](t, f).Data)
		}
		if total > ackd {
			e.HandleStream(frameOf(t, agentprotocol.TypeStreamAck, "s5", agentprotocol.StreamAck{StreamID: "s5", Bytes: total - ackd}))
			ackd = total
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.HandleStream(frameOf(t, agentprotocol.TypeExecClose, "s5", agentprotocol.ExecClose{SessionID: "s5"}))
}

func TestDisconnectEndsEverySession(t *testing.T) {
	e, d, rec := termSetup(t)
	require.Empty(t, openTerm(t, e, rec, "s6").Error)
	e.Disconnected()
	require.Eventually(t, d.tty(0).isClosed, 2*time.Second, 5*time.Millisecond)
}

func TestCreditGate(t *testing.T) {
	g := newCreditGate()
	require.NoError(t, g.Acquire(context.Background(), agentprotocol.StreamWindow))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, g.Acquire(ctx, 1), context.DeadlineExceeded, "a full window blocks")
	go func() { time.Sleep(20 * time.Millisecond); g.Ack(agentprotocol.StreamWindow) }()
	require.NoError(t, g.Acquire(context.Background(), 10))
	g.Close()
	require.ErrorIs(t, g.Acquire(context.Background(), 1), agentprotocol.ErrWindowClosed)
	// A chunk bigger than the window still goes out when nothing is in flight.
	require.NoError(t, newCreditGate().Acquire(context.Background(), agentprotocol.StreamWindow*2))
}

// ---- file browser ----

func fsReq(t *testing.T, e *Engine, rec *streamRec, id string, req agentprotocol.FSRequest) {
	t.Helper()
	req.RequestID, req.ServiceID = id, termSvc
	e.HandleStream(frameOf(t, agentprotocol.TypeFSRequest, id, req))
}

func fsResp(t *testing.T, rec *streamRec, id string, n int) agentprotocol.FSResponse {
	t.Helper()
	return decode[agentprotocol.FSResponse](t, rec.waitFor(t, agentprotocol.TypeFSResponse, id, n)[n-1])
}

func TestFileListThroughTheShell(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/app"] = docker.PathStat{Name: "app", Mode: os.ModeDir | 0o755}
	var gotArgv []string
	d.onExec = func(_ string, argv []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		gotArgv = argv
		_, _ = io.WriteString(stdout,
			"regular file\t12\t1700000000\t644\tb.txt\t\n"+
				"directory\t4096\t1700000001\t755\tZ-dir\t\n"+
				"symbolic link\t5\t1700000002\t777\tlink\t/etc/hosts\n"+
				"regular file\t1\t1700000003\t600\ta.txt\t\n"+
				"garbage line\n")
		return 0, nil
	}
	fsReq(t, e, rec, "f1", agentprotocol.FSRequest{Op: agentprotocol.FSList, Path: "/app/../app/"})
	r := fsResp(t, rec, "f1", 1)
	require.Empty(t, r.Error)
	require.Equal(t, agentprotocol.FSDone, r.Stage)
	require.Equal(t, "/app", r.Path)
	require.Equal(t, []string{"sh", "-c", listScript, "sh", "/app"}, gotArgv, "the path is an argument, never interpolated into the script")
	var names []string
	for _, en := range r.Entries {
		names = append(names, en.Type+":"+en.Name)
	}
	require.Equal(t, []string{"dir:Z-dir", "file:a.txt", "file:b.txt", "link:link"}, names, "directories first, then by name")
	require.Equal(t, "/etc/hosts", r.Entries[3].LinkTarget)
	require.Equal(t, "0644", r.Entries[2].Mode)
	require.Equal(t, int64(12), r.Entries[2].Size)
}

func TestFileListTruncates(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/big"] = docker.PathStat{Mode: os.ModeDir | 0o755}
	d.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		for i := range agentprotocol.MaxListEntries + 50 {
			if _, err := fmt.Fprintf(stdout, "regular file\t1\t1\t644\tf%05d\t\n", i); err != nil {
				return -1, fmt.Errorf("exec stream: %w", err)
			}
		}
		return 0, nil
	}
	fsReq(t, e, rec, "f2", agentprotocol.FSRequest{Op: agentprotocol.FSList, Path: "/big"})
	r := fsResp(t, rec, "f2", 1)
	require.True(t, r.Truncated)
	require.Len(t, r.Entries, agentprotocol.MaxListEntries)
}

func TestFileListFallsBackToTheArchive(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/srv"] = docker.PathStat{Mode: os.ModeDir | 0o755}
	d.onExec = func(string, []string, docker.ExecSpec, io.Writer, io.Writer) (int, error) {
		return -1, errors.New(`exec attach: OCI runtime exec failed: exec: "sh": executable file not found in $PATH`)
	}
	d.archive["/srv/."] = tarOf(t, map[string][]byte{"./app": []byte("binary"), "./sub/deep.txt": []byte("x")}, "./", "./sub/")
	fsReq(t, e, rec, "f3", agentprotocol.FSRequest{Op: agentprotocol.FSList, Path: "/srv"})
	r := fsResp(t, rec, "f3", 1)
	require.Empty(t, r.Error)
	var names []string
	for _, en := range r.Entries {
		names = append(names, en.Type+":"+en.Name)
	}
	require.Equal(t, []string{"dir:sub", "file:app"}, names, "only the immediate children, from the archive")
}

func TestFileListErrors(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/etc/hosts"] = docker.PathStat{Mode: 0o644, Size: 3}
	fsReq(t, e, rec, "e1", agentprotocol.FSRequest{Op: agentprotocol.FSList, Path: "/missing"})
	require.Equal(t, agentprotocol.FSErrNotFound, fsResp(t, rec, "e1", 1).Code)
	fsReq(t, e, rec, "e2", agentprotocol.FSRequest{Op: agentprotocol.FSList, Path: "/etc/hosts"})
	require.Equal(t, agentprotocol.FSErrNotDir, fsResp(t, rec, "e2", 1).Code)
	fsReq(t, e, rec, "e3", agentprotocol.FSRequest{Op: agentprotocol.FSList, Path: "relative/path"})
	require.Contains(t, fsResp(t, rec, "e3", 1).Error, "absolute")
}

func TestFileReadStreamsInChunksUnderTheWindow(t *testing.T) {
	e, d, rec := termSetup(t)
	body := bytes.Repeat([]byte("0123456789abcdef"), 40_000) // 640 KB
	d.stats["/data/log.txt"] = docker.PathStat{Name: "log.txt", Mode: 0o644, Size: int64(len(body))}
	d.archive["/data/log.txt"] = tarOf(t, map[string][]byte{"log.txt": body})
	fsReq(t, e, rec, "r1", agentprotocol.FSRequest{Op: agentprotocol.FSRead, Path: "/data/log.txt"})
	resp := fsResp(t, rec, "r1", 1)
	require.Equal(t, agentprotocol.FSReady, resp.Stage)
	require.Equal(t, "log.txt", resp.Entry.Name)
	require.Equal(t, int64(len(body)), resp.Entry.Size)

	var got []byte
	ackd, maxUnacked := 0, 0
	deadline := time.Now().Add(5 * time.Second)
	for eof := false; !eof; {
		require.True(t, time.Now().Before(deadline), "the read did not finish")
		got = got[:0]
		for _, f := range rec.of(agentprotocol.TypeFSData, "r1") {
			dd := decode[agentprotocol.FSData](t, f)
			require.Empty(t, dd.Error)
			got = append(got, dd.Data...)
			eof = eof || dd.EOF
		}
		maxUnacked = max(maxUnacked, len(got)-ackd)
		if len(got) > ackd {
			e.HandleStream(frameOf(t, agentprotocol.TypeStreamAck, "r1", agentprotocol.StreamAck{StreamID: "r1", Bytes: len(got) - ackd}))
			ackd = len(got)
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.LessOrEqual(t, maxUnacked, agentprotocol.StreamWindow+agentprotocol.FSChunk, "never more than a window unacknowledged")
	require.Equal(t, body, got)
}

func TestFileReadRefusals(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/dir"] = docker.PathStat{Mode: os.ModeDir | 0o755}
	d.stats["/huge"] = docker.PathStat{Mode: 0o644, Size: 10 << 20}
	fsReq(t, e, rec, "x1", agentprotocol.FSRequest{Op: agentprotocol.FSRead, Path: "/dir"})
	require.Equal(t, agentprotocol.FSErrIsDir, fsResp(t, rec, "x1", 1).Code)
	fsReq(t, e, rec, "x2", agentprotocol.FSRequest{Op: agentprotocol.FSRead, Path: "/huge", MaxBytes: 1 << 20})
	require.Equal(t, agentprotocol.FSErrTooLarge, fsResp(t, rec, "x2", 1).Code)
}

func TestFileReadFollowsSymlinks(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/cur"] = docker.PathStat{Name: "cur", Mode: os.ModeSymlink | 0o777, LinkTarget: "releases/7/app.conf"}
	d.stats["/releases/7/app.conf"] = docker.PathStat{Name: "app.conf", Mode: 0o644, Size: 5}
	d.archive["/releases/7/app.conf"] = tarOf(t, map[string][]byte{"app.conf": []byte("hello")})
	fsReq(t, e, rec, "l1", agentprotocol.FSRequest{Op: agentprotocol.FSRead, Path: "/cur"})
	resp := fsResp(t, rec, "l1", 1)
	require.Empty(t, resp.Error)
	require.Equal(t, "/releases/7/app.conf", resp.Path, "a relative link resolves against its own directory")
}

func TestFileUpload(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/data"] = docker.PathStat{Mode: os.ModeDir | 0o755}
	payload := []byte("uploaded bytes")
	fsReq(t, e, rec, "u1", agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Path: "/data/new.txt", Size: int64(len(payload)), Mode: 0o600})
	require.Equal(t, agentprotocol.FSReady, fsResp(t, rec, "u1", 1).Stage)

	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "u1", agentprotocol.FSData{RequestID: "u1", Data: payload[:6]}))
	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "u1", agentprotocol.FSData{RequestID: "u1", Data: payload[6:], EOF: true}))
	done := fsResp(t, rec, "u1", 2)
	require.Empty(t, done.Error)
	require.Equal(t, agentprotocol.FSDone, done.Stage)
	require.Equal(t, "/data/new.txt", done.Path)

	acks := 0
	for _, f := range rec.of(agentprotocol.TypeStreamAck, "u1") {
		acks += decode[agentprotocol.StreamAck](t, f).Bytes
	}
	require.Equal(t, len(payload), acks, "every consumed byte is credited back")

	require.Len(t, d.copies, 1)
	require.Equal(t, "/data", d.copies[0].dir)
	tr := tar.NewReader(bytes.NewReader(d.copies[0].data))
	hdr, err := tr.Next()
	require.NoError(t, err)
	require.Equal(t, "new.txt", hdr.Name)
	require.Equal(t, int64(0o600), hdr.Mode)
	got, _ := io.ReadAll(tr)
	require.Equal(t, payload, got)
}

func TestFileUploadRefusals(t *testing.T) {
	e, d, rec := termSetup(t)
	d.stats["/data"] = docker.PathStat{Mode: os.ModeDir | 0o755}
	d.stats["/file"] = docker.PathStat{Mode: 0o644}
	fsReq(t, e, rec, "w1", agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Path: "/data/x", Size: agentprotocol.MaxUploadBytes + 1})
	require.Equal(t, agentprotocol.FSErrTooLarge, fsResp(t, rec, "w1", 1).Code)
	fsReq(t, e, rec, "w2", agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Path: "/nowhere/x", Size: 1})
	require.Equal(t, agentprotocol.FSErrNotFound, fsResp(t, rec, "w2", 1).Code)
	fsReq(t, e, rec, "w3", agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Path: "/file/x", Size: 1})
	require.Equal(t, agentprotocol.FSErrNotDir, fsResp(t, rec, "w3", 1).Code)
	fsReq(t, e, rec, "w4", agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Path: "/data/..", Size: 1})
	require.Equal(t, agentprotocol.FSErrFailed, fsResp(t, rec, "w4", 1).Code, "the path must name a file")

	// More data than announced aborts and stores nothing.
	fsReq(t, e, rec, "w5", agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Path: "/data/y", Size: 3})
	require.Equal(t, agentprotocol.FSReady, fsResp(t, rec, "w5", 1).Stage)
	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "w5", agentprotocol.FSData{RequestID: "w5", Data: []byte("toolong"), EOF: true}))
	r := fsResp(t, rec, "w5", 2)
	require.Contains(t, r.Error, "more data than announced")
	require.Empty(t, d.copies)
}

func TestCleanContainerPath(t *testing.T) {
	for in, want := range map[string]string{
		"/":                 "/",
		"/etc/../etc/hosts": "/etc/hosts",
		"/a//b/":            "/a/b",
		"/../../etc":        "/etc",
	} {
		got, err := CleanContainerPath(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "relative", "./x", "/a\x00b", "/" + strings.Repeat("a", maxPathLen)} {
		_, err := CleanContainerPath(bad)
		require.Error(t, err, bad)
	}
}

func TestParseListLine(t *testing.T) {
	e, ok := parseListLine("regular empty file\t0\t5\t644\tempty\t")
	require.True(t, ok)
	require.Equal(t, agentprotocol.FSTypeFile, e.Type)
	e, ok = parseListLine("fifo\t0\t5\t644\tpipe\t")
	require.True(t, ok)
	require.Equal(t, agentprotocol.FSTypeOther, e.Type)
	_, ok = parseListLine("not enough\tfields")
	require.False(t, ok)
}
