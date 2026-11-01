package statuspublic

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/statuspages"
	"github.com/nebulactrl/nebulactrl/internal/uptime"
)

const testHost = "status.acme.example"

func oneHost() (*fakeSource, *clock, *Handler) {
	src := newSource(map[string]statuspages.PublicPage{testHost: fixturePage("auto")})
	clk := newClock()
	return src, clk, newTestHandler(src, clk, nil)
}

// The cache is rebuilt on the events the two publishers send; a rename on either
// side must fail here rather than silently stop invalidating.
func TestEventNamesMatchTheirPublishers(t *testing.T) {
	require.Equal(t, statuspages.EventPageUpdated, EventStatusPageUpdated)
	require.Equal(t, uptime.EventStateChanged, EventUptimeStateChanged)
}

func TestHostLookupStripsPortAndCase(t *testing.T) {
	src, _, h := oneHost()
	for _, host := range []string{testHost, "STATUS.Acme.Example", testHost + ":443", "Status.ACME.example.:8443"} {
		w := get(h, host, "/")
		require.Equalf(t, http.StatusOK, w.Code, "host %q", host)
		require.Contains(t, w.Body.String(), "<h1>Acme Status</h1>")
	}
	require.Equal(t, 1, src.count(testHost), "every spelling resolves to the same cached host")
	require.Len(t, src.calls, 1, "the source only ever sees the normalized hostname")
}

func TestUnknownHostsPathsAndMethodsAreRefused(t *testing.T) {
	src, _, h := oneHost()

	w := get(h, "other.example", "/")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "Page not found")
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "Acme")

	for _, p := range []string{"/admin", "/api/v1/me", "/agent/v1/connect", "/mcp", "/setup", "/login", "/status.json/", "/index.html", "/../etc/passwd", "/robots.txt"} {
		w := get(h, testHost, p)
		require.Equalf(t, http.StatusNotFound, w.Code, p)
		require.Contains(t, w.Body.String(), "Page not found", p)
		require.NotContains(t, strings.ToLower(w.Body.String()), "<script", p)
	}
	require.Zero(t, src.count(testHost), "unknown paths never reach the database")

	for _, host := range []string{"203.0.113.7", "[2001:db8::1]:443", "bad host", "a/b.example", "", "x:y"} {
		require.Equalf(t, http.StatusNotFound, get(h, host, "/").Code, "host %q", host)
	}
	require.Zero(t, src.count("203.0.113.7"))

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		w := do(h, m, testHost, "/")
		require.Equalf(t, http.StatusMethodNotAllowed, w.Code, m)
		require.Equal(t, "GET, HEAD", w.Header().Get("Allow"))
	}
}

func TestHealthzAnswersForAnyHostWithoutLookup(t *testing.T) {
	src, _, h := oneHost()
	for _, host := range []string{"nebulactrl:8081", "anything.example", testHost} {
		w := get(h, host, "/healthz")
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "ok\n", w.Body.String())
	}
	require.Zero(t, src.count(testHost))
	require.Empty(t, src.calls)
}

// allowedJSONKeys is the public contract of status.json; a key outside it is a
// leak until someone decides otherwise here.
var allowedJSONKeys = map[string]bool{
	"title": true, "description": true, "footer": true, "status": true, "state": true, "label": true, "showUptime": true,
	"generatedAt": true, "groups": true, "name": true, "slug": true, "url": true, "components": true, "uptime": true, "uptime24h": true, "days": true,
	"date": true, "downMinutes": true, "maintenance": true, "incidents": true, "activeIncidents": true, "key": true,
	"pastIncidents": true, "impact": true, "startedAt": true, "resolvedAt": true, "updates": true,
	"body": true, "at": true, "startsAt": true, "endsAt": true, "active": true,
}

func collectKeys(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			into[k] = true
			collectKeys(c, into)
		}
	case []any:
		for _, c := range x {
			collectKeys(c, into)
		}
	}
}

func TestStatusJSONHasOnlyPublicFields(t *testing.T) {
	_, _, h := oneHost()
	w := get(h, testHost, "/status.json")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
	keys := map[string]bool{}
	collectKeys(doc, keys)
	for k := range keys {
		require.Truef(t, allowedJSONKeys[k], "status.json leaks the field %q", k)
	}
	body := w.Body.String()
	for _, internal := range []string{"serviceId", "workspace", "hostname", "\"id\"", "theme", "error"} {
		require.NotContains(t, body, internal)
	}
	require.Contains(t, body, `"footer":"Acme Inc.\nContact: status@acme.example \u003cb\u003e"`)
	require.Contains(t, body, `"url":"https://api.acme.example/docs"`)
	require.NotContains(t, body, "javascript:", "a link that is not https never reaches the JSON")
	require.Contains(t, body, `"slug":"api"`)
	require.Contains(t, body, `"uptime24h":99.98`)
	require.Contains(t, body, `"key":"k-inc-1"`)
	require.Contains(t, body, `"key":"k-mw-1"`)

	require.Equal(t, "Acme Status", doc["title"])
	require.Equal(t, map[string]any{"state": "partial_outage", "label": "Partial outage"}, doc["status"])
	groups := doc["groups"].([]any)
	require.Len(t, groups, 2)
	api := groups[0].(map[string]any)["components"].([]any)[0].(map[string]any)
	require.Equal(t, "API", api["name"])
	require.InDelta(t, 99.99, api["uptime"], 1e-9, "0.99996 is truncated to 99.99, never rounded up to 100")
	days := api["days"].([]any)
	require.Len(t, days, 90)
	require.Equal(t, "operational", days[89].(map[string]any)["status"])

	dash := groups[1].(map[string]any)["components"].([]any)[0].(map[string]any)
	dd := dash["days"].([]any)
	var statuses []string
	for _, d := range dd[:6] {
		statuses = append(statuses, d.(map[string]any)["status"].(string))
	}
	require.Equal(t, []string{"no_data", "maintenance", "outage", "degraded", "minor_issues", "operational"}, statuses)
	require.Equal(t, []any{"Database failover"}, dd[2].(map[string]any)["incidents"])

	unknown := groups[1].(map[string]any)["components"].([]any)[3].(map[string]any)
	require.Equal(t, map[string]any{"state": "unknown", "label": "No data"}, unknown["status"], "a state the renderer does not know is shown as unknown")
	require.Empty(t, unknown["days"])
	require.NotContains(t, unknown, "uptime")
}

func TestStatusJSONHidesUptimeWhenThePageDoes(t *testing.T) {
	p := fixturePage("auto")
	p.ShowUptime = false
	h := newTestHandler(newSource(map[string]statuspages.PublicPage{testHost: p}), newClock(), nil)

	w := get(h, testHost, "/status.json")
	body := w.Body.String()
	require.NotContains(t, body, `"uptime"`)
	require.NotContains(t, body, "uptime24h")
	require.NotContains(t, body, "downMinutes")
	require.Contains(t, body, `"showUptime":false`)
	require.Contains(t, body, `"status":"outage"`, "the band is still public")

	html := get(h, testHost, "/").Body.String()
	require.NotContains(t, html, "uptime", "no percentage, no word 'uptime' on the page")
	require.NotContains(t, html, "min down")
	require.NotContains(t, html, "99.99")
	require.Contains(t, html, "Down", "day titles say the band instead")
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	_, _, h := oneHost()
	for _, p := range []string{"/", "/status.json", "/feed.atom", "/badge/status.svg", "/badge/api/uptime.svg", "/status.css?v=x", "/status.js?v=x", "/healthz", "/nope", "/fonts/ibm-plex-sans.woff2"} {
		w := get(h, testHost, p)
		hd := w.Header()
		require.Containsf(t, hd.Get("Content-Security-Policy"), "default-src 'none'", p)
		require.Containsf(t, hd.Get("Content-Security-Policy"), "frame-ancestors 'none'", p)
		require.Containsf(t, hd.Get("Content-Security-Policy"), "img-src 'self' data:", p)
		require.Containsf(t, hd.Get("Content-Security-Policy"), "font-src 'self'", p)
		require.NotContainsf(t, hd.Get("Content-Security-Policy"), "unsafe", p)
		require.Containsf(t, hd.Get("Content-Security-Policy"), "script-src 'self';", p)
		require.Containsf(t, hd.Get("Content-Security-Policy"), "connect-src 'self';", p, "only the page's own address, for the script that refreshes it")
		require.Equalf(t, "nosniff", hd.Get("X-Content-Type-Options"), p)
		require.Equalf(t, "no-referrer", hd.Get("Referrer-Policy"), p)
		require.Equalf(t, "DENY", hd.Get("X-Frame-Options"), p)
	}
	for _, p := range []string{"/", "/status.json", "/feed.atom"} {
		require.Equal(t, "public, max-age=30", get(h, testHost, p).Header().Get("Cache-Control"), p)
	}
	require.Equal(t, "public, max-age=300", get(h, testHost, "/badge/status.svg").Header().Get("Cache-Control"))
	for _, c := range []struct {
		host, path string
		want       int
	}{{"other.example", "/", 404}, {testHost, "/nope", 404}} {
		require.Equal(t, "no-store", get(h, c.host, c.path).Header().Get("Cache-Control"), "errors are never cached")
	}
}

// The page must work under its own CSP: no inline script or style, no handler
// attributes, and no script but its own same-origin one, whatever the content
// says.
func TestPageNeedsNothingTheCSPForbids(t *testing.T) {
	p := fixturePage("dark")
	p.Title = `"><script>alert(1)</script>`
	p.Description = `<img src=x onerror=alert(2)>`
	p.ActiveIncidents[0].Updates[0].Body = `<script>alert(3)</script> & "quotes"`
	p.Groups[1].Components[0].Name = `<b onmouseover=alert(4)>Dash</b>`
	h := newTestHandler(newSource(map[string]statuspages.PublicPage{testHost: p}), newClock(), nil)

	html := get(h, testHost, "/").Body.String()
	for _, bad := range []string{"<style", "javascript:", " style=", "<img", "<b "} {
		require.NotContainsf(t, strings.ToLower(html), bad, "page contains %q", bad)
	}
	require.Equal(t, 1, strings.Count(html, "<script"), "only the page's own script")
	require.Regexp(t, `<script src="/status\.js\?v=[0-9a-f]{8}" defer></script>`, html)
	require.Contains(t, html, "&lt;script&gt;alert(3)&lt;/script&gt;")
	require.Contains(t, html, `<link rel="stylesheet" href="/status.css?v=`)
	require.Contains(t, html, `<noscript><meta http-equiv="refresh" content="60"></noscript>`)
}

func TestRateLimitPerClient(t *testing.T) {
	src := newSource(map[string]statuspages.PublicPage{testHost: fixturePage("auto")})
	clk := newClock()
	h := newTestHandler(src, clk, func(o *Options) { o.RatePerMinute = 3 })

	from := func(xff string) *httptest.ResponseRecorder {
		return get(h, testHost, "/", "X-Forwarded-For", xff)
	}
	for range 3 {
		require.Equal(t, http.StatusOK, from("198.51.100.1").Code)
	}
	w := from("198.51.100.1")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Header().Get("Content-Security-Policy"), "default-src 'none'")

	require.Equal(t, http.StatusOK, from("198.51.100.2").Code, "another client has its own bucket")
	require.Equal(t, http.StatusOK, get(h, testHost, "/healthz", "X-Forwarded-For", "198.51.100.1").Code, "the proxy's health check is never limited")
	require.Equal(t, http.StatusNotFound, get(h, testHost, "/nope", "X-Forwarded-For", "198.51.100.1").Code, "junk paths cost nothing and are not counted")

	// Only the last X-Forwarded-For entry counts: a client cannot pick its own bucket.
	require.Equal(t, http.StatusTooManyRequests, from("203.0.113.99, 198.51.100.1").Code)

	// IPv6 clients share their /64.
	for range 3 {
		require.Equal(t, http.StatusOK, from("2001:db8:1:2::1").Code)
	}
	require.Equal(t, http.StatusTooManyRequests, from("2001:db8:1:2:ffff::7").Code)
	require.Equal(t, http.StatusOK, from("2001:db8:1:3::1").Code)

	clk.advance(30 * time.Second) // one token per 20 s
	require.Equal(t, http.StatusOK, from("198.51.100.1").Code)
	require.Equal(t, http.StatusTooManyRequests, from("198.51.100.1").Code)
}

var cssLink = regexp.MustCompile(`href="(/status\.css\?v=[0-9a-f]+)"`)

func TestRateLimitUsesThePeerWhenNotBehindTheProxy(t *testing.T) {
	_, _, h := oneHost()
	h.limiter = newLimiter(2)
	hit := func(remote, xff string) int {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = testHost
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	// A public peer reaches the listener directly and writes any header: the header is ignored.
	require.Equal(t, 200, hit("203.0.113.5:1000", "1.1.1.1"))
	require.Equal(t, 200, hit("203.0.113.5:1001", "2.2.2.2"))
	require.Equal(t, 429, hit("203.0.113.5:1002", "3.3.3.3"))
}

func TestLimiterTableIsBounded(t *testing.T) {
	l := newLimiter(1)
	l.max = 3
	now := newClock().now()
	ips := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"}
	for _, ip := range ips {
		l.allow(netip.MustParseAddr(ip), now)
	}
	require.Equal(t, 3, l.size(), "the table never grows past its bound")

	// Clients that arrive while it is full share the overflow bucket instead of running unlimited.
	allowed := 0
	for i := range 200 {
		if l.allow(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), now.Add(time.Duration(i)*time.Microsecond)) {
			allowed++
		}
	}
	require.Less(t, allowed, 200)
	require.Equal(t, 3, l.size())

	// Idle buckets are full again and are dropped to make room.
	later := now.Add(2 * time.Minute)
	require.True(t, l.allow(netip.MustParseAddr("192.0.2.77"), later))
	require.LessOrEqual(t, l.size(), 3)
	require.Contains(t, l.clients, netip.MustParseAddr("192.0.2.77"))
}

func TestCacheReusesAndRefreshesAfterTheTTL(t *testing.T) {
	src, clk, h := oneHost()
	get(h, testHost, "/")
	get(h, testHost, "/status.json")
	get(h, testHost, "/badge/api/status.svg")
	get(h, testHost, "/feed.atom")
	require.Equal(t, 1, src.count(testHost), "page, JSON and badge share one lookup")

	clk.advance(19 * time.Second)
	get(h, testHost, "/")
	require.Equal(t, 1, src.count(testHost))

	p := fixturePage("auto")
	p.Title = "Acme Status v2"
	src.set(testHost, p)
	clk.advance(2 * time.Second) // 21 s
	require.Contains(t, get(h, testHost, "/").Body.String(), "Acme Status v2")
	require.Equal(t, 2, src.count(testHost))
}

func TestUnknownHostsAreCachedToo(t *testing.T) {
	src, _, h := oneHost()
	for range 5 {
		require.Equal(t, 404, get(h, "nope.example", "/").Code)
	}
	require.Equal(t, 1, src.count("nope.example"))
}

func TestEventsInvalidateTheCache(t *testing.T) {
	src := newSource(map[string]statuspages.PublicPage{testHost: fixturePage("auto")})
	clk := newClock()
	bus := events.NewBus()
	h := newTestHandler(src, clk, func(o *Options) { o.Bus = bus })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.Watch(ctx)

	generation := func() uint64 {
		h.cache.mu.Lock()
		defer h.cache.mu.Unlock()
		return h.cache.gen
	}
	// Wait until the watcher is subscribed (events published before are lost).
	require.Eventually(t, func() bool {
		bus.Publish(events.Event{Type: EventStatusPageUpdated})
		return generation() > 0
	}, 5*time.Second, 5*time.Millisecond)

	// Let the events of the wait loop above drain before counting generations.
	for last, stable := generation(), 0; stable < 5; {
		time.Sleep(10 * time.Millisecond)
		if cur := generation(); cur == last {
			stable++
		} else {
			last, stable = cur, 0
		}
	}

	get(h, testHost, "/")
	n := src.count(testHost)
	get(h, testHost, "/")
	require.Equal(t, n, src.count(testHost), "still cached")

	g := generation()
	bus.Publish(events.Event{Type: events.TypeProjectUpdated}) // unrelated
	bus.Publish(events.Event{Type: EventUptimeStateChanged, ResourceID: "svc"})
	require.Eventually(t, func() bool { return generation() > g }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, g+1, generation(), "only the two page and uptime events invalidate")

	p := fixturePage("auto")
	p.Overall = statuspages.StateMajorOutage
	src.set(testHost, p)
	get(h, testHost, "/")
	require.Equal(t, n, src.count(testHost), "a page younger than the rebuild floor is not rebuilt for every event")

	clk.advance(3 * time.Second)
	require.Contains(t, get(h, testHost, "/").Body.String(), "Major outage", "rebuilt well inside the TTL")
	require.Equal(t, n+1, src.count(testHost))
	get(h, testHost, "/")
	require.Equal(t, n+1, src.count(testHost), "and cached again")
}

func TestConcurrentRequestsShareOneLookup(t *testing.T) {
	src, _, h := oneHost()
	src.block = make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 12)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = get(h, testHost, "/", "X-Forwarded-For", "198.51.100."+string(rune('1'+i%9))).Code
		}()
	}
	require.Eventually(t, func() bool { return src.count(testHost) >= 1 }, 5*time.Second, time.Millisecond)
	time.Sleep(100 * time.Millisecond) // let the others pile up behind the first lookup
	close(src.block)
	wg.Wait()
	for _, c := range codes {
		require.Equal(t, 200, c)
	}
	require.Equal(t, 1, src.count(testHost), "one lookup per host at a time")
}

func TestStalePageIsServedWhileTheLookupFails(t *testing.T) {
	src, clk, h := oneHost()
	require.Equal(t, 200, get(h, testHost, "/").Code)

	src.fail(errors.New("database down"))
	clk.advance(time.Minute)
	w := get(h, testHost, "/")
	require.Equal(t, 200, w.Code, "the page that explains an outage stays up")
	require.Contains(t, w.Body.String(), "Acme Status")

	clk.advance(11 * time.Minute)
	w = get(h, testHost, "/")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "10", w.Header().Get("Retry-After"))
	require.NotContains(t, w.Body.String(), "database down", "no error text on a public page")

	src.fail(nil)
	require.Equal(t, 200, get(h, testHost, "/").Code, "and it recovers")
}

func TestAPanickingLookupDoesNotWedgeTheHost(t *testing.T) {
	c := newCache(time.Second, time.Minute, 4, newClock().now)
	_, err := c.get(t.Context(), "a.example", func(context.Context) (*entry, error) { panic("boom") })
	require.ErrorContains(t, err, "boom")
	e, err := c.get(t.Context(), "a.example", func(context.Context) (*entry, error) { return &entry{}, nil })
	require.NoError(t, err)
	require.NotNil(t, e)
}

func TestCacheEvictsInventedHostsBeforeRealPages(t *testing.T) {
	clk := newClock()
	c := newCache(20*time.Second, time.Hour, 3, clk.now)
	build := func(miss bool) func(context.Context) (*entry, error) {
		return func(context.Context) (*entry, error) { return &entry{notFound: miss}, nil }
	}
	_, _ = c.get(t.Context(), "real.example", build(false))
	clk.advance(time.Second)
	_, _ = c.get(t.Context(), "miss1.example", build(true))
	clk.advance(time.Second)
	_, _ = c.get(t.Context(), "miss2.example", build(true))
	for i := range 20 {
		clk.advance(time.Second)
		_, _ = c.get(t.Context(), "spray"+string(rune('a'+i))+".example", build(true))
		require.LessOrEqual(t, c.size(), 3)
	}
	c.mu.Lock()
	_, ok := c.entries["real.example"]
	c.mu.Unlock()
	require.True(t, ok, "invented hosts never push a real page out")
}

func TestConditionalRequestsHeadAndGzip(t *testing.T) {
	_, _, h := oneHost()
	w := get(h, testHost, "/")
	etag := w.Header().Get("ETag")
	require.NotEmpty(t, etag)
	plain := w.Body.Bytes()
	require.Greater(t, len(plain), 4096, "the fixture page is big enough to compress")

	w304 := get(h, testHost, "/", "If-None-Match", etag)
	require.Equal(t, http.StatusNotModified, w304.Code)
	require.Empty(t, w304.Body.Bytes())
	require.Contains(t, w304.Header().Get("Content-Security-Policy"), "default-src 'none'")

	wh := do(h, http.MethodHead, testHost, "/")
	require.Equal(t, 200, wh.Code)
	require.Empty(t, wh.Body.Bytes())
	require.Equal(t, w.Header().Get("Content-Length"), wh.Header().Get("Content-Length"))

	wz := get(h, testHost, "/", "Accept-Encoding", "br, gzip;q=0.8")
	require.Equal(t, "gzip", wz.Header().Get("Content-Encoding"))
	require.Contains(t, wz.Header().Values("Vary"), "Accept-Encoding")
	zr, err := gzip.NewReader(bytes.NewReader(wz.Body.Bytes()))
	require.NoError(t, err)
	unz, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.Equal(t, plain, unz)
	require.Less(t, wz.Body.Len(), len(plain)/2)

	require.Empty(t, get(h, testHost, "/", "Accept-Encoding", "gzip;q=0").Header().Get("Content-Encoding"))
	require.Empty(t, get(h, testHost, "/").Header().Get("Content-Encoding"))
}

func TestBadgeRoutes(t *testing.T) {
	_, _, h := oneHost()
	w := get(h, testHost, "/badge/api/status.svg")
	require.Equal(t, 200, w.Code)
	require.Equal(t, "image/svg+xml; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))
	require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "cross-origin", w.Header().Get("Cross-Origin-Resource-Policy"))
	require.Contains(t, w.Header().Get("Content-Security-Policy"), "default-src 'none'")
	require.Contains(t, w.Body.String(), "API: Up")

	require.Contains(t, get(h, testHost, "/badge/status.svg").Body.String(), "status: Partial outage")
	require.Contains(t, get(h, testHost, "/badge/docs/status.svg").Body.String(), "Docs: Maintenance")
	require.Contains(t, get(h, testHost, "/badge/dashboard/status.svg").Body.String(), "Dashboard: Down")
	require.Contains(t, get(h, testHost, "/badge/mail/status.svg").Body.String(), "Mail: No data")

	require.Contains(t, get(h, testHost, "/badge/api/uptime.svg").Body.String(), "API uptime (90d): 99.99%")
	require.Contains(t, get(h, testHost, "/badge/api/uptime.svg?window=90d").Body.String(), "99.99%")
	require.Contains(t, get(h, testHost, "/badge/api/uptime.svg?window=24h").Body.String(), "API uptime (24h): 99.98%")
	require.Contains(t, get(h, testHost, "/badge/dashboard/uptime.svg?window=24h").Body.String(), "95%")
	require.Contains(t, get(h, testHost, "/badge/docs/uptime.svg").Body.String(), "N/A", "a component without data")
	require.Equal(t, http.StatusBadRequest, get(h, testHost, "/badge/api/uptime.svg?window=7d").Code)

	// Only the /badge/status.svg and /badge/<slug>/... schemes exist; a ?component= query is not one.
	for _, p := range []string{
		"/badge.svg", "/badge.svg?component=API", "/badge/nope/status.svg", "/badge/nope/uptime.svg", "/badge/API/status.svg",
		"/badge/api/other.svg", "/badge/api", "/badge/api/", "/badge//status.svg", "/badge/api/status.svg/x", "/badge/a_b/status.svg",
	} {
		require.Equalf(t, http.StatusNotFound, get(h, testHost, p).Code, p)
	}

	// Only badges are cached for 5 minutes and open to other origins' scripts' reads.
	for _, p := range []string{"/", "/feed.atom"} {
		hd := get(h, testHost, p).Header()
		require.Equal(t, "public, max-age=30", hd.Get("Cache-Control"), p)
		require.Empty(t, hd.Get("Access-Control-Allow-Origin"), p)
	}

	// A page that hides uptime hides it from badges too, but still has a status badge.
	p := fixturePage("auto")
	p.ShowUptime = false
	hh := newTestHandler(newSource(map[string]statuspages.PublicPage{testHost: p}), newClock(), nil)
	require.Equal(t, 404, get(hh, testHost, "/badge/api/uptime.svg").Code)
	require.Contains(t, get(hh, testHost, "/badge/api/status.svg").Body.String(), "API: Up")
}

func TestBadgeOutputIsEscapedAndWellFormed(t *testing.T) {
	p := fixturePage("auto")
	p.Groups[0].Components[0].Name = `A<&>"B'`
	hh := newTestHandler(newSource(map[string]statuspages.PublicPage{testHost: p}), newClock(), nil)
	for _, path := range []string{
		"/badge/a-b/status.svg",
		"/badge/a-b/uptime.svg",
		"/badge/a-b/status.svg?label=%3Cscript%3Ealert(1)%3C/script%3E&upLabel=%22+onload%3D%22x&style=for-the-badge",
		"/badge/status.svg?color=%22%3E%3Cscript%3E&labelColor=url(javascript:x)&style=bogus",
	} {
		w := get(hh, testHost, path)
		require.Equalf(t, 200, w.Code, path)
		b := w.Body.Bytes()
		dec := xml.NewDecoder(bytes.NewReader(b))
		for {
			_, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoErrorf(t, err, "%s: %s", path, b)
		}
		s := string(b)
		require.NotContains(t, s, "<script", path)
		require.NotContains(t, s, `<&>`, path)
		require.NotContains(t, s, `onload="x`, path, "an attribute cannot be injected")
		require.NotContains(t, s, "javascript:", path)
	}
}

func TestStaticAssetsAndFonts(t *testing.T) {
	src := newSource(map[string]statuspages.PublicPage{testHost: fixturePage("auto")})
	fonts := map[string][]byte{
		"ibm-plex-sans-latin-wght-normal": []byte("sans-bytes"),
		"ibm-plex-mono-latin-400-normal":  []byte("mono-400"),
	}
	h := newTestHandler(src, newClock(), func(o *Options) {
		o.Fonts = func(stem string) ([]byte, bool) { b, ok := fonts[stem]; return b, ok }
	})

	page := get(h, testHost, "/").Body.String()
	require.Regexp(t, cssLink, page)
	href := cssLink.FindStringSubmatch(page)[1]

	css := get(h, "no-such-host.example", href) // assets are not tied to a host
	require.Equal(t, 200, css.Code)
	require.Equal(t, "text/css; charset=utf-8", css.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=31536000, immutable", css.Header().Get("Cache-Control"))
	sheet := css.Body.String()
	require.Contains(t, sheet, `@media (prefers-color-scheme: dark)`)
	require.Equal(t, 2, strings.Count(sheet, "--surface: #1a1d20"), "dark variables for the automatic and the forced dark theme")
	require.NotContains(t, sheet, "/*DARK")
	require.Contains(t, sheet, `@font-face{font-family:"IBM Plex Sans Variable"`)
	require.Contains(t, sheet, `font-family:"IBM Plex Mono";font-style:normal;font-display:swap;font-weight:400`)
	require.NotContains(t, sheet, "font-weight:600;src", "a font file that is not there gets no face")
	require.Contains(t, sheet, "max-width: 640px", "the narrow layout crops the bar to 30 days")
	require.Equal(t, "public, max-age=300", get(h, testHost, "/status.css?v=stale").Header().Get("Cache-Control"))

	jsLink := regexp.MustCompile(`<script src="(/status\.js\?v=[0-9a-f]+)" defer>`)
	require.Regexp(t, jsLink, page)
	js := get(h, "no-such-host.example", jsLink.FindStringSubmatch(page)[1])
	require.Equal(t, 200, js.Code)
	require.Equal(t, "text/javascript; charset=utf-8", js.Header().Get("Content-Type"))
	require.Equal(t, "public, max-age=31536000, immutable", js.Header().Get("Cache-Control"))
	require.Contains(t, js.Body.String(), "aria-live", "the tooltip script announces the focused day")
	require.Contains(t, js.Body.String(), "fetch(location.href", "the script refreshes the page in place instead of reloading it")
	require.Equal(t, "public, max-age=300", get(h, testHost, "/status.js?v=stale").Header().Get("Cache-Control"))

	f := get(h, testHost, pathFontSans)
	require.Equal(t, 200, f.Code)
	require.Equal(t, "font/woff2", f.Header().Get("Content-Type"))
	require.Equal(t, "sans-bytes", f.Body.String())
	require.Equal(t, 404, get(h, testHost, pathFontMono600).Code)

	bare := newTestHandler(src, newClock(), nil)
	require.NotContains(t, get(bare, testHost, "/status.css").Body.String(), "@font-face", "no embedded fonts: the system stack applies")
	require.Equal(t, 404, get(bare, testHost, pathFontSans).Code)
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"status.example.com":      "status.example.com",
		"Status.Example.COM":      "status.example.com",
		"status.example.com:443":  "status.example.com",
		"status.example.com.":     "status.example.com",
		"status.example.com.:80":  "status.example.com",
		"  status.example.com  ":  "status.example.com",
		"my-page_1.example.co.uk": "my-page_1.example.co.uk",
		"xn--bcher-kva.example":   "xn--bcher-kva.example",
	} {
		got, ok := normalizeHost(in)
		require.Truef(t, ok, in)
		require.Equal(t, want, got)
	}
	for _, in := range []string{
		"", ":443", "203.0.113.7", "203.0.113.7:80", "[::1]:80", "::1", "a b.example", "a/b", "a@b.example",
		"status.example.com:80a", "status.example.com:1:2", "ünï.example", strings.Repeat("a", 254),
	} {
		_, ok := normalizeHost(in)
		require.Falsef(t, ok, "%q", in)
	}
}
