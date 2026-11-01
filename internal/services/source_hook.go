package services

import (
	"context"

	"github.com/google/uuid"
)

// SourceHook is told when a git service is created or its source changes
// (repository, branch, build context, server or access token), so the
// repository's template env files (.env.example, …) and its project config
// file (nebula.toml) can be read before the first deploy (internal/variables,
// internal/configsync). It must return quickly: it runs in the request.
type SourceHook interface {
	GitSourceChanged(ctx context.Context, sc Scope)
}

// sourceHooks calls several hooks in order.
type sourceHooks []SourceHook

func (m sourceHooks) GitSourceChanged(ctx context.Context, sc Scope) {
	for _, h := range m {
		h.GitSourceChanged(ctx, sc)
	}
}

// AddSourceHook registers a hook (wire time only); hooks run in registration
// order.
func (s *Service) AddSourceHook(h SourceHook) {
	switch cur := s.sourceHook.(type) {
	case nil:
		s.sourceHook = h
	case sourceHooks:
		cur = append(cur, h)
		s.sourceHook = cur
	default:
		s.sourceHook = sourceHooks{cur, h}
	}
}

// gitSourceChanged calls the hook for a new git service (old is nil) or when
// the service's git source differs from old/oldServer; force reports a change
// that matters without showing in the config (a new access token).
func (s *Service) gitSourceChanged(ctx context.Context, sc Scope, old *Config, oldServer *uuid.UUID, force bool) {
	if s.sourceHook == nil || sc.Service.SourceType != SourceGit {
		return
	}
	if old != nil && !force {
		cfg := sc.Config()
		sameServer := (oldServer == nil) == (sc.Service.ServerID == nil) && (oldServer == nil || *oldServer == *sc.Service.ServerID)
		if old.GitURL == cfg.GitURL && old.GitBranch == cfg.GitBranch && old.BuildContext == cfg.BuildContext && sameServer {
			return
		}
	}
	s.sourceHook.GitSourceChanged(ctx, sc)
}
