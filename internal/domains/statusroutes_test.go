package domains

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type statusEnv struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	ws    db.Workspace
	cp    db.Server
	other db.Server
}

func newStatusEnv(t *testing.T) *statusEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	email := "ops@example.com"
	cp, err := q.CreateControlPlaneServer(bg, db.CreateControlPlaneServerParams{WorkspaceID: ws.ID, Name: "control-plane", AcmeEmail: &email})
	require.NoError(t, err)
	other, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "other"})
	require.NoError(t, err)
	statusroute.Set("nebulactrl:8081")
	t.Cleanup(func() {
		statusroute.Set("")
		systemroute.Set(systemroute.Config{})
	})
	return &statusEnv{pool: pool, q: q, ws: ws, cp: cp, other: other}
}

// page inserts a status page; a nil host means "no hostname yet".
func (e *statusEnv) page(t *testing.T, slug string, host *string, published bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`INSERT INTO status_pages (workspace_id, title, slug, hostname, published) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		e.ws.ID, slug, slug, host, published).Scan(&id))
	return id
}

// resolver gives the control-plane server a DNS-01 resolver of provider.
func (e *statusEnv) resolver(t *testing.T, provider string) {
	t.Helper()
	bg := context.Background()
	cred, err := e.q.InsertDNSCredential(bg, db.InsertDNSCredentialParams{
		ID: uuid.New(), WorkspaceID: e.ws.ID, Name: provider, Provider: provider, SecretsEnc: []byte("sealed"), EnvNames: []string{"CF_DNS_API_TOKEN"},
	})
	require.NoError(t, err)
	require.NoError(t, e.q.UpsertServerDNSResolver(bg, db.UpsertServerDNSResolverParams{ServerID: e.cp.ID, Provider: provider, CredentialID: cred.ID}))
}

func (e *statusEnv) routes(t *testing.T, server db.Server) map[string]agentprotocol.Route {
	t.Helper()
	rs, err := ServerRoutes(context.Background(), e.q, server.ID, uuid.Nil)
	require.NoError(t, err)
	byHost := map[string]agentprotocol.Route{}
	for _, r := range rs {
		for _, h := range r.Hosts {
			require.NotContains(t, byHost, h, "one route per host")
			byHost[h] = r
		}
	}
	return byHost
}

func TestPublishedPagesGetAPublicRouteOnTheControlPlaneServerOnly(t *testing.T) {
	e := newStatusEnv(t)
	live := e.page(t, "live", new("status.example.com"), true)
	e.page(t, "draft", new("draft.example.com"), false)
	e.page(t, "nohost", nil, true)

	got := e.routes(t, e.cp)
	require.Len(t, got, 1, "only the published page that has a hostname")
	r := got["status.example.com"]
	require.Equal(t, statusroute.ServiceID(live), r.ServiceID, "the id derives from the page id")
	require.Equal(t, []string{"nebulactrl:8081"}, r.Upstreams, "the status listener, never the dashboard's")
	require.Equal(t, agentprotocol.RouteKindExternal, r.Kind)
	require.Equal(t, "/healthz", r.HealthPath)
	require.Len(t, r.Domains, 1)
	d := r.Domains[0]
	require.Equal(t, "status.example.com", d.Host)
	require.Equal(t, agentprotocol.TLSAuto, d.TLSMode)
	require.Nil(t, d.Options.Access, "public: no allow list")
	require.Equal(t, "basic", d.Options.SecurityHeaders)
	require.Empty(t, d.DNSResolver, "HTTP-01 without a resolver")
	require.Empty(t, r.AllowCIDRs)

	require.Empty(t, e.routes(t, e.other), "other servers never serve status pages")

	// The id is stable across rebuilds, and unpublishing removes the route.
	require.Equal(t, r.ServiceID, e.routes(t, e.cp)["status.example.com"].ServiceID)
	_, err := e.pool.Exec(context.Background(), `UPDATE status_pages SET published = false WHERE id = $1`, live)
	require.NoError(t, err)
	require.Empty(t, e.routes(t, e.cp))
}

func TestStatusRoutesNeedAConfiguredUpstream(t *testing.T) {
	e := newStatusEnv(t)
	e.page(t, "live", new("status.example.com"), true)
	require.Len(t, e.routes(t, e.cp), 1)
	statusroute.Set("")
	require.Empty(t, e.routes(t, e.cp), "no upstream, no routes")
}

func TestStatusRoutesNeverShadowTheDashboardOrAnotherRoute(t *testing.T) {
	e := newStatusEnv(t)
	systemroute.Set(systemroute.Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080"})
	e.page(t, "evil", new("nebula.example.com"), true)
	e.page(t, "ok", new("status.example.com"), true)

	got := e.routes(t, e.cp)
	require.Len(t, got, 2)
	require.Equal(t, systemroute.ServiceID, got["nebula.example.com"].ServiceID, "the dashboard host keeps its own route")
	require.Equal(t, []string{"nebulactrl:8080"}, got["nebula.example.com"].Upstreams)
	require.Equal(t, []string{"nebulactrl:8081"}, got["status.example.com"].Upstreams)
}

// The dashboard's DNS-01 credential is the instance's own: no status page uses
// it, whatever its hostname (a team could otherwise spend the instance's
// certificate allowance). The dashboard's own route keeps it.
func TestStatusRoutesNeverUseTheDashboardsDNS01Resolver(t *testing.T) {
	e := newStatusEnv(t)
	systemroute.Set(systemroute.Config{Host: "nebula.example.com", Upstream: "nebulactrl:8080", DNSResolver: "cloudflare"})
	e.page(t, "same", new("status.example.com"), true)
	e.page(t, "sub", new("status.eu.example.com"), true)
	e.page(t, "foreign", new("status.customer.org"), true)
	e.resolver(t, "cloudflare")

	got := map[string]string{}
	for h, r := range e.routes(t, e.cp) {
		got[h] = r.Domains[0].DNSResolver
	}
	require.Equal(t, map[string]string{
		"nebula.example.com": "cloudflare", "status.example.com": "", "status.eu.example.com": "", "status.customer.org": "",
	}, got)
}

// A proxied status page whose Cloudflare record is NebulaCtrl's is locked to
// Cloudflare in the control-plane server's route table, like a service domain:
// the status route (a system route without a domain row) carries the lock data.
func TestProxiedStatusPagesCarryTheCloudflareOriginLock(t *testing.T) {
	e := newStatusEnv(t)
	bg := context.Background()
	const host = "status.example.com"
	live := e.page(t, "live", new(host), true)
	statusRoute := func() agentprotocol.Route {
		t.Helper()
		r, ok := e.routes(t, e.cp)[host]
		require.True(t, ok, "the page is served")
		require.Len(t, r.Domains, 1)
		return r
	}
	set := func(sql string, args ...any) {
		t.Helper()
		_, err := e.pool.Exec(bg, sql, append([]any{live}, args...)...)
		require.NoError(t, err)
	}
	record := func(proxied bool) {
		t.Helper()
		require.NoError(t, e.q.UpsertCloudflareRecord(bg, db.UpsertCloudflareRecordParams{
			ZoneID: "z1", RecordID: "rec-1", WorkspaceID: e.ws.ID, OwnerKind: "status_page", OwnerID: live,
			Hostname: host, Type: "A", Content: "203.0.113.10", Proxied: proxied,
		}))
	}

	// A page that has no Cloudflare settings is open.
	r := statusRoute()
	require.False(t, r.Domains[0].OriginLocked)
	require.Nil(t, r.OriginLock)

	// Proxied and opted in, but the record is not proxied yet: the setting alone does not lock.
	set(`UPDATE status_pages SET cf_zone_id = 'z1', cf_zone_name = 'example.com', cf_proxied = true, cf_origin_lock = true WHERE id = $1`)
	record(false)
	require.False(t, statusRoute().Domains[0].OriginLocked)

	// The proxied record locks the status route: the IP lock applies, the route carries Cloudflare's ranges.
	record(true)
	r = statusRoute()
	require.True(t, r.Domains[0].OriginLocked)
	require.False(t, r.Domains[0].RequireClientCert, "no Authenticated Origin Pulls state in the zone yet")
	require.NotNil(t, r.OriginLock)
	require.Equal(t, cloudflare.Builtin().CIDRs(), r.OriginLock.CloudflareCIDRs)
	require.Empty(t, r.OriginLock.ClientCAPEM)
	require.Equal(t, []string{"nebulactrl:8081"}, r.Upstreams, "still the status listener")

	// With Authenticated Origin Pulls on in the zone, the origin requires Cloudflare's client certificate.
	require.NoError(t, e.q.UpsertZoneAOP(bg, db.UpsertZoneAOPParams{WorkspaceID: e.ws.ID, ZoneID: "z1", State: "on"}))
	r = statusRoute()
	require.True(t, r.Domains[0].OriginLocked)
	require.True(t, r.Domains[0].RequireClientCert)
	require.Equal(t, cloudflare.OriginPullCA(), r.OriginLock.ClientCAPEM)

	// Opt-out, DNS only and a zone the page left: each opens the route again.
	for name, sql := range map[string]string{
		"opted out":     `UPDATE status_pages SET cf_origin_lock = false WHERE id = $1`,
		"dns only":      `UPDATE status_pages SET cf_proxied = false WHERE id = $1`,
		"left the zone": `UPDATE status_pages SET cf_zone_id = '' WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			set(`UPDATE status_pages SET cf_zone_id = 'z1', cf_proxied = true, cf_origin_lock = true WHERE id = $1`)
			require.True(t, statusRoute().Domains[0].OriginLocked)
			set(sql)
			r := statusRoute()
			require.False(t, r.Domains[0].OriginLocked)
			require.False(t, r.Domains[0].RequireClientCert)
			require.Nil(t, r.OriginLock)
		})
	}

	// An unpublished page has no route, hence nothing to lock; other servers never get one.
	set(`UPDATE status_pages SET cf_zone_id = 'z1', cf_proxied = true, cf_origin_lock = true, published = false WHERE id = $1`)
	require.NotContains(t, e.routes(t, e.cp), host)
	require.Empty(t, e.routes(t, e.other))
}
