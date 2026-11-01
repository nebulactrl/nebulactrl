// Package canvas stores the per-environment canvas layout (node positions,
// groups, viewport). Deployment code never reads it.
package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const (
	maxNodes  = 2000
	maxGroups = 200
	maxName   = 200
)

// Pos is a node position on the canvas.
type Pos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Group is a visual frame around services.
type Group struct {
	ID         string   `json:"id" minLength:"1" maxLength:"64"`
	Name       string   `json:"name" maxLength:"200"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	W          float64  `json:"w"`
	H          float64  `json:"h"`
	Collapsed  bool     `json:"collapsed"`
	ServiceIDs []string `json:"serviceIds"`
}

// Viewport is the saved camera.
type Viewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

// Layout is the stored canvas document.
type Layout struct {
	Nodes    map[string]Pos `json:"nodes"`
	Groups   []Group        `json:"groups"`
	Viewport Viewport       `json:"viewport"`
}

// Canvas is a layout with its version (the ETag).
type Canvas struct {
	Layout  Layout `json:"layout"`
	Version int64  `json:"version"`
}

func emptyLayout() Layout {
	return Layout{Nodes: map[string]Pos{}, Groups: []Group{}, Viewport: Viewport{Zoom: 1}}
}

func normalize(l Layout) Layout {
	if l.Nodes == nil {
		l.Nodes = map[string]Pos{}
	}
	if l.Groups == nil {
		l.Groups = []Group{}
	}
	for i := range l.Groups {
		if l.Groups[i].ServiceIDs == nil {
			l.Groups[i].ServiceIDs = []string{}
		}
	}
	if l.Viewport.Zoom == 0 {
		l.Viewport.Zoom = 1
	}
	return l
}

func validate(l Layout) error {
	if len(l.Nodes) > maxNodes || len(l.Groups) > maxGroups {
		return apierr.Unprocessable("layout_too_large", "the layout has too many nodes or groups")
	}
	for id := range l.Nodes {
		if _, err := uuid.Parse(id); err != nil {
			return apierr.Unprocessable("invalid_layout", "node keys must be service IDs")
		}
	}
	seen := map[string]bool{}
	for _, g := range l.Groups {
		if seen[g.ID] {
			return apierr.Unprocessable("invalid_layout", "group ids must be unique")
		}
		seen[g.ID] = true
		if len(g.Name) > maxName || len(g.ServiceIDs) > maxNodes {
			return apierr.Unprocessable("invalid_layout", "group is too large")
		}
	}
	if l.Viewport.Zoom < 0 {
		return apierr.Unprocessable("invalid_layout", "zoom must not be negative")
	}
	return nil
}

// Service implements the canvas use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus) *Service {
	return &Service{pool: pool, q: db.New(pool), bus: bus}
}

func (s *Service) env(ctx context.Context, scope string, projectID, envID uuid.UUID) (services.EnvScope, error) {
	env, err := services.LoadEnvironment(ctx, s.q, scope, envID)
	if err != nil {
		return services.EnvScope{}, err
	}
	if env.Env.ProjectID != projectID {
		return services.EnvScope{}, apierr.NotFound("environment")
	}
	return env, nil
}

func decode(row db.CanvasLayout) (Canvas, error) {
	var l Layout
	if len(row.Layout) > 0 {
		if err := json.Unmarshal(row.Layout, &l); err != nil {
			return Canvas{}, fmt.Errorf("decode layout: %w", err)
		}
	}
	return Canvas{Layout: normalize(l), Version: row.Version}, nil
}

// Get returns the environment's layout; version 0 means nothing was saved yet.
func (s *Service) Get(ctx context.Context, projectID, envID uuid.UUID) (Canvas, error) {
	if _, err := s.env(ctx, policy.ScopeProjectsRead, projectID, envID); err != nil {
		return Canvas{}, err
	}
	row, err := s.q.GetCanvasLayout(ctx, envID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Canvas{Layout: emptyLayout(), Version: 0}, nil
	}
	if err != nil {
		return Canvas{}, fmt.Errorf("get canvas: %w", err)
	}
	return decode(row)
}

// Put stores the layout if expectedVersion is still current (0 = none saved yet).
func (s *Service) Put(ctx context.Context, projectID, envID uuid.UUID, expectedVersion int64, layout Layout) (Canvas, error) {
	env, err := s.env(ctx, policy.ScopeProjectsWrite, projectID, envID)
	if err != nil {
		return Canvas{}, err
	}
	layout = normalize(layout)
	if err := validate(layout); err != nil {
		return Canvas{}, err
	}
	raw, err := json.Marshal(layout)
	if err != nil {
		return Canvas{}, err
	}
	conflict := func() error {
		cur := int64(0)
		if row, err := s.q.GetCanvasLayout(ctx, envID); err == nil {
			cur = row.Version
		}
		return apierr.PreconditionFailed("layout_conflict", "the layout changed since you loaded it").
			WithDetails(map[string]any{"currentVersion": cur})
	}
	var row db.CanvasLayout
	if expectedVersion == 0 {
		row, err = s.q.InsertCanvasLayout(ctx, db.InsertCanvasLayoutParams{EnvironmentID: envID, Layout: raw})
		if errors.Is(err, pgx.ErrNoRows) {
			return Canvas{}, conflict()
		}
	} else {
		row, err = s.q.UpdateCanvasLayoutCAS(ctx, db.UpdateCanvasLayoutCASParams{EnvironmentID: envID, Layout: raw, ExpectedVersion: expectedVersion})
		if errors.Is(err, pgx.ErrNoRows) {
			return Canvas{}, conflict()
		}
	}
	if err != nil {
		return Canvas{}, fmt.Errorf("save canvas: %w", err)
	}
	services.Publish(s.bus, services.EventCanvasUpdated, env.Env.ProjectID, envID, envID)
	return decode(row)
}

type getInput struct {
	ProjectID     uuid.UUID `path:"projectId"`
	EnvironmentID uuid.UUID `path:"environmentId"`
}

type putInput struct {
	ProjectID     uuid.UUID `path:"projectId"`
	EnvironmentID uuid.UUID `path:"environmentId"`
	IfMatch       string    `header:"If-Match" doc:"The ETag (version) from the last GET. Use \"0\" for the first save."`
	Body          struct {
		Layout Layout `json:"layout"`
	}
}

type output struct {
	ETag string `header:"ETag"`
	Body Canvas
}

// Register attaches the canvas endpoints.
func Register(h huma.API, svc *Service) {
	path := "/projects/{projectId}/environments/{environmentId}/canvas"
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getCanvas", Method: http.MethodGet, Path: path, Tags: []string{"canvas"},
		Summary:     "Get an environment's canvas layout",
		Description: "`version` 0 with an empty layout means nothing was saved yet. The ETag is the version.",
		Errors:      errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *getInput) (*output, error) {
		c, err := svc.Get(ctx, in.ProjectID, in.EnvironmentID)
		if err != nil {
			return nil, err
		}
		return &output{ETag: services.ETag(c.Version), Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "putCanvas", Method: http.MethodPut, Path: path, Tags: []string{"canvas"},
		Summary: "Save an environment's canvas layout",
		Description: "Requires `If-Match: \"<version>\"`; a stale version returns 412 `layout_conflict`. " +
			"The layout is presentation only and never affects deployments.",
		Errors: append(errs, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *putInput) (*output, error) {
		v, ok, err := services.ParseETag(in.IfMatch)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(http.StatusPreconditionRequired, "if_match_required", "send If-Match with the version from the last GET")
		}
		c, err := svc.Put(ctx, in.ProjectID, in.EnvironmentID, v, in.Body.Layout)
		if err != nil {
			return nil, err
		}
		return &output{ETag: services.ETag(c.Version), Body: c}, nil
	})
}
