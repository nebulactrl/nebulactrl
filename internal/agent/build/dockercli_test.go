package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeDockerCLI puts a shell script named docker first on PATH. The script
// appends its arguments (and, if set, the value of PROBE_ENV) to calls.log and
// then runs body, which may use the files in the returned directory as
// switches.
func fakeDockerCLI(t *testing.T, body string) (dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for the docker CLI")
	}
	dir = t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$*\" \"$PROBE_ENV\" >> \"" + filepath.Join(dir, "calls.log") + "\"\n" +
		"D=\"" + dir + "\"\n" + body + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func readCalls(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func touch(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o644))
}

func TestImageRepoIsTheTagWithoutDeploymentSuffix(t *testing.T) {
	require.Equal(t, "nc-build/web", ImageRepo("web"))
	tag := ImageTag("web", "0123456789abcdef")
	require.True(t, strings.HasPrefix(tag, ImageRepo("web")+":"), tag)
}

func TestRunDockerStreamsBothOutputsAndPassesExtraEnv(t *testing.T) {
	dir := fakeDockerCLI(t, `echo "out $1"; echo "err $PROBE_ENV" >&2`)
	var out bytes.Buffer
	err := runDocker(context.Background(), []string{"info"}, []string{"PROBE_ENV=secret-value"}, &out)
	require.NoError(t, err)
	require.Equal(t, "out info\nerr secret-value\n", out.String(), "stdout and stderr go to the same writer")
	require.Equal(t, []string{"info|secret-value"}, readCalls(t, dir))
}

func TestRunDockerWithoutExtraEnvInheritsTheParent(t *testing.T) {
	fakeDockerCLI(t, `echo "$PROBE_ENV"`)
	t.Setenv("PROBE_ENV", "from-parent")
	var out bytes.Buffer
	require.NoError(t, runDocker(context.Background(), nil, nil, &out))
	require.Equal(t, "from-parent\n", out.String())
}

func TestRunDockerReportsExitStatusAndMissingBinary(t *testing.T) {
	fakeDockerCLI(t, `echo "denied" >&2; exit 3`)
	var out bytes.Buffer
	err := runDocker(context.Background(), []string{"push"}, nil, &out)
	require.ErrorContains(t, err, "exit status 3")
	require.Equal(t, "denied\n", out.String())

	t.Setenv("PATH", t.TempDir())
	require.Error(t, runDocker(context.Background(), []string{"info"}, nil, &bytes.Buffer{}), "no docker binary on PATH")
}

// signalWriter reports when the child has said it is ready.
type signalWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (s *signalWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	got := strings.Contains(s.buf.String(), "ready")
	s.mu.Unlock()
	if got {
		s.once.Do(func() { close(s.ready) })
	}
	return len(p), nil
}

func (s *signalWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestRunDockerCancelInterruptsInsteadOfKilling(t *testing.T) {
	// A canceled build must get SIGINT so the CLI can tell BuildKit to stop;
	// the stand-in traps it and says so.
	fakeDockerCLI(t, `trap 'echo interrupted; kill $PID; exit 130' INT
sleep 60 &
PID=$!
echo ready
wait $PID`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &signalWriter{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- runDocker(ctx, []string{"buildx", "build"}, nil, out) }()

	select {
	case <-out.ready:
	case <-time.After(30 * time.Second):
		t.Fatal("the stand-in docker never started")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, out.String(), "interrupted", "the child received SIGINT")
	case <-time.After(30 * time.Second):
		t.Fatal("runDocker did not return after cancel")
	}
}

const pruneScript = `
case "$1 $2" in
"builder prune")
  echo "Deleted Build Cache:"
  echo "Total:	1.5GB"
  [ -f "$D/builder_fail" ] && { echo "builder prune failed" >&2; exit 1; }
  exit 0 ;;
"buildx inspect")
  [ -f "$D/no_builder" ] && exit 1
  exit 0 ;;
"buildx prune")
  echo "Total:	500MB"
  [ -f "$D/buildx_fail" ] && { echo "buildx prune failed" >&2; exit 2; }
  exit 0 ;;
esac
exit 0`

func TestPruneDaemonCacheOnlyWhenNoManagedBuilder(t *testing.T) {
	dir := fakeDockerCLI(t, pruneScript)
	touch(t, dir, "no_builder")
	var lines []string
	reclaimed, err := Prune(context.Background(), 72*time.Hour, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.Equal(t, int64(1_500_000_000), reclaimed)
	require.Equal(t, []string{
		"builder prune -f --filter until=72h|",
		"buildx inspect " + agentprotocol.BuildxBuilderName + "|",
	}, readCalls(t, dir))
	require.Equal(t, []string{"Deleted Build Cache:", "Total:\t1.5GB"}, lines, "progress lines are passed to logf")
}

func TestPruneSumsDaemonAndManagedBuilderReclaimedSpace(t *testing.T) {
	dir := fakeDockerCLI(t, pruneScript)
	reclaimed, err := Prune(context.Background(), 24*time.Hour, func(string) {})
	require.NoError(t, err)
	require.Equal(t, int64(2_000_000_000), reclaimed)
	require.Equal(t, []string{
		"builder prune -f --filter until=24h|",
		"buildx inspect " + agentprotocol.BuildxBuilderName + "|",
		"buildx prune --builder " + agentprotocol.BuildxBuilderName + " -f --filter until=24h|",
	}, readCalls(t, dir))
}

func TestPruneReportsFailuresButStillPrunesTheOtherBuilder(t *testing.T) {
	// the daemon prune fails: its error wins, the managed builder is still pruned
	dir := fakeDockerCLI(t, pruneScript)
	touch(t, dir, "builder_fail")
	touch(t, dir, "buildx_fail")
	reclaimed, err := Prune(context.Background(), time.Hour, func(string) {})
	require.ErrorContains(t, err, "exit status 1", "the first failure is returned")
	require.Equal(t, int64(2_000_000_000), reclaimed, "what was reported before the failure still counts")
	require.Len(t, readCalls(t, dir), 3)

	// only the managed builder's prune fails
	dir = fakeDockerCLI(t, pruneScript)
	touch(t, dir, "buildx_fail")
	_, err = Prune(context.Background(), time.Hour, func(string) {})
	require.ErrorContains(t, err, "exit status 2")
}

func TestPruneSubHourAgesRoundDownToZeroHours(t *testing.T) {
	dir := fakeDockerCLI(t, pruneScript)
	touch(t, dir, "no_builder")
	_, err := Prune(context.Background(), 30*time.Minute, func(string) {})
	require.NoError(t, err)
	require.Equal(t, "builder prune -f --filter until=0h|", readCalls(t, dir)[0])
}

func TestBuildxVersion(t *testing.T) {
	fakeDockerCLI(t, `[ "$1 $2" = "buildx version" ] && { echo "github.com/docker/buildx v0.17.1 abc123"; exit 0; }
exit 1`)
	v, err := BuildxVersion(context.Background())
	require.NoError(t, err)
	require.Equal(t, "github.com/docker/buildx v0.17.1 abc123", v, "output is trimmed")

	fakeDockerCLI(t, `echo "docker: 'buildx' is not a docker command" >&2; exit 1`)
	_, err = BuildxVersion(context.Background())
	require.Error(t, err, "a missing plugin is an error, so the agent does not advertise push builds")

	t.Setenv("PATH", t.TempDir())
	_, err = BuildxVersion(context.Background())
	require.Error(t, err, "no docker binary at all")
}

func TestBuilderFreeBytesUsesInjectionOrTheFilesystem(t *testing.T) {
	b := &Builder{FreeBytes: func(path string) (uint64, error) { return uint64(len(path)), nil }}
	n, err := b.freeBytes("abcd")
	require.NoError(t, err)
	require.Equal(t, uint64(4), n)

	real := &Builder{}
	n, err = real.freeBytes(t.TempDir())
	require.NoError(t, err)
	require.Positive(t, n, "the real filesystem reports free space")

	_, err = real.freeBytes(filepath.Join(t.TempDir(), "does", "not", "exist"))
	require.Error(t, err)
}
