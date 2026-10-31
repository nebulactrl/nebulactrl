// Package pagesassets embeds the sources of the maintenance and error pages:
// the HTML template, its CSS and small script, the nginx configuration
// template of the pages server, and the Eclipse backdrop engine bundle.
//
// assets/eclipse.js is generated from packages/ascii (the same WebGL2 engine as the
// login screen) and committed, so `go build` needs no bun:
//
//go:generate sh -c "cd ../../packages/ascii && bun run scripts/build-pages-engine.ts"
package pagesassets

import _ "embed"

// PageTemplate is the html/template of every page kind.
//
//go:embed page.html.tmpl
var PageTemplate string

// PageCSS is inlined into every page.
//
//go:embed page.css
var PageCSS string

// PageScript fills in the visited host name and localizes the ETA.
//
//go:embed page.js
var PageScript string

// NginxTemplate is the text/template of the pages server configuration.
//
//go:embed nginx.conf.tmpl
var NginxTemplate string

// EclipseEngine is the IIFE bundle of the Eclipse ASCII backdrop.
//
//go:embed assets/eclipse.js
var EclipseEngine string
