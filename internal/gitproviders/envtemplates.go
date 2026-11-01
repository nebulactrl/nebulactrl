package gitproviders

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/envtemplate"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ReadEnvTemplates reads the template env files (.env.example, .env.sample,
// .env.template, .env.dist) in the repository root and in contextDir through
// the provider's API, without cloning. A missing file is skipped; any other
// provider error (rate limit, revoked credentials) is returned. It never reads
// .env, .env.local or .env.production. The result is nil when nothing was found.
func ReadEnvTemplates(ctx context.Context, r *RepoReader, contextDir string) ([]agentprotocol.EnvFileScan, error) {
	return envtemplate.Scan(contextDir, func(p string) ([]byte, bool, error) {
		data, err := r.Get(ctx, p)
		if errors.Is(err, ErrFileNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return data, true, nil
	})
}

// ServiceRepo returns the connection and repository a service is linked to;
// ok is false for a service without a git link (a pasted URL).
func (s *Service) ServiceRepo(ctx context.Context, serviceID uuid.UUID) (connectionID uuid.UUID, repo string, ok bool, err error) {
	sg, err := s.q.GetServiceGit(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", false, nil
	}
	if err != nil {
		return uuid.Nil, "", false, err
	}
	return sg.ConnectionID, sg.RepoFullName, true, nil
}
