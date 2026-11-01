package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The file browser relays file operations to the agent as fs_* stream frames.
// Paths are absolute paths inside the replica container; the agent reads and
// writes through the Docker API, so nothing outside the container is reachable.

const (
	maxPathLen = 4096
	maxNameLen = 255
)

// cleanPath validates and normalizes a container path.
func cleanPath(p string) (string, error) {
	switch {
	case p == "":
		return "", apierr.Unprocessable("invalid_path", "path is required")
	case len(p) > maxPathLen:
		return "", apierr.Unprocessable("invalid_path", "path is too long")
	case strings.ContainsRune(p, 0):
		return "", apierr.Unprocessable("invalid_path", "path contains a NUL byte")
	case !strings.HasPrefix(p, "/"):
		return "", apierr.Unprocessable("invalid_path", "path must be absolute")
	}
	return path.Clean(p), nil
}

// validName checks the file name of an upload: one path element.
func validName(n string) error {
	switch {
	case n == "" || n == "." || n == "..":
		return apierr.Unprocessable("invalid_name", "name must be a file name")
	case len(n) > maxNameLen:
		return apierr.Unprocessable("invalid_name", "name is too long")
	case strings.ContainsAny(n, "/\x00"):
		return apierr.Unprocessable("invalid_name", "name must not contain '/' or NUL")
	}
	return nil
}

// fsError converts an agent's file error into an API error.
func fsError(resp agentprotocol.FSResponse) error {
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
	case agentprotocol.FSErrNoReplica:
		return apierr.Conflict(CodeReplicaNotRunning, msg)
	case agentprotocol.FSErrPermission:
		return apierr.Unprocessable("file_permission_denied", msg)
	}
	return apierr.Unprocessable("file_operation_failed", msg)
}

// exchange is one file operation in flight: the stream, and the first response.
type exchange struct {
	id string
	st Stream
}

// close ends the exchange. Telling the agent first makes it stop a transfer
// that is still running (a no-op for one that finished).
func (ex *exchange) close() {
	_ = ex.st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: ex.id})
	ex.st.Close()
}

// begin opens a stream, sends the request and waits for the agent's first
// response. A response carrying an error is returned as an API error.
func (s *Service) begin(ctx context.Context, t target, req agentprotocol.FSRequest) (*exchange, agentprotocol.FSResponse, error) {
	id := uuid.NewString()
	st, err := s.agents.OpenStream(*t.ServerID, id)
	if err != nil {
		return nil, agentprotocol.FSResponse{}, apierr.Conflict(CodeServerOffline, "the server is offline")
	}
	req.RequestID, req.ServiceID = id, t.ServiceID.String()
	if err := st.Send(agentprotocol.TypeFSRequest, req); err != nil {
		st.Close()
		return nil, agentprotocol.FSResponse{}, apierr.Conflict(CodeServerOffline, "the server is offline")
	}
	resp, err := s.nextResponse(ctx, st)
	if err != nil {
		st.Close()
		return nil, agentprotocol.FSResponse{}, err
	}
	if resp.Error != "" {
		st.Close()
		return nil, resp, fsError(resp)
	}
	return &exchange{id: id, st: st}, resp, nil
}

// nextResponse waits for the next fs_response on the stream.
func (s *Service) nextResponse(ctx context.Context, st Stream) (agentprotocol.FSResponse, error) {
	timer := time.NewTimer(s.fsTimeout)
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
			return agentprotocol.FSResponse{}, apierr.Conflict(CodeServerOffline, "the server went offline during the operation")
		case <-timer.C:
			return agentprotocol.FSResponse{}, apierr.New(http.StatusGatewayTimeout, "agent_timeout", "the server did not answer in time")
		case <-ctx.Done():
			return agentprotocol.FSResponse{}, ctx.Err()
		}
	}
}

// ListFiles lists a directory of a replica container. process "" is the
// default process; replica counts within the process.
func (s *Service) ListFiles(ctx context.Context, serviceID uuid.UUID, process string, replica int, dir string) (FileListing, error) {
	t, _, err := s.authorize(ctx, serviceID)
	if err != nil {
		return FileListing{}, err
	}
	if err := t.requireRunning(process, replica); err != nil {
		return FileListing{}, err
	}
	p, err := cleanPath(dir)
	if err != nil {
		return FileListing{}, err
	}
	pol, err := s.fileWritePolicy(ctx, t)
	if err != nil {
		return FileListing{}, err
	}
	ex, resp, err := s.begin(ctx, t, agentprotocol.FSRequest{Op: agentprotocol.FSList, Replica: replica, Process: t.agentProcess(process), Path: p})
	if err != nil {
		return FileListing{}, err
	}
	defer ex.close()
	out := FileListing{
		Path: resp.Path, Replica: replica, Entries: make([]FileEntry, 0, len(resp.Entries)), Truncated: resp.Truncated,
		Writable: pol.Allowed, ReadOnlyReason: pol.Reason,
	}
	if out.Path == "" {
		out.Path = p
	}
	for _, e := range resp.Entries {
		fe := FileEntry{Name: e.Name, Type: e.Type, Size: e.Size, Mode: e.Mode, LinkTarget: e.LinkTarget}
		if e.MTime > 0 {
			ts := time.Unix(e.MTime, 0).UTC()
			fe.ModifiedAt = &ts
		}
		out.Entries = append(out.Entries, fe)
	}
	return out, nil
}

// Download streams a file of a replica container to w. With preview it reads
// at most PreviewLimit bytes (413 beyond). The read is audited before any byte
// is sent.
func (s *Service) Download(ctx context.Context, w http.ResponseWriter, serviceID uuid.UUID, process string, replica int, file string, preview bool) error {
	t, _, err := s.authorize(ctx, serviceID)
	if err != nil {
		return err
	}
	if err := t.requireRunning(process, replica); err != nil {
		return err
	}
	p, err := cleanPath(file)
	if err != nil {
		return err
	}
	req := agentprotocol.FSRequest{Op: agentprotocol.FSRead, Replica: replica, Process: t.agentProcess(process), Path: p}
	if preview {
		req.MaxBytes = PreviewLimit
	}
	ex, resp, err := s.begin(ctx, t, req)
	if err != nil {
		return err
	}
	defer ex.close()
	if resp.Entry == nil {
		return apierr.Internal()
	}
	if err := s.st.record(ctx, audit.Entry{
		Action: ActionFileRead, ResourceType: "service", ResourceID: t.ServiceID.String(), ProjectID: &t.ProjectID,
		Metadata: map[string]any{"path": resp.Path, "process": process, "replica": replica, "size": resp.Entry.Size, "preview": preview, "production": t.Production},
	}); err != nil {
		s.log.ErrorContext(ctx, "audit file.read", "service_id", t.ServiceID, "err", err)
		_ = ex.st.Send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: ex.id, Error: "canceled"})
		return apierr.Internal()
	}

	h := w.Header()
	h.Set("Content-Length", strconv.FormatInt(resp.Entry.Size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if preview {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Disposition", "inline")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(resp.Entry.Name))
	}
	w.WriteHeader(http.StatusOK)

	var written int64
	idle := time.NewTimer(s.fsTimeout)
	defer idle.Stop()
	for {
		select {
		case f := <-ex.st.Frames():
			if f.Type != agentprotocol.TypeFSData {
				continue
			}
			var d agentprotocol.FSData
			if f.Into(&d) != nil {
				return errAbort
			}
			if d.Error != "" {
				return errAbort
			}
			// The agent never sends more than FSChunk at a time, nor more than the
			// size it announced: a bigger frame or an endless body is not ours.
			if len(d.Data) > agentprotocol.FSChunk || written+int64(len(d.Data)) > resp.Entry.Size {
				return errAbort
			}
			if len(d.Data) > 0 {
				if _, err := w.Write(d.Data); err != nil {
					return nil // the client went away; the stream closes with the function
				}
				written += int64(len(d.Data))
				_ = ex.st.Send(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: ex.id, Bytes: len(d.Data)})
			}
			if d.EOF {
				if written != resp.Entry.Size {
					return errAbort // a short body must not look complete
				}
				return nil
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(s.fsTimeout)
		case <-ex.st.Done():
			return errAbort
		case <-idle.C:
			return errAbort
		case <-ctx.Done():
			return nil
		}
	}
}

// errAbort means the response already started: the handler aborts the
// connection so the client sees a truncated download as an error.
var errAbort = errors.New("transfer aborted")

// Upload stores one file in a directory of a replica container. size must be
// the exact body length. Uploads are refused in production unless the
// project's admin allowed writes.
func (s *Service) Upload(ctx context.Context, serviceID uuid.UUID, process string, replica int, dir, name string, size int64, body io.Reader) (UploadResult, error) {
	t, _, err := s.authorize(ctx, serviceID)
	if err != nil {
		return UploadResult{}, err
	}
	if err := t.requireRunning(process, replica); err != nil {
		return UploadResult{}, err
	}
	pol, err := s.fileWritePolicy(ctx, t)
	if err != nil {
		return UploadResult{}, err
	}
	if !pol.Allowed {
		return UploadResult{}, apierr.New(http.StatusForbidden, CodeProductionReadOnly, pol.Reason)
	}
	d, err := cleanPath(dir)
	if err != nil {
		return UploadResult{}, err
	}
	if err := validName(name); err != nil {
		return UploadResult{}, err
	}
	if size < 0 {
		return UploadResult{}, apierr.BadRequest("length_required", "the upload needs a Content-Length")
	}
	if size > agentprotocol.MaxUploadBytes {
		return UploadResult{}, apierr.New(http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("uploads are limited to %d MB", agentprotocol.MaxUploadBytes>>20))
	}
	dest := path.Join(d, name)
	ex, resp, err := s.begin(ctx, t, agentprotocol.FSRequest{Op: agentprotocol.FSWrite, Replica: replica, Process: t.agentProcess(process), Path: dest, Size: size})
	if err != nil {
		return UploadResult{}, err
	}
	defer ex.close()
	if resp.Stage != agentprotocol.FSReady {
		return UploadResult{}, apierr.Internal()
	}
	if err := s.st.record(ctx, audit.Entry{
		Action: ActionFileUpload, ResourceType: "service", ResourceID: t.ServiceID.String(), ProjectID: &t.ProjectID,
		Metadata: map[string]any{"path": resp.Path, "process": process, "replica": replica, "size": size, "production": t.Production},
	}); err != nil {
		s.log.ErrorContext(ctx, "audit file.upload", "service_id", t.ServiceID, "err", err)
		_ = ex.st.Send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: ex.id, Error: "canceled"})
		return UploadResult{}, apierr.Internal()
	}

	// The agent acknowledges every stored chunk (stream_ack) and finishes with
	// a final fs_response.
	win := agentprotocol.NewWindow()
	defer win.Close()
	final := make(chan agentprotocol.FSResponse, 1)
	go func() {
		for {
			select {
			case f := <-ex.st.Frames():
				switch f.Type {
				case agentprotocol.TypeStreamAck:
					var a agentprotocol.StreamAck
					if f.Into(&a) == nil {
						win.Ack(a.Bytes)
					}
				case agentprotocol.TypeFSResponse:
					var r agentprotocol.FSResponse
					if f.Into(&r) == nil {
						final <- r
						return
					}
				}
			case <-ex.st.Done():
				final <- agentprotocol.FSResponse{Error: "the server went offline during the upload", Code: agentprotocol.FSErrFailed}
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	buf := make([]byte, agentprotocol.FSChunk)
	var sent int64
	for sent < size {
		n, rerr := io.ReadFull(body, buf[:int(min(int64(len(buf)), size-sent))])
		if n > 0 {
			if err := win.Acquire(ctx, n); err != nil {
				return UploadResult{}, apierr.Conflict("upload_interrupted", "the upload was interrupted")
			}
			if err := ex.st.Send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: ex.id, Data: append([]byte(nil), buf[:n]...)}); err != nil {
				return UploadResult{}, apierr.Conflict(CodeServerOffline, "the server went offline during the upload")
			}
			sent += int64(n)
		}
		if rerr != nil {
			break
		}
	}
	if sent != size {
		_ = ex.st.Send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: ex.id, Error: "short body"})
		return UploadResult{}, apierr.BadRequest("short_body", "the request body is shorter than its Content-Length")
	}
	if err := ex.st.Send(agentprotocol.TypeFSData, agentprotocol.FSData{RequestID: ex.id, EOF: true}); err != nil {
		return UploadResult{}, apierr.Conflict(CodeServerOffline, "the server went offline during the upload")
	}
	select {
	case r := <-final:
		if r.Error != "" {
			return UploadResult{}, fsError(r)
		}
		return UploadResult{Path: r.Path, Size: size}, nil
	case <-time.After(s.fsTimeout):
		return UploadResult{}, apierr.New(http.StatusGatewayTimeout, "agent_timeout", "the server did not confirm the upload in time")
	case <-ctx.Done():
		return UploadResult{}, ctx.Err()
	}
}
