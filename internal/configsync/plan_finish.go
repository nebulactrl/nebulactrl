package configsync

import (
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// Shared layer names; also the prefix of the field ids recorded in environment_config_settings.owned_shared.
const (
	layerProject     = "project"
	layerEnvironment = "environment"
)

type sharedLayer struct {
	name    string
	vars    map[string]projectconfig.Variable
	secrets []string
	scope   string
	live    map[string]liveVar
	varPath func(key string) string
}

func (p *planned) sharedLayers() []sharedLayer {
	slug := p.env.Env.Slug
	return []sharedLayer{
		{name: layerProject, vars: p.res.Shared, secrets: p.res.SharedSecrets, scope: p.res.SharedSecretScope, live: p.live.SharedProject,
			varPath: func(k string) string { return "variables." + k }},
		{name: layerEnvironment, vars: p.res.EnvShared, secrets: p.res.EnvSharedSecrets, scope: p.res.EnvSharedSecretScope, live: p.live.SharedEnv,
			varPath: func(k string) string { return "environments." + slug + ".variables." + k }},
	}
}

// sharedDesired is the view of a shared variable the file writes.
func sharedDesired(v projectconfig.Variable, declared bool, live *liveVar) (fieldView, error) {
	tpl, err := varexpr.Parse(v.Value)
	if err != nil {
		return fieldView{}, refErr("invalid_template", "%v", err)
	}
	secret := templateSecret(tpl) || declared
	if live != nil {
		secret = secret || live.Secret
	}
	var parts []string
	if v.Comment != "" {
		parts = append(parts, "comment")
	}
	proj := strings.Join(parts, ",")
	display, masked := describeTemplate(v.Value)
	return fieldView{FP: varFP(v.Value, secret, "runtime", v.Comment, nil, proj), Display: display, Masked: masked, Proj: proj}, nil
}

// planShared plans the project and environment layers of shared variables.
func (p *planned) planShared() {
	for _, l := range p.sharedLayers() {
		sh := &sharedPlan{layer: l.name}
		declaredSecret := map[string]bool{}
		for _, k := range l.secrets {
			declaredSecret[k] = true
		}
		var keys []string
		for k := range l.vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			path := l.varPath(k)
			pos := p.res.Pos(path)
			if !onlyMatches(p.req.Only, path, l.name+"."+k) {
				continue
			}
			id := l.name + "." + k
			var lv *liveVar
			if v, ok := l.live[k]; ok {
				lv = &v
			}
			d, err := sharedDesired(l.vars[k], declaredSecret[k], lv)
			fp := &fieldPlan{fv: projectconfig.FieldValue{ID: id, Path: path, Pos: pos, Applies: AppliesStaged, Value: l.vars[k]}}
			if err != nil {
				p.errorf("invalid_template", path, pos, "%s", err.Error())
				fp.skipped = true
				sh.fields = append(sh.fields, fp)
				continue
			}
			fp.desired = d
			if lv != nil {
				fp.live, fp.present = liveVarView(*lv, d.Proj), true
			}
			p.finishShared(fp)
			sh.fields = append(sh.fields, fp)
		}
		for _, k := range l.secrets {
			path := l.varPath(k)
			if sc := "secrets.keys"; l.name == layerEnvironment {
				path = "environments." + p.env.Env.Slug + "." + sc
			} else {
				path = sc
			}
			if !onlyMatches(p.req.Only, path, l.name+"."+k+secretSuffix) {
				continue
			}
			pos := p.res.Pos(path)
			fp := &fieldPlan{fv: projectconfig.FieldValue{ID: l.name + "." + k + secretSuffix, Path: path, Pos: pos, Applies: AppliesStaged,
				Value: projectconfig.SecretDecl{Name: k, Scope: l.scope}}}
			fp.desired = fieldView{FP: projectconfigFingerprintSecret(true), Display: "secret"}
			if lv, ok := l.live[k]; ok {
				fp.live, fp.present = fieldView{FP: projectconfigFingerprintSecret(lv.Secret), Display: secretText(lv.Secret)}, true
				if !lv.Secret {
					p.warnf("secret_flag_differs", path, pos, "%s is stored as a plain shared variable; the apply marks it secret and keeps its value", k)
				}
			} else {
				p.warnf("secret_missing_value", path, pos, "%s has no value yet; set it before deploying", k)
			}
			p.finishShared(fp)
			sh.fields = append(sh.fields, fp)
		}
		if len(p.req.Only) == 0 {
			declared := func(id string) bool {
				if isAux(id) || !strings.HasPrefix(id, l.name+".") {
					return true
				}
				for _, f := range sh.fields {
					if f.fv.ID == id {
						return true
					}
				}
				return false
			}
			sh.released = releasedFields(p.live.OwnedShared, declared)
			if p.req.Prune {
				for _, id := range sh.released {
					if strings.HasSuffix(id, secretSuffix) {
						continue
					}
					key := strings.TrimPrefix(id, l.name+".")
					lv, ok := l.live[key]
					if !ok {
						continue
					}
					sh.removes = append(sh.removes, &fieldPlan{
						fv:   projectconfig.FieldValue{ID: id, Path: l.varPath(key), Applies: AppliesStaged},
						live: liveVarView(lv, ""), present: true, base: p.live.OwnedShared[id], hasBase: true, changed: true, remove: true,
					})
				}
			}
		}
		p.shared = append(p.shared, sh)
	}
}

func projectconfigFingerprintSecret(secret bool) string {
	return projectconfig.Fingerprint(map[string]any{"secret": secret, "scope": "runtime"})
}

func (p *planned) finishShared(fp *fieldPlan) {
	fp.base, fp.hasBase = p.live.OwnedShared[fp.fv.ID]
	fp.state = classify(fp.base, fp.hasBase, fp.live.FP, fp.present, fp.desired.FP)
	fp.changed = fp.state != StateInSync
}

// finish assembles the public plan from the planned services and layers.
func (p *planned) finish() {
	pub := &p.public
	notInFile := pub.Services
	pub.Services = make([]PlanService, 0, len(p.svcs)+len(notInFile))
	sum := PlanSummary{RequiresDeploy: []string{}}
	p.scopes.add("projects:write")
	blocked := false
	for _, sp := range p.svcs {
		pub.Services = append(pub.Services, sp.pl)
		switch sp.pl.Action {
		case ActionCreate:
			sum.Create++
		case ActionUpdate:
			sum.Update++
		case ActionUnchanged:
			sum.Unchanged++
		case ActionBlocked:
			blocked = true
		}
		if sp.ls != nil && sp.ls.Source == nil && sp.pl.Action != ActionBlocked && adoptsSomething(sp.pl) {
			sum.Adopted++
		}
		if sp.pl.Action == ActionBlocked {
			continue
		}
		if len(sp.released) > 0 {
			p.hasWork = true // releasing a field rewrites ownership: not a no-op
		}
		needsDeploy := false
		for _, c := range sp.pl.Changes {
			p.countChange(&sum, c)
			p.scopes.add(scopeOfField(c.Field))
			if p.env.Env.IsProduction && c.Op != OpRemove && strings.HasPrefix(c.Field, "tasks.") {
				// A task runs its command in the production container: the same
				// power as run_command, so tasks.Service.CreateTask / UpdateTask
				// need deployments:write there.
				p.scopes.add(policy.ScopeDeploymentsWrite)
			}
			if c.Applies == AppliesStaged {
				needsDeploy = true
			}
			if c.Op == OpRemove {
				sum.Pruned++
			}
			p.hasWork = true
		}
		for _, f := range append(append([]*fieldPlan(nil), sp.fields...), sp.removes...) {
			if f.skipped || !(f.changed || f.remove) {
				continue
			}
			if proxyScopeNeeded(f) {
				p.scopes.add("proxy:write")
			}
		}
		if sp.create && sp.rs.Kind == projectconfig.KindRoute {
			p.scopes.add("proxy:write")
		}
		if needsDeploy || sp.create {
			sum.RequiresDeploy = append(sum.RequiresDeploy, sp.slug)
		}
	}
	pub.Services = append(pub.Services, notInFile...)
	sum.NotInFile = len(notInFile)
	for _, sh := range p.shared {
		if len(sh.released) > 0 {
			p.hasWork = true
		}
		target := &pub.Shared.Project
		if sh.layer == layerEnvironment {
			target = &pub.Shared.Environment
		}
		for _, f := range append(append([]*fieldPlan(nil), sh.fields...), sh.removes...) {
			if f.skipped || !(f.changed || f.remove) {
				continue
			}
			c := p.change(&svcPlan{}, f)
			if f.remove {
				sum.Pruned++
			}
			*target = append(*target, c)
			p.countChange(&sum, c)
			p.hasWork = true
			if sh.layer == layerProject {
				p.needsRole = true
			}
		}
	}
	p.diags = p.diags.Dedup().Sorted()
	pub.Diagnostics = p.diags
	sum.Errors = p.diags.Count(projectconfig.SeverityError)
	sum.Warnings = p.diags.Count(projectconfig.SeverityWarning)
	pub.Summary = sum
	pub.Valid = sum.Errors == 0 && !blocked
	pub.RequiredScopes = p.scopes.list()
	if p.env.Env.IsProduction && (sum.Immediate > 0 || sum.Deferred > 0) {
		pub.Guard = Guard{ProductionApproval: ApprovalRequired, Reason: "production environment with changes that take effect at once"}
	}
	pub.PlanID = p.planID()
}

func (p *planned) countChange(sum *PlanSummary, c PlanChange) {
	switch c.Applies {
	case AppliesImmediate:
		sum.Immediate++
	case AppliesDeferred:
		sum.Deferred++
	default:
		sum.Staged++
	}
}

// proxyScopeNeeded reports a domain rule that touches proxy fields (the use case then needs proxy:write).
func proxyScopeNeeded(f *fieldPlan) bool {
	if !strings.HasPrefix(f.fv.ID, "domains.") || strings.HasSuffix(f.fv.ID, ".cloudflare") {
		return false
	}
	host, _ := splitDomainID(f.fv.ID)
	if strings.HasPrefix(host, "*.") {
		return true
	}
	r, ok := f.fv.Value.(projectconfig.DomainRule)
	if !ok {
		return false
	}
	for _, part := range ruleParts(r) {
		if part != "targetPort" && part != "component" && part != "process" {
			return true
		}
	}
	return r.PathPrefix != ""
}

// planID is the stale-plan guard: a digest of everything the plan depends on.
func (p *planned) planID() string {
	type svcDigest struct {
		Slug       string  `json:"slug"`
		ServiceID  *string `json:"serviceId"`
		Generation int64   `json:"generation"`
		Changes    string  `json:"changes"`
	}
	var svcs []svcDigest
	var immediate []string
	for _, sp := range p.svcs {
		d := svcDigest{Slug: sp.slug, Generation: sp.pl.Generation}
		if sp.pl.ServiceID != nil {
			s := sp.pl.ServiceID.String()
			d.ServiceID = &s
		}
		var cs []string
		for _, c := range sp.pl.Changes {
			cs = append(cs, strings.Join([]string{c.Field, c.Op, c.baseFP, c.liveFP, c.desiredFP}, "|"))
		}
		d.Changes = projectconfig.Fingerprint(cs)
		svcs = append(svcs, d)
		for _, f := range sp.fields {
			if f.fv.Applies == AppliesImmediate && !f.skipped {
				immediate = append(immediate, sp.slug+"/"+f.fv.ID+"="+f.live.FP)
			}
		}
	}
	var shared []string
	for _, sh := range p.shared {
		for _, f := range sh.fields {
			shared = append(shared, sh.layer+"/"+f.fv.ID+"="+f.live.FP+">"+f.desired.FP)
		}
	}
	only := append([]string(nil), p.req.Only...)
	sort.Strings(only)
	body := map[string]any{
		"schema": projectconfig.SchemaVersion, "environmentId": p.env.Env.ID.String(), "fileSha256": p.fileSHA,
		"attachmentsSha256": attachmentsSHA(p.req.Input.Attachments), "only": only, "prune": p.req.Prune,
		"services": svcs, "immediateLiveDigest": projectconfig.Fingerprint(immediate), "shared": projectconfig.Fingerprint(shared),
	}
	fp := projectconfig.Fingerprint(body)
	return "pl_" + strings.TrimPrefix(fp, "sha256:")[:32]
}

// adoptsSomething reports a service whose first apply changes it: some live value differs from the file.
func adoptsSomething(s PlanService) bool {
	for _, c := range s.Changes {
		if c.Reason == ReasonAdopt {
			return true
		}
	}
	return false
}
