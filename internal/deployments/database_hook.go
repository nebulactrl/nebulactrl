package deployments

import (
	"context"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DatabaseHook is the managed-database replication domain (internal/databases)
// as deployments sees it: a fenced old primary cannot be deployed, replicas and
// primaries explain themselves in the plan, and the deploy payload carries the
// replication role the agent polls.
type DatabaseHook interface {
	// PlanCheck lists the warnings and blocking problems of deploying cfg of sc.
	PlanCheck(ctx context.Context, q *db.Queries, sc services.Scope, cfg services.Config) (warnings []string, blocking []PlanBlocking, err error)
	// DatabaseRuntime is the replication part of the deploy payload (nil for a
	// service that is not part of a replica group).
	DatabaseRuntime(ctx context.Context, q *db.Queries, sc services.Scope) (*agentprotocol.DatabaseRuntime, error)
}

// SetDatabaseHook installs the replication hook (wire time only).
func (s *Service) SetDatabaseHook(h DatabaseHook) { s.Databases = h }
