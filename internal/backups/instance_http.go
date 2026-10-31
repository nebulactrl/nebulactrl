package backups

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type instanceBackupOutput struct{ Body InstanceBackupView }
type instanceRunOutput struct{ Body InstanceRunView }
type instanceRunListOutput struct {
	Body struct {
		Items []InstanceRunView `json:"items"`
	}
}

type patchInstanceBackupInput struct {
	Body struct {
		Enabled        *bool   `json:"enabled,omitempty" doc:"Run the schedule. Needs a complete destination."`
		Schedule       *string `json:"schedule,omitempty" doc:"5-field cron expression or @daily etc., evaluated in UTC (default 30 3 * * *). At most one run every 5 minutes."`
		RetentionCount *int    `json:"retentionCount,omitempty" minimum:"1" maximum:"365" doc:"Successful backups kept (default 14)."`
		Kind           *string `json:"kind,omitempty" enum:"s3,local,builtin" doc:"Changing the kind starts the destination afresh. builtin is the system bucket of the built-in S3 on the control-plane host: it needs no endpoint, bucket or keys, but it is not off-site."`
		Endpoint       *string `json:"endpoint,omitempty" doc:"S3-compatible endpoint URL; empty means AWS S3."`
		Region         *string `json:"region,omitempty"`
		Bucket         *string `json:"bucket,omitempty"`
		Prefix         *string `json:"prefix,omitempty" doc:"Key prefix inside the bucket or directory."`
		PathStyle      *bool   `json:"pathStyle,omitempty"`
		CompatMode     *bool   `json:"compatMode,omitempty"`
		AccessKey      *string `json:"accessKey,omitempty" doc:"Write-only. Omit or leave empty to keep the stored key."`
		SecretKey      *string `json:"secretKey,omitempty" doc:"Write-only. Omit or leave empty to keep the stored key."`
		LocalDir       *string `json:"localDir,omitempty" doc:"Local destinations: relative sub-directory of NEBULACTRL_BACKUP_DIR."`
	}
}

type listInstanceRunsInput struct {
	Limit int `query:"limit" minimum:"0" maximum:"200" default:"50"`
}

// RegisterInstance attaches the control-plane backup endpoints. All of them
// belong to the instance admin (the first account).
func RegisterInstance(h huma.API, svc *InstanceService) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}
	tag := []string{"instance"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getInstanceBackup", Method: http.MethodGet, Path: "/instance/backup", Tags: tag,
		Summary: "Get the control-plane backup settings",
		Description: "A scheduled pg_dump of NebulaCtrl's own database to one instance-wide destination. The instance secret key is never part of a backup. " +
			"Instance admin only.",
		Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*instanceBackupOutput, error) {
		v, err := svc.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &instanceBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateInstanceBackup", Method: http.MethodPatch, Path: "/instance/backup", Tags: tag,
		Summary:     "Update the control-plane backup settings",
		Description: "Credentials are write-only. Enabling needs a complete destination. Instance admin only.",
		Errors:      append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *patchInstanceBackupInput) (*instanceBackupOutput, error) {
		v, err := svc.Update(ctx, InstanceBackupPatch(in.Body))
		if err != nil {
			return nil, err
		}
		return &instanceBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testInstanceBackup", Method: http.MethodPost, Path: "/instance/backup/test", Tags: tag,
		Summary:     "Test the control-plane backup destination",
		Description: "The control plane writes, reads back and deletes a probe object. The verdict is stored on the settings.",
		Errors:      append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*instanceBackupOutput, error) {
		v, err := svc.Test(ctx)
		if err != nil {
			return nil, err
		}
		return &instanceBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "acknowledgeInstanceBackupKey", Method: http.MethodPost, Path: "/instance/backup/key-acknowledgement", Tags: tag,
		Summary: "Confirm the instance secret key is stored separately",
		Description: "Dismisses the reminder card. Backups do not contain NEBULACTRL_SECRET_KEY, and a restore is useless without it: " +
			"store it in a password manager or vault, apart from the backups.",
		Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*instanceBackupOutput, error) {
		v, err := svc.AcknowledgeKey(ctx)
		if err != nil {
			return nil, err
		}
		return &instanceBackupOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "runInstanceBackup", Method: http.MethodPost, Path: "/instance/backup/runs", Tags: tag,
		Summary: "Back up the control plane now", DefaultStatus: http.StatusAccepted,
		Description: "Returns 202 with the running backup; poll the runs or listen for `instance.backup.updated` events. " +
			"409 `backup_in_progress` while one is running.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*instanceRunOutput, error) {
		v, err := svc.RunNow(ctx)
		if err != nil {
			return nil, err
		}
		return &instanceRunOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listInstanceBackupRuns", Method: http.MethodGet, Path: "/instance/backup/runs", Tags: tag,
		Summary: "List control-plane backup runs", Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *listInstanceRunsInput) (*instanceRunListOutput, error) {
		items, err := svc.ListRuns(ctx, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &instanceRunListOutput{}
		out.Body.Items = items
		return out, nil
	})
}
