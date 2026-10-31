package webui

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func schemaDoc() []byte {
	return []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"` + OriginPlaceholder + SchemaPath + `","title":"nebula.toml","description":"` +
		strings.Repeat("a project file ", 60) + `","type":"object"}`)
}

func TestSchemaHandlerServesSchemaWithHeaders(t *testing.T) {
	h := SchemaHandler(schemaDoc(), WithPublicURL("https://nebula.example.com/"))
	rec := get(h, SchemaPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/schema+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("no ETag")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("a browser-based editor must be able to fetch the schema")
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if doc["$id"] != "https://nebula.example.com/schema/nebula.toml.json" {
		t.Fatalf("$id = %v: the origin placeholder was not replaced by the public URL", doc["$id"])
	}
	if strings.Contains(rec.Body.String(), "nebulactrl.invalid") {
		t.Fatal("the placeholder origin leaked")
	}
}

func TestSchemaHandlerSubstitutesRequestOriginWithoutPublicURL(t *testing.T) {
	h := SchemaHandler(schemaDoc())
	rec := get(h, SchemaPath, map[string]string{"X-Forwarded-Proto": "https"})
	if !strings.Contains(rec.Body.String(), `"$id":"https://example.com/schema/nebula.toml.json"`) {
		t.Fatalf("origin not taken from the request: %s", rec.Body.String()[:120])
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Host") {
		t.Fatalf("Vary = %q: the body depends on the Host header", rec.Header().Get("Vary"))
	}
	other := httptest.NewRequest(http.MethodGet, "http://other.test"+SchemaPath, nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, other)
	if !strings.Contains(rec2.Body.String(), `"$id":"http://other.test/schema/nebula.toml.json"`) {
		t.Fatal("second origin not rendered")
	}
	if rec.Header().Get("ETag") == rec2.Header().Get("ETag") {
		t.Fatal("different bodies share an ETag")
	}
}

func TestSchemaHandlerETagRevalidation(t *testing.T) {
	h := SchemaHandler(schemaDoc(), WithPublicURL("https://nebula.example.com"))
	first := get(h, SchemaPath, nil)
	etag := first.Header().Get("ETag")
	for _, inm := range []string{etag, "W/" + etag, `"nope", ` + etag, "*"} {
		rec := get(h, SchemaPath, map[string]string{"If-None-Match": inm})
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Fatalf("If-None-Match %q: code %d, %d body bytes", inm, rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("ETag") != etag {
			t.Fatalf("304 without the ETag")
		}
	}
	if rec := get(h, SchemaPath, map[string]string{"If-None-Match": `"stale"`}); rec.Code != http.StatusOK {
		t.Fatalf("stale validator: code %d", rec.Code)
	}
	// The same bytes always get the same ETag.
	if again := get(h, SchemaPath, nil).Header().Get("ETag"); again != etag {
		t.Fatalf("ETag changed between requests: %q vs %q", again, etag)
	}
}

func TestSchemaHandlerGzipAndHead(t *testing.T) {
	h := SchemaHandler(schemaDoc(), WithPublicURL("https://nebula.example.com"))
	rec := get(h, SchemaPath, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("a compressible schema is not gzipped")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !strings.Contains(string(plain), "https://nebula.example.com/schema/nebula.toml.json") {
		t.Fatal("gzipped body is not the rendered schema")
	}

	req := httptest.NewRequest(http.MethodHead, SchemaPath, nil)
	hr := httptest.NewRecorder()
	h.ServeHTTP(hr, req)
	if hr.Code != http.StatusOK || hr.Body.Len() != 0 || hr.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: code %d, body %d, length %q", hr.Code, hr.Body.Len(), hr.Header().Get("Content-Length"))
	}
}

func TestSchemaHandlerRefusesWrites(t *testing.T) {
	h := SchemaHandler(schemaDoc())
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, SchemaPath, strings.NewReader("{}")))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s: code %d, Allow %q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}
