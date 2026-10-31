package sysupdate

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type viewOutput struct{ Body UpdateView }

type startInput struct {
	Body struct {
		Version string `json:"version" minLength:"1" maxLength:"64" doc:"The release to install. It must be the version the release feed announced (the view's latest)." example:"0.1.2"`
	}
}

// Register attaches GET and POST /system/update and POST /system/update/check.
func Register(h huma.API, svc *Service) {
	tags := []string{"system"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getSystemUpdate", Method: http.MethodGet, Path: "/system/update", Tags: tags,
		Summary: "Is a newer NebulaCtrl release available, and the state of an update",
		Description: "The version this control plane runs, the newest release the daily check found (`latest`, `available`, its release notes), whether the caller can update now (`canUpdate`; otherwise `cannotUpdateReason`), " +
			"the manual installer command (`manualCommand`, empty unless the caller is the instance admin: it names the mirror the update comes from) and the update in flight or just finished (`updating`). Any authenticated user. " +
			"With `NEBULACTRL_UPDATE_CHECK=false` there is no notice (`checkEnabled` is false). A development build never has an update.",
		Errors: []int{http.StatusUnauthorized},
	}), func(ctx context.Context, _ *struct{}) (*viewOutput, error) {
		v, err := svc.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "startSystemUpdate", Method: http.MethodPost, Path: "/system/update", Tags: tags,
		Summary:       "Update this control plane to the announced release",
		DefaultStatus: http.StatusAccepted,
		Description: "Sends the installer to the agent on the control plane's own server, which starts it detached from the agent and the control plane. The installer takes a control-plane backup first and puts the previous version back when the new one does not become healthy. " +
			"The dashboard is unavailable for about a minute. Poll `GET /system/update`: `updating.state` is `running`, then `succeeded` once the new version answers, or `failed` with the reason. " +
			"Instance admin only; audited as `instance.update.start` (the outcome as `instance.update.succeeded` or `instance.update.failed`). " +
			"409 `up_to_date` when nothing newer exists, 409 `update_in_progress` while one runs; 422 `version_not_offered` when `version` is not the announced release, " +
			"422 `update_unavailable` when checks are off or this control plane has no server whose agent can run the installer.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
	}), policy.ScopeAdmin), func(ctx context.Context, in *startInput) (*viewOutput, error) {
		v, err := svc.Start(ctx, in.Body.Version)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "checkSystemUpdate", Method: http.MethodPost, Path: "/system/update/check", Tags: tags,
		Summary: "Check for a newer NebulaCtrl release now",
		Description: "Reads the release feed now instead of waiting for the daily check, and returns the fresh view (the same body as `GET /system/update`: `latest`, `available`, `checkedAt`). " +
			"It waits for the feed for at most 15 seconds. Instance admin only. Reads only the public release feed. " +
			"409 `update_check_disabled` when checks are off (`NEBULACTRL_UPDATE_CHECK=false`); 502 `update_check_failed` when the feed cannot be read (the last known release stays); " +
			"429 with `Retry-After` when the feed was checked less than 30 seconds ago.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests, http.StatusBadGateway},
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*viewOutput, error) {
		v, err := svc.CheckNow(ctx)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})
}
