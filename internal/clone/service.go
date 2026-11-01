package clone

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// ServiceInput describes a service clone.
type ServiceInput struct {
	// EnvironmentID is the environment of the clone (default: the source's).
	EnvironmentID *uuid.UUID
	// Name is the clone's name (default: "<name> copy" in the same environment,
	// the source's name in another one).
	Name string
	// ServerID places the clone (default: the source's server).
	ServerID *uuid.UUID
	// Domains is drop (default) or suffix.
	Domains string
	// DomainSuffix is added to the first label of each cloned hostname (default
	// "-copy" in the same environment, "-<environment slug>" in another one).
	DomainSuffix string
	// DryRun reports what the clone would do and changes nothing.
	DryRun bool
}

// CloneService clones one service into an environment of the same team. The
// caller needs write access to both the source and the target: a clone carries
// the source's secrets.
func (s *Service) CloneService(ctx context.Context, id uuid.UUID, in ServiceInput) (CloneResult, error) {
	res := newResult(in.DryRun)
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsWrite, id)
	if err != nil {
		return res, err
	}
	if sc.ProjectSystem {
		return res, apierr.Unprocessable("not_applicable", "services of the platform project cannot be cloned or moved")
	}
	if why := cloneable(sc.Service); why != "" {
		return res, apierr.Unprocessable("not_applicable", why)
	}
	targetID := sc.EnvironmentID
	if in.EnvironmentID != nil {
		targetID = *in.EnvironmentID
	}
	target, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, targetID)
	if err != nil {
		return res, err
	}
	if err := s.guardTarget(ctx, s.q, target); err != nil {
		return res, err
	}
	if !in.DryRun {
		if err := sc.Guard(ctx, "cloneService", fmt.Sprintf("Clone %s from production, with its secrets (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
			[]string{"The copy gets the source's secret values, git token, registry credentials and basic-auth hashes, in " + target.Env.Name + ", where people may run commands."},
			targetID, trimName(in.Name), in.Domains, in.DomainSuffix, uuidArg(in.ServerID)); err != nil {
			return res, err
		}
	}
	same := targetID == sc.EnvironmentID
	name := trimName(in.Name)
	if name == "" {
		name = sc.Service.Name
		if same {
			name += " copy"
		}
	}
	if len(name) > 100 {
		return res, apierr.Unprocessable("validation_failed", "name must be at most 100 characters")
	}
	mode, err := normalizeMode(in.Domains)
	if err != nil {
		return res, err
	}
	defSuffix := "-" + target.Env.Slug
	if same {
		defSuffix = "-copy"
	}
	suffix, err := normalizeSuffix(in.DomainSuffix, defSuffix)
	if err != nil {
		return res, err
	}

	var row db.Service
	err = s.runTx(ctx, in.DryRun, func(tx pgx.Tx) error {
		r := s.newRun(ctx, tx, target, mode, suffix, in.ServerID, &res)
		src, err := r.q.LockServiceRow(ctx, id)
		if err != nil {
			return fmt.Errorf("lock service: %w", err)
		}
		var ok bool
		if row, ok, err = r.createRow(src, name, false); err != nil || !ok {
			return err
		}
		// References to the clone's own slug follow the clone; references to
		// other services stay (they name the same siblings when the environment
		// is the same, and are checked against the target environment otherwise).
		if err := r.fill(src, row, map[string]string{src.Slug: row.Slug}); err != nil {
			return err
		}
		if same {
			n, err := r.copyOutgoingLinks(src.ID, row.ID)
			if err != nil {
				return err
			}
			res.Links = n
		} else if n, err := r.countLinks(src.EnvironmentID, src.ID); err != nil {
			return err
		} else if n > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%d link(s) to other services were not copied: they belong to the source's environment.", n))
		}
		if err := r.resolveRefs(); err != nil {
			return err
		}
		if !in.DryRun {
			if err := r.conflictError(); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "service.clone", ResourceType: "service", ResourceID: row.ID.String(), ProjectID: &target.Env.ProjectID,
			Metadata: map[string]any{"from": id.String(), "name": row.Name, "slug": row.Slug, "environment": target.Env.Slug, "domains": mode},
		})
	})
	if err != nil {
		if services.IsUniqueViolation(err) {
			return res, apierr.Conflict("slug_conflict", "a service with that name was just created; retry")
		}
		return res, err
	}
	if len(res.Services) == 0 { // skipped
		return res, apierr.Unprocessable("not_applicable", skipReason(res))
	}
	if in.DryRun {
		for i := range res.Services {
			res.Services[i].ID = uuid.Nil
		}
		return res, nil
	}
	nsc := services.ScopeOf(target, row)
	services.PublishService(s.Bus, services.EventServiceUpdated, nsc)
	view, err := s.Services.Get(ctx, row.ID)
	if err != nil {
		return res, err
	}
	res.Service = &view
	return res, nil
}

func skipReason(res CloneResult) string {
	if len(res.Skipped) > 0 {
		return res.Skipped[0].Reason
	}
	return "nothing was cloned"
}

// uuidArg renders an optional id for an approval digest.
func uuidArg(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// copyOutgoingLinks gives the clone the manual links the source has to other
// services of its environment.
func (r *run) copyOutgoingLinks(srcID, cloneID uuid.UUID) (int, error) {
	links, err := r.q.ListEnvironmentLinks(r.ctx, r.target.Env.ID)
	if err != nil {
		return 0, fmt.Errorf("list links: %w", err)
	}
	n := 0
	for _, l := range links {
		if l.FromServiceID != srcID {
			continue
		}
		if _, err := r.q.InsertLink(r.ctx, db.InsertLinkParams{EnvironmentID: r.target.Env.ID, FromServiceID: cloneID, ToServiceID: l.ToServiceID}); err != nil {
			return n, fmt.Errorf("clone link: %w", err)
		}
		n++
	}
	return n, nil
}

// countLinks counts the manual links a service of environment envID takes part in.
func (r *run) countLinks(envID, serviceID uuid.UUID) (int, error) {
	links, err := r.q.ListEnvironmentLinks(r.ctx, envID)
	if err != nil {
		return 0, fmt.Errorf("list links: %w", err)
	}
	n := 0
	for _, l := range links {
		if l.FromServiceID == serviceID || l.ToServiceID == serviceID {
			n++
		}
	}
	return n, nil
}

// EnvironmentInput describes an environment clone.
type EnvironmentInput struct {
	// Name of the new environment (required). It is never marked production.
	Name string
	// ServerID places every cloned service on one server (default: each service
	// keeps its server).
	ServerID *uuid.UUID
	// Domains is drop (default) or suffix.
	Domains string
	// DomainSuffix is added to the first label of each cloned hostname (default
	// "-<new environment slug>").
	DomainSuffix string
	// DryRun reports what the clone would do and changes nothing.
	DryRun bool
}

// CloneEnvironment copies an environment of a project into a new one: its
// services, variables (and the environment's shared variables), volumes,
// links and canvas layout. Slugs are kept, so a reference ${{postgres.DATABASE_URL}}
// in a cloned service points at the cloned postgres. The new environment is
// never a production environment.
func (s *Service) CloneEnvironment(ctx context.Context, envID uuid.UUID, in EnvironmentInput) (CloneResult, error) {
	res := newResult(in.DryRun)
	src, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, envID)
	if err != nil {
		return res, err
	}
	if err := s.guardTarget(ctx, s.q, src); err != nil {
		return res, err
	}
	if !in.DryRun && prodguard.Applies(ctx, src.Env.IsProduction) {
		targets := []prodguard.ApprovalTarget{services.GuardTarget("environment", src.Env.ID, src.Env.Name)}
		const op = "cloneEnvironment"
		if err := prodguard.Check(ctx, src.ProdScope(fmt.Sprintf("Clone the production environment %s, with its secrets", src.Env.Name), false, targets,
			"Every copy gets the source's secret values, git tokens, registry credentials and basic-auth hashes, in an environment people may run commands in.",
			"The new environment is called "+trimName(in.Name)+"."),
			op, prodguard.Digest(op, src.Env.ID, trimName(in.Name), in.Domains, in.DomainSuffix, uuidArg(in.ServerID))); err != nil {
			return res, err
		}
	}
	name := trimName(in.Name)
	if name == "" {
		return res, apierr.Unprocessable("validation_failed", "name is required")
	}
	if len(name) > 100 {
		return res, apierr.Unprocessable("validation_failed", "name must be at most 100 characters")
	}
	if err := projects.CheckEnvironmentSlug(projects.Slugify(name, "environment")); err != nil {
		return res, err
	}
	mode, err := normalizeMode(in.Domains)
	if err != nil {
		return res, err
	}
	var newEnv db.Environment
	var created []db.Service
	err = s.runTx(ctx, in.DryRun, func(tx pgx.Tx) error {
		q := db.New(tx)
		base := projects.Slugify(name, "environment")
		taken, err := q.ListEnvironmentSlugsLike(ctx, db.ListEnvironmentSlugsLikeParams{ProjectID: src.Env.ProjectID, Base: base})
		if err != nil {
			return err
		}
		newEnv, err = q.CreateEnvironment(ctx, db.CreateEnvironmentParams{
			ProjectID: src.Env.ProjectID, Name: name, Slug: projects.UniqueSlug(base, taken), IsProduction: false,
		})
		if err != nil {
			return err
		}
		suffix, err := normalizeSuffix(in.DomainSuffix, "-"+newEnv.Slug)
		if err != nil {
			return err
		}
		target := services.EnvScope{Env: newEnv, WorkspaceID: src.WorkspaceID, ProjectSlug: src.ProjectSlug}
		ref := envRef(newEnv)
		res.Environment = &ref
		r := s.newRun(ctx, tx, target, mode, suffix, in.ServerID, &res)

		rows, err := q.ListAllEnvironmentServices(ctx, src.Env.ID)
		if err != nil {
			return fmt.Errorf("list services: %w", err)
		}
		// Rows first, so every slug is known when the variables are copied.
		type pair struct{ src, row db.Service }
		var pairs []pair
		slugMap := map[string]string{}
		newID := map[uuid.UUID]uuid.UUID{}
		for _, b := range rows {
			row, ok, err := r.createRow(b, b.Name, true)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			pairs = append(pairs, pair{b, row})
			slugMap[b.Slug] = row.Slug
			newID[b.ID] = row.ID
			created = append(created, row)
		}
		for _, p := range pairs {
			if err := r.fill(p.src, p.row, slugMap); err != nil {
				return err
			}
		}
		links, err := q.ListEnvironmentLinks(ctx, src.Env.ID)
		if err != nil {
			return fmt.Errorf("list links: %w", err)
		}
		for _, l := range links {
			from, okF := newID[l.FromServiceID]
			to, okT := newID[l.ToServiceID]
			if !okF || !okT {
				continue
			}
			if _, err := q.InsertLink(ctx, db.InsertLinkParams{EnvironmentID: newEnv.ID, FromServiceID: from, ToServiceID: to}); err != nil {
				return fmt.Errorf("clone link: %w", err)
			}
			res.Links++
		}
		if res.SharedVariables, err = r.copySharedVariables(src.Env.ID); err != nil {
			return err
		}
		if err := canvas.CloneLayout(ctx, q, src.Env.ID, newEnv.ID, newID); err != nil {
			return err
		}
		if err := r.resolveRefs(); err != nil {
			return err
		}
		if !in.DryRun {
			if err := r.conflictError(); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "environment.clone", ResourceType: "environment", ResourceID: newEnv.ID.String(), ProjectID: &newEnv.ProjectID,
			Metadata: map[string]any{"from": src.Env.ID.String(), "name": newEnv.Name, "slug": newEnv.Slug, "services": len(created), "domains": mode},
		})
	})
	if err != nil {
		if projects.IsUniqueViolation(err) {
			return res, apierr.Conflict("slug_conflict", "an environment with that name was just created; retry")
		}
		return res, err
	}
	if in.DryRun {
		for i := range res.Services {
			res.Services[i].ID = uuid.Nil
		}
		res.Environment.ID = uuid.Nil
		return res, nil
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: events.TypeEnvironmentCreated, ProjectID: newEnv.ProjectID.String(), EnvironmentID: newEnv.ID.String(), ResourceID: newEnv.ID.String()})
	}
	target := services.EnvScope{Env: newEnv, WorkspaceID: src.WorkspaceID, ProjectSlug: src.ProjectSlug}
	for _, row := range created {
		services.PublishService(s.Bus, services.EventServiceUpdated, services.ScopeOf(target, row))
	}
	return res, nil
}

// copySharedVariables copies the environment-layer shared variables of from
// into the target environment, sealed again for their new rows. Generated ones
// are generated again.
func (r *run) copySharedVariables(from uuid.UUID) (int, error) {
	rows, err := r.q.ListEnvironmentSharedVariablesRaw(r.ctx, &from)
	if err != nil {
		return 0, fmt.Errorf("list environment shared variables: %w", err)
	}
	for _, v := range rows {
		id := uuid.New()
		valueEnc, err := r.reseal(secretbox.SharedVariablesValue, v.ID.String(), id.String(), v.ValueEnc)
		if err != nil {
			return 0, fmt.Errorf("copy shared variable %s: %w", v.Key, err)
		}
		templateEnc, err := r.reseal(secretbox.SharedVariablesTemplate, v.ID.String(), id.String(), v.TemplateEnc)
		if err != nil {
			return 0, fmt.Errorf("copy shared variable %s: %w", v.Key, err)
		}
		genCalls, secret := v.GenCalls, v.IsSecret
		if len(v.TemplateEnc) > 0 {
			tmpl, err := r.s.Box.OpenString(secretbox.SharedVariablesTemplate, v.ID.String(), v.TemplateEnc)
			if err != nil {
				return 0, fmt.Errorf("copy shared variable %s: %w", v.Key, err)
			}
			if m, err := varexpr.Materialize(tmpl); err == nil && m.Generated() {
				if valueEnc, err = r.s.Box.SealString(secretbox.SharedVariablesValue, id.String(), m.Stored); err != nil {
					return 0, err
				}
				secret = secret || m.Secret
				if genCalls, err = json.Marshal(m.Calls); err != nil {
					return 0, err
				}
			}
		}
		if err := r.q.CopySharedVariable(r.ctx, db.CopySharedVariableParams{
			ID: id, WorkspaceID: v.WorkspaceID, ProjectID: v.ProjectID, EnvironmentID: &r.target.Env.ID, Key: v.Key, ValueEnc: valueEnc,
			IsSecret: secret, Source: v.Source, TemplateEnc: templateEnc, GenCalls: genCalls, Comment: v.Comment,
		}); err != nil {
			return 0, fmt.Errorf("copy shared variable %s: %w", v.Key, err)
		}
	}
	return len(rows), nil
}
