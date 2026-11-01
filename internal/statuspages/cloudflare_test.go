package statuspages

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// fakeDNS records what the status pages tell the Cloudflare integration.
type fakeDNS struct {
	mu      sync.Mutex
	changed []hostChange
	removed int
	err     error
}

type hostChange struct {
	page  uuid.UUID
	fresh bool
}

func (f *fakeDNS) StatusPageHostChanged(_ context.Context, _ pgx.Tx, pageID uuid.UUID, fresh bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.changed = append(f.changed, hostChange{pageID, fresh})
	return nil
}

func (f *fakeDNS) StatusPageRemoved(context.Context, pgx.Tx, uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.removed++
	return nil
}

func (f *fakeDNS) calls() []hostChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hostChange(nil), f.changed...)
}

func TestPageHostnameChangesAreToldToTheDNSIntegration(t *testing.T) {
	f := newFx(t)
	fd := &fakeDNS{}
	f.svc.dns = fd

	// No hostname, nothing to keep in DNS.
	page := f.createPage("Acme", "")
	require.Empty(t, fd.calls())

	// The first hostname is fresh: it takes the team's defaults.
	host := "status.acme.com"
	_, err := f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &host})
	require.NoError(t, err)
	require.Equal(t, []hostChange{{page.ID, true}}, fd.calls())

	// Other changes do not touch DNS.
	title := "Acme status"
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Title: &title})
	require.NoError(t, err)
	same := host
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &same})
	require.NoError(t, err)
	require.Len(t, fd.calls(), 1)

	// A new hostname is a change, not fresh; removing it is told too.
	other := "status.acme.org"
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &other})
	require.NoError(t, err)
	none := ""
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &none})
	require.NoError(t, err)
	require.Equal(t, []hostChange{{page.ID, true}, {page.ID, false}, {page.ID, false}}, fd.calls())

	// A page created with a hostname is fresh.
	created := f.createPage("Created", "created.acme.com")
	require.Equal(t, hostChange{created.ID, true}, fd.calls()[3])

	// Deleting a page that had a hostname tells the integration (it collects the records);
	// deleting one without does not.
	require.NoError(t, f.svc.Delete(f.ctx, created.ID))
	require.Equal(t, 1, fd.removed)
	require.NoError(t, f.svc.Delete(f.ctx, page.ID))
	require.Equal(t, 1, fd.removed)
}

func TestAFailingDNSHookRollsTheChangeBack(t *testing.T) {
	f := newFx(t)
	fd := &fakeDNS{}
	f.svc.dns = fd
	page := f.createPage("Acme", "")

	fd.err = errors.New("database is gone")
	host := "status.acme.com"
	_, err := f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &host})
	require.Error(t, err)
	got, err := f.svc.Get(f.ctx, page.ID)
	require.NoError(t, err)
	require.Nil(t, got.Hostname, "the hostname is not stored when its DNS could not follow")

	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Other", Hostname: "other.acme.com"})
	require.Error(t, err)
	pages, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	require.Len(t, pages, 1)
}

// prefillFx adds what the project start needs on top of the fixture: domains,
// a staging environment and services that must not be picked.
type prefillFx struct {
	*fx
	other uuid.UUID // a project of another workspace
}

func newPrefillFx(t *testing.T) *prefillFx {
	t.Helper()
	f := newFx(t)
	var other uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM projects WHERE workspace_id <> $1`, f.ws).Scan(&other))
	return &prefillFx{fx: f, other: other}
}

func (f *prefillFx) service(env uuid.UUID, name string) uuid.UUID {
	f.t.Helper()
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env, Name: name, Slug: name, Kind: "app", SourceType: "image",
		Config: []byte(`{"image":"nginx:1","port":80}`),
	})
	require.NoError(f.t, err)
	return sv.ID
}

func (f *prefillFx) domain(svc uuid.UUID, host string) uuid.UUID {
	f.t.Helper()
	d, err := f.q.InsertDomain(context.Background(), db.InsertDomainParams{ServiceID: svc, Hostname: host, TargetPort: 80})
	require.NoError(f.t, err)
	return d.ID
}

func (f *prefillFx) env(project uuid.UUID, name string, production bool) uuid.UUID {
	f.t.Helper()
	e, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: project, Name: name, Slug: name, IsProduction: production})
	require.NoError(f.t, err)
	return e.ID
}

func (f *prefillFx) names(v StatusPageView) []string {
	out := make([]string, 0, len(v.Components))
	for _, c := range v.Components {
		out = append(out, c.DisplayName)
	}
	return out
}

func TestStartFromProjectPrefillsProductionServicesWithAPublicDomain(t *testing.T) {
	f := newPrefillFx(t)
	var prod uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM environments WHERE project_id = $1 AND is_production`, f.project).Scan(&prod))

	// Production, public: picked (in name order, whatever the order they were made in).
	f.domain(f.web, "www.acme.com")
	f.domain(f.api, "api.acme.com")
	f.domain(f.api, "api2.acme.com") // a second domain does not list the service twice
	// Production without a public domain: the database has none.
	// A redirect is not a public site.
	redirect := f.service(prod, "svc-redirect")
	redirectDomain := f.domain(redirect, "old.acme.com")
	f.exec(`UPDATE domains SET kind = 'redirect', redirect_to = 'www.acme.com' WHERE id = $1`, redirectDomain)
	// An IP allow list is not public either.
	private := f.service(prod, "svc-private")
	privateDomain := f.domain(private, "internal.acme.com")
	f.exec(`UPDATE domains SET options = '{"access":{"allowCidrs":["10.0.0.0/8"]}}' WHERE id = $1`, privateDomain)
	// Tailnet-only services never route their public domains.
	tailnet := f.service(prod, "svc-tailnet")
	f.domain(tailnet, "tailnet.acme.com")
	f.exec(`INSERT INTO service_tailnet (service_id, workspace_id, provider, hostname, applied_hide_public) VALUES ($1, $2, 'tailscale', 'tn', true)`, tailnet, f.ws)
	// A deleted service is gone.
	gone := f.service(prod, "svc-gone")
	f.domain(gone, "gone.acme.com")
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, gone)
	// Another environment of the project, even with a public domain, is not production.
	staging := f.env(f.project, "staging", false)
	f.domain(f.service(staging, "svc-staging"), "staging.acme.com")
	// Another project of the workspace is not the one asked for.
	otherProject, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws, Name: "blog", Slug: "blog"})
	require.NoError(t, err)
	f.domain(f.service(f.env(otherProject.ID, "production", true), "svc-blog"), "blog.acme.com")

	v, err := f.svc.Create(f.ctx, PageCreate{Title: "Acme", FromProjectID: &f.project})
	require.NoError(t, err)
	require.Equal(t, []string{"svc-secret-api", "svc-secret-web"}, f.names(v), "production services with a public domain, by name")
	for _, c := range v.Components {
		require.Empty(t, c.GroupName)
		require.Empty(t, c.URL)
	}
	require.False(t, v.Published)
	require.Nil(t, v.Hostname)

	// The page is a normal page afterwards: the list can be edited.
	again, err := f.svc.SetComponents(f.ctx, v.ID, []ComponentInput{{ServiceID: f.web, DisplayName: "Website", GroupName: "Customers"}})
	require.NoError(t, err)
	require.Equal(t, []string{"Website"}, f.names(again))

	// The project's other pages are unaffected, and the start is audited.
	require.Equal(t, 1, f.auditCount(ActionPageCreate))
	var meta string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT metadata::text FROM audit_events WHERE action = $1`, ActionPageCreate).Scan(&meta))
	require.Contains(t, meta, f.project.String())

	// Each project starts its own page; one without such services starts an empty page.
	blog, err := f.svc.Create(f.ctx, PageCreate{Title: "Blog only", FromProjectID: &otherProject.ID})
	require.NoError(t, err)
	require.Equal(t, []string{"svc-blog"}, f.names(blog))
	bare, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws, Name: "bare", Slug: "bare"})
	require.NoError(t, err)
	none, err := f.svc.Create(f.ctx, PageCreate{Title: "Bare", FromProjectID: &bare.ID})
	require.NoError(t, err)
	require.Empty(t, none.Components)
}

func TestStartFromProjectNeedsAProjectOfTheWorkspace(t *testing.T) {
	f := newPrefillFx(t)

	_, err := f.svc.Create(f.ctx, PageCreate{Title: "Foreign", FromProjectID: &f.other})
	requireAPIError(t, err, 422, "unknown_project")
	unknown := uuid.New()
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Unknown", FromProjectID: &unknown})
	requireAPIError(t, err, 422, "unknown_project")

	// Nothing was created.
	pages, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	require.Empty(t, pages)
}

func TestDNSCheckOfAProxiedPageExpectsCloudflare(t *testing.T) {
	f := newFx(t)
	f.controlPlane("203.0.113.10")
	page := f.createPage("Acme", "status.acme.com")
	f.exec(`UPDATE status_pages SET cf_zone_id = 'z1', cf_zone_name = 'acme.com', cf_proxied = true WHERE id = $1`, page.ID)
	check := func(addrs ...string) StatusPageDNSCheck {
		t.Helper()
		f.svc.resolver = fakeResolver{addrs: addrs}
		got, err := f.svc.DNS(f.ctx, page.ID)
		require.NoError(t, err)
		return got
	}

	// A proxied record resolves to Cloudflare's addresses: that is correct, not a mismatch.
	got := check("104.16.1.1", "2606:4700::6810:1")
	require.Equal(t, DNSOK, got.Status)
	require.True(t, got.Proxied)
	require.Contains(t, got.Message, "resolves through Cloudflare")
	require.Equal(t, []StatusPageDNSRecord{{Type: "A", Name: "status.acme.com", Value: "203.0.113.10"}}, got.Records, "the records behind the proxy are still shown")
	require.Equal(t, []string{"104.16.1.1", "2606:4700::6810:1"}, got.Resolved)

	// Not proxied yet (the record still points at the server): ok, but not proxied.
	got = check("203.0.113.10")
	require.Equal(t, DNSOK, got.Status)
	require.False(t, got.Proxied)
	require.Contains(t, got.Message, "points to this server")

	// Cloudflare's addresses next to another one are not "proxied": the extra address is reported.
	got = check("104.16.1.1", "198.51.100.7")
	require.Equal(t, DNSMismatch, got.Status)
	require.False(t, got.Proxied)

	// An address that is neither the server's nor Cloudflare's is a mismatch.
	got = check("198.51.100.7")
	require.Equal(t, DNSMismatch, got.Status)

	// Nothing resolves: missing, whatever the setting.
	f.svc.resolver = fakeResolver{}
	got, err := f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, DNSMissing, got.Status)
	require.False(t, got.Proxied)

	// A page that is not proxied by NebulaCtrl cannot vouch for Cloudflare's addresses.
	f.exec(`UPDATE status_pages SET cf_proxied = false WHERE id = $1`, page.ID)
	got = check("104.16.1.1")
	require.Equal(t, DNSMismatch, got.Status)
	require.False(t, got.Proxied)
	f.exec(`UPDATE status_pages SET cf_proxied = true, cf_zone_id = '' WHERE id = $1`, page.ID)
	got = check("104.16.1.1")
	require.Equal(t, DNSMismatch, got.Status, "a proxy setting without a matched zone is not in effect")

	// Without a control-plane address there is nothing to compare with.
	f.exec(`UPDATE status_pages SET cf_zone_id = 'z1' WHERE id = $1`, page.ID)
	f.exec(`UPDATE servers SET public_ip = NULL WHERE is_control_plane`)
	got = check("104.16.1.1")
	require.Equal(t, DNSUnknown, got.Status)
	require.False(t, got.Proxied)
}

func TestDNSCheckNeverTellsAManagedPageToCreateRecords(t *testing.T) {
	f := newFx(t)
	f.controlPlane("203.0.113.10")
	page := f.createPage("Acme", "status.acme.com")
	notFound := fakeResolver{err: &net.DNSError{Err: "no such host", IsNotFound: true}}

	// No Cloudflare zone: the records are for the user to create.
	f.svc.resolver = notFound
	got, err := f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, DNSMissing, got.Status)
	require.False(t, got.CloudflareRecord)
	require.Equal(t, []StatusPageDNSRecord{{Type: "A", Name: "status.acme.com", Value: "203.0.113.10"}}, got.Records)
	require.NotContains(t, got.Message, cloudflareLagMessage)

	// The zone is matched but the record is not there yet: still the user's to fix, not a cache.
	f.exec(`UPDATE status_pages SET cf_zone_id = 'z1', cf_zone_name = 'acme.com', cf_status = 'missing' WHERE id = $1`, page.ID)
	got, err = f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.False(t, got.CloudflareRecord)
	require.NotEqual(t, cloudflareLagMessage, got.Message)

	// Cloudflare's API confirmed the record: a lookup that shows nothing is waiting on DNS caches.
	for _, state := range []string{"managed", "present"} {
		f.exec(`UPDATE status_pages SET cf_status = $2 WHERE id = $1`, page.ID, state)
		got, err = f.svc.DNS(f.ctx, page.ID)
		require.NoError(t, err)
		require.Equal(t, DNSMissing, got.Status)
		require.True(t, got.CloudflareRecord)
		require.Equal(t, "Cloudflare has the record. Some DNS caches can take up to 30 minutes to see a new name.", got.Message)
	}

	// A stale address next to a confirmed record is the same wait.
	f.svc.resolver = fakeResolver{addrs: []string{"198.51.100.7"}}
	got, err = f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, DNSMismatch, got.Status)
	require.Equal(t, cloudflareLagMessage, got.Message)

	// Once it resolves, the check is ok and says nothing about waiting.
	f.svc.resolver = fakeResolver{addrs: []string{"203.0.113.10"}}
	got, err = f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, DNSOK, got.Status)
	require.True(t, got.CloudflareRecord)
	require.NotContains(t, got.Message, "caches")
}

func TestDNSCheckReportsWhenTheNameLastChanged(t *testing.T) {
	f := newFx(t)
	f.controlPlane("203.0.113.10")
	page := f.createPage("Acme", "status.acme.com")
	f.svc.resolver = fakeResolver{addrs: []string{"203.0.113.10"}}

	// Without a record of ours, the page's own edit is the change.
	got, err := f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.WithinDuration(t, page.UpdatedAt, got.NameChangedAt, time.Second)

	// A record NebulaCtrl created or changed later moves it.
	f.exec(`INSERT INTO cloudflare_records (zone_id, record_id, workspace_id, owner_kind, owner_id, hostname, type, content, proxied, updated_at)
		VALUES ('z1', 'r1', $1, 'status_page', $2, 'status.acme.com', 'A', '203.0.113.10', false, now() + interval '5 minutes')`, f.ws, page.ID)
	got, err = f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.True(t, got.NameChangedAt.After(page.UpdatedAt.Add(4*time.Minute)))
}
