package statuspages

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return tm
}

// state sets the current state of a service.
func (f *fx) state(service uuid.UUID, state, lastError string) {
	f.exec(`INSERT INTO uptime_state (service_id, workspace_id, state, last_error) VALUES ($1, $2, $3, $4)
	        ON CONFLICT (service_id) DO UPDATE SET state = EXCLUDED.state, last_error = EXCLUDED.last_error`,
		service, f.ws, state, lastError)
}

// day stores a day rollup (date as YYYY-MM-DD).
func (f *fx) day(service uuid.UUID, date string, checks, up, degraded, down, maintenance int) {
	f.exec(`INSERT INTO uptime_1d (service_id, bucket, checks, up, degraded, down, maintenance) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		service, at(f.t, date+"T00:00:00Z"), checks, up, degraded, down, maintenance)
}

// minute stores a minute rollup.
func (f *fx) minute(service uuid.UUID, ts string, checks, up, down, maintenance int) {
	f.exec(`INSERT INTO uptime_1m (service_id, bucket, checks, up, degraded, down, maintenance) VALUES ($1, $2, $3, $4, 0, $5, $6)`,
		service, at(f.t, ts), checks, up, down, maintenance)
}

// at moves the service clock.
func (f *fx) at(ts string) { f.now = at(f.t, ts) }

// published creates and publishes a page on status.acme.com.
func (f *fx) published(components ...ComponentInput) StatusPageView {
	f.t.Helper()
	p := f.createPage("Acme status", "status.acme.com")
	_, err := f.svc.SetComponents(f.ctx, p.ID, components)
	require.NoError(f.t, err)
	return f.publish(p.ID)
}

func findComponent(t *testing.T, p PublicPage, name string) PublicComponent {
	t.Helper()
	for _, g := range p.Groups {
		for _, c := range g.Components {
			if c.Name == name {
				return c
			}
		}
	}
	require.FailNow(t, "component not found", name)
	return PublicComponent{}
}

func dayOf(t *testing.T, c PublicComponent, date string) PublicDay {
	t.Helper()
	for _, d := range c.Days {
		if d.Date == date {
			return d
		}
	}
	require.FailNow(t, "day not found", date)
	return PublicDay{}
}

func TestPublicMapsStatesAvailabilityIncidentsAndMaintenance(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	page := f.createPage("Acme status", "status.acme.com")
	other := f.createPage("Other page", "other.acme.com")
	_, err := f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{
		{ServiceID: f.web, DisplayName: "Website", GroupName: "Core"},
		{ServiceID: f.api, DisplayName: "API"},
		{ServiceID: f.database, DisplayName: "Database", GroupName: "Core"},
	})
	require.NoError(t, err)
	f.publish(page.ID)

	// Current states (the error text is internal and must never show).
	f.state(f.api, "up", "")
	f.state(f.web, "degraded", "")
	f.state(f.database, "down", "dial tcp 10.0.0.5:5432: connection refused")

	// Availability: the window starts 2026-07-31 (89 days before today).
	f.day(f.api, "2026-07-30", 10, 0, 0, 10, 0) // outside the window
	f.day(f.api, "2026-07-31", 10, 10, 0, 0, 0) // first day of the window
	f.day(f.api, "2026-10-25", 100, 60, 0, 0, 40)
	f.day(f.api, "2026-10-27", 100, 90, 6, 4, 0)
	f.day(f.api, "2026-10-28", 1, 1, 0, 0, 0) // today, stale: the minute rollups win
	f.minute(f.api, "2026-10-28T00:00:00Z", 2, 2, 0, 0)
	f.minute(f.api, "2026-10-28T00:01:00Z", 2, 1, 1, 0)
	f.minute(f.api, "2026-10-27T23:59:00Z", 5, 0, 5, 0) // yesterday: not part of today's sum
	f.day(f.web, "2026-10-28", 10, 5, 5, 0, 0)          // today without minute rollups: the day row counts

	// Incidents.
	incident := func(title, impact, status string, pages []uuid.UUID, services ...uuid.UUID) StatusIncidentView {
		v, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: title, Impact: impact, Status: status, Body: title + " body", PageIDs: pages, ServiceIDs: services})
		require.NoError(t, err)
		return v
	}
	post := func(id uuid.UUID, status, body string) {
		_, err := f.svc.PostIncidentUpdate(f.ctx, id, IncidentUpdateInput{Status: status, Body: body})
		require.NoError(t, err)
	}
	f.at("2026-10-26T10:00:00Z")
	a := incident("Elevated API failures", ImpactMajor, IncidentInvestigating, []uuid.UUID{page.ID}, f.api)
	f.at("2026-10-26T11:00:00Z")
	post(a.ID, IncidentIdentified, "A bad deploy.")
	f.at("2026-10-18T09:00:00Z")
	b := incident("Slow website", ImpactMinor, IncidentInvestigating, []uuid.UUID{page.ID}, f.web)
	f.at("2026-10-18T11:00:00Z")
	post(b.ID, IncidentResolved, "Fixed.")
	f.at("2026-09-01T08:00:00Z")
	c := incident("Database blip", ImpactNone, IncidentInvestigating, []uuid.UUID{page.ID}, f.database)
	f.at("2026-09-01T10:00:00Z")
	post(c.ID, IncidentResolved, "Over.")
	f.at("2026-06-30T08:00:00Z")
	incident("Ancient history", ImpactMajor, IncidentResolved, []uuid.UUID{page.ID}, f.api)
	f.at("2026-10-27T08:00:00Z")
	incident("Other page incident", ImpactCritical, IncidentInvestigating, []uuid.UUID{other.ID}, f.api)
	incident("Internal only", ImpactCritical, IncidentInvestigating, nil, f.api)

	// Maintenance.
	f.at("2026-10-28T12:00:00Z")
	window := func(title, from, to string, pages []uuid.UUID, services ...uuid.UUID) MaintenanceWindowView {
		v, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: title, Description: title + " details", StartsAt: at(t, from), EndsAt: at(t, to), PageIDs: pages, ServiceIDs: services})
		require.NoError(t, err)
		return v
	}
	window("Running now", "2026-10-28T11:00:00Z", "2026-10-28T13:00:00Z", []uuid.UUID{page.ID}, f.api)
	window("Database upgrade", "2026-10-30T22:00:00Z", "2026-10-30T23:00:00Z", []uuid.UUID{page.ID}, f.database, f.web)
	window("Too far ahead", "2026-11-20T22:00:00Z", "2026-11-20T23:00:00Z", []uuid.UUID{page.ID}, f.api)
	window("Other page", "2026-10-29T22:00:00Z", "2026-10-29T23:00:00Z", []uuid.UUID{other.ID}, f.api)
	window("Not posted", "2026-10-29T22:00:00Z", "2026-10-29T23:00:00Z", nil, f.api)
	canceled := window("Canceled", "2026-10-29T10:00:00Z", "2026-10-29T11:00:00Z", []uuid.UUID{page.ID}, f.api)
	_, err = f.svc.CancelMaintenance(f.ctx, canceled.ID)
	require.NoError(t, err)
	var endedID uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO maintenance_windows (workspace_id, title, starts_at, ends_at) VALUES ($1, 'Ended', $2, $3) RETURNING id`,
		f.ws, at(t, "2026-10-27T10:00:00Z"), at(t, "2026-10-27T11:00:00Z")).Scan(&endedID))
	f.exec(`INSERT INTO maintenance_window_pages (window_id, page_id) VALUES ($1, $2)`, endedID, page.ID)

	got, err := f.svc.Public(ctx, "Status.Acme.com:443")
	require.NoError(t, err)

	require.Equal(t, "Acme status", got.Title)
	require.Equal(t, "auto", got.Theme)
	require.True(t, got.ShowUptime)
	require.WithinDuration(t, f.now, got.GeneratedAt, 0)
	require.Equal(t, StatePartialOutage, got.Overall, "one of three down; the major incident asks for the same")

	// Groups: ungrouped first, then groups in page order, components in page order.
	require.Len(t, got.Groups, 2)
	require.Equal(t, "", got.Groups[0].Name)
	require.Len(t, got.Groups[0].Components, 1)
	require.Equal(t, "API", got.Groups[0].Components[0].Name)
	require.Equal(t, "Core", got.Groups[1].Name)
	require.Len(t, got.Groups[1].Components, 2)
	require.Equal(t, "Website", got.Groups[1].Components[0].Name)
	require.Equal(t, "Database", got.Groups[1].Components[1].Name)

	api, web, db := findComponent(t, got, "API"), findComponent(t, got, "Website"), findComponent(t, got, "Database")
	require.Equal(t, StateOperational, api.State)
	require.Equal(t, StateDegraded, web.State)
	require.Equal(t, StateMajorOutage, db.State)

	// Every component has 90 days ending today.
	for _, c := range []PublicComponent{api, web, db} {
		require.Len(t, c.Days, UptimeDays)
		require.Equal(t, "2026-07-31", c.Days[0].Date)
		require.Equal(t, "2026-10-28", c.Days[UptimeDays-1].Date)
	}

	// Availability per day and over the window.
	require.InDelta(t, 1.0, *dayOf(t, api, "2026-07-31").Availability, 1e-9, "the first day of the window counts")
	y := dayOf(t, api, "2026-10-27")
	require.InDelta(t, 0.96, *y.Availability, 1e-9)
	require.Equal(t, 2, y.DownMinutes)
	m := dayOf(t, api, "2026-10-25")
	require.True(t, m.Maintenance, "maintenance checks flag the day")
	require.InDelta(t, 1.0, *m.Availability, 1e-9, "and are left out of the availability")
	today := dayOf(t, api, "2026-10-28")
	require.InDelta(t, 0.75, *today.Availability, 1e-9, "today's partial day comes from the minute rollups, not the stale day row")
	require.Equal(t, 1, today.DownMinutes)
	require.Nil(t, dayOf(t, api, "2026-10-26").Availability, "a day without checks has no data")
	require.InDelta(t, 1.0, *dayOf(t, web, "2026-10-28").Availability, 1e-9, "without minute rollups the day row is used")
	// (10 + 96 + 60 + 3) / (10 + 100 + 100 + 4 - 40); the day before the window is not part of it.
	require.InDelta(t, 169.0/174.0, *api.Availability, 1e-9)
	require.Nil(t, db.Availability, "no checks at all: no data")
	for _, d := range db.Days {
		require.Nil(t, d.Availability)
	}

	// Incidents on the day bars: titles per component and day.
	for _, d := range []string{"2026-10-26", "2026-10-27", "2026-10-28"} {
		require.Equal(t, []string{"Elevated API failures"}, dayOf(t, api, d).Incidents, d)
	}
	require.Empty(t, dayOf(t, api, "2026-10-25").Incidents)
	require.Equal(t, []string{"Slow website"}, dayOf(t, web, "2026-10-18").Incidents)
	require.Empty(t, dayOf(t, web, "2026-10-19").Incidents)
	require.Equal(t, []string{"Database blip"}, dayOf(t, db, "2026-09-01").Incidents, "resolved incidents stay on the bars for the whole window")
	require.Empty(t, dayOf(t, api, "2026-07-31").Incidents, "an incident older than the window is gone")

	// Active incidents: this page's open ones only.
	require.Len(t, got.ActiveIncidents, 1)
	ai := got.ActiveIncidents[0]
	require.Equal(t, "Elevated API failures", ai.Title)
	require.Equal(t, ImpactMajor, ai.Impact)
	require.Equal(t, IncidentIdentified, ai.Status)
	require.WithinDuration(t, at(t, "2026-10-26T10:00:00Z"), ai.StartedAt, 0)
	require.Nil(t, ai.ResolvedAt)
	require.Equal(t, []string{"API"}, ai.Components, "display names of this page's components")
	require.Len(t, ai.Updates, 2)
	require.Equal(t, IncidentIdentified, ai.Updates[0].Status, "newest first")
	require.Equal(t, "A bad deploy.", ai.Updates[0].Body)
	require.WithinDuration(t, at(t, "2026-10-26T11:00:00Z"), ai.Updates[0].At, 0)
	require.Equal(t, "Elevated API failures body", ai.Updates[1].Body)

	// Past incidents: resolved within 30 days.
	require.Len(t, got.PastIncidents, 1)
	pi := got.PastIncidents[0]
	require.Equal(t, "Slow website", pi.Title)
	require.Equal(t, IncidentResolved, pi.Status)
	require.NotNil(t, pi.ResolvedAt)
	require.WithinDuration(t, at(t, "2026-10-18T11:00:00Z"), *pi.ResolvedAt, 0)
	require.Equal(t, []string{"Website"}, pi.Components)
	require.Len(t, pi.Updates, 2)

	// Maintenance: active or within 14 days, soonest first.
	require.Len(t, got.Maintenance, 2)
	require.Equal(t, "Running now", got.Maintenance[0].Title)
	require.True(t, got.Maintenance[0].Active)
	require.Equal(t, []string{"API"}, got.Maintenance[0].Components)
	require.Equal(t, "Running now details", got.Maintenance[0].Description)
	require.Equal(t, "Database upgrade", got.Maintenance[1].Title)
	require.False(t, got.Maintenance[1].Active)
	require.Equal(t, []string{"Website", "Database"}, got.Maintenance[1].Components, "in page order")
	require.WithinDuration(t, at(t, "2026-10-30T22:00:00Z"), got.Maintenance[1].StartsAt, 0)

	// Nothing internal in the output.
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	text := string(raw)
	require.False(t, regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}`).MatchString(text), "no IDs")
	for _, secret := range []string{"svc-secret", secretProject, secretEnv, "10.0.0.5", "refused", "connection", "dial tcp", "status.acme.com", "other.acme.com", "Other page", "Internal only", "Ancient history", "Not posted", "Too far ahead", "Canceled"} {
		require.NotContains(t, text, secret)
	}
	require.NotContains(t, strings.ToLower(text), "error")
}

func TestPublicTypesHoldNothingInternal(t *testing.T) {
	// Every field of the view model is checked by name: no IDs, hostnames,
	// servers, services, errors or probe details can be added to it by accident.
	forbidden := regexp.MustCompile(`(?i)(^id$|id$|ids$|host|server|service|error|latency|replica|project|environment|deploy|user|author|created)`)
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(time.Time{}) || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			field := typ.Field(i)
			require.False(t, forbidden.MatchString(field.Name), "%s.%s looks internal", path, field.Name)
			walk(field.Type, path+"."+field.Name)
		}
	}
	walk(reflect.TypeOf(PublicPage{}), "PublicPage")
	require.Greater(t, len(seen), 5, "the walk reached the nested types")
}

func TestPublicNotFound(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()

	_, err := f.svc.Public(ctx, "status.acme.com")
	require.ErrorIs(t, err, ErrNotFound, "no page at all")

	p := f.createPage("Acme status", "status.acme.com")
	f.components(p.ID, f.api)
	_, err = f.svc.Public(ctx, "status.acme.com")
	require.ErrorIs(t, err, ErrNotFound, "an unpublished page is not served")

	f.publish(p.ID)
	got, err := f.svc.Public(ctx, "STATUS.acme.com.")
	require.NoError(t, err)
	require.Equal(t, "Acme status", got.Title)

	for _, host := range []string{"", "other.acme.com", "acme.com", "status.acme.com.evil.test", "   "} {
		_, err = f.svc.Public(ctx, host)
		require.ErrorIs(t, err, ErrNotFound, host)
	}

	no := false
	_, err = f.svc.Update(f.ctx, p.ID, PagePatch{Published: &no})
	require.NoError(t, err)
	_, err = f.svc.Public(ctx, "status.acme.com")
	require.ErrorIs(t, err, ErrNotFound, "unpublishing withdraws the page")

	f.publish(p.ID)
	require.NoError(t, f.svc.Delete(f.ctx, p.ID))
	_, err = f.svc.Public(ctx, "status.acme.com")
	require.ErrorIs(t, err, ErrNotFound, "a deleted page")
}

func TestPublicWithoutDataOrWithPausedServicesIsUnknown(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	f.published(ComponentInput{ServiceID: f.api, DisplayName: "API"}, ComponentInput{ServiceID: f.web, DisplayName: "Web"})

	got, err := f.svc.Public(ctx, "status.acme.com")
	require.NoError(t, err)
	require.Equal(t, StateUnknown, got.Overall, "nothing probed yet")
	require.Equal(t, StateUnknown, findComponent(t, got, "API").State)
	require.Nil(t, findComponent(t, got, "API").Availability)

	f.state(f.api, "paused", "")
	f.state(f.web, "unknown", "agent offline")
	got, err = f.svc.Public(ctx, "status.acme.com")
	require.NoError(t, err)
	require.Equal(t, StateUnknown, got.Overall)
	require.Equal(t, StateUnknown, findComponent(t, got, "API").State, "paused is not shown as operational")
	require.NotContains(t, strings.ToLower(mustJSON(t, got)), "offline")

	f.state(f.web, "up", "")
	got, err = f.svc.Public(ctx, "status.acme.com")
	require.NoError(t, err)
	require.Equal(t, StateOperational, got.Overall, "an unknown component does not worsen the page")
}

func TestPublicShowsServicesOfAGoneServerAsDown(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	var server uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO servers (workspace_id, name, status, last_seen_at) VALUES ($1, 'app-1', 'offline', $2) RETURNING id`,
		f.ws, f.now.Add(-2*time.Minute)).Scan(&server))
	f.exec(`UPDATE services SET server_id = $1 WHERE id = $2`, server, f.api)
	f.published(ComponentInput{ServiceID: f.api, DisplayName: "API"}, ComponentInput{ServiceID: f.web, DisplayName: "Web"})
	f.state(f.api, "unknown", "")
	f.state(f.web, "up", "")

	public := func() PublicPage {
		t.Helper()
		got, err := f.svc.Public(ctx, "status.acme.com")
		require.NoError(t, err)
		return got
	}

	got := public()
	require.Equal(t, StateUnknown, findComponent(t, got, "API").State, "silent for less than serverGoneAfter")
	require.Equal(t, StateOperational, got.Overall)

	f.exec(`UPDATE servers SET last_seen_at = $1 WHERE id = $2`, f.now.Add(-6*time.Minute), server)
	got = public()
	require.Equal(t, StateMajorOutage, findComponent(t, got, "API").State, "the server is gone")
	require.Equal(t, StatePartialOutage, got.Overall)

	w, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "Reboot", StartsAt: f.now.Add(-time.Minute), EndsAt: f.now.Add(time.Hour), ServiceIDs: []uuid.UUID{f.api}})
	require.NoError(t, err)
	require.Equal(t, StateMaintenance, findComponent(t, public(), "API").State, "an active window covers the service")
	_, err = f.svc.CancelMaintenance(f.ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, StateMajorOutage, findComponent(t, public(), "API").State, "a canceled window no longer covers it")

	f.exec(`UPDATE servers SET status = 'revoked' WHERE id = $1`, server)
	require.Equal(t, StateUnknown, findComponent(t, public(), "API").State, "a revoked server is not a gone one")

	f.exec(`UPDATE servers SET status = 'offline' WHERE id = $1`, server)
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, f.web)
	got = public()
	require.Equal(t, StateMajorOutage, got.Overall, "the only component left is down")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestPublicOverallFollowsMaintenanceWindowsAndIncidentImpact(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	page := f.published(ComponentInput{ServiceID: f.api, DisplayName: "API"})
	f.state(f.api, "up", "")

	overall := func() string {
		got, err := f.svc.Public(ctx, "status.acme.com")
		require.NoError(t, err)
		return got.Overall
	}
	require.Equal(t, StateOperational, overall())

	// The service's own maintenance state.
	f.state(f.api, "maintenance", "")
	require.Equal(t, StateMaintenance, overall())
	f.state(f.api, "up", "")

	// An active window posted to the page.
	w, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "Upgrade", StartsAt: f.now.Add(-time.Minute), EndsAt: f.now.Add(time.Hour), PageIDs: []uuid.UUID{page.ID}})
	require.NoError(t, err)
	require.Equal(t, StateMaintenance, overall(), "an active window with nothing worse")
	_, err = f.svc.CancelMaintenance(f.ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, StateOperational, overall(), "a canceled window no longer counts")

	// An open incident by impact; resolving it ends its effect.
	inc, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "Slow", Impact: ImpactMinor, Body: "Slow.", PageIDs: []uuid.UUID{page.ID}})
	require.NoError(t, err)
	require.Equal(t, StateDegraded, overall())
	impact := ImpactCritical
	_, err = f.svc.UpdateIncident(f.ctx, inc.ID, IncidentPatch{Impact: &impact})
	require.NoError(t, err)
	require.Equal(t, StateMajorOutage, overall())
	_, err = f.svc.PostIncidentUpdate(f.ctx, inc.ID, IncidentUpdateInput{Status: IncidentResolved, Body: "Fine."})
	require.NoError(t, err)
	require.Equal(t, StateOperational, overall())

	// A service deleted since is no longer a component.
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, f.api)
	got, err := f.svc.Public(ctx, "status.acme.com")
	require.NoError(t, err)
	require.Empty(t, got.Groups)
	require.Equal(t, StateUnknown, got.Overall)
}

func TestPublicTodayCountsOnlyTodaysMinutes(t *testing.T) {
	f := newFx(t)
	f.published(ComponentInput{ServiceID: f.api, DisplayName: "API"})
	f.state(f.api, "up", "")
	f.at("2026-10-28T00:30:00Z")

	// Just before midnight belongs to yesterday (not in the minute sum), just after to today.
	f.minute(f.api, "2026-10-27T23:59:00Z", 2, 0, 2, 0)
	f.minute(f.api, "2026-10-28T00:00:00Z", 2, 2, 0, 0)
	got, err := f.svc.Public(context.Background(), "status.acme.com")
	require.NoError(t, err)
	c := findComponent(t, got, "API")
	today := c.Days[UptimeDays-1]
	require.Equal(t, "2026-10-28", today.Date)
	require.InDelta(t, 1.0, *today.Availability, 1e-9)
	require.Nil(t, c.Days[UptimeDays-2].Availability, "the minute rollups do not make yesterday's day")
}
