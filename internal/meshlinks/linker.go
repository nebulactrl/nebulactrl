package meshlinks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Service is the services.MeshLinker: canvas edges, deletes and placement
// changes of the services domain call it.
var _ services.MeshLinker = (*Service)(nil)

// EdgeCreated opens the grants of a new canvas edge between services on
// different servers, in the edge's transaction. It does nothing for services on
// one server and when the plan has blockers (the consumer's deploy reports
// them). The caller (services.CreateLink) has checked acceptMeshChanges.
func (s *Service) EdgeCreated(ctx context.Context, tx pgx.Tx, envID, fromID, toID uuid.UUID) error {
	if s.mesh == nil {
		return nil
	}
	q := db.New(tx)
	w, ok, err := s.edgeWant(ctx, q, fromID, toID)
	if err != nil || !ok {
		return err
	}
	env, err := services.LoadEnvInfo(ctx, q, envID)
	if err != nil {
		return err
	}
	plan, err := s.PlanWants(ctx, q, env, []Want{w})
	if err != nil {
		return err
	}
	if len(plan.Blockers) > 0 {
		return nil
	}
	res, err := s.Reconcile(ctx, tx, envID, ModeAdd, Options{Edge: &[2]uuid.UUID{fromID, toID}})
	if err != nil {
		return err
	}
	if len(res.Blockers) > 0 {
		return fmt.Errorf("mesh link for %s -> %s blocked after planning: %s", w.ConsumerSlug, w.ProviderSlug, res.Blockers[0].Message)
	}
	return nil
}

// EdgeDeleted removes the grants a canvas edge opened, at once. Grants that a
// variable reference of the consumer still asks for are kept by the next repair.
func (s *Service) EdgeDeleted(ctx context.Context, tx pgx.Tx, envID, fromID, toID uuid.UUID) error {
	if s.mesh == nil {
		return nil
	}
	q := db.New(tx)
	exposures, err := q.ListMeshExposuresOfService(ctx, toID)
	if err != nil {
		return fmt.Errorf("list exposures: %w", err)
	}
	for _, ex := range exposures {
		grants, err := q.ListMeshGrantsOfExposure(ctx, ex.ID)
		if err != nil {
			return fmt.Errorf("list grants: %w", err)
		}
		for _, g := range grants {
			if g.Source == SourceEdge && g.RefID == fromID {
				if err := s.mesh.Revoke(ctx, tx, g.LinkID, g.ExposureID, g.RefID, g.Source); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ServiceDeleted drops every grant the service holds as a consumer and every
// exposure it has as a provider, in the delete's transaction.
func (s *Service) ServiceDeleted(ctx context.Context, tx pgx.Tx, envID, serviceID uuid.UUID) error {
	if s.mesh == nil {
		return nil
	}
	q := db.New(tx)
	if err := s.mesh.Release(ctx, tx, serviceID); err != nil {
		return err
	}
	exposures, err := q.ListMeshExposuresOfService(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("list exposures: %w", err)
	}
	for _, ex := range exposures {
		grants, err := q.ListMeshGrantsOfExposure(ctx, ex.ID)
		if err != nil {
			return fmt.Errorf("list grants: %w", err)
		}
		for _, g := range grants {
			if err := s.mesh.Revoke(ctx, tx, g.LinkID, g.ExposureID, g.RefID, g.Source); err != nil {
				return err
			}
		}
	}
	return nil
}

// Repair runs the removal-only reconciliation of an environment after a
// placement change or a delete (it never adds a device). Errors are logged: the
// periodic job repeats it.
func (s *Service) Repair(ctx context.Context, envID uuid.UUID) {
	if s.mesh == nil {
		return
	}
	if _, err := s.ReconcileEnv(ctx, envID, ModeRepair, Options{}); err != nil {
		s.logger.WarnContext(ctx, "mesh link repair", "environment_id", envID.String(), "err", err)
	}
}
