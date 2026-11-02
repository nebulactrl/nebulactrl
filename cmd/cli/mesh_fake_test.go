package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	msMesh    = cfgAPI + "/mesh"
	msServer  = cfgAPI + "/servers/" + dbSrvID + "/mesh"
	msEnv     = cfgAPI + "/environments/" + cfgProdID + "/mesh"
	msService = cfgAPI + "/services/" + fakeSvcID + "/mesh"
)

func TestMeshStatusShowsProviderACLAndNodes(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", msMesh, 200, map[string]any{
		"provider": "tailscale", "aclMode": "managed", "aclState": "in_sync", "paused": false, "aclMessage": "ACL is up to date",
		"nodes": []any{
			map[string]any{"serverName": "edge-1", "state": "online", "ips": []string{"100.64.0.1", "fd7a::1"}, "fqdn": "edge-1.tail.ts.net"},
			map[string]any{"serverName": "edge-2", "state": "pending", "ips": []string{}},
		},
	})

	r := f.run("mesh", "status")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Provider: tailscale   ACL: managed (in_sync)   Paused: false\nACL is up to date\n")
	require.Equal(t, []string{"edge-1", "online", "100.64.0.1", "edge-1.tail.ts.net"}, bkLine(t, r.stdout, "edge-1"), "only the first IP is shown")
	require.Equal(t, []string{"edge-2", "pending", "-", "-"}, bkLine(t, r.stdout, "edge-2"), "a node without IP or FQDN shows dashes")
	require.Equal(t, "Bearer nct_test", f.only("GET", msMesh).header.Get("Authorization"))
	require.Empty(t, f.only("GET", msMesh).header.Get("Idempotency-Key"), "reads carry no idempotency key")

	r = f.run("--json", "mesh", "status")
	require.Equal(t, "tailscale", decode[map[string]any](t, r.stdout)["provider"])

	f.reply("GET", msMesh, 200, map[string]any{"provider": "none", "aclMode": "off", "aclState": "-", "paused": true})
	r = f.run("mesh", "status")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Paused: true")
	require.NotContains(t, r.stdout, "ACL is", "no message line without a message")
}

func TestMeshStatusSurfacesErrors(t *testing.T) {
	f := newCfgFake(t)
	f.problem("GET", msMesh, 403, "forbidden", "needs mesh:read")
	r := f.run("mesh", "status")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs mesh:read (forbidden, HTTP 403)")

	f.replyRaw("GET", msMesh, cfgReply{status: 200, ctype: "application/json", body: "not json"})
	r = f.run("mesh", "status")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "unreadable answer")

	f.replyRaw("GET", msMesh, cfgReply{status: 200, ctype: "application/json", body: ""})
	r = f.run("mesh", "status")
	require.Equal(t, 0, r.code, "an empty answer is an empty mesh: %s", r.all())

	r = f.run("mesh", "status", "extra")
	require.Equal(t, 2, r.code)
}

func TestMeshEnableSendsTheConfirmationAndReportsThePlan(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("PUT", msServer, 200, map[string]any{"plan": map[string]any{"devicesAdded": 2, "warnings": []string{"billing applies", "ACL will change"}}})

	r := f.run("--idempotency-key", "mesh-key", "mesh", "enable", "EDGE-1", "--accept-mesh-changes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Devices added: 2\nwarning: billing applies\nwarning: ACL will change\nThe mesh node is being provisioned.\n", r.stdout)
	put := f.only("PUT", msServer)
	require.Equal(t, map[string]any{"enabled": true, "acceptMeshChanges": true}, put.json(t))
	require.Empty(t, put.query)
	require.Equal(t, "mesh-key", put.header.Get("Idempotency-Key"))
	require.Equal(t, "application/json", put.header.Get("Content-Type"))

	r = f.run("mesh", "enable", dbSrvID, "--dry-run")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Devices added: 2\nwarning: billing applies\nwarning: ACL will change\n", r.stdout, "a dry run does not claim anything is provisioned")
	second := f.calls("PUT", msServer)[1]
	require.Equal(t, "true", second.values(t).Get("dryRun"))
	require.Equal(t, map[string]any{"enabled": true, "acceptMeshChanges": false}, second.json(t))

	r = f.run("--json", "mesh", "enable", "edge-1")
	require.Equal(t, float64(2), decode[map[string]any](t, r.stdout)["plan"].(map[string]any)["devicesAdded"])
}

func TestMeshEnableRefusalsAndUnknownServers(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.problem("PUT", msServer, 409, "mesh_changes_not_accepted", "this adds 2 tailnet devices; pass acceptMeshChanges")
	r := f.run("mesh", "enable", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "this adds 2 tailnet devices; pass acceptMeshChanges (mesh_changes_not_accepted, HTTP 409)")
	require.NotContains(t, r.stdout, "provisioned")

	r = f.run("mesh", "enable", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)
	require.Len(t, f.calls("PUT", msServer), 1, "an unknown server is never sent")

	before := f.total()
	r = f.run("mesh", "enable")
	require.Equal(t, 2, r.code, "a wrong argument count is a usage error, like every other command")
	require.Contains(t, r.err, "accepts 1 arg(s), received 0")
	require.Contains(t, r.err, "usage: nebulactrl mesh enable <server>")
	r = f.run("mesh", "links")
	require.Equal(t, 2, r.code, "mesh_links.go uses the same rule")
	require.Equal(t, before, f.total())
}

func TestMeshDisableCheckAndReprovisionTargetTheResolvedServer(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("PUT", msServer, 200, map[string]any{"enabled": false})
	f.reply("POST", msServer+"/check", 200, map[string]any{"results": []any{
		map[string]any{"peerServerId": "peer-1", "outcome": "reachable", "message": "12ms"},
		map[string]any{"outcome": "unreachable"},
	}})
	f.reply("POST", msServer+"/reprovision", 202, map[string]any{"status": "provisioning"})

	r := f.run("mesh", "disable", "edge-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "The mesh node is being removed.\n", r.stdout)
	require.Equal(t, map[string]any{"enabled": false}, f.only("PUT", msServer).json(t))

	r = f.run("mesh", "check", "edge-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"peer-1", "reachable", "12ms"}, bkLine(t, r.stdout, "peer-1"))
	require.Equal(t, []string{"-", "unreachable", "-"}, bkLine(t, r.stdout, "unreachable"), "missing peer and message show dashes")
	require.Equal(t, "{}", string(f.only("POST", msServer+"/check").body))

	r = f.run("mesh", "reprovision", "edge-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "The mesh node is being re-provisioned.\n", r.stdout)
	require.Empty(t, f.only("POST", msServer+"/reprovision").body, "reprovision sends no body")
	require.NotEmpty(t, f.only("POST", msServer+"/reprovision").header.Get("Idempotency-Key"))

	for _, sub := range []string{"disable", "check", "reprovision"} {
		r = f.run("--json", "mesh", sub, "edge-1")
		require.Equal(t, 0, r.code, "%s: %s", sub, r.all())
		require.NotNil(t, decode[map[string]any](t, r.stdout), sub)
	}

	for _, sub := range []string{"disable", "check", "reprovision"} {
		r = f.run("mesh", sub, "ghost")
		require.Equal(t, 1, r.code, sub)
		require.Contains(t, r.err, `server "ghost" not found`, sub)
	}

	f.problem("POST", msServer+"/check", 502, "mesh_unavailable", "the tailnet API is unreachable")
	r = f.run("mesh", "check", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the tailnet API is unreachable (mesh_unavailable, HTTP 502)")
}

func TestMeshLinksListsLinksAndRoutes(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", msEnv, 200, map[string]any{
		"links": []any{
			map[string]any{"from": map[string]any{"name": "edge-1"}, "to": map[string]any{"name": "edge-2"}, "purpose": "database", "state": "active",
				"grants": []any{map[string]any{"id": "g1"}, map[string]any{"id": "g2"}}},
			"not an object",
		},
		"routes": []any{
			map[string]any{"consumerSlug": "web", "provider": "pg", "server": map[string]any{"name": "edge-2"}, "port": 5432, "state": "ready", "source": "variable"},
			map[string]any{"consumerSlug": "worker", "provider": "redis", "state": "pending", "source": "canvas"},
		},
	})

	r := f.run("mesh", "links", "acme-shop/production")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, []string{"edge-1", "->", "edge-2", "database", "active", "2"}, bkLine(t, r.stdout, "edge-1"))
	require.Equal(t, []string{"web", "pg", "edge-2", "5432", "ready", "variable"}, bkLine(t, r.stdout, "5432"))
	require.Equal(t, []string{"worker", "redis", "-", "pending", "canvas"}, bkLine(t, r.stdout, "worker"),
		"a route without a port shows a dash; its missing server name is an empty cell")
	require.Contains(t, r.stdout, "MESH PORT", "the route table follows the link table")

	r = f.run("mesh", "links", cfgProdID)
	require.Equal(t, 0, r.code, "an environment ID is accepted as is: %s", r.all())
	require.Len(t, f.calls("GET", msEnv), 2)

	r = f.run("--json", "mesh", "links", "acme-shop/production")
	require.Len(t, decode[map[string]any](t, r.stdout)["links"], 2)

	r = f.run("mesh", "links", "nonsense")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nonsense": use an environment ID or project/env`)
	r = f.run("mesh", "links", "acme-shop/nope")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `environment "nope" not found`)
	r = f.run("mesh", "links", "ghost/production")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `project "ghost" not found`)
	r = f.run("mesh", "links", "a/b/c")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use an environment ID or project/env")
}

func TestMeshServiceShowsExposuresRoutesAndConsumers(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", msService, 200, map[string]any{
		"exposures": []any{map[string]any{"server": map[string]any{"name": "edge-1"}, "purpose": "database", "containerPort": 5432, "meshPort": 15432, "alias": "pg"}},
		"routes":    []any{map[string]any{"consumerSlug": "pg", "provider": "backup", "server": map[string]any{"name": "edge-2"}, "port": 9000, "state": "ready", "source": "variable"}},
		"consumers": []any{map[string]any{"slug": "web", "server": map[string]any{"name": "edge-3"}, "source": "canvas", "state": "ready"}},
	})

	r := f.run("mesh", "service", fakeSvcID)
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"SERVER", "PURPOSE", "PORT", "ALIAS", "CONSUMER", "PROVIDER", "REACHED BY", "SOURCE"} {
		require.Contains(t, r.stdout, h)
	}
	require.Equal(t, []string{"edge-1", "database", "5432", "15432", "pg"}, bkLine(t, r.stdout, "15432")[:5])
	require.Equal(t, []string{"pg", "backup", "edge-2", "9000", "ready", "variable"}, bkLine(t, r.stdout, "9000"))
	require.Equal(t, []string{"web", "edge-3", "canvas", "ready"}, bkLine(t, r.stdout, "edge-3")[:4])

	r = f.run("--json", "mesh", "service", fakeSvcID)
	require.Len(t, decode[map[string]any](t, r.stdout)["exposures"], 1)

	r = f.run("mesh", "service", "not-a-ref")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "use a service ID or project/env/service-slug")

	f.problem("GET", msService, 404, "not_found", "service not found")
	r = f.run("mesh", "service", fakeSvcID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "service not found (not_found, HTTP 404)")
}

func TestMeshReconcileValidatesModeAndReportsTheOutcome(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", msEnv+"/reconcile", 200, map[string]any{
		"added": []any{map[string]any{"id": "l1"}, map[string]any{"id": "l2"}}, "removed": []any{map[string]any{"id": "l3"}},
		"waiting": 4, "devicesAdded": 2,
	})

	r := f.run("mesh", "reconcile", "acme-shop/production", "--accept-mesh-changes")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Mode add: 2 added, 1 removed, 4 waiting for their grace period, 2 device(s) added.\n", r.stdout)
	post := f.only("POST", msEnv+"/reconcile")
	require.Equal(t, map[string]any{"mode": "add", "acceptMeshChanges": true}, post.json(t))
	require.Equal(t, http.MethodPost, post.method)

	r = f.run("mesh", "reconcile", cfgProdID, "--mode", "repair")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Mode repair: ")
	require.Equal(t, map[string]any{"mode": "repair", "acceptMeshChanges": false}, f.calls("POST", msEnv+"/reconcile")[1].json(t))

	r = f.run("--json", "mesh", "reconcile", cfgProdID)
	require.Equal(t, float64(4), decode[map[string]any](t, r.stdout)["waiting"])

	before := f.total()
	r = f.run("mesh", "reconcile", cfgProdID, "--mode", "delete")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "--mode must be add or repair")
	require.Equal(t, before, f.total(), "a bad mode never reaches the API")

	f.reply("POST", msEnv+"/reconcile", 200, map[string]any{})
	r = f.run("mesh", "reconcile", cfgProdID)
	require.Equal(t, "Mode add: 0 added, 0 removed, 0 waiting for their grace period, - device(s) added.\n", r.stdout)

	f.problem("POST", msEnv+"/reconcile", 409, "mesh_changes_not_accepted", "mode add creates tailnet devices; pass --accept-mesh-changes")
	r = f.run("mesh", "reconcile", cfgProdID)
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "pass --accept-mesh-changes (mesh_changes_not_accepted, HTTP 409)")
}
