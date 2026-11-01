package uptime

import (
	"context"
	"fmt"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// SendReminders repeats uptime.down for every outage that is still open and
// due: the monitor alerts and has reminderMinutes > 0, the outage's alert was
// sent, the service is down right now (maintenance, unknown and paused send
// nothing), and reminderMinutes passed since the last alert or reminder. Each
// reminder is recorded (reminded_at) before it is sent, so it goes out at most
// once even if two sweeps overlap. It returns how many reminders were sent.
//
// The sweep runs every minute, so a reminder is at most a minute late.
func (s *Service) SendReminders(ctx context.Context) (int, error) {
	now := s.now().UTC()
	rows, err := s.q.ListUptimeReminders(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("list uptime reminders: %w", err)
	}
	sent := 0
	for _, r := range rows {
		inc := r.UptimeIncident
		won, err := s.q.ClaimUptimeReminder(ctx, db.ClaimUptimeReminderParams{ID: inc.ID, RemindedAt: now, Previous: inc.RemindedAt})
		if err != nil {
			return sent, fmt.Errorf("claim uptime reminder: %w", err)
		}
		if won == 0 {
			continue // resolved, or another sweep sent it
		}
		s.emit(ctx, notice{
			reminder: true, workspaceID: inc.WorkspaceID, projectID: r.ProjectID, serviceID: inc.ServiceID,
			service: r.ServiceName, environment: r.EnvironmentName, project: r.ProjectName,
			cause: inc.Cause, duration: max(now.Sub(inc.StartedAt), 0),
		})
		sent++
	}
	return sent, nil
}

// RemindArgs sends the reminders of open outages.
type RemindArgs struct{}

// Kind implements river.JobArgs.
func (RemindArgs) Kind() string { return "uptime_remind" }

// remindEvery is how often the reminder sweep runs.
const remindEvery = time.Minute
