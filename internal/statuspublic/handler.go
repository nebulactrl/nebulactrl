// Package statuspublic serves published status pages on their own hostnames.
//
// The control plane runs a second HTTP listener (NEBULACTRL_STATUS_LISTEN,
// default :8081) with this handler and nothing else on it. The control-plane
// host's Traefik routes each published page's hostname to that listener (see
// package statusroute), so a status hostname can never reach the dashboard, the
// API or the agent endpoints: they are not mounted on this listener at all.
//
// The handler looks the page up by Host (statuspages.Service.Public) and
// serves, for GET and HEAD only:
//
//	/                  the page, server-side rendered HTML (complete without JavaScript)
//	/status.json       the same model as JSON with public-safe field names
//	/feed.atom         an Atom feed of incident updates and maintenance windows
//	/badge/status.svg, /badge/<slug>/status.svg, /badge/<slug>/uptime.svg
//	                   SVG badges of the page and of a component
//	/status.css, /status.js, /fonts/*
//	                   the stylesheet, the day-tooltip script and the self-hosted IBM Plex fonts
//	/healthz           liveness for the proxy's health check (any Host)
//
// Everything else is a minimal 404. Pages are cached per host for 20 seconds
// and rebuilt early when a status page or an uptime state changes; requests are
// limited per client address; every response carries a strict CSP.
package statuspublic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/clientip"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/statuspages"
	"github.com/nebulactrl/nebulactrl/internal/webui"
)

// Event types that rebuild the cached pages: published on the event bus by the
// status pages service and by the uptime state machine.
const (
	EventStatusPageUpdated = statuspages.EventPageUpdated
	// EventUptimeStateChanged is uptime.EventStateChanged (not imported: the
	// package pulls in the notification stack; a test pins the two together).
	EventUptimeStateChanged = "uptime.state.changed"
)

const (
	// DefaultCacheTTL is how long a host's rendered page is reused.
	DefaultCacheTTL = 20 * time.Second
	// staleGrace is how long an expired page is still served when rebuilding it
	// fails (the database is down: the page that explains it should stay up).
	staleGrace = 10 * time.Minute
	// maxHosts bounds the per-host cache.
	maxHosts = 256
	// buildTimeout bounds one lookup and render.
	buildTimeout = 10 * time.Second
	// pageCache is the Cache-Control of pages, JSON and badges: shared caches
	// may keep them briefly, which also shields the listener from bursts.
	pageCache = "public, max-age=30"
)

// badgeCache is the Cache-Control of badges: they are embedded in READMEs and
// other pages, so they may be kept longer than the page.
const badgeCache = "public, max-age=300"

// route says what a request path asks for.
type route int

const (
	routeNone route = iota
	routePage
	routeJSON
	routeFeed
	routeCSS
	routeJS
	routeFont
	routeBadgeOverall // /badge/status.svg
	routeBadgeStatus  // /badge/<slug>/status.svg
	routeBadgeUptime  // /badge/<slug>/uptime.svg
)

// classify maps a path to a route and, for a component badge, its slug.
func classify(path string) (route, string) {
	switch path {
	case "/":
		return routePage, ""
	case "/status.json":
		return routeJSON, ""
	case "/feed.atom":
		return routeFeed, ""
	case "/status.css":
		return routeCSS, ""
	case "/status.js":
		return routeJS, ""
	case pathFontSans, pathFontMono400, pathFontMono600:
		return routeFont, ""
	case "/badge/status.svg":
		return routeBadgeOverall, ""
	}
	if rest, ok := strings.CutPrefix(path, "/badge/"); ok {
		slug, file, found := strings.Cut(rest, "/")
		if found && validSlug(slug) {
			switch file {
			case "status.svg":
				return routeBadgeStatus, slug
			case "uptime.svg":
				return routeBadgeUptime, slug
			}
		}
	}
	return routeNone, ""
}

// validSlug reports whether s has the shape slugify produces.
func validSlug(s string) bool {
	if s == "" || len(s) > maxSlug {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// csp allows nothing but same-origin scripts, styles, images (and data: icons)
// and fonts: no inline scripts or styles, no frames, no forms. The page's script
// may fetch the page itself (connect-src 'self') to refresh it without a reload.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Source looks a published page up by its hostname. It returns
// statuspages.ErrNotFound for an unknown or unpublished host.
type Source interface {
	Public(ctx context.Context, hostname string) (statuspages.PublicPage, error)
}

// Options configures a Handler.
type Options struct {
	Pages  Source
	Bus    *events.Bus // optional: without it pages refresh by TTL only
	Logger *slog.Logger
	// CacheTTL defaults to DefaultCacheTTL.
	CacheTTL time.Duration
	// RatePerMinute is the per-client request allowance; default 60.
	RatePerMinute int
	// Fonts finds a font file of the embedded web build by name stem; default
	// webui.Font. Without the files the page uses the system font stack.
	Fonts func(stem string) ([]byte, bool)
	// Now is the clock (tests); default time.Now.
	Now func() time.Time
}

// staticAsset is a file the listener itself serves.
type staticAsset struct {
	ctype string
	body  []byte
	gz    []byte
	etag  string
}

// Handler is the status listener's http.Handler.
type Handler struct {
	pages Source
	bus   *events.Bus
	log   *slog.Logger
	now   func() time.Time

	cache   *cache
	limiter *limiter

	css      *staticAsset
	cssHref  string
	cssVer   string
	js       *staticAsset
	fonts    map[string]*staticAsset // by URL path
	notFound []byte
}

// New builds the handler: it assembles the stylesheet and loads the fonts once.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.Fonts == nil {
		o.Fonts = webui.Font
	}
	h := &Handler{
		pages: o.Pages, bus: o.Bus, log: o.Logger, now: o.Now,
		cache:   newCache(o.CacheTTL, staleGrace, maxHosts, o.Now),
		limiter: newLimiter(o.RatePerMinute),
		fonts:   map[string]*staticAsset{},
	}
	var ff fontFiles
	ff.Sans, _ = o.Fonts("ibm-plex-sans-latin-wght-normal")
	ff.Mono400, _ = o.Fonts("ibm-plex-mono-latin-400-normal")
	ff.Mono600, _ = o.Fonts("ibm-plex-mono-latin-600-normal")
	for path, body := range map[string][]byte{pathFontSans: ff.Sans, pathFontMono400: ff.Mono400, pathFontMono600: ff.Mono600} {
		if len(body) > 0 {
			h.fonts[path] = &staticAsset{ctype: "font/woff2", body: body, etag: etagOf(body)}
		}
	}
	css := []byte(buildCSS(ff))
	h.cssVer = etagOf(css)[1:9]
	h.cssHref = "/status.css?v=" + h.cssVer
	h.css = &staticAsset{ctype: "text/css; charset=utf-8", body: css, gz: gzipped(css), etag: etagOf(css)}
	js := []byte(statusJS)
	h.js = &staticAsset{ctype: "text/javascript; charset=utf-8", body: js, gz: gzipped(js), etag: etagOf(js)}
	h.notFound = renderNotFound(h.cssHref)
	return h
}

// Watch rebuilds cached pages as soon as a status page or an uptime state
// changes, until ctx ends. Without the event, the cache TTL bounds how stale a
// page can be. Run it in a goroutine.
func (h *Handler) Watch(ctx context.Context) {
	if h.bus == nil {
		return
	}
	for ev := range h.bus.Subscribe(ctx) {
		switch ev.Type {
		case EventStatusPageUpdated, EventUptimeStateChanged:
			h.cache.invalidate()
		}
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.plain(w, r, http.StatusMethodNotAllowed, "Method not allowed\n")
		return
	}
	path := r.URL.Path
	if path == "/healthz" {
		h.plain(w, r, http.StatusOK, "ok\n")
		return
	}
	kind, slug := classify(path)
	if kind == routeNone {
		h.missing(w, r)
		return
	}
	if !h.limiter.allow(clientIP(r), h.now()) {
		w.Header().Set("Retry-After", "1")
		h.plain(w, r, http.StatusTooManyRequests, "Too many requests\n")
		return
	}
	switch kind {
	case routeCSS:
		h.serveCSS(w, r)
		return
	case routeJS:
		h.serveJS(w, r)
		return
	case routeFont:
		h.serveFont(w, r, path)
		return
	}

	host, ok := normalizeHost(r.Host)
	if !ok {
		h.missing(w, r)
		return
	}
	e, err := h.cache.get(r.Context(), host, func(ctx context.Context) (*entry, error) { return h.buildEntry(ctx, host) })
	switch {
	case err != nil:
		if r.Context().Err() == nil {
			h.log.ErrorContext(r.Context(), "status page lookup failed", "host", host, "error", err)
		}
		w.Header().Set("Retry-After", "10")
		h.plain(w, r, http.StatusServiceUnavailable, "Status temporarily unavailable\n")
		return
	case e.notFound:
		h.missing(w, r)
		return
	}

	switch kind {
	case routePage:
		h.send(w, r, "text/html; charset=utf-8", e.html, e.htmlGz, e.htmlETag, pageCache, false)
	case routeJSON:
		h.send(w, r, "application/json; charset=utf-8", e.json, e.jsonGz, e.jsonETag, pageCache, true)
	case routeFeed:
		h.send(w, r, "application/atom+xml; charset=utf-8", e.feed, e.feedGz, e.feedETag, pageCache, false)
	default:
		h.serveBadge(w, r, kind, slug, *e.page)
	}
}

// buildEntry looks the page of host up and renders everything served from it.
func (h *Handler) buildEntry(ctx context.Context, host string) (*entry, error) {
	page, err := h.pages.Public(ctx, host)
	if errors.Is(err, statuspages.ErrNotFound) {
		return &entry{notFound: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return h.entryOf(page, host)
}

// entryOf renders everything served from page, which is served on host.
func (h *Handler) entryOf(page statuspages.PublicPage, host string) (*entry, error) {
	html, err := renderHTML(page, h.cssHref, h.now())
	if err != nil {
		return nil, err
	}
	js, err := renderJSON(page)
	if err != nil {
		return nil, err
	}
	feed, err := renderFeed(page, host)
	if err != nil {
		return nil, err
	}
	return &entry{
		html: html, htmlGz: gzipped(html), htmlETag: etagOf(html),
		json: js, jsonGz: gzipped(js), jsonETag: etagOf(js),
		feed: feed, feedGz: gzipped(feed), feedETag: etagOf(feed),
		page: &page,
	}, nil
}

func (h *Handler) serveCSS(w http.ResponseWriter, r *http.Request) {
	cc := "public, max-age=300"
	if r.URL.Query().Get("v") == h.cssVer {
		cc = "public, max-age=31536000, immutable" // the URL carries the content hash
	}
	w.Header().Set("Cache-Control", cc)
	h.write(w, r, h.css.ctype, h.css.body, h.css.gz, h.css.etag)
}

func (h *Handler) serveJS(w http.ResponseWriter, r *http.Request) {
	cc := "public, max-age=300"
	if r.URL.Query().Get("v") == jsVer {
		cc = "public, max-age=31536000, immutable" // the URL carries the content hash
	}
	w.Header().Set("Cache-Control", cc)
	h.write(w, r, h.js.ctype, h.js.body, h.js.gz, h.js.etag)
}

func (h *Handler) serveFont(w http.ResponseWriter, r *http.Request, path string) {
	f, ok := h.fonts[path]
	if !ok {
		h.missing(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	h.write(w, r, f.ctype, f.body, nil, f.etag)
}

// send writes a cacheable body under the Cache-Control cc. cors marks the
// machine-readable ones other sites may fetch and embed.
func (h *Handler) send(w http.ResponseWriter, r *http.Request, ctype string, body, gz []byte, etag, cc string, cors bool) {
	w.Header().Set("Cache-Control", cc)
	if cors {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	h.write(w, r, ctype, body, gz, etag)
}

// serveBadge renders a badge of the page from the request's query.
func (h *Handler) serveBadge(w http.ResponseWriter, r *http.Request, kind route, slug string, p statuspages.PublicPage) {
	q := r.URL.Query()
	o := parseBadgeOpts(q)
	var svg []byte
	switch kind {
	case routeBadgeOverall:
		svg = statusBadge("status", p.Overall, o)
	default:
		c, ok := findComponent(p, slug)
		if !ok {
			h.missing(w, r)
			return
		}
		if kind == routeBadgeStatus {
			svg = statusBadge(c.Name, c.State, o)
			break
		}
		if !p.ShowUptime { // the page hides its figures: so do its badges
			h.missing(w, r)
			return
		}
		switch q.Get("window") {
		case "", "90d":
			svg = uptimeBadge(c.Name+" uptime (90d)", c.Availability, o)
		case "24h":
			svg = uptimeBadge(c.Name+" uptime (24h)", c.Availability24h, o)
		default:
			h.plain(w, r, http.StatusBadRequest, "window must be 24h or 90d\n")
			return
		}
	}
	h.send(w, r, "image/svg+xml; charset=utf-8", svg, nil, etagOf(svg), badgeCache, true)
}

// write sends body, gzipped when the client accepts it, or a 304 when its
// If-None-Match matches etag.
func (h *Handler) write(w http.ResponseWriter, r *http.Request, ctype string, body, gz []byte, etag string) {
	hd := w.Header()
	if etag != "" {
		hd.Set("ETag", etag)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if gz != nil {
		hd.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			hd.Set("Content-Encoding", "gzip")
			body = gz
		}
	}
	hd.Set("Content-Type", ctype)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // pre-rendered, escaped page body
	}
}

// plain writes a short uncacheable text response.
func (h *Handler) plain(w http.ResponseWriter, r *http.Request, status int, msg string) {
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Type", "text/plain; charset=utf-8")
	hd.Set("Content-Length", strconv.Itoa(len(msg)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(msg))
	}
}

// missing is the 404: a minimal page, never cached, the same for an unknown
// host and an unknown path.
func (h *Handler) missing(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Content-Length", strconv.Itoa(len(h.notFound)))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write(h.notFound)
	}
}

// setSecurityHeaders applies the headers every response carries.
func setSecurityHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", csp)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Cross-Origin-Opener-Policy", "same-origin")
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), interest-cohort=()")
}

// clientIP is the visitor's address: the shared trusted-client-IP rules (the
// forwarding headers count only from the local proxy, and behind Cloudflare the
// visitor is the address Cloudflare saw, not its edge).
func clientIP(r *http.Request) netip.Addr {
	return clientip.FromRequest(r)
}

// normalizeHost reduces a Host header to the hostname a page is looked up by:
// port stripped, lower-case, no trailing dot. IP addresses and anything that is
// not a plausible DNS name are refused before they reach the database.
func normalizeHost(hostport string) (string, bool) {
	h := strings.TrimSpace(hostport)
	if h == "" || strings.HasPrefix(h, "[") {
		return "", false
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		port := h[i+1:]
		for _, c := range port {
			if c < '0' || c > '9' {
				return "", false
			}
		}
		h = h[:i]
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" || len(h) > 253 {
		return "", false
	}
	for i := range len(h) {
		c := h[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
		default:
			return "", false
		}
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return "", false
	}
	return h, true
}

// etagOf is a strong validator of body.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// etagMatches reports whether an If-None-Match header names etag.
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == etag || t == "*" {
			return true
		}
	}
	return false
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.ReplaceAll(strings.TrimSpace(params), " ", ""), "q="); ok {
			if f, err := strconv.ParseFloat(q, 64); err == nil && f <= 0 {
				return false
			}
		}
		return true
	}
	return false
}
