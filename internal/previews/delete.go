package previews

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// runDelete tears a preview down: every service is deleted with its volumes
// (the agent removes the containers and volumes through remove_service) and
// its domains, then the environment itself. A service whose deployment is
// still running keeps the teardown waiting (retry = true). It is idempotent.
func (s *Service) runDelete(ctx context.Context, id uuid.UUID, reason string) (retry bool, err error) {
	pv, err := s.q.GetPreview(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sys := s.sysCtx(ctx, pv)
	rows, err := s.serviceRows(ctx, pv.EnvironmentID)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if active, err := s.q.GetActiveDeployment(ctx, r.ID); err == nil {
			_ = s.Deployer.Cancel(sys, active.ID) // only possible before the agent started it
		}
		err := s.Services.Delete(sys, r.ID, services.DeleteInput{Confirm: r.Slug, DeleteVolumes: true})
		switch {
		case err == nil:
		case isCode(err, "deployment_in_progress"):
			retry = true
		case isCode(err, "not_found"):
		default:
			return false, fmt.Errorf("delete service %s: %w", r.Slug, err)
		}
	}
	if retry {
		return true, nil
	}

	s.finalComment(ctx, pv, reason)

	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.DeletePreviewEnvironment(ctx, pv.EnvironmentID); err != nil {
			return err
		}
		return audit.Record(sys, tx, audit.Entry{
			Action: "preview.delete", ResourceType: "environment", ResourceID: pv.EnvironmentID.String(), ProjectID: &pv.ProjectID,
			Metadata: map[string]any{"pr": pv.PrNumber, "repo": pv.Repo, "reason": reason}, WorkspaceID: pv.WorkspaceID,
		})
	})
	if err != nil {
		return false, fmt.Errorf("delete preview environment: %w", err)
	}
	if s.Bus != nil {
		s.publish(pv)
	}
	s.emit(ctx, notifications.Event{
		Type: notifications.TypePreviewDeleted, ProjectID: &pv.ProjectID, Severity: notifications.SeverityInfo,
		Title:  fmt.Sprintf("Preview of PR #%d deleted", pv.PrNumber),
		Body:   deleteMessage(reason),
		Fields: map[string]string{"Pull request": fmt.Sprintf("#%d", pv.PrNumber), "Repository": pv.Repo},
	})
	return false, nil
}

// Teardown removes a preview now, with the work the delete job does: its
// services (containers and volumes through the agent), its domains and its
// environment. Deleting a project uses it, because the project's rows are gone
// as soon as the call returns and a queued job would find nothing to remove.
// It fails with 409 deployment_in_progress while a deployment of the preview
// still runs; retry once it has finished.
func (s *Service) Teardown(ctx context.Context, id uuid.UUID) error {
	retry, err := s.runDelete(ctx, id, "manual")
	if err != nil {
		return err
	}
	if retry {
		return apierr.Conflict("deployment_in_progress", "a deployment of a preview is in progress; wait for it to finish or cancel it")
	}
	return nil
}
