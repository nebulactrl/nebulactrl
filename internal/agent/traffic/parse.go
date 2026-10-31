// Package traffic turns the proxy's (Traefik) JSON access log into
// privacy-preserving per-minute aggregates. It tails the stdout of the managed
// proxy container, aggregates in memory and ships completed minutes to
// the control plane. Raw lines, client IPs, user agents, referrer URLs and
// query strings never leave the host.
package traffic

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ErrIgnored marks a well-formed access line that must not be counted:
// status 0, ACME challenges and redirect-router lines.
var ErrIgnored = errors.New("access line ignored")

// Event is one counted request.
type Event struct {
	Time        time.Time
	ServiceID   string // from the router name nc-<serviceId>-https; empty when no service router matched
	Host        string // lowercase, no port
	Path        string // query stripped
	Method      string
	Status      int
	DurationMs  float64
	BytesOut    int64
	BytesIn     int64
	ClientIP    string
	UserAgent   string
	Referer     string
	ContentType string
}

// accessLine mirrors the fields of Traefik's JSON access log the agent reads
// (static flags: --accesslog.format=json, defaultmode=keep, headers dropped
// except User-Agent, Referer and Content-Type); everything else is ignored.
type accessLine struct {
	StartUTC              string  `json:"StartUTC"`
	RouterName            string  `json:"RouterName"`
	ServiceName           string  `json:"ServiceName"`
	RequestHost           string  `json:"RequestHost"`
	RequestPath           string  `json:"RequestPath"`
	RequestMethod         string  `json:"RequestMethod"`
	RequestScheme         string  `json:"RequestScheme"`
	RequestContentSize    int64   `json:"RequestContentSize"`
	DownstreamStatus      int     `json:"DownstreamStatus"`
	DownstreamContentSize int64   `json:"DownstreamContentSize"`
	Duration              float64 `json:"Duration"` // nanoseconds
	ClientHost            string  `json:"ClientHost"`
	UserAgent             string  `json:"request_User-Agent"`
	Referer               string  `json:"request_Referer"`
	ContentType           string  `json:"downstream_Content-Type"`
}

var accessMarker = []byte(`"DownstreamStatus"`)

const (
	// routerPrefix starts every NebulaCtrl router name. HTTP: nc-<serviceId>-https
	// (the service's router; -https-local for hosts on the internal CA) or
	// nc-<serviceId>-http (its HTTP→HTTPS redirect); with per-domain rules
	// nc-<serviceId>-d<domainId8>-https|http|redir. L4: nc-<serviceId>-tcp|udp.
	routerPrefix = "nc-"
	uuidLen      = 36
	domainIDLen  = 8
)

// serviceFromRouter extracts the service ID from a router name of the form
// nc-<uuid>-https@rest or nc-<uuid>-d<id8>-https@rest. The redirect routers
// (-http, -redir, with or without the per-domain part) report redirect=true.
// Other names (api@internal, foreign routers, probe routers) yield ok=false.
func serviceFromRouter(name string) (id string, redirect, ok bool) {
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	if !strings.HasPrefix(name, routerPrefix) || len(name) < len(routerPrefix)+uuidLen {
		return "", false, false
	}
	id = name[len(routerPrefix) : len(routerPrefix)+uuidLen]
	if !isUUID(id) {
		return "", false, false
	}
	suffix := name[len(routerPrefix)+uuidLen:]
	perDomain := false
	if strings.HasPrefix(suffix, "-d") && len(suffix) >= 2+domainIDLen && isAlnum(suffix[2:2+domainIDLen]) {
		suffix, perDomain = suffix[2+domainIDLen:], true
	}
	switch suffix {
	case "":
		if !perDomain { // nc-<uuid>: the service name itself
			return id, false, true
		}
	case "-https", "-https-local", "-tcp", "-udp":
		return id, false, true
	case "-http", "-http-local", "-redir":
		return id, true, true
	}
	return "", false, false
}

func isAlnum(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func isUUID(s string) bool {
	if len(s) != uuidLen || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	return isHex(s[:8]) && isHex(s[9:13]) && isHex(s[14:18]) && isHex(s[19:23]) && isHex(s[24:])
}

// ParseLine decodes one Traefik access log line (stdout of the proxy
// container). Lines that are not access entries, or that must not be counted
// (redirects, ACME challenges, status 0), return ErrIgnored. A line whose router
// is not a NebulaCtrl service router is returned with an empty ServiceID
// (unrouted: unknown hosts, scanners).
func ParseLine(line []byte) (Event, error) {
	// Cheap pre-filter before decoding JSON.
	if !bytes.Contains(line, accessMarker) {
		return Event{}, ErrIgnored
	}
	var l accessLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, err
	}
	if l.DownstreamStatus == 0 {
		return Event{}, ErrIgnored
	}
	ts, err := time.Parse(time.RFC3339Nano, l.StartUTC)
	if err != nil {
		return Event{}, err
	}
	path := requestPath(l.RequestPath)
	// ACME HTTP-01 challenges are the proxy's own business.
	if strings.HasPrefix(path, "/.well-known/acme-challenge/") {
		return Event{}, ErrIgnored
	}
	svc, redirect, routed := serviceFromRouter(l.RouterName)
	if !routed {
		// Fall back to the service name (nc-<uuid>@rest) for the odd router
		// that does not follow the naming contract.
		svc, _, routed = serviceFromRouter(l.ServiceName)
	} else if redirect {
		// An http router that serves the service itself (forceHttps off, or
		// tlsMode none) shares the redirect router's name but answers with the
		// service; only a router that ends at noop@internal redirects.
		if _, _, served := serviceFromRouter(l.ServiceName); served {
			redirect = false
		}
	}
	// The explicit http→https redirect is not a visit.
	if redirect {
		return Event{}, ErrIgnored
	}
	if !routed {
		svc = ""
	}
	return Event{
		Time:        ts.UTC(),
		ServiceID:   svc,
		Host:        cleanHost(l.RequestHost),
		Path:        path,
		Method:      strings.ToUpper(l.RequestMethod),
		Status:      l.DownstreamStatus,
		DurationMs:  l.Duration / 1e6,
		BytesOut:    max(l.DownstreamContentSize, 0),
		BytesIn:     max(l.RequestContentSize, 0),
		ClientIP:    clientIP(l.ClientHost),
		UserAgent:   l.UserAgent,
		Referer:     l.Referer,
		ContentType: l.ContentType,
	}, nil
}

// clientIP returns the visitor address of an access-log ClientHost. Traefik
// logs the whole X-Forwarded-For header there when the request carries one,
// which only survives the entrypoint from a trusted peer (Cloudflare, see the
// origin lock). The entries left of the last one are whatever the visitor sent,
// so the last entry, the one the trusted proxy appended, is the address to use.
// A plain ClientHost (one address) is returned unchanged.
func clientIP(host string) string {
	if i := strings.LastIndexByte(host, ','); i >= 0 {
		return strings.TrimSpace(host[i+1:])
	}
	return host
}

// requestPath returns the path of a request URI without query or fragment.
func requestPath(uri string) string {
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	// Absolute-form request target (proxy style): drop scheme and authority.
	if i := strings.Index(uri, "://"); i >= 0 {
		rest := uri[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			uri = rest[j:]
		} else {
			uri = "/"
		}
	}
	if uri == "" {
		return "/"
	}
	return uri
}

// cleanHost lowercases a Host header and removes the port and trailing dot.
func cleanHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if h == "" {
		return "(none)"
	}
	return agentprotocol.MaskIPHost(h)
}
