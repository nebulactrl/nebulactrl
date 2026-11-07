package gitproviders

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
)

// The page that sends a GitHub App manifest to GitHub. GitHub only takes the
// manifest as a form POST from the browser, and the dashboard's CSP allows
// forms to its own origin only (form-action 'self'), so the dashboard opens
// this page instead: its CSP allows exactly one form target, the GitHub host
// of the flow (github.com or the Enterprise Server).

const (
	manifestFormScript = `document.getElementById("manifest").submit();`
	manifestFormStyle  = `:root{color-scheme:dark}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#121417;color:#c0c4cc;font:14px/1.5 system-ui,sans-serif}form{display:grid;gap:12px;justify-items:center}button{font:inherit;padding:6px 14px;cursor:pointer;color:#f1f3f8;background:#24262a;border:1px solid rgb(255 255 255/.16);border-radius:8px}`
)

var (
	manifestFormPage = template.Must(template.New("manifest").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Continue to GitHub</title>
<style>` + manifestFormStyle + `</style>
</head>
<body>
<form id="manifest" method="post" action="{{.PostURL}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>Taking you to GitHub to create the App…</p>
<button type="submit">Continue to GitHub</button>
</form>
<script>` + manifestFormScript + `</script>
</body>
</html>
`))
	manifestFormCSP = "default-src 'none'; script-src " + cspHash(manifestFormScript) + "; style-src " + cspHash(manifestFormStyle) +
		"; form-action %s; base-uri 'none'; frame-ancestors 'none'"
	// cspSourceOK keeps an origin that could not be written into a CSP source
	// expression out of the policy.
	cspSourceOK = regexp.MustCompile(`^https?://[A-Za-z0-9.\-:\[\]]+$`)
)

func cspHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func (s *Service) handleManifestForm(w http.ResponseWriter, r *http.Request) {
	postURL, manifest, err := s.ManifestForm(r.Context(), r.URL.Query().Get("state"))
	if err != nil {
		s.flowError(w, r, err)
		return
	}
	u, err := url.Parse(postURL)
	origin := ""
	if err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	if !cspSourceOK.MatchString(origin) {
		s.flowError(w, r, errors.New("the GitHub URL of the connection cannot be used as a form target: "+postURL))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", fmt.Sprintf(manifestFormCSP, origin))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	// GitHub gets the Origin header of a normal cross-site form post.
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if err := manifestFormPage.Execute(w, struct{ PostURL, Manifest string }{postURL, manifest}); err != nil {
		s.log.Error("render github manifest form", "err", err)
	}
}
