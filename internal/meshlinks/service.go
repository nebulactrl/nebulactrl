package meshlinks

import (
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Timings of the link domain.
const (
	// RepairInterval is how often MeshLinksReconcileJob looks at every
	// environment that has links or exposures.
	RepairInterval = 5 * time.Minute
	// ReferenceGrace is how long a grant whose variable reference is gone stays
	// (a reference removed and re-added, or a redeploy in between, keeps its
	// link). An edge's grant goes at once.
	ReferenceGrace = 10 * time.Minute
	// ReadyTimeout is how long a deployment waits for the links it needs.
	ReadyTimeout = 5 * time.Minute
)

// Service is the cross-server service link domain.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	box    *secretbox.Box
	mesh   mesh.Provider
	bus    *events.Bus
	logger *slog.Logger

	now   func() time.Time
	grace time.Duration

	// orphans remembers when the repair first saw a grant without a source. It
	// is in memory on purpose: a restart only extends the grace.
	mu      sync.Mutex
	orphans map[string]time.Time
}

// NewService returns the link domain. provider is the mesh (nil until the mesh
// is wired: every cross-server need then reports mesh_not_configured). The
// arguments may be nil for OpenAPI generation.
func NewService(pool *pgxpool.Pool, box *secretbox.Box, bus *events.Bus, provider mesh.Provider, logger *slog.Logger) *Service {
	s := &Service{
		pool: pool, box: box, mesh: provider, bus: bus, logger: logger,
		now: time.Now, grace: ReferenceGrace, orphans: map[string]time.Time{},
	}
	if pool != nil {
		s.q = db.New(pool)
	}
	if logger == nil {
		s.logger = slog.Default()
	}
	return s
}

// SetReferenceGrace overrides ReferenceGrace (NEBULACTRL_MESH_LINK_GRACE). A
// non-positive value keeps the current one.
func (s *Service) SetReferenceGrace(d time.Duration) {
	if d > 0 {
		s.grace = d
	}
}
