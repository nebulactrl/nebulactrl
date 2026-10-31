//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
)

// slowBuildDockerfile never finishes building by itself: the RUN sleeps. Its
// base image (alpine:3) is already on the host, the git fixture is built from it.
const slowBuildDockerfile = `FROM alpine:3
RUN echo nc-slow-build-started && sleep 900
COPY app /app
ENTRYPOINT ["/app"]
`

// slowBuildRepo publishes a repository whose Dockerfile takes 15 minutes to
// build on the git fixture's daemon and returns its git:// URL.
func slowBuildRepo(t *testing.T, g gitFixture) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	app := appFixture(t)
	work := t.TempDir()
	tree := filepath.Join(work, "tree")
	require.NoError(t, os.MkdirAll(tree, 0o755))
	bin, err := os.ReadFile(filepath.Join(app.appDir, "app"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tree, "app"), bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte(slowBuildDockerfile), 0o644))

	git := func(dir string, args ...string) {
		t.Helper()
		out, err := sh(ctx, dir, "git", append([]string{"-c", "user.name=itest", "-c", "user.email=itest@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		require.NoError(t, err, out)
	}
	git(tree, "init", "-q")
	git(tree, "add", "-A")
	git(tree, "commit", "-q", "-m", "slow build")
	name := fmt.Sprintf("slow-%d.git", time.Now().UnixNano())
	bare := filepath.Join(work, name)
	git(work, "clone", "-q", "--bare", tree, bare)
	git(bare, "update-server-info")
	out, err := sh(ctx, "", "docker", "cp", bare, gitContainer+":/srv/git/"+name)
	require.NoError(t, err, out)
	t.Cleanup(func() {
		_, _ = sh(context.Background(), "", "docker", "exec", gitContainer, "rm", "-rf", "/srv/git/"+name)
	})
	return strings.TrimSuffix(g.URL, "app.git") + name
}

// requireCanceledKeepingV1 checks what a cancel must leave behind: the
// deployment is canceled at wantStep, the old release is the same container, is
// still current and still answers, and nothing of the new release remains.
func requireCanceledKeepingV1(t *testing.T, s *Stack, svc Service, px *ProxyClient, d J, wantStep string, v1Container string, v1Deployment string) {
	t.Helper()
	logs := s.DeploymentLogs(d.Str("id"))
	require.Equal(t, "canceled", d.Str("status"), "%v\n%s", d, logs)
	require.Equal(t, wantStep, d.Str("canceledAtStep"), "%v", d)
	require.Contains(t, d.Str("statusMessage"), "Canceled by", "%v", d)
	require.Contains(t, d.Str("statusMessage"), "previous release", "%v", d)

	px.WaitBody(t, "/", "v1", 20*time.Second)
	running := s.RunningContainers(svc.ID)
	require.Len(t, running, 1, "only the old release runs")
	require.Equal(t, v1Container, running[0].ID, "the old container was never replaced")
	require.Len(t, s.Containers(svc.ID), 1, "no container of the canceled release remains")
	require.Equal(t, v1Deployment, s.Service(svc.ID).Str("currentDeploymentId"), "a canceled deployment never becomes current")

	// Canceled and audited with the step reached.
	var requested, done int
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'deployment.cancel' AND resource_id = $1 AND metadata->>'mode' = 'agent'`, d.Str("id")).Scan(&requested))
	require.Equal(t, 1, requested, "the request is audited")
	require.NoError(t, s.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'deployment.canceled' AND resource_id = $1 AND metadata->>'step' = $2`, d.Str("id"), wantStep).Scan(&done))
	require.Equal(t, 1, done, "the outcome is audited with the step")

	// A finished deployment cannot be canceled again.
	r := s.Do("POST", "/deployments/"+d.Str("id")+"/cancel", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "cannot_cancel", r.JSON().Str("code"))
}

// waitForLog waits until the persisted logs of a deployment contain want.
func (s *Stack) waitForLog(id, want string, timeout time.Duration) {
	s.t.Helper()
	var logs string
	require.True(s.t, eventually(timeout, 500*time.Millisecond, func() bool {
		logs = s.DeploymentLogs(id)
		return strings.Contains(logs, want)
	}), "%q never appeared in the logs of %s:\n%s", want, id, logs)
}

// TestCancelDuringBuildKeepsTheOldReleaseServing cancels a deployment while the
// agent builds a slow Dockerfile: the build is killed, its work directory and
// image are gone, the result is canceled and v1 answers without a failed
// request the whole time.
func TestCancelDuringBuildKeepsTheOldReleaseServing(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	g := gitServer(t)
	slowURL := slowBuildRepo(t, g)
	s := SharedStack(t)

	// v1: the fixture app, built from git.
	svc := s.NewService(SvcSpec{Name: "slowbuild", GitURL: g.URL, GitBranch: "main", Domain: s.Host(), Vars: map[string]string{"VERSION": "v1"}})
	v1 := s.DeployOK(svc.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)
	before := s.RunningContainers(svc.ID)
	require.Len(t, before, 1)

	// v2: the same service now builds the slow repository.
	cfg := map[string]any{
		"gitUrl": slowURL, "gitBranch": "main", "port": 8080, "stopGraceSeconds": 5,
		"healthcheck": map[string]any{"type": "http", "path": "/health", "intervalSeconds": 1, "deadlineSeconds": 30, "successThreshold": 1},
	}
	r := s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": cfg}, "If-Match", s.serviceETag(svc.ID))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)

	load := StartLoad(px, "/", 20*time.Millisecond)
	depID := s.Deploy(svc.ID)
	s.waitForLog(depID, "nc-slow-build-started", 3*time.Minute)
	require.Equal(t, "building", s.Deployment(depID).Str("status"))
	// The agent works on it (the command is acknowledged): cancel is no longer
	// the "not started" case.
	r = s.Do("POST", "/deployments/"+depID+"/cancel", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotNil(t, r.JSON()["cancelRequestedAt"], "the request is recorded: %s", r.Raw)

	d := s.WaitDeployment(depID, 90*time.Second)
	load.WaitMore(t, 30, 30*time.Second)
	res := load.Stop()

	requireCanceledKeepingV1(t, s, svc, px, d, "build", before[0].ID, v1.Str("id"))
	require.Zero(t, res.Non2xx, "failures while the build was canceled: %v", res.Failures)
	require.Zero(t, res.Errors, "%v", res.Failures)
	for _, b := range res.Bodies {
		require.Equal(t, "v1", b, "only the old release answered")
	}

	// The build left nothing behind: no work directory, no image of the canceled build.
	_, err := os.Stat(filepath.Join(s.StateDir, "builds", depID))
	require.True(t, os.IsNotExist(err), "the build work directory is removed")
	_, err = dockerEng.ImageInspect(context.Background(), "nc-build/"+svc.Slug+":"+depID[:8])
	require.ErrorIs(t, err, docker.ErrNotFound, "no image of the canceled build")
	logs := s.DeploymentLogs(depID)
	require.Contains(t, logs, "Canceled by")
	steps := s.stepEvents(depID)
	require.Contains(t, steps, "cancel:started", "%v", steps)
	require.Contains(t, steps, "cancel:succeeded", "%v", steps)
	require.NotContains(t, steps, "build:failed", "a cancel is not a build failure: %v", steps)

	// The service can be deployed again: the slot is free and nothing is stuck.
	// (Back to the fast repository so it ends quickly.)
	cfg["gitUrl"] = g.URL
	r = s.Do("PATCH", "/services/"+svc.ID, map[string]any{"config": cfg}, "If-Match", s.serviceETag(svc.ID))
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	s.DeployOK(svc.ID)
	px.WaitBody(t, "/", "v2", 30*time.Second)
}

// TestCancelDuringVerifyKeepsTheOldReleaseServing cancels an image deployment
// while it verifies the new route through the proxy (the probe is made to hang):
// the previous routes are restored first, the new container is removed, the
// result is canceled and v1 answers again.
func TestCancelDuringVerifyKeepsTheOldReleaseServing(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, "web", nil)
	v1 := s.Service(svc.ID).Str("currentDeploymentId")
	before := s.RunningContainers(svc.ID)
	require.Len(t, before, 1)

	// The probe of the new route never answers, so verification cannot finish.
	s.Fault.Hang(px.host)
	t.Cleanup(func() { s.Fault.Unhang(px.host) })
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	load := StartLoad(px, "/", 20*time.Millisecond)
	depID := s.Deploy(svc.ID)
	require.True(t, eventually(2*time.Minute, 300*time.Millisecond, func() bool {
		return indexOf(s.stepEvents(depID), "verify:started") >= 0
	}), "verification never started: %v\n%s", s.stepEvents(depID), s.DeploymentLogs(depID))
	require.Equal(t, "verifying", s.Deployment(depID).Str("status"))

	r := s.Do("POST", "/deployments/"+depID+"/cancel", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	d := s.WaitDeployment(depID, 60*time.Second)
	s.Fault.Unhang(px.host)
	load.WaitMore(t, 30, 30*time.Second)
	res := load.Stop()

	requireCanceledKeepingV1(t, s, svc, px, d, "verify", before[0].ID, v1)
	require.Contains(t, d.Str("statusMessage"), "previous routes were restored", "%v", d)
	require.Zero(t, res.Errors, "connection errors while the cancel restored the routes: %v", res.Failures)
	require.Zero(t, res.Non2xx, "failures while the cancel restored the routes: %v", res.Failures)
	for _, b := range res.Bodies {
		require.Contains(t, []string{"v1", "v2"}, b)
	}
	steps := s.stepEvents(depID)
	require.Contains(t, steps, "cancel:succeeded", "%v", steps)
	require.NotContains(t, steps, "retire:started", "the old release is never retired: %v", steps)
	require.NotContains(t, steps, "verify:failed", "a cancel is not a verification failure: %v", steps)
}
