package deployments

import (
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// buildAgentSupports reports whether the server's agent advertised
// agentprotocol.CapBuildOptions (no-cache builds and submodule checkout). A
// server that has never connected has no facts yet and counts as current.
func buildAgentSupports(srv db.Server) bool {
	has, known := services.ServerHasCapability(srv, agentprotocol.CapBuildOptions)
	return has || !known
}

func noCacheAgentError(server string) error {
	return apierr.Unprocessable("agent_too_old",
		fmt.Sprintf("The agent on %s is too old to deploy without cache (it would build with the cache and say nothing); update the agent first.", server))
}

// submodulesNeedAgent is the problem of a service that requires submodules
// ("on") on a server whose agent would silently skip them. Mode auto is not
// blocked: on an old agent it behaves as before submodule support.
func submodulesNeedAgent(cfg services.Config, srv *db.Server) *PlanBlocking {
	if cfg.Submodules != agentprotocol.SubmodulesOn || srv == nil || buildAgentSupports(*srv) {
		return nil
	}
	return &PlanBlocking{
		Code:    "agent_too_old",
		Message: "The agent on " + srv.Name + " is too old to check out git submodules; update the agent first, or set submodules to auto or off.",
	}
}
