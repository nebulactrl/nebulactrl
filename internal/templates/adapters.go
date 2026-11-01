package templates

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// VariablesAdapter adapts the variables service to VariableWriter.
type VariablesAdapter struct{ S *variables.Service }

// UpsertTx implements VariableWriter.
func (a VariablesAdapter) UpsertTx(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, key, value string, isSecret bool) error {
	_, err := a.S.UpsertTx(ctx, tx, serviceID, key, value, isSecret)
	return err
}

// Values implements VariableReader: the stored variables of a service, secrets
// without a value.
func (a VariablesAdapter) Values(ctx context.Context, serviceID uuid.UUID) (map[string]LiveVariable, error) {
	l, err := a.S.List(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]LiveVariable, len(l.Items))
	for _, v := range l.Items {
		lv := LiveVariable{Secret: v.IsSecret}
		if v.Value != nil && !v.IsSecret {
			lv.Value = *v.Value
		}
		out[v.Key] = lv
	}
	return out, nil
}

// DatabasesAdapter adapts the databases service to DatabaseCreator.
type DatabasesAdapter struct{ S *databases.Service }

// CreateTx implements DatabaseCreator. The template's licence check is part of
// the plan, so the engine licence is acknowledged here.
func (a DatabasesAdapter) CreateTx(ctx context.Context, tx pgx.Tx, in DatabaseSpec) (db.Service, error) {
	return a.S.CreateForTemplate(ctx, tx, databases.CreateInput{
		EnvironmentID: in.EnvironmentID, Name: in.Name, Engine: in.Engine, Version: in.Version,
		DatabaseName: in.DatabaseName, ServerID: in.ServerID, AcceptLicense: true,
	})
}

// PortsAdapter adapts proxyroutes to PortRouter.
type PortsAdapter struct{}

// AddPortRouteTx implements PortRouter: published template ports are open to the internet.
func (PortsAdapter) AddPortRouteTx(ctx context.Context, tx pgx.Tx, in PortRoute) (uuid.UUID, error) {
	return proxyroutes.AddPortRouteTx(ctx, tx, proxyroutes.PortRouteInput{
		EnvironmentID: in.EnvironmentID, TargetServiceID: in.TargetServiceID, Name: in.Name, Protocol: in.Protocol,
		ListenPort: in.ListenPort, TargetPort: in.TargetPort, OpenToInternet: true,
	})
}

// PortInUse implements PortRouter.
func (PortsAdapter) PortInUse(ctx context.Context, q *db.Queries, serverID uuid.UUID, protocol string, port int) (bool, error) {
	return proxyroutes.PortInUse(ctx, q, serverID, protocol, port)
}

// MaterializeWith adapts variables.Materialize to a Materializer.
func MaterializeWith(template string) (string, bool, error) {
	m, err := variables.Materialize(template)
	if err != nil {
		return "", false, err
	}
	return m.Stored, m.Secret, nil
}
