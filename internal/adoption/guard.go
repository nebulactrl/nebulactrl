package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// guardExisting applies the production guard to the production environments
// that already exist and that the adoption writes services into, variables and
// domains included (a new environment has nothing to protect). A reattached
// service always lives in an existing environment. The approval is bound to
// the choices.
func (s *Service) guardExisting(ctx context.Context, plan *AdoptionPlan, srv db.Server) error {
	for _, pe := range plan.Environments {
		if pe.Action != actExisting || !pe.Production {
			continue
		}
		var adopt, reattach []string
		for _, ps := range pe.Services {
			switch ps.Action {
			case ActionAdopt:
				adopt = append(adopt, ps.Slug)
			case ActionReattach:
				reattach = append(reattach, ps.Slug)
			}
		}
		if len(adopt)+len(reattach) == 0 || !prodguard.Applies(ctx, true) {
			continue
		}
		env, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsWrite, pe.ID)
		if err != nil {
			return err
		}
		sort.Strings(adopt)
		sort.Strings(reattach)
		body, err := json.Marshal(struct {
			Env      uuid.UUID
			Server   uuid.UUID
			Adopt    []string
			Reattach []string
		}{pe.ID, srv.ID, adopt, reattach})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		effects := []string{"Rebuilds " + strconv.Itoa(len(adopt)+len(reattach)) + " service(s) around their running containers: " + strings.Join(append(adopt, reattach...), ", ") + "."}
		if err := prodguard.Check(ctx,
			env.ProdScope(fmt.Sprintf("Adopt the workloads of %s into production (%s)", srv.Name, env.Env.Name), false, nil, append(effects, "No container is restarted and no deployment runs.")...),
			"adopt_server", prodguard.Digest("adopt_server", pe.ID, sum[:])); err != nil {
			return err
		}
	}
	return nil
}

// recheck compares what the plan was computed from with the server as it is
// now, under the server's lock: a concurrent adoption or review may have
// changed which services are held, unmanaged or already run here.
func (ap *applier) recheck(ctx context.Context, q *db.Queries, locked db.Server) error {
	un, err := q.ListServerUnmanagedServices(ctx, ap.srv.ID)
	if err != nil {
		return fmt.Errorf("list unmanaged services: %w", err)
	}
	rows, err := q.ListServerCurrentServices(ctx, &ap.srv.ID)
	if err != nil {
		return fmt.Errorf("list current services: %w", err)
	}
	cur := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		cur[i] = r.Service.ID
	}
	if !stateOf(locked.AdoptionState == services.AdoptionStateHeld, cur, un).equal(ap.state) {
		return apierr.Conflict("adoption_changed", "The workloads of this server changed while you reviewed them; review again.")
	}
	return nil
}
