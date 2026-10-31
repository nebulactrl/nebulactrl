package configsync

import (
	"errors"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

// useCaseError turns an error of a platform validator into a diagnostic: codes with a
// dedicated file code map to it, every other apierr code is passed through as
// invalid_value with the use case's code in the hint.
func (p *planned) useCaseError(err error, path string, pos projectconfig.Pos) {
	code, msg, hint := "invalid_value", err.Error(), ""
	var ae *apierr.Error
	if errors.As(err, &ae) {
		msg = ae.Message
		switch ae.Code {
		case services.CodeRequiresUnrestricted:
			code = "docker_unrestricted"
		case "license_not_accepted":
			code = "license_not_accepted"
		case "upstream_denied", "invalid_upstream":
			code = ae.Code
		case "domain_conflict":
			code = "hostname_in_use"
		default:
			hint = "cause: " + ae.Code
		}
	}
	d := Diagnostic{Severity: projectconfig.SeverityError, Code: code, Path: path, Message: msg, Hint: hint}
	p.diags = append(p.diags, d.WithPos(pos))
}

// field returns the planned field with id of the service.
func (sp *svcPlan) field(id string) *fieldPlan {
	for _, f := range sp.fields {
		if f.fv.ID == id && !f.skipped {
			return f
		}
	}
	return nil
}

// candidateConfig is the config the file leads to for a service: the live config (or the defaults of a
// new service) with every config.* field of the file set, defaulted. It is computed again at apply time,
// when the services the file creates exist and route upstreams can name their real ids.
func (p *planned) candidateConfig(sp *svcPlan) (services.Config, bool) {
	pc := p.pc
	pc.rs = sp.rs
	cfg := baseConfig(sp.ls)
	if sp.switchTo != "" {
		cfg = cfg.ForSource(sp.switchTo) // the fields of the old source do not carry over
	}
	for _, f := range sp.fields {
		if f.skipped || !strings.HasPrefix(f.fv.ID, "config.") {
			continue
		}
		cf, ok := lookupCfgField(f.fv.ID)
		if !ok {
			continue
		}
		if err := cf.set(pc, &cfg, f.fv.Value); err != nil {
			var re *refError
			if errors.As(err, &re) {
				p.errorf(re.Code, f.fv.Path, f.fv.Pos, "%s", re.Msg)
			} else {
				p.errorf("invalid_value", f.fv.Path, f.fv.Pos, "%v", err)
			}
			sp.blocked = true
			return cfg, false
		}
	}
	if cfg.GitURL == "" && pc.cloneURL != nil {
		if f := sp.field("git.link"); f != nil {
			if l, ok := f.fv.Value.(projectconfig.GitLink); ok {
				if c, res := pc.refs().connection(l.ConnectionName); res == refOK {
					url, _, err := pc.cloneURL(pc.ctx, c.ID, l.Repo)
					if err != nil {
						p.errorf("invalid_value", f.fv.Path, f.fv.Pos, "the clone URL of %s could not be read: %v", l.Repo, err)
						sp.blocked = true
						return cfg, false
					}
					cfg.GitURL = url
				}
			}
		}
	}
	return cfg.WithDefaults(), true
}

// checkService runs the checks the platform would run when the changes are written, against
// the merged value, so the plan is blocked instead of the apply failing half way.
func (s *Service) checkService(p *planned, sp *svcPlan) {
	pc, rs := p.pc, sp.rs
	svcPos := p.res.Pos(rs.Path)

	cfg, ok := p.candidateConfig(sp)
	if !ok {
		return
	}
	sourceType := sourceTypeOf(sp)
	if sp.create && rs.Kind == projectconfig.KindRoute && cfg.Route == nil {
		p.errorf("missing_required", rs.Path+".route", svcPos, "a route service needs a route")
	}
	if err := cfg.Validate(sourceType); err != nil {
		p.useCaseError(services.ConfigError(err), rs.Path, svcPos)
	}
	sp.cfg = cfg

	if sp.server != nil {
		if err := services.CheckDockerPlacement(cfg, sp.server.Unrestricted); err != nil {
			path := rs.Path + ".docker"
			pos := svcPos
			for _, f := range sp.fields {
				if strings.HasPrefix(f.fv.ID, "config.docker.") {
					path, pos = f.fv.Path, f.fv.Pos
					break
				}
			}
			p.useCaseError(err, path, pos)
		}
	}
	if rs.Kind == projectconfig.KindStack && cfg.Stack != nil {
		if f := sp.field("config.stack.compose"); f != nil {
			proj, err := services.CheckStackCompose(pc.ctx, pc.q, cfg.Stack.Compose, sp.server != nil && sp.server.Unrestricted)
			if err != nil {
				p.useCaseError(err, f.fv.Path, f.fv.Pos)
			} else if f.changed && composeNeedsUnrestricted(proj) {
				// An unrestricted server lifts the compose allow-list (privileged,
				// pid: host, devices, NET_ADMIN, ...): a person applies such a change.
				p.infof("auto_apply_privileged", f.fv.Path, f.fv.Pos, "this compose file uses host-level options that only an unrestricted server accepts; an automatic apply refuses it")
				p.privileged = true
			}
		}
	}

	// Privileged options: an auto-apply refuses them.
	for _, f := range sp.fields {
		if f.changed && dockerFieldPrivileged(f) && dockerPrivileged(cfg) {
			p.infof("auto_apply_privileged", f.fv.Path, f.fv.Pos, "this change touches host-level container options; an automatic apply refuses it")
			p.privileged = true
			break
		}
	}

	switch rs.Kind {
	case projectconfig.KindDatabase:
		p.checkDatabase(sp)
	case projectconfig.KindRoute:
		p.checkRoute(s, sp)
	}

	// Stateless scaling and egress.
	if f := sp.field("scale.replicas"); f != nil {
		if n, _ := asInt(f.fv.Value); n > 1 && s.hasVolumes(sp) {
			p.errorf("replicas_require_stateless", f.fv.Path, f.fv.Pos, "a service with volumes cannot run more than one replica")
		}
	}
	for _, f := range sp.fields {
		if f.skipped || !strings.HasPrefix(f.fv.ID, "processes.") || !strings.HasSuffix(f.fv.ID, ".scale.replicas") {
			continue
		}
		if n, _ := asInt(f.fv.Value); n > 1 && s.hasVolumes(sp) {
			p.errorf("replicas_require_stateless", f.fv.Path, f.fv.Pos, "a service with volumes cannot run more than one replica of a process")
		}
	}
	if cfg.BlockEgress {
		for _, f := range sp.fields {
			if strings.HasPrefix(f.fv.ID, "networks.") {
				p.errorf("egress_blocked", f.fv.Path, f.fv.Pos, "a service that blocks outbound traffic cannot join shared networks")
			}
		}
	}
	for _, f := range sp.fields {
		if f.skipped {
			continue
		}
		switch {
		case strings.HasPrefix(f.fv.ID, "networks."):
			name := strings.TrimPrefix(f.fv.ID, "networks.")
			if n, res := pc.refs().network(name); res == refOK && sp.server != nil && n.ServerID != sp.server.ID {
				p.errorf("invalid_value", f.fv.Path, f.fv.Pos, "the network %q lives on another server than %q", name, rs.Slug)
			}
		case strings.HasPrefix(f.fv.ID, "domains.") && !strings.HasSuffix(f.fv.ID, ".cloudflare"):
			p.checkDomain(sp, f)
		case strings.HasPrefix(f.fv.ID, "variables.") && strings.HasSuffix(f.fv.ID, secretSuffix):
			p.checkSecretDecl(sp, f)
		case strings.HasPrefix(f.fv.ID, "variables."):
			p.checkReferences(f)
		}
	}
}

// composeNeedsUnrestricted reports whether the compose project uses anything
// the safe allow-list refuses, whatever server it is planned for.
func composeNeedsUnrestricted(proj *composestack.Project) bool {
	return len(composestack.Restricted(composestack.Check(proj, composestack.Policy{Unrestricted: false}))) > 0
}

func sourceTypeOf(sp *svcPlan) string {
	if sp.switchTo != "" {
		return sp.switchTo
	}
	if sp.ls != nil {
		return sp.ls.Row.SourceType
	}
	switch sp.rs.Kind {
	case projectconfig.KindGit:
		return services.SourceGit
	case projectconfig.KindRoute:
		return services.SourceExternal
	case projectconfig.KindStack:
		return services.SourceCompose
	}
	return services.SourceImage
}

// dockerPrivileged reports options that give a container host-level reach.
func dockerPrivileged(c services.Config) bool {
	if c.Docker == nil {
		return false
	}
	if c.Docker.NeedsUnrestricted() {
		return true
	}
	for _, cap := range c.Docker.CapAdd {
		for _, u := range agentprotocol.UnrestrictedCapabilities {
			if agentprotocol.NormalizeCapability(cap) == u {
				return true
			}
		}
	}
	return false
}

func (s *Service) hasVolumes(sp *svcPlan) bool {
	if sp.ls != nil && len(sp.ls.Volumes) > 0 {
		return true
	}
	for id := range sp.rs.Fields {
		if strings.HasPrefix(id, "volumes.") {
			return true
		}
	}
	return false
}

// checkDatabase refuses a change to what a database's creation fixed, and a missing licence acknowledgement.
func (p *planned) checkDatabase(sp *svcPlan) {
	rs := sp.rs
	if sp.create {
		d := rs.Def.Database
		if d != nil && d.Engine != nil {
			if eng := engines.Lookup(*d.Engine); eng != nil && eng.LicenseClass == engines.LicenseSourceAvailable && (d.AcceptLicense == nil || !*d.AcceptLicense) {
				p.errorf("license_not_accepted", rs.Path+".database.accept_license", p.res.Pos(rs.Path+".database"), "%s", eng.LicenseNote)
			}
		}
		return
	}
	for _, f := range sp.fields {
		if strings.HasPrefix(f.fv.ID, "config.database.") && f.changed && !f.skipped {
			p.errorf("immutable_field", f.fv.Path, f.fv.Pos, "the database's %s cannot change through the file; use the database's upgrade flow in the dashboard", strings.TrimPrefix(f.fv.ID, "config.database."))
		}
	}
}

// checkRoute refuses a protocol change and a route without a server.
func (p *planned) checkRoute(s *Service, sp *svcPlan) {
	rs := sp.rs
	p.checkUpstreamPolicy(s, sp)
	if sp.server == nil && (sp.ls == nil || sp.ls.Row.ServerID == nil) {
		p.errorf("missing_required", rs.Path+".server", p.res.Pos(rs.Path), "a route service needs a server")
	}
	if f := sp.field("config.route.protocol"); f != nil && !sp.create && f.changed {
		p.errorf("immutable_field", f.fv.Path, f.fv.Pos, "the protocol of a route cannot change")
	}
}

// checkDomain finds hostnames held by another service and a basic-auth request without users.
func (p *planned) checkDomain(sp *svcPlan, f *fieldPlan) {
	host, path := splitDomainID(f.fv.ID)
	pc := p.pc
	if _, live := sp.liveDomain(host, path); !live {
		rows, err := pc.q.ListDomainsByHostnames(pc.ctx, []string{strings.ToLower(host)})
		if err == nil {
			for _, r := range rows {
				if r.PathPrefix == path && (sp.ls == nil || r.ServiceID != sp.ls.Row.ID) {
					p.errorf("hostname_in_use", f.fv.Path, f.fv.Pos, "%s%s is already used by service %q", host, path, r.ServiceSlug)
				}
			}
		}
	}
	if r, ok := f.fv.Value.(projectconfig.DomainRule); ok && r.Options != nil && r.Options.BasicAuth {
		users := int64(0)
		if d, live := sp.liveDomain(host, path); live {
			users, _ = pc.q.CountDomainAuthUsers(pc.ctx, d.ID)
		}
		if users == 0 {
			p.warnf("basic_auth_no_users", f.fv.Path+".options.basic_auth", f.fv.Pos, "basic authentication is on but the domain has no users yet; add users in the dashboard")
		}
	}
	p.checkDomainProcess(sp, f)
}

// checkDomainProcess applies the platform's rule for the process a domain routes to against the
// config the file leads to, and refuses a change of the process of a domain that exists (the
// platform fixes it when the domain is created).
func (p *planned) checkDomainProcess(sp *svcPlan, f *fieldPlan) {
	r, ok := f.fv.Value.(projectconfig.DomainRule)
	if !ok || sp.rs.Kind == projectconfig.KindStack || sp.rs.Kind == projectconfig.KindRoute {
		return
	}
	cfg := sp.cfg
	if r.Process == nil && !cfg.HasProcesses() {
		return
	}
	process, target := "", 0
	if r.Process != nil {
		process = *r.Process
	}
	if r.TargetPort != nil {
		target = *r.TargetPort
	}
	_, stored, err := cfg.ResolveDomainProcess(process, target)
	if err != nil {
		if f.changed {
			p.useCaseError(err, f.fv.Path, f.fv.Pos)
		}
		return
	}
	host, path := splitDomainID(f.fv.ID)
	if d, live := sp.liveDomain(host, path); live && r.Process != nil && d.Component != stored {
		p.errorf("immutable_field", f.fv.Path, f.fv.Pos, "the process of a domain cannot change; remove the domain and add it again")
	}
}

func (sp *svcPlan) liveDomain(host, path string) (db.Domain, bool) {
	if sp.ls == nil {
		return db.Domain{}, false
	}
	return sp.ls.findDomain(host, path)
}

// checkSecretDecl reports the declared secrets that need a value or a flag.
func (p *planned) checkSecretDecl(sp *svcPlan, f *fieldPlan) {
	key, _ := varKey(f.fv.ID)
	if sp.ls == nil {
		p.warnf("secret_missing_value", f.fv.Path, f.fv.Pos, "%s has no value yet; set it before deploying", key)
		return
	}
	lv, ok := sp.ls.Vars[key]
	switch {
	case !ok:
		p.warnf("secret_missing_value", f.fv.Path, f.fv.Pos, "%s has no value yet; set it before deploying", key)
	case !lv.Secret:
		p.warnf("secret_flag_differs", f.fv.Path, f.fv.Pos, "%s is stored as a plain variable; the apply marks it secret and keeps its value", key)
	case lv.Source == services.SourceSuggested:
		p.warnf("secret_missing_value", f.fv.Path, f.fv.Pos, "%s has no value yet; set it before deploying", key)
	}
}

// checkReferences turns the validator's warning about a reference to a service that is not in the file
// into the verdict that needs live state: an error unless the service exists in the environment.
func (p *planned) checkReferences(f *fieldPlan) {
	v, ok := f.fv.Value.(projectconfig.Variable)
	if !ok {
		return
	}
	t, err := varexpr.Parse(v.Value)
	if err != nil {
		return
	}
	for _, r := range t.Refs() {
		if r.NS == "" || services.IsReservedSlug(r.NS) || r.NS == "shared" || r.NS == "team" {
			continue
		}
		if _, inFile := p.res.Services[r.NS]; inFile {
			continue
		}
		if _, live := p.live.Services[r.NS]; live {
			continue
		}
		p.errorf("unknown_service_reference", f.fv.Path, f.fv.Pos, "the reference %s names %q, which is not a service of this environment", r.String(), r.NS)
	}
}

// dockerFieldPrivileged reports a docker option of the file that itself needs host-level reach: a
// field only an unrestricted server accepts, or a capability list with NET_ADMIN or SYS_ADMIN.
func dockerFieldPrivileged(f *fieldPlan) bool {
	name, ok := strings.CutPrefix(f.fv.ID, "config.docker.")
	if !ok || f.skipped {
		return false
	}
	if projectconfig.DockerFieldUnrestricted(name) {
		return true
	}
	if name == "capAdd" {
		caps, _ := asStrings(f.fv.Value)
		for _, c := range caps {
			for _, u := range agentprotocol.UnrestrictedCapabilities {
				if agentprotocol.NormalizeCapability(c) == u {
					return true
				}
			}
		}
	}
	return false
}

// checkUpstreamPolicy applies the server's upstream policy to the external upstreams of a route. Managed
// upstreams (a service of the environment) are checked when the route is written: a service the file
// creates has no id yet.
func (p *planned) checkUpstreamPolicy(s *Service, sp *svcPlan) {
	if s.Routes == nil || sp.server == nil || sp.cfg.Route == nil {
		return
	}
	rc := *sp.cfg.Route
	rc.Upstreams = nil
	for _, u := range sp.cfg.Route.Upstreams {
		if u.ServiceID == nil {
			rc.Upstreams = append(rc.Upstreams, u)
		}
	}
	if len(rc.Upstreams) == 0 {
		return
	}
	if err := s.Routes.CheckUpstreams(p.pc.ctx, p.pc.q, rc, p.env.Env.ID, *sp.server); err != nil {
		path, pos := sp.rs.Path+".route.upstreams", p.res.Pos(sp.rs.Path)
		if f := sp.field("config.route.upstreams"); f != nil {
			path, pos = f.fv.Path, f.fv.Pos
		}
		p.useCaseError(err, path, pos)
	}
}
