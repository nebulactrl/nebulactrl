package proxyserver

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) plan(reprovision bool) (ProxyPlan, error) {
	f.t.Helper()
	return f.svc.PlanDeploy(f.admin, f.srv.ID, reprovision, nil)
}

func TestGenerationOfKey(t *testing.T) {
	id := uuid.NewString()
	for key, want := range map[string]struct {
		gen int64
		ok  bool
	}{
		"proxy_configure:" + id + ":3:" + uuid.NewString(): {3, true},
		"proxy_configure:" + id + ":0:x":                   {0, true},
		"proxy_configure:" + id + ":x":                     {0, false},
		"sync_routes:" + id + ":3:x":                       {0, false},
		"proxy_configure:" + id + ":three:x":               {0, false},
		"":                                                 {0, false},
	} {
		gen, ok := generationOfKey(key)
		require.Equal(t, want.ok, ok, key)
		require.Equal(t, want.gen, gen, key)
	}
}

func TestDefaultTailnetHostname(t *testing.T) {
	require.Equal(t, "nebula-edge-1", DefaultTailnetHostname("Edge 1"))
	require.Equal(t, "nebula-server", DefaultTailnetHostname("!!!"), "a name without letters or digits still gives a valid machine name")
	require.Equal(t, "nebula-server", DefaultTailnetHostname(""))
	long := DefaultTailnetHostname(strings.Repeat("a", 40) + " " + strings.Repeat("b", 40))
	require.LessOrEqual(t, len(long), 63)
	require.True(t, ValidTailnetHostname(long), long)
	require.False(t, strings.HasSuffix(long, "-"))
}

func TestResultMessageReadsTheAgentsMessage(t *testing.T) {
	raw := func(v any) []byte {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return b
	}
	inner := raw(agentprotocol.ProxyConfigureResult{Outcome: "failed", Message: "port 443 in use"})
	require.Equal(t, "port 443 in use", resultMessage(raw(agentprotocol.ResultFrame{Data: inner})))
	require.Empty(t, resultMessage(nil))
	require.Empty(t, resultMessage([]byte("not json")))
	require.Empty(t, resultMessage(raw(agentprotocol.ResultFrame{})), "no data")
	require.Empty(t, resultMessage(raw(agentprotocol.ResultFrame{Data: []byte(`"x"`)})), "unreadable data")
}

func TestPlanRefusesAManagedTailnetWithoutTheIntegration(t *testing.T) {
	f := newFixture(t)
	f.exec(`UPDATE servers SET tailnet_mode = 'managed' WHERE id = $1`, f.srv.ID)
	_, err := f.plan(false)
	require.Equal(t, "tailnet_not_configured", code(t, err))

	_, err = f.plan(true)
	require.Equal(t, "tailnet_not_configured", code(t, err))

	f.exec(`UPDATE servers SET tailnet_mode = 'off' WHERE id = $1`, f.srv.ID)
	_, err = f.plan(true)
	require.Equal(t, "reprovision_not_applicable", code(t, err))
}

func TestPlanHostTailnetNeedsTailscaleOnTheHost(t *testing.T) {
	f := newFixture(t)
	f.exec(`UPDATE servers SET tailnet_mode = 'host' WHERE id = $1`, f.srv.ID)
	_, err := f.plan(false)
	require.Equal(t, "host_tailscale_not_detected", code(t, err))

	f.setFacts(agentprotocol.Facts{Capabilities: []string{agentprotocol.CapProxyV2}, HostTailscale: &agentprotocol.HostTailscale{Detected: false}})
	_, err = f.plan(false)
	require.Equal(t, "host_tailscale_not_detected", code(t, err))

	f.setFacts(agentprotocol.Facts{Capabilities: []string{agentprotocol.CapProxyV2}, HostTailscale: &agentprotocol.HostTailscale{Detected: true}})
	p, err := f.plan(false)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), "Host mode relies on the Tailscale running on the host itself")
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "tailnet.mode", From: "off", To: "host"})
}

func TestPlanListsEachStagedChange(t *testing.T) {
	f := newFixture(t)
	f.configureTailscale()
	yes := true

	// Upstream policy toggles are separate lines; only the host gateway one recreates the proxy.
	_, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{AllowLoopback: &yes, AllowLinkLocal: &yes})
	require.NoError(t, err)
	p, err := f.plan(false)
	require.NoError(t, err)
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "upstreamPolicy.allowLoopback", From: "off", To: "on"})
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "upstreamPolicy.allowLinkLocal", From: "off", To: "on"})
	require.False(t, p.RestartRequired)

	// A certificate that was installed and is now being removed.
	c, _ := f.upload("site", "site.example.com")
	f.exec(`UPDATE certificates SET applied_at = now() WHERE id = $1`, c.ID)
	require.NoError(t, f.svc.DeleteCertificate(f.admin, c.ID))
	p, err = f.plan(false)
	require.NoError(t, err)
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "certificate.site", From: "installed", To: "removed"})

	// A staged certificate that was never applied is announced with its names.
	f.upload("fresh", "fresh.example.com", "www.fresh.example.com")
	p, err = f.plan(false)
	require.NoError(t, err)
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "certificate.fresh", From: "-", To: "installed (fresh.example.com, www.fresh.example.com)"})
}

func TestPlanWarnsAboutTheManagedNodeBeingReplacedOrRemoved(t *testing.T) {
	f := newFixture(t)
	f.configureTailscale()
	f.exec(`UPDATE servers SET tailnet_mode = 'managed', tailnet_applied_mode = 'managed', tailnet_applied_hostname = 'old-edge', tailnet_hostname = 'new-edge' WHERE id = $1`, f.srv.ID)

	p, err := f.plan(false)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), `Changing the hostname re-provisions the node: the old device is deleted from your tailnet and "new-edge"`)
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "tailnet.hostname", From: "old-edge", To: "new-edge"})

	f.exec(`UPDATE servers SET tailnet_hostname = 'old-edge' WHERE id = $1`, f.srv.ID)
	p, err = f.plan(true)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), `Re-provisioning deletes the current device and registers "old-edge" again`)
	require.Contains(t, p.Diff, ProxyPlanChange{Field: "tailnet.provision", From: "-", To: "re-provision requested"})

	// MagicDNS recreates the proxy, and with it the Tailscale container that shares its namespace.
	f.exec(`UPDATE servers SET tailnet_magic_dns = true WHERE id = $1`, f.srv.ID)
	p, err = f.plan(false)
	require.NoError(t, err)
	require.True(t, p.RestartRequired)
	require.Contains(t, strings.Join(p.Reasons, "\n"), "MagicDNS adds the tailnet resolver")
	require.Contains(t, strings.Join(p.Reasons, "\n"), "shares the proxy container's network namespace")

	// Turning it off removes the device, which is named by its tailnet name when the node reported one.
	f.exec(`UPDATE servers SET tailnet_mode = 'off', tailnet_magic_dns = false WHERE id = $1`, f.srv.ID)
	p, err = f.plan(false)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), `deletes the device "old-edge" from your tailnet`)

	f.exec(`UPDATE servers SET tailnet_status = '{"state":"online","fqdn":"old-edge.tail1.ts.net"}'::jsonb WHERE id = $1`, f.srv.ID)
	p, err = f.plan(false)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), `deletes the device "old-edge.tail1.ts.net" from your tailnet`)
}

func TestPlanWarnsWhenTheServerIsOffline(t *testing.T) {
	f := newFixture(t)
	p, err := f.plan(false)
	require.NoError(t, err)
	require.Empty(t, p.Warnings)

	f.exec(`UPDATE servers SET status = 'offline' WHERE id = $1`, f.srv.ID)
	p, err = f.plan(false)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, "\n"), "The server is offline")
}

func TestDeployRefusesARevokedServerAndSendsNoTableWithoutARouteSource(t *testing.T) {
	f := newFixture(t)
	noRoutes := NewService(Deps{Pool: f.pool, Bus: f.svc.bus, Dispatcher: f.hub, Box: f.box, Tailscale: f.ts})

	_, err := noRoutes.Deploy(f.admin, f.srv.ID, false)
	require.NoError(t, err)
	var payload agentprotocol.ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(f.lastProxyConfigure().Payload, &payload))
	require.Empty(t, payload.Routes)
	require.False(t, payload.ReplaceRoutes, "without a route source the proxy keeps the routes it has")
	require.Zero(t, payload.TableSeq)

	f.exec(`UPDATE agent_commands SET status = 'succeeded', finished_at = now()`)
	f.exec(`UPDATE servers SET status = 'revoked' WHERE id = $1`, f.srv.ID)
	_, err = f.svc.Deploy(f.admin, f.srv.ID, false)
	require.Equal(t, "server_revoked", code(t, err))
}

// ---- results ----

// deployAndGet deploys and returns the pending proxy_configure command.
func (f *fixture) deployAndGet() db.AgentCommand {
	f.t.Helper()
	_, err := f.svc.Deploy(f.admin, f.srv.ID, false)
	require.NoError(f.t, err)
	return f.lastProxyConfigure()
}

func TestHandleResultIgnoresWhatIsNotItsToApply(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	cmd := f.deployAndGet()
	ok := succeededResult(t, agentprotocol.ProxyConfigureResult{Outcome: "succeeded"}, cmd)

	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, uuid.New(), ok), "unknown command")
	require.NoError(t, f.svc.HandleResult(bg, uuid.New(), cmd.ID, ok), "another server's report of this command")

	id := uuid.New()
	routesCmd, err := f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: id, ServerID: f.srv.ID, Kind: agentprotocol.KindSyncRoutes, Payload: []byte(`{}`), IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, routesCmd.ID, ok), "a command of another kind")

	id = uuid.New()
	bad, err := f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: id, ServerID: f.srv.ID, Kind: agentprotocol.KindProxyConfigure, Payload: []byte(`"garbage"`), IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, bad.ID, ok), "an unreadable payload is dropped, not retried")

	require.EqualValues(t, 0, f.reload().ProxyAppliedGeneration, "none of those applied anything")
}

func TestAFailedResultStillRecordsTheTailnetStateTheAgentSaw(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	cmd := f.deployAndGet()
	data, err := json.Marshal(agentprotocol.ProxyConfigureResult{Outcome: "failed", Tailnet: &agentprotocol.TailnetStatus{Mode: "managed", State: agentprotocol.TailnetStateNeedsAuth, Message: "auth key rejected"}})
	require.NoError(t, err)

	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, agentprotocol.ResultFrame{CommandID: cmd.ID.String(), Outcome: agentprotocol.OutcomeFailed, Error: "boom", Data: data}))
	srv := f.reload()
	require.EqualValues(t, 0, srv.ProxyAppliedGeneration)
	var st agentprotocol.TailnetStatus
	require.NoError(t, json.Unmarshal(srv.TailnetStatus, &st))
	require.Equal(t, "auth key rejected", st.Message)
}

func TestAnOlderResultRedeliveredLaterCannotUndoANewerSuccess(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	first := f.deployAndGet()
	f.exec(`UPDATE agent_commands SET status = 'failed', finished_at = now() WHERE id = $1`, first.ID)
	_, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{AllowHostGateway: ptr(false)})
	require.NoError(t, err)
	second := f.deployAndGet()
	f.exec(`UPDATE agent_commands SET status = 'succeeded', finished_at = now() WHERE id = $1`, second.ID)
	require.EqualValues(t, 0, f.reload().ProxyAppliedGeneration)

	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, first.ID, succeededResult(t, agentprotocol.ProxyConfigureResult{Outcome: "succeeded"}, first)))
	require.EqualValues(t, 0, f.reload().ProxyAppliedGeneration, "the older command's late success changes nothing")

	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, second.ID, succeededResult(t, agentprotocol.ProxyConfigureResult{Outcome: "succeeded"}, second)))
	require.NotZero(t, f.reload().ProxyAppliedGeneration)
}

func TestASuccessfulResultIgnoresCertificatesItDoesNotKnow(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	c, _ := f.upload("site", "site.example.com")
	cmd := f.deployAndGet()
	ok := succeededResult(t, agentprotocol.ProxyConfigureResult{
		Outcome: "succeeded",
		Certificates: []agentprotocol.CertStatus{
			{ID: "not-a-uuid", Loaded: false, Error: "x"},
			{ID: uuid.NewString(), Loaded: false, Error: "another server's certificate"},
			{ID: c.ID.String(), Loaded: true},
		},
	}, cmd)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, ok))

	certs, err := f.svc.ListCertificates(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, certs, 1)
	require.Equal(t, "none", certs[0].Change)
	require.Equal(t, "active", certs[0].Status, "a loaded certificate is healthy")
}

func TestASuccessfulDeployWithoutATailnetClearsTheStoredStatus(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.exec(`UPDATE servers SET tailnet_status = '{"state":"online"}'::jsonb WHERE id = $1`, f.srv.ID)
	cmd := f.deployAndGet()
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, succeededResult(t, agentprotocol.ProxyConfigureResult{Outcome: "succeeded"}, cmd)))

	var isNull bool
	require.NoError(t, f.pool.QueryRow(bg, `SELECT tailnet_status IS NULL FROM servers WHERE id = $1`, f.srv.ID).Scan(&isNull))
	require.True(t, isNull)
}

// ---- routes ----

func TestPushRoutesQueuesTheServersCurrentTable(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	f.exec(`UPDATE servers SET acme_email = 'ops@example.com', traffic_analytics_enabled = false WHERE id = $1`, f.srv.ID)
	svcID := uuid.NewString()
	f.routes = func(context.Context, *db.Queries, uuid.UUID) ([]agentprotocol.Route, error) {
		return []agentprotocol.Route{{ServiceID: svcID, Hosts: []string{"app.example.com"}, Upstreams: []string{"web:80"}}}, nil
	}

	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.PushRoutes(bg, tx, f.srv.ID) }))
	var raw []byte
	require.NoError(t, f.pool.QueryRow(bg, `SELECT payload FROM agent_commands WHERE server_id = $1 AND kind = $2`, f.srv.ID, agentprotocol.KindSyncRoutes).Scan(&raw))
	var pl agentprotocol.SyncRoutesPayload
	require.NoError(t, json.Unmarshal(raw, &pl))
	require.Len(t, pl.Routes, 1)
	require.Equal(t, svcID, pl.Routes[0].ServiceID)
	require.Equal(t, "ops@example.com", pl.ACMEEmail)
	require.NotNil(t, pl.TrafficAnalytics)
	require.False(t, *pl.TrafficAnalytics)
	require.NotNil(t, pl.Proxy)
	require.NotZero(t, pl.TableSeq)
	first := pl.TableSeq

	// Every push carries a newer table number.
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.PushRoutes(bg, tx, f.srv.ID) }))
	require.NoError(t, f.pool.QueryRow(bg, `SELECT payload FROM agent_commands WHERE server_id = $1 AND kind = $2 ORDER BY created_at DESC LIMIT 1`, f.srv.ID, agentprotocol.KindSyncRoutes).Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &pl))
	require.Greater(t, pl.TableSeq, first)
}

func TestPushRoutesSkipsRevokedServersAndSourcelessServices(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	count := func() int {
		var n int
		require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM agent_commands WHERE kind = $1`, agentprotocol.KindSyncRoutes).Scan(&n))
		return n
	}

	f.exec(`UPDATE servers SET status = 'revoked' WHERE id = $1`, f.srv.ID)
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.PushRoutes(bg, tx, f.srv.ID) }))
	require.Zero(t, count())

	f.exec(`UPDATE servers SET status = 'online' WHERE id = $1`, f.srv.ID)
	sourceless := NewService(Deps{Pool: f.pool, Bus: f.svc.bus, Dispatcher: f.hub, Box: f.box})
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return sourceless.PushRoutes(bg, tx, f.srv.ID) }))
	require.Zero(t, count())

	err := db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.PushRoutes(bg, tx, uuid.New()) })
	require.ErrorContains(t, err, "load server")
}

func TestRoutesViewNamesPlatformRoutesAndDefaultsPlainHosts(t *testing.T) {
	f := newFixture(t)
	page := uuid.New()
	f.exec(`INSERT INTO status_pages (id, workspace_id, title, slug, published, hostname) VALUES ($1, $2, 'Status', 'status', true, 'Status.Example.com')`, page, f.ws)
	f.routes = func(context.Context, *db.Queries, uuid.UUID) ([]agentprotocol.Route, error) {
		return []agentprotocol.Route{
			{ServiceID: statusroute.ServiceID(page), Hosts: []string{"status.example.com"}, Upstreams: []string{"nebulactrl:8081"}},
			{ServiceID: systemroute.ServiceID, Hosts: []string{"nebula.example.com"}, Upstreams: []string{"nebulactrl:8080"}},
			{ServiceID: "not-a-uuid", Upstreams: nil},
		}, nil
	}
	items, err := f.svc.Routes(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.Equal(t, "Status page status.example.com", items[0].ServiceName)
	require.Equal(t, []ProxyRouteHost{{Host: "status.example.com", Kind: "serve", TLSMode: "auto"}}, items[0].Hosts, "plain hosts are served with automatic TLS")
	require.Equal(t, systemroute.Name, items[1].ServiceName)
	require.Equal(t, "http", items[0].Protocol)
	require.Equal(t, "service", items[0].Kind)
	require.Empty(t, items[2].ServiceName)
	require.NotNil(t, items[2].Upstreams, "lists are never null in the API")
	require.NotNil(t, items[2].AllowCIDRs)
	require.Empty(t, items[2].Hosts)
}

func TestRoutesViewWithoutARouteSourceIsEmpty(t *testing.T) {
	f := newFixture(t)
	sourceless := NewService(Deps{Pool: f.pool, Bus: f.svc.bus, Dispatcher: f.hub, Box: f.box})
	items, err := sourceless.Routes(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
}

// ---- certificate expiry job ----

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestCertificateExpiryKeepsALoadErrorOverTheCountdown(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	c, err := f.q.InsertCertificate(bg, db.InsertCertificateParams{
		ID: uuid.New(), WorkspaceID: f.ws, ServerID: f.srv.ID, Name: "broken", Domains: []string{"broken.example.com"}, CertPem: "x", KeyEnc: []byte("k"),
		Fingerprint: "broken", Issuer: "ca", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(3 * 24 * time.Hour),
	})
	require.NoError(t, err)
	f.exec(`UPDATE certificates SET status = 'error', status_message = 'tls: failed to parse' WHERE id = $1`, c.ID)

	n, err := CheckCertificateExpiry(bg, f.pool, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, n, "it still counts as expiring")
	got, err := f.q.GetCertificateScoped(bg, db.GetCertificateScopedParams{ID: c.ID, WorkspaceID: f.ws})
	require.NoError(t, err)
	require.Equal(t, "error", got.Status)
	require.Equal(t, "tls: failed to parse", got.StatusMessage, "the error the proxy reported is not replaced by 'expires in 3 days'")
	var audits int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = 'certificate.expiring' AND resource_id = $1`, c.ID.String()).Scan(&audits))
	require.Zero(t, audits, "nothing is announced for a certificate already known to be broken")
}

func TestCertificateExpiryFailsWhenTheDatabaseIsGone(t *testing.T) {
	f := newFixture(t)
	f.pool.Close()
	_, err := CheckCertificateExpiry(context.Background(), f.pool, time.Now(), nil)
	require.ErrorContains(t, err, "list certificates")
}

func TestCertificateExpiryRunsAsAJob(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	c, err := f.q.InsertCertificate(bg, db.InsertCertificateParams{
		ID: uuid.New(), WorkspaceID: f.ws, ServerID: f.srv.ID, Name: "soon", Domains: []string{"soon.example.com"}, CertPem: "x", KeyEnc: []byte("k"),
		Fingerprint: "soon", Issuer: "ca", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10*24*time.Hour + time.Hour),
	})
	require.NoError(t, err)

	logs := &syncBuf{}
	jm := jobs.New(f.pool, slog.New(slog.NewTextHandler(logs, nil)))
	RegisterJobs(jm, nil)
	require.NoError(t, jm.Migrate(bg))
	require.NoError(t, jm.Start(bg))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, jm.Stop(ctx))
	})
	require.Equal(t, "certificate_expiry", CertExpiryArgs{}.Kind())
	require.NoError(t, jm.Insert(bg, CertExpiryArgs{}, nil))

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "certificates near expiry") && strings.Contains(logs.String(), "count=1")
	}, 15*time.Second, 50*time.Millisecond, logs.String())
	got, err := f.q.GetCertificateScoped(bg, db.GetCertificateScopedParams{ID: c.ID, WorkspaceID: f.ws})
	require.NoError(t, err)
	require.Equal(t, "expires in 10 days", got.StatusMessage)
}

// ---- certificate parsing ----

func TestParseCertificateRefusesWhatItCannotBeSureOf(t *testing.T) {
	g := genCert(t, certOpts{dns: []string{"app.example.com"}})
	now := time.Now()

	_, err := ParseCertificate(g.certPEM+"-----BEGIN TRUSTED CERTIFICATE-----\nAAAA\n-----END TRUSTED CERTIFICATE-----\n", g.keyPEM, now)
	require.Equal(t, "invalid_certificate", code(t, err))
	require.Contains(t, err.Error(), "unsupported PEM block")

	_, err = ParseCertificate(strings.Repeat(g.certPEM, 11), g.keyPEM, now)
	require.Contains(t, err.Error(), "more than 10 certificates")

	junk := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}))
	_, err = ParseCertificate(junk, g.keyPEM, now)
	require.Contains(t, err.Error(), "certificate 1 of the chain cannot be parsed")
	_, err = ParseCertificate(g.certPEM+junk, g.keyPEM, now)
	require.Contains(t, err.Error(), "certificate 2 of the chain cannot be parsed")

	garbageKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key")}))
	_, err = ParseCertificate(g.certPEM, garbageKey, now)
	require.Equal(t, "invalid_key", code(t, err))
	require.NotContains(t, err.Error(), "not a key")
}

func TestParseCertificateNamesTheIssuerWhenItHasNoCommonName(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{Organization: []string{"Leaf Org"}}, DNSNames: []string{"app.example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	issuer := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{Organization: []string{"Acme Trust"}, Country: []string{"DE"}}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, caKey)
	require.NoError(t, err)
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	p, err := ParseCertificate(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})), time.Now())
	require.NoError(t, err)
	require.Contains(t, p.Issuer, "Acme Trust", "without a common name the issuer is its full distinguished name")
}

// ---- HTTP ----

func TestHTTPProxyEndpoints(t *testing.T) {
	f := portsFixture(t)
	handler := func(ctx context.Context) http.Handler {
		p, _ := auth.PrincipalFrom(ctx)
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
			})
		})
		cfg := huma.DefaultConfig("test", "1")
		cfg.CreateHooks = nil
		Register(humachi.New(r, cfg), f.svc)
		return r
	}
	admin, reader := handler(f.admin), handler(f.reader)
	do := func(h http.Handler, method, path string, body any) (int, map[string]any) {
		var rd *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			rd = bytes.NewReader(raw)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m map[string]any
		if rec.Body.Len() > 0 {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
		}
		return rec.Code, m
	}
	base := "/servers/" + f.srv.ID.String()

	status, body := do(admin, http.MethodGet, base+"/proxy", nil)
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, false, body["staged"])

	status, _ = do(admin, http.MethodGet, "/servers/"+uuid.NewString()+"/proxy", nil)
	require.Equal(t, http.StatusNotFound, status)

	// Certificates: upload (the key never comes back), list, delete.
	g := genCert(t, certOpts{dns: []string{"app.example.com"}})
	status, body = do(admin, http.MethodPost, base+"/certificates", map[string]any{"name": "app", "certPem": g.certPEM, "keyPem": g.keyPEM})
	require.Equal(t, http.StatusCreated, status, body)
	require.NotContains(t, body, "keyPem")
	certID := body["id"].(string)
	status, _ = do(reader, http.MethodPost, base+"/certificates", map[string]any{"name": "again", "certPem": g.certPEM, "keyPem": g.keyPEM})
	require.Equal(t, http.StatusForbidden, status, "servers:read cannot upload")
	status, body = do(reader, http.MethodGet, base+"/certificates", nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["items"], 1)
	status, _ = do(admin, http.MethodDelete, "/certificates/"+certID, nil)
	require.Equal(t, http.StatusNoContent, status)

	// Settings, plan, deploy.
	status, body = do(admin, http.MethodPatch, base+"/proxy/settings", map[string]any{"upstreamPolicy": map[string]any{"allowHostGateway": false}, "ports": map[string]any{"http": 8080}})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, true, body["staged"])
	status, body = do(admin, http.MethodPost, base+"/proxy/plan", map[string]any{"ports": map[string]any{"http": 8081}})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, true, body["restartRequired"])
	status, body = do(admin, http.MethodPost, base+"/proxy/tailnet/reprovision", map[string]any{})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "reprovision_not_applicable", body["code"])
	status, body = do(admin, http.MethodPost, base+"/proxy/deploy", map[string]any{})
	require.Equal(t, http.StatusAccepted, status, body)
	require.NotEmpty(t, body["commandId"])
	status, body = do(admin, http.MethodPost, base+"/proxy/deploy", map[string]any{})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "proxy_deploy_in_progress", body["code"])

	status, body = do(reader, http.MethodGet, base+"/proxy/routes", nil)
	require.Equal(t, http.StatusOK, status, body)
	require.Empty(t, body["items"])
}
