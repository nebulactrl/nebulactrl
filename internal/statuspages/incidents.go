package statuspages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Incident impacts and statuses.
const (
	ImpactNone     = "none"
	ImpactMinor    = "minor"
	ImpactMajor    = "major"
	ImpactCritical = "critical"

	IncidentInvestigating = "investigating"
	IncidentIdentified    = "identified"
	IncidentMonitoring    = "monitoring"
	IncidentResolved      = "resolved"

	maxIncidentTitleLen = 120
	maxIncidentBodyLen  = 4000
)

var (
	impacts          = []string{ImpactNone, ImpactMinor, ImpactMajor, ImpactCritical}
	incidentStatuses = []string{IncidentInvestigating, IncidentIdentified, IncidentMonitoring, IncidentResolved}
)

// StatusIncidentView is an incident as the API shows it.
type StatusIncidentView struct {
	ID         uuid.UUID                  `json:"id"`
	Title      string                     `json:"title"`
	Impact     string                     `json:"impact" enum:"none,minor,major,critical"`
	Status     string                     `json:"status" enum:"investigating,identified,monitoring,resolved"`
	PageIDs    []uuid.UUID                `json:"pageIds" doc:"The status pages the incident is posted to"`
	ServiceIDs []uuid.UUID                `json:"serviceIds" doc:"The services it affects (shown as the affected components of each page that has them)"`
	Updates    []StatusIncidentUpdateView `json:"updates" doc:"The timeline, newest first"`
	CreatedAt  time.Time                  `json:"createdAt"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
	ResolvedAt *time.Time                 `json:"resolvedAt"`
}

// StatusIncidentUpdateView is one entry of an incident's timeline.
type StatusIncidentUpdateView struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status" enum:"investigating,identified,monitoring,resolved"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// IncidentCreate opens an incident; Body is its first update.
type IncidentCreate struct {
	Title      string
	Impact     string
	Status     string // default investigating
	Body       string
	PageIDs    []uuid.UUID
	ServiceIDs []uuid.UUID
}

// IncidentPatch changes an incident's header; nil fields stay as they are
// (an empty, non-nil list clears the pages or services).
type IncidentPatch struct {
	Title      *string
	Impact     *string
	PageIDs    []uuid.UUID
	ServiceIDs []uuid.UUID
}

// IncidentFilter narrows a list.
type IncidentFilter struct {
	Status string // "", a status, or "active" (not resolved)
	PageID *uuid.UUID
}

// IncidentUpdateInput posts a timeline entry.
type IncidentUpdateInput struct {
	Status string
	Body   string
}

func checkImpact(v string) error {
	if !slices.Contains(impacts, v) {
		return apierr.Unprocessable("invalid_impact", "impact must be none, minor, major or critical")
	}
	return nil
}

func checkIncidentStatus(v string) error {
	if !slices.Contains(incidentStatuses, v) {
		return apierr.Unprocessable("invalid_status", "status must be investigating, identified, monitoring or resolved")
	}
	return nil
}

// resolvedAtFor is the resolved_at of an incident in status: now once it is
// resolved (keeping an earlier time), nil otherwise (a reopened incident).
func resolvedAtFor(status string, prev *time.Time, now time.Time) *time.Time {
	if status != IncidentResolved {
		return nil
	}
	if prev != nil {
		return prev
	}
	return &now
}

// incidentViews builds the views of rows with their pages, services and timelines in three queries.
func incidentViews(ctx context.Context, q *db.Queries, rows []db.StatusIncident) ([]StatusIncidentView, error) {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	out := make([]StatusIncidentView, 0, len(rows))
	if len(ids) == 0 {
		return out, nil
	}
	pages, err := q.ListStatusIncidentPageIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list incident pages: %w", err)
	}
	svcs, err := q.ListStatusIncidentServiceIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list incident services: %w", err)
	}
	ups, err := q.ListStatusIncidentUpdates(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list incident updates: %w", err)
	}
	pageOf := map[uuid.UUID][]uuid.UUID{}
	for _, p := range pages {
		pageOf[p.IncidentID] = append(pageOf[p.IncidentID], p.PageID)
	}
	svcOf := map[uuid.UUID][]uuid.UUID{}
	for _, sv := range svcs {
		svcOf[sv.IncidentID] = append(svcOf[sv.IncidentID], sv.ServiceID)
	}
	upOf := map[uuid.UUID][]StatusIncidentUpdateView{}
	for _, u := range ups {
		upOf[u.IncidentID] = append(upOf[u.IncidentID], StatusIncidentUpdateView{ID: u.ID, Status: u.Status, Body: u.Body, CreatedAt: u.CreatedAt})
	}
	for _, r := range rows {
		v := StatusIncidentView{
			ID: r.ID, Title: r.Title, Impact: r.Impact, Status: r.Status,
			PageIDs: sortedIDs(pageOf[r.ID]), ServiceIDs: sortedIDs(svcOf[r.ID]), Updates: upOf[r.ID],
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ResolvedAt: r.ResolvedAt,
		}
		if v.Updates == nil {
			v.Updates = []StatusIncidentUpdateView{}
		}
		out = append(out, v)
	}
	return out, nil
}

// sortedIDs returns a sorted copy of ids (never nil).
func sortedIDs(ids []uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	if out == nil {
		out = []uuid.UUID{}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return out
}

func incidentView(ctx context.Context, q *db.Queries, row db.StatusIncident) (StatusIncidentView, error) {
	vs, err := incidentViews(ctx, q, []db.StatusIncident{row})
	if err != nil {
		return StatusIncidentView{}, err
	}
	return vs[0], nil
}

// checkLinks requires the pages and services to belong to the workspace.
func checkLinks(ctx context.Context, q *db.Queries, ws uuid.UUID, pageIDs, serviceIDs []uuid.UUID) error {
	if err := workspacePages(ctx, q, ws, pageIDs); err != nil {
		return err
	}
	_, err := workspaceServices(ctx, q, ws, serviceIDs)
	return err
}

// ListIncidents returns incidents, newest first.
func (s *Service) ListIncidents(ctx context.Context, f IncidentFilter, cursor string, limit int) (api.Page[StatusIncidentView], error) {
	ws, _, err := s.reader(ctx)
	if err != nil {
		return api.Page[StatusIncidentView]{}, err
	}
	at, id, ok, err := api.DecodeTimeCursor(cursor)
	if err != nil {
		return api.Page[StatusIncidentView]{}, err
	}
	params := db.ListStatusIncidentsParams{WorkspaceID: ws, PageID: f.PageID, RowLimit: int32(limit + 1)}
	switch f.Status {
	case "":
	case "active":
		active := true
		params.Active = &active
	default:
		if err := checkIncidentStatus(f.Status); err != nil {
			return api.Page[StatusIncidentView]{}, err
		}
		st := f.Status
		params.Status = &st
	}
	if ok {
		params.CursorCreatedAt, params.CursorID = &at, &id
	}
	rows, err := s.q.ListStatusIncidents(ctx, params)
	if err != nil {
		return api.Page[StatusIncidentView]{}, fmt.Errorf("list incidents: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = api.EncodeTimeCursor(last.CreatedAt, last.ID)
	}
	items, err := incidentViews(ctx, s.q, rows)
	if err != nil {
		return api.Page[StatusIncidentView]{}, err
	}
	return api.Page[StatusIncidentView]{Items: items, NextCursor: next}, nil
}

// GetIncident returns one incident.
func (s *Service) GetIncident(ctx context.Context, id uuid.UUID) (StatusIncidentView, error) {
	ws, _, err := s.reader(ctx)
	if err != nil {
		return StatusIncidentView{}, err
	}
	row, err := s.q.GetStatusIncident(ctx, db.GetStatusIncidentParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return StatusIncidentView{}, apierr.NotFound("incident")
	}
	if err != nil {
		return StatusIncidentView{}, fmt.Errorf("get incident: %w", err)
	}
	return incidentView(ctx, s.q, row)
}

// CreateIncident opens an incident whose first timeline entry is Body. A
// resolved incident is created already closed.
func (s *Service) CreateIncident(ctx context.Context, in IncidentCreate) (StatusIncidentView, error) {
	ws, p, err := s.writer(ctx)
	if err != nil {
		return StatusIncidentView{}, err
	}
	title, err := cleanLine("title", in.Title, 1, maxIncidentTitleLen)
	if err != nil {
		return StatusIncidentView{}, err
	}
	body, err := cleanText("body", in.Body, 1, maxIncidentBodyLen)
	if err != nil {
		return StatusIncidentView{}, err
	}
	if err := checkImpact(in.Impact); err != nil {
		return StatusIncidentView{}, err
	}
	status := in.Status
	if status == "" {
		status = IncidentInvestigating
	}
	if err := checkIncidentStatus(status); err != nil {
		return StatusIncidentView{}, err
	}
	pageIDs, serviceIDs := dedupe(in.PageIDs), dedupe(in.ServiceIDs)
	now := s.now().UTC()
	var row db.StatusIncident
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := checkLinks(ctx, q, ws, pageIDs, serviceIDs); err != nil {
			return err
		}
		var err error
		row, err = q.InsertStatusIncident(ctx, db.InsertStatusIncidentParams{
			WorkspaceID: ws, Title: title, Impact: in.Impact, Status: status, CreatedBy: userID(p), At: now,
			ResolvedAt: resolvedAtFor(status, nil, now),
		})
		if err != nil {
			return err
		}
		if _, err := q.InsertStatusIncidentUpdate(ctx, db.InsertStatusIncidentUpdateParams{
			IncidentID: row.ID, Status: status, Body: body, CreatedBy: userID(p), At: now,
		}); err != nil {
			return err
		}
		if err := setIncidentLinks(ctx, q, row.ID, pageIDs, serviceIDs); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionIncidentCreate, ResourceType: resourceIncident, ResourceID: row.ID.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "impact": row.Impact, "status": row.Status, "pages": len(pageIDs), "services": len(serviceIDs)},
		})
	})
	if err != nil {
		return StatusIncidentView{}, fail("create incident", err)
	}
	s.publish(ws, pageIDs...)
	return incidentView(ctx, s.q, row)
}

func setIncidentLinks(ctx context.Context, q *db.Queries, id uuid.UUID, pageIDs, serviceIDs []uuid.UUID) error {
	if pageIDs != nil {
		if err := q.DeleteStatusIncidentPages(ctx, id); err != nil {
			return fmt.Errorf("clear incident pages: %w", err)
		}
		for _, pg := range pageIDs {
			if err := q.InsertStatusIncidentPage(ctx, db.InsertStatusIncidentPageParams{IncidentID: id, PageID: pg}); err != nil {
				return fmt.Errorf("link incident page: %w", err)
			}
		}
	}
	if serviceIDs != nil {
		if err := q.DeleteStatusIncidentServices(ctx, id); err != nil {
			return fmt.Errorf("clear incident services: %w", err)
		}
		for _, sv := range serviceIDs {
			if err := q.InsertStatusIncidentService(ctx, db.InsertStatusIncidentServiceParams{IncidentID: id, ServiceID: sv}); err != nil {
				return fmt.Errorf("link incident service: %w", err)
			}
		}
	}
	return nil
}

// incidentPages returns the pages an incident is posted to.
func incidentPages(ctx context.Context, q *db.Queries, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListStatusIncidentPageIDs(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, fmt.Errorf("list incident pages: %w", err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.PageID
	}
	return out, nil
}

// UpdateIncident changes an incident's title, impact, pages or services. Pages
// it leaves are refreshed too.
func (s *Service) UpdateIncident(ctx context.Context, id uuid.UUID, in IncidentPatch) (StatusIncidentView, error) {
	ws, _, err := s.writer(ctx)
	if err != nil {
		return StatusIncidentView{}, err
	}
	var row db.StatusIncident
	var touched []uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetStatusIncidentForUpdate(ctx, db.GetStatusIncidentForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("incident")
		}
		if err != nil {
			return fmt.Errorf("get incident: %w", err)
		}
		title, impact := cur.Title, cur.Impact
		var changed []string
		if in.Title != nil {
			if title, err = cleanLine("title", *in.Title, 1, maxIncidentTitleLen); err != nil {
				return err
			}
			changed = append(changed, "title")
		}
		if in.Impact != nil {
			if err := checkImpact(*in.Impact); err != nil {
				return err
			}
			impact = *in.Impact
			changed = append(changed, "impact")
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
		if touched, err = incidentPages(ctx, q, id); err != nil {
			return err
		}
		row, err = q.UpdateStatusIncident(ctx, db.UpdateStatusIncidentParams{
			ID: id, WorkspaceID: ws, Title: title, Impact: impact, Status: cur.Status, ResolvedAt: cur.ResolvedAt, At: s.now().UTC(),
		})
		if err != nil {
			return err
		}
		if err := setIncidentLinks(ctx, q, id, pageIDs, serviceIDs); err != nil {
			return err
		}
		touched = append(touched, pageIDs...)
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionIncidentUpdate, ResourceType: resourceIncident, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "impact": row.Impact, "changed": changed},
		})
	})
	if err != nil {
		return StatusIncidentView{}, fail("update incident", err)
	}
	s.publish(ws, touched...)
	return incidentView(ctx, s.q, row)
}

// PostIncidentUpdate adds a timeline entry and moves the incident to its
// status. Resolved sets resolved_at; a later non-resolved update reopens the
// incident.
func (s *Service) PostIncidentUpdate(ctx context.Context, id uuid.UUID, in IncidentUpdateInput) (StatusIncidentView, error) {
	ws, p, err := s.writer(ctx)
	if err != nil {
		return StatusIncidentView{}, err
	}
	if err := checkIncidentStatus(in.Status); err != nil {
		return StatusIncidentView{}, err
	}
	body, err := cleanText("body", in.Body, 1, maxIncidentBodyLen)
	if err != nil {
		return StatusIncidentView{}, err
	}
	now := s.now().UTC()
	var row db.StatusIncident
	var pages []uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetStatusIncidentForUpdate(ctx, db.GetStatusIncidentForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("incident")
		}
		if err != nil {
			return fmt.Errorf("get incident: %w", err)
		}
		if _, err := q.InsertStatusIncidentUpdate(ctx, db.InsertStatusIncidentUpdateParams{
			IncidentID: id, Status: in.Status, Body: body, CreatedBy: userID(p), At: now,
		}); err != nil {
			return err
		}
		row, err = q.UpdateStatusIncident(ctx, db.UpdateStatusIncidentParams{
			ID: id, WorkspaceID: ws, Title: cur.Title, Impact: cur.Impact, Status: in.Status,
			ResolvedAt: resolvedAtFor(in.Status, cur.ResolvedAt, now), At: now,
		})
		if err != nil {
			return err
		}
		if pages, err = incidentPages(ctx, q, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionIncidentPost, ResourceType: resourceIncident, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": row.Title, "status": row.Status, "previousStatus": cur.Status},
		})
	})
	if err != nil {
		return StatusIncidentView{}, fail("post incident update", err)
	}
	s.publish(ws, pages...)
	return incidentView(ctx, s.q, row)
}

// DeleteIncident removes an incident with its timeline.
func (s *Service) DeleteIncident(ctx context.Context, id uuid.UUID) error {
	ws, _, err := s.writer(ctx)
	if err != nil {
		return err
	}
	var pages []uuid.UUID
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetStatusIncidentForUpdate(ctx, db.GetStatusIncidentForUpdateParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("incident")
		}
		if err != nil {
			return fmt.Errorf("get incident: %w", err)
		}
		if pages, err = incidentPages(ctx, q, id); err != nil {
			return err
		}
		if _, err := q.DeleteStatusIncident(ctx, db.DeleteStatusIncidentParams{ID: id, WorkspaceID: ws}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionIncidentDelete, ResourceType: resourceIncident, ResourceID: id.String(), WorkspaceID: ws,
			Metadata: map[string]any{"title": cur.Title, "status": cur.Status},
		})
	})
	if err != nil {
		return fail("delete incident", err)
	}
	s.publish(ws, pages...)
	return nil
}
