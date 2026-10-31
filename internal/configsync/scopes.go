package configsync

import (
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// scopeOfField is the scope the platform's own use case enforces for a field
// id. The table is what apply checks up front, so a token that lacks a scope
// fails with insufficient_scope before any write (see scopes_test.go: the
// table is asserted against the use cases themselves).
func scopeOfField(id string) string {
	switch {
	case strings.HasPrefix(id, "scale."):
		return policy.ScopeDeploymentsWrite // scaling.Service.Set
	case strings.HasPrefix(id, "processes."):
		return policy.ScopeDeploymentsWrite // scaling.Service.SetProcess
	case strings.HasPrefix(id, "networks."):
		return policy.ScopeDeploymentsWrite // networks.Service.Attach / Detach
	case strings.HasPrefix(id, "backups."):
		return policy.ScopeDeploymentsWrite // backups schedule use cases
	case strings.HasPrefix(id, "uptime."):
		return policy.ScopeAdmin // uptime.Service.UpdateMonitor
	case strings.HasPrefix(id, "config.route."):
		return policy.ScopeProxyWrite // proxyroutes create/update
	}
	return policy.ScopeProjectsWrite
}

// scopeRank orders scopes the way the plan lists them.
func scopeRank(s string) int {
	for i, x := range policy.AllScopes {
		if x == s {
			return i
		}
	}
	return len(policy.AllScopes)
}

// scopeSet collects the scopes a plan needs.
type scopeSet map[string]bool

func (s scopeSet) add(scope string) {
	if scope != "" {
		s[scope] = true
	}
}

// list returns the scopes, projects:write first and admin last (policy.AllScopes order
// with the write scopes before the rest).
func (s scopeSet) list() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return scopeRank(out[i]) < scopeRank(out[j]) })
	return out
}

// missingScopes returns the scopes of required that p does not hold (admin implies every scope).
func missingScopes(p *auth.Principal, required []string) []string {
	var out []string
	for _, s := range required {
		if p == nil || !policy.HasScope(p, s) {
			out = append(out, s)
		}
	}
	return out
}
