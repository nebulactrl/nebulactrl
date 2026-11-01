//go:build linux || darwin

package hostpty

import (
	"bytes"
	"context"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// readUntil reads s until the output contains want.
func readUntil(t *testing.T, s *Session, want string) string {
	t.Helper()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for !strings.Contains(out.String(), want) {
			n, err := s.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = s.Close()
		<-done
	}
	require.Contains(t, out.String(), want)
	return out.String()
}

func TestSessionRunsAShellOnAPty(t *testing.T) {
	s, err := Start(Spec{
		Argv: []string{"/bin/sh"}, Env: Env("xterm-256color", "/bin/sh"), Dir: HomeDir(), Cols: 100, Rows: 30,
	})
	require.NoError(t, err)
	defer s.Close()

	_, err = s.Write([]byte("stty size; tty; echo term=$TERM\n"))
	require.NoError(t, err)
	out := readUntil(t, s, "term=xterm-256color")
	require.Contains(t, out, "30 100", "the initial size reaches the pty")
	require.Contains(t, out, "/dev/", "the shell has a controlling terminal")

	require.NoError(t, s.Resize(context.Background(), 140, 50))
	_, err = s.Write([]byte("stty size\n"))
	require.NoError(t, err)
	readUntil(t, s, "50 140")

	// Somebody must read the terminal, like the agent's output pump does:
	// a shell cannot finish exiting while its output is not drained.
	drained := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := s.Read(buf); err != nil {
				drained <- err
				return
			}
		}
	}()
	_, err = s.Write([]byte("exit 5\n"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := s.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, 5, code)
	select {
	case err := <-drained:
		require.ErrorIs(t, err, io.EOF, "the output ends with EOF once the shell is gone")
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal never reported EOF")
	}
}

func TestEnvDoesNotInheritTheAgentEnvironment(t *testing.T) {
	t.Setenv("NEBULACTRL_ENROLL_TOKEN", "nct_enroll_secret")
	t.Setenv("NEBULACTRL_URL", "wss://cp.example")
	env := strings.Join(Env("xterm-256color", "/bin/bash"), "\n")
	require.NotContains(t, env, "nct_enroll_secret")
	require.NotContains(t, env, "NEBULACTRL_URL")
	require.Contains(t, env, "TERM=xterm-256color")
	require.Contains(t, env, "SHELL=/bin/bash")

	s, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "echo token=[$NEBULACTRL_ENROLL_TOKEN]"}, Env: Env("xterm", "/bin/sh"), Cols: 80, Rows: 24})
	require.NoError(t, err)
	defer s.Close()
	readUntil(t, s, "token=[]")
}

func TestCloseHangsUpAndKillsAShellThatIgnoresIt(t *testing.T) {
	// A shell that ignores SIGHUP survives the hang-up but not the kill.
	s, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "trap '' HUP; echo ready; while :; do sleep 1; done"}, Env: Env("xterm", "/bin/sh"), Cols: 80, Rows: 24})
	require.NoError(t, err)
	readUntil(t, s, "ready")
	pid := s.cmd.Process.Pid
	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "Close is idempotent")

	ctx, cancel := context.WithTimeout(context.Background(), killGrace+5*time.Second)
	defer cancel()
	code, err := s.Wait(ctx)
	require.NoError(t, err)
	require.NotZero(t, code, "the process was ended by a signal")
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 20*time.Millisecond, "the process is gone")
}

func TestStartRejectsAnEmptyCommand(t *testing.T) {
	_, err := Start(Spec{})
	require.Error(t, err)
}
