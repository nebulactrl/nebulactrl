// Package volbackup is the agent side of volume backups: file-level backups of
// an ordinary service's named volumes with restic.
//
// A backup is: optional pre-hook (inside the service container) -> freeze (docker
// pause) -> copy the volumes into a staging volume -> thaw -> optional post-hook
// -> upload the staged copy with restic, in the background, into one encrypted
// repository per service. The service is only ever paused (or stopped) for the
// local copy, never for the upload, and never longer than the freeze budget.
//
// Everything that touches volume bytes runs in a short-lived helper container
// started from the pinned restic image (agentprotocol.ResticImage): the agent
// may itself run in a container and cannot read /var/lib/docker. Helpers carry
// agentprotocol.LabelHelper=HelperBackup and never LabelManaged. Secrets (the
// repository password, S3 keys) are passed per exec in ExecSpec.Env, never in
// the helper's create config.
package volbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Docker is the part of docker.Engine the runner uses.
type Docker interface {
	ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error)
	ImagePull(ctx context.Context, ref, registryAuth string, progress func(line string)) error
	EnsureVolume(ctx context.Context, name string, labels map[string]string) error
	RemoveVolume(ctx context.Context, name string, force bool) error
	ContainerCreate(ctx context.Context, spec docker.ContainerSpec) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, timeout *time.Duration) error
	ContainerRemove(ctx context.Context, id string, force, removeVolumes bool) error
	ContainerInspect(ctx context.Context, idOrName string) (docker.ContainerInfo, error)
	ContainerList(ctx context.Context, f docker.ListFilter) ([]docker.ContainerInfo, error)
	ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error)
	ContainerPause(ctx context.Context, id string) error
	ContainerUnpause(ctx context.Context, id string) error
}

// ErrBusy is returned by Host.LockService when the service stayed busy.
var ErrBusy = errors.New("the service is busy")

// Host is what the runner needs from the deploy engine.
type Host interface {
	// LockService waits up to wait for the service's command lock (the one
	// deploys, restarts and scaling hold) and returns its release function.
	// It returns ErrBusy when the wait ran out. While the lock is held no
	// deployment can recreate the container, and the reconciler leaves the
	// service alone.
	LockService(ctx context.Context, serviceID string, wait time.Duration) (unlock func(), err error)
	// ServiceContainer returns the container of the service's current
	// deployment (a service with volumes has exactly one replica).
	ServiceContainer(ctx context.Context, serviceID string) (docker.ContainerInfo, bool)
	// SelfContainer returns the id of the container the agent runs in, "" when
	// the agent runs on the bare host.
	SelfContainer(ctx context.Context) string
}

// Runner executes volume backup commands.
type Runner struct {
	Docker Docker
	Host   Host
	// BackupDir is the agent's local backup directory (local destinations).
	BackupDir string
	// MkdirAll creates the backup directory before it is bind-mounted into a
	// helper (default os.MkdirAll); tests replace it.
	MkdirAll func(dir string, perm os.FileMode) error
	Log      *slog.Logger
	Now      func() time.Time

	mu        sync.Mutex
	active    map[string]struct{} // operation ids in flight (helpers the sweep must keep)
	repoLocks map[string]*sync.Mutex
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// track marks an operation as in flight until the returned function is called.
func (r *Runner) track(op string) func() {
	r.mu.Lock()
	if r.active == nil {
		r.active = map[string]struct{}{}
	}
	r.active[op] = struct{}{}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.active, op)
		r.mu.Unlock()
	}
}

func (r *Runner) isActive(op string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[op]
	return ok
}

// lockRepo serializes the restic runs of one service: a prune takes an
// exclusive repository lock, so it must never overlap a backup of the same repo.
func (r *Runner) lockRepo(ctx context.Context, serviceID string) (func(), error) {
	r.mu.Lock()
	if r.repoLocks == nil {
		r.repoLocks = map[string]*sync.Mutex{}
	}
	m := r.repoLocks[serviceID]
	if m == nil {
		m = &sync.Mutex{}
		r.repoLocks[serviceID] = m
	}
	r.mu.Unlock()
	got := make(chan struct{})
	go func() { m.Lock(); close(got) }()
	select {
	case <-got:
		return m.Unlock, nil
	case <-ctx.Done():
		go func() { <-got; m.Unlock() }()
		return nil, ctx.Err()
	}
}

// ---- helpers ----

type helperOpts struct {
	// op is the operation id (backup, restore or request id); kind a short word
	// in the container name ("bak", "frg", "rst", "fil").
	op, kind  string
	serviceID string
	// container and mode are recorded on the helper for the crash sweep: the
	// service container a backup froze or stopped, and how.
	container, mode string
	mounts          []docker.VolumeMount
	network         string
	caps            []string
	repo            repoSpec
	secrets         map[string]string
	stageVolume     string
}

// helper is a running restic helper container.
type helper struct {
	r       *Runner
	id      string
	name    string
	spec    repoSpec
	secrets map[string]string
	stage   string
}

func helperName(kind, op string) string {
	return agentprotocol.BackupHelperPrefix + kind + agentprotocol.Short(op)
}

// backupCaps are the capabilities a copy needs: read everything, keep owners
// and modes. Nothing network related.
var backupCaps = []string{"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID"}

// ensureImage pulls the restic image when it is not on the host yet.
func (r *Runner) ensureImage(ctx context.Context, logf func(string)) error {
	if _, err := r.Docker.ImageInspect(ctx, agentprotocol.ResticImage); err == nil {
		return nil
	}
	logf("pulling the backup helper image")
	if err := r.Docker.ImagePull(ctx, agentprotocol.ResticImage, "", nil); err != nil {
		return fmt.Errorf("pull the backup helper image: %w", err)
	}
	return nil
}

// newHelper starts an idle helper container with o's mounts. The caller must
// close it.
func (r *Runner) newHelper(ctx context.Context, o helperOpts, logf func(string)) (*helper, error) {
	if err := r.ensureImage(ctx, logf); err != nil {
		return nil, err
	}
	if err := r.Docker.EnsureVolume(ctx, agentprotocol.ResticCacheVolume, map[string]string{agentprotocol.LabelHelper: agentprotocol.HelperBackup}); err != nil {
		return nil, fmt.Errorf("create the backup cache volume: %w", err)
	}
	labels := map[string]string{
		agentprotocol.LabelHelper:        agentprotocol.HelperBackup,
		agentprotocol.LabelBackupID:      o.op,
		agentprotocol.LabelBackupService: o.serviceID,
	}
	if o.container != "" {
		labels[agentprotocol.LabelBackupContainer] = o.container
		labels[agentprotocol.LabelBackupMode] = o.mode
	}
	mounts := append([]docker.VolumeMount{{Volume: agentprotocol.ResticCacheVolume, Target: agentprotocol.HelperCacheDir}}, o.mounts...)
	caps := o.caps
	if caps == nil {
		caps = backupCaps
	}
	spec := docker.ContainerSpec{
		Name:           helperName(o.kind, o.op),
		Image:          agentprotocol.ResticImage,
		Entrypoint:     []string{"sleep", "86400"},
		Labels:         labels,
		Mounts:         mounts,
		NetworkMode:    o.network,
		CapDrop:        []string{"ALL"},
		CapAdd:         caps,
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "size=64m,mode=1777"},
		MemoryBytes:    agentprotocol.HelperMemoryBytes,
		NanoCPUs:       agentprotocol.HelperNanoCPUs,
	}
	// A helper of a previous try with the same operation id is stale.
	if old, err := r.Docker.ContainerInspect(ctx, spec.Name); err == nil {
		_ = r.Docker.ContainerRemove(ctx, old.ID, true, false)
	}
	id, err := r.Docker.ContainerCreate(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("create the backup helper: %w", err)
	}
	if err := r.Docker.ContainerStart(ctx, id); err != nil {
		_ = r.Docker.ContainerRemove(context.WithoutCancel(ctx), id, true, false)
		return nil, fmt.Errorf("start the backup helper: %w", err)
	}
	return &helper{r: r, id: id, name: spec.Name, spec: o.repo, secrets: o.secrets, stage: o.stageVolume}, nil
}

// close removes the helper and its staging volume. It survives a canceled ctx.
func (h *helper) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := h.r.Docker.ContainerRemove(ctx, h.id, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		h.r.log().Warn("remove backup helper", "container", h.name, "err", err)
	}
	if h.stage != "" {
		if err := h.r.Docker.RemoveVolume(ctx, h.stage, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			h.r.log().Warn("remove backup staging volume", "volume", h.stage, "err", err)
		}
	}
}

// exec runs argv in the helper.
func (h *helper) exec(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	return h.r.Docker.ContainerExec(ctx, h.id, argv, docker.ExecSpec{Env: env}, stdout, stderr)
}

// restic runs restic with the repository environment and returns the exit
// code and the tail of stderr (secrets scrubbed).
func (h *helper) restic(ctx context.Context, stdout io.Writer, args ...string) (int, string, error) {
	tail := &tailBuf{}
	if stdout == nil {
		stdout = io.Discard
	}
	code, err := h.exec(ctx, resticArgv(h.spec, args...), resticEnv(h.spec, h.secrets), stdout, tail)
	return code, scrub(tail.String(), h.secrets), err
}

// resticOK runs restic and turns a failure into an error.
func (h *helper) resticOK(ctx context.Context, stdout io.Writer, args ...string) error {
	code, msg, err := h.restic(ctx, stdout, args...)
	if err != nil {
		return fmt.Errorf("restic %s: %w", args[0], err)
	}
	if code != 0 {
		if msg == "" {
			msg = "no error output"
		}
		return fmt.Errorf("restic %s exited with status %d: %s", args[0], code, msg)
	}
	return nil
}

// openRepo makes sure the repository exists (creating it when asked) and
// removes stale locks of crashed runs.
func (h *helper) openRepo(ctx context.Context, create bool) (exists bool, err error) {
	code, msg, err := h.restic(ctx, nil, "cat", "config")
	if err != nil {
		return false, fmt.Errorf("restic cat config: %w", err)
	}
	switch code {
	case 0:
	case exitNoRepo:
		if !create {
			return false, nil
		}
		if err := h.resticOK(ctx, nil, "init", "--repository-version", "2"); err != nil {
			return false, err
		}
	case exitWrongKey:
		return false, errors.New("the repository password does not match this repository (wrong key); restore the original recovery key")
	default:
		if msg == "" {
			msg = "no error output"
		}
		return false, fmt.Errorf("cannot open the backup repository (restic exit %d): %s", code, msg)
	}
	// Only locks of dead processes are removed.
	_, _, _ = h.restic(ctx, nil, "unlock")
	return true, nil
}

// destination resolves where a repository lives from inside a helper: the
// restic repository spec, the mounts the helper needs and its network.
func (r *Runner) destination(ctx context.Context, repo agentprotocol.VolumeRepo) (repoSpec, []docker.VolumeMount, string, error) {
	if err := repo.Validate(); err != nil {
		return repoSpec{}, nil, "", err
	}
	switch repo.Destination.Kind {
	case agentprotocol.DestS3:
		return s3Repo(repo.Destination, repo.Path), nil, s3Network(repo.Destination.Endpoint), nil
	default:
		root, mounts, err := r.localMount(ctx)
		if err != nil {
			return repoSpec{}, nil, "", err
		}
		if repo.Destination.LocalDir != "" {
			if err := agentprotocol.ValidObjectKey(repo.Destination.LocalDir); err != nil {
				return repoSpec{}, nil, "", fmt.Errorf("invalid local directory: %w", err)
			}
		}
		// A local repository needs no network at all.
		return localRepo(root, repo.Destination, repo.Path), mounts, "none", nil
	}
}

// localMount makes the agent's backup directory visible in a helper. On the
// bare host that is a bind mount of the directory. An agent in a container
// only knows the directory by its in-container path: the mount of the agent's
// own container that covers it (a named volume or a bind) is mounted into the
// helper instead, and the repository path is adjusted.
func (r *Runner) localMount(ctx context.Context) (root string, mounts []docker.VolumeMount, err error) {
	if r.BackupDir == "" {
		return "", nil, errors.New("this agent has no backup directory configured")
	}
	dir := filepath.ToSlash(filepath.Clean(r.BackupDir))
	self := r.Host.SelfContainer(ctx)
	if self == "" {
		// A bind mount needs its source to exist; the first backup creates it.
		mk := r.MkdirAll
		if mk == nil {
			mk = os.MkdirAll
		}
		if err := mk(filepath.Clean(r.BackupDir), 0o700); err != nil {
			return "", nil, fmt.Errorf("create the backup directory: %w", err)
		}
		return agentprotocol.HelperDestDir, []docker.VolumeMount{{HostPath: filepath.Clean(r.BackupDir), Target: agentprotocol.HelperDestDir}}, nil
	}
	info, err := r.Docker.ContainerInspect(ctx, self)
	if err != nil {
		return "", nil, fmt.Errorf("inspect the agent container: %w", err)
	}
	var best *docker.MountInfo
	for i := range info.Mounts {
		m := &info.Mounts[i]
		d := path.Clean(m.Destination)
		if dir != d && !strings.HasPrefix(dir, strings.TrimSuffix(d, "/")+"/") {
			continue
		}
		if best == nil || len(m.Destination) > len(best.Destination) {
			best = m
		}
	}
	if best == nil {
		return "", nil, fmt.Errorf("the agent's backup directory %s is not on a volume or bind mount of the agent container, so a backup helper cannot reach it; mount a volume there", dir)
	}
	rel := strings.TrimPrefix(dir, path.Clean(best.Destination))
	vm := docker.VolumeMount{Target: agentprotocol.HelperDestDir}
	switch best.Type {
	case "bind":
		vm.HostPath = best.Source
	case "volume":
		vm.Volume = best.Name
	default:
		return "", nil, fmt.Errorf("the agent's backup directory is on a %s mount, which a backup helper cannot reach", best.Type)
	}
	return path.Join(agentprotocol.HelperDestDir, rel), []docker.VolumeMount{vm}, nil
}

// ---- small utilities ----

// tailBuf keeps the last bytes written to it.
type tailBuf struct {
	mu  sync.Mutex
	buf []byte
}

const tailMax = 4096

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailMax {
		t.buf = t.buf[len(t.buf)-tailMax:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// scrub masks every secret value (length >= 4) in s.
func scrub(s string, secrets map[string]string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "••••••")
		}
	}
	return s
}

func nopLog(string) {}
