package services

import (
	"context"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// LinkProvider adds proposals a domain package knows about to the variables a
// link to a service proposes (a template's Bucket offers its S3 settings). It
// is called after the generic proposals; keys the generic rows already hold
// win.
type LinkProvider interface {
	// LinkProposals returns the extra proposals for linking to the service, or
	// nil when it has none.
	LinkProposals(ctx context.Context, q *db.Queries, to db.Service) ([]ProposedVariable, error)
}

// SetLinkProvider installs the extra link proposals source (wire time only).
func (s *Service) SetLinkProvider(p LinkProvider) { s.linkProvider = p }

// proposeLink is the full proposal list for linking to the target: the generic
// rows, then the provider's rows whose key is not already proposed. A provider
// error is logged and ignored: the generic rows stay usable.
func (s *Service) proposeLink(ctx context.Context, q *db.Queries, to Scope) []ProposedVariable {
	out := ProposeLinkVariables(to.Service.Slug, to.Service.Kind, to.Config())
	if s.linkProvider == nil {
		return out
	}
	extra, err := s.linkProvider.LinkProposals(ctx, q, to.Service)
	if err != nil {
		s.logger.Warn("link proposals from provider", "service", to.Service.ID, "err", err)
		return out
	}
	have := make(map[string]bool, len(out))
	for _, p := range out {
		have[p.Key] = true
	}
	for _, p := range extra {
		if have[p.Key] {
			continue
		}
		have[p.Key] = true
		out = append(out, p)
	}
	return out
}
