package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const (
	ipA = "203.0.113.10"
	ipB = "198.51.100.7"
)

type noteRecorder struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (n *noteRecorder) Emit(_ context.Context, ev notifications.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
}

func (n *noteRecorder) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

type fakeTunnels struct {
	mu sync.Mutex
	m  map[uuid.UUID]string
}

func (f *fakeTunnels) set(id uuid.UUID, cname string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[uuid.UUID]string{}
	}
	if cname == "" {
		delete(f.m, id)
		return
	}
	f.m[id] = cname
}

func (f *fakeTunnels) TunnelTarget(_ context.Context, id uuid.UUID) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.m[id]
	return c, ok, nil
}

type env struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	svc     *Service
	fake    *fakeCF
	box     *secretbox.Box
	ws      uuid.UUID
	admin   context.Context // every scope
	dev     context.Context // projects:read + projects:write
	other   context.Context // admin of another team
	srvA    db.Server
	srvB    db.Server
	service db.Service
	logs    *bytes.Buffer
	notes   *noteRecorder
	tunnels *fakeTunnels
}

func principal(ws uuid.UUID, scopes []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: ws, Label: "tester", Scopes: scopes})
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	ws2, err := q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "shop", Slug: "shop"})
	require.NoError(t, err)
	envRow, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	a, b := ipA, ipB
	srvA, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha", PublicIp: &a})
	require.NoError(t, err)
	srvB, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "beta", PublicIp: &b})
	require.NoError(t, err)
	svcRow, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envRow.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &srvA.ID,
	})
	require.NoError(t, err)
	key, err := secretbox.GenerateKey()
	require.NoError(t, err)
	raw, err := secretbox.ParseKey(key)
	require.NoError(t, err)
	box, err := secretbox.New(raw)
	require.NoError(t, err)

	f := newFake(t)
	f.addZone("z1", "example.com")
	logs := &bytes.Buffer{}
	jm := jobs.New(pool, nil)
	svc := NewService(pool, box, jm, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	RegisterJobs(jm, svc)
	svc.SetBaseURL(f.srv.URL+"/client/v4", f.srv.Client())
	notes := &noteRecorder{}
	svc.SetNotifier(notes)
	tunnels := &fakeTunnels{}
	svc.SetTunnelTargets(tunnels)
	return &env{
		pool: pool, q: q, svc: svc, fake: f, box: box, ws: ws.ID,
		admin:   principal(ws.ID, policy.AllScopes),
		dev:     principal(ws.ID, []string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite}),
		other:   principal(ws2.ID, policy.AllScopes),
		srvA:    srvA,
		srvB:    srvB,
		service: svcRow,
		logs:    logs,
		notes:   notes,
		tunnels: tunnels,
	}
}

func (e *env) connect(t *testing.T) CloudflareConnectionView {
	t.Helper()
	v, err := e.svc.Put(e.admin, PutInput{Token: secretToken})
	require.NoError(t, err)
	return v
}

func (e *env) inTx(t *testing.T, fn func(tx pgx.Tx) error) {
	t.Helper()
	require.NoError(t, db.InTx(context.Background(), e.pool, fn))
}

// domain inserts a domain of the test service the way domains.Service does.
func (e *env) domain(t *testing.T, host, path string) db.Domain {
	t.Helper()
	var d db.Domain
	e.inTx(t, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		d, err = q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: e.service.ID, Hostname: host, TargetPort: 80, PathPrefix: path})
		if err != nil {
			return err
		}
		if err := e.svc.DomainCreated(context.Background(), tx, d.ID); err != nil {
			return err
		}
		d, err = q.GetDomainByID(context.Background(), d.ID)
		return err
	})
	return d
}

func (e *env) dom(t *testing.T, id uuid.UUID) db.Domain {
	t.Helper()
	d, err := e.q.GetDomainByID(context.Background(), id)
	require.NoError(t, err)
	return d
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

type auditRow struct{ Action, Meta string }

func (e *env) audits(t *testing.T, like string) []auditRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT action, metadata::text FROM audit_events WHERE action LIKE $1 ORDER BY id`, like)
	require.NoError(t, err)
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		require.NoError(t, rows.Scan(&r.Action, &r.Meta))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func code(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestConnectionLifecycleNeverLeaksTheToken(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()

	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.False(t, v.Configured)

	// Only administrators connect.
	_, err = e.svc.Put(e.dev, PutInput{Token: secretToken})
	require.Equal(t, "forbidden", code(t, err))
	// A token Cloudflare rejects is refused and nothing is stored.
	_, err = e.svc.Put(e.admin, PutInput{Token: "not-the-token"})
	require.Equal(t, "invalid_token", code(t, err))
	_, err = e.q.GetCloudflareConnection(bg, e.ws)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	// The first PUT needs a token.
	_, err = e.svc.Put(e.admin, PutInput{})
	require.Equal(t, "invalid_token", code(t, err))

	v = e.connect(t)
	require.True(t, v.Configured)
	require.Equal(t, "ok", v.Status)
	require.Equal(t, 1, v.ZoneCount)
	require.Equal(t, "acc1", v.AccountID)
	require.NotNil(t, v.TokenExpiresAt, "the expiry comes from the token endpoint")
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NotContains(t, string(raw), secretToken)

	// Sealed at rest.
	row, err := e.q.GetCloudflareConnection(bg, e.ws)
	require.NoError(t, err)
	require.False(t, bytes.Contains(row.TokenEnc, []byte(secretToken)))
	plain, err := e.box.OpenString(secretbox.CloudflareConnectionsToken, e.ws.String(), row.TokenEnc)
	require.NoError(t, err)
	require.Equal(t, secretToken, plain)

	// Any team member can see whether it is connected and pick a zone.
	dv, err := e.svc.Get(e.dev)
	require.NoError(t, err)
	require.True(t, dv.Configured)
	zones, err := e.svc.Zones(e.dev)
	require.NoError(t, err)
	require.Len(t, zones, 1)
	require.Equal(t, "example.com", zones[0].Name)
	// Another team sees nothing.
	ov, err := e.svc.Get(e.other)
	require.NoError(t, err)
	require.False(t, ov.Configured)

	// Omitting the token keeps the connection as it is.
	_, err = e.svc.Put(e.admin, PutInput{})
	require.NoError(t, err)
	row2, err := e.q.GetCloudflareConnection(bg, e.ws)
	require.NoError(t, err)
	require.Equal(t, row.TokenEnc, row2.TokenEnc, "omitting the token keeps the stored one")
	_, err = e.svc.Put(e.admin, PutInput{AccountID: "someone-else"})
	require.Equal(t, "invalid_account", code(t, err))

	// The zone cache refreshes on test.
	e.fake.addZone("z2", "example.org")
	v, err = e.svc.Test(e.admin)
	require.NoError(t, err)
	require.Equal(t, 2, v.ZoneCount)
	_, err = e.svc.Test(e.dev)
	require.Equal(t, "forbidden", code(t, err))

	// Neither the audit trail nor the logs ever contain the token.
	for _, r := range e.audits(t, "integration.cloudflare.%") {
		require.NotContains(t, r.Meta, secretToken)
	}
	require.Len(t, e.audits(t, "integration.cloudflare.update"), 2)
	require.NotContains(t, e.logs.String(), secretToken)

	// Disconnecting is for administrators too.
	require.Equal(t, "forbidden", code(t, e.svc.Delete(e.dev, false)))
	require.NoError(t, e.svc.Delete(e.admin, false))
	v, err = e.svc.Get(e.admin)
	require.NoError(t, err)
	require.False(t, v.Configured)
	require.Equal(t, "not_found", code(t, e.svc.Delete(e.admin, false)))
}

func TestConnectionReusesTheDNS01Token(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	credID := uuid.New()
	sealed, err := e.box.SealString(secretbox.DNSCredentialsSecrets, credID.String(), `{"CF_DNS_API_TOKEN":"`+secretToken+`"}`)
	require.NoError(t, err)
	cred, err := e.q.InsertDNSCredential(bg, db.InsertDNSCredentialParams{
		ID: credID, WorkspaceID: e.ws, Name: "cf", Provider: "cloudflare", SecretsEnc: sealed, EnvNames: []string{"CF_DNS_API_TOKEN"},
	})
	require.NoError(t, err)
	hzID := uuid.New()
	hz, err := e.box.SealString(secretbox.DNSCredentialsSecrets, hzID.String(), `{"HETZNER_API_KEY":"x"}`)
	require.NoError(t, err)
	hetzner, err := e.q.InsertDNSCredential(bg, db.InsertDNSCredentialParams{
		ID: hzID, WorkspaceID: e.ws, Name: "hz", Provider: "hetzner", SecretsEnc: hz, EnvNames: []string{"HETZNER_API_KEY"},
	})
	require.NoError(t, err)

	_, err = e.svc.Put(e.admin, PutInput{DNS01Credential: &hetzner.ID})
	require.Equal(t, "invalid_credential", code(t, err))
	unknown := uuid.New()
	_, err = e.svc.Put(e.admin, PutInput{DNS01Credential: &unknown})
	require.Equal(t, "not_found", code(t, err))
	_, err = e.svc.Put(e.other, PutInput{DNS01Credential: &cred.ID})
	require.Equal(t, "not_found", code(t, err), "another team's credential does not exist")

	v, err := e.svc.Put(e.admin, PutInput{DNS01Credential: &cred.ID})
	require.NoError(t, err)
	require.True(t, v.Configured)
	require.NotContains(t, e.logs.String(), secretToken)
}

func TestDomainGetsAMarkedRecordAndFollowsItsServer(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	d := e.domain(t, "app.example.com", "")
	require.Equal(t, "z1", d.CfZoneID)
	require.Equal(t, "example.com", d.CfZoneName)

	// Create.
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	recs := e.fake.records("app.example.com")
	require.Len(t, recs, 1)
	rec := recs[0]
	require.Equal(t, "A", rec.Type)
	require.Equal(t, ipA, rec.Content)
	require.True(t, rec.Proxied, "a new domain in a connected zone starts proxied")
	require.Equal(t, Marker(d.ID), rec.Comment, "the record carries the ownership marker")
	require.Equal(t, []string{MarkerTag}, rec.Tags)
	d = e.dom(t, d.ID)
	require.Equal(t, StatusManaged, d.CfStatus)
	require.Empty(t, d.CfError)
	require.NotNil(t, d.CfCheckedAt)
	tracked, err := e.q.ListCloudflareRecordsForHost(bg, db.ListCloudflareRecordsForHostParams{WorkspaceID: e.ws, ZoneID: "z1", Hostname: "app.example.com"})
	require.NoError(t, err)
	require.Len(t, tracked, 1)
	require.Equal(t, rec.ID, tracked[0].RecordID)
	created := e.audits(t, "dns.record.create")
	require.Len(t, created, 1)
	require.Contains(t, created[0].Meta, "app.example.com")
	require.Contains(t, created[0].Meta, ipA)

	// Syncing again changes nothing.
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Empty(t, e.fake.writeLog())

	// The server's public IP changes: the same record is retargeted in place.
	e.exec(t, `UPDATE servers SET public_ip = '192.0.2.44' WHERE id = $1`, e.srvA.ID)
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	recs = e.fake.records("app.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, rec.ID, recs[0].ID)
	require.Equal(t, "192.0.2.44", recs[0].Content)
	require.Equal(t, Marker(d.ID), recs[0].Comment)
	require.Len(t, e.audits(t, "dns.record.update"), 1)

	// The service moves to another server (a drain does exactly this and then
	// deploys): the deploy-success hook schedules a sync, which retargets the record.
	_, err = e.q.SetServiceServerBumped(bg, db.SetServiceServerBumpedParams{ID: e.service.ID, ServerID: &e.srvB.ID})
	require.NoError(t, err)
	// The sync job of the domain's creation is still queued: let it finish so the hook's job is visible.
	e.exec(t, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'cloudflare_sync_domain'`)
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.EnqueueSyncService(bg, tx, e.service.ID) })
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_domain' AND state = 'available'`))
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	recs = e.fake.records("app.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, ipB, recs[0].Content)
	require.Equal(t, rec.ID, recs[0].ID)

	// A Cloudflare Tunnel on the server swaps the address record for a proxied CNAME.
	e.tunnels.set(e.srvB.ID, "t1.cfargotunnel.com")
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	recs = e.fake.records("app.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, "CNAME", recs[0].Type)
	require.Equal(t, "t1.cfargotunnel.com", recs[0].Content)
	require.True(t, recs[0].Proxied, "tunnel records are always proxied")
	require.Equal(t, Marker(d.ID), recs[0].Comment)
	w := e.fake.writeLog()
	require.Len(t, w, 2)
	require.Equal(t, "DELETE", w[0][:6], "the address record goes first: a CNAME cannot sit next to it")
	require.Equal(t, "POST", w[1][:4])

	// Without the tunnel it is an address record again.
	e.tunnels.set(e.srvB.ID, "")
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	recs = e.fake.records("app.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, "A", recs[0].Type)
	require.Equal(t, ipB, recs[0].Content)
	require.True(t, recs[0].Proxied, "the proxy setting of the domain applies again")
	require.NotContains(t, e.logs.String(), secretToken)
}

func TestIPv6ServerGetsAnAAAARecord(t *testing.T) {
	e := newEnv(t)
	e.connect(t)
	e.exec(t, `UPDATE servers SET public_ip = '2001:db8::10' WHERE id = $1`, e.srvA.ID)
	d := e.domain(t, "v6.example.com", "")
	require.NoError(t, e.svc.SyncDomain(context.Background(), d.ID))
	recs := e.fake.records("v6.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, "AAAA", recs[0].Type)
	require.Equal(t, "2001:db8::10", recs[0].Content)
}

func TestRecordsStillGetCreatedWhenThePlanHasNoTags(t *testing.T) {
	e := newEnv(t)
	e.connect(t)
	e.fake.noTags = true
	d := e.domain(t, "app.example.com", "")
	require.NoError(t, e.svc.SyncDomain(context.Background(), d.ID))
	recs := e.fake.records("app.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, Marker(d.ID), recs[0].Comment)
	require.Empty(t, recs[0].Tags)
	require.Equal(t, StatusManaged, e.dom(t, d.ID).CfStatus)
}

func TestUnmarkedRecordsAreNeverModifiedUnlessAdopted(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	foreign := e.fake.put(Record{Type: "A", Name: "app.example.com", Content: "198.51.100.99", Comment: "my own note"})
	d := e.domain(t, "app.example.com", "")

	// Sync reports but never writes.
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Empty(t, e.fake.writeLog())
	st, err := e.svc.Status(e.dev, d.ID)
	require.NoError(t, err)
	require.Equal(t, StatusConflict, st.Status)
	require.True(t, st.NeedsAdopt)
	require.Len(t, st.Records, 1)
	require.False(t, st.Records[0].Owned)
	require.NotNil(t, st.Desired)
	require.Equal(t, ipA, st.Desired.Content)
	require.Equal(t, StatusConflict, e.dom(t, d.ID).CfStatus)

	// Fix without adopting refuses and writes nothing.
	_, err = e.svc.Fix(e.dev, d.ID, false)
	require.Equal(t, "adoption_required", code(t, err))
	require.Empty(t, e.fake.writeLog())
	got := e.fake.records("app.example.com")
	require.Equal(t, "198.51.100.99", got[0].Content)
	require.Equal(t, "my own note", got[0].Comment)

	// The replace is a team admin's action: a developer cannot take over records
	// of the zone, and nothing is written.
	_, err = e.svc.Fix(e.dev, d.ID, true)
	require.Equal(t, "forbidden", code(t, err))
	require.Empty(t, e.fake.writeLog())
	require.Equal(t, "198.51.100.99", e.fake.records("app.example.com")[0].Content)

	// Adopting takes over exactly that record.
	st, err = e.svc.Fix(e.admin, d.ID, true)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	got = e.fake.records("app.example.com")
	require.Len(t, got, 1)
	require.Equal(t, foreign.ID, got[0].ID, "the existing record is updated, not replaced")
	require.Equal(t, ipA, got[0].Content)
	require.Equal(t, Marker(d.ID), got[0].Comment)
	upd := e.audits(t, "dns.record.update")
	require.Len(t, upd, 1)
	require.Contains(t, upd[0].Meta, `"adopted": true`)

	// A record with the right target that NebulaCtrl did not create is left alone ("present").
	e.fake.put(Record{Type: "A", Name: "ok.example.com", Content: ipA, Proxied: true})
	ok := e.domain(t, "ok.example.com", "")
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncDomain(bg, ok.ID))
	require.Empty(t, e.fake.writeLog())
	require.Equal(t, StatusPresent, e.dom(t, ok.ID).CfStatus)
	st, err = e.svc.Status(e.dev, ok.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPresent, st.Status)
	require.False(t, st.NeedsAdopt)

	// A foreign CNAME blocks the address record: it stays until the explicit replace removes it.
	blocker := e.fake.put(Record{Type: "CNAME", Name: "alias.example.com", Content: "elsewhere.example.net"})
	al := e.domain(t, "alias.example.com", "")
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncDomain(bg, al.ID))
	require.Empty(t, e.fake.writeLog(), "a sync never touches a foreign CNAME")
	st, err = e.svc.Status(e.dev, al.ID)
	require.NoError(t, err)
	require.Equal(t, StatusConflict, st.Status)
	require.True(t, st.NeedsAdopt)
	require.Contains(t, st.Message, "elsewhere.example.net")
	_, err = e.svc.Fix(e.dev, al.ID, false)
	require.Equal(t, "adoption_required", code(t, err))
	after := e.fake.records("alias.example.com")
	require.Len(t, after, 1)
	require.Equal(t, blocker.ID, after[0].ID)
	require.Equal(t, "elsewhere.example.net", after[0].Content)
	st, err = e.svc.Fix(e.admin, al.ID, true)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	after = e.fake.records("alias.example.com")
	require.Len(t, after, 1)
	require.Equal(t, "A", after[0].Type)
	require.Equal(t, ipA, after[0].Content)
	gone := e.audits(t, "dns.record.delete")
	require.Len(t, gone, 1)
	require.Contains(t, gone[0].Meta, `"replaced": true`)
	require.Contains(t, gone[0].Meta, "elsewhere.example.net")

	// A record whose marker was edited away is no longer ours, even though it was tracked.
	e.fake.setComment("z1", foreign.ID, "edited by hand")
	e.fake.resetWrites()
	e.exec(t, `UPDATE servers SET public_ip = '192.0.2.77' WHERE id = $1`, e.srvA.ID)
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Empty(t, e.fake.writeLog(), "an unmarked record is never modified")
	require.Equal(t, ipA, e.fake.records("app.example.com")[0].Content)
}

func TestRemovingDomainsCollectsOnlyOwnedRecords(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	a := e.domain(t, "a.example.com", "")
	b := e.domain(t, "b.example.com", "")
	require.NoError(t, e.svc.SyncDomain(bg, a.ID))
	require.NoError(t, e.svc.SyncDomain(bg, b.ID))
	userRec := e.fake.put(Record{Type: "A", Name: "keep.example.com", Content: "198.51.100.1"})

	// Nothing to collect while every hostname is in use.
	e.fake.resetWrites()
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Empty(t, e.fake.writeLog())

	// Domain a is removed (the hook enqueues the collector in the same transaction).
	before := e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_collect'`)
	e.inTx(t, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeleteDomain(bg, a.ID); err != nil {
			return err
		}
		return e.svc.DomainsRemoved(bg, tx, e.ws)
	})
	require.Equal(t, before+1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_collect'`))
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Empty(t, e.fake.records("a.example.com"), "the record NebulaCtrl created is deleted")
	require.Len(t, e.fake.records("b.example.com"), 1)
	require.Len(t, e.fake.records("keep.example.com"), 1)
	require.Equal(t, userRec.ID, e.fake.records("keep.example.com")[0].ID)
	require.Len(t, e.audits(t, "dns.record.delete"), 1)
	left, err := e.q.ListCloudflareRecords(bg, e.ws)
	require.NoError(t, err)
	require.Len(t, left, 1)
	require.Equal(t, "b.example.com", left[0].Hostname)

	// Two domains on one hostname (different paths) share its record: it stays until both are gone.
	api1 := e.domain(t, "shared.example.com", "")
	api2 := e.domain(t, "shared.example.com", "/api")
	require.NoError(t, e.svc.SyncDomain(bg, api1.ID))
	require.NoError(t, e.svc.SyncDomain(bg, api2.ID))
	require.Len(t, e.fake.records("shared.example.com"), 1)
	e.exec(t, `DELETE FROM domains WHERE id = $1`, api1.ID)
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Len(t, e.fake.records("shared.example.com"), 1)
	e.exec(t, `DELETE FROM domains WHERE id = $1`, api2.ID)
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Empty(t, e.fake.records("shared.example.com"))

	// A record somebody re-labelled is forgotten, not deleted.
	require.NoError(t, e.svc.SyncDomain(bg, b.ID))
	recB := e.fake.records("b.example.com")[0]
	e.fake.setComment("z1", recB.ID, "mine now")
	e.exec(t, `DELETE FROM domains WHERE id = $1`, b.ID)
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Len(t, e.fake.records("b.example.com"), 1)
	left, err = e.q.ListCloudflareRecords(bg, e.ws)
	require.NoError(t, err)
	require.Empty(t, left)

	// Deleting a service soft-deletes it: its domains' records are collected too.
	c := e.domain(t, "c.example.com", "")
	require.NoError(t, e.svc.SyncDomain(bg, c.ID))
	// (the domain row stays: it is hidden by the soft-deleted service)
	e.exec(t, `UPDATE services SET deleted_at = now() WHERE id = $1`, e.service.ID)
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Empty(t, e.fake.records("c.example.com"))
}

func TestProxyToggleAndSSLModeWarning(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	d := e.domain(t, "app.example.com", "")
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))

	on, off := true, false
	st, err := e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	require.True(t, st.Proxied)
	require.True(t, st.EffectiveProxied)
	require.Equal(t, StatusManaged, st.Status)
	require.True(t, e.fake.records("app.example.com")[0].Proxied)
	require.Empty(t, st.Warnings, "Full SSL with a proxied record is fine")
	require.Equal(t, "full", st.SSLMode)

	// Flexible (and off) with a proxied record: Cloudflare speaks HTTP, the origin redirects to HTTPS.
	e.fake.ssl["z1"] = "flexible"
	st, err = e.svc.Status(e.dev, d.ID)
	require.NoError(t, err)
	require.Equal(t, "flexible", st.SSLMode)
	require.Len(t, st.Warnings, 1)
	require.Contains(t, st.Warnings[0], "Flexible")
	e.fake.ssl["z1"] = "off"
	st, err = e.svc.Status(e.dev, d.ID)
	require.NoError(t, err)
	require.Len(t, st.Warnings, 1)
	require.Contains(t, st.Warnings[0], "Off")

	// DNS only never warns.
	st, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, e.fake.records("app.example.com")[0].Proxied)
	require.Empty(t, st.Warnings)
	require.Empty(t, st.SSLMode, "the SSL mode is only read for proxied records")

	// There is no switch to stop the management: the setting is not part of the input.
	e.exec(t, `UPDATE servers SET public_ip = '192.0.2.88' WHERE id = $1`, e.srvA.ID)
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Equal(t, "192.0.2.88", e.fake.records("app.example.com")[0].Content, "the record follows its server")
	st, err = e.svc.Status(e.dev, d.ID)
	require.NoError(t, err)
	require.True(t, st.Manage)
	require.Equal(t, StatusManaged, st.Status)

	// The settings are audited, scoped to a writer, and the other team cannot reach them.
	require.NotEmpty(t, e.audits(t, "domain.dns.update"))
	_, err = e.svc.SetSettings(principal(e.ws, []string{policy.ScopeProjectsRead}), d.ID, SettingsInput{Proxied: &on})
	require.Equal(t, "forbidden", code(t, err))
	_, err = e.svc.SetSettings(e.other, d.ID, SettingsInput{Proxied: &on})
	require.Equal(t, "not_found", code(t, err))

	// Tunnel domains are proxied no matter what the toggle says.
	e.tunnels.set(e.srvA.ID, "t9.cfargotunnel.com")
	st, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.True(t, st.Tunnel)
	require.False(t, st.Proxied)
	require.True(t, st.EffectiveProxied)
	require.Equal(t, "CNAME", st.Desired.Type)
	require.True(t, e.fake.records("app.example.com")[0].Proxied)
}

func TestStatusClassification(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()

	// No connection: nothing to classify.
	none := e.domain(t, "app.example.com", "")
	st, err := e.svc.Status(e.dev, none.ID)
	require.NoError(t, err)
	require.False(t, st.Configured)
	require.Empty(t, st.Status)
	require.Empty(t, e.dom(t, none.ID).CfZoneID)

	// Connecting matches existing domains.
	e.connect(t)
	require.Equal(t, "z1", e.dom(t, none.ID).CfZoneID)

	// missing: in a zone, no record, and NebulaCtrl has not synced yet.
	st, err = e.svc.Status(e.dev, none.ID)
	require.NoError(t, err)
	require.True(t, st.Configured)
	require.Equal(t, StatusMissing, st.Status)
	require.Equal(t, "example.com", st.ZoneName)
	require.Empty(t, st.Records)
	require.Empty(t, e.fake.writeLog(), "reading the status never writes")

	// managed.
	require.NoError(t, e.svc.SyncDomain(bg, none.ID))
	st, err = e.svc.Status(e.dev, none.ID)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	require.Len(t, st.Records, 1)
	require.True(t, st.Records[0].Owned)
	require.Equal(t, ipA, st.Records[0].Content)

	// not_in_zone.
	out := e.domain(t, "app.elsewhere.dev", "")
	require.Equal(t, StatusNotInZone, e.dom(t, out.ID).CfStatus)
	require.Empty(t, e.dom(t, out.ID).CfZoneID)
	st, err = e.svc.Status(e.dev, out.ID)
	require.NoError(t, err)
	require.Equal(t, StatusNotInZone, st.Status)
	require.Empty(t, st.ZoneID)
	require.Empty(t, e.fake.records("app.elsewhere.dev"))
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncDomain(bg, out.ID))
	require.Empty(t, e.fake.writeLog())

	// conflict: somebody else's record points elsewhere.
	e.fake.put(Record{Type: "A", Name: "wrong.example.com", Content: "198.51.100.200"})
	w := e.domain(t, "wrong.example.com", "")
	st, err = e.svc.Status(e.dev, w.ID)
	require.NoError(t, err)
	require.Equal(t, StatusConflict, st.Status)

	// A managed record whose target drifted is wrong_target until synced.
	e.exec(t, `UPDATE servers SET public_ip = '192.0.2.55' WHERE id = $1`, e.srvA.ID)
	st, err = e.svc.Status(e.dev, none.ID)
	require.NoError(t, err)
	require.Equal(t, StatusWrongTarget, st.Status)
	require.False(t, st.NeedsAdopt, "NebulaCtrl's own record needs no adoption")
	st, err = e.svc.Fix(e.dev, none.ID, false)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	require.Equal(t, "192.0.2.55", e.fake.records("app.example.com")[0].Content)

	// Someone deletes our record in the dashboard: missing again, and the next sync restores it.
	rec := e.fake.records("app.example.com")[0]
	require.NoError(t, e.fake.client().DeleteRecord(bg, "z1", rec.ID))
	st, err = e.svc.Status(e.dev, none.ID)
	require.NoError(t, err)
	require.Equal(t, StatusMissing, st.Status)
	require.NoError(t, e.svc.SyncDomain(bg, none.ID))
	require.Len(t, e.fake.records("app.example.com"), 1)

	// Another team cannot see the domain at all.
	_, err = e.svc.Status(e.other, none.ID)
	require.Equal(t, "not_found", code(t, err))
	_, err = e.svc.Status(e.dev, uuid.New())
	require.Equal(t, "not_found", code(t, err))
	// Fixing something outside every zone is refused.
	_, err = e.svc.Fix(e.dev, out.ID, false)
	require.Equal(t, "not_in_zone", code(t, err))
}

func TestFailuresAreReportedOnceAndTokenFailuresMarkTheConnection(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	d := e.domain(t, "app.example.com", "")

	// Cloudflare is failing: the sync errors, the domain shows it, only the final attempt notifies.
	e.fake.failWrites = http.StatusInternalServerError
	err := e.svc.SyncDomain(bg, d.ID)
	require.Error(t, err)
	require.False(t, permanent(err), "a 5xx is retried")
	require.Contains(t, e.dom(t, d.ID).CfError, "500")
	e.svc.reportFailure(bg, OwnerDomain, d.ID, err, false)
	require.Equal(t, 0, e.notes.count())
	e.svc.reportFailure(bg, OwnerDomain, d.ID, err, true)
	require.Equal(t, 1, e.notes.count())
	e.svc.reportFailure(bg, OwnerDomain, d.ID, err, true)
	require.Equal(t, 1, e.notes.count(), "the same failure is announced once")
	ev := e.notes.events[0]
	require.Equal(t, notifications.TypeDNSSyncFailed, ev.Type)
	require.Equal(t, e.ws, ev.WorkspaceID)
	require.NotNil(t, ev.ServiceID)
	require.Contains(t, ev.Title, "app.example.com")
	require.NotContains(t, ev.Body, secretToken)

	// Recovery clears the error.
	e.fake.failWrites = 0
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Empty(t, e.dom(t, d.ID).CfError)

	// A rejected token is permanent, marks the connection and notifies once.
	e.fake.token = "rotated-" + secretToken
	err = e.svc.SyncDomain(bg, d.ID)
	require.Error(t, err)
	require.True(t, permanent(err))
	e.svc.reportFailure(bg, OwnerDomain, d.ID, err, true)
	e.svc.reportFailure(bg, OwnerDomain, d.ID, err, true)
	require.Equal(t, 2, e.notes.count())
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.Equal(t, "error", v.Status)
	require.NotEmpty(t, v.StatusMessage)
	require.NotContains(t, v.StatusMessage, secretToken)

	// Replacing the token repairs the connection.
	e.fake.token = secretToken
	v, err = e.svc.Put(e.admin, PutInput{Token: secretToken})
	require.NoError(t, err)
	require.Equal(t, "ok", v.Status)
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.NotContains(t, e.logs.String(), secretToken)
}

func TestHooksEnqueueSyncJobsOnlyWithAConnection(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	jobCount := func() int {
		return e.count(t, `SELECT count(*) FROM river_job WHERE kind IN ('cloudflare_sync_domain','cloudflare_collect')`)
	}

	// Without a connection the hooks do nothing.
	d0 := e.domain(t, "pre.example.com", "")
	e.inTx(t, func(tx pgx.Tx) error {
		if err := e.svc.EnqueueSyncServer(bg, tx, e.ws, e.srvA.ID); err != nil {
			return err
		}
		return e.svc.DomainsRemoved(bg, tx, e.ws)
	})
	require.Equal(t, 0, jobCount())
	require.Empty(t, e.dom(t, d0.ID).CfZoneID)

	// Connecting matches the existing domain and schedules its first sync.
	e.connect(t)
	require.Equal(t, "z1", e.dom(t, d0.ID).CfZoneID)
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_domain'`))

	// A new domain, a renamed domain and a server address change each schedule syncs.
	d := e.domain(t, "web.example.com", "")
	require.Equal(t, "z1", d.CfZoneID)
	e.exec(t, `UPDATE domains SET hostname = 'renamed.example.com' WHERE id = $1`, d.ID)
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.DomainRenamed(bg, tx, d.ID) })
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.EnqueueSyncServer(bg, tx, e.ws, e.srvA.ID) })
	require.GreaterOrEqual(t, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_domain'`), 2)
	// The other server has no domains.
	n := jobCount()
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.EnqueueSyncServer(bg, tx, e.ws, e.srvB.ID) })
	require.Equal(t, n, jobCount())
}

func TestNewDomainsStartProxiedExceptWildcardsAndExistingKeepTheirSetting(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()

	// A domain that exists before the connection is matched by it: managed from
	// now on and created proxied, the way a new domain is.
	before := e.domain(t, "before.example.com", "")
	require.Empty(t, before.CfZoneID)
	e.connect(t)
	before = e.dom(t, before.ID)
	require.Equal(t, "z1", before.CfZoneID)
	require.True(t, before.CfProxied)
	require.NoError(t, e.svc.SyncDomain(bg, before.ID))
	require.True(t, e.fake.records("before.example.com")[0].Proxied)

	d := e.domain(t, "app.example.com", "")
	require.True(t, d.CfProxied, "a new domain in a connected zone starts proxied")
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.True(t, e.fake.records("app.example.com")[0].Proxied)

	// A wildcard name starts DNS only, with the reason in the status.
	w := e.domain(t, "*.example.com", "")
	require.False(t, w.CfProxied)
	st, err := e.svc.Status(e.dev, w.ID)
	require.NoError(t, err)
	require.Contains(t, st.Warnings, wildcardProxyNote)

	// A domain that already lives in the zone keeps its proxy setting when the
	// connection is refreshed: a live DNS-only domain is never flipped.
	off := false
	_, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	_, err = e.svc.Test(e.admin)
	require.NoError(t, err)
	_, err = e.svc.Put(e.admin, PutInput{})
	require.NoError(t, err)
	require.False(t, e.dom(t, d.ID).CfProxied)
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.False(t, e.fake.records("app.example.com")[0].Proxied)

	// A zone that disappears from the token makes its domains not_in_zone on refresh.
	e.fake.removeZone("z1")
	_, err = e.svc.Test(e.admin)
	require.NoError(t, err)
	got := e.dom(t, d.ID)
	require.Equal(t, StatusNotInZone, got.CfStatus)
	require.Empty(t, got.CfZoneID)
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Empty(t, e.fake.writeLog())
}

func TestDisconnectingCanRemoveOurRecords(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	d := e.domain(t, "app.example.com", "")
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	userRec := e.fake.put(Record{Type: "A", Name: "user.example.com", Content: "198.51.100.5"})

	// A gate (tunnels) may refuse.
	e.svc.SetDeleteGate(func(context.Context, uuid.UUID) error {
		return apierr.Conflict("integration_in_use", "a tunnel uses it")
	})
	require.Equal(t, "integration_in_use", code(t, e.svc.Delete(e.admin, true)))
	require.Len(t, e.fake.records("app.example.com"), 1)
	e.svc.SetDeleteGate(nil)

	require.NoError(t, e.svc.Delete(e.admin, true))
	require.Empty(t, e.fake.records("app.example.com"), "our record is removed")
	require.Len(t, e.fake.records("user.example.com"), 1, "a record that is not ours stays")
	require.Equal(t, userRec.ID, e.fake.records("user.example.com")[0].ID)
	got := e.dom(t, d.ID)
	require.Empty(t, got.CfZoneID)
	require.Empty(t, got.CfStatus)
	left, err := e.q.ListCloudflareRecords(bg, e.ws)
	require.NoError(t, err)
	require.Empty(t, left)

	// Reconnect, sync, and disconnect without removing: the record stays in the zone.
	e.connect(t)
	require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	require.Len(t, e.fake.records("app.example.com"), 1)
	require.NoError(t, e.svc.Delete(e.admin, false))
	require.Len(t, e.fake.records("app.example.com"), 1)
	_, err = e.svc.Status(e.dev, d.ID)
	require.NoError(t, err)
}

func TestClientForAndConnectionForTunnels(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	_, err := e.svc.ClientFor(bg, e.ws)
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = e.svc.Connection(bg, e.ws)
	require.ErrorIs(t, err, ErrNotConfigured)

	e.connect(t)
	c, err := e.svc.ClientFor(bg, e.ws)
	require.NoError(t, err)
	zones, err := c.ListZones(bg)
	require.NoError(t, err)
	require.Len(t, zones, 1)
	conn, err := e.svc.Connection(bg, e.ws)
	require.NoError(t, err)
	require.Equal(t, "acc1", conn.AccountID)
	require.Len(t, conn.Zones, 1)
}

// Connecting Cloudflare later manages every domain that is already in its
// zones. A record somebody else created at the name is never overwritten: it is
// shown as a conflict until the explicit, audited replace.
func TestConnectingManagesExistingDomainsAndNeverOverwritesForeignRecords(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	free := e.domain(t, "free.example.com", "")
	taken := e.domain(t, "taken.example.com", "")
	require.Empty(t, e.dom(t, free.ID).CfZoneID, "no connection yet")
	foreign := e.fake.put(Record{Type: "CNAME", Name: "taken.example.com", Content: "elsewhere.example.net", Comment: "mine"})

	e.connect(t)
	for _, d := range []db.Domain{free, taken} {
		st, err := e.svc.Status(e.dev, d.ID)
		require.NoError(t, err)
		require.True(t, st.Manage, d.Hostname)
		require.Equal(t, "z1", st.ZoneID)
		require.True(t, st.Proxied, "a domain that becomes managed is created proxied")
		require.NoError(t, e.svc.SyncDomain(bg, d.ID))
	}

	// The free name got its record.
	require.Equal(t, StatusManaged, e.dom(t, free.ID).CfStatus)
	require.Equal(t, ipA, e.fake.records("free.example.com")[0].Content)

	// The taken name shows what exists and where it points; nothing was written to it.
	st, err := e.svc.Status(e.dev, taken.ID)
	require.NoError(t, err)
	require.Equal(t, StatusConflict, st.Status)
	require.True(t, st.NeedsAdopt)
	require.Contains(t, st.Message, "elsewhere.example.net")
	require.Equal(t, StatusConflict, e.dom(t, taken.ID).CfStatus)
	got := e.fake.records("taken.example.com")
	require.Len(t, got, 1)
	require.Equal(t, foreign.ID, got[0].ID)
	require.Equal(t, "elsewhere.example.net", got[0].Content)
	require.Equal(t, "mine", got[0].Comment)

	// Syncs, sweeps and status reads leave it alone, however often they run.
	require.NoError(t, e.svc.Sweep(bg))
	require.NoError(t, e.svc.SyncDomain(bg, taken.ID))
	require.Equal(t, "elsewhere.example.net", e.fake.records("taken.example.com")[0].Content)
	require.Empty(t, e.audits(t, "dns.record.delete"))

	// The replace is the one action: it is audited with what it removed.
	st, err = e.svc.Fix(e.admin, taken.ID, true)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	got = e.fake.records("taken.example.com")
	require.Len(t, got, 1)
	require.Equal(t, "A", got[0].Type)
	require.Equal(t, Marker(taken.ID), got[0].Comment)
	removed := e.audits(t, "dns.record.delete")
	require.Len(t, removed, 1)
	require.Contains(t, removed[0].Meta, `"replaced": true`)
	require.Contains(t, removed[0].Meta, "elsewhere.example.net")
	added := e.audits(t, "dns.record.create")
	require.NotEmpty(t, added)
}
