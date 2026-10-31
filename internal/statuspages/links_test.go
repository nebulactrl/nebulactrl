package statuspages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCheckComponentURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"   ":                              "",
		"https://example.com":              "https://example.com",
		" https://example.com/docs?a=1#x ": "https://example.com/docs?a=1#x",
		"HTTPS://Example.com/Docs":         "HTTPS://Example.com/Docs",
		"https://example.com:8443/status":  "https://example.com:8443/status",
		"https://[2001:db8::1]/health":     "https://[2001:db8::1]/health",
		"https://" + strings.Repeat("a", 60) + ".example.com/" + strings.Repeat("p", 200): "https://" + strings.Repeat("a", 60) + ".example.com/" + strings.Repeat("p", 200),
	} {
		got, err := checkComponentURL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{
		"http://example.com",
		"example.com",
		"//example.com",
		"/docs",
		"ftp://example.com",
		"javascript:alert(1)",
		"data:text/html,x",
		"https://",
		"https:///path",
		"https://user:secret@example.com",
		"https://exa mple.com",
		"https://example.com/\nx",
		"https://example.com/" + strings.Repeat("p", maxURLLen),
	} {
		_, err := checkComponentURL(bad)
		requireAPIError(t, err, 422, "invalid_url")
	}
}

func TestOpaqueKeyIsStableOpaqueAndPerKind(t *testing.T) {
	id := uuid.MustParse("3f1c2d9e-8a47-4b61-9c0d-5e7a1b2c3d4e")
	sum := sha256.Sum256([]byte("incident:" + id.String()))
	want := hex.EncodeToString(sum[:])[:16]
	require.Equal(t, want, opaqueKey("incident", id))
	require.Equal(t, opaqueKey("incident", id), opaqueKey("incident", id), "stable")
	require.Regexp(t, `^[0-9a-f]{16}$`, opaqueKey("maintenance", id))
	require.NotEqual(t, opaqueKey("incident", id), opaqueKey("maintenance", id), "the kind is part of the hash")
	require.NotEqual(t, opaqueKey("incident", id), opaqueKey("incident", uuid.New()))
	for _, k := range []string{opaqueKey("incident", id), opaqueKey("maintenance", id)} {
		require.NotContains(t, id.String(), k)
		require.NotContains(t, strings.ReplaceAll(id.String(), "-", ""), k)
	}
}

func TestFooterAndComponentLinksInTheAPI(t *testing.T) {
	f := newFx(t)
	v, err := f.svc.Create(f.ctx, PageCreate{Title: "Acme", Footer: "  Contact ops@acme.com\r\nThanks  "})
	require.NoError(t, err)
	require.Equal(t, "Contact ops@acme.com\nThanks", v.Footer, "plain text, CRLF normalized, trimmed")

	long := strings.Repeat("x", maxFooterLen+1)
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Long", Footer: long})
	requireAPIError(t, err, 422, "invalid_footer")
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Ctl", Footer: "bell\x07"})
	requireAPIError(t, err, 422, "invalid_footer")
	ok := strings.Repeat("é", maxFooterLen)
	_, err = f.svc.Create(f.ctx, PageCreate{Title: "Max", Footer: ok})
	require.NoError(t, err, "500 characters, not bytes")

	newFooter := "Status updates also on https://social.example/acme"
	v, err = f.svc.Update(f.ctx, v.ID, PagePatch{Footer: &newFooter})
	require.NoError(t, err)
	require.Equal(t, newFooter, v.Footer)
	title := "Renamed"
	v, err = f.svc.Update(f.ctx, v.ID, PagePatch{Title: &title})
	require.NoError(t, err)
	require.Equal(t, newFooter, v.Footer, "an omitted footer stays")
	empty := ""
	v, err = f.svc.Update(f.ctx, v.ID, PagePatch{Footer: &empty})
	require.NoError(t, err)
	require.Empty(t, v.Footer)
	_, err = f.svc.Update(f.ctx, v.ID, PagePatch{Footer: &long})
	requireAPIError(t, err, 422, "invalid_footer")

	// Component links: https only, stored as given, cleared with an empty URL.
	v, err = f.svc.SetComponents(f.ctx, v.ID, []ComponentInput{
		{ServiceID: f.api, URL: " https://acme.com/docs "},
		{ServiceID: f.web},
	})
	require.NoError(t, err)
	require.Equal(t, "https://acme.com/docs", v.Components[0].URL)
	require.Empty(t, v.Components[1].URL)
	got, err := f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "https://acme.com/docs", got.Components[0].URL)
	list, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	var listed *StatusPageView
	for i := range list {
		if list[i].ID == v.ID {
			listed = &list[i]
		}
	}
	require.NotNil(t, listed)
	require.Equal(t, "https://acme.com/docs", listed.Components[0].URL, "lists carry the links too")

	for _, bad := range []string{"http://acme.com", "acme.com", "https://u:p@acme.com", "javascript:alert(1)", "https://acme.com/" + strings.Repeat("p", maxURLLen)} {
		_, err = f.svc.SetComponents(f.ctx, v.ID, []ComponentInput{{ServiceID: f.api, URL: bad}})
		requireAPIError(t, err, 422, "invalid_url")
	}
	got, err = f.svc.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, "https://acme.com/docs", got.Components[0].URL, "a refused list changes nothing")

	v, err = f.svc.SetComponents(f.ctx, v.ID, []ComponentInput{{ServiceID: f.api}})
	require.NoError(t, err)
	require.Empty(t, v.Components[0].URL)
}

func TestPublicFooterLinksKeysAnd24hAvailability(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	page, err := f.svc.Create(f.ctx, PageCreate{Title: "Acme status", Hostname: "status.acme.com", Footer: "Contact ops@acme.com\nThanks"})
	require.NoError(t, err)
	_, err = f.svc.SetComponents(f.ctx, page.ID, []ComponentInput{
		{ServiceID: f.api, DisplayName: "API", URL: "https://acme.com/api-docs"},
		{ServiceID: f.web, DisplayName: "Website"},
		{ServiceID: f.database, DisplayName: "Database"},
	})
	require.NoError(t, err)
	f.publish(page.ID)
	f.state(f.api, "up", "")
	f.state(f.web, "up", "")

	// 24 hours before now (2026-10-28 12:00) is 2026-10-27 12:00, inclusive.
	f.minute(f.api, "2026-10-27T13:00:00Z", 4, 3, 0, 1, 0)
	f.minute(f.api, "2026-10-27T11:59:00Z", 100, 0, 0, 100, 0) // too old
	f.minute(f.api, "2026-10-27T12:00:00Z", 2, 2, 0, 0, 0)     // exactly 24 h ago
	f.minute(f.api, "2026-10-28T10:00:00Z", 4, 4, 0, 0, 0)
	f.minute(f.api, "2026-10-28T11:00:00Z", 2, 0, 0, 0, 2) // maintenance only
	f.day(f.web, "2026-10-28", 10, 10, 0, 0, 0)            // day rows say nothing about the last 24 h
	f.minute(f.database, "2026-10-28T09:00:00Z", 2, 0, 0, 0, 2)

	// Incidents and maintenance, for the keys.
	inc, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "Slow", Impact: ImpactMinor, Body: "Slow.", PageIDs: []uuid.UUID{page.ID}, ServiceIDs: []uuid.UUID{f.api}})
	require.NoError(t, err)
	inc2, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "Done", Impact: ImpactNone, Status: IncidentResolved, Body: "Done.", PageIDs: []uuid.UUID{page.ID}})
	require.NoError(t, err)
	win, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "Upgrade", StartsAt: f.now.Add(time.Hour), EndsAt: f.now.Add(2 * time.Hour), PageIDs: []uuid.UUID{page.ID}})
	require.NoError(t, err)

	got, err := f.svc.Public(ctx, "status.acme.com")
	require.NoError(t, err)

	require.Equal(t, "Contact ops@acme.com\nThanks", got.Footer)
	api, web, db := findComponent(t, got, "API"), findComponent(t, got, "Website"), findComponent(t, got, "Database")
	require.Equal(t, "https://acme.com/api-docs", api.URL)
	require.Empty(t, web.URL)

	// (2+4+2... checks) 12 checks, 9 up, 1 down, 2 maintenance: 9 / (12 - 2).
	require.NotNil(t, api.Availability24h)
	require.InDelta(t, 0.9, *api.Availability24h, 1e-9)
	require.Nil(t, web.Availability24h, "no minute rollups in the last 24 h")
	require.Nil(t, db.Availability24h, "only maintenance checks: no data")

	// Keys: opaque, stable, one per incident and window.
	require.Len(t, got.ActiveIncidents, 1)
	require.Len(t, got.PastIncidents, 1)
	require.Len(t, got.Maintenance, 1)
	aKey, pKey, mKey := got.ActiveIncidents[0].Key, got.PastIncidents[0].Key, got.Maintenance[0].Key
	require.Equal(t, opaqueKey("incident", inc.ID), aKey)
	require.Equal(t, opaqueKey("incident", inc2.ID), pKey)
	require.Equal(t, opaqueKey("maintenance", win.ID), mKey)
	require.Len(t, map[string]bool{aKey: true, pKey: true, mKey: true}, 3)
	for _, k := range []string{aKey, pKey, mKey} {
		require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), k)
		for _, id := range []uuid.UUID{inc.ID, inc2.ID, win.ID} {
			require.NotContains(t, id.String(), k)
			require.NotContains(t, k, strings.ReplaceAll(id.String(), "-", "")[:8], "no part of the UUID")
		}
	}
	again, err := f.svc.Public(ctx, "status.acme.com")
	require.NoError(t, err)
	require.Equal(t, aKey, again.ActiveIncidents[0].Key, "stable across calls")
	require.Equal(t, mKey, again.Maintenance[0].Key)

	// The keys do not leak the IDs anywhere in the output.
	text := mustJSON(t, got)
	for _, id := range []uuid.UUID{inc.ID, inc2.ID, win.ID} {
		require.NotContains(t, text, id.String())
	}
}
