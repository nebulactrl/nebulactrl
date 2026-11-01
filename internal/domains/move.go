package domains

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// MoveToService re-homes domains onto the target service as domains of its
// process (process "" = the target's default process), inside the caller's
// transaction. It does not bump the target's generation: the moved domains are
// also added to the Domains of the target's current release snapshot, so they
// show no pending change, and the DNS automation re-matches them. The caller
// builds and enqueues the route table in the same transaction, so every
// hostname flips from the old service's containers to the target's atomically.
func (s *Service) MoveToService(ctx context.Context, tx pgx.Tx, domainIDs []uuid.UUID, targetServiceID uuid.UUID, process string) error {
	if len(domainIDs) == 0 {
		return nil
	}
	q := db.New(tx)
	target, err := services.LoadScopeInternal(ctx, q, targetServiceID)
	if err != nil {
		return fmt.Errorf("load target service: %w", err)
	}
	// The process must exist on the target and have a port the domains can reach.
	if p, ok := target.Config().Process(process); !ok || p.Port <= 0 {
		return apierr.Unprocessable("invalid_process", fmt.Sprintf("the service has no routable process %q", process))
	}
	moved, err := q.MoveDomainsToService(ctx, db.MoveDomainsToServiceParams{DomainIds: domainIDs, ServiceID: targetServiceID, Component: storedProcess(target.Config(), process)})
	if err != nil {
		return fmt.Errorf("move domains: %w", err)
	}
	if rel, ok, err := services.AppliedRelease(ctx, q, target.Service); err != nil {
		return err
	} else if ok {
		ids := make([]uuid.UUID, len(moved))
		for i, d := range moved {
			ids[i] = d.ID
		}
		users, err := services.DomainAuthUsernames(ctx, q, ids)
		if err != nil {
			return err
		}
		snap := services.DecodeSnapshot(rel.Config)
		for _, d := range moved {
			snap.Domains = append(snap.Domains, services.SnapshotDomainOf(d, users[d.ID]))
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			return fmt.Errorf("encode release snapshot: %w", err)
		}
		if err := q.UpdateReleaseConfig(ctx, db.UpdateReleaseConfigParams{ID: rel.ID, Config: raw}); err != nil {
			return fmt.Errorf("update release snapshot: %w", err)
		}
	}
	if s.dns != nil {
		for _, d := range moved {
			if err := s.dns.DomainRenamed(ctx, tx, d.ID); err != nil {
				return fmt.Errorf("resync dns: %w", err)
			}
		}
	}
	return nil
}

// storedProcess is the value of domains.component for a process: empty for the
// default process, else its name.
func storedProcess(cfg services.Config, process string) string {
	if p, ok := cfg.Process(process); ok && !p.Default {
		return p.Name
	}
	return ""
}
