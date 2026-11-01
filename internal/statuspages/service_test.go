package statuspages

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
)

func TestPageHostnameValidationAndReservation(t *testing.T) {
	f := newFx(t)
	systemroute.Configure(&config.Config{PublicURL: "https://nebula.example.com", Installer: config.Installer{DashboardUpstream: "nebulactrl:8080"}})
	t.Cleanup(func() { systemroute.Configure(nil) })
	f.exec(`INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, 'app.acme.com', 80)`, f.api)

	v := f.createPage("Acme status", " Status.Acme.COM. ")
	require.Equal(t, "status.acme.com", *v.Hostname, "lowercased, trailing dot dropped")
	require.Equal(t, "acme-status", v.Slug)
	require.False(t, v.Published)
	require.Equal(t, "auto", v.Theme)
	require.True(t, v.ShowUptime)
	require.Empty(t, v.Components)

	// Invalid, reserved and taken hostnames, on create.
	for _, bad := range []string{"localhost", "203.0.113.9", "bad host.example.com", "*.acme.com"} {
		_, err := f.svc.Create(f.ctx, PageCreate{Title: "Bad", Hostname: bad})
		requireAPIError(t, err, 422, "invalid_hostname")
	}
	_, err := f.svc.Create(f.ctx, PageCreate{Title: "Dash", Hostname: "Nebula.Example.com"})
	requireAPIError(t, err, 409, "hostname_reserved")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Dup", Hostname: "STATUS.acme.com"})
	requireAPIError(t, err, 409, "hostname_conflict")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Domain", Hostname: "app.acme.com"})
	requireAPIError(t, err, 409, "hostname_conflict")

	// The same rules on update, and a refused update changes nothing.
	p2 := f.createPage("Second", "")
	for host, code := range map[string]string{"status.acme.com": "hostname_conflict", "app.acme.com": "hostname_conflict", "nebula.example.com": "hostname_reserved"} {
		_, err = f.svc.Update(f.ctx, p2.ID, PagePatch{Hostname: &host})
		requireAPIError(t, err, 409, code)
	}
	bad := "not a hostname"
	_, err = f.svc.Update(f.ctx, p2.ID, PagePatch{Hostname: &bad})
	requireAPIError(t, err, 422, "invalid_hostname")
	got, err := f.svc.Get(f.ctx, p2.ID)
	require.NoError(t, err)
	require.Nil(t, got.Hostname)

	// Both ways: the query service domains use sees the status hostname, and
	// a hostname that is only a service domain is not one.
	taken, err := f.q.StatusHostnameTaken(context.Background(), "status.acme.com")
	require.NoError(t, err)
	require.True(t, taken)
	taken, err = f.q.StatusHostnameTaken(context.Background(), "app.acme.com")
	require.NoError(t, err)
	require.False(t, taken)

	// Clearing the hostname frees it for another page.
	empty := ""
	cleared, err := f.svc.Update(f.ctx, v.ID, PagePatch{Hostname: &empty})
	require.NoError(t, err)
	require.Nil(t, cleared.Hostname)
	host := "status.acme.com"
	moved, err := f.svc.Update(f.ctx, p2.ID, PagePatch{Hostname: &host})
	require.NoError(t, err)
	require.Equal(t, host, *moved.Hostname)
}

// A hostname in a Cloudflare zone another team connected is refused with the
// same answer as a taken one (it never says whose it is); a pending zone claims
// nothing.
func TestPageHostnameInAnotherTeamsZoneIsUnavailable(t *testing.T) {
	f := newFx(t)
	rival, err := f.q.CreateWorkspace(context.Background(), "rival")
	require.NoError(t, err)
	f.exec(`INSERT INTO cloudflare_connections (workspace_id, token_enc, zones) VALUES ($1, decode('78', 'hex'), $2::jsonb)`,
		rival.ID, `[{"id":"z1","name":"claimed.example","status":"active"},{"id":"z2","name":"pending.example","status":"pending"}]`)

	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Squat", Hostname: "status.claimed.example"})
	requireAPIError(t, err, 409, "hostname_conflict")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "the hostname status.claimed.example is not available", ae.Message)

	p := f.createPage("Second", "")
	host := "claimed.example"
	_, err = f.svc.Update(f.ctx, p.ID, PagePatch{Hostname: &host})
	requireAPIError(t, err, 409, "hostname_conflict")

	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Pending", Hostname: "status.pending.example"})
	require.NoError(t, err)

	// The owner team may use its own zone.
	f.exec(`INSERT INTO cloudflare_connections (workspace_id, token_enc, zones) VALUES ($1, decode('78', 'hex'), $2::jsonb)`,
		f.ws, `[{"id":"z9","name":"mine.example","status":"active"}]`)
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Mine", Hostname: "status.mine.example"})
	require.NoError(t, err)
}

func TestPageSlugTitleAndThemeRules(t *testing.T) {
	f := newFx(t)
	a := f.createPage("Acme status", "")
	b := f.createPage("Acme status", "")
	require.Equal(t, "acme-status", a.Slug)
	require.Equal(t, "acme-status-2", b.Slug, "a derived slug that is taken gets a suffix")

	_, err := f.svc.Create(f.ctx, PageCreate{Title: "Third", Slug: "acme-status"})
	requireAPIError(t, err, 409, "slug_conflict")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Third", Slug: "Not A Slug"})
	requireAPIError(t, err, 422, "invalid_slug")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "   "})
	requireAPIError(t, err, 422, "invalid_title")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: strings.Repeat("x", maxTitleLen+1)})
	requireAPIError(t, err, 422, "invalid_title")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Themed", Theme: "neon"})
	requireAPIError(t, err, 422, "invalid_theme")

	other := "acme-status"
	_, err = f.svc.Update(f.ctx, b.ID, PagePatch{Slug: &other})
	requireAPIError(t, err, 409, "slug_conflict")

	no := false
	dark, title := "dark", "  Renamed  "
	v, err := f.svc.Update(f.ctx, b.ID, PagePatch{Theme: &dark, Title: &title, ShowUptime: &no})
	require.NoError(t, err)
	require.Equal(t, "Renamed", v.Title)
	require.Equal(t, "dark", v.Theme)
	require.False(t, v.ShowUptime)
	require.Equal(t, "acme-status-2", v.Slug, "untouched fields stay")
}

func TestPageAccessControl(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "")
	f.components(page.ID, f.api)

	t.Run("no principal", func(t *testing.T) {
		_, err := f.svc.List(context.Background())
		requireAPIError(t, err, 401, "unauthorized")
		_, err = f.svc.Create(context.Background(), PageCreate{Title: "x"})
		requireAPIError(t, err, 401, "unauthorized")
	})

	t.Run("a reader reads and cannot write", func(t *testing.T) {
		ctx := f.as([]string{policy.ScopeProjectsRead}, nil)
		list, err := f.svc.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, "svc-secret-api", list[0].Components[0].ServiceName, "an unrestricted reader sees the service")
		_, err = f.svc.Get(ctx, page.ID)
		require.NoError(t, err)

		_, err = f.svc.Create(ctx, PageCreate{Title: "Nope"})
		requireAPIError(t, err, 403, "forbidden")
		title := "Nope"
		_, err = f.svc.Update(ctx, page.ID, PagePatch{Title: &title})
		requireAPIError(t, err, 403, "forbidden")
		requireAPIError(t, f.svc.Delete(ctx, page.ID), 403, "forbidden")
		_, err = f.svc.SetComponents(ctx, page.ID, nil)
		requireAPIError(t, err, 403, "forbidden")
		_, err = f.svc.CreateIncident(ctx, IncidentCreate{Title: "x", Impact: ImpactNone, Body: "x"})
		requireAPIError(t, err, 403, "forbidden")
		_, err = f.svc.CreateMaintenance(ctx, MaintenanceCreate{Title: "x", StartsAt: f.now, EndsAt: f.now.Add(time.Hour)})
		requireAPIError(t, err, 403, "forbidden")
	})

	t.Run("a project-restricted token never writes and does not see other projects' names", func(t *testing.T) {
		ctx := f.as(policy.AllScopes, []uuid.UUID{uuid.New()})
		_, err := f.svc.Create(ctx, PageCreate{Title: "Nope"})
		requireAPIError(t, err, 403, "forbidden")
		got, err := f.svc.Get(ctx, page.ID)
		require.NoError(t, err)
		c := got.Components[0]
		require.Equal(t, f.api, c.ServiceID)
		require.NotEmpty(t, c.DisplayName, "the public name is not internal")
		require.Empty(t, c.ServiceName)
		require.Empty(t, c.EnvironmentName)
		require.Empty(t, c.ProjectName)

		own := f.as(policy.AllScopes, []uuid.UUID{f.project})
		got, err = f.svc.Get(own, page.ID)
		require.NoError(t, err)
		require.Equal(t, secretProject, got.Components[0].ProjectName, "a token restricted to the project sees it")
	})

	t.Run("another workspace's pages do not exist", func(t *testing.T) {
		_, err := f.svc.Get(f.ctx, f.foreignP)
		requireAPIError(t, err, 404, "not_found")
		title := "Hijack"
		_, err = f.svc.Update(f.ctx, f.foreignP, PagePatch{Title: &title})
		requireAPIError(t, err, 404, "not_found")
		requireAPIError(t, f.svc.Delete(f.ctx, f.foreignP), 404, "not_found")
		_, err = f.svc.SetComponents(f.ctx, f.foreignP, nil)
		requireAPIError(t, err, 404, "not_found")
		list, err := f.svc.List(f.ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
	})
}

func TestPublishPreconditionsAndRoutePushes(t *testing.T) {
	f := newFx(t)
	cp := f.controlPlane("203.0.113.10")
	events := f.bus.Subscribe(f.ctx)
	yes, no := true, false

	page := f.createPage("Acme", "")
	_, err := f.svc.Update(f.ctx, page.ID, PagePatch{Published: &yes})
	requireAPIError(t, err, 422, "publish_needs_hostname")

	host := "status.acme.com"
	v, err := f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &host})
	require.NoError(t, err)
	require.False(t, v.Published)
	require.Zero(t, f.routes.count(), "an unpublished page has no route: nothing to push")

	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Published: &yes})
	requireAPIError(t, err, 422, "publish_needs_components")
	got, err := f.svc.Get(f.ctx, page.ID)
	require.NoError(t, err)
	require.False(t, got.Published, "a refused publish changes nothing")

	f.components(page.ID, f.api)
	require.Zero(t, f.routes.count(), "components are not part of the route table")
	v = f.publish(page.ID)
	require.True(t, v.Published)
	require.Equal(t, []uuid.UUID{cp}, f.routes.calls, "publishing re-sends the control-plane server's routes")

	// Edits that do not change where the page is served push nothing.
	title := "Acme platform status"
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Title: &title, Published: &yes})
	require.NoError(t, err)
	require.Equal(t, 1, f.routes.count())

	// A published page keeps its preconditions.
	empty := ""
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &empty})
	requireAPIError(t, err, 422, "publish_needs_hostname")
	_, err = f.svc.SetComponents(f.ctx, page.ID, nil)
	requireAPIError(t, err, 422, "publish_needs_components")
	got, err = f.svc.Get(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, host, *got.Hostname)
	require.Len(t, got.Components, 1)

	// Moving the hostname of a published page re-sends the routes.
	moved := "status2.acme.com"
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &moved})
	require.NoError(t, err)
	require.Equal(t, 2, f.routes.count())

	// Unpublishing withdraws the route.
	v, err = f.svc.Update(f.ctx, page.ID, PagePatch{Published: &no})
	require.NoError(t, err)
	require.False(t, v.Published)
	require.Equal(t, 3, f.routes.count())
	// ...and clearing a hostname of an unpublished page does not.
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &empty})
	require.NoError(t, err)
	require.Equal(t, 3, f.routes.count())

	// Deleting a served page withdraws its route; deleting an unserved one does not.
	_, err = f.svc.Update(f.ctx, page.ID, PagePatch{Hostname: &host, Published: &yes})
	require.NoError(t, err)
	require.Equal(t, 4, f.routes.count())
	require.NoError(t, f.svc.Delete(f.ctx, page.ID))
	require.Equal(t, 5, f.routes.count())
	_, err = f.svc.Get(f.ctx, page.ID)
	requireAPIError(t, err, 404, "not_found")
	other := f.createPage("Other", "")
	require.NoError(t, f.svc.Delete(f.ctx, other.ID))
	require.Equal(t, 5, f.routes.count())

	// Every change announced itself.
	seen := map[string]int{}
	for _, e := range drain(events) {
		require.Equal(t, EventPageUpdated, e.Type)
		seen[e.ResourceID]++
	}
	require.Positive(t, seen[page.ID.String()])
	require.Positive(t, seen[other.ID.String()])

	require.Equal(t, 2, f.auditCount(ActionPageCreate))
	require.Equal(t, 2, f.auditCount(ActionPageDelete), "both deletes are audited")
	require.Positive(t, f.auditCount(ActionPageUpdate))
	require.Equal(t, 1, f.auditCount(ActionPageComponents))
}

func TestFailedRoutePushRollsTheChangeBack(t *testing.T) {
	f := newFx(t)
	f.controlPlane("203.0.113.10")
	host := "status.acme.com"
	page := f.createPage("Acme", host)
	f.components(page.ID, f.api)

	f.routes.err = errors.New("proxy unreachable")
	yes := true
	_, err := f.svc.Update(f.ctx, page.ID, PagePatch{Published: &yes})
	require.Error(t, err)
	got, gerr := f.svc.Get(f.ctx, page.ID)
	require.NoError(t, gerr)
	require.False(t, got.Published, "the page is not published when its route could not be sent")

	f.routes.err = nil
	v := f.publish(page.ID)
	require.True(t, v.Published)
}

func TestPublishNeedsALiveComponent(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "status.acme.com")
	f.components(page.ID, f.api)
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, f.api)

	yes := true
	_, err := f.svc.Update(f.ctx, page.ID, PagePatch{Published: &yes})
	requireAPIError(t, err, 422, "publish_needs_components")
}

func TestComponentsOwnershipNamesAndOrder(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "")

	v, err := f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{
		{ServiceID: f.web, DisplayName: "  Website ", GroupName: "Core"},
		{ServiceID: f.api},
		{ServiceID: f.database, DisplayName: "   ", GroupName: " Data "},
	})
	require.NoError(t, err)
	require.Len(t, v.Components, 3)
	names := func(v StatusPageView) []string {
		var out []string
		for _, c := range v.Components {
			out = append(out, c.DisplayName+"/"+c.GroupName)
		}
		return out
	}
	require.Equal(t, []string{"Website/Core", "svc-secret-api/", "svc-secret-db/Data"}, names(v),
		"order as given; a blank name defaults to the service's name")
	require.Equal(t, f.web, v.Components[0].ServiceID)
	require.Equal(t, "svc-secret-web", v.Components[0].ServiceName)
	require.Equal(t, secretProject, v.Components[0].ProjectName)

	// A service of another workspace is refused, and nothing changes.
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.api}, {ServiceID: f.foreign}})
	requireAPIError(t, err, 422, "unknown_service")
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: uuid.New()}})
	requireAPIError(t, err, 422, "unknown_service")
	got, err := f.svc.Get(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, names(v), names(got))

	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.api}, {ServiceID: f.api}})
	requireAPIError(t, err, 422, "duplicate_component")
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.api, DisplayName: strings.Repeat("n", maxDisplayNameLen+1)}})
	requireAPIError(t, err, 422, "invalid_display_name")
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.api, GroupName: strings.Repeat("g", maxGroupNameLen+1)}})
	requireAPIError(t, err, 422, "invalid_group_name")
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.api, DisplayName: "two\nlines"}})
	requireAPIError(t, err, 422, "invalid_display_name")
	ok, err := f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.api, DisplayName: strings.Repeat("é", maxDisplayNameLen)}})
	require.NoError(t, err, "60 characters, not 60 bytes")
	require.Len(t, ok.Components, 1)

	tooMany := make([]ComponentInput, MaxComponents+1)
	for i := range tooMany {
		tooMany[i] = ComponentInput{ServiceID: uuid.New()}
	}
	_, err = f.svc.SetComponents(f.ctx, page.ID, tooMany)
	requireAPIError(t, err, 422, "too_many_components")

	// A replacement reorders and drops.
	v, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.database, DisplayName: "DB"}, {ServiceID: f.web, DisplayName: "Web"}})
	require.NoError(t, err)
	require.Equal(t, []string{"DB/", "Web/"}, names(v))

	// A service deleted since disappears from the page and cannot be added again.
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, f.database)
	got, err = f.svc.Get(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"Web/"}, names(got))
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: f.database}})
	requireAPIError(t, err, 422, "unknown_service")

	require.Positive(t, f.auditCount(ActionPageComponents))
}

func TestComponentDefaultNameIsCutToTheLimit(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "")
	var envID uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT e.id FROM environments e JOIN services s ON s.environment_id = e.id WHERE s.id = $1`, f.api).Scan(&envID))
	long := strings.Repeat("ö", 80)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: long, Slug: "long", Kind: "app", SourceType: "image", Config: []byte(`{}`),
	})
	require.NoError(t, err)
	v, err := f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{{ServiceID: sv.ID}})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("ö", maxDisplayNameLen), v.Components[0].DisplayName)
}

func TestIncidentLifecycle(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "")
	page2 := f.createPage("Second", "")
	f.components(page.ID, f.api, f.web)
	events := f.bus.Subscribe(f.ctx)

	inc, err := f.svc.CreateIncident(f.ctx, IncidentCreate{
		Title: "  API errors ", Impact: ImpactMajor, Body: "We are looking into it.\r\nMore soon.",
		PageIDs: []uuid.UUID{page.ID, page.ID}, ServiceIDs: []uuid.UUID{f.api},
	})
	require.NoError(t, err)
	require.Equal(t, "API errors", inc.Title)
	require.Equal(t, IncidentInvestigating, inc.Status, "default status")
	require.Nil(t, inc.ResolvedAt)
	require.Equal(t, []uuid.UUID{page.ID}, inc.PageIDs, "pages are deduplicated")
	require.Equal(t, []uuid.UUID{f.api}, inc.ServiceIDs)
	require.Len(t, inc.Updates, 1, "the body is the first update")
	require.Equal(t, "We are looking into it.\nMore soon.", inc.Updates[0].Body)
	require.Equal(t, IncidentInvestigating, inc.Updates[0].Status)
	require.WithinDuration(t, f.now, inc.CreatedAt, time.Microsecond)
	require.Equal(t, []string{page.ID.String()}, resourceIDs(drain(events)))

	// Timeline: newest first; resolved sets resolved_at.
	f.now = f.now.Add(10 * time.Minute)
	inc, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: IncidentIdentified, Body: "A bad deploy."})
	require.NoError(t, err)
	require.Equal(t, IncidentIdentified, inc.Status)
	require.Nil(t, inc.ResolvedAt)
	require.Len(t, inc.Updates, 2)
	require.Equal(t, "A bad deploy.", inc.Updates[0].Body, "newest first")

	f.now = f.now.Add(20 * time.Minute)
	resolvedAt := f.now
	inc, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: IncidentResolved, Body: "Rolled back."})
	require.NoError(t, err)
	require.Equal(t, IncidentResolved, inc.Status)
	require.NotNil(t, inc.ResolvedAt)
	require.WithinDuration(t, resolvedAt, *inc.ResolvedAt, time.Microsecond)
	require.Len(t, inc.Updates, 3)

	// Another update after resolution reopens; resolving again sets a new time.
	f.now = f.now.Add(time.Hour)
	inc, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: IncidentMonitoring, Body: "Errors are back."})
	require.NoError(t, err)
	require.Nil(t, inc.ResolvedAt, "reopened")
	inc, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: IncidentResolved, Body: "Fixed for good."})
	require.NoError(t, err)
	require.WithinDuration(t, f.now, *inc.ResolvedAt, time.Microsecond)

	// Validation.
	_, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: "fixed", Body: "x"})
	requireAPIError(t, err, 422, "invalid_status")
	_, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: IncidentMonitoring, Body: " "})
	requireAPIError(t, err, 422, "invalid_body")
	_, err = f.svc.PostIncidentUpdate(f.ctx, uuid.New(), IncidentUpdateInput{Status: IncidentMonitoring, Body: "x"})
	requireAPIError(t, err, 404, "not_found")
	_, err = f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "x", Impact: "huge", Body: "x"})
	requireAPIError(t, err, 422, "invalid_impact")
	_, err = f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "x", Impact: ImpactMinor, Body: ""})
	requireAPIError(t, err, 422, "invalid_body")
	_, err = f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "x", Impact: ImpactMinor, Body: "x", Status: "later"})
	requireAPIError(t, err, 422, "invalid_status")
	_, err = f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "x", Impact: ImpactMinor, Body: "x", PageIDs: []uuid.UUID{f.foreignP}})
	requireAPIError(t, err, 422, "unknown_page")
	_, err = f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "x", Impact: ImpactMinor, Body: "x", ServiceIDs: []uuid.UUID{f.foreign}})
	requireAPIError(t, err, 422, "unknown_service")

	// An incident can be created already resolved.
	done, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "Past blip", Impact: ImpactNone, Status: IncidentResolved, Body: "It was brief.", PageIDs: []uuid.UUID{page2.ID}})
	require.NoError(t, err)
	require.NotNil(t, done.ResolvedAt)
	require.WithinDuration(t, f.now, *done.ResolvedAt, time.Microsecond)

	// Update the header: pages and services are replaced, an empty list clears.
	drain(events)
	title, impact := "API errors (EU)", ImpactCritical
	upd, err := f.svc.UpdateIncident(f.ctx, inc.ID, IncidentPatch{Title: &title, Impact: &impact, PageIDs: []uuid.UUID{page2.ID}, ServiceIDs: []uuid.UUID{f.web, f.api}})
	require.NoError(t, err)
	require.Equal(t, title, upd.Title)
	require.Equal(t, impact, upd.Impact)
	require.Equal(t, IncidentResolved, upd.Status, "the status only changes with an update")
	require.Equal(t, []uuid.UUID{page2.ID}, upd.PageIDs)
	require.ElementsMatch(t, []uuid.UUID{f.web, f.api}, upd.ServiceIDs)
	require.ElementsMatch(t, []string{page.ID.String(), page2.ID.String()}, resourceIDs(drain(events)),
		"the page the incident left is refreshed too")
	same, err := f.svc.UpdateIncident(f.ctx, inc.ID, IncidentPatch{})
	require.NoError(t, err)
	require.Equal(t, upd.PageIDs, same.PageIDs, "omitted lists stay")
	cleared, err := f.svc.UpdateIncident(f.ctx, inc.ID, IncidentPatch{PageIDs: []uuid.UUID{}, ServiceIDs: []uuid.UUID{}})
	require.NoError(t, err)
	require.Empty(t, cleared.PageIDs)
	require.Empty(t, cleared.ServiceIDs)
	badImpact := "huge"
	_, err = f.svc.UpdateIncident(f.ctx, inc.ID, IncidentPatch{Impact: &badImpact})
	requireAPIError(t, err, 422, "invalid_impact")
	_, err = f.svc.UpdateIncident(f.ctx, inc.ID, IncidentPatch{PageIDs: []uuid.UUID{f.foreignP}})
	requireAPIError(t, err, 422, "unknown_page")

	// Delete: gone with its timeline.
	require.NoError(t, f.svc.DeleteIncident(f.ctx, inc.ID))
	_, err = f.svc.GetIncident(f.ctx, inc.ID)
	requireAPIError(t, err, 404, "not_found")
	var left int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM status_incident_updates WHERE incident_id = $1`, inc.ID).Scan(&left))
	require.Zero(t, left)
	requireAPIError(t, f.svc.DeleteIncident(f.ctx, inc.ID), 404, "not_found")

	require.Equal(t, 2, f.auditCount(ActionIncidentCreate))
	require.Equal(t, 3, f.auditCount(ActionIncidentUpdate))
	require.Equal(t, 4, f.auditCount(ActionIncidentPost))
	require.Equal(t, 1, f.auditCount(ActionIncidentDelete))
}

func resourceIDs(evs []events.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.ResourceID)
	}
	return out
}

func TestIncidentListFiltersAndPagination(t *testing.T) {
	f := newFx(t)
	pa, pb := f.createPage("A", ""), f.createPage("B", "")

	mk := func(title, status string, pages ...uuid.UUID) StatusIncidentView {
		f.now = f.now.Add(time.Minute)
		v, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: title, Impact: ImpactMinor, Status: status, Body: "x", PageIDs: pages})
		require.NoError(t, err)
		return v
	}
	mk("one", IncidentInvestigating, pa.ID)
	mk("two", IncidentResolved, pa.ID, pb.ID)
	mk("three", IncidentMonitoring, pb.ID)
	mk("four", IncidentInvestigating)

	titles := func(p []StatusIncidentView) []string {
		var out []string
		for _, v := range p {
			out = append(out, v.Title)
		}
		return out
	}
	all, err := f.svc.ListIncidents(f.ctx, IncidentFilter{}, "", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"four", "three", "two", "one"}, titles(all.Items), "newest first")
	require.Empty(t, all.NextCursor)
	require.Len(t, all.Items[2].Updates, 1, "timelines come with the list")

	active, err := f.svc.ListIncidents(f.ctx, IncidentFilter{Status: "active"}, "", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"four", "three", "one"}, titles(active.Items))
	resolved, err := f.svc.ListIncidents(f.ctx, IncidentFilter{Status: IncidentResolved}, "", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"two"}, titles(resolved.Items))
	monitoring, err := f.svc.ListIncidents(f.ctx, IncidentFilter{Status: IncidentMonitoring}, "", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"three"}, titles(monitoring.Items))
	onB, err := f.svc.ListIncidents(f.ctx, IncidentFilter{PageID: &pb.ID}, "", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"three", "two"}, titles(onB.Items))
	onBActive, err := f.svc.ListIncidents(f.ctx, IncidentFilter{PageID: &pb.ID, Status: "active"}, "", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"three"}, titles(onBActive.Items))
	_, err = f.svc.ListIncidents(f.ctx, IncidentFilter{Status: "bogus"}, "", 50)
	requireAPIError(t, err, 422, "invalid_status")

	p1, err := f.svc.ListIncidents(f.ctx, IncidentFilter{}, "", 3)
	require.NoError(t, err)
	require.Equal(t, []string{"four", "three", "two"}, titles(p1.Items))
	require.NotEmpty(t, p1.NextCursor)
	p2, err := f.svc.ListIncidents(f.ctx, IncidentFilter{}, p1.NextCursor, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"one"}, titles(p2.Items))
	require.Empty(t, p2.NextCursor)
	_, err = f.svc.ListIncidents(f.ctx, IncidentFilter{}, "garbage", 3)
	requireAPIError(t, err, 400, "invalid_cursor")
}

func TestMaintenanceLifecycle(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "")
	events := f.bus.Subscribe(f.ctx)
	in := func(d time.Duration) time.Time { return f.now.Add(d) }

	w, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{
		Title: " Database upgrade ", Description: "Read-only for a few minutes.", StartsAt: in(2 * time.Hour), EndsAt: in(3 * time.Hour),
		ServiceIDs: []uuid.UUID{f.database, f.database}, PageIDs: []uuid.UUID{page.ID},
	})
	require.NoError(t, err)
	require.Equal(t, "Database upgrade", w.Title)
	require.Equal(t, PhaseUpcoming, w.Phase)
	require.Equal(t, []uuid.UUID{f.database}, w.ServiceIDs)
	require.Equal(t, []uuid.UUID{page.ID}, w.PageIDs)
	require.Nil(t, w.CanceledAt)
	require.Equal(t, []string{page.ID.String()}, resourceIDs(drain(events)))

	// Validation.
	_, err = f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "x", StartsAt: in(time.Hour), EndsAt: in(time.Hour)})
	requireAPIError(t, err, 422, "invalid_window")
	_, err = f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "x", StartsAt: in(-2 * time.Hour), EndsAt: in(-time.Hour)})
	requireAPIError(t, err, 422, "window_in_past")
	_, err = f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "", StartsAt: in(time.Hour), EndsAt: in(2 * time.Hour)})
	requireAPIError(t, err, 422, "invalid_title")
	_, err = f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "x", StartsAt: in(time.Hour), EndsAt: in(2 * time.Hour), ServiceIDs: []uuid.UUID{f.foreign}})
	requireAPIError(t, err, 422, "unknown_service")
	_, err = f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "x", StartsAt: in(time.Hour), EndsAt: in(2 * time.Hour), PageIDs: []uuid.UUID{f.foreignP}})
	requireAPIError(t, err, 422, "unknown_page")

	// Phases and the list filters.
	running, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "Running", StartsAt: in(-time.Hour), EndsAt: in(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, PhaseActive, running.Phase)
	require.Empty(t, running.PageIDs)
	require.Empty(t, running.ServiceIDs)
	titles := func(phase string, page *uuid.UUID) []string {
		p, err := f.svc.ListMaintenance(f.ctx, MaintenanceFilter{Phase: phase, PageID: page}, "", 50)
		require.NoError(t, err)
		var out []string
		for _, v := range p.Items {
			out = append(out, v.Title)
		}
		return out
	}
	require.Equal(t, []string{"Database upgrade", "Running"}, titles("", nil), "latest start first")
	require.Equal(t, []string{"Database upgrade"}, titles("upcoming", nil))
	require.Equal(t, []string{"Running"}, titles("active", nil))
	require.Empty(t, titles("past", nil))
	require.Equal(t, []string{"Database upgrade"}, titles("", &page.ID))
	_, err = f.svc.ListMaintenance(f.ctx, MaintenanceFilter{Phase: "soon"}, "", 10)
	requireAPIError(t, err, 422, "invalid_phase")

	// Update: times, text, links.
	drain(events)
	newTitle, newDesc := "DB upgrade", "Longer than planned."
	starts, ends := in(2*time.Hour+30*time.Minute), in(4*time.Hour)
	upd, err := f.svc.UpdateMaintenance(f.ctx, w.ID, MaintenancePatch{Title: &newTitle, Description: &newDesc, StartsAt: &starts, EndsAt: &ends, PageIDs: []uuid.UUID{}, ServiceIDs: []uuid.UUID{f.api, f.web}})
	require.NoError(t, err)
	require.Equal(t, newTitle, upd.Title)
	require.Equal(t, newDesc, upd.Description)
	require.True(t, upd.StartsAt.Equal(starts))
	require.True(t, upd.EndsAt.Equal(ends))
	require.Empty(t, upd.PageIDs)
	require.ElementsMatch(t, []uuid.UUID{f.api, f.web}, upd.ServiceIDs)
	require.Equal(t, []string{page.ID.String()}, resourceIDs(drain(events)), "the page it left is refreshed")
	early := in(5 * time.Hour)
	_, err = f.svc.UpdateMaintenance(f.ctx, w.ID, MaintenancePatch{StartsAt: &early})
	requireAPIError(t, err, 422, "invalid_window")

	// Cancel: sets canceled_at once, never again.
	f.now = f.now.Add(30 * time.Minute)
	canceledAt := f.now
	c, err := f.svc.CancelMaintenance(f.ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, PhaseCanceled, c.Phase)
	require.NotNil(t, c.CanceledAt)
	require.WithinDuration(t, canceledAt, *c.CanceledAt, time.Microsecond)
	f.now = f.now.Add(time.Minute)
	again, err := f.svc.CancelMaintenance(f.ctx, w.ID)
	require.NoError(t, err)
	require.WithinDuration(t, canceledAt, *again.CanceledAt, time.Microsecond, "canceling twice changes nothing")
	require.Equal(t, 1, f.auditCount(ActionMaintenanceStop))
	_, err = f.svc.UpdateMaintenance(f.ctx, w.ID, MaintenancePatch{Title: &newTitle})
	requireAPIError(t, err, 409, "window_canceled")
	require.Equal(t, []string{"DB upgrade"}, titles("past", nil), "a canceled window is past")
	require.Empty(t, titles("upcoming", nil))

	// An ended window can be neither canceled nor edited.
	f.now = f.now.Add(2 * time.Hour)
	_, err = f.svc.CancelMaintenance(f.ctx, running.ID)
	requireAPIError(t, err, 409, "window_ended")
	_, err = f.svc.UpdateMaintenance(f.ctx, running.ID, MaintenancePatch{Title: &newTitle})
	requireAPIError(t, err, 409, "window_ended")
	_, err = f.svc.CancelMaintenance(f.ctx, uuid.New())
	requireAPIError(t, err, 404, "not_found")

	// Delete.
	require.NoError(t, f.svc.DeleteMaintenance(f.ctx, w.ID))
	_, err = f.svc.GetMaintenance(f.ctx, w.ID)
	requireAPIError(t, err, 404, "not_found")
	requireAPIError(t, f.svc.DeleteMaintenance(f.ctx, w.ID), 404, "not_found")
	var links int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM maintenance_window_services WHERE window_id = $1`, w.ID).Scan(&links))
	require.Zero(t, links)

	require.Equal(t, 2, f.auditCount(ActionMaintenanceNew))
	require.Equal(t, 1, f.auditCount(ActionMaintenanceEdit))
	require.Equal(t, 1, f.auditCount(ActionMaintenanceTrash))
}

func TestDNSCheckReportsRecordsAndResolution(t *testing.T) {
	f := newFx(t)
	noHost := f.createPage("No host", "")
	_, err := f.svc.DNS(f.ctx, noHost.ID)
	requireAPIError(t, err, 422, "no_hostname")
	_, err = f.svc.DNS(f.ctx, f.foreignP)
	requireAPIError(t, err, 404, "not_found")

	page := f.createPage("Acme", "status.acme.com")

	// No control-plane server: nothing to compare with.
	got, err := f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, DNSUnknown, got.Status)
	require.Empty(t, got.Records)

	f.controlPlane("203.0.113.10")
	for _, tc := range []struct {
		name     string
		resolver fakeResolver
		status   string
		resolved []string
	}{
		{"no record", fakeResolver{err: &net.DNSError{Err: "no such host", IsNotFound: true}}, DNSMissing, []string{}},
		{"empty answer", fakeResolver{}, DNSMissing, []string{}},
		{"points here", fakeResolver{addrs: []string{"203.0.113.10"}}, DNSOK, []string{"203.0.113.10"}},
		{"points elsewhere", fakeResolver{addrs: []string{"198.51.100.7"}}, DNSMismatch, []string{"198.51.100.7"}},
		{"lookup fails", fakeResolver{err: errors.New("servfail")}, DNSUnknown, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.svc.resolver = tc.resolver
			got, err := f.svc.DNS(f.ctx, page.ID)
			require.NoError(t, err)
			require.Equal(t, "status.acme.com", got.Hostname)
			require.Equal(t, []StatusPageDNSRecord{{Type: "A", Name: "status.acme.com", Value: "203.0.113.10"}}, got.Records)
			require.Equal(t, tc.status, got.Status)
			require.Equal(t, tc.resolved, got.Resolved)
			require.NotEmpty(t, got.Message)
		})
	}

	// An IPv6 address needs an AAAA record.
	f.exec(`UPDATE servers SET public_ip = '203.0.113.10, 2001:db8::10' WHERE is_control_plane`)
	f.svc.resolver = fakeResolver{addrs: []string{"203.0.113.10", "2001:db8::10"}}
	got, err = f.svc.DNS(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, DNSOK, got.Status)
	require.Equal(t, []StatusPageDNSRecord{
		{Type: "A", Name: "status.acme.com", Value: "203.0.113.10"},
		{Type: "AAAA", Name: "status.acme.com", Value: "2001:db8::10"},
	}, got.Records)

	// A reader may check it.
	_, err = f.svc.DNS(f.as([]string{policy.ScopeProjectsRead}, nil), page.ID)
	require.NoError(t, err)
}
