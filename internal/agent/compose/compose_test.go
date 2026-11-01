package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseVersionAndMinimum(t *testing.T) {
	for _, c := range []struct {
		in   string
		ok   bool
		want bool
	}{
		{"2.24.0", true, true},
		{"v2.24.0", true, true},
		{"2.24", true, true},
		{"2.23.9", true, false},
		{"2.29.7\n", true, true},
		{"2.27.0-desktop.1", true, true},
		{"5.6.1", true, true},
		{"1.29.2", true, false},
		{"", false, false},
		{"dev", false, false},
		{"2.x.1", false, false},
	} {
		_, _, _, err := ParseVersion(c.in)
		require.Equal(t, c.ok, err == nil, c.in)
		require.Equal(t, c.want, AtLeastMin(c.in), c.in)
	}
}

func TestCleanEnvDropsDockerAndComposeKeys(t *testing.T) {
	agent := []string{
		"PATH=/usr/bin", "HOME=/home/agent", "DOCKER_CONFIG=/cfg", "DOCKER_HOST=unix:///run/docker.sock",
		"DOCKER_CONTEXT=ctx", "DOCKER_CERT_PATH=/certs", "DOCKER_TLS_VERIFY=1",
		"NEBULACTRL_TOKEN=enroll-token", "AWS_SECRET_ACCESS_KEY=agent-secret",
	}
	user := []string{
		"DB_PASSWORD=s3cr3t=with=equals", "DOCKER_HOST=tcp://evil:2375", "DOCKER_CONFIG=/tmp/evil",
		"COMPOSE_PROJECT_NAME=other", "COMPOSE_FILE=/etc/passwd", "COMPOSE_PROFILES=web,worker",
		"PATH=/tmp/evil", "HOME=/tmp/evil", "noequals", "=empty",
	}
	got := CleanEnv(agent, user)
	m := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	require.Equal(t, map[string]string{
		"DB_PASSWORD":       "s3cr3t=with=equals",
		"COMPOSE_PROFILES":  "web,worker",
		"PATH":              "/usr/bin",
		"HOME":              "/home/agent",
		"DOCKER_CONFIG":     "/cfg",
		"DOCKER_HOST":       "unix:///run/docker.sock",
		"DOCKER_CONTEXT":    "ctx",
		"DOCKER_CERT_PATH":  "/certs",
		"DOCKER_TLS_VERIFY": "1",
	}, m)
	// The agent's own values come last, so they win over a user key.
	last := map[string]int{}
	for i, kv := range got {
		k, _, _ := strings.Cut(kv, "=")
		last[k] = i
	}
	require.Greater(t, last["PATH"], last["DB_PASSWORD"])
}

func TestCleanEnvDropsLoaderAndInterpreterInjection(t *testing.T) {
	user := []string{
		"LD_PRELOAD=/tmp/evil.so", "LD_LIBRARY_PATH=/tmp", "DYLD_INSERT_LIBRARIES=/tmp/evil.dylib", "GCONV_PATH=/tmp/gconv",
		"BASH_ENV=/tmp/evil.sh", "NODE_OPTIONS=--require /tmp/evil.js", "PYTHONPATH=/tmp", "BUILDKIT_HOST=tcp://evil:1234",
		"NODE_ENV=production", "SSH_PORT=2222", "HTTP_PROXY=http://proxy:3128", "TZ=UTC",
	}
	got := CleanEnv([]string{"PATH=/usr/bin"}, user)
	require.ElementsMatch(t, []string{"NODE_ENV=production", "SSH_PORT=2222", "HTTP_PROXY=http://proxy:3128", "TZ=UTC", "PATH=/usr/bin"}, got)
}

// fakeDocker writes a shell script that prints what it was run with.
func fakeDocker(t *testing.T, body string) *Exec {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755))
	return &Exec{Bin: bin, Environ: func() []string {
		return []string{"PATH=/usr/bin:/bin", "HOME=/home/agent", "NEBULACTRL_TOKEN=tok"}
	}}
}

const echoScript = `echo "PWD=$(pwd)"
for a in "$@"; do echo "ARG:$a"; done
env | sort | sed 's/^/ENV:/'
`

func collect(into *[]string) Output { return func(l string) { *into = append(*into, l) } }

func TestExecUpBuildsArgumentVectorAndCleanEnvironment(t *testing.T) {
	x := fakeDocker(t, echoScript)
	dir := t.TempDir()
	proj := Project{
		Name: "nc-12345678", Dir: dir, Files: []string{dir + "/next/compose.yaml", dir + "/next/override.yaml"},
		Env: []string{"API_KEY=value one", "DOCKER_HOST=tcp://evil", "COMPOSE_PROFILES=dev"},
	}
	var lines []string
	require.NoError(t, x.Up(t.Context(), proj, UpOptions{Wait: true, WaitTimeout: 90 * time.Second, RemoveOrphans: true}, collect(&lines)))
	out := strings.Join(lines, "\n")

	var args []string
	for _, l := range lines {
		if a, ok := strings.CutPrefix(l, "ARG:"); ok {
			args = append(args, a)
		}
	}
	require.Equal(t, []string{
		"compose", "--ansi", "never", "--progress", "plain", "-p", "nc-12345678",
		"-f", dir + "/next/compose.yaml", "-f", dir + "/next/override.yaml", "--project-directory", dir,
		"up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "90",
	}, args)
	wd, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Contains(t, out, "PWD="+wd)
	require.Contains(t, out, "ENV:API_KEY=value one")
	require.Contains(t, out, "ENV:COMPOSE_PROFILES=dev")
	require.Contains(t, out, "ENV:HOME=/home/agent")
	require.NotContains(t, out, "evil")
	require.NotContains(t, out, "NEBULACTRL_TOKEN")
}

func TestExecByNameCommandsPassNoFiles(t *testing.T) {
	x := fakeDocker(t, `for a in "$@"; do echo "ARG:$a"; done`)
	proj := Project{Name: "nc-12345678", Dir: t.TempDir(), Files: []string{"ignored.yaml"}}
	args := func(fn func(Output) error) string {
		var lines []string
		require.NoError(t, fn(collect(&lines)))
		return strings.Join(lines, " ")
	}
	require.Equal(t, "ARG:compose ARG:--ansi ARG:never ARG:--progress ARG:plain ARG:-p ARG:nc-12345678 ARG:restart ARG:-t ARG:30",
		args(func(o Output) error { return x.Restart(t.Context(), proj, 30*time.Second, o) }))
	require.Equal(t, "ARG:compose ARG:--ansi ARG:never ARG:--progress ARG:plain ARG:-p ARG:nc-12345678 ARG:stop ARG:-t ARG:10",
		args(func(o Output) error { return x.Stop(t.Context(), proj, 10*time.Second, o) }))
	require.Equal(t, "ARG:compose ARG:--ansi ARG:never ARG:--progress ARG:plain ARG:-p ARG:nc-12345678 ARG:down ARG:--remove-orphans ARG:--volumes ARG:-t ARG:5",
		args(func(o Output) error {
			return x.Down(t.Context(), proj, DownOptions{RemoveVolumes: true, Grace: 5 * time.Second}, o)
		}))
	require.Equal(t, "ARG:compose ARG:--ansi ARG:never ARG:--progress ARG:plain ARG:-p ARG:nc-12345678 ARG:down ARG:--remove-orphans",
		args(func(o Output) error { return x.Down(t.Context(), proj, DownOptions{}, o) }))
}

func TestExecConfigReturnsStdoutAndEnablesEveryProfile(t *testing.T) {
	x := fakeDocker(t, `echo "warning: variable X is not set" >&2
for a in "$@"; do printf '%s ' "$a"; done >&2; echo >&2
echo '{"services":{}}'`)
	var lines []string
	raw, err := x.Config(t.Context(), Project{Name: "nc-1", Dir: t.TempDir(), Files: []string{"c.yaml"}}, collect(&lines))
	require.NoError(t, err)
	require.JSONEq(t, `{"services":{}}`, string(raw))
	require.Equal(t, "warning: variable X is not set", lines[0])
	require.Contains(t, lines[1], "--profile * -p nc-1 -f c.yaml")
	require.Contains(t, lines[1], "config --format json")
}

func TestExecVersion(t *testing.T) {
	x := fakeDocker(t, `echo v2.29.7`)
	v, err := x.Version(t.Context())
	require.NoError(t, err)
	require.Equal(t, "v2.29.7", v)

	_, err = fakeDocker(t, `echo "docker: 'compose' is not a docker command." >&2; exit 1`).Version(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a docker command")

	_, err = (&Exec{Bin: filepath.Join(t.TempDir(), "missing")}).Version(t.Context())
	require.Error(t, err, "a host without docker must not panic")
}

func TestExecFailureCarriesTheLastOutputLines(t *testing.T) {
	x := fakeDocker(t, `echo "pulling a"; echo "pull access denied for x" >&2; exit 3`)
	err := x.Pull(t.Context(), Project{Name: "nc-1", Dir: t.TempDir(), Files: []string{"c.yaml"}}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "docker compose pull")
	require.Contains(t, err.Error(), "pull access denied for x")
}

func TestExecCancelInterruptsTheCommand(t *testing.T) {
	x := fakeDocker(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := x.Up(ctx, Project{Name: "nc-1", Dir: t.TempDir()}, UpOptions{Wait: true}, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestLineWriterKeepsTheLastProgressFrame(t *testing.T) {
	var lines []string
	w := &lineWriter{fn: func(l string) { lines = append(lines, l) }}
	_, _ = w.Write([]byte("Container a Creating\rContainer a Created\nsecond"))
	_, _ = w.Write([]byte(" line\n\n"))
	w.flush()
	require.Equal(t, []string{"Container a Created", "second line"}, lines)
}
