//go:build linux || darwin

package hostpty

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartReportsAProgramThatCannotBeRun(t *testing.T) {
	_, err := Start(Spec{Argv: []string{"/nonexistent/nebulactrl-shell"}, Env: Env("xterm", "/bin/sh"), Cols: 80, Rows: 24})
	require.Error(t, err)
}

func TestWaitReturnsWhenTheCallerGivesUp(t *testing.T) {
	s, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "echo up; while :; do sleep 1; done"}, Env: Env("xterm", "/bin/sh"), Cols: 80, Rows: 24})
	require.NoError(t, err)
	defer s.Close()
	readUntil(t, s, "up")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, err := s.Wait(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, -1, code)
}

func TestExitCodeOfASignalledProcessIs128PlusTheSignal(t *testing.T) {
	s, err := Start(Spec{Argv: []string{"/bin/sh", "-c", "kill -TERM $$"}, Env: Env("xterm", "/bin/sh"), Cols: 80, Rows: 24})
	require.NoError(t, err)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := s.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, 143, code)
}

func TestClosedSessionRejectsInputAndResize(t *testing.T) {
	s, err := Start(Spec{Argv: []string{"/bin/sh"}, Env: Env("xterm", "/bin/sh"), Cols: 80, Rows: 24})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = s.Write([]byte("echo hi\n"))
	require.Error(t, err)
	require.Error(t, s.Resize(context.Background(), 100, 40))
	_, err = s.Read(make([]byte, 16))
	require.Error(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = s.Wait(ctx)
	require.NoError(t, err, "the hung-up shell is reaped")
}

func TestResizeClampsNonPositiveSizes(t *testing.T) {
	s, err := Start(Spec{Argv: []string{"/bin/sh"}, Env: Env("xterm", "/bin/sh"), Cols: 0, Rows: 0})
	require.NoError(t, err, "a zero initial size is raised to 1x1 instead of failing")
	defer s.Close()
	require.NoError(t, s.Resize(context.Background(), -5, 0))
	_, err = s.Write([]byte("stty size\n"))
	require.NoError(t, err)
	readUntil(t, s, "1 1")
}

func TestEnvPassesLocaleAndRefusesInjectedValues(t *testing.T) {
	t.Setenv("LANG", "de_DE.UTF-8")
	t.Setenv("LC_ALL", "C\nINJECTED=1")
	t.Setenv("HOME", "/home/someone")
	t.Setenv("PATH", "")
	env := Env("screen", "/bin/zsh")
	joined := strings.Join(env, "\n")
	require.Contains(t, env, "LANG=de_DE.UTF-8")
	require.NotContains(t, joined, "INJECTED", "a locale value with a newline is dropped")
	require.Contains(t, env, "HOME=/home/someone")
	require.Contains(t, env, "TERM=screen")
	require.Contains(t, env, "SHELL=/bin/zsh")
	require.Contains(t, env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "an empty PATH gets the standard one")
}

func TestHomeDirPrefersTheEnvironmentAndAlwaysAnswers(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	require.Equal(t, "/home/someone", HomeDir())
	t.Setenv("HOME", "")
	require.NotEmpty(t, HomeDir(), "without HOME the account's home (or /) is used")
	require.True(t, strings.HasPrefix(HomeDir(), "/"))
}
