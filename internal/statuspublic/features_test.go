package statuspublic

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

func TestFooterTextIsEscapedAndKeepsLineBreaks(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Contains(t, html, "<p class=\"footer-text\">Acme Inc.\nContact: status@acme.example &lt;b&gt;</p>")
	require.NotContains(t, html, "<b>")

	p := fixturePage("auto")
	p.Footer = ""
	require.NotContains(t, render(t, p), "footer-text", "no footer text, no empty paragraph")
}

func TestLastUpdatedIsATimeElementWithRelativeText(t *testing.T) {
	p := fixturePage("auto")
	for _, c := range []struct {
		age  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{60 * time.Second, "1 min ago"},
		{5 * time.Minute, "5 min ago"},
		{59 * time.Minute, "59 min ago"},
		{2 * time.Hour, "2 h ago"},
		{49 * time.Hour, "2 d ago"},
	} {
		require.Equal(t, c.want, relativeAge(p.GeneratedAt.Add(c.age), p.GeneratedAt), c.age.String())
		b, err := renderHTML(p, "/status.css?v=t", p.GeneratedAt.Add(c.age))
		require.NoError(t, err)
		require.Contains(t, string(b), `<time datetime="2026-10-29T12:00:00Z">29 Oct 2026, 12:00 UTC</time> (<span class="age">`+c.want+`</span>). This page refreshes every minute.`)
	}
	html := render(t, p)
	require.Contains(t, html, `<noscript><meta http-equiv="refresh" content="60"></noscript>`, "without script the page reloads every 60 seconds; with it the script swaps the content softly")
	require.NotContains(t, strings.ReplaceAll(html, `<noscript><meta http-equiv="refresh" content="60"></noscript>`, ""), `http-equiv="refresh"`, "a script-enabled reader is never forcibly reloaded")
}

func TestComponentNamedByAnActiveIncidentSaysSo(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Equal(t, 1, strings.Count(html, `<a class="incident-tag" href="#h-incidents">Incident in progress</a>`), "only Dashboard is named by the active incident")

	p := fixturePage("auto")
	p.ActiveIncidents = nil
	require.NotContains(t, render(t, p), "incident-tag", "no active incident, no tag")
}

func TestBarCaptionSaysWhichWindowTheFigureCovers(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Contains(t, html, `The bar shows the last 30 days. The uptime figure covers 90 days.`)

	p := fixturePage("auto")
	p.ShowUptime = false
	require.NotContains(t, render(t, p), "ago-note", "a page without figures has nothing to qualify")
}

func TestComponentLinksNeedHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.acme.example/docs?a=1":   "https://api.acme.example/docs?a=1",
		"  https://api.acme.example  ":        "https://api.acme.example",
		"http://api.acme.example":             "",
		"javascript:alert(1)":                 "",
		"data:text/html,x":                    "",
		"//api.acme.example":                  "",
		"https://":                            "",
		"https://user:pw@api.acme.example/":   "",
		"":                                    "",
		"https://api.acme.example/\"><script": "https://api.acme.example/%22%3E%3Cscript",
	} {
		require.Equalf(t, want, safeLink(in), "%q", in)
	}
	html := render(t, fixturePage("auto"))
	require.Equal(t, 1, strings.Count(html, `rel="noopener noreferrer nofollow"`), "only API has a valid link")
	require.NotContains(t, html, "javascript:")
}

func TestUptimeFiguresShowEachWindowThatHasData(t *testing.T) {
	mk := func(a24, a90 *float64, show bool) string {
		p := fixturePage("auto")
		p.ShowUptime = show
		p.Groups = []statuspages.PublicGroup{{Components: []statuspages.PublicComponent{
			{Name: "X", State: statuspages.StateOperational, Availability24h: a24, Availability: a90},
		}}}
		return render(t, p)
	}
	both := mk(new(0.9998), new(0.9995), true)
	require.Contains(t, both, `<span title="Last 24 hours">99.98% <small>24h</small></span><span title="Last 90 days">99.95% <small>90d</small></span>`)

	only24 := mk(new(0.5), nil, true)
	require.Contains(t, only24, `<span title="Last 24 hours">50% <small>24h</small></span>`)
	require.NotContains(t, only24, "Last 90 days")

	only90 := mk(nil, new(0.5), true)
	require.Contains(t, only90, `<span title="Last 90 days">50% <small>90d</small></span>`)
	require.NotContains(t, only90, "Last 24 hours")

	require.NotContains(t, mk(nil, nil, true), `class="uptime"`)
	hidden := mk(new(0.9998), new(0.9995), false)
	require.NotContains(t, hidden, `class="uptime"`, "the page hides its figures")
	require.NotContains(t, hidden, "99.98")
}

func TestGroupsAreCollapsibleDetailsAndUngroupedIsNot(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Equal(t, 1, strings.Count(html, "<details"))
	require.Contains(t, html, "<details class=\"card group\" open>\n<summary><h3>Web</h3></summary>")
	require.Equal(t, 1, strings.Count(html, `<div class="card group">`), "the ungrouped components are always shown")
	require.Equal(t, 1, strings.Count(html, "<script"), "only the page's own same-origin script")
}

func TestBarsAreFocusableWithASummary(t *testing.T) {
	html := render(t, fixturePage("auto"))
	bars := barRe.FindAllStringSubmatch(html, -1)
	require.Len(t, bars, 4)
	require.Equal(t, "Last 90 days: 99.99% available, 0 min down", bars[0][2])
	require.Equal(t, "Last 90 days: 98.73% available, 194 min down", bars[1][2], "144 + 43 + 7 minutes of the fixture's bad days")
	require.Equal(t, "Last 90 days: no data", bars[2][2], "Docs has days but no availability")

	p := fixturePage("auto")
	p.ShowUptime = false
	for _, m := range barRe.FindAllStringSubmatch(render(t, p), -1) {
		require.Equal(t, "Daily status over the last 90 days", m[2], "no figures when the page hides them")
	}
}

func TestHeadHasMetaOpenGraphAndTheFeedLink(t *testing.T) {
	html := render(t, fixturePage("auto"))
	require.Contains(t, html, `<meta property="og:type" content="website">`)
	require.Contains(t, html, `<meta property="og:title" content="Acme Status">`)
	require.Contains(t, html, `<meta property="og:description" content="Live status of Acme services. Questions: status@acme.example">`)
	require.Contains(t, html, `<link rel="alternate" type="application/atom+xml" title="Acme Status updates" href="/feed.atom">`)
	require.Contains(t, html, `<a href="/feed.atom">Atom feed</a>`)

	p := fixturePage("auto")
	p.Description = ""
	html = render(t, p)
	require.Contains(t, html, `<meta name="description" content="Partial outage">`, "no description: the state")
	require.Contains(t, html, `<meta property="og:description" content="Partial outage">`)

	p.Description = strings.Repeat("x", 300)
	html = render(t, p)
	require.Contains(t, html, `content="`+strings.Repeat("x", 154)+`…">`)
	require.NotContains(t, html, strings.Repeat("x", 156)+`">`, "the meta description stops at 155 characters")
	require.Contains(t, html, `<p class="desc">`+strings.Repeat("x", 300)+`</p>`, "the page itself shows the whole text")
}

func TestIncidentsAndWindowsHaveAnchors(t *testing.T) {
	html := render(t, fixturePage("auto"))
	for _, id := range []string{"inc-k-inc-1", "inc-k-inc-2", "mw-k-mw-1", "mw-k-mw-2"} {
		require.Containsf(t, html, ` id="`+id+`"`, id)
	}
	p := fixturePage("auto")
	p.ActiveIncidents[0].Key = ""
	p.Maintenance[0].Key = "a b\"><x"
	html = render(t, p)
	require.Equal(t, 1, strings.Count(html, `id="inc-`), "only the past incident keeps an anchor; the one without a key has none")
	require.Contains(t, html, ` id="mw-a_b___x"`, "an unsafe key is made safe")
}

func TestSlugs(t *testing.T) {
	for in, want := range map[string]string{
		"API":                    "api",
		"Customer API (EU)":      "customer-api-eu",
		"  --Web  Site-- ":       "web-site",
		"a/b\\c":                 "a-b-c",
		"Café":                   "caf",
		"日本語":                    "component",
		"":                       "component",
		strings.Repeat("a", 200): strings.Repeat("a", maxSlug),
	} {
		require.Equalf(t, want, slugify(in), "%q", in)
		require.True(t, validSlug(slugify(in)))
	}
}

func TestComponentSlugsAreUniqueAndFollowPageOrder(t *testing.T) {
	p := statuspages.PublicPage{Groups: []statuspages.PublicGroup{
		{Components: []statuspages.PublicComponent{{Name: "API"}, {Name: "Api"}}},
		{Name: "g", Components: []statuspages.PublicComponent{{Name: "api!"}, {Name: "API 2"}, {Name: "日本"}, {Name: "語"}}},
	}}
	var got []string
	for _, r := range componentRefs(p) {
		got = append(got, r.Slug)
	}
	require.Equal(t, []string{"api", "api-2", "api-3", "api-2-2", "component", "component-2"}, got)
	c, ok := findComponent(p, "api-3")
	require.True(t, ok)
	require.Equal(t, "api!", c.Name)
	_, ok = findComponent(p, "nope")
	require.False(t, ok)

	long := strings.Repeat("a", maxSlug)
	p = statuspages.PublicPage{Groups: []statuspages.PublicGroup{{Components: []statuspages.PublicComponent{{Name: long}, {Name: long}}}}}
	refs := componentRefs(p)
	require.Equal(t, long, refs[0].Slug)
	require.LessOrEqual(t, len(refs[1].Slug), maxSlug)
	require.True(t, strings.HasSuffix(refs[1].Slug, "-2"))
	require.True(t, validSlug(refs[1].Slug))
}
