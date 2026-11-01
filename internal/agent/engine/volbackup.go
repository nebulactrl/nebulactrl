package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/volbackup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Volume backups (see package volbackup). The engine contributes its command
// lock, its view of a service's container and the container sweep.

// BackupSweepInterval is how often leftovers of dead volume backups (helpers,
// staging volumes, a paused or stopped service container) are cleaned up.
const BackupSweepInterval = 2 * time.Minute

// volState lazily holds the Engine's volume backup runner: one per Engine, so
// the runner's registry of in-flight operations covers every command.
type volState struct {
	once   sync.Once
	runner *volbackup.Runner
}

// volumes returns the Engine's volume backup runner.
func (e *Engine) volumes() *volbackup.Runner {
	e.vol.once.Do(func() {
		e.vol.runner = &volbackup.Runner{Docker: e.cfg.Docker, Host: volHost{e}, BackupDir: e.cfg.BackupDir, Log: e.log}
	})
	return e.vol.runner
}

// volHost adapts the Engine to volbackup.Host.
type volHost struct{ e *Engine }

// LockService implements volbackup.Host.
func (h volHost) LockService(ctx context.Context, serviceID string, wait time.Duration) (func(), error) {
	got := make(chan func(), 1)
	go func() { got <- h.e.lockService(serviceID) }()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case unlock := <-got:
		return unlock, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	// The goroutine still waits for the lock: release it as soon as it arrives.
	go func() { (<-got)() }()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, volbackup.ErrBusy
}

// ServiceContainer implements volbackup.Host: the container of the service's
// current deployment (running one first).
func (h volHost) ServiceContainer(ctx context.Context, serviceID string) (docker.ContainerInfo, bool) {
	var first *docker.ContainerInfo
	for _, c := range h.e.currentReplicas(ctx, serviceID) {
		if c.Labels[agentprotocol.LabelManaged] != "true" || c.Labels[agentprotocol.LabelService] != serviceID {
			continue
		}
		c := c
		if c.Running {
			return c, true
		}
		if first == nil {
			first = &c
		}
	}
	if first != nil {
		return *first, true
	}
	return docker.ContainerInfo{}, false
}

// SelfContainer implements volbackup.Host.
func (h volHost) SelfContainer(ctx context.Context) string { return h.e.selfContainer(ctx) }

// sweepBackups cleans up after volume backups that died.
func (e *Engine) sweepBackups(ctx context.Context) error {
	return e.volumes().Sweep(ctx, e.isInflight)
}

func volFrame(cmdID, outcome, message string, data any) agentprotocol.ResultFrame {
	f := agentprotocol.ResultFrame{CommandID: cmdID, Outcome: agentprotocol.OutcomeSucceeded, Data: agentprotocol.MarshalResultData(data)}
	if outcome != agentprotocol.OutcomeSucceeded {
		f.Outcome, f.Error = agentprotocol.OutcomeFailed, message
	}
	return f
}

func (e *Engine) handleVolumeBackup(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.VolumeBackupPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid volume_backup payload: "+err.Error())
	}
	e.log.Info("backing up volumes", "service_id", p.ServiceID, "backup_id", p.BackupID, "mode", p.Mode)
	res, _ := e.volumes().Backup(ctx, p, cmd.Secrets, func(line string) { e.log.Info("volume backup", "backup_id", p.BackupID, "msg", line) })
	return volFrame(cmd.ID, res.Outcome, res.Message, res)
}

func (e *Engine) handleVolumeForget(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.VolumeForgetPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid volume_forget payload: "+err.Error())
	}
	res, _ := e.volumes().Forget(ctx, p, cmd.Secrets, func(line string) { e.log.Info("volume forget", "service_id", p.ServiceID, "msg", line) })
	return volFrame(cmd.ID, res.Outcome, res.Message, res)
}

func (e *Engine) handleVolumeRestore(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.VolumeRestorePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid volume_restore payload: "+err.Error())
	}
	e.log.Info("restoring a volume", "service_id", p.ServiceID, "restore_id", p.RestoreID, "in_place", p.InPlace)
	res, _ := e.volumes().Restore(ctx, p, cmd.Secrets, func(line string) { e.log.Info("volume restore", "restore_id", p.RestoreID, "msg", line) })
	return volFrame(cmd.ID, res.Outcome, res.Message, res)
}

func (e *Engine) handleVolumeRemove(ctx context.Context, cmd agentprotocol.Command) agentprotocol.ResultFrame {
	var p agentprotocol.VolumeRemovePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return failedResult(cmd.ID, "invalid volume_remove payload: "+err.Error())
	}
	res, _ := e.volumes().RemoveVolumes(ctx, p)
	return volFrame(cmd.ID, res.Outcome, res.Message, res)
}

// ---- backup file browser (interactive stream) ----

// startVolFile runs one backup file operation on its own goroutine, like a
// container file operation: abortable by the control plane through ExecClose.
func (e *Engine) startVolFile(req agentprotocol.VolFileRequest) {
	ctx, cancel := context.WithCancel(context.Background())
	e.term.mu.Lock()
	if e.term.cancels == nil {
		e.term.cancels = map[string]context.CancelFunc{}
	}
	e.term.cancels[req.RequestID] = cancel
	e.term.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.term.mu.Lock()
			delete(e.term.cancels, req.RequestID)
			e.term.mu.Unlock()
		}()
		e.handleVolFile(ctx, req)
	}()
}

func (e *Engine) handleVolFile(ctx context.Context, req agentprotocol.VolFileRequest) {
	send := func(typ string, body any) bool { return e.stream.Stream(typ, req.RequestID, body) }
	respond := func(r agentprotocol.FSResponse) {
		r.RequestID = req.RequestID
		send(agentprotocol.TypeFSResponse, r)
	}
	fail := func(err error) {
		var fe *volbackup.FileError
		if errors.As(err, &fe) {
			respond(agentprotocol.FSResponse{Code: fe.Code, Error: fe.Msg})
			return
		}
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrFailed, Error: err.Error()})
	}
	logf := func(line string) { e.log.Debug("backup files", "request", req.RequestID, "msg", line) }
	run := e.volumes()
	switch req.Op {
	case agentprotocol.VolFileList:
		entries, truncated, err := run.ListFiles(ctx, req, logf)
		if err != nil {
			fail(err)
			return
		}
		p, _ := agentprotocol.CleanVolumePath(req.Path)
		respond(agentprotocol.FSResponse{Stage: agentprotocol.FSDone, Path: p, Entries: entries, Truncated: truncated})
	case agentprotocol.VolFileRead, agentprotocol.VolFileArchive:
		entry, rc, err := run.OpenFile(ctx, req, logf)
		if err != nil {
			fail(err)
			return
		}
		defer rc.Close()
		e.pumpVolFile(ctx, req, entry, rc, send, respond)
	default:
		respond(agentprotocol.FSResponse{Code: agentprotocol.FSErrFailed, Error: "unknown backup file operation " + req.Op})
	}
}

// pumpVolFile streams a file or archive to the control plane under the stream
// window (credits come back as stream_ack frames).
func (e *Engine) pumpVolFile(ctx context.Context, req agentprotocol.VolFileRequest, entry agentprotocol.FSEntry, rc io.Reader,
	send func(string, any) bool, respond func(agentprotocol.FSResponse)) {
	gate := newCreditGate()
	e.term.mu.Lock()
	if e.term.gates == nil {
		e.term.gates = map[string]*creditGate{}
	}
	e.term.gates[req.RequestID] = gate
	e.term.mu.Unlock()
	defer func() {
		gate.Close()
		e.term.mu.Lock()
		delete(e.term.gates, req.RequestID)
		e.term.mu.Unlock()
	}()

	p, _ := agentprotocol.CleanVolumePath(req.Path)
	respond(agentprotocol.FSResponse{Stage: agentprotocol.FSReady, Path: p, Entry: &entry})
	limit := agentprotocol.MaxFileReadBytes
	buf := make([]byte, agentprotocol.FSChunk)
	var sent int64
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			sent += int64(n)
			if sent > int64(limit) {
				send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Error: "the download exceeds the size limit"})
				return
			}
			if gate.Acquire(ctx, n) != nil {
				return
			}
			if !send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Data: append([]byte(nil), buf[:n]...)}) {
				return
			}
		}
		if errors.Is(rerr, io.EOF) {
			send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, EOF: true})
			return
		}
		if rerr != nil {
			send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: req.RequestID, Error: "read backup: " + rerr.Error()})
			return
		}
	}
}
