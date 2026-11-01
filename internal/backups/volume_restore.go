package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// restoreLabel is the staged-change text of a swapped volume row.
func restoreLabel(b db.VolumeBackup) string {
	at := b.CreatedAt
	if b.StartedAt != nil {
		at = *b.StartedAt
	}
	return "restored from " + at.UTC().Format("Jan 2, 15:04")
}

// RestoreVolumeBackup restores one volume of a backup, either into a new Docker
// volume (the swap is staged on the service, applied by the next deploy) or in
// place after a safety backup.
func (s *Service) RestoreVolumeBackup(ctx context.Context, backupID uuid.UUID, in VolumeRestoreInput) (VolumeRestoreView, error) {
	b, sc, err := s.volumeBackupScope(ctx, policy.ScopeDeploymentsWrite, backupID)
	if err != nil {
		return VolumeRestoreView{}, err
	}
	if b.Status != StatusSucceeded || b.ObjectDeletedAt != nil || b.SnapshotID == "" {
		return VolumeRestoreView{}, apierr.Conflict("backup_not_restorable", "only a successful backup whose snapshot still exists can be restored")
	}
	mode := in.Mode
	if mode == "" {
		mode = VolumeRestoreNewVolume
	}
	if mode != VolumeRestoreNewVolume && mode != VolumeRestoreInPlace {
		return VolumeRestoreView{}, apierr.Unprocessable("validation_failed", "mode must be new_volume or in_place")
	}
	vols, err := s.q.ListServiceVolumes(ctx, sc.Service.ID)
	if err != nil {
		return VolumeRestoreView{}, fmt.Errorf("list volumes: %w", err)
	}
	if err := requireVolumeEligible(sc, vols); err != nil {
		return VolumeRestoreView{}, err
	}
	var vol *db.Volume
	for i := range vols {
		if vols[i].Name == in.Volume {
			vol = &vols[i]
		}
	}
	if vol == nil {
		return VolumeRestoreView{}, apierr.Unprocessable("validation_failed", "the service has no volume named "+in.Volume)
	}
	if !contains(b.Volumes, in.Volume) {
		return VolumeRestoreView{}, apierr.Unprocessable("validation_failed", "the backup does not contain the volume "+in.Volume)
	}
	if active, err := s.q.HasActiveVolumeRestore(ctx, sc.Service.ID); err != nil {
		return VolumeRestoreView{}, fmt.Errorf("check restores: %w", err)
	} else if active {
		return VolumeRestoreView{}, apierr.Conflict("backup_in_progress", "another restore of this service is still running")
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return VolumeRestoreView{}, fmt.Errorf("load destination: %w", err)
	}
	if dest.Kind == agentprotocol.DestLocal && (b.ServerID == nil || *b.ServerID != *sc.Service.ServerID) {
		return VolumeRestoreView{}, apierr.Unprocessable("local_backup_server", "this backup is stored in a directory on another server; it can only be restored on that server")
	}
	name, online, err := s.serverState(ctx, s.q, sc.Service.ServerID)
	if err != nil {
		return VolumeRestoreView{}, err
	}
	if !online {
		return VolumeRestoreView{}, apierr.Conflict("server_offline", "the server "+name+" is offline; the restore was not started")
	}
	repo, err := s.q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: sc.Service.ID, DestinationID: b.DestinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeRestoreView{}, apierr.Conflict("backup_not_restorable", "the backup's repository is not known")
	}
	if err != nil {
		return VolumeRestoreView{}, fmt.Errorf("load repository: %w", err)
	}
	if mode == VolumeRestoreInPlace {
		if err := sc.Guard(ctx, "restore_volume_in_place",
			fmt.Sprintf("Restore the volume %s of %s in production from a backup, overwriting its data (%s / %s)", vol.Name, sc.Service.Name, sc.ProjectName, sc.EnvName), true,
			[]string{
				"Overwrites the contents of the volume " + vol.Name + " (mounted at " + vol.MountPath + ") with the backup from " + b.CreatedAt.UTC().Format("2006-01-02 15:04 UTC") + ".",
				"A safety backup of the current data is taken first.",
			}, b.ID, vol.Name); err != nil {
			return VolumeRestoreView{}, err
		}
		return s.restoreVolumeInPlace(ctx, b, sc, vols, *vol, dest, in)
	}

	// New volume: restore into a fresh Docker volume; the swap is staged when
	// the agent confirmed (HandleResult).
	newName := agentprotocol.RestoredVolumeName(sc.Service.ID.String(), vol.Name, s.now())
	agentRepo, err := s.volumeRepo(ctx, dest, repo.RepoPath)
	if err != nil {
		return VolumeRestoreView{}, err
	}
	var r db.VolumeRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		r, err = q.InsertVolumeRestore(ctx, db.InsertVolumeRestoreParams{
			WorkspaceID: sc.WorkspaceID, BackupID: b.ID, ServiceID: sc.Service.ID, VolumeName: vol.Name, Mode: VolumeRestoreNewVolume,
			Status: RestoreRestoring, DockerName: newName, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(agentprotocol.VolumeRestorePayload{
			RestoreID: r.ID.String(), ServiceID: sc.Service.ID.String(), Repo: agentRepo, SnapshotID: b.SnapshotID,
			Volume: agentprotocol.VolumeRef{Name: vol.Name, DockerName: newName, MountPath: vol.MountPath},
		})
		cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: *sc.Service.ServerID, Kind: agentprotocol.KindVolumeRestore, Payload: payload, IdempotencyKey: "volume_restore:" + r.ID.String(),
		})
		if err != nil {
			return fmt.Errorf("enqueue volume_restore: %w", err)
		}
		if r, err = q.SetVolumeRestoreProgress(ctx, db.SetVolumeRestoreProgressParams{ID: r.ID, Status: RestoreRestoring, CommandID: &cmdID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeRestore, ResourceType: "volume_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "backup": b.ID.String(), "volume": vol.Name, "mode": VolumeRestoreNewVolume, "dockerName": newName},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return VolumeRestoreView{}, err
		}
		return VolumeRestoreView{}, fmt.Errorf("start volume restore: %w", err)
	}
	s.publish(sc, r.ID)
	return volumeRestoreView(r), nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// restoreVolumeInPlace takes the safety backup first; the restore command is
// sent once it succeeded (afterVolumeBackupFinished).
func (s *Service) restoreVolumeInPlace(ctx context.Context, b db.VolumeBackup, sc services.Scope, vols []db.Volume, vol db.Volume, dest db.BackupDestination, in VolumeRestoreInput) (VolumeRestoreView, error) {
	if in.ConfirmSlug != sc.Service.Slug {
		return VolumeRestoreView{}, apierr.Unprocessable("confirmation_required", "type the service's slug ("+sc.Service.Slug+") to confirm an in-place restore")
	}
	if sc.Service.CurrentDeploymentID == nil {
		return VolumeRestoreView{}, apierr.Conflict("service_not_deployed", "the service has not been deployed yet, so there is nothing to restore into; use a new volume instead")
	}
	cfg, err := s.safetyBackupConfig(ctx, sc, dest)
	if err != nil {
		return VolumeRestoreView{}, err
	}
	var r db.VolumeRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pre, err := s.startVolumeBackup(ctx, tx, volumeStart{sc: sc, vols: vols, cfg: cfg, dest: dest, trigger: TriggerPreRestore, actor: actorLabel(ctx)})
		if err != nil {
			return err
		}
		r, err = db.New(tx).InsertVolumeRestore(ctx, db.InsertVolumeRestoreParams{
			WorkspaceID: sc.WorkspaceID, BackupID: b.ID, ServiceID: sc.Service.ID, VolumeName: vol.Name, Mode: VolumeRestoreInPlace,
			Status: RestorePreBackup, DockerName: services.DockerVolumeName(vol), PreBackupID: &pre.ID, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeRestore, ResourceType: "volume_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{
				"service": sc.Service.Slug, "backup": b.ID.String(), "volume": vol.Name, "mode": VolumeRestoreInPlace,
				"preRestoreBackup": pre.ID.String(),
			},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return VolumeRestoreView{}, err
		}
		return VolumeRestoreView{}, fmt.Errorf("start volume restore: %w", err)
	}
	s.publish(sc, r.ID)
	return volumeRestoreView(r), nil
}

// safetyBackupConfig is the configuration of the safety backup an in-place
// restore takes: the service's saved configuration (hooks, mode, excludes) in
// the destination of the backup being restored, or the defaults.
func (s *Service) safetyBackupConfig(ctx context.Context, sc services.Scope, dest db.BackupDestination) (db.VolumeBackupConfig, error) {
	cfg, err := s.loadVolumeConfig(ctx, s.q, sc.Service.ID)
	if err != nil {
		return db.VolumeBackupConfig{}, err
	}
	if cfg == nil {
		return db.VolumeBackupConfig{
			ServiceID: sc.Service.ID, WorkspaceID: sc.WorkspaceID, Mode: agentprotocol.VolModeFreeze,
			FreezeBudgetSeconds: agentprotocol.DefaultFreezeBudgetSeconds, DestinationID: dest.ID,
		}, nil
	}
	c := *cfg
	c.DestinationID = dest.ID
	return c, nil
}

// ListVolumeRestores returns a service's volume restores, newest first.
func (s *Service) ListVolumeRestores(ctx context.Context, serviceID uuid.UUID) ([]VolumeRestoreView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceVolumeRestores(ctx, db.ListServiceVolumeRestoresParams{ServiceID: sc.Service.ID, Limit: 50})
	if err != nil {
		return nil, fmt.Errorf("list volume restores: %w", err)
	}
	out := make([]VolumeRestoreView, len(rows))
	for i, r := range rows {
		out[i] = volumeRestoreView(r)
	}
	return out, nil
}

// finishVolumeRestore marks a restore finished and tells listeners.
func (s *Service) finishVolumeRestore(ctx context.Context, id uuid.UUID, status, msg string) {
	r, err := s.q.SetVolumeRestoreProgress(ctx, db.SetVolumeRestoreProgressParams{ID: id, Status: status, Error: msg})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.ErrorContext(ctx, "finish volume restore", "restore_id", id, "err", err)
		}
		return
	}
	s.afterVolumeRestoreFinished(ctx, r)
}

func (s *Service) afterVolumeRestoreFinished(ctx context.Context, r db.VolumeRestore) {
	sc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID)
	if err != nil {
		return
	}
	s.publish(sc, r.ID)
	typ := NotifyRestoreSucceeded
	if r.Status == RestoreFailed {
		typ = NotifyRestoreFailed
	}
	s.notify(ctx, Event{
		Type: typ, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ProjectName: sc.ProjectName, Environment: sc.EnvSlug,
		ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, BackupID: r.BackupID, RestoreID: r.ID, Engine: volumeEngineLabel,
		Message: r.Error, At: s.now(),
	})
}

// sendVolumeRestore enqueues the volume_restore command of an in-place restore
// whose safety backup succeeded.
func (s *Service) sendVolumeRestore(ctx context.Context, r db.VolumeRestore) error {
	b, err := s.q.GetVolumeBackupInternal(ctx, r.BackupID)
	if err != nil {
		return fmt.Errorf("load backup: %w", err)
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return fmt.Errorf("load destination: %w", err)
	}
	repo, err := s.q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: r.ServiceID, DestinationID: b.DestinationID})
	if err != nil {
		return fmt.Errorf("load repository: %w", err)
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID)
	if err != nil {
		return fmt.Errorf("load service: %w", err)
	}
	if sc.Service.ServerID == nil {
		return errors.New("the service has no server")
	}
	vols, err := s.q.ListServiceVolumes(ctx, r.ServiceID)
	if err != nil {
		return err
	}
	var vol *db.Volume
	for i := range vols {
		if vols[i].Name == r.VolumeName {
			vol = &vols[i]
		}
	}
	if vol == nil {
		return fmt.Errorf("the volume %s no longer exists", r.VolumeName)
	}
	agentRepo, err := s.volumeRepo(ctx, dest, repo.RepoPath)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(agentprotocol.VolumeRestorePayload{
		RestoreID: r.ID.String(), ServiceID: r.ServiceID.String(), Repo: agentRepo, SnapshotID: b.SnapshotID,
		Volume:  agentprotocol.VolumeRef{Name: vol.Name, DockerName: services.DockerVolumeName(*vol), MountPath: vol.MountPath},
		InPlace: true, StopGraceSeconds: sc.Config().StopGraceSeconds,
	})
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: *sc.Service.ServerID, Kind: agentprotocol.KindVolumeRestore, Payload: payload, IdempotencyKey: "volume_restore:" + r.ID.String(),
		})
		if err != nil {
			return err
		}
		_, err = db.New(tx).SetVolumeRestoreProgress(ctx, db.SetVolumeRestoreProgressParams{ID: r.ID, Status: RestoreRestoring, CommandID: &cmdID})
		return err
	})
}

// stageRestoreSwap applies a succeeded restore into a new volume: in one
// transaction the volume row's previous Docker volume is retired, the row
// points at the restored volume and the service's generation is bumped (a
// staged change, applied by the next deploy).
func (s *Service) stageRestoreSwap(ctx context.Context, r db.VolumeRestore, newDocker string) error {
	b, err := s.q.GetVolumeBackupInternal(ctx, r.BackupID)
	if err != nil {
		return fmt.Errorf("load backup: %w", err)
	}
	if newDocker == "" {
		newDocker = r.DockerName
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		vols, err := q.ListServiceVolumes(ctx, r.ServiceID)
		if err != nil {
			return err
		}
		var vol *db.Volume
		for i := range vols {
			if vols[i].Name == r.VolumeName {
				vol = &vols[i]
			}
		}
		if vol == nil {
			return errNoVolumeForSwap
		}
		if old := services.DockerVolumeName(*vol); old != newDocker {
			if err := q.InsertRetiredVolume(ctx, db.InsertRetiredVolumeParams{
				ServiceID: r.ServiceID, VolumeName: vol.Name, DockerName: old,
				Reason: "Replaced by a restore of the backup from " + strings.TrimPrefix(restoreLabel(b), "restored from "),
			}); err != nil {
				return err
			}
		}
		if err := q.SetVolumeDockerName(ctx, db.SetVolumeDockerNameParams{ID: vol.ID, DockerName: newDocker, RestoreLabel: restoreLabel(b)}); err != nil {
			return err
		}
		if _, err := q.BumpServiceGeneration(ctx, r.ServiceID); err != nil {
			return err
		}
		_, err = q.SetVolumeRestoreProgress(ctx, db.SetVolumeRestoreProgressParams{ID: r.ID, Status: RestoreSucceeded})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already finished (duplicate result)
	}
	if err != nil {
		return err
	}
	if sc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID); err == nil {
		services.PublishService(s.bus, services.EventServiceUpdated, sc)
	}
	done, err := s.q.GetVolumeRestoreInternal(ctx, r.ID)
	if err == nil {
		s.afterVolumeRestoreFinished(ctx, done)
	}
	return nil
}

var errNoVolumeForSwap = errors.New("the volume no longer exists")
