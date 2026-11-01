package backups

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type destinationIDInput struct {
	ID uuid.UUID `path:"destinationId"`
}
type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}
type scheduleIDInput struct {
	ID uuid.UUID `path:"scheduleId"`
}
type backupIDInput struct {
	ID uuid.UUID `path:"backupId"`
}

type destinationOutput struct{ Body DestinationView }
type scheduleOutput struct{ Body ScheduleView }
type backupOutput struct{ Body BackupView }
type restoreOutput struct{ Body RestoreView }

type destinationListOutput struct {
	Body struct {
		Items []DestinationView `json:"items"`
	}
}
type scheduleListOutput struct {
	Body struct {
		Items []ScheduleView `json:"items"`
	}
}
type backupListOutput struct {
	Body struct {
		Items []BackupView `json:"items"`
	}
}
type restoreListOutput struct {
	Body struct {
		Items []RestoreView `json:"items"`
	}
}

type createDestinationInput struct {
	Body struct {
		Name       string `json:"name" minLength:"1" maxLength:"80"`
		Kind       string `json:"kind" enum:"s3,local"`
		Endpoint   string `json:"endpoint,omitempty" doc:"S3-compatible endpoint URL; empty means AWS S3."`
		Region     string `json:"region,omitempty"`
		Bucket     string `json:"bucket,omitempty"`
		Prefix     string `json:"prefix,omitempty" doc:"Key prefix inside the bucket or directory."`
		PathStyle  bool   `json:"pathStyle,omitempty" doc:"Use path-style addressing (MinIO, SeaweedFS, Garage, most self-hosted stores)."`
		CompatMode *bool  `json:"compatMode,omitempty" doc:"Limit SDK checksums for non-AWS stores. Default: on when an endpoint is set."`
		AccessKey  string `json:"accessKey,omitempty" doc:"Write-only."`
		SecretKey  string `json:"secretKey,omitempty" doc:"Write-only."`
		LocalDir   string `json:"localDir,omitempty" doc:"Local destinations: relative sub-directory of the agent's backup directory."`
	}
}

type patchDestinationInput struct {
	ID   uuid.UUID `path:"destinationId"`
	Body struct {
		Name       *string `json:"name,omitempty"`
		Endpoint   *string `json:"endpoint,omitempty"`
		Region     *string `json:"region,omitempty"`
		Bucket     *string `json:"bucket,omitempty"`
		Prefix     *string `json:"prefix,omitempty"`
		PathStyle  *bool   `json:"pathStyle,omitempty"`
		CompatMode *bool   `json:"compatMode,omitempty"`
		AccessKey  *string `json:"accessKey,omitempty" doc:"Omit or leave empty to keep the stored key."`
		SecretKey  *string `json:"secretKey,omitempty" doc:"Omit or leave empty to keep the stored key."`
		LocalDir   *string `json:"localDir,omitempty"`
	}
}

type testDestinationInput struct {
	ID   uuid.UUID `path:"destinationId"`
	Body struct {
		ServerID uuid.UUID `json:"serverId" doc:"The server whose agent performs the test (it is the one that talks to the destination)."`
	}
}

type createScheduleInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		DestinationID  uuid.UUID `json:"destinationId"`
		Cron           string    `json:"cron" doc:"5-field cron expression or @hourly/@daily/@weekly/@monthly/@yearly. At most one run every 5 minutes."`
		Timezone       string    `json:"timezone,omitempty" doc:"IANA zone, default UTC."`
		Enabled        *bool     `json:"enabled,omitempty"`
		RetentionCount *int      `json:"retentionCount,omitempty" minimum:"0" maximum:"1000" doc:"Keep the newest N successful backups (default 7, 0 = no limit)."`
		RetentionDays  *int      `json:"retentionDays,omitempty" minimum:"0" maximum:"3650" doc:"Delete backups older than N days (0 = no limit)."`
	}
}

type patchScheduleInput struct {
	ID   uuid.UUID `path:"scheduleId"`
	Body struct {
		DestinationID  *uuid.UUID `json:"destinationId,omitempty"`
		Cron           *string    `json:"cron,omitempty"`
		Timezone       *string    `json:"timezone,omitempty"`
		Enabled        *bool      `json:"enabled,omitempty"`
		RetentionCount *int       `json:"retentionCount,omitempty" minimum:"0" maximum:"1000"`
		RetentionDays  *int       `json:"retentionDays,omitempty" minimum:"0" maximum:"3650"`
	}
}

type listBackupsInput struct {
	ID    uuid.UUID `path:"serviceId"`
	Limit int       `query:"limit" minimum:"0" maximum:"200" default:"50"`
}

type createBackupInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		DestinationID uuid.UUID `json:"destinationId"`
	}
}

type lockInput struct {
	ID   uuid.UUID `path:"backupId"`
	Body struct {
		Locked bool `json:"locked"`
	}
}

type restoreInput struct {
	ID   uuid.UUID `path:"backupId"`
	Body struct {
		Mode        string     `json:"mode,omitempty" enum:"new,in_place" default:"new" doc:"new: restore into a new sibling database (default, the original is untouched). in_place: overwrite this database after a safety backup."`
		Name        string     `json:"name,omitempty" doc:"Name of the new database (mode new)."`
		ServerID    *uuid.UUID `json:"serverId,omitempty" doc:"Server for the new database (mode new). A backup stored in a local directory can only be restored on its own server."`
		ConfirmSlug string     `json:"confirmSlug,omitempty" doc:"In-place restores: the database's slug."`
	}
}

type restoreIDInput struct {
	ID uuid.UUID `path:"restoreId"`
}

// Register attaches the backup endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"backups"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listBackupDestinations", Method: http.MethodGet, Path: "/backup-destinations", Tags: tag,
		Summary: "List backup destinations", Errors: errs[:2],
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*destinationListOutput, error) {
		items, err := svc.ListDestinations(ctx)
		if err != nil {
			return nil, err
		}
		out := &destinationListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createBackupDestination", Method: http.MethodPost, Path: "/backup-destinations", Tags: tag,
		Summary: "Add a backup destination", DefaultStatus: http.StatusCreated,
		Description: "An S3-compatible bucket or a directory on the database's host (local; not off-site). Credentials are write-only. " +
			"Requires the admin scope. Recommended S3 permissions: s3:PutObject, s3:GetObject, s3:DeleteObject, s3:ListBucket, s3:AbortMultipartUpload on a bucket-scoped key.",
		Errors: append(errs[:2], http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeAdmin), func(ctx context.Context, in *createDestinationInput) (*destinationOutput, error) {
		v, err := svc.CreateDestination(ctx, DestinationInput(in.Body))
		if err != nil {
			return nil, err
		}
		return &destinationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateBackupDestination", Method: http.MethodPatch, Path: "/backup-destinations/{destinationId}", Tags: tag,
		Summary: "Update a backup destination", Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *patchDestinationInput) (*destinationOutput, error) {
		v, err := svc.UpdateDestination(ctx, in.ID, DestinationPatch(in.Body))
		if err != nil {
			return nil, err
		}
		return &destinationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteBackupDestination", Method: http.MethodDelete, Path: "/backup-destinations/{destinationId}", Tags: tag,
		Summary: "Delete a backup destination", DefaultStatus: http.StatusNoContent,
		Description: "409 `destination_in_use` while a schedule or a stored backup uses it.", Errors: append(errs, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *destinationIDInput) (*struct{}, error) {
		return nil, svc.DeleteDestination(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testBackupDestination", Method: http.MethodPost, Path: "/backup-destinations/{destinationId}/test", Tags: tag,
		Summary: "Test a backup destination",
		Description: "The agent on the chosen server writes, reads back and deletes a probe object. Waits up to 25 seconds; " +
			"if the agent has not answered by then the response carries no test result yet (poll the destination).",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *testDestinationInput) (*destinationOutput, error) {
		v, err := svc.TestDestination(ctx, in.ID, in.Body.ServerID)
		if err != nil {
			return nil, err
		}
		return &destinationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listBackupSchedules", Method: http.MethodGet, Path: "/services/{serviceId}/backup-schedules", Tags: tag,
		Summary: "List a database's backup schedules", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*scheduleListOutput, error) {
		items, err := svc.ListSchedules(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &scheduleListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createBackupSchedule", Method: http.MethodPost, Path: "/services/{serviceId}/backup-schedules", Tags: tag,
		Summary: "Schedule recurring backups", DefaultStatus: http.StatusCreated,
		Errors: append(errs, http.StatusUnprocessableEntity),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *createScheduleInput) (*scheduleOutput, error) {
		v, err := svc.CreateSchedule(ctx, in.ID, ScheduleInput(in.Body))
		if err != nil {
			return nil, err
		}
		return &scheduleOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateBackupSchedule", Method: http.MethodPatch, Path: "/backup-schedules/{scheduleId}", Tags: tag,
		Summary: "Update a backup schedule", Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *patchScheduleInput) (*scheduleOutput, error) {
		v, err := svc.UpdateSchedule(ctx, in.ID, SchedulePatch(in.Body))
		if err != nil {
			return nil, err
		}
		return &scheduleOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteBackupSchedule", Method: http.MethodDelete, Path: "/backup-schedules/{scheduleId}", Tags: tag,
		Summary: "Delete a backup schedule", DefaultStatus: http.StatusNoContent, Errors: errs,
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *scheduleIDInput) (*struct{}, error) {
		return nil, svc.DeleteSchedule(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listBackups", Method: http.MethodGet, Path: "/services/{serviceId}/backups", Tags: tag,
		Summary: "List a database's backups", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listBackupsInput) (*backupListOutput, error) {
		items, err := svc.ListBackups(ctx, in.ID, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &backupListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createBackup", Method: http.MethodPost, Path: "/services/{serviceId}/backups", Tags: tag,
		Summary: "Back up a database now", DefaultStatus: http.StatusAccepted,
		Description: "Returns 202 with a queued backup; poll it or listen for `backup.updated` events. 409 `server_offline` / `database_not_running` when it can not start.",
		Errors:      append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *createBackupInput) (*backupOutput, error) {
		v, err := svc.CreateBackup(ctx, in.ID, in.Body.DestinationID)
		if err != nil {
			return nil, err
		}
		return &backupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getBackup", Method: http.MethodGet, Path: "/backups/{backupId}", Tags: tag,
		Summary: "Get a backup", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *backupIDInput) (*backupOutput, error) {
		v, err := svc.GetBackup(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &backupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "lockBackup", Method: http.MethodPut, Path: "/backups/{backupId}/lock", Tags: tag,
		Summary: "Lock or unlock a backup", Description: "Locked backups are never removed by retention or deleted.", Errors: errs,
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *lockInput) (*backupOutput, error) {
		v, err := svc.SetLocked(ctx, in.ID, in.Body.Locked)
		if err != nil {
			return nil, err
		}
		return &backupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteBackup", Method: http.MethodDelete, Path: "/backups/{backupId}", Tags: tag,
		Summary: "Delete a backup", DefaultStatus: http.StatusNoContent,
		Description: "Deletes the stored object through the agent. 409 when the backup is locked or its server is offline.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *backupIDInput) (*struct{}, error) {
		return nil, svc.DeleteBackup(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "restoreBackup", Method: http.MethodPost, Path: "/backups/{backupId}/restore", Tags: tag,
		Summary: "Restore a backup", DefaultStatus: http.StatusAccepted,
		Description: "mode `new` (default) creates a sibling database, deploys it empty and restores into it; the original is untouched. " +
			"mode `in_place` first takes a safety backup, then overwrites the database; it needs `confirmSlug`. Poll the restore.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite), func(ctx context.Context, in *restoreInput) (*restoreOutput, error) {
		v, err := svc.Restore(ctx, in.ID, RestoreInput(in.Body))
		if err != nil {
			return nil, err
		}
		return &restoreOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getBackupRestore", Method: http.MethodGet, Path: "/backup-restores/{restoreId}", Tags: tag,
		Summary: "Get a restore", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *restoreIDInput) (*restoreOutput, error) {
		v, err := svc.GetRestore(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &restoreOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listBackupRestores", Method: http.MethodGet, Path: "/services/{serviceId}/backup-restores", Tags: tag,
		Summary: "List restores of a database", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*restoreListOutput, error) {
		items, err := svc.ListRestores(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &restoreListOutput{}
		out.Body.Items = items
		return out, nil
	})
	RegisterDump(h, svc)
}
