package platform

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// restart puts a platform service back into provisioning at step and starts the job.
func (s *Service) restart(ctx context.Context, row db.PlatformService, step, msg string, action string) (db.PlatformService, error) {
	var cur db.PlatformService
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		cur, err = q.SetPlatformProgress(ctx, db.SetPlatformProgressParams{ID: row.ID, Status: StatusProvisioning, Step: step, StatusMessage: msg, DeploymentIds: row.DeploymentIds})
		if err != nil {
			return err
		}
		o := decodeJSON[options](cur.Options)
		now := s.Now()
		o.StepSince = &now
		if _, err := q.SetPlatformOptions(ctx, db.SetPlatformOptionsParams{ID: row.ID, Options: encodeJSON(o)}); err != nil {
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, ProvisionArgs{ID: row.ID}, nil); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "platform_service", ResourceID: row.ID.String(),
			Metadata: map[string]any{"kind": row.Kind, "step": step},
		})
	})
	if err != nil {
		return db.PlatformService{}, fmt.Errorf("restart provisioning: %w", err)
	}
	s.publish(cur)
	return cur, nil
}

// Retry resumes a failed platform service from the step that failed (a failed
// deployment is deployed again).
func (s *Service) Retry(ctx context.Context, id uuid.UUID) (PlatformServiceView, error) {
	row, err := s.load(ctx, id)
	if err != nil {
		return PlatformServiceView{}, err
	}
	if row.Status != StatusFailed {
		return PlatformServiceView{}, apierr.Conflict("not_failed", "Only a failed platform service can be retried.")
	}
	step := row.Step
	if step == StepAwait {
		step = StepDeploy
	}
	cur, err := s.restart(ctx, row, step, "Retrying", ActionRewire)
	if err != nil {
		return PlatformServiceView{}, err
	}
	return s.view(ctx, cur)
}

// Rewire runs the wiring of a ready Headscale again: it mints a new API key
// and updates the Tailscale integration.
func (s *Service) Rewire(ctx context.Context, id uuid.UUID) (PlatformServiceView, error) {
	row, err := s.load(ctx, id)
	if err != nil {
		return PlatformServiceView{}, err
	}
	if row.Status != StatusReady {
		return PlatformServiceView{}, apierr.Conflict("not_ready", "Only a ready platform service can be reconnected.")
	}
	cur, err := s.restart(ctx, row, StepWire, "Reconnecting", ActionRewire)
	if err != nil {
		return PlatformServiceView{}, err
	}
	return s.view(ctx, cur)
}

// ---- removal --------------------------------------------------------------------------

// RemoveInput confirms a removal.
type RemoveInput struct {
	// Confirm must equal the kind.
	Confirm string
	// DeleteData also deletes the data volumes (otherwise they stay on the server).
	DeleteData bool
}

// Remove unwires the integration the platform service fed, then deletes its
// services. An integration that is still in use (a backup destination with
// schedules, a tailnet in use) refuses with 409 and nothing is changed.
func (s *Service) Remove(ctx context.Context, id uuid.UUID, in RemoveInput) error {
	row, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if in.Confirm != row.Kind {
		return apierr.Unprocessable("confirm_mismatch", "confirm must equal the kind").WithDetails(map[string]any{"kind": row.Kind})
	}
	if err := s.unwire(ctx, row); err != nil {
		return err
	}
	cur, err := s.q.SetPlatformProgress(ctx, db.SetPlatformProgressParams{ID: row.ID, Status: StatusRemoving, Step: row.Step, StatusMessage: "Removing", DeploymentIds: row.DeploymentIds})
	if err != nil {
		return err
	}
	s.publish(cur)
	rows, err := s.q.ListServicesIncludingDeleted(ctx, row.ServiceIds)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.DeletedAt != nil {
			continue
		}
		if err := s.Services.Delete(ctx, r.ID, services.DeleteInput{Confirm: r.Slug, DeleteVolumes: in.DeleteData}); err != nil {
			if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
				continue
			}
			s.fail(ctx, cur, "Removal stopped: "+err.Error())
			return err
		}
	}
	return db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeletePlatformService(ctx, db.DeletePlatformServiceParams{ID: row.ID, WorkspaceID: row.WorkspaceID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRemove, ResourceType: "platform_service", ResourceID: row.ID.String(),
			Metadata: map[string]any{"kind": row.Kind, "deleteData": in.DeleteData},
		})
	})
}

// unwire removes the Tailscale integration Headscale created. Anything already
// gone is fine; an integration still in use refuses.
func (s *Service) unwire(ctx context.Context, row db.PlatformService) error {
	w := decodeJSON[wiring](row.Wiring)
	if w.Type != WiringTailnet {
		return nil
	}
	cur, err := s.Tailnet.Get(ctx)
	if err != nil {
		return err
	}
	// Only the integration this service created: a different one stays.
	if !cur.Configured || cur.Provider != "headscale" || strings.TrimRight(cur.LoginServer, "/") != strings.TrimRight(row.PublicUrl, "/") {
		return nil
	}
	if err := s.Tailnet.Delete(ctx); err != nil {
		if ae, ok := apierr.As(err); ok && ae.Status == http.StatusNotFound {
			return nil
		}
		return err
	}
	return nil
}
