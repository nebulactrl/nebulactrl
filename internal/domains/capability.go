package domains

import (
	"encoding/json"
	"slices"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// SupportsProxyV2 reports whether the server's agent advertised
// agentprotocol.CapProxyV2. A server that has never connected has no facts yet
// and is assumed to enroll with a current agent.
func SupportsProxyV2(srv db.Server) bool {
	if len(srv.Facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapProxyV2)
}

// RequireProxyV2 returns 422 agent_too_old when the server's agent cannot
// render proxy v2 features: an old agent would silently drop the new fields.
func RequireProxyV2(srv *db.Server) error {
	if srv == nil || SupportsProxyV2(*srv) {
		return nil
	}
	return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old for this proxy feature; update the agent first")
}
