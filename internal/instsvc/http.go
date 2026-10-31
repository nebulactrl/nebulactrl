package instsvc

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type servicesOutput struct{ Body InstanceServicesView }

type sliceOutput struct{ Body SliceView }

type teamSliceOutput struct{ Body TeamSliceView }

type updateServicesInput struct {
	Body struct {
		DefaultQuotaBytes int64 `json:"defaultQuotaBytes" minimum:"1073741824" doc:"The S3 quota of every team without its own, in bytes. At least 1 GiB."`
	}
}

type sliceQuotaInput struct {
	SliceID uuid.UUID `path:"sliceId"`
	Body    struct {
		QuotaBytes *int64 `json:"quotaBytes" minimum:"1073741824" doc:"The team's own quota in bytes, at least 1 GiB; null resets it to the instance default."`
	}
}

type purgeInput struct {
	TeamID  uuid.UUID `path:"teamId"`
	Confirm string    `query:"confirm" doc:"The deleted team's name, as the slice table shows it."`
}

// Register attaches the instance services' routes: the instance administrator's
// Admin → Built-in services (`/admin/instance-services`, `/admin/team-slices`)
// and the team's read-only `GET /team-slice`.
func Register(h huma.API, svc *Service) {
	tags := []string{"instance-services"}
	adminErrs := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getInstanceServices", Method: http.MethodGet, Path: "/admin/instance-services", Tags: tags,
		Summary: "Built-in services: health, default quota and every team's slice",
		Description: "Instance administrator only (403 `instance_admin_required`). S3 holds NebulaCtrl's own data (backups), the registry the teams' images, " +
			"Loki their central logs. Each team has a slice of each; slices of deleted teams stay until `purgeAt`. No key, bucket or address is returned.",
		Errors: adminErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*servicesOutput, error) {
		v, err := svc.Overview(ctx)
		if err != nil {
			return nil, err
		}
		return &servicesOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateInstanceServices", Method: http.MethodPut, Path: "/admin/instance-services", Tags: tags,
		Summary: "Set the default S3 quota of the teams",
		Description: "Instance administrator only. The default applies to every team without its own quota (50 GiB until changed); the minimum is 1 GiB (422 `quota_too_small`). " +
			"Lowering it below what a team already uses only blocks that team's new backups: nothing is deleted. Audited.",
		Errors: append(adminErrs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateServicesInput) (*servicesOutput, error) {
		v, err := svc.UpdateDefaultQuota(ctx, in.Body.DefaultQuotaBytes)
		if err != nil {
			return nil, err
		}
		return &servicesOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkInstanceServices", Method: http.MethodPost, Path: "/admin/instance-services/check", Tags: tags,
		Summary:     "Check the built-in services now",
		Description: "Instance administrator only. Runs the same check as the minute-by-minute one: health of each service, the system bucket, and a slice for every team that lacks one.",
		Errors:      adminErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*servicesOutput, error) {
		v, err := svc.Check(ctx)
		if err != nil {
			return nil, err
		}
		return &servicesOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateTeamSliceQuota", Method: http.MethodPut, Path: "/admin/team-slices/{sliceId}/quota", Tags: tags,
		Summary: "Set one team's S3 quota",
		Description: "Instance administrator only. `quotaBytes` null resets the team to the instance default. Only the S3 slice has a quota (422 `quota_not_supported`). " +
			"Lowering it below use only blocks new backups; existing backups are never deleted to make room. Audited.",
		Errors: append(adminErrs, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *sliceQuotaInput) (*sliceOutput, error) {
		v, err := svc.UpdateSliceQuota(ctx, in.SliceID, in.Body.QuotaBytes)
		if err != nil {
			return nil, err
		}
		return &sliceOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "purgeTeamSlice", Method: http.MethodDelete, Path: "/admin/team-slices/{teamId}", Tags: tags,
		Summary: "Purge a deleted team's slices now", DefaultStatus: http.StatusNoContent,
		Description: "Instance administrator only. Deletes the bucket, its key, the registry images and the logs of a team that was deleted, instead of waiting for the 30 days. " +
			"`confirm` is the team's name. 409 `team_still_exists` while the team exists, 404 when it has no slices left, 422 `confirmation_mismatch`. Audited.",
		Errors: append(adminErrs, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, in *purgeInput) (*struct{}, error) {
		return nil, svc.PurgeTeamSlice(ctx, in.TeamID, in.Confirm)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTeamSlice", Method: http.MethodGet, Path: "/team-slice", Tags: tags,
		Summary: "The team's built-in backup storage, registry and logs",
		Description: "Used space and quota of the team's built-in backup storage, the size of its registry images and the log retention. " +
			"Never a key, bucket, repository prefix or address: the services are managed by the instance administrator.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*teamSliceOutput, error) {
		v, err := svc.TeamSlice(ctx)
		if err != nil {
			return nil, err
		}
		return &teamSliceOutput{Body: v}, nil
	})
}
