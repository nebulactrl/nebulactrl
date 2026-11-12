package domains

import (
	"encoding/json"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// StandardRuleIDPrefix starts the id of every standard protection rule. Ids a
// person writes may not use it (generated ids are hex and never do).
const StandardRuleIDPrefix = "std"

// StandardRule is one rule of the standard protection: what the Firewall
// settings show for it, and the rule itself.
type StandardRule struct {
	Rule        agentprotocol.FirewallRule `json:"rule"`
	Description string                     `json:"description" doc:"What the rule blocks, in a sentence"`
	Examples    []string                   `json:"examples" doc:"Request paths it blocks"`
}

// secretFiles matches a path segment that names a secret dotfile or folder:
// environment files, version control metadata, cloud and package-manager
// credentials. No application serves these on purpose; scanners request them
// from every public address within minutes. The segment must end there (or
// continue with "." or "/"), so .github, .gitignore, .well-known and
// repo.git stay untouched.
const secretFiles = `(?i)/\.(env|envrc|git|git-credentials|svn|hg|bzr|aws|ssh|docker|npmrc|pypirc|netrc|htpasswd|ds_store)(/|\.|$)`

// standard is the standard protection, in evaluation order.
var standard = []StandardRule{{
	Rule: agentprotocol.FirewallRule{
		ID: StandardRuleIDPrefix + "secrets", Name: "Secret files", Action: agentprotocol.FirewallBlock,
		Conditions: []agentprotocol.FirewallCondition{{Field: agentprotocol.FirewallFieldPath, Op: agentprotocol.FirewallOpRegex, Values: []string{secretFiles}}},
	},
	Description: "Requests for environment files, version control folders and credentials, which scanners probe on every public address.",
	Examples:    []string{"/.env", "/.env.production", "/.git/config", "/.svn/entries", "/.aws/credentials", "/.ssh/id_rsa", "/.npmrc", "/.DS_Store"},
}}

// StandardRules returns the standard protection NebulaCtrl applies to every
// HTTP service after the service's own rules. It cannot be switched off; an
// allow rule of the service lets a request through before it.
func StandardRules() []StandardRule {
	out := make([]StandardRule, len(standard))
	for i, s := range standard {
		s.Rule.Conditions = append([]agentprotocol.FirewallCondition(nil), s.Rule.Conditions...)
		s.Examples = append([]string(nil), s.Examples...)
		out[i] = s
	}
	return out
}

// IsStandardRuleID reports whether id is reserved for the standard protection.
func IsStandardRuleID(id string) bool { return strings.HasPrefix(id, StandardRuleIDPrefix) }

// DecodeFirewall reads a services.firewall value. It returns nil for NULL and
// for a document that does not decode (the column is only written through
// firewall.Service, which validates it).
func DecodeFirewall(raw json.RawMessage) *agentprotocol.Firewall {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var f agentprotocol.Firewall
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return &f
}

// RouteFirewall is the firewall an HTTP route carries: the service's active
// rules (services.firewall), then the standard protection.
func RouteFirewall(raw json.RawMessage) *agentprotocol.Firewall {
	active := DecodeFirewall(raw).Active()
	rules := make([]agentprotocol.FirewallRule, 0, len(active)+len(standard))
	rules = append(rules, active...)
	for _, s := range StandardRules() {
		rules = append(rules, s.Rule)
	}
	return &agentprotocol.Firewall{Rules: rules}
}

// FirewallOf is the route field of a service row: its active rules, then the
// standard protection (HTTP routes only).
func FirewallOf(s db.Service) *agentprotocol.Firewall { return RouteFirewall(s.Firewall) }
