package traffic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRequestKind(t *testing.T) {
	const (
		page  = agentprotocol.RequestKindPage
		api   = agentprotocol.RequestKindAPI
		asset = agentprotocol.RequestKindAsset
		other = agentprotocol.RequestKindOther
	)
	for _, tc := range []struct {
		name, contentType, path, want string
	}{
		// Content-Type, no telling extension.
		{"html", "text/html; charset=utf-8", "/pricing", page},
		{"xhtml", "application/xhtml+xml", "/pricing", page},
		{"upper case and parameters", "Text/HTML ; Charset=UTF-8", "/", page},
		{"json with charset", "application/json; charset=utf-8", "/api/orders", api},
		{"problem json", "application/problem+json", "/api/orders", api},
		{"vendor json", "application/vnd.api+json", "/api/orders", api},
		{"graphql response", "application/graphql-response+json", "/graphql", api},
		{"xml", "application/xml", "/feed", api},
		{"text xml", "text/xml", "/feed", api},
		{"atom xml", "application/atom+xml", "/feed", api},
		{"form", "application/x-www-form-urlencoded", "/login", api},
		{"multipart", "multipart/form-data", "/upload", api},
		{"grpc", "application/grpc", "/pkg.Svc/Method", api},
		{"grpc proto", "application/grpc+proto", "/pkg.Svc/Method", api},
		{"event stream", "text/event-stream", "/events", api},
		{"ndjson", "application/x-ndjson", "/stream", api},
		{"css", "text/css", "/theme", asset},
		{"text javascript", "text/javascript", "/bundle", asset},
		{"application javascript", "application/javascript", "/bundle", asset},
		{"x-javascript", "application/x-javascript", "/bundle", asset},
		{"ecmascript", "application/ecmascript", "/bundle", asset},
		{"wasm", "application/wasm", "/engine", asset},
		{"png", "image/png", "/avatar", asset},
		{"webp from an image optimizer", "image/webp", "/_next/image", asset},
		{"svg is an image, not xml", "image/svg+xml", "/logo", asset},
		{"font", "font/woff2", "/f", asset},
		{"application font", "application/font-woff", "/f", asset},
		{"x font", "application/x-font-ttf", "/f", asset},
		{"audio", "audio/mpeg", "/a", asset},
		{"video", "video/mp4", "/v", asset},
		{"manifest json is an asset, not api", "application/manifest+json", "/site", asset},
		{"plain text", "text/plain", "/health", other},
		{"unknown type", "application/pdf", "/report", other},
		{"octet stream without extension", "application/octet-stream", "/download", other},
		{"no content type, no extension", "", "/blog/post", other},
		{"no content type, root", "", "/", other},

		// The extension decides first, whatever the Content-Type says.
		{"octet stream with asset extension", "application/octet-stream", "/files/app.wasm", asset},
		{"304 without content type", "", "/assets/app.4f2c1.js", asset},
		{"failed asset answers html", "text/html", "/assets/app.4f2c1.js", asset},
		{"html extension beats json", "application/json", "/report.html", page},
		{"htm extension", "", "/old/index.htm", page},
		{"upper case extension", "", "/IMG/Logo.PNG", asset},
		{"dot in a directory is no extension", "", "/v1.2/orders", other},
		{"data file", "", "/data/items.json", other},
		{"txt file", "", "/robots.txt", other},
		{"json file with json type", "application/json", "/data/items.json", api},
		{"unknown extension", "", "/archive.zip", other},
		{"source map", "", "/assets/app.js.map", asset},
		{"web manifest", "", "/app.webmanifest", asset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, RequestKind(tc.contentType, tc.path))
		})
	}

	for _, ext := range []string{
		"js", "mjs", "cjs", "css", "map", "wasm", "png", "jpg", "jpeg", "gif", "webp", "avif", "svg", "ico", "bmp", "tif", "tiff",
		"woff", "woff2", "ttf", "otf", "eot", "mp4", "webm", "ogg", "mp3", "wav", "m4a", "flac", "webmanifest",
	} {
		require.Equal(t, asset, RequestKind("", "/static/file."+ext), ext)
	}
}

func TestSkipsAsset(t *testing.T) {
	const asset = agentprotocol.RequestKindAsset
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{200, true}, {204, true}, {299, true}, {304, true},
		{0, false}, {199, false}, {300, false}, {301, false}, {302, false}, {307, false}, {404, false}, {500, false},
	} {
		require.Equal(t, tc.want, skipsAsset(asset, "GET", "text/javascript", tc.status, false), "status %d", tc.status)
		require.False(t, skipsAsset(asset, "GET", "text/javascript", tc.status, true), "kept assets, status %d", tc.status)
		require.False(t, skipsAsset(agentprotocol.RequestKindPage, "GET", "text/html", tc.status, false), "a page is never skipped, status %d", tc.status)
	}

	// The path's extension is chosen by the client, so it alone never hides a
	// request: the method and the response must look like an asset load too.
	for _, tc := range []struct {
		name, method, contentType string
		want                      bool
	}{
		{"get with an asset type", "GET", "text/javascript", true},
		{"head with an asset type", "HEAD", "image/png; charset=x", true},
		{"get without a type", "GET", "", true},
		{"get with a blank type", "GET", " ; charset=x", true},
		{"post", "POST", "text/javascript", false},
		{"post without a type", "POST", "", false},
		{"put", "PUT", "", false},
		{"delete", "DELETE", "image/png", false},
		{"other method", "OTHER", "", false},
		{"lower case method is not GET", "get", "", false},
		{"get answering html", "GET", "text/html", false},
		{"get answering json", "GET", "application/json", false},
		{"get answering plain text", "GET", "text/plain", false},
	} {
		require.Equal(t, tc.want, skipsAsset(asset, tc.method, tc.contentType, 200, false), tc.name)
	}
}

func TestRequestKindIgnoresMatrixParameters(t *testing.T) {
	const (
		page  = agentprotocol.RequestKindPage
		asset = agentprotocol.RequestKindAsset
		other = agentprotocol.RequestKindOther
	)
	for _, tc := range []struct {
		name, contentType, path, want string
	}{
		{"asset extension hidden in a parameter", "text/html", "/admin;x.js", page},
		{"no type", "", "/admin;x.js", other},
		{"parameter after the extension", "", "/app.js;jsessionid=1", asset},
		{"parameter on a directory", "", "/a;x.js/b", other},
		{"parameter after a page extension", "", "/index.html;v=2", page},
		{"parameter with a dot only", "", "/orders;v=1.2", other},
	} {
		require.Equal(t, tc.want, RequestKind(tc.contentType, tc.path), tc.name)
	}
}
