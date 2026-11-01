package proxyserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const tsSecret = "tskey-client-PROXYTEST-secret"

// fakeTS is a minimal Tailscale API.
type fakeTS struct {
	srv *httptest.Server
	mu  sync.Mutex
	// recorded API calls
	mints   []string // request bodies of key creation
	deleted []string // device ids
}

func newFakeTS(t *testing.T) *fakeTS {
	f := &fakeTS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/oauth/token":
			form, _ := url.ParseQuery(string(body))
			if form.Get("client_secret") != tsSecret {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/keys"):
			f.mints = append(f.mints, string(body))
			_, _ = w.Write([]byte(`{"id":"kID","key":"tskey-auth-MINTED-9999","expires":"2026-10-26T11:00:00Z"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/api/v2/device/"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	box    *secretbox.Box
	svc    *Service
	ts     *tailscale.Service
	hub    *agents.Hub
	jm     *jobs.Manager
	fake   *fakeTS
	ws     uuid.UUID
	srv    db.Server
	admin  context.Context // all scopes
	token  context.Context // proxy:write + servers:read/write, no admin
	reader context.Context // servers:read only
	other  context.Context // another workspace, all scopes
	routes RouteSource
}

func principal(ws, user uuid.UUID, scopes []string, token bool) context.Context {
	p := &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Label: "t", Scopes: scopes}
	if token {
		id := uuid.New()
		p.Type, p.TokenID = auth.TypeToken, &id
	}
	return auth.WithPrincipal(context.Background(), p)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	ws2, err := q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	u2, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws2.ID, Email: "b@example.com", Name: "B", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "Edge 1"})
	require.NoError(t, err)
	facts, _ := json.Marshal(agentprotocol.Facts{Capabilities: []string{agentprotocol.CapProxyV2}})
	_, err = pool.Exec(bg, `UPDATE servers SET status='online', facts=$2 WHERE id=$1`, srv.ID, facts)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	bus := events.NewBus()
	jm := jobs.New(pool, nil)
	hub := agents.NewHub(pool, bus, jm, &config.Config{}, nil)
	t.Cleanup(hub.Close)
	fake := newFakeTS(t)
	tsSvc := tailscale.NewService(pool, box, jm, nil)
	tsSvc.SetBaseURL(fake.srv.URL, fake.srv.Client())
	tailscale.RegisterJobs(jm, tsSvc)

	f := &fixture{t: t, pool: pool, q: q, box: box, ts: tsSvc, hub: hub, jm: jm, fake: fake, ws: ws.ID}
	f.routes = func(context.Context, *db.Queries, uuid.UUID) ([]agentprotocol.Route, error) { return nil, nil }
	f.svc = NewService(Deps{Pool: pool, Bus: bus, Dispatcher: hub, Box: box, Tailscale: tsSvc,
		Routes: func(ctx context.Context, q *db.Queries, id uuid.UUID) ([]agentprotocol.Route, error) {
			return f.routes(ctx, q, id)
		}})
	f.srv, err = q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)
	f.admin = principal(ws.ID, u.ID, policy.AllScopes, false)
	f.token = principal(ws.ID, u.ID, []string{policy.ScopeProxyWrite, policy.ScopeServersRead, policy.ScopeServersWrite}, true)
	f.reader = principal(ws.ID, u.ID, []string{policy.ScopeServersRead}, true)
	f.other = principal(ws2.ID, u2.ID, policy.AllScopes, false)
	return f
}

func (f *fixture) reload() db.Server {
	f.t.Helper()
	s, err := f.q.LoadServerRow(context.Background(), db.LoadServerRowParams{ID: f.srv.ID})
	require.NoError(f.t, err)
	return s
}

func (f *fixture) configureTailscale() {
	f.t.Helper()
	_, err := f.ts.Put(f.admin, tailscale.PutInput{ClientID: "client-1", ClientSecret: tsSecret, DefaultTags: []string{"tag:nebula-svc"}})
	require.NoError(f.t, err)
}

func (f *fixture) upload(name string, dns ...string) (ServerCertificate, genResult) {
	f.t.Helper()
	g := genCert(f.t, certOpts{dns: dns})
	c, err := f.svc.UploadCertificate(f.admin, f.srv.ID, UploadCertificateInput{Name: name, CertPEM: g.certPEM, KeyPEM: g.keyPEM})
	require.NoError(f.t, err)
	return c, g
}

// lastProxyConfigure returns the newest proxy_configure command row of the server.
func (f *fixture) lastProxyConfigure() db.AgentCommand {
	f.t.Helper()
	var id uuid.UUID
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`SELECT id FROM agent_commands WHERE server_id=$1 AND kind=$2 ORDER BY created_at DESC LIMIT 1`, f.srv.ID, agentprotocol.KindProxyConfigure).Scan(&id))
	cmd, err := f.q.GetAgentCommand(context.Background(), id)
	require.NoError(f.t, err)
	return cmd
}

func succeededResult(t *testing.T, data agentprotocol.ProxyConfigureResult, cmd db.AgentCommand) agentprotocol.ResultFrame {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return agentprotocol.ResultFrame{CommandID: cmd.ID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: raw}
}

func TestUploadCertificateStagesAndNeverExposesTheKey(t *testing.T) {
	f := newFixture(t)
	g := genCert(t, certOpts{dns: []string{"app.example.com", "*.apps.example.com"}})
	c, err := f.svc.UploadCertificate(f.admin, f.srv.ID, UploadCertificateInput{Name: "wildcard", CertPEM: g.certPEM, KeyPEM: g.keyPEM})
	require.NoError(t, err)
	require.Equal(t, "add", c.Change)
	require.Equal(t, "pending", c.Status)
	require.Equal(t, []string{"app.example.com", "*.apps.example.com"}, c.Domains)
	require.GreaterOrEqual(t, c.DaysToExpiry, 89)
	require.EqualValues(t, 1, f.reload().ProxyGeneration, "an upload bumps proxy_generation")

	// The view (and the list and proxy views) never carry key material.
	keyBody := strings.Split(g.keyPEM, "\n")[1]
	for _, v := range []any{c} {
		raw, _ := json.Marshal(v)
		require.NotContains(t, string(raw), "PRIVATE KEY")
		require.NotContains(t, string(raw), keyBody)
	}
	list, err := f.svc.ListCertificates(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	pv, err := f.svc.GetProxy(f.admin, f.srv.ID)
	require.NoError(t, err)
	raw, _ := json.Marshal(pv)
	require.NotContains(t, string(raw), "PRIVATE KEY")
	require.NotContains(t, string(raw), keyBody)
	require.True(t, pv.Staged)

	// The key is sealed at rest and recoverable only through CertSecrets.
	var enc []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT key_enc FROM certificates WHERE id=$1`, c.ID).Scan(&enc))
	require.NotContains(t, string(enc), keyBody)

	// Staged certificates are not part of the applied config (route pushes)...
	applied, err := BuildConfig(context.Background(), f.q, f.srv.ID)
	require.NoError(t, err)
	require.Empty(t, applied.Certificates)
	// ...but are part of the desired payload, by reference only.
	payload, err := desiredPayload(context.Background(), f.q, f.reload(), false)
	require.NoError(t, err)
	require.Len(t, payload.Proxy.Certificates, 1)
	require.Equal(t, agentprotocol.SecretCertPrefix+c.ID.String(), payload.Proxy.Certificates[0].KeyRef)
	rawPayload, _ := json.Marshal(payload)
	require.NotContains(t, string(rawPayload), "PRIVATE KEY")
	require.NotContains(t, string(rawPayload), keyBody)
	secrets, err := CertSecrets(context.Background(), f.q, f.box, &payload.Proxy)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(g.keyPEM), strings.TrimSpace(secrets[agentprotocol.SecretCertPrefix+c.ID.String()]))

	// Audit: name and domains only.
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT metadata::text FROM audit_events WHERE action='certificate.upload'`).Scan(&meta))
	require.Contains(t, meta, "wildcard")
	require.Contains(t, meta, "app.example.com")
	require.NotContains(t, meta, "BEGIN")
	require.NotContains(t, meta, keyBody)
}

func TestUploadCertificateGuards(t *testing.T) {
	f := newFixture(t)
	g := genCert(t, certOpts{dns: []string{"a.example.com"}})
	in := UploadCertificateInput{Name: "a", CertPEM: g.certPEM, KeyPEM: g.keyPEM}

	_, err := f.svc.UploadCertificate(f.reader, f.srv.ID, in)
	require.Equal(t, "forbidden", code(t, err))
	_, err = f.svc.UploadCertificate(f.other, f.srv.ID, in)
	require.Equal(t, "not_found", code(t, err), "another workspace sees no such server")
	_, err = f.svc.ListCertificates(f.other, f.srv.ID)
	require.Equal(t, "not_found", code(t, err))

	// Tokens with proxy:write may upload (admin is not needed).
	_, err = f.svc.UploadCertificate(f.token, f.srv.ID, in)
	require.NoError(t, err)
	_, err = f.svc.UploadCertificate(f.token, f.srv.ID, UploadCertificateInput{Name: "dup-cert", CertPEM: g.certPEM, KeyPEM: g.keyPEM})
	require.Equal(t, "certificate_exists", code(t, err))
	g2 := genCert(t, certOpts{dns: []string{"b.example.com"}})
	_, err = f.svc.UploadCertificate(f.token, f.srv.ID, UploadCertificateInput{Name: "a", CertPEM: g2.certPEM, KeyPEM: g2.keyPEM})
	require.Equal(t, "certificate_name_taken", code(t, err))
	_, err = f.svc.UploadCertificate(f.token, f.srv.ID, UploadCertificateInput{Name: "  ", CertPEM: g2.certPEM, KeyPEM: g2.keyPEM})
	require.Equal(t, "validation_failed", code(t, err))
	_, err = f.svc.UploadCertificate(f.token, f.srv.ID, UploadCertificateInput{Name: "bad", CertPEM: g2.certPEM, KeyPEM: g.keyPEM})
	require.Equal(t, "key_mismatch", code(t, err))

	// An agent without proxy.v2 cannot stage certificates.
	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET facts='{}'::jsonb WHERE id=$1`, f.srv.ID)
	require.NoError(t, err)
	_, err = f.svc.UploadCertificate(f.token, f.srv.ID, UploadCertificateInput{Name: "new", CertPEM: g2.certPEM, KeyPEM: g2.keyPEM})
	require.Equal(t, "agent_too_old", code(t, err))
	_, err = f.svc.UpdateSettings(f.token, f.srv.ID, SettingsInput{AllowHostGateway: ptr(false)})
	require.Equal(t, "agent_too_old", code(t, err))
	_, err = f.svc.PlanDeploy(f.token, f.srv.ID, false, nil)
	require.Equal(t, "agent_too_old", code(t, err))
	_, err = f.svc.Deploy(f.token, f.srv.ID, false)
	require.Equal(t, "agent_too_old", code(t, err))
	pv, err := f.svc.GetProxy(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.True(t, pv.AgentTooOld)
}

func ptr[T any](v T) *T { return &v }

func TestDeleteCertificateHardWhenNeverAppliedStagedWhenApplied(t *testing.T) {
	f := newFixture(t)
	c1, _ := f.upload("never-applied", "one.example.com")
	require.NoError(t, f.svc.DeleteCertificate(f.token, c1.ID))
	_, err := f.q.GetCertificateScoped(context.Background(), db.GetCertificateScopedParams{ID: c1.ID, WorkspaceID: f.ws})
	require.Error(t, err, "a never-applied certificate is deleted at once")

	c2, _ := f.upload("applied", "two.example.com")
	_, err = f.pool.Exec(context.Background(), `UPDATE certificates SET applied_at=now(), status='active' WHERE id=$1`, c2.ID)
	require.NoError(t, err)
	require.Equal(t, "forbidden", code(t, f.svc.DeleteCertificate(f.reader, c2.ID)))
	require.Equal(t, "not_found", code(t, f.svc.DeleteCertificate(f.other, c2.ID)), "IDOR: another workspace gets 404")
	require.NoError(t, f.svc.DeleteCertificate(f.token, c2.ID))
	list, err := f.svc.ListCertificates(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "remove", list[0].Change, "an applied certificate is only staged for removal")
	// Still in the applied config until the proxy is deployed.
	applied, err := BuildConfig(context.Background(), f.q, f.srv.ID)
	require.NoError(t, err)
	require.Empty(t, applied.Certificates, "a soft-deleted certificate leaves the applied config")
	require.Equal(t, "not_found", code(t, f.svc.DeleteCertificate(f.token, c2.ID)), "deleting twice")
}

func TestDeleteCertificateRefusedWhileADomainPinsIt(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image", Config: []byte(`{}`), ServerID: &f.srv.ID})
	require.NoError(t, err)
	c, _ := f.upload("pinned", "app.example.com")
	_, err = f.pool.Exec(bg, `INSERT INTO domains (service_id, hostname, target_port, tls_mode, certificate_id) VALUES ($1, 'app.example.com', 80, 'custom', $2)`, sv.ID, c.ID)
	require.NoError(t, err)

	require.Equal(t, "certificate_in_use", code(t, f.svc.DeleteCertificate(f.admin, c.ID)))
	_, err = f.pool.Exec(bg, `DELETE FROM domains`)
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteCertificate(f.admin, c.ID))
}

func TestSettingsAdminOnlyTogglesAndValidation(t *testing.T) {
	f := newFixture(t)
	// A token with proxy:write may change the host gateway setting...
	v, err := f.svc.UpdateSettings(f.token, f.srv.ID, SettingsInput{AllowHostGateway: ptr(false)})
	require.NoError(t, err)
	require.False(t, v.UpstreamPolicy.AllowHostGateway)
	require.EqualValues(t, 1, v.Generation)
	// ...but the SSRF guard toggles need admin.
	_, err = f.svc.UpdateSettings(f.token, f.srv.ID, SettingsInput{AllowLoopback: ptr(true)})
	require.Equal(t, "forbidden", code(t, err))
	_, err = f.svc.UpdateSettings(f.token, f.srv.ID, SettingsInput{AllowLinkLocal: ptr(true)})
	require.Equal(t, "forbidden", code(t, err))
	require.False(t, PolicyFromRow(f.reload().UpstreamPolicy).AllowLoopback)
	v, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{AllowLoopback: ptr(true), AllowLinkLocal: ptr(true)})
	require.NoError(t, err)
	require.True(t, v.UpstreamPolicy.AllowLoopback)
	require.True(t, v.UpstreamPolicy.AllowLinkLocal)
	require.EqualValues(t, 2, v.Generation)
	// The applied policy is unchanged until Deploy proxy.
	require.True(t, v.AppliedPolicy.AllowHostGateway)
	require.False(t, v.AppliedPolicy.AllowLoopback)
	applied, err := BuildConfig(context.Background(), f.q, f.srv.ID)
	require.NoError(t, err)
	require.False(t, applied.UpstreamPolicy.AllowLoopback)
	require.True(t, applied.UpstreamPolicy.AllowHostGateway)

	// No-op PATCH: no new generation.
	v, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{AllowLoopback: ptr(true)})
	require.NoError(t, err)
	require.EqualValues(t, 2, v.Generation)

	_, err = f.svc.UpdateSettings(f.reader, f.srv.ID, SettingsInput{AllowHostGateway: ptr(true)})
	require.Equal(t, "forbidden", code(t, err))
	_, err = f.svc.UpdateSettings(f.other, f.srv.ID, SettingsInput{AllowHostGateway: ptr(true)})
	require.Equal(t, "not_found", code(t, err))
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("bogus")})
	require.Equal(t, "validation_failed", code(t, err))
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetHostname: ptr("Not Valid!")})
	require.Equal(t, "invalid_tailnet_hostname", code(t, err))
}

func TestTailnetModeRequirements(t *testing.T) {
	f := newFixture(t)
	// managed needs the integration
	_, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("managed")})
	require.Equal(t, "tailnet_not_configured", code(t, err))
	// host needs Tailscale detected on the host
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("host")})
	require.Equal(t, "host_tailscale_not_detected", code(t, err))
	facts, _ := json.Marshal(agentprotocol.Facts{
		Capabilities: []string{agentprotocol.CapProxyV2}, HostTailscale: &agentprotocol.HostTailscale{Detected: true, IPs: []string{"100.64.0.9"}},
	})
	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET facts=$2 WHERE id=$1`, f.srv.ID, facts)
	require.NoError(t, err)
	v, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("host")})
	require.NoError(t, err)
	require.Equal(t, "host", v.Tailnet.Mode)
	require.Equal(t, []string{"100.64.0.9"}, v.Tailnet.HostTailscale.IPs)
	require.True(t, v.Tailnet.HostTailscale.Detected)

	f.configureTailscale()
	v, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("managed"), TailnetMagicDNS: ptr(true)})
	require.NoError(t, err)
	require.Equal(t, "nebula-edge-1", v.Tailnet.Hostname, "default hostname is nebula-<server slug>")
	require.True(t, v.Tailnet.IntegrationConfigured)
	require.True(t, v.Tailnet.MagicDNS)
}

func TestPlanDescribesRestartsProvisioningAndDeletion(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.False(t, p.RestartRequired)
	require.Empty(t, p.Diff)

	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{AllowHostGateway: ptr(false)})
	require.NoError(t, err)
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.True(t, p.RestartRequired)
	require.Contains(t, strings.Join(p.Reasons, " "), "host.docker.internal")
	require.Equal(t, "upstreamPolicy.allowHostGateway", p.Diff[0].Field)

	f.configureTailscale()
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("managed"), TailnetMagicDNS: ptr(true)})
	require.NoError(t, err)
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	joined := strings.Join(p.Warnings, " ")
	require.Contains(t, joined, "Creates a device")
	require.Contains(t, joined, "nebula-edge-1")
	require.Contains(t, strings.Join(p.Reasons, " "), "MagicDNS")

	// Pretend it was applied, then plan the removal: the warning says the device is deleted.
	_, err = f.pool.Exec(context.Background(), `UPDATE servers SET tailnet_applied_mode='managed', tailnet_applied_hostname='nebula-edge-1', tailnet_applied_magic_dns=true,
		applied_upstream_policy=upstream_policy WHERE id=$1`, f.srv.ID)
	require.NoError(t, err)
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, true, nil)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, " "), "Re-provisioning deletes the current device")
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("off")})
	require.NoError(t, err)
	p, err = f.svc.PlanDeploy(f.admin, f.srv.ID, false, nil)
	require.NoError(t, err)
	require.Contains(t, strings.Join(p.Warnings, " "), "deletes the device")
	require.True(t, p.RestartRequired, "dropping MagicDNS recreates the proxy container")
	_, err = f.svc.PlanDeploy(f.admin, f.srv.ID, true, nil)
	require.Equal(t, "reprovision_not_applicable", code(t, err))
}

func TestDeployBuildsPayloadWithoutSecretsAndResultFlipsAppliedState(t *testing.T) {
	f := newFixture(t)
	f.configureTailscale()
	bg := context.Background()
	c, g := f.upload("site", "site.example.com")
	_, err := f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("managed"), AllowHostGateway: ptr(false)})
	require.NoError(t, err)
	srv := f.reload()
	require.EqualValues(t, 3, srv.ProxyGeneration)

	_, err = f.svc.Deploy(f.reader, f.srv.ID, false)
	require.Equal(t, "forbidden", code(t, err))
	_, err = f.svc.Deploy(f.other, f.srv.ID, false)
	require.Equal(t, "not_found", code(t, err))

	f.routes = func(context.Context, *db.Queries, uuid.UUID) ([]agentprotocol.Route, error) {
		return []agentprotocol.Route{{ServiceID: uuid.NewString(), Hosts: []string{"site.example.com"}, Upstreams: []string{"c:80"}}}, nil
	}
	res, err := f.svc.Deploy(f.token, f.srv.ID, false)
	require.NoError(t, err)
	require.EqualValues(t, 3, res.Generation)
	cmd := f.lastProxyConfigure()
	require.Equal(t, res.CommandID, cmd.ID)
	require.Nil(t, cmd.DeploymentID)
	require.True(t, strings.HasPrefix(cmd.IdempotencyKey, "proxy_configure:"+f.srv.ID.String()+":3:"))

	// The persisted payload has no key, no auth key, no hash.
	payloadText := string(cmd.Payload)
	require.NotContains(t, payloadText, "PRIVATE KEY")
	require.NotContains(t, payloadText, strings.Split(g.keyPEM, "\n")[1])
	require.NotContains(t, payloadText, "tskey")
	require.NotContains(t, payloadText, "$2a$")
	var payload agentprotocol.ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &payload))
	require.Len(t, payload.Proxy.Certificates, 1)
	require.Equal(t, c.ID.String(), payload.Proxy.Certificates[0].ID)
	require.Equal(t, agentprotocol.SecretCertPrefix+c.ID.String(), payload.Proxy.Certificates[0].KeyRef)
	require.NotNil(t, payload.Proxy.Tailnet)
	require.True(t, payload.Proxy.Tailnet.Provision, "first managed deploy provisions")
	require.Equal(t, "nebula-edge-1", payload.Proxy.Tailnet.Hostname)
	require.False(t, payload.Proxy.UpstreamPolicy.AllowHostGateway)
	require.True(t, payload.ReplaceRoutes)
	require.Len(t, payload.Routes, 1)

	// Send-time secrets: the key via CertSecrets, the auth key via MintTailnetKey.
	secrets, err := CertSecrets(bg, f.q, f.box, &payload.Proxy)
	require.NoError(t, err)
	require.Contains(t, secrets[payload.Proxy.Certificates[0].KeyRef], "PRIVATE KEY")
	key, err := f.svc.MintTailnetKey(bg, f.srv.ID)
	require.NoError(t, err)
	require.Equal(t, "tskey-auth-MINTED-9999", key)
	require.Len(t, f.fake.mints, 1)
	require.Contains(t, f.fake.mints[0], "nebulactrl server Edge 1")
	require.Contains(t, f.fake.mints[0], `"tag:nebula-svc"`)

	// A second deploy while the first is pending is refused.
	_, err = f.svc.Deploy(f.token, f.srv.ID, false)
	require.Equal(t, "proxy_deploy_in_progress", code(t, err))

	// The agent reports success: the cert fails to load, the node is online.
	ok := succeededResult(t, agentprotocol.ProxyConfigureResult{
		Outcome: "succeeded", Restarted: true,
		Certificates: []agentprotocol.CertStatus{{ID: c.ID.String(), Loaded: false, Error: "tls: failed to parse"}},
		Tailnet:      &agentprotocol.TailnetStatus{Mode: "managed", State: agentprotocol.TailnetStateOnline, FQDN: "nebula-edge-1.t.ts.net", IPs: []string{"100.64.0.5"}, DeviceID: "nDEV1"},
	}, cmd)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, ok))
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, ok), "results are redelivered: idempotent")

	srv = f.reload()
	require.EqualValues(t, 3, srv.ProxyAppliedGeneration)
	require.Equal(t, "managed", srv.TailnetAppliedMode)
	require.Equal(t, "nebula-edge-1", *srv.TailnetAppliedHostname)
	require.Equal(t, "nDEV1", *srv.TailnetDeviceID)
	require.False(t, PolicyFromRow(srv.AppliedUpstreamPolicy).AllowHostGateway)
	var st agentprotocol.TailnetStatus
	require.NoError(t, json.Unmarshal(srv.TailnetStatus, &st))
	require.Equal(t, "nebula-edge-1.t.ts.net", st.FQDN)

	certs, err := f.svc.ListCertificates(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.Equal(t, "none", certs[0].Change, "the certificate is applied now")
	require.Equal(t, "error", certs[0].Status)
	require.Contains(t, certs[0].StatusMessage, "failed to parse")

	// Route pushes now carry the applied state: certificate ref, applied tailnet, no provisioning.
	applied, err := BuildConfig(bg, f.q, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, applied.Certificates, 1)
	require.NotNil(t, applied.Tailnet)
	require.False(t, applied.Tailnet.Provision)
	require.Equal(t, "nebula-edge-1", applied.Tailnet.Hostname)
	require.False(t, applied.UpstreamPolicy.AllowHostGateway)

	_, err = f.pool.Exec(bg, `UPDATE agent_commands SET status='succeeded', finished_at=now() WHERE id=$1`, cmd.ID)
	require.NoError(t, err)
	pv, err := f.svc.GetProxy(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.False(t, pv.Staged)
	require.NotNil(t, pv.LastDeploy)
	require.Equal(t, "succeeded", pv.LastDeploy.Status)
	require.Equal(t, "online", pv.Tailnet.Status.State)

	// An unchanged redeploy does not provision again.
	_, err = f.svc.Deploy(f.token, f.srv.ID, false)
	require.NoError(t, err)
	var again agentprotocol.ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(f.lastProxyConfigure().Payload, &again))
	require.False(t, again.Proxy.Tailnet.Provision)
}

func TestFailedResultChangesNothingButRecordsTheError(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	c, _ := f.upload("site", "site.example.com")
	_, err := f.svc.Deploy(f.admin, f.srv.ID, false)
	require.NoError(t, err)
	cmd := f.lastProxyConfigure()
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, agentprotocol.ResultFrame{
		CommandID: cmd.ID.String(), Outcome: agentprotocol.OutcomeFailed, Error: "port 8443 in use",
	}))
	require.EqualValues(t, 0, f.reload().ProxyAppliedGeneration)
	certs, err := f.svc.ListCertificates(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.Equal(t, "add", certs[0].Change)
	require.Equal(t, c.ID, certs[0].ID)
	// A result for a command of another kind is ignored.
	other := agents.NewCommand{ServerID: f.srv.ID, Kind: agentprotocol.KindSyncRoutes, Payload: []byte(`{}`)}
	_ = other
	// The failure is visible and a retry is possible (the in-flight guard only covers unfinished commands).
	_, err = f.pool.Exec(bg, `UPDATE agent_commands SET status='failed', error='port 8443 in use', finished_at=now() WHERE id=$1`, cmd.ID)
	require.NoError(t, err)
	pv, err := f.svc.GetProxy(f.admin, f.srv.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", pv.LastDeploy.Status)
	require.Equal(t, "port 8443 in use", pv.LastDeploy.Message)
	_, err = f.svc.Deploy(f.admin, f.srv.ID, false)
	require.NoError(t, err)
}

func (f *fixture) riverJobs(kind string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT args::text FROM river_job WHERE kind=$1 ORDER BY id`, kind)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(f.t, rows.Scan(&a))
		out = append(out, a)
	}
	return out
}

func TestDisablingOrReprovisioningDeletesTheOldDeviceViaJob(t *testing.T) {
	f := newFixture(t)
	f.configureTailscale()
	bg := context.Background()
	// Applied managed node with a known device.
	_, err := f.pool.Exec(bg, `UPDATE servers SET tailnet_mode='managed', tailnet_applied_mode='managed', tailnet_hostname='nebula-edge',
		tailnet_applied_hostname='nebula-edge', tailnet_device_id='nOLD', proxy_generation=1 WHERE id=$1`, f.srv.ID)
	require.NoError(t, err)

	// Reprovision: Provision=true, the new device replaces the old, a delete job is queued for the old.
	_, err = f.svc.Deploy(f.admin, f.srv.ID, true)
	require.NoError(t, err)
	cmd := f.lastProxyConfigure()
	var p agentprotocol.ProxyConfigurePayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.True(t, p.Proxy.Tailnet.Provision)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, succeededResult(t, agentprotocol.ProxyConfigureResult{
		Tailnet: &agentprotocol.TailnetStatus{Mode: "managed", State: "online", DeviceID: "nNEW"},
	}, cmd)))
	require.Equal(t, "nNEW", *f.reload().TailnetDeviceID)
	jobs := f.riverJobs("tailnet_delete_device")
	require.Len(t, jobs, 1)
	require.Contains(t, jobs[0], `"nOLD"`)
	require.NotContains(t, jobs[0], "nNEW")

	// Disable: the payload asks to remove the node; on success the device id is cleared and the device queued.
	_, err = f.svc.UpdateSettings(f.admin, f.srv.ID, SettingsInput{TailnetMode: ptr("off")})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `UPDATE agent_commands SET status='succeeded' WHERE id=$1`, cmd.ID)
	require.NoError(t, err)
	_, err = f.svc.Deploy(f.admin, f.srv.ID, false)
	require.NoError(t, err)
	cmd = f.lastProxyConfigure()
	p = agentprotocol.ProxyConfigurePayload{}
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.NotNil(t, p.Proxy.Tailnet)
	require.True(t, p.Proxy.Tailnet.Remove)
	require.Equal(t, "off", p.Proxy.Tailnet.Mode)
	require.NoError(t, f.svc.HandleResult(bg, f.srv.ID, cmd.ID, succeededResult(t, agentprotocol.ProxyConfigureResult{}, cmd)))
	srv := f.reload()
	require.Nil(t, srv.TailnetDeviceID)
	require.Equal(t, "off", srv.TailnetAppliedMode)
	require.Nil(t, srv.TailnetStatus)
	jobs = f.riverJobs("tailnet_delete_device")
	require.Len(t, jobs, 2)
	require.Contains(t, jobs[1], `"nNEW"`)

	// The job itself deletes the device through the Tailscale API; without the integration it is a no-op.
	require.NoError(t, f.ts.DeleteDevice(bg, f.ws, "nNEW"))
	require.Equal(t, []string{"nNEW"}, f.fake.deleted)
	require.NoError(t, f.ts.Delete(f.admin))
	require.NoError(t, f.ts.DeleteDevice(bg, f.ws, "nNEW"))
	require.Equal(t, []string{"nNEW"}, f.fake.deleted, "no integration: nothing is called")
}

func TestMintTailnetKeyWithoutIntegrationIs422(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.MintTailnetKey(context.Background(), f.srv.ID)
	require.Equal(t, "tailnet_not_configured", code(t, err))
	noTS := NewService(Deps{Pool: f.pool, Box: f.box})
	_, err = noTS.MintTailnetKey(context.Background(), f.srv.ID)
	require.Equal(t, "tailnet_not_configured", code(t, err))
	var _ TailnetKeyMinter = f.svc
}

func TestL4PortsAppearInProxyView(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: env.ID, Name: "pg-proxy", Slug: "pg-proxy", Kind: "route", SourceType: "external", Config: []byte(`{}`), ServerID: &f.srv.ID})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, `INSERT INTO route_ports (service_id, server_id, protocol, listen_port) VALUES ($1,$2,'tcp',5433)`, sv.ID, f.srv.ID)
	require.NoError(t, err)
	pv, err := f.svc.GetProxy(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, pv.L4Ports, 1)
	require.Equal(t, ProxyL4Port{Protocol: "tcp", Port: 5433, ServiceID: sv.ID, ServiceName: "pg-proxy", Deployed: false}, pv.L4Ports[0])
}

func TestRoutesViewMasksAuthAndNamesServices(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{ID: uuid.New(), EnvironmentID: env.ID, Name: "Web", Slug: "web", Kind: "app", SourceType: "image", Config: []byte(`{}`), ServerID: &f.srv.ID})
	require.NoError(t, err)
	f.routes = func(context.Context, *db.Queries, uuid.UUID) ([]agentprotocol.Route, error) {
		return []agentprotocol.Route{{
			ServiceID: sv.ID.String(), Hosts: []string{"app.example.com"}, Upstreams: []string{"web-1:80"},
			RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{
				ID: uuid.NewString(), Host: "app.example.com", PathPrefix: "/api", AuthRef: "auth:x",
				Options: agentprotocol.RouteOptions{BasicAuth: true, RateLimit: &agentprotocol.RateLimit{Average: 1, Burst: 1, PeriodSeconds: 1}},
				// A hash must never be exposed even if a caller leaked one into the table.
				AuthUsers: []string{"admin:$2a$10$abcdefghijklmnopqrstuv"},
			}}},
		}, {ServiceID: uuid.NewString(), RouteExt: agentprotocol.RouteExt{Protocol: "tcp", ListenPort: 5433, AllowCIDRs: []string{"10.0.0.0/8"}}, Upstreams: []string{"db:5432"}}}, nil
	}
	items, err := f.svc.Routes(f.reader, f.srv.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "Web", items[0].ServiceName)
	require.True(t, items[0].Hosts[0].BasicAuth)
	require.True(t, items[0].Hosts[0].RateLimit)
	require.Equal(t, "/api", items[0].Hosts[0].PathPrefix)
	require.Equal(t, "tcp", items[1].Protocol)
	require.Equal(t, 5433, items[1].ListenPort)
	raw, _ := json.Marshal(items)
	require.NotContains(t, string(raw), "$2a$")
	_, err = f.svc.Routes(f.other, f.srv.ID)
	require.Equal(t, "not_found", code(t, err))
}

func TestCertificateExpiryJobMarksStatusAuditsOncePerThreshold(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	mk := func(name string, hours time.Duration) uuid.UUID {
		c, err := f.q.InsertCertificate(bg, db.InsertCertificateParams{
			ID: uuid.New(), WorkspaceID: f.ws, ServerID: f.srv.ID, Name: name, Domains: []string{name + ".example.com"}, CertPem: "x", KeyEnc: []byte("k"),
			Fingerprint: name, Issuer: "ca", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(hours),
		})
		require.NoError(t, err)
		return c.ID
	}
	far, soon, critical, gone := mk("far", 100*24*time.Hour), mk("soon", 20*24*time.Hour+time.Hour), mk("critical", 5*24*time.Hour+time.Hour), mk("gone", -time.Hour)
	// A stale countdown message of a renewed/far certificate is cleared.
	_, err := f.pool.Exec(bg, `UPDATE certificates SET status_message='expires in 12 days' WHERE id=$1`, far)
	require.NoError(t, err)

	n, err := CheckCertificateExpiry(bg, f.pool, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	get := func(id uuid.UUID) db.Certificate {
		c, err := f.q.GetCertificateScoped(bg, db.GetCertificateScopedParams{ID: id, WorkspaceID: f.ws})
		require.NoError(t, err)
		return c
	}
	require.Equal(t, "", get(far).StatusMessage)
	require.Equal(t, "expires in 20 days", get(soon).StatusMessage)
	require.Equal(t, "pending", get(soon).Status, "warning only: status unchanged")
	require.Equal(t, "expires in 5 days", get(critical).StatusMessage)
	require.Equal(t, "error", get(gone).Status)
	require.Contains(t, get(gone).StatusMessage, "expired on")

	// A second run (and a later day) does not repeat the audit event for a threshold.
	_, err = CheckCertificateExpiry(bg, f.pool, time.Now().Add(24*time.Hour), nil)
	require.NoError(t, err)
	var cnt int
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action='certificate.expiring' AND resource_id=$1`, soon.String()).Scan(&cnt))
	require.Equal(t, 1, cnt)
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action='certificate.expiring' AND resource_id=$1`, critical.String()).Scan(&cnt))
	require.Equal(t, 1, cnt)
	require.Equal(t, "expires in 19 days", get(soon).StatusMessage)

	// Crossing the next threshold audits again (30 -> 7).
	_, err = CheckCertificateExpiry(bg, f.pool, time.Now().Add(14*24*time.Hour), nil)
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action='certificate.expiring' AND resource_id=$1`, soon.String()).Scan(&cnt))
	require.Equal(t, 2, cnt)
	var meta string
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata::text FROM audit_events WHERE action='certificate.expiring' AND resource_id=$1 ORDER BY id LIMIT 1`, soon.String()).Scan(&meta))
	require.Contains(t, meta, `"thresholdDays": 30`)
}

func TestCertificateExpiryAnnouncesEachThresholdOnce(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	rec := &notificationstest.Recorder{}
	mk := func(name string, d time.Duration) {
		_, err := f.q.InsertCertificate(bg, db.InsertCertificateParams{
			ID: uuid.New(), WorkspaceID: f.ws, ServerID: f.srv.ID, Name: name, Domains: []string{name + ".example.com"}, CertPem: "x", KeyEnc: []byte("k"),
			Fingerprint: name, Issuer: "ca", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(d),
		})
		require.NoError(t, err)
	}
	mk("far", 100*24*time.Hour)
	mk("soon", 20*24*time.Hour+time.Hour)
	mk("critical", 5*24*time.Hour+time.Hour)
	mk("gone", -time.Hour)

	_, err := CheckCertificateExpiry(bg, f.pool, time.Now(), rec)
	require.NoError(t, err)
	got := rec.OfType(notifications.TypeCertExpiring)
	require.Len(t, got, 3, "the far certificate is not announced")
	by := map[string]notifications.Event{}
	for _, e := range got {
		by[e.Fields["Certificate"]] = e
		require.Equal(t, f.srv.ID, *e.ServerID)
		require.Equal(t, f.ws, e.WorkspaceID)
	}
	require.Equal(t, notifications.SeverityWarning, by["soon"].Severity)
	require.Equal(t, "Certificate soon expires in 20 days", by["soon"].Title)
	require.Equal(t, notifications.SeverityError, by["critical"].Severity)
	require.Equal(t, notifications.SeverityError, by["gone"].Severity)
	require.Equal(t, "Certificate gone has expired", by["gone"].Title)

	// The next day nothing is new; crossing the next threshold announces again.
	_, err = CheckCertificateExpiry(bg, f.pool, time.Now().Add(24*time.Hour), rec)
	require.NoError(t, err)
	require.Len(t, rec.OfType(notifications.TypeCertExpiring), 3)
	_, err = CheckCertificateExpiry(bg, f.pool, time.Now().Add(14*24*time.Hour), rec)
	require.NoError(t, err)
	require.Len(t, rec.OfType(notifications.TypeCertExpiring), 5, "soon crossed from 30 to 7 days and critical expired")
}
