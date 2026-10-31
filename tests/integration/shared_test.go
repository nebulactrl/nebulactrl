//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The shared harness: TestMain starts one migrated schema and one control plane
// (see run in main_test.go) and every parallel test gets a view onto it from
// SharedStack, with its own project and environment. All of them talk to one
// agent and one Traefik.
//
// The agent starts on first use rather than in TestMain: tests that need a
// private agent (ExclusiveStack) never call t.Parallel, so `go test` runs them
// first and to completion, and only then releases the parallel group. Starting
// the shared agent that late means it never fights an exclusive test's agent
// over the fixed-name Traefik container.
//
// Isolation between parallel tests rests on three things:
//   - names: every test has its own project, so service slugs never clash, and
//     its own hostnames (Stack.Host: <test>-<rand>.localhost);
//   - networks: every environment has its own Docker network, so one test's
//     services cannot reach another's;
//   - state: nothing in a shared test changes server-wide settings, restarts
//     the agent or recreates Traefik. Tests that do are exclusive.
type harness struct {
	pool     *pgxpool.Pool
	cp       *controlPlane
	stateDir string
	serverID string
	enrollTk string
	fault    *ProbeFault

	mu    sync.Mutex
	agent *Agent
}

var shared *harness

// sharedErr is why the shared harness is unavailable, if it is.
var sharedErr error

// startHarness creates the shared schema, control plane and the (still
// pending) server.
func startHarness() (*harness, func(), error) {
	pool, closeDB, err := openSharedSchema()
	if err != nil {
		return nil, nil, err
	}
	cp, err := startControlPlane(pool, false)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	h := &harness{pool: pool, cp: cp, fault: &ProbeFault{}}
	h.stateDir, err = os.MkdirTemp("", "nebulactrl-itest-agent-")
	if err != nil {
		cp.stop()
		closeDB()
		return nil, nil, err
	}
	status, raw, err := cp.do("POST", "/servers/enrollment-tokens", map[string]any{"name": "shared-host"})
	if err != nil || status != http.StatusCreated {
		cp.stop()
		closeDB()
		return nil, nil, fmt.Errorf("enrollment token: HTTP %d err=%v: %s", status, err, raw)
	}
	var tok struct {
		Token  string `json:"token"`
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		cp.stop()
		closeDB()
		return nil, nil, err
	}
	h.serverID, h.enrollTk = tok.Server.ID, tok.Token
	return h, func() {
		h.stopAgent()
		cp.stop()
		closeDB()
		_ = os.RemoveAll(h.stateDir)
	}, nil
}

// ensureAgent starts the shared agent if it is not running and waits until its
// server is online with a healthy proxy.
func (h *harness) ensureAgent() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agent != nil {
		select {
		case <-h.agent.Done():
			h.agent = nil
		default:
			return nil
		}
	}
	a, err := StartAgent(context.Background(), AgentOptions{
		StateDir: h.stateDir, URL: h.cp.Srv.URL, Token: h.enrollTk, Fault: h.fault,
	})
	if err != nil {
		return fmt.Errorf("start shared agent: %w", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		status, raw, err := h.cp.do("GET", "/servers/"+h.serverID, nil)
		if err == nil && status == http.StatusOK {
			var srv struct {
				Status string `json:"status"`
				Proxy  struct {
					Status string `json:"status"`
				} `json:"proxy"`
			}
			if json.Unmarshal(raw, &srv) == nil && srv.Status == "online" && srv.Proxy.Status == agentprotocol.ProxyOK {
				break
			}
		}
		if time.Now().After(deadline) {
			a.Stop()
			return fmt.Errorf("shared server did not come online with a healthy proxy: %s", raw)
		}
		time.Sleep(300 * time.Millisecond)
	}
	h.agent = a
	return nil
}

// stopAgent stops the shared agent (its workloads keep running).
func (h *harness) stopAgent() {
	h.mu.Lock()
	a := h.agent
	h.agent = nil
	h.mu.Unlock()
	if a != nil {
		a.Stop()
	}
}

// stopSharedAgent makes sure no shared agent runs. ExclusiveStack calls it so
// an exclusive test never shares Traefik with another agent, even when the
// binary runs the suite more than once (-count).
func stopSharedAgent() {
	if shared != nil {
		shared.stopAgent()
	}
}

// envMaxNetworks bounds how many Docker networks the parallel tests keep alive at
// once (see netSlots). NEBULACTRL_TEST_MAX_NETWORKS overrides the computed limit:
// 0 = unbounded, which is what everything but macOS gets.
//
// Why: the agent runs on the host and probes a new container through its IP on
// its environment network. OrbStack (and Docker Desktop) back every Docker
// network with a vmnet bridge on the Mac, and macOS only has a handful of them:
// bridge101-bridge108 here, so eight Docker networks including the default
// "bridge". A network created once they are used up has no route from the host,
// so the health check of every container on it times out. The limit is
// therefore what is left of those eight when the run starts (the dev stack, the
// edge network and other projects use some).
const envMaxNetworks = "NEBULACTRL_TEST_MAX_NETWORKS"

// macBridges is the number of Docker networks OrbStack can route to the host.
const macBridges = 8

// slotLimiter hands out a bounded number of network slots; a test asks for as
// many as its environments need (an environment is one network, plus an
// internal one when a service blocks egress) before it creates them, and gives
// them back once its networks are removed. Waiters are served one at a time, so
// a test that needs all slots is not starved by a stream of small ones.
type slotLimiter struct {
	limit int
	gate  sync.Mutex // one waiter at a time
	mu    sync.Mutex
	cond  *sync.Cond
	free  int
}

func newSlotLimiter(limit int) *slotLimiter {
	l := &slotLimiter{limit: limit, free: limit}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *slotLimiter) acquire(n int) int {
	if l.limit <= 0 || n <= 0 {
		return 0
	}
	n = min(n, l.limit)
	l.gate.Lock()
	defer l.gate.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.free < n {
		l.cond.Wait()
	}
	l.free -= n
	return n
}

func (l *slotLimiter) release(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	l.free += n
	l.mu.Unlock()
	l.cond.Broadcast()
}

// netSlots is set by TestMain (once Docker is known to be there).
var netSlots = newSlotLimiter(0)

func maxNetworks(ctx context.Context) int {
	if v := os.Getenv(envMaxNetworks); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			return n
		}
	}
	if runtime.GOOS != "darwin" {
		return 0
	}
	out, err := sh(ctx, "", "docker", "network", "ls", "--format", "{{.Name}}")
	if err != nil {
		return 1
	}
	used, haveEdge := 0, false
	for _, name := range strings.Fields(out) {
		switch name {
		case "host", "none": // no bridge
		default:
			used++
		}
		haveEdge = haveEdge || name == agentprotocol.EdgeNetwork
	}
	if !haveEdge {
		used++ // the agent creates it
	}
	return max(1, macBridges-used)
}

// SharedStack returns a view onto the shared control plane and agent with a
// project and environment of the test's own; it counts for one network slot.
// Call t.Parallel first.
func SharedStack(t *testing.T) *Stack { return SharedStackN(t, 1) }

// SharedStackN is SharedStack for a test that keeps `networks` Docker networks
// alive: one per environment it deploys into, plus one for every environment
// with a service that blocks egress (managed databases do by default).
func SharedStackN(t *testing.T, networks int) *Stack {
	t.Helper()
	requireDocker(t)
	if sharedErr != nil {
		t.Fatalf("shared harness: %v", sharedErr)
	}
	if shared == nil {
		t.Skip("shared harness not running")
	}
	slots := netSlots.acquire(networks)
	if err := shared.ensureAgent(); err != nil {
		netSlots.release(slots)
		t.Fatalf("shared harness: %v", err)
	}
	s := &Stack{
		t: t, Pool: shared.pool, App: shared.cp.App, Srv: shared.cp.Srv, client: shared.cp.Client,
		Agent: shared.currentAgent(), StateDir: shared.stateDir, Fault: shared.fault,
		ServerID: shared.serverID, EnrollTok: shared.enrollTk,
		shared: true, hostBase: hostBase(t.Name()), netSlots: slots,
	}
	t.Cleanup(func() { netSlots.release(s.netSlots) }) // after cleanupShared (LIFO)
	s.createProject("itest-" + s.hostBase)
	t.Cleanup(s.cleanupShared)
	return s
}

func (h *harness) currentAgent() *Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agent
}

// hostBase turns a test name into one DNS label (<= 63 chars) plus a random
// suffix: "TestIDOR" -> "idor-3fa91c".
func hostBase(name string) string {
	name = strings.ToLower(strings.TrimPrefix(name, "Test"))
	var b strings.Builder
	dash := true
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "t"
	}
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	return base + "-" + hex.EncodeToString(rnd[:])
}

// testEnvs records every environment a test created, so the run can remove
// their Docker networks at the end.
var testEnvs sync.Map

func trackEnv(id string) { testEnvs.Store(id, struct{}{}) }

// TrackEnv registers an environment created by a test through the API (a
// second project, say) so cleanup deletes its services and networks too.
func (s *Stack) TrackEnv(envID string) {
	trackEnv(envID)
	s.servicesMu.Lock()
	s.extraEnvs = append(s.extraEnvs, envID)
	s.servicesMu.Unlock()
}

// removeEnvNetworks removes an environment's networks. Traefik is attached to
// the networks of the services it routes and lets go of them only when the
// agent next syncs its routes, so it is disconnected first: a network that
// lingers keeps its bridge (see envMaxNetworks).
func removeEnvNetworks(ctx context.Context, envID string) {
	for _, n := range []string{agentprotocol.EnvInternalNetwork(envID), agentprotocol.EnvNetwork(envID)} {
		if _, err := sh(ctx, "", "docker", "network", "inspect", n); err != nil {
			continue // never created
		}
		_, _ = sh(ctx, "", "docker", "network", "disconnect", "-f", n, agentprotocol.ProxyContainer)
		_, _ = sh(ctx, "", "docker", "network", "rm", n)
	}
}

// sweepEnvNetworks removes the networks of every environment the run created.
// The proxy is already gone, so nothing is attached any more.
func sweepEnvNetworks(ctx context.Context) {
	testEnvs.Range(func(k, _ any) bool {
		removeEnvNetworks(ctx, k.(string))
		return true
	})
}

// cleanupShared deletes the test's services through the API, so the control
// plane stops supervising them and the agent removes containers and volumes,
// and then makes sure nothing is left on the Docker host. Removing containers
// behind the control plane's back would make its reconciler bring them back.
func (s *Stack) cleanupShared() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s.servicesMu.Lock()
	envs := append([]string{s.EnvID}, s.extraEnvs...)
	ids := append([]string(nil), s.serviceIDs...)
	s.servicesMu.Unlock()

	slugs := map[string]string{}
	for _, id := range ids {
		slugs[id] = ""
	}
	for _, env := range envs {
		r := s.do(s.client, "", "GET", "/environments/"+env+"/services", nil)
		if r.Status == http.StatusOK {
			for _, it := range r.JSON().Items() {
				slugs[it.Str("id")] = it.Str("slug")
			}
		}
	}
	for id, slug := range slugs {
		if slug == "" {
			if r := s.do(s.client, "", "GET", "/services/"+id, nil); r.Status == http.StatusOK {
				slugs[id] = r.JSON().Str("slug")
			}
		}
	}
	// A replica is removed through its own saga (a plain delete is refused with
	// replica_remove_required), and a primary with replicas cannot be deleted
	// (replicas_exist). A refused delete would leave the service desired, and the
	// reconciler would bring its container back after the sweep below: take the
	// replicas out first and wait for their rows to go.
	var replicaIDs []string
	for id := range slugs {
		var isReplica bool
		if err := s.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM database_replicas WHERE service_id::text = $1)", id).Scan(&isReplica); err == nil && isReplica {
			replicaIDs = append(replicaIDs, id)
			s.cancelActive(id)
			s.do(s.client, "", "DELETE", "/services/"+id+"/database/replica", nil)
		}
	}
	if len(replicaIDs) > 0 {
		eventually(2*time.Minute, time.Second, func() bool {
			var n int
			err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM database_replicas WHERE service_id::text = ANY($1::text[])", replicaIDs).Scan(&n)
			return err == nil && n == 0
		})
	}
	pending := map[string]string{}
	for id, slug := range slugs {
		if slug != "" { // "" = already deleted
			pending[id] = slug
			s.cancelActive(id)
		}
	}
	for pass := 0; pass < 4 && len(pending) > 0; pass++ {
		for id, slug := range pending {
			r := s.do(s.client, "", "DELETE", "/services/"+id+"?confirm="+slug+"&deleteVolumes=true", nil)
			if r.Status < 300 || r.Status == http.StatusNotFound {
				delete(pending, id)
			}
		}
		if len(pending) > 0 {
			time.Sleep(2 * time.Second)
		}
	}

	// The agent removes containers and volumes asynchronously.
	eventually(60*time.Second, 500*time.Millisecond, func() bool {
		for id := range slugs {
			cs, err := dockerEng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{agentprotocol.LabelService: id}})
			if err == nil && len(cs) > 0 {
				return false
			}
		}
		return true
	})
	for id := range slugs {
		cs, _ := dockerEng.ContainerList(ctx, docker.ListFilter{All: true, Labels: map[string]string{agentprotocol.LabelService: id}})
		for _, c := range cs {
			_ = dockerEng.ContainerRemove(ctx, c.ID, true, false)
		}
		if vols, err := sh(ctx, "", "docker", "volume", "ls", "-q", "--filter", "name=^"+agentprotocol.VolumePrefix+agentprotocol.Short(id)+"-"); err == nil {
			for _, v := range strings.Fields(vols) {
				_, _ = sh(ctx, "", "docker", "volume", "rm", "-f", v)
			}
		}
	}
	for _, env := range envs {
		removeEnvNetworks(ctx, env)
	}
}

// cancelActive cancels a service's in-flight deployments (a service with an
// active deployment cannot be deleted) and waits for them to settle.
func (s *Stack) cancelActive(serviceID string) {
	r := s.do(s.client, "", "GET", "/services/"+serviceID+"/deployments", nil)
	if r.Status != http.StatusOK {
		return
	}
	for _, d := range r.JSON().Items() {
		if terminal(d.Str("status")) {
			continue
		}
		id := d.Str("id")
		s.do(s.client, "", "POST", "/deployments/"+id+"/cancel", nil)
		eventually(60*time.Second, 500*time.Millisecond, func() bool {
			dr := s.do(s.client, "", "GET", "/deployments/"+id, nil)
			return dr.Status != http.StatusOK || terminal(dr.JSON().Str("status"))
		})
	}
}
