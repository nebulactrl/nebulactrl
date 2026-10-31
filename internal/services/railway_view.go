package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Desired states of a service (services.desired_state).
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// hasFailedStep reports whether a deployment status can carry a failed step.
func hasFailedStep(status string) bool {
	return status == "failed" || status == "rolled_back" || status == "rollback_failed"
}

// decorateViews fills the fields of the service resource that need extra
// queries: volumes, desired state, the failed step of the latest deployment
// and the staged-change summary. views[i] renders rows[i].
func decorateViews(ctx context.Context, q *db.Queries, box *secretbox.Box, rows []db.Service, views []ServiceView) error {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	vols, err := q.ListVolumesForServices(ctx, ids)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	volBySvc := map[uuid.UUID][]VolumeRef{}
	for _, v := range vols {
		volBySvc[v.ServiceID] = append(volBySvc[v.ServiceID], VolumeRef{ID: v.ID, Name: v.Name, MountPath: v.MountPath})
	}

	var failedDeps []uuid.UUID
	for _, v := range views {
		if v.LatestDeployment != nil && hasFailedStep(v.LatestDeployment.Status) {
			failedDeps = append(failedDeps, v.LatestDeployment.ID)
		}
	}
	failedStep := map[uuid.UUID]string{}
	if len(failedDeps) > 0 {
		steps, err := q.ListFailedDeploymentSteps(ctx, failedDeps)
		if err != nil {
			return fmt.Errorf("failed steps: %w", err)
		}
		for _, s := range steps {
			failedStep[s.DeploymentID] = s.Step
		}
	}

	// The step an active deployment is on (the LED strip's pre-deploy label).
	var activeDeps []uuid.UUID
	for _, v := range views {
		if v.LatestDeployment != nil && !hasFailedStep(v.LatestDeployment.Status) && v.LatestDeployment.FinishedAt == nil {
			activeDeps = append(activeDeps, v.LatestDeployment.ID)
		}
	}
	currentStep := map[uuid.UUID]string{}
	if len(activeDeps) > 0 {
		steps, err := q.ListCurrentDeploymentSteps(ctx, activeDeps)
		if err != nil {
			return fmt.Errorf("current steps: %w", err)
		}
		for _, s := range steps {
			if s.Status == "started" {
				currentStep[s.DeploymentID] = s.Step
			}
		}
	}

	// Staged changes: only services with generation > applied need a diff.
	releaseOf, err := currentReleases(ctx, q, rows)
	if err != nil {
		return err
	}
	cache := ResolverCache{}
	for i := range views {
		v, r := &views[i], rows[i]
		v.Volumes = volBySvc[r.ID]
		if v.Volumes == nil {
			v.Volumes = []VolumeRef{}
		}
		v.DesiredState = r.DesiredState
		if v.DesiredState == "" {
			v.DesiredState = DesiredRunning
		}
		if v.LatestDeployment != nil {
			if step, ok := failedStep[v.LatestDeployment.ID]; ok {
				v.LatestDeployment.FailedStep = &step
			}
			if step, ok := currentStep[v.LatestDeployment.ID]; ok {
				v.LatestDeployment.CurrentStep = &step
			}
		}
		if r.Generation <= r.AppliedGeneration {
			continue
		}
		kind := StagedEdited
		if r.CurrentDeploymentID == nil {
			kind = StagedNew
			v.PendingChangeCounts = PendingChangeCounts{Settings: 1}
		} else if rel, ok := releaseOf[r.ID]; ok && box != nil {
			cur, err := LoadCurrentStateCached(ctx, q, box, r, cache)
			if err != nil {
				return err
			}
			changes, err := DiffRelease(ctx, q, box, rel, cur)
			if err != nil {
				return err
			}
			v.PendingChangeCounts = CountChanges(changes)
			if len(changes) == 0 {
				// Discarding the last staged change bumps the generation but leaves
				// the service identical to its release: nothing is staged.
				v.HasStagedChanges = false
				continue
			}
		}
		v.StagedKind = &kind
	}
	return nil
}

// currentReleases maps each staged, deployed service to the release of its
// current deployment.
func currentReleases(ctx context.Context, q *db.Queries, rows []db.Service) (map[uuid.UUID]db.Release, error) {
	var depIDs []uuid.UUID
	for _, r := range rows {
		if r.Generation > r.AppliedGeneration && r.CurrentDeploymentID != nil {
			depIDs = append(depIDs, *r.CurrentDeploymentID)
		}
	}
	out := map[uuid.UUID]db.Release{}
	if len(depIDs) == 0 {
		return out, nil
	}
	deps, err := q.ListDeploymentsByIDs(ctx, depIDs)
	if err != nil {
		return nil, fmt.Errorf("current deployments: %w", err)
	}
	relIDs := make([]uuid.UUID, 0, len(deps))
	relByDep := map[uuid.UUID]uuid.UUID{}
	for _, d := range deps {
		if d.ReleaseID != nil {
			relIDs = append(relIDs, *d.ReleaseID)
			relByDep[d.ID] = *d.ReleaseID
		}
	}
	if len(relIDs) == 0 {
		return out, nil
	}
	rels, err := q.ListReleasesByIDs(ctx, relIDs)
	if err != nil {
		return nil, fmt.Errorf("current releases: %w", err)
	}
	byID := map[uuid.UUID]db.Release{}
	for _, r := range rels {
		byID[r.ID] = r
	}
	for _, r := range rows {
		if r.CurrentDeploymentID == nil {
			continue
		}
		if relID, ok := relByDep[*r.CurrentDeploymentID]; ok {
			if rel, ok := byID[relID]; ok {
				out[r.ID] = rel
			}
		}
	}
	return out, nil
}
