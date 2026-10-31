package tailscale

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// guardMeshInUse refuses (409 integration_in_use) to remove or change the
// integration while the server mesh is built on it: the mesh is enabled or a
// server still has a mesh node. verb is "removed" or "changed".
func guardMeshInUse(ctx context.Context, q *db.Queries, ws uuid.UUID, verb string) error {
	n, err := q.CountMeshInUse(ctx, ws)
	if err != nil {
		return fmt.Errorf("count mesh use: %w", err)
	}
	if n > 0 {
		return apierr.IntegrationInUse(fmt.Sprintf("the server mesh uses this integration; turn the mesh off and wait for its nodes to be removed before it is %s", verb))
	}
	return nil
}
