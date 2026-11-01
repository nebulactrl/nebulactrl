package composites_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/mcp/composites"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func setupDomainObj(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

func setupDomainCount(f *fx, query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// setupDomainFix returns the data.fix text of a tool error.
func setupDomainFix(t *testing.T, err error) string {
	t.Helper()
	var te *mcpkit.Error
	require.True(t, errors.As(err, &te), "%v", err)
	data, ok := te.Data.(map[string]any)
	require.True(t, ok, "error carries data")
	fix, _ := data["fix"].(string)
	return fix
}

func setupDomainNext(m map[string]any, tool string) map[string]any {
	next, _ := m["nextTools"].([]any)
	for _, n := range next {
		if o := n.(map[string]any); o["tool"] == tool {
			return o
		}
	}
	return nil
}

func TestSetupDomainGeneratesUnderTheServersBaseDomain(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})

	m := f.must("setup_domain", map[string]any{"service": "shop/staging/web"})
	require.Empty(t, sectionErrors(m))
	require.Equal(t, "shop/staging/web", m["service"])
	d := setupDomainObj(m, "domain")
	require.Equal(t, "web-staging-shop.apps.example.com", d["hostname"])
	require.Equal(t, true, d["generated"])
	require.Equal(t, true, d["created"])
	require.Equal(t, "pending", d["status"])
	require.EqualValues(t, 80, d["targetPort"])
	require.Nil(t, d["dnsOk"], "no DNS check has run yet")

	// Under the base domain: no DNS action.
	dns := setupDomainObj(m, "dns")
	require.Equal(t, false, dns["actionNeeded"])
	require.Contains(t, dns["guidance"], "no DNS action is needed")
	require.Equal(t, "203.0.113.7", dns["serverIp"])
	require.Equal(t, "A web-staging-shop.apps.example.com → 203.0.113.7", dns["record"])

	// Staged: generation ahead, apply_changes carries the expected generation.
	st := setupDomainObj(m, "staging")
	require.Equal(t, true, st["staged"])
	row, err := f.reload(web.ID)
	require.NoError(t, err)
	require.Greater(t, row.Generation, row.AppliedGeneration)
	require.EqualValues(t, row.Generation, st["generation"])
	require.Contains(t, st["message"], "next apply_changes")
	apply := setupDomainNext(m, "apply_changes")
	require.NotNil(t, apply)
	svcs := apply["args"].(map[string]any)["services"].([]any)
	require.EqualValues(t, row.Generation, svcs[0].(map[string]any)["expectedGeneration"])
	require.NotNil(t, setupDomainNext(m, "review_changes"))

	proxy := setupDomainObj(m, "proxy")
	require.Equal(t, "agent1", proxy["server"])
	require.Equal(t, true, proxy["available"])
	require.Equal(t, false, setupDomainObj(m, "probe")["requested"])

	// Calling again reports the same domain instead of adding another.
	again := f.must("setup_domain", map[string]any{"service": "web"})
	require.Equal(t, false, setupDomainObj(again, "domain")["created"])
	require.Equal(t, d["id"], setupDomainObj(again, "domain")["id"])
	require.Equal(t, 1, setupDomainCount(f, "SELECT count(*) FROM domains WHERE service_id = $1", web.ID))
}

func TestSetupDomainExplicitHostnameNeedsAnARecord(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})

	m := f.must("setup_domain", map[string]any{"service": "web", "hostname": "App.Example.org", "targetPort": 80, "detail": "full"})
	d := setupDomainObj(m, "domain")
	require.Equal(t, "app.example.org", d["hostname"])
	require.Equal(t, false, d["generated"])
	require.Equal(t, "serve", d["kind"], "full detail adds routing settings")
	dns := setupDomainObj(m, "dns")
	require.Equal(t, true, dns["actionNeeded"])
	require.Equal(t, "A app.example.org → 203.0.113.7", dns["record"])
	require.Contains(t, dns["guidance"], "A app.example.org → 203.0.113.7")
	require.Contains(t, m["summary"], "DNS action needed")
	require.Equal(t, 1, setupDomainCount(f, "SELECT count(*) FROM domains WHERE service_id = $1", web.ID))

	// Once the DNS check passed, nothing is left to do.
	_, err := f.pool.Exec(context.Background(), "UPDATE domains SET dns_ok = true, dns_checked_at = now() WHERE hostname = 'app.example.org'")
	require.NoError(t, err)
	m = f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	require.Equal(t, true, setupDomainObj(m, "domain")["dnsOk"])
	require.NotEmpty(t, setupDomainObj(m, "domain")["dnsCheckedAt"])
	require.Equal(t, false, setupDomainObj(m, "dns")["actionNeeded"])
}

func TestSetupDomainReportsCloudflareState(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	exec := func(status string) {
		_, err := f.pool.Exec(context.Background(), "UPDATE domains SET cf_zone_id = 'z1', cf_zone_name = 'example.org', cf_status = $1 WHERE hostname = 'app.example.org'", status)
		require.NoError(t, err)
	}
	exec("managed")
	m := f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	cf := setupDomainObj(setupDomainObj(m, "dns"), "cloudflare")
	require.Equal(t, true, cf["connected"])
	require.Equal(t, "example.org", cf["zone"])
	require.Equal(t, "managed", cf["status"])
	require.Equal(t, false, setupDomainObj(m, "dns")["actionNeeded"])

	exec("wrong_target")
	m = f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	require.Equal(t, true, setupDomainObj(m, "dns")["actionNeeded"])
	require.Contains(t, setupDomainObj(m, "dns")["guidance"], "Cloudflare")

	exec("missing")
	m = f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	require.Equal(t, true, setupDomainObj(m, "dns")["actionNeeded"])
	require.Contains(t, setupDomainObj(m, "dns")["guidance"], "fixDomainDns", "NebulaCtrl manages DNS in a connected zone and creates the record")
	require.NotContains(t, setupDomainObj(m, "dns")["guidance"], "adopt")

	// A record NebulaCtrl did not create is replaced only on an explicit action.
	exec("conflict")
	m = f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	require.Equal(t, "conflict", setupDomainObj(setupDomainObj(m, "dns"), "cloudflare")["status"])
	require.Equal(t, true, setupDomainObj(m, "dns")["actionNeeded"])
	require.Contains(t, setupDomainObj(m, "dns")["guidance"], "fixDomainDns")
	require.Contains(t, setupDomainObj(m, "dns")["guidance"], `"adopt": true`)
}

func TestSetupDomainConflictIsAStructuredError(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.service(svcOpts{env: f.staging, slug: "api"})
	f.must("setup_domain", map[string]any{"service": "api", "hostname": "taken.example.org"})

	_, err := f.call(f.admin, "setup_domain", map[string]any{"service": "web", "hostname": "taken.example.org"})
	require.Equal(t, "domain_conflict", errCode(err))
	fix := setupDomainFix(t, err)
	require.Contains(t, fix, "already attached")
	require.NotContains(t, err.Error(), "api", "the owner of the hostname is not revealed")
	require.Equal(t, 0, setupDomainCount(f, "SELECT count(*) FROM domains d JOIN services s ON s.id = d.service_id WHERE s.slug = 'web'"))
}

func TestSetupDomainNoBaseDomain(t *testing.T) {
	f := newFx(t)
	srv, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "bare"})
	require.NoError(t, err)
	f.service(svcOpts{env: f.staging, slug: "web", server: &srv})

	_, err = f.call(f.admin, "setup_domain", map[string]any{"service": "web"})
	require.Equal(t, "no_base_domain", errCode(err))
	require.Contains(t, setupDomainFix(t, err), "base domain")
	require.Contains(t, setupDomainFix(t, err), "explicit `hostname`")

	// An explicit hostname still works; the server's IP is unknown, so the
	// guidance is generic and points at the domain's own status message.
	m := f.must("setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	dns := setupDomainObj(m, "dns")
	require.Equal(t, true, dns["actionNeeded"])
	require.Equal(t, "", dns["record"])
	require.NotEmpty(t, dns["guidance"])
	require.Contains(t, m["warnings"], "The server has no public IP set, so DNS cannot be checked; set it in the server settings.")
}

func TestSetupDomainInvalidHostname(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	for _, bad := range []string{"https://app.example.org", "nodots", "192.168.1.1", "app.example.org/path"} {
		_, err := f.call(f.admin, "setup_domain", map[string]any{"service": "web", "hostname": bad})
		require.Equal(t, "invalid_hostname", errCode(err), bad)
		require.Contains(t, setupDomainFix(t, err), "bare lowercase hostname", bad)
	}
	require.Equal(t, 0, setupDomainCount(f, "SELECT count(*) FROM domains"))
}

func TestSetupDomainProxyUnavailable(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET proxy_status = 'error', proxy_error = 'port 443 in use' WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	_, err = f.call(f.admin, "setup_domain", map[string]any{"service": "web"})
	require.Equal(t, "proxy_unavailable", errCode(err))
	require.NotContains(t, err.Error(), "port 443 in use", "the proxy's own error is not in the error text")
	var te *mcpkit.Error
	require.True(t, errors.As(err, &te))
	detail := te.Data.(map[string]any)["detail"].(mcpkit.Block)
	require.True(t, detail.Untrusted)
	require.Contains(t, detail.Text, "port 443 in use")
	require.Contains(t, setupDomainFix(t, err), "diagnose_server")

	// An existing domain is still reported, with the proxy condition.
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET proxy_status = 'ok' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	f.must("setup_domain", map[string]any{"service": "web"})
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET proxy_status = 'error', proxy_error = 'port 443 in use' WHERE id = $1", f.server.ID)
	require.NoError(t, err)
	m := f.must("setup_domain", map[string]any{"service": "web"})
	proxy := setupDomainObj(m, "proxy")
	require.Equal(t, "error", proxy["status"])
	require.Equal(t, false, proxy["available"])
	require.Contains(t, proxy["error"].(map[string]any)["text"], "port 443 in use")
	require.NotNil(t, setupDomainNext(m, "diagnose_server"))
}

func TestSetupDomainDegradesWithoutServersRead(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	p := f.principal(policy.ScopeProjectsRead, policy.ScopeProjectsWrite)
	m, err := f.call(p, "setup_domain", map[string]any{"service": "web", "hostname": "app.example.org"})
	require.NoError(t, err)
	require.Equal(t, []string{"server"}, sectionErrors(m))
	e := m["errors"].([]any)[0].(map[string]any)
	require.Equal(t, "forbidden", e["code"])
	require.Contains(t, e["message"], "servers:read")
	dns := setupDomainObj(m, "dns")
	require.Equal(t, "", dns["record"], "the IP is not read without servers:read")
	// The domain's own statusMessage still carries the guidance (with the IP).
	require.Contains(t, setupDomainObj(m, "domain")["statusMessage"].(map[string]any)["text"], "203.0.113.7")
	require.Equal(t, "unknown", setupDomainObj(m, "proxy")["status"])
}

func TestSetupDomainArgumentErrors(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	for name, args := range map[string]map[string]any{
		"no service":             {},
		"bad detail":             {"service": "web", "detail": "everything"},
		"bad port":               {"service": "web", "hostname": "a.example.org", "targetPort": 70000},
		"generate wrong port":    {"service": "web", "targetPort": 9999},
		"unknown field":          {"service": "web", "host": "a.example.org"},
		"service does not exist": {"service": "shop/staging/ghost"},
	} {
		_, err := f.call(f.admin, "setup_domain", args)
		require.Error(t, err, name)
		if name == "service does not exist" {
			require.Equal(t, "not_found", errCode(err), name)
		} else {
			require.Equal(t, "invalid_argument", errCode(err), name)
		}
	}
	// Wrong target port with an explicit hostname is the domain service's 422, with a fix.
	_, err := f.call(f.admin, "setup_domain", map[string]any{"service": "web", "hostname": "a.example.org", "targetPort": 9999})
	require.Equal(t, "invalid_target_port", errCode(err))
	require.NotEmpty(t, setupDomainFix(t, err))
	require.Equal(t, 0, setupDomainCount(f, "SELECT count(*) FROM domains"))
}

func TestSetupDomainAmbiguousSelector(t *testing.T) {
	f := newFx(t)
	f.service(svcOpts{env: f.staging, slug: "web"})
	f.service(svcOpts{env: f.prod, slug: "web"})
	_, err := f.call(f.admin, "setup_domain", map[string]any{"service": "web"})
	require.Equal(t, "ambiguous_selector", errCode(err))
	require.Equal(t, 0, setupDomainCount(f, "SELECT count(*) FROM domains"))
}

// setupDomainWith returns the tool built over a deps copy with a Probe client.
func setupDomainWith(f *fx, probe *http.Client) mcpkit.Tool {
	d := f.deps
	d.Probe = probe
	for _, tl := range composites.Tools(d) {
		if tl.Name == "setup_domain" {
			return tl
		}
	}
	f.t.Fatal("setup_domain missing")
	return mcpkit.Tool{}
}

func setupDomainRun(f *fx, tl mcpkit.Tool, args any) map[string]any {
	f.t.Helper()
	raw, _ := json.Marshal(args)
	out, err := tl.Call(f.ctx(f.admin), f.admin, raw)
	require.NoError(f.t, err)
	b, _ := json.Marshal(out)
	var m map[string]any
	require.NoError(f.t, json.Unmarshal(b, &m))
	return m
}

func TestSetupDomainProbesALiveDomain(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "web-staging-shop.apps.example.com", r.Host)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	tl := setupDomainWith(f, &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}})

	// Freshly staged: probe requested but the domain is not live, no request is made.
	m := setupDomainRun(f, tl, map[string]any{"service": "web", "probe": true})
	pr := setupDomainObj(m, "probe")
	require.Equal(t, true, pr["requested"])
	require.Equal(t, false, pr["done"])
	require.Contains(t, pr["reason"], "not live")
	require.Zero(t, hits.Load())

	// After apply the domain is active (plain HTTP here).
	_, err := f.pool.Exec(context.Background(), "UPDATE domains SET status = 'active', tls_mode = 'none' WHERE service_id = $1", web.ID)
	require.NoError(t, err)
	m = setupDomainRun(f, tl, map[string]any{"service": "web", "probe": true})
	pr = setupDomainObj(m, "probe")
	require.Equal(t, true, pr["done"])
	res := setupDomainObj(pr, "result")
	require.Equal(t, true, res["ok"], pr)
	require.EqualValues(t, 200, res["status"])
	require.Equal(t, "http://web-staging-shop.apps.example.com/", res["url"])
	require.Equal(t, int32(1), hits.Load())
	require.Contains(t, m["summary"], "Probe: HTTP 200")
	require.Equal(t, true, setupDomainObj(m, "staging")["live"])

	// Without probe:true nothing is requested.
	setupDomainRun(f, tl, map[string]any{"service": "web"})
	require.Equal(t, int32(1), hits.Load())
}

func TestSetupDomainProbeNeedsADeployment(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web", undeployed: true})
	f.must("setup_domain", map[string]any{"service": "web"})
	_, err := f.pool.Exec(context.Background(), "UPDATE domains SET status = 'active' WHERE service_id = $1", web.ID)
	require.NoError(t, err)
	m := f.must("setup_domain", map[string]any{"service": "web", "probe": true})
	require.Equal(t, false, setupDomainObj(m, "probe")["done"])
	require.Contains(t, setupDomainObj(m, "probe")["reason"], "no successful deployment")
}

// With the default client a hostname that resolves to loopback is refused and
// nothing is requested.
func TestSetupDomainProbeRefusesLoopback(t *testing.T) {
	f := newFx(t)
	web := f.service(svcOpts{env: f.staging, slug: "web"})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	// A domain whose name resolves to 127.0.0.1 only.
	_, err := f.q.InsertDomain(context.Background(), db.InsertDomainParams{
		ServiceID: web.ID, Hostname: "localhost", TargetPort: 80, StatusMessage: "x", Kind: "serve", TlsMode: "none", Options: []byte("{}"),
	})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE domains SET status = 'active' WHERE service_id = $1", web.ID)
	require.NoError(t, err)

	m := f.must("setup_domain", map[string]any{"service": "shop/staging/web", "hostname": "localhost", "probe": true})
	pr := setupDomainObj(m, "probe")
	require.Equal(t, true, pr["done"])
	res := setupDomainObj(pr, "result")
	require.Equal(t, false, res["ok"])
	require.Contains(t, res["error"], "refused")
	require.Zero(t, hits.Load())
	require.Contains(t, m["summary"], "Probe failed")
}
