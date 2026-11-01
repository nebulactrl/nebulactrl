package engine

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Planner reports how a git source would be built without building it.
// *build.Builder implements it; a Builder without it cannot preview plans.
type Planner interface {
	Plan(ctx context.Context, req build.Request, logf func(string)) (agentprotocol.BuildPlanResult, error)
}

// buildPlanTimeout bounds one preview (clone, plan container, version
// resolution). It is far below the build timeout: a preview is interactive.
const buildPlanTimeout = 5 * time.Minute

// handleBuildPlan clones the payload's commit and reports the build plan. It
// builds nothing and creates no deployment; the result's Data is a
// BuildPlanResult. Problems with the source itself (no Dockerfile, nothing
// Railpack recognises) are part of that result, so the command still succeeds.
func (e *Engine) handleBuildPlan(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.BuildPlanPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid build_plan payload: "+err.Error())
	}
	planner, ok := e.cfg.Builder.(Planner)
	if !ok || e.cfg.Builder == nil {
		return failedResult(cmd.ID, "this agent cannot build images (builder not configured)")
	}
	vars := build.Vars{Args: map[string]string{}, Secrets: map[string]string{}}
	masked := map[string]string{}
	for _, k := range p.BuildArgKeys {
		if v, ok := cmd.Secrets[agentprotocol.SecretBuildPrefix+k]; ok {
			vars.Args[k] = v
		}
	}
	for _, k := range p.BuildSecretKeys {
		if v, ok := cmd.Secrets[agentprotocol.SecretBuildPrefix+k]; ok {
			vars.Secrets[k] = v
			masked[k] = v
		}
	}
	req := build.Request{
		DeploymentID: cmd.ID, ServiceID: p.ServiceID, ServiceSlug: p.ServiceSlug,
		GitURL: p.GitURL, CommitSHA: p.CommitSHA,
		DockerfilePath: p.DockerfilePath, BuildContext: p.BuildContext, BuildType: p.BuildType,
		Submodules: p.Submodules,
		Vars:       vars,
	}
	if p.HasToken {
		req.Token = cmd.Secrets[agentprotocol.SecretGitToken]
		req.TokenUser = cmd.Secrets[agentprotocol.SecretGitUsername]
		masked[agentprotocol.SecretGitToken] = req.Token
	}
	if p.HasSSHKey {
		req.SSHKey = cmd.Secrets[agentprotocol.SecretGitSSHKey]
		req.KnownHosts = p.KnownHosts
		masked[agentprotocol.SecretGitSSHKey] = req.SSHKey
	}
	if err := req.Validate(); err != nil {
		return failedResult(cmd.ID, "invalid build_plan source: "+err.Error())
	}
	red := newRedactor(masked, nil)

	ctx, cancel := context.WithTimeout(ctx, buildPlanTimeout)
	defer cancel()
	res, err := planner.Plan(ctx, req, func(string) {})
	if err != nil {
		return failedResult(cmd.ID, red.apply(err.Error()))
	}
	res.Message = red.apply(res.Message)
	data, _ := json.Marshal(res)
	frame := agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
	return frame
}

// isShellEntrypoint reports whether an image entrypoint is `<shell> -c`, the
// form Railpack images use: the container's command is then one script string.
func isShellEntrypoint(ep []string) bool {
	if len(ep) != 2 || ep[1] != "-c" {
		return false
	}
	switch path.Base(ep[0]) {
	case "sh", "bash", "ash", "dash":
		return true
	}
	return false
}

// shellQuoteWord single-quotes s for a POSIX shell unless it is plainly safe.
func shellQuoteWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// startCommand is the container Cmd for the service's StartCommand. For an
// image whose entrypoint is `<shell> -c` (Railpack) an argv such as
// ["node","server.js"] would run only `node`, so it is joined into one quoted
// script. A single-element command is already a script and is kept as is.
func (d *deploy) startCommand() []string {
	cmd := d.p.StartCommand
	if len(cmd) <= 1 || (d.p.Docker != nil && len(d.p.Docker.Entrypoint) > 0) {
		// An entrypoint override replaces the image's shell entrypoint, so
		// the command is plain argv for the override.
		return cmd
	}
	if d.shellEntry == nil {
		v := false
		if d.imageID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if info, err := d.e.cfg.Docker.ImageInspect(ctx, d.imageID); err == nil {
				v = isShellEntrypoint(info.Entrypoint)
			}
			cancel()
		}
		d.shellEntry = &v
	}
	if !*d.shellEntry {
		return cmd
	}
	words := make([]string, len(cmd))
	for i, w := range cmd {
		words[i] = shellQuoteWord(w)
	}
	return []string{strings.Join(words, " ")}
}

// buildMethodLabel names the build for deploy events.
func buildMethodLabel(i agentprotocol.BuildInfo) string { return build.Describe(i) }
