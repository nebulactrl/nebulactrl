// Package audit records who did what, in the same transaction as the change.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Actor types stored in audit_events.actor_type.
const (
	ActorUser   = "user"
	ActorToken  = "token"
	ActorMCP    = "mcp"
	ActorSystem = "system"
)

// Entry describes one audited action. Secret changes must record the key only,
// never the value.
type Entry struct {
	Action       string
	ResourceType string
	ResourceID   string
	ProjectID    *uuid.UUID
	Metadata     map[string]any
	// WorkspaceID names the team explicitly (e.g. a team created by a user who
	// acts in another team). Otherwise it comes from the principal, else from
	// the project or resource of the row.
	WorkspaceID uuid.UUID
}

type actorTypeKey struct{}

// WithActorType overrides the recorded actor type (e.g. "mcp" for MCP calls
// that authenticate with a token principal).
func WithActorType(ctx context.Context, actorType string) context.Context {
	return context.WithValue(ctx, actorTypeKey{}, actorType)
}

type viaKey struct{}

// WithVia marks ctx so that every audit row recorded under it carries
// "via": via in its metadata (e.g. "nebula.toml" for project-file applies).
func WithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

// ViaFrom returns the marker set by WithVia, or "".
func ViaFrom(ctx context.Context) string {
	v, _ := ctx.Value(viaKey{}).(string)
	return v
}

// Record inserts an audit row using tx. The actor is taken from the principal
// in ctx; without a principal the actor is "system".
func Record(ctx context.Context, tx pgx.Tx, e Entry) error {
	q := db.New(tx)
	actorType, actorID, label := ActorSystem, "", "system"
	ws := e.WorkspaceID
	if p, ok := auth.PrincipalFrom(ctx); ok {
		actorType, actorID, label = p.Type, p.ID().String(), p.Label
		if p.Type == auth.TypeSystem {
			actorID = ""
		}
		switch actorType {
		case auth.TypeUser:
			actorType = ActorUser
		case auth.TypeMCP:
			actorType = ActorMCP
		case auth.TypeSystem:
			actorType = ActorSystem
		default:
			actorType = ActorToken
		}
		if ws == uuid.Nil {
			ws = p.WorkspaceID
		}
	}
	if t, ok := ctx.Value(actorTypeKey{}).(string); ok && t != "" && actorType != ActorSystem {
		actorType = t
	}
	if ws == uuid.Nil {
		ws = resolveWorkspace(ctx, q, e)
	}
	if ws == uuid.Nil {
		// Last resort for rows that name nothing resolvable: the oldest team.
		w, err := q.FirstWorkspace(ctx)
		if err != nil {
			return fmt.Errorf("audit: resolve workspace: %w", err)
		}
		ws = w.ID
	}
	md := e.Metadata
	if md == nil {
		md = map[string]any{}
	}
	if via := ViaFrom(ctx); via != "" {
		if _, has := md["via"]; !has {
			md = maps.Clone(md)
			md["via"] = via
		}
	}
	meta, err := json.Marshal(md)
	if err != nil {
		return fmt.Errorf("audit: marshal metadata: %w", err)
	}
	if err := q.InsertAuditEvent(ctx, db.InsertAuditEventParams{
		WorkspaceID:  ws,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorLabel:   label,
		Action:       e.Action,
		ResourceType: e.ResourceType,
		ResourceID:   e.ResourceID,
		ProjectID:    e.ProjectID,
		Metadata:     meta,
	}); err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// resolveWorkspace finds the team of the audited resource for rows written
// without a principal (system actors): by project, else by resource id.
func resolveWorkspace(ctx context.Context, q *db.Queries, e Entry) uuid.UUID {
	ids := make([]uuid.UUID, 0, 2)
	if e.ProjectID != nil {
		ids = append(ids, *e.ProjectID)
	}
	if id, err := uuid.Parse(e.ResourceID); err == nil {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if ws, err := q.EventWorkspace(ctx, id); err == nil {
			return ws
		}
	}
	return uuid.Nil
}

// AuditEvent is the API view of an audit row.
type AuditEvent struct {
	ID           int64          `json:"id"`
	ActorType    string         `json:"actorType"`
	ActorID      string         `json:"actorId"`
	ActorLabel   string         `json:"actorLabel"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	ProjectID    *uuid.UUID     `json:"projectId,omitempty"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"createdAt"`
}

// Service reads the audit log.
type Service struct {
	q *db.Queries
}

// NewService returns a Service backed by pool (nil allowed for doc generation).
func NewService(pool db.DBTX) *Service {
	return &Service{q: db.New(pool)}
}

// Filter narrows an audit list. Zero values do not filter.
type Filter struct {
	// ProjectID selects events of one project.
	ProjectID *uuid.UUID
	// ResourceID selects events about one resource (a service, deployment, …).
	ResourceID string
	// Type is an action prefix, e.g. "deployment." or "service.create".
	Type string
}

// List returns audit events newest first. cursor is the last id of the
// previous page. Requires the admin scope; project-restricted principals only
// see events for their projects (a ProjectID outside that set yields no events).
func (s *Service) List(ctx context.Context, f Filter, cursor string, limit int) (items []AuditEvent, next string, err error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return nil, "", err
	}
	p, _ := auth.PrincipalFrom(ctx)
	var cur *int64
	if cursor != "" {
		v, perr := strconv.ParseInt(cursor, 10, 64)
		if perr != nil || v < 0 {
			return nil, "", apierr.BadRequest("invalid_cursor", "invalid cursor")
		}
		cur = &v
	}
	if limit <= 0 {
		limit = 50
	}
	params := db.ListAuditEventsParams{
		WorkspaceID: p.WorkspaceID,
		Limit:       int32(limit + 1),
		ProjectIds:  p.ProjectIDs,
		ProjectID:   f.ProjectID,
		CursorID:    cur,
	}
	if f.ResourceID != "" {
		params.ResourceID = &f.ResourceID
	}
	if f.Type != "" {
		params.ActionPrefix = &f.Type
	}
	rows, err := s.q.ListAuditEvents(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list audit events: %w", err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	items = make([]AuditEvent, 0, len(rows))
	for _, r := range rows {
		md := map[string]any{}
		if len(r.Metadata) > 0 {
			if err := json.Unmarshal(r.Metadata, &md); err != nil {
				return nil, "", errors.Join(errors.New("decode audit metadata"), err)
			}
		}
		items = append(items, AuditEvent{
			ID: r.ID, ActorType: r.ActorType, ActorID: r.ActorID, ActorLabel: r.ActorLabel,
			Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID,
			ProjectID: r.ProjectID, Metadata: md, CreatedAt: r.CreatedAt,
		})
	}
	return items, next, nil
}
