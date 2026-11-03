package templates

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// LinkProposals implements services.LinkProvider: linking to a service that
// came from a template proposes the variables the template's service declares
// in `provides`. The installed snapshot is read, so the proposals match the
// template as installed; an install from before snapshots falls back to the
// catalog.
func (s *Service) LinkProposals(ctx context.Context, q *db.Queries, to db.Service) ([]services.ProposedVariable, error) {
	if to.TemplateInstanceID == nil || to.TemplateKey == nil || strings.HasPrefix(*to.TemplateKey, "port:") {
		return nil, nil
	}
	inst, err := q.GetTemplateInstance(ctx, *to.TemplateInstanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get template instance: %w", err)
	}
	var tpl *CatalogTemplate
	if len(inst.TemplateSnapshot) > 0 {
		if tpl, err = decodeSnapshot(inst.TemplateSnapshot); err != nil {
			return nil, err
		}
	} else if s.Catalog != nil {
		tpl, _ = s.Catalog.Get(inst.TemplateID)
	}
	if tpl == nil {
		return nil, nil
	}
	key := *to.TemplateKey
	for _, sv := range tpl.Services {
		if sv.Key != key {
			continue
		}
		out := make([]services.ProposedVariable, 0, len(sv.Provides))
		for _, p := range sv.Provides {
			out = append(out, services.ProposedVariable{
				Key: p.Key, Value: rewriteRefs(p.Value, map[string]string{key: to.Slug}),
				Recommended: p.Recommended, Description: p.Description, Secret: p.Secret,
			})
		}
		return out, nil
	}
	return nil, nil
}
