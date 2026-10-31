package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestPlatformAllowListIsInternalRangesPlusServerAddresses(t *testing.T) {
	got := PlatformAllowList([]string{
		"203.0.113.10", "2001:db8::7", "203.0.113.10", // a duplicate
		"10.1.2.3",         // already inside 10/8
		"100.100.1.1",      // already inside the tailnet range
		"::ffff:192.0.2.5", // IPv4-mapped: listed as IPv4
		"not-an-ip", "", "0.0.0.0",
	})
	n := len(PlatformInternalCIDRs)
	require.Equal(t, PlatformInternalCIDRs, got[:n], "the internal ranges come first and are always there")
	require.Equal(t, []string{"192.0.2.5", "203.0.113.10", "2001:db8::7"}, got[n:])

	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fd7a:115c:a1e0::/48", "fc00::/7", "127.0.0.0/8", "::1/128"} {
		require.Contains(t, got, want)
	}
	require.NoError(t, agentprotocol.ValidateAccessLists(got, nil, true), "the proxy accepts the list")

	require.Equal(t, PlatformInternalCIDRs, PlatformAllowList(nil), "no server address: internal ranges only, never an empty (open) list")
}

func TestPlatformAllowListIsBoundedByTheProxyLimit(t *testing.T) {
	var many []string
	for i := range 300 {
		many = append(many, fmt.Sprintf("198.51.%d.%d", i/200, i%200+1))
	}
	got := PlatformAllowList(many)
	require.Len(t, got, agentprotocol.MaxAccessCIDRs)
	require.Equal(t, PlatformInternalCIDRs, got[:len(PlatformInternalCIDRs)])
	require.NoError(t, agentprotocol.ValidateAccessLists(got, nil, true))
}

func TestRestrictPlatformAccessNeverWidens(t *testing.T) {
	platform := PlatformAllowList([]string{"203.0.113.10"})

	t.Run("no user list", func(t *testing.T) {
		a := RestrictPlatformAccess(nil, platform, false)
		require.Equal(t, platform, a.AllowCIDRs)
		require.Empty(t, a.DenyCIDRs)
		require.Zero(t, a.TrustedProxyDepth)
	})

	t.Run("an open user list is cut down to the platform list", func(t *testing.T) {
		for _, open := range [][]string{{"0.0.0.0/0"}, {"::/0"}, {"0.0.0.0/0", "::/0"}} {
			a := RestrictPlatformAccess(&agentprotocol.Access{AllowCIDRs: open}, platform, false)
			require.NotEmpty(t, a.AllowCIDRs)
			require.False(t, agentprotocol.AnyOpenCIDR(a.AllowCIDRs), "%v must not stay open", open)
			for _, c := range a.AllowCIDRs {
				require.Contains(t, platform, c, "%v is not inside the platform list", c)
			}
		}
	})

	t.Run("a narrower user list narrows", func(t *testing.T) {
		a := RestrictPlatformAccess(&agentprotocol.Access{AllowCIDRs: []string{"10.1.0.0/16", "203.0.113.10", "8.8.8.8"}}, platform, false)
		require.ElementsMatch(t, []string{"10.1.0.0/16", "203.0.113.10"}, a.AllowCIDRs, "the public address outside the platform list is dropped")
	})

	t.Run("a user list that shares nothing is ignored, not open", func(t *testing.T) {
		a := RestrictPlatformAccess(&agentprotocol.Access{AllowCIDRs: []string{"8.8.8.8", "1.1.1.0/24"}}, platform, false)
		require.Equal(t, platform, a.AllowCIDRs)
	})

	t.Run("a user list wider than one range keeps the platform range", func(t *testing.T) {
		a := RestrictPlatformAccess(&agentprotocol.Access{AllowCIDRs: []string{"10.0.0.0/7"}}, platform, false)
		require.Equal(t, []string{"10.0.0.0/8"}, a.AllowCIDRs)
	})

	t.Run("deny stays, reject status stays", func(t *testing.T) {
		a := RestrictPlatformAccess(&agentprotocol.Access{DenyCIDRs: []string{"10.9.0.0/16"}, RejectStatus: 404}, platform, false)
		require.Equal(t, []string{"10.9.0.0/16"}, a.DenyCIDRs)
		require.Equal(t, 404, a.RejectStatus)
		require.Equal(t, platform, a.AllowCIDRs)
		_, err := agentprotocol.EffectiveAllow(a.AllowCIDRs, a.DenyCIDRs)
		require.NoError(t, err)
	})

	t.Run("a deny list that would leave nobody is dropped", func(t *testing.T) {
		a := RestrictPlatformAccess(&agentprotocol.Access{DenyCIDRs: []string{"0.0.0.0/0", "::/0"}}, platform, false)
		require.Empty(t, a.DenyCIDRs)
		require.Equal(t, platform, a.AllowCIDRs)
	})

	t.Run("client-IP depth is zero unless the IP comes from a trusted proxy", func(t *testing.T) {
		user := &agentprotocol.Access{TrustedProxyDepth: 3}
		require.Zero(t, RestrictPlatformAccess(user, platform, false).TrustedProxyDepth, "a forged X-Forwarded-For must not pass")
		require.Equal(t, 3, RestrictPlatformAccess(user, platform, true).TrustedProxyDepth)
	})

	t.Run("the user's value is not mutated", func(t *testing.T) {
		user := &agentprotocol.Access{AllowCIDRs: []string{"0.0.0.0/0"}}
		RestrictPlatformAccess(user, platform, false)
		require.Equal(t, []string{"0.0.0.0/0"}, user.AllowCIDRs)
	})
}

func TestIntersectCIDRs(t *testing.T) {
	require.ElementsMatch(t, []string{"10.1.0.0/16"}, intersectCIDRs([]string{"10.1.0.0/16"}, []string{"10.0.0.0/8"}))
	require.ElementsMatch(t, []string{"10.1.0.0/16"}, intersectCIDRs([]string{"10.0.0.0/8"}, []string{"10.1.0.0/16"}))
	require.ElementsMatch(t, []string{"10.1.2.3"}, intersectCIDRs([]string{"10.1.2.3"}, []string{"10.0.0.0/8"}))
	require.Empty(t, intersectCIDRs([]string{"11.0.0.0/8"}, []string{"10.0.0.0/8"}))
	require.Empty(t, intersectCIDRs([]string{"2001:db8::/32"}, []string{"10.0.0.0/8"}), "families do not mix")
	require.ElementsMatch(t, []string{"fd7a:115c:a1e0:1::/64"}, intersectCIDRs([]string{"fd7a:115c:a1e0:1::/64"}, []string{"fc00::/7"}))
	require.Empty(t, intersectCIDRs([]string{"junk"}, []string{"10.0.0.0/8"}))
}

// TestServerRoutesRestrictPlatformServices checks the table agents receive: every
// domain of a platform service is limited to the internal ranges and the
// servers of the workspace, whatever the domain's own options say; other
// services are untouched.
func TestServerRoutesRestrictPlatformServices(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	other, err := q.CreateWorkspace(bg, "other team")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "platform", Slug: "platform"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "platform", Slug: "platform"})
	require.NoError(t, err)

	server := func(w uuid.UUID, name, ip string) db.Server {
		t.Helper()
		p := db.CreateServerParams{WorkspaceID: w, Name: name}
		if ip != "" {
			p.PublicIp = &ip
		}
		s, err := q.CreateServer(bg, p)
		require.NoError(t, err)
		return s
	}
	alpha := server(ws.ID, "alpha", "203.0.113.10")
	server(ws.ID, "beta", "2001:db8::7")
	server(ws.ID, "no-address", "")
	revoked := server(ws.ID, "gone", "198.51.100.9")
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", revoked.ID)
	require.NoError(t, err)
	server(other.ID, "stranger", "192.0.2.77")

	deploy := func(slug string) db.Service {
		t.Helper()
		sv, err := q.InsertService(bg, db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: "image",
			Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &alpha.ID,
		})
		require.NoError(t, err)
		snap, _ := json.Marshal(map[string]any{"config": map[string]any{"image": "nginx:1", "port": 80}})
		src, _ := json.Marshal(map[string]any{"type": "image", "image": "nginx:1"})
		rel, err := q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: "nginx:1"})
		require.NoError(t, err)
		dep, err := q.InsertDeployment(bg, db.InsertDeploymentParams{
			ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &alpha.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
		})
		require.NoError(t, err)
		_, err = pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
		require.NoError(t, err)
		require.NoError(t, q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
		return sv
	}
	domain := func(sv db.Service, host string, options string) db.Domain {
		t.Helper()
		p := db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80}
		if options != "" {
			p.Options = json.RawMessage(options)
		}
		d, err := q.InsertDomain(bg, p)
		require.NoError(t, err)
		return d
	}
	hs := deploy("headscale")
	domain(hs, "hs.example.com", "")
	domain(hs, "admin.example.com", `{"access":{"allowCidrs":["0.0.0.0/0","::/0"],"trustedProxyDepth":2}}`)
	domain(hs, "narrow.example.com", `{"access":{"allowCidrs":["10.1.0.0/16","8.8.8.8"],"denyCidrs":["10.1.5.0/24"]}}`)
	web := deploy("web")
	domain(web, "app.example.com", "")
	domain(web, "closed.example.com", `{"access":{"allowCidrs":["198.18.0.0/15"]}}`)

	table := func() map[string]agentprotocol.DomainRule {
		t.Helper()
		routes, err := ServerRoutes(bg, q, alpha.ID, uuid.Nil)
		require.NoError(t, err)
		out := map[string]agentprotocol.DomainRule{}
		for _, r := range routes {
			for _, d := range r.Domains {
				out[d.Host] = d
			}
		}
		return out
	}

	// Before it is a platform service nothing is restricted.
	before := table()
	require.Nil(t, before["hs.example.com"].Options.Access)
	require.Nil(t, before["app.example.com"].Options.Access)

	_, err = q.InsertPlatformService(bg, db.InsertPlatformServiceParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Kind: "headscale", EnvironmentID: &env.ID, ServiceIds: []uuid.UUID{hs.ID},
		Hostname: "hs.example.com", PublicUrl: "https://hs.example.com", Options: json.RawMessage(`{}`), SecretsEnc: []byte{},
	})
	require.NoError(t, err)

	want := PlatformAllowList([]string{"203.0.113.10", "2001:db8::7"})
	got := table()

	a := got["hs.example.com"].Options.Access
	require.NotNil(t, a, "the Headscale hostname always carries an allow list")
	require.Equal(t, want, a.AllowCIDRs)
	require.NotContains(t, a.AllowCIDRs, "198.51.100.9", "a revoked server's address is not allowed")
	require.NotContains(t, a.AllowCIDRs, "192.0.2.77", "another team's server is not allowed")

	open := got["admin.example.com"].Options.Access
	require.False(t, agentprotocol.AnyOpenCIDR(open.AllowCIDRs), "an open list on the domain cannot make a platform service public")
	require.Zero(t, open.TrustedProxyDepth)
	for _, c := range open.AllowCIDRs {
		require.Contains(t, want, c)
	}

	narrow := got["narrow.example.com"].Options.Access
	require.Equal(t, []string{"10.1.0.0/16"}, narrow.AllowCIDRs)
	require.Equal(t, []string{"10.1.5.0/24"}, narrow.DenyCIDRs)

	require.Nil(t, got["app.example.com"].Options.Access, "other services are untouched")
	require.Equal(t, []string{"198.18.0.0/15"}, got["closed.example.com"].Options.Access.AllowCIDRs)

	// The deploy payload of the platform service carries the same rule.
	rules := []agentprotocol.DomainRule{{Host: "hs.example.com"}}
	require.NoError(t, ApplyPlatformAccessToRules(bg, q, alpha.ID, hs.ID, rules))
	require.Equal(t, want, rules[0].Options.Access.AllowCIDRs)
	plain := []agentprotocol.DomainRule{{Host: "app.example.com"}}
	require.NoError(t, ApplyPlatformAccessToRules(bg, q, alpha.ID, web.ID, plain))
	require.Nil(t, plain[0].Options.Access)

	// A new server joins the list, a revoked one leaves it.
	gamma := server(ws.ID, "gamma", "203.0.113.55")
	require.Equal(t, PlatformAllowList([]string{"203.0.113.10", "2001:db8::7", "203.0.113.55"}), table()["hs.example.com"].Options.Access.AllowCIDRs)
	_, err = pool.Exec(bg, "UPDATE servers SET public_ip = '203.0.113.56' WHERE id = $1", gamma.ID)
	require.NoError(t, err)
	require.Equal(t, PlatformAllowList([]string{"203.0.113.10", "2001:db8::7", "203.0.113.56"}), table()["hs.example.com"].Options.Access.AllowCIDRs)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'revoked' WHERE id = $1", gamma.ID)
	require.NoError(t, err)
	require.Equal(t, want, table()["hs.example.com"].Options.Access.AllowCIDRs)

	// PlatformServerIDs names the servers whose tables carry platform routes.
	ids, err := PlatformServerIDs(bg, q, ws.ID)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{alpha.ID}, ids)
	none, err := PlatformServerIDs(bg, q, other.ID)
	require.NoError(t, err)
	require.Empty(t, none)
}

type pushRecorder struct{ servers []uuid.UUID }

func (p *pushRecorder) PushRoutes(_ context.Context, _ pgx.Tx, id uuid.UUID) error {
	p.servers = append(p.servers, id)
	return nil
}

func TestPlatformRouteSyncPushesTheServersThatRunPlatformServices(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "platform", Slug: "platform"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "platform", Slug: "platform"})
	require.NoError(t, err)
	alpha, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha"})
	require.NoError(t, err)
	beta, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "beta"})
	require.NoError(t, err)

	insert := func(slug, kind string, srv uuid.UUID) db.Service {
		t.Helper()
		sv, err := q.InsertService(bg, db.InsertServiceParams{
			ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: "image",
			Config: json.RawMessage(`{}`), ServerID: &srv,
		})
		require.NoError(t, err)
		if kind != "" {
			_, err = q.InsertPlatformService(bg, db.InsertPlatformServiceParams{
				ID: uuid.New(), WorkspaceID: ws.ID, Kind: kind, EnvironmentID: &env.ID, ServiceIds: []uuid.UUID{sv.ID},
				Hostname: slug + ".example.com", PublicUrl: "https://" + slug + ".example.com", Options: json.RawMessage(`{}`), SecretsEnc: []byte{},
			})
			require.NoError(t, err)
		}
		return sv
	}
	changed := func() []uuid.UUID {
		t.Helper()
		rec := &pushRecorder{}
		require.NoError(t, db.InTx(bg, pool, func(tx pgx.Tx) error {
			return PlatformRouteSync{Routes: rec}.ServersChanged(bg, tx, ws.ID)
		}))
		return rec.servers
	}

	require.Empty(t, changed(), "no platform service, nothing to re-push")

	gone := insert("headscale", "headscale", alpha.ID)
	insert("web", "", beta.ID) // an ordinary service does not count
	require.Equal(t, []uuid.UUID{alpha.ID}, changed())

	_, err = pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE id = $1", gone.ID)
	require.NoError(t, err)
	require.Empty(t, changed(), "a deleted platform service no longer needs a push")
}
