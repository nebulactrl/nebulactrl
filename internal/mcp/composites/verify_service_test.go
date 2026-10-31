package composites_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// verifyHits records the requests a probe test server receives.
type verifyHits struct {
	mu    sync.Mutex
	paths []string
	hosts []string
}

func (h *verifyHits) add(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.paths = append(h.paths, r.URL.Path)
	h.hosts = append(h.hosts, r.Host)
}

func (h *verifyHits) n() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.paths)
}

// verifyServer starts a plain HTTP server answering handler and makes the
// composites probe through a client that dials it whatever the hostname is.
func verifyServer(f *fx, handler func(w http.ResponseWriter, r *http.Request)) *verifyHits {
	f.t.Helper()
	hits := &verifyHits{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r)
		handler(w, r)
	}))
	f.t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		DisableKeepAlives: true,
	}}
	d := f.deps
	d.Probe = client
	for _, tl := range composites.Tools(d) {
		f.tools[tl.Name] = tl
	}
	return hits
}

// verifyDomain attaches a plain-HTTP domain to a service.
func verifyDomain(f *fx, sv db.Service, host string, mut func(*db.InsertDomainParams)) db.Domain {
	f.t.Helper()
	p := db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80, TlsMode: "none"}
	if mut != nil {
		mut(&p)
	}
	d, err := f.q.InsertDomain(context.Background(), p)
	require.NoError(f.t, err)
	return d
}

func verifyReasonCodes(m map[string]any) []string {
	var out []string
	for _, r := range explainList(m, "reasons") {
		out = append(out, r.(map[string]any)["code"].(string))
	}
	return out
}

func verifyObservedJSON(f *fx, id uuid.UUID, obs map[string]any) {
	f.t.Helper()
	raw, err := json.Marshal(obs)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET observed = $2, observed_at = now() WHERE id = $1", id, raw)
	require.NoError(f.t, err)
}

func TestVerifyServicePass(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	_, err := f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{healthcheck,type}', '"http"') WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET config = jsonb_set(config, '{healthcheck,path}', '"/healthz"') WHERE id = $1`, sv.ID)
	require.NoError(t, err)
	verifyDomain(f, sv, "api.example.com", nil)
	hits := verifyServer(f, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	f.ag.lines[sv.ID.String()] = []string{"server started", "GET /healthz 200"}

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, "pass", m["result"], "%v", m["reasons"])
	require.Empty(t, explainList(m, "reasons"))
	require.Equal(t, true, m["domainsProbed"])
	require.Equal(t, "/healthz", m["path"], "the http healthcheck path is the default probe path")
	probes := explainList(m, "probes")
	require.Len(t, probes, 1)
	p0 := probes[0].(map[string]any)
	require.Equal(t, "api.example.com", p0["hostname"])
	require.Equal(t, true, p0["ok"])
	require.Equal(t, float64(200), p0["status"])
	require.Equal(t, "http://api.example.com/healthz", p0["url"])
	require.Equal(t, []string{"/healthz"}, hits.paths)
	require.Equal(t, []string{"api.example.com"}, hits.hosts)

	obs := m["observed"].(map[string]any)
	require.Equal(t, "running", obs["state"])
	require.Equal(t, "healthy", obs["health"])
	require.Equal(t, true, obs["reported"])
	require.Equal(t, float64(2), m["logLinesScanned"])
	require.Equal(t, float64(0), m["logErrorCount"])
	require.Empty(t, sectionErrors(m))
	require.Contains(t, m["summary"], "is up")

	// An explicit path wins over the healthcheck path.
	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api"), "path": "/ready"})
	require.Equal(t, "/ready", m["path"])
	require.Equal(t, "/ready", hits.paths[len(hits.paths)-1])

	// The hostname is a valid selector too.
	m = f.must("verify_service", map[string]any{"service": "api.example.com"})
	require.Equal(t, f.selector(f.staging, "api"), m["service"])
}

func TestVerifyService502Fails(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	verifyDomain(f, sv, "api.example.com", nil)
	verifyServer(f, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, "fail", m["result"])
	require.Contains(t, verifyReasonCodes(m), "domain_5xx")
	p0 := explainList(m, "probes")[0].(map[string]any)
	require.Equal(t, false, p0["ok"])
	require.Equal(t, float64(502), p0["status"])
	require.Contains(t, m["summary"], "FAILED")
	require.Contains(t, m["summary"], "api.example.com returned 502")
	var tools []string
	for _, nt := range explainList(m, "nextTools") {
		tools = append(tools, nt.(map[string]any)["tool"].(string))
	}
	require.Contains(t, tools, "diagnose_service")
}

func TestVerifyServiceRedirectOffHostNotFollowed(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	verifyDomain(f, sv, "api.example.com", nil)
	hits := verifyServer(f, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example.net/steal", http.StatusFound)
	})

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, "warn", m["result"])
	require.Contains(t, verifyReasonCodes(m), "domain_redirect_offhost")
	p0 := explainList(m, "probes")[0].(map[string]any)
	require.Equal(t, "http://evil.example.net", p0["redirectedTo"])
	require.Equal(t, float64(302), p0["status"])
	require.Equal(t, 1, hits.n(), "the off-host redirect must not be followed")
}

func TestVerifyServiceDefaultClientRefusesLoopback(t *testing.T) {
	f := newFx(t)
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { mu.Lock(); n++; mu.Unlock() }))
	t.Cleanup(srv.Close)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	// "localhost" resolves to loopback only; the default client must refuse it.
	verifyDomain(f, sv, "localhost", nil)

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, "fail", m["result"])
	require.Contains(t, verifyReasonCodes(m), "domain_unreachable")
	p0 := explainList(m, "probes")[0].(map[string]any)
	require.Equal(t, false, p0["ok"])
	require.Contains(t, p0["error"], "refused")
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, n)
}

func TestVerifyServiceNoDomainsUsesObservedHealthOnly(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "worker"})
	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "worker")})
	require.Equal(t, "pass", m["result"])
	require.Equal(t, false, m["domainsProbed"])
	require.Empty(t, explainList(m, "probes"))
	require.Contains(t, m["notes"], "the service has no domains: verified through observed health only (no HTTP probe)")
	require.Contains(t, m["summary"], "observed health only")

	f.setObserved(sv.ID, "running", "unhealthy")
	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "worker")})
	require.Equal(t, "fail", m["result"])
	require.Contains(t, verifyReasonCodes(m), "unhealthy")

	f.setObserved(sv.ID, "running", "starting")
	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "worker")})
	require.Equal(t, "warn", m["result"])
	require.Contains(t, verifyReasonCodes(m), "starting")

	f.setObserved(sv.ID, "exited", "unknown")
	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "worker")})
	require.Equal(t, "fail", m["result"])
	require.Contains(t, verifyReasonCodes(m), "not_running")
}

func TestVerifyServiceNotDeployed(t *testing.T) {
	f := newFx(t)
	verifyServer(f, func(http.ResponseWriter, *http.Request) {})
	sv := f.service(svcOpts{env: f.staging, slug: "fresh", undeployed: true})
	verifyDomain(f, sv, "fresh.example.com", nil)

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "fresh")})
	require.Equal(t, "fail", m["result"])
	require.Contains(t, verifyReasonCodes(m), "not_deployed")
	require.Equal(t, false, m["domainsProbed"])
	require.Empty(t, explainList(m, "probes"))

	// A failed first deployment is reported with its status.
	f.release(sv, "traefik/whoami:v1", "failed", "")
	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "fresh")})
	require.Contains(t, explainList(m, "reasons")[0].(map[string]any)["message"], "latest is failed")
}

func TestVerifyServiceObservedRestartsAndReplicas(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	started := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	verifyObservedJSON(f, sv.ID, map[string]any{
		"state": "running", "health": "healthy", "replicasReady": 1, "replicasDesired": 2,
		"replicas": []any{
			map[string]any{"index": 0, "state": "running", "health": "healthy", "restartCount": 6, "startedAt": started},
			map[string]any{"index": 1, "state": "running", "health": "unhealthy", "restartCount": 1},
		},
		"startedAt": started,
	})
	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api"), "detail": "full"})
	require.Equal(t, "fail", m["result"])
	codes := verifyReasonCodes(m)
	require.Contains(t, codes, "crash_loop")
	require.Contains(t, codes, "replicas_degraded")
	obs := m["observed"].(map[string]any)
	require.Equal(t, float64(7), obs["restartCount"])
	require.Len(t, explainList(obs, "replicas"), 2, "detail:full lists the replicas")

	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Empty(t, explainList(m["observed"].(map[string]any), "replicas"), "concise omits replicas")
}

func TestVerifyServiceLogErrorScan(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	var lines []string
	for i := range 15 {
		lines = append(lines, fmt.Sprintf("ERROR db connection failed #%d", i))
		lines = append(lines, "ok line")
	}
	f.ag.lines[sv.ID.String()] = lines

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, "warn", m["result"])
	require.Contains(t, verifyReasonCodes(m), "log_errors")
	require.Equal(t, float64(30), m["logLinesScanned"])
	require.Equal(t, float64(15), m["logErrorCount"])
	eb := m["errorLines"].(map[string]any)
	require.Equal(t, true, eb["untrusted"])
	require.Equal(t, float64(10), eb["lines"], "at most 10 error lines")
	require.Contains(t, eb["text"], "db connection failed #14")
	require.NotContains(t, eb["text"], "ok line")
}

func TestVerifyServicePartialFailures(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	verifyDomain(f, sv, "api.example.com", nil)
	verifyServer(f, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })

	// The runtime log read fails: the rest is still returned.
	f.ag.logErr = errors.New("boom")
	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Contains(t, sectionErrors(m), "logs")
	require.Equal(t, "pass", m["result"])
	require.Len(t, explainList(m, "probes"), 1)
	require.Equal(t, float64(0), m["logLinesScanned"])
	f.ag.logErr = nil

	// Without logs:read the scan is skipped and listed; without servers:read
	// the probe still runs (public addresses) and a note says so.
	p := f.principal(policy.ScopeProjectsRead)
	m, err := f.call(p, "verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.NoError(t, err)
	require.Equal(t, []string{"logs"}, sectionErrors(m))
	require.Equal(t, "forbidden", explainList(m, "errors")[0].(map[string]any)["code"])
	require.Equal(t, "pass", m["result"])
	require.Len(t, explainList(m, "probes"), 1)
	require.Contains(t, m["notes"], "the server address is unknown (needs servers:read): domains on a private address are refused")
}

func TestVerifyServiceDomainFindings(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	dns := verifyDomain(f, sv, "dns.example.com", nil)
	_, err := f.pool.Exec(context.Background(), "UPDATE domains SET dns_ok = false, status_message = 'A record points elsewhere' WHERE id = $1", dns.ID)
	require.NoError(t, err)
	verifyDomain(f, sv, "api.example.com", func(p *db.InsertDomainParams) { p.PathPrefix = "/v1" })
	to := "https://www.example.com"
	verifyDomain(f, sv, "old.example.com", func(p *db.InsertDomainParams) { p.Kind = "redirect"; p.RedirectTo = &to })
	hits := verifyServer(f, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "api.example.com" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(200)
	})

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api"), "detail": "full"})
	require.Equal(t, "warn", m["result"])
	codes := verifyReasonCodes(m)
	require.Contains(t, codes, "dns_not_ok")
	require.Contains(t, codes, "domain_4xx")
	require.Len(t, explainList(m, "probes"), 2, "the redirect domain is not probed")
	require.Contains(t, m["notes"], "1 redirect domain was not probed (the proxy answers them with a redirect)")
	require.ElementsMatch(t, []string{"/", "/v1/"}, hits.paths, "a path-prefix domain is probed under its prefix")
	doms := explainList(m, "domains")
	require.Len(t, doms, 3)
	var probed int
	for _, d := range doms {
		if d.(map[string]any)["probed"] == true {
			probed++
		}
	}
	require.Equal(t, 2, probed)

	// Concise output carries no domain table.
	m = f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Empty(t, explainList(m, "domains"))
}

func TestVerifyServiceStagedChangesAndLatestFailure(t *testing.T) {
	f := newFx(t)
	sv := f.service(svcOpts{env: f.staging, slug: "api"})
	f.stage(sv.ID)
	bad := f.release(sv, "traefik/whoami:v2", "failed", "")

	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	require.Equal(t, "warn", m["result"])
	require.Contains(t, verifyReasonCodes(m), "latest_deployment_failed")
	var tools []map[string]any
	for _, nt := range explainList(m, "nextTools") {
		tools = append(tools, nt.(map[string]any))
	}
	var found bool
	for _, nt := range tools {
		if nt["tool"] == "explain_failed_deployment" {
			found = true
			require.Equal(t, bad.ID.String(), nt["args"].(map[string]any)["deployment"])
		}
	}
	require.True(t, found, "explain_failed_deployment is suggested for a failed latest deployment")
	require.Len(t, explainList(m, "notes"), 2, "no-domains note plus the staged-changes note")
}

func TestVerifyServiceArguments(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "api"})
	f.service(svcOpts{env: f.prod, slug: "api"})

	for name, tc := range map[string]struct {
		args map[string]any
		code string
	}{
		"empty service":    {map[string]any{"service": ""}, mcpkit.CodeInvalidArgument},
		"missing key":      {map[string]any{}, mcpkit.CodeInvalidArgument},
		"relative path":    {map[string]any{"service": "shop/staging/api", "path": "health"}, mcpkit.CodeInvalidArgument},
		"protocol path":    {map[string]any{"service": "shop/staging/api", "path": "//evil.test/x"}, mcpkit.CodeInvalidArgument},
		"header injection": {map[string]any{"service": "shop/staging/api", "path": "/a\r\nHost: evil"}, mcpkit.CodeInvalidArgument},
		"bad detail":       {map[string]any{"service": "shop/staging/api", "detail": "loud"}, mcpkit.CodeInvalidArgument},
		"unknown field":    {map[string]any{"service": "shop/staging/api", "url": "http://evil.test"}, mcpkit.CodeInvalidArgument},
		"ambiguous":        {map[string]any{"service": "api"}, mcpkit.CodeAmbiguous},
		"not found":        {map[string]any{"service": "shop/staging/nothing"}, mcpkit.CodeNotFound},
	} {
		_, err := f.call(f.admin, "verify_service", tc.args)
		require.Error(t, err, name)
		require.Equal(t, tc.code, errCode(err), name)
	}
	_, err := f.call(f.admin, "verify_service", map[string]any{"service": uuid.NewString()})
	require.Error(t, err)
}

func TestVerifyServiceToolDescriptor(t *testing.T) {
	f := newFx(t)
	tl := f.tools["verify_service"]
	require.Equal(t, []string{policy.ScopeProjectsRead}, tl.Scopes)
	require.Equal(t, mcpkit.Hints{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: true}, tl.Hints)
	require.Equal(t, mcpkit.ToolsetCore, tl.Toolset)
}

// Slices serialize as [] never null, so the output schema holds.
func TestVerifyServiceOutputHasNoNullSlices(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "api"})
	m := f.must("verify_service", map[string]any{"service": f.selector(f.staging, "api")})
	for _, k := range []string{"reasons", "notes", "probes", "errors", "nextTools"} {
		require.IsType(t, []any{}, m[k], k)
	}
	require.IsType(t, []any{}, m["observed"].(map[string]any)["replicas"])
}
