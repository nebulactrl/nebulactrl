package dns01

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HasResolver reports whether the server's desired resolvers include provider.
func HasResolver(ctx context.Context, q *db.Queries, serverID uuid.UUID, provider string) (bool, error) {
	rs, err := q.ListServerDNSResolvers(ctx, serverID)
	if err != nil {
		return false, fmt.Errorf("list dns resolvers: %w", err)
	}
	for _, r := range rs {
		if r.Provider == provider {
			return true, nil
		}
	}
	return false, nil
}

// WildcardCovered reports whether one of the server's desired wildcard
// certificates covers host (a wildcard host is covered by the wildcard of the
// same base). For a generated host the certificate must opt in to cover them.
func WildcardCovered(ctx context.Context, q *db.Queries, serverID uuid.UUID, host string, generated bool) (bool, error) {
	ws, err := q.ListServerWildcardCerts(ctx, serverID)
	if err != nil {
		return false, fmt.Errorf("list wildcard certificates: %w", err)
	}
	for _, w := range ws {
		spec := agentprotocol.WildcardCert{Domain: w.Domain, IncludeApex: w.IncludeApex}
		if agentprotocol.WildcardCovers(spec, host) && (!generated || w.CoverGenerated) {
			return true, nil
		}
	}
	return false, nil
}
