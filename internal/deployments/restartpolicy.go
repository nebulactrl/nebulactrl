package deployments

import (
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// restartPolicyNeedsAgent is the blocker of a service with a restart policy
// other than the default on a server whose agent does not apply it (it would
// silently keep restarting the container). "always" is today's behaviour on
// every agent. A server that never connected has no facts and is assumed to
// enroll with a current agent.
func restartPolicyNeedsAgent(cfg services.Config, srv *db.Server) *PlanBlocking {
	if srv == nil || cfg.RestartPolicy == "" || cfg.RestartPolicy == services.RestartAlways {
		return nil
	}
	if has, known := services.ServerHasCapability(*srv, agentprotocol.CapRestartPolicy); known && !has {
		return &PlanBlocking{
			Code: "agent_too_old",
			Message: fmt.Sprintf("Update the agent on %s to use the %s restart policy. No changes were made.",
				srv.Name, cfg.RestartPolicy),
		}
	}
	return nil
}

// checkRestartPolicyDeploy refuses, before anything is persisted, to deploy a
// restart policy to an agent that cannot apply it.
func checkRestartPolicyDeploy(cfg services.Config, srv db.Server) error {
	if b := restartPolicyNeedsAgent(cfg, &srv); b != nil {
		return userErrorf("%s", b.Message)
	}
	return nil
}
