package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// DatabaseGuard is the managed-database domain (internal/databases) as services
// sees it: a primary with read replicas and a replica must not be deleted or
// moved like any other service.
type DatabaseGuard interface {
	// BeforeDelete runs in the delete's transaction and may veto it (409).
	BeforeDelete(ctx context.Context, tx pgx.Tx, sc Scope) error
	// BeforeMove runs before a change of the service's server and may veto it (409).
	BeforeMove(ctx context.Context, q *db.Queries, sc Scope, newServer *uuid.UUID) error
}

// SetDatabaseGuard installs the managed-database guard (wire time only).
func (s *Service) SetDatabaseGuard(g DatabaseGuard) { s.dbGuard = g }
