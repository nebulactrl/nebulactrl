package docker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveStream answers an upgrade request: it writes out, then either closes
// (hold=false, the process is done) or keeps the connection open and sends
// everything the client writes to in once the client hangs up.
func (d *fakeDaemon) serveStream(contentType string, out []byte, hold bool, in chan<- []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		h := hijack(d.t, w, contentType)
		go func() { <-d.done; _ = h.Close() }()
		defer h.Close()
		_, _ = h.Write(out)
		if !hold {
			return
		}
		data, _ := io.ReadAll(h.In)
		if in != nil {
			in <- data
		}
	}
}

func recv(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(asyncTimeout):
		require.FailNow(t, "timed out waiting for the daemon to see client data")
		return nil
	}
}

func signalled(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(asyncTimeout):
		require.FailNow(t, "timed out waiting for "+what)
	}
}

// ---- ContainerExec ----

func fakeExec(d *fakeDaemon, start http.HandlerFunc, exitCode int) {
	d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
	d.on("POST /exec/e1/start", start)
	d.onJSON("GET /exec/e1/json", 200, obj{"ID": "e1", "ContainerID": "c1", "Running": false, "ExitCode": exitCode})
}

func TestContainerExecDemultiplexesAndReturnsExitCode(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeExec(d, d.serveStream(multiplexedStream, muxed(t, stdoutFrame("out-1 "), stderrFrame("err-1"), stdoutFrame("out-2")), false, nil), 3)

	var stdout, stderr strings.Builder
	code, err := e.ContainerExec(context.Background(), "c1", []string{"sh", "-c", "run"},
		ExecSpec{User: "app", Env: []string{"TOKEN=s3cret"}}, &stdout, &stderr)
	require.NoError(t, err)
	assert.Equal(t, 3, code)
	assert.Equal(t, "out-1 out-2", stdout.String())
	assert.Equal(t, "err-1", stderr.String())

	var create container.ExecCreateRequest
	d.only("POST", "/containers/c1/exec").decode(t, &create)
	assert.Equal(t, "app", create.User)
	assert.Equal(t, []string{"TOKEN=s3cret"}, create.Env)
	assert.Equal(t, []string{"sh", "-c", "run"}, create.Cmd)
	assert.True(t, create.AttachStdout)
	assert.True(t, create.AttachStderr)
	assert.False(t, create.AttachStdin, "no stdin source means no stdin attachment")
	assert.False(t, create.Tty)

	var start container.ExecStartRequest
	d.only("POST", "/exec/e1/start").decode(t, &start)
	assert.False(t, start.Detach)
	assert.False(t, start.Tty)
	assert.Equal(t, []string{"POST /containers/c1/exec", "POST /exec/e1/start", "GET /exec/e1/json"}, d.calls())
}

func TestContainerExecForwardsStdinAndClosesIt(t *testing.T) {
	d, e := newFakeDaemon(t)
	d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
	d.onJSON("GET /exec/e1/json", 200, obj{"ID": "e1", "ExitCode": 0})
	d.on("POST /exec/e1/start", func(w http.ResponseWriter, _ *http.Request) {
		h := hijack(t, w, multiplexedStream)
		defer h.Close()
		// The process reads stdin to EOF, which only arrives if the engine closes the write side.
		data, _ := io.ReadAll(h.In)
		_, _ = h.Write(muxed(t, stdoutFrame("stdin was: "+string(data))))
	})

	var stdout strings.Builder
	code, err := e.ContainerExec(context.Background(), "c1", []string{"cat"},
		ExecSpec{Stdin: strings.NewReader("payload-bytes")}, &stdout, nil)
	require.NoError(t, err)
	assert.Zero(t, code)
	assert.Equal(t, "stdin was: payload-bytes", stdout.String())

	var create container.ExecCreateRequest
	d.only("POST", "/containers/c1/exec").decode(t, &create)
	assert.True(t, create.AttachStdin)
}

func TestContainerExecNilWritersDiscardOutput(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeExec(d, d.serveStream(multiplexedStream, muxed(t, stdoutFrame("x"), stderrFrame("y")), false, nil), 9)
	code, err := e.ContainerExec(context.Background(), "c1", []string{"true"}, ExecSpec{}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 9, code)
}

func TestContainerExecErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("empty argv", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		code, err := e.ContainerExec(ctx, "c1", nil, ExecSpec{}, nil, nil)
		require.EqualError(t, err, "exec: empty argv")
		assert.Equal(t, -1, code)
		assert.Empty(t, d.requests())
	})
	t.Run("create fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("POST /containers/c1/exec", 404, "No such container: c1")
		code, err := e.ContainerExec(ctx, "c1", []string{"ls"}, ExecSpec{}, nil, nil)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Contains(t, err.Error(), "exec create in c1")
		assert.Equal(t, -1, code)
	})
	t.Run("attach fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
		d.onError("POST /exec/e1/start", 409, "container is paused")
		code, err := e.ContainerExec(ctx, "c1", []string{"ls"}, ExecSpec{}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exec attach in c1")
		assert.Equal(t, -1, code)
		assert.Empty(t, d.find("GET", "/exec/e1/json"))
	})
	t.Run("corrupt stream", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeExec(d, d.serveStream(multiplexedStream, []byte{9, 0, 0, 0, 0, 0, 0, 1, 'x'}, false, nil), 0)
		code, err := e.ContainerExec(ctx, "c1", []string{"ls"}, ExecSpec{}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exec stream in c1")
		assert.Equal(t, -1, code)
	})
	t.Run("inspect fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
		d.on("POST /exec/e1/start", d.serveStream(multiplexedStream, nil, false, nil))
		d.onError("GET /exec/e1/json", 404, "No such exec instance")
		code, err := e.ContainerExec(ctx, "c1", []string{"ls"}, ExecSpec{}, nil, nil)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Contains(t, err.Error(), "exec inspect in c1")
		assert.Equal(t, -1, code)
	})
}

func TestContainerExecCancelledContextStopsAHungCommand(t *testing.T) {
	d, e := newFakeDaemon(t)
	attached := make(chan struct{})
	d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
	d.on("POST /exec/e1/start", func(w http.ResponseWriter, _ *http.Request) {
		h := hijack(t, w, multiplexedStream)
		defer h.Close()
		close(attached)
		_, _ = io.Copy(io.Discard, h.In) // the command never prints or exits
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		code int
		err  error
	}
	res := make(chan result, 1)
	go func() {
		code, err := e.ContainerExec(ctx, "c1", []string{"sleep", "999"}, ExecSpec{}, nil, nil)
		res <- result{code, err}
	}()
	signalled(t, attached, "the exec to attach")
	cancel()
	select {
	case r := <-res:
		require.ErrorIs(t, r.err, context.Canceled)
		assert.Equal(t, -1, r.code)
	case <-time.After(asyncTimeout):
		require.FailNow(t, "ContainerExec did not return after its context was cancelled")
	}
	assert.Empty(t, d.find("GET", "/exec/e1/json"), "no exit code is read for an aborted command")
}

// ---- ContainerExecTTY ----

func fakeTTYExec(d *fakeDaemon, start http.HandlerFunc) {
	d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
	d.on("POST /exec/e1/start", start)
}

func TestContainerExecTTYSession(t *testing.T) {
	d, e := newFakeDaemon(t)
	in := make(chan []byte, 1)
	fakeTTYExec(d, d.serveStream(rawStream, []byte("prompt$ "), true, in))
	d.onStatus("POST /exec/e1/resize", 200)
	var polls atomic.Int32
	d.on("GET /exec/e1/json", func(w http.ResponseWriter, _ *http.Request) {
		running := polls.Add(1) < 2 // the first poll still sees the shell running
		writeJSON(w, 200, obj{"ID": "e1", "Running": running, "ExitCode": 5})
	})

	s, err := e.ContainerExecTTY(context.Background(), "c1", TTYSpec{
		Argv: []string{"/bin/bash"}, Env: []string{"TERM=xterm"}, User: "root", Cols: 120, Rows: 40,
	})
	require.NoError(t, err)

	var create container.ExecCreateRequest
	d.only("POST", "/containers/c1/exec").decode(t, &create)
	assert.True(t, create.Tty)
	assert.True(t, create.AttachStdin)
	assert.Equal(t, "root", create.User)
	assert.Equal(t, []string{"TERM=xterm"}, create.Env)
	assert.Equal(t, []string{"/bin/bash"}, create.Cmd)
	require.NotNil(t, create.ConsoleSize)
	assert.Equal(t, [2]uint{40, 120}, *create.ConsoleSize, "console size is [height, width]")
	var start container.ExecStartRequest
	d.only("POST", "/exec/e1/start").decode(t, &start)
	assert.True(t, start.Tty)
	assert.Equal(t, [2]uint{40, 120}, *start.ConsoleSize)

	out := make([]byte, len("prompt$ "))
	_, err = io.ReadFull(s, out)
	require.NoError(t, err)
	assert.Equal(t, "prompt$ ", string(out))

	n, err := s.Write([]byte("ls\n"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	require.NoError(t, s.Resize(context.Background(), 100, 30))
	rq := d.only("POST", "/exec/e1/resize").Query
	assert.Equal(t, "30", rq.Get("h"))
	assert.Equal(t, "100", rq.Get("w"))

	code, err := s.Wait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 5, code)
	assert.EqualValues(t, 2, polls.Load())

	require.NoError(t, s.Close())
	assert.Equal(t, "ls\n", string(recv(t, in)), "what was typed reached the process; closing hangs up the terminal")
}

func TestContainerExecTTYWithoutSizeSendsNoConsoleSize(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeTTYExec(d, d.serveStream(rawStream, nil, false, nil))
	s, err := e.ContainerExecTTY(context.Background(), "c1", TTYSpec{Argv: []string{"sh"}, Cols: -5, Rows: -5})
	require.NoError(t, err)
	defer s.Close()
	var create container.ExecCreateRequest
	d.only("POST", "/containers/c1/exec").decode(t, &create)
	assert.Nil(t, create.ConsoleSize, "a zero (or negative) size lets the daemon pick its default")
}

func TestContainerExecTTYResizeClampsToOne(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeTTYExec(d, d.serveStream(rawStream, nil, false, nil))
	d.onStatus("POST /exec/e1/resize", 200)
	s, err := e.ContainerExecTTY(context.Background(), "c1", TTYSpec{Argv: []string{"sh"}})
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.Resize(context.Background(), 0, -3))
	q := d.only("POST", "/exec/e1/resize").Query
	assert.Equal(t, "1", q.Get("h"))
	assert.Equal(t, "1", q.Get("w"))
}

func TestContainerExecTTYResizeError(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeTTYExec(d, d.serveStream(rawStream, nil, false, nil))
	d.onError("POST /exec/e1/resize", 404, "No such exec instance")
	s, err := e.ContainerExecTTY(context.Background(), "c1", TTYSpec{Argv: []string{"sh"}})
	require.NoError(t, err)
	defer s.Close()
	err = s.Resize(context.Background(), 80, 24)
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "exec resize")
}

func TestContainerExecTTYErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("empty argv", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		s, err := e.ContainerExecTTY(ctx, "c1", TTYSpec{})
		require.EqualError(t, err, "exec: empty argv")
		assert.Nil(t, s)
		assert.Empty(t, d.requests())
	})
	t.Run("create fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("POST /containers/c1/exec", 404, "No such container: c1")
		_, err := e.ContainerExecTTY(ctx, "c1", TTYSpec{Argv: []string{"sh"}})
		require.ErrorIs(t, err, ErrNotFound)
		assert.Contains(t, err.Error(), "exec create in c1")
	})
	t.Run("attach fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onJSON("POST /containers/c1/exec", 201, obj{"Id": "e1"})
		d.onError("POST /exec/e1/start", 409, "container is not running")
		s, err := e.ContainerExecTTY(ctx, "c1", TTYSpec{Argv: []string{"sh"}})
		require.Error(t, err)
		assert.Nil(t, s)
		assert.Contains(t, err.Error(), "exec attach in c1")
	})
}

func TestContainerExecTTYWaitErrors(t *testing.T) {
	t.Run("inspect fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeTTYExec(d, d.serveStream(rawStream, nil, false, nil))
		d.onError("GET /exec/e1/json", 404, "No such exec instance")
		s, err := e.ContainerExecTTY(context.Background(), "c1", TTYSpec{Argv: []string{"sh"}})
		require.NoError(t, err)
		defer s.Close()
		code, err := s.Wait(context.Background())
		require.ErrorIs(t, err, ErrNotFound)
		assert.Equal(t, -1, code)
	})
	t.Run("context ends while the process still runs", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeTTYExec(d, d.serveStream(rawStream, nil, false, nil))
		ctx, cancel := context.WithCancel(context.Background())
		polled := make(chan struct{}, 1)
		d.on("GET /exec/e1/json", func(w http.ResponseWriter, _ *http.Request) {
			select {
			case polled <- struct{}{}:
			default:
			}
			writeJSON(w, 200, obj{"ID": "e1", "Running": true})
		})
		s, err := e.ContainerExecTTY(context.Background(), "c1", TTYSpec{Argv: []string{"sh"}})
		require.NoError(t, err)
		defer s.Close()
		done := make(chan error, 1)
		go func() { _, err := s.Wait(ctx); done <- err }()
		signalled(t, polled, "the first exec poll")
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(asyncTimeout):
			require.FailNow(t, "Wait did not return after its context was cancelled")
		}
	})
}

// ---- RunHostCommand ----

// fakeHostContainer registers the endpoints a helper container goes through.
// attach handles the upgrade; waitStatus is the exit code reported by the
// wait-for-removal call.
func fakeHostContainer(d *fakeDaemon, attach http.HandlerFunc, waitStatus int) {
	d.onJSON("POST /containers/create", 201, obj{"Id": "h1"})
	d.on("POST /containers/h1/wait", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, obj{"StatusCode": waitStatus})
	})
	d.on("POST /containers/h1/attach", attach)
	d.onStatus("POST /containers/h1/start", 204)
	d.onStatus("DELETE /containers/h1", 204)
}

func TestRunHostCommandCapturesOutputAndExitCode(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(multiplexedStream, muxed(t, stdoutFrame("hello "), stderrFrame("warn"), stdoutFrame("world")), false, nil), 7)

	res, err := e.RunHostCommand(context.Background(), HostCommandSpec{
		Name: "nebula-host-cmd", Image: "agent:1", Labels: map[string]string{"nebula.host-command": "true"},
		Argv: []string{"nsenter", "-t", "1", "--", "uname", "-a"}, Env: []string{"A=1"},
	})
	require.NoError(t, err)
	assert.Equal(t, HostCommandResult{Stdout: "hello world", Stderr: "warn", ExitCode: 7}, res)

	req := d.only("POST", "/containers/create")
	assert.Equal(t, "nebula-host-cmd", req.Query.Get("name"))
	var body createRequest
	req.decode(t, &body)
	assert.Equal(t, "agent:1", body.Image)
	assert.Equal(t, []string{"A=1"}, body.Env)
	assert.Equal(t, map[string]string{"nebula.host-command": "true"}, body.Labels)
	assert.Equal(t, []string{"nsenter"}, []string(body.Entrypoint), "argv[0] replaces the image entrypoint")
	assert.Equal(t, []string{"-t", "1", "--", "uname", "-a"}, []string(body.Cmd))
	assert.True(t, body.AttachStdout)
	assert.True(t, body.AttachStderr)
	assert.False(t, body.AttachStdin)
	assert.False(t, body.Tty)
	h := body.HostConfig
	assert.True(t, h.Privileged)
	assert.Equal(t, container.PidMode("host"), h.PidMode)
	assert.True(t, h.AutoRemove, "the daemon removes the helper even if the agent dies")
	assert.Equal(t, container.RestartPolicyDisabled, h.RestartPolicy.Name)

	assert.Equal(t, "removed", d.only("POST", "/containers/h1/wait").Query.Get("condition"))
	aq := d.only("POST", "/containers/h1/attach").Query
	assert.Equal(t, "1", aq.Get("stream"))
	assert.Equal(t, "1", aq.Get("stdout"))
	assert.Equal(t, "1", aq.Get("stderr"))
	assert.False(t, aq.Has("stdin"))
	assert.Empty(t, d.find("DELETE", "/containers/h1"), "a clean run leaves removal to the daemon")

	calls := d.calls()
	attachAt, startAt := -1, -1
	for i, c := range calls {
		switch c {
		case "POST /containers/h1/attach":
			attachAt = i
		case "POST /containers/h1/start":
			startAt = i
		}
	}
	assert.Less(t, attachAt, startAt, "attach must precede start so no output is lost: %v", calls)
}

func TestRunHostCommandSingleWordArgv(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(multiplexedStream, nil, false, nil), 0)
	res, err := e.RunHostCommand(context.Background(), HostCommandSpec{Name: "n", Image: "i", Argv: []string{"id"}})
	require.NoError(t, err)
	assert.Equal(t, HostCommandResult{}, res)
	var body createRequest
	d.only("POST", "/containers/create").decode(t, &body)
	assert.Equal(t, []string{"id"}, []string(body.Entrypoint))
	assert.Empty(t, body.Cmd)
}

func TestRunHostCommandTruncatesLargeOutput(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(multiplexedStream, muxed(t, stdoutFrame("abcdefgh"), stderrFrame("ok")), false, nil), 0)
	res, err := e.RunHostCommand(context.Background(), HostCommandSpec{Name: "n", Image: "i", Argv: []string{"x"}, MaxOutput: 4})
	require.NoError(t, err)
	assert.Equal(t, "abcd", res.Stdout)
	assert.Equal(t, "ok", res.Stderr)
	assert.True(t, res.Truncated)
}

func TestRunHostCommandStderrTruncationIsReported(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(multiplexedStream, muxed(t, stdoutFrame("ok"), stderrFrame("0123456789")), false, nil), 0)
	res, err := e.RunHostCommand(context.Background(), HostCommandSpec{Name: "n", Image: "i", Argv: []string{"x"}, MaxOutput: 5})
	require.NoError(t, err)
	assert.Equal(t, "01234", res.Stderr)
	assert.True(t, res.Truncated)
}

func TestRunHostCommandFailedWaitReportsUnknownExitCode(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(multiplexedStream, muxed(t, stdoutFrame("done")), false, nil), 0)
	d.onError("POST /containers/h1/wait", 500, "wait broke")
	res, err := e.RunHostCommand(context.Background(), HostCommandSpec{Name: "n", Image: "i", Argv: []string{"x"}})
	require.NoError(t, err)
	assert.Equal(t, "done", res.Stdout)
	assert.Equal(t, -1, res.ExitCode)
}

func TestRunHostCommandErrors(t *testing.T) {
	ctx := context.Background()
	spec := HostCommandSpec{Name: "hc", Image: "i", Argv: []string{"x"}}
	t.Run("empty argv", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		_, err := e.RunHostCommand(ctx, HostCommandSpec{Name: "hc"})
		require.EqualError(t, err, "host command: empty command")
		assert.Empty(t, d.requests())
	})
	t.Run("create fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("POST /containers/create", 404, "No such image: i")
		_, err := e.RunHostCommand(ctx, spec)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Contains(t, err.Error(), "create host command container hc")
		assert.Empty(t, d.find("DELETE", "/containers/h1"))
	})
	t.Run("attach fails removes the helper", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeHostContainer(d, nil, 0)
		d.onError("POST /containers/h1/attach", 500, "attach broke")
		_, err := e.RunHostCommand(ctx, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attach to host command container hc")
		assert.Equal(t, "1", d.only("DELETE", "/containers/h1").Query.Get("force"))
		assert.Empty(t, d.find("POST", "/containers/h1/start"))
	})
	t.Run("start fails removes the helper", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeHostContainer(d, d.serveStream(multiplexedStream, nil, true, nil), 0)
		d.onError("POST /containers/h1/start", 500, "cannot start")
		_, err := e.RunHostCommand(ctx, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "start host command container hc")
		assert.Equal(t, "1", d.only("DELETE", "/containers/h1").Query.Get("force"))
	})
	t.Run("corrupt output removes the helper", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeHostContainer(d, d.serveStream(multiplexedStream, []byte{9, 0, 0, 0, 0, 0, 0, 1, 'x'}, false, nil), 0)
		_, err := e.RunHostCommand(ctx, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read host command output")
		assert.Equal(t, "1", d.only("DELETE", "/containers/h1").Query.Get("force"))
	})
}

func TestRunHostCommandCancellationKillsTheHelper(t *testing.T) {
	d, e := newFakeDaemon(t)
	started := make(chan struct{})
	removed := make(chan struct{})
	fakeHostContainer(d, d.serveStream(multiplexedStream, nil, true, nil), 0)
	d.on("POST /containers/h1/wait", func(w http.ResponseWriter, _ *http.Request) {
		flushHeaders(w)
		<-d.done // the helper never exits by itself
	})
	d.on("POST /containers/h1/start", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
		close(started)
	})
	d.on("DELETE /containers/h1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
		select {
		case <-removed:
		default:
			close(removed)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := e.RunHostCommand(ctx, HostCommandSpec{Name: "hc", Image: "i", Argv: []string{"sleep", "999"}})
		errc <- err
	}()
	signalled(t, started, "the helper to start")
	cancel()
	select {
	case err := <-errc:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(asyncTimeout):
		require.FailNow(t, "RunHostCommand did not return after cancellation")
	}
	signalled(t, removed, "the helper to be force-removed")
	assert.Equal(t, "1", d.find("DELETE", "/containers/h1")[0].Query.Get("force"))
}

// ---- RunHostShell ----

func TestRunHostShellSession(t *testing.T) {
	d, e := newFakeDaemon(t)
	in := make(chan []byte, 1)
	fakeHostContainer(d, d.serveStream(rawStream, []byte("host# "), true, in), 3)
	d.onStatus("POST /containers/h1/resize", 200)

	id, s, err := e.RunHostShell(context.Background(), HostShellSpec{
		Name: "nebula-host-shell", Image: "agent:1", Labels: map[string]string{"k": "v"},
		Argv: []string{"nsenter", "-t", "1", "--", "/bin/sh"}, Env: []string{"TERM=xterm"}, Cols: 90, Rows: 25,
	})
	require.NoError(t, err)
	assert.Equal(t, "h1", id)

	var body createRequest
	req := d.only("POST", "/containers/create")
	assert.Equal(t, "nebula-host-shell", req.Query.Get("name"))
	req.decode(t, &body)
	assert.Equal(t, "agent:1", body.Image)
	assert.Equal(t, []string{"nsenter"}, []string(body.Entrypoint))
	assert.Equal(t, []string{"-t", "1", "--", "/bin/sh"}, []string(body.Cmd))
	assert.Equal(t, []string{"TERM=xterm"}, body.Env)
	assert.Equal(t, map[string]string{"k": "v"}, body.Labels)
	assert.True(t, body.Tty)
	assert.True(t, body.OpenStdin)
	assert.True(t, body.StdinOnce)
	assert.True(t, body.AttachStdin)
	assert.True(t, body.HostConfig.Privileged)
	assert.Equal(t, container.PidMode("host"), body.HostConfig.PidMode)
	assert.True(t, body.HostConfig.AutoRemove)

	aq := d.only("POST", "/containers/h1/attach").Query
	assert.Equal(t, "1", aq.Get("stdin"))
	rq := d.only("POST", "/containers/h1/resize").Query
	assert.Equal(t, "25", rq.Get("h"))
	assert.Equal(t, "90", rq.Get("w"))

	out := make([]byte, len("host# "))
	_, err = io.ReadFull(s, out)
	require.NoError(t, err)
	assert.Equal(t, "host# ", string(out))
	_, err = s.Write([]byte("id\n"))
	require.NoError(t, err)

	require.NoError(t, s.Resize(context.Background(), 0, 0))
	rqs := d.find("POST", "/containers/h1/resize")
	require.Len(t, rqs, 2)
	assert.Equal(t, "1", rqs[1].Query.Get("h"))
	assert.Equal(t, "1", rqs[1].Query.Get("w"))

	code, err := s.Wait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, code)

	require.NoError(t, s.Close())
	assert.Equal(t, "id\n", string(recv(t, in)))
}

func TestRunHostShellWithoutSizeDoesNotResize(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(rawStream, nil, true, nil), 0)
	_, s, err := e.RunHostShell(context.Background(), HostShellSpec{Name: "n", Image: "i", Argv: []string{"sh"}, Cols: 80})
	require.NoError(t, err)
	defer s.Close()
	assert.Empty(t, d.find("POST", "/containers/h1/resize"), "both dimensions are needed")
}

func TestRunHostShellIgnoresInitialResizeFailure(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(rawStream, nil, true, nil), 0)
	d.onError("POST /containers/h1/resize", 500, "resize broke")
	id, s, err := e.RunHostShell(context.Background(), HostShellSpec{Name: "n", Image: "i", Argv: []string{"sh"}, Cols: 80, Rows: 24})
	require.NoError(t, err)
	defer s.Close()
	assert.Equal(t, "h1", id)
	err = s.Resize(context.Background(), 80, 24)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resize host shell")
}

func TestRunHostShellErrors(t *testing.T) {
	ctx := context.Background()
	spec := HostShellSpec{Name: "hs", Image: "i", Argv: []string{"sh"}}
	t.Run("empty argv", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		id, s, err := e.RunHostShell(ctx, HostShellSpec{Name: "hs"})
		require.EqualError(t, err, "host shell: empty command")
		assert.Empty(t, id)
		assert.Nil(t, s)
		assert.Empty(t, d.requests())
	})
	t.Run("create fails", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		d.onError("POST /containers/create", 404, "No such image: i")
		_, s, err := e.RunHostShell(ctx, spec)
		require.ErrorIs(t, err, ErrNotFound)
		assert.Nil(t, s)
		assert.Contains(t, err.Error(), "create host shell container hs")
	})
	t.Run("attach fails removes the helper", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeHostContainer(d, nil, 0)
		d.onError("POST /containers/h1/attach", 500, "attach broke")
		id, s, err := e.RunHostShell(ctx, spec)
		require.Error(t, err)
		assert.Empty(t, id)
		assert.Nil(t, s)
		assert.Contains(t, err.Error(), "attach to host shell container hs")
		assert.Equal(t, "1", d.only("DELETE", "/containers/h1").Query.Get("force"))
	})
	t.Run("start fails removes the helper", func(t *testing.T) {
		d, e := newFakeDaemon(t)
		fakeHostContainer(d, d.serveStream(rawStream, nil, true, nil), 0)
		d.onError("POST /containers/h1/start", 500, "cannot start")
		id, s, err := e.RunHostShell(ctx, spec)
		require.Error(t, err)
		assert.Empty(t, id)
		assert.Nil(t, s)
		assert.Contains(t, err.Error(), "start host shell container hs")
		assert.Equal(t, "1", d.only("DELETE", "/containers/h1").Query.Get("force"))
	})
}

func TestHostShellWaitFailedDaemonWaitIsMinusOne(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(rawStream, nil, true, nil), 0)
	d.onError("POST /containers/h1/wait", 500, "wait broke")
	_, s, err := e.RunHostShell(context.Background(), HostShellSpec{Name: "n", Image: "i", Argv: []string{"sh"}})
	require.NoError(t, err)
	defer s.Close()
	code, err := s.Wait(context.Background())
	require.NoError(t, err)
	assert.Equal(t, -1, code)
}

func TestHostShellWaitHonoursContext(t *testing.T) {
	d, e := newFakeDaemon(t)
	fakeHostContainer(d, d.serveStream(rawStream, nil, true, nil), 0)
	d.on("POST /containers/h1/wait", func(w http.ResponseWriter, _ *http.Request) {
		flushHeaders(w)
		<-d.done // the shell is still running
	})
	_, s, err := e.RunHostShell(context.Background(), HostShellSpec{Name: "n", Image: "i", Argv: []string{"sh"}})
	require.NoError(t, err)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, err := s.Wait(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "wait for host shell exit")
	assert.Equal(t, -1, code)
}

// compile-time check that the stdcopy frame type used by the helpers is the daemon's.
var _ = stdcopy.Stdout
