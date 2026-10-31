package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Handles reports whether a command kind belongs to this package.
func Handles(kind string) bool {
	switch kind {
	case agentprotocol.KindDBBackup, agentprotocol.KindDBRestore, agentprotocol.KindBackupDelete, agentprotocol.KindBackupTest:
		return true
	}
	return isVolumeKind(kind)
}

const maxMessage = 2000

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxMessage {
		s = s[:maxMessage] + "…"
	}
	return s
}

// SecretsForCommand attaches the send-time secrets of a backup command: the
// destination's access and secret key and the database password. They are
// never persisted and never part of the command payload.
func (s *Service) SecretsForCommand(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	if isVolumeKind(kind) {
		return s.volumeSecrets(ctx, kind, payload)
	}
	var dest agentprotocol.BackupDestination
	var serviceID string
	switch kind {
	case agentprotocol.KindDBBackup:
		var p agentprotocol.DBBackupPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode db_backup payload: %w", err)
		}
		dest, serviceID = p.Destination, p.Target.ServiceID
	case agentprotocol.KindDBRestore:
		var p agentprotocol.DBRestorePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode db_restore payload: %w", err)
		}
		dest, serviceID = p.Destination, p.Target.ServiceID
	case agentprotocol.KindBackupDelete:
		var p agentprotocol.BackupDeletePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode backup_delete payload: %w", err)
		}
		dest = p.Destination
	case agentprotocol.KindBackupTest:
		var p agentprotocol.BackupTestPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode backup_test payload: %w", err)
		}
		dest = p.Destination
	default:
		return nil, nil
	}
	destID, err := uuid.Parse(dest.ID)
	if err != nil {
		return nil, fmt.Errorf("destination id: %w", err)
	}
	d, err := s.q.GetBackupDestinationInternal(ctx, destID)
	if err != nil {
		return nil, fmt.Errorf("load destination: %w", err)
	}
	out, err := s.destinationSecrets(ctx, d)
	if err != nil {
		return nil, err
	}
	if serviceID != "" {
		id, err := uuid.Parse(serviceID)
		if err != nil {
			return nil, fmt.Errorf("service id: %w", err)
		}
		sc, err := services.LoadScopeInternal(ctx, s.q, id)
		if err != nil {
			return nil, fmt.Errorf("load database: %w", err)
		}
		tgt, err := s.resolveTarget(ctx, s.q, sc)
		if err != nil {
			return nil, err
		}
		if tgt.Password != "" {
			out[agentprotocol.SecretDBPassword] = tgt.Password
		}
	}
	return out, nil
}

// scrub masks the secrets of a command in an agent-provided message before it
// is stored or shown.
func (s *Service) scrub(ctx context.Context, cmd db.AgentCommand, msg string) string {
	secrets, err := s.SecretsForCommand(ctx, cmd.Kind, cmd.Payload)
	if err == nil {
		for _, v := range secrets {
			if len(v) >= 4 {
				msg = strings.ReplaceAll(msg, v, "••••••")
			}
		}
	}
	return clip(msg)
}

// HandleResult applies the result of a backup command. Commands of other kinds
// are ignored.
func (s *Service) HandleResult(ctx context.Context, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	if !Handles(cmd.Kind) {
		return nil
	}
	ok := res.Outcome == agentprotocol.OutcomeSucceeded
	msg := res.Error
	switch cmd.Kind {
	case agentprotocol.KindDBBackup:
		var data agentprotocol.DBBackupResult
		_ = json.Unmarshal(res.Data, &data)
		if msg == "" {
			msg = data.Message
		}
		return s.finishBackup(ctx, cmd, ok, data, s.scrub(ctx, cmd, msg))
	case agentprotocol.KindDBRestore:
		r, err := s.q.GetBackupRestoreByCommand(ctx, &commandID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if ok {
			s.finishRestore(ctx, r.ID, RestoreSucceeded, "")
		} else {
			if msg == "" {
				msg = "the agent reported a failed restore"
			}
			s.finishRestore(ctx, r.ID, RestoreFailed, s.scrub(ctx, cmd, msg))
		}
	case agentprotocol.KindBackupTest:
		var p agentprotocol.BackupTestPayload
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return nil
		}
		id, err := uuid.Parse(p.Destination.ID)
		if err != nil {
			return nil
		}
		var data agentprotocol.BackupSimpleResult
		_ = json.Unmarshal(res.Data, &data)
		if msg == "" {
			msg = data.Message
		}
		msg = s.scrub(ctx, cmd, msg)
		if !ok {
			msg = s.builtinFull(ctx, id, msg)
		}
		s.recordTest(ctx, id, ok, msg)
	case agentprotocol.KindBackupDelete:
		return s.finishDeletion(ctx, cmd, ok, s.scrub(ctx, cmd, msg))
	case agentprotocol.KindVolumeBackup, agentprotocol.KindVolumeForget, agentprotocol.KindVolumeRestore, agentprotocol.KindVolumeRemove:
		return s.handleVolumeResult(ctx, cmd, res)
	}
	return nil
}

// HandleUndelivered applies the expiry or cancellation of a never-acked command.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	switch cmd.Kind {
	case agentprotocol.KindDBBackup:
		return s.failBackup(ctx, cmd.ID, StatusExpired, msg)
	case agentprotocol.KindDBRestore:
		if r, err := s.q.GetBackupRestoreByCommand(ctx, &commandID); err == nil {
			s.finishRestore(ctx, r.ID, RestoreFailed, msg)
		}
	case agentprotocol.KindBackupTest:
		var p agentprotocol.BackupTestPayload
		if json.Unmarshal(cmd.Payload, &p) == nil {
			if id, err := uuid.Parse(p.Destination.ID); err == nil {
				s.recordTest(ctx, id, false, msg)
			}
		}
	case agentprotocol.KindBackupDelete:
		return s.finishDeletion(ctx, cmd, false, msg)
	case agentprotocol.KindVolumeBackup, agentprotocol.KindVolumeForget, agentprotocol.KindVolumeRestore, agentprotocol.KindVolumeRemove:
		return s.handleVolumeUndelivered(ctx, cmd, msg)
	}
	return nil
}

// finishBackup records the outcome of a db_backup command.
func (s *Service) finishBackup(ctx context.Context, cmd db.AgentCommand, ok bool, data agentprotocol.DBBackupResult, msg string) error {
	if !ok {
		if msg == "" {
			msg = "the agent reported a failed backup"
		}
		if id, ok := commandDestinationID(cmd.Kind, cmd.Payload); ok {
			msg = s.builtinFull(ctx, id, msg)
		}
		return s.failBackup(ctx, cmd.ID, StatusFailed, msg)
	}
	b, err := s.q.GetBackupByCommand(ctx, &cmd.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var started *time.Time
	if !data.StartedAt.IsZero() {
		started = &data.StartedAt
	}
	key := data.ObjectKey
	if key == "" {
		key = b.ObjectKey
	}
	done, err := s.q.CompleteBackup(ctx, db.CompleteBackupParams{
		ID: b.ID, SizeBytes: data.SizeBytes, Sha256: data.SHA256, Format: data.Format, ObjectKey: key, StartedAt: started,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already finished (duplicate result)
	}
	if err != nil {
		return fmt.Errorf("complete backup: %w", err)
	}
	s.afterBackupFinished(ctx, done, NotifyBackupSucceeded)
	return nil
}

func (s *Service) failBackup(ctx context.Context, commandID uuid.UUID, status, msg string) error {
	b, err := s.q.GetBackupByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	failed, err := s.q.FailBackup(ctx, db.FailBackupParams{ID: b.ID, Status: status, Error: msg})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail backup: %w", err)
	}
	s.afterBackupFinished(ctx, failed, NotifyBackupFailed)
	return nil
}

// afterBackupFinished publishes, notifies, prunes and advances a restore that
// waited for this backup.
func (s *Service) afterBackupFinished(ctx context.Context, b db.Backup, typ string) {
	sc, err := services.LoadScopeInternal(ctx, s.q, b.ServiceID)
	if err != nil {
		return
	}
	s.publish(sc, EventBackupUpdated, b.ID)
	destName := ""
	if d, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID); err == nil {
		destName = d.Name
	}
	s.notify(ctx, Event{
		Type: typ, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ProjectName: sc.ProjectName, Environment: sc.EnvSlug,
		ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, BackupID: b.ID, Trigger: b.Trigger, Destination: destName,
		Engine: b.Engine, SizeBytes: b.SizeBytes, Message: b.Error, At: s.now(),
	})
	if b.Status == StatusSucceeded && b.ScheduleID != nil {
		s.applyRetention(ctx, *b.ScheduleID)
	}
	if b.Trigger != TriggerPreRestore {
		return
	}
	if dr, err := s.q.GetDumpRestoreByPreBackup(ctx, &b.ID); err == nil {
		s.afterDumpPreBackup(ctx, dr, b)
		return
	}
	r, err := s.q.GetBackupRestoreByPreBackup(ctx, &b.ID)
	if err != nil {
		return
	}
	if b.Status != StatusSucceeded {
		s.finishRestore(ctx, r.ID, RestoreFailed, "The safety backup taken before the restore failed, so nothing was changed: "+b.Error)
		return
	}
	if err := s.sendRestore(ctx, r); err != nil {
		s.finishRestore(ctx, r.ID, RestoreFailed, "could not start the restore: "+err.Error())
	}
}

// finishDeletion applies the result of a backup_delete command: on success the
// rows go away; on failure the objects stay restorable and retention retries.
func (s *Service) finishDeletion(ctx context.Context, cmd db.AgentCommand, ok bool, msg string) error {
	var p agentprotocol.BackupDeletePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil
	}
	var ids []uuid.UUID
	for _, raw := range p.BackupIDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, _ := s.q.ListBackupsByIDs(ctx, ids)
	var err error
	if ok {
		err = s.q.DeleteBackupsByIDs(ctx, ids)
	} else {
		if msg == "" {
			msg = "the stored object could not be deleted"
		}
		err = s.q.ClearBackupObjectDeleted(ctx, db.ClearBackupObjectDeletedParams{Ids: ids, Error: "Deleting the stored object failed: " + msg})
	}
	if err != nil {
		return fmt.Errorf("apply deletion result: %w", err)
	}
	for _, b := range rows {
		if sc, err := services.LoadScopeInternal(ctx, s.q, b.ServiceID); err == nil {
			s.publish(sc, EventBackupUpdated, b.ID)
		}
	}
	return nil
}
