package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type infoOutput struct{ Body InfoView }

type listFilesInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Process string    `query:"process" maxLength:"31" doc:"Services with processes only: the process whose replica to read (default: the default process). Replica counts within it."`
	Replica int       `query:"replica" minimum:"0" maximum:"999" default:"0" doc:"Replica index within the process."`
	Path    string    `query:"path" default:"/" maxLength:"4096" doc:"Absolute directory path inside the container."`
}

type listFilesOutput struct{ Body FileListing }

type projectIDInput struct {
	ID uuid.UUID `path:"projectId"`
}

type settingsOutput struct{ Body SettingsView }

type putSettingsInput struct {
	ID   uuid.UUID `path:"projectId"`
	Body SettingsView
}

// Register attaches the JSON endpoints. The WebSocket, the file download and
// the file upload are raw routes (MountAPI), documented here in prose.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"terminal"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceTerminal", Method: http.MethodGet, Path: "/services/{serviceId}/terminal", Tags: tag,
		Summary: "Terminal and file browser availability",
		Description: "What the caller can do with the service's web terminal and file browser: its replicas, whether a terminal can be opened now, " +
			"whether production needs a confirmation and whether the file browser may write. Requires the Owner, Admin or Developer role and " +
			"`deployments:write`; a Viewer gets 403.\n\n" +
			"**Terminal.** `GET /services/{serviceId}/terminal/ws` upgrades to a WebSocket (browser session cookie, same origin). The first client " +
			"message is JSON `{\"type\":\"open\",\"process\":\"worker\",\"replica\":0,\"command\":[…],\"cols\":80,\"rows\":24,\"confirmProduction\":false}` (`process` is optional: services with several processes, default the default process; `replica` counts within it); binary messages are terminal " +
			"input/output; JSON `{\"type\":\"resize\",\"cols\":…,\"rows\":…}` resizes and `{\"type\":\"close\"}` ends the session. The server answers " +
			"`opened`, `idle_warning`, `exit`, `error` and `closed` JSON messages. Production services need `confirmProduction: true`. A principal may " +
			"hold 3 sessions; an idle session (no input for 15 minutes) is closed. Sessions are audited (`terminal.open`, `terminal.close`); their I/O is never recorded.\n\n" +
			"**Files.** `GET /services/{serviceId}/files/content?process=&replica=&path=&preview=` streams a file (`preview=true` returns at most 1 MiB as text/plain); " +
			"`POST /services/{serviceId}/files/upload?process=&replica=&path=<dir>&name=<file>` stores the request body (Content-Length required, 50 MB at most). " +
			"Uploads are refused in production unless an admin allowed writes for the project.",
		Errors: errs,
	}, policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*infoOutput, error) {
		v, err := svc.Info(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &infoOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServiceFiles", Method: http.MethodGet, Path: "/services/{serviceId}/files", Tags: tag,
		Summary: "List a directory inside a replica container",
		Description: "Lists the immediate children of an absolute path (directories first, at most 2000 entries). The listing reads the container's own " +
			"filesystem through the Docker API; nothing on the host is reachable. Requires the Owner, Admin or Developer role.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsRead, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *listFilesInput) (*listFilesOutput, error) {
		v, err := svc.ListFiles(ctx, in.ID, in.Process, in.Replica, in.Path)
		if err != nil {
			return nil, err
		}
		return &listFilesOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTerminalSettings", Method: http.MethodGet, Path: "/projects/{projectId}/terminal-settings", Tags: tag,
		Summary: "A project's terminal settings", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *projectIDInput) (*settingsOutput, error) {
		v, err := svc.Settings(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setTerminalSettings", Method: http.MethodPut, Path: "/projects/{projectId}/terminal-settings", Tags: tag,
		Summary: "Change a project's terminal settings",
		Description: "Admins and Owners only. `allowProductionFileWrites` lets the file browser upload files in the project's production environments " +
			"(default off). Audited.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *putSettingsInput) (*settingsOutput, error) {
		v, err := svc.SetSettings(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: v}, nil
	})

	registerHostShell(h, svc)
}

// MountAPI attaches the raw routes under /api/v1 (authenticated and CSRF
// checked by the API router): the terminal WebSocket and the file transfer.
func (s *Service) MountAPI(r chi.Router) {
	r.Get("/services/{serviceId}/terminal/ws", s.serveWS)
	r.Get("/services/{serviceId}/files/content", s.serveContent)
	r.Post("/services/{serviceId}/files/upload", s.serveUpload)
	r.Get("/servers/{serverId}/shell/ws", s.serveHostWS)
}

// replicaParam parses the optional replica query parameter.
func replicaParam(r *http.Request) (int, error) {
	v := r.URL.Query().Get("replica")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > MaxReplica {
		return 0, apierr.Unprocessable("invalid_replica", "replica must be between 0 and 999")
	}
	return n, nil
}

// processParam reads the optional process query parameter ("" = the default
// process). A name that is none of the service's processes is refused later,
// against the service, as not found.
func processParam(r *http.Request) string {
	return r.URL.Query().Get("process")
}

func serviceParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "serviceId"))
	if err != nil {
		return uuid.Nil, apierr.NotFound("service")
	}
	return id, nil
}

func (s *Service) serveContent(w http.ResponseWriter, r *http.Request) {
	id, err := serviceParam(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	replica, err := replicaParam(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	q := r.URL.Query()
	err = s.Download(r.Context(), w, id, processParam(r), replica, q.Get("path"), q.Get("preview") == "true")
	if errors.Is(err, errAbort) {
		// The response started and then broke: abort the connection so the
		// client sees a failed download, not a complete short file.
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		writeHTTPError(w, err)
	}
}

func (s *Service) serveUpload(w http.ResponseWriter, r *http.Request) {
	id, err := serviceParam(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	replica, err := replicaParam(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if r.ContentLength < 0 {
		apierr.Write(w, apierr.New(http.StatusLengthRequired, "length_required", "the upload needs a Content-Length"))
		return
	}
	if r.ContentLength > agentprotocol.MaxUploadBytes {
		apierr.Write(w, apierr.New(http.StatusRequestEntityTooLarge, "file_too_large", "uploads are limited to 50 MB"))
		return
	}
	q := r.URL.Query()
	body := http.MaxBytesReader(w, r.Body, agentprotocol.MaxUploadBytes)
	res, err := s.Upload(r.Context(), id, processParam(r), replica, q.Get("path"), q.Get("name"), r.ContentLength, body)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(res)
}
