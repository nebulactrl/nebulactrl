package configsync

import (
	"errors"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// auxSuffix marks the owned entries that store a projection (the declared parts of a composite value).
const auxSuffix = "#keys"

func isAux(id string) bool { return strings.HasSuffix(id, auxSuffix) }

// liveKindOf is the file kind of a live service.
func liveKindOf(row db.Service) string {
	switch row.Kind {
	case services.KindDatabase:
		return projectconfig.KindDatabase
	case services.KindRoute:
		return projectconfig.KindRoute
	case services.KindStack:
		return projectconfig.KindStack
	}
	if row.SourceType == services.SourceGit {
		return projectconfig.KindGit
	}
	return projectconfig.KindImage
}

// sourceSwitchOf reports the source type a file moves a live app or worker to
// when its file kind is the other of image and git. Any other difference of
// kind is not a source change.
func sourceSwitchOf(row db.Service, fileKind string) (to string, ok bool) {
	switch from := liveKindOf(row); {
	case from == projectconfig.KindImage && fileKind == projectconfig.KindGit:
		return services.SourceGit, true
	case from == projectconfig.KindGit && fileKind == projectconfig.KindImage:
		return services.SourceImage, true
	}
	return "", false
}

// orderServices returns the slugs of the file in an order where a service follows the
// services it depends on (depends_on and references in its variables).
func orderServices(res *projectconfig.Resolved) []string {
	slugs := res.SortedSlugs()
	deps := map[string][]string{}
	for _, slug := range slugs {
		rs := res.Services[slug]
		if rs.Def == nil {
			continue
		}
		seen := map[string]bool{}
		add := func(d string) {
			if d != slug && !seen[d] {
				if _, ok := res.Services[d]; ok {
					seen[d] = true
					deps[slug] = append(deps[slug], d)
				}
			}
		}
		for _, d := range rs.Def.DependsOn {
			add(d)
		}
		for _, v := range rs.Def.Variables {
			if t, err := varexpr.Parse(v.Value); err == nil {
				for _, r := range t.Refs() {
					add(r.NS)
				}
			}
		}
		if rs.Def.Route != nil {
			for _, u := range rs.Def.Route.Upstreams {
				if u.Service != nil {
					add(*u.Service)
				}
			}
		}
	}
	placed := map[string]bool{}
	out := make([]string, 0, len(slugs))
	remaining := append([]string(nil), slugs...)
	for len(remaining) > 0 {
		pick := -1
		for i, s := range remaining {
			ready := true
			for _, d := range deps[s] {
				if !placed[d] {
					ready = false
					break
				}
			}
			if ready {
				pick = i
				break
			}
		}
		if pick < 0 { // a cycle (reported by the validator): keep the sorted order
			out = append(out, remaining...)
			break
		}
		placed[remaining[pick]] = true
		out = append(out, remaining[pick])
		remaining = append(remaining[:pick], remaining[pick+1:]...)
	}
	return out
}

// fieldPath maps a field id to the file key path used for released and pruned members.
func fieldPath(base, id string) string {
	for _, c := range []string{"variables", "volumes", "tasks", "backups", "networks", "links"} {
		if rest, ok := strings.CutPrefix(id, c+"."); ok {
			return base + "." + c + "." + strings.TrimSuffix(rest, secretSuffix)
		}
	}
	if rest, ok := strings.CutPrefix(id, "domains."); ok {
		if host, isCF := strings.CutSuffix(rest, ".cloudflare"); isCF {
			return base + ".domains." + `"` + host + `".cloudflare`
		}
		return base + ".domains." + `"` + rest + `"`
	}
	if spec, ok := projectconfig.LookupField(id); ok && strings.HasPrefix(spec.Key, "processes.") {
		// config.processes.<name> and processes.<name>.scale.<key> sit under processes.<name> in the file.
		pre, suf, _ := strings.Cut(spec.ID, "*")
		name := strings.TrimSuffix(strings.TrimPrefix(id, pre), suf)
		return base + "." + strings.Replace(spec.Key, "*", name, 1)
	}
	if spec, ok := projectconfig.LookupField(id); ok && !spec.Collection {
		return base + "." + spec.Key
	}
	return base + "." + id
}

// planServices plans every service of the file and lists the live services the file does not mention.
func (s *Service) planServices(p *planned) error {
	for _, slug := range orderServices(p.res) {
		rs := p.res.Services[slug]
		if !serviceSelected(p.req.Only, rs) {
			continue
		}
		sp := s.planService(p, slug, rs)
		p.svcs = append(p.svcs, sp)
	}
	for _, slug := range p.live.Order {
		if _, inFile := p.res.Services[slug]; inFile {
			continue
		}
		ls := p.live.Services[slug]
		id := ls.Row.ID
		p.public.Services = append(p.public.Services, PlanService{
			Slug: slug, Kind: liveKindOf(ls.Row), Action: ActionNotInFile, ServiceID: &id, Generation: ls.Row.Generation,
			Managed: ls.Source != nil, Note: "kept; remove it in the dashboard", Changes: []PlanChange{},
		})
	}
	return nil
}

// serviceSelected reports whether the only filter reaches the service.
func serviceSelected(only []string, rs *projectconfig.ResolvedService) bool {
	if len(only) == 0 {
		return true
	}
	if onlyMatches(only, rs.Path, "") {
		return true
	}
	for id, fv := range rs.Fields {
		if onlyMatches(only, fv.Path, id) {
			return true
		}
	}
	return false
}

func (s *Service) planService(p *planned, slug string, rs *projectconfig.ResolvedService) *svcPlan {
	pc := p.pc
	pc.rs = rs
	ls := p.live.Services[slug]
	sp := &svcPlan{slug: slug, rs: rs, ls: ls, create: ls == nil}
	sp.pl = PlanService{Slug: slug, Kind: rs.Kind, Changes: []PlanChange{}, Managed: ls != nil && ls.Source != nil}
	if ls != nil {
		id := ls.Row.ID
		sp.pl.ServiceID, sp.pl.Generation = &id, ls.Row.Generation
	}
	svcPos := p.res.Pos(rs.Path)

	if ls != nil && liveKindOf(ls.Row) != rs.Kind {
		if to, ok := sourceSwitchOf(ls.Row, rs.Kind); ok {
			sp.switchTo = to
		} else {
			p.errorf("source_type_change", rs.Path, svcPos, "service %q is a %s service; a file cannot turn it into a %s service", slug, liveKindOf(ls.Row), rs.Kind)
			sp.blocked = true
		}
	}

	// The effective server.
	if name := rs.ServerName; name != "" {
		srv, res := p.live.Refs.server(name)
		switch res {
		case refOK:
			sp.server = &srv
		case refNone:
			p.errorf("unknown_server", rs.Path+".server", firstPos(p.res.Pos(rs.Path+".server"), svcPos), "no server named %q", name)
			sp.blocked = true
		case refAmbiguous:
			p.errorf("ambiguous_reference", rs.Path+".server", firstPos(p.res.Pos(rs.Path+".server"), svcPos), "several servers are named %q", name)
			sp.blocked = true
		}
	} else if ls != nil && ls.Row.ServerID != nil {
		if srv, ok := p.live.Refs.serverByID(*ls.Row.ServerID); ok {
			sp.server = &srv
		}
	}

	var fieldStates []string
	for _, id := range rs.FieldIDs() {
		fv := rs.Fields[id]
		if !onlyMatches(p.req.Only, fv.Path, id) {
			continue
		}
		ops := opsFor(id)
		if ops == nil {
			p.errorf("invalid_value", fv.Path, fv.Pos, "the key is not supported by this version of the format")
			sp.blocked = true
			continue
		}
		fp := &fieldPlan{fv: fv}
		d, err := ops.desired(pc, ls, fv)
		if err != nil {
			code, msg := "invalid_value", err.Error()
			var re *refError
			if errors.As(err, &re) {
				code, msg = re.Code, re.Msg
			}
			p.errorf(code, fv.Path, fv.Pos, "%s", msg)
			fp.skipped = true
			sp.blocked = true
			sp.fields = append(sp.fields, fp)
			continue
		}
		fp.desired = d
		if sp.create {
			fp.state, fp.changed = StateNew, true
		} else {
			fp.live, fp.present = ops.live(pc, ls, id, d.Proj)
			fp.base, fp.hasBase = ls.Owned[id]
			fp.state = classify(fp.base, fp.hasBase, fp.live.FP, fp.present, d.FP)
			fp.changed = fp.state != StateInSync
			if !fp.present {
				fp.state = StateNew
			}
		}
		fieldStates = append(fieldStates, fp.state)
		sp.fields = append(sp.fields, fp)
	}

	if ls != nil && len(p.req.Only) == 0 {
		sp.released = releasedFields(ls.Owned, func(id string) bool {
			if isAux(id) {
				return true
			}
			_, ok := rs.Fields[id]
			return ok
		})
		if p.req.Prune {
			for _, id := range sp.released {
				if !prunable(id) {
					continue
				}
				ops := opsFor(id)
				if ops == nil {
					continue
				}
				lv, present := ops.live(pc, ls, id, ls.Owned[id+auxSuffix])
				if !present {
					continue
				}
				sp.removes = append(sp.removes, &fieldPlan{
					fv:   projectconfig.FieldValue{ID: id, Path: fieldPath(rs.Path, id), Applies: appliesOfRemoval(id)},
					live: lv, present: true, base: ls.Owned[id], hasBase: true, state: StateInSync, changed: true, remove: true,
				})
			}
		}
	}

	if !sp.blocked {
		before := p.diags.Count(projectconfig.SeverityError)
		s.checkService(p, sp)
		if p.diags.Count(projectconfig.SeverityError) > before {
			sp.blocked = true
		}
	}

	// Public form.
	sp.pl.State = StateNew
	if ls != nil {
		sp.pl.State = serviceStateOf(fieldStates)
		if len(fieldStates) == 0 {
			sp.pl.State = StateInSync
		}
		sp.pl.Unmanaged = unmanagedCounts(ls, rs)
	}
	for _, id := range sp.released {
		sp.pl.Released = append(sp.pl.Released, fieldPath(rs.Path, id))
	}
	switch {
	case sp.blocked:
		sp.pl.Action = ActionBlocked
		sp.pl.Blockers = blockerCodes(p.diags, rs.Path)
	case sp.create:
		sp.pl.Action = ActionCreate
	default:
		sp.pl.Action = ActionUnchanged
	}
	if !sp.blocked {
		if sp.switchTo != "" {
			// One change for the whole switch, ahead of the fields it carries.
			from, key := liveKindOf(ls.Row), sp.switchTo
			path := rs.Path + "." + key
			sp.pl.Changes = append(sp.pl.Changes, PlanChange{
				Path: path, Line: firstPos(p.res.Pos(path), svcPos).Line, Field: services.FieldSource, Op: OpSet, Applies: AppliesStaged,
				Reason: ReasonFileChanged, From: from, To: sp.rs.Kind,
				liveFP: "source:" + from, desiredFP: "source:" + sp.rs.Kind,
			})
		}
		for _, f := range sp.fields {
			if f.changed && !f.skipped {
				sp.pl.Changes = append(sp.pl.Changes, p.change(sp, f))
			}
		}
		for _, f := range sp.removes {
			sp.pl.Changes = append(sp.pl.Changes, p.change(sp, f))
		}
		if !sp.create && len(sp.pl.Changes) > 0 {
			sp.pl.Action = ActionUpdate
		}
	}
	return sp
}

func firstPos(a, b projectconfig.Pos) projectconfig.Pos {
	if !a.IsZero() {
		return a
	}
	return b
}

func appliesOfRemoval(id string) string {
	if strings.HasPrefix(id, "tasks.") || strings.HasPrefix(id, "backups.") || strings.HasPrefix(id, "uptime.") || id == "uptime" {
		return AppliesImmediate
	}
	return AppliesStaged
}

// blockerCodes lists the error codes reported under a service's key path.
func blockerCodes(ds Diagnostics, path string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range ds {
		if d.Severity == projectconfig.SeverityError && (d.Path == path || strings.HasPrefix(d.Path, path+".")) && !seen[d.Code] {
			seen[d.Code] = true
			out = append(out, d.Code)
		}
	}
	sort.Strings(out)
	return out
}

// unmanagedCounts counts the live members of collections the file does not mention.
func unmanagedCounts(ls *liveService, rs *projectconfig.ResolvedService) map[string]int {
	out := map[string]int{}
	n := 0
	for k := range ls.Vars {
		if _, ok := rs.Fields["variables."+k]; !ok {
			if _, ok := rs.Fields["variables."+k+secretSuffix]; !ok {
				n++
			}
		}
	}
	if n > 0 {
		out["variables"] = n
	}
	n = 0
	for _, d := range ls.Domains {
		if _, ok := rs.Fields["domains."+d.Hostname+d.PathPrefix]; !ok {
			n++
		}
	}
	if n > 0 {
		out["domains"] = n
	}
	n = 0
	for name := range ls.Tasks {
		if _, ok := rs.Fields["tasks."+name]; !ok {
			n++
		}
	}
	if n > 0 {
		out["tasks"] = n
	}
	n = 0
	for key := range ls.backupKeys() {
		if _, ok := rs.Fields["backups."+key]; !ok {
			n++
		}
	}
	if n > 0 {
		out["backups"] = n
	}
	n = 0
	for _, v := range ls.Volumes {
		if _, ok := rs.Fields["volumes."+v.Name]; !ok {
			n++
		}
	}
	if n > 0 {
		out["volumes"] = n
	}
	n = 0
	for _, p := range ls.Cfg.Processes {
		if _, ok := rs.Fields["config.processes."+p.Name]; !ok {
			n++
		}
	}
	if n > 0 {
		out["processes"] = n
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// change renders one field plan as a plan change.
func (p *planned) change(sp *svcPlan, f *fieldPlan) PlanChange {
	c := PlanChange{
		Path: f.fv.Path, Line: f.fv.Pos.Line, Field: f.fv.ID, Applies: f.fv.Applies,
		baseFP: f.base, liveFP: f.live.FP, desiredFP: f.desired.FP,
	}
	switch {
	case f.remove:
		c.Op, c.Reason = OpRemove, ReasonFileChanged
		c.From, c.Masked = f.live.Display, f.live.Masked
		c.To = ""
		c.desiredFP = ""
	case f.present:
		c.Op = OpSet
		c.From, c.To = f.live.Display, f.desired.Display
	default:
		c.Op, c.Reason = OpAdd, ReasonNew
		c.To = f.desired.Display
	}
	if !f.remove {
		switch f.state {
		case StateNew:
			c.Reason = ReasonNew
		case StateAdopt:
			c.Reason = ReasonAdopt
		case StateFileChanged:
			c.Reason = ReasonFileChanged
		case StateDrift:
			c.Reason = ReasonDrift
		case StateConflict:
			c.Reason = ReasonConflict
		}
	}
	c.Masked = c.Masked || f.desired.Masked || f.live.Masked
	if c.Masked {
		if f.present && f.live.Display != "" && !strings.HasPrefix(f.live.Display, "generated(") {
			c.From = Mask
		}
		if f.desired.Display != "" && !strings.HasPrefix(f.desired.Display, "generated(") && f.desired.Masked {
			c.To = Mask
		}
	}
	return c
}
