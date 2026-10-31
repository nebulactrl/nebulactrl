package hardening

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serverInput struct {
	ID uuid.UUID `path:"serverId"`
}

type settingsInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Weekly bool `json:"weekly" doc:"Audit the host every week (default true)"`
	}
}

type auditInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		InstallLynis bool `json:"installLynis,omitempty" doc:"Install Lynis from the distribution's or CISOfy's package repository when the server does not have it"`
	} `required:"false"`
}

type fixesInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		FixIDs []string `json:"fixIds,omitempty" doc:"The fixes to apply or undo. Apply with none selects the default set (the ones marked default in the fix list); rollback needs at least one."`
	} `required:"false"`
}

type viewOutput struct{ Body HardeningView }

type runOutput struct{ Body HardeningRun }

// Register attaches the host hardening endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"servers"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerHardening", Method: http.MethodGet, Path: "/servers/{serverId}/hardening", Tags: tags,
		Summary: "Host hardening of a server: the Lynis index, the safe fixes and what is left",
		Description: "The Security tab: Lynis's hardening index from the latest audit, every curated safe fix with its state on the host (applied, already compliant, pending, unavailable), " +
			"the findings that remain with an explanation and the commands to deal with them, and the history of runs. NebulaCtrl never offers a fix for SSH, the firewall or accounts.",
		Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverInput) (*viewOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServerHardening", Method: http.MethodPut, Path: "/servers/{serverId}/hardening", Tags: tags,
		Summary:     "Switch the weekly Lynis audit of a server",
		Description: "Needs the `servers:write` scope and the Owner or Admin role. Audited.",
		Errors:      errs,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *settingsInput) (*viewOutput, error) {
		v, err := svc.SetWeekly(ctx, in.ID, in.Body.Weekly)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	runErrs := append(append([]int{}, errs...), http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity)
	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "runServerHardeningAudit", Method: http.MethodPost, Path: "/servers/{serverId}/hardening/audit", Tags: tags,
		Summary:       "Audit the host with Lynis now",
		DefaultStatus: http.StatusAccepted,
		Description: "Queues a Lynis audit on the server. It only reads the host. With `installLynis`, the agent first installs Lynis (from CISOfy's repository, else the distribution's). " +
			"Needs the Owner or Admin role. 409 `server_offline` when the server is offline, 409 `hardening_running` while a run is in flight.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *auditInput) (*runOutput, error) {
		r, err := svc.Audit(ctx, in.ID, in.Body.InstallLynis)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "applyServerHardening", Method: http.MethodPost, Path: "/servers/{serverId}/hardening/apply", Tags: tags,
		Summary:       "Apply safe hardening fixes",
		DefaultStatus: http.StatusAccepted,
		Description: "Applies the selected fixes (the default set when none is selected), checks each one and undoes it by itself if it did not take effect, then audits again so the new index is known. " +
			"Each fix is reversible with the rollback endpoint. Never changes SSH, the firewall or accounts. Needs the Owner or Admin role; audited.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *fixesInput) (*runOutput, error) {
		r, err := svc.Apply(ctx, in.ID, in.Body.FixIDs)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "rollbackServerHardening", Method: http.MethodPost, Path: "/servers/{serverId}/hardening/rollback", Tags: tags,
		Summary:       "Undo safe hardening fixes",
		DefaultStatus: http.StatusAccepted,
		Description: "Restores what NebulaCtrl changed for the selected fixes: the previous sysctl values, file modes, login.defs lines and banners. A fix NebulaCtrl did not apply is left alone. " +
			"Needs the Owner or Admin role; audited.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *fixesInput) (*runOutput, error) {
		r, err := svc.Rollback(ctx, in.ID, in.Body.FixIDs)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})
}
