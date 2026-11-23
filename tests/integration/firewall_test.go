//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// cidrRE is the only form a client address takes in a request line: the /24
// (IPv4) or /48 (IPv6) network, never the address itself.
var cidrRE = regexp.MustCompile(`^(\d{1,3}(\.\d{1,3}){3}/24|[0-9a-f:]+/48)$`)

// guardAgent builds the agent for the edge guard container (a Linux binary of
// cmd/agent) and serves a GeoIP database for the agent's geolocation, so the
// stack's agent can enforce a firewall with a country condition.
func guardAgent(t *testing.T, arch string) func(*AgentOptions) {
	t.Helper()
	root, err := repoRoot()
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "nebulactrl-agent")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	require.NoError(t, goBuildLinux(ctx, root, arch, "./cmd/agent", bin))
	fi, err := os.Stat(bin)
	require.NoError(t, err)

	// The harness has no network access to DB-IP: the agent downloads the
	// MaxMind test database (a few made-up networks; the client of the test,
	// a private address, is in none of them) from here.
	raw, err := os.ReadFile(filepath.Join(root, "internal", "agent", "traffic", "testdata", "geoip", "GeoIP2-Country-Test.mmdb"))
	require.NoError(t, err)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, err = zw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	geoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/dbip-country-lite-") && strings.HasSuffix(r.URL.Path, ".mmdb.gz") {
			_, _ = w.Write(gz.Bytes())
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(geoSrv.Close)

	return func(o *AgentOptions) {
		o.GuardBinary = bin
		o.GuardBinaryID = fmt.Sprintf("itest/%d/%d", fi.Size(), fi.ModTime().Unix())
		o.GeoDBIPBase = geoSrv.URL
	}
}

// requestLines returns the stored request lines of a service that match q
// ("stream:requests" is added: the blocks view's logsQuery), decoded. The
// request store is read next to the runtime one, as the Logs explorer does.
func (s *Stack) requestLines(serviceID, q string) []agentprotocol.RequestRecord {
	s.t.Helper()
	var out []agentprotocol.RequestRecord
	for _, l := range s.historyLines(serviceID, url.Values{"q": {"stream:requests " + q}, "sources": {"runtime,requests"}, "limit": {"500"}}) {
		var rec agentprotocol.RequestRecord
		require.NoError(s.t, json.Unmarshal([]byte(l), &rec), "a request line is the JSON of a RequestRecord: %s", l)
		out = append(out, rec)
	}
	return out
}

func (s *Stack) firewallBlocks(serviceID string) J {
	s.t.Helper()
	r := s.Do("GET", "/services/"+serviceID+"/firewall/blocks?range=24h", nil)
	require.Equal(s.t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON()
}

// ruleCount is the number of requests the blocks view counts for rule id.
func ruleCount(b J, id string) int {
	for _, e := range itemsOf(b["byRule"]) {
		if e.Str("id") == id {
			return e.Int("requests")
		}
	}
	return 0
}

func ruleEntry(t *testing.T, b J, id string) J {
	t.Helper()
	for _, e := range itemsOf(b["byRule"]) {
		if e.Str("id") == id {
			return e
		}
	}
	require.Failf(t, "rule missing", "no byRule entry for %q in %v", id, b["byRule"])
	return nil
}

// waitStatus requests path through Traefik until the proxy answers want: a
// firewall change reaches the proxy a moment after the API answers.
func waitStatus(t *testing.T, px *ProxyClient, path string, want int) {
	t.Helper()
	var got int
	var err error
	require.True(t, eventually(60*time.Second, 300*time.Millisecond, func() bool {
		got, _, err = px.Get(path)
		return err == nil && got == want
	}), "GET %s: want %d, last %d (err %v)", path, want, got, err)
}

// 15. the firewall's blocks are attributed: a request blocked by the standard
// protection, by a rule of the service and (edge guard, country condition) by a
// rule the guard decides reaches the traffic aggregates and the request lines
// with the rule that blocked it and the ORIGINAL path (never the pages server's
// /err/403), and no client address is stored whole.
func TestFirewallBlocksAreAttributed(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	agentHook := guardAgent(t, app.arch)
	t.Cleanup(func() { _, _ = sh(context.Background(), "", "docker", "rm", "-f", agentprotocol.GuardContainer) })
	s := ExclusiveStack(t, StackOptions{Agent: agentHook})

	svc := s.NewService(SvcSpec{
		Name: "web", Image: app.AppImage, Domain: s.Host(),
		Vars: map[string]string{"VERSION": "v1"},
	})
	s.DeployOK(svc.ID)
	px := NewProxyClient(s.Host())
	px.WaitBody(t, "/", "v1", 30*time.Second)
	fwPath := "/services/" + svc.ID + "/firewall"

	pathCond := func(op string, values ...string) map[string]any {
		return map[string]any{"field": "path", "op": op, "values": values}
	}
	ownBlock := map[string]any{"id": "ownblock", "name": "No admin tools", "action": "block", "conditions": []any{pathCond("prefix", "/blocked")}}
	allowHome := map[string]any{"id": "allowhome", "name": "Home page", "action": "allow", "conditions": []any{pathCond("exact", "/")}}

	// -- routes: the standard protection and one rule of the service --------
	r := s.Do("PUT", fwPath, map[string]any{"rules": []any{ownBlock, allowHome}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "routes", r.JSON().Str("enforcement"))

	// The rules reach the proxy asynchronously: the first 403 says they are live.
	waitStatus(t, px, "/blocked/x", http.StatusForbidden)
	waitStatus(t, px, "/.env", http.StatusForbidden)
	envBlocks, ownBlocks := 1, 1
	for range 3 {
		code, _, err := px.Get("/.env")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, code, "standard protection blocks /.env")
		envBlocks++
	}
	for range 2 {
		code, _, err := px.Get("/blocked/x")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, code, "the service's own rule blocks /blocked/x")
		ownBlocks++
	}
	code, body, err := px.Get("/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, "the allow rule (and no block) lets / through")
	require.Equal(t, "v1", body)

	// Counted from the proxy's access log, within the usual aggregation delay
	// (see TestTrafficAnalytics).
	var blocks J
	ok := eventually(150*time.Second, 3*time.Second, func() bool {
		blocks = s.firewallBlocks(svc.ID)
		return ruleCount(blocks, "stdsecrets") >= envBlocks && ruleCount(blocks, "ownblock") >= ownBlocks
	})
	require.True(t, ok, "blocks never reached /.env x%d and /blocked/x x%d: %v", envBlocks, ownBlocks, blocks)
	require.True(t, blocks.Bool("trafficAnalytics"))
	require.Equal(t, "stream:requests @blocked_by:*", blocks.Str("logsQuery"))
	require.GreaterOrEqual(t, blocks.Int("total"), envBlocks+ownBlocks)
	std := ruleEntry(t, blocks, "stdsecrets")
	require.Equal(t, "standard", std.Str("kind"))
	require.Equal(t, "Secret files", std.Str("name"))
	require.Equal(t, "block", std.Str("action"))
	own := ruleEntry(t, blocks, "ownblock")
	require.Equal(t, "rule", own.Str("kind"))
	require.Equal(t, "No admin tools", own.Str("name"))
	require.Equal(t, "block", own.Str("action"))
	require.Zero(t, ruleCount(blocks, "allowhome"), "an allowed request is not a block")

	// The request lines name the rule and keep the path the client asked for.
	var lines []agentprotocol.RequestRecord
	ok = eventually(60*time.Second, 2*time.Second, func() bool {
		lines = s.requestLines(svc.ID, "@blocked_by:*")
		return countLines(lines, "stdsecrets", "/.env") >= envBlocks && countLines(lines, "ownblock", "/blocked/x") >= ownBlocks
	})
	require.True(t, ok, "request lines are missing blocked requests: %+v", lines)
	assertBlockedLines(t, s, svc.ID, lines)

	// -- guard: a country condition moves the whole firewall to the edge guard
	// (the harness location database knows no private address, so a request
	// from here is "not in GB").
	geoRule := map[string]any{
		"id": "geoblock", "name": "GB only", "action": "block",
		"conditions": []any{
			pathCond("prefix", "/geo"),
			map[string]any{"field": "country", "op": "in", "values": []string{"GB"}, "not": true},
		},
	}
	require.True(t, eventually(120*time.Second, time.Second, func() bool {
		st := s.Do("GET", "/servers/"+s.ServerID, nil).JSON().Obj("geoStatus")
		return st.Bool("downloaded")
	}), "the agent never installed the GeoIP database")
	r = s.Do("PUT", fwPath, map[string]any{"rules": []any{ownBlock, geoRule, allowHome}})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "guard", r.JSON().Str("enforcement"))

	waitStatus(t, px, "/geo/x", http.StatusForbidden)
	geoBlocks := 1
	for range 2 {
		code, _, err := px.Get("/geo/x")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, code, "the guard blocks a request from outside GB")
		geoBlocks++
	}
	// The whole firewall runs in the guard now: its standard and own rules
	// are attributed through the same header.
	for range 2 {
		code, _, err := px.Get("/.env")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, code)
		envBlocks++
		code, _, err = px.Get("/blocked/x")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, code)
		ownBlocks++
	}
	code, body, err = px.Get("/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, "the allow rule still lets / through")
	require.Equal(t, "v1", body)

	// The rule rides the guard's 403 into the proxy's access log ...
	logs, err := sh(context.Background(), "", "docker", "logs", "--tail", "400", agentprotocol.ProxyContainer)
	require.NoError(t, err)
	var sawHeader bool
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"RequestPath":"/geo/x"`) && strings.Contains(l, `"downstream_X-Nebula-Firewall-Rule":"geoblock"`) {
			sawHeader = true
		}
	}
	require.True(t, sawHeader, "no access-log line of /geo/x carries X-Nebula-Firewall-Rule: geoblock:\n%.3000s", logs)

	// ... and from there into the counts and the request lines.
	ok = eventually(150*time.Second, 3*time.Second, func() bool {
		blocks = s.firewallBlocks(svc.ID)
		return ruleCount(blocks, "geoblock") >= geoBlocks && ruleCount(blocks, "stdsecrets") >= envBlocks && ruleCount(blocks, "ownblock") >= ownBlocks
	})
	require.True(t, ok, "guard blocks never counted (geo x%d, /.env x%d, /blocked x%d): %v", geoBlocks, envBlocks, ownBlocks, blocks)
	geo := ruleEntry(t, blocks, "geoblock")
	require.Equal(t, "rule", geo.Str("kind"))
	require.Equal(t, "GB only", geo.Str("name"))
	require.Equal(t, "standard", ruleEntry(t, blocks, "stdsecrets").Str("kind"))

	ok = eventually(60*time.Second, 2*time.Second, func() bool {
		lines = s.requestLines(svc.ID, "@blocked_by:*")
		return countLines(lines, "geoblock", "/geo/x") >= geoBlocks && countLines(lines, "stdsecrets", "/.env") >= envBlocks && countLines(lines, "ownblock", "/blocked/x") >= ownBlocks
	})
	require.True(t, ok, "request lines are missing guard-blocked requests: %+v", lines)
	assertBlockedLines(t, s, svc.ID, lines)

	// -- privacy: no whole client address in the aggregates or the API -------
	dump := s.dumpTables([]string{"traffic_1m", "traffic_1h", "traffic_1d"})
	require.Contains(t, dump, s.Host(), "traffic rows should be keyed by host")
	require.Empty(t, ipv4RE.FindAllString(dump, -1), "an IPv4 address was persisted in the traffic tables")
	require.NotContains(t, dump, "::1")
	require.Empty(t, ipv4RE.FindAllString(string(jsonBytes(blocks)), -1), "IPv4 in the blocks view")
}

func countLines(lines []agentprotocol.RequestRecord, rule, path string) int {
	n := 0
	for _, l := range lines {
		if l.BlockedBy == rule && l.Path == path {
			n++
		}
	}
	return n
}

// assertBlockedLines checks what every blocked request line must hold: a 403,
// the original path and no whole client address, also for all request lines of
// the service.
func assertBlockedLines(t *testing.T, s *Stack, serviceID string, blocked []agentprotocol.RequestRecord) {
	t.Helper()
	for _, l := range blocked {
		require.NotEmpty(t, l.BlockedBy)
		require.Equal(t, http.StatusForbidden, l.Status, "%+v", l)
		require.False(t, strings.HasPrefix(l.Path, "/err/"), "a blocked request keeps the path it asked for, not the error page's: %+v", l)
		require.Contains(t, []string{"/.env", "/blocked/x", "/geo/x"}, l.Path, "%+v", l)
	}
	for _, l := range s.requestLines(serviceID, "") {
		require.False(t, strings.HasPrefix(l.Path, "/err/"), "%+v", l)
		require.True(t, l.IP == "" || cidrRE.MatchString(l.IP), "a request line holds the client's network, not its address: %+v", l)
	}
}
