package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (e *Engine) backupRunner() *backup.Runner {
	return &backup.Runner{Docker: e.cfg.Docker, Stores: backup.NewStoreFactory(e.cfg.BackupDir)}
}

// databaseContainer returns the id of a running container of the database service.
func (e *Engine) databaseContainer(ctx context.Context, serviceID string) (string, error) {
	for _, c := range e.currentReplicas(ctx, serviceID) {
		if c.Running {
			return c.ID, nil
		}
	}
	return "", errors.New("the database is not running on this host")
}

func backupFailed(id string, data any, msg string) agentprotocol.ResultFrame {
	return agentprotocol.ResultFrame{
		CommandID: id, Outcome: agentprotocol.OutcomeFailed, Error: msg, Data: agentprotocol.MarshalResultData(data),
	}
}

// handleDBBackup runs a streaming dump. It deliberately takes no service lock:
// a long backup must not block deploys (a deploy that recreates the database
// fails the dump, which is reported as such).
func (e *Engine) handleDBBackup(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DBBackupPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid db_backup payload: "+err.Error())
	}
	fail := func(msg string) agentprotocol.ResultFrame {
		return backupFailed(cmd.ID, agentprotocol.DBBackupResult{Outcome: agentprotocol.OutcomeFailed, Message: msg}, msg)
	}
	cid, err := e.databaseContainer(ctx, p.Target.ServiceID)
	if err != nil {
		return fail(err.Error())
	}
	e.log.Info("backing up database", "service_id", p.Target.ServiceID, "backup_id", p.BackupID, "engine", p.Target.Engine)
	res, err := e.backupRunner().Backup(ctx, cid, p, cmd.Secrets, func(line string) { e.log.Info("backup", "backup_id", p.BackupID, "msg", line) })
	if err != nil {
		return backupFailed(cmd.ID, res, res.Message)
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: agentprotocol.MarshalResultData(res)}
}

func (e *Engine) handleDBRestore(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.DBRestorePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid db_restore payload: "+err.Error())
	}
	fail := func(msg string) agentprotocol.ResultFrame {
		return backupFailed(cmd.ID, agentprotocol.DBRestoreResult{Outcome: agentprotocol.OutcomeFailed, Message: msg}, msg)
	}
	cid, err := e.databaseContainer(ctx, p.Target.ServiceID)
	if err != nil {
		return fail(err.Error())
	}
	e.log.Info("restoring database", "service_id", p.Target.ServiceID, "restore_id", p.RestoreID, "engine", p.Target.Engine)
	res, err := e.backupRunner().Restore(ctx, cid, p, cmd.Secrets, func(line string) { e.log.Info("restore", "restore_id", p.RestoreID, "msg", line) })
	if err != nil {
		return backupFailed(cmd.ID, res, res.Message)
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: agentprotocol.MarshalResultData(res)}
}

func (e *Engine) handleBackupDelete(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.BackupDeletePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid backup_delete payload: "+err.Error())
	}
	res, err := e.backupRunner().Delete(ctx, p, cmd.Secrets)
	if err != nil {
		return backupFailed(cmd.ID, res, res.Message)
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: agentprotocol.MarshalResultData(res)}
}

func (e *Engine) handleBackupTest(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.BackupTestPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid backup_test payload: "+err.Error())
	}
	res, err := e.backupRunner().Test(ctx, p, cmd.Secrets)
	if err != nil {
		return backupFailed(cmd.ID, res, fmt.Sprint(res.Message))
	}
	return agentprotocol.ResultFrame{CommandID: cmd.ID, Outcome: agentprotocol.OutcomeSucceeded, Data: agentprotocol.MarshalResultData(res)}
}
