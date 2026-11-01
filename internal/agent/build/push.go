package build

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Build on a build server and push to a registry (build_image). One
// `docker buildx build --push` against a per-project BuildKit builder does
// everything: nothing lands in the daemon's image store, so the keep-list
// machinery of Docker cleanup never concerns a build-only host, and the
// manifest digest comes from --metadata-file.
//
// Credentials: the registry credential lives in a throwaway DOCKER_CONFIG that
// holds nothing else and is removed when the build ends (also when it is
// canceled or the agent is told to stop). BUILDX_CONFIG points at a persistent
// directory under the state dir so the per-project builders survive.

// BuilderPrefix prefixes the per-project builder names
// (nebulactrl-buildkit-<project8>).
const BuilderPrefix = agentprotocol.BuildxBuilderName + "-"

// Default disk and BuildKit garbage collection limits of a build server.
const (
	// DefaultMinFreeBytes is the free space a build needs to start.
	DefaultMinFreeBytes = 5 << 30

	DefaultGCReservedSpace = "10GB"
	DefaultGCMaxUsedSpace  = "60%"
	DefaultGCMinFreeSpace  = "10GB"
)

// GCConfig is the BuildKit cache garbage collection of a per-project builder
// (buildkitd.toml [worker.oci]). Sizes are BuildKit strings: "10GB" or "60%".
type GCConfig struct {
	ReservedSpace string
	MaxUsedSpace  string
	MinFreeSpace  string
}

func (g GCConfig) withDefaults() GCConfig {
	if g.ReservedSpace == "" {
		g.ReservedSpace = DefaultGCReservedSpace
	}
	if g.MaxUsedSpace == "" {
		g.MaxUsedSpace = DefaultGCMaxUsedSpace
	}
	if g.MinFreeSpace == "" {
		g.MinFreeSpace = DefaultGCMinFreeSpace
	}
	return g
}

// PushRequest describes one build that is pushed to a registry.
type PushRequest struct {
	// Request carries the source (git, paths, credentials, variables). Its
	// ServiceSlug names logs only; the image name is Repository:Tag.
	Request
	// Platform is the single target platform, e.g. linux/arm64.
	Platform string
	// Repository is host/ns/repo (no tag); Tag is r-<release8>.
	Repository string
	Tag        string
	// Builder is the per-project builder, nebulactrl-buildkit-<project8>.
	Builder string
	// CacheRef is an optional registry cache ref ("" = the builder's local cache).
	CacheRef string
	// Insecure allows a plain-http registry. It is only ever set for loopback
	// registries and tests: the builder is configured with http=true,
	// insecure=true for the registry host and shares the daemon's network.
	Insecure bool
	// Auth is the push credential; nil pushes anonymously.
	Auth *agentprotocol.RegistryAuth
	// OnPush, when set, is called once when the build has produced the image
	// and the push starts (and always before a successful return).
	OnPush func()
}

// PushResult is what a pushed build produced.
type PushResult struct {
	// Digest is the manifest digest, sha256:….
	Digest string
	// Ref is Repository@Digest.
	Ref       string
	SizeBytes int64
	Info      agentprotocol.BuildInfo
}

var (
	hostRE     = regexp.MustCompile(`^(\[[0-9a-fA-F:]+\]|[a-z0-9]([a-z0-9.-]*[a-z0-9])?)(:[0-9]{1,5})?$`)
	repoPartRE = regexp.MustCompile(`^[a-z0-9]+([._-]{1,2}[a-z0-9]+)*$`)
	tagRE      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	platformRE = regexp.MustCompile(`^linux/[a-z0-9]+(/[a-z0-9]+)?$`)
	builderRE  = regexp.MustCompile(`^` + regexp.QuoteMeta(BuilderPrefix) + `[a-z0-9][a-z0-9-]*$`)
	digestRE   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	phaseRE    = regexp.MustCompile(`exporting to image|pushing layers|pushing manifest`)
)

// RegistryHost returns the registry host of a repository (host/ns/repo).
func RegistryHost(repository string) string {
	host, _, _ := strings.Cut(repository, "/")
	return host
}

// Validate checks the request before anything is cloned or run.
func (r PushRequest) Validate() error {
	if err := r.Request.Validate(); err != nil {
		return err
	}
	host, rest, ok := strings.Cut(r.Repository, "/")
	// Like the Docker CLI, a first path segment is only a registry host when
	// it has a dot or a port or is localhost; "acme/web" would mean Docker Hub.
	if !ok || !hostRE.MatchString(host) || rest == "" || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return fmt.Errorf("invalid repository %q: want host[:port]/namespace/name", r.Repository)
	}
	for _, p := range strings.Split(rest, "/") {
		if !repoPartRE.MatchString(p) {
			return fmt.Errorf("invalid repository %q", r.Repository)
		}
	}
	if !tagRE.MatchString(r.Tag) {
		return fmt.Errorf("invalid tag %q", r.Tag)
	}
	if !platformRE.MatchString(r.Platform) {
		return fmt.Errorf("invalid platform %q", r.Platform)
	}
	if !builderRE.MatchString(r.Builder) {
		return fmt.Errorf("invalid builder name %q: want %s<project>", r.Builder, BuilderPrefix)
	}
	if r.CacheRef != "" && strings.ContainsAny(r.CacheRef, " ,\t\n\"'") {
		return fmt.Errorf("invalid cache ref %q", r.CacheRef)
	}
	if r.Insecure && !hostRE.MatchString(host) {
		return errors.New("insecure registry host is invalid")
	}
	return nil
}

// BuildxVersion reports the docker buildx plugin's version, or an error when
// the plugin is missing (the agent then does not advertise CapBuildPush).
func BuildxVersion(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "buildx", "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// pushArgs is the exact `docker buildx build` argument vector (no shell). file
// is the Dockerfile or, with railpack, the plan file. Secret values are never
// part of it.
func pushArgs(req PushRequest, file, metaFile, ctxDir string, railpack bool) []string {
	args := []string{
		"buildx", "build", "--builder", req.Builder,
		"--platform", req.Platform,
		"--push", "--provenance=false", "--sbom=false",
		"--progress=plain",
		"--metadata-file", metaFile,
		"-f", file,
		"-t", req.Repository + ":" + req.Tag,
		"--label", agentprotocol.LabelManaged + "=true",
		"--label", agentprotocol.LabelService + "=" + req.ServiceID,
	}
	args = append(args, req.noCacheArgs()...)
	vars := req.Vars
	if railpack {
		args = append(args, "--build-arg", "BUILDKIT_SYNTAX="+agentprotocol.RailpackFrontendImage,
			"--build-arg", "cache-key="+req.railpackCacheKey())
		vars = Vars{Secrets: allVars(req.Vars)}
	}
	args = append(args, vars.DockerArgs()...)
	if railpack && len(vars.Secrets) > 0 {
		args = append(args, "--build-arg", "secrets-hash="+secretsHash(vars.Secrets))
	}
	if req.CacheRef != "" {
		args = append(args,
			"--cache-from", "type=registry,ref="+req.CacheRef,
			"--cache-to", "type=registry,ref="+req.CacheRef+",mode=max,image-manifest=true,oci-mediatypes=true,ignore-error=true")
	}
	return append(args, ctxDir)
}

// buildkitdConfig is the buildkitd.toml of a per-project builder: cache GC
// limits and, for a plain-http registry, its http/insecure entry.
func buildkitdConfig(gc GCConfig, host string, insecure bool) string {
	gc = gc.withDefaults()
	var b strings.Builder
	b.WriteString("[worker.oci]\n  gc = true\n")
	fmt.Fprintf(&b, "  reservedSpace = %q\n  maxUsedSpace = %q\n  minFreeSpace = %q\n", gc.ReservedSpace, gc.MaxUsedSpace, gc.MinFreeSpace)
	if insecure {
		fmt.Fprintf(&b, "\n[registry.%q]\n  http = true\n  insecure = true\n", host)
	}
	return b.String()
}

// builderLocks serializes the creation of one builder (two builds of the same
// project may start together).
var builderLocks sync.Map // builder name -> *sync.Mutex

func (b *Builder) buildxDir() string { return filepath.Join(b.StateDir, "buildx") }

// builderFile is the state file of a per-project builder (<builder><ext> in
// dir). The builder name comes from the control plane: it must keep the
// builder-name shape and stay one local file name.
func builderFile(dir, builder, ext string) (string, error) {
	name := builder + ext
	if !builderRE.MatchString(builder) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("invalid builder name %q: want %s<project>", builder, BuilderPrefix)
	}
	return filepath.Join(dir, name), nil
}

// ensureProjectBuilder creates the project's docker-container builder when it
// is missing or when its configuration (GC limits, insecure registry) changed.
// A recreate keeps the build cache (--keep-state).
func (b *Builder) ensureProjectBuilder(ctx context.Context, env []string, req PushRequest, logf func(string)) error {
	v, _ := builderLocks.LoadOrStore(req.Builder, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	host := RegistryHost(req.Repository)
	loopback := req.Insecure && IsLoopbackHost(host)
	cfg := buildkitdConfig(b.GC, host, req.Insecure)
	sum := sha256.Sum256([]byte(cfg + "|" + agentprotocol.BuildKitImage + "|" + fmt.Sprint(loopback)))
	want := hex.EncodeToString(sum[:])
	dir := b.buildxDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create buildx dir: %w", err)
	}
	cfgFile, err := builderFile(dir, req.Builder, ".toml")
	if err != nil {
		return err
	}
	hashFile, err := builderFile(dir, req.Builder, ".sha256")
	if err != nil {
		return err
	}

	lw := &LineWriter{Fn: logf}
	defer lw.Flush()
	exists := b.run(ctx, []string{"buildx", "inspect", "--builder", req.Builder}, env, io.Discard) == nil
	if exists {
		have, _ := os.ReadFile(hashFile)
		if strings.TrimSpace(string(have)) == want {
			if b.run(ctx, []string{"buildx", "inspect", "--bootstrap", "--builder", req.Builder}, env, io.Discard) == nil {
				return nil
			}
			logf("The BuildKit builder " + req.Builder + " did not start; recreating it")
		} else {
			logf("The BuildKit configuration of " + req.Builder + " changed; recreating it (the cache is kept)")
		}
		if err := b.run(ctx, []string{"buildx", "rm", "--keep-state", "--builder", req.Builder}, env, lw); err != nil {
			return fmt.Errorf("remove the BuildKit builder %s: %w", req.Builder, err)
		}
	}
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("write buildkitd config: %w", err)
	}
	logf("Creating the BuildKit builder " + req.Builder)
	args := []string{"buildx", "create", "--name", req.Builder, "--driver", "docker-container",
		"--driver-opt", "image=" + agentprotocol.BuildKitImage}
	if loopback {
		// The BuildKit container's 127.0.0.1 is not the host's: a registry
		// published on the host's loopback is only reachable on the host network.
		args = append(args, "--driver-opt", "network=host")
	}
	args = append(args, "--buildkitd-config", cfgFile, "--bootstrap")
	if err := b.run(ctx, args, env, lw); err != nil {
		return fmt.Errorf("create the BuildKit builder (is the docker buildx plugin installed?): %w", err)
	}
	if err := os.WriteFile(hashFile, []byte(want+"\n"), 0o600); err != nil {
		return fmt.Errorf("record builder config: %w", err)
	}
	return nil
}

// defaultDockerConfigDir is where the docker CLI keeps its configuration.
func defaultDockerConfigDir() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".docker")
	}
	return ""
}

// writeDockerConfig creates a throwaway DOCKER_CONFIG under <state>/dockercfg
// that holds only the push credential. It keeps what the CLI needs to find its
// plugins (buildx) and the daemon (current context) by reference, never copying
// credentials from the host's own configuration. The caller removes the
// directory.
func writeDockerConfig(root, host string, auth *agentprotocol.RegistryAuth) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "b-")
	if err != nil {
		return "", err
	}
	cfg := map[string]any{}
	if auth != nil && auth.Username != "" {
		_, key := registryEndpoint(host)
		cfg["auths"] = map[string]any{key: map[string]string{
			"auth": base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password)),
		}}
	}
	if def := defaultDockerConfigDir(); def != "" {
		var extra []string
		if raw, err := os.ReadFile(filepath.Join(def, "config.json")); err == nil {
			var host struct {
				CurrentContext      string   `json:"currentContext"`
				CLIPluginsExtraDirs []string `json:"cliPluginsExtraDirs"`
			}
			if json.Unmarshal(raw, &host) == nil {
				if host.CurrentContext != "" {
					cfg["currentContext"] = host.CurrentContext
				}
				extra = append(extra, host.CLIPluginsExtraDirs...)
			}
		}
		if st, err := os.Stat(filepath.Join(def, "cli-plugins")); err == nil && st.IsDir() {
			extra = append(extra, filepath.Join(def, "cli-plugins"))
		}
		if len(extra) > 0 {
			cfg["cliPluginsExtraDirs"] = extra
		}
		if st, err := os.Stat(filepath.Join(def, "contexts")); err == nil && st.IsDir() {
			_ = os.Symlink(filepath.Join(def, "contexts"), filepath.Join(dir, "contexts"))
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// sweepStaleDockerConfigs removes credential directories a crashed agent left
// behind.
func sweepStaleDockerConfigs(root string, olderThan time.Duration) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > olderThan {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

func (b *Builder) freeBytes(path string) (uint64, error) {
	if b.FreeBytes != nil {
		return b.FreeBytes(path)
	}
	u, err := disk.Usage(path)
	if err != nil {
		return 0, err
	}
	return u.Free, nil
}

// diskPreflight refuses to start a build when the state directory's filesystem
// is nearly full. Before giving up it prunes this builder's cache once.
func (b *Builder) diskPreflight(ctx context.Context, env []string, builder string, logf func(string)) error {
	min := b.MinFreeBytes
	if min == 0 {
		min = DefaultMinFreeBytes
	}
	if min < 0 {
		return nil
	}
	if err := os.MkdirAll(b.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	free, err := b.freeBytes(b.StateDir)
	if err != nil || free >= uint64(min) {
		return nil //nolint:nilerr // the preflight is best-effort: if free space cannot be read the build proceeds rather than refusing
	}
	logf(fmt.Sprintf("Only %s free on this build server; pruning the build cache of %s", humanBytes(free), builder))
	lw := &LineWriter{Fn: logf}
	_ = b.run(ctx, []string{"buildx", "prune", "--builder", builder, "-f"}, env, lw)
	lw.Flush()
	if free, err = b.freeBytes(b.StateDir); err == nil && free < uint64(min) {
		return fmt.Errorf("not enough free disk space on this build server: %s free, %s needed", humanBytes(free), humanBytes(uint64(min)))
	}
	return nil
}

func humanBytes(n uint64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// lastErrorLine picks the line of a build's output worth putting in an error.
func lastErrorLine(tail string) string {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.Contains(l, "ERROR") {
			if _, after, ok := strings.Cut(l, "ERROR:"); ok {
				return strings.TrimSpace(after)
			}
			return l
		}
	}
	return ""
}

// BuildAndPush clones req at its commit, builds it for req.Platform with the
// project's BuildKit builder and pushes it to req.Repository:req.Tag. The
// registry is checked first, before anything is cloned. The workspace and the
// credential directory are always removed.
func (b *Builder) BuildAndPush(ctx context.Context, req PushRequest, logf func(string)) (PushResult, error) {
	req.applyDefaults()
	if err := req.Validate(); err != nil {
		return PushResult{}, err
	}
	if b.StateDir == "" {
		return PushResult{}, errors.New("build: state dir not configured")
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	host := RegistryHost(req.Repository)
	logf("Checking the registry " + host)
	if res := CheckRegistry(ctx, b.HTTP, RegistryCheck{
		Host: host, Insecure: req.Insecure, Repository: req.Repository, Auth: req.Auth, WantPush: req.Auth != nil,
	}); res.Error != "" {
		return PushResult{}, errors.New("registry check failed: " + res.Error)
	}

	cfgRoot := filepath.Join(b.StateDir, "dockercfg")
	sweepStaleDockerConfigs(cfgRoot, timeout+10*time.Minute)
	cfgDir, err := writeDockerConfig(cfgRoot, host, req.Auth)
	if err != nil {
		return PushResult{}, fmt.Errorf("prepare registry credentials: %w", err)
	}
	// Removed whatever happens next, a cancel included: no context involved.
	defer func() { _ = os.RemoveAll(cfgDir) }()
	env := []string{"DOCKER_CONFIG=" + cfgDir, "BUILDX_CONFIG=" + b.buildxDir()}

	if err := b.diskPreflight(ctx, env, req.Builder, logf); err != nil {
		return PushResult{}, err
	}

	dir, repoDir, ctxDir, cleanup, err := b.checkout(ctx, req.Request, req.DeploymentID, logf)
	if err != nil {
		return PushResult{}, err
	}
	defer cleanup()
	if b.OnSource != nil {
		b.OnSource(req.Request, repoDir, ctxDir)
	}

	dec, err := Decide(req.BuildType, repoDir, req.DockerfilePath)
	if err != nil {
		return PushResult{}, err
	}
	info := dec.BuildInfo
	logf("Build method: " + Describe(info) + ". " + info.Reason)
	if req.NoCache {
		logf("Deploying without cache: --no-cache --pull for this build only")
	}

	if err := b.ensureProjectBuilder(ctx, env, req, logf); err != nil {
		return PushResult{}, err
	}

	meta := filepath.Join(dir, "metadata.json")
	var args []string
	buildVars := req.Vars
	switch dec.Method {
	case agentprotocol.MethodDockerfile:
		args = pushArgs(req, dec.DockerfileAbs, meta, ctxDir, false)
	case agentprotocol.MethodStarter:
		file, err := writeStarter(dir, dec.Starter, ctxDir, logf)
		if err != nil {
			return PushResult{}, err
		}
		args = pushArgs(req, file, meta, ctxDir, false)
	case agentprotocol.MethodRailpack:
		plan, err := b.railpackPrepare(ctx, "nc-railpack-"+agentprotocol.Short(req.DeploymentID), ctxDir, dir, allVars(req.Vars), logf)
		if err != nil {
			return PushResult{}, err
		}
		buildVars = Vars{Secrets: allVars(req.Vars)}
		plan.Info.Reason = info.Reason
		plan.Info.Builder = "buildx"
		info = plan.Info
		logf(Describe(info) + ": " + info.StartCommand)
		args = pushArgs(req, plan.PlanFile, meta, ctxDir, true)
	}

	if req.Info != nil {
		*req.Info = info
	}

	tail := &tailBuffer{max: 4096}
	pushing := false
	startPush := func() {
		if !pushing {
			pushing = true
			if req.OnPush != nil {
				req.OnPush()
			}
		}
	}
	lw := &LineWriter{Fn: func(line string) {
		if phaseRE.MatchString(line) {
			startPush()
		}
		logf(line)
	}}
	logf("$ docker " + strings.Join(maskArgs(args, req.Vars), " "))
	runErr := b.run(ctx, args, buildVars.Env(env), io.MultiWriter(lw, tail))
	lw.Flush()
	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return PushResult{}, fmt.Errorf("docker buildx build timed out after %s", timeout)
		}
		msg := "docker buildx build: " + runErr.Error()
		if l := lastErrorLine(tail.String()); l != "" {
			msg += ": " + l
		}
		return PushResult{}, errors.New(msg)
	}
	startPush()

	digest, err := readPushedDigest(meta)
	if err != nil {
		return PushResult{}, err
	}
	res := PushResult{Digest: digest, Ref: req.Repository + "@" + digest, Info: info}
	if n, err := manifestSize(ctx, b.HTTP, host, req.Insecure, req.Auth, req.Repository, digest); err == nil {
		res.SizeBytes = n
	}
	return res, nil
}

// readPushedDigest reads the manifest digest buildx wrote to --metadata-file.
func readPushedDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read build metadata: %w", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("parse build metadata: %w", err)
	}
	var digest string
	if v, ok := m["containerimage.digest"]; ok {
		_ = json.Unmarshal(v, &digest)
	}
	if !digestRE.MatchString(digest) {
		return "", errors.New("the build pushed no image digest (containerimage.digest missing from the build metadata)")
	}
	return digest, nil
}
