package domains

import (
	"encoding/json"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DecodeAutoBan reads a services.autoban value. It returns nil for NULL and
// for a document that does not decode (the column is only written through
// firewall.Service, which validates it).
func DecodeAutoBan(raw json.RawMessage) *agentprotocol.AutoBan {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var a agentprotocol.AutoBan
	if json.Unmarshal(raw, &a) != nil {
		return nil
	}
	return &a
}

// RouteAutoBan is the auto-ban policy an HTTP route carries: the stored policy
// when it is enabled, nil otherwise (a route without a policy bans nobody and
// stays on the proxy's compiled firewall unless its rules need the guard).
func RouteAutoBan(raw json.RawMessage) *agentprotocol.AutoBan {
	a := DecodeAutoBan(raw)
	if a == nil || !a.Enabled {
		return nil
	}
	return a
}

// AutoBanOf is the route field of a service row (HTTP routes only).
func AutoBanOf(s db.Service) *agentprotocol.AutoBan { return RouteAutoBan(s.Autoban) }

// Default policy values of a service that has none (and of a nebula.toml
// table that leaves a key out).
const (
	DefaultAutoBanThreshold = 10
	DefaultAutoBanWindow    = 600
	DefaultAutoBanDuration  = 3600
)

// DefaultAutoBan is the policy of a service that has none: off, with the
// default limits, so switching it on needs one toggle.
func DefaultAutoBan() agentprotocol.AutoBan {
	return agentprotocol.AutoBan{
		Threshold: DefaultAutoBanThreshold, WindowSeconds: DefaultAutoBanWindow, DurationSeconds: DefaultAutoBanDuration,
		Allow: []string{},
	}
}

// StoredAutoBan is the policy as the services.autoban column holds it,
// the default (off) policy for NULL.
func StoredAutoBan(raw json.RawMessage) agentprotocol.AutoBan {
	if a := DecodeAutoBan(raw); a != nil {
		if a.Allow == nil {
			a.Allow = []string{}
		}
		return *a
	}
	return DefaultAutoBan()
}

// AutoBanIsDefaultOff reports whether a is the policy of a service without
// one: such a policy is stored as NULL.
func AutoBanIsDefaultOff(a agentprotocol.AutoBan) bool {
	d := DefaultAutoBan()
	return !a.Enabled && a.Threshold == d.Threshold && a.WindowSeconds == d.WindowSeconds && a.DurationSeconds == d.DurationSeconds && len(a.Allow) == 0
}
