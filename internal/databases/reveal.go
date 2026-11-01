package databases

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// RevealTTL is how long a revealed value is meant to stay on screen.
const RevealTTL = 60 * time.Second

// Revealed is the response of the reveal action.
type Revealed struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	RevealedAt time.Time `json:"revealedAt"`
	ExpiresAt  time.Time `json:"expiresAt" doc:"The value is meant to be shown for this long only; clients must not persist it"`
}

// Reveal returns one derived export of a managed database in plain text (for
// example DATABASE_URL, to connect with psql). It requires the deployments:write
// scope (admin included), is audited as `secret.reveal` in the same transaction
// and is never cached: every other read stays masked.
func (s *Service) Reveal(ctx context.Context, serviceID uuid.UUID, key string) (Revealed, error) {
	sc, spec, eng, err := s.managed(ctx, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return Revealed{}, err
	}
	vars, err := s.resolved(ctx, s.q, sc)
	if err != nil {
		return Revealed{}, err
	}
	var found *engines.Export
	for _, e := range engines.Exports(spec, sc.Service.Slug, spec.Port(), func(k string) (string, bool) { v, ok := vars[k]; return v, ok }) {
		if e.Key == key {
			e := e
			found = &e
			break
		}
	}
	if found == nil {
		return Revealed{}, apierr.NotFound("export").WithDetails(map[string]any{"engine": eng.ID, "key": key})
	}
	now := time.Now().UTC()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Keys only: the value never reaches the audit log.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "secret.reveal", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "key": key},
		})
	})
	if err != nil {
		return Revealed{}, fmt.Errorf("audit reveal: %w", err)
	}
	return Revealed{Key: key, Value: found.Value, RevealedAt: now, ExpiresAt: now.Add(RevealTTL)}, nil
}
