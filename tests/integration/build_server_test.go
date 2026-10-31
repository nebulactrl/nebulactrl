//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/internal/agent/stats"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// externalRegistryImage is the registry the tests push to: the same CNCF
// Distribution release the managed "Set up for me" registry pins, run here as an
// *external* registry on a loopback port (Docker and BuildKit treat loopback
// registries as insecure, and the control plane only allows plain HTTP there).
const externalRegistryImage = "registry:3.1.2"

// localRegistry is an auth-less registry:3 container published on 127.0.0.1.
type localRegistry struct {
	// Host is host:port, the form a registry is registered with.
	Host string
	name string
}

// startLocalRegistry runs a registry container on a free loopback port and
// removes it (and its anonymous data volume) when the test ends.
func startLocalRegistry(t *testing.T) *localRegistry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	port, err := freePort()
	require.NoError(t, err)
	r := &localRegistry{name: fmt.Sprintf("nebulactrl-test-extreg-%d", port), Host: fmt.Sprintf("127.0.0.1:%d", port)}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_, _ = sh(cctx, "", "docker", "rm", "-fv", r.name)
	})
	out, err := sh(ctx, "", "docker", "run", "-d", "--name", r.name, "-p", fmt.Sprintf("127.0.0.1:%d:5000", port), externalRegistryImage)
	require.NoError(t, err, out)
	require.True(t, eventually(60*time.Second, 300*time.Millisecond, func() bool {
		res, err := http.Get("http://" + r.Host + "/v2/")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	}), "registry %s did not answer /v2/", r.Host)
	return r
}

// getJSON reads a registry API document.
func (r *localRegistry) getJSON(t *testing.T, path string, v any) {
	t.Helper()
	res, err := http.Get("http://" + r.Host + path)
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode, "GET %s: %s", path, raw)
	require.NoError(t, json.Unmarshal(raw, v), string(raw))
}

// repositories lists the catalog.
func (r *localRegistry) repositories(t *testing.T) []string {
	t.Helper()
	var cat struct {
		Repositories []string `json:"repositories"`
	}
	r.getJSON(t, "/v2/_catalog", &cat)
	return cat.Repositories
}

// tags lists a repository's tags.
func (r *localRegistry) tags(t *testing.T, repo string) []string {
	t.Helper()
	var tl struct {
		Tags []string `json:"tags"`
	}
	r.getJSON(t, "/v2/"+repo+"/tags/list", &tl)
	return tl.Tags
}

// StartBuildAgent is StartAgent for a build-only server: the engine runs with
// BuildOnly, so there is no Traefik, no edge network, no orphan or network
// sweep (nothing that would touch the target agent's containers on the shared
// Docker daemon), and the agent advertises build.push. It is the production
// wiring of cmd/agent minus the proxy.
func StartBuildAgent(ctx context.Context, opt AgentOptions) (*Agent, error) {
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		if os.Getenv("NEBULACTRL_TEST_AGENT_LOGS") != "" {
			opt.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("service", "build-agent")
		}
	}
	log := opt.Logger

	eps, err := conn.ParseEndpoints(opt.URL, true)
	if err != nil {
		return nil, err
	}
	eng, err := docker.NewSDKEngine()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(opt.StateDir)
	if err != nil {
		_ = eng.Close()
		return nil, err
	}
	cleanupEarly := func() { _ = st.Close(); _ = eng.Close() }
	a := &Agent{opt: opt, done: make(chan struct{})}

	collector := stats.New(stats.Options{
		Engine: eng, StateDir: opt.StateDir,
		HTTPPort: proxyPorts.HTTP, HTTPSPort: proxyPorts.HTTPS,
		// No proxy runs here; a build server reports it healthy so the server
		// reads as online.
		ProxyStatus:   func() agentprotocol.ProxyHealth { return agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK} },
		BuildxVersion: build.BuildxVersion,
	})
	facts := collector.Facts(ctx)

	identity, ok, err := st.Identity()
	if err != nil {
		cleanupEarly()
		return nil, err
	}
	if !ok || identity.Revoked {
		hostname, _ := os.Hostname()
		identity, err = conn.Enroll(ctx, st, eps.Enroll, opt.Token, hostname, facts, nil)
		if err != nil {
			cleanupEarly()
			return nil, err
		}
	}
	a.Identity = identity

	rctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	client := conn.New(conn.Config{
		URL: eps.Connect, Identity: identity, Store: st, Version: "itest",
		Facts: collector.Facts, Heartbeat: collector.Heartbeat, Logger: log,
	})
	deployer, err := engine.New(engine.Config{
		BuildOnly: true,
		Docker:    eng, Builder: &build.Builder{StateDir: opt.StateDir}, Sender: client, Logger: log,
		BackupDir: filepath.Join(opt.StateDir, "backups"),
	})
	if err != nil {
		cancel()
		cleanupEarly()
		return nil, err
	}
	client.SetHandler(deployer)
	client.SetReconciler(deployer)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); deployer.Run(rctx) }()
	go func() {
		defer close(a.done)
		err := client.Run(rctx)
		a.mu.Lock()
		a.runErr = err
		a.mu.Unlock()
		cancel()
		wg.Wait()
		cleanupEarly()
	}()
	return a, nil
}

// startBuildServer enrolls a second server on the stack's control plane, runs a
// build-only agent for it on this Docker daemon and gives it the Builds role
// (builds, no workloads). It returns the server's id.
func startBuildServer(t *testing.T, s *Stack) (string, *Agent) {
	t.Helper()
	serverID, token := s.EnrollmentToken("builder-" + s.hostBase)
	a, err := StartBuildAgent(context.Background(), AgentOptions{StateDir: t.TempDir(), URL: s.Srv.URL, Token: token})
	require.NoError(t, err)
	t.Cleanup(a.Stop)

	r := s.Do("PATCH", "/servers/"+serverID, map[string]any{"buildEnabled": true, "acceptWorkloads": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	srv := s.WaitServer(serverID, 90*time.Second)
	require.Equal(t, true, srv["buildEnabled"], "server: %v", srv)
	require.Equal(t, false, srv["acceptWorkloads"], "server: %v", srv)
	require.Contains(t, fmt.Sprint(srv.Obj("facts")["capabilities"]), agentprotocol.CapBuildPush, "build agent did not advertise build.push: %v", srv.Obj("facts"))
	return serverID, a
}

// waitServerOffline waits until the control plane marks a server offline (30 s
// without a heartbeat, plus the 10 s sweep).
func (s *Stack) waitServerOffline(serverID string, timeout time.Duration) {
	s.t.Helper()
	var last J
	require.True(s.t, eventually(timeout, time.Second, func() bool {
		r := s.Do("GET", "/servers/"+serverID, nil)
		if r.Status != http.StatusOK {
			return false
		}
		last = r.JSON()
		return last.Str("status") == "offline"
	}), "server %s did not go offline: %v", serverID, last)
}

// deploymentEvents returns a deployment's timeline.
func (s *Stack) deploymentEvents(id string) []J {
	s.t.Helper()
	r := s.Do("GET", "/deployments/"+id+"/events", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON().Items()
}

// hasEvent reports whether the timeline has an event of step and status whose
// message contains substr.
func hasEvent(evs []J, step, status, substr string) bool {
	for _, e := range evs {
		if e.Str("step") == step && (status == "" || e.Str("status") == status) && strings.Contains(e.Str("message"), substr) {
			return true
		}
	}
	return false
}

func describeEvents(evs []J) string {
	var sb strings.Builder
	for _, e := range evs {
		fmt.Fprintf(&sb, "  %s/%s: %s\n", e.Str("step"), e.Str("status"), e.Str("message"))
	}
	return sb.String()
}

// localBuildImages lists the nc-build images of a service on the daemon: what a
// build on the target (or a --load) would leave behind.
func localBuildImages(t *testing.T, slug string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sh(ctx, "", "docker", "image", "ls", "-q", "nc-build/"+slug)
	require.NoError(t, err)
	return strings.Fields(out)
}

// TestBuildServerFlow runs the build-server paths on the shared control plane and
// target agent, with a second, build-only agent on the same Docker daemon and a
// loopback registry:3.1.2 as the external registry:
//
//  1. a git service with buildLocation=build_server builds on the build agent and
//     pushes (events from both servers, tag r-<release8> in the registry, release
//     registryRef, the target container runs repo@digest, nothing built locally);
//  2. with the build agent stopped, a rollback to the earlier release succeeds by
//     pulling the digest;
//  3. with the build server offline at dispatch, the deployment falls back to
//     building on the target and says so.
//
// Bridge budget (macOS): the test holds one network slot, the target service's
// environment network. The build agent creates no network (its BuildKit builder
// uses the host network, and BuildOnly runs no proxy or sweeps), the registry
// and the git fixture sit on Docker's default bridge.
func TestBuildServerFlow(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	g := gitServer(t)
	reg := startLocalRegistry(t)
	s := SharedStackN(t, 1)

	target := s.Do("GET", "/servers/"+s.ServerID, nil).JSON()
	targetName := target.Str("name")
	require.NotEmpty(t, targetName)

	builderID, builderAgent := startBuildServer(t, s)
	// The per-project BuildKit builder and its cache are the build agent's
	// leftovers; remove them with the test.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = sh(ctx, "", "docker", "buildx", "rm", "-f", build.BuilderPrefix+agentprotocol.Short(s.ProjectID))
	})

	// The external registry: loopback, so plain HTTP is allowed; the registry has
	// no authentication, so the (required) push credential is a dummy pair.
	r := s.Do("POST", "/registries", map[string]any{
		"name": "itest-" + s.hostBase, "host": reg.Host, "pushUsername": "test", "pushPassword": "test-password-12345",
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	registryID := r.JSON().Str("id")
	require.Equal(t, "external", r.JSON().Str("kind"))
	// The registry row (like the builder's server row) stays in the shared
	// schema, which the run drops at the end: deleting it here would run before
	// cleanupShared removes the services that reference it (409 registry_in_use).

	// Test registry from the build server: reachable, credentials accepted.
	r = s.Do("POST", "/registries/"+registryID+"/check?serverId="+builderID, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Bool("ok"), "registry check: %s", r.Raw)

	svc := s.NewService(SvcSpec{
		Name: "built", GitURL: g.URL, GitBranch: "main", Domain: s.Host(),
		Vars: map[string]string{"VERSION": "v1"},
		Config: map[string]any{
			"buildLocation": "build_server",
			"registryId":    registryID,
		},
	})
	px := NewProxyClient(s.Host())

	var (
		d1         J
		ref1       string
		digest1    string
		release1   string
		repository string
	)

	t.Run("build_on_build_server_and_pull_by_digest", func(t *testing.T) {
		// The plan names the build server and the registry before anything runs.
		plan := s.Do("POST", "/services/"+svc.ID+"/deployments/plan", map[string]any{})
		require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
		pl := plan.JSON().Obj("placement")
		require.Equal(t, "build_server", pl.Str("location"), "plan: %s", plan.Raw)
		require.Equal(t, builderID, pl.Str("buildServerId"), "plan: %s", plan.Raw)
		require.Equal(t, reg.Host, pl.Str("registryHost"), "plan: %s", plan.Raw)
		require.False(t, pl.Bool("fallback"), "plan: %s", plan.Raw)

		d1 = s.DeployOK(svc.ID)
		px.WaitBody(t, "/", "v1", 30*time.Second)
		id := d1.Str("id")
		logs := s.DeploymentLogs(id)
		evs := s.deploymentEvents(id)

		// Two servers took part: the build server is recorded next to the target.
		require.Equal(t, s.ServerID, d1.Str("serverId"), "the deployment runs on the target")
		require.Equal(t, builderID, d1.Str("buildServerId"), "deployment: %v", d1)

		// The build agent emitted build and push; the target pulled and ran it.
		for _, step := range []string{agentprotocol.StepBuild, agentprotocol.StepPush} {
			require.True(t, hasEvent(evs, step, "succeeded", ""), "no succeeded %s event (build server):\n%s", step, describeEvents(evs))
		}
		require.True(t, hasEvent(evs, agentprotocol.StepPush, "succeeded", reg.Host), "push event does not name the registry:\n%s", describeEvents(evs))
		require.True(t, hasEvent(evs, agentprotocol.StepPull, "", ""), "no pull event (target):\n%s", describeEvents(evs))
		require.True(t, hasEvent(evs, agentprotocol.StepStart, "", ""), "no start event (target):\n%s", describeEvents(evs))
		require.Contains(t, logs, "["+agentprotocol.StreamBuild+"]", "no build log lines:\n%s", logs)
		require.Contains(t, logs, "["+agentprotocol.StreamDeploy+"]", "no deploy log lines:\n%s", logs)

		// The release records the pushed digest and where it was built.
		src := d1.Obj("release").Obj("source")
		release1 = d1.Obj("release").Str("id")
		ref1 = src.Str("registryRef")
		require.NotEmpty(t, ref1, "release source has no registryRef: %v", src)
		require.Equal(t, g.CommitSHA, src.Str("commitSha"))
		require.Equal(t, registryID, src.Str("registryId"))
		require.Equal(t, builderID, src.Str("builtOn"))
		require.True(t, strings.HasPrefix(src.Str("platform"), "linux/"), "source: %v", src)
		require.True(t, strings.HasPrefix(ref1, reg.Host+"/"), "registryRef %q is not under %s", ref1, reg.Host)
		repository, digest1, _ = strings.Cut(strings.TrimPrefix(ref1, reg.Host+"/"), "@")
		require.True(t, strings.HasPrefix(digest1, "sha256:"), "registryRef %q", ref1)
		require.Contains(t, repository, svc.Slug, "repository %q", repository)

		// The registry holds the repository with the release's tag, and the control
		// plane lists the image.
		require.Contains(t, reg.repositories(t), repository)
		require.Contains(t, reg.tags(t, repository), "r-"+agentprotocol.Short(release1))
		var found bool
		for _, it := range s.Do("GET", "/registries/"+registryID+"/images", nil).JSON().Items() {
			if it.Str("digest") == digest1 {
				found = true
				require.Equal(t, "live", it.Str("state"))
				require.Equal(t, reg.Host+"/"+repository, it.Str("repository"), "the image list holds host/namespace/repo")
			}
		}
		require.True(t, found, "registry image list does not contain %s", digest1)

		// The target runs exactly repo@digest, and nothing was built (or loaded)
		// on the daemon: the build agent pushes without loading.
		cs := s.RunningContainers(svc.ID)
		require.Len(t, cs, 1)
		img, err := dockerEng.ImageInspect(context.Background(), cs[0].ImageID)
		require.NoError(t, err)
		require.Contains(t, strings.Join(img.RepoDigests, ","), ref1, "container image digests: %v", img.RepoDigests)
		require.Empty(t, localBuildImages(t, svc.Slug), "an nc-build image exists: the build ran on (or loaded into) a target")

		// Nothing secret in what a build server's commands carried.
		var payload string
		require.NoError(t, s.Pool.QueryRow(context.Background(),
			`SELECT coalesce(string_agg(kind || ' ' || payload::text || coalesce(result::text,''), E'\n'), '') FROM agent_commands WHERE deployment_id = $1`, id).Scan(&payload))
		require.NotContains(t, payload, "test-password-12345", "the registry credential is in a persisted command")
		require.Contains(t, payload, agentprotocol.KindBuildImage)
	})

	t.Run("rollback_without_build_server", func(t *testing.T) {
		require.NotEmpty(t, ref1, "needs the first scenario")
		// A second release (same commit, other variable); then the build server
		// goes away for good.
		s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
		d2 := s.DeployOK(svc.ID)
		px.WaitBody(t, "/", "v2", 30*time.Second)

		builderAgent.Stop()

		// Make the target pull: drop its local reference to the digest (best
		// effort; the running release may share the image).
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = sh(ctx, "", "docker", "rmi", "-f", ref1)
		_, inspectErr := dockerEng.ImageInspect(ctx, ref1)
		removedLocally := inspectErr != nil

		r := s.Do("POST", "/deployments/"+d1.Str("id")+"/rollback", nil)
		require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
		rb := s.WaitDeployment(r.JSON().Str("id"), 3*time.Minute)
		require.Equal(t, "succeeded", rb.Str("status"), "rollback: %v\n%s", rb, s.DeploymentLogs(rb.Str("id")))
		require.Equal(t, d2.Str("id"), rb.Str("rolledBackFrom"))
		require.Equal(t, release1, rb.Obj("release").Str("id"), "the rollback reuses the first release")
		px.WaitBody(t, "/", "v1", 30*time.Second)

		// No build happened: the build server is gone, and the rollback never needed it.
		evs := s.deploymentEvents(rb.Str("id"))
		require.False(t, hasEvent(evs, agentprotocol.StepBuild, "", ""), "a rollback built:\n%s", describeEvents(evs))
		require.False(t, hasEvent(evs, agentprotocol.StepPush, "", ""), "a rollback pushed:\n%s", describeEvents(evs))
		require.True(t, hasEvent(evs, agentprotocol.StepPull, "", ""), "no pull event:\n%s", describeEvents(evs))

		cs := s.RunningContainers(svc.ID)
		require.Len(t, cs, 1)
		img, err := dockerEng.ImageInspect(context.Background(), cs[0].ImageID)
		require.NoError(t, err)
		require.Contains(t, strings.Join(img.RepoDigests, ","), ref1)
		if removedLocally {
			logs := s.DeploymentLogs(rb.Str("id"))
			require.Contains(t, logs, "Pulling from", "the digest was not local, so the target must have pulled it:\n%s", logs)
			require.NotContains(t, logs, "already present locally", logs)
		} else {
			t.Logf("the target kept a local copy of %s; the pull was a no-op", ref1)
		}
	})

	t.Run("fallback_when_build_server_is_offline", func(t *testing.T) {
		// builderAgent is stopped (previous scenario; Stop is idempotent). Wait
		// until the control plane knows.
		builderAgent.Stop()
		s.waitServerOffline(builderID, 90*time.Second)

		fb := s.NewService(SvcSpec{
			Name: "fallback", GitURL: g.URL, GitBranch: "main", Domain: s.Host("fallback"),
			Vars: map[string]string{"VERSION": "fb"},
			Config: map[string]any{
				"buildLocation": "build_server",
				"registryId":    registryID,
			},
		})

		plan := s.Do("POST", "/services/"+fb.ID+"/deployments/plan", map[string]any{})
		require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
		pl := plan.JSON().Obj("placement")
		require.Equal(t, "target", pl.Str("location"), "plan: %s", plan.Raw)
		require.True(t, pl.Bool("fallback"), "plan: %s", plan.Raw)
		require.Contains(t, string(plan.Raw), "No build server available", "the plan carries the warning")

		d := s.DeployOK(fb.ID)
		evs := s.deploymentEvents(d.Str("id"))
		require.True(t, hasEvent(evs, "placement", "", "No build server available; building on "+targetName),
			"no fallback event:\n%s", describeEvents(evs))

		// Built on the target, like a deployment without build servers: a local
		// image ID, no registry reference, no build server on the deployment.
		src := d.Obj("release").Obj("source")
		require.NotEmpty(t, src.Str("imageId"), "source: %v", src)
		require.Empty(t, src.Str("registryRef"), "source: %v", src)
		require.Nil(t, d["buildServerId"], "deployment: %v", d)
		require.NotEmpty(t, localBuildImages(t, fb.Slug), "the target did not build locally")
		NewProxyClient(s.Host("fallback")).WaitBody(t, "/", "fb", 30*time.Second)
		require.False(t, hasEvent(evs, agentprotocol.StepPush, "", ""), "a fallback build pushed:\n%s", describeEvents(evs))
	})
}
