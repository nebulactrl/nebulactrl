package deployments

import (
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

// networkProblems lists the shared-network settings that block a deployment:
// attachments to a network on another server, and attachments that outbound
// blocking would silently void.
func (in *inputs) networkProblems() []PlanBlocking {
	var out []PlanBlocking
	if in.sc.Service.Kind == services.KindRoute {
		return out
	}
	if in.sc.Service.ServerID != nil {
		for _, n := range in.networks {
			if n.ServerID != *in.sc.Service.ServerID {
				out = append(out, PlanBlocking{
					Code:    "network_wrong_server",
					Message: fmt.Sprintf("The shared network %q is on another server than %s. Detach it or move the service back.", n.Name, in.sc.Service.Name),
				})
				break
			}
		}
	}
	if in.cfg.BlockEgress && len(in.networks) > 0 {
		out = append(out, PlanBlocking{Code: "egress_blocked", Message: services.NetworkConflict().Message})
	}
	return out
}

// egressWarnings explains what blocking outbound internet does to this
// deployment: variables that look like internet calls, and the other services
// of the environment that only reach the service after their next deploy.
func (in *inputs) egressWarnings(changes []services.FieldChange) []string {
	var out []string
	if !in.cfg.BlockEgress || in.cfg.Route != nil {
		return out
	}
	if len(in.egressHints) > 0 {
		out = append(out, fmt.Sprintf("Outbound internet is blocked, but %s look like they call out to the internet; those calls will fail. Allow outbound internet if this is intended.", strings.Join(in.egressHints, ", ")))
	}
	if in.otherDeployed > 0 {
		for _, c := range changes {
			if c.Field == "config.blockEgress" || c.Field == "deployment" {
				out = append(out, "Other services in this environment are attached to the internal network automatically once this deployment succeeds, so they can reach this service; they are not redeployed.")
				break
			}
		}
	}
	return out
}
