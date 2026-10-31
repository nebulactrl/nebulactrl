//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/internal/agent/decommission"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/internal/agent/loghistory"
	"github.com/nebulactrl/nebulactrl/internal/agent/mesh"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/stats"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/internal/agent/traffic"
	agentuptime "github.com/nebulactrl/nebulactrl/internal/agent/uptime"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ProbeFault is a test-only fault injector for the proxy route probe (the
// "verify" step of a deploy). It exists only in this package: the production
// agent has no flag for it. Faults are per hostname, so a test can break the
// probe of its own service on the shared agent without touching the deploys of
// tests running in parallel (every test has its own hostnames). Hosts without
// a fault pass through to the real Traefik.
type ProbeFault struct {
	mu    sync.Mutex
	hosts map[string]int
	hangs map[string]bool
}

// Hang makes every probe of host block until its context ends (the deploy is
// canceled or the agent stops): the verify step never finishes by itself.
func (f *ProbeFault) Hang(host string) {
	f.mu.Lock()
	if f.hangs == nil {
		f.hangs = map[string]bool{}
	}
	f.hangs[host] = true
	f.mu.Unlock()
}

// Unhang lets the probes of host through again.
func (f *ProbeFault) Unhang(host string) {
	f.mu.Lock()
	delete(f.hangs, host)
	f.mu.Unlock()
}

func (f *ProbeFault) hanging(host string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hangs[host]
}

// Fail makes every probe of host answer HTTP code.
func (f *ProbeFault) Fail(host string, code int) {
	f.mu.Lock()
	if f.hosts == nil {
		f.hosts = map[string]int{}
	}
	f.hosts[host] = code
	f.mu.Unlock()
}

// Clear removes the fault of host.
func (f *ProbeFault) Clear(host string) {
	f.mu.Lock()
	delete(f.hosts, host)
	f.mu.Unlock()
}

func (f *ProbeFault) check(host, _ string) (int, error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	code, ok := f.hosts[host]
	return code, nil, ok
}

// faultProxy is the real proxy.Manager with an injectable Probe.
type faultProxy struct {
	*proxy.Manager
	fault *ProbeFault
}

func (p faultProxy) Probe(ctx context.Context, host, path string) (int, error) {
	if p.fault.hanging(host) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if code, err, ok := p.fault.check(host, path); ok {
		return code, err
	}
	return p.Manager.Probe(ctx, host, path)
}

// AgentOptions configure an in-process agent.
type AgentOptions struct {
	StateDir string
	URL      string // control plane base URL (http://127.0.0.1:port)
	Token    string // enrollment token; unused once an identity is stored
	Fault    *ProbeFault
	// OrphanMinAge keeps the orphan sweep away from containers this agent
	// never deployed (the dev agent1's services share the Docker daemon).
	OrphanMinAge time.Duration
	Logger       *slog.Logger
	// CleanupTune scopes and times the agent's Docker cleanup (a test shares
	// the daemon with the developer's own images).
	CleanupTune engine.CleanupTuner
	// DecommissionTune scopes the agent's decommission to the test's own
	// resources (the harness shares the daemon with the developer's containers).
	DecommissionTune func(*decommission.Config)
}

// Agent is the production agent wiring (cmd/agent run) in-process.
type Agent struct {
	opt    AgentOptions
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	runErr error

	Identity store.Identity
}

// StartAgent enrolls (when the state dir holds no identity) and starts the
// agent. It returns once the agent's goroutines are running.
func StartAgent(ctx context.Context, opt AgentOptions) (*Agent, error) {
	if opt.Fault == nil {
		opt.Fault = &ProbeFault{}
	}
	if opt.OrphanMinAge == 0 {
		opt.OrphanMinAge = 24 * time.Hour
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		if os.Getenv("NEBULACTRL_TEST_AGENT_LOGS") != "" {
			opt.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("service", "agent")
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
	a := &Agent{opt: opt, done: make(chan struct{})}
	cleanupEarly := func() { _ = st.Close(); _ = eng.Close() }

	px, err := proxy.NewManager(proxy.Config{
		Engine:    eng,
		Store:     st,
		AdminPort: proxyPorts.Admin,
		HTTPPort:  proxyPorts.HTTP,
		HTTPSPort: proxyPorts.HTTPS,
		Logger:    log,
		Pages:     true,
	})
	if err != nil {
		cleanupEarly()
		return nil, err
	}
	composeRunner := compose.New()
	// The server mesh nodes, wired like cmd/agent: observed by heartbeats,
	// driven by the engine. Link nodes need /dev/net/tun of the daemon's host
	// (the test process may not be on it): NEBULACTRL_TUN_DEVICE=1 states it.
	meshMgr := mesh.New(eng, log)
	meshMgr.TunDevice = stats.TunDevice
	collector := stats.New(stats.Options{
		Engine: eng, StateDir: opt.StateDir,
		HTTPPort: proxyPorts.HTTP, HTTPSPort: proxyPorts.HTTPS, ProxyStatus: px.Status,
		ComposeVersion: composeRunner.Version, MeshStatus: meshMgr.Status,
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
	logHistory, err := loghistory.New(eng, opt.StateDir, client, log)
	if err != nil {
		cancel()
		cleanupEarly()
		return nil, err
	}
	// Uptime probes, wired like cmd/agent. The probe period is the harness's
	// NEBULACTRL_UPTIME_INTERVAL (2 s; harness_timers_test.go), the same value the
	// control plane derives its "unknown" rule from.
	uptimeEvery := harnessTimers().UptimeInterval
	uptimeCollector := agentuptime.NewCollector(agentuptime.Config{Buffer: st, Sender: client, Logger: log, Interval: uptimeEvery})
	deployer, err := engine.New(engine.Config{
		Logs:   logHistory,
		Docker: wrapPull(eng), Proxy: faultProxy{Manager: px, fault: opt.Fault},
		Builder: &build.Builder{StateDir: opt.StateDir}, Sender: client, Logger: log,
		Uptime: uptimeCollector, UptimeInterval: uptimeEvery,
		OrphanMinAge:     opt.OrphanMinAge,
		BackupDir:        filepath.Join(opt.StateDir, "backups"),
		CleanupTune:      opt.CleanupTune,
		DecommissionTune: opt.DecommissionTune,
		Compose:          composeRunner,
		Mesh:             meshMgr,
		StackDir:         filepath.Join(opt.StateDir, agentprotocol.StackDirName),
	})
	if err != nil {
		cancel()
		cleanupEarly()
		return nil, err
	}
	client.SetHandler(deployer)
	client.SetReconciler(deployer)
	tc := traffic.NewCollector(traffic.Config{Engine: eng, Buffer: st, Sender: client, Container: agentprotocol.ProxyContainer, Logger: log})
	client.SetTrafficAckHandler(tc.Ack)
	client.SetProbesAckHandler(uptimeCollector.Ack)

	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){px.Run, deployer.Run, tc.Run, logHistory.Run, uptimeCollector.Run} {
		wg.Add(1)
		go func() { defer wg.Done(); fn(rctx) }()
	}
	go func() {
		defer close(a.done)
		err := client.Run(rctx)
		a.mu.Lock()
		a.runErr = err
		a.mu.Unlock()
		if errors.Is(err, conn.ErrRevoked) {
			identity.Revoked = true
			_ = st.PutIdentity(identity)
		}
		cancel()
		wg.Wait()
		cleanupEarly()
	}()
	return a, nil
}

// Stop cancels the agent and waits until everything it owns is closed. It
// does not touch containers (a stopped agent leaves its workloads running).
func (a *Agent) Stop() {
	a.cancel()
	select {
	case <-a.done:
	case <-time.After(60 * time.Second):
		panic("agent did not stop within 60s")
	}
}

// Err is the error client.Run returned (nil after Stop).
func (a *Agent) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runErr
}

// Done is closed once the agent has exited (revocation, Stop).
func (a *Agent) Done() <-chan struct{} { return a.done }

// MustStartAgent is StartAgent that fails the test; it stops the agent at cleanup.
func MustStartAgent(t testing.TB, opt AgentOptions) *Agent {
	t.Helper()
	a, err := StartAgent(context.Background(), opt)
	if err != nil {
		t.Fatalf("start agent: %v", err)
	}
	t.Cleanup(a.Stop)
	return a
}

// LiteAgent is a control-plane connection without Docker or Traefik: it enrolls
// with a token, authenticates with its Ed25519 identity and heartbeats a healthy
// proxy, so the server looks online. It is for tests about the connection itself
// (enrollment, revocation); it deploys nothing, so any number of them can run
// next to the shared agent.
type LiteAgent struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	runErr error

	Identity store.Identity
}

// StartLiteAgent enrolls with token and starts connecting to url. The agent
// stops when the test ends.
func StartLiteAgent(t testing.TB, url, token string) *LiteAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	eps, err := conn.ParseEndpoints(url, true)
	if err != nil {
		cancel()
		t.Fatalf("lite agent: %v", err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		cancel()
		t.Fatalf("lite agent: %v", err)
	}
	facts := agentprotocol.Facts{OS: "linux", Arch: "amd64", CPUs: 1, DockerVersion: "itest", CgroupVersion: "2"}
	identity, err := conn.Enroll(ctx, st, eps.Enroll, token, "lite-agent", facts, nil)
	if err != nil {
		cancel()
		_ = st.Close()
		t.Fatalf("lite agent enroll: %v", err)
	}
	client := conn.New(conn.Config{
		URL: eps.Connect, Identity: identity, Store: st, Version: "itest-lite",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Facts:  func(context.Context) agentprotocol.Facts { return facts },
		Heartbeat: func(context.Context) agentprotocol.Heartbeat {
			return agentprotocol.Heartbeat{DockerVersion: "itest", Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK}}
		},
		HeartbeatInterval: time.Second,
	})
	a := &LiteAgent{cancel: cancel, done: make(chan struct{}), Identity: identity}
	go func() {
		defer close(a.done)
		err := client.Run(ctx)
		a.mu.Lock()
		a.runErr = err
		a.mu.Unlock()
		_ = st.Close()
	}()
	t.Cleanup(func() {
		cancel()
		<-a.done
	})
	return a
}

// Done is closed once the agent has exited (revocation, test end).
func (a *LiteAgent) Done() <-chan struct{} { return a.done }

// Err is the error the connection ended with.
func (a *LiteAgent) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runErr
}
