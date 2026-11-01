//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	registryContainer = "nebulactrl-test-registry"
	gitContainer      = "nebulactrl-test-git"
	gitImage          = "nebulactrl-test-git:dev"
	appImageLocal     = "nebulactrl-test-app:dev"
)

// fixtureState is built once per test run, on first use.
type fixtureState struct {
	once sync.Once
	err  error

	// Registry is host:port of the throwaway registry; the daemon pulls from it
	// because the engine always pulls image sources.
	Registry string
	// AppImage is the reference services use: <registry>/nebulactrl-test-app:dev.
	AppImage string
	arch     string
	appDir   string // dir holding the compiled app binary + Dockerfile
	rootDir  string
}

var fixApp fixtureState

// appFixture builds the fixture app image and pushes it to a local registry.
func appFixture(t testing.TB) *fixtureState {
	t.Helper()
	requireDocker(t)
	fixApp.once.Do(func() { fixApp.err = fixApp.buildApp() })
	if fixApp.err != nil {
		t.Fatalf("fixture app: %v", fixApp.err)
	}
	return &fixApp
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("go.mod not found above %s", wd)
		}
	}
}

func (f *fixtureState) buildApp() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root, err := repoRoot()
	if err != nil {
		return err
	}
	f.rootDir = root

	archOut, err := sh(ctx, "", "docker", "info", "--format", "{{.Architecture}}")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(archOut) {
	case "aarch64", "arm64":
		f.arch = "arm64"
	case "x86_64", "amd64":
		f.arch = "amd64"
	default:
		f.arch = runtime.GOARCH
	}

	f.appDir, err = os.MkdirTemp("", "nebulactrl-fixture-app-")
	if err != nil {
		return err
	}
	if err := goBuildLinux(ctx, root, f.arch, filepath.Join(f.appDir, "app")); err != nil {
		return err
	}
	// The image's /app keeps this file's mode, and the hardened tests run it as
	// 65534. go build honors the caller's umask (077 gives 0700), so set it.
	if err := os.Chmod(filepath.Join(f.appDir, "app"), 0o755); err != nil {
		return err
	}
	df, err := os.ReadFile(filepath.Join(root, "tests/fixtures/app/Dockerfile"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(f.appDir, "Dockerfile"), df, 0o644); err != nil {
		return err
	}
	if _, err := sh(ctx, f.appDir, "docker", "build", "-t", appImageLocal, "."); err != nil {
		return err
	}

	// Throwaway registry on a free loopback port. Docker treats loopback
	// registries as insecure, so no TLS setup is needed.
	_, _ = sh(ctx, "", "docker", "rm", "-f", registryContainer)
	port, err := freePort()
	if err != nil {
		return err
	}
	if _, err := sh(ctx, "", "docker", "run", "-d", "--name", registryContainer,
		"-p", fmt.Sprintf("127.0.0.1:%d:5000", port), "registry:2"); err != nil {
		return err
	}
	f.Registry = fmt.Sprintf("127.0.0.1:%d", port)
	f.AppImage = f.Registry + "/nebulactrl-test-app:dev"
	if err := waitTCP(ctx, f.Registry); err != nil {
		return err
	}
	if _, err := sh(ctx, "", "docker", "tag", appImageLocal, f.AppImage); err != nil {
		return err
	}
	if _, err := sh(ctx, "", "docker", "push", f.AppImage); err != nil {
		return err
	}
	return nil
}

func goBuildLinux(ctx context.Context, root, arch, out string) error {
	cmd := newCmd(ctx, root, "go", "build", "-trimpath", "-o", out, "./tests/fixtures/app")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build fixture app: %w: %s", err, b)
	}
	return nil
}

// gitFixtureEnvExample is the .env.example of the git fixture repository.
const gitFixtureEnvExample = "# Postgres connection string\nDATABASE_URL=\nSECRET_KEY=changeme\nAPP_NAME=demo\n"

// gitFixture is a git:// server with a one-commit repository holding the fixture app.
type gitFixture struct {
	URL       string // git://127.0.0.1:<port>/app.git
	CommitSHA string
}

var (
	gitOnce sync.Once
	gitFix  gitFixture
	gitErr  error
)

func gitServer(t testing.TB) gitFixture {
	t.Helper()
	app := appFixture(t)
	gitOnce.Do(func() { gitFix, gitErr = buildGit(app) })
	if gitErr != nil {
		t.Fatalf("fixture git server: %v", gitErr)
	}
	return gitFix
}

func buildGit(app *fixtureState) (gitFixture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	work, err := os.MkdirTemp("", "nebulactrl-fixture-git-")
	if err != nil {
		return gitFixture{}, err
	}
	defer os.RemoveAll(work)

	// Working tree = Dockerfile + prebuilt binary (the build needs no network).
	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return gitFixture{}, err
	}
	for _, name := range []string{"app", "Dockerfile"} {
		b, err := os.ReadFile(filepath.Join(app.appDir, name))
		if err != nil {
			return gitFixture{}, err
		}
		mode := os.FileMode(0o644)
		if name == "app" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(tree, name), b, mode); err != nil {
			return gitFixture{}, err
		}
	}
	// The template env file of the repository (TestEnvExampleKeysArePrefilled…).
	if err := os.WriteFile(filepath.Join(tree, ".env.example"), []byte(gitFixtureEnvExample), 0o644); err != nil {
		return gitFixture{}, err
	}
	gitArgs := []string{"-c", "user.name=itest", "-c", "user.email=itest@example.com", "-c", "init.defaultBranch=main"}
	run := func(dir string, args ...string) (string, error) {
		return sh(ctx, dir, "git", append(append([]string{}, gitArgs...), args...)...)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture app"}} {
		if _, err := run(tree, args...); err != nil {
			return gitFixture{}, err
		}
	}
	sha, err := run(tree, "rev-parse", "HEAD")
	if err != nil {
		return gitFixture{}, err
	}
	ctxDir := filepath.Join(work, "ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return gitFixture{}, err
	}
	if _, err := run(work, "clone", "-q", "--bare", tree, filepath.Join(ctxDir, "app.git")); err != nil {
		return gitFixture{}, err
	}
	if _, err := run(filepath.Join(ctxDir, "app.git"), "update-server-info"); err != nil {
		return gitFixture{}, err
	}
	df, err := os.ReadFile(filepath.Join(app.rootDir, "tests/fixtures/git-server/Dockerfile"))
	if err != nil {
		return gitFixture{}, err
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), df, 0o644); err != nil {
		return gitFixture{}, err
	}
	if _, err := sh(ctx, ctxDir, "docker", "build", "-t", gitImage, "."); err != nil {
		return gitFixture{}, err
	}

	_, _ = sh(ctx, "", "docker", "rm", "-f", gitContainer)
	port, err := freePort()
	if err != nil {
		return gitFixture{}, err
	}
	if _, err := sh(ctx, "", "docker", "run", "-d", "--name", gitContainer,
		"-p", fmt.Sprintf("127.0.0.1:%d:9418", port), gitImage); err != nil {
		return gitFixture{}, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitTCP(ctx, addr); err != nil {
		return gitFixture{}, err
	}
	return gitFixture{URL: "git://" + addr + "/app.git", CommitSHA: sha}, nil
}

func teardownFixtures(ctx context.Context) {
	_, _ = sh(ctx, "", "docker", "rm", "-f", registryContainer, gitContainer)
	if fixApp.appDir != "" {
		_ = os.RemoveAll(fixApp.appDir)
	}
}
