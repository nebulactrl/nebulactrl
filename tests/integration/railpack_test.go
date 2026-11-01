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

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	railpackGitImage     = "nebulactrl-test-git-railpack:dev"
	railpackGitContainer = "nebulactrl-test-git-railpack"
)

// railpackFixtures serves the repositories of tests/fixtures/railpack (neither
// has a Dockerfile) over git://. It returns the repository URLs by name.
func railpackFixtures(t *testing.T) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root, err := repoRoot()
	require.NoError(t, err)
	work := t.TempDir()
	bare := filepath.Join(work, "ctx")
	require.NoError(t, os.MkdirAll(bare, 0o755))
	gitArgs := []string{"-c", "user.name=itest", "-c", "user.email=itest@example.com", "-c", "init.defaultBranch=main"}
	git := func(dir string, args ...string) {
		t.Helper()
		_, err := sh(ctx, dir, "git", append(append([]string{}, gitArgs...), args...)...)
		require.NoError(t, err)
	}
	for _, name := range []string{"node-app", "static-site"} {
		tree := filepath.Join(work, name)
		require.NoError(t, os.CopyFS(tree, os.DirFS(filepath.Join(root, "tests", "fixtures", "railpack", name))))
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture " + name}} {
			git(tree, args...)
		}
		git(work, "clone", "-q", "--bare", tree, filepath.Join(bare, name+".git"))
		git(filepath.Join(bare, name+".git"), "update-server-info")
	}
	dockerfile := "FROM alpine:3\n" +
		"RUN apk add --no-cache git git-daemon && git config --system safe.directory '*'\n" +
		"COPY node-app.git /srv/git/node-app.git\nCOPY static-site.git /srv/git/static-site.git\n" +
		"EXPOSE 9418\n" +
		`ENTRYPOINT ["git", "daemon", "--reuseaddr", "--export-all", "--base-path=/srv/git", "--listen=0.0.0.0", "--port=9418", "/srv/git"]` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bare, "Dockerfile"), []byte(dockerfile), 0o644))

	_, _ = sh(ctx, "", "docker", "rm", "-f", railpackGitContainer)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_, _ = sh(cctx, "", "docker", "rm", "-f", railpackGitContainer)
		_, _ = sh(cctx, "", "docker", "image", "rm", "-f", railpackGitImage)
	})
	_, err = sh(ctx, bare, "docker", "build", "-q", "-t", railpackGitImage, ".")
	require.NoError(t, err)
	port, err := freePort()
	require.NoError(t, err)
	_, err = sh(ctx, "", "docker", "run", "-d", "--name", railpackGitContainer, "-p", fmt.Sprintf("127.0.0.1:%d:9418", port), railpackGitImage)
	require.NoError(t, err)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	require.NoError(t, waitTCP(ctx, addr))
	return map[string]string{
		"node-app":    "git://" + addr + "/node-app.git",
		"static-site": "git://" + addr + "/static-site.git",
	}
}

// dockerHas reports whether `docker <args>` succeeds.
func dockerHas(args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := sh(ctx, "", "docker", args...)
	return err == nil
}

// railpackCleanup removes what a Railpack build leaves on the host: the built
// images, and the tooling and caches the test itself brought along (the
// frontend image, the managed BuildKit builder with its cache, its image). What
// was already on the host before the test stays.
func railpackCleanup(t *testing.T, slugs func() []string) {
	t.Helper()
	hadFrontend := dockerHas("image", "inspect", agentprotocol.RailpackFrontendImage)
	hadBuilder := dockerHas("buildx", "inspect", agentprotocol.BuildxBuilderName)
	hadBuildKit := dockerHas("image", "inspect", strings.Split(agentprotocol.BuildKitImage, "@")[0])
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, slug := range slugs() {
			out, _ := sh(ctx, "", "docker", "image", "ls", "--format", "{{.ID}}", "nc-build/"+slug)
			for _, id := range strings.Fields(out) {
				_, _ = sh(ctx, "", "docker", "image", "rm", "-f", id)
			}
		}
		if !hadBuilder {
			// Removes the builder container and its cache volume.
			_, _ = sh(ctx, "", "docker", "buildx", "rm", "-f", agentprotocol.BuildxBuilderName)
		}
		if !hadBuildKit {
			_, _ = sh(ctx, "", "docker", "image", "rm", "-f", strings.Split(agentprotocol.BuildKitImage, "@")[0])
		}
		if !hadFrontend {
			_, _ = sh(ctx, "", "docker", "image", "rm", "-f", agentprotocol.RailpackFrontendImage)
		}
		// Daemon-side cache only matters (and is only prunable by us) on hosts
		// that build Railpack through the daemon itself.
		if hadBuilder || !dockerHas("buildx", "inspect", agentprotocol.BuildxBuilderName) {
			_, _ = sh(ctx, "", "docker", "builder", "prune", "-f")
		}
	})
}

// Repositories without a Dockerfile build with the pinned Railpack and serve:
// a Node app and a static site, each previewed (build plan) and then deployed.
func TestRailpackBuilds(t *testing.T) {
	requireTier(t, tierFull)
	repos := railpackFixtures(t)
	s := ExclusiveStack(t)
	var slugs []string
	railpackCleanup(t, func() []string { return slugs })

	cases := []struct {
		name, repo, host, port, want, provider, start string
	}{
		{"node-app", repos["node-app"], "railpack-node.localhost", "3000", "railpack-node-ok", "node", "npm run start"},
		{"static-site", repos["static-site"], "railpack-static.localhost", "3000", "railpack-static-ok", "staticfile", "caddy run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := 3000
			svc := s.NewService(SvcSpec{
				Name: tc.name, GitURL: tc.repo, GitBranch: "main", Domain: tc.host, Port: port,
				Health: map[string]any{"path": "/", "deadlineSeconds": 90},
			})
			slugs = append(slugs, svc.Slug)
			require.Equal(t, "auto", s.Service(svc.ID).Obj("config").Str("buildType"), "git services default to the automatic build method")

			// The preview names the method and what Railpack detected, before anything is built.
			r := s.Do("POST", "/services/"+svc.ID+"/build-plan", nil)
			require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
			plan := r.JSON()
			require.Equal(t, "succeeded", plan.Str("outcome"), "%v", plan)
			require.False(t, plan.Bool("dockerfileFound"))
			info := plan.Obj("info")
			require.Equal(t, "railpack", info.Str("method"), "%v", plan)
			require.Equal(t, agentprotocol.RailpackVersion, info.Str("railpackVersion"))
			require.Contains(t, fmt.Sprint(info["providers"]), tc.provider)
			require.Contains(t, info.Str("startCommand"), tc.start)

			// Deploy: no Dockerfile, so Railpack builds it. The first build on a
			// host pulls Railpack's builder images, hence the long timeout.
			dep := s.WaitDeployment(s.Deploy(svc.ID), 12*time.Minute)
			require.Equal(t, "succeeded", dep.Str("status"), "deployment: %v\nlogs:\n%s", dep, s.DeploymentLogs(dep.Str("id")))
			build := dep.Obj("build")
			require.Equal(t, "railpack", build.Str("method"), "%v", dep)
			require.Equal(t, agentprotocol.RailpackVersion, build.Str("railpackVersion"))
			require.NotEmpty(t, build.Str("builder"))
			logs := s.DeploymentLogs(dep.Str("id"))
			require.Contains(t, logs, "Build method: Railpack")

			c := NewProxyClient(tc.host)
			ok := eventually(60*time.Second, time.Second, func() bool {
				code, body, err := c.Get("/")
				return err == nil && code == 200 && strings.Contains(body, tc.want)
			})
			code, body, err := c.Get("/")
			require.True(t, ok, "GET / on %s: %d %q err=%v", tc.host, code, body, err)
		})
	}
}
