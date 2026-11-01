package statuspublic

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nebulactrl/nebulactrl/internal/statuspages"
)

//go:embed templates/page.html
var pageTemplateSource string

//go:embed assets/status.css
var baseCSS string

//go:embed assets/dark.css
var darkCSS string

// statusJS is the page's one script: the day tooltip of the uptime bars. The
// page is complete without it. It is served from the page's own origin, which
// is all the CSP's script-src allows.
//
//go:embed assets/status.js
var statusJS string

// jsVer is the script's content hash; jsHref is its versioned URL.
var (
	jsVer  = etagOf([]byte(statusJS))[1:9]
	jsHref = "/status.js?v=" + jsVer
)

var templates = template.Must(template.New("status").Parse(pageTemplateSource))

// pageData is what the "page" template executes against.
type pageData struct {
	pageView
	Sprite          template.HTML
	CSSHref         string
	JSHref          string
	Favicon         template.URL
	MetaDescription string
}

// sprite holds the one pattern the bars draw with: the stripes of a degraded
// day, so it differs from its neighbours by shape as well as colour. It is
// inline in every page (a url(#id) reference cannot cross documents) and sized
// zero rather than hidden: some browsers do not paint patterns of a
// display:none SVG. The colours are in the stylesheet.
const sprite template.HTML = `<svg class="sprite" width="0" height="0" aria-hidden="true" focusable="false"><defs>` +
	`<pattern id="p-warn" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">` +
	`<rect class="pw-bg" width="5" height="5"/><rect class="pw-stripe" width="2" height="5"/>` +
	`</pattern></defs></svg>`

// faviconColors are the fills of the state favicon: a dot in the state's colour.
var faviconColors = map[string]string{
	statuspages.StateOperational:   "#2db36f",
	statuspages.StateDegraded:      "#d9a000",
	statuspages.StatePartialOutage: "#e8892c",
	statuspages.StateMajorOutage:   "#e5484d",
	statuspages.StateMaintenance:   "#2b8fd6",
	statuspages.StateUnknown:       "#8a919e",
}

// favicon returns a data: URL of a round icon in the state's colour; the CSP
// allows data: images.
func favicon(state string) template.URL {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="` + faviconColors[stateKey(state)] + `"/></svg>`
	return template.URL("data:image/svg+xml," + url.PathEscape(svg)) //nolint:gosec // constant markup, escaped
}

// metaDescriptionLimit is the length search engines and link previews show.
const metaDescriptionLimit = 155

// metaDescription is the page description as one short line for the meta tag.
func metaDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = metaDescriptionLimit
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:limit-1])) + "…"
}

// renderHTML renders the page; cssHref is the versioned stylesheet URL.
func renderHTML(p statuspages.PublicPage, cssHref string, now time.Time) ([]byte, error) {
	v := buildView(p, now)
	desc := metaDescription(p.Description)
	if desc == "" {
		desc = v.Overall.Label // a page without a description still says something useful in a link preview
	}
	d := pageData{
		pageView: v, Sprite: sprite, CSSHref: cssHref, JSHref: jsHref,
		Favicon: favicon(p.Overall), MetaDescription: desc,
	}
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "page", d); err != nil {
		return nil, fmt.Errorf("render status page: %w", err)
	}
	return buf.Bytes(), nil
}

// renderNotFound renders the minimal 404 page.
func renderNotFound(cssHref string) []byte {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "notfound", pageData{CSSHref: cssHref}); err != nil {
		return []byte("Not found\n")
	}
	return buf.Bytes()
}

// fontFiles says which self-hosted fonts the listener can serve.
type fontFiles struct {
	Sans    []byte // IBM Plex Sans, variable weight
	Mono400 []byte
	Mono600 []byte
}

// Paths the listener serves the fonts at.
const (
	pathFontSans    = "/fonts/ibm-plex-sans.woff2"
	pathFontMono400 = "/fonts/ibm-plex-mono-400.woff2"
	pathFontMono600 = "/fonts/ibm-plex-mono-600.woff2"
)

// buildCSS assembles the stylesheet: the font faces of the fonts that exist
// (otherwise the system stack in the sheet applies), the dark variables for
// both the automatic and the forced dark theme, and the base rules.
func buildCSS(f fontFiles) string {
	css := strings.Replace(baseCSS, "/*DARK-AUTO*/", darkCSS, 1)
	css = strings.Replace(css, "/*DARK-FORCED*/", darkCSS, 1)
	var faces strings.Builder
	if len(f.Sans) > 0 {
		fmt.Fprintf(&faces, "@font-face{font-family:\"IBM Plex Sans Variable\";font-style:normal;font-display:swap;font-weight:100 700;src:url(%s) format(\"woff2\")}\n", pathFontSans)
	}
	if len(f.Mono400) > 0 {
		fmt.Fprintf(&faces, "@font-face{font-family:\"IBM Plex Mono\";font-style:normal;font-display:swap;font-weight:400;src:url(%s) format(\"woff2\")}\n", pathFontMono400)
	}
	if len(f.Mono600) > 0 {
		fmt.Fprintf(&faces, "@font-face{font-family:\"IBM Plex Mono\";font-style:normal;font-display:swap;font-weight:600;src:url(%s) format(\"woff2\")}\n", pathFontMono600)
	}
	return faces.String() + css
}

// gzipped compresses b; it returns nil when compression does not pay.
func gzipped(b []byte) []byte {
	if len(b) < 512 {
		return nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write(b); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	if buf.Len() >= len(b) {
		return nil
	}
	return buf.Bytes()
}
