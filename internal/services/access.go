package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Event types published by the service-domain packages.
const (
	EventServiceUpdated    = "service.updated"
	EventServiceDeleted    = "service.deleted"
	EventDeploymentUpdated = "deployment.updated"
	EventDomainUpdated     = "domain.updated"
	EventVariableUpdated   = "variable.updated"
	EventLinkUpdated       = "link.updated"
	EventCanvasUpdated     = "canvas.updated"
	// EventConfigUpdated reports a change of an environment's nebula.toml state
	// (check result, apply); its resource is the environment.
	EventConfigUpdated = "config.updated"
)

// Scope is a live service together with the project/environment context every
// authorization and audit entry needs.
type Scope struct {
	Service       db.Service
	WorkspaceID   uuid.UUID
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	ProjectSlug   string
	ProjectName   string
	// ProjectSystem marks the platform project: only administrators reach it.
	ProjectSystem bool
	EnvSlug       string
	EnvName       string
	IsProduction  bool
}

// Config decodes the service configuration with defaults applied.
func (s Scope) Config() Config { return DecodeConfig(s.Service.Config) }

func scopeFromRow(r db.GetServiceScopeRow) Scope {
	return Scope{
		Service: r.Service, WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID, EnvironmentID: r.Service.EnvironmentID,
		ProjectSlug: r.ProjectSlug, ProjectName: r.ProjectName, ProjectSystem: r.ProjectSystem, EnvSlug: r.EnvSlug, EnvName: r.EnvName, IsProduction: r.IsProduction,
	}
}

// LoadScope authorizes scope for the caller and returns the live service. The
// lookup joins through workspace and project, so a service outside the caller's
// workspace or project restriction is indistinguishable from a missing one (404).
func LoadScope(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return Scope{}, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := q.GetServiceScope(ctx, db.GetServiceScopeParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, apierr.NotFound("service")
	}
	if err != nil {
		return Scope{}, fmt.Errorf("get service: %w", err)
	}
	if err := policy.Require(ctx, scope, &row.ProjectID); err != nil {
		return Scope{}, err
	}
	if row.ProjectSystem && policy.Require(ctx, policy.ScopeAdmin, nil) != nil {
		return Scope{}, apierr.NotFound("service") // platform services are for administrators
	}
	return scopeFromRow(row), nil
}

// LoadScopeInternal loads a live service without authorization. It is for
// background jobs and agent callbacks only.
func LoadScopeInternal(ctx context.Context, q *db.Queries, id uuid.UUID) (Scope, error) {
	internal, err := q.GetServiceScopeInternal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, apierr.NotFound("service")
	}
	if err != nil {
		return Scope{}, fmt.Errorf("get service: %w", err)
	}
	return scopeFromRow(db.GetServiceScopeRow(internal)), nil
}

// EnvScope is an environment with its project context.
type EnvScope struct {
	Env         db.Environment
	WorkspaceID uuid.UUID
	ProjectSlug string
}

// LoadEnvironment authorizes scope and returns the environment (404 outside the
// caller's workspace or project restriction).
func LoadEnvironment(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (EnvScope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return EnvScope{}, err
	}
	ws := policy.WorkspaceID(ctx)
	env, err := q.GetEnvironmentScoped(ctx, db.GetEnvironmentScopedParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return EnvScope{}, apierr.NotFound("environment")
	}
	if err != nil {
		return EnvScope{}, fmt.Errorf("get environment: %w", err)
	}
	if err := policy.Require(ctx, scope, &env.ProjectID); err != nil {
		return EnvScope{}, err
	}
	proj, err := q.GetProject(ctx, db.GetProjectParams{ID: env.ProjectID, WorkspaceID: ws})
	if err != nil {
		return EnvScope{}, fmt.Errorf("get project: %w", err)
	}
	if proj.System && policy.Require(ctx, policy.ScopeAdmin, nil) != nil {
		return EnvScope{}, apierr.NotFound("environment") // platform services are for administrators
	}
	return EnvScope{Env: env, WorkspaceID: ws, ProjectSlug: proj.Slug}, nil
}

// DecodeConfig decodes services.config and applies defaults. Corrupt JSON
// yields the defaults of an empty config.
func DecodeConfig(raw json.RawMessage) Config {
	var c Config
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &c)
	}
	return c.WithDefaults()
}

// ConfigError converts a config validation failure into a 422.
func ConfigError(err error) error {
	var pe *composeParseError
	if errors.As(err, &pe) {
		return InvalidComposeError(pe.err, nil)
	}
	if errors.Is(err, ErrInvalidConfig) {
		return apierr.Unprocessable("invalid_config", strings.TrimPrefix(err.Error(), ErrInvalidConfig.Error()+": "))
	}
	return err
}

// IsUniqueViolation reports whether err is a Postgres unique-constraint error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// PublishService announces a service change to the live-update feed, stamped
// with the owning team.
func PublishService(bus *events.Bus, typ string, sc Scope) {
	if bus == nil {
		return
	}
	bus.Publish(events.Event{
		Type: typ, ProjectID: sc.ProjectID.String(), EnvironmentID: sc.EnvironmentID.String(), ResourceID: sc.Service.ID.String(),
		WorkspaceID: WorkspaceStamp(sc.WorkspaceID),
	})
}

// Publish announces an arbitrary resource change. The feed resolves the team
// from the ids; use PublishIn when the resource may be gone by then (deletions).
func Publish(bus *events.Bus, typ string, projectID, environmentID, resourceID uuid.UUID) {
	PublishIn(bus, uuid.Nil, typ, projectID, environmentID, resourceID)
}

// PublishIn is Publish stamped with the owning team, so the feed delivers it to
// that team without looking the ids up. A nil team leaves the event unstamped.
func PublishIn(bus *events.Bus, workspaceID uuid.UUID, typ string, projectID, environmentID, resourceID uuid.UUID) {
	if bus == nil {
		return
	}
	bus.Publish(events.Event{
		Type: typ, ProjectID: projectID.String(), EnvironmentID: environmentID.String(), ResourceID: resourceID.String(),
		WorkspaceID: WorkspaceStamp(workspaceID),
	})
}

// WorkspaceStamp is the events.Event.WorkspaceID value for a team: empty for the
// nil id, which names no team.
func WorkspaceStamp(workspaceID uuid.UUID) string {
	if workspaceID == uuid.Nil {
		return ""
	}
	return workspaceID.String()
}

// ParseETag extracts the integer from an ETag / If-Match value ("12", W/"12").
// The empty string yields ok=false.
func ParseETag(v string) (n int64, present bool, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false, nil
	}
	v = strings.Trim(strings.TrimPrefix(v, "W/"), `"`)
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, true, apierr.BadRequest("invalid_if_match", "If-Match must be the quoted integer from a previous ETag")
	}
	return n, true, nil
}

// ETag formats n as a strong ETag value.
func ETag(n int64) string { return `"` + strconv.FormatInt(n, 10) + `"` }
