package proxyroutes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// validateRouteConfig checks the shape of a route (422 invalid_config).
func validateRouteConfig(cfg services.RouteConfig) error {
	full := services.Config{Route: &cfg}.WithDefaults()
	return services.ConfigError(full.Validate(services.SourceExternal))
}

// routeServiceConfig wraps a route in the service config JSON.
func routeServiceConfig(cfg services.RouteConfig) (json.RawMessage, error) {
	full := services.Config{Route: &cfg}.WithDefaults()
	return json.Marshal(full)
}

// insertRouteService inserts the kind=route services row and registers its
// listen port, inside tx. The route is staged (generation 1, never applied).
func insertRouteService(ctx context.Context, tx pgx.Tx, env services.EnvScope, name, slug string, cfg services.RouteConfig, serverID uuid.UUID) (db.Service, error) {
	q := db.New(tx)
	raw, err := routeServiceConfig(cfg)
	if err != nil {
		return db.Service{}, err
	}
	base := projects.Slugify(name, "route")
	taken, err := q.ListServiceSlugsLike(ctx, db.ListServiceSlugsLikeParams{EnvironmentID: env.Env.ID, Base: base})
	if err != nil {
		return db.Service{}, fmt.Errorf("list slugs: %w", err)
	}
	row, err := q.InsertService(ctx, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.Env.ID, Name: name, Slug: routeSlug(slug, base, taken), Kind: services.KindRoute,
		SourceType: services.SourceExternal, Config: raw, ServerID: &serverID,
	})
	if err != nil {
		return db.Service{}, fmt.Errorf("insert route service: %w", err)
	}
	if err := registerPort(ctx, q, row.ID, serverID, cfg); err != nil {
		return db.Service{}, err
	}
	return row, nil
}

// routeOf returns the decoded route config of a kind=route service.
func routeOf(row db.Service) (services.RouteConfig, error) {
	cfg := services.DecodeConfig(row.Config)
	if row.Kind != services.KindRoute || cfg.Route == nil {
		return services.RouteConfig{}, apierr.NotFound("route")
	}
	return *cfg.Route, nil
}

// routeSlug is the exact slug when one is requested, else the first free slug derived from the name.
func routeSlug(exact, base string, taken []string) string {
	if exact != "" {
		return exact
	}
	return projects.UniqueSlug(base, taken)
}
