package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// RefEdge is a dependency derived from variable references: the variables Keys
// of service From contain `${{<To's slug>.KEY}}`.
type RefEdge struct {
	From, To uuid.UUID
	Keys     []string
	// Since is the oldest update time among the referencing variables.
	Since time.Time
}

// ReferenceEdges derives the consumer → provider edges of an environment from
// the references in its variables (self references are ignored; the same pair
// yields one edge listing every referencing key). Edges are ordered by
// consumer then provider creation order of the services.
func ReferenceEdges(ctx context.Context, q *db.Queries, box *secretbox.Box, envID uuid.UUID) ([]RefEdge, error) {
	svcs, err := q.ListAllEnvironmentServices(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	bySlug := map[string]uuid.UUID{}
	order := map[uuid.UUID]int{}
	for i, s := range svcs {
		bySlug[s.Slug] = s.ID
		order[s.ID] = i
	}
	vars, err := q.ListEnvironmentVariables(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	type pair struct{ from, to uuid.UUID }
	edges := map[pair]*RefEdge{}
	for _, v := range vars {
		if _, ok := order[v.ServiceID]; !ok {
			continue
		}
		val, err := box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(v.ServiceID.String(), v.Key), v.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt variable %s: %w", v.Key, err)
		}
		for _, ref := range References(val) {
			to, ok := bySlug[ref[0]]
			if !ok || to == v.ServiceID {
				continue
			}
			p := pair{v.ServiceID, to}
			e := edges[p]
			if e == nil {
				e = &RefEdge{From: p.from, To: p.to, Since: v.UpdatedAt}
				edges[p] = e
			}
			if v.UpdatedAt.Before(e.Since) {
				e.Since = v.UpdatedAt
			}
			dup := false
			for _, k := range e.Keys {
				if k == v.Key {
					dup = true
					break
				}
			}
			if !dup {
				e.Keys = append(e.Keys, v.Key)
			}
		}
	}
	out := make([]RefEdge, 0, len(edges))
	for _, e := range edges {
		sort.Strings(e.Keys)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if order[out[i].From] != order[out[j].From] {
			return order[out[i].From] < order[out[j].From]
		}
		return order[out[i].To] < order[out[j].To]
	})
	return out, nil
}

// refLinkID is the deterministic id of a derived link.
func refLinkID(from, to uuid.UUID) string { return "ref:" + from.String() + ":" + to.String() }

// isRefLinkID reports whether id has the shape of a derived link id.
func isRefLinkID(id string) bool { return strings.HasPrefix(id, "ref:") }

// MergeLinks combines manual links with derived reference edges. A pair that is
// both manual and referenced is reported once as manual with its references.
func MergeLinks(envID uuid.UUID, manual []db.ServiceLink, edges []RefEdge) []LinkView {
	type pair struct{ from, to uuid.UUID }
	out := make([]LinkView, 0, len(manual)+len(edges))
	idx := map[pair]int{}
	for _, m := range manual {
		idx[pair{m.FromServiceID, m.ToServiceID}] = len(out)
		out = append(out, toLinkView(m))
	}
	for _, e := range edges {
		keys := make([]LinkReferenceKey, len(e.Keys))
		for i, k := range e.Keys {
			keys[i] = LinkReferenceKey{Key: k}
		}
		if i, ok := idx[pair{e.From, e.To}]; ok {
			out[i].References = keys
			continue
		}
		out = append(out, LinkView{
			ID: refLinkID(e.From, e.To), EnvironmentID: envID, FromServiceID: e.From, ToServiceID: e.To,
			Source: LinkReference, References: keys, CreatedAt: e.Since,
		})
	}
	return out
}

// errNotFoundLink is the 404 for unknown and derived link ids.
func errNotFoundLink() error { return apierr.NotFound("link") }

// parseLinkID returns the UUID of a manual link id. Derived ("ref:…") and
// malformed ids are not deletable and yield 404.
func parseLinkID(id string) (uuid.UUID, error) {
	if isRefLinkID(id) {
		return uuid.Nil, errNotFoundLink()
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, errNotFoundLink()
	}
	return u, nil
}
