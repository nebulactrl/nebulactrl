package statuspages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Phases of a maintenance window.
const (
	PhaseUpcoming = "upcoming"
	PhaseActive   = "active"
	PhaseEnded    = "ended"
	PhaseCanceled = "canceled"

	maxWindowTitleLen       = 120
	maxWindowDescriptionLen = 2000
)

// MaintenanceWindowView is a maintenance window as the API shows it.
type MaintenanceWindowView struct {
	ID          uuid.UUID   `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	StartsAt    time.Time   `json:"startsAt"`
	EndsAt      time.Time   `json:"endsAt"`
	CanceledAt  *time.Time  `json:"canceledAt"`
	Phase       string      `json:"phase" enum:"upcoming,active,ended,canceled"`
	PageIDs     []uuid.UUID `json:"pageIds" doc:"The status pages that announce the window"`
	ServiceIDs  []uuid.UUID `json:"serviceIds" doc:"The services in maintenance while it is active: no uptime alerts, excluded from availability"`
	CreatedAt   time.Time   `json:"createdAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
}

// MaintenanceCreate schedules a window.
type MaintenanceCreate struct {
	Title       string
	Description string
	StartsAt    time.Time
	EndsAt      time.Time
	ServiceIDs  []uuid.UUID
	PageIDs     []uuid.UUID
}

// MaintenancePatch changes a window; nil fields stay as they are (an empty,
// non-nil list clears the services or pages).
type MaintenancePatch struct {
	Title       *string
	Description *string
	StartsAt    *time.Time
	EndsAt      *time.Time
	ServiceIDs  []uuid.UUID
	PageIDs     []uuid.UUID
}

// MaintenanceFilter narrows a list.
type MaintenanceFilter struct {
	Phase  string // "", upcoming, active or past (ended or canceled)
	PageID *uuid.UUID
}

// PhaseOf is where a window is at now.
func PhaseOf(canceledAt *time.Time, startsAt, endsAt, now time.Time) string {
	switch {
	case canceledAt != nil:
		return PhaseCanceled
	case !now.Before(endsAt):
		return PhaseEnded
	case !now.Before(startsAt):
		return PhaseActive
	}
	return PhaseUpcoming
}

func (s *Service) maintenanceViews(ctx context.Context, q *db.Queries, rows []db.MaintenanceWindow) ([]MaintenanceWindowView, error) {
	out := make([]MaintenanceWindowView, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	pages, err := q.ListMaintenanceWindowPageIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list window pages: %w", err)
	}
	svcs, err := q.ListMaintenanceWindowServiceIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list window services: %w", err)
	}
	pageOf := map[uuid.UUID][]uuid.UUID{}
	for _, p := range pages {
		pageOf[p.WindowID] = append(pageOf[p.WindowID], p.PageID)
	}
	svcOf := map[uuid.UUID][]uuid.UUID{}
	for _, sv := range svcs {
		svcOf[sv.WindowID] = append(svcOf[sv.WindowID], sv.ServiceID)
	}
	now := s.now()
	for _, r := range rows {
		out = append(out, MaintenanceWindowView{
			ID: r.ID, Title: r.Title, Description: r.Description, StartsAt: r.StartsAt, EndsAt: r.EndsAt, CanceledAt: r.CanceledAt,
			Phase: PhaseOf(r.CanceledAt, r.StartsAt, r.EndsAt, now), PageIDs: sortedIDs(pageOf[r.ID]), ServiceIDs: sortedIDs(svcOf[r.ID]),
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) maintenanceView(ctx context.Context, q *db.Queries, row db.MaintenanceWindow) (MaintenanceWindowView, error) {
	vs, err := s.maintenanceViews(ctx, q, []db.MaintenanceWindow{row})
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	return vs[0], nil
}

func setWindowLinks(ctx context.Context, q *db.Queries, id uuid.UUID, pageIDs, serviceIDs []uuid.UUID) error {
	if pageIDs != nil {
		if err := q.DeleteMaintenanceWindowPages(ctx, id); err != nil {
			return fmt.Errorf("clear window pages: %w", err)
		}
		for _, pg := range pageIDs {
			if err := q.InsertMaintenanceWindowPage(ctx, db.InsertMaintenanceWindowPageParams{WindowID: id, PageID: pg}); err != nil {
				return fmt.Errorf("link window page: %w", err)
			}
		}
	}
	if serviceIDs != nil {
		if err := q.DeleteMaintenanceWindowServices(ctx, id); err != nil {
			return fmt.Errorf("clear window services: %w", err)
		}
		for _, sv := range serviceIDs {
			if err := q.InsertMaintenanceWindowService(ctx, db.InsertMaintenanceWindowServiceParams{WindowID: id, ServiceID: sv}); err != nil {
				return fmt.Errorf("link window service: %w", err)
			}
		}
	}
	return nil
}

func windowPages(ctx context.Context, q *db.Queries, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListMaintenanceWindowPageIDs(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, fmt.Errorf("list window pages: %w", err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.PageID
	}
	return out, nil
}

// checkWindowTimes requires a window that ends after it starts and has not
// ended yet.
func checkWindowTimes(startsAt, endsAt, now time.Time) error {
	if !endsAt.After(startsAt) {
		return apierr.Unprocessable("invalid_window", "endsAt must be after startsAt")
	}
	if !endsAt.After(now) {
		return apierr.Unprocessable("window_in_past", "the window must end in the future")
	}
	return nil
}

// ListMaintenance returns windows, latest start first.
func (s *Service) ListMaintenance(ctx context.Context, f MaintenanceFilter, cursor string, limit int) (api.Page[MaintenanceWindowView], error) {
	ws, _, err := s.reader(ctx)
	if err != nil {
		return api.Page[MaintenanceWindowView]{}, err
	}
	at, id, ok, err := api.DecodeTimeCursor(cursor)
	if err != nil {
		return api.Page[MaintenanceWindowView]{}, err
	}
	params := db.ListMaintenanceWindowsParams{WorkspaceID: ws, PageID: f.PageID, AsOf: s.now().UTC(), RowLimit: int32(limit + 1)}
	switch f.Phase {
	case "":
	case PhaseUpcoming, PhaseActive, "past":
		ph := f.Phase
		params.Phase = &ph
	default:
		return api.Page[MaintenanceWindowView]{}, apierr.Unprocessable("invalid_phase", "phase must be upcoming, active or past")
	}
	if ok {
		params.CursorStartsAt, params.CursorID = &at, &id
	}
	rows, err := s.q.ListMaintenanceWindows(ctx, params)
	if err != nil {
		return api.Page[MaintenanceWindowView]{}, fmt.Errorf("list maintenance windows: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = api.EncodeTimeCursor(last.StartsAt, last.ID)
	}
	items, err := s.maintenanceViews(ctx, s.q, rows)
	if err != nil {
		return api.Page[MaintenanceWindowView]{}, err
	}
	return api.Page[MaintenanceWindowView]{Items: items, NextCursor: next}, nil
}

// GetMaintenance returns one window.
func (s *Service) GetMaintenance(ctx context.Context, id uuid.UUID) (MaintenanceWindowView, error) {
	ws, _, err := s.reader(ctx)
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	row, err := s.q.GetMaintenanceWindow(ctx, db.GetMaintenanceWindowParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return MaintenanceWindowView{}, apierr.NotFound("maintenance window")
	}
	if err != nil {
		return MaintenanceWindowView{}, fmt.Errorf("get maintenance window: %w", err)
	}
	return s.maintenanceView(ctx, s.q, row)
}

// CreateMaintenance schedules a window. While it is active its services are in
// maintenance (no uptime alerts, out of the availability) and the pages it is
// posted to announce it.
func (s *Service) CreateMaintenance(ctx context.Context, in MaintenanceCreate) (MaintenanceWindowView, error) {
	ws, p, err := s.writer(ctx)
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	title, err := cleanLine("title", in.Title, 1, maxWindowTitleLen)
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	desc, err := cleanText("description", in.Description, 0, maxWindowDescriptionLen)
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	starts, ends := in.StartsAt.UTC(), in.EndsAt.UTC()
	now := s.now().UTC()
	if err := checkWindowTimes(starts, ends, now); err != nil {
		return MaintenanceWindowView{}, err
	}
	pageIDs, serviceIDs := dedupe(in.PageIDs), dedupe(in.ServiceIDs)
	var row db.MaintenanceWindow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := checkLinks(ctx, q, ws, pageIDs, serviceIDs); err != nil {
			return err
		}
		var err error
		row, err = q.InsertMaintenanceWindow(ctx, db.InsertMaintenanceWindowParams{
			WorkspaceID: ws, Title: title, Description: desc, StartsAt: starts, EndsAt: ends, CreatedBy: userID(p), At: now,
		})
		if err != nil {
			return err
		}
		if err := setWindowLinks(ctx, q, row.ID, pageIDs, serviceIDs); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionMaintenanceNew, ResourceType: resourceMaintenance, ResourceID: row.ID.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "startsAt": row.StartsAt, "endsAt": row.EndsAt, "pages": len(pageIDs), "services": len(serviceIDs)},
		})
	})
	if err != nil {
		return MaintenanceWindowView{}, fail("create maintenance window", err)
	}
	s.publish(ws, pageIDs...)
	return s.maintenanceView(ctx, s.q, row)
}

// UpdateMaintenance changes a window that has not ended or been canceled.
func (s *Service) UpdateMaintenance(ctx context.Context, id uuid.UUID, in MaintenancePatch) (MaintenanceWindowView, error) {
	ws, _, err := s.writer(ctx)
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	now := s.now().UTC()
	var row db.MaintenanceWindow
	var touched []uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetMaintenanceWindowForUpdate(ctx, db.GetMaintenanceWindowForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("maintenance window")
		}
		if err != nil {
			return fmt.Errorf("get maintenance window: %w", err)
		}
		if ph := PhaseOf(cur.CanceledAt, cur.StartsAt, cur.EndsAt, now); ph == PhaseCanceled || ph == PhaseEnded {
			return apierr.Conflict("window_"+ph, "a "+ph+" maintenance window can no longer be changed")
		}
		title, desc, starts, ends := cur.Title, cur.Description, cur.StartsAt, cur.EndsAt
		var changed []string
		if in.Title != nil {
			if title, err = cleanLine("title", *in.Title, 1, maxWindowTitleLen); err != nil {
				return err
			}
			changed = append(changed, "title")
		}
		if in.Description != nil {
			if desc, err = cleanText("description", *in.Description, 0, maxWindowDescriptionLen); err != nil {
				return err
			}
			changed = append(changed, "description")
		}
		if in.StartsAt != nil {
			starts = in.StartsAt.UTC()
			changed = append(changed, "startsAt")
		}
		if in.EndsAt != nil {
			ends = in.EndsAt.UTC()
			changed = append(changed, "endsAt")
		}
		if err := checkWindowTimes(starts, ends, now); err != nil {
			return err
		}
		var pageIDs, serviceIDs []uuid.UUID
		if in.PageIDs != nil {
			pageIDs = dedupe(in.PageIDs)
			changed = append(changed, "pages")
		}
		if in.ServiceIDs != nil {
			serviceIDs = dedupe(in.ServiceIDs)
			changed = append(changed, "services")
		}
		if err := checkLinks(ctx, q, ws, pageIDs, serviceIDs); err != nil {
			return err
		}
		if touched, err = windowPages(ctx, q, id); err != nil {
			return err
		}
		row, err = q.UpdateMaintenanceWindow(ctx, db.UpdateMaintenanceWindowParams{
			ID: id, WorkspaceID: ws, Title: title, Description: desc, StartsAt: starts, EndsAt: ends, At: now,
		})
		if err != nil {
			return err
		}
		if err := setWindowLinks(ctx, q, id, pageIDs, serviceIDs); err != nil {
			return err
		}
		touched = append(touched, pageIDs...)
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionMaintenanceEdit, ResourceType: resourceMaintenance, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "changed": changed},
		})
	})
	if err != nil {
		return MaintenanceWindowView{}, fail("update maintenance window", err)
	}
	s.publish(ws, touched...)
	return s.maintenanceView(ctx, s.q, row)
}

// CancelMaintenance ends a window now: it is never announced or applied again.
// Canceling a canceled window changes nothing; an ended one is 409.
func (s *Service) CancelMaintenance(ctx context.Context, id uuid.UUID) (MaintenanceWindowView, error) {
	ws, _, err := s.writer(ctx)
	if err != nil {
		return MaintenanceWindowView{}, err
	}
	now := s.now().UTC()
	var row db.MaintenanceWindow
	var pages []uuid.UUID
	canceled := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetMaintenanceWindowForUpdate(ctx, db.GetMaintenanceWindowForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("maintenance window")
		}
		if err != nil {
			return fmt.Errorf("get maintenance window: %w", err)
		}
		switch PhaseOf(cur.CanceledAt, cur.StartsAt, cur.EndsAt, now) {
		case PhaseCanceled:
			row = cur
			return nil // already canceled: nothing changes
		case PhaseEnded:
			return apierr.Conflict("window_ended", "the maintenance window has already ended")
		}
		if row, err = q.CancelMaintenanceWindow(ctx, db.CancelMaintenanceWindowParams{ID: id, WorkspaceID: ws, At: now}); err != nil {
			return err
		}
		canceled = true
		if pages, err = windowPages(ctx, q, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionMaintenanceStop, ResourceType: resourceMaintenance, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "startsAt": row.StartsAt, "endsAt": row.EndsAt},
		})
	})
	if err != nil {
		return MaintenanceWindowView{}, fail("cancel maintenance window", err)
	}
	if canceled {
		s.publish(ws, pages...)
	}
	return s.maintenanceView(ctx, s.q, row)
}

// DeleteMaintenance removes a window.
func (s *Service) DeleteMaintenance(ctx context.Context, id uuid.UUID) error {
	ws, _, err := s.writer(ctx)
	if err != nil {
		return err
	}
	var pages []uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetMaintenanceWindowForUpdate(ctx, db.GetMaintenanceWindowForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("maintenance window")
		}
		if err != nil {
			return fmt.Errorf("get maintenance window: %w", err)
		}
		if pages, err = windowPages(ctx, q, id); err != nil {
			return err
		}
		if _, err := q.DeleteMaintenanceWindow(ctx, db.DeleteMaintenanceWindowParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionMaintenanceTrash, ResourceType: resourceMaintenance, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": cur.Title, "startsAt": cur.StartsAt, "endsAt": cur.EndsAt},
		})
	})
	if err != nil {
		return fail("delete maintenance window", err)
	}
	s.publish(ws, pages...)
	return nil
}
