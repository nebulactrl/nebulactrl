// Package meshlinks implements cross-server service links: a consumer on one
// server reaches a provider (app, worker, database or
// replica) of the same environment on another server over the managed tailnet
// mesh.
//
// The package owns the question "which grants should exist": Derive computes
// the desired set from variable references, canvas edges and replicas, and
// Reconcile applies it through mesh.Provider. Links and the tailnet devices
// they cost are only ever ADDED by explicit actions (a deploy of the consumer,
// a canvas edge the user confirmed, a provider that moved server); the periodic
// repair only removes and re-creates rows of confirmed pairs.
package meshlinks

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Grant sources (mesh_grants.source).
const (
	SourceReference = "reference"
	SourceEdge      = "edge"
	SourceReplica   = "replica"
)

// Want is one grant that should exist: the consumer reaches the provider
// across servers, because of a variable reference or a canvas edge.
type Want struct {
	ConsumerID, ProviderID     uuid.UUID
	ConsumerSlug, ProviderSlug string
	ConsumerServer             uuid.UUID
	ProviderServer             uuid.UUID
	Source                     string
	// ProviderPort is the provider's container port.
	ProviderPort int
	// Keys lists the consumer's variables that reach the provider (references).
	Keys []string
	// NotExposable is set when the provider cannot be exposed on the mesh (a
	// route service, a Compose stack, no TCP port): the want is a blocker.
	NotExposable string
}

type pair struct{ consumer, provider uuid.UUID }

// Derived is the desired mesh state of one environment.
type Derived struct {
	Env      services.EnvInfo
	Services map[uuid.UUID]db.Service
	Wants    []Want
	// Edges are the canvas links between placed services (any servers).
	Edges map[pair]bool
	// MultiServer is true when the environment's services sit on at least two
	// servers: only then can a link be needed.
	MultiServer bool
}

// placedServers counts the distinct servers of the services of an environment.
func placedServers(svcs []db.Service) int {
	seen := map[uuid.UUID]bool{}
	for _, s := range svcs {
		if s.ServerID != nil {
			seen[*s.ServerID] = true
		}
	}
	return len(seen)
}

// NotExposableReason says why a provider cannot be exposed on the mesh ("" when it can).
func NotExposableReason(kind string, cfg services.Config) string {
	switch {
	case kind == services.KindRoute:
		return "a route service has no container to expose"
	case kind == services.KindStack:
		return "a Compose stack cannot be exposed on the mesh"
	case cfg.Port <= 0:
		return "it has no TCP port"
	}
	return ""
}

// consumes reports whether services of this kind take part as consumers: route
// services have no container and Compose stacks use their own networks.
func consumes(kind string) bool { return kind != services.KindRoute && kind != services.KindStack }

// Derive computes the desired grants of an environment from the variable
// references of its consumers (resolved in the consumer's context, so the
// targets of group exports are included) and from its canvas links. Services
// that are not placed, or are on the same server, need nothing.
func Derive(ctx context.Context, q *db.Queries, box *secretbox.Box, env services.EnvInfo) (Derived, error) {
	svcs, err := q.ListAllEnvironmentServices(ctx, env.ID)
	if err != nil {
		return Derived{}, fmt.Errorf("list services: %w", err)
	}
	d := Derived{Env: env, Services: make(map[uuid.UUID]db.Service, len(svcs)), Edges: map[pair]bool{}}
	for _, s := range svcs {
		d.Services[s.ID] = s
	}
	if d.MultiServer = placedServers(svcs) > 1; !d.MultiServer {
		return d, nil
	}
	r, err := services.LoadResolver(ctx, q, box, env)
	if err != nil {
		return Derived{}, err
	}
	seen := map[[3]string]int{}
	add := func(c, p db.Service, source string, keys []string) {
		if c.ServerID == nil || p.ServerID == nil || *c.ServerID == *p.ServerID || c.ID == p.ID || !consumes(c.Kind) {
			return
		}
		k := [3]string{c.ID.String(), p.ID.String(), source}
		i, ok := seen[k]
		if !ok {
			cfg := services.DecodeConfig(p.Config)
			d.Wants = append(d.Wants, Want{
				ConsumerID: c.ID, ProviderID: p.ID, ConsumerSlug: c.Slug, ProviderSlug: p.Slug,
				ConsumerServer: *c.ServerID, ProviderServer: *p.ServerID,
				Source: source, ProviderPort: cfg.Port, NotExposable: NotExposableReason(p.Kind, cfg),
			})
			i = len(d.Wants) - 1
			seen[k] = i
		}
		w := &d.Wants[i]
		for _, key := range keys {
			dup := false
			for _, have := range w.Keys {
				dup = dup || have == key
			}
			if !dup {
				w.Keys = append(w.Keys, key)
			}
		}
	}
	for _, c := range svcs {
		if c.ServerID == nil || !consumes(c.Kind) {
			continue
		}
		res, _, err := r.ResolveServiceLenient(c.Slug)
		if err != nil {
			return Derived{}, err
		}
		for _, u := range res.MeshUses {
			if p, ok := d.Services[u.ProviderID]; ok {
				add(c, p, SourceReference, u.Keys)
			}
		}
	}
	edges, err := q.ListEnvironmentLinks(ctx, env.ID)
	if err != nil {
		return Derived{}, fmt.Errorf("list links: %w", err)
	}
	for _, e := range edges {
		d.Edges[pair{e.FromServiceID, e.ToServiceID}] = true
		c, cok := d.Services[e.FromServiceID]
		p, pok := d.Services[e.ToServiceID]
		if cok && pok {
			add(c, p, SourceEdge, nil)
		}
	}
	for i := range d.Wants {
		sort.Strings(d.Wants[i].Keys)
	}
	sort.SliceStable(d.Wants, func(i, j int) bool {
		a, b := d.Wants[i], d.Wants[j]
		if a.ConsumerSlug != b.ConsumerSlug {
			return a.ConsumerSlug < b.ConsumerSlug
		}
		if a.ProviderSlug != b.ProviderSlug {
			return a.ProviderSlug < b.ProviderSlug
		}
		return a.Source < b.Source
	})
	return d, nil
}
