package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	msSvcLogDL    = cfgAPI + "/services/" + fakeSvcID + "/logs/download"
	msBuildPlan   = cfgAPI + "/services/" + fakeSvcID + "/build-plan"
	msDashAccess  = cfgAPI + "/instance/dashboard-access"
	msLoginMePath = cfgAPI + "/me"
)

func TestServicesDownloadLogsStreamsToStdoutWithTheQuery(t *testing.T) {
	f := newCfgFake(t)
	f.replyRaw("GET", msSvcLogDL, cfgReply{status: 200, ctype: "text/plain", body: "2026-01-02T03:04:05Z listening\n2026-01-02T03:04:06Z GET / 200\n"})

	r := f.run("services", "download-logs", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "2026-01-02T03:04:05Z listening\n2026-01-02T03:04:06Z GET / 200\n", r.stdout)
	require.Empty(t, r.err, "stdout downloads print no summary")
	require.Equal(t, map[string][]string{"format": {"txt"}}, map[string][]string(f.only("GET", msSvcLogDL).values(t)))
	require.Equal(t, "Bearer nct_test", f.only("GET", msSvcLogDL).header.Get("Authorization"))

	r = f.run("services", "download-logs", fakeSvcID, "--format", "ndjson", "--from", "2026-01-01", "--to", "2026-01-02T12:00:00Z",
		"--grep", "level:error", "--component", "api", "--process", "worker")
	require.Equal(t, 0, r.code, r.all())
	q := f.calls("GET", msSvcLogDL)[1].values(t)
	require.Equal(t, "ndjson", q.Get("format"))
	require.Equal(t, "2026-01-01T00:00:00Z", q.Get("from"))
	require.Equal(t, "2026-01-02T12:00:00Z", q.Get("to"))
	require.Equal(t, "level:error", q.Get("q"))
	require.Equal(t, "api", q.Get("component"))
	require.Equal(t, "worker", q.Get("process"))
}

func TestServicesDownloadLogsWritesAPrivateFileAndReportsItsSize(t *testing.T) {
	f := newCfgFake(t)
	f.replyRaw("GET", msSvcLogDL, cfgReply{status: 200, ctype: "text/plain", body: "line one\nline two\n"})
	out := filepath.Join(t.TempDir(), "web.log")

	r := f.run("services", "download-logs", fakeSvcID, "-o", out)
	require.Equal(t, 0, r.code, r.all())
	require.Empty(t, r.stdout, "the file got the bytes")
	require.Equal(t, "Saved 18 bytes to "+out+"\n", r.err)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "line one\nline two\n", string(raw))
	info, err := os.Stat(out)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// "-" is stdout, not a file called "-".
	r = f.run("services", "download-logs", fakeSvcID, "-o", "-")
	require.Equal(t, "line one\nline two\n", r.stdout)
	require.Empty(t, r.err)

	// A refusal leaves no file behind.
	f.problem("GET", msSvcLogDL, 409, "server_offline", "the service's server is offline")
	missing := filepath.Join(t.TempDir(), "none.log")
	r = f.run("services", "download-logs", fakeSvcID, "-o", missing)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the service's server is offline (server_offline, HTTP 409)")
	require.NoFileExists(t, missing)

	f.replyRaw("GET", msSvcLogDL, cfgReply{status: 200, ctype: "text/plain", body: "x"})
	r = f.run("services", "download-logs", fakeSvcID, "-o", filepath.Join(t.TempDir(), "no-such-dir", "x.log"))
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "open ")
}

func TestServicesDownloadLogsValidatesFlagsBeforeAnyRequest(t *testing.T) {
	f := newCfgFake(t)
	before := f.total()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"format":         {[]string{"--format", "csv"}, "--format must be txt or ndjson"},
		"from":           {[]string{"--from", "yesterday"}, `--from "yesterday": use an RFC 3339 time`},
		"to":             {[]string{"--to", "-5m"}, `--to "-5m": use an RFC 3339 time`},
		"order":          {[]string{"--from", "2026-01-02", "--to", "2026-01-01"}, "--from must be before --to"},
		"equal boundary": {[]string{"--from", "2026-01-02", "--to", "2026-01-02"}, "--from must be before --to"},
	} {
		r := f.run(append([]string{"services", "download-logs", fakeSvcID}, tc.args...)...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
		require.Contains(t, r.err, tc.want, name)
	}
	require.Equal(t, before, f.total())

	r := f.run("services", "download-logs", "not-a-ref")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use a service ID or project/env/service-slug")
}

func TestServicesBuildPlanDescribesWhatWouldBeBuilt(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", msBuildPlan, 200, map[string]any{
		"outcome": "succeeded",
		"info":    map[string]any{"method": "railpack", "reason": "no Dockerfile found", "railpackVersion": "0.9.1", "providers": []string{"node", "python"}, "startCommand": "npm start"},
	})

	r := f.run("services", "build-plan", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Method:   railpack\nWhy:      no Dockerfile found\nRailpack: 0.9.1\nProvider: node, python\nStart:    npm start\n", r.stdout)

	f.reply("POST", msBuildPlan, 200, map[string]any{"outcome": "failed", "message": "clone failed: auth", "info": map[string]any{"method": "dockerfile"}})
	r = f.run("services", "build-plan", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Method:   dockerfile\nWhy:      \nProblem:  clone failed: auth\n", r.stdout, "a plan that did not succeed shows the problem after what is known")

	f.reply("POST", msBuildPlan, 200, map[string]any{"outcome": "failed", "message": "branch not found"})
	r = f.run("services", "build-plan", fakeSvcID)
	require.Equal(t, "No plan: branch not found\n", r.stdout)

	r = f.run("--json", "services", "build-plan", fakeSvcID)
	require.Equal(t, "failed", decode[map[string]any](t, r.stdout)["outcome"])

	f.problem("POST", msBuildPlan, 422, "not_git", "only git services have a build plan")
	r = f.run("services", "build-plan", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "only git services have a build plan (not_git, HTTP 422)")
}

func TestDashboardAccessGetDescribesTheRestrictionAndCertificate(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", msDashAccess, 200, map[string]any{
		"enabled": true, "host": "nebula.example.com", "allowCidrs": []string{"203.0.113.0/24", "192.0.2.7"},
		"dnsResolver": "cloudflare", "certificate": map[string]any{"message": "issued, renews in 60 days"},
		"resolverAvailable": []any{map[string]any{"provider": "cloudflare", "applied": true}, map[string]any{"provider": "route53", "applied": false}},
		"viaDashboardRoute": true, "clientIp": "198.51.100.4", "clientIpAllowed": false, "source": "admin", "proxyDeployRequired": true,
	})

	r := f.run("instance", "dashboard-access", "get")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Host:         nebula.example.com\n"+
		"Allow list:   203.0.113.0/24, 192.0.2.7\n"+
		"Certificate:  DNS-01 cloudflare (issued, renews in 60 days)\n"+
		"Resolvers:    cloudflare, route53 (staged: needs Deploy proxy)\n"+
		"Your address: 198.51.100.4 (allowed: false)\n"+
		"Source:       admin\n"+
		"Deploy the proxy of the control-plane server to activate the DNS-01 resolver (Servers, Proxy).\n", r.stdout)

	f.reply("GET", msDashAccess, 200, map[string]any{"enabled": true, "host": "nebula.example.com", "certificate": map[string]any{"message": "issued"}, "source": "default"})
	r = f.run("instance", "dashboard-access", "get")
	require.Equal(t, "Host:         nebula.example.com\nAllow list:   everybody\nCertificate:  automatic HTTP-01 (issued)\nSource:       default\n", r.stdout)

	f.reply("GET", msDashAccess, 200, map[string]any{"enabled": false})
	r = f.run("instance", "dashboard-access", "get")
	require.Equal(t, "The dashboard has no system route (no https public URL with a DNS name), so there is nothing to restrict.\n", r.stdout)

	r = f.run("--json", "instance", "dashboard-access", "get")
	require.Equal(t, false, decode[map[string]any](t, r.stdout)["enabled"])

	f.problem("GET", msDashAccess, 403, "forbidden", "instance admins only")
	r = f.run("instance", "dashboard-access", "get")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "instance admins only (forbidden, HTTP 403)")
}

func TestDashboardAccessSetSendsOnlyWhatWasAskedForAndKeepsTheRest(t *testing.T) {
	f := newCfgFake(t)
	current := map[string]any{"enabled": true, "host": "nebula.example.com", "allowCidrs": []string{"203.0.113.0/24"}, "dnsResolver": "cloudflare",
		"certificate": map[string]any{"message": "ok"}, "source": "admin"}
	f.reply("GET", msDashAccess, 200, current)
	f.reply("PUT", msDashAccess, 200, current)

	r := f.run("instance", "dashboard-access", "set", "--allow", "192.0.2.0/24, 198.51.100.1", "--allow", "2001:db8::/32", "--confirm-lockout")
	require.Equal(t, 0, r.code, r.all())
	require.True(t, strings.HasPrefix(r.stdout, "Saved.\nHost:"), r.stdout)
	require.Equal(t, map[string]any{
		"allowCidrs": []any{"192.0.2.0/24", "198.51.100.1", "2001:db8::/32"}, "dnsResolver": "cloudflare", "confirmLockout": true,
	}, f.only("PUT", msDashAccess).json(t), "the certificate method stays; blanks around CIDRs are trimmed")

	r = f.run("instance", "dashboard-access", "set", "--tailnet-only")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"allowCidrs": []any{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, "dnsResolver": "cloudflare"}, f.calls("PUT", msDashAccess)[1].json(t))

	r = f.run("instance", "dashboard-access", "set", "--clear-allow", "--http-01")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"allowCidrs": []any{}, "dnsResolver": ""}, f.calls("PUT", msDashAccess)[2].json(t), "--http-01 clears the resolver")

	r = f.run("--json", "instance", "dashboard-access", "set", "--dns-resolver", "route53")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"allowCidrs": []any{"203.0.113.0/24"}, "dnsResolver": "route53"}, f.calls("PUT", msDashAccess)[3].json(t), "the current list stays")
	require.Equal(t, true, decode[map[string]any](t, r.stdout)["enabled"])

	f.problem("PUT", msDashAccess, 409, "dashboard_lockout", "the list excludes your address 198.51.100.4")
	r = f.run("instance", "dashboard-access", "set", "--allow", "192.0.2.1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the list excludes your address 198.51.100.4 (dashboard_lockout, HTTP 409)")
}

func TestDashboardAccessSetRejectsContradictoryFlagsLocally(t *testing.T) {
	f := newCfgFake(t)
	before := f.total()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"nothing":            {nil, "nothing to change"},
		"allow + tailnet":    {[]string{"--allow", "192.0.2.1", "--tailnet-only"}, "use one of --allow, --tailnet-only and --clear-allow"},
		"allow + clear":      {[]string{"--allow", "192.0.2.1", "--clear-allow"}, "use one of --allow, --tailnet-only and --clear-allow"},
		"tailnet + clear":    {[]string{"--tailnet-only", "--clear-allow"}, "use one of --allow, --tailnet-only and --clear-allow"},
		"resolver + http-01": {[]string{"--dns-resolver", "cloudflare", "--http-01"}, "use one of --dns-resolver and --http-01"},
	} {
		r := f.run(append([]string{"instance", "dashboard-access", "set"}, tc.args...)...)
		require.Equal(t, 2, r.code, "%s: %s", name, r.all())
		require.Contains(t, r.err, tc.want, name)
	}
	require.Equal(t, before, f.total())
}

func TestDocsCommandWritesTheReferenceToStdoutOrAFile(t *testing.T) {
	f := newCfgFake(t)
	r := f.run("docs")
	require.Equal(t, 0, r.code, r.all())
	require.True(t, strings.HasPrefix(r.stdout, "---\ntitle: CLI reference\n"), r.stdout[:min(60, len(r.stdout))])
	require.Contains(t, r.stdout, "### nebulactrl services deploy")

	out := filepath.Join(t.TempDir(), "cli.md")
	r = f.run("docs", "--out", out)
	require.Equal(t, 0, r.code, r.all())
	require.Empty(t, r.stdout)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, f.run("docs").stdout, string(raw), "the file holds exactly what stdout prints")

	r = f.run("docs", "--out", filepath.Join(t.TempDir(), "missing", "cli.md"))
	require.Equal(t, 1, r.code)
	require.Equal(t, 0, f.total(), "docs never talks to the control plane")
}

func TestLoginStoresTheTokenUnderItsTeamAndNeverEchoesIt(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", msLoginMePath, 200, map[string]any{"id": "u-1", "email": "alex@example.com", "name": "Alex", "workspaceId": tmTeamID})
	f.reply("GET", tmTeams, 200, map[string]any{"items": []any{
		map[string]any{"id": tmTeamID, "name": "Acme", "slug": "acme", "current": true},
		map[string]any{"id": tmTeam2ID, "name": "Side", "slug": "side", "current": false},
	}})
	// An earlier login for the same control plane left a token for another team.
	path := os.Getenv(envConfig)
	cfgWrite(t, path, `{"url":"`+f.srv.URL+`","token":"nct_old","team":"side","profiles":{"side":"nct_side"}}`)

	r := f.runStdin("nct_new\n", "login", "--url", f.srv.URL+"/", "--token", "-")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Logged in to "+f.srv.URL+" as alex@example.com\nSaved "+path+"\n", r.stdout)
	require.NotContains(t, r.all(), "nct_new")
	require.Equal(t, "Bearer nct_new", f.only("GET", msLoginMePath).header.Get("Authorization"))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved config
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, config{URL: f.srv.URL, Token: "nct_new", Team: "acme", Profiles: map[string]string{"side": "nct_side", "acme": "nct_new"}}, saved)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	r = f.run("--json", "login", "--url", f.srv.URL, "--token", "nct_new")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"url": f.srv.URL, "email": "alex@example.com", "name": "Alex", "config": path}, decode[map[string]any](t, r.stdout))
}

func TestLoginTakesTheURLAndTokenFromTheEnvironment(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", msLoginMePath, 200, map[string]any{"email": "alex@example.com"})
	f.problem("GET", tmTeams, 500, "internal", "teams unavailable")
	t.Setenv(envURL, f.srv.URL)
	t.Setenv(envToken, " nct_env \n")

	r := f.run("login")
	require.Equal(t, 0, r.code, "a failing team lookup does not fail the login: %s", r.all())
	require.Equal(t, "Bearer nct_env", f.only("GET", msLoginMePath).header.Get("Authorization"), "the env token is trimmed")
	saved, err := os.ReadFile(os.Getenv(envConfig))
	require.NoError(t, err)
	require.Contains(t, string(saved), `"token": "nct_env"`)
	require.NotContains(t, string(saved), "profiles", "no team, no profile")
}

func TestLoginRefusesWhatCannotWork(t *testing.T) {
	f := newCfgFake(t)
	t.Setenv(envURL, "")
	t.Setenv(envToken, "")
	before := f.total()

	r := f.run("login", "--token", "nct_x")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--url is required")
	r = f.run("login", "--url", f.srv.URL)
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--token is required")
	r = f.runStdin("  \n", "login", "--url", f.srv.URL, "--token", "-")
	require.Equal(t, 2, r.code, "an empty stdin is no token")
	r = f.run("login", "--url", "http://ctrl.example.com", "--token", "nct_x")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "refusing to send the API token over plain http to ctrl.example.com")
	r = f.run("login", "--url", "ftp://ctrl.example.com", "--token", "nct_x")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the scheme must be https")
	require.Equal(t, before, f.total(), "nothing was sent")
	require.NoFileExists(t, os.Getenv(envConfig), "and nothing saved")

	f.problem("GET", msLoginMePath, 401, "unauthorized", "bad token")
	r = f.run("login", "--url", f.srv.URL, "--token", "nct_wrong")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the control plane rejected the token (401)")

	f.problem("GET", msLoginMePath, 403, "token_kind_mismatch", "mcp token")
	r = f.run("login", "--url", f.srv.URL, "--token", "nct_mcp")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "this is an MCP token, which works only on /mcp")

	f.problem("GET", msLoginMePath, 500, "internal", "database down")
	r = f.run("login", "--url", f.srv.URL, "--token", "nct_x")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "database down (internal, HTTP 500)")
	require.NoFileExists(t, os.Getenv(envConfig), "a failed login saves nothing")
}
