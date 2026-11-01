// Package databases implements managed databases: the engine catalog
// (internal/databases/engines), creation of kind=database services, derived
// connection exports, audited credential reveal, credential rotation through
// the agent and version upgrades.
package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Event types published by this package.
const (
	EventDatabaseUpdated = "database.updated"
)

// Service implements the database use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	box    *secretbox.Box
	disp   agents.Dispatcher
	svcs   *services.Service
	logger *slog.Logger
	ext    *services.ExtSecrets
	now    func() time.Time
	rep    ReplicaDeps
	// brokenAfter is BrokenAfter unless SetBrokenAfter overrides it.
	brokenAfter time.Duration
}

// SetBrokenAfter overrides BrokenAfter (NEBULACTRL_REPLICA_BROKEN_AFTER). A
// non-positive value keeps the current one.
func (s *Service) SetBrokenAfter(d time.Duration) {
	if d > 0 {
		s.brokenAfter = d
	}
}

// SetExtSecrets installs the app's external secret manager handle (database
// credentials may reference `${{vault:…}}`). Nil: none.
func (s *Service) SetExtSecrets(e *services.ExtSecrets) { s.ext = e }

// NewService returns a Service (pool, box, disp and svcs may be nil for
// OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, box *secretbox.Box, disp agents.Dispatcher, svcs *services.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{pool: pool, bus: bus, box: box, disp: disp, svcs: svcs, logger: logger, now: time.Now, brokenAfter: BrokenAfter}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// managed loads a service the caller may act on with scope and checks that it
// is a managed database.
func (s *Service) managed(ctx context.Context, scope string, id uuid.UUID) (services.Scope, engines.Spec, *engines.Engine, error) {
	sc, err := services.LoadScope(ctx, s.q, scope, id)
	if err != nil {
		return services.Scope{}, engines.Spec{}, nil, err
	}
	return s.asManaged(sc)
}

func (s *Service) asManaged(sc services.Scope) (services.Scope, engines.Spec, *engines.Engine, error) {
	cfg := sc.Config()
	if sc.Service.Kind != services.KindDatabase || cfg.Database == nil {
		return services.Scope{}, engines.Spec{}, nil, apierr.Conflict("not_a_managed_database",
			"this service is not a managed database; create one from the Database menu")
	}
	eng := engines.Lookup(cfg.Database.Engine)
	if eng == nil {
		return services.Scope{}, engines.Spec{}, nil, apierr.Unprocessable("invalid_database", "unknown database engine "+cfg.Database.Engine)
	}
	return sc, *cfg.Database, eng, nil
}

// resolved returns the database service's resolved variables (user variables
// with references expanded, platform variables included).
func (s *Service) resolved(ctx context.Context, q *db.Queries, sc services.Scope) (map[string]string, error) {
	res, err := services.LoadResolver(ctx, q, s.box, sc.EnvInfo())
	if err != nil {
		return nil, err
	}
	out, err := s.ext.ResolveService(ctx, res, sc.ExtScope(services.ExtPurposeDatabase), sc.Service.Slug)
	if err != nil {
		return nil, err
	}
	return out.Vars, nil
}

// credentials returns the database's resolved credentials.
func (s *Service) credentials(ctx context.Context, q *db.Queries, sc services.Scope, spec engines.Spec, eng *engines.Engine) (engines.Credentials, map[string]string, error) {
	vars, err := s.resolved(ctx, q, sc)
	if err != nil {
		return engines.Credentials{}, nil, err
	}
	return eng.Creds(spec, func(k string) (string, bool) { v, ok := vars[k]; return v, ok }), vars, nil
}

func (s *Service) publish(sc services.Scope) {
	services.Publish(s.bus, EventDatabaseUpdated, sc.ProjectID, sc.EnvironmentID, sc.Service.ID)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
}

// currentServer returns the server running the database's current deployment.
func (s *Service) currentServer(ctx context.Context, q *db.Queries, sc services.Scope) (uuid.UUID, error) {
	if sc.Service.CurrentDeploymentID == nil {
		return uuid.Nil, apierr.Conflict("not_deployed", "the database has not been deployed yet")
	}
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sc.Service.CurrentDeploymentID})
	if err != nil {
		return uuid.Nil, fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return uuid.Nil, apierr.Conflict("not_deployed", "the database has no running deployment")
	}
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *deps[0].ServerID})
	if err != nil {
		return uuid.Nil, fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return uuid.Nil, apierr.Unprocessable("server_unavailable", fmt.Sprintf("server %q is revoked", srv.Name))
	}
	return srv.ID, nil
}

// referrers lists the services of the database's environment whose variables
// reference it (${{slug.KEY}}), other than the database itself.
func (s *Service) referrers(ctx context.Context, q *db.Queries, sc services.Scope) ([]db.Service, error) {
	rows, err := q.ListEnvironmentVariableTemplates(ctx, sc.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, r := range rows {
		if r.ServiceID == sc.Service.ID || ids[r.ServiceID] {
			continue
		}
		val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(r.ServiceID.String(), r.Key), r.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt variable %s: %w", r.Key, err)
		}
		for _, ref := range services.References(val) {
			if ref[0] == sc.Service.Slug {
				ids[r.ServiceID] = true
				break
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := q.ListAllEnvironmentServices(ctx, sc.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var out []db.Service
	for _, svc := range all {
		if ids[svc.ID] {
			out = append(out, svc)
		}
	}
	return out, nil
}

// stageReferrers bumps the generation of every service that references the
// database, so their changed connection strings show up as staged changes.
// It returns the services it restaged.
func (s *Service) stageReferrers(ctx context.Context, q *db.Queries, sc services.Scope) ([]db.Service, error) {
	refs, err := s.referrers(ctx, q, sc)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if _, err := q.BumpServiceGeneration(ctx, r.ID); err != nil {
			return nil, fmt.Errorf("stage %s: %w", r.Slug, err)
		}
	}
	return refs, nil
}

func (s *Service) publishReferrers(sc services.Scope, refs []db.Service) {
	for _, r := range refs {
		services.Publish(s.bus, services.EventServiceUpdated, sc.ProjectID, sc.EnvironmentID, r.ID)
	}
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
