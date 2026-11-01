package build

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// pushDocker plays the docker CLI for a pushed build: builder lifecycle through
// the shared fakeDocker, `buildx build` here (metadata file, credential
// snapshot while the build runs, optional blocking or failing hook).
type pushDocker struct {
	*fakeDocker

	pmu       sync.Mutex
	buildArgs [][]string
	buildEnvs [][]string
	cfgJSON   string // config.json of DOCKER_CONFIG while the build ran
	cfgDir    string
	onBuild   func(ctx context.Context, args []string, out io.Writer) error
	digest    string
}

func newPushDocker(t *testing.T) *pushDocker {
	return &pushDocker{fakeDocker: &fakeDocker{t: t, fixture: "node"}, digest: testDigest}
}

func envValue(env []string, key string) string {
	v := ""
	for _, e := range env {
		if k, val, ok := strings.Cut(e, "="); ok && k == key {
			v = val
		}
	}
	return v
}

func (p *pushDocker) run(ctx context.Context, args, env []string, out io.Writer) error {
	if len(args) >= 2 && args[0] == "buildx" && args[1] == "build" {
		p.pmu.Lock()
		p.buildArgs = append(p.buildArgs, append([]string(nil), args...))
		p.buildEnvs = append(p.buildEnvs, append([]string(nil), env...))
		p.cfgDir = envValue(env, "DOCKER_CONFIG")
		if raw, err := os.ReadFile(filepath.Join(p.cfgDir, "config.json")); err == nil {
			p.cfgJSON = string(raw)
		}
		p.pmu.Unlock()
		if p.onBuild != nil {
			if err := p.onBuild(ctx, args, out); err != nil {
				return err
			}
		}
		for i, a := range args {
			if a == "--metadata-file" {
				return os.WriteFile(args[i+1], []byte(fmt.Sprintf(`{"containerimage.digest":%q,"image.name":"x"}`, p.digest)), 0o644)
			}
		}
		return errors.New("no --metadata-file")
	}
	return p.fakeDocker.run(ctx, args, env, out)
}

func withDockerfile(t *testing.T) func(context.Context, Request, string) error {
	return func(_ context.Context, _ Request, dir string) error {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		return os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	}
}

// registry starts a plain-http registry that answers /v2/ and one manifest.
func registry(t *testing.T) (host string, srv *httptest.Server) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write([]byte(`{"config":{"size":100},"layers":[{"size":1000},{"size":2000}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), srv
}

func goodPush(host string) PushRequest {
	return PushRequest{
		Request:  goodRequest(),
		Platform: "linux/arm64", Repository: host + "/acme/web", Tag: "r-1a2b3c4d",
		Builder:  BuilderPrefix + "0a1b2c3d",
		Insecure: true,
		Auth:     &agentprotocol.RegistryAuth{Username: "robot", Password: "s3cret-pass-word"},
	}
}

func pushBuilder(t *testing.T, d *pushDocker) *Builder {
	return &Builder{StateDir: t.TempDir(), Clone: withDockerfile(t), Run: d.run, MinFreeBytes: -1}
}

func TestPushArgsUsePerProjectBuilderPlatformPushAndNoProvenance(t *testing.T) {
	req := goodPush("reg.example.com")
	req.Insecure = false
	req.Vars = Vars{Args: map[string]string{"NODE_ENV": "production"}, Secrets: map[string]string{"NPM_TOKEN": "tok-123456"}}
	args := pushArgs(req, "/w/src/Dockerfile", "/w/metadata.json", "/w/src", false)

	require.Equal(t, []string{"buildx", "build", "--builder", "nebulactrl-buildkit-0a1b2c3d",
		"--platform", "linux/arm64", "--push", "--provenance=false", "--sbom=false", "--progress=plain",
		"--metadata-file", "/w/metadata.json", "-f", "/w/src/Dockerfile",
		"-t", "reg.example.com/acme/web:r-1a2b3c4d",
		"--label", "nebulactrl.managed=true", "--label", "nebulactrl.service=svc-1",
		"--build-arg", "NODE_ENV=production", "--secret", "id=NPM_TOKEN,env=NCBS_NPM_TOKEN", "/w/src"}, args)
	for _, a := range args {
		require.NotContains(t, a, "tok-123456", "secret values never reach argv")
		require.NotEqual(t, "--load", a)
		require.NotEqual(t, "--iidfile", a)
		require.NotEqual(t, "--cache-from", a, "no registry cache unless asked")
	}

	req.CacheRef = "reg.example.com/acme/web:buildcache"
	args = pushArgs(req, "/f", "/m", "/c", false)
	require.Contains(t, args, "type=registry,ref=reg.example.com/acme/web:buildcache")
	require.Contains(t, strings.Join(args, " "), "--cache-to type=registry,ref=reg.example.com/acme/web:buildcache,mode=max")
}

func TestBuildAndPushHappyPath(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	b := pushBuilder(t, d)
	req := goodPush(host)
	var pushed int
	req.OnPush = func() { pushed++ }
	var info agentprotocol.BuildInfo
	req.Info = &info
	var lines []string

	res, err := b.BuildAndPush(context.Background(), req, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	require.Equal(t, testDigest, res.Digest)
	require.Equal(t, host+"/acme/web@"+testDigest, res.Ref)
	require.EqualValues(t, 3100, res.SizeBytes, "config plus layers from the registry's manifest")
	require.Equal(t, 1, pushed, "OnPush fires once, even when the output never names the push phase")
	require.Equal(t, "dockerfile", info.Method)

	// Builder lifecycle: inspect, create (with the buildkitd config), then the build.
	require.Equal(t, []string{"buildx inspect", "buildx create"}, d.verbs())
	create := d.find("buildx create")
	require.Equal(t, []string{"buildx", "create", "--name", "nebulactrl-buildkit-0a1b2c3d", "--driver", "docker-container",
		"--driver-opt", "image=" + agentprotocol.BuildKitImage}, create[:8])

	require.Len(t, d.buildArgs, 1)
	ba := d.buildArgs[0]
	require.Equal(t, []string{"--builder", "nebulactrl-buildkit-0a1b2c3d", "--platform", "linux/arm64", "--push"}, ba[2:7])
	require.Contains(t, ba, "--provenance=false")
	require.Contains(t, ba, "--sbom=false")
	require.Contains(t, ba, host+"/acme/web:r-1a2b3c4d")

	// The temp DOCKER_CONFIG held only the push credential while building...
	require.Contains(t, d.cfgJSON, base64.StdEncoding.EncodeToString([]byte("robot:s3cret-pass-word")))
	var cfg struct {
		Auths map[string]map[string]string `json:"auths"`
	}
	require.NoError(t, json.Unmarshal([]byte(d.cfgJSON), &cfg))
	require.Len(t, cfg.Auths, 1)
	require.Contains(t, cfg.Auths, host)
	require.Equal(t, filepath.Join(b.StateDir, "buildx"), envValue(d.buildEnvs[0], "BUILDX_CONFIG"))
	// ...and is gone afterwards, like the workspace.
	require.NoDirExists(t, d.cfgDir)
	require.NoDirExists(t, filepath.Join(b.StateDir, "builds", req.DeploymentID))
	entries, _ := os.ReadDir(filepath.Join(b.StateDir, "dockercfg"))
	require.Empty(t, entries)
}

func TestBuildAndPushPhaseCallbackFiresWhenTheOutputNamesThePush(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	var order []string
	d.onBuild = func(_ context.Context, _ []string, out io.Writer) error {
		_, _ = out.Write([]byte("#5 [build 1/1] RUN make\n#9 exporting to image\n#9 pushing layers 1.2s done\n"))
		return nil
	}
	req := goodPush(host)
	req.OnPush = func() { order = append(order, "push") }
	_, err := pushBuilder(t, d).BuildAndPush(context.Background(), req, func(l string) {
		if strings.Contains(l, "RUN make") {
			order = append(order, "build-line")
		}
		if strings.Contains(l, "exporting to image") {
			order = append(order, "export-line")
		}
	})
	require.NoError(t, err)
	require.Equal(t, []string{"build-line", "push", "export-line"}, order, "OnPush runs before the line that starts the push is logged, exactly once")
}

func TestBuildAndPushFailureRemovesCredentialsAndWorkdir(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	d.onBuild = func(_ context.Context, _ []string, out io.Writer) error {
		_, _ = out.Write([]byte("#7 RUN npm ci\n#7 ERROR: process \"npm ci\" did not complete successfully: exit code: 1\nERROR: failed to solve: process \"/bin/sh -c npm ci\" did not complete\n"))
		return errors.New("exit status 1")
	}
	b := pushBuilder(t, d)
	req := goodPush(host)
	_, err := b.BuildAndPush(context.Background(), req, func(string) {})
	require.ErrorContains(t, err, "docker buildx build: exit status 1")
	require.ErrorContains(t, err, "failed to solve")
	require.NoDirExists(t, d.cfgDir)
	require.NoDirExists(t, filepath.Join(b.StateDir, "builds", req.DeploymentID))
}

func TestBuildAndPushCancelMidBuildStopsAndCleansUp(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	started := make(chan struct{})
	d.onBuild = func(ctx context.Context, _ []string, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return context.Cause(ctx)
	}
	b := pushBuilder(t, d)
	ctx, cancel := context.WithCancelCause(context.Background())
	errCh := make(chan error, 1)
	req := goodPush(host)
	go func() {
		_, err := b.BuildAndPush(ctx, req, func(string) {})
		errCh <- err
	}()
	<-started
	require.DirExists(t, d.cfgDir, "the credentials exist while the build runs")
	cancel(errors.New("canceled by test"))
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "canceled by test")
	case <-time.After(5 * time.Second):
		t.Fatal("BuildAndPush did not return after the cancel")
	}
	require.NoDirExists(t, d.cfgDir, "the credential directory is removed even on cancel")
	require.NoDirExists(t, filepath.Join(b.StateDir, "builds", req.DeploymentID))
}

func TestBuildAndPushRegistryPreflightFailureStopsBeforeCloning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Www-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	d := newPushDocker(t)
	cloned := false
	b := &Builder{StateDir: t.TempDir(), MinFreeBytes: -1, Run: d.run,
		Clone: func(context.Context, Request, string) error { cloned = true; return nil }}
	req := goodPush(strings.TrimPrefix(srv.URL, "http://"))
	_, err := b.BuildAndPush(context.Background(), req, func(string) {})
	require.ErrorContains(t, err, "registry check failed")
	require.ErrorContains(t, err, "rejected the credential")
	require.False(t, cloned, "nothing is cloned when the registry is unusable")
	require.Empty(t, d.calls, "no docker command ran")
	require.NoDirExists(t, filepath.Join(b.StateDir, "dockercfg"), "no credential was written either")
}

func TestBuildAndPushRefusesWhenDiskIsFull(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	cloned := false
	b := &Builder{StateDir: t.TempDir(), MinFreeBytes: 5 << 30, Run: d.run,
		FreeBytes: func(string) (uint64, error) { return 1 << 30, nil },
		Clone:     func(context.Context, Request, string) error { cloned = true; return nil }}
	var lines []string
	_, err := b.BuildAndPush(context.Background(), goodPush(host), func(l string) { lines = append(lines, l) })
	require.ErrorContains(t, err, "not enough free disk space")
	require.False(t, cloned)
	require.Contains(t, d.verbs(), "buildx prune", "the builder's cache is pruned once before giving up")
	require.Contains(t, strings.Join(lines, "\n"), "pruning the build cache")
	entries, _ := os.ReadDir(filepath.Join(b.StateDir, "dockercfg"))
	require.Empty(t, entries, "credentials removed on this path too")

	// Enough space after the prune: the build goes ahead.
	calls := 0
	b.FreeBytes = func(string) (uint64, error) {
		calls++
		if calls == 1 {
			return 1 << 30, nil
		}
		return 50 << 30, nil
	}
	b.Clone = withDockerfile(t)
	_, err = b.BuildAndPush(context.Background(), goodPush(host), func(string) {})
	require.NoError(t, err)
}

func TestInsecureRegistryOnlyWhenTheRequestSaysSo(t *testing.T) {
	// Insecure + loopback: http/insecure registry entry and the host network.
	host, _ := registry(t)
	d := newPushDocker(t)
	b := pushBuilder(t, d)
	_, err := b.BuildAndPush(context.Background(), goodPush(host), func(string) {})
	require.NoError(t, err)
	toml, err := os.ReadFile(filepath.Join(b.StateDir, "buildx", BuilderPrefix+"0a1b2c3d.toml"))
	require.NoError(t, err)
	require.Contains(t, string(toml), fmt.Sprintf("[registry.%q]", host))
	require.Contains(t, string(toml), "http = true")
	require.Contains(t, string(toml), "insecure = true")
	create := d.find("buildx create")
	require.Contains(t, strings.Join(create, " "), "--driver-opt network=host")

	// Not insecure (a TLS registry): no registry entry, no host network.
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer tls.Close()
	d2 := newPushDocker(t)
	b2 := pushBuilder(t, d2)
	b2.HTTP = tls.Client()
	req := goodPush(strings.TrimPrefix(tls.URL, "https://"))
	req.Insecure = false
	_, err = b2.BuildAndPush(context.Background(), req, func(string) {})
	require.NoError(t, err)
	toml, err = os.ReadFile(filepath.Join(b2.StateDir, "buildx", BuilderPrefix+"0a1b2c3d.toml"))
	require.NoError(t, err)
	require.NotContains(t, string(toml), "[registry.")
	require.NotContains(t, string(toml), "insecure")
	require.NotContains(t, strings.Join(d2.find("buildx create"), " "), "network=host")
	require.Contains(t, string(toml), "gc = true")
	require.Contains(t, string(toml), `reservedSpace = "10GB"`)
	require.Contains(t, string(toml), `maxUsedSpace = "60%"`)
	require.Contains(t, string(toml), `minFreeSpace = "10GB"`)
}

func TestProjectBuilderIsReusedAndRecreatedWhenItsConfigChanges(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	b := pushBuilder(t, d)
	run := func() {
		_, err := b.BuildAndPush(context.Background(), goodPush(host), func(string) {})
		require.NoError(t, err)
	}
	run()
	require.Equal(t, 1, countVerb(d, "buildx create"))

	run() // same config: inspect --bootstrap only
	require.Equal(t, 1, countVerb(d, "buildx create"))
	require.Equal(t, 0, countVerb(d, "buildx rm"))

	b.GC = GCConfig{MaxUsedSpace: "20GB"}
	run() // changed GC limits: recreate, keeping the cache
	require.Equal(t, 2, countVerb(d, "buildx create"))
	rm := d.find("buildx rm")
	require.Equal(t, []string{"buildx", "rm", "--keep-state", "--builder", BuilderPrefix + "0a1b2c3d"}, rm)
	toml, _ := os.ReadFile(filepath.Join(b.StateDir, "buildx", BuilderPrefix+"0a1b2c3d.toml"))
	require.Contains(t, string(toml), `maxUsedSpace = "20GB"`)
}

func countVerb(d *pushDocker, verb string) int {
	n := 0
	for _, v := range d.verbs() {
		if v == verb {
			n++
		}
	}
	return n
}

func TestBuildAndPushRailpackGoesThroughTheSameBuildxPath(t *testing.T) {
	host, _ := registry(t)
	d := newPushDocker(t)
	var info agentprotocol.BuildInfo
	b := &Builder{StateDir: t.TempDir(), MinFreeBytes: -1, Run: d.run,
		Clone: func(_ context.Context, _ Request, dir string) error {
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"start":"node server.js"}}`), 0o644))
			return os.WriteFile(filepath.Join(dir, "server.js"), nil, 0o644)
		}}
	req := goodPush(host)
	req.Info = &info
	req.Vars = Vars{Args: map[string]string{"A": "b"}, Secrets: map[string]string{"TOKEN": "tok-abcdef"}}
	_, err := b.BuildAndPush(context.Background(), req, func(string) {})
	require.NoError(t, err)

	require.Equal(t, "railpack", info.Method)
	require.Equal(t, "buildx", info.Builder)
	require.Len(t, d.buildArgs, 1)
	ba := strings.Join(d.buildArgs[0], " ")
	require.Contains(t, ba, "--push")
	require.Contains(t, ba, "--platform linux/arm64")
	require.Contains(t, ba, "--build-arg BUILDKIT_SYNTAX="+agentprotocol.RailpackFrontendImage)
	require.Contains(t, ba, "railpack-plan.json")
	require.Contains(t, ba, "--secret id=A,env=NCBS_A", "Railpack hands every variable over as a BuildKit secret")
	require.Contains(t, ba, "--secret id=TOKEN,env=NCBS_TOKEN")
	require.NotContains(t, ba, "tok-abcdef")
	require.Equal(t, "tok-abcdef", envValue(d.buildEnvs[0], "NCBS_TOKEN"))
	require.NotContains(t, ba, "--load")
	require.NoDirExists(t, d.cfgDir)
}

func TestReadPushedDigest(t *testing.T) {
	write := func(content string) string {
		p := filepath.Join(t.TempDir(), "metadata.json")
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}
	got, err := readPushedDigest(write(`{"containerimage.digest":"` + testDigest + `","containerimage.descriptor":{"size":7}}`))
	require.NoError(t, err)
	require.Equal(t, testDigest, got)

	for name, content := range map[string]string{
		"missing key":   `{"image.name":"x"}`,
		"short digest":  `{"containerimage.digest":"sha256:abc"}`,
		"wrong algo":    `{"containerimage.digest":"sha512:` + strings.Repeat("a", 64) + `"}`,
		"not json":      `nope`,
		"wrong type":    `{"containerimage.digest":5}`,
		"uppercase hex": `{"containerimage.digest":"sha256:` + strings.Repeat("A", 64) + `"}`,
	} {
		_, err := readPushedDigest(write(content))
		require.Error(t, err, name)
	}
	_, err = readPushedDigest(filepath.Join(t.TempDir(), "absent.json"))
	require.ErrorContains(t, err, "read build metadata")
}

func TestPushRequestValidate(t *testing.T) {
	require.NoError(t, goodPush("reg.example.com:5000").Validate())
	for name, mut := range map[string]func(*PushRequest){
		"no host":             func(r *PushRequest) { r.Repository = "acme/web" },
		"empty path":          func(r *PushRequest) { r.Repository = "reg.example.com/" },
		"uppercase repo":      func(r *PushRequest) { r.Repository = "reg.example.com/Acme/Web" },
		"scheme in repo":      func(r *PushRequest) { r.Repository = "https://reg.example.com/acme/web" },
		"bad tag":             func(r *PushRequest) { r.Tag = "a b" },
		"empty tag":           func(r *PushRequest) { r.Tag = "" },
		"windows platform":    func(r *PushRequest) { r.Platform = "windows/amd64" },
		"flag-like platform":  func(r *PushRequest) { r.Platform = "linux/amd64 --privileged" },
		"foreign builder":     func(r *PushRequest) { r.Builder = "default" },
		"builder with option": func(r *PushRequest) { r.Builder = BuilderPrefix + "x --driver host" },
		"bad cache ref":       func(r *PushRequest) { r.CacheRef = "a,b" },
		"traversal":           func(r *PushRequest) { r.DockerfilePath = "../Dockerfile" },
	} {
		r := goodPush("reg.example.com")
		mut(&r)
		require.Error(t, r.Validate(), name)
	}
}

func TestWriteDockerConfigHoldsOnlyThePushCredential(t *testing.T) {
	def := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(def, "cli-plugins"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(def, "contexts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(def, "config.json"),
		[]byte(`{"auths":{"evil.example":{"auth":"c2VjcmV0"}},"credsStore":"desktop","currentContext":"desktop-linux","cliPluginsExtraDirs":["/opt/plugins"]}`), 0o600))
	t.Setenv("DOCKER_CONFIG", def)

	root := filepath.Join(t.TempDir(), "dockercfg")
	dir, err := writeDockerConfig(root, "reg.example.com", &agentprotocol.RegistryAuth{Username: "u", Password: "pw"})
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.NotContains(t, string(raw), "evil.example", "credentials of the host's own config are not copied")
	require.NotContains(t, cfg, "credsStore")
	require.Equal(t, "desktop-linux", cfg["currentContext"])
	require.ElementsMatch(t, []any{"/opt/plugins", filepath.Join(def, "cli-plugins")}, cfg["cliPluginsExtraDirs"])
	auths := cfg["auths"].(map[string]any)
	require.Len(t, auths, 1)
	require.Contains(t, auths, "reg.example.com")
	target, err := os.Readlink(filepath.Join(dir, "contexts"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(def, "contexts"), target)
	st, err := os.Stat(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	// Docker Hub is keyed the way `docker login` stores it.
	dir2, err := writeDockerConfig(root, "docker.io", &agentprotocol.RegistryAuth{Username: "u", Password: "pw"})
	require.NoError(t, err)
	raw, _ = os.ReadFile(filepath.Join(dir2, "config.json"))
	require.Contains(t, string(raw), "https://index.docker.io/v1/")

	// No credential: an empty auths section, never a stale one.
	dir3, err := writeDockerConfig(root, "reg.example.com", nil)
	require.NoError(t, err)
	raw, _ = os.ReadFile(filepath.Join(dir3, "config.json"))
	require.NotContains(t, string(raw), "auths")

	// A crashed agent's leftovers are swept.
	old := time.Now().Add(-3 * time.Hour)
	require.NoError(t, os.Chtimes(dir, old, old))
	sweepStaleDockerConfigs(root, time.Hour)
	require.NoDirExists(t, dir)
	require.DirExists(t, dir2)
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:5000": true, "localhost:5000": true, "localhost": true, "[::1]:5000": true, "127.0.0.2": true,
		"registry.example.com": false, "10.0.0.5:5000": false, "ghcr.io": false,
	} {
		require.Equal(t, want, IsLoopbackHost(host), host)
	}
}
