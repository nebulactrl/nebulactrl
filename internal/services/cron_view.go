package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// decorateCron fills ServiceView.Cron for every service whose deployed release
// is a cron service (out is parallel to rows): the hidden schedule task and
// its newest run.
func decorateCron(ctx context.Context, q *db.Queries, rows []db.Service, out []ServiceView) error {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if len(ids) == 0 {
		return nil
	}
	states, err := q.ListServiceCronState(ctx, ids)
	if err != nil {
		return fmt.Errorf("list cron state: %w", err)
	}
	by := make(map[uuid.UUID]db.ListServiceCronStateRow, len(states))
	for _, st := range states {
		by[st.ServiceID] = st
	}
	for i, r := range rows {
		st, ok := by[r.ID]
		if !ok {
			continue
		}
		v := &CronView{TaskID: st.TaskID, Schedule: st.Schedule, Timezone: st.Timezone, NextRunAt: st.NextRunAt}
		if st.RunID != nil {
			run := &CronRunView{ID: *st.RunID, StartedAt: st.RunStartedAt, FinishedAt: st.RunFinishedAt, ExitCode: st.RunExitCode}
			if st.RunStatus != nil {
				run.Status = *st.RunStatus
			}
			v.LastRun = run
		}
		out[i].Cron = v
	}
	return nil
}
