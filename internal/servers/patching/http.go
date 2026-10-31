package patching

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

type planInput struct {
	ID           uuid.UUID `path:"serverId"`
	SecurityOnly bool      `query:"securityOnly" doc:"Plan to upgrade only the packages with a security update"`
}

type runInput struct {
	ID    uuid.UUID `path:"serverId"`
	RunID uuid.UUID `path:"runId"`
}

type applyInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Confirm               string `json:"confirm" doc:"The server's name, typed by the person"`
		SecurityOnly          bool   `json:"securityOnly,omitempty" doc:"Upgrade only the packages with a security update"`
		AcknowledgeProduction bool   `json:"acknowledgeProduction,omitempty" doc:"Required when production services or the control plane run on the server"`
	}
}

type rebootInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Confirm               string `json:"confirm" doc:"The server's name, typed by the person"`
		AcknowledgeProduction bool   `json:"acknowledgeProduction,omitempty" doc:"Required when production services or the control plane run on the server"`
	}
}

type viewOutput struct{ Body PatchView }

type planOutput struct{ Body PatchPlan }

type runOutput struct{ Body PatchRun }

// Register attaches the server patching endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"servers"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	runErrs := append(append([]int{}, errs...), http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerPatches", Method: http.MethodGet, Path: "/servers/{serverId}/patches", Tags: tags,
		Summary: "Pending OS updates and reboot-required of a server",
		Description: "The Updates panel: how many packages are waiting (and how many are security updates), whether the host asks for a reboot, the pending packages with a warning " +
			"where an upgrade is risky, the production services that run on the server, and the recent runs. The agent checks every server about every 6 hours; `known` is false until the first check completes.",
		Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverInput) (*viewOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "planServerPatches", Method: http.MethodGet, Path: "/servers/{serverId}/patches/plan", Tags: tags,
		Summary: "What an upgrade of the server would do",
		Description: "The plan shown before the confirmation: the packages that would be upgraded, the risky ones among them and why, whether a reboot will probably be needed, whether Docker restarts, " +
			"the production services on the server and what the apply request must carry (the server's name, and `acknowledgeProduction` when `requiresAcknowledgement`). Changes nothing.",
		Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *planInput) (*planOutput, error) {
		p, err := svc.Plan(ctx, in.ID, in.SecurityOnly)
		if err != nil {
			return nil, err
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerPatchRun", Method: http.MethodGet, Path: "/servers/{serverId}/patches/runs/{runId}", Tags: tags,
		Summary:     "One check, upgrade or reboot of a server, with the upgrade's log",
		Description: "The log is the tail of what the package manager printed. A run in `rebooting` waits for the agent to reconnect on a new boot.",
		Errors:      errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *runInput) (*runOutput, error) {
		r, err := svc.GetRun(ctx, in.ID, in.RunID)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "checkServerPatches", Method: http.MethodPost, Path: "/servers/{serverId}/patches/check", Tags: tags,
		Summary:       "Check the server for updates now",
		DefaultStatus: http.StatusAccepted,
		Description: "Refreshes the package lists on the host and records the pending updates and reboot-required. Changes no package. Needs the Owner or Admin role. " +
			"409 `server_offline` when the server is offline, 409 `patching_running` while a run is in flight.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *serverInput) (*runOutput, error) {
		r, err := svc.Check(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "applyServerPatches", Method: http.MethodPost, Path: "/servers/{serverId}/patches/apply", Tags: tags,
		Summary:       "Upgrade the server's packages",
		DefaultStatus: http.StatusAccepted,
		Description: "Runs `apt-get upgrade` or `dnf upgrade` (security updates only with `securityOnly`) on the host as root, keeping the configuration files you changed, then checks again. " +
			"The upgrade runs detached on the host, so it finishes even when Docker restarts. Never reboots. Instance admin only; `confirm` must be the server's name; " +
			"a server that runs production services or the control plane also needs `acknowledgeProduction` (422 `production_ack_required`), and a token that asks for production approvals gets 409 `approval_required`. " +
			"Needs a completed check first (409 `no_check_yet`). Audited.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *applyInput) (*runOutput, error) {
		r, err := svc.Apply(ctx, in.ID, ApplyInput{
			Confirm: in.Body.Confirm, SecurityOnly: in.Body.SecurityOnly, AcknowledgeProduction: in.Body.AcknowledgeProduction,
		})
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "rebootServer", Method: http.MethodPost, Path: "/servers/{serverId}/patches/reboot", Tags: tags,
		Summary:       "Reboot the server",
		DefaultStatus: http.StatusAccepted,
		Description: "Schedules a reboot of the host a few seconds ahead. Every service on the server goes down until it boots and Docker starts them again. The run stays `rebooting` until the agent reconnects on a new boot " +
			"(it fails after 20 minutes). Instance admin only; `confirm` must be the server's name; a server that runs production services or the control plane also needs `acknowledgeProduction`. Audited.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *rebootInput) (*runOutput, error) {
		r, err := svc.Reboot(ctx, in.ID, RebootInput{Confirm: in.Body.Confirm, AcknowledgeProduction: in.Body.AcknowledgeProduction})
		if err != nil {
			return nil, err
		}
		return &runOutput{Body: r}, nil
	})
}
