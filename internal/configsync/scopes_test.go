package configsync

// Scope table tests (no database). The table is also asserted against the use
// cases themselves by the database tests of the apply path.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

func TestScopeOfFieldTable(t *testing.T) {
	for id, want := range map[string]string{
		"processes.worker.scale.replicas":                     policy.ScopeDeploymentsWrite,
		"processes.worker.scale.autoscaling.enabled":          policy.ScopeDeploymentsWrite,
		"processes.worker.scale.autoscaling.targetCpuPercent": policy.ScopeDeploymentsWrite,
		"config.processes.worker":                             policy.ScopeProjectsWrite,
		"config.processName":                                  policy.ScopeProjectsWrite,
		"config.singleton":                                    policy.ScopeProjectsWrite,
		"scale.replicas":                                      policy.ScopeDeploymentsWrite,
		"scale.autoscaling.enabled":                           policy.ScopeDeploymentsWrite,
		"scale.autoscaling.targetCpuPercent":                  policy.ScopeDeploymentsWrite,
		"networks.shared":                                     policy.ScopeDeploymentsWrite,
		"backups.nightly":                                     policy.ScopeDeploymentsWrite,
		"uptime.enabled":                                      policy.ScopeAdmin,
		"uptime.reminderMinutes":                              policy.ScopeAdmin,
		"config.route.protocol":                               policy.ScopeProxyWrite,
		"config.route.upstreams":                              policy.ScopeProxyWrite,
		"config.route.allowCidrs":                             policy.ScopeProxyWrite,
		"config.port":                                         policy.ScopeProjectsWrite,
		"config.docker.privileged":                            policy.ScopeProjectsWrite,
		"config.stack.compose":                                policy.ScopeProjectsWrite,
		"config.files":                                        policy.ScopeProjectsWrite,
		"variables.NODE_ENV":                                  policy.ScopeProjectsWrite,
		"variables.NODE_ENV#secret":                           policy.ScopeProjectsWrite,
		"domains.shop.acme.com":                               policy.ScopeProjectsWrite,
		"domains.shop.acme.com.cloudflare":                    policy.ScopeProjectsWrite,
		"volumes.data":                                        policy.ScopeProjectsWrite,
		"links.db":                                            policy.ScopeProjectsWrite,
		"tasks.digest":                                        policy.ScopeProjectsWrite,
		"git.link":                                            policy.ScopeProjectsWrite,
		"name":                                                policy.ScopeProjectsWrite,
		"server":                                              policy.ScopeProjectsWrite,
		"project.APP_NAME":                                    policy.ScopeProjectsWrite,
		"":                                                    policy.ScopeProjectsWrite,
	} {
		require.Equal(t, want, scopeOfField(id), "id %q", id)
	}
}

func TestScopeOfFieldEveryCatalogIDNeedsAValidScope(t *testing.T) {
	for _, spec := range projectconfig.Fields {
		id := strings.ReplaceAll(spec.ID, "*", "sample")
		got := scopeOfField(id)
		require.True(t, policy.ValidScope(got), "%s needs the unknown scope %q", id, got)

		var want string
		switch {
		case strings.HasPrefix(id, "scale."), strings.HasPrefix(id, "processes."), strings.HasPrefix(id, "networks."), strings.HasPrefix(id, "backups."):
			want = policy.ScopeDeploymentsWrite
		case strings.HasPrefix(id, "uptime."):
			want = policy.ScopeAdmin
		case strings.HasPrefix(id, "config.route."):
			want = policy.ScopeProxyWrite
		default:
			want = policy.ScopeProjectsWrite
		}
		require.Equal(t, want, got, id)
	}
}

func TestScopeSetListOrder(t *testing.T) {
	// Insertion order must not matter: the list follows policy.AllScopes.
	orders := [][]string{
		{policy.ScopeAdmin, policy.ScopeProxyWrite, policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite},
		{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite, policy.ScopeAdmin},
		{policy.ScopeDeploymentsWrite, policy.ScopeAdmin, policy.ScopeProjectsWrite, policy.ScopeProxyWrite},
		{policy.ScopeProxyWrite, policy.ScopeProjectsWrite, policy.ScopeAdmin, policy.ScopeDeploymentsWrite},
	}
	want := []string{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite, policy.ScopeAdmin}
	for _, order := range orders {
		for range 10 { // map iteration order is random
			s := scopeSet{}
			for _, scope := range order {
				s.add(scope)
			}
			require.Equal(t, want, s.list(), "inserted as %v", order)
		}
	}

	s := scopeSet{}
	for _, scope := range []string{policy.ScopeAdmin, policy.ScopeProjectsRead, policy.ScopeServersWrite, policy.ScopeProjectsWrite, policy.ScopeLogsRead, policy.ScopeDeploymentsRollback} {
		s.add(scope)
	}
	require.Equal(t, []string{
		policy.ScopeProjectsRead, policy.ScopeLogsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsRollback, policy.ScopeServersWrite, policy.ScopeAdmin,
	}, s.list(), "every known scope keeps its place in policy.AllScopes")

	require.Equal(t, 0, scopeRank(policy.ScopeProjectsRead))
	require.Equal(t, len(policy.AllScopes)-1, scopeRank(policy.ScopeAdmin))
	require.Equal(t, len(policy.AllScopes), scopeRank("not-a-scope"), "an unknown scope sorts after admin")
	unknown := scopeSet{}
	unknown.add("custom:x")
	unknown.add(policy.ScopeAdmin)
	unknown.add(policy.ScopeProjectsWrite)
	require.Equal(t, []string{policy.ScopeProjectsWrite, policy.ScopeAdmin, "custom:x"}, unknown.list())
}

func TestScopeSetAddIgnoresTheEmptyScopeAndDuplicates(t *testing.T) {
	s := scopeSet{}
	s.add("")
	require.Empty(t, s.list())
	require.NotNil(t, s.list(), "an empty set lists as an empty slice, not null")

	s.add(policy.ScopeProjectsWrite)
	s.add(policy.ScopeProjectsWrite)
	s.add("")
	require.Equal(t, []string{policy.ScopeProjectsWrite}, s.list())
}

func TestMissingScopes(t *testing.T) {
	token := func(scopes ...string) *auth.Principal {
		return &auth.Principal{Type: auth.TypeToken, Scopes: scopes}
	}
	required := []string{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite, policy.ScopeAdmin}

	for _, tc := range []struct {
		name     string
		p        *auth.Principal
		required []string
		want     []string
	}{
		{"holds everything", token(required...), required, nil},
		{"holds a subset", token(policy.ScopeProjectsWrite), required, []string{policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite, policy.ScopeAdmin}},
		{"holds an unrelated scope", token(policy.ScopeProjectsRead), []string{policy.ScopeProjectsWrite}, []string{policy.ScopeProjectsWrite}},
		{"admin implies every scope", token(policy.ScopeAdmin), required, nil},
		{"admin alone covers projects:write", token(policy.ScopeAdmin), []string{policy.ScopeProjectsWrite}, nil},
		{"holds nothing", token(), required, required},
		{"no principal holds nothing", nil, required, required},
		{"nothing required", token(), nil, nil},
		{"nothing required of nobody", nil, nil, nil},
		{"order follows the required list", token(policy.ScopeProjectsWrite), []string{policy.ScopeAdmin, policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite}, []string{policy.ScopeAdmin, policy.ScopeDeploymentsWrite}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := missingScopes(tc.p, tc.required)
			if tc.want == nil {
				require.Empty(t, got)
				return
			}
			require.Equal(t, tc.want, got)
		})
	}

	// The system principal carries the scopes it was built with and nothing else.
	sys := auth.System(unitUUID(1), unitUUID(2), "git push", policy.ScopeProjectsWrite)
	require.Empty(t, missingScopes(sys, []string{policy.ScopeProjectsWrite}))
	require.Equal(t, []string{policy.ScopeDeploymentsWrite}, missingScopes(sys, []string{policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite}))
}

func TestInsufficientScopeErrorNamesEveryMissingScope(t *testing.T) {
	p := &auth.Principal{Type: auth.TypeToken, Scopes: []string{policy.ScopeProjectsWrite}}
	missing := missingScopes(p, []string{policy.ScopeProjectsWrite, policy.ScopeProxyWrite, policy.ScopeAdmin, policy.ScopeDeploymentsWrite})
	err := ErrInsufficientScope(missing)
	require.Equal(t, 403, err.Status)
	require.Equal(t, CodeInsufficientScope, err.Code)
	require.Equal(t, []string{policy.ScopeAdmin, policy.ScopeDeploymentsWrite, policy.ScopeProxyWrite}, err.Details["scopes"])
}
