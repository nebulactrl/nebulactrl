package build

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func goodRequest() Request {
	return Request{
		DeploymentID: "d2d2d2d2-0000-4000-8000-000000000002", ServiceID: "svc-1", ServiceSlug: "web",
		GitURL: "https://example.com/r.git", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		DockerfilePath: "Dockerfile", BuildContext: ".",
	}
}

func TestValidateRelPathRejectsTraversalAndAbsolute(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "\\x", "../a", "a/../../b", "a/..", "..", "C:\\x", "a\x00b", "./../x"} {
		require.Error(t, ValidateRelPath("f", bad), "%q", bad)
	}
	for _, ok := range []string{"Dockerfile", "docker/Dockerfile.prod", ".", "./app", "a/b..c/d"} {
		require.NoError(t, ValidateRelPath("f", ok), "%q", ok)
	}
}

func TestRequestValidate(t *testing.T) {
	require.NoError(t, goodRequest().Validate())

	r := goodRequest()
	r.DockerfilePath = "../Dockerfile"
	require.ErrorContains(t, r.Validate(), "dockerfilePath")

	r = goodRequest()
	r.BuildContext = "/abs"
	require.ErrorContains(t, r.Validate(), "buildContext")

	r = goodRequest()
	r.CommitSHA = "main"
	require.ErrorContains(t, r.Validate(), "commit sha")

	r = goodRequest()
	r.DockerfilePath, r.BuildContext = "", ""
	require.NoError(t, r.Validate(), "empty paths take the defaults")

	for _, id := range []string{"../x", "a/b", "..", ".hidden", "a\x00b"} {
		r = goodRequest()
		r.DeploymentID = id
		require.ErrorContains(t, r.Validate(), "deployment id", "%q", id)
	}

	for _, key := range []string{"a,src=/etc/shadow", "A=B", "", "A\nB"} {
		r = goodRequest()
		r.Vars = Vars{Secrets: map[string]string{key: "v"}}
		require.ErrorContains(t, r.Validate(), "build variable name", "%q", key)
		r.Vars = Vars{Args: map[string]string{key: "v"}}
		require.ErrorContains(t, r.Validate(), "build variable name", "%q", key)
	}
}

func TestBuildRunsExactDockerArgumentVectorAndStreamsOutput(t *testing.T) {
	state := t.TempDir()
	var gotArgs []string
	var lines []string
	b := &Builder{
		StateDir: state,
		Clone: func(_ context.Context, req Request, dir string) error {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
			return os.WriteFile(filepath.Join(dir, "sub", "Dockerfile"), []byte("FROM scratch\n"), 0o644)
		},
		Run: func(_ context.Context, args []string, _ []string, out io.Writer) error {
			gotArgs = args
			_, _ = out.Write([]byte("#1 step one\r\n#2 step two\npartial"))
			for i, a := range args {
				if a == "--iidfile" {
					return os.WriteFile(args[i+1], []byte("sha256:cafe\n"), 0o644)
				}
			}
			return errors.New("no iidfile")
		},
	}
	req := goodRequest()
	req.DockerfilePath, req.BuildContext = "sub/Dockerfile", "sub"
	id, err := b.Build(context.Background(), req, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.Equal(t, "sha256:cafe", id)

	require.Equal(t, "build", gotArgs[0])
	require.Equal(t, "--progress=plain", gotArgs[1])
	require.Equal(t, "-f", gotArgs[2])
	require.True(t, strings.HasSuffix(gotArgs[3], filepath.Join("src", "sub", "Dockerfile")), gotArgs[3])
	require.Equal(t, []string{"-t", "nc-build/web:d2d2d2d2"}, gotArgs[4:6])
	require.Equal(t, []string{"--label", "nebulactrl.managed=true", "--label", "nebulactrl.service=svc-1"}, gotArgs[6:10])
	require.Equal(t, "--iidfile", gotArgs[10])
	require.True(t, strings.HasSuffix(gotArgs[len(gotArgs)-1], filepath.Join("src", "sub")))
	require.Len(t, gotArgs, 13)

	require.Contains(t, lines, "#1 step one")
	require.Contains(t, lines, "#2 step two")
	require.Contains(t, lines, "partial")

	_, statErr := os.Stat(filepath.Join(state, "builds", req.DeploymentID))
	require.True(t, os.IsNotExist(statErr), "build dir removed")
}

func TestBuildRefusesSymlinkEscapingTheRepository(t *testing.T) {
	state := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch"), 0o644))
	ran := false
	b := &Builder{
		StateDir: state,
		Clone: func(_ context.Context, _ Request, dir string) error {
			require.NoError(t, os.MkdirAll(dir, 0o755))
			return os.Symlink(outside, filepath.Join(dir, "evil"))
		},
		Run: func(context.Context, []string, []string, io.Writer) error { ran = true; return nil },
	}
	req := goodRequest()
	req.DockerfilePath = "evil/Dockerfile"
	_, err := b.Build(context.Background(), req, func(string) {})
	require.ErrorContains(t, err, "escapes the repository")
	require.False(t, ran)
}

func TestBuildRejectsInvalidRequestBeforeCloning(t *testing.T) {
	cloned := false
	b := &Builder{StateDir: t.TempDir(), Clone: func(context.Context, Request, string) error { cloned = true; return nil }}
	req := goodRequest()
	req.DockerfilePath = "../../etc/passwd"
	_, err := b.Build(context.Background(), req, func(string) {})
	require.ErrorContains(t, err, "dockerfilePath")
	require.False(t, cloned)
}

func TestBuildFailureReportsDockerError(t *testing.T) {
	b := &Builder{
		StateDir: t.TempDir(),
		Clone: func(_ context.Context, _ Request, dir string) error {
			require.NoError(t, os.MkdirAll(dir, 0o755))
			return os.WriteFile(filepath.Join(dir, "Dockerfile"), nil, 0o644)
		},
		Run: func(context.Context, []string, []string, io.Writer) error { return errors.New("exit status 1") },
	}
	_, err := b.Build(context.Background(), goodRequest(), func(string) {})
	require.ErrorContains(t, err, "docker build: exit status 1")
}

func TestCanceledBuildStopsTheCommandAndRemovesTheWorkDir(t *testing.T) {
	state := t.TempDir()
	ctx, cancel := context.WithCancelCause(context.Background())
	running := make(chan struct{})
	b := &Builder{
		StateDir: state,
		Clone: func(_ context.Context, _ Request, dir string) error {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
		},
		Run: func(ctx context.Context, _ []string, _ []string, out io.Writer) error {
			_, _ = out.Write([]byte("#1 sleeping\n"))
			close(running)
			<-ctx.Done() // a slow Dockerfile: the build only ends when it is canceled
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	req := goodRequest()
	go func() {
		_, err := b.Build(ctx, req, func(string) {})
		done <- err
	}()
	select {
	case <-running:
	case err := <-done:
		t.Fatalf("the build ended before it ran the command: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the build never ran the command")
	}
	cancel(errors.New("canceled by a user"))
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the build did not stop")
	}
	_, statErr := os.Stat(filepath.Join(state, "builds", req.DeploymentID))
	require.True(t, os.IsNotExist(statErr), "the work dir is removed after a cancel")
}
