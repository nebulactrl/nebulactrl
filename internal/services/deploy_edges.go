package services

import (
	"context"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// DeployEdges returns the consumer → provider dependencies that order a staged
// deploy: the edges derived from variable references plus the manual links of
// the environment (templates record `dependsOn` as manual links), so providers
// deploy first and one-shot providers finish before their consumers start.
func DeployEdges(ctx context.Context, q *db.Queries, box *secretbox.Box, envID uuid.UUID) ([]RefEdge, error) {
	edges, err := ReferenceEdges(ctx, q, box, envID)
	if err != nil {
		return nil, err
	}
	links, err := q.ListEnvironmentLinks(ctx, envID)
	if err != nil {
		return nil, err
	}
	type pair struct{ from, to uuid.UUID }
	have := make(map[pair]bool, len(edges))
	for _, e := range edges {
		have[pair{e.From, e.To}] = true
	}
	for _, l := range links {
		p := pair{l.FromServiceID, l.ToServiceID}
		if l.FromServiceID == l.ToServiceID || have[p] {
			continue
		}
		have[p] = true
		edges = append(edges, RefEdge{From: l.FromServiceID, To: l.ToServiceID, Since: l.CreatedAt})
	}
	// Route services deploy after the managed services they point at.
	routes, err := q.ListEnvironmentRouteServices(ctx, envID)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		rc := DecodeConfig(r.Config).Route
		if rc == nil {
			continue
		}
		for _, u := range rc.Upstreams {
			if u.ServiceID == nil || *u.ServiceID == r.ID || have[pair{r.ID, *u.ServiceID}] {
				continue
			}
			have[pair{r.ID, *u.ServiceID}] = true
			edges = append(edges, RefEdge{From: r.ID, To: *u.ServiceID, Since: r.CreatedAt})
		}
	}
	return edges, nil
}
