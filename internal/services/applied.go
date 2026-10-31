package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ProductionHasStaged reports whether s is a production service that runs a
// release and has staged changes. A git push must not ship those: it deploys
// the new commit with the configuration of the running release (see
// AppliedRelease).
func (s Scope) ProductionHasStaged() bool {
	return s.IsProduction && s.Service.CurrentDeploymentID != nil && s.Service.Generation > s.Service.AppliedGeneration
}

// AppliedRelease returns the release the service runs now: its applied
// configuration. ok is false when the service has no release.
func AppliedRelease(ctx context.Context, q *db.Queries, sv db.Service) (rel db.Release, ok bool, err error) {
	if sv.CurrentDeploymentID == nil {
		return db.Release{}, false, nil
	}
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sv.CurrentDeploymentID})
	if err != nil {
		return db.Release{}, false, fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ReleaseID == nil {
		return db.Release{}, false, nil
	}
	rel, err = q.GetReleaseByID(ctx, *deps[0].ReleaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Release{}, false, nil
	}
	if err != nil {
		return db.Release{}, false, fmt.Errorf("get release: %w", err)
	}
	return rel, true, nil
}

// AppliedScope returns s with the configuration of its running release in place
// of the live one, plus that release. ok is false when there is no release to
// take it from; s is returned unchanged then.
func AppliedScope(ctx context.Context, q *db.Queries, s Scope) (applied Scope, rel db.Release, ok bool, err error) {
	rel, ok, err = AppliedRelease(ctx, q, s.Service)
	if err != nil || !ok {
		return s, rel, false, err
	}
	raw, err := json.Marshal(DecodeSnapshot(rel.Config).Config)
	if err != nil {
		return s, rel, false, fmt.Errorf("encode applied config: %w", err)
	}
	applied = s
	applied.Service.Config = raw
	return applied, rel, true, nil
}
