package backups

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// actorLabel names who triggered an action: the caller's label, or "system".
func actorLabel(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Label != "" {
		return p.Label
	}
	return "system"
}

// objectKey builds <prefix>/<project>/<environment>/<service>/<timestamp>-<rand><ext>.
// The key is chosen by the control plane so the agent never invents paths.
func objectKey(d db.BackupDestination, sc services.Scope, engine string, at time.Time) string {
	var r [3]byte
	_, _ = rand.Read(r[:])
	parts := []string{}
	if p := objectPrefix(d); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, sc.ProjectSlug, sc.EnvSlug, sc.Service.Slug,
		at.UTC().Format("20060102T150405Z")+"-"+hex.EncodeToString(r[:])+objectExt(engine))
	return strings.Join(parts, "/")
}

// databaseRunning reports whether a scheduled backup should be attempted: it
// is skipped for stopped databases and when the agent last observed the
// database not running.
func databaseRunning(sv db.Service, now time.Time) (bool, string) {
	if sv.DesiredState == "stopped" {
		return false, "the database is stopped"
	}
	if sv.CurrentDeploymentID == nil {
		return false, "the database has not been deployed yet"
	}
	if len(sv.Observed) > 0 && sv.ObservedAt != nil && now.Sub(*sv.ObservedAt) < 3*time.Minute {
		var o agentprotocol.ObservedService
		if json.Unmarshal(sv.Observed, &o) == nil && o.State != "" && o.State != agentprotocol.StateRunning {
			return false, "the database is " + o.State
		}
	}
	return true, ""
}

type startInput struct {
	sc       services.Scope
	dest     db.BackupDestination
	schedule *db.BackupSchedule
	trigger  string
	tgt      target
	actor    string
}

// startBackup records a backup run and enqueues its db_backup command inside
// tx. The payload names the destination and key; credentials are attached at
// send time (SecretsForCommand).
func (s *Service) startBackup(ctx context.Context, tx pgx.Tx, in startInput) (db.Backup, error) {
	q := db.New(tx)
	if in.sc.Service.ServerID == nil {
		return db.Backup{}, apierr.Unprocessable("no_server", "the database is not placed on a server")
	}
	serverID := *in.sc.Service.ServerID
	key := objectKey(in.dest, in.sc, in.tgt.Engine, s.now())
	var schedID *uuid.UUID
	if in.schedule != nil {
		schedID = &in.schedule.ID
	}
	b, err := q.InsertBackup(ctx, db.InsertBackupParams{
		WorkspaceID: in.sc.WorkspaceID, ServiceID: in.sc.Service.ID, ScheduleID: schedID, DestinationID: in.dest.ID,
		ServerID: &serverID, Trigger: in.trigger, Status: StatusQueued, Engine: in.tgt.Engine, EngineVersion: in.tgt.Version,
		DatabaseName: in.tgt.Database, ObjectKey: key, ActorLabel: in.actor,
	})
	if err != nil {
		return b, fmt.Errorf("insert backup: %w", err)
	}
	dest, err := s.destinationFor(ctx, in.dest)
	if err != nil {
		return b, err
	}
	payload, err := json.Marshal(agentprotocol.DBBackupPayload{
		BackupID: b.ID.String(), Target: in.tgt.proto(in.sc.Service.ID.String()), Destination: dest, ObjectKey: key,
		Meta: agentprotocol.ManifestMeta{
			Project: in.sc.ProjectSlug, Environment: in.sc.EnvSlug, Service: in.sc.Service.Slug,
			ServiceID: in.sc.Service.ID.String(), Trigger: in.trigger,
		},
	})
	if err != nil {
		return b, err
	}
	cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
		ServerID: serverID, Kind: agentprotocol.KindDBBackup, Payload: payload, IdempotencyKey: "db_backup:" + b.ID.String(),
	})
	if err != nil {
		return b, fmt.Errorf("enqueue db_backup: %w", err)
	}
	if err := q.SetBackupCommand(ctx, db.SetBackupCommandParams{ID: b.ID, CommandID: &cmdID}); err != nil {
		return b, err
	}
	b.CommandID = &cmdID
	return b, nil
}

// insertSkipped records a backup that was not attempted.
func (s *Service) insertSkipped(ctx context.Context, q *db.Queries, in startInput, reason string) (db.Backup, error) {
	now := s.now()
	return q.InsertBackup(ctx, db.InsertBackupParams{
		WorkspaceID: in.sc.WorkspaceID, ServiceID: in.sc.Service.ID, ScheduleID: scheduleID(in.schedule), DestinationID: in.dest.ID,
		ServerID: in.sc.Service.ServerID, Trigger: in.trigger, Status: StatusSkipped, Engine: in.tgt.Engine, EngineVersion: in.tgt.Version,
		DatabaseName: in.tgt.Database, Error: reason, ActorLabel: in.actor, FinishedAt: &now,
	})
}

func scheduleID(s *db.BackupSchedule) *uuid.UUID {
	if s == nil {
		return nil
	}
	return &s.ID
}

// CreateBackup starts an on-demand backup to a destination.
func (s *Service) CreateBackup(ctx context.Context, serviceID, destinationID uuid.UUID) (BackupView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return BackupView{}, err
	}
	dest, err := s.destination(ctx, s.q, sc.WorkspaceID, destinationID)
	if err != nil {
		return BackupView{}, err
	}
	tgt, err := s.resolveTarget(ctx, s.q, sc)
	if err != nil {
		return BackupView{}, err
	}
	if sc.Service.ServerID == nil {
		return BackupView{}, apierr.Unprocessable("no_server", "the database is not placed on a server")
	}
	name, online, err := s.serverState(ctx, s.q, sc.Service.ServerID)
	if err != nil {
		return BackupView{}, err
	}
	if !online {
		return BackupView{}, apierr.Conflict("server_offline", "the server "+name+" is offline; the backup was not started")
	}
	if ok, why := databaseRunning(sc.Service, s.now()); !ok {
		return BackupView{}, apierr.Conflict("database_not_running", "the backup was not started: "+why)
	}
	var b db.Backup
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		b, err = s.startBackup(ctx, tx, startInput{sc: sc, dest: dest, trigger: TriggerManual, tgt: tgt, actor: actorLabel(ctx)})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionBackupCreate, ResourceType: "backup", ResourceID: b.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "destination": dest.Name, "trigger": TriggerManual},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return BackupView{}, err
		}
		return BackupView{}, fmt.Errorf("create backup: %w", err)
	}
	s.publish(sc, b.ID)
	return backupView(b, StatusQueued, dest.Name, dest.Kind), nil
}

// ListBackups returns a database's backups, newest first.
func (s *Service) ListBackups(ctx context.Context, serviceID uuid.UUID, limit int) ([]BackupView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.ListServiceBackups(ctx, db.ListServiceBackupsParams{ServiceID: sc.Service.ID, Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	out := make([]BackupView, len(rows))
	for i, r := range rows {
		out[i] = backupView(r.Backup, r.EffectiveStatus, r.DestinationName, r.DestinationKind)
	}
	return out, nil
}

// GetBackup returns one backup.
func (s *Service) GetBackup(ctx context.Context, id uuid.UUID) (BackupView, error) {
	b, _, err := s.backupScope(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return BackupView{}, err
	}
	r, err := s.q.GetBackupView(ctx, db.GetBackupViewParams{ID: b.ID, WorkspaceID: b.WorkspaceID})
	if err != nil {
		return BackupView{}, fmt.Errorf("get backup: %w", err)
	}
	return backupView(r.Backup, r.EffectiveStatus, r.DestinationName, r.DestinationKind), nil
}

// SetLocked protects a backup from retention (or lifts the protection).
func (s *Service) SetLocked(ctx context.Context, id uuid.UUID, locked bool) (BackupView, error) {
	b, sc, err := s.backupScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return BackupView{}, err
	}
	if !locked {
		if err := sc.Guard(ctx, guardUnlockBackup, fmt.Sprintf("Unlock a database backup of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
			[]string{"The backup can then be deleted by retention or by hand."}, id, locked); err != nil {
			return BackupView{}, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).SetBackupLocked(ctx, db.SetBackupLockedParams{ID: id, WorkspaceID: b.WorkspaceID, Locked: locked}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionBackupLock, ResourceType: "backup", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "locked": locked},
		})
	})
	if err != nil {
		return BackupView{}, fmt.Errorf("lock backup: %w", err)
	}
	s.publish(sc, id)
	return s.GetBackup(ctx, id)
}

// DeleteBackup removes a backup. A stored dump is deleted by the agent first;
// the row disappears once the agent confirmed (or immediately when there is no
// object).
func (s *Service) DeleteBackup(ctx context.Context, id uuid.UUID) error {
	b, sc, err := s.backupScope(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return err
	}
	if b.Locked {
		return apierr.Conflict("backup_locked", "the backup is locked; unlock it before deleting")
	}
	if b.Status == StatusQueued {
		return apierr.Conflict("backup_in_progress", "the backup is still running")
	}
	if err := sc.Guard(ctx, guardDeleteBackup, fmt.Sprintf("Delete a database backup of %s in production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), true,
		[]string{"The dump is removed from its destination."}, id); err != nil {
		return err
	}
	hasObject := b.Status == StatusSucceeded && b.ObjectDeletedAt == nil
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if hasObject {
			if err := s.enqueueDeletion(ctx, tx, []db.Backup{b}); err != nil {
				return err
			}
		} else if err := q.DeleteBackupsByIDs(ctx, []uuid.UUID{b.ID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionBackupDelete, ResourceType: "backup", ResourceID: id.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "objectKey": b.ObjectKey},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete backup: %w", err)
	}
	s.publish(sc, id)
	return nil
}

// enqueueDeletion marks backups' objects as being deleted and enqueues one
// backup_delete command per server and destination. The server of each backup
// must be online.
func (s *Service) enqueueDeletion(ctx context.Context, tx pgx.Tx, bs []db.Backup) error {
	q := db.New(tx)
	type group struct {
		server uuid.UUID
		dest   uuid.UUID
	}
	groups := map[group][]db.Backup{}
	var order []group
	for _, b := range bs {
		if b.ServerID == nil {
			return apierr.Conflict("server_unavailable", "the server that holds this backup no longer exists")
		}
		g := group{*b.ServerID, b.DestinationID}
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], b)
	}
	for _, g := range order {
		name, online, err := s.serverState(ctx, q, &g.server)
		if err != nil {
			return err
		}
		if !online {
			return apierr.Conflict("server_offline", "the server "+name+" is offline; the stored backup can not be deleted now")
		}
		dest, err := q.GetBackupDestinationInternal(ctx, g.dest)
		if err != nil {
			return fmt.Errorf("load destination: %w", err)
		}
		agentDest, err := s.destinationFor(ctx, dest)
		if err != nil {
			return err
		}
		p := agentprotocol.BackupDeletePayload{Destination: agentDest}
		ids := make([]uuid.UUID, 0, len(groups[g]))
		for _, b := range groups[g] {
			p.ObjectKeys = append(p.ObjectKeys, b.ObjectKey)
			p.BackupIDs = append(p.BackupIDs, b.ID.String())
			ids = append(ids, b.ID)
		}
		raw, _ := json.Marshal(p)
		if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: g.server, Kind: agentprotocol.KindBackupDelete, Payload: raw}); err != nil {
			return fmt.Errorf("enqueue backup_delete: %w", err)
		}
		if err := q.MarkBackupObjectDeleted(ctx, ids); err != nil {
			return err
		}
	}
	return nil
}

// applyRetention prunes the stored backups of a schedule after a successful run.
// Failures to enqueue are logged: retention retries after the next success.
func (s *Service) applyRetention(ctx context.Context, scheduleID uuid.UUID) {
	sched, err := s.q.GetBackupSchedule(ctx, scheduleID)
	if err != nil {
		return
	}
	rows, err := s.q.ListScheduleRetentionCandidates(ctx, &scheduleID)
	if err != nil {
		s.log.ErrorContext(ctx, "list retention candidates", "schedule_id", scheduleID, "err", err)
		return
	}
	cands := make([]Candidate, len(rows))
	for i, r := range rows {
		cands[i] = Candidate{ID: r.ID, CreatedAt: r.CreatedAt, Locked: r.Locked}
	}
	victims := Prune(cands, int(sched.RetentionCount), int(sched.RetentionDays), s.now())
	if len(victims) == 0 {
		return
	}
	var del []db.Backup
	for _, r := range rows {
		if slices.Contains(victims, r.ID) {
			del = append(del, r)
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.enqueueDeletion(ctx, tx, del) })
	if err != nil {
		s.log.WarnContext(ctx, "retention deletion not started", "schedule_id", scheduleID, "backups", len(del), "err", err)
	}
}
