// Package catalogfs embeds the built-in template catalog (templates/catalog/*.yaml).
// The files are reviewed in git; the server never fetches templates at runtime.
package catalogfs

import "embed"

// FS holds catalog/<id>.yaml.
//
//go:embed catalog/*.yaml
var FS embed.FS
