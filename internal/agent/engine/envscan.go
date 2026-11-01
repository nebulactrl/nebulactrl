package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// EnvScanner reads the template env files and config files of a git source
// without building it. *build.Builder implements it.
type EnvScanner interface {
	ScanEnv(ctx context.Context, req build.EnvScanRequest) (build.EnvScanOutcome, error)
}

// envScanTimeout bounds one scan: it is interactive (a create dialog waits for it).
const envScanTimeout = 2 * time.Minute

// handleEnvScan fetches the payload's branch head and reports its template env
// files and the requested config files. It builds nothing and creates no
// deployment; the result's Data is an EnvScanResult.
func (e *Engine) handleEnvScan(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.EnvScanPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid env_scan payload: "+err.Error())
	}
	scanner, ok := e.cfg.Builder.(EnvScanner)
	if !ok || e.cfg.Builder == nil {
		return failedResult(cmd.ID, "this agent cannot read git sources (builder not configured)")
	}
	if len(p.ConfigPaths) > agentprotocol.ScanMaxConfigPaths {
		return failedResult(cmd.ID, "too many config paths in env_scan payload")
	}
	for _, cp := range p.ConfigPaths {
		if !agentprotocol.ValidConfigPath(cp) {
			return failedResult(cmd.ID, "invalid config path in env_scan payload")
		}
	}
	req := build.EnvScanRequest{
		GitURL: p.GitURL, Branch: p.Branch, BuildContext: p.BuildContext, ConfigPaths: p.ConfigPaths, ConfigOnly: p.ConfigOnly,
	}
	masked := map[string]string{}
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
	red := newRedactor(masked, nil)

	ctx, cancel := context.WithTimeout(ctx, envScanTimeout)
	defer cancel()
	out, err := scanner.ScanEnv(ctx, req)
	if err != nil {
		return failedResult(cmd.ID, red.apply(err.Error()))
	}
	data, _ := json.Marshal(agentprotocol.EnvScanResult{
		Outcome: agentprotocol.OutcomeSucceeded, Commit: out.Commit, Files: out.Files, ConfigFiles: out.ConfigFiles,
	})
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: data}
}
