package projectconfig

import (
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// splitDomainKey splits a domains key "host" or "host/path-prefix" into the
// hostname and the path prefix ("" or "/api", no trailing slash).
func splitDomainKey(key string) (host, prefix string, ok bool) {
	host = key
	if i := strings.IndexByte(key, '/'); i >= 0 {
		host, prefix = key[:i], key[i:]
	}
	if host == "" {
		return "", "", false
	}
	if prefix != "" {
		if len(prefix) < 2 || strings.HasSuffix(prefix, "/") || len(prefix) > 200 ||
			strings.ContainsAny(prefix, " \t\r\n?#\\") || strings.Contains(prefix, "..") || strings.Contains(prefix, "//") {
			return host, prefix, false
		}
	}
	return host, prefix, true
}

func (c *svcCtx) checkDomains() {
	doms := c.def.Domains
	if len(doms) > MaxDomainsPerService {
		c.err(CodeLimitExceeded, rp("domains"), "%d domains; at most %d per service", len(doms), MaxDomainsPerService)
	}
	d := c.def
	for _, key := range sortedKeys(doms) {
		dm := doms[key]
		if dm == nil {
			continue
		}
		host, _, ok := splitDomainKey(key)
		switch {
		case !ok:
			c.err(CodeInvalidValue, rp("domains", key), "%q is not a domain key: write host or host/path-prefix, for example shop.acme.com/api", key)
		case host != strings.ToLower(host):
			c.err(CodeInvalidValue, rp("domains", key), "hostnames are lower-case: write %q", strings.ToLower(host))
		case !agentprotocol.ValidRouteHost(host):
			c.err(CodeInvalidValue, rp("domains", key), "%q is not a valid hostname (a wildcard is written *.example.com)", host)
		}
		c.checkDomain(key, dm, d)
	}
	if d.GenerateDomain != nil && *d.GenerateDomain && d.Port != nil && *d.Port == 0 && c.kind != KindStack {
		c.warn(CodeInvalidValue, rp("generate_domain"), "generate_domain needs a port: a worker has no HTTP service to publish")
	}
}

// checkDomainProcess validates which process of a service with processes a
// domain routes to, with the rules the platform applies when it creates the domain.
func (c *svcCtx) checkDomainProcess(key string, dm *Domain, kind string) {
	if kind != "serve" {
		return
	}
	cfg, _, ok := c.toConfig()
	if !ok {
		return
	}
	_, _, err := cfg.WithDefaults().ResolveDomainProcess(derefStr(dm.Process), derefInt(dm.TargetPort))
	if err == nil {
		return
	}
	path := rp("domains", key)
	if ae, isAPI := apierr.As(err); isAPI {
		switch ae.Code {
		case "unknown_process", "process_has_no_port":
			path = rp("domains", key, "process")
		case "invalid_target_port":
			path = rp("domains", key, "target_port")
		}
	}
	g := c.err(CodeInvalidValue, path, "%s", err.Error())
	if ae, isAPI := apierr.As(err); isAPI && ae.Code == "process_required" {
		g.Hint = "write process = \"<name>\" on the domain"
	}
}

func (c *svcCtx) checkDomain(key string, dm *Domain, d *Service) {
	dp := func(k string) []string { return rp("domains", key, k) }
	if dm.TargetPort != nil {
		c.intIn(dp("target_port"), dm.TargetPort, 1, 65535)
	}
	kind := "serve"
	if dm.Kind != nil {
		kind = *dm.Kind
		if kind != "serve" && kind != "redirect" {
			c.err(CodeInvalidValue, dp("kind"), "kind must be serve or redirect")
		}
	}
	if kind == "redirect" {
		switch {
		case dm.RedirectTo == nil:
			c.err(CodeMissingRequired, dp("redirect_to"), "a redirect domain needs `redirect_to`")
		case !agentprotocol.ValidHostname(*dm.RedirectTo) || len(*dm.RedirectTo) > 253:
			c.err(CodeInvalidValue, dp("redirect_to"), "redirect_to must be a hostname")
		}
	}
	if dm.RedirectCode != nil {
		switch *dm.RedirectCode {
		case 301, 302, 307, 308:
		default:
			c.err(CodeInvalidValue, dp("redirect_code"), "redirect_code must be 301, 302, 307 or 308")
		}
	}
	if dm.AddPrefix != nil && (len(*dm.AddPrefix) > 200 || (*dm.AddPrefix != "" && !strings.HasPrefix(*dm.AddPrefix, "/"))) {
		c.err(CodeInvalidValue, dp("add_prefix"), "add_prefix is empty or a path starting with / (at most 200 characters)")
	}
	if dm.TLS != nil {
		switch *dm.TLS {
		case "auto", "custom", "none":
		default:
			c.err(CodeInvalidValue, dp("tls"), "tls must be auto, custom or none")
		}
	}
	if dm.Certificate != nil {
		if strings.TrimSpace(*dm.Certificate) == "" || len(*dm.Certificate) > 200 {
			c.err(CodeInvalidValue, dp("certificate"), "a certificate is named by its name: 1 to 200 characters")
		}
		if dm.TLS == nil || *dm.TLS != "custom" {
			c.err(CodeInvalidValue, dp("certificate"), "certificate needs tls = \"custom\"")
		}
	}
	if dm.DNSResolver != nil && !dnsResolver.MatchString(*dm.DNSResolver) {
		c.err(CodeInvalidValue, dp("dns_resolver"), "dns_resolver is a DNS provider code such as cloudflare")
	}
	switch c.kind {
	case KindStack:
		if kind == "serve" && dm.Component == nil {
			c.err(CodeMissingRequired, dp("component"), "a domain of a stack needs `component`, the compose service it serves")
		}
		if dm.Process != nil {
			c.err(CodeInvalidValue, dp("process"), "process only applies to apps; a stack domain names its compose service in `component`")
		}
	case KindRoute:
		if d.Route != nil && d.Route.Protocol != nil && *d.Route.Protocol != "http" {
			c.err(CodeInvalidValue, rp("domains", key), "only http routes have domains; this route is %s", *d.Route.Protocol)
		}
		if dm.Component != nil {
			c.err(CodeInvalidValue, dp("component"), "component only applies to stacks")
		}
		if dm.Process != nil {
			c.err(CodeInvalidValue, dp("process"), "process only applies to apps")
		}
	default:
		if dm.Component != nil {
			c.err(CodeInvalidValue, dp("component"), "component only applies to stacks")
		}
		if len(d.Processes) > 0 {
			c.checkDomainProcess(key, dm, kind)
			break
		}
		if dm.Process != nil {
			c.err(CodeInvalidValue, dp("process"), "process only applies to a service that has `processes`")
		}
		if kind == "serve" && dm.TargetPort == nil && (d.Port == nil || *d.Port == 0) {
			g := c.err(CodeMissingRequired, rp("domains", key), "a domain needs the service `port` or its own `target_port`; a service without a port is a worker")
			g.Hint = "write port = <number> on the service, or target_port on the domain"
		}
	}
	if o := dm.Options; o != nil {
		c.checkRouteOptions(key, o)
	}
}

func (c *svcCtx) checkRouteOptions(key string, o *RouteOptions) {
	op := func(k ...string) []string { return append(rp("domains", key, "options"), k...) }
	if o.SecurityHeaders != nil && *o.SecurityHeaders != "basic" && *o.SecurityHeaders != "strict" {
		c.err(CodeInvalidValue, op("security_headers"), "security_headers must be basic or strict")
	}
	if o.CORS != nil && len(o.CORS.Origins) == 0 {
		c.err(CodeMissingRequired, op("cors", "origins"), "cors needs at least one origin")
	}
	if o.CORS != nil && o.CORS.MaxAgeSeconds != nil && *o.CORS.MaxAgeSeconds < 0 {
		c.err(CodeInvalidValue, op("cors", "max_age_seconds"), "max_age_seconds must be 0 or more")
	}
	for i, r := range o.Redirects {
		rpth := op("redirects", "["+itoa(i)+"]")
		if r.Regex == nil || r.Replacement == nil {
			c.err(CodeMissingRequired, rpth, "a redirect needs `regex` and `replacement`")
		}
	}
	if rl := o.RateLimit; rl != nil {
		if rl.Average == nil || rl.PeriodSeconds == nil || rl.Burst == nil {
			c.err(CodeMissingRequired, op("rate_limit"), "rate_limit needs `average`, `period_seconds` and `burst`")
		}
	}
	if a := o.Access; a != nil {
		if err := agentprotocol.ValidateAccessLists(a.AllowCIDRs, a.DenyCIDRs, false); err != nil {
			c.err(CodeInvalidValue, op("access"), "access: %s", err)
		}
		if a.RejectStatus != nil && (*a.RejectStatus < 400 || *a.RejectStatus > 599) {
			c.err(CodeInvalidValue, op("access", "reject_status"), "reject_status must be between 400 and 599")
		}
	}
	for _, hk := range []struct {
		name string
		m    map[string]string
	}{{"request_headers", o.RequestHeaders}, {"response_headers", o.ResponseHeaders}} {
		for _, name := range sortedKeys(hk.m) {
			if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n:") {
				c.err(CodeInvalidValue, op(hk.name, name), "%q is not a valid header name", name)
			}
		}
	}
	opts := routeOptions(o)
	if err := opts.Validate(); err != nil {
		c.err(CodeInvalidValue, op(), "options: %s", err)
	}
}

// routeOptions converts a domain's options table to the platform's options.
// Headers are sorted by name so equal tables convert to equal values.
func routeOptions(o *RouteOptions) agentprotocol.RouteOptions {
	if o == nil {
		return agentprotocol.RouteOptions{}
	}
	out := agentprotocol.RouteOptions{
		Compress: derefBool(o.Compress), Noindex: derefBool(o.Noindex), SecurityHeaders: derefStr(o.SecurityHeaders),
		BasicAuth: derefBool(o.BasicAuth), LargeUploads: derefBool(o.LargeUploads),
	}
	if o.ForceHTTPS != nil {
		v := *o.ForceHTTPS
		out.ForceHTTPS = &v
	}
	out.RequestHeaders = headers(o.RequestHeaders)
	out.ResponseHeaders = headers(o.ResponseHeaders)
	if cs := o.CORS; cs != nil {
		out.CORS = &agentprotocol.CORS{
			Origins: cs.Origins, Methods: cs.Methods, Headers: cs.Headers, ExposeHeaders: cs.ExposeHeaders,
			Credentials: derefBool(cs.Credentials), MaxAgeSeconds: derefInt(cs.MaxAgeSeconds),
		}
	}
	for _, r := range o.Redirects {
		out.Redirects = append(out.Redirects, agentprotocol.Redirect{Regex: derefStr(r.Regex), Replacement: derefStr(r.Replacement), Permanent: derefBool(r.Permanent)})
	}
	if rl := o.RateLimit; rl != nil {
		out.RateLimit = &agentprotocol.RateLimit{Average: derefInt(rl.Average), PeriodSeconds: derefInt(rl.PeriodSeconds), Burst: derefInt(rl.Burst), TrustedProxyDepth: derefInt(rl.TrustedProxyDepth)}
	}
	if a := o.Access; a != nil {
		out.Access = &agentprotocol.Access{AllowCIDRs: a.AllowCIDRs, DenyCIDRs: a.DenyCIDRs, TrustedProxyDepth: derefInt(a.TrustedProxyDepth), RejectStatus: derefInt(a.RejectStatus)}
	}
	return out
}

func headers(m map[string]string) []agentprotocol.Header {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]agentprotocol.Header, len(names))
	for i, n := range names {
		out[i] = agentprotocol.Header{Name: n, Value: m[n]}
	}
	return out
}
