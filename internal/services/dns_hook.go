package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DNSHook is told when a service's domains were deleted with it, so the DNS
// records of their hostnames can be collected (the Cloudflare integration).
type DNSHook interface {
	DomainsRemoved(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error
}

// SetDNSHook installs the hook (wire time only).
func (s *Service) SetDNSHook(h DNSHook) { s.dnsHook = h }
