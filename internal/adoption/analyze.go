package adoption

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Plan actions of a service.
const (
	ActionAdopt    = "adopt"
	ActionReattach = "reattach"
	ActionLeave    = "leave"
	ActionRemove   = "remove"
)

// Environment and project actions of a plan.
const (
	actCreate   = "create"
	actExisting = "existing"
	actNone     = "none"
)

// Reasons that are not about what runs on the host but about this instance.
const (
	reasonOtherTeam    = "belongs to another team"
	reasonOtherRelease = "this instance already has this service with another release"
)

// Context is what the analysis needs to know about this instance. Analyze is
// pure: every database read happens before it.
type Context struct {
	Server ContextServer
	// Projects are the projects of the team, to place new environments in.
	Projects []ContextProject
	// Environments holds the environments with the IDs found on the host, in any team.
	Environments map[uuid.UUID]ContextEnv
	// Services holds the services with the IDs found on the host, in any team, deleted ones included.
	Services map[uuid.UUID]ContextService
	// Domains are the host+path pairs this instance already serves.
	Domains map[string]bool
	// CheckDomain runs the checks a new domain of this instance passes (nil skips them).
	CheckDomain func(rule agentprotocol.DomainRule) error
}

// ContextServer is the server being reviewed.
type ContextServer struct {
	ID           uuid.UUID
	Name         string
	Held         bool
	Unrestricted bool
	// Current are the services this instance already runs on the server.
	Current map[uuid.UUID]bool
	// Unmanaged are the services left running unmanaged by an earlier review.
	Unmanaged map[uuid.UUID]bool
}

// ContextProject is a project of the team.
type ContextProject struct {
	ID       uuid.UUID
	Name     string
	Slug     string
	EnvSlugs []string
}

// ContextEnv is an environment of this instance.
type ContextEnv struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	ProjectName string
	ProjectSlug string
	Name        string
	Slug        string
	Production  bool
	SameTeam    bool
	// ServiceSlugs maps the slugs of the live services of the environment to their IDs.
	ServiceSlugs map[string]uuid.UUID
}

// ContextService is a service of this instance.
type ContextService struct {
	ID                  uuid.UUID
	EnvironmentID       uuid.UUID
	SameTeam            bool
	Deleted             bool
	CurrentDeploymentID *uuid.UUID
	// HostGone is true when the server the current deployment ran on no longer
	// exists or is revoked, so the service may be moved to this one.
	HostGone bool
}

// Analyze builds the adoption plan of an inventory. The plan is stateless:
// the same inventory, context and choices give the same plan.
func Analyze(inv agentprotocol.WorkloadInventory, cx Context, ch AdoptionChoices) *AdoptionPlan {
	a := &analyzer{inv: inv, cx: cx, ch: ch, seenDomains: map[string]bool{}}
	return a.run()
}

type analyzer struct {
	inv         agentprotocol.WorkloadInventory
	cx          Context
	ch          AdoptionChoices
	seenDomains map[string]bool
	plan        *AdoptionPlan
}

func (a *analyzer) warn(format string, args ...any) {
	a.plan.Warnings = append(a.plan.Warnings, fmt.Sprintf(format, args...))
}

func (a *analyzer) block(format string, args ...any) {
	a.plan.Blockers = append(a.plan.Blockers, fmt.Sprintf(format, args...))
}

func (a *analyzer) run() *AdoptionPlan {
	a.plan = &AdoptionPlan{
		ServerID: a.cx.Server.ID, Held: a.cx.Server.Held,
		Environments: []AdoptionEnvironment{}, Warnings: []string{}, Blockers: []string{},
	}
	groups := a.groups()
	known := map[uuid.UUID]bool{}
	svcs := make([]*AdoptionService, 0, len(groups))
	for _, g := range groups {
		known[g.id] = true
		svcs = append(svcs, a.service(g, a.ch.Services[g.id]))
		a.plan.Totals.Containers += len(g.cs)
	}
	for id := range a.ch.Services {
		if !known[id] {
			a.warn("The choice for service %s was ignored: no such service runs on this server.", id)
		}
	}
	a.unusedVolumes()
	a.placeEnvironments(svcs)
	return a.plan
}

// group is the containers (and routes) of one service ID.
type group struct {
	id     uuid.UUID
	cs     []agentprotocol.InventoryContainer // newest first
	routes []agentprotocol.Route
	// routeOnly marks a service that has external routes but no container.
	routeOnly bool
	// tailnet counts the tailnet sidecars left out of cs.
	tailnet int
}

// ownerOf is the service a route belongs to: the compose stack or the service
// of a named process, else the service of the route itself.
func ownerOf(r agentprotocol.Route) string {
	if r.Stack != "" {
		return r.Stack
	}
	return r.ServiceID
}

// helperContainer reports whether a labelled container is not a service replica.
func helperContainer(c agentprotocol.InventoryContainer) bool {
	for _, k := range []string{
		agentprotocol.LabelTaskRun, agentprotocol.LabelTaskService, agentprotocol.LabelHelper, agentprotocol.LabelBackupID,
		agentprotocol.LabelMesh, agentprotocol.LabelTunnel,
	} {
		if _, ok := c.Labels[k]; ok {
			return true
		}
	}
	return false
}

// groups collects the candidate services: those of a held server, or left
// unmanaged by an earlier review, that this instance does not already run here.
func (a *analyzer) groups() []*group {
	byID := map[uuid.UUID]*group{}
	var order []uuid.UUID
	get := func(id uuid.UUID) *group {
		g := byID[id]
		if g == nil {
			g = &group{id: id}
			byID[id] = g
			order = append(order, id)
		}
		return g
	}
	candidate := func(id uuid.UUID) bool {
		return (a.cx.Server.Held || a.cx.Server.Unmanaged[id]) && !a.cx.Server.Current[id]
	}
	for _, c := range a.inv.Containers {
		raw := c.Labels[agentprotocol.LabelService]
		if raw == "" || helperContainer(c) {
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			a.warn("Container %s has the service label %q, which is not a service ID; it is left alone.", c.Name, raw)
			continue
		}
		if !candidate(id) {
			continue
		}
		g := get(id)
		if _, ok := c.Labels[agentprotocol.LabelTailnet]; ok {
			g.tailnet++
			continue
		}
		g.cs = append(g.cs, c)
	}
	for _, r := range a.inv.Routes {
		if r.System {
			continue
		}
		id, err := uuid.Parse(ownerOf(r))
		if err != nil || !candidate(id) {
			continue
		}
		if g := byID[id]; g != nil {
			g.routes = append(g.routes, r)
			continue
		}
		if r.Kind == agentprotocol.RouteKindExternal {
			g := get(id)
			g.routeOnly = true
			g.routes = append(g.routes, r)
		}
	}
	out := make([]*group, 0, len(order))
	for _, id := range order {
		g := byID[id]
		if len(g.cs) > 0 {
			g.routeOnly = false
		}
		sort.SliceStable(g.cs, func(i, j int) bool {
			if !g.cs[i].Created.Equal(g.cs[j].Created) {
				return g.cs[i].Created.After(g.cs[j].Created)
			}
			return g.cs[i].Name < g.cs[j].Name
		})
		if len(g.cs) == 0 && !g.routeOnly {
			continue
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].id.String() < out[j].id.String() })
	return out
}

// unusedVolumes warns about managed volumes no container on the host mounts.
func (a *analyzer) unusedVolumes() {
	used := map[string]bool{}
	for _, c := range a.inv.Containers {
		for _, m := range c.Mounts {
			if m.Type == "volume" {
				used[m.Name] = true
			}
		}
	}
	n := 0
	for _, v := range a.inv.Volumes {
		if !used[v] {
			n++
		}
	}
	if n > 0 {
		a.warn("%d NebulaCtrl volume(s) on this host are not mounted by any container; they are left alone.", n)
	}
}

// envState is the placement of the services of one label environment.
type envState struct {
	id    uuid.UUID
	svcs  []*AdoptionService
	count int // adoptable services: orders the default names
}

// placeEnvironments puts every service in its environment, applies the
// environment choices and settles the action of each service.
func (a *analyzer) placeEnvironments(svcs []*AdoptionService) {
	byEnv := map[uuid.UUID]*envState{}
	var envOrder []uuid.UUID
	for _, ps := range svcs {
		var id uuid.UUID
		if ps.rec != nil {
			id = ps.rec.envID
		}
		st := byEnv[id]
		if st == nil {
			st = &envState{id: id}
			byEnv[id] = st
			envOrder = append(envOrder, id)
		}
		st.svcs = append(st.svcs, ps)
		if ps.Adoptable {
			st.count++
		}
	}
	sort.SliceStable(envOrder, func(i, j int) bool {
		x, y := byEnv[envOrder[i]], byEnv[envOrder[j]]
		if (x.id == uuid.Nil) != (y.id == uuid.Nil) {
			return y.id == uuid.Nil
		}
		if x.count != y.count {
			return x.count > y.count
		}
		return x.id.String() < y.id.String()
	})
	for id := range a.ch.Environments {
		if byEnv[id] == nil {
			a.warn("The choice for environment %s was ignored: no environment with this ID runs on this server.", id)
		}
	}

	pl := &projectPlacer{
		taken: map[string]bool{}, byID: map[uuid.UUID]*ContextProject{}, newSlugs: map[string]string{}, envSlugs: map[string]map[string]bool{},
	}
	for i := range a.cx.Projects {
		pl.taken[a.cx.Projects[i].Slug] = true
		pl.byID[a.cx.Projects[i].ID] = &a.cx.Projects[i]
	}
	rank := 0
	for _, id := range envOrder {
		st := byEnv[id]
		pe := AdoptionEnvironment{ID: id, Action: actNone, ProjectAction: actNone, Services: []AdoptionService{}}
		ce, exists := a.cx.Environments[id]
		switch {
		case id == uuid.Nil:
			pe.Name = "Unknown environment"
		case exists && !ce.SameTeam:
			pe.Name = "Environment of another team"
			for _, ps := range st.svcs {
				ps.Adoptable, ps.Reason = false, reasonOtherTeam
			}
		case exists:
			pe.Action, pe.ProjectAction = actExisting, actExisting
			pid := ce.ProjectID
			pe.ProjectID, pe.ProjectName, pe.ProjectSlug = &pid, ce.ProjectName, ce.ProjectSlug
			pe.Name, pe.Slug, pe.Production = ce.Name, ce.Slug, ce.Production
		default:
			pe.Action = actCreate
			rank++
			a.newEnvironment(&pe, a.ch.Environments[id], rank, pl)
		}
		a.settle(&pe, st, ce)
		if pe.Action == actCreate && !hasAdopted(st.svcs) {
			// nothing is created for an environment that keeps no service
			pe.Action, pe.ProjectAction, pe.ProjectID = actNone, actNone, nil
		}
		sort.SliceStable(st.svcs, func(i, j int) bool {
			if st.svcs[i].Slug != st.svcs[j].Slug {
				return st.svcs[i].Slug < st.svcs[j].Slug
			}
			return st.svcs[i].ID.String() < st.svcs[j].ID.String()
		})
		for _, ps := range st.svcs {
			pe.Services = append(pe.Services, *ps)
			switch ps.Action {
			case ActionAdopt, ActionReattach:
				a.plan.Totals.Adopt++
			case ActionLeave:
				a.plan.Totals.Leave++
			case ActionRemove:
				a.plan.Totals.Remove++
			}
		}
		a.plan.Environments = append(a.plan.Environments, pe)
	}
}

func hasAdopted(svcs []*AdoptionService) bool {
	for _, ps := range svcs {
		if ps.Action == ActionAdopt {
			return true
		}
	}
	return false
}

// projectPlacer tracks the slugs handed out while the environments are placed.
type projectPlacer struct {
	taken    map[string]bool               // project slugs: the team's, then the new ones
	byID     map[uuid.UUID]*ContextProject // the team's projects
	newSlugs map[string]string             // new project name → slug
	envSlugs map[string]map[string]bool    // project slug → environment slugs handed out
}

// newEnvironment names a new environment and picks its project.
func (a *analyzer) newEnvironment(pe *AdoptionEnvironment, ec AdoptionEnvironmentChoice, rank int, pl *projectPlacer) {
	pe.Name = "production"
	pe.Production = rank == 1
	if rank > 1 {
		pe.Name = fmt.Sprintf("environment-%d", rank)
	}
	if n := strings.TrimSpace(ec.Name); n != "" {
		pe.Name = n
	}
	if ec.Production != nil {
		pe.Production = *ec.Production
	}
	var projSlug string
	var taken []string
	if ec.ProjectID != nil {
		p := pl.byID[*ec.ProjectID]
		if p == nil {
			a.block("The project chosen for environment %s does not exist.", pe.ID)
			return
		}
		pid := p.ID
		pe.ProjectAction, pe.ProjectID, pe.ProjectName, pe.ProjectSlug = actExisting, &pid, p.Name, p.Slug
		projSlug = p.Slug
		taken = append(taken, p.EnvSlugs...)
	} else {
		name := strings.TrimSpace(ec.ProjectName)
		if name == "" {
			name = "Adopted from " + a.cx.Server.Name
		}
		slug, ok := pl.newSlugs[name]
		if !ok {
			list := make([]string, 0, len(pl.taken))
			for s := range pl.taken {
				list = append(list, s)
			}
			slug = projects.UniqueSlug(projects.Slugify(name, "project"), list)
			pl.taken[slug] = true
			pl.newSlugs[name] = slug
		}
		pe.ProjectAction, pe.ProjectName, pe.ProjectSlug = actCreate, name, slug
		projSlug = "new:" + slug
	}
	if pl.envSlugs[projSlug] == nil {
		pl.envSlugs[projSlug] = map[string]bool{}
	}
	for s := range pl.envSlugs[projSlug] {
		taken = append(taken, s)
	}
	slug := projects.UniqueSlug(projects.Slugify(pe.Name, "environment"), taken)
	for projects.CheckEnvironmentSlug(slug) != nil {
		slug = projects.UniqueSlug(slug+"-env", taken)
	}
	pe.Slug = slug
	pl.envSlugs[projSlug][slug] = true
}

// settle decides the action of every service of an environment and reports
// what stops the adoption.
func (a *analyzer) settle(pe *AdoptionEnvironment, st *envState, ce ContextEnv) {
	slugs := map[string]uuid.UUID{}
	for _, ps := range st.svcs {
		ch := a.ch.Services[ps.ID]
		switch ch.Action {
		case "", ActionAdopt, ActionLeave, ActionRemove:
		default:
			a.block("%s: %q is not an action (adopt, leave or remove).", ps.Name, ch.Action)
			ch.Action = ""
		}
		want := ch.Action
		if want == "" {
			want = ActionAdopt
			// A service that cannot be adopted stays running unmanaged, unless
			// this instance conflicts with it: that must be resolved by a choice.
			if !ps.Adoptable && ps.Reason != reasonOtherTeam {
				want = ActionLeave
			}
		}
		ps.Action = want
		if want != ActionAdopt {
			continue
		}
		if !ps.Adoptable {
			a.block("%s: %s", ps.Name, ps.Reason)
			continue
		}
		if _, ok := a.cx.Services[ps.ID]; ok {
			ps.Action = ActionReattach
			continue
		}
		if pe.Action == actExisting {
			if owner, taken := ce.ServiceSlugs[ps.Slug]; taken && owner != ps.ID {
				a.block("%s: the environment %s already has a service with the slug %s.", ps.Name, pe.Name, ps.Slug)
				continue
			}
		}
		if other, dup := slugs[ps.Slug]; dup && other != ps.ID {
			a.block("%s: another service of this environment has the slug %s.", ps.Name, ps.Slug)
			continue
		}
		slugs[ps.Slug] = ps.ID
	}
}
