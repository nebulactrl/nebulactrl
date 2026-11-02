package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	dmID1 = "d1d1d1d1-1111-4111-8111-d1d1d1d1d1d1"
	dmID2 = "d2d2d2d2-1111-4111-8111-d2d2d2d2d2d2"
	dmID3 = "d3d3d3d3-1111-4111-8111-d3d3d3d3d3d3"

	dmSvc     = cfgAPI + "/services/" + fakeSvcID
	dmDomains = dmSvc + "/domains"
)

func dmView(id, host string, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "hostname": host, "targetPort": 80, "status": "active", "dns": map[string]any{}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestDomainsLsShowsDNSCloudflareAndRedirectState(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dmDomains, 200, map[string]any{"items": []any{
		dmView(dmID1, "example.com", map[string]any{"dnsOk": true, "dns": map[string]any{"status": "managed", "proxied": true}, "wwwRedirect": true, "wwwPartner": "www.example.com"}),
		dmView(dmID2, "www.example.com", map[string]any{"dnsOk": false, "wwwRedirectFor": "example.com", "status": "redirect", "targetPort": 0,
			"dns": map[string]any{"status": "not_in_zone", "proxied": true}}),
		dmView(dmID3, "app.example.com", map[string]any{"generated": true, "dns": map[string]any{"status": "wrong_target"}}),
	}})

	r := f.run("domains", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"HOSTNAME", "PORT", "STATUS", "DNS", "CLOUDFLARE", "WWW REDIRECT", "GENERATED", "ID"} {
		require.Contains(t, r.stdout, h)
	}
	require.NotContains(t, r.stdout, "PROCESS", "the process column appears only when a domain has one")
	require.Equal(t, []string{"example.com", "80", "active", "ok", "managed", "(proxied)", "www.example.com", "→", "here", "no", dmID1},
		bkLine(t, r.stdout, dmID1))
	require.Equal(t, []string{"www.example.com", "0", "redirect", "mismatch", "not_in_zone", "→", "example.com", "no", dmID2},
		bkLine(t, r.stdout, dmID2), "a proxied record that is not in the zone shows no (proxied) note")
	require.Equal(t, []string{"app.example.com", "80", "active", "unknown", "wrong_target", "-", "yes", dmID3},
		bkLine(t, r.stdout, dmID3))
}

func TestDomainsLsAddsTheProcessColumnWhenAnyDomainHasOne(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dmDomains, 200, map[string]any{"items": []any{
		dmView(dmID1, "api.example.com", map[string]any{"process": "api"}),
		dmView(dmID2, "web.example.com", nil),
	}})
	r := f.run("domains", "ls", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "PROCESS")
	require.Equal(t, "api", bkLine(t, r.stdout, dmID1)[2])
	require.Equal(t, "-", bkLine(t, r.stdout, dmID2)[2], "a domain without a process shows a dash")

	f.reply("GET", dmDomains, 200, map[string]any{})
	r = f.run("--json", "domains", "ls", fakeSvcID)
	require.Equal(t, []any{}, decode[[]any](t, r.stdout))
}

func TestDomainsAddSendsTheHostnameLowercasedWithItsOptions(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", dmDomains, 201, dmView(dmID1, "shop.example.com", map[string]any{"wwwPartner": "www.shop.example.com"}))

	r := f.run("domains", "add", fakeSvcID, "Shop.Example.COM")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Added shop.example.com (staged until the next deploy)\n", r.stdout)
	require.Equal(t, map[string]any{"hostname": "shop.example.com"}, f.only("POST", dmDomains).json(t))

	r = f.run("domains", "add", fakeSvcID, "shop.example.com", "--port", "8080", "--component", "web", "--process", "api", "--www-redirect")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Added shop.example.com (staged until the next deploy)\n"+
		"Also routed: www.shop.example.com redirects (301) to shop.example.com. Point an A record for both hostnames at the server.\n", r.stdout)
	require.Equal(t, map[string]any{
		"hostname": "shop.example.com", "targetPort": float64(8080), "component": "web", "process": "api", "wwwRedirect": true,
	}, f.calls("POST", dmDomains)[1].json(t))

	r = f.run("--json", "domains", "add", fakeSvcID, "shop.example.com")
	require.Equal(t, "shop.example.com", decode[map[string]any](t, r.stdout)["hostname"])

	f.problem("POST", dmDomains, 409, "domain_taken", "shop.example.com is used by another service")
	r = f.run("domains", "add", fakeSvcID, "shop.example.com")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "shop.example.com is used by another service (domain_taken, HTTP 409)")

	r = f.run("domains", "add", fakeSvcID)
	require.Equal(t, 2, r.code)
}

func TestDomainsRmFindsTheDomainByHostnameOrID(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dmDomains, 200, map[string]any{"items": []any{
		dmView(dmID1, "example.com", nil), dmView(dmID2, "www.example.com", nil),
	}})
	f.reply("DELETE", cfgAPI+"/domains/"+dmID1, 204, "")
	f.reply("DELETE", cfgAPI+"/domains/"+dmID2, 204, "")

	r := f.run("domains", "rm", fakeSvcID, "EXAMPLE.com")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Removed example.com (staged until the next deploy)\n", r.stdout)
	require.Len(t, f.calls("DELETE", cfgAPI+"/domains/"+dmID1), 1)
	require.Empty(t, f.calls("DELETE", cfgAPI+"/domains/"+dmID2))

	r = f.run("--json", "domains", "rm", fakeSvcID, dmID2)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, map[string]any{"deleted": dmID2, "hostname": "www.example.com"}, decode[map[string]any](t, r.stdout))

	r = f.run("domains", "rm", fakeSvcID, "nope.example.com")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `domain "nope.example.com" not found on this service`)
	require.Len(t, f.calls("DELETE", cfgAPI+"/domains/"+dmID1), 1, "an unknown domain deletes nothing")

	f.problem("DELETE", cfgAPI+"/domains/"+dmID1, 403, "forbidden", "needs proxy:write")
	r = f.run("domains", "rm", fakeSvcID, "example.com")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs proxy:write (forbidden, HTTP 403)")
}

func TestDomainsGenerateSendsTheOptionsAsQuery(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", dmDomains+"/generate", 201, dmView(dmID1, "web-1a2b.apps.example.com", map[string]any{"generated": true}))

	r := f.run("domains", "generate", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Generated web-1a2b.apps.example.com (staged until the next deploy)\n", r.stdout)
	require.Empty(t, f.only("POST", dmDomains+"/generate").query)

	r = f.run("domains", "generate", fakeSvcID, "--component", "web", "--process", "api")
	require.Equal(t, 0, r.code, r.all())
	q := f.calls("POST", dmDomains+"/generate")[1].values(t)
	require.Equal(t, "web", q.Get("component"))
	require.Equal(t, "api", q.Get("process"))

	r = f.run("--json", "domains", "generate", fakeSvcID)
	require.Equal(t, "web-1a2b.apps.example.com", decode[map[string]any](t, r.stdout)["hostname"])

	f.problem("POST", dmDomains+"/generate", 409, "no_base_domain", "the server has no base domain")
	r = f.run("domains", "generate", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the server has no base domain (no_base_domain, HTTP 409)")
}

func TestDomainsWWWTogglesTheRedirectWithTheServicesGeneration(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dmDomains, 200, map[string]any{"items": []any{dmView(dmID1, "example.com", nil)}})
	f.reply("GET", dmSvc, 200, map[string]any{"id": fakeSvcID, "generation": 7})
	f.handle("PATCH", cfgAPI+"/domains/"+dmID1, func(req cfgReq) cfgReply {
		on := strings.Contains(string(req.body), "true")
		return jsonReply(200, dmView(dmID1, "example.com", map[string]any{"wwwRedirect": on, "wwwPartner": "www.example.com"}))
	})

	r := f.run("domains", "www", fakeSvcID, "Example.com", "ON")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "www.example.com redirects (301) to example.com (staged until the next deploy). Point an A record for both hostnames at the server.\n", r.stdout)
	patch := f.only("PATCH", cfgAPI+"/domains/"+dmID1)
	require.Equal(t, map[string]any{"wwwRedirect": true}, patch.json(t))
	require.Equal(t, `"7"`, patch.header.Get("If-Match"), "the edit is guarded by the service's generation")
	require.Equal(t, "application/json", patch.header.Get("Content-Type"))

	r = f.run("domains", "www", fakeSvcID, dmID1, "off")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "No www redirect for example.com (staged until the next deploy)\n", r.stdout)
	require.Equal(t, map[string]any{"wwwRedirect": false}, f.calls("PATCH", cfgAPI+"/domains/"+dmID1)[1].json(t))

	r = f.run("--json", "domains", "www", fakeSvcID, "example.com", "on")
	require.Equal(t, true, decode[map[string]any](t, r.stdout)["wwwRedirect"])
}

func TestDomainsWWWRejectsBadInputBeforeChangingAnything(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", dmDomains, 200, map[string]any{"items": []any{dmView(dmID1, "example.com", nil)}})
	f.reply("GET", dmSvc, 200, map[string]any{"id": fakeSvcID, "generation": 7})

	before := f.total()
	r := f.run("domains", "www", fakeSvcID, "example.com", "maybe")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "the last argument must be on or off")
	require.Equal(t, before, f.total())

	r = f.run("domains", "www", fakeSvcID, "other.example.com", "on")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `domain "other.example.com" not found on this service`)
	require.Empty(t, f.calls("PATCH", cfgAPI+"/domains/"+dmID1))

	f.problem("PATCH", cfgAPI+"/domains/"+dmID1, 412, "precondition_failed", "the service changed; retry")
	r = f.run("domains", "www", fakeSvcID, "example.com", "on")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the service changed; retry (precondition_failed, HTTP 412)")

	f.problem("GET", dmSvc, 404, "not_found", "service gone")
	r = f.run("domains", "www", fakeSvcID, "example.com", "on")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "service gone (not_found, HTTP 404)")
}
