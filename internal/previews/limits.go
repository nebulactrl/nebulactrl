package previews

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// maxConcurrent returns the project's limit of running previews.
func (s *Service) maxConcurrent(ctx context.Context, projectID uuid.UUID) (int, error) {
	set, err := s.q.GetPreviewSettings(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultMaxConcurrent, nil
	}
	if err != nil {
		return 0, err
	}
	return int(set.MaxConcurrent), nil
}

// enforceLimit pauses the least recently pushed previews of the project until
// no more than the limit are running. except (the preview that was just
// created or resumed) is never paused; previews still deploying are not
// pausable either, so the limit may be exceeded briefly. Errors are logged:
// the next sweep tries again.
func (s *Service) enforceLimit(ctx context.Context, projectID, except uuid.UUID) {
	limit, err := s.maxConcurrent(ctx, projectID)
	if err != nil {
		s.Logger.ErrorContext(ctx, "previews: read limit", "project", projectID.String(), "err", err)
		return
	}
	running, err := s.q.CountRunningPreviews(ctx, projectID)
	if err != nil {
		s.Logger.ErrorContext(ctx, "previews: count running previews", "project", projectID.String(), "err", err)
		return
	}
	if int(running) <= limit {
		return
	}
	cands, err := s.q.ListPausablePreviews(ctx, db.ListPausablePreviewsParams{ProjectID: projectID, ExceptID: except})
	if err != nil {
		s.Logger.ErrorContext(ctx, "previews: list pausable previews", "project", projectID.String(), "err", err)
		return
	}
	for _, c := range cands {
		if int(running) <= limit {
			return
		}
		paused, err := s.pause(ctx, c.ID)
		if err != nil {
			s.Logger.ErrorContext(ctx, "previews: pause preview", "preview", c.ID.String(), "err", err)
			continue
		}
		if paused {
			running--
		}
	}
}

// pause stops the services of a preview and marks it paused. A push to its PR
// resumes it. It reports false when the preview was not pausable any more
// (deleted, deploying again, already paused).
func (s *Service) pause(ctx context.Context, id uuid.UUID) (bool, error) {
	var pv db.Preview
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetPreview(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.lock(ctx, tx, prKey(cur.ProjectID, cur.ConnectionID, cur.Repo, int(cur.PrNumber))); err != nil {
			return err
		}
		if cur, err = q.LockPreview(ctx, id); err != nil {
			return err
		}
		if cur.Status != StatusReady && cur.Status != StatusFailed {
			return nil
		}
		st := decodeState(cur.DeployState)
		st.Done = true
		if _, err := q.UpdateWave(ctx, db.UpdateWaveParams{ID: id, Seq: int32(st.Seq), State: encodeState(st)}); err != nil {
			return err
		}
		if pv, err = q.UpdatePreviewStatus(ctx, db.UpdatePreviewStatusParams{
			ID: id, Status: StatusPaused, StatusMessage: "Paused: the project reached its limit of running previews. A new push resumes it.",
		}); err != nil {
			return err
		}
		return audit.Record(systemCtx(ctx, pv.WorkspaceID, pv.ProjectID), tx, audit.Entry{
			Action: "preview.pause", ResourceType: "environment", ResourceID: pv.EnvironmentID.String(), ProjectID: &pv.ProjectID,
			Metadata: map[string]any{"pr": pv.PrNumber}, WorkspaceID: pv.WorkspaceID,
		})
	})
	if err != nil {
		return false, fmt.Errorf("pause preview: %w", err)
	}
	if pv.ID == uuid.Nil {
		return false, nil
	}
	sys := s.sysCtx(ctx, pv)
	rows, err := s.serviceRows(ctx, pv.EnvironmentID)
	if err != nil {
		return true, err
	}
	for _, r := range rows {
		if r.CurrentDeploymentID == nil {
			continue
		}
		if _, err := s.Services.Stop(sys, r.ID); err != nil {
			s.Logger.WarnContext(ctx, "previews: stop service of paused preview", "service", r.Slug, "preview", pv.ID.String(), "err", err)
		}
	}
	s.publish(pv)
	if err := s.enqueuePublish(ctx, pv.ID); err != nil {
		s.Logger.ErrorContext(ctx, "previews: enqueue publish", "preview", pv.ID.String(), "err", err)
	}
	return true, nil
}

// Sweep deletes previews that saw no push for the project's idle days and
// enforces the concurrency limit (it also applies a lowered limit).
func (s *Service) Sweep(ctx context.Context) error {
	idle, err := s.q.ListIdlePreviews(ctx)
	if err != nil {
		return fmt.Errorf("list idle previews: %w", err)
	}
	var errs []error
	for _, pv := range idle {
		if err := s.beginDelete(ctx, pv.ID, "idle"); err != nil {
			errs = append(errs, err)
		}
	}
	projects, err := s.q.ListProjectsWithPreviews(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list projects: %w", err))...)
	}
	for _, p := range projects {
		s.enforceLimit(ctx, p, uuid.Nil)
	}
	return errors.Join(errs...)
}
