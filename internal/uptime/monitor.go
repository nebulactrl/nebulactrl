package uptime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Bounds of the thresholds (the same as the uptime_monitors CHECK constraints).
const (
	MinThreshold = 1
	MaxThreshold = 20
)

// MaxReminderMinutes bounds the reminder interval (one day); 0 turns reminders off.
const MaxReminderMinutes = 1440

// MonitorView is the uptime monitor of a service. A service without a row is
// monitored with the defaults.
type MonitorView struct {
	Enabled           bool `json:"enabled" doc:"Probe the service. When off, agents stop probing it and its state is paused."`
	Alerts            bool `json:"alerts" doc:"Send uptime.down and uptime.up notifications for outages of this service. Outages are recorded either way."`
	FailureThreshold  int  `json:"failureThreshold" minimum:"1" maximum:"20" doc:"Consecutive failed checks (30 s apart) that open an outage."`
	RecoveryThreshold int  `json:"recoveryThreshold" minimum:"1" maximum:"20" doc:"Consecutive passing checks that end it."`
	ReminderMinutes   int  `json:"reminderMinutes" minimum:"0" maximum:"1440" doc:"While an outage lasts, send uptime.down again every this many minutes (counted from the last alert). 0 = never. Needs alerts on; not sent during maintenance."`
}

// Validate rejects thresholds outside their bounds.
func (v MonitorView) Validate() error {
	if v.FailureThreshold < MinThreshold || v.FailureThreshold > MaxThreshold {
		return apierr.Unprocessable("invalid_failure_threshold", fmt.Sprintf("failureThreshold must be between %d and %d", MinThreshold, MaxThreshold))
	}
	if v.RecoveryThreshold < MinThreshold || v.RecoveryThreshold > MaxThreshold {
		return apierr.Unprocessable("invalid_recovery_threshold", fmt.Sprintf("recoveryThreshold must be between %d and %d", MinThreshold, MaxThreshold))
	}
	if v.ReminderMinutes < 0 || v.ReminderMinutes > MaxReminderMinutes {
		return apierr.Unprocessable("invalid_reminder_minutes", fmt.Sprintf("reminderMinutes must be between 0 and %d", MaxReminderMinutes))
	}
	return nil
}

func defaultMonitor() MonitorView {
	d := DefaultSettings()
	return MonitorView{Enabled: d.Enabled, Alerts: d.Alerts, FailureThreshold: d.FailureThreshold, RecoveryThreshold: d.RecoveryThreshold}
}

func monitorView(m db.UptimeMonitor) MonitorView {
	return MonitorView{
		Enabled: m.Enabled, Alerts: m.Alerts, FailureThreshold: int(m.FailureThreshold),
		RecoveryThreshold: int(m.RecoveryThreshold), ReminderMinutes: int(m.ReminderMinutes),
	}
}

// loadMonitor returns a service's monitor (the defaults without a row).
func loadMonitor(ctx context.Context, q *db.Queries, serviceID uuid.UUID) (MonitorView, error) {
	m, err := q.GetUptimeMonitor(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultMonitor(), nil
	}
	if err != nil {
		return MonitorView{}, fmt.Errorf("get uptime monitor: %w", err)
	}
	return monitorView(m), nil
}

// GetMonitor returns the uptime monitor of a service. Needs projects:read.
func (s *Service) GetMonitor(ctx context.Context, serviceID uuid.UUID) (MonitorView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return MonitorView{}, err
	}
	return loadMonitor(ctx, s.q, sc.Service.ID)
}

// UpdateMonitor replaces the uptime monitor of a service. Needs the admin
// scope (workspace owner or admin). Switching monitoring off pauses the
// service and closes its open outage without a recovery notice; switching it on
// returns it to unknown until its first check. The server's agent learns the
// change through the next Reconcile, which is pushed at once.
func (s *Service) UpdateMonitor(ctx context.Context, serviceID uuid.UUID, v MonitorView) (MonitorView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeAdmin, serviceID)
	if err != nil {
		return MonitorView{}, err
	}
	if err := v.Validate(); err != nil {
		return MonitorView{}, err
	}
	now := s.now().UTC()
	var toggled, stateChanged bool
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		toggled, stateChanged = false, false
		q := db.New(tx)
		prev, err := loadMonitor(ctx, q, sc.Service.ID)
		if err != nil {
			return err
		}
		if _, err := q.UpsertUptimeMonitor(ctx, db.UpsertUptimeMonitorParams{
			ServiceID: sc.Service.ID, WorkspaceID: sc.WorkspaceID, Enabled: v.Enabled, Alerts: v.Alerts,
			FailureThreshold: int32(v.FailureThreshold), RecoveryThreshold: int32(v.RecoveryThreshold), ReminderMinutes: int32(v.ReminderMinutes),
		}); err != nil {
			return fmt.Errorf("save uptime monitor: %w", err)
		}
		if prev.Enabled != v.Enabled {
			toggled = true
			if stateChanged, err = s.toggle(ctx, q, sc, v.Enabled, now); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionMonitorUpdate, ResourceType: "service", ResourceID: sc.Service.ID.String(),
			ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: map[string]any{
				"service": sc.Service.Name, "enabled": v.Enabled, "alerts": v.Alerts,
				"failureThreshold": v.FailureThreshold, "recoveryThreshold": v.RecoveryThreshold, "reminderMinutes": v.ReminderMinutes,
			},
		})
	})
	if err != nil {
		return MonitorView{}, err
	}
	if stateChanged {
		s.publish(sc.ProjectID, sc.EnvironmentID, sc.Service.ID)
	}
	if toggled && s.resync != nil && sc.Service.ServerID != nil {
		if err := s.resync.Resync(context.WithoutCancel(ctx), *sc.Service.ServerID); err != nil {
			s.log.WarnContext(ctx, "resync uptime settings", "server_id", sc.Service.ServerID.String(), "err", err)
		}
	}
	return v, nil
}

// toggle applies switching monitoring on or off to the service's state and
// open outage inside tx. It reports whether the state changed.
func (s *Service) toggle(ctx context.Context, q *db.Queries, sc services.Scope, enabled bool, now time.Time) (bool, error) {
	id := sc.Service.ID
	if err := q.EnsureUptimeState(ctx, db.EnsureUptimeStateParams{ServiceID: id, WorkspaceID: sc.WorkspaceID, Since: now}); err != nil {
		return false, fmt.Errorf("ensure uptime state: %w", err)
	}
	rows, err := q.LockUptimeStates(ctx, []uuid.UUID{id})
	if err != nil {
		return false, fmt.Errorf("lock uptime state: %w", err)
	}
	if len(rows) == 0 {
		return false, errors.New("lock uptime state: row missing")
	}
	st := stateOf(rows[0])
	var next State
	if enabled {
		next = Resume(st, now)
	} else {
		// The outage (if any) is closed here, so Pause has nothing to resolve.
		next, _ = Pause(st, now)
		if err := q.ResolveOpenUptimeIncident(ctx, db.ResolveOpenUptimeIncidentParams{ServiceID: id, ResolvedAt: now}); err != nil {
			return false, fmt.Errorf("close uptime incident: %w", err)
		}
	}
	if next.State == st.State {
		return false, nil
	}
	if err := q.SaveUptimeState(ctx, saveParams(id, next)); err != nil {
		return false, fmt.Errorf("save uptime state: %w", err)
	}
	return true, nil
}

// UptimeConfigFor returns the Reconcile.Uptime setting of a server: the
// services its agent must not probe. *Service implements the reconciler's
// UptimeSource with it.
func (s *Service) UptimeConfigFor(ctx context.Context, serverID uuid.UUID) (*agentprotocol.UptimeSettings, error) {
	ids, err := s.q.ListDisabledUptimeServices(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list disabled uptime services: %w", err)
	}
	out := &agentprotocol.UptimeSettings{}
	for _, id := range ids {
		out.Disabled = append(out.Disabled, id.String())
	}
	return out, nil
}
