package servers

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DNSHook keeps DNS records in step with a server's address (the Cloudflare
// integration). It runs in the transaction that changes the address.
type DNSHook interface {
	// EnqueueSyncServer re-syncs the DNS records of the domains of every service placed on the server.
	EnqueueSyncServer(ctx context.Context, tx pgx.Tx, workspaceID, serverID uuid.UUID) error
}

// SetDNS installs the DNS hook (wire time only).
func (s *Service) SetDNS(h DNSHook) { s.dns = h }

// sameIP compares two optional addresses ("" and nil are the same).
func sameIP(a, b *string) bool {
	va, vb := "", ""
	if a != nil {
		va = *a
	}
	if b != nil {
		vb = *b
	}
	return va == vb
}
