package deployments

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DNSHook keeps DNS records in step with where a service runs (the Cloudflare
// integration). It runs inside the transaction that records a successful
// deployment, so a move to another server (a drain included) retargets the
// service's records once the new server is live.
type DNSHook interface {
	EnqueueSyncService(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID) error
}

// SetDNS installs the DNS hook (wire time only).
func (s *Service) SetDNS(h DNSHook) { s.dns = h }
