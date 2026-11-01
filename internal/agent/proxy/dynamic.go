package proxy

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Dynamic (REST provider) config model: only the fields the agent emits.

type dynConfig struct {
	HTTP dynHTTP       `json:"http"`
	TCP  *dynTCP       `json:"tcp,omitempty"`
	UDP  *dynUDP       `json:"udp,omitempty"`
	TLS  *dynTLSConfig `json:"tls,omitempty"`
}

type dynHTTP struct {
	Routers           map[string]*dynRouter           `json:"routers"`
	Services          map[string]*dynService          `json:"services"`
	Middlewares       map[string]*dynMiddleware       `json:"middlewares,omitempty"`
	ServersTransports map[string]*dynServersTransport `json:"serversTransports,omitempty"`
}

type dynRouter struct {
	EntryPoints   []string `json:"entryPoints"`
	Rule          string   `json:"rule"`
	Service       string   `json:"service"`
	Middlewares   []string `json:"middlewares,omitempty"`
	TLS           *dynTLS  `json:"tls,omitempty"`
	Observability *dynObs  `json:"observability,omitempty"`
	// Priority overrides the rule-length default; only the catch-all sets it.
	Priority int `json:"priority,omitempty"`
}

type dynTLS struct {
	CertResolver string         `json:"certResolver,omitempty"`
	Domains      []dynTLSDomain `json:"domains,omitempty"`
	// Options names a TLS option of the config's tls.options (the Authenticated
	// Origin Pulls client certificate requirement of a locked domain).
	Options string `json:"options,omitempty"`
}

// dynTLSDomain is a tls.domains entry: the names a resolver requests for a
// router instead of the ones derived from its rule.
type dynTLSDomain struct {
	Main string   `json:"main"`
	SANs []string `json:"sans,omitempty"`
}

// dynObs switches off per-router observability; it is only emitted to disable
// access logs on the probe and marker routers.
type dynObs struct {
	AccessLogs bool `json:"accessLogs"`
}

type dynService struct {
	LoadBalancer *dynLoadBalancer `json:"loadBalancer"`
}

type dynLoadBalancer struct {
	PassHostHeader   bool            `json:"passHostHeader"`
	Servers          []dynServer     `json:"servers"`
	HealthCheck      *dynHealthCheck `json:"healthCheck,omitempty"`
	ServersTransport string          `json:"serversTransport,omitempty"`
}

type dynServersTransport struct {
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	ServerName         string `json:"serverName,omitempty"`
}

// dynHealthCheck is Traefik's active upstream health check: a server that
// fails it is taken out of rotation until it passes again.
type dynHealthCheck struct {
	Path            string `json:"path"`
	Interval        string `json:"interval"`
	Timeout         string `json:"timeout"`
	FollowRedirects bool   `json:"followRedirects"`
}

// Traefik upstream health check cadence.
const (
	healthInterval = "5s"
	healthTimeout  = "3s"
)

func healthCheckFor(path string) *dynHealthCheck {
	if path == "" {
		return nil
	}
	return &dynHealthCheck{Path: path, Interval: healthInterval, Timeout: healthTimeout}
}

type dynServer struct {
	URL string `json:"url"`
}

type dynMiddleware struct {
	RedirectScheme *dynRedirectScheme `json:"redirectScheme,omitempty"`
	RedirectRegex  *dynRedirectRegex  `json:"redirectRegex,omitempty"`
	StripPrefix    *dynStripPrefix    `json:"stripPrefix,omitempty"`
	AddPrefix      *dynAddPrefix      `json:"addPrefix,omitempty"`
	IPAllowList    *dynIPAllowList    `json:"ipAllowList,omitempty"`
	RateLimit      *dynRateLimit      `json:"rateLimit,omitempty"`
	BasicAuth      *dynBasicAuth      `json:"basicAuth,omitempty"`
	Headers        *dynHeaders        `json:"headers,omitempty"`
	Compress       *dynCompress       `json:"compress,omitempty"`
	Errors         *dynErrors         `json:"errors,omitempty"`
	ReplacePath    *dynReplacePath    `json:"replacePath,omitempty"`
}

// dynCompress is Traefik's compress middleware. The REST provider decodes the
// JSON without applying the middleware's defaults, so an empty object has no
// encodings and Traefik disables the router ("at least one encoding must be
// specified"): the defaults are spelled out. Server-sent events are never
// compressed, as compressing them buffers the stream.
type dynCompress struct {
	Encodings            []string `json:"encodings"`
	ExcludedContentTypes []string `json:"excludedContentTypes"`
}

// compressMiddleware is Traefik's default compression (zstd, brotli, gzip by
// client preference) without text/event-stream.
func compressMiddleware() *dynCompress {
	return &dynCompress{Encodings: []string{"zstd", "br", "gzip"}, ExcludedContentTypes: []string{"text/event-stream"}}
}

type dynRedirectScheme struct {
	Scheme    string `json:"scheme"`
	Port      string `json:"port,omitempty"`
	Permanent bool   `json:"permanent"`
}

type dynRedirectRegex struct {
	Regex       string `json:"regex"`
	Replacement string `json:"replacement"`
	Permanent   bool   `json:"permanent"`
}

type dynStripPrefix struct {
	Prefixes []string `json:"prefixes"`
}

type dynAddPrefix struct {
	Prefix string `json:"prefix"`
}

// dynErrors serves the error page of a failing upstream: a response with one
// of Status is replaced by the body the pages service returns for Query
// ({status} is the original status, which the response keeps).
type dynErrors struct {
	Status  []string `json:"status"`
	Service string   `json:"service"`
	Query   string   `json:"query"`
}

type dynReplacePath struct {
	Path string `json:"path"`
}

type dynIPStrategy struct {
	Depth int `json:"depth,omitempty"`
}

type dynIPAllowList struct {
	SourceRange      []string       `json:"sourceRange"`
	IPStrategy       *dynIPStrategy `json:"ipStrategy,omitempty"`
	RejectStatusCode int            `json:"rejectStatusCode,omitempty"`
}

type dynRateLimit struct {
	Average         int                 `json:"average"`
	Period          string              `json:"period"`
	Burst           int                 `json:"burst"`
	SourceCriterion *dynSourceCriterion `json:"sourceCriterion,omitempty"`
}

type dynSourceCriterion struct {
	IPStrategy *dynIPStrategy `json:"ipStrategy,omitempty"`
}

type dynBasicAuth struct {
	Users []string `json:"users"`
}

type dynHeaders struct {
	CustomRequestHeaders          map[string]string `json:"customRequestHeaders,omitempty"`
	CustomResponseHeaders         map[string]string `json:"customResponseHeaders,omitempty"`
	ContentTypeNosniff            bool              `json:"contentTypeNosniff,omitempty"`
	FrameDeny                     bool              `json:"frameDeny,omitempty"`
	ReferrerPolicy                string            `json:"referrerPolicy,omitempty"`
	STSSeconds                    int64             `json:"stsSeconds,omitempty"`
	STSIncludeSubdomains          bool              `json:"stsIncludeSubdomains,omitempty"`
	AccessControlAllowOriginList  []string          `json:"accessControlAllowOriginList,omitempty"`
	AccessControlAllowMethods     []string          `json:"accessControlAllowMethods,omitempty"`
	AccessControlAllowHeaders     []string          `json:"accessControlAllowHeaders,omitempty"`
	AccessControlExposeHeaders    []string          `json:"accessControlExposeHeaders,omitempty"`
	AccessControlAllowCredentials bool              `json:"accessControlAllowCredentials,omitempty"`
	AccessControlMaxAge           int64             `json:"accessControlMaxAge,omitempty"`
	AddVaryHeader                 bool              `json:"addVaryHeader,omitempty"`
}

type dynTCP struct {
	Routers     map[string]*dynTCPRouter     `json:"routers"`
	Services    map[string]*dynTCPService    `json:"services"`
	Middlewares map[string]*dynTCPMiddleware `json:"middlewares,omitempty"`
}

type dynTCPRouter struct {
	EntryPoints []string `json:"entryPoints"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	Middlewares []string `json:"middlewares,omitempty"`
}

type dynTCPService struct {
	LoadBalancer *dynTCPLoadBalancer `json:"loadBalancer"`
}

type dynTCPLoadBalancer struct {
	Servers       []dynAddrServer   `json:"servers"`
	ProxyProtocol *dynProxyProtocol `json:"proxyProtocol,omitempty"`
}

type dynAddrServer struct {
	Address string `json:"address"`
}

type dynProxyProtocol struct {
	Version int `json:"version"`
}

type dynTCPMiddleware struct {
	IPAllowList *dynTCPIPAllowList `json:"ipAllowList,omitempty"`
}

type dynTCPIPAllowList struct {
	SourceRange []string `json:"sourceRange"`
}

type dynUDP struct {
	Routers  map[string]*dynUDPRouter  `json:"routers"`
	Services map[string]*dynUDPService `json:"services"`
}

type dynUDPRouter struct {
	EntryPoints []string `json:"entryPoints"`
	Service     string   `json:"service"`
}

type dynUDPService struct {
	LoadBalancer *dynUDPLoadBalancer `json:"loadBalancer"`
}

type dynUDPLoadBalancer struct {
	Servers []dynAddrServer `json:"servers"`
}

type dynTLSConfig struct {
	Certificates []dynCertificate         `json:"certificates,omitempty"`
	Stores       map[string]*dynTLSStore  `json:"stores,omitempty"`
	Options      map[string]*dynTLSOption `json:"options,omitempty"`
}

// dynTLSStore carries the default store's generated certificate.
type dynTLSStore struct {
	DefaultGeneratedCert *dynDefaultGeneratedCert `json:"defaultGeneratedCert,omitempty"`
}

// dynDefaultGeneratedCert makes a resolver obtain the default certificate, so
// clients that match no router get a valid certificate instead of Traefik's
// self-signed one.
type dynDefaultGeneratedCert struct {
	Resolver string       `json:"resolver"`
	Domain   dynTLSDomain `json:"domain"`
}

// dynCertificate carries inline PEM: Traefik's FileOrContent type accepts the
// content itself, so nothing touches the container's filesystem.
type dynCertificate struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

// Options tunes BuildConfig.
type Options struct {
	// ACMEEmail, when set, makes non-local hostnames use the ACME resolver.
	ACMEEmail string
	// PublicHTTPSPort is the host port websecure is published on; it is used in
	// HTTP→HTTPS redirect Locations when it is not 443.
	PublicHTTPSPort int
	// Generation names the marker router (GenerationHost) that proves this
	// config is live. Traefik applies provider configs asynchronously.
	Generation int64
	// Certificates are the uploaded certificates with resolved keys. Only a
	// certificate that has a key and parses as a key pair is emitted; one
	// without a key is never rendered.
	Certificates []agentprotocol.CertRef
	// Policy is the upstream policy ValidateRoutes enforces.
	Policy agentprotocol.UpstreamPolicy
	// Pages enables the error pages: the errors middleware on every service
	// router, the catch-all 404 and maintenance mode. The pages server must be
	// running when it is set.
	Pages bool
	// DNSResolvers are the server's DNS-01 resolvers (ACMEEmail must be set;
	// the container's static config defines the same set) and Wildcards the
	// certificates requested through them. A wildcard whose provider has no
	// resolver is ignored.
	DNSResolvers []agentprotocol.DNSResolver
	Wildcards    []agentprotocol.WildcardCert
}

// hostRule renders Host(`a`) || Host(`b`) for a sorted-as-given host list. A
// wildcard host (*.example.com) matches exactly one label: HostRegexp.
func hostRule(hosts []string) string {
	parts := make([]string, len(hosts))
	for i, h := range hosts {
		if base, ok := strings.CutPrefix(h, "*."); ok {
			parts[i] = "HostRegexp(`^[^.]+\\." + regexp.QuoteMeta(base) + "$`)"
			continue
		}
		parts[i] = "Host(`" + h + "`)"
	}
	return strings.Join(parts, " || ")
}

// RouterNames returns the names of the http routers BuildConfig emits for
// routes, without the @rest provider suffix, including the generation marker.
func RouterNames(routes []agentprotocol.Route, opts Options) []string {
	cfg := buildDynamic(routes, opts)
	names := make([]string, 0, len(cfg.HTTP.Routers))
	for n := range cfg.HTTP.Routers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// L4RouterNames returns the tcp and udp router names BuildConfig emits.
func L4RouterNames(routes []agentprotocol.Route, opts Options) (tcp, udp []string) {
	cfg := buildDynamic(routes, opts)
	if cfg.TCP != nil {
		for n := range cfg.TCP.Routers {
			tcp = append(tcp, n)
		}
	}
	if cfg.UDP != nil {
		for n := range cfg.UDP.Routers {
			udp = append(udp, n)
		}
	}
	sort.Strings(tcp)
	sort.Strings(udp)
	return tcp, udp
}

// certUsable reports whether c can be rendered: it has a key and the pair parses.
func certUsable(c agentprotocol.CertRef) error {
	if c.KeyPEM == "" {
		return errNoKey
	}
	_, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
	return err
}

// certCovers reports whether a certificate's SAN list covers host (a wildcard
// matches exactly one label).
func certCovers(domains []string, host string) bool {
	host = strings.ToLower(host)
	for _, d := range domains {
		d = strings.ToLower(d)
		if d == host {
			return true
		}
		if strings.HasPrefix(d, "*.") {
			if i := strings.IndexByte(host, '.'); i > 0 && host[i+1:] == d[2:] {
				return true
			}
		}
	}
	return false
}

type builder struct {
	cfg          dynConfig
	opts         Options
	usable       []agentprotocol.CertRef
	needRedirect bool
	noLogs       *dynObs
	// dns is the set of providers with a resolver; wildcards the certificates
	// the proxy requests through them (both empty without an ACME email).
	dns       map[string]bool
	wildcards []agentprotocol.WildcardCert
	// tlsOptions are the Authenticated Origin Pulls options locked domains
	// reference, by name (see aopFor).
	tlsOptions map[string]*dynTLSOption
}

func (b *builder) covered(host string) bool {
	for _, c := range b.usable {
		if certCovers(c.Domains, host) {
			return true
		}
	}
	return false
}

// wildcardFor returns the wildcard certificate that serves host: one whose
// names cover it and, for a generated hostname, that opted in to cover them.
func (b *builder) wildcardFor(host string, generated bool) *agentprotocol.WildcardCert {
	for i := range b.wildcards {
		w := &b.wildcards[i]
		if agentprotocol.WildcardCovers(*w, host) && (!generated || w.CoverGenerated) {
			return w
		}
	}
	return nil
}

// tlsFor chooses the certificate source of a TLS router for one host, in this
// order: the store (no resolver) for custom-certificate domains, local names,
// hosts an uploaded certificate covers and hosts a wildcard certificate covers
// (the wildcard's anchor router requests it); the domain's DNS-01 resolver
// when it names one this server runs; the store for a wildcard host that no
// certificate covers (HTTP-01 cannot validate it); the ACME HTTP-01 resolver
// otherwise.
func (b *builder) tlsFor(host, mode, resolver string, generated bool) *dynTLS {
	if b.opts.ACMEEmail == "" || mode == agentprotocol.TLSCustom || isLocalName(strings.TrimPrefix(host, "*.")) || b.covered(host) {
		return &dynTLS{}
	}
	if b.wildcardFor(host, generated) != nil {
		return &dynTLS{}
	}
	if resolver != "" && b.dns[resolver] {
		t := &dynTLS{CertResolver: agentprotocol.DNSResolverName(resolver)}
		if agentprotocol.IsWildcardHost(host) {
			t.Domains = []dynTLSDomain{{Main: host}}
		}
		return t
	}
	if agentprotocol.IsWildcardHost(host) {
		return &dynTLS{}
	}
	return &dynTLS{CertResolver: CertResolver}
}

// wildcardAnchors renders the routers that make the proxy request each
// wildcard certificate (a router's tls.domains replaces the names derived from
// its rule; the unreachable host keeps the router from ever matching traffic)
// and the default store's generated certificate.
func (b *builder) wildcardAnchors() {
	for _, w := range b.wildcards {
		names := w.Names()
		dom := dynTLSDomain{Main: names[0], SANs: names[1:]}
		name := RouterPrefix + "cert-" + idShort(w.ID)
		b.cfg.HTTP.Routers[name] = &dynRouter{
			EntryPoints: []string{EntryWebSecure}, Rule: "Host(`" + name + ".nc.invalid`)", Service: "noop@internal",
			TLS:           &dynTLS{CertResolver: agentprotocol.DNSResolverName(w.Provider), Domains: []dynTLSDomain{dom}},
			Observability: b.noLogs, Priority: 1,
		}
		if w.Default {
			if b.cfg.TLS == nil {
				b.cfg.TLS = &dynTLSConfig{}
			}
			b.cfg.TLS.Stores = map[string]*dynTLSStore{"default": {
				DefaultGeneratedCert: &dynDefaultGeneratedCert{Resolver: agentprotocol.DNSResolverName(w.Provider), Domain: dom},
			}}
		}
	}
}

func buildDynamic(routes []agentprotocol.Route, opts Options) dynConfig {
	b := &builder{
		cfg: dynConfig{HTTP: dynHTTP{
			Routers:  map[string]*dynRouter{},
			Services: map[string]*dynService{},
		}},
		opts:   opts,
		noLogs: &dynObs{AccessLogs: false},
	}
	for _, c := range opts.Certificates {
		if certUsable(c) == nil {
			b.usable = append(b.usable, c)
		}
	}
	if opts.ACMEEmail != "" {
		b.dns = resolverSet(opts.DNSResolvers)
		for _, w := range opts.Wildcards {
			if b.dns[w.Provider] {
				b.wildcards = append(b.wildcards, w)
			}
		}
		sort.Slice(b.wildcards, func(i, j int) bool { return b.wildcards[i].Domain < b.wildcards[j].Domain })
	}
	for _, r := range routes {
		if r.IsL4() {
			b.l4(r)
		} else {
			b.http(r)
		}
	}
	if b.needRedirect {
		rs := &dynRedirectScheme{Scheme: "https", Permanent: true}
		if opts.PublicHTTPSPort != 0 && opts.PublicHTTPSPort != 443 {
			rs.Port = strconv.Itoa(opts.PublicHTTPSPort)
		}
		b.addMiddleware(redirectMiddleware, &dynMiddleware{RedirectScheme: rs})
	}
	if len(b.usable) > 0 {
		t := &dynTLSConfig{}
		for _, c := range b.usable {
			t.Certificates = append(t.Certificates, dynCertificate{CertFile: c.CertPEM, KeyFile: c.KeyPEM})
		}
		b.cfg.TLS = t
	}
	b.wildcardAnchors()
	b.attachTLSOptions()
	if opts.Pages {
		b.pagesRoutes()
	}
	b.cfg.HTTP.Routers[genRouter] = &dynRouter{
		EntryPoints: []string{EntryProbe}, Rule: "Host(`" + GenerationHost(opts.Generation) + "`)",
		Service: "ping@internal", Observability: b.noLogs,
	}
	return b.cfg
}

func (b *builder) addMiddleware(name string, m *dynMiddleware) {
	if b.cfg.HTTP.Middlewares == nil {
		b.cfg.HTTP.Middlewares = map[string]*dynMiddleware{}
	}
	b.cfg.HTTP.Middlewares[name] = m
}

// pagesRoutes renders what the pages server adds to every config: its service,
// the errors middleware that service routers use, and the low-priority
// catch-all that answers hosts no router matches with the 404 page. The
// catch-all is not on the probe entrypoint, so verification still sees an
// unrouted host as 404. Its names never match a service router
// (nc-<uuid>-…), so traffic analytics count it as unrouted.
func (b *builder) pagesRoutes() {
	b.cfg.HTTP.Services[pagesService] = &dynService{LoadBalancer: &dynLoadBalancer{
		PassHostHeader: true,
		Servers:        []dynServer{{URL: "http://" + agentprotocol.PagesContainer + ":" + strconv.Itoa(agentprotocol.PagesPort)}},
	}}
	b.addMiddleware(errorsMiddleware, &dynMiddleware{Errors: &dynErrors{
		Status: []string{"502-504"}, Service: pagesService, Query: "/err/{status}",
	}})
	b.addMiddleware(notFoundMiddleware, &dynMiddleware{ReplacePath: &dynReplacePath{Path: pages.ErrorPath(http.StatusNotFound)}})
	const rule = "PathPrefix(`/`)"
	b.cfg.HTTP.Routers[notFoundRouter+"-http"] = &dynRouter{
		EntryPoints: []string{EntryWeb}, Rule: rule, Service: pagesService,
		Middlewares: []string{notFoundMiddleware}, Priority: 1,
	}
	b.cfg.HTTP.Routers[notFoundRouter+"-https"] = &dynRouter{
		EntryPoints: []string{EntryWebSecure}, Rule: rule, Service: pagesService,
		Middlewares: []string{notFoundMiddleware}, TLS: &dynTLS{}, Priority: 1,
	}
}

// http renders one http route: the Traefik service, the default routers of
// hosts without a rule, and the per-domain routers of its rules.
func (b *builder) http(r agentprotocol.Route) {
	name := ServiceName(r.ServiceID)
	scheme := "http"
	if r.Scheme == "https" {
		scheme = "https"
	}
	servers := make([]dynServer, len(r.Upstreams))
	for i, u := range r.Upstreams {
		servers[i] = dynServer{URL: scheme + "://" + u}
	}
	lb := &dynLoadBalancer{
		PassHostHeader: r.PassHost == nil || *r.PassHost,
		Servers:        servers,
		HealthCheck:    healthCheckFor(r.HealthPath),
	}
	if t := r.Transport; t != nil && (t.InsecureSkipVerify || t.ServerName != "") {
		if b.cfg.HTTP.ServersTransports == nil {
			b.cfg.HTTP.ServersTransports = map[string]*dynServersTransport{}
		}
		b.cfg.HTTP.ServersTransports[name] = &dynServersTransport{InsecureSkipVerify: t.InsecureSkipVerify, ServerName: t.ServerName}
		lb.ServersTransport = name
	}
	b.cfg.HTTP.Services[name] = &dynService{LoadBalancer: lb}

	ruled := map[string]bool{}
	for _, d := range r.Domains {
		ruled[d.Host] = true
	}
	var rest []string
	for _, h := range r.Hosts {
		if !ruled[h] {
			rest = append(rest, h)
		}
	}
	t := target{service: name, origin: r.OriginLock}
	if r.HostOverride != "" {
		t.host = name + "-host"
		b.addMiddleware(t.host, &dynMiddleware{Headers: &dynHeaders{CustomRequestHeaders: map[string]string{"Host": r.HostOverride}}})
	}
	if r.Maintenance != nil {
		t.maint = name + "-maint"
		if b.opts.Pages {
			b.addMiddleware(t.maint, &dynMiddleware{ReplacePath: &dynReplacePath{Path: pages.MaintenancePath(r.ServiceID)}})
		}
	}
	if len(rest) > 0 {
		b.defaultHosts(t, rest)
	}
	for _, d := range r.Domains {
		b.domain(t, d)
	}
}

// target is where the serving routers of one route send requests: the route's
// Traefik service, or the pages server while the route is in maintenance.
type target struct {
	service string
	// maint names the replacePath middleware of the maintenance page; empty
	// outside maintenance.
	maint string
	// origin is the route's Cloudflare lock data; lock names the lock
	// middleware of the domain being rendered (empty: the domain is not locked).
	origin *agentprotocol.OriginLock
	lock   string
	// host names the middleware that sets the Host header to the route's
	// original upstream name after the agent pinned it to addresses; empty
	// when the Host header needs no override.
	host string
}

// withHost returns chain followed by the Host override middleware, if any.
func (t target) withHost(chain []string) []string {
	if t.host == "" {
		return chain
	}
	return append(slices.Clone(chain), t.host)
}

// serving returns the service and middlewares of a serving (non-probe,
// non-redirect) router: in maintenance the pages server behind the page's
// replacePath, otherwise the route's service behind chain, wrapped by the
// errors middleware when the pages server runs.
func (b *builder) serving(t target, chain []string) (service string, mws []string) {
	if t.maint != "" {
		// The lock stays in front of the maintenance page: a locked origin
		// answers Cloudflare only, in maintenance too.
		var mws []string
		if t.lock != "" {
			mws = append(mws, t.lock)
		}
		if !b.opts.Pages {
			// No pages server: a bare 503, never the service itself.
			b.cfg.HTTP.Services[maintenanceDownService] = &dynService{LoadBalancer: &dynLoadBalancer{PassHostHeader: true, Servers: []dynServer{}}}
			return maintenanceDownService, mws
		}
		return pagesService, append(mws, t.maint)
	}
	chain = t.withHost(chain)
	if b.opts.Pages {
		return t.service, append([]string{errorsMiddleware}, chain...)
	}
	return t.service, chain
}

// defaultHosts renders the routers of hosts served with defaults: https (ACME
// for public names, default certificate for local ones, so a service with both
// gets two routers), http redirect, and probe.
func (b *builder) defaultHosts(t target, hosts []string) {
	name := t.service
	svc, mws := b.serving(t, nil)
	var public, local []string
	for _, h := range hosts {
		if t := b.tlsFor(h, "", "", false); t.CertResolver != "" {
			public = append(public, h)
		} else {
			local = append(local, h)
		}
	}
	if len(public) > 0 {
		b.cfg.HTTP.Routers[name+"-https"] = &dynRouter{
			EntryPoints: []string{EntryWebSecure}, Rule: hostRule(public), Service: svc, Middlewares: mws,
			TLS: &dynTLS{CertResolver: CertResolver},
		}
	}
	if len(local) > 0 {
		routerName := name + "-https"
		if len(public) > 0 {
			routerName += "-local"
		}
		b.cfg.HTTP.Routers[routerName] = &dynRouter{
			EntryPoints: []string{EntryWebSecure}, Rule: hostRule(local), Service: svc, Middlewares: mws,
			TLS: &dynTLS{},
		}
	}
	b.needRedirect = true
	b.cfg.HTTP.Routers[name+"-http"] = &dynRouter{
		EntryPoints: []string{EntryWeb}, Rule: hostRule(hosts), Service: "noop@internal",
		Middlewares: []string{redirectMiddleware},
	}
	b.cfg.HTTP.Routers[name+"-probe"] = &dynRouter{
		EntryPoints: []string{EntryProbe}, Rule: hostRule(hosts), Service: name,
		Middlewares: t.withHost(nil), Observability: b.noLogs,
	}
}

func domainRule(d agentprotocol.DomainRule) string {
	rule := hostRule([]string{d.Host})
	if d.PathPrefix != "" {
		rule += " && PathPrefix(`" + d.PathPrefix + "`)"
	}
	return rule
}

// tlsForDomain is tlsFor for a domain rule. A tunnelled domain never uses the
// HTTP-01 resolver: its public name resolves to Cloudflare, which terminates
// the edge TLS, so the challenge could not reach this proxy. It keeps the
// default certificate, a covering certificate or wildcard, or its DNS-01
// resolver.
func (b *builder) tlsForDomain(d agentprotocol.DomainRule) *dynTLS {
	t := b.tlsFor(d.Host, d.TLSMode, d.DNSResolver, d.Generated)
	if d.Tunnel && t.CertResolver == CertResolver {
		return &dynTLS{}
	}
	return t
}

// domain renders the routers of one domain rule.
func (b *builder) domain(t target, d agentprotocol.DomainRule) {
	svc := t.service
	n := svc + "-d" + idShort(d.ID)
	rule := domainRule(d)
	tlsOn := d.TLSMode != agentprotocol.TLSNone

	if d.Kind == agentprotocol.DomainRedirect {
		b.redirectDomain(n, d, rule, tlsOn)
		return
	}

	t.lock = b.lockFor(t, d)
	chain, probeChain := b.chain(n, d, t.lock)
	// A tunnelled domain arrives over the tunnel as plain HTTP on the web
	// entrypoint (Cloudflare terminates the public TLS): redirecting it to
	// HTTPS there would loop.
	forceHTTPS := tlsOn && d.Options.ForceHTTPSOrDefault() && !d.Tunnel
	servSvc, servMws := b.serving(t, chain)
	if tlsOn {
		b.cfg.HTTP.Routers[n+"-https"] = &dynRouter{
			EntryPoints: []string{EntryWebSecure}, Rule: rule, Service: servSvc,
			Middlewares: servMws, TLS: b.aopFor(t, d, b.tlsForDomain(d)),
		}
	}
	if forceHTTPS {
		b.needRedirect = true
		b.cfg.HTTP.Routers[n+"-http"] = &dynRouter{
			EntryPoints: []string{EntryWeb}, Rule: rule, Service: "noop@internal",
			Middlewares: withLock(t.lock, redirectMiddleware),
		}
	} else {
		b.cfg.HTTP.Routers[n+"-http"] = &dynRouter{
			EntryPoints: []string{EntryWeb}, Rule: rule, Service: servSvc, Middlewares: servMws,
		}
	}
	b.cfg.HTTP.Routers[n+"-probe"] = &dynRouter{
		EntryPoints: []string{EntryProbe}, Rule: rule, Service: svc,
		Middlewares: t.withHost(probeChain), Observability: b.noLogs,
	}
}

// redirectDomain renders a redirect-only domain: no upstream, a redirectRegex
// on noop@internal.
func (b *builder) redirectDomain(n string, d agentprotocol.DomainRule, rule string, tlsOn bool) {
	scheme, port := "https", ""
	if !tlsOn {
		scheme = "http"
	} else if b.opts.PublicHTTPSPort != 0 && b.opts.PublicHTTPSPort != 443 {
		port = ":" + strconv.Itoa(b.opts.PublicHTTPSPort)
	}
	target := scheme + "://" + d.RedirectTo + port
	repl := target + "/"
	if d.PreservePath {
		repl = target + "${1}"
	}
	mw := n + "-redir"
	b.addMiddleware(mw, &dynMiddleware{RedirectRegex: &dynRedirectRegex{
		Regex: `^https?://[^/]+(.*)$`, Replacement: repl,
		Permanent: d.RedirectCode == 0 || d.RedirectCode == 301 || d.RedirectCode == 308,
	}})
	if tlsOn {
		b.cfg.HTTP.Routers[n+"-redir"] = &dynRouter{
			EntryPoints: []string{EntryWebSecure}, Rule: rule, Service: "noop@internal",
			Middlewares: []string{mw}, TLS: b.tlsForDomain(d),
		}
	}
	b.cfg.HTTP.Routers[n+"-http"] = &dynRouter{
		EntryPoints: []string{EntryWeb}, Rule: rule, Service: "noop@internal", Middlewares: []string{mw},
	}
}

// chain renders the middleware chain of a serve domain, cheapest rejection
// first: ipAllowList → rateLimit → basicAuth → redirectRegex list → stripPrefix
// → addPrefix → headers → compress. The probe chain keeps only the prefix
// rewrites: verification checks routing, not access rules.
func (b *builder) chain(n string, d agentprotocol.DomainRule, lock string) (full, probe []string) {
	o := d.Options
	add := func(kind string, m *dynMiddleware, inProbe bool) {
		name := n + "-" + kind
		b.addMiddleware(name, m)
		full = append(full, name)
		if inProbe {
			probe = append(probe, name)
		}
	}
	// A locked domain's peer is Cloudflare (the lock middleware runs first), and
	// the visitor is the address Cloudflare saw connect: the last X-Forwarded-For
	// entry, depth 1. See originLockRealIPDepth for why that entry and not another.
	strategy := func(depth int) *dynIPStrategy {
		if lock != "" {
			depth = originLockRealIPDepth
		}
		if depth > 0 {
			return &dynIPStrategy{Depth: depth}
		}
		return nil
	}
	if lock != "" {
		// Not a user rule: no probe copy (verification checks routing) and no
		// name per domain, the middleware is shared by every router of the CIDR set.
		full = append(full, lock)
	}
	if a := o.Access; a != nil {
		// An allow list that judges the TCP peer (no ipStrategy) is rendered as
		// written: see edge.go for why the Docker ranges are not taken out.
		strat := strategy(a.TrustedProxyDepth)
		src := effectiveSourceRange(a.AllowCIDRs, a.DenyCIDRs)
		add("ipallow", &dynMiddleware{IPAllowList: &dynIPAllowList{
			SourceRange: src, IPStrategy: strat, RejectStatusCode: a.RejectStatus,
		}}, false)
	}
	if rl := o.RateLimit; rl != nil {
		m := &dynRateLimit{Average: rl.Average, Period: strconv.Itoa(rl.PeriodSeconds) + "s", Burst: rl.Burst}
		if s := strategy(rl.TrustedProxyDepth); s != nil {
			m.SourceCriterion = &dynSourceCriterion{IPStrategy: s}
		}
		add("ratelimit", &dynMiddleware{RateLimit: m}, false)
	}
	if o.BasicAuth {
		add("auth", &dynMiddleware{BasicAuth: &dynBasicAuth{Users: append([]string(nil), d.AuthUsers...)}}, false)
	}
	for i, rd := range o.Redirects {
		add("redirect"+strconv.Itoa(i+1), &dynMiddleware{RedirectRegex: &dynRedirectRegex{
			Regex: rd.Regex, Replacement: rd.Replacement, Permanent: rd.Permanent,
		}}, false)
	}
	if d.StripPrefix && d.PathPrefix != "" {
		add("strip", &dynMiddleware{StripPrefix: &dynStripPrefix{Prefixes: []string{d.PathPrefix}}}, true)
	}
	if d.AddPrefix != "" {
		add("addprefix", &dynMiddleware{AddPrefix: &dynAddPrefix{Prefix: d.AddPrefix}}, true)
	}
	if h := headersFor(o); h != nil {
		add("headers", &dynMiddleware{Headers: h}, false)
	}
	if o.Compress {
		add("compress", &dynMiddleware{Compress: compressMiddleware()}, false)
	}
	return full, probe
}

// headersFor merges the security preset, CORS, custom headers and noindex into
// one headers middleware (nil when none applies).
func headersFor(o agentprotocol.RouteOptions) *dynHeaders {
	h := &dynHeaders{}
	used := false
	switch o.SecurityHeaders {
	case "basic", "strict":
		h.ContentTypeNosniff, h.FrameDeny = true, true
		h.ReferrerPolicy = "strict-origin-when-cross-origin"
		used = true
		if o.SecurityHeaders == "strict" {
			h.STSSeconds, h.STSIncludeSubdomains = 31536000, true
		}
	}
	if len(o.RequestHeaders) > 0 {
		h.CustomRequestHeaders = map[string]string{}
		for _, x := range o.RequestHeaders {
			h.CustomRequestHeaders[x.Name] = x.Value
		}
		used = true
	}
	if len(o.ResponseHeaders) > 0 || o.Noindex {
		h.CustomResponseHeaders = map[string]string{}
		for _, x := range o.ResponseHeaders {
			h.CustomResponseHeaders[x.Name] = x.Value
		}
		if o.Noindex {
			h.CustomResponseHeaders["X-Robots-Tag"] = "noindex, nofollow"
		}
		used = true
	}
	if c := o.CORS; c != nil {
		h.AccessControlAllowOriginList = append([]string(nil), c.Origins...)
		h.AccessControlAllowMethods = append([]string(nil), c.Methods...)
		h.AccessControlAllowHeaders = append([]string(nil), c.Headers...)
		h.AccessControlExposeHeaders = append([]string(nil), c.ExposeHeaders...)
		h.AccessControlAllowCredentials = c.Credentials
		h.AccessControlMaxAge = int64(c.MaxAgeSeconds)
		h.AddVaryHeader = true
		used = true
	}
	if !used {
		return nil
	}
	return h
}

// l4 renders a tcp or udp route: a router on the static entrypoint, a service,
// and (tcp) the source allowlist middleware.
func (b *builder) l4(r agentprotocol.Route) {
	name := ServiceName(r.ServiceID) + "-" + r.Protocol
	entry := L4Port{Proto: r.Protocol, Port: r.ListenPort}.Entry()
	servers := make([]dynAddrServer, len(r.Upstreams))
	for i, u := range r.Upstreams {
		servers[i] = dynAddrServer{Address: u}
	}
	if r.Protocol == agentprotocol.ProtoUDP {
		if b.cfg.UDP == nil {
			b.cfg.UDP = &dynUDP{Routers: map[string]*dynUDPRouter{}, Services: map[string]*dynUDPService{}}
		}
		b.cfg.UDP.Routers[name] = &dynUDPRouter{EntryPoints: []string{entry}, Service: name}
		b.cfg.UDP.Services[name] = &dynUDPService{LoadBalancer: &dynUDPLoadBalancer{Servers: servers}}
		return
	}
	if b.cfg.TCP == nil {
		b.cfg.TCP = &dynTCP{Routers: map[string]*dynTCPRouter{}, Services: map[string]*dynTCPService{}}
	}
	lb := &dynTCPLoadBalancer{Servers: servers}
	if r.ProxyProtocol > 0 {
		lb.ProxyProtocol = &dynProxyProtocol{Version: r.ProxyProtocol}
	}
	rt := &dynTCPRouter{EntryPoints: []string{entry}, Rule: "HostSNI(`*`)", Service: name}
	if len(r.AllowCIDRs) > 0 || len(r.DenyCIDRs) > 0 {
		if b.cfg.TCP.Middlewares == nil {
			b.cfg.TCP.Middlewares = map[string]*dynTCPMiddleware{}
		}
		mw := name + "-allow"
		b.cfg.TCP.Middlewares[mw] = &dynTCPMiddleware{IPAllowList: &dynTCPIPAllowList{SourceRange: effectiveSourceRange(r.AllowCIDRs, r.DenyCIDRs)}}
		rt.Middlewares = []string{mw}
	}
	b.cfg.TCP.Routers[name] = rt
	b.cfg.TCP.Services[name] = &dynTCPService{LoadBalancer: lb}
}

// neverMatch is an ipAllowList range no client address falls into.
const neverMatch = "0.0.0.0/32"

// effectiveSourceRange renders allow minus deny (deny first) as ipAllowList
// source ranges. ValidateRoutes has already rejected rule sets that fail, so
// the error path only guards direct callers: it fails closed.
func effectiveSourceRange(allow, deny []string) []string {
	out, err := agentprotocol.EffectiveAllow(allow, deny)
	if err != nil || len(out) == 0 {
		return []string{neverMatch}
	}
	return out
}

// BuildConfig renders the complete dynamic config for a route table. The
// config is always pushed whole (PUT /api/providers/rest), never patched.
// Hosts that match no router get Traefik's default 404, so an unrouted Host
// can never pass a verification probe.
func BuildConfig(routes []agentprotocol.Route, opts Options) ([]byte, error) {
	if err := ValidateRoutes(routes, opts.Policy); err != nil {
		return nil, err
	}
	// Rules carry && and path prefixes: keep them readable instead of \u0026.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(buildDynamic(routes, opts)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
