// Package hostclaim decides whether a team may use a hostname. Hostnames are one
// global namespace (a unique index on domains and status pages), so without a
// claim rule one team could take names that belong to another and block it.
//
// A name is claimed by a Cloudflare zone a team connected: every name in an
// active zone is usable only by the team that connected it (the oldest
// connection wins when two teams connected the same zone; the longest zone name
// wins when zones nest). A pending zone proves nothing and claims nothing:
// Cloudflare accepts a zone for any domain until its name servers are
// delegated, so the claim needs the delegation. Names outside every zone stay
// first come, first served. A refusal never says who holds a name or whether
// anybody does: see Unavailable.
package hostclaim

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/cfzone"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// activeZone is the Cloudflare status of a zone whose name servers point at
// Cloudflare, so its connection's owner controls the domain. A zone without a
// status counts as active, as everywhere else in the integration.
const activeZone = "active"

// ClaimedByOther reports whether host lies in a zone another team connected. A
// wildcard host is judged by its base.
func ClaimedByOther(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, host string) (bool, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	host = strings.TrimPrefix(host, "*.")
	if host == "" {
		return false, nil
	}
	claims, err := q.ListCloudflareZoneClaims(ctx)
	if err != nil {
		return false, fmt.Errorf("list cloudflare zone claims: %w", err)
	}
	var owner uuid.UUID
	bestLen := 0
	for _, c := range claims { // oldest connection first: it keeps a tie
		for _, z := range cfzone.Decode(c.Zones) {
			if (z.Status != "" && z.Status != activeZone) || z.Name == "" {
				continue
			}
			if host != z.Name && !strings.HasSuffix(host, "."+z.Name) {
				continue
			}
			if len(z.Name) > bestLen {
				owner, bestLen = c.WorkspaceID, len(z.Name)
			}
		}
	}
	return bestLen > 0 && owner != workspaceID, nil
}

// Unavailable is the answer for a name that cannot be used: the same message
// whether another team holds it by a claim, a domain or a status page, so a
// caller learns nothing about another team's names.
func Unavailable(code, host string) error {
	return apierr.Conflict(code, "the hostname "+host+" is not available")
}

// Check returns Unavailable(code, host) when another team's claim covers host.
func Check(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, code, host string) error {
	other, err := ClaimedByOther(ctx, q, workspaceID, host)
	if err != nil {
		return err
	}
	if other {
		return Unavailable(code, host)
	}
	return nil
}
