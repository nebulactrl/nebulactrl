package backups

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// DatabaseCreator creates managed databases; *databases.Service implements it.
type DatabaseCreator interface {
	Create(ctx context.Context, in databases.CreateInput) (services.ServiceView, error)
}

type databaseProvisioner struct{ d DatabaseCreator }

// NewDatabaseProvisioner returns the Provisioner that restores into a database
// created by the managed-database service: same engine, version, flavor and
// database name as the source, its own volume and freshly generated
// credentials.
func NewDatabaseProvisioner(d DatabaseCreator) Provisioner { return databaseProvisioner{d: d} }

// CreateSibling implements Provisioner.
func (p databaseProvisioner) CreateSibling(ctx context.Context, src services.Scope, name string, serverID uuid.UUID) (uuid.UUID, error) {
	spec, err := databaseSpec(src.Service.Config)
	if err != nil {
		return uuid.Nil, err
	}
	view, err := p.d.Create(ctx, databases.CreateInput{
		EnvironmentID: src.EnvironmentID, Name: name, Engine: spec.Engine, Version: spec.Version, Flavor: spec.Flavor,
		DatabaseName: spec.DatabaseName, ServerID: &serverID,
		// The source already runs this engine; its licence was accepted when it was created.
		AcceptLicense: true,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create restore target: %w", err)
	}
	return view.ID, nil
}
