package servers

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PlatformRoutesHook re-sends the route tables of the servers that run the platform
// service, Headscale (internal/domains.PlatformRouteSync). It accepts only
// internal networks and the public address of every server of the workspace, so
// a server that is added, revoked or changes address changes their allow list.
type PlatformRoutesHook interface {
	// ServersChanged queues the route table of every server that runs a platform
	// service of the workspace inside tx.
	ServersChanged(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error
}

// SetPlatformRoutes installs the platform route hook (wire time only).
func (s *Service) SetPlatformRoutes(h PlatformRoutesHook) { s.platform = h }

// platformRoutesChanged runs the hook, if any, inside tx.
func (s *Service) platformRoutesChanged(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error {
	if s.platform == nil {
		return nil
	}
	return s.platform.ServersChanged(ctx, tx, workspaceID)
}
