package agentupgrade

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type viewOutput struct{ Body InstanceUpgradeView }

type serverOutput struct{ Body ServerUpgradeView }

type rolloutOutput struct{ Body RolloutView }

type patchInput struct {
	Body struct {
		AgentAutoUpgrade *bool `json:"agentAutoUpgrade,omitempty" doc:"Upgrade an agent that is behind the control plane when it connects."`
	}
}

type serverInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Force bool `json:"force,omitempty" doc:"Install a version this agent rolled back before."`
	} `required:"false"`
}

type fleetInput struct {
	Body struct {
		ServerIDs []uuid.UUID `json:"serverIds,omitempty" doc:"The servers to upgrade. Empty: every server whose agent can be upgraded now."`
		Force     bool        `json:"force,omitempty" doc:"Install a version an agent rolled back before."`
	} `required:"false"`
}

// Register attaches the agent upgrade endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"servers"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	runErrs := append(append([]int{}, errs...), http.StatusConflict, http.StatusUnprocessableEntity)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getInstanceUpgrade", Method: http.MethodGet, Path: "/instance/upgrade", Tags: tags,
		Summary: "Control-plane version, agent version skew per server and the pre-upgrade backup",
		Description: "The version the control plane runs, whether agents behind it upgrade themselves when they connect, the outcome of the backup the control plane took before its last upgrade, " +
			"and for every server how its agent's version compares (`behind`, `current`, `ahead`, `unknown`), whether it can be upgraded now (and if not, why) and the state of its latest upgrade.",
		Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*viewOutput, error) {
		v, err := svc.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateInstanceUpgrade", Method: http.MethodPatch, Path: "/instance/upgrade", Tags: tags,
		Summary:     "Switch the automatic agent upgrade",
		Description: "Needs the `servers:write` scope and the instance admin (the first account). Audited.",
		Errors:      errs,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *patchInput) (*viewOutput, error) {
		if in.Body.AgentAutoUpgrade == nil {
			return nil, huma.Error422UnprocessableEntity("agentAutoUpgrade is required")
		}
		v, err := svc.SetAutoUpgrade(ctx, *in.Body.AgentAutoUpgrade)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "upgradeServerAgent", Method: http.MethodPost, Path: "/servers/{serverId}/agent-upgrade", Tags: tags,
		Summary:       "Upgrade one server's agent to the control plane's version",
		DefaultStatus: http.StatusAccepted,
		Description: "The agent downloads the binary from this control plane, checks its SHA-256 against the one in the command, replaces itself, restarts when its running work is done and rolls back if the new binary does not start. " +
			"Needs the Owner or Admin role; audited. 409 `server_offline`, `agent_current`, `agent_newer` or `upgrade_in_progress`; 422 `agent_predates_upgrade` (re-run the agent installer once) or `no_agent_binary`.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *serverInput) (*serverOutput, error) {
		v, err := svc.UpgradeServer(ctx, in.ID, in.Body.Force)
		if err != nil {
			return nil, err
		}
		return &serverOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "upgradeServerAgents", Method: http.MethodPost, Path: "/servers/agent-upgrades", Tags: tags,
		Summary:       "Upgrade agents one server at a time (staged)",
		DefaultStatus: http.StatusAccepted,
		Description: "Upgrades the given servers (every server whose agent can be upgraded now when none are given) in name order. Each upgrade must be confirmed by the agent reconnecting with the new version before the next one starts; " +
			"the first failure stops the rollout and leaves the remaining servers untouched. Needs the Owner or Admin role; audited. 409 `nothing_to_upgrade` when no agent qualifies.",
		Errors: runErrs,
	}), policy.ScopeServersWrite), func(ctx context.Context, in *fleetInput) (*rolloutOutput, error) {
		v, err := svc.UpgradeAll(ctx, in.Body.ServerIDs, in.Body.Force)
		if err != nil {
			return nil, err
		}
		return &rolloutOutput{Body: v}, nil
	})
}
