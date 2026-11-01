package domains

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TestServerRoutesCarryTheCloudflareOriginLock checks the route table the agents
// receive: which domains are locked, which require the client certificate, what
// the route carries, and that opt-outs, DNS-only and tunnelled domains stay open.
func TestServerRoutesCarryTheCloudflareOriginLock(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	ip := "203.0.113.10"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha", PublicIp: &ip})
	require.NoError(t, err)

	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(bg, sql, args...)
		require.NoError(t, err)
	}
	deploy := func(slug string) db.Service {
		t.Helper()
		sv, err := q.InsertService(bg, db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: "image",
			Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &srv.ID,
		})
		require.NoError(t, err)
		snap, _ := json.Marshal(map[string]any{"config": map[string]any{"image": "nginx:1", "port": 80}})
		src, _ := json.Marshal(map[string]any{"type": "image", "image": "nginx:1"})
		rel, err := q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: "nginx:1"})
		require.NoError(t, err)
		dep, err := q.InsertDeployment(bg, db.InsertDeploymentParams{
			ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &srv.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
		})
		require.NoError(t, err)
		exec("UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
		require.NoError(t, q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
		return sv
	}
	// domain adds a domain in a zone. proxied makes the Cloudflare record proxied
	// (the lock needs the record, not just the setting).
	domain := func(sv db.Service, host, zone string, proxied, optIn bool) db.Domain {
		t.Helper()
		d, err := q.InsertDomain(bg, db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80})
		require.NoError(t, err)
		exec(`UPDATE domains SET cf_zone_id = $2, cf_zone_name = 'x', cf_proxied = $3, cf_origin_lock = $4 WHERE id = $1`, d.ID, zone, proxied, optIn)
		require.NoError(t, q.UpsertCloudflareRecord(bg, db.UpsertCloudflareRecordParams{
			ZoneID: zone, RecordID: "rec-" + host, WorkspaceID: ws.ID, OwnerKind: "domain", OwnerID: d.ID, Hostname: host, Type: "A", Content: ip, Proxied: proxied,
		}))
		return d
	}
	aop := func(zone, state, code, reason string) {
		t.Helper()
		require.NoError(t, q.UpsertZoneAOP(bg, db.UpsertZoneAOPParams{WorkspaceID: ws.ID, ZoneID: zone, State: state, ReasonCode: code, Reason: reason}))
	}
	table := func() map[string]agentprotocol.Route {
		t.Helper()
		routes, err := ServerRoutes(bg, q, srv.ID, uuid.Nil)
		require.NoError(t, err)
		out := map[string]agentprotocol.Route{}
		for _, r := range routes {
			out[r.Hosts[0]] = r
		}
		return out
	}
	rule := func(r agentprotocol.Route, host string) agentprotocol.DomainRule {
		t.Helper()
		for _, d := range r.Domains {
			if d.Host == host {
				return d
			}
		}
		t.Fatalf("no rule for %s", host)
		return agentprotocol.DomainRule{}
	}

	web := deploy("web")
	locked := domain(web, "app.example.com", "z1", true, true)
	domain(web, "plain.example.com", "z1", false, true)  // DNS only
	domain(web, "optout.example.com", "z1", true, false) // operator opted out
	domain(web, "eu.example.org", "z2", true, true)      // zone without AOP
	aop("z1", "on", "", "")
	aop("z2", "unavailable", "permission", "no permission")

	other := deploy("docs")
	domain(other, "docs.example.net", "z3", false, true)

	got := table()
	wr := got["app.example.com"]
	require.NotNil(t, wr.OriginLock, "a route with a locked domain carries the shared data")
	require.Equal(t, cloudflare.Builtin().CIDRs(), wr.OriginLock.CloudflareCIDRs, "the built-in list until one was fetched")
	require.Equal(t, cloudflare.OriginPullCA(), wr.OriginLock.ClientCAPEM)

	a := rule(wr, "app.example.com")
	require.True(t, a.OriginLocked)
	require.True(t, a.RequireClientCert, "zone AOP is on")
	p := rule(wr, "plain.example.com")
	require.False(t, p.OriginLocked)
	require.False(t, p.RequireClientCert)
	o := rule(wr, "optout.example.com")
	require.False(t, o.OriginLocked, "opted out")
	eu := rule(wr, "eu.example.org")
	require.True(t, eu.OriginLocked, "IP lock applies without AOP")
	require.False(t, eu.RequireClientCert, "AOP is unavailable in that zone")

	dr := got["docs.example.net"]
	require.Nil(t, dr.OriginLock, "a route without locked domains carries nothing")
	require.False(t, rule(dr, "docs.example.net").OriginLocked)

	// The route push after Cloudflare changed its ranges carries the new list.
	list, err := cloudflare.NewIPList(cloudflare.IPSourceAPI, append([]string{"192.0.2.0/24"}, cloudflare.Builtin().V4...), cloudflare.Builtin().V6, nil)
	require.NoError(t, err)
	_, err = q.UpsertCloudflareIPList(bg, db.UpsertCloudflareIPListParams{V4: list.V4, V6: list.V6, Hash: list.Hash, Source: list.Source})
	require.NoError(t, err)
	require.Contains(t, table()["app.example.com"].OriginLock.CloudflareCIDRs, "192.0.2.0/24")

	// The record is not proxied (yet): the setting alone does not lock.
	exec(`UPDATE cloudflare_records SET proxied = false WHERE hostname = 'app.example.com'`)
	require.False(t, rule(table()["app.example.com"], "app.example.com").OriginLocked)
	exec(`UPDATE cloudflare_records SET proxied = true WHERE hostname = 'app.example.com'`)
	require.True(t, rule(table()["app.example.com"], "app.example.com").OriginLocked)

	// AOP switched off or unavailable: the certificate is no longer required, the IP lock stays.
	aop("z1", "unavailable", "ssl_mode", "flexible")
	a = rule(table()["app.example.com"], "app.example.com")
	require.True(t, a.OriginLocked)
	require.False(t, a.RequireClientCert)
	require.Empty(t, table()["app.example.com"].OriginLock.ClientCAPEM, "no CA when no domain requires the certificate")

	// A server behind a Cloudflare Tunnel has no public origin: nothing is locked.
	exec(`INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name, state, activated_at) VALUES ($1, $2, 'acc', 'tun', 'n', 'online', now())`, srv.ID, ws.ID)
	for host, r := range table() {
		for _, d := range r.Domains {
			require.False(t, d.OriginLocked, "%s is tunnelled", host)
		}
		require.Nil(t, r.OriginLock)
	}
	exec(`DELETE FROM server_tunnels`)

	// The opt-out wins over everything else.
	exec(`UPDATE domains SET cf_origin_lock = false WHERE id = $1`, locked.ID)
	require.False(t, rule(table()["app.example.com"], "app.example.com").OriginLocked)
}
