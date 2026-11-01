package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fakeDocker stands in for the docker CLI: it records every invocation and
// plays the parts the Railpack path depends on (plan files, image id, daemon
// image store, buildx builder).
type fakeDocker struct {
	t *testing.T

	mu    sync.Mutex
	calls [][]string
	envs  [][]string

	fixture        string // testdata prefix: node or static
	containerd     bool
	builderExists  bool
	nativeBuildErr string // output + failure of the first native `docker build`
	envFile        string // content of the env script copied into the planner
	envFileMode    os.FileMode
	planFails      bool
}

func (f *fakeDocker) run(_ context.Context, args, env []string, out io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.envs = append(f.envs, env)
	switch {
	case args[0] == "info":
		if f.containerd {
			_, _ = out.Write([]byte(`[["driver-type","io.containerd.snapshotter.v1"]]`))
		} else {
			_, _ = out.Write([]byte(`[["Backing Filesystem","btrfs"]]`))
		}
	case args[0] == "start":
		if f.planFails {
			_, _ = out.Write([]byte("error: no provider matched\n"))
			return errors.New("exit status 1")
		}
		_, _ = out.Write([]byte("Railpack 0.43.0\n"))
	case args[0] == "cp" && strings.Contains(args[1], ":/tmp/railpack-info.json"):
		return f.copyFixture("info", args[2])
	case args[0] == "cp" && strings.Contains(args[1], ":/tmp/railpack-plan.json"):
		return f.copyFixture("plan", args[2])
	case args[0] == "cp" && strings.HasSuffix(args[2], ":/tmp/nc-env.sh"):
		raw, err := os.ReadFile(args[1])
		require.NoError(f.t, err)
		st, _ := os.Stat(args[1])
		f.envFile, f.envFileMode = string(raw), st.Mode().Perm()
	case args[0] == "buildx" && args[1] == "inspect":
		if !f.builderExists {
			return errors.New("no builder")
		}
	case args[0] == "buildx" && args[1] == "create":
		f.builderExists = true
	case args[0] == "build" && f.nativeBuildErr != "":
		msg := f.nativeBuildErr
		f.nativeBuildErr = ""
		_, _ = out.Write([]byte(msg))
		return errors.New("exit status 1")
	case args[0] == "build" || (args[0] == "buildx" && args[1] == "build"):
		for i, a := range args {
			if a == "--iidfile" {
				return os.WriteFile(args[i+1], []byte("sha256:beef\n"), 0o644)
			}
		}
	}
	return nil
}

func (f *fakeDocker) copyFixture(kind, dst string) error {
	raw, err := os.ReadFile(filepath.Join("testdata", f.fixture+"-"+kind+".json"))
	require.NoError(f.t, err)
	return os.WriteFile(dst, raw, 0o644)
}

// verbs returns the docker subcommands in order ("create", "cp", "build", …).
func (f *fakeDocker) verbs() []string {
	var v []string
	for _, c := range f.calls {
		if c[0] == "buildx" {
			v = append(v, "buildx "+c[1])
		} else {
			v = append(v, c[0])
		}
	}
	return v
}

func (f *fakeDocker) find(verb string) []string {
	for _, c := range f.calls {
		if c[0] == verb || (c[0] == "buildx" && "buildx "+c[1] == verb) {
			return c
		}
	}
	return nil
}

func railpackBuilder(t *testing.T, f *fakeDocker, files map[string]string) *Builder {
	return &Builder{
		StateDir: t.TempDir(),
		Clone: func(_ context.Context, _ Request, dir string) error {
			for name, content := range files {
				p := filepath.Join(dir, filepath.FromSlash(name))
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
			}
			return os.MkdirAll(dir, 0o755)
		},
		Run: f.run,
	}
}

func nodeRepo() map[string]string {
	return map[string]string{"package.json": `{"scripts":{"start":"node server.js"}}`, "server.js": ""}
}

func TestRailpackBuildOnDaemonWithContainerdStore(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node", containerd: true}
	var logs []string
	req := goodRequest()
	req.BuildType = "" // auto, and the repository has no Dockerfile
	var info agentprotocol.BuildInfo
	req.Info = &info
	id, err := railpackBuilder(t, f, nodeRepo()).Build(context.Background(), req, func(l string) { logs = append(logs, l) })
	require.NoError(t, err)
	require.Equal(t, "sha256:beef", id)

	require.Equal(t, []string{"rm", "create", "cp", "cp", "start", "cp", "cp", "rm", "info", "build"}, f.verbs())

	create := f.find("create")
	require.Equal(t, []string{"create", "--name", "nc-railpack-d2d2d2d2", "--entrypoint", "/bin/sh", agentprotocol.RailpackFrontendImage,
		"-c", prepareScript, "sh", "prepare", "/app", "--plan-out", "/tmp/railpack-plan.json", "--info-out", "/tmp/railpack-info.json",
		"--env", "RAILPACK_NODE_VERSION"}, create)
	require.NotContains(t, create, "--privileged")
	require.Contains(t, f.find("cp")[2], "nc-railpack-d2d2d2d2:/app")

	build := f.find("build")
	require.Equal(t, "build", build[0])
	require.Contains(t, build, "--build-arg")
	require.Contains(t, build, "BUILDKIT_SYNTAX="+agentprotocol.RailpackFrontendImage)
	for i, a := range build {
		if a == "-f" {
			require.Equal(t, "railpack-plan.json", filepath.Base(build[i+1]))
		}
	}
	require.Contains(t, strings.Join(build, " "), "-t nc-build/web:d2d2d2d2")
	require.Contains(t, strings.Join(build, " "), "--label nebulactrl.managed=true --label nebulactrl.service=svc-1")
	require.NotContains(t, build, "buildx")
	require.NotContains(t, f.verbs(), "buildx inspect", "the daemon's own BuildKit is used")

	require.Equal(t, "railpack", info.Method)
	require.Equal(t, "docker", info.Builder)
	require.Equal(t, "0.43.0", info.RailpackVersion)
	require.Equal(t, []string{"node"}, info.Providers)
	require.Equal(t, "npm run start", info.StartCommand)
	require.Equal(t, []agentprotocol.BuildPackage{{Name: "node", Version: "26.13.0", Source: "RAILPACK_NODE_VERSION"}}, info.Packages)
	require.Contains(t, strings.Join(logs, "\n"), "RAILPACK_NODE_VERSION=26.13.0")
	require.Contains(t, strings.Join(logs, "\n"), "No Dockerfile")
	require.Contains(t, strings.Join(logs, "\n"), "Railpack 0.43.0 (node)")
}

func TestRailpackBuildUsesManagedBuildxWithoutContainerdStore(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node"}
	var info agentprotocol.BuildInfo
	req := goodRequest()
	req.Info = &info
	_, err := railpackBuilder(t, f, nodeRepo()).Build(context.Background(), req, func(string) {})
	require.NoError(t, err)

	require.Equal(t, []string{"rm", "create", "cp", "cp", "start", "cp", "cp", "rm", "info", "buildx inspect", "buildx create", "buildx build"}, f.verbs())
	create := f.find("buildx create")
	require.Equal(t, []string{"buildx", "create", "--name", "nebulactrl-buildkit", "--driver", "docker-container",
		"--driver-opt", "image=" + agentprotocol.BuildKitImage, "--bootstrap"}, create)
	bb := f.find("buildx build")
	require.Equal(t, []string{"buildx", "build", "--builder", "nebulactrl-buildkit", "--load", "--progress=plain"}, bb[:6])
	require.Equal(t, "buildx", info.Builder)
}

func TestRailpackBuildFallsBackToBuildxWhenDaemonRefusesMergeop(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node", containerd: true,
		nativeBuildErr: "ERROR: failed to solve: requested experimental feature mergeop  has been disabled on the build server\n"}
	var info agentprotocol.BuildInfo
	req := goodRequest()
	req.Info = &info
	_, err := railpackBuilder(t, f, nodeRepo()).Build(context.Background(), req, func(string) {})
	require.NoError(t, err)
	require.Equal(t, []string{"rm", "create", "cp", "cp", "start", "cp", "cp", "rm", "info", "build", "buildx inspect", "buildx create", "buildx build"}, f.verbs())
	require.Equal(t, "buildx", info.Builder)
}

func TestRailpackBuildOtherFailuresDoNotRetry(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node", containerd: true, nativeBuildErr: "npm ERR! missing script\n"}
	_, err := railpackBuilder(t, f, nodeRepo()).Build(context.Background(), goodRequest(), func(string) {})
	require.ErrorContains(t, err, "docker build: exit status 1")
	require.NotContains(t, f.verbs(), "buildx create")
}

// Secret values (and plain build values: Railpack passes every variable as a
// BuildKit secret) must never be in an argument vector; they travel in an 0600
// script copied into the planner container and in the build process's env.
func TestRailpackBuildKeepsSecretsOutOfArgv(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node", containerd: true}
	req := goodRequest()
	req.Vars = Vars{
		Args:    map[string]string{"RAILPACK_START_CMD": "node server.js --canary-plain-9", "NODE_ENV": "production"},
		Secrets: map[string]string{"API_KEY": "sekret-canary-12345", "MULTI": "line1\nit's 'quoted'"},
	}
	var logs []string
	_, err := railpackBuilder(t, f, nodeRepo()).Build(context.Background(), req, func(l string) { logs = append(logs, l) })
	require.NoError(t, err)

	for _, c := range f.calls {
		joined := strings.Join(c, "\x00")
		require.NotContains(t, joined, "sekret-canary-12345")
		require.NotContains(t, joined, "canary-plain-9")
		require.NotContains(t, joined, "it's")
	}
	require.NotContains(t, strings.Join(logs, "\n"), "sekret-canary-12345")
	require.NotContains(t, strings.Join(logs, "\n"), "canary-plain-9")

	create := strings.Join(f.find("create"), " ")
	for _, k := range []string{"API_KEY", "MULTI", "NODE_ENV", "RAILPACK_NODE_VERSION", "RAILPACK_START_CMD"} {
		require.Contains(t, create, "--env "+k, k)
	}
	require.Equal(t, os.FileMode(0o600), f.envFileMode)
	require.Contains(t, f.envFile, "API_KEY='sekret-canary-12345'\n")
	require.Contains(t, f.envFile, "MULTI='line1\nit'\\''s '\\''quoted'\\'''\n")

	build := f.find("build")
	joined := strings.Join(build, " ")
	for _, k := range []string{"API_KEY", "MULTI", "NODE_ENV", "RAILPACK_START_CMD"} {
		require.Contains(t, joined, "--secret id="+k+",env=NCBS_"+k)
	}
	require.Contains(t, joined, "--build-arg secrets-hash=")
	require.NotContains(t, joined, "--build-arg NODE_ENV", "railpack consumes plain variables as secrets, not build args")

	// The values reach only the docker build process's environment.
	var buildEnv []string
	for i, c := range f.calls {
		if c[0] == "build" {
			buildEnv = f.envs[i]
		}
	}
	require.Contains(t, buildEnv, "NCBS_API_KEY=sekret-canary-12345")
	require.Contains(t, buildEnv, "NCBS_NODE_ENV=production")
}

func TestRailpackPlanFailureNamesTheReason(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "node", containerd: true, planFails: true}
	_, err := railpackBuilder(t, f, map[string]string{"README.md": "x"}).Build(context.Background(), goodRequest(), func(string) {})
	require.ErrorContains(t, err, "railpack could not plan this source")
	require.Contains(t, f.verbs(), "rm", "the planner container is removed")
	require.NotContains(t, f.verbs(), "build")
}

func TestRailpackRequestedOverridesDockerfile(t *testing.T) {
	f := &fakeDocker{t: t, fixture: "static", containerd: true}
	req := goodRequest()
	req.BuildType = "railpack"
	var info agentprotocol.BuildInfo
	req.Info = &info
	repo := map[string]string{"Dockerfile": "FROM scratch\n", "index.html": "<h1>x</h1>"}
	_, err := railpackBuilder(t, f, repo).Build(context.Background(), req, func(string) {})
	require.NoError(t, err)
	require.Equal(t, []string{"staticfile"}, info.Providers)
	require.Contains(t, info.Reason, "ignored")
}

func TestStarterBuildUsesGeneratedDockerfileOutsideTheRepository(t *testing.T) {
	f := &fakeDocker{t: t}
	var dockerfile, ignore string
	b := railpackBuilder(t, f, map[string]string{"index.html": "hi", ".dockerignore": "secret.txt\n"})
	inner := b.Run
	b.Run = func(ctx context.Context, args []string, env []string, out io.Writer) error {
		for i, a := range args {
			if a == "-f" {
				raw, err := os.ReadFile(args[i+1])
				require.NoError(t, err)
				dockerfile = string(raw)
				raw, err = os.ReadFile(args[i+1] + ".dockerignore")
				require.NoError(t, err)
				ignore = string(raw)
				require.NotContains(t, args[i+1], string(filepath.Separator)+"src"+string(filepath.Separator), "generated file stays out of the checkout")
			}
		}
		return inner(ctx, args, env, out)
	}
	req := goodRequest()
	req.BuildType = "starter:static"
	var info agentprotocol.BuildInfo
	req.Info = &info
	var logs []string
	_, err := b.Build(context.Background(), req, func(l string) { logs = append(logs, l) })
	require.NoError(t, err)
	require.Equal(t, []string{"build"}, f.verbs()[:1])
	require.Contains(t, dockerfile, "FROM nginx:1.29-alpine")
	require.Contains(t, ignore, ".git\n")
	require.Contains(t, ignore, "secret.txt", "the repository's .dockerignore is kept")
	require.Equal(t, "starter", info.Method)
	require.Equal(t, "static", info.Starter)
	require.Contains(t, strings.Join(logs, "\n"), "FROM nginx:1.29-alpine", "generated Dockerfile is printed to the build log")
}

func TestDockerfileBuildPassesBuildVarsWithoutSecretsInArgv(t *testing.T) {
	f := &fakeDocker{t: t}
	req := goodRequest()
	req.Vars = Vars{Args: map[string]string{"NODE_ENV": "production"}, Secrets: map[string]string{"TOKEN": "sekret-canary-12345"}}
	var logs []string
	var info agentprotocol.BuildInfo
	req.Info = &info
	b := railpackBuilder(t, f, map[string]string{"Dockerfile": "FROM scratch\n"})
	_, err := b.Build(context.Background(), req, func(l string) { logs = append(logs, l) })
	require.NoError(t, err)
	require.Equal(t, []string{"build"}, f.verbs())
	joined := strings.Join(f.calls[0], " ")
	require.Contains(t, joined, "--build-arg NODE_ENV=production")
	require.Contains(t, joined, "--secret id=TOKEN,env=NCBS_TOKEN")
	require.NotContains(t, joined, "sekret-canary-12345")
	require.Contains(t, f.envs[0], "NCBS_TOKEN=sekret-canary-12345")
	require.NotContains(t, strings.Join(logs, "\n"), "sekret-canary-12345")
	require.Contains(t, strings.Join(logs, "\n"), "NODE_ENV=•••", "plain build arg values are masked in the log")
	require.Equal(t, "dockerfile", info.Method)
}

func TestPlanReportsMethodsWithoutBuilding(t *testing.T) {
	ctx := context.Background()
	req := goodRequest()

	f := &fakeDocker{t: t, fixture: "node", containerd: true}
	res, err := railpackBuilder(t, f, nodeRepo()).Plan(ctx, req, func(string) {})
	require.NoError(t, err)
	require.Equal(t, "succeeded", res.Outcome)
	require.False(t, res.DockerfileFound)
	require.Equal(t, "node", res.SuggestedStarter)
	require.Equal(t, "railpack", res.Info.Method)
	require.Equal(t, []string{"node"}, res.Info.Providers)
	require.Equal(t, "npm run start", res.Info.StartCommand)
	require.NotEmpty(t, res.Info.Steps)
	require.NotEmpty(t, res.Messages)
	require.NotContains(t, f.verbs(), "build")
	require.Equal(t, "auto", res.Requested)

	f = &fakeDocker{t: t}
	res, err = railpackBuilder(t, f, map[string]string{"Dockerfile": "FROM scratch\n"}).Plan(ctx, req, func(string) {})
	require.NoError(t, err)
	require.True(t, res.DockerfileFound)
	require.Equal(t, "dockerfile", res.Info.Method)
	require.Empty(t, f.calls, "no docker call for a Dockerfile plan")

	req.BuildType = "dockerfile"
	res, err = railpackBuilder(t, &fakeDocker{t: t}, nodeRepo()).Plan(ctx, req, func(string) {})
	require.NoError(t, err)
	require.Equal(t, "failed", res.Outcome)
	require.Contains(t, res.Message, "not found")
	require.Nil(t, res.Info)

	req.BuildType = "starter:static"
	res, err = railpackBuilder(t, &fakeDocker{t: t}, nodeRepo()).Plan(ctx, req, func(string) {})
	require.NoError(t, err)
	require.Equal(t, "failed", res.Outcome)
	require.Contains(t, res.Message, "index.html")
	require.Equal(t, "starter", res.Info.Method)

	req.BuildType = "starter:node"
	res, err = railpackBuilder(t, &fakeDocker{t: t}, nodeRepo()).Plan(ctx, req, func(string) {})
	require.NoError(t, err)
	require.Equal(t, "succeeded", res.Outcome)
}

func TestRailpackBuildArgsHaveNoShellAndPinnedFrontend(t *testing.T) {
	req := goodRequest()
	args := railpackBuildArgs(req, "/w/railpack-plan.json", "/w/iid", "/w/src", "docker")
	require.Equal(t, "build", args[0])
	require.Equal(t, "/w/src", args[len(args)-1])
	require.Contains(t, agentprotocol.RailpackFrontendImage, "@sha256:")
	require.Contains(t, agentprotocol.BuildKitImage, "@sha256:")
	require.Contains(t, agentprotocol.RailpackFrontendImage, ":v"+agentprotocol.RailpackVersion)
	for _, a := range args {
		require.NotContains(t, a, "sh -c")
	}
	require.NotContains(t, args, "--secret", "no variables, no secrets")
	require.NotContains(t, strings.Join(args, " "), "secrets-hash")
}

func TestSecretsHashChangesWithValues(t *testing.T) {
	a := secretsHash(map[string]string{"K": "1"})
	require.Equal(t, a, secretsHash(map[string]string{"K": "1"}))
	require.NotEqual(t, a, secretsHash(map[string]string{"K": "2"}))
	require.NotEqual(t, a, secretsHash(map[string]string{"L": "1"}))
	require.Len(t, a, 32)

	// The value is visible in the process list, so it must not be the plain
	// SHA-256 of the secrets, which a weak secret would not survive.
	plain := sha256.Sum256([]byte("K\x001\x00"))
	require.NotEqual(t, hex.EncodeToString(plain[:16]), a)
}

func TestWriteEnvScriptSkipsInvalidNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env.sh")
	keys, skipped, err := writeEnvScript(path, map[string]string{"OK_1": "v", "bad-name": "x", "1BAD": "y"})
	require.NoError(t, err)
	require.Equal(t, []string{"OK_1"}, keys)
	require.ElementsMatch(t, []string{"bad-name", "1BAD"}, skipped)
	keys, _, err = writeEnvScript(filepath.Join(t.TempDir(), "none.sh"), nil)
	require.NoError(t, err)
	require.Empty(t, keys)
}
