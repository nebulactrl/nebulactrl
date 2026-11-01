package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	oauthSecret = "tskey-client-TAILNETTEST-secret"
	mintedKey   = "tskey-auth-MINTED-9999"
)

var bg = context.Background()

// fakeTS is a minimal Tailscale API that records every call in order.
type fakeTS struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string // "METHOD path"
	mints []string // bodies of key creations
}

func newFakeTS(t *testing.T) *fakeTS {
	f := &fakeTS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
			return
		}
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/keys"):
			f.mints = append(f.mints, string(body))
			_, _ = w.Write([]byte(`{"id":"kMINT1","key":"` + mintedKey + `","expires":"2026-10-26T11:00:00Z"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/settings"):
			_, _ = w.Write([]byte(`{"httpsEnabled":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTS) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakeDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
}

func (d *fakeDisp) Enqueue(_ context.Context, _ pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	return uuid.New(), nil
}

func (d *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *fakeDisp) kinds() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.cmds {
		out = append(out, c.Kind)
	}
	return out
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ws     uuid.UUID
	env    db.Environment
	server db.Server
	other  db.Server
	svc    *Service
	ts     *tailscale.Service
	fake   *fakeTS
	disp   *fakeDisp
	admin  context.Context
	viewer context.Context
}

func principal(ws, user uuid.UUID, scopes []string) context.Context {
	return auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: user, WorkspaceID: ws, Label: "t", Scopes: scopes})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	other, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent2"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online'")
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	jm := jobs.New(pool, nil)
	fake := newFakeTS(t)
	tsSvc := tailscale.NewService(pool, box, jm, nil)
	tsSvc.SetBaseURL(fake.srv.URL, fake.srv.Client())
	tailscale.RegisterJobs(jm, tsSvc)
	disp := &fakeDisp{}
	return &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, env: env, server: srv, other: other, ts: tsSvc, fake: fake, disp: disp,
		svc:    NewService(Deps{Pool: pool, Bus: events.NewBus(), Dispatcher: disp, Tailscale: tsSvc}),
		admin:  principal(ws.ID, u.ID, policy.AllScopes),
		viewer: principal(ws.ID, u.ID, []string{policy.ScopeProjectsRead}),
	}
}

func (f *fixture) connect() {
	f.t.Helper()
	_, err := f.ts.Put(f.admin, tailscale.PutInput{ClientID: "client-1", ClientSecret: oauthSecret, AllowedTags: []string{"tag:nebula-svc", "tag:db"}})
	require.NoError(f.t, err)
}

func (f *fixture) connectHeadscale() {
	f.t.Helper()
	_, err := f.ts.Put(f.admin, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: "https://hs.example.com", ClientSecret: "hs-apikey-1234567", HeadscaleCredential: tailscale.HeadscaleAPIKey,
	})
	require.NoError(f.t, err)
}

// service inserts a (never deployed) image service on f.server.
func (f *fixture) service(slug string, tn *tailnetcfg.TailnetConfig) db.Service {
	f.t.Helper()
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, Tailnet: tn}.WithDefaults()
	raw, err := json.Marshal(cfg)
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID:            uuid.New(),
		EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

// deployed is service plus a domain and a succeeded current deployment.
func (f *fixture) deployed(slug, host string, tn *tailnetcfg.TailnetConfig) db.Service {
	f.t.Helper()
	sv := f.service(slug, tn)
	cfg := services.DecodeConfig(sv.Config)
	_, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{
		ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling",
	})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	return sv
}

func (f *fixture) scope(id uuid.UUID) services.Scope {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(bg, f.q, id)
	require.NoError(f.t, err)
	return sc
}

func (f *fixture) apply(spec agentprotocol.ServiceTailnetSpec, st agentprotocol.ServiceTailnetStatus, server uuid.UUID) {
	f.t.Helper()
	require.NoError(f.t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.apply(bg, tx, f.ws, &server, spec, &st) }))
}

func (f *fixture) row(id uuid.UUID) db.ServiceTailnet {
	f.t.Helper()
	r, err := f.q.GetServiceTailnet(bg, id)
	require.NoError(f.t, err)
	return r
}

func (f *fixture) riverArgs(kind string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(bg, `SELECT args::text FROM river_job WHERE kind = $1 ORDER BY id`, kind)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(f.t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

var online = agentprotocol.ServiceTailnetStatus{
	State: agentprotocol.ServiceTailnetOnline, FQDN: "web-production.tail1234.ts.net", IPs: []string{"100.64.0.7"}, DeviceID: "nNODE1CNTRL", Provisioned: true,
}

func TestSpecProvisioningDecisions(t *testing.T) {
	f := newFixture(t)
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)

	_, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.Equal(t, "tailnet_not_configured", apiCode(t, err))

	f.connect()
	spec, hide, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	require.False(t, hide)
	require.Equal(t, agentprotocol.TailnetEnsure, spec.Mode)
	require.True(t, spec.Provision, "a first deployment creates the device")
	require.Equal(t, "web-production", spec.Hostname, "default <service>-<environment>")
	require.Equal(t, []string{"tag:nebula-svc"}, spec.Tags)
	require.Equal(t, agentprotocol.TailnetProviderTailscale, spec.Provider)
	require.True(t, spec.Web)
	require.Equal(t, 80, spec.Port)
	require.NotEmpty(t, spec.IdentityHash)
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NotContains(t, string(raw), mintedKey)
	require.NotContains(t, string(raw), oauthSecret)

	// Once applied, the same configuration keeps the node.
	f.apply(*spec, online, f.server.ID)
	again, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	require.False(t, again.Provision)

	// A different hostname is a different device identity.
	cfg := sc.Config()
	cfg.Tailnet.Hostname = "api"
	renamed, _, err := f.svc.Spec(bg, f.q, sc, cfg, f.server, false)
	require.NoError(t, err)
	require.True(t, renamed.Provision)
	require.Equal(t, "api", renamed.Hostname)

	// Exposures are applied live: no new device.
	cfg = sc.Config()
	cfg.Tailnet.TCP = []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432}}
	live, _, err := f.svc.Spec(bg, f.q, sc, cfg, f.server, false)
	require.NoError(t, err)
	require.False(t, live.Provision)
	require.Len(t, live.TCP, 1)

	// Another server needs its own node.
	moved, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.other, false)
	require.NoError(t, err)
	require.True(t, moved.Provision)

	// An extra tag outside the allow-list is refused.
	cfg = sc.Config()
	cfg.Tailnet.ExtraTags = []string{"tag:prod-admin"}
	_, _, err = f.svc.Spec(bg, f.q, sc, cfg, f.server, false)
	require.Equal(t, "tailnet_tag_not_allowed", apiCode(t, err))
	cfg.Tailnet.ExtraTags = []string{"tag:db"}
	withTag, _, err := f.svc.Spec(bg, f.q, sc, cfg, f.server, false)
	require.NoError(t, err)
	require.Equal(t, []string{"tag:db", "tag:nebula-svc"}, withTag.Tags)
	require.True(t, withTag.Provision, "tags are part of the identity")
}

func TestRollbackKeepsTailnetStateAsApplied(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true, HidePublic: true})
	sc := f.scope(sv.ID)
	spec, hide, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	require.True(t, hide)
	f.apply(*spec, online, f.server.ID)

	// The release being rolled back to has no tailnet at all.
	old := sc.Config()
	old.Tailnet = nil
	kept, hide, err := f.svc.Spec(bg, f.q, sc, old, f.server, true)
	require.NoError(t, err)
	require.NotNil(t, kept, "a rollback never removes the node")
	require.Equal(t, agentprotocol.TailnetEnsure, kept.Mode)
	require.False(t, kept.Provision)
	require.Equal(t, "web-production", kept.Hostname)
	require.True(t, hide, "nor does it re-expose a tailnet-only service")

	// On another server (a drain) the node is re-created there.
	moved, _, err := f.svc.Spec(bg, f.q, sc, old, f.other, true)
	require.NoError(t, err)
	require.True(t, moved.Provision)

	// A service that never had a node gets none from a rollback.
	plain := f.service("plain", nil)
	psc := f.scope(plain.ID)
	none, hide, err := f.svc.Spec(bg, f.q, psc, psc.Config(), f.server, true)
	require.NoError(t, err)
	require.Nil(t, none)
	require.False(t, hide)
}

func TestDisablingRemovesTheNode(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)

	none, _, err := f.svc.Spec(bg, f.q, f.scope(f.service("plain", nil).ID), services.Config{}, f.server, false)
	require.NoError(t, err)
	require.Nil(t, none, "nothing to remove when there never was a node")

	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	f.apply(*spec, online, f.server.ID)

	off := sc.Config()
	off.Tailnet = nil
	rm, _, err := f.svc.Spec(bg, f.q, sc, off, f.server, false)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.TailnetRemove, rm.Mode)

	// The agent reports the sidecar removed: the row goes and the device is deleted in the background.
	f.apply(*rm, agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetRemoved, Removed: true}, f.server.ID)
	_, err = f.q.GetServiceTailnet(bg, sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	jobs := f.riverArgs("tailnet_delete_device")
	require.Len(t, jobs, 1)
	require.Contains(t, jobs[0], "nNODE1CNTRL")
	require.Empty(t, f.disp.kinds(), "the deploy already removed the sidecar on this server")

	// The job's work: the device is deleted through the API.
	require.NoError(t, f.ts.DeleteNode(bg, f.ws, tailscale.NodeRef{DeviceID: "nNODE1CNTRL"}))
	require.Contains(t, f.fake.callLog(), "DELETE /api/v2/device/nNODE1CNTRL")
}

func TestDeletingTheServiceRemovesSidecarAndDevice(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	f.apply(*spec, online, f.server.ID)

	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.ServiceDeleted(bg, tx, sc) }))
	_, err = f.q.GetServiceTailnet(bg, sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, []string{agentprotocol.KindRemoveTailnet}, f.disp.kinds())
	require.Equal(t, f.server.ID, f.disp.cmds[0].ServerID)
	require.Len(t, f.riverArgs("tailnet_delete_device"), 1)

	// A service that has no node is a no-op.
	plain := f.service("plain", nil)
	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.ServiceDeleted(bg, tx, f.scope(plain.ID)) }))
	require.Len(t, f.disp.cmds, 1)
}

func TestKeyIsMintedAtSendTimeOnlyWhenProvisioning(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)

	// No Provision, no key and no API call.
	keep := *spec
	keep.Provision = false
	secrets, err := f.svc.Secrets(bg, sv.ID, keep)
	require.NoError(t, err)
	require.Empty(t, secrets)
	require.Empty(t, f.fake.callLog())

	// First provisioning: one single-use tagged key, nothing to delete.
	secrets, err = f.svc.Secrets(bg, sv.ID, *spec)
	require.NoError(t, err)
	require.Equal(t, map[string]string{agentprotocol.SecretServiceTailnetKey: mintedKey}, secrets)
	require.Equal(t, []string{"POST /api/v2/tailnet/-/keys"}, f.fake.callLog())
	var req struct {
		Capabilities struct {
			Devices struct {
				Create struct {
					Reusable      bool     `json:"reusable"`
					Ephemeral     bool     `json:"ephemeral"`
					Preauthorized bool     `json:"preauthorized"`
					Tags          []string `json:"tags"`
				} `json:"create"`
			} `json:"devices"`
		} `json:"capabilities"`
		ExpirySeconds int    `json:"expirySeconds"`
		Description   string `json:"description"`
	}
	require.NoError(t, json.Unmarshal([]byte(f.fake.mints[0]), &req))
	c := req.Capabilities.Devices.Create
	require.False(t, c.Reusable)
	require.False(t, c.Ephemeral, "a host outage must not delete the node")
	require.True(t, c.Preauthorized)
	require.Equal(t, []string{"tag:nebula-svc"}, c.Tags)
	require.Equal(t, 3600, req.ExpirySeconds)
	require.Equal(t, "nebulactrl shop production web", req.Description, "names the service in characters the API accepts")

	// The key is never part of what is persisted: the spec has none.
	raw, _ := json.Marshal(spec)
	require.NotContains(t, string(raw), "tskey")

	// Re-provisioning an existing node deletes the old device first, so the new one keeps the name.
	f.apply(*spec, online, f.server.ID)
	spec.Provision = true
	_, err = f.svc.Secrets(bg, sv.ID, *spec)
	require.NoError(t, err)
	calls := f.fake.callLog()
	require.Equal(t, []string{"POST /api/v2/tailnet/-/keys", "DELETE /api/v2/device/nNODE1CNTRL", "POST /api/v2/tailnet/-/keys"}, calls)
	row := f.row(sv.ID)
	require.Empty(t, row.DeviceID, "the old device is forgotten")
	require.True(t, row.NeedsReprovision, "until a deployment applies the new node")
}

func TestSyncCommandSecretsAndReprovision(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)

	_, err = f.svc.Reprovision(f.admin, sv.ID)
	require.Equal(t, "not_applicable", apiCode(t, err), "nothing to re-provision before the first deployment")

	f.apply(*spec, online, f.server.ID)
	_, err = f.svc.Reprovision(f.viewer, sv.ID)
	require.Equal(t, "forbidden", apiCode(t, err))

	res, err := f.svc.Reprovision(f.admin, sv.ID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, res.CommandID)
	require.Equal(t, []string{agentprotocol.KindSyncTailnet}, f.disp.kinds())
	require.Equal(t, f.server.ID, f.disp.cmds[0].ServerID)
	var p agentprotocol.SyncTailnetPayload
	require.NoError(t, json.Unmarshal(f.disp.cmds[0].Payload, &p))
	require.True(t, p.Tailnet.Provision)
	require.NotContains(t, string(f.disp.cmds[0].Payload), "tskey", "the payload carries the spec only")
	row := f.row(sv.ID)
	require.True(t, row.NeedsReprovision)
	require.Equal(t, services.TailnetProvisioning, row.State)

	// The key is attached when the command is sent.
	secrets, err := f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTailnet, f.disp.cmds[0].Payload)
	require.NoError(t, err)
	require.Equal(t, mintedKey, secrets[agentprotocol.SecretServiceTailnetKey])
	none, err := f.svc.SecretsForCommand(bg, agentprotocol.KindRemoveTailnet, nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestApplyStoresStateWithoutAnySecret(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true, Funnel: true, HidePublic: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432}}})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	require.True(t, spec.Funnel)

	f.apply(*spec, online, f.server.ID)
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetOnline, row.State)
	require.Equal(t, "web-production.tail1234.ts.net", row.Fqdn)
	require.Equal(t, []string{"100.64.0.7"}, row.Ips)
	require.True(t, row.AppliedHidePublic)
	require.True(t, row.Funnel)
	require.False(t, row.NeedsReprovision)
	require.NotNil(t, row.ProvisionedAt)
	require.NotContains(t, string(row.Applied), "tskey")

	// The view the canvas chip reads.
	v := services.TailnetViewOf(row)
	require.Equal(t, "https://web-production.tail1234.ts.net", v.URL)
	require.True(t, v.Web)
	require.True(t, v.Funnel)
	require.Equal(t, []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432}}, v.TCP)

	// A node that needs attention is flagged for re-provisioning.
	bad := agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetNeedsAttn, Message: "Device removed from the tailnet or key rejected. Re-provision."}
	f.apply(*spec, bad, f.server.ID)
	row = f.row(sv.ID)
	require.Equal(t, services.TailnetNeedsAttn, row.State)
	require.True(t, row.NeedsReprovision)
	require.Equal(t, "nNODE1CNTRL", row.DeviceID, "the known device stays known so re-provisioning can delete it")
	again, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	require.True(t, again.Provision)
}

func TestTailnetOnlyRemovesThePublicRouter(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.deployed("web", "web.example.com", &tailnetcfg.TailnetConfig{Enabled: true, Web: true, HidePublic: true})
	sc := f.scope(sv.ID)

	routes, err := domains.ServerRoutes(bg, f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1, "public until the tailnet configuration is applied")
	require.Equal(t, []string{"web.example.com"}, routes[0].Hosts)

	spec, hide, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	require.True(t, hide)
	require.True(t, spec.HidePublic)
	f.apply(*spec, online, f.server.ID)

	routes, err = domains.ServerRoutes(bg, f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, routes, "Tailnet only: the domain is kept but never routed")
	doms, err := f.q.ListServiceDomains(bg, sv.ID)
	require.NoError(t, err)
	require.Len(t, doms, 1)

	// Switching it off applies on the next deployment and routes the domain again.
	off := *spec
	off.HidePublic = false
	f.apply(off, online, f.server.ID)
	routes, err = domains.ServerRoutes(bg, f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	require.Len(t, routes, 1)
}

func TestObservedStateAndStaleness(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	f.apply(*spec, online, f.server.ID)

	observe := func(server uuid.UUID, st *agentprotocol.ServiceTailnetStatus) {
		require.NoError(t, f.svc.HandleObserved(bg, server, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: sv.ID.String(), Tailnet: st}}}))
	}

	observe(f.server.ID, &agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetNeedsAttn, Message: "Device removed from the tailnet or key rejected. Re-provision."})
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetNeedsAttn, row.State)
	require.True(t, row.NeedsReprovision)

	// Another server cannot speak for the node.
	observe(f.other.ID, &agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetOnline})
	require.Equal(t, services.TailnetNeedsAttn, f.row(sv.ID).State)

	// A missing sidecar is only reported after the deployment grace period.
	observe(f.server.ID, nil)
	require.Equal(t, services.TailnetNeedsAttn, f.row(sv.ID).State)
	f.svc.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	observe(f.server.ID, nil)
	row = f.row(sv.ID)
	require.Equal(t, services.TailnetOffline, row.State)
	require.Contains(t, row.Message, "not running")

	observe(f.server.ID, &agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetOnline, FQDN: "web-production.tail1234.ts.net", IPs: []string{"100.64.0.7"}})
	require.Equal(t, services.TailnetOnline, f.row(sv.ID).State)
}

func TestPlanCheckWarningsAndBlocking(t *testing.T) {
	f := newFixture(t)
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true, Funnel: true, HidePublic: true})
	sc := f.scope(sv.ID)

	warns, probs, err := f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &f.server)
	require.NoError(t, err)
	require.Empty(t, warns)
	require.Len(t, probs, 1)
	require.Equal(t, "tailnet_not_configured", probs[0].Code)

	f.connect()
	warns, probs, err = f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &f.server)
	require.NoError(t, err)
	require.Empty(t, probs)
	all := strings.Join(warns, "\n")
	require.Contains(t, all, "creates device web-production")
	require.Contains(t, all, "Funnel is on")
	require.Contains(t, all, "Tailnet only")

	// An agent without the capability cannot manage sidecars.
	old := f.server
	old.Facts = json.RawMessage(`{"os":"linux","capabilities":["proxy.v2"]}`)
	_, probs, err = f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &old)
	require.NoError(t, err)
	require.Len(t, probs, 1)
	require.Equal(t, "agent_too_old", probs[0].Code)

	// Disabling a node that exists says what happens to the device.
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	f.apply(*spec, online, f.server.ID)
	off := sc.Config()
	off.Tailnet = nil
	warns, probs, err = f.svc.PlanCheck(bg, f.q, sc, off, &f.server)
	require.NoError(t, err)
	require.Empty(t, probs)
	all = strings.Join(warns, "\n")
	require.Contains(t, all, "removes device web-production.tail1234.ts.net")
	require.Contains(t, all, "new tailnet IP")
	require.Contains(t, all, "routed again", "Tailnet only is lifted with the node")
}

func TestValidateConfigAgainstTheIntegration(t *testing.T) {
	f := newFixture(t)
	sv := f.service("web", nil)
	sc := f.scope(sv.ID)
	web := tailnetcfg.TailnetConfig{Enabled: true, Web: true}

	require.Equal(t, "tailnet_not_configured", apiCode(t, f.svc.ValidateConfig(bg, sc, web)))

	f.connect()
	require.NoError(t, f.svc.ValidateConfig(bg, sc, web))
	bad := web
	bad.ExtraTags = []string{"tag:root"}
	require.Equal(t, "tailnet_tag_not_allowed", apiCode(t, f.svc.ValidateConfig(bg, sc, bad)))

	// HTTPS certificates disabled on the tailnet (known after a connection test).
	_, err := f.pool.Exec(bg, `UPDATE tailnet_integrations SET https_enabled = false`)
	require.NoError(t, err)
	require.Equal(t, "tailnet_https_disabled", apiCode(t, f.svc.ValidateConfig(bg, sc, web)))
	tcpOnly := tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432}}}
	require.NoError(t, f.svc.ValidateConfig(bg, sc, tcpOnly))

	// Headscale: no Funnel, no TLS-terminating ports, and no HTTPS requirement.
	f.connectHeadscale()
	require.NoError(t, f.svc.ValidateConfig(bg, sc, web))
	funnel := web
	funnel.Funnel = true
	require.Equal(t, "tailnet_funnel_unsupported", apiCode(t, f.svc.ValidateConfig(bg, sc, funnel)))
	tls := tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432, TLS: true}}}
	require.Equal(t, "tailnet_tls_unsupported", apiCode(t, f.svc.ValidateConfig(bg, sc, tls)))

	spec, _, err := f.svc.Spec(bg, f.q, sc, services.Config{Port: 80, Tailnet: &web}, f.server, false)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.TailnetProviderHeadscale, spec.Provider)
	require.Equal(t, "https://hs.example.com", spec.LoginServer)
}

func TestConfigChangedAuditsEnableDisableAndFunnel(t *testing.T) {
	f := newFixture(t)
	sv := f.service("web", nil)
	sc := f.scope(sv.ID)
	on := &tailnetcfg.TailnetConfig{Enabled: true, Web: true, Funnel: true}
	require.NoError(t, db.InTx(f.admin, f.pool, func(tx pgx.Tx) error { return f.svc.ConfigChanged(f.admin, tx, sc, nil, on) }))
	require.NoError(t, db.InTx(f.admin, f.pool, func(tx pgx.Tx) error { return f.svc.ConfigChanged(f.admin, tx, sc, on, nil) }))

	rows, err := f.pool.Query(bg, `SELECT action, metadata::text FROM audit_events WHERE action LIKE 'tailnet.%' ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a, meta string
		require.NoError(t, rows.Scan(&a, &meta))
		actions = append(actions, a)
		require.NotContains(t, meta, "tskey")
	}
	require.Equal(t, []string{"tailnet.enable", "tailnet.funnel_enabled", "tailnet.disable"}, actions)
}
