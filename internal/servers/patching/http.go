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

type autoUpdateInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Enabled               bool     `json:"enabled" doc:"Apply updates automatically every night"`
		Scope                 string   `json:"scope" enum:"security,all" doc:"security: only the packages with a security update (apt and dnf); all: every package"`
		WindowStart           string   `json:"windowStart" doc:"Start of the nightly window as HH:MM (24-hour) in timezone, for example 03:00. Avoid 01:00-02:59 where clocks change"`
		WindowMinutes         int      `json:"windowMinutes" doc:"Length of the window in minutes, 60-240. Nothing starts in its last 15 minutes"`
		Timezone              string   `json:"timezone" doc:"IANA time zone of windowStart, for example Europe/Berlin; UTC when empty"`
		Reboot                string   `json:"reboot" enum:"never,whenRequired" doc:"whenRequired: reboot after an upgrade that asks for it, inside the window, with guards"`
		HoldPackages          []string `json:"holdPackages" nullable:"false" doc:"Package names a scheduled run never upgrades, at most 20, extra to the built-in holds (Docker and containerd)"`
		AcknowledgeProduction bool     `json:"acknowledgeProduction,omitempty" doc:"Required to enable when production services or the control plane run on the server"`
	}
}

type autoUpdateOutput struct{ Body PatchAuto }

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
			"where an upgrade is risky, the production services that run on the server, the recent runs, and `auto`, the automatic updates (settings, next window, last scheduled run, quiet-hour suggestion). The agent checks every server about every 6 hours; `known` is false until the first check completes.",
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

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServerPatchAuto", Method: http.MethodPut, Path: "/servers/{serverId}/patches/auto", Tags: tags,
		Summary: "Change the automatic updates of a server",
		Description: "Replaces the settings. When enabled, NebulaCtrl applies the pending updates every night inside the window (a stable per-server delay of up to 30 minutes is added), " +
			"never starts one in the last 15 minutes of the window, and holds back Docker and containerd packages (`builtinHolds`) plus `holdPackages`. It waits for deployments, builds and backups on the server " +
			"and runs one scheduled upgrade at a time on the instance. `reboot: whenRequired` reboots only when at least 10 minutes of the window remain, the server did not reboot automatically in the last 24 hours, " +
			"is not the control plane host and every running container restarts by itself. Three failed runs in a row switch it off. Instance admin only; a server that runs production services or the control plane " +
			"also needs `acknowledgeProduction` to enable (422 `production_ack_required`). 422 `invalid_timezone`, `invalid_window`, `scope_unsupported`, `invalid_package`, `agent_too_old`, `patching_unsupported`. Audited.",
		Errors: runErrs,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *autoUpdateInput) (*autoUpdateOutput, error) {
		a, err := svc.UpdateAuto(ctx, in.ID, AutoInput{
			AutoSettings: AutoSettings{
				Enabled: in.Body.Enabled, Scope: in.Body.Scope, WindowStart: in.Body.WindowStart, WindowMinutes: in.Body.WindowMinutes,
				Timezone: in.Body.Timezone, Reboot: in.Body.Reboot, HoldPackages: in.Body.HoldPackages,
			},
			AcknowledgeProduction: in.Body.AcknowledgeProduction,
		})
		if err != nil {
			return nil, err
		}
		return &autoUpdateOutput{Body: a}, nil
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
