package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Entrypoint names of the managed Traefik instance.
const (
	EntryWeb       = "web"       // :80, plain HTTP (redirect + ACME HTTP-01)
	EntryWebSecure = "websecure" // :443, TLS (tcp + http3/udp)
	EntryProbe     = "probe"     // :8099, never published: post-deploy verification and liveness
	EntryAdmin     = "ncadmin"   // :9000, basic-auth protected admin entrypoint (REST provider, API)
)

const (
	// AdminUser is the basic-auth user of the admin entrypoint.
	AdminUser = "nebulactrl-agent"
	// CertResolver is the name of the ACME resolver; it only exists when the
	// server has an ACME email.
	CertResolver = "le"
	// ACMEStorage is where Traefik keeps issued certificates (the ACME volume).
	ACMEStorage = "/data/acme.json"
	// BootstrapFile is the static dynamic-config file the agent copies into the
	// container before its first start.
	BootstrapFile = "/etc/traefik/bootstrap.yml"
	// RESTPath is the REST provider endpoint that takes the full dynamic config.
	RESTPath = "/api/providers/rest"
	// APIRoutersPath lists http routers with their status (used to detect
	// configs Traefik accepted but could not build). APITCPRoutersPath and
	// APIUDPRoutersPath do the same for the L4 routers.
	APIRoutersPath    = "/api/http/routers"
	APITCPRoutersPath = "/api/tcp/routers"
	APIUDPRoutersPath = "/api/udp/routers"
	// PingHost is the Host header of the liveness router on the probe entrypoint.
	PingHost = "ping.nc.invalid"
	// GracePeriod lets in-flight requests finish when Traefik shuts down.
	GracePeriod = "10s"
	// ProvidersThrottle is the minimum time between provider configuration
	// events. Traefik's default (2s) would delay back-to-back applies.
	ProvidersThrottle = "200ms"
	// LargeUploadReadTimeout is the entrypoint read timeout on servers that route a
	// domain with RouteOptions.LargeUploads (a registry's layer uploads): long enough
	// for a multi-GB layer over a slow uplink, still bounded (30 minutes matches the
	// build timeout) so a stalled client cannot hold a connection forever.
	LargeUploadReadTimeout = "30m"
)

// Names of the dynamic objects the REST provider carries.
const (
	redirectMiddleware = "nc-https-redirect"
	genRouter          = "nc-gen"
	// pagesService is the Traefik service of the pages server (maintenance and
	// error pages); errorsMiddleware swaps a failing upstream's 502–504 for its
	// page and notFoundMiddleware rewrites the catch-all to the 404 page.
	pagesService = "nc-pages"
	// maintenanceDownService has no servers, so Traefik answers 503: it is
	// what a service in maintenance is routed to while no pages server runs, so
	// that a maintenance window never ends because the pages are unavailable.
	maintenanceDownService = "nc-maintenance-down"
	errorsMiddleware       = "nc-errors"
	notFoundMiddleware     = "nc-pages-notfound"
	notFoundRouter         = "nc-pages-notfound"
)

// GenerationHost is the Host header that answers 200 on the probe entrypoint
// once generation n of the dynamic config is live.
func GenerationHost(n int64) string { return "gen-" + strconv.FormatInt(n, 10) + ".nc.invalid" }

// RouterPrefix is prepended to a service ID to name its routers and service.
const RouterPrefix = "nc-"

// ServiceName is the Traefik service (and router name prefix) of a routed service.
func ServiceName(serviceID string) string { return RouterPrefix + serviceID }

// L4Port is one static tcp/udp entrypoint (and published host port).
type L4Port struct {
	Proto string // agentprotocol.ProtoTCP | ProtoUDP
	Port  int
}

// Entry is the entrypoint name: tcp-<port> or udp-<port>.
func (p L4Port) Entry() string { return p.Proto + "-" + strconv.Itoa(p.Port) }

// L4Ports derives the static entrypoint set of a route table: every distinct
// (protocol, listen port) of its tcp/udp routes, sorted by port then protocol.
func L4Ports(routes []agentprotocol.Route) []L4Port {
	seen := map[L4Port]bool{}
	var out []L4Port
	for _, r := range routes {
		if !r.IsL4() || r.ListenPort == 0 {
			continue
		}
		p := L4Port{Proto: r.Protocol, Port: r.ListenPort}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

// HasLargeUploads reports whether any domain of the table has the largeUploads
// option: the proxy then runs with the longer read timeout (a static flag, so the
// container is recreated when the answer changes).
func HasLargeUploads(routes []agentprotocol.Route) bool {
	for _, r := range routes {
		for _, d := range r.Domains {
			if d.Options.LargeUploads {
				return true
			}
		}
	}
	return false
}

// StaticOptions tunes StaticFlags.
type StaticOptions struct {
	// ACMEEmail, when set, adds the ACME resolver "le" (HTTP-01 on web).
	ACMEEmail string
	// PublicHTTPSPort is the host port websecure is published on; it is
	// advertised in the HTTP/3 Alt-Svc header when it is not 443.
	PublicHTTPSPort int
	// TrafficAnalytics enables the JSON access log on stdout.
	TrafficAnalytics bool
	// L4Ports adds one tcp-<port> / udp-<port> entrypoint per entry (sorted by
	// the caller; see L4Ports).
	L4Ports []L4Port
	// LargeUploads raises the incoming read timeout of the public entrypoints to
	// LargeUploadReadTimeout (a route has RouteOptions.LargeUploads; see HasLargeUploads).
	LargeUploads bool
	// DNSResolvers adds one DNS-01 ACME resolver per provider (only with an
	// ACMEEmail). Their credentials are the container's environment, not flags.
	DNSResolvers []agentprotocol.DNSResolver
	// TrustedProxyCIDRs, when set, makes the web and websecure entrypoints
	// trust X-Forwarded-* headers from these ranges only (Cloudflare's, see
	// TrustedProxyCIDRs); every other peer has them deleted. Empty keeps Traefik's
	// default of trusting nobody.
	TrustedProxyCIDRs []string
}

// StaticFlags renders Traefik's static configuration as command-line flags.
// A change in this set requires recreating the container.
func StaticFlags(o StaticOptions) []string {
	f := []string{
		"--global.checknewversion=false",
		"--global.sendanonymoususage=false",
		"--entrypoints." + EntryWeb + ".address=:80",
		"--entrypoints." + EntryWeb + ".transport.lifecycle.gracetimeout=" + GracePeriod,
		"--entrypoints." + EntryWeb + ".http.aliasheadersstrategy=delete",
		"--entrypoints." + EntryWebSecure + ".address=:443",
		"--entrypoints." + EntryWebSecure + ".transport.lifecycle.gracetimeout=" + GracePeriod,
		"--entrypoints." + EntryWebSecure + ".http.aliasheadersstrategy=delete",
		"--entrypoints." + EntryWebSecure + ".http3=true",
	}
	if o.PublicHTTPSPort != 0 && o.PublicHTTPSPort != 443 {
		f = append(f, "--entrypoints."+EntryWebSecure+".http3.advertisedport="+strconv.Itoa(o.PublicHTTPSPort))
	}
	if o.LargeUploads {
		// Traefik has no per-router read timeout; this is the entrypoint's (default
		// 60s, "the maximum duration for reading the entire request, including the
		// body"). Raised only on servers that route a domain with largeUploads.
		f = append(f,
			"--entrypoints."+EntryWeb+".transport.respondingtimeouts.readtimeout="+LargeUploadReadTimeout,
			"--entrypoints."+EntryWebSecure+".transport.respondingtimeouts.readtimeout="+LargeUploadReadTimeout,
		)
	}
	if len(o.TrustedProxyCIDRs) > 0 {
		// forwardedHeaders.trustedIPs is a static entrypoint option, so a change
		// of the set recreates the container like the other static flags.
		trusted := strings.Join(o.TrustedProxyCIDRs, ",")
		f = append(f,
			"--entrypoints."+EntryWeb+".forwardedheaders.trustedips="+trusted,
			"--entrypoints."+EntryWebSecure+".forwardedheaders.trustedips="+trusted,
		)
	}
	f = append(f,
		"--entrypoints."+EntryProbe+".address=:"+strconv.Itoa(agentprotocol.ProxyProbePort),
		"--entrypoints."+EntryProbe+".http.aliasheadersstrategy=delete",
		"--entrypoints."+EntryAdmin+".address=:"+strconv.Itoa(agentprotocol.ProxyAdminPort),
		"--entrypoints."+EntryAdmin+".http.aliasheadersstrategy=delete",
	)
	for _, p := range o.L4Ports {
		f = append(f, "--entrypoints."+p.Entry()+".address=:"+strconv.Itoa(p.Port)+"/"+p.Proto)
	}
	f = append(f,
		// The REST provider takes the dynamic config over the admin entrypoint.
		// providers.rest.insecure is never set: it would expose an unauthenticated
		// write endpoint to every container on the edge network.
		"--providers.rest=true",
		"--providers.file.filename="+BootstrapFile,
		"--providers.providersthrottleduration="+ProvidersThrottle,
		// ping@internal answers the generation marker and liveness routers.
		"--ping=true",
		"--ping.manualrouting=true",
		// The API (no dashboard) is only reachable through the authenticated
		// admin router in the bootstrap file; the agent reads router status.
		"--api=true",
		"--api.dashboard=false",
		// Traefik's own log is JSON on stderr; stdout carries only access lines.
		"--log.format=json",
		"--log.level=INFO",
		"--log.filepath=/dev/stderr",
	)
	if o.TrafficAnalytics {
		f = append(f,
			"--accesslog=true",
			"--accesslog.format=json",
			"--accesslog.bufferingsize=100",
			"--accesslog.fields.defaultmode=keep",
			"--accesslog.fields.headers.defaultmode=drop",
			"--accesslog.fields.headers.names.User-Agent=keep",
			"--accesslog.fields.headers.names.Referer=keep",
			"--accesslog.fields.headers.names.Content-Type=keep",
		)
	}
	if o.ACMEEmail != "" {
		f = append(f,
			"--certificatesresolvers."+CertResolver+".acme.email="+o.ACMEEmail,
			"--certificatesresolvers."+CertResolver+".acme.storage="+ACMEStorage,
			"--certificatesresolvers."+CertResolver+".acme.httpchallenge=true",
			"--certificatesresolvers."+CertResolver+".acme.httpchallenge.entrypoint="+EntryWeb,
		)
		f = append(f, dnsFlags(o.ACMEEmail, o.DNSResolvers)...)
	}
	return f
}

// BootstrapYAML renders the static dynamic-config file: the admin routers
// (REST provider and API behind basic auth) and the liveness router. passwordHash
// is a bcrypt hash.
func BootstrapYAML(passwordHash string) []byte {
	var b strings.Builder
	b.WriteString("# Generated by nebulactrl-agent. Static; the route table is pushed through the REST provider.\n")
	b.WriteString("http:\n")
	b.WriteString("  middlewares:\n")
	b.WriteString("    nc-admin-auth:\n")
	b.WriteString("      basicAuth:\n")
	b.WriteString("        users:\n")
	fmt.Fprintf(&b, "          - %q\n", AdminUser+":"+passwordHash)
	b.WriteString("  routers:\n")
	b.WriteString("    nc-admin:\n")
	fmt.Fprintf(&b, "      entryPoints: [%s]\n", EntryAdmin)
	fmt.Fprintf(&b, "      rule: Path(`%s`)\n", RESTPath)
	b.WriteString("      middlewares: [nc-admin-auth]\n")
	b.WriteString("      service: rest@internal\n")
	b.WriteString("      observability: {accessLogs: false}\n")
	b.WriteString("    nc-api:\n")
	fmt.Fprintf(&b, "      entryPoints: [%s]\n", EntryAdmin)
	// /api/http, /api/tcp and /api/udp router status. nc-admin's longer rule
	// keeps the REST provider endpoint on its own router.
	b.WriteString("      rule: PathPrefix(`/api`)\n")
	b.WriteString("      middlewares: [nc-admin-auth]\n")
	b.WriteString("      service: api@internal\n")
	b.WriteString("      observability: {accessLogs: false}\n")
	b.WriteString("    nc-ping:\n")
	fmt.Fprintf(&b, "      entryPoints: [%s]\n", EntryProbe)
	fmt.Fprintf(&b, "      rule: Host(`%s`)\n", PingHost)
	b.WriteString("      service: ping@internal\n")
	b.WriteString("      observability: {accessLogs: false}\n")
	return []byte(b.String())
}

// specHash fingerprints everything that defines the container: if it changes,
// the container is recreated.
func specHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isLocalName(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if net.ParseIP(h) != nil {
		return true
	}
	if !strings.Contains(h, ".") {
		return true
	}
	for _, suf := range []string{".localhost", ".local", ".internal", ".home.arpa", ".test", ".example", ".invalid"} {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	return h == "localhost"
}
