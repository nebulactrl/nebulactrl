package composites

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type setupDomainInput struct {
	Service    string `json:"service" jsonschema:"service selector: uuid, project/env/service, a hostname of the service, or a bare service slug"`
	Hostname   string `json:"hostname,omitempty" jsonschema:"bare hostname to attach, e.g. app.example.com (no scheme, path or port; *.example.com for a wildcard). Omit to generate <service>-<env>-<project>.<server base domain>"`
	TargetPort int    `json:"targetPort,omitempty" jsonschema:"container port to route to; must equal the HTTP port of the process the domain routes to (default: that port)"`
	Process    string `json:"process,omitempty" jsonschema:"services with several processes only: the process the domain routes to (a name from service_get processes). Omit it when only one process has an HTTP port"`
	Probe      bool   `json:"probe,omitempty" jsonschema:"also make one HTTP request to the hostname, but only when the service is deployed and the domain is already live (status active)"`
	Detail     string `json:"detail,omitempty" jsonschema:"concise (default) or full (adds routing and certificate settings of the domain)"`
}

// SetupDomainDomain is the domain as the control plane sees it.
type SetupDomainDomain struct {
	ID            string `json:"id"`
	Hostname      string `json:"hostname"`
	Created       bool   `json:"created" jsonschema:"false when the service already had this domain and it was only reported"`
	Generated     bool   `json:"generated"`
	TargetPort    int    `json:"targetPort"`
	Process       string `json:"process,omitempty" jsonschema:"the process of the service this domain routes to; empty for the default process and for services without processes"`
	Status        string `json:"status" jsonschema:"pending (staged or not yet routed) | active (live in the proxy) | error"`
	StatusMessage any    `json:"statusMessage" jsonschema:"message of the DNS check and the proxy about the domain, as an untrusted block; may carry the DNS record to create"`
	DNSOk         *bool  `json:"dnsOk" jsonschema:"result of the periodic DNS check; null until the first check ran"`
	DNSCheckedAt  string `json:"dnsCheckedAt"`
	Kind          string `json:"kind,omitempty"`
	TLSMode       string `json:"tlsMode,omitempty"`
	PathPrefix    string `json:"pathPrefix,omitempty"`
}

// SetupDomainCloudflare is the cached Cloudflare DNS state of the domain.
type SetupDomainCloudflare struct {
	Connected bool   `json:"connected" jsonschema:"true when the team has a Cloudflare connection that tracks this domain"`
	Zone      string `json:"zone,omitempty"`
	Manage    bool   `json:"manage" jsonschema:"read-only: true when the host is in a zone of the connected Cloudflare account, where NebulaCtrl always manages its DNS record"`
	Proxied   bool   `json:"proxied"`
	Status    string `json:"status,omitempty" jsonschema:"managed | present | conflict (a record NebulaCtrl did not create differs or blocks) | missing | wrong_target | not_in_zone"`
	Message   any    `json:"message,omitempty" jsonschema:"message of Cloudflare about the record, as an untrusted block"`
	Error     any    `json:"error,omitempty" jsonschema:"error of the Cloudflare API, as an untrusted block"`
}

// SetupDomainDNS tells what, if anything, a human must do in DNS.
type SetupDomainDNS struct {
	Record       string                `json:"record" jsonschema:"the exact record to create, e.g. 'A app.example.com → 203.0.113.7'; empty when the server IP is unknown"`
	ServerIP     string                `json:"serverIp"`
	ActionNeeded bool                  `json:"actionNeeded" jsonschema:"true when the owner must create or fix a DNS record"`
	Guidance     string                `json:"guidance"`
	Cloudflare   SetupDomainCloudflare `json:"cloudflare"`
}

// SetupDomainProxy is the state of the proxy on the service's server.
type SetupDomainProxy struct {
	Server    string `json:"server"`
	ServerID  string `json:"serverId"`
	Status    string `json:"status" jsonschema:"ok | starting | error | unknown (server state not readable or never reported)"`
	Available bool   `json:"available" jsonschema:"false when the proxy is in error: new domains are refused with proxy_unavailable"`
	Message   string `json:"message,omitempty"`
	Error     any    `json:"error,omitempty" jsonschema:"error the proxy reported, as an untrusted block"`
}

// SetupDomainStaging explains staged versus live.
type SetupDomainStaging struct {
	Staged            bool   `json:"staged"`
	Generation        int64  `json:"generation"`
	AppliedGeneration int64  `json:"appliedGeneration"`
	Deployed          bool   `json:"deployed" jsonschema:"the service has a successful deployment"`
	Live              bool   `json:"live" jsonschema:"the domain is active in the proxy"`
	Message           string `json:"message"`
}

// SetupDomainProbe is the optional HTTP probe.
type SetupDomainProbe struct {
	Requested bool        `json:"requested"`
	Done      bool        `json:"done"`
	Reason    string      `json:"reason,omitempty" jsonschema:"why no probe ran"`
	Result    ProbeResult `json:"result"`
}

// SetupDomainOutput is the result of setup_domain.
type SetupDomainOutput struct {
	Envelope
	Service  string             `json:"service" jsonschema:"project/env/service selector"`
	Domain   SetupDomainDomain  `json:"domain"`
	DNS      SetupDomainDNS     `json:"dns"`
	Proxy    SetupDomainProxy   `json:"proxy"`
	Staging  SetupDomainStaging `json:"staging"`
	Probe    SetupDomainProbe   `json:"probe"`
	Warnings []string           `json:"warnings"`
}

func (c *composites) setupDomain() mcpkit.Tool {
	return mcpkit.Tool{
		Name:  "setup_domain",
		Title: "Set up a domain for a service",
		Description: "Attach a public hostname to a service: pass `hostname`, or omit it to generate <service>-<env>-<project>.<base domain of the service's server>. " +
			"The domain is STAGED: it goes live at the next `apply_changes`. Returns the domain (status, dnsOk, dnsCheckedAt), the exact DNS record the owner must create " +
			"(`A <host> → <server IP>`; none when the host lives under the server's base domain or is a *.sslip.io name, or Cloudflare manages it), the proxy state of the " +
			"server, and what is still staged. Calling it again for a hostname the service already has reports its current state instead of failing; " +
			"`probe:true` then makes one SSRF-safe HTTP request to the host (only when the service is deployed and the domain is active). " +
			"Taken hostnames, a missing base domain, a stopped proxy and invalid hostnames come back as errors with a `data.fix` text. " +
			"It does NOT change DNS, deploy, remove domains (domain_delete) or edit routing rules/TLS (call_operation). Next: review_changes, then apply_changes.",
		Toolset: mcpkit.ToolsetCore,
		Scopes:  []string{policy.ScopeProjectsWrite},
		Hints:   mcpkit.Hints{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: true},
		Input:   setupDomainInput{},
		Output:  SetupDomainOutput{},
		Call: func(ctx context.Context, p *auth.Principal, raw json.RawMessage) (any, error) {
			in, err := mcpkit.Decode[setupDomainInput](raw)
			if err != nil {
				return nil, err
			}
			return c.runSetupDomain(ctx, p, in)
		},
	}
}

func (c *composites) runSetupDomain(ctx context.Context, p *auth.Principal, in setupDomainInput) (SetupDomainOutput, error) {
	detail, err := mcpkit.ParseDetail(in.Detail)
	if err != nil {
		return SetupDomainOutput{}, err
	}
	if strings.TrimSpace(in.Service) == "" {
		return SetupDomainOutput{}, invalid("service is required")
	}
	if in.TargetPort < 0 || in.TargetPort > 65535 {
		return SetupDomainOutput{}, invalid("targetPort must be between 1 and 65535")
	}
	svc, err := c.Resolver.Service(ctx, in.Service)
	if err != nil {
		return SetupDomainOutput{}, err
	}
	host := strings.ToLower(strings.TrimSpace(in.Hostname))
	process := strings.TrimSpace(in.Process)
	// A service with processes routes a domain to one process: its port is
	// the target, and a generated hostname names the process.
	if svc.Config.HasProcesses() || process != "" {
		_, stored, err := svc.Config.ResolveDomainProcess(process, in.TargetPort)
		if err != nil {
			return SetupDomainOutput{}, setupDomainError(err, host)
		}
		process = stored
	} else if host == "" && in.TargetPort != 0 && in.TargetPort != svc.Config.Port {
		return SetupDomainOutput{}, invalid("targetPort %d differs from the service port %d; a generated domain always routes to the service port", in.TargetPort, svc.Config.Port)
	}

	// ---- attach (staged) or find what is already there ----
	var dom services.DomainView
	created := false
	if d, ok := setupDomainExisting(svc, host, process); ok {
		dom = d
	} else {
		if host == "" {
			dom, err = c.Domains.Generate(ctx, svc.ID, process)
		} else {
			dom, err = c.Domains.CreateForComponent(ctx, svc.ID, host, in.TargetPort, process, domains.RuleInput{})
		}
		if err != nil {
			return SetupDomainOutput{}, setupDomainError(err, host)
		}
		created = true
	}

	// ---- independent reads ----
	var (
		view      = svc
		srv       *servers.Server
		serverErr bool
	)
	pr := c.newPartial(ctx, "setup_domain", 4)
	pr.run("service", func(ctx context.Context) error {
		v, err := c.Services.Get(ctx, svc.ID)
		if err != nil {
			return err
		}
		view = v
		for _, d := range v.Domains {
			if d.ID == dom.ID {
				dom = d
			}
		}
		return nil
	})
	switch {
	case svc.ServerID == nil:
		// Nothing to read: reported below.
	case !holds(p, policy.ScopeServersRead):
		serverErr = true
		pr.note("server", "forbidden", "server details (public IP, base domain, proxy state) need servers:read; DNS guidance comes from the domain's statusMessage")
	default:
		pr.run("server", func(ctx context.Context) error {
			s, err := c.Servers.Get(ctx, *svc.ServerID)
			if err != nil {
				serverErr = true
				return err
			}
			srv = &s
			return nil
		})
	}
	out := SetupDomainOutput{Warnings: []string{}}
	out.Errors = pr.wait()
	if view.ServerID == nil && svc.ServerID == nil {
		out.Warnings = append(out.Warnings, "The service is not assigned to a server yet: assign one (service_update) before this domain can be routed or its DNS checked.")
	}

	sel := svc.ID.String()
	if sc, err := c.scope(ctx, svc.ID); err == nil {
		sel = selectorOf(sc)
	}
	out.Service = sel
	out.Domain = setupDomainDomain(dom, created, detail.Full())
	out.Proxy = setupDomainProxy(view, srv, serverErr)
	out.DNS = setupDomainDNS(dom, view, srv)
	out.Staging = setupDomainStaging(view, dom)
	out.Warnings = append(out.Warnings, setupDomainWarnings(out, srv)...)
	out.Probe = c.setupDomainProbe(ctx, in.Probe, view, dom, srv)
	out.Summary = setupDomainSummary(out, created)
	out.NextTools = setupDomainNext(out)
	return out, nil
}

// setupDomainExisting finds a domain the service already has: the hostname when
// given, else its generated one for the process (stored "" = the default process).
func setupDomainExisting(svc services.ServiceView, host, process string) (services.DomainView, bool) {
	for _, d := range svc.Domains {
		if host != "" && strings.EqualFold(d.Hostname, host) {
			return d, true
		}
		if host == "" && d.Generated && d.Component == "" && d.Process == process {
			return d, true
		}
	}
	return services.DomainView{}, false
}

// setupDomainFixes are the concrete remedies for the structured domain errors.
var setupDomainFixes = map[string]string{
	"domain_conflict":     "That hostname is already attached to a service (the owner is not shown). Choose another hostname, or omit `hostname` to generate one. If you own it, remove it from the other service first (domain_delete) and retry.",
	"no_base_domain":      "The service's server has neither a base domain nor a public IP, so no hostname can be generated. Set the server's base domain (or its public IP, which gives <ip>.sslip.io names) in the server settings, or pass an explicit `hostname`.",
	"proxy_unavailable":   "The proxy (Traefik) on the service's server is not running, so new domains are refused. Run diagnose_server for that server, restart the proxy or fix the reported error, then retry.",
	"invalid_hostname":    "Pass a bare lowercase hostname with at least two labels and a letters-only top level domain, e.g. app.example.com: no scheme (https://), path, port or IP address. A wildcard looks like *.example.com.",
	"no_server":           "The service has no server yet. Assign one (service_update with a server id) and retry.",
	"no_http_port":        "The service has no HTTP port. Set one first (service_update port) and retry.",
	"invalid_target_port": "targetPort must equal the HTTP port of the process the domain routes to; omit targetPort to use it.",
	"process_required":    "Several processes of the service have an HTTP port: pass `process` with the name of the one this domain routes to (service_get lists them under processes).",
	"unknown_process":     "The service has no process of that name: use a name from service_get processes.",
	"process_has_no_port": "That process has no HTTP port. Give it one (stage_changes config.processes, port) and retry, or route a process that has one.",
	"invalid_process":     "The service has a single process: omit `process`.",
	"no_http_route":       "Only http route services take domains.",
	"component_required":  "Compose stacks route per component, which setup_domain does not cover: use find_operation (query \"create domain\") and call_operation with the component and targetPort.",
	"unknown_component":   "Compose stacks route per component, which setup_domain does not cover: use find_operation (query \"create domain\") and call_operation with the component and targetPort.",
}

// setupDomainError returns the structured domain errors as tool errors with a
// `data.fix`; any other error is returned unchanged.
func setupDomainError(err error, host string) error {
	ae, ok := apierr.As(err)
	if !ok || ae.Status >= 500 {
		return err
	}
	fix, ok := setupDomainFixes[ae.Code]
	if !ok {
		return err
	}
	msg := ae.Message
	data := map[string]any{"fix": fix}
	if ae.Code == "proxy_unavailable" {
		// The message carries the error the proxy reported on the host.
		msg = "the proxy on the service's server is not running"
		data["detail"] = mcpkit.WrapText("proxy.error", ae.Message)
	}
	e := mcpkit.Errf(ae.Code, "%s", msg)
	if host != "" {
		data["hostname"] = host
	}
	e.Data = data
	return e
}

func setupDomainDomain(d services.DomainView, created, full bool) SetupDomainDomain {
	out := SetupDomainDomain{
		ID: d.ID.String(), Hostname: d.Hostname, Created: created, Generated: d.Generated, TargetPort: d.TargetPort, Process: d.Process,
		Status: d.Status, StatusMessage: untrustedText("domain.message", clip(d.StatusMessage, 400)), DNSOk: d.DNSOk, DNSCheckedAt: ptrTime(d.DNSCheckedAt),
	}
	if full {
		out.Kind, out.TLSMode, out.PathPrefix = d.Kind, d.TLSMode, d.PathPrefix
	}
	return out
}

func setupDomainProxy(view services.ServiceView, srv *servers.Server, unreadable bool) SetupDomainProxy {
	out := SetupDomainProxy{Status: "unknown", Available: true}
	if view.Server != nil {
		out.Server, out.ServerID = view.Server.Name, view.Server.ID.String()
	}
	if srv == nil {
		if unreadable {
			out.Message = "proxy state not readable (needs servers:read); a stopped proxy would refuse new domains with proxy_unavailable"
		}
		return out
	}
	out.Server, out.ServerID = srv.Name, srv.ID.String()
	switch srv.Proxy.Status {
	case "":
		out.Message = "the proxy has not reported yet"
	case "error":
		out.Status, out.Available = "error", false
		out.Message = "the proxy is not running"
		out.Error = untrustedText("proxy.error", clip(srv.Proxy.Error, 300))
	default:
		out.Status = srv.Proxy.Status
	}
	if srv.Status != "online" {
		out.Message = strings.TrimSpace(out.Message + " The server is " + srv.Status + ", so nothing is served until it is back.")
	}
	return out
}

// setupDomainUnderBase reports whether host is the base domain or below it, and
// whether the base is a self-resolving sslip.io name.
func setupDomainUnderBase(host string, srv *servers.Server) (under bool, base string) {
	host = strings.TrimPrefix(host, "*.")
	if strings.HasSuffix(host, ".sslip.io") {
		return true, "sslip.io"
	}
	if srv == nil || srv.BaseDomain == nil {
		return false, ""
	}
	base = strings.Trim(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(*srv.BaseDomain)), "*."), ".")
	if base == "" {
		return false, ""
	}
	return host == base || strings.HasSuffix(host, "."+base), base
}

func setupDomainDNS(d services.DomainView, view services.ServiceView, srv *servers.Server) SetupDomainDNS {
	out := SetupDomainDNS{}
	if cf := d.DNS; cf != nil {
		out.Cloudflare = SetupDomainCloudflare{
			Connected: true, Zone: cf.ZoneName, Manage: cf.Manage, Proxied: cf.Proxied, Status: cf.Status,
			Message: untrustedText("cloudflare.message", clip(cf.Message, 300)), Error: untrustedText("cloudflare.error", clip(cf.Error, 300)),
		}
	}
	if srv != nil && srv.PublicIP != nil {
		out.ServerIP = strings.TrimSpace(*srv.PublicIP)
	}
	if out.ServerIP != "" {
		out.Record = fmt.Sprintf("A %s → %s", d.Hostname, out.ServerIP)
	}
	ok := d.DNSOk != nil && *d.DNSOk
	under, base := setupDomainUnderBase(d.Hostname, srv)
	cf := out.Cloudflare
	switch {
	case ok:
		out.Guidance = "DNS already resolves to the server; no action needed."
	case under && base == "sslip.io":
		out.Guidance = d.Hostname + " is an sslip.io name: it resolves to the server's IP by itself; no DNS action is needed."
	case under:
		out.Guidance = fmt.Sprintf("%s lives under the server's base domain %s, whose wildcard record already points at the server; no DNS action is needed. If dnsOk stays false, check that *.%s resolves to the server.", d.Hostname, base, base)
	case cf.Connected && (cf.Status == "managed" || cf.Status == "present"):
		out.Guidance = "Cloudflare DNS for the zone " + cf.Zone + " is managed by NebulaCtrl (" + cf.Status + "); no manual DNS action is needed."
	case cf.Connected && cf.Status == "conflict":
		out.ActionNeeded = true
		out.Guidance = "Cloudflare already has a record for this host that NebulaCtrl did not create and that differs from what the server needs, so NebulaCtrl leaves it alone. " +
			"Replacing it is the owner's explicit decision: call_operation fixDomainDns with domainId " + d.ID.String() + " and {\"adopt\": true} replaces the existing records at the name with NebulaCtrl's own."
		if out.Record != "" {
			out.Guidance += " The record needed is: " + out.Record + "."
		}
	case out.Record != "":
		out.ActionNeeded = true
		out.Guidance = "Create this DNS record at your DNS provider: " + out.Record + ". Certificates are issued once it resolves."
		if cf.Connected {
			switch cf.Status {
			case "wrong_target":
				out.Guidance += " The Cloudflare record for this host points somewhere else: NebulaCtrl manages DNS in this zone, so call_operation fixDomainDns with domainId " + d.ID.String() + " corrects it."
			case "not_in_zone":
				out.Guidance += " The host is in none of the connected Cloudflare zones, so NebulaCtrl does not manage it."
			case "missing":
				out.Guidance += " Cloudflare has no record for it yet: NebulaCtrl manages DNS in this zone, so call_operation fixDomainDns with domainId " + d.ID.String() + " creates it."
			}
		}
	case view.ServerID != nil:
		out.ActionNeeded = true
		out.Guidance = "Point an A record for " + d.Hostname + " to the server's public IP (not readable without servers:read, or not set on the server)."
		if d.StatusMessage != "" {
			out.Guidance += " The DNS check's own message, which may name the record, is in domain.statusMessage."
		}
	default:
		out.ActionNeeded = true
		out.Guidance = "Assign the service to a server, then point an A record for " + d.Hostname + " to that server's public IP."
	}
	return out
}

func setupDomainStaging(view services.ServiceView, d services.DomainView) SetupDomainStaging {
	s := SetupDomainStaging{
		Staged: view.Generation > view.AppliedGeneration, Generation: view.Generation, AppliedGeneration: view.AppliedGeneration,
		Deployed: view.CurrentDeploymentID != nil, Live: d.Status == "active",
	}
	switch {
	case s.Live && !s.Staged:
		s.Message = "The domain is live."
	case s.Live:
		s.Message = fmt.Sprintf("The domain is live; other changes are staged (generation %d, applied %d).", s.Generation, s.AppliedGeneration)
	case !s.Deployed:
		s.Message = fmt.Sprintf("Staged only: the service has never been deployed. The domain takes effect on the next apply_changes (expectedGeneration %d).", s.Generation)
	default:
		s.Message = fmt.Sprintf("Staged only: the domain takes effect on the next apply_changes (expectedGeneration %d).", s.Generation)
	}
	return s
}

func setupDomainWarnings(o SetupDomainOutput, srv *servers.Server) []string {
	var w []string
	if !o.Proxy.Available {
		w = append(w, "proxy_unavailable: "+o.Proxy.Message)
	}
	if srv != nil && srv.Status != "online" {
		w = append(w, fmt.Sprintf("Server %s is %s: applying will fail or wait until it is online.", srv.Name, srv.Status))
	}
	if srv != nil && (srv.PublicIP == nil || strings.TrimSpace(*srv.PublicIP) == "") {
		w = append(w, "The server has no public IP set, so DNS cannot be checked; set it in the server settings.")
	}
	if o.Domain.DNSOk != nil && !*o.Domain.DNSOk {
		w = append(w, "The last DNS check failed: the hostname does not resolve to the server yet.")
	}
	return w
}

// setupDomainProbe runs the optional probe against the service's own hostname.
func (c *composites) setupDomainProbe(ctx context.Context, want bool, view services.ServiceView, d services.DomainView, srv *servers.Server) SetupDomainProbe {
	out := SetupDomainProbe{Requested: want}
	if !want {
		return out
	}
	switch {
	case view.CurrentDeploymentID == nil:
		out.Reason = "not probed: the service has no successful deployment yet; apply_changes first"
	case d.Status != "active":
		out.Reason = "not probed: the domain is " + d.Status + ", not live yet; apply_changes, then call again with probe:true"
	case strings.HasPrefix(d.Hostname, "*."):
		out.Reason = "not probed: a wildcard hostname has no single address to request"
	default:
		var ips []string
		if srv != nil && srv.PublicIP != nil {
			ips = append(ips, *srv.PublicIP)
		}
		out.Result = c.probe(ctx, ProbeTarget{Hostname: d.Hostname, HTTP: d.TLSMode == "none", Path: "/"}, serverAddrs(ips...))
		out.Done = true
	}
	return out
}

func setupDomainSummary(o SetupDomainOutput, created bool) string {
	var b strings.Builder
	verb := "Attached"
	switch {
	case !created:
		verb = "Service already has"
	case o.Domain.Generated:
		verb = "Generated"
	}
	fmt.Fprintf(&b, "%s %s for %s (status %s", verb, o.Domain.Hostname, o.Service, o.Domain.Status)
	if o.Domain.DNSOk != nil {
		fmt.Fprintf(&b, ", dnsOk %t", *o.Domain.DNSOk)
	}
	b.WriteString(").")
	if o.DNS.ActionNeeded {
		b.WriteString(" DNS action needed: " + o.DNS.Guidance)
	} else {
		b.WriteString(" No DNS action needed.")
	}
	if o.Staging.Live {
		b.WriteString(" Live.")
	} else {
		b.WriteString(" " + o.Staging.Message)
	}
	if o.Probe.Done {
		if o.Probe.Result.OK {
			fmt.Fprintf(&b, " Probe: HTTP %d in %d ms.", o.Probe.Result.Status, o.Probe.Result.LatencyMs)
		} else {
			fmt.Fprintf(&b, " Probe failed: %s.", clip(o.Probe.Result.Error+setupDomainProbeStatus(o.Probe.Result), 200))
		}
	}
	if len(o.Errors) > 0 {
		b.WriteString(" Some sections could not be read: see errors[].")
	}
	return b.String()
}

func setupDomainProbeStatus(r ProbeResult) string {
	if r.Status == 0 {
		return ""
	}
	return fmt.Sprintf("HTTP %d", r.Status)
}

func setupDomainNext(o SetupDomainOutput) []NextTool {
	var next []NextTool
	if o.Staging.Staged {
		next = append(next,
			NextTool{Tool: "review_changes", Args: map[string]any{"service": o.Service}, Why: "review what is staged before applying"},
			NextTool{Tool: "apply_changes", Args: map[string]any{"services": []any{map[string]any{"service": o.Service, "expectedGeneration": o.Staging.Generation}}},
				Why: "make the domain live (production environments ask a human first)"})
	}
	if !o.Proxy.Available && o.Proxy.ServerID != "" {
		next = append(next, NextTool{Tool: "diagnose_server", Args: map[string]any{"server": o.Proxy.ServerID}, Why: "the proxy is not running"})
	}
	if !o.Staging.Live || o.DNS.ActionNeeded {
		host := o.Domain.Hostname
		next = append(next, NextTool{Tool: "setup_domain", Args: map[string]any{"service": o.Service, "hostname": host, "probe": true},
			Why: "after apply_changes" + map[bool]string{true: " and the DNS record", false: ""}[o.DNS.ActionNeeded] + ": re-check status, dnsOk and probe the site"})
	}
	return next
}
