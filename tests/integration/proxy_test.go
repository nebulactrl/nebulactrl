//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// externalUpstream runs the fixture app as a plain container that is NOT managed
// by NebulaCtrl and publishes it on a host port. The proxy reaches it through the
// host gateway (host.docker.internal), exactly like a LAN box or a host service.
func externalUpstream(t *testing.T, version string) (addr string, port int) {
	t.Helper()
	appFixture(t)
	port, err := freePort()
	require.NoError(t, err)
	name := fmt.Sprintf("nebulactrl-itest-ext-%d", port)
	ctx := context.Background()
	_, err = sh(ctx, "", "docker", "run", "-d", "--name", name,
		"-p", fmt.Sprintf("0.0.0.0:%d:8080", port), "-e", "VERSION="+version, appImageLocal)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", name) })
	require.NoError(t, waitTCP(ctx, net.JoinHostPort("127.0.0.1", fmt.Sprint(port))))
	return fmt.Sprintf("host.docker.internal:%d", port), port
}

// getWith issues GET path through the harness proxy with extra request tweaks.
func getWith(t *testing.T, p *ProxyClient, path string, mut func(*http.Request)) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+p.host+path, nil)
	require.NoError(t, err)
	if mut != nil {
		mut(req)
	}
	res, err := p.c.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	require.NoError(t, err)
	return res.StatusCode, strings.TrimSpace(string(b))
}

// serviceETag returns the service's current ETag (its generation).
func (s *Stack) serviceETag(id string) string {
	s.t.Helper()
	r := s.Do("GET", "/services/"+id, nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.Header.Get("ETag")
}

// domainID returns the ID of the service's domain with hostname.
func (s *Stack) domainID(serviceID, hostname string) string {
	s.t.Helper()
	r := s.Do("GET", "/services/"+serviceID+"/domains", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	for _, d := range r.JSON().Items() {
		if d.Str("hostname") == hostname {
			return d.Str("id")
		}
	}
	s.t.Fatalf("service %s has no domain %s: %s", serviceID, hostname, r.Raw)
	return ""
}

// patchDomain PATCHes a domain with the owning service's current ETag.
func (s *Stack) patchDomain(serviceID, domainID string, body map[string]any) Resp {
	s.t.Helper()
	return s.Do("PATCH", "/domains/"+domainID, body, "If-Match", s.serviceETag(serviceID))
}

// createRoute POSTs a route service and returns it.
func (s *Stack) createRoute(body map[string]any) J {
	s.t.Helper()
	if _, ok := body["serverId"]; !ok {
		body["serverId"] = s.ServerID
	}
	r := s.Do("POST", "/environments/"+s.EnvID+"/routes", body)
	require.Equal(s.t, http.StatusCreated, r.Status, string(r.Raw))
	j := r.JSON()
	s.servicesMu.Lock()
	s.serviceIDs = append(s.serviceIDs, j.Str("id"))
	s.servicesMu.Unlock()
	return j
}

// TestProxyExternalHTTPRoute: an HTTP route to an upstream the platform does not
// run is published with HTTPS, then protected step by step with the per-domain
// options (path prefix + strip, basic auth, IP allow-list, redirect domain).
// A route-only deployment applies each change; the first deploy goes through
// the verification probe.
func TestProxyExternalHTTPRoute(t *testing.T) {
	requireTier(t, tierFull)
	s := ExclusiveStack(t)
	addr, _ := externalUpstream(t, "ext-v1")

	route := s.createRoute(map[string]any{
		"name": "homelab", "protocol": "http",
		"upstreams":  []map[string]any{{"address": addr}},
		"domains":    []map[string]any{{"hostname": "ext.localhost"}},
		"healthPath": "/health",
	})
	rid := route.Str("id")
	require.Equal(t, "route", route.Str("kind"))

	d := s.DeployOK(rid)
	require.Equal(t, "route_only", d.Str("strategy"), "deployment: %v", d)
	px := NewProxyClient("ext.localhost")
	px.WaitBody(t, "/", "ext-v1", 30*time.Second)

	// Path prefix with strip: /api/ on a second domain row reaches the upstream's /.
	// It is compressed as well: Traefik disables a router whose compress
	// middleware lacks encodings, which made such a domain answer 404.
	r := s.Do("POST", "/services/"+rid+"/domains", map[string]any{
		"hostname": "ext.localhost", "pathPrefix": "/api", "stripPrefix": true, "options": map[string]any{"compress": true}})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	// Basic auth on the main domain: write-only password, usernames come back.
	mainDom := s.domainID(rid, "ext.localhost")
	const pw = "p4ss-canary-98765"
	r = s.Do("PUT", "/domains/"+mainDom+"/auth-users", []map[string]any{{"username": "alice", "password": pw}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.NotContains(t, string(r.Raw), pw)
	r = s.patchDomain(rid, mainDom, map[string]any{"options": map[string]any{"basicAuth": true, "noindex": true}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// Redirect domain: www.ext.localhost -> ext.localhost (permanent, path preserved).
	// Traefik's redirectRegex only has permanent/temporary: a permanent redirect of a
	// GET answers 301 (non-GET 308), a temporary one 302 (non-GET 307).
	r = s.Do("POST", "/services/"+rid+"/domains", map[string]any{
		"hostname": "www.ext.localhost", "kind": "redirect", "redirectTo": "ext.localhost", "redirectCode": 308, "preservePath": true})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	// Nothing has changed on the proxy before the deploy (staged).
	code, _ := getWith(t, px, "/", nil)
	require.Equal(t, 200, code, "staged options must not apply before deploy")

	s.DeployOK(rid)

	require.Eventually(t, func() bool {
		code, _ := getWith(t, px, "/", nil)
		return code == http.StatusUnauthorized
	}, 30*time.Second, 300*time.Millisecond, "basic auth must protect the domain after deploy")
	code, body := getWith(t, px, "/", func(r *http.Request) { r.SetBasicAuth("alice", pw) })
	require.Equal(t, 200, code)
	require.Equal(t, "ext-v1", body)
	code, _ = getWith(t, px, "/", func(r *http.Request) { r.SetBasicAuth("alice", "wrong") })
	require.Equal(t, http.StatusUnauthorized, code)

	// Path prefix + strip reaches the upstream root (the /api row has no auth),
	// through its compress middleware.
	code, body = getWith(t, px, "/api/", func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	require.Equal(t, 200, code, body)
	require.Equal(t, "ext-v1", body)

	// Redirect domain answers a permanent redirect with the preserved path.
	www := NewProxyClient("www.ext.localhost")
	www.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequest(http.MethodGet, "https://www.ext.localhost/some/path?x=1", nil)
	require.NoError(t, err)
	res, err := www.c.Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Equal(t, http.StatusMovedPermanently, res.StatusCode)
	// The Location carries the proxy's public HTTPS port when it is not 443.
	want := "https://ext.localhost/some/path?x=1"
	if proxyPorts.HTTPS != 443 {
		want = fmt.Sprintf("https://ext.localhost:%d/some/path?x=1", proxyPorts.HTTPS)
	}
	require.Equal(t, want, res.Header.Get("Location"))

	// IP allow-list: the source Traefik sees is the Docker gateway, never
	// 203.0.113.0/24, so the main domain now rejects even valid credentials.
	r = s.patchDomain(rid, mainDom, map[string]any{"options": map[string]any{
		"basicAuth": true, "access": map[string]any{"allowCidrs": []string{"203.0.113.0/24"}}}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	s.DeployOK(rid)
	require.Eventually(t, func() bool {
		code, _ := getWith(t, px, "/", func(r *http.Request) { r.SetBasicAuth("alice", pw) })
		return code == http.StatusForbidden
	}, 30*time.Second, 300*time.Millisecond, "IP allow-list must reject a non-listed source")

	// No secret material is persisted outside the sealed columns: the password
	// and bcrypt hashes never reach commands, audit, or any table in clear.
	cmds := s.dumpTables([]string{"agent_commands", "audit_events", "deployment_logs", "deployment_events"})
	require.NotContains(t, cmds, pw)
	require.NotContains(t, cmds, "$2a$")
	require.NotContains(t, cmds, "$2b$")
}

// TestProxyTCPRoute: a TCP route exposes a managed service's port on a host port
// through a static entrypoint. Adding the port recreates the proxy (HTTP comes
// back), a second route on the same port is refused, and the upstream policy
// rejects loopback and metadata addresses.
func TestProxyTCPRoute(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	s := ExclusiveStack(t)

	svc := s.NewService(SvcSpec{Name: "tcpapp", Image: app.AppImage, Domain: s.Host(), Vars: map[string]string{"VERSION": "v-tcp"}})
	s.DeployOK(svc.ID)
	web := NewProxyClient(s.Host())
	web.WaitBody(t, "/", "v-tcp", 30*time.Second)

	listen, err := freePort()
	require.NoError(t, err)
	sid := svc.ID
	route := s.createRoute(map[string]any{
		"name": "tcpapp-tcp", "protocol": "tcp", "listenPort": listen, "openToInternet": true,
		"upstreams": []map[string]any{{"serviceId": sid, "port": 8080}},
	})
	rid := route.Str("id")

	// The port is reserved immediately, even before the deploy.
	r := s.Do("POST", "/environments/"+s.EnvID+"/routes", map[string]any{
		"name": "dup", "serverId": s.ServerID, "protocol": "tcp", "listenPort": listen, "openToInternet": true,
		"upstreams": []map[string]any{{"serviceId": sid, "port": 8080}},
	})
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "port_in_use")

	// Reserved and out-of-range ports are rejected.
	for _, p := range []int{80, 443, 9000, 8099, 22} {
		r = s.Do("POST", "/environments/"+s.EnvID+"/routes", map[string]any{
			"name": "bad", "serverId": s.ServerID, "protocol": "tcp", "listenPort": p, "openToInternet": true,
			"upstreams": []map[string]any{{"serviceId": sid, "port": 8080}},
		})
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "port %d: %s", p, r.Raw)
	}

	// A TCP route needs an allow-list or an explicit open-to-internet.
	r = s.Do("POST", "/environments/"+s.EnvID+"/routes", map[string]any{
		"name": "closedless", "serverId": s.ServerID, "protocol": "tcp", "listenPort": listen + 1,
		"upstreams": []map[string]any{{"serviceId": sid, "port": 8080}},
	})
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))

	// Upstream policy: loopback and cloud metadata are denied for external upstreams.
	for _, bad := range []string{"127.0.0.1:9999", "169.254.169.254:80", "nebulactrl-traefik:9000", "localhost:80"} {
		r = s.Do("POST", "/environments/"+s.EnvID+"/routes", map[string]any{
			"name": "evil", "serverId": s.ServerID, "protocol": "http",
			"upstreams": []map[string]any{{"address": bad}}, "domains": []map[string]any{{"hostname": "evil.localhost"}},
		})
		require.Equal(t, http.StatusUnprocessableEntity, r.Status, "%s: %s", bad, r.Raw)
		require.Contains(t, string(r.Raw), "upstream_denied", bad)
	}

	// A token without proxy:write cannot publish traffic.
	tok := s.Token([]string{"projects:read", "projects:write", "deployments:write"}, nil)
	r = s.DoToken(tok, "POST", "/environments/"+s.EnvID+"/routes", map[string]any{
		"name": "nope", "serverId": s.ServerID, "protocol": "http",
		"upstreams": []map[string]any{{"address": "192.168.1.20:80"}}, "domains": []map[string]any{{"hostname": "nope.localhost"}},
	})
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))

	// The plan warns that the new port restarts the proxy.
	r = s.Do("POST", "/services/"+rid+"/deployments/plan", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Contains(t, strings.ToLower(string(r.Raw)), "restart", "plan should warn about the proxy restart: %s", r.Raw)

	// Deploy: the proxy is recreated with the new entrypoint.
	d := s.DeployOK(rid)
	require.Equal(t, "route_only", d.Str("strategy"))
	tcp := net.JoinHostPort("127.0.0.1", fmt.Sprint(listen))
	require.Eventually(t, func() bool {
		c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		res, err := c.Get("http://" + tcp + "/")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode == 200 && strings.TrimSpace(string(b)) == "v-tcp"
	}, 60*time.Second, 500*time.Millisecond, "TCP route must reach the managed service on %s", tcp)

	// HTTP is served again after the proxy recreate.
	web.WaitBody(t, "/", "v-tcp", 60*time.Second)

	// An external TCP upstream (a plain container on a host port).
	extAddr, _ := externalUpstream(t, "ext-tcp")
	listen2, err := freePort()
	require.NoError(t, err)
	route2 := s.createRoute(map[string]any{
		"name": "ext-tcp", "protocol": "tcp", "listenPort": listen2, "allowCidrs": []string{"0.0.0.0/0"},
		"upstreams": []map[string]any{{"address": extAddr}},
	})
	s.DeployOK(route2.Str("id"))
	tcp2 := net.JoinHostPort("127.0.0.1", fmt.Sprint(listen2))
	require.Eventually(t, func() bool {
		c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		res, err := c.Get("http://" + tcp2 + "/")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return strings.TrimSpace(string(b)) == "ext-tcp"
	}, 60*time.Second, 500*time.Millisecond, "external TCP route must reach the upstream on %s", tcp2)
	// The first TCP route is still served.
	res, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + tcp + "/")
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Equal(t, 200, res.StatusCode)

	// Deleting the route frees the port (route_ports row) and removes the listener.
	r = s.Do("DELETE", "/routes/"+rid, nil)
	require.Truef(t, r.Status == http.StatusNoContent || r.Status == http.StatusOK, "delete route: %d %s", r.Status, r.Raw)
	r = s.Do("GET", "/servers/"+s.ServerID+"/ports", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	for _, it := range r.JSON().Items() {
		require.NotEqual(t, listen, it.Int("port"), "deleted route must free its port: %s", r.Raw)
	}
}
