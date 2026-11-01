package svcalerts

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// ActionSettingsUpdate is the audit action of a threshold change.
const ActionSettingsUpdate = "notifications.alert_settings_update"

// IncidentView is one incident of a service.
type IncidentView struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind" enum:"unhealthy,crashed,restart_loop" doc:"The worst kind the incident reached."`
	Flapping   bool       `json:"flapping" doc:"The incident absorbed repeated failures; alerts for the service were paused until it was stable."`
	Detail     string     `json:"detail"`
	Restarts   int        `json:"restarts" doc:"Docker restarts counted when the incident was last updated."`
	OpenedAt   time.Time  `json:"openedAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	Resolution string     `json:"resolution" enum:",recovered,stopped,deleted" doc:"Empty while the incident is open."`
}

// GetSettings returns the workspace's alert thresholds.
func (s *Service) GetSettings(ctx context.Context) (SettingsView, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return SettingsView{}, err
	}
	cur, err := LoadSettings(ctx, s.pool, policy.WorkspaceID(ctx))
	if err != nil {
		return SettingsView{}, fmt.Errorf("load alert settings: %w", err)
	}
	return cur.View(), nil
}

// UpdateSettings replaces the workspace's alert thresholds.
func (s *Service) UpdateSettings(ctx context.Context, v SettingsView) (SettingsView, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return SettingsView{}, err
	}
	if err := v.Validate(); err != nil {
		return SettingsView{}, err
	}
	ws := policy.WorkspaceID(ctx)
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO service_alert_settings
			(workspace_id, crash_grace_seconds, unhealthy_grace_seconds, loop_restarts, loop_window_minutes,
			 recover_after_seconds, flap_incidents, flap_window_minutes, backup_grace_minutes, log_lines)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (workspace_id) DO UPDATE SET
			 crash_grace_seconds = EXCLUDED.crash_grace_seconds, unhealthy_grace_seconds = EXCLUDED.unhealthy_grace_seconds,
			 loop_restarts = EXCLUDED.loop_restarts, loop_window_minutes = EXCLUDED.loop_window_minutes,
			 recover_after_seconds = EXCLUDED.recover_after_seconds, flap_incidents = EXCLUDED.flap_incidents,
			 flap_window_minutes = EXCLUDED.flap_window_minutes, backup_grace_minutes = EXCLUDED.backup_grace_minutes,
			 log_lines = EXCLUDED.log_lines, updated_at = now()`,
			ws, v.CrashGraceSeconds, v.UnhealthyGraceSeconds, v.LoopRestarts, v.LoopWindowMinutes,
			v.RecoverAfterSeconds, v.FlapIncidents, v.FlapWindowMinutes, v.BackupGraceMinutes, v.LogLines); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSettingsUpdate, ResourceType: "notification_alert_settings", ResourceID: ws.String(),
			WorkspaceID: ws, Metadata: map[string]any{
				"crashGraceSeconds": v.CrashGraceSeconds, "unhealthyGraceSeconds": v.UnhealthyGraceSeconds,
				"loopRestarts": v.LoopRestarts, "loopWindowMinutes": v.LoopWindowMinutes,
				"recoverAfterSeconds": v.RecoverAfterSeconds, "flapIncidents": v.FlapIncidents,
				"flapWindowMinutes": v.FlapWindowMinutes, "backupGraceMinutes": v.BackupGraceMinutes, "logLines": v.LogLines,
			},
		})
	})
	if err != nil {
		return SettingsView{}, fmt.Errorf("save alert settings: %w", err)
	}
	s.forgetSettings(ws)
	return v, nil
}

// ListIncidents returns a service's incidents, newest first.
func (s *Service) ListIncidents(ctx context.Context, serviceID uuid.UUID, limit int) ([]IncidentView, error) {
	sc, err := services.LoadScope(ctx, db.New(s.pool), policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, kind, flapping, detail, restarts, opened_at, resolved_at, resolution
		FROM service_incidents WHERE service_id = $1 ORDER BY opened_at DESC LIMIT $2`, sc.Service.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()
	out := []IncidentView{}
	for rows.Next() {
		var v IncidentView
		if err := rows.Scan(&v.ID, &v.Kind, &v.Flapping, &v.Detail, &v.Restarts, &v.OpenedAt, &v.ResolvedAt, &v.Resolution); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type settingsOutput struct{ Body SettingsView }

type updateSettingsInput struct{ Body SettingsView }

type listIncidentsInput struct {
	ServiceID uuid.UUID `path:"serviceId"`
	Limit     int       `query:"limit" minimum:"1" maximum:"200" doc:"Default 50."`
}

type listIncidentsOutput struct {
	Body struct {
		Items []IncidentView `json:"items"`
	}
}

// Register attaches the alert endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceAlertSettings", Method: http.MethodGet, Path: "/notifications/alert-settings", Tags: []string{"notifications"},
		Summary:     "Get the service alert thresholds",
		Description: "How long a service must be down, unhealthy or restarting before service.crashed, service.unhealthy and service.restart_loop are sent, when an incident closes, and when a backup counts as missed. Which channel receives which event is the channel's own event selection.",
		Errors:      errs[:2],
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*settingsOutput, error) {
		v, err := svc.GetSettings(ctx)
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServiceAlertSettings", Method: http.MethodPut, Path: "/notifications/alert-settings", Tags: []string{"notifications"},
		Summary:     "Set the service alert thresholds",
		Description: "Replaces every threshold. Takes effect within a minute.",
		Errors:      errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateSettingsInput) (*settingsOutput, error) {
		v, err := svc.UpdateSettings(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServiceIncidents", Method: http.MethodGet, Path: "/services/{serviceId}/incidents", Tags: []string{"services"},
		Summary:     "List a service's health incidents",
		Description: "Stretches of time in which the service crashed, was unhealthy or restart-looped, newest first. An open incident has no resolvedAt.",
		Errors:      errs[:3],
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listIncidentsInput) (*listIncidentsOutput, error) {
		items, err := svc.ListIncidents(ctx, in.ServiceID, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &listIncidentsOutput{}
		out.Body.Items = items
		return out, nil
	})
}
