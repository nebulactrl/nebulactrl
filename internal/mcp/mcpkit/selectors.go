package mcpkit

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Choice is one candidate of an ambiguous or unresolved selector.
type Choice struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // project | environment | service
	// Path is the human selector that resolves to exactly this resource:
	// "project", "project/env" or "project/env/service".
	Path string `json:"path"`
	Name string `json:"name"`
}

// Resolver turns human selectors into resources. Every lookup goes through the
// domain services under the caller's principal (scopes, project restriction),
// so a selector can never reveal a resource the caller cannot read.
//
// Selector forms:
//
//	project:     uuid | slug | name
//	environment: uuid | "project/env" | "project" (its single production env)
//	service:     uuid | "project/env/service" | hostname (a domain of the service)
//	             | bare service slug when unique across the caller's estate
//
// Ambiguity and misses return an *Error with code ambiguous_selector or
// not_found and Data {"choices":[Choice…]}.
type Resolver struct {
	Projects     *projects.Service
	Environments *environments.Service
	Services     *services.Service
	// Pool resolves hostnames; nil disables hostname selectors.
	Pool *pgxpool.Pool
}

// NewResolver builds a Resolver.
func NewResolver(p *projects.Service, e *environments.Service, s *services.Service, pool *pgxpool.Pool) *Resolver {
	return &Resolver{Projects: p, Environments: e, Services: s, Pool: pool}
}

const (
	maxChoices   = 8
	maxScanItems = 2000
)

func choicesErr(code, msg string, cs []Choice) *Error {
	if len(cs) > maxChoices {
		cs = cs[:maxChoices]
	}
	e := Errf(code, "%s", msg)
	if len(cs) > 0 {
		e.Data = map[string]any{"choices": cs}
	}
	return e
}

// Project resolves a project selector.
func (r *Resolver) Project(ctx context.Context, sel string) (projects.Project, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return projects.Project{}, Errf(CodeInvalidArgument, "project selector is empty")
	}
	if id, err := uuid.Parse(sel); err == nil {
		return r.Projects.Get(ctx, id)
	}
	all, err := r.allProjects(ctx)
	if err != nil {
		return projects.Project{}, err
	}
	var hits []projects.Project
	for _, p := range all {
		if strings.EqualFold(p.Slug, sel) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		for _, p := range all {
			if strings.EqualFold(p.Name, sel) {
				hits = append(hits, p)
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return projects.Project{}, choicesErr(CodeNotFound, "no project matches "+quote(sel), nearProjects(all, sel))
	}
	return projects.Project{}, choicesErr(CodeAmbiguous, "several projects match "+quote(sel)+"; use an id", projectChoices(hits))
}

// Environment resolves an environment selector.
func (r *Resolver) Environment(ctx context.Context, sel string) (environments.Environment, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return environments.Environment{}, Errf(CodeInvalidArgument, "environment selector is empty")
	}
	if id, err := uuid.Parse(sel); err == nil {
		return r.Environments.Get(ctx, id)
	}
	projSel, envSel, hasEnv := strings.Cut(sel, "/")
	proj, err := r.Project(ctx, projSel)
	if err != nil {
		return environments.Environment{}, err
	}
	envs, err := r.Environments.List(ctx, proj.ID)
	if err != nil {
		return environments.Environment{}, err
	}
	if !hasEnv || strings.TrimSpace(envSel) == "" {
		var prod, standard []environments.Environment
		for _, e := range envs {
			if e.Kind == "preview" {
				continue
			}
			standard = append(standard, e)
			if e.IsProduction {
				prod = append(prod, e)
			}
		}
		switch {
		case len(prod) == 1:
			return prod[0], nil
		case len(prod) == 0 && len(standard) == 1:
			return standard[0], nil
		}
		return environments.Environment{}, choicesErr(CodeAmbiguous,
			"project "+quote(proj.Slug)+" has no single production environment; name one as project/env", envChoices(proj, envs))
	}
	envSel = strings.TrimSpace(envSel)
	var hits []environments.Environment
	for _, e := range envs {
		if strings.EqualFold(e.Slug, envSel) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		for _, e := range envs {
			if strings.EqualFold(e.Name, envSel) {
				hits = append(hits, e)
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return environments.Environment{}, choicesErr(CodeNotFound, "no environment "+quote(envSel)+" in project "+quote(proj.Slug), envChoices(proj, envs))
	}
	return environments.Environment{}, choicesErr(CodeAmbiguous, "several environments match "+quote(envSel)+"; use an id", envChoices(proj, hits))
}

// Service resolves a service selector.
func (r *Resolver) Service(ctx context.Context, sel string) (services.ServiceView, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return services.ServiceView{}, Errf(CodeInvalidArgument, "service selector is empty")
	}
	if id, err := uuid.Parse(sel); err == nil {
		return r.Services.Get(ctx, id)
	}
	switch parts := strings.Split(sel, "/"); len(parts) {
	case 3:
		env, err := r.Environment(ctx, parts[0]+"/"+parts[1])
		if err != nil {
			return services.ServiceView{}, err
		}
		return r.serviceInEnv(ctx, env, strings.TrimSpace(parts[2]))
	case 2:
		// "project/service": only when the project has one production env.
		env, err := r.Environment(ctx, parts[0])
		if err != nil {
			return services.ServiceView{}, err
		}
		return r.serviceInEnv(ctx, env, strings.TrimSpace(parts[1]))
	case 1:
		if strings.Contains(sel, ".") {
			if v, ok, err := r.byHostname(ctx, sel); err != nil {
				return services.ServiceView{}, err
			} else if ok {
				return v, nil
			}
		}
		return r.bareSlug(ctx, sel)
	}
	return services.ServiceView{}, Errf(CodeInvalidArgument, "service selector %s must be an id, project/env/service or a hostname", quote(sel))
}

func (r *Resolver) serviceInEnv(ctx context.Context, env environments.Environment, slug string) (services.ServiceView, error) {
	svcs, err := r.listServices(ctx, env.ID)
	if err != nil {
		return services.ServiceView{}, err
	}
	var hits []services.ServiceView
	for _, s := range svcs {
		if strings.EqualFold(s.Slug, slug) {
			hits = append(hits, s)
		}
	}
	if len(hits) == 0 {
		for _, s := range svcs {
			if strings.EqualFold(s.Name, slug) {
				hits = append(hits, s)
			}
		}
	}
	proj := env.ProjectID.String()
	if p, err := r.Projects.Get(ctx, env.ProjectID); err == nil {
		proj = p.Slug
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		cs := make([]Choice, 0, len(svcs))
		for _, s := range svcs {
			cs = append(cs, Choice{ID: s.ID.String(), Kind: "service", Path: proj + "/" + env.Slug + "/" + s.Slug, Name: s.Name})
		}
		return services.ServiceView{}, choicesErr(CodeNotFound, "no service "+quote(slug)+" in "+proj+"/"+env.Slug, cs)
	}
	cs := make([]Choice, 0, len(hits))
	for _, s := range hits {
		cs = append(cs, Choice{ID: s.ID.String(), Kind: "service", Path: proj + "/" + env.Slug + "/" + s.Slug, Name: s.Name})
	}
	return services.ServiceView{}, choicesErr(CodeAmbiguous, "several services match "+quote(slug)+"; use an id", cs)
}

// byHostname finds the service owning a domain. The lookup is by exact
// hostname; the service itself is then loaded through its policy check, so a
// hostname of a service the caller cannot read behaves like a miss.
func (r *Resolver) byHostname(ctx context.Context, host string) (services.ServiceView, bool, error) {
	if r.Pool == nil {
		return services.ServiceView{}, false, nil
	}
	var id uuid.UUID
	err := r.Pool.QueryRow(ctx, `SELECT service_id FROM domains WHERE lower(hostname) = lower($1) LIMIT 1`, host).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return services.ServiceView{}, false, nil
	}
	if err != nil {
		return services.ServiceView{}, false, err
	}
	v, err := r.Services.Get(ctx, id)
	if err != nil {
		if ae, ok := apierr.As(err); ok && (ae.Status == 404 || ae.Status == 403) {
			return services.ServiceView{}, false, nil
		}
		return services.ServiceView{}, false, err
	}
	return v, true, nil
}

// bareSlug resolves a service slug (or name) across every visible project and
// environment; more than one hit is ambiguous.
func (r *Resolver) bareSlug(ctx context.Context, slug string) (services.ServiceView, error) {
	all, err := r.allProjects(ctx)
	if err != nil {
		return services.ServiceView{}, err
	}
	var hits []services.ServiceView
	var paths []string
	scanned := 0
	for _, p := range all {
		envs, err := r.Environments.List(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, e := range envs {
			svcs, err := r.listServices(ctx, e.ID)
			if err != nil {
				continue
			}
			for _, s := range svcs {
				scanned++
				if strings.EqualFold(s.Slug, slug) || strings.EqualFold(s.Name, slug) {
					hits = append(hits, s)
					paths = append(paths, p.Slug+"/"+e.Slug+"/"+s.Slug)
				}
				if scanned > maxScanItems {
					break
				}
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return services.ServiceView{}, Errf(CodeNotFound, "no service matches %s; use project/env/service, a hostname or an id (search_resources finds services by name)", quote(slug))
	}
	cs := make([]Choice, len(hits))
	for i, s := range hits {
		cs[i] = Choice{ID: s.ID.String(), Kind: "service", Path: paths[i], Name: s.Name}
	}
	return services.ServiceView{}, choicesErr(CodeAmbiguous, quote(slug)+" matches several services; use one of the paths in choices", cs)
}

func (r *Resolver) allProjects(ctx context.Context) ([]projects.Project, error) {
	var out []projects.Project
	cursor := ""
	for range 20 {
		items, next, err := r.Projects.List(ctx, cursor, MaxPage)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

func (r *Resolver) listServices(ctx context.Context, envID uuid.UUID) ([]services.ServiceView, error) {
	var out []services.ServiceView
	cursor := ""
	for range 20 {
		items, next, err := r.Services.List(ctx, envID, cursor, MaxPage)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

func quote(s string) string { return `"` + s + `"` }

func projectChoices(ps []projects.Project) []Choice {
	cs := make([]Choice, len(ps))
	for i, p := range ps {
		cs[i] = Choice{ID: p.ID.String(), Kind: "project", Path: p.Slug, Name: p.Name}
	}
	return cs
}

func envChoices(p projects.Project, es []environments.Environment) []Choice {
	cs := make([]Choice, len(es))
	for i, e := range es {
		cs[i] = Choice{ID: e.ID.String(), Kind: "environment", Path: p.Slug + "/" + e.Slug, Name: e.Name}
	}
	return cs
}

// nearProjects suggests projects whose slug or name contains, or is contained
// in, the selector; falls back to the first few.
func nearProjects(all []projects.Project, sel string) []Choice {
	s := strings.ToLower(sel)
	var near []projects.Project
	for _, p := range all {
		sl, nm := strings.ToLower(p.Slug), strings.ToLower(p.Name)
		if strings.Contains(sl, s) || strings.Contains(nm, s) || strings.Contains(s, sl) {
			near = append(near, p)
		}
	}
	if len(near) == 0 {
		near = all
	}
	sort.SliceStable(near, func(i, j int) bool { return near[i].Slug < near[j].Slug })
	return projectChoices(near)
}
