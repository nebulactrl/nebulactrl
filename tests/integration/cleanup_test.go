//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/cleanup"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// scopedDaemon is the Docker daemon as the cleanup under test may see it: only
// the test's own images (and the dangling ones it made) are listed, and no
// networks or volumes. The harness shares the daemon with the developer's own
// images, which a real cleanup would happily remove.
type scopedDaemon struct {
	cleanup.Docker
	mu    sync.Mutex
	ids   map[string]bool // image IDs the test made without a tag
	match string          // repository substring of the test's tagged images
}

func (d *scopedDaemon) mine(img docker.ImageSummary) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ids[img.ID] {
		return true
	}
	for _, t := range img.RepoTags {
		if strings.Contains(t, d.match) {
			return true
		}
	}
	return false
}

func (d *scopedDaemon) addID(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids[id] = true
}

func (d *scopedDaemon) ImageList(ctx context.Context, ref string) ([]docker.ImageSummary, error) {
	all, err := d.Docker.ImageList(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out []docker.ImageSummary
	for _, img := range all {
		if d.mine(img) {
			out = append(out, img)
		}
	}
	return out, nil
}

func (d *scopedDaemon) NetworkList(context.Context, string, map[string]string) ([]docker.NetworkInfo, error) {
	return nil, nil
}

func (d *scopedDaemon) VolumeList(context.Context) ([]docker.VolumeInfo, error) { return nil, nil }

// ImagesSize measures only the test's images, so the report's reclaimed bytes
// are the test's own.
func (d *scopedDaemon) ImagesSize(ctx context.Context) (int64, error) {
	all, err := d.Docker.ImageList(ctx, "")
	if err != nil {
		return 0, err
	}
	var n int64
	for _, img := range all {
		if d.mine(img) {
			n += img.Size
		}
	}
	return n, nil
}

// dockerBuild builds an image FROM base with one label (a distinct image ID
// per label) and returns its ID.
func dockerBuild(t *testing.T, tag, base, label string) string {
	t.Helper()
	cmd := exec.Command("docker", "build", "-q", "-t", tag, "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("FROM %s\nLABEL nebulactrl.cleanup.test=%q\n", base, label))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func imagePresent(ctx context.Context, ref string) bool {
	_, err := dockerEng.ImageInspect(ctx, ref)
	return err == nil
}

// Docker cleanup: a dangling image and an old unused image are removed; the
// current release's image and the rollback image (a retained earlier release)
// stay, and rolling back to it afterwards finds it on the host. Volumes are
// never touched. The cleanup runs through the real path: the control plane
// computes the protected set, queues the command, the agent prunes, and the
// result lands in the run history.
//
// The agent's clock is moved 100 hours ahead (every unused image is "old") and
// its view of the daemon is scoped to this test's images (see scopedDaemon).
func TestDockerCleanupKeepsRollbackImages(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	ctx := context.Background()
	const repoName = "nc-cleanup-test"
	repo := app.Registry + "/" + repoName

	scope := &scopedDaemon{ids: map[string]bool{}, match: repoName}
	s := ExclusiveStack(t, StackOptions{CleanupTune: func(cfg *cleanup.Config) {
		scope.Docker = cfg.Docker
		cfg.Docker = scope
		cfg.Now = func() time.Time { return time.Now().Add(100 * time.Hour) }
		cfg.PruneBuildCache = nil
		cfg.IsManagedNetwork = nil
	}})

	// Everything this test puts on the daemon goes away again.
	var tags []string
	t.Cleanup(func() {
		for _, ref := range tags {
			_, _ = sh(ctx, "", "docker", "rmi", "-f", ref)
		}
	})
	t.Cleanup(func() {
		scope.mu.Lock()
		defer scope.mu.Unlock()
		for id := range scope.ids {
			_, _ = sh(ctx, "", "docker", "rmi", "-f", id)
		}
	})

	// v1, v2 and junk live in the harness registry; the host starts without them.
	for _, v := range []string{"v1", "v2", "junk"} {
		ref := repo + ":" + v
		tags = append(tags, ref)
		dockerBuild(t, ref, app.AppImage, v)
		_, err := sh(ctx, "", "docker", "push", ref)
		require.NoError(t, err)
		_, err = sh(ctx, "", "docker", "rmi", ref)
		require.NoError(t, err)
	}
	// An unused image that was pulled long ago.
	_, err := sh(ctx, "", "docker", "pull", repo+":junk")
	require.NoError(t, err)

	// A dangling image: the second build takes the tag from the first.
	// (The tag stays outside the cleanup's scope: only the dangling image is the test's subject.)
	const holder = "nebulactrl-cleanup-dangling:1"
	dangling := dockerBuild(t, holder, app.AppImage, "dangling-a")
	tags = append(tags, holder)
	scope.addID(dangling)
	dockerBuild(t, holder, app.AppImage, "dangling-b")

	// An unused volume: a cleanup must never delete it.
	vol := fmt.Sprintf("nebulactrl-cleanup-test-%d", time.Now().UnixNano())
	_, err = sh(ctx, "", "docker", "volume", "create", vol)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sh(ctx, "", "docker", "volume", "rm", "-f", vol) })

	// Two releases of one service: v1, then v2.
	svc := s.NewService(SvcSpec{Name: "web", Image: repo + ":v1", Vars: map[string]string{"VERSION": "v1"}})
	d1 := s.DeployOK(svc.ID)
	cfg := map[string]any{
		"image": repo + ":v2", "port": 8080, "stopGraceSeconds": 5,
		"healthcheck": map[string]any{"type": "http", "path": "/health", "intervalSeconds": 1, "deadlineSeconds": 30, "successThreshold": 1},
	}
	r := s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": cfg}, "If-Match", s.serviceETag(svc.ID))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	d2 := s.DeployOK(svc.ID)
	require.Equal(t, d2.Str("id"), s.Service(svc.ID).Str("currentDeploymentId"))

	// Before: all four images are on the host.
	for _, ref := range []string{repo + ":v1", repo + ":v2", repo + ":junk", dangling} {
		require.True(t, imagePresent(ctx, ref), "%s should be on the host before the cleanup", ref)
	}

	// Run the cleanup now.
	srvPath := "/servers/" + s.ServerID + "/cleanup"
	r = s.Do("POST", srvPath+"/runs", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	runID := r.JSON().Str("id")
	var run J
	require.True(t, eventually(2*time.Minute, 500*time.Millisecond, func() bool {
		v := s.Do("GET", srvPath, nil)
		if v.Status != http.StatusOK {
			return false
		}
		for _, it := range v.JSON()["runs"].([]any) {
			m := J(it.(map[string]any))
			if m.Str("id") == runID && m.Str("status") != "pending" {
				run = m
				return true
			}
		}
		return false
	}), "the cleanup did not finish")
	require.Equal(t, "succeeded", run.Str("status"), "%v", run)
	require.Equal(t, "manual", run.Str("trigger"))

	// What was sent: the protected set names both releases; no volumes in the plan.
	var payload string
	require.NoError(t, s.Pool.QueryRow(ctx, `SELECT payload::text FROM agent_commands WHERE kind = 'docker_cleanup' ORDER BY created_at DESC LIMIT 1`).Scan(&payload))
	require.Contains(t, payload, repo+":v1")
	require.Contains(t, payload, repo+":v2")
	require.Contains(t, payload, `"mode": "standard"`) // jsonb::text spacing

	// After: junk and the dangling image are gone; the current and the rollback image stay.
	require.False(t, imagePresent(ctx, repo+":junk"), "an old unused image is removed")
	require.False(t, imagePresent(ctx, dangling), "a dangling image is removed")
	require.True(t, imagePresent(ctx, repo+":v2"), "the current release's image stays")
	require.True(t, imagePresent(ctx, repo+":v1"), "the rollback image stays")
	require.Len(t, s.RunningContainers(svc.ID), 1, "the service keeps running")
	_, err = sh(ctx, "", "docker", "volume", "inspect", vol)
	require.NoError(t, err, "a cleanup never removes a volume")

	// The report.
	steps := map[string]J{}
	for _, it := range run["steps"].([]any) {
		m := J(it.(map[string]any))
		steps[m.Str("name")] = m
	}
	require.Equal(t, 1, steps["images_dangling"].Int("items"), "%v", run)
	require.Equal(t, 1, steps["images_unused"].Int("items"), "%v", run)
	require.GreaterOrEqual(t, steps["images_unused"].Int("protected"), 2, "v1 and v2 are kept for releases")
	require.Contains(t, fmt.Sprint(steps["images_unused"]["removed"]), repoName+":junk")
	require.Zero(t, steps["containers_unmanaged"].Int("items"), "stopped unmanaged containers are kept by default")
	require.Positive(t, run.Int("reclaimedBytes"), "the report says what was freed")
	require.Equal(t, 2, run.Int("items"))

	// Rolling back to v1 finds its image on the host (no pull).
	r = s.Do("POST", "/deployments/"+d1.Str("id")+"/rollback", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	rb := s.WaitDeployment(r.JSON().Str("id"), 3*time.Minute)
	require.Equal(t, "succeeded", rb.Str("status"), "%v\n%s", rb, s.DeploymentLogs(rb.Str("id")))
	require.Contains(t, s.DeploymentLogs(rb.Str("id")), "already present locally", "the rollback reused the retained image")

	// A second run right away finds nothing to remove.
	r = s.Do("POST", srvPath+"/runs", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	require.True(t, eventually(2*time.Minute, 500*time.Millisecond, func() bool {
		v := s.Do("GET", srvPath, nil).JSON()
		runs, _ := v["runs"].([]any)
		if len(runs) == 0 {
			return false
		}
		latest := J(runs[0].(map[string]any))
		return latest.Str("id") == r.JSON().Str("id") && latest.Str("status") == "succeeded" && latest.Int("items") == 0
	}), "the second cleanup should succeed with nothing to remove")

	// The volume action is guarded: the server's name must be typed.
	r = s.Do("POST", srvPath+"/volumes", map[string]any{"confirm": "nope"})
	require.Equal(t, http.StatusBadRequest, r.Status, string(r.Raw))
}
