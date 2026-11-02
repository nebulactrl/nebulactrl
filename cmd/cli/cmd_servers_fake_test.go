package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	srvList   = cfgAPI + "/servers"
	srvRevoke = cfgAPI + "/servers/" + dbSrvID + "/revoke"
	srvTokens = cfgAPI + "/servers/enrollment-tokens"
)

func srvView(name, id string, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "name": name, "status": "online", "drainState": "none", "serviceCount": 2, "acceptWorkloads": true}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestServersLsShowsOptionalColumnsOnlyWhenTheyMatter(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", srvList, 200, map[string]any{"items": []any{
		srvView("plain", "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaa1", map[string]any{"publicIp": "203.0.113.5", "agentVersion": "1.4.0", "lastSeenAt": "2026-01-02T12:00:00Z"}),
		srvView("pending", "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaa2", map[string]any{"status": "pending", "serviceCount": 0}),
	}})

	r := f.run("servers", "ls")
	require.Equal(t, 0, r.code, r.all())
	for _, absent := range []string{"ROLE", "DRAIN", "MODE"} {
		require.NotContains(t, r.stdout, absent, "no server needs the %s column", absent)
	}
	require.Equal(t, []string{"plain", "online", "203.0.113.5", "2", "1.4.0", "2026-01-02"}, bkLine(t, r.stdout, "plain")[:6])
	require.Equal(t, []string{"pending", "pending", "-", "0", "-", "-", "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaa2"}, bkLine(t, r.stdout, "pending"),
		"a server that never connected shows dashes")

	f.reply("GET", srvList, 200, map[string]any{"items": []any{
		srvView("builder", "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaa3", map[string]any{"buildEnabled": true, "acceptWorkloads": false}),
		srvView("hybrid", "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaa4", map[string]any{"buildEnabled": true, "unrestricted": true, "drainState": "draining"}),
		srvView("quiet", "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaa5", nil),
	}})
	r = f.run("servers", "ls")
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"ROLE", "DRAIN", "MODE"} {
		require.Contains(t, r.stdout, h)
	}
	require.Equal(t, []string{"builder", "online", "builds", "none", "restricted"}, bkLine(t, r.stdout, "builder")[:5])
	require.Equal(t, []string{"hybrid", "online", "both", "draining", "unrestricted"}, bkLine(t, r.stdout, "hybrid")[:5])
	require.Equal(t, []string{"quiet", "online", "deployments", "none", "restricted"}, bkLine(t, r.stdout, "quiet")[:5])

	r = f.run("--json", "servers", "ls")
	require.Len(t, decode[[]any](t, r.stdout), 3)

	f.problem("GET", srvList, 403, "forbidden", "needs servers:read")
	r = f.run("servers", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs servers:read (forbidden, HTTP 403)")
}

func TestServersEnrollTokenPrintsTheOneTimeTokenAndInstallCommand(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", srvTokens, 201, map[string]any{
		"server": map[string]any{"id": dbSrvID, "name": "edge-1"}, "token": "nce_secret", "expiresAt": "2026-01-02T12:00:00Z",
		"installCommand": "curl -fsSL https://ctrl.example.com/install.sh | sh -s -- nce_secret",
	})

	r := f.run("--idempotency-key", "en-key", "servers", "enroll-token", "--name", "edge-1", "--public-ip", "203.0.113.5", "--base-domain", "apps.example.com", "--acme-email", "ops@example.com")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Server   edge-1 ("+dbSrvID+")\nToken    nce_secret\nExpires  2026-01-02")
	require.Contains(t, r.stdout, "Run on the host (needs Docker):\n  curl -fsSL https://ctrl.example.com/install.sh | sh -s -- nce_secret\n")
	post := f.only("POST", srvTokens)
	require.Equal(t, map[string]any{"name": "edge-1", "publicIp": "203.0.113.5", "baseDomain": "apps.example.com", "acmeEmail": "ops@example.com"}, post.json(t))
	require.Equal(t, "en-key", post.header.Get("Idempotency-Key"))

	f.reply("POST", srvTokens, 201, map[string]any{"server": map[string]any{"id": dbSrvID, "name": "edge-1"}, "token": "nce_x", "expiresAt": "2026-01-02T12:00:00Z"})
	r = f.run("servers", "enroll-token", "--name", "edge-1")
	require.Equal(t, 0, r.code, r.all())
	require.NotContains(t, r.stdout, "Run on the host", "no install command, no instructions")
	require.Equal(t, map[string]any{"name": "edge-1"}, f.calls("POST", srvTokens)[1].json(t))

	r = f.run("--json", "servers", "enroll-token", "--name", "edge-1")
	require.Equal(t, "nce_x", decode[map[string]any](t, r.stdout)["token"])

	before := f.total()
	r = f.run("servers", "enroll-token")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--name is required")
	require.Equal(t, before, f.total())

	f.problem("POST", srvTokens, 409, "name_taken", "a server named edge-1 exists")
	r = f.run("servers", "enroll-token", "--name", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "a server named edge-1 exists (name_taken, HTTP 409)")
}

func TestServersRevokeResolvesNameOrID(t *testing.T) {
	f := newCfgFake(t)
	dbServers(f)
	f.reply("POST", srvRevoke, 200, map[string]any{"id": dbSrvID, "name": "edge-1"})

	r := f.run("servers", "revoke", "EDGE-1")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Revoked server edge-1 ("+dbSrvID+")\n", r.stdout)

	r = f.run("--json", "servers", "revoke", dbSrvID)
	require.Equal(t, "edge-1", decode[map[string]any](t, r.stdout)["name"])
	require.Len(t, f.calls("POST", srvRevoke), 2)

	r = f.run("servers", "revoke", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `server "ghost" not found`)
	require.Len(t, f.calls("POST", srvRevoke), 2, "an unknown server revokes nothing")

	f.problem("POST", srvRevoke, 409, "control_plane", "the control-plane server cannot be revoked")
	r = f.run("servers", "revoke", "edge-1")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the control-plane server cannot be revoked (control_plane, HTTP 409)")

	r = f.run("servers", "revoke")
	require.Equal(t, 2, r.code)
}
