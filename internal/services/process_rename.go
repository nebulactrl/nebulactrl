package services

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Renaming a named process is a staged change like any other edit of
// config.processes: the running release keeps the old name until the next deploy,
// so the scale row and the domains of the old name stay as they are. The staged
// renames are stored per service (service_process_rename); the deploy moves the
// domains to the new name (FinalizeProcessRenames).

// PendingProcessRenames maps the old name of each staged rename to its new name.
func PendingProcessRenames(ctx context.Context, q *db.Queries, serviceID uuid.UUID) (map[string]string, error) {
	rows, err := q.ListProcessRenames(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list process renames: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.FromProcess] = r.ToProcess
	}
	return out, nil
}

// SyncProcessRenames drops the staged renames cfg does not carry any more: the
// new name left the config, or the old name is back in it.
func SyncProcessRenames(ctx context.Context, q *db.Queries, serviceID uuid.UUID, cfg Config) error {
	if err := q.DeleteStaleProcessRenames(ctx, db.DeleteStaleProcessRenamesParams{ServiceID: serviceID, Keep: namedProcessNames(cfg)}); err != nil {
		return fmt.Errorf("drop stale process renames: %w", err)
	}
	return nil
}

// FinalizeProcessRenames runs after a successful deployment of release: the
// domains of a renamed process follow it to the new name once the release has
// the new name and no longer the old one. A staged rename the release resolved
// (the new name is in it) is deleted; one whose new name left the staged config
// is deleted too. No-op for a service without staged renames.
func FinalizeProcessRenames(ctx context.Context, tx pgx.Tx, sc Scope, release Config) error {
	q := db.New(tx)
	rows, err := q.ListProcessRenames(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("list process renames: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	fresh, err := LoadScopeInternal(ctx, q, sc.Service.ID)
	if err != nil {
		return err
	}
	staged := fresh.Config()
	moves := map[string]string{} // old name -> new name, for every rename the release shipped
	var done []string
	for _, r := range rows {
		inRelease := hasNamedProcess(release, r.ToProcess)
		if !inRelease && hasNamedProcess(staged, r.ToProcess) {
			continue // still staged: a later deploy ships it
		}
		if inRelease && !hasNamedProcess(release, r.FromProcess) {
			moves[r.FromProcess] = r.ToProcess
		}
		done = append(done, r.ToProcess)
	}
	if len(moves) > 0 {
		// Every move reads the components as they were before any of them.
		doms, err := q.ListServiceDomains(ctx, sc.Service.ID)
		if err != nil {
			return fmt.Errorf("list domains: %w", err)
		}
		ids := map[string][]uuid.UUID{}
		for _, d := range doms {
			if to, ok := moves[d.Component]; ok {
				ids[to] = append(ids[to], d.ID)
			}
		}
		for _, to := range slices.Sorted(maps.Keys(ids)) {
			if _, err := q.MoveDomainsToService(ctx, db.MoveDomainsToServiceParams{ServiceID: sc.Service.ID, Component: to, DomainIds: ids[to]}); err != nil {
				return fmt.Errorf("move domains to process %s: %w", to, err)
			}
		}
	}
	for _, to := range done {
		if err := q.DeleteProcessRename(ctx, db.DeleteProcessRenameParams{ServiceID: sc.Service.ID, ToProcess: to}); err != nil {
			return fmt.Errorf("delete process rename: %w", err)
		}
	}
	return nil
}

// namedProcessNames lists the names of the named processes of cfg (never nil).
func namedProcessNames(cfg Config) []string {
	names := make([]string, 0, len(cfg.Processes))
	for _, p := range cfg.Processes {
		names = append(names, p.Name)
	}
	return names
}

// hasNamedProcess reports whether cfg has a named process of that name.
func hasNamedProcess(cfg Config, name string) bool {
	return slices.ContainsFunc(cfg.Processes, func(p ProcessConfig) bool { return p.Name == name })
}
