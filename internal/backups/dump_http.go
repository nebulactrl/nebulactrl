package backups

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type dumpRestoreOutput struct{ Body DumpRestoreView }

type dumpRestoreListOutput struct {
	Body struct {
		Items []DumpRestoreView `json:"items"`
	}
}

type dumpRestoreIDInput struct {
	ID uuid.UUID `path:"restoreId"`
}

type createDumpRestoreInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Mode          string     `json:"mode,omitempty" enum:"new,in_place" default:"new" doc:"new: restore into a new sibling database (default, the original is untouched). in_place: replace this database's data after a safety backup."`
		Name          string     `json:"name,omitempty" doc:"Name of the new database (mode new)."`
		ServerID      *uuid.UUID `json:"serverId,omitempty" doc:"Server for the new database (mode new); default the source's server."`
		ConfirmSlug   string     `json:"confirmSlug,omitempty" doc:"In-place restores: the database's slug."`
		DestinationID *uuid.UUID `json:"destinationId,omitempty" doc:"In-place restores: the backup destination of the safety backup; default the destination of the database's backup schedule."`
		FileName      string     `json:"fileName,omitempty" doc:"The name of the file about to be uploaded (shown in the history)."`
		SizeBytes     int64      `json:"sizeBytes,omitempty" minimum:"0" doc:"The size of the file about to be uploaded. A file above the limit is refused before anything is created."`
	}
}

// RegisterDump attaches the restore-from-a-dump-file endpoints. The upload
// itself is a raw route (MountAPI), documented in prose on createDumpRestore.
func RegisterDump(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"backups"}

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createDumpRestore", Method: http.MethodPost, Path: "/services/{serviceId}/dump-restores", Tags: tag,
		Summary: "Restore a database from a dump file", DefaultStatus: http.StatusAccepted,
		Description: "Prepares a restore of a managed database from a file you upload: a `pg_dump` file (custom `-Fc`, tar `-Ft` or plain SQL, optionally gzipped), a `mysqldump`/`mariadb-dump` SQL file (optionally gzipped) or a `mongodump --archive` file. " +
			"mode `new` (default) creates a sibling database and deploys it empty; mode `in_place` first takes a safety backup (needs `confirmSlug`) and then replaces this database's data. " +
			"Poll the restore until its status is `awaiting_upload`, then send the file: `PUT /api/v1/dump-restores/{restoreId}/upload` with the file as the raw request body " +
			"(`Content-Type: application/octet-stream`, `Content-Length` required, optional `?fileName=`). It answers 202 once the agent has the whole file (status `restoring`); " +
			"poll `GET /dump-restores/{restoreId}` for `succeeded` or `failed`. The control plane stores nothing: the bytes stream through it to the agent. " +
			"The format is detected from the file's first bytes (422 `unsupported_dump_format` when it does not fit the engine); 413 `file_too_large` above the limit (`maxUploadBytes`, default 5 GiB).",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge),
	}), policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite), func(ctx context.Context, in *createDumpRestoreInput) (*dumpRestoreOutput, error) {
		v, err := svc.CreateDumpRestore(ctx, in.ID, DumpRestoreInput{
			Mode: in.Body.Mode, Name: in.Body.Name, ServerID: in.Body.ServerID, ConfirmSlug: in.Body.ConfirmSlug,
			DestinationID: in.Body.DestinationID, FileName: in.Body.FileName, SizeBytes: in.Body.SizeBytes,
		})
		if err != nil {
			return nil, err
		}
		return &dumpRestoreOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDumpRestore", Method: http.MethodGet, Path: "/dump-restores/{restoreId}", Tags: tag,
		Summary: "Get a restore from a dump file", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *dumpRestoreIDInput) (*dumpRestoreOutput, error) {
		v, err := svc.GetDumpRestore(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &dumpRestoreOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDumpRestores", Method: http.MethodGet, Path: "/services/{serviceId}/dump-restores", Tags: tag,
		Summary: "List restores from a dump file of a database", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*dumpRestoreListOutput, error) {
		items, err := svc.ListDumpRestores(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &dumpRestoreListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "cancelDumpRestore", Method: http.MethodPost, Path: "/dump-restores/{restoreId}/cancel", Tags: tag,
		Summary:     "Cancel a restore that has not received its file",
		Description: "Only while the restore is provisioning, taking its safety backup or awaiting its file. The new database it created stays; delete it like any service.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *dumpRestoreIDInput) (*dumpRestoreOutput, error) {
		v, err := svc.CancelDumpRestore(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &dumpRestoreOutput{Body: v}, nil
	})
}

func (s *Service) mountDump(r chi.Router) {
	r.Put("/dump-restores/{restoreId}/upload", s.serveDumpUpload)
}

func (s *Service) serveDumpUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "restoreId"))
	if err != nil {
		writeVolumeError(w, apierr.NotFound("restore"))
		return
	}
	fail := func(err error) {
		// The body may be unread: do not keep the connection for the next request.
		w.Header().Set("Connection", "close")
		writeVolumeError(w, err)
	}
	if r.ContentLength < 0 {
		fail(apierr.New(http.StatusLengthRequired, "length_required", "the upload needs a Content-Length"))
		return
	}
	if r.ContentLength > s.dumpLimit() {
		fail(tooLarge(s.dumpLimit()))
		return
	}
	rc := http.NewResponseController(w)
	v, err := s.UploadDump(r.Context(), id, DumpUpload{
		FileName: r.URL.Query().Get("fileName"), Size: r.ContentLength,
		Body: http.MaxBytesReader(w, r.Body, s.dumpLimit()),
		ExtendRead: func(d time.Duration) {
			_ = rc.SetReadDeadline(time.Now().Add(d))
		},
	})
	if err != nil {
		if _, ok := apierr.As(err); !ok {
			s.log.ErrorContext(r.Context(), "upload dump", "restore_id", id, "err", err)
		}
		fail(err)
		return
	}
	// Reads are over; the response must not be cut by the read deadline.
	_ = rc.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(body)
}
