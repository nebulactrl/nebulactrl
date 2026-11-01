package domains

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/hostclaim"
)

// checkHostClaim refuses a hostname inside a Cloudflare zone another team
// connected (package hostclaim). selfID is the domain being updated: its own
// hostname stays editable even when a claim appeared after it was created, only
// a new or renamed hostname is judged.
func (s *Service) checkHostClaim(ctx context.Context, q *db.Queries, workspaceID uuid.UUID, host string, selfID uuid.UUID) error {
	if selfID != uuid.Nil {
		if cur, err := q.GetDomainByID(ctx, selfID); err == nil && strings.EqualFold(cur.Hostname, host) {
			return nil
		}
	}
	return hostclaim.Check(ctx, q, workspaceID, "domain_conflict", host)
}

// teamCandidate is the last generated-hostname candidate: the first label with
// the team's own short id appended, so the name is tied to the team and no
// other team can squat all the plain candidates and leave it without one.
func teamCandidate(host string, workspaceID uuid.UUID) string {
	first, rest, _ := strings.Cut(host, ".")
	suffix := "-" + strings.ReplaceAll(workspaceID.String(), "-", "")[:8]
	if len(first)+len(suffix) > maxLabelLen {
		first = strings.Trim(first[:maxLabelLen-len(suffix)], "-")
	}
	return first + suffix + "." + rest
}
