package statuspublic

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

var (
	headingRe = regexp.MustCompile(`<h([123])[^>]*>(.*?)</h[123]>`)
	cellRe    = regexp.MustCompile(`<rect x="(\d+)" y="(\d+)" width="8" height="(\d+)" class="c-([a-z]+)"([^>]*)><title>(.*?)</title></rect>`)
	attrRe    = regexp.MustCompile(`data-([a-z]+)="([^"]*)"`)
	barRe     = regexp.MustCompile(`<svg viewBox="0 0 (\d+) 36" role="img" tabindex="0" aria-label="(.*?)"`)
)

func render(t *testing.T, p statuspages.PublicPage) string {
	t.Helper()
	b, err := renderHTML(p, "/status.css?v=test", p.GeneratedAt.Add(2*time.Minute))
	require.NoError(t, err)
	return string(b)
}

func outline(html string) []string {
	var out []string
	for _, m := range headingRe.FindAllStringSubmatch(html, -1) {
		out = append(out, "h"+m[1]+" "+m[2])
	}
	return out
}

// The page's skeleton: the headings in order are a snapshot of what the page
// shows and how it is nested, and they are what a screen reader navigates by.
func TestPageOutlineSnapshot(t *testing.T) {
	for _, theme := range []string{"light", "dark", "auto"} {
		html := render(t, fixturePage(theme))
		require.Equal(t, []string{
			"h1 Acme Status",
			"h2 Active incidents",
			"h3 Dashboard unavailable",
			"h2 Maintenance",
			"h3 Docs reindex",
			"h3 Database upgrade",
			"h2 Components",
			"h3 Web",
			"h2 Past incidents",
			"h3 Slow API",
		}, outline(html), theme)
	}
}

func TestThemeIsCarriedByTheDocument(t *testing.T) {
	for theme, scheme := range map[string]string{"light": "light", "dark": "dark", "auto": "light dark"} {
		html := render(t, fixturePage(theme))
		require.Containsf(t, html, `<html lang="en" data-theme="`+theme+`">`, theme)
		require.Containsf(t, html, `<meta name="color-scheme" content="`+scheme+`">`, theme)
	}
	html := render(t, fixturePage("neon"))
	require.Contains(t, html, `data-theme="auto"`, "an unknown theme is auto")
}

func TestStylesheetCoversBothThemesAndNarrowScreens(t *testing.T) {
	css := buildCSS(fontFiles{})
	require.Equal(t, 1, strings.Count(css, ":root {\n\tcolor-scheme: light;"), "light is the base palette")
	require.Contains(t, css, "@media (prefers-color-scheme: dark) {\n\t:root[data-theme=\"auto\"] {\n\t\tcolor-scheme: dark;")
	require.Contains(t, css, ":root[data-theme=\"dark\"] {\n\tcolor-scheme: dark;")
	require.Contains(t, css, "@media (max-width: 640px)")
	require.Contains(t, css, ".bar svg { width: 300%; margin-left: -200%; }", "the 90-day SVG is cropped to its last 30 days")
	require.Contains(t, css, "@media (forced-colors: active)")
	for _, v := range []string{"--bg", "--surface", "--ink-1", "--ink-2", "--ink-3", "--ok-text", "--bad-text", "--degraded-text", "--info-text", "--mute-text"} {
		require.Equalf(t, 3, strings.Count(css, v+":"), "%s is defined for light, auto-dark and dark", v)
	}
	require.NotContains(t, css, "@import", "no external stylesheet: the CSP allows 'self' only")
	require.NotContains(t, css, "http://fonts", "no external font")
	require.NotRegexp(t, `url\((https?:)?//`, css)
}

func TestEveryStateHasALabelAndAnIcon(t *testing.T) {
	states := []string{
		statuspages.StateOperational, statuspages.StateDegraded, statuspages.StatePartialOutage,
		statuspages.StateMajorOutage, statuspages.StateMaintenance, statuspages.StateUnknown,
	}
	seen := map[string]string{}
	for _, s := range states {
		p := fixturePage("auto")
		p.Overall = s
		html := render(t, p)
		require.Contains(t, html, `class="card banner st-`+s+`" role="status"><svg class="icon"`, s)
		label := overallLabel(s)
		require.Contains(t, html, "<span>"+label+"</span>", s)
		require.NotContains(t, seen, label)
		seen[label] = s
		require.NotEmpty(t, componentLabel(s))
	}
	require.Len(t, seen, len(states), "each state has its own sentence")

	icons := map[string]bool{}
	var tmplOut strings.Builder
	for _, s := range states {
		tmplOut.Reset()
		require.NoError(t, templates.ExecuteTemplate(&tmplOut, "icon", s))
		icons[tmplOut.String()] = true
	}
	require.Len(t, icons, len(states), "each state has its own shape, so colour is never the only cue")
	require.Equal(t, "No data", componentLabel("garbage"))
	require.Equal(t, statuspages.StateUnknown, stateKey("garbage"))
}

func TestComponentsShowLabelUptimeAndBar(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Contains(t, html, `<span class="name"><a href="https://api.acme.example/docs" rel="noopener noreferrer nofollow">API</a></span><span class="meta"><span class="uptime"><span title="Last 24 hours">99.98% <small>24h</small></span><span title="Last 90 days">99.99% <small>90d</small></span></span>`, "99.996% is truncated, not rounded up")
	require.Contains(t, html, `<span class="name">Dashboard</span>`, "a link that is not https is dropped")
	require.Contains(t, html, `<span title="Last 24 hours">95% <small>24h</small></span><span title="Last 90 days">98.73% <small>90d</small></span></span><a class="incident-tag" href="#h-incidents">Incident in progress</a><span class="state st-major_outage"><svg class="icon"`, "Dashboard is named by the active incident: the note sits beside its state, which keeps its icon and label")
	require.Contains(t, html, `Major outage</span>`)
	require.Contains(t, html, `Under maintenance</span>`)
	require.Contains(t, html, `Degraded performance</span>`)
	require.Contains(t, html, `class="state st-unknown"`, "a component with no data and an unknown state")
	require.Equal(t, 3, strings.Count(html, `<span class="uptime">`), "a component without availability shows no figure")

	bars := barRe.FindAllStringSubmatch(html, -1)
	require.Len(t, bars, 4, "one bar per component that has days")
	require.Equal(t, "898", bars[0][1], "90 cells of 8 units at a pitch of 10, without the last gap")
	require.Equal(t, "Last 90 days: 99.99% available, 0 min down", bars[0][2])
	require.Equal(t, 4, strings.Count(html, `role="img" tabindex="0" aria-label="`), "every bar is focusable and has a summary")
	require.Contains(t, html, `<span class="wide">90 days ago</span><span class="narrow">30 days ago</span>`)
}

func TestBarCellsFollowTheBandsAndCarryDetails(t *testing.T) {
	html := render(t, fixturePage("auto"))
	cells := cellRe.FindAllStringSubmatch(html, -1)
	require.Len(t, cells, 4*90, "API, Dashboard, Docs and Search have 90 days each (Mail none)")

	dash := cells[90 : 2*90] // Dashboard is the second component
	var keys []string
	for _, c := range dash[:6] {
		keys = append(keys, c[4])
	}
	require.Equal(t, []string{"none", "maint", "down", "warn", "good", "up"}, keys)
	require.Equal(t, "0", dash[0][1])
	require.Equal(t, "10", dash[1][1])
	require.Equal(t, "890", dash[89][1])
	require.Equal(t, []string{"24", "12"}, []string{dash[0][2], dash[0][3]}, "a day without data is a stub at the foot of the bar")
	for i, c := range dash[1:] {
		require.Equalf(t, []string{"0", "36"}, []string{c[2], c[3]}, "day %d with data is full height", i+1)
	}

	titles := []string{}
	for _, c := range dash[:6] {
		titles = append(titles, c[6])
	}
	require.Equal(t, []string{
		"1 Aug 2026: No data",
		"2 Aug 2026: Maintenance",
		"3 Aug 2026: 90% uptime, about 144 min down, incidents: Database failover",
		"4 Aug 2026: 97% uptime, about 43 min down",
		"5 Aug 2026: 99.5% uptime, about 7 min down",
		"6 Aug 2026: 99.95% uptime, some maintenance",
	}, titles)
}

// cellData is the data attributes of a cell, by name without the data- prefix.
func cellData(attrs string) map[string]string {
	out := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(attrs, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// The tooltip of the page's script reads each day from data attributes: the
// date as the page words dates, the state word, the figure only where the page
// shows figures, and the day's incidents.
func TestBarCellsCarryTheDataTheTooltipShows(t *testing.T) {
	html := render(t, fixturePage("auto"))
	cells := cellRe.FindAllStringSubmatch(html, -1)
	dash := cells[90 : 2*90]

	want := []map[string]string{
		{"date": "1 Aug 2026", "state": "No data"},
		{"date": "2 Aug 2026", "state": "Maintenance"},
		{"date": "3 Aug 2026", "state": "Down", "uptime": "90%", "note": "about 144 min down", "incidents": "Database failover"},
		{"date": "4 Aug 2026", "state": "Degraded", "uptime": "97%", "note": "about 43 min down"},
		{"date": "5 Aug 2026", "state": "Minor issues", "uptime": "99.5%", "note": "about 7 min down"},
		{"date": "6 Aug 2026", "state": "Operational", "uptime": "99.95%", "note": "some maintenance"},
	}
	for i, w := range want {
		require.Equalf(t, w, cellData(dash[i][5]), "day %d", i)
	}
	require.Equal(t, map[string]string{"date": "29 Oct 2026", "state": "Operational", "uptime": "100%"}, cellData(dash[89][5]))
	require.Contains(t, dash[2][6], "3 Aug 2026: 90% uptime", "the native title stays for a visitor without script")
}

func TestBarCellsCarryNoFiguresWhenThePageHidesThem(t *testing.T) {
	p := fixturePage("auto")
	p.ShowUptime = false
	html := render(t, p)
	cells := cellRe.FindAllStringSubmatch(html, -1)
	require.Len(t, cells, 4*90)
	for i, c := range cells {
		data := cellData(c[5])
		require.NotEmptyf(t, data["date"], "cell %d", i)
		require.NotEmptyf(t, data["state"], "cell %d", i)
		require.NotContainsf(t, data, "uptime", "cell %d", i)
		require.NotContainsf(t, c[5], "%", "cell %d", i)
		require.NotContainsf(t, c[6], "%", "cell %d", i)
		require.NotContainsf(t, c[5], "min down", "cell %d", i)
	}
	dash := cells[90 : 2*90]
	require.Equal(t, map[string]string{"date": "3 Aug 2026", "state": "Down", "incidents": "Database failover"}, cellData(dash[2][5]))
	require.Equal(t, map[string]string{"date": "6 Aug 2026", "state": "Operational", "note": "some maintenance"}, cellData(dash[5][5]))
}

func TestIncidentTitlesOfADayAreOnePerLine(t *testing.T) {
	d := statuspages.PublicDay{Incidents: []string{"A  \n b", "C"}}
	require.Equal(t, "A b\nC", dayIncidents(d))
}

func TestBandBoundaries(t *testing.T) {
	day := func(a *float64, maint bool) statuspages.PublicDay {
		return statuspages.PublicDay{Availability: a, Maintenance: maint}
	}
	for _, c := range []struct {
		d    statuspages.PublicDay
		want band
	}{
		{day(new(1.0), false), bandUp},
		{day(new(0.999), false), bandUp},
		{day(new(0.99899), false), bandGood},
		{day(new(0.99), false), bandGood},
		{day(new(0.98999), false), bandWarn},
		{day(new(0.95), false), bandWarn},
		{day(new(0.94999), false), bandDown},
		{day(new(0.0), false), bandDown},
		{day(nil, false), bandNone},
		{day(nil, true), bandMaint},
		{day(new(1.0), true), bandUp},
	} {
		require.Equal(t, c.want.Key, bandOf(c.d).Key)
	}
	seen := map[string]bool{}
	for _, b := range allBands {
		require.False(t, seen[b.Key])
		seen[b.Key] = true
	}
}

func TestPercentIsTruncatedNeverRoundedUp(t *testing.T) {
	for in, want := range map[float64]string{
		1: "100%", 0.99999: "99.99%", 0.99996: "99.99%", 0.9995: "99.95%", 0.999: "99.9%", 0.99: "99%",
		0.9873: "98.73%", 0.5: "50%", 0: "0%", 1.5: "100%", -0.2: "0%",
	} {
		require.Equalf(t, want, percentText(in), "%v", in)
	}
}

func TestEventsRenderTheirTimeline(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Contains(t, html, `<span class="badge st-major_outage">`)
	require.Contains(t, html, `Critical</span></header>`)
	require.Contains(t, html, `Identified · Started <time datetime="2026-10-29T10:00:00Z">29 Oct 2026, 10:00 UTC</time>`)
	require.Contains(t, html, `<p class="affects">Affects: Dashboard</p>`)
	// Newest update first, as the model delivers it.
	i, j := strings.Index(html, "Rolling back."), strings.Index(html, "We are looking into it.")
	require.Positive(t, i)
	require.Greater(t, j, i)
	require.Contains(t, html, `In progress</span>`)
	require.Contains(t, html, `Scheduled</span>`)
	require.Contains(t, html, `<time datetime="2026-11-01T11:00:00Z">1 Nov 2026, 11:00 UTC</time> to <time datetime="2026-11-01T13:00:00Z">`)
	require.Contains(t, html, `Resolved <time datetime="2026-10-29T09:00:00Z">`)

	empty := fixturePage("auto")
	empty.ActiveIncidents, empty.Maintenance, empty.PastIncidents = nil, nil, nil
	html = render(t, empty)
	require.Equal(t, []string{"h1 Acme Status", "h2 Components", "h3 Web", "h2 Past incidents"}, outline(html))
	require.Contains(t, html, "No incidents in the last 30 days.")
}

func TestFooterAndDocumentBasics(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Contains(t, html, `Powered by <a href="https://nebulactrl.com" rel="noopener noreferrer">NebulaCtrl</a>`)
	require.Contains(t, html, `Last updated <time datetime="2026-10-29T12:00:00Z">29 Oct 2026, 12:00 UTC</time> (<span class="age">2 min ago</span>). This page refreshes every minute.`)
	require.Contains(t, html, `<meta name="viewport" content="width=device-width, initial-scale=1">`)
	require.Contains(t, html, `<meta name="description" content="Live status of Acme services. Questions: status@acme.example">`)
	require.Contains(t, html, `<link rel="icon" href="data:image/svg&#43;xml,`, "a data: URL, which the CSP's img-src allows")
	require.Contains(t, html, `<p class="desc">Live status of Acme services.`+"\n"+`Questions:`, "line breaks of the description are kept")
	require.Equal(t, 1, strings.Count(html, "<script"), "only the page's own same-origin script")
	require.True(t, strings.HasPrefix(html, "<!doctype html>"))

	long := fixturePage("auto")
	long.Description = strings.Repeat("word ", 100)
	require.Equal(t, metaDescriptionLimit, len([]rune(metaDescription(long.Description))))
	require.Equal(t, 155, metaDescriptionLimit)
	require.True(t, strings.HasSuffix(metaDescription(long.Description), "…"))
}

func TestNotFoundPageIsMinimal(t *testing.T) {
	html := string(renderNotFound("/status.css?v=x"))
	require.Contains(t, html, "<h1>Page not found</h1>")
	require.NotContains(t, html, "<svg")
	require.Less(t, len(html), 1000)
}

func TestJSONRenderingIsStableForAFixture(t *testing.T) {
	p := statuspages.PublicPage{
		Title: "T", Overall: statuspages.StateOperational, ShowUptime: true,
		GeneratedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Groups: []statuspages.PublicGroup{{Components: []statuspages.PublicComponent{
			{Name: "A", State: statuspages.StateOperational, Availability: new(0.9999), Days: []statuspages.PublicDay{
				{Date: "2026-01-02", Availability: new(0.9999), DownMinutes: 1},
			}},
		}}},
	}
	b, err := renderJSON(p)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"title":"T","status":{"state":"operational","label":"All systems operational"},"showUptime":true,
		"generatedAt":"2026-01-02T03:04:05Z",
		"groups":[{"name":"","components":[{"name":"A","slug":"a","status":{"state":"operational","label":"Operational"},"uptime":99.99,
			"days":[{"date":"2026-01-02","status":"operational","uptime":99.99,"downMinutes":1,"incidents":[]}]}]}],
		"activeIncidents":[],"maintenance":[],"pastIncidents":[]
	}`, string(b))
}
