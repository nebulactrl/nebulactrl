package engine

import (
	"context"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// LogsService serves the log history commands and receives the control plane's
// log configuration (retention limits, drains). *loghistory.Service implements it.
type LogsService interface {
	// HandleLogsCommand runs KindLogsQuery and KindLogsDrainTest.
	HandleLogsCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame
	// ApplyLogsConfig installs Reconcile.Logs; secrets is Reconcile.Secrets.
	ApplyLogsConfig(cfg *agentprotocol.LogsConfig, secrets map[string]string)
}

func (e *Engine) handleLogsCommand(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	if e.cfg.Logs == nil {
		return failedResult(cmd.ID, "log history is not available on this agent")
	}
	return e.cfg.Logs.HandleLogsCommand(ctx, cmd)
}

func (e *Engine) applyLogs(cfg *agentprotocol.LogsConfig, secrets map[string]string) {
	if cfg != nil && e.cfg.Logs != nil {
		e.cfg.Logs.ApplyLogsConfig(cfg, secrets)
	}
}
