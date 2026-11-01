package cleanup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var t0 = time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)

const (
	mb   = int64(1_000_000)
	hour = time.Hour
	day  = 24 * time.Hour
)

// fakeImage is an image in the fake daemon.
type fakeImage struct {
	docker.ImageSummary
	lastTagged time.Time
}

// fakeDocker is an in-memory daemon implementing Docker.
type fakeDocker struct {
	mu         sync.Mutex
	images     map[string]*fakeImage // by ID
	containers map[string]*docker.ContainerInfo
	networks   map[string]docker.NetworkInfo
	volumes    map[string]docker.VolumeInfo

	removedImages          []string // refs passed to ImageRemove
	removedContainers      []string
	removedVolumes         []string
	removedNetworks        []string
	containerRemoveVolumes bool // a ContainerRemove asked for removeVolumes
	imageErr               map[string]error
	listErr                error
}

func newFake() *fakeDocker {
	return &fakeDocker{
		images: map[string]*fakeImage{}, containers: map[string]*docker.ContainerInfo{},
		networks: map[string]docker.NetworkInfo{}, volumes: map[string]docker.VolumeInfo{},
		imageErr: map[string]error{},
	}
}

func (f *fakeDocker) addImage(id string, tags []string, digests []string, age time.Duration, size int64) *fakeImage {
	img := &fakeImage{ImageSummary: docker.ImageSummary{ID: id, RepoTags: tags, RepoDigests: digests, Created: t0.Add(-age), Size: size}}
	f.images[id] = img
	return img
}

func (f *fakeDocker) addContainer(c docker.ContainerInfo) { f.containers[c.ID] = &c }

func (f *fakeDocker) ContainerList(context.Context, docker.ListFilter) ([]docker.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []docker.ContainerInfo
	for _, c := range f.containers {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _, removeVolumes bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("%w: container %s", docker.ErrNotFound, id)
	}
	if removeVolumes {
		f.containerRemoveVolumes = true
	}
	f.removedContainers = append(f.removedContainers, c.Name)
	delete(f.containers, id)
	return nil
}

func (f *fakeDocker) ImageList(context.Context, string) ([]docker.ImageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.ImageSummary
	for _, i := range f.images {
		out = append(out, i.ImageSummary)
	}
	return out, nil
}

func (f *fakeDocker) ImageInspect(_ context.Context, ref string) (docker.ImageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	img := f.byRef(ref)
	if img == nil {
		return docker.ImageInfo{}, fmt.Errorf("%w: image %s", docker.ErrNotFound, ref)
	}
	return docker.ImageInfo{ID: img.ID, RepoTags: img.RepoTags, Created: img.Created, LastTagged: img.lastTagged}, nil
}

func (f *fakeDocker) byRef(ref string) *fakeImage {
	if i, ok := f.images[ref]; ok {
		return i
	}
	for _, i := range f.images {
		if slices.Contains(i.RepoTags, ref) {
			return i
		}
	}
	return nil
}

// ImageRemove untags ref; the image goes with its last tag (or when removed by ID).
func (f *fakeDocker) ImageRemove(_ context.Context, ref string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedImages = append(f.removedImages, ref)
	if err := f.imageErr[ref]; err != nil {
		return err
	}
	img := f.byRef(ref)
	if img == nil {
		return fmt.Errorf("%w: image %s", docker.ErrNotFound, ref)
	}
	if ref == img.ID {
		delete(f.images, img.ID)
		return nil
	}
	img.RepoTags = slices.DeleteFunc(slices.Clone(img.RepoTags), func(t string) bool { return t == ref })
	if len(img.RepoTags) == 0 {
		delete(f.images, img.ID)
	}
	return nil
}

func (f *fakeDocker) ImagesSize(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, i := range f.images {
		n += i.Size
	}
	return n, nil
}

func (f *fakeDocker) NetworkList(context.Context, string, map[string]string) ([]docker.NetworkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.NetworkInfo
	for _, n := range f.networks {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedNetworks = append(f.removedNetworks, name)
	delete(f.networks, name)
	return nil
}

func (f *fakeDocker) VolumeList(context.Context) ([]docker.VolumeInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.VolumeInfo
	for _, v := range f.volumes {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeDocker) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedVolumes = append(f.removedVolumes, name)
	delete(f.volumes, name)
	return nil
}

func (f *fakeDocker) hasImage(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.images[id]
	return ok
}

func (f *fakeDocker) hasContainer(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.containers[id]
	return ok
}

func runner(f *fakeDocker, mut ...func(*Config)) *Runner {
	cfg := Config{
		Docker: f, Now: func() time.Time { return t0 },
		IsManagedNetwork: func(n string) bool { return len(n) > 15 && n[:15] == "nebulactrl-env-" },
		NetworkMinAge:    time.Minute,
	}
	for _, m := range mut {
		m(&cfg)
	}
	return New(cfg)
}

func payload(mut ...func(*agentprotocol.CleanupPayload)) agentprotocol.CleanupPayload {
	p := agentprotocol.CleanupPayload{
		RunID: "run-1", Mode: agentprotocol.CleanupModeStandard, Trigger: agentprotocol.CleanupTriggerScheduled,
		Protected: &agentprotocol.CleanupProtected{},
	}
	for _, m := range mut {
		m(&p)
	}
	return p
}

func step(t *testing.T, res agentprotocol.CleanupResult, name string) agentprotocol.CleanupStep {
	t.Helper()
	for _, s := range res.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step %q in %+v", name, res.Steps)
	return agentprotocol.CleanupStep{}
}

func TestRefusesWithoutProtectedSet(t *testing.T) {
	f := newFake()
	f.addImage("sha256:old", []string{"nc-build/web:abc"}, nil, 30*day, 10*mb)
	_, err := runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) { p.Protected = nil }))
	require.ErrorIs(t, err, ErrNoProtectedSet)
	require.True(t, f.hasImage("sha256:old"), "nothing may be pruned without the protected set")
	require.Empty(t, f.removedImages)
}

func TestProtectedImagesSurvive(t *testing.T) {
	f := newFake()
	// A built image of a rollback-eligible release: old, unused, named by ID.
	f.addImage("sha256:rollback-git", []string{"nc-build/web:aaaa1111"}, nil, 40*day, 80*mb)
	// An image-source release by tag (current release, old build date).
	f.addImage("sha256:cur-pg", []string{"postgres:17"}, []string{"postgres@sha256:pg17"}, 90*day, 400*mb)
	// An older release of the same service: its tag moved on, the image is
	// dangling but its digest is a release's.
	f.addImage("sha256:old-pg", nil, []string{"postgres@sha256:pg16"}, 120*day, 390*mb)
	// An image-source release recorded by fully qualified reference.
	f.addImage("sha256:redis", []string{"redis:7"}, []string{"redis@sha256:redis7"}, 60*day, 120*mb)
	// A release whose recorded identity is the local image ID (digest fallback).
	f.addImage("sha256:byid", []string{"internal.example/app:1"}, nil, 60*day, 50*mb)
	// A pinned infrastructure image nothing uses right now.
	f.addImage("sha256:traefik", []string{"traefik:v3.7.16"}, []string{"traefik@sha256:tr"}, 100*day, 200*mb)
	// Not protected: must go.
	f.addImage("sha256:junk-tagged", []string{"alpine:3.10"}, nil, 200*day, 7*mb)
	f.addImage("sha256:junk-dangling", nil, nil, 5*day, 30*mb)

	res, err := runner(f, func(c *Config) { c.Pinned = []string{"traefik:v3.7.16@sha256:tr"} }).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) {
		p.Protected = &agentprotocol.CleanupProtected{
			ImageIDs: []string{"sha256:rollback-git"},
			Images: []string{
				"postgres:17@sha256:pg17",
				"postgres@sha256:pg16",
				"docker.io/library/redis:7",
				"sha256:byid",
			},
		}
	}))
	require.NoError(t, err)

	for _, id := range []string{"sha256:rollback-git", "sha256:cur-pg", "sha256:old-pg", "sha256:redis", "sha256:byid", "sha256:traefik"} {
		require.True(t, f.hasImage(id), "%s is protected and must survive", id)
	}
	require.False(t, f.hasImage("sha256:junk-tagged"))
	require.False(t, f.hasImage("sha256:junk-dangling"))

	require.Equal(t, 1, step(t, res, agentprotocol.CleanupStepDangling).Items)
	require.Equal(t, 1, step(t, res, agentprotocol.CleanupStepUnusedImages).Items)
	// Kept because a release (or the infrastructure) needs them.
	require.Equal(t, 1, step(t, res, agentprotocol.CleanupStepDangling).Protected)
	require.Equal(t, 5, step(t, res, agentprotocol.CleanupStepUnusedImages).Protected)
}

func TestProtectedImageSurvivesEvenWhenForced(t *testing.T) {
	// A protected image is not removed by any route: not by tag, not by ID.
	f := newFake()
	f.addImage("sha256:keep", []string{"nc-build/api:1", "nc-build/api:2"}, nil, 365*day, mb)
	_, err := runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) {
		p.Protected = &agentprotocol.CleanupProtected{ImageIDs: []string{"sha256:keep"}}
	}))
	require.NoError(t, err)
	require.Empty(t, f.removedImages)
}

func TestUnusedImageAgeThreshold(t *testing.T) {
	f := newFake()
	f.addImage("sha256:old", []string{"a:1"}, nil, 73*hour, 10*mb)
	f.addImage("sha256:young", []string{"b:1"}, nil, 71*hour, 10*mb)
	// Old build date but pulled 30 minutes ago: as young as its pull.
	pulled := f.addImage("sha256:pulled", []string{"c:1"}, nil, 400*day, 10*mb)
	pulled.lastTagged = t0.Add(-30 * time.Minute)
	// Old build date, pulled long ago too.
	stale := f.addImage("sha256:stale", []string{"d:1"}, nil, 400*day, 10*mb)
	stale.lastTagged = t0.Add(-100 * day)

	res, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.False(t, f.hasImage("sha256:old"))
	require.True(t, f.hasImage("sha256:young"), "71 h is under the 72 h limit")
	require.True(t, f.hasImage("sha256:pulled"), "a recent pull makes an image young however old its build is")
	require.False(t, f.hasImage("sha256:stale"))
	require.Equal(t, 2, step(t, res, agentprotocol.CleanupStepUnusedImages).Items)

	// The limit comes from the command.
	res, err = runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) { p.UnusedImageMinAgeHours = 1 }))
	require.NoError(t, err)
	require.False(t, f.hasImage("sha256:young"))
	require.True(t, f.hasImage("sha256:pulled"), "pulled 30 min ago is under the 1 h limit")
	require.Equal(t, 1, step(t, res, agentprotocol.CleanupStepUnusedImages).Items)
}

func TestDanglingImagesGoWithoutAgeLimit(t *testing.T) {
	f := newFake()
	f.addImage("sha256:fresh-dangling", nil, nil, time.Minute, 3*mb)
	f.addImage("sha256:placeholder", []string{"<none>:<none>"}, nil, time.Minute, 3*mb)
	res, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.False(t, f.hasImage("sha256:fresh-dangling"))
	require.False(t, f.hasImage("sha256:placeholder"))
	require.Equal(t, 2, step(t, res, agentprotocol.CleanupStepDangling).Items)
}

func TestImagesInUseAreNeverRemoved(t *testing.T) {
	f := newFake()
	f.addImage("sha256:running", []string{"x:1"}, nil, 500*day, 10*mb)
	f.addImage("sha256:stopped", nil, nil, 500*day, 10*mb) // dangling, used by a stopped container
	f.addImage("sha256:free", []string{"y:1"}, nil, 500*day, 10*mb)
	f.addContainer(docker.ContainerInfo{ID: "c1", Name: "a", ImageID: "sha256:running", Running: true, State: "running"})
	f.addContainer(docker.ContainerInfo{ID: "c2", Name: "b", ImageID: "sha256:stopped", State: "exited", FinishedAt: t0.Add(-10 * day)})
	_, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.True(t, f.hasImage("sha256:running"))
	require.True(t, f.hasImage("sha256:stopped"))
	require.False(t, f.hasImage("sha256:free"))
}

func TestUnmanagedContainersUntouchedByDefault(t *testing.T) {
	f := newFake()
	f.addContainer(docker.ContainerInfo{ID: "u1", Name: "scratch", State: "exited", FinishedAt: t0.Add(-30 * day)})
	res, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.True(t, f.hasContainer("u1"))
	require.Empty(t, f.removedContainers)
	s := step(t, res, agentprotocol.CleanupStepContainers)
	require.Zero(t, s.Items)
	require.NotEmpty(t, s.Skipped)
}

func TestUnmanagedContainersRemovedWhenSwitchedOn(t *testing.T) {
	f := newFake()
	managed := map[string]string{agentprotocol.LabelManaged: "true"}
	f.addContainer(docker.ContainerInfo{ID: "old", Name: "old-stopped", State: "exited", FinishedAt: t0.Add(-30 * day)})
	f.addContainer(docker.ContainerInfo{ID: "recent", Name: "just-exited", State: "exited", FinishedAt: t0.Add(-10 * time.Minute)})
	f.addContainer(docker.ContainerInfo{ID: "running", Name: "svc", State: "running", Running: true})
	f.addContainer(docker.ContainerInfo{ID: "paused", Name: "frozen", State: "paused"})
	f.addContainer(docker.ContainerInfo{ID: "managed", Name: "nc-web-1", State: "exited", Labels: managed, FinishedAt: t0.Add(-30 * day)})
	f.addContainer(docker.ContainerInfo{ID: "never", Name: "created-only", State: "created", Created: t0.Add(-2 * day)})
	f.addImage("sha256:img", []string{"scratch:1"}, nil, 300*day, 12*mb)
	f.containers["old"].ImageID = "sha256:img"

	res, err := runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) { p.RemoveStoppedUnmanaged = true }))
	require.NoError(t, err)
	require.False(t, f.hasContainer("old"))
	require.False(t, f.hasContainer("never"))
	for _, id := range []string{"recent", "running", "paused", "managed"} {
		require.True(t, f.hasContainer(id), "%s must stay", id)
	}
	s := step(t, res, agentprotocol.CleanupStepContainers)
	require.Equal(t, 2, s.Items)
	require.ElementsMatch(t, []string{"old-stopped", "created-only"}, s.Removed)
	require.False(t, f.containerRemoveVolumes, "a container is never removed with its volumes")
	// Its image became free during the same run.
	require.False(t, f.hasImage("sha256:img"))
}

func TestHelperContainersAreNeverTouched(t *testing.T) {
	f := newFake()
	old := t0.Add(-30 * day)
	f.addContainer(docker.ContainerInfo{ID: "h1", Name: "restic-run", State: "exited", FinishedAt: old, Labels: map[string]string{HelperLabel: "volume-backup"}})
	f.addContainer(docker.ContainerInfo{ID: "h2", Name: "nebulactrl-hostshell-abc", State: "exited", FinishedAt: old})
	f.addContainer(docker.ContainerInfo{ID: "h3", Name: "nebulactrl-hostshell-def", State: "running", Running: true})
	_, err := runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) { p.RemoveStoppedUnmanaged = true }))
	require.NoError(t, err)
	require.Empty(t, f.removedContainers)
}

func TestVolumesAreNeverTouchedBySchedule(t *testing.T) {
	f := newFake()
	f.volumes["orphan"] = docker.VolumeInfo{Name: "orphan", RefCount: 0, Size: 100 * mb}
	f.volumes["nc-vol-12345678-data"] = docker.VolumeInfo{Name: "nc-vol-12345678-data", RefCount: 0, Size: 900 * mb}
	f.addContainer(docker.ContainerInfo{ID: "u1", Name: "scratch", State: "exited", FinishedAt: t0.Add(-30 * day)})
	for _, trig := range []string{agentprotocol.CleanupTriggerScheduled, agentprotocol.CleanupTriggerEmergency, agentprotocol.CleanupTriggerManual} {
		_, err := runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) {
			p.Trigger, p.RemoveStoppedUnmanaged = trig, true
		}))
		require.NoError(t, err)
	}
	require.Empty(t, f.removedVolumes)
	require.Len(t, f.volumes, 2)
	require.False(t, f.containerRemoveVolumes)
}

func TestVolumeModeRemovesOnlyUnmanagedUnusedVolumes(t *testing.T) {
	f := newFake()
	managed := map[string]string{agentprotocol.LabelManaged: "true"}
	f.volumes["anon"] = docker.VolumeInfo{Name: "anon", RefCount: 0, Size: 100 * mb}
	f.volumes["in-use"] = docker.VolumeInfo{Name: "in-use", RefCount: 1, Size: 50 * mb}
	f.volumes["unknown"] = docker.VolumeInfo{Name: "unknown", RefCount: -1}
	f.volumes["nc-vol-12345678-data"] = docker.VolumeInfo{Name: "nc-vol-12345678-data", RefCount: 0, Size: 900 * mb}
	f.volumes["labelled"] = docker.VolumeInfo{Name: "labelled", RefCount: 0, Labels: managed, Size: 10 * mb}
	f.volumes["listed"] = docker.VolumeInfo{Name: "listed", RefCount: 0, Size: 20 * mb}
	f.addImage("sha256:old", []string{"a:1"}, nil, 300*day, mb)

	res, err := runner(f).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) {
		p.Mode = agentprotocol.CleanupModeVolumes
		p.Protected = &agentprotocol.CleanupProtected{Volumes: []string{"listed"}}
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"anon"}, f.removedVolumes)
	require.True(t, f.hasImage("sha256:old"), "the volume action prunes nothing else")
	require.Len(t, res.Steps, 1)
	s := step(t, res, agentprotocol.CleanupStepVolumes)
	require.Equal(t, 1, s.Items)
	require.Equal(t, 100*mb, s.ReclaimedBytes)
}

func TestNetworksOnlyEmptyManagedAndOldEnough(t *testing.T) {
	f := newFake()
	f.networks["nebulactrl-env-aaaaaaaa"] = docker.NetworkInfo{Name: "nebulactrl-env-aaaaaaaa", Created: t0.Add(-time.Hour)}
	f.networks["nebulactrl-env-bbbbbbbb"] = docker.NetworkInfo{Name: "nebulactrl-env-bbbbbbbb", Created: t0.Add(-time.Hour), Containers: []string{"c1"}}
	f.networks["nebulactrl-env-cccccccc"] = docker.NetworkInfo{Name: "nebulactrl-env-cccccccc", Created: t0.Add(-5 * time.Second)}
	f.networks["nebulactrl-edge"] = docker.NetworkInfo{Name: "nebulactrl-edge", Created: t0.Add(-100 * day)}
	res, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.Equal(t, []string{"nebulactrl-env-aaaaaaaa"}, f.removedNetworks)
	require.Equal(t, 1, step(t, res, agentprotocol.CleanupStepNetworks).Items)
}

func TestBuildCacheStep(t *testing.T) {
	f := newFake()
	var gotAge time.Duration
	res, err := runner(f, func(c *Config) {
		c.PruneBuildCache = func(_ context.Context, older time.Duration) (int64, error) {
			gotAge = older
			return 321 * mb, nil
		}
	}).Run(context.Background(), payload())
	require.NoError(t, err)
	require.Equal(t, 7*day, gotAge, "build cache older than 7 days")
	s := step(t, res, agentprotocol.CleanupStepBuildCache)
	require.Equal(t, 321*mb, s.ReclaimedBytes)
	require.Equal(t, 321*mb, res.ReclaimedBytes)

	res, err = runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.NotEmpty(t, step(t, res, agentprotocol.CleanupStepBuildCache).Skipped)
}

func TestRunningBuildKeepsImagesAndBuildCacheAlone(t *testing.T) {
	f := newFake()
	f.addImage("sha256:dangling", nil, nil, day, 30*mb)
	f.addImage("sha256:old", []string{"old:1"}, nil, 100*day, 40*mb)
	pruned := false
	busy := true
	cfg := func(c *Config) {
		c.Busy = func() bool { return busy }
		c.PruneBuildCache = func(context.Context, time.Duration) (int64, error) { pruned = true; return 1, nil }
	}
	res, err := runner(f, cfg).Run(context.Background(), payload())
	require.NoError(t, err)
	require.False(t, pruned, "no build cache prune while a build runs")
	require.True(t, f.hasImage("sha256:dangling"))
	require.True(t, f.hasImage("sha256:old"))
	for _, name := range []string{agentprotocol.CleanupStepDangling, agentprotocol.CleanupStepUnusedImages, agentprotocol.CleanupStepBuildCache} {
		s := step(t, res, name)
		require.Contains(t, s.Skipped, "A build is running", name)
		require.Zero(t, s.Items)
	}

	busy = false
	_, err = runner(f, cfg).Run(context.Background(), payload())
	require.NoError(t, err)
	require.True(t, pruned, "the next cleanup prunes again")
	require.False(t, f.hasImage("sha256:dangling"))
}

func TestReportTotalsAndFailureIsolation(t *testing.T) {
	f := newFake()
	f.addImage("sha256:d1", nil, nil, day, 30*mb)
	f.addImage("sha256:d2", nil, nil, day, 20*mb)
	f.addImage("sha256:u1", []string{"a:1", "a:latest"}, nil, 100*day, 100*mb)
	f.addImage("sha256:locked", []string{"locked:1"}, nil, 100*day, 70*mb)
	f.imageErr["locked:1"] = errors.New("boom: daemon exploded")
	f.networks["nebulactrl-env-dddddddd"] = docker.NetworkInfo{Name: "nebulactrl-env-dddddddd", Created: t0.Add(-time.Hour)}

	res, err := runner(f, func(c *Config) {
		c.PruneBuildCache = func(context.Context, time.Duration) (int64, error) { return 50 * mb, errors.New("buildx gone") }
	}).Run(context.Background(), payload(func(p *agentprotocol.CleanupPayload) {
		p.RunID, p.Trigger = "run-42", agentprotocol.CleanupTriggerEmergency
	}))
	require.NoError(t, err)

	require.Equal(t, "run-42", res.RunID)
	require.Equal(t, agentprotocol.CleanupModeStandard, res.Mode)
	require.Equal(t, agentprotocol.CleanupTriggerEmergency, res.Trigger)

	d := step(t, res, agentprotocol.CleanupStepDangling)
	require.Equal(t, 2, d.Items)
	require.Equal(t, 50*mb, d.ReclaimedBytes)
	require.ElementsMatch(t, []string{"d1", "d2"}, d.Removed)

	u := step(t, res, agentprotocol.CleanupStepUnusedImages)
	require.Equal(t, 1, u.Items, "an image with two tags counts once")
	require.Equal(t, 100*mb, u.ReclaimedBytes)
	require.Equal(t, []string{"a:1, a:latest"}, u.Removed)
	require.Contains(t, u.Error, "locked:1", "the failing image is reported")
	require.True(t, f.hasImage("sha256:locked"))

	bc := step(t, res, agentprotocol.CleanupStepBuildCache)
	require.Equal(t, 50*mb, bc.ReclaimedBytes, "what the failed step did free is still counted")
	require.Contains(t, bc.Error, "buildx gone")

	n := step(t, res, agentprotocol.CleanupStepNetworks)
	require.Equal(t, 1, n.Items, "later steps run after an earlier one failed")

	require.Equal(t, 50*mb+100*mb+50*mb, res.ReclaimedBytes)
	require.Equal(t, 2+1+1, res.Items)
	require.True(t, Failed(res))

	// The removed names are capped.
	f2 := newFake()
	for i := range 40 {
		f2.addImage(fmt.Sprintf("sha256:%02d", i), nil, nil, day, mb)
	}
	res, err = runner(f2).Run(context.Background(), payload())
	require.NoError(t, err)
	d = step(t, res, agentprotocol.CleanupStepDangling)
	require.Equal(t, 40, d.Items)
	require.Len(t, d.Removed, agentprotocol.CleanupMaxRemovedNames)
	require.False(t, Failed(res))
}

func TestImageInUseRaceIsNotAFailure(t *testing.T) {
	f := newFake()
	f.addImage("sha256:race", []string{"r:1"}, nil, 100*day, mb)
	f.imageErr["r:1"] = errors.New("conflict: unable to delete (cannot be forced) - image is being used by running container abc")
	res, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.False(t, Failed(res))
	require.Zero(t, res.Items)
}

func TestListFailureReportsAndContinues(t *testing.T) {
	f := newFake()
	f.addImage("sha256:old", []string{"a:1"}, nil, 100*day, mb)
	f.listErr = errors.New("daemon busy")
	res, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.True(t, f.hasImage("sha256:old"), "without the container list nothing is removed")
	require.Contains(t, step(t, res, agentprotocol.CleanupStepUnusedImages).Error, "daemon busy")
	require.True(t, Failed(res))
}

func TestPinnedInfrastructureImagesAreRegistered(t *testing.T) {
	pinned := agentprotocol.PinnedImages()
	require.NotEmpty(t, pinned)
	var traefik, buildkit bool
	for _, p := range pinned {
		traefik = traefik || len(p) > 7 && p[:7] == "traefik"
		buildkit = buildkit || len(p) > 12 && p[:12] == "moby/buildki"
	}
	require.True(t, traefik, "Traefik is pinned")
	require.True(t, buildkit, "BuildKit is pinned")

	// And they are honoured without any help from the control plane.
	f := newFake()
	f.addImage("sha256:traefik", []string{"traefik:v3.7.16"}, []string{"traefik@sha256:c8a121e4bb1347e8fd52657fab12f2cb70b68f8a018895e2068f92c9e519b9c2"}, 300*day, 200*mb)
	_, err := runner(f).Run(context.Background(), payload())
	require.NoError(t, err)
	require.True(t, f.hasImage("sha256:traefik"))
}

func TestMatcher(t *testing.T) {
	m := newMatcher([]string{"nginx", "ghcr.io/o/r:1@sha256:aa", "repo@sha256:bb", "sha256:id1"}, []string{"sha256:id2"})
	cases := []struct {
		name string
		img  docker.ImageSummary
		want bool
	}{
		{"implicit latest", docker.ImageSummary{RepoTags: []string{"nginx:latest"}}, true},
		{"other tag of a protected repo", docker.ImageSummary{RepoTags: []string{"nginx:1.27"}}, false},
		{"tag of a ref with digest", docker.ImageSummary{RepoTags: []string{"ghcr.io/o/r:1"}}, true},
		{"digest of a ref with tag", docker.ImageSummary{RepoDigests: []string{"ghcr.io/o/r@sha256:aa"}}, true},
		{"digest-only ref names no tag", docker.ImageSummary{RepoTags: []string{"repo:latest"}}, false},
		{"digest-only ref matches digest", docker.ImageSummary{RepoDigests: []string{"repo@sha256:bb"}}, true},
		{"bare digest is an id", docker.ImageSummary{ID: "sha256:id1"}, true},
		{"image id", docker.ImageSummary{ID: "sha256:id2"}, true},
		{"unrelated", docker.ImageSummary{ID: "sha256:zz", RepoTags: []string{"x:1"}, RepoDigests: []string{"x@sha256:cc"}}, false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, m.protects(c.img), c.name)
	}
}
