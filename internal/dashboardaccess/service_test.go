package dashboardaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute/systemroutetest"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	tokenCanary = "cf-dashboard-token-canary-4711"
	host        = "nebula.example.com"
)

type adminFunc func(uuid.UUID) bool

func (f adminFunc) IsInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) { return f(id), nil }

// pusher records the system route configuration at every push, inside the
// transaction, the way proxyserver.PushRoutes builds the table.
type pusher struct {
	mu     sync.Mutex
	pushes []systemroute.Config
	fail   error
}

func (p *pusher) PushRoutes(_ context.Context, _ pgx.Tx, _ uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	c, _ := systemroute.Current()
	p.pushes = append(p.pushes, c)
	return nil
}

func (p *pusher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pushes)
}

type env struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	ws     uuid.UUID
	srv    db.Server
	admin  uuid.UUID
	ctx    context.Context // the instance admin
	push   *pusher
	svc    *Service
	box    *secretbox.Box
	dns    *dns01.Service
	logs   *bytes.Buffer
	logger *slog.Logger
}

func principalFor(user, ws uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Role: auth.RoleOwner, Label: "admin@example.com", Scopes: policy.AllScopes,
	})
}

// newEnv builds a control plane with an online control-plane server, an enabled
// system route, the instance admin and (optionally) the installer settings.
func newEnv(t *testing.T, inst config.Installer) *env {
	t.Helper()
	pool := testutil.NewPool(t)
	bg := context.Background()
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "Team")
	require.NoError(t, err)
	admin, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "admin@example.com", Name: "Admin", PasswordHash: "x"})
	require.NoError(t, err)
	email := "ops@example.com"
	srv, err := q.CreateControlPlaneServer(bg, db.CreateControlPlaneServerParams{WorkspaceID: ws.ID, Name: "control-plane", AcmeEmail: &email})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapDNS01}})
	_, err = pool.Exec(bg, `UPDATE servers SET status='online', facts=$2 WHERE id=$1`, srv.ID, facts)
	require.NoError(t, err)
	srv, err = q.GetControlPlaneServer(bg)
	require.NoError(t, err)

	inst.DashboardUpstream = "nebulactrl:8080"
	cfg := &config.Config{PublicURL: "https://" + host, Installer: inst}
	systemroute.Configure(cfg)
	t.Cleanup(func() { systemroutetest.Set(systemroute.Config{}) })

	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	push := &pusher{}
	svc := NewService(Deps{Pool: pool, Config: cfg, Admins: adminFunc(func(id uuid.UUID) bool { return id == admin.ID }), Routes: push, Logger: logger})
	return &env{
		pool: pool, q: q, ws: ws.ID, srv: srv, admin: admin.ID, ctx: principalFor(admin.ID, ws.ID), push: push, svc: svc, box: box,
		dns: dns01.NewService(pool, nil, box), logs: logs, logger: logger,
	}
}

func requireAPIError(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, status, ae.Status, ae.Error())
	if code != "" {
		require.Equal(t, code, ae.Code)
	}
	return ae
}

// viaRoute is a request that came through the host proxy from client.
func viaRoute(client string) Peer { return Peer{RemoteAddr: "10.212.0.3:40000", ForwardedFor: client} }

func (e *env) addCloudflareResolver(t *testing.T) {
	t.Helper()
	ctx := e.ctx
	cred, err := e.dns.CreateCredential(ctx, dns01.CredentialInput{Name: "cf", Provider: "cloudflare", Values: map[string]string{"CF_DNS_API_TOKEN": tokenCanary}})
	require.NoError(t, err)
	_, err = e.dns.SetResolver(ctx, e.srv.ID, "cloudflare", dns01.ResolverInput{CredentialID: cred.ID})
	require.NoError(t, err)
}

// applyDNS makes the server's desired DNS-01 configuration its applied one, as
// a successful proxy_configure result does.
func (e *env) applyDNS(t *testing.T) {
	t.Helper()
	require.NoError(t, e.applyDNSQuiet())
}

func (e *env) applyDNSQuiet() error {
	snap, err := dns01.Desired(context.Background(), e.q, e.srv.ID)
	if err != nil {
		return err
	}
	var pc agentprotocol.ProxyConfig
	snap.Apply(&pc)
	return e.q.SetServerAppliedDNS01(context.Background(), db.SetServerAppliedDNS01Params{ID: e.srv.ID, AppliedDns01: dns01.SnapshotOf(pc)})
}

func TestDashboardAccessIsInstanceAdminOnly(t *testing.T) {
	e := newEnv(t, config.Installer{})
	member, err := e.q.CreateUser(context.Background(), db.CreateUserParams{WorkspaceID: e.ws, Email: "m@example.com", Name: "M", PasswordHash: "x"})
	require.NoError(t, err)
	owner := principalFor(member.ID, e.ws) // a team owner who is not the instance admin
	tokenID := uuid.New()
	token := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, TokenID: &tokenID, UserID: e.admin, WorkspaceID: e.ws, Scopes: []string{policy.ScopeServersRead}})

	for name, ctx := range map[string]context.Context{"team owner, not instance admin": owner, "token without admin scope": token} {
		_, err := e.svc.Get(ctx, Peer{})
		requireAPIError(t, err, 403, "")
		_, err = e.svc.Update(ctx, Peer{}, UpdateInput{AllowCIDRs: []string{"100.64.0.0/10"}})
		requireAPIError(t, err, 403, "")
		t.Log(name)
	}
	_, err = e.svc.Get(context.Background(), Peer{})
	requireAPIError(t, err, 401, "")
	_, err = e.svc.Update(context.Background(), Peer{}, UpdateInput{})
	requireAPIError(t, err, 401, "")
	require.Zero(t, e.push.count())
	_, err = e.q.GetDashboardAccess(context.Background())
	require.ErrorIs(t, err, pgx.ErrNoRows, "a refused update stores nothing")

	// A token with the admin scope that belongs to the instance admin may.
	adminToken := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, TokenID: &tokenID, UserID: e.admin, WorkspaceID: e.ws, Scopes: []string{policy.ScopeAdmin}})
	_, err = e.svc.Get(adminToken, Peer{})
	require.NoError(t, err)
}

func TestGetDefaults(t *testing.T) {
	e := newEnv(t, config.Installer{})
	v, err := e.svc.Get(e.ctx, viaRoute("203.0.113.9"))
	require.NoError(t, err)
	require.True(t, v.Enabled)
	require.Equal(t, host, v.Host)
	require.Equal(t, []string{}, v.AllowCIDRs)
	require.Empty(t, v.DNSResolver)
	require.Equal(t, []DashboardResolverOption{}, v.ResolverAvailable)
	require.Equal(t, MethodHTTP01, v.Certificate.Method)
	require.Equal(t, "203.0.113.9", v.ClientIP)
	require.True(t, v.ClientIPAllowed)
	require.True(t, v.ViaDashboardRoute)
	require.False(t, v.ProxyDeployRequired)
	require.Equal(t, SourceDefault, v.Source)
	require.NotNil(t, v.ProxyServerID)
	require.Equal(t, e.srv.ID, *v.ProxyServerID)

	// Reached directly (SSH tunnel, plain IP): the route's allow list is not what this caller meets.
	v, err = e.svc.Get(e.ctx, Peer{RemoteAddr: "198.51.100.20:5555"})
	require.NoError(t, err)
	require.False(t, v.ViaDashboardRoute)
	require.Equal(t, "198.51.100.20", v.ClientIP)

	// Without a system route there is nothing to restrict.
	systemroutetest.Set(systemroute.Config{})
	v, err = e.svc.Get(e.ctx, Peer{})
	require.NoError(t, err)
	require.False(t, v.Enabled)
	_, err = e.svc.Update(e.ctx, Peer{}, UpdateInput{AllowCIDRs: []string{"100.64.0.0/10"}})
	requireAPIError(t, err, 409, "dashboard_route_disabled")
}

func TestLockoutGuard(t *testing.T) {
	tailnet := []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}
	cases := []struct {
		name       string
		peer       Peer
		allow      []string
		confirm    bool
		wantLocked bool // 409 dashboard_lockout
		wantIP     string
	}{
		{"excludes the caller", viaRoute("203.0.113.9"), tailnet, false, true, "203.0.113.9"},
		{"excludes the caller, confirmed", viaRoute("203.0.113.9"), tailnet, true, false, ""},
		{"includes the caller by CIDR", viaRoute("100.101.102.103"), tailnet, false, false, ""},
		{"includes the caller by single address", viaRoute("203.0.113.9"), []string{"203.0.113.9"}, false, false, ""},
		{"IPv6 caller inside the tailnet range", viaRoute("fd7a:115c:a1e0:ab12:4843:cd96:6200:1234"), tailnet, false, false, ""},
		{"IPv6 caller outside it", viaRoute("2001:db8::7"), tailnet, false, true, "2001:db8::7"},
		{"IPv4-mapped IPv6 caller is the IPv4 address", viaRoute("::ffff:100.64.0.9"), tailnet, false, false, ""},
		{"CIDR boundary: first address outside", viaRoute("100.128.0.0"), tailnet, false, true, "100.128.0.0"},
		{"CIDR boundary: last address inside", viaRoute("100.127.255.255"), tailnet, false, false, ""},
		{"empty list never locks out", viaRoute("203.0.113.9"), nil, false, false, ""},
		{"a forged leading entry does not help", viaRoute("100.64.0.9, 203.0.113.9"), tailnet, false, true, "203.0.113.9"},
		{"a direct caller is not subject to the route", Peer{RemoteAddr: "203.0.113.9:1234"}, tailnet, false, false, ""},
		{"a direct caller cannot spoof the header", Peer{RemoteAddr: "203.0.113.9:1234", ForwardedFor: "100.64.0.9"}, tailnet, false, false, ""},
		{"a local caller (loopback, no header)", Peer{RemoteAddr: "127.0.0.1:1234"}, tailnet, false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, config.Installer{})
			before, _ := systemroute.Current()
			v, err := e.svc.Update(e.ctx, c.peer, UpdateInput{AllowCIDRs: c.allow, ConfirmLockout: c.confirm})
			if c.wantLocked {
				ae := requireAPIError(t, err, 409, "dashboard_lockout")
				require.Contains(t, ae.Error(), c.wantIP, "the message names the caller's address")
				after, _ := systemroute.Current()
				require.Equal(t, before.AllowCIDRs, after.AllowCIDRs, "a refused save changes nothing")
				require.Zero(t, e.push.count())
				_, err := e.q.GetDashboardAccess(context.Background())
				require.ErrorIs(t, err, pgx.ErrNoRows)
				return
			}
			require.NoError(t, err)
			require.Equal(t, nonNil(c.allow), v.AllowCIDRs)
			cur, _ := systemroute.Current()
			require.Equal(t, nonNil(c.allow), nonNil(cur.AllowCIDRs))
			require.Equal(t, 1, e.push.count(), "the control-plane server's routes were re-sent")
			require.Equal(t, nonNil(c.allow), nonNil(e.push.pushes[0].AllowCIDRs), "the pushed table already carried the new list")
			if len(c.allow) > 0 && c.confirm {
				require.False(t, v.ClientIPAllowed, "the view still tells the caller is out")
			}
		})
	}
}

func TestUpdateValidationAndNormalization(t *testing.T) {
	e := newEnv(t, config.Installer{})
	peer := viaRoute("100.64.0.9")
	for name, allow := range map[string][]string{
		"not a CIDR":        {"100.64.0.0/10", "nope"},
		"prefix too long":   {"10.0.0.0/33"},
		"IPv6 prefix long":  {"fd00::/129"},
		"a name":            {"example.com"},
		"hostname with dot": {"1.2.3"},
	} {
		_, err := e.svc.Update(e.ctx, peer, UpdateInput{AllowCIDRs: allow})
		requireAPIError(t, err, 422, "invalid_cidr")
		t.Log(name)
	}
	var many []string
	for i := range 201 {
		many = append(many, fmt.Sprintf("10.%d.%d.0/24", i/250, i%250))
	}
	_, err := e.svc.Update(e.ctx, peer, UpdateInput{AllowCIDRs: many, ConfirmLockout: true})
	requireAPIError(t, err, 422, "invalid_cidr")
	require.Zero(t, e.push.count())

	// Whitespace, blanks and duplicates are tidied.
	v, err := e.svc.Update(e.ctx, peer, UpdateInput{AllowCIDRs: []string{" 100.64.0.0/10 ", "", "100.64.0.0/10", "fd7a:115c:a1e0::/48"}})
	require.NoError(t, err)
	require.Equal(t, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, v.AllowCIDRs)
	require.Equal(t, SourceAdmin, v.Source)
	row, err := e.q.GetDashboardAccess(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, row.AllowCidrs)
	require.NotNil(t, row.EditedAt)
	require.NotNil(t, row.EditedBy)
	require.Equal(t, e.admin, *row.EditedBy)

	// Clearing the list lifts the restriction.
	v, err = e.svc.Update(e.ctx, peer, UpdateInput{})
	require.NoError(t, err)
	require.Empty(t, v.AllowCIDRs)
	cur, _ := systemroute.Current()
	require.Empty(t, cur.AllowCIDRs)
}

func TestUpdateRollsBackWhenThePushFails(t *testing.T) {
	e := newEnv(t, config.Installer{})
	e.push.fail = fmt.Errorf("agent queue unavailable")
	_, err := e.svc.Update(e.ctx, viaRoute("100.64.0.9"), UpdateInput{AllowCIDRs: []string{"100.64.0.0/10"}})
	require.Error(t, err)
	cur, _ := systemroute.Current()
	require.Empty(t, cur.AllowCIDRs, "the process-wide configuration is restored")
	_, err = e.q.GetDashboardAccess(context.Background())
	require.ErrorIs(t, err, pgx.ErrNoRows, "and so is the row")
}

func TestUpdateIsAudited(t *testing.T) {
	e := newEnv(t, config.Installer{})
	_, err := e.svc.Update(e.ctx, viaRoute("203.0.113.9"), UpdateInput{AllowCIDRs: []string{"100.64.0.0/10"}, ConfirmLockout: true})
	require.NoError(t, err)
	var action, actorType string
	var meta []byte
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT action, actor_type, metadata FROM audit_events WHERE action = $1`, ActionUpdate).Scan(&action, &actorType, &meta))
	require.Equal(t, "dashboard.access.update", action)
	require.Equal(t, "user", actorType)
	var m map[string]any
	require.NoError(t, json.Unmarshal(meta, &m))
	require.Equal(t, true, m["confirmLockout"])
	require.EqualValues(t, 1, m["allowCidrCount"])
	require.Equal(t, []any{"100.64.0.0/10"}, m["allowCidrs"])
}

func TestResolverSelection(t *testing.T) {
	e := newEnv(t, config.Installer{})
	peer := viaRoute("100.64.0.9")

	_, err := e.svc.Update(e.ctx, peer, UpdateInput{DNSResolver: "nope-provider"})
	requireAPIError(t, err, 422, "invalid_dns_resolver")
	_, err = e.svc.Update(e.ctx, peer, UpdateInput{DNSResolver: "cloudflare"})
	requireAPIError(t, err, 422, "resolver_not_configured")
	require.Zero(t, e.push.count())

	// A staged resolver is accepted but the proxy must be deployed.
	e.addCloudflareResolver(t)
	v, err := e.svc.Get(e.ctx, peer)
	require.NoError(t, err)
	require.Equal(t, []DashboardResolverOption{{Provider: "cloudflare", ProviderName: "Cloudflare", Applied: false}}, v.ResolverAvailable)
	v, err = e.svc.Update(e.ctx, peer, UpdateInput{DNSResolver: "Cloudflare"})
	require.NoError(t, err)
	require.Equal(t, "cloudflare", v.DNSResolver)
	require.True(t, v.ProxyDeployRequired)
	require.Equal(t, MethodDNS01, v.Certificate.Method)
	require.Equal(t, "pending", v.Certificate.Status)
	require.Contains(t, v.Certificate.Message, "Deploy proxy")

	// Once the proxy applied it, nothing is left to do.
	e.applyDNS(t)
	v, err = e.svc.Get(e.ctx, peer)
	require.NoError(t, err)
	require.False(t, v.ProxyDeployRequired)
	require.Equal(t, MethodDNS01, v.Certificate.Method)
	require.Equal(t, "unknown", v.Certificate.Status)
	require.True(t, v.ResolverAvailable[0].Applied)

	// Back to HTTP-01.
	v, err = e.svc.Update(e.ctx, peer, UpdateInput{})
	require.NoError(t, err)
	require.Empty(t, v.DNSResolver)
	require.Equal(t, MethodHTTP01, v.Certificate.Method)

	// The token appears in no response and no audit row.
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), tokenCanary)
	var meta string
	rows, err := e.pool.Query(context.Background(), `SELECT metadata::text FROM audit_events`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		require.NoError(t, rows.Scan(&meta))
		require.NotContains(t, meta, tokenCanary)
	}
}

// The control-plane server's route table carries the stored settings, and names
// a DNS-01 resolver only when the server has one.
func TestRouteTableCarriesTheSettings(t *testing.T) {
	e := newEnv(t, config.Installer{})
	ctx := context.Background()
	table := func() agentprotocol.Route {
		routes, err := domains.ServerRoutes(ctx, e.q, e.srv.ID, uuid.Nil)
		require.NoError(t, err)
		for _, r := range routes {
			if r.ServiceID == systemroute.ServiceID {
				return r
			}
		}
		t.Fatal("no system route in the table")
		return agentprotocol.Route{}
	}

	d := table().Domains[0]
	require.Nil(t, d.Options.Access)
	require.Empty(t, d.DNSResolver)

	e.addCloudflareResolver(t)
	_, err := e.svc.Update(e.ctx, viaRoute("100.64.0.9"), UpdateInput{AllowCIDRs: []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, DNSResolver: "cloudflare"})
	require.NoError(t, err)
	d = table().Domains[0]
	require.Equal(t, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, d.Options.Access.AllowCIDRs)
	require.Equal(t, "cloudflare", d.DNSResolver)

	// The resolver disappears from the server: the dashboard falls back to HTTP-01.
	require.NoError(t, func() error {
		_, err := e.pool.Exec(ctx, `DELETE FROM server_dns_resolvers WHERE server_id = $1`, e.srv.ID)
		return err
	}())
	d = table().Domains[0]
	require.Empty(t, d.DNSResolver)
	require.NotNil(t, d.Options.Access)
}

func TestLoadSeedsFromTheInstallerUntilAnAdminEdits(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, config.Installer{})
	// Nothing configured, nothing stored.
	require.NoError(t, e.svc.Load(ctx))
	_, err := e.q.GetDashboardAccess(ctx)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// The installer's list seeds the row and the route configuration.
	cfg := func(cidrs ...string) *Service {
		return NewService(Deps{Pool: e.pool, Config: &config.Config{Installer: config.Installer{DashboardAllowCIDRs: cidrs}}, Admins: e.svc.admins, Routes: e.push, Logger: e.logger})
	}
	require.NoError(t, cfg("100.64.0.0/10").Load(ctx))
	row, err := e.q.GetDashboardAccess(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"100.64.0.0/10"}, row.AllowCidrs)
	require.True(t, row.SeededFromInstaller)
	require.Nil(t, row.EditedAt)
	cur, _ := systemroute.Current()
	require.Equal(t, []string{"100.64.0.0/10"}, cur.AllowCIDRs)
	v, err := e.svc.Get(e.ctx, Peer{})
	require.NoError(t, err)
	require.Equal(t, SourceInstaller, v.Source)

	// A restart with the same value changes nothing; a re-run with another list replaces it.
	updated := row.UpdatedAt
	require.NoError(t, cfg("100.64.0.0/10").Load(ctx))
	row, _ = e.q.GetDashboardAccess(ctx)
	require.True(t, row.UpdatedAt.Equal(updated), "an identical seed does not touch the row")
	require.NoError(t, cfg("192.0.2.0/24", "fd7a:115c:a1e0::/48").Load(ctx))
	cur, _ = systemroute.Current()
	require.Equal(t, []string{"192.0.2.0/24", "fd7a:115c:a1e0::/48"}, cur.AllowCIDRs)

	// An admin's edit makes the row the source of truth: restarts and installer re-runs leave it alone.
	_, err = e.svc.Update(e.ctx, viaRoute("203.0.113.9"), UpdateInput{AllowCIDRs: []string{"203.0.113.0/24"}})
	require.NoError(t, err)
	require.NoError(t, cfg("100.64.0.0/10").Load(ctx))
	row, _ = e.q.GetDashboardAccess(ctx)
	require.Equal(t, []string{"203.0.113.0/24"}, row.AllowCidrs)
	cur, _ = systemroute.Current()
	require.Equal(t, []string{"203.0.113.0/24"}, cur.AllowCIDRs, "the edit survives a restart")
	// Even an installer run without any list does not wipe the admin's list.
	require.NoError(t, cfg().Load(ctx))
	cur, _ = systemroute.Current()
	require.Equal(t, []string{"203.0.113.0/24"}, cur.AllowCIDRs)
	v, err = e.svc.Get(e.ctx, Peer{})
	require.NoError(t, err)
	require.Equal(t, SourceAdmin, v.Source)
}

func TestLoadKeepsALiveResolverOnlyForTheSameProvider(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, config.Installer{})
	svc := func(resolver string) *Service {
		return NewService(Deps{Pool: e.pool, Config: &config.Config{Installer: config.Installer{DashboardDNSResolver: resolver, DashboardDNSToken: "x"}}, Admins: e.svc.admins, Routes: e.push, Logger: e.logger})
	}
	require.NoError(t, svc("cloudflare").Load(ctx))
	row, _ := e.q.GetDashboardAccess(ctx)
	require.Empty(t, row.DnsResolver, "the resolver counts only once it is live")
	// The seeder records it when the proxy runs it.
	changed, err := svc("cloudflare").resolverLive(ctx, e.srv, "cloudflare")
	require.NoError(t, err)
	require.True(t, changed)
	cur, _ := systemroute.Current()
	require.Equal(t, "cloudflare", cur.DNSResolver)
	require.Equal(t, 1, e.push.count(), "the dashboard route was re-sent")
	changed, err = svc("cloudflare").resolverLive(ctx, e.srv, "cloudflare")
	require.NoError(t, err)
	require.False(t, changed, "recording it twice is a no-op")

	require.NoError(t, svc("cloudflare").Load(ctx))
	cur, _ = systemroute.Current()
	require.Equal(t, "cloudflare", cur.DNSResolver, "a restart keeps it")
	require.NoError(t, svc("").Load(ctx))
	cur, _ = systemroute.Current()
	require.Empty(t, cur.DNSResolver, "an installer run without DNS-01 goes back to HTTP-01")

	// An edited row is never touched by the installer.
	require.NoError(t, svc("cloudflare").Load(ctx))
	_, err = e.svc.Update(e.ctx, Peer{}, UpdateInput{})
	require.NoError(t, err)
	changed, err = svc("cloudflare").resolverLive(ctx, e.srv, "cloudflare")
	require.NoError(t, err)
	require.False(t, changed)
	cur, _ = systemroute.Current()
	require.Empty(t, cur.DNSResolver)
}

// ---- seeder ------------------------------------------------------------------

// fakeProxy applies the desired DNS-01 configuration the way a successful
// proxy_configure result does.
type fakeProxy struct {
	e       *env
	deploys int
	err     error
}

func (f *fakeProxy) Deploy(ctx context.Context, serverID uuid.UUID, _ bool) (proxyserver.ProxyDeployResult, error) {
	if f.err != nil {
		return proxyserver.ProxyDeployResult{}, f.err
	}
	f.deploys++
	return proxyserver.ProxyDeployResult{}, f.e.applyDNSQuiet()
}

func (e *env) seeder(t *testing.T, proxy ProxyDeployer, inst config.Installer) *Seeder {
	t.Helper()
	inst.DashboardUpstream = "nebulactrl:8080"
	cfg := &config.Config{PublicURL: "https://" + host, Installer: inst}
	svc := NewService(Deps{Pool: e.pool, Config: cfg, Admins: e.svc.admins, Routes: e.push, Logger: e.logger})
	return NewSeeder(SeederDeps{Pool: e.pool, Service: svc, DNS: e.dns, Proxy: proxy, Config: cfg, Logger: e.logger})
}

var seedEnv = config.Installer{DashboardDNSResolver: "cloudflare", DashboardDNSToken: tokenCanary, DashboardAllowCIDRs: []string{"100.64.0.0/10"}}

func TestSeederSetsUpDNS01Idempotently(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, seedEnv)
	require.NoError(t, e.svc.Load(ctx))
	fp := &fakeProxy{e: e}
	s := e.seeder(t, fp, seedEnv)
	require.True(t, s.Enabled())

	// The agent has not reported yet: nothing happens.
	_, err := e.pool.Exec(ctx, `UPDATE servers SET status='pending' WHERE id=$1`, e.srv.ID)
	require.NoError(t, err)
	done, err := s.Step(ctx)
	require.NoError(t, err)
	require.False(t, done)
	creds, err := e.dns.ListCredentials(principalFor(e.admin, e.ws), nil)
	require.NoError(t, err)
	require.Empty(t, creds)
	_, err = e.pool.Exec(ctx, `UPDATE servers SET status='online' WHERE id=$1`, e.srv.ID)
	require.NoError(t, err)

	// Step 1 creates the credential and stages the resolver, step 2 deploys the
	// proxy, step 3 sees it applied and switches the dashboard over.
	for i, want := range []bool{false, false, true} {
		done, err := s.Step(ctx)
		require.NoError(t, err, "step %d", i+1)
		require.Equal(t, want, done, "step %d", i+1)
	}
	require.Equal(t, 1, fp.deploys)
	creds, err = e.dns.ListCredentials(principalFor(e.admin, e.ws), nil)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	require.Equal(t, "dashboard-cloudflare", creds[0].Name)
	require.Equal(t, "cloudflare", creds[0].Provider)
	require.Equal(t, "server", creds[0].Scope)
	require.Equal(t, &e.srv.ID, creds[0].ServerID)
	st, err := e.dns.GetServerState(principalFor(e.admin, e.ws), e.srv.ID)
	require.NoError(t, err)
	require.Len(t, st.Resolvers, 1)
	require.Equal(t, dns01.ChangeNone, st.Resolvers[0].Change)

	row, err := e.q.GetDashboardAccess(ctx)
	require.NoError(t, err)
	require.Equal(t, "cloudflare", row.DnsResolver)
	require.Equal(t, []string{"100.64.0.0/10"}, row.AllowCidrs)
	cur, _ := systemroute.Current()
	require.Equal(t, "cloudflare", cur.DNSResolver)
	require.Equal(t, 1, e.push.count())

	// The token is sealed: the stored column is not the token.
	var sealed []byte
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT secrets_enc FROM dns_credentials WHERE id=$1`, creds[0].ID).Scan(&sealed))
	require.NotContains(t, string(sealed), tokenCanary)

	// Running it again (a restart) changes nothing: no second credential, deploy or push.
	s2 := e.seeder(t, fp, seedEnv)
	done, err = s2.Step(ctx)
	require.NoError(t, err)
	require.True(t, done)
	creds, _ = e.dns.ListCredentials(principalFor(e.admin, e.ws), nil)
	require.Len(t, creds, 1)
	require.Equal(t, 1, fp.deploys)
	require.Equal(t, 1, e.push.count())

	// The token appears nowhere it should not: logs, audit, the API view.
	require.NotContains(t, e.logs.String(), tokenCanary)
	rows, err := e.pool.Query(ctx, `SELECT metadata::text || action FROM audit_events`)
	require.NoError(t, err)
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var m string
		require.NoError(t, rows.Scan(&m))
		require.NotContains(t, m, tokenCanary)
		actions = append(actions, m)
	}
	require.True(t, containsAction(actions, "dns_credential.create"), "the credential creation is audited by the store")
	require.True(t, containsAction(actions, ActionSeed))
	v, err := e.svc.Get(e.ctx, viaRoute("100.64.0.9"))
	require.NoError(t, err)
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), tokenCanary)
	require.Equal(t, MethodDNS01, v.Certificate.Method)
	require.False(t, v.ProxyDeployRequired)
}

func containsAction(rows []string, action string) bool {
	for _, r := range rows {
		if strings.HasSuffix(r, action) {
			return true
		}
	}
	return false
}

func TestSeederLeavesEditedSettingsAlone(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, seedEnv)
	require.NoError(t, e.svc.Load(ctx))
	_, err := e.svc.Update(e.ctx, Peer{}, UpdateInput{AllowCIDRs: []string{"203.0.113.0/24"}})
	require.NoError(t, err)
	fp := &fakeProxy{e: e}
	done, err := e.seeder(t, fp, seedEnv).Step(ctx)
	require.NoError(t, err)
	require.True(t, done)
	creds, _ := e.dns.ListCredentials(principalFor(e.admin, e.ws), nil)
	require.Empty(t, creds)
	require.Zero(t, fp.deploys)
}

func TestSeederNeedsAnACMEEmailAndRetriesDeploys(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, seedEnv)
	require.NoError(t, e.svc.Load(ctx))
	_, err := e.pool.Exec(ctx, `UPDATE servers SET acme_email=NULL WHERE id=$1`, e.srv.ID)
	require.NoError(t, err)
	fp := &fakeProxy{e: e}
	s := e.seeder(t, fp, seedEnv)
	done, err := s.Step(ctx)
	require.ErrorContains(t, err, "NEBULACTRL_ACME_EMAIL")
	require.False(t, done)
	require.NotContains(t, err.Error(), tokenCanary)

	// A deploy already in progress is waited for, not an error.
	_, err = e.pool.Exec(ctx, `UPDATE servers SET acme_email='ops@example.com' WHERE id=$1`, e.srv.ID)
	require.NoError(t, err)
	fp.err = apierr.Conflict("proxy_deploy_in_progress", "busy")
	for range 3 {
		done, err = s.Step(ctx)
		require.NoError(t, err)
		require.False(t, done)
	}
	require.Zero(t, fp.deploys)

	// A proxy that never applies it is deployed to a bounded number of times,
	// with a grace period in between, and then reported.
	fp.err = nil
	now := time.Now()
	s.now = func() time.Time { return now }
	noApply := &noApplyProxy{}
	s.proxy = noApply
	for range seedMaxDeploys {
		_, err = s.Step(ctx)
		require.NoError(t, err)
		_, err = s.Step(ctx) // inside the grace period: no second deploy
		require.NoError(t, err)
		now = now.Add(seedDeployGrace + time.Second)
	}
	require.Equal(t, seedMaxDeploys, noApply.deploys)
	_, err = s.Step(ctx)
	require.ErrorContains(t, err, "Deploy proxy")
	require.Equal(t, seedMaxDeploys, noApply.deploys)
}

type noApplyProxy struct{ deploys int }

func (p *noApplyProxy) Deploy(context.Context, uuid.UUID, bool) (proxyserver.ProxyDeployResult, error) {
	p.deploys++
	return proxyserver.ProxyDeployResult{}, nil
}
