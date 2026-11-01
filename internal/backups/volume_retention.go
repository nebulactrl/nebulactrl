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
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// buildForgetPayload is the volume_forget command that applies a configuration's
// retention to a repository. stored are the service's successful, still stored
// backups in that repository. Locked backups travel as Protect so the agent
// never forgets them. It reports false when the configuration keeps everything.
func buildForgetPayload(serviceID uuid.UUID, repo agentprotocol.VolumeRepo, cfg db.VolumeBackupConfig, stored []db.VolumeBackup) (agentprotocol.VolumeForgetPayload, bool) {
	keep := agentprotocol.VolumeKeep{Daily: int(cfg.KeepDaily), Weekly: int(cfg.KeepWeekly), Monthly: int(cfg.KeepMonthly)}
	if !keep.Any() {
		return agentprotocol.VolumeForgetPayload{}, false
	}
	p := agentprotocol.VolumeForgetPayload{ServiceID: serviceID.String(), Repo: repo, Keep: &keep}
	for _, b := range stored {
		if b.Locked && b.SnapshotID != "" && b.ObjectDeletedAt == nil {
			p.Protect = append(p.Protect, b.SnapshotID)
		}
	}
	return p, true
}

// applyVolumeRetention enqueues the retention run after a successful backup.
// Failures are logged: retention retries after the next success.
func (s *Service) applyVolumeRetention(ctx context.Context, b db.VolumeBackup) {
	cfg, err := s.loadVolumeConfig(ctx, s.q, b.ServiceID)
	if err != nil {
		s.log.ErrorContext(ctx, "load volume backup config for retention", "service_id", b.ServiceID, "err", err)
		return
	}
	if cfg == nil || b.ServerID == nil {
		return
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return
	}
	repo, err := s.q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: b.ServiceID, DestinationID: b.DestinationID})
	if err != nil {
		return
	}
	stored, err := s.q.ListStoredVolumeBackups(ctx, db.ListStoredVolumeBackupsParams{ServiceID: b.ServiceID, DestinationID: b.DestinationID})
	if err != nil {
		s.log.ErrorContext(ctx, "list stored volume backups", "service_id", b.ServiceID, "err", err)
		return
	}
	agentRepo, err := s.volumeRepo(ctx, dest, repo.RepoPath)
	if err != nil {
		s.log.WarnContext(ctx, "volume retention not started", "service_id", b.ServiceID, "err", err)
		return
	}
	p, ok := buildForgetPayload(b.ServiceID, agentRepo, *cfg, stored)
	if !ok {
		return
	}
	raw, _ := json.Marshal(p)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: *b.ServerID, Kind: agentprotocol.KindVolumeForget, Payload: raw})
		return err
	})
	if err != nil {
		s.log.WarnContext(ctx, "volume retention not started", "service_id", b.ServiceID, "err", err)
	}
}

// ---- retired volumes ----

// ListRetiredVolumes returns the Docker volumes a restore swapped out.
func (s *Service) ListRetiredVolumes(ctx context.Context, serviceID uuid.UUID) ([]RetiredVolumeView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListRetiredVolumes(ctx, sc.Service.ID)
	if err != nil {
		return nil, fmt.Errorf("list retired volumes: %w", err)
	}
	out := make([]RetiredVolumeView, len(rows))
	for i, r := range rows {
		out[i] = retiredVolumeView(r)
	}
	return out, nil
}

// DeleteRetiredVolume enqueues the removal of a retired Docker volume; the row
// disappears once the agent confirmed. The service's server must be online.
func (s *Service) DeleteRetiredVolume(ctx context.Context, id uuid.UUID) error {
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, nil); err != nil {
		return err
	}
	r, err := s.q.GetRetiredVolume(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("retired volume")
	}
	if err != nil {
		return fmt.Errorf("get retired volume: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, r.ServiceID)
	if err != nil {
		return err
	}
	if sc.Service.ServerID == nil {
		return apierr.Conflict("server_unavailable", "the service is not placed on a server")
	}
	name, online, err := s.serverState(ctx, s.q, sc.Service.ServerID)
	if err != nil {
		return err
	}
	if !online {
		return apierr.Conflict("server_offline", "the server "+name+" is offline; the volume can not be removed now")
	}
	// A volume the service mounts again (a discarded swap) must never be removed.
	vols, err := s.q.ListServiceVolumes(ctx, sc.Service.ID)
	if err != nil {
		return fmt.Errorf("list volumes: %w", err)
	}
	for _, v := range vols {
		if services.DockerVolumeName(v) == r.DockerName {
			return apierr.Conflict("volume_in_use", "the service mounts this Docker volume")
		}
	}
	if err := sc.Guard(ctx, guardDeleteRetiredVolume, fmt.Sprintf("Delete a retired volume of %s in production, with its data (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), true,
		[]string{"The Docker volume " + r.DockerName + " and everything in it are removed from the host."}, r.ID); err != nil {
		return err
	}
	raw, _ := json.Marshal(agentprotocol.VolumeRemovePayload{
		ServiceID: sc.Service.ID.String(), Names: []string{r.DockerName}, RetiredIDs: []string{r.ID.String()},
	})
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: *sc.Service.ServerID, Kind: agentprotocol.KindVolumeRemove, Payload: raw}); err != nil {
			return fmt.Errorf("enqueue volume_remove: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRetiredDelete, ResourceType: "retired_volume", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "volume": r.VolumeName, "dockerName": r.DockerName},
		})
	})
	if err != nil {
		return fmt.Errorf("delete retired volume: %w", err)
	}
	return nil
}

// ---- recovery key ----

// resticRepository renders the repository the way the agent passes it to restic.
func resticRepository(d db.BackupDestination, repoPath string) string {
	repoPath = strings.Trim(repoPath, "/")
	if d.Kind == agentprotocol.DestLocal {
		p := "<the agent's backup directory>"
		if d.LocalDir != "" {
			p += "/" + strings.Trim(d.LocalDir, "/")
		}
		return p + "/" + repoPath
	}
	var host string
	switch {
	case d.Endpoint != "":
		host = strings.TrimSuffix(d.Endpoint, "/")
	case d.Region != "":
		host = "s3." + d.Region + ".amazonaws.com"
	default:
		host = "s3.amazonaws.com"
	}
	return "s3:" + host + "/" + d.Bucket + "/" + repoPath
}

// RevealVolumeBackupRecoveryKey returns the repository and its password so a
// backup can be restored without NebulaCtrl. Admin scope; audited.
func (s *Service) RevealVolumeBackupRecoveryKey(ctx context.Context, serviceID uuid.UUID) (VolumeBackupRecoveryKey, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeAdmin, serviceID)
	if err != nil {
		return VolumeBackupRecoveryKey{}, err
	}
	cfg, err := s.loadVolumeConfig(ctx, s.q, serviceID)
	if err != nil {
		return VolumeBackupRecoveryKey{}, err
	}
	if cfg == nil {
		return VolumeBackupRecoveryKey{}, apierr.Unprocessable(codeVolumeUnavailable, "this service has no volume backup configuration")
	}
	repo, err := s.q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: serviceID, DestinationID: cfg.DestinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return VolumeBackupRecoveryKey{}, apierr.Unprocessable(codeVolumeUnavailable, "the repository is created by the first backup")
	}
	if err != nil {
		return VolumeBackupRecoveryKey{}, fmt.Errorf("get volume repo: %w", err)
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, cfg.DestinationID)
	if err != nil {
		return VolumeBackupRecoveryKey{}, fmt.Errorf("load destination: %w", err)
	}
	pw, err := s.box.OpenString(secretbox.VolumeReposPassword, secretbox.RowID(serviceID.String(), cfg.DestinationID.String()), repo.PasswordEnc)
	if err != nil {
		return VolumeBackupRecoveryKey{}, fmt.Errorf("decrypt repository password: %w", err)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeRecoveryKey, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "destination": dest.Name},
		})
	})
	if err != nil {
		return VolumeBackupRecoveryKey{}, fmt.Errorf("audit recovery key: %w", err)
	}
	note := "Open the repository with restic (RESTIC_REPOSITORY and RESTIC_PASSWORD) and your own S3 credentials. Keep the password safe: without it, and without NEBULACTRL_SECRET_KEY, the backups can not be read."
	if dest.Kind == agentprotocol.DestLocal {
		note = "The repository lives on the server's disk, in the agent's backup directory. " + note
	}
	return VolumeBackupRecoveryKey{Repository: resticRepository(dest, repo.RepoPath), Password: pw, Note: note}, nil
}
