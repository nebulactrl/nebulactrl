// Package build clones git sources and builds images on the agent's own
// Docker host (ADR 0004): no registry, the local image ID is the digest.
package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DefaultTimeout bounds a clone plus build.
const DefaultTimeout = 30 * time.Minute

// Request describes one git build.
type Request struct {
	DeploymentID   string
	ServiceID      string
	ServiceSlug    string
	GitURL         string
	CommitSHA      string
	DockerfilePath string // relative to the repository root; default "Dockerfile"
	BuildContext   string // relative to the repository root; default "."
	Token          string // HTTPS token; empty for public repositories
	// TokenUser is the basic-auth user that goes with Token (default x-access-token).
	TokenUser string
	// SSHKey is the OpenSSH private key of the deploy key, for ssh:// and
	// scp-style GitURLs (required for them; the token does not apply).
	SSHKey string
	// KnownHosts are the pinned SSH host keys as known_hosts lines. A host that
	// is not listed fails the clone: the agent never trusts a key on first use.
	KnownHosts []string
	// Vars are the build-time variables (--build-arg and BuildKit secrets).
	Vars Vars
	// BuildType selects the build method (agentprotocol.Build*); empty is auto.
	BuildType string
	// Info, when non-nil, receives how the source was built.
	Info *agentprotocol.BuildInfo
	// NoCache builds with --no-cache --pull: no layer, cache mount or base image
	// from an earlier build is reused. It applies to this build only.
	NoCache bool
	// Submodules is the submodule mode (agentprotocol.Submodules*); empty is auto.
	Submodules string
	// CacheKey namespaces Railpack's BuildKit cache mounts (the project id;
	// empty falls back to ServiceID). Builds of other projects on the same
	// BuildKit builder never see, or poison, these caches.
	CacheKey string
	// Logf receives progress and warnings of the checkout (submodules); nil is silent.
	Logf func(string)
}

// ImageTag returns nc-build/<slug>:<deploymentId8>.
func ImageTag(slug, deploymentID string) string {
	return agentprotocol.BuildImagePrefix + slug + ":" + agentprotocol.Short(deploymentID)
}

// ImageRepo returns nc-build/<slug>.
func ImageRepo(slug string) string { return agentprotocol.BuildImagePrefix + slug }

func (r *Request) applyDefaults() {
	if r.DockerfilePath == "" {
		r.DockerfilePath = "Dockerfile"
	}
	if r.BuildContext == "" {
		r.BuildContext = "."
	}
}

// Validate checks the request. Paths must be relative and stay inside the
// repository; the repository URL must be non-empty and the commit pinned.
func (r Request) Validate() error {
	r.applyDefaults()
	if err := ValidateRelPath("dockerfilePath", r.DockerfilePath); err != nil {
		return err
	}
	if err := ValidateRelPath("buildContext", r.BuildContext); err != nil {
		return err
	}
	if r.BuildType != "" && !agentprotocol.ValidBuildType(r.BuildType) {
		return fmt.Errorf("unknown build type %q", r.BuildType)
	}
	if !agentprotocol.ValidSubmodules(r.Submodules) {
		return fmt.Errorf("unknown submodules mode %q", r.Submodules)
	}
	if r.GitURL == "" {
		return errors.New("git url is required")
	}
	if gitssh.LooksLikeSSH(r.GitURL) {
		if _, err := gitssh.ParseURL(r.GitURL); err != nil {
			return fmt.Errorf("invalid ssh git url: %w", err)
		}
		if r.SSHKey == "" {
			return errors.New("an SSH git url needs a deploy key (set one in the service's source settings)")
		}
	}
	if len(r.CommitSHA) < 7 || !isHex(r.CommitSHA) {
		return fmt.Errorf("invalid commit sha %q", r.CommitSHA)
	}
	if r.ServiceSlug == "" || r.DeploymentID == "" {
		return errors.New("service slug and deployment id are required")
	}
	// The deployment id names the workspace directory builds/<id>, which is
	// emptied before use: it must stay one plain path segment.
	if !deploymentIDRE.MatchString(r.DeploymentID) {
		return fmt.Errorf("invalid deployment id %q", r.DeploymentID)
	}
	return r.Vars.validate()
}

var deploymentIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ValidateRelPath rejects empty paths, absolute paths, NUL bytes and any ".."
// segment (same rule as the control plane's services.ValidateRelPath).
func ValidateRelPath(field, p string) error {
	if p == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || strings.Contains(p, "\x00") {
		return fmt.Errorf("%s must be a relative path", field)
	}
	if len(p) >= 2 && p[1] == ':' { // windows drive
		return fmt.Errorf("%s must be a relative path", field)
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return fmt.Errorf("%s must not contain ..", field)
		}
	}
	if strings.HasPrefix(path.Clean(p), "..") {
		return fmt.Errorf("%s must not escape the repository", field)
	}
	return nil
}

// Builder clones and builds. The zero value is not usable; set StateDir.
type Builder struct {
	// StateDir holds builds/<deploymentId> workspaces.
	StateDir string
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
	// OnSource, when set, is called once the commit is checked out and the
	// build context resolved, before anything is built.
	OnSource func(req Request, repoDir, ctxDir string)

	// HTTP is the client of registry checks (nil: a default client with a timeout).
	HTTP *http.Client
	// GC is the BuildKit cache GC of per-project builders (zero: defaults).
	GC GCConfig
	// MinFreeBytes is the free space a pushed build needs to start (0:
	// DefaultMinFreeBytes, negative: no check). FreeBytes reports the free space
	// of a path (nil: the real filesystem); tests replace it.
	MinFreeBytes int64
	FreeBytes    func(path string) (uint64, error)

	// Clone and Run are test hooks; nil uses go-git and the docker CLI. Run
	// executes one docker invocation; env holds extra environment entries for
	// the child (build secrets), nil for none.
	Clone func(ctx context.Context, req Request, dir string) error
	Run   func(ctx context.Context, args []string, env []string, out io.Writer) error
}

func (b *Builder) run(ctx context.Context, args, env []string, out io.Writer) error {
	if b.Run != nil {
		return b.Run(ctx, args, env, out)
	}
	return runDocker(ctx, args, env, out)
}

// workspaceNameRE is one plain path segment: what a workspace name (a
// deployment id, with or without the "plan-" prefix) may consist of.
var workspaceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// workspaceDir is the workspace builds/<name> below stateDir. The directory is
// emptied before use and removed after, so the name must stay a single local
// path segment and can never name stateDir or anything above it.
func workspaceDir(stateDir, name string) (string, error) {
	if !workspaceNameRE.MatchString(name) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("invalid workspace name %q", name)
	}
	return filepath.Join(stateDir, "builds", name), nil
}

// checkout creates the workspace builds/<id>, clones req there and resolves
// the build context. The caller must call cleanup.
func (b *Builder) checkout(ctx context.Context, req Request, id string, logf func(string)) (dir, repoDir, ctxDir string, cleanup func(), err error) {
	if b.StateDir == "" {
		return "", "", "", nil, errors.New("build: state dir not configured")
	}
	dir, err = workspaceDir(b.StateDir, id)
	if err != nil {
		return "", "", "", nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", "", "", nil, fmt.Errorf("clean build dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", nil, fmt.Errorf("create build dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	repoDir = filepath.Join(dir, "src")

	logf(fmt.Sprintf("Cloning %s at %s", redactGitURL(req.GitURL), short(req.CommitSHA)))
	req.Logf = logf
	clone := b.Clone
	if clone == nil {
		clone = gitClone
	}
	if err := clone(ctx, req, repoDir); err != nil {
		cleanup()
		return "", "", "", nil, fmt.Errorf("git clone: %w", err)
	}
	ctxDir, err = resolveInside(repoDir, req.BuildContext)
	if err != nil {
		cleanup()
		return "", "", "", nil, fmt.Errorf("buildContext: %w", err)
	}
	if st, err := os.Stat(ctxDir); err != nil || !st.IsDir() {
		cleanup()
		return "", "", "", nil, fmt.Errorf("buildContext: %q is not a directory in repository", req.BuildContext)
	}
	return dir, repoDir, ctxDir, cleanup, nil
}

// Build clones req at its commit, picks the build method (Dockerfile, Railpack
// or a starter Dockerfile) and builds, streaming every output line to logf. It
// returns the built image's ID. The workspace is always removed.
func (b *Builder) Build(ctx context.Context, req Request, logf func(string)) (string, error) {
	req.applyDefaults()
	if err := req.Validate(); err != nil {
		return "", err
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dir, repoDir, ctxDir, cleanup, err := b.checkout(ctx, req, req.DeploymentID, logf)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if b.OnSource != nil {
		b.OnSource(req, repoDir, ctxDir)
	}

	dec, err := Decide(req.BuildType, repoDir, req.DockerfilePath)
	if err != nil {
		return "", err
	}
	info := dec.BuildInfo
	logf("Build method: " + Describe(info) + ". " + info.Reason)
	if req.NoCache {
		logf("Deploying without cache: --no-cache --pull for this build only")
	}

	iid := filepath.Join(dir, "image.iid")
	var (
		args    []string
		backend string
	)
	buildVars := req.Vars
	switch dec.Method {
	case agentprotocol.MethodDockerfile:
		args = Args(req, dec.DockerfileAbs, iid, ctxDir)
	case agentprotocol.MethodStarter:
		file, err := writeStarter(dir, dec.Starter, ctxDir, logf)
		if err != nil {
			return "", err
		}
		args = Args(req, file, iid, ctxDir)
	case agentprotocol.MethodRailpack:
		plan, err := b.railpackPrepare(ctx, "nc-railpack-"+agentprotocol.Short(req.DeploymentID), ctxDir, dir, allVars(req.Vars), logf)
		if err != nil {
			return "", err
		}
		backend = b.railpackBackend(ctx)
		// Railpack hands every variable to its steps as a BuildKit secret, the
		// runtime defaults it was planned with included: the plan names them.
		req.Vars = Vars{Secrets: plan.Vars}
		buildVars = req.Vars
		if backend == "buildx" {
			if err := b.ensureBuildx(ctx, logf); err != nil {
				return "", err
			}
		}
		plan.Info.Reason = info.Reason
		plan.Info.Builder = backend
		info = plan.Info
		logf(Describe(info) + ": " + info.StartCommand)
		args = railpackBuildArgs(req, plan.PlanFile, iid, ctxDir, backend)
	}

	tail := &tailBuffer{max: 4096}
	lw := &LineWriter{Fn: logf}
	runBuild := func(args []string) error {
		logf("$ docker " + strings.Join(maskArgs(args, req.Vars), " "))
		tail.Reset()
		return b.run(ctx, args, buildVars.Env(nil), io.MultiWriter(lw, tail))
	}
	err = runBuild(args)
	if err != nil && dec.Method == agentprotocol.MethodRailpack && backend == "docker" && errNeedsMergeop(tail.String()) {
		// The daemon advertised the containerd store but still refuses
		// mergeop: fall back to the managed BuildKit container.
		logf("This Docker daemon cannot run Railpack's build graph; retrying with the managed BuildKit builder")
		if berr := b.ensureBuildx(ctx, logf); berr != nil {
			lw.Flush()
			return "", berr
		}
		info.Builder = "buildx"
		args = railpackBuildArgs(req, filepath.Join(dir, "railpack-plan.json"), iid, ctxDir, "buildx")
		err = runBuild(args)
	}
	lw.Flush()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("docker build timed out after %s", timeout)
		}
		return "", fmt.Errorf("docker build: %w", err)
	}
	raw, err := os.ReadFile(iid)
	if err != nil {
		return "", fmt.Errorf("read image id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", errors.New("docker build produced no image id")
	}
	if req.Info != nil {
		*req.Info = info
	}
	return id, nil
}

// Describe names a build method for logs, e.g. "Railpack 0.43.0 (node)".
func Describe(i agentprotocol.BuildInfo) string {
	switch i.Method {
	case agentprotocol.MethodRailpack:
		s := "Railpack"
		if i.RailpackVersion != "" {
			s += " " + i.RailpackVersion
		}
		if len(i.Providers) > 0 {
			s += " (" + strings.Join(i.Providers, ", ") + ")"
		}
		return s
	case agentprotocol.MethodStarter:
		return "starter Dockerfile (" + i.Starter + ")"
	}
	return "Dockerfile"
}

// writeStarter writes the generated Dockerfile (and a .dockerignore that keeps
// .git out of the image) next to the clone, never inside the repository, and
// prints the Dockerfile to the build log.
func writeStarter(dir, lang, ctxDir string, logf func(string)) (string, error) {
	text, err := StarterDockerfile(lang, ctxDir)
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, "Dockerfile.starter")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write starter Dockerfile: %w", err)
	}
	ignore := StarterIgnore(lang)
	if user, err := readRepoFile(ctxDir, ".dockerignore"); err == nil {
		ignore += "\n" + string(user)
	}
	if err := os.WriteFile(file+".dockerignore", []byte(ignore), 0o600); err != nil {
		return "", fmt.Errorf("write starter .dockerignore: %w", err)
	}
	logf("Generated Dockerfile for starter:" + lang)
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		logf("  " + l)
	}
	return file, nil
}

// Args is the exact `docker build` argument vector (no shell involved) for a
// Dockerfile. Build-time variables follow Vars.DockerArgs: secret values are
// never part of it.
func Args(req Request, dockerfile, iidFile, contextDir string) []string {
	args := []string{
		"build", "--progress=plain",
		"-f", dockerfile,
		"-t", ImageTag(req.ServiceSlug, req.DeploymentID),
		"--label", agentprotocol.LabelManaged + "=true",
		"--label", agentprotocol.LabelService + "=" + req.ServiceID,
		"--iidfile", iidFile,
	}
	args = append(args, req.noCacheArgs()...)
	args = append(args, req.Vars.DockerArgs()...)
	return append(args, contextDir)
}

// noCacheArgs are the flags of a "deploy without cache" build: --no-cache skips
// every cached layer and --pull refetches the base images instead of using the
// ones already on the host. They are valid for `docker build` and `docker
// buildx build` alike.
func (r Request) noCacheArgs() []string {
	if !r.NoCache {
		return nil
	}
	return []string{"--no-cache", "--pull"}
}

// maskArgs hides the values of --build-arg flags for logging.
func maskArgs(args []string, v Vars) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] != "--build-arg" {
			continue
		}
		if k, _, ok := strings.Cut(out[i+1], "="); ok {
			if _, secret := v.Args[k]; secret {
				out[i+1] = k + "=•••"
			}
		}
	}
	return out
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}
func (t *tailBuffer) Reset()         { t.buf = t.buf[:0] }
func (t *tailBuffer) String() string { return string(t.buf) }

// detached returns a context for cleanup that survives cancellation of ctx.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
}

func runDocker(ctx context.Context, args, env []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	// A canceled build (a person canceled the deploy, or the timeout) gets
	// SIGINT first: the docker CLI then tells BuildKit to stop the build instead
	// of leaving it running on the daemon. After WaitDelay the process is killed.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// Prune drops build cache older than olderThan: the daemon's (`docker builder
// prune`) and, when it exists, the managed BuildKit builder's. It returns the
// space the two reported as reclaimed.
func Prune(ctx context.Context, olderThan time.Duration, logf func(string)) (int64, error) {
	var reclaimed int64
	lw := &LineWriter{Fn: func(line string) {
		if n, ok := parseTotal(line); ok {
			reclaimed += n
		}
		logf(line)
	}}
	until := "until=" + strconv.Itoa(int(olderThan.Hours())) + "h"
	err := runDocker(ctx, []string{"builder", "prune", "-f", "--filter", until}, nil, lw)
	if runDocker(ctx, []string{"buildx", "inspect", agentprotocol.BuildxBuilderName}, nil, io.Discard) == nil {
		if berr := runDocker(ctx, []string{"buildx", "prune", "--builder", agentprotocol.BuildxBuilderName, "-f", "--filter", until}, nil, lw); err == nil {
			err = berr
		}
	}
	lw.Flush()
	return reclaimed, err
}

// parseTotal reads the "Total:\t1.234GB" line `docker builder prune` and
// `docker buildx prune` end with (decimal units, as go-units prints them).
func parseTotal(line string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Total:")
	if !ok {
		return 0, false
	}
	rest = strings.TrimSpace(rest)
	i := strings.IndexFunc(rest, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(rest[:i], 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}[strings.TrimSpace(rest[i:])]
	if mult == 0 {
		return 0, false
	}
	return int64(v * mult), true
}

// cloneOptions returns the go-git clone options for req (URL and credentials)
// and a function that turns a failed clone's error into one worth showing.
func cloneOptions(req Request) (*git.CloneOptions, func(error) error, error) {
	opts := &git.CloneOptions{URL: req.GitURL, NoCheckout: true, Tags: git.NoTags}
	explain := func(err error) error { return err }
	if gitssh.LooksLikeSSH(req.GitURL) {
		// Never fall back to ssh-agent or ~/.ssh/known_hosts: only the deploy
		// key and the pinned host keys the control plane sent count.
		ep, err := gitssh.ParseURL(req.GitURL)
		if err != nil {
			return nil, nil, err
		}
		auth, pins, err := gitssh.Credentials{PrivateKeyPEM: []byte(req.SSHKey), KnownHosts: req.KnownHosts}.Auth(ep)
		if err != nil {
			return nil, nil, err
		}
		opts.Auth = auth
		explain = func(err error) error { return gitssh.Explain(err, ep, pins) }
	} else if req.Token != "" {
		switch {
		case strings.HasPrefix(req.GitURL, "https://"):
			user := req.TokenUser
			if user == "" {
				user = agentprotocol.DefaultGitUsername
			}
			opts.Auth = &githttp.BasicAuth{Username: user, Password: req.Token}
		case strings.HasPrefix(req.GitURL, "http://"):
			return nil, nil, errors.New("an access token is never sent over plain http://: use an https:// URL")
		}
	}
	return opts, explain, nil
}

func gitClone(ctx context.Context, req Request, dir string) error {
	opts, explain, err := cloneOptions(req)
	if err != nil {
		return err
	}
	repo, err := cloneBound(ctx, dir, opts)
	if err != nil {
		return explain(err)
	}
	var hash plumbing.Hash
	if len(req.CommitSHA) == 40 {
		hash = plumbing.NewHash(req.CommitSHA)
	} else {
		h, err := repo.ResolveRevision(plumbing.Revision(req.CommitSHA))
		if err != nil {
			return fmt.Errorf("commit %s not found: %w", short(req.CommitSHA), err)
		}
		hash = *h
	}
	if _, err := repo.CommitObject(hash); err != nil {
		return fmt.Errorf("commit %s not found in repository: %w", short(req.CommitSHA), err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		return fmt.Errorf("checkout %s: %w", short(req.CommitSHA), err)
	}
	if agentprotocol.SubmodulesEnabled(req.Submodules) {
		return submoduleCheckout(ctx, repo, wt, req)
	}
	return nil
}

// cloneBound clones into dir through a filesystem that cannot leave dir: a
// symlink in an untrusted repository (src -> ../../..) must not make go-git
// write or read anywhere else on the host.
func cloneBound(ctx context.Context, dir string, opts *git.CloneOptions) (*git.Repository, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // checkout of the repository being built, not secret material
		return nil, err
	}
	// The bound filesystem compares paths that do not exist yet (a submodule's
	// directory) against the symlink-resolved base. A base that itself sits behind
	// a symlink (/var -> /private/var on macOS, a symlinked data dir) makes every
	// such path read as outside the base, so bind to the resolved directory.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	wt := osfs.New(resolved, osfs.WithBoundOS())
	dot, err := wt.Chroot(git.GitDirName)
	if err != nil {
		return nil, err
	}
	return git.CloneContext(ctx, filesystem.NewStorage(dot, cache.NewObjectLRUDefault()), wt, opts)
}

// resolveInside joins rel onto root and verifies the result, after resolving
// symlinks, is still inside root. rel must be a local path (relative, no
// element that climbs out); a link inside the repository that leads out is
// caught by the containment check on the resolved path.
func resolveInside(root, rel string) (string, error) {
	local := filepath.FromSlash(rel)
	if !filepath.IsLocal(local) {
		return "", fmt.Errorf("%w: %q", errEscapes, rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	p, err := filepath.EvalSymlinks(filepath.Join(realRoot, local))
	if err != nil {
		return "", fmt.Errorf("%q not found in repository", rel)
	}
	r, err := filepath.Rel(realRoot, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", errEscapes, rel)
	}
	return p, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// LineWriter turns a byte stream into lines for Fn. CR and LF both end a line.
type LineWriter struct {
	Fn  func(string)
	buf []byte
}

func (w *LineWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		if c == '\n' || c == '\r' {
			w.emit()
			continue
		}
		w.buf = append(w.buf, c)
	}
	return len(p), nil
}

func (w *LineWriter) emit() {
	if len(w.buf) > 0 {
		w.Fn(string(w.buf))
		w.buf = w.buf[:0]
	}
}

// Flush emits a trailing partial line.
func (w *LineWriter) Flush() { w.emit() }

// errEscapes marks a path that resolves outside the repository.
var errEscapes = errors.New("path escapes the repository")
