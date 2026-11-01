package webui

import (
	"net/http"
	"strconv"
)

// SchemaPath is where the control plane serves the JSON Schema of nebula.toml.
const SchemaPath = "/schema/nebula.toml.json"

// SchemaContentType is the media type of a JSON Schema document.
const SchemaContentType = "application/schema+json"

// schemaCacheControl lets editors and CDNs reuse the schema for five minutes;
// the ETag makes the refetch cheap.
const schemaCacheControl = "public, max-age=300"

// SchemaHandler serves schema, the embedded JSON Schema of nebula.toml. The
// document carries OriginPlaceholder (its $id); every response swaps it for
// the real origin, exactly like llms.txt, so editors that resolve the id see
// an address of this instance. The endpoint is public (it holds no instance
// data), answers GET and HEAD, honours If-None-Match and is readable from
// browser-based editors (CORS *).
func SchemaHandler(schema []byte, opts ...Option) http.Handler {
	s := &spa{rendered: map[string]*asset{}}
	for _, opt := range opts {
		opt(s)
	}
	tmpl := newAsset(SchemaPath, schema)
	tmpl.ctype = SchemaContentType
	tmpl.template = true

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		if s.publicURL == "" {
			h.Add("Vary", "Host")
			h.Add("Vary", "X-Forwarded-Proto")
		}
		a := s.render(SchemaPath, tmpl, s.origin(r))
		h.Set("Content-Type", SchemaContentType)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", schemaCacheControl)
		h.Set("Access-Control-Allow-Origin", "*")
		h.Add("Vary", "Accept-Encoding")
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
			_, _ = w.Write(body)
		}
	})
}
