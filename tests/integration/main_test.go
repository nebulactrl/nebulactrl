//go:build integration

// Package integration runs the control plane and an agent in-process against
// the local Docker daemon and a real Postgres. See tests/README.md.
//
// The suite has two tiers (NEBULACTRL_TEST_TIER=smoke|full, see tier_test.go)
// and two kinds of test:
//
//   - parallel tests call t.Parallel and take SharedStack(t): one control plane,
//     one migrated schema and one agent with Traefik, shared by all of them;
//   - exclusive tests (agent kill/restart, Traefik recreate, server-wide
//     settings, multi-server) never call t.Parallel and take ExclusiveStack(t),
//     a private control plane and agent. `go test` runs them first, one at a
//     time, and releases the parallel group once they are done.
package integration

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Environment switches.
const (
	// envTakeover lets the harness replace a running nebulactrl-traefik (it is a
	// fixed container name shared by every agent on a Docker host). tests/run.sh
	// sets it after stopping the dev agent1.
	envTakeover = "NEBULACTRL_TEST_PROXY_TAKEOVER"
	// envMulti enables the multi-server test (needs the dev `multi` profile's
	// docker-in-docker host; see tests/README.md).
	envMulti = "NEBULACTRL_TEST_MULTI"
)

// Host ports of the harness proxy; chosen free at start (override with
// NEBULACTRL_TEST_HTTP_PORT / _HTTPS_PORT / _ADMIN_PORT).
var proxyPorts struct{ HTTP, HTTPS, Admin int }

// dockerEng inspects and cleans up containers the tests create.
var dockerEng *docker.SDKEngine

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// skip ends the run without running any test. Locally that is a pass (a
// developer without Docker or a database); on CI it is a failure, so a gate
// whose tests all skipped can never count as green.
func skip(format string, args ...any) (int, error) {
	fmt.Fprintf(os.Stderr, "SKIP integration: "+format+"\n", args...)
	if os.Getenv("CI") != "" {
		return 1, errors.New("integration tests skipped on CI")
	}
	return 0, nil
}

func run(m *testing.M) (int, error) {
	t0 := time.Now()
	var err error
	if activeTier, err = parseTier(); err != nil {
		return 1, err
	}
	// `go test -list` only prints the test names (tests/run.sh --shard needs them):
	// no database, no Docker, no harness.
	flag.Parse()
	if f := flag.Lookup("test.list"); f != nil && f.Value.String() != "" {
		return m.Run(), nil
	}
	if os.Getenv(testutil.EnvDatabaseURL) == "" {
		return skip("%s is not set (see tests/README.md)", testutil.EnvDatabaseURL)
	}
	eng, err := docker.NewSDKEngine()
	if err != nil {
		return skip("docker client: %v", err)
	}
	defer eng.Close()
	pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = eng.Ping(pctx)
	cancel()
	if err != nil {
		return skip("docker daemon unreachable: %v", err)
	}

	// Every git fixture (the git:// server, the ssh server, a real or fake Gitea) listens on
	// 127.0.0.1. The in-process control plane refuses loopback git remotes and provider URLs
	// unless the instance admin allows them (internal/netguard reads this per connection);
	// this allows exactly that address. The agent has no such guard: it clones what the
	// control plane resolved.
	if err := os.Setenv(netguard.AllowEnv, "127.0.0.1"); err != nil {
		return 1, err
	}
	dockerEng = eng

	// The proxy container name and the edge network are fixed per Docker host.
	// Running next to the dev agent1 would make two agents fight over one
	// Traefik, so refuse unless explicitly told it was stopped.
	info, err := eng.ContainerInspect(context.Background(), agentprotocol.ProxyContainer)
	switch {
	case err == nil && info.Running && os.Getenv(envTakeover) != "1":
		return skip("container %s is running and is not owned by the harness.\n"+
			"Stop the dev agent (docker compose -f deploy/dev/docker-compose.yml --profile agent stop agent1)\n"+
			"and remove it, or run `make test-smoke` / `make test-full`, which do that and restore it afterwards.", agentprotocol.ProxyContainer)
	case err != nil && !errors.Is(err, docker.ErrNotFound):
		return 1, fmt.Errorf("inspect %s: %w", agentprotocol.ProxyContainer, err)
	}

	if proxyPorts.HTTP, err = portFromEnv("NEBULACTRL_TEST_HTTP_PORT", 18880); err != nil {
		return 1, err
	}
	if proxyPorts.HTTPS, err = portFromEnv("NEBULACTRL_TEST_HTTPS_PORT", 18443); err != nil {
		return 1, err
	}
	if proxyPorts.Admin, err = portFromEnv("NEBULACTRL_TEST_ADMIN_PORT", 19000); err != nil {
		return 1, err
	}

	nctx, ncancel := context.WithTimeout(context.Background(), 30*time.Second)
	netSlots = newSlotLimiter(maxNetworks(nctx))
	ncancel()

	// Everything slow and shared starts at once: the image pre-pull, the fixture
	// app image with its registry, and the shared database + control plane.
	stopHarness, err := bootstrap(eng)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(os.Stderr, "integration: tier %s ready after %s (network slots: %d, 0 = unbounded)\n",
		activeTier, time.Since(t0).Round(time.Second), netSlots.limit)

	code := m.Run()

	// The harness owns the proxy container now; leave the host as found.
	stopHarness()
	cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer ccancel()
	_ = eng.ContainerRemove(cctx, agentprotocol.ProxyContainer, true, false)
	_ = eng.ContainerRemove(cctx, agentprotocol.PagesContainer, true, false)
	sweepEnvNetworks(cctx)
	teardownFixtures(cctx)
	return code, nil
}

// bootstrap runs the one-time setup in parallel and returns the teardown of the
// shared control plane and database.
func bootstrap(eng *docker.SDKEngine) (func(), error) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		errs   []error
		stopCP = func() {}
		fail   = func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		prePull(eng, tierImages())
	}()
	go func() {
		defer wg.Done()
		fixApp.once.Do(func() { fixApp.err = fixApp.buildApp() })
		if fixApp.err != nil {
			fail(fmt.Errorf("fixture app: %w", fixApp.err))
			return
		}
		if activeTier == tierFull {
			gitOnce.Do(func() { gitFix, gitErr = buildGit(&fixApp) })
			if gitErr != nil {
				fail(fmt.Errorf("fixture git server: %w", gitErr))
			}
		}
	}()
	go func() {
		defer wg.Done()
		h, stop, err := startHarness()
		if err != nil {
			sharedErr = err
			fail(fmt.Errorf("shared harness: %w", err))
			return
		}
		shared, stopCP = h, stop
	}()
	wg.Wait()
	if len(errs) > 0 {
		stopCP()
		return nil, errors.Join(errs...)
	}
	return stopCP, nil
}

// openSharedSchema creates the shared, migrated schema.
func openSharedSchema() (*pgxpool.Pool, func(), error) {
	return testutil.OpenSchema(context.Background())
}

// tierImages lists the images the selected tier pulls from a registry. The
// agent pulls Traefik and the pages server on first start; the fixtures pull
// registry:2 and alpine. Pulling them up front, in parallel, replaces many
// serial pulls inside individual tests.
func tierImages() []string {
	imgs := []string{"registry:2", proxy.TraefikImage, proxy.PagesImage}
	if activeTier == tierFull {
		imgs = append(imgs,
			"alpine:3",
			"quay.io/prometheus/busybox:latest",
			"postgres:17", "valkey/valkey:9",
			seaweedImage, giteaImage,
			agentprotocol.RailpackFrontendImage, agentprotocol.BuildKitImage, agentprotocol.ResticImage, externalRegistryImage,
		)
		if os.Getenv(envMulti) == "1" {
			imgs = append(imgs, "traefik/whoami:latest", "docker:29-dind")
		}
		if os.Getenv(envMesh) == "1" {
			imgs = append(imgs, platform.ImageHeadscale, proxy.TailscaleImage)
		}
	}
	return imgs
}

// prePull pulls the images that are not on the host yet, four at a time. A
// failed pull is only reported: the test that needs the image fails with the
// real error, and tests that do not need it are unaffected.
func prePull(eng *docker.SDKEngine, images []string) {
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, img := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if _, err := eng.ImageInspect(ctx, img); err == nil {
				return
			}
			if _, err := sh(ctx, "", "docker", "pull", "-q", img); err != nil {
				fmt.Fprintf(os.Stderr, "integration: pre-pull %s: %v\n", img, err)
			}
		}()
	}
	wg.Wait()
}

// portFromEnv returns the env override, else def when it is free, else any free port.
func portFromEnv(name string, def int) (int, error) {
	if v := os.Getenv(name); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err != nil || p <= 0 || p > 65535 {
			return 0, fmt.Errorf("%s=%q is not a port", name, v)
		}
		return p, nil
	}
	if portFree(def) {
		return def, nil
	}
	return freePort()
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	_ = l.Close()
	// Docker publishes on all interfaces; check those too.
	l, err = net.Listen("tcp", fmt.Sprintf(":%d", p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// sh runs a command and returns trimmed combined output.
func sh(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

func requireDocker(t testing.TB) *docker.SDKEngine {
	t.Helper()
	if dockerEng == nil {
		t.Skip("docker not available")
	}
	return dockerEng
}
