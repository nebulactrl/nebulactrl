package agentprotocol

import (
	"sort"
	"sync"
	"time"
)

// KindDockerCleanup prunes a host's Docker data: dangling and old unused
// images, build cache, optionally stopped containers NebulaCtrl does not
// manage, and unused NebulaCtrl networks. It never touches volumes, except in
// CleanupModeVolumes, the separate explicit action.
const KindDockerCleanup = "docker_cleanup"

func init() { AllowedKinds[KindDockerCleanup] = true }

// CapDockerCleanup is advertised in Facts.Capabilities by agents that run
// docker_cleanup commands. A server that has never connected has no facts yet
// and is assumed to enroll with a current agent.
const CapDockerCleanup = "docker.cleanup"

// Cleanup modes (CleanupPayload.Mode).
const (
	// CleanupModeStandard is the scheduled and "Run cleanup now" cleanup.
	CleanupModeStandard = "standard"
	// CleanupModeVolumes removes unused volumes that NebulaCtrl does not manage
	// and nothing else. It is only ever started by an explicit, confirmed
	// action; the schedule never sends it.
	CleanupModeVolumes = "volumes"
)

// Cleanup triggers (CleanupPayload.Trigger).
const (
	CleanupTriggerScheduled = "scheduled"
	CleanupTriggerManual    = "manual"
	CleanupTriggerEmergency = "emergency"
)

// Cleanup defaults and limits shared by the control plane and the agent.
const (
	// DefaultUnusedImageMinAgeHours is how old an unused, unprotected image
	// must be before cleanup removes it.
	DefaultUnusedImageMinAgeHours = 72
	// DefaultBuildCacheMinAgeHours is how old build cache must be to be pruned.
	DefaultBuildCacheMinAgeHours = 168
	// CleanupMaxRemovedNames caps CleanupStep.Removed: the report names what
	// went, but a host with thousands of images must not produce a huge result.
	CleanupMaxRemovedNames = 25
)

// CleanupProtected is what a cleanup must never delete. The control plane
// computes it when it sends the command, from the releases it knows: the
// current release of every service on the server, the releases of its active
// deployments and the newest imageRetention succeeded releases (the ones a
// rollback may pick). The agent adds the pinned infrastructure images itself
// (PinnedImages).
type CleanupProtected struct {
	// Images are image references: "repo:tag", "repo@sha256:…" or
	// "repo:tag@sha256:…". A reference with a digest matches by digest.
	Images []string `json:"images"`
	// ImageIDs are local image IDs ("sha256:…") of built images.
	ImageIDs []string `json:"imageIds"`
	// Volumes are volume names the explicit volume cleanup must keep (every
	// volume NebulaCtrl knows a service uses).
	Volumes []string `json:"volumes,omitempty"`
}

// CleanupPayload is the persisted payload of a docker_cleanup command. It
// carries no secrets.
type CleanupPayload struct {
	// RunID is the control plane's record of this run.
	RunID   string `json:"runId"`
	Mode    string `json:"mode"`
	Trigger string `json:"trigger"`

	// UnusedImageMinAgeHours and BuildCacheMinAgeHours default to
	// DefaultUnusedImageMinAgeHours and DefaultBuildCacheMinAgeHours when 0.
	UnusedImageMinAgeHours int `json:"unusedImageMinAgeHours,omitempty"`
	BuildCacheMinAgeHours  int `json:"buildCacheMinAgeHours,omitempty"`
	// RemoveStoppedUnmanaged also removes stopped containers NebulaCtrl does
	// not manage (default off).
	RemoveStoppedUnmanaged bool `json:"removeStoppedUnmanaged"`

	// Protected is required in every mode. A command without it is refused:
	// pruning with an empty set would delete rollback images.
	Protected *CleanupProtected `json:"protected"`
}

// Cleanup step names (CleanupStep.Name).
const (
	CleanupStepContainers   = "containers_unmanaged"
	CleanupStepDangling     = "images_dangling"
	CleanupStepUnusedImages = "images_unused"
	CleanupStepBuildCache   = "build_cache"
	CleanupStepNetworks     = "networks"
	CleanupStepVolumes      = "volumes"
)

// CleanupStep is the outcome of one prune step.
type CleanupStep struct {
	Name string `json:"name"`
	// Items is how many things the step removed.
	Items int `json:"items"`
	// ReclaimedBytes is the disk space the step freed (best effort: shared
	// image layers are counted once, containers are not measured).
	ReclaimedBytes int64 `json:"reclaimedBytes"`
	// Protected is how many candidates the step kept because a release needs
	// them (images only).
	Protected int `json:"protected,omitempty"`
	// Removed names up to CleanupMaxRemovedNames of the removed things.
	Removed []string `json:"removed,omitempty"`
	// Error is why the step did not finish; the others still ran.
	Error string `json:"error,omitempty"`
	// Skipped says why the step did not run (it was switched off).
	Skipped string `json:"skipped,omitempty"`
}

// CleanupResult is ResultFrame.Data of a docker_cleanup command. The result
// outcome is "failed" when any step failed; the report still lists what the
// other steps freed.
type CleanupResult struct {
	RunID          string        `json:"runId"`
	Mode           string        `json:"mode"`
	Trigger        string        `json:"trigger"`
	ReclaimedBytes int64         `json:"reclaimedBytes"`
	Items          int           `json:"items"`
	Steps          []CleanupStep `json:"steps"`
	Message        string        `json:"message,omitempty"`
}

// Normalize fills the defaults.
func (p *CleanupPayload) Normalize() {
	if p.Mode == "" {
		p.Mode = CleanupModeStandard
	}
	if p.Trigger == "" {
		p.Trigger = CleanupTriggerManual
	}
	if p.UnusedImageMinAgeHours <= 0 {
		p.UnusedImageMinAgeHours = DefaultUnusedImageMinAgeHours
	}
	if p.BuildCacheMinAgeHours <= 0 {
		p.BuildCacheMinAgeHours = DefaultBuildCacheMinAgeHours
	}
}

// AddRemoved records the name of a removed thing, up to CleanupMaxRemovedNames.
func (s *CleanupStep) AddRemoved(name string) {
	if len(s.Removed) < CleanupMaxRemovedNames {
		s.Removed = append(s.Removed, name)
	}
}

// UnusedImageMinAge is UnusedImageMinAgeHours as a duration.
func (p CleanupPayload) UnusedImageMinAge() time.Duration {
	return time.Duration(p.UnusedImageMinAgeHours) * time.Hour
}

// BuildCacheMinAge is BuildCacheMinAgeHours as a duration.
func (p CleanupPayload) BuildCacheMinAge() time.Duration {
	return time.Duration(p.BuildCacheMinAgeHours) * time.Hour
}

// pinned is the registry of infrastructure images cleanup never removes.
var pinned struct {
	mu   sync.Mutex
	refs map[string]struct{}
}

// PinImage registers an image reference the agent itself runs or pulls (the
// proxy, pages server, connectors, builders, helper containers) so cleanup
// keeps it even when no container uses it right now. Call it from a package
// level `var` or `init` next to the constant naming the image; it returns ref
// so it can wrap the constant.
func PinImage(ref string) string {
	pinned.mu.Lock()
	defer pinned.mu.Unlock()
	if pinned.refs == nil {
		pinned.refs = map[string]struct{}{}
	}
	pinned.refs[ref] = struct{}{}
	return ref
}

// PinnedImages returns the registered infrastructure image references, sorted.
func PinnedImages() []string {
	pinned.mu.Lock()
	defer pinned.mu.Unlock()
	out := make([]string, 0, len(pinned.refs))
	for r := range pinned.refs {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
