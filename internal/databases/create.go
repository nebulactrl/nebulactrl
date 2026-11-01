package databases

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// CreateInput describes a new managed database.
type CreateInput struct {
	EnvironmentID uuid.UUID
	// Name is the display name; empty means the engine's name.
	Name string
	// Slug, when set, is the exact slug of the service (nebula.toml keys services by slug);
	// empty derives it from Name.
	Slug string
	// Engine is required; Version empty means the engine's default version.
	Engine, Version, Flavor, DatabaseName string
	// Username is the database user for engines that have one; empty means the
	// engine's default user.
	Username string
	ServerID *uuid.UUID
	// AcceptLicense must be true for engines with a source-available licence
	// (MongoDB, Redis): the API surfaces the licence note and the caller
	// acknowledges it.
	AcceptLicense bool
}

const passwordLength = 32

// CreateTx creates a staged managed database inside tx: the service, its data
// volume and its generated credentials. The caller owns the transaction, the
// audit entry and the events (use Publish with the returned scope). It checks
// projects:write on the environment like every other service mutation.
func (s *Service) CreateTx(ctx context.Context, tx pgx.Tx, in CreateInput) (db.Service, error) {
	row, _, err := s.createTx(ctx, tx, in)
	return row, err
}

// CreateForTemplate is CreateTx under the name template installs use.
func (s *Service) CreateForTemplate(ctx context.Context, tx pgx.Tx, in CreateInput) (db.Service, error) {
	return s.CreateTx(ctx, tx, in)
}

// Publish announces a database created with CreateTx after its transaction
// committed.
func (s *Service) Publish(env services.EnvScope, row db.Service) {
	sc := services.ScopeOf(env, row)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
}

func (s *Service) createTx(ctx context.Context, tx pgx.Tx, in CreateInput) (db.Service, services.EnvScope, error) {
	q := db.New(tx)
	env, err := services.LoadEnvironment(ctx, q, policy.ScopeProjectsWrite, in.EnvironmentID)
	if err != nil {
		return db.Service{}, services.EnvScope{}, err
	}
	eng := engines.Lookup(in.Engine)
	if eng == nil {
		return db.Service{}, env, apierr.Unprocessable("invalid_database",
			fmt.Sprintf("unknown database engine %q (one of %s)", in.Engine, strings.Join(engines.IDs(), ", ")))
	}
	spec := engines.Spec{Engine: eng.ID, Version: in.Version, Flavor: in.Flavor, DatabaseName: in.DatabaseName, Username: in.Username}.Normalize()
	if err := spec.Validate(); err != nil {
		return db.Service{}, env, apierr.Unprocessable("invalid_database", err.Error())
	}
	if eng.LicenseClass == engines.LicenseSourceAvailable && !in.AcceptLicense {
		return db.Service{}, env, apierr.Unprocessable("license_not_accepted", eng.LicenseNote).
			WithDetails(map[string]any{"engine": eng.ID, "license": eng.License})
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = eng.Label
	}
	row, err := s.svcs.CreateInTx(ctx, tx, env, services.CreateInput{
		Name: name, Kind: services.KindDatabase, SourceType: services.SourceImage,
		// Databases never need the internet (backups stream through the agent), so
		// outbound access starts blocked; the owner can allow it per service.
		Config: services.Config{Database: &spec, BlockEgress: true}, ServerID: in.ServerID, Slug: in.Slug,
	})
	if err != nil {
		return db.Service{}, env, err
	}
	vol, mount := spec.DataMount()
	if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: vol, MountPath: mount}); err != nil {
		return db.Service{}, env, fmt.Errorf("insert volume: %w", err)
	}
	sc := services.ScopeOf(env, row)
	for _, v := range spec.EnvDefaults() {
		val := v.Value
		if v.Generate {
			if val, err = secretgen.Secret(passwordLength, secretgen.AlphaAlnum); err != nil {
				return db.Service{}, env, err
			}
		}
		w := services.VarWrite{Key: v.Key, Value: val, Secret: v.Secret}
		if v.Generate {
			w.Source = services.SourceGenerated
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.box, sc, w); err != nil {
			return db.Service{}, env, fmt.Errorf("write %s: %w", v.Key, err)
		}
	}
	return row, env, nil
}

// Create creates a managed database (staged: nothing runs until deployed).
func (s *Service) Create(ctx context.Context, in CreateInput) (services.ServiceView, error) {
	var row db.Service
	var env services.EnvScope
	err := dbInTx(ctx, s, func(tx pgx.Tx) error {
		var err error
		if row, env, err = s.createTx(ctx, tx, in); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "database.create", ResourceType: "service", ResourceID: row.ID.String(), ProjectID: &env.Env.ProjectID,
			Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "engine": in.Engine, "version": in.Version},
		})
	})
	if services.IsUniqueViolation(err) {
		return services.ServiceView{}, apierr.Conflict("slug_conflict", "a service with that name was just created; retry")
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return services.ServiceView{}, err
		}
		return services.ServiceView{}, fmt.Errorf("create database: %w", err)
	}
	sc := services.ScopeOf(env, row)
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	return s.svcs.ViewFor(ctx, sc)
}

func dbInTx(ctx context.Context, s *Service, fn func(pgx.Tx) error) error {
	return db.InTx(ctx, s.pool, fn)
}
