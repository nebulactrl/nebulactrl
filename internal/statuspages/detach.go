package statuspages

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// DetachProject removes, inside tx, the components that show the services of
// the project from every status page. Nothing else on the pages changes: the
// remaining components keep their order and the pages stay as they are. It
// returns how many components went and a function that announces the change to
// the pages' viewers; call it only after tx committed. The caller has
// authorized the deletion of the project: no status page scope is checked here.
func (s *Service) DetachProject(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) (int, func(), error) {
	pages, err := db.New(tx).DeleteProjectStatusPageComponents(ctx, projectID)
	if err != nil {
		return 0, func() {}, fmt.Errorf("detach project from status pages: %w", err)
	}
	ws := policy.WorkspaceID(ctx)
	return len(pages), func() {
		if len(pages) > 0 {
			s.publish(ws, pages...)
		}
	}, nil
}
