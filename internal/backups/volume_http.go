package backups

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type retiredIDInput struct {
	ID uuid.UUID `path:"retiredId"`
}

type volumeConfigOutput struct{ Body VolumeBackupConfigView }
type volumeBackupOutput struct{ Body VolumeBackupView }
type volumeRestoreOutput struct{ Body VolumeRestoreView }
type volumeFilesOutput struct{ Body VolumeFileListing }
type recoveryKeyOutput struct{ Body VolumeBackupRecoveryKey }

type volumeBackupListOutput struct {
	Body struct {
		Items []VolumeBackupView `json:"items"`
	}
}
type volumeRestoreListOutput struct {
	Body struct {
		Items []VolumeRestoreView `json:"items"`
	}
}
type retiredVolumeListOutput struct {
	Body struct {
		Items []RetiredVolumeView `json:"items"`
	}
}

type putVolumeConfigInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body VolumeBackupConfigInput
}

type createVolumeBackupInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct{}
}

type listVolumeBackupsInput struct {
	ID    uuid.UUID `path:"serviceId"`
	Limit int       `query:"limit" minimum:"0" maximum:"200" default:"50"`
}

type lockVolumeBackupInput struct {
	ID   uuid.UUID `path:"backupId"`
	Body struct {
		Locked bool `json:"locked"`
	}
}

type restoreVolumeBackupInput struct {
	ID   uuid.UUID `path:"backupId"`
	Body struct {
		Volume      string `json:"volume" minLength:"1" maxLength:"63" doc:"The volume's name in the service."`
		Mode        string `json:"mode,omitempty" enum:"new_volume,in_place" default:"new_volume" doc:"new_volume restores into a new Docker volume and stages the swap (apply it with Deploy); the current volume is kept. in_place stops the service, overwrites the volume after a safety backup and starts it again."`
		ConfirmSlug string `json:"confirmSlug,omitempty" doc:"In-place restores: the service's slug."`
	}
}

type listVolumeFilesInput struct {
	ID     uuid.UUID `path:"backupId"`
	Volume string    `query:"volume" minLength:"1" maxLength:"63" doc:"The volume's name in the service."`
	Path   string    `query:"path" default:"/" maxLength:"4096" doc:"Absolute directory path inside the volume."`
}

type recoveryKeyInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct{}
}

// RegisterVolume attaches the volume backup endpoints. The raw file download is
// a chi route (MountAPI), documented here in prose.
func RegisterVolume(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"volume-backups"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getVolumeBackupConfig", Method: http.MethodGet, Path: "/services/{serviceId}/volume-backup", Tags: tag,
		Summary: "A service's volume backup configuration",
		Description: "Always 200: `configured: false` with the defaults when none is saved. `eligible: false` (with `ineligibleReason`) for services that cannot " +
			"use volume backups: managed databases (use database backups), route services and services without volumes.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*volumeConfigOutput, error) {
		v, err := svc.GetVolumeBackupConfig(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &volumeConfigOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "putVolumeBackupConfig", Method: http.MethodPut, Path: "/services/{serviceId}/volume-backup", Tags: tag,
		Summary: "Save a service's volume backup configuration",
		Description: "Destination, mode, hooks, excludes, retention and the optional schedule. 422 `volume_backup_unavailable` for ineligible services, " +
			"`invalid_cron` for a bad schedule. The first backup creates the repository.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *putVolumeConfigInput) (*volumeConfigOutput, error) {
		v, err := svc.PutVolumeBackupConfig(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &volumeConfigOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteVolumeBackupConfig", Method: http.MethodDelete, Path: "/services/{serviceId}/volume-backup", Tags: tag,
		Summary: "Remove a service's volume backup configuration", DefaultStatus: http.StatusNoContent,
		Description: "Turns the schedule off. Existing backups and the repository are kept.", Errors: errs,
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*struct{}, error) {
		return nil, svc.DeleteVolumeBackupConfig(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createVolumeBackup", Method: http.MethodPost, Path: "/services/{serviceId}/volume-backups", Tags: tag,
		Summary: "Back up a service's volumes now", DefaultStatus: http.StatusAccepted,
		Description: "Needs a saved configuration and an online server. Returns 202 with a queued backup; poll it or listen for `backup.updated` events. " +
			"409 `server_offline` when it can not start; 422 `volume_backup_unavailable` for ineligible services or without a configuration.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *createVolumeBackupInput) (*volumeBackupOutput, error) {
		v, err := svc.CreateVolumeBackup(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &volumeBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listVolumeBackups", Method: http.MethodGet, Path: "/services/{serviceId}/volume-backups", Tags: tag,
		Summary: "List a service's volume backups", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listVolumeBackupsInput) (*volumeBackupListOutput, error) {
		items, err := svc.ListVolumeBackups(ctx, in.ID, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &volumeBackupListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getVolumeBackup", Method: http.MethodGet, Path: "/volume-backups/{backupId}", Tags: tag,
		Summary: "Get a volume backup", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *backupIDInput) (*volumeBackupOutput, error) {
		v, err := svc.GetVolumeBackup(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &volumeBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "lockVolumeBackup", Method: http.MethodPut, Path: "/volume-backups/{backupId}/lock", Tags: tag,
		Summary: "Lock or unlock a volume backup", Description: "Locked backups are never removed by retention or deleted.", Errors: errs,
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *lockVolumeBackupInput) (*volumeBackupOutput, error) {
		v, err := svc.SetVolumeBackupLocked(ctx, in.ID, in.Body.Locked)
		if err != nil {
			return nil, err
		}
		return &volumeBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteVolumeBackup", Method: http.MethodDelete, Path: "/volume-backups/{backupId}", Tags: tag,
		Summary: "Delete a volume backup", DefaultStatus: http.StatusNoContent,
		Description: "Removes the snapshot through the agent. 409 `backup_locked`, `backup_in_progress` or `server_offline`.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *backupIDInput) (*struct{}, error) {
		return nil, svc.DeleteVolumeBackup(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "restoreVolumeBackup", Method: http.MethodPost, Path: "/volume-backups/{backupId}/restore", Tags: tag,
		Summary: "Restore a volume of a backup", DefaultStatus: http.StatusAccepted,
		Description: "mode `new_volume` (default) restores into a new Docker volume and stages the swap on the service: apply it with Deploy; the current " +
			"volume is kept as a retired volume. mode `in_place` first takes a safety backup, then stops the service, overwrites the volume and starts it " +
			"again; it needs `confirmSlug`. 409 `backup_not_restorable`, `server_offline`, `backup_in_progress`; 422 `confirmation_required`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *restoreVolumeBackupInput) (*volumeRestoreOutput, error) {
		v, err := svc.RestoreVolumeBackup(ctx, in.ID, VolumeRestoreInput{Volume: in.Body.Volume, Mode: in.Body.Mode, ConfirmSlug: in.Body.ConfirmSlug})
		if err != nil {
			return nil, err
		}
		return &volumeRestoreOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listVolumeRestores", Method: http.MethodGet, Path: "/services/{serviceId}/volume-restores", Tags: tag,
		Summary: "List a service's volume restores", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*volumeRestoreListOutput, error) {
		items, err := svc.ListVolumeRestores(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &volumeRestoreListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listVolumeBackupFiles", Method: http.MethodGet, Path: "/volume-backups/{backupId}/files", Tags: tag,
		Summary: "Browse the files of a volume backup",
		Description: "Lists one directory of one volume inside the snapshot (at most 5000 entries). The agent reads the repository, so the server must be online " +
			"(409 `server_offline`). Requires the `deployments:write` scope: backups can contain secrets.\n\n" +
			"**Download.** `GET /volume-backups/{backupId}/files/content?volume=&path=&archive=` streams a file (`Content-Disposition: attachment`); " +
			"a directory is downloaded as a `.zip` with `archive=true`. Audited as `volume_backup.file.download`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *listVolumeFilesInput) (*volumeFilesOutput, error) {
		v, err := svc.ListVolumeBackupFiles(ctx, in.ID, in.Volume, in.Path)
		if err != nil {
			return nil, err
		}
		return &volumeFilesOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listRetiredVolumes", Method: http.MethodGet, Path: "/services/{serviceId}/retired-volumes", Tags: tag,
		Summary:     "List the Docker volumes a restore swapped out",
		Description: "Retired volumes are unmounted but kept on the server (their data is the rollback path of a restore) until deleted.", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*retiredVolumeListOutput, error) {
		items, err := svc.ListRetiredVolumes(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &retiredVolumeListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteRetiredVolume", Method: http.MethodDelete, Path: "/retired-volumes/{retiredId}", Tags: tag,
		Summary: "Delete a retired Docker volume", DefaultStatus: http.StatusAccepted,
		Description: "Enqueues the removal on the server; the row disappears once the agent confirmed. 409 `server_offline` while the server is offline.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *retiredIDInput) (*struct{}, error) {
		return nil, svc.DeleteRetiredVolume(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "revealVolumeBackupRecoveryKey", Method: http.MethodPost, Path: "/services/{serviceId}/volume-backup/recovery-key", Tags: tag,
		Summary: "Reveal the repository password",
		Description: "The restic repository and password, so a backup can be restored without NebulaCtrl. Requires the admin scope; audited as " +
			"`volume_backup.recovery_key.reveal`. 422 `volume_backup_unavailable` until the first backup created the repository.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *recoveryKeyInput) (*recoveryKeyOutput, error) {
		v, err := svc.RevealVolumeBackupRecoveryKey(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &recoveryKeyOutput{Body: v}, nil
	})
}

// MountAPI attaches the raw volume backup routes under /api/v1 (authenticated
// and CSRF checked by the API router): the file download.
func (s *Service) MountAPI(r chi.Router) {
	r.Get("/volume-backups/{backupId}/files/content", s.serveVolumeFile)
	s.mountDump(r)
}

func (s *Service) serveVolumeFile(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "backupId"))
	if err != nil {
		writeVolumeError(w, apierr.NotFound("backup"))
		return
	}
	q := r.URL.Query()
	err = s.DownloadVolumeFile(r.Context(), w, id, q.Get("volume"), q.Get("path"), q.Get("archive") == "true")
	if errors.Is(err, errVolumeAbort) {
		// The response started and then broke: abort the connection so the
		// client sees a failed download, not a complete short file.
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		writeVolumeError(w, err)
	}
}

func writeVolumeError(w http.ResponseWriter, err error) {
	if ae, ok := apierr.As(err); ok {
		apierr.Write(w, ae)
		return
	}
	apierr.Write(w, apierr.Internal())
}
