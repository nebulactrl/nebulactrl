package deployments

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Guard operation names (prodguard approvals are bound to them).
const (
	guardDeploy       = "deploy"
	guardDeployStaged = "deploy_staged"
	guardRollback     = "rollback"
)

// strategyEffect says what a strategy means for traffic.
func strategyEffect(strategy string, reasons []string) string {
	if strategy == agentprotocol.StrategyRolling {
		return "Rolling update: the new container must pass its health check before the old one stops."
	}
	if strategy == StrategyRouteOnly {
		return "Route service: the proxy route table is updated; nothing is built or started."
	}
	s := "Recreate: the running container stops before the new one starts, so expect brief downtime"
	if len(reasons) > 0 {
		s += " (" + strings.Join(reasons, "; ") + ")"
	}
	return s + "."
}

// deployGuard is the guard scope of deploying one service's current state.
func deployGuard(in *inputs, commitSHA string) prodguard.ProdScope {
	sc := in.sc
	strategy, reasons := ChoosePlanStrategy(in.cfg, len(in.volumes))
	var eff []string
	switch {
	case in.cfg.Stack != nil:
		eff = append(eff, "Redeploys the Compose stack of this service.")
	case sc.Service.SourceType == services.SourceGit:
		src := "Builds branch " + in.cfg.GitBranch + " of " + in.cfg.GitURL
		if commitSHA != "" {
			src = "Builds commit " + commitSHA[:7] + " of " + in.cfg.GitURL
		}
		eff = append(eff, src+" and starts the result.")
	case in.cfg.Image != "":
		eff = append(eff, "Pulls "+in.cfg.Image+" and starts it.")
	}
	if sc.Service.Generation > sc.Service.AppliedGeneration {
		eff = append(eff, "Applies the changes staged since the last deployment (configuration, variables, domains, volumes).")
	}
	eff = append(eff, strategyEffect(strategy, reasons))
	if in.server != nil {
		eff = append(eff, "Target server: "+in.server.Name+".")
	}
	return sc.ProdScope(fmt.Sprintf("Deploy %s to production (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false, eff...)
}

// stagedGuard is the guard scope of deploying a batch of services of one
// environment. Every service is a target with its generation.
func stagedGuard(env services.EnvScope, targets []stagedTarget) prodguard.ProdScope {
	pt := make([]prodguard.ApprovalTarget, len(targets))
	names := make([]string, len(targets))
	eff := make([]string, 0, len(targets)+1)
	for i, t := range targets {
		pt[i] = prodguard.ApprovalTarget{Kind: "service", ID: t.sc.Service.ID, Name: t.sc.Service.Name, Generation: t.sc.Service.Generation}
		names[i] = t.sc.Service.Name
		strategy, reasons := ChoosePlanStrategy(t.in.cfg, len(t.in.volumes))
		eff = append(eff, t.sc.Service.Name+": "+strategyEffect(strategy, reasons))
		if buildsFromGit(t.sc, t.in.cfg) {
			// A service moved from an image to a repository builds before it starts.
			eff = append(eff, t.sc.Service.Name+": builds branch "+t.in.cfg.GitBranch+" of "+services.RepoLabel(t.in.cfg.GitURL)+" first.")
		}
	}
	eff = append(eff, "Services start in reference order; databases first.")
	return env.ProdScope(
		fmt.Sprintf("Deploy %d staged service(s) to production: %s", len(targets), strings.Join(names, ", ")),
		false, pt, eff...)
}

type stagedTarget struct {
	sc services.Scope
	in *inputs
}

func stagedDigest(envID uuid.UUID, ids []uuid.UUID, message string) []byte {
	return prodguard.DigestIDs(guardDeployStaged, ids, envID, message)
}
