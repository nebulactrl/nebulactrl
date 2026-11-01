package templates

import (
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Input formats: what an install input must look like. The web form picks the
// matching field type (email, url, numeric) and the server checks every
// non-empty value, so a template that needs an address or URL never receives
// free text in its place.
const (
	FormatText     = "text"     // default: any text
	FormatEmail    = "email"    // one address, no display name
	FormatURL      = "url"      // http or https URL with a host
	FormatHostname = "hostname" // DNS name or IP address, no scheme and no path
	FormatNumber   = "number"   // a non-negative whole number
)

// InputFormats lists every valid TemplateInput.Format ("" means text).
var InputFormats = []string{FormatText, FormatEmail, FormatURL, FormatHostname, FormatNumber}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// CheckInputFormat reports why v does not fit format ("" = it does). An empty
// value fits every format: whether a value is required is the input's own
// `required` flag.
func CheckInputFormat(format, v string) string {
	if v == "" {
		return ""
	}
	switch format {
	case FormatEmail:
		a, err := mail.ParseAddress(v)
		if err != nil || a.Address != v || !strings.Contains(a.Address[strings.LastIndex(a.Address, "@"):], ".") {
			return "must be an email address such as name@example.com"
		}
	case FormatURL:
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return "must be an http:// or https:// URL"
		}
	case FormatHostname:
		if ip := net.ParseIP(v); ip != nil {
			return ""
		}
		if len(v) > 253 || !hostnameRe.MatchString(v) {
			return "must be a host name or IP address without a scheme or path"
		}
	case FormatNumber:
		if len(v) > 9 || strings.Trim(v, "0123456789") != "" {
			return "must be a whole number"
		}
	}
	return ""
}

// impliedFormat is the format a required input's name asks for, "" when none:
// a key like ADMIN_EMAIL or OIDC_AUTH_URL tells the validator the input must
// say so, or the form would offer a plain text box for an address.
func impliedFormat(key string) string {
	switch {
	case key == "EMAIL" || strings.HasSuffix(key, "_EMAIL"):
		return FormatEmail
	case strings.HasSuffix(key, "_URL") || strings.HasSuffix(key, "_URI"):
		return FormatURL
	}
	return ""
}

// checkInputFormat validates the declared format of one input.
func (c *checker) inputFormat(in TemplateInput) {
	at := "input " + in.Key + ": "
	f := in.Format
	if f != "" && !slices.Contains(InputFormats, f) {
		c.addf(at+"format %q must be one of %s", f, strings.Join(InputFormats, ", "))
		return
	}
	if f != "" && f != FormatText && (in.Secret || len(in.Options) > 0) {
		c.add(at + "a secret or select input must be plain text")
	}
	if f != "" && !hasGenerator(in.Default) {
		if msg := CheckInputFormat(f, in.Default); msg != "" {
			c.addf(at+"default %q %s", in.Default, msg)
		}
	}
	if in.Required && !in.Secret && len(in.Options) == 0 && in.Default == "" && (f == "" || f == FormatText) {
		if want := impliedFormat(in.Key); want != "" {
			c.addf(at+"required and named like %s: declare format: %s so the form and the install check it", want, want)
		}
	}
}
