package meshlinks

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Link purpose of every link this package manages (mesh_links.purpose).
const purposeService = "service"

// linkKey identifies the link node of an environment: the consumer's server
// reaches the provider's server through it.
type linkKey struct{ consumer, provider uuid.UUID }

func (w Want) linkKey() linkKey { return linkKey{w.ConsumerServer, w.ProviderServer} }

// PlanWants says what serving wants needs from the mesh: the links (existing
// ones included), the tailnet devices it adds and what blocks it. It never
// writes. Wants whose provider cannot be exposed are blockers.
func (s *Service) PlanWants(ctx context.Context, q *db.Queries, env services.EnvInfo, wants []Want) (services.MeshPlan, error) {
	plan := services.EmptyMeshPlan()
	if len(wants) == 0 {
		return plan, nil
	}
	plan.CrossServer = true

	type group struct {
		key       linkKey
		providers map[string]bool
	}
	groups := map[linkKey]*group{}
	var order []linkKey
	seenBlock := map[string]bool{}
	block := func(b services.MeshBlocker) {
		k := b.Code + "\x00" + b.Message
		if !seenBlock[k] {
			seenBlock[k] = true
			plan.Blockers = append(plan.Blockers, b)
		}
	}
	for _, w := range wants {
		if w.NotExposable != "" {
			block(services.MeshBlocker{
				Code:    apierr.CodeMeshNotExposable,
				Message: fmt.Sprintf("%s cannot be reached over the mesh: %s", w.ProviderSlug, w.NotExposable),
			})
			continue
		}
		g := groups[w.linkKey()]
		if g == nil {
			g = &group{key: w.linkKey(), providers: map[string]bool{}}
			groups[w.linkKey()] = g
			order = append(order, w.linkKey())
		}
		g.providers[w.ProviderSlug] = true
	}
	if len(order) == 0 {
		return plan, nil
	}

	ids := make([]uuid.UUID, 0, 2*len(order))
	for _, k := range order {
		ids = append(ids, k.consumer, k.provider)
	}
	names, err := serverNames(ctx, q, ids)
	if err != nil {
		return services.MeshPlan{}, err
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if names[a.consumer] != names[b.consumer] {
			return names[a.consumer] < names[b.consumer]
		}
		return names[a.provider] < names[b.provider]
	})

	for _, k := range order {
		g := groups[k]
		pl := services.MeshPlanLink{
			FromServerID: k.consumer, FromServerName: names[k.consumer],
			ToServerID: k.provider, ToServerName: names[k.provider],
			Purpose: purposeService, State: "new", Providers: sortedKeys(g.providers),
		}
		if s.mesh == nil {
			block(services.MeshBlocker{Code: apierr.CodeMeshNotConfigured, Message: apierr.MeshNotConfigured().Message})
			plan.Links = append(plan.Links, pl)
			continue
		}
		lp, err := s.mesh.PlanLink(ctx, q, env.ID, k.consumer, k.provider, purposeService)
		if err != nil {
			return services.MeshPlan{}, err
		}
		for _, b := range lp.Blockers {
			block(services.MeshBlocker{Code: b.Code, Message: b.Message})
		}
		if lp.LinkExists {
			link, err := q.GetMeshLinkByKey(ctx, db.GetMeshLinkByKeyParams{
				EnvironmentID: env.ID, ServerID: k.consumer, RemoteServerID: k.provider, Purpose: purposeService,
			})
			switch {
			case err == nil:
				pl.State = link.State
			case errors.Is(err, pgx.ErrNoRows):
			default:
				return services.MeshPlan{}, fmt.Errorf("get mesh link: %w", err)
			}
		}
		plan.DevicesAdded += lp.DevicesAdded
		plan.Links = append(plan.Links, pl)
	}
	return plan, nil
}

func serverNames(ctx context.Context, q *db.Queries, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := q.LoadServerRows(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load servers: %w", err)
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wantsOfConsumer filters wants to the ones consumer id reaches.
func wantsOfConsumer(d Derived, id uuid.UUID) []Want {
	var out []Want
	for _, w := range d.Wants {
		if w.ConsumerID == id {
			out = append(out, w)
		}
	}
	return out
}

// PlanCheck is the mesh part of a deployment plan of sc: the links the service
// needs to reach providers on other servers, the devices they add and the
// blockers. A service on a single-server environment (or one that reaches no
// remote provider) gets the empty plan.
func (s *Service) PlanCheck(ctx context.Context, q *db.Queries, sc services.Scope) (services.MeshPlan, error) {
	if sc.Service.ServerID == nil || !consumes(sc.Service.Kind) {
		return services.EmptyMeshPlan(), nil
	}
	d, err := Derive(ctx, q, s.box, sc.EnvInfo())
	if err != nil {
		return services.MeshPlan{}, err
	}
	return s.PlanWants(ctx, q, d.Env, wantsOfConsumer(d, sc.Service.ID))
}

// PlanEdge is the mesh plan of the canvas edge from -> to: nothing when either
// service is unplaced or both run on one server.
func (s *Service) PlanEdge(ctx context.Context, envID, fromID, toID uuid.UUID) (services.MeshPlan, error) {
	return s.planEdge(ctx, s.q, envID, fromID, toID)
}

func (s *Service) planEdge(ctx context.Context, q *db.Queries, envID, fromID, toID uuid.UUID) (services.MeshPlan, error) {
	w, ok, err := s.edgeWant(ctx, q, fromID, toID)
	if err != nil || !ok {
		return services.EmptyMeshPlan(), err
	}
	env, err := services.LoadEnvInfo(ctx, q, envID)
	if err != nil {
		return services.MeshPlan{}, err
	}
	return s.PlanWants(ctx, q, env, []Want{w})
}

// edgeWant is the grant a canvas edge from -> to asks for (ok false for services
// on one server, unplaced services or a consumer that cannot take part).
func (s *Service) edgeWant(ctx context.Context, q *db.Queries, fromID, toID uuid.UUID) (Want, bool, error) {
	c, err := services.LoadScopeInternal(ctx, q, fromID)
	if err != nil {
		return Want{}, false, err
	}
	p, err := services.LoadScopeInternal(ctx, q, toID)
	if err != nil {
		return Want{}, false, err
	}
	cs, ps := c.Service, p.Service
	if cs.ServerID == nil || ps.ServerID == nil || *cs.ServerID == *ps.ServerID || !consumes(cs.Kind) {
		return Want{}, false, nil
	}
	cfg := services.DecodeConfig(ps.Config)
	return Want{
		ConsumerID: cs.ID, ProviderID: ps.ID, ConsumerSlug: cs.Slug, ProviderSlug: ps.Slug,
		ConsumerServer: *cs.ServerID, ProviderServer: *ps.ServerID,
		Source: SourceEdge, ProviderPort: cfg.Port, NotExposable: NotExposableReason(ps.Kind, cfg),
	}, true, nil
}

// Alias is the extra network alias the deployed service must carry when it is
// exposed on the mesh (DeployPayload.MeshAlias): "" when it has no exposure on
// its server. Any exposed service gets it, databases included.
func (s *Service) Alias(ctx context.Context, q *db.Queries, serviceID uuid.UUID) (string, error) {
	rows, err := q.ListMeshExposuresOfService(ctx, serviceID)
	if err != nil {
		return "", fmt.Errorf("list exposures: %w", err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return agentprotocol.MeshServiceAlias(serviceID.String()), nil
}
