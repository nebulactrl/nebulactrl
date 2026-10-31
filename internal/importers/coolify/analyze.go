// Package coolify imports a Coolify v4 instance into NebulaCtrl: projects,
// environments, applications, databases and their variables become staged
// NebulaCtrl projects, services and managed databases. Nothing runs until the
// reviewer deploys; data (databases, volumes) is not copied.
//
// The pipeline is Fetch (read the Coolify API with a read + read:sensitive
// token) → Analyze (pure: map, rewrite references, flag what is unsupported)
// → Plan (what the wizard and the CLI show; no values) → Apply (one
// transaction that stages everything).
package coolify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Target is what the import is judged against: the projects that already exist
// and the server the services would run on.
type Target struct {
	Projects []TargetProject
	Server   *TargetServer
	// HostTaken reports whether a hostname is used by a NebulaCtrl domain.
	HostTaken func(host string) bool
}

// TargetProject is an existing project.
type TargetProject struct {
	ID   uuid.UUID
	Name string
	Slug string
	Envs []TargetEnv
}

// TargetEnv is an existing environment with the slugs of its services.
type TargetEnv struct {
	ID           uuid.UUID
	Name         string
	Slug         string
	Production   bool
	ServiceSlugs []string
}

// TargetServer is the chosen server.
type TargetServer struct {
	ID           uuid.UUID
	Name         string
	Unrestricted bool
}

type analyzer struct {
	inv  *Inventory
	ch   CoolifyChoices
	tg   Target
	plan *CoolifyPlan

	needSensitive bool
	envTotal      int
	envUnreadable int
}

func isProductionName(n string) bool {
	n = strings.ToLower(strings.TrimSpace(n))
	return n == "production" || n == "prod"
}

// Analyze maps the inventory to a plan. It is pure: no I/O.
func Analyze(inv *Inventory, ch CoolifyChoices, tg Target) *CoolifyPlan {
	a := &analyzer{inv: inv, ch: ch, tg: tg, plan: &CoolifyPlan{
		CoolifyVersion: inv.Version,
		Projects:       []CoolifyPlanProject{}, Unsupported: []CoolifyPlanUnsupported{}, Registries: []CoolifyPlanRegistry{}, Rewrites: []CoolifyPlanRewrite{},
		Checks: []CoolifyCheck{}, Warnings: []string{}, Blockers: []string{}, Followups: []string{},
	}}
	a.run()
	return a.plan
}

func (a *analyzer) run() {
	p := a.plan
	for _, n := range a.inv.Notes {
		p.Warnings = append(p.Warnings, n)
	}

	envOwner := map[int]*Project{}
	for i := range a.inv.Projects {
		for _, e := range a.inv.Projects[i].Environments {
			envOwner[e.ID] = &a.inv.Projects[i]
		}
	}

	takenProjects := map[string]bool{}
	for _, tp := range a.tg.Projects {
		takenProjects[tp.Slug] = true
	}
	assigned := map[string]bool{} // coolify resource uuids placed in a plan

	projs := append([]Project(nil), a.inv.Projects...)
	sort.SliceStable(projs, func(i, j int) bool { return strings.ToLower(projs[i].Name) < strings.ToLower(projs[j].Name) })
	for _, cp := range projs {
		pc := a.ch.Projects[cp.UUID]
		pp := CoolifyPlanProject{UUID: cp.UUID, Name: cp.Name, Action: "create", Environments: []CoolifyPlanEnvironment{}}
		var existing *TargetProject
		switch {
		case pc.Skip != nil && *pc.Skip:
			pp.Action, pp.Slug = "skip", projects.Slugify(cp.Name, "project")
			p.Projects = append(p.Projects, pp)
			continue
		case pc.ExistingProjectID != nil:
			for i := range a.tg.Projects {
				if a.tg.Projects[i].ID == *pc.ExistingProjectID {
					existing = &a.tg.Projects[i]
				}
			}
			if existing == nil {
				p.Blockers = append(p.Blockers, fmt.Sprintf("The project chosen for %q does not exist.", cp.Name))
				pp.Action, pp.Slug = "skip", projects.Slugify(cp.Name, "project")
				p.Projects = append(p.Projects, pp)
				continue
			}
			pp.Action, pp.Name, pp.Slug, pp.ExistingID = "existing", existing.Name, existing.Slug, &existing.ID
			pp.Collision = true
		default:
			name := cp.Name
			if pc.Name != nil && strings.TrimSpace(*pc.Name) != "" {
				name = strings.TrimSpace(*pc.Name)
			}
			base := projects.Slugify(name, "project")
			if takenProjects[base] {
				pp.Collision = true
				if pc.Name == nil {
					name += " (Coolify)"
					base = projects.Slugify(name, "project")
				}
			}
			taken := make([]string, 0, len(takenProjects))
			for s := range takenProjects {
				taken = append(taken, s)
			}
			pp.Name, pp.Slug = name, projects.UniqueSlug(base, taken)
			takenProjects[pp.Slug] = true
		}
		a.planEnvironments(&pp, &cp, pc, existing, assigned)
		p.Projects = append(p.Projects, pp)
	}

	// Resources that belong to no known environment.
	for i := range a.inv.Applications {
		app := &a.inv.Applications[i]
		if !assigned[app.UUID] && envOwner[app.EnvironmentID] == nil {
			p.Unsupported = append(p.Unsupported, CoolifyPlanUnsupported{Kind: "application", Name: app.Name, Reason: "The application belongs to an environment the token cannot see."})
		}
	}
	for i := range a.inv.Databases {
		d := &a.inv.Databases[i]
		if !assigned[d.UUID] && envOwner[d.EnvironmentID] == nil {
			p.Unsupported = append(p.Unsupported, CoolifyPlanUnsupported{Kind: "database", Name: d.Name, Reason: "The database belongs to an environment the token cannot see."})
		}
	}
	for _, s := range a.inv.Services {
		proj := ""
		if cp := envOwner[s.EnvironmentID]; cp != nil {
			proj = cp.Name
		}
		why := "A Coolify one-click service (" + orDefault(s.Type, "template") + ") runs as a Docker Compose stack and is not imported automatically."
		if s.HasCompose {
			why += " Its Compose file is readable in Coolify: save it and import it with nebulactrl import compose <file> --as-stack."
		} else {
			why += " Copy its Compose file from Coolify (the token needs read:sensitive to read it over the API) and import it with nebulactrl import compose <file> --as-stack, or install the matching NebulaCtrl template."
		}
		p.Unsupported = append(p.Unsupported, CoolifyPlanUnsupported{Kind: "service", Name: s.Name, Project: proj, Reason: why})
	}
	a.finish()
}

func (a *analyzer) planEnvironments(pp *CoolifyPlanProject, cp *Project, pc CoolifyProjectChoice, existing *TargetProject, assigned map[string]bool) {
	envs := append([]Environment(nil), cp.Environments...)
	sort.SliceStable(envs, func(i, j int) bool {
		pi, pj := isProductionName(envs[i].Name), isProductionName(envs[j].Name)
		if pi != pj {
			return pi
		}
		return envs[i].ID < envs[j].ID
	})
	takenEnvSlugs := map[string]bool{}
	if existing != nil {
		for _, e := range existing.Envs {
			takenEnvSlugs[e.Slug] = true
		}
	}
	for _, ce := range envs {
		ec := pc.Environments[ce.Name]
		pe := CoolifyPlanEnvironment{Name: ce.Name, Action: "create", Resources: []CoolifyPlanResource{}, coolifyID: ce.ID}
		if ec.Skip != nil && *ec.Skip {
			pe.Action, pe.Slug = "skip", projects.Slugify(ce.Name, "environment")
			pp.Environments = append(pp.Environments, pe)
			continue
		}
		if ec.Name != nil && strings.TrimSpace(*ec.Name) != "" {
			pe.Name = strings.TrimSpace(*ec.Name)
		}
		var te *TargetEnv
		if existing != nil {
			for i := range existing.Envs {
				if strings.EqualFold(existing.Envs[i].Name, pe.Name) || existing.Envs[i].Slug == projects.Slugify(pe.Name, "environment") {
					te = &existing.Envs[i]
				}
			}
		}
		var serviceSlugs []string
		if te != nil {
			pe.Action, pe.Name, pe.Slug, pe.Production, pe.existingID = "existing", te.Name, te.Slug, te.Production, &te.ID
			serviceSlugs = append(serviceSlugs, te.ServiceSlugs...)
		} else {
			taken := make([]string, 0, len(takenEnvSlugs))
			for s := range takenEnvSlugs {
				taken = append(taken, s)
			}
			pe.Slug = projects.UniqueSlug(projects.Slugify(pe.Name, "environment"), taken)
			takenEnvSlugs[pe.Slug] = true
			pe.Production = isProductionName(pe.Name)
		}
		serviceSlugs = append(serviceSlugs, services.ReservedSlugs...)
		a.planResources(&pe, &ce, serviceSlugs, assigned)
		pp.Environments = append(pp.Environments, pe)
	}
}

func (a *analyzer) planResources(pe *CoolifyPlanEnvironment, ce *Environment, taken []string, assigned map[string]bool) {
	type item struct {
		name string
		make func() CoolifyPlanResource
	}
	var dbItems, appItems []item
	for i := range a.inv.Databases {
		d := &a.inv.Databases[i]
		if d.EnvironmentID != ce.ID || ce.ID == 0 {
			continue
		}
		assigned[d.UUID] = true
		dbItems = append(dbItems, item{d.Name, func() CoolifyPlanResource { return a.planDatabase(d, a.ch.Resources[d.UUID], pe) }})
	}
	for i := range a.inv.Applications {
		app := &a.inv.Applications[i]
		if app.EnvironmentID != ce.ID || ce.ID == 0 {
			continue
		}
		assigned[app.UUID] = true
		appItems = append(appItems, item{app.Name, func() CoolifyPlanResource { return a.planApplication(app, a.ch.Resources[app.UUID], pe) }})
	}
	byName := func(s []item) {
		sort.SliceStable(s, func(i, j int) bool { return strings.ToLower(s[i].name) < strings.ToLower(s[j].name) })
	}
	byName(dbItems)
	byName(appItems)
	for _, it := range append(dbItems, appItems...) {
		r := it.make()
		r.Slug = projects.UniqueSlug(projects.Slugify(r.Name, "service"), taken)
		taken = append(taken, r.Slug)
		if rc := r.choice; rc.Include != nil && r.Source.Type != "" && (r.Kind != "database" || !r.licence) {
			if !*rc.Include {
				r.Include, r.IncludeNote = false, "Left out in the import."
			}
		}
		pe.Resources = append(pe.Resources, r)
	}
	a.resolveEnvironment(pe)
}

// resolveEnvironment settles what needs every resource of the environment:
// worker detection, variables and the references between resources.
func (a *analyzer) resolveEnvironment(pe *CoolifyPlanEnvironment) {
	// Who can be pointed at.
	var targets []*target
	var others []otherHost
	for i := range pe.Resources {
		r := &pe.Resources[i]
		if r.Include && r.Source.Type != "" {
			targets = append(targets, &target{res: r, hosts: r.hosts, db: r.dbRec})
			continue
		}
		for _, h := range r.hosts {
			others = append(others, otherHost{h, r.Name, "not imported"})
		}
	}
	// Resources of other environments (a Coolify network can be shared).
	for i := range a.inv.Applications {
		app := &a.inv.Applications[i]
		if app.EnvironmentID != pe.coolifyID {
			others = append(others, otherHost{strings.ToLower(app.UUID), app.Name, "in another environment"})
		}
	}
	for i := range a.inv.Databases {
		d := &a.inv.Databases[i]
		if d.EnvironmentID != pe.coolifyID {
			others = append(others, otherHost{strings.ToLower(d.UUID), d.Name, "in another environment"})
		}
	}

	// Is an application used by another one? (Workers are used by nobody.)
	for _, t := range targets {
		if t.res.app == nil {
			continue
		}
		for i := range pe.Resources {
			o := &pe.Resources[i]
			if o == t.res || o.app == nil {
				continue
			}
			for _, e := range o.app.Envs {
				for _, h := range t.hosts {
					for _, s := range findAll(e.Value, h, true) {
						if hostBoundary(e.Value, s, s+len(h)) {
							t.res.referenced = true
						}
					}
				}
			}
		}
	}
	for i := range pe.Resources {
		r := &pe.Resources[i]
		if r.app == nil || !r.Include {
			continue
		}
		a.finalizeKind(r)
	}

	for i := range pe.Resources {
		r := &pe.Resources[i]
		if r.app == nil || !r.Include {
			continue
		}
		c := &refCtx{targets: targets, others: others, linked: map[*target]bool{}}
		a.buildEnv(r, c)
	}
	for i := range pe.Resources {
		if r := &pe.Resources[i]; r.Include && r.Kind == "database" {
			r.Followup = databaseFollowup(r.dbRec, r.Slug)
		}
	}
}

// finalizeKind decides between a web application and a worker: an application
// with no domain, no health check, no published port and no other service
// pointing at it serves nobody, so it becomes a worker without a port (the
// reviewer can override it).
func (a *analyzer) finalizeKind(r *CoolifyPlanResource) {
	rc := r.choice
	if rc.Kind != nil || rc.Port != nil {
		if r.Port == 0 {
			r.Kind = "worker"
		}
		return
	}
	if r.Port == 0 {
		r.Kind = "worker"
		return
	}
	enabled := false
	for _, d := range r.Domains {
		if d.Enabled || d.Conflict {
			enabled = true
		}
	}
	if len(splitList(r.app.FQDN)) > 0 {
		enabled = true
	}
	if !enabled && !r.app.Health.Enabled && !r.referenced && strings.TrimSpace(r.app.PortsMappings) == "" {
		r.Kind, r.Port = "worker", 0
		if r.Health != nil && r.Health.Type != "none" {
			r.Health = &CoolifyPlanHealth{Type: "none"}
		}
		r.addNote("kind", SevInfo, "No domain, no health check and no other service points at it: imported as a worker without a port. Set the port in the import (or the service settings) if it serves HTTP.")
	}
}

func (a *analyzer) finish() {
	p := a.plan
	var all []*CoolifyPlanResource
	for i := range p.Projects {
		for j := range p.Projects[i].Environments {
			for k := range p.Projects[i].Environments[j].Resources {
				all = append(all, &p.Projects[i].Environments[j].Resources[k])
			}
		}
	}

	hostsRegistries := map[string]*CoolifyPlanRegistry{}
	var regOrder []string
	var gitApps, gitNeedConn int
	for _, r := range all {
		if !r.Include {
			name := r.Name
			kind := "application"
			if r.Kind == "database" {
				kind = "database"
			}
			p.Unsupported = append(p.Unsupported, CoolifyPlanUnsupported{Kind: kind, Name: name, Reason: r.IncludeNote})
			continue
		}
		if r.Kind == "database" {
			p.Totals.Databases++
			p.Followups = append(p.Followups, r.Followup)
		} else {
			p.Totals.Services++
		}
		for _, e := range r.Env {
			if e.Skipped {
				continue
			}
			p.Totals.Variables++
			if e.Secret {
				p.Totals.Secrets++
			}
		}
		for _, d := range r.Domains {
			if d.Enabled {
				p.Totals.Domains++
			}
			if d.Conflict {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s: the hostname %s is already used by a NebulaCtrl service (an earlier import?) and was left out.", r.Name, d.Host))
			}
		}
		for _, v := range r.Volumes {
			p.general = append(p.general, fmt.Sprintf("%s: the data of volume %s (%s) is not copied. Copy it from Coolify's Docker volume before the cutover (for example docker run --rm -v <coolify volume>:/from -v <new volume>:/to alpine cp -a /from/. /to/).", r.Name, v.Name, v.MountPath))
		}
		if r.registryHost != "" && r.Source.Type == "image" {
			reg := hostsRegistries[r.registryHost]
			if reg == nil {
				reg = &CoolifyPlanRegistry{Host: r.registryHost, Services: []string{}}
				for _, l := range a.inv.Registries {
					if strings.EqualFold(l.Host, r.registryHost) || (r.registryHost == "docker.io" && (l.Host == "docker.io" || l.Host == "index.docker.io")) {
						reg.Username = l.Username
					}
				}
				_, reg.Provided = a.ch.Registries[r.registryHost]
				hostsRegistries[r.registryHost] = reg
				regOrder = append(regOrder, r.registryHost)
			}
			reg.Services = append(reg.Services, r.Name)
		}
		if r.Source.Type == "git" {
			gitApps++
			if a.ch.GitConnectionID == nil && r.Source.Git.Repo != "" {
				gitNeedConn++
			}
		}
	}
	for _, h := range regOrder {
		reg := hostsRegistries[h]
		// Docker Hub images are mostly public; only ask for hosts that are private by nature or that Coolify is logged in to.
		if h == "docker.io" && reg.Username == "" && !reg.Provided {
			continue
		}
		p.Registries = append(p.Registries, *reg)
		if !reg.Provided {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: image pulls from %s need credentials (Coolify's login cannot be exported). Provide them in the import or set them in each service's Source settings before deploying.", strings.Join(reg.Services, ", "), h))
		}
	}
	sort.SliceStable(p.Registries, func(i, j int) bool { return p.Registries[i].Host < p.Registries[j].Host })

	for _, r := range all {
		if r.Include {
			p.Rewrites = append(p.Rewrites, r.rewrites...)
		}
	}

	// preflight checks
	n := len(a.inv.Applications) + len(a.inv.Databases)
	msg := fmt.Sprintf("Read %d project(s), %d application(s), %d database(s), %d one-click service(s)", len(a.inv.Projects), len(a.inv.Applications), len(a.inv.Databases), len(a.inv.Services))
	if a.inv.Version != "" {
		msg = "Coolify " + a.inv.Version + ": " + msg
	}
	p.Checks = append(p.Checks, CoolifyCheck{ID: "coolify", Status: CheckOK, Message: msg + "."})
	switch {
	case a.envUnreadable > 0:
		p.Checks = append(p.Checks, CoolifyCheck{ID: "sensitive", Status: CheckFail, Message: fmt.Sprintf("Coolify did not return the value of %d variable(s): the token lacks the read:sensitive ability. Create a token with read and read:sensitive (Keys & Tokens → API tokens) and run the import again.", a.envUnreadable)})
		p.Blockers = append(p.Blockers, "The Coolify token cannot read variable values (read:sensitive is missing).")
	case a.needSensitive && len(a.inv.Applications) > 0:
		p.Checks = append(p.Checks, CoolifyCheck{ID: "sensitive", Status: CheckWarn, Message: "Coolify hid the custom Docker options, commands and database credentials: the token seems to lack read:sensitive. Docker options and deployment commands were not imported."})
	default:
		p.Checks = append(p.Checks, CoolifyCheck{ID: "sensitive", Status: CheckOK, Message: "The token can read sensitive values."})
	}
	if a.tg.Server != nil {
		p.Checks = append(p.Checks, CoolifyCheck{ID: "server", Status: CheckOK, Message: "Services will run on " + a.tg.Server.Name + "."})
		if len(a.inv.Servers) > 1 {
			p.Checks = append(p.Checks, CoolifyCheck{ID: "servers", Status: CheckWarn, Message: fmt.Sprintf("Coolify has %d servers; every service is placed on %s. Move services to other servers in their settings.", len(a.inv.Servers), a.tg.Server.Name)})
		}
	} else if n > 0 {
		p.Checks = append(p.Checks, CoolifyCheck{ID: "server", Status: CheckWarn, Message: "No server chosen: choose one to see which Docker options it accepts and to import."})
	}
	if gitNeedConn > 0 {
		p.Checks = append(p.Checks, CoolifyCheck{ID: "git", Status: CheckWarn, Message: fmt.Sprintf("%d git service(s) build from GitHub/GitLab repositories and no git connection was chosen: private repositories cannot be cloned until you link one (Settings → Source).", gitNeedConn)})
	}

	p.general = append(p.general, "DNS still points at Coolify: nothing is routed to NebulaCtrl until you move the records (pre-issue certificates with DNS-01 first if you keep both side by side).")
	p.general = append(p.general, "Review every service, then deploy from the staged bar or with nebulactrl services deploy <service> --wait.")
	for i := range p.Projects {
		if p.Projects[i].Action != "skip" {
			p.Totals.Projects++
		}
	}
	p.Followups = append(p.Followups, p.general...)
	p.Installable = len(p.Blockers) == 0
}
