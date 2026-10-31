package agentprotocol

// Proxy v2 protocol: typed per-domain options, external
// upstreams, TCP/UDP routes, custom certificates, the upstream policy and the
// per-server tailnet. Shared by the control plane (validation, builders) and the
// agent (rendering). Nothing here carries a secret value: bcrypt hashes and
// private keys travel in Command.Secrets (and Reconcile.Secrets) under the keys
// named by DomainRule.AuthRef and CertRef.KeyRef, and the agent resolves them
// into the json:"-"-style "resolved" fields before rendering.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
)

// Command kinds of the proxy protocol.
const (
	// KindProxyConfigure applies server-level proxy settings (certificates,
	// upstream policy, tailnet) and re-applies the current route table.
	KindProxyConfigure = "proxy_configure"
)

func init() { AllowedKinds[KindProxyConfigure] = true }

// CapProxyV2 is advertised in Facts.Capabilities by agents that understand this
// file. The control plane refuses to stage proxy v2 features (422
// agent_too_old) for servers without it, because an old agent would silently
// drop unknown JSON fields.
const CapProxyV2 = "proxy.v2"

// Route protocols (Route.Protocol; "" means http).
const (
	ProtoHTTP = "http"
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
)

// Route kinds (Route.Kind; "" means service).
const (
	RouteKindService  = "service"  // upstreams are managed replica containers
	RouteKindExternal = "external" // upstreams are operator-supplied addresses
)

// Domain rule kinds and TLS modes.
const (
	DomainServe    = "serve"
	DomainRedirect = "redirect"

	TLSAuto   = "auto"   // ACME (HTTP-01) when the server has an email and the name is public, else the default/custom certificate
	TLSCustom = "custom" // never ACME: an uploaded certificate (matched by SNI) or the default certificate
	TLSNone   = "none"   // plain HTTP only, no TLS router, no redirect
)

// Secret key prefixes in Command.Secrets / Reconcile.Secrets.
const (
	// SecretAuthPrefix + <domainId> → "user:bcrypt\nuser2:bcrypt2".
	SecretAuthPrefix = "auth:"
	// SecretCertPrefix + <certId> → private key PEM.
	SecretCertPrefix = "cert:"
	// SecretTailnetAuthKey is a single-use, pre-authorized, tagged auth key,
	// present only when TailnetSpec.Provision is true.
	SecretTailnetAuthKey = "tailnet:authkey"
)

// Well-known tailnet container names on a host.
const (
	TailscaleContainer   = "nebulactrl-tailscale"
	TailscaleStateVolume = "nebulactrl-tailscale-state"
)

// Tailnet modes (TailnetSpec.Mode, servers.tailnet_mode).
const (
	TailnetOff     = "off"
	TailnetManaged = "managed" // a tailscale container shares Traefik's network namespace (kernel mode)
	TailnetHost    = "host"    // the host already runs Tailscale; routes may target 100.x addresses by IP
)

// Tailnet states (TailnetStatus.State).
const (
	TailnetStateOff          = "off"
	TailnetStateProvisioning = "provisioning"
	TailnetStateOnline       = "online"
	TailnetStateOffline      = "offline"
	TailnetStateNeedsAuth    = "needs_attention"
)

// RouteExt carries the proxy v2 fields of a Route (embedded, so the JSON is flat).
type RouteExt struct {
	// Protocol is "http" (default), "tcp" or "udp". tcp/udp routes have no Hosts:
	// they listen on ListenPort of the server (a static Traefik entrypoint).
	Protocol string `json:"protocol,omitempty"`
	// Kind is "service" (default) or "external". External routes are checked
	// against the UpstreamPolicy.
	Kind string `json:"kind,omitempty"`
	// Scheme is the upstream scheme of an http route ("http" default, "https").
	Scheme    string             `json:"scheme,omitempty"`
	Transport *UpstreamTransport `json:"transport,omitempty"`
	// PassHost false rewrites the Host header to the upstream's (default true).
	PassHost *bool `json:"passHost,omitempty"`
	// Domains holds the per-hostname rule and options. Hosts without an entry
	// are served with defaults. Hosts must list every Domains[].Host.
	Domains []DomainRule `json:"domains,omitempty"`
	// ListenPort is the host port of a tcp/udp route.
	ListenPort int `json:"listenPort,omitempty"`
	// AllowCIDRs restricts tcp clients (Traefik IPAllowList). Not enforceable for
	// udp: Traefik has no UDP middleware. Empty = open to the internet.
	AllowCIDRs []string `json:"allowCidrs,omitempty"`
	// DenyCIDRs are refused even when AllowCIDRs (or an empty allow list) would
	// admit them: deny is evaluated first (see EffectiveAllow).
	DenyCIDRs []string `json:"denyCidrs,omitempty"`
	// ProxyProtocol is the PROXY protocol version sent to a tcp upstream (0, 1, 2).
	ProxyProtocol int `json:"proxyProtocol,omitempty"`
	// OriginLock is the Cloudflare origin lock data of the route's locked
	// domains (cforigin.go). Nil when no domain of the route is locked.
	OriginLock *OriginLock `json:"originLock,omitempty"`
	// VerifySkip skips the post-apply HTTP verification of an external route
	// (upstreams that legitimately answer 5xx).
	VerifySkip bool `json:"verifySkip,omitempty"`
	// Maintenance, when set, serves the maintenance page on every domain of the
	// route (HTTP routes only; see Maintenance).
	Maintenance *Maintenance `json:"maintenance,omitempty"`
	// System marks a route the control plane generates for its own containers
	// (the dashboard and the public status pages). Its upstream is a name only
	// the proxy's Docker network resolves, so the agent does not resolve and
	// pin it. A tenant can never set it: only the control plane's own table
	// builders do.
	System bool `json:"system,omitempty"`
	// HostOverride is set by the agent while rendering a route whose upstream
	// names it pinned to IP addresses: the original "name:port", sent as the
	// Host header when PassHost is false (Traefik would send the address).
	// Never serialised.
	HostOverride string `json:"-"`
}

// IsL4 reports whether the route is a tcp/udp route.
func (r Route) IsL4() bool { return r.Protocol == ProtoTCP || r.Protocol == ProtoUDP }

// UpstreamTransport tunes https upstreams.
type UpstreamTransport struct {
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	ServerName         string `json:"serverName,omitempty"`
}

// DomainRule is one hostname (plus optional path prefix) of a route and its policy.
type DomainRule struct {
	ID           string `json:"id"` // domains.id; names the Traefik objects (nc-<serviceId>-d<id8>-…)
	Host         string `json:"host"`
	PathPrefix   string `json:"pathPrefix,omitempty"` // "" or "/api" (no trailing slash)
	StripPrefix  bool   `json:"stripPrefix,omitempty"`
	AddPrefix    string `json:"addPrefix,omitempty"`
	Kind         string `json:"kind,omitempty"` // serve (default) | redirect
	RedirectTo   string `json:"redirectTo,omitempty"`
	RedirectCode int    `json:"redirectCode,omitempty"` // 301|302|307|308 (default 308)
	PreservePath bool   `json:"preservePath,omitempty"` // redirect keeps path+query
	TLSMode      string `json:"tlsMode,omitempty"`      // auto (default) | custom | none
	// CertificateID is the uploaded certificate the operator pinned (informational:
	// Traefik selects uploaded certificates by SNI/SAN).
	CertificateID string `json:"certificateId,omitempty"`
	// DNSResolver is the DNS-01 provider (a resolver of the server's
	// ProxyConfig.DNSResolvers) that obtains this domain's certificate; "" =
	// HTTP-01 (or a covering wildcard certificate). Wildcard hosts need one
	// unless a WildcardCert covers them.
	DNSResolver string `json:"dnsResolver,omitempty"`
	// Tunnel marks a domain whose DNS record is the server's Cloudflare Tunnel
	// CNAME: its traffic arrives over the tunnel as plain HTTP on the web
	// entrypoint, so it is served there without the HTTPS redirect and never
	// gets an HTTP-01 certificate (Cloudflare terminates the public TLS). A
	// DNS-01 resolver or wildcard certificate still applies.
	Tunnel bool `json:"tunnel,omitempty"`
	// OriginLocked locks the domain to Cloudflare (Route.OriginLock): its
	// routers get the Cloudflare peer allow list first in the chain, and the
	// user's IP rules and rate limit evaluate the real visitor IP.
	OriginLocked bool `json:"originLocked,omitempty"`
	// RequireClientCert makes the domain's TLS option require a client
	// certificate signed by Route.OriginLock.ClientCAPEM (Authenticated Origin
	// Pulls). Only with OriginLocked.
	RequireClientCert bool `json:"requireClientCert,omitempty"`
	// Generated marks a platform-generated hostname (WildcardCert.CoverGenerated).
	Generated bool         `json:"generated,omitempty"`
	Options   RouteOptions `json:"options"`
	// AuthRef is the Secrets key (SecretAuthPrefix+<domain id>) holding the
	// basic-auth users. Set whenever Options.BasicAuth is true.
	AuthRef string `json:"authRef,omitempty"`
	// AuthUsers is filled by the AGENT from Secrets[AuthRef] ("user:bcrypt" lines).
	// The control plane never sets it; it must not appear in persisted payloads.
	AuthUsers []string `json:"authUsers,omitempty"`
}

// Header is a custom request or response header.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CORS configures cross-origin response headers.
type CORS struct {
	Origins       []string `json:"origins"` // origins or "*"
	Methods       []string `json:"methods,omitempty"`
	Headers       []string `json:"headers,omitempty"`
	ExposeHeaders []string `json:"exposeHeaders,omitempty"`
	Credentials   bool     `json:"credentials,omitempty"`
	MaxAgeSeconds int      `json:"maxAgeSeconds,omitempty"`
}

// Redirect is one custom regex redirect (Go regexp syntax, ${1} replacements).
type Redirect struct {
	Regex       string `json:"regex"`
	Replacement string `json:"replacement"`
	Permanent   bool   `json:"permanent,omitempty"`
}

// RateLimit is Traefik's rateLimit middleware: Average requests per Period
// seconds with Burst, keyed by client IP (TrustedProxyDepth picks the
// X-Forwarded-For hop).
type RateLimit struct {
	Average           int `json:"average"`
	PeriodSeconds     int `json:"periodSeconds"`
	Burst             int `json:"burst"`
	TrustedProxyDepth int `json:"trustedProxyDepth,omitempty"`
}

// Access is the IP allow/deny rule set (rendered as one Traefik ipAllowList,
// deny first: see EffectiveAllow). An empty AllowCIDRs means every address not
// denied. Behind a CDN set TrustedProxyDepth so the real client IP is matched.
type Access struct {
	AllowCIDRs        []string `json:"allowCidrs,omitempty"`
	DenyCIDRs         []string `json:"denyCidrs,omitempty"`
	TrustedProxyDepth int      `json:"trustedProxyDepth,omitempty"`
	RejectStatus      int      `json:"rejectStatus,omitempty"` // default 403
}

// RouteOptions is the typed middleware set of a domain. Zero value = no
// middleware. It is stored as domains.options JSON.
type RouteOptions struct {
	// ForceHTTPS false serves the service over plain HTTP as well (default true).
	ForceHTTPS *bool `json:"forceHttps,omitempty"`
	Compress   bool  `json:"compress,omitempty"`
	Noindex    bool  `json:"noindex,omitempty"` // X-Robots-Tag: noindex, nofollow
	// SecurityHeaders is "", "basic" (nosniff, frame deny, referrer) or "strict" (basic + HSTS).
	SecurityHeaders string     `json:"securityHeaders,omitempty"`
	RequestHeaders  []Header   `json:"requestHeaders,omitempty"`
	ResponseHeaders []Header   `json:"responseHeaders,omitempty"`
	CORS            *CORS      `json:"cors,omitempty"`
	Redirects       []Redirect `json:"redirects,omitempty"`
	RateLimit       *RateLimit `json:"rateLimit,omitempty"`
	Access          *Access    `json:"access,omitempty"`
	BasicAuth       bool       `json:"basicAuth,omitempty"` // users live in domain_auth_users
	// LargeUploads is for domains that take request bodies that take minutes to
	// send (a container registry's layer uploads). Traefik has no per-router read
	// timeout: the incoming read timeout is a property of the entrypoint
	// (entrypoints.<name>.transport.respondingTimeouts.readTimeout, 60s by default,
	// "the maximum duration for reading the entire request, including the body"). So
	// when any domain of the route table sets this, the agent raises that timeout on
	// the websecure and web entrypoints to 30 minutes for the whole server
	// (internal/agent/proxy.LargeUploadReadTimeout); other servers keep the default.
	LargeUploads bool `json:"largeUploads,omitempty"`
}

// IsZero reports whether no option is set.
func (o RouteOptions) IsZero() bool {
	b, _ := json.Marshal(o)
	return string(b) == "{}"
}

// ForceHTTPSOrDefault is ForceHTTPS with its default (true).
func (o RouteOptions) ForceHTTPSOrDefault() bool { return o.ForceHTTPS == nil || *o.ForceHTTPS }

// ProxyConfig is the server-level proxy configuration. A nil *ProxyConfig in a
// payload leaves the agent's current configuration unchanged; a non-nil one
// replaces it whole.
type ProxyConfig struct {
	Certificates   []CertRef      `json:"certificates,omitempty"`
	UpstreamPolicy UpstreamPolicy `json:"upstreamPolicy"`
	Tailnet        *TailnetSpec   `json:"tailnet,omitempty"`
	// DNSResolvers are the server's DNS-01 certificate resolvers (static
	// Traefik configuration: a change recreates the proxy container) and
	// WildcardCerts the certificates requested through them. See dns01.go.
	DNSResolvers  []DNSResolver  `json:"dnsResolvers,omitempty"`
	WildcardCerts []WildcardCert `json:"wildcardCerts,omitempty"`
	// HTTPPort and HTTPSPort override the host ports the proxy publishes its
	// web entrypoints on (the agent's --proxy-http-port and --proxy-https-port).
	// 0 keeps the agent's own port. Changing them recreates the proxy
	// container (CapProxyPorts). They are part of the applied configuration, so
	// every proxy config the control plane sends carries the applied values.
	HTTPPort  int `json:"httpPort,omitempty"`
	HTTPSPort int `json:"httpsPort,omitempty"`
}

// CertRef is an uploaded certificate. CertPEM is public; the key rides in
// Secrets[KeyRef].
type CertRef struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	CertPEM string   `json:"certPem"`
	KeyRef  string   `json:"keyRef"` // SecretCertPrefix + id
	// KeyPEM is filled by the AGENT from Secrets[KeyRef]; never set by the control plane.
	KeyPEM string `json:"keyPem,omitempty"`
}

// UpstreamPolicy decides which addresses an external route may target. Always
// denied: unspecified addresses, the proxy's own containers (nebulactrl-*) and
// the admin port. Denied unless allowed per server: loopback, link-local
// (169.254.0.0/16 incl. cloud metadata). Everything else (LAN, CGNAT/tailnet
// 100.64.0.0/10, host gateway, public addresses, DNS names) is allowed.
type UpstreamPolicy struct {
	AllowLoopback    bool `json:"allowLoopback,omitempty"`
	AllowLinkLocal   bool `json:"allowLinkLocal,omitempty"`
	AllowHostGateway bool `json:"allowHostGateway"` // adds host.docker.internal:host-gateway to the proxy container
}

// DefaultUpstreamPolicy is the policy of a new server.
func DefaultUpstreamPolicy() UpstreamPolicy { return UpstreamPolicy{AllowHostGateway: true} }

// HostGatewayName is the name that resolves to the Docker host from the proxy
// container when UpstreamPolicy.AllowHostGateway is set.
const HostGatewayName = "host.docker.internal"

// TailnetSpec is the desired per-server tailnet egress.
type TailnetSpec struct {
	Mode string `json:"mode"` // off | managed | host
	// Hostname is the tailnet machine name of a managed node.
	Hostname string `json:"hostname,omitempty"`
	// MagicDNS adds 100.100.100.100 to the proxy container's DNS servers so
	// routes can name tailnet devices (managed mode only).
	MagicDNS bool `json:"magicDns,omitempty"`
	// Provision tells the agent to (re)create the node with
	// Secrets[SecretTailnetAuthKey]. False keeps the existing node and state.
	Provision bool `json:"provision,omitempty"`
	// Remove deletes the tailscale container and its state volume (mode off).
	Remove bool `json:"remove,omitempty"`
}

// TailnetStatus is the observed tailnet state, reported in heartbeats and
// proxy_configure results.
type TailnetStatus struct {
	Mode     string   `json:"mode"`
	State    string   `json:"state"`
	FQDN     string   `json:"fqdn,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	DeviceID string   `json:"deviceId,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// HostTailscale is the result of the agent's detection of a host-level Tailscale.
type HostTailscale struct {
	Detected bool     `json:"detected"`
	IPs      []string `json:"ips,omitempty"`
}

// ProxyConfigurePayload is the payload of a proxy_configure command. Secrets:
// SecretCertPrefix+<id> for every certificate and SecretAuthPrefix+<domain id>
// for every basic-auth domain of the routes the agent already holds, plus
// SecretTailnetAuthKey when Proxy.Tailnet.Provision.
type ProxyConfigurePayload struct {
	Proxy ProxyConfig `json:"proxy"`
	// Routes, when non-nil, replaces the table before applying (the control plane
	// sends it so a reconnecting agent that lost state converges). Nil re-applies
	// the agent's current table.
	Routes           []Route `json:"routes,omitempty"`
	ReplaceRoutes    bool    `json:"replaceRoutes,omitempty"`
	ACMEEmail        string  `json:"acmeEmail,omitempty"`
	TrafficAnalytics *bool   `json:"trafficAnalytics,omitempty"`
	// TableSeq numbers Routes (see SyncRoutesPayload.TableSeq). A table older
	// than the newest the agent has applied is not applied; the configuration
	// still is, over the agent's own table.
	TableSeq uint64 `json:"tableSeq,omitempty"`
}

// CertStatus is the load state of one certificate on the proxy.
type CertStatus struct {
	ID     string `json:"id"`
	Loaded bool   `json:"loaded"`
	Error  string `json:"error,omitempty"`
}

// ProxyConfigureResult is ResultFrame.Data of proxy_configure and sync_routes.
type ProxyConfigureResult struct {
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
	// Restarted reports that the proxy container was recreated (new L4 ports,
	// upstream policy host gateway, tailnet DNS, ...).
	Restarted    bool           `json:"restarted,omitempty"`
	Certificates []CertStatus   `json:"certificates,omitempty"`
	Tailnet      *TailnetStatus `json:"tailnet,omitempty"`
	// AcmeCerts are the observed DNS-01 wildcard and domain certificates.
	AcmeCerts []AcmeCertStatus `json:"acmeCerts,omitempty"`
}

// ---------------------------------------------------------------------------
// Validation shared by the control plane and the agent. Every value ends up in a
// Traefik rule, header or regex, so both sides validate.

var (
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$`)
	pathPrefixRe    = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*[A-Za-z0-9._~-]$`)
	headerNameRe    = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	corsOriginRe    = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	methodRe        = regexp.MustCompile(`^[A-Z]{3,10}$`)
	basicUsernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// forbiddenRequestHeaders can never be set or removed by an operator.
var forbiddenRequestHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "upgrade": true,
	"te": true, "trailer": true, "keep-alive": true, "proxy-authorization": true, "proxy-connection": true,
	"set-cookie": true, "x-forwarded-for": true, "x-real-ip": true,
}

// ValidHostname reports whether h is acceptable inside a Traefik Host() rule.
func ValidHostname(h string) bool { return hostPattern.MatchString(h) }

// ValidBasicUsername reports whether u is a valid basic-auth user name.
func ValidBasicUsername(u string) bool { return basicUsernameRe.MatchString(u) }

// ValidPathPrefix validates a domain path prefix: "" or "/x[/y]" without a
// trailing slash, at most 200 characters.
func ValidPathPrefix(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > 200 || !pathPrefixRe.MatchString(p) || strings.Contains(p, "//") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		return fmt.Errorf("invalid path prefix %q: use /segment[/segment] without a trailing slash", p)
	}
	return nil
}

func validCIDROrIP(s string) error {
	if _, err := netip.ParsePrefix(s); err == nil {
		return nil
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return nil
	}
	return fmt.Errorf("%q is not a CIDR or IP address", s)
}

func validateHeaders(kind string, hs []Header, request bool) error {
	if len(hs) > 30 {
		return fmt.Errorf("at most 30 %s headers", kind)
	}
	for _, h := range hs {
		if !headerNameRe.MatchString(h.Name) || len(h.Name) > 100 {
			return fmt.Errorf("invalid %s header name %q", kind, h.Name)
		}
		if request && forbiddenRequestHeaders[strings.ToLower(h.Name)] {
			return fmt.Errorf("the %s header cannot be set on requests", h.Name)
		}
		if !request && strings.EqualFold(h.Name, "set-cookie") {
			return fmt.Errorf("Set-Cookie cannot be set by the proxy")
		}
		if len(h.Value) > 2048 || strings.ContainsAny(h.Value, "\r\n\x00") {
			return fmt.Errorf("invalid value for %s header %q", kind, h.Name)
		}
	}
	return nil
}

// Validate checks every option value.
func (o RouteOptions) Validate() error {
	switch o.SecurityHeaders {
	case "", "basic", "strict":
	default:
		return fmt.Errorf("securityHeaders must be empty, basic or strict")
	}
	if err := validateHeaders("request", o.RequestHeaders, true); err != nil {
		return err
	}
	if err := validateHeaders("response", o.ResponseHeaders, false); err != nil {
		return err
	}
	if c := o.CORS; c != nil {
		if len(c.Origins) == 0 || len(c.Origins) > 20 {
			return fmt.Errorf("cors needs 1-20 origins")
		}
		for _, or := range c.Origins {
			if or != "*" && !corsOriginRe.MatchString(or) {
				return fmt.Errorf("invalid cors origin %q", or)
			}
		}
		if c.Credentials && len(c.Origins) == 1 && c.Origins[0] == "*" {
			return fmt.Errorf("cors credentials cannot be combined with the * origin")
		}
		for _, m := range c.Methods {
			if !methodRe.MatchString(m) {
				return fmt.Errorf("invalid cors method %q", m)
			}
		}
		for _, h := range append(append([]string(nil), c.Headers...), c.ExposeHeaders...) {
			if !headerNameRe.MatchString(h) {
				return fmt.Errorf("invalid cors header %q", h)
			}
		}
		if c.MaxAgeSeconds < 0 || c.MaxAgeSeconds > 86400 {
			return fmt.Errorf("cors maxAgeSeconds must be 0-86400")
		}
	}
	if len(o.Redirects) > 10 {
		return fmt.Errorf("at most 10 redirect rules")
	}
	for i, r := range o.Redirects {
		if r.Regex == "" || len(r.Regex) > 512 || len(r.Replacement) > 2048 || strings.ContainsAny(r.Replacement, "\r\n\x00") {
			return fmt.Errorf("redirect %d: regex and replacement must be set and at most 512/2048 characters", i+1)
		}
		if _, err := regexp.Compile(r.Regex); err != nil {
			return fmt.Errorf("redirect %d: invalid regex: %v", i+1, err)
		}
	}
	if rl := o.RateLimit; rl != nil {
		if rl.Average < 1 || rl.Burst < 1 || rl.PeriodSeconds < 1 || rl.PeriodSeconds > 3600 || rl.TrustedProxyDepth < 0 || rl.TrustedProxyDepth > 5 {
			return fmt.Errorf("rateLimit needs average>=1, burst>=1, period 1-3600s and trustedProxyDepth 0-5")
		}
	}
	if a := o.Access; a != nil {
		if err := ValidateAccessLists(a.AllowCIDRs, a.DenyCIDRs, true); err != nil {
			return fmt.Errorf("access: %v", err)
		}
		if a.TrustedProxyDepth < 0 || a.TrustedProxyDepth > 5 {
			return fmt.Errorf("access trustedProxyDepth must be 0-5")
		}
		if a.RejectStatus != 0 && (a.RejectStatus < 400 || a.RejectStatus > 599) {
			return fmt.Errorf("access rejectStatus must be 400-599")
		}
	}
	return nil
}

// Validate checks a domain rule (shape only; hostname resolution and DB
// uniqueness are the control plane's business).
func (d DomainRule) Validate() error {
	if !ValidRouteHost(d.Host) {
		return fmt.Errorf("invalid host %q", d.Host)
	}
	if err := ValidPathPrefix(d.PathPrefix); err != nil {
		return err
	}
	if d.AddPrefix != "" {
		if err := ValidPathPrefix(d.AddPrefix); err != nil {
			return fmt.Errorf("addPrefix: %v", err)
		}
	}
	switch d.Kind {
	case "", DomainServe:
	case DomainRedirect:
		if !hostPattern.MatchString(d.RedirectTo) || d.RedirectTo == d.Host {
			return fmt.Errorf("redirectTo must be a different valid hostname")
		}
		switch d.RedirectCode {
		case 0, 301, 302, 307, 308:
		default:
			return fmt.Errorf("redirectCode must be 301, 302, 307 or 308")
		}
	default:
		return fmt.Errorf("invalid domain kind %q", d.Kind)
	}
	switch d.TLSMode {
	case "", TLSAuto, TLSCustom, TLSNone:
	default:
		return fmt.Errorf("invalid tlsMode %q", d.TLSMode)
	}
	if d.Options.BasicAuth && d.AuthRef == "" {
		return fmt.Errorf("basic auth is enabled but the rule has no authRef")
	}
	return d.Options.Validate()
}

// ValidBasicAuthLines checks "user:hash" lines (hash must be bcrypt).
func ValidBasicAuthLines(lines []string) error {
	if len(lines) > 50 {
		return fmt.Errorf("at most 50 basic-auth users")
	}
	for _, l := range lines {
		u, h, ok := strings.Cut(l, ":")
		if !ok || !basicUsernameRe.MatchString(u) || !(strings.HasPrefix(h, "$2a$") || strings.HasPrefix(h, "$2b$") || strings.HasPrefix(h, "$2y$")) {
			return fmt.Errorf("invalid basic-auth entry for user %q", u)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Upstream policy.

// ReservedListenPorts can never be a tcp/udp listen port: 80, 443, the probe and
// admin entrypoints.
var ReservedListenPorts = map[int]bool{80: true, 443: true, ProxyProbePort: true, ProxyAdminPort: true}

// ValidateListenPort checks a tcp/udp host port (1024-65535, not reserved).
func ValidateListenPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("listen port must be between 1024 and 65535")
	}
	if ReservedListenPorts[port] {
		return fmt.Errorf("port %d is reserved for the proxy", port)
	}
	return nil
}

// CheckUpstream validates one "host:port" external upstream against p. It does
// not resolve DNS names: resolved addresses are judged by CheckResolved, by the
// control plane when a route is saved and by the agent on every apply.
func CheckUpstream(address string, p UpstreamPolicy) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("upstream %q must be host:port", address)
	}
	n := 0
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil || n < 1 || n > 65535 || fmt.Sprint(n) != port {
		return fmt.Errorf("upstream %q has an invalid port", address)
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasPrefix(lower, "nebulactrl-") || lower == ProxyContainer {
		return fmt.Errorf("upstream %q targets a NebulaCtrl infrastructure container", address)
	}
	if ip, err := netip.ParseAddr(strings.Trim(lower, "[]")); err == nil {
		ip = ip.Unmap()
		switch {
		case ip.IsUnspecified():
			return fmt.Errorf("upstream %q is an unspecified address", address)
		case ip.IsLoopback():
			if !p.AllowLoopback {
				return fmt.Errorf("upstream %q is a loopback address; an administrator can allow loopback per server", address)
			}
		case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
			if !p.AllowLinkLocal {
				return fmt.Errorf("upstream %q is a link-local address (includes cloud metadata); an administrator can allow it per server", address)
			}
		case metadataAddrs[ip]:
			if !p.AllowLinkLocal {
				return fmt.Errorf("upstream %q is a cloud metadata address; an administrator can allow it per server", address)
			}
		case ip.IsMulticast():
			return fmt.Errorf("upstream %q is a multicast address", address)
		}
		return nil
	}
	if !hostPattern.MatchString(host) {
		return fmt.Errorf("upstream host %q is not a valid name or IP", host)
	}
	switch {
	case lower == "localhost" || strings.HasSuffix(lower, ".localhost"):
		if !p.AllowLoopback {
			return fmt.Errorf("upstream %q is loopback; an administrator can allow loopback per server", address)
		}
	case lower == "metadata.google.internal" || lower == "metadata" || lower == "instance-data":
		if !p.AllowLinkLocal {
			return fmt.Errorf("upstream %q is a cloud metadata name; an administrator can allow it per server", address)
		}
	case lower == HostGatewayName:
		if !p.AllowHostGateway {
			return fmt.Errorf("%s is not available: host gateway access is disabled for this server", HostGatewayName)
		}
	}
	if n == ProxyAdminPort && (lower == "localhost" || lower == HostGatewayName) {
		return fmt.Errorf("upstream %q targets the proxy admin port", address)
	}
	return nil
}
