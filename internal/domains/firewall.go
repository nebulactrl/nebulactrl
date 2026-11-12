package domains

import (
	"encoding/json"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

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

// firewallFrom is the route field of a services.firewall value: nil when no
// rule is active, so a service with only disabled rules routes exactly like
// one without a firewall.
func firewallFrom(raw json.RawMessage) *agentprotocol.Firewall {
	f := DecodeFirewall(raw)
	active := f.Active()
	if len(active) == 0 {
		return nil
	}
	return &agentprotocol.Firewall{Rules: active}
}

// FirewallOf is the route field of a service row (nil without active rules).
func FirewallOf(s db.Service) *agentprotocol.Firewall { return firewallFrom(s.Firewall) }
