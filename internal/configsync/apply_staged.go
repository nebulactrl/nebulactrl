package configsync

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// phase1Field reports a field written inside the apply transaction: the staged kinds, plus the
// service name (a rename is one use case with the config).
func phase1Field(f *fieldPlan) bool {
	return f.fv.Applies == AppliesStaged || f.fv.ID == "name"
}

// changedWith returns the changed, computable fields of a service whose id has one of the prefixes.
func (sp *svcPlan) changedWith(prefixes ...string) []*fieldPlan {
	var out []*fieldPlan
	for _, f := range sp.fields {
		if f.skipped || !f.changed {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(f.fv.ID, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func (sp *svcPlan) removesWith(prefixes ...string) []*fieldPlan {
	var out []*fieldPlan
	for _, f := range sp.removes {
		for _, p := range prefixes {
			if strings.HasPrefix(f.fv.ID, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// applyService writes the staged part of one service.
func (a *applier) applyService(sp *svcPlan) error {
	slug := sp.slug
	pc := a.p.pc
	pc.rs = sp.rs
	pc.createdID = func(s string) (uuid.UUID, bool) {
		if a.created[s] {
			return a.rows[s].ID, true
		}
		return uuid.Nil, false
	}
	cfg, ok := a.p.candidateConfig(sp)
	if !ok {
		return a.fail(stepName("config", slug), fmt.Errorf("the config of %s could not be computed", slug))
	}
	sp.cfg = cfg
	if sp.create {
		row, err := a.createService(sp)
		if err != nil {
			return a.fail(stepName("service.create", slug), err)
		}
		a.rows[slug], a.created[slug], a.changed[slug] = row, true, true
		sc := services.ScopeOf(a.p.env, row)
		a.scopes[slug] = sc
		a.owned[row.ID] = map[string]string{}
		a.step(stepName("service.create", slug), StepApplied, "")
		a.out.Services = append(a.out.Services, AppliedService{Slug: slug, ServiceID: row.ID, Action: ActionCreate, Generation: row.Generation})
	} else {
		sc, err := a.scopeFor(slug)
		if err != nil {
			return a.fail(stepName("config", slug), err)
		}
		a.rows[slug] = sc.Service
		own := make(map[string]string, len(sp.ls.Owned))
		for k, v := range sp.ls.Owned {
			own[k] = v
		}
		a.owned[sc.Service.ID] = own
	}

	if err := a.applyConfig(sp); err != nil {
		return err
	}
	if err := a.applyVariables(sp); err != nil {
		return err
	}
	if err := a.applyDomains(sp); err != nil {
		return err
	}
	if err := a.applyProcessPrune(sp); err != nil {
		return err
	}
	if err := a.applyVolumes(sp); err != nil {
		return err
	}
	if err := a.applyNetworks(sp); err != nil {
		return err
	}
	if err := a.applyLinks(sp); err != nil {
		return err
	}
	id := a.rows[slug].ID
	sc, err := a.reload(slug, id)
	if err != nil {
		return err
	}
	if !sp.create {
		if sp.pl.Action == ActionUpdate || len(sp.removes) > 0 {
			a.out.Services = append(a.out.Services, AppliedService{Slug: slug, ServiceID: id, Action: ActionUpdate, Generation: sc.Service.Generation})
		} else {
			a.out.Services = append(a.out.Services, AppliedService{Slug: slug, ServiceID: id, Action: ActionUnchanged, Generation: sc.Service.Generation})
		}
	} else {
		for i := range a.out.Services {
			if a.out.Services[i].Slug == slug {
				a.out.Services[i].Generation = sc.Service.Generation
			}
		}
	}
	a.recordStagedOwnership(sp)
	a.planImmediate(sp)
	return nil
}

// createService creates the service of its kind with the config the file leads to.
func (a *applier) createService(sp *svcPlan) (db.Service, error) {
	rs, p := sp.rs, a.p
	name := sp.slug
	if f := rs.Fields["name"]; f.ID != "" {
		if n := strings.TrimSpace(asString(f.Value)); n != "" {
			name = n
		}
	}
	var serverID *uuid.UUID
	if sp.server != nil {
		serverID = &sp.server.ID
	}
	switch rs.Kind {
	case projectconfig.KindDatabase:
		if a.s.Databases == nil {
			return db.Service{}, fmt.Errorf("this server build cannot create managed databases")
		}
		d := rs.Def.Database
		in := databases.CreateInput{EnvironmentID: p.env.Env.ID, Name: name, Slug: sp.slug, ServerID: serverID}
		if d != nil {
			in.Engine, in.Version, in.Flavor = derefString(d.Engine), derefString(d.Version), derefString(d.Flavor)
			in.DatabaseName, in.Username = derefString(d.Name), derefString(d.Username)
			in.AcceptLicense = d.AcceptLicense != nil && *d.AcceptLicense
		}
		return a.s.Databases.CreateTx(a.ctx, a.tx, in)
	case projectconfig.KindRoute:
		if a.s.Routes == nil || serverID == nil || sp.cfg.Route == nil {
			return db.Service{}, fmt.Errorf("a route service needs a server and a route")
		}
		in := proxyroutes.CreateInput{Name: name, Slug: sp.slug, ServerID: *serverID, Protocol: sp.cfg.Route.Protocol, RouteFields: routeFieldsOf(*sp.cfg.Route, true)}
		row, after, err := a.s.Routes.CreateTx(a.ctx, a.tx, p.env, in)
		a.later(after)
		return row, err
	}
	kind, source := services.KindApp, services.SourceImage
	if !slices.ContainsFunc(sp.cfg.EffectiveProcesses(), func(p services.EffectiveProcess) bool { return p.Port > 0 }) {
		kind = services.KindWorker
	}
	switch rs.Kind {
	case projectconfig.KindGit:
		source = services.SourceGit
	case projectconfig.KindStack:
		kind, source = services.KindStack, services.SourceCompose
	}
	return a.s.Services.CreateInTx(a.ctx, a.tx, p.env, services.CreateInput{
		Name: name, Slug: sp.slug, Kind: kind, SourceType: source, Config: sp.cfg, ServerID: serverID,
	})
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// routeFieldsOf renders a route config as route use-case fields; all selects every field (create).
func routeFieldsOf(r services.RouteConfig, all bool) proxyroutes.RouteFields {
	ups := make([]proxyroutes.UpstreamInput, 0, len(r.Upstreams))
	for _, u := range r.Upstreams {
		ups = append(ups, proxyroutes.UpstreamInput{Address: u.Address, ServiceID: u.ServiceID, Port: u.Port})
	}
	f := proxyroutes.RouteFields{Upstreams: &ups}
	if all {
		f.Scheme, f.InsecureSkipVerify, f.ServerName = &r.Scheme, &r.InsecureSkipVerify, &r.ServerName
		f.PassHost, f.HealthPath, f.VerifySkip = r.PassHost, &r.HealthPath, &r.VerifySkip
		f.ListenPort, f.AllowCIDRs, f.DenyCIDRs = &r.ListenPort, &r.AllowCIDRs, &r.DenyCIDRs
		f.OpenToInternet, f.ProxyProtocol = &r.OpenToInternet, &r.ProxyProtocol
	}
	return f
}

// applyConfig writes name, server and the config of an existing service (and what a database creation
// did not already carry) with the owned keys only, on top of the config the row holds.
func (a *applier) applyConfig(sp *svcPlan) error {
	slug := sp.slug
	cfgChanged := sp.changedWith("config.")
	nameField := sp.field("name")
	serverField := sp.field("server")
	nameChanged := nameField != nil && nameField.changed
	serverChanged := serverField != nil && serverField.changed && !sp.create
	if sp.create && sp.rs.Kind != projectconfig.KindDatabase {
		return nil // the create call carried the whole config
	}
	if sp.switchTo != "" && !sp.create {
		if err := a.applySourceSwitch(sp); err != nil {
			return err
		}
	}
	if !nameChanged && !serverChanged && len(cfgChanged) == 0 {
		return nil
	}
	sc := a.scopes[slug]
	step := stepName("config", slug)
	pc := a.p.pc

	if sp.rs.Kind == projectconfig.KindRoute {
		return a.applyRouteUpdate(sp, sc, cfgChanged, nameField, nameChanged)
	}

	cfg := services.DecodeConfig(sc.Service.Config)
	blockEgress := false
	for _, f := range cfgChanged {
		if sp.rs.Kind == projectconfig.KindDatabase && strings.HasPrefix(f.fv.ID, "config.database.") {
			continue // fixed when the database was created
		}
		cf, ok := lookupCfgField(f.fv.ID)
		if !ok {
			continue
		}
		if err := cf.set(pc, &cfg, f.fv.Value); err != nil {
			return a.fail(step, err)
		}
		if f.fv.ID == "config.blockEgress" {
			blockEgress = true
		}
	}
	cfg = cfg.WithDefaults()
	in := services.UpdateInput{Config: &cfg}
	if blockEgress {
		in.BlockEgress = &cfg.BlockEgress
	}
	if nameChanged {
		n := strings.TrimSpace(asString(nameField.fv.Value))
		in.Name = &n
	}
	if serverChanged && sp.server != nil {
		in.ServerID = &sp.server.ID
	}
	row, after, err := a.s.Services.UpdateTx(a.ctx, a.tx, sc, sc.Service.Generation, in)
	a.later(after)
	if err != nil {
		return a.fail(step, err)
	}
	a.changed[slug] = true
	a.rows[slug] = row
	if _, err := a.reload(slug, row.ID); err != nil {
		return err
	}
	a.step(step, StepApplied, "")
	return nil
}

// applyProcessPrune drops the processes the file owned and no longer names. It runs after the
// domains, so a domain that routed to a dropped process is already gone.
func (a *applier) applyProcessPrune(sp *svcPlan) error {
	removes := sp.removesWith("config.processes.")
	if len(removes) == 0 {
		return nil
	}
	slug, step := sp.slug, stepName("processes", sp.slug)
	// Domain writes earlier in this apply bump the service's generation, so the
	// cached scope would fail the generation check.
	sc, err := a.reload(slug, a.rows[slug].ID)
	if err != nil {
		return a.fail(step, err)
	}
	cfg := services.DecodeConfig(sc.Service.Config)
	for _, f := range removes {
		dropProcess(&cfg, strings.TrimPrefix(f.fv.ID, "config.processes."))
	}
	cfg = cfg.WithDefaults()
	row, after, err := a.s.Services.UpdateTx(a.ctx, a.tx, sc, sc.Service.Generation, services.UpdateInput{Config: &cfg})
	a.later(after)
	if err != nil {
		return a.fail(step, err)
	}
	a.changed[slug] = true
	a.rows[slug] = row
	if _, err := a.reload(slug, row.ID); err != nil {
		return err
	}
	a.step(step, StepApplied, "")
	return nil
}

// applySourceSwitch moves an existing service to the source type the file declares, as a staged
// change: the config the file leads to replaces the old source's fields, the old credentials and
// repository link are cleared, and the next deployment ships the new source. The remaining config
// fields are applied on top by applyConfig.
func (a *applier) applySourceSwitch(sp *svcPlan) error {
	slug := sp.slug
	step := stepName("source", slug)
	sc, err := a.scopeFor(slug)
	if err != nil {
		return a.fail(step, err)
	}
	cfg := sp.cfg
	row, after, err := a.s.Services.StageSourceChangeTx(a.ctx, a.tx, sc, sc.Service.Generation, services.SourceChangeInput{SourceType: sp.switchTo, Config: &cfg})
	a.later(after)
	if err != nil {
		return a.fail(step, err)
	}
	a.changed[slug] = true
	a.rows[slug] = row
	if _, err := a.reload(slug, row.ID); err != nil {
		return err
	}
	a.step(step, StepApplied, "")
	return nil
}

func (a *applier) applyRouteUpdate(sp *svcPlan, sc services.Scope, cfgChanged []*fieldPlan, nameField *fieldPlan, nameChanged bool) error {
	slug, step := sp.slug, stepName("config", sp.slug)
	in := proxyroutes.UpdateInput{}
	if nameChanged {
		n := strings.TrimSpace(asString(nameField.fv.Value))
		in.Name = &n
	}
	if sp.cfg.Route != nil && len(cfgChanged) > 0 {
		r := *sp.cfg.Route
		for _, f := range cfgChanged {
			switch strings.TrimPrefix(f.fv.ID, "config.route.") {
			case "upstreams":
				in.RouteFields.Upstreams = routeFieldsOf(r, false).Upstreams
			case "scheme":
				in.Scheme = &r.Scheme
			case "insecureSkipVerify":
				in.InsecureSkipVerify = &r.InsecureSkipVerify
			case "serverName":
				in.ServerName = &r.ServerName
			case "passHost":
				in.PassHost = r.PassHost
			case "healthPath":
				in.HealthPath = &r.HealthPath
			case "verifySkip":
				in.VerifySkip = &r.VerifySkip
			case "listenPort":
				in.ListenPort = &r.ListenPort
			case "allowCidrs":
				in.AllowCIDRs = &r.AllowCIDRs
			case "denyCidrs":
				in.DenyCIDRs = &r.DenyCIDRs
			case "openToInternet":
				in.OpenToInternet = &r.OpenToInternet
			case "proxyProtocol":
				in.ProxyProtocol = &r.ProxyProtocol
			}
		}
	}
	row, after, err := a.s.Routes.UpdateTx(a.ctx, a.tx, sc, in)
	a.later(after)
	if err != nil {
		return a.fail(step, err)
	}
	a.changed[slug] = true
	if _, err := a.reload(slug, row.ID); err != nil {
		return err
	}
	a.step(step, StepApplied, "")
	return nil
}

// applyVariables writes the changed variables and secret declarations of a service in one call.
func (a *applier) applyVariables(sp *svcPlan) error {
	slug, step := sp.slug, stepName("variables", sp.slug)
	fields := sp.changedWith("variables.")
	removes := sp.removesWith("variables.")
	if len(fields) == 0 && len(removes) == 0 {
		return nil
	}
	sc := a.scopes[slug]
	byKey := map[string]*variables.VariableInput{}
	get := func(key string) *variables.VariableInput {
		in := byKey[key]
		if in == nil {
			in = &variables.VariableInput{Key: key}
			byKey[key] = in
		}
		return in
	}
	for _, f := range fields {
		key, isDecl := varKey(f.fv.ID)
		in := get(key)
		if isDecl {
			d, _ := f.fv.Value.(projectconfig.SecretDecl)
			t := true
			in.IsSecret = &t
			scope := normScope(d.Scope)
			in.Scope = &scope
			if !f.present && in.Value == nil {
				empty, suggested := "", services.SourceSuggested
				in.Value, in.Source = &empty, &suggested
			}
			continue
		}
		v, ok := f.fv.Value.(projectconfig.Variable)
		if !ok {
			continue
		}
		val, scope := v.Value, normScope(v.Scope)
		in.Value, in.Scope = &val, &scope
		if v.Comment != "" {
			c := v.Comment
			in.Comment = &c
		}
		if v.RotateEveryDays != nil {
			r := *v.RotateEveryDays
			in.RotateEveryDays = &r
		}
		if _, declared := sp.rs.Fields["variables."+key+secretSuffix]; declared {
			t := true
			in.IsSecret = &t
		}
		// A value written over a suggested placeholder is a manual value.
		in.Source = nil
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var inputs []variables.VariableInput
	for _, k := range keys {
		inputs = append(inputs, *byKey[k])
	}
	if len(inputs) > 0 {
		res, after, err := a.s.Variables.PutTx(a.ctx, a.tx, sc, inputs)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		if len(res.Changes) > 0 {
			a.changed[slug] = true
		}
	}
	for _, f := range removes {
		key, _ := varKey(f.fv.ID)
		_, _, after, err := a.s.Variables.DeleteTx(a.ctx, a.tx, sc, key)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	a.step(step, StepApplied, "")
	return nil
}

// ruleInput renders a declared rule as the domain use case's input (undeclared parts stay nil).
func ruleInput(pc *planCtx, serverID *uuid.UUID, r projectconfig.DomainRule, create bool, noUsers bool) (domains.RuleInput, error) {
	in := domains.RuleInput{
		StripPrefix: r.StripPrefix, AddPrefix: r.AddPrefix, Kind: r.Kind, RedirectTo: r.RedirectTo, RedirectCode: r.RedirectCode,
		PreservePath: r.PreservePath, TLSMode: r.TLS, DNSResolver: r.DNSResolver, WWWRedirect: r.WWWRedirect,
	}
	if create && r.PathPrefix != "" {
		p := r.PathPrefix
		in.PathPrefix = &p
	}
	if r.Certificate != nil {
		id := ""
		if name := strings.TrimSpace(*r.Certificate); name != "" {
			if serverID == nil {
				return in, refErr("invalid_value", "a certificate needs the service to have a server")
			}
			c, res := pc.refs().certificate(name, *serverID)
			switch res {
			case refNone:
				return in, refErr("unknown_certificate", "no certificate named %q on the service's server", name)
			case refAmbiguous:
				return in, refErr("ambiguous_reference", "several certificates are named %q", name)
			}
			id = c.ID.String()
		}
		in.CertificateID = &id
	}
	if r.Options != nil {
		o := *r.Options
		if noUsers {
			o.BasicAuth = false // users are never in the file; the plan warned
		}
		in.Options = &o
	}
	return in, nil
}

func (a *applier) applyDomains(sp *svcPlan) error {
	slug, step := sp.slug, stepName("domains", sp.slug)
	fields := make([]*fieldPlan, 0)
	for _, f := range sp.changedWith("domains.") {
		if !strings.HasSuffix(f.fv.ID, ".cloudflare") {
			fields = append(fields, f)
		}
	}
	var removes []*fieldPlan
	for _, f := range sp.removesWith("domains.") {
		if !strings.HasSuffix(f.fv.ID, ".cloudflare") {
			removes = append(removes, f)
		}
	}
	gen := sp.field("generate_domain")
	wantGen := gen != nil && gen.changed
	if len(fields) == 0 && len(removes) == 0 && !wantGen {
		return nil
	}
	sc := a.scopes[slug]
	var serverID *uuid.UUID
	if sp.server != nil {
		serverID = &sp.server.ID
	} else {
		serverID = sc.Service.ServerID
	}
	for _, f := range fields {
		r, ok := f.fv.Value.(projectconfig.DomainRule)
		if !ok {
			continue
		}
		host, path := splitDomainID(f.fv.ID)
		var existing db.Domain
		found := false
		if sp.ls != nil {
			existing, found = sp.ls.findDomain(host, path)
		}
		noUsers := false
		if r.Options != nil && r.Options.BasicAuth {
			n := int64(0)
			if found {
				n, _ = a.q.CountDomainAuthUsers(a.ctx, existing.ID)
			}
			noUsers = n == 0
		}
		in, err := ruleInput(a.p.pc, serverID, r, !found, noUsers)
		if err != nil {
			return a.fail(step, err)
		}
		if found {
			_, after, err := a.s.Domains.UpdateTx(a.ctx, a.tx, existing.ID, domains.UpdateInput{Rule: in})
			a.later(after)
			if err != nil {
				return a.fail(step, err)
			}
		} else {
			target, comp := 0, ""
			if r.TargetPort != nil {
				target = *r.TargetPort
			}
			if r.Component != nil {
				comp = *r.Component
			}
			if r.Process != nil {
				comp = *r.Process // an app's domain keeps its process in the component column
			}
			_, after, err := a.s.Domains.CreateTx(a.ctx, a.tx, sc, host, target, comp, in)
			a.later(after)
			if err != nil {
				return a.fail(step, err)
			}
		}
		a.changed[slug] = true
	}
	for _, f := range removes {
		host, path := splitDomainID(f.fv.ID)
		d, ok := sp.ls.findDomain(host, path)
		if !ok {
			continue
		}
		after, err := a.s.Domains.DeleteTx(a.ctx, a.tx, sc, d.ID)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	if wantGen {
		if want, _ := asBool(gen.fv.Value); want {
			_, after, err := a.s.Domains.GenerateTx(a.ctx, a.tx, sc, "")
			a.later(after)
			if err != nil {
				return a.fail(step, err)
			}
			a.changed[slug] = true
		}
	}
	a.step(step, StepApplied, "")
	return nil
}

func (a *applier) applyVolumes(sp *svcPlan) error {
	slug, step := sp.slug, stepName("volumes", sp.slug)
	fields, removes := sp.changedWith("volumes."), sp.removesWith("volumes.")
	if len(fields) == 0 && len(removes) == 0 {
		return nil
	}
	sc := a.scopes[slug]
	for _, f := range fields {
		v, ok := f.fv.Value.(projectconfig.Volume)
		if !ok {
			continue
		}
		exists := false
		if sp.ls != nil {
			for _, lv := range sp.ls.Volumes {
				exists = exists || lv.Name == v.Name
			}
		}
		if exists {
			if err := a.q.SetServiceVolumeMount(a.ctx, db.SetServiceVolumeMountParams{ServiceID: sc.Service.ID, Name: v.Name, Name_2: v.Name, MountPath: v.MountPath}); err != nil {
				return a.fail(step, err)
			}
			if _, err := a.q.BumpServiceGeneration(a.ctx, sc.Service.ID); err != nil {
				return a.fail(step, err)
			}
		} else if _, err := a.s.Services.AddVolumeTx(a.ctx, a.tx, sc, v.Name, v.MountPath); err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	for _, f := range removes {
		if err := a.s.Services.DeleteVolumeTx(a.ctx, a.tx, sc, strings.TrimPrefix(f.fv.ID, "volumes.")); err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	a.step(step, StepApplied, "")
	return nil
}

func (a *applier) applyNetworks(sp *svcPlan) error {
	slug, step := sp.slug, stepName("networks", sp.slug)
	fields, removes := sp.changedWith("networks."), sp.removesWith("networks.")
	if len(fields) == 0 && len(removes) == 0 {
		return nil
	}
	sc := a.scopes[slug]
	for _, f := range fields {
		n, res := a.p.pc.refs().network(strings.TrimPrefix(f.fv.ID, "networks."))
		if res != refOK {
			return a.fail(step, refErr("unknown_network", "no shared network named %q", strings.TrimPrefix(f.fv.ID, "networks.")))
		}
		_, after, err := a.s.Networks.AttachTx(a.ctx, a.tx, sc, n.ID)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	for _, f := range removes {
		n, res := a.p.pc.refs().network(strings.TrimPrefix(f.fv.ID, "networks."))
		if res != refOK {
			continue
		}
		after, err := a.s.Networks.DetachTx(a.ctx, a.tx, sc, n.ID)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	a.step(step, StepApplied, "")
	return nil
}

func (a *applier) applyLinks(sp *svcPlan) error {
	slug, step := sp.slug, stepName("links", sp.slug)
	fields, removes := sp.changedWith("links."), sp.removesWith("links.")
	if len(fields) == 0 && len(removes) == 0 {
		return nil
	}
	from := a.rows[slug].ID
	provider := func(id string) (uuid.UUID, bool) {
		to := strings.TrimPrefix(id, "links.")
		if a.created[to] {
			return a.rows[to].ID, true
		}
		if ls, ok := a.p.live.Services[to]; ok {
			return ls.Row.ID, true
		}
		return uuid.Nil, false
	}
	for _, f := range fields {
		to, ok := provider(f.fv.ID)
		if !ok {
			return a.fail(step, refErr("unknown_service_reference", "no service %q in this environment", strings.TrimPrefix(f.fv.ID, "links.")))
		}
		_, after, err := a.s.Services.CreateLinkTx(a.ctx, a.tx, a.p.env, from, to)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	for _, f := range removes {
		to, ok := provider(f.fv.ID)
		if !ok {
			continue
		}
		_, after, err := a.s.Services.DeleteLinkTx(a.ctx, a.tx, a.p.env, from, to)
		a.later(after)
		if err != nil {
			return a.fail(step, err)
		}
		a.changed[slug] = true
	}
	a.step(step, StepApplied, "")
	return nil
}

// applyShared writes the project and environment layers of shared variables.
func (a *applier) applyShared() error {
	p := a.p
	a.sharedOwned = make(map[string]string, len(p.live.OwnedShared))
	for k, v := range p.live.OwnedShared {
		a.sharedOwned[k] = v
	}
	for _, sh := range p.shared {
		step := "shared:" + sh.layer
		var inputs []variables.SharedVariableInput
		byKey := map[string]*variables.SharedVariableInput{}
		get := func(key string) *variables.SharedVariableInput {
			in := byKey[key]
			if in == nil {
				in = &variables.SharedVariableInput{Key: key}
				byKey[key] = in
			}
			return in
		}
		var keys []string
		for _, f := range sh.fields {
			if f.skipped || !f.changed {
				continue
			}
			key := strings.TrimPrefix(f.fv.ID, sh.layer+".")
			if strings.HasSuffix(key, secretSuffix) {
				key = strings.TrimSuffix(key, secretSuffix)
				in := get(key)
				t := true
				in.IsSecret = &t
				if !f.present && in.Value == nil {
					empty := ""
					in.Value = &empty
				}
			} else if v, ok := f.fv.Value.(projectconfig.Variable); ok {
				in := get(key)
				val := v.Value
				in.Value = &val
				if v.Comment != "" {
					c := v.Comment
					in.Comment = &c
				}
			}
		}
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// A key both written and declared secret is one input; declared names carry the flag.
			if _, declared := findField(sh.fields, sh.layer+"."+k+secretSuffix); declared {
				t := true
				byKey[k].IsSecret = &t
			}
			inputs = append(inputs, *byKey[k])
		}
		id := p.project.ID
		if sh.layer == layerEnvironment {
			id = p.env.Env.ID
		}
		kind := variables.LayerProject
		if sh.layer == layerEnvironment {
			kind = variables.LayerEnvironment
		}
		wrote := false
		if len(inputs) > 0 {
			_, after, err := a.s.Variables.PutSharedTx(a.ctx, a.tx, kind, id, inputs)
			a.later(after)
			if err != nil {
				return a.fail(step, err)
			}
			wrote = true
		}
		for _, f := range sh.removes {
			key := strings.TrimPrefix(f.fv.ID, sh.layer+".")
			_, _, after, err := a.s.Variables.DeleteSharedTx(a.ctx, a.tx, kind, id, key)
			a.later(after)
			if err != nil {
				return a.fail(step, err)
			}
			wrote = true
			delete(a.sharedOwned, f.fv.ID)
		}
		for _, f := range sh.fields {
			if f.skipped {
				continue
			}
			a.sharedOwned[f.fv.ID] = f.desired.FP
		}
		for _, id := range sh.released {
			delete(a.sharedOwned, id)
		}
		if wrote {
			a.step(step, StepApplied, "")
		}
	}
	return nil
}

func findField(fields []*fieldPlan, id string) (*fieldPlan, bool) {
	for _, f := range fields {
		if f.fv.ID == id && !f.skipped {
			return f, true
		}
	}
	return nil, false
}
