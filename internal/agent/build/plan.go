package build

import (
	"context"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Plan clones req's commit and reports how it would be built, without building
// it: which method applies and why, and for Railpack the detected providers,
// resolved packages, steps and start command. Problems that the user can fix
// (no Dockerfile with build type "dockerfile", nothing Railpack recognises) are
// reported in the result, not as an error; an error means the plan could not
// be attempted at all (clone failed, workspace unusable).
func (b *Builder) Plan(ctx context.Context, req Request, logf func(string)) (agentprotocol.BuildPlanResult, error) {
	req.applyDefaults()
	if err := req.Validate(); err != nil {
		return agentprotocol.BuildPlanResult{}, err
	}
	if req.BuildType == "" {
		req.BuildType = agentprotocol.BuildAuto
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res := agentprotocol.BuildPlanResult{
		Outcome: agentprotocol.OutcomeSucceeded, CommitSHA: req.CommitSHA,
		Requested: req.BuildType, DockerfilePath: req.DockerfilePath,
	}
	dir, repoDir, ctxDir, cleanup, err := b.checkout(ctx, req, "plan-"+req.DeploymentID, logf)
	if err != nil {
		return res, err
	}
	defer cleanup()
	res.SuggestedStarter = DetectStarter(ctxDir)

	dec, err := Decide(req.BuildType, repoDir, req.DockerfilePath)
	res.DockerfileFound = dec.DockerfileFound
	fail := func(info *agentprotocol.BuildInfo, msg string) (agentprotocol.BuildPlanResult, error) {
		res.Outcome, res.Message, res.Info = agentprotocol.OutcomeFailed, msg, info
		return res, nil
	}
	if err != nil {
		return fail(nil, err.Error())
	}
	info := dec.BuildInfo
	switch dec.Method {
	case agentprotocol.MethodStarter:
		if _, err := StarterDockerfile(dec.Starter, ctxDir); err != nil {
			return fail(&info, err.Error())
		}
	case agentprotocol.MethodRailpack:
		// Plan with the stored variables so RAILPACK_* settings (start command,
		// package versions) show up in the preview.
		plan, err := b.railpackPrepare(ctx, "nc-railpack-plan-"+agentprotocol.Short(req.DeploymentID), ctxDir, dir, allVars(req.Vars), logf)
		if err != nil {
			return fail(&info, err.Error())
		}
		plan.Info.Reason = info.Reason
		info = plan.Info
		res.Messages = plan.Messages
	}
	res.Info = &info
	return res, nil
}
