package domains

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/cfzone"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const autoCFToken = "cf-domains-canary-token-2718"

// autoCF is a team with a server that has an ACME contact, a deployed service
// and a Cloudflare connection.
type autoCF struct {
	t    *testing.T
	q    *db.Queries
	exec func(sql string, args ...any)
	ws   db.Workspace
	srv  db.Server
	svc  db.Service
	box  *secretbox.Box
}

func newAutoCF(t *testing.T) *autoCF {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	ip, email := "203.0.113.10", "ops@example.com"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha", PublicIp: &ip, AcmeEmail: &email})
	require.NoError(t, err)
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(bg, sql, args...)
		require.NoError(t, err)
	}
	sv, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
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
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	return &autoCF{t: t, q: q, exec: exec, ws: ws, srv: srv, svc: sv, box: box}
}

func (c *autoCF) connect(zones ...cfzone.Zone) {
	c.t.Helper()
	enc, err := c.box.SealString(secretbox.CloudflareConnectionsToken, c.ws.ID.String(), autoCFToken)
	require.NoError(c.t, err)
	raw, err := json.Marshal(zones)
	require.NoError(c.t, err)
	_, err = c.q.UpsertCloudflareConnection(context.Background(), db.UpsertCloudflareConnectionParams{
		WorkspaceID: c.ws.ID, TokenEnc: enc, AccountID: "acc", AccountName: "Account", Zones: raw,
	})
	require.NoError(c.t, err)
}

func (c *autoCF) domain(host string, mut func(id uuid.UUID)) db.Domain {
	c.t.Helper()
	d, err := c.q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: c.svc.ID, Hostname: host, TargetPort: 80})
	require.NoError(c.t, err)
	if mut != nil {
		mut(d.ID)
	}
	return d
}

func (c *autoCF) resolvers() map[string]string {
	c.t.Helper()
	rs, err := ServerRoutes(context.Background(), c.q, c.srv.ID, uuid.Nil)
	require.NoError(c.t, err)
	out := map[string]string{}
	for _, r := range rs {
		for _, d := range r.Domains {
			out[d.Host] = d.DNSResolver
		}
	}
	return out
}

func zoneOf(id, name string) cfzone.Zone {
	return cfzone.Zone{ID: id, Name: name, Status: "active", AccountID: "acc"}
}

// synced marks a domain as a DNS sync left it: its record written in zone z1.
func (c *autoCF) synced(status, syncError string) func(id uuid.UUID) {
	return func(id uuid.UUID) {
		c.exec(`UPDATE domains SET cf_zone_id = $2, cf_status = $3, cf_error = $4 WHERE id = $1`, id, "z1", status, syncError)
	}
}

// The route table the agents receive names the cloudflare resolver on the
// domains of a connected zone, whatever their proxy setting, wildcard hosts
// included, and leaves everything else alone.
func TestServerRoutesNameTheAutomaticCloudflareResolver(t *testing.T) {
	c := newAutoCF(t)
	c.domain("dnsonly.example.com", func(id uuid.UUID) {
		c.synced("managed", "")(id)
		c.exec(`UPDATE domains SET cf_proxied = false WHERE id = $1`, id)
	})
	c.domain("proxied.example.com", func(id uuid.UUID) {
		c.synced("managed", "")(id)
		c.exec(`UPDATE domains SET cf_proxied = true WHERE id = $1`, id)
	})
	c.domain("*.preview.example.com", c.synced("managed", ""))
	c.domain("elsewhere.other.org", nil)
	c.domain("plain.example.com", func(id uuid.UUID) {
		c.synced("managed", "")(id)
		c.exec(`UPDATE domains SET tls_mode = 'none' WHERE id = $1`, id)
	})
	c.domain("custom.example.com", func(id uuid.UUID) {
		c.synced("managed", "")(id)
		c.exec(`UPDATE domains SET tls_mode = 'custom' WHERE id = $1`, id)
	})
	c.domain("new.example.com", nil)                           // not synced yet
	c.domain("foreign.example.com", c.synced("present", ""))   // a record somebody else made
	c.domain("conflict.example.com", c.synced("conflict", "")) //
	c.domain("refused.example.com", c.synced("managed", "cloudflare API answered 403 (code 9109): no"))
	c.domain("manual.other.org", func(id uuid.UUID) { c.exec(`UPDATE domains SET dns_resolver = 'route53' WHERE id = $1`, id) })

	c.connect(zoneOf("z1", "example.com"))
	got := c.resolvers()
	cf := agentprotocol.DNSCloudflare
	require.Equal(t, map[string]string{
		"dnsonly.example.com":   cf, // DNS only
		"proxied.example.com":   cf, // proxied: the same
		"*.preview.example.com": cf, // a wildcard host in the zone
		"elsewhere.other.org":   "",
		"plain.example.com":     "", // no TLS
		"custom.example.com":    "", // an uploaded certificate
		"new.example.com":       "", // waiting for the first successful DNS sync
		"foreign.example.com":   "", // a foreign record proves nothing
		"conflict.example.com":  "",
		"refused.example.com":   "", // the last write was refused: HTTP-01 again
		"manual.other.org":      "route53",
	}, got)

	// The sync that wrote the record turns the name over on the next render.
	c.exec(`UPDATE domains SET cf_zone_id = 'z1', cf_status = 'managed' WHERE hostname = 'new.example.com'`)
	require.Equal(t, cf, c.resolvers()["new.example.com"])

	// Tunnel names keep their behaviour: the server's tunnel serves every name of a connected zone.
	c.exec(`INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name, state, activated_at)
		VALUES ($1, $2, 'acc', 't1', 'tunnel', 'online', now())`, c.srv.ID, c.ws.ID)
	got = c.resolvers()
	require.Empty(t, got["dnsonly.example.com"])
	require.Empty(t, got["proxied.example.com"])
}

// No certificate gap on the switch: the control plane's applied configuration
// (what every route push carries) has no Cloudflare resolver until Deploy proxy
// applied it, so the agent serves the name over HTTP-01 meanwhile (agent test
// TestAutoCloudflareUsesHTTP01UntilTheResolverIsApplied), and the token reaches
// the agent only in the secrets of the command.
func TestAppliedProxyConfigGetsTheResolverOnlyWithDeploy(t *testing.T) {
	c := newAutoCF(t)
	d := c.domain("app.example.com", c.synced("managed", ""))
	c.connect(zoneOf("z1", "example.com"))
	ctx := context.Background()

	require.Equal(t, agentprotocol.DNSCloudflare, c.resolvers()["app.example.com"], "the rule names it from the start")
	applied, err := proxyserver.BuildConfig(ctx, c.q, c.srv.ID)
	require.NoError(t, err)
	require.False(t, applied.HasDNSResolver(agentprotocol.DNSCloudflare), "…but the proxy runs no such resolver yet")
	secrets, err := dns01.Secrets(ctx, c.q, c.box, c.srv.ID, applied)
	require.NoError(t, err)
	require.Empty(t, secrets)

	// What Deploy proxy sends and applies.
	want, err := dns01.Desired(ctx, c.q, c.srv.ID)
	require.NoError(t, err)
	require.Len(t, want.Resolvers, 1)
	var pc agentprotocol.ProxyConfig
	want.Apply(&pc)
	raw, err := json.Marshal(agentprotocol.ProxyConfigurePayload{Proxy: pc, ACMEEmail: "ops@example.com"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), autoCFToken)
	require.NoError(t, c.q.SetServerAppliedDNS01(ctx, db.SetServerAppliedDNS01Params{ID: c.srv.ID, AppliedDns01: dns01.SnapshotOf(pc)}))

	applied, err = proxyserver.BuildConfig(ctx, c.q, c.srv.ID)
	require.NoError(t, err)
	require.True(t, applied.HasDNSResolver(agentprotocol.DNSCloudflare))
	cfg, err := json.Marshal(applied)
	require.NoError(t, err)
	require.NotContains(t, string(cfg), autoCFToken, "never in the configuration every route push carries")
	secrets, err = dns01.Secrets(ctx, c.q, c.box, c.srv.ID, applied)
	require.NoError(t, err)
	require.Equal(t, autoCFToken, secrets[agentprotocol.DNSSecretKey(dns01.AutoResolverID(c.ws.ID), "CF_DNS_API_TOKEN")])

	fresh, err := c.q.GetDomainByID(ctx, d.ID)
	require.NoError(t, err)
	certs, err := dns01.DomainCerts(ctx, c.q, []db.Domain{fresh})
	require.NoError(t, err)
	require.True(t, certs[d.ID].Automatic)
	require.False(t, certs[d.ID].Staged)
}

// Hostnames of status pages in a connected zone get the same resolver; a page of
// another team, a page whose record no sync wrote and a refused write do not.
func TestStatusRoutesNameTheAutomaticCloudflareResolver(t *testing.T) {
	e := newStatusEnv(t)
	bg := context.Background()
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	enc, err := box.SealString(secretbox.CloudflareConnectionsToken, e.ws.ID.String(), autoCFToken)
	require.NoError(t, err)
	connect := func(zones ...cfzone.Zone) {
		t.Helper()
		raw, _ := json.Marshal(zones)
		_, err := e.q.UpsertCloudflareConnection(bg, db.UpsertCloudflareConnectionParams{WorkspaceID: e.ws.ID, TokenEnc: enc, AccountID: "acc", AccountName: "A", Zones: raw})
		require.NoError(t, err)
	}
	in := e.page(t, "in", new("status.example.com"), true)
	e.page(t, "out", new("status.customer.org"), true)
	e.page(t, "fresh", new("fresh.example.com"), true)
	setPage := func(id uuid.UUID, status, syncError string) {
		t.Helper()
		_, err := e.pool.Exec(bg, `UPDATE status_pages SET cf_zone_id = 'z1', cf_status = $2, cf_error = $3 WHERE id = $1`, id, status, syncError)
		require.NoError(t, err)
	}
	setPage(in, "managed", "")
	other, err := e.q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	var foreign uuid.UUID
	require.NoError(t, e.pool.QueryRow(bg, `INSERT INTO status_pages (workspace_id, title, slug, hostname, published) VALUES ($1, 'f', 'f', 'status.foreign.example.com', true) RETURNING id`, other.ID).Scan(&foreign))

	resolverOf := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for h, r := range e.routes(t, e.cp) {
			out[h] = r.Domains[0].DNSResolver
		}
		return out
	}
	require.Equal(t, map[string]string{"status.example.com": "", "status.customer.org": "", "status.foreign.example.com": "", "fresh.example.com": ""}, resolverOf())

	connect(zoneOf("z1", "example.com"))
	require.Equal(t, map[string]string{
		"status.example.com":         agentprotocol.DNSCloudflare,
		"status.customer.org":        "",
		"status.foreign.example.com": "", // the other team's page: not issued with this team's token
		"fresh.example.com":          "", // its record has not been written yet
	}, resolverOf())

	setPage(in, "managed", "cloudflare API answered 403 (code 9109): no")
	require.Equal(t, "", resolverOf()["status.example.com"], "the last write was refused: HTTP-01")
}

// A wildcard hostname, or a name pinned to the cloudflare resolver, is accepted
// without a resolver on the server when its zone is connected; elsewhere the old
// rules stand.
func TestValidateDNS01AcceptsTheAutomaticChoice(t *testing.T) {
	c := newAutoCF(t)
	ctx := context.Background()
	var s *Service
	wildcard := spec{Hostname: "*.preview.example.com", TLSMode: agentprotocol.TLSAuto}
	pinned := spec{Hostname: "app.example.com", TLSMode: agentprotocol.TLSAuto, DNSResolver: agentprotocol.DNSCloudflare}

	require.Error(t, s.validateDNS01(ctx, c.q, &c.srv, wildcard), "no connection: a wildcard needs a resolver or a wildcard certificate")
	require.Error(t, s.validateDNS01(ctx, c.q, &c.srv, pinned), "no cloudflare resolver on the server")

	c.connect(zoneOf("z1", "example.com"))
	require.NoError(t, s.validateDNS01(ctx, c.q, &c.srv, wildcard), "no evidence is needed to accept the name: its record is synced after it is created")
	require.NoError(t, s.validateDNS01(ctx, c.q, &c.srv, pinned))
	outside := spec{Hostname: "*.preview.other.org", TLSMode: agentprotocol.TLSAuto}
	require.Error(t, s.validateDNS01(ctx, c.q, &c.srv, outside), "a wildcard outside every connected zone still needs one")

	c.connect(zoneOf("z2", "other.org"))
	require.Error(t, s.validateDNS01(ctx, c.q, &c.srv, wildcard), "the zone is not connected")
}
