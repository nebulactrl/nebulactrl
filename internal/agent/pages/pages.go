// Package pages renders the maintenance and error pages the agent serves
// through its pages server (ADR 0003, "Maintenance and error pages"): one static
// HTML file per page kind plus one per service in maintenance, and the nginx
// configuration that serves them with the right status codes and headers.
//
// Everything is rendered from embedded assets (deploy/pages): a page is a
// single self-contained HTML file with no external request. User text goes
// through html/template, so it is always escaped.
package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"

	pagesassets "github.com/nebulactrl/nebulactrl/deploy/pages"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Paths inside the pages container.
const (
	// Root is the document root; rendered pages live in Root/p.
	Root = "/usr/share/nebulactrl-pages"
	// ConfPath is the nginx configuration file.
	ConfPath = "/etc/nginx/nginx.conf"
)

// Retry-After values (seconds) when a page has no ETA to derive one from.
const (
	retryNoETA   = "120"
	retryPastETA = "300"
)

// Default maintenance copy.
const (
	DefaultTitle   = "Down for maintenance"
	DefaultMessage = "This service is being updated. Back soon."
)

// Page is what one HTML file shows.
type Page struct {
	// Kind is "maintenance", "404", "502", "503" or "504".
	Kind string
	// Title and Message replace the defaults of the kind (maintenance only).
	Title, Message string
	// ETA is the expected return time (maintenance only).
	ETA *time.Time
	// Host is the service hostname rendered when JavaScript cannot fill in the
	// visited one.
	Host string
	// Preview stops the page from replacing Host with location.hostname.
	Preview bool
}

type kindInfo struct {
	code           int
	kicker, tone   string
	title, message string
}

var kinds = map[string]kindInfo{
	"maintenance": {503, "Maintenance", "amber", DefaultTitle, DefaultMessage},
	"404":         {404, "404 · Not found", "muted", "Nothing here", "No service is configured for this address."},
	"502":         {502, "502 · Bad gateway", "red", "Service unavailable", "This service isn't responding right now. Try again in a moment. If you run it, check that it's started and healthy."},
	"503":         {503, "503 · Unavailable", "red", "Service unavailable", "This service has no healthy instance right now. It may be starting, stopped or being updated. Try again in a moment."},
	"504":         {504, "504 · Gateway timeout", "red", "Service timed out", "The service took too long to answer. Try again in a moment."},
}

// ErrorKinds are the static error pages, in rendering order.
var ErrorKinds = []string{"404", "502", "503", "504"}

type pageData struct {
	Lang, Kind, Tone, Kicker string
	Title, Message, Host     string
	ETA                      *time.Time
	ETAISO, ETAText          string
	Preview                  bool
	Style                    template.CSS
	Script, Engine           template.JS
}

var (
	tmplOnce sync.Once
	tmpl     *template.Template
	tmplErr  error
)

func pageTemplate() (*template.Template, error) {
	tmplOnce.Do(func() {
		tmpl, tmplErr = template.New("page").Parse(pagesassets.PageTemplate)
	})
	return tmpl, tmplErr
}

// Render returns the HTML of p.
func Render(p Page) ([]byte, error) {
	info, ok := kinds[p.Kind]
	if !ok {
		return nil, fmt.Errorf("pages: unknown kind %q", p.Kind)
	}
	t, err := pageTemplate()
	if err != nil {
		return nil, fmt.Errorf("pages: parse template: %w", err)
	}
	d := pageData{
		Lang: "en", Kind: p.Kind, Tone: info.tone, Kicker: info.kicker, Title: info.title, Message: info.message,
		Host: p.Host, Preview: p.Preview,
		Style: template.CSS(pagesassets.PageCSS), Script: template.JS(pagesassets.PageScript), Engine: template.JS(pagesassets.EclipseEngine),
	}
	if p.Kind == "maintenance" {
		if s := agentprotocol.CleanMaintenanceText(p.Title, agentprotocol.MaxMaintenanceTitle); s != "" {
			d.Title = s
		}
		if s := agentprotocol.CleanMaintenanceText(p.Message, agentprotocol.MaxMaintenanceMessage); s != "" {
			d.Message = s
		}
		if p.ETA != nil {
			u := p.ETA.UTC()
			d.ETA, d.ETAISO, d.ETAText = &u, u.Format(time.RFC3339), u.Format("Jan 2, 2006 15:04 UTC")
		}
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("pages: render %s: %w", p.Kind, err)
	}
	return buf.Bytes(), nil
}

// RetryAfter is the Retry-After header value of a maintenance page: the ETA as
// an HTTP date when it is in the future, a fixed delay otherwise.
func RetryAfter(eta *time.Time, now time.Time) string {
	switch {
	case eta == nil:
		return retryNoETA
	case eta.After(now):
		return eta.UTC().Format(http.TimeFormat)
	default:
		return retryPastETA
	}
}

// Entry is one service in maintenance.
type Entry struct {
	ServiceID string
	// Host is the first hostname of the route.
	Host        string
	Maintenance agentprotocol.Maintenance
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Entries lists the routes in maintenance, sorted by service ID.
func Entries(routes []agentprotocol.Route) []Entry {
	var out []Entry
	for _, r := range routes {
		if r.Maintenance == nil || r.IsL4() || !idPattern.MatchString(r.ServiceID) {
			continue
		}
		e := Entry{ServiceID: r.ServiceID, Maintenance: *r.Maintenance}
		if len(r.Hosts) > 0 {
			e.Host = r.Hosts[0]
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

// Union returns the entries of both lists, the second winning per service.
// The agent renders it before a route push so a page never vanishes while
// routers still point at it, and renders the exact set after.
func Union(old, next []Entry) []Entry {
	byID := map[string]Entry{}
	for _, e := range old {
		byID[e.ServiceID] = e
	}
	for _, e := range next {
		byID[e.ServiceID] = e
	}
	out := make([]Entry, 0, len(byID))
	for _, e := range byID {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

// Bundle is the complete content of a pages container: the nginx
// configuration and every rendered page, keyed by archive path.
type Bundle struct {
	// Hash fingerprints the content; it labels the container.
	Hash  string
	files map[string][]byte
}

// MaintenancePath is the path (on the pages server) of the 503 maintenance
// page of a service.
func MaintenancePath(serviceID string) string { return "/m/" + serviceID }

// ErrorPath is the path of the error page of an HTTP status.
func ErrorPath(code int) string { return "/err/" + strconv.Itoa(code) }

type nginxService struct{ ID, RetryAfter string }

// Build renders the bundle for the given maintenance entries.
func Build(entries []Entry, now time.Time) (*Bundle, error) {
	files := map[string][]byte{}
	for _, k := range ErrorKinds {
		b, err := Render(Page{Kind: k})
		if err != nil {
			return nil, err
		}
		files[rootRel+"/p/"+k+".html"] = b
	}
	var svcs []nginxService
	for _, e := range entries {
		if !idPattern.MatchString(e.ServiceID) {
			return nil, fmt.Errorf("pages: invalid service id %q", e.ServiceID)
		}
		m := e.Maintenance
		b, err := Render(Page{Kind: "maintenance", Title: m.Title, Message: m.Message, ETA: m.ETA, Host: e.Host})
		if err != nil {
			return nil, err
		}
		files[rootRel+"/p/m-"+e.ServiceID+".html"] = b
		svcs = append(svcs, nginxService{ID: e.ServiceID, RetryAfter: RetryAfter(m.ETA, now)})
	}
	conf, err := renderNginx(svcs)
	if err != nil {
		return nil, err
	}
	files[strings.TrimPrefix(ConfPath, "/")] = conf

	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(files[n]))
		h.Write(files[n])
	}
	return &Bundle{Hash: hex.EncodeToString(h.Sum(nil)), files: files}, nil
}

var rootRel = strings.TrimPrefix(Root, "/")

// Has reports whether the bundle holds the maintenance page of a service.
func (b *Bundle) Has(serviceID string) bool {
	_, ok := b.files[rootRel+"/p/m-"+serviceID+".html"]
	return ok
}

// File returns the content of an archive path (no leading slash).
func (b *Bundle) File(path string) ([]byte, bool) {
	c, ok := b.files[strings.TrimPrefix(path, "/")]
	return c, ok
}

// securityHeaders is repeated in every page location: nginx replaces, never
// merges, add_header between levels.
const securityHeaders = `add_header Cache-Control "no-store" always; ` +
	`add_header X-Content-Type-Options "nosniff" always; ` +
	`add_header X-Robots-Tag "noindex, nofollow" always; ` +
	`add_header Content-Security-Policy "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" always;`

// SecurityHeaders is the CSP and cache policy of a page, for callers that serve
// the HTML themselves (the control plane's preview).
func SecurityHeaders() map[string]string {
	return map[string]string{
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"X-Robots-Tag":            "noindex, nofollow",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
	}
}

func renderNginx(svcs []nginxService) ([]byte, error) {
	t, err := texttemplate.New("nginx").Parse(pagesassets.NginxTemplate)
	if err != nil {
		return nil, fmt.Errorf("pages: parse nginx template: %w", err)
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, map[string]any{
		"Port": agentprotocol.PagesPort, "Root": Root, "Headers": securityHeaders, "Services": svcs,
	})
	if err != nil {
		return nil, fmt.Errorf("pages: render nginx config: %w", err)
	}
	return buf.Bytes(), nil
}
