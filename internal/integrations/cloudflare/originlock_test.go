package cloudflare

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// pushRecorder stands in for the route push of the proxy server service.
type pushRecorder struct {
	mu      sync.Mutex
	servers []uuid.UUID
}

func (p *pushRecorder) PushRoutes(_ context.Context, _ pgx.Tx, id uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.servers = append(p.servers, id)
	return nil
}

func (p *pushRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.servers)
}

func (p *pushRecorder) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.servers = nil
}

// deploy gives the test service a current deployment on server A, so its
// domains are served there.
func (e *env) deploy(t *testing.T) {
	t.Helper()
	bg := context.Background()
	snap, _ := json.Marshal(map[string]any{"config": map[string]any{"image": "nginx:1", "port": 80}})
	src, _ := json.Marshal(map[string]any{"type": "image", "image": "nginx:1"})
	rel, err := e.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: e.service.ID, Source: src, Config: snap, ImageRef: "nginx:1"})
	require.NoError(t, err)
	dep, err := e.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: e.service.ID, ReleaseID: &rel.ID, ServerID: &e.srvA.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(t, err)
	e.exec(t, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(t, e.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: e.service.ID, CurrentDeploymentID: &dep.ID}))
}

// lockEnv is an environment with a connection, a deployed service, a push
// recorder and one proxied domain whose record has been synced.
func lockEnv(t *testing.T) (*env, *pushRecorder, db.Domain) {
	t.Helper()
	e := newEnv(t)
	rec := &pushRecorder{}
	e.svc.SetRoutePusher(rec)
	e.connect(t)
	e.deploy(t)
	d := e.domain(t, "app.example.com", "")
	on := true
	_, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	dom := e.dom(t, d.ID)
	return e, rec, dom
}

func TestOriginPullCAIsPinned(t *testing.T) {
	require.NoError(t, checkOriginPullCA(originPullCAPEM))
	require.NotEmpty(t, OriginPullCA())

	// A different certificate, or a mangled one, never passes.
	require.Error(t, checkOriginPullCA("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"))
	require.Error(t, checkOriginPullCA("not a pem"))

	// The pinned CA is Cloudflare's origin-pull CA and has years left; refresh the
	// file (see OriginPullCAFingerprint) before it is within 180 days of expiry.
	block, _ := pem.Decode([]byte(originPullCAPEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Equal(t, "origin-pull.cloudflare.net", cert.Subject.CommonName)
	require.True(t, cert.IsCA)
	require.True(t, time.Until(cert.NotAfter) > 180*24*time.Hour, "the pinned origin-pull CA expires on %s: refresh it", cert.NotAfter)
}

func TestLockPolicyMatrix(t *testing.T) {
	cases := []struct {
		name                   string
		proxied, tunnel, optIn bool
		locked                 bool
		reason                 string
	}{
		{"proxied and opted in", true, false, true, true, LockReasonNone},
		{"proxied, opted out", true, false, false, false, LockReasonOptedOut},
		{"DNS only", false, false, true, false, LockReasonNotProxied},
		{"DNS only, opted out", false, false, false, false, LockReasonNotProxied},
		{"tunnel, proxied flag on", true, true, true, false, LockReasonTunnel},
		{"tunnel, proxied flag off", false, true, true, false, LockReasonTunnel},
		{"tunnel, opted out", true, true, false, false, LockReasonTunnel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			locked, reason := LockPolicy(c.proxied, c.tunnel, c.optIn)
			require.Equal(t, c.locked, locked)
			require.Equal(t, c.reason, reason)
		})
	}
}

func TestProxiedDomainIsLockedWithAOPAfterTheRecordIsProxied(t *testing.T) {
	e, rec, d := lockEnv(t)
	require.True(t, d.CfOriginLock, "every domain starts opted in")

	// The record is proxied, Authenticated Origin Pulls was switched on in the zone
	// (PATCH of the tls_client_auth setting) and the proxy was told.
	require.True(t, e.fake.records("app.example.com")[0].Proxied)
	require.Equal(t, "on", e.fake.aop["z1"])
	require.Contains(t, e.fake.writeLog(), "PATCH /zones/z1/settings/tls_client_auth on")
	require.Len(t, e.audits(t, "cloudflare.zone.aop.enable"), 1, "the change in the customer's account is audited")
	require.Positive(t, rec.count())
	for _, id := range rec.servers {
		require.Equal(t, e.srvA.ID, id)
	}

	st, err := e.svc.Status(e.dev, d.ID)
	require.NoError(t, err)
	ol := st.OriginLock
	require.True(t, ol.Enabled)
	require.True(t, ol.OptIn)
	require.True(t, ol.IPLock)
	require.True(t, ol.RealClientIP)
	require.Equal(t, AOPOn, ol.AOP)
	require.Empty(t, ol.AOPReason)
	require.Empty(t, ol.Reason)
	require.Equal(t, IPSourceBuiltin, ol.IPListSource)

	// A second sync does not ask Cloudflare to change anything again.
	e.fake.resetWrites()
	rec.reset()
	require.NoError(t, e.svc.SyncDomain(context.Background(), d.ID))
	require.Empty(t, e.fake.writeLog())
	require.Zero(t, rec.count(), "nothing changed, nothing is pushed")

	// If somebody turns AOP off in Cloudflare, the next check (after the trust
	// interval) turns it on again.
	e.fake.aop["z1"] = "off"
	e.exec(t, `UPDATE cloudflare_zone_aop SET checked_at = now() - interval '1 hour'`)
	require.NoError(t, e.svc.SyncDomain(context.Background(), d.ID))
	require.Equal(t, "on", e.fake.aop["z1"])
}

func TestAOPStateIsRecordedPerZoneAndIPLockStillApplies(t *testing.T) {
	t.Run("token without the permission", func(t *testing.T) {
		e := newEnv(t)
		rec := &pushRecorder{}
		e.svc.SetRoutePusher(rec)
		e.connect(t)
		e.deploy(t)
		e.fake.aopDenied = true
		d := e.domain(t, "app.example.com", "")
		on := true
		_, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
		require.NoError(t, err)
		require.Empty(t, e.fake.aop["z1"], "nothing was changed")

		st, err := e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.True(t, st.OriginLock.Enabled, "IP lock still applies")
		require.True(t, st.OriginLock.IPLock)
		require.Equal(t, AOPUnavailable, st.OriginLock.AOP)
		require.Equal(t, AOPReasonPermission, st.OriginLock.AOPReasonCode)
		require.Contains(t, st.OriginLock.AOPReason, "Zone Settings")
		require.Equal(t, AOPPermissionHint, st.OriginLock.PermissionHint)

		row, err := e.q.GetZoneAOP(context.Background(), db.GetZoneAOPParams{WorkspaceID: e.ws, ZoneID: "z1"})
		require.NoError(t, err)
		require.Equal(t, AOPUnavailable, row.State)
		require.Equal(t, AOPReasonPermission, row.ReasonCode)

		// Granting the permission is picked up by the next sync: AOP turns on and
		// the proxy is told to require the certificate.
		e.fake.aopDenied = false
		rec.reset()
		require.NoError(t, e.svc.SyncDomain(context.Background(), d.ID))
		require.Equal(t, "on", e.fake.aop["z1"])
		require.Positive(t, rec.count())
		st, err = e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.Equal(t, AOPOn, st.OriginLock.AOP)
	})

	t.Run("flexible SSL", func(t *testing.T) {
		e := newEnv(t)
		e.connect(t)
		e.deploy(t)
		e.fake.ssl["z1"] = "flexible"
		d := e.domain(t, "app.example.com", "")
		on := true
		_, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
		require.NoError(t, err)
		require.Empty(t, e.fake.aop["z1"], "AOP cannot work in Flexible mode and is left alone")
		for _, w := range e.fake.writeLog() {
			require.NotContains(t, w, "tls_client_auth")
		}

		st, err := e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.True(t, st.OriginLock.Enabled)
		require.Equal(t, AOPUnavailable, st.OriginLock.AOP)
		require.Equal(t, AOPReasonSSLMode, st.OriginLock.AOPReasonCode)
		require.Contains(t, st.OriginLock.AOPReason, "Flexible")
		require.NotEmpty(t, st.Warnings, "the existing Flexible warning stays")

		// Fixing the mode shows at once (the stored verdict is stale) and the next sync enables AOP.
		e.fake.ssl["z1"] = "strict"
		st, err = e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.Equal(t, AOPOff, st.OriginLock.AOP)
		require.Empty(t, st.OriginLock.AOPReason)
		require.NoError(t, e.svc.SyncDomain(context.Background(), d.ID))
		st, err = e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.Equal(t, AOPOn, st.OriginLock.AOP)
		require.Equal(t, "on", e.fake.aop["z1"])
	})

	t.Run("SSL mode unreadable", func(t *testing.T) {
		e := newEnv(t)
		e.connect(t)
		e.deploy(t)
		e.fake.sslDenied = true
		d := e.domain(t, "app.example.com", "")
		on := true
		_, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
		require.NoError(t, err)
		st, err := e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.Equal(t, AOPUnavailable, st.OriginLock.AOP)
		require.Equal(t, AOPReasonPermission, st.OriginLock.AOPReasonCode)
	})
}

func TestOptOutTunnelAndDNSOnlyAreNotLocked(t *testing.T) {
	e, rec, d := lockEnv(t)
	off, on := false, true
	bg := context.Background()

	// Opting out is audited, pushed to the proxy and explained.
	rec.reset()
	st, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{OriginLock: &off})
	require.NoError(t, err)
	require.False(t, st.OriginLock.Enabled)
	require.False(t, st.OriginLock.OptIn)
	require.Equal(t, LockReasonOptedOut, st.OriginLock.Reason)
	require.Equal(t, AOPOff, st.OriginLock.AOP)
	require.False(t, st.OriginLock.IPLock)
	require.False(t, st.OriginLock.RealClientIP)
	require.Positive(t, rec.count(), "the unlock is pushed")
	var optedOut bool
	for _, a := range e.audits(t, "domain.dns.update") {
		var md map[string]any
		require.NoError(t, json.Unmarshal([]byte(a.Meta), &md))
		if v, ok := md["originLock"]; ok && v == false {
			optedOut = true
		}
	}
	require.True(t, optedOut, "the opt-out is in the audit trail")
	rows, err := e.q.ListOriginLockDomains(bg, []uuid.UUID{d.ID})
	require.NoError(t, err)
	require.Empty(t, rows)

	// Opting back in locks again.
	st, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{OriginLock: &on})
	require.NoError(t, err)
	require.True(t, st.OriginLock.Enabled)

	// DNS only: no lock.
	st, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, st.OriginLock.Enabled)
	require.Equal(t, LockReasonNotProxied, st.OriginLock.Reason)
	require.True(t, st.OriginLock.OptIn)

	// Tunnel: no public origin, nothing to lock, whatever the toggles say.
	e.tunnels.set(e.srvA.ID, "t9.cfargotunnel.com")
	st, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	require.True(t, st.Tunnel)
	require.False(t, st.OriginLock.Enabled)
	require.Equal(t, LockReasonTunnel, st.OriginLock.Reason)

	// Only writers change it.
	_, err = e.svc.SetSettings(principal(e.ws, []string{policy.ScopeProjectsRead}), d.ID, SettingsInput{OriginLock: &off})
	require.Equal(t, "forbidden", code(t, err))
}

func TestAForeignRecordIsNeverLocked(t *testing.T) {
	e, _, _ := lockEnv(t)
	// A record NebulaCtrl did not create stays as it is until it is replaced:
	// the name is not locked on a guess about it.
	e.fake.put(Record{Type: "A", Name: "foreign.example.com", Content: "198.51.100.99", Proxied: true})
	f := e.domain(t, "foreign.example.com", "")
	require.True(t, f.CfProxied)
	require.NoError(t, e.svc.SyncDomain(context.Background(), f.ID))
	st, err := e.svc.Status(e.dev, f.ID)
	require.NoError(t, err)
	require.Equal(t, StatusConflict, st.Status)
	require.False(t, st.OriginLock.Enabled)
	require.Equal(t, LockReasonPending, st.OriginLock.Reason)
}

func TestApplyOriginLockSkipsWhatTheAgentCannotLock(t *testing.T) {
	e, _, d := lockEnv(t)
	bg := context.Background()
	host := d.Hostname

	rules := func(extra ...agentprotocol.DomainRule) []agentprotocol.Route {
		base := agentprotocol.DomainRule{ID: d.ID.String(), Host: host}
		return []agentprotocol.Route{{ServiceID: "svc", Hosts: []string{host}, RouteExt: agentprotocol.RouteExt{Domains: append([]agentprotocol.DomainRule{base}, extra...)}}}
	}

	// The plain case: locked, with the AOP CA because the zone has AOP on.
	routes := rules()
	require.NoError(t, ApplyOriginLock(bg, e.q, e.srvA.ID, routes))
	require.True(t, routes[0].Domains[0].OriginLocked)
	require.True(t, routes[0].Domains[0].RequireClientCert)
	require.NotNil(t, routes[0].OriginLock)
	require.Equal(t, OriginPullCA(), routes[0].OriginLock.ClientCAPEM)
	require.Equal(t, Builtin().CIDRs(), routes[0].OriginLock.CloudflareCIDRs)

	// A tunnel flag, a redirect rule or a wildcard host of the same name poisons the host.
	routes = rules()
	routes[0].Domains[0].Tunnel = true
	require.NoError(t, ApplyOriginLock(bg, e.q, e.srvA.ID, routes))
	require.False(t, routes[0].Domains[0].OriginLocked)
	require.Nil(t, routes[0].OriginLock)

	routes = rules(agentprotocol.DomainRule{ID: uuid.NewString(), Host: host, Kind: agentprotocol.DomainRedirect, RedirectTo: "x.example.com"})
	require.NoError(t, ApplyOriginLock(bg, e.q, e.srvA.ID, routes))
	require.False(t, routes[0].Domains[0].OriginLocked, "the agent needs the rules of one host to agree")
	require.Nil(t, routes[0].OriginLock)

	routes = []agentprotocol.Route{{ServiceID: "svc", Hosts: []string{"*.example.com"}, RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{ID: d.ID.String(), Host: "*.example.com"}}}}}
	require.NoError(t, ApplyOriginLock(bg, e.q, e.srvA.ID, routes))
	require.False(t, routes[0].Domains[0].OriginLocked)

	// L4 routes are never touched.
	l4 := []agentprotocol.Route{{ServiceID: "svc", RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, Domains: []agentprotocol.DomainRule{{ID: d.ID.String(), Host: host}}}}}
	require.NoError(t, ApplyOriginLock(bg, e.q, e.srvA.ID, l4))
	require.False(t, l4[0].Domains[0].OriginLocked)

	// A tunnel on the server excludes the domain even when the rule is not flagged.
	e.tunnels.set(e.srvA.ID, "t9.cfargotunnel.com") // (the fake lookup is the service's; the query below is the builder's)
	e.exec(t, `INSERT INTO server_tunnels (server_id, workspace_id, account_id, tunnel_id, tunnel_name, state, activated_at) VALUES ($1, $2, 'acc1', 'tun', 'n', 'online', now())`, e.srvA.ID, e.ws)
	routes = rules()
	require.NoError(t, ApplyOriginLock(bg, e.q, e.srvA.ID, routes))
	require.False(t, routes[0].Domains[0].OriginLocked)

	// The deploy payload variant gives the same answer for the same rules.
	e.exec(t, `DELETE FROM server_tunnels`)
	pr := []agentprotocol.DomainRule{{ID: d.ID.String(), Host: host}}
	ol, err := ApplyOriginLockToRules(bg, e.q, e.srvA.ID, pr)
	require.NoError(t, err)
	require.NotNil(t, ol)
	require.True(t, pr[0].OriginLocked)
	require.True(t, pr[0].RequireClientCert)
	require.NotEmpty(t, ol.ClientCAPEM)
	none, err := ApplyOriginLockToRules(bg, e.q, e.srvA.ID, []agentprotocol.DomainRule{{ID: uuid.NewString(), Host: "other.example.net"}})
	require.NoError(t, err)
	require.Nil(t, none)
}
