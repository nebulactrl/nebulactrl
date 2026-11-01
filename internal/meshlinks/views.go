package meshlinks

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Route states.
const (
	// RoutePending: the consumer has no link, exposure or grant for the provider
	// yet (the consumer's deployment creates them).
	RoutePending = "pending"
	// RouteGranted: the grant exists; the link's own state says whether traffic flows.
	RouteGranted = "granted"
)

// MeshServerRef names a server in a view (named apart from services.ServerRef: huma derives schema names from type names).
type MeshServerRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// EnvGrantView is one grant: a link may reach an exposure on behalf of a consumer.
type EnvGrantView struct {
	LinkID       uuid.UUID `json:"linkId"`
	ExposureID   uuid.UUID `json:"exposureId"`
	ConsumerID   uuid.UUID `json:"consumerId"`
	ConsumerSlug string    `json:"consumerSlug"`
	ProviderID   uuid.UUID `json:"providerId"`
	ProviderSlug string    `json:"providerSlug"`
	Source       string    `json:"source" enum:"reference,edge,replica"`
	MeshPort     int       `json:"meshPort"`
	CreatedAt    time.Time `json:"createdAt"`
}

// EnvLinkView is a link node of the environment.
type EnvLinkView struct {
	ID      uuid.UUID      `json:"id"`
	From    MeshServerRef  `json:"from" doc:"The consumer's server: the link node runs here"`
	To      MeshServerRef  `json:"to" doc:"The provider's server"`
	Purpose string         `json:"purpose" enum:"service,replication"`
	State   string         `json:"state"`
	Message string         `json:"message"`
	Grants  []EnvGrantView `json:"grants"`
}

// RouteView is how one consumer reaches one provider on another server.
type RouteView struct {
	ConsumerID   uuid.UUID `json:"consumerId"`
	ConsumerSlug string    `json:"consumerSlug"`
	ProviderID   uuid.UUID `json:"providerId"`
	// Provider is the provider's slug, which is also the alias the consumer dials.
	Provider string        `json:"provider"`
	Server   MeshServerRef `json:"server" doc:"The provider's server"`
	Alias    string        `json:"alias"`
	// Port is the provider's mesh port (0 while it has no exposure): what the
	// consumer dials instead of the container port.
	Port int `json:"port"`
	// State is pending, granted or the link's state (online, offline, ...).
	State   string   `json:"state"`
	Source  string   `json:"source" doc:"The first source of the route (reference or edge)"`
	Sources []string `json:"sources"`
	// Keys lists the consumer's variables that reach the provider.
	Keys []string `json:"keys"`
	// DevicesAdded is 1 while the link of this route does not exist yet.
	DevicesAdded int `json:"devicesAdded"`
	// NotExposable is set when the provider cannot be reached over the mesh.
	NotExposable string `json:"notExposable,omitempty"`
}

// EnvironmentMeshView is GET /environments/{id}/mesh.
type EnvironmentMeshView struct {
	Links  []EnvLinkView  `json:"links"`
	Grants []EnvGrantView `json:"grants"`
	Routes []RouteView    `json:"routes"`
}

// ExposureView is a service port reachable on its server's mesh node.
type ExposureView struct {
	ID            uuid.UUID     `json:"id"`
	Server        MeshServerRef `json:"server"`
	Purpose       string        `json:"purpose" enum:"service,replication"`
	ContainerPort int           `json:"containerPort"`
	MeshPort      int           `json:"meshPort"`
	// Alias is the network alias the service's containers carry while exposed.
	Alias string `json:"alias"`
}

// ConsumerView is a service that reaches this one over the mesh.
type ConsumerView struct {
	ServiceID uuid.UUID     `json:"serviceId"`
	Slug      string        `json:"slug"`
	Server    MeshServerRef `json:"server"`
	Source    string        `json:"source"`
	State     string        `json:"state"`
}

// ServiceMeshView is GET /services/{id}/mesh.
type ServiceMeshView struct {
	Exposures []ExposureView `json:"exposures"`
	Routes    []RouteView    `json:"routes" doc:"The providers on other servers this service reaches"`
	Consumers []ConsumerView `json:"consumers" doc:"Services on other servers that reach this one"`
}

// routes builds the route views of wants.
func (s *Service) routes(st *state, names map[uuid.UUID]string, wants []Want) []RouteView {
	type rk struct{ consumer, provider uuid.UUID }
	idx := map[rk]int{}
	out := []RouteView{}
	for _, w := range wants {
		k := rk{w.ConsumerID, w.ProviderID}
		i, ok := idx[k]
		if !ok {
			rv := RouteView{
				ConsumerID: w.ConsumerID, ConsumerSlug: w.ConsumerSlug, ProviderID: w.ProviderID, Provider: w.ProviderSlug,
				Server: MeshServerRef{ID: w.ProviderServer, Name: names[w.ProviderServer]}, Alias: w.ProviderSlug,
				State: RoutePending, Sources: []string{}, Keys: []string{}, NotExposable: w.NotExposable,
			}
			link, lok := st.linkByKey[w.linkKey()]
			if !lok {
				rv.DevicesAdded = 1
			}
			if ex, eok := st.exposureOfService(w.ProviderID); eok {
				rv.Port = int(ex.MeshPort)
				if lok {
					if _, granted := st.grants[grantKey{link.ID, ex.ID, w.ConsumerID, w.Source}]; granted {
						rv.State = RouteGranted
						if link.State != "" {
							rv.State = link.State
						}
					}
				}
			}
			out = append(out, rv)
			i = len(out) - 1
			idx[k] = i
		}
		rv := &out[i]
		rv.Sources = append(rv.Sources, w.Source)
		rv.Keys = append(rv.Keys, w.Keys...)
	}
	for i := range out {
		sort.Strings(out[i].Sources)
		out[i].Source = out[i].Sources[0]
		out[i].Keys = dedupe(out[i].Keys)
	}
	return out
}

func dedupe(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// EnvironmentMesh lists the links, grants and routes of an environment.
func (s *Service) EnvironmentMesh(ctx context.Context, envID uuid.UUID) (EnvironmentMeshView, error) {
	es, err := services.LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID)
	if err != nil {
		return EnvironmentMeshView{}, err
	}
	env, err := services.LoadEnvInfo(ctx, s.q, es.Env.ID)
	if err != nil {
		return EnvironmentMeshView{}, err
	}
	d, err := Derive(ctx, s.q, s.box, env)
	if err != nil {
		return EnvironmentMeshView{}, err
	}
	st, err := loadState(ctx, s.q, envID)
	if err != nil {
		return EnvironmentMeshView{}, err
	}
	names, err := s.namesFor(ctx, d, st)
	if err != nil {
		return EnvironmentMeshView{}, err
	}
	out := EnvironmentMeshView{Links: []EnvLinkView{}, Grants: []EnvGrantView{}}
	grants := s.grantViews(d, st)
	byLink := map[uuid.UUID][]EnvGrantView{}
	for _, g := range grants {
		byLink[g.LinkID] = append(byLink[g.LinkID], g)
	}
	links := make([]db.MeshLink, 0, len(st.links))
	for _, l := range st.links {
		links = append(links, l)
	}
	sort.Slice(links, func(i, j int) bool {
		if !links[i].CreatedAt.Equal(links[j].CreatedAt) {
			return links[i].CreatedAt.Before(links[j].CreatedAt)
		}
		return links[i].ID.String() < links[j].ID.String()
	})
	for _, l := range links {
		gv := byLink[l.ID]
		if gv == nil {
			gv = []EnvGrantView{}
		}
		out.Links = append(out.Links, EnvLinkView{
			ID: l.ID, From: MeshServerRef{ID: l.ServerID, Name: names[l.ServerID]}, To: MeshServerRef{ID: l.RemoteServerID, Name: names[l.RemoteServerID]},
			Purpose: l.Purpose, State: l.State, Message: l.Message, Grants: gv,
		})
	}
	out.Grants = grants
	out.Routes = s.routes(st, names, d.Wants)
	return out, nil
}

// ServiceMesh lists what one service exposes, which providers on other servers
// it reaches and which services on other servers reach it.
func (s *Service) ServiceMesh(ctx context.Context, serviceID uuid.UUID) (ServiceMeshView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return ServiceMeshView{}, err
	}
	d, err := Derive(ctx, s.q, s.box, sc.EnvInfo())
	if err != nil {
		return ServiceMeshView{}, err
	}
	st, err := loadState(ctx, s.q, sc.EnvironmentID)
	if err != nil {
		return ServiceMeshView{}, err
	}
	names, err := s.namesFor(ctx, d, st)
	if err != nil {
		return ServiceMeshView{}, err
	}
	out := ServiceMeshView{Exposures: []ExposureView{}, Consumers: []ConsumerView{}}
	for _, ex := range st.exposures {
		if ex.ServiceID != serviceID {
			continue
		}
		out.Exposures = append(out.Exposures, ExposureView{
			ID: ex.ID, Server: MeshServerRef{ID: ex.ServerID, Name: names[ex.ServerID]}, Purpose: ex.Purpose,
			ContainerPort: int(ex.ContainerPort), MeshPort: int(ex.MeshPort), Alias: agentprotocol.MeshServiceAlias(serviceID.String()),
		})
	}
	sort.Slice(out.Exposures, func(i, j int) bool { return out.Exposures[i].Purpose < out.Exposures[j].Purpose })
	out.Routes = s.routes(st, names, wantsOfConsumer(d, serviceID))
	var asProvider []Want
	for _, w := range d.Wants {
		if w.ProviderID == serviceID {
			asProvider = append(asProvider, w)
		}
	}
	for _, w := range asProvider {
		state := RoutePending
		if link, ok := st.linkByKey[w.linkKey()]; ok {
			if ex, eok := st.exposureOfService(w.ProviderID); eok {
				if _, granted := st.grants[grantKey{link.ID, ex.ID, w.ConsumerID, w.Source}]; granted {
					state = link.State
					if state == "" {
						state = RouteGranted
					}
				}
			}
		}
		out.Consumers = append(out.Consumers, ConsumerView{
			ServiceID: w.ConsumerID, Slug: w.ConsumerSlug, Server: MeshServerRef{ID: w.ConsumerServer, Name: names[w.ConsumerServer]},
			Source: w.Source, State: state,
		})
	}
	return out, nil
}

func (s *Service) namesFor(ctx context.Context, d Derived, st *state) (map[uuid.UUID]string, error) {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, w := range d.Wants {
		add(w.ConsumerServer)
		add(w.ProviderServer)
	}
	for _, l := range st.links {
		add(l.ServerID)
		add(l.RemoteServerID)
	}
	for _, e := range st.exposures {
		add(e.ServerID)
	}
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	return serverNames(ctx, s.q, ids)
}

func (s *Service) grantViews(d Derived, st *state) []EnvGrantView {
	out := []EnvGrantView{}
	for _, g := range st.grants {
		ref := s.refOf(d, st, g)
		gv := EnvGrantView{
			LinkID: g.LinkID, ExposureID: g.ExposureID, ConsumerID: g.RefID, ConsumerSlug: ref.ConsumerSlug,
			ProviderID: ref.ProviderID, ProviderSlug: ref.ProviderSlug, Source: g.Source, CreatedAt: g.CreatedAt,
		}
		if ex, ok := st.exposureByID[g.ExposureID]; ok {
			gv.MeshPort = int(ex.MeshPort)
		}
		out = append(out, gv)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ConsumerSlug != b.ConsumerSlug {
			return a.ConsumerSlug < b.ConsumerSlug
		}
		if a.ProviderSlug != b.ProviderSlug {
			return a.ProviderSlug < b.ProviderSlug
		}
		return a.Source < b.Source
	})
	return out
}

// ReconcileView is the answer of POST /environments/{id}/mesh/reconcile.
type ReconcileView struct {
	Mode         string        `json:"mode" enum:"add,repair"`
	Added        []GrantChange `json:"added"`
	Removed      []GrantChange `json:"removed"`
	Waiting      int           `json:"waiting" doc:"Grants kept for their grace period"`
	DevicesAdded int           `json:"devicesAdded"`
	Restaged     []uuid.UUID   `json:"restaged" doc:"Consumers whose resolved addresses changed; their staged bar shows it"`
}

// GrantChange is one grant a reconciliation added or removed.
type GrantChange struct {
	Consumer string `json:"consumer"`
	Provider string `json:"provider"`
	Source   string `json:"source"`
}

func changes(in []GrantRef) []GrantChange {
	out := make([]GrantChange, 0, len(in))
	for _, r := range in {
		out = append(out, GrantChange{Consumer: r.ConsumerSlug, Provider: r.ProviderSlug, Source: r.Source})
	}
	return out
}

// ReconcileNow runs a reconciliation on request. ModeAdd adds every need of the
// environment and needs acceptMeshChanges when it adds tailnet devices; ModeRepair
// only removes (and re-creates rows of existing links).
func (s *Service) ReconcileNow(ctx context.Context, envID uuid.UUID, mode Mode, accept bool) (ReconcileView, error) {
	es, err := services.LoadEnvironment(ctx, s.q, policy.ScopeServersWrite, envID)
	if err != nil {
		return ReconcileView{}, err
	}
	if !mode.Valid() {
		return ReconcileView{}, apierr.Unprocessable("validation_failed", "mode must be add or repair")
	}
	if err := es.Guard(ctx, "reconcileEnvironmentMesh", fmt.Sprintf("Reconcile the mesh of the production environment %s", es.Env.Name), false,
		[]string{"Missing links, exposures and grants are created and tailnet devices are added at once."}, string(mode), accept); err != nil {
		return ReconcileView{}, err
	}
	opts := Options{All: true}
	if mode == ModeAdd {
		env, err := services.LoadEnvInfo(ctx, s.q, envID)
		if err != nil {
			return ReconcileView{}, err
		}
		d, err := Derive(ctx, s.q, s.box, env)
		if err != nil {
			return ReconcileView{}, err
		}
		plan, err := s.PlanWants(ctx, s.q, env, d.Wants)
		if err != nil {
			return ReconcileView{}, err
		}
		if len(plan.Blockers) > 0 {
			b := plan.Blockers[0]
			return ReconcileView{}, apierr.Unprocessable(b.Code, b.Message)
		}
		if plan.DevicesAdded > 0 && !accept {
			return ReconcileView{}, apierr.MeshConfirmationRequired(plan.DevicesAdded)
		}
	}
	var res Result
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if res, err = s.Reconcile(ctx, tx, envID, mode, opts); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "mesh.links.reconcile", ResourceType: "environment", ResourceID: envID.String(), ProjectID: &es.Env.ProjectID,
			Metadata: map[string]any{"mode": string(mode), "added": len(res.Added), "removed": len(res.Removed), "devicesAdded": res.DevicesAdded},
		})
	})
	if err != nil {
		return ReconcileView{}, fmt.Errorf("reconcile mesh links: %w", err)
	}
	s.Publish(res)
	return ReconcileView{
		Mode: string(mode), Added: changes(res.Added), Removed: changes(res.Removed), Waiting: res.Waiting,
		DevicesAdded: res.DevicesAdded, Restaged: res.Restaged,
	}, nil
}
