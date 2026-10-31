// Package cleanup prunes a host's Docker data on the control plane's command:
// stopped containers NebulaCtrl does not manage (opt-in), dangling and old
// unused images, build cache and unused NebulaCtrl networks. Volumes are never
// touched, except by the separate explicit volume mode.
//
// Rollback images are sacred. The control plane sends the set to keep (the
// current release of every service, the releases of active deployments and the
// newest imageRetention releases); the agent adds the pinned infrastructure
// images (agentprotocol.PinnedImages) and never removes an image in that set,
// whatever its age or use.
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ErrNoProtectedSet is returned for a command without the protected-image set.
// Pruning without it could delete rollback images, so the agent refuses.
var ErrNoProtectedSet = errors.New("the command carries no protected-image set; refusing to prune")

// StoppedContainerMinAge is how long an unmanaged container must have been
// stopped before the optional container step removes it: a container that just
// exited (a one-shot helper, a crash someone is looking at) is left alone.
const StoppedContainerMinAge = time.Hour

// Docker is the part of the daemon cleanup uses. *docker.SDKEngine implements
// it; tests use a fake.
type Docker interface {
	ContainerList(ctx context.Context, f docker.ListFilter) ([]docker.ContainerInfo, error)
	ContainerRemove(ctx context.Context, id string, force, removeVolumes bool) error
	ImageList(ctx context.Context, reference string) ([]docker.ImageSummary, error)
	ImageInspect(ctx context.Context, ref string) (docker.ImageInfo, error)
	ImageRemove(ctx context.Context, ref string, force bool) error
	// ImagesSize is the disk space all images occupy, shared layers once.
	ImagesSize(ctx context.Context) (int64, error)
	NetworkList(ctx context.Context, prefix string, labels map[string]string) ([]docker.NetworkInfo, error)
	NetworkRemove(ctx context.Context, name string) error
	VolumeList(ctx context.Context) ([]docker.VolumeInfo, error)
	RemoveVolume(ctx context.Context, name string, force bool) error
}

// Config wires a Runner.
type Config struct {
	Docker Docker
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// PruneBuildCache drops build cache older than the given age and returns
	// the space it freed; nil skips the build cache step.
	PruneBuildCache func(ctx context.Context, olderThan time.Duration) (int64, error)
	// IsManagedNetwork says which networks cleanup may remove when empty
	// (environment, internal and shared networks; never the edge network).
	IsManagedNetwork func(name string) bool
	// NetworkMinAge is how old an empty managed network must be (a deploy
	// creates its network a moment before the first container).
	NetworkMinAge time.Duration
	// Pinned adds image references to agentprotocol.PinnedImages (tests).
	Pinned []string
	// Busy reports that a build is running. Pruning images or build cache then
	// could remove layers or intermediate images the build is using, so those
	// steps are skipped (the next cleanup covers them). nil means never busy.
	Busy func() bool
}

// Runner executes cleanups.
type Runner struct {
	cfg Config
	log *slog.Logger
	now func() time.Time
}

// New returns a Runner.
func New(cfg Config) *Runner {
	r := &Runner{cfg: cfg, log: cfg.Logger, now: cfg.Now}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

// Failed reports whether any step of the result failed.
func Failed(res agentprotocol.CleanupResult) bool {
	for _, s := range res.Steps {
		if s.Error != "" {
			return true
		}
	}
	return false
}

// Run executes the command's cleanup. The error is only ErrNoProtectedSet (or
// an unknown mode); step failures are in the result, and the other steps still
// run.
func (r *Runner) Run(ctx context.Context, p agentprotocol.CleanupPayload) (agentprotocol.CleanupResult, error) {
	p.Normalize()
	res := agentprotocol.CleanupResult{RunID: p.RunID, Mode: p.Mode, Trigger: p.Trigger}
	if p.Protected == nil {
		return res, ErrNoProtectedSet
	}
	switch p.Mode {
	case agentprotocol.CleanupModeStandard:
		res.Steps = r.standard(ctx, p)
	case agentprotocol.CleanupModeVolumes:
		res.Steps = []agentprotocol.CleanupStep{r.volumes(ctx, p)}
	default:
		return res, fmt.Errorf("unknown cleanup mode %q", p.Mode)
	}
	for _, s := range res.Steps {
		res.ReclaimedBytes += s.ReclaimedBytes
		res.Items += s.Items
	}
	return res, nil
}

func (r *Runner) standard(ctx context.Context, p agentprotocol.CleanupPayload) []agentprotocol.CleanupStep {
	var steps []agentprotocol.CleanupStep
	// Containers first: an unmanaged container that goes frees its image for the
	// image step of this very run.
	steps = append(steps, r.containers(ctx, p))
	if r.cfg.Busy != nil && r.cfg.Busy() {
		const why = "A build is running; images and build cache are left alone so it cannot lose its layers. The next cleanup covers them."
		steps = append(steps,
			agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepDangling, Skipped: why},
			agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepUnusedImages, Skipped: why},
			agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepBuildCache, Skipped: why},
			r.networks(ctx))
		return steps
	}
	dangling, unused := r.images(ctx, p)
	steps = append(steps, dangling, unused, r.buildCache(ctx, p), r.networks(ctx))
	return steps
}

func isManaged(labels map[string]string) bool { return labels[agentprotocol.LabelManaged] == "true" }

// HelperLabel marks the short-lived helper containers NebulaCtrl starts itself
// (volume backups, restores); HostShellHelperPrefix names the host shell
// helpers. Cleanup never touches either, whatever their state.
const (
	HelperLabel           = "nebulactrl.helper"
	HostShellHelperPrefix = "nebulactrl-hostshell-"
)

func isHelper(c docker.ContainerInfo) bool {
	_, labelled := c.Labels[HelperLabel]
	return labelled || strings.HasPrefix(c.Name, HostShellHelperPrefix)
}

// containers removes stopped containers NebulaCtrl does not manage, when the
// switch is on. Their volumes stay.
func (r *Runner) containers(ctx context.Context, p agentprotocol.CleanupPayload) agentprotocol.CleanupStep {
	step := agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepContainers}
	if !p.RemoveStoppedUnmanaged {
		step.Skipped = "Stopped containers that NebulaCtrl does not manage are kept."
		return step
	}
	cs, err := r.cfg.Docker.ContainerList(ctx, docker.ListFilter{All: true})
	if err != nil {
		step.Error = "list containers: " + err.Error()
		return step
	}
	now := r.now()
	var errs []string
	for _, c := range cs {
		if isManaged(c.Labels) || isHelper(c) || c.Running || c.Restarting || c.State == "paused" || c.State == "removing" {
			continue
		}
		stopped := c.FinishedAt
		if stopped.IsZero() || stopped.Year() < 2000 {
			stopped = c.Created
		}
		if now.Sub(stopped) < StoppedContainerMinAge {
			continue
		}
		// Not forced, and never with its volumes.
		if err := r.cfg.Docker.ContainerRemove(ctx, c.ID, false, false); err != nil {
			if !errors.Is(err, docker.ErrNotFound) {
				errs = append(errs, fmt.Sprintf("remove container %s: %v", c.Name, err))
			}
			continue
		}
		step.Items++
		step.AddRemoved(c.Name)
		r.log.Info("cleanup: removed stopped container", "container", c.Name)
	}
	step.Error = joinErrs(errs)
	return step
}

// images removes dangling images and unused images older than the age limit,
// except protected ones. Both steps share one listing and one before/after
// size measurement.
func (r *Runner) images(ctx context.Context, p agentprotocol.CleanupPayload) (dangling, unused agentprotocol.CleanupStep) {
	dangling = agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepDangling}
	unused = agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepUnusedImages}
	eng := r.cfg.Docker
	fail := func(msg string) {
		dangling.Error, unused.Error = msg, msg
	}
	imgs, err := eng.ImageList(ctx, "")
	if err != nil {
		fail("list images: " + err.Error())
		return
	}
	cs, err := eng.ContainerList(ctx, docker.ListFilter{All: true})
	if err != nil {
		// Without the containers there is no telling which images are in use.
		fail("list containers: " + err.Error())
		return
	}
	inUse := map[string]bool{}
	for _, c := range cs {
		if c.ImageID != "" {
			inUse[c.ImageID] = true
		}
	}
	keep := newMatcher(append(append(append([]string{}, p.Protected.Images...), agentprotocol.PinnedImages()...), r.cfg.Pinned...), p.Protected.ImageIDs)
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].ID < imgs[j].ID })

	before, sizeErr := eng.ImagesSize(ctx)
	var danglingSize, unusedSize int64
	var dErrs, uErrs []string
	now := r.now()
	for _, img := range imgs {
		tags := realTags(img.RepoTags)
		step := &unused
		if len(tags) == 0 {
			step = &dangling
		}
		if keep.protects(img) {
			step.Protected++
			continue
		}
		if inUse[img.ID] {
			continue
		}
		errsOf := &uErrs
		if step == &dangling {
			errsOf = &dErrs
		}
		var name string
		if len(tags) == 0 {
			name = shortID(img.ID)
			if err := eng.ImageRemove(ctx, img.ID, false); err != nil {
				if !ignorable(err) {
					*errsOf = append(*errsOf, fmt.Sprintf("remove image %s: %v", name, err))
				}
				continue
			}
		} else {
			name = strings.Join(tags, ", ")
			if !r.oldEnough(ctx, img, p.UnusedImageMinAge(), now) {
				continue
			}
			removed := true
			for _, t := range tags {
				if err := eng.ImageRemove(ctx, t, false); err != nil {
					if !ignorable(err) {
						*errsOf = append(*errsOf, fmt.Sprintf("remove image %s: %v", t, err))
					}
					removed = false
					break
				}
			}
			if !removed {
				continue
			}
		}
		step.Items++
		step.AddRemoved(name)
		if step == &dangling {
			danglingSize += img.Size
		} else {
			unusedSize += img.Size
		}
		r.log.Info("cleanup: removed image", "image", name)
	}
	dangling.Error, unused.Error = joinErrs(dErrs), joinErrs(uErrs)

	// What the removals freed: the daemon's own before/after (shared layers are
	// freed only with their last image) split by the removed images' sizes, or
	// the plain sum when it cannot be measured.
	total := danglingSize + unusedSize
	if sizeErr == nil && total > 0 {
		if after, err := eng.ImagesSize(ctx); err == nil && before > after {
			total = before - after
		}
	}
	switch {
	case danglingSize+unusedSize == 0:
	case danglingSize == 0:
		unused.ReclaimedBytes = total
	case unusedSize == 0:
		dangling.ReclaimedBytes = total
	default:
		dangling.ReclaimedBytes = total * danglingSize / (danglingSize + unusedSize)
		unused.ReclaimedBytes = total - dangling.ReclaimedBytes
	}
	return dangling, unused
}

// oldEnough reports whether the image is older than minAge. An image is as old
// as the later of its build date and the time it was last tagged: a pull keeps
// the build date of the image, which can be months ago.
func (r *Runner) oldEnough(ctx context.Context, img docker.ImageSummary, minAge time.Duration, now time.Time) bool {
	ts := img.Created
	if info, err := r.cfg.Docker.ImageInspect(ctx, img.ID); err == nil {
		if info.Created.After(ts) {
			ts = info.Created
		}
		if info.LastTagged.After(ts) {
			ts = info.LastTagged
		}
	}
	return now.Sub(ts) >= minAge
}

// buildCache prunes build cache older than the limit.
func (r *Runner) buildCache(ctx context.Context, p agentprotocol.CleanupPayload) agentprotocol.CleanupStep {
	step := agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepBuildCache}
	if r.cfg.PruneBuildCache == nil {
		step.Skipped = "This agent cannot build, so it has no build cache."
		return step
	}
	n, err := r.cfg.PruneBuildCache(ctx, p.BuildCacheMinAge())
	step.ReclaimedBytes = n
	if err != nil {
		step.Error = "prune build cache: " + err.Error()
	}
	return step
}

// networks removes empty NebulaCtrl environment and shared networks.
func (r *Runner) networks(ctx context.Context) agentprotocol.CleanupStep {
	step := agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepNetworks}
	if r.cfg.IsManagedNetwork == nil {
		step.Skipped = "No managed networks on this agent."
		return step
	}
	nets, err := r.cfg.Docker.NetworkList(ctx, "", map[string]string{agentprotocol.LabelManaged: "true"})
	if err != nil {
		step.Error = "list networks: " + err.Error()
		return step
	}
	now := r.now()
	var errs []string
	for _, n := range nets {
		if !r.cfg.IsManagedNetwork(n.Name) || len(n.Containers) > 0 {
			continue
		}
		if !n.Created.IsZero() && now.Sub(n.Created) < r.cfg.NetworkMinAge {
			continue
		}
		if err := r.cfg.Docker.NetworkRemove(ctx, n.Name); err != nil {
			if !errors.Is(err, docker.ErrNotFound) {
				errs = append(errs, fmt.Sprintf("remove network %s: %v", n.Name, err))
			}
			continue
		}
		step.Items++
		step.AddRemoved(n.Name)
		r.log.Info("cleanup: removed unused network", "network", n.Name)
	}
	step.Error = joinErrs(errs)
	return step
}

// volumes removes unused volumes NebulaCtrl does not manage. It runs only in
// CleanupModeVolumes. A volume survives when any container (running or
// stopped) mounts it, when it is a NebulaCtrl volume (label or name prefix),
// or when the control plane lists it as a service's volume.
func (r *Runner) volumes(ctx context.Context, p agentprotocol.CleanupPayload) agentprotocol.CleanupStep {
	step := agentprotocol.CleanupStep{Name: agentprotocol.CleanupStepVolumes}
	vols, err := r.cfg.Docker.VolumeList(ctx)
	if err != nil {
		step.Error = "list volumes: " + err.Error()
		return step
	}
	protected := map[string]bool{}
	for _, v := range p.Protected.Volumes {
		protected[v] = true
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	var errs []string
	for _, v := range vols {
		if v.RefCount != 0 || protected[v.Name] || isManaged(v.Labels) || strings.HasPrefix(v.Name, agentprotocol.VolumePrefix) {
			if v.RefCount == 0 {
				step.Protected++
			}
			continue
		}
		if err := r.cfg.Docker.RemoveVolume(ctx, v.Name, false); err != nil {
			if !errors.Is(err, docker.ErrNotFound) && !ignorable(err) {
				errs = append(errs, fmt.Sprintf("remove volume %s: %v", v.Name, err))
			}
			continue
		}
		step.Items++
		step.AddRemoved(v.Name)
		if v.Size > 0 {
			step.ReclaimedBytes += v.Size
		}
		r.log.Info("cleanup: removed unused volume", "volume", v.Name)
	}
	step.Error = joinErrs(errs)
	return step
}

// ignorable reports a removal that failed because the thing is gone or in use
// again: a race with a deploy, not a failure of the cleanup.
func ignorable(err error) bool {
	if errors.Is(err, docker.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "being used") || strings.Contains(msg, "in use") ||
		strings.Contains(msg, "must be forced") || strings.Contains(msg, "conflict")
}

func joinErrs(errs []string) string {
	switch len(errs) {
	case 0:
		return ""
	case 1:
		return errs[0]
	}
	return fmt.Sprintf("%s (and %d more)", errs[0], len(errs)-1)
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}
