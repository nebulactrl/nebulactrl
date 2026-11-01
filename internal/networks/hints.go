package networks

import (
	"net"
	"net/url"
	"sort"
	"strings"
)

// outboundKeyPrefixes are variable name prefixes of services that are normally
// reached over the internet (mail relays, payment, analytics, object storage).
var outboundKeyPrefixes = []string{
	"SMTP_", "MAIL_", "SENDGRID_", "MAILGUN_", "POSTMARK_", "RESEND_", "STRIPE_", "PAYPAL_", "TWILIO_", "SENTRY_",
	"OPENAI_", "ANTHROPIC_", "AWS_", "S3_", "GCS_", "SLACK_", "DISCORD_", "WEBHOOK_", "OAUTH_",
}

// OutboundHints returns the variables of a service that look like they reach
// the internet: a URL whose host is a public name or address (not a private
// address, localhost, or a single-label service name), or a name that belongs
// to a typically external service. The list is sorted and meant for a deploy
// plan warning when the service blocks outbound internet.
func OutboundHints(vars map[string]string) []string {
	var hits []string
	for k, v := range vars {
		if strings.HasPrefix(k, "NEBULA_") || k == "PORT" {
			continue
		}
		if looksExternalURL(v) || hasOutboundPrefix(k) && strings.TrimSpace(v) != "" {
			hits = append(hits, k)
		}
	}
	sort.Strings(hits)
	return hits
}

func hasOutboundPrefix(key string) bool {
	for _, p := range outboundKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func looksExternalURL(v string) bool {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "://") {
		return false
	}
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	return publicHost(u.Hostname())
}

// publicHost reports whether a host name is plausibly on the internet: a
// dotted name that is not local, or a public IP address.
func publicHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
	}
	if !strings.Contains(h, ".") {
		return false // a service name on the private network
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa"} {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}
	return true
}
