package dns01

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/cfzone"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const autoToken = "cf-auto-canary-token-4711"

// autoEnv is a team with one server, one service on it and a Cloudflare
// connection whose zones the tests change.
type autoEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	box  *secretbox.Box
	ws   db.Workspace
	srv  db.Server
	svc  db.Service
}

func newAutoEnv(t *testing.T) *autoEnv {
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
	email := "ops@example.com"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha", AcmeEmail: &email})
	require.NoError(t, err)
	svc, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &srv.ID,
	})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	return &autoEnv{t: t, pool: pool, q: q, box: box, ws: ws, srv: srv, svc: svc}
}

// connect stores the team's Cloudflare connection with the given zones.
func (e *autoEnv) connect(zones ...cfzone.Zone) {
	e.t.Helper()
	enc, err := e.box.SealString(secretbox.CloudflareConnectionsToken, e.ws.ID.String(), autoToken)
	require.NoError(e.t, err)
	raw, err := json.Marshal(zones)
	require.NoError(e.t, err)
	_, err = e.q.UpsertCloudflareConnection(context.Background(), db.UpsertCloudflareConnectionParams{
		WorkspaceID: e.ws.ID, TokenEnc: enc, AccountID: "acc", AccountName: "Account", Zones: raw,
	})
	require.NoError(e.t, err)
}

func zone(id, name string) cfzone.Zone {
	return cfzone.Zone{ID: id, Name: name, Status: "active", AccountID: "acc"}
}

// sync records the state a DNS sync left a domain in (the evidence DNS-01 is chosen from).
func (e *autoEnv) sync(d db.Domain, zoneID, status, syncError string) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), `UPDATE domains SET cf_zone_id = $2, cf_status = $3, cf_error = $4 WHERE id = $1`, d.ID, zoneID, status, syncError)
	require.NoError(e.t, err)
}

// managed adds a domain whose record a sync wrote in zone z1.
func (e *autoEnv) managed(host string) db.Domain {
	e.t.Helper()
	d := e.domain(host, "", "")
	e.sync(d, "z1", "managed", "")
	return d
}

func (e *autoEnv) domain(host, tls, resolver string) db.Domain {
	e.t.Helper()
	d, err := e.q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: e.svc.ID, Hostname: host, TargetPort: 80, TlsMode: tls, DnsResolver: resolver})
	require.NoError(e.t, err)
	return d
}

func (e *autoEnv) server() db.Server {
	e.t.Helper()
	s, err := e.q.GetServerByID(context.Background(), e.srv.ID)
	require.NoError(e.t, err)
	return s
}

func (e *autoEnv) desired() Snapshot {
	e.t.Helper()
	s, err := Desired(context.Background(), e.q, e.srv.ID)
	require.NoError(e.t, err)
	return s
}

func ruleFor(d db.Domain) agentprotocol.DomainRule {
	return agentprotocol.DomainRule{ID: d.ID.String(), Host: d.Hostname, TLSMode: d.TlsMode, DNSResolver: d.DnsResolver}
}

func (e *autoEnv) apply(rules ...agentprotocol.DomainRule) []agentprotocol.DomainRule {
	e.t.Helper()
	routes := []agentprotocol.Route{{ServiceID: e.svc.ID.String(), Domains: rules}}
	require.NoError(e.t, ApplyAuto(context.Background(), e.q, e.srv.ID, routes))
	return routes[0].Domains
}

func (e *autoEnv) cert(d db.Domain) DomainCert {
	e.t.Helper()
	fresh, err := e.q.GetDomainByID(context.Background(), d.ID)
	require.NoError(e.t, err)
	certs, err := DomainCerts(context.Background(), e.q, []db.Domain{fresh})
	require.NoError(e.t, err)
	return certs[d.ID]
}

// applyDesired records what a successful Deploy proxy applies.
func (e *autoEnv) applyDesired() {
	e.t.Helper()
	var pc agentprotocol.ProxyConfig
	e.desired().Apply(&pc)
	require.NoError(e.t, e.q.SetServerAppliedDNS01(context.Background(), db.SetServerAppliedDNS01Params{ID: e.srv.ID, AppliedDns01: SnapshotOf(pc)}))
}

func TestAutoResolverOnlyForConnectedZones(t *testing.T) {
	e := newAutoEnv(t)
	inZone := e.managed("app.example.com")
	outside := e.domain("app.other.org", "", "")
	e.sync(outside, "", "not_in_zone", "")

	// No connection: nothing changes.
	require.Empty(t, e.desired().Resolvers)
	got := e.apply(ruleFor(inZone), ruleFor(outside))
	require.Empty(t, got[0].DNSResolver)

	e.connect(zone("z1", "example.com"))
	snap := e.desired()
	require.Len(t, snap.Resolvers, 1)
	r := snap.Resolvers[0]
	require.Equal(t, agentprotocol.DNSCloudflare, r.Provider)
	require.Equal(t, AutoResolverID(e.ws.ID), r.ID)
	require.Equal(t, []string{"CF_DNS_API_TOKEN"}, r.EnvNames)
	require.NotZero(t, r.Rev)
	require.NoError(t, agentprotocol.ValidateDNSResolver(r), "a resolver the agent accepts")
	var pc agentprotocol.ProxyConfig
	snap.Apply(&pc)
	require.NoError(t, agentprotocol.ValidateDNS01(pc, "ops@example.com"))

	got = e.apply(ruleFor(inZone), ruleFor(outside))
	require.Equal(t, agentprotocol.DNSCloudflare, got[0].DNSResolver, "a name in a connected zone uses DNS-01 through Cloudflare")
	require.Empty(t, got[1].DNSResolver, "a name outside every connected zone keeps HTTP-01")

	// A resolver a person set stays for names outside a connected zone.
	manual := ruleFor(outside)
	manual.DNSResolver = "route53"
	require.Equal(t, "route53", e.apply(manual)[0].DNSResolver)
}

func TestAutoResolverCredentialTravelsOnlyAsASecret(t *testing.T) {
	e := newAutoEnv(t)
	d := e.managed("app.example.com")
	e.connect(zone("z1", "example.com"))

	var pc agentprotocol.ProxyConfig
	e.desired().Apply(&pc)
	payload, err := json.Marshal(agentprotocol.ProxyConfigurePayload{Proxy: pc, ACMEEmail: "ops@example.com"})
	require.NoError(t, err)
	require.NotContains(t, string(payload), autoToken, "the token never enters a payload")
	rules, err := json.Marshal(e.apply(ruleFor(d)))
	require.NoError(t, err)
	require.NotContains(t, string(rules), autoToken)

	// Applied snapshot (what the server row stores) has names only.
	e.applyDesired()
	applied, err := json.Marshal(AppliedSnapshot(e.server()))
	require.NoError(t, err)
	require.NotContains(t, string(applied), autoToken)
	require.Contains(t, string(applied), AutoResolverID(e.ws.ID))

	// Secrets, produced for the outgoing frame only, carry the connection's token.
	secrets, err := Secrets(context.Background(), e.q, e.box, e.srv.ID, &pc)
	require.NoError(t, err)
	require.Equal(t, map[string]string{agentprotocol.DNSSecretKey(AutoResolverID(e.ws.ID), "CF_DNS_API_TOKEN"): autoToken}, secrets)

	// A rotated token reaches the next command and shows up as a changed Rev.
	before := e.desired().Resolvers[0].Rev
	enc, err := e.box.SealString(secretbox.CloudflareConnectionsToken, e.ws.ID.String(), "rotated-token-9876")
	require.NoError(t, err)
	raw, _ := json.Marshal([]cfzone.Zone{zone("z1", "example.com")})
	_, err = e.q.UpsertCloudflareConnection(context.Background(), db.UpsertCloudflareConnectionParams{WorkspaceID: e.ws.ID, TokenEnc: enc, AccountID: "acc", AccountName: "Account", Zones: raw})
	require.NoError(t, err)
	require.NotEqual(t, before, e.desired().Resolvers[0].Rev)
	secrets, err = Secrets(context.Background(), e.q, e.box, e.srv.ID, &pc)
	require.NoError(t, err)
	require.Equal(t, "rotated-token-9876", secrets[agentprotocol.DNSSecretKey(AutoResolverID(e.ws.ID), "CF_DNS_API_TOKEN")])

	// A resolver of another team is refused, and a removed connection adds
	// nothing (the agent keeps the value it has) instead of failing every push.
	other, err := e.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	foreign := agentprotocol.ProxyConfig{DNSResolvers: []agentprotocol.DNSResolver{{ID: AutoResolverID(other.ID), Provider: "cloudflare", EnvNames: []string{"CF_DNS_API_TOKEN"}}}}
	_, err = Secrets(context.Background(), e.q, e.box, e.srv.ID, &foreign)
	require.Error(t, err)
	_, err = e.q.DeleteCloudflareConnection(context.Background(), e.ws.ID)
	require.NoError(t, err)
	secrets, err = Secrets(context.Background(), e.q, e.box, e.srv.ID, &pc)
	require.NoError(t, err)
	require.Empty(t, secrets)
}

// DNS-01 is chosen from evidence NebulaCtrl has: a sync wrote the name's record
// with the connection's token. Nothing else shows the token may edit the zone.
func TestAutoDNS01NeedsASuccessfulDNSWriteInTheZone(t *testing.T) {
	e := newAutoEnv(t)
	d := e.domain("app.example.com", "", "")
	e.connect(zone("z1", "example.com"))
	const forbidden = "cloudflare API answered 403 (code 9109): Unauthorized to access requested resource"

	waiting := "Waiting for the first successful DNS sync in example.com"
	cases := map[string]struct {
		zoneID, status, syncError string
		fallback                  string // "" = DNS-01
	}{
		"never synced":                       {"", "", "", waiting},
		"synced into the zone, no status":    {"z1", "", "", waiting},
		"a foreign record is present":        {"z1", "present", "", waiting},
		"a foreign record conflicts":         {"z1", "conflict", "", waiting},
		"record missing":                     {"z1", "missing", "", waiting},
		"record points elsewhere":            {"z1", "wrong_target", "", waiting},
		"matched another zone before":        {"z9", "managed", "", waiting},
		"a sync wrote the record":            {"z1", "managed", "", ""},
		"the last write was refused (403)":   {"z1", "managed", forbidden, "Cloudflare refused the last DNS change"},
		"the last sync failed (unreachable)": {"z1", "managed", "Cloudflare could not be reached", "The last DNS sync with Cloudflare failed (Cloudflare could not be reached)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e.sync(d, c.zoneID, c.status, c.syncError)
			cert := e.cert(d)
			rules := e.apply(ruleFor(d))
			if c.fallback == "" {
				require.Equal(t, SourceDNS01, cert.Source)
				require.True(t, cert.Automatic)
				require.Empty(t, cert.Fallback)
				require.Equal(t, agentprotocol.DNSCloudflare, rules[0].DNSResolver)
				require.Len(t, e.desired().Resolvers, 1)
				return
			}
			require.Equal(t, SourceACME, cert.Source, "HTTP-01 until the evidence exists")
			require.False(t, cert.Automatic)
			require.Contains(t, cert.Fallback, c.fallback)
			require.Empty(t, rules[0].DNSResolver)
			require.Empty(t, e.desired().Resolvers, "no resolver for a server that serves no eligible name")
		})
	}

	t.Run("the permission failure text names the zone and the permission", func(t *testing.T) {
		e.sync(d, "z1", "managed", forbidden)
		require.Contains(t, e.cert(d).Fallback, "Zone, DNS: Edit on example.com")
	})

	t.Run("a later successful sync restores DNS-01", func(t *testing.T) {
		e.sync(d, "z1", "managed", forbidden)
		require.Equal(t, SourceACME, e.cert(d).Source)
		e.sync(d, "z1", "managed", "")
		require.Equal(t, SourceDNS01, e.cert(d).Source)
	})

	t.Run("names outside the token's zones, or in an inactive zone", func(t *testing.T) {
		e.sync(d, "z1", "managed", "")
		e.connect(zone("z2", "other.org"))
		c := e.cert(d)
		require.Equal(t, SourceACME, c.Source)
		require.Empty(t, c.Fallback, "a name outside every connected zone is simply not in one")
		e.connect(cfzone.Zone{ID: "z1", Name: "example.com", Status: "pending"})
		require.Contains(t, e.cert(d).Fallback, "not active")
		e.connect(zone("z1", "example.com"))
	})

	t.Run("the most specific zone is the name's", func(t *testing.T) {
		e.sync(d, "z3", "managed", "")
		e.connect(zone("z1", "example.com"), zone("z3", "app.example.com"))
		require.Equal(t, SourceDNS01, e.cert(d).Source)
		e.connect(zone("z1", "example.com"))
		e.sync(d, "z1", "managed", "")
	})

	t.Run("a rejected token", func(t *testing.T) {
		e.connect(zone("z1", "example.com"))
		require.NoError(t, e.q.SetCloudflareFailed(context.Background(), db.SetCloudflareFailedParams{WorkspaceID: e.ws.ID, StatusMessage: "rejected"}))
		require.Empty(t, e.desired().Resolvers)
		cert := e.cert(d)
		require.Equal(t, SourceACME, cert.Source)
		require.Contains(t, cert.Fallback, "rejected the connection's token")
	})

	t.Run("a server without an ACME email", func(t *testing.T) {
		e.connect(zone("z1", "example.com"))
		_, err := e.pool.Exec(context.Background(), `UPDATE servers SET acme_email = NULL WHERE id = $1`, e.srv.ID)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = e.pool.Exec(context.Background(), `UPDATE servers SET acme_email = 'ops@example.com' WHERE id = $1`, e.srv.ID)
		})
		require.Empty(t, e.desired().Resolvers, "ValidateDNS01 would refuse the deploy")
		require.Empty(t, e.apply(ruleFor(d))[0].DNSResolver)
		cert := e.cert(d)
		require.Equal(t, SourceACME, cert.Source)
		require.Contains(t, cert.Fallback, "no ACME email")
	})
}

func TestAutoSwitchOverKeepsHTTP01UntilTheResolverIsApplied(t *testing.T) {
	e := newAutoEnv(t)
	d := e.managed("app.example.com")
	e.connect(zone("z1", "example.com"))

	// Staged: the rule already names cloudflare, the proxy does not run it yet,
	// so the agent keeps the HTTP-01 resolver (agent test
	// TestAutoCloudflareUsesHTTP01UntilTheResolverIsApplied) and nothing is observed.
	require.Empty(t, AppliedSnapshot(e.server()).Resolvers, "nothing applied: the route table keeps the name on HTTP-01")
	c := e.cert(d)
	require.Equal(t, SourceDNS01, c.Source)
	require.True(t, c.Automatic)
	require.True(t, c.Staged)
	require.Equal(t, "unknown", c.Status, "no DNS-01 certificate is waited for while HTTP-01 still serves the name")
	require.Equal(t, agentprotocol.DNSCloudflare, c.Resolver)

	plan, err := PlanFor(context.Background(), e.q, e.server())
	require.NoError(t, err)
	require.True(t, plan.Restart, "a resolver is static configuration")
	require.Len(t, plan.Diff, 1)
	require.Equal(t, "dns01.resolver.cloudflare", plan.Diff[0].Field)
	require.Contains(t, plan.Diff[0].To, AutoCredentialName)
	require.NotEmpty(t, plan.Warnings)
	st := mustState(t, e)
	require.True(t, st.Staged)
	require.True(t, st.Resolvers[0].Automatic)
	require.Equal(t, ChangeAdd, st.Resolvers[0].Change)

	// Applied: the proxy runs the resolver, the status is observed from now on.
	e.applyDesired()
	c = e.cert(d)
	require.False(t, c.Staged)
	require.Equal(t, agentprotocol.AcmeStatusPending, c.Status)
	plan, err = PlanFor(context.Background(), e.q, e.server())
	require.NoError(t, err)
	require.Empty(t, plan.Diff)
	require.False(t, mustState(t, e).Staged)

	changed, err := Observe(context.Background(), e.q, e.srv.ID, []agentprotocol.AcmeCertStatus{{
		Kind: agentprotocol.AcmeKindDomain, ID: d.ID.String(), Status: agentprotocol.AcmeStatusActive, Issuer: "R11", IssuedVia: agentprotocol.AcmeIssuedDNS01,
	}})
	require.NoError(t, err)
	require.True(t, changed)
	c = e.cert(d)
	require.Equal(t, agentprotocol.AcmeStatusActive, c.Status)
	require.Equal(t, "R11", c.Issuer)
	require.Equal(t, agentprotocol.AcmeIssuedDNS01, c.IssuedVia)

	// The last sync is refused: the domain falls back to HTTP-01 (its certificate
	// stays in the proxy's store) and says why.
	e.sync(d, "z1", "managed", "cloudflare API answered 403 (code 9109): no")
	c = e.cert(d)
	require.Equal(t, SourceACME, c.Source)
	require.Contains(t, c.Fallback, "Cloudflare refused the last DNS change")
	plan, err = PlanFor(context.Background(), e.q, e.server())
	require.NoError(t, err)
	require.Len(t, plan.Diff, 1)
	require.Equal(t, "removed", plan.Diff[0].To)
}

func mustState(t *testing.T, e *autoEnv) ServerDNS01 {
	t.Helper()
	svc := NewService(e.pool, nil, e.box)
	st, err := svc.state(context.Background(), e.q, e.server(), time.Now())
	require.NoError(t, err)
	return st
}

// Which storage holds a certificate is what the agent reads from the proxy's
// files; the control plane keeps it per domain and says it as it is: an automatic
// name whose certificate came from HTTP-01 is not labelled DNS-01.
func TestIssuedViaIsObservedPersistedAndExposed(t *testing.T) {
	e := newAutoEnv(t)
	ctx := context.Background()
	d := e.managed("app.example.com")
	e.connect(zone("z1", "example.com"))
	id := d.ID.String()
	observe := func(st agentprotocol.AcmeCertStatus) bool {
		t.Helper()
		st.Kind, st.ID = agentprotocol.AcmeKindDomain, id
		changed, err := Observe(ctx, e.q, e.srv.ID, []agentprotocol.AcmeCertStatus{st})
		require.NoError(t, err)
		return changed
	}

	require.Empty(t, e.cert(d).IssuedVia, "nothing observed yet")

	// Staged: the proxy runs no Cloudflare resolver, the host is served with the
	// HTTP-01 certificate it holds. The agent reports the storage only.
	require.True(t, observe(agentprotocol.AcmeCertStatus{IssuedVia: agentprotocol.AcmeIssuedHTTP01}))
	c := e.cert(d)
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, c.IssuedVia)
	require.True(t, c.Automatic)
	require.True(t, c.Staged)
	require.Equal(t, "unknown", c.Status, "a store-only report carries no status")
	require.Contains(t, c.Message, "issued over HTTP-01 and is renewed there")
	require.False(t, observe(agentprotocol.AcmeCertStatus{IssuedVia: agentprotocol.AcmeIssuedHTTP01}), "an unchanged reading is not written again")

	// Applied, observed active: the certificate is still the HTTP-01 one.
	e.applyDesired()
	require.True(t, observe(agentprotocol.AcmeCertStatus{Status: agentprotocol.AcmeStatusActive, Issuer: "R11", IssuedVia: agentprotocol.AcmeIssuedHTTP01}))
	c = e.cert(d)
	require.Equal(t, agentprotocol.AcmeStatusActive, c.Status)
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, c.IssuedVia, "automatic, but the certificate renews over HTTP-01")
	require.True(t, c.Automatic)

	// A certificate the DNS-01 resolver issued.
	require.True(t, observe(agentprotocol.AcmeCertStatus{Status: agentprotocol.AcmeStatusActive, Issuer: "R11", IssuedVia: agentprotocol.AcmeIssuedDNS01}))
	c = e.cert(d)
	require.Equal(t, agentprotocol.AcmeIssuedDNS01, c.IssuedVia)
	require.NotContains(t, c.Message, "HTTP-01")

	// No storage holds one any more: forgotten.
	require.True(t, observe(agentprotocol.AcmeCertStatus{Status: agentprotocol.AcmeStatusPending}))
	require.Empty(t, e.cert(d).IssuedVia)

	// Other servers' and unknown ids are ignored; so are values the control plane does not know.
	other, err := e.q.CreateServer(ctx, db.CreateServerParams{WorkspaceID: e.ws.ID, Name: "other"})
	require.NoError(t, err)
	changed, err := Observe(ctx, e.q, other.ID, []agentprotocol.AcmeCertStatus{
		{Kind: agentprotocol.AcmeKindDomain, ID: id, IssuedVia: agentprotocol.AcmeIssuedDNS01},
		{Kind: agentprotocol.AcmeKindDomain, ID: uuid.NewString(), IssuedVia: agentprotocol.AcmeIssuedDNS01},
		{Kind: agentprotocol.AcmeKindDomain, ID: id, IssuedVia: "tls-alpn-01"},
		{Kind: agentprotocol.AcmeKindWildcard, ID: id},
	})
	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, e.cert(d).IssuedVia)

	// An HTTP-01 name shows its storage too, and the row goes with the domain.
	plain := e.domain("plain.other.org", "", "")
	_, err = Observe(ctx, e.q, e.srv.ID, []agentprotocol.AcmeCertStatus{{Kind: agentprotocol.AcmeKindDomain, ID: plain.ID.String(), IssuedVia: agentprotocol.AcmeIssuedHTTP01}})
	require.NoError(t, err)
	pc := e.cert(plain)
	require.Equal(t, SourceACME, pc.Source)
	require.Equal(t, agentprotocol.AcmeIssuedHTTP01, pc.IssuedVia)
	_, err = e.pool.Exec(ctx, `DELETE FROM domains WHERE id = $1`, plain.ID)
	require.NoError(t, err)
	var n int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM domain_cert_stores WHERE domain_id = $1`, plain.ID).Scan(&n))
	require.Zero(t, n)
}

func TestAutoProxiedAndDNSOnlyNamesAreTheSame(t *testing.T) {
	e := newAutoEnv(t)
	dnsOnly := e.managed("dnsonly.example.com")
	proxied := e.managed("proxied.example.com")
	// The proxy setting of a name (the orange cloud) is the Cloudflare DNS record's
	// own and plays no part in the certificate.
	_, err := e.pool.Exec(context.Background(), `UPDATE domains SET cf_proxied = true WHERE id = $1`, proxied.ID)
	require.NoError(t, err)
	_, err = e.pool.Exec(context.Background(), `UPDATE domains SET cf_proxied = false WHERE id = $1`, dnsOnly.ID)
	require.NoError(t, err)
	e.connect(zone("z1", "example.com"))
	got := e.apply(ruleFor(dnsOnly), ruleFor(proxied))
	require.Equal(t, agentprotocol.DNSCloudflare, got[0].DNSResolver)
	require.Equal(t, agentprotocol.DNSCloudflare, got[1].DNSResolver)
	e.applyDesired()
	for _, d := range []db.Domain{dnsOnly, proxied} {
		c := e.cert(d)
		require.Equal(t, SourceDNS01, c.Source, d.Hostname)
		require.True(t, c.Automatic, d.Hostname)
	}
}

func TestAutoWildcardHostAndWildcardCertificate(t *testing.T) {
	e := newAutoEnv(t)
	e.connect(zone("z1", "example.com"))
	wild := e.managed("*.preview.example.com")
	require.Equal(t, agentprotocol.DNSCloudflare, e.apply(ruleFor(wild))[0].DNSResolver, "a wildcard host in a connected zone uses the same resolver")
	require.Len(t, e.desired().Resolvers, 1)

	// Before its record is synced a wildcard name is still accepted: HTTP-01 cannot
	// issue it, so it waits for DNS-01 instead of needing a resolver set by hand.
	ok, err := InConnectedZone(context.Background(), e.q, e.server(), "*.fresh.example.com")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = InConnectedZone(context.Background(), e.q, e.server(), "*.fresh.other.org")
	require.NoError(t, err)
	require.False(t, ok)

	// A wildcard certificate of the server covers its names: no resolver is
	// added for them, and the domain reports the wildcard.
	_, err = e.pool.Exec(context.Background(), `DELETE FROM domains WHERE id = $1`, wild.ID)
	require.NoError(t, err)
	covered := e.managed("api.wc.example.com")
	_, err = e.q.InsertWildcardCert(context.Background(), db.InsertWildcardCertParams{WorkspaceID: e.ws.ID, ServerID: e.srv.ID, Domain: "wc.example.com", Provider: "cloudflare"})
	require.NoError(t, err)
	require.Empty(t, e.desired().Resolvers)
	require.Equal(t, SourceWildcard, e.cert(covered).Source)
}

func TestAutoTunnelDomainsKeepTheirBehaviour(t *testing.T) {
	e := newAutoEnv(t)
	d := e.managed("app.example.com")
	e.connect(zone("z1", "example.com"))
	require.Len(t, e.desired().Resolvers, 1)

	_, err := e.pool.Exec(context.Background(), `INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name, state, activated_at)
		VALUES ($1, $2, 'acc', 't1', 'tunnel', 'online', now())`, e.srv.ID, e.ws.ID)
	require.NoError(t, err)
	require.Empty(t, e.desired().Resolvers, "no certificate is requested at the origin of a tunnel")
	require.Empty(t, e.apply(ruleFor(d))[0].DNSResolver)
	flagged := ruleFor(d)
	flagged.Tunnel = true
	require.Empty(t, e.apply(flagged)[0].DNSResolver)
	c := e.cert(d)
	require.Equal(t, SourceACME, c.Source)
	require.Empty(t, c.Fallback, "a tunnel name is not a fallback")
}

func TestAutoStatusPageHostnames(t *testing.T) {
	e := newAutoEnv(t)
	ctx := context.Background()
	_, err := e.pool.Exec(ctx, `UPDATE servers SET is_control_plane = true WHERE id = $1`, e.srv.ID)
	require.NoError(t, err)
	other, err := e.q.CreateWorkspace(ctx, "other")
	require.NoError(t, err)
	page := func(ws uuid.UUID, slug, host string, published bool, status string) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, e.pool.QueryRow(ctx,
			`INSERT INTO status_pages (workspace_id, title, slug, hostname, published, cf_zone_id, cf_status) VALUES ($1, $2, $2, $3, $4, 'z1', $5) RETURNING id`,
			ws, slug, host, published, status).Scan(&id))
		return id
	}
	mine := page(e.ws.ID, "mine", "status.example.com", true, "managed")
	unsynced := page(e.ws.ID, "unsynced", "new.example.com", true, "")
	foreignRecord := page(e.ws.ID, "present", "taken.example.com", true, "present")
	draft := page(e.ws.ID, "draft", "draft.example.com", false, "managed")
	elsewhere := page(e.ws.ID, "elsewhere", "status.other.org", true, "managed")
	foreign := page(other.ID, "foreign", "foreign.example.com", true, "managed")

	require.Empty(t, mustAutoPages(t, e), "no connection yet")
	e.connect(zone("z1", "example.com"))
	got := mustAutoPages(t, e)
	require.True(t, got[mine], "a published page whose record a sync wrote")
	require.False(t, got[unsynced], "waiting for the first successful DNS sync")
	require.False(t, got[foreignRecord], "a record somebody else created proves nothing")
	require.False(t, got[draft])
	require.False(t, got[elsewhere], "outside every connected zone")
	require.False(t, got[foreign], "another team's page is not issued with this team's token")
	require.Len(t, e.desired().Resolvers, 1, "the control-plane server gets the resolver for its pages")

	_, err = e.pool.Exec(ctx, `UPDATE status_pages SET cf_error = 'cloudflare API answered 403 (code 1): no' WHERE id = $1`, mine)
	require.NoError(t, err)
	require.Empty(t, mustAutoPages(t, e), "the last write was refused")
	require.Empty(t, e.desired().Resolvers)
	_, err = e.pool.Exec(ctx, `UPDATE status_pages SET cf_error = '' WHERE id = $1`, mine)
	require.NoError(t, err)

	// Only the control-plane server serves status pages.
	_, err = e.pool.Exec(ctx, `UPDATE servers SET is_control_plane = false WHERE id = $1`, e.srv.ID)
	require.NoError(t, err)
	require.Empty(t, mustAutoPages(t, e))
	require.Empty(t, e.desired().Resolvers)
}

func mustAutoPages(t *testing.T, e *autoEnv) map[uuid.UUID]bool {
	t.Helper()
	got, err := AutoStatusPages(context.Background(), e.q, e.srv.ID)
	require.NoError(t, err)
	return got
}

func TestAutoResolverYieldsToAStagedCloudflareResolver(t *testing.T) {
	e := newAutoEnv(t)
	d := e.managed("app.example.com")
	e.connect(zone("z1", "example.com"))
	svc := NewService(e.pool, nil, e.box)
	admin := principal(e.ws.ID, policy.AllScopes)
	cred, err := svc.CreateCredential(admin, CredentialInput{Name: "mine", Provider: "cloudflare", Values: map[string]string{"CF_DNS_API_TOKEN": "manual-token"}})
	require.NoError(t, err)
	_, err = svc.SetResolver(admin, e.srv.ID, "cloudflare", ResolverInput{CredentialID: cred.ID})
	require.NoError(t, err)

	snap := e.desired()
	require.Len(t, snap.Resolvers, 1, "one resolver per provider")
	require.Equal(t, cred.ID.String(), snap.Resolvers[0].ID, "the person's resolver wins")
	require.Equal(t, agentprotocol.DNSCloudflare, e.apply(ruleFor(d))[0].DNSResolver)
}
