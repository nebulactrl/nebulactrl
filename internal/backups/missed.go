package backups

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/svcalerts"
)

// MissedCheckInterval is how often schedules are checked for a missed backup.
const MissedCheckInterval = 15 * time.Minute

// Kinds of backup schedule tracked for the missed-backup alert.
const (
	missedKindDatabase = "database"
	missedKindVolume   = "volume"
)

// autoGrace bounds the automatic grace period (a quarter of the interval).
const (
	minAutoGrace = 15 * time.Minute
	maxAutoGrace = 6 * time.Hour
)

// BackupMissedArgs checks every enabled schedule for a missed backup.
type BackupMissedArgs struct{}

// Kind implements river.JobArgs.
func (BackupMissedArgs) Kind() string { return "backup_missed_check" }

type missedWorker struct {
	river.WorkerDefaults[BackupMissedArgs]
	s *Service
}

func (w *missedWorker) Work(ctx context.Context, _ *river.Job[BackupMissedArgs]) error {
	return w.s.CheckMissed(ctx)
}

// registerMissedJob adds the worker and its periodic schedule.
func registerMissedJob(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &missedWorker{s: svc}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(MissedCheckInterval),
		func() (river.JobArgs, *river.InsertOpts) { return BackupMissedArgs{}, nil },
		&river.PeriodicJobOpts{ID: "backup_missed_check"},
	))
}

// MissedDeadline returns the instant by which the backup that follows a
// successful one at ref must have succeeded: the schedule's next due time after
// ref plus a grace. grace 0 picks a quarter of the interval to the run after
// that, between 15 minutes and 6 hours.
func MissedDeadline(cron, tz string, ref time.Time, grace time.Duration) (time.Time, error) {
	due, err := nextRun(cron, tz, ref)
	if err != nil {
		return time.Time{}, err
	}
	if grace <= 0 {
		grace = minAutoGrace
		if after, err := nextRun(cron, tz, due); err == nil {
			grace = min(max(after.Sub(due)/4, minAutoGrace), maxAutoGrace)
		}
	}
	return due.Add(grace), nil
}

// missedCandidate is one enabled schedule with what the check needs.
type missedCandidate struct {
	kind        string
	subjectID   uuid.UUID // the schedule (database) or the service (volume)
	serviceID   uuid.UUID
	cron, tz    string
	destination string
	lastOK      *time.Time
	inflight    bool
	stopped     bool
	since       *time.Time
	notifiedAt  *time.Time
}

// missedState decides what to do with a candidate: track it, alert, or nothing.
type missedState int

const (
	missedNothing missedState = iota
	missedTrack               // not tracked yet: record the baseline
	missedAlert
)

// evaluateMissed is the pure decision. ref is the newer of the last success
// and the time the schedule was first seen enabled.
func evaluateMissed(c missedCandidate, now time.Time, grace time.Duration) (missedState, time.Time, time.Time, error) {
	if c.since == nil {
		return missedTrack, time.Time{}, time.Time{}, nil
	}
	ref := *c.since
	if c.lastOK != nil && c.lastOK.After(ref) {
		ref = *c.lastOK
	}
	if c.inflight {
		return missedNothing, ref, time.Time{}, nil
	}
	deadline, err := MissedDeadline(c.cron, c.tz, ref, grace)
	if err != nil {
		return missedNothing, ref, time.Time{}, err
	}
	if !now.After(deadline) {
		return missedNothing, ref, deadline, nil
	}
	if c.notifiedAt != nil && !c.notifiedAt.Before(ref) {
		return missedNothing, ref, deadline, nil // already announced for this streak
	}
	return missedAlert, ref, deadline, nil
}

// CheckMissed sends backup.missed once for every enabled schedule (database or
// volume) whose backup did not succeed within its window. "Once" means once
// per streak: the next success starts a new one. A schedule is tracked from
// the first check that sees it enabled, so a new or re-enabled schedule gets a
// whole interval before it can be called missed; a stopped database is not
// checked (its skipped runs already raise backup.failed).
func (s *Service) CheckMissed(ctx context.Context) error {
	now := s.now()
	cands, err := s.missedCandidates(ctx)
	if err != nil {
		return err
	}
	grace := map[uuid.UUID]time.Duration{}
	var firstErr error
	keep := map[string][]uuid.UUID{missedKindDatabase: {}, missedKindVolume: {}}
	for _, c := range cands {
		if c.stopped {
			continue
		}
		keep[c.kind] = append(keep[c.kind], c.subjectID)
		sc, err := services.LoadScopeInternal(ctx, s.q, c.serviceID)
		if err != nil {
			continue // deleted
		}
		g, ok := grace[sc.WorkspaceID]
		if !ok {
			cfg, err := svcalerts.LoadSettings(ctx, s.pool, sc.WorkspaceID)
			if err != nil {
				return fmt.Errorf("load alert settings: %w", err)
			}
			g = cfg.BackupGrace
			grace[sc.WorkspaceID] = g
		}
		state, ref, deadline, err := evaluateMissed(c, now, g)
		if err != nil {
			s.log.WarnContext(ctx, "missed-backup check: bad schedule", "subject_id", c.subjectID.String(), "err", err)
			continue
		}
		switch state {
		case missedTrack:
			if _, err := s.pool.Exec(ctx, `INSERT INTO backup_missed_alerts (kind, subject_id, since) VALUES ($1,$2,$3)
				ON CONFLICT DO NOTHING`, c.kind, c.subjectID, now); err != nil && firstErr == nil {
				firstErr = err
			}
		case missedAlert:
			s.notify(ctx, missedEvent(sc, c, ref, deadline, now))
			if _, err := s.pool.Exec(ctx, `UPDATE backup_missed_alerts SET notified_at = $3 WHERE kind = $1 AND subject_id = $2`,
				c.kind, c.subjectID, now); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	// Forget schedules that were disabled, deleted or belong to stopped databases:
	// they start from a fresh baseline when they come back.
	for kind, ids := range keep {
		if _, err := s.pool.Exec(ctx, `DELETE FROM backup_missed_alerts WHERE kind = $1 AND NOT (subject_id = ANY($2))`, kind, ids); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) missedCandidates(ctx context.Context) ([]missedCandidate, error) {
	var out []missedCandidate
	scan := func(sql string, kind string) error {
		rows, err := s.pool.Query(ctx, sql)
		if err != nil {
			return fmt.Errorf("list %s schedules: %w", kind, err)
		}
		defer rows.Close()
		for rows.Next() {
			c := missedCandidate{kind: kind}
			var desired string
			if err := rows.Scan(&c.subjectID, &c.serviceID, &c.cron, &c.tz, &c.destination, &c.lastOK, &c.inflight, &desired, &c.since, &c.notifiedAt); err != nil {
				return err
			}
			c.stopped = desired == services.DesiredStopped
			out = append(out, c)
		}
		return rows.Err()
	}
	if err := scan(`SELECT s.id, s.service_id, s.cron, s.timezone, d.name,
			(SELECT max(b.finished_at) FROM backups b WHERE b.status = 'succeeded'
			   AND (b.schedule_id = s.id OR (b.schedule_id IS NULL AND b.trigger = 'manual' AND b.service_id = s.service_id))),
			EXISTS (SELECT 1 FROM backups b WHERE b.schedule_id = s.id AND b.status IN ('queued','running')),
			sv.desired_state, m.since, m.notified_at
		FROM backup_schedules s
		JOIN services sv ON sv.id = s.service_id AND sv.deleted_at IS NULL
		JOIN backup_destinations d ON d.id = s.destination_id
		LEFT JOIN backup_missed_alerts m ON m.kind = 'database' AND m.subject_id = s.id
		WHERE s.enabled`, missedKindDatabase); err != nil {
		return nil, err
	}
	if err := scan(`SELECT c.service_id, c.service_id, c.cron, c.timezone, d.name,
			(SELECT max(b.finished_at) FROM volume_backups b WHERE b.service_id = c.service_id
			   AND b.status = 'succeeded' AND b.trigger <> 'pre_restore'),
			EXISTS (SELECT 1 FROM volume_backups b WHERE b.service_id = c.service_id AND b.status IN ('queued','running')),
			sv.desired_state, m.since, m.notified_at
		FROM volume_backup_configs c
		JOIN services sv ON sv.id = c.service_id AND sv.deleted_at IS NULL
		JOIN backup_destinations d ON d.id = c.destination_id
		LEFT JOIN backup_missed_alerts m ON m.kind = 'volume' AND m.subject_id = c.service_id
		WHERE c.enabled AND c.cron <> ''`, missedKindVolume); err != nil {
		return nil, err
	}
	return out, nil
}

// missedEvent describes a missed backup for the notifier.
func missedEvent(sc services.Scope, c missedCandidate, ref, deadline, now time.Time) Event {
	last := "No backup has succeeded since the schedule was enabled " + ago(now, ref) + " ago"
	if c.lastOK != nil && !c.lastOK.Before(ref) {
		last = "The last successful backup finished " + ago(now, *c.lastOK) + " ago"
	}
	return Event{
		Type: NotifyBackupMissed, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ProjectName: sc.ProjectName,
		Environment: sc.EnvName, ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, Trigger: TriggerSchedule,
		Destination: c.destination, Volume: c.kind == missedKindVolume,
		Message: fmt.Sprintf("%s. The schedule (%s, %s) should have produced one by %s UTC. Check the backup history and the destination.",
			last, c.cron, c.tz, deadline.UTC().Format("2006-01-02 15:04")),
		At: now,
	}
}

// ago renders a duration coarsely ("3 days", "5 hours", "20 minutes").
func ago(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d minutes", max(int(d.Minutes()), 1))
}
