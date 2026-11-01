package clone

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// MoveInput describes a move of a service to another environment.
type MoveInput struct {
	// EnvironmentID is the target environment (required).
	EnvironmentID uuid.UUID
	// Force moves the service even though references or links break. The broken
	// references are listed in the result and fail the next deploy of the
	// services that hold them until they are fixed.
	Force bool
	// DryRun reports the plan and changes nothing.
	DryRun bool
}

// MoveIncomingRef is a service of the old environment that references the moved
// service.
type MoveIncomingRef struct {
	Service string   `json:"service"`
	Keys    []string `json:"keys"`
	id      uuid.UUID
}

// MoveResult is the plan or the outcome of a move.
type MoveResult struct {
	DryRun bool `json:"dryRun"`
	// Applied is true when the service was moved (never in a dry run).
	Applied bool `json:"applied"`
	// Blocked is true when references break and Force was not set.
	Blocked bool                `json:"blocked"`
	From    CloneEnvironmentRef `json:"from"`
	To      CloneEnvironmentRef `json:"to"`
	// Slug is the service's slug after the move; SlugChanged tells it differs
	// from the old one (the target environment already has a service with it).
	Slug        string `json:"slug"`
	SlugChanged bool   `json:"slugChanged"`
	// Outgoing are the references in the moved service's variables that stop
	// resolving (their service stays behind) or resolve to a different service.
	Outgoing []ReferenceFinding `json:"outgoing"`
	// Incoming are the services of the old environment that reference the moved
	// service; their references stop resolving.
	Incoming []MoveIncomingRef `json:"incoming"`
	// Links are the slugs the service was linked with on the canvas; the links are removed.
	Links    []string              `json:"links"`
	Warnings []string              `json:"warnings"`
	Service  *services.ServiceView `json:"service,omitempty"`
}

// MoveService moves a service to another environment. The move is staged: the
// service keeps running where it is until its next deployment, which joins the
// new environment's network and resolves its variables there. References and
// links are checked first: a move that breaks references needs Force, links
// are removed, and the services that referenced the moved one are marked as
// having staged changes.
func (s *Service) MoveService(ctx context.Context, id uuid.UUID, in MoveInput) (MoveResult, error) {
	res := MoveResult{DryRun: in.DryRun, Outgoing: []ReferenceFinding{}, Incoming: []MoveIncomingRef{}, Links: []string{}, Warnings: []string{}}
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, id)
	if err != nil {
		return res, err
	}
	target, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, in.EnvironmentID)
	if err != nil {
		return res, err
	}
	if target.Env.ID == sc.EnvironmentID {
		return res, apierr.Unprocessable("same_environment", "the service is already in that environment")
	}
	if sc.ProjectSystem {
		return res, apierr.Unprocessable("not_applicable", "services of the platform project cannot be cloned or moved")
	}
	switch sc.Service.Kind {
	case services.KindRoute, services.KindStack:
		return res, apierr.Unprocessable("not_applicable", "route services and Compose stacks cannot be moved: create them again in the target environment")
	}
	if err := s.guardTarget(ctx, s.q, target); err != nil {
		return res, err
	}
	from, err := s.q.GetEnvironmentScoped(ctx, db.GetEnvironmentScopedParams{ID: sc.EnvironmentID, WorkspaceID: policy.WorkspaceID(ctx)})
	if err != nil {
		return res, fmt.Errorf("get environment: %w", err)
	}
	if from.Kind == "preview" {
		return res, apierr.Unprocessable("preview_environment", "services of a pull-request preview cannot be moved out of it")
	}
	res.From, res.To = envRef(from), envRef(target.Env)

	if in.DryRun {
		err = s.planMove(ctx, s.q, sc, target, &res)
		return res, err
	}
	if err := s.guardMove(ctx, sc, target); err != nil {
		return res, err
	}
	var moved db.Service
	var dependents []uuid.UUID
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.LockServiceRow(ctx, id)
		if err != nil {
			return fmt.Errorf("lock service: %w", err)
		}
		sc.Service = cur
		if n, err := q.CountActiveDeployments(ctx, id); err != nil {
			return err
		} else if n > 0 {
			return apierr.Conflict("deployment_in_progress", "a deployment of this service is running; move it when it finishes")
		}
		res = MoveResult{DryRun: false, From: res.From, To: res.To, Outgoing: []ReferenceFinding{}, Incoming: []MoveIncomingRef{}, Links: []string{}, Warnings: []string{}}
		if err := s.planMove(ctx, q, sc, target, &res); err != nil {
			return err
		}
		if res.Blocked && !in.Force {
			return apierr.Conflict("move_blocked", "moving this service breaks references from or to it; review them and move again with force").
				WithDetails(map[string]any{"plan": res})
		}
		res.Blocked = false
		dependents = dependents[:0]
		for _, inc := range res.Incoming {
			dependents = append(dependents, inc.id)
		}
		if moved, err = q.SetServiceEnvironmentBumped(ctx, db.SetServiceEnvironmentBumpedParams{ID: id, EnvironmentID: target.Env.ID, Slug: res.Slug}); err != nil {
			return fmt.Errorf("move service: %w", err)
		}
		if _, err := q.DeleteServiceLinks(ctx, id); err != nil {
			return fmt.Errorf("remove links: %w", err)
		}
		// The file of the old environment no longer manages the service.
		if err := q.DeleteConfigSource(ctx, id); err != nil {
			return fmt.Errorf("drop config source: %w", err)
		}
		if res.SlugChanged {
			if err := s.rewriteOwnRefs(ctx, q, moved, sc.Service.Slug, res.Slug, services.ScopeOf(target, moved)); err != nil {
				return err
			}
		}
		if err := canvas.DropServiceTx(ctx, tx, from.ID, id); err != nil {
			return err
		}
		for _, d := range dependents {
			if _, err := q.BumpServiceGeneration(ctx, d); err != nil {
				return fmt.Errorf("stage dependent: %w", err)
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.move", ResourceType: "service", ResourceID: id.String(), ProjectID: &target.Env.ProjectID,
			Metadata: map[string]any{
				"from": from.Slug, "to": target.Env.Slug, "slug": res.Slug, "forced": in.Force,
				"brokenOutgoing": len(res.Outgoing), "brokenIncoming": len(res.Incoming), "linksRemoved": len(res.Links),
			},
		})
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return res, apierr.Conflict("slug_conflict", "a service with that name was just created in the target environment; retry")
		}
		return res, err
	}
	res.Applied = true
	nsc := services.ScopeOf(target, moved)
	services.PublishService(s.Bus, services.EventServiceUpdated, nsc)
	services.Publish(s.Bus, services.EventServiceUpdated, from.ProjectID, from.ID, id)
	services.Publish(s.Bus, services.EventCanvasUpdated, from.ProjectID, from.ID, from.ID)
	services.Publish(s.Bus, services.EventCanvasUpdated, target.Env.ProjectID, target.Env.ID, target.Env.ID)
	for _, d := range dependents {
		services.Publish(s.Bus, services.EventServiceUpdated, from.ProjectID, from.ID, d)
	}
	view, err := s.Services.Get(ctx, id)
	if err != nil {
		return res, err
	}
	res.Service = &view
	return res, nil
}

// guardMove asks the production guard before a service leaves or enters a
// production environment.
func (s *Service) guardMove(ctx context.Context, sc services.Scope, target services.EnvScope) error {
	const op = "move_service"
	effects := []string{
		"Stages a change of environment: the service joins the new environment's network and variables at its next deployment.",
		"Removes the service's canvas links.",
	}
	if prodguard.Applies(ctx, sc.IsProduction) {
		summary := fmt.Sprintf("Move %s out of production (%s / %s) to %s", sc.Service.Name, sc.ProjectName, sc.EnvName, target.Env.Name)
		if err := prodguard.Check(ctx, sc.ProdScope(summary, false, effects...), op, prodguard.Digest(op, sc.Service.ID, target.Env.ID)); err != nil {
			return err
		}
	}
	if prodguard.Applies(ctx, target.Env.IsProduction) {
		summary := fmt.Sprintf("Move %s into production (%s / %s)", sc.Service.Name, target.ProjectSlug, target.Env.Name)
		targets := []prodguard.ApprovalTarget{services.GuardTarget("service", sc.Service.ID, sc.Service.Name)}
		return prodguard.Check(ctx, target.ProdScope(summary, false, targets, effects...), op, prodguard.Digest(op, sc.Service.ID, target.Env.ID))
	}
	return nil
}

// planMove fills res with the checks and the outcome of a move: nothing is
// written. The service must not be a database with replica or mesh state.
func (s *Service) planMove(ctx context.Context, q *db.Queries, sc services.Scope, target services.EnvScope, res *MoveResult) error {
	svc := sc.Service
	if n, err := q.CountServiceMeshState(ctx, svc.ID); err != nil {
		return err
	} else if n > 0 {
		return apierr.Conflict("move_blocked", "the service takes part in a replica group or a cross-server link; remove those first").
			WithDetails(map[string]any{"reason": "mesh_or_replicas"})
	}
	oldSvcs, err := q.ListAllEnvironmentServices(ctx, sc.EnvironmentID)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	newSvcs, err := q.ListAllEnvironmentServices(ctx, target.Env.ID)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	oldSlugs, newSlugs := map[string]bool{}, map[string]bool{}
	nameByID := map[uuid.UUID]string{}
	for _, o := range oldSvcs {
		oldSlugs[o.Slug] = true
		nameByID[o.ID] = o.Slug
	}
	for _, n := range newSvcs {
		newSlugs[n.Slug] = true
	}

	// The slug the service gets in the target environment.
	taken, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: target.Env.ID, Base: svc.Slug})
	if err != nil {
		return err
	}
	taken = append(taken, services.ReservedSlugs...)
	res.Slug = svc.Slug
	for _, t := range taken {
		if t == svc.Slug {
			res.Slug, res.SlugChanged = projects.UniqueSlug(svc.Slug, taken), true
			break
		}
	}
	if res.SlugChanged {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s already has a service %q: the moved service is renamed to the slug %q.", target.Env.Name, svc.Slug, res.Slug))
	}

	// Outgoing references.
	oldShared, err := sharedKeys(ctx, q, sc.ProjectID, sc.EnvironmentID)
	if err != nil {
		return err
	}
	newShared, err := sharedKeys(ctx, q, target.Env.ProjectID, target.Env.ID)
	if err != nil {
		return err
	}
	vars, err := q.ListServiceVariables(ctx, svc.ID)
	if err != nil {
		return fmt.Errorf("list variables: %w", err)
	}
	for _, v := range vars {
		val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(svc.ID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return fmt.Errorf("open %s.%s: %w", svc.Slug, v.Key, err)
		}
		t, err := varexpr.Parse(val)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, ref := range t.Refs() {
			if seen[ref.String()] {
				continue
			}
			seen[ref.String()] = true
			f := ReferenceFinding{Service: svc.Slug, Key: v.Key, Ref: ref.String()}
			switch ref.NS {
			case "", varexpr.NSTeam:
				continue
			case varexpr.NSShared:
				if !oldShared[ref.Key] || newShared[ref.Key] {
					continue
				}
				f.Status, f.Message = RefUnresolved, "the shared variable "+ref.Key+" does not exist in "+target.Env.Name
			case svc.Slug:
				continue // a reference to itself follows the service
			default:
				switch {
				case !oldSlugs[ref.NS]:
					continue // already broken before the move
				case newSlugs[ref.NS]:
					f.Status, f.Message = RefRebound, "would resolve to the service "+ref.NS+" of "+target.Env.Name+", not to the one it points at now"
				default:
					f.Status, f.Message = RefUnresolved, ref.NS+" stays in "+res.From.Name+"; there is no service "+ref.NS+" in "+target.Env.Name
				}
			}
			res.Outgoing = append(res.Outgoing, f)
		}
	}

	// Incoming references.
	edges, err := services.ReferenceEdges(ctx, q, s.Box, sc.EnvironmentID)
	if err != nil {
		return err
	}
	for _, e := range edges {
		if e.To == svc.ID {
			res.Incoming = append(res.Incoming, MoveIncomingRef{Service: nameByID[e.From], Keys: e.Keys, id: e.From})
		}
	}

	// Manual links.
	links, err := q.ListEnvironmentLinks(ctx, sc.EnvironmentID)
	if err != nil {
		return fmt.Errorf("list links: %w", err)
	}
	for _, l := range links {
		switch svc.ID {
		case l.FromServiceID:
			res.Links = append(res.Links, nameByID[l.ToServiceID])
		case l.ToServiceID:
			res.Links = append(res.Links, nameByID[l.FromServiceID])
		}
	}
	sort.Strings(res.Links)
	if len(res.Links) > 0 {
		res.Warnings = append(res.Warnings, "Canvas links of the service are removed; draw them again in the new environment.")
	}
	if len(res.Incoming) > 0 || len(res.Outgoing) > 0 {
		res.Blocked = true
	}
	if res.From.ProjectID != res.To.ProjectID {
		res.Warnings = append(res.Warnings, "The target environment belongs to another project: project-level shared variables, git connections and team settings follow the new project.")
	}
	return nil
}

// rewriteOwnRefs rewrites references to the moved service's old slug in its own
// variables after it was renamed.
func (s *Service) rewriteOwnRefs(ctx context.Context, q *db.Queries, row db.Service, from, to string, sc services.Scope) error {
	vars, err := q.ListServiceVariables(ctx, row.ID)
	if err != nil {
		return fmt.Errorf("list variables: %w", err)
	}
	for _, v := range vars {
		val, err := s.Box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(row.ID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return fmt.Errorf("open %s: %w", v.Key, err)
		}
		nv, changed := varexpr.RewriteRefSlug(val, from, to)
		if !changed {
			continue
		}
		w := services.VarWrite{Key: v.Key, Value: nv, Secret: v.IsSecret, Scope: v.Scope, Source: v.Source}
		if len(v.TemplateEnc) > 0 {
			tmpl, err := s.Box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(row.ID.String(), v.Key), v.TemplateEnc)
			if err != nil {
				return fmt.Errorf("open template of %s: %w", v.Key, err)
			}
			w.Template, _ = varexpr.RewriteRefSlug(tmpl, from, to)
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.Box, sc, w); err != nil {
			return fmt.Errorf("rewrite %s: %w", v.Key, err)
		}
	}
	return nil
}
