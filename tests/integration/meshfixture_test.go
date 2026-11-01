//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The mesh fixture: a real, pinned Headscale container
// (policy.mode: database, Tailscale's public DERP map) reachable from both Docker daemons,
// agent1 (in-process, the local daemon) and agent2 (the dev `multi` profile's
// docker-in-docker host2), with the mesh enabled on both. Nothing here is
// faked: the server nodes, the link nodes, the ACL and the replication or HTTP
// traffic are real. The tests use host2 for everything that must run on "the
// other server".
//
// Needs NEBULACTRL_TEST_MULTI=1 and NEBULACTRL_TEST_MESH=1. A run that cannot
// start link nodes (no /dev/net/tun in the daemon, a Docker that refuses
// NET_ADMIN) FAILS with the node's own log instead of skipping, so a gap is
// reported as "not verified" and never mistaken for a pass.

// envMesh enables the mesh tests (they pull Headscale and Tailscale images and
// start docker-in-docker, so they are not part of the default suite).
const envMesh = "NEBULACTRL_TEST_MESH"

// envMeshHostIP overrides the address every container (and the test process)
// uses to reach the Headscale container.
const envMeshHostIP = "NEBULACTRL_TEST_MESH_HOST_IP"

const (
	meshHeadscaleName = "nebulactrl-itest-headscale"
	// host2Registry is a registry inside host2 (its loopback is host2's, so its
	// daemon treats it as insecure and the engine's "always pull" works there).
	host2Registry     = "127.0.0.1:5000"
	host2RegistryName = "nebulactrl-itest-registry"
	// host2HTTPS is where compose publishes host2's Traefik on the test machine.
	host2HTTPS = "127.0.0.1:9443"
	meshTag    = "tag:nebula-mesh"
)

// requireMesh skips unless the mesh switches are set.
func requireMesh(t testing.TB) {
	t.Helper()
	requireTier(t, tierFull)
	if os.Getenv(envMulti) != "1" || os.Getenv(envMesh) != "1" {
		t.Skipf("set %s=1 and %s=1 to run the mesh tests (they start Headscale and the dev `multi` profile's host2)", envMulti, envMesh)
	}
}

// ---- docker hosts ------------------------------------------------------------

// dockerHost runs docker commands against one daemon: the local one, or host2's
// (through `docker exec` on the docker-in-docker container, whose CLI talks to
// its own daemon).
type dockerHost struct {
	name   string
	prefix []string
}

var (
	localHost = dockerHost{name: "local", prefix: []string{"docker"}}
	host2Host = dockerHost{name: "host2", prefix: []string{"docker", "exec", host2Name, "docker"}}
)

func (h dockerHost) docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return sh(ctx, "", h.prefix[0], append(append([]string{}, h.prefix[1:]...), args...)...)
}

func (h dockerHost) mustDocker(t testing.TB, args ...string) string {
	t.Helper()
	out, err := h.docker(args...)
	require.NoError(t, err, "%s: docker %s\n%s", h.name, strings.Join(args, " "), out)
	return out
}

// containerOf returns the id of a running container of a service ("" if none).
func (h dockerHost) containerOf(serviceID string) string {
	out, err := h.docker("ps", "-q", "--filter", "label="+agentprotocol.LabelService+"="+serviceID, "--filter", "status=running")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Split(out, "\n")[0])
}

// waitContainer waits for a running container of the service.
func (h dockerHost) waitContainer(t testing.TB, serviceID string, timeout time.Duration) string {
	t.Helper()
	var id string
	require.Truef(t, eventually(timeout, time.Second, func() bool { id = h.containerOf(serviceID); return id != "" }),
		"no running container of service %s on %s", serviceID, h.name)
	return id
}

// exec runs argv in a container; a non-zero exit is an error.
func (h dockerHost) exec(container string, argv ...string) (string, error) {
	return h.docker(append([]string{"exec", container}, argv...)...)
}

// inspect is `docker inspect -f format ref` on this host.
func (h dockerHost) inspect(t testing.TB, ref, format string) string {
	t.Helper()
	return strings.TrimSpace(h.mustDocker(t, "inspect", "-f", format, ref))
}

// psql runs a statement as postgres in the database `app` of a service's container.
func (h dockerHost) psql(t testing.TB, serviceID, sql string) string {
	t.Helper()
	c := h.waitContainer(t, serviceID, 60*time.Second)
	out, err := h.exec(c, "psql", "-U", "postgres", "-d", "app", "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql)
	require.NoError(t, err, "%s psql %q:\n%s", h.name, sql, out)
	return strings.TrimSpace(out)
}

// tryPsql is psql that reports failure instead of failing the test.
func (h dockerHost) tryPsql(serviceID, sql string) (string, error) {
	c := h.containerOf(serviceID)
	if c == "" {
		return "", fmt.Errorf("no running container of %s on %s", serviceID, h.name)
	}
	out, err := h.exec(c, "psql", "-U", "postgres", "-d", "app", "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql)
	return strings.TrimSpace(out), err
}

// ---- Headscale ---------------------------------------------------------------

type headscaleFixture struct {
	Name     string
	LoginURL string // what nodes and the control plane dial: http://<host IP>:<port>
	APIURL   string // what the test dials: the published port on loopback
	APIKey   string
	port     int
	hostIP   string
	volume   string
}

// startHeadscale runs the pinned Headscale of the platform catalog with
// deploy/dev/headscale/config.yaml, published on every interface, and returns it
// once its API answers. hostIP is the address containers use to reach it.
func startHeadscale(t testing.TB, root, hostIP string, port int) *headscaleFixture {
	t.Helper()
	cfg := filepath.Join(root, "deploy/dev/headscale/config.yaml")
	_, err := os.Stat(cfg)
	require.NoError(t, err)
	hs := &headscaleFixture{
		Name: meshHeadscaleName, port: port, hostIP: hostIP,
		volume:   meshHeadscaleName + "-data",
		LoginURL: fmt.Sprintf("http://%s:%d", hostIP, port),
		APIURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
	}
	removeHeadscale(hs.Name, hs.volume)
	mustSh(t, "docker", "run", "-d", "--name", hs.Name, "--label", "nebulactrl.itest=mesh",
		"-p", fmt.Sprintf("0.0.0.0:%d:%d", port, port),
		"-v", cfg+":/etc/headscale/config.yaml:ro",
		"-v", hs.volume+":/var/lib/headscale",
		"-e", "HEADSCALE_SERVER_URL="+hs.LoginURL,
		"-e", fmt.Sprintf("HEADSCALE_LISTEN_ADDR=0.0.0.0:%d", port),
		platform.ImageHeadscale, "serve")
	t.Cleanup(func() { removeHeadscale(hs.Name, hs.volume) })
	require.Truef(t, eventually(90*time.Second, time.Second, func() bool {
		res, err := http.Get(hs.APIURL + "/health")
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	}), "Headscale did not become healthy:\n%s", mustShLoose("docker", "logs", "--tail", "60", hs.Name))
	hs.APIKey = strings.TrimSpace(mustSh(t, "docker", "exec", hs.Name, "headscale", "apikeys", "create", "--expiration", "24h"))
	require.NotEmpty(t, hs.APIKey)
	return hs
}

func removeHeadscale(name, volume string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = sh(ctx, "", "docker", "rm", "-f", name)
	_, _ = sh(ctx, "", "docker", "volume", "rm", "-f", volume)
}

// mustShLoose runs a command and returns its output whatever happens (diagnostics).
func mustShLoose(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, _ := sh(ctx, "", name, args...)
	return out
}

// api calls Headscale's REST API with the key.
func (h *headscaleFixture) api(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.APIURL+"/api/v1"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, err
}

// hsNode is a Headscale node as the test needs it.
type hsNode struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	GivenName   string   `json:"givenName"`
	IPAddresses []string `json:"ipAddresses"`
	Online      bool     `json:"online"`
	ForcedTags  []string `json:"forcedTags"`
	ValidTags   []string `json:"validTags"`
	Tags        []string `json:"tags"`
}

func (n hsNode) hasTag(tag string) bool {
	for _, set := range [][]string{n.ForcedTags, n.ValidTags, n.Tags} {
		for _, x := range set {
			if x == tag {
				return true
			}
		}
	}
	return false
}

func (n hsNode) ipv4() string {
	for _, ip := range n.IPAddresses {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			return ip
		}
	}
	return ""
}

func (h *headscaleFixture) nodes(t testing.TB) []hsNode {
	t.Helper()
	code, raw, err := h.api("GET", "/node", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, string(raw))
	var out struct {
		Nodes []hsNode `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return out.Nodes
}

// hsRule is one rule of the policy NebulaCtrl wrote.
type hsRule struct {
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

type hsPolicy struct {
	TagOwners map[string][]string `json:"tagOwners"`
	ACLs      []hsRule            `json:"acls"`
	Raw       string              `json:"-"`
}

// policy reads the stored policy (zero value when none was set yet).
func (h *headscaleFixture) policy(t testing.TB) hsPolicy {
	t.Helper()
	code, raw, err := h.api("GET", "/policy", nil)
	require.NoError(t, err)
	if code != http.StatusOK {
		return hsPolicy{}
	}
	var env struct {
		Policy string `json:"policy"`
	}
	require.NoError(t, json.Unmarshal(raw, &env), string(raw))
	var p hsPolicy
	require.NoError(t, json.Unmarshal([]byte(env.Policy), &p), env.Policy)
	p.Raw = env.Policy
	return p
}

// seedPolicy stores a default-deny policy that lets NebulaCtrl's tags exist, so
// the first pre-authorized key can be minted before NebulaCtrl has written its
// own rules (managed mode replaces the whole document afterwards).
func (h *headscaleFixture) seedPolicy(t testing.TB) {
	t.Helper()
	doc := `{"tagOwners":{"` + meshTag + `":[],"tag:nebula-svc":[]},"acls":[]}`
	code, raw, err := h.api("PUT", "/policy", map[string]any{"policy": doc})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, string(raw))
}

// dsts returns every destination (ip:port) a source IP may reach.
func (p hsPolicy) dsts(srcIP string) []string {
	var out []string
	for _, r := range p.ACLs {
		for _, s := range r.Src {
			if s == srcIP {
				out = append(out, r.Dst...)
			}
		}
	}
	return out
}

// ---- host reachability -------------------------------------------------------

// pickMeshHostIP finds an address of this machine that the test process, local
// containers and host2's containers all use to reach the published Headscale
// port. host.docker.internal does not resolve inside host2's inner containers,
// so the login server must carry an IP. probePort answers
// /health on 0.0.0.0 while the probe runs.
func pickMeshHostIP(t testing.TB, probePort int) string {
	t.Helper()
	if v := os.Getenv(envMeshHostIP); v != "" {
		return v
	}
	var cands []string
	if gw, err := sh(context.Background(), "", "docker", "network", "inspect", "bridge", "-f", "{{(index .IPAM.Config 0).Gateway}}"); err == nil && strings.TrimSpace(gw) != "" {
		cands = append(cands, strings.TrimSpace(gw))
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			cands = append(cands, ipn.IP.String())
		}
	}
	var tried []string
	for _, ip := range cands {
		u := fmt.Sprintf("http://%s:%d/health", ip, probePort)
		c := &http.Client{Timeout: 3 * time.Second}
		var res *http.Response
		var err error
		// The probe container may need a moment to listen.
		for range 10 {
			if res, err = c.Get(u); err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			tried = append(tried, ip+" (test process: "+err.Error()+")")
			continue
		}
		_ = res.Body.Close()
		// host2's own netns is where its inner containers' traffic leaves from.
		if out, err := sh(context.Background(), "", "docker", "exec", host2Name, "wget", "-q", "-O", "-", "-T", "4", u); err != nil {
			tried = append(tried, ip+" (host2: "+strings.TrimSpace(out)+")")
			continue
		}
		if out, err := sh(context.Background(), "", "docker", "run", "--rm", "--entrypoint", "wget", "alpine:3", "-q", "-O", "-", "-T", "4", u); err != nil {
			tried = append(tried, ip+" (local container: "+strings.TrimSpace(out)+")")
			continue
		}
		return ip
	}
	t.Fatalf("no address of this machine reaches the Headscale port from the test process, a local container and host2 (set %s): tried %v", envMeshHostIP, tried)
	return ""
}

// ---- the mesh stack ----------------------------------------------------------

// meshStack is the mesh test world: one control plane, two servers on two
// Docker daemons, one Headscale, the mesh enabled on both servers.
type meshStack struct {
	*Stack
	T      *testing.T
	App    *fixtureState
	HS     *headscaleFixture
	Srv1   string // the stack's server: the local daemon (in-process agent1)
	Srv2   string // host2 (docker-in-docker, agent2 container)
	Local  dockerHost
	Host2  dockerHost
	H2App  string // the fixture app image as host2's daemon can pull it
	HostIP string
	root   string
	// intruder is a client for an app of ANOTHER environment on host2 (set by the
	// service-link phase, used by the replication phase's isolation checks).
	intruder *ProxyClient
}

// newMeshStack starts everything and returns once both servers have mesh nodes
// online with tailnet IPs and the ACL is applied. It fails (never skips) when a
// part cannot start, with the part's own log.
func newMeshStack(t *testing.T) *meshStack {
	t.Helper()
	requireMesh(t)
	requireDocker(t)
	app := appFixture(t)
	root, err := repoRoot()
	require.NoError(t, err)
	// Link nodes need /dev/net/tun of the DAEMON's host. The test process may not
	// be on it (macOS): state the answer, as the dev compose does for agent2.
	t.Setenv("NEBULACTRL_TUN_DEVICE", "1")

	s := ExclusiveStack(t, StackOptions{ListenAll: true})
	m := &meshStack{Stack: s, T: t, App: app, Srv1: s.ServerID, Local: localHost, Host2: host2Host, root: root}

	m.startHost2()
	port, err := freePort()
	require.NoError(t, err)
	// Headscale first answers on loopback only to find an address every daemon can use.
	probe := startHeadscaleProbe(t, port)
	m.HostIP = pickMeshHostIP(t, port)
	probe()
	m.HS = startHeadscale(t, root, m.HostIP, port)
	m.HS.seedPolicy(t)

	m.preloadHost2Images()
	m.H2App = m.publishAppImageOnHost2()

	// Cleanup order is LIFO: diagnostics first, then the nodes, then host2.
	t.Cleanup(func() { m.cleanupMeshContainers() })
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(m.diagnostics())
		}
	})

	m.connectIntegration()
	m.enableMesh(m.Srv1)
	m.enableMesh(m.Srv2)
	m.waitMeshOnline(m.Srv1, 5*time.Minute)
	m.waitMeshOnline(m.Srv2, 5*time.Minute)
	return m
}

// startHeadscaleProbe publishes a throwaway container answering /health on
// 0.0.0.0:port until the returned stop func runs, so pickMeshHostIP tests the
// very path Headscale will be reached on (a published port of the Docker VM)
// before the real container, which needs the chosen address in its server_url,
// exists.
func startHeadscaleProbe(t testing.TB, port int) (stop func()) {
	t.Helper()
	name := meshHeadscaleName + "-probe"
	_, _ = sh(context.Background(), "", "docker", "rm", "-f", name)
	mustSh(t, "docker", "run", "-d", "--name", name, "-p", fmt.Sprintf("0.0.0.0:%d:%d", port, port), "alpine:3", "sh", "-c",
		fmt.Sprintf(`while true; do printf "HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nok\n" | nc -l -p %d; done`, port))
	stop = func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", name) }
	t.Cleanup(stop)
	return stop
}

// startHost2 brings up host2 (docker-in-docker) and agent2 of the dev `multi`
// profile against this test's control plane, wipes everything a previous run
// left inside host2's daemon, makes sure /dev/net/tun exists there and waits for
// the server to be online.
func (m *meshStack) startHost2() {
	t := m.T
	s := m.Stack
	cpPort := s.Srv.Listener.Addr().(*net.TCPAddr).Port
	srv2, tok2 := s.EnrollmentToken("host2")
	m.Srv2 = srv2
	env := append(os.Environ(),
		fmt.Sprintf("NEBULACTRL_DEV_CP_PORT=%d", cpPort),
		"NEBULACTRL_ENROLL_TOKEN_2="+tok2,
	)
	compose := func(timeout time.Duration, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		full := []string{"compose", "-f", composeFile, "--profile", "multi"}
		if _, err := os.Stat(m.root + "/.env"); err == nil {
			full = []string{"compose", "--env-file", ".env", "-f", composeFile, "--profile", "multi"}
		}
		cmd := newCmd(ctx, m.root, "docker", append(full, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	down := func() {
		// host2-data outlives the container (it caches images): empty its daemon so the
		// next run does not restart this run's containers.
		_, _ = sh(context.Background(), "", "docker", "exec", host2Name, "sh", "-c",
			"docker rm -f $(docker ps -aq) >/dev/null 2>&1; docker network prune -f >/dev/null 2>&1; docker volume prune -af >/dev/null 2>&1; true")
		_, _ = compose(2*time.Minute, "rm", "-sf", "agent2", "host2")
		_, _ = sh(context.Background(), "", "docker", "volume", "rm", "-f", agent2Volume)
	}
	down()
	t.Cleanup(down)
	if out, err := compose(10*time.Minute, "up", "-d", "--build", "host2"); err != nil {
		t.Fatalf("compose up host2: %v\n%s", err, out)
	}
	// Stale inner containers of an earlier run restart with the daemon; remove them
	// before the agent looks at the daemon.
	_, _ = sh(context.Background(), "", "docker", "exec", host2Name, "sh", "-c",
		"docker rm -f $(docker ps -aq) >/dev/null 2>&1; docker network prune -f >/dev/null 2>&1; docker volume prune -af >/dev/null 2>&1; true")
	if out, err := sh(context.Background(), "", "docker", "exec", host2Name, "sh", "-c",
		"test -c /dev/net/tun || { mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 600 /dev/net/tun; }; test -c /dev/net/tun"); err != nil {
		t.Fatalf("host2 has no /dev/net/tun and it could not be created: %v\n%s\n"+
			"Link nodes (cross-server links and replication) cannot run: this is NOT verified on this machine", err, out)
	}
	if out, err := compose(10*time.Minute, "up", "-d", "--build", "agent2"); err != nil {
		t.Fatalf("compose up agent2: %v\n%s", err, out)
	}
	srv := s.WaitServer(srv2, 3*time.Minute)
	require.Equal(t, "online", srv.Str("status"))
	require.Truef(t, srv.Obj("facts").Bool("tunDevice"), "agent2 must report facts.tunDevice (NEBULACTRL_TUN_DEVICE on agent2, /dev/net/tun in host2): %v", srv.Obj("facts"))
}

// preloadHost2Images copies the images the tests need from the local daemon to
// host2's (or pulls them there), so a run does not depend on the registry rate limit.
func (m *meshStack) preloadHost2Images() {
	for _, img := range []string{"postgres:17", "traefik/whoami:latest", "registry:2", "alpine:3"} {
		if _, err := host2Host.docker("image", "inspect", img); err == nil {
			continue
		}
		if _, err := localHost.docker("image", "inspect", img); err == nil {
			if out, err := sh(context.Background(), "", "bash", "-c", fmt.Sprintf("docker save %s | docker exec -i %s docker load", img, host2Name)); err != nil {
				m.T.Logf("preload %s into host2 failed (host2 will pull it): %v %s", img, err, out)
			}
			continue
		}
		if out, err := host2Host.docker("pull", img); err != nil {
			m.T.Logf("host2 could not pull %s: %v %s", img, err, out)
		}
	}
}

// publishAppImageOnHost2 loads the fixture app into host2 and serves it from a
// registry on host2's loopback; services on host2 use the returned reference.
func (m *meshStack) publishAppImageOnHost2() string {
	t := m.T
	_, _ = host2Host.docker("rm", "-f", host2RegistryName)
	host2Host.mustDocker(t, "run", "-d", "--name", host2RegistryName, "-p", host2Registry+":5000", "registry:2")
	out, err := sh(context.Background(), "", "bash", "-c", fmt.Sprintf("docker save %s | docker exec -i %s docker load", appImageLocal, host2Name))
	require.NoError(t, err, out)
	ref := host2Registry + "/nebulactrl-test-app:dev"
	host2Host.mustDocker(t, "tag", appImageLocal, ref)
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		_, err := host2Host.docker("push", ref)
		return err == nil
	}), "could not push the fixture app to the registry on host2")
	return ref
}

// connectIntegration stores the Headscale API-key integration and turns the mesh
// on for the team (managed ACL: the Headscale API key can write the policy).
func (m *meshStack) connectIntegration() {
	t := m.T
	r := m.Do("PUT", "/integrations/tailscale", map[string]any{
		"provider": "headscale", "loginServer": m.HS.LoginURL, "clientSecret": m.HS.APIKey,
		"headscaleCredential": "apikey",
		"defaultTags":         []string{"tag:nebula-svc"},
		"allowedTags":         []string{"tag:nebula-svc", meshTag},
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	r = m.Do("PUT", "/mesh/settings", map[string]any{"aclMode": "managed", "meshTag": meshTag})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
}

// enableMesh plans (dry run: devices added, tun device) then enables the mesh on a server.
func (m *meshStack) enableMesh(serverID string) {
	t := m.T
	plan := m.Do("PUT", "/servers/"+serverID+"/mesh?dryRun=true", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.GreaterOrEqual(t, plan.JSON().Obj("plan").Int("devicesAdded"), 1, string(plan.Raw))
	require.True(t, plan.JSON().Obj("plan").Bool("tunDevice"), "the host must have /dev/net/tun for link nodes: %s", plan.Raw)
	r := m.Do("PUT", "/servers/"+serverID+"/mesh", map[string]any{"enabled": true, "acceptMeshChanges": true})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
}

// meshNode returns GET /servers/{id}/mesh (the node view).
func (m *meshStack) meshNode(serverID string) J {
	r := m.Do("GET", "/servers/"+serverID+"/mesh", nil)
	if r.Status != http.StatusOK {
		return J{}
	}
	j := r.JSON()
	if n := j.Obj("node"); n != nil {
		return n
	}
	return j
}

func strs(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// waitMeshOnline waits until the server's node is online with a tailnet IP.
func (m *meshStack) waitMeshOnline(serverID string, timeout time.Duration) J {
	t := m.T
	var n J
	ok := eventually(timeout, 2*time.Second, func() bool {
		n = m.meshNode(serverID)
		return n.Str("state") == agentprotocol.MeshStateOnline && len(strs(n["ips"])) > 0
	})
	require.Truef(t, ok, "the mesh node of server %s is not online within %s: %v", serverID, timeout, n)
	return n
}

// serverNodeIP is the IPv4 tailnet address of a server's node.
func (m *meshStack) serverNodeIP(t testing.TB, serverID string) string {
	t.Helper()
	var ips []string
	require.NoError(t, m.Pool.QueryRow(context.Background(), `SELECT ips FROM server_mesh WHERE server_id = $1`, serverID).Scan(&ips))
	for _, ip := range ips {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			return ip
		}
	}
	t.Fatalf("server %s has no IPv4 tailnet address: %v", serverID, ips)
	return ""
}

// meshLinkRow is a row of mesh_links.
type meshLinkRow struct {
	ID, EnvID, Server, Remote, Purpose, State, Hostname string
	IPs                                                 []string
}

func (l meshLinkRow) ipv4() string {
	for _, ip := range l.IPs {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			return ip
		}
	}
	return ""
}

// links returns the link nodes of an environment ("" = all), oldest first.
func (m *meshStack) links(t testing.TB, envID string) []meshLinkRow {
	t.Helper()
	rows, err := m.Pool.Query(context.Background(),
		`SELECT id::text, environment_id::text, server_id::text, remote_server_id::text, purpose, state, hostname, ips
		   FROM mesh_links WHERE ($1 = '' OR environment_id::text = $1) ORDER BY created_at`, envID)
	require.NoError(t, err)
	defer rows.Close()
	var out []meshLinkRow
	for rows.Next() {
		var l meshLinkRow
		require.NoError(t, rows.Scan(&l.ID, &l.EnvID, &l.Server, &l.Remote, &l.Purpose, &l.State, &l.Hostname, &l.IPs))
		out = append(out, l)
	}
	require.NoError(t, rows.Err())
	return out
}

// waitLinks waits until the environment has n links of purpose, all online, and
// returns them. When a link never comes up it fails with the link node's log
// (the usual cause on a machine whose docker-in-docker has no /dev/net/tun).
func (m *meshStack) waitLinks(env, purpose string, n int, timeout time.Duration) []meshLinkRow {
	t := m.T
	var got []meshLinkRow
	ok := eventually(timeout, 2*time.Second, func() bool {
		got = nil
		for _, l := range m.links(t, env) {
			if l.Purpose == purpose {
				got = append(got, l)
			}
		}
		if len(got) != n {
			return false
		}
		for _, l := range got {
			if l.State != agentprotocol.MeshStateOnline || l.ipv4() == "" {
				return false
			}
		}
		return true
	})
	if !ok {
		t.Fatalf("want %d %s link(s) online in environment %s within %s, have %+v\n%s", n, purpose, env, timeout, got, m.diagnostics())
	}
	return got
}

// exposures returns (meshPort, containerPort, purpose) of a service's exposures.
type meshExposureRow struct {
	ID, ServerID, ServiceID, Purpose string
	ContainerPort, MeshPort          int
}

func (m *meshStack) exposures(t testing.TB, serviceID string) []meshExposureRow {
	t.Helper()
	rows, err := m.Pool.Query(context.Background(),
		`SELECT id::text, server_id::text, service_id::text, purpose, container_port, mesh_port
		   FROM mesh_exposures WHERE service_id::text = $1 ORDER BY purpose`, serviceID)
	require.NoError(t, err)
	defer rows.Close()
	var out []meshExposureRow
	for rows.Next() {
		var e meshExposureRow
		require.NoError(t, rows.Scan(&e.ID, &e.ServerID, &e.ServiceID, &e.Purpose, &e.ContainerPort, &e.MeshPort))
		out = append(out, e)
	}
	require.NoError(t, rows.Err())
	return out
}

// grantCount is the number of mesh_grants rows of a ref (a consumer or replica).
func (m *meshStack) grantCount(t testing.TB, refID string) int {
	t.Helper()
	var n int
	require.NoError(t, m.Pool.QueryRow(context.Background(), `SELECT count(*) FROM mesh_grants WHERE ref_id::text = $1`, refID).Scan(&n))
	return n
}

// waitPolicy waits for the Headscale policy to satisfy ok.
func (m *meshStack) waitPolicy(timeout time.Duration, what string, ok func(hsPolicy) bool) hsPolicy {
	t := m.T
	var p hsPolicy
	good := eventually(timeout, 2*time.Second, func() bool { p = m.HS.policy(t); return ok(p) })
	require.Truef(t, good, "the Headscale policy never satisfied %q within %s:\n%s", what, timeout, p.Raw)
	return p
}

// ---- environments, clients, services on host2 --------------------------------

// newEnv creates another environment in the stack's project and returns its id.
func (m *meshStack) newEnv(name string) string {
	t := m.T
	r := m.Do("POST", "/projects/"+m.ProjectID+"/environments", map[string]any{"name": name})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	id := r.JSON().Str("id")
	trackEnv(id)
	m.servicesMu.Lock()
	m.extraEnvs = append(m.extraEnvs, id)
	m.servicesMu.Unlock()
	return id
}

// host2Client returns a client for https://<host>/ served by host2's Traefik.
func host2Client(host string) *ProxyClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, host2HTTPS)
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: host}, //nolint:gosec // test-only self-signed default cert
		DisableKeepAlives: true,
	}
	return &ProxyClient{c: &http.Client{Transport: tr, Timeout: 8 * time.Second}, host: host, https: host2HTTPS}
}

// fetchVia asks an app (through its Traefik client c) to GET target from inside
// its container (tests/fixtures/app /fetch) and returns (status, body).
func fetchVia(c *ProxyClient, target string) (int, string, error) {
	return c.Get("/fetch?url=" + url.QueryEscape(target))
}

// waitFetch polls until /fetch of target answers `ok <status> ...` with the
// wanted status (and, when contains is not empty, a body containing it).
func waitFetch(t testing.TB, c *ProxyClient, target string, status int, contains string, timeout time.Duration) string {
	t.Helper()
	var code int
	var body string
	var err error
	prefix := fmt.Sprintf("ok %d", status)
	require.Truef(t, eventually(timeout, 2*time.Second, func() bool {
		code, body, err = fetchVia(c, target)
		return err == nil && code == http.StatusOK && strings.HasPrefix(body, prefix) && strings.Contains(body, contains)
	}), "fetch %s via the consumer: want %q containing %q, last %d %q err=%v", target, prefix, contains, code, body, err)
	return body
}

// requireFetchFails asserts the consumer cannot fetch target (anything but ok).
func requireFetchFails(t testing.TB, c *ProxyClient, target string) string {
	t.Helper()
	code, body, err := fetchVia(c, target)
	require.NoError(t, err, "the consumer app itself must answer")
	require.Equal(t, http.StatusInternalServerError, code, "fetch %s unexpectedly worked: %q", target, body)
	return body
}

// startApp creates and deploys a fixture app service on a server/environment
// with a hostname, returning the service and a client for its domain. On host2
// the image comes from host2's registry.
func (m *meshStack) startApp(name, serverID, envID string, vars map[string]string, cfg map[string]any) (Service, *ProxyClient) {
	t := m.T
	image := m.App.AppImage
	client := NewProxyClient
	if serverID == m.Srv2 {
		image = m.H2App
		client = host2Client
	}
	host := m.Host(name)
	if vars == nil {
		vars = map[string]string{}
	}
	if _, ok := vars["VERSION"]; !ok {
		vars["VERSION"] = name
	}
	svc := m.NewService(SvcSpec{Name: name, ServerID: serverID, Env: envID, Image: image, Domain: host, Vars: vars, Config: cfg})
	d := m.WaitDeployment(m.Deploy(svc.ID), 5*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "deployment of %s: %v\n%s", name, d, m.DeploymentLogs(d.Str("id")))
	c := client(host)
	waitConsumer(t, c, "/", vars["VERSION"], 90*time.Second)
	return svc, c
}

// ---- diagnostics and cleanup ---------------------------------------------------

// diagnostics collects what explains a mesh failure: the control plane's view of
// the mesh, every mesh container and its log on both daemons, the Headscale
// nodes and policy.
func (m *meshStack) diagnostics() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n===== mesh diagnostics (host IP %s, login server %s) =====\n", m.HostIP, m.HS.LoginURL)
	for _, p := range []string{"/mesh", "/servers/" + m.Srv1 + "/mesh", "/servers/" + m.Srv2 + "/mesh"} {
		r := m.Do("GET", p, nil)
		fmt.Fprintf(&sb, "GET %s -> %d %s\n", p, r.Status, truncate(string(r.Raw), 3000))
	}
	for _, h := range []dockerHost{m.Local, m.Host2} {
		out, _ := h.docker("ps", "-a", "--filter", "label="+agentprotocol.LabelMesh, "--format", "{{.Names}}\t{{.Status}}\t{{.Image}}")
		fmt.Fprintf(&sb, "--- %s mesh containers ---\n%s\n", h.name, out)
		for _, name := range meshNames(out) {
			logs, _ := h.docker("logs", "--tail", "40", name)
			fmt.Fprintf(&sb, "--- %s logs %s ---\n%s\n", h.name, name, truncate(logs, 2500))
		}
	}
	if code, raw, err := m.HS.api("GET", "/node", nil); err == nil {
		fmt.Fprintf(&sb, "--- headscale nodes (%d) ---\n%s\n", code, truncate(string(raw), 3000))
	}
	fmt.Fprintf(&sb, "--- headscale policy ---\n%s\n", m.HS.policy(m.T).Raw)
	fmt.Fprintf(&sb, "--- headscale logs ---\n%s\n", truncate(mustShLoose("docker", "logs", "--tail", "40", m.HS.Name), 3000))
	return sb.String()
}

func meshNames(psOut string) []string {
	var out []string
	for _, ln := range strings.Split(psOut, "\n") {
		if f := strings.Fields(ln); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// cleanupMeshContainers removes the mesh nodes of the local daemon (they sit on
// environment networks that the Stack cleanup removes afterwards). host2's
// daemon is wiped when host2 goes down.
func (m *meshStack) cleanupMeshContainers() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ids, _ := sh(ctx, "", "docker", "ps", "-aq", "--filter", "label="+agentprotocol.LabelMesh)
	for _, id := range strings.Fields(ids) {
		_, _ = sh(ctx, "", "docker", "rm", "-f", id)
	}
	vols, _ := sh(ctx, "", "docker", "volume", "ls", "-q", "--filter", "name=nc-mesh-")
	for _, v := range strings.Fields(vols) {
		_, _ = sh(ctx, "", "docker", "volume", "rm", "-f", v)
	}
	_, _ = sh(ctx, "", "docker", "network", "rm", agentprotocol.MeshNetwork)
}

// use points the stack's (and its embedded Stack's) failure reporting at t, the
// *testing.T of the phase that is running. Phases run one after the other, so
// the swap needs no locking; failures inside a phase end that phase only.
func (m *meshStack) use(t *testing.T) {
	m.T = t
	m.Stack.t = t
}

// phase runs fn as a named subtest of parent against this stack and restores
// the parent afterwards (the stack's cleanups report through it).
func (m *meshStack) phase(parent *testing.T, name string, fn func(t *testing.T)) {
	parent.Helper()
	parent.Run(name, func(t *testing.T) {
		m.use(t)
		fn(t)
	})
	m.use(parent)
}
