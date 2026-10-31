package configsync

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// domainOps handles domains.<host><path>: one fingerprint over the rule keys the
// file declares; keys it does not declare keep their live value.
type domainOps struct{}

// splitDomainID splits a field id into hostname and path prefix ("" or "/api").
func splitDomainID(id string) (host, path string) {
	rest := strings.TrimPrefix(id, "domains.")
	rest = strings.TrimSuffix(rest, ".cloudflare")
	host, p, found := strings.Cut(rest, "/")
	if found {
		path = "/" + p
	}
	return host, path
}

// findDomain returns the live domain of a (host, path) pair.
func (ls *liveService) findDomain(host, path string) (db.Domain, bool) {
	for _, d := range ls.Domains {
		if strings.EqualFold(d.Hostname, host) && d.PathPrefix == path {
			return d, true
		}
	}
	return db.Domain{}, false
}

// ruleParts lists the declared keys of a rule, in a fixed order.
func ruleParts(r projectconfig.DomainRule) []string {
	var p []string
	add := func(name string, declared bool) {
		if declared {
			p = append(p, name)
		}
	}
	add("targetPort", r.TargetPort != nil)
	add("component", r.Component != nil)
	add("process", r.Process != nil)
	add("stripPrefix", r.StripPrefix != nil)
	add("addPrefix", r.AddPrefix != nil)
	add("kind", r.Kind != nil)
	add("redirectTo", r.RedirectTo != nil)
	add("redirectCode", r.RedirectCode != nil)
	add("preservePath", r.PreservePath != nil)
	add("tls", r.TLS != nil)
	add("certificate", r.Certificate != nil)
	add("dnsResolver", r.DNSResolver != nil)
	add("wwwRedirect", r.WWWRedirect != nil)
	add("options", r.Options != nil)
	return p
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

// optionsText is the canonical text of an option set ("" when empty).
func optionsText(o any) string {
	raw, _ := json.Marshal(o)
	return services.NormalizeOptions(raw)
}

// liveRuleMap projects a live domain row onto the parts.
func liveRuleMap(pc *planCtx, ls *liveService, d db.Domain, parts []string) map[string]any {
	m := map[string]any{}
	for _, p := range parts {
		switch p {
		case "targetPort":
			m[p] = int(d.TargetPort)
		case "component":
			m[p] = d.Component
		case "process":
			m[p] = d.Component // an app's domain keeps its process in the component column
		case "stripPrefix":
			m[p] = d.StripPrefix
		case "addPrefix":
			m[p] = d.AddPrefix
		case "kind":
			m[p] = d.Kind
		case "redirectTo":
			if d.RedirectTo != nil {
				m[p] = normHost(*d.RedirectTo)
			} else {
				m[p] = ""
			}
		case "redirectCode":
			m[p] = int(d.RedirectCode)
		case "preservePath":
			m[p] = d.PreservePath
		case "tls":
			m[p] = d.TlsMode
		case "certificate":
			name := ""
			if d.CertificateID != nil {
				if c, ok := pc.refs().certificateByID(*d.CertificateID); ok {
					name = strings.ToLower(c.Name)
				}
			}
			m[p] = name
		case "dnsResolver":
			m[p] = d.DnsResolver
		case "wwwRedirect":
			_, has := services.FindWWWCompanion(ls.Domains, d)
			m[p] = has
		case "options":
			m[p] = optionsText(services.DomainOptions(d.Options))
		}
	}
	return m
}

func (domainOps) live(pc *planCtx, ls *liveService, id, proj string) (fieldView, bool) {
	host, path := splitDomainID(id)
	d, ok := ls.findDomain(host, path)
	if !ok {
		return fieldView{}, false
	}
	parts := splitParts(proj)
	return fieldView{FP: projectconfig.Fingerprint(liveRuleMap(pc, ls, d, parts)), Display: ruleDisplay(liveRuleMap(pc, ls, d, parts)), Proj: proj}, true
}

func splitParts(proj string) []string {
	if proj == "" {
		return nil
	}
	return strings.Split(proj, ",")
}

// ruleDisplay renders the declared parts of a rule for the plan: key=value pairs, sorted; option sets by their text.
func ruleDisplay(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(display(m[k]))
	}
	if b.Len() == 0 {
		return "(defaults)"
	}
	return b.String()
}

func (domainOps) desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	r, ok := fv.Value.(projectconfig.DomainRule)
	if !ok {
		return fieldView{}, refErr("wrong_type", "domain %s has an unexpected value", fv.ID)
	}
	if r.Certificate != nil && strings.TrimSpace(*r.Certificate) != "" {
		if err := pc.checkCertificate(ls, strings.TrimSpace(*r.Certificate)); err != nil {
			return fieldView{}, err
		}
	}
	parts := ruleParts(r)
	m := map[string]any{}
	for _, p := range parts {
		switch p {
		case "targetPort":
			m[p] = *r.TargetPort
		case "component":
			m[p] = *r.Component
		case "process":
			// The service's own process is stored as "", whatever name it carries.
			m[p] = *r.Process
			if *r.Process == defaultProcessNameOf(pc, ls) {
				m[p] = ""
			}
		case "stripPrefix":
			m[p] = *r.StripPrefix
		case "addPrefix":
			m[p] = *r.AddPrefix
		case "kind":
			m[p] = *r.Kind
		case "redirectTo":
			m[p] = normHost(*r.RedirectTo)
		case "redirectCode":
			m[p] = *r.RedirectCode
		case "preservePath":
			m[p] = *r.PreservePath
		case "tls":
			m[p] = *r.TLS
		case "certificate":
			m[p] = strings.ToLower(strings.TrimSpace(*r.Certificate))
		case "dnsResolver":
			m[p] = *r.DNSResolver
		case "wwwRedirect":
			m[p] = *r.WWWRedirect
		case "options":
			m[p] = optionsText(r.Options)
		}
	}
	return fieldView{FP: projectconfig.Fingerprint(m), Display: ruleDisplay(m), Proj: strings.Join(parts, ",")}, nil
}

// cloudflareOps handles domains.<host><path>.cloudflare (immediate). DNS itself is always managed
// in a connected zone, so the file only declares proxied and origin_lock. A key the file leaves out
// is not part of the plan: an omitted proxied never changes a live name (the server applies its
// default only to a name it starts managing).
type cloudflareOps struct{}

func cfParts(c projectconfig.CloudflareRule) []string {
	var p []string
	if c.Proxied != nil {
		p = append(p, "proxied")
	}
	if c.OriginLock != nil {
		p = append(p, "originLock")
	}
	return p
}

func (cloudflareOps) live(pc *planCtx, ls *liveService, id, proj string) (fieldView, bool) {
	host, path := splitDomainID(id)
	d, ok := ls.findDomain(host, path)
	if !ok {
		return fieldView{}, false
	}
	m := map[string]any{}
	for _, p := range splitParts(proj) {
		switch p {
		case "proxied":
			m[p] = d.CfProxied
		case "originLock":
			m[p] = d.CfOriginLock
		}
	}
	return fieldView{FP: projectconfig.Fingerprint(m), Display: ruleDisplay(m), Proj: proj}, true
}

func (cloudflareOps) desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	c, ok := fv.Value.(projectconfig.CloudflareRule)
	if !ok {
		return fieldView{}, refErr("wrong_type", "cloudflare settings %s have an unexpected value", fv.ID)
	}
	if !pc.refs().Cloudflare {
		return fieldView{}, refErr("cloudflare_not_connected", "this team has no Cloudflare connection; connect one under Team settings → Integrations")
	}
	m := map[string]any{}
	if c.Proxied != nil {
		m["proxied"] = *c.Proxied
	}
	if c.OriginLock != nil {
		m["originLock"] = *c.OriginLock
	}
	return fieldView{FP: projectconfig.Fingerprint(m), Display: ruleDisplay(m), Proj: strings.Join(cfParts(c), ",")}, nil
}

// serverIDOf is the server a service of the file runs on: the file's effective server, else the live one.
func (pc *planCtx) serverIDOf(ls *liveService) *uuid.UUID {
	if pc.rs != nil && pc.rs.ServerName != "" {
		if srv, res := pc.refs().server(pc.rs.ServerName); res == refOK {
			return &srv.ID
		}
		return nil
	}
	if ls != nil {
		return ls.Row.ServerID
	}
	return nil
}

// checkCertificate resolves an uploaded certificate name on the service's server.
func (pc *planCtx) checkCertificate(ls *liveService, name string) error {
	sid := pc.serverIDOf(ls)
	if sid == nil {
		return refErr("invalid_value", "a certificate needs the service to have a server")
	}
	switch _, res := pc.refs().certificate(name, *sid); res {
	case refNone:
		return refErr("unknown_certificate", "no certificate named %q on the service's server", name)
	case refAmbiguous:
		return refErr("ambiguous_reference", "several certificates are named %q", name)
	}
	return nil
}
