package servers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ServerPatchSummary is the patching state of a server as the server views show
// it (internal/servers/patching has the full panel and the actions).
type ServerPatchSummary struct {
	Known          bool       `json:"known" doc:"A check for OS updates has completed at least once"`
	Supported      bool       `json:"supported" doc:"The host has a supported package manager (apt or dnf)"`
	Updates        int        `json:"updates" doc:"Pending package upgrades"`
	Security       int        `json:"security" doc:"How many of them are security updates"`
	RebootRequired bool       `json:"rebootRequired" doc:"The host asks for a reboot"`
	CheckedAt      *time.Time `json:"checkedAt"`
	Busy           string     `json:"busy" enum:"check,apply,reboot," doc:"The check, upgrade or reboot in flight, if any"`
}

func (s *Service) patchesByServer(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ServerPatchSummary, error) {
	out := map[uuid.UUID]ServerPatchSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListServerPatchSummaries(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list server patch summaries: %w", err)
	}
	for _, r := range rows {
		out[r.ServerID] = patchSummary(r)
	}
	busy, err := s.q.ListBusyServerPatchRuns(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list busy patch runs: %w", err)
	}
	for _, b := range busy {
		sum := out[b.ServerID]
		if sum.Busy == "" {
			sum.Busy = b.Action
			out[b.ServerID] = sum
		}
	}
	return out, nil
}

func patchSummary(r db.ListServerPatchSummariesRow) ServerPatchSummary {
	return ServerPatchSummary{
		Known: r.CheckedAt != nil, Supported: r.Supported, Updates: int(r.Updates), Security: int(r.SecurityUpdates),
		RebootRequired: r.RebootRequired, CheckedAt: r.CheckedAt, Busy: r.Busy,
	}
}
