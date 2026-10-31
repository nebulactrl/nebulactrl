package backups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// MinInterval is the shortest allowed gap between two scheduled backups.
const MinInterval = 5 * time.Minute

// ScheduleInput creates a schedule.
type ScheduleInput struct {
	DestinationID  uuid.UUID
	Cron           string
	Timezone       string
	Enabled        *bool
	RetentionCount *int
	RetentionDays  *int
}

// SchedulePatch updates a schedule; nil fields are unchanged.
type SchedulePatch struct {
	DestinationID  *uuid.UUID
	Cron           *string
	Timezone       *string
	Enabled        *bool
	RetentionCount *int
	RetentionDays  *int
}

// nextRun validates a cron expression and returns its next run after now.
func nextRun(expr, tz string, now time.Time) (time.Time, error) {
	sch, err := cronexpr.Parse(expr)
	if err != nil {
		return time.Time{}, apierr.Unprocessable("invalid_cron", err.Error())
	}
	loc, err := cronexpr.LoadLocation(tz)
	if err != nil {
		return time.Time{}, apierr.Unprocessable("invalid_timezone", err.Error())
	}
	first := sch.Next(now.In(loc))
	if first.IsZero() {
		return time.Time{}, apierr.Unprocessable("cron_never_fires", "the expression never matches a date")
	}
	if second := sch.Next(first); !second.IsZero() && second.Sub(first) < MinInterval {
		return time.Time{}, apierr.Unprocessable("cron_too_frequent", fmt.Sprintf("backups can run at most every %d minutes", int(MinInterval.Minutes())))
	}
	return first.UTC(), nil
}

func validRetention(count, days int) error {
	if count < 0 || count > 1000 || days < 0 || days > 3650 {
		return apierr.Unprocessable("validation_failed", "retentionCount must be 0-1000 and retentionDays 0-3650 (0 = no limit)")
	}
	return nil
}

// ListSchedules returns a database's schedules.
func (s *Service) ListSchedules(ctx context.Context, serviceID uuid.UUID) ([]ScheduleView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBackupSchedulesByService(ctx, sc.Service.ID)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	out := make([]ScheduleView, len(rows))
	for i, r := range rows {
		name := ""
		if d, err := s.q.GetBackupDestinationInternal(ctx, r.DestinationID); err == nil {
			name = d.Name
		}
		out[i] = scheduleView(r, name)
	}
	return out, nil
}

// CreateSchedule adds a recurring backup to a database.
func (s *Service) CreateSchedule(ctx context.Context, serviceID uuid.UUID, in ScheduleInput) (ScheduleView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return ScheduleView{}, err
	}
	if _, err := s.resolveTarget(ctx, s.q, sc); err != nil {
		return ScheduleView{}, err
	}
	dest, err := s.destination(ctx, s.q, sc.WorkspaceID, in.DestinationID)
	if err != nil {
		return ScheduleView{}, err
	}
	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}
	count, days, enabled := 7, 0, true
	if in.RetentionCount != nil {
		count = *in.RetentionCount
	}
	if in.RetentionDays != nil {
		days = *in.RetentionDays
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if err := validRetention(count, days); err != nil {
		return ScheduleView{}, err
	}
	next, err := nextRun(in.Cron, tz, s.now())
	if err != nil {
		return ScheduleView{}, err
	}
	if err := sc.Guard(ctx, guardCreateSchedule, fmt.Sprintf("Add a backup schedule to %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{
			"Backups start on the new schedule.",
			fmt.Sprintf("Schedule: %s (%s), enabled: %t, keep %d backups / %d days.", in.Cron, tz, enabled, count, days),
		}, dest.ID, in.Cron, tz, enabled, count, days); err != nil {
		return ScheduleView{}, err
	}
	var row db.BackupSchedule
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).InsertBackupSchedule(ctx, db.InsertBackupScheduleParams{
			WorkspaceID: sc.WorkspaceID, ServiceID: serviceID, DestinationID: dest.ID, Cron: in.Cron, Timezone: tz, Enabled: enabled,
			RetentionCount: int32(count), RetentionDays: int32(days), NextRunAt: &next,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionScheduleCreate, ResourceType: "backup_schedule", ResourceID: row.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "cron": in.Cron, "timezone": tz, "destination": dest.Name},
		})
	})
	if err != nil {
		return ScheduleView{}, fmt.Errorf("create schedule: %w", err)
	}
	s.publish(sc, EventBackupUpdated, row.ID)
	return scheduleView(row, dest.Name), nil
}

func (s *Service) scheduleScope(ctx context.Context, scope string, id uuid.UUID) (db.BackupSchedule, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.BackupSchedule{}, services.Scope{}, err
	}
	sch, err := s.q.GetBackupSchedule(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sch, services.Scope{}, apierr.NotFound("backup schedule")
	}
	if err != nil {
		return sch, services.Scope{}, fmt.Errorf("get schedule: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, scope, sch.ServiceID)
	if err != nil {
		return sch, sc, err
	}
	return sch, sc, nil
}

// UpdateSchedule changes a schedule. Changing the cron or timezone recomputes
// the next run; re-enabling resumes from now (missed runs are not replayed).
func (s *Service) UpdateSchedule(ctx context.Context, id uuid.UUID, p SchedulePatch) (ScheduleView, error) {
	cur, sc, err := s.scheduleScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return ScheduleView{}, err
	}
	destID, expr, tz, enabled := cur.DestinationID, cur.Cron, cur.Timezone, cur.Enabled
	count, days := int(cur.RetentionCount), int(cur.RetentionDays)
	if p.DestinationID != nil {
		destID = *p.DestinationID
	}
	if p.Cron != nil {
		expr = *p.Cron
	}
	if p.Timezone != nil {
		tz = *p.Timezone
		if tz == "" {
			tz = "UTC"
		}
	}
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	if p.RetentionCount != nil {
		count = *p.RetentionCount
	}
	if p.RetentionDays != nil {
		days = *p.RetentionDays
	}
	if err := validRetention(count, days); err != nil {
		return ScheduleView{}, err
	}
	dest, err := s.destination(ctx, s.q, sc.WorkspaceID, destID)
	if err != nil {
		return ScheduleView{}, err
	}
	next := cur.NextRunAt
	if expr != cur.Cron || tz != cur.Timezone || (enabled && !cur.Enabled) || next == nil {
		n, err := nextRun(expr, tz, s.now())
		if err != nil {
			return ScheduleView{}, err
		}
		next = &n
	}
	if err := sc.Guard(ctx, guardUpdateSchedule, fmt.Sprintf("Change the backup schedule of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
		[]string{
			"Backups and their retention follow the new settings at once; shorter retention deletes older backups.",
			fmt.Sprintf("Schedule: %s (%s), enabled: %t, keep %d backups / %d days.", expr, tz, enabled, count, days),
		}, id, destID, expr, tz, enabled, count, days); err != nil {
		return ScheduleView{}, err
	}
	var row db.BackupSchedule
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = db.New(tx).UpdateBackupSchedule(ctx, db.UpdateBackupScheduleParams{
			ID: id, DestinationID: destID, Cron: expr, Timezone: tz, Enabled: enabled,
			RetentionCount: int32(count), RetentionDays: int32(days), NextRunAt: next,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionScheduleUpdate, ResourceType: "backup_schedule", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "cron": expr, "timezone": tz, "enabled": enabled},
		})
	})
	if err != nil {
		return ScheduleView{}, fmt.Errorf("update schedule: %w", err)
	}
	s.publish(sc, EventBackupUpdated, id)
	return scheduleView(row, dest.Name), nil
}

// DeleteSchedule removes a schedule. Backups it made are kept (unmanaged by
// retention afterwards).
func (s *Service) DeleteSchedule(ctx context.Context, id uuid.UUID) error {
	_, sc, err := s.scheduleScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return err
	}
	if err := sc.Guard(ctx, guardDeleteSchedule, fmt.Sprintf("Delete the backup schedule of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), true,
		[]string{"Scheduled backups stop."}, id); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).DeleteBackupSchedule(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionScheduleDelete, ResourceType: "backup_schedule", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug},
		})
	})
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	s.publish(sc, EventBackupUpdated, id)
	return nil
}
