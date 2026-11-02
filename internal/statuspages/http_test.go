package statuspages

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// httpCaller is who a request is made as; a nil principal is no sign-in.
type httpCaller struct{ p *auth.Principal }

func (f *fx) owner() httpCaller {
	p, _ := auth.PrincipalFrom(f.ctx)
	return httpCaller{p}
}

func (f *fx) tokenCaller(scopes []string, projects []uuid.UUID) httpCaller {
	p, _ := auth.PrincipalFrom(f.as(scopes, projects))
	return httpCaller{p}
}

// do sends one request through the registered routes and returns status and body.
func (f *fx) do(c httpCaller, method, path string, body any) (int, []byte) {
	f.t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if c.p != nil {
				req = req.WithContext(auth.WithPrincipal(req.Context(), c.p))
			}
			next.ServeHTTP(w, req)
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	cfg.Components.Schemas = api.NewSchemaRegistry()
	Register(humachi.New(r, cfg), f.svc)
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(f.t, err)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// call is do for a request that must answer want, decoded into out (when set).
func (f *fx) call(c httpCaller, want int, method, path string, body, out any) {
	f.t.Helper()
	code, raw := f.do(c, method, path, body)
	require.Equal(f.t, want, code, "%s %s: %s", method, path, raw)
	if out != nil {
		require.NoError(f.t, json.Unmarshal(raw, out), string(raw))
	}
}

// errCode returns the machine-readable code of an error response.
func errCode(t *testing.T, raw []byte) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(raw, &e), string(raw))
	return e.Code
}

type pageList struct {
	Items []StatusPageView `json:"items"`
}

func TestStatusPageEndpointsLifecycle(t *testing.T) {
	f := newFx(t)
	f.controlPlane()
	f.svc.resolver = fakeResolver{addrs: []string{"203.0.113.10"}}
	o := f.owner()

	var created StatusPageView
	f.call(o, http.StatusCreated, http.MethodPost, "/status-pages", map[string]any{
		"title": "Acme status", "hostname": "Status.Acme.com", "description": "How we are doing", "footer": "Line one\nLine two",
		"showUptime": false, "theme": "dark",
	}, &created)
	require.Equal(t, "Acme status", created.Title)
	require.Equal(t, "acme-status", created.Slug)
	require.Equal(t, "status.acme.com", *created.Hostname)
	require.Equal(t, "How we are doing", created.Description)
	require.Equal(t, "Line one\nLine two", created.Footer)
	require.False(t, created.ShowUptime)
	require.Equal(t, "dark", created.Theme)
	require.False(t, created.Published)
	require.NotNil(t, created.Components, "an empty list, not null")
	require.Equal(t, 1, f.auditCount(ActionPageCreate))

	// Components, with a link and a group.
	var withComps StatusPageView
	f.call(o, http.StatusOK, http.MethodPut, "/status-pages/"+created.ID.String()+"/components", map[string]any{
		"components": []map[string]any{
			{"serviceId": f.web, "displayName": "Website", "groupName": "Core", "url": "https://acme.com"},
			{"serviceId": f.api},
		},
	}, &withComps)
	require.Len(t, withComps.Components, 2)
	require.Equal(t, "Website", withComps.Components[0].DisplayName)
	require.Equal(t, "Core", withComps.Components[0].GroupName)
	require.Equal(t, "https://acme.com", withComps.Components[0].URL)
	require.Equal(t, "svc-secret-api", withComps.Components[1].DisplayName, "default name is the service's")

	// A component the workspace does not own is refused and nothing changes.
	code, raw := f.do(o, http.MethodPut, "/status-pages/"+created.ID.String()+"/components", map[string]any{
		"components": []map[string]any{{"serviceId": f.foreign}},
	})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	require.Equal(t, "unknown_service", errCode(t, raw))
	code, raw = f.do(o, http.MethodPut, "/status-pages/"+created.ID.String()+"/components", map[string]any{
		"components": []map[string]any{{"serviceId": f.api, "url": "http://insecure.example"}},
	})
	require.Equal(t, http.StatusUnprocessableEntity, code, string(raw))
	require.Equal(t, "invalid_url", errCode(t, raw))

	// Publish with PATCH; the list and the page show it.
	var published StatusPageView
	f.call(o, http.StatusOK, http.MethodPatch, "/status-pages/"+created.ID.String(), map[string]any{"published": true, "title": "Acme platform"}, &published)
	require.True(t, published.Published)
	require.Equal(t, "Acme platform", published.Title)
	require.Equal(t, "acme-status", published.Slug, "omitted fields stay")
	require.Equal(t, 1, f.routes.count(), "publishing pushed the routes")

	var list pageList
	f.call(o, http.StatusOK, http.MethodGet, "/status-pages", nil, &list)
	require.Len(t, list.Items, 1)
	require.Equal(t, published.ID, list.Items[0].ID)
	require.Len(t, list.Items[0].Components, 2)

	var got StatusPageView
	f.call(o, http.StatusOK, http.MethodGet, "/status-pages/"+created.ID.String(), nil, &got)
	require.Equal(t, "Acme platform", got.Title)
	require.True(t, got.Published)

	// DNS: the server's address against the resolver's; fresh=true is accepted.
	for _, path := range []string{"/status-pages/" + created.ID.String() + "/dns", "/status-pages/" + created.ID.String() + "/dns?fresh=true"} {
		var dns StatusPageDNSCheck
		f.call(o, http.StatusOK, http.MethodGet, path, nil, &dns)
		require.Equal(t, "status.acme.com", dns.Hostname)
		require.Equal(t, DNSOK, dns.Status)
		require.Equal(t, []string{"203.0.113.10"}, dns.Resolved)
		require.Equal(t, []StatusPageDNSRecord{{Type: "A", Name: "status.acme.com", Value: "203.0.113.10"}}, dns.Records)
	}

	// Conflicts and validation map to their status and code.
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{"duplicate slug", http.MethodPost, "/status-pages", map[string]any{"title": "Other", "slug": "acme-status"}, 409, "slug_conflict"},
		{"duplicate hostname", http.MethodPost, "/status-pages", map[string]any{"title": "Other", "hostname": "status.acme.com"}, 409, "hostname_conflict"},
		{"bad hostname", http.MethodPost, "/status-pages", map[string]any{"title": "Other", "hostname": "not a host"}, 422, "invalid_hostname"},
		{"unknown project", http.MethodPost, "/status-pages", map[string]any{"title": "Other", "fromProjectId": uuid.New()}, 422, "unknown_project"},
		{"unpublish hostname of a published page", http.MethodPatch, "/status-pages/" + created.ID.String(), map[string]any{"hostname": ""}, 422, "publish_needs_hostname"},
		{"unknown page", http.MethodGet, "/status-pages/" + uuid.NewString(), nil, 404, "not_found"},
		{"unknown page update", http.MethodPatch, "/status-pages/" + uuid.NewString(), map[string]any{"title": "x"}, 404, "not_found"},
		{"unknown page dns", http.MethodGet, "/status-pages/" + uuid.NewString() + "/dns", nil, 404, "not_found"},
		{"another workspace's page", http.MethodGet, "/status-pages/" + f.foreignP.String(), nil, 404, "not_found"},
		{"unknown page delete", http.MethodDelete, "/status-pages/" + uuid.NewString(), nil, 404, "not_found"},
		{"unknown page components", http.MethodPut, "/status-pages/" + uuid.NewString() + "/components", map[string]any{"components": []any{}}, 404, "not_found"},
	} {
		code, raw := f.do(o, tc.method, tc.path, tc.body)
		require.Equal(t, tc.status, code, "%s: %s", tc.name, raw)
		require.Equal(t, tc.code, errCode(t, raw), tc.name)
	}

	// Schema validation happens before the service: a missing title, a bad
	// theme or a malformed id never reach it.
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"no title", http.MethodPost, "/status-pages", map[string]any{"hostname": "x.example.com"}},
		{"unknown theme", http.MethodPost, "/status-pages", map[string]any{"title": "T", "theme": "neon"}},
		{"title too long", http.MethodPatch, "/status-pages/" + created.ID.String(), map[string]any{"title": string(make([]byte, 101))}},
		{"id not a uuid", http.MethodGet, "/status-pages/not-a-uuid", nil},
	} {
		code, raw := f.do(o, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusUnprocessableEntity, code, "%s: %s", tc.name, raw)
	}
	require.Equal(t, 1, f.auditCount(ActionPageCreate), "refused creates left no trace")

	// Delete: 204, then gone.
	code, raw = f.do(o, http.MethodDelete, "/status-pages/"+created.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, code, string(raw))
	require.Empty(t, raw)
	code, raw = f.do(o, http.MethodGet, "/status-pages/"+created.ID.String(), nil)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "not_found", errCode(t, raw))
	require.Equal(t, 1, f.auditCount(ActionPageDelete))
}

func TestCreateStatusPageFromProjectPrefillsComponents(t *testing.T) {
	f := newFx(t)
	f.exec(`INSERT INTO domains (service_id, hostname, target_port) VALUES ($1, 'api.acme.com', 80)`, f.api)

	var v StatusPageView
	f.call(f.owner(), http.StatusCreated, http.MethodPost, "/status-pages", map[string]any{"title": "From project", "fromProjectId": f.project}, &v)
	require.Len(t, v.Components, 1, "only the production service with a public domain")
	require.Equal(t, f.api, v.Components[0].ServiceID)
	require.Equal(t, "svc-secret-api", v.Components[0].DisplayName)
}

func TestStatusEndpointsAreAuthenticatedAndScoped(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "status.acme.com")
	f.components(page.ID, f.api)
	inc, err := f.svc.CreateIncident(f.ctx, IncidentCreate{Title: "Down", Impact: ImpactMajor, Body: "x", PageIDs: []uuid.UUID{page.ID}})
	require.NoError(t, err)
	win, err := f.svc.CreateMaintenance(f.ctx, MaintenanceCreate{Title: "Upgrade", StartsAt: f.now.Add(time.Hour), EndsAt: f.now.Add(2 * time.Hour)})
	require.NoError(t, err)

	pid, iid, wid := page.ID.String(), inc.ID.String(), win.ID.String()
	reads := []struct{ method, path string }{
		{http.MethodGet, "/status-pages"},
		{http.MethodGet, "/status-pages/" + pid},
		{http.MethodGet, "/status-pages/" + pid + "/dns"},
		{http.MethodGet, "/status-incidents"},
		{http.MethodGet, "/status-incidents/" + iid},
		{http.MethodGet, "/maintenance-windows"},
		{http.MethodGet, "/maintenance-windows/" + wid},
	}
	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/status-pages", map[string]any{"title": "Nope"}},
		{http.MethodPatch, "/status-pages/" + pid, map[string]any{"title": "Nope"}},
		{http.MethodDelete, "/status-pages/" + pid, nil},
		{http.MethodPut, "/status-pages/" + pid + "/components", map[string]any{"components": []any{}}},
		{http.MethodPost, "/status-incidents", map[string]any{"title": "Nope", "impact": "minor", "body": "x"}},
		{http.MethodPatch, "/status-incidents/" + iid, map[string]any{"title": "Nope"}},
		{http.MethodPost, "/status-incidents/" + iid + "/updates", map[string]any{"status": "resolved", "body": "Nope"}},
		{http.MethodDelete, "/status-incidents/" + iid, nil},
		{http.MethodPost, "/maintenance-windows", map[string]any{"title": "Nope", "startsAt": f.now.Add(time.Hour), "endsAt": f.now.Add(2 * time.Hour)}},
		{http.MethodPatch, "/maintenance-windows/" + wid, map[string]any{"title": "Nope"}},
		{http.MethodPost, "/maintenance-windows/" + wid + "/cancel", nil},
		{http.MethodDelete, "/maintenance-windows/" + wid, nil},
	}

	for _, r := range reads {
		code, raw := f.do(httpCaller{}, r.method, r.path, nil)
		require.Equal(t, http.StatusUnauthorized, code, "%s %s signed out: %s", r.method, r.path, raw)
		require.Equal(t, "unauthorized", errCode(t, raw))
		code, raw = f.do(f.tokenCaller([]string{policy.ScopeServersRead}, nil), r.method, r.path, nil)
		require.Equal(t, http.StatusForbidden, code, "%s %s without projects:read: %s", r.method, r.path, raw)
		code, raw = f.do(f.tokenCaller([]string{policy.ScopeProjectsRead}, nil), r.method, r.path, nil)
		require.Equal(t, http.StatusOK, code, "%s %s with projects:read: %s", r.method, r.path, raw)
	}
	for _, w := range writes {
		code, raw := f.do(httpCaller{}, w.method, w.path, w.body)
		require.Equal(t, http.StatusUnauthorized, code, "%s %s signed out: %s", w.method, w.path, raw)
		// A reader cannot publish statements.
		code, raw = f.do(f.tokenCaller([]string{policy.ScopeProjectsRead}, nil), w.method, w.path, w.body)
		require.Equal(t, http.StatusForbidden, code, "%s %s as a reader: %s", w.method, w.path, raw)
		require.Equal(t, "forbidden", errCode(t, raw))
		// Not even a token with every scope but restricted to other projects.
		code, raw = f.do(f.tokenCaller(policy.AllScopes, []uuid.UUID{uuid.New()}), w.method, w.path, w.body)
		require.Equal(t, http.StatusForbidden, code, "%s %s as a restricted token: %s", w.method, w.path, raw)
	}

	// None of the refused writes changed anything.
	got, err := f.svc.Get(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, "Acme", got.Title)
	_, err = f.svc.GetIncident(f.ctx, inc.ID)
	require.NoError(t, err)
	gw, err := f.svc.GetMaintenance(f.ctx, win.ID)
	require.NoError(t, err)
	require.Nil(t, gw.CanceledAt)
}

type incidentList struct {
	Items      []StatusIncidentView `json:"items"`
	NextCursor string               `json:"nextCursor"`
}

func TestStatusIncidentEndpointsLifecycle(t *testing.T) {
	f := newFx(t)
	pa, pb := f.createPage("A", ""), f.createPage("B", "")
	o := f.owner()

	var inc StatusIncidentView
	f.call(o, http.StatusCreated, http.MethodPost, "/status-incidents", map[string]any{
		"title": "API errors", "impact": "major", "body": "Investigating.", "pageIds": []uuid.UUID{pa.ID}, "serviceIds": []uuid.UUID{f.api},
	}, &inc)
	require.Equal(t, IncidentInvestigating, inc.Status, "default status")
	require.Equal(t, ImpactMajor, inc.Impact)
	require.Equal(t, []uuid.UUID{pa.ID}, inc.PageIDs)
	require.Equal(t, []uuid.UUID{f.api}, inc.ServiceIDs)
	require.Len(t, inc.Updates, 1)
	require.Nil(t, inc.ResolvedAt)

	// Post updates: the timeline grows (newest first), resolved sets resolvedAt.
	// The fixture clock is frozen, so it moves on between posts: updates stamped
	// with the same instant are ordered by their random id.
	var posted StatusIncidentView
	f.now = f.now.Add(time.Minute)
	f.call(o, http.StatusCreated, http.MethodPost, "/status-incidents/"+inc.ID.String()+"/updates", map[string]any{"status": "identified", "body": "A bad deploy."}, &posted)
	require.Equal(t, IncidentIdentified, posted.Status)
	require.Len(t, posted.Updates, 2)
	require.Equal(t, "A bad deploy.", posted.Updates[0].Body)
	f.now = f.now.Add(time.Minute)
	f.call(o, http.StatusCreated, http.MethodPost, "/status-incidents/"+inc.ID.String()+"/updates", map[string]any{"status": "resolved", "body": "Rolled back."}, &posted)
	require.NotNil(t, posted.ResolvedAt)

	// A second, active incident on page B.
	var other StatusIncidentView
	f.call(o, http.StatusCreated, http.MethodPost, "/status-incidents", map[string]any{
		"title": "Slow", "impact": "minor", "status": "monitoring", "body": "Watching.", "pageIds": []uuid.UUID{pb.ID},
	}, &other)
	require.Equal(t, IncidentMonitoring, other.Status)

	// PATCH: header only; the lists replace.
	var upd StatusIncidentView
	f.call(o, http.StatusOK, http.MethodPatch, "/status-incidents/"+other.ID.String(), map[string]any{
		"title": "Slow (EU)", "impact": "critical", "pageIds": []uuid.UUID{pa.ID, pb.ID}, "serviceIds": []uuid.UUID{f.web},
	}, &upd)
	require.Equal(t, "Slow (EU)", upd.Title)
	require.Equal(t, ImpactCritical, upd.Impact)
	require.Equal(t, IncidentMonitoring, upd.Status)
	require.ElementsMatch(t, []uuid.UUID{pa.ID, pb.ID}, upd.PageIDs)
	require.Equal(t, []uuid.UUID{f.web}, upd.ServiceIDs)

	var got StatusIncidentView
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents/"+inc.ID.String(), nil, &got)
	require.Equal(t, IncidentResolved, got.Status)
	require.Len(t, got.Updates, 3)

	// Filters, page size and cursor.
	titles := func(l incidentList) []string {
		var out []string
		for _, i := range l.Items {
			out = append(out, i.Title)
		}
		return out
	}
	var all, active, resolved, onB incidentList
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents", nil, &all)
	require.ElementsMatch(t, []string{"API errors", "Slow (EU)"}, titles(all))
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents?status=active", nil, &active)
	require.Equal(t, []string{"Slow (EU)"}, titles(active))
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents?status=resolved", nil, &resolved)
	require.Equal(t, []string{"API errors"}, titles(resolved))
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents?pageId="+pb.ID.String(), nil, &onB)
	require.Equal(t, []string{"Slow (EU)"}, titles(onB))

	var p1, p2 incidentList
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents?limit=1", nil, &p1)
	require.Len(t, p1.Items, 1)
	require.NotEmpty(t, p1.NextCursor)
	f.call(o, http.StatusOK, http.MethodGet, "/status-incidents?limit=1&cursor="+url.QueryEscape(p1.NextCursor), nil, &p2)
	require.Len(t, p2.Items, 1)
	require.NotEqual(t, p1.Items[0].ID, p2.Items[0].ID)
	require.Empty(t, p2.NextCursor)

	// Errors.
	for _, tc := range []struct {
		name, method, path string
		body               any
		status             int
		code               string
	}{
		{"bad cursor", http.MethodGet, "/status-incidents?cursor=garbage", nil, 400, "invalid_cursor"},
		{"bad status filter", http.MethodGet, "/status-incidents?status=bogus", nil, 422, ""},
		{"page id not a uuid", http.MethodGet, "/status-incidents?pageId=nope", nil, 422, ""},
		{"unknown page", http.MethodPost, "/status-incidents", map[string]any{"title": "x", "impact": "minor", "body": "x", "pageIds": []uuid.UUID{f.foreignP}}, 422, "unknown_page"},
		{"unknown service", http.MethodPost, "/status-incidents", map[string]any{"title": "x", "impact": "minor", "body": "x", "serviceIds": []uuid.UUID{f.foreign}}, 422, "unknown_service"},
		{"blank body", http.MethodPost, "/status-incidents", map[string]any{"title": "x", "impact": "minor", "body": " "}, 422, "invalid_body"},
		{"bad impact", http.MethodPost, "/status-incidents", map[string]any{"title": "x", "impact": "huge", "body": "x"}, 422, ""},
		{"missing impact", http.MethodPost, "/status-incidents", map[string]any{"title": "x", "body": "x"}, 422, ""},
		{"unknown incident", http.MethodGet, "/status-incidents/" + uuid.NewString(), nil, 404, "not_found"},
		{"unknown incident update", http.MethodPatch, "/status-incidents/" + uuid.NewString(), map[string]any{"title": "x"}, 404, "not_found"},
		{"update of unknown incident", http.MethodPost, "/status-incidents/" + uuid.NewString() + "/updates", map[string]any{"status": "resolved", "body": "x"}, 404, "not_found"},
		{"bad update status", http.MethodPost, "/status-incidents/" + inc.ID.String() + "/updates", map[string]any{"status": "fixed", "body": "x"}, 422, ""},
		{"unknown incident delete", http.MethodDelete, "/status-incidents/" + uuid.NewString(), nil, 404, "not_found"},
	} {
		code, raw := f.do(o, tc.method, tc.path, tc.body)
		require.Equal(t, tc.status, code, "%s: %s", tc.name, raw)
		if tc.code != "" {
			require.Equal(t, tc.code, errCode(t, raw), tc.name)
		}
	}

	// Delete.
	code, raw := f.do(o, http.MethodDelete, "/status-incidents/"+inc.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, code, string(raw))
	code, _ = f.do(o, http.MethodGet, "/status-incidents/"+inc.ID.String(), nil)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, 1, f.auditCount(ActionIncidentDelete))
}

type windowList struct {
	Items      []MaintenanceWindowView `json:"items"`
	NextCursor string                  `json:"nextCursor"`
}

func TestMaintenanceEndpointsLifecycle(t *testing.T) {
	f := newFx(t)
	page := f.createPage("Acme", "")
	o := f.owner()
	at := func(d time.Duration) time.Time { return f.now.Add(d) }

	var w MaintenanceWindowView
	f.call(o, http.StatusCreated, http.MethodPost, "/maintenance-windows", map[string]any{
		"title": "DB upgrade", "description": "Read-only.", "startsAt": at(2 * time.Hour), "endsAt": at(3 * time.Hour),
		"serviceIds": []uuid.UUID{f.database}, "pageIds": []uuid.UUID{page.ID},
	}, &w)
	require.Equal(t, PhaseUpcoming, w.Phase)
	require.Equal(t, "Read-only.", w.Description)
	require.Equal(t, []uuid.UUID{f.database}, w.ServiceIDs)
	require.Equal(t, []uuid.UUID{page.ID}, w.PageIDs)
	require.True(t, w.StartsAt.Equal(at(2*time.Hour)))

	var running MaintenanceWindowView
	f.call(o, http.StatusCreated, http.MethodPost, "/maintenance-windows", map[string]any{
		"title": "Running", "startsAt": at(-time.Hour), "endsAt": at(time.Hour),
	}, &running)
	require.Equal(t, PhaseActive, running.Phase)

	titles := func(l windowList) []string {
		var out []string
		for _, i := range l.Items {
			out = append(out, i.Title)
		}
		return out
	}
	var all, upcoming, active, onPage windowList
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows", nil, &all)
	require.Equal(t, []string{"DB upgrade", "Running"}, titles(all), "latest start first")
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows?phase=upcoming", nil, &upcoming)
	require.Equal(t, []string{"DB upgrade"}, titles(upcoming))
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows?phase=active", nil, &active)
	require.Equal(t, []string{"Running"}, titles(active))
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows?pageId="+page.ID.String(), nil, &onPage)
	require.Equal(t, []string{"DB upgrade"}, titles(onPage))
	var p1, p2 windowList
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows?limit=1", nil, &p1)
	require.Equal(t, []string{"DB upgrade"}, titles(p1))
	require.NotEmpty(t, p1.NextCursor)
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows?limit=1&cursor="+url.QueryEscape(p1.NextCursor), nil, &p2)
	require.Equal(t, []string{"Running"}, titles(p2))

	var got, upd MaintenanceWindowView
	f.call(o, http.StatusOK, http.MethodGet, "/maintenance-windows/"+w.ID.String(), nil, &got)
	require.Equal(t, w.ID, got.ID)
	f.call(o, http.StatusOK, http.MethodPatch, "/maintenance-windows/"+w.ID.String(), map[string]any{
		"title": "DB upgrade (long)", "description": "Longer.", "endsAt": at(4 * time.Hour), "serviceIds": []uuid.UUID{f.api, f.web}, "pageIds": []uuid.UUID{},
	}, &upd)
	require.Equal(t, "DB upgrade (long)", upd.Title)
	require.Equal(t, "Longer.", upd.Description)
	require.True(t, upd.EndsAt.Equal(at(4*time.Hour)))
	require.True(t, upd.StartsAt.Equal(at(2*time.Hour)), "omitted startsAt stays")
	require.ElementsMatch(t, []uuid.UUID{f.api, f.web}, upd.ServiceIDs)
	require.Empty(t, upd.PageIDs)

	// Cancel: canceledAt once; a canceled window cannot be edited.
	var canceled MaintenanceWindowView
	f.call(o, http.StatusOK, http.MethodPost, "/maintenance-windows/"+w.ID.String()+"/cancel", nil, &canceled)
	require.Equal(t, PhaseCanceled, canceled.Phase)
	require.NotNil(t, canceled.CanceledAt)
	code, raw := f.do(o, http.MethodPatch, "/maintenance-windows/"+w.ID.String(), map[string]any{"title": "x"})
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Equal(t, "window_canceled", errCode(t, raw))

	// Errors.
	for _, tc := range []struct {
		name, method, path string
		body               any
		status             int
		code               string
	}{
		{"ends before it starts", http.MethodPost, "/maintenance-windows", map[string]any{"title": "x", "startsAt": at(2 * time.Hour), "endsAt": at(time.Hour)}, 422, "invalid_window"},
		{"ends in the past", http.MethodPost, "/maintenance-windows", map[string]any{"title": "x", "startsAt": at(-2 * time.Hour), "endsAt": at(-time.Hour)}, 422, "window_in_past"},
		{"unknown service", http.MethodPost, "/maintenance-windows", map[string]any{"title": "x", "startsAt": at(time.Hour), "endsAt": at(2 * time.Hour), "serviceIds": []uuid.UUID{f.foreign}}, 422, "unknown_service"},
		{"unknown page", http.MethodPost, "/maintenance-windows", map[string]any{"title": "x", "startsAt": at(time.Hour), "endsAt": at(2 * time.Hour), "pageIds": []uuid.UUID{f.foreignP}}, 422, "unknown_page"},
		{"no title", http.MethodPost, "/maintenance-windows", map[string]any{"startsAt": at(time.Hour), "endsAt": at(2 * time.Hour)}, 422, ""},
		{"bad phase filter", http.MethodGet, "/maintenance-windows?phase=soon", nil, 422, ""},
		{"page id not a uuid", http.MethodGet, "/maintenance-windows?pageId=nope", nil, 422, ""},
		{"bad cursor", http.MethodGet, "/maintenance-windows?cursor=garbage", nil, 400, "invalid_cursor"},
		{"unknown window", http.MethodGet, "/maintenance-windows/" + uuid.NewString(), nil, 404, "not_found"},
		{"unknown window update", http.MethodPatch, "/maintenance-windows/" + uuid.NewString(), map[string]any{"title": "x"}, 404, "not_found"},
		{"unknown window cancel", http.MethodPost, "/maintenance-windows/" + uuid.NewString() + "/cancel", nil, 404, "not_found"},
		{"unknown window delete", http.MethodDelete, "/maintenance-windows/" + uuid.NewString(), nil, 404, "not_found"},
	} {
		code, raw := f.do(o, tc.method, tc.path, tc.body)
		require.Equal(t, tc.status, code, "%s: %s", tc.name, raw)
		if tc.code != "" {
			require.Equal(t, tc.code, errCode(t, raw), tc.name)
		}
	}

	// Delete.
	code, raw = f.do(o, http.MethodDelete, "/maintenance-windows/"+w.ID.String(), nil)
	require.Equal(t, http.StatusNoContent, code, string(raw))
	code, _ = f.do(o, http.MethodGet, "/maintenance-windows/"+w.ID.String(), nil)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, 1, f.auditCount(ActionMaintenanceTrash))
}

func TestOptionalUUID(t *testing.T) {
	id := uuid.New()
	got, err := optionalUUID("page_id", id.String())
	require.NoError(t, err)
	require.Equal(t, id, *got)

	got, err = optionalUUID("page_id", "")
	require.NoError(t, err)
	require.Nil(t, got, "absent means no filter")

	_, err = optionalUUID("page_id", "nope")
	requireAPIError(t, err, 400, "invalid_page_id")
}
