package backups

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Browsing a backup is an interactive stream, not a command: the control plane
// opens a stream to the agent, sends one volfile_request (the only frame that
// carries secrets) and relays the fs_response / fs_data frames. Nothing is
// persisted.

// volumeFSTimeout bounds the wait for the agent's next frame: restic may need a
// while to open a remote repository.
const volumeFSTimeout = 90 * time.Second

// Stream is one open exchange with an agent. *agents.Stream implements it.
type Stream interface {
	Frames() <-chan agentprotocol.Frame
	Done() <-chan struct{}
	Send(typ string, body any) error
	Close()
}

// Streams opens interactive streams to connected agents (the file browser of a
// backup is one).
type Streams interface {
	// OpenStream registers stream id on the server's connection; it fails with
	// agents.ErrNotConnected while the server is offline.
	OpenStream(serverID uuid.UUID, id string) (Stream, error)
}

// HubStreams adapts the agent hub to Streams.
type HubStreams struct{ Hub *agents.Hub }

// OpenStream implements Streams.
func (h HubStreams) OpenStream(serverID uuid.UUID, id string) (Stream, error) {
	s, err := h.Hub.OpenStream(serverID, id)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// errVolumeAbort means the response already started: the handler aborts the
// connection so the client sees a truncated download as an error.
var errVolumeAbort = errors.New("transfer aborted")

func offline() error {
	return apierr.Conflict("server_offline", "the server is offline")
}

// volFSError converts an agent's file error into an API error.
func volFSError(resp agentprotocol.FSResponse) error {
	msg := resp.Error
	if msg == "" {
		msg = "file operation failed"
	}
	switch resp.Code {
	case agentprotocol.FSErrNotFound:
		return apierr.NotFound("path")
	case agentprotocol.FSErrNotDir:
		return apierr.Unprocessable("not_a_directory", msg)
	case agentprotocol.FSErrIsDir:
		return apierr.Unprocessable("is_a_directory", msg)
	case agentprotocol.FSErrTooLarge:
		return apierr.New(http.StatusRequestEntityTooLarge, "file_too_large", msg)
	case agentprotocol.FSErrPermission:
		return apierr.Unprocessable("file_permission_denied", msg)
	}
	return apierr.Unprocessable("file_operation_failed", msg)
}

// volumeExchange is one file operation in flight: the stream and its id.
type volumeExchange struct {
	id string
	st Stream
}

// close ends the exchange. Telling the agent first makes it stop a transfer
// that is still running (a no-op for one that finished).
func (ex *volumeExchange) close() {
	_ = ex.st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: ex.id})
	ex.st.Close()
}

// fileTarget is a backup prepared for a file operation.
type fileTarget struct {
	b      db.VolumeBackup
	sc     services.Scope
	server uuid.UUID
	repo   agentprotocol.VolumeRepo
	secret map[string]string
	volume string
	path   string
}

// prepareFiles authorizes and validates a file operation on a backup.
func (s *Service) prepareFiles(ctx context.Context, backupID uuid.UUID, volume, p string) (fileTarget, error) {
	// Backups can hold secrets: browsing needs the same right as the container file browser.
	b, sc, err := s.volumeBackupScope(ctx, policy.ScopeDeploymentsWrite, backupID)
	if err != nil {
		return fileTarget{}, err
	}
	if b.Status != StatusSucceeded || b.ObjectDeletedAt != nil || b.SnapshotID == "" {
		return fileTarget{}, apierr.Conflict("backup_not_restorable", "only a successful backup whose snapshot still exists can be browsed")
	}
	if !contains(b.Volumes, volume) {
		return fileTarget{}, apierr.Unprocessable("validation_failed", "the backup does not contain the volume "+volume)
	}
	clean, err := agentprotocol.CleanVolumePath(p)
	if err != nil {
		return fileTarget{}, apierr.Unprocessable("invalid_path", err.Error())
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return fileTarget{}, fmt.Errorf("load destination: %w", err)
	}
	repo, err := s.q.GetVolumeRepo(ctx, db.GetVolumeRepoParams{ServiceID: b.ServiceID, DestinationID: b.DestinationID})
	if err != nil {
		return fileTarget{}, fmt.Errorf("load repository: %w", err)
	}
	// A local destination is only readable on the server it was written on; an
	// S3 repository can be read from the server that runs the service now.
	var server *uuid.UUID
	if dest.Kind == agentprotocol.DestLocal {
		server = b.ServerID
	} else if server = sc.Service.ServerID; server == nil {
		server = b.ServerID
	}
	if server == nil {
		return fileTarget{}, offline()
	}
	secrets, err := s.repoSecrets(ctx, s.q, b.ServiceID, b.DestinationID)
	if err != nil {
		return fileTarget{}, err
	}
	agentRepo, err := s.volumeRepo(ctx, dest, repo.RepoPath)
	if err != nil {
		return fileTarget{}, err
	}
	return fileTarget{b: b, sc: sc, server: *server, repo: agentRepo, secret: secrets, volume: volume, path: clean}, nil
}

// beginFiles opens a stream, sends the request and waits for the agent's first
// response. A response carrying an error is returned as an API error.
func (s *Service) beginFiles(ctx context.Context, t fileTarget, op string) (*volumeExchange, agentprotocol.FSResponse, error) {
	if s.streams == nil {
		return nil, agentprotocol.FSResponse{}, offline()
	}
	id := uuid.NewString()
	st, err := s.streams.OpenStream(t.server, id)
	if err != nil {
		return nil, agentprotocol.FSResponse{}, offline()
	}
	req := agentprotocol.VolFileRequest{
		RequestID: id, Op: op, ServiceID: t.b.ServiceID.String(), Repo: t.repo, SnapshotID: t.b.SnapshotID,
		Volume: t.volume, Path: t.path, Secrets: t.secret,
	}
	if err := st.Send(agentprotocol.TypeVolFileRequest, req); err != nil {
		st.Close()
		return nil, agentprotocol.FSResponse{}, offline()
	}
	resp, err := s.nextVolumeResponse(ctx, st)
	if err != nil {
		st.Close()
		return nil, agentprotocol.FSResponse{}, err
	}
	if resp.Error != "" {
		st.Close()
		return nil, resp, volFSError(resp)
	}
	return &volumeExchange{id: id, st: st}, resp, nil
}

// nextVolumeResponse waits for the next fs_response on the stream.
func (s *Service) nextVolumeResponse(ctx context.Context, st Stream) (agentprotocol.FSResponse, error) {
	timer := time.NewTimer(s.fsTimeout())
	defer timer.Stop()
	for {
		select {
		case f := <-st.Frames():
			if f.Type != agentprotocol.TypeFSResponse {
				continue
			}
			var r agentprotocol.FSResponse
			if err := f.Into(&r); err != nil {
				return r, apierr.Internal()
			}
			return r, nil
		case <-st.Done():
			return agentprotocol.FSResponse{}, apierr.Conflict("server_offline", "the server went offline during the operation")
		case <-timer.C:
			return agentprotocol.FSResponse{}, apierr.New(http.StatusGatewayTimeout, "agent_timeout", "the server did not answer in time")
		case <-ctx.Done():
			return agentprotocol.FSResponse{}, ctx.Err()
		}
	}
}

func (s *Service) fsTimeout() time.Duration {
	if s.fsWait > 0 {
		return s.fsWait
	}
	return volumeFSTimeout
}

// ListVolumeBackupFiles lists one directory of a volume inside a backup.
func (s *Service) ListVolumeBackupFiles(ctx context.Context, backupID uuid.UUID, volume, dir string) (VolumeFileListing, error) {
	t, err := s.prepareFiles(ctx, backupID, volume, dir)
	if err != nil {
		return VolumeFileListing{}, err
	}
	ex, resp, err := s.beginFiles(ctx, t, agentprotocol.VolFileList)
	if err != nil {
		return VolumeFileListing{}, err
	}
	defer ex.close()
	out := VolumeFileListing{
		BackupID: backupID, Volume: volume, Path: resp.Path, Entries: make([]VolumeFileEntry, 0, len(resp.Entries)), Truncated: resp.Truncated,
	}
	if out.Path == "" {
		out.Path = t.path
	}
	for _, e := range resp.Entries {
		fe := VolumeFileEntry{Name: e.Name, Type: e.Type, Size: e.Size, Mode: e.Mode}
		if e.MTime > 0 {
			ts := time.Unix(e.MTime, 0).UTC()
			fe.ModifiedAt = &ts
		}
		out.Entries = append(out.Entries, fe)
	}
	return out, nil
}

// downloadName is the file name offered to the browser.
func downloadName(volume, p string, archive bool) string {
	name := path.Base(p)
	if p == "/" || name == "." || name == "/" {
		name = volume
	}
	if archive {
		name += ".zip"
	}
	return name
}

// DownloadVolumeFile streams a file of a backup (or a directory as a zip with
// archive) to w. The download is audited before any byte is sent.
func (s *Service) DownloadVolumeFile(ctx context.Context, w http.ResponseWriter, backupID uuid.UUID, volume, file string, archive bool) error {
	t, err := s.prepareFiles(ctx, backupID, volume, file)
	if err != nil {
		return err
	}
	op := agentprotocol.VolFileRead
	if archive {
		op = agentprotocol.VolFileArchive
	}
	ex, resp, err := s.beginFiles(ctx, t, op)
	if err != nil {
		return err
	}
	defer ex.close()
	if resp.Entry == nil {
		return apierr.Internal()
	}
	size := resp.Entry.Size // < 0: unknown (an archive)
	if err := s.recordVolumeDownload(ctx, t, size, archive); err != nil {
		s.log.ErrorContext(ctx, "audit volume_backup.file.download", "backup_id", backupID, "err", err)
		_ = ex.st.Send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: ex.id, Error: "canceled"})
		return apierr.Internal()
	}

	h := w.Header()
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if archive {
		h.Set("Content-Type", "application/zip")
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(downloadName(volume, t.path, archive)))
	w.WriteHeader(http.StatusOK)

	var written int64
	idle := time.NewTimer(s.fsTimeout())
	defer idle.Stop()
	for {
		select {
		case f := <-ex.st.Frames():
			if f.Type != agentprotocol.TypeFSData {
				continue
			}
			var d agentprotocol.FSData
			if f.Into(&d) != nil || d.Error != "" {
				return errVolumeAbort
			}
			if len(d.Data) > 0 {
				if _, err := w.Write(d.Data); err != nil {
					return nil // the client went away; the stream closes with the function
				}
				written += int64(len(d.Data))
				_ = ex.st.Send(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: ex.id, Bytes: len(d.Data)})
			}
			if d.EOF {
				if size >= 0 && written != size {
					return errVolumeAbort // a short body must not look complete
				}
				return nil
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(s.fsTimeout())
		case <-ex.st.Done():
			return errVolumeAbort
		case <-idle.C:
			return errVolumeAbort
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Service) recordVolumeDownload(ctx context.Context, t fileTarget, size int64, archive bool) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionVolumeFileDownload, ResourceType: "volume_backup", ResourceID: t.b.ID.String(), ProjectID: &t.sc.ProjectID,
			Metadata: map[string]any{
				"service": t.sc.Service.Slug, "volume": t.volume, "path": t.path, "archive": archive, "size": size,
				"snapshot": strings.TrimSpace(t.b.SnapshotID),
			},
		})
	})
}
