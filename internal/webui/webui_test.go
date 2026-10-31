package webui

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func testHandler(t *testing.T, opts ...Option) http.Handler {
	t.Helper()
	h, err := NewHandler(fstest.MapFS{
		"index.html":                         {Data: []byte("<html>" + strings.Repeat("shell ", 200) + "</html>")},
		"favicon.svg":                        {Data: []byte("<svg/>")},
		"assets/app-abc123.js":               {Data: []byte(strings.Repeat("console.log(1);", 100))},
		"assets/font-xyz.woff2":              {Data: []byte("woff2-bytes")},
		"llms.txt":                           {Data: []byte("# NebulaCtrl\n\n- [Quickstart](" + OriginPlaceholder + "/docs/getting-started/quickstart.md): start\n")},
		"llms-full.txt":                      {Data: []byte("Source: " + OriginPlaceholder + "/docs/x\n")},
		"docs/getting-started/quickstart.md": {Data: []byte("# Quickstart\n\nSee [x](" + OriginPlaceholder + "/docs/x.md).\n")},
		".keep":                              {Data: nil},
	}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSPAFallbackServesIndexWithoutCaching(t *testing.T) {
	h := testHandler(t)
	// /docs is the in-app documentation: public pages the SPA renders, deep links included.
	for _, p := range []string{"/", "/login", "/p/abc/production", "/servers/123", "/docs", "/docs/operations/variables", "/docs/reference/api/services"} {
		rec := get(h, p, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html>") {
			t.Fatalf("%s: code %d", p, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q", p, cc)
		}
	}
}

func TestHashedAssetsAreImmutable(t *testing.T) {
	h := testHandler(t)
	rec := get(h, "/assets/font-xyz.woff2", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if cc := get(h, "/favicon.svg", nil).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("favicon Cache-Control = %q", cc)
	}
}

func TestMissingFilesAndReservedPrefixesAre404(t *testing.T) {
	h := testHandler(t)
	for _, p := range []string{"/assets/gone-1.js", "/wp-login.php", "/api/v1/nope", "/agent/v1/nope", "/mcp"} {
		if rec := get(h, p, nil); rec.Code != 404 {
			t.Fatalf("%s: code %d, want 404", p, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("POST: code %d", rec.Code)
	}
}

func TestGzipAndConditionalRequests(t *testing.T) {
	h := testHandler(t)
	rec := get(h, "/assets/app-abc123.js", map[string]string{"Accept-Encoding": "gzip, br"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("expected gzip")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(plain), "console.log(1);") {
		t.Fatal("gzip body mismatch")
	}
	if get(h, "/assets/app-abc123.js", nil).Header().Get("Content-Encoding") != "" {
		t.Fatal("identity request must not be gzipped")
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if rec2 := get(h, "/", map[string]string{"If-None-Match": get(h, "/", nil).Header().Get("ETag")}); rec2.Code != http.StatusNotModified {
		t.Fatalf("conditional: code %d", rec2.Code)
	}
}

func TestNewHandlerRequiresIndex(t *testing.T) {
	if _, err := NewHandler(fstest.MapFS{".keep": {}}); err == nil {
		t.Fatal("want error without index.html")
	}
}

func TestMachineReadableDocsAreServedPubliclyWithTheirOrigin(t *testing.T) {
	cases := []struct {
		path, ctype, contains string
	}{
		{"/llms.txt", "text/plain; charset=utf-8", "(https://nebula.example.com/docs/getting-started/quickstart.md)"},
		{"/llms-full.txt", "text/plain; charset=utf-8", "Source: https://nebula.example.com/docs/x\n"},
		{"/docs/getting-started/quickstart.md", "text/markdown; charset=utf-8", "[x](https://nebula.example.com/docs/x.md)"},
	}
	h := testHandler(t, WithPublicURL("https://nebula.example.com/"))
	for _, tc := range cases {
		rec := get(h, tc.path, nil) // no cookie, no token: the docs are public
		if rec.Code != 200 {
			t.Fatalf("%s: code %d", tc.path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.ctype {
			t.Fatalf("%s: Content-Type %q, want %q", tc.path, got, tc.ctype)
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.contains) || strings.Contains(body, OriginPlaceholder) {
			t.Fatalf("%s: body %q", tc.path, body)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control %q", tc.path, cc)
		}
	}
}

func TestMachineReadableDocsUseTheRequestHostWithoutAPublicURL(t *testing.T) {
	h := testHandler(t)
	rec := get(h, "/llms.txt", nil)
	if !strings.Contains(rec.Body.String(), "(http://example.com/docs/getting-started/quickstart.md)") {
		t.Fatalf("host origin missing: %q", rec.Body.String())
	}
	rec = get(h, "/llms.txt", map[string]string{"X-Forwarded-Proto": "https"})
	if !strings.Contains(rec.Body.String(), "(https://example.com/docs/") {
		t.Fatalf("forwarded proto ignored: %q", rec.Body.String())
	}
	if v := rec.Header().Values("Vary"); !strings.Contains(strings.Join(v, ","), "Host") {
		t.Fatalf("Vary = %v", v)
	}
}

func TestRenderedOriginsAreBounded(t *testing.T) {
	h := testHandler(t)
	for i := 0; i < maxRendered*3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
		req.Host = strings.Repeat("a", i+1) + ".example.com"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if n := len(h.(*spa).rendered); n > maxRendered {
		t.Fatalf("%d rendered entries, cap %d", n, maxRendered)
	}
}

func TestRawMarkdownForUnknownPageIs404(t *testing.T) {
	if rec := get(testHandler(t), "/docs/nope.md", nil); rec.Code != 404 {
		t.Fatalf("code %d, want 404", rec.Code)
	}
}

func TestShellCarriesAContentSecurityPolicy(t *testing.T) {
	h := testHandler(t)
	for _, p := range []string{"/", "/login", "/servers/123", "/docs"} {
		csp := get(h, p, nil).Header().Get("Content-Security-Policy")
		for _, want := range []string{
			"default-src 'self'", "script-src 'self'", "object-src 'none'", "base-uri 'none'",
			"form-action 'self'", "frame-ancestors 'none'", "style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:", "font-src 'self' data:", "connect-src 'self' ws://example.com",
		} {
			if !strings.Contains(csp, want) {
				t.Fatalf("%s: CSP %q lacks %q", p, csp, want)
			}
		}
		if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "unsafe-eval") {
			t.Fatalf("%s: scripts must not be loosened: %q", p, csp)
		}
	}
	if csp := get(h, "/assets/app-abc123.js", nil).Header().Get("Content-Security-Policy"); csp != "" {
		t.Fatalf("a script asset carries a CSP: %q", csp)
	}
}

func TestCSPNamesTheWebSocketOriginOfThePage(t *testing.T) {
	csp := get(testHandler(t), "/", map[string]string{"X-Forwarded-Proto": "https"}).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' wss://example.com;") {
		t.Fatalf("CSP %q", csp)
	}
	csp = get(testHandler(t, WithPublicURL("https://nebula.example.com/")), "/", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' wss://nebula.example.com;") {
		t.Fatalf("CSP %q", csp)
	}
	if v := get(testHandler(t), "/", nil).Header().Values("Vary"); !strings.Contains(strings.Join(v, ","), "Host") {
		t.Fatalf("the policy depends on the Host, Vary = %v", v)
	}
}

func TestCSPIgnoresAHostThatCannotBeASource(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.example; script-src *"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "evil") || !strings.Contains(csp, "connect-src 'self';") || !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("CSP %q", csp)
	}
}

// The policy is written for the shell Vite builds: no inline script and no
// third-party origin. If the build changes, this fails before a browser does.
func TestPolicyFitsTheBuiltShell(t *testing.T) {
	b, err := fs.ReadFile(embedded, "dist/index.html")
	if err != nil {
		t.Skip("the web UI is not built into this binary")
	}
	html := string(b)
	for _, tag := range regexp.MustCompile(`(?is)<script[^>]*>`).FindAllString(html, -1) {
		if !strings.Contains(tag, " src=") {
			t.Fatalf("inline script in index.html (script-src 'self' blocks it): %s", tag)
		}
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Fatal("inline event handler in index.html")
	}
	if m := regexp.MustCompile(`https?://[^\s"'<>]+`).FindString(html); m != "" {
		t.Fatalf("index.html loads from another origin (%s); connect/script/style-src would block it", m)
	}
}
