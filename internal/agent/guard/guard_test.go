package guard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	countryFixture = "../traffic/testdata/geoip/GeoIP2-Country-Test.mmdb"
	ipGB           = "81.2.69.142"
	ipUS           = "216.160.83.56"
	ipSE           = "89.160.20.112"
)

func cond(field, op string, values ...string) agentprotocol.FirewallCondition {
	return agentprotocol.FirewallCondition{Field: field, Op: op, Values: values}
}

func named(field, op, name string, values ...string) agentprotocol.FirewallCondition {
	return agentprotocol.FirewallCondition{Field: field, Op: op, Name: name, Values: values}
}

func not(c agentprotocol.FirewallCondition) agentprotocol.FirewallCondition {
	c.Not = true
	return c
}

func rule(id, action string, conds ...agentprotocol.FirewallCondition) agentprotocol.FirewallRule {
	return agentprotocol.FirewallRule{ID: id, Name: "rule " + id, Action: action, Conditions: conds}
}

func fw(rules ...agentprotocol.FirewallRule) *agentprotocol.Firewall {
	return &agentprotocol.Firewall{Rules: rules}
}

type harness struct {
	t   *testing.T
	dir string
	s   *Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	s, err := NewServer(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return &harness{t: t, dir: dir, s: s}
}

// write stores rules as the agent does and returns the file's hash.
func (h *harness) write(r Rules) string {
	h.t.Helper()
	raw, err := Encode(r)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(filepath.Join(h.dir, RulesFile), raw, 0o644))
	return Hash(raw)
}

func (h *harness) writeRaw(raw string) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(filepath.Join(h.dir, RulesFile), []byte(raw), 0o644))
}

func (h *harness) load(r Rules) string {
	h.t.Helper()
	hash := h.write(r)
	require.NoError(h.t, h.s.Reload())
	return hash
}

func (h *harness) installGeo(name string) {
	h.t.Helper()
	raw, err := os.ReadFile(countryFixture)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(filepath.Join(h.dir, name), raw, 0o644))
}

// visit is a request as Traefik's forwardAuth sends it.
type visit struct {
	route, client string
	method, uri   string
	forwardedFor  string
	cfIP          string
	header        map[string]string
}

func (h *harness) check(v visit) *httptest.ResponseRecorder {
	h.t.Helper()
	target := "/v1/check/" + v.route
	if v.client != "" {
		target += "?client=" + v.client
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if v.method != "" {
		req.Header.Set("X-Forwarded-Method", v.method)
	}
	if v.uri != "" {
		req.Header.Set("X-Forwarded-Uri", v.uri)
	}
	if v.forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", v.forwardedFor)
	}
	if v.cfIP != "" {
		req.Header.Set("Cf-Connecting-Ip", v.cfIP)
	}
	for k, val := range v.header {
		req.Header.Set(k, val)
	}
	rec := httptest.NewRecorder()
	h.s.ServeHTTP(rec, req)
	return rec
}

func (h *harness) status(v visit) int { return h.check(v).Code }

func TestCheckBlocksWithThe403PageAndAllowsTheRest(t *testing.T) {
	h := newHarness(t)
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{
		"r1": fw(
			rule("office", agentprotocol.FirewallAllow, cond("ip", "in", "203.0.113.0/24")),
			rule("admin", agentprotocol.FirewallBlock, cond("path", "prefix", "/admin")),
			rule("bots", agentprotocol.FirewallBlock, cond("userAgent", "contains", "BadBot")),
		),
	}})

	blocked := h.check(visit{route: "r1", client: "peer", method: "GET", uri: "/admin/users", forwardedFor: "198.51.100.9"})
	require.Equal(t, http.StatusForbidden, blocked.Code)
	require.Equal(t, "text/html; charset=utf-8", blocked.Header().Get("Content-Type"))
	require.Equal(t, "no-store", blocked.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", blocked.Header().Get("X-Content-Type-Options"))
	require.Contains(t, blocked.Header().Get("Content-Security-Policy"), "default-src 'none'")
	require.Contains(t, blocked.Body.String(), "<html")
	require.Contains(t, blocked.Body.String(), "Access blocked", "the 403 page of the pages bundle")

	allowed := h.check(visit{route: "r1", client: "peer", method: "GET", uri: "/shop", forwardedFor: "198.51.100.9"})
	require.Equal(t, http.StatusOK, allowed.Code)
	require.Empty(t, allowed.Body.String())

	require.Equal(t, http.StatusOK, h.status(visit{route: "r1", client: "peer", method: "GET", uri: "/admin", forwardedFor: "203.0.113.50"}),
		"an allow rule above the block rule lets the office in")
	require.Equal(t, http.StatusForbidden, h.status(visit{route: "r1", client: "peer", method: "GET", uri: "/", forwardedFor: "198.51.100.9", header: map[string]string{"User-Agent": "xx BADBOT xx"}}))
	require.Equal(t, http.StatusOK, h.status(visit{route: "r1", client: "peer", method: "GET", uri: "/", forwardedFor: "203.0.113.50", header: map[string]string{"User-Agent": "badbot"}}))
}

func TestCheckUnknownRoutePassesAndOtherPathsAre404(t *testing.T) {
	h := newHarness(t)
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": fw(rule("all", agentprotocol.FirewallBlock, not(cond("path", "exact", "/never"))))}})
	require.Equal(t, http.StatusForbidden, h.status(visit{route: "r1", method: "GET", uri: "/"}))
	require.Equal(t, http.StatusOK, h.status(visit{route: "other", method: "GET", uri: "/"}), "a route the guard does not know goes through")
	require.Equal(t, http.StatusOK, h.status(visit{route: "", method: "GET", uri: "/"}))

	for _, path := range []string{"/", "/v1", "/v1/unknown", "/metrics"} {
		rec := httptest.NewRecorder()
		h.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}

func TestCheckRebuildsTheVisitorRequestFromForwardedHeaders(t *testing.T) {
	h := newHarness(t)
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{
		"r1": fw(
			rule("method", agentprotocol.FirewallBlock, cond("method", "in", "DELETE")),
			rule("exact", agentprotocol.FirewallBlock, cond("path", "exact", "/.env")),
			rule("query", agentprotocol.FirewallBlock, named("query", "equals", "token", "secret")),
			rule("present", agentprotocol.FirewallBlock, named("query", "present", "debug")),
			rule("header", agentprotocol.FirewallBlock, named("header", "equals", "X-Api-Key", "k1")),
			rule("ip", agentprotocol.FirewallBlock, cond("ip", "in", "192.0.2.0/24")),
		),
	}})
	cases := []struct {
		name string
		v    visit
		want int
	}{
		{"method from X-Forwarded-Method", visit{method: "DELETE", uri: "/x", forwardedFor: "198.51.100.1"}, 403},
		{"another method", visit{method: "GET", uri: "/x", forwardedFor: "198.51.100.1"}, 200},
		{"path from X-Forwarded-Uri", visit{method: "GET", uri: "/.env", forwardedFor: "198.51.100.1"}, 403},
		{"the query string is not part of the path", visit{method: "GET", uri: "/.env?x=1", forwardedFor: "198.51.100.1"}, 403},
		{"the path is decoded", visit{method: "GET", uri: "/%2eenv", forwardedFor: "198.51.100.1"}, 403},
		{"path near miss", visit{method: "GET", uri: "/.env/", forwardedFor: "198.51.100.1"}, 200},
		{"query value", visit{method: "GET", uri: "/x?a=1&token=secret", forwardedFor: "198.51.100.1"}, 403},
		{"query value is exact", visit{method: "GET", uri: "/x?token=secret2", forwardedFor: "198.51.100.1"}, 200},
		{"query present without a value", visit{method: "GET", uri: "/x?debug", forwardedFor: "198.51.100.1"}, 403},
		{"request headers are the visitor's", visit{method: "GET", uri: "/x", forwardedFor: "198.51.100.1", header: map[string]string{"x-api-key": "k1"}}, 403},
		{"first X-Forwarded-For entry", visit{method: "GET", uri: "/x", forwardedFor: "192.0.2.77, 198.51.100.1"}, 403},
		{"later entries are proxies", visit{method: "GET", uri: "/x", forwardedFor: "198.51.100.1, 192.0.2.77"}, 200},
		{"X-Forwarded-For with spaces", visit{method: "GET", uri: "/x", forwardedFor: "  192.0.2.5  "}, 403},
		{"a missing method matches no method rule", visit{uri: "/x", forwardedFor: "198.51.100.1"}, 200},
		{"a missing uri is /", visit{method: "GET", forwardedFor: "198.51.100.1"}, 200},
		{"an unparsable address matches no ip rule", visit{method: "GET", uri: "/x", forwardedFor: "not-an-ip"}, 200},
		{"no address at all", visit{method: "GET", uri: "/x"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.v.route, c.v.client = "r1", "peer"
			require.Equal(t, c.want, h.status(c.v))
		})
	}
}

func TestCheckClientSourceSelectsPeerOrCloudflareAddress(t *testing.T) {
	h := newHarness(t)
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{
		"r1": fw(rule("ip", agentprotocol.FirewallBlock, cond("ip", "in", "192.0.2.0/24", "2001:db8::/32"))),
	}})
	// Behind Cloudflare the peer (X-Forwarded-For) is Cloudflare's: only
	// Cf-Connecting-IP names the visitor.
	for _, c := range []struct {
		name string
		v    visit
		want int
	}{
		{"cf: the header decides", visit{client: "cf", forwardedFor: "198.51.100.1", cfIP: "192.0.2.9"}, 403},
		{"cf: a Cloudflare peer address in the blocked range is not the visitor", visit{client: "cf", forwardedFor: "192.0.2.9", cfIP: "198.51.100.1"}, 200},
		{"cf: ipv6 visitor", visit{client: "cf", forwardedFor: "198.51.100.1", cfIP: "2001:db8::77"}, 403},
		{"cf: no header", visit{client: "cf", forwardedFor: "192.0.2.9"}, 200},
		{"peer: X-Forwarded-For decides", visit{client: "peer", forwardedFor: "192.0.2.9", cfIP: "198.51.100.1"}, 403},
		{"peer: a client-sent Cf-Connecting-IP is ignored", visit{client: "peer", forwardedFor: "198.51.100.1", cfIP: "192.0.2.9"}, 200},
		{"default is the peer", visit{forwardedFor: "192.0.2.9", cfIP: "198.51.100.1"}, 403},
		{"unknown source is the peer", visit{client: "bogus", forwardedFor: "198.51.100.1", cfIP: "192.0.2.9"}, 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.v.route, c.v.method, c.v.uri = "r1", "GET", "/"
			require.Equal(t, c.want, h.status(c.v))
		})
	}
}

func TestReadyReportsTheLoadedRulesHash(t *testing.T) {
	h := newHarness(t)
	ready := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ready"+query, nil))
		return rec
	}

	// Nothing loaded yet: the agent's wait for its rules does not succeed.
	require.Equal(t, http.StatusServiceUnavailable, ready("?rules=abcdef0123456789").Code)
	require.NoError(t, h.s.Reload(), "no rules file yet is not an error")
	require.Equal(t, http.StatusServiceUnavailable, ready("?rules=abcdef0123456789").Code)

	hash := h.write(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": fw(rule("a", agentprotocol.FirewallBlock, cond("method", "in", "GET")))}})
	require.Equal(t, http.StatusServiceUnavailable, ready("?rules="+hash).Code, "written but not loaded yet")
	require.NoError(t, h.s.Reload())

	rec := ready("?rules=" + hash)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, hash, rec.Body.String())
	require.Equal(t, http.StatusServiceUnavailable, ready("?rules=0000000000000000").Code, "another document is loaded")
	require.Equal(t, http.StatusOK, ready("").Code, "no hash asked for: the server is up")

	hash2 := h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": fw(rule("a", agentprotocol.FirewallBlock, cond("method", "in", "POST")))}})
	require.NotEqual(t, hash, hash2)
	require.Equal(t, http.StatusServiceUnavailable, ready("?rules="+hash).Code, "the old document is gone")
	require.Equal(t, http.StatusOK, ready("?rules="+hash2).Code)
}

func TestCountryRulesAreDroppedWithoutAGeoIPDatabase(t *testing.T) {
	h := newHarness(t)
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{
		"r1": fw(
			rule("notde", agentprotocol.FirewallBlock, not(cond("country", "in", "DE"))),
			rule("cn", agentprotocol.FirewallBlock, cond("country", "in", "CN")),
			rule("cnallow", agentprotocol.FirewallAllow, cond("country", "in", "US"), cond("path", "prefix", "/admin")),
			rule("admin", agentprotocol.FirewallBlock, cond("path", "prefix", "/admin")),
		),
		"countries": fw(rule("only", agentprotocol.FirewallBlock, cond("country", "in", "CN"))),
	}})

	for _, ip := range []string{ipGB, ipUS, ipSE, "198.51.100.1", "10.0.0.1"} {
		require.Equal(t, http.StatusOK, h.status(visit{route: "r1", client: "peer", method: "GET", uri: "/", forwardedFor: ip}),
			"'block when not in DE' must not block %s without a database", ip)
		require.Equal(t, http.StatusOK, h.status(visit{route: "countries", client: "peer", method: "GET", uri: "/", forwardedFor: ip}))
	}
	// The rules without countries still apply, and the allow rule that needed
	// a country does not shield /admin.
	require.Equal(t, http.StatusForbidden, h.status(visit{route: "r1", client: "peer", method: "GET", uri: "/admin", forwardedFor: ipUS}))
}

func TestCountryRulesUseTheGeoIPDatabase(t *testing.T) {
	h := newHarness(t)
	h.installGeo("geo-country-1.mmdb")
	h.load(Rules{Geo: "geo-country-1.mmdb", Routes: map[string]*agentprotocol.Firewall{
		"block": fw(rule("gb", agentprotocol.FirewallBlock, cond("country", "in", "GB", "BT"))),
		"only":  fw(rule("notde", agentprotocol.FirewallBlock, not(cond("country", "in", "GB", "US")))),
		"mixed": fw(
			rule("us", agentprotocol.FirewallAllow, cond("country", "in", "US")),
			rule("admin", agentprotocol.FirewallBlock, cond("path", "prefix", "/admin")),
		),
	}})
	status := func(route, ip, uri string) int {
		return h.status(visit{route: route, client: "peer", method: "GET", uri: uri, forwardedFor: ip})
	}
	require.Equal(t, 403, status("block", ipGB, "/"))
	require.Equal(t, 200, status("block", ipUS, "/"))
	require.Equal(t, 200, status("block", ipSE, "/"))
	require.Equal(t, 403, status("block", "::ffff:"+ipGB, "/"), "an IPv4-mapped address is looked up as IPv4")
	require.Equal(t, 200, status("block", "10.1.2.3", "/"), "a private address has no country")
	require.Equal(t, 200, status("block", "198.51.100.1", "/"), "an address of no network has no country")

	require.Equal(t, 200, status("only", ipGB, "/"))
	require.Equal(t, 200, status("only", ipUS, "/"))
	require.Equal(t, 403, status("only", ipSE, "/"))

	require.Equal(t, 200, status("mixed", ipUS, "/admin"), "the US allow rule is above the block")
	require.Equal(t, 403, status("mixed", ipGB, "/admin"))
	require.Equal(t, 200, status("mixed", ipGB, "/other"))

	// The 403 for a country block is the same page.
	rec := h.check(visit{route: "block", client: "peer", method: "GET", uri: "/", forwardedFor: ipGB})
	require.Contains(t, rec.Body.String(), "Access blocked")

	// Country rules also work behind Cloudflare, reading Cf-Connecting-IP.
	require.Equal(t, 403, h.status(visit{route: "block", client: "cf", method: "GET", uri: "/", forwardedFor: ipUS, cfIP: ipGB}))
	require.Equal(t, 200, h.status(visit{route: "block", client: "cf", method: "GET", uri: "/", forwardedFor: ipGB, cfIP: ipUS}))
}

func TestCountryRulesAppearOnceTheDatabaseFileDoes(t *testing.T) {
	h := newHarness(t)
	rules := Rules{Geo: "geo-country-1.mmdb", Routes: map[string]*agentprotocol.Firewall{
		"r1": fw(rule("gb", agentprotocol.FirewallBlock, cond("country", "in", "GB"))),
	}}
	hash := h.write(rules)
	// The rules name a database the agent has not copied yet: the guard loads
	// them without the country rules (and says it has the document).
	require.NoError(t, h.s.Reload())
	v := visit{route: "r1", client: "peer", method: "GET", uri: "/", forwardedFor: ipGB}
	require.Equal(t, 200, h.status(v))
	rec := httptest.NewRecorder()
	h.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ready?rules="+hash, nil))
	require.Equal(t, 200, rec.Code)

	// The next tick finds the file and enforces the country rule.
	h.installGeo("geo-country-1.mmdb")
	require.NoError(t, h.s.Reload())
	require.Equal(t, 403, h.status(v))
}

func TestReloadSwitchesDatabasesAndRemovesStaleOnes(t *testing.T) {
	h := newHarness(t)
	h.installGeo("geo-country-1.mmdb")
	fwGB := fw(rule("gb", agentprotocol.FirewallBlock, cond("country", "in", "GB")))
	h.load(Rules{Geo: "geo-country-1.mmdb", Routes: map[string]*agentprotocol.Firewall{"r1": fwGB}})
	v := visit{route: "r1", client: "peer", method: "GET", uri: "/", forwardedFor: ipGB}
	require.Equal(t, 403, h.status(v))

	h.installGeo("geo-country-2.mmdb")
	h.load(Rules{Geo: "geo-country-2.mmdb", Routes: map[string]*agentprotocol.Firewall{"r1": fwGB}})
	require.Equal(t, 403, h.status(v))
	require.NoFileExists(t, filepath.Join(h.dir, "geo-country-1.mmdb"), "the replaced database is deleted")
	require.FileExists(t, filepath.Join(h.dir, "geo-country-2.mmdb"))
	require.FileExists(t, filepath.Join(h.dir, RulesFile))

	// Rules without a database drop the country rules again.
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": fwGB}})
	require.Equal(t, 200, h.status(v))

	// A Geo that is a path is not opened: no traversal out of the directory.
	h.load(Rules{Geo: "../traffic/testdata/geoip/GeoIP2-Country-Test.mmdb", Routes: map[string]*agentprotocol.Firewall{"r1": fwGB}})
	require.Equal(t, 200, h.status(v))
}

func TestReloadKeepsThePreviousRulesWhenTheFileDoesNotParse(t *testing.T) {
	h := newHarness(t)
	blockAll := fw(rule("all", agentprotocol.FirewallBlock, cond("method", "in", "GET")))
	hash := h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": blockAll}})
	v := visit{route: "r1", client: "peer", method: "GET", uri: "/", forwardedFor: "198.51.100.1"}
	require.Equal(t, 403, h.status(v))

	// A half-written file.
	h.writeRaw(`{"routes":{"r1":{"rules":[{"id":"all","na`)
	require.ErrorContains(t, h.s.Reload(), "parse rules")
	require.Equal(t, 403, h.status(v), "the previous rules stay in force")
	rec := httptest.NewRecorder()
	h.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ready?rules="+hash, nil))
	require.Equal(t, 200, rec.Code, "and are still the loaded document")

	// A document that parses but holds an invalid firewall is refused whole.
	h.writeRaw(`{"routes":{"r1":{"rules":[{"id":"x","name":"x","action":"block","conditions":[{"field":"path","op":"regex","values":["(unclosed"]}]}]}}}`)
	require.ErrorContains(t, h.s.Reload(), "route r1")
	require.Equal(t, 403, h.status(v))

	// Trying again with the same broken file stays an error (not recorded as loaded).
	require.Error(t, h.s.Reload())

	// A good file replaces them.
	allow := fw(rule("none", agentprotocol.FirewallBlock, cond("method", "in", "DELETE")))
	hash2 := h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": allow}})
	require.NotEqual(t, hash, hash2)
	require.Equal(t, 200, h.status(v))
}

func TestReloadWithoutAFileLeavesTheServerEmpty(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, h.s.Reload())
	require.Equal(t, 200, h.status(visit{route: "r1", method: "GET", uri: "/"}))
}

func TestReloadDropsRoutesRemovedFromTheFile(t *testing.T) {
	h := newHarness(t)
	block := fw(rule("all", agentprotocol.FirewallBlock, cond("method", "in", "GET")))
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": block, "r2": block}})
	v := visit{client: "peer", method: "GET", uri: "/", forwardedFor: "198.51.100.1"}
	require.Equal(t, 403, h.status(visit{route: "r1", client: v.client, method: v.method, uri: v.uri, forwardedFor: v.forwardedFor}))
	require.Equal(t, 403, h.status(visit{route: "r2", client: v.client, method: v.method, uri: v.uri, forwardedFor: v.forwardedFor}))

	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r2": block}})
	require.Equal(t, 200, h.status(visit{route: "r1", client: v.client, method: v.method, uri: v.uri, forwardedFor: v.forwardedFor}), "a removed route goes through")
	require.Equal(t, 403, h.status(visit{route: "r2", client: v.client, method: v.method, uri: v.uri, forwardedFor: v.forwardedFor}))

	// A route with a nil or empty firewall never blocks.
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": nil, "r2": {}}})
	require.Equal(t, 200, h.status(visit{route: "r1", method: "GET", uri: "/"}))
	require.Equal(t, 200, h.status(visit{route: "r2", method: "GET", uri: "/"}))
}

func TestDisabledRulesAreNotEvaluated(t *testing.T) {
	h := newHarness(t)
	off := rule("off", agentprotocol.FirewallBlock, cond("method", "in", "GET"))
	off.Disabled = true
	h.load(Rules{Routes: map[string]*agentprotocol.Firewall{"r1": fw(off)}})
	require.Equal(t, 200, h.status(visit{route: "r1", method: "GET", uri: "/"}))
}

func TestEncodeAndHash(t *testing.T) {
	r := Rules{Geo: "geo-x.mmdb", Routes: map[string]*agentprotocol.Firewall{
		"r1": fw(rule("a", agentprotocol.FirewallBlock, cond("path", "regex", `a<b>&c`), cond("userAgent", "contains", "x"))),
	}}
	raw, err := Encode(r)
	require.NoError(t, err)
	require.Contains(t, string(raw), `a<b>&c`, "HTML is not escaped: regular expressions stay readable")
	require.True(t, strings.HasSuffix(string(raw), "\n"))

	var back Rules
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, r, back)

	require.Regexp(t, `^[0-9a-f]{16}$`, Hash(raw))
	require.Equal(t, Hash(raw), Hash(append([]byte(nil), raw...)))
	r.Geo = ""
	raw2, err := Encode(r)
	require.NoError(t, err)
	require.NotEqual(t, Hash(raw), Hash(raw2))

	// The encoding is deterministic (maps are sorted), so the agent's hash of
	// the document it writes equals the hash the guard reports.
	a, _ := Encode(Rules{Routes: map[string]*agentprotocol.Firewall{"b": fw(), "a": fw(), "c": fw()}})
	b, _ := Encode(Rules{Routes: map[string]*agentprotocol.Firewall{"c": fw(), "a": fw(), "b": fw()}})
	require.Equal(t, string(a), string(b))
}
