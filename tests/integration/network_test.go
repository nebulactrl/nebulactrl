//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// fetchFrom asks the fixture app behind px to request target from inside its
// container (GET /fetch) and returns the status and body.
func fetchFrom(t testing.TB, px *ProxyClient, target string) (int, string) {
	t.Helper()
	code, body, err := px.GetRetry("/fetch?url=" + url.QueryEscape(target))
	require.NoError(t, err, "GET /fetch via %s", px.host)
	return code, body
}

// daemonIsolatesNetworks reports whether the Docker daemon keeps containers on
// different bridge networks from reaching each other by IP. Docker on Linux does
// (iptables); OrbStack and Docker Desktop route between their networks, so
// there only the DNS (name) isolation can be asserted.
func daemonIsolatesNetworks(t testing.TB) bool {
	t.Helper()
	out, err := sh(context.Background(), "", "docker", "info", "--format", "{{.OperatingSystem}}")
	require.NoError(t, err)
	os := strings.ToLower(out)
	if strings.Contains(os, "orbstack") || strings.Contains(os, "docker desktop") {
		t.Logf("%q routes between Docker networks: asserting name isolation only, not isolation by IP", out)
		return false
	}
	return true
}

// hostAddresses are addresses of the machine running the tests that a container
// might reach: the Docker host alias, then every non-loopback IPv4 address.
func hostAddresses() []string {
	out := []string{"host.docker.internal"}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip := ipn.IP.To4(); ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

// 8. network isolation: every environment has its own network. A service sees
// its siblings by slug (and its own environment's "web", not another's), cannot
// reach another environment's services by name or IP, and a service that blocks
// outbound internet cannot reach out while its siblings still reach it.
//
// Two phases, so that never more than two networks are alive at once (see
// envMaxNetworks): first environments A and B side by side, then B goes away and
// the blocking service (with its internal network) joins A.
func TestNetworkIsolation(t *testing.T) {
	requireTier(t, tierSmoke)
	t.Parallel()
	app := appFixture(t)
	s := SharedStackN(t, 2)

	r := s.Do("POST", "/projects", map[string]any{"name": "B " + s.hostBase})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	r = s.Do("GET", "/projects/"+r.JSON().Str("id")+"/environments", nil)
	envB := r.JSON().Items()[0].Str("id")
	s.TrackEnv(envB)

	svc := func(name, version, host string, env string, cfg map[string]any) Service {
		return s.NewService(SvcSpec{
			Name: name, Image: app.AppImage, Domain: host, Env: env, Config: cfg,
			Vars: map[string]string{"VERSION": version},
		})
	}
	deployAll := func(svcs ...Service) {
		t.Helper()
		deps := make([]string, len(svcs))
		for i, sv := range svcs {
			deps[i] = s.Deploy(sv.ID)
		}
		for i, d := range deps {
			dep := s.WaitDeployment(d, 3*time.Minute)
			require.Equal(t, "succeeded", dep.Str("status"), "service %s: %v\n%s", svcs[i].Slug, dep, s.DeploymentLogs(d))
		}
	}

	// ---- Phase 1: environment A (web, client) next to environment B (its own
	// web, and a service only B has) ----------------------------------------------
	web := svc("web", "a-web", s.Host("web"), "", nil)
	client := svc("client", "a-client", s.Host("client"), "", nil)
	webB := svc("web", "b-web", s.Host("webb"), envB, nil)
	bOnly := svc("bonly", "b-only", s.Host("bonly"), envB, nil)
	deployAll(web, client, webB, bOnly)

	clientPx := NewProxyClient(s.Host("client"))
	clientPx.WaitBody(t, "/", "a-client", 30*time.Second)
	envNet := agentprotocol.EnvNetwork(s.EnvID)
	require.Equal(t, []string{envNet}, networkNames(s.RunningContainers(client.ID)[0].Networks), "no service joins the shared edge network")

	// Siblings by slug, and the slug resolves inside the caller's environment only.
	code, body := fetchFrom(t, clientPx, "http://web:8080/")
	require.Equal(t, 200, code, body)
	require.Equal(t, "ok 200 a-web", body, "\"web\" must resolve to this environment's web, not environment B's")

	// Another environment: unknown by name and, where the daemon isolates
	// networks, unreachable by IP.
	ipIsolated := daemonIsolatesNetworks(t)
	code, body = fetchFrom(t, clientPx, "http://bonly:8080/")
	require.Equal(t, http.StatusInternalServerError, code, "environment B's service resolved by name from A: %s", body)
	bC := s.RunningContainers(webB.ID)
	require.Len(t, bC, 1)
	bIP := bC[0].Networks[agentprotocol.EnvNetwork(envB)].IP
	require.NotEmpty(t, bIP, "environment B's container has no IP on its network: %v", bC[0].Networks)
	code, body = fetchFrom(t, clientPx, "http://"+net.JoinHostPort(bIP, "8080")+"/")
	if ipIsolated {
		require.Equal(t, http.StatusInternalServerError, code, "environment B's container reached by IP from A: %s", body)
	}
	// And the same the other way round.
	bPx := NewProxyClient(s.Host("bonly"))
	bPx.WaitBody(t, "/", "b-only", 30*time.Second)
	code, body = fetchFrom(t, bPx, "http://web:8080/")
	require.Equal(t, "ok 200 b-web", body, "B's \"web\" is its own: %d", code)
	aIP := s.RunningContainers(web.ID)[0].Networks[envNet].IP
	require.NotEmpty(t, aIP)
	code, body = fetchFrom(t, bPx, "http://"+net.JoinHostPort(aIP, "8080")+"/")
	if ipIsolated {
		require.Equal(t, http.StatusInternalServerError, code, "environment A's container reached by IP from B: %s", body)
	}

	// ---- Phase 2: B goes away; a database stand-in that blocks outbound internet
	// joins A, and the services deployed after it can still reach it -------------
	s.removeServices(webB, bOnly)
	removeEnvNetworks(context.Background(), envB)

	db := svc("db", "a-db", s.Host("db"), "", map[string]any{"blockEgress": true})
	deployAll(db)
	// Siblings join the internal network when they are (re)deployed after it.
	deployAll(web, client)
	clientPx.WaitBody(t, "/", "a-client", 30*time.Second)
	dbPx := NewProxyClient(s.Host("db"))
	dbPx.WaitBody(t, "/", "a-db", 30*time.Second)

	intNet := agentprotocol.EnvInternalNetwork(s.EnvID)
	dbC := s.RunningContainers(db.ID)
	require.Len(t, dbC, 1)
	require.Equal(t, []string{intNet}, networkNames(dbC[0].Networks), "a service that blocks egress joins only the internal network")
	clientC := s.RunningContainers(client.ID)
	require.Len(t, clientC, 1)
	require.ElementsMatch(t, []string{envNet, intNet}, networkNames(clientC[0].Networks),
		"a sibling of a blocking service joins the environment and the internal network")
	code, body = fetchFrom(t, clientPx, "http://db:8080/")
	require.Equal(t, 200, code, body)
	require.Equal(t, "ok 200 a-db", body, "siblings reach the service that blocks outbound internet")

	// Outbound: a listener on this machine stands in for "the internet". A
	// normal service reaches it, the one that blocks egress does not. The
	// control (the normal service succeeding) is what makes the failure meaningful.
	l, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	outside := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "outside") }))
	outside.Listener.Close()
	outside.Listener = l
	outside.Start()
	t.Cleanup(outside.Close)
	port := l.Addr().(*net.TCPAddr).Port

	var target string
	var tried []string
	for _, h := range hostAddresses() {
		cand := "http://" + net.JoinHostPort(h, fmt.Sprint(port)) + "/"
		code, body = fetchFrom(t, clientPx, cand)
		tried = append(tried, fmt.Sprintf("%s -> %d %s", cand, code, strings.TrimSpace(body)))
		if code == 200 && body == "ok 200 outside" {
			target = cand
			break
		}
	}
	require.NotEmpty(t, target, "no address of this machine is reachable from a normal container, so the egress block cannot be shown: %v", tried)
	code, body = fetchFrom(t, dbPx, target)
	require.Equal(t, http.StatusInternalServerError, code, "the service that blocks egress reached %s: %s", target, body)
}

// removeServices deletes services through the API and waits until the agent
// has removed their containers.
func (s *Stack) removeServices(svcs ...Service) {
	s.t.Helper()
	for _, sv := range svcs {
		s.cancelActive(sv.ID)
		r := s.Do("DELETE", "/services/"+sv.ID+"?confirm="+sv.Slug+"&deleteVolumes=true", nil)
		require.Truef(s.t, r.Status == http.StatusOK || r.Status == http.StatusNoContent, "delete %s: %d %s", sv.Slug, r.Status, r.Raw)
	}
	require.True(s.t, eventually(60*time.Second, 500*time.Millisecond, func() bool {
		for _, sv := range svcs {
			if len(s.Containers(sv.ID)) > 0 {
				return false
			}
		}
		return true
	}), "the agent did not remove the containers of the deleted services")
}

func networkNames[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
