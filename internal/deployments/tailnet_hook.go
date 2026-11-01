package deployments

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TailnetHook is the per-service tailnet domain (internal/tailnet) as the
// deployments domain sees it. The tailnet part of a deployment never fails the
// app deployment: Spec only errors for problems that make the whole deployment
// impossible (no integration, an agent that cannot manage sidecars).
type TailnetHook interface {
	// PlanCheck lists the warnings and blocking problems of deploying cfg of sc
	// to srv (nil when the service has no server).
	PlanCheck(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, srv *db.Server) (warnings []string, blocking []services.TailnetProblem, err error)
	// Spec builds the tailnet part of a deploy payload (no secrets). keep is
	// true for deployments of an existing release (rollback, reconcile, drain):
	// they never change the tailnet's configuration, they only keep (or, on
	// another server, re-create) the node as it was last applied. hidePublic
	// reports whether the service's public domains must not be routed.
	Spec(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config, srv db.Server, keep bool) (spec *agentprotocol.ServiceTailnetSpec, hidePublic bool, err error)
	// Secrets attaches the send-time node key of a provisioning spec. The key is
	// minted here, never persisted and never part of the command payload.
	Secrets(ctx context.Context, serviceID uuid.UUID, spec agentprotocol.ServiceTailnetSpec) (map[string]string, error)
	// RecordDeploy stores what the agent reported for the tailnet step of a
	// successful deployment, inside the finalising transaction.
	RecordDeploy(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID, dep db.Deployment, st *agentprotocol.ServiceTailnetStatus) error
}

// SetServiceTailnet installs the per-service tailnet hook (wire time only).
func (s *Service) SetServiceTailnet(h TailnetHook) { s.ServiceTailnet = h }
