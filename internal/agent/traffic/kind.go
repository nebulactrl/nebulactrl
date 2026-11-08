package traffic

import (
	"path"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// assetExtensions are the file extensions of scripts, styles, source maps,
// images, fonts, media and web manifests. A static .json or .txt file is data,
// not an asset.
var assetExtensions = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true, ".wasm": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true,
	".svg": true, ".ico": true, ".bmp": true, ".tif": true, ".tiff": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".ogg": true, ".mp3": true, ".wav": true, ".m4a": true, ".flac": true,
	".webmanifest": true,
}

// RequestKind classifies a request as page, api, asset or other. The
// extension of the path's last segment decides first, when it is a known asset
// or HTML one: the Content-Type of a failed request describes the error body
// (a 404 on /assets/app.js usually answers text/html), not what was asked for.
// A matrix parameter (";x.js") is not part of the extension: servers drop it
// before routing. Otherwise the Content-Type decides (a media type, parameters
// ignored), and what neither recognises is "other".
func RequestKind(contentType, p string) string {
	switch ext := lastSegmentExt(p); {
	case assetExtensions[ext]:
		return agentprotocol.RequestKindAsset
	case ext == ".html" || ext == ".htm":
		return agentprotocol.RequestKindPage
	}
	if kind := kindOfContentType(contentType); kind != "" {
		return kind
	}
	return agentprotocol.RequestKindOther
}

// lastSegmentExt returns the lowercase extension of the last path segment,
// with its matrix parameters (everything from the first ';') removed.
func lastSegmentExt(p string) string {
	seg := p[strings.LastIndexByte(p, '/')+1:]
	seg, _, _ = strings.Cut(seg, ";")
	return strings.ToLower(path.Ext(seg))
}

// kindOfContentType returns the kind a Content-Type names, "" when it names
// none.
func kindOfContentType(contentType string) string {
	mt, _, _ := strings.Cut(contentType, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	switch mt {
	case "":
		return ""
	case "text/html", "application/xhtml+xml":
		return agentprotocol.RequestKindPage
	case "application/manifest+json", // before the +json rule
		"text/css", "text/javascript", "application/javascript", "application/x-javascript",
		"application/ecmascript", "application/wasm":
		return agentprotocol.RequestKindAsset
	case "application/json", "application/xml", "text/xml", "application/x-www-form-urlencoded",
		"text/event-stream", "application/x-ndjson", "application/graphql-response+json":
		return agentprotocol.RequestKindAPI
	}
	switch {
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "font/"),
		strings.HasPrefix(mt, "application/font-"), strings.HasPrefix(mt, "application/x-font-"),
		strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"):
		return agentprotocol.RequestKindAsset
	case strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"),
		strings.HasPrefix(mt, "multipart/"), strings.HasPrefix(mt, "application/grpc"):
		return agentprotocol.RequestKindAPI
	}
	return ""
}

// skipsAsset reports whether the request logs leave a request out: a loaded
// asset is noise unless the server keeps assets. Only a GET or HEAD that
// succeeded (2xx, or a 304 from the browser cache) and whose response is an
// asset by its Content-Type, or has none, is skipped: a path's extension is
// chosen by the client (`POST /users/1.js`, a format suffix), so any other
// method, or an answer that is not an asset, is stored. A failed request is a
// signal and always stored.
func skipsAsset(kind, method, contentType string, status int, keepAssets bool) bool {
	if kind != agentprotocol.RequestKindAsset || keepAssets || (method != "GET" && method != "HEAD") {
		return false
	}
	if !(status >= 200 && status < 300 || status == 304) {
		return false
	}
	mt, _, _ := strings.Cut(contentType, ";")
	return strings.TrimSpace(mt) == "" || kindOfContentType(contentType) == agentprotocol.RequestKindAsset
}
