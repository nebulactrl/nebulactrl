package domains_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The standard protection is on for every HTTP service and cannot be switched
// off: it must block what scanners probe for, and nothing an application
// plausibly serves.
func TestStandardProtectionBlocksSecretFilesOnly(t *testing.T) {
	m, err := agentprotocol.CompileFirewall(domains.RouteFirewall(nil))
	require.NoError(t, err)
	decide := func(path string) string {
		_, action := m.Decide(agentprotocol.FirewallRequest{Method: "GET", Path: path})
		return action
	}
	for _, path := range []string{
		"/.env", "/.env.production", "/.ENV", "/.envrc", "/api/.env",
		"/.git", "/.git/config", "/app/.git/HEAD", "/.git-credentials",
		"/.svn/entries", "/.hg/store", "/.bzr/branch",
		"/.aws/credentials", "/.ssh/id_rsa", "/.docker/config.json",
		"/.npmrc", "/.pypirc", "/.netrc", "/static/.htpasswd", "/.DS_Store",
	} {
		require.Equal(t, agentprotocol.FirewallBlock, decide(path), path)
	}
	for _, path := range []string{
		"/", "/.well-known/acme-challenge/token", "/.well-known/security.txt",
		"/.github/workflows/ci.yml", "/.gitignore", "/.gitlab-ci.yml", "/repo.git/info/refs",
		"/environment", "/env", "/.envoy/config", "/.dockerignore", "/dockerfile",
		"/docs/aws/setup", "/user/.profile", "/wp-login.php",
	} {
		require.Empty(t, decide(path), path)
	}
}

func TestServiceRulesRunBeforeTheStandardProtection(t *testing.T) {
	raw := []byte(`{"rules":[{"id":"example","name":"Public example","action":"allow","conditions":[{"field":"path","op":"exact","values":["/.env.example"]}]}]}`)
	fw := domains.RouteFirewall(raw)
	m, err := agentprotocol.CompileFirewall(fw)
	require.NoError(t, err)
	id, action := m.Decide(agentprotocol.FirewallRequest{Method: "GET", Path: "/.env.example"})
	require.Equal(t, "example", id)
	require.Equal(t, agentprotocol.FirewallAllow, action, "an allow rule of the service exempts a path")
	_, action = m.Decide(agentprotocol.FirewallRequest{Method: "GET", Path: "/.env"})
	require.Equal(t, agentprotocol.FirewallBlock, action)
}

func TestFullServiceFirewallPlusStandardFitsARoute(t *testing.T) {
	rules := make([]agentprotocol.FirewallRule, agentprotocol.MaxFirewallRules)
	for i := range rules {
		rules[i] = agentprotocol.FirewallRule{ID: fmt.Sprintf("r%d", i), Name: "r", Action: agentprotocol.FirewallBlock,
			Conditions: []agentprotocol.FirewallCondition{{Field: agentprotocol.FirewallFieldMethod, Op: agentprotocol.FirewallOpIn, Values: []string{"TRACE"}}}}
	}
	raw, err := json.Marshal(agentprotocol.Firewall{Rules: rules})
	require.NoError(t, err)
	fw := domains.RouteFirewall(raw)
	require.Len(t, fw.Rules, agentprotocol.MaxFirewallRules+len(domains.StandardRules()))
	require.NoError(t, fw.Validate(), "the agent accepts a full service firewall with the standard protection")
	for _, s := range domains.StandardRules() {
		require.True(t, domains.IsStandardRuleID(s.Rule.ID))
	}
}
