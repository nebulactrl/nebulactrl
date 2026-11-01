package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// isVolumeKind reports whether a command kind is one of the volume backup kinds.
func isVolumeKind(kind string) bool {
	switch kind {
	case agentprotocol.KindVolumeBackup, agentprotocol.KindVolumeForget, agentprotocol.KindVolumeRestore, agentprotocol.KindVolumeRemove:
		return true
	}
	return false
}

// volumeSecrets attaches the send-time secrets of a volume command: the
// destination's keys and the repository password. volume_remove has none.
func (s *Service) volumeSecrets(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	var serviceID string
	var repo agentprotocol.VolumeRepo
	switch kind {
	case agentprotocol.KindVolumeBackup:
		var p agentprotocol.VolumeBackupPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode volume_backup payload: %w", err)
		}
		serviceID, repo = p.ServiceID, p.Repo
	case agentprotocol.KindVolumeForget:
		var p agentprotocol.VolumeForgetPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode volume_forget payload: %w", err)
		}
		serviceID, repo = p.ServiceID, p.Repo
	case agentprotocol.KindVolumeRestore:
		var p agentprotocol.VolumeRestorePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("decode volume_restore payload: %w", err)
		}
		serviceID, repo = p.ServiceID, p.Repo
	default:
		return nil, nil
	}
	sid, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("service id: %w", err)
	}
	did, err := uuid.Parse(repo.Destination.ID)
	if err != nil {
		return nil, fmt.Errorf("destination id: %w", err)
	}
	return s.repoSecrets(ctx, s.q, sid, did)
}

// handleVolumeResult applies the result of a volume command.
func (s *Service) handleVolumeResult(ctx context.Context, cmd db.AgentCommand, res agentprotocol.ResultFrame) error {
	ok := res.Outcome == agentprotocol.OutcomeSucceeded
	msg := res.Error
	switch cmd.Kind {
	case agentprotocol.KindVolumeBackup:
		var data agentprotocol.VolumeBackupResult
		_ = json.Unmarshal(res.Data, &data)
		if msg == "" {
			msg = data.Message
		}
		return s.finishVolumeBackup(ctx, cmd, ok, data, s.scrub(ctx, cmd, msg))
	case agentprotocol.KindVolumeForget:
		var data agentprotocol.VolumeForgetResult
		_ = json.Unmarshal(res.Data, &data)
		if msg == "" {
			msg = data.Message
		}
		return s.finishVolumeForget(ctx, cmd, ok && data.Outcome != agentprotocol.OutcomeFailed, data, s.scrub(ctx, cmd, msg))
	case agentprotocol.KindVolumeRestore:
		var data agentprotocol.VolumeRestoreResult
		_ = json.Unmarshal(res.Data, &data)
		if msg == "" {
			msg = data.Message
		}
		return s.finishVolumeRestoreCommand(ctx, cmd, ok && data.Outcome != agentprotocol.OutcomeFailed, data, s.scrub(ctx, cmd, msg))
	case agentprotocol.KindVolumeRemove:
		var data agentprotocol.VolumeRemoveResult
		_ = json.Unmarshal(res.Data, &data)
		return s.finishVolumeRemove(ctx, cmd, ok && data.Outcome != agentprotocol.OutcomeFailed, clip(firstNonEmpty(msg, data.Message)))
	}
	return nil
}

// handleVolumeUndelivered applies the expiry or cancellation of a never-acked
// volume command: nothing was changed.
func (s *Service) handleVolumeUndelivered(ctx context.Context, cmd db.AgentCommand, msg string) error {
	switch cmd.Kind {
	case agentprotocol.KindVolumeBackup:
		return s.failVolumeBackup(ctx, cmd.ID, StatusExpired, msg)
	case agentprotocol.KindVolumeForget:
		return s.finishVolumeForget(ctx, cmd, false, agentprotocol.VolumeForgetResult{}, msg)
	case agentprotocol.KindVolumeRestore:
		if r, err := s.q.GetVolumeRestoreByCommand(ctx, &cmd.ID); err == nil {
			s.finishVolumeRestore(ctx, r.ID, RestoreFailed, msg)
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// finishVolumeBackup records the outcome of a volume_backup command.
func (s *Service) finishVolumeBackup(ctx context.Context, cmd db.AgentCommand, ok bool, data agentprotocol.VolumeBackupResult, msg string) error {
	if data.Outcome == agentprotocol.OutcomeSkipped {
		if msg == "" {
			msg = "The backup was skipped: the service stayed busy."
		}
		return s.failVolumeBackup(ctx, cmd.ID, StatusSkipped, msg)
	}
	if !ok || data.Outcome == agentprotocol.OutcomeFailed {
		if msg == "" {
			msg = "the agent reported a failed backup"
		}
		if id, ok := commandDestinationID(cmd.Kind, cmd.Payload); ok {
			msg = s.builtinFull(ctx, id, msg)
		}
		return s.failVolumeBackup(ctx, cmd.ID, StatusFailed, msg)
	}
	if data.SnapshotID == "" {
		return s.failVolumeBackup(ctx, cmd.ID, StatusFailed, "the agent did not report a snapshot id")
	}
	b, err := s.q.GetVolumeBackupByCommand(ctx, &cmd.ID)
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
	consistency := data.Consistency
	if consistency == "" {
		consistency = b.Mode
	}
	done, err := s.q.CompleteVolumeBackup(ctx, db.CompleteVolumeBackupParams{
		ID: b.ID, SnapshotID: data.SnapshotID, Consistency: consistency, SizeBytes: data.BytesProcessed, BytesAdded: data.BytesAdded,
		Files: data.FilesNew + data.FilesChanged + data.FilesUnchanged, FreezeMs: int32(data.FreezeMs), StageMs: int32(data.StageMs),
		StageMethod: data.StageMethod, HookOutcome: data.HookOutcome, Warnings: nonNil(data.Warnings), StartedAt: started,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already finished (duplicate result)
	}
	if err != nil {
		return fmt.Errorf("complete volume backup: %w", err)
	}
	s.afterVolumeBackupFinished(ctx, done, NotifyBackupSucceeded)
	return nil
}

func (s *Service) failVolumeBackup(ctx context.Context, commandID uuid.UUID, status, msg string) error {
	b, err := s.q.GetVolumeBackupByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	failed, err := s.q.FailVolumeBackup(ctx, db.FailVolumeBackupParams{ID: b.ID, Status: status, Error: msg})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail volume backup: %w", err)
	}
	typ := NotifyBackupFailed
	if status == StatusSkipped {
		typ = NotifyBackupSkipped
	}
	s.afterVolumeBackupFinished(ctx, failed, typ)
	return nil
}

// afterVolumeBackupFinished publishes, notifies, applies retention and advances
// an in-place restore that waited for its safety backup.
func (s *Service) afterVolumeBackupFinished(ctx context.Context, b db.VolumeBackup, typ string) {
	sc, err := services.LoadScopeInternal(ctx, s.q, b.ServiceID)
	if err != nil {
		return
	}
	s.publish(sc, b.ID)
	destName := ""
	if d, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID); err == nil {
		destName = d.Name
	}
	s.notify(ctx, Event{
		Type: typ, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ProjectName: sc.ProjectName, Environment: sc.EnvSlug,
		ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, BackupID: b.ID, Trigger: b.Trigger, Destination: destName,
		Engine: volumeEngineLabel, SizeBytes: b.SizeBytes, Message: b.Error, At: s.now(),
	})
	if b.Status == StatusSucceeded {
		s.applyVolumeRetention(ctx, b)
	}
	if b.Trigger != TriggerPreRestore {
		return
	}
	r, err := s.q.GetVolumeRestoreByPreBackup(ctx, &b.ID)
	if err != nil {
		return
	}
	if b.Status != StatusSucceeded {
		s.finishVolumeRestore(ctx, r.ID, RestoreFailed, "The safety backup taken before the restore did not succeed, so nothing was changed: "+b.Error)
		return
	}
	if err := s.sendVolumeRestore(ctx, r); err != nil {
		s.finishVolumeRestore(ctx, r.ID, RestoreFailed, "could not start the restore: "+err.Error())
	}
}

// finishVolumeForget applies the result of a volume_forget command. Removed
// snapshots mark their rows' objects as deleted. A user deletion (BackupIDs)
// removes the rows on success and brings the backup back on failure.
func (s *Service) finishVolumeForget(ctx context.Context, cmd db.AgentCommand, ok bool, data agentprotocol.VolumeForgetResult, msg string) error {
	var p agentprotocol.VolumeForgetPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		s.log.WarnContext(ctx, "volume forget result ignored: the stored payload cannot be parsed", "command_id", cmd.ID, "err", err)
		return nil
	}
	serviceID, err := uuid.Parse(p.ServiceID)
	if err != nil {
		s.log.WarnContext(ctx, "volume forget result ignored: the stored payload has no valid service id", "command_id", cmd.ID, "err", err)
		return nil
	}
	destID, err := uuid.Parse(p.Repo.Destination.ID)
	if err != nil {
		s.log.WarnContext(ctx, "volume forget result ignored: the stored payload has no valid destination id", "command_id", cmd.ID, "err", err)
		return nil
	}
	var ids []uuid.UUID
	for _, raw := range p.BackupIDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	var touched []db.VolumeBackup
	if ok && len(data.Removed) > 0 {
		rows, err := s.q.MarkVolumeSnapshotsForgotten(ctx, db.MarkVolumeSnapshotsForgottenParams{
			ServiceID: serviceID, DestinationID: destID, SnapshotIds: data.Removed,
		})
		if err != nil {
			return fmt.Errorf("mark forgotten snapshots: %w", err)
		}
		touched = append(touched, rows...)
	}
	if len(ids) > 0 {
		rows, _ := s.q.ListVolumeBackupsByIDs(ctx, ids)
		if ok {
			err = s.q.DeleteVolumeBackupsByIDs(ctx, ids)
		} else {
			if msg == "" {
				msg = "the snapshot could not be deleted"
			}
			err = s.q.ClearVolumeBackupObjectDeleted(ctx, db.ClearVolumeBackupObjectDeletedParams{Ids: ids, Error: "Deleting the snapshot failed: " + msg})
		}
		if err != nil {
			return fmt.Errorf("apply volume deletion result: %w", err)
		}
		touched = append(touched, rows...)
	} else if !ok {
		s.log.WarnContext(ctx, "volume retention did not run", "service_id", serviceID, "err", msg)
	}
	if len(touched) == 0 {
		return nil
	}
	if sc, err := services.LoadScopeInternal(ctx, s.q, serviceID); err == nil {
		for _, b := range touched {
			s.publish(sc, b.ID)
		}
	}
	return nil
}

// finishVolumeRestoreCommand applies the result of a volume_restore command.
func (s *Service) finishVolumeRestoreCommand(ctx context.Context, cmd db.AgentCommand, ok bool, data agentprotocol.VolumeRestoreResult, msg string) error {
	r, err := s.q.GetVolumeRestoreByCommand(ctx, &cmd.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ok {
		if msg == "" {
			msg = "the agent reported a failed restore"
		}
		s.finishVolumeRestore(ctx, r.ID, RestoreFailed, msg)
		return nil
	}
	if r.Mode != VolumeRestoreNewVolume {
		s.finishVolumeRestore(ctx, r.ID, RestoreSucceeded, "")
		return nil
	}
	err = s.stageRestoreSwap(ctx, r, data.DockerName)
	switch {
	case errors.Is(err, errNoVolumeForSwap):
		// The volume was removed meanwhile: keep the restored data reachable as a retired volume.
		docker := firstNonEmpty(data.DockerName, r.DockerName)
		_ = s.q.InsertRetiredVolume(ctx, db.InsertRetiredVolumeParams{
			ServiceID: r.ServiceID, VolumeName: r.VolumeName, DockerName: docker, Reason: "Restored, but the volume was removed from the service meanwhile",
		})
		s.finishVolumeRestore(ctx, r.ID, RestoreFailed, "The volume "+r.VolumeName+" was removed from the service while the restore ran. The restored data is kept in the Docker volume "+docker+".")
	case err != nil:
		s.finishVolumeRestore(ctx, r.ID, RestoreFailed, "the restored volume could not be attached to the service: "+err.Error())
	}
	return nil
}

// finishVolumeRemove applies the result of a volume_remove command: the
// retired rows go away once the agent confirmed the removal.
func (s *Service) finishVolumeRemove(ctx context.Context, cmd db.AgentCommand, ok bool, msg string) error {
	var p agentprotocol.VolumeRemovePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		s.log.WarnContext(ctx, "volume remove result ignored: the stored payload cannot be parsed", "command_id", cmd.ID, "err", err)
		return nil
	}
	if !ok {
		s.log.WarnContext(ctx, "volume removal failed", "service_id", p.ServiceID, "volumes", p.Names, "err", msg)
		return nil
	}
	var ids []uuid.UUID
	for _, raw := range p.RetiredIDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := s.q.DeleteRetiredVolumesByIDs(ctx, ids); err != nil {
		return fmt.Errorf("delete retired volumes: %w", err)
	}
	if sid, err := uuid.Parse(p.ServiceID); err == nil {
		if sc, err := services.LoadScopeInternal(ctx, s.q, sid); err == nil {
			for _, id := range ids {
				s.publish(sc, id)
			}
		}
	}
	return nil
}
