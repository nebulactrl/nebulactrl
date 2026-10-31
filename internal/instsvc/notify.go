package instsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// notifyQuota announces that a team's built-in backup storage crossed 80% or
// 100% of its quota: once to the team, and a copy to the team that owns the
// control-plane host, which is where the instance administrator's channels are.
// Titles and bodies carry no keys, buckets or endpoints.
func (s *Service) notifyQuota(ctx context.Context, r db.TeamServiceSlice, level int, used, quota int64) {
	if s.notifier == nil {
		return
	}
	typ, sev, what := notifications.TypeStorageQuotaWarning, notifications.SeverityWarning, "is nearly full"
	if level >= 100 {
		typ, sev, what = notifications.TypeStorageQuotaFull, notifications.SeverityError, "is full"
	}
	fields := map[string]string{"Used": formatBytes(used), "Quota": formatBytes(quota), "Team": r.TeamName}
	pct := used * 100 / quota
	body := fmt.Sprintf("%s of %s used (%d%%).", formatBytes(used), formatBytes(quota), pct)
	if level >= 100 {
		body += " New backups fail until space is freed. Existing backups are kept."
	} else {
		body += " Delete backups you no longer need, or ask the instance administrator for a larger quota."
	}
	notifications.Emit(ctx, s.notifier, notifications.Event{
		Type: typ, Severity: sev, WorkspaceID: r.TeamID, Fields: fields,
		Title: "Built-in backup storage " + what, Body: body,
	})

	cp, err := s.q.GetControlPlaneWorkspace(ctx)
	if errors.Is(err, pgx.ErrNoRows) || cp == r.TeamID {
		return
	}
	if err != nil {
		s.log.WarnContext(ctx, "find control-plane team for quota alert", "err", err)
		return
	}
	notifications.Emit(ctx, s.notifier, notifications.Event{
		Type: typ, Severity: sev, WorkspaceID: cp, Fields: fields,
		Title: fmt.Sprintf("Built-in backup storage of team %s %s", r.TeamName, what), Body: body,
	})
}

// formatBytes renders a size in binary units ("42.5 GiB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
