package conn

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// isLoopbackHost reports whether host is localhost or a loopback IP literal:
// traffic to it never leaves the machine, so plaintext is acceptable (the
// installer's agent on the control-plane host dials http://127.0.0.1:<port>).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// Endpoints are the control-plane URLs derived from the --url flag.
type Endpoints struct {
	// Enroll is the HTTP(S) URL of POST /agent/v1/enroll.
	Enroll string
	// Connect is the WS(S) URL of /agent/v1/connect.
	Connect string
	// Secure is true for https/wss.
	Secure bool
	// Base is the HTTP(S) base URL of the control plane (no trailing slash):
	// where the agent downloads its own upgrade from.
	Base string
}

// ParseEndpoints derives the enrollment and websocket URLs from the control-plane
// base URL. Accepted schemes are https/wss (secure) and http/ws (only with
// insecureDev, or when the host is a loopback address). A path prefix on the
// base URL is preserved.
func ParseEndpoints(raw string, insecureDev bool) (Endpoints, error) {
	if strings.TrimSpace(raw) == "" {
		return Endpoints{}, errors.New("control plane url is required (--url)")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Endpoints{}, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if u.Host == "" {
		return Endpoints{}, fmt.Errorf("invalid url %q: missing host", raw)
	}
	var httpScheme, wsScheme string
	var secure bool
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		httpScheme, wsScheme, secure = "https", "wss", true
	case "http", "ws":
		if !insecureDev && !isLoopbackHost(u.Hostname()) {
			return Endpoints{}, fmt.Errorf("refusing insecure url %q: use https:// or pass --insecure-dev (plaintext is only accepted for loopback addresses)", raw)
		}
		httpScheme, wsScheme = "http", "ws"
	default:
		return Endpoints{}, fmt.Errorf("invalid url %q: scheme must be https, wss (or http, ws with --insecure-dev)", raw)
	}
	base := strings.TrimRight(u.Path, "/")
	mk := func(scheme, p string) string {
		return (&url.URL{Scheme: scheme, Host: u.Host, Path: base + p}).String()
	}
	return Endpoints{
		Enroll:  mk(httpScheme, "/agent/v1/enroll"),
		Connect: mk(wsScheme, "/agent/v1/connect"),
		Secure:  secure,
		Base:    mk(httpScheme, ""),
	}, nil
}
