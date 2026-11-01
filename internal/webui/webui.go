// Package webui serves the single-page web app embedded in the server binary.
//
// internal/webui/dist is populated by `make build` (and the Dockerfile) from
// apps/web/dist. In a source checkout that has not been built it only holds a
// .keep file; Handler then returns nil so the server answers plain 404s for
// non-API paths instead of serving a broken page.
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

//go:embed all:dist
var embedded embed.FS

const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheRevalid   = "no-cache"
	indexName      = "index.html"
	assetsPrefix   = "assets/"
	minGzipBytes   = 512
)

// reservedPrefixes are served by other handlers; an unmatched request below
// them is a real 404, never the SPA shell.
var reservedPrefixes = []string{"/api/", "/agent/", "/mcp"}

// OriginPlaceholder is the origin the web build writes into llms.txt,
// llms-full.txt and the raw markdown pages (packages/docs/src/build/llms.ts).
// The handler swaps it for the real origin on every request.
const OriginPlaceholder = "https://origin.nebulactrl.invalid"

// maxRendered bounds the per-origin cache of rendered files when no public URL
// is configured, so arbitrary Host headers cannot grow it without limit.
const maxRendered = 24

// Option configures the handler.
type Option func(*spa)

// WithPublicURL sets the origin (NEBULACTRL_PUBLIC_URL, no trailing slash) put
// into the machine-readable docs. Without it the origin is taken from the
// request's Host and X-Forwarded-Proto.
func WithPublicURL(u string) Option {
	return func(s *spa) { s.publicURL = strings.TrimRight(strings.TrimSpace(u), "/") }
}

// Handler returns the SPA handler for the embedded build, or nil when the
// build has not been embedded (no dist/index.html).
func Handler(opts ...Option) http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil
	}
	h, err := NewHandler(sub, opts...)
	if err != nil {
		return nil
	}
	return h
}

// viteHashLen is the length of the content hash Vite appends to asset names.
const viteHashLen = 8

// Font returns the woff2 file of the embedded build named "<stem>-<hash>.woff2"
// in assets/ (for example stem "ibm-plex-sans-latin-wght-normal"), so other
// listeners of the server can serve the same self-hosted fonts. ok is false in
// a build without the web UI or without that font.
func Font(stem string) (body []byte, ok bool) {
	entries, err := fs.ReadDir(embedded, "dist/"+strings.TrimSuffix(assetsPrefix, "/"))
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		name := e.Name()
		rest, found := strings.CutPrefix(name, stem+"-")
		if !found || e.IsDir() || !strings.HasSuffix(rest, ".woff2") || len(rest) != viteHashLen+len(".woff2") {
			continue
		}
		b, err := fs.ReadFile(embedded, path.Join("dist", "assets", name))
		if err != nil {
			return nil, false
		}
		return b, true
	}
	return nil, false
}

type asset struct {
	body      []byte
	gzipped   []byte // nil when not worth compressing
	etag      string
	ctype     string
	immutable bool
	// template marks a file that carries OriginPlaceholder (llms.txt,
	// llms-full.txt, docs/<slug>.md): it is rendered per origin before serving.
	template bool
}

// spa serves files from memory (the whole build is a few MB) with caching
// headers: hashed files under assets/ are immutable, everything else
// revalidates with an ETag, and extension-less unknown paths get index.html.
type spa struct {
	files     map[string]*asset // keyed by slash path without leading slash
	index     *asset
	publicURL string

	mu       sync.Mutex
	rendered map[string]*asset // name + "\x00" + origin
}

// NewHandler builds a handler over fsys, which must contain index.html.
func NewHandler(fsys fs.FS, opts ...Option) (http.Handler, error) {
	s := &spa{files: map[string]*asset{}, rendered: map[string]*asset{}}
	for _, opt := range opts {
		opt(s)
	}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == ".keep" {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		a := newAsset(p, body)
		if isTemplate(p) {
			a.template = true
			a.gzipped = nil // compressed after the origin is known
		}
		s.files[p] = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.index = s.files[indexName]
	if s.index == nil {
		return nil, fs.ErrNotExist
	}
	return s, nil
}

func newAsset(name string, body []byte) *asset {
	sum := sha256.Sum256(body)
	a := &asset{
		body:      body,
		etag:      `"` + hex.EncodeToString(sum[:12]) + `"`,
		immutable: strings.HasPrefix(name, assetsPrefix),
	}
	a.ctype = mime.TypeByExtension(path.Ext(name))
	if path.Ext(name) == ".md" {
		// Not in every system mime table; the docs serve raw markdown.
		a.ctype = "text/markdown; charset=utf-8"
	}
	if a.ctype == "" {
		a.ctype = http.DetectContentType(body)
	}
	if compressible(a.ctype) && len(body) >= minGzipBytes {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(body)
		if zw.Close() == nil && buf.Len() < len(body) {
			a.gzipped = buf.Bytes()
		}
	}
	return a
}

// isTemplate reports whether a build file carries OriginPlaceholder: the
// llms.txt index, llms-full.txt and the raw markdown of every docs page.
func isTemplate(name string) bool {
	return name == "llms.txt" || name == "llms-full.txt" ||
		(strings.HasPrefix(name, "docs/") && strings.HasSuffix(name, ".md"))
}

// origin is the scheme and host the machine-readable docs link to.
func (s *spa) origin(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host
}

// render returns a with the placeholder origin replaced, cached per origin.
func (s *spa) render(name string, a *asset, origin string) *asset {
	key := name + "\x00" + origin
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rendered[key]; ok {
		return r
	}
	if len(s.rendered) >= maxRendered {
		s.rendered = map[string]*asset{}
	}
	r := newAsset(name, bytes.ReplaceAll(a.body, []byte(OriginPlaceholder), []byte(origin)))
	r.ctype = a.ctype
	s.rendered[key] = r
	return r
}

func compressible(ctype string) bool {
	switch {
	case strings.HasPrefix(ctype, "text/"),
		strings.Contains(ctype, "javascript"),
		strings.Contains(ctype, "json"),
		strings.Contains(ctype, "svg"),
		strings.Contains(ctype, "xml"):
		return true
	}
	return false
}

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(r.URL.Path, p) || r.URL.Path == strings.TrimSuffix(p, "/") {
			http.NotFound(w, r)
			return
		}
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	a := s.files[name]
	switch {
	case a != nil:
	case path.Ext(name) != "":
		// A missing file (stale hashed asset, probe for /wp-login.php, …) must
		// not turn into a 200 HTML page.
		http.NotFound(w, r)
		return
	default:
		a = s.index // client-side route
	}
	if a.template {
		if s.publicURL == "" {
			w.Header().Add("Vary", "Host")
			w.Header().Add("Vary", "X-Forwarded-Proto")
		}
		a = s.render(name, a, s.origin(r))
	}
	s.write(w, r, a)
}

// cspBase is the Content-Security-Policy of the dashboard shell, minus the
// WebSocket source (see csp). It matches what the build needs and nothing more:
//
//   - scripts: the module bundle and its preloads, same origin; the shell has no
//     inline script, so there is no 'unsafe-inline' for scripts;
//   - styles: the CSS bundle plus inline styles (React style props, chart and
//     terminal libraries set them), which is why style-src has 'unsafe-inline';
//   - fonts: self-hosted woff2 in /assets (data: covers a font Vite inlines);
//   - images: same origin, data: (the noise texture, inlined SVG) and blob: (the
//     downloads built in the page);
//   - connect: the API and event streams on the same origin, plus the terminal's
//     WebSocket (older Safari does not count 'self' for ws://, so it is named).
const cspBase = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'%s; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// wsHostOK keeps a Host header that could not be written into a CSP source
// (spaces, semicolons, commas) out of the policy.
var wsHostOK = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]+$`)

// csp returns the policy for a request: cspBase with the page's own WebSocket origin.
func (s *spa) csp(r *http.Request) string {
	origin := s.origin(r)
	scheme, host, ok := strings.Cut(origin, "://")
	if !ok || !wsHostOK.MatchString(host) {
		return fmt.Sprintf(cspBase, "")
	}
	ws := "ws"
	if scheme == "https" {
		ws = "wss"
	}
	return fmt.Sprintf(cspBase, " "+ws+"://"+host)
}

func (s *spa) write(w http.ResponseWriter, r *http.Request, a *asset) {
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	if a == s.index {
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", s.csp(r))
		if s.publicURL == "" {
			h.Add("Vary", "Host")
			h.Add("Vary", "X-Forwarded-Proto")
		}
	}
	h.Add("Vary", "Accept-Encoding")
	if a.immutable {
		h.Set("Cache-Control", cacheImmutable)
	} else {
		h.Set("Cache-Control", cacheRevalid)
	}
	h.Set("ETag", a.etag)
	if etagMatches(r.Header.Get("If-None-Match"), a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.body
	if a.gzipped != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		h.Set("Content-Encoding", "gzip")
		body = a.gzipped
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // embedded web build, not user input
	}
}

func etagMatches(header, etag string) bool {
	for _, c := range strings.Split(header, ",") {
		c = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c), "W/"))
		if c == etag || c == "*" {
			return true
		}
	}
	return false
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		params = strings.ReplaceAll(params, " ", "")
		if q, ok := strings.CutPrefix(params, "q="); ok {
			f, err := strconv.ParseFloat(q, 64)
			return err != nil || f > 0
		}
		return true
	}
	return false
}
