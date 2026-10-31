package pages

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRenderEscapesCustomText(t *testing.T) {
	eta := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	out, err := Render(Page{
		Kind:    "maintenance",
		Title:   `<script>alert("t")</script>`,
		Message: "line one\n<img src=x onerror=alert(1)> & more",
		ETA:     &eta,
		Host:    `a"><b>.example.com`,
	})
	require.NoError(t, err)
	html := string(out)
	require.NotContains(t, html, `<script>alert("t")</script>`)
	require.NotContains(t, html, "<img src=x")
	require.NotContains(t, html, "<b>.example.com")
	require.Contains(t, html, "&lt;script&gt;alert(&#34;t&#34;)&lt;/script&gt;")
	require.Contains(t, html, "&lt;img src=x onerror=alert(1)&gt; &amp; more")
	require.Contains(t, html, `<time datetime="2026-11-02T09:30:00Z">Nov 2, 2026 09:30 UTC</time>`)
	// The only scripts are ours: the page script and the engine.
	require.Equal(t, 2, strings.Count(html, "<script>"))
}

func TestRenderDefaultsAndKinds(t *testing.T) {
	out, err := Render(Page{Kind: "maintenance"})
	require.NoError(t, err)
	require.Contains(t, string(out), DefaultTitle)
	require.Contains(t, string(out), DefaultMessage)
	require.NotContains(t, string(out), "<time")

	for _, k := range ErrorKinds {
		b, err := Render(Page{Kind: k})
		require.NoError(t, err, k)
		require.Contains(t, string(b), `data-kind="`+k+`"`)
	}
	nf, _ := Render(Page{Kind: "404"})
	require.Contains(t, string(nf), "No service is configured for this address.")

	_, err = Render(Page{Kind: "nope"})
	require.Error(t, err)
}

func TestRenderIsSelfContained(t *testing.T) {
	out, err := Render(Page{Kind: "maintenance", Host: "app.example.com"})
	require.NoError(t, err)
	html := string(out)
	for _, bad := range []string{"<link", "src=\"http", "href=\"http", "url(http", "@import", "<img"} {
		require.NotContains(t, html, bad)
	}
	require.Contains(t, html, "app.example.com")
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	require.Equal(t, "120", RetryAfter(nil, now))
	past := now.Add(-time.Minute)
	require.Equal(t, "300", RetryAfter(&past, now))
	future := now.Add(2 * time.Hour)
	got := RetryAfter(&future, now)
	require.Equal(t, "Mon, 26 Oct 2026 14:00:00 GMT", got)
	_, err := http.ParseTime(got)
	require.NoError(t, err)
}

func TestBuildNginxAndHash(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	eta := now.Add(30 * time.Minute)
	svc := "11111111-1111-1111-1111-111111111111"
	b, err := Build([]Entry{{ServiceID: svc, Host: "app.example.com", Maintenance: agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta}}}, now)
	require.NoError(t, err)
	conf, ok := b.File(ConfPath)
	require.True(t, ok)
	c := string(conf)
	require.Contains(t, c, "location = /m/"+svc+" { error_page 503 /p/m-"+svc+".html; return 503; }")
	require.Contains(t, c, `add_header Retry-After "Mon, 26 Oct 2026 12:30:00 GMT" always;`)
	require.Contains(t, c, "location = /err/404 { return 404; }")
	require.Contains(t, c, `add_header Cache-Control "no-store" always;`)
	require.True(t, b.Has(svc))
	page, ok := b.File(Root + "/p/m-" + svc + ".html")
	require.True(t, ok)
	require.Contains(t, string(page), "Upgrade")

	same, err := Build([]Entry{{ServiceID: svc, Host: "app.example.com", Maintenance: agentprotocol.Maintenance{Title: "Upgrade", ETA: &eta}}}, now)
	require.NoError(t, err)
	require.Equal(t, b.Hash, same.Hash)
	other, err := Build([]Entry{{ServiceID: svc, Host: "app.example.com", Maintenance: agentprotocol.Maintenance{Title: "Other", ETA: &eta}}}, now)
	require.NoError(t, err)
	require.NotEqual(t, b.Hash, other.Hash)
	none, err := Build(nil, now)
	require.NoError(t, err)
	require.False(t, none.Has(svc))
	require.NotEqual(t, b.Hash, none.Hash)

	_, err = Build([]Entry{{ServiceID: "../x", Maintenance: agentprotocol.Maintenance{}}}, now)
	require.Error(t, err)
}

func TestEntriesUnion(t *testing.T) {
	routes := []agentprotocol.Route{
		{ServiceID: "b", Hosts: []string{"b.example.com", "b2.example.com"}, RouteExt: agentprotocol.RouteExt{Maintenance: &agentprotocol.Maintenance{Message: "x"}}},
		{ServiceID: "a", Hosts: []string{"a.example.com"}},
		{ServiceID: "c", Hosts: []string{"c.example.com"}, RouteExt: agentprotocol.RouteExt{Maintenance: &agentprotocol.Maintenance{}}},
		{ServiceID: "bad/id", RouteExt: agentprotocol.RouteExt{Maintenance: &agentprotocol.Maintenance{}}},
		{ServiceID: "l4", RouteExt: agentprotocol.RouteExt{Protocol: "tcp", Maintenance: &agentprotocol.Maintenance{}}},
	}
	es := Entries(routes)
	require.Len(t, es, 2)
	require.Equal(t, "b", es[0].ServiceID)
	require.Equal(t, "b.example.com", es[0].Host)
	require.Equal(t, "c", es[1].ServiceID)

	u := Union([]Entry{{ServiceID: "b", Host: "old"}, {ServiceID: "z"}}, es)
	require.Len(t, u, 3)
	require.Equal(t, "b.example.com", u[0].Host, "the new entry wins")
	require.Equal(t, "z", u[2].ServiceID)
}

func TestArchive(t *testing.T) {
	b, err := Build(nil, time.Now())
	require.NoError(t, err)
	r, err := b.Archive()
	require.NoError(t, err)
	raw := new(bytes.Buffer)
	_, err = raw.ReadFrom(r)
	require.NoError(t, err)
	require.Contains(t, raw.String(), "etc/nginx/nginx.conf")
	require.Contains(t, raw.String(), "usr/share/nebulactrl-pages/p/404.html")
}
