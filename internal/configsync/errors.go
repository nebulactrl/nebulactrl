package configsync

import (
	"net/http"
	"sort"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// Error codes of the config use cases.
const (
	CodeConfigBlocked          = "config_blocked"
	CodePlanStale              = "plan_stale"
	CodeInsufficientScope      = "insufficient_scope"
	CodeUnknownEnvironment     = "unknown_environment"
	CodeEnvironmentRequired    = "environment_required"
	CodePruneNotAllowed        = "prune_not_allowed"
	CodeDeployNotAllowed       = "deploy_not_allowed"
	CodeFieldNotOwned          = "field_not_owned"
	CodeProductionAutoApplyOff = "production_auto_apply_unsupported"
)

// ErrConfigBlocked is the 422 returned when the plan has blockers; details.plan carries the plan.
func ErrConfigBlocked(p Plan) *apierr.Error {
	return apierr.Unprocessable(CodeConfigBlocked, "the file has errors; fix them and plan again").
		WithDetails(map[string]any{"plan": p})
}

// ErrPlanStale is the 409 returned when the environment changed since the plan was made; details.plan carries the fresh plan.
func ErrPlanStale(p Plan) *apierr.Error {
	return apierr.Conflict(CodePlanStale, "the environment or the file changed since this plan was made; review the new plan").
		WithDetails(map[string]any{"plan": p})
}

// ErrInsufficientScope is the 403 returned (before any write) when the caller lacks a scope the plan needs.
func ErrInsufficientScope(missing []string) *apierr.Error {
	m := append([]string(nil), missing...)
	sort.Strings(m)
	return apierr.New(http.StatusForbidden, CodeInsufficientScope, "the token lacks scopes this plan needs").
		WithDetails(map[string]any{"scopes": m})
}
